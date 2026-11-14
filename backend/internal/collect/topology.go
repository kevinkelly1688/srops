// @author: kevinkelly
// @license: Apache Licence
// @file: topology.go
// @time: 2026/10/28

package collect

import (
	"context"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

// NodeMetrics 并发抓取全部存活节点的 /metrics，key 为节点 ID；失败的节点不在结果中。
func NodeMetrics(ctx context.Context, cl *sr.Client, nodes []model.Node) map[string]sr.Metrics {
	out := map[string]sr.Metrics{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, n := range nodes {
		if !n.Alive || n.HTTP == 0 {
			continue
		}
		wg.Add(1)
		go func(n model.Node) {
			defer wg.Done()
			c, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			if m, err := cl.NodeMetrics(c, n.Host, n.HTTP); err == nil {
				mu.Lock()
				out[n.ID] = m
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	return out
}

// Enrich 用节点指标补充 FE JVM 堆使用率与 BE Compaction Score，并刷新状态。
func Enrich(nodes []model.Node, mt map[string]sr.Metrics) {
	for i := range nodes {
		n := &nodes[i]
		m, ok := mt[n.ID]
		if !ok {
			continue
		}
		switch n.Type {
		case "fe":
			used, ok1 := m.Get("jvm_heap_size_bytes", "type", "used")
			max, ok2 := m.Get("jvm_heap_size_bytes", "type", "max")
			if ok1 && ok2 && max > 0 {
				v := float64(int(used/max*1000+0.5)) / 10
				n.JVM = &v
			}
		case "be":
			c1, ok1 := m.Max("starrocks_be_tablet_cumulative_max_compaction_score")
			c2, ok2 := m.Max("starrocks_be_tablet_base_max_compaction_score")
			if ok1 || ok2 {
				v := c1
				if !ok1 || (ok2 && c2 > v) {
					v = c2
				}
				n.Compaction = &v
			}
		}
		sr.Refresh(n)
	}
}

// Topology 采集集群架构。
func (m *Manager) Topology(ctx context.Context, c model.Cluster) (*model.Topology, map[string]sr.Metrics, error) {
	cl, err := m.Client(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	t := &model.Topology{ClusterID: c.ID, CollectedAt: time.Now(), Version: c.Version, Mode: c.Mode, Warnings: []string{}}
	if v, err := cl.Version(ctx); err == nil && v != "" {
		t.Version = v
	}
	t.Mode = cl.RunMode(ctx)
	nodes, warns := cl.Nodes(ctx, t.Mode)
	t.Warnings = append(t.Warnings, warns...)
	for i := range nodes {
		if c.Dirs != nil {
			if nodes[i].Type == "fe" {
				nodes[i].Dir = c.Dirs.FE
			} else {
				nodes[i].Dir = c.Dirs.BE
			}
		}
	}
	mt := NodeMetrics(ctx, cl, nodes)
	Enrich(nodes, mt)
	t.Nodes = nodes
	if r, err := cl.Query(ctx, "SHOW CATALOGS"); err == nil {
		for i := range r.Data {
			t.Catalogs = append(t.Catalogs, model.Catalog{Name: r.Get(i, "Catalog"), Type: r.Get(i, "Type")})
		}
	} else {
		t.Warnings = append(t.Warnings, "SHOW CATALOGS 失败："+err.Error())
	}
	return t, mt, nil
}
