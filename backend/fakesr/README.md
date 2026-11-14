# fakesr — 假 StarRocks 集群

用于本地开发/测试 StarRocks 运维平台。一个进程模拟整个集群：

- MySQL 协议查询端口（只响应平台用到的固定查询）
- 每个 FE/BE/CN 节点一个 HTTP `/metrics`（Prometheus 文本格式）
- 一个 SSH 服务（只支持 `exec`，返回固定的主机检查结果）

独立 Go module（`starrocks-ops/fakesr`），不依赖主工程。

## 运行

```bash
cd backend/fakesr
GOTOOLCHAIN=local go run ./cmd/fakesr -scenario problems
GOTOOLCHAIN=local go run ./cmd/fakesr -mode shared-data -scenario healthy -v
GOTOOLCHAIN=local go test ./...
```

平台 DSN：`sr_ops:secret@tcp(127.0.0.1:19030)/?timeout=5s&readTimeout=30s`

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `-host` | `127.0.0.1` | 监听地址，同时是各节点对外的 IP |
| `-query-port` | `19030` | MySQL 协议端口 |
| `-user` / `-password` | `sr_ops` / `secret` | 其它账号返回 1045 Access denied；`-password` 也是 SSH 密码 |
| `-mode` | `shared-nothing` | `shared-nothing` 或 `shared-data` |
| `-scenario` | `problems` | `healthy` 或 `problems` |
| `-ssh-port` | `12222` | SSH 端口，`0` 关闭 |
| `-version` | `3.5.6` | shared-data 下未显式指定时为 `4.0.2` |
| `-hosts` | false | 主机模式：每个节点使用独立的回环地址（`127.0.0.11` 起）和独立的 SSH 主机，见下文。需要固定的 `-ssh-port` |
| `-spare` | 0 | 主机模式下额外的空白主机数量（`127.0.0.50` 起），用于演示部署 / 扩容 |
| `-fe-dir` / `-be-dir` | `/opt/starrocks/fe` / `/opt/starrocks/be` | 主机模式下 FE、BE / CN 的安装目录 |
| `-s3-port` | -1 | 启动内存 S3 服务（0 = 随机端口，-1 = 关闭），保存备份仓库里的快照文件，见下文 |
| `-s3-access-key` | `fakeak` | S3 服务接受的访问密钥 ID（密钥任意） |
| `-v` | false | 打印每条查询 / SSH 命令 |

## 主机模式（`-hosts`）

每台主机（节点所在地址与空白主机）在自己的回环地址上监听 SSH（同一个端口），并模拟：

- **虚拟文件系统**：安装目录、`conf/*.conf`、`lib/*.jar`（内容为 `version=<版本>`）、元数据 / 数据目录、`log/` 下的样例日志；
- **进程**：`bin/start_{fe,be,cn}.sh --daemon`（可带 `--helper`）、`stop_*.sh [-g]`，状态与 `SHOW FRONTENDS / BACKENDS` 的 Alive 联动；停止 Leader FE 时 Follower 自动接任；启动时读取 `lib` 下 jar 的版本，所以替换 `lib` 后节点版本随之改变（升级 / 回滚）；
- **内核参数与 ulimit**：空白主机初始为“不达标”，`sudo tee / sysctl -p / swapoff` 修复后变化；
- **安装命令**：`mkdir`、`cp -a`、`mv`、`rm -rf`、`tar -xzf --strip-components=1`（真的解压上传的 tar.gz）、`stat`、`sha256sum`、`cat > file`、`test`；
- **SSH 公钥**：首次可用密码登录，写入 `authorized_keys` 的公钥之后才能用密钥登录；
- **动态节点**：空白主机上启动没有 `--helper` 的 FE，会创建一个新的空集群（root 初始密码为空，`ALTER USER root IDENTIFIED BY` 生效）；`ALTER SYSTEM ADD FOLLOWER / OBSERVER / BACKEND / COMPUTE NODE` 把主机上已安装的节点加入集群，`DECOMMISSION / DROP / BLACKLIST` 语句同样支持。

**演示用安装包**：真实的 StarRocks 安装包在假主机上无法启动（假主机靠 `lib/starrocks-<fe|be|cn>.jar` 里的 `version=<版本>` 判断节点版本）。`demo-pkgs/` 里已有生成好的 3.5.6 / 3.5.8 假安装包和一个假 JDK 包，直接在「安装部署 → 安装包」上传即可；需要其他版本时：

```bash
go run ./cmd/mkpkg -version 3.6.0 -out demo-pkgs [-arch arm64] [-jdk=false]
```

平台下发的命令是固定写法，这里按这些写法逐条模拟；没见过的命令返回 127。这只能验证“命令顺序与流程”，**不能代替真实主机上的验证**。

## 内存 S3（`-s3-port`）

启用后 fakesr 同时提供一个路径风格的 S3 服务（只支持按前缀列出 `list-type=2` 与批量删除 `POST ?delete`，要求带 SigV4 的 `Authorization` 头，访问密钥 ID 为 `-s3-access-key`）。仓库 `repo_s3_prod`（`s3a://sr-backup/prod-sr-01`）的快照在其中有文件，目录布局与 StarRocks 一致：

```
prod-sr-01/__starrocks_repository_repo_s3_prod/__ss_<快照>/…
```

`SHOW SNAPSHOT` 只列出对象存储里仍有文件的快照：平台删除快照目录后，快照随之消失；备份完成后会生成新快照的文件。用于演示和测试「备份快照清理」。代码里可用 `AddSnapshot(repo, name, 时间)` 加入任意时间的快照。

## 拓扑与端口

| 节点 | shared-nothing | shared-data | HTTP 端口 | 其它端口（仅展示，不监听） |
|---|---|---|---|---|
| FE | 1 LEADER + 2 FOLLOWER + 1 OBSERVER | 1 LEADER + 2 FOLLOWER | 18030+i | edit_log 19010+i，rpc 19020+i，query 19030 |
| BE | 6 | 0 | 18040+i | heartbeat 19050+i，be_port 19060+i，brpc 18060+i |
| CN | 2 | 4 | 18050+i | heartbeat 19070+i，be_port 19080+i，brpc 18070+i，starlet 19090+i |

## 场景差异（problems 相对 healthy）

- OBSERVER 回放落后 1500 条 journal；Leader JVM 堆使用 82%
- BE3 CPU 92%、cumulative compaction score 186；BE4 MaxDiskUsedPct 87.12%
- `/statistic` 共 12 个不健康 tablet
- 物化视图：1 个 FAILED（Memory of query exceed limit）、1 个 inactive
- Routine Load：1 个 PAUSED（offset out of range: partition 7）
- partitions_meta：4 张表单副本、6 张表单桶数据 < 100MB 且分桶 ≥ 16；shared-data 下部分 MAX_CS > 100
- 资源组未设置 big_query 限制；`enable_query_queue_select=false`
- 慢查询 143 条（healthy 12）；CANCELLED 导入 17（healthy 2）
- 权限缺 `REPOSITORY`
- FE 错误率 0.4%（healthy 0.05%），p99 约 2800ms（healthy 约 900ms）
- SSH：THP `[always]`、`max_map_count=65530`

## 支持的查询

大小写不敏感，忽略首尾空白、末尾 `;` 和反引号。其余查询返回 `1064 fake starrocks: unsupported query: ...`。

- `SELECT 1`、`SET ...`、`USE ...`、COM_PING、`SELECT @@version_comment`
- `SELECT current_version()`
- `SHOW FRONTENDS` / `SHOW BACKENDS` / `SHOW COMPUTE NODES`
- `ADMIN SHOW FRONTEND CONFIG [LIKE '...']`（`run_mode`、`default_replication_num`、`qe_slow_log_ms`）
- `SHOW GRANTS`、`SHOW CATALOGS`、`SHOW PROC '/statistic'`、`SHOW RESOURCE GROUPS [ALL]`
- `SHOW VARIABLES [LIKE '...']`
- `SELECT ... FROM information_schema.<表>`，按 `FROM` 后的表名匹配（WHERE 等忽略）：
  `schemata`、`tables`、`materialized_views`、`routine_load_jobs`、`partitions_meta`、`loads`
- `SELECT COUNT(*) FROM starrocks_audit_db__.starrocks_audit_tbl__ ...`

## /metrics

- FE：`starrocks_fe_qps`、`starrocks_fe_query_total`、`starrocks_fe_query_err`、
  `starrocks_fe_query_latency_ms{quantile}`、`starrocks_fe_connection_total`、`jvm_heap_size_bytes{type}`、
  （shared-data）`starrocks_fe_max_tablet_compaction_score`
- BE：`starrocks_be_tablet_cumulative_max_compaction_score`、`starrocks_be_tablet_base_max_compaction_score`、
  `starrocks_be_process_mem_bytes`
- CN：`starrocks_be_process_mem_bytes`

## SSH

用户 `starrocks`，密码同 `-password` 或任意公钥；每次启动生成临时 host key。支持的命令：

- `cat /proc/sys/vm/swappiness` / `overcommit_memory` / `max_map_count`
- `cat /sys/kernel/mm/transparent_hugepage/enabled`
- `date +%s%3N`
- `test -x <path>/bin/start_{fe,be,cn}.sh && echo ok`

其它命令输出 `command not found`，退出码 127。

## 代码中启动

```go
cfg := DefaultConfig()
cfg.QueryPort, cfg.SSHPort = 0, 0                     // 0 = 随机端口
cfg.FEHTTPBase, cfg.BEHTTPBase, cfg.CNHTTPBase = 0, 0, 0
c, err := Start(cfg)
defer c.Stop()
// c.QueryAddr()、c.SSHAddr()、c.FEHTTPAddrs() ...
```
