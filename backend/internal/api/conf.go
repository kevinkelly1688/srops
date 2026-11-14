// @author: kevinkelly
// @license: Apache Licence
// @file: conf.go
// @time: 2026/10/28

package api

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"starrocks-ops/internal/sshx"
)

// 通过 SSH 读写节点的 conf/fe.conf、be.conf、cn.conf：参数配置的「持久化」，以及部署、扩容时生成配置。

// confTarget 是一个要写入配置文件的节点。
type confTarget struct{ Kind, Host string }

func uniqStr(xs []string) []string {
	sort.Strings(xs)
	var out []string
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func confPath(dir, kind string) string { return fmt.Sprintf("%s/conf/%s.conf", dir, kind) }

// setConfLine 把配置文本中的 key 设为 value：已有的 key 行就地替换（保留注释与其他行），没有就追加到末尾。
func setConfLine(content, key, value string) string {
	re := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	lines := strings.Split(content, "\n")
	found := false
	for i, ln := range lines {
		if re.MatchString(strings.TrimRight(ln, "\r")) {
			lines[i] = key + " = " + value
			found = true
		}
	}
	if found {
		return strings.Join(lines, "\n")
	}
	content = strings.TrimRight(content, "\n")
	if content != "" {
		content += "\n"
	}
	return content + key + " = " + value + "\n"
}

// confValue 读取配置文本中 key 的值（最后一次出现）；不存在返回 ""，false。
func confValue(content, key string) (string, bool) {
	re := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=\s*(.*?)\s*$`)
	v, ok := "", false
	for _, ln := range strings.Split(content, "\n") {
		if m := re.FindStringSubmatch(strings.TrimRight(ln, "\r")); m != nil {
			v, ok = m[1], true
		}
	}
	return v, ok
}

// writeRemoteFile 以「写临时文件再 mv」的方式原子地替换远端文件。
func writeRemoteFile(ctx context.Context, sess *sshx.Session, path, content string) error {
	res, err := sess.RunStdin(ctx, fmt.Sprintf("cat > %s.tmp && mv %s.tmp %s", path, path, path), strings.NewReader(content))
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("写入 %s 失败：%s", path, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

func readRemoteFile(ctx context.Context, sess *sshx.Session, path string) (string, error) {
	res, err := sess.Run(ctx, "cat "+path)
	if err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("读取 %s 失败：%s", path, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return res.Stdout, nil
}

// setConfOnHost 在一台主机上修改配置项，修改前备份原文件。返回备份文件名。
func (s *Server) setConfOnHost(ctx context.Context, tgt sshx.Target, dir, kind, key, value string) (string, error) {
	sess, err := s.Keys.Dial(ctx, tgt)
	if err != nil {
		return "", err
	}
	defer sess.Close()
	p := confPath(dir, kind)
	old, err := readRemoteFile(ctx, sess, p)
	if err != nil {
		return "", err
	}
	bak := p + ".bak-" + time.Now().Format("20060102150405")
	if res, err := sess.Run(ctx, fmt.Sprintf("cp %s %s", p, bak)); err != nil || res.Code != 0 {
		return "", fmt.Errorf("备份 %s 失败", p)
	}
	if err := writeRemoteFile(ctx, sess, p, setConfLine(old, key, value)); err != nil {
		return "", err
	}
	return bak, nil
}

// persistParam 把参数写入各目标节点的配置文件；某台失败不影响其他台，最后汇总报错。
func (s *Server) persistParam(x srCtx, key, value string, tg []confTarget, stmts *[]string) (string, error) {
	if len(tg) == 0 {
		return "仅内存", fmt.Errorf("没有可写入配置文件的节点")
	}
	seen := map[confTarget]bool{}
	var okN, badN []string
	for _, t := range tg {
		if seen[t] {
			continue
		}
		seen[t] = true
		label := fmt.Sprintf("%s.conf@%s", t.Kind, t.Host)
		tgt, err := s.sshTarget(x.c, t.Host)
		if err != nil {
			return "仅内存", err
		}
		cctx, cancel := context.WithTimeout(x.ctx, 30*time.Second)
		bak, err := s.setConfOnHost(cctx, tgt, nodeDir(x.c, t.Kind), t.Kind, key, value)
		cancel()
		if err != nil {
			badN = append(badN, label+"："+err.Error())
			continue
		}
		okN = append(okN, label)
		*stmts = append(*stmts, fmt.Sprintf("# %s: %s = %s（原文件已备份为 %s）", label, key, value, bak))
	}
	if len(badN) > 0 {
		msg := "写入配置文件失败：" + strings.Join(badN, "；")
		if len(okN) > 0 {
			msg += "（已写入：" + strings.Join(okN, "、") + "）"
		}
		return "部分写入", fmt.Errorf("%s", msg)
	}
	return fmt.Sprintf("已写入 %s（%d 台）", confName(tg), len(okN)), nil
}

func confName(tg []confTarget) string {
	names := map[string]bool{}
	for _, t := range tg {
		names[t.Kind+".conf"] = true
	}
	var out []string
	for n := range names {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, " / ")
}
