// @author: kevinkelly
// @license: Apache Licence
// @file: task.go
// @time: 2026/10/28

package model

import "time"

// Task 是一次后台任务：节点启停、部署、升级等需要较长时间、过程中要看日志的操作。
type Task struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"` // node.start / node.stop / node.restart / deploy / upgrade ...
	Cluster string            `json:"cluster"`
	Title   string            `json:"title"`
	By      string            `json:"by"`
	State   string            `json:"state"` // running / paused / done / failed / canceled
	Prog    int               `json:"prog"`
	Err     string            `json:"err,omitempty"`
	Start   time.Time         `json:"start"`
	End     time.Time         `json:"end"`
	Data    map[string]string `json:"data,omitempty"`
	Log     []string          `json:"log,omitempty"`
}

// Finished 任务是否已结束。
func (t Task) Finished() bool {
	return t.State == "done" || t.State == "failed" || t.State == "canceled"
}

// NodeBL 记录节点加入黑名单的原因（StarRocks 自身不保存原因）。
type NodeBL struct {
	Cluster string    `json:"cluster"`
	Kind    string    `json:"kind"` // be / cn
	ID      string    `json:"id"`   // BackendId / ComputeNodeId
	Host    string    `json:"host"`
	Reason  string    `json:"reason"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

// Host 是平台纳管的主机（部署 / 扩容的目标）。平台通过自己的 SSH 密钥登录，不保存主机密码。
type Host struct {
	IP        string    `json:"ip"`
	Name      string    `json:"name"`
	Port      int       `json:"port"`
	User      string    `json:"user"`
	Sudo      bool      `json:"sudo"`
	CPU       int       `json:"cpu"`
	MemGB     int       `json:"mem"`
	DiskGB    int       `json:"diskGB"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"` // amd64 / arm64
	AVX2      bool      `json:"avx2"`
	CIDR      string    `json:"cidr"` // 主机 IP 所在网段，用于 priority_networks
	SSH       string    `json:"ssh"`  // ok 或失败原因
	CheckedAt time.Time `json:"checkedAt"`
	AddedBy   string    `json:"addedBy"`
	AddedAt   time.Time `json:"addedAt"`
	UsedBy    []string  `json:"usedBy,omitempty"` // 部署 / 扩容时记录的集群
}

// Package 是上传到平台的安装包（StarRocks 或 JDK）。
type Package struct {
	Name string    `json:"name"`
	Kind string    `json:"kind"` // starrocks / jdk
	Ver  string    `json:"ver"`
	Arch string    `json:"arch"`
	Size int64     `json:"size"`
	SHA  string    `json:"sha"`
	At   time.Time `json:"at"`
	By   string    `json:"by"`
}

// UpgradePlan 是一个集群的升级计划：滚动升级与回滚共用同一份记录。
type UpgradePlan struct {
	ID             string    `json:"id"`
	Cluster        string    `json:"cluster"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	Package        string    `json:"package"`
	Interval       int       `json:"interval"`       // 每台节点完成后等待的秒数
	DisableBalance bool      `json:"disableBalance"` // 升级期间暂停 Tablet 均衡
	State          string    `json:"state"`          // pending / running / paused / done / failed / rollingback / rolledback
	Note           string    `json:"note"`
	By             string    `json:"by"`
	At             time.Time `json:"at"`
	Updated        time.Time `json:"updated"`
	TaskID         string    `json:"taskId"`
	Cur            string    `json:"cur"`  // 正在处理的节点（kind|host:port）
	Done           []string  `json:"done"` // 已处理完成的节点
	PrevBalance    string    `json:"prevBalance,omitempty"`
}
