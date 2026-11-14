// @author: kevinkelly
// @license: Apache Licence
// @file: scaleout.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

// 节点扩容：在已纳管的主机上安装与集群同版本的 FE / BE / CN 并加入集群。
// 配置沿用集群里同类型现有节点的配置文件（仅改 priority_networks），启动前先按官方流程执行 ADD。

type scalePlan struct {
	Version  string          `json:"version"`
	Dir      string          `json:"dir"`      // 新节点的安装目录（沿用集群的部署目录）
	Home     string          `json:"home"`     // 预检用的目录（安装目录的上级）
	JavaHome string          `json:"javaHome"` // 参考节点配置里的 JAVA_HOME
	Helper   string          `json:"helper"`   // Leader FE 的 edit_log 地址
	RefHost  string          `json:"refHost"`
	Packages []model.Package `json:"packages"`
	Hosts    []model.Host    `json:"hosts"` // 可选主机：SSH 通、不在该集群该类型节点里
}

func parentDir(d string) string {
	if i := strings.LastIndex(d, "/"); i > 0 {
		return d[:i]
	}
	return "/"
}

// scaleContext 取得集群当前节点、参考节点及其配置。
func (s *Server) scaleContext(ctx context.Context, c model.Cluster, kind string) (nodes []model.Node, ref model.Node, refConf string, err error) {
	cl, err := s.Mgr.Client(ctx, c)
	if err != nil {
		return nil, ref, "", fmt.Errorf("连接集群失败：%w", err)
	}
	nodes, warns := cl.Nodes(ctx, c.Mode)
	if len(nodes) == 0 {
		return nil, ref, "", fmt.Errorf("读取节点列表失败：%s", strings.Join(warns, "；"))
	}
	for _, n := range nodes {
		if n.Type != kind || !n.Alive {
			continue
		}
		if ref.Host == "" || (kind == "fe" && n.Role == "Leader") {
			ref = n
		}
	}
	if ref.Host == "" {
		return nodes, ref, "", fmt.Errorf("集群里没有存活的 %s 节点可作为配置参考", strings.ToUpper(kind))
	}
	tgt, err := s.sshTarget(c, ref.Host)
	if err != nil {
		return nodes, ref, "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sess, err := s.Keys.Dial(cctx, tgt)
	if err != nil {
		return nodes, ref, "", fmt.Errorf("无法登录参考节点 %s：%w", ref.Host, err)
	}
	defer sess.Close()
	refConf, err = readRemoteFile(cctx, sess, confPath(nodeDir(c, kind), kind))
	return nodes, ref, refConf, err
}

func scaleKind(k string, c model.Cluster) error {
	if k != "fe" && k != "be" && k != "cn" {
		return bad("节点类型不合法")
	}
	if k == "be" && c.Mode == "shared-data" {
		return bad("存算分离集群没有 BE，请扩容 CN")
	}
	return nil
}

func (s *Server) scalePlanHandler(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	kind := r.URL.Query().Get("kind")
	if err := scaleKind(kind, c); err != nil {
		writeErr(w, err)
		return
	}
	if miss := c.Missing(); len(miss) > 0 {
		fail(w, http.StatusConflict, "该集群的运维必填信息未完善（"+strings.Join(miss, "、")+"），请先在「集群接入」中补全")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	nodes, ref, refConf, err := s.scaleContext(ctx, c, kind)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	pl := scalePlan{Version: c.Version, Dir: nodeDir(c, kind), RefHost: ref.Host}
	pl.Home = parentDir(pl.Dir)
	pl.JavaHome, _ = confValue(refConf, "JAVA_HOME")
	for _, n := range nodes {
		if n.Type == "fe" && n.Role == "Leader" {
			pl.Helper = fmt.Sprintf("%s:%d", n.Host, n.EditLog)
		}
	}
	taken := map[string]bool{}
	for _, n := range nodes {
		if n.Type == kind {
			taken[n.Host] = true
		}
	}
	s.Store.View(func(st *storeState) {
		for _, p := range st.Packages {
			if p.Kind == "starrocks" && p.Ver == c.Version {
				pl.Packages = append(pl.Packages, p)
			}
		}
		for _, h := range st.Hosts {
			if h.SSH == "ok" && !taken[h.IP] {
				pl.Hosts = append(pl.Hosts, h)
			}
		}
	})
	sort.Slice(pl.Hosts, func(i, j int) bool { return pl.Hosts[i].IP < pl.Hosts[j].IP })
	ok(w, pl)
}

type scaleIn struct {
	Kind    string   `json:"kind"`
	Role    string   `json:"role"` // FE：FOLLOWER / OBSERVER
	Hosts   []string `json:"hosts"`
	Package string   `json:"package"`
}

func (s *Server) scaleOut(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	var in scaleIn
	if !decode(w, r, &in) {
		return
	}
	if err := scaleKind(in.Kind, c); err != nil {
		writeErr(w, err)
		return
	}
	if in.Kind == "fe" && in.Role != "FOLLOWER" && in.Role != "OBSERVER" {
		fail(w, http.StatusBadRequest, "FE 角色须为 FOLLOWER 或 OBSERVER")
		return
	}
	if len(in.Hosts) == 0 || len(in.Hosts) > 16 {
		fail(w, http.StatusBadRequest, "请选择 1–16 台主机")
		return
	}
	pp, err := s.resolvePackages(c.Version, in.Package, "")
	if err != nil {
		writeErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	nodes, ref, refConf, err := s.scaleContext(ctx, c, in.Kind)
	if err != nil {
		if e, isAPI := err.(apiErr); isAPI {
			writeErr(w, e)
		} else {
			fail(w, http.StatusBadGateway, err.Error())
		}
		return
	}
	var hosts []model.Host
	for _, ip := range uniqStr(append([]string(nil), in.Hosts...)) {
		h, found := s.hostByIP(ip)
		if !found || h.SSH != "ok" {
			fail(w, http.StatusBadRequest, "主机 "+ip+" 未纳管或 SSH 不通")
			return
		}
		if h.Arch != "" && h.Arch != pp.pkg.Arch {
			fail(w, http.StatusBadRequest, fmt.Sprintf("主机 %s 的架构是 %s，安装包是 %s", ip, h.Arch, pp.pkg.Arch))
			return
		}
		for _, n := range nodes {
			if n.Type == in.Kind && n.Host == ip {
				fail(w, http.StatusConflict, "主机 "+ip+" 已经是该集群的 "+strings.ToUpper(in.Kind)+" 节点")
				return
			}
		}
		// 新节点之后要用集群配置的 SSH 信息运维：现在就验证能登录
		tgt, err := s.sshTarget(c, ip)
		if err != nil {
			writeErr(w, err)
			return
		}
		cctx, ccancel := context.WithTimeout(ctx, 20*time.Second)
		sess, err := s.Keys.Dial(cctx, tgt)
		ccancel()
		if err != nil {
			fail(w, http.StatusConflict, fmt.Sprintf("主机 %s 无法用该集群的 SSH 配置登录（%v）。扩容后节点管理、巡检等都要用这组信息，请先让它能登录", ip, err))
			return
		}
		sess.Close()
		hosts = append(hosts, h)
	}
	var helper string
	for _, n := range nodes {
		if n.Type == "fe" && n.Role == "Leader" {
			helper = fmt.Sprintf("%s:%d", n.Host, n.EditLog)
		}
	}
	if in.Kind == "fe" && helper == "" {
		fail(w, http.StatusConflict, "找不到 Leader FE，无法扩容 FE")
		return
	}
	label := strings.ToUpper(in.Kind)
	t := s.newTask("node.scaleout", c.ID, fmt.Sprintf("扩容 %s ×%d：%s", label, len(hosts), strings.Join(in.Hosts, "、")), u.Login, map[string]string{"kind": in.Kind})
	s.auditSQL(r, u, "node.scaleout", fmt.Sprintf("扩容 %s ×%d：%s（任务 %s 已开始）", label, len(hosts), strings.Join(in.Hosts, "、"), t.ID()), "", nil, c.ID)
	ip := clientIP(r)
	_ = ref
	go func() {
		err := s.runScaleOut(t, c, in, pp, hosts, refConf, helper)
		res := "成功"
		if err != nil {
			res = "失败：" + err.Error()
		}
		s.auditDone(ip, u, c.ID, "node.scaleout", fmt.Sprintf("扩容 %s ×%d（任务 %s）", label, len(hosts), t.ID()), res)
		t.Finish(err)
	}()
	ok(w, map[string]any{"ok": true, "taskId": t.ID()})
}

func (s *Server) runScaleOut(t *taskRun, c model.Cluster, in scaleIn, pp pkgPlan, hosts []model.Host, refConf, helper string) error {
	ctx := t.ctx
	cl, err := s.Mgr.Client(ctx, c)
	if err != nil {
		return err
	}
	dir := nodeDir(c, in.Kind)
	parent := parentDir(dir)
	for i, h := range hosts {
		if t.Cancelled() {
			return context.Canceled
		}
		t.SetProg(i * 100 / len(hosts))
		t.Logf("—— [%d/%d] %s ——", i+1, len(hosts), h.IP)
		if err := s.scaleOneHost(ctx, t, c, in, pp, h, dir, parent, refConf, helper, cl); err != nil {
			return fmt.Errorf("%s：%w", h.IP, err)
		}
		s.markHostsUsed(c.ID, h.IP)
	}
	t.Logf("✓ 扩容完成")
	return nil
}

func (s *Server) scaleOneHost(ctx context.Context, t *taskRun, c model.Cluster, in scaleIn, pp pkgPlan, h model.Host, dir, parent, refConf, helper string, cl *sr.Client) error {
	tgt, err := s.sshTarget(c, h.IP)
	if err != nil {
		return err
	}
	rm, err := s.dialRemote(ctx, t, tgt)
	if err != nil {
		return err
	}
	defer rm.Close()
	kind := in.Kind
	if run, err := rm.running(dir, kind); err != nil {
		return err
	} else if run {
		return fmt.Errorf("%s 上已有运行中的 %s 进程（%s）", h.IP, strings.ToUpper(kind), dir)
	}
	up := parent + "/.upload"
	if _, err := rm.run("mkdir -p " + up); err != nil {
		return err
	}
	pkgRemote := up + "/" + pp.pkg.Name
	if err := rm.upload(s.pkgPath(pp.pkg.Name), pp.pkg.Size, pp.pkg.SHA, pkgRemote); err != nil {
		return err
	}
	ex := fmt.Sprintf("%s/.pkg-%s", parent, pp.pkg.Ver)
	if err := rm.extract(pkgRemote, ex); err != nil {
		return err
	}
	src := "fe"
	if kind != "fe" {
		src = "be"
	}
	if _, err := rm.run("mkdir -p " + dir); err != nil {
		return err
	}
	if _, err := rm.run(fmt.Sprintf("cp -a %s/%s/. %s/", ex, src, dir)); err != nil {
		return err
	}
	// 配置：整份沿用参考节点，只改本机的 priority_networks
	conf := refConf
	if h.CIDR != "" {
		conf = setConfLine(conf, "priority_networks", h.CIDR)
	}
	if err := rm.writeFile(confPath(dir, kind), conf); err != nil {
		return err
	}
	var mk []string
	if kind == "fe" {
		if v, ok := confValue(conf, "meta_dir"); ok {
			mk = append(mk, v)
		}
	} else if v, ok := confValue(conf, "storage_root_path"); ok {
		for _, sp := range strings.Split(v, ";") {
			if i := strings.Index(sp, ","); i >= 0 {
				sp = sp[:i]
			}
			if sp = strings.TrimSpace(sp); sp != "" {
				mk = append(mk, sp)
			}
		}
	}
	if len(mk) > 0 {
		if _, err := rm.run("mkdir -p " + strings.Join(mk, " ")); err != nil {
			return err
		}
	}
	if _, err := rm.run("rm -rf " + ex); err != nil {
		return err
	}
	return s.scaleStartAndAdd(ctx, rm, c, in, h, dir, conf, helper, cl)
}

func confPort(conf, key string, def int) int {
	v, _ := confValue(conf, key)
	return atoiOr(v, def)
}

// scaleStartAndAdd 按官方顺序加入集群：FE 先 ADD 再带 --helper 启动；BE / CN 先启动再 ADD。
func (s *Server) scaleStartAndAdd(ctx context.Context, rm *remote, c model.Cluster, in scaleIn, h model.Host, dir, conf, helper string, cl *sr.Client) error {
	if in.Kind == "fe" {
		edit := confPort(conf, "edit_log_port", 9010)
		if member, _ := aliveVia(ctx, cl, c.Mode, "fe", h.IP, edit); !member {
			stmt := fmt.Sprintf(`ALTER SYSTEM ADD %s "%s:%d"`, in.Role, h.IP, edit)
			rm.logf("$ %s", stmt)
			if err := cl.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		if err := rm.start(dir, "fe", helper); err != nil {
			return err
		}
		return rm.waitAlive(cl, c.Mode, "fe", dir, edit, nodeStartWait)
	}
	hb := confPort(conf, "heartbeat_service_port", 9050)
	if err := rm.start(dir, in.Kind, ""); err != nil {
		return err
	}
	what := map[string]string{"be": "BACKEND", "cn": "COMPUTE NODE"}[in.Kind]
	if member, _ := aliveVia(ctx, cl, c.Mode, in.Kind, h.IP, hb); !member {
		stmt := fmt.Sprintf(`ALTER SYSTEM ADD %s "%s:%d"`, what, h.IP, hb)
		rm.logf("$ %s", stmt)
		if err := cl.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return rm.waitAlive(cl, c.Mode, in.Kind, dir, hb, nodeStartWait)
}
