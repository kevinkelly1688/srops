// @author: kevinkelly
// @license: Apache Licence
// @file: rg.go
// @time: 2026/10/28

package sr

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Classifier 是资源组的一条分类器（各条件之间为“与”，多个分类器之间为“或”）。
type Classifier struct {
	ID        string   `json:"id,omitempty"`
	User      string   `json:"user,omitempty"`
	Role      string   `json:"role,omitempty"`
	QueryType []string `json:"queryType,omitempty"` // select / insert / mv / update / delete
	SourceIP  string   `json:"sourceIp,omitempty"`
	DB        string   `json:"db,omitempty"`
}

// Key 是分类器内容的规范化形式，用于比较（忽略 ID）。
func (c Classifier) Key() string {
	qt := append([]string(nil), c.QueryType...)
	for i := range qt {
		qt[i] = strings.ToLower(qt[i])
	}
	sort.Strings(qt)
	return strings.Join([]string{c.User, c.Role, strings.Join(qt, ","), c.SourceIP, c.DB}, "|")
}

// ResourceGroup 是 SHOW RESOURCE GROUPS 的一行。
type ResourceGroup struct {
	Name        string       `json:"name"`
	ID          string       `json:"id"`
	CPUWeight   int          `json:"cpuWeight"`
	Exclusive   int          `json:"exclusiveCpuCores"`
	MemPct      float64      `json:"memPct"` // 0 表示未限制
	Concurrency int          `json:"concurrency"`
	BigCPUSec   int64        `json:"bigCpuSec"`
	BigScanRows int64        `json:"bigScanRows"`
	BigMem      int64        `json:"bigMem"`
	SpillPct    float64      `json:"spillPct"`
	Type        string       `json:"type"`
	Builtin     bool         `json:"builtin"`
	Classifiers []Classifier `json:"classifiers"`
}

func atoiS(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func atoi64S(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// pctOf 解析 "40.0%" / "0.4" / "40" 为百分数（40）。
func pctOf(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "%") {
		f, _ := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		return f
	}
	f, _ := strconv.ParseFloat(s, 64)
	if f <= 1 {
		return f * 100
	}
	return f
}

var clsGroup = regexp.MustCompile(`\((?:[^()]|\([^()]*\))*\)`)

// ParseClassifiers 解析 SHOW RESOURCE GROUPS 的 classifiers 列：
// "(id=10012, weight=1.0, user=etl, query_type in (SELECT,INSERT), source_ip=10.0.%)"，多个分类器用换行或逗号分隔。
func ParseClassifiers(s string) []Classifier {
	var out []Classifier
	for _, g := range clsGroup.FindAllString(s, -1) {
		g = strings.TrimSpace(g[1 : len(g)-1])
		var c Classifier
		for _, part := range splitTop(g) {
			part = strings.TrimSpace(part)
			low := strings.ToLower(part)
			switch {
			case strings.HasPrefix(low, "query_type in"):
				in := part[strings.Index(part, "(")+1:]
				in = strings.TrimSuffix(strings.TrimSpace(in), ")")
				for _, q := range strings.Split(in, ",") {
					if q = strings.ToLower(strings.Trim(strings.TrimSpace(q), `'"`)); q != "" {
						c.QueryType = append(c.QueryType, q)
					}
				}
			default:
				k, v, ok := strings.Cut(part, "=")
				if !ok {
					continue
				}
				k, v = strings.ToLower(strings.TrimSpace(k)), strings.Trim(strings.TrimSpace(v), `'"`)
				switch k {
				case "id":
					c.ID = v
				case "user":
					c.User = v
				case "role":
					c.Role = v
				case "source_ip":
					c.SourceIP = v
				case "db":
					c.DB = v
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// splitTop 按顶层逗号切分（括号内的逗号不切）。
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// ResourceGroups 读取全部资源组（含内置的 default_wg / default_mv_wg）。
func (c *Client) ResourceGroups(ctx context.Context) ([]ResourceGroup, error) {
	r, err := c.Query(ctx, "SHOW RESOURCE GROUPS ALL")
	if err != nil {
		return nil, err
	}
	var out []ResourceGroup
	for i := range r.Data {
		g := ResourceGroup{
			Name: r.Get(i, "name"), ID: r.Get(i, "id"), CPUWeight: atoiS(r.Get(i, "cpu_weight")), Exclusive: atoiS(r.Get(i, "exclusive_cpu_cores")),
			MemPct: pctOf(r.Get(i, "mem_limit")), Concurrency: atoiS(r.Get(i, "concurrency_limit")), BigCPUSec: atoi64S(r.Get(i, "big_query_cpu_second_limit")),
			BigScanRows: atoi64S(r.Get(i, "big_query_scan_rows_limit")), BigMem: atoi64S(r.Get(i, "big_query_mem_limit")),
			SpillPct: pctOf(r.Get(i, "spill_mem_limit_threshold")), Type: r.Get(i, "type"), Classifiers: ParseClassifiers(r.Get(i, "classifiers")),
		}
		g.Builtin = g.Name == "default_wg" || g.Name == "default_mv_wg" || strings.EqualFold(g.Type, "short_query") && g.ID == "0"
		if g.Name == "" {
			continue
		}
		out = append(out, g)
	}
	return out, nil
}

// RGSpec 是新建 / 修改资源组的用户输入。
type RGSpec struct {
	Name        string       `json:"name"`
	CPUWeight   int          `json:"cpuWeight"`
	Exclusive   int          `json:"exclusiveCpuCores"`
	MemPct      int          `json:"memPct"`
	Concurrency int          `json:"concurrency"`
	BigCPUSec   int64        `json:"bigCpuSec"`
	BigScanRows int64        `json:"bigScanRows"`
	BigMem      int64        `json:"bigMem"`
	SpillPct    int          `json:"spillPct"`
	Classifiers []Classifier `json:"classifiers"`
}

var (
	userRe  = regexp.MustCompile(`^[A-Za-z0-9_.@\-]{1,64}$`)
	ipRe    = regexp.MustCompile(`^[0-9A-Fa-f:.%/]{1,64}$`)
	qtypeOK = map[string]bool{"select": true, "insert": true, "mv": true, "update": true, "delete": true}
)

// Validate 校验输入；create 为 true 时名称必须合法。
func (s *RGSpec) Validate(create bool) error {
	if create {
		if !ValidIdent(s.Name) {
			return fmt.Errorf("资源组名称只能包含字母、数字、下划线，且以字母或下划线开头（最长 64）")
		}
		if s.Name == "default_wg" || s.Name == "default_mv_wg" {
			return fmt.Errorf("%s 是系统内置资源组，不能使用该名称", s.Name)
		}
	}
	if (s.CPUWeight > 0) == (s.Exclusive > 0) {
		return fmt.Errorf("CPU 权重（cpu_weight）与独占核数（exclusive_cpu_cores）必须且只能设置其中一个（大于 0）")
	}
	if s.CPUWeight < 0 || s.CPUWeight > 65535 || s.Exclusive < 0 || s.Exclusive > 1024 {
		return fmt.Errorf("CPU 权重 / 独占核数超出范围")
	}
	if s.MemPct < 0 || s.MemPct > 100 {
		return fmt.Errorf("内存上限须为 0–100 的百分比（0 表示不限制）")
	}
	if s.Concurrency < 0 || s.BigCPUSec < 0 || s.BigScanRows < 0 || s.BigMem < 0 {
		return fmt.Errorf("并发上限与大查询熔断阈值不能为负数")
	}
	if s.SpillPct < 0 || s.SpillPct > 100 {
		return fmt.Errorf("落盘阈值须为 0–100 的百分比（0 表示不设置）")
	}
	if len(s.Classifiers) == 0 {
		return fmt.Errorf("至少需要一个分类器，否则没有查询会进入该资源组")
	}
	for i := range s.Classifiers {
		c := &s.Classifiers[i]
		c.ID = ""
		if c.User != "" && !userRe.MatchString(c.User) {
			return fmt.Errorf("分类器 %d：用户名不合法", i+1)
		}
		if c.Role != "" && !userRe.MatchString(c.Role) {
			return fmt.Errorf("分类器 %d：角色名不合法", i+1)
		}
		if c.SourceIP != "" && !ipRe.MatchString(c.SourceIP) {
			return fmt.Errorf("分类器 %d：来源 IP 须为 IP 或 CIDR（可含 %% 通配）", i+1)
		}
		if c.DB != "" && !ValidIdent(c.DB) {
			return fmt.Errorf("分类器 %d：数据库名不合法", i+1)
		}
		for j, q := range c.QueryType {
			q = strings.ToLower(strings.TrimSpace(q))
			if !qtypeOK[q] {
				return fmt.Errorf("分类器 %d：query_type 只支持 select / insert / mv / update / delete", i+1)
			}
			c.QueryType[j] = q
		}
		if c.User == "" && c.Role == "" && c.SourceIP == "" && c.DB == "" && len(c.QueryType) == 0 {
			return fmt.Errorf("分类器 %d 没有任何条件", i+1)
		}
	}
	return nil
}

func (c Classifier) sql() string {
	var p []string
	add := func(k, v string) {
		if v != "" {
			p = append(p, k+"="+MustLit(v))
		}
	}
	add("user", c.User)
	add("role", c.Role)
	if len(c.QueryType) > 0 {
		q := make([]string, len(c.QueryType))
		for i, x := range c.QueryType {
			q[i] = MustLit(x)
		}
		p = append(p, "query_type in ("+strings.Join(q, ", ")+")")
	}
	add("source_ip", c.SourceIP)
	add("db", c.DB)
	return "(" + strings.Join(p, ", ") + ")"
}

func clsList(cs []Classifier) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = c.sql()
	}
	return strings.Join(s, ", ")
}

// props 生成 WITH (...) 中的属性。
func (s RGSpec) props() string {
	kv := func(k string, v any) string { return fmt.Sprintf(`"%s" = "%v"`, k, v) }
	p := []string{kv("cpu_weight", s.CPUWeight), kv("exclusive_cpu_cores", s.Exclusive)}
	mem := "0"
	if s.MemPct > 0 {
		mem = fmt.Sprintf("%d%%", s.MemPct)
	}
	p = append(p, kv("mem_limit", mem), kv("concurrency_limit", s.Concurrency),
		kv("big_query_cpu_second_limit", s.BigCPUSec), kv("big_query_scan_rows_limit", s.BigScanRows), kv("big_query_mem_limit", s.BigMem))
	if s.SpillPct > 0 {
		p = append(p, kv("spill_mem_limit_threshold", fmt.Sprintf("%.2f", float64(s.SpillPct)/100)))
	}
	return strings.Join(p, ", ")
}

// CreateSQL 生成 CREATE RESOURCE GROUP 语句。
func (s RGSpec) CreateSQL() string {
	return fmt.Sprintf("CREATE RESOURCE GROUP `%s` TO %s WITH (%s)", s.Name, clsList(s.Classifiers), s.props())
}

// AlterSQL 生成修改资源组所需的语句序列：先改属性，再添加新分类器，最后删除不再需要的旧分类器。
func (s RGSpec) AlterSQL(old ResourceGroup) []string {
	out := []string{fmt.Sprintf("ALTER RESOURCE GROUP `%s` WITH (%s)", s.Name, s.props())}
	oldKeys, newKeys := map[string]bool{}, map[string]bool{}
	for _, c := range old.Classifiers {
		oldKeys[c.Key()] = true
	}
	var add []Classifier
	for _, c := range s.Classifiers {
		newKeys[c.Key()] = true
		if !oldKeys[c.Key()] {
			add = append(add, c)
		}
	}
	if len(add) > 0 {
		out = append(out, fmt.Sprintf("ALTER RESOURCE GROUP `%s` ADD %s", s.Name, clsList(add)))
	}
	var drop []string
	for _, c := range old.Classifiers {
		if !newKeys[c.Key()] && c.ID != "" {
			drop = append(drop, c.ID)
		}
	}
	if len(drop) > 0 {
		out = append(out, fmt.Sprintf("ALTER RESOURCE GROUP `%s` DROP (%s)", s.Name, strings.Join(drop, ", ")))
	}
	return out
}

// SpecOf 把现有资源组还原成可编辑的输入。
func SpecOf(g ResourceGroup) RGSpec {
	return RGSpec{Name: g.Name, CPUWeight: g.CPUWeight, Exclusive: g.Exclusive, MemPct: int(g.MemPct + 0.5), Concurrency: g.Concurrency,
		BigCPUSec: g.BigCPUSec, BigScanRows: g.BigScanRows, BigMem: g.BigMem, SpillPct: int(g.SpillPct + 0.5), Classifiers: g.Classifiers}
}
