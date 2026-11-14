// @author: kevinkelly
// @license: Apache Licence
// @file: perm.go
// @time: 2026/10/28

// Package perm 定义平台的权限模型：菜单「查看」权限（<页面>:view）与按钮权限（<模块>.<动作>）。
// 权限树由 tools/build_help.py 从前端 js/perm.js 生成（tree_gen.go），前后端共用同一份定义。
package perm

import (
	"fmt"
	"sort"
	"strings"
)

type Btn struct {
	Code, Name string
	High       bool // 高危操作
}

type Menu struct {
	ID, Name string
	Public   bool // 所有登录用户可见（如使用手册）
	Btns     []Btn
}

type Group struct {
	Name  string
	Menus []Menu
}

const (
	// AdminRole 是内置超级管理员角色：固定拥有全部权限与全部集群，不可修改。
	AdminRole = "r_admin"
	// AllClusters 在角色的集群范围中表示“全部集群（含以后新接入的）”。
	AllClusters = "*"
)

var (
	all    []string
	valid  = map[string]bool{}
	viewOf = map[string]string{} // 按钮码 → 所属菜单的查看码
	names  = map[string]string{}
	menuOf = map[string]string{
		"auth.login": "登录", "review.submit": "操作审计", "review.approve": "操作审计", "review.reject": "操作审计", "review.withdraw": "操作审计",
		"setting.watermark": "平台设置",
	}
)

func init() {
	for _, g := range Tree {
		for _, m := range g.Menus {
			v := m.ID + ":view"
			menuOf[v] = m.Name
			all = append(all, v)
			valid[v] = true
			names[v] = m.Name + "·查看"
			for _, b := range m.Btns {
				all = append(all, b.Code)
				valid[b.Code] = true
				viewOf[b.Code] = v
				menuOf[b.Code] = m.Name
				names[b.Code] = b.Name
			}
		}
	}
}

// All 返回全部权限码（拷贝）。
func All() []string { return append([]string(nil), all...) }

// Valid 判断权限码是否存在。
func Valid(code string) bool { return valid[code] }

// MenuOf 返回权限码所属菜单的名称（审计日志的“菜单”列）；平台内部操作码（登录、复核）也有对应名称，未知返回“—”。
func MenuOf(code string) string {
	if n, ok := menuOf[code]; ok {
		return n
	}
	return "—"
}

// Name 返回权限的中文名称（未知时返回码本身）。
func Name(code string) string {
	if n, ok := names[code]; ok {
		return n
	}
	return code
}

// Check 校验角色的权限列表：权限码必须存在、不重复，且每个按钮权限必须同时拥有所属菜单的「查看」。
func Check(perms []string) error {
	seen := map[string]bool{}
	for _, p := range perms {
		if !valid[p] {
			return fmt.Errorf("未知的权限码：%s", p)
		}
		seen[p] = true
	}
	for p := range seen {
		if v, ok := viewOf[p]; ok && !seen[v] {
			return fmt.Errorf("按钮权限「%s」(%s) 需要同时拥有「%s」", Name(p), p, Name(v))
		}
	}
	return nil
}

// Dedup 去重并排序。
func Dedup(perms []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range perms {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Role 是权限计算所需的角色信息（避免依赖 model 包）。
type Role struct {
	ID       string
	Perms    []string
	Clusters []string
}

// Access 是一个用户在平台上的有效权限：所有角色的并集。
type Access struct {
	Admin       bool
	perms       map[string]bool
	allClusters bool
	clusters    map[string]bool
}

// Resolve 合并用户全部角色。未知角色（已删除）被忽略。
func Resolve(roleIDs []string, roles []Role) *Access {
	a := &Access{perms: map[string]bool{}, clusters: map[string]bool{}}
	by := map[string]Role{}
	for _, r := range roles {
		by[r.ID] = r
	}
	for _, id := range roleIDs {
		if id == AdminRole {
			a.Admin, a.allClusters = true, true
			continue
		}
		r, ok := by[id]
		if !ok {
			continue
		}
		for _, p := range r.Perms {
			a.perms[p] = true
		}
		for _, c := range r.Clusters {
			if c == AllClusters {
				a.allClusters = true
			} else {
				a.clusters[c] = true
			}
		}
	}
	return a
}

// Can 判断是否拥有权限码；code 为空或 help:view（公开菜单）总是允许；用 | 分隔表示任一。
func (a *Access) Can(code string) bool {
	if code == "" {
		return true
	}
	for _, c := range strings.Split(code, "|") {
		if a.Admin || a.perms[c] || publicView(c) {
			return true
		}
	}
	return false
}

func publicView(code string) bool {
	for _, g := range Tree {
		for _, m := range g.Menus {
			if m.Public && m.ID+":view" == code {
				return true
			}
		}
	}
	return false
}

// AllClusters 是否可访问全部集群。
func (a *Access) AllClusters() bool { return a.allClusters }

// Cluster 是否可访问指定集群。
func (a *Access) Cluster(id string) bool { return a.allClusters || a.clusters[id] }

// Perms 返回有效权限码（排序）。
func (a *Access) Perms() []string {
	if a.Admin {
		return Dedup(All())
	}
	out := make([]string, 0, len(a.perms))
	for p := range a.perms {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Clusters 返回显式授权的集群（不含“全部”）。
func (a *Access) Clusters() []string {
	out := make([]string, 0, len(a.clusters))
	for c := range a.clusters {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// Covers 判断该角色的全部权限与集群范围都不超出本访问权限，即当前用户有资格管理它（防止越权提升）。
func (a *Access) Covers(r Role) bool {
	if a.Admin {
		return true
	}
	for _, p := range r.Perms {
		if !a.perms[p] {
			return false
		}
	}
	return a.CoversClusters(r.Clusters)
}

// CoversClusters 判断集群范围是否不超出本访问权限。
func (a *Access) CoversClusters(cs []string) bool {
	if a.allClusters {
		return true
	}
	for _, c := range cs {
		if c == AllClusters || !a.clusters[c] {
			return false
		}
	}
	return true
}

// CoversPerms 判断权限码是否都在本访问权限内。
func (a *Access) CoversPerms(ps []string) bool {
	if a.Admin {
		return true
	}
	for _, p := range ps {
		if !a.perms[p] {
			return false
		}
	}
	return true
}

// Missing 返回 ps 中本访问权限没有的权限码。
func (a *Access) Missing(ps []string) []string {
	var out []string
	for _, p := range ps {
		if !a.Admin && !a.perms[p] {
			out = append(out, p)
		}
	}
	return out
}
