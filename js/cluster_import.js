// @author: kevinkelly
// @license: Apache Licence
// @file: cluster_import.js
// @time: 2026/10/28

/* 集群导入向导 / 未导入集群时的引导页 / 原型“全新部署”演示模式
   平台独立部署后为空：导入已有集群并填写必填信息（FE 连接 + SSH 凭据 + 部署目录）后才能执行运维操作。
   FE 连接在导入时必须测试通过；SSH 与部署目录可“稍后填写”，此时集群只读（见 perm.js B() 与 core.js render()）。 */
const CI_STEPS = ['基本信息', 'FE 连接', 'SSH 与部署目录', '监控与审计', '确认导入'];
const CI = {step: 1, edit: null, f: {}, det: null, sshOk: false};

function ciDefaults(c) {
  const h = c ? c.fe.split(':')[0] : '';
  return c ? {name: c.name, env: c.env, owner: c.owner || ME.login, desc: c.desc || '', hosts: h, qport: c.fe.split(':')[1] || 9030, hport: (c.http || '').split(':')[1] || 8030, user: c.user, pwd: '********',
    sshUser: c.ssh ? c.ssh.user : 'starrocks', sshPort: c.ssh ? c.ssh.port : 22, sshAuth: c.ssh && c.ssh.auth === 'password' ? '密码（仅首次分发平台公钥）' : '平台密钥', sshPwd: c.ssh && c.ssh.auth === 'password' ? '********' : '', promSel: c.promSelector || '', sudo: c.ssh ? c.ssh.sudo : true, sshLater: !c.ssh,
    feDir: c.dirs ? c.dirs.fe : '/opt/starrocks/fe', beDir: c.dirs ? c.dirs.be : '/opt/starrocks/be', prom: c.prom || '', audit: !!c.audit, channel: ''}
    : {name: '', env: '生产', owner: ME.login, desc: '', hosts: '10.50.1.11, 10.50.1.12, 10.50.1.13', qport: 9030, hport: 8030, user: 'sr_ops', pwd: '',
    sshUser: 'starrocks', sshPort: 22, sshAuth: '平台密钥', sshPwd: '', promSel: '', sudo: true, sshLater: false, feDir: '/opt/starrocks/fe', beDir: '/opt/starrocks/be', prom: '', audit: true, channel: ''};
}
function ciOpen(c, step) {
  CI.sshHosts = null;
  CI.edit = c ? c.id : null; CI.f = ciDefaults(c); CI.step = step || 1; CI.sshOk = !!(c && c.ssh);
  CI.det = c ? {version: c.version, mode: c.mode, nodes: genNodes(c), grants: ciGrants()} : null;
  ciPaint();
}
const ciGrants = () => [['SELECT ON information_schema / 业务库', true, '查看集群、巡检、查询分析'], ['NODE ON SYSTEM', true, '扩缩容、黑名单、节点管理'], ['OPERATE ON SYSTEM', true, '参数修改、KILL 查询、Compaction'],
  ['CREATE RESOURCE GROUP ON SYSTEM', true, '资源组管理'], ['REPOSITORY ON SYSTEM', false, '备份恢复（缺少时该功能不可用）']];

/* 读取当前步骤表单 */
function ciSave() {
  const v = id => { const e = $('#ci_' + id); return e ? (e.type === 'checkbox' ? e.checked : e.value.trim()) : undefined; };
  Object.keys(CI.f).forEach(k => { const x = v(k); if (x !== undefined) CI.f[k] = x; });
}
function ciPaint() {
  const f = CI.f, st = CI.step, m = $('#modal'), edit = CI.edit;
  const inp = (id, label, req, attrs = '') => `<label class="${req ? 'req' : ''}">${label}</label><input id="ci_${id}" value="${esc(f[id])}" ${attrs}>`;
  let body = '';
  if (st === 1) body = `<div class="form">${inp('name', '集群名称', 1, `placeholder="平台内唯一，如 prod-sr-02" ${edit ? 'disabled' : ''}`)}
      <label class="req">环境</label><select id="ci_env">${['生产', '预发', '测试', '开发'].map(e => `<option ${e === f.env ? 'selected' : ''}>${e}</option>`).join('')}</select>
      <label class="req">负责人</label><select id="ci_owner">${USERS.map(u => `<option value="${u.login}" ${u.login === f.owner ? 'selected' : ''}>${u.name}（${u.login}）</option>`).join('')}</select>
      ${inp('desc', '描述', 0, 'placeholder="业务用途，可选"')}</div>
      <div class="notice" style="margin-top:12px"><span>ℹ</span><div>导入只建立纳管关系，<b>不会</b>修改集群。必填信息：FE 连接（第 2 步）、SSH 与部署目录（第 3 步，运维操作必需）；Prometheus 与审计表为可选。</div></div>`;
  if (st === 2) body = `<div class="form">${inp('hosts', 'FE 地址', 1, 'class="mono" placeholder="多个用逗号分隔；建议填负载均衡地址"')}
      ${inp('qport', 'query_port', 1, 'type="number"')}${inp('hport', 'http_port', 1, 'type="number"')}
      ${inp('user', '运维账号', 1, 'class="mono"')}<label class="req">密码</label><input id="ci_pwd" type="password" value="${esc(f.pwd)}" placeholder="平台加密保存（AES-256）">
      <div class="hint">建议使用专用运维账号（见「集群接入」页的最小权限建议），不要使用 root。</div></div>
      <div class="row" style="margin:12px 0"><button class="btn primary" data-on="ciTest">⚡ 测试连接并识别集群</button><span class="small muted">执行 SELECT current_version()、SHOW FRONTENDS / BACKENDS / COMPUTE NODES、SHOW GRANTS</span></div>
      ${CI.det ? ciDetHtml() : '<div class="muted small">测试通过后才能进入下一步。</div>'}`;
  if (st === 3) {
    const hosts = [...new Set(CI.det.nodes.map(n => n.host))];
    body = `<div class="notice warn" style="margin-bottom:12px"><span>🔑</span><div><b>运维必填</b>：节点启停、扩缩容、升级、日志查看、系统环境巡检都通过 SSH 执行，需要部署目录定位 <code>bin/start_*.sh</code>。</div></div>
      <label class="chk" style="margin-bottom:12px"><input type="checkbox" id="ci_sshLater" data-on="ciLater" ${f.sshLater ? 'checked' : ''}> 稍后填写（导入后集群<b>只能查看</b>，不能执行运维操作）</label>
      <div class="form" style="${f.sshLater ? 'opacity:.4;pointer-events:none' : ''}">${inp('sshUser', 'SSH 用户', 1, 'class="mono"')}${inp('sshPort', 'SSH 端口', 1, 'type="number"')}
        <label class="req">认证方式</label><select id="ci_sshAuth">${['平台密钥', '密码（仅首次分发平台公钥）'].map(a => `<option ${a === f.sshAuth ? 'selected' : ''}>${a}</option>`).join('')}</select>
        ${f.sshAuth === '平台密钥' ? `<div class="hint" style="grid-column:1/-1">把平台公钥加入各主机 ~${esc(f.sshUser)}/.ssh/authorized_keys：<code style="word-break:break-all">${esc(CI.pubkey || 'ssh-ed25519 AAAA…（平台首次启动时生成） sr-ops')}</code></div>` : `<label class="req">SSH 密码</label><input id="ci_sshPwd" type="password" value="${esc(f.sshPwd)}" placeholder="平台加密保存">`}
        <label>sudo</label><label class="chk"><input type="checkbox" id="ci_sudo" ${f.sudo ? 'checked' : ''}> 免密 sudo（修改内核参数、一键修复时需要）</label>
        ${inp('feDir', 'FE 部署目录', 1, 'class="mono"')}${inp('beDir', CI.det.mode === 'shared-data' ? 'CN 部署目录' : 'BE / CN 部署目录', 1, 'class="mono"')}
        <div class="hint">目录下应有 bin/start_fe.sh、bin/start_be.sh（或 start_cn.sh）与 conf/</div></div>
      ${f.sshLater ? '' : `<div class="row" style="margin:12px 0"><button class="btn primary" data-on="ciSsh">⚡ 测试 SSH（${hosts.length} 台主机）</button></div>
      ${CI.sshHosts ? ciSshTable() : CI.sshOk ? `<div class="tbl-wrap"><table><thead><tr><th>主机</th><th>角色</th><th>SSH</th><th>sudo</th><th>部署目录</th></tr></thead><tbody>${hosts.map(h => { const ns = CI.det.nodes.filter(n => n.host === h); return `<tr><td class="mono">${h}</td><td>${ns.map(n => n.type.toUpperCase()).join(' / ')}</td><td><span class="tag ok">已连通</span></td><td>${f.sudo ? '<span class="tag ok">可用</span>' : '<span class="tag">未启用</span>'}</td><td class="mono small">${ns[0].type === 'fe' ? f.feDir : f.beDir} ✓</td></tr>`; }).join('')}</tbody></table></div>` : ''}`}`;
  }
  if (st === 4) body = `<div class="form">${inp('prom', 'Prometheus', 0, 'class="mono" placeholder="http://prometheus:9090（可选）"')}
      <div class="hint">未配置时：总览趋势改用平台内置采样（最近 60 分钟），指标类告警不可用。</div>
      ${inp('promSel', '标签过滤', 0, 'class="mono" placeholder=\'可选，如 job="StarRocks_prod"（多集群共用 Prometheus 时填写）\'')}
      <label>审计表</label><label class="chk"><input type="checkbox" id="ci_audit" ${f.audit ? 'checked' : ''}> 已安装 AuditLoader（starrocks_audit_db__.starrocks_audit_tbl__）</label>
      <div class="hint">未安装时：慢查询 Top SQL、SQL 审计不可用。${docLink('administration/management/audit_loader/', 'AuditLoader ↗')}</div>
      <label>默认告警渠道</label><select id="ci_channel"><option value="">暂不设置</option>${CHANNELS.filter(c => c.on).map(c => `<option ${c.name === f.channel ? 'selected' : ''}>${c.name}</option>`).join('')}</select></div>`;
  if (st === 5) body = `<div class="grid g2" style="align-items:start"><div><h4 style="margin-top:0">集群</h4><table>${[['名称', f.name], ['环境', f.env], ['版本 / 架构', `${CI.det.version} · ${modeText(CI.det.mode)}`], ['节点', ciNodeSum()], ['FE 连接', `${f.user}@${f.hosts.split(',')[0].trim()}:${f.qport}`], ['SSH', f.sshLater ? '<span class="up">稍后填写</span>' : `${f.sshUser}@*:${f.sshPort}（${f.sshAuth}）`], ['部署目录', f.sshLater ? '—' : `${f.feDir}<br>${f.beDir}`], ['Prometheus', f.prom || '未配置'], ['审计表', f.audit ? '已安装' : '未安装']].map(([k, v]) => `<tr><td class="muted" style="width:90px">${k}</td><td class="small">${v}</td></tr>`).join('')}</table></div>
      <div><h4 style="margin-top:0">导入后可用功能</h4>${ciCaps().map(([n, ok, why]) => `<div class="row between small" style="padding:5px 0;border-bottom:1px solid var(--border)"><span>${n}</span><span>${ok === true ? '<span class="tag ok">可用</span>' : ok === 'part' ? '<span class="tag warn">部分</span>' : '<span class="tag bad">不可用</span>'}${ok === true ? '' : `<div class="muted" style="font-size:11px;text-align:right">${why}</div>`}</span></div>`).join('')}</div></div>
      ${f.sshLater ? '<div class="notice warn" style="margin-top:12px"><span>🔒</span><div>未填写 SSH 与部署目录：导入后集群为<b>待完善</b>状态，只能查看，所有运维按钮置灰。可随时在「集群接入 → 完善信息」补全。</div></div>' : '<div class="notice ok" style="margin-top:12px"><span>✓</span><div>必填信息完整，导入后即可对集群进行运维操作。</div></div>'}`;
  m.className = 'modal wide';
  m.innerHTML = `<header><h3>${edit ? (CI.step === 3 && clusterMissing(CLUSTERS.find(c => c.id === edit)).length ? '完善集群信息 · ' : '编辑集群 · ') + esc(edit) : '导入集群'}</h3><button class="btn sm" data-mclose="1">✕</button></header>
    <div class="mbody"><div class="steps">${CI_STEPS.map((s, i) => `<div class="st ${i + 1 < st ? 'done' : i + 1 === st ? 'on' : ''}"><b>${i + 1 < st ? '✓' : i + 1}</b>${s}</div>`).join('')}</div>${body}</div>
    <footer><span class="perm-code">权限码：${edit ? 'cluster.edit' : 'cluster.add'}</span><button class="btn" data-mclose="1">取消</button>
      ${st > 1 ? '<button class="btn" data-on="ciPrev">上一步</button>' : ''}${st < 5 ? '<button class="btn primary" data-on="ciNext">下一步</button>' : `<button class="btn primary" data-on="ciDone">${edit ? '保存' : '完成导入'}</button>`}</footer>`;
  MODAL_OK = null; $('#mask').classList.add('open');
}
// 服务端返回的逐台 SSH 测试结果
const ciSshTable = () => `${CI.sshOk ? '' : '<div class="notice bad" style="margin-top:8px"><span>!</span><div>部分主机未通过，修正后重新测试；也可勾选“稍后填写”。</div></div>'}
  <div class="tbl-wrap"><table><thead><tr><th>主机</th><th>角色</th><th>SSH</th><th>sudo</th><th>部署目录</th></tr></thead><tbody>${CI.sshHosts.map(h => `<tr><td class="mono">${esc(h.host)}</td><td>${esc(h.roles)}</td>
  <td>${h.ssh === '已连通' ? '<span class="tag ok">已连通</span>' : `<span class="tag bad">失败</span><div class="small up">${esc(h.ssh)}</div>`}</td><td class="small">${esc(h.sudo || '—')}</td><td class="small ${h.dir === '✓' ? '' : 'up'}">${esc(h.dir || '—')}</td></tr>`).join('')}</tbody></table></div>`;
const ciNodeSum = () => ['fe', 'be', 'cn'].map(t => [t, CI.det.nodes.filter(n => n.type === t).length]).filter(x => x[1]).map(([t, n]) => `${t.toUpperCase()} ${n}`).join(' · ');
function ciDetHtml() {
  const d = CI.det;
  return `<div class="notice ok"><span>✓</span><div>连接成功：StarRocks <b>${d.version}</b> · ${modeText(d.mode)} · ${ciNodeSum()}</div></div>
    <div class="grid g2" style="align-items:start"><div class="tbl-wrap"><table><thead><tr><th>节点</th><th>角色</th><th>状态</th></tr></thead><tbody>${d.nodes.map(n => `<tr><td class="mono small">${n.host}</td><td class="small">${n.type.toUpperCase()}${n.role ? ' · ' + n.role : ''}</td><td>${stDot(n.status === 'warn' ? 'up' : n.status)}</td></tr>`).join('')}</tbody></table></div>
    <div><div class="small b" style="margin-bottom:4px">运维账号权限检查（SHOW GRANTS）</div>${d.grants.map(([g, ok, use]) => `<div class="row between small" style="padding:4px 0;border-bottom:1px solid var(--border)"><span class="mono">${g}</span>${ok ? '<span class="tag ok">✓</span>' : `<span class="tag warn">缺少</span>`}</div><div class="muted" style="font-size:11px">${use}</div>`).join('')}
    ${d.grants.some(g => !g[1]) ? code(`GRANT REPOSITORY ON SYSTEM TO USER '${CI.f.user}'@'%';`) : ''}</div></div>`;
}
function ciCaps() {
  const f = CI.f, ssh = !f.sshLater, repo = CI.det.grants.find(g => g[0].startsWith('REPOSITORY'))[1];
  return [['集群总览 / 架构 / 节点状态', true], ['查询分析 / 资源组 / 导入作业 / 物化视图 / 参数', ssh ? true : 'part', '需 SSH 后才能执行操作，仅可查看'],
    ['历史趋势与指标告警（Prometheus）', !!f.prom, '未配置：趋势改用平台内置采样（最近 60 分钟）'], ['性能巡检', ssh ? true : 'part', '系统环境类巡检项跳过（需 SSH）'],
    ['慢查询 Top SQL / SQL 审计', f.audit, '未安装 AuditLoader'], ['节点启停 / 扩缩容 / 升级 / 日志', ssh, '未填写 SSH 与部署目录'], ['备份恢复', repo && ssh, repo ? '未填写 SSH' : '运维账号缺少 REPOSITORY 权限']];
}
ON.ciPrev = () => { ciSave(); CI.step--; ciPaint(); };
ON.ciLater = () => { ciSave(); ciPaint(); };
ON.ciTest = () => {
  ciSave(); const f = CI.f;
  if (!f.hosts || !f.user || !f.pwd) return toast('请填写 FE 地址、运维账号和密码');
  if (!/^[\d.]+(\s*,\s*[\d.]+)*$/.test(f.hosts)) return toast('FE 地址格式不正确，多个地址用逗号分隔');
  toast('正在连接 ' + f.hosts.split(',')[0].trim() + ':' + f.qport + ' …');
  setTimeout(() => { const mode = f.hosts.startsWith('10.6') ? 'shared-data' : 'shared-nothing';
    CI.det = {version: mode === 'shared-data' ? '4.0.2' : '3.5.6', mode, nodes: genNodes({fe: f.hosts.split(',')[0].trim() + ':' + f.qport, mode, version: mode === 'shared-data' ? '4.0.2' : '3.5.6'}), grants: ciGrants()};
    ciPaint(); }, 700);
};
ON.ciSsh = () => { ciSave(); const f = CI.f; if (!f.sshUser || !f.feDir || !f.beDir) return toast('请填写 SSH 用户与部署目录'); toast('正在测试 SSH …'); setTimeout(() => { CI.sshOk = true; ciPaint(); }, 700); };
ON.ciNext = () => {
  ciSave(); const f = CI.f;
  if (CI.step === 1) { if (!/^[\w-]{3,}$/.test(f.name)) return toast('集群名称至少 3 位，只能包含字母、数字、- 和 _'); if (!CI.edit && CLUSTERS.some(c => c.id === f.name)) return toast('集群名称已存在'); }
  if (CI.step === 2 && !CI.det) return toast('请先测试连接');
  if (CI.step === 3 && !f.sshLater && !CI.sshOk) return toast('请先测试 SSH，或勾选“稍后填写”');
  CI.step++; ciPaint();
};
ON.ciDone = () => {
  ciSave(); const f = CI.f, host = f.hosts.split(',')[0].trim();
  const v = {name: f.name, env: f.env, owner: f.owner, desc: f.desc, version: CI.det.version, mode: CI.det.mode, fe: `${host}:${f.qport}`, http: `${host}:${f.hport}`, user: f.user,
    prom: f.prom, status: f.prom ? 'ok' : 'warn', ssh: f.sshLater ? null : {user: f.sshUser, port: +f.sshPort, auth: f.sshAuth, sudo: f.sudo}, dirs: f.sshLater ? null : {fe: f.feDir, be: f.beDir},
    audit: f.audit ? 'starrocks_audit_db__.starrocks_audit_tbl__' : ''};
  let c;
  if (CI.edit) { c = CLUSTERS.find(x => x.id === CI.edit); Object.assign(c, v); audit('cluster.edit', `编辑集群 ${c.name}${clusterMissing(c).length ? '' : '（必填信息已完整）'}`); }
  else { c = {id: f.name, added: now().slice(0, 10), ...v}; CLUSTERS.push(c); audit('cluster.add', `导入集群 ${c.name}（${c.version} · ${modeText(c.mode)}）${f.sshLater ? '，SSH 稍后填写' : ''}`); }
  closeModal(); CUR = c; NODES = genNodes(c); buildTop();
  toast(clusterMissing(c).length ? `已导入 ${c.name}（待完善：仅可查看）` : `${c.name} 已就绪，可以开始运维`);
  if (CI.edit || location.hash === '#overview') render(CI.edit ? undefined : 'overview'); else go('overview');
};
ACTIONS['cluster.add'] = () => ciOpen(null, 1);
ACTIONS['cluster.edit'] = d => ciOpen(CLUSTERS.find(x => x.id === d.id), +d.step || 1);

/* ---------- 未导入任何集群时的引导页 ---------- */
function onboardingHtml() {
  const step = (n, t, d, done) => `<div class="card" style="flex:1;min-width:220px"><div class="row"><span class="avatar" style="background:${done ? 'var(--ok)' : 'var(--brand)'}">${done ? '✓' : n}</span><b>${t}</b></div><div class="small muted" style="margin-top:8px">${d}</div></div>`;
  return `<div class="card" style="padding:36px 28px;text-align:center"><div style="font-size:44px">★</div><h2 style="justify-content:center;font-size:20px">欢迎使用 StarRocks 运维平台</h2>
    <p class="muted" style="max-width:640px;margin:0 auto 20px">平台已独立部署完成，但还没有纳管任何集群。导入已有集群并填写必填信息后，即可使用总览、巡检、节点管理、升级、备份等运维功能。</p>
    <div class="row" style="justify-content:center;gap:10px">${can('cluster.add') ? B('cluster.add', '＋ 导入已有集群', {cls: 'primary'}) : '<span class="tag warn">请联系管理员导入集群</span>'}${canView('deploy') ? '<a class="btn" href="#deploy">⬇ 部署新集群</a>' : ''}<a class="btn" href="#help/install">? 查看导入说明</a></div></div>
    <div class="row" style="margin-top:16px;align-items:stretch">${step(1, '填写 FE 连接', 'FE 地址、端口、运维账号与密码；测试通过后自动识别版本、架构与节点。')}${step(2, '填写 SSH 与部署目录', '运维必填：用于节点启停、扩缩容、升级、日志与系统巡检。可稍后填写，期间集群只读。')}${step(3, '可选：Prometheus / 审计表', '启用趋势图、指标告警、慢查询分析等功能。')}${step(4, '开始运维', '在顶栏选择集群，使用左侧菜单的各项功能。')}</div>`;
}

/* ---------- 原型：模拟“平台刚部署完成”的空状态 ---------- */
function applyFreshInstall() {
  CLUSTERS.length = 0; CUR = null; NODES = [];
  [ALERTS, AUDIT, DEPLOY_TASKS, UPGRADE_PLANS, BACKUPS, HOSTS, BLACKLIST].forEach(a => a.length = 0);
  USERS.splice(1);   // 只保留内置管理员 admin
}

document.addEventListener('change', e => { if (e.target.id === 'ci_sshAuth') { ciSave(); CI.sshOk = false; CI.sshHosts = null; ciPaint(); } });
