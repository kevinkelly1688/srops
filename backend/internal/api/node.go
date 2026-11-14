// @author: kevinkelly
// @license: Apache Licence
// @file: node.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
	"starrocks-ops/internal/sshx"
	"starrocks-ops/internal/store"
)

// 节点管理：启停、重启（SSH 执行安装目录下的脚本）；下线、移除、黑名单（SQL）；查看节点日志（SSH tail）。

// 轮询间隔与等待上限；测试中会调小。
var (
	nodePollEvery = 2 * time.Second
	nodeStartWait = 180 * time.Second
	nodeStopWait  = 180 * time.Second
)

var nodeKindRe = regexp.MustCompile(`^(fe|be|cn)$`)
var nodeKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]{0,252}:\d{1,5}$`)

// nodeKey 是节点在接口路径中的标识：FE 为 host:edit_log_port，BE / CN 为 host:heartbeat_port（与 ALTER SYSTEM 语句中的写法一致）。
func nodeKey(n model.Node) string {
	if n.Type == "fe" {
		return fmt.Sprintf("%s:%d", n.Host, n.EditLog)
	}
	return fmt.Sprintf("%s:%d", n.Host, n.Port)
}

func nodeDir(c model.Cluster, kind string) string {
	if kind == "fe" {
		return c.Dirs.FE
	}
	return c.Dirs.BE
}

// sshTarget 返回连接集群主机所需的 SSH 参数；必填信息未完善时报 409。
func (s *Server) sshTarget(c model.Cluster, host string) (sshx.Target, error) {
	if miss := c.Missing(); len(miss) > 0 {
		return sshx.Target{}, conflict("该集群的运维必填信息未完善（" + strings.Join(miss, "、") + "），请先在「集群接入」中补全")
	}
	if !hostRe.MatchString(host) {
		return sshx.Target{}, bad("主机地址不合法")
	}
	t := sshx.Target{Host: host, Port: c.SSH.Port, User: c.SSH.User}
	if c.SSH.Auth == "password" {
		pw, err := s.Box.Decrypt(c.SSH.PasswordEnc)
		if err != nil {
			return t, err
		}
		t.Password = pw
	}
	if !dirRe.MatchString(c.Dirs.FE) || !dirRe.MatchString(c.Dirs.BE) {
		return t, bad("部署目录不合法，请在「集群接入」中修正")
	}
	return t, nil
}

func startCmd(dir, kind string) string {
	return fmt.Sprintf("cd %s && bin/start_%s.sh --daemon", dir, kind)
}
func stopCmd(dir, kind string, graceful bool) string {
	g := ""
	if graceful {
		g = " -g"
	}
	return fmt.Sprintf("cd %s && bin/stop_%s.sh%s", dir, kind, g)
}
func statusCmd(dir, kind string) string {
	return fmt.Sprintf("if [ -f %[1]s/bin/%[2]s.pid ] && kill -0 $(cat %[1]s/bin/%[2]s.pid) 2>/dev/null; then echo running; else echo stopped; fi", dir, kind)
}

// verAtLeast 判断版本号 v（如 3.5.6）是否 ≥ major.minor。
func verAtLeast(v string, major, minor int) bool {
	parts := strings.SplitN(strings.TrimLeft(v, "vV"), ".", 3)
	if len(parts) < 2 {
		return false
	}
	a, _ := strconv.Atoi(parts[0])
	b, _ := strconv.Atoi(strings.TrimFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	return a > major || (a == major && b >= minor)
}

// ---------- 找节点 / 并发控制 ----------

type nodeCtx struct {
	c    model.Cluster
	n    model.Node
	all  []model.Node
	kind string
	key  string
}

// resolveNode 读取集群当前节点并按路径中的 kind / node 找到目标；失败时已写好响应。
func (s *Server) resolveNode(w http.ResponseWriter, r *http.Request, needSSH bool) (nodeCtx, bool) {
	var nc nodeCtx
	nc.kind, nc.key = r.PathValue("kind"), r.PathValue("node")
	if !nodeKindRe.MatchString(nc.kind) || !nodeKeyRe.MatchString(nc.key) {
		fail(w, http.StatusBadRequest, "节点标识不合法")
		return nc, false
	}
	c, found := s.cluster(w, r)
	if !found {
		return nc, false
	}
	nc.c = c
	if needSSH {
		if miss := c.Missing(); len(miss) > 0 {
			fail(w, http.StatusConflict, "该集群的运维必填信息未完善（"+strings.Join(miss, "、")+"），请先在「集群接入」中补全")
			return nc, false
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cl, err := s.Mgr.Client(ctx, c)
	if err != nil {
		fail(w, http.StatusBadGateway, "连接集群失败："+err.Error())
		return nc, false
	}
	nodes, warns := cl.Nodes(ctx, c.Mode)
	if len(nodes) == 0 {
		fail(w, http.StatusBadGateway, "读取节点列表失败："+strings.Join(warns, "；"))
		return nc, false
	}
	nc.all = nodes
	for _, n := range nodes {
		if n.Type == nc.kind && nodeKey(n) == nc.key {
			nc.n = n
		}
	}
	if nc.n.Host == "" {
		fail(w, http.StatusNotFound, "集群中没有该节点："+strings.ToUpper(nc.kind)+" "+nc.key)
		return nc, false
	}
	return nc, true
}

func (s *Server) lockNode(cluster, kind, key string) bool {
	k := cluster + "|" + kind + "|" + key
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.nodeBusy == nil {
		s.nodeBusy = map[string]bool{}
	}
	if s.nodeBusy[k] {
		return false
	}
	s.nodeBusy[k] = true
	return true
}

func (s *Server) unlockNode(cluster, kind, key string) {
	s.taskMu.Lock()
	delete(s.nodeBusy, cluster+"|"+kind+"|"+key)
	s.taskMu.Unlock()
}

// quorumAfterStop：停止 FE 后，存活的 Leader / Follower 是否仍占多数。
func quorumAfterStop(all []model.Node, n model.Node) (alive, total int, ok bool) {
	for _, x := range all {
		if x.Type != "fe" || x.Role == "Observer" {
			continue
		}
		total++
		if x.Alive && !(x.Host == n.Host && x.EditLog == n.EditLog) {
			alive++
		}
	}
	if n.Type != "fe" || n.Role == "Observer" || !n.Alive {
		return alive, total, true
	}
	return alive, total, alive > total/2
}

// ---------- 启动 / 停止 / 重启（SSH） ----------

type nodeOpIn struct {
	Graceful *bool `json:"graceful"` // nil = 按版本默认（BE / CN 3.4+ 优雅退出）
	Force    bool  `json:"force"`    // 忽略 FE 多数派检查
}

var nodeVerb = map[string]string{"start": "启动", "stop": "停止", "restart": "重启"}

func (s *Server) nodeOp(op string) handler {
	code := "node." + op
	return func(w http.ResponseWriter, r *http.Request, u *model.User) {
		var in nodeOpIn
		if r.ContentLength != 0 && !decode(w, r, &in) {
			return
		}
		nc, ok2 := s.resolveNode(w, r, true)
		if !ok2 {
			return
		}
		c, n := nc.c, nc.n
		label := fmt.Sprintf("%s %s", strings.ToUpper(n.Type), nodeKey(n))
		if op != "start" && n.Type == "fe" {
			if alive, total, okq := quorumAfterStop(nc.all, n); !okq && !in.Force {
				fail(w, http.StatusConflict, fmt.Sprintf("%s后，存活的 Leader / Follower 只剩 %d / %d，不足多数，集群将无法选主。确需操作请勾选「强制」", nodeVerb[op], alive, total))
				return
			}
		}
		graceful := n.Type != "fe" && verAtLeast(n.Version, 3, 4)
		if in.Graceful != nil {
			graceful = *in.Graceful
		}
		if !s.lockNode(c.ID, n.Type, nodeKey(n)) {
			fail(w, http.StatusConflict, label+" 正在执行其他操作，请等待完成")
			return
		}
		t := s.newTask(code, c.ID, nodeVerb[op]+" "+label, u.Login, map[string]string{"kind": n.Type, "node": nodeKey(n), "host": n.Host})
		s.auditSQL(r, u, code, fmt.Sprintf("%s %s（任务 %s 已开始）", nodeVerb[op], label, t.ID()), "", nil, c.ID)
		ip := clientIP(r)
		go func() {
			defer s.unlockNode(c.ID, n.Type, nodeKey(n))
			err := s.runNodeOp(t, c, n, op, graceful)
			res := "成功"
			if err != nil {
				res = "失败：" + err.Error()
			}
			s.Store.Audit(store.AuditEntry{User: u.Login, IP: ip, Cluster: c.ID, Code: code, Act: fmt.Sprintf("%s %s（任务 %s）", nodeVerb[op], label, t.ID()), Result: res, SQL: nodeSQLLog(c, n, op, graceful)})
			t.Finish(err)
		}()
		ok(w, map[string]any{"ok": true, "taskId": t.ID()})
	}
}

func nodeSQLLog(c model.Cluster, n model.Node, op string, graceful bool) string {
	d := nodeDir(c, n.Type)
	switch op {
	case "start":
		return n.Host + ": " + startCmd(d, n.Type)
	case "stop":
		return n.Host + ": " + stopCmd(d, n.Type, graceful)
	}
	return n.Host + ": " + stopCmd(d, n.Type, graceful) + "\n" + n.Host + ": " + startCmd(d, n.Type)
}

func (s *Server) runNodeOp(t *taskRun, c model.Cluster, n model.Node, op string, graceful bool) error {
	tgt, err := s.sshTarget(c, n.Host)
	if err != nil {
		return err
	}
	t.Mask(tgt.Password)
	ctx, cancel := context.WithTimeout(t.ctx, nodeStartWait+nodeStopWait+time.Minute)
	defer cancel()
	t.Logf("连接 %s@%s:%d …", tgt.User, tgt.Host, tgt.Port)
	sess, err := s.Keys.Dial(ctx, tgt)
	if err != nil {
		return err
	}
	defer sess.Close()
	dir := nodeDir(c, n.Type)
	switch op {
	case "stop":
		t.SetProg(10)
		return s.stopNode(ctx, t, sess, dir, n, graceful)
	case "start":
		t.SetProg(10)
		return s.startNode(ctx, t, c, sess, dir, n)
	}
	if err := s.stopNode(ctx, t, sess, dir, n, graceful); err != nil {
		return err
	}
	t.SetProg(50)
	return s.startNode(ctx, t, c, sess, dir, n)
}

// runLogged 执行命令并把命令与输出写入任务日志；退出码非 0 视为失败。
func runLogged(ctx context.Context, t *taskRun, sess *sshx.Session, cmd string) (sshx.Result, error) {
	t.Logf("$ %s", cmd)
	res, err := sess.Run(ctx, cmd)
	if err != nil {
		return res, err
	}
	if o := strings.TrimSpace(res.Stdout); o != "" {
		t.Logf("%s", o)
	}
	if o := strings.TrimSpace(res.Stderr); o != "" {
		t.Logf("%s", o)
	}
	if res.Code != 0 {
		return res, fmt.Errorf("命令退出码 %d", res.Code)
	}
	return res, nil
}

func procRunning(ctx context.Context, sess *sshx.Session, dir, kind string) (bool, error) {
	res, err := sess.Run(ctx, statusCmd(dir, kind))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(res.Stdout) == "running", nil
}

func (s *Server) stopNode(ctx context.Context, t *taskRun, sess *sshx.Session, dir string, n model.Node, graceful bool) error {
	run, err := procRunning(ctx, sess, dir, n.Type)
	if err != nil {
		return err
	}
	if !run {
		t.Logf("进程未在运行，无需停止")
		return nil
	}
	if graceful {
		t.Logf("使用优雅退出（-g）：等待运行中的查询完成，可能需要几十秒到几分钟")
	}
	if _, err := runLogged(ctx, t, sess, stopCmd(dir, n.Type, graceful)); err != nil {
		return err
	}
	deadline := time.Now().Add(nodeStopWait)
	for {
		run, err := procRunning(ctx, sess, dir, n.Type)
		if err != nil {
			return err
		}
		if !run {
			t.Logf("进程已退出")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %d 秒后进程仍未退出", int(nodeStopWait.Seconds()))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nodePollEvery):
		}
	}
}

// nodeAlive 通过 SQL 判断节点是否已加入并存活；known=false 表示 SQL 暂时读不到。
func (s *Server) nodeAlive(ctx context.Context, c model.Cluster, n model.Node) (alive, known bool) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cl, err := s.Mgr.Client(cctx, c)
	if err != nil {
		return false, false
	}
	nodes, _ := cl.Nodes(cctx, c.Mode)
	for _, x := range nodes {
		if x.Type == n.Type && nodeKey(x) == nodeKey(n) {
			return x.Alive, true
		}
	}
	return false, len(nodes) > 0
}

func (s *Server) startNode(ctx context.Context, t *taskRun, c model.Cluster, sess *sshx.Session, dir string, n model.Node) error {
	run, err := procRunning(ctx, sess, dir, n.Type)
	if err != nil {
		return err
	}
	if run {
		t.Logf("进程已在运行，跳过启动脚本")
	} else if _, err := runLogged(ctx, t, sess, startCmd(dir, n.Type)); err != nil {
		return err
	}
	t.SetProg(60)
	t.Logf("等待节点上线（最长 %d 秒）…", int(nodeStartWait.Seconds()))
	deadline := time.Now().Add(nodeStartWait)
	lastKnown := false
	for {
		run, err := procRunning(ctx, sess, dir, n.Type)
		if err != nil {
			return err
		}
		if run {
			alive, known := s.nodeAlive(ctx, c, n)
			lastKnown = known
			if alive {
				t.Logf("进程运行中，SHOW 显示节点 Alive = true")
				return nil
			}
		}
		if time.Now().After(deadline) {
			switch {
			case !run:
				return fmt.Errorf("启动脚本已执行，但进程未保持运行，请查看节点日志")
			case !lastKnown:
				return fmt.Errorf("进程在运行，但无法通过 SQL 确认节点状态，请稍后在「集群架构」中核对")
			}
			return fmt.Errorf("进程在运行，但 %d 秒内节点仍未显示为存活（Alive = false），请查看节点日志", int(nodeStartWait.Seconds()))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nodePollEvery):
		}
	}
}

// ---------- 下线 / 移除（SQL） ----------

func (s *Server) nodeSQL(op string) handler {
	code := "node." + op
	return func(w http.ResponseWriter, r *http.Request, u *model.User) {
		nc, ok2 := s.resolveNode(w, r, false)
		if !ok2 {
			return
		}
		n := nc.n
		addr := nodeKey(n)
		var stmt string
		switch {
		case op == "decommission" && n.Type == "be":
			stmt = fmt.Sprintf(`ALTER SYSTEM DECOMMISSION BACKEND "%s"`, addr)
		case op == "decommission" && n.Type == "cn", op == "drop" && n.Type == "cn":
			stmt = fmt.Sprintf(`ALTER SYSTEM DROP COMPUTE NODE "%s"`, addr) // CN 无数据，直接移除
		case op == "decommission":
			fail(w, http.StatusBadRequest, "FE 不支持下线，请使用「移除」")
			return
		case n.Type == "be":
			stmt = fmt.Sprintf(`ALTER SYSTEM DROP BACKEND "%s"`, addr)
		default: // FE
			if n.Role == "Leader" {
				fail(w, http.StatusBadRequest, "不能移除 Leader FE：请先让其他 Follower 接任（停止 Leader）后再移除")
				return
			}
			stmt = fmt.Sprintf(`ALTER SYSTEM DROP %s "%s"`, strings.ToUpper(n.Role), addr)
		}
		x, done, ok3 := s.open(w, r, 60*time.Second)
		if !ok3 {
			return
		}
		defer done()
		verb := map[string]string{"decommission": "下线", "drop": "移除"}[op]
		label := fmt.Sprintf("%s %s", strings.ToUpper(n.Type), addr)
		if err := s.exec(r, u, x, code, verb+" "+label, stmt); err != nil {
			sqlFail(w, err)
			return
		}
		ok(w, map[string]any{"ok": true, "sql": stmt + ";"})
	}
}

// ---------- 黑名单 ----------

type blEntry struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Host   string `json:"host"`
	Node   string `json:"node"`
	Reason string `json:"reason"`
	Auto   bool   `json:"auto"`
	By     string `json:"by"`
	At     string `json:"at"`
}

var blStmt = map[string]string{"be": "BACKEND", "cn": "COMPUTE NODE"}

// idMap 读取 SHOW BACKENDS / COMPUTE NODES，返回 kind:id → host:port 以及 host:port → id。
func idMap(ctx context.Context, cl *sr.Client, mode string) (byID map[string]string, byAddr map[string]string) {
	byID, byAddr = map[string]string{}, map[string]string{}
	for _, q := range []struct{ sql, kind, idCol string }{{"SHOW BACKENDS", "be", "BackendId"}, {"SHOW COMPUTE NODES", "cn", "ComputeNodeId"}} {
		if q.kind == "be" && mode == "shared-data" {
			continue
		}
		r, err := cl.Query(ctx, q.sql)
		if err != nil {
			continue
		}
		for i := range r.Data {
			id, addr := r.Get(i, q.idCol), fmt.Sprintf("%s:%s", r.Get(i, "IP"), r.Get(i, "HeartbeatPort"))
			if id != "" {
				byID[q.kind+":"+id], byAddr[q.kind+":"+addr] = addr, id
			}
		}
	}
	return
}

func (s *Server) listNodeBlacklist(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 40*time.Second)
	if !ok2 {
		return
	}
	defer done()
	byID, _ := idMap(x.ctx, x.cl, x.c.Mode)
	saved := map[string]model.NodeBL{}
	s.Store.View(func(st *store.State) {
		for _, b := range st.NodeBL {
			if b.Cluster == x.c.ID {
				saved[b.Kind+":"+b.ID] = b
			}
		}
	})
	out := []blEntry{}
	var warns []string
	for kind, stmt := range blStmt {
		if kind == "be" && x.c.Mode == "shared-data" {
			continue
		}
		rows, err := x.cl.Query(x.ctx, "SHOW "+stmt+" BLACKLIST")
		if err != nil {
			warns = append(warns, "SHOW "+stmt+" BLACKLIST 失败："+err.Error())
			continue
		}
		col := map[string]string{"be": "BackendId", "cn": "ComputeNodeId"}[kind]
		for i := range rows.Data {
			id := rows.Get(i, col)
			e := blEntry{Kind: kind, ID: id, Node: byID[kind+":"+id], Reason: "—", Auto: !strings.EqualFold(rows.Get(i, "AddBlackListType"), "MANUAL"), At: rows.Get(i, "LostConnectionTime")}
			if e.Node != "" {
				e.Host = strings.SplitN(e.Node, ":", 2)[0]
			}
			if b, found := saved[kind+":"+id]; found {
				e.Reason, e.By, e.At = b.Reason, b.By, b.At.Format("2006-01-02 15:04:05")
			}
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].ID < out[j].Kind+out[j].ID })
	ok(w, map[string]any{"items": out, "warnings": warns})
}

func (s *Server) addNodeBlacklist(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		Kind   string `json:"kind"`
		Node   string `json:"node"` // host:heartbeat_port
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Reason = cleanText(in.Reason)
	if (in.Kind != "be" && in.Kind != "cn") || !nodeKeyRe.MatchString(in.Node) || rlen(in.Reason) > 100 {
		fail(w, http.StatusBadRequest, "参数不合法（类型 be / cn，节点 host:端口，原因最多 100 字）")
		return
	}
	x, done, ok2 := s.open(w, r, 40*time.Second)
	if !ok2 {
		return
	}
	defer done()
	_, byAddr := idMap(x.ctx, x.cl, x.c.Mode)
	id := byAddr[in.Kind+":"+in.Node]
	if id == "" {
		fail(w, http.StatusNotFound, "集群中没有该节点："+in.Node)
		return
	}
	stmt := fmt.Sprintf("ADD %s BLACKLIST %s", blStmt[in.Kind], id)
	if err := s.exec(r, u, x, "node.blacklist", "加入黑名单 "+strings.ToUpper(in.Kind)+" "+in.Node+"（"+in.Reason+"）", stmt); err != nil {
		sqlFail(w, err)
		return
	}
	s.Store.Update(func(st *store.State) error {
		st.NodeBL = append(st.NodeBL, model.NodeBL{Cluster: x.c.ID, Kind: in.Kind, ID: id, Host: in.Node, Reason: in.Reason, By: u.Login, At: time.Now()})
		return nil
	})
	ok(w, map[string]any{"ok": true, "id": id})
}

func (s *Server) removeNodeBlacklist(w http.ResponseWriter, r *http.Request, u *model.User) {
	kind, id := r.PathValue("kind"), r.PathValue("bid")
	if (kind != "be" && kind != "cn") || !regexp.MustCompile(`^\d{1,18}$`).MatchString(id) {
		fail(w, http.StatusBadRequest, "参数不合法")
		return
	}
	x, done, ok2 := s.open(w, r, 40*time.Second)
	if !ok2 {
		return
	}
	defer done()
	stmt := fmt.Sprintf("DELETE %s BLACKLIST %s", blStmt[kind], id)
	if err := s.exec(r, u, x, "node.blacklist", "移出黑名单 "+strings.ToUpper(kind)+" #"+id, stmt); err != nil {
		sqlFail(w, err)
		return
	}
	s.Store.Update(func(st *store.State) error {
		var keep []model.NodeBL
		for _, b := range st.NodeBL {
			if !(b.Cluster == x.c.ID && b.Kind == kind && b.ID == id) {
				keep = append(keep, b)
			}
		}
		st.NodeBL = keep
		return nil
	})
	ok(w, map[string]bool{"ok": true})
}

// ---------- 节点日志 ----------

var logFiles = map[string][]string{
	"fe": {"fe.log", "fe.warn.log", "fe.audit.log", "fe.out"},
	"be": {"be.INFO", "be.WARNING", "be.out"},
	"cn": {"cn.INFO", "cn.WARNING", "cn.out"},
}

func (s *Server) nodeLog(w http.ResponseWriter, r *http.Request, u *model.User) {
	nc, ok2 := s.resolveNode(w, r, true)
	if !ok2 {
		return
	}
	q := r.URL.Query()
	file := q.Get("file")
	if file == "" {
		file = logFiles[nc.kind][0]
	}
	if !contains(logFiles[nc.kind], file) {
		fail(w, http.StatusBadRequest, "不支持的日志文件")
		return
	}
	lines, _ := strconv.Atoi(q.Get("lines"))
	if lines <= 0 || lines > 1000 {
		lines = 200
	}
	kw := q.Get("q")
	if len(kw) > 100 || strings.ContainsAny(kw, "\r\n\x00") {
		fail(w, http.StatusBadRequest, "关键字不合法")
		return
	}
	fetch := lines
	if kw != "" {
		fetch = 5000 // 在最近 5000 行中过滤
	}
	tgt, err := s.sshTarget(nc.c, nc.n.Host)
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sess, err := s.Keys.Dial(ctx, tgt)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	defer sess.Close()
	path := nodeDir(nc.c, nc.kind) + "/log/" + file
	cmd := fmt.Sprintf("tail -n %d %s", fetch, path)
	res, err := sess.Run(ctx, cmd)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	if res.Code != 0 {
		fail(w, http.StatusBadGateway, "读取日志失败："+strings.TrimSpace(res.Stderr+res.Stdout))
		return
	}
	all := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if kw != "" {
		var hit []string
		for _, ln := range all {
			if strings.Contains(strings.ToLower(ln), strings.ToLower(kw)) {
				hit = append(hit, ln)
			}
		}
		all = hit
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	text := strings.Join(all, "\n")
	if len(text) > 1<<20 {
		text = text[len(text)-1<<20:]
	}
	s.audit(r, u, nc.c.ID, "node.log", fmt.Sprintf("查看节点日志 %s %s %s", strings.ToUpper(nc.kind), nodeKey(nc.n), file), nil)
	ok(w, map[string]any{"file": file, "files": logFiles[nc.kind], "text": text, "lines": len(all), "path": path})
}
