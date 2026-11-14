// @author: kevinkelly
// @license: Apache Licence
// @file: sim.go
// @time: 2026/10/28

package fakesr

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
)

// sim 保存假集群中可被修改的状态：资源组、变量、FE 配置、导入作业、物化视图等。
type sim struct {
	mu     sync.Mutex
	nextID int
	rgs    []*fakeRG
	vars   []*kv
	feCfg  []*kv
	rls    []*fakeRL
	mvs    []*fakeMV
	run    []*fakeQ
	black  []string
	ops    *opsSim
}

type kv struct {
	k, v, typ string
	mutable   bool
	def       string
}

type fakeCls struct {
	id    string
	attrs []string // 已按显示格式排好，如 user=etl_user
}

type fakeRG struct {
	name, id string
	props    map[string]string
	cls      []fakeCls
	builtin  bool
}

type fakeRL struct {
	db, name, table, state, reason string
	errRows, total, loaded         int64
	pausedAt                       string
}

type fakeMV struct {
	db, name, refresh, partition string
	active                       bool
	inactiveReason               string
	state, errMsg                string
	startedAt                    time.Time
	finishedAt                   string
	rows                         int64
	def                          string
}

func newSim(problems bool) *sim {
	s := &sim{nextID: 10100}
	bigCPU, bigScan, bigMem := "0", "0", "0"
	if !problems {
		bigCPU, bigScan, bigMem = "300", "1000000000", "10737418240"
	}
	add := func(name, id, cpu, mem, conc, cls string, big bool) {
		g := &fakeRG{name: name, id: id, props: map[string]string{"cpu_weight": cpu, "exclusive_cpu_cores": "0", "mem_limit": mem, "concurrency_limit": conc,
			"big_query_cpu_second_limit": "0", "big_query_scan_rows_limit": "0", "big_query_mem_limit": "0", "spill_mem_limit_threshold": "100%"}}
		if big {
			g.props["big_query_cpu_second_limit"], g.props["big_query_scan_rows_limit"], g.props["big_query_mem_limit"] = bigCPU, bigScan, bigMem
		}
		g.cls = []fakeCls{{id: fmt.Sprint(atoiF(id) + 1), attrs: []string{cls}}}
		if name == "rg_adhoc" {
			g.props["spill_mem_limit_threshold"] = "80%"
		}
		s.rgs = append(s.rgs, g)
	}
	add("rg_etl", "10011", "8", "40.0%", "8", "user=etl_user", false)
	add("rg_bi", "10021", "16", "50.0%", "32", "role=bi_role", false)
	add("rg_adhoc", "10031", "4", "20.0%", "16", "user=adhoc", true)

	s.vars = []*kv{
		{k: "enable_query_queue_select", v: pickS(problems, "false", "true"), typ: "bool", mutable: true, def: "false"},
		{k: "enable_query_queue_statistic", v: "false", typ: "bool", mutable: true, def: "false"},
		{k: "enable_query_queue_load", v: "false", typ: "bool", mutable: true, def: "false"},
		{k: "query_queue_concurrency_limit", v: "0", typ: "int", mutable: true, def: "0"},
		{k: "query_timeout", v: "300", typ: "int", mutable: true, def: "300"},
		{k: "pipeline_dop", v: "0", typ: "int", mutable: true, def: "0"},
		{k: "enable_profile", v: "false", typ: "bool", mutable: true, def: "false"},
		{k: "time_zone", v: "Asia/Shanghai", typ: "string", mutable: true, def: "Asia/Shanghai"},
		{k: "sql_mode", v: "ONLY_FULL_GROUP_BY", typ: "string", mutable: true, def: "ONLY_FULL_GROUP_BY"},
	}
	s.feCfg = []*kv{
		{k: "run_mode", v: "shared_nothing", typ: "String"},
		{k: "default_replication_num", v: "3", typ: "short", mutable: true},
		{k: "qe_slow_log_ms", v: "5000", typ: "long", mutable: true},
		{k: "query_queue_mem_used_pct_limit", v: "0", typ: "int", mutable: true},
		{k: "query_queue_max_queued_queries", v: "1024", typ: "int", mutable: true},
		{k: "query_queue_pending_timeout_second", v: "300", typ: "int", mutable: true},
		{k: "enable_sql_blacklist", v: "true", typ: "boolean", mutable: true},
		{k: "tablet_sched_disable_balance", v: "false", typ: "boolean", mutable: true},
		{k: "http_port", v: "8030", typ: "int"},
		{k: "edit_log_port", v: "9010", typ: "int"},
		{k: "tablet_sched_max_scheduling_tablets", v: "10000", typ: "long", mutable: true},
	}
	s.rls = []*fakeRL{
		{db: "ods", name: "rl_orders", table: "ods_order_log", state: "RUNNING", total: 482000000, loaded: 481950000, errRows: 12000},
		{db: "ods", name: "rl_events", table: "ods_event_log", state: "RUNNING", total: 1200000000, loaded: 1199000000, errRows: 4000},
		{db: "ods", name: "rl_payments", table: "ods_payment", state: "RUNNING", total: 86000000, loaded: 86000000},
		{db: "ods", name: "rl_clicks", table: "ods_click_log", state: "RUNNING", total: 2300000000, loaded: 2290000000, errRows: 9000000},
	}
	if problems {
		s.rls[1].state, s.rls[1].reason, s.rls[1].pausedAt = "PAUSED", "offset out of range: partition 7", "2026-10-08 09:12:44"
	}
	s.mvs = []*fakeMV{
		{db: "ads", name: "mv_sales_daily", refresh: "ASYNC", partition: "RANGE", active: true, state: "SUCCESS", finishedAt: "2026-10-08 08:10:12", rows: 1840000, def: "SELECT dt, shop_id, SUM(amount) FROM dw.dwd_order_detail GROUP BY dt, shop_id"},
		{db: "ads", name: "mv_user_retention", refresh: "ASYNC", partition: "UNPARTITIONED", active: true, state: "SUCCESS", finishedAt: "2026-10-08 08:10:40", rows: 920000, def: "SELECT uid, COUNT(DISTINCT dt) FROM dw.dwd_user_event GROUP BY uid"},
		{db: "dw", name: "mv_order_wide", refresh: "ASYNC", partition: "RANGE", active: true, state: "SUCCESS", finishedAt: "2026-10-08 08:11:02", rows: 52000000, def: "SELECT o.*, u.name FROM dw.dwd_order_detail o JOIN dim.dim_user u ON o.uid = u.uid"},
		{db: "ads", name: "mv_gmv_hourly", refresh: "ASYNC", partition: "UNPARTITIONED", active: true, state: "SUCCESS", finishedAt: "2026-10-08 09:00:05", rows: 8760, def: "SELECT hour(ts) h, SUM(amount) FROM dw.dwd_payment GROUP BY h"},
	}
	if problems {
		s.mvs[1].state, s.mvs[1].errMsg = "FAILED", "Memory of query exceed limit"
		s.mvs[2].active, s.mvs[2].inactiveReason = false, "base-table dw.dwd_order_detail schema changed"
	}
	s.initQuery(problems)
	return s
}

func pickS(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

func atoiF(s string) int { n, _ := strconv.Atoi(s); return n }

func sqlErr(format string, a ...any) error {
	return mysql.NewError(mysql.ER_UNKNOWN_ERROR, fmt.Sprintf(format, a...))
}

var (
	reRGCreate = regexp.MustCompile(`(?is)^create resource group (\w+) to (.*) with \((.*)\)$`)
	reRGAlter  = regexp.MustCompile(`(?is)^alter resource group (\w+) (.*)$`)
	reRGDrop   = regexp.MustCompile(`(?is)^drop resource group (\w+)$`)
	reProp     = regexp.MustCompile(`["']([a-z_]+)["']\s*=\s*["']([^"']*)["']`)
	reSetG     = regexp.MustCompile(`(?is)^set global (\w+)\s*=\s*(.+)$`)
	reAdminSet = regexp.MustCompile(`(?is)^admin set frontend config \(\s*["']([a-z_]+)["']\s*=\s*["']([^"']*)["']\s*\)$`)
	reRL       = regexp.MustCompile(`(?is)^(pause|resume|stop) routine load for (\w+)\.(\w+)$`)
	reRLTask   = regexp.MustCompile(`(?is)^show routine load task from (\w+) where jobname\s*=\s*["'](\w+)["']$`)
	reMVRef    = regexp.MustCompile(`(?is)^refresh materialized view (\w+)\.(\w+)(.*)$`)
	reMVAlter  = regexp.MustCompile(`(?is)^alter materialized view (\w+)\.(\w+) (active|inactive)$`)
	reMVCancel = regexp.MustCompile(`(?is)^cancel refresh materialized view (\w+)\.(\w+)$`)
	reClsGroup = regexp.MustCompile(`\((?:[^()]|\([^()]*\))*\)`)
)

// parseCls 解析 "(user="a", query_type in ("select"))" 为显示用属性。
func parseCls(g string) []string {
	g = strings.TrimSpace(g[1 : len(g)-1])
	var out []string
	depth, start := 0, 0
	parts := []string{}
	for i, r := range g {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, g[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, g[start:])
	for _, p := range parts {
		p = strings.TrimSpace(p)
		low := strings.ToLower(p)
		if strings.HasPrefix(low, "query_type in") {
			in := p[strings.Index(p, "(")+1:]
			in = strings.TrimSuffix(strings.TrimSpace(in), ")")
			var qs []string
			for _, q := range strings.Split(in, ",") {
				qs = append(qs, strings.ToUpper(strings.Trim(strings.TrimSpace(q), `"'`)))
			}
			out = append(out, "query_type in ("+strings.Join(qs, ",")+")")
			continue
		}
		k, v, ok := strings.Cut(p, "=")
		if ok {
			out = append(out, strings.ToLower(strings.TrimSpace(k))+"="+strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
	return out
}

func (s *sim) newID() string { s.nextID += 10; return strconv.Itoa(s.nextID) }

func (g *fakeRG) show() []any {
	p := func(k string) string { return g.props[k] }
	var lines []string
	for _, c := range g.cls {
		lines = append(lines, "(id="+c.id+", weight=1.0, "+strings.Join(c.attrs, ", ")+")")
	}
	return []any{g.name, g.id, p("cpu_weight"), p("exclusive_cpu_cores"), p("mem_limit"), p("big_query_cpu_second_limit"), p("big_query_scan_rows_limit"),
		p("big_query_mem_limit"), p("concurrency_limit"), p("spill_mem_limit_threshold"), "normal", strings.Join(lines, "\n")}
}

func (c *Cluster) showResourceGroupsSim(all bool) (*mysql.Result, error) {
	cols := []string{"name", "id", "cpu_weight", "exclusive_cpu_cores", "mem_limit", "big_query_cpu_second_limit",
		"big_query_scan_rows_limit", "big_query_mem_limit", "concurrency_limit", "spill_mem_limit_threshold", "type", "classifiers"}
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	var rows [][]any
	for _, g := range c.sim.rgs {
		rows = append(rows, g.show())
	}
	if all {
		def := &fakeRG{name: "default_wg", id: "0", props: map[string]string{"cpu_weight": "1", "exclusive_cpu_cores": "0", "mem_limit": "100.0%", "concurrency_limit": "0",
			"big_query_cpu_second_limit": "0", "big_query_scan_rows_limit": "0", "big_query_mem_limit": "0", "spill_mem_limit_threshold": "100%"}}
		rows = append(rows, def.show())
	}
	return resultset(cols, rows)
}

func (s *sim) rg(name string) *fakeRG {
	for _, g := range s.rgs {
		if g.name == name {
			return g
		}
	}
	return nil
}

// handleDDL 处理会修改状态的语句；handled 为 false 表示不是这类语句。
func (c *Cluster) handleDDL(q string) (res *mysql.Result, handled bool, err error) {
	s := c.sim
	l := strings.ToLower(q)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.HasPrefix(l, "create resource group"):
		m := reRGCreate.FindStringSubmatch(q)
		if m == nil {
			return nil, true, mysql.NewError(mysql.ER_PARSE_ERROR, "fake starrocks: bad CREATE RESOURCE GROUP: "+q)
		}
		if s.rg(m[1]) != nil {
			return nil, true, sqlErr("Resource group %s already exists", m[1])
		}
		g := &fakeRG{name: m[1], id: s.newID(), props: map[string]string{"cpu_weight": "0", "exclusive_cpu_cores": "0", "mem_limit": "0", "concurrency_limit": "0",
			"big_query_cpu_second_limit": "0", "big_query_scan_rows_limit": "0", "big_query_mem_limit": "0", "spill_mem_limit_threshold": "100%"}}
		for _, pm := range reProp.FindAllStringSubmatch(m[3], -1) {
			g.props[pm[1]] = pm[2]
		}
		if g.props["cpu_weight"] == "0" && g.props["exclusive_cpu_cores"] == "0" {
			return nil, true, sqlErr("cpu_weight or exclusive_cpu_cores must be set")
		}
		if g.props["mem_limit"] != "0" && !strings.HasSuffix(g.props["mem_limit"], "%") {
			g.props["mem_limit"] += ".0%"
		}
		for _, cg := range reClsGroup.FindAllString(m[2], -1) {
			g.cls = append(g.cls, fakeCls{id: s.newID(), attrs: parseCls(cg)})
		}
		s.rgs = append(s.rgs, g)
		return okResult(), true, nil
	case strings.HasPrefix(l, "drop resource group"):
		m := reRGDrop.FindStringSubmatch(q)
		if m == nil {
			return nil, true, mysql.NewError(mysql.ER_PARSE_ERROR, "bad DROP RESOURCE GROUP")
		}
		for i, g := range s.rgs {
			if g.name == m[1] {
				s.rgs = append(s.rgs[:i], s.rgs[i+1:]...)
				return okResult(), true, nil
			}
		}
		return nil, true, sqlErr("Resource group %s does not exist", m[1])
	case strings.HasPrefix(l, "alter resource group"):
		m := reRGAlter.FindStringSubmatch(q)
		if m == nil {
			return nil, true, mysql.NewError(mysql.ER_PARSE_ERROR, "bad ALTER RESOURCE GROUP")
		}
		g := s.rg(m[1])
		if g == nil {
			return nil, true, sqlErr("Resource group %s does not exist", m[1])
		}
		rest := strings.TrimSpace(m[2])
		rl := strings.ToLower(rest)
		switch {
		case strings.HasPrefix(rl, "with"):
			for _, pm := range reProp.FindAllStringSubmatch(rest, -1) {
				v := pm[2]
				if pm[1] == "mem_limit" && v != "0" && !strings.HasSuffix(v, "%") {
					v += ".0%"
				}
				g.props[pm[1]] = v
			}
		case strings.HasPrefix(rl, "add"):
			for _, cg := range reClsGroup.FindAllString(rest[3:], -1) {
				g.cls = append(g.cls, fakeCls{id: s.newID(), attrs: parseCls(cg)})
			}
		case strings.HasPrefix(rl, "drop all"):
			g.cls = nil
		case strings.HasPrefix(rl, "drop"):
			ids := map[string]bool{}
			for _, id := range strings.Split(strings.Trim(strings.TrimSpace(rest[4:]), "() "), ",") {
				ids[strings.TrimSpace(id)] = true
			}
			var keep []fakeCls
			for _, c := range g.cls {
				if !ids[c.id] {
					keep = append(keep, c)
				}
			}
			g.cls = keep
		default:
			return nil, true, mysql.NewError(mysql.ER_PARSE_ERROR, "bad ALTER RESOURCE GROUP: "+q)
		}
		return okResult(), true, nil
	case strings.HasPrefix(l, "set global"):
		m := reSetG.FindStringSubmatch(q)
		if m == nil {
			return okResult(), true, nil
		}
		for _, v := range s.vars {
			if v.k == strings.ToLower(m[1]) {
				v.v = strings.Trim(strings.TrimSpace(m[2]), `"'`)
				return okResult(), true, nil
			}
		}
		return nil, true, sqlErr("Unknown system variable '%s'", m[1])
	case strings.HasPrefix(l, "admin set frontend config"):
		m := reAdminSet.FindStringSubmatch(q)
		if m == nil {
			return nil, true, mysql.NewError(mysql.ER_PARSE_ERROR, "bad ADMIN SET FRONTEND CONFIG")
		}
		for _, v := range s.feCfg {
			if v.k == m[1] {
				if !v.mutable {
					return nil, true, sqlErr("Config '%s' is not mutable", m[1])
				}
				v.v = m[2]
				return okResult(), true, nil
			}
		}
		return nil, true, sqlErr("Config '%s' does not exist or is not mutable", m[1])
	case reRL.MatchString(q):
		m := reRL.FindStringSubmatch(q)
		op := strings.ToLower(m[1])
		for i, j := range s.rls {
			if j.db != m[2] || j.name != m[3] {
				continue
			}
			switch op {
			case "pause":
				if j.state != "RUNNING" {
					return nil, true, sqlErr("Job %s is not in RUNNING state (now %s)", j.name, j.state)
				}
				j.state, j.pausedAt, j.reason = "PAUSED", time.Now().Format(tsLayout), "User root pauses routine load job"
			case "resume":
				if j.state != "PAUSED" {
					return nil, true, sqlErr("Job %s is not in PAUSED state (now %s)", j.name, j.state)
				}
				j.state, j.pausedAt, j.reason = "NEED_SCHEDULE", "", ""
			case "stop":
				s.rls = append(s.rls[:i], s.rls[i+1:]...)
			}
			return okResult(), true, nil
		}
		return nil, true, sqlErr("Routine load job %s.%s does not exist", m[2], m[3])
	case reMVRef.MatchString(q):
		m := reMVRef.FindStringSubmatch(q)
		mv := s.mv(m[1], m[2])
		if mv == nil {
			return nil, true, sqlErr("Materialized view %s.%s does not exist", m[1], m[2])
		}
		if !mv.active {
			return nil, true, sqlErr("Materialized view %s is not active", m[2])
		}
		mv.state, mv.startedAt, mv.errMsg = "RUNNING", time.Now(), ""
		return okResult(), true, nil
	case reMVAlter.MatchString(q):
		m := reMVAlter.FindStringSubmatch(q)
		mv := s.mv(m[1], m[2])
		if mv == nil {
			return nil, true, sqlErr("Materialized view %s.%s does not exist", m[1], m[2])
		}
		mv.active = strings.EqualFold(m[3], "active")
		if mv.active {
			mv.inactiveReason = ""
		} else {
			mv.inactiveReason = "user use alter materialized view set status to inactive"
		}
		return okResult(), true, nil
	case reMVCancel.MatchString(q):
		m := reMVCancel.FindStringSubmatch(q)
		mv := s.mv(m[1], m[2])
		if mv == nil {
			return nil, true, sqlErr("Materialized view %s.%s does not exist", m[1], m[2])
		}
		if mv.state != "RUNNING" {
			return nil, true, sqlErr("No running refresh task for %s", m[2])
		}
		mv.state = "CANCELED"
		return okResult(), true, nil
	}
	return nil, false, nil
}

func (s *sim) mv(db, name string) *fakeMV {
	for _, m := range s.mvs {
		if m.db == db && m.name == name {
			return m
		}
	}
	return nil
}

// ---------- 查询 ----------

func (c *Cluster) showFrontendConfigSim(q string) (*mysql.Result, error) {
	cols := []string{"Key", "AliasNames", "Value", "Type", "IsMutable", "Comment"}
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	var all [][]any
	for _, v := range c.sim.feCfg {
		val := v.v
		if v.k == "run_mode" {
			val = pick(c.sharedDat, "shared_data", "shared_nothing")
		}
		all = append(all, []any{v.k, "[]", val, v.typ, boolStr(v.mutable), ""})
	}
	return resultset(cols, filterLike(q, all))
}

func (c *Cluster) showVariablesSim(q string) (*mysql.Result, error) {
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	var all [][]any
	for _, v := range c.sim.vars {
		all = append(all, []any{v.k, v.v, v.def, boolStr(v.v != v.def)})
	}
	return resultset([]string{"Variable_name", "Value", "Default_value", "Is_changed"}, filterLike(q, all))
}

func (c *Cluster) routineLoadJobsSim() (*mysql.Result, error) {
	cols := []string{"ID", "NAME", "CREATE_TIME", "PAUSE_TIME", "END_TIME", "DB_NAME", "TABLE_NAME", "STATE", "DATA_SOURCE_TYPE", "CURRENT_TASK_NUM",
		"JOB_PROPERTIES", "DATA_SOURCE_PROPERTIES", "CUSTOM_PROPERTIES", "STATISTICS", "PROGRESS", "TIMESTAMP_PROGRESS", "REASONS_OF_STATE_CHANGED", "ERROR_LOG_URLS", "TRACKING_SQL", "OTHER_MSG"}
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	var rows [][]any
	for i, j := range c.sim.rls {
		stat := fmt.Sprintf(`{"receivedBytes":%d,"errorRows":%d,"committedTaskNum":120,"loadedRows":%d,"loadRowsRate":%d,"abortedTaskNum":2,"totalRows":%d,"unselectedRows":0,"receivedBytesRate":812345,"taskExecuteTimeMs":9000}`,
			j.total*120, j.errRows, j.loaded, pickInt(j.state == "RUNNING", 46000, 0), j.total)
		errURL := ""
		if j.errRows > 0 {
			errURL = "http://127.0.0.1:18040/api/_load_error_log?file=error_log_" + j.name
		}
		rows = append(rows, []any{fmt.Sprint(20000 + i), j.name, "2026-09-12 10:00:00", j.pausedAt, "", j.db, j.table, j.state, "KAFKA", fmt.Sprint(pickInt(j.state == "RUNNING", 3, 0)),
			`{"currentTaskConcurrentNum":"3","maxBatchIntervalS":"10"}`, `{"topic":"` + j.name + `","currentKafkaPartitions":"0,1,2,3,4,5,6,7","brokerList":"kafka1:9092"}`, "{}",
			stat, `{"0":"1234567","1":"1234321"}`, `{"0":"2026-10-08 10:20:00"}`, j.reason, errURL, "", ""})
	}
	return resultset(cols, rows)
}

func pickInt(c bool, a, b int) int {
	if c {
		return a
	}
	return b
}

func (c *Cluster) routineLoadTasks(m []string) (*mysql.Result, error) {
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	cols := []string{"TaskId", "TxnId", "TxnStatus", "JobId", "CreateTime", "ExecuteStartTime", "Timeout", "BeId", "DataSourceProperties"}
	for i, j := range c.sim.rls {
		if j.db == m[1] && j.name == m[2] {
			if j.state != "RUNNING" {
				return resultset(cols, nil)
			}
			return resultset(cols, [][]any{{"a1b2c3d4", "9001", "UNKNOWN", fmt.Sprint(20000 + i), c.now(), c.now(), "60", "10001", `{"0":"1234567"}`}})
		}
	}
	return nil, sqlErr("Routine load job %s does not exist", m[2])
}

func (c *Cluster) materializedViewsSim() (*mysql.Result, error) {
	cols := []string{"MATERIALIZED_VIEW_ID", "TABLE_SCHEMA", "TABLE_NAME", "REFRESH_TYPE", "IS_ACTIVE", "INACTIVE_REASON", "PARTITION_TYPE", "TASK_ID", "TASK_NAME",
		"LAST_REFRESH_START_TIME", "LAST_REFRESH_FINISHED_TIME", "LAST_REFRESH_DURATION", "LAST_REFRESH_STATE", "LAST_REFRESH_ERROR_CODE", "LAST_REFRESH_ERROR_MESSAGE", "TABLE_ROWS", "MATERIALIZED_VIEW_DEFINITION"}
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	var rows [][]any
	for i, m := range c.sim.mvs {
		if m.state == "RUNNING" && time.Since(m.startedAt) > 1500*time.Millisecond { // 模拟刷新完成
			m.state, m.finishedAt = "SUCCESS", time.Now().Format(tsLayout)
		}
		start, dur := "", "0.000"
		if !m.startedAt.IsZero() {
			start = m.startedAt.Format(tsLayout)
			dur = "1.512"
		} else {
			start, dur = "2026-10-08 08:10:00", "12.345"
		}
		errCode := "0"
		if m.errMsg != "" {
			errCode = "1064"
		}
		rows = append(rows, []any{fmt.Sprint(30000 + i), m.db, m.name, m.refresh, boolStr(m.active), m.inactiveReason, m.partition, fmt.Sprint(40000 + i), "mv-" + fmt.Sprint(30000+i),
			start, m.finishedAt, dur, m.state, errCode, m.errMsg, fmt.Sprint(m.rows), m.def})
	}
	return resultset(cols, rows)
}

func (c *Cluster) loadsSim(l string) (*mysql.Result, error) {
	if strings.Contains(l, "group by") {
		return c.loads()
	}
	cols := []string{"JOB_ID", "LABEL", "DATABASE_NAME", "STATE", "PROGRESS", "TYPE", "PRIORITY", "SCAN_ROWS", "SCAN_BYTES", "FILTERED_ROWS", "UNSELECTED_ROWS", "SINK_ROWS",
		"CREATE_TIME", "LOAD_FINISH_TIME", "ERROR_MSG", "TRACKING_SQL"}
	rows := [][]any{
		{"50001", "broker_load_orders_20261008", "ods", "FINISHED", "ETL:100%; LOAD:100%", "BROKER", "NORMAL", "1200000", "310000000", "0", "0", "1200000", "2026-10-08 06:00:00", "2026-10-08 06:03:20", "", ""},
		{"50002", "insert_e5f2a1", "dw", "FINISHED", "", "INSERT", "NORMAL", "88000", "9100000", "0", "0", "88000", "2026-10-08 07:30:10", "2026-10-08 07:30:15", "", ""},
		{"50003", "stream_load_click_0930", "ods", "CANCELLED", "ETL:N/A; LOAD:N/A", "STREAM", "NORMAL", "500000", "120000000", "500000", "0", "0", "2026-10-08 08:20:00", "2026-10-08 08:20:03",
			"type:ETL_QUALITY_UNSATISFIED; msg:quality not good enough to cancel", "select tracking_log from information_schema.load_tracking_logs where job_id=50003"},
	}
	return resultset(cols, rows)
}

func (c *Cluster) showRunningQueries() (*mysql.Result, error) {
	cols := []string{"QueryId", "ResourceGroupId", "StartTime", "PendingTimeout", "QueryTimeout", "State", "Slots", "Fragments", "DOP", "Frontend", "FeStartTime"}
	rows := [][]any{{"q-run-1", "10021", c.now(), c.now(), c.now(), "RUNNING", "1", "3", "4", "fe-1", c.now()}}
	if c.problems {
		rows = append(rows, []any{"q-pend-1", "10031", c.now(), c.now(), c.now(), "PENDING", "1", "3", "4", "fe-1", c.now()},
			[]any{"q-pend-2", "10031", c.now(), c.now(), c.now(), "PENDING", "1", "3", "4", "fe-1", c.now()})
	}
	return resultset(cols, rows)
}

// 供测试读取内部状态
func (c *Cluster) sortedRGNames() []string {
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	var n []string
	for _, g := range c.sim.rgs {
		n = append(n, g.name)
	}
	sort.Strings(n)
	return n
}
