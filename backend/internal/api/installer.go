// @author: kevinkelly
// @license: Apache Licence
// @file: installer.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
	"starrocks-ops/internal/sshx"
)

// remote 是任务里对一台主机的 SSH 会话封装：每条命令与输出写入任务日志，文件上传带进度与校验。
type remote struct {
	s    *Server
	ctx  context.Context
	t    *taskRun
	sess *sshx.Session
	host string
}

func (s *Server) dialRemote(ctx context.Context, t *taskRun, tgt sshx.Target) (*remote, error) {
	t.Mask(tgt.Password)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sess, err := s.Keys.Dial(cctx, tgt)
	if err != nil {
		return nil, err
	}
	return &remote{s: s, ctx: ctx, t: t, sess: sess, host: tgt.Host}, nil
}

func (r *remote) Close() { r.sess.Close() }

func (r *remote) logf(format string, a ...any) {
	r.t.Logf("[%s] "+format, append([]any{r.host}, a...)...)
}

// run 执行命令并记录；退出码非 0 返回错误（含 stderr）。
func (r *remote) run(cmd string) (sshx.Result, error) {
	r.logf("$ %s", cmd)
	res, err := r.sess.Run(r.ctx, cmd)
	if err != nil {
		return res, err
	}
	if o := strings.TrimSpace(res.Stdout); o != "" {
		r.logf("%s", o)
	}
	if res.Code != 0 {
		return res, fmt.Errorf("命令退出码 %d：%s", res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return res, nil
}

// quiet 执行命令但不记录；只在传输失败时返回错误，由调用方看退出码与输出。
func (r *remote) quiet(cmd string) (sshx.Result, error) { return r.sess.Run(r.ctx, cmd) }

func (r *remote) runStdin(cmd, content string) error {
	r.logf("$ %s  # 写入 %d 字节", cmd, len(content))
	res, err := r.sess.RunStdin(r.ctx, cmd, strings.NewReader(content))
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("命令退出码 %d：%s", res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

func (r *remote) readFile(p string) (string, error) { return readRemoteFile(r.ctx, r.sess, p) }

func (r *remote) writeFile(p, content string) error {
	r.logf("写入 %s（%d 字节）", p, len(content))
	return writeRemoteFile(r.ctx, r.sess, p, content)
}

// setConf 读取配置文件，依次设置 key = value，写回。敏感值在日志里已按 Mask 隐藏。
func (r *remote) setConf(path string, kv [][2]string) error {
	old, err := r.readFile(path)
	if err != nil {
		return err
	}
	cur := old
	for _, p := range kv {
		cur = setConfLine(cur, p[0], p[1])
	}
	if cur == old {
		r.logf("%s 无需修改", path)
		return nil
	}
	for _, p := range kv {
		r.logf("  %s = %s", p[0], p[1])
	}
	return r.writeFile(path, cur)
}

type countReader struct {
	r        io.Reader
	n, total int64
	next     int64
	log      func(pct int)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.total > 0 && c.n >= c.next {
		c.log(int(c.n * 100 / c.total))
		c.next = (c.n*100/c.total/20 + 1) * c.total / 5 // 每 20% 记一次
	}
	return n, err
}

// upload 把本地文件传到远端 remotePath；远端已有大小与 SHA256 都一致的同名文件时跳过；传完校验 SHA256。
func (r *remote) upload(local string, size int64, sha, remotePath string) error {
	if res, err := r.quiet("stat -c %s " + remotePath); err == nil && res.Code == 0 && strings.TrimSpace(res.Stdout) == strconv.FormatInt(size, 10) {
		if res2, err := r.quiet("sha256sum " + remotePath); err == nil && res2.Code == 0 && strings.HasPrefix(strings.TrimSpace(res2.Stdout), sha) {
			r.logf("%s 已存在且校验一致，跳过上传", remotePath)
			return nil
		}
	}
	f, err := os.Open(local)
	if err != nil {
		return fmt.Errorf("读取本地安装包失败：%w", err)
	}
	defer f.Close()
	r.logf("上传 %s（%.1f MB）…", remotePath, float64(size)/1048576)
	cr := &countReader{r: f, total: size, log: func(p int) { r.logf("  已上传 %d%%", p) }}
	res, err := r.sess.RunStdin(r.ctx, fmt.Sprintf("cat > %s.part && mv %s.part %s", remotePath, remotePath, remotePath), cr)
	if err != nil {
		return fmt.Errorf("上传失败：%w", err)
	}
	if res.Code != 0 {
		return fmt.Errorf("上传失败（退出码 %d）：%s", res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	res, err = r.sess.Run(r.ctx, "sha256sum "+remotePath)
	if err != nil || res.Code != 0 {
		return fmt.Errorf("校验上传文件失败")
	}
	if got := strings.Fields(res.Stdout); len(got) == 0 || got[0] != sha {
		return fmt.Errorf("上传后 SHA256 不一致（期望 %s）", sha)
	}
	r.logf("上传完成，SHA256 校验通过")
	return nil
}

// extract 把安装包解压到 dir（去掉最外层目录）。
func (r *remote) extract(pkgRemote, dir string) error {
	if _, err := r.run("rm -rf " + dir); err != nil {
		return err
	}
	if _, err := r.run("mkdir -p " + dir); err != nil {
		return err
	}
	_, err := r.run(fmt.Sprintf("tar -xzf %s -C %s --strip-components=1", pkgRemote, dir))
	return err
}

func (r *remote) running(dir, kind string) (bool, error) {
	return procRunning(r.ctx, r.sess, dir, kind)
}

// start 启动节点进程；已在运行则跳过。helper 仅用于新 FE 首次启动。
func (r *remote) start(dir, kind, helper string) error {
	run, err := r.running(dir, kind)
	if err != nil {
		return err
	}
	if run {
		r.logf("%s 进程已在运行，跳过启动", strings.ToUpper(kind))
		return nil
	}
	cmd := startCmd(dir, kind)
	if helper != "" {
		cmd = fmt.Sprintf("cd %s && bin/start_%s.sh --helper %s --daemon", dir, kind, helper)
	}
	if _, err := r.run(cmd); err != nil {
		r.tailLogs(dir, kind)
		return err
	}
	return nil
}

// tailLogs 启动失败时把日志末尾带进任务日志，便于排查。
func (r *remote) tailLogs(dir, kind string) {
	for _, f := range map[string][]string{"fe": {"fe.out", "fe.log"}, "be": {"be.out"}, "cn": {"cn.out"}}[kind] {
		if res, err := r.quiet(fmt.Sprintf("tail -n 15 %s/log/%s", dir, f)); err == nil && res.Code == 0 && strings.TrimSpace(res.Stdout) != "" {
			r.logf("--- %s/log/%s 末尾 ---\n%s", dir, f, strings.TrimSpace(res.Stdout))
		}
	}
}

// ---------- 通过 SQL 等待节点 ----------

// aliveVia 通过 cl 判断指定节点是否已加入并存活。FE 用 edit_log 端口，BE / CN 用心跳端口。
func aliveVia(ctx context.Context, cl *sr.Client, mode, kind, host string, port int) (member, alive bool) {
	nodes, _ := cl.Nodes(ctx, mode)
	for _, n := range nodes {
		p := n.Port
		if n.Type == "fe" {
			p = n.EditLog
		}
		if n.Type == kind && n.Host == host && p == port {
			return true, n.Alive
		}
	}
	return false, false
}

func (r *remote) waitAlive(cl *sr.Client, mode, kind, dir string, port int, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		cctx, cancel := context.WithTimeout(r.ctx, 20*time.Second)
		member, alive := aliveVia(cctx, cl, mode, kind, r.host, port)
		cancel()
		if alive {
			r.logf("%s 已加入集群并存活", strings.ToUpper(kind))
			return nil
		}
		if time.Now().After(deadline) {
			r.tailLogs(dir, kind)
			if !member {
				return fmt.Errorf("%s %s:%d 未出现在集群节点列表中", strings.ToUpper(kind), r.host, port)
			}
			return fmt.Errorf("%s %s:%d 已加入集群，但 %d 秒内未显示为存活", strings.ToUpper(kind), r.host, port, int(limit.Seconds()))
		}
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case <-time.After(nodePollEvery):
		}
	}
}

// requiredJava 返回该版本需要的最低 JDK 主版本。
func requiredJava(version string) int {
	if verAtLeast(version, 3, 5) {
		return 17
	}
	return 8
}

// pkgPlan 是一次安装所需的安装包信息。
type pkgPlan struct {
	pkg model.Package
	jdk *model.Package
}

func (s *Server) resolvePackages(version, pkgName, jdkName string) (pkgPlan, error) {
	var pp pkgPlan
	p, found := s.packageByName(pkgName)
	if !found || p.Kind != "starrocks" {
		return pp, bad("安装包 " + pkgName + " 不存在，请先在「安装包」页上传")
	}
	if version != "" && p.Ver != version {
		return pp, bad(fmt.Sprintf("安装包版本 %s 与所选版本 %s 不一致", p.Ver, version))
	}
	pp.pkg = p
	if jdkName != "" {
		j, found := s.packageByName(jdkName)
		if !found || j.Kind != "jdk" {
			return pp, bad("JDK 包 " + jdkName + " 不存在")
		}
		pp.jdk = &j
	}
	return pp, nil
}
