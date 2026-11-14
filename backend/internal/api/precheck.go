// @author: kevinkelly
// @license: Apache Licence
// @file: precheck.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/sshx"
)

// 环境预检（只读）与一键修复（需要 root 或免密 sudo）。检查项依据官方「部署前提条件」与「检查环境配置」。

type checkHost struct {
	IP    string   `json:"ip"`
	Roles []string `json:"roles"` // FE / BE / CN
}

type checkSpec struct {
	Hosts    []checkHost `json:"hosts"`
	Home     string      `json:"home"`     // 安装根目录（检查是否可写、剩余空间）
	Version  string      `json:"version"`  // 目标 StarRocks 版本，决定需要的 JDK
	Arch     string      `json:"arch"`     // 安装包架构，空表示不检查
	JavaHome string      `json:"javaHome"` // 指定的 JDK 目录；空表示使用系统 java
}

type checkItem struct {
	ID      string `json:"id"`
	Item    string `json:"item"`
	Cmd     string `json:"cmd"`
	Expect  string `json:"expect"`
	Fix     string `json:"fix"`
	Fixable bool   `json:"fixable"`
}

type checkCell struct {
	St  string `json:"st"` // ok / warn / bad / skip
	Val string `json:"val"`
}

var rolePorts = map[string][]int{"FE": {8030, 9020, 9030, 9010}, "BE": {8040, 9050, 9060, 8060}, "CN": {8040, 9050, 9060, 8060}}

func ancestorCmd(dir, tail string) string {
	return fmt.Sprintf(`d=%s; while [ ! -e "$d" ]; do d=$(dirname "$d"); done; %s`, dir, tail)
}

func checkItems(sp checkSpec) []checkItem {
	javaCmd := "java -version 2>&1 | head -1"
	javaExpect := fmt.Sprintf("JDK ≥ %d", requiredJava(sp.Version))
	if sp.JavaHome != "" {
		javaCmd, javaExpect = fmt.Sprintf("test -x %s/bin/java && echo ok", sp.JavaHome), sp.JavaHome+"/bin/java 存在"
	}
	return []checkItem{
		{ID: "arch", Item: "CPU 架构与安装包一致", Cmd: "uname -m", Expect: "x86_64 ↔ amd64，aarch64 ↔ arm64"},
		{ID: "avx2", Item: "CPU 支持 AVX2（amd64）", Cmd: "grep -m1 -c avx2 /proc/cpuinfo", Expect: "支持（官方安装包要求）"},
		{ID: "swap", Item: "关闭 Swap", Cmd: "awk 'NR>1' /proc/swaps | wc -l", Expect: "0 个 swap 设备", Fix: "swapoff -a；注释 /etc/fstab 中的 swap 行", Fixable: true},
		{ID: "swappiness", Item: "vm.swappiness", Cmd: "cat /proc/sys/vm/swappiness", Expect: "0", Fix: "sysctl vm.swappiness=0（写入 /etc/sysctl.d）", Fixable: true},
		{ID: "thp", Item: "透明大页 THP", Cmd: "cat /sys/kernel/mm/transparent_hugepage/enabled", Expect: "madvise 或 never", Fix: "echo madvise > …/transparent_hugepage/enabled（并写入开机配置）", Fixable: true},
		{ID: "overcommit", Item: "vm.overcommit_memory", Cmd: "cat /proc/sys/vm/overcommit_memory", Expect: "1", Fix: "sysctl vm.overcommit_memory=1", Fixable: true},
		{ID: "max_map_count", Item: "vm.max_map_count", Cmd: "cat /proc/sys/vm/max_map_count", Expect: "≥ 262144", Fix: "sysctl vm.max_map_count=262144", Fixable: true},
		{ID: "nofile", Item: "打开文件数 ulimit -n", Cmd: "ulimit -n", Expect: "≥ 655350（至少 65535）", Fix: "写入 /etc/security/limits.d（新会话生效）", Fixable: true},
		{ID: "nproc", Item: "进程数 ulimit -u", Cmd: "ulimit -u", Expect: "≥ 65535", Fix: "写入 /etc/security/limits.d（新会话生效）", Fixable: true},
		{ID: "time", Item: "与平台的时钟偏差", Cmd: "date +%s%3N", Expect: "FE 之间 < 5 秒，建议启用 chrony / ntpd"},
		{ID: "ports", Item: "所需端口未被占用", Cmd: "ss -ltnH | awk '{print $4}'", Expect: "FE：8030 9020 9030 9010；BE / CN：8040 9050 9060 8060"},
		{ID: "dir", Item: "安装目录可写", Cmd: ancestorCmd(sp.Home, `test -w "$d" && echo ok`), Expect: sp.Home + " 或其上级目录可写", Fix: "sudo 创建 " + sp.Home + " 并把属主改为 SSH 用户（只改这个目录本身）", Fixable: checkFixDir(strings.TrimRight(sp.Home, "/")) == nil},
		{ID: "disk", Item: "安装目录剩余空间", Cmd: ancestorCmd(sp.Home, `df -P -BG "$d" | awk 'NR==2{print $4}'`), Expect: "≥ 20 GB"},
		{ID: "java", Item: "JDK", Cmd: javaCmd, Expect: javaExpect, Fix: "安装平台上传的 JDK 包到 " + orStr(sp.JavaHome, "<JAVA_HOME>"), Fixable: sp.JavaHome != ""},
		{ID: "sudo", Item: "免密 sudo（一键修复需要）", Cmd: "sudo -n true", Expect: "可用，或使用 root 账号"},
	}
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var javaVerRe = regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`)

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

// evalHost 在一台主机上执行全部检查项。
func (s *Server) evalHost(ctx context.Context, h model.Host, sp checkSpec, roles []string, items []checkItem) map[string]checkCell {
	out := map[string]checkCell{}
	sess, err := s.Keys.Dial(ctx, hostTarget(h))
	if err != nil {
		for _, it := range items {
			out[it.ID] = checkCell{"bad", "无法连接：" + err.Error()}
		}
		return out
	}
	defer sess.Close()
	run := func(cmd string) (string, int) {
		res, err := sess.Run(ctx, cmd)
		if err != nil {
			return err.Error(), -1
		}
		return strings.TrimSpace(res.Stdout), res.Code
	}
	for _, it := range items {
		var c checkCell
		o, code := run(it.Cmd)
		if code < 0 {
			out[it.ID] = checkCell{"bad", "执行失败：" + o}
			continue
		}
		switch it.ID {
		case "arch":
			if sp.Arch == "" {
				c = checkCell{"skip", normArch(o)}
			} else if normArch(o) == sp.Arch {
				c = checkCell{"ok", o}
			} else {
				c = checkCell{"bad", fmt.Sprintf("主机 %s，安装包 %s", o, sp.Arch)}
			}
		case "avx2":
			switch {
			case sp.Arch != "" && sp.Arch != "amd64":
				c = checkCell{"skip", "非 amd64"}
			case strings.HasPrefix(o, "0") || o == "":
				c = checkCell{"bad", "不支持 AVX2"}
			default:
				c = checkCell{"ok", "支持"}
			}
		case "swap":
			if n := atoiOr(o, -1); n == 0 {
				c = checkCell{"ok", "已关闭"}
			} else {
				c = checkCell{"warn", fmt.Sprintf("%s 个 swap 设备", o)}
			}
		case "swappiness":
			c = pickCell(atoiOr(o, -1) == 0, o, "warn")
		case "thp":
			c = pickCell(!strings.Contains(o, "[always]"), o, "warn")
		case "overcommit":
			c = pickCell(o == "1", o, "warn")
		case "max_map_count":
			c = pickCell(atoiOr(o, 0) >= 262144, o, "bad")
		case "nofile":
			switch n := atoiOr(o, 0); {
			case n >= 655350:
				c = checkCell{"ok", o}
			case n >= 65535:
				c = checkCell{"warn", o + "（建议 655350）"}
			default:
				c = checkCell{"bad", o}
			}
		case "nproc":
			c = pickCell(atoiOr(o, 0) >= 65535, o, "warn")
		case "time":
			c = checkCell{"skip", "—"}
		case "ports":
			used := usedPorts(o)
			var hit []string
			seen := map[int]bool{}
			for _, r := range roles {
				for _, p := range rolePorts[r] {
					if used[p] && !seen[p] {
						seen[p] = true
						hit = append(hit, strconv.Itoa(p))
					}
				}
			}
			if code != 0 && o == "" {
				c = checkCell{"warn", "无法执行 ss，未检查"}
			} else if len(hit) > 0 {
				c = checkCell{"bad", "已被占用：" + strings.Join(hit, "、")}
			} else {
				c = checkCell{"ok", "未占用"}
			}
		case "dir":
			c = pickCell(o == "ok", map[bool]string{true: "可写", false: "不可写"}[o == "ok"], "bad")
		case "disk":
			g := atoiOr(strings.TrimSuffix(o, "G"), -1)
			switch {
			case g >= 20:
				c = checkCell{"ok", o}
			case g >= 5:
				c = checkCell{"warn", o}
			default:
				c = checkCell{"bad", orStr(o, "未知")}
			}
		case "java":
			failSt := "warn" // 只有 FE 必须有 JDK；BE / CN 只在 Java UDF、外部表等功能下需要
			if contains(roles, "FE") {
				failSt = "bad"
			}
			if sp.JavaHome != "" {
				c = pickCell(o == "ok", map[bool]string{true: sp.JavaHome, false: "未安装：" + sp.JavaHome}[o == "ok"], failSt)
			} else if m := javaVerRe.FindStringSubmatch(o); m != nil {
				major := atoiOr(m[1], 0)
				if major == 1 {
					major = atoiOr(m[2], 0)
				}
				if major >= requiredJava(sp.Version) {
					c = checkCell{"ok", o}
				} else {
					c = checkCell{failSt, fmt.Sprintf("JDK %d，需要 ≥ %d", major, requiredJava(sp.Version))}
				}
			} else {
				c = checkCell{failSt, "未安装 java"}
			}
		case "sudo":
			if idOut, _ := run("id -u"); idOut == "0" {
				c = checkCell{"ok", "root 账号"}
			} else if code == 0 {
				c = checkCell{"ok", "可用"}
			} else {
				c = checkCell{"warn", "不可用"}
			}
		}
		out[it.ID] = c
	}
	// 时钟偏差：取一次往返的中点
	t0 := time.Now()
	if o, code := run("date +%s%3N"); code == 0 {
		t1 := time.Now()
		if ms, err := strconv.ParseInt(o, 10, 64); err == nil {
			off := ms - (t0.UnixMilli() + t1.Sub(t0).Milliseconds()/2)
			if off < 0 {
				off = -off
			}
			st := "ok"
			if off > 5000 {
				st = "bad"
			} else if off > 1000 {
				st = "warn"
			}
			out["time"] = checkCell{st, fmt.Sprintf("偏差 %d ms", off)}
		}
	}
	return out
}

func pickCell(okCond bool, val, failSt string) checkCell {
	if okCond {
		return checkCell{"ok", val}
	}
	return checkCell{failSt, val}
}

func usedPorts(ss string) map[int]bool {
	m := map[int]bool{}
	for _, ln := range strings.Split(ss, "\n") {
		ln = strings.TrimSpace(ln)
		if i := strings.LastIndex(ln, ":"); i >= 0 {
			if p, err := strconv.Atoi(ln[i+1:]); err == nil {
				m[p] = true
			}
		}
	}
	return m
}

func (s *Server) validateCheckSpec(sp *checkSpec) error {
	if len(sp.Hosts) == 0 || len(sp.Hosts) > 64 {
		return bad("请选择 1–64 台主机")
	}
	if !dirRe.MatchString(sp.Home) || strings.Contains(sp.Home, "..") {
		return bad("安装目录须为绝对路径，只能包含字母、数字、. _ - /")
	}
	if sp.JavaHome != "" && (!dirRe.MatchString(sp.JavaHome) || strings.Contains(sp.JavaHome, "..")) {
		return bad("JAVA_HOME 路径不合法")
	}
	if sp.Arch != "" && sp.Arch != "amd64" && sp.Arch != "arm64" {
		return bad("架构不合法")
	}
	for _, h := range sp.Hosts {
		for _, r := range h.Roles {
			if _, ok := rolePorts[r]; !ok {
				return bad("角色不合法：" + r)
			}
		}
	}
	return nil
}

func (s *Server) precheck(w http.ResponseWriter, r *http.Request, u *model.User) {
	var sp checkSpec
	if !decode(w, r, &sp) {
		return
	}
	if err := s.validateCheckSpec(&sp); err != nil {
		writeErr(w, err)
		return
	}
	s.rememberPrecheckHome(u.Login, sp.Home)
	items := checkItems(sp)
	res := make([]map[string]checkCell, len(sp.Hosts))
	var wg sync.WaitGroup
	for i, ch := range sp.Hosts {
		h, found := s.hostByIP(ch.IP)
		if !found {
			res[i] = map[string]checkCell{}
			for _, it := range items {
				res[i][it.ID] = checkCell{"bad", "主机未纳管，请先在「主机管理」添加"}
			}
			continue
		}
		wg.Add(1)
		go func(i int, h model.Host, roles []string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
			defer cancel()
			res[i] = s.evalHost(ctx, h, sp, roles, items)
		}(i, h, ch.Roles)
	}
	wg.Wait()
	results := map[string]map[string]checkCell{}
	nbad, nwarn := 0, 0
	for _, it := range items {
		results[it.ID] = map[string]checkCell{}
		for i, ch := range sp.Hosts {
			c := res[i][it.ID]
			results[it.ID][ch.IP] = c
			switch c.St {
			case "bad":
				nbad++
			case "warn":
				nwarn++
			}
		}
	}
	ips := make([]string, len(sp.Hosts))
	for i, h := range sp.Hosts {
		ips[i] = h.IP
	}
	s.auditSQL(r, u, "deploy.precheck", fmt.Sprintf("环境预检 %d 台主机：不通过 %d 项，警告 %d 项", len(sp.Hosts), nbad, nwarn), "", nil)
	ok(w, map[string]any{"items": items, "hosts": ips, "results": results, "bad": nbad, "warn": nwarn})
}

// ---------- 一键修复 ----------

type fixIn struct {
	Hosts      []string `json:"hosts"`
	Items      []string `json:"items"`
	JavaHome   string   `json:"javaHome"`
	JDKPackage string   `json:"jdkPackage"`
	Home       string   `json:"home"` // 安装根目录（修复「安装目录可写」时必填）
}

var sysctlKeys = map[string]string{"swappiness": "vm.swappiness = 0", "overcommit": "vm.overcommit_memory = 1", "max_map_count": "vm.max_map_count = 262144"}

func (s *Server) fixEnv(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in fixIn
	if !decode(w, r, &in) {
		return
	}
	allowed := map[string]bool{"swap": true, "swappiness": true, "thp": true, "overcommit": true, "max_map_count": true, "nofile": true, "nproc": true, "java": true, "dir": true}
	var items []string
	for _, id := range in.Items {
		if !allowed[id] {
			fail(w, http.StatusBadRequest, "不支持修复的检查项："+id)
			return
		}
		items = append(items, id)
	}
	if len(in.Hosts) == 0 || len(in.Hosts) > 64 || len(items) == 0 {
		fail(w, http.StatusBadRequest, "请选择主机与要修复的检查项")
		return
	}
	var hosts []model.Host
	for _, ip := range in.Hosts {
		h, found := s.hostByIP(ip)
		if !found {
			fail(w, http.StatusBadRequest, "主机未纳管："+ip)
			return
		}
		hosts = append(hosts, h)
	}
	home := strings.TrimRight(in.Home, "/")
	if contains(items, "dir") {
		// 页面脚本没有随请求带上部署目录（例如升级后浏览器仍在用旧版脚本）时，沿用该用户最近一次预检的安装目录；
		// 复核通过后重放的请求不沿用，避免执行到与申请时不同的目录。
		if home == "" && !isReviewed(r) {
			home = s.precheckHome(u.Login)
		}
		if home == "" {
			fail(w, http.StatusBadRequest, "没有收到部署目录：请重新执行预检后再修复；如果仍然提示，说明页面脚本是旧版本，请按 Ctrl+F5 强制刷新页面（并确认平台的 web 目录已一起升级）")
			return
		}
		if err := checkFixDir(home); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var jdk model.Package
	if contains(items, "java") {
		p, found := s.packageByName(in.JDKPackage)
		if !found || p.Kind != "jdk" || !dirRe.MatchString(in.JavaHome) || strings.Contains(in.JavaHome, "..") {
			fail(w, http.StatusBadRequest, "安装 JDK 需要选择已上传的 JDK 包并填写合法的 JAVA_HOME 目录")
			return
		}
		jdk = p
		for _, h := range hosts {
			if h.Arch != "" && h.Arch != p.Arch {
				fail(w, http.StatusBadRequest, fmt.Sprintf("JDK 包架构 %s 与主机 %s（%s）不一致", p.Arch, h.IP, h.Arch))
				return
			}
		}
	}
	t := s.newTask("deploy.fix", "", fmt.Sprintf("一键修复环境项（%d 台主机 · %s）", len(hosts), strings.Join(items, "、")), u.Login, nil)
	s.auditSQL(r, u, "deploy.fix", fmt.Sprintf("一键修复环境项 %s：%s（任务 %s）", strings.Join(items, "、"), strings.Join(in.Hosts, "、"), t.ID()), "", nil)
	ip := clientIP(r)
	go func() {
		var errs []string
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, h := range hosts {
			wg.Add(1)
			go func(h model.Host) {
				defer wg.Done()
				if err := s.fixHost(t, h, items, in.JavaHome, home, jdk); err != nil {
					t.Logf("[%s] 失败：%v", h.IP, err)
					mu.Lock()
					errs = append(errs, h.IP+"："+err.Error())
					mu.Unlock()
				} else {
					t.Logf("[%s] 修复完成", h.IP)
				}
			}(h)
		}
		wg.Wait()
		var err error
		if len(errs) > 0 {
			sort.Strings(errs)
			err = fmt.Errorf("%d 台主机修复失败：%s", len(errs), strings.Join(errs, "；"))
		}
		res := "成功"
		if err != nil {
			res = "失败：" + err.Error()
		}
		s.auditDone(ip, u, "", "deploy.fix", fmt.Sprintf("一键修复环境项完成（任务 %s）", t.ID()), res)
		t.Finish(err)
	}()
	ok(w, map[string]any{"ok": true, "taskId": t.ID()})
}

func (s *Server) fixHost(t *taskRun, h model.Host, items []string, javaHome, home string, jdk model.Package) error {
	ctx, cancel := context.WithTimeout(t.ctx, 30*time.Minute)
	defer cancel()
	rm, err := s.dialRemote(ctx, t, hostTarget(h))
	if err != nil {
		return err
	}
	defer rm.Close()
	t.SetProg(10)
	sudo := "sudo -n "
	if res, _ := rm.quiet("id -u"); strings.TrimSpace(res.Stdout) == "0" {
		sudo = ""
	} else if res, err := rm.quiet("sudo -n true"); err != nil || res.Code != 0 {
		if hasAny(items, "swap", "swappiness", "thp", "overcommit", "max_map_count", "nofile", "nproc", "dir") {
			return fmt.Errorf("需要 root 账号或免密 sudo")
		}
	}
	if contains(items, "dir") {
		if err := fixInstallDir(rm, sudo, h.User, home); err != nil {
			return err
		}
	}
	var sysctl []string
	for _, id := range items {
		if l, ok := sysctlKeys[id]; ok {
			sysctl = append(sysctl, l)
		}
	}
	if len(sysctl) > 0 {
		const f = "/etc/sysctl.d/99-starrocks.conf"
		if err := rm.runStdin(sudo+"tee "+f+" > /dev/null", strings.Join(sysctl, "\n")+"\n"); err != nil {
			return err
		}
		if _, err := rm.run(sudo + "sysctl -p " + f); err != nil {
			return err
		}
	}
	if contains(items, "swap") {
		if _, err := rm.run(sudo + "swapoff -a"); err != nil {
			return err
		}
		if _, err := rm.run(sudo + "sed -i '/[[:space:]]swap[[:space:]]/ s/^/#/' /etc/fstab"); err != nil {
			return err
		}
	}
	if contains(items, "thp") {
		if err := rm.runStdin(sudo+"tee /sys/kernel/mm/transparent_hugepage/enabled > /dev/null", "madvise\n"); err != nil {
			return err
		}
		if err := rm.runStdin(sudo+"tee /etc/tmpfiles.d/starrocks-thp.conf > /dev/null", "w /sys/kernel/mm/transparent_hugepage/enabled - - - - madvise\n"); err != nil {
			return err
		}
	}
	if contains(items, "nofile") || contains(items, "nproc") {
		var ls []string
		if contains(items, "nofile") {
			ls = append(ls, "* soft nofile 655350", "* hard nofile 655350")
		}
		if contains(items, "nproc") {
			ls = append(ls, "* soft nproc 655350", "* hard nproc 655350")
		}
		if err := rm.runStdin(sudo+"tee /etc/security/limits.d/20-starrocks.conf > /dev/null", strings.Join(ls, "\n")+"\n"); err != nil {
			return err
		}
		rm.logf("limits 对新登录会话生效，已运行的进程需重启")
	}
	t.SetProg(60)
	if contains(items, "java") {
		parent := javaHome[:strings.LastIndex(javaHome, "/")]
		if parent == "" {
			parent = "/"
		}
		up := parent + "/.upload"
		if _, err := rm.run("mkdir -p " + up); err != nil {
			return err
		}
		remotePkg := up + "/" + jdk.Name
		if err := rm.upload(s.pkgPath(jdk.Name), jdk.Size, jdk.SHA, remotePkg); err != nil {
			return err
		}
		if err := rm.extract(remotePkg, javaHome); err != nil {
			return err
		}
		if _, err := rm.run(fmt.Sprintf("test -x %s/bin/java && echo ok", javaHome)); err != nil {
			return fmt.Errorf("JDK 解压后没有找到 %s/bin/java", javaHome)
		}
	}
	return nil
}

// rememberPrecheckHome 记录用户最近一次预检使用的安装目录。
func (s *Server) rememberPrecheckHome(login, home string) {
	s.pcHomeMu.Lock()
	defer s.pcHomeMu.Unlock()
	if s.pcHome == nil {
		s.pcHome = map[string]string{}
	}
	s.pcHome[login] = strings.TrimRight(home, "/")
}

func (s *Server) precheckHome(login string) string {
	s.pcHomeMu.Lock()
	defer s.pcHomeMu.Unlock()
	return s.pcHome[login]
}

// 这些顶级目录下的路径不允许由平台改属主。
var systemTopDirs = map[string]bool{"bin": true, "boot": true, "dev": true, "etc": true, "lib": true, "lib32": true, "lib64": true, "libx32": true,
	"proc": true, "root": true, "run": true, "sbin": true, "sys": true, "usr": true, "var": true}

// checkFixDir 校验平台可以代为创建并授权的安装根目录：合法绝对路径，至少两级，不在系统目录下，
// 也不能是 /home 下别人的家目录（/home/<用户> 本身）。不满足时需要管理员手工处理。
func checkFixDir(dir string) error {
	if !dirRe.MatchString(dir) || strings.HasSuffix(dir, "/") {
		return fmt.Errorf("部署目录 %q 不合法", dir)
	}
	parts := strings.Split(dir[1:], "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("部署目录 %q 不合法", dir)
		}
	}
	switch {
	case len(parts) < 2:
		return fmt.Errorf("部署目录 %s 层级太浅，平台不会修改它的属主，请手工创建并授权", dir)
	case systemTopDirs[parts[0]]:
		return fmt.Errorf("部署目录 %s 位于系统目录下，平台不会修改它的属主，请手工创建并授权", dir)
	case parts[0] == "home" && len(parts) < 3:
		return fmt.Errorf("部署目录 %s 是某个用户的家目录，平台不会修改它的属主，请手工处理", dir)
	}
	return nil
}

// fixInstallDir 创建安装根目录并把属主交给 SSH 用户，只处理这一个目录本身（不递归）。
// 目录已存在且属于 root 或该用户之外的账号时不动，避免抢走别的服务的数据目录。sudo 为空表示当前已是 root。
func fixInstallDir(rm *remote, sudo, user, dir string) error {
	if !userRe.MatchString(user) {
		return fmt.Errorf("SSH 用户名 %q 不合法，无法授权目录", user)
	}
	if err := checkFixDir(dir); err != nil {
		return err
	}
	if res, err := rm.quiet("stat -c %U " + dir); err != nil {
		return err
	} else if res.Code == 0 {
		if owner := strings.TrimSpace(res.Stdout); owner != "root" && owner != user {
			return fmt.Errorf("%s 已存在且属于用户 %s，为避免影响其他服务没有修改，请手工处理", dir, owner)
		}
	}
	if _, err := rm.run(sudo + "mkdir -p " + dir); err != nil {
		return err
	}
	if user != "root" {
		if _, err := rm.run(sudo + "chown " + user + ": " + dir); err != nil {
			return err
		}
	}
	rm.logf("安装目录 %s 已就绪（属主 %s）", dir, user)
	return nil
}

func hasAny(items []string, ids ...string) bool {
	for _, id := range ids {
		if contains(items, id) {
			return true
		}
	}
	return false
}

var _ = sshx.Target{}
