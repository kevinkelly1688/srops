// @author: kevinkelly
// @license: Apache Licence
// @file: hostsim_cmd.go
// @time: 2026/10/28

package fakesr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 平台下发的命令写法固定，这里按这些写法逐条模拟；没见过的命令返回 127（command not found）。

var (
	reHStart  = regexp.MustCompile(`^cd (\S+) && bin/start_(fe|be|cn)\.sh(?: --helper (\S+))? --daemon$`)
	reHStop   = regexp.MustCompile(`^cd (\S+) && bin/stop_(fe|be|cn)\.sh( -g)?$`)
	reHStatus = regexp.MustCompile(`^if \[ -f (\S+)/bin/(fe|be|cn)\.pid \] && kill -0 \$\(cat \S+\) 2>/dev/null; then echo running; else echo stopped; fi$`)
	reHTail   = regexp.MustCompile(`^tail -n (\d+) (\S+)$`)
	reHCat    = regexp.MustCompile(`^cat (/\S+)$`)
	reHWrite  = regexp.MustCompile(`^cat > (\S+)$`)
	reHCp     = regexp.MustCompile(`^cp (-a )?(\S+) (\S+)$`)
	reHMv     = regexp.MustCompile(`^mv (\S+) (\S+)$`)
	reHRm     = regexp.MustCompile(`^rm -rf (.+)$`)
	reHMkdir  = regexp.MustCompile(`^mkdir -p (.+)$`)
	reHTest   = regexp.MustCompile(`^test (-[xefdw]) (\S+)$`)
	reHStat   = regexp.MustCompile(`^stat -c %s (\S+)$`)
	reHStatU  = regexp.MustCompile(`^stat -c %U (\S+)$`)
	reHSudoMk = regexp.MustCompile(`^sudo -n mkdir -p (\S+)$`)
	reHSudoCh = regexp.MustCompile(`^sudo -n chown ([a-z_][a-z0-9_-]*): (\S+)$`)
	reHSha    = regexp.MustCompile(`^sha256sum (\S+)$`)
	reHTar    = regexp.MustCompile(`^tar -xzf (\S+) -C (\S+) --strip-components=1$`)
	reHSudoT  = regexp.MustCompile(`^sudo -n tee (\S+) > /dev/null$`)
	reHAuthK  = regexp.MustCompile(`echo '([^']+)' >> ~/\.ssh/authorized_keys`)
	reHCidr   = regexp.MustCompile(`grep -F '([0-9.]+)/'`)
	reHDirOK  = regexp.MustCompile(`^d=(\S+); while \[ ! -e "\$d" \]; do d=\$\(dirname "\$d"\); done; (test -w "\$d" && echo ok|df -P -BG "\$d" \| awk 'NR==2\{print \$4\}')$`)
	reHSedFs  = regexp.MustCompile(`^sudo -n sed -i `)
)

// splitAnd 把用 && 连接的简单命令拆开；含子 shell / 引号的复杂命令不拆。
func splitAnd(cmd string) []string {
	for _, p := range []string{"cd ", "mkdir ", "cat > ", "test ", "rm ", "mv ", "cp ", "tar ", "stat ", "sha256sum "} {
		if strings.HasPrefix(cmd, p) {
			return strings.Split(cmd, " && ")
		}
	}
	return []string{cmd}
}

// exec 执行一条命令；stdin 供 `cat > file` / `tee` 读取。
func (h *hostSim) exec(cmd string, stdin io.Reader) (string, int) {
	h.c.logf("ssh[%s] exec: %.200s", h.ip, cmd)
	if reHStart.MatchString(cmd) || reHStop.MatchString(cmd) || reHStatus.MatchString(cmd) {
		return h.exec1(cmd, stdin)
	}
	var out strings.Builder
	for _, s := range splitAnd(cmd) {
		o, code := h.exec1(strings.TrimSpace(s), stdin)
		out.WriteString(o)
		if code != 0 {
			return out.String(), code
		}
	}
	return out.String(), 0
}

func (h *hostSim) exists(p string) bool {
	if _, ok := h.fs[p]; ok || h.dirs[p] {
		return true
	}
	pre := strings.TrimRight(p, "/") + "/"
	for k := range h.fs {
		if strings.HasPrefix(k, pre) {
			return true
		}
	}
	for k := range h.dirs {
		if strings.HasPrefix(k, pre) {
			return true
		}
	}
	return false
}

// writable 判断 SSH 用户能否在 p（或其最近的已存在上级目录）下写入：该目录属主为 root 时不可写。调用方需持有 h.mu。
func (h *hostSim) writable(p string) bool {
	for d := strings.TrimRight(p, "/"); d != "" && d != "/"; d = parentOf(d) {
		if h.exists(d) {
			return h.owner[d] != "root"
		}
	}
	return true
}

func parentOf(d string) string {
	if i := strings.LastIndex(d, "/"); i > 0 {
		return d[:i]
	}
	return "/"
}

func (h *hostSim) writableLocked(p string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.writable(p)
}

func (h *hostSim) isDir(p string) bool {
	if h.dirs[p] {
		return true
	}
	pre := strings.TrimRight(p, "/") + "/"
	for k := range h.fs {
		if strings.HasPrefix(k, pre) {
			return true
		}
	}
	return false
}

func (h *hostSim) removeAll(p string) {
	p = strings.TrimRight(p, "/")
	delete(h.fs, p)
	delete(h.dirs, p)
	pre := p + "/"
	for k := range h.fs {
		if strings.HasPrefix(k, pre) {
			delete(h.fs, k)
		}
	}
	for k := range h.dirs {
		if strings.HasPrefix(k, pre) {
			delete(h.dirs, k)
		}
	}
}

func (h *hostSim) copyTree(src, dst string) bool {
	src, dst = strings.TrimRight(src, "/."), strings.TrimRight(dst, "/")
	if !h.exists(src) {
		return false
	}
	if b, ok := h.fs[src]; ok {
		h.fs[dst] = append([]byte(nil), b...)
		return true
	}
	h.dirs[dst] = true
	pre := src + "/"
	for k, v := range h.fs {
		if strings.HasPrefix(k, pre) {
			h.fs[dst+"/"+strings.TrimPrefix(k, pre)] = append([]byte(nil), v...)
		}
	}
	return true
}

func (h *hostSim) exec1(cmd string, stdin io.Reader) (string, int) {
	if strings.HasPrefix(cmd, "cat /proc/") || strings.HasPrefix(cmd, "cat /sys/") {
		return h.system(cmd)
	}
	switch {
	case cmd == "":
		return "", 0
	case cmd == "echo ok":
		return "ok\n", 0
	case strings.HasPrefix(cmd, "chmod "), strings.HasPrefix(cmd, "touch "):
		return "", 0
	case reHStart.MatchString(cmd):
		m := reHStart.FindStringSubmatch(cmd)
		return h.start(m[1], m[2], m[3])
	case reHStop.MatchString(cmd):
		m := reHStop.FindStringSubmatch(cmd)
		return h.stop(m[1], m[2], m[3] != "")
	case reHStatus.MatchString(cmd):
		m := reHStatus.FindStringSubmatch(cmd)
		h.mu.Lock()
		p := h.procs[m[1]+"|"+m[2]]
		h.mu.Unlock()
		if p != nil && p.running {
			return "running\n", 0
		}
		return "stopped\n", 0
	case reHTail.MatchString(cmd):
		m := reHTail.FindStringSubmatch(cmd)
		n, _ := strconv.Atoi(m[1])
		return h.tail(m[2], n)
	case reHTest.MatchString(cmd):
		m := reHTest.FindStringSubmatch(cmd)
		h.mu.Lock()
		defer h.mu.Unlock()
		ok := h.exists(m[2])
		switch m[1] {
		case "-d":
			ok = h.isDir(m[2])
		case "-f", "-x":
			_, ok = h.fs[m[2]]
		case "-w":
			ok = !strings.HasPrefix(m[2], "/proc") && h.writable(m[2])
		}
		if ok {
			return "", 0
		}
		return "", 1
	case reHCat.MatchString(cmd):
		p := reHCat.FindStringSubmatch(cmd)[1]
		h.mu.Lock()
		b, ok := h.fs[p]
		h.mu.Unlock()
		if !ok {
			return "cat: " + p + ": No such file or directory\n", 1
		}
		return string(b), 0
	case reHWrite.MatchString(cmd):
		p := reHWrite.FindStringSubmatch(cmd)[1]
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err.Error() + "\n", 1
		}
		h.writeFile(p, b)
		return "", 0
	case reHSudoT.MatchString(cmd):
		return h.sudoTee(reHSudoT.FindStringSubmatch(cmd)[1], stdin)
	case reHMkdir.MatchString(cmd):
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, p := range strings.Fields(reHMkdir.FindStringSubmatch(cmd)[1]) {
			p = strings.TrimRight(p, "/")
			if !h.writable(p) {
				return "mkdir: cannot create directory '" + p + "': Permission denied\n", 1
			}
			h.dirs[p] = true
		}
		return "", 0
	case reHStatU.MatchString(cmd):
		p := strings.TrimRight(reHStatU.FindStringSubmatch(cmd)[1], "/")
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.exists(p) {
			return "stat: cannot statx '" + p + "': No such file or directory\n", 1
		}
		if o := h.owner[p]; o != "" {
			return o + "\n", 0
		}
		return "starrocks\n", 0
	case reHSudoMk.MatchString(cmd):
		if !h.sudoOK {
			return "sudo: a password is required\n", 1
		}
		p := strings.TrimRight(reHSudoMk.FindStringSubmatch(cmd)[1], "/")
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.exists(p) {
			h.owner[p] = "root"
		}
		h.dirs[p] = true
		return "", 0
	case reHSudoCh.MatchString(cmd):
		if !h.sudoOK {
			return "sudo: a password is required\n", 1
		}
		m := reHSudoCh.FindStringSubmatch(cmd)
		p := strings.TrimRight(m[2], "/")
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.exists(p) {
			return "chown: cannot access '" + p + "': No such file or directory\n", 1
		}
		h.owner[p] = m[1]
		return "", 0
	case reHRm.MatchString(cmd):
		h.mu.Lock()
		for _, p := range strings.Fields(reHRm.FindStringSubmatch(cmd)[1]) {
			h.removeAll(p)
		}
		h.mu.Unlock()
		return "", 0
	case reHMv.MatchString(cmd):
		m := reHMv.FindStringSubmatch(cmd)
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.exists(m[1]) {
			return "mv: cannot stat '" + m[1] + "': No such file or directory\n", 1
		}
		h.copyTree(m[1], m[2])
		h.removeAll(m[1])
		return "", 0
	case reHCp.MatchString(cmd):
		m := reHCp.FindStringSubmatch(cmd)
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.copyTree(m[2], m[3]) {
			return "cp: cannot stat '" + m[2] + "': No such file or directory\n", 1
		}
		return "", 0
	case reHStat.MatchString(cmd):
		p := reHStat.FindStringSubmatch(cmd)[1]
		h.mu.Lock()
		b, ok := h.fs[p]
		h.mu.Unlock()
		if !ok {
			return "stat: cannot stat '" + p + "': No such file or directory\n", 1
		}
		return fmt.Sprintf("%d\n", len(b)), 0
	case reHSha.MatchString(cmd):
		p := reHSha.FindStringSubmatch(cmd)[1]
		h.mu.Lock()
		b, ok := h.fs[p]
		h.mu.Unlock()
		if !ok {
			return "sha256sum: " + p + ": No such file or directory\n", 1
		}
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:]) + "  " + p + "\n", 0
	case reHTar.MatchString(cmd):
		m := reHTar.FindStringSubmatch(cmd)
		return h.untar(m[1], m[2])
	case strings.Contains(cmd, ">> ~/.ssh/authorized_keys"):
		if m := reHAuthK.FindStringSubmatch(cmd); m != nil {
			if f := strings.Fields(m[1]); len(f) >= 2 {
				h.authorize(f[1])
			}
		}
		return "", 0
	}
	return h.system(cmd)
}

// system 模拟系统信息与内核参数相关的命令。
func (h *hostSim) system(cmd string) (string, int) {
	k := func(name string) string {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.kernel[name]
	}
	switch {
	case cmd == "cat /proc/sys/vm/swappiness":
		return k("swappiness") + "\n", 0
	case cmd == "cat /sys/kernel/mm/transparent_hugepage/enabled":
		return k("thp") + "\n", 0
	case cmd == "cat /proc/sys/vm/overcommit_memory":
		return k("overcommit") + "\n", 0
	case cmd == "cat /proc/sys/vm/max_map_count":
		return k("max_map_count") + "\n", 0
	case cmd == "awk 'NR>1' /proc/swaps | wc -l":
		return k("swap") + "\n", 0
	case cmd == "ulimit -n":
		return k("nofile") + "\n", 0
	case cmd == "ulimit -u":
		return k("nproc") + "\n", 0
	case cmd == "date +%s%3N":
		return strconv.FormatInt(time.Now().UnixMilli(), 10) + "\n", 0
	case cmd == "uname -m":
		return "x86_64\n", 0
	case cmd == "grep -m1 -c avx2 /proc/cpuinfo":
		return "1\n", 0
	case cmd == "id -u":
		return "1000\n", 0
	case cmd == "sudo -n true":
		if h.sudoOK {
			return "", 0
		}
		return "sudo: a password is required\n", 1
	case strings.HasPrefix(cmd, `echo "cpu=$(nproc)"`):
		return "cpu=32\nmem=134217728\narch=x86_64\navx2=1\nos=Rocky Linux 9.3 (Blue Onyx)\nhost=sr-" + strings.ReplaceAll(h.ip, ".", "-") + "\ndisk=1800\n", 0
	case strings.HasPrefix(cmd, `echo "cidr=$(ip -4 -o addr show`):
		if m := reHCidr.FindStringSubmatch(cmd); m != nil {
			return "cidr=" + m[1] + "/24\n", 0
		}
		return "cidr=\n", 0
	case strings.HasPrefix(cmd, "ss -ltnH"):
		return h.listening(), 0
	case reHDirOK.MatchString(cmd):
		if strings.Contains(cmd, "df -P") {
			return "200G\n", 0
		}
		if p := reHDirOK.FindStringSubmatch(cmd)[1]; strings.HasPrefix(p, "/proc") || !h.writableLocked(p) {
			return "", 1
		}
		return "ok\n", 0
	case cmd == "java -version 2>&1 | head -1":
		if v := k("java"); v != "" {
			return v + "\n", 0
		}
		return "bash: java: command not found\n", 0
	case cmd == "sudo -n swapoff -a":
		h.setKernel("swap", "0")
		return "", 0
	case reHSedFs.MatchString(cmd):
		return "", 0
	case strings.HasPrefix(cmd, "sudo -n sysctl -p "):
		p := strings.TrimSpace(strings.TrimPrefix(cmd, "sudo -n sysctl -p "))
		h.mu.Lock()
		b := h.fs[p]
		h.mu.Unlock()
		for key, v := range parseConfSysctl(string(b)) {
			switch key {
			case "vm.swappiness":
				h.setKernel("swappiness", v)
			case "vm.overcommit_memory":
				h.setKernel("overcommit", v)
			case "vm.max_map_count":
				h.setKernel("max_map_count", v)
			}
		}
		return "", 0
	}
	name := cmd
	if f := strings.Fields(cmd); len(f) > 0 {
		name = f[0]
	}
	return fmt.Sprintf("bash: %s: command not found\n", name), 127
}

func parseConfSysctl(s string) map[string]string {
	m := map[string]string{}
	for _, ln := range strings.Split(s, "\n") {
		if i := strings.Index(ln, "="); i > 0 && !strings.HasPrefix(strings.TrimSpace(ln), "#") {
			m[strings.TrimSpace(ln[:i])] = strings.TrimSpace(ln[i+1:])
		}
	}
	return m
}

func (h *hostSim) setKernel(k, v string) {
	h.mu.Lock()
	h.kernel[k] = v
	h.mu.Unlock()
}

// sudoTee 模拟 `sudo -n tee <path>`：写入文件，并对内核相关路径立即生效。
func (h *hostSim) sudoTee(path string, stdin io.Reader) (string, int) {
	if !h.sudoOK {
		return "sudo: a password is required\n", 1
	}
	b, _ := io.ReadAll(stdin)
	switch {
	case path == "/sys/kernel/mm/transparent_hugepage/enabled":
		v := strings.TrimSpace(string(b))
		if v == "madvise" || v == "never" {
			h.setKernel("thp", map[string]string{"madvise": "always [madvise] never", "never": "always madvise [never]"}[v])
		}
	case strings.HasPrefix(path, "/etc/security/limits.d/"):
		for _, ln := range strings.Split(string(b), "\n") {
			f := strings.Fields(ln)
			if len(f) == 4 && f[0] == "*" && f[1] == "hard" {
				if f[2] == "nofile" {
					h.setKernel("nofile", f[3])
				} else if f[2] == "nproc" {
					h.setKernel("nproc", f[3])
				}
			}
		}
	}
	h.writeFile(path, b)
	return "", 0
}

// listening 返回本机监听的端口（每行 addr:port）：22 加上运行中进程的各端口。
func (h *hostSim) listening() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.listeningLocked()
}

func (h *hostSim) listeningLocked() string {
	ports := []int{22}
	for _, p := range h.procs {
		if !p.running {
			continue
		}
		for _, key := range []string{"http_port", "rpc_port", "query_port", "edit_log_port", "be_port", "be_http_port", "heartbeat_service_port", "brpc_port"} {
			if v, err := strconv.Atoi(p.conf[key]); err == nil {
				ports = append(ports, v)
			}
		}
	}
	sort.Ints(ports)
	var b strings.Builder
	for _, p := range ports {
		fmt.Fprintf(&b, "0.0.0.0:%d\n", p)
	}
	return b.String()
}

// untar 解压 .tar.gz 到目录，去掉最外一层目录（--strip-components=1）。
func (h *hostSim) untar(file, dir string) (string, int) {
	h.mu.Lock()
	b, ok := h.fs[file]
	h.mu.Unlock()
	if !ok {
		return "tar: " + file + ": Cannot open: No such file or directory\n", 2
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return "gzip: stdin: not in gzip format\ntar: Child returned status 1\n", 2
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		hd, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "tar: Unexpected EOF in archive\n", 2
		}
		if hd.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(hd.Name, "./"), "/", 2)
		if len(parts) < 2 || strings.Contains(parts[1], "..") {
			continue
		}
		body, _ := io.ReadAll(tr)
		files[parts[1]] = body
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.exists(dir) {
		return "tar: " + dir + ": Cannot open: No such file or directory\n", 2
	}
	for name, body := range files {
		h.fs[strings.TrimRight(dir, "/")+"/"+name] = body
	}
	return "", 0
}

// confPaths 返回主机上某类节点的配置文件路径（部署时目录由平台决定）。
func (h *hostSim) confPaths(kind string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	suf := "/conf/" + kind + ".conf"
	var out []string
	for p := range h.fs {
		if strings.HasSuffix(p, suf) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func (h *hostSim) jarVersion(dir, kind string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.fs[dir+"/lib/starrocks-"+kind+".jar"]
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "version=") {
			return strings.TrimSpace(strings.TrimPrefix(ln, "version="))
		}
	}
	return ""
}

func (h *hostSim) start(dir, kind, helper string) (string, int) {
	h.mu.Lock()
	_, has := h.fs[dir+"/bin/start_"+kind+".sh"]
	confB, hasConf := h.fs[dir+"/conf/"+kind+".conf"]
	p := h.procs[dir+"|"+kind]
	h.mu.Unlock()
	if !has || !hasConf {
		return "bash: bin/start_" + kind + ".sh: No such file or directory\n", 127
	}
	if p != nil && p.running {
		return strings.ToUpper(kind) + " running as process 12345. Stop it first.\n", 1
	}
	conf := parseConf(string(confB))
	ver := h.jarVersion(dir, kind)
	if ver == "" {
		return "Error: " + dir + "/lib/starrocks-" + kind + ".jar not found\n", 1
	}
	if jh := conf["JAVA_HOME"]; jh != "" && kind == "fe" {
		h.mu.Lock()
		_, okJ := h.fs[jh+"/bin/java"]
		h.mu.Unlock()
		if !okJ {
			return "Error: JAVA_HOME " + jh + " does not exist or has no bin/java\n", 1
		}
	}
	if kind == "be" {
		for _, sp := range strings.Split(conf["storage_root_path"], ";") {
			sp = strings.TrimSpace(sp)
			if i := strings.Index(sp, ","); i >= 0 {
				sp = sp[:i]
			}
			if sp == "" {
				continue
			}
			h.mu.Lock()
			okD := h.exists(strings.TrimRight(sp, "/"))
			h.mu.Unlock()
			if !okD {
				return "ERROR: storage_root_path " + sp + " does not exist, create it first\n", 1
			}
		}
	}
	if p == nil {
		p = &hproc{kind: kind, dir: dir}
		h.mu.Lock()
		h.procs[dir+"|"+kind] = p
		h.mu.Unlock()
	}
	p.conf, p.helper = conf, helper

	// 确定所属集群与节点
	root := h.c
	if kind == "fe" && p.n == nil {
		if helper == "" {
			c, err := root.spawnChild(h, p)
			if err != nil {
				return "Error: " + err.Error() + "\n", 1
			}
			_ = c
		} else {
			hostPort := strings.SplitN(helper, ":", 2)
			ep, _ := strconv.Atoi(hostPort[len(hostPort)-1])
			cl := root.findClusterByFE(hostPort[0], ep)
			if cl == nil {
				return "Error: helper " + helper + " is not a running FE\n", 1
			}
			n := cl.findFEByAddr(h.ip, confInt(conf, "edit_log_port", 9010))
			if n == nil {
				return "Error: this FE is not a member of the cluster, run ALTER SYSTEM ADD FOLLOWER/OBSERVER first\n", 1
			}
			p.n = n
			n.hs = h
		}
	}
	h.mu.Lock()
	p.running = true
	n := p.n
	h.mu.Unlock()
	if n == nil && kind != "fe" { // BE / CN：可能已经被 ADD 过
		if cl, nn := root.findRegistered(h.ip, kind, confInt(conf, "heartbeat_service_port", 9050)); cl != nil {
			h.mu.Lock()
			p.n = nn
			n = nn
			h.mu.Unlock()
		}
	}
	if n != nil {
		time.AfterFunc(250*time.Millisecond, func() {
			n.ver = ver
			n.down.Store(false)
		})
	}
	return "Start " + strings.ToUpper(kind) + "\n", 0
}

func (h *hostSim) stop(dir, kind string, graceful bool) (string, int) {
	h.mu.Lock()
	p := h.procs[dir+"|"+kind]
	h.mu.Unlock()
	if p == nil {
		return "bash: bin/stop_" + kind + ".sh: No such file or directory\n", 127
	}
	if graceful {
		time.Sleep(250 * time.Millisecond)
	}
	h.mu.Lock()
	p.running = false
	n := p.n
	h.mu.Unlock()
	if n != nil {
		n.down.Store(true)
		h.c.failoverAny(n)
	}
	return "", 0
}

// failover：Leader FE 停止后，由存活的 Follower 接任。
func (c *Cluster) failover(n *node) {
	if n.kind != kindFE {
		return
	}
	c.nmu.Lock()
	defer c.nmu.Unlock()
	if n.role != "LEADER" {
		return
	}
	for _, f := range c.FEs {
		if f.role == "FOLLOWER" && !f.down.Load() {
			f.role, n.role = "LEADER", "FOLLOWER"
			return
		}
	}
}

// failoverAny 在节点所属的集群（根集群或其子集群）里执行 failover。
func (c *Cluster) failoverAny(n *node) {
	for _, cl := range c.allClusters() {
		for _, x := range cl.nodesOf(kindFE) {
			if x == n {
				cl.failover(n)
				return
			}
		}
	}
}

// tail 返回日志文件的最后 n 行；日志内容按文件名生成。
func (h *hostSim) tail(path string, n int) (string, int) {
	noFile := "tail: cannot open '" + path + "' for reading: No such file or directory\n"
	i := strings.LastIndex(path, "/log/")
	if i < 0 {
		return noFile, 1
	}
	dir, name := path[:i], path[i+5:]
	var kind nodeKind = -1
	h.mu.Lock()
	for _, p := range h.procs {
		if p.dir == dir {
			for k := kindFE; k <= kindCN; k++ {
				if k.String() == p.kind {
					for _, x := range logNames[k] {
						if x == name {
							kind = k
						}
					}
				}
			}
		}
	}
	h.mu.Unlock()
	if kind < 0 {
		return noFile, 1
	}
	lines := h.c.logLines(kind, name)
	if n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n", 0
}
