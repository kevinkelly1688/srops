// @author: kevinkelly
// @license: Apache Licence
// @file: param.go
// @time: 2026/10/28

package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sr"
	"starrocks-ops/internal/store"
)

// beNode 是可以接受 BE 配置修改的节点（BE / CN）。
type beNode struct {
	ID, Host string
	HTTP     int
	Kind     string // be / cn
}

func (n beNode) label() string { return fmt.Sprintf("%s:%d", n.Host, n.HTTP) }

// beNodes 读取 BE 与 CN 节点，key 为 be_configs 中的 BE_ID。
func beNodes(x srCtx) map[string]beNode {
	out := map[string]beNode{}
	for _, q := range []struct{ sql, id, kind string }{{"SHOW BACKENDS", "BackendId", "be"}, {"SHOW COMPUTE NODES", "ComputeNodeId", "cn"}} {
		r, err := x.cl.Query(x.ctx, q.sql)
		if err != nil {
			continue
		}
		for i := range r.Data {
			n := beNode{ID: r.Get(i, q.id), Host: r.Get(i, "IP"), HTTP: atoiLoose(r.Get(i, "HttpPort")), Kind: q.kind}
			if n.ID != "" && n.Host != "" && n.HTTP > 0 {
				out[n.ID] = n
			}
		}
	}
	return out
}

func (s *Server) listParams(w http.ResponseWriter, r *http.Request, u *model.User) {
	kind := r.PathValue("kind")
	x, done, ok2 := s.open(w, r, 60*time.Second)
	if !ok2 {
		return
	}
	defer done()
	var items []sr.Param
	var nodes []string
	var err error
	switch kind {
	case "fe":
		items, err = x.cl.FEParams(x.ctx)
	case "var":
		items, err = x.cl.Variables(x.ctx)
	case "be":
		names := map[string]string{}
		for id, n := range beNodes(x) {
			names[id] = n.label()
		}
		items, nodes, err = x.cl.BEConfigs(x.ctx, names)
		for i := range items {
			items[i].NodeVals = nil // 列表只返回汇总值；各节点的值在「节点参数对比」中查看
		}
	default:
		fail(w, http.StatusNotFound, "未知的参数类型")
		return
	}
	if err != nil {
		fail(w, http.StatusBadGateway, "读取参数失败："+err.Error())
		return
	}
	if items == nil {
		items = []sr.Param{}
	}
	ok(w, map[string]any{"items": items, "nodes": nodes})
}

// compareParams 返回各节点取值不一致的参数。
func (s *Server) compareParams(w http.ResponseWriter, r *http.Request, u *model.User) {
	kind := r.PathValue("kind")
	x, done, ok2 := s.open(w, r, 90*time.Second)
	if !ok2 {
		return
	}
	defer done()
	var diffs []sr.Param
	var nodes, warns []string
	switch kind {
	case "fe":
		fes, w2, closeAll := s.feClients(x)
		defer closeAll()
		warns = w2
		per := map[string]map[string]sr.Param{}
		for k, cl := range fes {
			list, err := cl.FEParams(x.ctx)
			if err != nil {
				warns = append(warns, "FE "+k+" 读取失败："+err.Error())
				continue
			}
			nodes = append(nodes, k)
			m := map[string]sr.Param{}
			for _, p := range list {
				m[p.Key] = p
			}
			per[k] = m
		}
		sort.Strings(nodes)
		keys := map[string]sr.Param{}
		for _, m := range per {
			for k, p := range m {
				keys[k] = p
			}
		}
		for k, p := range keys {
			vals := map[string]string{}
			distinct := map[string]bool{}
			for _, n := range nodes {
				v := per[n][k].Value
				vals[n] = v
				distinct[v] = true
			}
			if len(distinct) > 1 {
				p.NodeVals, p.Diff = vals, true
				diffs = append(diffs, p)
			}
		}
	case "be":
		names := map[string]string{}
		for id, n := range beNodes(x) {
			names[id] = n.label()
		}
		list, ns, err := x.cl.BEConfigs(x.ctx, names)
		if err != nil {
			fail(w, http.StatusBadGateway, "读取参数失败："+err.Error())
			return
		}
		nodes = ns
		for _, p := range list {
			if p.Diff {
				diffs = append(diffs, p)
			}
		}
	default:
		fail(w, http.StatusNotFound, "只支持 FE 与 BE 的节点参数对比")
		return
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Key < diffs[j].Key })
	if diffs == nil {
		diffs = []sr.Param{}
	}
	ok(w, map[string]any{"items": diffs, "nodes": nodes, "warnings": warns})
}

type paramEdit struct {
	Value   string   `json:"value"`
	Scope   []string `json:"scope"` // 空 = 全部节点；否则为节点标识（FE 为 host:port，BE 为 host:http 端口）
	Persist bool     `json:"persist"`
}

func (s *Server) recordParam(c model.ParamChange) {
	s.Store.Update(func(st *store.State) error {
		st.Params = append(st.Params, c)
		if len(st.Params) > 5000 {
			st.Params = st.Params[len(st.Params)-5000:]
		}
		return nil
	})
}

func (s *Server) editParam(w http.ResponseWriter, r *http.Request, u *model.User) {
	kind, key := r.PathValue("kind"), r.PathValue("key")
	var in paramEdit
	if !decode(w, r, &in) {
		return
	}
	if in.Persist && !accessOf(r).Can("param.persist") {
		fail(w, http.StatusForbidden, "无权限：param.persist（持久化到配置文件）")
		return
	}
	if in.Persist && kind == "var" {
		fail(w, http.StatusBadRequest, "全局变量由 SET GLOBAL 自动持久化，无需写入配置文件")
		return
	}
	if !sr.ValidParamKey(key) {
		fail(w, http.StatusBadRequest, "参数名不合法")
		return
	}
	if strings.ContainsAny(in.Value, "\r\n\x00") || len(in.Value) > 1000 {
		fail(w, http.StatusBadRequest, "参数值不合法")
		return
	}
	x, done, ok2 := s.open(w, r, 90*time.Second)
	if !ok2 {
		return
	}
	defer done()
	if in.Persist {
		if _, err := s.sshTarget(x.c, "127.0.0.1"); err != nil { // 只为校验 SSH / 部署目录是否已完善
			writeErr(w, err)
			return
		}
	}
	rec := model.ParamChange{At: time.Now(), User: u.Login, Cluster: x.c.ID, Kind: kind, Key: key, To: in.Value, Persist: "仅内存"}
	var err error
	var stmts []string
	var tg []confTarget
	switch kind {
	case "fe":
		err = s.editFE(x, in, &rec, &stmts, &tg)
	case "var":
		err = s.editVar(x, in, &rec, &stmts)
	case "be":
		err = s.editBE(x, in, &rec, &stmts, &tg)
	default:
		fail(w, http.StatusNotFound, "未知的参数类型")
		return
	}
	var be apiErr
	if e, isAPI := err.(apiErr); isAPI {
		be = e
	}
	if be.code == http.StatusBadRequest || be.code == http.StatusNotFound { // 校验类错误：未执行，不记变更历史
		s.auditSQL(r, u, "param.edit", "修改参数 "+key+"（未执行）", "", err, x.c.ID)
		writeErr(w, err)
		return
	}
	rec.Status = "已生效"
	if err == nil && in.Persist {
		var perr error
		rec.Persist, perr = s.persistParam(x, key, in.Value, tg, &stmts)
		if perr != nil {
			err = perr
		}
	}
	if err != nil {
		rec.Status = "失败：" + err.Error()
	}
	s.recordParam(rec)
	act := fmt.Sprintf("修改 %s 参数 %s：%s → %s（%s）", map[string]string{"fe": "FE", "be": "BE", "var": "全局变量"}[kind], key, rec.From, rec.To, rec.Scope)
	s.auditSQL(r, u, "param.edit", act, strings.Join(stmts, "\n"), err, x.c.ID)
	if err != nil {
		sqlFail(w, err)
		return
	}
	ok(w, map[string]any{"ok": true, "from": rec.From, "to": rec.To, "scope": rec.Scope})
}

func (s *Server) editFE(x srCtx, in paramEdit, rec *model.ParamChange, stmts *[]string, tg *[]confTarget) error {
	list, err := x.cl.FEParams(x.ctx)
	if err != nil {
		return err
	}
	var cur *sr.Param
	for i := range list {
		if list[i].Key == rec.Key {
			cur = &list[i]
		}
	}
	switch {
	case cur == nil:
		return missing("FE 配置项 " + rec.Key + " 不存在")
	case !cur.Mutable && !in.Persist:
		return bad(rec.Key + " 是静态参数，不能在线修改：勾选「写入配置文件」可只修改 fe.conf，重启 FE 后生效")
	}
	fileOnly := !cur.Mutable
	rec.From = cur.Value
	q, err := sr.SetFEConfigSQL(rec.Key, in.Value)
	if err != nil {
		return bad(err.Error())
	}
	if !fileOnly {
		*stmts = append(*stmts, q+";")
	}
	nodes, _ := x.cl.Nodes(x.ctx, x.c.Mode)
	feAddr := map[string]string{} // host:query_port → host
	var allHosts []string
	for _, n := range nodes {
		if n.Type == "fe" {
			feAddr[fmt.Sprintf("%s:%d", n.Host, n.Port)] = n.Host
			allHosts = append(allHosts, n.Host)
		}
	}
	if len(in.Scope) == 0 {
		rec.Scope = "全部 FE"
		for _, h := range uniqStr(allHosts) {
			*tg = append(*tg, confTarget{"fe", h})
		}
		if fileOnly {
			rec.Persist = "仅配置文件"
			return nil
		}
		return s.onAllFEs(x, q)
	}
	rec.Scope = strings.Join(in.Scope, "、")
	if fileOnly {
		for _, k := range in.Scope {
			h, found := feAddr[k]
			if !found {
				return bad("FE " + k + " 不存在")
			}
			*tg = append(*tg, confTarget{"fe", h})
		}
		return nil
	}
	fes, _, closeAll := s.feClients(x)
	defer closeAll()
	for _, k := range in.Scope {
		if _, found := fes[k]; !found {
			return bad("FE " + k + " 不存在或未存活")
		}
	}
	for _, k := range in.Scope {
		if err := fes[k].Exec(x.ctx, q); err != nil {
			return fmt.Errorf("FE %s：%v", k, err)
		}
		*tg = append(*tg, confTarget{"fe", feAddr[k]})
	}
	return nil
}

func (s *Server) editVar(x srCtx, in paramEdit, rec *model.ParamChange, stmts *[]string) error {
	list, err := x.cl.Variables(x.ctx)
	if err != nil {
		return err
	}
	found := false
	for _, p := range list {
		if p.Key == rec.Key {
			found, rec.From = true, p.Value
		}
	}
	if !found {
		return missing("全局变量 " + rec.Key + " 不存在")
	}
	q, err := sr.SetVariableSQL(rec.Key, in.Value)
	if err != nil {
		return bad(err.Error())
	}
	rec.Scope, rec.Persist = "GLOBAL 变量", "—"
	*stmts = append(*stmts, q+";")
	return x.cl.Exec(x.ctx, q)
}

func (s *Server) editBE(x srCtx, in paramEdit, rec *model.ParamChange, stmts *[]string, tg *[]confTarget) error {
	nodes := beNodes(x)
	names := map[string]string{}
	byLabel := map[string]beNode{}
	for id, n := range nodes {
		names[id] = n.label()
		byLabel[n.label()] = n
	}
	list, _, err := x.cl.BEConfigs(x.ctx, names)
	if err != nil {
		return err
	}
	var cur *sr.Param
	for i := range list {
		if list[i].Key == rec.Key {
			cur = &list[i]
		}
	}
	switch {
	case cur == nil:
		return missing("BE 配置项 " + rec.Key + " 不存在")
	case !cur.Mutable && !in.Persist:
		return bad(rec.Key + " 是静态参数，不能在线修改：勾选「写入配置文件」可只修改 be.conf / cn.conf，重启节点后生效")
	}
	fileOnly := !cur.Mutable
	rec.From = cur.Value
	var targets []beNode
	if len(in.Scope) == 0 {
		rec.Scope = fmt.Sprintf("全部 BE / CN（%d）", len(cur.NodeVals))
		for lbl := range cur.NodeVals {
			if n, found := byLabel[lbl]; found {
				targets = append(targets, n)
			}
		}
	} else {
		rec.Scope = strings.Join(in.Scope, "、")
		for _, k := range in.Scope {
			n, found := byLabel[k]
			if !found {
				return bad("节点 " + k + " 不存在")
			}
			targets = append(targets, n)
		}
	}
	if len(targets) == 0 {
		return bad("没有可修改的节点")
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].label() < targets[j].label() })
	for _, n := range targets {
		*tg = append(*tg, confTarget{n.Kind, n.Host})
	}
	if fileOnly {
		return nil
	}
	var okN, bad []string
	for _, n := range targets {
		*stmts = append(*stmts, fmt.Sprintf("curl -XPOST http://%s/api/update_config?%s=%s", n.label(), rec.Key, in.Value))
		if e := sr.SetBEConfig(x.ctx, n.Host, n.HTTP, rec.Key, in.Value); e != nil {
			bad = append(bad, n.label()+": "+e.Error())
		} else {
			okN = append(okN, n.label())
		}
	}
	if len(bad) > 0 {
		msg := strings.Join(bad, "；")
		if len(okN) > 0 {
			msg += "（已在 " + strconv.Itoa(len(okN)) + " 个节点生效：" + strings.Join(okN, "、") + "）"
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func (s *Server) paramHistory(w http.ResponseWriter, r *http.Request, u *model.User) {
	cid := r.PathValue("id")
	out := []model.ParamChange{}
	s.Store.View(func(st *store.State) {
		for i := len(st.Params) - 1; i >= 0 && len(out) < 500; i-- {
			if st.Params[i].Cluster == cid {
				out = append(out, st.Params[i])
			}
		}
	})
	ok(w, map[string]any{"items": out})
}
