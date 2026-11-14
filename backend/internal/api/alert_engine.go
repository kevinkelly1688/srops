// @author: kevinkelly
// @license: Apache Licence
// @file: alert_engine.go
// @time: 2026/10/28

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"starrocks-ops/internal/mail"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/notify"
	"starrocks-ops/internal/sms"
	"starrocks-ops/internal/store"
)

// alertRule 是内置告警规则：平台每分钟读取集群的总览数据评估一次，不依赖 Prometheus。
type alertRule struct {
	ID, Name, Scope, Unit, Desc string
	Warn, Crit                  float64
	HasWarn                     bool
	DurMin                      int
	Op                          string // 比较方式：>=（默认）、>、<=、<
}

var alertRules = []alertRule{
	{ID: "cluster_unreachable", Name: "集群连接失败", Scope: "集群", Desc: "平台连续 2 次无法连接集群 FE（采集失败）。", Crit: 1, DurMin: 0},
	{ID: "node_down", Name: "节点失联", Scope: "节点", Desc: "FE / BE / CN 的心跳丢失（Alive = false）。", Crit: 1, DurMin: 1},
	{ID: "be_disk_usage", Name: "BE 磁盘使用率", Scope: "节点", Unit: "%", Desc: "BE 单盘最高使用率；达到 storage_flood_stage_usage_percent 后拒绝导入。", Warn: 80, Crit: 85, HasWarn: true, DurMin: 5},
	{ID: "compaction_score", Name: "Compaction Score", Scope: "节点", Desc: "BE 上 Tablet 的最高 Compaction Score；过高说明导入太频繁或 Compaction 跟不上，可能报 too many versions。", Warn: 50, Crit: 100, HasWarn: true, DurMin: 10},
	{ID: "fe_jvm_heap", Name: "FE JVM 堆使用率", Scope: "节点", Unit: "%", Desc: "FE JVM 堆使用率偏高有 Full GC 风险。", Warn: 80, Crit: 90, HasWarn: true, DurMin: 5},
	{ID: "query_p99", Name: "查询 P99 延迟", Scope: "集群", Unit: "ms", Desc: "各 FE 查询延迟 P99 的最大值。", Warn: 3000, Crit: 10000, HasWarn: true, DurMin: 5},
	{ID: "routine_load_paused", Name: "Routine Load 暂停", Scope: "集群", Unit: "个", Desc: "处于 PAUSED 状态的 Routine Load 作业数。", Warn: 1, Crit: 3, HasWarn: true, DurMin: 1},
	{ID: "mv_refresh_failed", Name: "物化视图刷新失败", Scope: "集群", Unit: "个", Desc: "最近一次刷新失败的异步物化视图数。", Warn: 1, Crit: 3, HasWarn: true, DurMin: 0},
	{ID: "unhealthy_tablets", Name: "不健康 Tablet", Scope: "集群", Unit: "个", Desc: "SHOW PROC '/statistic' 中的 UnhealthyTabletNum 合计。", Warn: 1, Crit: 100, HasWarn: true, DurMin: 10},
}

func ruleByID(id string) (alertRule, bool) {
	for _, r := range alertRules {
		if r.ID == id {
			return r, true
		}
	}
	return alertRule{}, false
}

// effRule 是叠加了用户配置后的规则参数。
type effRule struct {
	alertRule
	Enabled  bool
	Channels []string
}

func effective(r alertRule, cfg map[string]model.AlertRuleCfg) effRule {
	e := effRule{alertRule: r, Enabled: true}
	c, ok := cfg[r.ID]
	if !ok {
		return e
	}
	if c.Enabled != nil {
		e.Enabled = *c.Enabled
	}
	if c.Warn != nil {
		e.Warn = *c.Warn
	}
	if c.Crit != nil {
		e.Crit = *c.Crit
	}
	if c.DurMin != nil {
		e.DurMin = *c.DurMin
	}
	e.Channels = c.Channels
	return e
}

// level 返回该取值对应的告警级别；空串表示正常。
func (e effRule) level(v float64) string {
	cmp := func(th float64) bool {
		switch e.Op {
		case ">":
			return v > th
		case "<=":
			return v <= th
		case "<":
			return v < th
		}
		return v >= th
	}
	switch {
	case cmp(e.Crit):
		return "critical"
	case e.HasWarn && cmp(e.Warn):
		return "warning"
	}
	return ""
}

// customAlertRule 把自定义规则转为引擎使用的规则。
func customAlertRule(cr model.CustomRule) alertRule {
	r := alertRule{ID: cr.ID, Name: cr.Name, Scope: "PromQL", Unit: cr.Unit, Desc: cr.Desc, Crit: cr.Crit, DurMin: cr.DurMin, Op: cr.Op}
	if cr.Warn != nil {
		r.Warn, r.HasWarn = *cr.Warn, true
	}
	return r
}

func isCustomRule(id string) bool { return strings.HasPrefix(id, "custom_") }

// lookupRule 按 ID 查找内置规则（叠加用户配置）或自定义规则。
func lookupRule(id string, cfg map[string]model.AlertRuleCfg, custom []model.CustomRule) (effRule, bool) {
	if r, ok := ruleByID(id); ok {
		return effective(r, cfg), true
	}
	for _, cr := range custom {
		if cr.ID == id {
			return effRule{alertRule: customAlertRule(cr), Enabled: cr.Enabled, Channels: cr.Channels}, true
		}
	}
	return effRule{}, false
}

// ruleLabel 返回规则的显示名称（自定义规则也能查到）。
func (s *Server) ruleLabel(id string) string {
	if r, ok := ruleByID(id); ok {
		return r.Name
	}
	name := id
	s.Store.View(func(st *store.State) {
		for _, cr := range st.CustomRules {
			if cr.ID == id {
				name = cr.Name
			}
		}
	})
	return name
}

// sample 是一次评估得到的取值；Unknown 表示这次没有采到（不据此恢复告警）。
type sample struct {
	Rule, Inst, Desc, Val string
	V                     float64
	Unknown               bool
}

func fmtNum(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}

func nodeLabel(n model.Node) string {
	p := n.HTTP
	if n.Type == "fe" {
		p = n.Port
	}
	return fmt.Sprintf("%s %s:%d", strings.ToUpper(n.Type), n.Host, p)
}

// samplesOf 从总览数据中取出各规则的当前取值。
func samplesOf(ov *model.Overview) []sample {
	var out []sample
	add := func(rule, inst string, v *float64, desc string) {
		if v == nil {
			out = append(out, sample{Rule: rule, Inst: inst, Unknown: true})
			return
		}
		out = append(out, sample{Rule: rule, Inst: inst, V: *v, Val: fmtNum(*v), Desc: desc})
	}
	for _, n := range ov.Nodes {
		lbl := nodeLabel(n)
		down := 0.0
		if !n.Alive {
			down = 1
		}
		d := "节点心跳丢失"
		if n.ErrMsg != "" {
			d += "：" + n.ErrMsg
		}
		out = append(out, sample{Rule: "node_down", Inst: lbl, V: down, Val: fmtNum(down), Desc: d})
		if !n.Alive {
			continue // 失联节点的其他指标没有意义
		}
		switch n.Type {
		case "be":
			add("be_disk_usage", lbl, n.Disk, "单盘最高使用率 %s%%")
			add("compaction_score", lbl, n.Compaction, "Compaction Score %s")
		case "fe":
			add("fe_jvm_heap", lbl, n.JVM, "JVM 堆使用率 %s%%")
		}
	}
	for i := range out {
		if strings.Contains(out[i].Desc, "%s") {
			out[i].Desc = fmt.Sprintf(out[i].Desc, out[i].Val)
		}
	}
	k := ov.KPI
	f := func(p *int) *float64 {
		if p == nil {
			return nil
		}
		v := float64(*p)
		return &v
	}
	cl := "集群"
	add("query_p99", cl, k.P99, "各 FE 查询延迟 P99 最大值 %s ms")
	add("routine_load_paused", cl, f(k.RLPaused), "%s 个 Routine Load 作业处于 PAUSED")
	add("mv_refresh_failed", cl, f(k.MVFailed), "%s 个物化视图最近一次刷新失败")
	if k.Unhealthy != nil {
		v := float64(*k.Unhealthy)
		add("unhealthy_tablets", cl, &v, "%s 个 Tablet 不健康")
	} else {
		add("unhealthy_tablets", cl, nil, "")
	}
	for i := range out {
		if strings.Contains(out[i].Desc, "%s") && !out[i].Unknown {
			out[i].Desc = fmt.Sprintf(out[i].Desc, out[i].Val)
		}
	}
	return out
}

func newAlertID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "AL-" + time.Now().Format("0102-150405") + "-" + hex.EncodeToString(b)
}

func alertKey(cluster, rule, inst string) string { return cluster + "|" + rule + "|" + inst }

// evalCluster 评估一个集群并更新告警；采集失败时只记录失败次数。
func (s *Server) evalCluster(ctx context.Context, c model.Cluster, now time.Time) {
	defer s.evalCustom(ctx, c, now) // 自定义 PromQL 规则只依赖 Prometheus，不受 FE 连接状态影响
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	ov, err := s.Mgr.Overview(cctx, c)
	cancel()
	if err == nil && len(ov.Nodes) == 0 { // FE 都读不到时拓扑为空而不是报错：按采集失败处理，避免把所有告警误判为已恢复
		err = fmt.Errorf("没有读取到任何节点：%s", strings.Join(ov.Warnings, "；"))
	}
	var samples []sample
	s.alertMu.Lock()
	if err != nil {
		s.alertFail[c.ID]++
		n := s.alertFail[c.ID]
		s.alertMu.Unlock()
		if n < 2 {
			return
		}
		samples = []sample{{Rule: "cluster_unreachable", Inst: c.Name, V: 1, Val: "1", Desc: "无法连接集群：" + err.Error()}}
	} else {
		s.alertFail[c.ID] = 0
		s.alertMu.Unlock()
		samples = append(samples, sample{Rule: "cluster_unreachable", Inst: c.Name, V: 0, Val: "0"})
		samples = append(samples, samplesOf(ov)...)
	}
	s.applySamples(c, samples, err == nil, now)
}

// applySamples 把评估结果写入告警状态。complete 为 true 表示本次采集完整，消失的对象会被视为已恢复。
func (s *Server) applySamples(c model.Cluster, samples []sample, complete bool, now time.Time) {
	s.applyCore(c, samples, func(rule string) bool { return complete && !isCustomRule(rule) }, now)
}

// applyCore 是 applySamples 的实现：absent(rule) 为 true 时，该规则下本次没有出现的对象视为已恢复。
func (s *Server) applyCore(c model.Cluster, samples []sample, absent func(rule string) bool, now time.Time) {
	s.Store.Update(func(st *store.State) error {
		seen := map[string]bool{}
		find := func(key string) int {
			for i := range st.Alerts {
				a := &st.Alerts[i]
				if a.State != "resolved" && alertKey(a.Cluster, a.Rule, a.Inst) == key {
					return i
				}
			}
			return -1
		}
		resolve := func(i int) {
			st.Alerts[i].State, st.Alerts[i].ResolvedAt = "resolved", now
		}
		for _, sm := range samples {
			e, ok := lookupRule(sm.Rule, st.AlertCfg, st.CustomRules)
			if !ok {
				continue
			}
			key := alertKey(c.ID, sm.Rule, sm.Inst)
			seen[key] = true
			i := find(key)
			if !e.Enabled {
				if i >= 0 {
					resolve(i)
				}
				continue
			}
			if sm.Unknown {
				continue
			}
			lv := e.level(sm.V)
			s.alertMu.Lock()
			if lv == "" {
				delete(s.alertPend, key)
				s.alertMu.Unlock()
				if i >= 0 {
					resolve(i)
				}
				continue
			}
			since, pending := s.alertPend[key]
			if !pending {
				s.alertPend[key], since = now, now
			}
			s.alertMu.Unlock()
			if now.Sub(since) < time.Duration(e.DurMin)*time.Minute {
				continue
			}
			if i >= 0 {
				a := &st.Alerts[i]
				a.Level, a.Value, a.Desc, a.LastAt = lv, sm.Val, sm.Desc, now
				continue
			}
			st.Alerts = append(st.Alerts, model.Alert{ID: newAlertID(), Cluster: c.ID, Rule: sm.Rule, Name: e.Name, Inst: sm.Inst, Level: lv, Value: sm.Val, Desc: sm.Desc,
				State: "firing", FirstAt: since, LastAt: now})
			// 告警的“触发时间”取开始异常的时间；通知的合并等待从告警创建时算起
			st.Alerts[len(st.Alerts)-1].FirstAt = now
		}
		for i := range st.Alerts { // 对象已不存在（如节点被移除）：视为恢复
			a := &st.Alerts[i]
			if a.Cluster == c.ID && a.State != "resolved" && absent(a.Rule) && !seen[alertKey(a.Cluster, a.Rule, a.Inst)] {
				resolve(i)
			}
		}
		// 清理：已恢复超过 7 天的告警；总数超过 3000 时丢弃最早已恢复的
		keep := st.Alerts[:0]
		for _, a := range st.Alerts {
			if a.State == "resolved" && now.Sub(a.ResolvedAt) > 7*24*time.Hour {
				continue
			}
			keep = append(keep, a)
		}
		st.Alerts = keep
		if len(st.Alerts) > 3000 {
			sort.SliceStable(st.Alerts, func(i, j int) bool {
				ai, aj := st.Alerts[i], st.Alerts[j]
				if (ai.State == "resolved") != (aj.State == "resolved") {
					return ai.State != "resolved"
				}
				return ai.FirstAt.After(aj.FirstAt)
			})
			st.Alerts = st.Alerts[:3000]
		}
		return nil
	})
	// 评估不再产生的告警键清理其 pending 记录
	s.alertMu.Lock()
	for k := range s.alertPend {
		parts := strings.SplitN(k, "|", 3)
		if strings.HasPrefix(k, c.ID+"|") && len(parts) == 3 && absent(parts[1]) {
			found := false
			for _, sm := range samples {
				if alertKey(c.ID, sm.Rule, sm.Inst) == k {
					found = true
					break
				}
			}
			if !found {
				delete(s.alertPend, k)
			}
		}
	}
	s.alertMu.Unlock()
}

// EvalAlerts 评估全部集群并发送通知；now 便于测试传入。
func (s *Server) EvalAlerts(ctx context.Context, now time.Time) {
	for _, c := range s.Store.Clusters() {
		s.evalCluster(ctx, c, now)
	}
	s.dispatchAlerts(ctx, now)
}

func (s *Server) alertLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.EvalAlerts(ctx, now)
		}
	}
}

// ---------- 通知 ----------

var levelText = map[string]string{"critical": "严重", "warning": "警告", "info": "提示"}

func silenced(list []model.Silence, a model.Alert, now time.Time) bool {
	for _, sl := range list {
		if now.Before(sl.From) || !now.Before(sl.Until) {
			continue
		}
		if (sl.Cluster == "" || sl.Cluster == a.Cluster) && (sl.Rule == "" || sl.Rule == a.Rule) && (sl.Inst == "" || strings.Contains(a.Inst, sl.Inst)) {
			return true
		}
	}
	return false
}

// inQuiet 判断是否处于免打扰时段（支持跨零点，如 23:00–08:00）。
func inQuiet(n model.NotifySettings, now time.Time) bool {
	if !n.Quiet {
		return false
	}
	var fh, fm, th, tm int
	if _, err := fmt.Sscanf(n.QuietFrom, "%d:%d", &fh, &fm); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(n.QuietTo, "%d:%d", &th, &tm); err != nil {
		return false
	}
	cur, from, to := now.Hour()*60+now.Minute(), fh*60+fm, th*60+tm
	if from == to {
		return false
	}
	if from < to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to
}

type alertGroup struct {
	cluster, rule string
	fire, recover []int // alerts 下标
}

// dispatchAlerts 按平台设置里的发送策略（合并、重复提醒、恢复通知、免打扰）和静默规则发送告警通知。
func (s *Server) dispatchAlerts(ctx context.Context, now time.Time) {
	var alerts []model.Alert
	var chans []model.Channel
	var sil []model.Silence
	var cfgs map[string]model.AlertRuleCfg
	var custom []model.CustomRule
	s.Store.View(func(st *store.State) {
		custom = append(custom, st.CustomRules...)
		alerts = append(alerts, st.Alerts...)
		chans = append(chans, st.Channels...)
		sil = append(sil, st.Silences...)
		cfgs = map[string]model.AlertRuleCfg{}
		for k, v := range st.AlertCfg {
			cfgs[k] = v
		}
	})
	ns := s.cfg().Notify
	quiet := inQuiet(ns, now)
	groups := map[string]*alertGroup{}
	var order []string
	grp := func(a model.Alert) *alertGroup {
		k := a.Cluster + "|" + a.Rule
		g := groups[k]
		if g == nil {
			g = &alertGroup{cluster: a.Cluster, rule: a.Rule}
			groups[k] = g
			order = append(order, k)
		}
		return g
	}
	retryOK := func(a model.Alert) bool {
		return a.NotifyTry.IsZero() || now.Sub(a.NotifyTry) >= 5*time.Minute || a.NotifyErr == ""
	}
	var finished []string // 不需要再发恢复通知的已恢复告警
	for i, a := range alerts {
		if silenced(sil, a, now) {
			continue
		}
		switch a.State {
		case "resolved":
			if a.RecoverSent {
				continue
			}
			if !ns.Recover || a.Notified.IsZero() {
				finished = append(finished, a.ID)
				continue
			}
			if (quiet && a.Level != "critical") || !retryOK(a) {
				continue
			}
			grp(a).recover = append(grp(a).recover, i)
		case "firing":
			if quiet && a.Level != "critical" {
				continue
			}
			if !retryOK(a) {
				continue
			}
			if a.Notified.IsZero() {
				if now.Sub(a.FirstAt) >= time.Duration(ns.MergeMin)*time.Minute {
					grp(a).fire = append(grp(a).fire, i)
				}
			} else if ns.RepeatMin > 0 && now.Sub(a.Notified) >= time.Duration(ns.RepeatMin)*time.Minute {
				grp(a).fire = append(grp(a).fire, i)
			}
		}
	}
	type result struct {
		ids  []string
		kind string // fire / recover
		ok   bool
		err  string
		sent bool
	}
	var results []result
	chRes := map[string]string{}
	for _, k := range order {
		g := groups[k]
		cname := g.cluster
		if c, err := s.Store.Cluster(g.cluster); err == nil {
			cname = c.Name
		}
		for _, kind := range []string{"fire", "recover"} {
			idx := g.fire
			if kind == "recover" {
				idx = g.recover
			}
			if len(idx) == 0 {
				continue
			}
			var list []model.Alert
			var ids []string
			for _, i := range idx {
				list = append(list, alerts[i])
				ids = append(ids, alerts[i].ID)
			}
			e, found := lookupRule(g.rule, cfgs, custom)
			if !found {
				e = effective(alertRuleOr(g.rule), cfgs)
			}
			delivered, errs := 0, []string{}
			for _, ch := range chans {
				if !ch.Enabled || (len(e.Channels) > 0 && !contains(e.Channels, ch.ID)) {
					continue
				}
				var sub []model.Alert
				for _, a := range list {
					if contains(ch.Levels, a.Level) {
						sub = append(sub, a)
					}
				}
				if len(sub) == 0 {
					continue
				}
				m := s.alertMessage(cname, g.rule, sub, kind == "recover", now)
				sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := s.sendChannel(sctx, ch, m)
				cancel()
				if err != nil {
					errs = append(errs, ch.Name+"："+err.Error())
					chRes[ch.ID] = "失败：" + err.Error()
				} else {
					delivered++
					chRes[ch.ID] = "成功"
				}
			}
			r := result{ids: ids, kind: kind}
			switch {
			case delivered > 0:
				r.ok, r.sent = true, true
			case len(errs) > 0:
				r.err = strings.Join(errs, "；")
			default:
				r.ok = kind == "recover" // 没有可用渠道：恢复通知不再等待；首次告警保持待发送，等配置好渠道后补发
			}
			results = append(results, r)
			if len(errs) > 0 || delivered > 0 {
				res := "成功"
				if delivered == 0 {
					res = "失败：" + strings.Join(errs, "；")
				} else if len(errs) > 0 {
					res = "部分失败：" + strings.Join(errs, "；")
				}
				act := fmt.Sprintf("发送告警通知：%s · %s（%d 条，%s）", cname, s.ruleLabel(g.rule), len(list), map[string]string{"fire": "触发 / 提醒", "recover": "恢复"}[kind])
				s.Store.Audit(store.AuditEntry{User: "系统（告警中心）", Cluster: g.cluster, Code: "alert.channel", Act: act, Result: res})
			}
		}
	}
	if len(results) == 0 && len(finished) == 0 {
		return
	}
	s.Store.Update(func(st *store.State) error {
		for _, r := range results {
			for _, id := range r.ids {
				for i := range st.Alerts {
					a := &st.Alerts[i]
					if a.ID != id {
						continue
					}
					a.NotifyTry = now
					switch {
					case r.kind == "recover" && r.ok:
						a.RecoverSent, a.NotifyErr = true, ""
					case r.kind == "fire" && r.ok && r.sent:
						a.Notified, a.NotifyN, a.NotifyErr = now, a.NotifyN+1, ""
					case r.err != "":
						a.NotifyErr = r.err
					}
				}
			}
		}
		for _, id := range finished {
			for i := range st.Alerts {
				if st.Alerts[i].ID == id {
					st.Alerts[i].RecoverSent = true
				}
			}
		}
		for i := range st.Channels {
			if res, ok := chRes[st.Channels[i].ID]; ok {
				st.Channels[i].LastAt, st.Channels[i].LastResult = now, res
			}
		}
		return nil
	})
}

func alertRuleOr(id string) alertRule {
	r, _ := ruleByID(id)
	r.ID = id
	return r
}

// alertMessage 组装一条通知：同一集群、同一规则的多个对象合并在一条消息里。
func (s *Server) alertMessage(cluster, rule string, list []model.Alert, recovered bool, now time.Time) notify.Message {
	top := "info"
	for _, a := range list {
		if a.Level == "critical" || (a.Level == "warning" && top == "info") {
			top = a.Level
		}
	}
	tag := "【" + levelText[top] + "】"
	level := map[string]string{"critical": "bad", "warning": "warn", "info": "warn"}[top]
	if recovered {
		tag, level = "【已恢复】", "ok"
	}
	rn := s.ruleLabel(rule)
	title := fmt.Sprintf("%sStarRocks · %s · %s", tag, cluster, rn)
	if len(list) > 1 {
		title += fmt.Sprintf("（%d 项）", len(list))
	}
	m := notify.Message{Title: title, Level: level, Summary: []string{"集群：" + cluster, "时间：" + now.Local().Format("2006-01-02 15:04")}}
	m.SMS, m.Params = smsText(tag, cluster, rn, list, recovered), smsParams(tag, cluster, rn, list, recovered, now)
	var lines []string
	var data []map[string]any
	for i, a := range list {
		if i < 8 {
			l := a.Inst + "：" + a.Desc
			if recovered {
				l = a.Inst + "：已恢复（持续 " + a.ResolvedAt.Sub(a.FirstAt).Round(time.Minute).String() + "）"
			}
			lines = append(lines, l)
		}
		data = append(data, map[string]any{"id": a.ID, "inst": a.Inst, "level": a.Level, "value": a.Value, "desc": a.Desc, "firstAt": a.FirstAt})
	}
	if len(list) > 8 {
		lines = append(lines, fmt.Sprintf("……另有 %d 项，请登录平台查看", len(list)-8))
	}
	m.Sections = []notify.Section{{Head: fmt.Sprintf("%s（%d）", map[bool]string{false: "告警对象", true: "已恢复对象"}[recovered], len(list)), Lines: lines}}
	if u := s.cfg().Basic.URL; u != "" {
		m.Summary = append(m.Summary, "查看详情："+strings.TrimRight(u, "/")+"/#alert")
	}
	m.Data = map[string]any{"event": "alert", "status": map[bool]string{false: "firing", true: "resolved"}[recovered], "cluster": cluster, "rule": rule, "ruleName": rn, "alerts": data}
	return m
}

// sendChannel 解密渠道地址与密钥后发送；邮件渠道经平台 SMTP 发送。
func (s *Server) sendChannel(ctx context.Context, ch model.Channel, m notify.Message) error {
	u, err := s.Box.Decrypt(ch.URLEnc)
	if err != nil {
		return fmt.Errorf("渠道地址解密失败：%v", err)
	}
	if ch.Type == "邮件" {
		to, err := mail.SplitAddrs(u)
		if err != nil {
			return err
		}
		return s.SendMail(ctx, to, m.Title, notify.PlainText(m))
	}
	if ch.Type == "短信" {
		phones, err := sms.SplitPhones(u)
		if err != nil {
			return err
		}
		return s.SendSMS(ctx, phones, m)
	}
	sec := ""
	if ch.SecretEnc != "" {
		if sec, err = s.Box.Decrypt(ch.SecretEnc); err != nil {
			return fmt.Errorf("签名密钥解密失败：%v", err)
		}
	}
	return notify.Default.Send(ctx, ch.Type, u, sec, m)
}
