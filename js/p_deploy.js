// @author: kevinkelly
// @license: Apache Licence
// @file: p_deploy.js
// @time: 2026/10/28

/* 安装部署（向导 / 任务 / 主机 / 安装包）与 升级回滚 */
const DW = {step: 1, name: 'dev-sr-02', ver: '3.5.6', mode: 'shared-nothing', home: '/data/starrocks', pwd: '', role: {}, checked: false, fixed: false, running: false, done: false, log: [],
  s3: {ep: 'https://s3.cn-north-1.amazonaws.com.cn', bucket: 's3://sr-shared-data/dev-sr-02', region: 'cn-north-1'}};
const STEPS = ['基本信息', '主机与角色', '环境预检', '配置确认', '执行部署'];

pages.deploy = {
  render(sub) {
    sub = sub || 'wizard';
    return tabs('deploy', [['wizard', '部署向导'], ['tasks', `部署任务 (${DEPLOY_TASKS.length})`], ['hosts', `主机管理 (${HOSTS.length})`], ['pkgs', '安装包']], sub) + this[sub]();
  },
  wizard() {
    if (!can('deploy.create')) return `<div class="notice warn"><span>🔒</span><div>当前用户没有「新建部署任务」(deploy.create) 权限，仅可查看部署任务与主机。</div></div>` + this.tasks();
    const st = (i) => i + 1 < DW.step ? 'done' : i + 1 === DW.step ? 'on' : '';
    return `<div class="card"><div class="steps">${STEPS.map((s, i) => `<div class="st ${st(i)}"><b>${i + 1 < DW.step ? '✓' : i + 1}</b>${s}</div>`).join('')}</div>
      ${this['w' + DW.step]()}
      <div class="hr"></div><div class="row between"><button class="btn" data-on="dwPrev" ${DW.step === 1 || DW.running ? 'disabled' : ''}>上一步</button>
      <span class="row">${DW.step < 5 ? `<button class="btn primary" data-on="dwNext">下一步</button>` : DW.done ? `<a class="btn" href="#deploy/tasks">查看部署任务</a><button class="btn primary" data-on="dwReset">新建另一个</button>` : B('deploy.execute', DW.running ? '部署中…' : '▶ 开始部署', {cls: 'primary'})}</span></div></div>`;
  },
  w1() {
    return `<div class="form" style="max-width:720px">
      <label class="req">集群名称</label><input id="dwName" value="${DW.name}">
      <label class="req">StarRocks 版本</label><select id="dwVer">${[...new Set(PACKAGES.filter(p => !p.ver.startsWith('JDK')).map(p => p.ver))].map(v => `<option ${v === DW.ver ? 'selected' : ''}>${v}</option>`).join('')}</select>
      <div class="hint">版本 ≥ 3.5 需要 JDK 17；安装包在「安装包」页上传。</div>
      <label class="req">部署架构</label><div class="row"><label class="chk"><input type="radio" name="dwMode" value="shared-nothing" ${DW.mode === 'shared-nothing' ? 'checked' : ''}> 存算一体（FE + BE）</label><label class="chk"><input type="radio" name="dwMode" value="shared-data" ${DW.mode === 'shared-data' ? 'checked' : ''}> 存算分离（FE + CN + 对象存储）</label></div>
      <label class="req">部署目录</label><input id="dwHome" value="${DW.home}">
      <label class="req">root 初始密码</label><input id="dwPwd" type="password" placeholder="部署后为 root 设置的密码" value="${DW.pwd}">
      ${DW.mode === 'shared-data' ? `<label class="req">对象存储 Endpoint</label><input value="${DW.s3.ep}"><label class="req">存储路径</label><input value="${DW.s3.bucket}"><label>Region</label><input value="${DW.s3.region}"><label class="req">认证方式</label><select><option>AK/SK</option><option>IAM Role (Instance Profile)</option></select>` : ''}
    </div>
    <div class="notice" style="margin-top:12px"><span>ℹ</span><div>规划建议：生产 FE 3 台（1 Leader + 2 Follower，奇数），BE ≥ 3 台以支持 3 副本；存算分离 CN ≥ 3 台。${docLink('deployment/plan_cluster/', '集群规划 ↗')}</div></div>`;
  },
  w2() {
    const ok = HOSTS.filter(h => h.ssh !== '已使用');
    const roles = DW.mode === 'shared-data' ? ['FE', 'CN'] : ['FE', 'BE', 'CN'];
    return `<div class="toolbar"><span class="small muted">为每台主机勾选角色（同一台可混部 FE + BE，生产不建议）。</span><span class="sp"></span>${B('deploy.host', '＋ 添加主机', {cls: 'sm'})}</div>
    <div class="tbl-wrap"><table><thead><tr><th>主机</th><th>规格</th><th>系统 / 架构</th><th>SSH</th>${roles.map(r => `<th>${r}</th>`).join('')}</tr></thead><tbody>
    ${ok.map(h => `<tr><td class="mono">${h.ip}<div class="small muted">${h.name}</div></td><td class="small">${h.cpu}C / ${h.mem}G · ${h.disk}</td><td class="small">${h.os}<br>${h.arch}</td><td>${h.ssh === '已连通' ? '<span class="tag ok">已连通</span>' : `<span class="tag bad">${h.ssh}</span>`}</td>
      ${roles.map(r => `<td><input type="checkbox" class="dwR" data-ip="${h.ip}" data-r="${r}" ${(DW.role[h.ip] || []).includes(r) ? 'checked' : ''} ${h.ssh !== '已连通' ? 'disabled' : ''}></td>`).join('')}</tr>`).join('')}
    </tbody></table></div><div id="dwSum" class="small" style="margin-top:8px"></div>`;
  },
  w3() {
    const hs = Object.keys(DW.role).filter(ip => DW.role[ip].length);
    const idx = ip => HOSTS.findIndex(h => h.ip === ip);
    const cell = (r, i) => { const v = DW.fixed && r !== 'ok' && r !== '—' ? 'ok' : r; return v === 'ok' ? '<span class="tag ok">通过</span>' : v === 'warn' ? '<span class="tag warn">警告</span>' : v === 'bad' ? '<span class="tag bad">不通过</span>' : '—'; };
    const fails = DW.fixed ? 0 : PRECHECK.reduce((a, p) => a + hs.filter(ip => ['bad', 'warn'].includes(p.res[idx(ip)])).length, 0);
    return `<div class="toolbar">${B('deploy.precheck', DW.checked ? '⟳ 重新预检' : '▶ 开始预检', {cls: 'primary'})}${DW.checked && fails ? B('deploy.fix', `一键修复（${fails} 项）`, {cls: 'danger'}) : ''}<span class="sp"></span><span class="small muted">检查项依据官方「部署前提条件」与「检查环境配置」${docLink('deployment/environment_configurations/', '↗')}</span></div>
    ${!DW.checked ? '<div class="muted" style="padding:30px;text-align:center">点击「开始预检」，平台通过 SSH 在所有目标主机执行检查脚本（只读，不修改系统）。</div>' :
    `<div class="tbl-wrap"><table><thead><tr><th>检查项</th><th>期望</th>${hs.map(ip => `<th class="mono">${ip}</th>`).join('')}<th>修复命令</th></tr></thead><tbody>
    ${PRECHECK.map(p => `<tr><td><b>${p.item}</b><div class="small muted mono">${esc(p.cmd)}</div></td><td class="small">${p.expect}</td>${hs.map(ip => `<td>${cell(p.res[idx(ip)])}</td>`).join('')}<td class="small mono" style="max-width:260px">${esc(p.fix) || '—'}</td></tr>`).join('')}
    </tbody></table></div>
    ${fails ? `<div class="notice warn" style="margin-top:10px"><span>!</span><div>${fails} 项未通过：可「一键修复」（会修改 sysctl / THP / limits 并写入开机配置），或手动修复后重新预检。存在 <b>不通过</b> 项时不允许进入下一步。</div></div>` : '<div class="notice ok" style="margin-top:10px"><span>✓</span><div>全部通过，可以继续。</div></div>'}`}`;
  },
  w4() {
    const by = r => Object.keys(DW.role).filter(ip => DW.role[ip].includes(r));
    const fe = by('FE');
    return `<div class="grid g2"><div><h4 style="margin-top:0">拓扑</h4><table>
      ${[['集群', DW.name], ['版本', DW.ver], ['架构', modeText(DW.mode)], ['FE', fe.join(', ') + `（Leader: ${fe[0] || '—'}）`], ...(DW.mode === 'shared-data' ? [['CN', by('CN').join(', ')], ['对象存储', DW.s3.bucket]] : [['BE', by('BE').join(', ')], ['CN', by('CN').join(', ') || '—']]), ['目录', DW.home]].map(([k, v]) => `<tr><td class="muted" style="width:90px">${k}</td><td>${v}</td></tr>`).join('')}</table>
      <h4>部署步骤</h4><ol class="small"><li>分发安装包与 JDK，解压到 ${DW.home}</li><li>生成 fe.conf / ${DW.mode === 'shared-data' ? 'cn.conf' : 'be.conf'}（下方可编辑）</li><li>启动 Leader FE：<code>start_fe.sh --daemon</code></li><li>其余 FE：<code>start_fe.sh --helper ${fe[0] || '&lt;leader&gt;'}:9010 --daemon</code> + <code>ALTER SYSTEM ADD FOLLOWER</code></li><li>启动 ${DW.mode === 'shared-data' ? 'CN 并 ADD COMPUTE NODE' : 'BE 并 ADD BACKEND'}</li><li>设置 root 密码，校验 SHOW FRONTENDS / BACKENDS</li><li>接入平台（自动加入「集群接入」）</li></ol></div>
      <div><h4 style="margin-top:0">fe.conf（差异项）</h4><textarea rows="9" class="mono small">meta_dir = ${DW.home}/fe/meta
priority_networks = 10.40.1.0/24
http_port = 8030
rpc_port = 9020
query_port = 9030
edit_log_port = 9010
JAVA_HOME = /opt/jdk17
JAVA_OPTS = "-Xmx16384m -XX:+UseG1GC"${DW.mode === 'shared-data' ? `\nrun_mode = shared_data\ncloud_native_storage_type = S3\naws_s3_path = ${DW.s3.bucket.replace('s3://', '')}\naws_s3_endpoint = ${DW.s3.ep}` : ''}</textarea>
      <h4>${DW.mode === 'shared-data' ? 'cn.conf' : 'be.conf'}（差异项）</h4><textarea rows="6" class="mono small">priority_networks = 10.40.1.0/24
be_port = 9060
be_http_port = 8040
heartbeat_service_port = 9050
brpc_port = 8060
${DW.mode === 'shared-data' ? 'storage_root_path = /data1/starrocks/cn  # Data Cache\nstarlet_port = 9070' : 'storage_root_path = /data1/starrocks/be;/data2/starrocks/be\nmem_limit = 90%'}</textarea></div></div>`;
  },
  w5() {
    return `<div class="row" style="margin-bottom:8px"><b>${DW.name}</b><span class="tag">${DW.ver}</span><span class="tag">${modeText(DW.mode)}</span>${DW.done ? '<span class="tag ok">部署成功</span>' : DW.running ? '<span class="tag info">执行中</span>' : '<span class="tag">待执行</span>'}</div>
      <div class="meter" style="height:10px;margin-bottom:10px"><b id="dwBar" style="width:${DW.done ? 100 : 0}%"></b></div>
      <div class="log" id="dwLog">${DW.log.length ? esc(DW.log.join('\n')) :'点击「开始部署」执行。执行过程中可离开本页，进度在「部署任务」中查看。'}</div>`;
  },
  tasks() {
    return `<div class="card"><div class="tbl-wrap"><table><thead><tr><th>任务</th><th>名称</th><th>版本</th><th>架构</th><th>主机数</th><th>状态</th><th>进度</th><th>发起人</th><th>时间</th><th>操作</th></tr></thead><tbody>
    ${DEPLOY_TASKS.map(t => `<tr><td class="mono">${t.id}</td><td>${t.name}</td><td>${t.ver}</td><td>${modeText(t.mode)}</td><td>${t.hosts}</td><td><span class="tag ${t.state === '已完成' ? 'ok' : t.state === '失败' ? 'bad' : 'info'}">${t.state}</span>${t.err ? `<div class="small up">${t.err}</div>` : ''}</td><td style="width:110px">${meter(t.prog, 101, 101)}</td><td>${t.by}</td><td>${t.at}</td>
      <td class="ops"><button class="btn sm" data-on="taskLog" data-id="${t.id}">日志</button>${t.state === '失败' ? B('deploy.execute', '重试', {cls: 'sm', data: {retry: t.id}}) : ''}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  hosts() {
    return `<div class="toolbar"><span class="small muted">平台通过 SSH（密钥）管理主机。被纳管集群使用的主机标记为「已使用」。</span><span class="sp"></span>${B('deploy.host', '＋ 添加主机', {cls: 'primary'})}${B('deploy.precheck', '批量预检')}</div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>IP</th><th>主机名</th><th>CPU</th><th>内存</th><th>磁盘</th><th>系统</th><th>架构</th><th>SSH</th><th>操作</th></tr></thead><tbody>
    ${HOSTS.map(h => `<tr><td class="mono">${h.ip}</td><td>${h.name}</td><td>${h.cpu} C</td><td>${h.mem} G</td><td class="small">${h.disk}</td><td>${h.os}</td><td class="small">${h.arch}</td><td><span class="tag ${h.ssh === '已连通' ? 'ok' : h.ssh === '已使用' ? 'info' : 'bad'}">${h.ssh}</span></td><td class="ops">${B('deploy.host', '删除', {cls: 'sm danger', data: {del: h.ip}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  pkgs() {
    return `<div class="toolbar"><span class="small muted">安装包从 <a href="https://www.mirrorship.cn/zh-CN/download/community" target="_blank" rel="noopener">官网下载页</a> 获取后上传，平台校验 SHA256。</span><span class="sp"></span>${B('deploy.pkg', '⇧ 上传安装包', {cls: 'primary'})}</div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>文件</th><th>版本</th><th>架构</th><th>大小</th><th>SHA256</th><th>上传</th><th>操作</th></tr></thead><tbody>
    ${PACKAGES.map(p => `<tr><td class="mono small">${p.name}</td><td>${p.ver}</td><td>${p.arch}</td><td>${p.size}</td><td class="mono small">${p.sha}</td><td class="small">${p.up} · ${p.by}</td><td>${B('deploy.pkg', '删除', {cls: 'sm danger', data: {del: p.name}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  init() {
    if (ROUTE.sub && ROUTE.sub !== 'wizard') return;
    if (DW.step === 1) {
      $$('input[name=dwMode]').forEach(r => r.onchange = () => { saveW1(); DW.mode = r.value; DW.role = {}; rerender(); });
    }
    if (DW.step === 2) { const upd = () => { DW.role = {}; $$('.dwR').forEach(c => { if (c.checked) (DW.role[c.dataset.ip] = DW.role[c.dataset.ip] || []).push(c.dataset.r); }); sum(); }; $$('.dwR').forEach(c => c.onchange = upd); upd(); }
    function sum() { const c = r => Object.values(DW.role).filter(x => x.includes(r)).length; const fe = c('FE');
      $('#dwSum').innerHTML = `已选：FE <b>${fe}</b> · ${DW.mode === 'shared-data' ? `CN <b>${c('CN')}</b>` : `BE <b>${c('BE')}</b> · CN <b>${c('CN')}</b>`} ${fe && fe % 2 === 0 ? '<span class="tag warn">FE Follower 建议奇数个</span>' : ''} ${DW.mode !== 'shared-data' && c('BE') && c('BE') < 3 ? '<span class="tag warn">BE &lt; 3 时建表默认副本数需改为 1</span>' : ''}`; }
  }
};
function saveW1() { if (!$('#dwName')) return; DW.name = $('#dwName').value; DW.ver = $('#dwVer').value; DW.home = $('#dwHome').value; DW.pwd = $('#dwPwd').value; }
ON.dwPrev = () => { DW.step--; rerender(); };
ON.dwReset = () => { Object.assign(DW, {step: 1, role: {}, checked: false, fixed: false, running: false, done: false, log: []}); rerender(); };
ON.dwNext = () => {
  if (DW.step === 1) { saveW1(); if (!DW.name) return toast('请填写集群名称'); }
  if (DW.step === 2) { const r = Object.values(DW.role); if (!r.some(x => x.includes('FE'))) return toast('至少选择 1 台 FE'); if (!r.some(x => x.includes(DW.mode === 'shared-data' ? 'CN' : 'BE'))) return toast(`至少选择 1 台 ${DW.mode === 'shared-data' ? 'CN' : 'BE'}`); }
  if (DW.step === 3) { if (!DW.checked) return toast('请先执行环境预检'); const hs = Object.keys(DW.role).filter(ip => DW.role[ip].length); const bad = !DW.fixed && PRECHECK.some(p => hs.some(ip => p.res[HOSTS.findIndex(h => h.ip === ip)] === 'bad')); if (bad) return toast('存在不通过项，请修复后重新预检'); }
  DW.step++; rerender();
};
ACTIONS['deploy.precheck'] = () => { if (ROUTE.sub === 'hosts') { audit('deploy.precheck', '批量环境预检'); return toast('已对已连通主机执行预检，结果见部署向导'); } toast('预检执行中…'); setTimeout(() => { DW.checked = true; audit('deploy.precheck', `环境预检 ${Object.keys(DW.role).join(', ')}`); rerender(); }, 800); };
ACTIONS['deploy.fix'] = () => risky('deploy.fix', '一键修复环境项', '将在目标主机上修改内核参数（sysctl）、THP、limits.conf，并安装 JDK 17。修改会写入开机配置。', `swapoff -a\ncat >> /etc/sysctl.conf <<EOF\nvm.swappiness=0\nvm.overcommit_memory=1\nvm.max_map_count=262144\nEOF\nsysctl -p\necho madvise > /sys/kernel/mm/transparent_hugepage/enabled\ntar xzf jdk-17*.tar.gz -C /opt && ln -sfn /opt/jdk-17* /opt/jdk17`, () => { DW.fixed = true; audit('deploy.fix', '一键修复环境项'); toast('修复完成，请重新预检确认'); rerender(); });
ACTIONS['deploy.execute'] = d => {
  if (d.retry) { const t = DEPLOY_TASKS.find(x => x.id === d.retry); t.state = '执行中'; t.err = ''; audit('deploy.execute', '重试部署 ' + t.id); rerender(); setTimeout(() => { t.state = '已完成'; t.prog = 100; if (ROUTE.page === 'deploy') rerender(); }, 3000); return; }
  if (DW.running) return;
  risky('deploy.execute', '执行部署 ' + DW.name, `将在 ${Object.keys(DW.role).length} 台主机上安装 StarRocks ${DW.ver}（${modeText(DW.mode)}）。`, '', () => {
    DW.running = true; rerender();
    const id = 'DP-00' + (14 + DEPLOY_TASKS.length - 3);
    const task = {id, name: DW.name + ' 新建（' + modeText(DW.mode) + '）', ver: DW.ver, mode: DW.mode, hosts: Object.keys(DW.role).length, state: '执行中', prog: 0, by: ME.login, at: now()};
    DEPLOY_TASKS.unshift(task); audit('deploy.execute', '执行部署 ' + id + ' ' + DW.name);
    const fe = Object.keys(DW.role).filter(ip => DW.role[ip].includes('FE')), wk = Object.keys(DW.role).filter(ip => DW.role[ip].some(r => r !== 'FE'));
    const lines = [`[1/7] 分发 StarRocks-${DW.ver} 与 JDK 17 到 ${Object.keys(DW.role).length} 台主机 …… ok`, '[2/7] 解压并生成配置文件 fe.conf / ' + (DW.mode === 'shared-data' ? 'cn.conf' : 'be.conf') + ' …… ok',
      `[3/7] 启动 Leader FE ${fe[0]}：start_fe.sh --daemon …… ok`, ...fe.slice(1).map(ip => `[4/7] 启动 FE ${ip}：start_fe.sh --helper ${fe[0]}:9010 --daemon；ALTER SYSTEM ADD FOLLOWER "${ip}:9010" …… ok`),
      ...wk.map(ip => `[5/7] 启动 ${DW.mode === 'shared-data' ? 'CN' : 'BE'} ${ip}；ALTER SYSTEM ADD ${DW.mode === 'shared-data' ? 'COMPUTE NODE' : 'BACKEND'} "${ip}:9050" …… ok`),
      '[6/7] SET PASSWORD FOR root；校验 SHOW FRONTENDS / SHOW BACKENDS …… 全部 Alive', '[7/7] 接入运维平台 …… ok', '✓ 部署完成'];
    let k = 0; const t = setInterval(() => {
      const log = $('#dwLog'), bar = $('#dwBar');
      DW.log.push(lines[k]);
      if (log) { log.textContent = DW.log.join('\n'); log.scrollTop = 1e9; }
      task.prog = Math.round((k + 1) / lines.length * 100); if (bar) bar.style.width = task.prog + '%';
      if (++k >= lines.length) { clearInterval(t); DW.running = false; DW.done = true; task.state = '已完成';
        Object.keys(DW.role).forEach(ip => HOSTS.find(h => h.ip === ip).ssh = '已使用');
        CLUSTERS.push({id: DW.name, name: DW.name, env: '开发', version: DW.ver, mode: DW.mode, fe: fe[0] + ':9030', http: fe[0] + ':8030', user: 'root', prom: '', status: 'ok', desc: '平台部署', added: now().slice(0, 10), owner: ME.login,
          ssh: {user: 'starrocks', port: 22, auth: '平台密钥', sudo: true}, dirs: {fe: DW.home + '/fe', be: DW.home + (DW.mode === 'shared-data' ? '/cn' : '/be')}, audit: ''});
        buildTop(); if (ROUTE.page === 'deploy') rerender(); }
    }, 600);
  });
};
ON.taskLog = d => { const t = DEPLOY_TASKS.find(x => x.id === d.id); openDrawer(t.id + ' 执行日志', `<div class="log" style="height:60vh">[1/7] 分发安装包 …… ok\n[2/7] 生成配置 …… ok\n${t.state === '失败' ? '[3/7] 连接 10.40.1.23 …… FAILED\nPermission denied (publickey,gssapi-keyex,gssapi-with-mic)\n任务终止：请在「主机管理」修复 SSH 认证后重试。' : '……\n✓ 部署完成'}</div>`, true); };
ACTIONS['deploy.host'] = d => {
  if (d.del) return risky('deploy.host', '删除主机 ' + d.del, '仅从平台移除主机记录，不会卸载主机上的软件。', '', () => { HOSTS.splice(HOSTS.findIndex(h => h.ip === d.del), 1); audit('deploy.host', '删除主机 ' + d.del); rerender(); });
  openModal({title: '添加主机', code: 'deploy.host', body: `<div class="form"><label class="req">IP 列表</label><textarea id="hIp" rows="3" class="mono" placeholder="每行一个，支持 10.40.1.31-35"></textarea>
    <label class="req">SSH 端口</label><input value="22"><label class="req">SSH 用户</label><input value="starrocks"><label class="req">认证方式</label><select><option>平台密钥（推荐）</option><option>密码（仅首次分发公钥）</option></select>
    <div class="hint">添加后自动探测 CPU / 内存 / 磁盘 / 系统信息。</div></div>`,
    onOk() { $('#hIp').value.split(/\s+/).filter(Boolean).forEach(ip => HOSTS.push({ip, name: 'host-' + ip.split('.').pop(), cpu: 32, mem: 128, disk: '2×SSD 1.9T', os: 'Rocky 9.3', arch: 'x86_64 (AVX2)', ssh: '已连通', used: '—'})); audit('deploy.host', '添加主机'); rerender(); }});
};
ACTIONS['deploy.pkg'] = d => {
  if (d.del) return risky('deploy.pkg', '删除安装包', d.del, '', () => { PACKAGES.splice(PACKAGES.findIndex(p => p.name === d.del), 1); audit('deploy.pkg', '删除安装包 ' + d.del); rerender(); });
  openModal({title: '上传安装包', code: 'deploy.pkg', body: `<div class="form"><label class="req">文件</label><input type="file"><label>SHA256</label><input class="mono" placeholder="可选，填写后校验"></div>`, ok: '上传', onOk() { PACKAGES.unshift({name: 'StarRocks-4.0.3-centos-amd64.tar.gz', ver: '4.0.3', arch: 'amd64', size: '2.3 GB', sha: 'e1b0…44c2', up: now().slice(0, 10), by: ME.login}); audit('deploy.pkg', '上传安装包 StarRocks-4.0.3'); rerender(); }});
};

/* ---------- 升级回滚 ---------- */
const UPG = {running: false, idx: -1, paused: false};
function upgradeOrder() {
  const be = NODES.filter(n => n.type === 'be'), cn = NODES.filter(n => n.type === 'cn'), fe = NODES.filter(n => n.type === 'fe');
  const feOrder = fe.filter(n => n.role === 'Observer').concat(fe.filter(n => n.role === 'Follower'), fe.filter(n => n.role === 'Leader'));
  return be.concat(cn, feOrder);
}
pages.upgrade = {
  render() {
    const order = upgradeOrder(), target = UPGRADE_PLANS.find(p => p.cluster === CUR.id && p.state !== '已完成');
    return `<div class="grid g-main"><div class="card"><h2>${CUR.name} 当前版本 <span class="tag brand">${CUR.version}</span><span class="right">${B('upgrade.create', '＋ 创建升级计划', {cls: 'primary sm'})}</span></h2>
      <div class="notice"><span>ℹ</span><div><b>升级顺序</b>：先 BE → CN → 最后 FE（FE 内按 Observer → Follower → Leader）。<b>降级 / 回滚顺序相反</b>：先 FE 再 BE / CN。大版本须逐级升级（如 3.3 → 3.4 → 3.5），升级前建议备份元数据并关闭 Tablet 均衡与 Compaction 的高负载时段。${docLink('deployment/upgrade/', '升级 ↗')} ${docLink('deployment/downgrade/', '降级 ↗')}</div></div>
      <h4>滚动升级执行（逐台：优雅停止 → 替换 lib/bin → 启动 → 等待心跳与 Tablet 健康）</h4>
      <div class="tbl-wrap"><table><thead><tr><th>#</th><th>节点</th><th>角色</th><th>当前</th><th>状态</th></tr></thead><tbody>
      ${order.map((n, i) => `<tr><td>${i + 1}</td><td class="mono">${n.host}</td><td>${n.type.toUpperCase()}${n.role ? ' · ' + n.role : ''}</td><td>${n.version}</td><td>${UPG.idx > i ? '<span class="tag ok">已完成</span>' : UPG.idx === i ? `<span class="tag info">${UPG.paused ? '已暂停' : '升级中…'}</span>` : '<span class="tag">待升级</span>'}</td></tr>`).join('')}
      </tbody></table></div>
      <div class="row" style="margin-top:12px">${target ? B('upgrade.execute', UPG.running ? '升级中…' : `▶ 执行 ${target.from} → ${target.to}`, {cls: 'primary', data: {id: target.id}}) : '<span class="small muted">当前集群没有待执行的升级计划</span>'}
        ${UPG.running ? B('upgrade.pause', UPG.paused ? '继续' : '暂停') : ''}${B('upgrade.rollback', '回滚 / 降级', {cls: 'danger'})}</div></div>
      <div class="card"><h2>升级前检查</h2>${[['FE / BE 全部 Alive', 'ok'], ['不健康 Tablet = 0', 'warn'], ['元数据已备份（meta 目录）', 'ok'], ['目标版本安装包已上传', 'ok'], ['已暂停 Tablet 均衡', 'info'], ['无运行中的 Schema Change / Backup', 'ok']].map(([k, s]) => `<div class="row between small" style="padding:6px 0;border-bottom:1px solid var(--border)"><span>${k}</span>${tagOf(s)}</div>`).join('')}
        ${code('ADMIN SET FRONTEND CONFIG ("tablet_sched_disable_balance" = "true");\n-- 升级完成后恢复\nADMIN SET FRONTEND CONFIG ("tablet_sched_disable_balance" = "false");')}</div></div>
    <div class="card" style="margin-top:16px"><h2>升级计划</h2><div class="tbl-wrap"><table><thead><tr><th>编号</th><th>集群</th><th>版本</th><th>升级路径</th><th>状态</th><th>创建</th></tr></thead><tbody>
      ${UPGRADE_PLANS.map(p => `<tr><td class="mono">${p.id}</td><td>${p.cluster}</td><td>${p.from} → ${p.to}</td><td class="small">${p.path}</td><td><span class="tag ${p.state === '已完成' ? 'ok' : 'info'}">${p.state}</span></td><td class="small">${p.by} · ${p.at}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  }
};
ACTIONS['upgrade.create'] = () => openModal({title: '创建升级计划', code: 'upgrade.create', body: `<div class="form"><label>集群</label><input value="${CUR.name}（当前 ${CUR.version}）" disabled>
  <label class="req">目标版本</label><select id="upTo">${[...new Set(PACKAGES.filter(p => !p.ver.startsWith('JDK')).map(p => p.ver))].map(v => `<option>${v}</option>`).join('')}</select>
  <label>执行窗口</label><input value="2026-10-08 01:00 ~ 05:00"><label>每台间隔</label><input value="等待 Tablet 健康后 60 秒"></div>
  <div class="notice warn" style="margin-top:10px"><span>!</span><div>跨大版本（例如 3.3 → 3.5）平台会自动拆分为逐级升级。</div></div>`,
  onOk() { const to = $('#upTo').value; UPGRADE_PLANS.unshift({id: 'UP-00' + (UPGRADE_PLANS.length + 4), cluster: CUR.id, from: CUR.version, to, state: '待执行', by: ME.login, at: now(), path: `${CUR.version} → ${to}`}); audit('upgrade.create', `创建升级计划 ${CUR.version} → ${to}`); rerender(); }});
ACTIONS['upgrade.execute'] = d => {
  if (UPG.running) return;
  const p = UPGRADE_PLANS.find(x => x.id === d.id);
  risky('upgrade.execute', `滚动升级 ${p.from} → ${p.to}`, `将逐台重启 ${upgradeOrder().length} 个节点，期间单节点不可用，查询可能有短暂失败重试。`, '', () => {
    UPG.running = true; UPG.idx = 0; audit('upgrade.execute', `执行滚动升级 ${p.from} → ${p.to}`); rerender();
    const order = upgradeOrder();
    const t = setInterval(() => {
      if (UPG.paused) return;
      order[UPG.idx].version = p.to; UPG.idx++;
      if (UPG.idx >= order.length) { clearInterval(t); UPG.running = false; p.state = '已完成'; CUR.version = p.to; buildTop(); toast('升级完成'); }
      if (ROUTE.page === 'upgrade') rerender();
    }, 900);
  });
};
ACTIONS['upgrade.pause'] = () => { UPG.paused = !UPG.paused; audit('upgrade.pause', UPG.paused ? '暂停升级' : '继续升级'); rerender(); };
ACTIONS['upgrade.rollback'] = () => risky('upgrade.rollback', '回滚 / 降级', '降级顺序：先 FE（Leader 最后）再 BE / CN。仅支持降级到上一个小版本 / 大版本，且降级前不得使用新版本的新特性（新特性产生的元数据无法被旧版本识别）。', '', () => { audit('upgrade.rollback', '回滚集群版本'); toast('已提交回滚任务'); });
