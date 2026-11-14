// @author: kevinkelly
// @license: Apache Licence
// @file: backup.go
// @time: 2026/10/28

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
	"starrocks-ops/internal/store"
)

// ---------- 总览：作业 / 仓库 / 策略 ----------

type policyOut struct {
	model.BackupPolicy
	Next time.Time `json:"next"`
}

func (s *Server) policiesOf(cluster string) []policyOut {
	out := []policyOut{}
	s.Store.View(func(st *store.State) {
		for _, p := range st.Backups {
			if p.Cluster == cluster {
				o := policyOut{BackupPolicy: p}
				if p.Enabled {
					o.Next = nextRun(p, time.Now())
				}
				out = append(out, o)
			}
		}
	})
	return out
}

func (s *Server) backupOverview(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	var warns []string
	repos, err := x.cl.Repos(x.ctx)
	if err != nil {
		warns = append(warns, "读取仓库失败："+err.Error())
	}
	if repos == nil {
		repos = []sr.Repo{}
	}
	jobs, w2, err := x.cl.BackupJobs(x.ctx)
	if err != nil {
		warns = append(warns, "读取备份作业失败："+err.Error())
	}
	warns = append(warns, w2...)
	if jobs == nil {
		jobs = []sr.BackupJob{}
	}
	dbs, _ := x.cl.Databases(x.ctx)
	ok(w, map[string]any{"jobs": jobs, "repos": repos, "policies": s.policiesOf(x.c.ID), "databases": dbs, "warnings": warns})
}

func (s *Server) repoSnapshots(w http.ResponseWriter, r *http.Request, u *model.User) {
	repo := r.PathValue("repo")
	if !sr.ValidIdent(repo) {
		fail(w, http.StatusBadRequest, "仓库名不合法")
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	list, err := x.cl.Snapshots(x.ctx, repo)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取快照失败："+err.Error())
		return
	}
	if list == nil {
		list = []sr.Snapshot{}
	}
	ok(w, map[string]any{"items": list})
}

// ---------- 仓库 ----------

func (s *Server) createRepo(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in sr.RepoSpec
	if !decode(w, r, &in) {
		return
	}
	q, err := in.SQL()
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	if repos, err := x.cl.Repos(x.ctx); err == nil {
		for _, rp := range repos {
			if rp.Name == in.Name {
				fail(w, http.StatusConflict, "仓库 "+in.Name+" 已存在")
				return
			}
		}
	}
	if err := s.exec(r, u, x, "backup.repo", "创建仓库 "+in.Name+"（"+in.Location+"）", q); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) dropRepo(w http.ResponseWriter, r *http.Request, u *model.User) {
	name := r.PathValue("repo")
	q, err := sr.DropRepoSQL(name)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	if err := s.exec(r, u, x, "backup.repo", "删除仓库 "+name, q); err != nil {
		sqlFail(w, err)
		return
	}
	s.Store.Update(func(st *store.State) error {
		for i, rc := range st.RepoCreds {
			if rc.Cluster == x.c.ID && rc.Repo == name {
				st.RepoCreds = append(st.RepoCreds[:i], st.RepoCreds[i+1:]...)
				break
			}
		}
		return nil
	})
	ok(w, map[string]bool{"ok": true})
}

// ---------- 备份 / 恢复 / 取消 ----------

func (s *Server) activeJobGuard(w http.ResponseWriter, x srCtx, db string) bool {
	busy, what, err := x.cl.HasActiveJob(x.ctx, db)
	if err != nil {
		fail(w, http.StatusBadGateway, "检查数据库 "+db+" 的作业失败："+err.Error())
		return false
	}
	if busy {
		fail(w, http.StatusConflict, "数据库 "+db+" 上已有进行中的 "+what+"；同一数据库同时只能有一个备份 / 恢复作业")
		return false
	}
	return true
}

func (s *Server) startBackup(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in sr.BackupSpec
	if !decode(w, r, &in) {
		return
	}
	q, err := in.SQL()
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	if !s.activeJobGuard(w, x, in.DB) {
		return
	}
	obj := "整库"
	if len(in.Tables) > 0 {
		obj = strings.Join(in.Tables, "、")
	}
	if err := s.exec(r, u, x, "backup.create", fmt.Sprintf("发起备份 %s.%s → %s（%s）", in.DB, in.Snapshot, in.Repo, obj), q); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) startRestore(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in sr.RestoreSpec
	if !decode(w, r, &in) {
		return
	}
	q, err := in.SQL()
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	if !s.activeJobGuard(w, x, in.DB) {
		return
	}
	obj := "快照内全部表"
	if len(in.Tables) > 0 {
		var ns []string
		for _, t := range in.Tables {
			if t.As != "" {
				ns = append(ns, t.Name+" → "+t.As)
			} else {
				ns = append(ns, t.Name)
			}
		}
		obj = strings.Join(ns, "、")
	}
	if err := s.exec(r, u, x, "backup.restore", fmt.Sprintf("恢复 %s.%s ← %s（%s）", in.DB, in.Snapshot, in.Repo, obj), q); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		DB   string `json:"db"`
		Kind string `json:"kind"`
	}
	if !decode(w, r, &in) {
		return
	}
	q, err := sr.CancelSQL(in.Kind, in.DB)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	if err := s.exec(r, u, x, "backup.cancel", map[string]string{"backup": "取消备份作业 ", "restore": "取消恢复作业 "}[in.Kind]+in.DB, q); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---------- 备份策略 ----------

type policyIn struct {
	Name    string   `json:"name"`
	DBs     []string `json:"dbs"`
	Repo    string   `json:"repo"`
	Every   string   `json:"every"`
	At      string   `json:"at"`
	Weekday int      `json:"weekday"`
	Enabled *bool    `json:"enabled"`
	// 保留策略：只清理本策略创建的快照；0 = 不按该条件清理
	KeepLast int `json:"keepLast"`
	KeepDays int `json:"keepDays"`
}

func (in *policyIn) validate() error {
	in.Name = cleanText(in.Name)
	switch {
	case in.Name == "" || rlen(in.Name) > 40:
		return fmt.Errorf("策略名称须为 1–40 个字")
	case len(in.DBs) == 0 || len(in.DBs) > 50:
		return fmt.Errorf("请选择 1–50 个数据库")
	case !sr.ValidIdent(in.Repo):
		return fmt.Errorf("仓库名不合法")
	case in.Every != "daily" && in.Every != "weekly":
		return fmt.Errorf("周期须为 daily 或 weekly")
	case !hhmm.MatchString(in.At):
		return fmt.Errorf("时间格式须为 HH:MM")
	case in.Weekday < 0 || in.Weekday > 6:
		return fmt.Errorf("星期须为 0–6（0 为周日）")
	case in.KeepLast < 0 || in.KeepLast > 1000:
		return fmt.Errorf("保留份数须为 0–1000（0 = 不限）")
	case in.KeepDays < 0 || in.KeepDays > 3650:
		return fmt.Errorf("保留天数须为 0–3650（0 = 不限）")
	}
	seen := map[string]bool{}
	for _, d := range in.DBs {
		if !sr.ValidIdent(d) {
			return fmt.Errorf("数据库名 %q 不合法", d)
		}
		if seen[d] {
			return fmt.Errorf("数据库 %s 重复", d)
		}
		seen[d] = true
	}
	return nil
}

func newPolicyID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "bp_" + hex.EncodeToString(b)
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in policyIn
	if !decode(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	if msg := s.checkPolicyRepo(r.Context(), c, in.Repo); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	if msg := s.checkRetention(r.Context(), c, in); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	p := model.BackupPolicy{ID: newPolicyID(), Cluster: c.ID, Name: in.Name, DBs: in.DBs, Repo: in.Repo, Every: in.Every, At: in.At, Weekday: in.Weekday, KeepLast: in.KeepLast, KeepDays: in.KeepDays,
		Enabled: in.Enabled == nil || *in.Enabled, CreatedBy: u.Login, CreatedAt: time.Now(), LastRun: time.Now()} // 从下一个周期开始
	err := s.Store.Update(func(st *store.State) error {
		n := 0
		for _, x := range st.Backups {
			if x.Cluster == c.ID {
				n++
				if x.Name == p.Name {
					return conflict("已有同名的备份策略")
				}
			}
		}
		if n >= 50 {
			return bad("每个集群最多 50 条备份策略")
		}
		st.Backups = append(st.Backups, p)
		return nil
	})
	s.auditSQL(r, u, "backup.policy", "新建备份策略 "+p.Name+"："+describePolicy(p), "", err, c.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, policyOut{BackupPolicy: p, Next: nextRun(p, time.Now())})
}

// checkRetention 设置了保留策略时，要求仓库支持清理且已配置清理凭据。
func (s *Server) checkRetention(ctx context.Context, c model.Cluster, in policyIn) string {
	if in.KeepLast == 0 && in.KeepDays == 0 {
		return ""
	}
	if _, found := s.repoCred(c.ID, in.Repo); !found {
		return "设置保留策略前，请先为仓库 " + in.Repo + " 配置清理凭据（仓库列表 → 清理设置）"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if cl, err := s.Mgr.Client(ctx, c); err == nil {
		if rp, err := findRepo(ctx, cl, in.Repo); err == nil {
			if _, err := parseRepoLoc(rp.Location); err != nil {
				return err.Error()
			}
		}
	}
	return ""
}

// checkPolicyRepo 校验仓库存在且可写；无法连接集群时不阻止保存（返回空串）。
func (s *Server) checkPolicyRepo(ctx context.Context, c model.Cluster, repo string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cl, err := s.Mgr.Client(ctx, c)
	if err != nil {
		return ""
	}
	repos, err := cl.Repos(ctx)
	if err != nil {
		return ""
	}
	for _, rp := range repos {
		if rp.Name == repo {
			if rp.ReadOnly {
				return "仓库 " + repo + " 是只读仓库，不能用于备份"
			}
			return ""
		}
	}
	return "仓库 " + repo + " 不存在"
}

func describePolicy(p model.BackupPolicy) string {
	when := "每天 " + p.At
	if p.Every == "weekly" {
		when = "每" + []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[p.Weekday%7] + " " + p.At
	}
	d := fmt.Sprintf("%s，库 %s，仓库 %s", when, strings.Join(p.DBs, "、"), p.Repo)
	switch {
	case p.KeepLast > 0 && p.KeepDays > 0:
		d += fmt.Sprintf("，保留最近 %d 份且 %d 天内的", p.KeepLast, p.KeepDays)
	case p.KeepLast > 0:
		d += fmt.Sprintf("，保留最近 %d 份", p.KeepLast)
	case p.KeepDays > 0:
		d += fmt.Sprintf("，保留 %d 天内的", p.KeepDays)
	}
	return d
}

func (s *Server) updatePolicy(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in policyIn
	if !decode(w, r, &in) {
		return
	}
	cid, pid := r.PathValue("id"), r.PathValue("pid")
	var out model.BackupPolicy
	act := ""
	toggleOnly := in.Name == "" && len(in.DBs) == 0 && in.Repo == "" && in.Every == "" && in.At == ""
	err := s.Store.Update(func(st *store.State) error {
		for i := range st.Backups {
			p := &st.Backups[i]
			if p.ID != pid || p.Cluster != cid {
				continue
			}
			if toggleOnly {
				if in.Enabled == nil {
					return bad("没有要修改的内容")
				}
				p.Enabled = *in.Enabled
				act = map[bool]string{true: "启用", false: "停用"}[p.Enabled] + "备份策略 " + p.Name
				if p.Enabled {
					p.LastRun = time.Now() // 重新启用后从下一个周期开始，不补跑停用期间错过的
				}
			} else {
				if err := in.validate(); err != nil {
					return bad(err.Error())
				}
				for _, x := range st.Backups {
					if x.Cluster == cid && x.ID != pid && x.Name == in.Name {
						return conflict("已有同名的备份策略")
					}
				}
				if in.KeepLast > 0 || in.KeepDays > 0 {
					found := false
					for _, rc := range st.RepoCreds {
						found = found || (rc.Cluster == cid && rc.Repo == in.Repo)
					}
					if !found {
						return bad("设置保留策略前，请先为仓库 " + in.Repo + " 配置清理凭据（仓库列表 → 清理设置）")
					}
				}
				p.Name, p.DBs, p.Repo, p.Every, p.At, p.Weekday = in.Name, in.DBs, in.Repo, in.Every, in.At, in.Weekday
				p.KeepLast, p.KeepDays = in.KeepLast, in.KeepDays
				if in.Enabled != nil {
					p.Enabled = *in.Enabled
				}
				p.LastRun = time.Now()
				act = "修改备份策略 " + p.Name + "：" + describePolicy(*p)
			}
			out = *p
			return nil
		}
		return missing("备份策略不存在")
	})
	if act == "" {
		act = "修改备份策略 " + pid
	}
	s.auditSQL(r, u, "backup.policy", act, "", err, cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	res := policyOut{BackupPolicy: out}
	if out.Enabled {
		res.Next = nextRun(out, time.Now())
	}
	ok(w, res)
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request, u *model.User) {
	cid, pid := r.PathValue("id"), r.PathValue("pid")
	name := pid
	err := s.Store.Update(func(st *store.State) error {
		for i, p := range st.Backups {
			if p.ID == pid && p.Cluster == cid {
				name = p.Name
				st.Backups = append(st.Backups[:i], st.Backups[i+1:]...)
				return nil
			}
		}
		return missing("备份策略不存在")
	})
	s.auditSQL(r, u, "backup.policy", "删除备份策略 "+name, "", err, cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---------- 定时执行 ----------

// lastDue 返回不晚于 now 的最近一个计划时刻。
func lastDue(p model.BackupPolicy, now time.Time) time.Time {
	var h, m int
	fmt.Sscanf(p.At, "%d:%d", &h, &m)
	t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if p.Every == "weekly" {
		t = t.AddDate(0, 0, -((int(t.Weekday()) - p.Weekday + 7) % 7))
	}
	for t.After(now) {
		if p.Every == "weekly" {
			t = t.AddDate(0, 0, -7)
		} else {
			t = t.AddDate(0, 0, -1)
		}
	}
	return t
}

// nextRun 返回晚于 now 的下一个计划时刻。
func nextRun(p model.BackupPolicy, now time.Time) time.Time {
	t := lastDue(p, now)
	for !t.After(now) {
		if p.Every == "weekly" {
			t = t.AddDate(0, 0, 7)
		} else {
			t = t.AddDate(0, 0, 1)
		}
	}
	return t
}

var backupRunning sync.Map // 策略 ID → 正在执行

// RunDueBackups 执行已到点的备份策略，返回本次执行的策略数；now 便于测试传入。
func (s *Server) RunDueBackups(ctx context.Context, now time.Time) int {
	var due []model.BackupPolicy
	s.Store.View(func(st *store.State) {
		for _, p := range st.Backups {
			if p.Enabled && p.LastRun.Before(lastDue(p, now)) {
				due = append(due, p)
			}
		}
	})
	n := 0
	for _, p := range due {
		if _, busy := backupRunning.LoadOrStore(p.ID, true); busy {
			continue
		}
		n++
		s.runPolicy(ctx, p, now)
		backupRunning.Delete(p.ID)
	}
	return n
}

// runPolicy 对策略中的每个库发起备份；已有进行中作业的库会跳过。结果记入策略与审计日志。
func (s *Server) runPolicy(ctx context.Context, p model.BackupPolicy, now time.Time) {
	var parts []string
	c, err := s.Store.Cluster(p.Cluster)
	var cl *sr.Client
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cl, err = s.Mgr.Client(cctx, c)
		cancel()
	}
	if err != nil {
		parts = append(parts, "连接集群失败："+err.Error())
	} else {
		stamp := now.Format("20060102_150405")
		for _, db := range p.DBs {
			qctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			snap := fmt.Sprintf("auto_%s_%s", strings.TrimPrefix(p.ID, "bp_"), stamp)
			spec := sr.BackupSpec{DB: db, Snapshot: snap, Repo: p.Repo}
			q, e := spec.SQL()
			if e == nil {
				var busy bool
				var what string
				if busy, what, e = cl.HasActiveJob(qctx, db); e == nil && busy {
					parts = append(parts, db+" 跳过（已有进行中的 "+what+"）")
					cancel()
					s.Store.Audit(store.AuditEntry{User: "系统（备份策略）", Cluster: p.Cluster, Code: "backup.create", Act: "定时备份 " + p.Name + "：" + db + " 跳过，已有进行中的作业", Result: "失败：已有进行中的作业"})
					continue
				}
				if e == nil {
					e = cl.Exec(qctx, q)
				}
			}
			cancel()
			res := "成功"
			if e != nil {
				res = "失败：" + e.Error()
				parts = append(parts, db+" 失败："+e.Error())
			} else {
				parts = append(parts, db+" 已提交（"+snap+"）")
			}
			s.Store.Audit(store.AuditEntry{User: "系统（备份策略）", Cluster: p.Cluster, Code: "backup.create", Act: "定时备份 " + p.Name + "：" + db + " → " + p.Repo, Result: res, SQL: q + ";"})
		}
	}
	s.Store.Update(func(st *store.State) error {
		for i := range st.Backups {
			if st.Backups[i].ID == p.ID {
				st.Backups[i].LastRun, st.Backups[i].LastResult = now, strings.Join(parts, "；")
			}
		}
		return nil
	})
}

// backupLoop 每 30 秒检查一次到点的备份策略。
func (s *Server) backupLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.RunDueBackups(ctx, now)
			s.RunDueCleanups(ctx, now)
		}
	}
}
