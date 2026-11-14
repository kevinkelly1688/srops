// @author: kevinkelly
// @license: Apache Licence
// @file: runner.go
// @time: 2026/10/28

package inspect

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"starrocks-ops/internal/collect"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/notify"
	"starrocks-ops/internal/secure"
	"starrocks-ops/internal/sshx"
	"starrocks-ops/internal/store"
)

var keepReports atomic.Int64 // 纳秒

func init() { keepReports.Store(int64(90 * 24 * time.Hour)) }

// KeepFor 返回巡检报告保留期（平台设置 → 基础）。
func KeepFor() time.Duration { return time.Duration(keepReports.Load()) }

// SetKeepReports 设置巡检报告保留天数。
func SetKeepReports(days int) {
	if days >= 7 {
		keepReports.Store(int64(time.Duration(days) * 24 * time.Hour))
	}
}

var ErrBusy = errors.New("该集群正在巡检中，请稍后")

type Runner struct {
	Store *store.Store
	Mgr   *collect.Manager
	Keys  *sshx.Keys
	Box   *secure.Box
	// Notifier 发送定时巡检通知；nil 时使用 notify.Default
	Notifier *notify.Client
	// Mailer 通过平台 SMTP 发送邮件（通知方式「邮件」）；BaseURL 返回平台访问地址（用于消息中的“查看详情”链接）
	Mailer  func(ctx context.Context, to []string, subject, body string) error
	BaseURL func() string

	mu      sync.Mutex
	running map[string]bool
}

// effective 返回应用了配置覆盖后的规则及启用状态。
func (r *Runner) effective() ([]Rule, map[string]bool) {
	var cfg map[string]model.RuleConfig
	r.Store.View(func(st *store.State) {
		cfg = map[string]model.RuleConfig{}
		for k, v := range st.Rules {
			cfg[k] = v
		}
	})
	var out []Rule
	en := map[string]bool{}
	for _, rl := range Rules() {
		c := cfg[rl.ID]
		out = append(out, rl.apply(c))
		en[rl.ID] = c.Enabled == nil || *c.Enabled
	}
	return out, en
}

// RuleViews 返回规则列表（含默认值与当前值）。
func (r *Runner) RuleViews() []RuleView {
	rules, en := r.effective()
	defs := Rules()
	var out []RuleView
	for i, rl := range rules {
		out = append(out, RuleView{ID: rl.ID, Cat: rl.Cat, Name: rl.Name, Src: rl.Src, Doc: rl.Doc, Unit: strings.TrimSpace(rl.Unit),
			Warn: rl.Warn, Bad: rl.Bad, DefWarn: defs[i].Warn, DefBad: defs[i].Bad, Lower: rl.Lower, Enabled: en[rl.ID], Threshold: rl.ThText()})
	}
	return out
}

// Run 执行一次巡检并保存报告。cats 为空表示全部分类。
func (r *Runner) Run(ctx context.Context, c model.Cluster, by string, cats []string) (*model.Report, error) {
	r.mu.Lock()
	if r.running == nil {
		r.running = map[string]bool{}
	}
	if r.running[c.ID] {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	r.running[c.ID] = true
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.running, c.ID); r.mu.Unlock() }()

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	start := time.Now()
	topo, mt, err := r.Mgr.Topology(ctx, c)
	if err != nil {
		return nil, err
	}
	cl, err := r.Mgr.Client(ctx, c)
	if err != nil {
		return nil, err
	}
	env := &Env{Ctx: ctx, Cluster: c, CL: cl, Topo: topo, Metrics: mt, Keys: r.Keys}
	if c.SSH != nil && c.SSH.Auth == "password" {
		env.SSHPass, _ = r.Box.Decrypt(c.SSH.PasswordEnc)
	}

	rules, en := r.effective()
	items := make([]model.Item, len(rules))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for i := range rules {
		rl := &rules[i]
		if !en[rl.ID] || (len(cats) > 0 && !in(rl.Cat, cats)) {
			continue
		}
		wg.Add(1)
		go func(i int, rl *Rule) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			defer func() {
				if p := recover(); p != nil {
					log.Printf("inspect rule %s panic: %v", rl.ID, p)
					items[i] = rl.fail(fmt.Errorf("规则执行异常：%v", p))
				}
			}()
			items[i] = rl.Check(env, rl)
		}(i, rl)
	}
	wg.Wait()

	var ignored map[string]bool
	r.Store.View(func(st *store.State) {
		ignored = map[string]bool{}
		for _, id := range st.Ignores[c.ID] {
			ignored[id] = true
		}
	})
	rep := &model.Report{}
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		if ignored[it.ID] && it.Status != "ok" {
			it.Ignored = true
		}
		rep.Items = append(rep.Items, it)
	}
	rep.ClusterID, rep.By, rep.At = c.ID, by, start
	rep.DurationMs = time.Since(start).Milliseconds()
	rep.ID = r.newID(c.ID, start)
	rep.Rescore()
	if err := r.Store.SaveReport(rep); err != nil {
		return nil, err
	}
	return rep, nil
}

func (r *Runner) newID(cluster string, t time.Time) string {
	for {
		id := "IR-" + t.Format("20060102150405")
		if _, err := r.Store.Report(cluster, id); err != nil {
			return id
		}
		t = t.Add(time.Second)
	}
}

// SetIgnore 设置忽略项，并重新计算该集群最新报告的健康分。
func (r *Runner) SetIgnore(cluster, rule string, ignore bool) (*model.Report, error) {
	err := r.Store.Update(func(st *store.State) error {
		cur := st.Ignores[cluster]
		var out []string
		for _, x := range cur {
			if x != rule {
				out = append(out, x)
			}
		}
		if ignore {
			out = append(out, rule)
		}
		st.Ignores[cluster] = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	list := r.Store.Reports(cluster, KeepFor())
	if len(list) == 0 {
		return nil, nil
	}
	rep, err := r.Store.Report(cluster, list[0].ID)
	if err != nil {
		return nil, err
	}
	for i := range rep.Items {
		if rep.Items[i].ID == rule {
			rep.Items[i].Ignored = ignore && rep.Items[i].Status != "ok"
		}
	}
	rep.Rescore()
	return rep, r.Store.SaveReport(rep)
}

// ---------- 定时巡检 ----------

// due 判断定时任务是否到期。
func due(s model.Schedule, now time.Time) bool {
	if !s.Enabled {
		return false
	}
	if s.Every == "hours" {
		h := s.Hours
		if h < 1 {
			h = 1
		}
		return now.Sub(s.LastRun) >= time.Duration(h)*time.Hour
	}
	p := strings.SplitN(s.At, ":", 2)
	if len(p) != 2 {
		return false
	}
	hh, _ := strconv.Atoi(p[0])
	mm, _ := strconv.Atoi(p[1])
	at := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	return !now.Before(at) && s.LastRun.Before(at)
}

// StartScheduler 每 30 秒检查一次定时巡检。
func (r *Runner) StartScheduler(ctx context.Context) {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			now := time.Now()
			var todo []string
			var scheds map[string]model.Schedule
			r.Store.View(func(st *store.State) {
				scheds = map[string]model.Schedule{}
				for k, v := range st.Schedules {
					scheds[k] = v
					if due(v, now) {
						todo = append(todo, k)
					}
				}
			})
			for _, id := range todo {
				c, err := r.Store.Cluster(id)
				if err != nil {
					continue
				}
				s := scheds[id]
				r.Store.Update(func(st *store.State) error { x := st.Schedules[id]; x.LastRun = now; st.Schedules[id] = x; return nil })
				go r.RunScheduled(ctx, c, s)
			}
		}
	}()
}

// RunScheduled 执行一次定时巡检，并按配置发送结果通知。
func (r *Runner) RunScheduled(ctx context.Context, c model.Cluster, s model.Schedule) {
	rep, err := r.Run(ctx, c, "定时任务", s.Cats)
	if err != nil {
		log.Printf("定时巡检 %s 失败: %v", c.ID, err)
		r.Store.Audit(store.AuditEntry{User: "定时任务", Cluster: c.ID, Code: "inspect.run", Act: "定时巡检", Result: "失败：" + err.Error()})
		if s.Notify != nil && !errors.Is(err, ErrBusy) {
			r.notifyScheduled(ctx, c, s.Notify, FailureMessage(c, err))
		}
		return
	}
	r.Store.Audit(store.AuditEntry{User: "定时任务", Cluster: c.ID, Code: "inspect.run", Act: fmt.Sprintf("定时巡检 %s 健康分 %d", rep.ID, rep.Score), Result: "成功"})
	if ShouldNotify(s.Notify, rep) {
		r.notifyScheduled(ctx, c, s.Notify, ReportMessage(c, rep))
	}
}
