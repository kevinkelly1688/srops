// @author: kevinkelly
// @license: Apache Licence
// @file: p_system.js
// @time: 2026/10/28

/* 平台配置：用户管理 / 角色权限 / 集群接入 / 平台设置 */
const roleName = id => (ROLES.find(r => r.id === id) || {}).name || id;
const UF = {q: '', st: ''};
pages.users = {
  render() {
    const list = USERS.filter(u => (!UF.q || (u.login + u.name + u.dept).includes(UF.q)) && (!UF.st || u.status === UF.st));
    return `<div class="toolbar"><input type="search" id="ufQ" placeholder="账号 / 姓名 / 部门" value="${esc(UF.q)}"><select id="ufS"><option value="">全部状态</option><option value="active" ${UF.st === 'active' ? 'selected' : ''}>正常</option><option value="locked" ${UF.st === 'locked' ? 'selected' : ''}>已锁定</option></select>
      <span class="sp"></span>${B('user.create', '＋ 新建用户', {cls: 'primary'})}</div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>账号</th><th>姓名</th><th>部门</th><th>邮箱 / 手机</th><th>角色</th><th>状态</th><th>最近登录</th><th>操作</th></tr></thead><tbody>
    ${list.map(u => `<tr><td class="mono"><b>${u.login}</b>${u.id === ME.id ? ' <span class="tag brand">当前</span>' : ''}</td><td>${u.name}</td><td>${u.dept}</td><td class="small">${u.email}<br>${u.phone}</td><td>${u.roles.map(r => `<span class="tag">${roleName(r)}</span>`).join(' ')}</td>
      <td>${u.status === 'active' ? '<span class="tag ok">正常</span>' : '<span class="tag bad">已锁定</span>'}</td><td class="small">${u.last}</td>
      <td class="ops">${B('user.edit', '编辑', {cls: 'sm', data: {id: u.id}})}${B('user.reset', '重置密码', {cls: 'sm', data: {id: u.id}})}${u.login !== 'admin' ? B('user.lock', u.status === 'active' ? '锁定' : '解锁', {cls: 'sm', data: {id: u.id}}) + B('user.delete', '删除', {cls: 'sm danger', data: {id: u.id}}) : ''}</td></tr>`).join('')}
    </tbody></table></div><div class="small muted" style="margin-top:8px">用户的最终权限 = 所有角色权限的并集；数据范围 = 所有角色可访问集群的并集。内置账号 admin 不可锁定 / 删除。</div></div>`;
  },
  init() { const q = $('#ufQ'), s = $('#ufS'); if (q) { q.onchange = () => { UF.q = q.value; rerender(); }; s.onchange = () => { UF.st = s.value; rerender(); }; } }
};
function userForm(u = {}) {
  return `<div class="form"><label class="req">账号</label><input id="uL" value="${u.login || ''}" ${u.login ? 'disabled' : ''} placeholder="字母 / 数字 / 下划线">
  <label class="req">姓名</label><input id="uN" value="${u.name || ''}"><label>部门</label><input id="uD" value="${u.dept || ''}"><label class="req">邮箱</label><input id="uE" type="email" value="${u.email || ''}"><label>手机</label><input id="uP" value="${u.phone || ''}">
  ${u.login ? '' : `<label class="req">初始密码</label><input id="uPw" type="password" placeholder="至少 ${SETTINGS.pwdMinLen} 位，含大小写字母、数字与符号"><div class="hint">首次登录强制修改密码</div>`}
  <label class="req">角色</label><div>${ROLES.map(r => `<label class="chk" style="margin-right:12px"><input type="checkbox" class="uR" value="${r.id}" ${(u.roles || []).includes(r.id) ? 'checked' : ''} ${!can('role.edit') && r.id === 'r_admin' && !ME.roles.includes('r_admin') ? 'disabled' : ''}>${r.name}</label>`).join('')}</div>
  <label>有效期</label><input type="text" value="${u.expire || '永久'}"></div>`;
}
ACTIONS['user.create'] = () => openModal({title: '新建用户', code: 'user.create', body: userForm(), onOk() {
  const login = $('#uL').value.trim(), roles = $$('.uR').filter(x => x.checked).map(x => x.value);
  if (!/^\w{3,}$/.test(login)) { toast('账号格式不正确'); return false; } if (USERS.some(u => u.login === login)) { toast('账号已存在'); return false; } if (!roles.length) { toast('至少选择一个角色'); return false; }
  USERS.push({id: Math.max(...USERS.map(u => u.id)) + 1, login, name: $('#uN').value, dept: $('#uD').value, email: $('#uE').value, phone: $('#uP').value, roles, status: 'active', last: '—', created: now().slice(0, 10)});
  audit('user.create', `新建用户 ${login}（${roles.map(roleName).join('/')}）`); buildTop(); rerender(); }});
ACTIONS['user.edit'] = d => { const u = USERS.find(x => x.id == d.id); openModal({title: '编辑用户 ' + u.login, code: 'user.edit', body: userForm(u), onOk() {
  const roles = $$('.uR').filter(x => x.checked).map(x => x.value); if (!roles.length) { toast('至少选择一个角色'); return false; }
  const before = u.roles.map(roleName).join('/');
  Object.assign(u, {name: $('#uN').value, dept: $('#uD').value, email: $('#uE').value, phone: $('#uP').value, roles});
  audit('user.edit', `编辑用户 ${u.login}；角色 ${before} → ${roles.map(roleName).join('/')}`); buildNav(); buildTop(); rerender(); }}); };
ACTIONS['user.reset'] = d => { const u = USERS.find(x => x.id == d.id); openModal({title: '重置密码 ' + u.login, code: 'user.reset', body: `<div class="form"><label>方式</label><select><option>生成随机密码并发送邮件到 ${u.email}</option><option>手动设置</option></select><label>首次登录</label><label class="chk"><input type="checkbox" checked> 强制修改密码</label></div>`, onOk() { audit('user.reset', '重置密码 ' + u.login); toast('新密码已发送至 ' + u.email); }}); };
ACTIONS['user.lock'] = d => { const u = USERS.find(x => x.id == d.id); if (u.id === ME.id) return toast('不能锁定当前登录用户'); u.status = u.status === 'active' ? 'locked' : 'active'; audit('user.lock', (u.status === 'locked' ? '锁定' : '解锁') + '用户 ' + u.login); buildTop(); rerender(); };
ACTIONS['user.delete'] = d => { const u = USERS.find(x => x.id == d.id); if (u.id === ME.id) return toast('不能删除当前登录用户');
  openModal({title: '⚠ 删除用户 ' + u.login, okCls: 'danger primary', ok: '删除', code: 'user.delete', body: `<div class="notice bad"><span>!</span><div>删除后该账号无法登录，历史审计记录保留。建议优先使用「锁定」。</div></div>`, onOk() { USERS.splice(USERS.indexOf(u), 1); audit('user.delete', '删除用户 ' + u.login); buildTop(); rerender(); }}); };

/* ---------- 角色权限 ---------- */
const RE = {id: 'r_dba', draft: null, clusters: null, view: 'perm'};
function loadDraft() { const r = ROLES.find(x => x.id === RE.id) || ROLES[0]; RE.id = r.id; RE.draft = new Set(r.perms); RE.clusters = new Set(r.clusters); }
pages.roles = {
  render() {
    if (!RE.draft || !ROLES.some(r => r.id === RE.id)) loadDraft();
    const r = ROLES.find(x => x.id === RE.id), editable = can('role.edit') && !(r.builtin && r.id === 'r_admin');
    const dirty = [...RE.draft].sort().join() !== [...r.perms].sort().join() || [...RE.clusters].sort().join() !== [...r.clusters].sort().join();
    return `<div class="grid g-side">
      <div class="card" style="align-self:start"><h2>角色 <span class="sub">${ROLES.length}</span><span class="right">${B('role.create', '＋', {cls: 'sm', title: '新建角色'})}</span></h2>
        <div class="role-list">${ROLES.map(x => `<a data-on="roleSel" data-id="${x.id}" class="${x.id === RE.id ? 'on' : ''}">⛨ ${x.name}${x.builtin ? ' <span class="tag" style="margin-left:auto">内置</span>' : `<span class="small muted" style="margin-left:auto">${USERS.filter(u => u.roles.includes(x.id)).length} 人</span>`}</a>`).join('')}</div>
        <div class="hr"></div><a data-on="roleMatrix">▦ 查看全部角色权限矩阵</a></div>
      <div class="card">
        <h2 style="font-size:16px">${r.name} ${r.builtin ? '<span class="tag">内置</span>' : ''}<span class="sub">${r.desc}</span>
          <span class="right">${B('role.copy', '复制', {cls: 'sm', data: {id: r.id}})}${!r.builtin ? B('role.edit', '改名 / 描述', {cls: 'sm', data: {meta: r.id}}) + B('role.delete', '删除', {cls: 'sm danger', data: {id: r.id}}) : ''}</span></h2>
        ${r.id === 'r_admin' ? '<div class="notice"><span>ℹ</span><div>超级管理员固定拥有全部权限，不可修改。</div></div>' : !can('role.edit') ? '<div class="notice warn"><span>🔒</span><div>只读：需要「编辑角色权限」(role.edit) 才能修改。</div></div>' : ''}
        <div class="tabs"><a data-on="reView" data-v="perm" class="${RE.view === 'perm' ? 'on' : ''}">功能权限（菜单 + 按钮）</a><a data-on="reView" data-v="data" class="${RE.view === 'data' ? 'on' : ''}">数据范围（集群）</a><a data-on="reView" data-v="member" class="${RE.view === 'member' ? 'on' : ''}">成员 (${USERS.filter(u => u.roles.includes(r.id)).length})</a></div>
        ${RE.view === 'perm' ? this.perm(editable) : RE.view === 'data' ? this.data(editable) : this.member(r)}
        ${RE.view !== 'member' && editable ? `<div class="row" style="justify-content:flex-end;margin-top:12px;position:sticky;bottom:0;background:var(--panel);padding:8px 0">${dirty ? '<span class="small" style="color:var(--warn)">● 有未保存的修改</span>' : ''}<button class="btn" data-on="reReset" ${dirty ? '' : 'disabled'}>撤销</button>${B('role.edit', '保存', {cls: 'primary'})}</div>` : ''}
      </div></div>`;
  },
  perm(ed) {
    const D = RE.draft, dis = ed ? '' : 'disabled';
    const total = ALL_CODES().length;
    return `<div class="toolbar"><span class="small muted">已选 <b>${D.size}</b> / ${total} 项权限。勾选按钮权限会自动勾选所属菜单的「查看」；取消「查看」会同时取消该菜单下全部按钮。<span style="color:var(--bad)">●</span> 为高危操作。</span><span class="sp"></span>
      ${ed ? '<button class="btn sm" data-on="reAll" data-v="1">全选</button><button class="btn sm" data-on="reAll" data-v="0">清空</button><button class="btn sm" data-on="reAll" data-v="view">仅查看</button>' : ''}</div>
    <div class="ptree">${PERM_TREE.map(g => {
      const codes = g.menus.flatMap(m => [m.id + ':view'].concat(m.btns.map(b => b[0])));
      const n = codes.filter(c => D.has(c)).length;
      return `<div class="pg"><input type="checkbox" data-on="reGrp" data-g="${g.group}" ${n === codes.length ? 'checked' : ''} ${dis}> ${g.group} <span class="small muted" style="font-weight:400">${n}/${codes.length}</span></div>` +
        g.menus.map(m => `<div class="pm"><div><label class="chk"><input type="checkbox" data-on="reChk" data-c="${m.id}:view" ${D.has(m.id + ':view') || m.public ? 'checked' : ''} ${dis || m.public ? 'disabled' : ''}><b>${m.ico} ${m.name}</b></label><div class="small muted mono" style="margin-left:22px">${m.id}:view</div></div>
          <div class="pbtns">${m.btns.length ? m.btns.map(([c, nm, risk]) => `<label class="chk" title="${c}"><input type="checkbox" data-on="reChk" data-c="${c}" ${D.has(c) ? 'checked' : ''} ${dis}>${nm}${risk ? ' <span class="risk">●</span>' : ''} <span class="code">${c}</span></label>`).join('') : '<span class="small muted">无按钮权限（所有用户可见）</span>'}</div></div>`).join('');
    }).join('')}</div>`;
  },
  data(ed) {
    const all = RE.clusters.has('*');
    return `<div class="small muted" style="margin-bottom:10px">数据范围决定该角色可以在哪些集群上使用上面的功能权限。未授权的集群在顶栏集群切换中显示为 🔒。</div>
    <label class="chk" style="margin-bottom:10px"><input type="checkbox" data-on="reCl" data-c="*" ${all ? 'checked' : ''} ${ed ? '' : 'disabled'}><b>全部集群（含以后新接入的集群）</b></label>
    <div class="tbl-wrap"><table><thead><tr><th style="width:40px"></th><th>集群</th><th>环境</th><th>版本</th><th>架构</th></tr></thead><tbody>
    ${CLUSTERS.map(c => `<tr><td><input type="checkbox" data-on="reCl" data-c="${c.id}" ${all || RE.clusters.has(c.id) ? 'checked' : ''} ${ed && !all ? '' : 'disabled'}></td><td><b>${c.name}</b></td><td>${c.env}</td><td>${c.version}</td><td>${modeText(c.mode)}</td></tr>`).join('')}
    </tbody></table></div>`;
  },
  member(r) {
    const mem = USERS.filter(u => u.roles.includes(r.id));
    return `<div class="toolbar"><span class="small muted">在「用户管理 → 编辑用户」中为用户分配角色。</span><span class="sp"></span>${B('user.edit', '去用户管理分配', {cls: 'sm', data: {goUsers: 1}})}</div><div class="tbl-wrap"><table><thead><tr><th>账号</th><th>姓名</th><th>部门</th><th>状态</th></tr></thead><tbody>
    ${mem.map(u => `<tr><td class="mono">${u.login}</td><td>${u.name}</td><td>${u.dept}</td><td>${u.status === 'active' ? '<span class="tag ok">正常</span>' : '<span class="tag bad">锁定</span>'}</td></tr>`).join('') || '<tr><td colspan="4" class="muted">暂无成员</td></tr>'}</tbody></table></div>`;
  }
};
ON.roleSel = d => { RE.id = d.id; loadDraft(); rerender(); };
ON.reView = d => { RE.view = d.v; rerender(); };
ON.reReset = () => { loadDraft(); rerender(); };
ON.reChk = (d, el) => {
  const c = d.c, on = el.checked;
  if (on) { RE.draft.add(c); if (!c.endsWith(':view')) RE.draft.add(BTNS[c].menu + ':view'); }
  else { RE.draft.delete(c); if (c.endsWith(':view')) MENUS[c.split(':')[0]].btns.forEach(b => RE.draft.delete(b[0])); }
  rerender();
};
ON.reGrp = (d, el) => { PERM_TREE.find(g => g.group === d.g).menus.forEach(m => [m.id + ':view'].concat(m.btns.map(b => b[0])).forEach(c => el.checked ? RE.draft.add(c) : RE.draft.delete(c))); rerender(); };
ON.reAll = d => { RE.draft = d.v === '1' ? new Set(ALL_CODES()) : d.v === 'view' ? new Set(Object.keys(MENUS).map(m => m + ':view')) : new Set(); rerender(); };
ON.reCl = (d, el) => { if (d.c === '*') RE.clusters = el.checked ? new Set(['*']) : new Set(); else el.checked ? RE.clusters.add(d.c) : RE.clusters.delete(d.c); rerender(); };
ON.roleMatrix = () => {
  openDrawer('角色权限矩阵', `<div class="small muted" style="margin-bottom:8px">✔ 拥有 · 空白 无；<span style="color:var(--bad)">●</span> 高危</div><div class="tbl-wrap"><table class="matrix"><thead><tr><th>菜单 / 按钮</th>${ROLES.map(r => `<th>${r.name}</th>`).join('')}</tr></thead><tbody>
  ${PERM_TREE.map(g => `<tr><td colspan="${ROLES.length + 1}" style="background:var(--panel2);font-weight:600">${g.group}</td></tr>` + g.menus.map(m => `<tr><td><b>${m.name}</b> <span class="muted">查看</span></td>${ROLES.map(r => `<td>${m.public || r.perms.includes(m.id + ':view') ? '✔' : ''}</td>`).join('')}</tr>` + m.btns.map(([c, n, risk]) => `<tr><td style="padding-left:22px">${n}${risk ? ' <span style="color:var(--bad)">●</span>' : ''}</td>${ROLES.map(r => `<td>${r.perms.includes(c) ? '✔' : ''}</td>`).join('')}</tr>`).join('')).join('')).join('')}
  </tbody></table></div><div class="row" style="margin-top:10px"><button class="btn" data-print="1">打印 / 导出</button></div>`, true);
};
ACTIONS['role.edit'] = d => {
  const r = ROLES.find(x => x.id === (d.meta || RE.id));
  if (d.meta) return openModal({title: '编辑角色信息', code: 'role.edit', body: `<div class="form"><label class="req">名称</label><input id="rN" value="${esc(r.name)}"><label>描述</label><textarea id="rD" rows="2">${esc(r.desc)}</textarea></div>`, onOk() { r.name = $('#rN').value; r.desc = $('#rD').value; audit('role.edit', '修改角色信息 ' + r.name); rerender(); }});
  const add = [...RE.draft].filter(c => !r.perms.includes(c)), del = r.perms.filter(c => !RE.draft.has(c));
  const label = c => c.endsWith(':view') ? MENUS[c.split(':')[0]].name + '·查看' : BTNS[c].name;
  openModal({title: `保存角色「${r.name}」`, code: 'role.edit', body: `<div class="small">变更将立即对 <b>${USERS.filter(u => u.roles.includes(r.id)).length}</b> 名成员生效（已登录用户刷新后生效）。</div>
    ${add.length ? `<h4 style="color:var(--ok)">＋ 新增 ${add.length} 项</h4><div class="small">${add.map(c => `<span class="tag ok" style="margin:2px">${label(c)}</span>`).join('')}</div>` : ''}
    ${del.length ? `<h4 style="color:var(--bad)">－ 移除 ${del.length} 项</h4><div class="small">${del.map(c => `<span class="tag bad" style="margin:2px">${label(c)}</span>`).join('')}</div>` : ''}
    <h4>数据范围</h4><div class="small">${RE.clusters.has('*') ? '全部集群' : [...RE.clusters].join('、') || '<span class="up">无（该角色将无法访问任何集群）</span>'}</div>`,
    onOk() { r.perms = [...RE.draft]; r.clusters = [...RE.clusters]; audit('role.edit', `角色「${r.name}」权限变更：+${add.length} −${del.length}`, '成功', [...add.map(c => '+ ' + c), ...del.map(c => '- ' + c)].join('\n') || '—'); buildNav(); buildTop(); toast('已保存'); rerender(); }});
};
ACTIONS['role.create'] = () => openModal({title: '新建角色', code: 'role.create', body: `<div class="form"><label class="req">名称</label><input id="rN"><label>描述</label><textarea id="rD" rows="2"></textarea><label>初始权限</label><select id="rT"><option value="">空白</option>${ROLES.map(r => `<option value="${r.id}">复制「${r.name}」</option>`).join('')}</select></div>`,
  onOk() { const n = $('#rN').value.trim(); if (!n) { toast('请输入名称'); return false; } const src = ROLES.find(r => r.id === $('#rT').value);
    const r = {id: 'r_' + Date.now(), name: n, desc: $('#rD').value || '自定义角色', builtin: false, clusters: src ? src.clusters.slice() : [], perms: src ? src.perms.slice() : ['overview:view']};
    ROLES.push(r); RE.id = r.id; loadDraft(); audit('role.create', '新建角色 ' + n); rerender(); }});
ACTIONS['role.copy'] = d => { const s = ROLES.find(r => r.id === d.id); const r = {...s, id: 'r_' + Date.now(), name: s.name + ' - 副本', builtin: false, perms: s.perms.slice(), clusters: s.clusters.slice()}; ROLES.push(r); RE.id = r.id; loadDraft(); audit('role.copy', '复制角色 ' + s.name); rerender(); };
ACTIONS['role.delete'] = d => { const r = ROLES.find(x => x.id === d.id); const n = USERS.filter(u => u.roles.includes(r.id)).length;
  if (n) return openModal({title: '无法删除', ok: '', cancel: '知道了', body: `<div class="notice warn"><span>!</span><div>角色「${r.name}」仍有 ${n} 名成员，请先在用户管理中移除后再删除。</div></div>`});
  risky('role.delete', '删除角色 ' + r.name, '删除后不可恢复。', '', () => { ROLES.splice(ROLES.indexOf(r), 1); RE.id = ROLES[0].id; loadDraft(); audit('role.delete', '删除角色 ' + r.name); rerender(); }); };
ACTIONS['user.edit'] = (orig => d => d.goUsers ? go('users') : orig(d))(ACTIONS['user.edit']);

/* ---------- 集群接入 ---------- */
pages.clusters = {
  render() {
    return `<div class="toolbar"><span class="small muted">导入已有集群并填写必填信息后即可运维。平台通过 MySQL 协议（FE 9030）执行 SQL、HTTP（FE 8030 / BE 8040）读取指标与修改配置、SSH 执行启停与部署。</span><span class="sp"></span>${B('cluster.add', '＋ 导入集群', {cls: 'primary'})}</div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>集群</th><th>环境</th><th>版本</th><th>架构</th><th>FE 地址</th><th>运维账号</th><th>必填信息</th><th>可选信息</th><th>操作</th></tr></thead><tbody>
    ${CLUSTERS.map(c => { const miss = clusterMissing(c);
      return `<tr><td><b>${c.name}</b><div class="small muted">${c.desc || '—'}</div></td><td><span class="tag">${c.env}</span></td><td>${c.version}</td><td>${modeText(c.mode)}</td><td class="mono small">${c.fe}<br>${c.http}</td><td class="mono">${c.user}</td>
      <td>${miss.length ? `<span class="tag warn">待完善</span><div class="small up">缺少：${miss.join('、')}</div><div class="small muted">仅可查看，不能运维</div>` : '<span class="tag ok">完整 · 可运维</span>'}</td>
      <td class="small">${c.prom ? '✓ Prometheus' : '<span class="muted">✗ Prometheus</span>'}<br>${c.audit ? '✓ 审计表' : '<span class="muted">✗ 审计表</span>'}</td>
      <td class="ops">${miss.length ? B('cluster.edit', '完善信息', {cls: 'sm primary', data: {id: c.id, step: 3}}) : ''}${B('cluster.test', '测试', {cls: 'sm', data: {id: c.id}})}${B('cluster.edit', '编辑', {cls: 'sm', data: {id: c.id}})}${B('cluster.delete', '移除', {cls: 'sm danger', data: {id: c.id}})}</td></tr>`; }).join('') || `<tr><td colspan="9" class="muted" style="text-align:center;padding:30px">尚未导入任何集群</td></tr>`}
    </tbody></table></div></div>
    <div class="card" style="margin-top:16px"><h2>运维账号最小权限建议</h2>${code(`CREATE USER 'sr_ops'@'10.1.1.%' IDENTIFIED BY '******';\n-- 节点管理 / 参数 / 黑名单需要 SYSTEM 级权限\nGRANT NODE, OPERATE ON SYSTEM TO USER 'sr_ops'@'10.1.1.%';\nGRANT SELECT ON ALL TABLES IN ALL DATABASES TO USER 'sr_ops'@'10.1.1.%';\n-- 备份恢复\nGRANT REPOSITORY ON SYSTEM TO USER 'sr_ops'@'10.1.1.%';\n-- 资源组\nGRANT CREATE RESOURCE GROUP ON SYSTEM TO USER 'sr_ops'@'10.1.1.%';`)}${docLink('administration/user_privs/', '管理用户权限 ↗')}</div>`;
  }
};
/* 导入 / 编辑集群向导见 js/cluster_import.js */
ACTIONS['cluster.test'] = d => { const c = CLUSTERS.find(x => x.id === d.id); openModal({title: '连通性测试 · ' + c.name, ok: '', cancel: '关闭', body: [['MySQL 协议 ' + c.fe, 'ok', '12 ms'], ['FE HTTP /api/bootstrap', 'ok', '8 ms'], ['SHOW FRONTENDS / BACKENDS 权限', 'ok', '—'], ['Prometheus ' + (c.prom || '（未配置）'), c.prom ? 'ok' : 'warn', c.prom ? '35 ms' : '巡检的指标类项目将跳过'], ['SSH 到全部节点', c.ssh ? 'ok' : 'bad', c.ssh ? `${c.ssh.user}@*:${c.ssh.port} 全部成功` : '未填写 SSH 凭据，运维操作不可用']].map(([k, s, v]) => `<div class="row between" style="padding:8px 0;border-bottom:1px solid var(--border)"><span>${k}</span><span class="small muted">${v}</span>${tagOf(s)}</div>`).join('')}); audit('cluster.test', '连通性测试 ' + c.name); };
ACTIONS['cluster.delete'] = d => { const c = CLUSTERS.find(x => x.id === d.id); if (CUR && c.id === CUR.id && CLUSTERS.length > 1) return toast('不能移除当前选中的集群，请先切换'); risky('cluster.delete', '移除集群 ' + c.name, '仅从平台移除纳管关系，<b>不会</b>停止或删除 StarRocks 集群。相关巡检历史、告警规则将一并删除。', '', () => { CLUSTERS.splice(CLUSTERS.indexOf(c), 1); if (CUR === c) { CUR = CLUSTERS[0] || null; NODES = CUR ? genNodes(CUR) : []; } audit('cluster.delete', '移除集群 ' + c.name); buildTop(); rerender(); }); };

/* ---------- 平台设置 ---------- */
/* 通知设置：邮件 / 短信渠道依赖的发送服务，以及告警发送策略 */
/* 保存设置：原型直接写入本地对象；连接平台服务时由 live.js 改为调用服务端接口（返回 false 表示异步保存，保存成功后再关闭弹窗） */
let commitSetting = (kind, v, target) => {
  Object.assign(target, v);
  if (kind === 'basic') applyBasic();
  audit('setting.' + kind, '修改' + ({security: '安全策略', notify: '通知设置', basic: '基础设置', watermark: '页面水印'})[kind]);
  toast('已保存'); rerender(); return true;
};
const NOTIFY_CFG = {smtpSkipVerify: false, smtpHost: 'smtp.example.com', smtpPort: 465, smtpSec: 'SSL', smtpUser: 'sr-ops@example.com', smtpPwd: true, from: 'sr-ops@example.com', fromName: 'StarRocks 运维平台',
  smsUrl: 'https://sms.example.com/api/send', smsSign: '【数据平台】', smsProvider: 'http', smsKeyId: '', smsSecretSet: false, smsTemplate: '', smsAppId: '', smsRegion: '', smsParams: 'cluster,rule,level', mergeMin: 5, recover: true, repeatMin: 60, quiet: false, quietFrom: '23:00', quietTo: '08:00'};
/* 基础设置 */
const BASIC_CFG = {name: 'StarRocks 运维平台', url: 'https://sr-ops.example.com', proxy: 'http://proxy.example.com:3128', noProxy: '10.0.0.0/8, *.example.com', interval: 15, reportDays: 90, auditDays: 180};
pages.settings = {
  render(sub) {
    sub = sub || 'security';
    const S = SETTINGS, row = (k, v) => `<div class="row between" style="padding:8px 0;border-bottom:1px solid var(--border)"><span class="muted">${k}</span><span>${v}</span></div>`;
    const body = {
      security: `<div class="card"><h2>安全策略 <span class="right">${B('setting.security', '编辑', {cls: 'sm'})}</span></h2>
        ${row('无权限按钮显示方式', S.noPermBtn === 'hide' ? '隐藏' : '置灰并显示 🔒（推荐，便于用户知道需要申请什么权限）')}${row('高危操作二次确认（输入集群名）', S.highRiskConfirm ? '<span class="tag ok">开启</span>' : '<span class="tag">关闭</span>')}
        ${row('高危操作双人复核', S.dualReview ? '<span class="tag ok">开启</span>' : '<span class="tag">关闭</span>')}${row('会话超时', S.sessionTimeout + ' 分钟无操作自动登出')}${row('密码策略', `≥ ${S.pwdMinLen} 位，含大小写 / 数字 / 符号；${S.pwdExpireDays ? S.pwdExpireDays + ' 天过期' : '永不过期'}`)}
        ${row('登录失败锁定', `连续 ${S.loginFailLock} 次失败锁定 30 分钟`)}${row('访问 IP 白名单', S.ipWhitelist ? `<span class="mono">${esc(S.ipWhitelist)}</span>` : '<span class="muted">不限制</span>')}</div>`,
      notify: (N => `<div class="grid g2"><div class="card"><h2>邮件服务（SMTP） <span class="right">${B('setting.notify', '编辑', {cls: 'sm'})}</span></h2>${row('邮件服务器', `<span class="mono">${esc(N.smtpHost)}:${N.smtpPort}</span>（${N.smtpSec === 'none' ? '不加密' : N.smtpSec}）`)}${row('认证账号', N.smtpUser ? esc(N.smtpUser) + (N.smtpPwd ? ' · 密码已设置' : '') : '<span class="muted">匿名</span>')}${row('发件人', `${esc(N.fromName)} &lt;${esc(N.from)}&gt;`)}
        <h2 style="margin-top:16px">短信网关</h2>${row('服务商', ({aliyun: '阿里云短信', tencent: '腾讯云短信'})[N.smsProvider] || '通用 HTTP 网关')}${row(N.smsProvider === 'aliyun' || N.smsProvider === 'tencent' ? '接入点' : '网关地址', N.smsUrl ? `<span class="mono">${esc(N.smsUrl)}</span>` : (N.smsProvider === 'aliyun' || N.smsProvider === 'tencent' ? '<span class="muted">官方默认地址</span>' : '<span class="muted">未配置</span>'))}${N.smsProvider === 'aliyun' || N.smsProvider === 'tencent' ? row('AccessKey', N.smsKeyId ? `<span class="mono">${esc(N.smsKeyId)}</span>${N.smsSecretSet ? ' · 密钥已设置' : ' · <span class="up">密钥未设置</span>'}` : '<span class="muted">未配置</span>') + row('模板', N.smsTemplate ? `<span class="mono">${esc(N.smsTemplate)}</span>（变量：${esc(N.smsParams || 'cluster,rule,level')}）` : '<span class="muted">未配置</span>') : row('鉴权密钥', N.smsSecretSet ? '已设置' : '<span class="muted">无</span>')}${row('短信签名', esc(N.smsSign) || '—')}</div>
        <div class="card"><h2>发送策略 <span class="right">${B('setting.notify', '编辑', {cls: 'sm'})}</span></h2>${row('告警合并', N.mergeMin ? `同一规则 ${N.mergeMin} 分钟内合并发送` : '不合并')}${row('重复提醒', N.repeatMin ? `未恢复的告警每 ${N.repeatMin} 分钟重复通知` : '不重复')}${row('恢复通知', N.recover ? '<span class="tag ok">开启</span>' : '<span class="tag">关闭</span>')}
        ${row('免打扰时段', N.quiet ? `${N.quietFrom} – ${N.quietTo}（仅发送“严重”级别）` : '<span class="tag">关闭</span>')}${row('通知渠道', `${CHANNELS.length} 个（在「告警中心 → 通知渠道」管理）`)}
        <div class="small muted" style="margin-top:8px">合并、重复提醒、恢复通知、免打扰供告警中心的通知渠道使用（连接平台服务时立即生效；严重级别在免打扰时段照常发送）。短信网关供告警中心的“短信”渠道发送；邮件服务（SMTP）用于「告警通知 → 邮件」「定时巡检结果通知 → 邮件」与用户账号邮件。</div></div></div>`)(NOTIFY_CFG),
      basic: (G => `<div class="card"><h2>基础设置 <span class="right">${B('setting.basic', '编辑', {cls: 'sm'})}</span></h2>${row('平台名称', esc(G.name))}${row('平台访问地址', G.url ? `<span class="mono">${esc(G.url)}</span>（通知消息中的“查看详情”链接）` : '<span class="muted">未设置（通知消息不带链接）</span>')}${row('出网 HTTP 代理', G.proxy ? `<span class="mono">${esc(G.proxy)}</span>（用于 Lark / Slack 等境外通知渠道）` : '<span class="muted">不使用代理</span>')}${row('不走代理的地址', G.noProxy ? `<span class="mono">${esc(G.noProxy)}</span>` : '—')}${row('指标采集间隔', G.interval + ' 秒')}${row('巡检报告保留', G.reportDays + ' 天')}${row('审计日志保留', G.auditDays + ' 天（不少于 180 天，期间不可删除）')}${row('平台版本', (typeof LIVE !== 'undefined' && LIVE.version ? 'v' + String(LIVE.version).replace(/^v/, '') + ' · ' : '') + '<a href="#help/changelog">更新记录</a>')}</div>`)(BASIC_CFG),
    };
    body.watermark = wmSettingsCard();
    return tabs('settings', [['security', '安全策略'], ['notify', '通知'], ['basic', '基础'], ['watermark', '水印']], sub) + body[sub];
  }
};
ACTIONS['setting.security'] = () => { const S = SETTINGS; openModal({title: '安全策略', code: 'setting.security', body: `<div class="form">
  <label>无权限按钮</label><select id="sNp"><option value="disable" ${S.noPermBtn === 'disable' ? 'selected' : ''}>置灰并显示 🔒</option><option value="hide" ${S.noPermBtn === 'hide' ? 'selected' : ''}>隐藏</option></select>
  <label>高危二次确认</label><label class="chk"><input type="checkbox" id="sHr" ${S.highRiskConfirm ? 'checked' : ''}> 执行前输入集群名</label>
  <label>双人复核</label><label class="chk"><input type="checkbox" id="sDr" ${S.dualReview ? 'checked' : ''}> 高危操作需另一人批准</label>
  <label>会话超时(分钟)</label><input type="number" id="sSt" value="${S.sessionTimeout}"><label>密码最小长度</label><input type="number" id="sPl" value="${S.pwdMinLen}"><label>密码过期(天)</label><input type="number" id="sPe" min="0" value="${S.pwdExpireDays}"><label>失败锁定次数</label><input type="number" id="sLf" value="${S.loginFailLock}"><label>IP 白名单</label><input id="sIp" class="mono" value="${esc(S.ipWhitelist)}" placeholder="10.0.0.0/8, 192.168.1.5（留空不限制）"><label></label><div class="small muted">密码过期天数 0 = 永不过期。白名单必须包含您当前的来源 IP；本机（127.0.0.1）始终可访问，可作为误锁后的恢复途径。使用反向代理时，需在服务启动参数 -trusted-proxies 中配置代理地址，平台才能识别真实来源 IP。</div></div>`,
  onOk() {
    const v = {noPermBtn: $('#sNp').value, highRiskConfirm: $('#sHr').checked, dualReview: $('#sDr').checked, sessionTimeout: +$('#sSt').value, pwdMinLen: +$('#sPl').value, pwdExpireDays: +$('#sPe').value, loginFailLock: +$('#sLf').value, ipWhitelist: $('#sIp').value.trim()};
    if (!(v.sessionTimeout >= 5 && v.sessionTimeout <= 480)) { toast('会话超时须为 5–480 分钟'); return false; }
    if (!(v.pwdMinLen >= 8 && v.pwdMinLen <= 32)) { toast('密码最小长度须为 8–32 位'); return false; }
    if (!(v.pwdExpireDays >= 0 && v.pwdExpireDays <= 3650)) { toast('密码过期天数须为 0–3650'); return false; }
    if (!(v.loginFailLock >= 3 && v.loginFailLock <= 20)) { toast('失败锁定次数须为 3–20'); return false; }
    return commitSetting('security', v, S);
  }}); };
ACTIONS['setting.notify'] = () => { const N = NOTIFY_CFG; openModal({title: '通知设置', code: 'setting.notify', wide: true, body: `<div class="form">
  <label></label><b>邮件服务（SMTP）</b>
  <label class="req">邮件服务器</label><div class="row"><input id="nH" class="mono" value="${esc(N.smtpHost)}" placeholder="smtp.example.com" style="flex:1"><input id="nP" type="number" min="1" max="65535" value="${N.smtpPort}" style="width:90px">
    <select id="nS" style="width:auto">${[['SSL', 'SSL/TLS'], ['STARTTLS', 'STARTTLS'], ['none', '不加密']].map(([v, t]) => `<option value="${v}" ${N.smtpSec === v ? 'selected' : ''}>${t}</option>`).join('')}</select></div>
  <label></label><label class="chk"><input type="checkbox" id="nV" ${N.smtpSkipVerify ? 'checked' : ''}> 不校验服务器证书（仅用于使用自签名证书的内网邮件服务器）</label>
  <label>认证账号</label><input id="nU" value="${esc(N.smtpUser)}" placeholder="留空为匿名发送">
  <label>认证密码</label><input id="nW" type="password" autocomplete="new-password" placeholder="${N.smtpPwd ? '已设置（留空保持不变）' : '授权码 / 密码'}">
  <label class="req">发件人地址</label><input id="nF" value="${esc(N.from)}"><label>发件人名称</label><input id="nN" value="${esc(N.fromName)}">
  <label></label><div class="row"><button class="btn sm" data-on="nTestMail">发送测试邮件</button><span class="small muted">发送到当前用户邮箱 ${esc(ME.email)}</span></div>
  <label></label><b>短信网关</b>
  <label>服务商</label><select id="nGP">${[['http', '通用 HTTP 短信网关'], ['aliyun', '阿里云短信'], ['tencent', '腾讯云短信']].map(([v, t]) => `<option value="${v}" ${(N.smsProvider || 'http') === v ? 'selected' : ''}>${t}</option>`).join('')}</select>
  <label id="nGUL">网关地址</label><input id="nG" class="mono" value="${esc(N.smsUrl)}" placeholder="https://sms.example.com/api/send（留空则「短信」渠道不可用）">
  <label class="nGc">AccessKeyId</label><input id="nGK" class="nGc mono" value="${esc(N.smsKeyId)}" placeholder="阿里云 AccessKeyId / 腾讯云 SecretId">
  <label class="nGc nGh">鉴权密钥</label><input id="nGS" class="nGc" type="password" autocomplete="new-password" placeholder="${N.smsSecretSet ? '已设置（留空保持不变）' : 'AccessKeySecret / SecretKey / 网关 Token'}">
  <label>短信签名</label><input id="nG2" value="${esc(N.smsSign)}" placeholder="【公司名】">
  <label class="nGc">模板 CODE / ID</label><input id="nGT" class="nGc mono" value="${esc(N.smsTemplate)}" placeholder="阿里云如 SMS_123456789；腾讯云为数字模板 ID">
  <label class="nGt">SdkAppId</label><input id="nGA" class="nGt mono" value="${esc(N.smsAppId)}" placeholder="腾讯云短信应用的 SdkAppId">
  <label class="nGt">地域</label><input id="nGR" class="nGt mono" value="${esc(N.smsRegion)}" placeholder="默认 ap-guangzhou">
  <label>模板变量</label><input id="nGV" class="mono" value="${esc(N.smsParams || 'cluster,rule,level')}" placeholder="cluster,rule,level"><label></label><div class="small muted">按模板里变量出现的顺序填写，可用：level 级别、cluster 集群、rule 规则、inst 对象、value 取值、count 对象数、time 时间、status 状态；每个值最多 20 个字。</div>
  <label>发送测试短信</label><div class="row"><input id="nGPh" class="mono" placeholder="手机号，最多 3 个，逗号分隔" style="flex:1"><button class="btn sm" data-on="nTestSms">发送</button></div>
  <label></label><b>发送策略</b>
  <label>告警合并</label><div class="row small">同一规则 <input id="nM" type="number" min="0" max="60" value="${N.mergeMin}" style="width:70px"> 分钟内合并为一条（0 = 不合并）</div>
  <label>重复提醒</label><div class="row small">未恢复的告警每 <input id="nR" type="number" min="0" max="1440" value="${N.repeatMin}" style="width:80px"> 分钟重复通知（0 = 不重复）</div>
  <label>恢复通知</label><label class="chk"><input type="checkbox" id="nO" ${N.recover ? 'checked' : ''}> 告警恢复时发送通知</label>
  <label>免打扰时段</label><div class="row small"><label class="chk"><input type="checkbox" id="nQ" ${N.quiet ? 'checked' : ''}> 开启</label><input id="nQ1" type="time" value="${N.quietFrom}" style="width:110px"> – <input id="nQ2" type="time" value="${N.quietTo}" style="width:110px"><span class="muted">期间只发送“严重”级别</span></div></div>`,
  onOk() {
    const v = {smtpHost: $('#nH').value.trim(), smtpPort: +$('#nP').value, smtpSec: $('#nS').value, smtpUser: $('#nU').value.trim(), from: $('#nF').value.trim(), fromName: $('#nN').value.trim(),
      smsUrl: $('#nG').value.trim(), smsSign: $('#nG2').value.trim(), smsProvider: $('#nGP').value, smsKeyId: $('#nGK').value.trim(), smsTemplate: $('#nGT').value.trim(), smsAppId: $('#nGA').value.trim(), smsRegion: $('#nGR').value.trim(), smsParams: $('#nGV').value.trim(), mergeMin: +$('#nM').value, repeatMin: +$('#nR').value, recover: $('#nO').checked, quiet: $('#nQ').checked, quietFrom: $('#nQ1').value, quietTo: $('#nQ2').value};
    if (!/^[A-Za-z0-9.-]+$/.test(v.smtpHost)) { toast('邮件服务器地址格式不正确'); return false; }
    if (!(v.smtpPort >= 1 && v.smtpPort <= 65535)) { toast('端口须为 1–65535'); return false; }
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(v.from)) { toast('发件人地址格式不正确'); return false; }
    if (v.smsUrl && !/^https?:\/\/\S+$/.test(v.smsUrl)) { toast('短信网关须为 http(s):// 开头的地址'); return false; }
    if (!(v.mergeMin >= 0 && v.mergeMin <= 60) || !(v.repeatMin >= 0 && v.repeatMin <= 1440)) { toast('合并 / 重复间隔超出范围'); return false; }
    if (v.quiet && (!v.quietFrom || !v.quietTo || v.quietFrom === v.quietTo)) { toast('请设置有效的免打扰起止时间'); return false; }
    v.smtpSkipVerify = $('#nV').checked;
    v.smtpPwd = v.smtpUser ? (N.smtpPwd || !!$('#nW').value) : false;
    v.smsSecretSet = N.smsSecretSet || !!$('#nGS').value;
    return commitSetting('notify', v, N, {smtpPassword: $('#nW').value, smsSecret: $('#nGS').value});
  }});
  const syncSms = () => { const p = $('#nGP').value, cloud = p !== 'http'; $$('.nGc').forEach(x => x.style.display = ''); $$('.nGt').forEach(x => x.style.display = p === 'tencent' ? '' : 'none');
    $$('.nGh').forEach(x => x.textContent = cloud ? '密钥' : '鉴权 Token'); $('#nGUL').textContent = cloud ? '接入点（可选）' : '网关地址';
    $('#nG').placeholder = cloud ? '留空使用官方地址' : 'https://sms.example.com/api/send（留空则「短信」渠道不可用）'; };
  $('#nGP').onchange = syncSms; syncSms(); };
ON.nTestSms = () => { const ph = $('#nGPh').value.trim(); if (!ph) return toast('请先填写接收测试短信的手机号'); audit('setting.notify', '发送测试短信'); toast('测试短信已发送（模拟）'); };
ON.nTestMail = () => { const h = $('#nH').value.trim(); if (!h) return toast('请先填写邮件服务器'); audit('setting.notify', '发送测试邮件到 ' + ME.email); toast(`测试邮件已通过 ${h} 发送到 ${ME.email}（模拟）`); };
ACTIONS['setting.basic'] = () => { const G = BASIC_CFG; openModal({title: '基础设置', code: 'setting.basic', body: `<div class="form">
  <label class="req">平台名称</label><input id="gN" maxlength="30" value="${esc(G.name)}"><label></label><div class="small muted">显示在浏览器标签页与顶栏</div>
  <label>平台访问地址</label><input id="gU" class="mono" value="${esc(G.url)}" placeholder="https://sr-ops.example.com"><label></label><div class="small muted">用户实际访问平台的地址（反向代理后的地址），用于通知消息中的“查看详情”链接</div>
  <label>出网 HTTP 代理</label><input id="gP" class="mono" value="${esc(G.proxy)}" placeholder="http://proxy.example.com:3128（留空不使用）">
  <label>不走代理的地址</label><input id="gX" class="mono" value="${esc(G.noProxy)}" placeholder="10.0.0.0/8, *.example.com">
  <label>指标采集间隔</label><select id="gI">${[15, 30, 60].map(x => `<option value="${x}" ${G.interval === x ? 'selected' : ''}>${x} 秒</option>`).join('')}</select>
  <label>巡检报告保留</label><div class="row small"><input id="gR" type="number" min="7" max="730" value="${G.reportDays}" style="width:90px"> 天（7–730）</div>
  <label>审计日志保留</label><div class="row small"><input id="gA" type="number" min="180" max="3650" value="${G.auditDays}" style="width:90px"> 天（不少于 180 天，等保要求）</div></div>`,
  onOk() {
    const v = {name: $('#gN').value.trim(), url: $('#gU').value.trim().replace(/\/+$/, ''), proxy: $('#gP').value.trim(), noProxy: $('#gX').value.trim(), interval: +$('#gI').value, reportDays: +$('#gR').value, auditDays: +$('#gA').value};
    if (!v.name) { toast('请填写平台名称'); return false; }
    if (v.url && !/^https?:\/\/[^\s/]+/.test(v.url)) { toast('平台访问地址须为 http(s):// 开头'); return false; }
    if (v.proxy && !/^(https?|socks5):\/\/[^\s/]+:\d+$/.test(v.proxy)) { toast('代理格式为 http://主机:端口'); return false; }
    if (!(v.reportDays >= 7 && v.reportDays <= 730)) { toast('巡检报告保留须为 7–730 天'); return false; }
    if (!(v.auditDays >= 180 && v.auditDays <= 3650)) { toast('审计日志保留不能少于 180 天'); return false; }
    return commitSetting('basic', v, G);
  }}); };
/* 平台名称显示在标签页与顶栏（默认名称时顶栏保持 StarRocks Ops） */
function applyBasic() { document.title = BASIC_CFG.name; const l = $('.logo > div'); if (l) l.firstChild.nodeValue = BASIC_CFG.name === 'StarRocks 运维平台' ? 'StarRocks Ops' : BASIC_CFG.name; }
