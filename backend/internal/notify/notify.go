// @author: kevinkelly
// @license: Apache Licence
// @file: notify.go
// @time: 2026/10/28

// Package notify 把消息发送到群机器人 / Webhook：企业微信、钉钉、飞书、Lark、Slack 与通用 Webhook。
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Types 是支持的通知类型（与前端 CHANNEL_TYPES 的名称一致）。
var Types = []string{"企业微信", "钉钉", "飞书", "Lark", "Slack", "Webhook"}

// Section 是消息中的一个分组，例如“异常”“警告”。
type Section struct {
	Head  string
	Lines []string
}

// Message 是与渠道无关的消息内容。
type Message struct {
	Title    string
	Level    string // bad / warn / ok
	Summary  []string
	Sections []Section
	Data     any // 通用 Webhook 的结构化数据

	SMS    string            // 短信正文（不含签名）；为空时短信渠道用标题
	Params map[string]string // 短信模板变量：level / cluster / rule / inst / value / count / time / status
}

// Client 发送消息；Now 可在测试中替换。
type Client struct {
	HTTP *http.Client
	Now  func() time.Time
}

// Default 使用系统代理（HTTPS_PROXY 等环境变量），超时 10 秒。
var Default = &Client{HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: proxyFor}}, Now: time.Now}

var (
	proxyMu      sync.RWMutex
	proxyURL     *url.URL
	noProxyRules []string
)

// SetProxy 设置出网 HTTP 代理（平台设置 → 基础）。proxy 为空时回退到环境变量 HTTPS_PROXY / HTTP_PROXY；
// noProxy 为逗号分隔的主机名、域名后缀（.example.com 或 *.example.com）或 CIDR，命中的地址直连。
func SetProxy(proxy, noProxy string) error {
	var u *url.URL
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		var err error
		if u, err = url.Parse(proxy); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
			return errors.New("代理地址格式为 http://主机:端口")
		}
	}
	var rules []string
	for _, r := range strings.Split(noProxy, ",") {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" {
			rules = append(rules, r)
		}
	}
	proxyMu.Lock()
	proxyURL, noProxyRules = u, rules
	proxyMu.Unlock()
	return nil
}

func proxyFor(req *http.Request) (*url.URL, error) {
	proxyMu.RLock()
	u, rules := proxyURL, noProxyRules
	proxyMu.RUnlock()
	if u == nil {
		return http.ProxyFromEnvironment(req)
	}
	host := strings.ToLower(req.URL.Hostname())
	ip := net.ParseIP(host)
	for _, r := range rules {
		if _, n, err := net.ParseCIDR(r); err == nil {
			if ip != nil && n.Contains(ip) {
				return nil, nil
			}
			continue
		}
		r = strings.TrimPrefix(strings.TrimPrefix(r, "*"), ".")
		if host == r || strings.HasSuffix(host, "."+r) {
			return nil, nil
		}
	}
	return u, nil
}

// ValidURL 校验通知地址。
func ValidURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("地址须为 http:// 或 https:// 开头的完整 URL")
	}
	return nil
}

// Mask 脱敏显示地址：保留协议、主机和路径开头，隐藏 token。
func Mask(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "****"
	}
	tail := raw
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	p := u.Path
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[:i+1]
	}
	if len(p) > 30 {
		p = p[:30] + "…/"
	}
	return u.Scheme + "://" + u.Host + p + "****" + tail
}

// Send 按类型组装请求并发送；对方返回业务错误码时返回 error。
func (c *Client) Send(ctx context.Context, typ, rawURL, secret string, m Message) error {
	if err := ValidURL(rawURL); err != nil {
		return err
	}
	var body any
	target := rawURL
	headers := map[string]string{}
	switch typ {
	case "企业微信":
		body = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": "## " + m.Title + "\n" + text(m, "**%s**", "\n")}}
	case "钉钉":
		if secret != "" {
			ts := strconv.FormatInt(c.Now().UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(ts + "\n" + secret))
			sep := "&"
			if !strings.Contains(target, "?") {
				sep = "?"
			}
			target += sep + "timestamp=" + ts + "&sign=" + url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		}
		body = map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": m.Title, "text": "### " + m.Title + "\n\n" + text(m, "**%s**", "\n\n")}}
	case "飞书", "Lark":
		tpl := map[string]string{"bad": "red", "warn": "orange"}[m.Level]
		if tpl == "" {
			tpl = "green"
		}
		card := map[string]any{
			"msg_type": "interactive",
			"card": map[string]any{
				"config":   map[string]any{"wide_screen_mode": true},
				"header":   map[string]any{"template": tpl, "title": map[string]string{"tag": "plain_text", "content": m.Title}},
				"elements": []any{map[string]string{"tag": "markdown", "content": text(m, "**%s**", "\n")}},
			},
		}
		if secret != "" {
			ts := strconv.FormatInt(c.Now().Unix(), 10)
			mac := hmac.New(sha256.New, []byte(ts+"\n"+secret))
			card["timestamp"] = ts
			card["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		body = card
	case "Slack":
		color := map[string]string{"bad": "#d93025", "warn": "#f29900"}[m.Level]
		if color == "" {
			color = "#1e8e3e"
		}
		body = map[string]any{"text": m.Title, "attachments": []any{map[string]any{"color": color, "blocks": []any{
			map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": "*" + m.Title + "*\n" + text(m, "*%s*", "\n")}},
		}}}}
	case "Webhook", "其他":
		body = map[string]any{"title": m.Title, "level": m.Level, "text": text(m, "%s", "\n"), "data": m.Data}
	default:
		return fmt.Errorf("不支持的通知类型：%s", typ)
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if (typ == "Webhook" || typ == "其他") && secret != "" {
		ts := strconv.FormatInt(c.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts + "."))
		mac.Write(buf)
		headers["X-SROps-Timestamp"] = ts
		headers["X-SROps-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "sr-ops")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("连接失败：%v", stripURL(err))
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d：%s", resp.StatusCode, short(rb))
	}
	return checkReply(typ, rb)
}

// checkReply 解析各平台的业务返回码（HTTP 200 也可能失败，例如签名错误、关键词不匹配）。
func checkReply(typ string, rb []byte) error {
	var r struct {
		ErrCode    *int   `json:"errcode"`
		ErrMsg     string `json:"errmsg"`
		Code       *int   `json:"code"`
		Msg        string `json:"msg"`
		StatusCode *int   `json:"StatusCode"`
	}
	switch typ {
	case "企业微信", "钉钉":
		if json.Unmarshal(rb, &r) != nil || r.ErrCode == nil {
			return fmt.Errorf("返回格式无法识别：%s", short(rb))
		}
		if *r.ErrCode != 0 {
			return fmt.Errorf("errcode=%d：%s", *r.ErrCode, r.ErrMsg)
		}
	case "飞书", "Lark":
		if json.Unmarshal(rb, &r) != nil || (r.Code == nil && r.StatusCode == nil) {
			return fmt.Errorf("返回格式无法识别：%s", short(rb))
		}
		if r.Code != nil && *r.Code != 0 {
			return fmt.Errorf("code=%d：%s", *r.Code, r.Msg)
		}
		if r.Code == nil && *r.StatusCode != 0 {
			return fmt.Errorf("StatusCode=%d", *r.StatusCode)
		}
	case "Slack":
		if s := strings.TrimSpace(string(rb)); s != "ok" {
			return fmt.Errorf("返回：%s", short(rb))
		}
	}
	return nil
}

// PlainText 把消息渲染为纯文本（邮件正文）。
func PlainText(m Message) string { return text(m, "【%s】", "\n") }

// text 把摘要与分组渲染为各平台的 Markdown；bold 为加粗格式，nl 为换行符。
func text(m Message, bold, nl string) string {
	var b []string
	b = append(b, m.Summary...)
	for _, s := range m.Sections {
		if len(s.Lines) == 0 {
			continue
		}
		b = append(b, fmt.Sprintf(bold, s.Head))
		for _, l := range s.Lines {
			b = append(b, "• "+l)
		}
	}
	return strings.Join(b, nl)
}

func short(b []byte) string {
	s := strings.TrimSpace(string(b))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// stripURL 去掉错误信息中的完整地址（含 token），只保留主机。
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		host := ue.URL
		if u, e := url.Parse(ue.URL); e == nil {
			host = u.Host
		}
		return fmt.Errorf("%s %s：%v", ue.Op, host, ue.Err)
	}
	return err
}
