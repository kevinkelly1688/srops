// @author: kevinkelly
// @license: Apache Licence
// @file: task.go
// @time: 2026/10/28

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/perm"
)

// 后台任务：节点启停、部署、升级等耗时操作。HTTP 请求只负责校验并创建任务，立即返回任务号；
// 前端轮询 GET /api/tasks/{tid} 查看进度与日志。任务日志保存在 data/tasks/，平台重启后仍可查看。

const (
	maxTaskLog   = 4000 // 单个任务最多保留的日志行数
	maxTasks     = 400  // 最多保留的任务数
	taskSaveGap  = 2 * time.Second
	taskLineSize = 2000
)

type taskRun struct {
	s       *Server
	mu      sync.Mutex
	t       model.Task
	masks   []string
	saved   time.Time
	dropped int
	ctx     context.Context
	cancel  context.CancelFunc
}

// taskKinds 决定谁能看到任务：节点任务需要 nodes:view，部署需要 deploy:view，升级需要 upgrade:view。
func taskMenu(kind string) string {
	switch {
	case strings.HasPrefix(kind, "node."):
		return "nodes"
	case strings.HasPrefix(kind, "deploy"):
		return "deploy"
	case strings.HasPrefix(kind, "upgrade"):
		return "upgrade"
	case strings.HasPrefix(kind, "backup."):
		return "backup"
	}
	return ""
}

func (s *Server) loadTasks() {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	s.tasks = map[string]*taskRun{}
	for _, t := range s.Store.LoadTasks() {
		if len(s.tasks) >= maxTasks {
			s.Store.DeleteTask(t.ID)
			continue
		}
		if !t.Finished() && t.State != "paused" {
			t.State, t.End = "failed", time.Now()
			t.Err = "平台重启，任务被中断"
			t.Log = append(t.Log, "!! 平台重启，任务被中断，请检查目标主机状态后重试")
			s.Store.SaveTask(t)
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.tasks[t.ID] = &taskRun{s: s, t: t, ctx: ctx, cancel: cancel}
	}
}

// newTask 创建并登记一个运行中的任务。
func (s *Server) newTask(kind, cluster, title, by string, data map[string]string) *taskRun {
	b := make([]byte, 3)
	rand.Read(b)
	id := "T-" + time.Now().Format("20060102150405") + "-" + hex.EncodeToString(b)
	ctx, cancel := context.WithCancel(context.Background())
	t := &taskRun{s: s, ctx: ctx, cancel: cancel, t: model.Task{ID: id, Kind: kind, Cluster: cluster, Title: title, By: by, State: "running", Start: time.Now(), Data: data}}
	s.taskMu.Lock()
	if s.tasks == nil {
		s.tasks = map[string]*taskRun{}
	}
	s.tasks[id] = t
	if len(s.tasks) > maxTasks { // 淘汰最旧的已结束任务
		var oldest *taskRun
		for _, x := range s.tasks {
			x.mu.Lock()
			fin := x.t.Finished()
			x.mu.Unlock()
			if fin && (oldest == nil || x.t.Start.Before(oldest.t.Start)) {
				oldest = x
			}
		}
		if oldest != nil {
			delete(s.tasks, oldest.t.ID)
			s.Store.DeleteTask(oldest.t.ID)
		}
	}
	s.taskMu.Unlock()
	t.save(true)
	return t
}

func (s *Server) task(id string) *taskRun {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	return s.tasks[id]
}

func (t *taskRun) ID() string { return t.t.ID }

// Mask 登记需要在日志中隐藏的敏感值（密码、密钥）。
func (t *taskRun) Mask(secrets ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, x := range secrets {
		if len(x) >= 3 {
			t.masks = append(t.masks, x)
		}
	}
}

func (t *taskRun) clean(line string) string {
	for _, m := range t.masks {
		line = strings.ReplaceAll(line, m, "***")
	}
	if len(line) > taskLineSize {
		line = line[:taskLineSize] + " …"
	}
	return line
}

// Logf 追加一行日志（多行文本拆成多行，每行带时间）。
func (t *taskRun) Logf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	ts := time.Now().Format("15:04:05")
	t.mu.Lock()
	for _, ln := range strings.Split(strings.TrimRight(msg, "\r\n"), "\n") {
		t.t.Log = append(t.t.Log, ts+" "+t.clean(strings.TrimRight(ln, "\r")))
	}
	if over := len(t.t.Log) - maxTaskLog; over > 0 {
		t.t.Log = t.t.Log[over:]
		t.dropped += over
	}
	t.mu.Unlock()
	t.save(false)
}

func (t *taskRun) SetProg(p int) {
	t.mu.Lock()
	if p > 100 {
		p = 100
	}
	t.t.Prog = p
	t.mu.Unlock()
}

func (t *taskRun) SetState(state string) {
	t.mu.Lock()
	t.t.State = state
	t.mu.Unlock()
	t.save(true)
}

func (t *taskRun) SetData(k, v string) {
	t.mu.Lock()
	if t.t.Data == nil {
		t.t.Data = map[string]string{}
	}
	t.t.Data[k] = v
	t.mu.Unlock()
}

// Finish 结束任务：err 为 nil 表示成功；任务已被取消时状态为 canceled。
func (t *taskRun) Finish(err error) {
	t.mu.Lock()
	t.t.End = time.Now()
	switch {
	case t.ctx.Err() != nil:
		t.t.State, t.t.Err = "canceled", "已取消"
	case err != nil:
		t.t.State, t.t.Err = "failed", t.clean(err.Error())
		t.t.Log = append(t.t.Log, time.Now().Format("15:04:05")+" !! 失败："+t.clean(err.Error()))
	default:
		t.t.State, t.t.Prog = "done", 100
	}
	t.saveLocked()
	t.mu.Unlock()
}

func (t *taskRun) saveLocked() {
	t.saved = time.Now()
	if err := t.s.Store.SaveTask(t.t); err != nil {
		logf("保存任务 %s 失败：%v", t.t.ID, err)
	}
}

func (t *taskRun) save(force bool) {
	t.mu.Lock()
	if !force && time.Since(t.saved) < taskSaveGap {
		t.mu.Unlock()
		return
	}
	t.saved = time.Now()
	snap := t.t
	snap.Log = append([]string(nil), t.t.Log...)
	t.mu.Unlock()
	if err := t.s.Store.SaveTask(snap); err != nil {
		logf("保存任务 %s 失败：%v", snap.ID, err)
	}
}

func (t *taskRun) Cancelled() bool { return t.ctx.Err() != nil }

func (t *taskRun) snapshot(withLog bool, from int) (model.Task, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.t
	total := t.dropped + len(t.t.Log)
	if withLog {
		i := from - t.dropped
		if i < 0 {
			i = 0
		}
		if i > len(t.t.Log) {
			i = len(t.t.Log)
		}
		c.Log = append([]string(nil), t.t.Log[i:]...)
	} else {
		c.Log = nil
	}
	return c, total
}

func taskVisible(acc *perm.Access, t model.Task) bool {
	menu := taskMenu(t.Kind)
	if menu == "" || !acc.Can(menu+":view") {
		return false
	}
	return t.Cluster == "" || acc.Cluster(t.Cluster)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, u *model.User) {
	acc := accessOf(r)
	cluster, prefix := r.URL.Query().Get("cluster"), r.URL.Query().Get("kind")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	s.taskMu.Lock()
	all := make([]*taskRun, 0, len(s.tasks))
	for _, t := range s.tasks {
		all = append(all, t)
	}
	s.taskMu.Unlock()
	out := []model.Task{}
	for _, t := range all {
		c, _ := t.snapshot(false, 0)
		if !taskVisible(acc, c) || (cluster != "" && c.Cluster != cluster) || (prefix != "" && !strings.HasPrefix(c.Kind, prefix)) {
			continue
		}
		out = append(out, c)
	}
	sortTasks(out)
	if len(out) > limit {
		out = out[:limit]
	}
	ok(w, map[string]any{"items": out})
}

func sortTasks(ts []model.Task) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].Start.After(ts[j-1].Start); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

// getTask 返回任务详情；from 为已取得的日志行数，只返回其后的新增日志。
func (s *Server) getTask(w http.ResponseWriter, r *http.Request, u *model.User) {
	t := s.task(r.PathValue("tid"))
	if t == nil {
		fail(w, http.StatusNotFound, "任务不存在")
		return
	}
	from, _ := strconv.Atoi(r.URL.Query().Get("from"))
	c, total := t.snapshot(true, from)
	if !taskVisible(accessOf(r), c) {
		fail(w, http.StatusForbidden, "无权查看该任务")
		return
	}
	ok(w, map[string]any{"task": c, "total": total})
}
