// @author: kevinkelly
// @license: Apache Licence
// @file: sim_ops.go
// @time: 2026/10/28

package fakesr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-mysql-org/go-mysql/mysql"
)

type fakeSnap struct{ name, ts string }

type fakeRepo struct {
	name, loc string
	ro        bool
	created   string
	snaps     []fakeSnap
}

type fakeBJob struct {
	kind, id, db, snap, repo, state, objs, created, finished, errMsg string
	started                                                          time.Time
}

// opsSim 是备份恢复与 BE 配置的模拟状态（挂在 sim 上）。
type opsSim struct {
	c      *Cluster
	repos  []*fakeRepo
	jobs   []*fakeBJob // 每个库每种类型只保留最近一次
	beCfg  map[string]map[string]string
	nextID int
}

var beCfgDefs = []struct {
	k, v, typ string
	mutable   bool
}{
	{"cumulative_compaction_num_threads_per_disk", "1", "int", true},
	{"base_compaction_num_threads_per_disk", "1", "int", true},
	{"max_compaction_concurrency", "-1", "int", true},
	{"disable_storage_page_cache", "false", "bool", true},
	{"mem_limit", "90%", "string", false},
	{"be_http_port", "8040", "int", false},
}

func newOpsSim(c *Cluster) *opsSim {
	o := &opsSim{c: c, beCfg: map[string]map[string]string{}, nextID: 70000}
	o.repos = []*fakeRepo{
		{name: "repo_s3_prod", loc: "s3a://sr-backup/prod-sr-01", created: "2026-08-01 10:00:00", snaps: []fakeSnap{{"snap_dws_20261005", "2026-10-05-02-00-01-123"}, {"snap_ads_20261006", "2026-10-06-02-00-02-456"}}},
		{name: "repo_hdfs_ro", loc: "hdfs://nn1:8020/backup/sr", ro: true, created: "2026-07-01 09:00:00", snaps: []fakeSnap{{"snap_dim_20261001", "2026-10-01-03-00-00-789"}}},
	}
	return o
}

func (c *Cluster) beNodeKey(n *node) string { return fmt.Sprintf("%s%d", n.kind, n.idx) }

// initBECfg 在集群启动后为每个 BE / CN 建立配置副本（problems 场景下 be-5 有一项参数不一致）。
func (c *Cluster) initBECfg() {
	o := c.sim.ops
	for _, n := range append(c.nodesOf(kindBE), c.nodesOf(kindCN)...) {
		m := map[string]string{}
		for _, d := range beCfgDefs {
			m[d.k] = d.v
		}
		if c.problems && n.kind == kindBE && n.idx == 3 {
			m["cumulative_compaction_num_threads_per_disk"] = "2"
		}
		o.beCfg[c.beNodeKey(n)] = m
	}
}

func (c *Cluster) beID(n *node) string {
	if n.kind == kindBE {
		return fmt.Sprint(10001 + n.idx)
	}
	return fmt.Sprint(20001 + n.idx)
}

func (c *Cluster) beConfigsSim() (*mysql.Result, error) {
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	var rows [][]any
	for _, n := range append(c.nodesOf(kindBE), c.nodesOf(kindCN)...) {
		m := c.sim.ops.beCfg[c.beNodeKey(n)]
		for _, d := range beCfgDefs {
			rows = append(rows, []any{c.beID(n), d.k, m[d.k], d.typ, boolStr(d.mutable)})
		}
	}
	return resultset([]string{"BE_ID", "NAME", "VALUE", "TYPE", "IS_MUTABLE"}, rows)
}

// updateConfigHandler 模拟 BE / CN 的 POST /api/update_config?key=value。
func (c *Cluster) updateConfigHandler(n *node) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		reply := func(status, msg string) {
			_ = json.NewEncoder(rw).Encode(map[string]string{"status": status, "msg": msg})
		}
		if r.Method != http.MethodPost {
			rw.WriteHeader(http.StatusMethodNotAllowed)
			reply("FAIL", "POST only")
			return
		}
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		m := c.sim.ops.beCfg[c.beNodeKey(n)]
		for k, vs := range r.URL.Query() {
			found := false
			for _, d := range beCfgDefs {
				if d.k != k {
					continue
				}
				found = true
				if !d.mutable {
					reply("INTERNAL_ERROR", "set config "+k+" failed, config is not mutable")
					return
				}
				m[k] = vs[0]
			}
			if !found {
				reply("NOT_FOUND", "config "+k+" not found")
				return
			}
		}
		reply("OK", "")
	}
}

// ---------- 备份恢复 ----------

var (
	reRepoCreate  = regexp.MustCompile(`(?is)^create (read only )?repository (\w+) with broker(?: (\w+))? on location "([^"]+)" properties \((.*)\)$`)
	reRepoDrop    = regexp.MustCompile(`(?is)^drop repository (\w+)$`)
	reSnapOn      = regexp.MustCompile(`(?is)^show snapshot on (\w+)$`)
	reShowBackup  = regexp.MustCompile(`(?is)^show (backup|restore) from (\w+)$`)
	reBackup      = regexp.MustCompile(`(?is)^backup snapshot (\w+)\.(\w+) to (\w+)(?: on \((.*?)\))?$`)
	reRestore     = regexp.MustCompile(`(?is)^restore snapshot (\w+)\.(\w+) from (\w+)(?: on \((.*?)\))? properties \((.*)\)$`)
	reCancelB     = regexp.MustCompile(`(?is)^cancel (backup|restore) from (\w+)$`)
	userDatabases = []string{"ods", "dw", "ads", "dim", "tmp"}
)

func (o *opsSim) repo(name string) *fakeRepo {
	for _, r := range o.repos {
		if r.name == name {
			return r
		}
	}
	return nil
}

func hasDB(db string) bool {
	for _, d := range userDatabases {
		if d == db {
			return true
		}
	}
	return false
}

// settle 推进作业状态：创建 1.5 秒后完成；备份完成后快照出现在仓库里。
func (o *opsSim) settle() {
	for _, j := range o.jobs {
		if j.state == "FINISHED" || j.state == "CANCELLED" || time.Since(j.started) < 1500*time.Millisecond {
			continue
		}
		j.state, j.finished = "FINISHED", time.Now().Format(tsLayout)
		if j.kind == "backup" {
			if r := o.repo(j.repo); r != nil {
				r.snaps = append(r.snaps, fakeSnap{j.snap, j.started.Format("2006-01-02-15-04-05") + "-000"})
				o.c.seedSnapshotS3(r, j.snap)
			}
		}
	}
}

func (o *opsSim) active(db string) *fakeBJob {
	o.settle()
	for _, j := range o.jobs {
		if j.db == db && j.state != "FINISHED" && j.state != "CANCELLED" {
			return j
		}
	}
	return nil
}

func (o *opsSim) put(j *fakeBJob) {
	for i, x := range o.jobs {
		if x.db == j.db && x.kind == j.kind {
			o.jobs[i] = j
			return
		}
	}
	o.jobs = append(o.jobs, j)
}

// handleOps 处理备份恢复相关语句。
func (c *Cluster) handleOps(q, l string) (*mysql.Result, bool, error) {
	o := c.sim.ops
	switch {
	case l == "show databases":
		dbs := append([]string{"information_schema", "sys", "_statistics_"}, userDatabases...)
		rows := make([][]any, len(dbs))
		for i, d := range dbs {
			rows[i] = []any{d}
		}
		res, err := resultset([]string{"Database"}, rows)
		return res, true, err
	case l == "show repositories":
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		var rows [][]any
		for i, r := range o.repos {
			rows = append(rows, []any{fmt.Sprint(i + 1), r.name, r.created, boolStr(r.ro), r.loc, "", ""})
		}
		res, err := resultset([]string{"RepoId", "RepoName", "CreateTime", "IsReadOnly", "Location", "Broker", "ErrMsg"}, rows)
		return res, true, err
	case reRepoCreate.MatchString(q):
		m := reRepoCreate.FindStringSubmatch(q)
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		if o.repo(m[2]) != nil {
			return nil, true, sqlErr("Repository %s already exist", m[2])
		}
		if !strings.Contains(m[5], `"aws.s3.access_key"`) && !strings.Contains(m[5], `"username"`) && m[5] != "" {
			return nil, true, sqlErr("Failed to create repository: missing credentials")
		}
		o.repos = append(o.repos, &fakeRepo{name: m[2], loc: m[4], ro: strings.TrimSpace(m[1]) != "", created: time.Now().Format(tsLayout)})
		return okResult(), true, nil
	case reRepoDrop.MatchString(q):
		name := reRepoDrop.FindStringSubmatch(q)[1]
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		for i, r := range o.repos {
			if r.name == name {
				o.repos = append(o.repos[:i], o.repos[i+1:]...)
				return okResult(), true, nil
			}
		}
		return nil, true, sqlErr("Repository %s does not exist", name)
	case reSnapOn.MatchString(q):
		name := reSnapOn.FindStringSubmatch(q)[1]
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		o.settle()
		r := o.repo(name)
		if r == nil {
			return nil, true, sqlErr("Repository %s does not exist", name)
		}
		var rows [][]any
		for _, s := range r.snaps {
			if !c.snapExists(r, s.name) {
				continue
			}
			rows = append(rows, []any{s.name, s.ts, "OK"})
		}
		res, err := resultset([]string{"Snapshot", "Timestamp", "Status"}, rows)
		return res, true, err
	case reShowBackup.MatchString(q):
		m := reShowBackup.FindStringSubmatch(q)
		kind, db := strings.ToLower(m[1]), m[2]
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		o.settle()
		if !hasDB(db) {
			return nil, true, sqlErr("Unknown database '%s'", db)
		}
		var rows [][]any
		var cols []string
		for _, j := range o.jobs {
			if j.db != db || j.kind != kind {
				continue
			}
			prog := "100%"
			if j.state != "FINISHED" && j.state != "CANCELLED" {
				prog = "35%"
			}
			if kind == "backup" {
				rows = append(rows, []any{j.id, j.snap, j.db, j.state, j.objs, j.created, j.created, j.finished, j.finished, "0", prog, j.errMsg, "[OK]", "86400000"})
			} else {
				rows = append(rows, []any{j.id, j.snap, "2026-10-01-03-00-00-789", j.db, j.state, "false", "3", j.objs, j.created, j.created, j.created, j.finished, j.finished, "0", prog, j.errMsg, "[OK]", "86400000"})
			}
		}
		if kind == "backup" {
			cols = []string{"JobId", "SnapshotName", "DbName", "State", "BackupObjs", "CreateTime", "SnapshotFinishedTime", "UploadFinishedTime", "FinishedTime", "UnfinishedTasks", "Progress", "TaskErrMsg", "Status", "Timeout"}
		} else {
			cols = []string{"JobId", "Label", "Timestamp", "DbName", "State", "AllowLoad", "ReplicationNum", "RestoreObjs", "CreateTime", "MetaPreparedTime", "SnapshotFinishedTime", "DownloadFinishedTime", "FinishedTime", "UnfinishedTasks", "Progress", "TaskErrMsg", "Status", "Timeout"}
		}
		res, err := resultset(cols, rows)
		return res, true, err
	case reBackup.MatchString(q):
		m := reBackup.FindStringSubmatch(q)
		db, snap, repo := m[1], m[2], m[3]
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		if !hasDB(db) {
			return nil, true, sqlErr("Unknown database '%s'", db)
		}
		r := o.repo(repo)
		switch {
		case r == nil:
			return nil, true, sqlErr("Repository %s does not exist", repo)
		case r.ro:
			return nil, true, sqlErr("Repository %s is read only", repo)
		case o.active(db) != nil:
			return nil, true, sqlErr("Can only run one backup or restore job of a database at same time")
		}
		o.nextID++
		objs := m[4]
		if objs == "" {
			objs = "[全部表]"
		}
		o.put(&fakeBJob{kind: "backup", id: fmt.Sprint(o.nextID), db: db, snap: snap, repo: repo, state: "UPLOADING", objs: objs, created: time.Now().Format(tsLayout), started: time.Now()})
		return okResult(), true, nil
	case reRestore.MatchString(q):
		m := reRestore.FindStringSubmatch(q)
		db, snap, repo := m[1], m[2], m[3]
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		if !hasDB(db) {
			return nil, true, sqlErr("Unknown database '%s'", db)
		}
		r := o.repo(repo)
		if r == nil {
			return nil, true, sqlErr("Repository %s does not exist", repo)
		}
		ts := regexp.MustCompile(`"backup_timestamp"\s*=\s*"([^"]*)"`).FindStringSubmatch(m[5])
		found := false
		for _, s := range r.snaps {
			found = found || (s.name == snap && ts != nil && s.ts == ts[1])
		}
		if !found {
			return nil, true, sqlErr("Failed to get info of snapshot '%s' because it does not exist in repository %s", snap, repo)
		}
		if o.active(db) != nil {
			return nil, true, sqlErr("Can only run one backup or restore job of a database at same time")
		}
		o.nextID++
		objs := m[4]
		if objs == "" {
			objs = "[全部表]"
		}
		o.put(&fakeBJob{kind: "restore", id: fmt.Sprint(o.nextID), db: db, snap: snap, repo: repo, state: "DOWNLOADING", objs: objs, created: time.Now().Format(tsLayout), started: time.Now()})
		return okResult(), true, nil
	case reCancelB.MatchString(q):
		m := reCancelB.FindStringSubmatch(q)
		c.sim.mu.Lock()
		defer c.sim.mu.Unlock()
		if j := o.active(m[2]); j != nil && j.kind == strings.ToLower(m[1]) {
			j.state, j.finished = "CANCELLED", time.Now().Format(tsLayout)
			return okResult(), true, nil
		}
		return nil, true, sqlErr("No running %s job in database %s", strings.ToLower(m[1]), m[2])
	}
	return nil, false, nil
}

// SnapshotNames 供测试使用：返回仓库内的快照名（已排序）。
func (c *Cluster) SnapshotNames(repo string) []string {
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	c.sim.ops.settle()
	var out []string
	if r := c.sim.ops.repo(repo); r != nil {
		for _, s := range r.snaps {
			if c.snapExists(r, s.name) {
				out = append(out, s.name)
			}
		}
	}
	sort.Strings(out)
	return out
}
