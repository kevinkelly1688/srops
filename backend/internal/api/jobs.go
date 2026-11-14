// @author: kevinkelly
// @license: Apache Licence
// @file: jobs.go
// @time: 2026/10/28

package api

import (
	"net/http"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

// ---------- 导入作业 ----------

func (s *Server) listLoads(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	var warns []string
	jobs, err := x.cl.RoutineLoads(x.ctx)
	if err != nil {
		warns = append(warns, "读取 Routine Load 失败："+err.Error())
	}
	if jobs == nil {
		jobs = []sr.RoutineLoad{}
	}
	recent, err := x.cl.RecentLoads(x.ctx, 50)
	if err != nil {
		warns = append(warns, "读取最近导入失败："+err.Error())
	}
	if recent == nil {
		recent = []sr.LoadJob{}
	}
	stats, err := x.cl.LoadStats(x.ctx)
	if err != nil {
		stats = map[string]int64{}
	}
	ok(w, map[string]any{"jobs": jobs, "recent": recent, "stats": stats, "warnings": warns})
}

func (s *Server) loadDetail(w http.ResponseWriter, r *http.Request, u *model.User) {
	db, name := r.PathValue("db"), r.PathValue("name")
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	jobs, err := x.cl.RoutineLoads(x.ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, j := range jobs {
		if j.DB != db || j.Name != name {
			continue
		}
		var cols []string
		var tasks [][]string
		if rs, err := x.cl.RoutineLoadTasks(x.ctx, db, name); err == nil {
			cols, tasks = rs.Cols, rs.Data
		}
		s.auditSQL(r, u, "load.detail", "查看错误详情 "+db+"."+name, "", nil, x.c.ID)
		ok(w, map[string]any{"job": j, "taskCols": cols, "tasks": tasks})
		return
	}
	fail(w, http.StatusNotFound, "作业不存在")
}

func (s *Server) loadOp(op, code, label string) handler {
	return func(w http.ResponseWriter, r *http.Request, u *model.User) {
		db, name := r.PathValue("db"), r.PathValue("name")
		x, done, ok2 := s.open(w, r, 30*time.Second)
		if !ok2 {
			return
		}
		defer done()
		q, err := x.cl.RoutineLoadSQL(op, db, name)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.exec(r, u, x, code, label+" Routine Load "+db+"."+name, q); err != nil {
			sqlFail(w, err)
			return
		}
		ok(w, map[string]bool{"ok": true})
	}
}

// ---------- 物化视图 ----------

func (s *Server) listMVs(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	mvs, err := x.cl.MVs(x.ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取物化视图失败："+err.Error())
		return
	}
	if mvs == nil {
		mvs = []sr.MV{}
	}
	ok(w, map[string]any{"items": mvs})
}

func (s *Server) mvOp(op, code, label string) handler {
	return func(w http.ResponseWriter, r *http.Request, u *model.User) {
		db, name := r.PathValue("db"), r.PathValue("name")
		var opt sr.MVRefreshOpt
		realOp, label := op, label
		switch op {
		case "refresh":
			var in struct {
				Start string `json:"start"`
				End   string `json:"end"`
				Force bool   `json:"force"`
			}
			if r.ContentLength != 0 && !decode(w, r, &in) {
				return
			}
			opt = sr.MVRefreshOpt{Start: strings.TrimSpace(in.Start), End: strings.TrimSpace(in.End), Force: in.Force}
		case "toggle":
			var in struct {
				Active bool `json:"active"`
			}
			if !decode(w, r, &in) {
				return
			}
			realOp = map[bool]string{true: "active", false: "inactive"}[in.Active]
			label = map[bool]string{true: "启用", false: "停用"}[in.Active]
		}
		q, err := sr.MVSQL(realOp, db, name, opt)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		x, done, ok2 := s.open(w, r, 30*time.Second)
		if !ok2 {
			return
		}
		defer done()
		if err := s.exec(r, u, x, code, label+"物化视图 "+db+"."+name, q); err != nil {
			sqlFail(w, err)
			return
		}
		ok(w, map[string]bool{"ok": true})
	}
}
