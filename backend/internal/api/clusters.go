// @author: kevinkelly
// @license: Apache Licence
// @file: clusters.go
// @time: 2026/10/28

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/collect"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
	"starrocks-ops/internal/sshx"
	"starrocks-ops/internal/store"
)

type storeState = store.State

func storeAudit(user, ip, code, act, res string) store.AuditEntry {
	return store.AuditEntry{User: user, IP: ip, Code: code, Act: act, Result: res}
}

// clusterOut 是集群列表的输出：去掉密文，附带“待完善”信息。
type clusterOut struct {
	model.Cluster
	Missing []string `json:"missing"`
}

func out(c model.Cluster) clusterOut {
	m := c.Missing()
	if m == nil {
		m = []string{}
	}
	return clusterOut{Cluster: c.Public(), Missing: m}
}

func (s *Server) listClusters(w http.ResponseWriter, r *http.Request, u *model.User) {
	list := []clusterOut{}
	acc := accessOf(r)
	for _, c := range s.Store.Clusters() {
		if acc.Cluster(c.ID) { // 只返回数据范围内的集群
			list = append(list, out(c))
		}
	}
	ok(w, list)
}

// ---------- 连接测试 / 识别 ----------

type connIn struct {
	Hosts     []string `json:"hosts"`
	QueryPort int      `json:"queryPort"`
	HTTPPort  int      `json:"httpPort"`
	User      string   `json:"user"`
	Password  string   `json:"password"`
	ID        string   `json:"id"` // 编辑已有集群且未改密码时，用已保存的密码
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]{0,252}$`)

func (s *Server) connInfo(in connIn) (sr.ConnInfo, error) {
	var hosts []string
	for _, h := range in.Hosts {
		for _, x := range strings.Split(h, ",") {
			if x = strings.TrimSpace(x); x != "" {
				if !hostRe.MatchString(x) {
					return sr.ConnInfo{}, fmt.Errorf("FE 地址不合法：%s", x)
				}
				hosts = append(hosts, x)
			}
		}
	}
	if len(hosts) == 0 || in.User == "" {
		return sr.ConnInfo{}, errors.New("请填写 FE 地址和运维账号")
	}
	if in.QueryPort <= 0 || in.QueryPort > 65535 || in.HTTPPort <= 0 || in.HTTPPort > 65535 {
		return sr.ConnInfo{}, errors.New("端口不合法")
	}
	pw := in.Password
	if (pw == "" || pw == "********") && in.ID != "" {
		c, err := s.Store.Cluster(in.ID)
		if err != nil {
			return sr.ConnInfo{}, err
		}
		if pw, err = s.Box.Decrypt(c.PasswordEnc); err != nil {
			return sr.ConnInfo{}, err
		}
	}
	if pw == "" {
		return sr.ConnInfo{}, errors.New("请填写密码")
	}
	return sr.ConnInfo{Hosts: hosts, QueryPort: in.QueryPort, HTTPPort: in.HTTPPort, User: in.User, Password: pw}, nil
}

type grant struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Use  string `json:"use"`
}

type detectOut struct {
	Version string       `json:"version"`
	Mode    string       `json:"mode"`
	Host    string       `json:"host"`
	Nodes   []model.Node `json:"nodes"`
	Grants  []grant      `json:"grants"`
	Warns   []string     `json:"warnings"`
}

func detect(ctx context.Context, ci sr.ConnInfo) (*detectOut, error) {
	cl, err := sr.Open(ctx, ci)
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	d := &detectOut{Host: cl.Host}
	if d.Version, err = cl.Version(ctx); err != nil {
		return nil, fmt.Errorf("读取版本失败：%w", err)
	}
	d.Mode = cl.RunMode(ctx)
	d.Nodes, d.Warns = cl.Nodes(ctx, d.Mode)
	if d.Warns == nil {
		d.Warns = []string{}
	}
	collect.Enrich(d.Nodes, collect.NodeMetrics(ctx, cl, d.Nodes))
	d.Grants = checkGrants(ctx, cl, ci.User)
	return d, nil
}

// checkGrants 根据 SHOW GRANTS 判断运维账号权限（root / ALL / root 角色视为全部具备）。
func checkGrants(ctx context.Context, cl *sr.Client, user string) []grant {
	text := ""
	if r, err := cl.Query(ctx, "SHOW GRANTS"); err == nil {
		for i := range r.Data {
			text += " " + strings.ToUpper(strings.Join(r.Data[i], " "))
		}
	}
	all := strings.EqualFold(user, "root") || strings.Contains(text, "ALL PRIVILEGES") || strings.Contains(text, "ROLE 'ROOT'") || strings.Contains(text, "ROLE 'DB_ADMIN'")
	has := func(p string) bool { return all || strings.Contains(text, p) }
	return []grant{
		{"SELECT ON information_schema / 业务库", has("SELECT"), "查看集群、巡检、查询分析"},
		{"NODE ON SYSTEM", has("NODE"), "扩缩容、黑名单、节点管理"},
		{"OPERATE ON SYSTEM", has("OPERATE"), "参数修改、KILL 查询、Compaction"},
		{"CREATE RESOURCE GROUP ON SYSTEM", has("CREATE RESOURCE GROUP"), "资源组管理"},
		{"REPOSITORY ON SYSTEM", has("REPOSITORY"), "备份恢复（缺少时该功能不可用）"},
	}
}

func (s *Server) testConn(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in connIn
	if !decode(w, r, &in) {
		return
	}
	ci, err := s.connInfo(in)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	d, err := detect(ctx, ci)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, d)
}

// ---------- SSH 测试 ----------

type sshIn struct {
	ID       string `json:"id"`
	User     string `json:"user"`
	Port     int    `json:"port"`
	Auth     string `json:"auth"` // key / password
	Password string `json:"password"`
	Sudo     bool   `json:"sudo"`
	FEDir    string `json:"feDir"`
	BEDir    string `json:"beDir"`
	Nodes    []struct {
		Host string `json:"host"`
		Type string `json:"type"`
	} `json:"nodes"`
}

var dirRe = regexp.MustCompile(`^/[A-Za-z0-9._/\-]{1,250}$`)
var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_\-]{0,31}$`)

func (s *Server) sshPassword(in sshIn) string {
	if in.Auth != "password" {
		return ""
	}
	if (in.Password == "" || in.Password == "********") && in.ID != "" {
		if c, err := s.Store.Cluster(in.ID); err == nil && c.SSH != nil {
			p, _ := s.Box.Decrypt(c.SSH.PasswordEnc)
			return p
		}
	}
	return in.Password
}

type hostCheck struct {
	Host  string `json:"host"`
	Roles string `json:"roles"`
	SSH   string `json:"ssh"`
	Sudo  string `json:"sudo"`
	Dir   string `json:"dir"`
	OK    bool   `json:"ok"`
}

func (s *Server) testSSH(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in sshIn
	if !decode(w, r, &in) {
		return
	}
	if !userRe.MatchString(in.User) || in.Port <= 0 || in.Port > 65535 {
		fail(w, http.StatusBadRequest, "SSH 用户或端口不合法")
		return
	}
	if !dirRe.MatchString(in.FEDir) || !dirRe.MatchString(in.BEDir) || strings.Contains(in.FEDir+in.BEDir, "..") {
		fail(w, http.StatusBadRequest, "部署目录须为绝对路径，只能包含字母、数字、. _ - /")
		return
	}
	hosts := map[string][]string{}
	var order []string
	for _, n := range in.Nodes {
		if !hostRe.MatchString(n.Host) {
			continue
		}
		if _, ok := hosts[n.Host]; !ok {
			order = append(order, n.Host)
		}
		hosts[n.Host] = append(hosts[n.Host], n.Type)
	}
	pw := s.sshPassword(in)
	res := make([]hostCheck, len(order))
	var wg sync.WaitGroup
	for i, h := range order {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			res[i] = s.checkHost(r.Context(), h, hosts[h], in, pw)
		}(i, h)
	}
	wg.Wait()
	all := len(res) > 0
	for _, x := range res {
		all = all && x.OK
	}
	ok(w, map[string]any{"ok": all, "hosts": res})
}

func (s *Server) checkHost(ctx context.Context, h string, types []string, in sshIn, pw string) hostCheck {
	hc := hostCheck{Host: h, Roles: strings.ToUpper(strings.Join(uniq(types), " / "))}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sess, err := s.Keys.Dial(ctx, sshx.Target{Host: h, Port: in.Port, User: in.User, Password: pw})
	if err != nil {
		hc.SSH = err.Error()
		return hc
	}
	defer sess.Close()
	hc.SSH, hc.OK = "已连通", true
	var bad []string
	for _, t := range uniq(types) {
		dir, script := in.BEDir, "start_be.sh"
		switch t {
		case "fe":
			dir, script = in.FEDir, "start_fe.sh"
		case "cn":
			script = "start_cn.sh"
		}
		res, err := sess.Run(ctx, fmt.Sprintf("test -x %s/bin/%s && echo ok", dir, script))
		if err != nil || strings.TrimSpace(res.Stdout) != "ok" {
			bad = append(bad, dir+"/bin/"+script+" 不存在或不可执行")
		}
	}
	if len(bad) > 0 {
		hc.Dir, hc.OK = strings.Join(bad, "；"), false
	} else {
		hc.Dir = "✓"
	}
	if in.Sudo {
		if res, err := sess.Run(ctx, "sudo -n true"); err == nil && res.Code == 0 {
			hc.Sudo = "可用"
		} else {
			hc.Sudo = "不可用（需要免密 sudo）"
		}
	} else {
		hc.Sudo = "未启用"
	}
	return hc
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) pubkey(w http.ResponseWriter, r *http.Request, u *model.User) {
	ok(w, map[string]string{"publicKey": s.Keys.PublicKey})
}

// ---------- 导入 / 编辑 / 移除 ----------

type clusterIn struct {
	connIn
	Name         string `json:"name"`
	Env          string `json:"env"`
	Owner        string `json:"owner"`
	Desc         string `json:"desc"`
	Prom         string `json:"prom"`
	PromSelector string `json:"promSelector"`
	Audit        bool   `json:"audit"`
	SSH          *sshIn `json:"ssh"` // nil = 稍后填写
}

var envs = map[string]bool{"生产": true, "预发": true, "测试": true, "开发": true}

func (s *Server) buildCluster(ctx context.Context, in clusterIn, prev *model.Cluster) (model.Cluster, error) {
	var c model.Cluster
	if prev != nil {
		c = *prev
	} else {
		c.ID, c.Name, c.CreatedAt = in.Name, in.Name, time.Now()
		if !store.ValidID(c.ID) {
			return c, errors.New("集群名称须为 3–64 位字母、数字、- 或 _")
		}
	}
	if !envs[in.Env] {
		return c, errors.New("环境不合法")
	}
	if in.Prom != "" && !strings.HasPrefix(in.Prom, "http://") && !strings.HasPrefix(in.Prom, "https://") {
		return c, errors.New("Prometheus 地址须以 http:// 或 https:// 开头")
	}
	if prev != nil {
		in.ID = prev.ID
	}
	ci, err := s.connInfo(in.connIn)
	if err != nil {
		return c, err
	}
	cl, err := sr.Open(ctx, ci)
	if err != nil {
		return c, err
	}
	defer cl.Close()
	if c.Version, err = cl.Version(ctx); err != nil {
		return c, fmt.Errorf("读取版本失败：%w", err)
	}
	c.Mode = cl.RunMode(ctx)
	if c.PasswordEnc, err = s.Box.Encrypt(ci.Password); err != nil {
		return c, err
	}
	c.Env, c.Owner, c.Desc = in.Env, in.Owner, strings.TrimSpace(in.Desc)
	c.FEHosts, c.QueryPort, c.HTTPPort, c.User = ci.Hosts, ci.QueryPort, ci.HTTPPort, ci.User
	c.Prom, c.PromSelector = strings.TrimSpace(in.Prom), strings.TrimSpace(in.PromSelector)
	c.AuditTable = ""
	if in.Audit {
		c.AuditTable = "starrocks_audit_db__.starrocks_audit_tbl__"
	}
	c.SSH, c.Dirs = nil, nil
	if in.SSH != nil {
		x := in.SSH
		if !userRe.MatchString(x.User) || x.Port <= 0 || x.Port > 65535 || !dirRe.MatchString(x.FEDir) || !dirRe.MatchString(x.BEDir) {
			return c, errors.New("SSH 用户、端口或部署目录不合法")
		}
		sc := &model.SSHConfig{User: x.User, Port: x.Port, Auth: "key", Sudo: x.Sudo}
		if x.Auth == "password" {
			sc.Auth = "password"
			if sc.PasswordEnc, err = s.Box.Encrypt(s.sshPassword(*x)); err != nil {
				return c, err
			}
		}
		c.SSH, c.Dirs = sc, &model.Dirs{FE: x.FEDir, BE: x.BEDir}
	}
	c.UpdatedAt = time.Now()
	return c, nil
}

func (s *Server) createCluster(w http.ResponseWriter, r *http.Request, u *model.User) {
	if !accessOf(r).AllClusters() {
		fail(w, http.StatusForbidden, "导入集群需要拥有「全部集群」数据范围的角色（新集群对数据范围受限的角色不可见）")
		return
	}
	var in clusterIn
	if !decode(w, r, &in) {
		return
	}
	if _, err := s.Store.Cluster(in.Name); err == nil {
		fail(w, http.StatusConflict, "集群名称已存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, err := s.buildCluster(ctx, in, nil)
	if err == nil {
		err = s.Store.PutCluster(c, true)
	}
	s.audit(r, u, in.Name, "cluster.add", fmt.Sprintf("导入集群 %s（%s · %s）", in.Name, c.Version, c.Mode), err)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Mgr.Sampler.Watch(c)
	ok(w, out(c))
}

func (s *Server) updateCluster(w http.ResponseWriter, r *http.Request, u *model.User) {
	prev, found := s.cluster(w, r)
	if !found {
		return
	}
	var in clusterIn
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, err := s.buildCluster(ctx, in, &prev)
	if err == nil {
		err = s.Store.PutCluster(c, false)
	}
	act := "编辑集群 " + prev.ID
	if len(prev.Missing()) > 0 && len(c.Missing()) == 0 {
		act += "（必填信息已完整）"
	}
	s.audit(r, u, prev.ID, "cluster.edit", act, err)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Mgr.Forget(c.ID)
	s.Mgr.Sampler.Watch(c)
	ok(w, out(c))
}

func (s *Server) deleteCluster(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	err := s.Store.DeleteCluster(c.ID)
	s.audit(r, u, c.ID, "cluster.delete", "移除集群 "+c.ID+"（仅解除纳管）", err)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Mgr.Forget(c.ID)
	s.Mgr.Sampler.Stop(c.ID)
	ok(w, map[string]bool{"ok": true})
}

// checkCluster 对已导入集群做连通性检查。
func (s *Server) checkCluster(w http.ResponseWriter, r *http.Request, u *model.User) {
	c, found := s.cluster(w, r)
	if !found {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	type item struct {
		Name, Status, Detail string
	}
	var items []item
	t0 := time.Now()
	s.Mgr.Forget(c.ID)
	cl, err := s.Mgr.Client(ctx, c)
	if err != nil {
		items = append(items, item{"MySQL 协议 " + strings.Join(c.FEHosts, ",") + fmt.Sprintf(":%d", c.QueryPort), "bad", err.Error()})
	} else {
		items = append(items, item{fmt.Sprintf("MySQL 协议 %s:%d", cl.Host, c.QueryPort), "ok", time.Since(t0).Round(time.Millisecond).String()})
		nodes, _ := cl.Nodes(ctx, c.Mode)
		mt := collect.NodeMetrics(ctx, cl, nodes)
		reach := 0
		for _, n := range nodes {
			if _, ok := mt[n.ID]; ok {
				reach++
			}
		}
		st := "ok"
		if reach < len(nodes) {
			st = "warn"
		}
		items = append(items, item{"节点 HTTP /metrics", st, fmt.Sprintf("%d / %d 个节点可达", reach, len(nodes))})
		g := checkGrants(ctx, cl, c.User)
		var miss []string
		for _, x := range g {
			if !x.OK {
				miss = append(miss, x.Name)
			}
		}
		if len(miss) == 0 {
			items = append(items, item{"运维账号权限", "ok", "完整"})
		} else {
			items = append(items, item{"运维账号权限", "warn", "缺少 " + strings.Join(miss, "、")})
		}
	}
	if c.Prom == "" {
		items = append(items, item{"Prometheus", "warn", "未配置：趋势改用平台内置采样"})
	} else if _, err := collect.PromTrend(ctx, c); err != nil {
		items = append(items, item{"Prometheus " + c.Prom, "bad", err.Error()})
	} else {
		items = append(items, item{"Prometheus " + c.Prom, "ok", "可查询"})
	}
	if c.SSH == nil {
		items = append(items, item{"SSH", "bad", "未填写 SSH 凭据，运维操作不可用"})
	} else {
		items = append(items, item{"SSH", "info", "请在「完善信息 / 编辑」第 3 步中逐台测试"})
	}
	s.audit(r, u, c.ID, "cluster.test", "连通性测试 "+c.ID, nil)
	ok(w, items)
}
