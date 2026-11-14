// @author: kevinkelly
// @license: Apache Licence
// @file: sim_query.go
// @time: 2026/10/28

package fakesr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-mysql-org/go-mysql/mysql"
)

type fakeQ struct {
	qid, conn, db, user, start string
	ms                         int64
	scan, rows, cpu, mem, sql  string
}

func (s *sim) initQuery(problems bool) {
	s.run = []*fakeQ{
		{"7f1c2a90-aaaa-4b1e-9d11-000000000001", "1001", "dws", "bi_user", "2026-10-08 22:00:01", 3200, "1.2 GB", "12000000", "3.4", "820 MB", "SELECT shop_id, SUM(amount) FROM dws.fact_order WHERE dt = '2026-10-07' GROUP BY shop_id"},
		{"7f1c2a90-aaaa-4b1e-9d11-000000000002", "1002", "ads", "etl_user", "2026-10-08 21:59:40", 25000, "40 GB", "880000000", "120.5", "6.1 GB", "INSERT INTO ads.ads_gmv_daily SELECT dt, SUM(amount) FROM dw.dwd_order_detail GROUP BY dt"},
	}
	if problems {
		s.run = append(s.run, &fakeQ{"7f1c2a90-aaaa-4b1e-9d11-000000000003", "1003", "dws", "adhoc", "2026-10-08 21:50:00", 612000, "820 GB", "32000000000", "9800.2", "62 GB",
			"SELECT * FROM dws.fact_order o JOIN dim.dim_sku s ON o.sku_id = s.id WHERE s.cate = 'x'"})
	}
}

var (
	reKillConn = regexp.MustCompile(`(?is)^kill query (\d+)$`)
	reKillQid  = regexp.MustCompile(`(?is)^kill query ["']([0-9a-z\-]+)["']$`)
	reQProfile = regexp.MustCompile(`(?is)^select get_query_profile\(["']([0-9a-z\-]+)["']\)$`)
	reAnalyze  = regexp.MustCompile(`(?is)^analyze profile from ["']([0-9a-z\-]+)["']$`)
	reExplain  = regexp.MustCompile(`(?is)^explain( costs| verbose)? (.+)$`)
	reBlackAdd = regexp.MustCompile(`(?is)^add sqlblacklist ["'](.+)["']$`)
	reBlackDel = regexp.MustCompile(`(?is)^delete sqlblacklist (\d+)$`)
	reLimitOff = regexp.MustCompile(`limit (\d+) offset (\d+)`)
	reLikeLit  = regexp.MustCompile(`(?i)stmt like "((?:[^"\\]|\\.)*)"`)
	reMinMs    = regexp.MustCompile(`querytime >= (\d+)`)
)

func reEqLit(col string) *regexp.Regexp { return regexp.MustCompile(col + ` = "([^"]*)"`) }

const knownProfileQID = "7f1c2a90-aaaa-4b1e-9d11-0000000000ff" // 已结束但 Profile 仍保留的查询

func (c *Cluster) legacyProc() bool {
	v := c.cfg.Version
	return strings.HasPrefix(v, "2.") || strings.HasPrefix(v, "3.0") || strings.HasPrefix(v, "3.1")
}

// handleQuery 处理查询分析相关语句。
func (c *Cluster) handleQuery(q, l string) (*mysql.Result, bool, error) {
	s := c.sim
	switch {
	case l == "show proc '/global_current_queries'":
		if c.legacyProc() {
			return nil, true, sqlErr("Unknown proc node path: global_current_queries")
		}
		return c.currentQueries(true)
	case l == "show proc '/current_queries'":
		return c.currentQueries(false)
	case l == "show full processlist" || l == "show processlist":
		s.mu.Lock()
		defer s.mu.Unlock()
		var rows [][]any
		for _, r := range s.run {
			rows = append(rows, []any{r.conn, r.user, "10.1.1.9:51" + r.conn[len(r.conn)-3:], r.db, "Query", r.start, fmt.Sprint(r.ms / 1000), "", r.sql, "false", "default_warehouse"})
		}
		res, err := resultset([]string{"Id", "User", "Host", "Db", "Command", "ConnectionStartTime", "Time", "State", "Info", "IsPending", "Warehouse"}, rows)
		return res, true, err
	case reKillConn.MatchString(q) || reKillQid.MatchString(q):
		s.mu.Lock()
		defer s.mu.Unlock()
		conn, qid := "", ""
		if m := reKillConn.FindStringSubmatch(q); m != nil {
			conn = m[1]
		} else {
			qid = reKillQid.FindStringSubmatch(q)[1]
		}
		for i, r := range s.run {
			if (conn != "" && r.conn == conn) || (qid != "" && r.qid == qid) {
				s.run = append(s.run[:i], s.run[i+1:]...)
				return okResult(), true, nil
			}
		}
		return nil, true, sqlErr("Unknown thread id: %s%s", conn, qid)
	case reQProfile.MatchString(q):
		qid := reQProfile.FindStringSubmatch(q)[1]
		known := qid == knownProfileQID || c.hasRunning(qid)
		text := ""
		if known {
			text = "Query:\n  Summary:\n     - Query ID: " + qid + "\n     - QueryAllocatedMemoryUsage: 6.1 GB\n     - QueryPeakMemoryUsagePerNode: 2.3 GB\n     - QueryExecutionWallTime: 25s\n  Planner:\n     - Total: 120ms\n  Execution:\n    Fragment 0:\n      Pipeline (id=0):\n        OLAP_SCAN (plan_node_id=0): 18s  scan rows 880000000\n        HASH_JOIN (plan_node_id=3): 5.2s\n"
		}
		res, err := resultset([]string{"get_query_profile('" + qid + "')"}, [][]any{{text}})
		return res, true, err
	case reAnalyze.MatchString(q):
		qid := reAnalyze.FindStringSubmatch(q)[1]
		if qid != knownProfileQID && !c.hasRunning(qid) {
			return nil, true, sqlErr("Profile of query %s not found", qid)
		}
		res, err := resultset([]string{"Explain String"}, [][]any{{"Summary"}, {"Fragment 0: OLAP_SCAN consumes 72% of total time (scan rows too many)"}})
		return res, true, err
	case reExplain.MatchString(q):
		m := reExplain.FindStringSubmatch(q)
		if strings.Contains(strings.ToLower(m[2]), "no_such_table") {
			return nil, true, sqlErr("Getting analyzing error. Detail message: Unknown table 'no_such_table'")
		}
		lines := []string{"PLAN FRAGMENT 0", " OUTPUT EXPRS:1: shop_id | 2: sum", "  RESULT SINK", "  1:AGGREGATE (update finalize)", "  0:OlapScanNode", "     TABLE: fact_order", "     partitions=1/280"}
		if strings.TrimSpace(m[1]) != "" {
			lines = append(lines, "     cardinality=320000000", "     avgRowSize=24.0")
		}
		rows := make([][]any, len(lines))
		for i, ln := range lines {
			rows[i] = []any{ln}
		}
		res, err := resultset([]string{"Explain String"}, rows)
		return res, true, err
	case l == "show sqlblacklist":
		s.mu.Lock()
		defer s.mu.Unlock()
		var rows [][]any
		for i, p := range s.black {
			rows = append(rows, []any{strconv.Itoa(i + 1), p})
		}
		res, err := resultset([]string{"Index", "Forbidden SQL"}, rows)
		return res, true, err
	case reBlackAdd.MatchString(q):
		s.mu.Lock()
		defer s.mu.Unlock()
		s.black = append(s.black, unescapeLit(reBlackAdd.FindStringSubmatch(q)[1]))
		return okResult(), true, nil
	case reBlackDel.MatchString(q):
		s.mu.Lock()
		defer s.mu.Unlock()
		n, _ := strconv.Atoi(reBlackDel.FindStringSubmatch(q)[1])
		if n < 1 || n > len(s.black) {
			return nil, true, sqlErr("No such sql blacklist index: %d", n)
		}
		s.black = append(s.black[:n-1], s.black[n:]...)
		return okResult(), true, nil
	}
	return nil, false, nil
}

func (c *Cluster) hasRunning(qid string) bool {
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	for _, r := range c.sim.run {
		if r.qid == qid {
			return true
		}
	}
	return false
}

func (c *Cluster) currentQueries(global bool) (*mysql.Result, bool, error) {
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	cols := []string{"StartTime", "QueryId", "ConnectionId", "Database", "User", "ScanBytes", "ProcessRows", "CPUCostSeconds", "MemoryUsageBytes", "ExecTime"}
	if global {
		cols = []string{"StartTime", "feIp", "QueryId", "ConnectionId", "Database", "User", "ScanBytes", "ProcessRows", "CPUCostSeconds", "MemoryUsageBytes", "ExecTime"}
	}
	var rows [][]any
	for _, r := range c.sim.run {
		row := []any{r.start, r.qid, r.conn, r.db, r.user, r.scan, r.rows, r.cpu, r.mem, fmt.Sprint(r.ms)}
		if global {
			row = []any{r.start, c.nodesOf(kindFE)[0].ip, r.qid, r.conn, r.db, r.user, r.scan, r.rows, r.cpu, r.mem, fmt.Sprint(r.ms)}
		}
		rows = append(rows, row)
	}
	res, err := resultset(cols, rows)
	return res, true, err
}

type auditRec struct {
	qid, ts, user, ip, db, state, err string
	ms, scan, ret, mem                int64
	rg, stmt, digest                  string
}

var auditData = []auditRec{
	{"a0000001", "2026-10-08 21:58:11", "bi_user", "10.1.1.9", "dws", "EOF", "0", 8200, 120000000, 100, 2147483648, "rg_bi", "SELECT shop_id, SUM(amount) FROM dws.fact_order GROUP BY shop_id", "d-8f21"},
	{"a0000002", "2026-10-08 21:50:02", "adhoc", "10.1.2.31", "dws", "EOF", "0", 612000, 32000000000, 10, 66571993088, "rg_adhoc", "SELECT * FROM dws.fact_order o JOIN dim.dim_sku s ON o.sku_id = s.id", "d-c3a9"},
	{"a0000003", "2026-10-08 21:40:19", "etl_user", "10.1.3.5", "ads", "OK", "0", 25000, 880000000, 0, 6535000000, "rg_etl", "INSERT INTO ads.ads_gmv_daily SELECT dt, SUM(amount) FROM dw.dwd_order_detail GROUP BY dt", "d-e517"},
	{"a0000004", "2026-10-08 21:31:45", "bi_user", "10.1.1.9", "dws", "ERR", "1064", 120, 0, 0, 0, "rg_bi", "SELECT x FROM dws.no_such_table", "d-0b44"},
	{"a0000005", "2026-10-08 21:20:00", "adhoc", "10.1.2.31", "dws", "EOF", "0", 7100, 420000000, 50, 3221225472, "rg_adhoc", "SELECT * FROM dws.fact_order o JOIN dim.dim_sku s ON o.sku_id = s.id", "d-c3a9"},
	{"a0000006", "2026-10-08 21:10:30", "dev_zhao", "10.2.0.8", "ods", "EOF", "0", 300, 1000, 10, 1048576, "default_wg", "SELECT * FROM ods.ods_order_log LIMIT 10", "d-77aa"},
	{"a0000007", "2026-10-08 21:00:00", "bi_user", "10.1.1.9", "dws", "EOF", "0", 5400, 90000000, 100, 1073741824, "rg_bi", "SELECT shop_id, SUM(amount) FROM dws.fact_order GROUP BY shop_id", "d-8f21"},
}

// auditSim 处理审计表查询：条数、慢查询聚合、明细（支持用户 / 库 / 状态 / 关键字 / 耗时过滤与分页）。
func (c *Cluster) auditSim(l, q string) (*mysql.Result, error) {
	userF, dbF, stateF := reEqLit("user").FindStringSubmatch(q), reEqLit("db").FindStringSubmatch(q), reEqLit("state").FindStringSubmatch(q)
	kw := reLikeLit.FindStringSubmatch(q)
	minMs := int64(0)
	if m := reMinMs.FindStringSubmatch(l); m != nil {
		minMs, _ = strconv.ParseInt(m[1], 10, 64)
	}
	rows := []auditRec{}
	for _, a := range auditData {
		switch {
		case userF != nil && a.user != userF[1], dbF != nil && a.db != dbF[1], stateF != nil && a.state != stateF[1], a.ms < minMs:
			continue
		case kw != nil && !likeMatchEsc(kw[1], a.stmt):
			continue
		}
		rows = append(rows, a)
	}
	switch {
	case strings.Contains(l, "group by digest"):
		type agg struct {
			a                   auditRec
			n, total, max, scan int64
			mem                 int64
		}
		var order []string
		m := map[string]*agg{}
		for _, a := range rows {
			g := m[a.digest+a.user]
			if g == nil {
				g = &agg{a: a}
				m[a.digest+a.user] = g
				order = append(order, a.digest+a.user)
			}
			g.n++
			g.total += a.ms
			g.scan += a.scan
			if a.ms > g.max {
				g.max, g.a = a.ms, a
			}
			if a.mem > g.mem {
				g.mem = a.mem
			}
		}
		// 按总耗时降序
		for i := 0; i < len(order); i++ {
			for j := i + 1; j < len(order); j++ {
				if m[order[j]].total > m[order[i]].total {
					order[i], order[j] = order[j], order[i]
				}
			}
		}
		var out [][]any
		for _, k := range order {
			g := m[k]
			out = append(out, []any{g.a.digest, g.a.user, g.a.qid, g.a.stmt, g.a.db, g.n, float64(g.total) / float64(g.n), g.max, g.scan, g.mem})
		}
		return resultset([]string{"digest", "user", "qid", "stmt", "db", "cnt", "avg_ms", "max_ms", "scan_rows", "mem_bytes"}, out)
	case strings.HasPrefix(l, "select count("):
		return countResult(int64(pick(c.problems, 143, 12)))
	}
	off, lim := 0, len(rows)
	if m := reLimitOff.FindStringSubmatch(l); m != nil {
		lim, _ = strconv.Atoi(m[1])
		off, _ = strconv.Atoi(m[2])
	}
	var out [][]any
	for i := off; i < len(rows) && i < off+lim; i++ {
		a := rows[i]
		out = append(out, []any{a.qid, a.ts, a.user, a.ip, a.db, a.state, a.err, a.ms, a.scan, a.ret, a.mem, a.rg, a.stmt})
	}
	return resultset([]string{"queryId", "timestamp", "user", "clientIp", "db", "state", "errorCode", "queryTime", "scanRows", "returnRows", "memCostBytes", "resourceGroup", "stmt"}, out)
}

// unescapeLit 还原 SQL 字符串字面量里的转义：反斜杠加任意字符 → 该字符（与 StarRocks 解析 "a\\b" 得到 a\b 一致）。
func unescapeLit(lit string) string {
	var b strings.Builder
	rs := []rune(lit)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) {
			i++
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

// likeMatchEsc 实现 SQL 字符串字面量中的 LIKE：先还原字面量转义（双反斜杠 → 单反斜杠，反斜杠加引号 → 引号），再把「反斜杠 + % 或 _」当作字面字符。
func likeMatchEsc(lit, s string) bool {
	var raw []rune
	rs := []rune(lit)
	for i := 0; i < len(rs); i++ {
		if rs[i] == '\\' && i+1 < len(rs) {
			i++
		}
		raw = append(raw, rs[i])
	}
	var b strings.Builder
	b.WriteString("(?is)^")
	for i := 0; i < len(raw); i++ {
		switch r := raw[i]; {
		case r == '\\' && i+1 < len(raw):
			i++
			b.WriteString(regexp.QuoteMeta(string(raw[i])))
		case r == '%':
			b.WriteString(".*")
		case r == '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(s)
}
