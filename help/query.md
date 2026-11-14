# 查询分析 / 资源隔离

## 查询分析

### 运行中查询

读取 `SHOW PROC '/global_current_queries'`（全部 FE 汇总；3.1 及更早版本没有该视图时，平台改为逐台 FE 读取 `/current_queries`），并用 `SHOW FULL PROCESSLIST` 补充每个查询正在执行的 SQL，显示用户、库、资源组、所在 FE、已运行时长、扫描量、内存、CPU 时间。运行超过 60 秒的标红。可勾选“每 5 秒自动刷新”。

| 按钮 | 权限码 | 说明 |
|---|---|---|
| Profile | `query.profile` | 查看该查询的 Query Profile |
| 终止 | `query.kill` · 高危 | 到查询所在的 FE 上执行 `KILL QUERY <连接 ID>`，只终止查询，不断开连接；客户端会收到错误 |

> 终止前平台会**重新读取**运行中的查询，确认 Query ID 与连接仍然对应；如果查询已经结束，会提示“已结束”，不会误杀该连接上后来执行的其他查询。

### 慢查询 Top SQL

按 **SQL 指纹（Digest）** 聚合审计表，按总耗时排序，最多 50 条；每行展示次数、平均 / 最大耗时、扫描行数、峰值内存，以及耗时最长的一次执行的 SQL。可按时间范围（1 小时 / 24 小时 / 7 天 / 30 天）、最小耗时、用户、SQL 关键字筛选。

> 前提：集群已安装 **AuditLoader** 插件，把 `fe.audit.log` 写入 `starrocks_audit_db__.starrocks_audit_tbl__`，且平台账号有读取权限。未开启 SQL Digest 的记录不会聚合（每条单独一行）。慢查询阈值由 FE 参数 `qe_slow_log_ms` 控制。提示“读取审计表失败”时的处理方法见 [常见问题](#help/faq)。

- **Profile**（`query.profile`）：读取耗时最长那次执行的 Query Profile。**Profile 只保留在 FE 内存中**，且需要提前开启 `enable_profile`，历史查询可能已经取不到。
- **执行计划**（`query.explain`）：打开可编辑的 SQL 框，选择 `EXPLAIN` 或 `EXPLAIN COSTS` 后分析。**只支持单条 SELECT / WITH 查询，只做 EXPLAIN，不会真正运行该 SQL**。
- **⇩ 导出 CSV**（`query.export`）：按当前筛选条件导出（最多 200 条，防公式注入），导出本身记入审计。

### 读懂 Query Profile

Profile 抽屉分两部分：**分析结论**（`ANALYZE PROFILE FROM '<query_id>'` 的输出，当前版本不支持时不显示）和**原始 Profile**（`get_query_profile`，可复制）。

| 关注点 | 含义 | 常见优化 |
|---|---|---|
| OLAP_SCAN 耗时高 | 扫描数据多 | 加分区 / 前缀索引过滤条件、物化视图预聚合 |
| partitions=279/280 | 几乎扫描了全部分区 | 检查 WHERE 是否带分区列 |
| HASH_JOIN (SHUFFLE) | 两边都按 Key 重分布 | 小表改 BROADCAST；同分桶表用 Colocate Join |
| EXCHANGE 耗时高 | 网络传输多 | 同上，减少 Shuffle |
| AGGREGATE 行数大 | 高基数聚合 / 精确去重 | bitmap / HLL、物化视图 |

手动获取 Profile：

```
SET enable_profile = true;
SHOW PROFILELIST;
ANALYZE PROFILE FROM '<query_id>';
```

### SQL 黑名单

命中黑名单正则的 SQL 直接被拒绝。页面顶部显示 FE 配置 `enable_sql_blacklist` 是否已启用——**未启用时规则不会生效**，需先在「参数配置」中改为 true。

- 添加规则 / 删除规则都需要权限 `query.blacklist`（高危，可走双人复核）。
- 正则大小写不敏感、匹配整条 SQL，特殊字符需要转义（直接输入单个反斜杠即可，平台会处理 SQL 字符串转义）。
- **过宽的规则会被拒绝**：如果规则能匹配 `select 1`、`show databases` 这类常见语句，说明会误拦截大量正常查询，平台不会写入。
- 删除规则后，其后的规则序号会前移，请以最新列表为准。
- StarRocks 不记录规则的添加人与时间；可在「操作审计」中按权限码 `query.blacklist` 查询。

### SQL 审计

在「操作审计」的 **SQL 审计** 页签查看（`audit.sql`）：读取顶栏所选集群的审计表，按时间范围、用户、库、状态（OK / EOF / ERR）、最小耗时、SQL 关键字筛选，分页加载。这是**集群内的 SQL 执行记录**，与「平台操作审计」（平台上的按钮操作）是两回事。

## 资源隔离

### 资源组

读取 `SHOW RESOURCE GROUPS ALL` 与 `SHOW USAGE RESOURCE GROUPS`，展示每个资源组的 CPU、内存、并发、大查询熔断、落盘阈值、分类器，以及运行 / 排队的查询数（排队数来自 `SHOW RUNNING QUERIES`，需要 3.3 及以上版本，否则显示“—”）。系统内置的 `default_wg` 只读。

资源组通过**分类器**匹配查询：同一个分类器内的各条件（`user`、`role`、`query_type`、`source_ip`、`db`）是“与”，多个分类器之间是“或”。可限制：

| 参数 | 说明 |
|---|---|
| `cpu_weight` | CPU 调度权重（软隔离） |
| `exclusive_cpu_cores` | 独占 CPU 核（硬隔离，适合短查询）；与 `cpu_weight` **二选一** |
| `mem_limit` | 可用内存占 BE 查询内存池的百分比（0 = 不限制） |
| `concurrency_limit` | 并发上限（0 = 不限制） |
| `big_query_cpu_second_limit` / `big_query_scan_rows_limit` / `big_query_mem_limit` | 大查询熔断阈值，超过任一项即终止查询（0 = 不设置） |
| `spill_mem_limit_threshold` | 落盘阈值（0 = 不设置） |

| 按钮 | 权限码 |
|---|---|
| ＋ 新建资源组 | `rg.create` |
| 编辑 | `rg.edit` |
| 删除（高危，匹配的查询将落入 `default_wg`；可走双人复核） | `rg.delete` |

> 编辑资源组时，平台先修改属性，再**添加**新的分类器，最后**删除**不再需要的旧分类器，因此修改过程中不会出现“没有分类器”的空档；某一步失败时会提示前面几步已经生效。至少需要保留一个分类器。

### 查询队列

开启后，资源超过阈值的新查询进入队列等待，而不是直接执行或失败，用于削峰。页面展示 SELECT / 统计信息 / 导入的排队开关、并发上限、内存与 CPU 使用率上限、最大排队数、排队超时，以及当前排队 / 运行中的查询数。

编辑需要 `queue.edit`。平台会先判断每一项是**全局变量**（`SET GLOBAL`，持久化）还是 **FE 配置**（`ADMIN SET FRONTEND CONFIG`，平台在每个存活的 FE 上逐台执行）；当前版本没有的项不会显示。

> FE 配置项默认只在内存中生效，FE 重启后恢复为 `fe.conf` 中的值；如需持久化，到「参数配置」修改并勾选“同时写入配置文件”。

> 建议：为报表、ETL、即席查询、在线 API 分别建资源组；即席查询组务必配置大查询熔断。
