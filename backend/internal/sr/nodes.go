// @author: kevinkelly
// @license: Apache Licence
// @file: nodes.go
// @time: 2026/10/28

package sr

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"starrocks-ops/internal/model"
)

// Nodes 读取 SHOW FRONTENDS / BACKENDS / COMPUTE NODES，按类型与地址排序后编号（fe-1、be-1 ...）。
// 某一类读取失败时返回已取得的节点，并把原因放进 warnings。
func (c *Client) Nodes(ctx context.Context, mode string) (nodes []model.Node, warnings []string) {
	if fe, err := c.Query(ctx, "SHOW FRONTENDS"); err != nil {
		warnings = append(warnings, "SHOW FRONTENDS 失败："+err.Error())
	} else {
		nodes = append(nodes, frontends(fe)...)
	}
	if mode != "shared-data" {
		if be, err := c.Query(ctx, "SHOW BACKENDS"); err != nil {
			warnings = append(warnings, "SHOW BACKENDS 失败："+err.Error())
		} else {
			nodes = append(nodes, backends(be)...)
		}
	}
	if cn, err := c.Query(ctx, "SHOW COMPUTE NODES"); err != nil {
		warnings = append(warnings, "SHOW COMPUTE NODES 失败："+err.Error())
	} else {
		nodes = append(nodes, computeNodes(cn)...)
	}
	number(nodes)
	return nodes, warnings
}

var roleOrder = map[string]int{"Leader": 0, "Follower": 1, "Observer": 2}

func number(nodes []model.Node) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.Type != b.Type {
			return typeOrder(a.Type) < typeOrder(b.Type)
		}
		if a.Type == "fe" && a.Role != b.Role {
			return roleOrder[a.Role] < roleOrder[b.Role]
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		return a.Port < b.Port
	})
	seq := map[string]int{}
	for i := range nodes {
		seq[nodes[i].Type]++
		nodes[i].ID = fmt.Sprintf("%s-%d", nodes[i].Type, seq[nodes[i].Type])
	}
}

func typeOrder(t string) int { return map[string]int{"fe": 0, "be": 1, "cn": 2}[t] }

func frontends(r *Rows) []model.Node {
	var out []model.Node
	for i := range r.Data {
		role := strings.ToUpper(r.Get(i, "Role"))
		n := model.Node{
			Type: "fe", Host: r.Get(i, "IP"), Port: atoi(r.Get(i, "QueryPort")), EditLog: atoi(r.Get(i, "EditLogPort")),
			RPC: atoi(r.Get(i, "RpcPort")), HTTP: atoi(r.Get(i, "HttpPort")), Alive: isTrue(r.Get(i, "Alive")),
			Version: shortVersion(r.Get(i, "Version")), Replayed: atoi64(r.Get(i, "ReplayedJournalId")),
			StartTime: r.Get(i, "StartTime"), HB: r.Get(i, "LastHeartbeat"), ErrMsg: r.Get(i, "ErrMsg"),
		}
		switch {
		case role == "LEADER" || isTrue(r.Get(i, "IsMaster")) || isTrue(r.Get(i, "IsLeader")):
			n.Role = "Leader"
		case role == "OBSERVER":
			n.Role = "Observer"
		default:
			n.Role = "Follower"
		}
		if n.Host == "" {
			n.Host = r.Get(i, "Host")
		}
		out = append(out, n)
	}
	// Journal：以 Leader 的已回放 ID 作为基准
	var leader int64
	for _, n := range out {
		if n.Role == "Leader" {
			leader = n.Replayed
		}
	}
	for i := range out {
		out[i].Journal = leader
		out[i].Status = status(out[i])
	}
	return out
}

func backends(r *Rows) []model.Node {
	var out []model.Node
	for i := range r.Data {
		n := model.Node{
			Type: "be", Host: r.Get(i, "IP"), Port: atoi(r.Get(i, "HeartbeatPort")), BEPort: atoi(r.Get(i, "BePort")),
			HTTP: atoi(r.Get(i, "HttpPort")), BRPC: atoi(r.Get(i, "BrpcPort")), Alive: isTrue(r.Get(i, "Alive")),
			Version: shortVersion(r.Get(i, "Version")), Tablets: atoi64(r.Get(i, "TabletNum")),
			StartTime: r.Get(i, "LastStartTime"), HB: r.Get(i, "LastHeartbeat"), ErrMsg: r.Get(i, "ErrMsg"),
			CPU: pct(r.Get(i, "CpuUsedPct")), Mem: pct(r.Get(i, "MemUsedPct")), Disk: pct(r.Get(i, "MaxDiskUsedPct")),
			Running: atoi(r.Get(i, "NumRunningQueries")), Cores: atoi(r.Get(i, "CpuCores")),
		}
		if n.Disk == nil {
			n.Disk = pct(r.Get(i, "UsedPct"))
		}
		used, total := ParseSize(r.Get(i, "DataUsedCapacity")), ParseSize(r.Get(i, "TotalCapacity"))
		if dt := ParseSize(r.Get(i, "DataTotalCapacity")); dt > 0 {
			total = dt
		}
		n.UsedBytes, n.TotalBytes = used, total
		if total > 0 {
			n.DataUsed, n.DataTotal = f(round(used/(1<<40), 2)), f(round(total/(1<<40), 2))
		}
		if isTrue(r.Get(i, "SystemDecommissioned")) || isTrue(r.Get(i, "ClusterDecommissioned")) {
			n.Status = "decommission"
		} else {
			n.Status = status(n)
		}
		out = append(out, n)
	}
	return out
}

func computeNodes(r *Rows) []model.Node {
	var out []model.Node
	for i := range r.Data {
		n := model.Node{
			Type: "cn", Host: r.Get(i, "IP"), Port: atoi(r.Get(i, "HeartbeatPort")), BEPort: atoi(r.Get(i, "BePort")),
			HTTP: atoi(r.Get(i, "HttpPort")), BRPC: atoi(r.Get(i, "BrpcPort")), Starlet: atoi(r.Get(i, "StarletPort")),
			Alive: isTrue(r.Get(i, "Alive")), Version: shortVersion(r.Get(i, "Version")),
			StartTime: r.Get(i, "LastStartTime"), HB: r.Get(i, "LastHeartbeat"), ErrMsg: r.Get(i, "ErrMsg"),
			CPU: pct(r.Get(i, "CpuUsedPct")), Mem: pct(r.Get(i, "MemUsedPct")), Warehouse: r.Get(i, "WarehouseName"),
			Running: atoi(r.Get(i, "NumRunningQueries")), Cores: atoi(r.Get(i, "CpuCores")),
		}
		if isTrue(r.Get(i, "SystemDecommissioned")) {
			n.Status = "decommission"
		} else {
			n.Status = status(n)
		}
		out = append(out, n)
	}
	return out
}

// status：失联为 down；资源或错误信息异常为 warn。
func status(n model.Node) string {
	if !n.Alive {
		return "down"
	}
	if n.ErrMsg != "" || gte(n.Disk, 85) || gte(n.CPU, 90) || gte(n.Mem, 90) || gte(n.JVM, 85) || gte(n.Compaction, 100) {
		return "warn"
	}
	return "up"
}

// Refresh 根据补充的指标（JVM、Compaction）重新计算状态。
func Refresh(n *model.Node) {
	if n.Status != "decommission" {
		n.Status = status(*n)
	}
}

// ---------- 解析工具 ----------

func gte(p *float64, v float64) bool { return p != nil && *p >= v }
func f(v float64) *float64           { return &v }
func round(v float64, d int) float64 {
	p := 1.0
	for i := 0; i < d; i++ {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}

func isTrue(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "true") }
func atoi(s string) int    { v, _ := strconv.Atoi(strings.TrimSpace(s)); return v }
func atoi64(s string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v
}

// pct 解析 "87.12 %" / "87.12%" / "87.12"，空串返回 nil。
func pct(s string) *float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return f(round(v, 2))
}

// Pct 是 pct 的导出版本。
func Pct(s string) *float64 { return pct(s) }

// ParseSize 解析 "3.100 TB" / "85MB" / "512 KB" / "1024"（字节），失败返回 0。
func ParseSize(s string) float64 {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	if s == "" {
		return 0
	}
	units := []struct {
		suf string
		mul float64
	}{{"PB", 1 << 50}, {"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suf) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suf), 64)
			if err != nil {
				return 0
			}
			return v * u.mul
		}
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func shortVersion(v string) string {
	if i := strings.IndexAny(v, "- "); i > 0 {
		return v[:i]
	}
	return v
}
