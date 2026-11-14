// @author: kevinkelly
// @license: Apache Licence
// @file: packages.go
// @time: 2026/10/28

package api

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/store"
)

// 安装包管理：StarRocks 安装包与 JDK 包上传到 data/packages/，登记文件名解析出的版本与架构、大小与 SHA256。
// 上传时流式写盘并同时计算 SHA256、检查压缩包内容（StarRocks 包必须含 fe/bin/start_fe.sh 与 be/bin/start_be.sh；JDK 包必须含 bin/java）。

const maxPackageSize = 8 << 30

var (
	srPkgRe  = regexp.MustCompile(`^StarRocks-(\d+\.\d+\.\d+)(?:-rc\d+)?-([A-Za-z0-9]+)-(amd64|arm64)\.tar\.gz$`)
	jdkPkgRe = regexp.MustCompile(`^(?:jdk|OpenJDK|amazon-corretto|java)[A-Za-z0-9._+\-]*\.tar\.gz$`)
	jdkVerRe = regexp.MustCompile(`(?i)(?:jdk-?|openjdk-?|corretto-?|java-?)(\d+)`)
	shaRe    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// parsePackageName 从文件名识别类型、版本与架构；不认识的文件名返回 ok=false。
func parsePackageName(name string) (kind, ver, arch string, ok bool) {
	if m := srPkgRe.FindStringSubmatch(name); m != nil {
		return "starrocks", m[1], m[3], true
	}
	if jdkPkgRe.MatchString(name) {
		arch = "amd64"
		if l := strings.ToLower(name); strings.Contains(l, "aarch64") || strings.Contains(l, "arm64") {
			arch = "arm64"
		}
		ver = ""
		if m := jdkVerRe.FindStringSubmatch(name); m != nil {
			ver = m[1]
		}
		return "jdk", ver, arch, true
	}
	return "", "", "", false
}

// scanPackage 读取 .tar.gz 内容，确认它确实是对应类型的安装包。r 会被读完。
func scanPackage(r io.Reader, kind string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		io.Copy(io.Discard, r)
		return errors.New("不是有效的 gzip 压缩包")
	}
	tr := tar.NewReader(gz)
	var hasFE, hasBE, hasJava bool
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			io.Copy(io.Discard, r)
			return errors.New("压缩包已损坏或不完整：" + err.Error())
		}
		n := strings.TrimPrefix(h.Name, "./")
		parts := strings.SplitN(n, "/", 2)
		if len(parts) == 2 {
			switch parts[1] {
			case "fe/bin/start_fe.sh":
				hasFE = true
			case "be/bin/start_be.sh":
				hasBE = true
			case "bin/java":
				hasJava = true
			}
		}
	}
	io.Copy(io.Discard, r) // 读完 gzip 尾部，保证上传流被耗尽
	switch {
	case kind == "starrocks" && !(hasFE && hasBE):
		return errors.New("不是 StarRocks 安装包：缺少 fe/bin/start_fe.sh 或 be/bin/start_be.sh")
	case kind == "jdk" && !hasJava:
		return errors.New("不是 JDK 包：缺少 bin/java")
	}
	return nil
}

func (s *Server) listPackages(w http.ResponseWriter, r *http.Request, u *model.User) {
	out := []model.Package{}
	s.Store.View(func(st *store.State) { out = append(out, st.Packages...) })
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	ok(w, map[string]any{"items": out})
}

func (s *Server) pkgPath(name string) string { return filepath.Join(s.Store.PackageDir(), name) }

func (s *Server) packageByName(name string) (model.Package, bool) {
	var p model.Package
	var found bool
	s.Store.View(func(st *store.State) {
		for _, x := range st.Packages {
			if x.Name == name {
				p, found = x, true
			}
		}
	})
	return p, found
}

// uploadPackage 接收 multipart 表单：可选字段 sha256 + 文件字段 file。大文件流式处理，不整体读入内存。
func (s *Server) uploadPackage(w http.ResponseWriter, r *http.Request, u *model.User) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPackageSize)
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, http.StatusBadRequest, "请使用 multipart/form-data 上传")
		return
	}
	var want, name string
	var p model.Package
	var tmp string
	cleanup := func() {
		if tmp != "" {
			os.Remove(tmp)
		}
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			fail(w, http.StatusBadRequest, "读取上传内容失败："+err.Error())
			return
		}
		switch part.FormName() {
		case "sha256":
			b, _ := io.ReadAll(io.LimitReader(part, 128))
			want = strings.ToLower(strings.TrimSpace(string(b)))
		case "file":
			name = path.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
			kind, ver, arch, okName := parsePackageName(name)
			if !okName {
				fail(w, http.StatusBadRequest, "文件名不符合要求：StarRocks 包应为 StarRocks-<版本>-<系统>-<amd64|arm64>.tar.gz（例如 StarRocks-3.5.6-centos-amd64.tar.gz），JDK 包应为 jdk*.tar.gz")
				return
			}
			if _, dup := s.packageByName(name); dup {
				fail(w, http.StatusConflict, "同名安装包已存在，请先删除")
				return
			}
			if err := os.MkdirAll(s.Store.PackageDir(), 0o700); err != nil {
				fail(w, http.StatusInternalServerError, err.Error())
				return
			}
			tmp = s.pkgPath(name) + ".part"
			f, err := os.Create(tmp)
			if err != nil {
				fail(w, http.StatusInternalServerError, err.Error())
				return
			}
			h := sha256.New()
			pr, pw := io.Pipe()
			scanned := make(chan error, 1)
			go func() { scanned <- scanPackage(pr, kind) }()
			n, cerr := io.Copy(io.MultiWriter(f, h, pw), part)
			pw.Close()
			serr := <-scanned
			f.Close()
			switch {
			case cerr != nil:
				cleanup()
				fail(w, http.StatusBadRequest, "上传中断或超过 8 GB 上限："+cerr.Error())
				return
			case serr != nil:
				cleanup()
				fail(w, http.StatusBadRequest, serr.Error())
				return
			}
			p = model.Package{Name: name, Kind: kind, Ver: ver, Arch: arch, Size: n, SHA: hex.EncodeToString(h.Sum(nil)), At: time.Now(), By: u.Login}
		}
	}
	if p.Name == "" {
		cleanup()
		fail(w, http.StatusBadRequest, "没有收到文件")
		return
	}
	if want != "" {
		if !shaRe.MatchString(want) || want != p.SHA {
			cleanup()
			s.auditSQL(r, u, "deploy.pkg", "上传安装包 "+p.Name+"（SHA256 不匹配）", "", errors.New("SHA256 不匹配"))
			fail(w, http.StatusBadRequest, fmt.Sprintf("SHA256 校验失败：文件实际为 %s", p.SHA))
			return
		}
	}
	err = s.Store.Update(func(st *store.State) error {
		for _, x := range st.Packages {
			if x.Name == p.Name {
				return conflict("同名安装包已存在，请先删除")
			}
		}
		if err := os.Rename(tmp, s.pkgPath(p.Name)); err != nil {
			return err
		}
		tmp = ""
		st.Packages = append(st.Packages, p)
		return nil
	})
	cleanup()
	s.auditSQL(r, u, "deploy.pkg", fmt.Sprintf("上传安装包 %s（%.1f MB，SHA256 %s）", p.Name, float64(p.Size)/1048576, p.SHA[:12]), "", err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, p)
}

func (s *Server) deletePackage(w http.ResponseWriter, r *http.Request, u *model.User) {
	name := r.PathValue("name")
	var found bool
	err := s.Store.Update(func(st *store.State) error {
		for i := range st.Packages {
			if st.Packages[i].Name == name {
				found = true
				st.Packages = append(st.Packages[:i], st.Packages[i+1:]...)
				return nil
			}
		}
		return missing("安装包不存在")
	})
	if found {
		os.Remove(s.pkgPath(name))
	}
	s.auditSQL(r, u, "deploy.pkg", "删除安装包 "+name, "", err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, map[string]bool{"ok": true})
}
