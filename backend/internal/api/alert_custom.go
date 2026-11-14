// @author: kevinkelly
// @license: Apache Licence
// @file: alert_custom.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"starrocks-ops/internal/collect"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/store"
)

// customRun 是自定义规则在某个集群上最近一次评估的结果（仅内存，用于页面展示）。
type customRun struct {
	At     time.Time
	Err    string
	Series int
}

const (
	maxCustomRules  = 50
	maxCustomSeries = 200 // 每条规则每个集群最多处理的序列数
)

// expandSel 把表达式里的 $sel 替换为集群的 Prometheus 选择器（如 job="StarRocks_prod"）。
// 写法：starrocks_be_disks_data_used_pct{$sel} 或 foo{$sel,type="x"}；集群没有配置选择器时 $sel 与它后面的逗号一起去掉。
func expandSel(expr, selector string) string {
	sel := strings.Trim(strings.TrimSpace(selector), "{}")
	if sel == "" {
		expr = strings.ReplaceAll(expr, "$sel,", "")
		return strings.ReplaceAll(expr, "$sel", "")
	}
	return strings.ReplaceAll(expr, "$sel", sel)
}

// seriesInst 用序列的标签生成告警对象名：只有 instance 时直接用 instance，否则列出全部标签。
func seriesInst(lbl map[string]string, fallback string) string {
	var keys []string
	for k := range lbl {
		if k != "__name__" && k != "job" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	switch {
	case len(keys) == 0:
		return fallback
	case len(keys) == 1 && keys[0] == "instance":
		return lbl["instance"]
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + lbl[k]
	}
	return strings.Join(parts, ", ")
}

var opText = map[string]string{">=": "≥", ">": ">", "<=": "≤", "<": "<"}

// evalCustom 评估适用于该集群的自定义 PromQL 规则。
// 查询失败的规则不会改变已有告警（不能把查询失败当成恢复）；停用 / 删除 / 不再适用的规则下的告警视为已恢复。
func (s *Server) evalCustom(ctx context.Context, c model.Cluster, now time.Time) {
	var all []model.CustomRule
	hasOpen := false
	s.Store.View(func(st *store.State) {
		all = append(all, st.CustomRules...)
		for _, a := range st.Alerts {
			if a.Cluster == c.ID && a.State != "resolved" && isCustomRule(a.Rule) {
				hasOpen = true
			}
		}
	})
	if len(all) == 0 && !hasOpen {
		return
	}
	active, okRules := map[string]bool{}, map[string]bool{}
	var samples []sample
	for _, cr := range all {
		if !cr.Enabled || (cr.Cluster != "" && cr.Cluster != c.ID) || strings.TrimSpace(c.Prom) == "" {
			continue
		}
		active[cr.ID] = true
		qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		series, err := collect.PromQuery(qctx, c.Prom, expandSel(cr.Expr, c.PromSelector))
		cancel()
		run := customRun{At: now, Series: len(series)}
		if err != nil {
			run.Err = err.Error()
		} else {
			okRules[cr.ID] = true
		}
		s.alertMu.Lock()
		s.customErr[cr.ID+"|"+c.ID] = run
		s.alertMu.Unlock()
		if err != nil {
			continue
		}
		seen := map[string]int{}
		for i, sr := range series {
			if i >= maxCustomSeries || math.IsNaN(sr.V) || math.IsInf(sr.V, 0) {
				continue
			}
			inst := seriesInst(sr.Labels, c.Name)
			if n := seen[inst]; n > 0 {
				seen[inst]++
				inst += fmt.Sprintf(" #%d", n+1)
			} else {
				seen[inst] = 1
			}
			val := fmtNum(sr.V)
			desc := fmt.Sprintf("当前值 %s%s（条件：%s %s%s", val, cr.Unit, opText[cr.Op], fmtNum(cr.Crit), cr.Unit)
			if cr.Warn != nil {
				desc += fmt.Sprintf("，警告 %s %s%s", opText[cr.Op], fmtNum(*cr.Warn), cr.Unit)
			}
			desc += "）"
			samples = append(samples, sample{Rule: cr.ID, Inst: inst, V: sr.V, Val: val + cr.Unit, Desc: desc})
		}
	}
	s.applyCore(c, samples, func(rule string) bool {
		return isCustomRule(rule) && (!active[rule] || okRules[rule])
	}, now)
}

// ---------- 接口 ----------

type customOut struct {
	model.CustomRule
	ClusterName string   `json:"clusterName"`
	Status      []string `json:"status"` // 各集群最近一次评估结果
}

func (s *Server) customView(list []model.CustomRule) []customOut {
	names := map[string]string{}
	for _, c := range s.Store.Clusters() {
		names[c.ID] = c.Name
	}
	s.alertMu.Lock()
	defer s.alertMu.Unlock()
	out := make([]customOut, 0, len(list))
	for _, cr := range list {
		if cr.Channels == nil {
			cr.Channels = []string{}
		}
		o := customOut{CustomRule: cr, ClusterName: names[cr.Cluster], Status: []string{}}
		for cid, nm := range names {
			if cr.Cluster != "" && cr.Cluster != cid {
				continue
			}
			if run, ok := s.customErr[cr.ID+"|"+cid]; ok {
				msg := nm + "：" + fmt.Sprintf("%d 条序列", run.Series)
				if run.Err != "" {
					msg = nm + "：查询失败 · " + run.Err
				}
				o.Status = append(o.Status, msg)
			}
		}
		sort.Strings(o.Status)
		out = append(out, o)
	}
	return out
}

func (s *Server) listCustomRules(w http.ResponseWriter, r *http.Request, u *model.User) {
	var list []model.CustomRule
	s.Store.View(func(st *store.State) { list = append(list, st.CustomRules...) })
	ok(w, s.customView(list))
}

type customIn struct {
	Name     string    `json:"name"`
	Expr     string    `json:"expr"`
	Desc     string    `json:"desc"`
	Unit     string    `json:"unit"`
	Cluster  string    `json:"cluster"`
	Op       string    `json:"op"`
	Warn     *float64  `json:"warn"`
	Crit     *float64  `json:"crit"`
	DurMin   *int      `json:"durMin"`
	Channels *[]string `json:"channels"`
	Enabled  *bool     `json:"enabled"`
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// checkExpr 校验 PromQL 文本（语法由 Prometheus 在“试运行”和评估时检查）。
func checkExpr(e string) error {
	switch {
	case strings.TrimSpace(e) == "":
		return bad("请填写 PromQL 表达式")
	case rlen(e) > 2000:
		return bad("PromQL 表达式最多 2000 个字符")
	case strings.ContainsRune(e, 0):
		return bad("PromQL 表达式含有非法字符")
	}
	return nil
}

// apply 把输入合并到规则上并校验。
func (in customIn) apply(cr *model.CustomRule, st *store.State) error {
	if in.Name != "" {
		cr.Name = cleanText(in.Name)
	}
	if in.Expr != "" {
		cr.Expr = strings.TrimSpace(in.Expr)
	}
	if in.Op != "" {
		cr.Op = in.Op
	}
	if in.Crit != nil {
		cr.Crit = *in.Crit
	}
	if in.Warn != nil {
		w := *in.Warn
		cr.Warn = &w
	}
	if in.DurMin != nil {
		cr.DurMin = *in.DurMin
	}
	if in.Channels != nil {
		cr.Channels = *in.Channels
	}
	if in.Enabled != nil {
		cr.Enabled = *in.Enabled
	}
	cr.Desc, cr.Unit, cr.Cluster = cleanText(in.Desc), cleanText(in.Unit), in.Cluster
	switch {
	case cr.Name == "" || rlen(cr.Name) > 40:
		return bad("规则名称须为 1–40 个字")
	case rlen(cr.Desc) > 200:
		return bad("说明最多 200 个字")
	case rlen(cr.Unit) > 10:
		return bad("单位最多 10 个字符")
	case cr.Op != ">=" && cr.Op != ">" && cr.Op != "<=" && cr.Op != "<":
		return bad("比较方式须为 ≥ / > / ≤ / <")
	case !finite(cr.Crit) || (cr.Warn != nil && !finite(*cr.Warn)):
		return bad("阈值须为有效数字")
	case cr.DurMin < 0 || cr.DurMin > 1440:
		return bad("持续时间须为 0–1440 分钟")
	}
	if err := checkExpr(cr.Expr); err != nil {
		return err
	}
	if cr.Warn != nil {
		up := cr.Op == ">=" || cr.Op == ">"
		if (up && *cr.Warn > cr.Crit) || (!up && *cr.Warn < cr.Crit) {
			return bad("警告阈值须比严重阈值宽松（" + map[bool]string{true: "≥ / > 规则的警告阈值不能大于严重阈值", false: "≤ / < 规则的警告阈值不能小于严重阈值"}[up] + "）")
		}
	}
	if cr.Cluster != "" {
		found := false
		for _, c := range st.Clusters {
			found = found || c.ID == cr.Cluster
		}
		if !found {
			return bad("集群不存在")
		}
	}
	for _, id := range cr.Channels {
		found := false
		for _, ch := range st.Channels {
			found = found || ch.ID == id
		}
		if !found {
			return bad("通知渠道不存在：" + id)
		}
	}
	for _, name := range func() []string {
		var ns []string
		for _, r := range alertRules {
			ns = append(ns, r.Name)
		}
		return ns
	}() {
		if name == cr.Name {
			return conflict("与内置规则重名")
		}
	}
	for _, x := range st.CustomRules {
		if x.ID != cr.ID && x.Name == cr.Name {
			return conflict("已有同名的自定义规则")
		}
	}
	return nil
}

func (s *Server) createCustomRule(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "告警规则对所有集群生效，需要拥有「全部集群」数据范围的角色才能修改")
		return
	}
	var in customIn
	if !decode(w, r, &in) {
		return
	}
	if in.Crit == nil {
		fail(w, http.StatusBadRequest, "请填写严重阈值")
		return
	}
	if in.Op == "" {
		in.Op = ">="
	}
	cr := model.CustomRule{ID: "custom_" + newAlertID()[len(newAlertID())-8:], Enabled: true, By: u.Login, At: time.Now()}
	err := s.Store.Update(func(st *store.State) error {
		if len(st.CustomRules) >= maxCustomRules {
			return bad(fmt.Sprintf("最多 %d 条自定义规则", maxCustomRules))
		}
		if err := in.apply(&cr, st); err != nil {
			return err
		}
		st.CustomRules = append(st.CustomRules, cr)
		return nil
	})
	s.audit(r, u, cr.Cluster, "alert.rule", "新建自定义告警规则 "+cr.Name+"："+cr.Expr+" "+opText[cr.Op]+" "+fmtNum(cr.Crit), err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, s.customView([]model.CustomRule{cr})[0])
}

func (s *Server) updateCustomRule(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "告警规则对所有集群生效，需要拥有「全部集群」数据范围的角色才能修改")
		return
	}
	rid := r.PathValue("rid")
	var in customIn
	if !decode(w, r, &in) {
		return
	}
	// 只传 enabled 时是启停；否则是整体编辑（未传的字段沿用原值，警告阈值传 null 表示不设）
	toggleOnly := in.Enabled != nil && in.Name == "" && in.Expr == "" && in.Crit == nil
	var out model.CustomRule
	act := ""
	err := s.Store.Update(func(st *store.State) error {
		for i := range st.CustomRules {
			cr := st.CustomRules[i]
			if cr.ID != rid {
				continue
			}
			if toggleOnly {
				cr.Enabled = *in.Enabled
				act = map[bool]string{true: "启用", false: "停用"}[cr.Enabled] + "自定义告警规则 " + cr.Name
			} else {
				cr.Warn = nil // 整体编辑：未传警告阈值 = 不设置警告级别
				if err := in.apply(&cr, st); err != nil {
					return err
				}
				act = "修改自定义告警规则 " + cr.Name + "：" + cr.Expr
			}
			cr.By, cr.At = u.Login, time.Now()
			st.CustomRules[i], out = cr, cr
			return nil
		}
		return missing("自定义规则不存在")
	})
	if act == "" {
		act = "修改自定义告警规则 " + rid
	}
	s.audit(r, u, out.Cluster, "alert.rule", act, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, s.customView([]model.CustomRule{out})[0])
}

// resolveRuleAlerts 把规则下未恢复的告警置为已恢复。
func resolveRuleAlerts(st *store.State, rule string, now time.Time) {
	for i := range st.Alerts {
		a := &st.Alerts[i]
		if a.Rule == rule && a.State != "resolved" {
			a.State, a.ResolvedAt = "resolved", now
		}
	}
}

func (s *Server) deleteCustomRule(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "告警规则对所有集群生效，需要拥有「全部集群」数据范围的角色才能修改")
		return
	}
	rid := r.PathValue("rid")
	name := rid
	err := s.Store.Update(func(st *store.State) error {
		for i, cr := range st.CustomRules {
			if cr.ID == rid {
				name = cr.Name
				st.CustomRules = append(st.CustomRules[:i], st.CustomRules[i+1:]...)
				resolveRuleAlerts(st, rid, time.Now())
				return nil
			}
		}
		return missing("自定义规则不存在")
	})
	if err == nil {
		s.alertMu.Lock()
		for k := range s.customErr {
			if strings.HasPrefix(k, rid+"|") {
				delete(s.customErr, k)
			}
		}
		s.alertMu.Unlock()
	}
	s.audit(r, u, "", "alert.rule", "删除自定义告警规则 "+name, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// testCustomRule 在指定集群的 Prometheus 上试运行表达式，返回前 20 条序列，便于确认表达式写对了。
func (s *Server) testCustomRule(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		Expr    string `json:"expr"`
		Cluster string `json:"cluster"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := checkExpr(in.Expr); err != nil {
		writeErr(w, err)
		return
	}
	c, err := s.Store.Cluster(in.Cluster)
	if err != nil {
		fail(w, http.StatusBadRequest, "请选择一个集群来试运行")
		return
	}
	if !accessOf(r).Cluster(c.ID) {
		fail(w, http.StatusForbidden, "该集群不在您的数据范围内")
		return
	}
	if strings.TrimSpace(c.Prom) == "" {
		fail(w, http.StatusConflict, "集群 "+c.Name+" 没有配置 Prometheus 地址（在「集群接入」中编辑集群）")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	series, err := collect.PromQuery(ctx, c.Prom, expandSel(strings.TrimSpace(in.Expr), c.PromSelector))
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	type row struct {
		Inst   string            `json:"inst"`
		Labels map[string]string `json:"labels"`
		Value  float64           `json:"value"`
	}
	rows := []row{}
	for i, sr := range series {
		if i >= 20 {
			break
		}
		if !finite(sr.V) {
			continue
		}
		rows = append(rows, row{Inst: seriesInst(sr.Labels, c.Name), Labels: sr.Labels, Value: sr.V})
	}
	ok(w, map[string]any{"count": len(series), "series": rows, "expr": expandSel(strings.TrimSpace(in.Expr), c.PromSelector)})
}
