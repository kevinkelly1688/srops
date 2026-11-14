// @author: kevinkelly
// @license: Apache Licence
// @file: settings.go
// @time: 2026/10/28

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/inspect"
	"starrocks-ops/internal/mail"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/notify"
	"starrocks-ops/internal/secure"
	"starrocks-ops/internal/sms"
	"starrocks-ops/internal/store"
)

// cfg 返回当前平台设置。
func (s *Server) cfg() model.Settings { return s.Store.Settings() }

// applySettings 把设置应用到运行中的组件：出网代理、指标采样间隔、巡检报告保留期。
func (s *Server) applySettings() {
	c := s.cfg()
	notify.SetProxy(c.Basic.Proxy, c.Basic.NoProxy)
	s.Mgr.Sampler.SetInterval(c.Basic.Interval)
	inspect.SetKeepReports(c.Basic.ReportDays)
}

// ---------- 来源 IP / 白名单 ----------

var trustedProxies []*net.IPNet

// SetTrustedProxies 设置可信反向代理（逗号分隔的 IP / CIDR）。只有来自这些地址的请求，才采信 X-Forwarded-For。
func SetTrustedProxies(list string) error {
	var out []*net.IPNet
	for _, t := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' }) {
		n, err := parseNet(t)
		if err != nil {
			return fmt.Errorf("可信代理 %q 格式不正确", t)
		}
		out = append(out, n)
	}
	trustedProxies = out
	return nil
}

func parseNet(t string) (*net.IPNet, error) {
	if strings.Contains(t, "/") {
		_, n, err := net.ParseCIDR(t)
		return n, err
	}
	ip := net.ParseIP(t)
	if ip == nil {
		return nil, fmt.Errorf("bad ip")
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

func isTrusted(ip net.IP) bool {
	for _, n := range trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP 返回请求的来源 IP；来自可信代理时取 X-Forwarded-For 中最靠右的不可信地址。
func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		h = r.RemoteAddr
	}
	if len(trustedProxies) == 0 {
		return h
	}
	if ip := net.ParseIP(h); ip == nil || !isTrusted(ip) {
		return h
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if ip := net.ParseIP(p); ip != nil && (!isTrusted(ip) || i == 0) {
			return p
		}
	}
	return h
}

// parseWhitelist 解析白名单（逗号 / 空白分隔的 IP 或 CIDR）。
func parseWhitelist(s string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, t := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\n' }) {
		n, err := parseNet(t)
		if err != nil {
			return nil, fmt.Errorf("白名单项 %q 不是合法的 IP 或 CIDR", t)
		}
		out = append(out, n)
	}
	return out, nil
}

type whitelist struct {
	mu   sync.Mutex
	key  string
	nets []*net.IPNet
}

// allowed 判断来源 IP 是否被放行。白名单为空时不限制；本机回环地址始终放行（本机控制台是误锁后的恢复途径）。
func (w *whitelist) allowed(raw, ip string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	if p.IsLoopback() {
		return true
	}
	w.mu.Lock()
	if w.key != raw {
		w.nets, _ = parseWhitelist(raw)
		w.key = raw
	}
	nets := w.nets
	w.mu.Unlock()
	for _, n := range nets {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

func (s *Server) ipGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw := s.cfg().Security.IPWhitelist; raw != "" && !s.wl.allowed(raw, clientIP(r)) {
			http.Error(w, "访问被拒绝：来源 IP 不在平台设置的访问白名单内", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- 读取 ----------

var (
	timeRe     = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	smtpHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	colorRe    = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

type notifyOut struct {
	model.NotifySettings
	SMTPPwd      bool `json:"smtpPwd"`      // 是否已设置密码（不返回密码本身，也不返回密文）
	SMSSecretSet bool `json:"smsSecretSet"` // 是否已设置短信密钥
}

func notifyView(n model.NotifySettings) notifyOut {
	set, sms := n.SMTPPwdEnc != "", n.SMSSecretEnc != ""
	n.SMTPPwdEnc, n.SMSSecretEnc = "", ""
	return notifyOut{NotifySettings: n, SMTPPwd: set, SMSSecretSet: sms}
}

// maskProxy 隐藏代理地址中的密码。
func maskProxy(p string) string {
	u, err := url.Parse(p)
	if err != nil || u.User == nil {
		return p
	}
	if _, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), "****")
	}
	return strings.Replace(u.String(), "%2A%2A%2A%2A", "****", 1)
}

// restoreProxy 编辑时代理密码显示为 ****，提交后沿用原来的密码。
func restoreProxy(in, old string) string {
	if !strings.Contains(in, ":****@") {
		return in
	}
	ou, err := url.Parse(old)
	if err != nil || ou.User == nil {
		return in
	}
	pw, _ := ou.User.Password()
	return strings.Replace(in, ":****@", ":"+url.QueryEscape(pw)+"@", 1)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, u *model.User) {
	c := s.cfg()
	acc := accessOf(r)
	out := map[string]any{
		"name": c.Basic.Name, "url": c.Basic.URL, "watermark": c.Watermark, "ip": clientIP(r),
		"security": map[string]any{"noPermBtn": c.Security.NoPermBtn, "highRiskConfirm": c.Security.HighRiskConfirm, "dualReview": c.Security.DualReview,
			"sessionTimeout": c.Security.SessionTimeout, "pwdMinLen": c.Security.PwdMinLen},
	}
	if acc.Can("settings:view") {
		b := c.Basic
		b.Proxy = maskProxy(b.Proxy)
		out["security"] = c.Security
		out["basic"] = b
		out["notify"] = notifyView(c.Notify)
	}
	ok(w, out)
}

// ---------- 保存 ----------

// changes 列出两个设置段落中变化的字段（不含加密字段，代理密码已隐藏），用于审计。
func changes(oldV, newV any) string {
	flat := func(v any) map[string]any {
		b, _ := json.Marshal(v)
		m := map[string]any{}
		json.Unmarshal(b, &m)
		return m
	}
	a, b := flat(oldV), flat(newV)
	var keys []string
	for k := range b {
		if !strings.HasSuffix(k, "Enc") && fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		ov, nv := fmt.Sprint(a[k]), fmt.Sprint(b[k])
		if k == "proxy" {
			ov, nv = maskProxy(ov), maskProxy(nv)
		}
		out = append(out, fmt.Sprintf("%s: %s → %s", k, ov, nv))
	}
	return strings.Join(out, "\n")
}

// saveSetting 在写锁内读取、修改并保存设置；mutate 返回错误则不保存。
func (s *Server) saveSetting(mutate func(c *model.Settings) error) (before, after model.Settings, err error) {
	err = s.Store.Update(func(st *store.State) error {
		cur := model.DefaultSettings()
		if st.Settings != nil {
			cur = *st.Settings
		}
		before = cur
		if err := mutate(&cur); err != nil {
			return err
		}
		st.Settings = &cur
		after = cur
		return nil
	})
	if err == nil {
		s.applySettings()
	}
	return
}

func inRange(v, lo, hi int) bool { return v >= lo && v <= hi }

func (s *Server) putSecurity(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in model.SecuritySettings
	if !decode(w, r, &in) {
		return
	}
	in.IPWhitelist = strings.TrimSpace(in.IPWhitelist)
	var b, a model.Settings
	err := func() error {
		switch {
		case in.NoPermBtn != "disable" && in.NoPermBtn != "hide":
			return bad("无权限按钮显示方式须为 disable 或 hide")
		case !inRange(in.SessionTimeout, 5, 480):
			return bad("会话超时须为 5–480 分钟")
		case !inRange(in.PwdMinLen, 8, 32):
			return bad("密码最小长度须为 8–32 位")
		case !inRange(in.PwdExpireDays, 0, 3650):
			return bad("密码过期天数须为 0–3650（0 = 永不过期）")
		case !inRange(in.LoginFailLock, 3, 20):
			return bad("登录失败锁定次数须为 3–20")
		case len(in.IPWhitelist) > 2000:
			return bad("IP 白名单过长")
		}
		if in.IPWhitelist != "" {
			nets, err := parseWhitelist(in.IPWhitelist)
			if err != nil {
				return bad(err.Error())
			}
			me := net.ParseIP(clientIP(r))
			okme := me != nil && me.IsLoopback()
			for _, n := range nets {
				okme = okme || (me != nil && n.Contains(me))
			}
			if !okme {
				return bad(fmt.Sprintf("您当前的访问 IP（%s）不在新的白名单内，保存后将无法访问平台。请把它加入白名单", clientIP(r)))
			}
		}
		var err error
		b, a, err = s.saveSetting(func(c *model.Settings) error { c.Security = in; return nil })
		return err
	}()
	s.auditSQL(r, u, "setting.security", "修改安全策略", changes(b.Security, a.Security), err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, a.Security)
}

type notifyIn struct {
	model.NotifySettings
	SMTPPassword      string `json:"smtpPassword"`
	ClearSMTPPassword bool   `json:"clearSmtpPassword"`
	SMSSecret         string `json:"smsSecret"`
	ClearSMSSecret    bool   `json:"clearSmsSecret"`
}

// checkNotify 校验通知设置。
func checkNotify(n model.NotifySettings) error {
	n.SMTPHost, n.SMTPUser, n.From, n.FromName = strings.TrimSpace(n.SMTPHost), strings.TrimSpace(n.SMTPUser), strings.TrimSpace(n.From), strings.TrimSpace(n.FromName)
	switch {
	case n.SMTPHost != "" && !smtpHostRe.MatchString(n.SMTPHost):
		return bad("邮件服务器地址格式不正确")
	case !inRange(n.SMTPPort, 1, 65535):
		return bad("SMTP 端口须为 1–65535")
	case n.SMTPSec != "SSL" && n.SMTPSec != "STARTTLS" && n.SMTPSec != "none":
		return bad("加密方式须为 SSL / STARTTLS / none")
	case rlen(n.SMTPUser) > 100 || strings.ContainsAny(n.SMTPUser, "\r\n"):
		return bad("认证账号不合法")
	case n.SMTPHost != "" && !mail.ValidAddr(n.From):
		return bad("发件人地址格式不正确")
	case rlen(n.FromName) > 50 || strings.ContainsAny(n.FromName, "\r\n"):
		return bad("发件人名称不合法")
	case rlen(n.SMSSign) > 30:
		return bad("短信签名最多 30 个字")
	case n.SMSProvider != "" && !contains(sms.Providers, n.SMSProvider):
		return bad("短信服务商须为 http / aliyun / tencent")
	case !smsKeyRe.MatchString(n.SMSKeyID) || !smsKeyRe.MatchString(n.SMSTemplate):
		return bad("短信 AccessKey / 模板只能包含字母、数字和 . _ - 且不超过 128 位")
	case n.SMSAppID != "" && !smsAppRe.MatchString(n.SMSAppID):
		return bad("SdkAppId 只能是数字")
	case n.SMSRegion != "" && !smsRegionRe.MatchString(n.SMSRegion):
		return bad("地域格式不正确，例如 ap-guangzhou")
	case !inRange(n.MergeMin, 0, 60):
		return bad("告警合并窗口须为 0–60 分钟")
	case !inRange(n.RepeatMin, 0, 1440):
		return bad("重复提醒间隔须为 0–1440 分钟")
	}
	if n.SMSURL != "" {
		if err := notify.ValidURL(n.SMSURL); err != nil {
			return bad("短信网关" + err.Error())
		}
	}
	if _, err := parseSMSParams(n.SMSParams); err != nil {
		return err
	}
	if n.Quiet && (!timeRe.MatchString(n.QuietFrom) || !timeRe.MatchString(n.QuietTo) || n.QuietFrom == n.QuietTo) {
		return bad("请设置有效的免打扰起止时间（HH:MM，起止不能相同）")
	}
	return nil
}

func (s *Server) putNotify(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in notifyIn
	if !decode(w, r, &in) {
		return
	}
	var b, a model.Settings
	err := checkNotify(in.NotifySettings)
	if err == nil {
		var enc string
		if in.SMTPPassword != "" {
			if enc, err = s.Box.Encrypt(in.SMTPPassword); err != nil {
				err = apiErr{http.StatusInternalServerError, err.Error()}
			}
		}
		var smsEnc string
		if err == nil && in.SMSSecret != "" {
			if smsEnc, err = s.Box.Encrypt(in.SMSSecret); err != nil {
				err = apiErr{http.StatusInternalServerError, err.Error()}
			}
		}
		if err == nil {
			b, a, err = s.saveSetting(func(c *model.Settings) error {
				n := in.NotifySettings
				n.SMTPHost, n.SMTPUser, n.From, n.FromName = strings.TrimSpace(n.SMTPHost), strings.TrimSpace(n.SMTPUser), strings.TrimSpace(n.From), strings.TrimSpace(n.FromName)
				n.SMSURL, n.SMSSign, n.SMSKeyID, n.SMSTemplate = strings.TrimSpace(n.SMSURL), strings.TrimSpace(n.SMSSign), strings.TrimSpace(n.SMSKeyID), strings.TrimSpace(n.SMSTemplate)
				n.SMTPPwdEnc, n.SMSSecretEnc = c.Notify.SMTPPwdEnc, c.Notify.SMSSecretEnc
				switch {
				case in.ClearSMTPPassword || n.SMTPUser == "":
					n.SMTPPwdEnc = ""
				case enc != "":
					n.SMTPPwdEnc = enc
				}
				switch {
				case in.ClearSMSSecret:
					n.SMSSecretEnc = ""
				case smsEnc != "":
					n.SMSSecretEnc = smsEnc
				}
				c.Notify = n
				return nil
			})
		}
	}
	detail := changes(b.Notify, a.Notify)
	if in.SMTPPassword != "" {
		detail = strings.TrimSpace(detail + "\nsmtpPassword: 已更新")
	}
	if in.SMSSecret != "" {
		detail = strings.TrimSpace(detail + "\nsmsSecret: 已更新")
	}
	s.auditSQL(r, u, "setting.notify", "修改通知设置", detail, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, notifyView(a.Notify))
}

// mailConfig 把设置转为发信配置（解密密码）。
func (s *Server) mailConfig(n model.NotifySettings) (mail.Config, error) {
	c := mail.Config{Host: n.SMTPHost, Port: n.SMTPPort, Sec: n.SMTPSec, SkipVerify: n.SMTPSkipVerify, User: n.SMTPUser, From: n.From, FromName: n.FromName}
	if n.SMTPPwdEnc != "" {
		pw, err := s.Box.Decrypt(n.SMTPPwdEnc)
		if err != nil {
			return c, fmt.Errorf("邮件密码解密失败：%v", err)
		}
		c.Password = pw
	}
	return c, nil
}

// SendMail 用平台设置中的 SMTP 发送邮件。
func (s *Server) SendMail(ctx context.Context, to []string, subject, body string) error {
	c, err := s.mailConfig(s.cfg().Notify)
	if err != nil {
		return err
	}
	return mail.Send(ctx, c, to, subject, body)
}

// testMail 用表单中的（尚未保存的）SMTP 配置发送测试邮件；密码留空时沿用已保存的。
func (s *Server) testMail(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in notifyIn
	if !decode(w, r, &in) {
		return
	}
	to := u.Email
	err := checkNotify(in.NotifySettings)
	if err == nil && (in.SMTPHost == "" || !mail.ValidAddr(to)) {
		err = bad("请先填写邮件服务器；当前用户的邮箱也需要有效")
	}
	if err == nil {
		n := in.NotifySettings
		n.SMTPPwdEnc = s.cfg().Notify.SMTPPwdEnc
		var c mail.Config
		if c, err = s.mailConfig(n); err == nil {
			if in.ClearSMTPPassword || n.SMTPUser == "" {
				c.Password = ""
			}
			if in.SMTPPassword != "" {
				c.Password = in.SMTPPassword
			}
			ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
			defer cancel()
			err = mail.Send(ctx, c, []string{to}, "【StarRocks 运维平台】测试邮件",
				fmt.Sprintf("这是一封测试邮件，由 %s 在「平台设置 → 通知」中发送。\n收到这封邮件说明邮件服务配置正确。\n\n时间：%s", u.Login, time.Now().Format("2006-01-02 15:04:05")))
			if err != nil {
				err = apiErr{http.StatusBadGateway, err.Error()}
			}
		}
	}
	s.audit(r, u, "", "setting.notify", "发送测试邮件到 "+to, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]any{"ok": true, "to": to})
}

func (s *Server) putBasic(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in model.BasicSettings
	if !decode(w, r, &in) {
		return
	}
	in.Name, in.URL, in.NoProxy = cleanText(in.Name), strings.TrimRight(cleanText(in.URL), "/"), cleanText(in.NoProxy)
	in.Proxy = cleanText(in.Proxy)
	var b, a model.Settings
	err := func() error {
		switch {
		case in.Name == "" || rlen(in.Name) > 30:
			return bad("平台名称必填，最多 30 个字")
		case in.Interval != 15 && in.Interval != 30 && in.Interval != 60:
			return bad("指标采集间隔须为 15 / 30 / 60 秒")
		case !inRange(in.ReportDays, 7, 730):
			return bad("巡检报告保留须为 7–730 天")
		case !inRange(in.AuditDays, 180, 3650):
			return bad("审计日志保留不能少于 180 天")
		case rlen(in.NoProxy) > 500:
			return bad("不走代理的地址过长")
		}
		if in.URL != "" {
			if err := notify.ValidURL(in.URL); err != nil {
				return bad("平台访问地址" + err.Error())
			}
		}
		var err error
		b, a, err = s.saveSetting(func(c *model.Settings) error {
			in.Proxy = restoreProxy(in.Proxy, c.Basic.Proxy)
			if in.Proxy != "" || in.NoProxy != "" {
				if e := notify.SetProxy(in.Proxy, in.NoProxy); e != nil {
					return bad(e.Error())
				}
			}
			c.Basic = in
			return nil
		})
		return err
	}()
	s.auditSQL(r, u, "setting.basic", "修改基础设置", changes(b.Basic, a.Basic), err)
	if err != nil {
		writeErr(w, err)
		return
	}
	a.Basic.Proxy = maskProxy(a.Basic.Proxy)
	ok(w, a.Basic)
}

// putWatermark 只有超级管理员可以修改（不通过权限码授权）。
func (s *Server) putWatermark(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).Admin {
		s.audit(r, u, "", "setting.watermark", "尝试修改水印", errDenied)
		fail(w, http.StatusForbidden, "仅超级管理员可以自定义水印")
		return
	}
	var in model.Watermark
	if !decode(w, r, &in) {
		return
	}
	in.Text, in.Text2 = strings.TrimSpace(in.Text), strings.TrimSpace(in.Text2)
	var b, a model.Settings
	err := func() error {
		switch {
		case in.Text == "" || rlen(in.Text) > 100 || rlen(in.Text2) > 100:
			return bad("水印第一行必填，两行都不超过 100 个字")
		case !inRange(in.Size, 10, 28):
			return bad("字号须为 10–28")
		case in.Opacity < 0.04 || in.Opacity > 0.4:
			return bad("透明度须为 0.04–0.4")
		case !inRange(in.Angle, -60, 60):
			return bad("角度须为 -60–60")
		case !inRange(in.Gap, 140, 360):
			return bad("间距须为 140–360")
		case !colorRe.MatchString(in.Color):
			return bad("颜色须为 #RRGGBB")
		}
		var err error
		b, a, err = s.saveSetting(func(c *model.Settings) error { c.Watermark = in; return nil })
		return err
	}()
	s.auditSQL(r, u, "setting.watermark", fmt.Sprintf("修改水印：%s，「%s」", map[bool]string{true: "开启", false: "关闭"}[in.On], in.Text), changes(b.Watermark, a.Watermark), err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, a.Watermark)
}

// hashPwd 生成密码哈希并返回设置时间（用于密码过期）。
func hashPwd(pw string) (string, time.Time, error) {
	h, err := secure.HashPassword(pw)
	return h, time.Now(), err
}
