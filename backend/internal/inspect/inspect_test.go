// @author: kevinkelly
// @license: Apache Licence
// @file: inspect_test.go
// @time: 2026/10/28

package inspect

import (
	"testing"
	"time"

	"starrocks-ops/internal/model"
)

func TestJudge(t *testing.T) {
	r := Rule{Warn: th(80), Bad: th(85)}
	for v, want := range map[float64]string{79.9: "ok", 80: "warn", 84.9: "warn", 85: "bad"} {
		if got := r.judge(v); got != want {
			t.Errorf("judge(%v) = %s，期望 %s", v, got, want)
		}
	}
	low := Rule{Warn: th(262144), Lower: true}
	if low.judge(65530) != "warn" || low.judge(262144) != "ok" {
		t.Fatal("越小越差的规则判定错误")
	}
	if low.ThText() != "警告 < 262144" {
		t.Fatalf("阈值说明：%s", low.ThText())
	}
}

func TestRescore(t *testing.T) {
	r := model.Report{Items: []model.Item{{Status: "bad"}, {Status: "bad", Ignored: true}, {Status: "warn"}, {Status: "info"}, {Status: "skip"}, {Status: "ok"}}}
	r.Rescore()
	if r.Score != 95 || r.Counts["bad"] != 1 || r.Counts["ignored"] != 1 {
		t.Fatalf("score=%d counts=%v", r.Score, r.Counts)
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 8, 10, 0, 30, 0, time.Local)
	daily := model.Schedule{Enabled: true, Every: "daily", At: "10:00", LastRun: now.Add(-24 * time.Hour)}
	if !due(daily, now) {
		t.Fatal("每天 10:00 应到期")
	}
	daily.LastRun = now
	if due(daily, now) {
		t.Fatal("今天已执行过不应重复")
	}
	if due(model.Schedule{Enabled: true, Every: "daily", At: "11:00"}, now) {
		t.Fatal("未到时间不应执行")
	}
	if !due(model.Schedule{Enabled: true, Every: "hours", Hours: 6, LastRun: now.Add(-7 * time.Hour)}, now) {
		t.Fatal("间隔已到应执行")
	}
	if due(model.Schedule{Enabled: false, Every: "hours", Hours: 1}, now) {
		t.Fatal("停用不应执行")
	}
}

func TestRuleIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Rules() {
		if seen[r.ID] {
			t.Fatalf("规则 ID 重复：%s", r.ID)
		}
		seen[r.ID] = true
		if !contains(Cats, r.Cat) {
			t.Fatalf("%s 的分类 %s 不在 Cats 中", r.ID, r.Cat)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
