# 权限码清单

> 本页由 `tools/build_help.py` 根据 `js/perm.js` 自动生成，请勿手工编辑。● 表示高危操作（执行前需输入集群名确认，可配置双人复核）。

> 例外：**页面水印**的自定义不通过权限码授权，固定只有拥有「超级管理员」角色的用户可以修改。

共 **93** 个权限码。

## 概览

| 菜单 | 权限码 | 权限名称 | 高危 |
|---|---|---|---|
| **集群总览** | `overview:view` | 查看（显示菜单、打开页面） | |
| | `overview.refresh` | 刷新数据 |  |
| | `overview.export` | 导出概览报告 |  |
| **集群架构** | `topology:view` | 查看（显示菜单、打开页面） | |
| | `topology.detail` | 查看节点详情 |  |

## 性能与诊断

| 菜单 | 权限码 | 权限名称 | 高危 |
|---|---|---|---|
| **性能巡检** | `inspect:view` | 查看（显示菜单、打开页面） | |
| | `inspect.run` | 立即巡检 |  |
| | `inspect.schedule` | 定时巡检配置 |  |
| | `inspect.rule` | 巡检规则 / 阈值编辑 |  |
| | `inspect.export` | 导出巡检报告 |  |
| | `inspect.ignore` | 忽略巡检项 |  |
| **查询分析** | `query:view` | 查看（显示菜单、打开页面） | |
| | `query.kill` | 终止查询 (KILL) | ● |
| | `query.profile` | 查看 Query Profile |  |
| | `query.explain` | 执行计划分析 |  |
| | `query.export` | 导出慢查询 |  |
| | `query.blacklist` | SQL 黑名单管理 | ● |
| **资源隔离** | `resource:view` | 查看（显示菜单、打开页面） | |
| | `rg.create` | 新建资源组 |  |
| | `rg.edit` | 编辑资源组 |  |
| | `rg.delete` | 删除资源组 | ● |
| | `queue.edit` | 查询队列配置 |  |

## 集群运维

| 菜单 | 权限码 | 权限名称 | 高危 |
|---|---|---|---|
| **节点管理** | `nodes:view` | 查看（显示菜单、打开页面） | |
| | `node.start` | 启动节点 |  |
| | `node.stop` | 停止节点 | ● |
| | `node.restart` | 重启节点 | ● |
| | `node.scaleout` | 扩容节点 |  |
| | `node.decommission` | 缩容下线 (DECOMMISSION) | ● |
| | `node.drop` | 强制删除 (DROP) | ● |
| | `node.blacklist` | 黑名单管理 |  |
| | `node.log` | 查看节点日志 |  |
| **安装部署** | `deploy:view` | 查看（显示菜单、打开页面） | |
| | `deploy.host` | 主机管理（添加 / 删除主机） |  |
| | `deploy.precheck` | 环境预检 |  |
| | `deploy.fix` | 一键修复环境项 | ● |
| | `deploy.create` | 新建部署任务 |  |
| | `deploy.execute` | 执行部署 | ● |
| | `deploy.pkg` | 安装包管理（上传 / 删除） |  |
| **升级回滚** | `upgrade:view` | 查看（显示菜单、打开页面） | |
| | `upgrade.create` | 创建升级计划 |  |
| | `upgrade.execute` | 执行滚动升级 | ● |
| | `upgrade.pause` | 暂停 / 继续 |  |
| | `upgrade.rollback` | 回滚 / 降级 | ● |
| **备份恢复** | `backup:view` | 查看（显示菜单、打开页面） | |
| | `backup.repo` | 仓库管理 (REPOSITORY) |  |
| | `backup.create` | 发起备份 |  |
| | `backup.restore` | 恢复数据 | ● |
| | `backup.cancel` | 取消作业 |  |
| | `backup.policy` | 备份策略配置 |  |
| | `backup.clean` | 清理备份快照 | ● |
| **导入作业** | `load:view` | 查看（显示菜单、打开页面） | |
| | `load.pause` | 暂停 Routine Load |  |
| | `load.resume` | 恢复 Routine Load |  |
| | `load.stop` | 停止作业 | ● |
| | `load.detail` | 查看错误详情 |  |
| **物化视图** | `mv:view` | 查看（显示菜单、打开页面） | |
| | `mv.refresh` | 手动刷新 |  |
| | `mv.toggle` | 启用 / 停用 |  |
| | `mv.cancel` | 取消刷新任务 |  |
| **参数配置** | `param:view` | 查看（显示菜单、打开页面） | |
| | `param.edit` | 修改参数 | ● |
| | `param.persist` | 持久化到配置文件 |  |
| | `param.history` | 查看变更历史 |  |
| | `param.compare` | 节点参数对比 |  |

## 告警与审计

| 菜单 | 权限码 | 权限名称 | 高危 |
|---|---|---|---|
| **告警中心** | `alert:view` | 查看（显示菜单、打开页面） | |
| | `alert.ack` | 认领 / 处理告警 |  |
| | `alert.silence` | 静默告警 |  |
| | `alert.rule` | 告警规则管理 |  |
| | `alert.channel` | 通知渠道管理 |  |
| **操作审计** | `audit:view` | 查看（显示菜单、打开页面） | |
| | `audit.export` | 导出审计日志 |  |
| | `audit.sql` | 查看 SQL 审计 |  |

## 平台配置

| 菜单 | 权限码 | 权限名称 | 高危 |
|---|---|---|---|
| **用户管理** | `users:view` | 查看（显示菜单、打开页面） | |
| | `user.create` | 新建用户 |  |
| | `user.edit` | 编辑用户 |  |
| | `user.delete` | 删除用户 | ● |
| | `user.reset` | 重置密码 |  |
| | `user.lock` | 锁定 / 解锁 |  |
| **角色权限** | `roles:view` | 查看（显示菜单、打开页面） | |
| | `role.create` | 新建角色 |  |
| | `role.edit` | 编辑角色权限 |  |
| | `role.delete` | 删除角色 | ● |
| | `role.copy` | 复制角色 |  |
| **集群接入** | `clusters:view` | 查看（显示菜单、打开页面） | |
| | `cluster.add` | 导入集群 |  |
| | `cluster.edit` | 编辑连接 |  |
| | `cluster.delete` | 移除集群 | ● |
| | `cluster.test` | 连通性测试 |  |
| **平台设置** | `settings:view` | 查看（显示菜单、打开页面） | |
| | `setting.security` | 安全策略 |  |
| | `setting.notify` | 通知设置 |  |
| | `setting.basic` | 基础设置 |  |

## 帮助

| 菜单 | 权限码 | 权限名称 | 高危 |
|---|---|---|---|
| **使用手册** | `help:view` | 查看（显示菜单、打开页面） | |
