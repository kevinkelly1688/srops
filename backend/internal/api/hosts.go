// @author: kevinkelly
// @license: Apache Licence
// @file: hosts.go
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
	"starrocks-ops/internal/store"
)

// 主机管理：部署、扩容的目标主机。平台只保存主机地址、SSH 用户 / 端口和探测到的规格；
// 首次添加时可用密码登录并把平台公钥写入 authorized_keys，之后一律使用密钥，密码不落盘。

const probeScript = `echo "cpu=$(nproc)"; echo "mem=$(awk '/MemTotal/ {print $2}' /proc/meminfo)"; echo "arch=$(uname -m)"; echo "avx2=$(grep -m1 -c avx2 /proc/cpuinfo)"; echo "os=$(. /etc/os-release && echo $PRETTY_NAME)"; echo "host=$(hostname)"; echo "disk=$(df -P -BG / | awk 'NR==2{print $2}')"`

func cidrCmd(ip string) string {
	return fmt.Sprintf(`echo "cidr=$(ip -4 -o addr show | awk '{print $4}' | grep -F '%s/' | head -1)"`, ip)
}

var ipv4Re = regexp.MustCompile(`^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)
var ipRangeRe = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.)(\d{1,3})-(\d{1,3})$`)

// expandHosts 展开主机列表：每行 / 空格 / 逗号分隔一个，支持 10.40.1.31-35 形式的末段范围。
func expandHosts(text string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, tok := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == ';' }) {
		if m := ipRangeRe.FindStringSubmatch(tok); m != nil {
			a, _ := strconv.Atoi(m[2])
			b, _ := strconv.Atoi(m[3])
			if a > b || b > 255 || b-a >= 64 {
				return nil, fmt.Errorf("地址范围不合法：%s（末段 0–255，一次最多 64 个）", tok)
			}
			for i := a; i <= b; i++ {
				add(m[1] + strconv.Itoa(i))
			}
			continue
		}
		if m := ipv4Re.FindStringSubmatch(tok); m != nil {
			for _, o := range m[1:] {
				if n, _ := strconv.Atoi(o); n > 255 {
					return nil, fmt.Errorf("IP 地址不合法：%s", tok)
				}
			}
			add(tok)
			continue
		}
		if hostRe.MatchString(tok) && !strings.HasPrefix(tok, "-") {
			add(tok)
			continue
		}
		return nil, fmt.Errorf("主机地址不合法：%s", tok)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("请填写主机地址")
	}
	if len(out) > 64 {
		return nil, fmt.Errorf("一次最多添加 64 台主机")
	}
	return out, nil
}

func normArch(a string) string {
	switch strings.TrimSpace(a) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return strings.TrimSpace(a)
}

// probeHost 读取主机规格；失败的项保持零值。
func probeHost(ctx context.Context, sess *sshx.Session, h *model.Host) {
	if res, err := sess.Run(ctx, probeScript); err == nil && res.Code == 0 {
		for _, ln := range strings.Split(res.Stdout, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(ln), "=")
			if !ok {
				continue
			}
			switch k {
			case "cpu":
				h.CPU, _ = strconv.Atoi(v)
			case "mem":
				kb, _ := strconv.ParseFloat(v, 64)
				h.MemGB = int(kb/1024/1024 + 0.5)
			case "arch":
				h.Arch = normArch(v)
			case "avx2":
				h.AVX2 = strings.TrimSpace(v) != "0" && v != ""
			case "os":
				h.OS = v
			case "host":
				h.Name = v
			case "disk":
				h.DiskGB, _ = strconv.Atoi(strings.TrimSuffix(v, "G"))
			}
		}
	}
	if res, err := sess.Run(ctx, cidrCmd(h.IP)); err == nil {
		if k, v, ok := strings.Cut(strings.TrimSpace(res.Stdout), "="); ok && k == "cidr" {
			h.CIDR = v
		}
	}
}

// hostTarget 返回主机的 SSH 连接参数（只用平台密钥）。
func hostTarget(h model.Host) sshx.Target {
	return sshx.Target{Host: h.IP, Port: h.Port, User: h.User}
}

func (s *Server) hostByIP(ip string) (model.Host, bool) {
	var out model.Host
	var found bool
	s.Store.View(func(st *store.State) {
		for _, h := range st.Hosts {
			if h.IP == ip {
				out, found = h, true
			}
		}
	})
	return out, found
}

func (s *Server) putHost(h model.Host) {
	s.Store.Update(func(st *store.State) error {
		for i := range st.Hosts {
			if st.Hosts[i].IP == h.IP {
				h.UsedBy = st.Hosts[i].UsedBy
				st.Hosts[i] = h
				return nil
			}
		}
		st.Hosts = append(st.Hosts, h)
		return nil
	})
}

// markHostsUsed 记录主机被某个集群使用（部署 / 扩容完成后）。
func (s *Server) markHostsUsed(cluster string, ips ...string) {
	s.Store.Update(func(st *store.State) error {
		for i := range st.Hosts {
			for _, ip := range ips {
				if st.Hosts[i].IP == ip && !contains(st.Hosts[i].UsedBy, cluster) {
					st.Hosts[i].UsedBy = append(st.Hosts[i].UsedBy, cluster)
				}
			}
		}
		return nil
	})
}

type hostOut struct {
	model.Host
	Used []string `json:"used"` // 使用该主机的集群（含导入集群的 FE 地址）
}

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request, u *model.User) {
	out := []hostOut{}
	s.Store.View(func(st *store.State) {
		for _, h := range st.Hosts {
			used := append([]string{}, h.UsedBy...)
			for _, c := range st.Clusters {
				if contains(c.FEHosts, h.IP) && !contains(used, c.ID) {
					used = append(used, c.ID)
				}
			}
			out = append(out, hostOut{Host: h, Used: used})
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	ok(w, map[string]any{"items": out, "publicKey": s.Keys.PublicKey})
}

type hostIn struct {
	IPs      string `json:"ips"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Auth     string `json:"auth"` // key / password（仅用于首次分发公钥）
	Password string `json:"password"`
	Sudo     bool   `json:"sudo"`
}

type hostResult struct {
	IP    string `json:"ip"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Note  string `json:"note,omitempty"`
}

func (s *Server) addHosts(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in hostIn
	if !decode(w, r, &in) {
		return
	}
	if in.Port == 0 {
		in.Port = 22
	}
	if !userRe.MatchString(in.User) || in.Port < 1 || in.Port > 65535 || (in.Auth != "key" && in.Auth != "password") || (in.Auth == "password" && in.Password == "") {
		fail(w, http.StatusBadRequest, "SSH 用户、端口或认证方式不合法")
		return
	}
	ips, err := expandHosts(in.IPs)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	res := make([]hostResult, len(ips))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, ip := range ips {
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res[i] = s.addOneHost(r.Context(), in, ip, u)
		}(i, ip)
	}
	wg.Wait()
	okN := 0
	var bad []string
	for _, x := range res {
		if x.OK {
			okN++
		} else {
			bad = append(bad, x.IP)
		}
	}
	act := fmt.Sprintf("添加主机 %d 台成功", okN)
	if len(bad) > 0 {
		act += "，失败：" + strings.Join(bad, "、")
	}
	var aerr error
	if okN == 0 {
		aerr = fmt.Errorf("全部失败")
	}
	s.auditSQL(r, u, "deploy.host", act, "", aerr)
	ok(w, map[string]any{"results": res})
}

func (s *Server) addOneHost(parent context.Context, in hostIn, ip string, u *model.User) hostResult {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	h := model.Host{IP: ip, Port: in.Port, User: in.User, Sudo: in.Sudo, AddedBy: u.Login, AddedAt: time.Now()}
	note := ""
	if in.Auth == "password" {
		sess, err := s.Keys.Dial(ctx, sshx.Target{Host: ip, Port: in.Port, User: in.User, Password: in.Password})
		if err != nil {
			return hostResult{IP: ip, Error: scrubPw(err.Error(), in.Password)}
		}
		pub := s.Keys.PublicKey
		f := strings.Fields(pub)
		if len(f) < 2 || strings.ContainsAny(pub, "'\r\n") {
			sess.Close()
			return hostResult{IP: ip, Error: "平台公钥格式异常"}
		}
		cmd := fmt.Sprintf("mkdir -p ~/.ssh && chmod 700 ~/.ssh && (grep -qF '%s' ~/.ssh/authorized_keys 2>/dev/null || echo '%s' >> ~/.ssh/authorized_keys) && chmod 600 ~/.ssh/authorized_keys", f[1], pub)
		r2, err := sess.Run(ctx, cmd)
		sess.Close()
		if err != nil || r2.Code != 0 {
			return hostResult{IP: ip, Error: "写入 authorized_keys 失败：" + strings.TrimSpace(r2.Stderr)}
		}
		note = "已把平台公钥写入 ~/.ssh/authorized_keys，之后使用密钥登录（密码不保存）"
	}
	sess, err := s.Keys.Dial(ctx, hostTarget(h))
	if err != nil {
		msg := err.Error()
		if in.Auth == "key" {
			msg += "。请先把平台公钥加入该主机的 ~/.ssh/authorized_keys，或改用「密码（仅首次分发公钥）」"
		}
		return hostResult{IP: ip, Error: scrubPw(msg, in.Password)}
	}
	defer sess.Close()
	h.SSH = "ok"
	probeHost(ctx, sess, &h)
	h.CheckedAt = time.Now()
	if in.Sudo {
		if r2, err := sess.Run(ctx, "sudo -n true"); err != nil || r2.Code != 0 {
			note += "（注意：该用户不能免密 sudo，一键修复环境项将不可用）"
		}
	}
	s.putHost(h)
	return hostResult{IP: ip, OK: true, Note: note}
}

func scrubPw(msg, pw string) string {
	if len(pw) >= 3 {
		msg = strings.ReplaceAll(msg, pw, "***")
	}
	return msg
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request, u *model.User) {
	ip := r.PathValue("ip")
	var used []string
	err := s.Store.Update(func(st *store.State) error {
		for i := range st.Hosts {
			if st.Hosts[i].IP != ip {
				continue
			}
			used = append(used, st.Hosts[i].UsedBy...)
			for _, c := range st.Clusters {
				if contains(c.FEHosts, ip) && !contains(used, c.ID) {
					used = append(used, c.ID)
				}
			}
			if len(used) > 0 {
				return conflict("该主机正被集群使用（" + strings.Join(used, "、") + "），不能删除")
			}
			st.Hosts = append(st.Hosts[:i], st.Hosts[i+1:]...)
			return nil
		}
		return missing("主机不存在")
	})
	s.auditSQL(r, u, "deploy.host", "删除主机 "+ip, "", err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}

// recheckHost 重新探测主机的连通性与规格。
func (s *Server) recheckHost(w http.ResponseWriter, r *http.Request, u *model.User) {
	h, found := s.hostByIP(r.PathValue("ip"))
	if !found {
		fail(w, http.StatusNotFound, "主机不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	h.CheckedAt = time.Now()
	sess, err := s.Keys.Dial(ctx, hostTarget(h))
	if err != nil {
		h.SSH = err.Error()
	} else {
		h.SSH = "ok"
		probeHost(ctx, sess, &h)
		sess.Close()
	}
	s.putHost(h)
	ok(w, h)
}
