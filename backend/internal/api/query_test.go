// @author: kevinkelly
// @license: Apache Licence
// @file: query_test.go
// @time: 2026/10/28

package api

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"starrocks-ops/internal/sr"
)

func opsEnvVer(t *testing.T, scenario, version string) (*env, string) {
	e := newEnvVer(t, "shared-nothing", scenario, version)
	e.login()
	cl := e.conn()
	cl["name"], cl["env"], cl["owner"] = "prod-test", "生产", "admin"
	var out struct{ ID string }
	e.expect(200, "POST", "/api/clusters", cl, &out)
	return e, "/api/clusters/" + out.ID
}

type qList struct {
	Items    []sr.RunningQuery `json:"items"`
	Warnings []string          `json:"warnings"`
	Global   bool              `json:"global"`
}

func (l qList) find(qid string) (sr.RunningQuery, bool) {
	for _, q := range l.Items {
		if q.QueryID == qid {
			return q, true
		}
	}
	return sr.RunningQuery{}, false
}

const qidLong = "7f1c2a90-aaaa-4b1e-9d11-000000000003"

func TestRunningQueriesAndKill(t *testing.T) {
	for _, ver := range []string{"3.5.6", "3.1.9"} { // 3.1 没有 global_current_queries，改为逐台 FE 读取
		t.Run(ver, func(t *testing.T) {
			e, base := opsEnvVer(t, "problems", ver)
			var l qList
			e.expect(200, "GET", base+"/queries", nil, &l)
			if l.Global != (ver == "3.5.6") {
				t.Fatalf("global 标记不对：%v", l.Global)
			}
			q, ok := l.find(qidLong)
			if len(l.Items) != 3 || !ok || q.User != "adhoc" || q.DB != "dws" || q.ExecMs != 612000 || q.FE != "127.0.0.1" || !strings.Contains(q.SQL, "JOIN dim.dim_sku") || q.ConnID != "1003" {
				t.Fatalf("运行中的查询解析不对：%+v", l.Items)
			}
			if ver == "3.5.6" && q.ScanBytes != "820 GB" {
				t.Fatalf("扫描量：%+v", q)
			}

			// 参数校验与不存在的查询
			e.expect(400, "POST", base+"/queries/kill", map[string]any{"queryId": "x'; DROP"}, nil)
			e.expect(404, "POST", base+"/queries/kill", map[string]any{"queryId": "7f1c2a90-aaaa-4b1e-9d11-0000000000aa"}, nil)
			e.expect(409, "POST", base+"/queries/kill", map[string]any{"queryId": qidLong, "connectionId": "9999"}, nil) // 连接与查询对不上，拒绝执行
			e.expect(200, "GET", base+"/queries", nil, &l)
			if len(l.Items) != 3 {
				t.Fatal("对不上的终止请求不应终止任何查询")
			}

			e.expect(200, "POST", base+"/queries/kill", map[string]any{"queryId": qidLong, "fe": "127.0.0.1", "connectionId": "1003"}, nil)
			l = qList{}
			e.expect(200, "GET", base+"/queries", nil, &l)
			if _, ok := l.find(qidLong); ok || len(l.Items) != 2 {
				t.Fatalf("终止后应只剩 2 个查询：%+v", l.Items)
			}
			if a := lastAudit(t, e, "query.kill"); !strings.Contains(a.SQL, "KILL QUERY 1003") || !strings.Contains(a.Act, qidLong) || !strings.Contains(a.Act, "adhoc") {
				t.Fatalf("审计：%+v", a)
			}
			e.expect(404, "POST", base+"/queries/kill", map[string]any{"queryId": qidLong}, nil) // 已结束
		})
	}
}

func TestProfileAndExplain(t *testing.T) {
	e, base := opsEnv(t, "problems")
	var p struct{ Profile, Analysis string }
	e.expect(200, "GET", base+"/queries/7f1c2a90-aaaa-4b1e-9d11-0000000000ff/profile", nil, &p)
	if !strings.Contains(p.Profile, "QueryPeakMemoryUsagePerNode") || !strings.Contains(p.Analysis, "OLAP_SCAN") {
		t.Fatalf("Profile：%+v", p)
	}
	e.expect(502, "GET", base+"/queries/7f1c2a90-aaaa-4b1e-9d11-0000000000aa/profile", nil, nil) // 没有 Profile
	e.expect(400, "GET", base+"/queries/bad%27id/profile", nil, nil)
	if a := lastAudit(t, e, "query.profile"); a.ResClass != "失败" {
		t.Fatalf("取不到 Profile 应记为失败：%+v", a)
	}

	var x struct{ Plan string }
	e.expect(200, "POST", base+"/explain", map[string]any{"sql": "SELECT shop_id, SUM(amount) FROM dws.fact_order GROUP BY shop_id;", "costs": true}, &x)
	if !strings.Contains(x.Plan, "cardinality=320000000") || !strings.Contains(x.Plan, "OlapScanNode") {
		t.Fatalf("执行计划：%s", x.Plan)
	}
	e.expect(200, "POST", base+"/explain", map[string]any{"sql": "-- hi\nWITH a AS (SELECT 1) SELECT * FROM a"}, &x)
	if strings.Contains(x.Plan, "cardinality") {
		t.Fatal("非 COSTS 不应有基数信息")
	}
	for name, sql := range map[string]string{"空": " ", "DDL": "DROP TABLE dws.fact_order", "多语句": "SELECT 1; DROP TABLE t", "INSERT": "INSERT INTO t SELECT 1",
		"过长": "SELECT " + strings.Repeat("1,", 11000) + "1", "SET": "SET GLOBAL x = 1"} {
		if code := e.call("POST", base+"/explain", map[string]any{"sql": sql}, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	e.expect(502, "POST", base+"/explain", map[string]any{"sql": "SELECT * FROM no_such_table"}, nil)
	if a := lastAudit(t, e, "query.explain"); a.ResClass != "失败" || !strings.Contains(a.SQL, "EXPLAIN SELECT * FROM no_such_table") {
		t.Fatalf("审计：%+v", a)
	}
}

func TestSlowQueriesAndSQLAudit(t *testing.T) {
	e, base := opsEnv(t, "problems")
	var sq struct{ Items []sr.SlowQuery }
	e.expect(200, "GET", base+"/slow-queries?minutes=1440&minMs=5000", nil, &sq)
	// 4 个指纹中耗时 ≥5s 的：d-c3a9(adhoc ×2)、d-8f21(bi_user ×2)、d-e517
	if len(sq.Items) != 3 || sq.Items[0].Digest != "d-c3a9" || sq.Items[0].Count != 2 || sq.Items[0].MaxMs != 612000 || sq.Items[0].AvgMs != 309550 || sq.Items[0].QueryID != "a0000002" || sq.Items[0].MemBytes != 66571993088 {
		t.Fatalf("慢查询聚合不对：%+v", sq.Items)
	}
	e.expect(200, "GET", base+"/slow-queries?minutes=1440&minMs=5000&user=etl_user", nil, &sq)
	if len(sq.Items) != 1 || sq.Items[0].Digest != "d-e517" {
		t.Fatalf("用户过滤：%+v", sq.Items)
	}
	e.expect(200, "GET", base+"/slow-queries?minutes=1440&minMs=5000&q=dim_sku", nil, &sq) // 关键字（下划线按字面匹配）
	if len(sq.Items) != 1 || sq.Items[0].Digest != "d-c3a9" {
		t.Fatalf("关键字过滤：%+v", sq.Items)
	}
	e.expect(200, "GET", base+"/slow-queries?minutes=1440&minMs=5000&q=dim%25sku", nil, &sq) // % 不应被当作通配符
	if len(sq.Items) != 0 {
		t.Fatalf("%% 应按字面匹配：%+v", sq.Items)
	}
	for _, bad := range []string{"minutes=0", "minutes=99999", "minutes=abc", "user=a%22b", "minMs=-1"} {
		if code := e.call("GET", base+"/slow-queries?"+bad, nil, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", bad, code)
		}
	}

	// 导出 CSV：有 BOM、公式注入防护、审计
	req, _ := http.NewRequest("GET", e.srv.URL+base+"/slow-queries/export?minutes=1440&minMs=5000", nil)
	req.Header.Set("X-Requested-With", "sr-ops")
	resp, err := e.c.Do(req)
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(body), "\xEF\xBB\xBF") || !strings.Contains(string(body), "d-c3a9") || !strings.Contains(resp.Header.Get("Content-Disposition"), "slow_sql_") {
		t.Fatalf("导出：%d %q", resp.StatusCode, body)
	}
	if a := lastAudit(t, e, "query.export"); a.ResClass != "成功" || !strings.Contains(a.SQL, "starrocks_audit_tbl__") {
		t.Fatalf("导出审计：%+v", a)
	}

	// SQL 审计明细：筛选 + 分页
	var al struct {
		Items []sr.AuditRow
		More  bool
	}
	e.expect(200, "GET", base+"/sql-audit?minutes=1440&limit=3", nil, &al)
	if len(al.Items) != 3 || !al.More || al.Items[0].QueryID != "a0000001" || al.Items[0].RG != "rg_bi" {
		t.Fatalf("第一页：%+v", al)
	}
	e.expect(200, "GET", base+"/sql-audit?minutes=1440&limit=3&offset=6", nil, &al)
	if len(al.Items) != 1 || al.More || al.Items[0].QueryID != "a0000007" {
		t.Fatalf("最后一页：%+v", al)
	}
	al.Items = nil
	e.expect(200, "GET", base+"/sql-audit?minutes=1440&state=ERR", nil, &al)
	if len(al.Items) != 1 || al.Items[0].ErrorCode != "1064" {
		t.Fatalf("状态过滤：%+v", al.Items)
	}
	al.Items = nil
	e.expect(200, "GET", base+"/sql-audit?minutes=1440&user=bi_user&db=dws", nil, &al)
	if len(al.Items) != 3 {
		t.Fatalf("用户 + 库过滤：%+v", al.Items)
	}
	e.expect(400, "GET", base+"/sql-audit?minutes=60&state=DROP", nil, nil)
	e.expect(400, "GET", base+"/sql-audit?minutes=60&db=a.b", nil, nil)
	e.expect(400, "GET", base+"/sql-audit?minutes=60&offset=-5", nil, nil)

	// 权限：值班只读有查询分析查看权限但没有 audit.sql / query.export
	e.newUser("oncall2", []string{"r_oncall"}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall2", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	oc.expect(200, "GET", base+"/slow-queries?minutes=60", nil, nil)
	oc.expect(200, "GET", base+"/queries", nil, nil)
	oc.expect(403, "GET", base+"/sql-audit?minutes=60", nil, nil)
	oc.expect(403, "GET", base+"/slow-queries/export?minutes=60", nil, nil)
	oc.expect(403, "POST", base+"/queries/kill", map[string]any{"queryId": qidLong}, nil)
	oc.expect(403, "POST", base+"/explain", map[string]any{"sql": "select 1"}, nil)
	oc.expect(200, "GET", base+"/queries/7f1c2a90-aaaa-4b1e-9d11-0000000000ff/profile", nil, nil) // 值班只读有 query.profile
}

func TestSQLBlacklist(t *testing.T) {
	e, base := opsEnv(t, "problems")
	var bl struct {
		Items   []sr.BlackItem
		Enabled bool
		Known   bool
	}
	e.expect(200, "GET", base+"/blacklist", nil, &bl)
	if len(bl.Items) != 0 || !bl.Enabled || !bl.Known {
		t.Fatalf("初始：%+v", bl)
	}
	e.expect(200, "POST", base+"/blacklist", map[string]any{"pattern": `select \* from dws\.big_table.*`}, nil)
	e.expect(200, "POST", base+"/blacklist", map[string]any{"pattern": `drop table "x"`}, nil) // 含引号：转义后写入
	bl.Items = nil
	e.expect(200, "GET", base+"/blacklist", nil, &bl)
	if len(bl.Items) != 2 || bl.Items[0].Index != "1" || bl.Items[0].Pattern != `select \* from dws\.big_table.*` || bl.Items[1].Pattern != `drop table "x"` {
		t.Fatalf("添加后（正则经转义写入、读回应与输入一致）：%+v", bl)
	}
	if a := lastAudit(t, e, "query.blacklist"); !strings.Contains(a.SQL, "ADD SQLBLACKLIST ") || a.ResClass != "成功" {
		t.Fatalf("审计：%+v", a)
	}
	for name, p := range map[string]string{"空": " ", "全部拦截": ".*", "匹配常见语句": "select.*", "不合法": "(unclosed", "换行": "a\nb", "过长": strings.Repeat("a", 1001), "show": "show\\s+databases"} {
		if code := e.call("POST", base+"/blacklist", map[string]any{"pattern": p}, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	e.expect(400, "DELETE", base+"/blacklist/abc", nil, nil)
	e.expect(502, "DELETE", base+"/blacklist/99", nil, nil)
	e.expect(200, "DELETE", base+"/blacklist/1", nil, nil)
	bl.Items = nil
	e.expect(200, "GET", base+"/blacklist", nil, &bl)
	if len(bl.Items) != 1 || !strings.Contains(bl.Items[0].Pattern, "drop table") {
		t.Fatalf("删除后：%+v", bl)
	}

	// 双人复核：添加与删除都先进入待复核，批准后按原请求方法执行
	e.expect(200, "PUT", "/api/settings/security", map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": true, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}, nil)
	e.newUser("admin3", []string{"r_admin"}, "Admin3@2026xyzab")
	admin3 := e.firstLogin("admin3", "Admin3@2026xyzab", "Admin3@2026xyzab2")
	var pend map[string]any
	e.expect(202, "POST", base+"/blacklist", map[string]any{"pattern": `select \* from dws\.other_big`}, &pend)
	ridAdd := pend["reviewId"].(string)
	e.expect(202, "DELETE", base+"/blacklist/1", nil, &pend)
	ridDel := pend["reviewId"].(string)
	e.expect(202, "POST", base+"/queries/kill", map[string]any{"queryId": qidLong, "connectionId": "1003"}, &pend)
	ridKill := pend["reviewId"].(string)
	bl.Items = nil
	e.expect(200, "GET", base+"/blacklist", nil, &bl)
	if len(bl.Items) != 1 {
		t.Fatalf("待复核期间不应修改黑名单：%+v", bl)
	}
	var rv struct{ Items []reviewOut }
	e.expect(200, "GET", "/api/reviews", nil, &rv)
	titles := map[string]bool{}
	for _, it := range rv.Items {
		titles[it.Title] = true
	}
	for _, want := range []string{`添加 SQL 黑名单规则 select \* from dws\.other_big`, "删除 SQL 黑名单规则 #1", "终止查询 " + qidLong} {
		if !titles[want] {
			t.Errorf("缺少复核标题 %q：%v", want, titles)
		}
	}
	var ap map[string]any
	admin3.expect(200, "POST", "/api/reviews/"+ridAdd+"/approve", nil, &ap)
	admin3.expect(200, "POST", "/api/reviews/"+ridDel+"/approve", nil, &ap)
	admin3.expect(200, "POST", "/api/reviews/"+ridKill+"/approve", nil, &ap)
	if ap["ok"] != true {
		t.Fatalf("批准执行失败：%v", ap)
	}
	bl.Items = nil
	e.expect(200, "GET", base+"/blacklist", nil, &bl)
	if len(bl.Items) != 1 || !strings.Contains(bl.Items[0].Pattern, "other_big") {
		t.Fatalf("批准后应为：删除旧规则、添加新规则：%+v", bl)
	}
	var l qList
	e.expect(200, "GET", base+"/queries", nil, &l)
	if _, ok := l.find(qidLong); ok {
		t.Fatal("批准后查询应被终止")
	}
}
