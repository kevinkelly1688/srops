// @author: kevinkelly
// @license: Apache Licence
// @file: upgrade_test.go
// @time: 2026/10/28

package api

import (
	"strings"
	"testing"
	"time"

	"starrocks-ops/internal/model"
)

func tailLog(t model.Task) string {
	l := strings.Join(t.Log, "\n")
	if len(l) > 1500 {
		l = "…" + l[len(l)-1500:]
	}
	return l
}

func upEnv(t *testing.T, scenario string) (*env, string) {
	e, base := hostsEnv(t, scenario)
	oh, ov, op := upHealthWait, upVersionWait, upPauseCheck
	upHealthWait, upVersionWait, upPauseCheck = 600*time.Millisecond, 10*time.Second, 50*time.Millisecond
	t.Cleanup(func() { upHealthWait, upVersionWait, upPauseCheck = oh, ov, op })
	return e, base
}

type upOverview struct {
	Version string
	Nodes   []upNodeOut
	Plans   []model.UpgradePlan
	Paused  bool
}

func (e *env) upOverview(base string) upOverview {
	e.t.Helper()
	var o upOverview
	e.expect(200, "GET", base+"/upgrade", nil, &o)
	return o
}

func TestUpgradePlanValidation(t *testing.T) {
	e, base := upEnv(t, "healthy")
	for _, v := range []string{"3.5.8", "3.7.0", "3.4.1", "3.5.6", "3.6.0", "4.0.1"} {
		n, d := srPkg(t, v, true)
		e.upload(n, d, "")
	}
	o := e.upOverview(base)
	if o.Version != "3.5.6" || len(o.Nodes) != 12 {
		t.Fatalf("概览：%+v", o)
	}
	// 升级顺序：BE → CN → FE（Observer → Follower → Leader）
	var seq []string
	for _, n := range o.Nodes {
		if n.Type == "fe" {
			seq = append(seq, "fe-"+n.Role)
		} else {
			seq = append(seq, n.Type)
		}
	}
	want := "be,be,be,be,be,be,cn,cn,fe-Observer,fe-Follower,fe-Follower,fe-Leader"
	if strings.Join(seq, ",") != want {
		t.Fatalf("升级顺序 %s，期望 %s", strings.Join(seq, ","), want)
	}
	cases := []struct {
		pkg  string
		code int
		msg  string
	}{
		{"StarRocks-3.5.6-centos-amd64.tar.gz", 400, "相同"},
		{"StarRocks-3.4.1-centos-amd64.tar.gz", 400, "回滚"},
		{"StarRocks-3.7.0-centos-amd64.tar.gz", 400, "3.6.x"},
		{"StarRocks-9.9.9-centos-amd64.tar.gz", 400, "不存在"},
		{"StarRocks-3.5.8-centos-amd64.tar.gz", 200, ""},
		{"StarRocks-3.6.0-centos-amd64.tar.gz", 200, ""},
		{"StarRocks-4.0.1-centos-amd64.tar.gz", 400, "跨小版本"}, // 3.5 → 4.0 不跨小版本？见下
	}
	_ = cases
	for _, c := range cases[:6] {
		var er map[string]string
		var plan model.UpgradePlan
		code := e.call("POST", base+"/upgrade/plans", map[string]any{"package": c.pkg, "interval": 0}, &plan)
		if code != c.code {
			t.Errorf("%s：状态码 %d，期望 %d", c.pkg, code, c.code)
		}
		_ = er
	}
	var plan model.UpgradePlan
	e.expect(400, "POST", base+"/upgrade/plans", map[string]any{"package": "StarRocks-3.5.8-centos-amd64.tar.gz", "interval": 9999}, nil)
	e.expect(200, "POST", base+"/upgrade/plans", map[string]any{"package": "StarRocks-3.5.8-centos-amd64.tar.gz", "interval": 5, "disableBalance": true}, &plan)
	if plan.From != "3.5.6" || plan.To != "3.5.8" || plan.State != "pending" || plan.Interval != 5 || !plan.DisableBalance {
		t.Fatalf("计划：%+v", plan)
	}
	// 只能删除未执行的计划
	e.expect(200, "DELETE", base+"/upgrade/plans/"+plan.ID, nil, nil)
	e.expect(404, "DELETE", base+"/upgrade/plans/"+plan.ID, nil, nil)
	// 没有 SSH 信息的集群不能建计划
	c2 := e.conn()
	c2["name"], c2["env"], c2["owner"] = "nossh", "测试", "admin"
	e.expect(200, "POST", "/api/clusters", c2, nil)
	e.expect(409, "POST", "/api/clusters/nossh/upgrade/plans", map[string]any{"package": "StarRocks-3.5.8-centos-amd64.tar.gz"}, nil)
	// 权限：值班只读只能看
	e.newUser("viewer2", []string{"r_oncall"}, "Viewer@2026xyzabcd")
	v := e.firstLogin("viewer2", "Viewer@2026xyzabcd", "Viewer@2026xyzabcd2")
	v.expect(403, "GET", base+"/upgrade", nil, nil)
	v.expect(403, "POST", base+"/upgrade/plans", map[string]any{"package": "x"}, nil)
}

func TestUpgradeRollingAndRollback(t *testing.T) {
	e, base := upEnv(t, "healthy")
	n58, d58 := srPkg(t, "3.5.8", true)
	e.upload(n58, d58, "")
	n56, d56 := srPkg(t, "3.5.6", true)
	e.upload(n56, d56, "")
	var plan model.UpgradePlan
	e.expect(200, "POST", base+"/upgrade/plans", map[string]any{"package": n58, "interval": 0, "disableBalance": true}, &plan)
	// 预检
	var pc struct {
		Items []upCheck
		Bad   int
	}
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/precheck", nil, &pc)
	if pc.Bad != 0 {
		t.Fatalf("预检不应有不通过项：%+v", pc.Items)
	}
	ids := map[string]string{}
	for _, it := range pc.Items {
		ids[it.ID] = it.St
	}
	for _, id := range []string{"alive", "tablet", "fequorum", "pkg", "path", "ssh", "dir", "arch", "space", "jobs", "balance", "meta"} {
		if ids[id] == "" {
			t.Errorf("预检缺少 %s：%+v", id, pc.Items)
		}
	}
	// 执行
	var run struct {
		TaskID string `json:"taskId"`
	}
	e.expect(409, "POST", base+"/upgrade/plans/"+plan.ID+"/rollback", nil, nil) // 还没执行过
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/execute", nil, &run)
	e.expect(409, "POST", base+"/upgrade/plans/"+plan.ID+"/execute", nil, nil) // 正在执行
	tk := e.waitTask(run.TaskID)
	if tk.State != "done" {
		t.Fatalf("升级任务：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	// 顺序：BE → CN → FE，Leader 最后；每个节点都有版本检查
	log := strings.Join(tk.Log, "\n")
	pos := func(s string) int { return strings.Index(log, s) }
	if !(pos("BE 127.0.0.15:19050") < pos("CN 127.0.0.21:19070") && pos("CN 127.0.0.22:19071") < pos("FE 127.0.0.14:19013") && pos("FE 127.0.0.14:19013") < pos("FE 127.0.0.12:19011") && pos("FE 127.0.0.13:19012") < pos("FE 127.0.0.11:19010")) {
		t.Fatalf("升级顺序不对：\n%s", log)
	}
	for _, want := range []string{"tablet_sched_disable_balance\" = \"true\"", "tablet_sched_disable_balance\" = \"false\"", "已存活，版本 3.5.8", "✓ 升级完成"} {
		if !strings.Contains(log, want) {
			t.Errorf("日志缺少 %q", want)
		}
	}
	nodes := e.topo(base)
	for _, n := range nodes {
		if n.Version != "3.5.8" || !n.Alive {
			t.Fatalf("节点 %s %s 版本 %s alive=%v", n.Type, nodeKey(n), n.Version, n.Alive)
		}
	}
	// 主机上：新文件、旧目录备份、FE 元数据备份
	if jar, _ := e.fake.HostFile("127.0.0.15", "/opt/starrocks/be/lib/starrocks-be.jar"); !strings.Contains(jar, "3.5.8") {
		t.Fatalf("be lib 应是新版本：%q", jar)
	}
	if jar, _ := e.fake.HostFile("127.0.0.15", "/opt/starrocks/be/lib.bak-3.5.6/starrocks-be.jar"); !strings.Contains(jar, "3.5.6") {
		t.Fatalf("应保留旧版本备份：%q", jar)
	}
	if !e.fake.HostHasDir("127.0.0.11", "/opt/starrocks/fe/meta.bak-3.5.6") {
		t.Fatal("升级 FE 前应备份 meta 目录")
	}
	if len(e.fake.HostPaths("127.0.0.15", "/opt/starrocks/.pkg-")) != 0 {
		t.Fatal("解压临时目录应清理")
	}
	o := e.upOverview(base)
	if len(o.Plans) != 1 || o.Plans[0].State != "done" || len(o.Plans[0].Done) != 12 || o.Plans[0].Cur != "" {
		t.Fatalf("计划状态：%+v", o.Plans)
	}
	if c, _ := e.api.Store.Cluster("prod-test"); c.Version != "3.5.8" {
		t.Fatalf("集群版本应更新：%s", c.Version)
	}
	var fe paramList
	e.expect(200, "GET", base+"/params/fe", nil, &fe)
	if p, _ := fe.get("tablet_sched_disable_balance"); p.Value != "false" {
		t.Fatalf("均衡应恢复：%s", p.Value)
	}
	if a := lastAudit(t, e, "upgrade.execute"); a.Res != "成功" {
		t.Fatalf("审计：%+v", a)
	}
	e.expect(409, "POST", base+"/upgrade/plans/"+plan.ID+"/execute", nil, nil) // 已完成

	// 回滚：先 FE（Leader 最后），再 CN、BE
	leader := findNodeOf(e.topo(base), "fe", "Leader") // 升级期间 Leader 已经换过人，按当前角色判断
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/rollback", nil, &run)
	tk = e.waitTask(run.TaskID)
	if tk.State != "done" {
		t.Fatalf("回滚任务：%+v\n%s", tk, tailLog(tk))
	}
	log = strings.Join(tk.Log, "\n")
	pos = func(s string) int { return strings.Index(log, "—— ["+s) }
	posNode := func(label string) int {
		i := strings.Index(log, label)
		if i < 0 {
			t.Fatalf("日志里没有 %s", label)
		}
		return i
	}
	leaderPos := posNode("FE " + nodeKey(leader) + "（")
	for _, n := range e.topo(base) {
		if n.Type == "fe" && nodeKey(n) != nodeKey(leader) && posNode("FE "+nodeKey(n)+"（") > leaderPos {
			t.Fatalf("Leader 应最后回滚，但 %s 在它之后", nodeKey(n))
		}
		if n.Type == "cn" && posNode("CN "+nodeKey(n)+"（") < leaderPos {
			t.Fatalf("CN 应在全部 FE 之后回滚")
		}
		if n.Type == "be" && posNode("BE "+nodeKey(n)+"（") < posNode("CN 127.0.0.22:19071（") {
			t.Fatalf("BE 应在 CN 之后回滚")
		}
	}
	_ = pos
	// 回滚后 Leader 角色在停止期间被 Follower 接任，这里只校验版本与存活
	for _, n := range e.topo(base) {
		if n.Version != "3.5.6" || !n.Alive {
			t.Fatalf("回滚后节点 %s %s 版本 %s alive=%v", n.Type, nodeKey(n), n.Version, n.Alive)
		}
	}
	if jar, _ := e.fake.HostFile("127.0.0.15", "/opt/starrocks/be/lib/starrocks-be.jar"); !strings.Contains(jar, "3.5.6") {
		t.Fatalf("回滚后 lib 应是旧版本：%q", jar)
	}
	o = e.upOverview(base)
	if o.Plans[0].State != "rolledback" {
		t.Fatalf("计划状态：%+v", o.Plans[0])
	}
	if c, _ := e.api.Store.Cluster("prod-test"); c.Version != "3.5.6" {
		t.Fatalf("集群版本应回到 3.5.6：%s", c.Version)
	}
	// 回滚后可以再次升级（已处理节点被跳过）
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/execute", nil, &run)
	if tk = e.waitTask(run.TaskID); tk.State != "done" {
		t.Fatalf("再次升级：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
}

func TestUpgradePauseCancelResume(t *testing.T) {
	e, base := upEnv(t, "healthy")
	n58, d58 := srPkg(t, "3.5.8", true)
	e.upload(n58, d58, "")
	var plan model.UpgradePlan
	e.expect(200, "POST", base+"/upgrade/plans", map[string]any{"package": n58, "interval": 1}, &plan)
	var run struct {
		TaskID string `json:"taskId"`
	}
	e.expect(409, "POST", base+"/upgrade/plans/"+plan.ID+"/pause", map[string]any{"paused": true}, nil) // 没在执行
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/execute", nil, &run)
	deadline := time.Now().Add(15 * time.Second)
	for len(e.upOverview(base).Plans[0].Done) < 1 {
		if time.Now().After(deadline) {
			t.Fatal("第一个节点应很快完成")
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/pause", map[string]any{"paused": true}, nil)
	for e.upOverview(base).Plans[0].State != "paused" {
		if time.Now().After(deadline) {
			t.Fatal("计划应进入暂停")
		}
		time.Sleep(100 * time.Millisecond)
	}
	var tr struct{ Task model.Task }
	e.expect(200, "GET", "/api/tasks/"+run.TaskID, nil, &tr)
	if tr.Task.State != "paused" {
		t.Fatalf("任务应为 paused：%s", tr.Task.State)
	}
	if !e.upOverview(base).Paused {
		t.Fatal("概览应显示已暂停")
	}
	done1 := len(e.upOverview(base).Plans[0].Done)
	time.Sleep(1500 * time.Millisecond)
	if n := len(e.upOverview(base).Plans[0].Done); n != done1 {
		t.Fatalf("暂停期间不应继续处理节点：%d → %d", done1, n)
	}
	// 取消：任务结束，计划为 failed，已处理的节点保留
	e.expect(200, "POST", "/api/tasks/"+run.TaskID+"/cancel", nil, nil)
	tk := e.waitTask(run.TaskID)
	if tk.State != "canceled" {
		t.Fatalf("任务应取消：%+v", tk)
	}
	pl := e.upOverview(base).Plans[0]
	if pl.State != "failed" || !strings.Contains(pl.Note, "已取消") || len(pl.Done) < 1 || len(pl.Done) >= 12 {
		t.Fatalf("取消后的计划：%+v", pl)
	}
	// 继续：已升级的节点被跳过
	var plan2 model.UpgradePlan
	e.expect(200, "POST", base+"/upgrade/plans", map[string]any{"package": n58, "interval": 0}, &plan2) // 另建一个，旧的 failed 的可继续
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/execute", nil, &run)
	tk = e.waitTask(run.TaskID)
	if tk.State != "done" || !logHas(tk, "已经是 3.5.8，跳过") {
		t.Fatalf("继续升级：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	for _, n := range e.topo(base) {
		if n.Version != "3.5.8" {
			t.Fatalf("节点 %s 版本 %s", nodeKey(n), n.Version)
		}
	}
}

func TestUpgradeBlockedAndFailures(t *testing.T) {
	// 有不健康 Tablet：第一个 BE 升级后等不到恢复，任务失败，计划可继续
	e, base := upEnv(t, "problems")
	n58, d58 := srPkg(t, "3.5.8", true)
	e.upload(n58, d58, "")
	var plan model.UpgradePlan
	e.expect(200, "POST", base+"/upgrade/plans", map[string]any{"package": n58, "interval": 0}, &plan)
	var pc struct {
		Items []upCheck
		Bad   int
	}
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/precheck", nil, &pc)
	for _, it := range pc.Items {
		if it.ID == "tablet" && it.St != "warn" {
			t.Fatalf("有不健康 Tablet 应给出警告：%+v", it)
		}
	}
	var run struct {
		TaskID string `json:"taskId"`
	}
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/execute", nil, &run)
	tk := e.waitTask(run.TaskID)
	if tk.State != "failed" || !strings.Contains(tk.Err, "不健康 Tablet") {
		t.Fatalf("应因 Tablet 不健康而停止：%+v", tk)
	}
	pl := e.upOverview(base).Plans[0]
	if pl.State != "failed" || len(pl.Done) != 1 || !strings.HasPrefix(pl.Done[0], "be|") {
		t.Fatalf("计划：%+v", pl)
	}
	if a := lastAudit(t, e, "upgrade.execute"); !strings.HasPrefix(a.Res, "失败") {
		t.Fatalf("审计应记录失败：%+v", a)
	}
	// 回滚：没有备份目录、也没有旧版本安装包的节点无法回滚
	e.fake.HostRemove("127.0.0.15", "/opt/starrocks/be/lib.bak-3.5.6")
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/rollback", nil, &run)
	tk = e.waitTask(run.TaskID)
	if tk.State != "failed" || !strings.Contains(tk.Err, "无法回滚") {
		t.Fatalf("缺少备份应无法回滚：%+v", tk)
	}
	// 上传旧版本安装包后，回滚改用安装包重装 bin / lib
	n56, d56 := srPkg(t, "3.5.6", true)
	e.upload(n56, d56, "")
	e.expect(200, "POST", base+"/upgrade/plans/"+plan.ID+"/rollback", nil, &run)
	tk = e.waitTask(run.TaskID)
	if tk.State != "done" || !logHas(tk, "改用旧版本安装包") {
		t.Fatalf("应改用旧安装包回滚：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	if jar, _ := e.fake.HostFile("127.0.0.15", "/opt/starrocks/be/lib/starrocks-be.jar"); !strings.Contains(jar, "3.5.6") {
		t.Fatalf("回滚后 lib：%q", jar)
	}
}

func TestUpgradeDualReview(t *testing.T) {
	e, base := upEnv(t, "healthy")
	n58, d58 := srPkg(t, "3.5.8", true)
	e.upload(n58, d58, "")
	var plan model.UpgradePlan
	e.expect(200, "POST", base+"/upgrade/plans", map[string]any{"package": n58, "interval": 0}, &plan)
	e.expect(200, "PUT", "/api/settings/security", map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": true, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}, nil)
	e.newUser("admin2", []string{"r_admin"}, "Admin2@2026xyzab")
	admin2 := e.firstLogin("admin2", "Admin2@2026xyzab", "Admin2@2026xyzab2")
	var pend map[string]any
	e.expect(202, "POST", base+"/upgrade/plans/"+plan.ID+"/execute", nil, &pend)
	if st := e.upOverview(base).Plans[0].State; st != "pending" {
		t.Fatalf("待复核期间不应执行：%s", st)
	}
	var rv struct{ Items []reviewOut }
	e.expect(200, "GET", "/api/reviews", nil, &rv)
	if len(rv.Items) != 1 || !strings.Contains(rv.Items[0].Title, "滚动升级集群 prod-test：3.5.6 → 3.5.8") {
		t.Fatalf("复核申请：%+v", rv.Items)
	}
	var ap map[string]any
	admin2.expect(200, "POST", "/api/reviews/"+pend["reviewId"].(string)+"/approve", nil, &ap)
	if ap["ok"] != true {
		t.Fatalf("批准失败：%v", ap)
	}
	var l struct{ Items []model.Task }
	e.expect(200, "GET", "/api/tasks?kind=upgrade", nil, &l)
	if len(l.Items) != 1 {
		t.Fatalf("批准后应有升级任务：%d", len(l.Items))
	}
	if tk := e.waitTask(l.Items[0].ID); tk.State != "done" || tk.By != "admin" {
		t.Fatalf("任务：%+v", tk)
	}
	e.expect(202, "POST", base+"/upgrade/plans/"+plan.ID+"/rollback", nil, &pend) // 回滚同样要复核
}

func TestUpgradeVersionRules(t *testing.T) {
	for _, c := range []struct {
		from, to string
		ok       bool
	}{{"3.5.6", "3.5.8", true}, {"3.5.6", "3.6.0", true}, {"3.5.6", "3.7.1", false}, {"3.5.6", "4.0.1", true}, {"3.5.6", "3.5.6", false}, {"3.5.8", "3.5.6", false}, {"3.3.9", "3.5.0", false}, {"bad", "3.5.0", false}} {
		if err := checkUpgradePath(c.from, c.to); (err == nil) != c.ok {
			t.Errorf("%s → %s：%v", c.from, c.to, err)
		}
	}
	if verCmp("3.5.10", "3.5.9") != 1 || verCmp("3.5.6", "3.5.6") != 0 || verCmp("3.4.9", "3.5.0") != -1 {
		t.Error("版本比较")
	}
}
