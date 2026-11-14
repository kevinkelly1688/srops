// @author: kevinkelly
// @license: Apache Licence
// @file: sms_test.go
// @time: 2026/10/28

package sms

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"starrocks-ops/internal/notify"
)

func testClient() *Client {
	return &Client{HTTP: http.DefaultClient, Now: func() time.Time { return time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC) }, Nonce: func() string { return "nonce-1" }}
}

var msg = notify.Message{Title: "【严重】StarRocks · prod · 节点失联", Level: "bad", SMS: "【严重】prod · 节点失联：BE 10.0.0.1:8040", Params: map[string]string{
	"level": "严重", "cluster": "prod", "rule": "节点失联", "inst": "BE 10.0.0.1:8040", "count": "1", "time": "16:00", "status": "告警"}}

func TestPhones(t *testing.T) {
	got, err := SplitPhones("13800000000, 13900000000；+8613700000000 13800000000")
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range []string{"", "abc", "1380000a", "https://x", "1234567890123456"} {
		if _, err := SplitPhones(bad); err == nil {
			t.Errorf("应拒绝 %q", bad)
		}
	}
	if Mask("13800001234") != "138****1234" || MaskList([]string{"13800001234", "123"}) != "138****1234, ***" {
		t.Fatal("脱敏")
	}
}

func TestConfigCheck(t *testing.T) {
	for name, c := range map[string]Config{
		"http 无地址":    {},
		"http 地址":     {URL: "ftp://x"},
		"aliyun 无密钥":  {Provider: "aliyun", Sign: "a", Template: "T"},
		"aliyun 无签名":  {Provider: "aliyun", KeyID: "k", Secret: "s", Template: "T", Sign: "【】"},
		"aliyun 无模板":  {Provider: "aliyun", KeyID: "k", Secret: "s", Sign: "a"},
		"tencent 无应用": {Provider: "tencent", KeyID: "k", Secret: "s", Sign: "a", Template: "1"},
		"未知":          {Provider: "x"},
	} {
		if c.Check() == nil {
			t.Errorf("%s：应报错", name)
		}
	}
	if err := (Config{URL: "https://gw.example.com/send"}).Check(); err != nil {
		t.Fatal(err)
	}
}

func TestGateway(t *testing.T) {
	var got map[string]any
	var auth, key string
	reply := `{"code":0}`
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		json.Unmarshal(b, &got)
		auth, key = r.Header.Get("Authorization"), r.Header.Get("X-SROps-Key")
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	c := Config{URL: srv.URL, KeyID: "kid", Secret: "tok", Sign: "【数据平台】", Template: "T1", Params: []string{"cluster", "level"}}
	if err := testClient().Send(t.Context(), c, []string{"13800000000"}, msg); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer tok" || key != "kid" || got["sign"] != "【数据平台】" || got["template"] != "T1" || !strings.HasPrefix(got["content"].(string), "【数据平台】【严重】") {
		t.Fatalf("请求：%v %s %s", got, auth, key)
	}
	if p := got["params"].(map[string]any); len(p) != 2 || p["cluster"] != "prod" || p["level"] != "严重" {
		t.Fatalf("params：%v", p)
	}
	reply = `{"code":1001,"msg":"余额不足"}`
	if err := testClient().Send(t.Context(), c, []string{"13800000000"}, msg); err == nil || !strings.Contains(err.Error(), "余额不足") {
		t.Fatalf("业务错误码：%v", err)
	}
	reply, status = `busy`, 503
	if err := testClient().Send(t.Context(), c, []string{"13800000000"}, msg); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("HTTP 错误：%v", err)
	}
	reply, status = `ok`, 200 // 非 JSON 的 2xx 视为成功
	if err := testClient().Send(t.Context(), c, []string{"13800000000"}, msg); err != nil {
		t.Fatal(err)
	}
}

func TestAliyun(t *testing.T) {
	var form url.Values
	reply := `{"Code":"OK","Message":"OK","RequestId":"1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(b))
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	c := Config{Provider: "aliyun", URL: srv.URL, KeyID: "LTAI-test", Secret: "sec", Sign: "【数据平台】", Template: "SMS_123", Params: []string{"cluster", "rule", "level"}}
	if err := testClient().Send(t.Context(), c, []string{"+8613800000000", "13900000000"}, msg); err != nil {
		t.Fatal(err)
	}
	if form.Get("Action") != "SendSms" || form.Get("SignName") != "数据平台" || form.Get("PhoneNumbers") != "13800000000,13900000000" || form.Get("TemplateCode") != "SMS_123" || form.Get("AccessKeyId") != "LTAI-test" {
		t.Fatalf("参数：%v", form)
	}
	var tp map[string]string
	json.Unmarshal([]byte(form.Get("TemplateParam")), &tp)
	if tp["cluster"] != "prod" || tp["rule"] != "节点失联" || tp["level"] != "严重" || len(tp) != 3 {
		t.Fatalf("模板变量：%v", tp)
	}
	// 独立重算签名：按 RPC 规范排序、编码后 HMAC-SHA1
	var keys []string
	for k := range form {
		if k != "Signature" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var pairs []string
	for _, k := range keys {
		pairs = append(pairs, percent(k)+"="+percent(form.Get(k)))
	}
	mac := hmac.New(sha1.New, []byte("sec&"))
	mac.Write([]byte("POST&%2F&" + percent(strings.Join(pairs, "&"))))
	if want := base64.StdEncoding.EncodeToString(mac.Sum(nil)); form.Get("Signature") != want {
		t.Fatalf("签名不一致：%s vs %s", form.Get("Signature"), want)
	}
	if percent("a b*c~d/中") != "a%20b%2Ac~d%2F%E4%B8%AD" {
		t.Fatalf("percent：%s", percent("a b*c~d/中"))
	}
	reply = `{"Code":"isv.BUSINESS_LIMIT_CONTROL","Message":"触发限流"}`
	if err := testClient().Send(t.Context(), c, []string{"13800000000"}, msg); err == nil || !strings.Contains(err.Error(), "触发限流") {
		t.Fatalf("业务错误：%v", err)
	}
}

func TestTencent(t *testing.T) {
	var hdr http.Header
	var body map[string]any
	reply := `{"Response":{"SendStatusSet":[{"Code":"Ok","PhoneNumber":"+8613800000000"}],"RequestId":"x"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hdr, body = r.Header.Clone(), map[string]any{}
		json.Unmarshal(b, &body)
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	c := Config{Provider: "tencent", URL: srv.URL, KeyID: "AKID", Secret: "sk", Sign: "数据平台", Template: "123456", AppID: "1400000001", Region: "ap-beijing", Params: []string{"cluster", "inst"}}
	if err := testClient().Send(t.Context(), c, []string{"13800000000"}, msg); err != nil {
		t.Fatal(err)
	}
	if hdr.Get("X-TC-Action") != "SendSms" || hdr.Get("X-TC-Version") != "2021-01-11" || hdr.Get("X-TC-Region") != "ap-beijing" || hdr.Get("X-TC-Timestamp") != "1791532800" {
		t.Fatalf("请求头：%v", hdr)
	}
	au := hdr.Get("Authorization")
	if !strings.HasPrefix(au, "TC3-HMAC-SHA256 Credential=AKID/2026-10-09/sms/tc3_request, SignedHeaders=content-type;host;x-tc-action, Signature=") || len(au) < 150 {
		t.Fatalf("Authorization：%s", au)
	}
	if body["SmsSdkAppId"] != "1400000001" || body["SignName"] != "数据平台" || body["TemplateId"] != "123456" || body["PhoneNumberSet"].([]any)[0] != "+8613800000000" {
		t.Fatalf("请求体：%v", body)
	}
	if ps := body["TemplateParamSet"].([]any); len(ps) != 2 || ps[0] != "prod" || ps[1] != "BE 10.0.0.1:8040" {
		t.Fatalf("变量按配置顺序：%v", ps)
	}
	reply = `{"Response":{"Error":{"Code":"AuthFailure.SignatureFailure","Message":"签名错误"},"RequestId":"x"}}`
	if err := testClient().Send(t.Context(), c, []string{"13800000000"}, msg); err == nil || !strings.Contains(err.Error(), "签名错误") {
		t.Fatalf("接口错误：%v", err)
	}
	reply = `{"Response":{"SendStatusSet":[{"Code":"LimitExceeded.PhoneNumberDailyLimit","Message":"超出日限","PhoneNumber":"+8613800000000"}]}}`
	if err := testClient().Send(t.Context(), c, []string{"13800000000"}, msg); err == nil || !strings.Contains(err.Error(), "138****0000") || !strings.Contains(err.Error(), "超出日限") {
		t.Fatalf("号码级错误：%v", err)
	}
}

func TestParamClip(t *testing.T) {
	m := notify.Message{Params: map[string]string{"inst": strings.Repeat("长", 30)}}
	vals, _ := paramValues(Config{Params: []string{"inst", "level"}}, m)
	if len([]rune(vals[0])) != 20 || vals[1] != "-" {
		t.Fatalf("变量应截断到 20 字、缺失用 -：%v", vals)
	}
}
