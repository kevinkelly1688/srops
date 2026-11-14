// @author: kevinkelly
// @license: Apache Licence
// @file: auth.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/perm"
	"starrocks-ops/internal/secure"
)

const (
	cookieName = "sr_sid"
	maxAge     = 12 * time.Hour
	lockFor    = 30 * time.Minute
)

type session struct {
	login         string
	created, seen time.Time
}

// Sessions 是内存会话表与登录失败计数（平台重启后需重新登录）。
type Sessions struct {
	mu     sync.Mutex
	m      map[string]*session
	fails  map[string]int
	locked map[string]time.Time
}

func NewSessions() *Sessions {
	return &Sessions{m: map[string]*session{}, fails: map[string]int{}, locked: map[string]time.Time{}}
}

func (ss *Sessions) get(tok string, idleTimeout time.Duration) (string, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.m[tok]
	if !ok {
		return "", false
	}
	now := time.Now()
	if now.Sub(s.seen) > idleTimeout || now.Sub(s.created) > maxAge {
		delete(ss.m, tok)
		return "", false
	}
	s.seen = now
	return s.login, true
}

func (ss *Sessions) dropUser(login, except string) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for k, s := range ss.m {
		if s.login == login && k != except {
			delete(ss.m, k)
		}
	}
}

// Bootstrap 在没有任何用户时创建内置管理员 admin，初始密码写入 data/initial_admin_password。
func (s *Server) Bootstrap() error {
	s.alertPend, s.alertFail, s.customErr = map[string]time.Time{}, map[string]int{}, map[string]customRun{}
	if err := s.seedRoles(); err != nil {
		return err
	}
	s.applySettings()
	s.loadTasks()
	s.normalizeUpgrades()
	if s.Runner != nil {
		s.Runner.Mailer = s.SendMail
		s.Runner.BaseURL = func() string { return s.cfg().Basic.URL }
	}
	var n int
	s.Store.View(func(st *storeState) { n = len(st.Users) })
	if n > 0 {
		return nil
	}
	pw := secure.RandomPassword()
	h, err := secure.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := s.Store.PutUser(model.User{Login: "admin", Name: "系统管理员", Roles: []string{"r_admin"}, PwdHash: h, PwdAt: time.Now(), MustChange: true, CreatedAt: time.Now()}); err != nil {
		return err
	}
	f := filepath.Join(s.Store.Dir(), "initial_admin_password")
	if err := os.WriteFile(f, []byte(pw+"\n"), 0o600); err != nil {
		return err
	}
	logf("已创建内置管理员 admin，初始密码见 %s（首次登录必须修改）", f)
	return nil
}

type handler func(w http.ResponseWriter, r *http.Request, u *model.User)

type ctxKey struct{}

// accessOf 取出 auth 中间件为本次请求计算好的有效权限。
func accessOf(r *http.Request) *perm.Access {
	a, _ := r.Context().Value(ctxKey{}).(*perm.Access)
	return a
}

// auth 校验会话、CSRF 头与权限码（code 可用 | 表示任一）。allowMustChange=false 时，未修改初始密码的用户被拒绝。
// 路径中带集群 {id} 的接口，还会校验该集群在用户的数据范围内。权限每次请求都按最新的角色计算，角色变更立即生效。
func (s *Server) auth(code string, allowMustChange bool, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Header.Get("X-Requested-With") != "sr-ops" {
			fail(w, http.StatusForbidden, "缺少请求头 X-Requested-With")
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil {
			fail(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		login, okk := s.Sessions.get(c.Value, time.Duration(s.cfg().Security.SessionTimeout)*time.Minute)
		if !okk {
			fail(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		var u model.User
		var found bool
		var acc *perm.Access
		s.Store.View(func(st *storeState) {
			for _, x := range st.Users {
				if x.Login == login {
					u, found = x, true
				}
			}
			if found {
				acc = accessIn(st, u.Roles)
			}
		})
		if !found || u.Locked {
			fail(w, http.StatusUnauthorized, "账号不存在或已锁定")
			return
		}
		if u.Expired(time.Now()) {
			fail(w, http.StatusUnauthorized, "账号已过有效期")
			return
		}
		if u.MustChange && !allowMustChange {
			fail(w, http.StatusForbidden, "must_change_password")
			return
		}
		if !acc.Can(code) {
			s.audit(r, &u, r.PathValue("id"), code, "无权限访问 "+r.Method+" "+r.URL.Path, errDenied)
			fail(w, http.StatusForbidden, "无权限："+code)
			return
		}
		if id := r.PathValue("id"); id != "" && !acc.Cluster(id) {
			s.audit(r, &u, id, code, "无权访问集群 "+id+"："+r.Method+" "+r.URL.Path, errDenied)
			fail(w, http.StatusForbidden, "无权访问该集群（不在您的数据范围内）")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, acc)), &u)
	}
}

type deniedErr struct{}

func (deniedErr) Error() string { return "拒绝（无权限）" }

var errDenied = deniedErr{}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Requested-With") != "sr-ops" {
		fail(w, http.StatusForbidden, "缺少请求头 X-Requested-With")
		return
	}
	var in struct{ Login, Password string }
	if !decode(w, r, &in) {
		return
	}
	ss := s.Sessions
	ss.mu.Lock()
	if t, ok := ss.locked[in.Login]; ok && time.Now().Before(t) {
		ss.mu.Unlock()
		fail(w, http.StatusTooManyRequests, "连续失败次数过多，账号已临时锁定 30 分钟，请稍后再试或联系管理员")
		return
	}
	ss.mu.Unlock()
	failLimit := s.cfg().Security.LoginFailLock
	u, found := s.Store.User(in.Login)
	if !found || !secure.CheckPassword(in.Password, u.PwdHash) {
		ss.mu.Lock()
		ss.fails[in.Login]++
		n := ss.fails[in.Login]
		if n >= failLimit {
			ss.locked[in.Login], ss.fails[in.Login] = time.Now().Add(lockFor), 0
		}
		ss.mu.Unlock()
		s.Store.Audit(storeAudit(in.Login, clientIP(r), "auth.login", "登录失败（账号或密码错误）", "失败"))
		if n >= failLimit {
			fail(w, http.StatusTooManyRequests, "连续失败 "+itoa(failLimit)+" 次，账号已临时锁定 30 分钟")
			return
		}
		fail(w, http.StatusUnauthorized, "账号或密码错误，还可尝试 "+itoa(failLimit-n)+" 次")
		return
	}
	if u.Locked {
		s.Store.Audit(storeAudit(in.Login, clientIP(r), "auth.login", "登录被拒绝（账号已锁定）", "拒绝"))
		fail(w, http.StatusForbidden, "账号已被管理员锁定，请联系管理员解锁")
		return
	}
	if u.Expired(time.Now()) {
		s.Store.Audit(storeAudit(in.Login, clientIP(r), "auth.login", "登录被拒绝（账号已过有效期）", "拒绝"))
		fail(w, http.StatusForbidden, "账号已过有效期，请联系管理员")
		return
	}
	tok := secure.Token()
	now := time.Now()
	ss.mu.Lock()
	ss.fails[in.Login] = 0
	ss.m[tok] = &session{login: u.Login, created: now, seen: now}
	ss.mu.Unlock()
	prev := u.LastLogin
	u.LastLogin = now
	if days := s.cfg().Security.PwdExpireDays; days > 0 {
		switch {
		case u.PwdAt.IsZero():
			u.PwdAt = now // 升级前创建的账号：从现在开始计算密码有效期
		case now.Sub(u.PwdAt) > time.Duration(days)*24*time.Hour && !u.MustChange:
			u.MustChange = true
			s.Store.Audit(storeAudit(u.Login, clientIP(r), "auth.login", fmt.Sprintf("密码已超过 %d 天有效期，要求修改", days), "成功"))
		}
	}
	s.Store.PutUser(u)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	s.Store.Audit(storeAudit(u.Login, clientIP(r), "auth.login", "登录成功（账号密码）", "成功"))
	v := s.userView(&u)
	v["ip"] = clientIP(r)
	v["prevLogin"] = prev
	ok(w, v)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, u *model.User) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.Sessions.mu.Lock()
		delete(s.Sessions.m, c.Value)
		s.Sessions.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	s.audit(r, u, "", "auth.login", "退出登录", nil)
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *model.User) {
	v := s.userView(u)
	v["ip"] = clientIP(r)
	ok(w, v)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct{ Old, New string }
	if !decode(w, r, &in) {
		return
	}
	if !secure.CheckPassword(in.Old, u.PwdHash) {
		fail(w, http.StatusBadRequest, "当前密码不正确")
		return
	}
	if in.Old == in.New {
		fail(w, http.StatusBadRequest, "新密码不能与当前密码相同")
		return
	}
	if err := secure.ValidatePassword(in.New, s.cfg().Security.PwdMinLen); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	h, err := secure.HashPassword(in.New)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	u.PwdHash, u.MustChange, u.PwdAt = h, false, time.Now()
	if err := s.Store.PutUser(*u); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		s.Sessions.dropUser(u.Login, c.Value) // 其他会话下线
	}
	os.Remove(filepath.Join(s.Store.Dir(), "initial_admin_password"))
	s.audit(r, u, "", "auth.login", "修改密码", nil)
	ok(w, s.userView(u))
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
