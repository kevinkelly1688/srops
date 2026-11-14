// @author: kevinkelly
// @license: Apache Licence
// @file: rbac_test.go
// @time: 2026/10/28

package api

import (
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"starrocks-ops/fakesr"
	"starrocks-ops/internal/perm"
	"starrocks-ops/internal/store"
)

// as 以另一个用户登录，返回使用独立会话的 env。
func (e *env) as(login, pw string) *env {
	e.t.Helper()
	n := *e
	jar, _ := cookiejar.New(nil)
	n.c = &http.Client{Jar: jar}
	n.expect(200, "POST", "/api/login", map[string]string{"login": login, "password": pw}, nil)
	return &n
}

// firstLogin 新建用户首次登录：必须先改密码才能访问业务接口。
func (e *env) firstLogin(login, initial, pw string) *env {
	e.t.Helper()
	n := e.as(login, initial)
	n.expect(403, "GET", "/api/clusters", nil, nil)
	n.expect(200, "POST", "/api/password", map[string]string{"old": initial, "new": pw}, nil)
	return n
}

type userRes struct {
	User            userOut `json:"user"`
	InitialPassword string  `json:"initialPassword"`
}

func (e *env) newUser(login string, roles []string, pw string) userRes {
	e.t.Helper()
	var res userRes
	e.expect(200, "POST", "/api/users", map[string]any{"login": login, "name": "测试 " + login, "email": login + "@example.com", "roles": roles, "password": pw}, &res)
	return res
}

func TestRBAC(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	const adminPw = "Admin@2026xyz"

	// ---------- 角色与当前用户 ----------
	var me struct {
		Perms    []string  `json:"perms"`
		Clusters []string  `json:"clusters"`
		RoleList []roleOut `json:"roleList"`
	}
	e.expect(200, "GET", "/api/me", nil, &me)
	if len(me.Perms) != len(perm.All()) || len(me.Clusters) != 1 || me.Clusters[0] != "*" || len(me.RoleList) != 1 || me.RoleList[0].ID != "r_admin" {
		t.Fatalf("管理员的 /api/me 不对：%d 项权限 %v", len(me.Perms), me.Clusters)
	}
	var roles []roleOut
	e.expect(200, "GET", "/api/roles", nil, &roles)
	if len(roles) != 5 || roles[0].ID != "r_admin" || roles[0].Members != 1 || len(roles[0].Perms) != len(perm.All()) {
		t.Fatalf("预置角色不对：%+v", roles)
	}

	// 两个集群
	for _, name := range []string{"prod-test", "stg-test"} {
		body := e.conn()
		body["name"], body["env"], body["owner"] = name, "测试", "admin"
		e.expect(200, "POST", "/api/clusters", body, nil)
	}

	// ---------- 创建用户：校验 ----------
	e.expect(400, "POST", "/api/users", map[string]any{"login": "a b", "name": "x", "email": "x@example.com", "roles": []string{"r_ops"}}, nil)
	e.expect(400, "POST", "/api/users", map[string]any{"login": "bad_mail", "name": "x", "email": "nope", "roles": []string{"r_ops"}}, nil)
	e.expect(400, "POST", "/api/users", map[string]any{"login": "no_role", "name": "x", "email": "x@example.com", "roles": []string{}}, nil)
	e.expect(400, "POST", "/api/users", map[string]any{"login": "ghost_role", "name": "x", "email": "x@example.com", "roles": []string{"r_ghost"}}, nil)
	e.expect(400, "POST", "/api/users", map[string]any{"login": "weak_pw", "name": "x", "email": "x@example.com", "roles": []string{"r_ops"}, "password": "weak"}, nil)
	e.expect(400, "POST", "/api/users", map[string]any{"login": "old_exp", "name": "x", "email": "x@example.com", "roles": []string{"r_ops"}, "expireAt": "2020-01-01"}, nil)
	ops := e.newUser("ops_zhang", []string{"r_ops"}, "Zhang@2026xyz!")
	if ops.InitialPassword != "" || !ops.User.MustChange || ops.User.Status != "active" {
		t.Fatalf("指定密码时不应返回密码，且首次登录须改密：%+v", ops)
	}
	e.expect(409, "POST", "/api/users", map[string]any{"login": "OPS_zhang", "name": "x", "email": "x@example.com", "roles": []string{"r_ops"}}, nil) // 账号不区分大小写
	gen := e.newUser("dba_li", []string{"r_dba"}, "")
	if len(gen.InitialPassword) < 10 {
		t.Fatalf("未指定密码时应生成随机密码：%+v", gen)
	}

	// ---------- 数据范围：DBA 只能看 prod-test ----------
	e.expect(200, "PUT", "/api/roles/r_dba", map[string]any{"clusters": []string{"prod-test"}}, nil)
	dba := e.firstLogin("dba_li", gen.InitialPassword, "Dba@2026xyzab")
	var cl []clusterOut
	dba.expect(200, "GET", "/api/clusters", nil, &cl)
	if len(cl) != 1 || cl[0].ID != "prod-test" {
		t.Fatalf("DBA 只应看到 prod-test：%+v", cl)
	}
	dba.expect(200, "GET", "/api/clusters/prod-test/inspections", nil, nil)
	dba.expect(403, "GET", "/api/clusters/stg-test/inspections", nil, nil)
	dba.expect(403, "GET", "/api/clusters/stg-test/overview", nil, nil)
	dba.expect(200, "GET", "/api/clusters/prod-test/topology", nil, nil)
	// 没有授予的功能
	dba.expect(403, "PUT", "/api/clusters/prod-test/inspect/schedule", map[string]any{"enabled": false, "every": "daily", "at": "10:00"}, nil)
	dba.expect(403, "POST", "/api/clusters", e.conn(), nil)
	dba.expect(403, "GET", "/api/users", nil, nil)
	dba.expect(403, "GET", "/api/roles", nil, nil)
	dba.expect(403, "POST", "/api/users", map[string]any{"login": "x_user", "name": "x", "email": "x@example.com", "roles": []string{"r_ops"}}, nil)
	// 角色变更对已登录用户立即生效
	e.expect(400, "PUT", "/api/roles/r_dba", map[string]any{"perms": []string{"query.kill"}}, nil) // 按钮权限缺少菜单查看
	e.expect(200, "PUT", "/api/roles/r_dba", map[string]any{"perms": []string{"overview:view", "inspect:view"}}, nil)
	dba.expect(403, "GET", "/api/clusters/prod-test/topology", nil, nil)
	dba.expect(200, "GET", "/api/clusters/prod-test/overview", nil, nil)
	var dme struct{ Perms []string }
	dba.expect(200, "GET", "/api/me", nil, &dme)
	if len(dme.Perms) != 2 {
		t.Fatalf("DBA 的有效权限应只剩 2 项：%v", dme.Perms)
	}

	// ---------- 禁止越权提升 ----------
	var um roleOut
	e.expect(200, "POST", "/api/roles", map[string]any{"name": "用户管理员", "desc": "只管用户", "clusters": []string{"*"},
		"perms": []string{"overview:view", "users:view", "roles:view", "user.create", "user.edit", "user.reset", "user.lock", "user.delete"}}, &um)
	e.expect(409, "POST", "/api/roles", map[string]any{"name": "用户管理员"}, nil)
	e.expect(409, "POST", "/api/roles", map[string]any{"name": "超级管理员"}, nil)
	e.expect(400, "POST", "/api/roles", map[string]any{"name": "坏权限", "perms": []string{"nodes:view", "node.fly"}}, nil)
	e.expect(400, "POST", "/api/roles", map[string]any{"name": "坏集群", "perms": []string{"overview:view"}, "clusters": []string{"nope"}}, nil)
	mgrRes := e.newUser("mgr_wang", []string{um.ID}, "")
	mgr := e.firstLogin("mgr_wang", mgrRes.InitialPassword, "Mgr@2026xyzab1")
	mgr.expect(403, "POST", "/api/users", map[string]any{"login": "evil_1", "name": "x", "email": "x@example.com", "roles": []string{"r_admin"}}, nil)
	mgr.expect(403, "POST", "/api/users", map[string]any{"login": "evil_2", "name": "x", "email": "x@example.com", "roles": []string{"r_ops"}}, nil) // r_ops 的权限比他多
	mgr.expect(200, "POST", "/api/users", map[string]any{"login": "peer_1", "name": "x", "email": "x@example.com", "roles": []string{um.ID}}, nil)
	mgr.expect(403, "PUT", "/api/users/admin", map[string]any{"name": "x", "email": "x@example.com", "roles": []string{um.ID}}, nil)     // 不能管理 admin
	mgr.expect(403, "PUT", "/api/users/ops_zhang", map[string]any{"name": "x", "email": "x@example.com", "roles": []string{um.ID}}, nil) // 不能管理权限更高的用户
	mgr.expect(403, "POST", "/api/users/ops_zhang/reset", map[string]any{}, nil)
	mgr.expect(403, "POST", "/api/users/ops_zhang/lock", map[string]any{"locked": true}, nil)
	mgr.expect(403, "DELETE", "/api/users/ops_zhang", nil, nil)
	mgr.expect(403, "PUT", "/api/users/mgr_wang", map[string]any{"name": "x", "email": "x@example.com", "roles": []string{um.ID, "r_dba"}}, nil) // 不能改自己的角色
	mgr.expect(200, "PUT", "/api/users/mgr_wang", map[string]any{"name": "王管理", "email": "x@example.com", "roles": []string{um.ID}}, nil)        // 但可以改自己的资料
	mgr.expect(403, "POST", "/api/users/mgr_wang/reset", map[string]any{}, nil)
	mgr.expect(403, "POST", "/api/users/mgr_wang/lock", map[string]any{"locked": true}, nil)
	mgr.expect(403, "DELETE", "/api/users/mgr_wang", nil, nil)
	mgr.expect(403, "PUT", "/api/roles/"+um.ID, map[string]any{"perms": []string{"overview:view"}}, nil) // 没有 role.edit
	// 有 role.edit 的角色也不能授予自己没有的权限
	var re roleOut
	e.expect(200, "POST", "/api/roles", map[string]any{"name": "角色编辑", "clusters": []string{"prod-test"}, "perms": []string{"overview:view", "roles:view", "role.edit", "role.create", "role.copy"}}, &re)
	reRes := e.newUser("role_ed", []string{re.ID}, "")
	roleEd := e.firstLogin("role_ed", reRes.InitialPassword, "RoleEd@2026xyz1")
	roleEd.expect(403, "PUT", "/api/roles/"+re.ID, map[string]any{"perms": []string{"overview:view", "roles:view", "role.edit", "node.stop", "nodes:view"}}, nil)
	roleEd.expect(403, "PUT", "/api/roles/"+re.ID, map[string]any{"clusters": []string{"stg-test"}}, nil)
	roleEd.expect(403, "PUT", "/api/roles/r_ops", map[string]any{"perms": []string{"overview:view"}}, nil) // r_ops 权限比他高
	roleEd.expect(403, "POST", "/api/roles/r_ops/copy", nil, nil)
	roleEd.expect(403, "POST", "/api/roles", map[string]any{"name": "偷偷提权", "perms": []string{"overview:view", "user.create", "users:view"}, "clusters": []string{"prod-test"}}, nil)
	roleEd.expect(200, "POST", "/api/roles", map[string]any{"name": "只看总览", "perms": []string{"overview:view"}, "clusters": []string{"prod-test"}}, nil)
	var cp roleOut
	roleEd.expect(200, "POST", "/api/roles/"+re.ID+"/copy", nil, &cp)
	if cp.Name != "角色编辑 - 副本" || len(cp.Perms) != 5 {
		t.Fatalf("复制角色不对：%+v", cp)
	}
	roleEd.expect(200, "PUT", "/api/roles/"+re.ID, map[string]any{"perms": []string{"overview:view", "roles:view", "role.edit"}}, nil) // 可以收回自己的权限
	roleEd.expect(403, "POST", "/api/roles/"+re.ID+"/copy", nil, nil)                                                                  // 已失去 role.copy

	// ---------- 锁定 / 解锁 / 重置密码 / 删除：会话立即失效 ----------
	e.expect(200, "POST", "/api/users/dba_li/lock", map[string]any{"locked": true}, nil)
	dba.expect(401, "GET", "/api/clusters", nil, nil)
	e.expect(403, "POST", "/api/login", map[string]string{"login": "dba_li", "password": "Dba@2026xyzab"}, nil)
	e.expect(200, "POST", "/api/users/dba_li/lock", map[string]any{"locked": false}, nil)
	dba = e.as("dba_li", "Dba@2026xyzab")
	dba.expect(200, "GET", "/api/clusters", nil, nil)

	var rst struct{ Password string }
	e.expect(200, "POST", "/api/users/mgr_wang/reset", map[string]any{}, &rst)
	mgr.expect(401, "GET", "/api/users", nil, nil) // 重置后原会话下线
	e.expect(401, "POST", "/api/login", map[string]string{"login": "mgr_wang", "password": "Mgr@2026xyzab1"}, nil)
	mgr = e.firstLogin("mgr_wang", rst.Password, "Mgr@2026xyzab2")
	e.expect(400, "POST", "/api/users/mgr_wang/reset", map[string]any{"password": "weak"}, nil)
	e.expect(403, "POST", "/api/users/admin/reset", map[string]any{}, nil) // 不能重置自己（admin 就是当前用户）

	// 管理员保护
	e.expect(403, "POST", "/api/users/admin/lock", map[string]any{"locked": true}, nil)
	e.expect(403, "DELETE", "/api/users/admin", nil, nil)
	e.newUser("admin2", []string{"r_admin"}, "Admin2@2026xyzab")
	admin2 := e.firstLogin("admin2", "Admin2@2026xyzab", "Admin2@2026xyzab2")
	admin2.expect(403, "PUT", "/api/users/admin", map[string]any{"name": "x", "email": "x@example.com", "roles": []string{"r_ops"}}, nil) // admin 必须保留超级管理员
	admin2.expect(403, "POST", "/api/users/admin/lock", map[string]any{"locked": true}, nil)
	admin2.expect(403, "DELETE", "/api/users/admin", nil, nil)
	admin2.expect(403, "PUT", "/api/roles/r_admin", map[string]any{"perms": []string{"overview:view"}}, nil)
	admin2.expect(403, "DELETE", "/api/roles/r_admin", nil, nil)
	admin2.expect(403, "PUT", "/api/roles/r_audit", map[string]any{"name": "改名"}, nil)                      // 内置角色不能改名
	admin2.expect(403, "DELETE", "/api/roles/r_audit", nil, nil)                                            // 内置角色不能删除
	admin2.expect(200, "PUT", "/api/roles/r_audit", map[string]any{"clusters": []string{"prod-test"}}, nil) // 内置角色的权限与范围可调
	admin2.expect(200, "PUT", "/api/roles/r_audit", map[string]any{"clusters": []string{"*"}}, nil)

	// 有效期
	exp := e.newUser("temp_user", []string{"r_oncall"}, "Temp@2026xyzabc")
	tmp := e.firstLogin("temp_user", "Temp@2026xyzabc", "Temp@2026xyzabd")
	tmp.expect(200, "GET", "/api/clusters", nil, nil)
	must(t, e.api.Store.Update(func(st *store.State) error {
		for i := range st.Users {
			if st.Users[i].Login == exp.User.Login {
				st.Users[i].ExpireAt = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
			}
		}
		return nil
	}))
	tmp.expect(401, "GET", "/api/clusters", nil, nil)
	e.expect(403, "POST", "/api/login", map[string]string{"login": "temp_user", "password": "Temp@2026xyzabd"}, nil)
	var list []userOut
	e.expect(200, "GET", "/api/users", nil, &list)
	for _, u := range list {
		if u.Login == "temp_user" && (!u.Expired || u.Status != "locked") {
			t.Fatalf("过期用户应显示为不可用：%+v", u)
		}
	}
	e.expect(200, "PUT", "/api/users/temp_user", map[string]any{"name": "t", "email": "t@example.com", "roles": []string{"r_oncall"}, "expireAt": time.Now().AddDate(0, 0, 30).Format("2006-01-02")}, nil)
	e.as("temp_user", "Temp@2026xyzabd")

	// ---------- 角色删除 / 集群移除 ----------
	e.expect(409, "DELETE", "/api/roles/"+um.ID, nil, nil) // 仍有成员
	e.expect(200, "DELETE", "/api/users/mgr_wang", nil, nil)
	mgr.expect(401, "GET", "/api/clusters", nil, nil)
	e.expect(404, "DELETE", "/api/users/mgr_wang", nil, nil)
	e.expect(404, "DELETE", "/api/roles/r_ghost", nil, nil)
	var rr []roleOut
	e.expect(200, "GET", "/api/roles", nil, &rr)
	e.expect(200, "PUT", "/api/roles/r_dba", map[string]any{"clusters": []string{"prod-test", "stg-test"}}, nil)
	e.expect(200, "DELETE", "/api/clusters/stg-test", nil, nil)
	e.expect(200, "GET", "/api/roles", nil, &rr)
	for _, r := range rr {
		if r.ID == "r_dba" && (len(r.Clusters) != 1 || r.Clusters[0] != "prod-test") {
			t.Fatalf("移除集群后应从角色数据范围中去掉：%+v", r)
		}
	}

	// 登录页的失败锁定：管理员解锁时一并解除
	for i := 0; i < 5; i++ {
		e.call("POST", "/api/login", map[string]string{"login": "ops_zhang", "password": "wrong"}, nil)
	}
	e.expect(429, "POST", "/api/login", map[string]string{"login": "ops_zhang", "password": "Zhang@2026xyz!"}, nil)
	e.expect(200, "GET", "/api/users", nil, &list)
	for _, u := range list {
		if u.Login == "ops_zhang" && !u.TempLocked {
			t.Fatalf("应显示为临时锁定：%+v", u)
		}
	}
	e.expect(200, "POST", "/api/users/ops_zhang/lock", map[string]any{"locked": false}, nil)
	e.as("ops_zhang", "Zhang@2026xyz!")

	// ---------- 审计 ----------
	audit, _ := os.ReadFile(filepath.Join(e.dir, "audit.jsonl"))
	for _, want := range []string{"user.create", "user.edit", "user.reset", "user.lock", "user.delete", "role.create", "role.edit", "role.copy", "拒绝（无权限）"} {
		if !strings.Contains(string(audit), want) {
			t.Errorf("审计日志缺少 %s", want)
		}
	}
	if strings.Contains(string(audit), rst.Password) || strings.Contains(string(audit), gen.InitialPassword) {
		t.Fatal("审计日志中不应出现密码")
	}
	raw, _ := os.ReadFile(filepath.Join(e.dir, "state.json"))
	if strings.Contains(string(raw), rst.Password) || strings.Contains(string(raw), "Zhang@2026xyz!") || strings.Contains(string(raw), adminPw) {
		t.Fatal("state.json 中不应出现明文密码")
	}
}

// 只有「角色权限」查看权限（没有「用户管理」）的用户可以看到成员，但看不到邮箱和手机。
func TestRolesViewerSeesNoContacts(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	e.expect(200, "POST", "/api/roles", map[string]any{"name": "只看角色", "perms": []string{"overview:view", "roles:view"}, "clusters": []string{}}, nil)
	var rs []roleOut
	e.expect(200, "GET", "/api/roles", nil, &rs)
	var id string
	for _, r := range rs {
		if r.Name == "只看角色" {
			id = r.ID
		}
	}
	var cr userRes
	e.expect(200, "POST", "/api/users", map[string]any{"login": "viewer_1", "name": "查看者", "email": "v@example.com", "phone": "13800000000", "roles": []string{id}}, &cr)
	v := e.firstLogin("viewer_1", cr.InitialPassword, "Viewer@2026xyz1")
	var us []userOut
	v.expect(200, "GET", "/api/users", nil, &us)
	if len(us) != 2 {
		t.Fatalf("应看到 2 个用户：%+v", us)
	}
	for _, u := range us {
		if u.Email != "" || u.Phone != "" {
			t.Fatalf("不应看到联系方式：%+v", u)
		}
	}
	e.expect(200, "GET", "/api/users", nil, &us)
	if us[0].Email == "" && us[1].Email == "" {
		t.Fatal("管理员应能看到邮箱")
	}
}
