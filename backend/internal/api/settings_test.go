// @author: kevinkelly
// @license: Apache Licence
// @file: settings_test.go
// @time: 2026/10/28

package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"io"
	"net"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"starrocks-ops/fakesr"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/store"
)

// fakeSMTP 是不加密、不认证的最小 SMTP 服务，记录收到的邮件。
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	mail []string
}

func startSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				rd := bufio.NewReader(c)
				w := func(s string) { io.WriteString(c, s+"\r\n") }
				w("220 fake")
				var data strings.Builder
				inData := false
				for {
					line, err := rd.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimRight(line, "\r\n")
					if inData {
						if line == "." {
							inData = false
							f.mu.Lock()
							f.mail = append(f.mail, data.String())
							f.mu.Unlock()
							w("250 queued")
							continue
						}
						data.WriteString(line + "\n")
						continue
					}
					switch strings.ToUpper(strings.Fields(line + " x")[0]) {
					case "DATA":
						inData = true
						w("354 go")
					case "QUIT":
						w("221 bye")
						return
					case "EHLO":
						w("250 fake")
					default:
						w("250 OK")
					}
				}
			}()
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return port(f.ln.Addr().String()) }
func (f *fakeSMTP) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mail)
}
func (f *fakeSMTP) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.mail) == 0 {
		return ""
	}
	return f.mail[len(f.mail)-1]
}

func (e *env) withIP(ip string) *env {
	n := *e
	n.hdr = map[string]string{"X-Forwarded-For": ip}
	return &n
}

func TestSettingsSecurityEffective(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()

	var st struct {
		Name     string
		Security map[string]any
		Basic    map[string]any
		Notify   map[string]any
	}
	e.expect(200, "GET", "/api/settings", nil, &st)
	if st.Name != "StarRocks 运维平台" || st.Security["sessionTimeout"] != float64(30) || st.Basic["auditDays"] != float64(180) || st.Notify["smtpPort"] != float64(465) {
		t.Fatalf("默认设置不对：%+v", st)
	}

	// 参数校验
	good := map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": false, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range good {
			m[a] = b
		}
		m[k] = v
		return m
	}
	for k, v := range map[string]any{"noPermBtn": "x", "sessionTimeout": 1, "pwdMinLen": 4, "pwdExpireDays": -1, "loginFailLock": 1, "ipWhitelist": "10.0.0.0/33"} {
		e.expect(400, "PUT", "/api/settings/security", with(k, v), nil)
	}

	// 密码最小长度对创建用户立即生效
	e.expect(200, "PUT", "/api/settings/security", with("pwdMinLen", 14), nil)
	e.expect(400, "POST", "/api/users", map[string]any{"login": "pw_short", "name": "x", "email": "x@example.com", "roles": []string{"r_ops"}, "password": "Abcd@12345xy"}, nil)
	gen := e.newUser("pw_long", []string{"r_ops"}, "")
	if len(gen.InitialPassword) < 14 {
		t.Fatalf("随机密码长度应不小于最小长度：%d", len(gen.InitialPassword))
	}
	e.expect(200, "PUT", "/api/settings/security", with("pwdMinLen", 10), nil)

	// 登录失败锁定次数
	e.expect(200, "PUT", "/api/settings/security", with("loginFailLock", 3), nil)
	e.newUser("lock_me", []string{"r_oncall"}, "Lock@2026xyzab1")
	for i := 0; i < 2; i++ {
		e.expect(401, "POST", "/api/login", map[string]string{"login": "lock_me", "password": "bad"}, nil)
	}
	e.expect(429, "POST", "/api/login", map[string]string{"login": "lock_me", "password": "bad"}, nil)
	e.expect(200, "POST", "/api/users/lock_me/lock", map[string]any{"locked": false}, nil)
	e.expect(200, "PUT", "/api/settings/security", with("loginFailLock", 5), nil)

	// 会话超时：把会话的最近访问时间调到 6 分钟前，超时 5 分钟即失效
	e.expect(200, "PUT", "/api/settings/security", with("sessionTimeout", 5), nil)
	user := e.firstLogin("lock_me", "Lock@2026xyzab1", "Lock@2026xyzab2")
	user.expect(200, "GET", "/api/me", nil, nil)
	e.api.Sessions.mu.Lock()
	for _, s := range e.api.Sessions.m {
		if s.login == "lock_me" {
			s.seen = time.Now().Add(-6 * time.Minute)
		}
	}
	e.api.Sessions.mu.Unlock()
	user.expect(401, "GET", "/api/me", nil, nil)
	e.expect(200, "PUT", "/api/settings/security", with("sessionTimeout", 30), nil)

	// 密码有效期：超过天数后登录要求修改密码
	e.expect(200, "PUT", "/api/settings/security", with("pwdExpireDays", 30), nil)
	must(t, e.api.Store.Update(func(st *store.State) error {
		for i := range st.Users {
			if st.Users[i].Login == "lock_me" {
				st.Users[i].PwdAt = time.Now().AddDate(0, 0, -31)
			}
		}
		return nil
	}))
	var me map[string]any
	fresh := e.as("admin", "Admin@2026xyz") // 独立会话，避免覆盖 admin 的 cookie
	fresh.c.Jar, _ = cookiejar.New(nil)
	fresh.expect(200, "POST", "/api/login", map[string]string{"login": "lock_me", "password": "Lock@2026xyzab2"}, &me)
	if me["mustChange"] != true {
		t.Fatalf("密码过期后应要求修改：%v", me)
	}
	e.expect(200, "PUT", "/api/settings/security", with("pwdExpireDays", 0), nil)
	e.expect(200, "GET", "/api/clusters", nil, nil) // admin 的会话不受影响
}

func TestIPWhitelist(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	must(t, SetTrustedProxies("127.0.0.1"))
	t.Cleanup(func() { SetTrustedProxies("") })
	good := map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": false, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5}
	put := func(env *env, wl string) int {
		b := map[string]any{}
		for k, v := range good {
			b[k] = v
		}
		b["ipWhitelist"] = wl
		return env.call("PUT", "/api/settings/security", b, nil)
	}
	office := e.withIP("203.0.113.9")
	if c := put(office, "10.0.0.0/8"); c != 400 {
		t.Fatalf("白名单不含当前来源 IP 时应拒绝保存（否则会把自己锁在外面），得到 %d", c)
	}
	if c := put(office, "10.0.0.0/8, 203.0.113.0/24"); c != 200 {
		t.Fatalf("包含当前 IP 应可保存，得到 %d", c)
	}
	office.expect(200, "GET", "/api/me", nil, nil)
	e.withIP("198.51.100.7").expect(403, "GET", "/api/me", nil, nil)
	e.withIP("198.51.100.7").expect(403, "POST", "/api/login", map[string]string{"login": "admin", "password": "x"}, nil)
	e.withIP("10.1.2.3").expect(200, "GET", "/api/health", nil, nil)
	// 伪造 X-Forwarded-For 的多级代理：取最靠右的不可信地址
	e.withIP("198.51.100.7, 203.0.113.9").expect(200, "GET", "/api/me", nil, nil) // 最右是 203.0.113.9
	e.withIP("203.0.113.9, 198.51.100.7").expect(403, "GET", "/api/me", nil, nil) // 最右是 198.51.100.7（前面的是伪造）
	// 本机回环地址始终可访问（误锁后的恢复途径）
	e.expect(200, "GET", "/api/me", nil, nil)
	if c := put(e, ""); c != 200 {
		t.Fatalf("清空白名单失败：%d", c)
	}
	e.withIP("198.51.100.7").expect(200, "GET", "/api/me", nil, nil)

	// 不信任代理时忽略 X-Forwarded-For
	must(t, SetTrustedProxies(""))
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:5555"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if clientIP(r) != "192.0.2.1" {
		t.Fatalf("未配置可信代理时不应采信 X-Forwarded-For：%s", clientIP(r))
	}
	if SetTrustedProxies("not-an-ip") == nil {
		t.Fatal("非法的可信代理应报错")
	}
}

func TestSettingsNotifyBasicWatermark(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	smtp := startSMTP(t)

	// ---- 通知 / SMTP ----
	notify := map[string]any{"smtpHost": "127.0.0.1", "smtpPort": smtp.port(), "smtpSec": "none", "smtpSkipVerify": false, "smtpUser": "", "from": "sr-ops@example.com", "fromName": "平台",
		"smsUrl": "", "smsSign": "", "mergeMin": 5, "repeatMin": 60, "recover": true, "quiet": false, "quietFrom": "23:00", "quietTo": "08:00"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range notify {
			m[a] = b
		}
		m[k] = v
		return m
	}
	for k, v := range map[string]any{"smtpPort": 0, "smtpSec": "tls", "smtpHost": "bad host!", "from": "nope", "mergeMin": 99, "repeatMin": -1, "smsUrl": "ftp://x"} {
		e.expect(400, "PUT", "/api/settings/notify", with(k, v), nil)
	}
	e.expect(400, "PUT", "/api/settings/notify", func() map[string]any { m := with("quiet", true); m["quietFrom"] = "25:00"; return m }(), nil)
	e.expect(400, "PUT", "/api/settings/notify", func() map[string]any {
		m := with("quiet", true)
		m["quietFrom"], m["quietTo"] = "22:00", "22:00"
		return m
	}(), nil)

	// 测试邮件：使用表单里（尚未保存）的配置，发送到当前用户邮箱
	e.expect(200, "PUT", "/api/users/admin", map[string]any{"name": "系统管理员", "email": "admin@example.com", "roles": []string{"r_admin"}}, nil)
	e.expect(200, "POST", "/api/settings/notify/test-mail", notify, nil)
	if smtp.count() != 1 || !strings.Contains(smtp.last(), "To: admin@example.com") {
		t.Fatalf("应收到测试邮件：%q", smtp.last())
	}
	bad := with("smtpPort", 1)
	e.expect(502, "POST", "/api/settings/notify/test-mail", bad, nil)

	// 保存：密码加密保存，接口只返回“已设置”
	n2 := with("smtpUser", "ops")
	n2["smtpPassword"] = "TopSecret-Smtp-Pw"
	var no map[string]any
	e.expect(200, "PUT", "/api/settings/notify", n2, &no)
	if no["smtpPwd"] != true || no["smtpPassword"] != nil || no["smtpPwdEnc"] != nil {
		t.Fatalf("不应返回密码：%v", no)
	}
	raw, _ := os.ReadFile(filepath.Join(e.dir, "state.json"))
	if strings.Contains(string(raw), "TopSecret-Smtp-Pw") {
		t.Fatal("state.json 中不应有明文 SMTP 密码")
	}
	// 清空认证账号：同时清除已保存的密码
	e.expect(200, "PUT", "/api/settings/notify", notify, &no)
	if no["smtpPwd"] != false {
		t.Fatalf("清空账号后应清除密码：%v", no)
	}

	// 通知方式「邮件」：定时巡检结果经平台 SMTP 发送
	cl := e.conn()
	cl["name"], cl["env"], cl["owner"] = "prod-test", "生产", "admin"
	e.expect(200, "POST", "/api/clusters", cl, nil)
	e.expect(200, "PUT", "/api/settings/basic", map[string]any{"name": "运维平台", "url": "https://ops.example.com/", "proxy": "", "noProxy": "", "interval": 15, "reportDays": 90, "auditDays": 180}, nil)
	sched := map[string]any{"enabled": true, "every": "daily", "at": "10:00", "notify": map[string]any{"type": "邮件", "url": "ops@example.com; dba@example.com", "when": "always"}}
	e.expect(200, "PUT", "/api/clusters/prod-test/inspect/schedule", sched, nil)
	e.expect(400, "PUT", "/api/clusters/prod-test/inspect/schedule", map[string]any{"enabled": true, "every": "daily", "at": "10:00", "notify": map[string]any{"type": "邮件", "url": "nope", "when": "always"}}, nil)
	var sc model.Schedule
	e.expect(200, "GET", "/api/clusters/prod-test/inspect/schedule", nil, &sc)
	if sc.Notify == nil || sc.Notify.URLMasked != "ops@example.com, dba@example.com" {
		t.Fatalf("邮件通知配置不对：%+v", sc.Notify)
	}
	before := smtp.count()
	e.expect(200, "POST", "/api/clusters/prod-test/inspect/notify-test", map[string]any{"type": "邮件", "when": "always"}, nil)
	if smtp.count() != before+1 || !strings.Contains(smtp.last(), "To: ops@example.com, dba@example.com") {
		t.Fatalf("应向收件人发送邮件：%q", smtp.last())
	}
	var stored model.Schedule
	e.api.Store.View(func(st *store.State) { stored = st.Schedules["prod-test"] })
	c, _ := e.api.Store.Cluster("prod-test")
	e.api.Runner.RunScheduled(context.Background(), c, stored)
	if smtp.count() != before+2 {
		t.Fatal("定时巡检应发送邮件")
	}

	// ---- 基础 ----
	basic := map[string]any{"name": "数据平台运维中心", "url": "https://ops.example.com", "proxy": "http://user:p4ss@proxy.local:3128", "noProxy": "10.0.0.0/8", "interval": 30, "reportDays": 60, "auditDays": 365}
	badB := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range basic {
			m[a] = b
		}
		m[k] = v
		return m
	}
	for k, v := range map[string]any{"name": "", "interval": 20, "reportDays": 3, "auditDays": 30, "url": "ftp://x", "proxy": "proxy.local"} {
		e.expect(400, "PUT", "/api/settings/basic", badB(k, v), nil)
	}
	var bo map[string]any
	e.expect(200, "PUT", "/api/settings/basic", basic, &bo)
	if bo["proxy"] != "http://user:****@proxy.local:3128" {
		t.Fatalf("代理密码应隐藏：%v", bo["proxy"])
	}
	// 回传隐藏后的地址，密码沿用
	bo["interval"] = 60
	e.expect(200, "PUT", "/api/settings/basic", bo, nil)
	if got := e.api.cfg().Basic.Proxy; got != "http://user:p4ss@proxy.local:3128" {
		t.Fatalf("应沿用原来的代理密码：%s", got)
	}
	if e.api.cfg().Basic.Interval != 60 || e.api.Mgr.Sampler == nil {
		t.Fatal("采集间隔未保存")
	}
	audit, _ := os.ReadFile(filepath.Join(e.dir, "audit.jsonl"))
	if strings.Contains(string(audit), "p4ss") || strings.Contains(string(audit), "TopSecret") {
		t.Fatal("审计日志中不应出现代理或 SMTP 密码")
	}
	e.expect(200, "PUT", "/api/settings/basic", map[string]any{"name": "数据平台运维中心", "url": "", "proxy": "", "noProxy": "", "interval": 15, "reportDays": 90, "auditDays": 180}, nil)

	// 所有登录用户都能读到名称、安全展示策略与水印；详细设置只有 settings:view 能看
	opsRes := e.newUser("ops_b", []string{"r_ops"}, "")
	ob := e.firstLogin("ops_b", opsRes.InitialPassword, "Ops@2026xyzabc1")
	var pub map[string]any
	ob.expect(200, "GET", "/api/settings", nil, &pub)
	if pub["name"] != "数据平台运维中心" || pub["notify"] != nil || pub["basic"] != nil || pub["watermark"] == nil {
		t.Fatalf("普通用户只应看到公开设置：%v", pub)
	}
	if _, has := pub["security"].(map[string]any)["ipWhitelist"]; has {
		t.Fatal("普通用户不应看到 IP 白名单")
	}

	// ---- 水印：仅超级管理员 ----
	wm := map[string]any{"on": true, "text": "{name} {login}", "text2": "内部资料", "size": 16, "opacity": 0.12, "angle": -20, "gap": 200, "color": "#d23b3b"}
	ob.expect(403, "PUT", "/api/settings/watermark", wm, nil)
	for k, v := range map[string]any{"text": "", "size": 5, "opacity": 0.9, "angle": 99, "gap": 10, "color": "red"} {
		m := map[string]any{}
		for a, b := range wm {
			m[a] = b
		}
		m[k] = v
		e.expect(400, "PUT", "/api/settings/watermark", m, nil)
	}
	e.expect(200, "PUT", "/api/settings/watermark", wm, nil)
	ob.expect(200, "GET", "/api/settings", nil, &pub)
	if pub["watermark"].(map[string]any)["color"] != "#d23b3b" {
		t.Fatalf("水印应对所有用户生效：%v", pub["watermark"])
	}

	// ---- 越权：没有 setting.* 权限的用户 ----
	ob.expect(403, "PUT", "/api/settings/security", map[string]any{}, nil)
	ob.expect(403, "PUT", "/api/settings/notify", notify, nil)
	ob.expect(403, "PUT", "/api/settings/basic", basic, nil)
	ob.expect(403, "POST", "/api/settings/notify/test-mail", notify, nil)
}

func TestCreateUserMail(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	// 没有配置邮件服务：用户照常创建，返回 mailError
	var res map[string]any
	e.expect(200, "POST", "/api/users", map[string]any{"login": "mail_a", "name": "甲", "email": "a@example.com", "roles": []string{"r_oncall"}, "sendMail": true}, &res)
	if res["mailError"] == nil || res["initialPassword"] == nil {
		t.Fatalf("未配置邮件服务应返回 mailError，且仍显示初始密码：%v", res)
	}
	smtp := startSMTP(t)
	e.expect(200, "PUT", "/api/settings/notify", map[string]any{"smtpHost": "127.0.0.1", "smtpPort": smtp.port(), "smtpSec": "none", "from": "sr-ops@example.com", "fromName": "平台",
		"mergeMin": 5, "repeatMin": 60, "recover": true, "quietFrom": "23:00", "quietTo": "08:00"}, nil)
	e.expect(200, "POST", "/api/users", map[string]any{"login": "mail_b", "name": "乙", "email": "b@example.com", "roles": []string{"r_oncall"}, "sendMail": true}, &res)
	if res["mailSent"] != "b@example.com" || smtp.count() != 1 {
		t.Fatalf("应发送邮件：%v", res)
	}
	pw := res["initialPassword"].(string)
	// 邮件正文是 base64：解码后含账号与密码
	_, body, _ := strings.Cut(smtp.last(), "\n\n")
	dec := decodeB64(strings.ReplaceAll(body, "\n", ""))
	if !strings.Contains(dec, "mail_b") || !strings.Contains(dec, pw) {
		t.Fatalf("邮件应含账号与密码：%q", dec)
	}
	e.expect(200, "POST", "/api/users/mail_b/reset", map[string]any{"sendMail": true}, &res)
	if res["mailSent"] != "b@example.com" || smtp.count() != 2 {
		t.Fatalf("重置后应发送邮件：%v", res)
	}
	audit, _ := os.ReadFile(filepath.Join(e.dir, "audit.jsonl"))
	if strings.Contains(string(audit), pw) || strings.Contains(string(audit), res["password"].(string)) {
		t.Fatal("审计日志中不应出现密码")
	}
}

func TestAuditQueryExportPrune(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	e.newUser("dba_x", []string{"r_dba"}, "Dba@2026xyzabc1")
	e.expect(200, "PUT", "/api/roles/r_dba", map[string]any{"clusters": []string{}}, nil)
	e.firstLogin("dba_x", "Dba@2026xyzabc1", "Dba@2026xyzabc2")
	for i := 0; i < 5; i++ {
		e.call("POST", "/api/login", map[string]string{"login": "nobody", "password": "wrong"}, nil)
	}

	type page struct {
		Items []auditOut
		More  bool
	}
	var p page
	e.expect(200, "GET", "/api/audit?limit=3", nil, &p)
	if len(p.Items) != 3 || !p.More || p.Items[0].At.Before(p.Items[1].At) {
		t.Fatalf("应按时间倒序分页：%+v", p)
	}
	var p2 page
	e.expect(200, "GET", "/api/audit?limit=3&offset=3", nil, &p2)
	if len(p2.Items) == 0 || p2.Items[0].At.After(p.Items[2].At) {
		t.Fatalf("第二页应接在第一页之后：%+v", p2)
	}
	e.expect(200, "GET", "/api/audit?result=失败&limit=100", nil, &p)
	if len(p.Items) != 5 {
		t.Fatalf("应有 5 条登录失败：%d", len(p.Items))
	}
	for _, it := range p.Items {
		if it.ResClass != "失败" || it.Menu != "登录" || it.Code != "auth.login" {
			t.Fatalf("筛选结果不对：%+v", it)
		}
	}
	e.expect(200, "GET", "/api/audit?q=新建用户&user=admin", nil, &p)
	if len(p.Items) != 1 || p.Items[0].Menu != "用户管理" || p.Items[0].Code != "user.create" {
		t.Fatalf("按关键字与用户筛选不对：%+v", p.Items)
	}
	e.expect(200, "GET", "/api/audit?from="+time.Now().Format("2006-01-02")+"&to="+time.Now().Format("2006-01-02"), nil, &p)
	if len(p.Items) == 0 {
		t.Fatal("包含今天的日期范围应有记录")
	}
	e.expect(200, "GET", "/api/audit?to=2000-01-01", nil, &p)
	if len(p.Items) != 0 {
		t.Fatal("早于所有记录的范围应为空")
	}
	e.expect(400, "GET", "/api/audit?result=乱写", nil, nil)
	e.expect(400, "GET", "/api/audit?from=昨天", nil, nil)

	// 数据范围受限的用户：看不到别人的平台级记录，只能看到自己的
	dba := e.as("dba_x", "Dba@2026xyzabc2")
	dba.expect(200, "GET", "/api/audit?limit=500", nil, &p)
	for _, it := range p.Items {
		if it.User != "dba_x" {
			t.Fatalf("范围受限用户不应看到其他用户的平台级记录：%+v", it)
		}
	}
	dba.expect(403, "GET", "/api/audit/export", nil, nil) // 没有 audit.export

	// 导出 CSV
	resp, err := e.c.Get(e.srv.URL + "/api/audit/export?result=失败")
	must(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "\xef\xbb\xbf") || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("应导出带 BOM 的 CSV 附件：%q", resp.Header)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(body), "\xef\xbb\xbf"))).ReadAll()
	if err != nil || len(rows) != 6 || rows[0][0] != "时间" || rows[1][4] != "登录" {
		t.Fatalf("CSV 内容不对：%v %v", err, rows)
	}
	if csvCell("=1+1") != "'=1+1" || csvCell("@SUM(A1)") != "'@SUM(A1)" || csvCell("正常") != "正常" || csvCell("") != "" {
		t.Fatal("CSV 公式注入防护不对")
	}
	var after page
	e.expect(200, "GET", "/api/audit?code=audit.export&result=成功", nil, &after)
	if len(after.Items) != 1 {
		t.Fatal("导出本身应记录审计")
	}

	// 清理：只删除早于保留期的记录
	f := filepath.Join(e.dir, "audit.jsonl")
	old := time.Now().AddDate(0, 0, -400)
	line, _ := json.Marshal(store.AuditEntry{At: old, User: "ghost", Code: "auth.login", Act: "很久以前的操作", Result: "成功"})
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o600)
	fh.Write(append(line, '\n'))
	fh.Close()
	e.expect(200, "GET", "/api/audit?user=ghost", nil, &p)
	if len(p.Items) != 1 {
		t.Fatalf("应能查到旧记录：%d", len(p.Items))
	}
	n, err := e.api.Store.PruneAudit(time.Now().AddDate(0, 0, -180))
	if err != nil || n != 1 {
		t.Fatalf("应只删除 1 条超过保留期的记录：%d %v", n, err)
	}
	e.expect(200, "GET", "/api/audit?user=ghost", nil, &p)
	if len(p.Items) != 0 {
		t.Fatal("旧记录应已被清理")
	}
	e.expect(200, "GET", "/api/audit?limit=500", nil, &p)
	if len(p.Items) < 10 {
		t.Fatalf("保留期内的记录不应被删除：%d", len(p.Items))
	}
}

func TestDualReview(t *testing.T) {
	e := newEnv(t, fakesr.ModeSharedNothing, "healthy")
	e.login()
	sec := func(on bool) {
		e.expect(200, "PUT", "/api/settings/security", map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": on, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}, nil)
	}
	e.newUser("admin2", []string{"r_admin"}, "Admin2@2026xyzab")
	admin2 := e.firstLogin("admin2", "Admin2@2026xyzab", "Admin2@2026xyzab2")
	e.newUser("victim", []string{"r_oncall"}, "Victim@2026xyzab")
	e.newUser("victim2", []string{"r_oncall"}, "Victim@2026xyzab")
	e.newUser("victim3", []string{"r_oncall"}, "Victim@2026xyzab")
	cl := e.conn()
	cl["name"], cl["env"], cl["owner"] = "prod-test", "生产", "admin"
	e.expect(200, "POST", "/api/clusters", cl, nil)

	// 未开启：直接执行
	e.expect(200, "DELETE", "/api/users/victim3", nil, nil)

	sec(true)
	var pend map[string]any
	e.expect(202, "DELETE", "/api/users/victim", nil, &pend)
	if pend["pending"] != true || pend["reviewId"] == nil {
		t.Fatalf("应返回待复核：%v", pend)
	}
	rid := pend["reviewId"].(string)
	if _, found := e.api.Store.User("victim"); !found {
		t.Fatal("提交复核后用户不应被删除")
	}
	e.expect(409, "DELETE", "/api/users/victim", nil, nil) // 重复提交

	type rl struct {
		Items   []reviewOut
		Pending int
	}
	var list rl
	e.expect(200, "GET", "/api/reviews", nil, &list)
	if len(list.Items) != 1 || list.Pending != 1 || list.Items[0].CanApprove || !list.Items[0].Mine || list.Items[0].Menu != "用户管理" {
		t.Fatalf("申请人自己看到的待复核不对：%+v", list)
	}
	e.expect(403, "POST", "/api/reviews/"+rid+"/approve", nil, nil) // 不能复核自己的
	admin2.expect(200, "GET", "/api/reviews", nil, &list)
	if !list.Items[0].CanApprove {
		t.Fatal("另一位超级管理员应可批准")
	}

	// 没有该权限的用户不能复核
	e.newUser("aud_y", []string{"r_audit"}, "Audit@2026xyzab1")
	aud := e.firstLogin("aud_y", "Audit@2026xyzab1", "Audit@2026xyzab2")
	aud.expect(200, "GET", "/api/reviews", nil, &list)
	if list.Items[0].CanApprove {
		t.Fatal("审计员没有 user.delete，不能批准")
	}
	aud.expect(403, "POST", "/api/reviews/"+rid+"/approve", nil, nil)
	aud.expect(403, "POST", "/api/reviews/"+rid+"/reject", map[string]any{}, nil)

	// 批准：以申请人的身份执行
	var ap map[string]any
	admin2.expect(200, "POST", "/api/reviews/"+rid+"/approve", nil, &ap)
	if ap["ok"] != true || ap["status"] != "approved" {
		t.Fatalf("批准失败：%v", ap)
	}
	if _, found := e.api.Store.User("victim"); found {
		t.Fatal("批准后用户应被删除")
	}
	admin2.expect(409, "POST", "/api/reviews/"+rid+"/approve", nil, nil) // 不能重复处理
	var p struct{ Items []auditOut }
	e.expect(200, "GET", "/api/audit?code=user.delete&limit=10", nil, &p)
	var sawPending, sawExec bool
	for _, it := range p.Items {
		sawPending = sawPending || (it.ResClass == "待复核" && it.User == "admin")
		sawExec = sawExec || (it.ResClass == "成功" && it.User == "admin" && strings.Contains(it.Act, "删除用户 victim"))
	}
	if !sawPending || !sawExec {
		t.Fatalf("审计应记录提交（待复核）与以申请人身份执行：%+v", p.Items)
	}
	e.expect(200, "GET", "/api/audit?code=review.approve&result=成功", nil, &p)
	if len(p.Items) != 1 || p.Items[0].User != "admin2" {
		t.Fatalf("批准动作应记录复核人：%+v", p.Items)
	}
	e.expect(200, "GET", "/api/audit?code=review.approve&result=失败", nil, &p)
	if len(p.Items) != 3 {
		t.Fatalf("自批、无权限、重复批准都应记录为失败：%d", len(p.Items))
	}

	// 驳回
	e.expect(202, "DELETE", "/api/roles/r_oncall", nil, &pend) // 有成员，批准也会失败；先驳回
	rid2 := pend["reviewId"].(string)
	admin2.expect(200, "POST", "/api/reviews/"+rid2+"/reject", map[string]any{"note": "还有成员在用"}, nil)
	e.expect(200, "GET", "/api/reviews", nil, &list)
	if list.Items[0].Status != "rejected" || list.Items[0].Note != "还有成员在用" || list.Items[0].Reviewer != "admin2" {
		t.Fatalf("驳回状态不对：%+v", list.Items[0])
	}

	// 批准后执行失败（有成员）：状态为 failed，原因可见
	e.expect(202, "DELETE", "/api/roles/r_oncall", nil, &pend)
	admin2.expect(200, "POST", "/api/reviews/"+pend["reviewId"].(string)+"/approve", nil, &ap)
	if ap["ok"] != false || ap["status"] != "failed" || !strings.Contains(ap["note"].(string), "成员") {
		t.Fatalf("执行失败应给出原因：%v", ap)
	}

	// 集群级申请：范围受限的复核人不能批准；申请人权限被收回后批准失败
	e.expect(202, "DELETE", "/api/clusters/prod-test", nil, &pend)
	rid3 := pend["reviewId"].(string)
	e.newUser("ops_scoped", []string{"r_ops"}, "Scoped@2026xyzab")
	e.expect(200, "PUT", "/api/roles/r_ops", map[string]any{"clusters": []string{}}, nil)
	sc := e.firstLogin("ops_scoped", "Scoped@2026xyzab", "Scoped@2026xyzab2")
	sc.expect(403, "POST", "/api/reviews/"+rid3+"/approve", nil, nil) // 没有 cluster.delete 且不在范围
	e.expect(200, "PUT", "/api/roles/r_ops", map[string]any{"clusters": []string{"*"}}, nil)
	// 撤回
	e.expect(200, "POST", "/api/reviews/"+rid3+"/withdraw", nil, nil)
	admin2.expect(409, "POST", "/api/reviews/"+rid3+"/approve", nil, nil)
	admin2.expect(403, "POST", "/api/reviews/"+rid2+"/withdraw", nil, nil)

	// 申请有效期 24 小时
	e.expect(202, "DELETE", "/api/clusters/prod-test", nil, &pend)
	rid4 := pend["reviewId"].(string)
	must(t, e.api.Store.Update(func(st *store.State) error {
		for i := range st.Reviews {
			if st.Reviews[i].ID == rid4 {
				st.Reviews[i].CreatedAt = time.Now().Add(-25 * time.Hour)
			}
		}
		return nil
	}))
	admin2.expect(409, "POST", "/api/reviews/"+rid4+"/approve", nil, nil)
	if _, err := e.api.Store.Cluster("prod-test"); err != nil {
		t.Fatal("过期申请不应被执行")
	}

	// 申请人的权限在批准前被收回：批准时重新校验，不会越权执行
	var cdel roleOut
	e.expect(200, "POST", "/api/roles", map[string]any{"name": "集群移除", "perms": []string{"clusters:view", "cluster.delete"}, "clusters": []string{"*"}}, &cdel)
	e.newUser("req_z", []string{cdel.ID}, "Req@2026xyzabcd1")
	req := e.firstLogin("req_z", "Req@2026xyzabcd1", "Req@2026xyzabcd2")
	req.expect(202, "DELETE", "/api/clusters/prod-test", nil, &pend)
	e.expect(200, "PUT", "/api/roles/"+cdel.ID, map[string]any{"perms": []string{"clusters:view"}}, nil)
	admin2.expect(200, "POST", "/api/reviews/"+pend["reviewId"].(string)+"/approve", nil, &ap)
	if ap["ok"] != false || !strings.Contains(ap["note"].(string), "没有该操作的权限") {
		t.Fatalf("申请人已无权限，批准时应失败：%v", ap)
	}
	if _, err := e.api.Store.Cluster("prod-test"); err != nil {
		t.Fatal("申请人无权限时集群不应被移除")
	}

	// 关闭复核后恢复直接执行
	sec(false)
	e.expect(200, "DELETE", "/api/clusters/prod-test", nil, nil)
}

func decodeB64(s string) string {
	b, _ := base64.StdEncoding.DecodeString(s)
	return string(b)
}
