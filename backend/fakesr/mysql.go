// @author: kevinkelly
// @license: Apache Licence
// @file: mysql.go
// @time: 2026/10/28

package fakesr

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"

	"github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/server"
)

// credProvider 对任意用户名都返回"存在"，非法用户得到随机密码，
// 从而统一走 1045 Access denied 而不是 "no such user"。
type credProvider struct {
	mu             sync.Mutex
	user, password string
	bogus          string
}

func (p *credProvider) setPassword(user, pw string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if user != p.user {
		return false
	}
	p.password = pw
	return true
}

func newCredProvider(user, password string) *credProvider {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &credProvider{user: user, password: password, bogus: hex.EncodeToString(b)}
}

func (p *credProvider) CheckUsername(string) (bool, error) { return true, nil }

func (p *credProvider) GetCredential(user string) (string, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if user == p.user {
		return p.password, true, nil
	}
	return p.bogus, true, nil
}

func (c *Cluster) serveMySQL() {
	defer c.wg.Done()
	c.serveMySQLOn(c.mysqlLn)
}

func (c *Cluster) serveMySQLOn(ln net.Listener) {
	srvConf := server.NewServer("8.0.33", mysql.DEFAULT_COLLATION_ID, mysql.AUTH_NATIVE_PASSWORD, nil, nil)
	cred := c.cred
	c.acceptLoop(ln, func(conn net.Conn) {
		mc, err := server.NewCustomizedConn(conn, srvConf, cred, &handler{c: c})
		if err != nil {
			c.logf("mysql handshake from %s: %v", conn.RemoteAddr(), err)
			return
		}
		for !mc.Closed() {
			if err := mc.HandleCommand(); err != nil {
				return
			}
		}
	})
}

// handler 实现 server.Handler。
type handler struct {
	server.EmptyHandler
	c *Cluster
}

func (h *handler) UseDB(string) error { return nil }

func (h *handler) HandleQuery(query string) (*mysql.Result, error) {
	h.c.logf("query: %s", query)
	return h.c.route(query)
}

func (h *handler) HandleOtherCommand(cmd byte, _ []byte) error {
	return mysql.NewError(mysql.ER_UNKNOWN_ERROR, fmt.Sprintf("fake starrocks: command %d is not supported", cmd))
}

var (
	reSpaces    = regexp.MustCompile(`\s+`)
	reFromTable = regexp.MustCompile(`\bfrom\s+([a-z0-9_]+)\.([a-z0-9_]+)`)
	reLike      = regexp.MustCompile(`(?i)\blike\s+['"]([^'"]*)['"]`)
)

// normalize 去除首尾空白、末尾分号和反引号，并压缩空白。
func normalize(q string) string {
	q = strings.TrimSpace(q)
	q = strings.TrimSpace(strings.TrimRight(q, ";"))
	q = strings.ReplaceAll(q, "`", "")
	return reSpaces.ReplaceAllString(q, " ")
}

// likePattern 提取 LIKE '<pattern>'；无 LIKE 时返回 ("", false)。
func likePattern(q string) (string, bool) {
	m := reLike.FindStringSubmatch(q)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// likeMatch 实现简单的 SQL LIKE（% 与 _），大小写不敏感。
func likeMatch(pattern, s string) bool {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(s)
}

func okResult() *mysql.Result { return &mysql.Result{} }

func unsupported(q string) error {
	return mysql.NewError(mysql.ER_PARSE_ERROR, "fake starrocks: unsupported query: "+q)
}

// route 将查询分发到对应的结果构造函数。
func (c *Cluster) route(raw string) (*mysql.Result, error) {
	q := normalize(raw)
	l := strings.ToLower(q)

	if res, handled, err := c.handleDDL(q); handled {
		return res, err
	}
	if res, handled, err := c.handleQuery(q, l); handled {
		return res, err
	}
	if res, handled, err := c.handleOps(q, l); handled {
		return res, err
	}
	if res, handled, err := c.handleNodes(q, l); handled {
		return res, err
	}
	switch {
	case l == "":
		return okResult(), nil
	case l == "select 1":
		return resultset([]string{"1"}, [][]any{{int64(1)}})
	case strings.HasPrefix(l, "set "), strings.HasPrefix(l, "use "):
		return okResult(), nil
	case strings.HasPrefix(l, "select @@version_comment"):
		return resultset([]string{"@@version_comment"}, [][]any{{"StarRocks version " + c.versionString()}})
	case l == "select current_version()":
		return resultset([]string{"current_version()"}, [][]any{{c.versionString()}})
	case l == "show frontends":
		return c.showFrontends()
	case l == "show backends":
		return c.showBackends()
	case l == "show compute nodes":
		return c.showComputeNodes()
	case strings.HasPrefix(l, "admin show frontend config"):
		return c.showFrontendConfigSim(q)
	case strings.HasPrefix(l, "show grants"):
		return c.showGrants()
	case strings.HasPrefix(l, "show proc ") && strings.Contains(l, "'/statistic'"):
		return c.showStatistic()
	case l == "show catalogs":
		return c.showCatalogs()
	case strings.HasPrefix(l, "show usage resource groups"):
		return c.showUsageResourceGroups()
	case strings.HasPrefix(l, "show resource groups"):
		return c.showResourceGroupsSim(strings.HasSuffix(l, " all"))
	case l == "show running queries":
		return c.showRunningQueries()
	case reRLTask.MatchString(q):
		return c.routineLoadTasks(reRLTask.FindStringSubmatch(q))
	case strings.HasPrefix(l, "show variables"), strings.HasPrefix(l, "show global variables"),
		strings.HasPrefix(l, "show session variables"):
		return c.showVariablesSim(q)
	case strings.HasPrefix(l, "select "):
		if m := reFromTable.FindStringSubmatch(l); m != nil {
			if fn := c.selectFrom(m[1], m[2], l, q); fn != nil {
				return fn()
			}
		}
	}
	return nil, unsupported(q)
}

// selectFrom 返回 information_schema / 审计表 SELECT 的结果构造函数，不支持时为 nil。
func (c *Cluster) selectFrom(db, table, l, q string) func() (*mysql.Result, error) {
	if db == "starrocks_audit_db__" && table == "starrocks_audit_tbl__" {
		return func() (*mysql.Result, error) { return c.auditSim(l, q) }
	}
	if db != "information_schema" {
		return nil
	}
	switch table {
	case "schemata":
		return func() (*mysql.Result, error) { return countResult(14) }
	case "tables":
		return func() (*mysql.Result, error) { return countResult(1286) }
	case "materialized_views":
		return c.materializedViewsSim
	case "routine_load_jobs":
		return c.routineLoadJobsSim
	case "partitions_meta":
		return c.partitionsMeta
	case "be_configs":
		return c.beConfigsSim
	case "loads":
		return func() (*mysql.Result, error) { return c.loadsSim(l) }
	}
	return nil
}

func countResult(n int64) (*mysql.Result, error) {
	return resultset([]string{"COUNT(*)"}, [][]any{{n}})
}

// resultset 构造文本协议结果集。不用 BuildSimpleTextResultset：它会把空字符串编码成 NULL。
// 列类型取自首行（string→VARCHAR, int64→BIGINT, float64→DOUBLE），空结果时为 VARCHAR。
func resultset(names []string, rows [][]any) (*mysql.Result, error) {
	rs := &mysql.Resultset{Fields: make([]*mysql.Field, len(names))}
	for i, n := range names {
		f := &mysql.Field{Name: []byte(n), Charset: 33, Type: mysql.MYSQL_TYPE_VAR_STRING}
		if len(rows) > 0 {
			switch rows[0][i].(type) {
			case int64:
				f.Type, f.Charset, f.Flag = mysql.MYSQL_TYPE_LONGLONG, 63, mysql.BINARY_FLAG|mysql.NOT_NULL_FLAG
			case float64:
				f.Type, f.Charset, f.Flag = mysql.MYSQL_TYPE_DOUBLE, 63, mysql.BINARY_FLAG|mysql.NOT_NULL_FLAG
			}
		}
		rs.Fields[i] = f
	}
	for ri, r := range rows {
		if len(r) != len(names) {
			return nil, fmt.Errorf("row %d has %d columns, want %d", ri, len(r), len(names))
		}
		var data []byte
		for _, v := range r {
			b, err := mysql.FormatTextValue(v)
			if err != nil {
				return nil, err
			}
			data = append(data, mysql.PutLengthEncodedString(b)...)
		}
		rs.RowDatas = append(rs.RowDatas, data)
	}
	return &mysql.Result{Resultset: rs}, nil
}
