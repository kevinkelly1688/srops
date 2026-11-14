// @author: kevinkelly
// @license: Apache Licence
// @file: fakesr_test.go
// @time: 2026/10/28

package fakesr

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/ssh"
)

func startTest(t *testing.T, mode, scenario string) *Cluster {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Mode, cfg.Scenario = mode, scenario
	cfg.QueryPort, cfg.SSHPort = 0, 0
	cfg.FEHTTPBase, cfg.BEHTTPBase, cfg.CNHTTPBase = 0, 0, 0
	c, err := Start(cfg)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(c.Stop)
	return c
}

func openDB(t *testing.T, c *Cluster, user, pass string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", user+":"+pass+"@tcp("+c.QueryAddr()+")/?timeout=5s&readTimeout=30s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// queryMaps 执行查询并返回列名与按列名索引的行；任何 NULL 值视为失败。
func queryMaps(t *testing.T, db *sql.DB, q string) ([]string, []map[string]string) {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for i, col := range cols {
			if !vals[i].Valid { // 平台按 string 扫描，不应出现 NULL
				t.Fatalf("%s: column %s is NULL", q, col)
			}
			m[col] = vals[i].String
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols, out
}

func TestMySQL(t *testing.T) {
	c := startTest(t, ModeSharedNothing, ScenarioProblems)
	db := openDB(t, c, "sr_ops", "secret")

	cols, fes := queryMaps(t, db, "SHOW FRONTENDS")
	if want := "Name,IP,EditLogPort,HttpPort,QueryPort,RpcPort,Role,ClusterId,Join,Alive,ReplayedJournalId," +
		"LastHeartbeat,IsHelper,ErrMsg,StartTime,Version"; strings.Join(cols, ",") != want {
		t.Fatalf("frontends columns = %v", cols)
	}
	if len(fes) != 4 || fes[0]["Role"] != "LEADER" || fes[3]["Role"] != "OBSERVER" {
		t.Fatalf("unexpected frontends: %v", fes)
	}
	if fes[0]["ReplayedJournalId"] != "1182334" || fes[3]["ReplayedJournalId"] != "1180834" {
		t.Fatalf("journal ids: %s %s", fes[0]["ReplayedJournalId"], fes[3]["ReplayedJournalId"])
	}

	cols, bes := queryMaps(t, db, "show backends;")
	if len(cols) != 26 || cols[0] != "BackendId" || cols[25] != "CpuUsedPct" {
		t.Fatalf("backends columns = %v", cols)
	}
	if len(bes) != 6 || bes[4]["MaxDiskUsedPct"] != "87.12 %" || bes[3]["CpuUsedPct"] != "92.0 %" {
		t.Fatalf("unexpected backends: %v", bes)
	}

	_, ver := queryMaps(t, db, "SELECT current_version()")
	if ver[0]["current_version()"] != "3.5.6-a1b2c3d" {
		t.Fatalf("version = %v", ver)
	}

	_, cnt := queryMaps(t, db, "SELECT COUNT(*) FROM information_schema.tables WHERE TABLE_TYPE = 'BASE TABLE'")
	if cnt[0]["COUNT(*)"] != "1286" {
		t.Fatalf("tables count = %v", cnt)
	}

	_, cfgRows := queryMaps(t, db, "ADMIN SHOW FRONTEND CONFIG LIKE 'run_mode'")
	if len(cfgRows) != 1 || cfgRows[0]["Value"] != "shared_nothing" {
		t.Fatalf("run_mode = %v", cfgRows)
	}

	_, parts := queryMaps(t, db, "SELECT DB_NAME, TABLE_NAME, PARTITION_NAME, REPLICATION_NUM, BUCKETS, DATA_SIZE, MAX_CS FROM information_schema.partitions_meta")
	if len(parts) != 30 {
		t.Fatalf("partitions_meta rows = %d", len(parts))
	}

	if _, err := db.Query("SELECT * FROM foo.bar"); err == nil || !strings.Contains(err.Error(), "1064") {
		t.Fatalf("expected 1064 error, got %v", err)
	}
}

func TestAccessDenied(t *testing.T) {
	c := startTest(t, ModeSharedNothing, ScenarioHealthy)
	err := openDB(t, c, "sr_ops", "wrong").Ping()
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1045 {
		t.Fatalf("expected 1045, got %v", err)
	}
}

func TestSharedData(t *testing.T) {
	c := startTest(t, ModeSharedData, ScenarioProblems)
	db := openDB(t, c, "sr_ops", "secret")
	cols, bes := queryMaps(t, db, "SHOW BACKENDS")
	if len(cols) != 26 || len(bes) != 0 {
		t.Fatalf("shared-data backends: cols=%d rows=%d", len(cols), len(bes))
	}
	_, cns := queryMaps(t, db, "SHOW COMPUTE NODES")
	if len(cns) != 4 || cns[0]["WarehouseName"] != "default_warehouse" {
		t.Fatalf("compute nodes: %v", cns)
	}
}

func TestMetrics(t *testing.T) {
	c := startTest(t, ModeSharedNothing, ScenarioProblems)
	resp, err := http.Get("http://" + c.FEHTTPAddrs()[0] + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{
		"# TYPE starrocks_fe_qps gauge",
		"starrocks_fe_query_total ",
		`starrocks_fe_query_latency_ms{quantile="0.99"}`,
		`jvm_heap_size_bytes{type="max"} 17179869184`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("fe metrics missing %q", want)
		}
	}
}

func TestSSH(t *testing.T) {
	c := startTest(t, ModeSharedNothing, ScenarioProblems)
	client, err := ssh.Dial("tcp", c.SSHAddr(), &ssh.ClientConfig{
		User:            "starrocks",
		Auth:            []ssh.AuthMethod{ssh.Password("secret")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	run := func(cmd string) (string, error) {
		s, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		out, err := s.CombinedOutput(cmd)
		return string(out), err
	}
	out, err := run("cat /proc/sys/vm/max_map_count")
	if err != nil || strings.TrimSpace(out) != "65530" {
		t.Fatalf("max_map_count: %q %v", out, err)
	}
	_, err = run("uname -a")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 127 {
		t.Fatalf("expected exit 127, got %v", err)
	}
}

// TestAllQueries 确认所有约定查询在各模式/场景下都能执行。
func TestAllQueries(t *testing.T) {
	queries := []string{
		"SELECT 1", "SET NAMES utf8mb4", "USE ods",
		"SELECT current_version()", "SHOW FRONTENDS", "SHOW BACKENDS", "SHOW COMPUTE NODES",
		"ADMIN SHOW FRONTEND CONFIG LIKE 'default_replication_num'",
		"ADMIN SHOW FRONTEND CONFIG LIKE 'no_such_key'",
		"SHOW GRANTS", "SHOW PROC '/statistic'", "SHOW CATALOGS",
		"SELECT COUNT(*) FROM information_schema.schemata",
		"SELECT TABLE_SCHEMA, TABLE_NAME, IS_ACTIVE, LAST_REFRESH_STATE, LAST_REFRESH_ERROR_MESSAGE FROM information_schema.materialized_views",
		"SELECT DB_NAME, NAME, STATE, REASONS_OF_STATE_CHANGED FROM information_schema.routine_load_jobs WHERE STATE <> 'RUNNING'",
		"SELECT STATE, COUNT(*) FROM information_schema.loads WHERE CREATE_TIME > now() - INTERVAL 1 DAY GROUP BY STATE",
		"SHOW RESOURCE GROUPS ALL", "SHOW VARIABLES LIKE 'enable_query_queue_select'",
		"SELECT COUNT(*) FROM starrocks_audit_db__.starrocks_audit_tbl__ WHERE queryTime > 5000",
	}
	for _, mode := range []string{ModeSharedNothing, ModeSharedData} {
		for _, sc := range []string{ScenarioHealthy, ScenarioProblems} {
			t.Run(mode+"/"+sc, func(t *testing.T) {
				db := openDB(t, startTest(t, mode, sc), "sr_ops", "secret")
				for _, q := range queries {
					queryMaps(t, db, q)
				}
				_, a := queryMaps(t, db, "select count(*) from `starrocks_audit_db__`.`starrocks_audit_tbl__`;")
				if want := pick(sc == ScenarioProblems, "143", "12"); a[0]["COUNT(*)"] != want {
					t.Fatalf("audit count = %v, want %s", a, want)
				}
			})
		}
	}
	db := openDB(t, startTest(t, ModeSharedNothing, ScenarioHealthy), "root", "secret")
	var me *mysql.MySQLError
	if err := db.Ping(); !errors.As(err, &me) || me.Number != 1045 {
		t.Fatalf("expected 1045 for unknown user, got %v", err)
	}
}
