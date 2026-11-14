// @author: kevinkelly
// @license: Apache Licence
// @file: rbac.go
// @time: 2026/10/28

package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/perm"
	"starrocks-ops/internal/store"
)

// apiErr 是带 HTTP 状态码的业务错误，在 Store.Update 的闭包里返回，由 writeErr 统一输出。
type apiErr struct {
	code int
	msg  string
}

func (e apiErr) Error() string { return e.msg }

func bad(msg string) error       { return apiErr{http.StatusBadRequest, msg} }
func forbidden(msg string) error { return apiErr{http.StatusForbidden, msg} }
func missing(msg string) error   { return apiErr{http.StatusNotFound, msg} }
func conflict(msg string) error  { return apiErr{http.StatusConflict, msg} }

func writeErr(w http.ResponseWriter, err error) {
	if e, ok := err.(apiErr); ok {
		fail(w, e.code, e.msg)
		return
	}
	fail(w, http.StatusInternalServerError, err.Error())
}

const adminRoleName = "超级管理员"
const adminRoleDesc = "平台全部功能与全部集群，含用户与权限管理，不可修改。"

// adminRole 是内置的虚拟角色：不存储，始终拥有全部权限与全部集群。
func adminRole() model.Role {
	return model.Role{ID: perm.AdminRole, Name: adminRoleName, Desc: adminRoleDesc, Builtin: true, Clusters: []string{perm.AllClusters}, Perms: perm.All()}
}

func permRoles(st *store.State) []perm.Role {
	out := make([]perm.Role, 0, len(st.Roles))
	for _, r := range st.Roles {
		out = append(out, perm.Role{ID: r.ID, Perms: r.Perms, Clusters: r.Clusters})
	}
	return out
}

// accessIn 计算一组角色的有效权限（在持有 Store 锁时调用）。
func accessIn(st *store.State, roleIDs []string) *perm.Access {
	return perm.Resolve(roleIDs, permRoles(st))
}

func findRole(st *store.State, id string) (model.Role, bool) {
	if id == perm.AdminRole {
		return adminRole(), true
	}
	for _, r := range st.Roles {
		if r.ID == id {
			return r, true
		}
	}
	return model.Role{}, false
}

// seedRoles 首次启动时写入预置角色（运维管理员、DBA、值班只读、审计员）。
func (s *Server) seedRoles() error {
	return s.Store.Update(func(st *store.State) error {
		if len(st.Roles) > 0 {
			return nil
		}
		for _, p := range perm.Presets() {
			st.Roles = append(st.Roles, model.Role{ID: p.ID, Name: p.Name, Desc: p.Desc, Builtin: p.Builtin, Clusters: p.Clusters, Perms: p.Perms, CreatedAt: time.Now()})
		}
		return nil
	})
}

func newRoleID(st *store.State) string {
	for {
		b := make([]byte, 4)
		rand.Read(b)
		id := "r_" + hex.EncodeToString(b)
		if _, ok := findRole(st, id); !ok {
			return id
		}
	}
}

// userView 是用户自己的视图：含有效权限、数据范围与所属角色详情，前端据此渲染菜单与按钮。
func (s *Server) userView(u *model.User) map[string]any {
	var acc *perm.Access
	var roles []model.Role
	s.Store.View(func(st *store.State) {
		acc = accessIn(st, u.Roles)
		for _, id := range u.Roles {
			if r, ok := findRole(st, id); ok {
				roles = append(roles, r)
			}
		}
	})
	cl := acc.Clusters()
	if acc.AllClusters() {
		cl = []string{perm.AllClusters}
	}
	rl := make([]roleOut, 0, len(roles))
	for _, r := range roles {
		rl = append(rl, roleView(r, 0))
	}
	return map[string]any{"login": u.Login, "name": u.Name, "email": u.Email, "phone": u.Phone, "dept": u.Dept, "roles": u.Roles, "pwdAt": u.PwdAt,
		"mustChange": u.MustChange, "lastLogin": u.LastLogin, "expireAt": u.ExpireAt, "perms": acc.Perms(), "clusters": cl, "roleList": rl}
}

type roleOut struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Desc     string   `json:"desc"`
	Builtin  bool     `json:"builtin"`
	Clusters []string `json:"clusters"`
	Perms    []string `json:"perms"`
	Members  int      `json:"members"`
}

func roleView(r model.Role, members int) roleOut {
	o := roleOut{ID: r.ID, Name: r.Name, Desc: r.Desc, Builtin: r.Builtin, Clusters: r.Clusters, Perms: r.Perms, Members: members}
	if o.Clusters == nil {
		o.Clusters = []string{}
	}
	if o.Perms == nil {
		o.Perms = []string{}
	}
	return o
}

// ---------- 输入校验 ----------

func rlen(s string) int { return utf8.RuneCountInString(s) }

func cleanText(s string) string { return strings.TrimSpace(s) }

// countAdmins 统计仍可登录的超级管理员数量（未锁定、未过期）。
func countAdmins(users []model.User, now time.Time) int {
	n := 0
	for _, u := range users {
		if u.Locked || u.Expired(now) {
			continue
		}
		for _, r := range u.Roles {
			if r == perm.AdminRole {
				n++
				break
			}
		}
	}
	return n
}

func hasRole(u model.User, id string) bool {
	for _, r := range u.Roles {
		if r == id {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, x := range b {
		if !m[x] {
			return false
		}
	}
	return true
}

// manageable 判断 actor 是否有资格管理 target：target 的每个角色都不能超出 actor 自己的权限与数据范围。
func manageable(st *store.State, acc *perm.Access, target model.User) bool {
	for _, id := range target.Roles {
		r, ok := findRole(st, id)
		if !ok {
			continue
		}
		if !acc.Covers(perm.Role{ID: r.ID, Perms: r.Perms, Clusters: r.Clusters}) {
			return false
		}
	}
	return true
}
