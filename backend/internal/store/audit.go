// @author: kevinkelly
// @license: Apache Licence
// @file: audit.go
// @time: 2026/10/28

package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"starrocks-ops/internal/model"
)

// Settings 返回平台设置；尚未保存过时返回默认值。
func (s *Store) Settings() model.Settings {
	var out model.Settings
	s.View(func(st *State) {
		if st.Settings != nil {
			out = *st.Settings
		} else {
			out = model.DefaultSettings()
		}
	})
	return out
}

// ResultClass 把审计结果归类：成功 / 失败 / 拒绝 / 待复核。
func ResultClass(res string) string {
	switch {
	case strings.HasPrefix(res, "失败"):
		return "失败"
	case strings.HasPrefix(res, "拒绝"):
		return "拒绝"
	case strings.HasPrefix(res, "待复核"):
		return "待复核"
	}
	return "成功"
}

// AuditFilter 是审计查询条件，空字段不过滤。
type AuditFilter struct {
	User, Cluster, Code, Result, Q string
	From, To                       time.Time
	Visible                        func(AuditEntry) bool // 数据范围过滤
}

func (f AuditFilter) match(e AuditEntry) bool {
	switch {
	case f.User != "" && e.User != f.User:
		return false
	case f.Cluster != "" && e.Cluster != f.Cluster:
		return false
	case f.Code != "" && e.Code != f.Code:
		return false
	case f.Result != "" && ResultClass(e.Result) != f.Result:
		return false
	case !f.From.IsZero() && e.At.Before(f.From):
		return false
	case !f.To.IsZero() && !e.At.Before(f.To):
		return false
	case f.Q != "" && !strings.Contains(strings.ToLower(e.User+" "+e.Code+" "+e.Act+" "+e.SQL+" "+e.Cluster), strings.ToLower(f.Q)):
		return false
	}
	return f.Visible == nil || f.Visible(e)
}

// QueryAudit 按时间倒序返回匹配的审计记录：跳过前 offset 条，最多 limit 条；more 表示后面还有。
func (s *Store) QueryAudit(f AuditFilter, offset, limit int) (list []AuditEntry, more bool, err error) {
	fh, err := os.Open(filepath.Join(s.dir, "audit.jsonl"))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer fh.Close()
	skipped := 0
	err = reverseLines(fh, func(line []byte) bool {
		var e AuditEntry
		if json.Unmarshal(line, &e) != nil || !f.match(e) {
			return true
		}
		if skipped < offset {
			skipped++
			return true
		}
		if len(list) >= limit {
			more = true
			return false
		}
		list = append(list, e)
		return true
	})
	return
}

// reverseLines 从文件末尾向前逐行回调（空行跳过）；fn 返回 false 时停止。
func reverseLines(f *os.File, fn func(line []byte) bool) error {
	const block = 64 << 10
	st, err := f.Stat()
	if err != nil {
		return err
	}
	pos := st.Size()
	var carry []byte
	buf := make([]byte, block)
	for pos > 0 {
		n := int64(block)
		if pos < n {
			n = pos
		}
		pos -= n
		if _, err := f.ReadAt(buf[:n], pos); err != nil && err != io.EOF {
			return err
		}
		data := append(append([]byte(nil), buf[:n]...), carry...)
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			if line := bytes.TrimSpace(data[i+1:]); len(line) > 0 && !fn(line) {
				return nil
			}
			data = data[:i]
		}
		carry = data
	}
	if line := bytes.TrimSpace(carry); len(line) > 0 {
		fn(line)
	}
	return nil
}

// PruneAudit 删除早于 before 的审计记录，返回删除条数。保留期内的记录不会被删除。
func (s *Store) PruneAudit(before time.Time) (int, error) {
	auditMu.Lock()
	defer auditMu.Unlock()
	path := filepath.Join(s.dir, "audit.jsonl")
	in, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		in.Close()
		return 0, err
	}
	w := bufio.NewWriter(out)
	rd := bufio.NewReaderSize(in, 1<<20)
	dropped := 0
	for {
		line, rerr := rd.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			var e AuditEntry
			if json.Unmarshal(t, &e) == nil && e.At.Before(before) {
				dropped++
			} else {
				w.Write(t)
				w.WriteByte('\n')
			}
		}
		if rerr != nil {
			break
		}
	}
	in.Close()
	if err := w.Flush(); err != nil {
		out.Close()
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	if dropped == 0 {
		os.Remove(tmp)
		return 0, nil
	}
	return dropped, os.Rename(tmp, path)
}
