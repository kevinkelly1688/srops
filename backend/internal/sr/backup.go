// @author: kevinkelly
// @license: Apache Licence
// @file: backup.go
// @time: 2026/10/28

package sr

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ---------- 仓库 ----------

// Repo 是 SHOW REPOSITORIES 的一行。
type Repo struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	ReadOnly bool   `json:"readOnly"`
	Broker   string `json:"broker"`
	Created  string `json:"createTime"`
	Err      string `json:"err"`
}

// Repos 读取全部仓库。
func (c *Client) Repos(ctx context.Context) ([]Repo, error) {
	r, err := c.Query(ctx, "SHOW REPOSITORIES")
	if err != nil {
		return nil, err
	}
	var out []Repo
	for i := range r.Data {
		out = append(out, Repo{Name: r.Get(i, "RepoName"), Location: r.Get(i, "Location"), ReadOnly: isTrue(r.Get(i, "IsReadOnly")), Broker: r.Get(i, "Broker"),
			Created: r.Get(i, "CreateTime"), Err: r.Get(i, "ErrMsg")})
	}
	return out, nil
}

// Snapshot 是仓库中的一个快照。
type Snapshot struct {
	Name      string `json:"name"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"`
}

// Snapshots 读取仓库内的快照（SHOW SNAPSHOT ON repo）。
func (c *Client) Snapshots(ctx context.Context, repo string) ([]Snapshot, error) {
	q, ok := Quote(repo)
	if !ok {
		return nil, fmt.Errorf("仓库名不合法")
	}
	r, err := c.Query(ctx, "SHOW SNAPSHOT ON "+q)
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for i := range r.Data {
		out = append(out, Snapshot{Name: r.Get(i, "Snapshot"), Timestamp: r.Get(i, "Timestamp"), Status: r.Get(i, "Status")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp > out[j].Timestamp })
	return out, nil
}

// RepoSpec 是创建仓库的输入。密钥只用于拼接 SQL，平台不保存。
type RepoSpec struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // s3 / hdfs
	Location  string `json:"location"`
	ReadOnly  bool   `json:"readOnly"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	SSL       *bool  `json:"ssl"`
	PathStyle bool   `json:"pathStyle"`
	Broker    string `json:"broker"`   // HDFS 需要的 Broker 名称
	Username  string `json:"username"` // HDFS
	Password  string `json:"password"` // HDFS
}

var (
	locRe      = regexp.MustCompile(`^(s3a?|oss|cos|obs|gs|tos|hdfs|viewfs|azblob|wasbs?|abfss?|adl)://[A-Za-z0-9._\-/:@+=%]{1,500}$`)
	endpointRe = regexp.MustCompile(`^(https?://)?[A-Za-z0-9.\-]{1,253}(:\d{1,5})?$`)
	keyRe      = regexp.MustCompile(`^[A-Za-z0-9/+=_.\-]{1,256}$`)
)

// SQL 生成 CREATE REPOSITORY 语句。
func (s RepoSpec) SQL() (string, error) {
	q, ok := Quote(s.Name)
	if !ok {
		return "", fmt.Errorf("仓库名称只能包含字母、数字、下划线，且以字母或下划线开头")
	}
	if !locRe.MatchString(s.Location) {
		return "", fmt.Errorf("仓库位置格式不对，示例：s3a://bucket/path 或 hdfs://nn:8020/path")
	}
	var props []string
	add := func(k, v string) { props = append(props, fmt.Sprintf(`"%s" = %s`, k, MustLit(v))) }
	broker := "WITH BROKER"
	switch s.Type {
	case "s3":
		if !keyRe.MatchString(s.AccessKey) || !keyRe.MatchString(s.SecretKey) {
			return "", fmt.Errorf("请填写 Access Key 与 Secret Key（仅限字母、数字与 /+=_.- 符号）")
		}
		add("aws.s3.access_key", s.AccessKey)
		add("aws.s3.secret_key", s.SecretKey)
		if s.Endpoint != "" {
			if !endpointRe.MatchString(s.Endpoint) {
				return "", fmt.Errorf("Endpoint 格式不对")
			}
			add("aws.s3.endpoint", s.Endpoint)
		}
		if s.Region != "" {
			if !regexp.MustCompile(`^[a-z0-9\-]{2,32}$`).MatchString(s.Region) {
				return "", fmt.Errorf("Region 格式不对")
			}
			add("aws.s3.region", s.Region)
		}
		if s.SSL != nil {
			add("aws.s3.enable_ssl", fmt.Sprint(*s.SSL))
		}
		if s.PathStyle {
			add("aws.s3.enable_path_style_access", "true")
		}
	case "hdfs":
		if !ValidIdent(s.Broker) {
			return "", fmt.Errorf("HDFS 仓库需要指定 Broker 名称（SHOW BROKER 中的名称）")
		}
		broker = "WITH BROKER " + "`" + s.Broker + "`"
		if s.Username != "" {
			if !userRe.MatchString(s.Username) {
				return "", fmt.Errorf("HDFS 用户名不合法")
			}
			add("username", s.Username)
			if s.Password != "" {
				add("password", s.Password)
			}
		}
	default:
		return "", fmt.Errorf("仓库类型只支持 s3（含兼容 S3 的 OSS / COS / MinIO）与 hdfs")
	}
	ro := ""
	if s.ReadOnly {
		ro = "READ ONLY "
	}
	return fmt.Sprintf("CREATE %sREPOSITORY %s %s ON LOCATION %s PROPERTIES (%s)", ro, q, broker, MustLit(s.Location), strings.Join(props, ", ")), nil
}

// ---------- 备份 / 恢复作业 ----------

// BackupJob 是某个库最近一次的备份或恢复作业。
type BackupJob struct {
	Kind     string `json:"kind"` // backup / restore
	ID       string `json:"id"`
	DB       string `json:"db"`
	Snapshot string `json:"snapshot"`
	State    string `json:"state"`
	Objects  string `json:"objects"`
	Created  string `json:"createTime"`
	Finished string `json:"finishTime"`
	Progress string `json:"progress"`
	Err      string `json:"err"`
	Status   string `json:"status"`
	Active   bool   `json:"active"`
}

var sysDBs = map[string]bool{"information_schema": true, "sys": true, "_statistics_": true, "starrocks_monitor": true}

// Databases 列出用户库（去掉系统库）。
func (c *Client) Databases(ctx context.Context) ([]string, error) {
	r, err := c.Query(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, row := range r.Data {
		if len(row) > 0 && !sysDBs[row[0]] && ValidIdent(row[0]) {
			out = append(out, row[0])
		}
	}
	return out, nil
}

// BackupJobs 读取每个库最近的备份与恢复作业（SHOW BACKUP / SHOW RESTORE 只返回该库最近一次作业）。
func (c *Client) BackupJobs(ctx context.Context) (jobs []BackupJob, warns []string, err error) {
	dbs, err := c.Databases(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(dbs) > 200 {
		warns = append(warns, fmt.Sprintf("集群有 %d 个数据库，仅读取前 200 个的备份作业", len(dbs)))
		dbs = dbs[:200]
	}
	for _, db := range dbs {
		q, _ := Quote(db)
		if r, e := c.Query(ctx, "SHOW BACKUP FROM "+q); e != nil {
			warns = append(warns, "SHOW BACKUP FROM "+db+" 失败："+e.Error())
		} else {
			for i := range r.Data {
				j := BackupJob{Kind: "backup", ID: r.Get(i, "JobId"), DB: db, Snapshot: r.Get(i, "SnapshotName"), State: r.Get(i, "State"), Objects: r.Get(i, "BackupObjs"),
					Created: r.Get(i, "CreateTime"), Finished: r.Get(i, "FinishedTime"), Progress: r.Get(i, "Progress"), Err: r.Get(i, "TaskErrMsg"), Status: r.Get(i, "Status")}
				j.Active = j.State != "FINISHED" && j.State != "CANCELLED"
				jobs = append(jobs, j)
			}
		}
		if r, e := c.Query(ctx, "SHOW RESTORE FROM "+q); e != nil {
			warns = append(warns, "SHOW RESTORE FROM "+db+" 失败："+e.Error())
		} else {
			for i := range r.Data {
				j := BackupJob{Kind: "restore", ID: r.Get(i, "JobId"), DB: db, Snapshot: r.Get(i, "Label"), State: r.Get(i, "State"), Objects: r.Get(i, "RestoreObjs"),
					Created: r.Get(i, "CreateTime"), Finished: r.Get(i, "FinishedTime"), Progress: r.Get(i, "Progress"), Err: r.Get(i, "TaskErrMsg"), Status: r.Get(i, "Status")}
				j.Active = j.State != "FINISHED" && j.State != "CANCELLED"
				jobs = append(jobs, j)
			}
		}
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].Created > jobs[j].Created })
	return jobs, warns, nil
}

var (
	snapRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)
	tsRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2}-\d{3}$`)
)

// BackupSpec 是发起备份的输入。
type BackupSpec struct {
	DB       string   `json:"db"`
	Snapshot string   `json:"snapshot"`
	Repo     string   `json:"repo"`
	Tables   []string `json:"tables"` // 为空表示整库
}

// SQL 生成 BACKUP SNAPSHOT 语句。
func (s BackupSpec) SQL() (string, error) {
	qd, ok1 := Quote(s.DB)
	qr, ok2 := Quote(s.Repo)
	switch {
	case !ok1:
		return "", fmt.Errorf("数据库名不合法")
	case !ok2:
		return "", fmt.Errorf("仓库名不合法")
	case !snapRe.MatchString(s.Snapshot):
		return "", fmt.Errorf("快照名只能包含字母、数字、下划线（最长 128）")
	}
	on := ""
	if len(s.Tables) > 0 {
		var ts []string
		for _, t := range s.Tables {
			q, ok := Quote(strings.TrimSpace(t))
			if !ok {
				return "", fmt.Errorf("表名 %q 不合法", t)
			}
			ts = append(ts, q)
		}
		on = " ON (" + strings.Join(ts, ", ") + ")"
	}
	return fmt.Sprintf("BACKUP SNAPSHOT %s.`%s` TO %s%s", qd, s.Snapshot, qr, on), nil
}

// RestoreTable 是恢复对象，As 非空表示恢复为新表名。
type RestoreTable struct {
	Name string `json:"name"`
	As   string `json:"as"`
}

// RestoreSpec 是恢复的输入。
type RestoreSpec struct {
	DB          string         `json:"db"`
	Snapshot    string         `json:"snapshot"`
	Repo        string         `json:"repo"`
	Timestamp   string         `json:"timestamp"`
	Tables      []RestoreTable `json:"tables"`
	Replication int            `json:"replication"`
}

// SQL 生成 RESTORE SNAPSHOT 语句。
func (s RestoreSpec) SQL() (string, error) {
	qd, ok1 := Quote(s.DB)
	qr, ok2 := Quote(s.Repo)
	switch {
	case !ok1:
		return "", fmt.Errorf("数据库名不合法")
	case !ok2:
		return "", fmt.Errorf("仓库名不合法")
	case !snapRe.MatchString(s.Snapshot):
		return "", fmt.Errorf("快照名不合法")
	case !tsRe.MatchString(s.Timestamp):
		return "", fmt.Errorf("快照时间戳格式应为 yyyy-MM-dd-HH-mm-ss-SSS（可从仓库的快照列表中选择）")
	case s.Replication < 0 || s.Replication > 10:
		return "", fmt.Errorf("副本数须为 1–10")
	}
	on := ""
	if len(s.Tables) > 0 {
		var ts []string
		for _, t := range s.Tables {
			n, ok := Quote(strings.TrimSpace(t.Name))
			if !ok {
				return "", fmt.Errorf("表名 %q 不合法", t.Name)
			}
			if t.As != "" {
				a, ok := Quote(strings.TrimSpace(t.As))
				if !ok {
					return "", fmt.Errorf("新表名 %q 不合法", t.As)
				}
				n += " AS " + a
			}
			ts = append(ts, n)
		}
		on = " ON (" + strings.Join(ts, ", ") + ")"
	}
	props := fmt.Sprintf(`"backup_timestamp" = "%s"`, s.Timestamp)
	if s.Replication > 0 {
		props += fmt.Sprintf(`, "replication_num" = "%d"`, s.Replication)
	}
	return fmt.Sprintf("RESTORE SNAPSHOT %s.`%s` FROM %s%s PROPERTIES (%s)", qd, s.Snapshot, qr, on, props), nil
}

// CancelSQL 生成取消备份 / 恢复的语句。
func CancelSQL(kind, db string) (string, error) {
	q, ok := Quote(db)
	if !ok {
		return "", fmt.Errorf("数据库名不合法")
	}
	switch kind {
	case "backup":
		return "CANCEL BACKUP FROM " + q, nil
	case "restore":
		return "CANCEL RESTORE FROM " + q, nil
	}
	return "", fmt.Errorf("作业类型只能是 backup 或 restore")
}

// DropRepoSQL 生成删除仓库语句。
func DropRepoSQL(name string) (string, error) {
	q, ok := Quote(name)
	if !ok {
		return "", fmt.Errorf("仓库名不合法")
	}
	return "DROP REPOSITORY " + q, nil
}

// HasActiveJob 判断库上是否有进行中的备份或恢复作业（同一库同时只能有一个）。
func (c *Client) HasActiveJob(ctx context.Context, db string) (bool, string, error) {
	q, ok := Quote(db)
	if !ok {
		return false, "", fmt.Errorf("数据库名不合法")
	}
	for _, kind := range []string{"BACKUP", "RESTORE"} {
		r, err := c.Query(ctx, "SHOW "+kind+" FROM "+q)
		if err != nil {
			return false, "", err
		}
		for i := range r.Data {
			if st := r.Get(i, "State"); st != "FINISHED" && st != "CANCELLED" && st != "" {
				return true, strings.ToLower(kind) + " 作业 " + r.Get(i, "JobId") + "（" + st + "）", nil
			}
		}
	}
	return false, "", nil
}
