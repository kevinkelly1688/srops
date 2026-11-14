// @author: kevinkelly
// @license: Apache Licence
// @file: views.go
// @time: 2026/10/28

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"starrocks-ops/internal/inspect"
	"starrocks-ops/internal/mail"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/notify"
	"starrocks-ops/internal/store"
)

func (s *Server) overview(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	o, err := s.Mgr.Overview(ctx, c)
	if err != nil {
		fail(w, http.StatusBadGateway, "连接集群失败："+err.Error())
		return
	}
	if list := s.Store.Reports(c.ID, inspect.KeepFor()); len(list) > 0 {
		o.LastReport = &list[0]
	}
	ok(w, o)
}

func (s *Server) topology(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	t, _, err := s.Mgr.Topology(ctx, c)
	if err != nil {
		fail(w, http.StatusBadGateway, "连接集群失败："+err.Error())
		return
	}
	ok(w, t)
}

// ---------- 巡检 ----------

func (s *Server) listRules(w http.ResponseWriter, r *http.Request, u *model.User) {
	ok(w, map[string]any{"cats": inspect.Cats, "rules": s.Runner.RuleViews()})
}

func (s *Server) updateRule(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "巡检规则阈值对所有集群生效，需要拥有「全部集群」数据范围的角色才能修改")
		return
	}
	id := r.PathValue("rule")
	var def *inspect.Rule
	for _, x := range inspect.Rules() {
		if x.ID == id {
			x := x
			def = &x
		}
	}
	if def == nil {
		fail(w, http.StatusNotFound, "规则不存在")
		return
	}
	var in struct {
		Enabled *bool    `json:"enabled"`
		Warn    *float64 `json:"warn"`
		Bad     *float64 `json:"bad"`
		Reset   bool     `json:"reset"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.Warn != nil && def.Warn == nil) || (in.Bad != nil && def.Bad == nil) {
		fail(w, http.StatusBadRequest, "该规则不支持修改此阈值")
		return
	}
	if in.Warn != nil && in.Bad != nil && !in.Reset {
		if (!def.Lower && *in.Bad < *in.Warn) || (def.Lower && *in.Bad > *in.Warn) {
			fail(w, http.StatusBadRequest, "异常阈值应比警告阈值更严格")
			return
		}
	}
	err := s.Store.Update(func(st *store.State) error {
		if in.Reset {
			delete(st.Rules, id)
			return nil
		}
		c := st.Rules[id]
		if in.Enabled != nil {
			c.Enabled = in.Enabled
		}
		if in.Warn != nil {
			c.Warn = in.Warn
		}
		if in.Bad != nil {
			c.Bad = in.Bad
		}
		st.Rules[id] = c
		return nil
	})
	s.audit(r, u, "", "inspect.rule", "修改巡检规则 "+def.Name, err)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, v := range s.Runner.RuleViews() {
		if v.ID == id {
			ok(w, v)
			return
		}
	}
}

func (s *Server) listReports(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	list := s.Store.Reports(c.ID, inspect.KeepFor())
	if list == nil {
		list = []model.ReportMeta{}
	}
	ok(w, list)
}

func (s *Server) runInspection(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	var in struct {
		Cats []string `json:"cats"`
	}
	if r.ContentLength > 0 && !decode(w, r, &in) {
		return
	}
	rep, err := s.Runner.Run(r.Context(), c, u.Login, in.Cats)
	act := "执行性能巡检"
	if rep != nil {
		act = fmt.Sprintf("执行性能巡检 %s，健康分 %d", rep.ID, rep.Score)
	}
	s.audit(r, u, c.ID, "inspect.run", act, err)
	switch {
	case errors.Is(err, inspect.ErrBusy):
		fail(w, http.StatusConflict, err.Error())
	case err != nil:
		fail(w, http.StatusBadGateway, "巡检失败："+err.Error())
	default:
		ok(w, rep)
	}
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	rep, err := s.Store.Report(c.ID, r.PathValue("rid"))
	if err != nil {
		fail(w, http.StatusNotFound, "报告不存在")
		return
	}
	ok(w, rep)
}

func (s *Server) ignore(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	var in struct {
		Rule   string `json:"rule"`
		Ignore bool   `json:"ignore"`
	}
	if !decode(w, r, &in) {
		return
	}
	known := false
	for _, x := range inspect.Rules() {
		known = known || x.ID == in.Rule
	}
	if !known {
		fail(w, http.StatusBadRequest, "规则不存在")
		return
	}
	rep, err := s.Runner.SetIgnore(c.ID, in.Rule, in.Ignore)
	s.audit(r, u, c.ID, "inspect.ignore", map[bool]string{true: "忽略", false: "取消忽略"}[in.Ignore]+"巡检项 "+in.Rule, err)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, rep)
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	var sc model.Schedule
	var has bool
	s.Store.View(func(st *store.State) { sc, has = st.Schedules[c.ID] })
	if !has {
		sc = model.Schedule{Enabled: false, Every: "daily", At: "10:00", Hours: 6}
	}
	ok(w, sc.Public())
}

var hhmm = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func (s *Server) putSchedule(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	var in model.Schedule
	if !decode(w, r, &in) {
		return
	}
	if in.Every != "daily" && in.Every != "hours" {
		fail(w, http.StatusBadRequest, "周期须为 daily 或 hours")
		return
	}
	if in.Every == "daily" && !hhmm.MatchString(in.At) {
		fail(w, http.StatusBadRequest, "时间格式须为 HH:MM")
		return
	}
	if in.Every == "hours" && (in.Hours < 1 || in.Hours > 168) {
		fail(w, http.StatusBadRequest, "间隔须为 1–168 小时")
		return
	}
	for _, cat := range in.Cats {
		if !contains(inspect.Cats, cat) {
			fail(w, http.StatusBadRequest, "未知分类："+cat)
			return
		}
	}
	err := s.Store.Update(func(st *store.State) error {
		old := st.Schedules[c.ID]
		n, err := s.mergeNotify(in.Notify, old.Notify)
		if err != nil {
			return err
		}
		in.Notify = n
		in.LastRun, in.LastNotify, in.LastNotifyResult = old.LastRun, old.LastNotify, old.LastNotifyResult
		if in.LastRun.IsZero() {
			in.LastRun = time.Now() // 新建的定时任务从下一个周期开始
		}
		st.Schedules[c.ID] = in
		return nil
	})
	desc := "停用定时巡检"
	if in.Enabled {
		desc = "启用定时巡检：" + map[string]string{"daily": "每天 " + in.At, "hours": fmt.Sprintf("每 %d 小时", in.Hours)}[in.Every]
	}
	if err == nil && in.Notify != nil {
		desc += "，结果通知：" + in.Notify.Type + map[string]string{"always": "（每次）", "problem": fmt.Sprintf("（有异常或低于 %d 分）", in.Notify.MinScore)}[in.Notify.When]
	}
	var bad badRequest
	if errors.As(err, &bad) {
		s.audit(r, u, c.ID, "inspect.schedule", desc, err)
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, u, c.ID, "inspect.schedule", desc, err)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	ok(w, in.Public())
}

// badRequest 标记参数错误（返回 400 而不是 500）。
type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

// mergeNotify 校验提交的通知配置并与已保存的合并：地址 / 密钥留空表示保持不变，明文加密后保存。
func (s *Server) mergeNotify(in, old *model.Notify) (*model.Notify, error) {
	if in == nil {
		return nil, nil
	}
	if !contains(notify.Types, in.Type) && in.Type != "邮件" {
		return nil, badRequest{"不支持的通知类型：" + in.Type}
	}
	if in.When != "always" && in.When != "problem" {
		return nil, badRequest{"通知条件须为 always 或 problem"}
	}
	if in.MinScore < 0 || in.MinScore > 100 {
		return nil, badRequest{"健康分阈值须为 0–100"}
	}
	out := &model.Notify{Type: in.Type, When: in.When, MinScore: in.MinScore}
	if old != nil && old.Type != in.Type {
		old = nil // 换了通知类型：原地址与密钥不再沿用
	}
	var err error
	switch {
	case strings.TrimSpace(in.URL) != "":
		u := strings.TrimSpace(in.URL)
		if in.Type == "邮件" { // 收件人列表
			to, err := mail.SplitAddrs(u)
			if err != nil {
				return nil, badRequest{err.Error()}
			}
			u = strings.Join(to, ", ")
			out.URLMasked = u
		} else {
			if err := notify.ValidURL(u); err != nil {
				return nil, badRequest{err.Error()}
			}
			out.URLMasked = notify.Mask(u)
		}
		if out.URLEnc, err = s.Box.Encrypt(u); err != nil {
			return nil, err
		}
	case old != nil && old.URLEnc != "":
		out.URLEnc, out.URLMasked = old.URLEnc, old.URLMasked
	default:
		if in.Type == "邮件" {
			return nil, badRequest{"请填写收件人邮箱"}
		}
		return nil, badRequest{"请填写 " + in.Type + " 的 Webhook 地址"}
	}
	if in.Type == "邮件" {
		return out, nil // 邮件不使用签名密钥
	}
	switch {
	case in.ClearSecret:
	case in.Secret != "":
		if out.SecretEnc, err = s.Box.Encrypt(in.Secret); err != nil {
			return nil, err
		}
	case old != nil:
		out.SecretEnc = old.SecretEnc
	}
	return out, nil
}

// testNotify 用提交的配置（未填的地址 / 密钥沿用已保存的）发送一条测试消息：内容为最近一次巡检报告。
func (s *Server) testNotify(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	var in model.Notify
	if !decode(w, r, &in) {
		return
	}
	var old *model.Notify
	s.Store.View(func(st *store.State) { old = st.Schedules[c.ID].Notify })
	n, err := s.mergeNotify(&in, old)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	var m notifyMsg
	if list := s.Store.Reports(c.ID, inspect.KeepFor()); len(list) > 0 {
		if rep, e := s.Store.Report(c.ID, list[0].ID); e == nil {
			m = inspect.ReportMessage(c, rep)
		}
	}
	if m.Title == "" {
		m = notifyMsg{Title: "StarRocks 巡检 · " + c.Name, Level: "ok", Summary: []string{"该集群还没有巡检报告。"}}
	}
	m = s.Runner.WithLink(m)
	m.Title = "【测试】" + m.Title
	m.Summary = append(m.Summary, "这是一条测试消息，由 "+u.Login+" 在「性能巡检 → 定时巡检」中发送。")
	err = s.Runner.SendNotify(r.Context(), n, m)
	s.audit(r, u, c.ID, "inspect.schedule", "测试巡检通知（"+n.Type+"）", err)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, map[string]any{"ok": true})
}

type notifyMsg = notify.Message

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
