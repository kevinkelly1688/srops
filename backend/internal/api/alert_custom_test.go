// @author: kevinkelly
// @license: Apache Licence
// @file: alert_custom_test.go
// @time: 2026/10/28

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"starrocks-ops/internal/model"
)

// fakeProm 是一个只支持即时查询的假 Prometheus：按完整表达式返回预设的向量。
type fakeProm struct {
	srv  *httptest.Server
	mu   sync.Mutex
	vec  map[string][][2]any // 表达式 → [instance, 值]
	errs map[string]string   // 表达式 → 业务错误
	seen []string
	down bool
}

func newFakeProm(t *testing.T) *fakeProm {
	p := &fakeProm{vec: map[string][][2]any{}, errs: map[string]string{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		q := r.URL.Query().Get("query")
		p.seen = append(p.seen, q)
		if p.down {
			http.Error(w, "down", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if msg, bad := p.errs[q]; bad {
			json.NewEncoder(w).Encode(map[string]any{"status": "error", "errorType": "bad_data", "error": msg})
			return
		}
		var res []map[string]any
		for _, v := range p.vec[q] {
			res = append(res, map[string]any{"metric": map[string]string{"__name__": "m", "job": "x", "instance": v[0].(string)}, "value": []any{1.0, strconv.FormatFloat(v[1].(float64), 'f', -1, 64)}})
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": res}})
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProm) set(q string, inst string, v float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vec[q] = [][2]any{{inst, v}}
}

func (p *fakeProm) queries() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.seen...)
}

func setProm(e *env, url, selector string) {
	e.api.Store.Update(func(st *storeState) error {
		st.Clusters[0].Prom, st.Clusters[0].PromSelector = url, selector
		return nil
	})
}

func TestCustomRuleLifecycle(t *testing.T) {
	e, base := opsEnv(t, "healthy")
	p := newFakeProm(t)
	setProm(e, p.srv.URL, `job="sr_prod"`)
	h := newHook(t)
	var ch model.Channel
	e.expect(200, "POST", "/api/alert-channels", map[string]any{"type": "Webhook", "name": "Hook", "url": h.srv.URL + "/a", "levels": []string{"critical", "warning"}}, &ch)

	const expr = `max(node_filesystem_used_pct{$sel,mount="/data"}) by (instance)`
	exp := `max(node_filesystem_used_pct{job="sr_prod",mount="/data"}) by (instance)`
	var cr customOut
	e.expect(200, "POST", "/api/alert-rules/custom", map[string]any{"name": "数据盘使用率", "expr": expr, "op": ">=", "warn": 80, "crit": 90, "durMin": 0, "unit": "%", "channels": []string{ch.ID}}, &cr)
	if !strings.HasPrefix(cr.ID, "custom_") || !cr.Enabled || cr.Op != ">=" {
		t.Fatalf("创建结果：%+v", cr)
	}
	p.set(exp, "10.0.0.1:9100", 92.5)
	now := time.Now()
	e.api.EvalAlerts(t.Context(), now)
	found := false
	for _, q := range p.queries() {
		found = found || q == exp
	}
	if !found {
		t.Fatalf("$sel 应替换为集群的选择器：%v", p.queries())
	}
	a, ok := e.alerts(false).find(cr.ID, "10.0.0.1:9100")
	if !ok || a.Level != "critical" || a.Value != "92.5%" || a.Name != "数据盘使用率" || !strings.Contains(a.Desc, "当前值 92.5%") {
		t.Fatalf("应产生严重告警：%+v", a)
	}

	// 值降到警告区间：同一条告警，级别随之变化
	p.set(exp, "10.0.0.1:9100", 85)
	e.api.EvalAlerts(t.Context(), now.Add(time.Minute))
	l := e.alerts(false)
	if a, _ = l.find(cr.ID, "10.0.0.1:9100"); a.Level != "warning" || len(l.Items) != 1 {
		t.Fatalf("应降为警告且不重复：%+v", l.Items)
	}

	// 通知：合并窗口满后发送，标题里是自定义规则名
	e.api.EvalAlerts(t.Context(), now.Add(6*time.Minute))
	if h.count() == 0 || !strings.Contains(h.last(), "数据盘使用率") {
		t.Fatalf("应发送通知并带规则名：%d %s", h.count(), h.last())
	}

	// 查询失败（Prometheus 不可用 / 表达式错误）不能当成恢复
	p.mu.Lock()
	p.down = true
	p.mu.Unlock()
	e.api.EvalAlerts(t.Context(), now.Add(7*time.Minute))
	if _, ok := e.alerts(false).find(cr.ID, ""); !ok {
		t.Fatal("Prometheus 不可用时告警应保持")
	}
	var rules []customOut
	e.expect(200, "GET", "/api/alert-rules/custom", nil, &rules)
	if len(rules) != 1 || len(rules[0].Status) != 1 || !strings.Contains(rules[0].Status[0], "查询失败") {
		t.Fatalf("应展示最近一次评估失败：%+v", rules)
	}
	p.mu.Lock()
	p.down = false
	p.mu.Unlock()

	// 值恢复正常：告警进入历史
	p.set(exp, "10.0.0.1:9100", 20)
	e.api.EvalAlerts(t.Context(), now.Add(8*time.Minute))
	if _, ok := e.alerts(false).find(cr.ID, ""); ok {
		t.Fatal("值恢复后告警应关闭")
	}
	if _, ok := e.alerts(true).find(cr.ID, ""); !ok {
		t.Fatal("已恢复的告警应出现在历史里")
	}
	rules = nil
	e.expect(200, "GET", "/api/alert-rules/custom", nil, &rules)
	if rules[0].Status[0] != "prod-test：1 条序列" {
		t.Fatalf("评估状态：%v", rules[0].Status)
	}

	// 对象消失（序列没有了）视为恢复；停用后已有告警关闭
	p.set(exp, "10.0.0.2:9100", 99)
	e.api.EvalAlerts(t.Context(), now.Add(9*time.Minute))
	if _, ok := e.alerts(false).find(cr.ID, "10.0.0.2"); !ok {
		t.Fatal("新对象应告警")
	}
	e.expect(200, "PUT", "/api/alert-rules/custom/"+cr.ID, map[string]any{"enabled": false}, &cr)
	if cr.Enabled {
		t.Fatal("应已停用")
	}
	e.api.EvalAlerts(t.Context(), now.Add(10*time.Minute))
	if _, ok := e.alerts(false).find(cr.ID, ""); ok {
		t.Fatal("停用后告警应关闭")
	}

	var cr2 customOut
	// 整体编辑：改为 ≤，警告阈值不传 = 不设警告
	e.expect(200, "PUT", "/api/alert-rules/custom/"+cr.ID, map[string]any{"name": "数据盘剩余", "expr": expr, "op": "<=", "crit": 10, "durMin": 0, "enabled": true}, &cr2)
	cr = cr2
	if cr.Warn != nil || cr.Op != "<=" || cr.Name != "数据盘剩余" || !cr.Enabled {
		t.Fatalf("编辑后：%+v", cr)
	}
	p.set(exp, "10.0.0.3:9100", 8)
	e.api.EvalAlerts(t.Context(), now.Add(11*time.Minute))
	if a, ok = e.alerts(false).find(cr.ID, "10.0.0.3"); !ok || a.Level != "critical" {
		t.Fatalf("≤ 规则：%+v", a)
	}
	p.set(exp, "10.0.0.3:9100", 50)
	e.api.EvalAlerts(t.Context(), now.Add(12*time.Minute))
	if _, ok = e.alerts(false).find(cr.ID, ""); ok {
		t.Fatal("≤ 规则恢复")
	}

	// 静默可以选自定义规则；删除规则后其告警立即关闭
	e.expect(200, "POST", "/api/silences", map[string]any{"cluster": base[len("/api/clusters/"):], "rule": cr.ID, "reason": "维护", "minutes": 60}, nil)
	p.set(exp, "10.0.0.3:9100", 1)
	e.api.EvalAlerts(t.Context(), now.Add(13*time.Minute))
	if a, ok = e.alerts(false).find(cr.ID, ""); !ok || !a.Silenced {
		t.Fatalf("应被静默：%+v", a)
	}
	e.expect(200, "DELETE", "/api/alert-rules/custom/"+cr.ID, nil, nil)
	e.expect(404, "DELETE", "/api/alert-rules/custom/"+cr.ID, nil, nil)
	if _, ok = e.alerts(false).find(cr.ID, ""); ok {
		t.Fatal("删除规则后告警应关闭")
	}
	var aud auditOut = lastAudit(t, e, "alert.rule")
	if !strings.Contains(aud.Act, "删除自定义告警规则") {
		t.Fatalf("应有审计：%+v", aud)
	}
}

func TestCustomRuleValidationAndTest(t *testing.T) {
	e, base := opsEnv(t, "healthy")
	good := map[string]any{"name": "规则A", "expr": "up == 0", "op": ">", "crit": 0}
	e.expect(200, "POST", "/api/alert-rules/custom", good, nil)
	for name, mut := range map[string]func(m map[string]any){
		"重名":       func(m map[string]any) {},
		"内置重名":     func(m map[string]any) { m["name"] = "节点失联" },
		"无名称":      func(m map[string]any) { m["name"] = " " },
		"无表达式":     func(m map[string]any) { m["name"] = "x1"; m["expr"] = "  " },
		"表达式过长":    func(m map[string]any) { m["name"] = "x2"; m["expr"] = strings.Repeat("a", 2001) },
		"比较方式":     func(m map[string]any) { m["name"] = "x3"; m["op"] = "==" },
		"无严重阈值":    func(m map[string]any) { m["name"] = "x4"; delete(m, "crit") },
		"警告大于严重":   func(m map[string]any) { m["name"] = "x5"; m["warn"] = 5; m["crit"] = 3 },
		"≤ 警告小于严重": func(m map[string]any) { m["name"] = "x6"; m["op"] = "<="; m["warn"] = 1; m["crit"] = 3 },
		"持续时间":     func(m map[string]any) { m["name"] = "x7"; m["durMin"] = 2000 },
		"集群不存在":    func(m map[string]any) { m["name"] = "x8"; m["cluster"] = "nope" },
		"渠道不存在":    func(m map[string]any) { m["name"] = "x9"; m["channels"] = []string{"C-none"} },
	} {
		b := map[string]any{}
		for k, v := range good {
			b[k] = v
		}
		mut(b)
		want := 400
		if name == "重名" || name == "内置重名" {
			want = 409
		}
		if code := e.call("POST", "/api/alert-rules/custom", b, nil); code != want {
			t.Errorf("%s：期望 %d，实际 %d", name, want, code)
		}
	}
	e.expect(404, "PUT", "/api/alert-rules/custom/custom_none", map[string]any{"enabled": false}, nil)

	// 试运行
	cid := base[len("/api/clusters/"):]
	e.expect(409, "POST", "/api/alert-rules/custom/test", map[string]any{"expr": "up", "cluster": cid}, nil) // 没配置 Prometheus
	e.expect(400, "POST", "/api/alert-rules/custom/test", map[string]any{"expr": "up", "cluster": "nope"}, nil)
	e.expect(400, "POST", "/api/alert-rules/custom/test", map[string]any{"expr": " ", "cluster": cid}, nil)
	p := newFakeProm(t)
	setProm(e, p.srv.URL, "")
	p.set("up == 0", "a:1", 1)
	var res struct {
		Count  int `json:"count"`
		Series []struct {
			Inst  string
			Value float64
		}
		Expr string
	}
	e.expect(200, "POST", "/api/alert-rules/custom/test", map[string]any{"expr": "up == 0", "cluster": cid}, &res)
	if res.Count != 1 || res.Series[0].Inst != "a:1" || res.Series[0].Value != 1 {
		t.Fatalf("试运行结果：%+v", res)
	}
	p.mu.Lock()
	p.errs["bad("] = "parse error: unexpected end of input"
	p.mu.Unlock()
	e.expect(502, "POST", "/api/alert-rules/custom/test", map[string]any{"expr": "bad(", "cluster": cid}, nil) // 语法错误由 Prometheus 返回
	// 没有 $sel 选择器时，$sel 与逗号一起去掉
	if got := expandSel(`m{$sel,a="b"}`, ""); got != `m{a="b"}` {
		t.Fatalf("expandSel: %s", got)
	}
	if got := expandSel(`m{$sel}`, ` {job="x"} `); got != `m{job="x"}` {
		t.Fatalf("expandSel: %s", got)
	}

	// 权限：值班只读可以看，不能改
	e.newUser("oncall6", []string{"r_oncall"}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall6", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	oc.expect(200, "GET", "/api/alert-rules/custom", nil, nil)
	oc.expect(403, "POST", "/api/alert-rules/custom", map[string]any{"name": "o", "expr": "up", "crit": 1}, nil)
	oc.expect(403, "DELETE", "/api/alert-rules/custom/custom_x", nil, nil)
	oc.expect(403, "POST", "/api/alert-rules/custom/test", map[string]any{"expr": "up", "cluster": cid}, nil)
}
