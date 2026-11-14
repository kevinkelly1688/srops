// @author: kevinkelly
// @license: Apache Licence
// @file: audit.go
// @time: 2026/10/28

package api

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/perm"
	"starrocks-ops/internal/store"
)

type auditOut struct {
	At       time.Time `json:"at"`
	User     string    `json:"user"`
	IP       string    `json:"ip"`
	Cluster  string    `json:"cluster"`
	Menu     string    `json:"menu"`
	Code     string    `json:"code"`
	Act      string    `json:"act"`
	Res      string    `json:"res"`
	ResClass string    `json:"resClass"` // 成功 / 失败 / 拒绝 / 待复核
	SQL      string    `json:"sql"`
}

func toAuditOut(e store.AuditEntry) auditOut {
	return auditOut{At: e.At, User: e.User, IP: e.IP, Cluster: e.Cluster, Menu: perm.MenuOf(e.Code), Code: e.Code, Act: e.Act, Res: e.Result, ResClass: store.ResultClass(e.Result), SQL: e.SQL}
}

// parseTime 接受 RFC3339 或 YYYY-MM-DD（按服务器本地时区的当天 0 点）。
func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02", v, time.Now().Location())
}

// auditFilter 解析查询条件，并按调用者的数据范围限制可见记录：
// 数据范围为“全部集群”的用户可以看到全部记录；范围受限的用户只能看到范围内集群的记录和自己的操作。
func auditFilter(r *http.Request, me string, acc *perm.Access) (store.AuditFilter, error) {
	q := r.URL.Query()
	f := store.AuditFilter{User: q.Get("user"), Cluster: q.Get("cluster"), Code: q.Get("code"), Result: q.Get("result"), Q: strings.TrimSpace(q.Get("q"))}
	var err error
	if f.From, err = parseTime(q.Get("from")); err != nil {
		return f, bad("开始时间格式须为 YYYY-MM-DD")
	}
	if f.To, err = parseTime(q.Get("to")); err != nil {
		return f, bad("结束时间格式须为 YYYY-MM-DD")
	}
	if !f.To.IsZero() && len(q.Get("to")) == 10 {
		f.To = f.To.AddDate(0, 0, 1) // 结束日期包含当天
	}
	if f.Result != "" && f.Result != "成功" && f.Result != "失败" && f.Result != "拒绝" && f.Result != "待复核" {
		return f, bad("结果须为 成功 / 失败 / 拒绝 / 待复核")
	}
	if !acc.AllClusters() {
		f.Visible = func(e store.AuditEntry) bool { return e.User == me || (e.Cluster != "" && acc.Cluster(e.Cluster)) }
	}
	return f, nil
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request, u *model.User) {
	f, err := auditFilter(r, u.Login, accessOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	list, more, err := s.Store.QueryAudit(f, offset, limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]auditOut, 0, len(list))
	for _, e := range list {
		out = append(out, toAuditOut(e))
	}
	ok(w, map[string]any{"items": out, "more": more, "offset": offset, "limit": limit})
}

const exportMax = 50000

// csvCell 防止 CSV 公式注入：以 = + - @ 开头的单元格前加单引号。
func csvCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (s *Server) exportAudit(w http.ResponseWriter, r *http.Request, u *model.User) {
	f, err := auditFilter(r, u.Login, accessOf(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	list, more, err := s.Store.QueryAudit(f, 0, exportMax)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, u, "", "audit.export", fmt.Sprintf("导出审计日志 %d 条", len(list)), nil)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit_`+time.Now().Format("20060102_150405")+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("\xef\xbb\xbf")) // UTF-8 BOM：Excel 直接打开不乱码
	cw := csv.NewWriter(w)
	cw.Write([]string{"时间", "用户", "来源IP", "集群", "菜单", "权限码", "操作", "结果", "执行内容"})
	for _, e := range list {
		o := toAuditOut(e)
		cw.Write([]string{o.At.Format("2006-01-02 15:04:05"), csvCell(o.User), o.IP, csvCell(o.Cluster), o.Menu, o.Code, csvCell(o.Act), csvCell(o.Res), csvCell(o.SQL)})
	}
	if more {
		cw.Write([]string{"", "", "", "", "", "", "（已达到导出上限 " + strconv.Itoa(exportMax) + " 条，请缩小时间范围）"})
	}
	cw.Flush()
}

// StartMaintenance 定期清理超过保留期的审计日志、已处理的复核记录，并让过期未处理的复核申请失效。
func (s *Server) StartMaintenance(ctx context.Context) {
	go s.backupLoop(ctx)
	go s.alertLoop(ctx)
	run := func() {
		days := s.cfg().Basic.AuditDays
		if days < 180 {
			days = 180
		}
		cut := time.Now().AddDate(0, 0, -days)
		if n, err := s.Store.PruneAudit(cut); err != nil {
			logf("清理审计日志失败: %v", err)
		} else if n > 0 {
			logf("已清理 %d 条超过 %d 天的审计日志", n, days)
		}
		s.Store.Update(func(st *store.State) error {
			keep := st.Reviews[:0]
			for _, rv := range st.Reviews {
				if rv.Status == "pending" && time.Since(rv.CreatedAt) > reviewTTL {
					rv.Status, rv.DecidedAt, rv.Note = "expired", time.Now(), "超过 24 小时未处理，已失效"
				}
				if rv.Status == "executing" && time.Since(rv.DecidedAt) > 10*time.Minute {
					rv.Status, rv.Note = "failed", "执行中断（服务重启）"
				}
				if rv.Status != "pending" && rv.CreatedAt.Before(cut) {
					continue
				}
				keep = append(keep, rv)
			}
			st.Reviews = keep
			return nil
		})
	}
	go func() {
		run()
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				run()
			}
		}
	}()
}
