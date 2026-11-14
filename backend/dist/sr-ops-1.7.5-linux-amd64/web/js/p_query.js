/* 查询分析 / 资源隔离 */
pages.query = {
  render(sub) {
    sub = sub || 'running';
    return tabs('query', [['running', `运行中查询 (${RUNNING.length})`], ['slow', '慢查询 Top SQL'], ['blacklist', 'SQL 黑名单']], sub) + this[sub]();
  },
  running() {
    return `<div class="notice"><span>ℹ</span><div>数据来源：<code>SHOW PROC '/current_queries'</code>（全部 FE 汇总）。终止查询执行 <code>KILL QUERY '&lt;query_id&gt;'</code>，仅终止该查询，不断开连接。</div></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>Query ID</th><th>用户 / 库</th><th>资源组</th><th>FE</th><th>已运行</th><th>扫描</th><th>内存</th><th>CPU 时间</th><th>SQL</th><th>操作</th></tr></thead><tbody>
    ${RUNNING.map(q => `<tr><td class="mono small" title="${q.qid}">${q.qid.slice(0, 8)}…</td><td>${q.user}<div class="small muted">${q.db}</div></td><td><span class="tag">${q.rg}</span></td><td>${q.fe}</td>
      <td class="${q.sec > 60 ? 'up b' : ''}">${q.sec}s</td><td class="small">${q.scan}</td><td>${q.mem}</td><td>${q.cpu}</td><td class="mono small" style="max-width:320px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc(q.sql)}">${esc(q.sql)}</td>
      <td class="ops">${B('query.profile', 'Profile', {cls: 'sm', data: {q: q.qid}})}${B('query.kill', '终止', {cls: 'sm danger', data: {q: q.qid}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  slow() {
    return `<div class="toolbar"><select><option>最近 1 小时</option><option>最近 24 小时</option><option>最近 7 天</option></select><select><option>耗时 &gt; 5s</option><option>耗时 &gt; 1s</option><option>扫描 &gt; 1 亿行</option></select><input type="search" placeholder="按 SQL / 用户过滤"><span class="sp"></span>${B('query.export', '⇩ 导出')}</div>
    <div class="notice"><span>ℹ</span><div>按 <b>SQL 指纹（Digest）</b>聚合审计日志（AuditLoader 写入 <code>starrocks_audit_db__.starrocks_audit_tbl__</code>）。${docLink('administration/management/audit_loader/', 'AuditLoader ↗')} ${docLink('administration/sql_digest/', 'SQL Digest ↗')}</div></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>指纹</th><th>SQL 模板</th><th>用户</th><th>次数</th><th>平均(s)</th><th>最大(s)</th><th>扫描行</th><th>峰值内存</th><th>诊断</th><th>操作</th></tr></thead><tbody>
    ${SLOW.map(s => `<tr><td class="mono small">${s.digest}</td><td class="mono small" style="max-width:360px">${esc(s.sql)}</td><td>${s.user}</td><td>${s.cnt}</td><td>${s.avg}</td><td class="${s.max > 60 ? 'up b' : ''}">${s.max}</td><td>${s.scanRows}</td><td>${s.mem}</td>
      <td class="small">${s.issue === '—' ? '<span class="muted">—</span>' : `<span class="tag warn">${s.issue}</span>`}</td><td class="ops">${B('query.profile', 'Profile', {cls: 'sm', data: {q: s.digest}})}${B('query.explain', '执行计划', {cls: 'sm', data: {d: s.digest}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  blacklist() {
    return `<div class="notice warn"><span>⚠</span><div>SQL 黑名单命中后查询直接报错拒绝。需 FE 配置 <code>enable_sql_blacklist = true</code>。${docLink('administration/Blacklist/', 'SQL 黑名单 ↗')}</div></div>
    <div class="card"><h2>黑名单规则 <span class="right">${B('query.blacklist', '＋ 添加规则', {cls: 'sm primary'})}</span></h2>
    <div class="tbl-wrap"><table><thead><tr><th>ID</th><th>正则</th><th>添加人</th><th>时间</th><th>操作</th></tr></thead><tbody>
    ${SQL_BLACKLIST.map(b => `<tr><td>${b.id}</td><td class="mono">${esc(b.pattern)}</td><td>${b.by}</td><td>${b.at}</td><td>${B('query.blacklist', '删除', {cls: 'sm danger', data: {del: b.id}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  }
};
ACTIONS['query.kill'] = d => risky('query.kill', '终止查询', `将终止查询 <code>${d.q}</code>，客户端会收到错误。`, `KILL QUERY '${d.q}';`, () => {
  const i = RUNNING.findIndex(q => q.qid === d.q); if (i >= 0) RUNNING.splice(i, 1);
  audit('query.kill', 'KILL QUERY ' + d.q.slice(0, 8), '成功', `KILL QUERY '${d.q}';`); toast('已终止'); rerender();
});
ACTIONS['query.profile'] = () => {
  const P = PROFILE, max = Math.max(...P.tree.map(t => t.t));
  openDrawer('Query Profile', `<div class="row small"><span class="tag">总耗时 ${P.total}</span><span class="tag">Planning ${P.planning}</span><span class="tag">排队 ${P.queue}</span><span class="tag">峰值内存 ${P.mem}</span><span class="tag">扫描 ${P.scan}</span></div>
    <h4>算子树（耗时占比）</h4>
    <div class="tbl-wrap"><table><thead><tr><th>算子</th><th>输出行</th><th style="width:200px">耗时</th></tr></thead><tbody>
    ${P.tree.map(t => `<tr><td class="mono small" style="padding-left:${10 + t.lv * 14}px">${t.lv ? '└ ' : ''}${t.hot ? '<b style="color:var(--bad)">' + t.id + '</b>' : t.id}</td><td class="small">${t.rows}</td><td><div class="row" style="gap:6px;flex-wrap:nowrap"><div class="meter" style="flex:1"><b class="${t.hot ? 'bad' : ''}" style="width:${t.t / max * 100}%"></b></div><span class="small">${t.t}s</span></div></td></tr>`).join('')}
    </tbody></table></div>
    <h4>优化建议</h4><ol>${P.advice.map(a => `<li>${a}</li>`).join('')}</ol>
    <h4>获取方式</h4>${code("SET enable_profile = true;\n-- 执行 SQL 后\nSHOW PROFILELIST;\nANALYZE PROFILE FROM '<query_id>';")}
    ${docLink('best_practices/query_tuning/query_profile_overview/', 'Query Profile 说明 ↗')}`, true);
};
ACTIONS['query.explain'] = d => {
  const s = SLOW.find(x => x.digest === d.d);
  openDrawer('执行计划分析', `${code('EXPLAIN COSTS\n' + s.sql)}
  ${code(`PLAN FRAGMENT 1
  6:HASH JOIN
  |  join op: INNER JOIN (PARTITIONED)
  |  equal join conjunct: 3: sku_id = 9: id
  |  cardinality: 320,000,000
  |----5:EXCHANGE  (HASH_PARTITIONED: 9: id)
  2:EXCHANGE  (HASH_PARTITIONED: 3: sku_id)
  0:OlapScanNode
     TABLE: fact_order  partitions=279/280  rollup: fact_order
     tabletRatio=4464/4480  cardinality=320,000,000`)}
  <div class="notice warn"><span>!</span><div><b>诊断：</b>${s.issue}。小表 dim_sku 统计信息为 18 天前（ANALYZE 过期），优化器低估其行数选择了 Shuffle Join。</div></div>
  ${code('ANALYZE TABLE dim.dim_sku;\n-- 或临时提示\nSELECT ... FROM dws.fact_order o JOIN [BROADCAST] dim.dim_sku s ON ...')}`, true);
};
ACTIONS['query.export'] = () => { audit('query.export', '导出慢查询 Top SQL'); toast('已导出 slow_sql_top.csv（模拟）'); };
ACTIONS['query.blacklist'] = d => {
  if (d.del) return risky('query.blacklist', '删除黑名单规则', `删除规则 #${d.del} 后，匹配的 SQL 将恢复可执行。`, `DELETE SQLBLACKLIST ${d.del};`, () => { SQL_BLACKLIST.splice(SQL_BLACKLIST.findIndex(b => b.id == d.del), 1); audit('query.blacklist', '删除 SQL 黑名单 #' + d.del, '成功', `DELETE SQLBLACKLIST ${d.del};`); rerender(); });
  openModal({title: '添加 SQL 黑名单', code: 'query.blacklist', body: `<div class="form"><label class="req">正则表达式</label><input id="blP" class="mono" placeholder="select \\* from big_table"><div class="hint">大小写不敏感，匹配整条 SQL；特殊字符需转义。</div></div>`,
    onOk() { const p = $('#blP').value.trim(); if (!p) { toast('请输入正则'); return false; } SQL_BLACKLIST.push({id: SQL_BLACKLIST.length + 2, pattern: p, by: ME.login, at: now()}); audit('query.blacklist', '添加 SQL 黑名单', '成功', `ADD SQLBLACKLIST "${p}";`); rerender(); }});
};

/* ---------- 资源隔离 ---------- */
pages.resource = {
  render(sub) {
    sub = sub || 'groups';
    return tabs('resource', [['groups', '资源组'], ['queue', '查询队列']], sub) + (sub === 'queue' ? this.queue() : this.groups());
  },
  groups() {
    return `<div class="notice"><span>ℹ</span><div>资源组按分类器（用户 / 角色 / 查询类型 / 来源 IP / 库）匹配查询，限制 CPU 权重、内存比例、并发与大查询熔断。${docLink('administration/management/resource_management/resource_group/', '资源组 ↗')}</div></div>
    <div class="card"><h2>资源组 <span class="sub">${RES_GROUPS.length} 个</span><span class="right">${B('rg.create', '＋ 新建资源组', {cls: 'sm primary'})}</span></h2>
    <div class="tbl-wrap"><table><thead><tr><th>名称</th><th>CPU 权重</th><th>独占核</th><th>内存上限</th><th>并发上限</th><th>大查询熔断（CPU 秒 / 扫描行 / 内存）</th><th>分类器</th><th>运行 / 排队</th><th>操作</th></tr></thead><tbody>
    ${RES_GROUPS.map(g => `<tr><td><b>${g.name}</b></td><td>${g.cpu}</td><td>${g.exclusive || '—'}</td><td>${g.mem}</td><td>${g.conc || '不限'}</td><td class="small">${g.bigCpu ? g.bigCpu + 's' : '—'} / ${g.bigScan} / ${g.bigMem}</td><td class="mono small">${esc(g.cls)}</td><td>${g.running} / <span class="${g.queued ? 'up b' : ''}">${g.queued}</span></td>
      <td class="ops">${B('rg.edit', '编辑', {cls: 'sm', data: {n: g.name}})}${B('rg.delete', '删除', {cls: 'sm danger', data: {n: g.name}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  queue() {
    const q = QUEUE_CFG;
    return `<div class="grid g2"><div class="card"><h2>查询队列 <span class="right">${B('queue.edit', '编辑', {cls: 'sm'})}</span></h2>
      ${[['SELECT 查询排队', q.select], ['统计信息查询排队', q.statistic], ['导入排队', q.load]].map(([k, v]) => `<div class="row between small" style="padding:6px 0;border-bottom:1px solid var(--border)"><span class="muted">${k}</span>${v ? '<span class="tag ok">开启</span>' : '<span class="tag">关闭</span>'}</div>`).join('')}
      ${[['单 BE 并发上限 query_queue_concurrency_limit', q.conc], ['内存使用率上限 query_queue_mem_used_pct_limit', q.mem], ['最大排队数 query_queue_max_queued_queries', q.maxQueued], ['排队超时 query_queue_pending_timeout_second', q.timeout + ' s']].map(([k, v]) => `<div class="row between small" style="padding:6px 0;border-bottom:1px solid var(--border)"><span class="muted mono">${k}</span><b>${v}</b></div>`).join('')}
    </div><div class="card"><h2>当前排队</h2>${lineChart([{name: '排队查询数', color: 'var(--c3)', data: series(5, 60, 2, 3, [40, 50, 6])}], {h: 140})}
      <div class="small muted">开启后，资源超过阈值的新查询进入队列等待而不是直接执行或失败。${docLink('administration/management/resource_management/query_queues/', '查询队列 ↗')}</div></div></div>`;
  }
};
function rgForm(g = {}) {
  return `<div class="form"><label class="req">名称</label><input id="rgN" value="${g.name || ''}" ${g.name ? 'disabled' : ''}>
  <label class="req">CPU 权重 cpu_weight</label><input id="rgC" type="number" value="${g.cpu || 4}"><label>独占核 exclusive_cpu_cores</label><input type="number" value="${g.exclusive || 0}">
  <label class="req">内存上限 mem_limit</label><input value="${g.mem || '20%'}"><label>并发上限 concurrency_limit</label><input type="number" value="${g.conc || 0}">
  <label>大查询 CPU 秒</label><input value="${g.bigCpu || 0}"><label>大查询扫描行</label><input value="${g.bigScan || ''}"><label>大查询内存</label><input value="${g.bigMem || ''}">
  <label class="req">分类器</label><input class="mono" id="rgCls" value="${esc(g.cls || "user='xxx'")}"></div>`;
}
const rgSql = (g, op) => `${op} RESOURCE GROUP ${g.name}${op === 'CREATE' ? `\nTO (${g.cls})` : ''}\nWITH (\n  "cpu_weight" = "${g.cpu}",\n  "mem_limit" = "${g.mem}",\n  "concurrency_limit" = "${g.conc || 0}",\n  "big_query_cpu_second_limit" = "${g.bigCpu || 0}"\n);`;
ACTIONS['rg.create'] = () => openModal({title: '新建资源组', code: 'rg.create', body: rgForm(), onOk() {
  const g = {name: $('#rgN').value || 'rg_new', cpu: +$('#rgC').value, mem: '20%', conc: 0, spill: '—', bigCpu: 0, bigScan: '—', bigMem: '—', cls: $('#rgCls').value, running: 0, queued: 0};
  RES_GROUPS.push(g); audit('rg.create', '新建资源组 ' + g.name, '成功', rgSql(g, 'CREATE')); toast('已创建'); rerender(); }});
ACTIONS['rg.edit'] = d => { const g = RES_GROUPS.find(x => x.name === d.n); openModal({title: '编辑资源组 ' + g.name, code: 'rg.edit', body: rgForm(g) + code(rgSql(g, 'ALTER')), onOk() { g.cpu = +$('#rgC').value; audit('rg.edit', '编辑资源组 ' + g.name, '成功', rgSql(g, 'ALTER')); toast('已保存'); rerender(); }}); };
ACTIONS['rg.delete'] = d => risky('rg.delete', '删除资源组 ' + d.n, '删除后匹配该资源组的查询将落入 default_wg。', `DROP RESOURCE GROUP ${d.n};`, () => { RES_GROUPS.splice(RES_GROUPS.findIndex(x => x.name === d.n), 1); audit('rg.delete', '删除资源组 ' + d.n, '成功', `DROP RESOURCE GROUP ${d.n};`); rerender(); });
ACTIONS['queue.edit'] = () => { const q = QUEUE_CFG; openModal({title: '查询队列配置', code: 'queue.edit', body: `<div class="form">
  <label>SELECT 排队</label><label class="chk"><input type="checkbox" id="qS" ${q.select ? 'checked' : ''}> enable_query_queue_select</label>
  <label>导入排队</label><label class="chk"><input type="checkbox" id="qL" ${q.load ? 'checked' : ''}> enable_query_queue_load</label>
  <label>并发上限</label><input type="number" id="qC" value="${q.conc}"><label>排队超时 (s)</label><input type="number" id="qT" value="${q.timeout}"></div>`,
  onOk() { q.select = $('#qS').checked; q.load = $('#qL').checked; q.conc = +$('#qC').value; q.timeout = +$('#qT').value; audit('queue.edit', '修改查询队列配置', '成功', `SET GLOBAL enable_query_queue_select = ${q.select};\nSET GLOBAL query_queue_concurrency_limit = ${q.conc};`); rerender(); }}); };
