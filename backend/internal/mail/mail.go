// @author: kevinkelly
// @license: Apache Licence
// @file: mail.go
// @time: 2026/10/28

// Package mail 通过 SMTP 发送纯文本邮件（SSL/TLS、STARTTLS、不加密三种方式）。
package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Host       string
	Port       int
	Sec        string // SSL / STARTTLS / none
	SkipVerify bool   // 不校验服务器证书（自签名证书的内网 SMTP）
	User       string
	Password   string
	From       string
	FromName   string
}

var addrRe = regexp.MustCompile(`^[^@\s<>,;"]+@[^@\s<>,;"]+\.[^@\s<>,;"]+$`)

// ValidAddr 判断邮箱地址格式。
func ValidAddr(a string) bool { return len(a) <= 254 && addrRe.MatchString(a) }

// SplitAddrs 按逗号 / 分号 / 空白拆分收件人并校验，最多 20 个。
func SplitAddrs(s string) ([]string, error) {
	var out []string
	for _, a := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t' }) {
		if !ValidAddr(a) {
			return nil, fmt.Errorf("邮箱地址格式不正确：%s", a)
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, errors.New("请填写至少一个收件人邮箱")
	}
	if len(out) > 20 {
		return nil, errors.New("收件人最多 20 个")
	}
	return out, nil
}

func noCRLF(s string) bool { return !strings.ContainsAny(s, "\r\n") }

// Check 校验配置是否可用于发送。
func (c Config) Check() error {
	switch {
	case c.Host == "" || c.Port < 1 || c.Port > 65535:
		return errors.New("请先在「平台设置 → 通知」中配置邮件服务器")
	case c.Sec != "SSL" && c.Sec != "STARTTLS" && c.Sec != "none":
		return errors.New("加密方式须为 SSL / STARTTLS / none")
	case !ValidAddr(c.From):
		return errors.New("请先配置发件人地址")
	case !noCRLF(c.FromName) || !noCRLF(c.User):
		return errors.New("发件人名称或账号含非法字符")
	}
	return nil
}

// plainAuth 与 smtp.PlainAuth 相同，但允许在“不加密”配置下使用（由管理员明确选择）。
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}
func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("SMTP 服务器要求额外的认证步骤，不支持")
	}
	return nil, nil
}

// Send 发送一封纯文本邮件。
func Send(ctx context.Context, c Config, to []string, subject, body string) error {
	if err := c.Check(); err != nil {
		return err
	}
	if len(to) == 0 {
		return errors.New("没有收件人")
	}
	for _, a := range to {
		if !ValidAddr(a) {
			return fmt.Errorf("邮箱地址格式不正确：%s", a)
		}
	}
	if !noCRLF(subject) {
		return errors.New("邮件主题不能换行")
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsCfg := &tls.Config{ServerName: c.Host, InsecureSkipVerify: c.SkipVerify, MinVersion: tls.VersionTLS12}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("连接 %s 失败：%v", addr, err)
	}
	conn.SetDeadline(deadline)
	if c.Sec == "SSL" {
		conn = tls.Client(conn, tlsCfg)
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP 握手失败：%v", err)
	}
	defer cl.Close()
	if c.Sec == "STARTTLS" {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("服务器不支持 STARTTLS，请改用 SSL 或不加密")
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS 失败：%v", err)
		}
	}
	if c.User != "" {
		if ok, _ := cl.Extension("AUTH"); !ok {
			return errors.New("服务器不支持认证，请清空认证账号")
		}
		if err := cl.Auth(plainAuth{c.User, c.Password}); err != nil {
			return fmt.Errorf("SMTP 认证失败：%v", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return fmt.Errorf("发件人被拒绝：%v", err)
	}
	for _, a := range to {
		if err := cl.Rcpt(a); err != nil {
			return fmt.Errorf("收件人 %s 被拒绝：%v", a, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("发送失败：%v", err)
	}
	if _, err := w.Write(build(c, to, subject, body)); err != nil {
		return fmt.Errorf("发送失败：%v", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("邮件被服务器拒绝：%v", err)
	}
	cl.Quit()
	return nil
}

func build(c Config, to []string, subject, body string) []byte {
	from := c.From
	if c.FromName != "" {
		from = mime.QEncoding.Encode("utf-8", c.FromName) + " <" + c.From + ">"
	}
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(body, "\r\n", "\n")))
	var lines []string
	for len(enc) > 76 {
		lines, enc = append(lines, enc[:76]), enc[76:]
	}
	lines = append(lines, enc)
	h := []string{
		"From: " + from,
		"To: " + strings.Join(to, ", "),
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
	}
	return []byte(strings.Join(h, "\r\n") + "\r\n\r\n" + strings.Join(lines, "\r\n") + "\r\n")
}
