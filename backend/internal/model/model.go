// @author: kevinkelly
// @license: Apache Licence
// @file: model.go
// @time: 2026/10/28

// Package model 定义平台持久化与 API 共用的数据结构。JSON 字段名与前端原型保持一致。
package model

import "time"

// Cluster 是一个已导入（纳管）的 StarRocks 集群。密码类字段只以密文保存，API 输出时清空。
type Cluster struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Env          string     `json:"env"`
	Owner        string     `json:"owner"`
	Desc         string     `json:"desc"`
	FEHosts      []string   `json:"feHosts"`
	QueryPort    int        `json:"queryPort"`
	HTTPPort     int        `json:"httpPort"`
	User         string     `json:"user"`
	PasswordEnc  string     `json:"passwordEnc,omitempty"`
	Version      string     `json:"version"`
	Mode         string     `json:"mode"` // shared-nothing / shared-data
	Prom         string     `json:"prom"`
	PromSelector string     `json:"promSelector"` // 例如 job="StarRocks_prod"
	SSH          *SSHConfig `json:"ssh"`
	Dirs         *Dirs      `json:"dirs"`
	AuditTable   string     `json:"audit"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// SSHConfig 运维必填：节点启停、系统巡检等通过 SSH 执行。
type SSHConfig struct {
	User        string `json:"user"`
	Port        int    `json:"port"`
	Auth        string `json:"auth"` // key / password
	PasswordEnc string `json:"passwordEnc,omitempty"`
	Sudo        bool   `json:"sudo"`
}

// Dirs 运维必填：StarRocks 部署目录。
type Dirs struct {
	FE string `json:"fe"`
	BE string `json:"be"`
}

// Missing 返回运维必填但未填写的信息；为空表示可运维。
func (c *Cluster) Missing() []string {
	var m []string
	if c.SSH == nil {
		m = append(m, "SSH 凭据")
	}
	if c.Dirs == nil {
		m = append(m, "部署目录")
	}
	return m
}

// Public 返回去掉密文的副本。
func (c Cluster) Public() Cluster {
	c.PasswordEnc = ""
	if c.SSH != nil {
		s := *c.SSH
		s.PasswordEnc = ""
		c.SSH = &s
	}
	return c
}

// Node 是集群中的一个 FE / BE / CN 节点（字段与前端 NODES 对齐，未知数值为 nil）。
type Node struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"` // fe / be / cn
	Role       string   `json:"role,omitempty"`
	Host       string   `json:"host"`
	Port       int      `json:"port"` // FE: query_port；BE/CN: heartbeat_service_port
	EditLog    int      `json:"editLog,omitempty"`
	RPC        int      `json:"rpc,omitempty"`
	HTTP       int      `json:"http"`
	BEPort     int      `json:"bePort,omitempty"`
	BRPC       int      `json:"brpc,omitempty"`
	Starlet    int      `json:"starlet,omitempty"`
	Status     string   `json:"status"` // up / warn / down / decommission
	Alive      bool     `json:"alive"`
	Version    string   `json:"version"`
	CPU        *float64 `json:"cpu"`
	Mem        *float64 `json:"mem"`
	Disk       *float64 `json:"disk"`
	JVM        *float64 `json:"jvm,omitempty"`
	Journal    int64    `json:"journal,omitempty"`
	Replayed   int64    `json:"replayed,omitempty"`
	Tablets    int64    `json:"tablets,omitempty"`
	DataUsed   *float64 `json:"dataUsed,omitempty"`  // TB
	DataTotal  *float64 `json:"dataTotal,omitempty"` // TB
	UsedBytes  float64  `json:"-"`
	TotalBytes float64  `json:"-"`
	Compaction *float64 `json:"compaction,omitempty"`
	Warehouse  string   `json:"warehouse,omitempty"`
	StartTime  string   `json:"startTime"`
	HB         string   `json:"hb"`
	ErrMsg     string   `json:"errMsg,omitempty"`
	Dir        string   `json:"dir"`
	Running    int      `json:"runningQueries,omitempty"`
	Cores      int      `json:"cores,omitempty"` // CPU 核数（BE / CN）
}

// Topology 是集群架构。
type Topology struct {
	ClusterID   string    `json:"clusterId"`
	Version     string    `json:"version"`
	Mode        string    `json:"mode"`
	Nodes       []Node    `json:"nodes"`
	Catalogs    []Catalog `json:"catalogs"`
	CollectedAt time.Time `json:"collectedAt"`
	Warnings    []string  `json:"warnings"`
}

type Catalog struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Point 是一个趋势采样点。
type Point struct {
	T   int64    `json:"t"` // unix 秒
	QPS *float64 `json:"qps"`
	P99 *float64 `json:"p99"`
	P95 *float64 `json:"p95"`
}

// Overview 是集群总览。
type Overview struct {
	ClusterID   string      `json:"clusterId"`
	CollectedAt time.Time   `json:"collectedAt"`
	KPI         KPI         `json:"kpi"`
	Trend       Trend       `json:"trend"`
	Nodes       []Node      `json:"nodes"`
	Info        []KV        `json:"info"`
	LastReport  *ReportMeta `json:"lastReport"`
	Warnings    []string    `json:"warnings"`

	// 资源组实时用量（SHOW USAGE RESOURCE GROUPS，3.1.4+）；nil = 未采集
	ResourceGroups []RGUsage `json:"resourceGroups"`
	TotalCores     int       `json:"totalCores"` // 全部 BE / CN 的 CPU 核数
}

// RGUsage 是一个资源组在所有 BE / CN 上的用量合计。
type RGUsage struct {
	Name     string  `json:"name"`
	CPUCores float64 `json:"cpuCores"`
	MemBytes float64 `json:"memBytes"`
	Running  int     `json:"running"`
}

type KPI struct {
	QPS          *float64 `json:"qps"`
	P99          *float64 `json:"p99"`
	Connections  *float64 `json:"connections"`
	FE           Count    `json:"fe"`
	BE           Count    `json:"be"`
	CN           Count    `json:"cn"`
	StorageUsed  *float64 `json:"storageUsed"`  // 字节
	StorageTotal *float64 `json:"storageTotal"` // 字节
	MaxDiskPct   *float64 `json:"maxDiskPct"`
	Unhealthy    *int64   `json:"unhealthyTablets"`
	RLRunning    *int     `json:"routineLoadRunning"`
	RLPaused     *int     `json:"routineLoadPaused"`
	MVTotal      *int     `json:"mvTotal"`
	MVFailed     *int     `json:"mvFailed"`
}

type Count struct {
	Total int `json:"total"`
	Alive int `json:"alive"`
}

type Trend struct {
	Source string  `json:"source"` // prometheus / sampler / ""
	Points []Point `json:"points"`
}

type KV struct {
	K string `json:"k"`
	V string `json:"v"`
}

// ---------- 巡检 ----------

// Item 是一条巡检结果。
type Item struct {
	ID        string   `json:"id"`
	Cat       string   `json:"cat"`
	Name      string   `json:"name"`
	Src       string   `json:"src"`
	Value     string   `json:"val"`
	Threshold string   `json:"th"`
	Status    string   `json:"st"` // ok / warn / bad / info / skip / error
	Advice    string   `json:"adv"`
	Doc       string   `json:"doc"`
	Details   []Detail `json:"details,omitempty"`
	Ignored   bool     `json:"ignored,omitempty"`
}

type Detail struct {
	Target string `json:"target"`
	Value  string `json:"value"`
	Status string `json:"st"`
}

type ReportMeta struct {
	ID         string         `json:"id"`
	ClusterID  string         `json:"clusterId"`
	At         time.Time      `json:"at"`
	By         string         `json:"by"`
	DurationMs int64          `json:"durationMs"`
	Score      int            `json:"score"`
	Counts     map[string]int `json:"counts"`
}

type Report struct {
	ReportMeta
	Items []Item `json:"items"`
}

// Rescore 按“异常 −4、警告 −1，忽略项不计”重新计算健康分与计数。
func (r *Report) Rescore() {
	r.Counts = map[string]int{}
	score := 100
	for _, it := range r.Items {
		if it.Ignored {
			r.Counts["ignored"]++
			continue
		}
		r.Counts[it.Status]++
		switch it.Status {
		case "bad":
			score -= 4
		case "warn":
			score--
		}
	}
	if score < 0 {
		score = 0
	}
	r.Score = score
}

// RuleConfig 是对内置规则的覆盖配置。
type RuleConfig struct {
	Enabled *bool    `json:"enabled,omitempty"`
	Warn    *float64 `json:"warn,omitempty"`
	Bad     *float64 `json:"bad,omitempty"`
}

// Schedule 是集群的定时巡检配置。
type Schedule struct {
	Enabled bool      `json:"enabled"`
	Every   string    `json:"every"` // daily / hours
	At      string    `json:"at"`    // HH:MM（daily）
	Hours   int       `json:"hours"` // 间隔小时（hours）
	Cats    []string  `json:"cats"`  // 空 = 全部
	LastRun time.Time `json:"lastRun"`
	Notify  *Notify   `json:"notify,omitempty"` // 结果通知；nil = 不通知

	LastNotify       time.Time `json:"lastNotify"`
	LastNotifyResult string    `json:"lastNotifyResult,omitempty"`
}

// Notify 是定时巡检的结果通知：发送到群机器人 / Webhook。
// URL 中通常带有机器人 token，与签名密钥一样加密保存，接口只返回脱敏后的地址。
type Notify struct {
	Type     string `json:"type"`     // 企业微信 / 钉钉 / 飞书 / Lark / Slack / Webhook
	When     string `json:"when"`     // always 每次都发 / problem 仅有问题时
	MinScore int    `json:"minScore"` // when=problem：有异常项或健康分低于该值时发送

	URLEnc    string `json:"urlEnc,omitempty"`
	SecretEnc string `json:"secretEnc,omitempty"`

	URLMasked string `json:"urlMasked,omitempty"` // 脱敏后的地址，用于显示

	// 以下仅用于接口：URL / Secret 为提交的明文（留空 = 保持不变），HasSecret 为返回值
	URL         string `json:"url,omitempty"`
	Secret      string `json:"secret,omitempty"`
	ClearSecret bool   `json:"clearSecret,omitempty"`
	HasSecret   bool   `json:"hasSecret,omitempty"`
}

// Public 返回去掉密文的副本。
func (s Schedule) Public() Schedule {
	if s.Notify != nil {
		n := *s.Notify
		n.HasSecret = n.SecretEnc != ""
		n.URLEnc, n.SecretEnc, n.URL, n.Secret, n.ClearSecret = "", "", "", "", false
		s.Notify = &n
	}
	return s
}

// ---------- 用户 ----------

type User struct {
	Login      string    `json:"login"`
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	Phone      string    `json:"phone,omitempty"`
	Dept       string    `json:"dept,omitempty"`
	Roles      []string  `json:"roles"`
	PwdHash    string    `json:"pwdHash"`
	MustChange bool      `json:"mustChange"`
	Locked     bool      `json:"locked"`
	ExpireAt   string    `json:"expireAt,omitempty"` // YYYY-MM-DD，当天结束后不能登录；空 = 永久
	PwdAt      time.Time `json:"pwdAt"`              // 最近一次设置密码的时间（用于密码过期）
	LastLogin  time.Time `json:"lastLogin"`
	CreatedAt  time.Time `json:"createdAt"`
	CreatedBy  string    `json:"createdBy,omitempty"`
}

// Expired 判断账号是否已过有效期（按服务器本地时间，有效期当天 24 点前仍可登录）。
func (u User) Expired(now time.Time) bool {
	if u.ExpireAt == "" {
		return false
	}
	t, err := time.ParseInLocation("2006-01-02", u.ExpireAt, now.Location())
	return err == nil && !now.Before(t.AddDate(0, 0, 1))
}

// Role 是角色：功能权限（权限码）+ 数据范围（集群 ID，"*" 表示全部集群）。
// 内置超级管理员 r_admin 不存储，固定拥有全部权限。
type Role struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Desc      string    `json:"desc"`
	Builtin   bool      `json:"builtin"`
	Clusters  []string  `json:"clusters"`
	Perms     []string  `json:"perms"`
	CreatedAt time.Time `json:"createdAt"`
}
