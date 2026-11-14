// @author: kevinkelly
// @license: Apache Licence
// @file: store.go
// @time: 2026/10/28

// Package store 是平台元数据存储：data/state.json（用户、集群、规则配置、定时巡检、忽略项）
// 与 data/reports/<集群>/<报告>.json（巡检报告），审计日志追加写入 data/audit.jsonl。
// 写入采用“临时文件 + rename”保证原子性。第一阶段使用文件存储，后续可替换为 MySQL。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"starrocks-ops/internal/model"
)

type State struct {
	Users       []model.User                  `json:"users"`
	Roles       []model.Role                  `json:"roles"`
	Settings    *model.Settings               `json:"settings,omitempty"`
	Reviews     []model.Review                `json:"reviews,omitempty"`
	Clusters    []model.Cluster               `json:"clusters"`
	Rules       map[string]model.RuleConfig   `json:"rules"`
	Schedules   map[string]model.Schedule     `json:"schedules"` // key: 集群 ID
	Ignores     map[string][]string           `json:"ignores"`   // key: 集群 ID → 规则 ID
	Backups     []model.BackupPolicy          `json:"backupPolicies,omitempty"`
	Params      []model.ParamChange           `json:"paramChanges,omitempty"`
	Alerts      []model.Alert                 `json:"alerts,omitempty"`
	AlertCfg    map[string]model.AlertRuleCfg `json:"alertRules,omitempty"`
	Silences    []model.Silence               `json:"silences,omitempty"`
	Channels    []model.Channel               `json:"channels,omitempty"`
	CustomRules []model.CustomRule            `json:"customRules,omitempty"`
	RepoCreds   []model.RepoCred              `json:"repoCreds,omitempty"`
	NodeBL      []model.NodeBL                `json:"nodeBlacklist,omitempty"`
	Hosts       []model.Host                  `json:"hosts,omitempty"`
	Packages    []model.Package               `json:"packages,omitempty"`
	Upgrades    []model.UpgradePlan           `json:"upgradePlans,omitempty"`
}

type Store struct {
	dir string
	mu  sync.RWMutex
	st  State
}

var ErrNotFound = errors.New("not found")

// Open 打开（或初始化）数据目录。
func Open(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "reports"), filepath.Join(dir, "import")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	s := &Store{dir: dir, st: State{Rules: map[string]model.RuleConfig{}, Schedules: map[string]model.Schedule{}, Ignores: map[string][]string{}}}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if err := json.Unmarshal(b, &s.st); err != nil {
			return nil, fmt.Errorf("state.json 解析失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s.st.Rules == nil {
		s.st.Rules = map[string]model.RuleConfig{}
	}
	if s.st.Schedules == nil {
		s.st.Schedules = map[string]model.Schedule{}
	}
	if s.st.Ignores == nil {
		s.st.Ignores = map[string][]string{}
	}
	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func writeAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) save() error { return writeAtomic(filepath.Join(s.dir, "state.json"), s.st) }

// Update 在写锁内修改状态并持久化。
func (s *Store) Update(fn func(st *State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.st); err != nil {
		return err
	}
	return s.save()
}

// View 在读锁内读取状态（fn 内不要持有返回的切片引用到锁外）。
func (s *Store) View(fn func(st *State)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.st)
}

// ---------- 用户 ----------

func (s *Store) User(login string) (model.User, bool) {
	var u model.User
	var ok bool
	s.View(func(st *State) {
		for _, x := range st.Users {
			if x.Login == login {
				u, ok = x, true
			}
		}
	})
	return u, ok
}

func (s *Store) PutUser(u model.User) error {
	return s.Update(func(st *State) error {
		for i := range st.Users {
			if st.Users[i].Login == u.Login {
				st.Users[i] = u
				return nil
			}
		}
		st.Users = append(st.Users, u)
		return nil
	})
}

// ---------- 集群 ----------

func (s *Store) Clusters() []model.Cluster {
	var out []model.Cluster
	s.View(func(st *State) { out = append(out, st.Clusters...) })
	return out
}

func (s *Store) Cluster(id string) (model.Cluster, error) {
	for _, c := range s.Clusters() {
		if c.ID == id {
			return c, nil
		}
	}
	return model.Cluster{}, ErrNotFound
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`)

// ValidID 集群名称：3–64 位字母、数字、- 和 _。
func ValidID(id string) bool { return idRe.MatchString(id) }

func (s *Store) PutCluster(c model.Cluster, create bool) error {
	return s.Update(func(st *State) error {
		for i := range st.Clusters {
			if st.Clusters[i].ID == c.ID {
				if create {
					return fmt.Errorf("集群名称 %s 已存在", c.ID)
				}
				st.Clusters[i] = c
				return nil
			}
		}
		if !create {
			return ErrNotFound
		}
		st.Clusters = append(st.Clusters, c)
		return nil
	})
}

func (s *Store) DeleteCluster(id string) error {
	err := s.Update(func(st *State) error {
		for i := range st.Clusters {
			if st.Clusters[i].ID == id {
				st.Clusters = append(st.Clusters[:i], st.Clusters[i+1:]...)
				delete(st.Schedules, id)
				delete(st.Ignores, id)
				st.Backups = filterPolicies(st.Backups, id)
				st.Params = filterParams(st.Params, id)
				var al []model.Alert
				for _, a := range st.Alerts {
					if a.Cluster != id {
						al = append(al, a)
					}
				}
				st.Alerts = al
				var rc []model.RepoCred
				for _, x := range st.RepoCreds {
					if x.Cluster != id {
						rc = append(rc, x)
					}
				}
				st.RepoCreds = rc
				var cr []model.CustomRule
				for _, x := range st.CustomRules {
					if x.Cluster != id {
						cr = append(cr, x)
					}
				}
				st.CustomRules = cr
				var sl []model.Silence
				for _, x := range st.Silences {
					if x.Cluster != id {
						sl = append(sl, x)
					}
				}
				st.Silences = sl
				var nb []model.NodeBL
				for _, x := range st.NodeBL {
					if x.Cluster != id {
						nb = append(nb, x)
					}
				}
				st.NodeBL = nb
				var up []model.UpgradePlan
				for _, x := range st.Upgrades {
					if x.Cluster != id {
						up = append(up, x)
					}
				}
				st.Upgrades = up
				for k := range st.Roles { // 角色数据范围中不再引用已移除的集群
					var keep []string
					for _, c := range st.Roles[k].Clusters {
						if c != id {
							keep = append(keep, c)
						}
					}
					st.Roles[k].Clusters = keep
				}
				return nil
			}
		}
		return ErrNotFound
	})
	if err == nil {
		os.RemoveAll(filepath.Join(s.dir, "reports", id))
	}
	return err
}

// ---------- 巡检报告 ----------

func (s *Store) reportDir(cluster string) string { return filepath.Join(s.dir, "reports", cluster) }

func (s *Store) SaveReport(r *model.Report) error {
	d := s.reportDir(r.ClusterID)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(d, r.ID+".json"), r)
}

var reportRe = regexp.MustCompile(`^IR-\d{14}$`)

func (s *Store) Report(cluster, id string) (*model.Report, error) {
	if !reportRe.MatchString(id) || !ValidID(cluster) {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(filepath.Join(s.reportDir(cluster), id+".json"))
	if err != nil {
		return nil, ErrNotFound
	}
	var r model.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Reports 返回报告摘要（新→旧），并删除超过 keep 的旧报告。
func (s *Store) Reports(cluster string, keep time.Duration) []model.ReportMeta {
	ents, _ := os.ReadDir(s.reportDir(cluster))
	var out []model.ReportMeta
	for _, e := range ents {
		id := strings.TrimSuffix(e.Name(), ".json")
		r, err := s.Report(cluster, id)
		if err != nil {
			continue
		}
		if keep > 0 && time.Since(r.At) > keep {
			os.Remove(filepath.Join(s.reportDir(cluster), e.Name()))
			continue
		}
		out = append(out, r.ReportMeta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// ---------- 审计 ----------

type AuditEntry struct {
	At      time.Time `json:"at"`
	User    string    `json:"user"`
	IP      string    `json:"ip"`
	Cluster string    `json:"cluster"`
	Code    string    `json:"code"`
	Act     string    `json:"act"`
	Result  string    `json:"res"`
	SQL     string    `json:"sql,omitempty"` // 变更明细（SQL 或权限增减）
}

var auditMu sync.Mutex

func (s *Store) Audit(e AuditEntry) {
	e.At = time.Now()
	b, _ := json.Marshal(e)
	auditMu.Lock()
	defer auditMu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.dir, "audit.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func filterPolicies(list []model.BackupPolicy, cluster string) []model.BackupPolicy {
	var out []model.BackupPolicy
	for _, p := range list {
		if p.Cluster != cluster {
			out = append(out, p)
		}
	}
	return out
}

func filterParams(list []model.ParamChange, cluster string) []model.ParamChange {
	var out []model.ParamChange
	for _, p := range list {
		if p.Cluster != cluster {
			out = append(out, p)
		}
	}
	return out
}
