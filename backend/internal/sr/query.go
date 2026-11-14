// @author: kevinkelly
// @license: Apache Licence
// @file: query.go
// @time: 2026/10/28

package sr

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ---------- 运行中的查询 ----------

// RunningQuery 是一条正在执行的查询。
type RunningQuery struct {
	QueryID   string `json:"queryId"`
	FE        string `json:"fe"`
	ConnID    string `json:"connectionId"`
	DB        string `json:"db"`
	User      string `json:"user"`
	Start     string `json:"start"`
	ExecMs    int64  `json:"execMs"`
	ScanBytes string `json:"scanBytes"`
	ScanRows  string `json:"scanRows"`
	CPU       string `json:"cpu"`
	Mem       string `json:"mem"`
	SQL       string `json:"sql"`
	RG        string `json:"resourceGroup"`
}

// CurrentQueries 读取运行中的查询：优先用汇总全部 FE 的 /global_current_queries，不支持时读取本 FE 的 /current_queries。
// global 表示结果已包含所有 FE。
func (c *Client) CurrentQueries(ctx context.Context) (qs []RunningQuery, global bool, err error) {
	r, err := c.Query(ctx, "SHOW PROC '/global_current_queries'")
	if err == nil {
		global = true
	} else if r, err = c.Query(ctx, "SHOW PROC '/current_queries'"); err != nil {
		return nil, false, err
	}
	for i := range r.Data {
		q := RunningQuery{QueryID: r.Get(i, "QueryId"), FE: r.Get(i, "feIp"), ConnID: r.Get(i, "ConnectionId"), DB: r.Get(i, "Database"), User: r.Get(i, "User"),
			Start: r.Get(i, "StartTime"), ExecMs: atoi64S(r.Get(i, "ExecTime")), ScanBytes: r.Get(i, "ScanBytes"), CPU: r.Get(i, "CPUCostSeconds"), Mem: r.Get(i, "MemoryUsageBytes")}
		q.ScanRows = r.Get(i, "ProcessRows")
		if q.ScanRows == "" {
			q.ScanRows = r.Get(i, "ScanRows")
		}
		if q.QueryID != "" {
			qs = append(qs, q)
		}
	}
	return qs, global, nil
}

// ProcessList 读取 SHOW FULL PROCESSLIST，返回 连接 ID → 当前 SQL。
func (c *Client) ProcessList(ctx context.Context) map[string]string {
	out := map[string]string{}
	r, err := c.Query(ctx, "SHOW FULL PROCESSLIST")
	if err != nil {
		return out
	}
	for i := range r.Data {
		if sql := r.Get(i, "Info"); sql != "" {
			out[r.Get(i, "Id")] = sql
		}
	}
	return out
}

var qidRe = regexp.MustCompile(`^[0-9A-Za-z\-]{8,64}$`)

// ValidQueryID 判断是否为合法的 Query ID（UUID 形式）。
func ValidQueryID(s string) bool { return qidRe.MatchString(s) }

// ---------- Profile / 执行计划 ----------

// QueryProfile 读取 Query Profile 文本；analyze 为 true 时同时返回 ANALYZE PROFILE 的分析结果。
func (c *Client) QueryProfile(ctx context.Context, qid string) (profile, analysis string, err error) {
	if !ValidQueryID(qid) {
		return "", "", fmt.Errorf("Query ID 不合法")
	}
	profile, err = c.Scalar(ctx, "SELECT get_query_profile("+MustLit(qid)+")")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(profile) == "" {
		return "", "", fmt.Errorf("没有找到该查询的 Profile：需要先开启 enable_profile（或 enable_query_profile），且 Profile 仍保留在 FE 内存中")
	}
	if r, e := c.Query(ctx, "ANALYZE PROFILE FROM "+MustLit(qid)); e == nil {
		var b strings.Builder
		for _, row := range r.Data {
			b.WriteString(strings.Join(row, "\t"))
			b.WriteByte('\n')
		}
		analysis = b.String()
	}
	return profile, analysis, nil
}

// CheckExplainable 校验可用于 EXPLAIN 的语句：必须是单条 SELECT / WITH 查询。
func CheckExplainable(sql string) (string, error) {
	s := strings.TrimSpace(sql)
	s = strings.TrimRight(s, "; \t\r\n")
	if s == "" {
		return "", fmt.Errorf("请输入要分析的 SQL")
	}
	if len(s) > 20000 {
		return "", fmt.Errorf("SQL 过长（最多 20000 字符）")
	}
	if strings.Contains(s, ";") {
		return "", fmt.Errorf("只能分析单条语句（不能包含分号）")
	}
	low := strings.ToLower(stripLeadingComments(s))
	if !strings.HasPrefix(low, "select") && !strings.HasPrefix(low, "with") && !strings.HasPrefix(low, "(select") {
		return "", fmt.Errorf("只支持分析 SELECT / WITH 查询")
	}
	return s, nil
}

func stripLeadingComments(s string) string {
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--"):
			i := strings.IndexByte(s, '\n')
			if i < 0 {
				return ""
			}
			s = s[i+1:]
		case strings.HasPrefix(s, "/*"):
			i := strings.Index(s, "*/")
			if i < 0 {
				return ""
			}
			s = s[i+2:]
		default:
			return s
		}
	}
}

// Explain 返回 EXPLAIN [COSTS] 的文本。
func (c *Client) Explain(ctx context.Context, sql string, costs bool) (string, error) {
	s, err := CheckExplainable(sql)
	if err != nil {
		return "", err
	}
	prefix := "EXPLAIN "
	if costs {
		prefix = "EXPLAIN COSTS "
	}
	r, err := c.Query(ctx, prefix+s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, row := range r.Data {
		b.WriteString(strings.Join(row, "\t"))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// ---------- 审计表（AuditLoader） ----------

const auditTbl = "starrocks_audit_db__.starrocks_audit_tbl__"

// AuditFilter 是审计表查询条件。
type AuditFilter struct {
	Minutes int    // 最近多少分钟（1–43200）
	MinMs   int64  // 最小耗时（毫秒）
	User    string // 精确匹配
	DB      string
	State   string // OK / EOF / ERR
	Keyword string // SQL 包含
	Limit   int
	Offset  int
}

func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

func (f AuditFilter) where(queryOnly bool) (string, error) {
	if f.Minutes < 1 || f.Minutes > 43200 {
		return "", fmt.Errorf("时间范围须为 1 分钟到 30 天")
	}
	w := []string{fmt.Sprintf("`timestamp` >= DATE_SUB(NOW(), INTERVAL %d MINUTE)", f.Minutes)}
	if queryOnly {
		w = append(w, "isQuery = 1")
	}
	if f.MinMs > 0 {
		w = append(w, fmt.Sprintf("queryTime >= %d", f.MinMs))
	}
	if f.User != "" {
		if !userRe.MatchString(f.User) {
			return "", fmt.Errorf("用户名不合法")
		}
		w = append(w, "user = "+MustLit(f.User))
	}
	if f.DB != "" {
		if !ValidIdent(f.DB) {
			return "", fmt.Errorf("库名不合法")
		}
		w = append(w, "db = "+MustLit(f.DB))
	}
	switch f.State {
	case "":
	case "OK", "EOF", "ERR":
		w = append(w, "state = "+MustLit(f.State))
	default:
		return "", fmt.Errorf("状态只能是 OK / EOF / ERR")
	}
	if f.Keyword != "" {
		if len(f.Keyword) > 200 {
			return "", fmt.Errorf("关键字过长")
		}
		l, err := Lit("%" + likeEscape(f.Keyword) + "%")
		if err != nil {
			return "", err
		}
		w = append(w, "stmt LIKE "+l)
	}
	return strings.Join(w, " AND "), nil
}

// SlowSQL 生成慢查询 Top SQL 的聚合语句（按 SQL 指纹 + 用户聚合；未开启 SQL Digest 的记录不聚合）。
func (f AuditFilter) SlowSQL() (string, error) {
	w, err := f.where(true)
	if err != nil {
		return "", err
	}
	if f.Limit < 1 || f.Limit > 200 {
		f.Limit = 50
	}
	return fmt.Sprintf("SELECT digest, user, MAX_BY(queryId, queryTime) AS qid, MAX_BY(stmt, queryTime) AS stmt, MAX_BY(db, queryTime) AS db, COUNT(*) AS cnt, AVG(queryTime) AS avg_ms, MAX(queryTime) AS max_ms, "+
		"SUM(scanRows) AS scan_rows, MAX(memCostBytes) AS mem_bytes FROM %s WHERE %s GROUP BY digest, user, IF(digest = '', queryId, '') ORDER BY SUM(queryTime) DESC LIMIT %d", auditTbl, w, f.Limit), nil
}

// ListSQL 生成明细查询语句；多取一行用于判断是否还有更多。
func (f AuditFilter) ListSQL() (string, error) {
	w, err := f.where(false)
	if err != nil {
		return "", err
	}
	if f.Limit < 1 || f.Limit > 200 {
		f.Limit = 100
	}
	if f.Offset < 0 || f.Offset > 100000 {
		return "", fmt.Errorf("偏移量不合法")
	}
	return fmt.Sprintf("SELECT queryId, `timestamp`, user, clientIp, db, state, errorCode, queryTime, scanRows, returnRows, memCostBytes, resourceGroup, SUBSTR(stmt, 1, 4000) AS stmt "+
		"FROM %s WHERE %s ORDER BY `timestamp` DESC LIMIT %d OFFSET %d", auditTbl, w, f.Limit+1, f.Offset), nil
}

// SlowQuery 是一条聚合后的慢查询。
type SlowQuery struct {
	Digest   string  `json:"digest"`
	User     string  `json:"user"`
	QueryID  string  `json:"queryId"`
	SQL      string  `json:"sql"`
	DB       string  `json:"db"`
	Count    int64   `json:"count"`
	AvgMs    float64 `json:"avgMs"`
	MaxMs    int64   `json:"maxMs"`
	ScanRows int64   `json:"scanRows"`
	MemBytes int64   `json:"memBytes"`
}

// SlowQueries 执行慢查询聚合。
func (c *Client) SlowQueries(ctx context.Context, f AuditFilter) ([]SlowQuery, string, error) {
	q, err := f.SlowSQL()
	if err != nil {
		return nil, "", err
	}
	r, err := c.Query(ctx, q)
	if err != nil {
		return nil, q, err
	}
	var out []SlowQuery
	for i := range r.Data {
		out = append(out, SlowQuery{Digest: r.Get(i, "digest"), User: r.Get(i, "user"), QueryID: r.Get(i, "qid"), SQL: r.Get(i, "stmt"), DB: r.Get(i, "db"),
			Count: atoi64S(r.Get(i, "cnt")), AvgMs: ParseFloatDefault(r.Get(i, "avg_ms"), 0), MaxMs: atoi64S(r.Get(i, "max_ms")),
			ScanRows: atoi64S(r.Get(i, "scan_rows")), MemBytes: atoi64S(r.Get(i, "mem_bytes"))})
	}
	return out, q, nil
}

// AuditRow 是审计表的一行。
type AuditRow struct {
	QueryID   string `json:"queryId"`
	Time      string `json:"time"`
	User      string `json:"user"`
	ClientIP  string `json:"clientIp"`
	DB        string `json:"db"`
	State     string `json:"state"`
	ErrorCode string `json:"errorCode"`
	Ms        int64  `json:"ms"`
	ScanRows  int64  `json:"scanRows"`
	Rows      int64  `json:"returnRows"`
	Mem       int64  `json:"memBytes"`
	RG        string `json:"resourceGroup"`
	SQL       string `json:"sql"`
}

// AuditRows 读取一页审计记录，more 表示后面还有。
func (c *Client) AuditRows(ctx context.Context, f AuditFilter) (rows []AuditRow, more bool, sql string, err error) {
	sql, err = f.ListSQL()
	if err != nil {
		return nil, false, "", err
	}
	r, err := c.Query(ctx, sql)
	if err != nil {
		return nil, false, sql, err
	}
	limit := f.Limit
	if limit < 1 || limit > 200 {
		limit = 100
	}
	for i := range r.Data {
		rows = append(rows, AuditRow{QueryID: r.Get(i, "queryId"), Time: r.Get(i, "timestamp"), User: r.Get(i, "user"), ClientIP: r.Get(i, "clientIp"), DB: r.Get(i, "db"),
			State: r.Get(i, "state"), ErrorCode: r.Get(i, "errorCode"), Ms: atoi64S(r.Get(i, "queryTime")), ScanRows: atoi64S(r.Get(i, "scanRows")),
			Rows: atoi64S(r.Get(i, "returnRows")), Mem: atoi64S(r.Get(i, "memCostBytes")), RG: r.Get(i, "resourceGroup"), SQL: r.Get(i, "stmt")})
	}
	if len(rows) > limit {
		rows, more = rows[:limit], true
	}
	return rows, more, sql, nil
}

// ---------- SQL 黑名单 ----------

// BlackItem 是一条 SQL 黑名单规则。
type BlackItem struct {
	Index   string `json:"index"`
	Pattern string `json:"pattern"`
}

// Blacklist 读取 SHOW SQLBLACKLIST。
func (c *Client) Blacklist(ctx context.Context) ([]BlackItem, error) {
	r, err := c.Query(ctx, "SHOW SQLBLACKLIST")
	if err != nil {
		return nil, err
	}
	var out []BlackItem
	for i := range r.Data {
		if len(r.Data[i]) >= 2 {
			out = append(out, BlackItem{Index: r.Data[i][0], Pattern: r.Data[i][1]})
		}
	}
	return out, nil
}

// probeSQL 是黑名单规则不应命中的常见语句：命中说明规则过宽，会误伤正常查询。
var probeSQL = []string{"select 1", "show databases", "select current_version()", "select * from t limit 1", "select count(*) from t"}

// CheckBlacklistPattern 校验黑名单正则：长度、换行、是否合法，以及是否会拦截所有查询。
func CheckBlacklistPattern(p string) error {
	switch {
	case strings.TrimSpace(p) == "":
		return fmt.Errorf("请输入正则表达式")
	case len(p) > 1000:
		return fmt.Errorf("正则过长（最多 1000 字符）")
	case strings.ContainsAny(p, "\r\n\x00"):
		return fmt.Errorf("正则不能包含换行")
	}
	re, err := regexp.Compile("(?i)" + p)
	if err != nil {
		return fmt.Errorf("正则不合法：%v", err)
	}
	for _, s := range probeSQL {
		if re.MatchString(s) {
			return fmt.Errorf("该规则会匹配到常见的正常语句（如 %q），生效后会误拦截大量查询，请写得更精确", s)
		}
	}
	return nil
}

// BlacklistIndex 校验黑名单序号。
func BlacklistIndex(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 1000000 {
		return 0, fmt.Errorf("规则序号不合法")
	}
	return n, nil
}
