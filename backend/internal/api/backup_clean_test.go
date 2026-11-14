// @author: kevinkelly
// @license: Apache Licence
// @file: backup_clean_test.go
// @time: 2026/10/28

package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"starrocks-ops/fakesr"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

const s3Dir = "prod-sr-01/__starrocks_repository_repo_s3_prod/"

// s3Env 启动带内存 S3 的假集群并导入平台。
func s3Env(t *testing.T) (*env, string) {
	e := newEnvCfg(t, fakesr.ModeSharedNothing, "healthy", "", func(c *fakesr.Config) { c.S3, c.S3Port = true, 0 })
	e.login()
	cl := e.conn()
	cl["name"], cl["env"], cl["owner"] = "prod-test", "生产", "admin"
	var out struct{ ID string }
	e.expect(200, "POST", "/api/clusters", cl, &out)
	return e, "/api/clusters/" + out.ID
}

func (e *env) setCred(base string) {
	e.t.Helper()
	e.expect(200, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", map[string]any{"endpoint": "http://" + e.fake.S3Addr(), "region": "us-east-1", "pathStyle": true, "accessKey": "fakeak", "secretKey": "sk-very-secret"}, nil)
}

func (e *env) policySnaps(p policyOut, start int, ages ...int) []string {
	var names []string
	for i, d := range ages {
		n := fmt.Sprintf("auto_%s_%03d", strings.TrimPrefix(p.ID, "bp_"), start+i)
		must(e.t, e.fake.AddSnapshot("repo_s3_prod", n, time.Now().Add(-time.Duration(d)*24*time.Hour)))
		names = append(names, n)
	}
	return names
}

func TestBackupCleanCredentials(t *testing.T) {
	e, base := s3Env(t)
	var info struct {
		Supported bool
		Reason    string
		Dir       string
		Cred      credView
	}
	e.expect(200, "GET", base+"/backup/repos/repo_s3_prod/cleanup", nil, &info)
	if !info.Supported || info.Dir != "s3://sr-backup/"+s3Dir || info.Cred.Configured {
		t.Fatalf("初始状态：%+v", info)
	}
	info.Reason = ""
	e.expect(200, "GET", base+"/backup/repos/repo_hdfs_ro/cleanup", nil, &info)
	if info.Supported || !strings.Contains(info.Reason, "暂不支持") {
		t.Fatalf("HDFS 仓库应不支持清理：%+v", info)
	}
	e.expect(404, "GET", base+"/backup/repos/no_repo/cleanup", nil, nil)

	good := map[string]any{"endpoint": "http://" + e.fake.S3Addr(), "region": "us-east-1", "pathStyle": true, "accessKey": "fakeak", "secretKey": "sk-very-secret"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range good {
			m[a] = b
		}
		m[k] = v
		return m
	}
	e.expect(400, "PUT", base+"/backup/repos/repo_hdfs_ro/cleanup", good, nil)
	e.expect(400, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", with("endpoint", "ftp://x y"), nil)
	e.expect(400, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", with("region", "BAD REGION"), nil)
	e.expect(400, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", with("accessKey", ""), nil)         // 首次必须填 AccessKey
	e.expect(400, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", with("secretKey", ""), nil)         // 首次必须填 SecretKey
	e.expect(502, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", with("accessKey", "wrongkey"), nil) // 存储拒绝：验证失败，不保存
	e.expect(502, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", with("endpoint", "http://127.0.0.1:1"), nil)
	e.expect(200, "GET", base+"/backup/repos/repo_s3_prod/cleanup", nil, &info)
	if info.Cred.Configured {
		t.Fatal("验证失败时不应保存凭据")
	}

	var v credView
	e.expect(200, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", good, &v)
	if !v.Configured || v.Endpoint != "http://"+e.fake.S3Addr() || !v.PathStyle || v.AKMasked == "fakeak" || v.By != "admin" {
		t.Fatalf("保存结果：%+v", v)
	}
	raw, _ := os.ReadFile(filepath.Join(e.dir, "state.json"))
	if strings.Contains(string(raw), "sk-very-secret") || strings.Contains(string(raw), "fakeak") {
		t.Fatal("state.json 中不应有明文凭据")
	}
	// 更新：AccessKey / SecretKey 留空沿用；换 AccessKey 必须同时给 SecretKey
	e.expect(200, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", map[string]any{"endpoint": "http://" + e.fake.S3Addr(), "region": "cn-north-1", "pathStyle": true}, &v)
	if v.Region != "cn-north-1" {
		t.Fatalf("更新地域：%+v", v)
	}
	e.expect(400, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", map[string]any{"endpoint": "http://" + e.fake.S3Addr(), "accessKey": "fakeak2"}, nil)

	// 权限：没有 backup.clean 的用户可以看状态，不能改
	var ro struct{ ID string }
	e.expect(200, "POST", "/api/roles", map[string]any{"name": "备份只读", "perms": []string{"backup:view", "backup.create"}, "clusters": []string{"*"}}, &ro)
	e.newUser("oncall8", []string{ro.ID}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall8", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	oc.expect(200, "GET", base+"/backup/repos/repo_s3_prod/cleanup", nil, nil)
	oc.expect(403, "PUT", base+"/backup/repos/repo_s3_prod/cleanup", good, nil)
	oc.expect(403, "DELETE", base+"/backup/repos/repo_s3_prod/cleanup", nil, nil)
	oc.expect(403, "POST", base+"/backup/repos/repo_s3_prod/cleanup", map[string]any{"snapshots": []string{"snap_dws_20261005"}}, nil)

	e.expect(200, "DELETE", base+"/backup/repos/repo_s3_prod/cleanup", nil, nil)
	e.expect(404, "DELETE", base+"/backup/repos/repo_s3_prod/cleanup", nil, nil)
}

func TestBackupCleanManual(t *testing.T) {
	e, base := s3Env(t)
	body := func(names ...string) map[string]any { return map[string]any{"snapshots": names} }
	e.expect(409, "POST", base+"/backup/repos/repo_s3_prod/cleanup", body("snap_dws_20261005"), nil) // 还没配置凭据
	e.setCred(base)
	for name, b := range map[string]map[string]any{
		"空":       {},
		"二选一":     {"snapshots": []string{"a"}, "policy": "bp_x"},
		"非法快照名":   body("../etc"),
		"路径符号":    body("a/b"),
		"重复":      body("snap_dws_20261005", "snap_dws_20261005"),
		"策略不存在":   {"policy": "bp_none"},
		"HDFS 仓库": nil,
	} {
		path := base + "/backup/repos/repo_s3_prod/cleanup"
		want := 400
		if name == "策略不存在" {
			want = 404
		}
		if b == nil {
			path, b, want = base+"/backup/repos/repo_hdfs_ro/cleanup", body("snap_dim_20261001"), 400
		}
		if code := e.call("POST", path, b, nil); code != want {
			t.Errorf("%s：期望 %d，实际 %d", name, want, code)
		}
	}
	e.expect(404, "POST", base+"/backup/repos/repo_s3_prod/cleanup", body("snap_not_exist"), nil)

	// 删除一个快照：只删它自己的目录
	other := e.fake.S3Keys("sr-backup", s3Dir+"__ss_snap_ads_20261006/")
	var r struct {
		TaskID string `json:"taskId"`
		Count  int
	}
	e.expect(200, "POST", base+"/backup/repos/repo_s3_prod/cleanup", body("snap_dws_20261005"), &r)
	tk := e.waitTask(r.TaskID)
	if tk.State != "done" || tk.Kind != "backup.clean" || r.Count != 1 || !strings.Contains(strings.Join(tk.Log, "\n"), "共删除 4 个文件") {
		t.Fatalf("任务：%+v", tk)
	}
	if left := e.fake.S3Keys("sr-backup", s3Dir+"__ss_snap_dws_20261005/"); len(left) != 0 {
		t.Fatalf("对象应删光：%v", left)
	}
	if got := e.fake.S3Keys("sr-backup", s3Dir+"__ss_snap_ads_20261006/"); strings.Join(got, ",") != strings.Join(other, ",") || len(got) != 4 {
		t.Fatalf("其他快照不应受影响：%v", got)
	}
	var sn struct{ Items []sr.Snapshot }
	e.expect(200, "GET", base+"/backup/repos/repo_s3_prod/snapshots", nil, &sn)
	if len(sn.Items) != 1 || sn.Items[0].Name != "snap_ads_20261006" {
		t.Fatalf("SHOW SNAPSHOT：%+v", sn.Items)
	}
	if a := lastAudit(t, e, "backup.clean"); !strings.Contains(a.Act, "删除备份快照 repo_s3_prod / snap_dws_20261005") || a.ResClass != "成功" || !strings.Contains(a.SQL, "S3 DELETE s3://sr-backup/"+s3Dir+"__ss_snap_dws_20261005/") {
		t.Fatalf("审计：%+v", a)
	}

	must(t, e.fake.AddSnapshot("repo_s3_prod", "snap_extra", time.Now()))
	// 并发保护：同一仓库同时只能有一个清理任务
	if !e.api.lockNode(strings.TrimPrefix(base, "/api/clusters/"), "repo", "repo_s3_prod") {
		t.Fatal("加锁失败")
	}
	e.expect(409, "POST", base+"/backup/repos/repo_s3_prod/cleanup", body("snap_extra"), nil)
	e.api.unlockNode(strings.TrimPrefix(base, "/api/clusters/"), "repo", "repo_s3_prod")

	// 有进行中的恢复作业时拒绝清理
	e.expect(200, "POST", base+"/backup/restore", map[string]any{"db": "dw", "snapshot": "snap_ads_20261006", "repo": "repo_s3_prod", "timestamp": "2026-10-06-02-00-02-456"}, nil)
	e.expect(409, "POST", base+"/backup/repos/repo_s3_prod/cleanup", body("snap_extra"), nil)
}

func TestBackupRetentionPlan(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local)
	p := model.BackupPolicy{ID: "bp_ab12cd34"}
	mk := func(daysAgo int) sr.Snapshot {
		return sr.Snapshot{Name: fmt.Sprintf("auto_ab12cd34_%d", daysAgo), Timestamp: now.Add(-time.Duration(daysAgo)*24*time.Hour).Format("2006-01-02-15-04-05") + "-000", Status: "OK"}
	}
	snaps := []sr.Snapshot{mk(1), mk(2), mk(3), mk(10), mk(20), mk(40)}
	snaps = append(snaps,
		sr.Snapshot{Name: "manual_old", Timestamp: "2020-01-01-00-00-00-000", Status: "OK"},             // 不是该策略创建的：永远不动
		sr.Snapshot{Name: "auto_ab12cd34_bad", Timestamp: "garbage", Status: "OK"},                      // 时间无法解析：保留
		sr.Snapshot{Name: "auto_ab12cd34_run", Timestamp: "2020-01-01-00-00-00-000", Status: "RUNNING"}, // 状态不是 OK：保留
		sr.Snapshot{Name: "auto_ab12cd3499_x", Timestamp: "2020-01-01-00-00-00-000", Status: "OK"},      // 前缀相近的其他策略
	)
	names := func(l []snapAt) string {
		var o []string
		for _, s := range l {
			o = append(o, strings.TrimPrefix(s.Name, "auto_ab12cd34_"))
		}
		return strings.Join(o, ",")
	}
	for _, c := range []struct {
		last, days   int
		keep, remove string
	}{
		{0, 0, "1,2,3,10,20,40", ""}, // 没有保留策略：全部保留
		{3, 0, "1,2,3", "40,20,10"},  // 只按份数
		{0, 15, "1,2,3,10", "40,20"}, // 只按天数
		{2, 15, "1,2,3,10", "40,20"}, // 两者都要满足才删：3 号虽然超出份数，但没超过 15 天
		{5, 15, "1,2,3,10,20", "40"}, // 20 号超过 15 天但还在最近 5 份内
		{0, 1, "1", "40,20,10,3,2"},  // 最新的一个永远保留
		{1, 0, "1", "40,20,10,3,2"},
	} {
		p.KeepLast, p.KeepDays = c.last, c.days
		keep, remove, skipped := planRetention(p, snaps, now)
		if names(keep) != c.keep || names(remove) != c.remove || skipped != 2 {
			t.Errorf("last=%d days=%d：保留 %s 删除 %s 跳过 %d", c.last, c.days, names(keep), names(remove), skipped)
		}
	}
	// 一个都没有时不会出错
	if k, r, _ := planRetention(model.BackupPolicy{ID: "bp_zz", KeepLast: 1}, snaps, now); len(k) != 0 || len(r) != 0 {
		t.Fatalf("无匹配快照：%v %v", k, r)
	}
}

func TestBackupRetentionPolicyFlow(t *testing.T) {
	e, base := s3Env(t)
	cid := strings.TrimPrefix(base, "/api/clusters/")
	pbody := map[string]any{"name": "每日备份", "dbs": []string{"dw"}, "repo": "repo_s3_prod", "every": "daily", "at": "02:00"}
	withKeep := func(last, days int) map[string]any {
		m := map[string]any{"keepLast": last, "keepDays": days}
		for k, v := range pbody {
			m[k] = v
		}
		return m
	}
	e.expect(400, "POST", base+"/backup/policies", withKeep(3, 0), nil) // 还没有清理凭据
	e.expect(400, "POST", base+"/backup/policies", withKeep(-1, 0), nil)
	e.expect(400, "POST", base+"/backup/policies", withKeep(0, 99999), nil)
	e.setCred(base)
	var p policyOut
	e.expect(200, "POST", base+"/backup/policies", withKeep(3, 0), &p)
	if p.KeepLast != 3 || p.KeepDays != 0 {
		t.Fatalf("新建策略：%+v", p)
	}
	// 编辑策略时同样要求凭据；清空保留策略后可以删除凭据，否则被拒绝
	e.expect(409, "DELETE", base+"/backup/repos/repo_s3_prod/cleanup", nil, nil)

	names := e.policySnaps(p, 0, 1, 2, 3, 4, 5, 6) // auto_<id>_00 最新 … _05 最旧
	var pv struct {
		Keep, Remove []snapAt
		Skipped      int
		CredReady    bool
	}
	e.expect(200, "GET", base+"/backup/policies/"+p.ID+"/clean-preview", nil, &pv)
	if len(pv.Keep) != 3 || len(pv.Remove) != 3 || pv.Remove[0].Name != names[5] || pv.Remove[2].Name != names[3] || !pv.CredReady {
		t.Fatalf("预览：%+v", pv)
	}
	e.expect(404, "GET", base+"/backup/policies/bp_none/clean-preview", nil, nil)

	// 立即清理：删除 3 个最旧的，保留最新 3 个与非本策略的快照
	var r struct{ TaskID string }
	e.expect(200, "POST", base+"/backup/repos/repo_s3_prod/cleanup", map[string]any{"policy": p.ID}, &r)
	if tk := e.waitTask(r.TaskID); tk.State != "done" {
		t.Fatalf("任务：%+v", tk)
	}
	got := strings.Join(e.fake.SnapshotNames("repo_s3_prod"), ",")
	for _, n := range names[:3] {
		if !strings.Contains(got, n) {
			t.Fatalf("应保留 %s：%s", n, got)
		}
	}
	for _, n := range names[3:] {
		if strings.Contains(got, n) {
			t.Fatalf("应已删除 %s：%s", n, got)
		}
	}
	if !strings.Contains(got, "snap_ads_20261006") || !strings.Contains(got, "snap_dws_20261005") {
		t.Fatalf("非本策略的快照不应受影响：%s", got)
	}
	e.expect(409, "POST", base+"/backup/repos/repo_s3_prod/cleanup", map[string]any{"policy": p.ID}, nil) // 没有需要清理的

	// 定时清理：每小时一次；每次最多删除 10 个；记录结果
	more := e.policySnaps(p, 100, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20)
	_ = more
	e.expect(200, "PUT", base+"/backup/policies/"+p.ID, withKeep(1, 0), &p)
	if n := e.api.RunDueCleanups(t.Context(), time.Now()); n != 1 {
		t.Fatalf("应启动 1 个策略的清理：%d", n)
	}
	var res string
	for i := 0; i < 200; i++ {
		var o bkOverview
		e.expect(200, "GET", base+"/backup", nil, &o)
		if o.Policies[0].CleanResult != "" {
			res = o.Policies[0].CleanResult
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(res, "已删除 10 个过期快照") || !strings.Contains(res, "另有") {
		t.Fatalf("定时清理结果：%q", res)
	}
	if n := e.api.RunDueCleanups(t.Context(), time.Now()); n != 0 {
		t.Fatalf("一小时内不应重复清理：%d", n)
	}
	// 一小时后继续清理剩下的，直到只剩最新的一个
	for round := 0; round < 3; round++ {
		now := time.Now().Add(time.Duration(round+1) * 2 * time.Hour)
		if e.api.RunDueCleanups(t.Context(), now) == 0 {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for i := 0; i < 200; i++ {
			pp, _ := e.api.policyOf(cid, p.ID)
			if !pp.LastClean.Before(now.Add(-time.Minute)) || pp.CleanResult != res {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	left := 0
	for _, n := range e.fake.SnapshotNames("repo_s3_prod") {
		if strings.HasPrefix(n, policyPrefix(model.BackupPolicy{ID: p.ID})) {
			left++
		}
	}
	if left != 1 {
		t.Fatalf("保留份数 1：策略快照还剩 %d 个：%v", left, e.fake.SnapshotNames("repo_s3_prod"))
	}
	// 停用的策略不会被定时清理
	e.expect(200, "PUT", base+"/backup/policies/"+p.ID, map[string]any{"enabled": false}, nil)
	if n := e.api.RunDueCleanups(t.Context(), time.Now().Add(100*time.Hour)); n != 0 {
		t.Fatalf("停用的策略不应清理：%d", n)
	}
	// 关闭保留策略后才能删除凭据
	e.expect(200, "PUT", base+"/backup/policies/"+p.ID, withKeep(0, 0), nil)
	e.expect(200, "DELETE", base+"/backup/repos/repo_s3_prod/cleanup", nil, nil)
}
