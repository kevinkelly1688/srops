# 节点管理

按 FE / BE / CN 分页签管理集群节点（存算分离集群没有 BE 页签），另有 **黑名单**、**操作记录** 两个页签。

> **运维必填信息**：启动 / 停止 / 重启 / 日志 / 扩容需要通过 SSH 登录节点主机，要求集群的 SSH 凭据与部署目录已填写（见「集群接入」）。没有填写的集群，这些按钮置灰。下线、移除、黑名单只用 SQL，但同样遵循“待完善集群只读”的规则。

## 1. 节点列表

数据来自 `SHOW FRONTENDS / BACKENDS / COMPUTE NODES` 与节点 `/metrics`，每行显示地址、状态、版本、CPU / 内存 / 磁盘水位，以及：

- **FE**：角色（Leader / Follower / Observer）、JVM 堆、Journal 落后条数。
- **BE**：Tablet 数、数据量、Compaction Score。
- **CN**：Warehouse、运行中的查询数。

地址的写法与 `ALTER SYSTEM` 语句一致：FE 为 `主机:edit_log_port`，BE / CN 为 `主机:heartbeat_service_port`。点击地址打开节点详情。

## 2. 操作一览

| 按钮 | 权限码 | 高危 | 实际执行 |
|---|---|---|---|
| 启动 | `node.start` | | SSH：`cd <部署目录> && bin/start_<fe/be/cn>.sh --daemon` |
| 停止 | `node.stop` | ● | SSH：`bin/stop_<..>.sh`；BE / CN（3.4 及以上）默认带 `-g` 优雅退出 |
| 重启 | `node.restart` | ● | 先停止，再启动，等待节点重新存活 |
| 下线（BE） | `node.decommission` | ● | `ALTER SYSTEM DECOMMISSION BACKEND "ip:9050"` |
| 下线（CN） | `node.decommission` | ● | `ALTER SYSTEM DROP COMPUTE NODE "ip:9050"`（CN 无状态，直接移除） |
| 移除 / 强制删除 | `node.drop` | ● | FE：`ALTER SYSTEM DROP FOLLOWER/OBSERVER "ip:9010"`；BE：`DROP BACKEND`；CN：`DROP COMPUTE NODE` |
| 拉黑 / 移出 | `node.blacklist` | | `ADD / DELETE BACKEND BLACKLIST <id>`（CN 为 `COMPUTE NODE`） |
| 日志 | `node.log` | | SSH：`tail` 节点日志文件 |
| ＋ 扩容 | `node.scaleout` | | 见第 4 节 |

> ⚠ 高危操作需要输入集群名确认，开启双人复核时先提交复核，批准后以申请人身份执行。Leader FE 不提供“移除”按钮；需要更换 Leader 时，先停止 Leader，让 Follower 自动选主。

## 3. 启动 / 停止 / 重启

这三个操作在服务端以**后台任务**执行：点击后立即打开任务日志，可以随时关闭，回到「操作记录」页签再看。日志里有每条命令及其输出。

- **启动**：已经在运行则跳过启动脚本；否则执行 `start_*.sh --daemon`，然后每隔几秒检查“进程在运行 **并且** `SHOW` 里该节点 Alive = true”，最长等待 180 秒。进程在跑但节点一直不存活时任务失败，日志指向节点日志。
- **停止**：弹窗里可选 **优雅退出（`-g`）**：BE / CN 会等待运行中的查询完成再退出（3.4 及以上默认勾选，较慢）。停止后平台确认进程已经退出。
- **FE 多数派保护**：停止 / 重启某个 Leader / Follower 后，存活的 Leader + Follower 数量不足多数时，平台拒绝执行（FE 将无法选主）。确需操作可勾选 **强制**。
- **同一节点同一时间只能有一个操作**，重复提交会提示正在执行。
- 失败的操作不会自动重试。启动脚本不存在、目录不对等错误会原样写在日志里。

## 4. 扩容

1. 先在「安装部署 → 主机管理」添加目标主机，并在「安装包」上传与集群**相同版本**的安装包。
2. 进入对应页签（FE / BE / CN），点击 **＋ 扩容**，选择目标主机（SSH 已连通、不是该集群同类型节点的主机）和安装包。扩容 FE 需选择角色：Observer（不参与选主，扩展读能力）或 Follower。
3. 点击 **环境预检**：检查内核参数、端口占用、目录可写、磁盘空间、JDK 等（只读），不通过项修复后才能开始。
4. 点击 **开始扩容**。平台依次执行：上传安装包并校验 SHA256 → 解压 → 把 bin / lib / conf 复制到集群约定的安装目录 → 沿用集群里同类型节点的配置文件（只改本机的 `priority_networks`）→ 创建元数据 / 数据目录 → 加入集群。

加入集群的顺序遵循官方流程：

```
# FE：先 ADD，再用 --helper 首次启动
ALTER SYSTEM ADD OBSERVER "<新主机>:9010";
bin/start_fe.sh --helper <leader>:9010 --daemon
# BE / CN：先启动，再 ADD
bin/start_be.sh --daemon
ALTER SYSTEM ADD BACKEND "<新主机>:9050";
```

要点：

- **新主机必须能用该集群配置的 SSH 信息登录**（用户、端口、认证方式）。扩容之后节点管理、巡检、升级都用这组信息运维，所以平台在开始前就会验证，不通过则拒绝。
- 新节点的 `JAVA_HOME`、数据目录沿用参考节点的路径：这些路径在新主机上必须存在（预检的 JDK 项会检查 `JAVA_HOME`）。
- BE 加入后 FE 自动均衡 Tablet，可用 `SHOW PROC '/cluster_balance'` 观察。
- 扩容任务出现在「操作记录」页签中。

## 5. 缩容（下线）

**BE 一定优先使用「下线」（DECOMMISSION）**：

1. 平台先估算下线后剩余 BE 的平均磁盘使用率，确认空间足够。
2. 执行后节点状态变为“下线中”，列表显示剩余 Tablet 数；迁移完成后 FE 自动移除该节点。
3. 剩余 BE 数量不足副本数时，StarRocks 会拒绝下线。

> ⚠ 不要对仍有数据的 BE 使用 DROP：DROP 会直接移除节点，副本数不足的数据可能丢失。只有节点已彻底损坏无法恢复时才使用。

FE 移除注意：移除 Follower 后剩余 Leader + Follower 应为**奇数**，否则影响选主可用性。

## 6. 黑名单

黑名单中的 BE / CN **不参与查询调度**，但仍承载数据副本和导入。适用于某节点磁盘 IO 抖动、网络异常时临时隔离查询流量。

- 加入时必须填写原因（StarRocks 本身不保存原因，由平台记录；通过平台以外的方式加入的显示“—”）。
- 列表显示节点、类型、来源（手动 / 自动，自动指 FE 因连接失败自动拉黑）、操作人和时间。
- 问题解决后点击 **移出**。

## 7. 节点日志

**日志** 在抽屉中读取节点日志文件末尾，可选择 200 / 500 / 1000 行，文件限定为：

| 节点 | 可选文件 |
|---|---|
| FE | `fe.log`、`fe.warn.log`、`fe.audit.log`、`fe.out` |
| BE | `be.INFO`、`be.WARNING`、`be.out` |
| CN | `cn.INFO`、`cn.WARNING`、`cn.out` |

输入关键字时，平台在最近 5000 行里过滤（不区分大小写）。每次查看都会写入审计。

## 8. 操作记录

列出该集群上通过平台执行的节点启停、扩容任务：状态、发起人、耗时，点击 **日志** 查看完整输出。任务日志保存在平台的 `data/tasks/`，平台重启后仍可查看；重启时仍在执行的任务会被标记为“失败：平台重启，任务被中断”。
