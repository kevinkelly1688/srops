// @author: kevinkelly
// @license: Apache Licence
// @file: ops_test.go
// @time: 2026/10/28

package api

import (
	"strings"
	"testing"
	"time"

	"starrocks-ops/fakesr"
	"starrocks-ops/internal/sr"
)

// opsEnv 登录并接入一个集群 prod-test。
func opsEnv(t *testing.T, scenario string) (*env, string) {
	e := newEnv(t, fakesr.ModeSharedNothing, scenario)
	e.login()
	cl := e.conn()
	cl["name"], cl["env"], cl["owner"] = "prod-test", "生产", "admin"
	var out struct{ ID string }
	e.expect(200, "POST", "/api/clusters", cl, &out)
	return e, "/api/clusters/" + out.ID
}

type rgList struct {
	Groups []struct {
		sr.ResourceGroup
		Running int `json:"running"`
		Queued  int `json:"queued"`
	} `json:"groups"`
	Warnings []string `json:"warnings"`
}

func (l rgList) find(name string) (sr.ResourceGroup, int, bool) {
	for _, g := range l.Groups {
		if g.Name == name {
			return g.ResourceGroup, g.Queued, true
		}
	}
	return sr.ResourceGroup{}, 0, false
}

func lastAudit(t *testing.T, e *env, code string) auditOut {
	t.Helper()
	var p struct{ Items []auditOut }
	e.expect(200, "GET", "/api/audit?limit=5&code="+code, nil, &p)
	if len(p.Items) == 0 {
		t.Fatalf("没有 %s 的审计记录", code)
	}
	return p.Items[0]
}

func TestResourceGroups(t *testing.T) {
	e, base := opsEnv(t, "problems")
	var l rgList
	l = rgList{}
	e.expect(200, "GET", base+"/resource-groups", nil, &l)
	if _, _, ok := l.find("default_wg"); !ok {
		t.Fatal("应包含内置的 default_wg")
	}
	adhoc, queued, ok := l.find("rg_adhoc")
	if !ok || adhoc.CPUWeight != 4 || adhoc.MemPct != 20 || adhoc.Concurrency != 16 || len(adhoc.Classifiers) != 1 || adhoc.Classifiers[0].User != "adhoc" || adhoc.SpillPct != 80 {
		t.Fatalf("rg_adhoc 解析不对：%+v", adhoc)
	}
	if queued != 2 {
		t.Fatalf("rg_adhoc 应有 2 个排队，实际 %d", queued)
	}
	if g, _, _ := l.find("default_wg"); !g.Builtin {
		t.Fatal("default_wg 应标记为内置")
	}

	// 新建
	spec := map[string]any{"name": "rg_new", "cpuWeight": 6, "memPct": 15, "concurrency": 10, "bigCpuSec": 120, "spillPct": 0,
		"classifiers": []map[string]any{{"user": "newbie", "queryType": []string{"select"}}, {"role": "analyst", "sourceIp": "10.1.%"}}}
	e.expect(200, "POST", base+"/resource-groups", spec, nil)
	e.expect(409, "POST", base+"/resource-groups", spec, nil) // 重复
	for name, mut := range map[string]func(m map[string]any){
		"注入名称":    func(m map[string]any) { m["name"] = "x`; DROP" },
		"cpu 双设置": func(m map[string]any) { m["name"] = "rg_b"; m["exclusiveCpuCores"] = 2 },
		"无分类器":    func(m map[string]any) { m["name"] = "rg_c"; m["classifiers"] = []any{} },
		"内置名称":    func(m map[string]any) { m["name"] = "default_wg" },
	} {
		b := map[string]any{}
		for k, v := range spec {
			b[k] = v
		}
		mut(b)
		if code := e.call("POST", base+"/resource-groups", b, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	l = rgList{}
	e.expect(200, "GET", base+"/resource-groups", nil, &l)
	g, _, ok := l.find("rg_new")
	if !ok || g.CPUWeight != 6 || g.MemPct != 15 || g.Concurrency != 10 || g.BigCPUSec != 120 || len(g.Classifiers) != 2 {
		t.Fatalf("新建结果不对：%+v", g)
	}
	if a := lastAudit(t, e, "rg.create"); !strings.Contains(a.SQL, "CREATE RESOURCE GROUP `rg_new` TO (user=\"newbie\"") || a.ResClass != "成功" || a.Cluster == "" {
		t.Fatalf("审计应记录执行的 SQL 与集群：%+v", a)
	}

	// 编辑：改属性、换一个分类器、保留另一个
	spec["cpuWeight"], spec["memPct"] = 9, 25
	spec["classifiers"] = []map[string]any{{"user": "newbie", "queryType": []string{"select"}}, {"db": "ods"}}
	e.expect(200, "PUT", base+"/resource-groups/rg_new", spec, nil)
	l = rgList{}
	e.expect(200, "GET", base+"/resource-groups", nil, &l)
	g, _, _ = l.find("rg_new")
	if g.CPUWeight != 9 || g.MemPct != 25 || len(g.Classifiers) != 2 {
		t.Fatalf("编辑结果不对：%+v", g)
	}
	var hasDB, hasRole bool
	for _, c := range g.Classifiers {
		hasDB = hasDB || c.DB == "ods"
		hasRole = hasRole || c.Role == "analyst"
	}
	if !hasDB || hasRole {
		t.Fatalf("分类器应新增 db=ods 并移除 role=analyst：%+v | %s", g.Classifiers, lastAudit(t, e, "rg.edit").SQL)
	}
	if a := lastAudit(t, e, "rg.edit"); !strings.Contains(a.SQL, "ADD (db=\"ods\")") || !strings.Contains(a.SQL, "DROP (") {
		t.Fatalf("审计应包含 ADD / DROP：%s", a.SQL)
	}
	e.expect(404, "PUT", base+"/resource-groups/not_exist", spec, nil)
	e.expect(400, "PUT", base+"/resource-groups/default_wg", spec, nil)
	e.expect(400, "DELETE", base+"/resource-groups/default_wg", nil, nil)
	e.expect(400, "DELETE", base+"/resource-groups/x%60y", nil, nil)

	// 删除
	e.expect(200, "DELETE", base+"/resource-groups/rg_new", nil, nil)
	l = rgList{}
	e.expect(200, "GET", base+"/resource-groups", nil, &l)
	if _, _, ok := l.find("rg_new"); ok {
		t.Fatal("删除后不应存在")
	}
	e.expect(502, "DELETE", base+"/resource-groups/rg_new", nil, nil) // 再删一次，StarRocks 报错并原样返回
	if a := lastAudit(t, e, "rg.delete"); a.ResClass != "失败" {
		t.Fatalf("失败的删除应记为失败：%+v", a)
	}
}

type queueRes struct {
	Items []sr.QueueItem `json:"items"`
	Pend  int            `json:"pending"`
	Run   int            `json:"running"`
}

func (q queueRes) val(k string) (string, string) {
	for _, it := range q.Items {
		if it.Key == k {
			return it.Value, it.Source
		}
	}
	return "", ""
}

func TestQueryQueue(t *testing.T) {
	e, base := opsEnv(t, "problems")
	var q queueRes
	q = queueRes{}
	e.expect(200, "GET", base+"/query-queue", nil, &q)
	if v, src := q.val("enable_query_queue_select"); v != "false" || src != "var" {
		t.Fatalf("select 队列开关应来自变量：%v %v", v, src)
	}
	if v, src := q.val("query_queue_max_queued_queries"); v != "1024" || src != "fe" {
		t.Fatalf("最大排队数应来自 FE 配置：%v %v", v, src)
	}
	if q.Pend != 2 || q.Run != 1 {
		t.Fatalf("排队 / 运行数不对：%+v", q)
	}
	if _, src := q.val("query_queue_cpu_used_permille_limit"); src != "" {
		t.Fatal("当前版本没有的配置项应省略")
	}

	e.expect(200, "PUT", base+"/query-queue", map[string]any{"values": map[string]string{"enable_query_queue_select": "true", "query_queue_max_queued_queries": "500", "query_queue_concurrency_limit": "40"}}, nil)
	q = queueRes{}
	e.expect(200, "GET", base+"/query-queue", nil, &q)
	if v, _ := q.val("enable_query_queue_select"); v != "true" {
		t.Fatal("变量未生效")
	}
	if v, _ := q.val("query_queue_max_queued_queries"); v != "500" {
		t.Fatal("FE 配置未生效")
	}
	if v, _ := q.val("query_queue_concurrency_limit"); v != "40" {
		t.Fatal("并发上限未生效")
	}
	a := lastAudit(t, e, "queue.edit")
	if !strings.Contains(a.SQL, "SET GLOBAL enable_query_queue_select = true") || !strings.Contains(a.SQL, `ADMIN SET FRONTEND CONFIG ("query_queue_max_queued_queries" = "500")`) {
		t.Fatalf("审计 SQL 不对：%s", a.SQL)
	}
	if !strings.Contains(a.Act, "false → true") {
		t.Fatalf("审计应记录旧值 → 新值：%s", a.Act)
	}
	// 值未变化
	var r map[string]any
	e.expect(200, "PUT", base+"/query-queue", map[string]any{"values": map[string]string{"enable_query_queue_select": "true"}}, &r)
	if r["changed"] != false {
		t.Fatalf("值未变应提示未修改：%v", r)
	}
	for name, v := range map[string]map[string]string{
		"未知项":   {"query_timeout": "10"},
		"非布尔":   {"enable_query_queue_load": "yes"},
		"非数字":   {"query_queue_max_queued_queries": "abc"},
		"负数":    {"query_queue_max_queued_queries": "-1"},
		"超百分比":  {"query_queue_mem_used_pct_limit": "101"},
		"当前版本无": {"query_queue_cpu_used_permille_limit": "10"},
	} {
		if code := e.call("PUT", base+"/query-queue", map[string]any{"values": v}, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
}

type loadsRes struct {
	Jobs   []sr.RoutineLoad `json:"jobs"`
	Recent []sr.LoadJob     `json:"recent"`
	Stats  map[string]int64 `json:"stats"`
}

func (l loadsRes) state(name string) string {
	for _, j := range l.Jobs {
		if j.Name == name {
			return j.State
		}
	}
	return "(不存在)"
}

func TestLoadJobs(t *testing.T) {
	e, base := opsEnv(t, "problems")
	var l loadsRes
	l = loadsRes{}
	e.expect(200, "GET", base+"/loads", nil, &l)
	if len(l.Jobs) != 4 || l.state("rl_events") != "PAUSED" || l.state("rl_orders") != "RUNNING" {
		t.Fatalf("作业列表不对：%+v", l.Jobs)
	}
	var ev sr.RoutineLoad
	for _, j := range l.Jobs {
		if j.Name == "rl_events" {
			ev = j
		}
	}
	if ev.Reason != "offset out of range: partition 7" || ev.Source != "KAFKA" || ev.Total != 1200000000 {
		t.Fatalf("字段解析不对：%+v", ev)
	}
	var clicks sr.RoutineLoad
	for _, j := range l.Jobs {
		if j.Name == "rl_clicks" {
			clicks = j
		}
	}
	if clicks.ErrPct < 0.38 || clicks.ErrPct > 0.40 { // 9,000,000 / 2,300,000,000 = 0.39%
		t.Fatalf("错误率应约 0.39%%：%v", clicks.ErrPct)
	}
	if len(l.Recent) != 3 || l.Stats["CANCELLED"] != 17 {
		t.Fatalf("最近导入 / 统计不对：%d %v", len(l.Recent), l.Stats)
	}

	// 暂停 → 再暂停（StarRocks 报错）→ 恢复
	p := base + "/routine-loads/ods/rl_orders"
	e.expect(200, "POST", p+"/pause", nil, nil)
	l = loadsRes{}
	e.expect(200, "GET", base+"/loads", nil, &l)
	if l.state("rl_orders") != "PAUSED" {
		t.Fatal("暂停未生效")
	}
	e.expect(502, "POST", p+"/pause", nil, nil)
	e.expect(200, "POST", p+"/resume", nil, nil)
	l = loadsRes{}
	e.expect(200, "GET", base+"/loads", nil, &l)
	if s := l.state("rl_orders"); s != "NEED_SCHEDULE" && s != "RUNNING" {
		t.Fatalf("恢复后状态 %s", s)
	}
	if a := lastAudit(t, e, "load.resume"); !strings.Contains(a.SQL, "RESUME ROUTINE LOAD FOR `ods`.`rl_orders`") {
		t.Fatalf("审计 SQL：%s", a.SQL)
	}
	e.expect(400, "POST", base+"/routine-loads/ods/bad%20name/pause", nil, nil)

	// 详情
	var d struct {
		Job      sr.RoutineLoad `json:"job"`
		TaskCols []string       `json:"taskCols"`
		Tasks    [][]string     `json:"tasks"`
	}
	e.expect(200, "GET", base+"/routine-loads/ods/rl_events", nil, &d)
	if d.Job.Reason == "" {
		t.Fatal("详情应包含暂停原因")
	}
	e.expect(404, "GET", base+"/routine-loads/ods/nope", nil, nil)

	// 停止
	e.expect(200, "DELETE", p, nil, nil)
	l = loadsRes{}
	e.expect(200, "GET", base+"/loads", nil, &l)
	if l.state("rl_orders") != "(不存在)" {
		t.Fatal("停止后作业应消失")
	}
}

type mvRes struct {
	Items []sr.MV `json:"items"`
}

func (m mvRes) get(name string) sr.MV {
	for _, x := range m.Items {
		if x.Name == name {
			return x
		}
	}
	return sr.MV{}
}

func TestMaterializedViews(t *testing.T) {
	e, base := opsEnv(t, "problems")
	var m mvRes
	m = mvRes{}
	e.expect(200, "GET", base+"/materialized-views", nil, &m)
	if len(m.Items) != 4 || m.get("mv_user_retention").State != "FAILED" || m.get("mv_user_retention").Error == "" || m.get("mv_order_wide").Active || m.get("mv_order_wide").Inactive == "" {
		t.Fatalf("列表不对：%+v", m.Items)
	}
	p := base + "/materialized-views/ads/mv_user_retention"
	e.expect(200, "POST", p+"/refresh", map[string]any{"force": true}, nil)
	m = mvRes{}
	e.expect(200, "GET", base+"/materialized-views", nil, &m)
	if m.get("mv_user_retention").State != "RUNNING" {
		t.Fatalf("刷新后应为 RUNNING：%+v", m.get("mv_user_retention"))
	}
	if a := lastAudit(t, e, "mv.refresh"); !strings.Contains(a.SQL, "REFRESH MATERIALIZED VIEW `ads`.`mv_user_retention` FORCE") {
		t.Fatalf("审计 SQL：%s", a.SQL)
	}
	e.expect(200, "POST", p+"/cancel", nil, nil)
	e.expect(502, "POST", p+"/cancel", nil, nil) // 已无运行中的刷新
	e.expect(200, "POST", p+"/active", map[string]any{"active": false}, nil)
	m = mvRes{}
	e.expect(200, "GET", base+"/materialized-views", nil, &m)
	if m.get("mv_user_retention").Active {
		t.Fatal("停用未生效")
	}
	e.expect(502, "POST", p+"/refresh", nil, nil) // 未激活不能刷新
	e.expect(200, "POST", p+"/active", map[string]any{"active": true}, nil)
	e.expect(200, "POST", p+"/refresh", map[string]any{"start": "2026-10-01", "end": "2026-10-07"}, nil)
	if a := lastAudit(t, e, "mv.refresh"); !strings.Contains(a.SQL, `PARTITION START ("2026-10-01") END ("2026-10-07")`) {
		t.Fatalf("审计 SQL：%s", a.SQL)
	}
	e.expect(400, "POST", p+"/refresh", map[string]any{"start": "2026-10-01"}, nil)
	e.expect(400, "POST", base+"/materialized-views/ads/mv%27x/refresh", nil, nil)
	time.Sleep(1700 * time.Millisecond)
	m = mvRes{}
	e.expect(200, "GET", base+"/materialized-views", nil, &m)
	if m.get("mv_user_retention").State != "SUCCESS" {
		t.Fatalf("刷新应已完成：%+v", m.get("mv_user_retention"))
	}
}

func TestOpsRBACAndReview(t *testing.T) {
	e, base := opsEnv(t, "problems")
	e.newUser("oncall1", []string{"r_oncall"}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall1", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	// 值班只读：能看导入作业，不能暂停；没有资源隔离菜单权限
	oc.expect(200, "GET", base+"/loads", nil, nil)
	oc.expect(403, "POST", base+"/routine-loads/ods/rl_orders/pause", nil, nil)
	oc.expect(403, "GET", base+"/resource-groups", nil, nil)
	oc.expect(403, "POST", base+"/resource-groups", map[string]any{}, nil)
	oc.expect(403, "PUT", base+"/query-queue", map[string]any{"values": map[string]string{"enable_query_queue_load": "true"}}, nil)
	oc.expect(403, "POST", base+"/materialized-views/ads/mv_sales_daily/refresh", nil, nil)
	if a := lastAudit(t, e, "load.pause"); a.ResClass != "拒绝" || a.User != "oncall1" {
		t.Fatalf("越权应记为拒绝：%+v", a)
	}
	// DBA 数据范围为空：看不到该集群
	e.newUser("dba1", []string{"r_dba"}, "Dba@2026xyzabcde")
	dba := e.firstLogin("dba1", "Dba@2026xyzabcde", "Dba@2026xyzabcde2")
	dba.expect(403, "GET", base+"/resource-groups", nil, nil)
	dba.expect(403, "GET", base+"/loads", nil, nil)

	// 双人复核：删除资源组 / 停止 Routine Load 先进入待复核
	e.expect(200, "PUT", "/api/settings/security", map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": true, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}, nil)
	e.newUser("admin2", []string{"r_admin"}, "Admin2@2026xyzab")
	admin2 := e.firstLogin("admin2", "Admin2@2026xyzab", "Admin2@2026xyzab2")
	var pend map[string]any
	e.expect(202, "DELETE", base+"/resource-groups/rg_etl", nil, &pend)
	rid1 := pend["reviewId"].(string)
	e.expect(202, "DELETE", base+"/routine-loads/ods/rl_clicks", nil, &pend)
	rid2 := pend["reviewId"].(string)
	var l rgList
	l = rgList{}
	e.expect(200, "GET", base+"/resource-groups", nil, &l)
	if _, _, ok := l.find("rg_etl"); !ok {
		t.Fatal("待复核期间资源组不应被删除")
	}
	var rv struct{ Items []reviewOut }
	e.expect(200, "GET", "/api/reviews", nil, &rv)
	titles := map[string]string{}
	for _, it := range rv.Items {
		titles[it.Code] = it.Title
		if it.Cluster == "" {
			t.Errorf("复核申请应记录集群：%+v", it)
		}
	}
	if titles["rg.delete"] != "删除资源组 rg_etl" || titles["load.stop"] != "停止 Routine Load ods.rl_clicks" {
		t.Fatalf("复核标题不对：%v", titles)
	}
	var ap map[string]any
	admin2.expect(200, "POST", "/api/reviews/"+rid1+"/approve", nil, &ap)
	admin2.expect(200, "POST", "/api/reviews/"+rid2+"/approve", nil, &ap)
	if ap["ok"] != true {
		t.Fatalf("批准失败：%v", ap)
	}
	l = rgList{}
	e.expect(200, "GET", base+"/resource-groups", nil, &l)
	if _, _, ok := l.find("rg_etl"); ok {
		t.Fatal("批准后资源组应被删除")
	}
	var ls loadsRes
	ls = loadsRes{}
	e.expect(200, "GET", base+"/loads", nil, &ls)
	if ls.state("rl_clicks") != "(不存在)" {
		t.Fatal("批准后作业应被停止")
	}
	// 暂停 / 恢复 / 新建不属于高危，不走复核
	e.expect(200, "POST", base+"/routine-loads/ods/rl_orders/pause", nil, nil)
}
