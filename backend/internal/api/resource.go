// @author: kevinkelly
// @license: Apache Licence
// @file: resource.go
// @time: 2026/10/28

package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

// ---------- 资源组 ----------

type rgOut struct {
	sr.ResourceGroup
	Running int     `json:"running"`
	Queued  int     `json:"queued"` // -1 表示当前版本无法获取
	CPUUsed float64 `json:"cpuUsed"`
	MemUsed int64   `json:"memUsed"`
}

func (s *Server) listRG(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	groups, err := x.cl.ResourceGroups(x.ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取资源组失败："+err.Error())
		return
	}
	var warns []string
	use := map[string]*rgOut{}
	out := make([]rgOut, len(groups))
	for i, g := range groups {
		out[i] = rgOut{ResourceGroup: g, Queued: -1}
		if g.Classifiers == nil {
			out[i].Classifiers = []sr.Classifier{}
		}
		use[g.Name] = &out[i]
	}
	if rs, err := x.cl.Query(x.ctx, "SHOW USAGE RESOURCE GROUPS"); err != nil {
		warns = append(warns, "运行状态未获取（需要 StarRocks 3.1.4 及以上）："+err.Error())
	} else {
		for i := range rs.Data {
			if g := use[rs.Get(i, "Name")]; g != nil {
				g.Running += atoiLoose(rs.Get(i, "BERunningQueries"))
				g.CPUUsed += sr.ParseFloatDefault(rs.Get(i, "BEInUseCpuCores"), 0)
				g.MemUsed += int64(sr.ParseFloatDefault(rs.Get(i, "BEInUseMemBytes"), 0))
			}
		}
	}
	if rs, err := x.cl.Query(x.ctx, "SHOW RUNNING QUERIES"); err == nil {
		byID := map[string]*rgOut{}
		for i := range out {
			out[i].Queued = 0
			byID[out[i].ID] = &out[i]
		}
		for i := range rs.Data {
			if strings.EqualFold(rs.Get(i, "State"), "PENDING") {
				if g := byID[rs.Get(i, "ResourceGroupId")]; g != nil {
					g.Queued++
				}
			}
		}
	}
	ok(w, map[string]any{"groups": out, "warnings": warns})
}

func atoiLoose(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func (s *Server) createRG(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in sr.RGSpec
	if !decode(w, r, &in) {
		return
	}
	if err := in.Validate(true); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	groups, err := x.cl.ResourceGroups(x.ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取资源组失败："+err.Error())
		return
	}
	for _, g := range groups {
		if g.Name == in.Name {
			fail(w, http.StatusConflict, "资源组 "+in.Name+" 已存在")
			return
		}
	}
	if err := s.exec(r, u, x, "rg.create", "新建资源组 "+in.Name, in.CreateSQL()); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) updateRG(w http.ResponseWriter, r *http.Request, u *model.User) {
	name := r.PathValue("name")
	var in sr.RGSpec
	if !decode(w, r, &in) {
		return
	}
	in.Name = name
	if err := in.Validate(false); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	groups, err := x.cl.ResourceGroups(x.ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取资源组失败："+err.Error())
		return
	}
	var old *sr.ResourceGroup
	for i := range groups {
		if groups[i].Name == name {
			old = &groups[i]
		}
	}
	switch {
	case old == nil:
		fail(w, http.StatusNotFound, "资源组不存在")
		return
	case old.Builtin:
		fail(w, http.StatusBadRequest, name+" 是系统内置资源组，不能修改")
		return
	}
	if err := s.exec(r, u, x, "rg.edit", "编辑资源组 "+name, in.AlterSQL(*old)...); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) deleteRG(w http.ResponseWriter, r *http.Request, u *model.User) {
	name := r.PathValue("name")
	if !sr.ValidIdent(name) {
		fail(w, http.StatusBadRequest, "资源组名称不合法")
		return
	}
	if name == "default_wg" || name == "default_mv_wg" {
		fail(w, http.StatusBadRequest, name+" 是系统内置资源组，不能删除")
		return
	}
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	if err := s.exec(r, u, x, "rg.delete", "删除资源组 "+name, "DROP RESOURCE GROUP `"+name+"`"); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---------- 查询队列 ----------

func (s *Server) getQueue(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	items := x.cl.QueueConfig(x.ctx)
	if items == nil {
		items = []sr.QueueItem{}
	}
	pending, running := -1, -1
	if rs, err := x.cl.Query(x.ctx, "SHOW RUNNING QUERIES"); err == nil {
		pending, running = 0, 0
		for i := range rs.Data {
			if strings.EqualFold(rs.Get(i, "State"), "PENDING") {
				pending++
			} else {
				running++
			}
		}
	}
	ok(w, map[string]any{"items": items, "pending": pending, "running": running})
}

func (s *Server) putQueue(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		Values map[string]string `json:"values"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Values) == 0 {
		fail(w, http.StatusBadRequest, "没有要修改的配置")
		return
	}
	known := map[string]struct {
		Bool bool
		Max  int64
	}{}
	for _, k := range sr.QueueKeys {
		known[k.Key] = struct {
			Bool bool
			Max  int64
		}{k.Bool, k.Max}
	}
	for k, v := range in.Values {
		kk, okk := known[k]
		switch {
		case !okk:
			fail(w, http.StatusBadRequest, "不支持修改 "+k)
			return
		case kk.Bool && v != "true" && v != "false":
			fail(w, http.StatusBadRequest, k+" 只能是 true 或 false")
			return
		case !kk.Bool:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 || (kk.Max > 0 && n > kk.Max) {
				fail(w, http.StatusBadRequest, fmt.Sprintf("%s 须为 0–%d 的整数（0 表示不限制）", k, kk.Max))
				return
			}
			if k == "query_queue_mem_used_pct_limit" && n > 100 {
				fail(w, http.StatusBadRequest, k+" 是百分比，不能超过 100")
				return
			}
		}
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	cur := map[string]sr.QueueItem{}
	for _, it := range x.cl.QueueConfig(x.ctx) {
		cur[it.Key] = it
	}
	var stmts, change []string
	var feStmts []string
	for _, k := range sr.QueueKeys { // 按固定顺序，结果可预期
		v, want := in.Values[k.Key]
		if !want {
			continue
		}
		it, has := cur[k.Key]
		if !has {
			fail(w, http.StatusBadRequest, "当前 StarRocks 版本不支持 "+k.Key)
			return
		}
		if it.Value == v {
			continue
		}
		change = append(change, fmt.Sprintf("%s: %s → %s", k.Key, it.Value, v))
		if it.Source == "var" {
			stmts = append(stmts, fmt.Sprintf("SET GLOBAL %s = %s", k.Key, v))
		} else {
			feStmts = append(feStmts, fmt.Sprintf(`ADMIN SET FRONTEND CONFIG ("%s" = "%s")`, k.Key, v))
		}
	}
	if len(change) == 0 {
		ok(w, map[string]bool{"ok": true, "changed": false})
		return
	}
	var err error
	for _, q := range stmts {
		if err = x.cl.Exec(x.ctx, q); err != nil {
			break
		}
	}
	if err == nil {
		for _, q := range feStmts {
			if err = s.onAllFEs(x, q); err != nil {
				break
			}
		}
	}
	s.auditSQL(r, u, "queue.edit", "修改查询队列配置："+strings.Join(change, "；"), strings.Join(append(stmts, feStmts...), ";\n")+";", err, x.c.ID)
	if err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true, "changed": true})
}
