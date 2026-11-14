// @author: kevinkelly
// @license: Apache Licence
// @file: rules.go
// @time: 2026/10/28

package inspect

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

// Rules 返回全部内置规则（顺序即报告顺序）。
func Rules() []Rule {
	return []Rule{
		// ---------- 节点健康 ----------
		{ID: "fe_alive", Cat: "节点健康", Name: "FE 节点存活", Src: "SHOW FRONTENDS → Alive", Doc: "administration/management/Scale_up_down/",
			Advice: "检查失联 FE 的进程与 fe.log；Follower 失联过半会导致无法选主。", Check: checkAlive("fe")},
		{ID: "be_alive", Cat: "节点健康", Name: "BE / CN 节点存活", Src: "SHOW BACKENDS / SHOW COMPUTE NODES → Alive", Doc: "administration/management/Scale_up_down/",
			Advice: "检查失联节点的进程、be.out / be.WARNING 与网络；长时间失联会触发副本补齐。", Check: checkAlive("be", "cn")},
		{ID: "fe_journal_lag", Cat: "节点健康", Name: "FE 元数据同步延迟", Src: "SHOW FRONTENDS → Leader 与各 FE 的 ReplayedJournalId 差值", Doc: "administration/Meta_recovery/",
			Warn: th(1000), Bad: th(100000), Unit: " 条", Advice: "检查落后 FE 的磁盘 IO、GC 与网络；持续落后可能导致读到旧元数据。", Check: checkJournalLag},
		{ID: "fe_jvm_heap", Cat: "节点健康", Name: "FE JVM 堆使用率", Src: "FE /metrics → jvm_heap_size_bytes{type=used|max}", Doc: "administration/management/monitoring/",
			Warn: th(80), Bad: th(90), Unit: "%", Advice: "调大 fe.conf 中 JAVA_OPTS 的 -Xmx（建议 ≥ 16G），排查分区 / Tablet 数量是否过多。", Check: checkJVM},
		// ---------- 资源水位 ----------
		{ID: "be_disk", Cat: "资源水位", Name: "BE 磁盘使用率", Src: "SHOW BACKENDS → MaxDiskUsedPct", Doc: "administration/management/Scale_up_down/",
			Warn: th(80), Bad: th(85), Unit: "%", Advice: "扩容 BE 或清理过期分区；达到 storage_flood_stage_usage_percent（默认 95%）后将拒绝导入。", Check: checkNodePct("be_disk")},
		{ID: "node_mem", Cat: "资源水位", Name: "BE / CN 内存使用率", Src: "SHOW BACKENDS / COMPUTE NODES → MemUsedPct", Doc: "administration/management/resource_management/",
			Warn: th(85), Bad: th(95), Unit: "%", Advice: "排查大查询与导入内存占用，配置资源组 mem_limit 与 query_mem_limit。", Check: checkNodePct("mem")},
		{ID: "node_cpu", Cat: "资源水位", Name: "BE / CN CPU 使用率", Src: "SHOW BACKENDS / COMPUTE NODES → CpuUsedPct", Doc: "administration/management/resource_management/",
			Warn: th(80), Bad: th(90), Unit: "%", Advice: "排查是否有大查询未受资源组约束，或 Compaction 积压导致 CPU 偏高。", Check: checkNodePct("cpu")},
		// ---------- 数据健康 ----------
		{ID: "unhealthy_tablets", Cat: "数据健康", Name: "不健康 Tablet", Src: "SHOW PROC '/statistic' → UnhealthyTabletNum", Doc: "administration/Data_recovery/",
			Warn: th(1), Bad: th(100), Unit: " 个", Advice: "SHOW PROC '/statistic/<DbId>' 定位库，ADMIN SHOW REPLICA STATUS 查看副本；节点重启后补齐中属正常，持续不降需排查。", Check: checkUnhealthy},
		{ID: "single_replica", Cat: "数据健康", Name: "单副本表", Src: "information_schema.partitions_meta → REPLICATION_NUM = 1", Doc: "table_design/",
			Warn: th(1), Bad: th(1), Unit: " 张", Advice: "生产表应使用 3 副本：ALTER TABLE t SET (\"replication_num\" = \"3\")；单副本在节点故障时会丢数据。", Check: checkSingleReplica},
		{ID: "small_tablets", Cat: "数据健康", Name: "Tablet 过小的表", Src: "information_schema.partitions_meta → DATA_SIZE / BUCKETS", Doc: "table_design/data_distribution/",
			Warn: th(1), Bad: th(50), Unit: " 张", Advice: "平均 Tablet 小于 100MB 且分桶 ≥ 16：元数据膨胀、调度开销大。建议使用自动分桶或调小 BUCKETS（单 Tablet 1GB–10GB 为宜）。", Check: checkSmallTablets},
		// ---------- Compaction ----------
		{ID: "compaction_score", Cat: "Compaction", Name: "Compaction Score", Src: "BE /metrics → starrocks_be_tablet_{cumulative,base}_max_compaction_score；存算分离：FE starrocks_fe_max_tablet_compaction_score", Doc: "administration/management/compaction/",
			Warn: th(50), Bad: th(100), Advice: "降低导入频率（攒批），检查磁盘 IO；必要时调大 compaction 线程数。分数过高会导致查询变慢甚至 too many versions。", Check: checkCompaction},
		// ---------- 查询性能 ----------
		{ID: "query_p99", Cat: "查询性能", Name: "查询 P99 延迟", Src: "FE /metrics → starrocks_fe_query_latency_ms{quantile=\"0.99\"}", Doc: "best_practices/query_tuning/",
			Warn: th(3000), Bad: th(10000), Unit: " ms", Advice: "在「查询分析」中按 SQL 指纹定位慢 SQL，查看 Query Profile。", Check: checkP99},
		{ID: "query_err_rate", Cat: "查询性能", Name: "查询失败率（FE 启动以来）", Src: "FE /metrics → starrocks_fe_query_err / starrocks_fe_query_total", Doc: "administration/management/monitoring/",
			Warn: th(1), Bad: th(5), Unit: "%", Advice: "查看 fe.audit.log / 审计表中 state=ERR 的 SQL，区分语法错误、超时与内存超限。", Check: checkErrRate},
		{ID: "slow_queries", Cat: "查询性能", Name: "慢查询数（> 5s，近 1 小时）", Src: "审计表 starrocks_audit_db__.starrocks_audit_tbl__", Doc: "best_practices/query_tuning/query_profile_overview/",
			Warn: th(50), Bad: th(200), Unit: " 条", Advice: "按 SQL 指纹聚合定位 Top SQL，查看 Query Profile。", Check: checkSlow},
		{ID: "big_query_guard", Cat: "查询性能", Name: "大查询熔断", Src: "SHOW RESOURCE GROUPS ALL → big_query_*", Doc: "administration/management/monitor_manage_big_queries/", Check: checkBigQuery},
		// ---------- 导入作业 ----------
		{ID: "routine_load_paused", Cat: "导入作业", Name: "Routine Load 暂停作业", Src: "information_schema.routine_load_jobs → STATE = PAUSED", Doc: "loading/RoutineLoad/",
			Warn: th(1), Bad: th(1), Unit: " 个", Advice: "查看 REASONS_OF_STATE_CHANGED，修复数据源问题后 RESUME ROUTINE LOAD。", Check: checkRoutineLoad},
		{ID: "load_fail_rate", Cat: "导入作业", Name: "导入失败率（近 24 小时）", Src: "information_schema.loads → STATE = CANCELLED", Doc: "loading/",
			Warn: th(2), Bad: th(10), Unit: "%", Advice: "查看 information_schema.loads 的 ERROR_MSG，常见为数据格式不符或 too many filtered rows。", Check: checkLoadFail},
		// ---------- 物化视图 ----------
		{ID: "mv_failed", Cat: "物化视图", Name: "刷新失败的物化视图", Src: "information_schema.materialized_views → LAST_REFRESH_STATE = FAILED", Doc: "using_starrocks/async_mv/Materialized_view/",
			Warn: th(1), Bad: th(5), Unit: " 个", Advice: "查看 LAST_REFRESH_ERROR_MESSAGE；内存不足可设置 session.query_mem_limit 或按分区刷新。", Check: checkMV(false)},
		{ID: "mv_inactive", Cat: "物化视图", Name: "非活跃物化视图", Src: "information_schema.materialized_views → IS_ACTIVE = false", Doc: "using_starrocks/async_mv/Materialized_view/",
			Warn: th(1), Unit: " 个", Advice: "基表 Schema Change 会使物化视图失效：确认定义有效后 ALTER MATERIALIZED VIEW mv ACTIVE。", Check: checkMV(true)},
		// ---------- 配置合规 ----------
		{ID: "default_replication", Cat: "配置合规", Name: "默认副本数", Src: "ADMIN SHOW FRONTEND CONFIG LIKE 'default_replication_num'", Doc: "administration/management/FE_configuration/", Check: checkReplication},
		{ID: "query_queue", Cat: "配置合规", Name: "查询队列", Src: "SHOW VARIABLES LIKE 'enable_query_queue_select'", Doc: "administration/management/resource_management/query_queues/", Check: checkQueryQueue},
		{ID: "version_consistent", Cat: "配置合规", Name: "节点版本一致", Src: "SHOW FRONTENDS / BACKENDS / COMPUTE NODES → Version", Doc: "deployment/upgrade/",
			Advice: "存在不同版本的节点：确认是否处于滚动升级过程中，升级完成后应保持一致。", Check: checkVersions},
		// ---------- 系统环境（SSH） ----------
		{ID: "os_swappiness", Cat: "系统环境", Name: "Swap（vm.swappiness）", Src: "cat /proc/sys/vm/swappiness", Doc: "deployment/environment_configurations/",
			Warn: th(1), Advice: "echo 0 > /proc/sys/vm/swappiness 并写入 /etc/sysctl.conf（vm.swappiness=0），同时 swapoff -a。", Check: checkSysNum("cat /proc/sys/vm/swappiness")},
		{ID: "os_thp", Cat: "系统环境", Name: "透明大页 THP", Src: "cat /sys/kernel/mm/transparent_hugepage/enabled", Doc: "deployment/environment_configurations/",
			Advice: "echo madvise > /sys/kernel/mm/transparent_hugepage/enabled，并写入开机脚本。", Check: checkTHP},
		{ID: "os_overcommit", Cat: "系统环境", Name: "内存 Overcommit", Src: "cat /proc/sys/vm/overcommit_memory", Doc: "deployment/environment_configurations/",
			Advice: "vm.overcommit_memory 建议为 1：echo 1 > /proc/sys/vm/overcommit_memory 并写入 /etc/sysctl.conf。", Check: checkOvercommit},
		{ID: "os_max_map_count", Cat: "系统环境", Name: "vm.max_map_count", Src: "cat /proc/sys/vm/max_map_count", Doc: "deployment/environment_configurations/",
			Warn: th(262144), Lower: true, Advice: "echo 262144 > /proc/sys/vm/max_map_count 并写入 /etc/sysctl.conf。", Check: checkSysNum("cat /proc/sys/vm/max_map_count")},
		{ID: "clock_skew", Cat: "系统环境", Name: "时钟偏差", Src: "date +%s%3N（与平台时间比较，已扣除网络往返）", Doc: "deployment/environment_configurations/",
			Warn: th(1000), Bad: th(5000), Unit: " ms", Advice: "启用 chrony / ntp 时钟同步；FE 之间时钟偏差超过 5 秒会导致无法选主。", Check: checkClock},
	}
}

// ---------- 实现 ----------

func pctStr(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + "%" }

func checkAlive(types ...string) func(e *Env, r *Rule) model.Item {
	return func(e *Env, r *Rule) model.Item {
		var ds []model.Detail
		tot, alive := 0, 0
		for _, n := range e.Topo.Nodes {
			if !in(n.Type, types) {
				continue
			}
			tot++
			st := "ok"
			if n.Alive {
				alive++
			} else {
				st = "bad"
			}
			ds = append(ds, model.Detail{Target: fmt.Sprintf("%s %s", strings.ToUpper(n.Type), n.Host), Value: map[bool]string{true: "存活", false: "失联 " + n.ErrMsg}[n.Alive], Status: st})
		}
		if tot == 0 {
			return r.skip("集群中没有此类节点")
		}
		it := r.item(fmt.Sprintf("%d / %d 存活", alive, tot), map[bool]string{true: "ok", false: "bad"}[alive == tot], "")
		it.Threshold = "全部存活"
		it.Details = ds
		return it
	}
}

func checkJournalLag(e *Env, r *Rule) model.Item {
	var lag int64
	var who string
	var ds []model.Detail
	for _, n := range e.Topo.Nodes {
		if n.Type != "fe" || n.Role == "Leader" || !n.Alive {
			continue
		}
		l := n.Journal - n.Replayed
		ds = append(ds, model.Detail{Target: n.Role + " " + n.Host, Value: fmt.Sprintf("落后 %d", l), Status: r.judge(float64(l))})
		if l >= lag {
			lag, who = l, n.Role+" "+n.Host
		}
	}
	if len(ds) == 0 {
		return r.skip("只有一个 FE")
	}
	it := r.value(float64(lag), fmt.Sprintf("最大落后 %d（%s）", lag, who))
	it.Details = ds
	return it
}

func checkJVM(e *Env, r *Rule) model.Item {
	var ds []model.Detail
	var max float64
	var who string
	for _, n := range e.Topo.Nodes {
		if n.Type == "fe" && n.JVM != nil {
			ds = append(ds, model.Detail{Target: n.Role + " " + n.Host, Value: pctStr(*n.JVM), Status: r.judge(*n.JVM)})
			if *n.JVM >= max {
				max, who = *n.JVM, n.Role+" "+n.Host
			}
		}
	}
	if len(ds) == 0 {
		return r.skip("FE /metrics 不可达")
	}
	it := r.value(max, fmt.Sprintf("最高 %s（%s）", pctStr(max), who))
	it.Details = ds
	return it
}

func checkNodePct(kind string) func(e *Env, r *Rule) model.Item {
	return func(e *Env, r *Rule) model.Item {
		var ds []model.Detail
		var max float64
		var who string
		for _, n := range e.Topo.Nodes {
			var v *float64
			switch kind {
			case "be_disk":
				if n.Type == "be" {
					v = n.Disk
				}
			case "mem":
				if n.Type != "fe" {
					v = n.Mem
				}
			case "cpu":
				if n.Type != "fe" {
					v = n.CPU
				}
			}
			if v == nil || !n.Alive {
				continue
			}
			ds = append(ds, model.Detail{Target: n.ID + " " + n.Host, Value: pctStr(*v), Status: r.judge(*v)})
			if *v >= max {
				max, who = *v, n.ID
			}
		}
		if len(ds) == 0 {
			if kind == "be_disk" && e.Topo.Mode == "shared-data" {
				return r.skip("存算分离集群数据在对象存储，无 BE 本地数据盘")
			}
			return r.skip("没有可用的节点数据（集群版本可能不提供该列）")
		}
		it := r.value(max, fmt.Sprintf("最高 %s（%s）", pctStr(max), who))
		it.Details = ds
		return it
	}
}

func checkUnhealthy(e *Env, r *Rule) model.Item {
	rows, err := e.CL.Query(e.Ctx, "SHOW PROC '/statistic'")
	if err != nil {
		return r.fail(err)
	}
	var ds []model.Detail
	var total float64 = -1
	for i := range rows.Data {
		v, _ := strconv.ParseFloat(rows.Get(i, "UnhealthyTabletNum"), 64)
		if strings.EqualFold(rows.Get(i, "DbId"), "Total") {
			total = v
		} else if v > 0 {
			ds = append(ds, model.Detail{Target: rows.Get(i, "DbName"), Value: num(v), Status: r.judge(v)})
		}
	}
	if total < 0 {
		return r.fail(fmt.Errorf("未找到 Total 行"))
	}
	it := r.value(total, num(total))
	it.Details = ds
	return it
}

type tableStat struct {
	db, tbl       string
	size          float64
	buckets, maxB int
	minRep        int
}

func partitionStats(e *Env) (map[string]*tableStat, *sr.Rows, error) {
	e.partsMu.Lock()
	defer e.partsMu.Unlock()
	if e.cache.parts != nil || e.cache.partsErr != nil {
		return e.cache.parts, e.cache.partsRows, e.cache.partsErr
	}
	rows, err := e.CL.Query(e.Ctx, "SELECT DB_NAME, TABLE_NAME, PARTITION_NAME, REPLICATION_NUM, BUCKETS, DATA_SIZE, MAX_CS FROM information_schema.partitions_meta")
	if err != nil {
		e.cache.partsErr = err
		return nil, nil, err
	}
	m := map[string]*tableStat{}
	for i := range rows.Data {
		k := rows.Get(i, "DB_NAME") + "." + rows.Get(i, "TABLE_NAME")
		t, ok := m[k]
		if !ok {
			t = &tableStat{db: rows.Get(i, "DB_NAME"), tbl: rows.Get(i, "TABLE_NAME"), minRep: 1 << 30}
			m[k] = t
		}
		b, _ := strconv.Atoi(rows.Get(i, "BUCKETS"))
		rep, err := strconv.Atoi(rows.Get(i, "REPLICATION_NUM"))
		t.size += sr.ParseSize(rows.Get(i, "DATA_SIZE"))
		t.buckets += b
		if b > t.maxB {
			t.maxB = b
		}
		if err == nil && rep < t.minRep {
			t.minRep = rep
		}
	}
	e.cache.parts, e.cache.partsRows = m, rows
	return m, rows, nil
}

func sortedKeys(m map[string]*tableStat) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func checkSingleReplica(e *Env, r *Rule) model.Item {
	if e.Topo.Mode == "shared-data" {
		return r.skip("存算分离集群数据持久化在对象存储，不依赖副本数")
	}
	m, _, err := partitionStats(e)
	if err != nil {
		return r.fail(err)
	}
	var ds []model.Detail
	for _, k := range sortedKeys(m) {
		if m[k].minRep == 1 {
			ds = append(ds, model.Detail{Target: k, Value: "replication_num = 1", Status: "bad"})
		}
	}
	it := r.value(float64(len(ds)), fmt.Sprintf("%d 张表", len(ds)))
	it.Details = ds
	return it
}

func checkSmallTablets(e *Env, r *Rule) model.Item {
	m, _, err := partitionStats(e)
	if err != nil {
		return r.fail(err)
	}
	var ds []model.Detail
	for _, k := range sortedKeys(m) {
		t := m[k]
		if t.maxB >= 16 && t.buckets > 0 && t.size/float64(t.buckets) < 100<<20 {
			ds = append(ds, model.Detail{Target: k, Value: fmt.Sprintf("平均 %.1f MB / Tablet，%d 个 Tablet", t.size/float64(t.buckets)/(1<<20), t.buckets), Status: "warn"})
		}
	}
	it := r.value(float64(len(ds)), fmt.Sprintf("%d 张表平均 Tablet < 100MB", len(ds)))
	it.Details = ds
	return it
}

func checkCompaction(e *Env, r *Rule) model.Item {
	var ds []model.Detail
	var max float64 = -1
	var who string
	if e.Topo.Mode == "shared-data" {
		for _, n := range e.Topo.Nodes {
			if m, ok := e.Metrics[n.ID]; ok && n.Type == "fe" {
				if v, ok := m.Get("starrocks_fe_max_tablet_compaction_score"); ok && v > max {
					max, who = v, "FE 汇总"
				}
			}
		}
		if _, rows, err := partitionStats(e); err == nil && rows.Has("MAX_CS") {
			for i := range rows.Data {
				v, err := strconv.ParseFloat(rows.Get(i, "MAX_CS"), 64)
				if err == nil && v >= *r.Warn {
					ds = append(ds, model.Detail{Target: rows.Get(i, "DB_NAME") + "." + rows.Get(i, "TABLE_NAME") + " / " + rows.Get(i, "PARTITION_NAME"), Value: num(v), Status: r.judge(v)})
				}
				if err == nil && v > max {
					max, who = v, rows.Get(i, "DB_NAME")+"."+rows.Get(i, "TABLE_NAME")
				}
			}
		}
	} else {
		for _, n := range e.Topo.Nodes {
			if n.Type == "be" && n.Compaction != nil {
				ds = append(ds, model.Detail{Target: n.ID + " " + n.Host, Value: num(*n.Compaction), Status: r.judge(*n.Compaction)})
				if *n.Compaction > max {
					max, who = *n.Compaction, n.ID
				}
			}
		}
	}
	if max < 0 {
		return r.skip("节点 /metrics 不可达，无法读取 Compaction Score")
	}
	it := r.value(max, fmt.Sprintf("最高 %s（%s）", num(max), who))
	it.Details = ds
	return it
}

func feMetricMax(e *Env, name string, labels ...string) (float64, bool) {
	var max float64
	ok := false
	for _, n := range e.Topo.Nodes {
		if m, has := e.Metrics[n.ID]; has && n.Type == "fe" {
			if v, has := m.Get(name, labels...); has && (!ok || v > max) {
				max, ok = v, true
			}
		}
	}
	return max, ok
}

func checkP99(e *Env, r *Rule) model.Item {
	v, ok := feMetricMax(e, "starrocks_fe_query_latency_ms", "quantile", "0.99")
	if !ok {
		return r.skip("FE /metrics 不可达")
	}
	return r.value(v, fmt.Sprintf("%.0f ms", v))
}

func checkErrRate(e *Env, r *Rule) model.Item {
	var errs, total float64
	got := false
	for _, n := range e.Topo.Nodes {
		if m, ok := e.Metrics[n.ID]; ok && n.Type == "fe" {
			a, ok1 := m.Get("starrocks_fe_query_err")
			b, ok2 := m.Get("starrocks_fe_query_total")
			if ok1 && ok2 {
				errs, total, got = errs+a, total+b, true
			}
		}
	}
	if !got || total == 0 {
		return r.skip("FE /metrics 不可达或尚无查询")
	}
	v := errs / total * 100
	return r.value(v, fmt.Sprintf("%.2f%%（%s / %s）", v, num(errs), num(total)))
}

var tableRe = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

func checkSlow(e *Env, r *Rule) model.Item {
	t := e.Cluster.AuditTable
	if t == "" {
		return r.skip("未配置审计表（AuditLoader），可在集群接入中补充")
	}
	if !tableRe.MatchString(t) {
		return r.fail(fmt.Errorf("审计表名不合法：%s", t))
	}
	v, err := e.CL.Scalar(e.Ctx, "SELECT COUNT(*) FROM "+t+" WHERE `timestamp` >= DATE_SUB(NOW(), INTERVAL 1 HOUR) AND queryTime > 5000")
	if err != nil {
		return r.fail(err)
	}
	n, _ := strconv.ParseFloat(v, 64)
	return r.value(n, num(n)+" 条")
}

func checkBigQuery(e *Env, r *Rule) model.Item {
	rows, err := e.CL.Query(e.Ctx, "SHOW RESOURCE GROUPS ALL")
	if err != nil {
		return r.fail(err)
	}
	var with []string
	seen := map[string]bool{}
	for i := range rows.Data {
		name := rows.Get(i, "name")
		for _, c := range []string{"big_query_cpu_second_limit", "big_query_scan_rows_limit", "big_query_mem_limit"} {
			if v, _ := strconv.ParseFloat(rows.Get(i, c), 64); v > 0 && !seen[name] {
				with, seen[name] = append(with, name), true
			}
		}
	}
	if len(with) > 0 {
		it := r.item("已配置："+strings.Join(with, "、"), "ok", "")
		it.Threshold = "至少一个资源组配置"
		return it
	}
	it := r.item("未配置", "info", "为 default / 即席查询资源组配置 big_query_cpu_second_limit、big_query_scan_rows_limit，防止单个大查询拖垮集群。")
	it.Threshold = "建议配置"
	return it
}

func checkRoutineLoad(e *Env, r *Rule) model.Item {
	rows, err := e.CL.Query(e.Ctx, "SELECT DB_NAME, NAME, STATE, REASONS_OF_STATE_CHANGED FROM information_schema.routine_load_jobs")
	if err != nil {
		return r.fail(err)
	}
	var ds []model.Detail
	for i := range rows.Data {
		if strings.EqualFold(rows.Get(i, "STATE"), "PAUSED") {
			ds = append(ds, model.Detail{Target: rows.Get(i, "DB_NAME") + "." + rows.Get(i, "NAME"), Value: rows.Get(i, "REASONS_OF_STATE_CHANGED"), Status: "bad"})
		}
	}
	it := r.value(float64(len(ds)), fmt.Sprintf("%d 个 PAUSED / 共 %d 个", len(ds), len(rows.Data)))
	it.Details = ds
	return it
}

func checkLoadFail(e *Env, r *Rule) model.Item {
	rows, err := e.CL.Query(e.Ctx, "SELECT STATE, COUNT(*) FROM information_schema.loads WHERE CREATE_TIME >= DATE_SUB(NOW(), INTERVAL 1 DAY) GROUP BY STATE")
	if err != nil {
		return r.fail(err)
	}
	var total, failed float64
	for i := range rows.Data {
		v, _ := strconv.ParseFloat(rows.Data[i][len(rows.Data[i])-1], 64)
		total += v
		if strings.EqualFold(rows.Get(i, "STATE"), "CANCELLED") {
			failed += v
		}
	}
	if total == 0 {
		it := r.item("近 24 小时无导入", "ok", "")
		return it
	}
	v := failed / total * 100
	return r.value(v, fmt.Sprintf("%.1f%%（%s / %s）", v, num(failed), num(total)))
}

func checkMV(inactive bool) func(e *Env, r *Rule) model.Item {
	return func(e *Env, r *Rule) model.Item {
		rows, err := e.CL.Query(e.Ctx, "SELECT TABLE_SCHEMA, TABLE_NAME, IS_ACTIVE, LAST_REFRESH_STATE, LAST_REFRESH_ERROR_MESSAGE FROM information_schema.materialized_views")
		if err != nil {
			return r.fail(err)
		}
		var ds []model.Detail
		for i := range rows.Data {
			name := rows.Get(i, "TABLE_SCHEMA") + "." + rows.Get(i, "TABLE_NAME")
			if inactive && strings.EqualFold(rows.Get(i, "IS_ACTIVE"), "false") {
				ds = append(ds, model.Detail{Target: name, Value: "INACTIVE", Status: "warn"})
			}
			if !inactive && strings.EqualFold(rows.Get(i, "LAST_REFRESH_STATE"), "FAILED") {
				ds = append(ds, model.Detail{Target: name, Value: rows.Get(i, "LAST_REFRESH_ERROR_MESSAGE"), Status: "warn"})
			}
		}
		it := r.value(float64(len(ds)), fmt.Sprintf("%d / 共 %d 个", len(ds), len(rows.Data)))
		it.Details = ds
		return it
	}
}

func checkReplication(e *Env, r *Rule) model.Item {
	if e.Topo.Mode == "shared-data" {
		return r.skip("存算分离集群不依赖副本数")
	}
	v, err := e.CL.FEConfig(e.Ctx, "default_replication_num")
	if err != nil {
		return r.fail(err)
	}
	n, _ := strconv.Atoi(v)
	bes := 0
	for _, x := range e.Topo.Nodes {
		if x.Type == "be" {
			bes++
		}
	}
	it := r.item(v, "ok", "")
	it.Threshold = "BE ≥ 3 时为 3"
	if n < 3 && bes >= 3 {
		it.Status, it.Advice = "warn", "ADMIN SET FRONTEND CONFIG (\"default_replication_num\" = \"3\") 并写入 fe.conf；已有表需单独修改 replication_num。"
	}
	return it
}

func checkQueryQueue(e *Env, r *Rule) model.Item {
	v, err := e.CL.Variable(e.Ctx, "enable_query_queue_select")
	if err != nil {
		return r.fail(err)
	}
	if strings.EqualFold(v, "true") {
		it := r.item("已开启", "ok", "")
		it.Threshold = "建议开启"
		return it
	}
	it := r.item("未开启", "info", "SET GLOBAL enable_query_queue_select = true，并配置 query_queue_concurrency_limit，削峰保护。")
	it.Threshold = "建议开启"
	return it
}

func checkVersions(e *Env, r *Rule) model.Item {
	vs := map[string][]string{}
	for _, n := range e.Topo.Nodes {
		if n.Version != "" {
			vs[n.Version] = append(vs[n.Version], n.ID)
		}
	}
	var keys []string
	for k := range vs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	it := r.item("一致 "+strings.Join(keys, ""), "ok", "")
	it.Threshold = "一致"
	if len(keys) > 1 {
		it.Value, it.Status, it.Advice = "存在 "+strings.Join(keys, " / "), "warn", r.Advice
		for _, k := range keys {
			it.Details = append(it.Details, model.Detail{Target: k, Value: strings.Join(vs[k], "、"), Status: "warn"})
		}
	}
	return it
}

// ---------- 系统环境（SSH） ----------

func sshResults(e *Env, r *Rule, cmd string, judge func(out string) (string, string)) model.Item {
	if e.Cluster.SSH == nil {
		return r.skip("未配置 SSH，可在「集群接入 → 完善信息」中补充")
	}
	res := e.SSHAll(cmd)
	var ds []model.Detail
	for _, h := range e.Hosts() {
		x := res[h]
		if x.Err != "" {
			ds = append(ds, model.Detail{Target: h, Value: x.Err, Status: "error"})
			continue
		}
		val, st := judge(strings.TrimSpace(x.Out))
		ds = append(ds, model.Detail{Target: h, Value: val, Status: st})
	}
	st := worst(ds)
	bad := 0
	for _, d := range ds {
		if d.Status != "ok" {
			bad++
		}
	}
	val := fmt.Sprintf("%d 台主机全部符合", len(ds))
	if bad > 0 {
		val = fmt.Sprintf("%d / %d 台主机不符合", bad, len(ds))
	}
	it := r.item(val, st, "")
	if it.Threshold == "—" {
		it.Threshold = map[string]string{"os_thp": "madvise 或 never", "os_overcommit": "1"}[r.ID]
	}
	it.Details = ds
	return it
}

func checkSysNum(cmd string) func(e *Env, r *Rule) model.Item {
	return func(e *Env, r *Rule) model.Item {
		return sshResults(e, r, cmd, func(out string) (string, string) {
			v, err := strconv.ParseFloat(out, 64)
			if err != nil {
				return "无法解析：" + out, "error"
			}
			return out, r.judge(v)
		})
	}
}

func checkTHP(e *Env, r *Rule) model.Item {
	return sshResults(e, r, "cat /sys/kernel/mm/transparent_hugepage/enabled", func(out string) (string, string) {
		if strings.Contains(out, "[always]") {
			return out, "warn"
		}
		return out, "ok"
	})
}

func checkOvercommit(e *Env, r *Rule) model.Item {
	return sshResults(e, r, "cat /proc/sys/vm/overcommit_memory", func(out string) (string, string) {
		if out != "1" {
			return out, "warn"
		}
		return out, "ok"
	})
}

func checkClock(e *Env, r *Rule) model.Item {
	if e.Cluster.SSH == nil {
		return r.skip("未配置 SSH，可在「集群接入 → 完善信息」中补充")
	}
	off := e.ClockOffsets()
	var ds []model.Detail
	var max float64
	for _, h := range e.Hosts() {
		o, ok := off[h]
		if !ok {
			ds = append(ds, model.Detail{Target: h, Value: e.SSHAll("date +%s%3N")[h].Err, Status: "error"})
			continue
		}
		a := o
		if a < 0 {
			a = -a
		}
		if a > max {
			max = a
		}
		ds = append(ds, model.Detail{Target: h, Value: fmt.Sprintf("%+.0f ms", o), Status: r.judge(a)})
	}
	if len(ds) == 0 {
		return r.skip("没有主机")
	}
	it := r.value(max, fmt.Sprintf("最大偏差 %.0f ms", max))
	if st := worst(ds); st == "warn" && it.Status == "ok" {
		it.Status = "warn"
	}
	it.Details = ds
	return it
}

func in(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
