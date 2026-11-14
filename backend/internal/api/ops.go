// @author: kevinkelly
// @license: Apache Licence
// @file: ops.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
)

// srCtx 是一次集群 SQL 请求的上下文。
type srCtx struct {
	c   model.Cluster
	cl  *sr.Client
	ctx context.Context
}

// open 解析路径中的集群并取得连接；失败时已写好响应并返回 ok=false。
func (s *Server) open(w http.ResponseWriter, r *http.Request, d time.Duration) (x srCtx, done func(), ok bool) {
	c, found := s.cluster(w, r)
	if !found {
		return x, nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), d)
	cl, err := s.Mgr.Client(ctx, c)
	if err != nil {
		cancel()
		fail(w, http.StatusBadGateway, "连接集群失败："+err.Error())
		return x, nil, false
	}
	return srCtx{c: c, cl: cl, ctx: ctx}, cancel, true
}

// sqlFail 把 StarRocks 返回的错误输出给前端。
func sqlFail(w http.ResponseWriter, err error) {
	fail(w, http.StatusBadGateway, err.Error())
}

// exec 依次执行语句并写审计（含执行的 SQL，密钥已脱敏）；遇到错误即停止，错误信息注明已执行到哪一步。
func (s *Server) exec(r *http.Request, u *model.User, x srCtx, code, act string, stmts ...string) error {
	var done []string
	var err error
	for i, q := range stmts {
		if err = x.cl.Exec(x.ctx, q); err != nil {
			if len(stmts) > 1 {
				err = fmt.Errorf("第 %d/%d 条语句失败（前 %d 条已生效）：%v", i+1, len(stmts), i, err)
			}
			break
		}
		done = append(done, q)
	}
	shown := make([]string, 0, len(stmts))
	for _, q := range stmts {
		shown = append(shown, sr.MaskSecrets(q)+";")
	}
	s.auditSQL(r, u, code, act, strings.Join(shown, "\n"), err, x.c.ID)
	return err
}

// feClients 为集群的每个存活 FE 各开一个连接（修改 FE 配置、对比配置时需要逐台执行）。
func (s *Server) feClients(x srCtx) (map[string]*sr.Client, []string, func()) {
	var warns []string
	out := map[string]*sr.Client{}
	nodes, w := x.cl.Nodes(x.ctx, x.c.Mode)
	warns = append(warns, w...)
	ci, err := s.Mgr.ConnInfo(x.c)
	if err != nil {
		return out, append(warns, err.Error()), func() {}
	}
	for _, n := range nodes {
		if n.Type != "fe" {
			continue
		}
		key := fmt.Sprintf("%s:%d", n.Host, n.Port)
		if _, dup := out[key]; dup {
			continue
		}
		if !n.Alive {
			warns = append(warns, "FE "+key+" 未存活，已跳过")
			continue
		}
		one := ci
		one.Hosts, one.QueryPort = []string{n.Host}, n.Port
		cctx, cancel := context.WithTimeout(x.ctx, 6*time.Second)
		cl, err := sr.Open(cctx, one)
		cancel()
		if err != nil {
			warns = append(warns, "FE "+key+" 连接失败："+err.Error())
			continue
		}
		out[key] = cl
	}
	return out, warns, func() {
		for _, c := range out {
			c.Close()
		}
	}
}

// onAllFEs 在每个存活 FE 上执行同一条语句；任意一台失败都会返回错误（并列出成功的 FE）。
func (s *Server) onAllFEs(x srCtx, stmt string) error {
	fes, warns, closeAll := s.feClients(x)
	defer closeAll()
	if len(fes) == 0 {
		return fmt.Errorf("没有可连接的 FE：%s", strings.Join(warns, "；"))
	}
	var okFE, bad []string
	for k, cl := range fes {
		if err := cl.Exec(x.ctx, stmt); err != nil {
			bad = append(bad, k+": "+err.Error())
		} else {
			okFE = append(okFE, k)
		}
	}
	if len(bad) > 0 {
		msg := strings.Join(bad, "；")
		if len(okFE) > 0 {
			msg += "（已在 " + strings.Join(okFE, "、") + " 上生效）"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
