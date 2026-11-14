// @author: kevinkelly
// @license: Apache Licence
// @file: fixdir_test.go
// @time: 2026/10/28

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCheckFixDir(t *testing.T) {
	for dir, ok := range map[string]bool{
		"/data/starrocks":       true,
		"/opt/starrocks":        true,
		"/data1/sr/be":          true,
		"/home/starrocks/sr":    true,
		"/mnt/disk1":            true,
		"/data":                 false, // 层级太浅
		"/":                     false,
		"/usr/local/starrocks":  false, // 系统目录
		"/etc/sr":               false,
		"/var/lib/sr":           false,
		"/root/sr":              false,
		"/home/bob":             false, // 别人的家目录
		"/data/../etc":          false,
		"/data/./sr":            false,
		"/data//sr":             false,
		"/data/sr/":             false,
		"/data; rm -rf /":       false,
		"data/starrocks":        false,
		"":                      false,
		"/data/starrocks/$(id)": false,
	} {
		if err := checkFixDir(dir); (err == nil) != ok {
			t.Errorf("checkFixDir(%q) = %v，期望可用=%v", dir, err, ok)
		}
	}
}

// 「安装目录可写」可一键修复：创建目录并把属主交给 SSH 用户；已有的 root 目录接管；别的用户的目录不动。
func TestFixInstallDir(t *testing.T) {
	e, _ := hostsEnv(t, "healthy")
	e.addSpare("127.0.0.50-52")
	jn, jd := jdkPkg(t)
	e.upload(jn, jd, "")

	// .50：/data 是 root 的，starrocks 不能在里面建目录；.51：/data/starrocks 已由 root 建好；.52：/data/starrocks 属于别的服务账号
	e.fake.HostSetOwner("127.0.0.50", "/data", "root")
	e.fake.HostSetOwner("127.0.0.51", "/data/starrocks", "root")
	e.fake.HostSetOwner("127.0.0.52", "/data/starrocks", "mysql")

	precheck := func(home string, ips ...string) (map[string]map[string]checkCell, []checkItem) {
		var hs []map[string]any
		for _, ip := range ips {
			hs = append(hs, map[string]any{"ip": ip, "roles": []string{"FE"}})
		}
		var pc struct {
			Items   []checkItem
			Results map[string]map[string]checkCell
		}
		e.expect(200, "POST", "/api/deploy/precheck", map[string]any{"hosts": hs, "home": home, "version": "3.5.6", "arch": "amd64"}, &pc)
		return pc.Results, pc.Items
	}
	dirItem := func(items []checkItem) checkItem {
		for _, it := range items {
			if it.ID == "dir" {
				return it
			}
		}
		t.Fatal("没有「安装目录可写」检查项")
		return checkItem{}
	}

	res, items := precheck("/data/starrocks", "127.0.0.50", "127.0.0.51", "127.0.0.52")
	if res["dir"]["127.0.0.50"].St != "bad" || res["dir"]["127.0.0.51"].St != "bad" || res["dir"]["127.0.0.52"].St != "ok" {
		t.Fatalf("修复前预检：%+v", res["dir"])
	}
	if it := dirItem(items); !it.Fixable || !strings.Contains(it.Fix, "/data/starrocks") {
		t.Fatalf("合法目录应可一键修复：%+v", it)
	}
	// 目录太浅 / 在系统目录下：不提供修复
	for _, home := range []string{"/data", "/usr/local/sr"} {
		if _, items := precheck(home, "127.0.0.50"); dirItem(items).Fixable {
			t.Errorf("%s 不应提供一键修复", home)
		}
	}

	// 参数校验
	for name, body := range map[string]map[string]any{
		"目录太浅":  {"hosts": []string{"127.0.0.50"}, "items": []string{"dir"}, "home": "/data"},
		"系统目录":  {"hosts": []string{"127.0.0.50"}, "items": []string{"dir"}, "home": "/usr/local/starrocks"},
		"别人家目录": {"hosts": []string{"127.0.0.50"}, "items": []string{"dir"}, "home": "/home/bob"},
		"目录穿越":  {"hosts": []string{"127.0.0.50"}, "items": []string{"dir"}, "home": "/data/../etc"},
		"目录注入":  {"hosts": []string{"127.0.0.50"}, "items": []string{"dir"}, "home": "/data/sr; id"},
	} {
		if code := e.call("POST", "/api/deploy/fix", body, nil); code != 400 {
			t.Errorf("%s：应 400，实际 %d", name, code)
		}
	}
	if e.fake.HostHasDir("127.0.0.50", "/data/starrocks") {
		t.Fatal("校验失败的请求不应创建目录")
	}

	// 修复 .50（创建）与 .51（接管）；同时修复 JDK：JDK 要写在刚授权的目录下，所以目录必须先处理
	var fx struct {
		TaskID string `json:"taskId"`
	}
	e.expect(200, "POST", "/api/deploy/fix", map[string]any{"hosts": []string{"127.0.0.50", "127.0.0.51"}, "items": []string{"java", "dir"}, "home": "/data/starrocks/", "javaHome": "/data/starrocks/jdk", "jdkPackage": jn}, &fx)
	tk := e.waitTask(fx.TaskID)
	if tk.State != "done" || !logHas(tk, "mkdir -p /data/starrocks") || !logHas(tk, "chown starrocks: /data/starrocks") {
		t.Fatalf("修复任务：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	for _, ip := range []string{"127.0.0.50", "127.0.0.51"} {
		if got := e.fake.HostOwner(ip, "/data/starrocks"); got != "starrocks" {
			t.Errorf("%s 修复后 /data/starrocks 属主 = %q", ip, got)
		}
		if _, ok := e.fake.HostFile(ip, "/data/starrocks/jdk/bin/java"); !ok {
			t.Errorf("%s 应已在安装目录下装好 JDK", ip)
		}
	}
	if got := e.fake.HostOwner("127.0.0.50", "/data"); got != "root" {
		t.Errorf("只能改安装目录本身，/data 属主不应变：%q", got)
	}
	res, _ = precheck("/data/starrocks", "127.0.0.50", "127.0.0.51")
	if res["dir"]["127.0.0.50"].St != "ok" || res["dir"]["127.0.0.51"].St != "ok" {
		t.Errorf("修复后复查：%+v", res["dir"])
	}

	// .52：目录属于别的服务账号，不抢占，任务失败并说明
	e.expect(200, "POST", "/api/deploy/fix", map[string]any{"hosts": []string{"127.0.0.52"}, "items": []string{"dir"}, "home": "/data/starrocks"}, &fx)
	tk = e.waitTask(fx.TaskID)
	if tk.State != "failed" || !logHas(tk, "属于用户 mysql") {
		t.Fatalf("别人的目录应拒绝修改：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	if got := e.fake.HostOwner("127.0.0.52", "/data/starrocks"); got != "mysql" {
		t.Errorf("别人的目录属主被改了：%q", got)
	}
}

// errOf 发一个请求，返回状态码与错误提示。
func (e *env) errOf(method, path string, body any) (int, string) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "sr-ops")
	for k, v := range e.hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.c.Do(req)
	must(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var er struct{ Error string }
	json.Unmarshal(raw, &er)
	return resp.StatusCode, er.Error
}

// 页面脚本没有随请求带上部署目录时（例如升级后浏览器仍在用旧版脚本）：沿用最近一次预检的安装目录；
// 没有预检过则给出明确提示，而不是「部署目录 "" 不合法」。
func TestFixInstallDirWithoutHome(t *testing.T) {
	e, _ := hostsEnv(t, "healthy")
	e.addSpare("127.0.0.50")
	e.fake.HostSetOwner("127.0.0.50", "/data", "root")
	fix := map[string]any{"hosts": []string{"127.0.0.50"}, "items": []string{"dir"}}
	precheck := func(home string) {
		e.expect(200, "POST", "/api/deploy/precheck", map[string]any{"hosts": []map[string]any{{"ip": "127.0.0.50", "roles": []string{"FE"}}}, "home": home, "version": "3.5.6", "arch": "amd64"}, nil)
	}

	// 还没预检过
	if code, msg := e.errOf("POST", "/api/deploy/fix", fix); code != 400 || !strings.Contains(msg, "没有收到部署目录") || strings.Contains(msg, "不合法") {
		t.Fatalf("没预检过应给出明确提示：%d %q", code, msg)
	}
	// 沿用最近一次预检的目录，规则照常生效
	precheck("/data")
	if code, msg := e.errOf("POST", "/api/deploy/fix", fix); code != 400 || !strings.Contains(msg, "层级太浅") {
		t.Fatalf("应沿用预检的 /data 并按规则拒绝：%d %q", code, msg)
	}
	if e.fake.HostHasDir("127.0.0.50", "/data/starrocks") {
		t.Fatal("被拒绝的请求不应创建目录")
	}
	// 预检 /data/starrocks 后，没带目录的修复请求也能完成
	precheck("/data/starrocks/")
	var fx struct {
		TaskID string `json:"taskId"`
	}
	e.expect(200, "POST", "/api/deploy/fix", fix, &fx)
	if tk := e.waitTask(fx.TaskID); tk.State != "done" {
		t.Fatalf("修复任务：%+v\n%s", tk, strings.Join(tk.Log, "\n"))
	}
	if got := e.fake.HostOwner("127.0.0.50", "/data/starrocks"); got != "starrocks" {
		t.Errorf("修复后属主 = %q", got)
	}
	// 请求里带了目录时以请求为准
	e.fake.HostSetOwner("127.0.0.50", "/opt", "root")
	fix["home"] = "/opt/sr"
	e.expect(200, "POST", "/api/deploy/fix", fix, &fx)
	if tk := e.waitTask(fx.TaskID); tk.State != "done" || e.fake.HostOwner("127.0.0.50", "/opt/sr") != "starrocks" {
		t.Fatalf("带目录的请求：%+v", tk)
	}
}
