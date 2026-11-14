// @author: kevinkelly
// @license: Apache Licence
// @file: rule.go
// @time: 2026/10/28

// Package inspect 是性能巡检引擎：规则 = 数据来源 + 判定阈值 + 处置建议。
// 结果状态：ok 正常 / warn 警告 / bad 异常 / info 建议 / skip 跳过（前置条件不满足）/ error 采集失败。
package inspect

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"starrocks-ops/internal/model"
)

var Cats = []string{"节点健康", "资源水位", "数据健康", "Compaction", "查询性能", "导入作业", "物化视图", "配置合规", "系统环境"}

// Rule 是一条巡检规则。Warn / Bad 为 nil 表示该级别不适用；Lower=true 表示数值越小越差。
type Rule struct {
	ID, Cat, Name, Src, Doc, Unit string
	Warn, Bad                     *float64
	Lower                         bool
	Advice                        string
	Check                         func(e *Env, r *Rule) model.Item
}

func th(v float64) *float64 { return &v }

// judge 按阈值判定。
func (r *Rule) judge(v float64) string {
	worse := func(t *float64) bool {
		if t == nil {
			return false
		}
		if r.Lower {
			return v < *t
		}
		return v >= *t
	}
	switch {
	case worse(r.Bad):
		return "bad"
	case worse(r.Warn):
		return "warn"
	}
	return "ok"
}

// ThText 返回阈值说明，例如“警告 ≥ 80%，异常 ≥ 85%”。
func (r *Rule) ThText() string {
	op := "≥"
	if r.Lower {
		op = "<"
	}
	var p []string
	if r.Warn != nil {
		p = append(p, fmt.Sprintf("警告 %s %s%s", op, num(*r.Warn), r.Unit))
	}
	if r.Bad != nil {
		p = append(p, fmt.Sprintf("异常 %s %s%s", op, num(*r.Bad), r.Unit))
	}
	return strings.Join(p, "，")
}

func num(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// item 生成结果骨架。
func (r *Rule) item(val, st, adv string) model.Item {
	th := r.ThText()
	if th == "" {
		th = "—"
	}
	if adv == "" && st != "ok" && st != "skip" {
		adv = r.Advice
	}
	if st == "ok" {
		adv = "—"
	}
	return model.Item{ID: r.ID, Cat: r.Cat, Name: r.Name, Src: r.Src, Value: val, Threshold: th, Status: st, Advice: adv, Doc: r.Doc}
}

func (r *Rule) skip(why string) model.Item { return r.item("—", "skip", why) }
func (r *Rule) fail(err error) model.Item {
	return r.item("—", "error", "采集失败："+err.Error())
}
func (r *Rule) value(v float64, display string) model.Item {
	return r.item(display, r.judge(v), "")
}

// apply 用配置覆盖规则阈值（返回副本）。
func (r Rule) apply(c model.RuleConfig) Rule {
	if c.Warn != nil && r.Warn != nil {
		r.Warn = c.Warn
	}
	if c.Bad != nil && r.Bad != nil {
		r.Bad = c.Bad
	}
	return r
}

// worst 返回明细中最差的状态。
func worst(ds []model.Detail) string {
	rank := map[string]int{"ok": 0, "info": 1, "warn": 2, "bad": 3, "error": 2}
	st := "ok"
	for _, d := range ds {
		if rank[d.Status] > rank[st] {
			st = d.Status
		}
	}
	if st == "error" {
		st = "warn"
	}
	return st
}

// RuleView 是规则列表 API 的输出。
type RuleView struct {
	ID, Cat, Name, Src, Doc, Unit string
	Warn, Bad                     *float64
	DefWarn, DefBad               *float64
	Lower, Enabled                bool
	Threshold                     string
}
