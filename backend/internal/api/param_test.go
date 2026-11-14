// @author: kevinkelly
// @license: Apache Licence
// @file: param_test.go
// @time: 2026/10/28

package api

import (
	"strings"
	"testing"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

type paramList struct {
	Items []sr.Param `json:"items"`
	Nodes []string   `json:"nodes"`
}

func (l paramList) get(k string) (sr.Param, bool) {
	for _, p := range l.Items {
		if p.Key == k {
			return p, true
		}
	}
	return sr.Param{}, false
}

func TestParams(t *testing.T) {
	e, base := opsEnv(t, "problems")

	// ---- FE 配置 ----
	var fe paramList
	e.expect(200, "GET", base+"/params/fe", nil, &fe)
	if p, ok := fe.get("qe_slow_log_ms"); !ok || p.Value != "5000" || !p.Mutable || p.Type != "long" {
		t.Fatalf("FE 配置：%+v", fe.Items)
	}
	if p, _ := fe.get("http_port"); p.Mutable {
		t.Fatal("http_port 应为静态参数")
	}
	e.expect(200, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "3000"}, nil)
	fe = paramList{}
	e.expect(200, "GET", base+"/params/fe", nil, &fe)
	if p, _ := fe.get("qe_slow_log_ms"); p.Value != "3000" {
		t.Fatalf("修改未生效：%+v", p)
	}
	a := lastAudit(t, e, "param.edit")
	if !strings.Contains(a.SQL, `ADMIN SET FRONTEND CONFIG ("qe_slow_log_ms" = "3000")`) || !strings.Contains(a.Act, "5000 → 3000") || !strings.Contains(a.Act, "全部 FE") {
		t.Fatalf("审计：%+v", a)
	}
	// 静态参数 / 不存在 / 持久化（无 SSH） / 非法值
	e.expect(400, "PUT", base+"/params/fe/http_port", map[string]any{"value": "9000"}, nil)
	e.expect(404, "PUT", base+"/params/fe/no_such_key", map[string]any{"value": "1"}, nil)
	e.expect(409, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "1", "persist": true}, nil) // 持久化需要集群的 SSH 信息，此集群没有填写
	e.expect(400, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "1\n2"}, nil)
	e.expect(400, "PUT", base+"/params/fe/bad%60key", map[string]any{"value": "1"}, nil)
	e.expect(404, "PUT", base+"/params/zz/qe_slow_log_ms", map[string]any{"value": "1"}, nil)
	e.expect(502, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": `3000" ) ; DROP`}, nil) // 注入内容被当作字符串转义，由 StarRocks 判定值不合法
	if a := lastAudit(t, e, "param.edit"); !strings.Contains(a.SQL, `"3000\" ) ; DROP"`) || a.ResClass != "失败" {
		t.Fatalf("引号应被转义且记为失败：%+v", a)
	}
	// 仅修改某一台 FE
	e.expect(400, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "100", "scope": []string{"9.9.9.9:1"}}, nil)

	// ---- 全局变量 ----
	var vars paramList
	e.expect(200, "GET", base+"/params/var", nil, &vars)
	if p, ok := vars.get("enable_profile"); !ok || p.Value != "false" || p.Default != "false" || p.Changed {
		t.Fatalf("变量：%+v", p)
	}
	e.expect(200, "PUT", base+"/params/var/enable_profile", map[string]any{"value": "true"}, nil)
	vars = paramList{}
	e.expect(200, "GET", base+"/params/var", nil, &vars)
	if p, _ := vars.get("enable_profile"); p.Value != "true" || !p.Changed {
		t.Fatalf("变量修改后应标记已修改：%+v", p)
	}
	if a := lastAudit(t, e, "param.edit"); !strings.Contains(a.SQL, "SET GLOBAL enable_profile = true") {
		t.Fatalf("审计：%+v", a)
	}
	e.expect(200, "PUT", base+"/params/var/time_zone", map[string]any{"value": "UTC"}, nil)
	if a := lastAudit(t, e, "param.edit"); !strings.Contains(a.SQL, `SET GLOBAL time_zone = "UTC"`) {
		t.Fatalf("字符串变量应加引号：%+v", a)
	}
	e.expect(404, "PUT", base+"/params/var/no_such_var", map[string]any{"value": "1"}, nil)
	e.expect(404, "PUT", base+"/params/var/a.b", map[string]any{"value": "1"}, nil)

	// ---- BE 配置 ----
	var be paramList
	e.expect(200, "GET", base+"/params/be", nil, &be)
	p, ok := be.get("cumulative_compaction_num_threads_per_disk")
	if !ok || p.Value != "1" || !p.Mutable || !p.Diff || p.NodeVals != nil || len(be.Nodes) != 8 {
		t.Fatalf("BE 配置（汇总值取多数，标记不一致）：%+v nodes=%v", p, be.Nodes)
	}
	if q, _ := be.get("mem_limit"); q.Mutable || q.Diff {
		t.Fatalf("mem_limit：%+v", q)
	}
	e.expect(200, "PUT", base+"/params/be/base_compaction_num_threads_per_disk", map[string]any{"value": "2"}, nil)
	be = paramList{}
	e.expect(200, "GET", base+"/params/be", nil, &be)
	if q, _ := be.get("base_compaction_num_threads_per_disk"); q.Value != "2" || q.Diff {
		t.Fatalf("所有 BE 应已修改：%+v", q)
	}
	if a := lastAudit(t, e, "param.edit"); strings.Count(a.SQL, "curl -XPOST") != 8 || !strings.Contains(a.Act, "全部 BE / CN（8）") {
		t.Fatalf("应对 6 个 BE 与 2 个 CN 逐台执行：%+v", a)
	}
	e.expect(400, "PUT", base+"/params/be/mem_limit", map[string]any{"value": "80%"}, nil)
	e.expect(404, "PUT", base+"/params/be/no_such_cfg", map[string]any{"value": "1"}, nil)
	e.expect(400, "PUT", base+"/params/be/base_compaction_num_threads_per_disk", map[string]any{"value": "3", "scope": []string{"1.2.3.4:5"}}, nil)

	// ---- 节点对比 ----
	var cmp paramList
	e.expect(200, "GET", base+"/params/be/compare", nil, &cmp)
	q, ok := cmp.get("cumulative_compaction_num_threads_per_disk")
	if len(cmp.Items) != 1 || !ok || len(q.NodeVals) != 8 {
		t.Fatalf("BE 对比应只列出不一致的参数：%+v", cmp.Items)
	}
	vals := map[string]int{}
	for _, v := range q.NodeVals {
		vals[v]++
	}
	if vals["1"] != 7 || vals["2"] != 1 {
		t.Fatalf("各节点取值：%v", q.NodeVals)
	}
	// 把不一致的那台改成与其他一致后，对比结果为空
	var target string
	for n, v := range q.NodeVals {
		if v == "2" {
			target = n
		}
	}
	e.expect(200, "PUT", base+"/params/be/cumulative_compaction_num_threads_per_disk", map[string]any{"value": "1", "scope": []string{target}}, nil)
	cmp = paramList{}
	e.expect(200, "GET", base+"/params/be/compare", nil, &cmp)
	if len(cmp.Items) != 0 {
		t.Fatalf("改一致后不应再有差异：%+v", cmp.Items)
	}
	cmp = paramList{}
	e.expect(200, "GET", base+"/params/fe/compare", nil, &cmp)
	if len(cmp.Items) != 0 || len(cmp.Nodes) == 0 {
		t.Fatalf("FE 对比：%+v", cmp)
	}
	e.expect(404, "GET", base+"/params/var/compare", nil, nil)

	// ---- 变更历史 ----
	var h struct{ Items []model.ParamChange }
	e.expect(200, "GET", base+"/params/history", nil, &h)
	if len(h.Items) < 6 || h.Items[0].Key != "cumulative_compaction_num_threads_per_disk" || h.Items[0].From != "1" || h.Items[0].Persist != "仅内存" {
		t.Fatalf("变更历史（最新在前）：%+v", h.Items)
	}
	var sawFE, sawVar bool
	for _, c := range h.Items {
		sawFE = sawFE || (c.Kind == "fe" && c.Key == "qe_slow_log_ms" && c.From == "5000" && c.To == "3000" && c.Status == "已生效" && c.User == "admin")
		sawVar = sawVar || (c.Kind == "var" && c.Key == "enable_profile" && c.Persist == "—")
	}
	if !sawFE || !sawVar {
		t.Fatalf("历史应包含 FE 与变量的修改：%+v", h.Items)
	}
	for _, c := range h.Items {
		if c.Key == "http_port" || c.Key == "no_such_key" {
			t.Fatalf("校验失败的修改不应记入历史：%+v", c)
		}
	}
}

func TestParamsRBACAndReview(t *testing.T) {
	e, base := opsEnv(t, "healthy")
	e.newUser("oncall4", []string{"r_oncall"}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall4", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	oc.expect(403, "GET", base+"/params/fe", nil, nil) // 值班只读没有参数配置菜单
	oc.expect(403, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "1"}, nil)
	oc.expect(403, "GET", base+"/params/history", nil, nil)

	// 自定义角色：能看参数、能改，但没有变更历史 / 对比权限
	var ro roleOut
	e.expect(200, "POST", "/api/roles", map[string]any{"name": "参数运维", "perms": []string{"param:view", "param.edit"}, "clusters": []string{"*"}}, &ro)
	e.newUser("param1", []string{ro.ID}, "Param@2026xyzabcd")
	pu := e.firstLogin("param1", "Param@2026xyzabcd", "Param@2026xyzabcd2")
	pu.expect(200, "GET", base+"/params/fe", nil, nil)
	pu.expect(403, "GET", base+"/params/history", nil, nil)
	pu.expect(403, "GET", base+"/params/be/compare", nil, nil)

	// 双人复核：修改参数先进入待复核，批准后以申请人身份执行并记入历史
	e.expect(200, "PUT", "/api/settings/security", map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": true, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}, nil)
	e.newUser("admin5", []string{"r_admin"}, "Admin5@2026xyzab")
	admin5 := e.firstLogin("admin5", "Admin5@2026xyzab", "Admin5@2026xyzab2")
	var pend map[string]any
	pu.expect(202, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "4000"}, &pend)
	var fe paramList
	e.expect(200, "GET", base+"/params/fe", nil, &fe)
	if p, _ := fe.get("qe_slow_log_ms"); p.Value != "5000" {
		t.Fatal("待复核期间不应修改参数")
	}
	var rv struct{ Items []reviewOut }
	e.expect(200, "GET", "/api/reviews", nil, &rv)
	if len(rv.Items) != 1 || rv.Items[0].Title != "修改参数 qe_slow_log_ms = 4000" || rv.Items[0].Requester != "param1" {
		t.Fatalf("复核申请：%+v", rv.Items)
	}
	var ap map[string]any
	admin5.expect(200, "POST", "/api/reviews/"+pend["reviewId"].(string)+"/approve", nil, &ap)
	if ap["ok"] != true {
		t.Fatalf("批准失败：%v", ap)
	}
	fe = paramList{}
	e.expect(200, "GET", base+"/params/fe", nil, &fe)
	if p, _ := fe.get("qe_slow_log_ms"); p.Value != "4000" {
		t.Fatalf("批准后应已修改：%+v", p)
	}
	var h struct{ Items []model.ParamChange }
	e.expect(200, "GET", base+"/params/history", nil, &h)
	if len(h.Items) != 1 || h.Items[0].User != "param1" {
		t.Fatalf("历史应记录申请人：%+v", h.Items)
	}
}
