# 集群总览 / 集群架构

## 集群总览

打开平台后的默认页，用一屏回答“集群现在好不好”。

### 指标卡

| 指标 | 来源 | 关注点 |
|---|---|---|
| QPS | FE 指标 `starrocks_fe_qps` | 与昨日同期对比，突增可能是业务高峰或异常请求 |
| 查询 P99 | `starrocks_fe_query_latency_ms{quantile="0.99"}` | 超过 3s 关注，结合「查询分析」定位 |
| FE / BE / CN 存活 | `SHOW FRONTENDS / BACKENDS / COMPUTE NODES` | 任一节点非 Alive 都会产生告警 |
| 存储使用 | `SHOW BACKENDS` 的磁盘用量（存算分离为对象存储用量与缓存命中率） | 按最近 7 天增长线性预测到达 85% 的天数 |
| 导入速率 | Routine Load / Stream Load 指标 | 暂停的 Routine Load 会在副标题提示 |
| 巡检健康分 | 最近一次巡检 | 低于 80 分请到「性能巡检」处理 |

> 连接平台服务时，总览为真实数据：节点与存储来自 `SHOW FRONTENDS / BACKENDS / COMPUTE NODES`，QPS、延迟、连接数、JVM 来自各 FE 的 `/metrics`，Compaction Score 来自 BE 的 `/metrics`，Tablet、物化视图、Routine Load 来自 `SHOW PROC '/statistic'` 与 information_schema。趋势图在配置了 Prometheus 时显示最近 60 分钟历史，否则由平台每 15 秒采样一次（服务重启后重新开始采样）。某项采集失败时页面顶部会列出原因，其余数据照常显示。

### 其他区域

- **查询吞吐与延迟**：最近 60 分钟 QPS、P99 / P90 曲线。
- **资源组 CPU 占用**：哪个资源组在消耗 CPU，点击跳转「资源隔离」。数据来自 `SHOW USAGE RESOURCE GROUPS`（StarRocks **3.1.4 及以上**），按资源组合计各 BE / CN 正在使用的 CPU 核、内存与运行中查询；条形图为各资源组占已用 CPU 的比例，表格中“占集群 CPU”为占全部存活 BE / CN 核数的比例。没有查询在运行时显示“当前没有查询在使用资源组的 CPU”；低版本集群显示“未采集”。
- **节点健康**：所有节点的 CPU / 内存 / 磁盘水位；BE 显示 Compaction Score（> 100 标红），FE 显示 JVM 堆使用率。点击行打开节点详情。
- **最近一次巡检 / 活跃告警 / 集群信息**：右侧栏。连接平台服务时，**活跃告警**显示当前集群在告警中心里的真实告警（来自平台每分钟的规则评估，见 [告警中心](#help/alert)），最多显示 6 条，其余请到告警中心查看。

### 按钮

| 按钮 | 权限码 | 说明 |
|---|---|---|
| ⟳ 刷新 | `overview.refresh` | 重新拉取指标 |
| ⇩ 导出概览报告 | `overview.export` | 通过浏览器打印为 PDF，用于日报 / 周报 |

## 集群架构

以拓扑图展示：客户端 → 负载均衡 → FE（Leader / Follower / Observer）→ BE 或 CN →（存算分离）对象存储 /（存算一体）External Catalog。

- 节点边框为黄色表示该节点存在告警。
- 点击节点打开详情抽屉（需要权限 `topology.detail`），包括端口、版本、心跳、资源曲线和常用命令。

> 默认端口：FE `http_port 8030`、`rpc_port 9020`、`query_port 9030`、`edit_log_port 9010`；BE / CN `be_port 9060`、`be_http_port 8040`、`heartbeat_service_port 9050`、`brpc_port 8060`，存算分离另有 `starlet_port 9070`。
