/* ============================================================================
   模拟数据（原型）。字段与 StarRocks 真实接口对应关系见每段注释，后端联调时按此替换。
   官方文档：https://docs.mirrorship.cn/zh/docs/
   ========================================================================== */
const DOC = 'https://docs.mirrorship.cn/zh/docs/';

/* ---------- 纳管集群（平台配置 → 集群接入） ---------- */
const CLUSTERS = [
  {id: 'prod-sr-01', name: 'prod-sr-01', env: '生产', version: '3.5.6', mode: 'shared-nothing', fe: '10.10.1.11:9030', http: '10.10.1.11:8030', user: 'sr_ops', prom: 'http://10.10.9.5:9090', status: 'ok', desc: '核心报表 / 实时数仓', added: '2026-01-06', owner: 'ops_zhang',
    ssh: {user: 'starrocks', port: 22, auth: '平台密钥', sudo: true}, dirs: {fe: '/data/starrocks/fe', be: '/data/starrocks/be'}, audit: 'starrocks_audit_db__.starrocks_audit_tbl__'},
  {id: 'stg-sr-01', name: 'stg-sr-01', env: '预发', version: '4.0.2', mode: 'shared-data', fe: '10.20.1.11:9030', http: '10.20.1.11:8030', user: 'sr_ops', prom: 'http://10.20.9.5:9090', status: 'ok', desc: '存算分离验证集群（S3）', added: '2026-03-15', owner: 'ops_zhang',
    ssh: {user: 'starrocks', port: 22, auth: '平台密钥', sudo: true}, dirs: {fe: '/data/starrocks/fe', be: '/data/starrocks/cn'}, audit: 'starrocks_audit_db__.starrocks_audit_tbl__'},
  {id: 'dev-sr-01', name: 'dev-sr-01', env: '开发', version: '3.3.18', mode: 'shared-nothing', fe: '10.30.1.11:9030', http: '10.30.1.11:8030', user: 'root', prom: '', status: 'warn', desc: '开发自测，待升级', added: '2026-04-02', owner: 'dba_li',
    ssh: null, dirs: null, audit: ''},   // 导入时选择了“稍后填写”SSH：只读，不能执行运维操作
];
let CUR = CLUSTERS[0];
const modeText = m => m === 'shared-data' ? '存算分离' : '存算一体';
/* 依赖“当前集群”的页面：没有已导入集群时显示引导页；集群必填信息不完整时只读 */
const CLUSTER_PAGES = ['overview', 'topology', 'inspect', 'query', 'resource', 'nodes', 'upgrade', 'backup', 'load', 'mv', 'param', 'alert'];
/* 运维必填信息：FE 连接（导入时已校验）+ SSH 凭据 + 部署目录；Prometheus、审计表为可选 */
const clusterMissing = c => !c ? [] : [!c.ssh && 'SSH 凭据', !c.dirs && '部署目录'].filter(Boolean);

/* ---------- 节点（SHOW FRONTENDS / SHOW BACKENDS / SHOW COMPUTE NODES） ---------- */
function genNodes(c) {
  const base = c.fe.split(':')[0].split('.').slice(0, 3).join('.');
  const out = [];
  const fe = [['Leader', 11], ['Follower', 12], ['Follower', 13], ['Observer', 14]];
  fe.forEach(([role, ip], i) => out.push({id: 'fe-' + (i + 1), type: 'fe', role, host: `${base}.${ip}`, port: 9030, editLog: 9010, http: 8030, rpc: 9020,
    status: 'up', alive: true, version: c.version, cpu: [32, 18, 15, 12][i], mem: [64, 48, 46, 31][i], disk: [41, 39, 40, 22][i],
    jvm: [82, 55, 52, 40][i], journal: 1182334 + i, replayed: 1182334 - [0, 3, 5, 12][i], startTime: '2026-09-18 02:11', hb: '2026-10-06 10:42:10', dir: '/data/starrocks/fe'}));
  if (c.mode === 'shared-nothing') {
    const be = [[21, 61, 70, 74, 2410], [22, 58, 66, 71, 2396], [23, 63, 69, 72, 2402], [24, 92, 79, 76, 2399], [25, 55, 72, 87, 2420], [26, 49, 60, 69, 2390]];
    be.forEach(([ip, cpu, mem, disk, tablets], i) => out.push({id: 'be-' + (i + 1), type: 'be', host: `${base}.${ip}`, port: 9050, bePort: 9060, http: 8040, brpc: 8060,
      status: i === 3 ? 'warn' : 'up', alive: true, version: c.version, cpu, mem, disk, tablets, dataUsed: [3.1, 3.0, 3.1, 3.2, 3.6, 2.9][i], dataTotal: 4.2,
      compaction: [32, 41, 28, 186, 37, 30][i], memLimit: '90%', startTime: '2026-09-18 02:20', hb: '2026-10-06 10:42:11', dir: '/data1/starrocks/be,/data2/starrocks/be'}));
    [[31, 22, 30], [32, 18, 27]].forEach(([ip, cpu, mem], i) => out.push({id: 'cn-' + (i + 1), type: 'cn', host: `${base}.${ip}`, port: 9050, http: 8040, brpc: 8060,
      status: 'up', alive: true, version: c.version, cpu, mem, disk: 12, startTime: '2026-09-25 14:00', hb: '2026-10-06 10:42:11', dir: '/data/starrocks/cn', warehouse: '—'}));
  } else {
    [[31, 46, 58, 34], [32, 51, 60, 36], [33, 47, 55, 35], [34, 88, 81, 40]].forEach(([ip, cpu, mem, disk], i) => out.push({id: 'cn-' + (i + 1), type: 'cn', host: `${base}.${ip}`, port: 9050, http: 8040, brpc: 8060, starlet: 9070,
      status: i === 3 ? 'warn' : 'up', alive: true, version: c.version, cpu, mem, disk, cacheHit: [93, 91, 94, 72][i], startTime: '2026-09-30 01:10', hb: '2026-10-06 10:42:11', dir: '/data/starrocks/cn (Data Cache)', warehouse: 'default_warehouse'}));
  }
  return out;
}
let NODES = CUR ? genNodes(CUR) : [];
const TYPE_META = {fe: {label: 'FE', color: 'var(--c1)', full: 'Frontend（元数据 / 查询规划）'}, be: {label: 'BE', color: 'var(--c2)', full: 'Backend（存储 + 计算）'}, cn: {label: 'CN', color: 'var(--c4)', full: 'Compute Node（无状态计算）'}};
const BLACKLIST = [{id: 7, host: '10.10.1.24', type: 'BE', reason: '磁盘 IO 抖动，临时排除查询调度', by: 'ops_zhang', at: '2026-10-06 09:58', auto: false}];

/* ---------- 告警（Prometheus AlertManager / 平台规则） ---------- */
const ALERTS = [
  {id: 'A1001', lv: 'critical', name: 'BE 磁盘使用率超过 85%', inst: 'be-5 10.10.1.25', time: '10:31', desc: '/data2 使用率 87%，接近 storage_flood_stage_usage_percent(95%)，到达后拒绝导入。', state: 'firing', rule: 'be_disk_usage'},
  {id: 'A1002', lv: 'critical', name: 'Compaction Score 过高', inst: 'be-4 10.10.1.24', time: '10:12', desc: 'cumulative compaction score 186 > 100，导入频繁，查询可能变慢，严重时报 too many versions。', state: 'firing', rule: 'compaction_score'},
  {id: 'A1003', lv: 'warning', name: 'Routine Load 作业暂停', inst: 'ods.rl_order_events', time: '09:47', desc: 'Kafka 分区 offset out of range，作业自动 PAUSED。', state: 'firing', rule: 'routine_load_paused'},
  {id: 'A1004', lv: 'warning', name: 'FE JVM 堆使用率高', inst: 'fe-1 10.10.1.11 (Leader)', time: '09:20', desc: 'Old Gen 使用 82%，Full GC 风险。', state: 'acked', rule: 'fe_jvm_heap', ackBy: 'ops_zhang'},
  {id: 'A1005', lv: 'info', name: '物化视图刷新失败', inst: 'dws.mv_sales_daily', time: '08:05', desc: '刷新任务因内存超限失败（exceed memory limit）。', state: 'firing', rule: 'mv_refresh_failed'},
];
const ALERT_RULES = [
  {id: 'be_disk_usage', name: 'BE 磁盘使用率', expr: 'starrocks_be_disks_data_used_capacity / starrocks_be_disks_total_capacity * 100', op: '>', warn: 80, crit: 85, dur: '5m', on: true},
  {id: 'compaction_score', name: 'Compaction Score', expr: 'max(starrocks_be_tablet_cumulative_max_compaction_score)', op: '>', warn: 50, crit: 100, dur: '10m', on: true},
  {id: 'fe_jvm_heap', name: 'FE JVM 堆使用率', expr: 'jvm_heap_size_bytes{type="used"} / jvm_heap_size_bytes{type="max"} * 100', op: '>', warn: 80, crit: 90, dur: '5m', on: true},
  {id: 'node_down', name: '节点失联', expr: 'up{group=~"fe|be"} == 0', op: '==', warn: '-', crit: 0, dur: '1m', on: true},
  {id: 'query_p99', name: '查询 P99 延迟 (ms)', expr: 'starrocks_fe_query_latency_ms{quantile="0.99"}', op: '>', warn: 3000, crit: 10000, dur: '5m', on: true},
  {id: 'query_err_rate', name: '查询失败率 (%)', expr: 'rate(starrocks_fe_query_err[5m]) / rate(starrocks_fe_query_total[5m]) * 100', op: '>', warn: 1, crit: 5, dur: '5m', on: true},
  {id: 'routine_load_paused', name: 'Routine Load 暂停', expr: 'starrocks_fe_routine_load_jobs{state="PAUSED"}', op: '>', warn: 0, crit: 3, dur: '1m', on: true},
  {id: 'mv_refresh_failed', name: '物化视图刷新失败', expr: 'starrocks_fe_mv_refresh_total_failed_jobs (增量)', op: '>', warn: 0, crit: 3, dur: '0m', on: true},
  {id: 'txn_publish', name: '事务 Publish 超时', expr: 'starrocks_fe_txn_failed{type="publish_timeout"}', op: '>', warn: 1, crit: 10, dur: '5m', on: false},
];
const SILENCES = [{id: 'S1', match: 'inst=~"be-6.*"', reason: '计划内换盘维护', by: 'ops_zhang', until: '2026-10-06 18:00'}];
const CHANNELS = [
  {id: 'C1', type: '企业微信', name: '运维值班群', target: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=****', lv: '严重 / 警告', on: true},
  {id: 'C2', type: '邮件', name: 'DBA 邮件组', target: 'dba-team@example.com', lv: '严重 / 警告 / 提示', on: true},
  {id: 'C3', type: '钉钉', name: '数据平台群', target: 'https://oapi.dingtalk.com/robot/send?access_token=****', lv: '严重', on: false},
  {id: 'C4', type: 'Webhook', name: '工单系统', target: 'https://itsm.example.com/api/alert', lv: '严重', on: true},
  {id: 'C5', type: '飞书', name: '数据平台飞书群', target: 'https://open.feishu.cn/open-apis/bot/v2/hook/****', secret: true, mention: '@所有人（仅严重）', lv: '严重 / 警告', on: true},
  {id: 'C6', type: 'Lark', name: 'Overseas On-call (Lark)', target: 'https://open.larksuite.com/open-apis/bot/v2/hook/****', secret: true, mention: '—', lv: '严重 / 警告', on: true},
  {id: 'C7', type: 'Slack', name: '#sr-alerts', target: 'https://hooks.slack.com/services/T0****/B0****/****', secret: false, mention: '@here（仅严重）', lv: '严重 / 警告 / 提示', on: true},
  {id: 'C8', type: '其他', name: 'Microsoft Teams 值班频道', target: 'https://example.webhook.office.com/webhookb2/****', secret: false, mention: '—', lv: '严重', on: false},
];
/* 通知渠道类型：地址格式、是否支持签名校验、@ 提醒方式、消息格式 */
const CHANNEL_TYPES = {
  '企业微信': {ph: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=<key>', secret: '', mention: '@手机号 / @all', fmt: 'Markdown 消息'},
  '钉钉': {ph: 'https://oapi.dingtalk.com/robot/send?access_token=<token>', secret: '加签密钥（SEC 开头）', mention: '@手机号 / @所有人', fmt: 'Markdown 消息'},
  '飞书': {ph: 'https://open.feishu.cn/open-apis/bot/v2/hook/<token>', secret: '签名校验密钥（机器人安全设置 → 签名校验）', mention: '@所有人 / @用户 open_id', fmt: '消息卡片（interactive），按级别显示红 / 橙 / 蓝标题', hint: '飞书群 → 设置 → 群机器人 → 添加「自定义机器人」获取 Webhook；建议同时开启签名校验或 IP 白名单（填平台出口 IP）。'},
  'Lark': {ph: 'https://open.larksuite.com/open-apis/bot/v2/hook/<token>', secret: 'Signature verification secret', mention: '@all / @user open_id', fmt: 'Message card (interactive)，可选英文模板', hint: 'Lark 为飞书国际版，域名为 open.larksuite.com，配置方式与飞书相同；海外集群 / 海外团队使用。平台需能访问境外地址（可配置 HTTP 代理）。'},
  'Slack': {ph: 'https://hooks.slack.com/services/T000/B000/XXXX', secret: '', mention: '@here / @channel / <@UserID>', fmt: 'Block Kit 消息（附颜色条：严重红 / 警告橙 / 提示蓝）', hint: 'Slack App → Incoming Webhooks 生成 Webhook URL（绑定一个频道）；如需发往多个频道或 @用户，可改用 Bot Token（xoxb-）+ 频道名。平台需能访问境外地址（可配置 HTTP 代理）。', bot: true},
  '邮件': {ph: 'dba-team@example.com; ops@example.com', secret: '', mention: '', fmt: 'HTML 邮件'},
  '短信': {ph: '13800000000, 13900000000', secret: '', mention: '', fmt: '纯文本（≤ 70 字）'},
  'Webhook': {ph: 'https://itsm.example.com/api/alert', secret: 'HMAC 签名密钥（可选）', mention: '', fmt: 'JSON（AlertManager 兼容格式）'},
  '其他': {ph: 'https://im.example.com/api/send', secret: '鉴权 Token / 签名密钥（可选）', mention: '', fmt: '自定义请求（方法、请求头、消息模板均可配置）', hint: '用于未内置的通知方式，如 Microsoft Teams、Telegram、Discord、自建 IM、短信网关等：填写接收地址，按对方接口要求设置请求头，并在消息模板中用变量组织请求体。', custom: true},
};

/* ---------- 性能巡检项 ---------- */
const INSPECT_CATS = ['节点健康', '资源水位', '数据健康', 'Compaction', '查询性能', '导入作业', '物化视图', '配置合规', '系统环境'];
const INSPECT_ITEMS = [
  {cat: '节点健康', name: 'FE 节点存活', src: "SHOW FRONTENDS → Alive / Join", val: '4 / 4 存活', th: '全部存活', st: 'ok', adv: '—', doc: 'administration/management/Scale_up_down/'},
  {cat: '节点健康', name: 'FE 元数据同步延迟', src: "SHOW FRONTENDS → ReplayedJournalId 差值", val: 'Observer 落后 12', th: '< 1000', st: 'ok', adv: '—', doc: 'administration/Meta_recovery/'},
  {cat: '节点健康', name: 'FE 元数据 Checkpoint', src: 'fe/meta/image 生成时间', val: '26 小时未生成', th: '< 24 小时', st: 'warn', adv: '检查 Leader 内存是否足够生成 image（checkpoint 需要约 2 倍元数据内存）；确认 fe.log 中无 "checkpoint failed"。', doc: 'administration/Meta_recovery/'},
  {cat: '节点健康', name: 'FE JVM 堆使用率', src: 'jvm_heap_size_bytes', val: '82%（fe-1 Leader）', th: '< 80%', st: 'warn', adv: '调大 fe.conf 中 JAVA_OPTS 的 -Xmx（建议 ≥ 16G），排查元数据中 Tablet/分区数量是否过多。', doc: 'administration/management/monitoring/'},
  {cat: '节点健康', name: 'BE / CN 节点存活', src: 'SHOW BACKENDS / SHOW COMPUTE NODES', val: '8 / 8 存活', th: '全部存活', st: 'ok', adv: '—', doc: 'administration/management/Scale_up_down/'},
  {cat: '资源水位', name: 'BE 磁盘使用率', src: 'SHOW BACKENDS → MaxDiskUsedPct', val: '最高 87%（be-5）', th: '< 80%', st: 'bad', adv: '扩容 BE 或清理过期分区；达到 storage_flood_stage_usage_percent(默认 95%) 后将拒绝导入。', doc: 'administration/management/Scale_up_down/'},
  {cat: '资源水位', name: 'BE 内存使用率', src: 'process mem_tracker / mem_limit', val: '最高 79%', th: '< 85%', st: 'ok', adv: '—', doc: 'administration/management/resource_management/'},
  {cat: '资源水位', name: 'CPU 使用率', src: 'starrocks_be_cpu', val: '最高 92%（be-4）', th: '< 80%', st: 'warn', adv: 'be-4 Compaction 积压导致 CPU 偏高，见 Compaction 项；检查是否有大查询未受资源组约束。', doc: 'administration/management/resource_management/'},
  {cat: '数据健康', name: '不健康 Tablet', src: "SHOW PROC '/statistic' → UnhealthyTabletNum", val: '12', th: '= 0', st: 'warn', adv: "SHOW PROC '/statistic/<DbId>' 定位库，ADMIN SHOW REPLICA STATUS 查看副本状态；通常由节点重启后副本补齐中引起。", doc: 'administration/Data_recovery/'},
  {cat: '数据健康', name: '单副本表', src: 'information_schema.partitions_meta → REPLICATION_NUM = 1', val: '4 张表', th: '0（生产）', st: 'bad', adv: '生产表应使用 3 副本：ALTER TABLE t SET ("replication_num" = "3")；单副本节点故障会丢数据。', doc: 'table_design/'},
  {cat: '数据健康', name: '分桶 Tablet 大小', src: 'information_schema.partitions_meta → DATA_SIZE / BUCKETS', val: '23 张表 < 100MB/Tablet', th: '1GB ~ 10GB', st: 'warn', adv: 'Tablet 过小导致元数据膨胀、调度开销大；建议使用自动分桶或合理设置 BUCKETS。', doc: 'table_design/data_distribution/'},
  {cat: '数据健康', name: '副本均衡', src: "SHOW PROC '/cluster_balance'", val: '最大偏差 1.6%', th: '< 10%', st: 'ok', adv: '—', doc: 'administration/'},
  {cat: 'Compaction', name: 'Compaction Score', src: 'starrocks_be_tablet_cumulative_max_compaction_score', val: '最高 186（be-4）', th: '< 100', st: 'bad', adv: '降低导入频率（攒批 ≥ 5s / 单次），检查 be-4 磁盘 IO；必要时调大 cumulative_compaction_num_threads_per_disk。', doc: 'administration/management/compaction/'},
  {cat: 'Compaction', name: 'Tablet 最大版本数', src: 'SHOW TABLET / be metrics max_version_count', val: '820', th: '< 500（上限 1000）', st: 'warn', adv: '版本数超过 tablet_max_versions(1000) 时导入报 "too many versions"。', doc: 'administration/management/compaction/'},
  {cat: '查询性能', name: '查询 P99 延迟', src: 'starrocks_fe_query_latency_ms{quantile="0.99"}', val: '2.8 s', th: '< 3 s', st: 'ok', adv: '—', doc: 'best_practices/query_tuning/'},
  {cat: '查询性能', name: '慢查询数（> 5s，近 1h）', src: '审计日志 starrocks_audit_db__.starrocks_audit_tbl__', val: '143', th: '< 50', st: 'warn', adv: '前往「查询分析 → 慢查询」按 SQL 指纹聚合定位 Top SQL，并查看 Query Profile。', doc: 'best_practices/query_tuning/query_profile_overview/'},
  {cat: '查询性能', name: '查询失败率', src: 'starrocks_fe_query_err / query_total', val: '0.4%', th: '< 1%', st: 'ok', adv: '—', doc: 'administration/management/monitoring/'},
  {cat: '查询性能', name: '大查询熔断', src: 'SHOW RESOURCE GROUPS → big_query_*', val: '未配置', th: '已配置', st: 'info', adv: '为 default / adhoc 资源组配置 big_query_cpu_second_limit、big_query_scan_rows_limit，防止单个大查询拖垮集群。', doc: 'administration/management/monitor_manage_big_queries/'},
  {cat: '导入作业', name: 'Routine Load 暂停作业', src: 'SHOW ALL ROUTINE LOAD → State', val: '1 个 PAUSED', th: '0', st: 'bad', adv: '查看 ReasonOfStateChanged，修复后 RESUME ROUTINE LOAD。', doc: 'loading/RoutineLoad/'},
  {cat: '导入作业', name: '导入失败率（24h）', src: 'information_schema.loads → STATE = CANCELLED', val: '0.7%', th: '< 2%', st: 'ok', adv: '—', doc: 'loading/'},
  {cat: '导入作业', name: '运行中事务数', src: "SHOW PROC '/transactions'", val: '最高 312（ods）', th: '< 800（上限 1000）', st: 'ok', adv: '—', doc: 'loading/'},
  {cat: '物化视图', name: '刷新失败的物化视图', src: 'information_schema.materialized_views → LAST_REFRESH_STATE', val: '1', th: '0', st: 'warn', adv: '查看 information_schema.task_runs 的 ERROR_MESSAGE，可为刷新任务设置 session.query_mem_limit 或拆分分区刷新。', doc: 'using_starrocks/async_mv/Materialized_view/'},
  {cat: '物化视图', name: '非活跃物化视图', src: 'materialized_views → IS_ACTIVE = false', val: '1', th: '0', st: 'warn', adv: '基表 Schema 变更会使 MV 失效：ALTER MATERIALIZED VIEW mv ACTIVE。', doc: 'using_starrocks/async_mv/Materialized_view/'},
  {cat: '配置合规', name: '默认副本数', src: 'ADMIN SHOW FRONTEND CONFIG LIKE "default_replication_num"', val: '3', th: '3', st: 'ok', adv: '—', doc: 'administration/management/FE_configuration/'},
  {cat: '配置合规', name: '查询队列', src: 'SHOW VARIABLES LIKE "enable_query_queue%"', val: '未开启', th: '建议开启', st: 'info', adv: 'SET GLOBAL enable_query_queue_select = true，并配置 query_queue_concurrency_limit，削峰保护。', doc: 'administration/management/resource_management/query_queues/'},
  {cat: '配置合规', name: 'FE / BE 版本一致', src: 'SHOW FRONTENDS / BACKENDS → Version', val: '一致 3.5.6', th: '一致', st: 'ok', adv: '—', doc: 'deployment/upgrade/'},
  {cat: '系统环境', name: 'Swap 关闭', src: 'vm.swappiness', val: '0', th: '0', st: 'ok', adv: '—', doc: 'deployment/environment_configurations/'},
  {cat: '系统环境', name: '透明大页 THP', src: '/sys/kernel/mm/transparent_hugepage/enabled', val: '1 台主机为 always（10.10.1.26）', th: 'madvise', st: 'warn', adv: 'echo madvise > /sys/kernel/mm/transparent_hugepage/enabled，并写入开机脚本。', doc: 'deployment/environment_configurations/'},
  {cat: '系统环境', name: 'ulimit 文件句柄', src: 'ulimit -n', val: '655350', th: '≥ 655350', st: 'ok', adv: '—', doc: 'deployment/environment_configurations/'},
  {cat: '系统环境', name: '时钟同步', src: 'chronyc tracking → offset', val: '最大 3 ms', th: '< 5 s（FE 间）', st: 'ok', adv: '—', doc: 'deployment/environment_configurations/'},
];
const INSPECT_HISTORY = [
  {id: 'IR-20261006-1000', at: '2026-10-06 10:00', by: '定时任务', score: 74, bad: 4, warn: 10, dur: '41s'},
  {id: 'IR-20261005-1000', at: '2026-10-05 10:00', by: '定时任务', score: 79, bad: 3, warn: 9, dur: '39s'},
  {id: 'IR-20261004-2210', at: '2026-10-04 22:10', by: 'ops_zhang', score: 85, bad: 2, warn: 7, dur: '40s'},
  {id: 'IR-20261004-1000', at: '2026-10-04 10:00', by: '定时任务', score: 87, bad: 2, warn: 5, dur: '38s'},
];

/* ---------- 查询（SHOW PROC '/current_queries'、审计日志） ---------- */
const RUNNING = [
  {qid: '8c1f7a2e-61d4-11f0-b1a2-00163e0a1b2c', user: 'bi_report', db: 'dws', fe: 'fe-1', rg: 'report', start: '10:41:02', sec: 128, scan: '3.2 亿行 / 18.6 GB', mem: '12.4 GB', cpu: '612 s', sql: 'SELECT region, sku, SUM(amount) FROM dws.fact_order o JOIN dim.dim_sku s ON o.sku_id = s.id WHERE dt BETWEEN \'2026-01-01\' AND \'2026-10-05\' GROUP BY region, sku ORDER BY 3 DESC'},
  {qid: '91aa0b31-61d4-11f0-b1a2-00163e0a1b2c', user: 'etl_user', db: 'ods', fe: 'fe-2', rg: 'etl', start: '10:42:11', sec: 59, scan: '8800 万行 / 4.1 GB', mem: '3.1 GB', cpu: '140 s', sql: 'INSERT INTO dwd.dwd_order_detail SELECT ... FROM ods.ods_order WHERE dt = \'2026-10-06\''},
  {qid: 'a3c52e10-61d4-11f0-b1a2-00163e0a1b2c', user: 'adhoc_liu', db: 'dws', fe: 'fe-3', rg: 'adhoc', start: '10:42:55', sec: 15, scan: '1.1 亿行 / 6.0 GB', mem: '5.8 GB', cpu: '88 s', sql: 'SELECT * FROM dws.fact_order WHERE user_id IN (SELECT user_id FROM dim.dim_user WHERE city LIKE \'%州%\')'},
  {qid: 'b77e9f02-61d4-11f0-b1a2-00163e0a1b2c', user: 'api_svc', db: 'ads', fe: 'fe-1', rg: 'default_wg', start: '10:43:09', sec: 1, scan: '2.3 万行 / 1 MB', mem: '22 MB', cpu: '0.2 s', sql: 'SELECT * FROM ads.ads_user_profile WHERE user_id = 100231'},
];
const SLOW = [
  {digest: 'd41a9c', sql: 'SELECT region, sku, SUM(amount) FROM dws.fact_order o JOIN dim.dim_sku s ON ... GROUP BY region, sku ORDER BY ?', user: 'bi_report', cnt: 38, avg: 42.1, max: 128.4, scanRows: '3.2 亿', mem: '12.4 GB', last: '10:41', issue: '大表 Shuffle Join + 扫描全年分区'},
  {digest: '7be20f', sql: 'SELECT * FROM dws.fact_order WHERE user_id IN (SELECT user_id FROM dim.dim_user WHERE city LIKE ?)', user: 'adhoc_liu', cnt: 21, avg: 18.6, max: 33.0, scanRows: '1.1 亿', mem: '5.8 GB', last: '10:42', issue: 'LIKE 前缀通配无法下推，SELECT *'},
  {digest: '0c93d1', sql: 'SELECT dt, COUNT(DISTINCT user_id) FROM dwd.dwd_pv WHERE dt >= ? GROUP BY dt', user: 'bi_report', cnt: 64, avg: 9.4, max: 15.2, scanRows: '9.8 亿', mem: '7.1 GB', last: '10:30', issue: '精确去重，可用 bitmap / HLL 或物化视图改写'},
  {digest: 'f1e5aa', sql: 'INSERT INTO dwd.dwd_order_detail SELECT ... FROM ods.ods_order WHERE dt = ?', user: 'etl_user', cnt: 12, avg: 7.8, max: 11.0, scanRows: '8800 万', mem: '3.1 GB', last: '10:42', issue: '—'},
  {digest: '5a0b77', sql: 'SELECT o.*, u.* FROM ods.ods_order o LEFT JOIN dim.dim_user u ON o.uid = u.id WHERE o.dt = ?', user: 'adhoc_liu', cnt: 8, avg: 6.3, max: 8.9, scanRows: '2100 万', mem: '2.4 GB', last: '09:58', issue: 'Join 键类型不一致（VARCHAR vs BIGINT）导致隐式转换'},
];
const SQL_BLACKLIST = [{id: 1, pattern: 'select count\\(\\*\\) from dws\\.fact_order', by: 'dba_li', at: '2026-09-21 14:00'}];
const PROFILE = {
  total: '128.4s', planning: '0.31s', queue: '0s', mem: '12.4 GB', scan: '3.2 亿行',
  tree: [
    {id: 'RESULT_SINK', t: 0.1, rows: '1,000', lv: 0},
    {id: 'MERGE_EXCHANGE (id=12)', t: 0.4, rows: '1,000', lv: 1},
    {id: 'TOP-N (id=11)', t: 1.9, rows: '1,000', lv: 2},
    {id: 'AGGREGATE (merge, id=10)', t: 6.2, rows: '86 万', lv: 3},
    {id: 'EXCHANGE (HASH, id=9)', t: 9.8, rows: '86 万', lv: 4},
    {id: 'AGGREGATE (update, id=8)', t: 14.5, rows: '2,300 万', lv: 5},
    {id: 'HASH_JOIN (SHUFFLE, INNER, id=7)', t: 38.6, rows: '3.2 亿', lv: 6, hot: true},
    {id: 'EXCHANGE (HASH, id=5)  ← fact_order', t: 21.2, rows: '3.2 亿', lv: 7, hot: true},
    {id: 'OLAP_SCAN dws.fact_order (id=0)  partitions 279/280', t: 31.9, rows: '3.2 亿', lv: 8, hot: true},
    {id: 'EXCHANGE (HASH, id=6)  ← dim_sku', t: 0.6, rows: '41 万', lv: 7},
    {id: 'OLAP_SCAN dim.dim_sku (id=1)', t: 0.4, rows: '41 万', lv: 8},
  ],
  advice: [
    'fact_order 扫描 279/280 个分区：WHERE 条件跨全年，确认业务是否需要；考虑按月预聚合物化视图。',
    'dim_sku 仅 41 万行，可改为 BROADCAST Join 避免对 3.2 亿行事实表 Shuffle（统计信息过期会导致误选 Shuffle：ANALYZE TABLE dim.dim_sku）。',
    '若 fact_order 与 dim_sku 按 sku_id 同分桶且在同一 Colocate Group，可走 Colocate Join，无需网络交换。',
  ],
};

/* ---------- 资源组（SHOW RESOURCE GROUPS ALL） ---------- */
const RES_GROUPS = [
  {name: 'report', cpu: 8, mem: '40%', conc: 30, spill: '—', bigCpu: 600, bigScan: '50 亿', bigMem: '20 GB', cls: "user='bi_report'", running: 6, queued: 0},
  {name: 'etl', cpu: 6, mem: '35%', conc: 10, spill: '80%', bigCpu: 0, bigScan: '—', bigMem: '—', cls: "user='etl_user', query_type in ('insert')", running: 3, queued: 1},
  {name: 'adhoc', cpu: 2, mem: '15%', conc: 5, spill: '—', bigCpu: 120, bigScan: '10 亿', bigMem: '8 GB', cls: "role='analyst'", running: 2, queued: 3},
  {name: 'api_short', cpu: 4, mem: '10%', conc: 0, spill: '—', bigCpu: 10, bigScan: '1000 万', bigMem: '1 GB', cls: "user='api_svc', source_ip='10.40.0.0/16'", running: 18, queued: 0, exclusive: 4},
];
const QUEUE_CFG = {select: false, statistic: true, load: false, conc: 50, mem: 0.9, cpu: 0, maxQueued: 1024, timeout: 300};

/* ---------- 导入作业（SHOW ROUTINE LOAD / information_schema.loads） ---------- */
const LOADS = [
  {name: 'rl_order_events', db: 'ods', type: 'Routine Load', src: 'Kafka: order_events (12 分区)', state: 'PAUSED', lag: '1.2 亿', rate: '0', err: '0.01%', reason: 'offset out of range: partition 7', created: '2026-08-01'},
  {name: 'rl_click_log', db: 'ods', type: 'Routine Load', src: 'Kafka: click_log (24 分区)', state: 'RUNNING', lag: '3,210', rate: '42,800 行/s', err: '0%', reason: '', created: '2026-07-12'},
  {name: 'rl_pay_flow', db: 'ods', type: 'Routine Load', src: 'Kafka: pay_flow (6 分区)', state: 'RUNNING', lag: '120', rate: '3,500 行/s', err: '0%', reason: '', created: '2026-06-30'},
  {name: 'pipe_s3_logs', db: 'ods', type: 'Pipe', src: 's3://lake/logs/*.parquet', state: 'RUNNING', lag: '18 文件', rate: '—', err: '0%', reason: '', created: '2026-09-02'},
  {name: 'label_dwd_20261006_02', db: 'dwd', type: 'Broker Load', src: 'hdfs://ns1/warehouse/dwd/...', state: 'FINISHED', lag: '—', rate: '—', err: '0%', reason: '', created: '2026-10-06'},
  {name: 'label_ads_20261006_01', db: 'ads', type: 'Stream Load', src: 'HTTP PUT /api/ads/_stream_load', state: 'CANCELLED', lag: '—', rate: '—', err: '—', reason: 'too many filtered rows (max_filter_ratio=0)', created: '2026-10-06'},
];

/* ---------- 异步物化视图（information_schema.materialized_views） ---------- */
const MVS = [
  {name: 'dws.mv_sales_daily', refresh: 'ASYNC EVERY 1 HOUR', part: 'dt', active: true, state: 'FAILED', last: '08:00', dur: '6m12s', rows: '1.2 亿', size: '8.6 GB', err: 'Memory of query exceed limit (16GB)', hit: 412},
  {name: 'dws.mv_user_retention', refresh: 'ASYNC EVERY 1 DAY', part: 'dt', active: true, state: 'SUCCESS', last: '02:00', dur: '11m40s', rows: '3,600 万', size: '2.1 GB', err: '', hit: 96},
  {name: 'ads.mv_gmv_realtime', refresh: 'ASYNC EVERY 5 MINUTE', part: '—', active: true, state: 'RUNNING', last: '10:40', dur: '—', rows: '86 万', size: '120 MB', err: '', hit: 5210},
  {name: 'dws.mv_sku_rank', refresh: 'MANUAL', part: '—', active: false, state: 'SUCCESS', last: '2026-10-03', dur: '2m03s', rows: '41 万', size: '60 MB', err: '基表 dim.dim_sku 发生 Schema Change，视图失效', hit: 0},
];

/* ---------- 备份恢复（SHOW REPOSITORIES / SHOW BACKUP / SHOW SNAPSHOT） ---------- */
const REPOS = [
  {name: 'repo_s3_prod', type: 'S3', loc: 's3://sr-backup/prod-sr-01', ro: false, snaps: 14, created: '2026-02-01'},
  {name: 'repo_hdfs_dr', type: 'HDFS', loc: 'hdfs://dr-ns/starrocks/backup', ro: true, snaps: 6, created: '2026-05-10'},
];
const BACKUPS = [
  {id: 'BK-24', kind: '备份', snap: 'snap_dws_20261006', db: 'dws', objs: 'fact_order, dim_sku, mv_sales_daily', repo: 'repo_s3_prod', state: 'UPLOADING', prog: 63, size: '412 GB', start: '2026-10-06 02:00', by: '定时策略'},
  {id: 'BK-23', kind: '备份', snap: 'snap_ads_20261006', db: 'ads', objs: '全部表 (32)', repo: 'repo_s3_prod', state: 'FINISHED', prog: 100, size: '38 GB', start: '2026-10-06 01:30', by: '定时策略'},
  {id: 'RS-07', kind: '恢复', snap: 'snap_dim_20261001', db: 'dim', objs: 'dim_user AS dim_user_restore', repo: 'repo_s3_prod', state: 'FINISHED', prog: 100, size: '6 GB', start: '2026-10-02 15:12', by: 'dba_li'},
  {id: 'BK-22', kind: '备份', snap: 'snap_dws_20261005', db: 'dws', objs: 'fact_order, dim_sku, mv_sales_daily', repo: 'repo_s3_prod', state: 'CANCELLED', prog: 41, size: '—', start: '2026-10-05 02:00', by: '定时策略'},
];
const BACKUP_POLICIES = [
  {name: '核心库每日备份', dbs: 'dws, ads', cron: '每天 02:00', repo: 'repo_s3_prod', keep: '保留 14 份', on: true},
  {name: '维表每周备份', dbs: 'dim', cron: '每周日 03:00', repo: 'repo_hdfs_dr', keep: '保留 8 份', on: true},
];

/* ---------- 参数（ADMIN SHOW FRONTEND CONFIG / BE http /varz / SHOW VARIABLES） ---------- */
const PARAMS = {
  fe: [
    {k: 'max_routine_load_task_num_per_be', v: '16', def: '16', mutable: true, desc: '每个 BE 上 Routine Load 任务并发上限'},
    {k: 'tablet_sched_max_scheduling_tablets', v: '10000', def: '10000', mutable: true, desc: 'Tablet 调度并发数上限（副本修复 / 均衡）'},
    {k: 'max_running_txn_num_per_db', v: '1000', def: '1000', mutable: true, desc: '单库运行中导入事务上限'},
    {k: 'default_replication_num', v: '3', def: '3', mutable: true, desc: '建表默认副本数'},
    {k: 'qe_slow_log_ms', v: '5000', def: '5000', mutable: true, desc: '慢查询阈值 (ms)'},
    {k: 'enable_statistic_collect', v: 'true', def: 'true', mutable: true, desc: '自动统计信息采集'},
    {k: 'meta_dir', v: '/data/starrocks/fe/meta', def: '${STARROCKS_HOME}/meta', mutable: false, desc: '元数据目录（静态参数，需修改 fe.conf 并重启）'},
    {k: 'JAVA_OPTS (-Xmx)', v: '16384m', def: '8192m', mutable: false, desc: 'FE JVM 堆大小（静态，需重启）'},
  ],
  be: [
    {k: 'cumulative_compaction_num_threads_per_disk', v: '1', def: '1', mutable: true, desc: '每块盘 Cumulative Compaction 线程数'},
    {k: 'max_compaction_concurrency', v: '-1', def: '-1', mutable: true, desc: 'Compaction 最大并发，-1 不限制'},
    {k: 'storage_flood_stage_usage_percent', v: '95', def: '95', mutable: true, desc: '磁盘使用率超过该值时拒绝导入'},
    {k: 'mem_limit', v: '90%', def: '90%', mutable: false, desc: 'BE 进程内存上限（需重启）'},
    {k: 'tablet_max_versions', v: '1000', def: '1000', mutable: true, desc: 'Tablet 最大版本数，超过报 too many versions'},
    {k: 'storage_root_path', v: '/data1/starrocks/be;/data2/starrocks/be', def: '${STARROCKS_HOME}/storage', mutable: false, desc: '数据目录（静态，需重启）'},
  ],
  var: [
    {k: 'query_timeout', v: '300', def: '300', mutable: true, desc: '查询超时 (秒)，SET GLOBAL 生效'},
    {k: 'query_mem_limit', v: '0', def: '0', mutable: true, desc: '单查询在单 BE 上的内存上限 (Byte)，0 不限制'},
    {k: 'pipeline_dop', v: '0', def: '0', mutable: true, desc: 'Pipeline 并行度，0 为自动'},
    {k: 'enable_spill', v: 'false', def: 'false', mutable: true, desc: '中间结果落盘（大查询防 OOM）'},
    {k: 'enable_query_queue_select', v: 'false', def: 'false', mutable: true, desc: 'SELECT 查询排队'},
    {k: 'new_planner_optimize_timeout', v: '3000', def: '3000', mutable: true, desc: '优化器超时 (ms)'},
  ],
};
const PARAM_HISTORY = [
  {at: '2026-10-05 21:10', user: 'dba_li', scope: 'FE（全部）', k: 'qe_slow_log_ms', from: '3000', to: '5000', persist: '已写入 fe.conf', st: '已生效'},
  {at: '2026-10-03 14:02', user: 'ops_zhang', scope: 'BE be-4', k: 'cumulative_compaction_num_threads_per_disk', from: '1', to: '2', persist: '仅内存', st: '已生效'},
  {at: '2026-09-28 10:15', user: 'dba_li', scope: 'GLOBAL 变量', k: 'query_timeout', from: '600', to: '300', persist: '—', st: '已生效'},
];

/* ---------- 安装部署 ---------- */
const HOSTS = [
  {ip: '10.40.1.11', name: 'sr-new-fe1', cpu: 16, mem: 64, disk: '1×SSD 500G', os: 'CentOS 7.9', arch: 'x86_64 (AVX2)', ssh: '已连通', used: '—'},
  {ip: '10.40.1.12', name: 'sr-new-fe2', cpu: 16, mem: 64, disk: '1×SSD 500G', os: 'CentOS 7.9', arch: 'x86_64 (AVX2)', ssh: '已连通', used: '—'},
  {ip: '10.40.1.13', name: 'sr-new-fe3', cpu: 16, mem: 64, disk: '1×SSD 500G', os: 'Rocky 9.3', arch: 'x86_64 (AVX2)', ssh: '已连通', used: '—'},
  {ip: '10.40.1.21', name: 'sr-new-be1', cpu: 48, mem: 256, disk: '4×NVMe 3.8T', os: 'Rocky 9.3', arch: 'x86_64 (AVX2)', ssh: '已连通', used: '—'},
  {ip: '10.40.1.22', name: 'sr-new-be2', cpu: 48, mem: 256, disk: '4×NVMe 3.8T', os: 'Rocky 9.3', arch: 'x86_64 (AVX2)', ssh: '已连通', used: '—'},
  {ip: '10.40.1.23', name: 'sr-new-be3', cpu: 48, mem: 256, disk: '4×NVMe 3.8T', os: 'Rocky 9.3', arch: 'x86_64 (AVX2)', ssh: '认证失败', used: '—'},
];
const PACKAGES = [
  {name: 'StarRocks-4.0.2-centos-amd64.tar.gz', ver: '4.0.2', arch: 'amd64', size: '2.3 GB', sha: 'a91c…e02f', up: '2026-09-20', by: 'ops_zhang'},
  {name: 'StarRocks-3.5.6-centos-amd64.tar.gz', ver: '3.5.6', arch: 'amd64', size: '2.1 GB', sha: '5d10…77ab', up: '2026-08-30', by: 'ops_zhang'},
  {name: 'StarRocks-3.5.6-ubuntu-arm64.tar.gz', ver: '3.5.6', arch: 'arm64', size: '2.0 GB', sha: 'c33e…1a90', up: '2026-08-30', by: 'ops_zhang'},
  {name: 'jdk-17.0.12_linux-x64_bin.tar.gz', ver: 'JDK 17', arch: 'amd64', size: '182 MB', sha: '0b2f…9c11', up: '2026-08-30', by: 'ops_zhang'},
];
const PRECHECK = [
  {item: 'CPU 指令集 AVX2', cmd: 'grep -c avx2 /proc/cpuinfo', expect: '> 0', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: '不支持 AVX2 的机器需使用无 AVX2 版本安装包'},
  {item: '操作系统 / glibc', cmd: 'cat /etc/os-release; ldd --version', expect: 'CentOS 7.9+ / Rocky / Ubuntu 22.04', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: ''},
  {item: 'JDK 版本', cmd: 'java -version', expect: 'JDK 17（3.5+ 必需）', res: ['bad', 'bad', 'ok', 'ok', 'ok', '—'], fix: '解压平台安装包中的 jdk-17 到 /opt/jdk17，并在 fe.conf / be.conf 设置 JAVA_HOME'},
  {item: 'Swap 关闭 (vm.swappiness)', cmd: 'sysctl vm.swappiness; swapon --show', expect: '0 且无 swap 分区', res: ['ok', 'ok', 'ok', 'warn', 'ok', '—'], fix: 'swapoff -a; echo "vm.swappiness=0" >> /etc/sysctl.conf; sysctl -p'},
  {item: '内存 Overcommit', cmd: 'sysctl vm.overcommit_memory', expect: '1', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: 'echo "vm.overcommit_memory=1" >> /etc/sysctl.conf; sysctl -p'},
  {item: '透明大页 THP', cmd: 'cat /sys/kernel/mm/transparent_hugepage/enabled', expect: 'madvise', res: ['ok', 'ok', 'warn', 'warn', 'warn', '—'], fix: 'echo madvise > /sys/kernel/mm/transparent_hugepage/enabled; echo madvise > /sys/kernel/mm/transparent_hugepage/defrag'},
  {item: '文件句柄 / 进程数 ulimit', cmd: 'ulimit -n; ulimit -u', expect: '≥ 655350 / ≥ 65535', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: '在 /etc/security/limits.conf 增加 * soft nofile 655350 等'},
  {item: 'vm.max_map_count', cmd: 'sysctl vm.max_map_count', expect: '≥ 262144', res: ['ok', 'ok', 'ok', 'bad', 'bad', '—'], fix: 'echo "vm.max_map_count=262144" >> /etc/sysctl.conf; sysctl -p'},
  {item: 'TCP 参数', cmd: 'sysctl net.ipv4.tcp_abort_on_overflow net.core.somaxconn', expect: '1 / ≥ 1024', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: 'echo "net.ipv4.tcp_abort_on_overflow=1" >> /etc/sysctl.conf; echo "net.core.somaxconn=1024" >> /etc/sysctl.conf'},
  {item: '时钟同步', cmd: 'chronyc tracking', expect: '各节点偏差 < 5s', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: 'yum install -y chrony && systemctl enable --now chronyd'},
  {item: 'SELinux / 防火墙', cmd: 'getenforce; systemctl is-active firewalld', expect: 'Disabled / 端口已放通', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: 'setenforce 0；或放通 FE 8030/9020/9030/9010，BE/CN 9060/8040/9050/8060/9070'},
  {item: '端口占用', cmd: 'ss -lntp | grep -E "8030|9020|9030|9010|9060|8040|9050|8060"', expect: '未被占用', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: '释放端口或在配置中修改端口'},
  {item: '数据盘挂载', cmd: 'lsblk; mount | grep data', expect: 'ext4/xfs，独立数据盘，noatime', res: ['ok', 'ok', 'ok', 'ok', 'ok', '—'], fix: ''},
  {item: 'SSH 连通 / sudo', cmd: 'ssh -o BatchMode=yes <host> sudo -n true', expect: '免密 + 无交互 sudo', res: ['ok', 'ok', 'ok', 'ok', 'ok', 'bad'], fix: '把平台公钥加入目标主机 ~/.ssh/authorized_keys'},
];
const DEPLOY_TASKS = [
  {id: 'DP-0012', name: 'stg-sr-02 新建（存算分离）', ver: '4.0.2', mode: 'shared-data', hosts: 7, state: '已完成', prog: 100, by: 'ops_zhang', at: '2026-09-28 15:20'},
  {id: 'DP-0011', name: 'prod-sr-01 扩容 BE×2', ver: '3.5.6', mode: 'shared-nothing', hosts: 2, state: '已完成', prog: 100, by: 'ops_zhang', at: '2026-09-18 02:00'},
  {id: 'DP-0013', name: 'dev-sr-02 新建（存算一体）', ver: '3.5.6', mode: 'shared-nothing', hosts: 3, state: '失败', prog: 45, by: 'ops_zhang', at: '2026-10-05 16:40', err: '10.40.1.23 SSH 认证失败'},
];
const UPGRADE_PLANS = [
  {id: 'UP-005', cluster: 'dev-sr-01', from: '3.3.18', to: '3.5.6', state: '待执行', by: 'ops_zhang', at: '2026-10-06 09:00', path: '3.3.18 → 3.4.x → 3.5.6（不建议跨大版本直接升级）'},
  {id: 'UP-004', cluster: 'prod-sr-01', from: '3.5.3', to: '3.5.6', state: '已完成', by: 'ops_zhang', at: '2026-09-18 01:00', path: '3.5.3 → 3.5.6（同大版本补丁升级）'},
];

/* ---------- 平台操作审计 ---------- */
const AUDIT = [
  {at: '2026-10-06 10:31', user: 'ops_zhang', ip: '10.1.2.33', cluster: 'prod-sr-01', menu: '节点管理', code: 'node.blacklist', act: '加入黑名单 BE 10.10.1.24', res: '成功', sql: 'ADD BACKEND BLACKLIST 10005;'},
  {at: '2026-10-06 10:02', user: 'ops_zhang', ip: '10.1.2.33', cluster: 'prod-sr-01', menu: '性能巡检', code: 'inspect.run', act: '执行性能巡检（全部 9 类）', res: '成功', sql: '—'},
  {at: '2026-10-06 09:41', user: 'dba_li', ip: '10.1.5.12', cluster: 'prod-sr-01', menu: '查询分析', code: 'query.kill', act: 'KILL QUERY 3f1c…', res: '成功', sql: "KILL QUERY '3f1c2a0e-61c9-11f0-b1a2-00163e0a1b2c';"},
  {at: '2026-10-06 09:30', user: 'oncall_wang', ip: '10.1.7.8', cluster: 'prod-sr-01', menu: '节点管理', code: 'node.restart', act: '尝试重启 be-4', res: '拒绝（无权限）', sql: '—'},
  {at: '2026-10-05 21:10', user: 'dba_li', ip: '10.1.5.12', cluster: 'prod-sr-01', menu: '参数配置', code: 'param.edit', act: 'qe_slow_log_ms 3000 → 5000', res: '成功', sql: 'ADMIN SET FRONTEND CONFIG ("qe_slow_log_ms" = "5000");'},
  {at: '2026-10-05 17:22', user: 'admin', ip: '10.1.1.2', cluster: '—', menu: '角色权限', code: 'role.edit', act: '角色「DBA」增加 param.edit', res: '成功', sql: '—'},
  {at: '2026-10-05 16:40', user: 'ops_zhang', ip: '10.1.2.33', cluster: 'dev-sr-02', menu: '安装部署', code: 'deploy.execute', act: '执行部署 DP-0013', res: '失败', sql: '—'},
  {at: '2026-10-05 11:03', user: 'admin', ip: '10.1.1.2', cluster: '—', menu: '用户管理', code: 'user.lock', act: '锁定用户 dev_zhao', res: '成功', sql: '—'},
];
