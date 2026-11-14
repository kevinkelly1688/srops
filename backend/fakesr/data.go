// @author: kevinkelly
// @license: Apache Licence
// @file: data.go
// @time: 2026/10/28

package fakesr

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
)

const (
	tsLayout      = "2006-01-02 15:04:05"
	clusterID     = "1478523690"
	leaderJournal = 1182334
	beTotalTB     = 3.5
	nodeMemBytes  = 128 << 30 // 每个 BE/CN 128GiB 物理内存
)

// versionString 是 current_version()：取 Leader FE 当前运行的版本（升级期间各节点版本会不同）。
func (c *Cluster) versionString() string {
	v := c.cfg.Version
	for _, n := range c.nodesOf(kindFE) {
		if n.role == "LEADER" && n.ver != "" {
			v = n.ver
		}
	}
	if strings.Contains(v, "-") {
		return v
	}
	return v + "-a1b2c3d"
}

// verOf 返回节点版本（升级期间各节点版本可能不同）。
func (c *Cluster) verOf(n *node) string {
	if n.ver == "" {
		if strings.Contains(c.cfg.Version, "-") {
			return c.cfg.Version
		}
		return c.cfg.Version + "-a1b2c3d"
	}
	if strings.Contains(n.ver, "-") {
		return n.ver
	}
	return n.ver + "-a1b2c3d"
}

func (c *Cluster) now() string       { return time.Now().Format(tsLayout) }
func (c *Cluster) startTime() string { return c.startedAt.Add(-72 * time.Hour).Format(tsLayout) }
func boolStr(b bool) string          { return fmt.Sprintf("%t", b) }
func pct2(v float64) string          { return fmt.Sprintf("%.2f %%", v) }
func pct1(v float64) string          { return fmt.Sprintf("%.1f %%", v) }
func tb(v float64) string            { return fmt.Sprintf("%.3f TB", v) }
func pick[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

// ---------- 节点状态（SHOW 与 /metrics 共用） ----------

// beStat 是单个 BE 的静态指标。
type beStat struct {
	usedPct, maxDiskPct  float64
	cpuPct, memPct       float64
	cumuScore, baseScore float64
	tablets              int
	running              int
}

func (c *Cluster) beStat(i int) beStat {
	if c.problems {
		used := []float64{52.31, 48.77, 55.02, 61.40, 84.65, 50.12}
		maxd := []float64{58.20, 53.11, 60.45, 66.02, 87.12, 55.67}
		cpu := []float64{38.5, 41.2, 35.7, 92.0, 44.3, 39.8}
		mem := []float64{55.4, 58.9, 52.1, 64.7, 61.3, 57.2}
		cumu := []float64{24, 31, 27, 186, 38, 22}
		return beStat{used[i%6], maxd[i%6], cpu[i%6], mem[i%6], cumu[i%6], 6 + float64(i%3), 21480 + i*37, 3 + i%4}
	}
	used := []float64{41.20, 38.70, 44.10, 42.90, 45.30, 40.60}
	cpu := []float64{28.4, 31.9, 25.6, 33.2, 29.7, 27.1}
	mem := []float64{46.3, 49.8, 44.2, 51.7, 48.5, 45.9}
	cumu := []float64{8, 11, 7, 12, 9, 10}
	return beStat{used[i%6], used[i%6] + 3.15, cpu[i%6], mem[i%6], cumu[i%6], 3 + float64(i%3), 21480 + i*37, 2 + i%3}
}

func (c *Cluster) cnStat(i int) (cpu, mem float64) {
	if c.problems {
		return []float64{46.2, 51.8, 43.5, 57.9}[i%4], []float64{58.4, 62.1, 55.6, 60.3}[i%4]
	}
	return []float64{26.2, 31.8, 23.5, 29.9}[i%4], []float64{42.4, 47.1, 44.6, 40.3}[i%4]
}

// ---------- SHOW FRONTENDS / BACKENDS / COMPUTE NODES ----------

func (c *Cluster) showFrontends() (*mysql.Result, error) {
	cols := []string{"Name", "IP", "EditLogPort", "HttpPort", "QueryPort", "RpcPort", "Role", "ClusterId",
		"Join", "Alive", "ReplayedJournalId", "LastHeartbeat", "IsHelper", "ErrMsg", "StartTime", "Version"}
	var rows [][]any
	c.settleNodes()
	for _, fe := range c.nodesOf(kindFE) {
		host := fe.ip
		editLog := fe.ports.edit
		journal := leaderJournal
		switch fe.role {
		case "FOLLOWER":
			journal -= 2 + fe.idx
		case "OBSERVER":
			journal -= pick(c.problems, 1500, 5)
		}
		rows = append(rows, []any{
			fmt.Sprintf("%s_%d_%d", host, editLog, 1759800000000+int64(fe.idx)*1000),
			host,
			fmt.Sprint(editLog),
			fmt.Sprint(fe.httpPort),
			fmt.Sprint(c.QueryPort()),
			fmt.Sprint(fe.ports.rpc),
			fe.role,
			clusterID,
			"true",
			boolStr(!fe.down.Load()),
			fmt.Sprint(journal),
			c.now(),
			boolStr(fe.idx == 0),
			pick(fe.down.Load(), "java.net.ConnectException: Connection refused", ""),
			c.startTime(),
			c.verOf(fe),
		})
	}
	return resultset(cols, rows)
}

func (c *Cluster) showBackends() (*mysql.Result, error) {
	cols := []string{"BackendId", "IP", "HeartbeatPort", "BePort", "HttpPort", "BrpcPort", "LastStartTime",
		"LastHeartbeat", "Alive", "SystemDecommissioned", "ClusterDecommissioned", "TabletNum", "DataUsedCapacity",
		"AvailCapacity", "TotalCapacity", "UsedPct", "MaxDiskUsedPct", "ErrMsg", "Version", "Status",
		"DataTotalCapacity", "DataUsedPct", "CpuCores", "NumRunningQueries", "MemUsedPct", "CpuUsedPct"}
	var rows [][]any
	c.settleNodes()
	for _, be := range c.nodesOf(kindBE) {
		st := c.beStat(be.idx)
		if !be.decom.IsZero() { // 下线中：Tablet 逐步迁走
			st.tablets = int(float64(st.tablets) * (1 - c.decomProgress(be)))
		}
		used := beTotalTB * st.usedPct / 100
		dataUsed := used * 0.97
		avail := beTotalTB - used
		dataTotal := dataUsed + avail
		status := fmt.Sprintf(`{"lastSuccessReportTabletsTime":"%s","lastStreamLoadTime":-1,"isSetStoragePath":true}`,
			time.Now().Add(-30*time.Second).Format(tsLayout))
		rows = append(rows, []any{
			fmt.Sprint(10001 + be.idx),
			be.ip,
			fmt.Sprint(be.ports.hb),
			fmt.Sprint(be.ports.be),
			fmt.Sprint(be.httpPort),
			fmt.Sprint(be.ports.brpc),
			c.startTime(),
			c.now(),
			boolStr(!be.down.Load()),
			boolStr(!be.decom.IsZero()),
			"false",
			fmt.Sprint(st.tablets),
			tb(dataUsed),
			tb(avail),
			tb(beTotalTB),
			pct2(st.usedPct),
			pct2(st.maxDiskPct),
			pick(be.down.Load(), "java.net.ConnectException: Connection refused", ""),
			c.verOf(be),
			status,
			tb(dataTotal),
			pct2(dataUsed / dataTotal * 100),
			"32",
			fmt.Sprint(st.running),
			pct2(st.memPct),
			pct1(st.cpuPct),
		})
	}
	return resultset(cols, rows)
}

func (c *Cluster) showComputeNodes() (*mysql.Result, error) {
	cols := []string{"ComputeNodeId", "IP", "HeartbeatPort", "BePort", "HttpPort", "BrpcPort", "LastStartTime",
		"LastHeartbeat", "Alive", "SystemDecommissioned", "ClusterDecommissioned", "ErrMsg", "Version", "CpuCores",
		"NumRunningQueries", "MemUsedPct", "CpuUsedPct", "HasStoragePath", "StarletPort", "WorkerId", "WarehouseName"}
	var rows [][]any
	c.settleNodes()
	for _, cn := range c.nodesOf(kindCN) {
		cpu, mem := c.cnStat(cn.idx)
		starlet, worker := "0", "-1"
		if c.sharedDat {
			starlet, worker = fmt.Sprint(cn.ports.starlet), "1"
		}
		rows = append(rows, []any{
			fmt.Sprint(20001 + cn.idx),
			cn.ip,
			fmt.Sprint(cn.ports.hb),
			fmt.Sprint(cn.ports.be),
			fmt.Sprint(cn.httpPort),
			fmt.Sprint(cn.ports.brpc),
			c.startTime(),
			c.now(),
			boolStr(!cn.down.Load()),
			boolStr(!cn.decom.IsZero()),
			"false",
			pick(cn.down.Load(), "java.net.ConnectException: Connection refused", ""),
			c.verOf(cn),
			"32",
			fmt.Sprint(1 + cn.idx%3),
			pct2(mem),
			pct1(cpu),
			boolStr(c.sharedDat),
			starlet,
			worker,
			"default_warehouse",
		})
	}
	return resultset(cols, rows)
}

// ---------- 配置 / 权限 / 变量 ----------

// filterLike 按首列过滤 LIKE '<pattern>'；无 LIKE 时返回全部。
func filterLike(q string, rows [][]any) [][]any {
	p, ok := likePattern(q)
	if !ok {
		return rows
	}
	var out [][]any
	for _, r := range rows {
		if likeMatch(p, r[0].(string)) {
			out = append(out, r)
		}
	}
	return out
}

func (c *Cluster) showGrants() (*mysql.Result, error) {
	ui := fmt.Sprintf("'%s'@'%%'", c.cfg.User)
	sys := "NODE, OPERATE"
	if !c.problems {
		sys = "NODE, OPERATE, REPOSITORY"
	}
	rows := [][]any{
		{ui, "default_catalog", "GRANT SELECT ON ALL TABLES IN ALL DATABASES TO USER " + ui},
		{ui, "default_catalog", "GRANT " + sys + " ON SYSTEM TO USER " + ui},
		{ui, "default_catalog", "GRANT CREATE RESOURCE GROUP ON SYSTEM TO USER " + ui},
	}
	return resultset([]string{"UserIdentity", "Catalog", "Grants"}, rows)
}

func (c *Cluster) showCatalogs() (*mysql.Result, error) {
	return resultset([]string{"Catalog", "Type", "Comment"}, [][]any{
		{"default_catalog", "Internal", "An internal catalog contains this cluster's self-managed tables."},
		{"hive_ods", "Hive", "hive metastore catalog"},
		{"iceberg_lake", "Iceberg", "iceberg rest catalog"},
	})
}

// showUsageResourceGroups 模拟 SHOW USAGE RESOURCE GROUPS（3.1.4+）：每个 BE / CN 上正在使用的资源组。
func (c *Cluster) showUsageResourceGroups() (*mysql.Result, error) {
	cols := []string{"Name", "Id", "Backend", "BEInUseCpuCores", "BEInUseMemBytes", "BERunningQueries"}
	nodes := c.nodesOf(kindBE)
	if c.sharedDat {
		nodes = c.nodesOf(kindCN)
	}
	type rg struct {
		name, id string
		cores    float64
		memGB    float64
		running  int
	}
	groups := []rg{{"rg_bi", "10021", 3.2, 6, 5}, {"rg_etl", "10011", 2.1, 9, 2}, {"default_wg", "0", 0.6, 1.5, 1}}
	if c.problems {
		groups = append(groups, rg{"rg_adhoc", "10031", 7.4, 18, 6}) // 未限制的大查询占满 CPU
	}
	var rows [][]any
	for _, n := range nodes {
		for _, g := range groups {
			f := 1 + 0.1*float64(n.idx)
			rows = append(rows, []any{g.name, g.id, n.ip, fmt.Sprintf("%.3f", g.cores*f), fmt.Sprint(int64(g.memGB * f * (1 << 30))), fmt.Sprint(g.running)})
		}
	}
	return resultset(cols, rows)
}

// ---------- SHOW PROC '/statistic' ----------

func (c *Cluster) showStatistic() (*mysql.Result, error) {
	cols := []string{"DbId", "DbName", "TableNum", "PartitionNum", "IndexNum", "TabletNum", "ReplicaNum",
		"UnhealthyTabletNum", "InconsistentTabletNum", "CloningTabletNum", "ErrorStateTabletNum"}
	type db struct {
		id, name                         string
		tables, parts, idx, tablets      int
		unhealthy, inconsistent, cloning int
	}
	dbs := []db{
		{"10002", "ods", 412, 9860, 431, 158240, 0, 0, 0},
		{"10005", "dw", 386, 7120, 402, 214880, 0, 0, 0},
		{"10008", "ads", 268, 3010, 275, 42160, 0, 0, 0},
		{"10011", "dim", 154, 154, 160, 4928, 0, 0, 0},
		{"10014", "tmp", 66, 66, 66, 1056, 0, 0, 0},
	}
	if c.problems {
		dbs[0].unhealthy, dbs[0].cloning = 7, 3
		dbs[1].unhealthy, dbs[1].cloning = 5, 2
	}
	replicas := pick(c.sharedDat, 1, 3)
	var rows [][]any
	var t db
	for _, d := range dbs {
		rows = append(rows, []any{d.id, d.name, fmt.Sprint(d.tables), fmt.Sprint(d.parts), fmt.Sprint(d.idx),
			fmt.Sprint(d.tablets), fmt.Sprint(d.tablets * replicas), fmt.Sprint(d.unhealthy),
			fmt.Sprint(d.inconsistent), fmt.Sprint(d.cloning), "0"})
		t.tables += d.tables
		t.parts += d.parts
		t.idx += d.idx
		t.tablets += d.tablets
		t.unhealthy += d.unhealthy
		t.inconsistent += d.inconsistent
		t.cloning += d.cloning
	}
	rows = append(rows, []any{"Total", fmt.Sprint(len(dbs)), fmt.Sprint(t.tables), fmt.Sprint(t.parts),
		fmt.Sprint(t.idx), fmt.Sprint(t.tablets), fmt.Sprint(t.tablets * replicas), fmt.Sprint(t.unhealthy),
		fmt.Sprint(t.inconsistent), fmt.Sprint(t.cloning), "0"})
	return resultset(cols, rows)
}

// ---------- information_schema ----------

func (c *Cluster) loads() (*mysql.Result, error) {
	return resultset([]string{"STATE", "COUNT(*)"}, [][]any{
		{"FINISHED", int64(2414)},
		{"CANCELLED", int64(pick(c.problems, 17, 2))},
	})
}

// partTable 描述 partitions_meta 中一张表的分区。
type partTable struct {
	db, name   string
	sizes      []string  // 每个分区的数据量
	bucketsBad int       // problems 场景的分桶数
	bucketsOK  int       // healthy 场景的分桶数
	replBad    int       // problems 场景的副本数
	csBad      []float64 // shared-data problems 场景的 MAX_CS，nil 表示用默认
}

var partTables = []partTable{
	{"dw", "dwd_order_detail", []string{"52.4GB", "51.8GB", "49.9GB"}, 64, 64, 3, []float64{8, 11, 186}},
	{"dw", "dwd_user_event", []string{"88.1GB", "90.3GB", "86.7GB"}, 96, 96, 3, nil},
	{"dw", "dwd_payment", []string{"18.2GB", "17.6GB", "18.9GB"}, 64, 64, 3, nil},
	{"dw", "dws_shop_daily", []string{"8.4GB", "8.1GB", "7.9GB"}, 32, 32, 1, nil},
	{"ods", "ods_order_log", []string{"12.6GB", "12.1GB", "11.8GB"}, 32, 32, 1, nil},
	{"ods", "ods_click_log", []string{"21.3GB", "20.9GB", "22.4GB"}, 48, 48, 1, []float64{9, 142, 14}},
	{"ods", "ods_app_log", []string{"6.4GB"}, 32, 32, 3, nil},
	{"dim", "dim_user", []string{"85MB"}, 32, 4, 3, nil},
	{"dim", "dim_product", []string{"512KB"}, 16, 1, 3, nil},
	{"dim", "dim_region", []string{"48KB"}, 16, 1, 1, nil},
	{"ads", "ads_gmv_daily", []string{"1.2GB", "1.1GB", "980MB"}, 32, 8, 3, []float64{121, 6, 4}},
	{"ads", "ads_user_retention", []string{"640MB", "610MB"}, 24, 4, 3, nil},
	{"ads", "ads_channel_roi", []string{"210MB", "198MB"}, 16, 2, 3, nil},
	{"tmp", "tmp_import_check", []string{"2.3GB"}, 8, 8, 3, nil},
}

func (c *Cluster) partitionsMeta() (*mysql.Result, error) {
	cols := []string{"DB_NAME", "TABLE_NAME", "PARTITION_NAME", "REPLICATION_NUM", "BUCKETS", "DATA_SIZE", "MAX_CS"}
	var rows [][]any
	k := 0
	for _, t := range partTables {
		for i, size := range t.sizes {
			pname := t.name
			if len(t.sizes) > 1 {
				pname = fmt.Sprintf("p202610%02d", 5+i)
			}
			repl, buckets := 3, t.bucketsOK
			if c.problems {
				repl, buckets = t.replBad, t.bucketsBad
			}
			cs := float64(2 + k%9)
			if c.sharedDat && c.problems && t.csBad != nil {
				cs = t.csBad[i]
			}
			rows = append(rows, []any{t.db, t.name, pname, int64(repl), int64(buckets), size, cs})
			k++
		}
	}
	return resultset(cols, rows)
}
