// @author: kevinkelly
// @license: Apache Licence
// @file: sim_nodes.go
// @time: 2026/10/28

package fakesr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
)

// 下线（DECOMMISSION）从开始到节点被移除所需的时间。
const decomDuration = 3 * time.Second

var (
	reDecom     = regexp.MustCompile(`(?i)^alter system decommission (backend|compute node) ["']([^"':]+):(\d+)["']$`)
	reDropNode  = regexp.MustCompile(`(?i)^alter system drop (backend|compute node|follower|observer) ["']([^"':]+):(\d+)["']$`)
	reBlackNode = regexp.MustCompile(`(?i)^(add|delete) (backend|compute node) blacklist ([0-9, ]+)$`)
	reShowBlack = regexp.MustCompile(`(?i)^show (backend|compute node) blacklist$`)
)

func (c *Cluster) decomProgress(n *node) float64 {
	p := float64(time.Since(n.decom)) / float64(decomDuration)
	if p > 1 {
		return 1
	}
	return p
}

// settleNodes 移除已完成下线的节点。
func (c *Cluster) settleNodes() {
	c.nmu.Lock()
	defer c.nmu.Unlock()
	keep := func(ns []*node) []*node {
		out := ns[:0:0]
		for _, n := range ns {
			if !n.decom.IsZero() && time.Since(n.decom) >= decomDuration {
				continue
			}
			out = append(out, n)
		}
		return out
	}
	c.BEs, c.CNs = keep(c.BEs), keep(c.CNs)
}

func (c *Cluster) nodeID(n *node) string {
	if n.kind == kindCN {
		return fmt.Sprint(20001 + n.idx)
	}
	return fmt.Sprint(10001 + n.idx)
}

// findNode 按 SQL 里的 "ip:port" 查找节点：BE / CN 用心跳端口，FE 用 edit_log 端口。
func (c *Cluster) findNode(kind, ip string, port int) *node {
	for _, n := range c.allNodes() {
		if n.ip != ip {
			continue
		}
		switch {
		case n.kind == kindFE && (kind == "follower" || kind == "observer") && n.ports.edit == port:
			return n
		case n.kind == kindBE && kind == "backend" && n.ports.hb == port:
			return n
		case n.kind == kindCN && kind == "compute node" && n.ports.hb == port:
			return n
		}
	}
	return nil
}

// handleNodes 处理节点下线、移除与黑名单语句。
func (c *Cluster) handleNodes(q, l string) (*mysql.Result, bool, error) {
	if res, handled, err := c.handleAddNode(q, l); handled {
		return res, handled, err
	}
	switch {
	case reDecom.MatchString(q):
		m := reDecom.FindStringSubmatch(q)
		port, _ := strconv.Atoi(m[3])
		c.settleNodes()
		n := c.findNode(strings.ToLower(m[1]), m[2], port)
		if n == nil {
			return nil, true, sqlErr("Backend does not exist[%s:%d]", m[2], port)
		}
		if strings.EqualFold(m[1], "compute node") {
			return nil, true, sqlErr("ALTER SYSTEM DECOMMISSION COMPUTE NODE is not supported, use DROP COMPUTE NODE")
		}
		c.nmu.Lock()
		defer c.nmu.Unlock()
		if !n.decom.IsZero() {
			return nil, true, sqlErr("backend [%s:%d] is already decommissioned", m[2], port)
		}
		if len(c.BEs) <= 3 {
			return nil, true, sqlErr("It will cause insufficient BE number(%d) to satisfy the replication num(3), cannot decommission", len(c.BEs)-1)
		}
		n.decom = time.Now()
		return okResult(), true, nil
	case reDropNode.MatchString(q):
		m := reDropNode.FindStringSubmatch(q)
		port, _ := strconv.Atoi(m[3])
		c.settleNodes()
		kind := strings.ToLower(m[1])
		n := c.findNode(kind, m[2], port)
		if n == nil {
			return nil, true, sqlErr("Unknown node [%s:%d]", m[2], port)
		}
		c.nmu.Lock()
		defer c.nmu.Unlock()
		switch n.kind {
		case kindFE:
			if n.role == "LEADER" {
				return nil, true, sqlErr("can not drop current master node.")
			}
			if (kind == "follower") != (n.role == "FOLLOWER") {
				return nil, true, sqlErr("frontend [%s:%d] is not a %s", m[2], port, kind)
			}
			c.FEs = removeNode(c.FEs, n)
		case kindBE:
			c.BEs = removeNode(c.BEs, n)
		case kindCN:
			c.CNs = removeNode(c.CNs, n)
		}
		delete(c.nodeBL, kindName(n)+":"+c.nodeID(n))
		return okResult(), true, nil
	case reBlackNode.MatchString(q):
		m := reBlackNode.FindStringSubmatch(q)
		kind := "be"
		if strings.EqualFold(m[2], "compute node") {
			kind = "cn"
		}
		c.settleNodes()
		var ids []string
		for _, s := range strings.Split(m[3], ",") {
			if s = strings.TrimSpace(s); s != "" {
				ids = append(ids, s)
			}
		}
		c.nmu.Lock()
		defer c.nmu.Unlock()
		if c.nodeBL == nil {
			c.nodeBL = map[string]string{}
		}
		for _, id := range ids {
			var found *node
			for _, n := range append(append([]*node{}, c.BEs...), c.CNs...) {
				if kindName(n) == kind && c.nodeID(n) == id {
					found = n
				}
			}
			if strings.EqualFold(m[1], "add") {
				if found == nil {
					return nil, true, sqlErr("Backend [%s] does not exist", id)
				}
				c.nodeBL[kind+":"+id] = time.Now().Format(tsLayout)
			} else {
				delete(c.nodeBL, kind+":"+id)
			}
		}
		return okResult(), true, nil
	case reShowBlack.MatchString(q):
		kind, col := "be", "BackendId"
		if strings.EqualFold(reShowBlack.FindStringSubmatch(q)[1], "compute node") {
			kind, col = "cn", "ComputeNodeId"
		}
		c.nmu.RLock()
		defer c.nmu.RUnlock()
		var rows [][]any
		for k, at := range c.nodeBL {
			if strings.HasPrefix(k, kind+":") {
				rows = append(rows, []any{strings.TrimPrefix(k, kind+":"), "MANUAL", at, "0", "0"})
			}
		}
		sortRows(rows)
		res, err := resultset([]string{col, "AddBlackListType", "LostConnectionTime", "LostConnectionNumberInPeriod", "CheckTimePeriod(s)"}, rows)
		return res, true, err
	}
	return nil, false, nil
}

func kindName(n *node) string { return n.kind.String() }

func removeNode(ns []*node, n *node) []*node {
	out := make([]*node, 0, len(ns))
	for _, x := range ns {
		if x != n {
			out = append(out, x)
		}
	}
	return out
}

func sortRows(rows [][]any) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j][0].(string) < rows[j-1][0].(string); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}
