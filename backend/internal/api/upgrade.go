// @author: kevinkelly
// @license: Apache Licence
// @file: upgrade.go
// @time: 2026/10/28

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// 升级回滚：按官方顺序对集群做滚动升级——先 BE，再 CN，最后 FE（Observer → Follower → Leader）。
// 每个节点：优雅停止 → 备份旧的 bin / lib（改名 *.bak-<旧版本>）→ 换上新版本文件 → 启动 → 等待存活且版本为目标版本 → 等待 Tablet 健康。
// 回滚（降级）顺序相反：先 FE 再 CN / BE，把备份的目录换回去。每个节点的步骤都可重复执行，中断后可继续。

// 轮询与等待；测试中会调小。
var (
	upHealthWait   = 10 * time.Minute
	upVersionWait  = 3 * time.Minute
	upPauseCheck   = 500 * time.Millisecond
	replaceDirsFE  = []string{"bin", "lib", "spark-dpp"}
	replaceDirsBE  = []string{"bin", "lib"}
	errUpgradeBusy = conflict("该集群正在执行升级或回滚")
)

func replaceDirs(kind string) []string {
	if kind == "fe" {
		return replaceDirsFE
	}
	return replaceDirsBE
}

// upCtl 是运行中升级的控制开关。
type upCtl struct {
	mu     sync.Mutex
	paused bool
}

func (s *Server) upControl(pid string) *upCtl {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.upCtls == nil {
		s.upCtls = map[string]*upCtl{}
	}
	if s.upCtls[pid] == nil {
		s.upCtls[pid] = &upCtl{}
	}
	return s.upCtls[pid]
}

// ---------- 版本与顺序 ----------

func verParts(v string) (maj, min, patch int, ok bool) {
	p := strings.SplitN(strings.TrimLeft(v, "vV"), ".", 3)
	if len(p) < 3 {
		return 0, 0, 0, false
	}
	a, e1 := strconv.Atoi(p[0])
	b, e2 := strconv.Atoi(p[1])
	c, e3 := strconv.Atoi(strings.TrimFunc(p[2], func(r rune) bool { return r < '0' || r > '9' }))
	return a, b, c, e1 == nil && e2 == nil && e3 == nil
}

func verCmp(a, b string) int {
	a1, a2, a3, _ := verParts(a)
	b1, b2, b3, _ := verParts(b)
	for _, d := range [][2]int{{a1, b1}, {a2, b2}, {a3, b3}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// checkUpgradePath 校验升级路径：只能升级到更高的版本，同一大版本内不能跨小版本。
func checkUpgradePath(from, to string) error {
	fa, fb, _, ok1 := verParts(from)
	ta, tb, _, ok2 := verParts(to)
	if !ok1 || !ok2 {
		return bad("无法识别版本号")
	}
	switch c := verCmp(from, to); {
	case c == 0:
		return bad("目标版本与当前版本相同")
	case c > 0:
		return bad(fmt.Sprintf("目标版本 %s 低于当前版本 %s：降级请使用「回滚」，且仅支持回到升级前的版本", to, from))
	}
	if ta == fa && tb > fb+1 {
		return bad(fmt.Sprintf("不能跨小版本升级：请先升级到 %d.%d.x，再升级到 %s（官方要求逐级升级）", fa, fb+1, to))
	}
	return nil
}

func nodeRank(n model.Node, down bool) int {
	switch n.Type {
	case "be":
		return 10
	case "cn":
		return 20
	}
	return map[string]int{"Observer": 30, "Follower": 31, "Leader": 32}[n.Role]
}

// upgradeOrder：BE → CN → FE（Observer → Follower → Leader）。
func upgradeOrder(nodes []model.Node) []model.Node {
	out := append([]model.Node(nil), nodes...)
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := nodeRank(out[i], false), nodeRank(out[j], false); ri != rj {
			return ri < rj
		}
		return nodeKey(out[i]) < nodeKey(out[j])
	})
	return out
}

// downgradeOrder：FE（Follower → Observer → Leader 最后）→ CN → BE。
func downgradeOrder(nodes []model.Node) []model.Node {
	rank := func(n model.Node) int {
		switch {
		case n.Type == "fe" && n.Role == "Follower":
			return 1
		case n.Type == "fe" && n.Role == "Observer":
			return 2
		case n.Type == "fe":
			return 3
		case n.Type == "cn":
			return 4
		}
		return 5
	}
	out := append([]model.Node(nil), nodes...)
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return nodeKey(out[i]) < nodeKey(out[j])
	})
	return out
}

func planNodeID(n model.Node) string { return n.Type + "|" + nodeKey(n) }

// ---------- 存取 ----------

func (s *Server) planByID(cluster, pid string) (model.UpgradePlan, bool) {
	var p model.UpgradePlan
	var found bool
	s.Store.View(func(st *store.State) {
		for _, x := range st.Upgrades {
			if x.ID == pid && x.Cluster == cluster {
				p, found = x, true
			}
		}
	})
	return p, found
}

func (s *Server) updatePlan(pid string, fn func(p *model.UpgradePlan)) {
	s.Store.Update(func(st *store.State) error {
		for i := range st.Upgrades {
			if st.Upgrades[i].ID == pid {
				fn(&st.Upgrades[i])
				st.Upgrades[i].Updated = time.Now()
			}
		}
		return nil
	})
}

// normalizeUpgrades 平台重启后，把还在“执行中”的计划标为失败（对应的任务已被中断）。
func (s *Server) normalizeUpgrades() {
	s.Store.Update(func(st *store.State) error {
		for i := range st.Upgrades {
			switch st.Upgrades[i].State {
			case "running", "rollingback", "paused":
				st.Upgrades[i].State, st.Upgrades[i].Note = "failed", "平台重启，任务被中断；已处理的节点会被跳过，可继续执行"
			}
		}
		return nil
	})
}

// ---------- 页面数据 ----------

type upNodeOut struct {
	Type    string `json:"type"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Role    string `json:"role"`
	Key     string `json:"key"`
	Version string `json:"version"`
	Alive   bool   `json:"alive"`
	Order   int    `json:"order"`
}

func (s *Server) upgradeOverview(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 40*time.Second)
	if !ok2 {
		return
	}
	defer done()
	nodes, warns := x.cl.Nodes(x.ctx, x.c.Mode)
	ver, _ := x.cl.Version(x.ctx)
	var outN []upNodeOut
	for i, n := range upgradeOrder(nodes) {
		outN = append(outN, upNodeOut{n.Type, n.Host, n.Port, n.Role, planNodeID(n), n.Version, n.Alive, i + 1})
	}
	plans := []model.UpgradePlan{}
	s.Store.View(func(st *store.State) {
		for i := len(st.Upgrades) - 1; i >= 0; i-- {
			if st.Upgrades[i].Cluster == x.c.ID {
				p := st.Upgrades[i]
				p.Done = append([]string{}, p.Done...)
				plans = append(plans, p)
			}
		}
	})
	paused := false
	for _, p := range plans {
		if p.State == "running" || p.State == "paused" || p.State == "rollingback" {
			paused = s.upControl(p.ID).paused
		}
	}
	ok(w, map[string]any{"version": ver, "nodes": outN, "plans": plans, "paused": paused, "warnings": warns})
}

// ---------- 创建 / 删除计划 ----------

type planIn struct {
	Package        string `json:"package"`
	Interval       int    `json:"interval"`
	DisableBalance bool   `json:"disableBalance"`
}

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	var in planIn
	if !decode(w, r, &in) {
		return
	}
	if miss := c.Missing(); len(miss) > 0 {
		fail(w, http.StatusConflict, "该集群的运维必填信息未完善（"+strings.Join(miss, "、")+"），请先在「集群接入」中补全")
		return
	}
	if in.Interval < 0 || in.Interval > 3600 {
		fail(w, http.StatusBadRequest, "每台节点之间的等待时间为 0–3600 秒")
		return
	}
	p, found := s.packageByName(in.Package)
	if !found || p.Kind != "starrocks" {
		fail(w, http.StatusBadRequest, "安装包不存在，请先在「安装部署 → 安装包」上传目标版本")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cl, err := s.Mgr.Client(ctx, c)
	if err != nil {
		fail(w, http.StatusBadGateway, "连接集群失败："+err.Error())
		return
	}
	cur, err := cl.Version(ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取当前版本失败："+err.Error())
		return
	}
	cur = shortVer(cur)
	if err := checkUpgradePath(cur, p.Ver); err != nil {
		writeErr(w, err)
		return
	}
	b := make([]byte, 3)
	rand.Read(b)
	plan := model.UpgradePlan{ID: "UP-" + time.Now().Format("060102150405") + "-" + hex.EncodeToString(b), Cluster: c.ID, From: cur, To: p.Ver, Package: p.Name,
		Interval: in.Interval, DisableBalance: in.DisableBalance, State: "pending", By: u.Login, At: time.Now(), Updated: time.Now(), Done: []string{}}
	s.Store.Update(func(st *store.State) error { st.Upgrades = append(st.Upgrades, plan); return nil })
	s.auditSQL(r, u, "upgrade.create", fmt.Sprintf("创建升级计划 %s：%s → %s（%s）", plan.ID, cur, p.Ver, p.Name), "", nil, c.ID)
	ok(w, plan)
}

func shortVer(v string) string {
	if i := strings.IndexAny(v, "- "); i > 0 {
		return v[:i]
	}
	return v
}

func (s *Server) deletePlan(w http.ResponseWriter, r *http.Request, u *model.User) {
	cid, pid := r.PathValue("id"), r.PathValue("pid")
	err := s.Store.Update(func(st *store.State) error {
		for i, p := range st.Upgrades {
			if p.ID == pid && p.Cluster == cid {
				if p.State != "pending" {
					return conflict("只能删除尚未执行的计划")
				}
				st.Upgrades = append(st.Upgrades[:i], st.Upgrades[i+1:]...)
				return nil
			}
		}
		return missing("升级计划不存在")
	})
	s.auditSQL(r, u, "upgrade.create", "删除升级计划 "+pid, "", err, cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---------- 升级前检查 ----------

type upCheck struct {
	ID   string `json:"id"`
	Item string `json:"item"`
	St   string `json:"st"` // ok / warn / bad / info
	Val  string `json:"val"`
}

func (s *Server) upgradeChecks(ctx context.Context, c model.Cluster, plan model.UpgradePlan, nodes []model.Node, cl *sr.Client) []upCheck {
	var out []upCheck
	add := func(id, item, st, val string) { out = append(out, upCheck{id, item, st, val}) }
	// 1 节点全部存活
	var dead []string
	for _, n := range nodes {
		if !n.Alive {
			dead = append(dead, strings.ToUpper(n.Type)+" "+nodeKey(n))
		}
	}
	if len(dead) == 0 {
		add("alive", "FE / BE / CN 全部存活", "ok", fmt.Sprintf("%d 个节点", len(nodes)))
	} else {
		add("alive", "FE / BE / CN 全部存活", "bad", "未存活："+strings.Join(dead, "、"))
	}
	// 2 不健康 Tablet
	if n, ok2 := unhealthyTablets(ctx, cl); !ok2 {
		add("tablet", "不健康 Tablet = 0", "warn", "无法读取 SHOW PROC '/statistic'")
	} else if n == 0 {
		add("tablet", "不健康 Tablet = 0", "ok", "0")
	} else {
		add("tablet", "不健康 Tablet = 0", "warn", fmt.Sprintf("%d 个不健康 Tablet，建议先处理（升级过程中会逐台等待 Tablet 恢复）", n))
	}
	// 3 FE 副本数
	voters := 0
	for _, n := range nodes {
		if n.Type == "fe" && n.Role != "Observer" {
			voters++
		}
	}
	if voters >= 3 {
		add("fequorum", "FE 高可用（Leader + Follower ≥ 3）", "ok", fmt.Sprintf("%d 个", voters))
	} else {
		add("fequorum", "FE 高可用（Leader + Follower ≥ 3）", "warn", fmt.Sprintf("只有 %d 个，升级 FE 期间会有短暂不可用", voters))
	}
	// 4 安装包
	pkg, found := s.packageByName(plan.Package)
	if !found {
		add("pkg", "目标版本安装包已上传", "bad", plan.Package+" 已被删除，请重新上传")
	} else {
		add("pkg", "目标版本安装包已上传", "ok", fmt.Sprintf("%s（%s · %s · %.1f MB）", pkg.Name, pkg.Ver, pkg.Arch, float64(pkg.Size)/1048576))
	}
	// 5 路径
	if err := checkUpgradePath(plan.From, plan.To); err != nil {
		add("path", "升级路径合法", "bad", err.Error())
	} else {
		note := ""
		if fa, _, _, _ := verParts(plan.From); true {
			if ta, _, _, _ := verParts(plan.To); ta > fa {
				note = "（跨大版本，请先阅读官方升级说明与兼容性变更）"
			}
		}
		add("path", "升级路径合法", "ok", plan.From+" → "+plan.To+note)
	}
	// 6 各主机：SSH、目录、架构、空间
	type hk struct{ host, dir string }
	seen := map[hk]bool{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var sshBad, dirBad, archBad, spaceBad, spaceWarn []string
	for _, n := range nodes {
		k := hk{n.Host, nodeDir(c, n.Type)}
		if seen[k] {
			continue
		}
		seen[k] = true
		wg.Add(1)
		go func(n model.Node, dir string) {
			defer wg.Done()
			label := n.Host + "（" + strings.ToUpper(n.Type) + "）"
			tgt, err := s.sshTarget(c, n.Host)
			if err != nil {
				mu.Lock()
				sshBad = append(sshBad, label+"："+err.Error())
				mu.Unlock()
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			sess, err := s.Keys.Dial(cctx, tgt)
			if err != nil {
				mu.Lock()
				sshBad = append(sshBad, label+"："+err.Error())
				mu.Unlock()
				return
			}
			defer sess.Close()
			run := func(cmd string) (string, int) {
				res, err := sess.Run(cctx, cmd)
				if err != nil {
					return err.Error(), -1
				}
				return strings.TrimSpace(res.Stdout), res.Code
			}
			if _, code := run(fmt.Sprintf("test -d %s/bin && test -d %s/lib && echo ok", dir, dir)); code != 0 {
				mu.Lock()
				dirBad = append(dirBad, label+" "+dir)
				mu.Unlock()
			}
			if found {
				if o, _ := run("uname -m"); normArch(o) != pkg.Arch {
					mu.Lock()
					archBad = append(archBad, fmt.Sprintf("%s 是 %s", label, normArch(o)))
					mu.Unlock()
				}
			}
			o, _ := run(ancestorCmd(parentDir(dir), `df -P -BG "$d" | awk 'NR==2{print $4}'`))
			g := atoiOr(strings.TrimSuffix(o, "G"), -1)
			mu.Lock()
			switch {
			case g >= 0 && g < 2:
				spaceBad = append(spaceBad, fmt.Sprintf("%s 仅剩 %d GB", label, g))
			case g >= 0 && g < 10:
				spaceWarn = append(spaceWarn, fmt.Sprintf("%s 仅剩 %d GB", label, g))
			}
			mu.Unlock()
		}(n, k.dir)
	}
	wg.Wait()
	sort.Strings(sshBad)
	listOrOK := func(id, item string, bads []string, okMsg string) {
		sort.Strings(bads)
		if len(bads) == 0 {
			add(id, item, "ok", okMsg)
		} else {
			add(id, item, "bad", strings.Join(bads, "；"))
		}
	}
	listOrOK("ssh", "所有节点主机 SSH 可登录", sshBad, fmt.Sprintf("%d 台", len(seen)))
	listOrOK("dir", "安装目录存在（bin / lib）", dirBad, "全部存在")
	if found {
		listOrOK("arch", "CPU 架构与安装包一致", archBad, pkg.Arch)
	}
	switch {
	case len(spaceBad) > 0:
		sort.Strings(spaceBad)
		add("space", "安装目录所在磁盘有足够空间", "bad", strings.Join(spaceBad, "；"))
	case len(spaceWarn) > 0:
		sort.Strings(spaceWarn)
		add("space", "安装目录所在磁盘有足够空间", "warn", strings.Join(spaceWarn, "；"))
	default:
		add("space", "安装目录所在磁盘有足够空间", "ok", "≥ 10 GB")
	}
	// 7 备份 / 恢复作业
	var busy []string
	if r, err := cl.Query(ctx, "SHOW DATABASES"); err == nil {
		for i := range r.Data {
			db := r.Get(i, "Database")
			if db == "" || db == "information_schema" || db == "sys" || db == "_statistics_" {
				continue
			}
			if act, why, err := cl.HasActiveJob(ctx, db); err == nil && act {
				busy = append(busy, db+" 上有 "+why)
			}
		}
		if len(busy) == 0 {
			add("jobs", "没有进行中的备份 / 恢复作业", "ok", "")
		} else {
			add("jobs", "没有进行中的备份 / 恢复作业", "warn", strings.Join(busy, "；"))
		}
	}
	// 8 Tablet 均衡
	if r, err := cl.Query(ctx, "ADMIN SHOW FRONTEND CONFIG LIKE 'tablet_sched_disable_balance'"); err == nil && len(r.Data) > 0 {
		v := r.Get(0, "Value")
		if plan.DisableBalance {
			add("balance", "Tablet 均衡", "info", "当前 "+v+"；升级期间平台会临时关闭，升级结束后恢复")
		} else {
			add("balance", "Tablet 均衡", "info", "当前 "+v+"；未选择暂停，滚动重启期间可能触发副本补齐 / 均衡")
		}
	}
	add("meta", "FE 元数据备份", "info", "平台在升级每个 FE 前会把 meta_dir 复制为 *.bak-"+plan.From+"（升级前仍建议另行备份）")
	return out
}

func unhealthyTablets(ctx context.Context, cl *sr.Client) (int64, bool) {
	r, err := cl.Query(ctx, "SHOW PROC '/statistic'")
	if err != nil {
		return 0, false
	}
	for i := range r.Data {
		if strings.EqualFold(r.Get(i, "DbId"), "Total") {
			n, _ := strconv.ParseInt(strings.TrimSpace(r.Get(i, "UnhealthyTabletNum")), 10, 64)
			return n, true
		}
	}
	return 0, false
}

func (s *Server) planPrecheck(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 90*time.Second)
	if !ok2 {
		return
	}
	defer done()
	plan, found := s.planByID(x.c.ID, r.PathValue("pid"))
	if !found {
		fail(w, http.StatusNotFound, "升级计划不存在")
		return
	}
	nodes, _ := x.cl.Nodes(x.ctx, x.c.Mode)
	checks := s.upgradeChecks(x.ctx, x.c, plan, nodes, x.cl)
	bads := 0
	for _, c := range checks {
		if c.St == "bad" {
			bads++
		}
	}
	ok(w, map[string]any{"items": checks, "bad": bads})
}

// ---------- 执行 / 回滚 ----------

func (s *Server) planTitle(r *http.Request, verb string) (string, string) {
	cid := r.PathValue("id")
	if p, found := s.planByID(cid, r.PathValue("pid")); found {
		if verb == "回滚" {
			return fmt.Sprintf("回滚集群 %s：%s → %s", cid, p.To, p.From), cid
		}
		return fmt.Sprintf("滚动升级集群 %s：%s → %s", cid, p.From, p.To), cid
	}
	return verb + " " + r.PathValue("pid"), cid
}

func (s *Server) planRun(rollback bool) handler {
	code := "upgrade.execute"
	if rollback {
		code = "upgrade.rollback"
	}
	return func(w http.ResponseWriter, r *http.Request, u *model.User) {
		c, found := s.cluster(w, r)
		if !found {
			return
		}
		plan, found := s.planByID(c.ID, r.PathValue("pid"))
		if !found {
			fail(w, http.StatusNotFound, "升级计划不存在")
			return
		}
		if miss := c.Missing(); len(miss) > 0 {
			fail(w, http.StatusConflict, "该集群的运维必填信息未完善（"+strings.Join(miss, "、")+"）")
			return
		}
		switch {
		case plan.State == "running" || plan.State == "rollingback":
			fail(w, http.StatusConflict, "该计划正在执行")
			return
		case rollback && plan.State == "pending":
			fail(w, http.StatusConflict, "该计划还没有执行过，无需回滚")
			return
		case !rollback && plan.State == "done":
			fail(w, http.StatusConflict, "该计划已完成")
			return
		}
		pp, err := s.resolvePackages(plan.To, plan.Package, "")
		if err != nil && !rollback {
			writeErr(w, err)
			return
		}
		if !s.lockNode(c.ID, "upgrade", "cluster") {
			writeErr(w, errUpgradeBusy)
			return
		}
		verb, title := "滚动升级", fmt.Sprintf("滚动升级 %s：%s → %s", c.ID, plan.From, plan.To)
		if rollback {
			verb, title = "回滚", fmt.Sprintf("回滚 %s：%s → %s", c.ID, plan.To, plan.From)
		}
		t := s.newTask("upgrade", c.ID, title, u.Login, map[string]string{"plan": plan.ID, "dir": map[bool]string{false: "up", true: "down"}[rollback]})
		state := "running"
		if rollback {
			state = "rollingback"
		}
		s.updatePlan(plan.ID, func(p *model.UpgradePlan) { p.State, p.TaskID, p.Note = state, t.ID(), "" })
		s.upControl(plan.ID).paused = false
		s.auditSQL(r, u, code, fmt.Sprintf("%s（任务 %s 已开始）", title, t.ID()), "", nil, c.ID)
		ip := clientIP(r)
		go func() {
			defer s.unlockNode(c.ID, "upgrade", "cluster")
			err := s.runUpgrade(t, c, plan.ID, pp, rollback)
			res := "成功"
			if err != nil {
				res = "失败：" + err.Error()
			}
			s.auditDone(ip, u, c.ID, code, fmt.Sprintf("%s（任务 %s）", title, t.ID()), res)
			final := "done"
			note := ""
			switch {
			case t.Cancelled():
				final, note = "failed", "已取消；已处理的节点会被跳过，可继续执行"
			case err != nil:
				final, note = "failed", err.Error()
			case rollback:
				final = "rolledback"
			}
			s.updatePlan(plan.ID, func(p *model.UpgradePlan) { p.State, p.Note, p.Cur = final, note, "" })
			t.Finish(err)
		}()
		_ = verb
		ok(w, map[string]any{"ok": true, "taskId": t.ID()})
	}
}

func (s *Server) planPause(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if !decode(w, r, &in) {
		return
	}
	plan, found := s.planByID(r.PathValue("id"), r.PathValue("pid"))
	if !found {
		fail(w, http.StatusNotFound, "升级计划不存在")
		return
	}
	if plan.State != "running" && plan.State != "rollingback" && plan.State != "paused" {
		fail(w, http.StatusConflict, "该计划没有在执行")
		return
	}
	s.upControl(plan.ID).paused = in.Paused
	verb := map[bool]string{true: "暂停", false: "继续"}[in.Paused]
	s.auditSQL(r, u, "upgrade.pause", verb+"升级 "+plan.ID+"（当前节点完成后生效）", "", nil, plan.Cluster)
	ok(w, map[string]any{"ok": true, "paused": in.Paused})
}

// waitIfPaused 在节点之间检查暂停开关与取消。
func (s *Server) waitIfPaused(t *taskRun, plan model.UpgradePlan, rollback bool) error {
	ctl := s.upControl(plan.ID)
	paused := false
	for {
		if t.Cancelled() {
			return context.Canceled
		}
		if !ctl.paused {
			if paused {
				t.SetState("running")
				s.updatePlan(plan.ID, func(p *model.UpgradePlan) { p.State = map[bool]string{false: "running", true: "rollingback"}[rollback] })
				t.Logf("已继续")
			}
			return nil
		}
		if !paused {
			paused = true
			t.SetState("paused")
			s.updatePlan(plan.ID, func(p *model.UpgradePlan) { p.State = "paused" })
			t.Logf("已暂停：下一个节点开始前等待继续（点击「继续」或取消任务）")
		}
		select {
		case <-t.ctx.Done():
			return context.Canceled
		case <-time.After(upPauseCheck):
		}
	}
}

func (s *Server) runUpgrade(t *taskRun, c model.Cluster, pid string, pp pkgPlan, rollback bool) error {
	ctx := t.ctx
	plan, _ := s.planByID(c.ID, pid)
	cl, err := s.Mgr.Client(ctx, c)
	if err != nil {
		return fmt.Errorf("连接集群失败：%w", err)
	}
	nodes, warns := cl.Nodes(ctx, c.Mode)
	if len(nodes) == 0 {
		return fmt.Errorf("读取节点列表失败：%s", strings.Join(warns, "；"))
	}
	target, order := plan.To, upgradeOrder(nodes)
	if rollback {
		target, order = plan.From, downgradeOrder(nodes)
		t.Logf("回滚：%s → %s。降级顺序：先 FE（Leader 最后），再 CN / BE。", plan.To, plan.From)
		t.Logf("注意：如果升级后使用过新版本的新特性，旧版本可能无法识别新产生的元数据 / 数据。")
	} else {
		t.Logf("滚动升级：%s → %s。顺序：BE → CN → FE（Observer → Follower → Leader），共 %d 个节点。", plan.From, plan.To, len(nodes))
		checks := s.upgradeChecks(ctx, c, plan, nodes, cl)
		var bads []string
		for _, ck := range checks {
			t.Logf("预检 %s：%s %s", map[string]string{"ok": "✓", "warn": "!", "bad": "✗", "info": "i"}[ck.St], ck.Item, ck.Val)
			if ck.St == "bad" {
				bads = append(bads, ck.Item)
			}
		}
		if len(bads) > 0 {
			return fmt.Errorf("升级前检查未通过：%s", strings.Join(bads, "、"))
		}
		if plan.DisableBalance {
			prev := ""
			if r, err := cl.Query(ctx, "ADMIN SHOW FRONTEND CONFIG LIKE 'tablet_sched_disable_balance'"); err == nil && len(r.Data) > 0 {
				prev = r.Get(0, "Value")
			}
			s.updatePlan(pid, func(p *model.UpgradePlan) {
				if p.PrevBalance == "" {
					p.PrevBalance = prev
				}
			})
			t.Logf("$ ADMIN SET FRONTEND CONFIG (\"tablet_sched_disable_balance\" = \"true\")  # 升级期间暂停 Tablet 均衡")
			if err := s.setFEConfig(ctx, c, cl, "tablet_sched_disable_balance", "true"); err != nil {
				t.Logf("警告：暂停 Tablet 均衡失败：%v（继续升级）", err)
			}
		}
	}
	var pkgLocal model.Package
	if !rollback {
		pkgLocal = pp.pkg
	} else if p, found := s.packageByName(strings.Replace(plan.Package, plan.To, plan.From, 1)); found {
		pkgLocal = p // 回滚时若备份目录丢失，用旧版本安装包兜底
	}
	total := len(order)
	for i, n := range order {
		if err := s.waitIfPaused(t, plan, rollback); err != nil {
			return err
		}
		id := planNodeID(n)
		label := fmt.Sprintf("%s %s", strings.ToUpper(n.Type), nodeKey(n))
		t.SetProg(i * 100 / total)
		t.Logf("—— [%d/%d] %s（当前 %s，目标 %s）——", i+1, total, label, orStr(n.Version, "?"), target)
		s.updatePlan(pid, func(p *model.UpgradePlan) { p.Cur = id })
		skipped := false
		if n.Version == target && n.Alive {
			t.Logf("已经是 %s，跳过", target)
			skipped = true
		} else if rollback {
			if err := s.rollbackNode(ctx, t, c, plan, n, pkgLocal); err != nil {
				return fmt.Errorf("%s：%w", label, err)
			}
		} else {
			if err := s.upgradeNode(ctx, t, c, plan, n, pp); err != nil {
				return fmt.Errorf("%s：%w", label, err)
			}
		}
		s.updatePlan(pid, func(p *model.UpgradePlan) {
			if !contains(p.Done, id) {
				p.Done = append(p.Done, id)
			}
		})
		if skipped {
			continue
		}
		// 每个节点之后：等 Tablet 恢复健康（回滚是应急操作，等不到只警告），再按间隔等待
		if n.Type != "fe" {
			if err := s.waitHealthy(ctx, t, cl); err != nil {
				if !rollback {
					return err
				}
				t.Logf("警告：%v", err)
			}
		}
		if i < total-1 && plan.Interval > 0 && !rollback {
			t.Logf("等待 %d 秒后处理下一个节点…", plan.Interval)
			select {
			case <-ctx.Done():
				return context.Canceled
			case <-time.After(time.Duration(plan.Interval) * time.Second):
			}
		}
	}
	if !rollback && plan.DisableBalance {
		restore := orStr(plan.PrevBalance, "false")
		t.Logf("$ ADMIN SET FRONTEND CONFIG (\"tablet_sched_disable_balance\" = \"%s\")  # 恢复 Tablet 均衡", restore)
		if err := s.setFEConfig(ctx, c, cl, "tablet_sched_disable_balance", restore); err != nil {
			t.Logf("警告：恢复 Tablet 均衡失败：%v，请手动执行", err)
		}
	}
	// 记录集群版本
	newVer := target
	s.Store.Update(func(st *store.State) error {
		for i := range st.Clusters {
			if st.Clusters[i].ID == c.ID {
				st.Clusters[i].Version = newVer
			}
		}
		return nil
	})
	if rollback {
		t.Logf("✓ 回滚完成，集群版本 %s", newVer)
	} else {
		t.Logf("✓ 升级完成，集群版本 %s", newVer)
	}
	return nil
}

// setFEConfig 在所有存活的 FE 上设置 FE 配置（仅内存）。
func (s *Server) setFEConfig(ctx context.Context, c model.Cluster, cl *sr.Client, key, value string) error {
	q, err := sr.SetFEConfigSQL(key, value)
	if err != nil {
		return err
	}
	return s.onAllFEs(srCtx{c: c, cl: cl, ctx: ctx}, q)
}

func (s *Server) waitHealthy(ctx context.Context, t *taskRun, cl *sr.Client) error {
	deadline := time.Now().Add(upHealthWait)
	announced := false
	for {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		n, ok2 := unhealthyTablets(cctx, cl)
		cancel()
		if !ok2 || n == 0 {
			if announced {
				t.Logf("Tablet 已恢复健康")
			}
			return nil
		}
		if !announced {
			announced = true
			t.Logf("等待不健康 Tablet 恢复（当前 %d 个，最长 %d 分钟）…", n, int(upHealthWait.Minutes()))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %d 分钟后仍有 %d 个不健康 Tablet，已停止升级以免影响数据可用性；处理后可继续执行", int(upHealthWait.Minutes()), n)
		}
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-time.After(nodePollEvery):
		}
	}
}

// waitVersion 等待节点存活且版本为 want。
func (s *Server) waitVersion(ctx context.Context, t *taskRun, c model.Cluster, n model.Node, want string) error {
	deadline := time.Now().Add(upVersionWait)
	var last string
	for {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		cl, err := s.Mgr.Client(cctx, c)
		var nodes []model.Node
		if err == nil {
			nodes, _ = cl.Nodes(cctx, c.Mode)
		}
		cancel()
		for _, x := range nodes {
			if x.Type == n.Type && nodeKey(x) == nodeKey(n) {
				last = fmt.Sprintf("版本 %s，Alive=%v", x.Version, x.Alive)
				if x.Alive && x.Version == want {
					t.Logf("节点已存活，版本 %s", x.Version)
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("节点未在 %d 秒内以版本 %s 存活（%s）", int(upVersionWait.Seconds()), want, last)
		}
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-time.After(nodePollEvery):
		}
	}
}

// replaceNodeFiles 换掉 dirs 里的目录：旧目录改名为 <dir>.bak-<oldVer>（已改过名则不重复），再从解压目录复制新的。
func (s *Server) replaceNodeFiles(rm *remote, dir, ex, srcKind, oldVer string) error {
	for _, d := range replaceDirs(srcKind) {
		bak := fmt.Sprintf("%s/%s.bak-%s", dir, d, oldVer)
		if res, _ := rm.quiet("test -d " + bak); res.Code == 0 {
			rm.logf("%s 已有备份 %s，跳过改名", d, bak)
			if _, err := rm.run("rm -rf " + dir + "/" + d); err != nil {
				return err
			}
		} else if res, _ := rm.quiet("test -d " + dir + "/" + d); res.Code == 0 {
			if _, err := rm.run(fmt.Sprintf("mv %s/%s %s", dir, d, bak)); err != nil {
				return err
			}
		}
		src := fmt.Sprintf("%s/%s/%s", ex, map[string]string{"fe": "fe", "be": "be", "cn": "be"}[srcKind], d)
		if res, _ := rm.quiet("test -d " + src); res.Code != 0 {
			if d == "spark-dpp" {
				continue // 新版本包里没有该目录
			}
			return fmt.Errorf("安装包里没有 %s", src)
		}
		if _, err := rm.run(fmt.Sprintf("cp -a %s %s/%s", src, dir, d)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) upgradeNode(ctx context.Context, t *taskRun, c model.Cluster, plan model.UpgradePlan, n model.Node, pp pkgPlan) error {
	tgt, err := s.sshTarget(c, n.Host)
	if err != nil {
		return err
	}
	rm, err := s.dialRemote(ctx, t, tgt)
	if err != nil {
		return err
	}
	defer rm.Close()
	dir := nodeDir(c, n.Type)
	parent := parentDir(dir)
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
	graceful := n.Type != "fe" && verAtLeast(n.Version, 3, 4)
	if err := s.stopNode(ctx, t, rm.sess, dir, n, graceful); err != nil {
		return err
	}
	if n.Type == "fe" { // 升级 FE 前备份元数据
		conf, err := rm.readFile(confPath(dir, "fe"))
		if err != nil {
			return err
		}
		meta, ok2 := confValue(conf, "meta_dir")
		if !ok2 {
			meta = dir + "/meta"
		}
		bak := fmt.Sprintf("%s.bak-%s", strings.TrimRight(meta, "/"), plan.From)
		if res, _ := rm.quiet("test -d " + bak); res.Code == 0 {
			rm.logf("元数据备份 %s 已存在，跳过", bak)
		} else if _, err := rm.run(fmt.Sprintf("cp -a %s %s", meta, bak)); err != nil {
			return fmt.Errorf("备份元数据失败：%w", err)
		}
	}
	if err := s.replaceNodeFiles(rm, dir, ex, n.Type, plan.From); err != nil {
		return err
	}
	if err := s.startNode(ctx, t, c, rm.sess, dir, n); err != nil {
		return err
	}
	if err := s.waitVersion(ctx, t, c, n, plan.To); err != nil {
		return err
	}
	rm.run("rm -rf " + ex)
	return nil
}

func (s *Server) rollbackNode(ctx context.Context, t *taskRun, c model.Cluster, plan model.UpgradePlan, n model.Node, oldPkg model.Package) error {
	tgt, err := s.sshTarget(c, n.Host)
	if err != nil {
		return err
	}
	rm, err := s.dialRemote(ctx, t, tgt)
	if err != nil {
		return err
	}
	defer rm.Close()
	dir := nodeDir(c, n.Type)
	if err := s.stopNode(ctx, t, rm.sess, dir, n, n.Type != "fe" && verAtLeast(n.Version, 3, 4)); err != nil {
		return err
	}
	allBak := true
	for _, d := range replaceDirs(n.Type) {
		if d == "spark-dpp" {
			continue
		}
		if res, _ := rm.quiet(fmt.Sprintf("test -d %s/%s.bak-%s", dir, d, plan.From)); res.Code != 0 {
			allBak = false
		}
	}
	if allBak {
		for _, d := range replaceDirs(n.Type) {
			bak := fmt.Sprintf("%s/%s.bak-%s", dir, d, plan.From)
			if res, _ := rm.quiet("test -d " + bak); res.Code != 0 {
				continue
			}
			if _, err := rm.run(fmt.Sprintf("rm -rf %s/%s", dir, d)); err != nil {
				return err
			}
			if _, err := rm.run(fmt.Sprintf("mv %s %s/%s", bak, dir, d)); err != nil {
				return err
			}
		}
	} else if oldPkg.Name != "" {
		rm.logf("没有找到 %s 备份目录，改用旧版本安装包 %s 重新安装 bin / lib", plan.From, oldPkg.Name)
		parent := parentDir(dir)
		if _, err := rm.run("mkdir -p " + parent + "/.upload"); err != nil {
			return err
		}
		remotePkg := parent + "/.upload/" + oldPkg.Name
		if err := rm.upload(s.pkgPath(oldPkg.Name), oldPkg.Size, oldPkg.SHA, remotePkg); err != nil {
			return err
		}
		ex := fmt.Sprintf("%s/.pkg-%s", parent, oldPkg.Ver)
		if err := rm.extract(remotePkg, ex); err != nil {
			return err
		}
		if err := s.replaceNodeFiles(rm, dir, ex, n.Type, plan.To); err != nil {
			return err
		}
		rm.run("rm -rf " + ex)
	} else {
		return fmt.Errorf("该节点上没有 %s 的备份目录（%s/bin.bak-%s 等），也没有上传 %s 的安装包，无法回滚", plan.From, dir, plan.From, plan.From)
	}
	if err := s.startNode(ctx, t, c, rm.sess, dir, n); err != nil {
		return err
	}
	return s.waitVersion(ctx, t, c, n, plan.From)
}
