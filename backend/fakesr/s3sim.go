// @author: kevinkelly
// @license: Apache Licence
// @file: s3sim.go
// @time: 2026/10/28

package fakesr

import (
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// s3Sim 是内存中的 S3 服务（路径风格，只支持按前缀列出与批量删除），保存备份仓库里的“快照文件”。
// 仓库位置形如 s3a://sr-backup/prod-sr-01，快照目录为 <路径>/__starrocks_repository_<仓库>/__ss_<快照>/。
// 启用后 SHOW SNAPSHOT 只列出对象存储里仍有文件的快照：平台删除快照目录后，快照随之消失。
type s3Sim struct {
	mu   sync.Mutex
	ak   string
	objs map[string]int64 // bucket/key → 大小
	srv  *http.Server
	ln   net.Listener
}

func (s *s3Sim) put(bucket string, keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		s.objs[bucket+"/"+k] = 1024
	}
}

func (s *s3Sim) keys(bucket, prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objs {
		if strings.HasPrefix(k, bucket+"/"+prefix) {
			out = append(out, strings.TrimPrefix(k, bucket+"/"))
		}
	}
	sort.Strings(out)
	return out
}

func (s *s3Sim) serve(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+s.ak+"/") || r.Header.Get("X-Amz-Date") == "" {
		s3Err(w, 403, "AccessDenied", "Access Denied")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket := strings.SplitN(path, "/", 2)[0]
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range s.objs {
			if strings.HasPrefix(k, bucket+"/"+prefix) {
				keys = append(keys, strings.TrimPrefix(k, bucket+"/"))
			}
		}
		sort.Strings(keys)
		start := 0
		fmt.Sscanf(r.URL.Query().Get("continuation-token"), "t%d", &start)
		end := start + 1000
		trunc := end < len(keys)
		if end > len(keys) {
			end = len(keys)
		}
		if start > end {
			start = end
		}
		var b strings.Builder
		fmt.Fprintf(&b, `<ListBucketResult><IsTruncated>%v</IsTruncated>`, trunc)
		if trunc {
			fmt.Fprintf(&b, `<NextContinuationToken>t%d</NextContinuationToken>`, end)
		}
		for _, k := range keys[start:end] {
			var e strings.Builder
			xml.EscapeText(&e, []byte(k))
			fmt.Fprintf(&b, `<Contents><Key>%s</Key><Size>%d</Size></Contents>`, e.String(), s.objs[bucket+"/"+k])
		}
		b.WriteString(`</ListBucketResult>`)
		io.WriteString(w, b.String())
	case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
		body, _ := io.ReadAll(r.Body)
		var d struct {
			Objects []struct{ Key string } `xml:"Object"`
		}
		if err := xml.Unmarshal(body, &d); err != nil || r.Header.Get("Content-Md5") == "" {
			s3Err(w, 400, "MalformedXML", "bad delete request")
			return
		}
		for _, o := range d.Objects {
			delete(s.objs, bucket+"/"+o.Key)
		}
		io.WriteString(w, `<DeleteResult></DeleteResult>`)
	default:
		s3Err(w, 501, "NotImplemented", "not supported by fakesr")
	}
}

func s3Err(w http.ResponseWriter, code int, c, msg string) {
	w.WriteHeader(code)
	fmt.Fprintf(w, `<Error><Code>%s</Code><Message>%s</Message></Error>`, c, msg)
}

// startS3 启动 S3 服务并为已有的 S3 仓库快照生成文件。
func (c *Cluster) startS3() error {
	host := c.cfg.Host
	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(c.cfg.S3Port)))
	if err != nil {
		return fmt.Errorf("listen s3: %w", err)
	}
	s := &s3Sim{ak: c.cfg.S3AccessKey, objs: map[string]int64{}, ln: ln}
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second}
	c.s3 = s
	c.mu.Lock()
	c.https = append(c.https, s.srv)
	c.mu.Unlock()
	go func() { _ = s.srv.Serve(ln) }()
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	for _, r := range c.sim.ops.repos {
		for _, sn := range r.snaps {
			c.seedSnapshotS3(r, sn.name)
		}
	}
	return nil
}

// S3Addr 返回 S3 服务地址（未启用时为空）。
func (c *Cluster) S3Addr() string {
	if c.s3 == nil {
		return ""
	}
	return c.s3.ln.Addr().String()
}

// s3Loc 解析仓库位置（s3 / s3a / oss / cos 等 S3 协议）：返回桶与不带首尾斜杠的路径。
func s3Loc(loc string) (bucket, base string, ok bool) {
	i := strings.Index(loc, "://")
	if i < 0 {
		return "", "", false
	}
	switch loc[:i] {
	case "s3", "s3a", "oss", "cos", "obs", "tos":
	default:
		return "", "", false
	}
	rest := loc[i+3:]
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 {
		base = strings.Trim(parts[1], "/")
	}
	return parts[0], base, parts[0] != ""
}

func snapDir(base, repo, snap string) string {
	p := "__starrocks_repository_" + repo + "/__ss_" + snap + "/"
	if base != "" {
		p = base + "/" + p
	}
	return p
}

// seedSnapshotS3 为一个快照在 S3 里生成几个文件（调用方持有 sim.mu）。
func (c *Cluster) seedSnapshotS3(r *fakeRepo, snap string) {
	if c.s3 == nil {
		return
	}
	bucket, base, ok := s3Loc(r.loc)
	if !ok {
		return
	}
	d := snapDir(base, r.name, snap)
	c.s3.put(bucket, d+"__info_2026-10-01-00-00-00-000", d+"__meta_2026-10-01-00-00-00-000", d+"__db_10001/__tbl_10002/__part_10003/__idx_10002/__10004/seg_0.dat.abc", d+"__db_10001/__tbl_10002/__part_10003/__idx_10002/__10004/seg_1.dat.def")
}

// snapExists 在启用 S3 且仓库是 S3 位置时，判断快照目录下是否还有文件；其他情况视为存在（调用方持有 sim.mu）。
func (c *Cluster) snapExists(r *fakeRepo, snap string) bool {
	if c.s3 == nil {
		return true
	}
	bucket, base, ok := s3Loc(r.loc)
	if !ok {
		return true
	}
	return len(c.s3.keys(bucket, snapDir(base, r.name, snap))) > 0
}

// AddSnapshot 供测试 / 演示使用：在仓库里直接加入一个快照（时间戳为 at）。
func (c *Cluster) AddSnapshot(repo, snap string, at time.Time) error {
	c.sim.mu.Lock()
	defer c.sim.mu.Unlock()
	r := c.sim.ops.repo(repo)
	if r == nil {
		return fmt.Errorf("repository %s does not exist", repo)
	}
	r.snaps = append(r.snaps, fakeSnap{snap, at.Format("2006-01-02-15-04-05") + "-000"})
	c.seedSnapshotS3(r, snap)
	return nil
}

// S3Keys 供测试使用：返回 S3 中桶内某前缀下的对象键。
func (c *Cluster) S3Keys(bucket, prefix string) []string {
	if c.s3 == nil {
		return nil
	}
	return c.s3.keys(bucket, prefix)
}
