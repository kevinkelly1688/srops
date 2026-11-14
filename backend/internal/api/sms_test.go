// @author: kevinkelly
// @license: Apache Licence
// @file: sms_test.go
// @time: 2026/10/28

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"starrocks-ops/internal/model"
)

// smsGW 记录收到的短信请求的假短信服务。
type smsGW struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
	auth   []string
	reply  string
}

func newSMSGW(t *testing.T) *smsGW {
	g := &smsGW{reply: `{"code":0}`}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.bodies, g.auth = append(g.bodies, string(b)), append(g.auth, r.Header.Get("Authorization"))
		io.WriteString(w, g.reply)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *smsGW) count() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.bodies) }
func (g *smsGW) last() (string, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.bodies) == 0 {
		return "", ""
	}
	return g.bodies[len(g.bodies)-1], g.auth[len(g.auth)-1]
}

func putNotifyWith(e *env, mut func(n map[string]any)) map[string]any {
	e.t.Helper()
	var cur map[string]any
	e.expect(200, "GET", "/api/settings", nil, &cur)
	n := cur["notify"].(map[string]any)
	delete(n, "smtpPwd")
	delete(n, "smsSecretSet")
	mut(n)
	var out map[string]any
	e.expect(200, "PUT", "/api/settings/notify", n, &out)
	return out
}

func TestSMSChannelAndGateway(t *testing.T) {
	e, _ := opsEnv(t, "problems")
	gw := newSMSGW(t)

	// 渠道校验与脱敏
	var ch model.Channel
	e.expect(200, "POST", "/api/alert-channels", map[string]any{"type": "短信", "name": "值班短信", "url": "13800000000, 13900001111；13800000000", "levels": []string{"critical"}}, &ch)
	if ch.URLMasked != "138****0000, 139****1111" || ch.URLEnc != "" {
		t.Fatalf("手机号应脱敏且不返回密文：%+v", ch)
	}
	for name, b := range map[string]map[string]any{
		"无号码":  {"type": "短信", "name": "x1", "levels": []string{"critical"}},
		"非号码":  {"type": "短信", "name": "x2", "url": "https://hook.example.com", "levels": []string{"critical"}},
		"号码过短": {"type": "短信", "name": "x3", "url": "12345", "levels": []string{"critical"}},
		"号码过多": {"type": "短信", "name": "x4", "url": strings.Repeat("1380000000", 1) + "0," + func() string {
			var p []string
			for i := 0; i < 20; i++ {
				p = append(p, "139000010"+string(rune('0'+i/10))+string(rune('0'+i%10)))
			}
			return strings.Join(p, ",")
		}(), "levels": []string{"critical"}},
	} {
		if code := e.call("POST", "/api/alert-channels", b, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(e.dir, "state.json"))
	if strings.Contains(string(raw), "13800000000") {
		t.Fatal("state.json 中不应有明文手机号")
	}

	// 网关未配置：测试发送失败并给出指引
	e.expect(502, "POST", "/api/alert-channels/"+ch.ID+"/test", nil, nil)

	// 配置通用 HTTP 网关；密钥加密保存且不返回
	out := putNotifyWith(e, func(n map[string]any) {
		n["mergeMin"] = 0
		n["smsProvider"], n["smsUrl"], n["smsSign"], n["smsParams"] = "http", gw.srv.URL, "【运维平台】", "cluster, rule"
		n["smsSecret"] = "gw-token-123"
	})
	if out["smsSecretSet"] != true || out["smsSecretEnc"] != nil || out["smsParams"] != "cluster, rule" {
		t.Fatalf("设置返回：%v", out)
	}
	raw, _ = os.ReadFile(filepath.Join(e.dir, "state.json"))
	if strings.Contains(string(raw), "gw-token-123") {
		t.Fatal("state.json 中不应有明文短信密钥")
	}
	e.expect(200, "POST", "/api/alert-channels/"+ch.ID+"/test", nil, nil)
	body, auth := gw.last()
	if auth != "Bearer gw-token-123" || !strings.Contains(body, `"phones":["13800000000","13900001111"]`) || !strings.Contains(body, "【运维平台】【测试】") {
		t.Fatalf("网关请求：%s %s", auth, body)
	}

	// 真实告警：只发严重级别
	e.zeroDur()
	before := gw.count()
	now := time.Now()
	e.api.EvalAlerts(t.Context(), now)
	e.api.EvalAlerts(t.Context(), now.Add(time.Minute))
	if gw.count() <= before {
		t.Fatal("严重告警应发送短信")
	}
	body, _ = gw.last()
	if !strings.Contains(body, "prod-test") || !strings.Contains(body, "【严重】") || strings.Contains(body, "【警告】") {
		t.Fatalf("短信内容：%s", body)
	}

	// 网关业务错误体现在渠道的最近发送结果里
	gw.mu.Lock()
	gw.reply = `{"code":5,"msg":"签名未报备"}`
	gw.mu.Unlock()
	e.expect(502, "POST", "/api/alert-channels/"+ch.ID+"/test", nil, nil)
	var list []model.Channel
	e.expect(200, "GET", "/api/alert-channels", nil, &list)
	if !strings.Contains(list[0].LastResult, "签名未报备") {
		t.Fatalf("最近发送结果：%q", list[0].LastResult)
	}

	// 设置校验
	for k, v := range map[string]any{"smsProvider": "x", "smsParams": "cluster,bogus", "smsAppId": "abc", "smsRegion": "BAD REGION", "smsKeyId": "a b", "smsTemplate": strings.Repeat("a", 200)} {
		var cur map[string]any
		e.expect(200, "GET", "/api/settings", nil, &cur)
		n := cur["notify"].(map[string]any)
		delete(n, "smtpPwd")
		delete(n, "smsSecretSet")
		n[k] = v
		if code := e.call("PUT", "/api/settings/notify", n, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", k, code)
		}
	}
	// 清除密钥
	out = putNotifyWith(e, func(n map[string]any) { n["clearSmsSecret"] = true })
	if out["smsSecretSet"] != false {
		t.Fatalf("清除密钥后：%v", out["smsSecretSet"])
	}
}

func TestSMSCloudProvidersAndTestSend(t *testing.T) {
	e, _ := opsEnv(t, "healthy")
	gw := newSMSGW(t)
	gw.reply = `{"Code":"OK","Message":"OK"}`
	putNotifyWith(e, func(n map[string]any) {
		n["smsProvider"], n["smsUrl"], n["smsSign"], n["smsKeyId"], n["smsTemplate"], n["smsParams"] = "aliyun", gw.srv.URL, "【运维平台】", "LTAI-key", "SMS_1001", "level,cluster,rule"
		n["smsSecret"] = "ali-secret"
	})
	// 测试短信：用表单里的配置，密钥留空沿用已保存的
	form := map[string]any{"smsProvider": "aliyun", "smsUrl": gw.srv.URL, "smsSign": "【运维平台】", "smsKeyId": "LTAI-key", "smsTemplate": "SMS_1001", "smsParams": "level,cluster,rule",
		"smtpPort": 465, "smtpSec": "SSL", "mergeMin": 5, "repeatMin": 60, "recover": true, "quietFrom": "23:00", "quietTo": "08:00", "phone": "13800000000"}
	e.expect(200, "POST", "/api/settings/notify/test-sms", form, nil)
	body, _ := gw.last()
	q, _ := url.ParseQuery(body)
	if q.Get("Action") != "SendSms" || q.Get("PhoneNumbers") != "13800000000" || q.Get("TemplateCode") != "SMS_1001" || q.Get("SignName") != "运维平台" || q.Get("Signature") == "" || !strings.Contains(q.Get("TemplateParam"), "测试集群") {
		t.Fatalf("阿里云请求：%v", q)
	}
	// 业务错误体现为 502；号码校验为 400
	gw.mu.Lock()
	gw.reply = `{"Code":"isv.MOBILE_NUMBER_ILLEGAL","Message":"号码非法"}`
	gw.mu.Unlock()
	e.expect(502, "POST", "/api/settings/notify/test-sms", form, nil)
	form["phone"] = "abc"
	e.expect(400, "POST", "/api/settings/notify/test-sms", form, nil)
	form["phone"] = "13800000001,13800000002,13800000003,13800000004"
	e.expect(400, "POST", "/api/settings/notify/test-sms", form, nil)
	form["phone"] = "13800000000"
	form["smsProvider"] = "bogus"
	e.expect(400, "POST", "/api/settings/notify/test-sms", form, nil)

	// 未配置密钥的云厂商：给出明确指引
	putNotifyWith(e, func(n map[string]any) { n["clearSmsSecret"] = true })
	form["smsProvider"] = "aliyun"
	e.expect(502, "POST", "/api/settings/notify/test-sms", form, nil)

	// 权限：没有 setting.notify 的用户
	e.newUser("oncall7", []string{"r_oncall"}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall7", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	oc.expect(403, "POST", "/api/settings/notify/test-sms", form, nil)
}
