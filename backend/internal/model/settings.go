// @author: kevinkelly
// @license: Apache Licence
// @file: settings.go
// @time: 2026/10/28

package model

import "time"

// Settings 是平台设置（平台设置菜单）。JSON 字段名与前端 SETTINGS / NOTIFY_CFG / BASIC_CFG / WATERMARK 保持一致。
type Settings struct {
	Security  SecuritySettings `json:"security"`
	Notify    NotifySettings   `json:"notify"`
	Basic     BasicSettings    `json:"basic"`
	Watermark Watermark        `json:"watermark"`
}

type SecuritySettings struct {
	NoPermBtn       string `json:"noPermBtn"`       // disable 置灰并显示锁 / hide 隐藏
	HighRiskConfirm bool   `json:"highRiskConfirm"` // 高危操作执行前输入名称确认（界面行为）
	DualReview      bool   `json:"dualReview"`      // 高危操作需另一人批准后执行（服务端强制）
	SessionTimeout  int    `json:"sessionTimeout"`  // 无操作自动登出，分钟
	PwdMinLen       int    `json:"pwdMinLen"`
	PwdExpireDays   int    `json:"pwdExpireDays"` // 0 = 永不过期
	LoginFailLock   int    `json:"loginFailLock"` // 连续失败几次锁定 30 分钟
	IPWhitelist     string `json:"ipWhitelist"`   // 逗号分隔的 IP / CIDR；空 = 不限制
}

type NotifySettings struct {
	SMTPHost       string `json:"smtpHost"`
	SMTPPort       int    `json:"smtpPort"`
	SMTPSec        string `json:"smtpSec"` // SSL / STARTTLS / none
	SMTPSkipVerify bool   `json:"smtpSkipVerify"`
	SMTPUser       string `json:"smtpUser"`
	SMTPPwdEnc     string `json:"smtpPwdEnc,omitempty"`
	From           string `json:"from"`
	FromName       string `json:"fromName"`
	SMSURL         string `json:"smsUrl"`      // http 网关地址；阿里云 / 腾讯云为可选的接入点
	SMSSign        string `json:"smsSign"`     // 短信签名
	SMSProvider    string `json:"smsProvider"` // http / aliyun / tencent；空 = 按 http
	SMSKeyID       string `json:"smsKeyId"`
	SMSSecretEnc   string `json:"smsSecretEnc,omitempty"`
	SMSTemplate    string `json:"smsTemplate"`
	SMSAppID       string `json:"smsAppId"`
	SMSRegion      string `json:"smsRegion"`
	SMSParams      string `json:"smsParams"` // 模板变量及顺序，逗号分隔，如 cluster,rule,level
	MergeMin       int    `json:"mergeMin"`
	RepeatMin      int    `json:"repeatMin"`
	Recover        bool   `json:"recover"`
	Quiet          bool   `json:"quiet"`
	QuietFrom      string `json:"quietFrom"`
	QuietTo        string `json:"quietTo"`
}

type BasicSettings struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Proxy      string `json:"proxy"`
	NoProxy    string `json:"noProxy"`
	Interval   int    `json:"interval"`   // 指标采集间隔，秒
	ReportDays int    `json:"reportDays"` // 巡检报告保留
	AuditDays  int    `json:"auditDays"`  // 审计日志保留（不少于 180）
}

type Watermark struct {
	On      bool    `json:"on"`
	Text    string  `json:"text"`
	Text2   string  `json:"text2"`
	Size    int     `json:"size"`
	Opacity float64 `json:"opacity"`
	Angle   int     `json:"angle"`
	Gap     int     `json:"gap"`
	Color   string  `json:"color"`
}

// DefaultSettings 是首次部署时的默认设置。
func DefaultSettings() Settings {
	return Settings{
		Security: SecuritySettings{NoPermBtn: "disable", HighRiskConfirm: true, SessionTimeout: 30, PwdMinLen: 10, PwdExpireDays: 90, LoginFailLock: 5},
		Notify: NotifySettings{SMTPPort: 465, SMTPSec: "SSL", FromName: "StarRocks 运维平台", MergeMin: 5, RepeatMin: 60, Recover: true,
			QuietFrom: "23:00", QuietTo: "08:00"},
		Basic:     BasicSettings{Name: "StarRocks 运维平台", Interval: 15, ReportDays: 90, AuditDays: 180},
		Watermark: Watermark{On: true, Text: "{name} {login}", Text2: "{date} {time} · 内部资料 严禁外传", Size: 14, Opacity: 0.1, Angle: -22, Gap: 220, Color: "#808080"},
	}
}

// Review 是待复核（双人复核）的高危操作申请。批准后由服务端以申请人的身份执行原请求。
type Review struct {
	ID        string            `json:"id"`
	Code      string            `json:"code"` // 权限码
	Title     string            `json:"title"`
	Cluster   string            `json:"cluster"`
	Requester string            `json:"requester"`
	IP        string            `json:"ip"`
	CreatedAt time.Time         `json:"createdAt"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	PathVals  map[string]string `json:"pathVals,omitempty"`
	Body      string            `json:"body,omitempty"`

	Status    string    `json:"status"` // pending / approved / rejected / withdrawn / expired / failed
	Reviewer  string    `json:"reviewer,omitempty"`
	DecidedAt time.Time `json:"decidedAt"`
	Note      string    `json:"note,omitempty"` // 驳回原因 / 执行结果
}
