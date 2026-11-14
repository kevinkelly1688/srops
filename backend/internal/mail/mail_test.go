// @author: kevinkelly
// @license: Apache Licence
// @file: mail_test.go
// @time: 2026/10/28

package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"mime"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

type srv struct {
	ln   net.Listener
	cert tls.Certificate
	mu   sync.Mutex
	got  struct {
		auth, from, data string
		rcpt             []string
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// start 启动一个最小的 SMTP 服务：mode 为 SSL（隐式 TLS）/ STARTTLS / none。
func start(t *testing.T, mode string, wantAuth bool) *srv {
	s := &srv{cert: selfSigned(t)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if mode == "SSL" {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{s.cert}})
	}
	s.ln = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c, mode, wantAuth)
		}
	}()
	return s
}

func (s *srv) serve(c net.Conn, mode string, wantAuth bool) {
	defer c.Close()
	tp := textproto.NewConn(c)
	tp.PrintfLine("220 fake ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch cmd {
		case "EHLO", "HELO":
			tp.PrintfLine("250-fake")
			if mode == "STARTTLS" {
				tp.PrintfLine("250-STARTTLS")
			}
			if wantAuth {
				tp.PrintfLine("250-AUTH PLAIN")
			}
			tp.PrintfLine("250 OK")
		case "STARTTLS":
			tp.PrintfLine("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if tc.Handshake() != nil {
				return
			}
			c = tc
			tp = textproto.NewConn(tc)
		case "AUTH":
			raw, _ := base64.StdEncoding.DecodeString(strings.Fields(line)[2])
			s.mu.Lock()
			s.got.auth = strings.ReplaceAll(string(raw), "\x00", "|")
			s.mu.Unlock()
			if s.got.auth != "|ops|s3cret" {
				tp.PrintfLine("535 bad credentials")
				continue
			}
			tp.PrintfLine("235 ok")
		case "MAIL":
			s.mu.Lock()
			s.got.from = line
			s.mu.Unlock()
			tp.PrintfLine("250 OK")
		case "RCPT":
			if strings.Contains(line, "blocked@") {
				tp.PrintfLine("550 no such user")
				continue
			}
			s.mu.Lock()
			s.got.rcpt = append(s.got.rcpt, line)
			s.mu.Unlock()
			tp.PrintfLine("250 OK")
		case "DATA":
			tp.PrintfLine("354 go")
			b, _ := tp.ReadDotBytes()
			s.mu.Lock()
			s.got.data = string(b)
			s.mu.Unlock()
			tp.PrintfLine("250 queued")
		case "QUIT":
			tp.PrintfLine("221 bye")
			return
		default:
			tp.PrintfLine("250 OK")
		}
	}
}

func cfgFor(s *srv, sec string) Config {
	_, p, _ := net.SplitHostPort(s.ln.Addr().String())
	var port int
	for _, ch := range p {
		port = port*10 + int(ch-'0')
	}
	return Config{Host: "127.0.0.1", Port: port, Sec: sec, SkipVerify: true, From: "sr-ops@example.com", FromName: "StarRocks 运维平台"}
}

func TestSendModes(t *testing.T) {
	for _, mode := range []string{"none", "STARTTLS", "SSL"} {
		s := start(t, mode, true)
		c := cfgFor(s, mode)
		c.User, c.Password = "ops", "s3cret"
		if err := Send(context.Background(), c, []string{"a@example.com", "b@example.com"}, "【巡检】健康分 71", "第一行\n第二行"); err != nil {
			t.Fatalf("%s：%v", mode, err)
		}
		s.mu.Lock()
		got := s.got
		s.mu.Unlock()
		if got.auth != "|ops|s3cret" || !strings.Contains(got.from, "sr-ops@example.com") || len(got.rcpt) != 2 {
			t.Fatalf("%s 信封不对：%+v", mode, got)
		}
		hdr, body, _ := strings.Cut(strings.ReplaceAll(got.data, "\r\n", "\n"), "\n\n")
		if !strings.Contains(hdr, "Content-Transfer-Encoding: base64") || strings.Contains(hdr, "健康分") {
			t.Fatalf("%s 邮件头不对（中文须编码）：%s", mode, hdr)
		}
		dec, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(body), "\n", ""))
		if string(dec) != "第一行\n第二行" {
			t.Fatalf("%s 正文不对：%q", mode, dec)
		}
		var subj string
		for _, l := range strings.Split(hdr, "\n") {
			if strings.HasPrefix(l, "Subject: ") {
				subj, _ = new(mime.WordDecoder).DecodeHeader(strings.TrimPrefix(l, "Subject: "))
			}
		}
		if subj != "【巡检】健康分 71" {
			t.Fatalf("%s 主题不对：%q", mode, subj)
		}
	}
}

func TestSendErrors(t *testing.T) {
	s := start(t, "none", true)
	c := cfgFor(s, "none")
	c.User, c.Password = "ops", "wrong"
	if err := Send(context.Background(), c, []string{"a@example.com"}, "x", "y"); err == nil || !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("应报认证失败：%v", err)
	}
	c.Password = "s3cret"
	if err := Send(context.Background(), c, []string{"blocked@example.com"}, "x", "y"); err == nil || !strings.Contains(err.Error(), "blocked@example.com") {
		t.Fatalf("应报收件人被拒绝：%v", err)
	}
	if err := Send(context.Background(), c, []string{"a@example.com"}, "x\r\nBcc: evil@example.com", "y"); err == nil {
		t.Fatal("主题换行应拒绝（防头注入）")
	}
	c2 := c
	c2.Port = 1
	if err := Send(context.Background(), c2, []string{"a@example.com"}, "x", "y"); err == nil || !strings.Contains(err.Error(), "连接") {
		t.Fatalf("应报连接失败：%v", err)
	}
	// 证书校验：自签名证书在不跳过校验时必须失败
	s2 := start(t, "SSL", false)
	c3 := cfgFor(s2, "SSL")
	c3.SkipVerify = false
	if err := Send(context.Background(), c3, []string{"a@example.com"}, "x", "y"); err == nil {
		t.Fatal("未跳过证书校验时，自签名证书应被拒绝")
	}
	// STARTTLS 要求服务器支持
	c4 := cfgFor(s, "STARTTLS")
	if err := Send(context.Background(), c4, []string{"a@example.com"}, "x", "y"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("服务器不支持 STARTTLS 应报错：%v", err)
	}
	_ = bufio.NewReader
}

func TestAddrs(t *testing.T) {
	if got, err := SplitAddrs("a@x.com; b@y.org,c@z.io"); err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"", "nope", "a@x", "a@x.com, <evil>@y.com", "a b@x.com"} {
		if _, err := SplitAddrs(bad); err == nil {
			t.Errorf("%q 应拒绝", bad)
		}
	}
}
