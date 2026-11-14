// @author: kevinkelly
// @license: Apache Licence
// @file: sampler.go
// @time: 2026/10/28

package collect

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

const keepWindow = 60 * 60 // 秒：保留最近 60 分钟

// Sampler 在未配置 Prometheus 时，由平台每 15 秒抓取各 FE 的 /metrics，保留最近 60 分钟的 QPS 与延迟趋势（仅内存）。
type Sampler struct {
	every  atomic.Int64 // 采样间隔，秒
	m      *Manager
	mu     sync.Mutex
	series map[string][]model.Point
	stops  map[string]context.CancelFunc
}

func newSampler(m *Manager) *Sampler {
	s := &Sampler{m: m, series: map[string][]model.Point{}, stops: map[string]context.CancelFunc{}}
	s.every.Store(15)
	return s
}

// SetInterval 设置采样间隔（平台设置 → 基础 → 指标采集间隔），下一轮起生效。
func (s *Sampler) SetInterval(sec int) {
	if sec >= 5 {
		s.every.Store(int64(sec))
	}
}

// Watch 开始（或重新开始）采样一个集群。
func (s *Sampler) Watch(c model.Cluster) {
	s.Stop(c.ID)
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.stops[c.ID] = cancel
	s.mu.Unlock()
	go s.loop(ctx, c)
}

// Stop 停止采样并丢弃数据。
func (s *Sampler) Stop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.stops[id]; ok {
		f()
		delete(s.stops, id)
	}
	delete(s.series, id)
}

func (s *Sampler) Points(id string) []model.Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.Point(nil), s.series[id]...)
}

func (s *Sampler) loop(ctx context.Context, c model.Cluster) {
	var fes []model.Node
	var refreshed time.Time
	for {
		if cl, err := s.m.Client(ctx, c); err == nil {
			if time.Since(refreshed) > 5*time.Minute || len(fes) == 0 {
				if r, err := cl.Query(ctx, "SHOW FRONTENDS"); err == nil {
					fes = fes[:0]
					for i := range r.Data {
						fes = append(fes, model.Node{ID: r.Get(i, "IP") + ":" + r.Get(i, "HttpPort"), Host: r.Get(i, "IP"), HTTP: atoi(r.Get(i, "HttpPort")), Alive: r.Get(i, "Alive") == "true"})
					}
					refreshed = time.Now()
				}
			}
			p := FEPoint(NodeMetrics(ctx, cl, fes))
			p.T = time.Now().Unix()
			s.mu.Lock()
			if _, ok := s.stops[c.ID]; ok {
				ser := append(s.series[c.ID], p)
				i := 0
				for i < len(ser)-1 && ser[i].T < p.T-keepWindow {
					i++
				}
				s.series[c.ID] = ser[i:]
			}
			s.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(s.every.Load()) * time.Second):
		}
	}
}

// FEPoint 汇总多个 FE 的指标：QPS 求和，延迟取最大。
func FEPoint(mt map[string]sr.Metrics) model.Point {
	var p model.Point
	var qps, p99, p95 float64
	var has1, has2, has3 bool
	for _, m := range mt {
		if v, ok := m.Get("starrocks_fe_qps"); ok {
			qps += v
			has1 = true
		}
		if v, ok := m.Get("starrocks_fe_query_latency_ms", "quantile", "0.99"); ok {
			if v > p99 {
				p99 = v
			}
			has2 = true
		}
		if v, ok := m.Get("starrocks_fe_query_latency_ms", "quantile", "0.95"); ok {
			if v > p95 {
				p95 = v
			}
			has3 = true
		}
	}
	if has1 {
		p.QPS = &qps
	}
	if has2 {
		p.P99 = &p99
	}
	if has3 {
		p.P95 = &p95
	}
	return p
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
