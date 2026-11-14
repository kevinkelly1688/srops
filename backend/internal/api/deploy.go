// @author: kevinkelly
// @license: Apache Licence
// @file: deploy.go
// @time: 2026/10/28

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
	"starrocks-ops/internal/store"
)

// 安装部署：在已纳管的主机上从零部署一个集群，部署完成后自动接入平台。
// 步骤：分发安装包并生成配置 → 启动 Leader FE → 设置 root 密码 → 其余 FE → BE / CN → 校验 → 接入平台。
// 每一步都可重复执行（已完成的部分会跳过），失败后可在「部署任务」中重试。

type deployNode struct {
	Host     string   `json:"host"`
	Roles    []string `json:"roles"` // FE / BE / CN
	Observer bool     `json:"observer"`
}

type deploySpec struct {
	Name         string            `json:"name"`
	Env          string            `json:"env"`
	Desc         string            `json:"desc"`
	Version      string            `json:"version"`
	Package      string            `json:"package"`
	JDKPackage   string            `json:"jdkPackage"`
	Mode         string            `json:"mode"` // shared-nothing / shared-data
	Home         string            `json:"home"`
	RootPassword string            `json:"rootPassword"`
	Nodes        []deployNode      `json:"nodes"`
	FEConf       map[string]string `json:"feConf"`
	BEConf       map[string]string `json:"beConf"`
	S3           *struct {
		Path      string `json:"path"`
		Endpoint  string `json:"endpoint"`
		Region    string `json:"region"`
		AccessKey string `json:"accessKey"`
		SecretKey string `json:"secretKey"`
	} `json:"s3"`
	Owner string `json:"-"`
}

type deployPlan struct {
	spec    deploySpec
	pp      pkgPlan
	hosts   map[string]model.Host
	fes     []deployNode // 第一个为 Leader
	workers []deployNode // BE / CN
	user    string
	port    int
	sudo    bool
}

func (p deployPlan) feDir() string { return p.spec.Home + "/fe" }
func (p deployPlan) beDir() string {
	if p.spec.Mode == "shared-data" {
		return p.spec.Home + "/cn"
	}
	return p.spec.Home + "/be"
}
func (p deployPlan) javaHome() string {
	if p.pp.jdk != nil {
		return p.spec.Home + "/jdk"
	}
	return ""
}

func nodeHas(n deployNode, r string) bool { return contains(n.Roles, r) }

func validConf(m map[string]string) error {
	for k, v := range m {
		if !sr.ValidParamKey(k) || strings.ContainsAny(v, "\r\n\x00") || len(v) > 500 {
			return bad("配置项 " + k + " 不合法")
		}
	}
	return nil
}

func (s *Server) planDeploy(spec deploySpec) (deployPlan, error) {
	p := deployPlan{spec: spec, hosts: map[string]model.Host{}}
	if !store.ValidID(spec.Name) {
		return p, bad("集群名称须为 3–64 位字母、数字、- 或 _")
	}
	if _, err := s.Store.Cluster(spec.Name); err == nil {
		return p, conflict("集群名称 " + spec.Name + " 已存在")
	}
	if !envs[spec.Env] {
		return p, bad("环境不合法")
	}
	if spec.Mode != "shared-nothing" && spec.Mode != "shared-data" {
		return p, bad("部署架构不合法")
	}
	if !dirRe.MatchString(spec.Home) || strings.Contains(spec.Home, "..") || spec.Home == "/" {
		return p, bad("部署目录须为绝对路径，只能包含字母、数字、. _ - /")
	}
	if len(spec.RootPassword) < 8 || strings.ContainsAny(spec.RootPassword, "\r\n\x00") {
		return p, bad("root 密码至少 8 位")
	}
	if validConf(spec.FEConf) != nil || validConf(spec.BEConf) != nil {
		return p, bad("配置项名称或取值不合法")
	}
	pp, err := s.resolvePackages(spec.Version, spec.Package, spec.JDKPackage)
	if err != nil {
		return p, err
	}
	p.pp = pp
	seen := map[string]bool{}
	nBE, nCN := 0, 0
	for _, n := range spec.Nodes {
		if seen[n.Host] {
			return p, bad("主机重复：" + n.Host)
		}
		seen[n.Host] = true
		if len(n.Roles) == 0 {
			continue
		}
		h, found := s.hostByIP(n.Host)
		if !found || h.SSH != "ok" {
			return p, bad("主机 " + n.Host + " 未纳管或 SSH 不通，请先在「主机管理」添加并连通")
		}
		if h.Arch != "" && h.Arch != pp.pkg.Arch {
			return p, bad(fmt.Sprintf("主机 %s 的架构是 %s，安装包是 %s", h.IP, h.Arch, pp.pkg.Arch))
		}
		if pp.jdk != nil && h.Arch != "" && h.Arch != pp.jdk.Arch {
			return p, bad(fmt.Sprintf("主机 %s 的架构是 %s，JDK 包是 %s", h.IP, h.Arch, pp.jdk.Arch))
		}
		if len(p.hosts) == 0 {
			p.user, p.port, p.sudo = h.User, h.Port, h.Sudo
		} else if h.User != p.user || h.Port != p.port {
			return p, bad("所有主机的 SSH 用户与端口必须一致（集群接入后用同一组 SSH 信息运维）")
		}
		p.hosts[h.IP] = h
		for _, r := range n.Roles {
			if r != "FE" && r != "BE" && r != "CN" {
				return p, bad("角色不合法：" + r)
			}
		}
		if nodeHas(n, "BE") && nodeHas(n, "CN") {
			return p, bad("主机 " + n.Host + " 不能同时部署 BE 与 CN")
		}
		if nodeHas(n, "BE") {
			nBE++
		}
		if nodeHas(n, "CN") {
			nCN++
		}
		if nodeHas(n, "BE") || nodeHas(n, "CN") {
			p.workers = append(p.workers, n)
		}
	}
	// FE 顺序：非 Observer 在前（第一个是 Leader），Observer 在后
	var voters, obs []deployNode
	for _, n := range spec.Nodes {
		if nodeHas(n, "FE") {
			if n.Observer {
				obs = append(obs, n)
			} else {
				voters = append(voters, n)
			}
		}
	}
	p.fes = append(voters, obs...)
	if len(voters) == 0 {
		return p, bad("至少需要 1 个 Leader / Follower FE")
	}
	switch spec.Mode {
	case "shared-nothing":
		if nBE == 0 {
			return p, bad("存算一体至少需要 1 个 BE")
		}
	default:
		if nBE > 0 {
			return p, bad("存算分离不部署 BE，请使用 CN")
		}
		if nCN == 0 {
			return p, bad("存算分离至少需要 1 个 CN")
		}
		if spec.S3 == nil || spec.S3.Path == "" || spec.S3.Region == "" {
			return p, bad("存算分离需要填写对象存储路径与 Region")
		}
	}
	return p, nil
}

// deployExec 创建部署任务（POST /api/deploy/tasks）或重试失败的任务（POST /api/tasks/{tid}/retry）。
func (s *Server) deployExec(w http.ResponseWriter, r *http.Request, u *model.User) {
	var spec deploySpec
	var t *taskRun
	if tid := r.PathValue("tid"); tid != "" {
		t = s.task(tid)
		if t == nil || t.t.Kind != "deploy" {
			fail(w, http.StatusNotFound, "部署任务不存在")
			return
		}
		c, _ := t.snapshot(false, 0)
		if c.State != "failed" && c.State != "canceled" {
			fail(w, http.StatusConflict, "只有失败或已取消的部署任务可以重试")
			return
		}
		raw, err := s.Box.Decrypt(c.Data["spec"])
		if err != nil || json.Unmarshal([]byte(raw), &spec) != nil {
			fail(w, http.StatusInternalServerError, "无法读取部署参数")
			return
		}
	} else if !decode(w, r, &spec) {
		return
	}
	p, err := s.planDeploy(spec)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !s.lockNode("", "deploy", spec.Name) {
		fail(w, http.StatusConflict, "集群 "+spec.Name+" 正在部署中")
		return
	}
	title := fmt.Sprintf("部署 %s（%s · %s）", spec.Name, p.pp.pkg.Ver, map[string]string{"shared-nothing": "存算一体", "shared-data": "存算分离"}[spec.Mode])
	if t == nil {
		b, _ := json.Marshal(spec)
		enc, err := s.Box.Encrypt(string(b))
		if err != nil {
			s.unlockNode("", "deploy", spec.Name)
			writeErr(w, err)
			return
		}
		t = s.newTask("deploy", "", title, u.Login, map[string]string{"spec": enc, "name": spec.Name, "version": p.pp.pkg.Ver})
	} else {
		t.restart()
	}
	spec.Owner = u.Login
	p.spec = spec
	ip := clientIP(r)
	s.auditSQL(r, u, "deploy.execute", fmt.Sprintf("%s（任务 %s 已开始）", title, t.ID()), fmt.Sprintf("安装包 %s；主机 %s", spec.Package, deployHosts(spec)), nil)
	go func() {
		defer s.unlockNode("", "deploy", spec.Name)
		err := s.runDeploy(t, p)
		res := "成功"
		if err != nil {
			res = "失败：" + err.Error()
		}
		s.auditDone(ip, u, spec.Name, "deploy.execute", fmt.Sprintf("%s（任务 %s）", title, t.ID()), res)
		t.Finish(err)
	}()
	ok(w, map[string]any{"ok": true, "taskId": t.ID()})
}

func deployHosts(spec deploySpec) string {
	var out []string
	for _, n := range spec.Nodes {
		if len(n.Roles) > 0 {
			out = append(out, n.Host+"("+strings.Join(n.Roles, "+")+")")
		}
	}
	return strings.Join(out, "、")
}

// restart 把失败 / 已取消的任务重新置为执行中（日志保留，继续追加）。
func (t *taskRun) restart() {
	t.mu.Lock()
	t.t.State, t.t.Err, t.t.End, t.t.Prog = "running", "", time.Time{}, 0
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.t.Log = append(t.t.Log, time.Now().Format("15:04:05")+" —— 重试 ——")
	t.mu.Unlock()
	t.save(true)
}

// cancelTask 取消正在执行的部署 / 升级任务。
func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request, u *model.User) {
	t := s.task(r.PathValue("tid"))
	if t == nil {
		fail(w, http.StatusNotFound, "任务不存在")
		return
	}
	c, _ := t.snapshot(false, 0)
	code := map[string]string{"deploy": "deploy.execute", "upgrade": "upgrade.execute"}[c.Kind]
	if code == "" || !accessOf(r).Can(code) {
		fail(w, http.StatusForbidden, "无权取消该任务")
		return
	}
	if c.Cluster != "" && !accessOf(r).Cluster(c.Cluster) {
		fail(w, http.StatusForbidden, "无权访问该集群")
		return
	}
	if c.Finished() {
		fail(w, http.StatusConflict, "任务已结束")
		return
	}
	t.Logf("收到取消请求（%s），将在当前步骤结束后停止", u.Login)
	t.cancel()
	s.auditSQL(r, u, code, "取消任务 "+c.ID+"："+c.Title, "", nil, c.Cluster)
	ok(w, map[string]bool{"ok": true})
}

// ---------- 执行 ----------

func (s *Server) runDeploy(t *taskRun, p deployPlan) error {
	spec := p.spec
	t.Mask(spec.RootPassword)
	if spec.S3 != nil {
		t.Mask(spec.S3.AccessKey, spec.S3.SecretKey)
	}
	ctx := t.ctx
	const total = 7
	stage := 0
	step := func(name string) error {
		if t.Cancelled() {
			return context.Canceled
		}
		stage++
		t.SetProg((stage - 1) * 100 / total)
		t.Logf("[%d/%d] %s", stage, total, name)
		return nil
	}
	mode := spec.Mode

	// 1. 分发安装包并生成配置
	if err := step("分发安装包并生成配置（" + deployHosts(spec) + "）"); err != nil {
		return err
	}
	var errs []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for _, n := range spec.Nodes {
		if len(n.Roles) == 0 {
			continue
		}
		wg.Add(1)
		go func(n deployNode) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := s.installDeployHost(ctx, t, p, n); err != nil {
				t.Logf("[%s] 失败：%v", n.Host, err)
				mu.Lock()
				errs = append(errs, n.Host+"："+err.Error())
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("%d 台主机安装失败：%s", len(errs), strings.Join(errs, "；"))
	}

	// 2. 启动 Leader FE，等待 SQL 可用
	leader := p.fes[0]
	if err := step("启动 Leader FE " + leader.Host); err != nil {
		return err
	}
	lr, err := s.dialRemote(ctx, t, hostTarget(p.hosts[leader.Host]))
	if err != nil {
		return err
	}
	defer lr.Close()
	if err := lr.start(p.feDir(), "fe", ""); err != nil {
		return err
	}
	cl, usedPw, err := s.waitSQL(ctx, t, lr, leader.Host, spec.RootPassword, p.feDir())
	if err != nil {
		return err
	}
	defer func() { cl.Close() }()

	// 3. root 密码
	if err := step("设置 root 密码"); err != nil {
		return err
	}
	if usedPw {
		t.Logf("root 密码已设置，跳过")
	} else {
		lit, err := sr.Lit(spec.RootPassword)
		if err != nil {
			return err
		}
		t.Logf("$ ALTER USER root IDENTIFIED BY ***")
		if err := cl.Exec(ctx, "ALTER USER root IDENTIFIED BY "+lit); err != nil {
			return fmt.Errorf("设置 root 密码失败：%w", err)
		}
		cl.Close()
		cl, err = sr.Open(ctx, sr.ConnInfo{Hosts: []string{leader.Host}, QueryPort: 9030, HTTPPort: 8030, User: "root", Password: spec.RootPassword})
		if err != nil {
			return fmt.Errorf("设置密码后重新连接失败：%w", err)
		}
	}

	// 4. 其余 FE
	if err := step(fmt.Sprintf("加入其余 %d 个 FE", len(p.fes)-1)); err != nil {
		return err
	}
	for _, n := range p.fes[1:] {
		if err := s.addFE(ctx, t, p, cl, leader, n); err != nil {
			return fmt.Errorf("FE %s：%w", n.Host, err)
		}
	}

	// 5. BE / CN
	if err := step(fmt.Sprintf("启动并加入 %d 台 BE / CN", len(p.workers))); err != nil {
		return err
	}
	for _, n := range p.workers {
		if err := s.addWorker(ctx, t, p, cl, mode, n); err != nil {
			return fmt.Errorf("%s：%w", n.Host, err)
		}
	}

	// 6. 校验
	if err := step("校验集群状态"); err != nil {
		return err
	}
	nodes, warns := cl.Nodes(ctx, mode)
	dead := 0
	for _, n := range nodes {
		if !n.Alive {
			dead++
		}
	}
	t.Logf("SHOW FRONTENDS / BACKENDS / COMPUTE NODES：共 %d 个节点，%d 个未存活", len(nodes), dead)
	for _, w := range warns {
		t.Logf("警告：%s", w)
	}
	if dead > 0 || len(nodes) != len(p.fes)+len(p.workers) {
		return fmt.Errorf("集群节点数 %d（应为 %d），未存活 %d 个", len(nodes), len(p.fes)+len(p.workers), dead)
	}

	// 7. 接入平台
	if err := step("接入平台"); err != nil {
		return err
	}
	if _, err := s.Store.Cluster(spec.Name); err == nil {
		t.Logf("集群 %s 已在平台中，跳过接入", spec.Name)
	} else {
		var fes []string
		for _, n := range p.fes {
			fes = append(fes, n.Host)
		}
		in := clusterIn{connIn: connIn{Hosts: fes, QueryPort: 9030, HTTPPort: 8030, User: "root", Password: spec.RootPassword},
			Name: spec.Name, Env: spec.Env, Owner: spec.Owner, Desc: orStr(spec.Desc, "平台部署"),
			SSH: &sshIn{User: p.user, Port: p.port, Auth: "key", Sudo: p.sudo, FEDir: p.feDir(), BEDir: p.beDir()}}
		c, err := s.buildCluster(ctx, in, nil)
		if err == nil {
			err = s.Store.PutCluster(c, true)
		}
		if err != nil {
			return fmt.Errorf("接入平台失败：%w", err)
		}
		s.Mgr.Sampler.Watch(c)
		t.Logf("集群 %s 已接入平台（版本 %s）", c.ID, c.Version)
	}
	var ips []string
	for _, n := range spec.Nodes {
		if len(n.Roles) > 0 {
			ips = append(ips, n.Host)
		}
	}
	s.markHostsUsed(spec.Name, ips...)
	t.SetData("cluster", spec.Name)
	t.Logf("✓ 部署完成")
	return nil
}

// waitSQL 等待 FE 的查询端口可用并连上（先试空密码，再试设定的密码）。返回客户端以及是否已经是设定的密码。
func (s *Server) waitSQL(ctx context.Context, t *taskRun, r *remote, host, pw, feDir string) (*sr.Client, bool, error) {
	t.Logf("等待 %s:9030 可连接（最长 %d 秒）…", host, int(nodeStartWait.Seconds()))
	deadline := time.Now().Add(nodeStartWait)
	var last error
	for {
		for _, try := range []string{"", pw} {
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			cl, err := sr.Open(cctx, sr.ConnInfo{Hosts: []string{host}, QueryPort: 9030, HTTPPort: 8030, User: "root", Password: try})
			cancel()
			if err == nil {
				t.Logf("已连接 FE")
				return cl, try != "", nil
			}
			last = err
		}
		if time.Now().After(deadline) {
			r.tailLogs(feDir, "fe")
			return nil, false, fmt.Errorf("FE 启动后 %d 秒内无法连接：%v", int(nodeStartWait.Seconds()), last)
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(nodePollEvery):
		}
	}
}

func (s *Server) addFE(ctx context.Context, t *taskRun, p deployPlan, cl *sr.Client, leader, n deployNode) error {
	rm, err := s.dialRemote(ctx, t, hostTarget(p.hosts[n.Host]))
	if err != nil {
		return err
	}
	defer rm.Close()
	role := "FOLLOWER"
	if n.Observer {
		role = "OBSERVER"
	}
	member, _ := aliveVia(ctx, cl, p.spec.Mode, "fe", n.Host, 9010)
	if !member {
		stmt := fmt.Sprintf(`ALTER SYSTEM ADD %s "%s:9010"`, role, n.Host)
		rm.logf("$ %s", stmt)
		if err := cl.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	if err := rm.start(p.feDir(), "fe", fmt.Sprintf("%s:9010", leader.Host)); err != nil {
		return err
	}
	return rm.waitAlive(cl, p.spec.Mode, "fe", p.feDir(), 9010, nodeStartWait)
}

func (s *Server) addWorker(ctx context.Context, t *taskRun, p deployPlan, cl *sr.Client, mode string, n deployNode) error {
	rm, err := s.dialRemote(ctx, t, hostTarget(p.hosts[n.Host]))
	if err != nil {
		return err
	}
	defer rm.Close()
	kind, what := "be", "BACKEND"
	if nodeHas(n, "CN") {
		kind, what = "cn", "COMPUTE NODE"
	}
	if err := rm.start(p.beDir(), kind, ""); err != nil {
		return err
	}
	member, _ := aliveVia(ctx, cl, mode, kind, n.Host, 9050)
	if !member {
		stmt := fmt.Sprintf(`ALTER SYSTEM ADD %s "%s:9050"`, what, n.Host)
		rm.logf("$ %s", stmt)
		if err := cl.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return rm.waitAlive(cl, mode, kind, p.beDir(), 9050, nodeStartWait)
}

// installDeployHost 在一台主机上完成：上传安装包（与 JDK）、解压、复制各角色目录、生成配置、创建数据目录。
func (s *Server) installDeployHost(ctx context.Context, t *taskRun, p deployPlan, n deployNode) error {
	h := p.hosts[n.Host]
	rm, err := s.dialRemote(ctx, t, hostTarget(h))
	if err != nil {
		return err
	}
	defer rm.Close()
	home := p.spec.Home
	type role struct{ kind, dir, src string }
	var roles []role
	if nodeHas(n, "FE") {
		roles = append(roles, role{"fe", p.feDir(), "fe"})
	}
	if nodeHas(n, "BE") {
		roles = append(roles, role{"be", p.beDir(), "be"})
	}
	if nodeHas(n, "CN") {
		roles = append(roles, role{"cn", p.beDir(), "be"})
	}
	allRunning := true
	for _, r := range roles {
		run, err := rm.running(r.dir, r.kind)
		if err != nil {
			return err
		}
		allRunning = allRunning && run
	}
	if allRunning {
		rm.logf("各角色进程都已在运行，跳过安装与配置")
		return nil
	}
	up := home + "/.upload"
	if _, err := rm.run("mkdir -p " + up); err != nil {
		return err
	}
	pkgRemote := up + "/" + p.pp.pkg.Name
	if err := rm.upload(s.pkgPath(p.pp.pkg.Name), p.pp.pkg.Size, p.pp.pkg.SHA, pkgRemote); err != nil {
		return err
	}
	if jh := p.javaHome(); jh != "" {
		if res, err := rm.quiet(fmt.Sprintf("test -x %s/bin/java && echo ok", jh)); err == nil && res.Code == 0 {
			rm.logf("JDK %s 已存在，跳过", jh)
		} else {
			jr := up + "/" + p.pp.jdk.Name
			if err := rm.upload(s.pkgPath(p.pp.jdk.Name), p.pp.jdk.Size, p.pp.jdk.SHA, jr); err != nil {
				return err
			}
			if err := rm.extract(jr, jh); err != nil {
				return err
			}
		}
	}
	ex := fmt.Sprintf("%s/.pkg-%s", home, p.pp.pkg.Ver)
	if err := rm.extract(pkgRemote, ex); err != nil {
		return err
	}
	cidr := h.CIDR
	if cidr == "" {
		cidr = h.IP + "/32"
	}
	nBE := 0
	for _, w := range p.workers {
		if nodeHas(w, "BE") {
			nBE++
		}
	}
	for _, r := range roles {
		if run, _ := rm.running(r.dir, r.kind); run {
			rm.logf("%s 进程已在运行，不覆盖其文件", strings.ToUpper(r.kind))
			continue
		}
		if _, err := rm.run("mkdir -p " + r.dir); err != nil {
			return err
		}
		if _, err := rm.run(fmt.Sprintf("cp -a %s/%s/. %s/", ex, r.src, r.dir)); err != nil {
			return err
		}
		kv := [][2]string{{"priority_networks", cidr}}
		var mk []string // 需要预先创建的目录
		switch r.kind {
		case "fe":
			meta := r.dir + "/meta"
			kv = append(kv, [2]string{"meta_dir", meta})
			mk = append(mk, meta)
			if jh := p.javaHome(); jh != "" {
				kv = append(kv, [2]string{"JAVA_HOME", jh})
			}
			if p.spec.Mode == "shared-nothing" && nBE < 3 {
				kv = append(kv, [2]string{"default_replication_num", strconv.Itoa(nBE)})
			}
			if p.spec.Mode == "shared-data" {
				kv = append(kv, [2]string{"run_mode", "shared_data"}, [2]string{"cloud_native_meta_port", "6090"}, [2]string{"cloud_native_storage_type", "S3"},
					[2]string{"enable_load_volume_from_conf", "true"}, [2]string{"aws_s3_path", p.spec.S3.Path}, [2]string{"aws_s3_region", p.spec.S3.Region})
				if p.spec.S3.Endpoint != "" {
					kv = append(kv, [2]string{"aws_s3_endpoint", p.spec.S3.Endpoint})
				}
				if p.spec.S3.AccessKey != "" {
					kv = append(kv, [2]string{"aws_s3_use_aws_sdk_default_behavior", "false"}, [2]string{"aws_s3_access_key", p.spec.S3.AccessKey}, [2]string{"aws_s3_secret_key", p.spec.S3.SecretKey})
				} else {
					kv = append(kv, [2]string{"aws_s3_use_instance_profile", "true"})
				}
			}
			kv = append(kv, confPairs(p.spec.FEConf)...)
		default:
			kv = append(kv, [2]string{"storage_root_path", r.dir + "/storage"})
			if jh := p.javaHome(); jh != "" {
				kv = append(kv, [2]string{"JAVA_HOME", jh}) // Java UDF、外部表等功能需要
			}
			kv = append(kv, confPairs(p.spec.BEConf)...)
			for _, sp := range strings.Split(lastVal(kv, "storage_root_path"), ";") {
				if i := strings.Index(sp, ","); i >= 0 {
					sp = sp[:i]
				}
				if sp = strings.TrimSpace(sp); sp != "" {
					mk = append(mk, sp)
				}
			}
		}
		if err := rm.setConf(confPath(r.dir, r.kind), kv); err != nil {
			return err
		}
		if len(mk) > 0 {
			if _, err := rm.run("mkdir -p " + strings.Join(mk, " ")); err != nil {
				return err
			}
		}
	}
	_, err = rm.run("rm -rf " + ex)
	return err
}

func confPairs(m map[string]string) [][2]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][2]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, [2]string{k, m[k]})
	}
	return out
}

func lastVal(kv [][2]string, k string) string {
	v := ""
	for _, p := range kv {
		if p[0] == k {
			v = p[1]
		}
	}
	return v
}
