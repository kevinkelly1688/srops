// @author: kevinkelly
// @license: Apache Licence
// @file: packages_import.go
// @time: 2026/10/28

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"starrocks-ops/internal/model"
	"starrocks-ops/internal/store"
)

// 从服务器导入安装包：安装包已经在服务器上（例如直接用 wget 下载好）时，不必再经浏览器上传。
// 平台只处理两个固定目录里的文件，请求里只带文件名，不接受任意路径：
//   - <数据目录>/import/    推荐：把包放在这里，导入时校验后“移动”到 packages/（同一磁盘上是瞬间完成的重命名）；
//   - <数据目录>/packages/  已经手工放进去、但还没有登记的文件，原地登记。
// 校验与上传一致：文件名格式、压缩包内容、SHA256（可选核对）。

const importFreshWindow = 15 * time.Second // 最近有写入的文件视为仍在下载

type importCand struct {
	Name    string    `json:"name"`
	Where   string    `json:"where"` // import / packages
	Kind    string    `json:"kind"`
	Ver     string    `json:"ver"`
	Arch    string    `json:"arch"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Fresh   bool      `json:"fresh"` // 最近仍在写入
}

func (s *Server) importDir() string { return s.Store.PackageImportDir() }

func (s *Server) importSrc(where, name string) (string, error) {
	switch where {
	case "import":
		return filepath.Join(s.importDir(), name), nil
	case "packages":
		return s.pkgPath(name), nil
	}
	return "", bad("where 须为 import 或 packages")
}

// GET /api/packages/import：列出两个目录里可以导入的文件。
func (s *Server) listImportable(w http.ResponseWriter, r *http.Request, u *model.User) {
	if err := os.MkdirAll(s.importDir(), 0o700); err != nil {
		fail(w, http.StatusInternalServerError, "无法创建导入目录："+err.Error())
		return
	}
	registered := map[string]bool{}
	s.Store.View(func(st *store.State) {
		for _, p := range st.Packages {
			registered[p.Name] = true
		}
	})
	items, ignored := []importCand{}, []string{}
	now := time.Now()
	for _, where := range []string{"import", "packages"} {
		dir := s.importDir()
		if where == "packages" {
			dir = s.Store.PackageDir()
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			fi, err := os.Lstat(filepath.Join(dir, e.Name()))
			if err != nil || !fi.Mode().IsRegular() || strings.HasSuffix(e.Name(), ".part") {
				continue
			}
			kind, ver, arch, ok2 := parsePackageName(e.Name())
			switch {
			case !ok2:
				if where == "import" {
					ignored = append(ignored, e.Name())
				}
			case where == "packages" && registered[e.Name()]:
				// 已登记的正式文件
			default:
				items = append(items, importCand{Name: e.Name(), Where: where, Kind: kind, Ver: ver, Arch: arch, Size: fi.Size(), ModTime: fi.ModTime(), Fresh: now.Sub(fi.ModTime()) < importFreshWindow})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ModTime.After(items[j].ModTime) })
	sort.Strings(ignored)
	ok(w, map[string]any{"dir": s.importDir(), "items": items, "ignored": ignored})
}

// POST /api/packages/import：{"name": "...", "where": "import|packages", "sha256": "可选"}
func (s *Server) importPackage(w http.ResponseWriter, r *http.Request, u *model.User) {
	var in struct {
		Name   string `json:"name"`
		Where  string `json:"where"`
		SHA256 string `json:"sha256"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Where == "" {
		in.Where = "import"
	}
	in.SHA256 = strings.ToLower(strings.TrimSpace(in.SHA256))
	var p model.Package
	err := func() error {
		switch {
		case in.Name == "" || in.Name != filepath.Base(in.Name) || strings.ContainsAny(in.Name, `/\`) || strings.HasPrefix(in.Name, "."):
			return bad("只能导入导入目录里的文件，请只填写文件名")
		case in.SHA256 != "" && !shaRe.MatchString(in.SHA256):
			return bad("SHA256 应为 64 位十六进制")
		}
		kind, ver, arch, okName := parsePackageName(in.Name)
		if !okName {
			return bad("文件名不符合要求：StarRocks 包应为 StarRocks-<版本>-<系统>-<amd64|arm64>.tar.gz，JDK 包应为 jdk*.tar.gz")
		}
		src, err := s.importSrc(in.Where, in.Name)
		if err != nil {
			return err
		}
		if _, dup := s.packageByName(in.Name); dup {
			return conflict("同名安装包已存在，请先删除")
		}
		fi, err := os.Lstat(src)
		switch {
		case os.IsNotExist(err):
			return missing("文件不存在：" + src)
		case err != nil:
			return err
		case !fi.Mode().IsRegular():
			return bad("不是普通文件（不接受符号链接和目录）")
		case fi.Size() > maxPackageSize:
			return bad("文件超过 8 GB 上限")
		case time.Since(fi.ModTime()) < importFreshWindow:
			return conflict("文件最近 15 秒内仍有写入，可能还在下载，请下载完成后再导入")
		}
		if !s.lockNode("", "pkg", in.Name) {
			return conflict("该安装包正在导入")
		}
		defer s.unlockNode("", "pkg", in.Name)

		f, err := os.Open(src)
		if err != nil {
			if os.IsPermission(err) {
				return apiErr{http.StatusForbidden, fmt.Sprintf("平台服务没有读取该文件的权限：请执行 chmod 644 %s，并确认平台用户可以访问它所在的目录", src)}
			}
			return err
		}
		defer f.Close() // 校验结束后会提前关闭（Windows 上不能移动仍打开的文件），defer 只是兜底
		h := sha256.New()
		pr, pw := io.Pipe()
		scanned := make(chan error, 1)
		go func() { scanned <- scanPackage(pr, kind) }()
		n, cerr := io.Copy(io.MultiWriter(h, pw), f)
		pw.Close()
		f.Close()
		if serr := <-scanned; serr != nil {
			return bad(serr.Error())
		}
		if cerr != nil {
			return fmt.Errorf("读取文件失败：%v", cerr)
		}
		sum := hex.EncodeToString(h.Sum(nil))
		if in.SHA256 != "" && in.SHA256 != sum {
			return bad(fmt.Sprintf("SHA256 校验失败：文件实际为 %s", sum))
		}
		p = model.Package{Name: in.Name, Kind: kind, Ver: ver, Arch: arch, Size: n, SHA: sum, At: time.Now(), By: u.Login}
		return s.Store.Update(func(st *store.State) error {
			for _, x := range st.Packages {
				if x.Name == p.Name {
					return conflict("同名安装包已存在，请先删除")
				}
			}
			if in.Where == "import" {
				if err := os.MkdirAll(s.Store.PackageDir(), 0o700); err != nil {
					return err
				}
				if err := moveFile(src, s.pkgPath(p.Name)); err != nil {
					return fmt.Errorf("移动文件到安装包目录失败：%v", err)
				}
			}
			st.Packages = append(st.Packages, p)
			return nil
		})
	}()
	act := fmt.Sprintf("从服务器导入安装包 %s（%s）", in.Name, map[string]string{"import": "导入目录", "packages": "安装包目录原地登记"}[in.Where])
	if err == nil {
		act = fmt.Sprintf("%s（%.1f MB，SHA256 %s）", act, float64(p.Size)/1048576, p.SHA[:12])
	}
	s.auditSQL(r, u, "deploy.pkg", act, "", err)
	if err != nil {
		writeErr(w, err)
		return
	}
	ok(w, p)
}

// moveFile 先尝试重命名（同一文件系统内瞬间完成），失败（跨磁盘）时复制后删除原文件。
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Remove(src); err != nil {
		return errors.New("已复制，但无法删除原文件：" + err.Error())
	}
	return nil
}
