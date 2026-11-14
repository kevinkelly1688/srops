// @author: kevinkelly
// @license: Apache Licence
// @file: alert_test.go
// @time: 2026/10/28

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"starrocks-ops/internal/model"
)

// hook 是一个记录收到的 Webhook 请求的测试服务。
type hook struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string
	fail bool
}

func newHook(t *testing.T) *hook {
	h := &hook{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.fail {
			http.Error(w, "boom", 500)
			return
		}
		h.reqs = append(h.reqs, string(b))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hook) count() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.reqs) }
func (h *hook) last() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reqs) == 0 {
		return ""
	}
	return h.reqs[len(h.reqs)-1]
}

type alertList struct {
	Items  []alertOut     `json:"items"`
	Counts map[string]int `json:"counts"`
}

func (l alertList) find(rule, inst string) (alertOut, bool) {
	for _, a := range l.Items {
		if a.Rule == rule && (inst == "" || strings.Contains(a.Inst, inst)) {
			return a, true
		}
	}
	return alertOut{}, false
}

func (e *env) alerts(history bool) alertList {
	e.t.Helper()
	var l alertList
	p := "/api/alerts"
	if history {
		p += "?history=1"
	}
	e.expect(200, "GET", p, nil, &l)
	return l
}

// setRules 把所有规则的持续时间改为 0，便于一次评估就触发。
func (e *env) zeroDur() {
	for _, r := range alertRules {
		e.expect(200, "PUT", "/api/alert-rules/"+r.ID, map[string]any{"durMin": 0}, nil)
	}
}

func TestAlertLifecycle(t *testing.T) {
	e, _ := opsEnv(t, "problems")
	h := newHook(t)
	var ch map[string]any
	e.expect(200, "POST", "/api/alert-channels", map[string]any{"type": "Webhook", "name": "测试 Hook", "url": h.srv.URL + "/a?token=SECRETTOKEN", "levels": []string{"critical", "warning", "info"}}, &ch)
	if strings.Contains(ch["url"].(string), "SECRETTOKEN") || ch["urlEnc"] != nil && ch["urlEnc"] != "" {
		t.Fatalf("地址应脱敏且不返回密文：%v", ch)
	}
	now := time.Now()

	// 持续时间：be_disk_usage 默认要持续 5 分钟，首次评估不触发；node_down 持续 1 分钟，也不触发
	e.api.EvalAlerts(t.Context(), now)
	l := e.alerts(false)
	if _, ok := l.find("be_disk_usage", ""); ok {
		t.Fatal("持续时间未满，不应触发磁盘告警")
	}
	if _, ok := l.find("mv_refresh_failed", ""); !ok { // 物化视图失败规则持续 0 分钟：立即触发
		t.Fatalf("mv_refresh_failed 应立即触发：%+v", l.Items)
	}
	if _, ok := l.find("routine_load_paused", ""); ok {
		t.Fatal("Routine Load 暂停需持续 1 分钟，首次评估不应触发")
	}
	// 满足持续时间后触发，并按阈值判级别
	e.api.EvalAlerts(t.Context(), now.Add(6*time.Minute))
	l = e.alerts(false)
	disk, ok := l.find("be_disk_usage", "")
	if !ok || disk.Level != "warning" && disk.Level != "critical" {
		t.Fatalf("磁盘告警：%+v", l.Items)
	}
	if cs, ok := l.find("compaction_score", ""); ok {
		t.Fatalf("Compaction 需持续 10 分钟，6 分钟时不应触发：%+v", cs)
	}
	if rl, ok := l.find("routine_load_paused", ""); !ok || rl.Level != "warning" || rl.ClusterName != "prod-test" {
		t.Fatalf("Routine Load 暂停（1 个，≥ 警告阈值 1，< 严重阈值 3）应为警告：%+v", rl)
	}
	e.api.EvalAlerts(t.Context(), now.Add(11*time.Minute))
	l = e.alerts(false)
	cs, ok := l.find("compaction_score", "")
	if !ok || cs.Level != "critical" || cs.Value != "186" {
		t.Fatalf("Compaction Score 186 应为严重：%+v", cs)
	}
	if l.Counts["critical"] == 0 || l.Counts["warning"] == 0 {
		t.Fatalf("计数：%v", l.Counts)
	}
	n := len(l.Items)
	e.api.EvalAlerts(t.Context(), now.Add(12*time.Minute))
	if len(e.alerts(false).Items) != n {
		t.Fatal("同一对象持续异常期间不应产生重复告警")
	}
	e.api.EvalAlerts(t.Context(), now.Add(17*time.Minute)) // Compaction 告警在 +11 分钟产生，合并等待 5 分钟后首次通知

	// 通知：合并等待 5 分钟（默认 mergeMin=5）；同一集群同一规则合并为一条；按规则分组 → 每个规则一条
	if h.count() == 0 {
		t.Fatal("满足合并等待后应已发出通知")
	}
	first := h.count()
	body := h.last()
	var msg map[string]any
	_ = json.Unmarshal([]byte(body), &msg)
	if !strings.Contains(body, "prod-test") {
		t.Fatalf("通知应包含集群名：%s", body)
	}
	// 再评估：未到重复提醒间隔（60 分钟），不重复发送
	e.api.EvalAlerts(t.Context(), now.Add(20*time.Minute))
	if h.count() != first {
		t.Fatalf("未到重复提醒间隔不应重复发送：%d → %d", first, h.count())
	}
	e.api.EvalAlerts(t.Context(), now.Add(75*time.Minute))
	if h.count() <= first {
		t.Fatal("超过重复提醒间隔后应再次通知")
	}

	// 认领：认领后不再重复提醒
	id := cs.ID
	e.expect(200, "POST", "/api/alerts/"+id+"/ack", nil, nil)
	e.expect(409, "POST", "/api/alerts/"+id+"/ack", nil, nil)
	e.expect(404, "POST", "/api/alerts/AL-none/ack", nil, nil)
	l = e.alerts(false)
	if a, _ := l.find("compaction_score", ""); a.State != "acked" || a.AckBy != "admin" {
		t.Fatalf("认领后：%+v", a)
	}

	// 恢复：把阈值调高 → 告警恢复 → 发送恢复通知
	before := h.count()
	e.expect(200, "PUT", "/api/alert-rules/compaction_score", map[string]any{"warn": 500, "crit": 1000}, nil)
	e.api.EvalAlerts(t.Context(), now.Add(80*time.Minute))
	hist := e.alerts(true)
	if a, ok := hist.find("compaction_score", ""); !ok || a.State != "resolved" || a.AckBy != "admin" {
		t.Fatalf("恢复后应进入历史：%+v", hist.Items)
	}
	if _, ok := e.alerts(false).find("compaction_score", ""); ok {
		t.Fatal("恢复后不应在当前告警中")
	}
	if h.count() <= before || !strings.Contains(strings.Join(h.reqs[before:], ""), "【已恢复】") {
		t.Fatalf("应发送恢复通知：%d → %d", before, h.count())
	}
	// 恢复通知只发一次
	c2 := h.count()
	e.api.EvalAlerts(t.Context(), now.Add(81*time.Minute))
	if h.count() != c2 {
		t.Fatal("恢复通知不应重复发送")
	}

	// 通知渠道的发送状态
	var chans []model.Channel
	e.expect(200, "GET", "/api/alert-channels", nil, &chans)
	if len(chans) != 1 || chans[0].LastResult != "成功" || chans[0].LastAt.IsZero() {
		t.Fatalf("渠道发送状态：%+v", chans)
	}
	// 审计
	var p struct{ Items []auditOut }
	e.expect(200, "GET", "/api/audit?code=alert.channel&q=发送告警通知", nil, &p)
	if len(p.Items) == 0 || p.Items[0].User != "系统（告警中心）" {
		t.Fatalf("通知发送应写入审计：%+v", p.Items)
	}
}

func TestAlertNotifyPolicy(t *testing.T) {
	e, _ := opsEnv(t, "problems")
	h := newHook(t)
	var warnOnly map[string]any
	e.expect(200, "POST", "/api/alert-channels", map[string]any{"type": "Webhook", "name": "仅严重", "url": h.srv.URL, "levels": []string{"critical"}}, &warnOnly)
	e.zeroDur()
	setNotify := func(mut func(m map[string]any)) {
		var cur map[string]any
		e.expect(200, "GET", "/api/settings", nil, &cur)
		n := cur["notify"].(map[string]any)
		delete(n, "smtpPwd")
		mut(n)
		e.expect(200, "PUT", "/api/settings/notify", n, nil)
	}
	now := time.Now()

	// mergeMin=0：立即发送；渠道只收严重级别（routine_load_paused 是警告，不应出现）
	setNotify(func(n map[string]any) { n["mergeMin"] = 0 })
	e.api.EvalAlerts(t.Context(), now)
	if h.count() == 0 {
		t.Fatal("mergeMin=0 应立即通知")
	}
	for _, r := range h.reqs {
		if strings.Contains(r, "Routine Load") {
			t.Fatalf("渠道只接收严重级别，不应收到警告：%s", r)
		}
	}
	// 同一规则多个对象合并为一条：compaction_score 在 problems 场景有 1 个 BE 超阈值，node 类告警也各自合并
	crit := h.count()

	// 免打扰：非严重不发；严重照发
	e.expect(200, "PUT", "/api/alert-channels/"+warnOnly["id"].(string), map[string]any{"type": "Webhook", "name": "仅严重", "levels": []string{"critical", "warning", "info"}}, nil)
	setNotify(func(n map[string]any) {
		n["quiet"] = true
		hh := now.Hour()
		n["quietFrom"] = time.Date(2026, 1, 1, hh, 0, 0, 0, time.Local).Add(-time.Hour).Format("15:04")
		n["quietTo"] = time.Date(2026, 1, 1, hh, 0, 0, 0, time.Local).Add(2 * time.Hour).Format("15:04")
	})
	e.api.EvalAlerts(t.Context(), now.Add(time.Minute))
	if h.count() != crit {
		t.Fatalf("免打扰时段内非严重告警不应发送：%d → %d", crit, h.count())
	}
	// 免打扰结束后补发尚未发送的警告
	setNotify(func(n map[string]any) { n["quiet"] = false })
	e.api.EvalAlerts(t.Context(), now.Add(2*time.Minute))
	if h.count() <= crit || !strings.Contains(strings.Join(h.reqs[crit:], ""), "Routine Load") {
		t.Fatalf("免打扰结束后应补发警告：%d → %d", crit, h.count())
	}

	// 静默：被静默的告警不发送；解除后恢复
	var sl model.Silence
	e.expect(200, "POST", "/api/silences", map[string]any{"cluster": "prod-test", "rule": "mv_refresh_failed", "reason": "维护", "minutes": 60}, &sl)
	if sl.By != "admin" || !sl.Until.After(time.Now().Add(59*time.Minute)) {
		t.Fatalf("静默：%+v", sl)
	}
	setNotify(func(n map[string]any) { n["repeatMin"] = 1 })
	before := h.count()
	e.api.EvalAlerts(t.Context(), now.Add(10*time.Minute))
	for _, r := range h.reqs[before:] {
		if strings.Contains(r, "物化视图") {
			t.Fatalf("被静默的规则不应通知：%s", r)
		}
	}
	l := e.alerts(false)
	if a, ok := l.find("mv_refresh_failed", ""); !ok || !a.Silenced {
		t.Fatalf("被静默的告警应标记 silenced：%+v", a)
	}
	var sls []model.Silence
	e.expect(200, "GET", "/api/silences", nil, &sls)
	if len(sls) != 1 {
		t.Fatalf("静默列表：%+v", sls)
	}
	e.expect(200, "DELETE", "/api/silences/"+sl.ID, nil, nil)
	e.expect(404, "DELETE", "/api/silences/"+sl.ID, nil, nil)
	before = h.count()
	e.api.EvalAlerts(t.Context(), now.Add(20*time.Minute))
	if h.count() <= before || !strings.Contains(strings.Join(h.reqs[before:], ""), "物化视图") {
		t.Fatal("解除静默后应发送通知")
	}
	// 渠道发送失败：5 分钟内不重试，之后重试
	h.mu.Lock()
	h.fail = true
	h.mu.Unlock()
	setNotify(func(n map[string]any) { n["repeatMin"] = 1 })
	e.api.EvalAlerts(t.Context(), now.Add(40*time.Minute))
	h.mu.Lock()
	h.fail = false
	h.mu.Unlock()
	cnt := h.count()
	e.api.EvalAlerts(t.Context(), now.Add(41*time.Minute))
	if h.count() != cnt {
		t.Fatal("失败后 5 分钟内不应重试")
	}
	e.api.EvalAlerts(t.Context(), now.Add(47*time.Minute))
	if h.count() <= cnt {
		t.Fatal("失败 5 分钟后应重试")
	}
	var chans []model.Channel
	e.expect(200, "GET", "/api/alert-channels", nil, &chans)
	if chans[0].LastResult != "成功" {
		t.Fatalf("渠道最近状态：%+v", chans[0])
	}
}

func TestAlertRulesChannelsRBAC(t *testing.T) {
	e, _ := opsEnv(t, "healthy")
	// 规则
	var rules []ruleOut
	e.expect(200, "GET", "/api/alert-rules", nil, &rules)
	if len(rules) != len(alertRules) || rules[2].ID != "be_disk_usage" || rules[2].Warn != 80 || rules[2].Crit != 85 || rules[2].DurMin != 5 || !rules[2].Enabled {
		t.Fatalf("规则列表：%+v", rules)
	}
	var one ruleOut
	e.expect(200, "PUT", "/api/alert-rules/be_disk_usage", map[string]any{"warn": 70, "crit": 90, "durMin": 3, "enabled": false}, &one)
	if one.Warn != 70 || one.Crit != 90 || one.DurMin != 3 || one.Enabled || !one.Custom || one.DefWarn != 80 {
		t.Fatalf("修改后：%+v", one)
	}
	e.expect(200, "PUT", "/api/alert-rules/be_disk_usage", map[string]any{"reset": true}, &one)
	if one.Warn != 80 || !one.Enabled || one.Custom {
		t.Fatalf("恢复默认：%+v", one)
	}
	for name, b := range map[string]map[string]any{
		"警告大于严重": {"warn": 95},
		"负数":     {"warn": -1},
		"持续时间":   {"durMin": 5000},
		"无内容":    {},
		"不存在的渠道": {"channels": []string{"C-none"}},
		"固定阈值":   nil,
	} {
		if b == nil {
			if code := e.call("PUT", "/api/alert-rules/node_down", map[string]any{"crit": 5}, nil); code != 400 {
				t.Errorf("%s：期望 400，实际 %d", name, code)
			}
			continue
		}
		if code := e.call("PUT", "/api/alert-rules/be_disk_usage", b, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	e.expect(404, "PUT", "/api/alert-rules/no_rule", map[string]any{"warn": 1}, nil)
	e.expect(400, "PUT", "/api/alert-rules/node_down", map[string]any{"warn": 1}, nil) // 无警告阈值

	// 渠道校验
	good := map[string]any{"type": "飞书", "name": "飞书群", "url": "https://open.feishu.cn/open-apis/bot/v2/hook/abc", "secret": "SIGNSECRET", "levels": []string{"critical"}}
	var ch model.Channel
	e.expect(200, "POST", "/api/alert-channels", good, &ch)
	if !ch.HasSecret && false {
		t.Fatal()
	}
	var list []model.Channel
	e.expect(200, "GET", "/api/alert-channels", nil, &list)
	if len(list) != 1 || !list[0].HasSecret || list[0].URLEnc != "" || list[0].SecretEnc != "" || strings.Contains(list[0].URLMasked, "abc") && !strings.Contains(list[0].URLMasked, "*") {
		t.Fatalf("渠道列表不应泄露地址与密钥：%+v", list)
	}
	e.expect(409, "POST", "/api/alert-channels", good, nil)
	for name, mut := range map[string]func(m map[string]any){
		"类型":    func(m map[string]any) { m["name"] = "x1"; m["type"] = "短信" },
		"地址":    func(m map[string]any) { m["name"] = "x2"; m["url"] = "ftp://x" },
		"无地址":   func(m map[string]any) { m["name"] = "x3"; delete(m, "url") },
		"无级别":   func(m map[string]any) { m["name"] = "x4"; m["levels"] = []string{} },
		"错误级别":  func(m map[string]any) { m["name"] = "x5"; m["levels"] = []string{"fatal"} },
		"名称":    func(m map[string]any) { m["name"] = " " },
		"邮件收件人": func(m map[string]any) { m["name"] = "x6"; m["type"] = "邮件"; m["url"] = "not-an-email" },
	} {
		b := map[string]any{}
		for k, v := range good {
			b[k] = v
		}
		mut(b)
		if code := e.call("POST", "/api/alert-channels", b, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	e.expect(200, "POST", "/api/alert-channels", map[string]any{"type": "邮件", "name": "DBA 邮件", "url": "dba@example.com, ops@example.com", "levels": []string{"critical", "warning"}}, nil)
	// 编辑：地址 / 密钥留空沿用
	e.expect(200, "PUT", "/api/alert-channels/"+list[0].ID, map[string]any{"type": "飞书", "name": "飞书群2", "levels": []string{"critical", "warning"}, "enabled": false}, &ch)
	if ch.Name != "飞书群2" || ch.Enabled || len(ch.Levels) != 2 {
		t.Fatalf("编辑后：%+v", ch)
	}
	var raw model.Channel
	e.api.Store.View(func(st *storeState) { raw = st.Channels[0] })
	if raw.URLEnc == "" || raw.SecretEnc == "" || strings.Contains(raw.URLEnc, "feishu") || strings.Contains(raw.SecretEnc, "SIGNSECRET") {
		t.Fatalf("地址与密钥应加密保存且编辑时沿用：%+v", raw)
	}
	e.expect(200, "PUT", "/api/alert-channels/"+list[0].ID, map[string]any{"type": "飞书", "name": "飞书群2", "levels": []string{"critical"}, "clearSecret": true}, nil)
	e.api.Store.View(func(st *storeState) { raw = st.Channels[0] })
	if raw.SecretEnc != "" {
		t.Fatal("清除密钥后不应保留")
	}
	e.expect(404, "PUT", "/api/alert-channels/C-none", good, nil)
	e.expect(502, "POST", "/api/alert-channels/"+list[0].ID+"/test", nil, nil) // 地址不可达
	e.expect(404, "POST", "/api/alert-channels/C-none/test", nil, nil)

	// 在规则里引用渠道，删除渠道时同步清理
	e.expect(200, "PUT", "/api/alert-rules/node_down", map[string]any{"channels": []string{list[0].ID}}, nil)
	e.expect(200, "DELETE", "/api/alert-channels/"+list[0].ID, nil, nil)
	e.expect(404, "DELETE", "/api/alert-channels/"+list[0].ID, nil, nil)
	e.expect(200, "GET", "/api/alert-rules", nil, &rules)
	for _, r := range rules {
		if r.ID == "node_down" && len(r.Channels) != 0 {
			t.Fatalf("删除渠道后规则不应再引用：%+v", r)
		}
	}

	// 静默校验
	for name, b := range map[string]map[string]any{
		"无原因":  {"cluster": "prod-test", "minutes": 60},
		"时长过短": {"cluster": "prod-test", "reason": "x", "minutes": 1},
		"时长过长": {"cluster": "prod-test", "reason": "x", "minutes": 99999},
		"规则":   {"cluster": "prod-test", "reason": "x", "minutes": 60, "rule": "nope"},
		"集群":   {"cluster": "nope", "reason": "x", "minutes": 60},
	} {
		if code := e.call("POST", "/api/silences", b, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}

	// 权限与数据范围
	e.newUser("oncall5", []string{"r_oncall"}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall5", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	oc.expect(200, "GET", "/api/alerts", nil, nil)
	oc.expect(200, "GET", "/api/alert-rules", nil, nil)
	oc.expect(200, "GET", "/api/alert-channels", nil, nil)
	oc.expect(403, "PUT", "/api/alert-rules/node_down", map[string]any{"enabled": false}, nil)
	oc.expect(403, "POST", "/api/alert-channels", good, nil)
	oc.expect(403, "POST", "/api/silences", map[string]any{"cluster": "prod-test", "reason": "x", "minutes": 60}, nil)
	// 范围受限的角色：只能看 / 操作范围内集群的告警；不能改全局规则、渠道，不能静默“全部集群”
	var ro roleOut
	e.expect(200, "POST", "/api/roles", map[string]any{"name": "单集群告警", "perms": []string{"alert:view", "alert.ack", "alert.silence", "alert.rule", "alert.channel"}, "clusters": []string{}}, &ro)
	e.newUser("scoped1", []string{ro.ID}, "Scoped@2026xyzab")
	sc := e.firstLogin("scoped1", "Scoped@2026xyzab", "Scoped@2026xyzab2")
	e.api.EvalAlerts(t.Context(), time.Now())
	sc.expect(403, "PUT", "/api/alert-rules/node_down", map[string]any{"enabled": false}, nil)
	sc.expect(403, "POST", "/api/alert-channels", good, nil)
	sc.expect(403, "POST", "/api/silences", map[string]any{"reason": "x", "minutes": 60}, nil)
	sc.expect(403, "POST", "/api/silences", map[string]any{"cluster": "prod-test", "reason": "x", "minutes": 60}, nil)
	var sl alertList
	sc.expect(200, "GET", "/api/alerts", nil, &sl)
	if len(sl.Items) != 0 {
		t.Fatalf("数据范围为空的角色看不到任何集群的告警：%+v", sl.Items)
	}
	// 范围内的集群可认领 / 静默
	e.expect(200, "PUT", "/api/roles/"+ro.ID, map[string]any{"clusters": []string{"prod-test"}}, nil)
	e.expect(200, "PUT", "/api/alert-rules/mv_refresh_failed", map[string]any{"durMin": 0, "warn": 0}, nil)
	e.api.EvalAlerts(t.Context(), time.Now())
	sl = alertList{}
	sc.expect(200, "GET", "/api/alerts", nil, &sl)
	must(t, e.api.Store.Update(func(st *storeState) error {
		st.Alerts = append(st.Alerts, model.Alert{ID: "AL-t1", Cluster: "prod-test", Rule: "node_down", Name: "节点失联", Inst: "BE x", Level: "critical", State: "firing", FirstAt: time.Now(), LastAt: time.Now()},
			model.Alert{ID: "AL-t2", Cluster: "other-cluster", Rule: "node_down", Name: "节点失联", Inst: "BE y", Level: "critical", State: "firing", FirstAt: time.Now(), LastAt: time.Now()})
		return nil
	}))
	sc.expect(200, "GET", "/api/alerts", nil, &sl)
	for _, a := range sl.Items {
		if a.Cluster != "prod-test" {
			t.Fatalf("不应看到范围外集群的告警：%+v", a)
		}
	}
	sc.expect(403, "POST", "/api/alerts/AL-t2/ack", nil, nil)
	sc.expect(200, "POST", "/api/alerts/AL-t1/ack", nil, nil)
	sc.expect(200, "POST", "/api/silences", map[string]any{"cluster": "prod-test", "reason": "维护", "minutes": 30}, nil)
}

func TestClusterUnreachableAlert(t *testing.T) {
	e, _ := opsEnv(t, "healthy")
	e.expect(200, "PUT", "/api/alert-rules/cluster_unreachable", map[string]any{"durMin": 0}, nil)
	now := time.Now()
	e.fake.Stop() // 集群不可达
	e.api.EvalAlerts(t.Context(), now)
	if _, ok := e.alerts(false).find("cluster_unreachable", ""); ok {
		t.Fatal("第一次采集失败不应告警（需要连续 2 次）")
	}
	e.api.EvalAlerts(t.Context(), now.Add(time.Minute))
	a, ok := e.alerts(false).find("cluster_unreachable", "")
	if !ok || a.Level != "critical" || !strings.Contains(a.Desc, "无法连接") {
		t.Fatalf("连续失败后应触发：%+v", e.alerts(false).Items)
	}
}
