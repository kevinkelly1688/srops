// @author: kevinkelly
// @license: Apache Licence
// @file: client.go
// @time: 2026/10/28

// Package sr 封装对 StarRocks 集群的只读访问：MySQL 协议执行 SHOW / information_schema 查询，HTTP 读取 FE / BE 的 /metrics。
// 所有结果按列名读取，兼容不同版本列顺序与新增列。
package sr

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// ConnInfo 是连接一个集群所需的信息（密码为明文，仅在内存中使用）。
type ConnInfo struct {
	Hosts     []string // FE 地址（可多个，依次尝试）
	QueryPort int
	HTTPPort  int
	User      string
	Password  string
}

// Client 持有到某个集群的连接池。
type Client struct {
	DB   *sql.DB
	Host string // 实际连通的 FE
	info ConnInfo
	http *http.Client
}

// Open 依次尝试 FE 地址，返回第一个能 Ping 通的连接。
func Open(ctx context.Context, ci ConnInfo) (*Client, error) {
	if len(ci.Hosts) == 0 {
		return nil, errors.New("未填写 FE 地址")
	}
	var errs []string
	for _, h := range ci.Hosts {
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd = ci.User, ci.Password
		cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(h, strconv.Itoa(ci.QueryPort))
		cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 60*time.Second, 30*time.Second
		cfg.AllowNativePasswords = true
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			errs = append(errs, h+": "+err.Error())
			continue
		}
		db := sql.OpenDB(connector)
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(2)
		db.SetConnMaxIdleTime(5 * time.Minute)
		pctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		err = db.PingContext(pctx)
		cancel()
		if err != nil {
			db.Close()
			errs = append(errs, h+": "+friendlyErr(err))
			continue
		}
		return &Client{DB: db, Host: h, info: ci, http: &http.Client{Timeout: 5 * time.Second}}, nil
	}
	return nil, fmt.Errorf("无法连接 FE：%s", strings.Join(errs, "；"))
}

func (c *Client) Close() error { return c.DB.Close() }

func friendlyErr(err error) string {
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1045 {
		return "账号或密码错误"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "连接超时（检查网络与 query_port）"
	}
	return err.Error()
}

// Rows 是按列名访问的查询结果。
type Rows struct {
	Cols []string
	Data [][]string
	idx  map[string]int
}

// Get 返回第 i 行列 col 的值（列名不区分大小写，不存在返回 ""）。
func (r *Rows) Get(i int, col string) string {
	if r.idx == nil {
		r.idx = map[string]int{}
		for k, c := range r.Cols {
			r.idx[strings.ToLower(c)] = k
		}
	}
	k, ok := r.idx[strings.ToLower(col)]
	if !ok || i >= len(r.Data) || k >= len(r.Data[i]) {
		return ""
	}
	return r.Data[i][k]
}

// Has 判断列是否存在。
func (r *Rows) Has(col string) bool { r.Get(0, col); _, ok := r.idx[strings.ToLower(col)]; return ok }

// Query 执行只读 SQL（文本协议，不带参数）。
func (c *Client) Query(ctx context.Context, q string) (*Rows, error) {
	rs, err := c.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	out := &Rows{Cols: cols}
	for rs.Next() {
		raw := make([]sql.NullString, len(cols))
		ptr := make([]any, len(cols))
		for i := range raw {
			ptr[i] = &raw[i]
		}
		if err := rs.Scan(ptr...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, v := range raw {
			row[i] = v.String
		}
		out.Data = append(out.Data, row)
	}
	return out, rs.Err()
}

// Scalar 返回第一行第一列。
func (c *Client) Scalar(ctx context.Context, q string) (string, error) {
	r, err := c.Query(ctx, q)
	if err != nil {
		return "", err
	}
	if len(r.Data) == 0 || len(r.Data[0]) == 0 {
		return "", nil
	}
	return r.Data[0][0], nil
}

// Version 返回 current_version()，去掉 commit 后缀。
func (c *Client) Version(ctx context.Context) (string, error) {
	v, err := c.Scalar(ctx, "SELECT current_version()")
	if err != nil {
		return "", err
	}
	if i := strings.IndexAny(v, "- "); i > 0 {
		v = v[:i]
	}
	return v, nil
}

// FEConfig 读取 FE 配置项的值（ADMIN SHOW FRONTEND CONFIG）。
func (c *Client) FEConfig(ctx context.Context, key string) (string, error) {
	r, err := c.Query(ctx, fmt.Sprintf("ADMIN SHOW FRONTEND CONFIG LIKE '%s'", key))
	if err != nil {
		return "", err
	}
	if len(r.Data) == 0 {
		return "", nil
	}
	return r.Get(0, "Value"), nil
}

// RunMode 返回 shared-nothing / shared-data。
func (c *Client) RunMode(ctx context.Context) string {
	v, err := c.FEConfig(ctx, "run_mode")
	if err == nil && strings.Contains(strings.ToLower(v), "shared_data") {
		return "shared-data"
	}
	return "shared-nothing"
}

// Variable 读取系统变量（SHOW VARIABLES LIKE）。
func (c *Client) Variable(ctx context.Context, name string) (string, error) {
	r, err := c.Query(ctx, fmt.Sprintf("SHOW VARIABLES LIKE '%s'", name))
	if err != nil || len(r.Data) == 0 {
		return "", err
	}
	return r.Get(0, "Value"), nil
}

// HTTPGet 读取节点 HTTP 接口（如 /metrics）。
func (c *Client) HTTPGet(ctx context.Context, host string, port int, path string) ([]byte, error) {
	return httpGet(ctx, c.http, fmt.Sprintf("http://%s%s", net.JoinHostPort(host, strconv.Itoa(port)), path))
}
