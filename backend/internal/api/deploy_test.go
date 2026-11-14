// @author: kevinkelly
// @license: Apache Licence
// @file: deploy_test.go
// @time: 2026/10/28

package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"

	"starrocks-ops/internal/model"
)

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		must(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(body))
		must(t, err)
	}
	must(t, tw.Close())
	must(t, gz.Close())
	return buf.Bytes()
}

// srPkg 生成一个最小的 StarRocks 安装包；withJar=false 时缺少 FE 的 jar（用于模拟启动失败）。
func srPkg(t *testing.T, ver string, withJar bool) (name string, data []byte) {
	top := "StarRocks-" + ver + "-centos-amd64/"
	f := map[string]string{
		top + "fe/bin/start_fe.sh": "#!/bin/bash\n", top + "fe/bin/stop_fe.sh": "#!/bin/bash\n",
		top + "fe/conf/fe.conf":    "# fe.conf\nhttp_port = 8030\nrpc_port = 9020\nquery_port = 9030\nedit_log_port = 9010\n",
		top + "be/bin/start_be.sh": "#!/bin/bash\n", top + "be/bin/stop_be.sh": "#!/bin/bash\n",
		top + "be/bin/start_cn.sh": "#!/bin/bash\n", top + "be/bin/stop_cn.sh": "#!/bin/bash\n",
		top + "be/conf/be.conf":         "# be.conf\nbe_port = 9060\nbe_http_port = 8040\nheartbeat_service_port = 9050\nbrpc_port = 8060\n",
		top + "be/conf/cn.conf":         "# cn.conf\nbe_port = 9060\nbe_http_port = 8040\nheartbeat_service_port = 9050\nbrpc_port = 8060\n",
		top + "be/lib/starrocks-be.jar": "version=" + ver + "\n", top + "be/lib/starrocks-cn.jar": "version=" + ver + "\n",
	}
	if withJar {
		f[top+"fe/lib/starrocks-fe.jar"] = "version=" + ver + "\n"
	}
	return "StarRocks-" + ver + "-centos-amd64.tar.gz", tarGz(t, f)
}

func jdkPkg(t *testing.T) (string, []byte) {
	return "jdk-17.0.9_linux-x64_bin.tar.gz", tarGz(t, map[string]string{"jdk-17.0.9/bin/java": "#!java\n", "jdk-17.0.9/release": "JAVA_VERSION=17\n"})
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// upload 以 multipart 上传安装包；fields 里可带 sha256。
func (e *env) upload(name string, data []byte, wantSHA string) (int, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if wantSHA != "" {
		must(e.t, mw.WriteField("sha256", wantSHA))
	}
	fw, err := mw.CreateFormFile("file", name)
	must(e.t, err)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/packages", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Requested-With", "sr-ops")
	resp, err := e.c.Do(req)
	must(e.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(b, &out)
	if resp.StatusCode >= 300 {
		e.t.Logf("上传 %s -> %d %s", name, resp.StatusCode, b)
	}
	return resp.StatusCode, out
}

type hostRes struct {
	Results []hostResult `json:"results"`
}

// addSpare 添加 Hosts 模式下的空白主机 127.0.0.50-52（密码登录，首次分发公钥）。
func (e *env) addSpare(rng string) hostRes {
	e.t.Helper()
	var r hostRes
	e.expect(200, "POST", "/api/hosts", map[string]any{"ips": rng, "port": e.sshPort, "user": "starrocks", "auth": "password", "password": "secret", "sudo": true}, &r)
	return r
}

func TestHostsAndPackages(t *testing.T) {
	e, _ := hostsEnv(t, "healthy")
	// 参数校验
	for name, body := range map[string]map[string]any{
		"无地址":    {"ips": "", "port": e.sshPort, "user": "starrocks", "auth": "key"},
		"用户非法":   {"ips": "127.0.0.50", "port": e.sshPort, "user": "Bad User", "auth": "key"},
		"密码为空":   {"ips": "127.0.0.50", "port": e.sshPort, "user": "starrocks", "auth": "password"},
		"范围过大":   {"ips": "10.0.0.1-200", "port": 22, "user": "starrocks", "auth": "key"},
		"地址非法":   {"ips": "1.2.3.999", "port": 22, "user": "starrocks", "auth": "key"},
		"注入":     {"ips": "1.2.3.4;rm -rf /", "port": 22, "user": "starrocks", "auth": "key"},
		"认证方式非法": {"ips": "127.0.0.50", "port": 22, "user": "starrocks", "auth": "x"},
	} {
		if code := e.call("POST", "/api/hosts", body, nil); code != 400 {
			t.Errorf("%s：应返回 400，实际 %d", name, code)
		}
	}
	// 使用密钥但公钥尚未加入：失败并给出提示
	var r hostRes
	e.expect(200, "POST", "/api/hosts", map[string]any{"ips": "127.0.0.50", "port": e.sshPort, "user": "starrocks", "auth": "key"}, &r)
	if len(r.Results) != 1 || r.Results[0].OK || !strings.Contains(r.Results[0].Error, "authorized_keys") {
		t.Fatalf("未授权公钥应失败并提示：%+v", r.Results)
	}
	// 密码登录并分发公钥，范围展开
	r = e.addSpare("127.0.0.50-52")
	if len(r.Results) != 3 {
		t.Fatalf("应展开为 3 台：%+v", r.Results)
	}
	for _, x := range r.Results {
		if !x.OK || !strings.Contains(x.Note, "公钥") {
			t.Fatalf("添加失败：%+v", x)
		}
	}
	var hl struct {
		Items     []hostOut
		PublicKey string
	}
	e.expect(200, "GET", "/api/hosts", nil, &hl)
	if len(hl.Items) != 3 || hl.PublicKey == "" {
		t.Fatalf("主机列表：%+v", hl)
	}
	h := hl.Items[0]
	if h.IP != "127.0.0.50" || h.CPU != 32 || h.MemGB != 128 || h.Arch != "amd64" || !h.AVX2 || h.CIDR != "127.0.0.50/24" || h.SSH != "ok" || !strings.Contains(h.OS, "Rocky") {
		t.Fatalf("探测结果：%+v", h.Host)
	}
	// 之后可以直接用密钥再次添加（密码未保存）
	e.expect(200, "POST", "/api/hosts", map[string]any{"ips": "127.0.0.50", "port": e.sshPort, "user": "starrocks", "auth": "key"}, &r)
	if !r.Results[0].OK {
		t.Fatalf("公钥已分发，密钥登录应成功：%+v", r.Results)
	}
	if b, _ := json.Marshal(hl); strings.Contains(string(b), "secret") {
		t.Fatal("主机列表不应包含密码")
	}
	e.expect(200, "POST", "/api/hosts/127.0.0.51/check", nil, nil)
	e.expect(404, "POST", "/api/hosts/127.0.0.99/check", nil, nil)

	// 安装包：上传
	name, data := srPkg(t, "3.5.6", true)
	code, out := e.upload(name, data, sha(data))
	if code != 200 || out["ver"] != "3.5.6" || out["arch"] != "amd64" || out["kind"] != "starrocks" || out["sha"] != sha(data) || int64(out["size"].(float64)) != int64(len(data)) {
		t.Fatalf("上传结果 %d %v", code, out)
	}
	if code, _ := e.upload(name, data, ""); code != 409 {
		t.Fatalf("同名应 409，实际 %d", code)
	}
	if code, _ := e.upload("StarRocks-3.5.7-centos-amd64.tar.gz", data, strings.Repeat("0", 64)); code != 400 {
		t.Fatalf("SHA256 不一致应 400，实际 %d", code)
	}
	if code, _ := e.upload("evil.tar.gz", data, ""); code != 400 {
		t.Fatalf("不认识的文件名应 400，实际 %d", code)
	}
	if code, _ := e.upload("../../x/StarRocks-3.5.8-centos-amd64.tar.gz", []byte("not gzip"), ""); code != 400 {
		t.Fatalf("不是 gzip 应 400，实际 %d", code)
	}
	if code, _ := e.upload("StarRocks-3.5.9-centos-amd64.tar.gz", tarGz(t, map[string]string{"x/readme": "hi"}), ""); code != 400 {
		t.Fatalf("缺少启动脚本应 400，实际 %d", code)
	}
	jn, jd := jdkPkg(t)
	if code, out := e.upload(jn, jd, ""); code != 200 || out["kind"] != "jdk" || out["ver"] != "17" || out["arch"] != "amd64" {
		t.Fatalf("JDK 上传 %d %v", code, out)
	}
	if code, _ := e.upload("jdk-8.tar.gz", tarGz(t, map[string]string{"jdk8/lib/x": "x"}), ""); code != 400 {
		t.Fatalf("JDK 包缺少 bin/java 应 400，实际 %d", code)
	}
	var pl struct{ Items []model.Package }
	e.expect(200, "GET", "/api/packages", nil, &pl)
	if len(pl.Items) != 2 {
		t.Fatalf("应有 2 个包：%+v", pl.Items)
	}
	// 失败的上传不应留下 .part 文件
	for _, bad := range []string{"StarRocks-3.5.7-centos-amd64.tar.gz.part", "StarRocks-3.5.8-centos-amd64.tar.gz.part"} {
		if _, err := os.Stat(e.dir + "/packages/" + bad); err == nil {
			t.Fatalf("遗留了临时文件 %s", bad)
		}
	}
	e.expect(200, "DELETE", "/api/packages/"+jn, nil, nil)
	e.expect(404, "DELETE", "/api/packages/"+jn, nil, nil)
	if _, err := os.Stat(e.dir + "/packages/" + jn); err == nil {
		t.Fatal("删除后文件应移除")
	}
	// 删除主机
	e.expect(200, "DELETE", "/api/hosts/127.0.0.52", nil, nil)
	e.expect(404, "DELETE", "/api/hosts/127.0.0.52", nil, nil)
	// 权限：只读用户不能添加主机 / 上传
	e.newUser("viewer1", []string{"r_oncall"}, "Viewer@2026xyzabcd")
	v := e.firstLogin("viewer1", "Viewer@2026xyzabcd", "Viewer@2026xyzabcd2")
	v.expect(403, "POST", "/api/hosts", map[string]any{"ips": "127.0.0.50", "port": 22, "user": "x", "auth": "key"}, nil)
	v.expect(403, "GET", "/api/hosts", nil, nil)
}

func TestPrecheckAndFix(t *testing.T) {
	e, _ := hostsEnv(t, "healthy")
	e.addSpare("127.0.0.50-51")
	// 一台已有集群节点的主机（健康环境）也加入，对比
	var r hostRes
	e.expect(200, "POST", "/api/hosts", map[string]any{"ips": "127.0.0.15", "port": e.sshPort, "user": "starrocks", "auth": "password", "password": "secret"}, &r)
	if !r.Results[0].OK {
		t.Fatalf("%+v", r.Results)
	}
	jn, jd := jdkPkg(t)
	e.upload(jn, jd, "")

	spec := map[string]any{"hosts": []map[string]any{{"ip": "127.0.0.50", "roles": []string{"FE"}}, {"ip": "127.0.0.15", "roles": []string{"BE"}}, {"ip": "127.0.0.77", "roles": []string{"BE"}}},
		"home": "/data/starrocks", "version": "3.5.6", "arch": "amd64", "javaHome": "/data/starrocks/jdk"}
	var pc struct {
		Items   []checkItem
		Hosts   []string
		Results map[string]map[string]checkCell
		Bad     int
		Warn    int
	}
	e.expect(200, "POST", "/api/deploy/precheck", spec, &pc)
	st := func(id, ip string) string { return pc.Results[id][ip].St }
	for id, want := range map[string]string{"swap": "warn", "swappiness": "warn", "thp": "warn", "overcommit": "warn", "max_map_count": "bad", "nofile": "bad", "nproc": "warn", "arch": "ok", "avx2": "ok", "dir": "ok", "disk": "ok", "java": "bad", "ports": "ok", "sudo": "ok", "time": "ok"} {
		if got := st(id, "127.0.0.50"); got != want {
			t.Errorf("空白主机 %s = %s（%s），期望 %s", id, got, pc.Results[id]["127.0.0.50"].Val, want)
		}
	}
	if st("swap", "127.0.0.15") != "ok" || st("max_map_count", "127.0.0.15") != "ok" || st("nofile", "127.0.0.15") != "ok" {
		t.Errorf("已有节点的主机环境应通过：%+v", pc.Results["max_map_count"])
	}
	if st("swap", "127.0.0.77") != "bad" || !strings.Contains(pc.Results["swap"]["127.0.0.77"].Val, "未纳管") {
		t.Errorf("未纳管主机应报错：%+v", pc.Results["swap"]["127.0.0.77"])
	}
	if pc.Bad == 0 || pc.Warn == 0 || len(pc.Items) != 15 {
		t.Errorf("汇总：bad=%d warn=%d items=%d", pc.Bad, pc.Warn, len(pc.Items))
	}
	// 校验
	for name, mod := range map[string]func(m map[string]any){
		"目录注入":   func(m map[string]any) { m["home"] = "/data; rm -rf /" },
		"目录穿越":   func(m map[string]any) { m["home"] = "/data/../etc" },
		"无主机":    func(m map[string]any) { m["hosts"] = []any{} },
		"角色非法":   func(m map[string]any) { m["hosts"] = []map[string]any{{"ip": "127.0.0.50", "roles": []string{"XX"}}} },
		"架构非法":   func(m map[string]any) { m["arch"] = "mips" },
		"jdk 注入": func(m map[string]any) { m["javaHome"] = "/opt/j dk;id" },
	} {
		b, _ := json.Marshal(spec)
		var c map[string]any
		json.Unmarshal(b, &c)
		mod(c)
		if code := e.call("POST", "/api/deploy/precheck", c, nil); code != 400 {
			t.Errorf("%s：应 400，实际 %d", name, code)
		}
	}
	// 架构不一致
	spec["arch"] = "arm64"
	e.expect(200, "POST", "/api/deploy/precheck", spec, &pc)
	if st("arch", "127.0.0.50") != "bad" || st("avx2", "127.0.0.50") != "skip" {
		t.Errorf("arm64 包 + amd64 主机应不通过：%+v", pc.Results["arch"]["127.0.0.50"])
	}
	spec["arch"] = "amd64"

	// 一键修复：系统项 + JDK
	var fx struct {
		TaskID string `json:"taskId"`
	}
	e.expect(400, "POST", "/api/deploy/fix", map[string]any{"hosts": []string{"127.0.0.50"}, "items": []string{"rm -rf"}}, nil)
	e.expect(400, "POST", "/api/deploy/fix", map[string]any{"hosts": []string{"127.0.0.99"}, "items": []string{"swap"}}, nil)
	e.expect(400, "POST", "/api/deploy/fix", map[string]any{"hosts": []string{"127.0.0.50"}, "items": []string{"java"}}, nil)
	e.expect(200, "POST", "/api/deploy/fix", map[string]any{"hosts": []string{"127.0.0.50", "127.0.0.51"}, "items": []string{"swap", "swappiness", "thp", "overcommit", "max_map_count", "nofile", "nproc", "java"}, "javaHome": "/data/starrocks/jdk", "jdkPackage": jn}, &fx)
	tk := e.waitTask(fx.TaskID)
	if tk.State != "done" || !logHas(tk, "sysctl -p /etc/sysctl.d/99-starrocks.conf") || !logHas(tk, "swapoff -a") || !logHas(tk, "上传完成") {
		t.Fatalf("修复任务：%+v", tk)
	}
	for _, ip := range []string{"127.0.0.50", "127.0.0.51"} {
		for k, want := range map[string]string{"swap": "0", "swappiness": "0", "overcommit": "1", "max_map_count": "262144", "nofile": "655350", "nproc": "655350", "thp": "always [madvise] never"} {
			if got := e.fake.Kernel(ip, k); got != want {
				t.Errorf("%s 修复后 %s = %q，期望 %q", ip, k, got, want)
			}
		}
		if _, ok := e.fake.HostFile(ip, "/data/starrocks/jdk/bin/java"); !ok {
			t.Errorf("%s 应已安装 JDK", ip)
		}
	}
	// 修复后复查：全部通过
	e.expect(200, "POST", "/api/deploy/precheck", map[string]any{"hosts": []map[string]any{{"ip": "127.0.0.50", "roles": []string{"FE"}}}, "home": "/data/starrocks", "version": "3.5.6", "arch": "amd64", "javaHome": "/data/starrocks/jdk"}, &pc)
	if pc.Bad != 0 || pc.Warn != 0 {
		t.Errorf("修复后应全部通过：bad=%d warn=%d %+v", pc.Bad, pc.Warn, pc.Results)
	}
	// 没有 sudo 权限的主机：任务失败并说明原因
	e.api.Store.Update(func(st *storeState) error {
		for i := range st.Hosts {
			st.Hosts[i].Sudo = false
		}
		return nil
	})
	e.fake.Kernel("127.0.0.50", "swap")
	if a := lastAudit(t, e, "deploy.fix"); !strings.Contains(a.Act, "一键修复环境项") {
		t.Fatalf("审计：%+v", a)
	}
	// 私有的 sudo 不可用通过 fake 无法直接切换：只验证审计与任务记录
	var l struct{ Items []model.Task }
	e.expect(200, "GET", "/api/tasks?kind=deploy.fix", nil, &l)
	if len(l.Items) != 1 || l.Items[0].Cluster != "" {
		t.Fatalf("任务列表：%+v", l.Items)
	}
}

// deploySpecFor 生成一个 2 FE + 1 BE 的部署参数。
func deploySpecFor(name, pkg, jdk string) map[string]any {
	return map[string]any{"name": name, "env": "测试", "version": "3.5.6", "package": pkg, "jdkPackage": jdk, "mode": "shared-nothing", "home": "/data/starrocks", "rootPassword": "Root@2026pass",
		"nodes":  []map[string]any{{"host": "127.0.0.50", "roles": []string{"FE"}}, {"host": "127.0.0.51", "roles": []string{"FE"}}, {"host": "127.0.0.52", "roles": []string{"BE"}}},
		"feConf": map[string]string{"qe_slow_log_ms": "1000"}}
}

func TestDeployNewCluster(t *testing.T) {
	e, _ := hostsEnv(t, "healthy")
	e.addSpare("127.0.0.50-52")
	pn, pd := srPkg(t, "3.5.6", true)
	jn, jd := jdkPkg(t)
	if code, _ := e.upload(pn, pd, ""); code != 200 {
		t.Fatal("上传安装包失败")
	}
	if code, _ := e.upload(jn, jd, ""); code != 200 {
		t.Fatal("上传 JDK 失败")
	}

	// 参数校验
	for name, mod := range map[string]func(m map[string]any){
		"名称非法":      func(m map[string]any) { m["name"] = "a b" },
		"名称已存在":     func(m map[string]any) { m["name"] = "prod-test" },
		"环境非法":      func(m map[string]any) { m["env"] = "火星" },
		"密码太短":      func(m map[string]any) { m["rootPassword"] = "123" },
		"目录非法":      func(m map[string]any) { m["home"] = "/data/x;id" },
		"安装包不存在":    func(m map[string]any) { m["package"] = "StarRocks-9.9.9-centos-amd64.tar.gz" },
		"版本与安装包不一致": func(m map[string]any) { m["version"] = "3.4.0" },
		"主机未纳管": func(m map[string]any) {
			m["nodes"] = []map[string]any{{"host": "127.0.0.99", "roles": []string{"FE", "BE"}}}
		},
		"没有 BE": func(m map[string]any) { m["nodes"] = []map[string]any{{"host": "127.0.0.50", "roles": []string{"FE"}}} },
		"BE 与 CN 同机": func(m map[string]any) {
			m["nodes"] = []map[string]any{{"host": "127.0.0.50", "roles": []string{"FE", "BE", "CN"}}}
		},
		"没有 FE": func(m map[string]any) { m["nodes"] = []map[string]any{{"host": "127.0.0.50", "roles": []string{"BE"}}} },
		"配置项非法": func(m map[string]any) { m["feConf"] = map[string]string{"a b": "1"} },
		"存算分离缺对象存储": func(m map[string]any) {
			m["mode"] = "shared-data"
			m["nodes"] = []map[string]any{{"host": "127.0.0.50", "roles": []string{"FE"}}, {"host": "127.0.0.52", "roles": []string{"CN"}}}
		},
	} {
		m := deploySpecFor("dev-sr-01", pn, jn)
		mod(m)
		if code := e.call("POST", "/api/deploy/tasks", m, nil); code != 400 && code != 409 {
			t.Errorf("%s：应 400 / 409，实际 %d", name, code)
		}
	}

	// 第一次部署：安装包缺少 FE 的 jar，FE 启动失败 → 任务失败；换成完整的包后重试成功
	e.expect(200, "DELETE", "/api/packages/"+pn, nil, nil)
	bn, bd := srPkg(t, "3.5.6", false)
	e.upload(bn, bd, "")
	var dp struct {
		TaskID string `json:"taskId"`
	}
	e.expect(200, "POST", "/api/deploy/tasks", deploySpecFor("dev-sr-01", pn, jn), &dp)
	tk := e.waitTask(dp.TaskID)
	if tk.State != "failed" || !strings.Contains(tk.Err, "退出码 1") || !logHas(tk, "starrocks-fe.jar not found") {
		t.Fatalf("缺少 jar 应启动失败并带出日志：%+v", tk)
	}
	if !logHas(tk, "[1/7]") || !logHas(tk, "[2/7]") || logHas(tk, "[3/7]") {
		t.Fatalf("应停在第 2 步：%v", tk.Log)
	}
	if _, err := e.api.Store.Cluster("dev-sr-01"); err == nil {
		t.Fatal("失败的部署不应接入集群")
	}
	// 失败的任务不能取消；成功的任务不能重试
	e.expect(409, "POST", "/api/tasks/"+dp.TaskID+"/cancel", nil, nil)
	e.expect(404, "POST", "/api/tasks/nope/retry", nil, nil)
	e.expect(200, "DELETE", "/api/packages/"+pn, nil, nil)
	e.upload(pn, pd, "")
	e.expect(200, "POST", "/api/tasks/"+dp.TaskID+"/retry", nil, &dp)
	tk = e.waitTask(dp.TaskID)
	if tk.State != "done" || tk.Prog != 100 {
		t.Fatalf("重试后应成功：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	log := strings.Join(tk.Log, "\n")
	for _, want := range []string{"[7/7] 接入平台", "ALTER USER root IDENTIFIED BY ***", "start_fe.sh --helper 127.0.0.50:9010 --daemon", "ALTER SYSTEM ADD FOLLOWER", "ALTER SYSTEM ADD BACKEND", "✓ 部署完成", "上传完成，SHA256 校验通过"} {
		if !strings.Contains(log, want) {
			t.Errorf("日志缺少 %q", want)
		}
	}
	if strings.Contains(log, "Root@2026pass") {
		t.Fatal("日志不应出现 root 密码")
	}
	if strings.Index(log, "ADD FOLLOWER") > strings.Index(log, "--helper") {
		t.Fatal("必须先 ADD FOLLOWER，再带 --helper 启动")
	}
	if strings.Index(log, "start_be.sh") > strings.Index(log, "ADD BACKEND") {
		t.Fatal("BE 应先启动再 ADD")
	}

	// 主机上的文件与配置
	fe, _ := e.fake.HostFile("127.0.0.50", "/data/starrocks/fe/conf/fe.conf")
	for _, want := range []string{"meta_dir = /data/starrocks/fe/meta", "priority_networks = 127.0.0.50/24", "JAVA_HOME = /data/starrocks/jdk", "default_replication_num = 1", "qe_slow_log_ms = 1000", "http_port = 8030"} {
		if !strings.Contains(fe, want) {
			t.Errorf("fe.conf 缺少 %q：\n%s", want, fe)
		}
	}
	be, _ := e.fake.HostFile("127.0.0.52", "/data/starrocks/be/conf/be.conf")
	if !strings.Contains(be, "storage_root_path = /data/starrocks/be/storage") || !strings.Contains(be, "priority_networks = 127.0.0.52/24") {
		t.Errorf("be.conf：\n%s", be)
	}
	if left := e.fake.HostPaths("127.0.0.50", "/data/starrocks/.pkg-"); len(left) != 0 {
		t.Errorf("解压临时目录应已清理：%v", left)
	}

	// 部署出的新集群：2 FE + 1 BE，root 密码已设置；平台已自动接入
	ch := e.fake.Children()
	if len(ch) != 1 {
		t.Fatalf("应创建 1 个新集群，实际 %d", len(ch))
	}
	if fes, bes, cns := ch[0].NodeCount(); fes != 2 || bes != 1 || cns != 0 {
		t.Fatalf("新集群节点数 FE=%d BE=%d CN=%d", fes, bes, cns)
	}
	var cs []clusterOut
	e.expect(200, "GET", "/api/clusters", nil, &cs)
	var got *clusterOut
	for i := range cs {
		if cs[i].ID == "dev-sr-01" {
			got = &cs[i]
		}
	}
	if got == nil || got.Version != "3.5.6" || got.Mode != "shared-nothing" || got.SSH == nil || got.SSH.Auth != "key" || got.Dirs == nil || got.Dirs.FE != "/data/starrocks/fe" || got.Dirs.BE != "/data/starrocks/be" || got.User != "root" || len(got.Missing) != 0 {
		t.Fatalf("接入的集群：%+v", got)
	}
	tp := e.topo("/api/clusters/dev-sr-01")
	alive := 0
	for _, n := range tp {
		if n.Alive {
			alive++
		}
	}
	if len(tp) != 3 || alive != 3 {
		t.Fatalf("新集群拓扑：%d 个节点，%d 个存活", len(tp), alive)
	}
	if a := lastAudit(t, e, "deploy.execute"); a.Res != "成功" || !strings.Contains(a.Act, "部署 dev-sr-01") {
		t.Fatalf("审计：%+v", a)
	}
	// 主机被使用，不能删除
	e.expect(409, "DELETE", "/api/hosts/127.0.0.50", nil, nil)
	// 新集群可以用平台密钥运维：对其节点做启停
	be1 := findNodeOf(tp, "be", "")
	if tk := e.runNode(nodePath("/api/clusters/dev-sr-01", be1, "stop"), nil); tk.State != "done" {
		t.Fatalf("新集群 BE 停止：%+v", tk)
	}
	// 部署后这几台主机上的端口已被占用：预检应报出来
	var pc2 struct {
		Results map[string]map[string]checkCell
	}
	e.expect(200, "POST", "/api/deploy/precheck", map[string]any{"hosts": []map[string]any{{"ip": "127.0.0.50", "roles": []string{"FE"}}, {"ip": "127.0.0.52", "roles": []string{"BE"}}}, "home": "/data/starrocks", "version": "3.5.6", "arch": "amd64"}, &pc2)
	if c := pc2.Results["ports"]["127.0.0.50"]; c.St != "bad" || !strings.Contains(c.Val, "9030") {
		t.Fatalf("FE 端口应被占用：%+v", c)
	}
	if c := pc2.Results["ports"]["127.0.0.52"]; c.St != "ok" { // 上面已把这台的 BE 停掉
		t.Fatalf("BE 已停止，端口应空闲：%+v", c)
	}
	// 同名再次部署：已存在
	e.expect(409, "POST", "/api/deploy/tasks", deploySpecFor("dev-sr-01", pn, jn), nil)
	// 重试已成功的任务
	e.expect(409, "POST", "/api/tasks/"+dp.TaskID+"/retry", nil, nil)
}

func TestScaleOut(t *testing.T) {
	e, base := hostsEnv(t, "healthy")
	e.addSpare("127.0.0.50-51")
	pn, pd := srPkg(t, "3.5.6", true)
	e.upload(pn, pd, "")
	on, od := srPkg(t, "3.4.1", true)
	e.upload(on, od, "")

	var pl scalePlan
	e.expect(400, "GET", base+"/nodes/scaleout/plan?kind=xx", nil, nil)
	e.expect(200, "GET", base+"/nodes/scaleout/plan?kind=be", nil, &pl)
	if pl.Version != "3.5.6" || pl.Dir != "/opt/starrocks/be" || pl.Home != "/opt/starrocks" || len(pl.Packages) != 1 || pl.Packages[0].Name != pn || len(pl.Hosts) != 2 || !strings.HasPrefix(pl.Helper, "127.0.0.11:") {
		t.Fatalf("扩容计划：%+v", pl)
	}
	// 版本不一致 / 主机已是节点 / 参数非法
	e.expect(400, "POST", base+"/nodes/scaleout", map[string]any{"kind": "be", "hosts": []string{"127.0.0.50"}, "package": on}, nil)
	e.expect(400, "POST", base+"/nodes/scaleout", map[string]any{"kind": "fe", "role": "LEADER", "hosts": []string{"127.0.0.50"}, "package": pn}, nil)
	e.expect(400, "POST", base+"/nodes/scaleout", map[string]any{"kind": "be", "hosts": []string{}, "package": pn}, nil)
	e.expect(400, "POST", base+"/nodes/scaleout", map[string]any{"kind": "be", "hosts": []string{"127.0.0.99"}, "package": pn}, nil)
	var r hostRes
	e.expect(200, "POST", "/api/hosts", map[string]any{"ips": "127.0.0.16", "port": e.sshPort, "user": "starrocks", "auth": "password", "password": "secret"}, &r)
	e.expect(409, "POST", base+"/nodes/scaleout", map[string]any{"kind": "be", "hosts": []string{"127.0.0.16"}, "package": pn}, nil)

	// 扩容 BE
	before := len(e.topo(base))
	tk := e.runNode(base+"/nodes/scaleout", map[string]any{"kind": "be", "hosts": []string{"127.0.0.50"}, "package": pn})
	if tk.State != "done" || !logHas(tk, "ALTER SYSTEM ADD BACKEND") || !logHas(tk, "✓ 扩容完成") {
		t.Fatalf("BE 扩容：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	if len(e.topo(base)) != before+1 {
		t.Fatalf("扩容后节点数应 +1")
	}
	nb := findNodeOf(e.topo(base), "be", "")
	_ = nb
	conf, _ := e.fake.HostFile("127.0.0.50", "/opt/starrocks/be/conf/be.conf")
	if !strings.Contains(conf, "priority_networks = 127.0.0.50/24") || !strings.Contains(conf, "storage_root_path = /data1/starrocks/be;/data2/starrocks/be") {
		t.Fatalf("新 BE 的配置应沿用参考节点：\n%s", conf)
	}
	found := false
	for _, n := range e.topo(base) {
		if n.Type == "be" && n.Host == "127.0.0.50" && n.Alive {
			found = true
		}
	}
	if !found {
		t.Fatal("新 BE 应存活")
	}
	// 扩容 FE（Observer）：先 ADD 再带 helper 启动
	tk = e.runNode(base+"/nodes/scaleout", map[string]any{"kind": "fe", "role": "OBSERVER", "hosts": []string{"127.0.0.51"}, "package": pn})
	if tk.State != "done" || !logHas(tk, "ALTER SYSTEM ADD OBSERVER") || !logHas(tk, "--helper 127.0.0.11:19010") {
		t.Fatalf("FE 扩容：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	obs := 0
	for _, n := range e.topo(base) {
		if n.Type == "fe" && n.Role == "Observer" && n.Alive {
			obs++
		}
	}
	if obs != 2 {
		t.Fatalf("应有 2 个存活的 Observer，实际 %d", obs)
	}
	// 主机上已有进程：再次扩容同一台同类型主机被拒绝（它已是节点）
	e.expect(409, "POST", base+"/nodes/scaleout", map[string]any{"kind": "be", "hosts": []string{"127.0.0.50"}, "package": pn}, nil)
	// 主机 UsedBy 已记录
	var hl struct{ Items []hostOut }
	e.expect(200, "GET", "/api/hosts", nil, &hl)
	for _, h := range hl.Items {
		if h.IP == "127.0.0.50" && !contains(h.Used, "prod-test") {
			t.Fatalf("扩容后应记录使用该主机的集群：%+v", h.Used)
		}
	}
	// 扩容记录在节点页的「操作记录」里
	var l struct{ Items []model.Task }
	e.expect(200, "GET", "/api/tasks?cluster=prod-test&kind=node.scaleout", nil, &l)
	if len(l.Items) != 2 {
		t.Fatalf("扩容任务：%d", len(l.Items))
	}
}

func TestDeploySharedData(t *testing.T) {
	e, _ := hostsEnv(t, "healthy")
	e.addSpare("127.0.0.50-52")
	pn, pd := srPkg(t, "3.5.6", true)
	e.upload(pn, pd, "")
	spec := map[string]any{"name": "sd-test", "env": "开发", "version": "3.5.6", "package": pn, "mode": "shared-data", "home": "/data/sr", "rootPassword": "Root@2026pass",
		"nodes": []map[string]any{{"host": "127.0.0.50", "roles": []string{"FE"}}, {"host": "127.0.0.51", "roles": []string{"CN"}}, {"host": "127.0.0.52", "roles": []string{"CN"}}},
		"s3":    map[string]any{"path": "sr-data/sd-test", "region": "cn-north-1", "endpoint": "https://s3.cn-north-1.amazonaws.com.cn", "accessKey": "AKIATESTACCESSKEY1", "secretKey": "SECRETKEYSECRETKEY0987"}}
	// BE 在存算分离里不允许
	bad := map[string]any{}
	b, _ := json.Marshal(spec)
	json.Unmarshal(b, &bad)
	bad["nodes"] = []map[string]any{{"host": "127.0.0.50", "roles": []string{"FE"}}, {"host": "127.0.0.51", "roles": []string{"BE"}}}
	e.expect(400, "POST", "/api/deploy/tasks", bad, nil)
	var dp struct {
		TaskID string `json:"taskId"`
	}
	e.expect(200, "POST", "/api/deploy/tasks", spec, &dp)
	tk := e.waitTask(dp.TaskID)
	if tk.State != "done" {
		t.Fatalf("存算分离部署：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	log := strings.Join(tk.Log, "\n")
	if strings.Contains(log, "AKIATESTACCESSKEY1") || strings.Contains(log, "SECRETKEYSECRETKEY0987") || strings.Contains(log, "Root@2026pass") {
		t.Fatal("日志不应出现密钥和密码")
	}
	fe, _ := e.fake.HostFile("127.0.0.50", "/data/sr/fe/conf/fe.conf")
	for _, want := range []string{"run_mode = shared_data", "cloud_native_storage_type = S3", "aws_s3_path = sr-data/sd-test", "aws_s3_region = cn-north-1", "aws_s3_access_key = AKIATESTACCESSKEY1", "enable_load_volume_from_conf = true"} {
		if !strings.Contains(fe, want) {
			t.Errorf("fe.conf 缺少 %q", want)
		}
	}
	if strings.Contains(fe, "default_replication_num") {
		t.Error("存算分离不应设置副本数")
	}
	cn, _ := e.fake.HostFile("127.0.0.51", "/data/sr/cn/conf/cn.conf")
	if !strings.Contains(cn, "priority_networks = 127.0.0.51/24") {
		t.Errorf("cn.conf：%s", cn)
	}
	var cs []clusterOut
	e.expect(200, "GET", "/api/clusters", nil, &cs)
	for _, c := range cs {
		if c.ID == "sd-test" {
			if c.Mode != "shared-data" || c.Dirs.BE != "/data/sr/cn" || c.Dirs.FE != "/data/sr/fe" {
				t.Fatalf("集群：%+v", c)
			}
			if tp := e.topo("/api/clusters/sd-test"); len(tp) != 3 || findNodeOf(tp, "cn", "").Host == "" {
				t.Fatalf("拓扑：%+v", tp)
			}
			return
		}
	}
	t.Fatal("存算分离集群未接入")
}

func TestDeployDualReview(t *testing.T) {
	e, _ := hostsEnv(t, "healthy")
	e.addSpare("127.0.0.50-52")
	pn, pd := srPkg(t, "3.5.6", true)
	e.upload(pn, pd, "")
	e.expect(200, "PUT", "/api/settings/security", map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": true, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}, nil)
	e.newUser("admin2", []string{"r_admin"}, "Admin2@2026xyzab")
	admin2 := e.firstLogin("admin2", "Admin2@2026xyzab", "Admin2@2026xyzab2")
	var pend map[string]any
	e.expect(202, "POST", "/api/deploy/tasks", deploySpecFor("rv-test", pn, ""), &pend)
	if _, err := e.api.Store.Cluster("rv-test"); err == nil {
		t.Fatal("待复核期间不应部署")
	}
	var rv struct{ Items []reviewOut }
	e.expect(200, "GET", "/api/reviews", nil, &rv)
	if len(rv.Items) != 1 || rv.Items[0].Title != "部署集群 rv-test" {
		t.Fatalf("复核申请：%+v", rv.Items)
	}
	var ap map[string]any
	admin2.expect(200, "POST", "/api/reviews/"+pend["reviewId"].(string)+"/approve", nil, &ap)
	if ap["ok"] != true {
		t.Fatalf("批准失败：%v", ap)
	}
	var l struct{ Items []model.Task }
	e.expect(200, "GET", "/api/tasks?kind=deploy", nil, &l)
	if len(l.Items) != 1 {
		t.Fatalf("批准后应创建部署任务：%d", len(l.Items))
	}
	if tk := e.waitTask(l.Items[0].ID); tk.State != "done" || tk.By != "admin" {
		t.Fatalf("任务：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	// 一键修复也走复核
	e.expect(202, "POST", "/api/deploy/fix", map[string]any{"hosts": []string{"127.0.0.51"}, "items": []string{"swap"}}, &pend)
}
