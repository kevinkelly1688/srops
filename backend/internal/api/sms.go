// @author: kevinkelly
// @license: Apache Licence
// @file: sms.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/notify"
	"starrocks-ops/internal/sms"
)

var (
	smsKeyRe    = regexp.MustCompile(`^[A-Za-z0-9._\-]{0,128}$`)
	smsAppRe    = regexp.MustCompile(`^[0-9]{1,20}$`)
	smsRegionRe = regexp.MustCompile(`^[a-z0-9\-]{1,32}$`)
)

// parseSMSParams 解析模板变量列表（逗号分隔），只允许预定义的变量名，最多 8 个、不重复。
func parseSMSParams(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '，' || r == ' ' }) {
		if !contains(sms.ParamKeys, f) {
			return nil, bad("模板变量 " + f + " 不支持，可用：" + strings.Join(sms.ParamKeys, "、"))
		}
		if seen[f] {
			return nil, bad("模板变量 " + f + " 重复")
		}
		seen[f] = true
		out = append(out, f)
	}
	if len(out) > 8 {
		return nil, bad("模板变量最多 8 个")
	}
	return out, nil
}

// smsConfig 把设置转为短信配置（解密密钥）。
func (s *Server) smsConfig(n model.NotifySettings) (sms.Config, error) {
	params, _ := parseSMSParams(n.SMSParams)
	c := sms.Config{Provider: n.SMSProvider, URL: n.SMSURL, KeyID: n.SMSKeyID, Sign: n.SMSSign, Template: n.SMSTemplate, AppID: n.SMSAppID, Region: n.SMSRegion, Params: params}
	if n.SMSSecretEnc != "" {
		sec, err := s.Box.Decrypt(n.SMSSecretEnc)
		if err != nil {
			return c, fmt.Errorf("短信密钥解密失败：%v", err)
		}
		c.Secret = sec
	}
	return c, nil
}

// SendSMS 用平台设置中的短信网关发送短信。
func (s *Server) SendSMS(ctx context.Context, phones []string, m notify.Message) error {
	c, err := s.smsConfig(s.cfg().Notify)
	if err != nil {
		return err
	}
	return sms.Default.Send(ctx, c, phones, m)
}

// testSMS 用表单中的（尚未保存的）短信网关配置向指定手机号发送一条测试短信；密钥留空时沿用已保存的。
func (s *Server) testSMS(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		notifyIn
		Phone string `json:"phone"`
	}
	if !decode(w, r, &in) {
		return
	}
	phones, err := sms.SplitPhones(in.Phone)
	if err == nil && len(phones) > 3 {
		err = bad("测试短信最多发给 3 个号码")
	}
	if err == nil {
		err = checkNotify(in.NotifySettings)
	}
	if err != nil {
		if _, isAPI := err.(apiErr); !isAPI {
			err = bad(err.Error())
		}
		s.audit(r, u, "", "setting.notify", "发送测试短信", err)
		writeErr(w, err)
		return
	}
	n := in.NotifySettings
	n.SMSSecretEnc = s.cfg().Notify.SMSSecretEnc
	c, err := s.smsConfig(n)
	if err == nil {
		switch {
		case in.ClearSMSSecret:
			c.Secret = ""
		case in.SMSSecret != "":
			c.Secret = in.SMSSecret
		}
		now := time.Now()
		m := notify.Message{Title: "【测试】StarRocks 运维平台短信测试", Level: "warn", SMS: "【测试】这是一条来自 StarRocks 运维平台的测试短信，由 " + u.Login + " 发送。",
			Params: map[string]string{"level": "测试", "cluster": "测试集群", "rule": "短信测试", "inst": "-", "value": "-", "count": "1", "time": now.Format("15:04"), "status": "测试"}}
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		if err = sms.Default.Send(ctx, c, phones, m); err != nil {
			err = apiErr{http.StatusBadGateway, err.Error()}
		}
	}
	s.audit(r, u, "", "setting.notify", "发送测试短信到 "+sms.MaskList(phones), err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]any{"ok": true, "to": sms.MaskList(phones)})
}

// smsText 是短信正文（不含签名）：级别 + 集群 + 规则 + 首个对象（+ 对象数）。
func smsText(tag, cluster, rule string, list []model.Alert, recovered bool) string {
	first := ""
	if len(list) > 0 {
		first = list[0].Inst
	}
	t := tag + cluster + " · " + rule
	if first != "" {
		t += "：" + first
	}
	if len(list) > 1 {
		t += fmt.Sprintf(" 等 %d 项", len(list))
	}
	if !recovered {
		t += "，请登录运维平台处理"
	}
	if r := []rune(t); len(r) > 120 {
		t = string(r[:119]) + "…"
	}
	return t
}

// smsParams 是云厂商短信模板的变量取值。
func smsParams(tag, cluster, rule string, list []model.Alert, recovered bool, now time.Time) map[string]string {
	p := map[string]string{"level": strings.Trim(tag, "【】"), "cluster": cluster, "rule": rule, "count": fmt.Sprint(len(list)), "time": now.Local().Format("15:04"), "status": "告警"}
	if recovered {
		p["status"] = "恢复"
	}
	if len(list) > 0 {
		p["inst"], p["value"] = list[0].Inst, list[0].Value
	}
	return p
}
