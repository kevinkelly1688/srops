// @author: kevinkelly
// @license: Apache Licence
// @file: live_ops.js
// @time: 2026/10/28

/* ============================================================================
   真实模式：直接通过 SQL 操作集群的页面（资源隔离 / 导入作业 / 物化视图 / ...）
   依赖 live.js 的 api()、liveLoad()、loadingCard()、errCard()、livePending()、liveFail()。
   每个页面：读取接口 → 渲染；按钮 → 调接口 → 刷新。高危操作走服务端双人复核（接口返回 202 即为待复核）。
   ========================================================================== */
const OPS = {};                                   // 页面数据缓存：OPS[页面][集群 ID]
const opsBase = () => '/api/clusters/' + encodeURIComponent(CUR.id);
const opsEnc = encodeURIComponent;
function opsGet(key, path) {                      // 取缓存；没有则异步加载，返回 null
  OPS[key] = OPS[key] || {};
  const id = CUR.id;
  if (OPS[key][id]) return OPS[key][id];
  liveLoad('ops-' + key, id, async () => { OPS[key][id] = await api('GET', `${opsBase()}${path}`); });
  return null;
}
const opsErr = key => LIVE.err['ops-' + key + CUR.id];
const opsReload = (...keys) => { keys.forEach(k => { if (OPS[k]) delete OPS[k][CUR.id]; }); rerender(); };
const opsLoading = (key, msg, retry) => opsErr(key) ? errCard(opsErr(key), retry) : loadingCard(msg);
ON.opsRetry = () => { Object.keys(OPS).forEach(k => delete OPS[k][CUR.id]); rerender(); };
const warnNotice = w => (w && w.length) ? `<div class="notice warn"><span>!</span><div>${w.map(esc).join('；')}</div></div>` : '';
const opsTag = (s, map) => { const c = (map || {})[s] || ''; return `<span class="tag ${c}">${esc(s || '—')}</span>`; };
const JOB_CLS = {RUNNING: 'info', PENDING: 'info', NEED_SCHEDULE: 'info', SUCCESS: 'ok', FINISHED: 'ok', PAUSED: 'warn', FAILED: 'bad', CANCELLED: 'bad', CANCELED: 'bad', UNSTABLE: 'warn'};
const jobTagL = s => opsTag(s, JOB_CLS);
const gb = b => b > 0 ? (b / 1073741824).toFixed(b >= 1073741824 * 10 ? 0 : 1) + ' GB' : '—';

/* 高危操作确认（真实模式）：输入集群名；开启双人复核时按钮为“提交复核”，由服务端决定是否立即执行 */
function liveRisky(pcode, title, detail, sql, run) {
  const needInput = SETTINGS.highRiskConfirm;
  openModal({title: '⚠ ' + title, okCls: 'danger primary', ok: SETTINGS.dualReview ? '提交复核' : '确认执行', code: pcode,
    body: `<div class="notice bad"><span>⚠</span><div>${detail}</div></div>
      ${sql ? `<div class="small muted">将执行：</div>${code(sql)}` : ''}
      ${SETTINGS.dualReview ? '<div class="notice warn"><span>👥</span><div>平台已开启<b>双人复核</b>：提交后需另一名拥有同权限的用户在「操作审计 → 待复核」中批准后才会执行。</div></div>' : ''}
      ${needInput ? `<div class="form" style="margin-top:8px"><label class="req">集群名确认</label><input id="riskInput" placeholder="请输入 ${esc(CUR.name)}"></div>` : ''}`,
    onOk() {
      if (needInput && $('#riskInput').value.trim() !== CUR.name) { toast('集群名不匹配'); return false; }
      run(); return false;
    }});
}
/* 调接口并统一处理：待复核 / 成功 / 失败 */
function opsCall(method, path, body, okMsg, reload) {
  return api(method, opsBase() + path, body).then(res => {
    if (livePending(res)) return res;
    closeModal(); toast(okMsg || '已执行'); opsReload(...(reload || [])); return res;
  }).catch(liveFail);
}

/* ============================ 资源隔离 ============================ */
const _resourceDemo = {render: pages.resource.render, groups: pages.resource.groups, queue: pages.resource.queue};
pages.resource.render = function (sub) {
  if (!LIVE.on) return _resourceDemo.render.call(this, sub);
  sub = sub || 'groups';
  return tabs('resource', [['groups', '资源组'], ['queue', '查询队列']], sub) + (sub === 'queue' ? this.queue() : this.groups());
};
pages.resource.groups = function () {
  if (!LIVE.on) return _resourceDemo.groups.call(this);
  const d = opsGet('rg', '/resource-groups');
  if (!d) return opsLoading('rg', '正在读取 SHOW RESOURCE GROUPS…', 'opsRetry');
  const cpu = g => g.exclusiveCpuCores > 0 ? `独占 ${g.exclusiveCpuCores} 核` : g.cpuWeight;
  const big = g => (g.bigCpuSec || g.bigScanRows || g.bigMem) ? `${g.bigCpuSec ? g.bigCpuSec + ' s' : '—'} / ${g.bigScanRows ? fmt(g.bigScanRows) + ' 行' : '—'} / ${gb(g.bigMem)}` : '<span class="muted">未设置</span>';
  const cls = g => g.classifiers.length ? g.classifiers.map(c => `<div class="mono small">${esc(clsText(c))}</div>`).join('') : '<span class="muted small">—</span>';
  return `${warnNotice(d.warnings)}<div class="notice"><span>ℹ</span><div>资源组按分类器（用户 / 角色 / 查询类型 / 来源 IP / 库）匹配查询，限制 CPU、内存、并发与大查询熔断。数据来自 <code>SHOW RESOURCE GROUPS ALL</code> 与 <code>SHOW USAGE RESOURCE GROUPS</code>。${docLink('administration/management/resource_management/resource_group/', '资源组 ↗')}</div></div>
    <div class="card"><h2>资源组 <span class="sub">${d.groups.length} 个</span><span class="right"><button class="btn sm" data-on="opsRetry">⟳ 刷新</button> ${B('rg.create', '＋ 新建资源组', {cls: 'sm primary'})}</span></h2>
    <div class="tbl-wrap"><table><thead><tr><th>名称</th><th>CPU</th><th>内存上限</th><th>并发上限</th><th>大查询熔断（CPU / 扫描 / 内存）</th><th>落盘阈值</th><th>分类器</th><th>运行 / 排队</th><th>操作</th></tr></thead><tbody>
    ${d.groups.map(g => `<tr><td><b>${esc(g.name)}</b>${g.builtin ? ' <span class="tag">内置</span>' : ''}</td><td>${cpu(g)}</td><td>${g.memPct ? g.memPct + '%' : '不限'}</td><td>${g.concurrency || '不限'}</td><td class="small">${big(g)}</td><td>${g.spillPct && g.spillPct < 100 ? g.spillPct + '%' : '—'}</td><td>${cls(g)}</td>
      <td>${g.running} / ${g.queued < 0 ? '<span class="muted">—</span>' : `<span class="${g.queued ? 'up b' : ''}">${g.queued}</span>`}</td>
      <td class="ops">${g.builtin ? '<span class="small muted">系统内置</span>' : B('rg.edit', '编辑', {cls: 'sm', data: {n: g.name}}) + B('rg.delete', '删除', {cls: 'sm danger', data: {n: g.name}})}</td></tr>`).join('')}
    </tbody></table></div>
    <div class="small muted" style="margin-top:8px">运行数来自各 BE / CN 正在使用该资源组的查询合计；排队数来自 SHOW RUNNING QUERIES（3.3 及以上）。</div></div>`;
};
const clsText = c => [c.user && 'user=' + c.user, c.role && 'role=' + c.role, (c.queryType || []).length && 'query_type in (' + c.queryType.join(',') + ')', c.sourceIp && 'source_ip=' + c.sourceIp, c.db && 'db=' + c.db].filter(Boolean).join(', ');
pages.resource.queue = function () {
  if (!LIVE.on) return _resourceDemo.queue.call(this);
  const d = opsGet('queue', '/query-queue');
  if (!d) return opsLoading('queue', '正在读取查询队列配置…', 'opsRetry');
  const it = k => d.items.find(x => x.key === k);
  const flag = (k, label) => { const x = it(k); return x ? `<div class="row between small" style="padding:6px 0;border-bottom:1px solid var(--border)"><span class="muted">${label}</span>${x.value === 'true' ? '<span class="tag ok">开启</span>' : '<span class="tag">关闭</span>'}</div>` : ''; };
  const num = (k, label) => { const x = it(k); return x ? `<div class="row between small" style="padding:6px 0;border-bottom:1px solid var(--border)"><span class="muted mono">${k} <span class="muted">${label}</span></span><b>${esc(x.value)}</b></div>` : ''; };
  return `<div class="grid g2"><div class="card"><h2>查询队列 <span class="right"><button class="btn sm" data-on="opsRetry">⟳</button> ${B('queue.edit', '编辑', {cls: 'sm'})}</span></h2>
      ${flag('enable_query_queue_select', 'SELECT 查询排队 enable_query_queue_select')}${flag('enable_query_queue_statistic', '统计信息查询排队 enable_query_queue_statistic')}${flag('enable_query_queue_load', '导入排队 enable_query_queue_load')}
      ${num('query_queue_concurrency_limit', '单 BE 并发上限')}${num('query_queue_mem_used_pct_limit', '内存使用率上限 %')}${num('query_queue_cpu_used_permille_limit', 'CPU 使用率上限 ‰')}${num('query_queue_max_queued_queries', '最大排队数')}${num('query_queue_pending_timeout_second', '排队超时 s')}
      ${d.items.length ? '' : '<div class="muted small">当前版本没有查询队列相关配置。</div>'}
    </div><div class="card"><h2>当前状态</h2>
      <div class="grid g2"><div class="kpi"><div class="l">排队中</div><div class="v ${d.pending > 0 ? 'up' : ''}">${d.pending < 0 ? '—' : d.pending}</div></div><div class="kpi"><div class="l">运行中</div><div class="v">${d.running < 0 ? '—' : d.running}</div></div></div>
      <div class="small muted" style="margin-top:8px">开启后，资源超过阈值的新查询进入队列等待而不是直接执行或失败。数据来自 <code>SHOW RUNNING QUERIES</code>（3.3 及以上）。${docLink('administration/management/resource_management/query_queues/', '查询队列 ↗')}</div></div></div>
      <div class="notice" style="margin-top:12px"><span>ℹ</span><div>全局变量通过 <code>SET GLOBAL</code> 修改并持久化；FE 配置项通过 <code>ADMIN SET FRONTEND CONFIG</code> 在<b>每个存活的 FE</b> 上逐台执行，仅在内存中生效，FE 重启后恢复为配置文件中的值（持久化到 fe.conf 将在后续版本提供）。</div></div>`;
};

/* ----- 资源组表单 ----- */
const QTYPES = ['select', 'insert', 'mv', 'update', 'delete'];
const clsRow = (c = {}) => `<div class="cls-row" style="display:grid;grid-template-columns:repeat(4,1fr) auto;gap:6px;align-items:center;margin-bottom:6px;padding:8px;border:1px solid var(--border);border-radius:6px">
  <input data-c="user" placeholder="用户 user" value="${esc(c.user || '')}"><input data-c="role" placeholder="角色 role" value="${esc(c.role || '')}"><input data-c="sourceIp" placeholder="来源 IP / CIDR，如 10.1.%" value="${esc(c.sourceIp || '')}"><input data-c="db" placeholder="库 db" value="${esc(c.db || '')}">
  <button class="btn sm danger" type="button" data-on="rgDelCls" title="删除该分类器">✕</button>
  <div style="grid-column:1/-1" class="small">查询类型：${QTYPES.map(q => `<label class="chk" style="display:inline-flex;margin-right:10px"><input type="checkbox" data-q="${q}" ${(c.queryType || []).includes(q) ? 'checked' : ''}> ${q}</label>`).join('')}<span class="muted">（不选 = 不限）</span></div></div>`;
function rgFormHtml(g) {
  const e = !!g;
  g = g || {cpuWeight: 4, exclusiveCpuCores: 0, memPct: 20, concurrency: 0, bigCpuSec: 0, bigScanRows: 0, bigMem: 0, spillPct: 0, classifiers: [{}]};
  return `<div class="form"><label class="req">名称</label><input id="rgN" value="${esc(g.name || '')}" ${e ? 'disabled' : ''} placeholder="字母、数字、下划线">
    <label>CPU 权重 cpu_weight</label><input id="rgC" type="number" min="0" value="${g.cpuWeight}"><label>独占核数 exclusive_cpu_cores</label><input id="rgX" type="number" min="0" value="${g.exclusiveCpuCores}"><div class="hint">二选一：使用权重时独占核填 0；使用独占核时权重填 0</div>
    <label>内存上限 mem_limit (%)</label><input id="rgM" type="number" min="0" max="100" value="${g.memPct}"><div class="hint">0 = 不限制</div>
    <label>并发上限 concurrency_limit</label><input id="rgK" type="number" min="0" value="${g.concurrency}"><div class="hint">0 = 不限制</div>
    <label>大查询：CPU 秒</label><input id="rgB1" type="number" min="0" value="${g.bigCpuSec}"><label>大查询：扫描行数</label><input id="rgB2" type="number" min="0" value="${g.bigScanRows}"><label>大查询：内存 (GB)</label><input id="rgB3" type="number" min="0" step="0.1" value="${g.bigMem ? +(g.bigMem / 1073741824).toFixed(2) : 0}"><div class="hint">超过任一阈值的查询会被终止；0 = 不设置</div>
    <label>落盘阈值 (%)</label><input id="rgS" type="number" min="0" max="100" value="${g.spillPct && g.spillPct < 100 ? g.spillPct : 0}"><div class="hint">spill_mem_limit_threshold，0 = 不设置</div></div>
    <h4 style="margin:12px 0 6px">分类器 <span class="small muted">各分类器之间为“或”，同一分类器内各条件为“与”</span></h4><div id="rgClsBox">${g.classifiers.map(clsRow).join('')}</div>
    <button class="btn sm" type="button" data-on="rgAddCls">＋ 添加分类器</button>`;
}
ON.rgAddCls = () => $('#rgClsBox').insertAdjacentHTML('beforeend', clsRow());
ON.rgDelCls = (d, el) => { const rows = $$('#rgClsBox .cls-row'); if (rows.length <= 1) return toast('至少保留一个分类器'); el.closest('.cls-row').remove(); };
function readRgForm() {
  const n = id => +$(id).value || 0;
  const classifiers = $$('#rgClsBox .cls-row').map(r => {
    const c = {}; $$('input[data-c]', r).forEach(i => { if (i.value.trim()) c[i.dataset.c] = i.value.trim(); });
    const q = $$('input[data-q]:checked', r).map(i => i.dataset.q); if (q.length) c.queryType = q; return c;
  });
  return {name: $('#rgN').value.trim(), cpuWeight: n('#rgC'), exclusiveCpuCores: n('#rgX'), memPct: n('#rgM'), concurrency: n('#rgK'), bigCpuSec: n('#rgB1'), bigScanRows: n('#rgB2'),
    bigMem: Math.round(n('#rgB3') * 1073741824), spillPct: n('#rgS'), classifiers};
}
const _rg = {create: ACTIONS['rg.create'], edit: ACTIONS['rg.edit'], del: ACTIONS['rg.delete'], queue: ACTIONS['queue.edit']};
ACTIONS['rg.create'] = d => {
  if (!LIVE.on) return _rg.create(d);
  openModal({title: '新建资源组', code: 'rg.create', wide: true, body: rgFormHtml(), onOk() {
    const b = readRgForm(); if (!b.name) { toast('请输入名称'); return false; }
    opsCall('POST', '/resource-groups', b, '已创建 ' + b.name, ['rg']); return false;
  }});
};
ACTIONS['rg.edit'] = d => {
  if (!LIVE.on) return _rg.edit(d);
  const g = ((OPS.rg || {})[CUR.id] || {groups: []}).groups.find(x => x.name === d.n); if (!g) return;
  openModal({title: '编辑资源组 ' + g.name, code: 'rg.edit', wide: true, body: rgFormHtml(g), onOk() {
    opsCall('PUT', '/resource-groups/' + opsEnc(g.name), readRgForm(), '已保存 ' + g.name, ['rg']); return false;
  }});
};
ACTIONS['rg.delete'] = d => {
  if (!LIVE.on) return _rg.del(d);
  liveRisky('rg.delete', '删除资源组 ' + esc(d.n), '删除后匹配该资源组的查询将落入 default_wg。', 'DROP RESOURCE GROUP `' + d.n + '`;', () => opsCall('DELETE', '/resource-groups/' + opsEnc(d.n), undefined, '已删除 ' + d.n, ['rg']));
};
ACTIONS['queue.edit'] = () => {
  if (!LIVE.on) return _rg.queue();
  const d = (OPS.queue || {})[CUR.id]; if (!d) return;
  const it = k => d.items.find(x => x.key === k);
  const chk = (k, label) => it(k) ? `<label>${label}</label><label class="chk"><input type="checkbox" data-k="${k}" ${it(k).value === 'true' ? 'checked' : ''}> ${k}</label>` : '';
  const num = (k, label) => it(k) ? `<label>${label}</label><input type="number" min="0" data-k="${k}" value="${esc(it(k).value)}"><div class="hint mono">${k}（0 = 不限制）</div>` : '';
  openModal({title: '查询队列配置', code: 'queue.edit', body: `<div class="form">${chk('enable_query_queue_select', 'SELECT 排队')}${chk('enable_query_queue_statistic', '统计信息排队')}${chk('enable_query_queue_load', '导入排队')}
    ${num('query_queue_concurrency_limit', '并发上限')}${num('query_queue_mem_used_pct_limit', '内存使用率上限 (%)')}${num('query_queue_cpu_used_permille_limit', 'CPU 使用率上限 (‰)')}${num('query_queue_max_queued_queries', '最大排队数')}${num('query_queue_pending_timeout_second', '排队超时 (s)')}</div>`,
    onOk() {
      const values = {}; $$('#modal [data-k]').forEach(i => { values[i.dataset.k] = i.type === 'checkbox' ? String(i.checked) : String(i.value).trim(); });
      api('PUT', opsBase() + '/query-queue', {values}).then(r => { closeModal(); toast(r.changed === false ? '没有变化' : '已保存'); opsReload('queue'); }).catch(liveFail); return false;
    }});
};

/* ============================ 导入作业 ============================ */
const _loadDemo = pages.load.render;
pages.load.render = function () {
  if (!LIVE.on) return _loadDemo.call(this);
  const d = opsGet('load', '/loads');
  if (!d) return opsLoading('load', '正在读取 Routine Load 与导入记录…', 'opsRetry');
  const jobs = d.jobs, run = jobs.filter(j => j.state === 'RUNNING' || j.state === 'NEED_SCHEDULE').length, paused = jobs.filter(j => j.state === 'PAUSED').length;
  const fin = d.stats.FINISHED || 0, can = d.stats.CANCELLED || 0, tot = Object.values(d.stats).reduce((a, b) => a + b, 0);
  const kpi = (l, v) => `<div class="card kpi"><div class="l">${l}</div><div class="v" style="font-size:20px">${v}</div></div>`;
  return `${warnNotice(d.warnings)}<div class="grid g4">${kpi('Routine Load', `${run} 运行 / <span class="${paused ? 'up' : ''}">${paused}</span> 暂停`)}${kpi('导入速率', fmt(jobs.reduce((a, j) => a + (j.rowRate || 0), 0)) + ' 行/s')}${kpi('导入失败（保留期内）', `${can} / ${fmt(tot)}${tot ? ` (${(can / tot * 100).toFixed(1)}%)` : ''}`)}${kpi('已完成导入', fmt(fin))}</div>
    <div class="card" style="margin-top:16px"><h2>Routine Load <span class="sub">information_schema.routine_load_jobs</span><span class="right"><button class="btn sm" data-on="opsRetry">⟳ 刷新</button></span></h2>
    <div class="tbl-wrap"><table><thead><tr><th>作业</th><th>目标表</th><th>数据源</th><th>状态</th><th>已导入 / 总行数</th><th>速率</th><th>错误率</th><th>操作</th></tr></thead><tbody>
    ${jobs.map(j => `<tr><td><b>${esc(j.name)}</b>${j.reason && j.state !== 'RUNNING' ? `<div class="small up" style="max-width:260px">${esc(j.reason)}</div>` : ''}</td><td class="small">${esc(j.db)}.${esc(j.table)}</td><td class="small mono">${esc(j.source)}</td><td>${jobTagL(j.state)}</td><td class="small">${fmt(j.loadedRows)} / ${fmt(j.totalRows)}</td><td>${j.rowRate ? fmt(j.rowRate) + '/s' : '—'}</td><td class="${j.errPct >= 1 ? 'up b' : ''}">${j.errPct}%</td>
      <td class="ops">${j.state === 'PAUSED' ? B('load.resume', '恢复', {cls: 'sm', data: {db: j.db, n: j.name}}) : ['RUNNING', 'NEED_SCHEDULE'].includes(j.state) ? B('load.pause', '暂停', {cls: 'sm', data: {db: j.db, n: j.name}}) : ''}${['CANCELLED', 'STOPPED'].includes(j.state) ? '' : B('load.stop', '停止', {cls: 'sm danger', data: {db: j.db, n: j.name}})}${B('load.detail', '详情', {cls: 'sm', data: {db: j.db, n: j.name}})}</td></tr>`).join('') || '<tr><td colspan="8" class="muted">没有 Routine Load 作业</td></tr>'}
    </tbody></table></div></div>
    <div class="card" style="margin-top:16px"><h2>最近的其他导入 <span class="sub">Broker / Stream / Insert 等 · information_schema.loads · 最近 ${d.recent.length} 条</span></h2>
    <div class="tbl-wrap"><table><thead><tr><th>Label</th><th>库</th><th>类型</th><th>状态</th><th>扫描 / 过滤 / 写入行</th><th>创建</th><th>结束</th><th>失败原因</th></tr></thead><tbody>
    ${d.recent.map(l => `<tr><td class="mono small">${esc(l.label)}</td><td>${esc(l.db)}</td><td>${esc(l.type)}</td><td>${jobTagL(l.state)}</td><td class="small">${fmt(l.scanRows)} / ${l.filteredRows ? `<span class="up">${fmt(l.filteredRows)}</span>` : 0} / ${fmt(l.sinkRows)}</td><td class="small">${esc(l.createTime)}</td><td class="small">${esc(l.finishTime || '—')}</td><td class="small" style="max-width:300px">${esc(l.error || '')}</td></tr>`).join('') || '<tr><td colspan="8" class="muted">没有记录</td></tr>'}
    </tbody></table></div></div>
    <div class="notice" style="margin-top:12px"><span>ℹ</span><div>Routine Load 暂停后修复数据源问题再恢复；停止后作业不可恢复，需要重新创建。${docLink('loading/RoutineLoad/', 'Routine Load ↗')} ${docLink('loading/', '导入总览 ↗')}</div></div>`;
};
const _ld = {pause: ACTIONS['load.pause'], resume: ACTIONS['load.resume'], stop: ACTIONS['load.stop'], detail: ACTIONS['load.detail']};
const ldPath = d => `/routine-loads/${opsEnc(d.db)}/${opsEnc(d.n)}`;
ACTIONS['load.pause'] = d => { if (!LIVE.on) return _ld.pause(d); opsCall('POST', ldPath(d) + '/pause', undefined, '已暂停 ' + d.n, ['load']); };
ACTIONS['load.resume'] = d => { if (!LIVE.on) return _ld.resume(d); opsCall('POST', ldPath(d) + '/resume', undefined, '已提交恢复 ' + d.n, ['load']); };
ACTIONS['load.stop'] = d => {
  if (!LIVE.on) return _ld.stop(d);
  liveRisky('load.stop', `停止 ${esc(d.db)}.${esc(d.n)}`, 'STOP 后作业不可恢复，只能重新创建；已消费的 offset 记录将丢失。', `STOP ROUTINE LOAD FOR \`${d.db}\`.\`${d.n}\`;`, () => opsCall('DELETE', ldPath(d), undefined, '已停止 ' + d.n, ['load']));
};
ACTIONS['load.detail'] = d => {
  if (!LIVE.on) return _ld.detail(d);
  api('GET', opsBase() + ldPath(d)).then(r => {
    const j = r.job;
    const kv = (k, v) => `<tr><td class="muted" style="width:130px">${k}</td><td>${v}</td></tr>`;
    openDrawer(`${j.db}.${j.name} 详情`, `${j.reason ? `<div class="notice bad"><span>!</span><div>${esc(j.reason)}</div></div>` : ''}
      <table>${kv('状态', jobTagL(j.state))}${kv('目标表', esc(j.db + '.' + j.table))}${kv('数据源', esc(j.source))}${kv('数据源属性', `<span class="mono small">${esc(j.sourceProp)}</span>`)}${kv('创建时间', esc(j.createTime))}${j.pauseTime ? kv('暂停时间', esc(j.pauseTime)) : ''}
      ${kv('消费进度', `<span class="mono small">${esc(j.progress || '—')}</span>`)}${kv('已导入 / 错误 / 总行数', `${fmt(j.loadedRows)} / ${fmt(j.errorRows)} / ${fmt(j.totalRows)}`)}${j.errorUrls ? kv('错误数据地址', `<span class="mono small" style="word-break:break-all">${esc(j.errorUrls)}</span>`) : ''}${j.otherMsg ? kv('其他信息', esc(j.otherMsg)) : ''}</table>
      <h4>当前子任务</h4>${r.tasks && r.tasks.length ? `<div class="tbl-wrap"><table><thead><tr>${r.taskCols.map(k => `<th>${esc(k)}</th>`).join('')}</tr></thead><tbody>${r.tasks.map(t => `<tr>${t.map(v => `<td class="small mono">${esc(v)}</td>`).join('')}</tr>`).join('')}</tbody></table></div>` : '<div class="muted small">没有运行中的子任务（作业暂停或尚未调度）</div>'}
      <h4>常见原因</h4><ul class="small"><li>offset out of range：Kafka 数据已过期，ALTER ROUTINE LOAD 设置 kafka_offsets 后恢复</li><li>too many filtered rows：数据格式 / 类型不匹配，调大 max_filter_ratio 或修正源数据</li><li>too many versions：导入过于频繁，Compaction 跟不上</li></ul>
      ${code(`SHOW ROUTINE LOAD FOR ${j.db}.${j.name}\\G\nSHOW ROUTINE LOAD TASK FROM ${j.db} WHERE JobName = "${j.name}";`)}`, true);
  }).catch(liveFail);
};

/* ============================ 物化视图 ============================ */
const _mvDemo = pages.mv.render;
let mvTimer = 0;
pages.mv.render = function () {
  if (!LIVE.on) return _mvDemo.call(this);
  const d = opsGet('mv', '/materialized-views');
  if (!d) return opsLoading('mv', '正在读取 information_schema.materialized_views…', 'opsRetry');
  clearTimeout(mvTimer);
  if (d.items.some(m => m.state === 'RUNNING' || m.state === 'PENDING')) mvTimer = setTimeout(() => { if (ROUTE.page === 'mv') opsReload('mv'); }, 4000);
  const busy = m => m.state === 'RUNNING' || m.state === 'PENDING';
  return `<div class="card"><h2>异步物化视图 <span class="sub">${d.items.length} 个 · information_schema.materialized_views</span><span class="right"><button class="btn sm" data-on="opsRetry">⟳ 刷新</button></span></h2><div class="tbl-wrap"><table><thead><tr><th>物化视图</th><th>刷新方式</th><th>分区</th><th>状态</th><th>最近刷新</th><th>耗时</th><th>行数</th><th>操作</th></tr></thead><tbody>
    ${d.items.map(m => `<tr><td><b>${esc(m.db)}.${esc(m.name)}</b>${m.error ? `<div class="small up" style="max-width:300px">${esc(m.error)}</div>` : ''}${!m.active && m.inactiveReason ? `<div class="small muted" style="max-width:300px">${esc(m.inactiveReason)}</div>` : ''}</td><td class="small mono">${esc(m.refresh || '—')}</td><td class="small">${esc(m.partition || '—')}</td><td>${m.active ? '' : '<span class="tag bad">INACTIVE</span> '}${jobTagL(m.state)}</td><td class="small">${esc(m.lastFinish || m.lastStart || '—')}</td><td class="small">${m.duration ? esc(m.duration) + ' s' : '—'}</td><td class="small">${fmt(m.rows)}</td>
      <td class="ops">${busy(m) ? B('mv.cancel', '取消', {cls: 'sm', data: {db: m.db, n: m.name}}) : B('mv.refresh', '刷新', {cls: 'sm', data: {db: m.db, n: m.name}})}${B('mv.toggle', m.active ? '停用' : '启用', {cls: 'sm', data: {db: m.db, n: m.name, on: m.active ? '0' : '1'}})}<button class="btn sm" data-on="mvDef" data-db="${esc(m.db)}" data-n="${esc(m.name)}">定义</button></td></tr>`).join('') || '<tr><td colspan="8" class="muted">没有异步物化视图</td></tr>'}
    </tbody></table></div></div>
    <div class="notice" style="margin-top:12px"><span>ℹ</span><div>基表 Schema Change 会使物化视图变为 INACTIVE，需「启用」(<code>ALTER MATERIALIZED VIEW … ACTIVE</code>) 重新激活。刷新内存不足可设置 <code>"session.query_mem_limit"</code> 或开启 spill。${docLink('using_starrocks/async_mv/Materialized_view/', '异步物化视图 ↗')}</div></div>`;
};
ON.mvDef = d => { const m = ((OPS.mv || {})[CUR.id] || {items: []}).items.find(x => x.db === d.db && x.name === d.n); if (m) openDrawer(`${m.db}.${m.name} 定义`, code(m.definition || '（未返回定义）'), true); };
const _mv = {refresh: ACTIONS['mv.refresh'], toggle: ACTIONS['mv.toggle'], cancel: ACTIONS['mv.cancel']};
const mvPath = d => `/materialized-views/${opsEnc(d.db)}/${opsEnc(d.n)}`;
ACTIONS['mv.refresh'] = d => {
  if (!LIVE.on) return _mv.refresh(d);
  openModal({title: `刷新 ${d.db}.${d.n}`, code: 'mv.refresh', body: `<div class="form"><label>刷新范围</label><select id="mvR"><option value="">全部分区</option><option value="range">指定分区区间</option></select>
    <label class="mvr" style="display:none">起始</label><input class="mvr" style="display:none" id="mvS" placeholder="2026-10-01"><label class="mvr" style="display:none">结束</label><input class="mvr" style="display:none" id="mvE" placeholder="2026-10-07">
    <label>强制</label><label class="chk"><input type="checkbox" id="mvF"> FORCE（忽略基表是否变化）</label></div><div class="small muted" style="margin-top:8px">异步提交，刷新进度请在列表中查看。</div>`,
    onOk() {
      const body = {force: $('#mvF').checked}; if ($('#mvR').value === 'range') { body.start = $('#mvS').value.trim(); body.end = $('#mvE').value.trim(); if (!body.start || !body.end) { toast('请填写起止时间'); return false; } }
      opsCall('POST', mvPath(d) + '/refresh', body, '已提交刷新 ' + d.n, ['mv']); return false;
    }});
};
document.addEventListener('change', e => { if (e.target.id === 'mvR') $$('.mvr').forEach(x => x.style.display = e.target.value === 'range' ? '' : 'none'); });
ACTIONS['mv.toggle'] = d => { if (!LIVE.on) return _mv.toggle(d); opsCall('POST', mvPath(d) + '/active', {active: d.on === '1'}, (d.on === '1' ? '已启用 ' : '已停用 ') + d.n, ['mv']); };
ACTIONS['mv.cancel'] = d => { if (!LIVE.on) return _mv.cancel(d); opsCall('POST', mvPath(d) + '/cancel', undefined, '已取消刷新 ' + d.n, ['mv']); };

LIVE_PAGES.push('resource', 'load', 'mv');
const _liveEnterOps = liveEnter;
liveEnter = async function (me) { Object.keys(OPS).forEach(k => delete OPS[k]); return _liveEnterOps(me); };   // 换账号登录后不沿用上一个账号的缓存

