// @author: kevinkelly
// @license: Apache Licence
// @file: prom.go
// @time: 2026/10/28

package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/model"
)

var promHTTP = &http.Client{Timeout: 8 * time.Second}

// promRange 执行 Prometheus query_range，返回 unix 秒 → 值（多条序列时取第一条）。
func promRange(ctx context.Context, base, q string, start, end time.Time, step time.Duration) (map[int64]float64, error) {
	u := strings.TrimRight(base, "/") + "/api/v1/query_range?" + url.Values{
		"query": {q}, "start": {strconv.FormatInt(start.Unix(), 10)}, "end": {strconv.FormatInt(end.Unix(), 10)},
		"step": {strconv.Itoa(int(step.Seconds()))},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := promHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("Prometheus 返回无法解析（HTTP %d）", resp.StatusCode)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("Prometheus 查询失败：%s", r.Error)
	}
	out := map[int64]float64{}
	if len(r.Data.Result) == 0 {
		return out, nil
	}
	for _, v := range r.Data.Result[0].Values {
		ts, _ := v[0].(float64)
		s, _ := v[1].(string)
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			out[int64(ts)] = f
		}
	}
	return out, nil
}

// PromSeries 是即时查询返回的一条序列。
type PromSeries struct {
	Labels map[string]string
	V      float64
}

// PromQuery 执行 Prometheus 即时查询（/api/v1/query），支持返回即时向量或标量；区间向量、字符串会报错。
func PromQuery(ctx context.Context, base, q string) ([]PromSeries, error) {
	u := strings.TrimRight(base, "/") + "/api/v1/query?" + url.Values{"query": {q}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := promHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接 Prometheus 失败：%v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var r struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("Prometheus 返回无法解析（HTTP %d）", resp.StatusCode)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("Prometheus 查询失败：%s", r.Error)
	}
	parse := func(v [2]any) (float64, bool) {
		s, _ := v[1].(string)
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	}
	switch r.Data.ResultType {
	case "vector":
		var vec []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(r.Data.Result, &vec); err != nil {
			return nil, fmt.Errorf("Prometheus 返回无法解析")
		}
		out := make([]PromSeries, 0, len(vec))
		for _, x := range vec {
			if f, ok := parse(x.Value); ok {
				out = append(out, PromSeries{Labels: x.Metric, V: f})
			}
		}
		return out, nil
	case "scalar":
		var v [2]any
		if err := json.Unmarshal(r.Data.Result, &v); err != nil {
			return nil, fmt.Errorf("Prometheus 返回无法解析")
		}
		if f, ok := parse(v); ok {
			return []PromSeries{{Labels: map[string]string{}, V: f}}, nil
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("表达式返回的类型是 %s，需要即时向量或标量（区间向量请套上 rate()、avg_over_time() 等函数）", r.Data.ResultType)
	}
}

// PromTrend 从 Prometheus 读取最近 60 分钟 QPS / P99 / P95。selector 例如 job="StarRocks_prod"。
func PromTrend(ctx context.Context, c model.Cluster) ([]model.Point, error) {
	sel := ""
	if strings.TrimSpace(c.PromSelector) != "" {
		sel = "{" + strings.Trim(strings.TrimSpace(c.PromSelector), "{}") + "}"
	}
	lat := func(q string) string {
		s := `quantile="` + q + `"`
		if sel != "" {
			s = strings.Trim(sel, "{}") + "," + s
		}
		return "max(starrocks_fe_query_latency_ms{" + s + "})"
	}
	end := time.Now().Truncate(time.Minute)
	start, step := end.Add(-60*time.Minute), time.Minute
	qps, err := promRange(ctx, c.Prom, "sum(starrocks_fe_qps"+sel+")", start, end, step)
	if err != nil {
		return nil, err
	}
	p99, _ := promRange(ctx, c.Prom, lat("0.99"), start, end, step)
	p95, _ := promRange(ctx, c.Prom, lat("0.95"), start, end, step)
	var pts []model.Point
	for t := start; !t.After(end); t = t.Add(step) {
		p := model.Point{T: t.Unix()}
		if v, ok := qps[t.Unix()]; ok {
			p.QPS = &v
		}
		if v, ok := p99[t.Unix()]; ok {
			p.P99 = &v
		}
		if v, ok := p95[t.Unix()]; ok {
			p.P95 = &v
		}
		pts = append(pts, p)
	}
	return pts, nil
}
