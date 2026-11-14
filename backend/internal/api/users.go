// @author: kevinkelly
// @license: Apache Licence
// @file: users.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/perm"
	"starrocks-ops/internal/secure"
	"starrocks-ops/internal/store"
)

var (
	loginRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

const builtinAdmin = "admin"

type userOut struct {
	Login      string    `json:"login"`
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	Phone      string    `json:"phone"`
	Dept       string    `json:"dept"`
	Roles      []string  `json:"roles"`
	Status     string    `json:"status"`     // active / locked
	Locked     bool      `json:"locked"`     // 管理员锁定
	TempLocked bool      `json:"tempLocked"` // 连续输错密码被临时锁定
	Expired    bool      `json:"expired"`
	ExpireAt   string    `json:"expireAt"`
	MustChange bool      `json:"mustChange"`
	LastLogin  time.Time `json:"lastLogin"`
	CreatedAt  time.Time `json:"createdAt"`
	CreatedBy  string    `json:"createdBy"`
	Self       bool      `json:"self"`
	Manageable bool      `json:"manageable"` // 当前用户有资格管理该用户（其角色不超出自己的权限）
}

func (s *Server) tempLocked(login string) bool {
	s.Sessions.mu.Lock()
	defer s.Sessions.mu.Unlock()
	t, ok := s.Sessions.locked[login]
	return ok && time.Now().Before(t)
}

func (s *Server) clearFails(login string) {
	s.Sessions.mu.Lock()
	delete(s.Sessions.locked, login)
	delete(s.Sessions.fails, login)
	s.Sessions.mu.Unlock()
}

func (s *Server) userOutOf(st *store.State, acc *perm.Access, actor string, u model.User, now time.Time) userOut {
	o := userOut{Login: u.Login, Name: u.Name, Email: u.Email, Phone: u.Phone, Dept: u.Dept, Roles: u.Roles, Locked: u.Locked, TempLocked: s.tempLocked(u.Login),
		Expired: u.Expired(now), ExpireAt: u.ExpireAt, MustChange: u.MustChange, LastLogin: u.LastLogin, CreatedAt: u.CreatedAt, CreatedBy: u.CreatedBy,
		Self: u.Login == actor, Manageable: u.Login != actor && manageable(st, acc, u)}
	if o.Roles == nil {
		o.Roles = []string{}
	}
	o.Status = "active"
	if o.Locked || o.TempLocked || o.Expired {
		o.Status = "locked"
	}
	return o
}

// listUsers 返回全部用户。只有「角色权限」查看权限、没有「用户管理」查看权限的用户看不到邮箱与手机。
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, u *model.User) {
	acc := accessOf(r)
	full := acc.Can("users:view")
	out := []userOut{}
	now := time.Now()
	s.Store.View(func(st *store.State) {
		for _, x := range st.Users {
			o := s.userOutOf(st, acc, u.Login, x, now)
			if !full {
				o.Email, o.Phone = "", ""
			}
			out = append(out, o)
		}
	})
	ok(w, out)
}

type userIn struct {
	Login    string   `json:"login"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Phone    string   `json:"phone"`
	Dept     string   `json:"dept"`
	Roles    []string `json:"roles"`
	ExpireAt string   `json:"expireAt"`
	Password string   `json:"password"` // 创建 / 重置时指定；留空则生成随机密码
	SendMail bool     `json:"sendMail"` // 同时把账号与密码发送到用户邮箱（需先配置邮件服务）
}

// check 校验用户信息字段（不含账号与密码）。
func (in *userIn) check() error {
	in.Name, in.Email, in.Phone, in.Dept = cleanText(in.Name), cleanText(in.Email), cleanText(in.Phone), cleanText(in.Dept)
	switch {
	case in.Name == "" || rlen(in.Name) > 50:
		return bad("姓名必填，最多 50 个字")
	case !emailRe.MatchString(in.Email) || rlen(in.Email) > 100:
		return bad("邮箱格式不正确")
	case rlen(in.Phone) > 30:
		return bad("手机号过长")
	case rlen(in.Dept) > 50:
		return bad("部门最多 50 个字")
	case len(in.Roles) == 0:
		return bad("至少选择一个角色")
	}
	if in.ExpireAt != "" {
		t, err := time.ParseInLocation("2006-01-02", in.ExpireAt, time.Now().Location())
		if err != nil {
			return bad("有效期格式须为 YYYY-MM-DD，留空表示永久")
		}
		if time.Now().After(t.AddDate(0, 0, 1)) {
			return bad("有效期不能早于今天")
		}
	}
	seen := map[string]bool{}
	for _, id := range in.Roles {
		if seen[id] {
			return bad("角色重复：" + id)
		}
		seen[id] = true
	}
	return nil
}

// grantable 检查 actor 是否可以授予这些角色：角色必须存在，且不超出 actor 自己的权限与数据范围（防止越权提升）。
func grantable(st *store.State, acc *perm.Access, roleIDs []string) error {
	for _, id := range roleIDs {
		r, ok := findRole(st, id)
		if !ok {
			return bad("角色不存在：" + id)
		}
		if !acc.Covers(perm.Role{ID: r.ID, Perms: r.Perms, Clusters: r.Clusters}) {
			return forbidden(fmt.Sprintf("不能授予角色「%s」：它的权限或数据范围超出了您自己的权限", r.Name))
		}
	}
	return nil
}

func actorAccess(st *store.State, login string) (model.User, *perm.Access, error) {
	for _, x := range st.Users {
		if x.Login == login {
			return x, accessIn(st, x.Roles), nil
		}
	}
	return model.User{}, nil, forbidden("当前账号已不存在")
}

func findUser(st *store.State, login string) (int, bool) {
	for i := range st.Users {
		if st.Users[i].Login == login {
			return i, true
		}
	}
	return -1, false
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in userIn
	if !decode(w, r, &in) {
		return
	}
	in.Login = strings.TrimSpace(in.Login)
	err := func() error {
		if !loginRe.MatchString(in.Login) {
			return bad("账号须为 3–32 位字母、数字或下划线")
		}
		return in.check()
	}()
	generated := in.Password == ""
	pw := in.Password
	var hash string
	if err == nil {
		if generated {
			pw = secure.RandomPasswordN(s.cfg().Security.PwdMinLen)
		} else if err = secure.ValidatePassword(pw, s.cfg().Security.PwdMinLen); err != nil {
			err = bad(err.Error())
		}
	}
	if err == nil {
		if hash, err = secure.HashPassword(pw); err != nil {
			err = apiErr{http.StatusInternalServerError, err.Error()}
		}
	}
	var out userOut
	if err == nil {
		err = s.Store.Update(func(st *store.State) error {
			_, acc, err := actorAccess(st, u.Login)
			if err != nil {
				return err
			}
			for _, x := range st.Users {
				if strings.EqualFold(x.Login, in.Login) {
					return conflict("账号已存在")
				}
			}
			if err := grantable(st, acc, in.Roles); err != nil {
				return err
			}
			nu := model.User{Login: in.Login, Name: in.Name, Email: in.Email, Phone: in.Phone, Dept: in.Dept, Roles: in.Roles, PwdHash: hash,
				MustChange: true, ExpireAt: in.ExpireAt, CreatedAt: time.Now(), PwdAt: time.Now(), CreatedBy: u.Login}
			st.Users = append(st.Users, nu)
			out = s.userOutOf(st, acc, u.Login, nu, time.Now())
			return nil
		})
	}
	s.audit(r, u, "", "user.create", fmt.Sprintf("新建用户 %s（角色 %s）", in.Login, strings.Join(in.Roles, "/")), err)
	if err != nil {
		writeErr(w, err)
		return
	}
	res := map[string]any{"user": out}
	if generated {
		res["initialPassword"] = pw // 仅此一次返回
	}
	if in.SendMail {
		s.mailCredentials(r, u, res, out.Email, in.Login, pw, "已为您创建平台账号")
	}
	ok(w, res)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request, u *model.User) {
	login := r.PathValue("login")
	var in userIn
	if !decode(w, r, &in) {
		return
	}
	var out userOut
	var act string
	err := in.check()
	if err == nil {
		err = s.Store.Update(func(st *store.State) error {
			_, acc, err := actorAccess(st, u.Login)
			if err != nil {
				return err
			}
			i, found := findUser(st, login)
			if !found {
				return missing("用户不存在")
			}
			old := st.Users[i]
			if login != u.Login && !manageable(st, acc, old) {
				return forbidden("不能管理权限高于自己的用户")
			}
			rolesChanged := !sameSet(old.Roles, in.Roles)
			if login == u.Login && (rolesChanged || in.ExpireAt != old.ExpireAt) {
				return forbidden("不能修改自己的角色或有效期，请联系其他管理员")
			}
			if rolesChanged {
				if err := grantable(st, acc, in.Roles); err != nil {
					return err
				}
			}
			if login == builtinAdmin && (!containsStr(in.Roles, perm.AdminRole) || in.ExpireAt != "") {
				return forbidden("内置账号 admin 必须保留超级管理员角色，且不能设置有效期")
			}
			nu := old
			nu.Name, nu.Email, nu.Phone, nu.Dept, nu.Roles, nu.ExpireAt = in.Name, in.Email, in.Phone, in.Dept, in.Roles, in.ExpireAt
			next := append([]model.User(nil), st.Users...)
			next[i] = nu
			if countAdmins(next, time.Now()) == 0 {
				return conflict("至少需要保留一个可用的超级管理员")
			}
			if rolesChanged {
				act = fmt.Sprintf("编辑用户 %s；角色 %s → %s", login, strings.Join(old.Roles, "/"), strings.Join(in.Roles, "/"))
			} else {
				act = "编辑用户 " + login
			}
			st.Users[i] = nu
			out = s.userOutOf(st, acc, u.Login, nu, time.Now())
			return nil
		})
	}
	if act == "" {
		act = "编辑用户 " + login
	}
	s.audit(r, u, "", "user.edit", act, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, out)
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// resetPassword 重置密码：随机生成（默认）或管理员指定，首次登录强制修改，原会话全部下线，并解除登录失败锁定。
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request, u *model.User) {
	login := r.PathValue("login")
	var in struct {
		Password string `json:"password"`
		SendMail bool   `json:"sendMail"`
	}
	if !decode(w, r, &in) {
		return
	}
	var email string
	generated := in.Password == ""
	pw := in.Password
	var err error
	if generated {
		pw = secure.RandomPasswordN(s.cfg().Security.PwdMinLen)
	} else if err = secure.ValidatePassword(pw, s.cfg().Security.PwdMinLen); err != nil {
		err = bad(err.Error())
	}
	var hash string
	if err == nil {
		if hash, err = secure.HashPassword(pw); err != nil {
			err = apiErr{http.StatusInternalServerError, err.Error()}
		}
	}
	if err == nil {
		err = s.Store.Update(func(st *store.State) error {
			if login == u.Login {
				return forbidden("不能重置自己的密码，请使用右上角个人菜单中的「修改密码」")
			}
			_, acc, err := actorAccess(st, u.Login)
			if err != nil {
				return err
			}
			i, found := findUser(st, login)
			if !found {
				return missing("用户不存在")
			}
			if !manageable(st, acc, st.Users[i]) {
				return forbidden("不能管理权限高于自己的用户")
			}
			st.Users[i].PwdHash, st.Users[i].MustChange, st.Users[i].PwdAt = hash, true, time.Now()
			email = st.Users[i].Email
			return nil
		})
	}
	s.audit(r, u, "", "user.reset", "重置密码 "+login, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Sessions.dropUser(login, "")
	s.clearFails(login)
	res := map[string]any{"ok": true}
	if generated {
		res["password"] = pw // 仅此一次返回
	}
	if in.SendMail {
		s.mailCredentials(r, u, res, email, login, pw, "您的平台密码已被管理员重置")
	}
	ok(w, res)
}

// lockUser 锁定 / 解锁。锁定后立即下线；解锁同时解除“连续输错密码”的临时锁定。
func (s *Server) lockUser(w http.ResponseWriter, r *http.Request, u *model.User) {
	login := r.PathValue("login")
	var in struct {
		Locked bool `json:"locked"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.Store.Update(func(st *store.State) error {
		if login == u.Login {
			return forbidden("不能锁定当前登录的账号")
		}
		if login == builtinAdmin {
			return forbidden("内置账号 admin 不可锁定")
		}
		_, acc, err := actorAccess(st, u.Login)
		if err != nil {
			return err
		}
		i, found := findUser(st, login)
		if !found {
			return missing("用户不存在")
		}
		if !manageable(st, acc, st.Users[i]) {
			return forbidden("不能管理权限高于自己的用户")
		}
		next := append([]model.User(nil), st.Users...)
		next[i].Locked = in.Locked
		if countAdmins(next, time.Now()) == 0 {
			return conflict("至少需要保留一个可用的超级管理员")
		}
		st.Users[i].Locked = in.Locked
		return nil
	})
	word := map[bool]string{true: "锁定", false: "解锁"}[in.Locked]
	s.audit(r, u, "", "user.lock", word+"用户 "+login, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	if in.Locked {
		s.Sessions.dropUser(login, "")
	} else {
		s.clearFails(login)
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request, u *model.User) {
	login := r.PathValue("login")
	err := s.Store.Update(func(st *store.State) error {
		if login == u.Login {
			return forbidden("不能删除当前登录的账号")
		}
		if login == builtinAdmin {
			return forbidden("内置账号 admin 不可删除")
		}
		_, acc, err := actorAccess(st, u.Login)
		if err != nil {
			return err
		}
		i, found := findUser(st, login)
		if !found {
			return missing("用户不存在")
		}
		if !manageable(st, acc, st.Users[i]) {
			return forbidden("不能管理权限高于自己的用户")
		}
		next := append(append([]model.User(nil), st.Users[:i]...), st.Users[i+1:]...)
		if countAdmins(next, time.Now()) == 0 {
			return conflict("至少需要保留一个可用的超级管理员")
		}
		st.Users = next
		return nil
	})
	s.audit(r, u, "", "user.delete", "删除用户 "+login, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.Sessions.dropUser(login, "")
	s.clearFails(login)
	ok(w, map[string]bool{"ok": true})
}

// mailCredentials 把账号与密码邮件发送给用户（可选）。邮件以明文传输密码，请只在内部可信邮件服务上使用；用户首次登录必须修改密码。
func (s *Server) mailCredentials(r *http.Request, actor *model.User, res map[string]any, to, login, pw, lead string) {
	url := s.cfg().Basic.URL
	body := fmt.Sprintf("您好，\n\n%s。\n\n账号：%s\n密码：%s\n\n首次登录必须修改密码。", lead, login, pw)
	if url != "" {
		body += "\n登录地址：" + url
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	err := s.SendMail(ctx, []string{to}, "【"+s.cfg().Basic.Name+"】账号信息", body)
	s.audit(r, actor, "", "user.create", "邮件通知用户 "+login+"（"+to+"）", err)
	if err != nil {
		res["mailError"] = err.Error()
		return
	}
	res["mailSent"] = to
}
