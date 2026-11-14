# 导入作业 / 物化视图

## 导入作业

读取 `information_schema.routine_load_jobs`（Routine Load）与 `information_schema.loads`（Broker Load / Stream Load / Insert 等的最近记录）。顶部指标卡显示 Routine Load 运行 / 暂停数、导入速率（各作业速率之和）、导入失败比例与已完成数。

### Routine Load

列出每个作业的目标表、数据源、状态、已导入 / 总行数、速率、错误率，暂停的作业直接显示原因。

| 按钮 | 权限码 | 对应命令 |
|---|---|---|
| 暂停 | `load.pause` | `PAUSE ROUTINE LOAD FOR db.job` |
| 恢复 | `load.resume` | `RESUME ROUTINE LOAD FOR db.job` |
| 停止（高危） | `load.stop` | `STOP ROUTINE LOAD FOR db.job`；**停止后不可恢复，只能重建**；可走双人复核 |
| 详情 | `load.detail` | 数据源属性、消费进度、暂停原因、错误数据地址（`ErrorLogUrls`）、当前子任务（`SHOW ROUTINE LOAD TASK`） |

### 最近的其他导入

最近 50 条 Broker Load / Stream Load / Insert 等导入记录，显示状态、扫描 / 过滤 / 写入行数、起止时间，失败的显示失败原因。这部分只读。

> 当前版本不管理 Pipe 与 Spark Load；Routine Load 的**创建与修改**请在 StarRocks 中执行。

### 常见错误

| 错误 | 原因 | 处理 |
|---|---|---|
| offset out of range | Kafka 数据已过期删除 | `ALTER ROUTINE LOAD` 设置 `kafka_offsets` 后恢复 |
| too many filtered rows | 数据格式 / 类型不匹配 | 修正源数据或适当调大 `max_filter_ratio` |
| too many versions | 导入太频繁，Compaction 跟不上 | 增大攒批间隔，见巡检 Compaction 项 |
| 事务数达上限 | 单库运行中事务超过 `max_running_txn_num_per_db` | 合并导入任务或调大参数 |

## 物化视图

读取 `information_schema.materialized_views`，列出异步物化视图的刷新方式、分区方式、状态、最近刷新时间与耗时、行数；刷新失败显示错误信息，INACTIVE 显示原因。有刷新任务运行时页面每 4 秒自动刷新。点击“定义”查看建表语句。

| 按钮 | 权限码 | 说明 |
|---|---|---|
| 刷新 | `mv.refresh` | 可选全部分区或指定分区区间（起止时间格式 `YYYY-MM-DD`），可选 FORCE；以异步方式提交，进度在列表中查看 |
| 启用 / 停用 | `mv.toggle` | `ALTER MATERIALIZED VIEW mv ACTIVE / INACTIVE` |
| 取消 | `mv.cancel` | `CANCEL REFRESH MATERIALIZED VIEW mv`，取消运行中的刷新任务 |

> 基表发生 Schema Change 会使物化视图变为 **INACTIVE**，需手动启用。刷新失败报内存超限时，可为物化视图设置 `"session.query_mem_limit"` 或开启 spill，或改为按分区刷新。当前版本不显示“查询改写命中次数”。
