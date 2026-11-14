// @author: kevinkelly
// @license: Apache Licence
// @file: metrics.go
// @time: 2026/10/28

package fakesr

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

const (
	feQPS       = 3800.0
	feHeapMax   = 16 << 30
	feQueryBase = 1_200_000_000 // 启动时的累计查询数
)

// jitter 返回 v 在 ±frac 范围内的随机抖动值。
func jitter(v, frac float64) float64 {
	return v * (1 + (rand.Float64()*2-1)*frac)
}

// promWriter 输出 Prometheus 文本格式。
type promWriter struct{ b strings.Builder }

func (w *promWriter) typ(name, t string) { fmt.Fprintf(&w.b, "# TYPE %s %s\n", name, t) }

func (w *promWriter) val(name, labels string, v float64) {
	if labels != "" {
		name += "{" + labels + "}"
	}
	fmt.Fprintf(&w.b, "%s %s\n", name, fmtFloat(v))
}

func (w *promWriter) gauge(name string, v float64) {
	w.typ(name, "gauge")
	w.val(name, "", v)
}

func fmtFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.3f", v)
}

func (c *Cluster) metricsHandler(n *node) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if n.down.Load() {
			http.Error(rw, "process is not running", http.StatusServiceUnavailable)
			return
		}
		c.nodeMux(n).ServeHTTP(rw, r)
	})
}

func (c *Cluster) nodeMux(n *node) http.Handler {
	mux := http.NewServeMux()
	if n.kind != kindFE {
		mux.HandleFunc("/api/update_config", c.updateConfigHandler(n))
	}
	mux.HandleFunc("/metrics", func(rw http.ResponseWriter, _ *http.Request) {
		var w promWriter
		switch n.kind {
		case kindFE:
			c.feMetrics(&w, n)
		case kindBE:
			c.beMetrics(&w, n)
		case kindCN:
			c.cnMetrics(&w, n)
		}
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = rw.Write([]byte(w.b.String()))
	})
	return mux
}

func (c *Cluster) feMetrics(w *promWriter, n *node) {
	elapsed := time.Since(c.startedAt).Seconds()
	total := float64(int64(feQueryBase + feQPS*elapsed))
	errRate := pick(c.problems, 0.004, 0.0005)

	w.gauge("starrocks_fe_qps", jitter(feQPS, 0.03))

	w.typ("starrocks_fe_query_total", "counter")
	w.val("starrocks_fe_query_total", "", total)
	w.typ("starrocks_fe_query_err", "counter")
	w.val("starrocks_fe_query_err", "", float64(int64(total*errRate)))

	p75, p95, p99 := 120.0, 400.0, 900.0
	if c.problems {
		p75, p95, p99 = 180.0, 900.0, 2800.0
	}
	const lat = "starrocks_fe_query_latency_ms"
	w.typ(lat, "summary")
	w.val(lat, `quantile="0.75"`, jitter(p75, 0.05))
	w.val(lat, `quantile="0.95"`, jitter(p95, 0.05))
	w.val(lat, `quantile="0.99"`, jitter(p99, 0.05))
	w.val(lat+"_sum", "", total*p75*0.6)
	w.val(lat+"_count", "", total)

	w.gauge("starrocks_fe_connection_total", float64(int(jitter(730, 0.03))))

	used := 0.5
	if c.problems && n.role == "LEADER" {
		used = 0.82
	}
	w.typ("jvm_heap_size_bytes", "gauge")
	w.val("jvm_heap_size_bytes", `type="max"`, feHeapMax)
	w.val("jvm_heap_size_bytes", `type="committed"`, feHeapMax)
	w.val("jvm_heap_size_bytes", `type="used"`, float64(int64(jitter(feHeapMax*used, 0.02))))

	if c.sharedDat {
		w.gauge("starrocks_fe_max_tablet_compaction_score", pick(c.problems, 186.0, 12.0))
	}
}

func (c *Cluster) beMetrics(w *promWriter, n *node) {
	st := c.beStat(n.idx)
	w.gauge("starrocks_be_tablet_cumulative_max_compaction_score", st.cumuScore)
	w.gauge("starrocks_be_tablet_base_max_compaction_score", st.baseScore)
	w.gauge("starrocks_be_process_mem_bytes", float64(int64(jitter(nodeMemBytes*st.memPct/100, 0.01))))
}

func (c *Cluster) cnMetrics(w *promWriter, n *node) {
	_, mem := c.cnStat(n.idx)
	w.gauge("starrocks_be_process_mem_bytes", float64(int64(jitter(nodeMemBytes*mem/100, 0.01))))
}
