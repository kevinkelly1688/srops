// @author: kevinkelly
// @license: Apache Licence
// @file: presets.go
// @time: 2026/10/28

package perm

// Preset 是首次部署时写入的预置角色。
type Preset struct {
	ID, Name, Desc string
	Builtin        bool
	Clusters       []string
	Perms          []string
}

func views(ids ...string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id + ":view"
	}
	return out
}

// btns 返回指定菜单的全部按钮权限码。
func btns(ids ...string) []string {
	var out []string
	for _, g := range Tree {
		for _, m := range g.Menus {
			for _, id := range ids {
				if m.ID == id {
					for _, b := range m.Btns {
						out = append(out, b.Code)
					}
				}
			}
		}
	}
	return out
}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return Dedup(out)
}

// Presets 不含超级管理员（超级管理员是固定的虚拟角色，始终拥有全部权限）。
func Presets() []Preset {
	return []Preset{
		{ID: "r_ops", Name: "运维管理员", Desc: "集群全部运维操作；不能管理平台用户与角色。", Clusters: []string{AllClusters},
			Perms: join(views("overview", "topology", "inspect", "query", "resource", "nodes", "deploy", "upgrade", "backup", "load", "mv", "param", "alert", "audit", "clusters"),
				btns("overview", "topology", "inspect", "query", "resource", "nodes", "deploy", "upgrade", "backup", "load", "mv", "param", "alert"),
				[]string{"audit.sql", "cluster.test", "cluster.edit"})},
		{ID: "r_dba", Name: "DBA", Desc: "查询、资源组、参数、导入、物化视图、备份；节点只读。数据范围需管理员指定集群。", Clusters: []string{},
			Perms: join(views("overview", "topology", "inspect", "query", "resource", "nodes", "backup", "load", "mv", "param", "alert", "audit"),
				btns("overview", "topology", "query", "resource", "load", "mv"),
				[]string{"inspect.run", "inspect.export", "backup.create", "backup.cancel", "backup.repo", "param.history", "param.compare", "param.edit", "alert.ack", "node.log", "audit.sql"})},
		{ID: "r_oncall", Name: "值班只读", Desc: "只看监控、巡检与告警，可认领告警。", Clusters: []string{AllClusters},
			Perms: join(views("overview", "topology", "inspect", "query", "nodes", "load", "mv", "alert"),
				[]string{"topology.detail", "inspect.export", "alert.ack", "query.profile", "load.detail", "node.log"})},
		{ID: "r_audit", Name: "审计员", Desc: "只读审计日志与用户 / 角色配置，便于合规检查。", Builtin: true, Clusters: []string{AllClusters},
			Perms: join(views("overview", "audit", "users", "roles"), []string{"audit.export", "audit.sql"})},
	}
}
