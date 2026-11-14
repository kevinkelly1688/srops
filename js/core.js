// @author: kevinkelly
// @license: Apache Licence
// @file: core.js
// @time: 2026/10/28

/* ============================================================================
   通用工具：DOM / 图表 / 弹窗 / 抽屉 / toast / 路由 / 按钮动作分发 / 审计
   ========================================================================== */
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];
const esc = s => String(s == null ? '' : s).replace(/[&<>"]/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;'}[c]));
const rng = seed => { let s = seed >>> 0; return () => (s = (s * 1664525 + 1013904223) >>> 0) / 4294967296; };
const fmt = n => n >= 1e8 ? (n / 1e8).toFixed(1) + ' 亿' : n >= 1e4 ? (n / 1e4).toFixed(1) + ' 万' : String(Math.round(n * 100) / 100);
const now = () => { const d = new Date(); const p = x => String(x).padStart(2, '0'); return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`; };
const docLink = (slug, text) => `<a class="ref" href="${DOC}${slug}" target="_blank" rel="noopener">${text || '官方文档 ↗'}</a>`;
const stTag = {ok: ['ok', '正常'], warn: ['warn', '警告'], bad: ['bad', '异常'], info: ['info', '建议'], skip: ['', '跳过'], error: ['bad', '采集失败']};
const tagOf = st => `<span class="tag ${stTag[st][0]}">${stTag[st][1]}</span>`;
const sevText = {critical: '严重', warning: '警告', info: '提示'};
const sevCls = {critical: 'bad', warning: 'warn', info: 'info'};
const nodeSt = {up: ['ok', '运行中'], warn: ['warn', '告警'], down: ['bad', '已停止'], decommission: ['info', '下线中'], joining: ['info', '扩容中']};
const stDot = s => `<span style="white-space:nowrap"><span class="dot ${nodeSt[s][0]}"></span>${nodeSt[s][1]}</span>`;
const meter = (v, w = 70, b = 85) => v == null ? '<span class="muted small">—</span>' : `<div class="meter" title="${v}%"><b class="${v >= b ? 'bad' : v >= w ? 'warn' : ''}" style="width:${v}%"></b></div>`;

let toastTimer;
function toast(msg) {
  const t = $('#toast'); t.textContent = msg; t.classList.add('show');
  clearTimeout(toastTimer); toastTimer = setTimeout(() => t.classList.remove('show'), 1800);
}
function copyText(text) {
  const done = () => toast('已复制到剪贴板');
  if (navigator.clipboard && window.isSecureContext) navigator.clipboard.writeText(text).then(done, fb); else fb();
  function fb() { const ta = document.createElement('textarea'); ta.value = text; document.body.appendChild(ta); ta.select(); try { document.execCommand('copy'); } catch (e) {} ta.remove(); done(); }
}
const code = text => `<pre class="code"><button class="cp" data-copy="${esc(text)}">复制</button>${esc(text)}</pre>`;

/* ---------- 迷你图表（SVG） ---------- */
function series(seed, n, base, amp, spike) {
  const r = rng(seed); const out = [];
  for (let i = 0; i < n; i++) {
    let v = base + Math.sin(i / 6) * amp * .5 + (r() - .5) * amp;
    if (spike && i >= spike[0] && i <= spike[1]) v += spike[2];
    out.push(Math.max(0, v));
  }
  return out;
}
function lineChart(list, {w = 560, h = 170, unit = '', xl = ['-60m', '-45m', '-30m', '-15m', '现在']} = {}) {
  const pad = {l: 46, r: 10, t: 10, b: 22};
  const max = (Math.max(...list.flatMap(s => s.data)) * 1.1) || 1, n = list[0].data.length;
  const X = i => pad.l + (w - pad.l - pad.r) * i / (n - 1), Y = v => pad.t + (h - pad.t - pad.b) * (1 - v / max);
  let g = '';
  for (let k = 0; k <= 4; k++) { const v = max * k / 4, y = Y(v); g += `<line x1="${pad.l}" x2="${w - pad.r}" y1="${y}" y2="${y}" stroke="var(--border)"/><text x="${pad.l - 6}" y="${y + 4}" text-anchor="end" font-size="10" fill="var(--muted)">${fmt(v)}${unit}</text>`; }
  xl.forEach((t, i) => g += `<text x="${pad.l + (w - pad.l - pad.r) * i / (xl.length - 1)}" y="${h - 6}" text-anchor="middle" font-size="10" fill="var(--muted)">${t}</text>`);
  const paths = list.map(s => `<path d="${s.data.map((v, i) => (i ? 'L' : 'M') + X(i).toFixed(1) + ' ' + Y(v).toFixed(1)).join('')}" fill="none" stroke="${s.color}" stroke-width="1.8"/>`).join('');
  const lg = list.length > 1 ? `<div class="legend">${list.map(s => `<span><i style="background:${s.color}"></i>${s.name}</span>`).join('')}</div>` : '';
  return `<svg viewBox="0 0 ${w} ${h}" role="img" style="width:100%;height:auto">${g}${paths}</svg>${lg}`;
}
function stackBar(items) {
  const total = items.reduce((a, b) => a + b[1], 0) || 1;
  return `<div class="bar">${items.map(([n, v, c]) => `<span style="width:${v / total * 100}%;background:${c}" title="${esc(n)} ${v}%">${v >= 8 ? `${esc(n)} ${v}%` : ''}</span>`).join('')}</div>
  <div class="legend">${items.map(([n, v, c]) => `<span><i style="background:${c}"></i>${esc(n)} ${v}%</span>`).join('')}</div>`;
}
function ring(score) {
  const c = score >= 85 ? 'var(--ok)' : score >= 70 ? 'var(--warn)' : 'var(--bad)';
  return `<div class="ring" style="background:conic-gradient(${c} ${score * 3.6}deg,var(--panel2) 0)"><div><span><b style="color:${c}">${score}</b><br><span class="small muted">健康分</span></span></div></div>`;
}

/* ---------- 抽屉 / 弹窗 ---------- */
function openDrawer(title, html, wide) { $('#drTitle').textContent = title; $('#drBody').innerHTML = html; $('#drawer').classList.toggle('wide', !!wide); $('#drawer').classList.add('open'); }
function closeDrawer() { $('#drawer').classList.remove('open', 'wide'); }
let MODAL_OK = null;
function openModal({title, body, ok = '确定', okCls = 'primary', wide = false, code: pcode = '', onOk = null, cancel = '取消'}) {
  const m = $('#modal');
  m.className = 'modal' + (wide ? ' wide' : '');
  delete m.dataset.force;
  m.innerHTML = `<header><h3>${title}</h3><button class="btn sm" data-mclose="1">✕</button></header><div class="mbody">${body}</div>
    <footer>${pcode ? `<span class="perm-code">权限码：${pcode}${BTNS[pcode] && BTNS[pcode].risk ? ' · <b style="color:var(--bad)">高危</b>' : ''}</span>` : ''}
    ${cancel ? `<button class="btn" data-mclose="1">${cancel}</button>` : ''}${ok ? `<button class="btn ${okCls}" id="mOk">${ok}</button>` : ''}</footer>`;
  MODAL_OK = onOk; $('#mask').classList.add('open');
  const f = m.querySelector('input,select,textarea'); if (f) setTimeout(() => f.focus(), 30);
}
function closeModal() { $('#mask').classList.remove('open'); MODAL_OK = null; }

/* 高危操作确认：输入集群名；开启双人复核时提交为待复核 */
function risky(pcode, title, detail, sql, done) {
  const needInput = SETTINGS.highRiskConfirm;
  openModal({title: '⚠ ' + title, okCls: 'danger primary', ok: SETTINGS.dualReview ? '提交复核' : '确认执行', code: pcode,
    body: `<div class="notice bad"><span>⚠</span><div>${detail}</div></div>
      ${sql ? `<div class="small muted">将执行：</div>${code(sql)}` : ''}
      ${SETTINGS.dualReview ? '<div class="notice warn"><span>👥</span><div>平台已开启<b>双人复核</b>：提交后需另一名拥有同权限的用户在「操作审计 → 待复核」中批准后才会执行。</div></div>' : ''}
      ${needInput ? `<div class="form" style="margin-top:8px"><label class="req">集群名确认</label><input id="riskInput" placeholder="请输入 ${CUR.name}"></div>` : ''}`,
    onOk() {
      if (needInput && $('#riskInput').value.trim() !== CUR.name) { toast('集群名不匹配'); return false; }
      if (SETTINGS.dualReview) { audit(pcode, title + '（待复核）', '待复核', sql); toast('已提交复核'); return true; }
      done(); return true;
    }});
}

/* ---------- 审计 ---------- */
function audit(pcode, act, res = '成功', sql = '—') {
  const meta = BTNS[pcode];
  AUDIT.unshift({at: now(), user: ME.login, ip: '10.1.1.2', cluster: CUR ? CUR.name : '—', menu: meta ? MENUS[meta.menu].name : '—', code: pcode, act, res, sql: sql || '—'});
}

/* ---------- 路由 ---------- */
const pages = {};
const ACTIONS = {};          // 权限码 → 处理函数 (dataset, el)
let ROUTE = {page: 'overview', sub: ''};
function go(page, sub = '') { location.hash = page + (sub ? '/' + sub : ''); }
function render(route) {
  const [page, sub = ''] = ((typeof route === 'string' ? route : location.hash.slice(1)) || 'overview').split('/');
  ROUTE = {page: pages[page] ? page : 'overview', sub};
  const p = pages[ROUTE.page], view = $('#view');
  $('#pageTitle').textContent = MENUS[ROUTE.page] ? MENUS[ROUTE.page].name : p.title;
  $('#crumb').textContent = MENUS[ROUTE.page] ? MENUS[ROUTE.page].group : '';
  $$('.nav a').forEach(a => a.classList.toggle('active', a.dataset.go === ROUTE.page));
  if (!canView(ROUTE.page)) {
    view.innerHTML = `<div class="card" style="text-align:center;padding:60px 20px"><div style="font-size:48px">🔒</div><h2 style="justify-content:center">403 · 无权访问「${MENUS[ROUTE.page].name}」</h2>
      <p class="muted">当前用户 <b>${ME.login}</b> 的角色没有菜单权限 <code>${ROUTE.page}:view</code>。请联系管理员在「平台配置 → 角色权限」中授权。</p></div>`;
    return;
  }
  const cl = myClusters();
  if (CLUSTER_PAGES.includes(ROUTE.page) && !CLUSTERS.length) { view.innerHTML = onboardingHtml(); return; }
  if (cl && CUR && !cl.has(CUR.id) && !['users', 'roles', 'clusters', 'settings', 'help', 'audit', 'deploy'].includes(ROUTE.page)) {
    view.innerHTML = `<div class="card" style="text-align:center;padding:60px 20px"><div style="font-size:48px">⛔</div><h2 style="justify-content:center">无权访问集群「${CUR.name}」</h2><p class="muted">当前角色的数据范围仅包含：${[...cl].join('、')}。请在顶栏切换集群。</p></div>`;
    return;
  }
  const miss = CLUSTER_PAGES.includes(ROUTE.page) ? clusterMissing(CUR) : [];
  view.innerHTML = (miss.length ? `<div class="notice warn"><span>🔒</span><div>集群「<b>${esc(CUR.name)}</b>」必填信息未完善（缺少：${miss.join('、')}），当前<b>只能查看</b>，运维操作已禁用。
    ${can('cluster.edit') ? `<a data-act="cluster.edit" data-id="${esc(CUR.id)}" data-step="3">立即完善 →</a>` : '请联系管理员在「集群接入」中补全。'}</div></div>` : '') + p.render(sub);
  p.init && p.init(sub);
  window.scrollTo(0, 0);
}
const rerender = () => render(ROUTE.page + (ROUTE.sub ? '/' + ROUTE.sub : ''));
const tabs = (page, list, cur) => `<div class="tabs">${list.map(([id, t]) => `<a href="#${page}/${id}" class="${cur === id ? 'on' : ''}">${t}</a>`).join('')}</div>`;

/* ---------- 侧栏（按菜单权限生成） ---------- */
function buildNav() {
  let h = '';
  PERM_TREE.forEach(g => {
    const ms = g.menus.filter(m => canView(m.id));
    if (!ms.length) return;
    h += `<div class="nav-group">${g.group}</div>` + ms.map(m => `<a data-go="${m.id}" href="#${m.id}"><span class="ico">${m.ico}</span>${m.name}${m.id === 'alert' && ALERTS.some(a => a.state === 'firing') ? `<span class="badge">${ALERTS.filter(a => a.state === 'firing').length}</span>` : ''}</a>`).join('');
  });
  $('#nav').innerHTML = h;
}
function buildTop() {
  const cl = myClusters();
  $('#clusterSel').innerHTML = CLUSTERS.length ? '' : '<option>（尚未导入集群）</option>';
  $('#clusterSel').innerHTML += CLUSTERS.map(c => `<option value="${c.id}" ${CUR && c.id === CUR.id ? 'selected' : ''} ${cl && !cl.has(c.id) ? 'disabled' : ''}>${c.name}（${c.version} · ${modeText(c.mode)}）${cl && !cl.has(c.id) ? ' 🔒' : clusterMissing(c).length ? ' · 待完善' : ''}</option>`).join('');
  $('#userSel').innerHTML = USERS.filter(u => u.status === 'active').map(u => `<option value="${u.id}" ${u.id === ME.id ? 'selected' : ''}>${u.login}（${u.roles.map(r => (ROLES.find(x => x.id === r) || {}).name).join('/')}）</option>`).join('');
  $('#avatar').textContent = ME.name.slice(0, 1);
}

/* ---------- 全局事件 ---------- */
document.addEventListener('click', e => {
  const t = e.target;
  const cp = t.closest('[data-copy]'); if (cp) { copyText(cp.dataset.copy); return; }
  if (t.closest('[data-close]')) { closeDrawer(); return; }
  if (t.closest('[data-mclose]') || (t.id === 'mask' && !$('#modal').dataset.force)) { closeModal(); return; }
  if (t.closest('#mOk')) { if (!MODAL_OK || MODAL_OK() !== false) closeModal(); return; }
  if (t.closest('[data-print]')) { window.print(); return; }
  const a = t.closest('[data-act]');
  if (a) {
    const pc = a.dataset.act;
    if (!can(pc)) { audit(pc, '尝试操作：' + (BTNS[pc] || {}).name, '拒绝（无权限）'); toast('无权限'); return; }
    if (ACTIONS[pc]) ACTIONS[pc](a.dataset, a); else toast('原型：' + ((BTNS[pc] || {}).name || pc));
    return;
  }
  const h = t.closest('[data-on]');   // 页面内非权限类交互
  if (h && ON[h.dataset.on]) { ON[h.dataset.on](h.dataset, h, e); return; }
});
const ON = {};   // 普通交互（查看、切换、筛选），不需要权限码
