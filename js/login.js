// @author: kevinkelly
// @license: Apache Licence
// @file: login.js
// @time: 2026/10/28

/* 登录页 / 退出 / 个人菜单 / 修改密码 / 主题（浅色 · 深色 · 护眼） */
const THEMES = [['light', '☀ 浅色'], ['dark', '☾ 深色'], ['eye', '🌿 护眼']];
function curTheme() { return document.documentElement.dataset.theme || 'light'; }
function setTheme(t) {
  document.documentElement.dataset.theme = t;
  try { localStorage.setItem('sr_theme', t); } catch (e) {}
  paintThemeSw();
}
function paintThemeSw() {
  $$('[data-theme-sw]').forEach(el => el.innerHTML = THEMES.map(([id, n]) => `<button data-on="theme" data-t="${id}" class="${curTheme() === id ? 'on' : ''}" title="${n}">${n}</button>`).join(''));
}
ON.theme = d => { setTheme(d.t); toast('已切换为' + THEMES.find(x => x[0] === d.t)[1].slice(2) + '主题'); };

/* ---------- 登录页 ---------- */
const LG = {fails: {}, lockedUntil: {}, err: ''};
function showLogin() {
  closeModal(); closeDrawer(); $('#umenu').classList.remove('open');
  $('#app').hidden = true; const el = $('#login'); el.hidden = false;
  let last = ''; try { last = localStorage.getItem('sr_last_login') || ''; } catch (e) {}
  el.innerHTML = `
  <div class="brand">
    <div class="lg"><i>★</i>StarRocks Ops</div>
    <h2>StarRocks 集群运维平台</h2>
    <p>覆盖部署、巡检、诊断、扩缩容、升级、备份与告警的一站式运维；权限精确到每一个操作按钮，全部操作可审计。</p>
    <div class="feat">
      <div><b>⬇ 安装部署</b>向导式部署存算一体 / 存算分离，环境预检一键修复</div>
      <div><b>✚ 性能巡检</b>9 类 27 项规则，健康评分与处置建议</div>
      <div><b>▤ 集群运维</b>扩缩容、滚动升级、备份恢复、参数变更</div>
      <div><b>⛨ 权限审计</b>菜单 + 按钮级授权、高危二次确认、双人复核</div>
    </div>
    <div class="foot">知识来源：StarRocks 官方文档 docs.mirrorship.cn · 平台 v0.6.0（原型）</div>
    <svg width="420" height="420" viewBox="0 0 100 100"><polygon points="50,2 61,38 98,38 68,60 79,96 50,74 21,96 32,60 2,38 39,38" fill="#fff"/></svg>
  </div>
  <div class="pane">
    <div class="tools"><div class="theme-sw" data-theme-sw="1"></div></div>
    <form class="box" id="lgForm" autocomplete="off">
      <h3>欢迎登录</h3><div class="muted small" style="margin-bottom:20px">请使用平台账号和密码登录</div>
      <div class="fld"><label for="lgU">账号</label><input id="lgU" placeholder="请输入账号" value="${esc(last)}" autocomplete="username"></div>
      <div class="fld"><label for="lgP">密码</label><div class="pw"><input id="lgP" type="password" placeholder="请输入密码" autocomplete="current-password"><button type="button" data-on="lgEye" title="显示 / 隐藏密码">👁</button></div></div>
      <div class="err" id="lgErr">${esc(LG.err)}</div>
      <div class="row between small" style="margin-bottom:14px"><label class="chk"><input type="checkbox" id="lgRem" ${last ? 'checked' : ''}> 记住账号</label><a data-on="lgForgot">忘记密码？</a></div>
      <button class="btn primary go" type="submit">登 录</button>
      <div class="demo">🧪 演示账号（选择后自动填充，密码相同）：
        <select data-on="noop" id="lgDemo"><option value="">— 选择角色 —</option>${USERS.map(u => `<option value="${u.login}">${u.login} · ${u.roles.map(roleName).join('/')}${u.status === 'locked' ? '（已锁定）' : ''}</option>`).join('')}</select>
        ${CLUSTERS.length ? `<label class="chk" style="margin-top:8px" title="清空已纳管集群与演示数据，只保留内置管理员 admin，体验平台刚部署完成后导入集群的流程"><input type="checkbox" id="lgFresh"> 模拟全新部署（无集群，仅 admin）</label>` : ''}</div>
      <div class="small muted" style="margin-top:14px;text-align:center">登录即表示同意《平台使用规范》，所有操作将被记录审计</div>
    </form>
  </div>`;
  paintThemeSw();
  $('#lgForm').onsubmit = e => { e.preventDefault(); tryLogin(); };
  $('#lgDemo').onchange = e => { const u = USERS.find(x => x.login === e.target.value); if (!u) return; LG.err = ''; showLogin(); $('#lgU').value = u.login; $('#lgP').value = DEMO_PWD; };
  setTimeout(() => (last ? $('#lgP') : $('#lgU')).focus(), 30);
}
function lgError(msg) { LG.err = msg; const e = $('#lgErr'); if (e) e.textContent = msg; }
function tryLogin() {
  const login = $('#lgU').value.trim(), pwd = $('#lgP').value;
  if (!login || !pwd) return lgError('请输入账号和密码');
  const u = USERS.find(x => x.login === login);
  if (LG.lockedUntil[login] && LG.lockedUntil[login] > Date.now()) return lgError(`连续失败 ${SETTINGS.loginFailLock} 次，账号已临时锁定 30 分钟，请稍后再试或联系管理员`);
  if (!u || pwd !== DEMO_PWD) {
    const n = LG.fails[login] = (LG.fails[login] || 0) + 1;
    authAudit(login, '登录失败（账号或密码错误）', '失败');
    if (n >= SETTINGS.loginFailLock) { LG.lockedUntil[login] = Date.now() + 30 * 60e3; return lgError(`连续失败 ${n} 次，账号已临时锁定 30 分钟`); }
    return lgError(`账号或密码错误，还可尝试 ${SETTINGS.loginFailLock - n} 次`);
  }
  if (u.status !== 'active') { authAudit(login, '登录被拒绝（账号已锁定）', '拒绝'); return lgError('账号已被管理员锁定，请联系管理员解锁'); }
  const fresh = $('#lgFresh') && $('#lgFresh').checked;
  if (fresh && login !== 'admin') return lgError('全新部署的平台只有内置管理员 admin，请使用 admin 登录');
  try { $('#lgRem').checked ? localStorage.setItem('sr_last_login', login) : localStorage.removeItem('sr_last_login'); } catch (e) {}
  if (fresh) applyFreshInstall();
  LG.fails[login] = 0; enterApp(u, '账号密码');
  if (fresh) { go('overview'); setTimeout(() => { ON.uPwd(); toast('首次登录，请先修改初始密码'); }, 300); }
}
function authAudit(user, act, res) { AUDIT.unshift({at: now(), user, ip: '10.1.1.2', cluster: '—', menu: '登录', code: 'auth.login', act, res, sql: '—'}); }
function enterApp(u, how) {
  ME = u; LG.err = '';
  const prev = u.last; u.last = now();
  authAudit(u.login, `登录成功（${how}）`, '成功');
  try { sessionStorage.setItem('sr_uid', u.id); } catch (e) {}
  const cl = myClusters(); if (cl && CUR && !cl.has(CUR.id)) { CUR = CLUSTERS.find(c => cl.has(c.id)) || CUR; NODES = genNodes(CUR); }
  $('#login').hidden = true; $('#app').hidden = false;
  buildNav(); buildTop(); paintThemeSw(); render();
  if (prev && prev !== '—') toast(`欢迎，${u.name}。上次登录：${prev}`);
}
ON.lgEye = () => { const p = $('#lgP'); p.type = p.type === 'password' ? 'text' : 'password'; };
ON.lgForgot = () => openModal({title: '忘记密码', ok: '', cancel: '知道了', body: `<ul class="small" style="padding-left:18px;margin:0;line-height:1.9"><li>请联系平台管理员在「用户管理」中重置密码，新密码将发送到你的邮箱。</li><li>使用新密码首次登录后需立即修改。</li></ul>`});

/* ---------- 个人菜单 / 退出 / 修改密码 ---------- */
ON.umenu = () => {
  const m = $('#umenu');
  m.innerHTML = `<div class="uh"><b>${ME.name}</b> <span class="small muted mono">${ME.login}</span><div class="small muted">${ME.roles.map(roleName).join(' / ')}</div><div class="small muted">本次登录：${ME.last}</div></div>
    <a data-on="uProfile">👤 个人信息与权限</a><a data-on="uPwd">🔑 修改密码</a><a href="#help/login">❔ 登录与账号说明</a>
    <div class="sep"></div><div style="padding:6px 10px"><div class="small muted" style="margin-bottom:6px">界面主题</div><div class="theme-sw" data-theme-sw="1"></div></div>
    <div class="sep"></div><a data-on="logout" style="color:var(--bad)">⎋ 退出登录</a>`;
  m.classList.toggle('open'); paintThemeSw();
};
document.addEventListener('click', e => { if (!e.target.closest('.umenu-wrap')) $('#umenu').classList.remove('open'); });
ON.uProfile = () => {
  $('#umenu').classList.remove('open');
  const ps = [...myPerms()], cl = myClusters();
  openDrawer('个人信息与权限', `<table>${[['账号', ME.login], ['姓名', ME.name], ['部门', ME.dept], ['邮箱', ME.email], ['角色', ME.roles.map(roleName).join('、')], ['可访问集群', cl ? [...cl].join('、') : '全部集群'], ['本次登录', ME.last]].map(([k, v]) => `<tr><td class="muted" style="width:110px">${k}</td><td>${esc(v)}</td></tr>`).join('')}</table>
    <h4>我拥有的权限（${ps.length} 项）</h4>${PERM_TREE.map(g => g.menus.filter(m => canView(m.id)).map(m => `<div style="margin:6px 0"><b class="small">${m.name}</b> ${m.btns.filter(b => can(b[0])).map(b => `<span class="tag" style="margin:2px">${b[1]}</span>`).join('') || '<span class="small muted">仅查看</span>'}</div>`).join('')).join('')}`);
};
ON.uPwd = () => {
  $('#umenu').classList.remove('open');
  openModal({title: '修改密码', body: `<div class="form"><label class="req">当前密码</label><input type="password" id="pw0"><label class="req">新密码</label><input type="password" id="pw1"><div class="hint">至少 ${SETTINGS.pwdMinLen} 位，须包含大写、小写字母、数字和符号；不能与最近 3 次相同</div><label class="req">确认新密码</label><input type="password" id="pw2"></div>`,
    onOk() {
      const a = $('#pw0').value, b = $('#pw1').value, c = $('#pw2').value;
      if (a !== DEMO_PWD) { toast('当前密码不正确'); return false; }
      if (b.length < SETTINGS.pwdMinLen || !/[A-Z]/.test(b) || !/[a-z]/.test(b) || !/\d/.test(b) || !/[^\w]/.test(b)) { toast('新密码不符合复杂度要求'); return false; }
      if (b !== c) { toast('两次输入的新密码不一致'); return false; }
      authAudit(ME.login, '修改密码', '成功'); toast('密码已修改（原型不会真正保存）');
    }});
};
ON.logout = () => {
  $('#umenu').classList.remove('open');
  openModal({title: '退出登录', ok: '退出', okCls: 'danger primary', body: `<div>确定退出账号 <b>${ME.login}</b> 吗？未保存的修改将丢失。</div>`, onOk() {
    authAudit(ME.login, '退出登录', '成功');
    try { sessionStorage.removeItem('sr_uid'); } catch (e) {}
    setTimeout(showLogin, 0);
  }});
};

/* 启动入口：会话内已登录则直接进入，否则显示登录页 */
function loginInit() {
  let uid = null; try { uid = sessionStorage.getItem('sr_uid'); } catch (e) {}
  const u = uid && USERS.find(x => x.id == uid && x.status === 'active');
  if (u) { ME = u; $('#login').hidden = true; buildNav(); buildTop(); paintThemeSw(); render(); }
  else showLogin();
}
