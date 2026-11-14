// @author: kevinkelly
// @license: Apache Licence
// @file: notify.go
// @time: 2026/10/28

package inspect

import (
	"context"
	"fmt"
	"log"
	"time"

	"starrocks-ops/internal/mail"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/notify"
	"starrocks-ops/internal/store"
)

// maxLines 每个分组最多列出的巡检项，其余提示到平台查看。
const maxLines = 5

// ShouldNotify 判断这次巡检结果是否需要通知。
func ShouldNotify(n *model.Notify, rep *model.Report) bool {
	if n == nil {
		return false
	}
	if n.When != "problem" {
		return true
	}
	return rep.Counts["bad"] > 0 || rep.Score < n.MinScore
}

// ReportMessage 把巡检报告组装为通知消息。
func ReportMessage(c model.Cluster, rep *model.Report) notify.Message {
	level := "ok"
	if rep.Counts["warn"] > 0 {
		level = "warn"
	}
	if rep.Counts["bad"] > 0 {
		level = "bad"
	}
	m := notify.Message{
		Title: fmt.Sprintf("StarRocks 巡检 · %s 健康分 %d", c.Name, rep.Score),
		Level: level,
		Summary: []string{
			fmt.Sprintf("报告 %s（%s · %s）", rep.ID, rep.By, rep.At.Local().Format("2006-01-02 15:04")),
			fmt.Sprintf("异常 %d · 警告 %d · 建议 %d · 正常 %d · 跳过 %d · 采集失败 %d",
				rep.Counts["bad"], rep.Counts["warn"], rep.Counts["info"], rep.Counts["ok"], rep.Counts["skip"], rep.Counts["error"]),
		},
	}
	var problems []map[string]string
	for _, st := range []struct{ code, head string }{{"bad", "异常"}, {"warn", "警告"}, {"error", "采集失败"}} {
		var lines []string
		n := 0
		for _, it := range rep.Items {
			if it.Status != st.code || it.Ignored {
				continue
			}
			n++
			problems = append(problems, map[string]string{"id": it.ID, "cat": it.Cat, "name": it.Name, "status": it.Status, "value": it.Value, "threshold": it.Threshold})
			if n <= maxLines {
				l := it.Name + "：" + it.Value
				if it.Threshold != "" && st.code != "error" {
					l += "（阈值 " + it.Threshold + "）"
				}
				lines = append(lines, l)
			}
		}
		if n > maxLines {
			lines = append(lines, fmt.Sprintf("……另有 %d 项，请登录平台查看", n-maxLines))
		}
		m.Sections = append(m.Sections, notify.Section{Head: fmt.Sprintf("%s（%d）", st.head, n), Lines: lines})
	}
	m.Data = map[string]any{"event": "inspection", "cluster": c.ID, "clusterName": c.Name, "reportId": rep.ID, "at": rep.At, "by": rep.By, "score": rep.Score, "counts": rep.Counts, "problems": problems}
	return m
}

// FailureMessage 是定时巡检本身失败时的通知。
func FailureMessage(c model.Cluster, err error) notify.Message {
	return notify.Message{
		Title:   fmt.Sprintf("StarRocks 巡检 · %s 定时巡检失败", c.Name),
		Level:   "bad",
		Summary: []string{time.Now().Format("2006-01-02 15:04"), "错误：" + err.Error()},
		Data:    map[string]any{"event": "inspection_failed", "cluster": c.ID, "clusterName": c.Name, "error": err.Error()},
	}
}

// WithLink 在消息末尾加上平台的“查看详情”链接（平台设置中配置了访问地址时）。
func (r *Runner) WithLink(m notify.Message) notify.Message {
	if r.BaseURL != nil {
		if u := r.BaseURL(); u != "" {
			m.Summary = append(append([]string(nil), m.Summary...), "查看详情："+u+"/#inspect")
		}
	}
	return m
}

// SendNotify 解密地址与密钥后发送。
func (r *Runner) SendNotify(ctx context.Context, n *model.Notify, m notify.Message) error {
	u, err := r.Box.Decrypt(n.URLEnc)
	if err != nil {
		return fmt.Errorf("通知地址解密失败：%v", err)
	}
	if n.Type == "邮件" { // URLEnc 保存的是收件人列表，经平台 SMTP 发送
		if r.Mailer == nil {
			return fmt.Errorf("邮件服务未启用")
		}
		to, err := mail.SplitAddrs(u)
		if err != nil {
			return err
		}
		return r.Mailer(ctx, to, m.Title, notify.PlainText(m))
	}
	sec := ""
	if n.SecretEnc != "" {
		if sec, err = r.Box.Decrypt(n.SecretEnc); err != nil {
			return fmt.Errorf("签名密钥解密失败：%v", err)
		}
	}
	cl := r.Notifier
	if cl == nil {
		cl = notify.Default
	}
	return cl.Send(ctx, n.Type, u, sec, m)
}

// notifyScheduled 发送定时巡检结果并记录发送状态。
func (r *Runner) notifyScheduled(ctx context.Context, c model.Cluster, n *model.Notify, m notify.Message) {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	m = r.WithLink(m)
	err := r.SendNotify(sctx, n, m)
	res := "成功"
	if err != nil {
		res = "失败：" + err.Error()
		log.Printf("定时巡检通知 %s 失败: %v", c.ID, err)
	}
	r.Store.Update(func(st *store.State) error {
		x := st.Schedules[c.ID]
		x.LastNotify, x.LastNotifyResult = time.Now(), res
		st.Schedules[c.ID] = x
		return nil
	})
	r.Store.Audit(store.AuditEntry{User: "定时任务", Cluster: c.ID, Code: "inspect.schedule", Act: "发送巡检通知（" + n.Type + "）", Result: res})
}
