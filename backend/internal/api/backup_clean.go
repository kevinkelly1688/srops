// @author: kevinkelly
// @license: Apache Licence
// @file: backup_clean.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/s3x"
	"starrocks-ops/internal/sr"
	"starrocks-ops/internal/store"
)

// 备份快照清理。
//
// StarRocks 没有删除仓库中快照的语句（DROP REPOSITORY 只删除仓库映射），过期的快照只能删除对象存储里的快照目录：
//
//	<仓库位置>/__starrocks_repository_<仓库名>/__ss_<快照名>/
//
// 所以清理需要仓库所在对象存储的访问凭据（创建仓库时填写的密钥平台不保存），在「清理设置」里单独配置，加密保存。
// 目前支持 S3 协议的存储（s3 / s3a / oss / cos / obs / tos），HDFS 等暂不支持。

const cleanCode = "backup.clean"

var (
	snapNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-]{0,127}$`)
	credEPRe   = regexp.MustCompile(`^(https?://)?[A-Za-z0-9.\-]{1,253}(:\d{1,5})?(/[A-Za-z0-9._\-/]*)?$`)
	credKeyRe  = regexp.MustCompile(`^[A-Za-z0-9/+=_.\-]{1,256}$`)
	regionRe   = regexp.MustCompile(`^[a-z0-9\-]{0,32}$`)
)

const (
	maxAutoDelete   = 10 // 定时清理每次最多删除的快照数
	maxManualDelete = 50
	cleanEvery      = time.Hour
)

type repoLoc struct{ Bucket, Base string }

func parseRepoLoc(location string) (repoLoc, error) {
	u, err := url.Parse(location)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return repoLoc{}, bad("仓库位置格式不正确")
	}
	switch u.Scheme {
	case "s3", "s3a", "oss", "cos", "obs", "tos":
		return repoLoc{Bucket: u.Host, Base: strings.Trim(u.Path, "/")}, nil
	}
	return repoLoc{}, bad("仓库位置 " + u.Scheme + ":// 暂不支持自动清理：目前只支持 S3 协议的对象存储（s3 / s3a / oss / cos / obs / tos）")
}

func (l repoLoc) repoPrefix(repo string) string {
	p := "__starrocks_repository_" + repo + "/"
	if l.Base != "" {
		p = l.Base + "/" + p
	}
	return p
}

func (l repoLoc) snapPrefix(repo, snap string) string {
	return l.repoPrefix(repo) + "__ss_" + snap + "/"
}

func (l repoLoc) String(repo string) string {
	return "s3://" + l.Bucket + "/" + l.repoPrefix(repo)
}

// ---------- 凭据 ----------

func (s *Server) repoCred(cluster, repo string) (model.RepoCred, bool) {
	var rc model.RepoCred
	found := false
	s.Store.View(func(st *store.State) {
		for _, x := range st.RepoCreds {
			if x.Cluster == cluster && x.Repo == repo {
				rc, found = x, true
			}
		}
	})
	return rc, found
}

func (s *Server) s3Client(rc model.RepoCred) (*s3x.Client, error) {
	ak, err := s.Box.Decrypt(rc.AKEnc)
	if err != nil {
		return nil, fmt.Errorf("清理凭据解密失败：%v", err)
	}
	sk, err := s.Box.Decrypt(rc.SKEnc)
	if err != nil {
		return nil, fmt.Errorf("清理凭据解密失败：%v", err)
	}
	return &s3x.Client{Endpoint: rc.Endpoint, Region: rc.Region, AccessKey: ak, SecretKey: sk, PathStyle: rc.PathStyle, HTTP: &http.Client{Timeout: 60 * time.Second}}, nil
}

func maskKey(k string) string {
	if len(k) <= 6 {
		return strings.Repeat("*", len(k))
	}
	return k[:3] + "****" + k[len(k)-3:]
}

type credView struct {
	Configured bool      `json:"configured"`
	Endpoint   string    `json:"endpoint,omitempty"`
	Region     string    `json:"region,omitempty"`
	PathStyle  bool      `json:"pathStyle"`
	AKMasked   string    `json:"akMasked,omitempty"`
	By         string    `json:"by,omitempty"`
	At         time.Time `json:"at,omitempty"`
}

func viewCred(rc model.RepoCred, found bool) credView {
	if !found {
		return credView{}
	}
	return credView{Configured: true, Endpoint: rc.Endpoint, Region: rc.Region, PathStyle: rc.PathStyle, AKMasked: rc.AKMasked, By: rc.By, At: rc.At}
}

// findRepo 在集群中查找仓库。
func findRepo(ctx context.Context, cl *sr.Client, name string) (sr.Repo, error) {
	repos, err := cl.Repos(ctx)
	if err != nil {
		return sr.Repo{}, apiErr{http.StatusBadGateway, "读取仓库失败：" + err.Error()}
	}
	for _, r := range repos {
		if r.Name == name {
			return r, nil
		}
	}
	return sr.Repo{}, missing("仓库 " + name + " 不存在")
}

// GET .../backup/repos/{repo}/cleanup：该仓库是否支持清理、清理凭据状态。
func (s *Server) cleanupInfo(w http.ResponseWriter, r *http.Request, u *model.User) {
	repo := r.PathValue("repo")
	if !sr.ValidIdent(repo) {
		fail(w, http.StatusBadRequest, "仓库名不合法")
		return
	}
	x, done, ok2 := s.open(w, r, 30*time.Second)
	if !ok2 {
		return
	}
	defer done()
	rp, err := findRepo(x.ctx, x.cl, repo)
	if err != nil {
		writeErr(w, err)
		return
	}
	rc, found := s.repoCred(x.c.ID, repo)
	out := map[string]any{"cred": viewCred(rc, found), "supported": true}
	if loc, err := parseRepoLoc(rp.Location); err != nil {
		out["supported"], out["reason"] = false, err.Error()
	} else {
		out["dir"] = loc.String(repo)
	}
	ok(w, out)
}

type credIn struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	PathStyle bool   `json:"pathStyle"`
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
}

// PUT .../backup/repos/{repo}/cleanup：保存清理凭据（先用它列出仓库目录验证，通过才保存）。
func (s *Server) putCleanCred(w http.ResponseWriter, r *http.Request, u *model.User) {
	repo := r.PathValue("repo")
	if !sr.ValidIdent(repo) {
		fail(w, http.StatusBadRequest, "仓库名不合法")
		return
	}
	var in credIn
	if !decode(w, r, &in) {
		return
	}
	in.Endpoint, in.Region, in.AccessKey = strings.TrimSpace(in.Endpoint), strings.TrimSpace(in.Region), strings.TrimSpace(in.AccessKey)
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	err := func() error {
		rp, err := findRepo(x.ctx, x.cl, repo)
		if err != nil {
			return err
		}
		loc, err := parseRepoLoc(rp.Location)
		if err != nil {
			return err
		}
		old, had := s.repoCred(x.c.ID, repo)
		switch {
		case !credEPRe.MatchString(in.Endpoint):
			return bad("请填写对象存储的访问端点，例如 https://s3.cn-north-1.amazonaws.com.cn 或 http://minio:9000")
		case !regionRe.MatchString(in.Region):
			return bad("地域格式不正确，例如 cn-north-1")
		case in.AccessKey != "" && !credKeyRe.MatchString(in.AccessKey), in.SecretKey != "" && !credKeyRe.MatchString(in.SecretKey):
			return bad("AccessKey / SecretKey 格式不正确")
		}
		var ak, sk, oldAK string
		if had {
			if oldAK, err = s.Box.Decrypt(old.AKEnc); err != nil {
				return err
			}
		}
		if in.AccessKey == "" {
			if !had {
				return bad("请填写 AccessKey")
			}
			ak = oldAK
		} else {
			ak = in.AccessKey
		}
		if in.SecretKey == "" {
			if !had || ak != oldAK { // 换了 AccessKey 必须同时填写对应的 SecretKey
				return bad("请填写 SecretKey")
			}
			if sk, err = s.Box.Decrypt(old.SKEnc); err != nil {
				return err
			}
		} else {
			sk = in.SecretKey
		}
		cli := &s3x.Client{Endpoint: in.Endpoint, Region: in.Region, AccessKey: ak, SecretKey: sk, PathStyle: in.PathStyle, HTTP: &http.Client{Timeout: 30 * time.Second}}
		if _, _, err := cli.List(x.ctx, loc.Bucket, loc.repoPrefix(repo), 1); err != nil {
			return apiErr{http.StatusBadGateway, "验证失败（没有保存）：" + err.Error()}
		}
		rc := model.RepoCred{Cluster: x.c.ID, Repo: repo, Endpoint: in.Endpoint, Region: in.Region, PathStyle: in.PathStyle, AKMasked: maskKey(ak), By: u.Login, At: time.Now()}
		if rc.AKEnc, err = s.Box.Encrypt(ak); err != nil {
			return err
		}
		if rc.SKEnc, err = s.Box.Encrypt(sk); err != nil {
			return err
		}
		return s.Store.Update(func(st *store.State) error {
			for i := range st.RepoCreds {
				if st.RepoCreds[i].Cluster == x.c.ID && st.RepoCreds[i].Repo == repo {
					st.RepoCreds[i] = rc
					return nil
				}
			}
			st.RepoCreds = append(st.RepoCreds, rc)
			return nil
		})
	}()
	s.auditSQL(r, u, cleanCode, "设置仓库 "+repo+" 的清理凭据（AccessKey "+maskKey(in.AccessKey)+"）", "", err, x.c.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	rc, found := s.repoCred(x.c.ID, repo)
	ok(w, viewCred(rc, found))
}

// DELETE .../backup/repos/{repo}/cleanup：删除清理凭据；仍在使用该凭据的保留策略会被拒绝。
func (s *Server) deleteCleanCred(w http.ResponseWriter, r *http.Request, u *model.User) {
	cid, repo := r.PathValue("id"), r.PathValue("repo")
	err := s.Store.Update(func(st *store.State) error {
		for _, p := range st.Backups {
			if p.Cluster == cid && p.Repo == repo && (p.KeepLast > 0 || p.KeepDays > 0) {
				return conflict("备份策略 " + p.Name + " 还在使用保留策略清理该仓库，请先关闭它的保留策略")
			}
		}
		for i, x := range st.RepoCreds {
			if x.Cluster == cid && x.Repo == repo {
				st.RepoCreds = append(st.RepoCreds[:i], st.RepoCreds[i+1:]...)
				return nil
			}
		}
		return missing("没有配置清理凭据")
	})
	s.auditSQL(r, u, cleanCode, "删除仓库 "+repo+" 的清理凭据", "", err, cid)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// ---------- 保留策略计划 ----------

type snapAt struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"`
}

func policyPrefix(p model.BackupPolicy) string {
	return "auto_" + strings.TrimPrefix(p.ID, "bp_") + "_"
}

// parseSnapTS 解析 SHOW SNAPSHOT 的 Timestamp（2026-10-05-02-00-01-123）。
func parseSnapTS(ts string) (time.Time, bool) {
	if len(ts) < 19 {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02-15-04-05", ts[:19], time.Local)
	return t, err == nil
}

// planRetention 按保留策略把策略创建的快照分为保留和待删除两组（待删除按从旧到新排序）。
// 无法解析时间或状态不是 OK 的快照一律保留。
func planRetention(p model.BackupPolicy, snaps []sr.Snapshot, now time.Time) (keep, remove []snapAt, skipped int) {
	var list []snapAt
	pre := policyPrefix(p)
	for _, sn := range snaps {
		if !strings.HasPrefix(sn.Name, pre) {
			continue
		}
		at, ok := parseSnapTS(sn.Timestamp)
		if !ok || (sn.Status != "" && !strings.EqualFold(sn.Status, "OK")) {
			skipped++
			continue
		}
		list = append(list, snapAt{sn.Name, at})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].At.After(list[j].At) })
	if p.KeepLast <= 0 && p.KeepDays <= 0 {
		return list, nil, skipped
	}
	for i, sn := range list {
		del := i > 0 && (p.KeepLast <= 0 || i >= p.KeepLast) && (p.KeepDays <= 0 || now.Sub(sn.At) > time.Duration(p.KeepDays)*24*time.Hour)
		if del {
			remove = append(remove, sn)
		} else {
			keep = append(keep, sn)
		}
	}
	for i, j := 0, len(remove)-1; i < j; i, j = i+1, j-1 { // 从旧到新
		remove[i], remove[j] = remove[j], remove[i]
	}
	return keep, remove, skipped
}

func (s *Server) policyOf(cid, pid string) (model.BackupPolicy, bool) {
	var p model.BackupPolicy
	found := false
	s.Store.View(func(st *store.State) {
		for _, x := range st.Backups {
			if x.ID == pid && x.Cluster == cid {
				p, found = x, true
			}
		}
	})
	return p, found
}

// GET .../backup/policies/{pid}/clean-preview：按当前保留策略，哪些快照会保留、哪些会删除（只读，不删除）。
func (s *Server) cleanPreview(w http.ResponseWriter, r *http.Request, u *model.User) {
	p, found := s.policyOf(r.PathValue("id"), r.PathValue("pid"))
	if !found {
		fail(w, http.StatusNotFound, "备份策略不存在")
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	snaps, err := x.cl.Snapshots(x.ctx, p.Repo)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取快照失败："+err.Error())
		return
	}
	keep, remove, skipped := planRetention(p, snaps, time.Now())
	_, hasCred := s.repoCred(x.c.ID, p.Repo)
	if keep == nil {
		keep = []snapAt{}
	}
	if remove == nil {
		remove = []snapAt{}
	}
	ok(w, map[string]any{"keepLast": p.KeepLast, "keepDays": p.KeepDays, "keep": keep, "remove": remove, "skipped": skipped, "credReady": hasCred, "prefix": policyPrefix(p)})
}

// ---------- 执行 ----------

type cleanCtx struct {
	c    model.Cluster
	cl   *sr.Client
	repo string
	loc  repoLoc
	rc   model.RepoCred
}

// prepareClean 检查仓库、凭据，以及是否有正在进行的恢复作业（恢复时不能删除仓库里的快照）。
func (s *Server) prepareClean(ctx context.Context, c model.Cluster, cl *sr.Client, repo string) (*cleanCtx, error) {
	rp, err := findRepo(ctx, cl, repo)
	if err != nil {
		return nil, err
	}
	loc, err := parseRepoLoc(rp.Location)
	if err != nil {
		return nil, err
	}
	rc, found := s.repoCred(c.ID, repo)
	if !found {
		return nil, conflict("请先为仓库 " + repo + " 配置清理凭据（仓库列表 → 清理设置）")
	}
	jobs, _, err := cl.BackupJobs(ctx)
	if err != nil {
		return nil, apiErr{http.StatusBadGateway, "检查备份 / 恢复作业失败：" + err.Error()}
	}
	for _, j := range jobs {
		if j.Kind == "restore" && j.Active {
			return nil, conflict("数据库 " + j.DB + " 上有进行中的恢复作业，恢复结束前不能清理快照")
		}
	}
	return &cleanCtx{c: c, cl: cl, repo: repo, loc: loc, rc: rc}, nil
}

// execClean 在后台任务里逐个删除快照目录。
func (s *Server) execClean(t *taskRun, cc *cleanCtx, names []string, who, ip string) error {
	cli, err := s.s3Client(cc.rc)
	if err != nil {
		return err
	}
	t.Logf("仓库 %s：%s", cc.repo, cc.loc.String(cc.repo))
	t.Logf("准备删除 %d 个快照：%s", len(names), strings.Join(names, "、"))
	var failed []string
	for i, name := range names {
		if t.Cancelled() {
			return fmt.Errorf("已取消")
		}
		prefix := cc.loc.snapPrefix(cc.repo, name)
		t.Logf("[%d/%d] 删除快照 %s：s3://%s/%s", i+1, len(names), name, cc.loc.Bucket, prefix)
		last := 0
		n, err := cli.DeletePrefix(t.ctx, cc.loc.Bucket, prefix, func(d int) {
			if d-last >= 1000 {
				t.Logf("    已删除 %d 个文件", d)
				last = d
			}
		})
		res := "成功"
		if err != nil {
			res = "失败：" + err.Error()
			failed = append(failed, name)
			t.Logf("    失败（已删除 %d 个文件）：%v", n, err)
		} else {
			t.Logf("    完成，共删除 %d 个文件", n)
		}
		t.SetProg((i + 1) * 100 / len(names))
		s.Store.Audit(store.AuditEntry{User: who, IP: ip, Cluster: cc.c.ID, Code: cleanCode, Act: fmt.Sprintf("删除备份快照 %s / %s（%d 个文件，任务 %s）", cc.repo, name, n, t.ID()), Result: res,
			SQL: "S3 DELETE s3://" + cc.loc.Bucket + "/" + prefix + "*"})
	}
	// 确认：快照已不在 SHOW SNAPSHOT 中
	cctx, cancel := context.WithTimeout(t.ctx, 30*time.Second)
	defer cancel()
	if left, err := cc.cl.Snapshots(cctx, cc.repo); err == nil {
		var still []string
		for _, sn := range left {
			if contains(names, sn.Name) && !contains(failed, sn.Name) {
				still = append(still, sn.Name)
			}
		}
		if len(still) > 0 {
			t.Logf("提示：文件已删除，但 SHOW SNAPSHOT 仍列出 %s（可能是 FE 缓存或对象存储延迟，稍后刷新确认）", strings.Join(still, "、"))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d 个快照删除失败：%s", len(failed), strings.Join(failed, "、"))
	}
	t.Logf("清理完成")
	return nil
}

// startClean 创建后台清理任务。
func (s *Server) startClean(cc *cleanCtx, names []string, who, ip, title string) (*taskRun, error) {
	if !s.lockNode(cc.c.ID, "repo", cc.repo) {
		return nil, conflict("仓库 " + cc.repo + " 正在清理，请等待完成")
	}
	t := s.newTask(cleanCode, cc.c.ID, title, who, map[string]string{"repo": cc.repo})
	go func() {
		defer s.unlockNode(cc.c.ID, "repo", cc.repo)
		t.Finish(s.execClean(t, cc, names, who, ip))
	}()
	return t, nil
}

// POST .../backup/repos/{repo}/cleanup：删除指定快照（snapshots），或按备份策略的保留策略清理（policy）。
func (s *Server) runClean(w http.ResponseWriter, r *http.Request, u *model.User) {
	repo := r.PathValue("repo")
	var in struct {
		Snapshots []string `json:"snapshots"`
		Policy    string   `json:"policy"`
	}
	if !decode(w, r, &in) {
		return
	}
	switch {
	case !sr.ValidIdent(repo):
		fail(w, http.StatusBadRequest, "仓库名不合法")
		return
	case (len(in.Snapshots) == 0) == (in.Policy == ""):
		fail(w, http.StatusBadRequest, "请指定要删除的快照，或指定备份策略（二选一）")
		return
	case len(in.Snapshots) > maxManualDelete:
		fail(w, http.StatusBadRequest, fmt.Sprintf("一次最多删除 %d 个快照", maxManualDelete))
		return
	}
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	var names []string
	title := "清理备份快照 " + repo
	err := func() error {
		cc, err := s.prepareClean(x.ctx, x.c, x.cl, repo)
		if err != nil {
			return err
		}
		snaps, err := x.cl.Snapshots(x.ctx, repo)
		if err != nil {
			return apiErr{http.StatusBadGateway, "读取快照失败：" + err.Error()}
		}
		if in.Policy != "" {
			p, found := s.policyOf(x.c.ID, in.Policy)
			switch {
			case !found:
				return missing("备份策略不存在")
			case p.Repo != repo:
				return bad("该备份策略使用的仓库是 " + p.Repo)
			case p.KeepLast <= 0 && p.KeepDays <= 0:
				return bad("该备份策略没有设置保留策略")
			}
			_, remove, _ := planRetention(p, snaps, time.Now())
			if len(remove) == 0 {
				return conflict("按当前保留策略没有需要清理的快照")
			}
			for _, sn := range remove {
				names = append(names, sn.Name)
			}
			if len(names) > maxManualDelete {
				names = names[:maxManualDelete]
			}
			title = "按保留策略清理 " + p.Name
		} else {
			have := map[string]bool{}
			for _, sn := range snaps {
				have[sn.Name] = true
			}
			seen := map[string]bool{}
			for _, n := range in.Snapshots {
				switch {
				case !snapNameRe.MatchString(n) || strings.Contains(n, ".."):
					return bad("快照名 " + n + " 不合法")
				case !have[n]:
					return missing("仓库 " + repo + " 中没有快照 " + n)
				case seen[n]:
					return bad("快照 " + n + " 重复")
				}
				seen[n] = true
				names = append(names, n)
			}
		}
		t, err := s.startClean(cc, names, u.Login, clientIP(r), title)
		if err != nil {
			return err
		}
		s.auditSQL(r, u, cleanCode, fmt.Sprintf("%s：%d 个快照（任务 %s 已开始）", title, len(names), t.ID()), strings.Join(names, ", "), nil, x.c.ID)
		ok(w, map[string]any{"ok": true, "taskId": t.ID(), "count": len(names)})
		return nil
	}()
	if err != nil {
		s.auditSQL(r, u, cleanCode, title, strings.Join(in.Snapshots, ", "), err, x.c.ID)
		writeErr(w, err)
	}
}

// ---------- 定时清理 ----------

var cleanRunning sync.Map // 策略 ID → 正在清理

// RunDueCleanups 对设置了保留策略的启用中的备份策略，每小时按保留策略清理一次；now 便于测试传入。返回启动清理的策略数。
func (s *Server) RunDueCleanups(ctx context.Context, now time.Time) int {
	var due []model.BackupPolicy
	s.Store.View(func(st *store.State) {
		for _, p := range st.Backups {
			if p.Enabled && (p.KeepLast > 0 || p.KeepDays > 0) && now.Sub(p.LastClean) >= cleanEvery {
				due = append(due, p)
			}
		}
	})
	n := 0
	for _, p := range due {
		if _, busy := cleanRunning.LoadOrStore(p.ID, true); busy {
			continue
		}
		n++
		go func(p model.BackupPolicy) {
			defer cleanRunning.Delete(p.ID)
			s.cleanPolicy(ctx, p, now)
		}(p)
	}
	return n
}

func (s *Server) setCleanResult(pid string, at time.Time, res string) {
	s.Store.Update(func(st *store.State) error {
		for i := range st.Backups {
			if st.Backups[i].ID == pid {
				st.Backups[i].LastClean, st.Backups[i].CleanResult = at, res
			}
		}
		return nil
	})
}

// cleanPolicy 执行一次定时清理：每次最多删除 maxAutoDelete 个最旧的过期快照。
func (s *Server) cleanPolicy(ctx context.Context, p model.BackupPolicy, now time.Time) {
	who := "系统（备份策略）"
	c, err := s.Store.Cluster(p.Cluster)
	var cc *cleanCtx
	var names []string
	total := 0
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		var cl *sr.Client
		if cl, err = s.Mgr.Client(cctx, c); err == nil {
			if cc, err = s.prepareClean(cctx, c, cl, p.Repo); err == nil {
				var snaps []sr.Snapshot
				if snaps, err = cl.Snapshots(cctx, p.Repo); err == nil {
					_, remove, _ := planRetention(p, snaps, now)
					total = len(remove)
					for _, sn := range remove {
						if len(names) < maxAutoDelete {
							names = append(names, sn.Name)
						}
					}
				}
			}
		}
		cancel()
	}
	if err != nil {
		msg := err.Error()
		s.setCleanResult(p.ID, now, "未执行："+msg)
		s.Store.Audit(store.AuditEntry{User: who, Cluster: p.Cluster, Code: cleanCode, Act: "按保留策略清理 " + p.Name, Result: "失败：" + msg})
		return
	}
	if len(names) == 0 {
		s.setCleanResult(p.ID, now, "无需清理")
		return
	}
	t, err := s.startClean(cc, names, who, "", "按保留策略清理 "+p.Name)
	if err != nil {
		s.setCleanResult(p.ID, now, "未执行："+err.Error())
		return
	}
	s.Store.Audit(store.AuditEntry{User: who, Cluster: p.Cluster, Code: cleanCode, Act: fmt.Sprintf("按保留策略清理 %s：%d 个快照（任务 %s 已开始）", p.Name, len(names), t.ID()), Result: "成功", SQL: strings.Join(names, ", ")})
	// 等任务结束后记录结果
	for !func() bool { c, _ := t.snapshot(false, 0); return c.Finished() }() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	c2, _ := t.snapshot(false, 0)
	res := fmt.Sprintf("已删除 %d 个过期快照", len(names))
	if total > len(names) {
		res += fmt.Sprintf("（另有 %d 个下次清理）", total-len(names))
	}
	if c2.State != "done" {
		res = "清理失败：" + c2.Err
	}
	s.setCleanResult(p.ID, time.Now(), res)
}
