// @author: kevinkelly
// @license: Apache Licence
// @file: metrics.go
// @time: 2026/10/28

package sr

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// Sample 是 Prometheus 文本格式中的一条样本。
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Metrics 是一个节点 /metrics 的解析结果。
type Metrics []Sample

// Get 返回指定名称、且标签全部匹配的第一个样本值。
func (m Metrics) Get(name string, labels ...string) (float64, bool) {
	for _, s := range m {
		if s.Name == name && match(s.Labels, labels) {
			return s.Value, true
		}
	}
	return 0, false
}

// Max 返回同名样本的最大值（忽略标签）。
func (m Metrics) Max(name string) (float64, bool) {
	v, ok := math.Inf(-1), false
	for _, s := range m {
		if s.Name == name && !math.IsNaN(s.Value) {
			v, ok = math.Max(v, s.Value), true
		}
	}
	return v, ok
}

func match(l map[string]string, kv []string) bool {
	for i := 0; i+1 < len(kv); i += 2 {
		if l[kv[i]] != kv[i+1] {
			return false
		}
	}
	return true
}

// ParseMetrics 解析 Prometheus 文本暴露格式（不支持的行直接跳过）。
func ParseMetrics(b []byte) Metrics {
	var out Metrics
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		s, ok := parseLine(line)
		if ok {
			out = append(out, s)
		}
	}
	return out
}

func parseLine(line string) (Sample, bool) {
	s := Sample{Labels: map[string]string{}}
	i := strings.IndexAny(line, "{ ")
	if i <= 0 {
		return s, false
	}
	s.Name = line[:i]
	rest := line[i:]
	if rest[0] == '{' {
		end, ok := parseLabels(rest, s.Labels)
		if !ok {
			return s, false
		}
		rest = rest[end:]
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return s, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return s, false
	}
	s.Value = v
	return s, true
}

// parseLabels 解析 {k="v",...}，返回右花括号之后的位置。
func parseLabels(s string, into map[string]string) (int, bool) {
	i := 1
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',') {
			i++
		}
		if i < len(s) && s[i] == '}' {
			return i + 1, true
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 || i+eq+1 >= len(s) || s[i+eq+1] != '"' {
			return 0, false
		}
		key := strings.TrimSpace(s[i : i+eq])
		i += eq + 2
		var val strings.Builder
		for i < len(s) && s[i] != '"' {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					val.WriteByte('\n')
				default:
					val.WriteByte(s[i])
				}
			} else {
				val.WriteByte(s[i])
			}
			i++
		}
		if i >= len(s) {
			return 0, false
		}
		into[key] = val.String()
		i++
	}
	return 0, false
}

func httpGet(ctx context.Context, c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回 HTTP %d", url, resp.StatusCode)
	}
	return b, nil
}

// NodeMetrics 抓取并解析某个节点的 /metrics。
func (c *Client) NodeMetrics(ctx context.Context, host string, httpPort int) (Metrics, error) {
	b, err := c.HTTPGet(ctx, host, httpPort, "/metrics")
	if err != nil {
		return nil, err
	}
	return ParseMetrics(b), nil
}
