// @author: kevinkelly
// @license: Apache Licence
// @file: query.go
// @time: 2026/10/28

package api

import (
	"encoding/csv"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

// feHost 取 "host:port" 中的 host。
func feHost(key string) string {
	h, _, err := net.SplitHostPort(key)
	if err != nil {
		return key
	}
	return h
}

func (s *Server) listQueries(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 40*time.Second)
	if !ok2 {
		return
	}
	defer done()
	fes, warns, closeAll := s.feClients(x)
	defer closeAll()
	if len(fes) == 0 {
		fes = map[string]*sr.Client{x.cl.Host: x.cl}
	}
	qs, global, err := x.cl.CurrentQueries(x.ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取运行中的查询失败："+err.Error())
		return
	}
	if !global { // 旧版本没有汇总视图：逐台 FE 读取
		qs = nil
		for key, cl := range fes {
			one, _, err := cl.CurrentQueries(x.ctx)
			if err != nil {
				warns = append(warns, "FE "+key+" 读取失败："+err.Error())
				continue
			}
			for i := range one {
				one[i].FE = feHost(key)
			}
			qs = append(qs, one...)
		}
	}
	sqls := map[string]string{} // FE 主机 + 连接 ID → SQL
	for key, cl := range fes {
		for id, q := range cl.ProcessList(x.ctx) {
			sqls[feHost(key)+"/"+id] = q
		}
	}
	rg := map[string]string{}
	if groups, err := x.cl.ResourceGroups(x.ctx); err == nil {
		names := map[string]string{}
		for _, g := range groups {
			names[g.ID] = g.Name
		}
		if rs, err := x.cl.Query(x.ctx, "SHOW RUNNING QUERIES"); err == nil {
			for i := range rs.Data {
				rg[rs.Get(i, "QueryId")] = names[rs.Get(i, "ResourceGroupId")]
			}
		}
	}
	for i := range qs {
		qs[i].SQL = sqls[qs[i].FE+"/"+qs[i].ConnID]
		if len(qs[i].SQL) > 4000 {
			qs[i].SQL = qs[i].SQL[:4000] + " …"
		}
		qs[i].RG = rg[qs[i].QueryID]
	}
	if qs == nil {
		qs = []sr.RunningQuery{}
	}
	ok(w, map[string]any{"items": qs, "warnings": warns, "global": global})
}

// killQuery 终止查询：先重新读取运行中的查询，确认 Query ID 与连接仍然对应，再到所属 FE 上执行 KILL。
func (s *Server) killQuery(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		QueryID string `json:"queryId"`
		FE      string `json:"fe"`
		ConnID  string `json:"connectionId"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !sr.ValidQueryID(in.QueryID) {
		fail(w, http.StatusBadRequest, "Query ID 不合法")
		return
	}
	x, done, ok2 := s.open(w, r, 40*time.Second)
	if !ok2 {
		return
	}
	defer done()
	fes, _, closeAll := s.feClients(x)
	defer closeAll()
	qs, global, err := x.cl.CurrentQueries(x.ctx)
	if err != nil {
		sqlFail(w, err)
		return
	}
	if !global {
		qs = nil
		for key, cl := range fes {
			if one, _, e := cl.CurrentQueries(x.ctx); e == nil {
				for i := range one {
					one[i].FE = feHost(key)
				}
				qs = append(qs, one...)
			}
		}
	}
	var target *sr.RunningQuery
	for i := range qs {
		if qs[i].QueryID == in.QueryID {
			target = &qs[i]
		}
	}
	if target == nil {
		fail(w, http.StatusNotFound, "该查询已经结束或不存在")
		return
	}
	if in.ConnID != "" && target.ConnID != in.ConnID {
		fail(w, http.StatusConflict, "该连接上的查询已经变化，请刷新后重试")
		return
	}
	cl := x.cl
	for key, c := range fes {
		if feHost(key) == target.FE && target.FE != "" {
			cl = c
		}
	}
	x.cl = cl
	stmt := "KILL QUERY " + strconv.Itoa(atoiLoose(target.ConnID))
	if target.ConnID == "" || atoiLoose(target.ConnID) == 0 {
		stmt = "KILL QUERY " + sr.MustLit(target.QueryID)
	}
	if err := s.exec(r, u, x, "query.kill", fmt.Sprintf("终止查询 %s（用户 %s，已运行 %d ms）", target.QueryID, target.User, target.ExecMs), stmt); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) queryProfile(w http.ResponseWriter, r *http.Request, u *model.User) {
	qid := r.PathValue("qid")
	if !sr.ValidQueryID(qid) {
		fail(w, http.StatusBadRequest, "Query ID 不合法")
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	prof, ana, err := x.cl.QueryProfile(x.ctx, qid)
	s.auditSQL(r, u, "query.profile", "查看 Query Profile "+qid, "SELECT get_query_profile('"+qid+"');", err, x.c.ID)
	if err != nil {
		sqlFail(w, err)
		return
	}
	if len(prof) > 2<<20 {
		prof = prof[:2<<20] + "\n…（已截断）"
	}
	ok(w, map[string]any{"profile": prof, "analysis": ana})
}

func (s *Server) explainQuery(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		SQL   string `json:"sql"`
		Costs bool   `json:"costs"`
	}
	if !decode(w, r, &in) {
		return
	}
	if _, err := sr.CheckExplainable(in.SQL); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	text, err := x.cl.Explain(x.ctx, in.SQL, in.Costs)
	mode := map[bool]string{true: "EXPLAIN COSTS", false: "EXPLAIN"}[in.Costs]
	short := strings.Join(strings.Fields(in.SQL), " ")
	if len(short) > 300 {
		short = short[:300] + " …"
	}
	s.auditSQL(r, u, "query.explain", "执行计划分析", mode+" "+short, err, x.c.ID)
	if err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]string{"plan": text})
}

// ---------- 慢查询 / SQL 审计（审计表） ----------

func auditFilterOf(r *http.Request) (sr.AuditFilter, error) {
	q := r.URL.Query()
	num := func(k string, def int64) (int64, error) {
		if q.Get(k) == "" {
			return def, nil
		}
		n, err := strconv.ParseInt(q.Get(k), 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("参数 %s 不合法", k)
		}
		return n, nil
	}
	var f sr.AuditFilter
	var err error
	var n int64
	if n, err = num("minutes", 60); err != nil {
		return f, err
	}
	f.Minutes = int(n)
	if f.MinMs, err = num("minMs", 0); err != nil {
		return f, err
	}
	if n, err = num("limit", 100); err != nil {
		return f, err
	}
	f.Limit = int(n)
	if n, err = num("offset", 0); err != nil {
		return f, err
	}
	f.Offset = int(n)
	f.User, f.DB, f.State, f.Keyword = strings.TrimSpace(q.Get("user")), strings.TrimSpace(q.Get("db")), strings.TrimSpace(q.Get("state")), strings.TrimSpace(q.Get("q"))
	return f, nil
}

const auditHint = "读取审计表失败：需要集群已安装 AuditLoader 插件（写入 starrocks_audit_db__.starrocks_audit_tbl__），且平台账号有读取权限"

func (s *Server) slowQueries(w http.ResponseWriter, r *http.Request, u *model.User) {
	f, err := auditFilterOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if f.MinMs == 0 {
		f.MinMs = 5000
	}
	f.Limit = 50
	if _, err := f.SlowSQL(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	list, _, err := x.cl.SlowQueries(x.ctx, f)
	if err != nil {
		fail(w, http.StatusBadGateway, auditHint+"："+err.Error())
		return
	}
	if list == nil {
		list = []sr.SlowQuery{}
	}
	ok(w, map[string]any{"items": list})
}

func (s *Server) exportSlowQueries(w http.ResponseWriter, r *http.Request, u *model.User) {
	f, err := auditFilterOf(r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if f.MinMs == 0 {
		f.MinMs = 5000
	}
	f.Limit = 200
	if _, err := f.SlowSQL(); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	list, q, err := x.cl.SlowQueries(x.ctx, f)
	s.auditSQL(r, u, "query.export", "导出慢查询 Top SQL", q+";", err, x.c.ID)
	if err != nil {
		fail(w, http.StatusBadGateway, auditHint+"："+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="slow_sql_`+time.Now().Format("20060102_150405")+`.csv"`)
	w.Write([]byte("\xEF\xBB\xBF"))
	cw := csv.NewWriter(w)
	cw.Write([]string{"SQL 指纹", "用户", "库", "次数", "平均耗时(ms)", "最大耗时(ms)", "扫描行数", "峰值内存(bytes)", "代表 Query ID", "SQL"})
	for _, q := range list {
		cw.Write([]string{csvCell(q.Digest), csvCell(q.User), csvCell(q.DB), strconv.FormatInt(q.Count, 10), strconv.FormatFloat(q.AvgMs, 'f', 0, 64), strconv.FormatInt(q.MaxMs, 10),
			strconv.FormatInt(q.ScanRows, 10), strconv.FormatInt(q.MemBytes, 10), q.QueryID, csvCell(q.SQL)})
	}
	cw.Flush()
}

func (s *Server) sqlAudit(w http.ResponseWriter, r *http.Request, u *model.User) {
	f, err := auditFilterOf(r)
	if err == nil {
		_, err = f.ListSQL()
	}
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	rows, more, _, err := x.cl.AuditRows(x.ctx, f)
	if err != nil {
		fail(w, http.StatusBadGateway, auditHint+"："+err.Error())
		return
	}
	if rows == nil {
		rows = []sr.AuditRow{}
	}
	ok(w, map[string]any{"items": rows, "more": more})
}

// ---------- SQL 黑名单 ----------

func (s *Server) listBlacklist(w http.ResponseWriter, r *http.Request, u *model.User) {
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	items, err := x.cl.Blacklist(x.ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取 SQL 黑名单失败："+err.Error())
		return
	}
	if items == nil {
		items = []sr.BlackItem{}
	}
	enabled, _ := x.cl.FEConfig(x.ctx, "enable_sql_blacklist")
	ok(w, map[string]any{"items": items, "enabled": strings.EqualFold(enabled, "true"), "known": enabled != ""})
}

// changeBlacklist 同时处理添加（POST）与删除（DELETE）：二者共用权限码 query.blacklist，复核后按原请求方法执行。
func (s *Server) changeBlacklist(w http.ResponseWriter, r *http.Request, u *model.User) {
	var stmt, act string
	if r.Method == http.MethodDelete {
		n, err := sr.BlacklistIndex(r.PathValue("idx"))
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		stmt, act = fmt.Sprintf("DELETE SQLBLACKLIST %d", n), fmt.Sprintf("删除 SQL 黑名单规则 #%d", n)
	} else {
		var in struct {
			Pattern string `json:"pattern"`
		}
		if !decode(w, r, &in) {
			return
		}
		if err := sr.CheckBlacklistPattern(in.Pattern); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		lit, err := sr.Lit(in.Pattern)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		stmt, act = "ADD SQLBLACKLIST "+lit, "添加 SQL 黑名单规则 "+in.Pattern
	}
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	if err := s.exec(r, u, x, "query.blacklist", act, stmt); err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}
