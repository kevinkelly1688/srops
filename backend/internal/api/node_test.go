// @author: kevinkelly
// @license: Apache Licence
// @file: node_test.go
// @time: 2026/10/28

package api

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"starrocks-ops/fakesr"
	"starrocks-ops/internal/model"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// hostsEnv 启动 Hosts 模式的假集群（每个节点独立地址与 SSH 主机），登录并接入集群 prod-test（含 SSH 信息）。
func hostsEnv(t *testing.T, scenario string) (*env, string) {
	t.Helper()
	oldP, oldS, oldT := nodePollEvery, nodeStartWait, nodeStopWait
	nodePollEvery, nodeStartWait, nodeStopWait = 100*time.Millisecond, 20*time.Second, 20*time.Second
	t.Cleanup(func() { nodePollEvery, nodeStartWait, nodeStopWait = oldP, oldS, oldT })
	sp := freePort(t)
	e := newEnvCfg(t, fakesr.ModeSharedNothing, scenario, "", func(c *fakesr.Config) {
		c.Hosts, c.SSHPort, c.Spare = true, sp, 3
	})
	e.sshPort = sp
	e.login()
	cl := e.conn()
	cl["name"], cl["env"], cl["owner"] = "prod-test", "生产", "admin"
	var d detectOut
	e.expect(200, "POST", "/api/clusters/test", e.conn(), &d)
	cl["ssh"] = e.sshIn(d.Nodes)
	var out struct{ ID string }
	e.expect(200, "POST", "/api/clusters", cl, &out)
	return e, "/api/clusters/" + out.ID
}

func (e *env) topo(base string) []model.Node {
	e.t.Helper()
	var tp model.Topology
	e.expect(200, "GET", base+"/topology", nil, &tp)
	return tp.Nodes
}

func findNodeOf(nodes []model.Node, typ, role string) model.Node {
	for _, n := range nodes {
		if n.Type == typ && (role == "" || n.Role == role) {
			return n
		}
	}
	return model.Node{}
}

func nodePath(base string, n model.Node, op string) string {
	return fmt.Sprintf("%s/nodes/%s/%s/%s", base, n.Type, nodeKey(n), op)
}

// waitTask 轮询任务直到结束。
func (e *env) waitTask(id string) model.Task {
	e.t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		var r struct{ Task model.Task }
		e.expect(200, "GET", "/api/tasks/"+id, nil, &r)
		if r.Task.Finished() {
			return r.Task
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("任务 %s 超时：%+v", id, r.Task)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *env) runNode(path string, body any) model.Task {
	e.t.Helper()
	var r struct {
		TaskID string `json:"taskId"`
	}
	e.expect(200, "POST", path, body, &r)
	if r.TaskID == "" {
		e.t.Fatal("没有返回任务号")
	}
	return e.waitTask(r.TaskID)
}

func logHas(t model.Task, sub string) bool { return strings.Contains(strings.Join(t.Log, "\n"), sub) }

func TestNodeStartStopRestart(t *testing.T) {
	e, base := hostsEnv(t, "healthy")
	nodes := e.topo(base)
	if len(nodes) != 12 {
		t.Fatalf("节点数 %d", len(nodes))
	}
	hosts := map[string]bool{}
	for _, n := range nodes {
		hosts[n.Host] = true
	}
	if len(hosts) != 12 {
		t.Fatalf("Hosts 模式下每个节点应有独立地址，实际 %d 个", len(hosts))
	}

	be := findNodeOf(nodes, "be", "")
	// 停止 BE（3.5.6 默认优雅退出 -g）
	tk := e.runNode(nodePath(base, be, "stop"), nil)
	if tk.State != "done" || !logHas(tk, "bin/stop_be.sh -g") || !logHas(tk, "进程已退出") {
		t.Fatalf("停止任务：%+v", tk)
	}
	if st := findNodeOf(e.topo(base), "be", ""); st.Alive {
		t.Fatal("停止后 BE 应为失联")
	}
	// 再次停止：无需操作
	tk = e.runNode(nodePath(base, be, "stop"), map[string]any{"graceful": false})
	if tk.State != "done" || !logHas(tk, "无需停止") {
		t.Fatalf("重复停止：%+v", tk)
	}
	// 启动，等待 SQL 显示存活
	tk = e.runNode(nodePath(base, be, "start"), nil)
	if tk.State != "done" || !logHas(tk, "Alive = true") || tk.Prog != 100 {
		t.Fatalf("启动任务：%+v", tk)
	}
	if !findNodeOf(e.topo(base), "be", "").Alive {
		t.Fatal("启动后 BE 应存活")
	}
	// 重启
	tk = e.runNode(nodePath(base, be, "restart"), nil)
	if tk.State != "done" || !logHas(tk, "stop_be.sh") || !logHas(tk, "start_be.sh") {
		t.Fatalf("重启任务：%+v", tk)
	}
	// 任务列表与审计
	var l struct{ Items []model.Task }
	e.expect(200, "GET", "/api/tasks?cluster=prod-test&kind=node.", nil, &l)
	if len(l.Items) != 4 || len(l.Items[0].Log) != 0 {
		t.Fatalf("任务列表应有 4 条且不含日志：%d", len(l.Items))
	}
	if a := lastAudit(t, e, "node.restart"); !strings.Contains(a.Act, "重启 BE") || a.Res != "成功" || !strings.Contains(a.SQL, "stop_be.sh") {
		t.Fatalf("审计：%+v", a)
	}

	// 非法标识 / 不存在的节点
	e.expect(400, "POST", base+"/nodes/xx/1.2.3.4:5/start", nil, nil)
	e.expect(404, "POST", base+"/nodes/be/10.9.9.9:19050/start", nil, nil)
	e.expect(404, "POST", "/api/clusters/nope/nodes/be/1.2.3.4:5/start", nil, nil)
}

func TestNodeFEQuorum(t *testing.T) {
	e, base := hostsEnv(t, "healthy")
	nodes := e.topo(base)
	leader := findNodeOf(nodes, "fe", "Leader")
	var followers []model.Node
	for _, n := range nodes {
		if n.Type == "fe" && n.Role == "Follower" {
			followers = append(followers, n)
		}
	}
	// 4 个 FE：1 Leader + 2 Follower + 1 Observer。停掉 Leader 后 Follower 接任，仍有 2/3 存活
	tk := e.runNode(nodePath(base, leader, "stop"), nil)
	if tk.State != "done" || logHas(tk, "stop_fe.sh -g") {
		t.Fatalf("停止 Leader：%+v", tk)
	}
	after := e.topo(base)
	if findNodeOf(after, "fe", "Leader").Host == leader.Host {
		t.Fatal("停止 Leader 后应由 Follower 接任")
	}
	// 此时再停一个 Follower：存活 1/3，不足多数
	var victim model.Node
	for _, n := range after {
		if n.Type == "fe" && n.Alive && n.Role != "Observer" {
			victim = n
		}
	}
	if code := e.call("POST", nodePath(base, victim, "stop"), nil, nil); code != 409 {
		t.Fatalf("多数派检查应返回 409，实际 %d", code)
	}
	// 强制
	tk = e.runNode(nodePath(base, victim, "stop"), map[string]any{"force": true})
	if tk.State != "done" {
		t.Fatalf("强制停止：%+v", tk)
	}
	// 启动旧 Leader 恢复
	tk = e.runNode(nodePath(base, leader, "start"), nil)
	if tk.State != "done" {
		t.Fatalf("启动 FE：%+v", tk)
	}
	_ = followers
}

func TestNodeBusyAndFailure(t *testing.T) {
	e, base := hostsEnv(t, "healthy")
	be := findNodeOf(e.topo(base), "be", "")
	// 同一节点并发操作：第二个请求 409
	var r struct {
		TaskID string `json:"taskId"`
	}
	e.expect(200, "POST", nodePath(base, be, "stop"), map[string]any{"graceful": true}, &r)
	e.expect(409, "POST", nodePath(base, be, "start"), nil, nil)
	e.waitTask(r.TaskID)

	// 启动脚本不存在 → 任务失败并保留原因
	cl := e.api.Store.Clusters()[0]
	cl.Dirs.BE = "/opt/nowhere/be"
	cl.UpdatedAt = time.Now()
	must(t, e.api.Store.PutCluster(cl, false))
	tk := e.runNode(nodePath(base, be, "start"), nil)
	if tk.State != "failed" || !strings.Contains(tk.Err, "退出码 127") {
		t.Fatalf("应因脚本不存在而失败：%+v", tk)
	}
	if a := lastAudit(t, e, "node.start"); !strings.HasPrefix(a.Res, "失败") {
		t.Fatalf("审计应记录失败：%+v", a)
	}

	// 没有 SSH 信息的集群不能操作
	c2 := e.conn()
	c2["name"], c2["env"], c2["owner"] = "nossh", "测试", "admin"
	e.expect(200, "POST", "/api/clusters", c2, nil)
	e.expect(409, "POST", nodePath("/api/clusters/nossh", be, "start"), nil, nil)
}

func TestNodeDecommissionDropBlacklist(t *testing.T) {
	e, base := hostsEnv(t, "healthy")
	nodes := e.topo(base)

	// FE：不能移除 Leader、不能下线 FE
	leader := findNodeOf(nodes, "fe", "Leader")
	e.expect(400, "POST", nodePath(base, leader, "drop"), nil, nil)
	e.expect(400, "POST", nodePath(base, leader, "decommission"), nil, nil)
	obs := findNodeOf(nodes, "fe", "Observer")
	var res map[string]any
	e.expect(200, "POST", nodePath(base, obs, "drop"), nil, &res)
	if !strings.Contains(res["sql"].(string), `DROP OBSERVER "`+nodeKey(obs)+`"`) {
		t.Fatalf("SQL：%v", res["sql"])
	}
	if findNodeOf(e.topo(base), "fe", "Observer").Host != "" {
		t.Fatal("Observer 应已移除")
	}

	// 黑名单：加入 → 列表带原因 → 移出
	be := findNodeOf(nodes, "be", "")
	e.expect(400, "POST", base+"/node-blacklist", map[string]any{"kind": "fe", "node": nodeKey(be)}, nil)
	e.expect(404, "POST", base+"/node-blacklist", map[string]any{"kind": "be", "node": "9.9.9.9:1", "reason": "x"}, nil)
	var add struct{ ID string }
	e.expect(200, "POST", base+"/node-blacklist", map[string]any{"kind": "be", "node": nodeKey(be), "reason": "磁盘 IO 抖动"}, &add)
	var bl struct{ Items []blEntry }
	e.expect(200, "GET", base+"/node-blacklist", nil, &bl)
	if len(bl.Items) != 1 || bl.Items[0].ID != add.ID || bl.Items[0].Reason != "磁盘 IO 抖动" || bl.Items[0].Node != nodeKey(be) || bl.Items[0].By != "admin" {
		t.Fatalf("黑名单：%+v", bl.Items)
	}
	e.expect(200, "DELETE", base+"/node-blacklist/be/"+add.ID, nil, nil)
	bl.Items = nil
	e.expect(200, "GET", base+"/node-blacklist", nil, &bl)
	if len(bl.Items) != 0 {
		t.Fatalf("移出后应为空：%+v", bl.Items)
	}
	if a := lastAudit(t, e, "node.blacklist"); !strings.Contains(a.SQL, "DELETE BACKEND BLACKLIST") {
		t.Fatalf("审计：%+v", a)
	}

	// CN 黑名单走 COMPUTE NODE 语句
	cn := findNodeOf(nodes, "cn", "")
	e.expect(200, "POST", base+"/node-blacklist", map[string]any{"kind": "cn", "node": nodeKey(cn), "reason": "维护"}, nil)
	if a := lastAudit(t, e, "node.blacklist"); !strings.Contains(a.SQL, "ADD COMPUTE NODE BLACKLIST") {
		t.Fatalf("审计：%+v", a)
	}

	// BE 下线：进入 decommission 状态，随后被移除
	e.expect(200, "POST", nodePath(base, be, "decommission"), nil, &res)
	if !strings.Contains(res["sql"].(string), "DECOMMISSION BACKEND") {
		t.Fatalf("SQL：%v", res["sql"])
	}
	if n := findNodeOf(e.topo(base), "be", ""); n.Status != "decommission" {
		t.Fatalf("下线中的 BE 状态应为 decommission，实际 %s", n.Status)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		gone := true
		for _, n := range e.topo(base) {
			if n.Type == "be" && nodeKey(n) == nodeKey(be) {
				gone = false
			}
		}
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("下线完成后节点应被移除")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// CN 下线 = DROP COMPUTE NODE
	e.expect(200, "POST", nodePath(base, cn, "decommission"), nil, &res)
	if !strings.Contains(res["sql"].(string), "DROP COMPUTE NODE") {
		t.Fatalf("SQL：%v", res["sql"])
	}
}

func TestNodeLog(t *testing.T) {
	e, base := hostsEnv(t, "healthy")
	nodes := e.topo(base)
	fe := findNodeOf(nodes, "fe", "Leader")
	var lg struct {
		File  string
		Files []string
		Text  string
		Lines int
	}
	e.expect(200, "GET", nodePath(base, fe, "log")+"?lines=50", nil, &lg)
	if lg.File != "fe.log" || lg.Lines != 50 || !strings.Contains(lg.Text, "tablet scheduler") || len(lg.Files) != 4 {
		t.Fatalf("日志：%+v", lg)
	}
	// 关键字过滤 + 指定文件
	e.expect(200, "GET", nodePath(base, fe, "log")+"?file=fe.warn.log&q=exceeds&lines=10", nil, &lg)
	if lg.File != "fe.warn.log" || lg.Lines != 10 || strings.Contains(lg.Text, "INFO") {
		t.Fatalf("过滤日志：%+v", lg)
	}
	e.expect(200, "GET", nodePath(base, fe, "log")+"?q=no-such-keyword-xyz", nil, &lg)
	if lg.Lines != 0 || lg.Text != "" {
		t.Fatalf("无匹配应为空：%+v", lg)
	}
	// 文件名必须在白名单内（防路径穿越）
	for _, f := range []string{"../conf/fe.conf", "fe.conf", "be.INFO", "fe.log%3Bid"} {
		e.expect(400, "GET", nodePath(base, fe, "log")+"?file="+f, nil, nil)
	}
	e.expect(400, "GET", nodePath(base, fe, "log")+"?q="+strings.Repeat("x", 101), nil, nil)
	be := findNodeOf(nodes, "be", "")
	e.expect(200, "GET", nodePath(base, be, "log")+"?file=be.INFO&lines=5", nil, &lg)
	if lg.Lines != 5 || !strings.Contains(lg.Text, "compaction") {
		t.Fatalf("BE 日志：%+v", lg)
	}
}

func TestNodeDualReview(t *testing.T) {
	e, base := hostsEnv(t, "healthy")
	e.expect(200, "PUT", "/api/settings/security", map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": true, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}, nil)
	e.newUser("admin2", []string{"r_admin"}, "Admin2@2026xyzab")
	admin2 := e.firstLogin("admin2", "Admin2@2026xyzab", "Admin2@2026xyzab2")
	be := findNodeOf(e.topo(base), "be", "")
	// 启动不需要复核；停止需要
	var pend map[string]any
	e.expect(202, "POST", nodePath(base, be, "stop"), nil, &pend)
	if !findNodeOf(e.topo(base), "be", "").Alive {
		t.Fatal("待复核期间不应停止节点")
	}
	var ap map[string]any
	admin2.expect(200, "POST", "/api/reviews/"+pend["reviewId"].(string)+"/approve", nil, &ap)
	if ap["ok"] != true {
		t.Fatalf("批准失败：%v", ap)
	}
	// 批准后任务已在后台创建
	var l struct{ Items []model.Task }
	e.expect(200, "GET", "/api/tasks?kind=node.stop", nil, &l)
	if len(l.Items) != 1 {
		t.Fatalf("批准后应创建 1 个停止任务：%d", len(l.Items))
	}
	if tk := e.waitTask(l.Items[0].ID); tk.State != "done" || tk.By != "admin" {
		t.Fatalf("任务：%+v", tk)
	}
	pend = map[string]any{}
	e.expect(200, "POST", nodePath(base, be, "start"), nil, &pend)
	if _, isPending := pend["pending"]; isPending {
		t.Fatal("启动不应进入复核")
	}
}

func TestParamPersistToConf(t *testing.T) {
	e, base := hostsEnv(t, "healthy")
	nodes := e.topo(base)
	fe := findNodeOf(nodes, "fe", "Leader")
	fePath := "/opt/starrocks/fe/conf/fe.conf"

	// FE 动态参数：在线修改 + 写入全部 FE 的 fe.conf
	var res map[string]any
	e.expect(200, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "3000", "persist": true}, &res)
	for _, n := range nodes {
		if n.Type != "fe" {
			continue
		}
		txt, ok := e.fake.HostFile(n.Host, fePath)
		if !ok || !strings.Contains(txt, "qe_slow_log_ms = 3000\n") || !strings.Contains(txt, "edit_log_port") {
			t.Fatalf("%s 的 fe.conf 未写入或丢失原内容：%q", n.Host, txt)
		}
		if _, bak := e.fake.HostFile(n.Host, fePath); !bak {
			t.Fatal("缺少配置文件")
		}
	}
	var h struct{ Items []model.ParamChange }
	e.expect(200, "GET", base+"/params/history", nil, &h)
	if len(h.Items) != 1 || !strings.Contains(h.Items[0].Persist, "fe.conf") || h.Items[0].Status != "已生效" {
		t.Fatalf("历史：%+v", h.Items)
	}
	if a := lastAudit(t, e, "param.edit"); !strings.Contains(a.SQL, "fe.conf@"+fe.Host) || !strings.Contains(a.SQL, "已备份为") {
		t.Fatalf("审计应记录写入的文件与备份：%+v", a)
	}

	// 再次修改同一参数：就地替换，不重复追加
	e.expect(200, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "4000", "persist": true, "scope": []string{fmt.Sprintf("%s:%d", fe.Host, fe.Port)}}, &res)
	txt, _ := e.fake.HostFile(fe.Host, fePath)
	if strings.Count(txt, "qe_slow_log_ms") != 1 || !strings.Contains(txt, "qe_slow_log_ms = 4000") {
		t.Fatalf("应就地替换：%q", txt)
	}
	other := findNodeOf(nodes, "fe", "Follower")
	if o, _ := e.fake.HostFile(other.Host, fePath); !strings.Contains(o, "qe_slow_log_ms = 3000") {
		t.Fatalf("scope 之外的 FE 不应被修改：%q", o)
	}

	// 静态参数：不带 persist 被拒绝；带 persist 只写文件（不在线修改）
	e.expect(400, "PUT", base+"/params/fe/http_port", map[string]any{"value": "8031"}, nil)
	e.expect(200, "PUT", base+"/params/fe/http_port", map[string]any{"value": "8031", "persist": true, "scope": []string{fmt.Sprintf("%s:%d", fe.Host, fe.Port)}}, &res)
	if txt, _ := e.fake.HostFile(fe.Host, fePath); !strings.Contains(txt, "http_port = 8031") {
		t.Fatalf("静态参数应只写文件：%q", txt)
	}
	var fe1 paramList
	e.expect(200, "GET", base+"/params/fe", nil, &fe1)
	if p, _ := fe1.get("http_port"); p.Value != "8030" {
		t.Fatalf("静态参数在线值不应变化：%s", p.Value)
	}

	// BE 参数：写入对应 be.conf（动态参数在线 + 文件）
	be := findNodeOf(nodes, "be", "")
	e.expect(200, "PUT", base+"/params/be/max_compaction_concurrency", map[string]any{"value": "4", "persist": true, "scope": []string{fmt.Sprintf("%s:%d", be.Host, be.HTTP)}}, &res)
	bt, _ := e.fake.HostFile(be.Host, "/opt/starrocks/be/conf/be.conf")
	if !strings.Contains(bt, "max_compaction_concurrency = 4") || !strings.Contains(bt, "storage_root_path") {
		t.Fatalf("be.conf：%q", bt)
	}
	// BE 静态参数 + persist：只写文件，在线值不变
	e.expect(200, "PUT", base+"/params/be/mem_limit", map[string]any{"value": "80%", "persist": true, "scope": []string{fmt.Sprintf("%s:%d", be.Host, be.HTTP)}}, &res)
	bt, _ = e.fake.HostFile(be.Host, "/opt/starrocks/be/conf/be.conf")
	if !strings.Contains(bt, "mem_limit = 80%") {
		t.Fatalf("静态参数应写入文件：%q", bt)
	}
	var bl paramList
	e.expect(200, "GET", base+"/params/be", nil, &bl)
	if p, _ := bl.get("mem_limit"); p.Value != "90%" {
		t.Fatalf("静态参数在线值不应变化：%s", p.Value)
	}
	e.expect(200, "GET", base+"/params/history", nil, &h)
	if h.Items[0].Key != "mem_limit" || !strings.Contains(h.Items[0].Persist, "be.conf") {
		t.Fatalf("历史：%+v", h.Items[0])
	}

	// 持久化需要单独的 param.persist 权限
	var ro roleOut
	e.expect(200, "POST", "/api/roles", map[string]any{"name": "参数运维", "perms": []string{"param:view", "param.edit"}, "clusters": []string{"*"}}, &ro)
	e.newUser("param1", []string{ro.ID}, "Param@2026xyzabcd")
	pu := e.firstLogin("param1", "Param@2026xyzabcd", "Param@2026xyzabcd2")
	pu.expect(403, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "1234", "persist": true}, nil)
	pu.expect(200, "PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "1234"}, nil)
	if txt, _ := e.fake.HostFile(fe.Host, fePath); strings.Contains(txt, "1234") {
		t.Fatal("没有 param.persist 权限时不应写文件")
	}

	// 全局变量无需持久化
	e.expect(400, "PUT", base+"/params/var/query_timeout", map[string]any{"value": "600", "persist": true}, nil)

	// 一台主机写入失败：报错，且不影响其他台已写入的结果
	cl := e.api.Store.Clusters()[0]
	cl.Dirs.FE = "/opt/starrocks/fe2"
	cl.UpdatedAt = time.Now()
	must(t, e.api.Store.PutCluster(cl, false))
	if code := e.call("PUT", base+"/params/fe/qe_slow_log_ms", map[string]any{"value": "1", "persist": true}, nil); code != 502 {
		t.Fatalf("目录不存在时应失败（502），实际 %d", code)
	}
	e.expect(200, "GET", base+"/params/history", nil, &h)
	if !strings.HasPrefix(h.Items[0].Status, "失败") {
		t.Fatalf("历史应记录失败：%+v", h.Items[0])
	}
}

func TestSetConfLine(t *testing.T) {
	in := "# fe.conf\nmeta_dir = /a\n  query_timeout=5\n#http_port = 1\nhttp_port = 8030\n"
	out := setConfLine(in, "http_port", "9999")
	if !strings.Contains(out, "http_port = 9999\n") || !strings.Contains(out, "#http_port = 1\n") || strings.Count(out, "9999") != 1 {
		t.Fatalf("替换：%q", out)
	}
	if !strings.Contains(setConfLine(in, "query_timeout", "9"), "\nquery_timeout = 9\n") {
		t.Fatal("带前导空格、无空格的写法也应被替换")
	}
	out = setConfLine("a = 1", "b", "2")
	if out != "a = 1\nb = 2\n" {
		t.Fatalf("追加：%q", out)
	}
	if v, ok := confValue("x = 1\nx = 2 \n", "x"); !ok || v != "2" {
		t.Fatalf("confValue：%q", v)
	}
	if !verAtLeast("3.5.6", 3, 4) || verAtLeast("3.3.9", 3, 4) || !verAtLeast("4.0.2", 3, 4) || verAtLeast("", 3, 4) {
		t.Fatal("版本比较")
	}
}
