// @author: kevinkelly
// @license: Apache Licence
// @file: sshx.go
// @time: 2026/10/28

// Package sshx 通过 SSH 在集群主机上执行只读命令（系统环境巡检、部署目录校验）。
// 平台首次启动生成 ed25519 密钥；主机指纹采用“首次信任”（TOFU）记录在 data/ssh/known_hosts，之后指纹变化即拒绝连接。
package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type Keys struct {
	signer     ssh.Signer
	PublicKey  string // authorized_keys 格式
	knownHosts string
	mu         sync.Mutex
}

// LoadOrCreate 读取或生成平台 SSH 密钥。
func LoadOrCreate(dir string) (*Keys, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	b, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		_, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		blk, merr := ssh.MarshalPrivateKey(priv, "sr-ops")
		if merr != nil {
			return nil, merr
		}
		b = pem.EncodeToMemory(blk)
		if err := os.WriteFile(keyPath, b, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, err
	}
	kh := filepath.Join(dir, "known_hosts")
	if _, err := os.Stat(kh); errors.Is(err, os.ErrNotExist) {
		os.WriteFile(kh, nil, 0o600)
	}
	pub := string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " sr-ops"
	return &Keys{signer: signer, PublicKey: pub, knownHosts: kh}, nil
}

// hostKeyCallback：已知主机校验指纹；未知主机记录后放行（TOFU）。
func (k *Keys) hostKeyCallback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	cb, err := knownhosts.New(k.knownHosts)
	if err != nil {
		return err
	}
	err = cb(hostname, remote, key)
	var ke *knownhosts.KeyError
	if errors.As(err, &ke) && len(ke.Want) == 0 {
		f, ferr := os.OpenFile(k.knownHosts, os.O_APPEND|os.O_WRONLY, 0o600)
		if ferr != nil {
			return ferr
		}
		defer f.Close()
		_, ferr = f.WriteString(knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n")
		return ferr
	}
	if errors.As(err, &ke) {
		return fmt.Errorf("主机 %s 的 SSH 指纹与首次记录不一致，已拒绝连接（如确认主机重装，请从 known_hosts 删除该行）", hostname)
	}
	return err
}

// Target 是一次 SSH 连接的参数。
type Target struct {
	Host     string
	Port     int
	User     string
	Password string // 为空则使用平台密钥
}

// Result 是一条命令的执行结果。
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Session 复用一条 SSH 连接执行多条命令。
type Session struct{ c *ssh.Client }

func (k *Keys) Dial(ctx context.Context, t Target) (*Session, error) {
	auth := []ssh.AuthMethod{ssh.PublicKeys(k.signer)}
	if t.Password != "" {
		auth = []ssh.AuthMethod{ssh.Password(t.Password), ssh.PublicKeys(k.signer)}
	}
	cfg := &ssh.ClientConfig{User: t.User, Auth: auth, HostKeyCallback: k.hostKeyCallback, Timeout: 8 * time.Second}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	d := net.Dialer{Timeout: 8 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 失败: %w", addr, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SSH 认证 %s@%s 失败: %w", t.User, addr, err)
	}
	return &Session{c: ssh.NewClient(cc, chans, reqs)}, nil
}

func (s *Session) Close() error { return s.c.Close() }

// Run 执行命令；ctx 取消时关闭会话。
func (s *Session) Run(ctx context.Context, cmd string) (Result, error) {
	return s.RunStdin(ctx, cmd, nil)
}

// RunStdin 同 Run，并把 in 作为命令的标准输入（用于上传文件、写配置：cat > file）。
func (s *Session) RunStdin(ctx context.Context, cmd string, in io.Reader) (Result, error) {
	sess, err := s.c.NewSession()
	if err != nil {
		return Result{}, err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout, sess.Stderr = &out, &errb
	if in != nil {
		sess.Stdin = in
	}
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case err = <-done:
	}
	r := Result{Stdout: out.String(), Stderr: errb.String()}
	var ee *ssh.ExitError
	if errors.As(err, &ee) {
		r.Code = ee.ExitStatus()
		return r, nil
	}
	return r, err
}
