// @author: kevinkelly
// @license: Apache Licence
// @file: backup_test.go
// @time: 2026/10/28

package api

import (
	"strings"
	"testing"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

type bkOverview struct {
	Jobs      []sr.BackupJob `json:"jobs"`
	Repos     []sr.Repo      `json:"repos"`
	Policies  []policyOut    `json:"policies"`
	Databases []string       `json:"databases"`
	Warnings  []string       `json:"warnings"`
}

func (o bkOverview) job(kind, db string) (sr.BackupJob, bool) {
	for _, j := range o.Jobs {
		if j.Kind == kind && j.DB == db {
			return j, true
		}
	}
	return sr.BackupJob{}, false
}

func TestBackupRestore(t *testing.T) {
	e, base := opsEnv(t, "problems")
	var o bkOverview
	e.expect(200, "GET", base+"/backup", nil, &o)
	if len(o.Repos) != 2 || o.Repos[0].Name != "repo_s3_prod" || !o.Repos[1].ReadOnly || len(o.Databases) != 5 || len(o.Jobs) != 0 {
		t.Fatalf("初始状态不对：%+v", o)
	}
	var sn struct{ Items []sr.Snapshot }
	e.expect(200, "GET", base+"/backup/repos/repo_s3_prod/snapshots", nil, &sn)
	if len(sn.Items) != 2 || sn.Items[0].Name != "snap_ads_20261006" || sn.Items[0].Timestamp != "2026-10-06-02-00-02-456" { // 按时间倒序
		t.Fatalf("快照列表：%+v", sn.Items)
	}
	e.expect(502, "GET", base+"/backup/repos/no_repo/snapshots", nil, nil)
	e.expect(400, "GET", base+"/backup/repos/bad%20name/snapshots", nil, nil)

	// 发起备份
	e.expect(200, "POST", base+"/backup/jobs", map[string]any{"db": "dw", "snapshot": "snap_dw_ui", "repo": "repo_s3_prod", "tables": []string{"dwd_order_detail", "dwd_payment"}}, nil)
	if a := lastAudit(t, e, "backup.create"); !strings.Contains(a.SQL, "BACKUP SNAPSHOT `dw`.`snap_dw_ui` TO `repo_s3_prod` ON (`dwd_order_detail`, `dwd_payment`)") || a.ResClass != "成功" {
		t.Fatalf("审计：%+v", a)
	}
	o = bkOverview{}
	e.expect(200, "GET", base+"/backup", nil, &o)
	if j, ok := o.job("backup", "dw"); !ok || j.State != "UPLOADING" || !j.Active || j.Snapshot != "snap_dw_ui" {
		t.Fatalf("备份作业：%+v", o.Jobs)
	}
	e.expect(409, "POST", base+"/backup/jobs", map[string]any{"db": "dw", "snapshot": "snap_dw_2", "repo": "repo_s3_prod"}, nil) // 同库同时只能一个作业
	e.expect(409, "POST", base+"/backup/restore", map[string]any{"db": "dw", "snapshot": "snap_dim_20261001", "repo": "repo_hdfs_ro", "timestamp": "2026-10-01-03-00-00-789"}, nil)

	// 取消
	e.expect(200, "POST", base+"/backup/cancel", map[string]any{"db": "dw", "kind": "backup"}, nil)
	e.expect(502, "POST", base+"/backup/cancel", map[string]any{"db": "dw", "kind": "backup"}, nil)
	e.expect(400, "POST", base+"/backup/cancel", map[string]any{"db": "dw", "kind": "drop"}, nil)
	o = bkOverview{}
	e.expect(200, "GET", base+"/backup", nil, &o)
	if j, _ := o.job("backup", "dw"); j.State != "CANCELLED" || j.Active {
		t.Fatalf("取消后：%+v", j)
	}

	// 备份完成后快照出现在仓库里
	e.expect(200, "POST", base+"/backup/jobs", map[string]any{"db": "ads", "snapshot": "snap_ads_ui", "repo": "repo_s3_prod"}, nil)
	time.Sleep(1700 * time.Millisecond)
	e.expect(200, "GET", base+"/backup/repos/repo_s3_prod/snapshots", nil, &sn)
	found := false
	for _, s := range sn.Items {
		found = found || s.Name == "snap_ads_ui"
	}
	o = bkOverview{}
	e.expect(200, "GET", base+"/backup", nil, &o)
	if j, _ := o.job("backup", "ads"); !found || j.State != "FINISHED" {
		t.Fatalf("备份完成后应有快照：%v %+v", sn.Items, j)
	}

	// 参数校验
	for name, b := range map[string]map[string]any{
		"库名注入": {"db": "dw`;drop", "snapshot": "s1", "repo": "repo_s3_prod"},
		"快照名":  {"db": "dw", "snapshot": "bad name", "repo": "repo_s3_prod"},
		"仓库名":  {"db": "dw", "snapshot": "s1", "repo": "r p"},
		"表名":   {"db": "dw", "snapshot": "s1", "repo": "repo_s3_prod", "tables": []string{"t;x"}},
	} {
		if code := e.call("POST", base+"/backup/jobs", b, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	e.expect(502, "POST", base+"/backup/jobs", map[string]any{"db": "dim", "snapshot": "s_ro", "repo": "repo_hdfs_ro"}, nil) // 只读仓库不能备份
	e.expect(502, "POST", base+"/backup/jobs", map[string]any{"db": "nodb", "snapshot": "s_x", "repo": "repo_s3_prod"}, nil) // 库不存在

	// 恢复：时间戳必须是仓库中真实存在的快照
	rs := map[string]any{"db": "dim", "snapshot": "snap_dim_20261001", "repo": "repo_hdfs_ro", "timestamp": "2026-10-01-03-00-00-789", "replication": 3,
		"tables": []map[string]string{{"name": "dim_user", "as": "dim_user_restore"}}}
	e.expect(400, "POST", base+"/backup/restore", map[string]any{"db": "dim", "snapshot": "snap_dim_20261001", "repo": "repo_hdfs_ro", "timestamp": "yesterday"}, nil)
	e.expect(400, "POST", base+"/backup/restore", map[string]any{"db": "dim", "snapshot": "snap_dim_20261001", "repo": "repo_hdfs_ro", "timestamp": "2026-10-01-03-00-00-789", "replication": 99}, nil)
	e.expect(502, "POST", base+"/backup/restore", map[string]any{"db": "dim", "snapshot": "snap_dim_20261001", "repo": "repo_hdfs_ro", "timestamp": "2020-01-01-00-00-00-000"}, nil)
	e.expect(200, "POST", base+"/backup/restore", rs, nil)
	if a := lastAudit(t, e, "backup.restore"); !strings.Contains(a.SQL, "RESTORE SNAPSHOT `dim`.`snap_dim_20261001` FROM `repo_hdfs_ro` ON (`dim_user` AS `dim_user_restore`)") || !strings.Contains(a.SQL, `"backup_timestamp" = "2026-10-01-03-00-00-789"`) {
		t.Fatalf("恢复审计：%+v", a)
	}
	o = bkOverview{}
	e.expect(200, "GET", base+"/backup", nil, &o)
	if j, ok := o.job("restore", "dim"); !ok || j.Snapshot != "snap_dim_20261001" || !strings.Contains(j.Objects, "dim_user") {
		t.Fatalf("恢复作业：%+v", o.Jobs)
	}
}

func TestRepos(t *testing.T) {
	e, base := opsEnv(t, "healthy")
	s3 := map[string]any{"name": "repo_oss", "type": "s3", "location": "s3a://sr-backup/oss", "accessKey": "AKIAEXAMPLE123", "secretKey": "s3cr3t/Key+Value==", "endpoint": "https://oss-cn-hangzhou.aliyuncs.com", "region": "cn-hangzhou", "pathStyle": true}
	e.expect(200, "POST", base+"/backup/repos", s3, nil)
	a := lastAudit(t, e, "backup.repo")
	if strings.Contains(a.SQL, "AKIAEXAMPLE123") || strings.Contains(a.SQL, "s3cr3t") {
		t.Fatalf("审计中不应出现密钥明文：%s", a.SQL)
	}
	if !strings.Contains(a.SQL, `"aws.s3.access_key" = "***"`) || !strings.Contains(a.SQL, `"aws.s3.secret_key" = "***"`) || !strings.Contains(a.SQL, `"aws.s3.endpoint" = "https://oss-cn-hangzhou.aliyuncs.com"`) || !strings.Contains(a.SQL, "CREATE REPOSITORY `repo_oss`") {
		t.Fatalf("审计 SQL：%s", a.SQL)
	}
	// 平台不保存密钥
	if b, err := readState(e); err != nil || strings.Contains(b, "AKIAEXAMPLE123") || strings.Contains(b, "s3cr3t") {
		t.Fatalf("状态文件中不应有仓库密钥：%v", err)
	}
	e.expect(409, "POST", base+"/backup/repos", s3, nil)
	e.expect(200, "POST", base+"/backup/repos", map[string]any{"name": "repo_ro", "type": "s3", "location": "s3a://x/y", "accessKey": "AK", "secretKey": "SK", "readOnly": true}, nil)
	var o bkOverview
	e.expect(200, "GET", base+"/backup", nil, &o)
	var ro bool
	for _, r := range o.Repos {
		if r.Name == "repo_ro" {
			ro = r.ReadOnly
		}
	}
	if len(o.Repos) != 4 || !ro {
		t.Fatalf("仓库列表：%+v", o.Repos)
	}
	for name, b := range map[string]map[string]any{
		"类型":            {"name": "r1", "type": "ftp", "location": "s3a://x/y"},
		"位置":            {"name": "r1", "type": "s3", "location": "file:///etc", "accessKey": "a", "secretKey": "b"},
		"名称":            {"name": "r-1", "type": "s3", "location": "s3a://x/y", "accessKey": "a", "secretKey": "b"},
		"密钥缺失":          {"name": "r1", "type": "s3", "location": "s3a://x/y"},
		"密钥注入":          {"name": "r1", "type": "s3", "location": "s3a://x/y", "accessKey": `a" , "x"="y`, "secretKey": "b"},
		"endpoint":      {"name": "r1", "type": "s3", "location": "s3a://x/y", "accessKey": "a", "secretKey": "b", "endpoint": "http://a b"},
		"hdfs 无 broker": {"name": "r1", "type": "hdfs", "location": "hdfs://nn/x"},
	} {
		if code := e.call("POST", base+"/backup/repos", b, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}
	e.expect(200, "DELETE", base+"/backup/repos/repo_ro", nil, nil)
	e.expect(502, "DELETE", base+"/backup/repos/repo_ro", nil, nil)
	e.expect(400, "DELETE", base+"/backup/repos/bad%60name", nil, nil)
}

func readState(e *env) (string, error) {
	var out strings.Builder
	var err error
	e.api.Store.View(func(st *storeState) {
		out.WriteString(strings.Join(func() (l []string) {
			for _, c := range st.Clusters {
				l = append(l, c.Name)
			}
			for _, p := range st.Backups {
				l = append(l, p.Name, p.Repo, p.LastResult)
			}
			for _, p := range st.Params {
				l = append(l, p.Key, p.To, p.Status)
			}
			return
		}(), "\n"))
	})
	return out.String(), err
}

func TestBackupPolicies(t *testing.T) {
	e, base := opsEnv(t, "healthy")
	var p policyOut
	body := map[string]any{"name": "每日备份", "dbs": []string{"dw", "ads"}, "repo": "repo_s3_prod", "every": "daily", "at": "02:00"}
	e.expect(200, "POST", base+"/backup/policies", body, &p)
	if p.ID == "" || !p.Enabled || p.Next.IsZero() || !p.Next.After(time.Now()) || p.Next.Sub(time.Now()) > 24*time.Hour+time.Minute {
		t.Fatalf("新建策略：%+v", p)
	}
	e.expect(409, "POST", base+"/backup/policies", body, nil) // 同名
	for name, mut := range map[string]func(m map[string]any){
		"空名称":   func(m map[string]any) { m["name"] = " " },
		"无库":    func(m map[string]any) { m["name"] = "n2"; m["dbs"] = []string{} },
		"库名":    func(m map[string]any) { m["name"] = "n3"; m["dbs"] = []string{"a;b"} },
		"重复库":   func(m map[string]any) { m["name"] = "n4"; m["dbs"] = []string{"dw", "dw"} },
		"周期":    func(m map[string]any) { m["name"] = "n5"; m["every"] = "hourly" },
		"时间":    func(m map[string]any) { m["name"] = "n6"; m["at"] = "25:00" },
		"星期":    func(m map[string]any) { m["name"] = "n7"; m["every"] = "weekly"; m["weekday"] = 9 },
		"仓库不存在": func(m map[string]any) { m["name"] = "n8"; m["repo"] = "no_such_repo" },
		"只读仓库":  func(m map[string]any) { m["name"] = "n9"; m["repo"] = "repo_hdfs_ro" },
	} {
		b := map[string]any{}
		for k, v := range body {
			b[k] = v
		}
		mut(b)
		if code := e.call("POST", base+"/backup/policies", b, nil); code != 400 {
			t.Errorf("%s：期望 400，实际 %d", name, code)
		}
	}

	// 停用 / 启用 / 修改 / 删除
	e.expect(200, "PUT", base+"/backup/policies/"+p.ID, map[string]any{"enabled": false}, &p)
	if p.Enabled || !p.Next.IsZero() {
		t.Fatalf("停用后：%+v", p)
	}
	e.expect(200, "PUT", base+"/backup/policies/"+p.ID, map[string]any{"enabled": true}, &p)
	e.expect(200, "PUT", base+"/backup/policies/"+p.ID, map[string]any{"name": "每日备份", "dbs": []string{"dw"}, "repo": "repo_s3_prod", "every": "weekly", "at": "03:30", "weekday": 0}, &p)
	if p.Every != "weekly" || p.Weekday != 0 || len(p.DBs) != 1 || p.Next.Weekday() != time.Sunday || p.Next.Hour() != 3 || p.Next.Minute() != 30 {
		t.Fatalf("修改后：%+v", p)
	}
	e.expect(404, "PUT", base+"/backup/policies/bp_none", map[string]any{"enabled": false}, nil)
	e.expect(400, "PUT", base+"/backup/policies/"+p.ID, map[string]any{}, nil)

	// 定时执行：到点才执行；已有进行中作业的库被跳过；结果记入策略与审计
	pol := func() model.BackupPolicy {
		var o bkOverview
		e.expect(200, "GET", base+"/backup", nil, &o)
		return o.Policies[0].BackupPolicy
	}
	e.expect(200, "PUT", base+"/backup/policies/"+p.ID, map[string]any{"name": "每日备份", "dbs": []string{"dw", "ads"}, "repo": "repo_s3_prod", "every": "daily", "at": "02:00"}, nil)
	now := time.Now()
	if n := e.api.RunDueBackups(t.Context(), now); n != 0 {
		t.Fatalf("新建 / 修改后到下一个周期前不应执行：%d", n)
	}
	// 把"上次执行"改到两天前，并让当前时间晚于今日 02:00
	must(t, e.api.Store.Update(func(st *storeState) error {
		for i := range st.Backups {
			st.Backups[i].LastRun = now.Add(-48 * time.Hour)
		}
		return nil
	}))
	at := time.Date(now.Year(), now.Month(), now.Day(), 2, 5, 0, 0, now.Location())
	if at.Before(now.Add(-24 * time.Hour)) {
		at = at.AddDate(0, 0, 1)
	}
	// 先让 ads 库上有一个进行中的备份
	e.expect(200, "POST", base+"/backup/jobs", map[string]any{"db": "ads", "snapshot": "busy_one", "repo": "repo_s3_prod"}, nil)
	if n := e.api.RunDueBackups(t.Context(), at); n != 1 {
		t.Fatalf("应执行 1 个到点的策略：%d", n)
	}
	got := pol()
	if !strings.Contains(got.LastResult, "dw 已提交") || !strings.Contains(got.LastResult, "ads 跳过") || !got.LastRun.Equal(at) {
		t.Fatalf("执行结果：%+v", got)
	}
	var ap struct{ Items []auditOut }
	e.expect(200, "GET", "/api/audit?code=backup.create&limit=10&q=定时备份", nil, &ap)
	var okRun, skipRun bool
	for _, it := range ap.Items {
		okRun = okRun || (it.User == "系统（备份策略）" && it.ResClass == "成功" && strings.Contains(it.SQL, "BACKUP SNAPSHOT `dw`.`auto_"))
		skipRun = skipRun || (strings.Contains(it.Act, "ads 跳过") && it.ResClass == "失败")
	}
	if !okRun || !skipRun {
		t.Fatalf("审计应记录定时备份的成功与跳过：%+v", ap.Items)
	}
	if n := e.api.RunDueBackups(t.Context(), at.Add(time.Minute)); n != 0 {
		t.Fatalf("同一周期不应重复执行：%d", n)
	}
	// 停用的策略不执行
	e.expect(200, "PUT", base+"/backup/policies/"+p.ID, map[string]any{"enabled": false}, nil)
	must(t, e.api.Store.Update(func(st *storeState) error { st.Backups[0].LastRun = now.Add(-72 * time.Hour); return nil }))
	if n := e.api.RunDueBackups(t.Context(), at.Add(24*time.Hour)); n != 0 {
		t.Fatalf("停用的策略不应执行：%d", n)
	}

	e.expect(200, "DELETE", base+"/backup/policies/"+p.ID, nil, nil)
	e.expect(404, "DELETE", base+"/backup/policies/"+p.ID, nil, nil)

	// 移除集群时一并删除其策略
	e.expect(200, "POST", base+"/backup/policies", map[string]any{"name": "x", "dbs": []string{"dw"}, "repo": "repo_s3_prod", "every": "daily", "at": "01:00"}, nil)
	e.expect(200, "DELETE", "/api/clusters/prod-test", nil, nil)
	e.api.Store.View(func(st *storeState) {
		if len(st.Backups) != 0 {
			t.Errorf("移除集群后策略应被清理：%+v", st.Backups)
		}
	})
}

func TestPolicySchedule(t *testing.T) {
	loc := time.Local
	daily := model.BackupPolicy{Every: "daily", At: "02:00"}
	for _, c := range []struct {
		now, last, next time.Time
	}{
		{time.Date(2026, 10, 8, 1, 0, 0, 0, loc), time.Date(2026, 10, 7, 2, 0, 0, 0, loc), time.Date(2026, 10, 8, 2, 0, 0, 0, loc)},
		{time.Date(2026, 10, 8, 2, 0, 0, 0, loc), time.Date(2026, 10, 8, 2, 0, 0, 0, loc), time.Date(2026, 10, 9, 2, 0, 0, 0, loc)},
		{time.Date(2026, 10, 8, 23, 59, 0, 0, loc), time.Date(2026, 10, 8, 2, 0, 0, 0, loc), time.Date(2026, 10, 9, 2, 0, 0, 0, loc)},
	} {
		if got := lastDue(daily, c.now); !got.Equal(c.last) {
			t.Errorf("daily lastDue(%v) = %v，期望 %v", c.now, got, c.last)
		}
		if got := nextRun(daily, c.now); !got.Equal(c.next) {
			t.Errorf("daily nextRun(%v) = %v，期望 %v", c.now, got, c.next)
		}
	}
	// 2026-10-08 是周四；每周日 03:30
	weekly := model.BackupPolicy{Every: "weekly", At: "03:30", Weekday: 0}
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, loc)
	if got, want := lastDue(weekly, now), time.Date(2026, 10, 4, 3, 30, 0, 0, loc); !got.Equal(want) {
		t.Errorf("weekly lastDue = %v，期望 %v", got, want)
	}
	if got, want := nextRun(weekly, now), time.Date(2026, 10, 11, 3, 30, 0, 0, loc); !got.Equal(want) {
		t.Errorf("weekly nextRun = %v，期望 %v", got, want)
	}
	// 当天是周日但时间未到：上一次是上周日
	sun := time.Date(2026, 10, 11, 1, 0, 0, 0, loc)
	if got, want := lastDue(weekly, sun), time.Date(2026, 10, 4, 3, 30, 0, 0, loc); !got.Equal(want) {
		t.Errorf("周日未到点 lastDue = %v，期望 %v", got, want)
	}
}

func TestBackupRBACAndReview(t *testing.T) {
	e, base := opsEnv(t, "problems")
	e.newUser("oncall3", []string{"r_oncall"}, "Oncall@2026xyzab")
	oc := e.firstLogin("oncall3", "Oncall@2026xyzab", "Oncall@2026xyzab2")
	oc.expect(403, "GET", base+"/backup", nil, nil) // 值班只读没有备份恢复菜单
	e.newUser("backup1", []string{"r_dba"}, "Backup@2026xyzab")
	// DBA 数据范围为空：看不到集群
	db := e.firstLogin("backup1", "Backup@2026xyzab", "Backup@2026xyzab2")
	db.expect(403, "GET", base+"/backup", nil, nil)

	// 双人复核：恢复先进入待复核
	e.expect(200, "PUT", "/api/settings/security", map[string]any{"noPermBtn": "disable", "highRiskConfirm": true, "dualReview": true, "sessionTimeout": 30, "pwdMinLen": 10, "pwdExpireDays": 90, "loginFailLock": 5, "ipWhitelist": ""}, nil)
	e.newUser("admin4", []string{"r_admin"}, "Admin4@2026xyzab")
	admin4 := e.firstLogin("admin4", "Admin4@2026xyzab", "Admin4@2026xyzab2")
	var pend map[string]any
	rs := map[string]any{"db": "dim", "snapshot": "snap_dim_20261001", "repo": "repo_hdfs_ro", "timestamp": "2026-10-01-03-00-00-789", "tables": []map[string]string{{"name": "dim_user", "as": "dim_user_new"}}}
	e.expect(202, "POST", base+"/backup/restore", rs, &pend)
	var o bkOverview
	e.expect(200, "GET", base+"/backup", nil, &o)
	if _, ok := o.job("restore", "dim"); ok {
		t.Fatal("待复核期间不应开始恢复")
	}
	var rv struct{ Items []reviewOut }
	e.expect(200, "GET", "/api/reviews", nil, &rv)
	if len(rv.Items) != 1 || rv.Items[0].Title != "恢复数据 dim.snap_dim_20261001" || rv.Items[0].Cluster == "" {
		t.Fatalf("复核申请：%+v", rv.Items)
	}
	var ap map[string]any
	admin4.expect(200, "POST", "/api/reviews/"+pend["reviewId"].(string)+"/approve", nil, &ap)
	if ap["ok"] != true {
		t.Fatalf("批准失败：%v", ap)
	}
	o = bkOverview{}
	e.expect(200, "GET", base+"/backup", nil, &o)
	if _, ok := o.job("restore", "dim"); !ok {
		t.Fatal("批准后应开始恢复")
	}
	// 发起备份 / 取消 / 创建仓库不属于高危，不走复核
	e.expect(200, "POST", base+"/backup/jobs", map[string]any{"db": "ods", "snapshot": "s_direct", "repo": "repo_s3_prod"}, nil)
}
