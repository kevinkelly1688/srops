// @author: kevinkelly
// @license: Apache Licence
// @file: ops_test.go
// @time: 2026/10/28

package sr

import (
	"reflect"
	"strings"
	"testing"
)

func TestIdentAndLiteral(t *testing.T) {
	for _, s := range []string{"rg_etl", "_x", "A1"} {
		if !ValidIdent(s) {
			t.Errorf("%q 应合法", s)
		}
	}
	for _, s := range []string{"", "1a", "a-b", "a b", "a`b", "a;drop", "a.b", strings.Repeat("a", 65), "库"} {
		if ValidIdent(s) {
			t.Errorf("%q 不应合法", s)
		}
	}
	if l, err := Lit(`a"b\c`); err != nil || l != `"a\"b\\c"` {
		t.Errorf("转义结果 %q %v", l, err)
	}
	if _, err := Lit("a\nb"); err == nil {
		t.Error("换行应报错")
	}
	for in, want := range map[string]string{"true": "true", "FALSE": "false", "12": "12", "-3": "-3", "0.8": "0.8", "abc": `"abc"`, "1e5": `"1e5"`, "": `""`, "10%": `"10%"`, "+1": `"+1"`} {
		if got, _ := Value(in); got != want {
			t.Errorf("Value(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestMaskSecrets(t *testing.T) {
	q := `CREATE REPOSITORY r WITH BROKER ON LOCATION "s3a://b/p" PROPERTIES ("aws.s3.access_key" = "AK123", "aws.s3.secret_key" = "S\"K", "aws.s3.endpoint" = "https://x", "password"="p w")`
	got := MaskSecrets(q)
	for _, leak := range []string{"AK123", "S\\\"K", "p w"} {
		if strings.Contains(got, leak) {
			t.Errorf("密钥泄露 %q：%s", leak, got)
		}
	}
	if !strings.Contains(got, "https://x") || !strings.Contains(got, "s3a://b/p") {
		t.Errorf("非密钥内容被误伤：%s", got)
	}
}

func TestParseClassifiers(t *testing.T) {
	s := "(id=10012, weight=1.0, user=etl_user)\n(id=10013, weight=4.0625, role=bi, query_type in (SELECT,INSERT), source_ip=10.0.%, db=ods)"
	got := ParseClassifiers(s)
	want := []Classifier{{ID: "10012", User: "etl_user"}, {ID: "10013", Role: "bi", QueryType: []string{"select", "insert"}, SourceIP: "10.0.%", DB: "ods"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("解析结果 %+v", got)
	}
	if len(ParseClassifiers("")) != 0 {
		t.Error("空串应无分类器")
	}
}

func TestRGSpec(t *testing.T) {
	ok := RGSpec{Name: "rg_new", CPUWeight: 8, MemPct: 20, Concurrency: 10, BigCPUSec: 300, SpillPct: 80,
		Classifiers: []Classifier{{User: "etl", QueryType: []string{"SELECT"}}, {Role: "bi", SourceIP: "10.0.%"}}}
	if err := ok.Validate(true); err != nil {
		t.Fatal(err)
	}
	sql := ok.CreateSQL()
	for _, want := range []string{"CREATE RESOURCE GROUP `rg_new` TO (user=\"etl\", query_type in (\"select\")), (role=\"bi\", source_ip=\"10.0.%\") WITH (", `"cpu_weight" = "8"`,
		`"mem_limit" = "20%"`, `"concurrency_limit" = "10"`, `"big_query_cpu_second_limit" = "300"`, `"spill_mem_limit_threshold" = "0.80"`} {
		if !strings.Contains(sql, want) {
			t.Errorf("缺少 %q：\n%s", want, sql)
		}
	}
	bad := map[string]RGSpec{
		"名称":      {Name: "a-b", CPUWeight: 1, Classifiers: ok.Classifiers},
		"内置":      {Name: "default_wg", CPUWeight: 1, Classifiers: ok.Classifiers},
		"cpu 缺失":  {Name: "x", Classifiers: ok.Classifiers},
		"cpu 双设置": {Name: "x", CPUWeight: 1, Exclusive: 2, Classifiers: ok.Classifiers},
		"内存":      {Name: "x", CPUWeight: 1, MemPct: 101, Classifiers: ok.Classifiers},
		"无分类器":    {Name: "x", CPUWeight: 1},
		"注入用户":    {Name: "x", CPUWeight: 1, Classifiers: []Classifier{{User: `a") --`}}},
		"注入 IP":   {Name: "x", CPUWeight: 1, Classifiers: []Classifier{{SourceIP: `1.1.1.1"; drop`}}},
		"库名":      {Name: "x", CPUWeight: 1, Classifiers: []Classifier{{DB: "a.b"}}},
		"类型":      {Name: "x", CPUWeight: 1, Classifiers: []Classifier{{QueryType: []string{"drop"}}}},
		"空条件":     {Name: "x", CPUWeight: 1, Classifiers: []Classifier{{}}},
	}
	for name, sp := range bad {
		if err := sp.Validate(true); err == nil {
			t.Errorf("%s 应校验失败", name)
		}
	}
}

func TestRGAlterSQL(t *testing.T) {
	old := ResourceGroup{Name: "rg", Classifiers: []Classifier{{ID: "1", User: "a"}, {ID: "2", Role: "b"}, {ID: "3", User: "c"}}}
	sp := RGSpec{Name: "rg", CPUWeight: 4, Classifiers: []Classifier{{User: "a"}, {User: "d", QueryType: []string{"SELECT"}}}}
	if err := sp.Validate(false); err != nil {
		t.Fatal(err)
	}
	got := sp.AlterSQL(old)
	if len(got) != 3 || !strings.HasPrefix(got[0], "ALTER RESOURCE GROUP `rg` WITH (") {
		t.Fatalf("语句序列 %v", got)
	}
	if got[1] != "ALTER RESOURCE GROUP `rg` ADD (user=\"d\", query_type in (\"select\"))" {
		t.Errorf("ADD 语句 %s", got[1])
	}
	if got[2] != "ALTER RESOURCE GROUP `rg` DROP (2, 3)" {
		t.Errorf("DROP 语句 %s", got[2])
	}
	// 分类器未变化时只有属性修改
	same := RGSpec{Name: "rg", CPUWeight: 4, Classifiers: []Classifier{{User: "a"}, {Role: "b"}, {User: "c"}}}
	if g := same.AlterSQL(old); len(g) != 1 {
		t.Errorf("分类器未变应只有 1 条：%v", g)
	}
}

func TestMVSQL(t *testing.T) {
	cases := []struct {
		op  string
		opt MVRefreshOpt
		sql string
		bad bool
	}{
		{"refresh", MVRefreshOpt{}, "REFRESH MATERIALIZED VIEW `ads`.`mv1`", false},
		{"refresh", MVRefreshOpt{Force: true}, "REFRESH MATERIALIZED VIEW `ads`.`mv1` FORCE", false},
		{"refresh", MVRefreshOpt{Start: "2026-10-01", End: "2026-10-07", Force: true}, `REFRESH MATERIALIZED VIEW ` + "`ads`.`mv1`" + ` PARTITION START ("2026-10-01") END ("2026-10-07") FORCE`, false},
		{"refresh", MVRefreshOpt{Start: "2026-10-01"}, "", true},
		{"refresh", MVRefreshOpt{Start: "x", End: "y"}, "", true},
		{"active", MVRefreshOpt{}, "ALTER MATERIALIZED VIEW `ads`.`mv1` ACTIVE", false},
		{"inactive", MVRefreshOpt{}, "ALTER MATERIALIZED VIEW `ads`.`mv1` INACTIVE", false},
		{"cancel", MVRefreshOpt{}, "CANCEL REFRESH MATERIALIZED VIEW `ads`.`mv1`", false},
		{"drop", MVRefreshOpt{}, "", true},
	}
	for _, c := range cases {
		got, err := MVSQL(c.op, "ads", "mv1", c.opt)
		if (err != nil) != c.bad || got != c.sql {
			t.Errorf("%s %+v → %q %v", c.op, c.opt, got, err)
		}
	}
	if _, err := MVSQL("refresh", "ads", "mv1`; DROP", MVRefreshOpt{}); err == nil {
		t.Error("物化视图名含特殊字符应被拒绝")
	}
}

func TestRoutineLoadSQL(t *testing.T) {
	c := &Client{}
	if q, err := c.RoutineLoadSQL("PAUSE", "ods", "rl_a"); err != nil || q != "PAUSE ROUTINE LOAD FOR `ods`.`rl_a`" {
		t.Errorf("%q %v", q, err)
	}
	for _, bad := range [][3]string{{"DROP", "ods", "a"}, {"PAUSE", "o ds", "a"}, {"STOP", "ods", "a;b"}} {
		if _, err := c.RoutineLoadSQL(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("%v 应被拒绝", bad)
		}
	}
}
