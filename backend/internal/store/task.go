// @author: kevinkelly
// @license: Apache Licence
// @file: task.go
// @time: 2026/10/28

package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"starrocks-ops/internal/model"
)

// 后台任务的日志可能很长，单独保存为 data/tasks/<任务号>.json，不放进 state.json。

var taskIDRe = regexp.MustCompile(`^T-\d{14}-[0-9a-f]{6}$`)

func (s *Store) taskDir() string { return filepath.Join(s.dir, "tasks") }

// SaveTask 写入任务文件。
func (s *Store) SaveTask(t model.Task) error {
	if !taskIDRe.MatchString(t.ID) {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(s.taskDir(), 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.taskDir(), t.ID+".json"), t)
}

// LoadTasks 读取全部任务（新→旧）。
func (s *Store) LoadTasks() []model.Task {
	ents, _ := os.ReadDir(s.taskDir())
	var out []model.Task
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(s.taskDir(), e.Name()))
		if err != nil {
			continue
		}
		var t model.Task
		if json.Unmarshal(b, &t) == nil && taskIDRe.MatchString(t.ID) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out
}

// DeleteTask 删除任务文件。
func (s *Store) DeleteTask(id string) {
	if taskIDRe.MatchString(id) {
		os.Remove(filepath.Join(s.taskDir(), id+".json"))
	}
}

// PackageDir 是上传的安装包目录。
func (s *Store) PackageDir() string { return filepath.Join(s.dir, "packages") }

// PackageImportDir 是“从服务器导入”的中转目录：把安装包放在这里，再在页面上导入登记。
func (s *Store) PackageImportDir() string { return filepath.Join(s.dir, "import") }
