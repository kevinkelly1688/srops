// @author: kevinkelly
// @license: Apache Licence
// @file: packages_import_test.go
// @time: 2026/10/28

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"starrocks-ops/fakesr"
	"starrocks-ops/internal/model"
)

type importList struct {
	Dir     string       `json:"dir"`
	Items   []importCand `json:"items"`
	Ignored []string     `json:"ignored"`
}

func (l importList) find(name, where string) (importCand, bool) {
	for _, c := range l.Items {
		if c.Name == name && c.Where == where {
			return c, true
		}
	}
	return importCand{}, false
}

// put 把文件写进目录并把修改时间调到很久以前（避免被当作“仍在下载”）。
func put(t *testing.T, dir, name string, data []byte, age time.Duration) string {
	t.Helper()
	must(t, os.MkdirAll(dir, 0o700))
	p := filepath.Join(dir, name)
	must(t, os.WriteFile(p, data, 0o644))
	old := time.Now().Add(-age)
	must(t, os.Chtimes(p, old, old))
	return p
}

func TestImportPackageFromServer(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	impDir, pkgDir := filepath.Join(e.dir, "import"), filepath.Join(e.dir, "packages")

	// 导入目录由平台创建；初始为空
	var l importList
	e.expect(200, "GET", "/api/packages/import", nil, &l)
	if l.Dir != impDir || len(l.Items) != 0 {
		t.Fatalf("初始状态：%+v", l)
	}

	name, data := srPkg(t, "4.1.0", true)
	jname, jdata := jdkPkg(t)
	put(t, impDir, name, data, time.Hour)
	put(t, impDir, jname, jdata, time.Hour)
	put(t, impDir, "notes.txt", []byte("x"), time.Hour)                                    // 名称不符：只出现在 ignored
	put(t, impDir, "StarRocks-3.5.6-centos-amd64.tar.gz", []byte("not a gzip"), time.Hour) // 名称对、内容坏
	put(t, impDir, "StarRocks-3.5.7-centos-amd64.tar.gz", data, time.Second)               // 刚写入：仍在下载
	put(t, impDir, "StarRocks-3.5.8-centos-amd64.tar.gz.part", data, time.Hour)            // 临时文件：忽略
	strayName, strayData := srPkg(t, "3.5.9", true)
	put(t, pkgDir, strayName, strayData, time.Hour) // 直接放进 packages/ 未登记（wget 下载到那里的情况）

	e.expect(200, "GET", "/api/packages/import", nil, &l)
	if c, ok := l.find(name, "import"); !ok || c.Ver != "4.1.0" || c.Arch != "amd64" || c.Kind != "starrocks" || c.Fresh || c.Size != int64(len(data)) {
		t.Fatalf("候选文件：%+v", l.Items)
	}
	if c, ok := l.find("StarRocks-3.5.7-centos-amd64.tar.gz", "import"); !ok || !c.Fresh {
		t.Fatalf("刚写入的文件应标记为仍在写入：%+v", l.Items)
	}
	if _, ok := l.find(strayName, "packages"); !ok {
		t.Fatalf("packages/ 里未登记的文件应可导入：%+v", l.Items)
	}
	if strings.Join(l.Ignored, ",") != "notes.txt" {
		t.Fatalf("不符合命名的文件：%v", l.Ignored)
	}

	// 参数校验
	for name, body := range map[string]map[string]any{
		"路径穿越":    {"name": "../packages/" + name},
		"绝对路径":    {"name": filepath.Join(impDir, name)},
		"隐藏文件":    {"name": ".hidden.tar.gz"},
		"文件名不符":   {"name": "notes.txt"},
		"where":   {"name": name, "where": "etc"},
		"sha 格式":  {"name": name, "sha256": "abc"},
		"sha 不匹配": {"name": name, "sha256": strings.Repeat("0", 64)},
		"内容损坏":    {"name": "StarRocks-3.5.6-centos-amd64.tar.gz"},
	} {
		if code := e.call("POST", "/api/packages/import", body, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	e.expect(404, "POST", "/api/packages/import", map[string]any{"name": "StarRocks-9.9.9-centos-amd64.tar.gz"}, nil)
	e.expect(409, "POST", "/api/packages/import", map[string]any{"name": "StarRocks-3.5.7-centos-amd64.tar.gz"}, nil) // 仍在下载
	if _, err := os.Stat(filepath.Join(impDir, name)); err != nil {
		t.Fatal("校验失败时不应移动文件")
	}

	// 导入：校验通过后移动到 packages/ 并登记
	var p model.Package
	e.expect(200, "POST", "/api/packages/import", map[string]any{"name": name, "sha256": strings.ToUpper(sha(data))}, &p)
	if p.Name != name || p.Ver != "4.1.0" || p.Arch != "amd64" || p.SHA != sha(data) || p.Size != int64(len(data)) || p.By != "admin" {
		t.Fatalf("登记结果：%+v", p)
	}
	if _, err := os.Stat(filepath.Join(impDir, name)); !os.IsNotExist(err) {
		t.Fatal("导入后原文件应已移走")
	}
	if b, err := os.ReadFile(filepath.Join(pkgDir, name)); err != nil || string(b) != string(data) {
		t.Fatalf("文件应在 packages/ 中：%v", err)
	}
	var pk struct{ Items []model.Package }
	e.expect(200, "GET", "/api/packages", nil, &pk)
	found := false
	for _, x := range pk.Items {
		found = found || x.Name == name
	}
	if !found {
		t.Fatalf("安装包列表里应有 %s", name)
	}
	if a := lastAudit(t, e, "deploy.pkg"); !strings.Contains(a.Act, "从服务器导入安装包 "+name) || a.ResClass != "成功" {
		t.Fatalf("审计：%+v", a)
	}

	// 重复导入同名文件
	put(t, impDir, name, data, time.Hour)
	e.expect(409, "POST", "/api/packages/import", map[string]any{"name": name}, nil)

	// JDK 包同样可以导入；packages/ 里未登记的文件原地登记（文件不移动）
	e.expect(200, "POST", "/api/packages/import", map[string]any{"name": jname}, &p)
	if p.Kind != "jdk" || p.Ver != "17" {
		t.Fatalf("JDK：%+v", p)
	}
	e.expect(200, "POST", "/api/packages/import", map[string]any{"name": strayName, "where": "packages"}, &p)
	if _, err := os.Stat(filepath.Join(pkgDir, strayName)); err != nil || p.Ver != "3.5.9" {
		t.Fatalf("原地登记：%v %+v", err, p)
	}
	e.expect(200, "GET", "/api/packages/import", nil, &l)
	if _, ok := l.find(strayName, "packages"); ok {
		t.Fatal("已登记的文件不应再出现在候选里")
	}

	// 删除后文件随之删除（和上传的包一致）
	e.expect(200, "DELETE", "/api/packages/"+strayName, nil, nil)
	if _, err := os.Stat(filepath.Join(pkgDir, strayName)); !os.IsNotExist(err) {
		t.Fatal("删除安装包应同时删除文件")
	}

	// 权限：没有 deploy.pkg 的用户既不能列出也不能导入
	e.newUser("oncall9", []string{"r_oncall"}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall9", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	oc.expect(403, "GET", "/api/packages/import", nil, nil)
	oc.expect(403, "POST", "/api/packages/import", map[string]any{"name": name}, nil)
}

func TestImportRejectsSymlinkAndDir(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	impDir := filepath.Join(e.dir, "import")
	name, data := srPkg(t, "4.1.1", true)
	outside := put(t, e.dir, "outside.bin", data, time.Hour)
	must(t, os.MkdirAll(impDir, 0o700))
	if err := os.Symlink(outside, filepath.Join(impDir, name)); err != nil {
		t.Skip("当前系统不能创建符号链接：", err)
	}
	var l importList
	e.expect(200, "GET", "/api/packages/import", nil, &l)
	if len(l.Items) != 0 {
		t.Fatalf("符号链接不应出现在候选里：%+v", l.Items)
	}
	e.expect(400, "POST", "/api/packages/import", map[string]any{"name": name}, nil)
	// 目录同名
	must(t, os.MkdirAll(filepath.Join(impDir, "StarRocks-4.1.2-centos-amd64.tar.gz"), 0o700))
	e.expect(400, "POST", "/api/packages/import", map[string]any{"name": "StarRocks-4.1.2-centos-amd64.tar.gz"}, nil)
}
