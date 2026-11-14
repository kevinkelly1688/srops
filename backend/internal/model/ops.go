// @author: kevinkelly
// @license: Apache Licence
// @file: ops.go
// @time: 2026/10/28

package model

import "time"

// BackupPolicy 是平台按计划发起的备份：到点对每个库执行 BACKUP SNAPSHOT。
type BackupPolicy struct {
	ID         string    `json:"id"`
	Cluster    string    `json:"cluster"`
	Name       string    `json:"name"`
	DBs        []string  `json:"dbs"`
	Repo       string    `json:"repo"`
	Every      string    `json:"every"`   // daily / weekly
	At         string    `json:"at"`      // HH:MM
	Weekday    int       `json:"weekday"` // 0=周日 … 6=周六（weekly 时有效）
	Enabled    bool      `json:"enabled"`
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
	LastRun    time.Time `json:"lastRun"`
	LastResult string    `json:"lastResult"`

	// 保留策略：只清理本策略创建的快照（名称以 auto_<策略ID>_ 开头）。两项都为 0 表示不清理；
	// 都设置时，同时满足“超出最近 KeepLast 个”和“超过 KeepDays 天”才会删除；最新的一个快照永远保留。
	KeepLast    int       `json:"keepLast"`
	KeepDays    int       `json:"keepDays"`
	LastClean   time.Time `json:"lastClean"`
	CleanResult string    `json:"cleanResult"`
}

// RepoCred 是备份仓库（对象存储）的清理凭据：StarRocks 没有删除快照的语句，只能由平台直接删除对象存储里的快照目录。
// 凭据加密保存，只用于列出和删除该仓库目录下的对象；建议使用只对该前缀有读写权限的子账号。
type RepoCred struct {
	Cluster   string    `json:"cluster"`
	Repo      string    `json:"repo"`
	Endpoint  string    `json:"endpoint"`
	Region    string    `json:"region"`
	PathStyle bool      `json:"pathStyle"`
	AKEnc     string    `json:"akEnc"`
	SKEnc     string    `json:"skEnc"`
	AKMasked  string    `json:"akMasked"`
	By        string    `json:"by"`
	At        time.Time `json:"at"`
}

// ParamChange 是一次参数修改记录。
type ParamChange struct {
	At      time.Time `json:"at"`
	User    string    `json:"user"`
	Cluster string    `json:"cluster"`
	Kind    string    `json:"kind"`  // fe / be / var
	Scope   string    `json:"scope"` // 如「全部 FE」「be-2」「GLOBAL」
	Key     string    `json:"key"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Persist string    `json:"persist"`
	Status  string    `json:"status"`
}
