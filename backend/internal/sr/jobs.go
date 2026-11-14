// @author: kevinkelly
// @license: Apache Licence
// @file: jobs.go
// @time: 2026/10/28

package sr

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ---------- Routine Load ----------

// RoutineLoad 是 information_schema.routine_load_jobs 的一行。
type RoutineLoad struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	DB         string  `json:"db"`
	Table      string  `json:"table"`
	State      string  `json:"state"`
	Source     string  `json:"source"` // KAFKA / PULSAR
	SourceProp string  `json:"sourceProp"`
	Tasks      int     `json:"tasks"`
	CreateTime string  `json:"createTime"`
	PauseTime  string  `json:"pauseTime"`
	Reason     string  `json:"reason"`
	ErrorURLs  string  `json:"errorUrls"`
	OtherMsg   string  `json:"otherMsg"`
	Progress   string  `json:"progress"`
	Lag        string  `json:"lag"`
	Loaded     int64   `json:"loadedRows"`
	ErrorRows  int64   `json:"errorRows"`
	Total      int64   `json:"totalRows"`
	RowRate    int64   `json:"rowRate"`
	ErrPct     float64 `json:"errPct"`
}

// RoutineLoads 读取全部 Routine Load 作业。
func (c *Client) RoutineLoads(ctx context.Context) ([]RoutineLoad, error) {
	r, err := c.Query(ctx, "SELECT * FROM information_schema.routine_load_jobs")
	if err != nil {
		return nil, err
	}
	var out []RoutineLoad
	for i := range r.Data {
		j := RoutineLoad{ID: r.Get(i, "ID"), Name: r.Get(i, "NAME"), DB: r.Get(i, "DB_NAME"), Table: r.Get(i, "TABLE_NAME"), State: r.Get(i, "STATE"),
			Source: r.Get(i, "DATA_SOURCE_TYPE"), SourceProp: r.Get(i, "DATA_SOURCE_PROPERTIES"), Tasks: atoiS(r.Get(i, "CURRENT_TASK_NUM")),
			CreateTime: r.Get(i, "CREATE_TIME"), PauseTime: r.Get(i, "PAUSE_TIME"), Reason: r.Get(i, "REASONS_OF_STATE_CHANGED"),
			ErrorURLs: r.Get(i, "ERROR_LOG_URLS"), OtherMsg: r.Get(i, "OTHER_MSG"), Progress: r.Get(i, "PROGRESS")}
		var st struct {
			LoadedRows   int64   `json:"loadedRows"`
			ErrorRows    int64   `json:"errorRows"`
			TotalRows    int64   `json:"totalRows"`
			LoadRowsRate float64 `json:"loadRowsRate"`
		}
		if json.Unmarshal([]byte(r.Get(i, "STATISTICS")), &st) == nil {
			j.Loaded, j.ErrorRows, j.Total, j.RowRate = st.LoadedRows, st.ErrorRows, st.TotalRows, int64(st.LoadRowsRate)
			if st.TotalRows > 0 {
				j.ErrPct = float64(int(float64(st.ErrorRows)/float64(st.TotalRows)*10000+0.5)) / 100
			}
		}
		j.Lag = lagOf(r.Get(i, "TIMESTAMP_PROGRESS"), r.Get(i, "PROGRESS"))
		if j.Name != "" {
			out = append(out, j)
		}
	}
	return out, nil
}

// lagOf 尽力给出消费延迟的描述：优先用 TIMESTAMP_PROGRESS（各分区消费到的消息时间），否则为空。
func lagOf(tsProgress, _ string) string {
	if tsProgress == "" {
		return ""
	}
	var m map[string]string
	if json.Unmarshal([]byte(tsProgress), &m) != nil || len(m) == 0 {
		return ""
	}
	return fmt.Sprintf("%d 个分区有消费进度", len(m))
}

// RoutineLoadOp 对 Routine Load 作业执行 PAUSE / RESUME / STOP。
func (c *Client) RoutineLoadSQL(op, db, name string) (string, error) {
	qd, ok1 := Quote(db)
	qn, ok2 := Quote(name)
	if !ok1 || !ok2 {
		return "", fmt.Errorf("库名或作业名不合法")
	}
	switch op {
	case "PAUSE", "RESUME", "STOP":
		return fmt.Sprintf("%s ROUTINE LOAD FOR %s.%s", op, qd, qn), nil
	}
	return "", fmt.Errorf("不支持的操作 %s", op)
}

// RoutineLoadTasks 读取作业当前的子任务（SHOW ROUTINE LOAD TASK）。
func (c *Client) RoutineLoadTasks(ctx context.Context, db, name string) (*Rows, error) {
	qd, ok1 := Quote(db)
	if !ok1 || !ValidIdent(name) {
		return nil, fmt.Errorf("库名或作业名不合法")
	}
	return c.Query(ctx, fmt.Sprintf("SHOW ROUTINE LOAD TASK FROM %s WHERE JobName = %s", qd, MustLit(name)))
}

// ---------- 其他导入（information_schema.loads） ----------

// LoadJob 是 information_schema.loads 的一行（Broker / Stream / Insert / Spark Load 等）。
type LoadJob struct {
	Label    string `json:"label"`
	DB       string `json:"db"`
	State    string `json:"state"`
	Type     string `json:"type"`
	Progress string `json:"progress"`
	Scan     int64  `json:"scanRows"`
	Filtered int64  `json:"filteredRows"`
	Sink     int64  `json:"sinkRows"`
	Created  string `json:"createTime"`
	Finished string `json:"finishTime"`
	Error    string `json:"error"`
	Tracking string `json:"trackingSql"`
}

// RecentLoads 读取最近的导入作业（按创建时间倒序，最多 limit 条）。
func (c *Client) RecentLoads(ctx context.Context, limit int) ([]LoadJob, error) {
	r, err := c.Query(ctx, fmt.Sprintf("SELECT * FROM information_schema.loads ORDER BY CREATE_TIME DESC LIMIT %d", limit))
	if err != nil {
		return nil, err
	}
	var out []LoadJob
	for i := range r.Data {
		out = append(out, LoadJob{Label: r.Get(i, "LABEL"), DB: r.Get(i, "DATABASE_NAME"), State: r.Get(i, "STATE"), Type: r.Get(i, "TYPE"), Progress: r.Get(i, "PROGRESS"),
			Scan: atoi64S(r.Get(i, "SCAN_ROWS")), Filtered: atoi64S(r.Get(i, "FILTERED_ROWS")), Sink: atoi64S(r.Get(i, "SINK_ROWS")),
			Created: r.Get(i, "CREATE_TIME"), Finished: r.Get(i, "LOAD_FINISH_TIME"), Error: r.Get(i, "ERROR_MSG"), Tracking: r.Get(i, "TRACKING_SQL")})
	}
	return out, nil
}

// LoadStats 统计近期导入的状态分布。
func (c *Client) LoadStats(ctx context.Context) (map[string]int64, error) {
	r, err := c.Query(ctx, "SELECT STATE, COUNT(*) FROM information_schema.loads GROUP BY STATE")
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, row := range r.Data {
		if len(row) >= 2 {
			n, _ := strconv.ParseInt(row[1], 10, 64)
			out[strings.ToUpper(row[0])] += n
		}
	}
	return out, nil
}

// ---------- 物化视图 ----------

// MV 是 information_schema.materialized_views 的一行。
type MV struct {
	DB         string `json:"db"`
	Name       string `json:"name"`
	Refresh    string `json:"refresh"`
	Active     bool   `json:"active"`
	Inactive   string `json:"inactiveReason"`
	Partition  string `json:"partition"`
	State      string `json:"state"`
	LastStart  string `json:"lastStart"`
	LastFinish string `json:"lastFinish"`
	Duration   string `json:"duration"`
	Error      string `json:"error"`
	Rows       int64  `json:"rows"`
	Definition string `json:"definition"`
}

// MVs 读取全部异步物化视图。
func (c *Client) MVs(ctx context.Context) ([]MV, error) {
	r, err := c.Query(ctx, "SELECT * FROM information_schema.materialized_views")
	if err != nil {
		return nil, err
	}
	var out []MV
	for i := range r.Data {
		m := MV{DB: r.Get(i, "TABLE_SCHEMA"), Name: r.Get(i, "TABLE_NAME"), Refresh: r.Get(i, "REFRESH_TYPE"), Active: isTrue(r.Get(i, "IS_ACTIVE")),
			Inactive: r.Get(i, "INACTIVE_REASON"), Partition: r.Get(i, "PARTITION_TYPE"), State: r.Get(i, "LAST_REFRESH_STATE"),
			LastStart: r.Get(i, "LAST_REFRESH_START_TIME"), LastFinish: r.Get(i, "LAST_REFRESH_FINISHED_TIME"), Duration: r.Get(i, "LAST_REFRESH_DURATION"),
			Error: r.Get(i, "LAST_REFRESH_ERROR_MESSAGE"), Rows: atoi64S(r.Get(i, "TABLE_ROWS")), Definition: r.Get(i, "MATERIALIZED_VIEW_DEFINITION")}
		if m.Name != "" {
			out = append(out, m)
		}
	}
	return out, nil
}

// MVSQL 生成物化视图的管理语句。
func MVSQL(op, db, name string, opt MVRefreshOpt) (string, error) {
	qd, ok1 := Quote(db)
	qn, ok2 := Quote(name)
	if !ok1 || !ok2 {
		return "", fmt.Errorf("库名或物化视图名不合法")
	}
	full := qd + "." + qn
	switch op {
	case "refresh":
		s := "REFRESH MATERIALIZED VIEW " + full
		if opt.Start != "" || opt.End != "" {
			if !dateRe.MatchString(opt.Start) || !dateRe.MatchString(opt.End) {
				return "", fmt.Errorf("分区区间须填写 YYYY-MM-DD 或 YYYY-MM-DD HH:MM:SS 格式的起止时间")
			}
			s += fmt.Sprintf(" PARTITION START (%s) END (%s)", MustLit(opt.Start), MustLit(opt.End))
		}
		if opt.Force {
			s += " FORCE"
		}
		return s, nil
	case "active":
		return "ALTER MATERIALIZED VIEW " + full + " ACTIVE", nil
	case "inactive":
		return "ALTER MATERIALIZED VIEW " + full + " INACTIVE", nil
	case "cancel":
		return "CANCEL REFRESH MATERIALIZED VIEW " + full, nil
	}
	return "", fmt.Errorf("不支持的操作 %s", op)
}

// MVRefreshOpt 是手动刷新的可选参数。
type MVRefreshOpt struct {
	Start, End string
	Force      bool
}

// ---------- 查询队列 ----------

// QueueItem 是查询队列相关的一个变量或 FE 配置。
type QueueItem struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source"` // var（全局变量）/ fe（FE 配置，需逐台设置）
	Bool   bool   `json:"bool"`
}

// QueueKeys 是平台管理的查询队列参数。
var QueueKeys = []struct {
	Key  string
	Bool bool
	Max  int64
}{
	{"enable_query_queue_select", true, 0}, {"enable_query_queue_statistic", true, 0}, {"enable_query_queue_load", true, 0},
	{"query_queue_concurrency_limit", false, 100000}, {"query_queue_mem_used_pct_limit", false, 0},
	{"query_queue_cpu_used_permille_limit", false, 1000}, {"query_queue_max_queued_queries", false, 1000000}, {"query_queue_pending_timeout_second", false, 86400},
}

// QueueConfig 读取查询队列配置：先找全局变量，没有再找 FE 配置；当前版本不支持的项会被省略。
func (c *Client) QueueConfig(ctx context.Context) []QueueItem {
	var out []QueueItem
	for _, k := range QueueKeys {
		if v, err := c.Variable(ctx, k.Key); err == nil && v != "" {
			out = append(out, QueueItem{k.Key, v, "var", k.Bool})
			continue
		}
		if v, err := c.FEConfig(ctx, k.Key); err == nil && v != "" {
			out = append(out, QueueItem{k.Key, v, "fe", k.Bool})
		}
	}
	return out
}

// ParseFloatDefault 解析数字，失败返回 def。
func ParseFloatDefault(s string, def float64) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return def
	}
	return f
}
