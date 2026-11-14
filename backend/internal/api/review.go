// @author: kevinkelly
// @license: Apache Licence
// @file: review.go
// @time: 2026/10/28

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/perm"
	"starrocks-ops/internal/store"
)

// 双人复核：开启后，高危操作（权限码标为高危且接入了 review 包装的接口）不会立即执行，
// 而是保存为待复核申请；由另一名拥有同一权限码（且数据范围包含该集群）的用户批准后，服务端以申请人的身份执行原请求。

const reviewTTL = 24 * time.Hour

type ctxReviewed struct{}

func isReviewed(r *http.Request) bool { v, _ := r.Context().Value(ctxReviewed{}).(bool); return v }

// describe 返回申请的标题与所属集群（用于列表展示与权限判断）。
type describe func(r *http.Request) (title, cluster string)

func okStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

var pathKeys = []string{"id", "login", "role", "rid", "db", "name", "idx", "kind", "key", "repo", "pid", "node", "bid", "tid"}

// review 包装高危操作的处理函数。
func (s *Server) review(code string, desc describe, h handler) handler {
	s.reviewable[code] = h
	return func(w http.ResponseWriter, r *http.Request, u *model.User) {
		if isReviewed(r) || !s.cfg().Security.DualReview {
			h(w, r, u)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			fail(w, http.StatusBadRequest, "请求体过大")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body)) // 让 desc 也能读取请求体
		title, cluster := desc(r)
		pv := map[string]string{}
		for _, k := range pathKeys {
			if v := r.PathValue(k); v != "" {
				pv[k] = v
			}
		}
		var rv model.Review
		err = s.Store.Update(func(st *store.State) error {
			for _, x := range st.Reviews {
				if x.Status == "pending" && x.Requester == u.Login && x.Code == code && x.Path == r.URL.Path && x.Method == r.Method && time.Since(x.CreatedAt) < reviewTTL {
					return conflict("已有相同的待复核申请，请等待另一位拥有该权限的用户处理")
				}
			}
			b := make([]byte, 3)
			rand.Read(b)
			rv = model.Review{ID: "RV-" + time.Now().Format("20060102150405") + "-" + hex.EncodeToString(b), Code: code, Title: title, Cluster: cluster, Requester: u.Login, IP: clientIP(r),
				CreatedAt: time.Now(), Method: r.Method, Path: r.URL.Path, PathVals: pv, Body: string(body), Status: "pending"}
			st.Reviews = append(st.Reviews, rv)
			return nil
		})
		if err != nil {
			writeErr(w, err)
			return
		}
		s.Store.Audit(store.AuditEntry{User: u.Login, IP: clientIP(r), Cluster: cluster, Code: code, Act: title + "（提交复核 " + rv.ID + "）", Result: "待复核", SQL: r.Method + " " + r.URL.Path})
		okStatus(w, http.StatusAccepted, map[string]any{"pending": true, "reviewId": rv.ID, "message": "平台已开启双人复核：操作已提交，需另一名拥有该权限的用户在「操作审计 → 待复核」中批准后才会执行"})
	}
}

type recorder struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (r *recorder) Header() http.Header         { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return r.buf.Write(b) }
func (r *recorder) WriteHeader(c int)           { r.code = c }

// runReview 以申请人的身份执行已批准的请求；执行前重新校验申请人的账号状态、权限与数据范围。
func (s *Server) runReview(ctx context.Context, rv model.Review) (bool, string) {
	h := s.reviewable[rv.Code]
	if h == nil {
		return false, "该操作不支持复核执行"
	}
	var req model.User
	var acc *perm.Access
	var found bool
	s.Store.View(func(st *store.State) {
		for _, x := range st.Users {
			if x.Login == rv.Requester {
				req, found = x, true
				acc = accessIn(st, x.Roles)
			}
		}
	})
	switch {
	case !found || req.Locked || req.Expired(time.Now()):
		return false, "申请人的账号已不可用"
	case !acc.Can(rv.Code):
		return false, "申请人当前已没有该操作的权限"
	case rv.PathVals["id"] != "" && !acc.Cluster(rv.PathVals["id"]):
		return false, "申请人当前已没有该集群的访问权限"
	}
	c := context.WithValue(context.WithValue(ctx, ctxKey{}, acc), ctxReviewed{}, true)
	rq, err := http.NewRequestWithContext(c, rv.Method, rv.Path, strings.NewReader(rv.Body))
	if err != nil {
		return false, err.Error()
	}
	rq.RemoteAddr = net.JoinHostPort(rv.IP, "0")
	rq.Header.Set("Content-Type", "application/json")
	for k, v := range rv.PathVals {
		rq.SetPathValue(k, v)
	}
	rec := &recorder{h: http.Header{}}
	h(rec, rq, &req)
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	if rec.code >= 300 {
		var e struct{ Error string }
		json.Unmarshal(rec.buf.Bytes(), &e)
		if e.Error == "" {
			e.Error = "HTTP " + http.StatusText(rec.code)
		}
		return false, e.Error
	}
	return true, "已执行"
}

type reviewOut struct {
	ID         string    `json:"id"`
	Code       string    `json:"code"`
	Menu       string    `json:"menu"`
	Title      string    `json:"title"`
	Cluster    string    `json:"cluster"`
	Requester  string    `json:"requester"`
	IP         string    `json:"ip"`
	CreatedAt  time.Time `json:"createdAt"`
	Detail     string    `json:"detail"`
	Status     string    `json:"status"`
	Reviewer   string    `json:"reviewer"`
	DecidedAt  time.Time `json:"decidedAt"`
	Note       string    `json:"note"`
	CanApprove bool      `json:"canApprove"`
	Mine       bool      `json:"mine"`
}

func canDecide(rv model.Review, acc *perm.Access, me string) bool {
	return rv.Status == "pending" && rv.Requester != me && acc.Can(rv.Code) && (rv.Cluster == "" || acc.Cluster(rv.Cluster)) && time.Since(rv.CreatedAt) < reviewTTL
}

func (s *Server) listReviews(w http.ResponseWriter, r *http.Request, u *model.User) {
	acc := accessOf(r)
	out := []reviewOut{}
	pending := 0
	s.Store.View(func(st *store.State) {
		for i := len(st.Reviews) - 1; i >= 0; i-- {
			rv := st.Reviews[i]
			if !(acc.AllClusters() || rv.Requester == u.Login || (rv.Cluster != "" && acc.Cluster(rv.Cluster))) {
				continue
			}
			if rv.Status == "pending" && time.Since(rv.CreatedAt) >= reviewTTL {
				rv.Status, rv.Note = "expired", "超过 24 小时未处理，已失效"
			}
			if rv.Status == "pending" {
				pending++
			}
			if len(out) < 200 {
				out = append(out, reviewOut{ID: rv.ID, Code: rv.Code, Menu: perm.MenuOf(rv.Code), Title: rv.Title, Cluster: rv.Cluster, Requester: rv.Requester, IP: rv.IP, CreatedAt: rv.CreatedAt,
					Detail: rv.Method + " " + rv.Path, Status: rv.Status, Reviewer: rv.Reviewer, DecidedAt: rv.DecidedAt, Note: rv.Note, CanApprove: canDecide(rv, acc, u.Login), Mine: rv.Requester == u.Login})
			}
		}
	})
	ok(w, map[string]any{"items": out, "pending": pending, "dualReview": s.cfg().Security.DualReview})
}

// claim 把待复核申请置为目标状态（原子操作，避免重复处理）。check 返回错误则不修改。
func (s *Server) claim(rid string, to string, check func(rv model.Review) error, set func(rv *model.Review)) (model.Review, error) {
	var out model.Review
	err := s.Store.Update(func(st *store.State) error {
		for i := range st.Reviews {
			if st.Reviews[i].ID != rid {
				continue
			}
			rv := &st.Reviews[i]
			if err := check(*rv); err != nil { // 先校验身份与权限，再看状态
				return err
			}
			if rv.Status != "pending" {
				return conflict("该申请已处理（" + rv.Status + "）")
			}
			if time.Since(rv.CreatedAt) >= reviewTTL {
				rv.Status, rv.DecidedAt, rv.Note = "expired", time.Now(), "超过 24 小时未处理，已失效"
				return conflict("该申请已超过 24 小时，已失效，请重新提交")
			}
			rv.Status, rv.DecidedAt = to, time.Now()
			if set != nil {
				set(rv)
			}
			out = *rv
			return nil
		}
		return missing("复核申请不存在")
	})
	return out, err
}

func (s *Server) reviewerCheck(acc *perm.Access, me string) func(model.Review) error {
	return func(rv model.Review) error {
		switch {
		case rv.Requester == me:
			return forbidden("不能复核自己提交的操作")
		case !acc.Can(rv.Code):
			return forbidden("您没有该操作的权限（" + rv.Code + "），不能复核")
		case rv.Cluster != "" && !acc.Cluster(rv.Cluster):
			return forbidden("该集群不在您的数据范围内，不能复核")
		}
		return nil
	}
}

func (s *Server) approveReview(w http.ResponseWriter, r *http.Request, u *model.User) {
	rid := r.PathValue("rid")
	rv, err := s.claim(rid, "executing", s.reviewerCheck(accessOf(r), u.Login), func(rv *model.Review) { rv.Reviewer = u.Login })
	if err != nil {
		s.audit(r, u, "", "review.approve", "批准复核 "+rid, err)
		writeErr(w, err)
		return
	}
	done, note := s.runReview(r.Context(), rv)
	status := "approved"
	if !done {
		status = "failed"
	}
	s.Store.Update(func(st *store.State) error {
		for i := range st.Reviews {
			if st.Reviews[i].ID == rid {
				st.Reviews[i].Status, st.Reviews[i].Note, st.Reviews[i].DecidedAt = status, note, time.Now()
			}
		}
		return nil
	})
	res := "成功"
	if !done {
		res = "失败：" + note
	}
	s.Store.Audit(store.AuditEntry{User: u.Login, IP: clientIP(r), Cluster: rv.Cluster, Code: "review.approve", Act: "批准复核 " + rid + "：" + rv.Title + "（申请人 " + rv.Requester + "）", Result: res})
	ok(w, map[string]any{"ok": done, "status": status, "note": note})
}

func (s *Server) rejectReview(w http.ResponseWriter, r *http.Request, u *model.User) {
	rid := r.PathValue("rid")
	var in struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Note = cleanText(in.Note)
	if rlen(in.Note) > 200 {
		fail(w, http.StatusBadRequest, "驳回原因最多 200 个字")
		return
	}
	rv, err := s.claim(rid, "rejected", s.reviewerCheck(accessOf(r), u.Login), func(rv *model.Review) { rv.Reviewer, rv.Note = u.Login, in.Note })
	if err != nil {
		s.audit(r, u, "", "review.reject", "驳回复核 "+rid, err)
		writeErr(w, err)
		return
	}
	s.Store.Audit(store.AuditEntry{User: u.Login, IP: clientIP(r), Cluster: rv.Cluster, Code: "review.reject", Act: "驳回复核 " + rid + "：" + rv.Title + "（申请人 " + rv.Requester + "）", Result: "拒绝（" + u.Login + " 驳回）"})
	ok(w, map[string]bool{"ok": true})
}

func (s *Server) withdrawReview(w http.ResponseWriter, r *http.Request, u *model.User) {
	rid := r.PathValue("rid")
	rv, err := s.claim(rid, "withdrawn", func(rv model.Review) error {
		if rv.Requester != u.Login {
			return forbidden("只有申请人可以撤回")
		}
		return nil
	}, nil)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.audit(r, u, rv.Cluster, "review.withdraw", "撤回复核申请 "+rid+"："+rv.Title, nil)
	ok(w, map[string]bool{"ok": true})
}
