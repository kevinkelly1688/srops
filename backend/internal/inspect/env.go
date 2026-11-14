// @author: kevinkelly
// @license: Apache Licence
// @file: env.go
// @time: 2026/10/28

package inspect

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
	"starrocks-ops/internal/sshx"
)

// Env 是一次巡检的上下文：集群连接、拓扑与节点指标只采集一次，供所有规则共享。
type Env struct {
	Ctx     context.Context
	Cluster model.Cluster
	CL      *sr.Client
	Topo    *model.Topology
	Metrics map[string]sr.Metrics
	Keys    *sshx.Keys
	SSHPass string

	cache struct {
		parts     map[string]*tableStat
		partsRows *sr.Rows
		partsErr  error
	}
	partsMu sync.Mutex
	sshOnce sync.Once
	sshOut  map[string]map[string]hostOut // host → cmd → 结果
	offsets map[string]float64
}

type hostOut struct{ Out, Err string }

// sshCommands 是系统环境规则需要的全部命令，每台主机一次连接执行完。
var sshCommands = []string{
	"cat /proc/sys/vm/swappiness",
	"cat /sys/kernel/mm/transparent_hugepage/enabled",
	"cat /proc/sys/vm/overcommit_memory",
	"cat /proc/sys/vm/max_map_count",
	"date +%s%3N",
}

// Hosts 返回集群全部节点主机（去重排序）。
func (e *Env) Hosts() []string {
	seen := map[string]bool{}
	var hs []string
	for _, n := range e.Topo.Nodes {
		if !seen[n.Host] {
			seen[n.Host] = true
			hs = append(hs, n.Host)
		}
	}
	sort.Strings(hs)
	return hs
}

func (e *Env) collectSSH() {
	e.sshOut, e.offsets = map[string]map[string]hostOut{}, map[string]float64{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, h := range e.Hosts() {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, off := e.runHost(h)
			mu.Lock()
			e.sshOut[h] = res
			if off != nil {
				e.offsets[h] = *off
			}
			mu.Unlock()
		}(h)
	}
	wg.Wait()
}

func (e *Env) runHost(h string) (map[string]hostOut, *float64) {
	res := map[string]hostOut{}
	ctx, cancel := context.WithTimeout(e.Ctx, 20*time.Second)
	defer cancel()
	s, err := e.Keys.Dial(ctx, sshx.Target{Host: h, Port: e.Cluster.SSH.Port, User: e.Cluster.SSH.User, Password: e.SSHPass})
	if err != nil {
		for _, c := range sshCommands {
			res[c] = hostOut{Err: err.Error()}
		}
		return res, nil
	}
	defer s.Close()
	var off *float64
	for _, c := range sshCommands {
		t0 := time.Now()
		r, err := s.Run(ctx, c)
		t1 := time.Now()
		switch {
		case err != nil:
			res[c] = hostOut{Err: err.Error()}
		case r.Code != 0:
			res[c] = hostOut{Err: strings.TrimSpace("退出码 " + strconv.Itoa(r.Code) + " " + r.Stderr)}
		default:
			res[c] = hostOut{Out: r.Stdout}
			if c == "date +%s%3N" {
				if ms, err := strconv.ParseInt(strings.TrimSpace(r.Stdout), 10, 64); err == nil {
					v := float64(ms - (t0.UnixMilli() + t1.Sub(t0).Milliseconds()/2))
					off = &v
				}
			}
		}
	}
	return res, off
}

// SSHAll 返回各主机执行 cmd 的结果（cmd 必须在 sshCommands 中）。
func (e *Env) SSHAll(cmd string) map[string]hostOut {
	e.sshOnce.Do(e.collectSSH)
	out := map[string]hostOut{}
	for h, m := range e.sshOut {
		out[h] = m[cmd]
	}
	return out
}

// ClockOffsets 返回各主机相对平台的时钟偏差（毫秒）。
func (e *Env) ClockOffsets() map[string]float64 {
	e.sshOnce.Do(e.collectSSH)
	return e.offsets
}
