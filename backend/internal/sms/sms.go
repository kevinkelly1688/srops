// @author: kevinkelly
// @license: Apache Licence
// @file: sms.go
// @time: 2026/10/28

// Package sms 发送告警短信：阿里云短信、腾讯云短信，以及通用 HTTP 短信网关。
//
// 云厂商短信必须使用审核通过的“签名 + 模板”：平台按配置的参数顺序把告警信息填入模板变量。
// 可用的变量：level（级别）、cluster（集群）、rule（规则）、inst（首个对象）、value（首个取值）、count（对象数）、time（时间）、status（告警 / 恢复）。
package sms

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/notify"
)

// Providers 是支持的服务商。
var Providers = []string{"http", "aliyun", "tencent"}

// ParamKeys 是模板变量可选的取值。
var ParamKeys = []string{"level", "cluster", "rule", "inst", "value", "count", "time", "status"}

// DefaultParams 是未配置变量顺序时使用的变量。
var DefaultParams = []string{"cluster", "rule", "level"}

// Config 是短信网关配置。
type Config struct {
	Provider string // http / aliyun / tencent；空 = 填了网关地址则按 http
	URL      string // http：网关地址（必填）；aliyun / tencent：接入点（可选，默认官方地址）
	KeyID    string // AccessKeyId / SecretId；http 网关可选（放在请求头 X-SROps-Key）
	Secret   string // AccessKeySecret / SecretKey；http 网关作为 Bearer Token（可选）
	Sign     string // 短信签名，如 【数据平台】
	Template string // 模板 CODE / 模板 ID；http 网关原样带上（可选）
	AppID    string // 腾讯云 SdkAppId
	Region   string // 腾讯云地域，默认 ap-guangzhou
	Params   []string
}

// Client 发送短信；Now / Nonce 可在测试中替换。
type Client struct {
	HTTP  *http.Client
	Now   func() time.Time
	Nonce func() string
}

// Default 复用通知模块的出网 HTTP 客户端（含平台设置里的代理）。
var Default = &Client{HTTP: notify.Default.HTTP, Now: time.Now, Nonce: func() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}}

var phoneRe = regexp.MustCompile(`^\+?[0-9]{6,15}$`)

// ValidPhone 校验手机号（国内 11 位或带国家码的国际号码）。
func ValidPhone(p string) bool { return phoneRe.MatchString(p) }

// SplitPhones 把逗号 / 空格 / 分号分隔的手机号拆开并校验，最多 20 个。
func SplitPhones(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '，' || r == '；' || r == ' ' || r == '\n' || r == '\t'
	}) {
		if !ValidPhone(f) {
			return nil, fmt.Errorf("手机号格式不正确：%s", Mask(f))
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("请填写至少一个手机号")
	}
	if len(out) > 20 {
		return nil, errors.New("一个渠道最多 20 个手机号")
	}
	return out, nil
}

// Mask 脱敏显示手机号：138****0000。
func Mask(p string) string {
	if len(p) <= 7 {
		return strings.Repeat("*", len(p))
	}
	return p[:3] + "****" + p[len(p)-4:]
}

// MaskList 脱敏显示手机号列表。
func MaskList(ps []string) string {
	m := make([]string, len(ps))
	for i, p := range ps {
		m[i] = Mask(p)
	}
	return strings.Join(m, ", ")
}

// provider 返回实际使用的服务商。
func (c Config) provider() string {
	if c.Provider == "" {
		return "http"
	}
	return c.Provider
}

// Check 检查配置是否完整。
func (c Config) Check() error {
	switch c.provider() {
	case "http":
		if strings.TrimSpace(c.URL) == "" {
			return errors.New("短信网关未配置：请到「平台设置 → 通知 → 短信网关」填写网关地址")
		}
		return notify.ValidURL(c.URL)
	case "aliyun", "tencent":
		name := map[string]string{"aliyun": "阿里云", "tencent": "腾讯云"}[c.provider()]
		switch {
		case c.KeyID == "" || c.Secret == "":
			return errors.New(name + "短信的 AccessKey 未配置：请到「平台设置 → 通知 → 短信网关」填写")
		case strings.Trim(c.Sign, "【】 ") == "":
			return errors.New("短信签名未配置")
		case c.Template == "":
			return errors.New("短信模板未配置")
		case c.provider() == "tencent" && c.AppID == "":
			return errors.New("腾讯云短信的 SdkAppId 未配置")
		}
		if c.URL != "" {
			return notify.ValidURL(c.URL)
		}
		return nil
	}
	return fmt.Errorf("不支持的短信服务商：%s", c.Provider)
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// paramValues 按配置的变量名取出本条消息的值（每个值最多 20 个字，云厂商对变量长度有限制）。
func paramValues(c Config, m notify.Message) ([]string, map[string]string) {
	keys := c.Params
	if len(keys) == 0 {
		keys = DefaultParams
	}
	vals := make([]string, len(keys))
	kv := map[string]string{}
	for i, k := range keys {
		v := clip(strings.TrimSpace(m.Params[k]), 20)
		if v == "" {
			v = "-"
		}
		vals[i], kv[k] = v, v
	}
	return vals, kv
}

// Send 发送一条短信给 phones。
func (cl *Client) Send(ctx context.Context, c Config, phones []string, m notify.Message) error {
	if err := c.Check(); err != nil {
		return err
	}
	if len(phones) == 0 {
		return errors.New("没有接收手机号")
	}
	switch c.provider() {
	case "aliyun":
		return cl.aliyun(ctx, c, phones, m)
	case "tencent":
		return cl.tencent(ctx, c, phones, m)
	}
	return cl.gateway(ctx, c, phones, m)
}

func (cl *Client) do(req *http.Request) ([]byte, int, error) {
	req.Header.Set("User-Agent", "sr-ops")
	resp, err := cl.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = fmt.Errorf("%s %s：%v", ue.Op, req.URL.Host, ue.Err)
		}
		return nil, 0, fmt.Errorf("连接短信服务失败：%v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return b, resp.StatusCode, nil
}

func short(b []byte) string {
	s := strings.TrimSpace(string(b))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// ---------- 通用 HTTP 网关 ----------
//
// POST <网关地址>，Content-Type: application/json
//
//	{"sign":"【数据平台】","phones":["13800000000"],"content":"【数据平台】【严重】…","title":"…","level":"bad",
//	 "template":"<模板>","params":{"cluster":"…"}}
//
// 配置了密钥时带请求头 Authorization: Bearer <密钥>；配置了 KeyID 时带 X-SROps-Key。
// HTTP 状态非 2xx，或返回 JSON 中 code / errcode 不是 0（或 "OK" / "success"）都视为失败。
func (cl *Client) gateway(ctx context.Context, c Config, phones []string, m notify.Message) error {
	_, kv := paramValues(c, m)
	content := m.SMS
	if content == "" {
		content = m.Title
	}
	if sg := strings.TrimSpace(c.Sign); sg != "" && !strings.HasPrefix(content, sg) {
		content = sg + content
	}
	body, _ := json.Marshal(map[string]any{"sign": c.Sign, "phones": phones, "content": content, "title": m.Title, "level": m.Level, "template": c.Template, "params": kv})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	if c.KeyID != "" {
		req.Header.Set("X-SROps-Key", c.KeyID)
	}
	rb, code, err := cl.do(req)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return fmt.Errorf("短信网关返回 HTTP %d：%s", code, short(rb))
	}
	var r map[string]any
	if json.Unmarshal(rb, &r) != nil {
		return nil // 非 JSON 的 2xx 视为成功
	}
	for _, k := range []string{"code", "errcode", "errCode", "ErrCode"} {
		v, ok := r[k]
		if !ok {
			continue
		}
		s := strings.ToLower(fmt.Sprint(v))
		if s == "0" || s == "200" || s == "ok" || s == "success" {
			return nil
		}
		msg := ""
		for _, mk := range []string{"msg", "message", "errmsg", "errMsg"} {
			if x, ok := r[mk]; ok {
				msg = fmt.Sprint(x)
				break
			}
		}
		return fmt.Errorf("短信网关返回错误 %s=%v：%s", k, v, msg)
	}
	return nil
}

// ---------- 阿里云短信（Dysmsapi 2017-05-25，RPC 签名 V1） ----------

func percent(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "+", "%20")
	e = strings.ReplaceAll(e, "*", "%2A")
	return strings.ReplaceAll(e, "%7E", "~")
}

func aliyunPhone(p string) string { return strings.TrimPrefix(strings.TrimPrefix(p, "+86"), "+") }

func (cl *Client) aliyun(ctx context.Context, c Config, phones []string, m notify.Message) error {
	_, kv := paramValues(c, m)
	tp, _ := json.Marshal(kv)
	var ph []string
	for _, p := range phones {
		ph = append(ph, aliyunPhone(p))
	}
	params := map[string]string{
		"AccessKeyId": c.KeyID, "Action": "SendSms", "Format": "JSON", "PhoneNumbers": strings.Join(ph, ","), "RegionId": "cn-hangzhou",
		"SignName": strings.Trim(c.Sign, "【】 "), "SignatureMethod": "HMAC-SHA1", "SignatureNonce": cl.Nonce(), "SignatureVersion": "1.0",
		"TemplateCode": c.Template, "TemplateParam": string(tp), "Timestamp": cl.Now().UTC().Format("2006-01-02T15:04:05Z"), "Version": "2017-05-25",
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = percent(k) + "=" + percent(params[k])
	}
	canon := strings.Join(pairs, "&")
	mac := hmac.New(sha1.New, []byte(c.Secret+"&"))
	mac.Write([]byte("POST&%2F&" + percent(canon)))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	endpoint := c.URL
	if endpoint == "" {
		endpoint = "https://dysmsapi.aliyuncs.com/"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(canon+"&Signature="+percent(sig)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rb, code, err := cl.do(req)
	if err != nil {
		return err
	}
	var r struct{ Code, Message string }
	if json.Unmarshal(rb, &r) != nil {
		return fmt.Errorf("阿里云短信返回无法解析（HTTP %d）：%s", code, short(rb))
	}
	if r.Code != "OK" {
		return fmt.Errorf("阿里云短信发送失败：%s %s", r.Code, r.Message)
	}
	return nil
}

// ---------- 腾讯云短信（sms 2021-01-11，TC3-HMAC-SHA256） ----------

func hmac256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func tencentPhone(p string) string {
	if strings.HasPrefix(p, "+") {
		return p
	}
	if len(p) == 11 && p[0] == '1' {
		return "+86" + p
	}
	return "+" + p
}

func (cl *Client) tencent(ctx context.Context, c Config, phones []string, m notify.Message) error {
	vals, _ := paramValues(c, m)
	var ph []string
	for _, p := range phones {
		ph = append(ph, tencentPhone(p))
	}
	body, _ := json.Marshal(map[string]any{"PhoneNumberSet": ph, "SmsSdkAppId": c.AppID, "SignName": strings.Trim(c.Sign, "【】 "), "TemplateId": c.Template, "TemplateParamSet": vals})
	endpoint := c.URL
	if endpoint == "" {
		endpoint = "https://sms.tencentcloudapi.com"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return errors.New("短信接入点格式不正确")
	}
	region := c.Region
	if region == "" {
		region = "ap-guangzhou"
	}
	now := cl.Now().UTC()
	ts, date := strconv.FormatInt(now.Unix(), 10), now.Format("2006-01-02")
	const ct = "application/json; charset=utf-8"
	canonical := "POST\n/\n\ncontent-type:" + ct + "\nhost:" + u.Host + "\nx-tc-action:sendsms\n\ncontent-type;host;x-tc-action\n" + hexSHA256(body)
	scope := date + "/sms/tc3_request"
	toSign := "TC3-HMAC-SHA256\n" + ts + "\n" + scope + "\n" + hexSHA256([]byte(canonical))
	key := hmac256(hmac256(hmac256([]byte("TC3"+c.Secret), date), "sms"), "tc3_request")
	sig := hex.EncodeToString(hmac256(key, toSign))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Host", u.Host)
	req.Header.Set("X-TC-Action", "SendSms")
	req.Header.Set("X-TC-Version", "2021-01-11")
	req.Header.Set("X-TC-Timestamp", ts)
	req.Header.Set("X-TC-Region", region)
	req.Header.Set("Authorization", "TC3-HMAC-SHA256 Credential="+c.KeyID+"/"+scope+", SignedHeaders=content-type;host;x-tc-action, Signature="+sig)
	rb, code, err := cl.do(req)
	if err != nil {
		return err
	}
	var r struct {
		Response struct {
			Error *struct{ Code, Message string }
			Set   []struct{ Code, Message, PhoneNumber string } `json:"SendStatusSet"`
		}
	}
	if json.Unmarshal(rb, &r) != nil {
		return fmt.Errorf("腾讯云短信返回无法解析（HTTP %d）：%s", code, short(rb))
	}
	if e := r.Response.Error; e != nil {
		return fmt.Errorf("腾讯云短信发送失败：%s %s", e.Code, e.Message)
	}
	var fails []string
	for _, st := range r.Response.Set {
		if !strings.EqualFold(st.Code, "ok") {
			fails = append(fails, Mask(strings.TrimPrefix(st.PhoneNumber, "+86"))+" "+st.Code+" "+st.Message)
		}
	}
	if len(fails) > 0 {
		return fmt.Errorf("腾讯云短信发送失败：%s", strings.Join(fails, "；"))
	}
	if len(r.Response.Set) == 0 {
		return fmt.Errorf("腾讯云短信返回为空（HTTP %d）：%s", code, short(rb))
	}
	return nil
}
