// @author: kevinkelly
// @license: Apache Licence
// @file: alert.go
// @time: 2026/10/28

package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"starrocks-ops/internal/mail"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/notify"
	"starrocks-ops/internal/sms"
	"starrocks-ops/internal/store"
)

// ---------- 告警列表 ----------

type alertOut struct {
	model.Alert
	ClusterName string `json:"clusterName"`
	Silenced    bool   `json:"silenced"`
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request, u *model.User) {
	acc := accessOf(r)
	cluster := r.URL.Query().Get("cluster")
	history := r.URL.Query().Get("history") == "1"
	now := time.Now()
	names := map[string]string{}
	for _, c := range s.Store.Clusters() {
		names[c.ID] = c.Name
	}
	out := []alertOut{}
	counts := map[string]int{"critical": 0, "warning": 0, "info": 0}
	s.Store.View(func(st *store.State) {
		for i := len(st.Alerts) - 1; i >= 0; i-- {
			a := st.Alerts[i]
			if !acc.Cluster(a.Cluster) || (cluster != "" && a.Cluster != cluster) {
				continue
			}
			if history != (a.State == "resolved") {
				continue
			}
			if !history {
				counts[a.Level]++
			}
			if len(out) < 500 {
				out = append(out, alertOut{Alert: a, ClusterName: names[a.Cluster], Silenced: a.State != "resolved" && silenced(st.Silences, a, now)})
			}
		}
	})
	sort.SliceStable(out, func(i, j int) bool {
		if history {
			return out[i].ResolvedAt.After(out[j].ResolvedAt)
		}
		rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
		if rank[out[i].Level] != rank[out[j].Level] {
			return rank[out[i].Level] < rank[out[j].Level]
		}
		return out[i].FirstAt.After(out[j].FirstAt)
	})
	ok(w, map[string]any{"items": out, "counts": counts})
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request, u *model.User) {
	aid := r.PathValue("aid")
	acc := accessOf(r)
	var a model.Alert
	err := s.Store.Update(func(st *store.State) error {
		for i := range st.Alerts {
			if st.Alerts[i].ID != aid {
				continue
			}
			x := &st.Alerts[i]
			switch {
			case !acc.Cluster(x.Cluster):
				return forbidden("该告警所属集群不在您的数据范围内")
			case x.State == "resolved":
				return conflict("该告警已恢复")
			case x.State == "acked":
				return conflict("该告警已被 " + x.AckBy + " 认领")
			}
			x.State, x.AckBy, x.AckAt = "acked", u.Login, time.Now()
			a = *x
			return nil
		}
		return missing("告警不存在")
	})
	s.auditSQL(r, u, "alert.ack", "认领告警 "+a.Name+"（"+a.Inst+"）", "", err, a.Cluster)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---------- 规则 ----------

type ruleOut struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Scope    string    `json:"scope"`
	Unit     string    `json:"unit"`
	Desc     string    `json:"desc"`
	HasWarn  bool      `json:"hasWarn"`
	Warn     float64   `json:"warn"`
	Crit     float64   `json:"crit"`
	DurMin   int       `json:"durMin"`
	Enabled  bool      `json:"enabled"`
	Channels []string  `json:"channels"`
	DefWarn  float64   `json:"defWarn"`
	DefCrit  float64   `json:"defCrit"`
	DefDur   int       `json:"defDur"`
	Custom   bool      `json:"custom"`
	By       string    `json:"by,omitempty"`
	At       time.Time `json:"at,omitempty"`
}

func ruleView(r alertRule, cfg map[string]model.AlertRuleCfg) ruleOut {
	e := effective(r, cfg)
	o := ruleOut{ID: r.ID, Name: r.Name, Scope: r.Scope, Unit: r.Unit, Desc: r.Desc, HasWarn: r.HasWarn, Warn: e.Warn, Crit: e.Crit, DurMin: e.DurMin, Enabled: e.Enabled, Channels: e.Channels,
		DefWarn: r.Warn, DefCrit: r.Crit, DefDur: r.DurMin}
	if o.Channels == nil {
		o.Channels = []string{}
	}
	if c, ok := cfg[r.ID]; ok {
		o.Custom, o.By, o.At = true, c.By, c.At
	}
	return o
}

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request, u *model.User) {
	var cfg map[string]model.AlertRuleCfg
	s.Store.View(func(st *store.State) { cfg = st.AlertCfg })
	out := make([]ruleOut, 0, len(alertRules))
	for _, ru := range alertRules {
		out = append(out, ruleView(ru, cfg))
	}
	ok(w, out)
}

func (s *Server) updateAlertRule(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "告警规则对所有集群生效，需要拥有「全部集群」数据范围的角色才能修改")
		return
	}
	id := r.PathValue("rule")
	def, found := ruleByID(id)
	if !found {
		fail(w, http.StatusNotFound, "规则不存在")
		return
	}
	var in struct {
		Enabled  *bool     `json:"enabled"`
		Warn     *float64  `json:"warn"`
		Crit     *float64  `json:"crit"`
		DurMin   *int      `json:"durMin"`
		Channels *[]string `json:"channels"`
		Reset    bool      `json:"reset"`
	}
	if !decode(w, r, &in) {
		return
	}
	var view ruleOut
	var act string
	err := s.Store.Update(func(st *store.State) error {
		if st.AlertCfg == nil {
			st.AlertCfg = map[string]model.AlertRuleCfg{}
		}
		if in.Reset {
			delete(st.AlertCfg, id)
			act = "恢复默认 " + def.Name
			view = ruleView(def, st.AlertCfg)
			return nil
		}
		c := st.AlertCfg[id]
		var parts []string
		if in.Enabled != nil {
			c.Enabled = in.Enabled
			parts = append(parts, map[bool]string{true: "启用", false: "停用"}[*in.Enabled])
		}
		if in.Warn != nil {
			if !def.HasWarn {
				return bad("该规则没有警告阈值")
			}
			if *in.Warn < 0 {
				return bad("阈值不能为负数")
			}
			c.Warn = in.Warn
			parts = append(parts, "警告阈值 "+fmtNum(*in.Warn))
		}
		if in.Crit != nil {
			if def.ID == "cluster_unreachable" || def.ID == "node_down" {
				return bad("该规则的严重阈值固定为 1")
			}
			if *in.Crit < 0 {
				return bad("阈值不能为负数")
			}
			c.Crit = in.Crit
			parts = append(parts, "严重阈值 "+fmtNum(*in.Crit))
		}
		if in.DurMin != nil {
			if *in.DurMin < 0 || *in.DurMin > 1440 {
				return bad("持续时间须为 0–1440 分钟")
			}
			c.DurMin = in.DurMin
			parts = append(parts, fmt.Sprintf("持续 %d 分钟", *in.DurMin))
		}
		if in.Channels != nil {
			for _, id := range *in.Channels {
				found := false
				for _, ch := range st.Channels {
					found = found || ch.ID == id
				}
				if !found {
					return bad("通知渠道不存在：" + id)
				}
			}
			c.Channels = *in.Channels
			parts = append(parts, fmt.Sprintf("通知渠道 %d 个", len(*in.Channels)))
		}
		e := effective(def, map[string]model.AlertRuleCfg{id: c})
		if def.HasWarn && e.Warn > e.Crit {
			return bad("警告阈值不能大于严重阈值")
		}
		if len(parts) == 0 {
			return bad("没有要修改的内容")
		}
		c.By, c.At = u.Login, time.Now()
		st.AlertCfg[id] = c
		act = strings.Join(parts, "，") + " · " + def.Name
		view = ruleView(def, st.AlertCfg)
		return nil
	})
	if act == "" {
		act = def.Name
	}
	s.audit(r, u, "", "alert.rule", "修改告警规则："+act, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, view)
}

// ---------- 静默 ----------

func (s *Server) listSilences(w http.ResponseWriter, r *http.Request, u *model.User) {
	acc := accessOf(r)
	now := time.Now()
	out := []model.Silence{}
	s.Store.View(func(st *store.State) {
		for _, x := range st.Silences {
			if now.Before(x.Until) && (x.Cluster == "" || acc.Cluster(x.Cluster)) {
				out = append(out, x)
			}
		}
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Until.Before(out[j].Until) })
	ok(w, out)
}

func (s *Server) createSilence(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		Cluster string `json:"cluster"`
		Rule    string `json:"rule"`
		Inst    string `json:"inst"`
		Reason  string `json:"reason"`
		Minutes int    `json:"minutes"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Inst, in.Reason = cleanText(in.Inst), cleanText(in.Reason)
	acc := accessOf(r)
	switch {
	case in.Reason == "" || rlen(in.Reason) > 100:
		fail(w, http.StatusBadRequest, "请填写静默原因（最多 100 个字）")
		return
	case rlen(in.Inst) > 100:
		fail(w, http.StatusBadRequest, "对象匹配文本最多 100 个字")
		return
	case in.Minutes < 5 || in.Minutes > 43200:
		fail(w, http.StatusBadRequest, "静默时长须为 5 分钟到 30 天")
		return
	case in.Rule != "" && s.ruleLabel(in.Rule) == in.Rule && !func() bool { _, ok := ruleByID(in.Rule); return ok }():
		fail(w, http.StatusBadRequest, "规则不存在")
		return
	case in.Cluster == "" && !acc.AllClusters():
		fail(w, http.StatusForbidden, "静默全部集群需要拥有「全部集群」数据范围的角色")
		return
	case in.Cluster != "" && !acc.Cluster(in.Cluster):
		fail(w, http.StatusForbidden, "该集群不在您的数据范围内")
		return
	}
	if in.Cluster != "" {
		if _, err := s.Store.Cluster(in.Cluster); err != nil {
			fail(w, http.StatusBadRequest, "集群不存在")
			return
		}
	}
	now := time.Now()
	sl := model.Silence{ID: "S-" + newAlertID()[3:], Cluster: in.Cluster, Rule: in.Rule, Inst: in.Inst, Reason: in.Reason, By: u.Login, From: now, Until: now.Add(time.Duration(in.Minutes) * time.Minute)}
	err := s.Store.Update(func(st *store.State) error {
		n := 0
		for _, x := range st.Silences {
			if now.Before(x.Until) {
				n++
			}
		}
		if n >= 200 {
			return bad("有效的静默规则已达 200 条")
		}
		st.Silences = append(st.Silences, sl)
		return nil
	})
	s.auditSQL(r, u, "alert.silence", "新建静默："+s.silenceText(sl), "", err, in.Cluster)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, sl)
}

func (s *Server) silenceText(sl model.Silence) string {
	var p []string
	if sl.Cluster != "" {
		p = append(p, "集群="+sl.Cluster)
	}
	if sl.Rule != "" {
		p = append(p, "规则="+s.ruleLabel(sl.Rule))
	}
	if sl.Inst != "" {
		p = append(p, "对象包含 "+sl.Inst)
	}
	if len(p) == 0 {
		p = append(p, "全部告警")
	}
	return strings.Join(p, "，") + "，至 " + sl.Until.Local().Format("2006-01-02 15:04") + "（" + sl.Reason + "）"
}

func (s *Server) deleteSilence(w http.ResponseWriter, r *http.Request, u *model.User) {
	sid := r.PathValue("sid")
	acc := accessOf(r)
	var sl model.Silence
	err := s.Store.Update(func(st *store.State) error {
		for i, x := range st.Silences {
			if x.ID != sid {
				continue
			}
			if (x.Cluster == "" && !acc.AllClusters()) || (x.Cluster != "" && !acc.Cluster(x.Cluster)) {
				return forbidden("该静默规则不在您的数据范围内")
			}
			sl = x
			st.Silences = append(st.Silences[:i], st.Silences[i+1:]...)
			return nil
		}
		return missing("静默规则不存在")
	})
	s.auditSQL(r, u, "alert.silence", "解除静默："+s.silenceText(sl), "", err, sl.Cluster)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---------- 通知渠道 ----------

var alertLevels = []string{"critical", "warning", "info"}

type chanIn struct {
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	URL         string   `json:"url"`
	Secret      string   `json:"secret"`
	ClearSecret bool     `json:"clearSecret"`
	Levels      []string `json:"levels"`
	Enabled     *bool    `json:"enabled"`
}

func chanOut(list []model.Channel) []model.Channel {
	out := make([]model.Channel, len(list))
	for i, c := range list {
		c.URLEnc, c.SecretEnc = "", ""
		if c.Levels == nil {
			c.Levels = []string{}
		}
		out[i] = c
	}
	return out
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request, u *model.User) {
	var list []model.Channel
	s.Store.View(func(st *store.State) { list = append(list, st.Channels...) })
	for i := range list {
		list[i].HasSecret = list[i].SecretEnc != ""
	}
	ok(w, chanOut(list))
}

// buildChannel 校验输入并生成渠道：地址 / 密钥留空表示沿用旧值（类型不变时）。
func (s *Server) buildChannel(in chanIn, old *model.Channel) (model.Channel, error) {
	var c model.Channel
	if old != nil {
		c = *old
	}
	in.Name = cleanText(in.Name)
	switch {
	case in.Name == "" || rlen(in.Name) > 40:
		return c, bad("渠道名称须为 1–40 个字")
	case !contains(notify.Types, in.Type) && in.Type != "邮件" && in.Type != "短信":
		return c, bad("不支持的渠道类型：" + in.Type + "（支持：" + strings.Join(append(append([]string{}, notify.Types...), "邮件", "短信"), "、") + "）")
	case len(in.Levels) == 0:
		return c, bad("请至少选择一个接收级别")
	}
	for _, l := range in.Levels {
		if !contains(alertLevels, l) {
			return c, bad("接收级别只能是 critical / warning / info")
		}
	}
	if old != nil && old.Type != in.Type {
		c.URLEnc, c.URLMasked, c.SecretEnc = "", "", "" // 换了类型：原地址与密钥不再沿用
	}
	c.Type, c.Name, c.Levels = in.Type, in.Name, in.Levels
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	var err error
	switch u := strings.TrimSpace(in.URL); {
	case u != "":
		if in.Type == "邮件" {
			to, e := mail.SplitAddrs(u)
			if e != nil {
				return c, bad(e.Error())
			}
			u = strings.Join(to, ", ")
			c.URLMasked = u
		} else if in.Type == "短信" {
			phones, e := sms.SplitPhones(u)
			if e != nil {
				return c, bad(e.Error())
			}
			u = strings.Join(phones, ", ")
			c.URLMasked = sms.MaskList(phones)
		} else {
			if e := notify.ValidURL(u); e != nil {
				return c, bad(e.Error())
			}
			c.URLMasked = notify.Mask(u)
		}
		if c.URLEnc, err = s.Box.Encrypt(u); err != nil {
			return c, err
		}
	case c.URLEnc == "":
		return c, bad(map[string]string{"邮件": "请填写收件人邮箱", "短信": "请填写接收短信的手机号"}[in.Type] + map[bool]string{true: "", false: "请填写 " + in.Type + " 的 Webhook 地址"}[in.Type == "邮件" || in.Type == "短信"])
	}
	if in.Type == "邮件" || in.Type == "短信" {
		c.SecretEnc = ""
		return c, nil
	}
	switch {
	case in.ClearSecret:
		c.SecretEnc = ""
	case in.Secret != "":
		if c.SecretEnc, err = s.Box.Encrypt(in.Secret); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "通知渠道对所有集群共用，需要拥有「全部集群」数据范围的角色才能管理")
		return
	}
	var in chanIn
	if !decode(w, r, &in) {
		return
	}
	var ch model.Channel
	err := s.Store.Update(func(st *store.State) error {
		if len(st.Channels) >= 30 {
			return bad("最多 30 个通知渠道")
		}
		c, err := s.buildChannel(in, nil)
		if err != nil {
			return err
		}
		for _, x := range st.Channels {
			if x.Name == c.Name {
				return conflict("已有同名的通知渠道")
			}
		}
		if in.Enabled == nil {
			c.Enabled = true
		}
		c.ID, c.CreatedBy, c.CreatedAt = "C-"+newAlertID()[3:], u.Login, time.Now()
		st.Channels = append(st.Channels, c)
		ch = c
		return nil
	})
	s.audit(r, u, "", "alert.channel", "新建通知渠道 "+in.Type+" · "+in.Name, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, chanOut([]model.Channel{ch})[0])
}

func (s *Server) updateChannel(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "通知渠道对所有集群共用，需要拥有「全部集群」数据范围的角色才能管理")
		return
	}
	cid := r.PathValue("cid")
	var in chanIn
	if !decode(w, r, &in) {
		return
	}
	var ch model.Channel
	err := s.Store.Update(func(st *store.State) error {
		for i := range st.Channels {
			if st.Channels[i].ID != cid {
				continue
			}
			old := st.Channels[i]
			c, err := s.buildChannel(in, &old)
			if err != nil {
				return err
			}
			for _, x := range st.Channels {
				if x.ID != cid && x.Name == c.Name {
					return conflict("已有同名的通知渠道")
				}
			}
			st.Channels[i], ch = c, c
			return nil
		}
		return missing("通知渠道不存在")
	})
	s.audit(r, u, "", "alert.channel", "编辑通知渠道 "+in.Type+" · "+in.Name, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, chanOut([]model.Channel{ch})[0])
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "通知渠道对所有集群共用，需要拥有「全部集群」数据范围的角色才能管理")
		return
	}
	cid := r.PathValue("cid")
	name := cid
	err := s.Store.Update(func(st *store.State) error {
		for i, c := range st.Channels {
			if c.ID == cid {
				name = c.Type + " · " + c.Name
				st.Channels = append(st.Channels[:i], st.Channels[i+1:]...)
				for i := range st.CustomRules {
					var keep []string
					for _, id := range st.CustomRules[i].Channels {
						if id != cid {
							keep = append(keep, id)
						}
					}
					st.CustomRules[i].Channels = keep
				}
				for k, cfg := range st.AlertCfg { // 规则里不再引用已删除的渠道
					var keep []string
					for _, id := range cfg.Channels {
						if id != cid {
							keep = append(keep, id)
						}
					}
					cfg.Channels = keep
					st.AlertCfg[k] = cfg
				}
				return nil
			}
		}
		return missing("通知渠道不存在")
	})
	s.audit(r, u, "", "alert.channel", "删除通知渠道 "+name, err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// testChannel 向渠道发送一条测试消息（渠道停用时也可测试）。
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request, u *model.User) {
	cid := r.PathValue("cid")
	var ch model.Channel
	found := false
	s.Store.View(func(st *store.State) {
		for _, c := range st.Channels {
			if c.ID == cid {
				ch, found = c, true
			}
		}
	})
	if !found {
		fail(w, http.StatusNotFound, "通知渠道不存在")
		return
	}
	now := time.Now()
	m := s.alertMessage("（测试集群）", "node_down", []model.Alert{{ID: "TEST", Level: "critical", Inst: "BE 10.0.0.1:8040", Desc: "这是一条测试消息，由 " + u.Login + " 在「告警中心 → 通知渠道」发送", Value: "1", FirstAt: now}}, false, now)
	m.Title = "【测试】" + m.Title
	m.SMS = "【测试】" + m.SMS
	err := s.sendChannel(r.Context(), ch, m)
	res := "成功"
	if err != nil {
		res = "失败：" + err.Error()
	}
	s.Store.Update(func(st *store.State) error {
		for i := range st.Channels {
			if st.Channels[i].ID == cid {
				st.Channels[i].LastAt, st.Channels[i].LastResult = now, res
			}
		}
		return nil
	})
	s.audit(r, u, "", "alert.channel", "测试通知渠道 "+ch.Type+" · "+ch.Name, err)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, map[string]bool{"ok": true})
}
