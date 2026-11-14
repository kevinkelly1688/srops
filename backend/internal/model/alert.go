// @author: kevinkelly
// @license: Apache Licence
// @file: alert.go
// @time: 2026/10/28

package model

import "time"

// AlertRuleCfg 是对内置告警规则的覆盖配置（阈值、持续时间、启停、通知渠道）；nil 表示沿用默认。
type AlertRuleCfg struct {
	Enabled  *bool     `json:"enabled,omitempty"`
	Warn     *float64  `json:"warn,omitempty"`
	Crit     *float64  `json:"crit,omitempty"`
	DurMin   *int      `json:"durMin,omitempty"`
	Channels []string  `json:"channels,omitempty"` // 为空表示发送到所有启用的渠道
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at,omitempty"`
}

// Alert 是一条告警（同一集群、规则、对象在持续异常期间只有一条）。
type Alert struct {
	ID          string    `json:"id"`
	Cluster     string    `json:"cluster"`
	Rule        string    `json:"rule"`
	Name        string    `json:"name"`
	Inst        string    `json:"inst"`
	Level       string    `json:"level"` // critical / warning / info
	Value       string    `json:"value"`
	Desc        string    `json:"desc"`
	State       string    `json:"state"` // firing / acked / resolved
	FirstAt     time.Time `json:"firstAt"`
	LastAt      time.Time `json:"lastAt"` // 最近一次评估仍然异常的时间
	AckBy       string    `json:"ackBy,omitempty"`
	AckAt       time.Time `json:"ackAt,omitempty"`
	ResolvedAt  time.Time `json:"resolvedAt,omitempty"`
	Notified    time.Time `json:"notified,omitempty"` // 最近一次成功发出通知的时间
	NotifyN     int       `json:"notifyN,omitempty"`
	RecoverSent bool      `json:"recoverSent,omitempty"`
	NotifyErr   string    `json:"notifyErr,omitempty"`
	NotifyTry   time.Time `json:"notifyTry,omitempty"` // 最近一次尝试发送的时间（失败后 5 分钟内不重试）
}

// Silence 是静默规则：匹配的告警不再发送通知（仍会显示）。
type Silence struct {
	ID      string    `json:"id"`
	Cluster string    `json:"cluster"` // 为空 = 全部集群
	Rule    string    `json:"rule"`    // 为空 = 全部规则
	Inst    string    `json:"inst"`    // 对象包含该文本；为空 = 全部对象
	Reason  string    `json:"reason"`
	By      string    `json:"by"`
	From    time.Time `json:"from"`
	Until   time.Time `json:"until"`
}

// Channel 是告警通知渠道。地址与密钥加密保存。
type Channel struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	URLEnc     string    `json:"urlEnc,omitempty"`
	URLMasked  string    `json:"url"`
	SecretEnc  string    `json:"secretEnc,omitempty"`
	HasSecret  bool      `json:"hasSecret"`
	Levels     []string  `json:"levels"` // critical / warning / info
	Enabled    bool      `json:"enabled"`
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
	LastAt     time.Time `json:"lastAt,omitempty"`
	LastResult string    `json:"lastResult,omitempty"`
}

// CustomRule 是用户自定义的 PromQL 告警规则：平台每分钟对配置了 Prometheus 的集群执行一次即时查询，
// 返回的每条序列是一个告警对象，按“比较方式 + 阈值”判定级别。
type CustomRule struct {
	ID       string    `json:"id"` // custom_ 开头
	Name     string    `json:"name"`
	Expr     string    `json:"expr"` // 可用 $sel 代表该集群的 Prometheus 选择器
	Desc     string    `json:"desc,omitempty"`
	Unit     string    `json:"unit,omitempty"`
	Cluster  string    `json:"cluster"` // 为空 = 所有配置了 Prometheus 的集群
	Op       string    `json:"op"`      // >= > <= <
	Warn     *float64  `json:"warn,omitempty"`
	Crit     float64   `json:"crit"`
	DurMin   int       `json:"durMin"`
	Enabled  bool      `json:"enabled"`
	Channels []string  `json:"channels"`
	By       string    `json:"by"`
	At       time.Time `json:"at"`
}
