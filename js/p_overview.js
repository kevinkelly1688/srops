// @author: kevinkelly
// @license: Apache Licence
// @file: p_overview.js
// @time: 2026/10/28

/* 概览：集群总览 / 集群架构 */
pages.overview = {
  render() {
    const fe = NODES.filter(n => n.type === 'fe'), be = NODES.filter(n => n.type === 'be'), cn = NODES.filter(n => n.type === 'cn');
    const kpi = (l, v, d, cls = 'flat') => `<div class="card kpi"><div class="l">${l}</div><div class="v">${v}</div><div class="d ${cls}">${d}</div></div>`;
    const last = INSPECT_HISTORY[0];
    const sd = CUR.mode === 'shared-data';
    return `
    <div class="toolbar"><span class="tag brand">${CUR.env}</span><span class="small muted">${CUR.name} · StarRocks ${CUR.version} · ${modeText(CUR.mode)} · FE ${CUR.fe}</span><span class="sp"></span>
      ${B('overview.refresh', '⟳ 刷新', {cls: 'sm'})}${B('overview.export', '⇩ 导出概览报告', {cls: 'sm'})}</div>
    <div class="grid g6">
      ${kpi('QPS', '3,862', '▲ 8.1% 较昨日同期', 'up')}
      ${kpi('查询 P99', '2.8 s', '▲ 0.6s 较 1 小时前', 'up')}
      ${kpi('FE / BE / CN 存活', `${fe.length}/${be.length}/${cn.length}`, '全部存活', 'down')}
      ${kpi(sd ? '对象存储用量' : '存储使用', sd ? '38.2 TB' : '18.9 / 25.2 TB', sd ? 'Data Cache 命中率 88%' : '75% · 预计 23 天达 85%', 'up')}
      ${kpi('导入速率', '4.6 万行/s', 'Routine Load 1 个暂停', 'up')}
      ${kpi('巡检健康分', last.score, `${last.bad} 异常 · ${last.warn} 警告`, last.score < 80 ? 'up' : 'flat')}
    </div>
    <div class="grid g-main" style="margin-top:16px">
      <div class="grid" style="align-content:start">
        <div class="card"><h2>查询吞吐与延迟 <span class="sub">最近 60 分钟 · 来源 Prometheus（模拟）</span></h2>
          <div class="grid g2">
            <div>${lineChart([{name: 'QPS', color: 'var(--c1)', data: series(3, 60, 3600, 600, [40, 52, 900])}])}</div>
            <div>${lineChart([{name: 'P99 (ms)', color: 'var(--c5)', data: series(8, 60, 2100, 500, [40, 52, 900])}, {name: 'P90 (ms)', color: 'var(--c3)', data: series(9, 60, 800, 200, [40, 52, 300])}])}</div>
          </div></div>
        <div class="card"><h2>资源组 CPU 占用 <span class="sub">按资源组（SHOW USAGE RESOURCE GROUPS）</span><span class="right"><a href="#resource">资源隔离 →</a></span></h2>
          ${stackBar([['report', 41, 'var(--c1)'], ['etl', 27, 'var(--c2)'], ['adhoc', 18, 'var(--c3)'], ['api_short', 9, 'var(--c4)'], ['default_wg', 5, 'var(--c6)']])}</div>
        <div class="card"><h2>节点健康 <span class="sub">FE ${fe.length} · BE ${be.length} · CN ${cn.length}</span><span class="right"><a href="#nodes">节点管理 →</a></span></h2>
          <div class="tbl-wrap"><table><thead><tr><th>类型</th><th>节点</th><th>角色</th><th>状态</th><th>CPU</th><th>内存</th><th>磁盘</th><th>${sd ? '缓存命中' : 'Compaction'}</th></tr></thead><tbody>
          ${NODES.map(n => `<tr class="click" data-on="node" data-id="${n.id}"><td><span class="tag" style="color:${TYPE_META[n.type].color}">${TYPE_META[n.type].label}</span></td><td class="mono">${n.host}</td><td>${n.role || (n.type === 'be' ? 'BE' : 'CN')}</td><td>${stDot(n.status)}</td>
            <td style="width:100px">${meter(n.cpu)}</td><td style="width:100px">${meter(n.mem)}</td><td style="width:100px">${meter(n.disk, 75, 85)}</td>
            <td>${n.type === 'fe' ? `<span class="small muted">JVM ${n.jvm}%</span>` : sd ? (n.cacheHit ? n.cacheHit + '%' : '—') : n.compaction != null ? `<span class="${n.compaction > 100 ? 'up b' : ''}">${n.compaction}</span>` : '—'}</td></tr>`).join('')}
          </tbody></table></div></div>
      </div>
      <div class="grid" style="align-content:start">
        <div class="card"><h2>最近一次巡检 <span class="right"><a href="#inspect">查看 →</a></span></h2>
          <div class="score">${ring(last.score)}<div class="small"><div>${last.at}</div><div class="muted">${last.by} · 耗时 ${last.dur}</div><div style="margin-top:6px"><span class="tag bad">${last.bad} 异常</span> <span class="tag warn">${last.warn} 警告</span></div></div></div></div>
        <div class="card"><h2>活跃告警 <span class="sub">${ALERTS.filter(a => a.state === 'firing').length}</span><span class="right"><a href="#alert">告警中心 →</a></span></h2>
          ${ALERTS.slice(0, 4).map(a => `<div style="padding:8px 0;border-bottom:1px solid var(--border)"><div class="row"><span class="tag ${sevCls[a.lv]}">${sevText[a.lv]}</span><b class="small">${a.name}</b></div><div class="small muted">${a.inst} · ${a.time}</div></div>`).join('')}</div>
        <div class="card"><h2>集群信息</h2>
          ${[['集群', CUR.name], ['版本', CUR.version], ['架构', modeText(CUR.mode)], ['数据库 / 表', '14 / 1,286'], ['分区 / Tablet', '3.1 万 / 14.4 万'], ['默认副本数', '3'], ['物化视图', MVS.length + ' 个（1 个失败）'], ['Catalog', 'default_catalog, hive_ods, iceberg_lake']].map(([k, v]) => `<div class="row between small" style="padding:3px 0"><span class="muted">${k}</span><b>${v}</b></div>`).join('')}</div>
      </div>
    </div>`;
  }
};
ACTIONS['overview.refresh'] = () => { toast('已刷新（模拟数据）'); rerender(); };
ACTIONS['overview.export'] = () => { audit('overview.export', '导出概览报告'); toast('正在生成 PDF…（使用浏览器打印）'); setTimeout(() => window.print(), 300); };

/* 节点详情抽屉（多个页面共用） */
function nodeDetail(id) {
  const n = NODES.find(x => x.id === id); if (!n) return;
  const rows = Object.entries({
    '类型': TYPE_META[n.type].full, '角色': n.role || '—', '地址': `${n.host}`, '端口': n.type === 'fe' ? `query ${n.port} · http ${n.http} · rpc ${n.rpc} · edit_log ${n.editLog}` : `heartbeat ${n.port} · http ${n.http} · brpc ${n.brpc}${n.bePort ? ' · be_port ' + n.bePort : ''}${n.starlet ? ' · starlet ' + n.starlet : ''}`,
    '版本': n.version, '状态': nodeSt[n.status][1], '启动时间': n.startTime, '最近心跳': n.hb, '部署目录': n.dir,
    ...(n.type === 'fe' ? {'JVM 堆': n.jvm + '%', 'Journal / 已回放': `${n.journal} / ${n.replayed}`} : {}),
    ...(n.type === 'be' ? {'Tablet 数': n.tablets, '数据量': `${n.dataUsed} / ${n.dataTotal} TB`, 'Compaction Score': n.compaction, 'mem_limit': n.memLimit} : {}),
    ...(n.type === 'cn' ? {'Warehouse': n.warehouse, 'Data Cache 命中率': n.cacheHit ? n.cacheHit + '%' : '—'} : {}),
  });
  openDrawer(`${TYPE_META[n.type].label} · ${n.host}`, `
    <table>${rows.map(([k, v]) => `<tr><td class="muted" style="width:130px">${k}</td><td>${esc(v)}</td></tr>`).join('')}</table>
    <h4>资源（最近 60 分钟）</h4>${lineChart([{name: 'CPU%', color: 'var(--c1)', data: series(n.cpu, 60, n.cpu, 12)}, {name: '内存%', color: 'var(--c2)', data: series(n.mem + 3, 60, n.mem, 6)}], {h: 150, unit: '%'})}
    <h4>相关命令</h4>${code(n.type === 'fe' ? `SHOW FRONTENDS;\n-- 进程目录\n${n.dir}/bin/start_fe.sh --daemon\n${n.dir}/bin/stop_fe.sh` : n.type === 'be' ? `SHOW BACKENDS;\nSHOW PROC '/backends';\ncurl http://${n.host}:${n.http}/metrics | grep compaction_score` : `SHOW COMPUTE NODES;\n${n.dir.split(' ')[0]}/bin/start_cn.sh --daemon`)}
    <div class="row" style="margin-top:8px"><a href="#nodes">到节点管理执行操作 →</a></div>`);
}
ON.node = d => nodeDetail(d.id);

/* ---------- 集群架构 ---------- */
const TOPO_TEXT = {
  catalogs: () => 'External Catalog：hive_ods（Hive Metastore）· iceberg_lake（REST）',
  storage: () => ['对象存储 · s3://sr-shared-data/' + CUR.name, 'Storage Volume: builtin_storage_volume · 38.2 TB'],
};
pages.topology = {
  render() {
    const fe = NODES.filter(n => n.type === 'fe'), be = NODES.filter(n => n.type === 'be'), cn = NODES.filter(n => n.type === 'cn');
    const sd = CUR.mode === 'shared-data';
    const W = 960, box = (n, x, y, w = 140) => `<g class="nd" data-on="${can('topology.detail') ? 'node' : 'nodeNo'}" data-id="${n.id}"><rect x="${x}" y="${y}" width="${w}" height="54" rx="10" fill="var(--panel)" stroke="${n.status === 'warn' ? 'var(--warn)' : TYPE_META[n.type].color}" stroke-width="1.5"/>
      <text x="${x + 10}" y="${y + 20}" font-size="12" font-weight="600" fill="var(--text)">${TYPE_META[n.type].label}${n.role ? ' · ' + n.role : ''}</text>
      <text x="${x + 10}" y="${y + 38}" font-size="11" fill="var(--muted)" font-family="Consolas,monospace">${n.host}</text>
      <circle cx="${x + w - 14}" cy="${y + 16}" r="5" fill="${n.status === 'warn' ? 'var(--warn)' : 'var(--ok)'}"/></g>`;
    const row = (list, y, w = 140) => { const gap = 16, tot = list.length * w + (list.length - 1) * gap; let x = (W - tot) / 2; return list.map(n => { const s = box(n, x, y, w); x += w + gap; return s; }).join(''); };
    const H = sd ? 470 : 470;
    return `
    <div class="notice"><span>ℹ</span><div>${sd ? '<b>存算分离</b>：数据持久化在对象存储（S3 / HDFS / OSS 等），CN 节点无状态，本地盘作为 Data Cache；FE 管理元数据并通过 StarManager 管理 Shard。' : '<b>存算一体</b>：BE 同时负责数据存储与计算，数据按 Tablet 多副本分布在 BE 本地盘；CN 为可选的无状态计算节点（常用于湖分析弹性扩缩）。'}
      ${docLink('introduction/Architecture/', '架构说明 ↗')}</div></div>
    <div class="card"><h2>${CUR.name} 拓扑 <span class="sub">点击节点查看详情</span></h2>
    <svg class="topo" viewBox="0 0 ${W} ${H}">
      <text x="20" y="30" font-size="12" fill="var(--muted)">客户端（MySQL 协议 :9030 / HTTP :8030）</text>
      <rect x="20" y="40" width="${W - 40}" height="40" rx="8" fill="var(--panel2)" stroke="var(--border)"/><text x="${W / 2}" y="65" text-anchor="middle" font-size="12" fill="var(--text)">负载均衡（LVS / F5 / ProxySQL）→ FE query_port 9030</text>
      <line x1="${W / 2}" y1="80" x2="${W / 2}" y2="110" stroke="var(--border)" stroke-width="2"/>
      <text x="20" y="104" font-size="12" fill="var(--muted)">Frontend · 元数据（BDB JE 复制）/ 查询规划</text>
      ${row(fe, 112, 170)}
      <line x1="${W / 2}" y1="166" x2="${W / 2}" y2="210" stroke="var(--border)" stroke-width="2"/>
      <text x="20" y="204" font-size="12" fill="var(--muted)">${sd ? 'Compute Node · 无状态计算 + Data Cache' : 'Backend · 存储 + 计算（Tablet 多副本）'}</text>
      ${sd ? row(cn, 212) : row(be, 212, 130)}
      ${!sd && cn.length ? `<text x="20" y="300" font-size="12" fill="var(--muted)">Compute Node · 弹性计算（湖查询）</text>${row(cn, 308)}` : ''}
      ${sd ? `<line x1="${W / 2}" y1="266" x2="${W / 2}" y2="340" stroke="var(--border)" stroke-width="2" stroke-dasharray="4 4"/>
        <rect x="160" y="340" width="${W - 320}" height="70" rx="12" fill="var(--accent-soft)" stroke="var(--accent)"/>
        <text x="${W / 2}" y="370" text-anchor="middle" font-size="13" font-weight="600" fill="var(--text)">${esc(TOPO_TEXT.storage()[0])}</text>
        <text x="${W / 2}" y="392" text-anchor="middle" font-size="11" fill="var(--muted)">${esc(TOPO_TEXT.storage()[1])}</text>` :
        `<rect x="160" y="390" width="${W - 320}" height="56" rx="12" fill="var(--panel2)" stroke="var(--border)" stroke-dasharray="4 4"/>
        <text x="${W / 2}" y="414" text-anchor="middle" font-size="12" fill="var(--text)">${esc(TOPO_TEXT.catalogs())}</text>
        <text x="${W / 2}" y="434" text-anchor="middle" font-size="11" fill="var(--muted)">湖查询由 BE / CN 直接读取 HDFS / S3</text>`}
    </svg></div>
    <div class="grid g3" style="margin-top:16px">
      ${Object.entries(TYPE_META).map(([k, m]) => `<div class="card"><h2><span class="tag" style="color:${m.color}">${m.label}</span>${m.full}</h2><div class="small muted">${{
        fe: 'Leader 负责元数据写入；Follower 参与选主并可读；Observer 不参与选主，用于扩展查询并发。生产建议 3 个 Follower（含 Leader）。',
        be: '存储数据（Tablet / 多副本）与执行查询；单机多盘配置 storage_root_path。缩容需 DECOMMISSION 迁移完数据后再移除。',
        cn: '无状态，仅计算；存算分离下用 Data Cache 缓存热数据；可快速扩缩容。'}[k]}</div></div>`).join('')}
    </div>`;
  }
};
ON.nodeNo = () => toast('无权限：需要「查看节点详情」(topology.detail)');
