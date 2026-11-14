// @author: kevinkelly
// @license: Apache Licence
// @file: cluster.go
// @time: 2026/10/28

package fakesr

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// 部署模式与场景
const (
	ModeSharedNothing = "shared-nothing"
	ModeSharedData    = "shared-data"

	ScenarioHealthy  = "healthy"
	ScenarioProblems = "problems"
)

// Config 描述一个假集群。端口为 0 表示随机分配。
type Config struct {
	Host     string
	User     string
	Password string
	Mode     string
	Scenario string
	Version  string

	// Hosts 为 true 时每个节点使用独立的回环地址（127.0.0.11 起），每台主机各有一个 SSH 服务，
	// 并模拟主机上的文件、进程与日志（节点管理 / 部署 / 升级用）。SSHPort 必须是固定端口。
	Hosts bool
	FEDir string // Hosts 模式下 FE 的安装目录
	BEDir string // Hosts 模式下 BE / CN 的安装目录
	Spare int    // Hosts 模式下额外的空白主机数量（127.0.0.50 起），用于部署 / 扩容
	Empty bool   // 不创建任何节点（部署出的新集群：之后由 ADD 语句逐个加入）

	// S3 为 true 时启动一个内存 S3 服务（S3Port 0 = 随机端口），保存备份仓库里的快照文件；
	// 平台删除快照目录后，SHOW SNAPSHOT 随之不再列出该快照。访问密钥 ID 为 S3AccessKey（默认 fakeak，密钥任意）。
	S3          bool
	S3Port      int
	S3AccessKey string

	QueryPort int // 0 = 随机
	SSHPort   int // <0 禁用, 0 = 随机

	// 各类节点 HTTP 端口基址，第 i 个节点 = base+i；0 = 每个节点随机端口
	FEHTTPBase int
	BEHTTPBase int
	CNHTTPBase int

	Logger *log.Logger // nil 时静默
}

// DefaultConfig 返回与命令行默认值一致的配置。
func DefaultConfig() Config {
	return Config{
		Host:       "127.0.0.1",
		User:       "sr_ops",
		Password:   "secret",
		Mode:       ModeSharedNothing,
		Scenario:   ScenarioProblems,
		Version:    "3.5.6",
		QueryPort:  19030,
		SSHPort:    12222,
		FEHTTPBase: 18030,
		BEHTTPBase: 18040,
		CNHTTPBase: 18050,
		FEDir:      "/opt/starrocks/fe",
		BEDir:      "/opt/starrocks/be",
	}
}

// 节点角色
type nodeKind int

const (
	kindFE nodeKind = iota
	kindBE
	kindCN
)

func (k nodeKind) String() string {
	return [...]string{"fe", "be", "cn"}[k]
}

// node 是一个虚拟节点（FE/BE/CN）。
type node struct {
	kind     nodeKind
	idx      int
	role     string // FE: LEADER/FOLLOWER/OBSERVER
	httpPort int
	ip       string
	ports    nodePorts
	hs       *hostSim
	down     atomic.Bool // 进程已停止
	decom    time.Time   // 非零：已开始下线（迁移 Tablet）
	ver      string      // 空 = 集群版本
}

// Cluster 是运行中的假集群。
type Cluster struct {
	cfg       Config
	problems  bool
	sharedDat bool
	startedAt time.Time

	FEs []*node
	BEs []*node
	CNs []*node

	mysqlLn net.Listener
	sshLn   net.Listener
	extra   []net.Listener // Hosts 模式：各 FE 地址上的查询端口、各主机的 SSH 端口
	https   []*http.Server

	sim   *sim
	nmu   sync.RWMutex // 保护 FEs / BEs / CNs 的增删
	hosts map[string]*hostSim

	parent   *Cluster   // 由主机上的 FE 启动而创建的新集群：指向根集群
	children []*Cluster // 根集群：部署出的新集群
	nextIdx  [3]int     // 各类节点的下一个编号
	cred     *credProvider
	s3       *s3Sim
	nodeBL   map[string]string // 节点黑名单：be:<id> / cn:<id> → 加入时间
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	done     chan struct{}
	wg       sync.WaitGroup
	once     sync.Once
}

// QueryAddr 返回 MySQL 协议监听地址。
func (c *Cluster) QueryAddr() string { return c.mysqlLn.Addr().String() }

// SSHAddr 返回 SSH 监听地址，未启用时为空。
func (c *Cluster) SSHAddr() string {
	if c.sshLn == nil {
		return ""
	}
	return c.sshLn.Addr().String()
}

// QueryPort 返回实际的查询端口。
func (c *Cluster) QueryPort() int { return c.mysqlLn.Addr().(*net.TCPAddr).Port }

// FEHTTPAddrs / BEHTTPAddrs / CNHTTPAddrs 返回各节点 HTTP 地址。
func (c *Cluster) FEHTTPAddrs() []string { return c.httpAddrs(c.nodesOf(kindFE)) }
func (c *Cluster) BEHTTPAddrs() []string { return c.httpAddrs(c.nodesOf(kindBE)) }
func (c *Cluster) CNHTTPAddrs() []string { return c.httpAddrs(c.nodesOf(kindCN)) }

func (c *Cluster) httpAddrs(ns []*node) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = net.JoinHostPort(n.ip, strconv.Itoa(n.httpPort))
	}
	return out
}

// serveNodeHTTP 为节点启动 /metrics HTTP 服务（port=0 随机）。
func (c *Cluster) serveNodeHTTP(n *node, port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(n.ip, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen %s%d http: %w", n.kind, n.idx, err)
	}
	n.httpPort = ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: c.metricsHandler(n), ReadHeaderTimeout: 5 * time.Second}
	c.mu.Lock()
	c.https = append(c.https, srv)
	c.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// nodesOf 返回某类节点的快照（节点可能被动态增删）。
func (c *Cluster) nodesOf(k nodeKind) []*node {
	c.nmu.RLock()
	defer c.nmu.RUnlock()
	src := map[nodeKind][]*node{kindFE: c.FEs, kindBE: c.BEs, kindCN: c.CNs}[k]
	return append([]*node(nil), src...)
}

// allNodes 返回全部节点（FE、BE、CN 顺序）。
func (c *Cluster) allNodes() []*node {
	c.nmu.RLock()
	defer c.nmu.RUnlock()
	out := append([]*node(nil), c.FEs...)
	out = append(out, c.BEs...)
	return append(out, c.CNs...)
}

func (c *Cluster) logf(format string, args ...any) {
	if c.cfg.Logger != nil {
		c.cfg.Logger.Printf(format, args...)
	}
}

// Start 启动假集群：MySQL 协议、每节点一个 HTTP /metrics、可选 SSH。
func Start(cfg Config) (*Cluster, error) {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Mode != ModeSharedNothing && cfg.Mode != ModeSharedData {
		return nil, fmt.Errorf("invalid mode %q", cfg.Mode)
	}
	if cfg.Scenario != ScenarioHealthy && cfg.Scenario != ScenarioProblems {
		return nil, fmt.Errorf("invalid scenario %q", cfg.Scenario)
	}
	if cfg.Version == "" {
		cfg.Version = "3.5.6"
	}

	c := &Cluster{
		cfg:       cfg,
		problems:  cfg.Scenario == ScenarioProblems,
		sharedDat: cfg.Mode == ModeSharedData,
		startedAt: time.Now(),
		conns:     map[net.Conn]struct{}{},
		done:      make(chan struct{}),
		sim:       newSim(cfg.Scenario == ScenarioProblems),
	}

	nFE, nBE, nCN := 4, 6, 2
	roles := []string{"LEADER", "FOLLOWER", "FOLLOWER", "OBSERVER"}
	if c.sharedDat {
		nFE, nBE, nCN = 3, 0, 4
	}
	if cfg.Empty {
		nFE, nBE, nCN = 0, 0, 0
	}
	c.nextIdx = [3]int{nFE, nBE, nCN}
	c.cred = newCredProvider(cfg.User, cfg.Password)
	for i := 0; i < nFE; i++ {
		c.FEs = append(c.FEs, &node{kind: kindFE, idx: i, role: roles[i], ports: defaultPorts(kindFE, i)})
	}
	for i := 0; i < nBE; i++ {
		c.BEs = append(c.BEs, &node{kind: kindBE, idx: i, ports: defaultPorts(kindBE, i)})
	}
	for i := 0; i < nCN; i++ {
		c.CNs = append(c.CNs, &node{kind: kindCN, idx: i, ports: defaultPorts(kindCN, i)})
	}
	k := 0
	for _, group := range [][]*node{c.FEs, c.BEs, c.CNs} {
		for _, n := range group {
			n.ip = cfg.Host
			if cfg.Hosts {
				n.ip = fmt.Sprintf("127.0.0.%d", 11+k)
			}
			k++
		}
	}

	c.sim.ops = newOpsSim(c)
	c.initBECfg()
	if cfg.S3 && c.cfg.S3AccessKey == "" {
		c.cfg.S3AccessKey = "fakeak"
	}

	ok := false
	defer func() {
		if !ok {
			c.Stop()
		}
	}()

	var err error
	c.mysqlLn, err = net.Listen("tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.QueryPort)))
	if err != nil {
		return nil, fmt.Errorf("listen query port: %w", err)
	}

	bases := map[nodeKind]int{kindFE: cfg.FEHTTPBase, kindBE: cfg.BEHTTPBase, kindCN: cfg.CNHTTPBase}
	for _, group := range [][]*node{c.FEs, c.BEs, c.CNs} {
		for _, n := range group {
			port := 0
			if b := bases[n.kind]; b > 0 {
				port = b + n.idx
			}
			if err := c.serveNodeHTTP(n, port); err != nil {
				return nil, err
			}
		}
	}

	if cfg.Hosts && !cfg.Empty {
		if cfg.SSHPort <= 0 {
			return nil, fmt.Errorf("hosts mode needs a fixed ssh port")
		}
		if err := c.startHosts(); err != nil {
			return nil, err
		}
	} else if cfg.SSHPort >= 0 {
		if err := c.startSSH(); err != nil {
			return nil, err
		}
	}

	if cfg.S3 {
		if err := c.startS3(); err != nil {
			return nil, err
		}
	}

	c.wg.Add(1)
	go c.serveMySQL()

	ok = true
	return c, nil
}

// Stop 关闭所有监听和活动连接。
func (c *Cluster) Stop() {
	c.once.Do(func() {
		close(c.done)
		if c.mysqlLn != nil {
			_ = c.mysqlLn.Close()
		}
		if c.sshLn != nil {
			_ = c.sshLn.Close()
		}
		c.mu.Lock()
		for _, ln := range c.extra {
			_ = ln.Close()
		}
		c.mu.Unlock()
		for _, ch := range c.Children() {
			ch.Stop()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, s := range c.https {
			_ = s.Shutdown(ctx)
		}
		c.mu.Lock()
		for conn := range c.conns {
			_ = conn.Close()
		}
		c.mu.Unlock()
		c.wg.Wait()
	})
}

// track 记录活动连接，Stop 时统一关闭；集群已停止时返回 false。
func (c *Cluster) track(conn net.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		return false
	default:
	}
	c.conns[conn] = struct{}{}
	return true
}

func (c *Cluster) untrack(conn net.Conn) {
	c.mu.Lock()
	delete(c.conns, conn)
	c.mu.Unlock()
}

// acceptLoop 是 MySQL/SSH 共用的 accept 循环。
func (c *Cluster) acceptLoop(ln net.Listener, handle func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-c.done:
				return
			default:
			}
			c.logf("accept: %v", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if !c.track(conn) {
			_ = conn.Close()
			return
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			defer c.untrack(conn)
			defer conn.Close()
			handle(conn)
		}()
	}
}

// VersionString 返回 current_version() 的完整值。
func (c *Cluster) VersionString() string { return c.versionString() }

// HasBE 是否有 BE 节点（存算一体）。
func (c *Cluster) HasBE() bool { return len(c.nodesOf(kindBE)) > 0 }
