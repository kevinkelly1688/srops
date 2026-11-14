// @author: kevinkelly
// @license: Apache Licence
// @file: live_query.js
// @time: 2026/10/28

/* ============================================================================
   真实模式：查询分析（运行中查询 / 慢查询 / SQL 黑名单）与 SQL 审计页签。
   依赖 live.js、live_ops.js（OPS / opsGet / opsCall / liveRisky 等）。
   ========================================================================== */
const QRY = {
  slow: {minutes: 1440, minMs: 5000, user: '', q: ''},
  sa: {minutes: 1440, minMs: 0, user: '', db: '', state: '', q: '', items: [], more: false, key: null, loading: false, err: '', cluster: ''},
  auto: false, timer: 0,
};
const opsDrop = prefix => Object.keys(OPS).forEach(k => { if (k.startsWith(prefix)) delete OPS[k]; });
const qs = o => { const p = new URLSearchParams(); Object.entries(o).forEach(([k, v]) => { if (v !== '' && v != null) p.set(k, v); }); return p.toString(); };
const MIN_OPTS = [[60, '最近 1 小时'], [1440, '最近 24 小时'], [10080, '最近 7 天'], [43200, '最近 30 天']];
const bytesH = b => { b = +b || 0; return b >= 2 ** 30 ? (b / 2 ** 30).toFixed(1) + ' GB' : b >= 2 ** 20 ? (b / 2 ** 20).toFixed(0) + ' MB' : b > 0 ? (b / 1024).toFixed(0) + ' KB' : '—'; };
const secH = ms => ms == null ? '—' : ms >= 60000 ? (ms / 60000).toFixed(1) + ' min' : (ms / 1000).toFixed(ms >= 10000 ? 0 : 1) + ' s';

/* ============================ 查询分析 ============================ */
const _queryDemo = {render: pages.query.render, running: pages.query.running, slow: pages.query.slow, blacklist: pages.query.blacklist, init: pages.query.init};
pages.query.render = function (sub) {
  if (!LIVE.on) return _queryDemo.render.call(this, sub);
  sub = sub || 'running';
  const run = ((OPS.queries || {})[CUR.id] || {items: null}).items;
  return tabs('query', [['running', `运行中查询${run ? ` (${run.length})` : ''}`], ['slow', '慢查询 Top SQL'], ['blacklist', 'SQL 黑名单']], sub) + this[sub]();
};
pages.query.init = function (sub) {
  clearInterval(QRY.timer);
  if (!LIVE.on) return;
  const el = $('#qAuto'); if (el) el.onchange = () => { QRY.auto = el.checked; rerender(); };
  if (QRY.auto && (sub || 'running') === 'running') QRY.timer = setInterval(() => { if (ROUTE.page !== 'query' || (ROUTE.sub || 'running') !== 'running') return clearInterval(QRY.timer); opsReload('queries'); }, 5000);
  const f = QRY.slow, bind = (id, k, num) => { const e = $(id); if (e) e.onchange = () => { f[k] = num ? +e.value : e.value.trim(); opsDrop('slow|'); rerender(); }; };
  bind('#sqM', 'minutes', 1); bind('#sqD', 'minMs', 1); bind('#sqU', 'user'); bind('#sqQ', 'q');
};
pages.query.running = function () {
  if (!LIVE.on) return _queryDemo.running.call(this);
  const d = opsGet('queries', '/queries');
  if (!d) return opsLoading('queries', '正在读取运行中的查询…', 'opsRetry');
  return `${warnNotice(d.warnings)}<div class="toolbar"><span class="small muted">数据来源：<code>SHOW PROC '${d.global ? '/global_current_queries' : '/current_queries'}'</code>${d.global ? '（全部 FE 汇总）' : '（逐台 FE 读取）'} + <code>SHOW FULL PROCESSLIST</code></span><span class="sp"></span>
      <label class="chk"><input type="checkbox" id="qAuto" ${QRY.auto ? 'checked' : ''}> 每 5 秒自动刷新</label><button class="btn sm" data-on="opsRetry">⟳ 刷新</button></div>
    <div class="notice"><span>ℹ</span><div>终止查询执行 <code>KILL QUERY &lt;连接 ID&gt;</code>，仅终止该查询，不断开连接；平台会先重新核对 Query ID 与连接仍然对应，并到该查询所在的 FE 上执行。</div></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>Query ID</th><th>用户 / 库</th><th>资源组</th><th>FE</th><th>已运行</th><th>扫描</th><th>内存</th><th>CPU (s)</th><th>SQL</th><th>操作</th></tr></thead><tbody>
    ${d.items.map(q => `<tr><td class="mono small" title="${esc(q.queryId)}">${esc(q.queryId.slice(0, 8))}…</td><td>${esc(q.user)}<div class="small muted">${esc(q.db)}</div></td><td>${q.resourceGroup ? `<span class="tag">${esc(q.resourceGroup)}</span>` : '—'}</td><td class="small">${esc(q.fe || '—')}</td>
      <td class="${q.execMs > 60000 ? 'up b' : ''}">${secH(q.execMs)}</td><td class="small">${esc(q.scanBytes || '—')}<div class="muted">${esc(q.scanRows || '')} 行</div></td><td class="small">${esc(q.mem || '—')}</td><td class="small">${esc(q.cpu || '—')}</td>
      <td class="mono small" style="max-width:320px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc(q.sql)}">${esc(q.sql || '—')}</td>
      <td class="ops">${B('query.profile', 'Profile', {cls: 'sm', data: {q: q.queryId}})}${B('query.kill', '终止', {cls: 'sm danger', data: {q: q.queryId, fe: q.fe || '', conn: q.connectionId}})}</td></tr>`).join('') || '<tr><td colspan="10" class="muted">当前没有运行中的查询</td></tr>'}
    </tbody></table></div></div>`;
};
pages.query.slow = function () {
  if (!LIVE.on) return _queryDemo.slow.call(this);
  const f = QRY.slow, key = 'slow|' + qs(f);
  const d = opsGet(key, '/slow-queries?' + qs(f));
  const tool = `<div class="toolbar"><select id="sqM">${MIN_OPTS.map(([v, t]) => `<option value="${v}" ${v === f.minutes ? 'selected' : ''}>${t}</option>`).join('')}</select>
    <select id="sqD">${[[1000, '耗时 ≥ 1s'], [5000, '耗时 ≥ 5s'], [10000, '耗时 ≥ 10s'], [60000, '耗时 ≥ 1 分钟']].map(([v, t]) => `<option value="${v}" ${v === f.minMs ? 'selected' : ''}>${t}</option>`).join('')}</select>
    <input id="sqU" placeholder="用户" value="${esc(f.user)}" style="width:110px"><input type="search" id="sqQ" placeholder="SQL 包含…" value="${esc(f.q)}"><span class="sp"></span>${B('query.export', '⇩ 导出 CSV')}</div>`;
  const note = `<div class="notice"><span>ℹ</span><div>按 <b>SQL 指纹（Digest）</b>聚合审计表（AuditLoader 写入 <code>starrocks_audit_db__.starrocks_audit_tbl__</code>），按总耗时排序，最多 50 条。未开启 SQL Digest 的记录不会聚合。${docLink('administration/management/audit_loader/', 'AuditLoader ↗')} ${docLink('administration/sql_digest/', 'SQL Digest ↗')}</div></div>`;
  if (!d) return tool + note + opsLoading(key, '正在读取审计表…', 'opsRetry');
  return tool + note + `<div class="card"><div class="tbl-wrap"><table><thead><tr><th>指纹</th><th>SQL（耗时最长的一次）</th><th>用户</th><th>次数</th><th>平均</th><th>最大</th><th>扫描行</th><th>峰值内存</th><th>操作</th></tr></thead><tbody>
    ${d.items.map((s, i) => `<tr><td class="mono small">${esc(s.digest || '—')}</td><td class="mono small" style="max-width:360px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="${esc(s.sql)}">${esc(s.sql)}</td><td>${esc(s.user)}</td><td>${s.count}</td><td>${secH(s.avgMs)}</td><td class="${s.maxMs > 60000 ? 'up b' : ''}">${secH(s.maxMs)}</td><td class="small">${fmt(s.scanRows)}</td><td class="small">${bytesH(s.memBytes)}</td>
      <td class="ops">${B('query.profile', 'Profile', {cls: 'sm', data: {q: s.queryId}})}${B('query.explain', '执行计划', {cls: 'sm', data: {i, k: key}})}</td></tr>`).join('') || '<tr><td colspan="9" class="muted">该时间范围内没有符合条件的慢查询</td></tr>'}
    </tbody></table></div></div>`;
};
pages.query.blacklist = function () {
  if (!LIVE.on) return _queryDemo.blacklist.call(this);
  const d = opsGet('black', '/blacklist');
  if (!d) return opsLoading('black', '正在读取 SHOW SQLBLACKLIST…', 'opsRetry');
  return `<div class="notice ${d.enabled ? '' : 'warn'}"><span>${d.enabled ? 'ℹ' : '⚠'}</span><div>${d.enabled ? 'SQL 黑名单已启用（<code>enable_sql_blacklist = true</code>）。' : d.known ? '当前 <b>未启用</b> SQL 黑名单：规则不会生效，需要先将 FE 配置 <code>enable_sql_blacklist</code> 设为 true（在「参数配置」中修改）。' : '无法读取 <code>enable_sql_blacklist</code>，请确认 FE 配置。'}命中规则的查询会直接报错拒绝。${docLink('administration/Blacklist/', 'SQL 黑名单 ↗')}</div></div>
    <div class="card"><h2>黑名单规则 <span class="sub">${d.items.length} 条</span><span class="right"><button class="btn sm" data-on="opsRetry">⟳</button> ${B('query.blacklist', '＋ 添加规则', {cls: 'sm primary'})}</span></h2>
    <div class="tbl-wrap"><table><thead><tr><th>序号</th><th>正则</th><th>操作</th></tr></thead><tbody>
    ${d.items.map(b => `<tr><td>${esc(b.index)}</td><td class="mono" style="word-break:break-all">${esc(b.pattern)}</td><td>${B('query.blacklist', '删除', {cls: 'sm danger', data: {del: b.index, p: b.pattern}})}</td></tr>`).join('') || '<tr><td colspan="3" class="muted">暂无规则</td></tr>'}
    </tbody></table></div><div class="small muted" style="margin-top:8px">StarRocks 不记录规则的添加人与时间；谁在何时添加 / 删除，可在「操作审计」中按权限码 query.blacklist 查询。</div></div>`;
};

const _q = {kill: ACTIONS['query.kill'], profile: ACTIONS['query.profile'], explain: ACTIONS['query.explain'], exp: ACTIONS['query.export'], black: ACTIONS['query.blacklist']};
ACTIONS['query.kill'] = d => {
  if (!LIVE.on) return _q.kill(d);
  liveRisky('query.kill', '终止查询', `将终止查询 <code>${esc(d.q)}</code>，客户端会收到错误。`, d.conn ? `KILL QUERY ${d.conn};` : `KILL QUERY '${d.q}';`,
    () => opsCall('POST', '/queries/kill', {queryId: d.q, fe: d.fe, connectionId: d.conn}, '已终止查询', ['queries']));
};
ACTIONS['query.profile'] = d => {
  if (!LIVE.on) return _q.profile(d);
  toast('正在读取 Profile…');
  api('GET', `${opsBase()}/queries/${opsEnc(d.q)}/profile`).then(r => {
    const big = r.profile.length > 200000;
    openDrawer('Query Profile', `<div class="small muted">Query ID <span class="mono">${esc(d.q)}</span></div>
      ${r.analysis ? `<h4>分析结论（ANALYZE PROFILE）</h4><pre class="code" style="white-space:pre-wrap">${esc(r.analysis)}</pre>` : '<div class="notice"><span>ℹ</span><div>当前版本未返回 ANALYZE PROFILE 结果，请查看下面的原始 Profile。</div></div>'}
      <h4>原始 Profile</h4>${big ? `<pre class="code" style="max-height:60vh;overflow:auto">${esc(r.profile)}</pre>` : code(r.profile)}
      <h4>获取方式</h4>${code("SET enable_profile = true;\n-- 执行 SQL 后\nSHOW PROFILELIST;\nANALYZE PROFILE FROM '<query_id>';")}${docLink('best_practices/query_tuning/query_profile_overview/', 'Query Profile 说明 ↗')}`, true);
  }).catch(liveFail);
};
ACTIONS['query.explain'] = d => {
  if (!LIVE.on) return _q.explain(d);
  const s = (((OPS[d.k] || {})[CUR.id] || {}).items || [])[+d.i]; if (!s) return;
  openModal({title: '执行计划分析', code: 'query.explain', wide: true, ok: '分析', body: `<div class="form"><label>SQL</label><textarea id="exSql" rows="8" class="mono" style="width:100%">${esc(s.sql)}</textarea>
    <label>模式</label><select id="exMode"><option value="">EXPLAIN（执行计划）</option><option value="costs">EXPLAIN COSTS（含统计信息与基数估算）</option></select></div>
    <div class="small muted" style="margin-top:6px">只会对 SELECT / WITH 查询执行 EXPLAIN，不会真正运行该 SQL。审计表中的 SQL 可能被截断，请核对后再分析。</div>`,
    onOk() {
      const sql = $('#exSql').value, costs = $('#exMode').value === 'costs';
      api('POST', opsBase() + '/explain', {sql, costs}).then(r => { closeModal(); openDrawer('执行计划', `${code((costs ? 'EXPLAIN COSTS\n' : 'EXPLAIN\n') + sql)}${code(r.plan)}
        <div class="notice"><span>ℹ</span><div>常见问题：小表统计信息过期时优化器可能选择 Shuffle Join，可执行 <code>ANALYZE TABLE</code> 更新统计信息，或用 <code>[BROADCAST]</code> 提示。</div></div>`, true); }).catch(liveFail);
      return false;
    }});
};
ACTIONS['query.export'] = () => {
  if (!LIVE.on) return _q.exp();
  const a = document.createElement('a'); a.href = `${opsBase()}/slow-queries/export?${qs(QRY.slow)}`; a.download = ''; document.body.appendChild(a); a.click(); a.remove();
  toast('正在导出慢查询…');
};
ACTIONS['query.blacklist'] = d => {
  if (!LIVE.on) return _q.black(d);
  if (d.del) return liveRisky('query.blacklist', '删除黑名单规则 #' + esc(d.del), `删除后，匹配 <code>${esc(d.p)}</code> 的 SQL 将恢复可执行。注意：删除后其后的规则序号会前移。`, `DELETE SQLBLACKLIST ${d.del};`,
    () => opsCall('DELETE', '/blacklist/' + opsEnc(d.del), undefined, '已删除规则', ['black']));
  openModal({title: '添加 SQL 黑名单', code: 'query.blacklist', body: `<div class="form"><label class="req">正则表达式</label><input id="blP" class="mono" placeholder="select \\* from dws\\.big_table.*"><div class="hint">大小写不敏感，匹配整条 SQL；特殊字符需转义。过宽的规则（会匹配 <code>select 1</code> 等常见语句）会被拒绝。</div></div>`,
    onOk() {
      const p = $('#blP').value.trim(); if (!p) { toast('请输入正则'); return false; }
      liveRisky('query.blacklist', '添加 SQL 黑名单规则', `命中该规则的查询将被直接拒绝：<code>${esc(p)}</code>`, `ADD SQLBLACKLIST "${p.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}";`, () => opsCall('POST', '/blacklist', {pattern: p}, '已添加规则', ['black']));
      return false;
    }});
};

/* ============================ 操作审计 → SQL 审计页签 ============================ */
const saQuery = () => qs({minutes: QRY.sa.minutes, minMs: QRY.sa.minMs || '', user: QRY.sa.user, db: QRY.sa.db, state: QRY.sa.state, q: QRY.sa.q});
function saLoad(append) {
  const S = QRY.sa; if (S.loading || !CUR) return;
  S.loading = true; S.err = '';
  const key = CUR.id + '|' + saQuery(), id = CUR.id;
  api('GET', `/api/clusters/${opsEnc(id)}/sql-audit?${saQuery()}&limit=100&offset=${append ? S.items.length : 0}`)
    .then(r => { S.items = append ? S.items.concat(r.items) : r.items; S.more = r.more; S.key = key; })
    .catch(e => { S.err = e.message; S.key = key; }).finally(() => { S.loading = false; if (ROUTE.page === 'audit') rerender(); });
}
function liveSqlAuditHtml() {
  if (!can('audit.sql')) return '<div class="card"><div class="muted">🔒 需要「查看 SQL 审计」(audit.sql) 权限</div></div>';
  if (!CUR) return '<div class="card"><div class="muted">请先在「集群接入」中导入集群。</div></div>';
  const S = QRY.sa, key = CUR.id + '|' + saQuery();
  if (S.key !== key && !S.loading) { if (!S.err || S.key !== key) saLoad(false); }
  const tag = st => `<span class="tag ${st === 'ERR' ? 'bad' : 'ok'}">${esc(st || '—')}</span>`;
  return `<div class="notice"><span>ℹ</span><div>读取集群 <b>${esc(CUR.name)}</b>（顶栏可切换）的 StarRocks 审计表 <code>starrocks_audit_db__.starrocks_audit_tbl__</code>（需安装 AuditLoader 插件）。这是<b>集群内的 SQL 执行记录</b>；平台自身的按钮操作在「平台操作审计」页签。</div></div>
    <div class="toolbar"><select id="saM">${MIN_OPTS.map(([v, t]) => `<option value="${v}" ${v === S.minutes ? 'selected' : ''}>${t}</option>`).join('')}</select>
      <input id="saU" placeholder="用户" value="${esc(S.user)}" style="width:100px"><input id="saD" placeholder="库" value="${esc(S.db)}" style="width:90px">
      <select id="saS"><option value="">全部状态</option>${['OK', 'EOF', 'ERR'].map(x => `<option ${x === S.state ? 'selected' : ''}>${x}</option>`).join('')}</select>
      <select id="saT">${[[0, '不限耗时'], [1000, '≥ 1s'], [5000, '≥ 5s'], [60000, '≥ 1 分钟']].map(([v, t]) => `<option value="${v}" ${v === S.minMs ? 'selected' : ''}>${t}</option>`).join('')}</select>
      <input type="search" id="saQ" placeholder="SQL 包含…" value="${esc(S.q)}"><button class="btn sm" data-on="saRefresh">⟳ 刷新</button></div>
    ${S.err ? errCard(S.err, 'saRefresh') : `<div class="card">${S.loading && !S.items.length ? loadingCard('正在读取审计表…') : `<div class="tbl-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>来源 IP</th><th>库</th><th>状态</th><th>耗时</th><th>扫描行</th><th>返回行</th><th>内存</th><th>资源组</th><th>SQL</th></tr></thead><tbody>
      ${S.items.map(a => `<tr><td class="small">${esc(a.time)}</td><td>${esc(a.user)}</td><td class="mono small">${esc(a.clientIp)}</td><td>${esc(a.db)}</td><td>${tag(a.state)}${a.state === 'ERR' && a.errorCode && a.errorCode !== '0' ? `<div class="small muted">${esc(a.errorCode)}</div>` : ''}</td><td class="${a.ms > 60000 ? 'up b' : ''}">${secH(a.ms)}</td><td class="small">${fmt(a.scanRows)}</td><td class="small">${fmt(a.returnRows)}</td><td class="small">${bytesH(a.memBytes)}</td><td class="small">${esc(a.resourceGroup || '—')}</td>
        <td class="mono small" style="max-width:340px;white-space:pre-wrap;word-break:break-all">${esc(a.sql.length > 300 ? a.sql.slice(0, 300) + ' …' : a.sql)}</td></tr>`).join('') || '<tr><td colspan="11" class="muted">没有符合条件的记录</td></tr>'}
      </tbody></table></div>${S.more ? `<div style="text-align:center;margin-top:10px"><button class="btn" data-on="saMore" ${S.loading ? 'disabled' : ''}>${S.loading ? '加载中…' : '加载更多'}</button></div>` : ''}`}</div>`}`;
}
ON.saRefresh = () => { QRY.sa.key = null; QRY.sa.err = ''; rerender(); };
ON.saMore = () => saLoad(true);
const _auditInit2 = pages.audit.init;
pages.audit.init = function (sub) {
  _auditInit2.call(this, sub);
  if (!LIVE.on || sub !== 'sql') return;
  const S = QRY.sa, bind = (id, k, num) => { const e = $(id); if (e) e.onchange = () => { S[k] = num ? +e.value : e.value.trim(); S.key = null; S.err = ''; rerender(); }; };
  bind('#saM', 'minutes', 1); bind('#saU', 'user'); bind('#saD', 'db'); bind('#saS', 'state'); bind('#saT', 'minMs', 1); bind('#saQ', 'q');
};
const _liveEnterQ = liveEnter;
liveEnter = async function (me) { Object.assign(QRY.sa, {items: [], more: false, key: null, err: ''}); opsDrop('slow|'); return _liveEnterQ(me); };
LIVE_PAGES.push('query');
