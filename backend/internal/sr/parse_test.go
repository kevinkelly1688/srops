// @author: kevinkelly
// @license: Apache Licence
// @file: parse_test.go
// @time: 2026/10/28

package sr

import "testing"

func TestParseMetrics(t *testing.T) {
	m := ParseMetrics([]byte(`# TYPE starrocks_fe_qps gauge
starrocks_fe_qps 3812.5
starrocks_fe_query_latency_ms{quantile="0.99", type="all"} 2801
jvm_heap_size_bytes{type="used"} 1.4e+10
bad line
weird{a="x\"y"} 1
`))
	if v, ok := m.Get("starrocks_fe_qps"); !ok || v != 3812.5 {
		t.Fatalf("qps = %v %v", v, ok)
	}
	if v, ok := m.Get("starrocks_fe_query_latency_ms", "quantile", "0.99"); !ok || v != 2801 {
		t.Fatalf("p99 = %v %v", v, ok)
	}
	if v, ok := m.Get("jvm_heap_size_bytes", "type", "used"); !ok || v != 1.4e10 {
		t.Fatalf("heap = %v", v)
	}
	if _, ok := m.Get("weird", "a", `x"y`); !ok {
		t.Fatal("转义的标签值未解析")
	}
}

func TestParseSizeAndPct(t *testing.T) {
	for in, want := range map[string]float64{"1.5 GB": 1.5 * (1 << 30), "85MB": 85 << 20, "512 KB": 512 << 10, "1024": 1024, "": 0, "x": 0, "3.100 TB": 3.1 * (1 << 40)} {
		if got := ParseSize(in); got != want {
			t.Errorf("ParseSize(%q) = %v，期望 %v", in, got, want)
		}
	}
	if p := pct("87.12 %"); p == nil || *p != 87.12 {
		t.Fatalf("pct = %v", p)
	}
	if pct("") != nil || pct("N/A") != nil {
		t.Fatal("空值应返回 nil")
	}
}
