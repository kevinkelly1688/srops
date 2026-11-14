// @author: kevinkelly
// @license: Apache Licence
// @file: param.go
// @time: 2026/10/28

package sr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Param 是一个参数（FE 配置 / BE 配置 / 全局变量）。
type Param struct {
	Key      string            `json:"key"`
	Value    string            `json:"value"`
	Default  string            `json:"default,omitempty"`
	Type     string            `json:"type,omitempty"`
	Mutable  bool              `json:"mutable"`
	Comment  string            `json:"comment,omitempty"`
	Changed  bool              `json:"changed,omitempty"` // 变量：与默认值不同
	Diff     bool              `json:"diff,omitempty"`    // 各节点值不一致
	NodeVals map[string]string `json:"nodeValues,omitempty"`
}

var paramKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,127}$`)

// ValidParamKey 判断参数名是否安全。
func ValidParamKey(k string) bool { return paramKeyRe.MatchString(k) }

// FEParams 读取一个 FE 的全部配置项（ADMIN SHOW FRONTEND CONFIG）。
func (c *Client) FEParams(ctx context.Context) ([]Param, error) {
	r, err := c.Query(ctx, "ADMIN SHOW FRONTEND CONFIG")
	if err != nil {
		return nil, err
	}
	var out []Param
	for i := range r.Data {
		out = append(out, Param{Key: r.Get(i, "Key"), Value: r.Get(i, "Value"), Type: r.Get(i, "Type"), Mutable: isTrue(r.Get(i, "IsMutable")), Comment: r.Get(i, "Comment")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Variables 读取全局变量（SHOW GLOBAL VARIABLES）；新版本带有默认值与是否已修改。
func (c *Client) Variables(ctx context.Context) ([]Param, error) {
	r, err := c.Query(ctx, "SHOW GLOBAL VARIABLES")
	if err != nil {
		return nil, err
	}
	var out []Param
	for i := range r.Data {
		p := Param{Key: r.Get(i, "Variable_name"), Value: r.Get(i, "Value"), Mutable: true}
		if r.Has("Default_value") {
			p.Default = r.Get(i, "Default_value")
			p.Changed = isTrue(r.Get(i, "Is_changed"))
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// BEConfigs 读取各 BE / CN 的配置（information_schema.be_configs），按参数名汇总：Value 取出现最多的值，NodeVals 为各节点的值。
// ids 是节点 ID（BE_ID）到展示名的映射，缺失时直接用 ID。
func (c *Client) BEConfigs(ctx context.Context, names map[string]string) ([]Param, []string, error) {
	r, err := c.Query(ctx, "SELECT * FROM information_schema.be_configs")
	if err != nil {
		return nil, nil, err
	}
	by := map[string]*Param{}
	nodes := map[string]bool{}
	for i := range r.Data {
		k, node := r.Get(i, "NAME"), r.Get(i, "BE_ID")
		if n, ok := names[node]; ok {
			node = n
		}
		nodes[node] = true
		p := by[k]
		if p == nil {
			p = &Param{Key: k, Type: r.Get(i, "TYPE"), Mutable: isTrue(r.Get(i, "IS_MUTABLE")), NodeVals: map[string]string{}}
			by[k] = p
		}
		p.NodeVals[node] = r.Get(i, "VALUE")
	}
	var out []Param
	for _, p := range by {
		count := map[string]int{}
		for _, v := range p.NodeVals {
			count[v]++
		}
		best, bn := "", -1
		for v, n := range count {
			if n > bn || (n == bn && v < best) {
				best, bn = v, n
			}
		}
		p.Value, p.Diff = best, len(count) > 1
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	var ns []string
	for n := range nodes {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return out, ns, nil
}

// SetFEConfigSQL 生成 ADMIN SET FRONTEND CONFIG 语句。
func SetFEConfigSQL(key, value string) (string, error) {
	if !ValidParamKey(key) {
		return "", fmt.Errorf("参数名不合法")
	}
	v, err := Lit(value)
	if err != nil {
		return "", err
	}
	if len(value) > 1000 {
		return "", fmt.Errorf("参数值过长")
	}
	return fmt.Sprintf(`ADMIN SET FRONTEND CONFIG ("%s" = %s)`, key, v), nil
}

// SetVariableSQL 生成 SET GLOBAL 语句。
func SetVariableSQL(key, value string) (string, error) {
	if !ValidParamKey(key) || strings.Contains(key, ".") {
		return "", fmt.Errorf("变量名不合法")
	}
	if len(value) > 1000 {
		return "", fmt.Errorf("变量值过长")
	}
	v, err := Value(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SET GLOBAL %s = %s", key, v), nil
}

// SetBEConfig 通过 BE 的 HTTP 接口修改一个配置项（仅内存生效，重启后恢复）。
func SetBEConfig(ctx context.Context, host string, httpPort int, key, value string) error {
	if !ValidParamKey(key) {
		return fmt.Errorf("参数名不合法")
	}
	if len(value) > 1000 || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("参数值不合法")
	}
	u := fmt.Sprintf("http://%s/api/update_config?%s=%s", net.JoinHostPort(host, strconv.Itoa(httpPort)), key, urlEscape(value))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	cl := &http.Client{Timeout: 8 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	body := strings.TrimSpace(string(b))
	failed := resp.StatusCode != 200
	var js struct{ Status, Msg string }
	if json.Unmarshal(b, &js) == nil && js.Status != "" { // BE 返回 {"status":"OK","msg":""}
		failed = failed || !strings.EqualFold(js.Status, "ok")
		if js.Msg != "" {
			body = js.Status + "：" + js.Msg
		}
	} else if low := strings.ToLower(body); strings.Contains(low, "fail") || strings.Contains(low, "error") || strings.Contains(low, "not found") {
		failed = true
	}
	if failed {
		if body == "" {
			body = resp.Status
		}
		return fmt.Errorf("%s", body)
	}
	return nil
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
