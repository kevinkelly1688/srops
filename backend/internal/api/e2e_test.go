// @author: kevinkelly
// @license: Apache Licence
// @file: e2e_test.go
// @time: 2026/10/28

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"starrocks-ops/fakesr"
	"starrocks-ops/internal/collect"
	"starrocks-ops/internal/inspect"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/secure"
	"starrocks-ops/internal/sshx"
	"starrocks-ops/internal/store"
)

// env 启动一个假 StarRocks 集群与一个完整的平台服务。
type env struct {
	t    *testing.T
	fake *fakesr.Cluster
	srv  *httptest.Server
	c    *http.Client
	dir  string
	api  *Server
	hdr  map[string]string // 附加请求头（如 X-Forwarded-For）

	sshPort int // Hosts 模式下各主机的 SSH 端口
}

func newEnv(t *testing.T, mode, scenario string) *env { return newEnvVer(t, mode, scenario, "") }

// newEnvVer 同 newEnv，可指定假集群的 StarRocks 版本。
func newEnvVer(t *testing.T, mode, scenario, version string) *env {
	t.Helper()
	return newEnvCfg(t, mode, scenario, version, nil)
}

// newEnvCfg 同 newEnvVer，并允许调整假集群配置（如 Hosts 模式）。
func newEnvCfg(t *testing.T, mode, scenario, version string, mod func(*fakesr.Config)) *env {
	t.Helper()
	cfg := fakesr.DefaultConfig()
	if version != "" {
		cfg.Version = version
	}
	cfg.Mode, cfg.Scenario, cfg.QueryPort, cfg.SSHPort = mode, scenario, 0, 0
	cfg.FEHTTPBase, cfg.BEHTTPBase, cfg.CNHTTPBase = 0, 0, 0
	if mode == fakesr.ModeSharedData && version == "" {
		cfg.Version = "4.0.2"
	}
	if mod != nil {
		mod(&cfg)
	}
	fake, err := fakesr.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Stop)

	dir := t.TempDir()
	st, err := store.Open(dir)
	must(t, err)
	box, err := secure.LoadOrCreateKey(filepath.Join(dir, "secret.key"))
	must(t, err)
	keys, err := sshx.LoadOrCreate(filepath.Join(dir, "ssh"))
	must(t, err)
	mgr := collect.NewManager(box)
	s := &Server{Store: st, Mgr: mgr, Runner: &inspect.Runner{Store: st, Mgr: mgr, Keys: keys, Box: box}, Keys: keys, Box: box,
		WebDir: filepath.Join("..", "..", ".."), Version: "test", Sessions: NewSessions()}
	must(t, s.Bootstrap())
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	jar, _ := cookiejar.New(nil)
	return &env{t: t, fake: fake, srv: hs, c: &http.Client{Jar: jar}, dir: dir, api: s}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// call 发请求并把 JSON 结果解码到 out；返回状态码。
func (e *env) call(method, path string, body, out any) int {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "sr-ops")
	for k, v := range e.hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.c.Do(req)
	must(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: 解码失败 %v: %s", method, path, err, b)
		}
	}
	if resp.StatusCode >= 300 && out == nil {
		e.t.Logf("%s %s -> %d %s", method, path, resp.StatusCode, b)
	}
	return resp.StatusCode
}

func (e *env) expect(code int, method, path string, body, out any) {
	e.t.Helper()
	if got := e.call(method, path, body, out); got != code {
		e.t.Fatalf("%s %s: 状态码 %d，期望 %d", method, path, got, code)
	}
}

// login 用初始密码登录并修改密码。
func (e *env) login() {
	pw, err := os.ReadFile(filepath.Join(e.dir, "initial_admin_password"))
	must(e.t, err)
	initial := strings.TrimSpace(string(pw))
	var me map[string]any
	e.expect(200, "POST", "/api/login", map[string]string{"login": "admin", "password": initial}, &me)
	if me["mustChange"] != true {
		e.t.Fatal("首次登录应要求修改密码")
	}
	e.expect(403, "GET", "/api/clusters", nil, nil) // 未改密码前不能访问业务接口
	e.expect(400, "POST", "/api/password", map[string]string{"old": initial, "new": "weak"}, nil)
	e.expect(200, "POST", "/api/password", map[string]string{"old": initial, "new": "Admin@2026xyz"}, &me)
	if _, err := os.Stat(filepath.Join(e.dir, "initial_admin_password")); !os.IsNotExist(err) {
		e.t.Fatal("修改密码后应删除初始密码文件")
	}
}

func port(addr string) int {
	_, p, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(p)
	return n
}

func (e *env) conn() map[string]any {
	return map[string]any{"hosts": []string{"127.0.0.1"}, "queryPort": e.fake.QueryPort(), "httpPort": port(e.fake.FEHTTPAddrs()[0]), "user": "sr_ops", "password": "secret"}
}

func (e *env) sshIn(nodes []model.Node) map[string]any {
	var ns []map[string]string
	for _, n := range nodes {
		ns = append(ns, map[string]string{"host": n.Host, "type": n.Type})
	}
	sp := port(e.fake.SSHAddr())
	if e.sshPort != 0 {
		sp = e.sshPort
	}
	return map[string]any{"user": "starrocks", "port": sp, "auth": "password", "password": "secret", "feDir": "/opt/starrocks/fe", "beDir": "/opt/starrocks/be", "nodes": ns}
}

func item(t *testing.T, rep model.Report, id string) model.Item {
	t.Helper()
	for _, it := range rep.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("报告中没有规则 %s", id)
	return model.Item{}
}

func TestEndToEndSharedNothingProblems(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "problems")

	// 未登录 / 缺少 CSRF 头
	e.expect(401, "GET", "/api/clusters", nil, nil)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/login", strings.NewReader(`{}`))
	resp, _ := e.c.Do(req)
	if resp.StatusCode != 403 {
		t.Fatalf("缺少 X-Requested-With 时应 403，实际 %d", resp.StatusCode)
	}
	resp.Body.Close()
	e.expect(401, "POST", "/api/login", map[string]string{"login": "admin", "password": "wrong"}, nil)
	e.login()

	// 静态文件白名单
	for path, want := range map[string]int{"/": 200, "/js/core.js": 200, "/backend/go.mod": 404, "/data/state.json": 404, "/js/../backend/go.mod": 404} {
		r, err := e.c.Get(e.srv.URL + path)
		must(t, err)
		r.Body.Close()
		if r.StatusCode != want {
			t.Errorf("GET %s = %d，期望 %d", path, r.StatusCode, want)
		}
	}

	// 1. 测试连接并识别
	var d detectOut
	e.expect(400, "POST", "/api/clusters/test", map[string]any{"hosts": []string{"bad host;"}, "queryPort": 1, "httpPort": 1, "user": "x", "password": "y"}, nil)
	bad := e.conn()
	bad["password"] = "nope"
	e.expect(502, "POST", "/api/clusters/test", bad, nil)
	e.expect(200, "POST", "/api/clusters/test", e.conn(), &d)
	if d.Version != "3.5.6" || d.Mode != "shared-nothing" || len(d.Nodes) != 12 {
		t.Fatalf("识别结果不对：%s %s %d 个节点", d.Version, d.Mode, len(d.Nodes))
	}
	for _, g := range d.Grants {
		if strings.HasPrefix(g.Name, "REPOSITORY") == g.OK {
			t.Errorf("权限检查 %s = %v 不符合 problems 场景", g.Name, g.OK)
		}
	}

	// 2. SSH 测试（含非法目录）
	in := e.sshIn(d.Nodes)
	in["feDir"] = "/opt/x; rm -rf /"
	e.expect(400, "POST", "/api/clusters/test-ssh", in, nil)
	var sshRes struct {
		OK    bool        `json:"ok"`
		Hosts []hostCheck `json:"hosts"`
	}
	e.expect(200, "POST", "/api/clusters/test-ssh", e.sshIn(d.Nodes), &sshRes)
	if !sshRes.OK || len(sshRes.Hosts) != 1 {
		t.Fatalf("SSH 测试失败：%+v", sshRes)
	}

	// 3. 先以“SSH 稍后填写”导入 → 待完善
	body := e.conn()
	body["name"], body["env"], body["owner"], body["audit"] = "prod-test", "生产", "admin", true
	var co clusterOut
	e.expect(200, "POST", "/api/clusters", body, &co)
	if len(co.Missing) != 2 || co.PasswordEnc != "" {
		t.Fatalf("导入后应为待完善且不返回密文：%+v", co)
	}
	e.expect(409, "POST", "/api/clusters", body, nil)
	// 补全 SSH
	body["ssh"] = e.sshIn(d.Nodes)
	body["password"] = "" // 不改密码
	e.expect(200, "PUT", "/api/clusters/prod-test", body, &co)
	if len(co.Missing) != 0 || co.SSH == nil || co.SSH.PasswordEnc != "" {
		t.Fatalf("补全后应可运维：%+v", co)
	}
	state, _ := os.ReadFile(filepath.Join(e.dir, "state.json"))
	if bytes.Contains(state, []byte(`"secret"`)) {
		t.Fatal("state.json 中不应出现明文密码")
	}

	// 4. 架构
	var topo model.Topology
	e.expect(200, "GET", "/api/clusters/prod-test/topology", nil, &topo)
	if len(topo.Nodes) != 12 || topo.Nodes[0].ID != "fe-1" || topo.Nodes[0].Role != "Leader" {
		t.Fatalf("拓扑不对：%+v", topo.Nodes[0])
	}
	var hot *model.Node
	for i := range topo.Nodes {
		if topo.Nodes[i].Compaction != nil && *topo.Nodes[i].Compaction >= 186 {
			hot = &topo.Nodes[i]
		}
	}
	if hot == nil || hot.Status != "warn" {
		t.Fatalf("Compaction 186 的 BE 应为告警：%+v", hot)
	}
	if topo.Nodes[0].JVM == nil || *topo.Nodes[0].JVM < 80 {
		t.Fatalf("Leader JVM 应从 /metrics 读到 ~82%%：%v", topo.Nodes[0].JVM)
	}

	// 5. 总览
	var ov model.Overview
	e.expect(200, "GET", "/api/clusters/prod-test/overview", nil, &ov)
	k := ov.KPI
	if k.FE.Total != 4 || k.BE.Total != 6 || k.CN.Total != 2 || k.QPS == nil || k.P99 == nil || k.StorageTotal == nil {
		t.Fatalf("KPI 不完整：%+v", k)
	}
	if len(ov.ResourceGroups) != 4 || ov.ResourceGroups[0].Name != "rg_adhoc" || ov.ResourceGroups[0].Running != 6*6 || ov.TotalCores <= 0 {
		t.Fatalf("资源组用量不对：%+v，总核数 %d", ov.ResourceGroups, ov.TotalCores)
	}
	if *k.Unhealthy != 12 || *k.RLPaused != 1 || *k.MVFailed != 1 {
		t.Fatalf("KPI 数值不对：unhealthy=%v paused=%v mvFailed=%v", *k.Unhealthy, *k.RLPaused, *k.MVFailed)
	}
	if ov.Trend.Source != "sampler" {
		t.Fatalf("未配置 Prometheus 时趋势应来自内置采样：%s", ov.Trend.Source)
	}

	// 6. 巡检
	var rep model.Report
	e.expect(200, "POST", "/api/clusters/prod-test/inspections", nil, &rep)
	want := map[string]string{
		"fe_alive": "ok", "be_alive": "ok", "fe_journal_lag": "warn", "fe_jvm_heap": "warn", "be_disk": "bad", "node_cpu": "bad",
		"unhealthy_tablets": "warn", "single_replica": "bad", "small_tablets": "warn", "compaction_score": "bad",
		"slow_queries": "warn", "big_query_guard": "info", "routine_load_paused": "bad", "load_fail_rate": "ok",
		"mv_failed": "warn", "mv_inactive": "warn", "default_replication": "ok", "query_queue": "info", "version_consistent": "ok",
		"os_swappiness": "ok", "os_thp": "warn", "os_overcommit": "ok", "os_max_map_count": "warn", "clock_skew": "ok",
	}
	for id, st := range want {
		if it := item(t, rep, id); it.Status != st {
			t.Errorf("%s = %s（%s），期望 %s；%s", id, it.Status, it.Value, st, it.Advice)
		}
	}
	if rep.Score >= 100 || rep.Counts["bad"] == 0 {
		t.Fatalf("健康分应扣分：%d %+v", rep.Score, rep.Counts)
	}
	var list []model.ReportMeta
	e.expect(200, "GET", "/api/clusters/prod-test/inspections", nil, &list)
	if len(list) != 1 || list[0].ID != rep.ID {
		t.Fatalf("报告列表不对：%+v", list)
	}
	var got model.Report
	e.expect(200, "GET", "/api/clusters/prod-test/inspections/"+rep.ID, nil, &got)
	e.expect(404, "GET", "/api/clusters/prod-test/inspections/IR-..%2f..%2fstate", nil, nil)

	// 忽略一个异常项 → 健康分 +4
	var after model.Report
	e.expect(200, "POST", "/api/clusters/prod-test/inspect/ignore", map[string]any{"rule": "single_replica", "ignore": true}, &after)
	if after.Score != rep.Score+4 {
		t.Fatalf("忽略后健康分应 +4：%d → %d", rep.Score, after.Score)
	}

	// 规则阈值：把磁盘异常阈值调到 90 → be_disk 变为警告
	e.expect(400, "PUT", "/api/inspect/rules/be_disk", map[string]any{"warn": 95, "bad": 90}, nil)
	e.expect(200, "PUT", "/api/inspect/rules/be_disk", map[string]any{"warn": 80, "bad": 90}, nil)
	var rep2 model.Report
	e.expect(200, "POST", "/api/clusters/prod-test/inspections", map[string]any{"cats": []string{"资源水位"}}, &rep2)
	if it := item(t, rep2, "be_disk"); it.Status != "warn" {
		t.Fatalf("阈值调整后 be_disk 应为警告：%s", it.Status)
	}
	if len(rep2.Items) != 3 {
		t.Fatalf("只巡检“资源水位”应有 3 项，实际 %d", len(rep2.Items))
	}

	// 定时巡检
	e.expect(400, "PUT", "/api/clusters/prod-test/inspect/schedule", map[string]any{"enabled": true, "every": "daily", "at": "25:00"}, nil)
	e.expect(200, "PUT", "/api/clusters/prod-test/inspect/schedule", map[string]any{"enabled": true, "every": "daily", "at": "10:00"}, nil)

	// 定时巡检结果通知：模拟飞书机器人
	var msgs []map[string]any
	var gotMu sync.Mutex
	feishu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		gotMu.Lock()
		msgs = append(msgs, b)
		gotMu.Unlock()
		io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	defer feishu.Close()
	hook := feishu.URL + "/open-apis/bot/v2/hook/TOKEN-0123456789"
	sched := map[string]any{"enabled": true, "every": "daily", "at": "10:00"}
	sched["notify"] = map[string]any{"type": "飞书", "url": hook, "secret": "sec", "when": "problem", "minScore": 80}
	e.expect(400, "PUT", "/api/clusters/prod-test/inspect/schedule", map[string]any{"enabled": true, "every": "daily", "at": "10:00", "notify": map[string]any{"type": "飞书", "when": "always"}}, nil)
	e.expect(400, "PUT", "/api/clusters/prod-test/inspect/schedule", map[string]any{"enabled": true, "every": "daily", "at": "10:00", "notify": map[string]any{"type": "QQ", "url": hook, "when": "always"}}, nil)
	var sc model.Schedule
	e.expect(200, "PUT", "/api/clusters/prod-test/inspect/schedule", sched, &sc)
	e.expect(200, "GET", "/api/clusters/prod-test/inspect/schedule", nil, &sc)
	if sc.Notify == nil || !sc.Notify.HasSecret || sc.Notify.URLEnc != "" || sc.Notify.SecretEnc != "" || sc.Notify.URLMasked == "" || strings.Contains(sc.Notify.URLMasked, "TOKEN-0123") {
		t.Fatalf("通知配置应脱敏返回：%+v", sc.Notify)
	}
	if raw, _ := os.ReadFile(filepath.Join(e.dir, "state.json")); bytes.Contains(raw, []byte("TOKEN-0123")) || bytes.Contains(raw, []byte(`"secret":`)) {
		t.Fatal("state.json 中不应有明文通知地址或密钥")
	}
	// 只改条件、不重填地址与密钥：保持原值
	sched["notify"] = map[string]any{"type": "飞书", "when": "always"}
	e.expect(200, "PUT", "/api/clusters/prod-test/inspect/schedule", sched, &sc)
	if !sc.Notify.HasSecret || sc.Notify.When != "always" {
		t.Fatalf("留空应保持原地址与密钥：%+v", sc.Notify)
	}
	e.expect(200, "POST", "/api/clusters/prod-test/inspect/notify-test", map[string]any{"type": "飞书", "when": "always"}, nil)
	if len(msgs) != 1 || msgs[0]["sign"] == nil || !strings.Contains(fmt.Sprint(msgs[0]["card"]), "【测试】") || !strings.Contains(fmt.Sprint(msgs[0]["card"]), "BE 磁盘使用率") {
		t.Fatalf("测试消息不对：%v", msgs)
	}
	e.expect(400, "POST", "/api/clusters/prod-test/inspect/notify-test", map[string]any{"type": "钉钉", "when": "always"}, nil)              // 换类型须重填地址
	e.expect(502, "POST", "/api/clusters/prod-test/inspect/notify-test", map[string]any{"type": "钉钉", "url": hook, "when": "always"}, nil) // 飞书的应答不是钉钉格式
	// 定时任务执行后发送巡检结果
	c, err := e.api.Store.Cluster("prod-test")
	must(t, err)
	var stored model.Schedule
	e.api.Store.View(func(st *store.State) { stored = st.Schedules["prod-test"] })
	e.api.Runner.RunScheduled(context.Background(), c, stored)
	gotMu.Lock()
	if len(msgs) != 3 || !strings.Contains(fmt.Sprint(msgs[2]["card"]), "定时任务") || !strings.Contains(fmt.Sprint(msgs[2]["card"]), "red") {
		t.Fatalf("定时巡检应发送结果通知：%v", msgs)
	}
	gotMu.Unlock()
	e.expect(200, "GET", "/api/clusters/prod-test/inspect/schedule", nil, &sc)
	if sc.LastNotifyResult != "成功" || sc.LastNotify.IsZero() {
		t.Fatalf("应记录通知结果：%+v", sc)
	}
	// 仅有问题时通知：无异常且分数达标时不发送
	if inspect.ShouldNotify(&model.Notify{When: "problem", MinScore: 80}, &model.Report{ReportMeta: model.ReportMeta{Score: 90, Counts: map[string]int{"warn": 2}}}) {
		t.Fatal("无异常且分数达标时不应通知")
	}
	if !inspect.ShouldNotify(&model.Notify{When: "problem", MinScore: 80}, &model.Report{ReportMeta: model.ReportMeta{Score: 79, Counts: map[string]int{}}}) {
		t.Fatal("低于阈值应通知")
	}

	// 连通性测试 / 移除
	var checks []map[string]string
	e.expect(200, "GET", "/api/clusters/prod-test/test", nil, &checks)
	e.expect(200, "DELETE", "/api/clusters/prod-test", nil, nil)
	e.expect(404, "GET", "/api/clusters/prod-test/overview", nil, nil)

	audit, _ := os.ReadFile(filepath.Join(e.dir, "audit.jsonl"))
	for _, code := range []string{"auth.login", "cluster.add", "cluster.edit", "inspect.run", "inspect.ignore", "inspect.rule", "inspect.schedule", "cluster.delete"} {
		if !bytes.Contains(audit, []byte(`"code":"`+code+`"`)) {
			t.Errorf("审计日志缺少 %s", code)
		}
	}
}

func TestEndToEndSharedDataHealthy(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedData, "healthy")
	e.login()
	body := e.conn()
	body["name"], body["env"] = "sd-test", "预发"
	var co clusterOut
	e.expect(200, "POST", "/api/clusters", body, &co)
	if co.Mode != "shared-data" || co.Version != "4.0.2" {
		t.Fatalf("应识别为存算分离 4.0.2：%s %s", co.Mode, co.Version)
	}
	var topo model.Topology
	e.expect(200, "GET", "/api/clusters/sd-test/topology", nil, &topo)
	for _, n := range topo.Nodes {
		if n.Type == "be" {
			t.Fatal("存算分离不应有 BE")
		}
	}
	var rep model.Report
	e.expect(200, "POST", "/api/clusters/sd-test/inspections", nil, &rep)
	for id, st := range map[string]string{"be_disk": "skip", "single_replica": "skip", "default_replication": "skip", "compaction_score": "ok",
		"os_thp": "skip", "slow_queries": "skip", "routine_load_paused": "ok", "query_queue": "ok", "big_query_guard": "ok"} {
		if it := item(t, rep, id); it.Status != st {
			t.Errorf("%s = %s（%s），期望 %s", id, it.Status, it.Value, st)
		}
	}
	for _, it := range rep.Items {
		if it.Status == "bad" || it.Status == "error" {
			t.Errorf("健康场景不应有 %s：%s %s %s", it.Status, it.ID, it.Value, it.Advice)
		}
	}
}
