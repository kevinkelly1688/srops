// @author: kevinkelly
// @license: Apache Licence
// @file: server.go
// @time: 2026/10/28

// Package api 提供平台 HTTP 接口，并托管前端静态文件。
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/collect"
	"starrocks-ops/internal/inspect"
	"starrocks-ops/internal/model"
	"starrocks-ops/internal/secure"
	"starrocks-ops/internal/sshx"
	"starrocks-ops/internal/store"
)

type Server struct {
	Store    *store.Store
	Mgr      *collect.Manager
	Runner   *inspect.Runner
	Keys     *sshx.Keys
	Box      *secure.Box
	WebDir   string
	Version  string
	ICP      string // ICP 备案号，来自配置文件 SR_ICP；空 = 页面不展示
	Sessions *Sessions

	wl         whitelist
	alertMu    sync.Mutex
	alertPend  map[string]time.Time // 告警键 → 开始异常的时间（持续满规则的“持续时间”才触发）
	alertFail  map[string]int       // 集群 ID → 连续采集失败次数
	customErr  map[string]customRun // 自定义规则 ID|集群 ID → 最近一次评估结果
	reviewable map[string]handler   // 权限码 → 可在批准复核后执行的处理函数
	pcHomeMu   sync.Mutex
	pcHome     map[string]string // 用户 → 最近一次预检的安装目录（一键修复请求没带目录时沿用）

	taskMu   sync.Mutex
	tasks    map[string]*taskRun // 后台任务
	nodeBusy map[string]bool     // 正在执行操作的节点
	upCtls   map[string]*upCtl   // 升级计划的暂停开关
}

// Handler 组装路由。
func (s *Server) Handler() http.Handler {
	s.reviewable = map[string]handler{}
	mux := http.NewServeMux()
	// 无需登录
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/login", s.login)
	// 登录后（允许 mustChange 状态）
	mux.HandleFunc("POST /api/logout", s.auth("", true, s.logout))
	mux.HandleFunc("GET /api/me", s.auth("", true, s.me))
	mux.HandleFunc("POST /api/password", s.auth("", true, s.changePassword))
	// 集群接入
	mux.HandleFunc("GET /api/clusters", s.auth("", false, s.listClusters))
	mux.HandleFunc("POST /api/clusters/test", s.auth("cluster.add|cluster.edit", false, s.testConn))
	mux.HandleFunc("POST /api/clusters/test-ssh", s.auth("cluster.add|cluster.edit", false, s.testSSH))
	mux.HandleFunc("POST /api/clusters", s.auth("cluster.add", false, s.createCluster))
	mux.HandleFunc("PUT /api/clusters/{id}", s.auth("cluster.edit", false, s.updateCluster))
	mux.HandleFunc("DELETE /api/clusters/{id}", s.auth("cluster.delete", false, s.review("cluster.delete", func(r *http.Request) (string, string) {
		return "移除集群 " + r.PathValue("id"), r.PathValue("id")
	}, s.deleteCluster)))
	mux.HandleFunc("GET /api/clusters/{id}/test", s.auth("cluster.test", false, s.checkCluster))
	mux.HandleFunc("GET /api/ssh/pubkey", s.auth("cluster.add|cluster.edit", false, s.pubkey))
	// 用户管理 / 角色权限
	mux.HandleFunc("GET /api/users", s.auth("users:view|roles:view", false, s.listUsers))
	mux.HandleFunc("POST /api/users", s.auth("user.create", false, s.createUser))
	mux.HandleFunc("PUT /api/users/{login}", s.auth("user.edit", false, s.updateUser))
	mux.HandleFunc("POST /api/users/{login}/reset", s.auth("user.reset", false, s.resetPassword))
	mux.HandleFunc("POST /api/users/{login}/lock", s.auth("user.lock", false, s.lockUser))
	mux.HandleFunc("DELETE /api/users/{login}", s.auth("user.delete", false, s.review("user.delete", func(r *http.Request) (string, string) {
		return "删除用户 " + r.PathValue("login"), ""
	}, s.deleteUser)))
	mux.HandleFunc("GET /api/roles", s.auth("roles:view|users:view|user.create|user.edit", false, s.listRoles))
	mux.HandleFunc("POST /api/roles", s.auth("role.create", false, s.createRole))
	mux.HandleFunc("PUT /api/roles/{role}", s.auth("role.edit", false, s.updateRole))
	mux.HandleFunc("DELETE /api/roles/{role}", s.auth("role.delete", false, s.review("role.delete", func(r *http.Request) (string, string) {
		name := r.PathValue("role")
		s.Store.View(func(st *store.State) {
			if ro, ok := findRole(st, name); ok {
				name = ro.Name
			}
		})
		return "删除角色 " + name, ""
	}, s.deleteRole)))
	// 平台设置
	mux.HandleFunc("GET /api/settings", s.auth("", false, s.getSettings))
	mux.HandleFunc("PUT /api/settings/security", s.auth("setting.security", false, s.putSecurity))
	mux.HandleFunc("PUT /api/settings/notify", s.auth("setting.notify", false, s.putNotify))
	mux.HandleFunc("POST /api/settings/notify/test-mail", s.auth("setting.notify", false, s.testMail))
	mux.HandleFunc("POST /api/settings/notify/test-sms", s.auth("setting.notify", false, s.testSMS))
	mux.HandleFunc("PUT /api/settings/basic", s.auth("setting.basic", false, s.putBasic))
	mux.HandleFunc("PUT /api/settings/watermark", s.auth("", false, s.putWatermark)) // 仅超级管理员（在处理函数内校验）
	// 操作审计 / 双人复核
	mux.HandleFunc("GET /api/audit", s.auth("audit:view", false, s.listAudit))
	mux.HandleFunc("GET /api/audit/export", s.auth("audit.export", false, s.exportAudit))
	mux.HandleFunc("GET /api/reviews", s.auth("audit:view", false, s.listReviews))
	mux.HandleFunc("POST /api/reviews/{rid}/approve", s.auth("", false, s.approveReview)) // 权限在处理函数内按申请的权限码校验
	mux.HandleFunc("POST /api/reviews/{rid}/reject", s.auth("", false, s.rejectReview))
	mux.HandleFunc("POST /api/reviews/{rid}/withdraw", s.auth("", false, s.withdrawReview))
	mux.HandleFunc("POST /api/roles/{role}/copy", s.auth("role.copy", false, s.copyRole))
	// 集群总览 / 架构
	mux.HandleFunc("GET /api/clusters/{id}/overview", s.auth("overview:view", false, s.overview))
	mux.HandleFunc("GET /api/clusters/{id}/topology", s.auth("topology:view", false, s.topology))
	// 性能巡检
	mux.HandleFunc("GET /api/inspect/rules", s.auth("inspect:view", false, s.listRules))
	mux.HandleFunc("PUT /api/inspect/rules/{rule}", s.auth("inspect.rule", false, s.updateRule))
	mux.HandleFunc("GET /api/clusters/{id}/inspections", s.auth("inspect:view", false, s.listReports))
	mux.HandleFunc("POST /api/clusters/{id}/inspections", s.auth("inspect.run", false, s.runInspection))
	mux.HandleFunc("GET /api/clusters/{id}/inspections/{rid}", s.auth("inspect:view", false, s.getReport))
	mux.HandleFunc("POST /api/clusters/{id}/inspect/ignore", s.auth("inspect.ignore", false, s.ignore))
	mux.HandleFunc("GET /api/clusters/{id}/inspect/schedule", s.auth("inspect:view", false, s.getSchedule))
	mux.HandleFunc("PUT /api/clusters/{id}/inspect/schedule", s.auth("inspect.schedule", false, s.putSchedule))
	mux.HandleFunc("POST /api/clusters/{id}/inspect/notify-test", s.auth("inspect.schedule", false, s.testNotify))
	// 告警中心
	mux.HandleFunc("GET /api/alerts", s.auth("alert:view", false, s.listAlerts))
	mux.HandleFunc("POST /api/alerts/{aid}/ack", s.auth("alert.ack", false, s.ackAlert))
	mux.HandleFunc("GET /api/alert-rules", s.auth("alert:view", false, s.listAlertRules))
	mux.HandleFunc("PUT /api/alert-rules/{rule}", s.auth("alert.rule", false, s.updateAlertRule))
	mux.HandleFunc("GET /api/alert-rules/custom", s.auth("alert:view", false, s.listCustomRules))
	mux.HandleFunc("POST /api/alert-rules/custom", s.auth("alert.rule", false, s.createCustomRule))
	mux.HandleFunc("POST /api/alert-rules/custom/test", s.auth("alert.rule", false, s.testCustomRule))
	mux.HandleFunc("PUT /api/alert-rules/custom/{rid}", s.auth("alert.rule", false, s.updateCustomRule))
	mux.HandleFunc("DELETE /api/alert-rules/custom/{rid}", s.auth("alert.rule", false, s.deleteCustomRule))
	mux.HandleFunc("GET /api/silences", s.auth("alert:view", false, s.listSilences))
	mux.HandleFunc("POST /api/silences", s.auth("alert.silence", false, s.createSilence))
	mux.HandleFunc("DELETE /api/silences/{sid}", s.auth("alert.silence", false, s.deleteSilence))
	mux.HandleFunc("GET /api/alert-channels", s.auth("alert:view", false, s.listChannels))
	mux.HandleFunc("POST /api/alert-channels", s.auth("alert.channel", false, s.createChannel))
	mux.HandleFunc("PUT /api/alert-channels/{cid}", s.auth("alert.channel", false, s.updateChannel))
	mux.HandleFunc("DELETE /api/alert-channels/{cid}", s.auth("alert.channel", false, s.deleteChannel))
	mux.HandleFunc("POST /api/alert-channels/{cid}/test", s.auth("alert.channel", false, s.testChannel))
	// 备份恢复
	mux.HandleFunc("GET /api/clusters/{id}/backup", s.auth("backup:view", false, s.backupOverview))
	mux.HandleFunc("GET /api/clusters/{id}/backup/repos/{repo}/snapshots", s.auth("backup:view", false, s.repoSnapshots))
	mux.HandleFunc("POST /api/clusters/{id}/backup/repos", s.auth("backup.repo", false, s.createRepo))
	mux.HandleFunc("DELETE /api/clusters/{id}/backup/repos/{repo}", s.auth("backup.repo", false, s.dropRepo))
	mux.HandleFunc("POST /api/clusters/{id}/backup/jobs", s.auth("backup.create", false, s.startBackup))
	mux.HandleFunc("POST /api/clusters/{id}/backup/restore", s.auth("backup.restore", false, s.review("backup.restore", func(r *http.Request) (string, string) {
		var in struct {
			DB       string `json:"db"`
			Snapshot string `json:"snapshot"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		return "恢复数据 " + in.DB + "." + in.Snapshot, r.PathValue("id")
	}, s.startRestore)))
	mux.HandleFunc("GET /api/clusters/{id}/backup/repos/{repo}/cleanup", s.auth("backup:view", false, s.cleanupInfo))
	mux.HandleFunc("PUT /api/clusters/{id}/backup/repos/{repo}/cleanup", s.auth("backup.clean", false, s.putCleanCred))
	mux.HandleFunc("DELETE /api/clusters/{id}/backup/repos/{repo}/cleanup", s.auth("backup.clean", false, s.deleteCleanCred))
	mux.HandleFunc("POST /api/clusters/{id}/backup/repos/{repo}/cleanup", s.auth("backup.clean", false, s.review("backup.clean", func(r *http.Request) (string, string) {
		var in struct {
			Snapshots []string `json:"snapshots"`
			Policy    string   `json:"policy"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Policy != "" {
			return "按保留策略清理仓库 " + r.PathValue("repo") + " 的备份快照", r.PathValue("id")
		}
		return "删除仓库 " + r.PathValue("repo") + " 的备份快照 " + strings.Join(in.Snapshots, "、"), r.PathValue("id")
	}, s.runClean)))
	mux.HandleFunc("GET /api/clusters/{id}/backup/policies/{pid}/clean-preview", s.auth("backup:view", false, s.cleanPreview))
	mux.HandleFunc("POST /api/clusters/{id}/backup/cancel", s.auth("backup.cancel", false, s.cancelJob))
	mux.HandleFunc("POST /api/clusters/{id}/backup/policies", s.auth("backup.policy", false, s.createPolicy))
	mux.HandleFunc("PUT /api/clusters/{id}/backup/policies/{pid}", s.auth("backup.policy", false, s.updatePolicy))
	mux.HandleFunc("DELETE /api/clusters/{id}/backup/policies/{pid}", s.auth("backup.policy", false, s.deletePolicy))
	// 参数配置
	mux.HandleFunc("GET /api/clusters/{id}/params/history", s.auth("param.history", false, s.paramHistory))
	mux.HandleFunc("GET /api/clusters/{id}/params/{kind}", s.auth("param:view", false, s.listParams))
	mux.HandleFunc("GET /api/clusters/{id}/params/{kind}/compare", s.auth("param.compare", false, s.compareParams))
	mux.HandleFunc("PUT /api/clusters/{id}/params/{kind}/{key}", s.auth("param.edit", false, s.review("param.edit", func(r *http.Request) (string, string) {
		var in struct {
			Value string `json:"value"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		return "修改参数 " + r.PathValue("key") + " = " + in.Value, r.PathValue("id")
	}, s.editParam)))
	// 查询分析
	mux.HandleFunc("GET /api/clusters/{id}/queries", s.auth("query:view", false, s.listQueries))
	mux.HandleFunc("POST /api/clusters/{id}/queries/kill", s.auth("query.kill", false, s.review("query.kill", func(r *http.Request) (string, string) {
		var in struct {
			QueryID string `json:"queryId"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		return "终止查询 " + in.QueryID, r.PathValue("id")
	}, s.killQuery)))
	mux.HandleFunc("GET /api/clusters/{id}/queries/{qid}/profile", s.auth("query.profile", false, s.queryProfile))
	mux.HandleFunc("POST /api/clusters/{id}/explain", s.auth("query.explain", false, s.explainQuery))
	mux.HandleFunc("GET /api/clusters/{id}/slow-queries", s.auth("query:view", false, s.slowQueries))
	mux.HandleFunc("GET /api/clusters/{id}/slow-queries/export", s.auth("query.export", false, s.exportSlowQueries))
	mux.HandleFunc("GET /api/clusters/{id}/sql-audit", s.auth("audit.sql", false, s.sqlAudit))
	mux.HandleFunc("GET /api/clusters/{id}/blacklist", s.auth("query:view", false, s.listBlacklist))
	blDesc := func(r *http.Request) (string, string) {
		if r.Method == http.MethodDelete {
			return "删除 SQL 黑名单规则 #" + r.PathValue("idx"), r.PathValue("id")
		}
		var in struct {
			Pattern string `json:"pattern"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		return "添加 SQL 黑名单规则 " + in.Pattern, r.PathValue("id")
	}
	mux.HandleFunc("POST /api/clusters/{id}/blacklist", s.auth("query.blacklist", false, s.review("query.blacklist", blDesc, s.changeBlacklist)))
	mux.HandleFunc("DELETE /api/clusters/{id}/blacklist/{idx}", s.auth("query.blacklist", false, s.review("query.blacklist", blDesc, s.changeBlacklist)))
	// 资源隔离
	mux.HandleFunc("GET /api/clusters/{id}/resource-groups", s.auth("resource:view", false, s.listRG))
	mux.HandleFunc("POST /api/clusters/{id}/resource-groups", s.auth("rg.create", false, s.createRG))
	mux.HandleFunc("PUT /api/clusters/{id}/resource-groups/{name}", s.auth("rg.edit", false, s.updateRG))
	mux.HandleFunc("DELETE /api/clusters/{id}/resource-groups/{name}", s.auth("rg.delete", false, s.review("rg.delete", func(r *http.Request) (string, string) {
		return "删除资源组 " + r.PathValue("name"), r.PathValue("id")
	}, s.deleteRG)))
	mux.HandleFunc("GET /api/clusters/{id}/query-queue", s.auth("resource:view", false, s.getQueue))
	mux.HandleFunc("PUT /api/clusters/{id}/query-queue", s.auth("queue.edit", false, s.putQueue))
	// 导入作业
	mux.HandleFunc("GET /api/clusters/{id}/loads", s.auth("load:view", false, s.listLoads))
	mux.HandleFunc("GET /api/clusters/{id}/routine-loads/{db}/{name}", s.auth("load.detail", false, s.loadDetail))
	mux.HandleFunc("POST /api/clusters/{id}/routine-loads/{db}/{name}/pause", s.auth("load.pause", false, s.loadOp("PAUSE", "load.pause", "暂停")))
	mux.HandleFunc("POST /api/clusters/{id}/routine-loads/{db}/{name}/resume", s.auth("load.resume", false, s.loadOp("RESUME", "load.resume", "恢复")))
	mux.HandleFunc("DELETE /api/clusters/{id}/routine-loads/{db}/{name}", s.auth("load.stop", false, s.review("load.stop", func(r *http.Request) (string, string) {
		return "停止 Routine Load " + r.PathValue("db") + "." + r.PathValue("name"), r.PathValue("id")
	}, s.loadOp("STOP", "load.stop", "停止"))))
	// 物化视图
	mux.HandleFunc("GET /api/clusters/{id}/materialized-views", s.auth("mv:view", false, s.listMVs))
	mux.HandleFunc("POST /api/clusters/{id}/materialized-views/{db}/{name}/refresh", s.auth("mv.refresh", false, s.mvOp("refresh", "mv.refresh", "刷新")))
	mux.HandleFunc("POST /api/clusters/{id}/materialized-views/{db}/{name}/active", s.auth("mv.toggle", false, s.mvOp("toggle", "mv.toggle", "")))
	mux.HandleFunc("POST /api/clusters/{id}/materialized-views/{db}/{name}/cancel", s.auth("mv.cancel", false, s.mvOp("cancel", "mv.cancel", "取消刷新")))
	// 后台任务
	mux.HandleFunc("GET /api/tasks", s.auth("nodes:view|deploy:view|upgrade:view", false, s.listTasks))
	mux.HandleFunc("GET /api/tasks/{tid}", s.auth("nodes:view|deploy:view|upgrade:view", false, s.getTask))
	// 节点管理
	nodeDesc := func(verb string) describe {
		return func(r *http.Request) (string, string) {
			return verb + " " + strings.ToUpper(r.PathValue("kind")) + " " + r.PathValue("node"), r.PathValue("id")
		}
	}
	mux.HandleFunc("POST /api/clusters/{id}/nodes/{kind}/{node}/start", s.auth("node.start", false, s.nodeOp("start")))
	mux.HandleFunc("POST /api/clusters/{id}/nodes/{kind}/{node}/stop", s.auth("node.stop", false, s.review("node.stop", nodeDesc("停止"), s.nodeOp("stop"))))
	mux.HandleFunc("POST /api/clusters/{id}/nodes/{kind}/{node}/restart", s.auth("node.restart", false, s.review("node.restart", nodeDesc("重启"), s.nodeOp("restart"))))
	mux.HandleFunc("POST /api/clusters/{id}/nodes/{kind}/{node}/decommission", s.auth("node.decommission", false, s.review("node.decommission", nodeDesc("下线"), s.nodeSQL("decommission"))))
	mux.HandleFunc("POST /api/clusters/{id}/nodes/{kind}/{node}/drop", s.auth("node.drop", false, s.review("node.drop", nodeDesc("移除"), s.nodeSQL("drop"))))
	mux.HandleFunc("GET /api/clusters/{id}/nodes/{kind}/{node}/log", s.auth("node.log", false, s.nodeLog))
	mux.HandleFunc("GET /api/clusters/{id}/node-blacklist", s.auth("nodes:view", false, s.listNodeBlacklist))
	mux.HandleFunc("POST /api/clusters/{id}/node-blacklist", s.auth("node.blacklist", false, s.addNodeBlacklist))
	mux.HandleFunc("DELETE /api/clusters/{id}/node-blacklist/{kind}/{bid}", s.auth("node.blacklist", false, s.removeNodeBlacklist))
	// 安装部署：主机、安装包、环境预检 / 修复、部署任务
	mux.HandleFunc("GET /api/hosts", s.auth("deploy:view", false, s.listHosts))
	mux.HandleFunc("POST /api/hosts", s.auth("deploy.host", false, s.addHosts))
	mux.HandleFunc("DELETE /api/hosts/{ip}", s.auth("deploy.host", false, s.deleteHost))
	mux.HandleFunc("POST /api/hosts/{ip}/check", s.auth("deploy.host", false, s.recheckHost))
	mux.HandleFunc("GET /api/packages", s.auth("deploy:view", false, s.listPackages))
	mux.HandleFunc("POST /api/packages", s.auth("deploy.pkg", false, s.uploadPackage))
	mux.HandleFunc("GET /api/packages/import", s.auth("deploy.pkg", false, s.listImportable))
	mux.HandleFunc("POST /api/packages/import", s.auth("deploy.pkg", false, s.importPackage))
	mux.HandleFunc("DELETE /api/packages/{name}", s.auth("deploy.pkg", false, s.deletePackage))
	mux.HandleFunc("POST /api/deploy/precheck", s.auth("deploy.precheck", false, s.precheck))
	mux.HandleFunc("POST /api/deploy/fix", s.auth("deploy.fix", false, s.review("deploy.fix", func(r *http.Request) (string, string) {
		var in fixIn
		json.NewDecoder(r.Body).Decode(&in)
		return fmt.Sprintf("一键修复环境项（%d 台主机：%s）", len(in.Hosts), strings.Join(in.Items, "、")), ""
	}, s.fixEnv)))
	deployDesc := func(r *http.Request) (string, string) {
		if tid := r.PathValue("tid"); tid != "" {
			return "重试部署任务 " + tid, ""
		}
		var in deploySpec
		json.NewDecoder(r.Body).Decode(&in)
		return "部署集群 " + in.Name, ""
	}
	mux.HandleFunc("POST /api/deploy/tasks", s.auth("deploy.execute", false, s.review("deploy.execute", deployDesc, s.deployExec)))
	mux.HandleFunc("POST /api/tasks/{tid}/retry", s.auth("deploy.execute", false, s.review("deploy.execute", deployDesc, s.deployExec)))
	mux.HandleFunc("POST /api/tasks/{tid}/cancel", s.auth("", false, s.cancelTask)) // 权限在处理函数内按任务类型校验
	mux.HandleFunc("GET /api/clusters/{id}/nodes/scaleout/plan", s.auth("node.scaleout", false, s.scalePlanHandler))
	mux.HandleFunc("POST /api/clusters/{id}/nodes/scaleout", s.auth("node.scaleout", false, s.scaleOut))
	// 升级回滚
	mux.HandleFunc("GET /api/clusters/{id}/upgrade", s.auth("upgrade:view", false, s.upgradeOverview))
	mux.HandleFunc("POST /api/clusters/{id}/upgrade/plans", s.auth("upgrade.create", false, s.createPlan))
	mux.HandleFunc("DELETE /api/clusters/{id}/upgrade/plans/{pid}", s.auth("upgrade.create", false, s.deletePlan))
	mux.HandleFunc("POST /api/clusters/{id}/upgrade/plans/{pid}/precheck", s.auth("upgrade:view", false, s.planPrecheck))
	mux.HandleFunc("POST /api/clusters/{id}/upgrade/plans/{pid}/execute", s.auth("upgrade.execute", false, s.review("upgrade.execute", func(r *http.Request) (string, string) { return s.planTitle(r, "滚动升级") }, s.planRun(false))))
	mux.HandleFunc("POST /api/clusters/{id}/upgrade/plans/{pid}/pause", s.auth("upgrade.pause", false, s.planPause))
	mux.HandleFunc("POST /api/clusters/{id}/upgrade/plans/{pid}/rollback", s.auth("upgrade.rollback", false, s.review("upgrade.rollback", func(r *http.Request) (string, string) { return s.planTitle(r, "回滚") }, s.planRun(true))))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "接口不存在") })
	// 前端
	mux.HandleFunc("/", s.static)
	return s.ipGuard(secureHeaders(mux))
}

func secureHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

// static 只放行前端所需文件，避免泄露数据目录、源码等。
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/" {
		p = "/index.html"
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	allowed := clean == "/index.html" || clean == "/docs/用户使用手册.md" ||
		(strings.HasPrefix(clean, "/css/") && strings.HasSuffix(clean, ".css")) ||
		(strings.HasPrefix(clean, "/js/") && strings.HasSuffix(clean, ".js"))
	if !allowed || strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.WebDir, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	if fi, err := os.Stat(full); err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	if clean == "/index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFile(w, r, full)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{"ok": true, "version": s.Version, "mode": "live", "icp": s.ICP})
}

// ---------- 工具 ----------

func ok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误："+err.Error())
		return false
	}
	return true
}

func (s *Server) audit(r *http.Request, u *model.User, cluster, code, act string, err error) {
	s.auditSQL(r, u, code, act, "", err, cluster)
}

// auditSQL 同 audit，并记录变更明细；cluster 可选。
func (s *Server) auditSQL(r *http.Request, u *model.User, code, act, detail string, err error, cluster ...string) {
	e := store.AuditEntry{IP: clientIP(r), Code: code, Act: act, Result: "成功", SQL: detail}
	if err == errDenied {
		e.Result = "拒绝（无权限）"
	}
	if len(cluster) > 0 {
		e.Cluster = cluster[0]
	}
	if u != nil {
		e.User = u.Login
	}
	if err != nil && err != errDenied {
		e.Result = "失败：" + err.Error()
	}
	s.Store.Audit(e)
}

// auditDone 在请求结束之后（后台任务完成时）写审计，res 为最终结果文字。
func (s *Server) auditDone(ip string, u *model.User, cluster, code, act, res string) {
	s.Store.Audit(store.AuditEntry{User: u.Login, IP: ip, Cluster: cluster, Code: code, Act: act, Result: res})
}

// cluster 读取路径中的集群；不存在时写 404。
func (s *Server) cluster(w http.ResponseWriter, r *http.Request) (model.Cluster, bool) {
	c, err := s.Store.Cluster(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "集群不存在")
		return c, false
	}
	return c, err == nil
}

func logf(format string, a ...any) { log.Printf(format, a...) }
