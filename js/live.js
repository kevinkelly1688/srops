// @author: kevinkelly
// @license: Apache Licence
// @file: live.js
// @time: 2026/10/28

/* ============================================================================
   真实模式（live）：页面由 sr-ops 服务端托管时自动启用（/api/health 可访问）。
   - 登录 / 修改密码 / 退出走服务端；集群列表、导入向导走 /api/clusters。
   - 已接入后端：集群总览、集群架构、性能巡检、查询分析、资源隔离、导入作业、物化视图、备份恢复、参数配置、告警中心、节点管理、安装部署、升级回滚（live_*.js）、集群接入、用户管理、角色权限、操作审计、平台设置。其余页面仍为演示数据，并在页面顶部提示。
   直接以 file:// 打开时不启用，原型行为不变。
   ========================================================================== */
const LIVE = {on: false, version: '', ov: {}, topo: {}, reports: {}, report: {}, rules: null, sched: {}, busy: {}, err: {}, viewing: {}};
const LIVE_PAGES = ['overview', 'topology', 'inspect', 'clusters', 'users', 'roles', 'audit', 'settings', 'help'];

async function api(method, path, body) {
  const opt = {method, credentials: 'same-origin', headers: {'X-Requested-With': 'sr-ops'}};
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const r = await fetch(path, opt);
  let data = null;
  const raw = await r.text();
  try { data = JSON.parse(raw); } catch (e) {}
  if (r.status === 401 && path !== '/api/login') { showLogin(); lgError('会话已过期，请重新登录'); throw new Error('未登录'); }
  if (r.status === 403 && data && data.error === 'must_change_password') { liveForcePwd(); throw new Error('请先修改初始密码'); }
  if (!r.ok) throw new Error((data && data.error) || (raw && raw.length < 120 && !raw.startsWith('<') ? raw.trim() : 'HTTP ' + r.status));
  return data;
}
const fmtTime = s => s ? String(s).replace('T', ' ').slice(0, 19) : '—';
const hhmm = t => { const d = new Date(t * 1000); return String(d.getHours()).padStart(2, '0') + ':' + String(d.getMinutes()).padStart(2, '0'); };
const dur = ms => ms < 1000 ? ms + ' ms' : (ms / 1000).toFixed(1) + ' s';
const tb = b => b == null ? '—' : (b / 2 ** 40).toFixed(2) + ' TB';

/* ---------- 数据映射 ---------- */
function liveCluster(c) {
  const h = (c.feHosts || [])[0] || '';
  return {...c, fe: `${h}:${c.queryPort}`, http: `${h}:${c.httpPort}`, status: c.prom ? 'ok' : 'warn', added: (c.createdAt || '').slice(0, 10)};
}
async function liveLoadClusters() {
  const list = await api('GET', '/api/clusters');
  CLUSTERS.length = 0;
  list.forEach(c => CLUSTERS.push(liveCluster(c)));
  if (!CUR || !CLUSTERS.some(c => c.id === CUR.id)) CUR = CLUSTERS[0] || null;
  else CUR = CLUSTERS.find(c => c.id === CUR.id);
  NODES = CUR && LIVE.topo[CUR.id] ? LIVE.topo[CUR.id].nodes : [];
}
function liveUser(me) {
  return {id: me.login, login: me.login, name: me.name || me.login, email: me.email || '—', phone: me.phone || '', dept: me.dept || '—', roles: me.roles || [], status: 'active', last: fmtTime(me.lastLogin), created: '—'};
}

/* ---------- ICP 备案号（配置文件 SR_ICP，经 /api/health 下发；空则不显示） ---------- */
const liveIcpHtml = () => `<a href="https://beian.miit.gov.cn/" target="_blank" rel="noopener noreferrer">${esc(LIVE.icp)}</a>`;
function liveIcp() { const f = $('#icp'); if (f) { f.hidden = !LIVE.icp; f.innerHTML = LIVE.icp ? liveIcpHtml() : ''; } }

/* ---------- 启动：探测后端 ---------- */
const _loginInitDemo = loginInit;
loginInit = function () {
  if (!/^https?:$/.test(location.protocol)) return _loginInitDemo();
  fetch('/api/health', {cache: 'no-store'}).then(r => r.ok ? r.json() : null).catch(() => null).then(async h => {
    if (!h || h.mode !== 'live') return _loginInitDemo();
    LIVE.on = true; LIVE.version = h.version; LIVE.icp = h.icp || ''; liveIcp();
    const ver = $('#ver'); if (ver && h.version) { ver.textContent = 'v' + String(h.version).replace(/^v/, ''); ver.hidden = false; }   // 版本号来自服务端（打包时由 VERSION 注入）
    document.documentElement.classList.add('live');
    const pill = $('.top .pill.mock'); if (pill) { pill.className = 'pill'; pill.textContent = '● 已连接平台服务 ' + h.version; pill.title = '集群总览、集群架构、性能巡检、查询分析、资源隔离、导入作业、物化视图、备份恢复、参数配置、告警中心、集群接入、用户管理、角色权限、操作审计、平台设置为真实数据；其余页面为演示数据'; }
    // 演示数据中与真实集群无关的部分清空，避免误导
    [ALERTS, AUDIT].forEach(a => a.length = 0);
    CLUSTERS.length = 0; CUR = null; NODES = [];
    try {
      const me = await fetch('/api/me', {credentials: 'same-origin'}).then(r => r.ok ? r.json() : null);
      if (me) return liveEnter(me);
    } catch (e) {}
    showLogin();
  });
};

async function liveEnter(me) {
  ME = liveUser(me);
  USERS.length = 0; USERS.push(ME);
  ROLES.length = 0; (me.roleList || []).forEach(r => ROLES.push(r));   // 当前用户的角色（含权限与数据范围），菜单与按钮据此渲染
  RBAC.loaded = false; RBAC.at = 0;
  $('#login').hidden = true; $('#app').hidden = false;
  if (me.mustChange) { buildNav(); buildTop(); paintThemeSw(); $('#view').innerHTML = '<div class="card muted">请先修改初始密码。</div>'; return liveForcePwd(); }
  try { await liveLoadClusters(); } catch (e) { toast(e.message); }
  try { await liveLoadSettings(); } catch (e) { toast(e.message); }
  buildNav(); buildTop(); paintThemeSw(); render();
  if (me.prevLogin && !String(me.prevLogin).startsWith('0001')) toast(`欢迎，${ME.name}。上次登录：${fmtTime(me.prevLogin)}`);
}

/* ---------- 登录页：去掉演示账号区，登录走服务端 ---------- */
const _showLoginDemo = showLogin;
showLogin = function () {
  _showLoginDemo();
  if (!LIVE.on) return;
  $('#app').hidden = true;
  const d = $('#login .demo'); if (d) d.remove();
  const f = $('#login .foot'); if (f) f.textContent = `知识来源：StarRocks 官方文档 docs.mirrorship.cn · 平台 ${LIVE.version}`;
  if (LIVE.icp) { const p = $('#login .pane'); if (p) p.insertAdjacentHTML('beforeend', `<div class="icp">${liveIcpHtml()}</div>`); }
  $('#lgForm').onsubmit = e => { e.preventDefault(); liveLogin(); };
};
async function liveLogin() {
  const login = $('#lgU').value.trim(), pwd = $('#lgP').value;
  if (!login || !pwd) return lgError('请输入账号和密码');
  try {
    const me = await api('POST', '/api/login', {login, password: pwd});
    try { $('#lgRem').checked ? localStorage.setItem('sr_last_login', login) : localStorage.removeItem('sr_last_login'); } catch (e) {}
    LG.err = '';
    liveEnter(me);
  } catch (e) { lgError(e.message); }
}

/* ---------- 修改密码 / 退出 ---------- */
function livePwdModal(force) {
  openModal({title: force ? '首次登录：请修改初始密码' : '修改密码', cancel: force ? '' : '取消', body: `${force ? '<div class="notice warn"><span>🔑</span><div>为了安全，首次登录必须修改初始密码，修改后才能使用平台。</div></div>' : ''}
    <div class="form"><label class="req">当前密码</label><input type="password" id="pw0"><label class="req">新密码</label><input type="password" id="pw1"><div class="hint">至少 10 位，须包含大写、小写字母、数字和符号</div><label class="req">确认新密码</label><input type="password" id="pw2"></div>`,
    onOk() {
      const a = $('#pw0').value, b = $('#pw1').value;
      if (b !== $('#pw2').value) { toast('两次输入的新密码不一致'); return false; }
      api('POST', '/api/password', {old: a, new: b}).then(me => { closeModal(); toast('密码已修改'); liveEnter(me); }).catch(e => toast(e.message));
      return false;
    }});
  if (force) { $('#modal').dataset.force = '1'; const x = $('#modal header [data-mclose]'); if (x) x.remove(); }
}
function liveForcePwd() { if (!$('#mask').classList.contains('open')) livePwdModal(true); }
const _uPwdDemo = ON.uPwd;
ON.uPwd = () => { if (!LIVE.on) return _uPwdDemo(); $('#umenu').classList.remove('open'); livePwdModal(false); };
const _logoutDemo = ON.logout;
ON.logout = () => {
  if (!LIVE.on) return _logoutDemo();
  $('#umenu').classList.remove('open');
  openModal({title: '退出登录', ok: '退出', okCls: 'danger primary', body: `<div>确定退出账号 <b>${esc(ME.login)}</b> 吗？</div>`, onOk() {
    api('POST', '/api/logout').catch(() => {}).finally(() => { LIVE.ov = {}; LIVE.topo = {}; showLogin(); });
  }});
};

/* ---------- 节点：真实模式下来自拓扑缓存 ---------- */
const _genNodesDemo = genNodes;
genNodes = c => LIVE.on ? ((LIVE.topo[c.id] || {}).nodes || []) : _genNodesDemo(c);
const _nodeDetailDemo = nodeDetail;
nodeDetail = id => {
  if (!LIVE.on) return _nodeDetailDemo(id);
  const n = NODES.find(x => x.id === id); if (!n) return;
  const pct = v => v == null ? '—' : v + '%';
  const rows = [['类型', TYPE_META[n.type].full], ['角色', n.role || '—'], ['地址', n.host],
    ['端口', n.type === 'fe' ? `query ${n.port} · http ${n.http} · rpc ${n.rpc} · edit_log ${n.editLog}` : `heartbeat ${n.port} · http ${n.http} · brpc ${n.brpc}${n.bePort ? ' · be_port ' + n.bePort : ''}${n.starlet ? ' · starlet ' + n.starlet : ''}`],
    ['版本', n.version], ['状态', nodeSt[n.status][1]], ['启动时间', n.startTime || '—'], ['最近心跳', n.hb || '—'], ['部署目录', n.dir || '未填写'],
    ...(n.type === 'fe' ? [['JVM 堆', pct(n.jvm)], ['已回放 Journal', n.replayed], ['落后 Leader', n.journal - n.replayed]] : [['CPU', pct(n.cpu)], ['内存', pct(n.mem)], ['运行中查询', n.runningQueries || 0]]),
    ...(n.type === 'be' ? [['磁盘（最高）', pct(n.disk)], ['Tablet 数', n.tablets], ['数据量', `${n.dataUsed ?? '—'} / ${n.dataTotal ?? '—'} TB`], ['Compaction Score', n.compaction ?? '—']] : []),
    ...(n.type === 'cn' ? [['Warehouse', n.warehouse || '—']] : []), ...(n.errMsg ? [['错误信息', n.errMsg]] : [])];
  openDrawer(`${TYPE_META[n.type].label} · ${n.host}`, `<table>${rows.map(([k, v]) => `<tr><td class="muted" style="width:130px">${k}</td><td>${esc(v)}</td></tr>`).join('')}</table>
    <div class="small muted" style="margin-top:10px">数据来源：SHOW ${n.type === 'fe' ? 'FRONTENDS' : n.type === 'be' ? 'BACKENDS' : 'COMPUTE NODES'} 与节点 /metrics，采集于 ${fmtTime((LIVE.topo[CUR.id] || {}).collectedAt)}</div>`);
};
TOPO_TEXT.catalogs = (orig => () => {
  if (!LIVE.on) return orig();
  const cs = ((LIVE.topo[CUR.id] || {}).catalogs || []).filter(c => c.name !== 'default_catalog');
  return cs.length ? 'External Catalog：' + cs.map(c => `${c.name}（${c.type}）`).join(' · ') : '未配置 External Catalog';
})(TOPO_TEXT.catalogs);
TOPO_TEXT.storage = (orig => () => LIVE.on ? ['对象存储（Storage Volume）', 'CN 本地盘作为 Data Cache'] : orig())(TOPO_TEXT.storage);

/* ---------- 异步加载工具：页面先渲染“加载中”，取回后重绘 ---------- */
function liveLoad(key, id, fn) {
  if (LIVE.busy[key + id]) return;
  LIVE.busy[key + id] = true; delete LIVE.err[key + id];
  fn().catch(e => { LIVE.err[key + id] = e.message; }).finally(() => {
    delete LIVE.busy[key + id];
    if (CUR && CUR.id === id && $('#login').hidden) rerender();
  });
}
const loadingCard = msg => `<div class="card" style="text-align:center;padding:50px 20px"><div class="muted">⏳ ${msg}</div></div>`;
const errCard = (msg, retry) => `<div class="notice bad"><span>!</span><div>${esc(msg)} <a data-on="${retry}">重试</a></div></div>`;
function loadTopo(id) {
  liveLoad('topo', id, async () => { const t = await api('GET', `/api/clusters/${encodeURIComponent(id)}/topology`); LIVE.topo[id] = t; if (CUR && CUR.id === id) NODES = t.nodes; });
}
function loadOverview(id) {
  liveLoad('ov', id, async () => { const o = await api('GET', `/api/clusters/${encodeURIComponent(id)}/overview`); LIVE.ov[id] = o; LIVE.topo[id] = {...(LIVE.topo[id] || {}), nodes: o.nodes, collectedAt: o.collectedAt}; if (CUR && CUR.id === id) NODES = o.nodes; });
}
ON.liveRetryOv = () => loadOverview(CUR.id);
ON.liveRetryTopo = () => loadTopo(CUR.id);

/* ---------- 集群总览（真实） ---------- */
const _overviewDemo = pages.overview.render;
pages.overview.render = function () {
  if (!LIVE.on) return _overviewDemo.call(this);
  const o = LIVE.ov[CUR.id], err = LIVE.err['ov' + CUR.id];
  if (!o) { loadOverview(CUR.id); return err ? errCard('采集失败：' + err, 'liveRetryOv') : loadingCard('正在连接 ' + esc(CUR.name) + ' 采集总览…'); }
  const k = o.kpi, n = v => v == null ? '—' : Math.round(v).toLocaleString();
  const kpi = (l, v, d, cls = 'flat') => `<div class="card kpi"><div class="l">${l}</div><div class="v">${v}</div><div class="d ${cls}">${d}</div></div>`;
  const cnt = c => `${c.alive}/${c.total}`;
  const allAlive = k.fe.alive === k.fe.total && k.be.alive === k.be.total && k.cn.alive === k.cn.total;
  const lr = o.lastReport;
  const pts = (o.trend.points || []).filter(p => p.qps != null);
  const sd = CUR.mode === 'shared-data';
  const series2 = key => pts.map(p => p[key] ?? 0);
  const xl = pts.length > 1 ? [0, .25, .5, .75, 1].map(f => hhmm(pts[Math.round((pts.length - 1) * f)].t)) : [];
  return `
  <div class="toolbar"><span class="tag brand">${esc(CUR.env)}</span><span class="small muted">${esc(CUR.name)} · StarRocks ${esc(CUR.version)} · ${modeText(CUR.mode)} · 采集于 ${fmtTime(o.collectedAt).slice(11)}</span><span class="sp"></span>
    ${B('overview.refresh', '⟳ 刷新', {cls: 'sm'})}${B('overview.export', '⇩ 导出概览报告', {cls: 'sm'})}</div>
  ${o.warnings.length ? `<div class="notice warn"><span>!</span><div><b>部分数据未采集：</b>${o.warnings.map(esc).join('；')}</div></div>` : ''}
  <div class="grid g6">
    ${kpi('QPS', n(k.qps), '各 FE 当前值之和')}
    ${kpi('查询 P99', k.p99 == null ? '—' : (k.p99 >= 1000 ? (k.p99 / 1000).toFixed(2) + ' s' : Math.round(k.p99) + ' ms'), '各 FE 最大值', k.p99 >= 3000 ? 'up' : 'flat')}
    ${kpi('FE / BE / CN 存活', `${cnt(k.fe)} · ${cnt(k.be)} · ${cnt(k.cn)}`, allAlive ? '全部存活' : '存在失联节点', allAlive ? 'down' : 'up')}
    ${sd ? kpi('连接数', n(k.connections), '各 FE 之和') : kpi('存储使用', k.storageTotal ? `${tb(k.storageUsed)} / ${tb(k.storageTotal)}` : '—', k.maxDiskPct != null ? `单盘最高 ${k.maxDiskPct}%` : '', k.maxDiskPct >= 80 ? 'up' : 'flat')}
    ${kpi('不健康 Tablet', n(k.unhealthyTablets), `Routine Load：${k.routineLoadRunning ?? '—'} 运行 / ${k.routineLoadPaused ?? '—'} 暂停`, (k.unhealthyTablets || k.routineLoadPaused) ? 'up' : 'flat')}
    ${kpi('巡检健康分', lr ? lr.score : '—', lr ? `${lr.counts.bad || 0} 异常 · ${lr.counts.warn || 0} 警告` : '尚未巡检', lr && lr.score < 80 ? 'up' : 'flat')}
  </div>
  <div class="grid g-main" style="margin-top:16px">
    <div class="grid" style="align-content:start">
      <div class="card"><h2>查询吞吐与延迟 <span class="sub">${o.trend.source === 'prometheus' ? '最近 60 分钟 · Prometheus' : '平台内置采样（每 15 秒，保留 60 分钟）'}</span></h2>
        ${pts.length > 1 ? `<div class="grid g2"><div>${lineChart([{name: 'QPS', color: 'var(--c1)', data: series2('qps')}], {xl})}</div>
          <div>${lineChart([{name: 'P99 (ms)', color: 'var(--c5)', data: series2('p99')}, {name: 'P95 (ms)', color: 'var(--c3)', data: series2('p95')}], {xl})}</div></div>`
        : `<div class="muted small" style="padding:20px 0">采样中：已采集 ${pts.length} 个点，约 30 秒后可显示趋势（配置 Prometheus 可直接显示历史趋势）。</div>`}</div>
      ${liveRgCard(o)}
      <div class="card"><h2>节点健康 <span class="sub">FE ${k.fe.total} · BE ${k.be.total} · CN ${k.cn.total}</span><span class="right"><a href="#topology">集群架构 →</a></span></h2>
        <div class="tbl-wrap"><table><thead><tr><th>类型</th><th>节点</th><th>角色</th><th>状态</th><th>CPU</th><th>内存</th><th>磁盘</th><th>${sd ? 'Warehouse' : 'Compaction'}</th></tr></thead><tbody>
        ${o.nodes.map(x => `<tr class="click" data-on="node" data-id="${x.id}"><td><span class="tag" style="color:${TYPE_META[x.type].color}">${TYPE_META[x.type].label}</span></td><td class="mono">${esc(x.host)}:${x.type === 'fe' ? x.port : x.http}</td><td>${x.role || x.type.toUpperCase()}</td><td>${stDot(x.status)}</td>
          <td style="width:100px">${meter(x.cpu)}</td><td style="width:100px">${meter(x.mem)}</td><td style="width:100px">${meter(x.disk, 75, 85)}</td>
          <td>${x.type === 'fe' ? `<span class="small muted">JVM ${x.jvm ?? '—'}%</span>` : sd ? esc(x.warehouse || '—') : x.compaction != null ? `<span class="${x.compaction >= 100 ? 'up b' : ''}">${x.compaction}</span>` : '—'}</td></tr>`).join('')}
        </tbody></table></div></div>
    </div>
    <div class="grid" style="align-content:start">
      <div class="card"><h2>最近一次巡检 <span class="right"><a href="#inspect">查看 →</a></span></h2>
        ${lr ? `<div class="score">${ring(lr.score)}<div class="small"><div>${fmtTime(lr.at)}</div><div class="muted">${esc(lr.by)} · 耗时 ${dur(lr.durationMs)}</div><div style="margin-top:6px"><span class="tag bad">${lr.counts.bad || 0} 异常</span> <span class="tag warn">${lr.counts.warn || 0} 警告</span></div></div></div>`
          : `<div class="muted small">尚未巡检。</div><div style="margin-top:8px">${B('inspect.run', '▶ 立即巡检', {cls: 'sm primary'})}</div>`}</div>
      ${liveAlertCard(o)}
      <div class="card"><h2>物化视图 / 导入</h2>
        ${[['物化视图', k.mvTotal == null ? '—' : `${k.mvTotal} 个${k.mvFailed ? `（<span class="up">${k.mvFailed} 个刷新失败</span>）` : ''}`], ['Routine Load', k.routineLoadRunning == null ? '—' : `${k.routineLoadRunning} 运行${k.routineLoadPaused ? ` · <span class="up">${k.routineLoadPaused} 暂停</span>` : ''}`], ['连接数', n(k.connections)]].map(([a, b]) => `<div class="row between small" style="padding:3px 0"><span class="muted">${a}</span><b>${b}</b></div>`).join('')}</div>
      <div class="card"><h2>集群信息</h2>
        ${o.info.map(x => `<div class="row between small" style="padding:3px 0"><span class="muted">${esc(x.k)}</span><b style="text-align:right">${esc(x.v)}</b></div>`).join('')}</div>
    </div>
  </div>`;
};
/* 资源组 CPU 占用：SHOW USAGE RESOURCE GROUPS 按资源组合计各 BE / CN 正在使用的 CPU 核 */
const RG_COLORS = ['var(--c1)', 'var(--c2)', 'var(--c3)', 'var(--c4)', 'var(--c5)', 'var(--c6)'];
function liveRgCard(o) {
  const head = `<h2>资源组 CPU 占用 <span class="sub">实时 · SHOW USAGE RESOURCE GROUPS</span><span class="right"><a href="#resource">资源隔离 →</a></span></h2>`;
  const g = o.resourceGroups;
  if (g == null) return `<div class="card">${head}<div class="muted small">未采集（该功能需要 StarRocks 3.1.4 及以上版本，原因见页面顶部提示）。</div></div>`;
  const used = g.reduce((a, x) => a + x.cpuCores, 0);
  if (!g.length || used <= 0) return `<div class="card">${head}<div class="muted small">当前没有查询在使用资源组的 CPU。</div></div>`;
  const pctOf = v => Math.round(v / used * 100);
  return `<div class="card">${head}
    ${stackBar(g.map((x, i) => [x.name, pctOf(x.cpuCores), RG_COLORS[i % RG_COLORS.length]]))}
    <div class="tbl-wrap" style="margin-top:8px"><table><thead><tr><th>资源组</th><th>使用 CPU 核</th><th>占集群 CPU</th><th>使用内存</th><th>运行中查询</th></tr></thead><tbody>
    ${g.map(x => `<tr><td>${esc(x.name)}</td><td>${x.cpuCores.toFixed(1)}</td><td>${o.totalCores ? meter(Math.round(x.cpuCores / o.totalCores * 1000) / 10) : '—'}</td><td>${(x.memBytes / 1073741824).toFixed(1)} GB</td><td>${x.running}</td></tr>`).join('')}
    </tbody></table></div>
    <div class="small muted" style="margin-top:6px">合计使用 ${used.toFixed(1)} 核${o.totalCores ? ` / 集群共 ${o.totalCores} 核（存活的 BE / CN）` : ''}；条形图为各资源组占已用 CPU 的比例。</div></div>`;
}
/* 活跃告警：告警中心尚未接入后端，按本次采集的数据实时判断 */
function liveAlertCard(o) {
  if (typeof liveAlertCardReal === 'function') return liveAlertCardReal(o);   // live_alert.js：告警中心已接入，显示真实告警
  const k = o.kpi, a = [];
  o.nodes.forEach(x => {
    const id = `${TYPE_META[x.type].label} ${x.host}:${x.type === 'fe' ? x.port : x.http}`;
    if (!x.alive) a.push(['bad', '节点失联', id + (x.errMsg ? ' · ' + x.errMsg : '')]);
    if (x.disk != null && x.disk >= 85) a.push(['bad', `磁盘使用率 ${x.disk}%`, id]); else if (x.disk != null && x.disk >= 80) a.push(['warn', `磁盘使用率 ${x.disk}%`, id]);
    if (x.compaction != null && x.compaction >= 100) a.push(['warn', `Compaction Score ${x.compaction}`, id]);
    if (x.jvm != null && x.jvm >= 85) a.push(['warn', `FE JVM 堆使用 ${x.jvm}%`, id]);
  });
  if (k.unhealthyTablets) a.push(['warn', `不健康 Tablet ${k.unhealthyTablets} 个`, 'SHOW PROC \'/statistic\'']);
  if (k.routineLoadPaused) a.push(['warn', `Routine Load 暂停 ${k.routineLoadPaused} 个`, 'information_schema.routine_load_jobs']);
  if (k.mvFailed) a.push(['warn', `物化视图刷新失败 ${k.mvFailed} 个`, 'information_schema.materialized_views']);
  if (k.p99 != null && k.p99 >= 3000) a.push(['warn', `查询 P99 ${(k.p99 / 1000).toFixed(2)} s`, 'FE /metrics']);
  a.sort((x, y) => (x[0] === 'bad' ? 0 : 1) - (y[0] === 'bad' ? 0 : 1));
  return `<div class="card"><h2>活跃告警 <span class="sub">${a.length}</span><span class="right"><a href="#inspect">性能巡检 →</a></span></h2>
    <div class="small muted" style="margin-bottom:4px">告警中心尚未接入后端，以下为按本次采集数据实时判断的异常（阈值同巡检规则默认值）。</div>
    ${a.slice(0, 6).map(([lv, name, inst]) => `<div style="padding:8px 0;border-bottom:1px solid var(--border)"><div class="row"><span class="tag ${lv}">${lv === 'bad' ? '严重' : '警告'}</span><b class="small">${esc(name)}</b></div><div class="small muted mono">${esc(inst)}</div></div>`).join('') || '<div class="muted small" style="padding:8px 0">✓ 当前无异常</div>'}
    ${a.length > 6 ? `<div class="small muted" style="padding-top:6px">另有 ${a.length - 6} 项，请执行巡检查看完整结果</div>` : ''}</div>`;
}
const _ovRefreshDemo = ACTIONS['overview.refresh'];
ACTIONS['overview.refresh'] = () => { if (!LIVE.on) return _ovRefreshDemo(); delete LIVE.ov[CUR.id]; rerender(); };

/* ---------- 集群架构（真实） ---------- */
const _topoDemo = pages.topology.render;
pages.topology.render = function () {
  if (!LIVE.on) return _topoDemo.call(this);
  const t = LIVE.topo[CUR.id], err = LIVE.err['topo' + CUR.id];
  if (!t || !t.catalogs) { loadTopo(CUR.id); return err ? errCard('采集失败：' + err, 'liveRetryTopo') : loadingCard('正在读取 SHOW FRONTENDS / BACKENDS / COMPUTE NODES…'); }
  NODES = t.nodes;
  return `<div class="toolbar"><span class="small muted">采集于 ${fmtTime(t.collectedAt)} · ${t.nodes.length} 个节点</span><span class="sp"></span><button class="btn sm" data-on="liveTopoRefresh">⟳ 刷新</button></div>
    ${(t.warnings || []).length ? `<div class="notice warn"><span>!</span><div>${t.warnings.map(esc).join('；')}</div></div>` : ''}` + _topoDemo.call(this);
};
ON.liveTopoRefresh = () => { delete LIVE.topo[CUR.id]; rerender(); };

/* ---------- 性能巡检（真实） ---------- */
function liveApplyReport(rep) {
  LIVE.report[CUR.id] = rep;
  INSPECT_ITEMS.length = 0;
  rep.items.forEach(it => INSPECT_ITEMS.push(it));
  INS.ignored = new Set(rep.items.filter(i => i.ignored).map(i => i.name));
}
function loadInspect(id, rid) {
  liveLoad('ins', id, async () => {
    const [list, rules] = await Promise.all([api('GET', `/api/clusters/${encodeURIComponent(id)}/inspections`), api('GET', '/api/inspect/rules')]);
    LIVE.reports[id] = list; LIVE.rules = rules;
    LIVE.sched[id] = await api('GET', `/api/clusters/${encodeURIComponent(id)}/inspect/schedule`);
    const want = rid || (LIVE.viewing[id]) || (list[0] && list[0].id);
    if (want) { const rep = await api('GET', `/api/clusters/${encodeURIComponent(id)}/inspections/${want}`); if (CUR && CUR.id === id) liveApplyReport(rep); LIVE.viewing[id] = want; }
    else { LIVE.report[id] = null; if (CUR && CUR.id === id) INSPECT_ITEMS.length = 0; }
  });
}
const _inspectDemo = {result: pages.inspect.result, history: pages.inspect.history, rule: pages.inspect.rule};
pages.inspect.result = function () {
  if (!LIVE.on) return _inspectDemo.result.call(this);
  if (!(CUR.id in LIVE.report)) { loadInspect(CUR.id); return LIVE.err['ins' + CUR.id] ? errCard(LIVE.err['ins' + CUR.id], 'liveRetryIns') : loadingCard('正在读取巡检报告…'); }
  const rep = LIVE.report[CUR.id];
  if (rep && INSPECT_ITEMS[0] !== rep.items[0]) liveApplyReport(rep);
  if (!rep) return `<div class="card" style="text-align:center;padding:50px 20px"><div style="font-size:40px">✚</div><h2 style="justify-content:center">${esc(CUR.name)} 还没有巡检报告</h2>
    <p class="muted">巡检会读取 SHOW 命令、information_schema、节点 /metrics，并通过 SSH 检查系统参数（只读，不修改集群）。共 ${(LIVE.rules || {rules: []}).rules.length} 条规则。</p>
    ${INS.running ? liveRunning() : B('inspect.run', '▶ 立即巡检', {cls: 'primary'})}</div>`;
  const items = INSPECT_ITEMS.filter(i => (INS.cat === '全部' || i.cat === INS.cat) && (INS.st === '全部' || i.st === INS.st));
  const c = rep.counts || {};
  const isLatest = (LIVE.reports[CUR.id] || [])[0] && LIVE.reports[CUR.id][0].id === rep.id;
  return `
  <div class="grid g-main">
    <div class="card"><div class="score">${ring(rep.score)}
      <div style="flex:1"><div class="row"><b style="font-size:16px">${esc(CUR.name)} 性能巡检</b><span class="tag">${rep.id}</span>${isLatest ? '' : '<span class="tag warn">历史报告</span> <a data-on="liveInsLatest">回到最新</a>'}</div>
      <div class="small muted">${fmtTime(rep.at)} · ${esc(rep.by)} · 耗时 ${dur(rep.durationMs)} · ${rep.items.length} 项</div>
      <div class="row" style="margin-top:8px"><span class="tag bad">异常 ${c.bad || 0}</span><span class="tag warn">警告 ${c.warn || 0}</span><span class="tag info">建议 ${c.info || 0}</span><span class="tag ok">正常 ${c.ok || 0}</span>${c.skip ? `<span class="tag">跳过 ${c.skip}</span>` : ''}${c.error ? `<span class="tag bad">采集失败 ${c.error}</span>` : ''}${c.ignored ? `<span class="tag">已忽略 ${c.ignored}</span>` : ''}</div></div>
      <div class="row" style="align-self:flex-start">${INS.running ? '' : B('inspect.run', '▶ 立即巡检', {cls: 'primary'})}${B('inspect.export', '⇩ 导出报告')}</div></div>
      ${INS.running ? liveRunning() : ''}</div>
    <div class="card"><h2>分类结果</h2>${INSPECT_CATS.map(cat => { const l = rep.items.filter(i => i.cat === cat && !i.ignored); const b = l.filter(i => i.st === 'bad').length, w = l.filter(i => i.st === 'warn').length, s = l.filter(i => i.st === 'skip').length;
      return `<div class="row between small" style="padding:3px 0"><a data-on="insCat" data-c="${cat}">${cat}</a><span>${b ? `<span class="tag bad">${b}</span> ` : ''}${w ? `<span class="tag warn">${w}</span>` : ''}${!b && !w && s === l.length && l.length ? '<span class="tag">跳过</span>' : !b && !w ? '<span class="tag ok">正常</span>' : ''}</span></div>`; }).join('')}</div>
  </div>
  <div class="card" style="margin-top:16px">
    <div class="toolbar"><b>巡检明细</b>
      <select id="insCatSel">${['全部'].concat(INSPECT_CATS).map(x => `<option ${x === INS.cat ? 'selected' : ''}>${x}</option>`).join('')}</select>
      <select id="insStSel">${[['全部', '全部状态'], ['bad', '异常'], ['warn', '警告'], ['info', '建议'], ['ok', '正常'], ['skip', '跳过'], ['error', '采集失败']].map(([v, t]) => `<option value="${v}" ${v === INS.st ? 'selected' : ''}>${t}</option>`).join('')}</select>
      <span class="sp"></span><span class="small muted">点击行查看检查方法、各节点明细与处置建议</span></div>
    <div class="tbl-wrap"><table><thead><tr><th>分类</th><th>巡检项</th><th>当前值</th><th>阈值</th><th>结果</th><th>处置建议</th><th>操作</th></tr></thead><tbody>
    ${items.map(i => `<tr class="click" data-on="insItem" data-n="${esc(i.name)}" style="${i.ignored ? 'opacity:.45' : ''}"><td class="small muted">${i.cat}</td><td><b>${esc(i.name)}</b></td><td>${esc(i.val)}</td><td class="small muted">${esc(i.th)}</td><td>${tagOf(i.st)}</td>
      <td class="small" style="max-width:380px">${esc(i.adv)}</td><td>${['bad', 'warn', 'info'].includes(i.st) && isLatest ? B('inspect.ignore', i.ignored ? '取消忽略' : '忽略', {cls: 'sm', data: {n: i.name}}) : ''}</td></tr>`).join('')}
    </tbody></table></div></div>`;
};
let insTimer = 0;
const liveRunning = () => `<div class="notice" style="margin-top:12px"><span>⏳</span><div>正在巡检 ${esc(CUR.name)}：采集拓扑与节点指标 → 执行 SQL / information_schema 检查 → SSH 系统环境检查… 已用时 <b id="insElapsed">${Math.round((Date.now() - INS.started) / 1000)}</b> 秒</div></div>`;
ON.liveRetryIns = () => loadInspect(CUR.id);
ON.liveInsLatest = () => { LIVE.viewing[CUR.id] = null; delete LIVE.report[CUR.id]; rerender(); };
ON.liveInsOpen = d => { LIVE.viewing[CUR.id] = d.id; delete LIVE.report[CUR.id]; go('inspect', 'result'); rerender(); };
pages.inspect.history = function () {
  if (!LIVE.on) return _inspectDemo.history.call(this);
  const list = LIVE.reports[CUR.id];
  if (!list) { loadInspect(CUR.id); return loadingCard('正在读取巡检历史…'); }
  const pts = list.slice(0, 20).reverse();
  return `<div class="card"><h2>巡检历史 <span class="sub">保留 90 天 · 共 ${list.length} 份</span></h2>
    ${pts.length > 1 ? lineChart([{name: '健康分', color: 'var(--c2)', data: pts.map(h => h.score)}], {h: 140, xl: [pts[0], pts[Math.floor(pts.length / 2)], pts[pts.length - 1]].map(h => fmtTime(h.at).slice(5, 16))}) : ''}
    <div class="tbl-wrap" style="margin-top:10px"><table><thead><tr><th>报告编号</th><th>时间</th><th>触发</th><th>健康分</th><th>异常</th><th>警告</th><th>跳过</th><th>耗时</th><th>操作</th></tr></thead><tbody>
    ${list.map(h => `<tr><td class="mono">${h.id}</td><td>${fmtTime(h.at)}</td><td>${esc(h.by)}</td><td><b>${h.score}</b></td><td>${h.counts.bad || 0}</td><td>${h.counts.warn || 0}</td><td>${h.counts.skip || 0}</td><td>${dur(h.durationMs)}</td><td><a data-on="liveInsOpen" data-id="${h.id}">查看</a></td></tr>`).join('') || '<tr><td colspan="9" class="muted">暂无</td></tr>'}
    </tbody></table></div></div>`;
};
pages.inspect.rule = function () {
  if (!LIVE.on) return _inspectDemo.rule.call(this);
  const R = LIVE.rules, s = LIVE.sched[CUR.id];
  if (!R || !s) { loadInspect(CUR.id); return loadingCard('正在读取巡检规则…'); }
  const every = s.every === 'hours' ? `每 ${s.hours} 小时` : `每天 ${s.at}`;
  return `<div class="grid g2">
    <div class="card"><h2>定时巡检 · ${esc(CUR.name)} <span class="right">${B('inspect.schedule', '编辑', {cls: 'sm'})}</span></h2>
      ${[['状态', s.enabled ? '<span class="tag ok">已启用</span>' : '<span class="tag">已停用</span>'], ['执行周期', every], ['巡检范围', (s.cats || []).length ? s.cats.join('、') : '全部分类'], ['上次执行', s.lastRun && !s.lastRun.startsWith('0001') ? fmtTime(s.lastRun) : '—'], ['结果通知', ntText(s.notify)], ['上次通知', ntLast(s)]].map(([k, v]) => `<div class="row between small" style="padding:6px 0;border-bottom:1px solid var(--border)"><span class="muted">${k}</span><span>${v}</span></div>`).join('')}</div>
    <div class="card"><h2>健康分计算</h2><div class="small">满分 100：每个<span class="tag bad">异常</span>扣 4 分，每个<span class="tag warn">警告</span>扣 1 分；建议、跳过、采集失败与被忽略的项不扣分。<br>≥ 85 健康 · 70–84 关注 · &lt; 70 风险。<br><br><span class="tag">跳过</span> 表示前置条件不满足（如未配置 SSH / 审计表，或与集群架构无关），不代表正常。</div></div>
  </div>
  <div class="card" style="margin-top:16px"><h2>巡检规则 / 阈值 <span class="sub">${R.rules.length} 条内置规则，阈值对所有集群生效</span></h2>
    <div class="tbl-wrap"><table><thead><tr><th>分类</th><th>规则</th><th>检查方法（数据来源）</th><th>阈值</th><th>状态</th><th>参考文档</th><th>操作</th></tr></thead><tbody>
    ${R.rules.map(r => `<tr style="${r.Enabled ? '' : 'opacity:.5'}"><td class="small muted">${r.Cat}</td><td>${esc(r.Name)}</td><td class="mono small">${esc(r.Src)}</td><td class="small">${esc(r.Threshold) || '—'}${(r.Warn !== r.DefWarn && r.Warn != null && r.DefWarn != null && r.Warn !== r.DefWarn) || (r.Bad != null && r.DefBad != null && r.Bad !== r.DefBad) ? ' <span class="tag info">已修改</span>' : ''}</td>
      <td>${r.Enabled ? '<span class="tag ok">启用</span>' : '<span class="tag">停用</span>'}</td><td>${docLink(r.Doc)}</td><td>${B('inspect.rule', '编辑', {cls: 'sm', data: {id: r.ID}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
};
const _insItemDemo = ON.insItem;
ON.insItem = (d, el, e) => {
  if (!LIVE.on) return _insItemDemo(d, el, e);
  if (e.target.closest('button')) return;
  const i = INSPECT_ITEMS.find(x => x.name === d.n);
  openDrawer(i.name, `<div class="row">${tagOf(i.st)}<span class="tag">${i.cat}</span>${i.ignored ? '<span class="tag">已忽略</span>' : ''}</div>
    <h4>当前值</h4><div>${esc(i.val)}（阈值：${esc(i.th)}）</div>
    ${(i.details || []).length ? `<h4>明细（${i.details.length}）</h4><div class="tbl-wrap"><table><tbody>${i.details.map(x => `<tr><td class="mono small">${esc(x.target)}</td><td class="small">${esc(x.value)}</td><td>${tagOf(x.st)}</td></tr>`).join('')}</tbody></table></div>` : ''}
    <h4>检查方法</h4>${code(i.src)}<h4>${i.st === 'skip' ? '跳过原因' : '处置建议'}</h4><div>${esc(i.adv)}</div><h4>参考</h4>${docLink(i.doc, 'StarRocks 官方文档 ↗')}`, true);
};
const _insActs = {run: ACTIONS['inspect.run'], ignore: ACTIONS['inspect.ignore'], schedule: ACTIONS['inspect.schedule'], rule: ACTIONS['inspect.rule']};
ACTIONS['inspect.run'] = () => {
  if (!LIVE.on) return _insActs.run();
  if (INS.running) return;
  const id = CUR.id;
  INS.running = true; INS.started = Date.now();
  if (ROUTE.page !== 'inspect') go('inspect', 'result'); else rerender();
  clearInterval(insTimer); insTimer = setInterval(() => { const el = $('#insElapsed'); if (el) el.textContent = Math.round((Date.now() - INS.started) / 1000); }, 1000);
  api('POST', `/api/clusters/${encodeURIComponent(id)}/inspections`).then(rep => {
    LIVE.viewing[id] = rep.id; LIVE.report[id] = rep; (LIVE.reports[id] = LIVE.reports[id] || []).unshift(rep); delete LIVE.ov[id];
    if (CUR.id === id) liveApplyReport(rep);
    toast(`巡检完成：健康分 ${rep.score}`);
  }).catch(e => toast('巡检失败：' + e.message)).finally(() => { INS.running = false; clearInterval(insTimer); if (ROUTE.page === 'inspect' || ROUTE.page === 'overview') rerender(); });
};
ACTIONS['inspect.ignore'] = d => {
  if (!LIVE.on) return _insActs.ignore(d);
  const it = INSPECT_ITEMS.find(x => x.name === d.n);
  api('POST', `/api/clusters/${encodeURIComponent(CUR.id)}/inspect/ignore`, {rule: it.id, ignore: !it.ignored}).then(rep => {
    if (rep) { liveApplyReport(rep); const l = LIVE.reports[CUR.id]; if (l && l[0] && l[0].id === rep.id) { l[0].score = rep.score; l[0].counts = rep.counts; } }
    delete LIVE.ov[CUR.id]; rerender();
  }).catch(e => toast(e.message));
};
/* ---------- 定时巡检结果通知（群机器人 / Webhook） ---------- */
const NT_TYPES = ['企业微信', '钉钉', '飞书', 'Lark', 'Slack', 'Webhook', '邮件'];
const NT_HINT = {
  '企业微信': '群聊 → 添加群机器人 → 复制 Webhook 地址。',
  '钉钉': '群设置 → 机器人 → 自定义机器人。安全设置选「加签」时填写 SEC 开头的密钥；选「自定义关键词」时关键词请包含“巡检”。',
  '飞书': CHANNEL_TYPES['飞书'].hint, 'Lark': CHANNEL_TYPES['Lark'].hint,
  'Slack': 'Slack App → Incoming Webhooks 生成 Webhook URL。平台访问境外地址时，可在服务启动环境中设置 HTTPS_PROXY 代理。',
  '邮件': '经「平台设置 → 通知」中配置的邮件服务（SMTP）发送，多个收件人用逗号或分号分隔，最多 20 个。邮件为纯文本，主题为健康分摘要。',
  'Webhook': '以 JSON POST 到该地址（含健康分、各状态数量与问题项）。填写密钥时附带请求头 X-SROps-Timestamp 与 X-SROps-Signature（sha256=HMAC(密钥, 时间戳 + "." + 请求体)）。',
};
const ntWhen = n => n.when === 'always' ? '每次巡检后' : `有异常项或健康分低于 ${n.minScore} 分时`;
const ntText = n => n ? `${n.type} · ${ntWhen(n)}<div class="small muted mono">${esc(n.urlMasked || '')}${n.hasSecret ? ' · 已设置签名密钥' : ''}</div>` : '<span class="muted">不通知</span>';
const ntLast = s => !s.lastNotify || s.lastNotify.startsWith('0001') ? '—' : `${fmtTime(s.lastNotify)} ${s.lastNotifyResult === '成功' ? '<span class="tag ok">成功</span>' : `<span class="tag bad" title="${esc(s.lastNotifyResult)}">失败</span><div class="small muted">${esc(s.lastNotifyResult)}</div>`}`;
function ntFields(type, n) {
  const same = n && n.type === type, T = CHANNEL_TYPES[type] || {};
  if (type === '邮件') return `<label class="req">收件人邮箱</label><input id="sNu" placeholder="${esc(same ? '已保存：' + n.urlMasked + '（留空保持不变）' : 'dba@example.com; ops@example.com')}">
    <label></label><div class="small muted">${esc(NT_HINT['邮件'])}</div>`;
  return `<label class="req">Webhook 地址</label><input id="sNu" class="mono" placeholder="${esc(same ? '已保存：' + n.urlMasked + '（留空保持不变）' : T.ph || 'https://')}">
    <label>签名密钥</label><div><input id="sNs" type="password" autocomplete="new-password" placeholder="${esc(same && n.hasSecret ? '已设置（留空保持不变）' : T.secret || '可选')}" style="width:100%">${same && n.hasSecret ? '<label class="chk small"><input type="checkbox" id="sNc"> 清除已保存的密钥</label>' : ''}</div>
    <label></label><div class="small muted">${esc(NT_HINT[type] || '')}</div>`;
}
function ntBody() {
  const type = $('#sNt').value; if (!type) return null;
  const c = $('#sNc');
  const sec = $('#sNs');
  return {type, url: $('#sNu').value.trim(), secret: sec ? sec.value : '', clearSecret: !!(c && c.checked), when: $('#sNw').value, minScore: +$('#sNm').value || 0};
}
document.addEventListener('change', e => {
  if (e.target.id === 'sNt') { const t = e.target.value, n = LIVE.sched[CUR.id] && LIVE.sched[CUR.id].notify; $('#sNtBox').hidden = !t; if (t) $('#sNtF').innerHTML = ntFields(t, n); }
  if (e.target.id === 'sNw') $('#sNmBox').hidden = e.target.value !== 'problem';
});
ON.liveNtTest = (d, el) => {
  const body = ntBody(); if (!body) return toast('请先选择通知方式');
  el.disabled = true; el.textContent = '发送中…';
  api('POST', `/api/clusters/${encodeURIComponent(CUR.id)}/inspect/notify-test`, body)
    .then(() => toast('测试消息已发送，请到对应群 / 频道确认'))
    .catch(e => toast('发送失败：' + e.message))
    .finally(() => { el.disabled = false; el.textContent = '发送测试消息'; });
};
ACTIONS['inspect.schedule'] = () => {
  if (!LIVE.on) return _insActs.schedule();
  const s = LIVE.sched[CUR.id], n = s.notify;
  openModal({title: '定时巡检 · ' + CUR.name, code: 'inspect.schedule', body: `<div class="form">
    <label>启用</label><label class="chk"><input type="checkbox" id="sOn" ${s.enabled ? 'checked' : ''}> 按周期自动巡检</label>
    <label class="req">周期</label><select id="sEv"><option value="daily" ${s.every !== 'hours' ? 'selected' : ''}>每天</option><option value="hours" ${s.every === 'hours' ? 'selected' : ''}>每隔 N 小时</option></select>
    <label>每天时间</label><input type="time" id="sAt" value="${s.at || '10:00'}"><label>间隔（小时）</label><input type="number" id="sHr" min="1" max="168" value="${s.hours || 6}">
    <label>巡检范围</label><div>${INSPECT_CATS.map(c => `<label class="chk" style="margin-right:10px"><input type="checkbox" class="sCat" value="${c}" ${!(s.cats || []).length || s.cats.includes(c) ? 'checked' : ''}>${c}</label>`).join('')}</div>
    <label>结果通知</label><select id="sNt"><option value="">不通知</option>${NT_TYPES.map(t => `<option ${n && n.type === t ? 'selected' : ''}>${t}</option>`).join('')}</select></div>
    <div id="sNtBox" ${n ? '' : 'hidden'}><div class="form" style="margin-top:8px">
      <label>发送条件</label><div class="row"><select id="sNw" style="width:auto"><option value="problem" ${!n || n.when === 'problem' ? 'selected' : ''}>仅有问题时</option><option value="always" ${n && n.when === 'always' ? 'selected' : ''}>每次巡检后</option></select>
        <span id="sNmBox" class="row small" ${n && n.when === 'always' ? 'hidden' : ''}>有异常项或健康分低于 <input type="number" id="sNm" min="0" max="100" value="${n ? n.minScore : 80}" style="width:70px"> 分</span></div>
      <div id="sNtF" style="display:contents">${n ? ntFields(n.type, n) : ''}</div>
      <label></label><div class="row"><button class="btn sm" data-on="liveNtTest">发送测试消息</button><span class="small muted">内容为该集群最近一次巡检报告；定时巡检失败时也会通知</span></div></div></div>`,
    onOk() {
      const cats = $$('.sCat').filter(x => x.checked).map(x => x.value);
      const notify = ntBody();
      if (notify && !notify.url && !(n && n.type === notify.type)) { toast('请填写 Webhook 地址'); return false; }
      const body = {enabled: $('#sOn').checked, every: $('#sEv').value, at: $('#sAt').value, hours: +$('#sHr').value, cats: cats.length === INSPECT_CATS.length ? [] : cats, notify};
      api('PUT', `/api/clusters/${encodeURIComponent(CUR.id)}/inspect/schedule`, body).then(x => { LIVE.sched[CUR.id] = x; closeModal(); toast('已保存'); rerender(); }).catch(e => toast(e.message));
      return false;
    }});
};
ACTIONS['inspect.rule'] = d => {
  if (!LIVE.on) return _insActs.rule(d);
  const r = LIVE.rules.rules.find(x => x.ID === d.id); if (!r) return;
  const op = r.Lower ? '<' : '≥', u = r.Unit || '';
  openModal({title: '编辑规则：' + r.Name, code: 'inspect.rule', body: `<div class="form">
    <label>启用</label><label class="chk"><input type="checkbox" id="rEn" ${r.Enabled ? 'checked' : ''}> 参与巡检</label>
    ${r.DefWarn != null ? `<label>警告阈值（${op}）</label><div class="row"><input type="number" step="any" id="rW" value="${r.Warn}" style="width:140px"> ${u}<span class="small muted">默认 ${r.DefWarn}</span></div>` : ''}
    ${r.DefBad != null ? `<label>异常阈值（${op}）</label><div class="row"><input type="number" step="any" id="rB" value="${r.Bad}" style="width:140px"> ${u}<span class="small muted">默认 ${r.DefBad}</span></div>` : ''}
    ${r.DefWarn == null && r.DefBad == null ? '<label>阈值</label><div class="small muted">该规则为固定判定，没有可调阈值。</div>' : ''}
    <label>检查方法</label><div class="mono small">${esc(r.Src)}</div></div>`,
    onOk() {
      const body = {enabled: $('#rEn').checked};
      if ($('#rW')) body.warn = +$('#rW').value;
      if ($('#rB')) body.bad = +$('#rB').value;
      api('PUT', '/api/inspect/rules/' + r.ID, body).then(v => { Object.assign(r, v); closeModal(); toast('已保存，下次巡检生效'); rerender(); }).catch(e => toast(e.message));
      return false;
    }});
};

/* ---------- 集群导入向导：走服务端 ---------- */
const _ci = {test: ON.ciTest, ssh: ON.ciSsh, done: ON.ciDone, open: ciOpen};
ciOpen = (c, step) => {
  _ci.open(c, step);
  if (LIVE.on && !CI.pubkey) api('GET', '/api/ssh/pubkey').then(x => { CI.pubkey = x.publicKey; if ($('#mask').classList.contains('open') && CI.step === 3) ciPaint(); }).catch(() => {});
};
const ciHosts = f => f.hosts.split(',').map(s => s.trim()).filter(Boolean);
ON.ciTest = () => {
  if (!LIVE.on) return _ci.test();
  ciSave(); const f = CI.f;
  if (!f.hosts || !f.user || !f.pwd) return toast('请填写 FE 地址、运维账号和密码');
  toast('正在连接 ' + ciHosts(f)[0] + ':' + f.qport + ' …');
  api('POST', '/api/clusters/test', {id: CI.edit || '', hosts: ciHosts(f), queryPort: +f.qport, httpPort: +f.hport, user: f.user, password: f.pwd})
    .then(d => { CI.det = {version: d.version, mode: d.mode, nodes: d.nodes, grants: d.grants.map(g => [g.name, g.ok, g.use]), warnings: d.warnings}; CI.sshOk = false; ciPaint(); })
    .catch(e => { CI.det = null; ciPaint(); toast('连接失败：' + e.message); });
};
const ciSSHBody = f => ({id: CI.edit || '', user: f.sshUser, port: +f.sshPort, auth: f.sshAuth === '平台密钥' ? 'key' : 'password', password: f.sshPwd || '', sudo: f.sudo, feDir: f.feDir, beDir: f.beDir});
ON.ciSsh = () => {
  if (!LIVE.on) return _ci.ssh();
  ciSave(); const f = CI.f;
  if (!f.sshUser || !f.feDir || !f.beDir) return toast('请填写 SSH 用户与部署目录');
  toast('正在逐台测试 SSH …');
  api('POST', '/api/clusters/test-ssh', {...ciSSHBody(f), nodes: CI.det.nodes.map(n => ({host: n.host, type: n.type}))}).then(r => {
    CI.sshOk = r.ok; CI.sshHosts = r.hosts; ciPaint();
  }).catch(e => toast('SSH 测试失败：' + e.message));
};
ON.ciDone = () => {
  if (!LIVE.on) return _ci.done();
  ciSave(); const f = CI.f;
  const body = {name: f.name, env: f.env, owner: f.owner, desc: f.desc, hosts: ciHosts(f), queryPort: +f.qport, httpPort: +f.hport, user: f.user, password: f.pwd,
    prom: f.prom, promSelector: f.promSel, audit: f.audit, ssh: f.sshLater ? null : ciSSHBody(f)};
  const edit = CI.edit;
  api(edit ? 'PUT' : 'POST', edit ? '/api/clusters/' + encodeURIComponent(edit) : '/api/clusters', body).then(async c => {
    closeModal();
    delete LIVE.ov[c.id]; delete LIVE.topo[c.id];
    await liveLoadClusters();
    CUR = CLUSTERS.find(x => x.id === c.id); NODES = [];
    buildTop();
    toast(c.missing.length ? `已导入 ${c.name}（待完善：仅可查看）` : `${c.name} 已就绪，可以开始运维`);
    if (edit || location.hash === '#overview') render(edit ? undefined : 'overview'); else go('overview');
  }).catch(e => toast('保存失败：' + e.message));
};
const _clActs = {test: ACTIONS['cluster.test'], del: ACTIONS['cluster.delete']};
ACTIONS['cluster.test'] = d => {
  if (!LIVE.on) return _clActs.test(d);
  openModal({title: '连通性测试 · ' + d.id, ok: '', cancel: '关闭', body: '<div class="muted" id="ctBody">测试中…</div>'});
  api('GET', `/api/clusters/${encodeURIComponent(d.id)}/test`).then(items => {
    const el = $('#ctBody'); if (!el) return;
    el.outerHTML = items.map(x => `<div class="row between" style="padding:8px 0;border-bottom:1px solid var(--border)"><span>${esc(x.Name)}</span><span class="small muted" style="flex:1;text-align:right;margin-right:10px">${esc(x.Detail)}</span>${tagOf(x.Status)}</div>`).join('');
  }).catch(e => { const el = $('#ctBody'); if (el) el.textContent = e.message; });
};
ACTIONS['cluster.delete'] = d => {
  if (!LIVE.on) return _clActs.del(d);
  const c = CLUSTERS.find(x => x.id === d.id);
  confirmTyped('移除集群 ' + esc(c.name), '仅从平台移除纳管关系，<b>不会</b>停止或删除 StarRocks 集群。该集群的巡检报告与定时任务将一并删除。', c.name, '确认移除', () =>
    api('DELETE', '/api/clusters/' + encodeURIComponent(c.id)).then(async res => { if (livePending(res)) return; closeModal(); delete LIVE.ov[c.id]; delete LIVE.topo[c.id]; await liveLoadClusters(); buildTop(); rerender(); toast('已移除'); }).catch(liveFail), 'cluster.delete');
};

/* ---------- 切换集群：清空缓存的节点 ---------- */
document.addEventListener('change', e => {
  if (LIVE.on && e.target.id === 'clusterSel') { INS.cat = '全部'; INS.st = '全部'; }
}, true);

/* ---------- 用户管理 / 角色权限（真实） ----------
   用户、角色、权限由服务端保存并在每次请求时校验；前端按 /api/me 返回的权限渲染按钮。
   这里把服务端数据映射为原型页面使用的 USERS / ROLES 结构，复用原有页面。 */
const RBAC = {loaded: false, at: 0, busy: false};
const encL = s => encodeURIComponent(s);
function liveSetRoles(list) { ROLES.length = 0; (list || []).forEach(r => ROLES.push(r)); }
function liveMapUser(u) {
  return {...u, id: u.login, status: u.status, last: u.lastLogin && !String(u.lastLogin).startsWith('0001') ? fmtTime(u.lastLogin) : '—', created: String(u.createdAt || '').slice(0, 10)};
}
const permSig = () => [...myPerms()].sort().join() + '|' + [...(myClusters() || ['*'])].sort().join();
/* 重新读取当前用户、角色与用户列表；有权限时才读取后两者。权限有变化时刷新菜单。 */
async function liveLoadRbac() {
  const before = permSig();
  const me = await api('GET', '/api/me');
  ME.roles = me.roles || []; ME.name = me.name || me.login; ME.dept = me.dept || '—'; ME.phone = me.phone || '';
  liveSetRoles(me.roleList);
  if (can('roles:view') || can('users:view') || can('user.create') || can('user.edit')) liveSetRoles(await api('GET', '/api/roles'));
  if (can('users:view') || can('roles:view')) { const us = await api('GET', '/api/users'); USERS.length = 0; us.forEach(u => USERS.push(liveMapUser(u))); }
  else { USERS.length = 0; USERS.push(ME); }
  RBAC.loaded = true; RBAC.at = Date.now();
  await liveLoadSettings();
  if (permSig() !== before) { buildNav(); buildTop(); }
}
/* 读取并重新渲染；silent=静默刷新（数据没有变化时不重绘，且已有读取在进行时跳过）。非静默的读取按顺序执行，保证操作后的列表是最新的。 */
let rbacChain = Promise.resolve();
function liveRbacLoad(silent, then) {
  if (silent && RBAC.busy) return;
  rbacChain = rbacChain.then(() => {
    RBAC.busy = true;
    const snap = () => JSON.stringify([USERS, ROLES]);
    const before = snap();
    return liveLoadRbac().then(() => { delete LIVE.err.rbac; if (then) then(); if (!silent || snap() !== before || then) rerender(); })
      .catch(e => { LIVE.err.rbac = e.message; if (!silent) rerender(); })
      .finally(() => { RBAC.busy = false; RBAC.at = Date.now(); });
  });
}
ON.liveRetryRbac = () => { delete LIVE.err.rbac; rerender(); };
function rbacGate() {
  if (!RBAC.loaded) { if (!LIVE.err.rbac) liveRbacLoad(); return LIVE.err.rbac ? errCard('读取失败：' + LIVE.err.rbac, 'liveRetryRbac') : loadingCard('正在读取用户与角色…'); }
  if (Date.now() - RBAC.at > 20000) liveRbacLoad(true);
  return null;
}
/* 当前用户能否授予 / 管理该角色：角色的权限与数据范围都不能超出自己的（服务端同样校验） */
function liveCovers(r) {
  if (ME.roles.includes('r_admin')) return true;
  const mine = myPerms(), mc = myClusters();
  if (!r.perms.every(p => mine.has(p))) return false;
  return mc === null || (!r.clusters.includes('*') && r.clusters.every(c => mc.has(c)));
}
/* 权限被服务端拒绝（多半是角色刚被管理员调整）时，重新读取权限 */
const _apiBase = api;
api = async function (method, path, body) {
  try { return await _apiBase(method, path, body); }
  catch (e) { if (/^无权限/.test(e.message) && !RBAC.busy) setTimeout(() => liveRbacLoad(true), 0); throw e; }
};

const _usersDemo = pages.users.render, _rolesDemo = pages.roles.render;
const userStTag = u => (u.locked ? '<span class="tag bad">已锁定</span>' : u.tempLocked ? '<span class="tag bad" title="连续输错密码被临时锁定，30 分钟后自动解除，或由管理员点「解锁」">登录失败锁定</span>' : u.expired ? '<span class="tag bad">已过期</span>' : '<span class="tag ok">正常</span>')
  + (u.mustChange ? ' <span class="tag info" title="尚未修改初始密码 / 重置后的密码">待改密</span>' : '');
pages.users.render = function () {
  if (!LIVE.on) return _usersDemo.call(this);
  const g = rbacGate(); if (g) return g;
  const list = USERS.filter(u => (!UF.q || (u.login + u.name + (u.dept || '')).includes(UF.q)) && (!UF.st || u.status === UF.st));
  const ops = u => {
    if (u.self) return B('user.edit', '编辑', {cls: 'sm', data: {id: u.id}});
    if (!u.manageable) return '<span class="small muted" title="该用户的角色权限高于您，不能管理">无权管理</span>';
    return B('user.edit', '编辑', {cls: 'sm', data: {id: u.id}}) + B('user.reset', '重置密码', {cls: 'sm', data: {id: u.id}})
      + (u.login !== 'admin' ? B('user.lock', u.locked || u.tempLocked ? '解锁' : '锁定', {cls: 'sm', data: {id: u.id}}) + B('user.delete', '删除', {cls: 'sm danger', data: {id: u.id}}) : '');
  };
  return `<div class="toolbar"><input type="search" id="ufQ" placeholder="账号 / 姓名 / 部门" value="${esc(UF.q)}"><select id="ufS"><option value="">全部状态</option><option value="active" ${UF.st === 'active' ? 'selected' : ''}>正常</option><option value="locked" ${UF.st === 'locked' ? 'selected' : ''}>不可登录</option></select>
    <span class="sp"></span>${B('user.create', '＋ 新建用户', {cls: 'primary'})}</div>
  <div class="card"><div class="tbl-wrap"><table><thead><tr><th>账号</th><th>姓名</th><th>部门</th><th>邮箱 / 手机</th><th>角色</th><th>状态</th><th>有效期</th><th>最近登录</th><th>操作</th></tr></thead><tbody>
  ${list.map(u => `<tr><td class="mono"><b>${esc(u.login)}</b>${u.self ? ' <span class="tag brand">当前</span>' : ''}</td><td>${esc(u.name)}</td><td>${esc(u.dept || '—')}</td><td class="small">${esc(u.email || '—')}<br>${esc(u.phone || '')}</td>
    <td>${u.roles.map(r => `<span class="tag">${esc(roleName(r))}</span>`).join(' ')}</td><td>${userStTag(u)}</td><td class="small">${u.expireAt ? esc(u.expireAt) : '永久'}</td><td class="small">${u.last}</td><td class="ops">${ops(u)}</td></tr>`).join('') || '<tr><td colspan="9" class="muted">没有符合条件的用户</td></tr>'}
  </tbody></table></div><div class="small muted" style="margin-top:8px">用户的最终权限 = 所有角色权限的并集；数据范围 = 所有角色可访问集群的并集。内置账号 admin 不可锁定 / 删除。只能授予、管理<b>权限不超过自己</b>的角色与用户。</div></div>`;
};
pages.roles.render = function () {
  if (!LIVE.on) return _rolesDemo.call(this);
  const g = rbacGate(); if (g) return g;
  return _rolesDemo.call(this);
};

const _userFormDemo = userForm;
userForm = function (u = {}) {
  if (!LIVE.on) return _userFormDemo(u);
  const edit = !!u.login, self = edit && u.self;
  return `<div class="form"><label class="req">账号</label><input id="uL" value="${esc(u.login || '')}" ${edit ? 'disabled' : ''} placeholder="3–32 位字母 / 数字 / 下划线">
  <label class="req">姓名</label><input id="uN" maxlength="50" value="${esc(u.name || '')}"><label>部门</label><input id="uD" maxlength="50" value="${esc(u.dept || '')}"><label class="req">邮箱</label><input id="uE" type="email" value="${esc(u.email || '')}"><label>手机</label><input id="uP" maxlength="30" value="${esc(u.phone || '')}">
  ${edit ? '' : `<label>初始密码</label><input id="uPw" type="password" autocomplete="new-password" placeholder="留空则生成随机密码（创建后显示一次）"><label></label><div class="hint">至少 10 位，含大小写字母、数字与符号；首次登录强制修改</div>`}
  <label class="req">角色</label><div>${ROLES.map(r => { const okc = liveCovers(r); return `<label class="chk" style="margin-right:12px" title="${okc ? '' : '该角色的权限或数据范围超出了您自己的，不能授予'}"><input type="checkbox" class="uR" value="${esc(r.id)}" ${(u.roles || []).includes(r.id) ? 'checked' : ''} ${self || !okc ? 'disabled' : ''}>${esc(r.name)}${okc ? '' : ' 🔒'}</label>`; }).join('')}</div>
  ${self ? '<label></label><div class="small muted">不能修改自己的角色与有效期，请联系其他管理员</div>' : ''}
  <label>有效期</label><div class="row"><input type="date" id="uX" value="${esc(u.expireAt || '')}" ${self || u.login === 'admin' ? 'disabled' : ''} style="width:auto"><span class="small muted">留空 = 永久；到期当天结束后不能登录</span></div>
  ${edit ? '' : `<label>邮件通知</label><label class="chk"><input type="checkbox" id="uMail"> 同时把账号与密码发送到用户邮箱</label><label></label><div class="small muted">需先在「平台设置 → 通知」配置邮件服务；邮件以明文传输密码，请仅在内部可信的邮件服务上使用。</div>`}</div>`;
};
function readUserForm(edit) {
  const b = {name: $('#uN').value.trim(), dept: $('#uD').value.trim(), email: $('#uE').value.trim(), phone: $('#uP').value.trim(), expireAt: $('#uX').value, roles: $$('.uR').filter(x => x.checked).map(x => x.value)};
  if (!edit && !/^\w{3,32}$/.test($('#uL').value.trim())) return '账号须为 3–32 位字母、数字或下划线';
  if (!b.name) return '请填写姓名';
  if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(b.email)) return '邮箱格式不正确';
  if (!b.roles.length) return '至少选择一个角色';
  return b;
}
/* 密码只在创建 / 重置后显示这一次 */
function showSecretOnce(title, login, pw, label, res) {
  openModal({title, ok: '', cancel: '我已记录', body: `<div class="notice warn"><span>🔑</span><div>${label}<b>只显示这一次</b>，关闭后无法再次查看。请通过安全渠道告知用户，用户首次登录时必须修改。</div></div>
    <div class="form"><label>账号</label><b class="mono">${esc(login)}</b><label>${label.replace(/，.*$/, '')}</label><div class="row"><code class="mono" style="font-size:15px;user-select:all">${esc(pw)}</code><button class="btn sm" data-copy="${esc(pw)}">复制</button></div></div>
    ${res && res.mailSent ? `<div class="small" style="margin-top:8px;color:var(--ok)">✓ 已发送到 ${esc(res.mailSent)}</div>` : ''}${res && res.mailError ? `<div class="small" style="margin-top:8px;color:var(--bad)">✗ 邮件发送失败：${esc(res.mailError)}</div>` : ''}`});
  $('#modal').dataset.force = '1'; const x = $('#modal header [data-mclose]'); if (x) x.remove();
}
/* 平台级高危操作：输入目标名称确认（不依赖当前集群） */
function confirmTyped(title, detail, expect, okText, run, code) {
  const typed = SETTINGS.highRiskConfirm;
  openModal({title: '⚠ ' + title, okCls: 'danger primary', ok: SETTINGS.dualReview ? '提交复核' : okText, code, body: `<div class="notice bad"><span>⚠</span><div>${detail}</div></div>
    ${SETTINGS.dualReview ? '<div class="notice warn"><span>👥</span><div>平台已开启<b>双人复核</b>：提交后需另一名拥有同权限的用户在「操作审计 → 待复核」中批准后才会执行。</div></div>' : ''}
    ${typed ? `<div class="form" style="margin-top:8px"><label class="req">输入确认</label><input id="cfTyped" placeholder="请输入 ${esc(expect)}"></div>` : ''}`,
    onOk() { if (typed && $('#cfTyped').value.trim() !== expect) { toast('输入的名称不匹配'); return false; } run(); return false; }});
}
const liveFail = e => toast(e.message);

const _uAct = {create: ACTIONS['user.create'], edit: ACTIONS['user.edit'], reset: ACTIONS['user.reset'], lock: ACTIONS['user.lock'], del: ACTIONS['user.delete']};
ACTIONS['user.create'] = () => {
  if (!LIVE.on) return _uAct.create();
  openModal({title: '新建用户', code: 'user.create', body: userForm(), onOk() {
    const b = readUserForm(false); if (typeof b === 'string') { toast(b); return false; }
    b.login = $('#uL').value.trim(); const pw = $('#uPw').value; if (pw) b.password = pw;
    b.sendMail = !!$('#uMail').checked;
    api('POST', '/api/users', b).then(res => { closeModal(); liveRbacLoad(false); if (res.initialPassword) showSecretOnce('用户已创建', res.user.login, res.initialPassword, '初始密码', res); else toast('已创建用户 ' + res.user.login + (res.mailSent ? '，账号信息已邮件发送' : '')); if (res.mailError) toast('邮件发送失败：' + res.mailError); }).catch(liveFail);
    return false;
  }});
};
ACTIONS['user.edit'] = d => {
  if (d.goUsers) return go('users');
  if (!LIVE.on) return _uAct.edit(d);
  const u = USERS.find(x => x.id == d.id); if (!u) return;
  openModal({title: '编辑用户 ' + u.login, code: 'user.edit', body: userForm(u), onOk() {
    const b = readUserForm(true); if (typeof b === 'string') { toast(b); return false; }
    api('PUT', `/api/users/${encL(u.login)}`, b).then(() => { closeModal(); toast('已保存'); liveRbacLoad(false); }).catch(liveFail);
    return false;
  }});
};
ACTIONS['user.reset'] = d => {
  if (!LIVE.on) return _uAct.reset(d);
  const u = USERS.find(x => x.id == d.id); if (!u) return;
  openModal({title: '重置密码 ' + u.login, code: 'user.reset', body: `<div class="form"><label>方式</label><select id="rsM"><option value="random">生成随机密码（仅显示一次）</option><option value="manual">手动设置</option></select>
    <label id="rsL" style="display:none">新密码</label><input id="rsP" type="password" autocomplete="new-password" style="display:none" placeholder="至少 ${SETTINGS.pwdMinLen} 位，含大小写字母、数字与符号">
    <label>邮件通知</label><label class="chk"><input type="checkbox" id="rsMail"> 同时发送到用户邮箱</label></div>
    <div class="small muted" style="margin-top:8px">重置后该用户的所有登录会话立即下线，并解除登录失败锁定；用户下次登录必须修改密码。</div>`, onOk() {
    const manual = $('#rsM').value === 'manual', pw = $('#rsP').value;
    if (manual && !pw) { toast('请输入新密码'); return false; }
    const rb = manual ? {password: pw} : {}; rb.sendMail = !!$('#rsMail').checked;
    api('POST', `/api/users/${encL(u.login)}/reset`, rb).then(res => { closeModal(); liveRbacLoad(false); if (res.password) showSecretOnce('密码已重置', u.login, res.password, '新密码', res); else toast('密码已重置，该用户需在下次登录时修改' + (res.mailSent ? '（已邮件发送）' : '')); if (res.mailError) toast('邮件发送失败：' + res.mailError); }).catch(liveFail);
    return false;
  }});
};
document.addEventListener('change', e => { if (e.target.id === 'rsM') { const m = e.target.value === 'manual' ? '' : 'none'; $('#rsL').style.display = $('#rsP').style.display = m; } });
ACTIONS['user.lock'] = d => {
  if (!LIVE.on) return _uAct.lock(d);
  const u = USERS.find(x => x.id == d.id); if (!u) return;
  const lock = !(u.locked || u.tempLocked);
  api('POST', `/api/users/${encL(u.login)}/lock`, {locked: lock}).then(() => { toast((lock ? '已锁定 ' : '已解锁 ') + u.login); liveRbacLoad(false); }).catch(liveFail);
};
ACTIONS['user.delete'] = d => {
  if (!LIVE.on) return _uAct.del(d);
  const u = USERS.find(x => x.id == d.id); if (!u) return;
  confirmTyped('删除用户 ' + esc(u.login), '删除后该账号无法登录，历史审计记录保留。建议优先使用「锁定」。', u.login, '删除', () =>
    api('DELETE', `/api/users/${encL(u.login)}`).then(res => { if (livePending(res)) return; closeModal(); toast('已删除 ' + u.login); liveRbacLoad(false); }).catch(liveFail), 'user.delete');
};

/* ----- 角色 ----- */
const _rAct = {edit: ACTIONS['role.edit'], create: ACTIONS['role.create'], copy: ACTIONS['role.copy'], del: ACTIONS['role.delete']};
const afterRoleChange = id => () => { if (id) RE.id = id; loadDraft(); };
ACTIONS['role.create'] = () => {
  if (!LIVE.on) return _rAct.create();
  openModal({title: '新建角色', code: 'role.create', body: `<div class="form"><label class="req">名称</label><input id="rN" maxlength="30"><label>描述</label><textarea id="rD" rows="2" maxlength="200"></textarea><label>初始权限</label><select id="rT"><option value="">空白（仅「集群总览·查看」）</option>${ROLES.filter(liveCovers).map(r => `<option value="${esc(r.id)}">复制「${esc(r.name)}」</option>`).join('')}</select></div>`,
    onOk() {
      const n = $('#rN').value.trim(); if (!n) { toast('请输入名称'); return false; }
      const src = ROLES.find(r => r.id === $('#rT').value);
      api('POST', '/api/roles', {name: n, desc: $('#rD').value.trim(), perms: src ? src.perms : ['overview:view'], clusters: src ? src.clusters : []})
        .then(r => { closeModal(); liveRbacLoad(false, afterRoleChange(r.id)); }).catch(liveFail);
      return false;
    }});
};
ACTIONS['role.copy'] = d => {
  if (!LIVE.on) return _rAct.copy(d);
  api('POST', `/api/roles/${encL(d.id)}/copy`).then(r => { toast('已复制为「' + r.name + '」'); liveRbacLoad(false, afterRoleChange(r.id)); }).catch(liveFail);
};
ACTIONS['role.delete'] = d => {
  if (!LIVE.on) return _rAct.del(d);
  const r = ROLES.find(x => x.id === d.id); if (!r) return;
  confirmTyped('删除角色 ' + esc(r.name), '删除后不可恢复。仍有成员的角色不能删除。', r.name, '删除', () =>
    api('DELETE', `/api/roles/${encL(r.id)}`).then(res => { if (livePending(res)) return; closeModal(); toast('已删除角色'); liveRbacLoad(false, () => { RE.id = (ROLES[0] || {}).id; loadDraft(); }); }).catch(liveFail), 'role.delete');
};
ACTIONS['role.edit'] = d => {
  if (!LIVE.on) return _rAct.edit(d);
  const r = ROLES.find(x => x.id === (d.meta || RE.id)); if (!r) return;
  if (d.meta) return openModal({title: '编辑角色信息', code: 'role.edit', body: `<div class="form"><label class="req">名称</label><input id="rN" maxlength="30" value="${esc(r.name)}"><label>描述</label><textarea id="rD" rows="2" maxlength="200">${esc(r.desc)}</textarea></div>`, onOk() {
    api('PUT', `/api/roles/${encL(r.id)}`, {name: $('#rN').value.trim(), desc: $('#rD').value.trim()}).then(() => { closeModal(); toast('已保存'); liveRbacLoad(false, afterRoleChange(r.id)); }).catch(liveFail);
    return false;
  }});
  const add = [...RE.draft].filter(c => !r.perms.includes(c)), del = r.perms.filter(c => !RE.draft.has(c));
  const label = c => c.endsWith(':view') ? MENUS[c.split(':')[0]].name + '·查看' : (BTNS[c] || {}).name || c;
  const members = USERS.filter(u => u.roles.includes(r.id)).length;
  openModal({title: `保存角色「${r.name}」`, code: 'role.edit', body: `<div class="small">变更立即对 <b>${members}</b> 名成员生效（每次请求都按最新权限校验，已登录用户无需重新登录）。</div>
    ${add.length ? `<h4 style="color:var(--ok)">＋ 新增 ${add.length} 项</h4><div class="small">${add.map(c => `<span class="tag ok" style="margin:2px">${esc(label(c))}</span>`).join('')}</div>` : ''}
    ${del.length ? `<h4 style="color:var(--bad)">－ 移除 ${del.length} 项</h4><div class="small">${del.map(c => `<span class="tag bad" style="margin:2px">${esc(label(c))}</span>`).join('')}</div>` : ''}
    <h4>数据范围</h4><div class="small">${RE.clusters.has('*') ? '全部集群' : [...RE.clusters].map(esc).join('、') || '<span class="up">无（该角色将无法访问任何集群）</span>'}</div>`,
    onOk() {
      api('PUT', `/api/roles/${encL(r.id)}`, {perms: [...RE.draft], clusters: [...RE.clusters]}).then(() => { closeModal(); toast('已保存'); liveRbacLoad(false, afterRoleChange(r.id)); }).catch(liveFail);
      return false;
    }});
};

/* ---------- 平台设置（真实） ----------
   设置保存在服务端，登录后读取并写入原型页面使用的 SETTINGS / NOTIFY_CFG / BASIC_CFG / WATERMARK；保存时调用接口。 */
function liveApplySettings(st) {
  Object.assign(SETTINGS, st.security || {});
  if (st.basic) Object.assign(BASIC_CFG, st.basic);
  BASIC_CFG.name = st.name || BASIC_CFG.name;
  if (st.url !== undefined && !st.basic) BASIC_CFG.url = st.url;
  if (st.notify) Object.assign(NOTIFY_CFG, st.notify);
  if (st.watermark) Object.assign(WATERMARK, st.watermark);
  if (st.ip) ME.ip = st.ip;
  applyBasic(); paintWatermark();
}
async function liveLoadSettings() { liveApplySettings(await api('GET', '/api/settings')); }
const _commitDemo = commitSetting;
commitSetting = function (kind, v, target, extra) {
  if (!LIVE.on) return _commitDemo(kind, v, target, extra);
  const body = {...v, ...(extra || {})};
  delete body.smtpPwd;
  api('PUT', '/api/settings/' + kind, body).then(res => {
    Object.assign(target, res);
    if (kind === 'basic') applyBasic();
    if (kind === 'watermark') paintWatermark();
    closeModal(); toast('已保存'); rerender();
  }).catch(liveFail);
  return false;
};
const _nTestDemo = ON.nTestMail;
ON.nTestMail = (d, el) => {
  if (!LIVE.on) return _nTestDemo();
  const host = $('#nH').value.trim(); if (!host) return toast('请先填写邮件服务器');
  const body = {...NOTIFY_CFG, smtpHost: host, smtpPort: +$('#nP').value, smtpSec: $('#nS').value, smtpSkipVerify: $('#nV').checked, smtpUser: $('#nU').value.trim(), from: $('#nF').value.trim(), fromName: $('#nN').value.trim(), smtpPassword: $('#nW').value};
  delete body.smtpPwd; delete body.smsSecretSet;
  el.disabled = true; el.textContent = '发送中…';
  api('POST', '/api/settings/notify/test-mail', body).then(r => toast('测试邮件已发送到 ' + r.to + '，请查收')).catch(e => toast('发送失败：' + e.message)).finally(() => { el.disabled = false; el.textContent = '发送测试邮件'; });
};

const _nTestSmsDemo = ON.nTestSms;
ON.nTestSms = (d, el) => {
  if (!LIVE.on) return _nTestSmsDemo();
  const phone = $('#nGPh').value.trim(); if (!phone) return toast('请先填写接收测试短信的手机号');
  const body = {...NOTIFY_CFG, smsProvider: $('#nGP').value, smsUrl: $('#nG').value.trim(), smsKeyId: $('#nGK').value.trim(), smsSign: $('#nG2').value.trim(), smsTemplate: $('#nGT').value.trim(), smsAppId: $('#nGA').value.trim(), smsRegion: $('#nGR').value.trim(), smsParams: $('#nGV').value.trim(), smsSecret: $('#nGS').value, phone};
  delete body.smtpPwd;
  el.disabled = true; el.textContent = '发送中…';
  api('POST', '/api/settings/notify/test-sms', body).then(r => toast('测试短信已发送到 ' + r.to + '，请查收')).catch(e => toast('发送失败：' + e.message)).finally(() => { el.disabled = false; el.textContent = '发送'; });
};

/* ---------- 双人复核：高危操作返回 202（待复核）时的统一处理 ---------- */
function livePending(res) {
  if (res && res.pending) { closeModal(); toast('已提交复核：需另一名拥有该权限的用户批准后才会执行'); REVS.loaded = false; return true; }
  return false;
}

/* ---------- 操作审计（真实） ---------- */
const AUDS = {items: [], more: false, key: null, loading: false, err: ''};
const REVS = {items: [], pending: 0, loaded: false, loading: false, err: '', dual: false};
const audQuery = () => { const p = new URLSearchParams(); [['user', 'user'], ['result', 'res'], ['q', 'q'], ['from', 'from'], ['to', 'to']].forEach(([k, f]) => AUD[f] && p.set(k, AUD[f])); return p; };
function liveAuditLoad(append) {
  if (AUDS.loading) return;
  AUDS.loading = true; AUDS.err = '';
  const key = audQuery().toString(), p = audQuery(); p.set('limit', 100); p.set('offset', append ? AUDS.items.length : 0);
  api('GET', '/api/audit?' + p).then(r => { AUDS.items = append ? AUDS.items.concat(r.items) : r.items; AUDS.more = r.more; AUDS.key = key; })
    .catch(e => { AUDS.err = e.message; }).finally(() => { AUDS.loading = false; if (ROUTE.page === 'audit') rerender(); });
}
function liveReviewsLoad() {
  if (REVS.loading) return;
  REVS.loading = true; REVS.err = '';
  api('GET', '/api/reviews').then(r => { REVS.items = r.items; REVS.pending = r.pending; REVS.dual = r.dualReview; REVS.loaded = true; })
    .catch(e => { REVS.err = e.message; }).finally(() => { REVS.loading = false; if (ROUTE.page === 'audit') rerender(); });
}
ON.liveRetryAud = () => { AUDS.err = ''; REVS.err = ''; AUDS.key = null; REVS.loaded = false; rerender(); };
ON.liveAudRefresh = () => { AUDS.key = null; REVS.loaded = false; rerender(); };
ON.liveAudMore = () => liveAuditLoad(true);
const _audit = {render: pages.audit.render, init: pages.audit.init};
const REV_ST = {pending: ['warn', '待复核'], executing: ['info', '执行中'], approved: ['ok', '已批准并执行'], rejected: ['bad', '已驳回'], withdrawn: ['', '已撤回'], expired: ['', '已失效'], failed: ['bad', '执行失败']};
pages.audit.render = function (sub) {
  if (!LIVE.on) return _audit.render.call(this, sub);
  sub = sub || 'ops';
  if (!REVS.loaded && !REVS.err) liveReviewsLoad();
  const head = tabs('audit', [['ops', '平台操作审计'], ['review', `待复核 (${REVS.pending})`], ['sql', 'SQL 审计']], sub);
  if (sub === 'review') return head + liveAuditReview();
  if (sub === 'sql') return head + liveSqlAuditHtml();   // live_ops.js
  const key = audQuery().toString();
  if (AUDS.key !== key && !AUDS.loading && !AUDS.err) liveAuditLoad(false);
  if (AUDS.err) return head + errCard('读取审计日志失败：' + AUDS.err, 'liveRetryAud');
  const tag = a => `<span class="tag ${a.resClass === '成功' ? 'ok' : a.resClass === '待复核' ? 'warn' : 'bad'}" title="${esc(a.res)}">${esc(a.resClass)}</span>${a.resClass === '失败' && a.res.length > 2 ? `<div class="small muted" style="max-width:180px">${esc(a.res.replace(/^失败：/, ''))}</div>` : ''}`;
  return head + `<div class="toolbar"><input id="auU" placeholder="账号" value="${esc(AUD.user)}" style="width:110px"><select id="auR"><option value="">全部结果</option>${['成功', '失败', '拒绝', '待复核'].map(r => `<option ${r === AUD.res ? 'selected' : ''}>${r}</option>`).join('')}</select>
      <input type="date" id="auF" value="${esc(AUD.from || '')}" title="开始日期" style="width:auto"> – <input type="date" id="auT" value="${esc(AUD.to || '')}" title="结束日期（含当天）" style="width:auto">
      <input type="search" id="auQ" placeholder="操作 / 权限码 / 执行内容" value="${esc(AUD.q)}"><button class="btn sm" data-on="liveAudRefresh">⟳ 刷新</button><span class="sp"></span>${B('audit.export', '⇩ 导出 CSV')}</div>
    <div class="card">${AUDS.loading && !AUDS.items.length ? loadingCard('正在读取审计日志…') : `<div class="tbl-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>来源 IP</th><th>集群</th><th>菜单</th><th>权限码</th><th>操作</th><th>结果</th><th>执行内容</th></tr></thead><tbody>
    ${AUDS.items.map(a => `<tr><td class="small">${fmtTime(a.at)}</td><td>${esc(a.user)}</td><td class="mono small">${esc(a.ip)}</td><td>${esc(a.cluster || '—')}</td><td>${esc(a.menu)}</td><td class="mono small">${esc(a.code)}</td><td>${esc(a.act)}</td><td>${tag(a)}</td><td class="mono small" style="max-width:300px;white-space:pre-wrap">${esc(a.sql || '—')}</td></tr>`).join('') || '<tr><td colspan="9" class="muted">没有符合条件的记录</td></tr>'}
    </tbody></table></div>${AUDS.more ? `<div style="text-align:center;margin-top:10px"><button class="btn" data-on="liveAudMore" ${AUDS.loading ? 'disabled' : ''}>${AUDS.loading ? '加载中…' : '加载更多'}</button></div>` : ''}`}
    <div class="small muted" style="margin-top:8px">平台所有按钮操作（包括因无权限被拒绝的尝试）均记录审计，保留 ${BASIC_CFG.auditDays || 180} 天，保留期内不可删除。数据范围受限的用户只能看到范围内集群的记录和自己的操作。最多导出 50000 条，导出本身也会被记录。</div></div>`;
};
function liveAuditReview() {
  if (REVS.err) return errCard('读取复核申请失败：' + REVS.err, 'liveRetryAud');
  if (!REVS.loaded) return loadingCard('正在读取复核申请…');
  const stTag = r => { const [c, t] = REV_ST[r.status] || ['', r.status]; return `<span class="tag ${c}">${t}</span>`; };
  const ops = r => r.canApprove ? `<button class="btn sm primary" data-on="rvOk" data-id="${esc(r.id)}">批准并执行</button><button class="btn sm" data-on="rvNo" data-id="${esc(r.id)}">驳回</button>`
    : r.status === 'pending' ? (r.mine ? `<button class="btn sm" data-on="rvWd" data-id="${esc(r.id)}">撤回</button><span class="small muted"> 等待他人复核</span>` : '<span class="small muted">您没有该操作的权限，不能复核</span>')
    : `<span class="small muted">${r.reviewer ? esc(r.reviewer) + ' · ' : ''}${fmtTime(r.decidedAt)}${r.note ? '<br>' + esc(r.note) : ''}</span>`;
  return `<div class="notice"><span>👥</span><div>开启「平台设置 → 安全策略 → 双人复核」后，高危操作先提交到这里，由<b>另一位</b>拥有相同权限码（且数据范围包含该集群）的用户批准后，平台才以申请人的身份执行；24 小时内未处理自动失效。当前：${REVS.dual ? '<span class="tag ok">已开启</span>' : '<span class="tag">未开启</span>'}</div></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>提交时间</th><th>提交人</th><th>集群</th><th>权限码</th><th>操作</th><th>执行内容</th><th>状态</th><th>处理</th></tr></thead><tbody>
    ${REVS.items.map(r => `<tr><td class="small">${fmtTime(r.createdAt)}</td><td>${esc(r.requester)}${r.mine ? ' <span class="tag brand">我</span>' : ''}</td><td>${esc(r.cluster || '—')}</td><td class="mono small">${esc(r.code)}</td><td>${esc(r.title)}</td><td class="mono small">${esc(r.detail)}</td><td>${stTag(r)}</td><td class="ops">${ops(r)}</td></tr>`).join('') || '<tr><td colspan="8" class="muted">暂无复核申请</td></tr>'}
    </tbody></table></div></div>`;
}
pages.audit.init = function (sub) {
  if (!LIVE.on) return _audit.init.call(this, sub);
  const set = (id, f) => { const el = $(id); if (el) el.onchange = () => { AUD[f] = el.value.trim(); AUDS.key = null; AUDS.err = ''; rerender(); }; };
  set('#auU', 'user'); set('#auR', 'res'); set('#auQ', 'q'); set('#auF', 'from'); set('#auT', 'to');
};
const afterReview = () => { REVS.loaded = false; AUDS.key = null; liveLoadClusters().then(() => { buildTop(); rerender(); }).catch(() => rerender()); liveRbacLoad(true); };
const _rvDemo = {ok: ON.rvOk, no: ON.rvNo};
ON.rvOk = (d, el) => {
  if (!LIVE.on) return _rvDemo.ok(d, el);
  el.disabled = true;
  api('POST', `/api/reviews/${encL(d.id)}/approve`).then(r => { toast(r.ok ? '已批准，操作已执行' : '已批准，但执行失败：' + r.note); afterReview(); }).catch(e => { toast(e.message); afterReview(); });
};
ON.rvNo = d => {
  if (!LIVE.on) return _rvDemo.no(d);
  openModal({title: '驳回复核申请', ok: '驳回', okCls: 'danger primary', body: `<div class="form"><label>原因</label><input id="rvNote" maxlength="200" placeholder="选填，申请人可见"></div>`, onOk() {
    api('POST', `/api/reviews/${encL(d.id)}/reject`, {note: $('#rvNote').value}).then(() => { closeModal(); toast('已驳回'); afterReview(); }).catch(liveFail);
    return false;
  }});
};
ON.rvWd = d => api('POST', `/api/reviews/${encL(d.id)}/withdraw`).then(() => { toast('已撤回'); afterReview(); }).catch(liveFail);
const _audExpDemo = ACTIONS['audit.export'];
ACTIONS['audit.export'] = () => {
  if (!LIVE.on) return _audExpDemo();
  const a = document.createElement('a'); a.href = '/api/audit/export?' + audQuery(); a.download = ''; document.body.appendChild(a); a.click(); a.remove();
  toast('正在导出审计日志…'); AUDS.key = null;
};

/* ---------- 未接入后端的页面：顶部提示演示数据 ---------- */
const _renderLive = render;
render = function (...a) {
  const r = _renderLive.apply(this, a);
  if (LIVE.on && $('#login').hidden && !LIVE_PAGES.includes(ROUTE.page) && !(CLUSTER_PAGES.includes(ROUTE.page) && !CLUSTERS.length)) {
    $('#view').insertAdjacentHTML('afterbegin', '<div class="notice"><span>🧪</span><div>该页面<b>尚未接入后端</b>，以下为演示数据，与当前集群无关。已接入：集群总览、集群架构、性能巡检、查询分析、资源隔离、导入作业、物化视图、备份恢复、参数配置、告警中心、节点管理、安装部署、升级回滚、集群接入、用户管理、角色权限、操作审计、平台设置。</div></div>');
  }
  return r;
};
