// @author: kevinkelly
// @license: Apache Licence
// @file: perm_test.go
// @time: 2026/10/28

package perm

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// 服务端权限树必须与前端 js/perm.js 一致（修改 perm.js 后运行 python tools/build_help.py 重新生成）。
func TestTreeMatchesFrontend(t *testing.T) {
	b, err := os.ReadFile("../../../js/perm.js")
	if err != nil {
		t.Skip("找不到前端 perm.js：", err)
	}
	src := string(b)
	src = src[:regexp.MustCompile(`/\* 扁平化索引`).FindStringIndex(src)[0]]
	want := map[string]bool{}
	for _, m := range regexp.MustCompile(`\{id: '(\w+)', ico: '[^']*', name: '[^']+', btns: \[`).FindAllStringSubmatch(src, -1) {
		want[m[1]+":view"] = true
	}
	for _, m := range regexp.MustCompile(`\['([a-z]+\.[a-z]+)', '[^']+'(?:, 'high')?\]`).FindAllStringSubmatch(src, -1) {
		want[m[1]] = true
	}
	var got, missing, extra []string
	for _, c := range All() {
		got = append(got, c)
		if !want[c] {
			extra = append(extra, c)
		}
	}
	have := map[string]bool{}
	for _, c := range got {
		have[c] = true
	}
	for c := range want {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 || len(extra) > 0 || len(got) != 93 {
		t.Fatalf("权限树与 perm.js 不一致：服务端 %d 项；缺少 %v；多出 %v（运行 python tools/build_help.py）", len(got), missing, extra)
	}
}

func TestPresetsValid(t *testing.T) {
	for _, p := range Presets() {
		if err := Check(p.Perms); err != nil {
			t.Errorf("预置角色 %s：%v", p.ID, err)
		}
	}
}

func TestCheck(t *testing.T) {
	if Check([]string{"node.stop"}) == nil {
		t.Error("只有按钮权限、没有菜单查看，应拒绝")
	}
	if Check([]string{"nodes:view", "node.stop"}) != nil {
		t.Error("合法组合被拒绝")
	}
	if Check([]string{"nodes:view", "node.fly"}) == nil {
		t.Error("未知权限码应拒绝")
	}
}

func TestAccess(t *testing.T) {
	roles := []Role{
		{ID: "r_dba", Perms: []string{"query:view", "query.kill"}, Clusters: []string{"c1"}},
		{ID: "r_oncall", Perms: []string{"query:view"}, Clusters: []string{"*"}},
		{ID: "r_none", Perms: nil, Clusters: nil},
	}
	a := Resolve([]string{"r_dba"}, roles)
	if !a.Can("query.kill") || a.Can("query.profile") || !a.Can("help:view") || !a.Can("") || !a.Can("x|query:view") || a.Can("x|y") {
		t.Fatal("权限判断不对")
	}
	if !a.Cluster("c1") || a.Cluster("c2") || a.AllClusters() {
		t.Fatal("集群范围不对")
	}
	u := Resolve([]string{"r_dba", "r_oncall", "r_gone"}, roles)
	if !u.AllClusters() || !u.Cluster("zzz") || !u.Can("query.kill") {
		t.Fatal("多角色应取并集，已删除的角色应忽略")
	}
	if !Resolve([]string{AdminRole}, roles).Can("node.drop") {
		t.Fatal("超级管理员应拥有全部权限")
	}
	if !a.Covers(Role{Perms: []string{"query:view"}, Clusters: []string{"c1"}}) || a.Covers(Role{Perms: []string{"query:view"}, Clusters: []string{"c2"}}) ||
		a.Covers(Role{Perms: []string{"query.profile"}}) || a.Covers(Role{Perms: nil, Clusters: []string{"*"}}) {
		t.Fatal("Covers 判断不对")
	}
	if !Resolve([]string{AdminRole}, nil).Covers(Role{Perms: All(), Clusters: []string{"*"}}) {
		t.Fatal("超级管理员覆盖一切")
	}
}
