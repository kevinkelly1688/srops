// @author: kevinkelly
// @license: Apache Licence
// @file: overview.go
// @time: 2026/10/28

package collect

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

// 统计库表时排除的系统库
const sysSchemas = "'information_schema','_statistics_','sys','starrocks_audit_db__','_starrocks_'"

// Overview 采集集群总览。各项独立采集，失败的写入 Warnings，其余照常返回。
func (m *Manager) Overview(ctx context.Context, c model.Cluster) (*model.Overview, error) {
	topo, mt, err := m.Topology(ctx, c)
	if err != nil {
		return nil, err
	}
	cl, _ := m.Client(ctx, c)
	o := &model.Overview{ClusterID: c.ID, CollectedAt: time.Now(), Nodes: topo.Nodes, Warnings: topo.Warnings}
	warn := func(what string, err error) { o.Warnings = append(o.Warnings, what+"未采集："+err.Error()) }

	// 节点计数与存储
	var used, total, maxDisk float64
	var hasDisk bool
	for _, n := range topo.Nodes {
		cnt := map[string]*model.Count{"fe": &o.KPI.FE, "be": &o.KPI.BE, "cn": &o.KPI.CN}[n.Type]
		cnt.Total++
		if n.Alive {
			cnt.Alive++
		}
		if n.Type == "be" {
			used += n.UsedBytes
			total += n.TotalBytes
			if n.Disk != nil && *n.Disk > maxDisk {
				maxDisk, hasDisk = *n.Disk, true
			}
		}
	}
	if total > 0 {
		o.KPI.StorageUsed, o.KPI.StorageTotal = &used, &total
	}
	if hasDisk {
		o.KPI.MaxDiskPct = &maxDisk
	}

	// QPS / 延迟 / 连接数：当前值来自 FE /metrics
	fe := map[string]sr.Metrics{}
	var conns float64
	var hasConn bool
	for _, n := range topo.Nodes {
		if mm, ok := mt[n.ID]; ok && n.Type == "fe" {
			fe[n.ID] = mm
			if v, ok := mm.Get("starrocks_fe_connection_total"); ok {
				conns += v
				hasConn = true
			}
		}
	}
	p := FEPoint(fe)
	o.KPI.QPS, o.KPI.P99 = p.QPS, p.P99
	if hasConn {
		o.KPI.Connections = &conns
	}
	if len(fe) == 0 {
		o.Warnings = append(o.Warnings, "FE /metrics 不可达：QPS、延迟、连接数未采集（检查平台到 FE http_port 的网络）")
	}

	// 趋势：优先 Prometheus，否则平台内置采样
	if c.Prom != "" {
		if pts, err := PromTrend(ctx, c); err == nil {
			o.Trend = model.Trend{Source: "prometheus", Points: pts}
		} else {
			warn("Prometheus 趋势", err)
		}
	}
	if o.Trend.Source == "" {
		o.Trend = model.Trend{Source: "sampler", Points: m.Sampler.Points(c.ID)}
	}

	// Tablet 健康 / 库表规模
	if r, err := cl.Query(ctx, "SHOW PROC '/statistic'"); err == nil {
		for i := range r.Data {
			if strings.EqualFold(r.Get(i, "DbId"), "Total") {
				v := atoi64(r.Get(i, "UnhealthyTabletNum"))
				o.KPI.Unhealthy = &v
				o.Info = append(o.Info, model.KV{K: "分区 / Tablet", V: fmt.Sprintf("%s / %s", human(r.Get(i, "PartitionNum")), human(r.Get(i, "TabletNum")))})
			}
		}
	} else {
		warn("Tablet 统计", err)
	}
	dbs, err1 := cl.Scalar(ctx, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name NOT IN ("+sysSchemas+")")
	tbs, err2 := cl.Scalar(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_type = 'BASE TABLE' AND table_schema NOT IN ("+sysSchemas+")")
	if err1 == nil && err2 == nil {
		o.Info = append([]model.KV{{K: "数据库 / 表", V: human(dbs) + " / " + human(tbs)}}, o.Info...)
	}

	// 资源组实时用量：按资源组合计各 BE / CN 的 CPU 核、内存与运行中查询
	for _, n := range topo.Nodes {
		if n.Type != "fe" && n.Alive {
			o.TotalCores += n.Cores
		}
	}
	if r, err := cl.Query(ctx, "SHOW USAGE RESOURCE GROUPS"); err == nil {
		o.ResourceGroups = []model.RGUsage{}
		idx := map[string]int{}
		for i := range r.Data {
			name := r.Get(i, "Name")
			k, ok := idx[name]
			if !ok {
				k = len(o.ResourceGroups)
				idx[name] = k
				o.ResourceGroups = append(o.ResourceGroups, model.RGUsage{Name: name})
			}
			g := &o.ResourceGroups[k]
			g.CPUCores += atof(r.Get(i, "BEInUseCpuCores"))
			g.MemBytes += atof(r.Get(i, "BEInUseMemBytes"))
			g.Running += int(atoi64(r.Get(i, "BERunningQueries")))
		}
		sort.SliceStable(o.ResourceGroups, func(a, b int) bool { return o.ResourceGroups[a].CPUCores > o.ResourceGroups[b].CPUCores })
	} else {
		warn("资源组用量（需要 3.1.4 及以上版本）", err)
	}

	// 物化视图 / Routine Load
	if r, err := cl.Query(ctx, "SELECT TABLE_SCHEMA, TABLE_NAME, IS_ACTIVE, LAST_REFRESH_STATE FROM information_schema.materialized_views"); err == nil {
		tot, failed := len(r.Data), 0
		for i := range r.Data {
			if strings.EqualFold(r.Get(i, "LAST_REFRESH_STATE"), "FAILED") {
				failed++
			}
		}
		o.KPI.MVTotal, o.KPI.MVFailed = &tot, &failed
	} else {
		warn("物化视图", err)
	}
	if r, err := cl.Query(ctx, "SELECT DB_NAME, NAME, STATE FROM information_schema.routine_load_jobs"); err == nil {
		run, paused := 0, 0
		for i := range r.Data {
			switch strings.ToUpper(r.Get(i, "STATE")) {
			case "RUNNING", "NEED_SCHEDULE":
				run++
			case "PAUSED":
				paused++
			}
		}
		o.KPI.RLRunning, o.KPI.RLPaused = &run, &paused
	} else {
		warn("Routine Load", err)
	}

	// 集群信息
	info := []model.KV{{K: "集群", V: c.Name}, {K: "版本", V: topo.Version}, {K: "架构", V: map[string]string{"shared-data": "存算分离", "shared-nothing": "存算一体"}[topo.Mode]}}
	if v, err := cl.FEConfig(ctx, "default_replication_num"); err == nil && v != "" {
		info = append(info, model.KV{K: "默认副本数", V: v})
	}
	if len(topo.Catalogs) > 0 {
		var names []string
		for _, ca := range topo.Catalogs {
			names = append(names, ca.Name)
		}
		info = append(info, model.KV{K: "Catalog", V: strings.Join(names, ", ")})
	}
	o.Info = append(info, o.Info...)
	return o, nil
}

func atof(s string) float64 { v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return v }

func atoi64(s string) int64 { v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return v }

// human 把大数字转成“万 / 亿”。
func human(s string) string {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return s
	}
	switch {
	case v >= 1e8:
		return strconv.FormatFloat(v/1e8, 'f', 1, 64) + " 亿"
	case v >= 1e4:
		return strconv.FormatFloat(v/1e4, 'f', 1, 64) + " 万"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
