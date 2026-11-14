// @author: kevinkelly
// @license: Apache Licence
// @file: roles.go
// @time: 2026/10/28

package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/perm"
	"starrocks-ops/internal/store"
)

// listRoles 返回全部角色（超级管理员在最前）及成员数。
func (s *Server) listRoles(w http.ResponseWriter, r *http.Request, u *model.User) {
	out := []roleOut{}
	s.Store.View(func(st *store.State) {
		members := map[string]int{}
		for _, x := range st.Users {
			for _, id := range x.Roles {
				members[id]++
			}
		}
		for _, role := range append([]model.Role{adminRole()}, st.Roles...) {
			out = append(out, roleView(role, members[role.ID]))
		}
	})
	ok(w, out)
}

type roleIn struct {
	Name     *string   `json:"name"`
	Desc     *string   `json:"desc"`
	Perms    *[]string `json:"perms"`
	Clusters *[]string `json:"clusters"`
}

// checkScope 校验并规范化数据范围：集群必须存在；包含 * 时只保留 *。
func checkScope(st *store.State, cs []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, c := range cs {
		if c == perm.AllClusters {
			return []string{perm.AllClusters}, nil
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		found := false
		for _, x := range st.Clusters {
			found = found || x.ID == c
		}
		if !found {
			return nil, bad("集群不存在：" + c)
		}
		out = append(out, c)
	}
	return out, nil
}

func checkRoleName(st *store.State, name, self string) error {
	if name == "" || rlen(name) > 30 {
		return bad("角色名称必填，最多 30 个字")
	}
	if name == adminRoleName {
		return conflict("角色名称已存在")
	}
	for _, x := range st.Roles {
		if x.Name == name && x.ID != self {
			return conflict("角色名称已存在")
		}
	}
	return nil
}

// escalation 检查 actor 是否可以授予这些权限与数据范围（不能超出自己拥有的）。
func escalation(acc *perm.Access, perms, clusters []string) error {
	if m := acc.Missing(perms); len(m) > 0 {
		names := make([]string, 0, 3)
		for i, c := range m {
			if i == 3 {
				names = append(names, fmt.Sprintf("等 %d 项", len(m)))
				break
			}
			names = append(names, perm.Name(c))
		}
		return forbidden("不能授予自己没有的权限：" + strings.Join(names, "、"))
	}
	if !acc.CoversClusters(clusters) {
		return forbidden("不能授予超出自己数据范围的集群")
	}
	return nil
}

func (s *Server) createRole(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in roleIn
	if !decode(w, r, &in) {
		return
	}
	var out roleOut
	name := ""
	if in.Name != nil {
		name = cleanText(*in.Name)
	}
	err := s.Store.Update(func(st *store.State) error {
		_, acc, err := actorAccess(st, u.Login)
		if err != nil {
			return err
		}
		if err := checkRoleName(st, name, ""); err != nil {
			return err
		}
		desc := ""
		if in.Desc != nil {
			desc = cleanText(*in.Desc)
		}
		if rlen(desc) > 200 {
			return bad("描述最多 200 个字")
		}
		if desc == "" {
			desc = "自定义角色"
		}
		perms := []string{"overview:view"}
		if in.Perms != nil {
			perms = perm.Dedup(*in.Perms)
		}
		if err := perm.Check(perms); err != nil {
			return bad(err.Error())
		}
		clusters := []string{}
		if in.Clusters != nil {
			if clusters, err = checkScope(st, *in.Clusters); err != nil {
				return err
			}
		}
		if err := escalation(acc, perms, clusters); err != nil {
			return err
		}
		role := model.Role{ID: newRoleID(st), Name: name, Desc: desc, Clusters: clusters, Perms: perms, CreatedAt: time.Now()}
		st.Roles = append(st.Roles, role)
		out = roleView(role, 0)
		return nil
	})
	s.audit(r, u, "", "role.create", fmt.Sprintf("新建角色 %s", name), err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, out)
}

func (s *Server) updateRole(w http.ResponseWriter, r *http.Request, u *model.User) {
	id := r.PathValue("role")
	var in roleIn
	if !decode(w, r, &in) {
		return
	}
	var out roleOut
	var act, sql string
	err := s.Store.Update(func(st *store.State) error {
		if id == perm.AdminRole {
			return forbidden("超级管理员固定拥有全部权限，不可修改")
		}
		_, acc, err := actorAccess(st, u.Login)
		if err != nil {
			return err
		}
		k := -1
		for i := range st.Roles {
			if st.Roles[i].ID == id {
				k = i
			}
		}
		if k < 0 {
			return missing("角色不存在")
		}
		old := st.Roles[k]
		if !acc.Covers(perm.Role{ID: old.ID, Perms: old.Perms, Clusters: old.Clusters}) {
			return forbidden("不能修改权限高于自己的角色")
		}
		nr := old
		nr.Perms = append([]string(nil), old.Perms...)
		nr.Clusters = append([]string(nil), old.Clusters...)
		if in.Name != nil || in.Desc != nil {
			if old.Builtin {
				return forbidden("内置角色不能改名或修改描述")
			}
			if in.Name != nil {
				nr.Name = cleanText(*in.Name)
				if err := checkRoleName(st, nr.Name, id); err != nil {
					return err
				}
			}
			if in.Desc != nil {
				nr.Desc = cleanText(*in.Desc)
				if rlen(nr.Desc) > 200 {
					return bad("描述最多 200 个字")
				}
			}
		}
		if in.Perms != nil {
			nr.Perms = perm.Dedup(*in.Perms)
			if err := perm.Check(nr.Perms); err != nil {
				return bad(err.Error())
			}
		}
		if in.Clusters != nil {
			if nr.Clusters, err = checkScope(st, *in.Clusters); err != nil {
				return err
			}
		}
		if err := escalation(acc, nr.Perms, nr.Clusters); err != nil {
			return err
		}
		add, del := diff(old.Perms, nr.Perms)
		act = fmt.Sprintf("修改角色 %s：权限 +%d −%d", old.Name, len(add), len(del))
		if !sameSet(old.Clusters, nr.Clusters) {
			act += "；数据范围 " + scopeText(old.Clusters) + " → " + scopeText(nr.Clusters)
		}
		if nr.Name != old.Name {
			act += "；改名 " + old.Name + " → " + nr.Name
		}
		for _, c := range add {
			sql += "+ " + c + "\n"
		}
		for _, c := range del {
			sql += "- " + c + "\n"
		}
		st.Roles[k] = nr
		members := 0
		for _, x := range st.Users {
			if hasRole(x, id) {
				members++
			}
		}
		out = roleView(nr, members)
		return nil
	})
	if act == "" {
		act = "修改角色 " + id
	}
	s.auditSQL(r, u, "role.edit", act, strings.TrimSpace(sql), err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, out)
}

func scopeText(cs []string) string {
	if containsStr(cs, perm.AllClusters) {
		return "全部集群"
	}
	if len(cs) == 0 {
		return "无"
	}
	return strings.Join(cs, "、")
}

// diff 返回 b 相对 a 新增与移除的权限码。
func diff(a, b []string) (add, del []string) {
	ma, mb := map[string]bool{}, map[string]bool{}
	for _, x := range a {
		ma[x] = true
	}
	for _, x := range b {
		mb[x] = true
		if !ma[x] {
			add = append(add, x)
		}
	}
	for _, x := range a {
		if !mb[x] {
			del = append(del, x)
		}
	}
	return
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request, u *model.User) {
	id := r.PathValue("role")
	name := id
	err := s.Store.Update(func(st *store.State) error {
		if id == perm.AdminRole {
			return forbidden("超级管理员为内置角色，不可删除")
		}
		k := -1
		for i := range st.Roles {
			if st.Roles[i].ID == id {
				k = i
			}
		}
		if k < 0 {
			return missing("角色不存在")
		}
		name = st.Roles[k].Name
		if st.Roles[k].Builtin {
			return forbidden("内置角色不可删除")
		}
		n := 0
		for _, x := range st.Users {
			if hasRole(x, id) {
				n++
			}
		}
		if n > 0 {
			return conflict(fmt.Sprintf("角色「%s」仍有 %d 名成员，请先在用户管理中移除后再删除", name, n))
		}
		st.Roles = append(st.Roles[:k:k], st.Roles[k+1:]...)
		return nil
	})
	s.audit(r, u, "", "role.delete", "删除角色 "+name, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) copyRole(w http.ResponseWriter, r *http.Request, u *model.User) {
	id := r.PathValue("role")
	var out roleOut
	srcName := id
	err := s.Store.Update(func(st *store.State) error {
		_, acc, err := actorAccess(st, u.Login)
		if err != nil {
			return err
		}
		src, found := findRole(st, id)
		if !found {
			return missing("角色不存在")
		}
		srcName = src.Name
		if id == perm.AdminRole {
			return forbidden("超级管理员不可复制")
		}
		if !acc.Covers(perm.Role{ID: src.ID, Perms: src.Perms, Clusters: src.Clusters}) {
			return forbidden("不能复制权限高于自己的角色")
		}
		name := src.Name + " - 副本"
		for i := 2; checkRoleName(st, name, "") != nil; i++ {
			name = fmt.Sprintf("%s - 副本%d", src.Name, i)
		}
		role := model.Role{ID: newRoleID(st), Name: name, Desc: src.Desc, Clusters: append([]string{}, src.Clusters...), Perms: append([]string{}, src.Perms...), CreatedAt: time.Now()}
		st.Roles = append(st.Roles, role)
		out = roleView(role, 0)
		return nil
	})
	s.audit(r, u, "", "role.copy", "复制角色 "+srcName, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, out)
}
