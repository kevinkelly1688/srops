// @author: kevinkelly
// @license: Apache Licence
// @file: hostsim.go
// @time: 2026/10/28

package fakesr

import (
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// hostSim 模拟一台主机（Hosts 模式）：虚拟文件系统、内核参数、已授权的 SSH 公钥，以及主机上的 StarRocks 进程。
// 每台主机在自己的回环地址上监听 SSH；命令按平台实际下发的写法逐条模拟（见 hostsim_cmd.go）。
type hostSim struct {
	c      *Cluster // 创建主机的（根）集群
	ip     string
	mu     sync.Mutex
	fs     map[string][]byte
	dirs   map[string]bool
	procs  map[string]*hproc // dir|kind → 进程
	kernel map[string]string
	keys   map[string]bool // 已授权的公钥（base64 部分）
	sudoOK bool
	owner  map[string]string // 目录 → 属主；没有记录的视为 SSH 用户自己的目录，属主为 root 的目录对 SSH 用户不可写
}

// hproc 是主机上的一个 StarRocks 进程（FE / BE / CN）。
type hproc struct {
	kind    string
	dir     string
	n       *node // 已加入集群后对应的节点
	running bool
	helper  string
	conf    map[string]string
}

func (c *Cluster) nodeDir(kind nodeKind) string {
	if kind == kindFE {
		return c.cfg.FEDir
	}
	return c.cfg.BEDir
}

// parseConf 解析 key = value 配置（忽略注释与空行）。
func parseConf(s string) map[string]string {
	m := map[string]string{}
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(strings.TrimRight(ln, "\r"))
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if i := strings.Index(ln, "="); i > 0 {
			m[strings.TrimSpace(ln[:i])] = strings.TrimSpace(ln[i+1:])
		}
	}
	return m
}

func confInt(m map[string]string, k string, def int) int {
	if v, err := strconv.Atoi(m[k]); err == nil {
		return v
	}
	return def
}

func (c *Cluster) confOf(n *node) string {
	switch n.kind {
	case kindFE:
		return fmt.Sprintf(`# fe.conf
LOG_DIR = ${STARROCKS_HOME}/log
meta_dir = %s/meta
http_port = %d
rpc_port = %d
query_port = %d
edit_log_port = %d
priority_networks = 127.0.0.0/8
sys_log_level = INFO
`, c.cfg.FEDir, n.httpPort, n.ports.rpc, c.QueryPort(), n.ports.edit)
	case kindBE:
		return fmt.Sprintf(`# be.conf
be_port = %d
be_http_port = %d
heartbeat_service_port = %d
brpc_port = %d
storage_root_path = /data1/starrocks/be;/data2/starrocks/be
priority_networks = 127.0.0.0/8
`, n.ports.be, n.httpPort, n.ports.hb, n.ports.brpc)
	}
	return fmt.Sprintf(`# cn.conf
be_port = %d
be_http_port = %d
heartbeat_service_port = %d
brpc_port = %d
priority_networks = 127.0.0.0/8
`, n.ports.be, n.httpPort, n.ports.hb, n.ports.brpc)
}

func newHost(c *Cluster, ip string, healthy bool) *hostSim {
	h := &hostSim{c: c, ip: ip, fs: map[string][]byte{}, dirs: map[string]bool{}, procs: map[string]*hproc{}, keys: map[string]bool{}, sudoOK: true, owner: map[string]string{},
		kernel: map[string]string{"swappiness": "0", "thp": "always [madvise] never", "overcommit": "1", "max_map_count": "262144", "nofile": "655350", "nproc": "655350", "swap": "0", "java": ""}}
	if !healthy {
		h.kernel = map[string]string{"swappiness": "60", "thp": "[always] madvise never", "overcommit": "0", "max_map_count": "65530", "nofile": "1024", "nproc": "4096", "swap": "1", "java": ""}
	}
	return h
}

// startHosts 为每个节点地址（以及额外的空白主机）启动 SSH 服务；各 FE 地址上同时监听查询端口。
func (c *Cluster) startHosts() error {
	c.hosts = map[string]*hostSim{}
	for _, n := range c.allNodes() {
		h, err := c.addHost(n.ip, !c.problems || !(n.kind == kindBE && n.idx == 3))
		if err != nil {
			return err
		}
		h.install(n)
		n.hs = h
		if n.kind == kindFE && n.ip != c.cfg.Host {
			if err := c.listenQuery(n.ip); err != nil {
				return err
			}
		}
	}
	for i := 0; i < c.cfg.Spare; i++ {
		if _, err := c.addHost(fmt.Sprintf("127.0.0.%d", 50+i), false); err != nil {
			return err
		}
	}
	return nil
}

// addHost 创建主机并在其回环地址上监听 SSH。
func (c *Cluster) addHost(ip string, healthy bool) (*hostSim, error) {
	if h := c.hosts[ip]; h != nil {
		return h, nil
	}
	h := newHost(c, ip, healthy)
	c.hosts[ip] = h
	conf, err := c.sshConf(h)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(c.cfg.SSHPort)))
	if err != nil {
		return nil, fmt.Errorf("listen ssh %s: %w", ip, err)
	}
	c.mu.Lock()
	c.extra = append(c.extra, ln)
	c.mu.Unlock()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.acceptLoop(ln, func(conn net.Conn) { c.handleSSH(conn, conf, h) })
	}()
	return h, nil
}

// listenQuery 在 ip 上监听本集群的查询端口（各 FE 的 query_port 相同）。
func (c *Cluster) listenQuery(ip string) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(c.QueryPort())))
	if err != nil {
		return fmt.Errorf("listen query %s: %w", ip, err)
	}
	c.mu.Lock()
	c.extra = append(c.extra, ln)
	c.mu.Unlock()
	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.serveMySQLOn(ln) }()
	return nil
}

// install 在主机上放置初始节点的安装文件并登记进程。
func (h *hostSim) install(n *node) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := n.kind.String()
	d := h.c.nodeDir(n.kind)
	ver := h.c.cfg.Version
	for _, f := range []string{"bin/start_" + k + ".sh", "bin/stop_" + k + ".sh"} {
		h.fs[d+"/"+f] = []byte("#!/bin/bash\n")
	}
	h.fs[d+"/lib/starrocks-"+k+".jar"] = []byte("version=" + ver + "\n")
	conf := h.c.confOf(n)
	h.fs[d+"/conf/"+k+".conf"] = []byte(conf)
	pc := parseConf(conf)
	if meta := pc["meta_dir"]; meta != "" {
		h.dirs[meta] = true
		h.fs[meta+"/image/ROLE"] = []byte("role=FOLLOWER\n")
	}
	for _, sp := range strings.Split(pc["storage_root_path"], ";") {
		if sp = strings.TrimSpace(sp); sp != "" {
			h.dirs[sp] = true
		}
	}
	h.procs[d+"|"+k] = &hproc{kind: k, dir: d, n: n, running: true, conf: pc}
}

// authorize 记录公钥（authorized_keys）。
func (h *hostSim) authorize(key string) {
	h.mu.Lock()
	h.keys[key] = true
	h.mu.Unlock()
}

func keyBody(k ssh.PublicKey) string {
	f := strings.Fields(string(ssh.MarshalAuthorizedKey(k)))
	if len(f) >= 2 {
		return f[1]
	}
	return ""
}

func (h *hostSim) isAuthorized(k ssh.PublicKey) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.keys[keyBody(k)]
}

// 生成的样例日志文件名
var logNames = map[nodeKind][]string{
	kindFE: {"fe.log", "fe.warn.log", "fe.audit.log", "fe.out"},
	kindBE: {"be.INFO", "be.WARNING", "be.out"},
	kindCN: {"cn.INFO", "cn.WARNING", "cn.out"},
}

// logLines 生成 1000 行样例日志。
func (c *Cluster) logLines(kind nodeKind, name string) []string {
	ok := false
	for _, x := range logNames[kind] {
		ok = ok || x == name
	}
	if !ok {
		return nil
	}
	var out []string
	base := time.Now().Add(-1000 * time.Second)
	for i := 0; i < 1000; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		switch {
		case strings.HasSuffix(name, ".warn.log") || strings.HasSuffix(name, ".WARNING"):
			if kind == kindFE {
				out = append(out, fmt.Sprintf("%s WARN (thrift-server-pool-%d|512) [Coordinator.exec():874] query exec time 120s exceeds 60s", ts.Format("2006-01-02 15:04:05,000"), i%8))
			} else {
				out = append(out, fmt.Sprintf("W%s %d storage_engine.cpp:612] disk /data2/starrocks/be usage 0.87 exceeds warning level 0.85", ts.Format("0102 15:04:05.000"), 1000+i))
			}
		case name == "fe.audit.log":
			out = append(out, fmt.Sprintf("%s [query] |Client=10.0.0.%d:5%04d|User=bi|State=EOF|Time=%d|Stmt=select count(*) from dw.dwd_order_detail", ts.Format("2006-01-02 15:04:05,000"), i%200, i, 20+i%90))
		case kind == kindFE:
			out = append(out, fmt.Sprintf("%s INFO (tablet scheduler|%d) [TabletScheduler.schedule():312] tablet scheduler: pending %d, running %d", ts.Format("2006-01-02 15:04:05,000"), 40+i%4, 10+i%7, i%5))
		default:
			out = append(out, fmt.Sprintf("I%s %d tablet_manager.cpp:1021] start to do cumulative compaction. tablet=%d score=%d", ts.Format("0102 15:04:05.000"), 1000+i, 128000+i, 20+i%160))
		}
	}
	return out
}

// HostFile 供测试使用：读取 Hosts 模式下某台主机上的文件内容。
func (c *Cluster) HostFile(ip, path string) (string, bool) {
	h := c.hosts[ip]
	if h == nil {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.fs[path]
	return string(b), ok
}

// HostPaths 供测试使用：列出主机上以 prefix 开头的文件路径。
func (c *Cluster) HostPaths(ip, prefix string) []string {
	h := c.hosts[ip]
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for p := range h.fs {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// SSHPort 返回 Hosts 模式下各主机的 SSH 端口。
func (c *Cluster) SSHPort() int { return c.cfg.SSHPort }

// Kernel 供测试使用：读取 / 设置主机的内核参数模拟值。
func (c *Cluster) Kernel(ip, key string) string {
	h := c.hosts[ip]
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.kernel[key]
}

// Children 返回由主机上的 FE 启动而创建的新集群（部署测试用）。
func (c *Cluster) Children() []*Cluster {
	c.nmu.RLock()
	defer c.nmu.RUnlock()
	return append([]*Cluster(nil), c.children...)
}

// QueryHostPort 返回查询端口（部署出的新集群用）。
func (c *Cluster) QueryHostPort() (string, int) { return c.cfg.Host, c.QueryPort() }

// NodeCount 返回各类节点数量。
func (c *Cluster) NodeCount() (fe, be, cn int) {
	return len(c.nodesOf(kindFE)), len(c.nodesOf(kindBE)), len(c.nodesOf(kindCN))
}

func (h *hostSim) writeFile(p string, b []byte) {
	h.mu.Lock()
	h.fs[p] = b
	h.mu.Unlock()
}

var _ = io.EOF

// Hosts 供测试使用。
func (c *Cluster) Hosts() map[string]string {
	out := map[string]string{}
	for ip, h := range c.hosts {
		h.mu.Lock()
		out[ip] = fmt.Sprintf("procs=%d listening=%q", len(h.procs), h.listeningLocked())
		h.mu.Unlock()
	}
	return out
}

// HostHasDir 供测试使用：主机上是否存在该目录（或其下有文件）。
func (c *Cluster) HostHasDir(ip, path string) bool {
	h := c.hosts[ip]
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.isDir(path)
}

// HostSetOwner 供测试使用：创建目录并设置属主（owner 为 root 时，SSH 用户在其下不可写）。
func (c *Cluster) HostSetOwner(ip, path, owner string) {
	if h := c.hosts[ip]; h != nil {
		h.mu.Lock()
		h.dirs[path] = true
		h.owner[path] = owner
		h.mu.Unlock()
	}
}

// HostOwner 供测试使用：读取目录的属主；没有记录的目录返回空串。
func (c *Cluster) HostOwner(ip, path string) string {
	h := c.hosts[ip]
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.owner[path]
}

// HostRemove 供测试使用：删除主机上的文件或目录。
func (c *Cluster) HostRemove(ip, path string) {
	if h := c.hosts[ip]; h != nil {
		h.mu.Lock()
		h.removeAll(path)
		h.mu.Unlock()
	}
}
