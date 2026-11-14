// @author: kevinkelly
// @license: Apache Licence
// @file: ssh.go
// @time: 2026/10/28

package fakesr

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshUser = "starrocks"

var reStartScript = regexp.MustCompile(`^test -x \S+/bin/start_(fe|be|cn)\.sh && echo ok$`)

func (c *Cluster) sshConf(host *hostSim) (*ssh.ServerConfig, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	conf := &ssh.ServerConfig{
		PasswordCallback: func(m ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if m.User() == sshUser && subtle.ConstantTimeCompare(pw, []byte(c.cfg.Password)) == 1 {
				return nil, nil
			}
			return nil, fmt.Errorf("password rejected for %q", m.User())
		},
		PublicKeyCallback: func(m ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if m.User() != sshUser {
				return nil, fmt.Errorf("unknown user %q", m.User())
			}
			if host != nil && !host.isAuthorized(key) { // Hosts 模式：只接受已写入 authorized_keys 的公钥
				return nil, fmt.Errorf("public key is not authorized")
			}
			return nil, nil
		},
	}
	conf.AddHostKey(signer)
	return conf, nil
}

func (c *Cluster) startSSH() error {
	conf, err := c.sshConf(nil)
	if err != nil {
		return err
	}
	port := c.cfg.SSHPort
	c.sshLn, err = net.Listen("tcp", net.JoinHostPort(c.cfg.Host, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("listen ssh: %w", err)
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.acceptLoop(c.sshLn, func(conn net.Conn) { c.handleSSH(conn, conf, nil) })
	}()
	return nil
}

func (c *Cluster) handleSSH(conn net.Conn, conf *ssh.ServerConfig, host *hostSim) {
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	sc, chans, reqs, err := ssh.NewServerConn(conn, conf)
	if err != nil {
		c.logf("ssh handshake from %s: %v", conn.RemoteAddr(), err)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	defer sc.Close()
	go ssh.DiscardRequests(reqs)

	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go c.handleSession(ch, chReqs, host)
	}
}

func (c *Cluster) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request, host *hostSim) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			// 仅支持 exec；env/pty-req 等直接拒绝
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)

		c.logf("ssh exec: %s", payload.Command)
		var out string
		var code int
		if host != nil {
			out, code = host.exec(strings.TrimSpace(payload.Command), ch)
		} else {
			out, code = c.execCommand(strings.TrimSpace(payload.Command))
		}
		if code == 0 {
			_, _ = ch.Write([]byte(out))
		} else {
			_, _ = ch.Stderr().Write([]byte(out))
		}
		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, uint32(code))
		_, _ = ch.SendRequest("exit-status", false, status)
		return
	}
}

// execCommand 模拟主机命令输出，返回 (输出, 退出码)。
func (c *Cluster) execCommand(cmd string) (string, int) {
	switch cmd {
	case "cat /proc/sys/vm/swappiness":
		return "0\n", 0
	case "cat /sys/kernel/mm/transparent_hugepage/enabled":
		return pick(c.problems, "[always] madvise never\n", "always [madvise] never\n"), 0
	case "cat /proc/sys/vm/overcommit_memory":
		return "1\n", 0
	case "cat /proc/sys/vm/max_map_count":
		return pick(c.problems, "65530\n", "262144\n"), 0
	case "date +%s%3N":
		return strconv.FormatInt(time.Now().UnixMilli(), 10) + "\n", 0
	}
	if reStartScript.MatchString(cmd) {
		return "ok\n", 0
	}
	name := cmd
	if f := strings.Fields(cmd); len(f) > 0 {
		name = f[0]
	}
	return fmt.Sprintf("bash: %s: command not found\n", name), 127
}
