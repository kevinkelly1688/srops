// @author: kevinkelly
// @license: Apache Licence
// @file: notify_test.go
// @time: 2026/10/28

package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type hit struct {
	query  string
	header http.Header
	body   map[string]any
	raw    []byte
}

// bot 模拟机器人接口：记录请求并返回 reply。
func bot(t *testing.T, status int, reply string) (*httptest.Server, *hit) {
	h := &hit{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.query, h.header = r.URL.RawQuery, r.Header
		h.raw, _ = io.ReadAll(r.Body)
		if err := json.Unmarshal(h.raw, &h.body); err != nil {
			t.Errorf("请求体不是 JSON：%s", h.raw)
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, h
}

var fixed = time.Unix(1760000000, 0)

func client() *Client {
	return &Client{HTTP: http.DefaultClient, Now: func() time.Time { return fixed }}
}

func msg() Message {
	return Message{Title: "StarRocks 巡检 · prod 健康分 71", Level: "bad", Summary: []string{"报告 IR-1"},
		Sections: []Section{{Head: "异常（1）", Lines: []string{"BE 磁盘使用率：87%"}}, {Head: "警告（0）"}}, Data: map[string]any{"score": 71}}
}

func TestWeCom(t *testing.T) {
	srv, h := bot(t, 200, `{"errcode":0,"errmsg":"ok"}`)
	if err := client().Send(context.Background(), "企业微信", srv.URL+"/cgi-bin/webhook/send?key=k", "", msg()); err != nil {
		t.Fatal(err)
	}
	c := h.body["markdown"].(map[string]any)["content"].(string)
	if h.body["msgtype"] != "markdown" || !strings.Contains(c, "**异常（1）**") || !strings.Contains(c, "• BE 磁盘使用率：87%") || strings.Contains(c, "警告（0）") {
		t.Fatalf("内容不对：%s", c)
	}
	srv2, _ := bot(t, 200, `{"errcode":93000,"errmsg":"invalid webhook url"}`)
	if err := client().Send(context.Background(), "企业微信", srv2.URL, "", msg()); err == nil || !strings.Contains(err.Error(), "93000") {
		t.Fatalf("应返回业务错误，得到 %v", err)
	}
}

func TestDingTalkSign(t *testing.T) {
	srv, h := bot(t, 200, `{"errcode":0,"errmsg":"ok"}`)
	if err := client().Send(context.Background(), "钉钉", srv.URL+"/robot/send?access_token=x", "SECabc", msg()); err != nil {
		t.Fatal(err)
	}
	ts := "1760000000000"
	mac := hmac.New(sha256.New, []byte("SECabc"))
	mac.Write([]byte(ts + "\nSECabc"))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !strings.Contains(h.query, "access_token=x&timestamp="+ts+"&sign=") {
		t.Fatalf("签名参数缺失：%s", h.query)
	}
	if got := strings.SplitN(h.query, "&sign=", 2)[1]; got != strings.NewReplacer("+", "%2B", "/", "%2F", "=", "%3D").Replace(want) {
		t.Fatalf("签名不对：%s，期望 %s", got, want)
	}
	if !strings.Contains(h.body["markdown"].(map[string]any)["text"].(string), "\n\n") {
		t.Fatal("钉钉 Markdown 需要空行换行")
	}
}

func TestFeishuLarkSign(t *testing.T) {
	for _, typ := range []string{"飞书", "Lark"} {
		srv, h := bot(t, 200, `{"code":0,"msg":"success"}`)
		if err := client().Send(context.Background(), typ, srv.URL+"/open-apis/bot/v2/hook/x", "sec", msg()); err != nil {
			t.Fatal(typ, err)
		}
		mac := hmac.New(sha256.New, []byte("1760000000\nsec"))
		if h.body["timestamp"] != "1760000000" || h.body["sign"] != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("%s 签名不对：%v", typ, h.body)
		}
		card := h.body["card"].(map[string]any)
		if h.body["msg_type"] != "interactive" || card["header"].(map[string]any)["template"] != "red" {
			t.Fatalf("%s 卡片不对：%v", typ, card)
		}
	}
	srv, _ := bot(t, 200, `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`)
	if err := client().Send(context.Background(), "飞书", srv.URL, "bad", msg()); err == nil || !strings.Contains(err.Error(), "19021") {
		t.Fatalf("应返回签名错误，得到 %v", err)
	}
}

func TestSlack(t *testing.T) {
	srv, h := bot(t, 200, "ok")
	if err := client().Send(context.Background(), "Slack", srv.URL+"/services/T/B/X", "", msg()); err != nil {
		t.Fatal(err)
	}
	att := h.body["attachments"].([]any)[0].(map[string]any)
	if att["color"] != "#d93025" || !strings.Contains(string(h.raw), "*异常（1）*") {
		t.Fatalf("Slack 消息不对：%s", h.raw)
	}
	srv2, _ := bot(t, 404, "no_service")
	if err := client().Send(context.Background(), "Slack", srv2.URL, "", msg()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("应返回 HTTP 错误，得到 %v", err)
	}
}

func TestWebhookHMAC(t *testing.T) {
	srv, h := bot(t, 204, "")
	if err := client().Send(context.Background(), "Webhook", srv.URL+"/alert", "k", msg()); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write([]byte("1760000000."))
	mac.Write(h.raw)
	if h.header.Get("X-SROps-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || h.header.Get("X-SROps-Timestamp") != "1760000000" {
		t.Fatalf("HMAC 头不对：%v", h.header)
	}
	if h.body["data"].(map[string]any)["score"] != float64(71) {
		t.Fatalf("结构化数据缺失：%s", h.raw)
	}
}

func TestValidateAndMask(t *testing.T) {
	for _, u := range []string{"", "ftp://x/y", "hooks.slack.com/services/x", "https://"} {
		if ValidURL(u) == nil {
			t.Errorf("%q 应校验失败", u)
		}
	}
	m := Mask("https://open.feishu.cn/open-apis/bot/v2/hook/0123456789abcdef")
	if strings.Contains(m, "0123456789") || !strings.HasPrefix(m, "https://open.feishu.cn/open-apis/bot/v2/hook/****") || !strings.HasSuffix(m, "cdef") {
		t.Fatalf("脱敏不对：%s", m)
	}
	if strings.Contains(Mask("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=SECRETKEY1234"), "SECRETKEY") {
		t.Fatal("查询参数中的 key 应被隐藏")
	}
	if err := client().Send(context.Background(), "钉钉", "http://127.0.0.1:1/robot/send?access_token=TOPSECRET", "", msg()); err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Fatalf("错误信息不应包含 token：%v", err)
	}
}

func TestProxyRules(t *testing.T) {
	t.Cleanup(func() { SetProxy("", "") })
	if err := SetProxy("ftp://x", ""); err == nil {
		t.Fatal("非法代理地址应拒绝")
	}
	if err := SetProxy("http://proxy.local:3128", "10.0.0.0/8, *.corp.example.com, intranet"); err != nil {
		t.Fatal(err)
	}
	for url, want := range map[string]string{
		"https://hooks.slack.com/x":      "proxy.local:3128",
		"https://10.2.3.4/hook":          "",
		"https://bot.corp.example.com/x": "",
		"https://corp.example.com/x":     "",
		"https://intranet/x":             "",
		"https://evilcorp.example.com/x": "proxy.local:3128",
	} {
		req, _ := http.NewRequest("POST", url, nil)
		u, _ := proxyFor(req)
		got := ""
		if u != nil {
			got = u.Host
		}
		if got != want {
			t.Errorf("%s 代理 = %q，期望 %q", url, got, want)
		}
	}
}
