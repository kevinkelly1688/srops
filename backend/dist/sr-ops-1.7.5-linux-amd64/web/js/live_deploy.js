/* ============================================================================
   真实模式：安装部署（主机管理 / 安装包 / 环境预检与修复 / 部署向导 / 部署任务）与节点扩容
   依赖 live.js、live_ops.js、live_node.js（taskDrawer / nodeRisky）。耗时操作都是服务端后台任务，这里查看任务日志。
   ========================================================================== */
const DP = {hosts: null, pkgs: null, tasks: null, err: {}, busy: {}};   // 主机 / 安装包 / 部署任务（平台级，不随集群切换）
const dpLoad = (key, path, pick) => {
  if (DP[key]) return DP[key];
  if (!DP.busy[key]) {
    DP.busy[key] = true; delete DP.err[key];
    api('GET', path).then(r => { DP[key] = pick ? pick(r) : r; }).catch(e => { DP.err[key] = e.message; })
      .finally(() => { DP.busy[key] = false; if (ROUTE.page === 'deploy') rerender(); });
  }
  return null;
};
const dpReset = (...keys) => { keys.forEach(k => { DP[k] = null; }); if (ROUTE.page === 'deploy') rerender(); };
const dpWait = (key, msg) => DP.err[key] ? `<div class="notice bad"><span>!</span><div>${esc(DP.err[key])} <a data-on="dpRetry">重试</a></div></div>` : loadingCard(msg);
ON.dpRetry = () => { DP.hosts = DP.pkgs = DP.tasks = null; DP.err = {}; rerender(); };
const sizeText = b => b >= 1073741824 ? (b / 1073741824).toFixed(2) + ' GB' : (b / 1048576).toFixed(1) + ' MB';
const MODE_TXT = {'shared-nothing': '存算一体', 'shared-data': '存算分离'};

/* ---------- 部署向导状态 ---------- */
const DWL = {step: 1, name: '', env: '测试', desc: '', pkg: '', jdk: '', mode: 'shared-nothing', home: '/data/starrocks', pwd: '', pwd2: '',
  s3: {path: '', endpoint: '', region: '', ak: '', sk: ''}, roles: {}, feConf: '', beConf: '', pc: null, pcBusy: false, taskId: '', done: false};
const dwlPkg = () => ((DP.pkgs || []).find(p => p.name === DWL.pkg)) || null;
const dwlJdk = () => ((DP.pkgs || []).find(p => p.name === DWL.jdk)) || null;
const dwlNodes = () => Object.keys(DWL.roles).filter(ip => ['FE', 'BE', 'CN'].some(r => DWL.roles[ip][r])).map(ip => ({host: ip, roles: ['FE', 'BE', 'CN'].filter(r => DWL.roles[ip][r]), observer: !!DWL.roles[ip].obs && !!DWL.roles[ip].FE}));
const dwlJavaHome = () => dwlJdk() ? DWL.home.replace(/\/+$/, '') + '/jdk' : '';
const parseConfText = t => { const m = {}; String(t || '').split('\n').forEach(l => { l = l.trim(); if (!l || l[0] === '#') return; const i = l.indexOf('='); if (i > 0) m[l.slice(0, i).trim()] = l.slice(i + 1).trim(); }); return m; };

const _deployDemo = {render: pages.deploy.render, wizard: pages.deploy.wizard, tasks: pages.deploy.tasks, hosts: pages.deploy.hosts, pkgs: pages.deploy.pkgs, init: pages.deploy.init};
pages.deploy.render = function (sub) {
  if (!LIVE.on) return _deployDemo.render.call(this, sub);
  sub = sub || 'wizard';
  const n = k => (DP[k] || []).length;
  return tabs('deploy', [['wizard', '部署向导'], ['tasks', `部署任务${DP.tasks ? ` (${n('tasks')})` : ''}`], ['hosts', `主机管理${DP.hosts ? ` (${n('hosts')})` : ''}`], ['pkgs', `安装包${DP.pkgs ? ` (${n('pkgs')})` : ''}`]], sub) + this[sub]();
};

/* ---------- 主机管理 ---------- */
pages.deploy.hosts = function () {
  if (!LIVE.on) return _deployDemo.hosts.call(this);
  const hs = dpLoad('hosts', '/api/hosts', r => { DP.pubkey = r.publicKey; return r.items; });
  if (!hs) return dpWait('hosts', '正在读取主机…');
  return `<div class="toolbar"><span class="small muted">平台通过自己的 SSH 密钥管理主机；首次添加可用密码登录并自动写入平台公钥，密码不保存。被集群使用的主机不能删除。</span><span class="sp"></span><button class="btn sm" data-on="dpRetry">⟳ 刷新</button> ${B('deploy.host', '＋ 添加主机', {cls: 'primary'})}</div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>IP</th><th>主机名</th><th>CPU</th><th>内存</th><th>根盘</th><th>系统</th><th>架构</th><th>SSH</th><th>使用</th><th>操作</th></tr></thead><tbody>
    ${hs.map(h => `<tr><td class="mono">${esc(h.ip)}<div class="small muted">${esc(h.user)}@:${h.port}${h.sudo ? ' · sudo' : ''}</div></td><td>${esc(h.name || '—')}</td><td>${h.cpu || '—'} C</td><td>${h.mem || '—'} G</td><td class="small">${h.diskGB ? h.diskGB + ' G' : '—'}</td><td class="small">${esc(h.os || '—')}</td>
      <td class="small">${esc(h.arch || '—')}${h.arch === 'amd64' ? (h.avx2 ? ' · AVX2' : ' · <span class="up">无 AVX2</span>') : ''}</td>
      <td>${h.ssh === 'ok' ? '<span class="tag ok">已连通</span>' : `<span class="tag bad" title="${esc(h.ssh)}">连接失败</span><div class="small muted" style="max-width:180px">${esc(String(h.ssh).slice(0, 60))}</div>`}<div class="small muted">${fmtTime(h.checkedAt)}</div></td>
      <td>${(h.used || []).length ? h.used.map(c => `<span class="tag info">${esc(c)}</span>`).join(' ') : '<span class="muted small">空闲</span>'}</td>
      <td class="ops">${B('deploy.host', '重新检测', {cls: 'sm', data: {chk: h.ip}})}${B('deploy.host', '删除', {cls: 'sm danger', data: {del: h.ip}})}</td></tr>`).join('') || '<tr><td colspan="10" class="muted">还没有主机，点击「添加主机」</td></tr>'}
    </tbody></table></div></div>`;
};
const _dhAct = ACTIONS['deploy.host'];
ACTIONS['deploy.host'] = d => {
  if (!LIVE.on) return _dhAct(d);
  if (d.chk) return api('POST', `/api/hosts/${encodeURIComponent(d.chk)}/check`).then(h => { toast(h.ssh === 'ok' ? '已重新检测：连通' : '检测失败：' + h.ssh); dpReset('hosts'); }).catch(liveFail);
  if (d.del) return confirmTyped('删除主机 ' + d.del, '仅从平台移除主机记录，不会卸载主机上的软件，也不会撤销已写入的平台公钥。', d.del, '确认删除', () =>
    api('DELETE', `/api/hosts/${encodeURIComponent(d.del)}`).then(() => { closeModal(); toast('已删除'); dpReset('hosts'); }).catch(liveFail), 'deploy.host');
  openModal({title: '添加主机', code: 'deploy.host', ok: '添加', wide: true, body: `<div class="form"><label class="req">主机地址</label><textarea id="hIp" rows="3" class="mono" placeholder="每行一个，支持 10.40.1.31-35（末段范围，一次最多 64 台）"></textarea>
    <label class="req">SSH 端口</label><input id="hPort" value="22"><label class="req">SSH 用户</label><input id="hUser" value="starrocks">
    <label class="req">认证方式</label><select id="hAuth"><option value="password">密码（仅首次分发公钥，密码不保存）</option><option value="key">平台密钥（公钥已加入主机）</option></select>
    <label id="hPwL" class="req">登录密码</label><input id="hPw" type="password" autocomplete="off">
    <label>免密 sudo</label><label class="chk"><input type="checkbox" id="hSudo"> 该用户可免密 sudo（「一键修复环境项」需要；也可以直接用 root）</label></div>
    <div id="hKeyBox" class="notice" style="margin-top:10px;display:none"><span>ℹ</span><div>请把下面的平台公钥加入目标主机该用户的 <code>~/.ssh/authorized_keys</code>：<div class="mono small" style="word-break:break-all;margin-top:6px;user-select:all">${esc(DP.pubkey || '')}</div></div></div>
    <div id="hRes" style="margin-top:10px"></div>`,
    onOk() {
      const body = {ips: $('#hIp').value, port: +$('#hPort').value || 22, user: $('#hUser').value.trim(), auth: $('#hAuth').value, password: $('#hPw').value, sudo: $('#hSudo').checked};
      if (!body.ips.trim()) { toast('请填写主机地址'); return false; }
      $('#hRes').innerHTML = '<div class="muted">⏳ 正在连接并探测…</div>';
      api('POST', '/api/hosts', body).then(r => {
        const okN = r.results.filter(x => x.ok).length;
        $('#hRes').innerHTML = `<div class="notice ${okN === r.results.length ? 'ok' : 'warn'}"><span>${okN === r.results.length ? '✓' : '!'}</span><div>成功 ${okN} / ${r.results.length}</div></div>` +
          `<div class="tbl-wrap"><table><tbody>${r.results.map(x => `<tr><td class="mono">${esc(x.ip)}</td><td>${x.ok ? '<span class="tag ok">已添加</span>' : '<span class="tag bad">失败</span>'}</td><td class="small">${esc(x.error || x.note || '')}</td></tr>`).join('')}</tbody></table></div>`;
        dpReset('hosts');
      }).catch(e => { $('#hRes').innerHTML = `<div class="notice bad"><span>!</span><div>${esc(e.message)}</div></div>`; });
      return false;
    }});
  const sync = () => { const pw = $('#hAuth').value === 'password'; $('#hPw').style.display = pw ? '' : 'none'; $('#hPwL').style.display = pw ? '' : 'none'; $('#hKeyBox').style.display = pw ? 'none' : ''; };
  $('#hAuth').onchange = sync; sync();
};

/* ---------- 安装包 ---------- */
pages.deploy.pkgs = function () {
  if (!LIVE.on) return _deployDemo.pkgs.call(this);
  const ps = dpLoad('pkgs', '/api/packages', r => r.items);
  if (!ps) return dpWait('pkgs', '正在读取安装包…');
  return `<div class="toolbar"><span class="small muted">从 <a href="https://www.mirrorship.cn/zh-CN/download/community" target="_blank" rel="noopener">官网下载页</a> 获取 StarRocks 安装包后上传，平台校验压缩包内容并计算 SHA256；JDK 包用于「一键修复」和部署时安装 JDK。</span><span class="sp"></span><button class="btn sm" data-on="dpRetry">⟳ 刷新</button> ${B('deploy.pkg', '⇩ 从服务器导入', {data: {imp: '1'}})} ${B('deploy.pkg', '⇧ 上传安装包', {cls: 'primary'})}</div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>文件</th><th>类型</th><th>版本</th><th>架构</th><th>大小</th><th>SHA256</th><th>上传</th><th>操作</th></tr></thead><tbody>
    ${ps.map(p => `<tr><td class="mono small">${esc(p.name)}</td><td>${p.kind === 'jdk' ? 'JDK' : 'StarRocks'}</td><td>${esc(p.ver || '—')}</td><td>${esc(p.arch)}</td><td>${sizeText(p.size)}</td><td class="mono small" title="${esc(p.sha)}">${esc(p.sha.slice(0, 12))}…</td><td class="small">${fmtTime(p.at).slice(0, 10)} · ${esc(p.by)}</td><td>${B('deploy.pkg', '删除', {cls: 'sm danger', data: {del: p.name}})}</td></tr>`).join('') || '<tr><td colspan="8" class="muted">还没有安装包</td></tr>'}
    </tbody></table></div></div>`;
};
const _dpkAct = ACTIONS['deploy.pkg'];
ACTIONS['deploy.pkg'] = d => {
  if (!LIVE.on) return _dpkAct(d);
  if (d.del) return confirmTyped('删除安装包', '将删除平台上的安装包文件 ' + esc(d.del) + '（不影响已部署的集群）。', d.del, '确认删除', () =>
    api('DELETE', `/api/packages/${encodeURIComponent(d.del)}`).then(() => { closeModal(); toast('已删除'); dpReset('pkgs'); }).catch(liveFail), 'deploy.pkg');
  if (d.imp) return dpImportModal();
  openModal({title: '上传安装包', code: 'deploy.pkg', ok: '上传', body: `<div class="form"><label class="req">文件</label><input type="file" id="pkF" accept=".gz,.tgz">
    <label>SHA256</label><input class="mono" id="pkS" placeholder="可选，填写后校验（官网下载页提供）"></div>
    <div class="small muted" style="margin-top:8px">文件名须为 <code>StarRocks-&lt;版本&gt;-&lt;系统&gt;-&lt;amd64|arm64&gt;.tar.gz</code>（如 StarRocks-3.5.6-centos-amd64.tar.gz）或 <code>jdk*.tar.gz</code>。大文件请保持页面打开。</div>
    <div class="meter" style="height:8px;margin-top:10px;display:none" id="pkBarW"><b id="pkBar" style="width:0%"></b></div><div id="pkMsg" class="small" style="margin-top:6px"></div>`,
    onOk() {
      const f = $('#pkF').files[0]; if (!f) { toast('请选择文件'); return false; }
      const fd = new FormData(); if ($('#pkS').value.trim()) fd.append('sha256', $('#pkS').value.trim()); fd.append('file', f, f.name);
      const xhr = new XMLHttpRequest(); xhr.open('POST', '/api/packages'); xhr.setRequestHeader('X-Requested-With', 'sr-ops');
      $('#pkBarW').style.display = ''; $('#pkMsg').textContent = '上传中…';
      xhr.upload.onprogress = e => { if (e.lengthComputable) { $('#pkBar').style.width = Math.round(e.loaded * 100 / e.total) + '%'; $('#pkMsg').textContent = `上传中 ${sizeText(e.loaded)} / ${sizeText(e.total)}`; } };
      xhr.onload = () => { let r = {}; try { r = JSON.parse(xhr.responseText); } catch (e) {} if (xhr.status < 300) { closeModal(); toast('已上传 ' + f.name); dpReset('pkgs'); } else $('#pkMsg').innerHTML = `<span class="up">${esc(r.error || 'HTTP ' + xhr.status)}</span>`; };
      xhr.onerror = () => { $('#pkMsg').innerHTML = '<span class="up">网络错误</span>'; };
      xhr.send(fd); return false;
    }});
};

/* 从服务器导入：安装包已经在服务器上（如直接 wget 下载）时，不必再经浏览器上传 */
function dpImportModal() {
  api('GET', '/api/packages/import').then(r => {
    const rows = r.items.map((c, i) => `<tr><td class="mono small">${esc(c.name)}</td><td>${c.kind === 'jdk' ? 'JDK' : 'StarRocks'}</td><td>${esc(c.ver || '—')}</td><td>${esc(c.arch)}</td><td>${sizeText(c.size)}</td>
      <td class="small">${c.where === 'import' ? '导入目录' : '安装包目录（原地登记）'}</td><td class="small">${fmtTime(c.modTime).slice(5, 16)}${c.fresh ? ' <span class="tag warn">仍在写入</span>' : ''}</td>
      <td><button class="btn sm primary" data-on="dpImport" data-i="${i}" ${c.fresh ? 'disabled' : ''}>导入</button></td></tr>`).join('');
    DP.imp = r.items;
    openModal({title: '从服务器导入安装包', ok: '', cancel: '关闭', wide: true, body: `<div class="notice"><span>ℹ</span><div>把安装包放到平台服务器的 <b class="mono">${esc(r.dir)}</b> 目录后，在这里导入：平台校验文件名、压缩包内容并计算 SHA256，通过后把文件<b>移动</b>到安装包目录并登记（同一磁盘上是瞬间完成的）。<br>
        例如：<span class="mono">wget -P ${esc(r.dir)} https://releases.starrocks.io/starrocks/StarRocks-&lt;版本&gt;-centos-amd64.tar.gz</span>（保持官方文件名，不要解压）。</div></div>
      <div class="form" style="margin-top:10px"><label>SHA256</label><input class="mono" id="ipS" placeholder="可选，填写后核对（对下面选择导入的那一个文件生效）"></div>
      <div class="tbl-wrap" style="margin-top:8px"><table><thead><tr><th>文件</th><th>类型</th><th>版本</th><th>架构</th><th>大小</th><th>位置</th><th>修改时间</th><th></th></tr></thead><tbody>
      ${rows || '<tr><td colspan="8" class="muted">目录里还没有可导入的安装包</td></tr>'}</tbody></table></div>
      ${r.ignored.length ? `<div class="small muted" style="margin-top:8px">以下文件名不符合要求，已忽略：${r.ignored.map(esc).join('、')}</div>` : ''}
      <div id="ipMsg" class="small" style="margin-top:6px"></div>`});
  }).catch(liveFail);
}
ON.dpImport = (d, el) => {
  const c = (DP.imp || [])[+d.i]; if (!c) return;
  el.disabled = true; $('#ipMsg').innerHTML = `<span class="muted">正在校验并导入 ${esc(c.name)}（${sizeText(c.size)}），大文件需要一两分钟，请保持页面打开…</span>`;
  api('POST', '/api/packages/import', {name: c.name, where: c.where, sha256: ($('#ipS').value || '').trim()})
    .then(() => { closeModal(); toast('已导入 ' + c.name); dpReset('pkgs'); })
    .catch(e => { el.disabled = false; $('#ipMsg').innerHTML = `<span class="up">${esc(e.message)}</span>`; });
};

/* ---------- 部署任务 ---------- */
pages.deploy.tasks = function () {
  if (!LIVE.on) return _deployDemo.tasks.call(this);
  const ts = dpLoad('tasks', '/api/tasks?limit=100', r => r.items.filter(t => t.kind === 'deploy' || t.kind === 'deploy.fix'));
  if (!ts) return dpWait('tasks', '正在读取部署任务…');
  return `<div class="toolbar"><span class="small muted">部署与环境修复都在服务端后台执行，可随时关闭页面，回来在这里查看日志。失败的部署修复问题后可重试（已完成的步骤会跳过）。</span><span class="sp"></span><button class="btn sm" data-on="dpRetry">⟳ 刷新</button></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>任务</th><th>内容</th><th>状态</th><th>进度</th><th>发起人</th><th>开始</th><th>操作</th></tr></thead><tbody>
    ${ts.map(t => `<tr><td class="mono small">${esc(t.id)}</td><td>${esc(t.title)}</td><td>${taskTag(t.state)}${t.err ? `<div class="small up" style="max-width:300px">${esc(t.err)}</div>` : ''}</td><td style="width:110px">${meter(t.state === 'done' ? 100 : t.prog, 101, 101)}</td><td>${esc(t.by)}</td><td class="small">${fmtTime(t.start)}</td>
      <td class="ops"><button class="btn sm" data-on="dpTaskLog" data-id="${esc(t.id)}" data-t="${esc(t.title)}">日志</button>${t.kind === 'deploy' && (t.state === 'failed' || t.state === 'canceled') ? B('deploy.execute', '重试', {cls: 'sm', data: {retry: t.id}}) : ''}${t.state === 'running' && t.kind === 'deploy' ? B('deploy.execute', '取消', {cls: 'sm danger', data: {cancel: t.id}}) : ''}</td></tr>`).join('') || '<tr><td colspan="7" class="muted">还没有部署任务</td></tr>'}
    </tbody></table></div></div>`;
};
ON.dpTaskLog = d => taskDrawer(d.id, d.t, () => dpReset('tasks'));

/* ---------- 部署向导 ---------- */
pages.deploy.wizard = function () {
  if (!LIVE.on) return _deployDemo.wizard.call(this);
  if (!can('deploy.create')) return `<div class="notice warn"><span>🔒</span><div>当前用户没有「新建部署任务」(deploy.create) 权限，仅可查看部署任务与主机。</div></div>` + this.tasks();
  if (!DP.hosts) dpLoad('hosts', '/api/hosts', r => { DP.pubkey = r.publicKey; return r.items; });
  if (!DP.pkgs) dpLoad('pkgs', '/api/packages', r => r.items);
  if (!DP.hosts || !DP.pkgs) return dpWait(DP.hosts ? 'pkgs' : 'hosts', '正在读取主机与安装包…');
  const st = i => i + 1 < DWL.step ? 'done' : i + 1 === DWL.step ? 'on' : '';
  const STEPS = ['基本信息', '主机与角色', '环境预检', '配置确认', '执行部署'];
  return `<div class="card"><div class="steps">${STEPS.map((s, i) => `<div class="st ${st(i)}"><b>${i + 1 < DWL.step ? '✓' : i + 1}</b>${s}</div>`).join('')}</div>
    ${this['w' + DWL.step]()}
    <div class="hr"></div><div class="row between"><button class="btn" data-on="dwlPrev" ${DWL.step === 1 || DWL.pcBusy ? 'disabled' : ''}>上一步</button>
    <span class="row">${DWL.step < 5 ? `<button class="btn primary" data-on="dwlNext">下一步</button>` : DWL.done ? `<button class="btn" data-on="dwlLog">查看日志</button><button class="btn primary" data-on="dwlReset">新建另一个</button>` : B('deploy.execute', DWL.taskId ? '部署中… 查看日志' : '▶ 开始部署', {cls: 'primary'})}</span></div></div>`;
};
const optList = (arr, sel, fn) => arr.map(x => `<option value="${esc(x.value ?? x)}" ${(x.value ?? x) === sel ? 'selected' : ''}>${esc(fn ? fn(x) : x)}</option>`).join('');
pages.deploy.w1 = function () {
  const srp = (DP.pkgs || []).filter(p => p.kind === 'starrocks'), jdks = (DP.pkgs || []).filter(p => p.kind === 'jdk');
  const sd = DWL.mode === 'shared-data';
  return `<div class="form" style="max-width:760px">
    <label class="req">集群名称</label><input id="dwName" value="${esc(DWL.name)}" placeholder="3–64 位字母、数字、- 或 _">
    <label class="req">环境</label><select id="dwEnv">${optList(['生产', '预发', '测试', '开发'], DWL.env)}</select>
    <label>说明</label><input id="dwDesc" value="${esc(DWL.desc)}" placeholder="可选">
    <label class="req">安装包</label><select id="dwPkg"><option value="">（请选择）</option>${optList(srp.map(p => ({value: p.name, p})), DWL.pkg, x => `${x.p.name}（${x.p.ver} · ${x.p.arch}）`)}</select>
    <div class="hint">${srp.length ? '安装包在「安装包」页上传。' : '<span class="up">还没有 StarRocks 安装包，请先到「安装包」页上传。</span>'}</div>
    <label>JDK 包</label><select id="dwJdk"><option value="">（使用主机自带的 JDK）</option>${optList(jdks.map(p => ({value: p.name, p})), DWL.jdk, x => `${x.p.name}（JDK ${x.p.ver} · ${x.p.arch}）`)}</select>
    <div class="hint">版本 ≥ 3.5 需要 JDK 17。选择 JDK 包时平台把它装到「部署目录/jdk」并写入 fe.conf 的 JAVA_HOME。</div>
    <label class="req">部署架构</label><div class="row"><label class="chk"><input type="radio" name="dwMode" value="shared-nothing" ${!sd ? 'checked' : ''}> 存算一体（FE + BE）</label><label class="chk"><input type="radio" name="dwMode" value="shared-data" ${sd ? 'checked' : ''}> 存算分离（FE + CN + 对象存储）</label></div>
    <label class="req">部署目录</label><input id="dwHome" value="${esc(DWL.home)}"><div class="hint">FE 装在 <code>目录/fe</code>，BE 在 <code>目录/be</code>，CN 在 <code>目录/cn</code>；FE 元数据在 <code>fe/meta</code>，BE 数据在 <code>be/storage</code>。</div>
    <label class="req">root 密码</label><input id="dwPwd" type="password" autocomplete="new-password" value="${esc(DWL.pwd)}" placeholder="至少 8 位；部署时为 root 设置"><label class="req">确认密码</label><input id="dwPwd2" type="password" autocomplete="new-password" value="${esc(DWL.pwd2)}">
    ${sd ? `<label class="req">存储路径</label><input id="dwS3p" value="${esc(DWL.s3.path)}" placeholder="bucket/path，如 sr-data/dev-sr-02"><label>Endpoint</label><input id="dwS3e" value="${esc(DWL.s3.endpoint)}" placeholder="https://s3.cn-north-1.amazonaws.com.cn（AWS 可留空）">
    <label class="req">Region</label><input id="dwS3r" value="${esc(DWL.s3.region)}" placeholder="cn-north-1"><label>Access Key</label><input id="dwS3a" value="${esc(DWL.s3.ak)}" autocomplete="off" placeholder="留空则使用实例角色（Instance Profile）"><label>Secret Key</label><input id="dwS3s" type="password" autocomplete="new-password" value="${esc(DWL.s3.sk)}">` : ''}
  </div>
  <div class="notice" style="margin-top:12px"><span>ℹ</span><div>规划建议：生产 FE 3 台（1 Leader + 2 Follower，奇数），BE ≥ 3 台以支持 3 副本；存算分离 CN ≥ 3 台。${docLink('deployment/plan_cluster/', '集群规划 ↗')}</div></div>`;
};
function dwlSaveW1() {
  if (!$('#dwName')) return;
  Object.assign(DWL, {name: $('#dwName').value.trim(), env: $('#dwEnv').value, desc: $('#dwDesc').value.trim(), pkg: $('#dwPkg').value, jdk: $('#dwJdk').value, home: $('#dwHome').value.trim(), pwd: $('#dwPwd').value, pwd2: $('#dwPwd2').value});
  if ($('#dwS3p')) Object.assign(DWL.s3, {path: $('#dwS3p').value.trim(), endpoint: $('#dwS3e').value.trim(), region: $('#dwS3r').value.trim(), ak: $('#dwS3a').value.trim(), sk: $('#dwS3s').value});
}
pages.deploy.w2 = function () {
  const pkg = dwlPkg(), sd = DWL.mode === 'shared-data', worker = sd ? 'CN' : 'BE';
  const hs = DP.hosts.filter(h => h.ssh === 'ok');
  return `<div class="toolbar"><span class="small muted">为每台主机勾选角色（同一台可混部 FE + ${worker}，生产不建议）。第一个勾选 FE 的主机为 Leader。已有集群使用的主机会标出。</span><span class="sp"></span>${B('deploy.host', '＋ 添加主机', {cls: 'sm'})}</div>
    <div class="tbl-wrap"><table><thead><tr><th>主机</th><th>规格</th><th>系统 / 架构</th><th>使用</th><th>FE</th><th>Observer</th><th>${worker}</th></tr></thead><tbody>
    ${hs.map(h => { const r = DWL.roles[h.ip] || {}, mis = pkg && h.arch && h.arch !== pkg.arch; return `<tr class="${mis ? 'muted' : ''}"><td class="mono">${esc(h.ip)}<div class="small muted">${esc(h.name || '')}</div></td><td class="small">${h.cpu}C / ${h.mem}G</td><td class="small">${esc(h.os || '—')}<br>${esc(h.arch)}${mis ? ` <span class="up">与安装包（${pkg.arch}）不符</span>` : ''}</td>
      <td>${(h.used || []).length ? h.used.map(c => `<span class="tag info">${esc(c)}</span>`).join(' ') : '<span class="small muted">空闲</span>'}</td>
      <td><input type="checkbox" class="dwR" data-ip="${esc(h.ip)}" data-r="FE" ${r.FE ? 'checked' : ''} ${mis ? 'disabled' : ''}></td><td><input type="checkbox" class="dwR" data-ip="${esc(h.ip)}" data-r="obs" ${r.obs ? 'checked' : ''} ${mis || !r.FE ? 'disabled' : ''}></td>
      <td><input type="checkbox" class="dwR" data-ip="${esc(h.ip)}" data-r="${worker}" ${r[worker] ? 'checked' : ''} ${mis ? 'disabled' : ''}></td></tr>`; }).join('') || '<tr><td colspan="7" class="muted">没有可用主机（需要 SSH 已连通），请先添加主机</td></tr>'}
    </tbody></table></div><div id="dwSum" class="small" style="margin-top:8px"></div>`;
};
pages.deploy.w3 = function () {
  const pc = DWL.pc, jdkAuto = !!dwlJdk();
  const cell = c => !c ? '—' : c.st === 'ok' ? `<span class="tag ok" title="${esc(c.val)}">通过</span>` : c.st === 'warn' ? `<span class="tag warn" title="${esc(c.val)}">警告</span><div class="small muted">${esc(c.val)}</div>` : c.st === 'bad' ? `<span class="tag bad" title="${esc(c.val)}">不通过</span><div class="small up">${esc(c.val)}</div>` : '<span class="muted">—</span>';
  const fixable = pc ? pc.items.filter(it => it.fixable && pc.hosts.some(ip => ['bad', 'warn'].includes((pc.results[it.id][ip] || {}).st))) : [];
  return `<div class="toolbar">${B('deploy.precheck', pc ? '⟳ 重新预检' : '▶ 开始预检', {cls: 'primary'})}${pc && fixable.length ? B('deploy.fix', `一键修复（${fixable.length} 项）`, {cls: 'danger'}) : ''}<span class="sp"></span><span class="small muted">只读检查，通过 SSH 在所有目标主机执行；依据官方「部署前提条件」与「检查环境配置」${docLink('deployment/environment_configurations/', '↗')}</span></div>
    ${DWL.pcBusy ? '<div class="muted" style="padding:30px;text-align:center">⏳ 预检执行中…</div>' : !pc ? '<div class="muted" style="padding:30px;text-align:center">点击「开始预检」，平台通过 SSH 在所有目标主机执行检查命令（只读，不修改系统）。</div>' :
    `<div class="tbl-wrap"><table><thead><tr><th>检查项</th><th>期望</th>${pc.hosts.map(ip => `<th class="mono">${esc(ip)}</th>`).join('')}<th>修复</th></tr></thead><tbody>
    ${pc.items.map(it => `<tr><td><b>${esc(it.item)}</b><div class="small muted mono" style="max-width:260px;word-break:break-all">${esc(it.cmd.length > 70 ? it.cmd.slice(0, 70) + '…' : it.cmd)}</div></td><td class="small">${esc(it.expect)}</td>${pc.hosts.map(ip => `<td>${cell(pc.results[it.id][ip])}</td>`).join('')}<td class="small" style="max-width:240px">${it.fixable ? esc(it.fix) : '—'}</td></tr>`).join('')}
    </tbody></table></div>
    ${dwlBlockers().length ? `<div class="notice warn" style="margin-top:10px"><span>!</span><div>${dwlBlockers().length} 项不通过：可「一键修复」（修改 sysctl / THP / limits，需要 root 或免密 sudo），或手动修复后重新预检。存在<b>不通过</b>项时不能进入下一步。${jdkAuto ? ' JDK 项会在部署时自动安装，不拦截。' : ''}</div></div>` : '<div class="notice ok" style="margin-top:10px"><span>✓</span><div>没有不通过项，可以继续。</div></div>'}`}`;
};
const dwlBlockers = () => { const pc = DWL.pc; if (!pc) return []; const out = []; pc.items.forEach(it => { if (it.id === 'java' && dwlJdk()) return; pc.hosts.forEach(ip => { if ((pc.results[it.id][ip] || {}).st === 'bad') out.push([it.id, ip]); }); }); return out; };
pages.deploy.w4 = function () {
  const nodes = dwlNodes(), fe = nodes.filter(n => n.roles.includes('FE')), wk = nodes.filter(n => !n.roles.every(r => r === 'FE')), sd = DWL.mode === 'shared-data';
  const feN = fe.filter(n => !n.observer);
  const gen = [`meta_dir = ${DWL.home}/fe/meta`, 'priority_networks = <主机所在网段>', ...(dwlJavaHome() ? [`JAVA_HOME = ${dwlJavaHome()}`] : []), ...(!sd && wk.length < 3 ? [`default_replication_num = ${wk.length}`] : []), ...(sd ? ['run_mode = shared_data', 'cloud_native_storage_type = S3', `aws_s3_path = ${DWL.s3.path}`, `aws_s3_region = ${DWL.s3.region}`, ...(DWL.s3.endpoint ? [`aws_s3_endpoint = ${DWL.s3.endpoint}`] : []), DWL.s3.ak ? 'aws_s3_access_key / aws_s3_secret_key = ***' : 'aws_s3_use_instance_profile = true'] : [])];
  return `<div class="grid g2"><div><h4 style="margin-top:0">拓扑</h4><table>
      ${[['集群', DWL.name + '（' + DWL.env + '）'], ['版本', (dwlPkg() || {}).ver + ' · ' + (dwlPkg() || {}).arch], ['架构', MODE_TXT[DWL.mode]], ['FE', fe.map(n => n.host + (n.observer ? '（Observer）' : '')).join(', ') + `（Leader：${feN[0] ? feN[0].host : '—'}）`], [sd ? 'CN' : 'BE', wk.map(n => n.host).join(', ')], ['目录', DWL.home], ['JDK', dwlJdk() ? dwlJdk().name : '主机自带']].map(([k, v]) => `<tr><td class="muted" style="width:90px">${k}</td><td>${esc(v)}</td></tr>`).join('')}</table>
      <h4>部署步骤</h4><ol class="small"><li>分发安装包${dwlJdk() ? '与 JDK' : ''}到各主机（校验 SHA256），解压并生成配置</li><li>启动 Leader FE：<code>start_fe.sh --daemon</code>，等待可连接</li><li>设置 root 密码</li><li>其余 FE：先 <code>ALTER SYSTEM ADD FOLLOWER/OBSERVER</code>，再 <code>start_fe.sh --helper ${feN[0] ? feN[0].host : '&lt;leader&gt;'}:9010 --daemon</code></li><li>${sd ? 'CN' : 'BE'}：启动后 <code>ALTER SYSTEM ADD ${sd ? 'COMPUTE NODE' : 'BACKEND'}</code></li><li>校验全部节点存活</li><li>接入平台（自动出现在「集群接入」）</li></ol></div>
      <div><h4 style="margin-top:0">平台会自动设置的 fe.conf 项</h4><div class="log small" style="height:auto;max-height:200px">${esc(gen.join('\n'))}</div>
      <h4>fe.conf 追加 / 覆盖（每行 <code>key = value</code>）</h4><textarea id="dwFeConf" rows="5" class="mono small" placeholder="例如：qe_slow_log_ms = 3000">${esc(DWL.feConf)}</textarea>
      <h4>${sd ? 'cn.conf' : 'be.conf'} 追加 / 覆盖</h4><textarea id="dwBeConf" rows="5" class="mono small" placeholder="例如：mem_limit = 80%">${esc(DWL.beConf)}</textarea>
      <div class="small muted">BE / CN 自动设置 <code>storage_root_path = 部署目录/${sd ? 'cn' : 'be'}/storage</code> 与 <code>priority_networks</code>；端口使用默认值（FE 8030 / 9020 / 9030 / 9010，BE / CN 8040 / 9050 / 9060 / 8060）。</div></div></div>`;
};
pages.deploy.w5 = function () {
  return `<div class="row" style="margin-bottom:8px"><b>${esc(DWL.name)}</b><span class="tag">${esc((dwlPkg() || {}).ver || '')}</span><span class="tag">${MODE_TXT[DWL.mode]}</span>${DWL.done ? '<span class="tag ok">部署成功</span>' : DWL.taskId ? '<span class="tag info">已提交</span>' : '<span class="tag">待执行</span>'}</div>
    <div class="notice"><span>ℹ</span><div>${DWL.taskId ? `部署任务 <code>${esc(DWL.taskId)}</code> 已在服务端执行，可点击「查看日志」，也可以离开本页，进度在「部署任务」中查看。` : `点击「开始部署」后在服务端后台执行（${dwlNodes().length} 台主机）。${SETTINGS.dualReview ? '平台已开启双人复核，需要另一位同事批准后才会开始。' : ''}`}</div></div>`;
};
pages.deploy.init = function (sub) {
  if (!LIVE.on) return _deployDemo.init && _deployDemo.init.call(this, sub);
  if (ROUTE.sub && ROUTE.sub !== 'wizard') return;
  if (DWL.step === 1) $$('input[name=dwMode]').forEach(r => r.onchange = () => { dwlSaveW1(); DWL.mode = r.value; DWL.roles = {}; DWL.pc = null; rerender(); });
  if (DWL.step === 2) {
    const sd = DWL.mode === 'shared-data', worker = sd ? 'CN' : 'BE';
    const sum = () => { const el = $('#dwSum'); if (!el) return; const c = r => Object.values(DWL.roles).filter(x => x[r]).length, fe = c('FE'); el.innerHTML = `已选：FE <b>${fe}</b>（Observer ${Object.values(DWL.roles).filter(x => x.FE && x.obs).length}） · ${worker} <b>${c(worker)}</b> ${fe && (fe - Object.values(DWL.roles).filter(x => x.FE && x.obs).length) % 2 === 0 ? '<span class="tag warn">Leader + Follower 建议奇数个</span>' : ''} ${!sd && c('BE') && c('BE') < 3 ? '<span class="tag warn">BE &lt; 3 时副本数将自动设为 BE 数量</span>' : ''}`; };
    $$('.dwR').forEach(c => c.onchange = () => { const r = DWL.roles[c.dataset.ip] = DWL.roles[c.dataset.ip] || {}; r[c.dataset.r] = c.checked; if (c.dataset.r === 'FE' && !c.checked) r.obs = false; DWL.pc = null; rerender(); });
    sum();
  }
  if (DWL.step === 4) { const a = $('#dwFeConf'), b = $('#dwBeConf'); if (a) a.oninput = () => { DWL.feConf = a.value; }; if (b) b.oninput = () => { DWL.beConf = b.value; }; }
};
ON.dwlPrev = () => { DWL.step--; rerender(); };
ON.dwlReset = () => { Object.assign(DWL, {step: 1, roles: {}, pc: null, taskId: '', done: false, pwd: '', pwd2: ''}); rerender(); };
ON.dwlLog = () => taskDrawer(DWL.taskId, '部署 ' + DWL.name, t => { if (t.state === 'done') { DWL.done = true; liveLoadClusters && liveLoadClusters().then(() => { buildTop(); rerender(); }); } dpReset('tasks'); });
ON.dwlNext = () => {
  if (DWL.step === 1) {
    dwlSaveW1();
    if (!/^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$/.test(DWL.name)) return toast('集群名称须为 3–64 位字母、数字、- 或 _');
    if (CLUSTERS.some(c => c.id === DWL.name)) return toast('集群名称已存在');
    if (!DWL.pkg) return toast('请选择安装包');
    if (!/^\/[A-Za-z0-9._\/-]+$/.test(DWL.home) || DWL.home.includes('..')) return toast('部署目录须为绝对路径，只能包含字母、数字、. _ - /');
    if (DWL.pwd.length < 8) return toast('root 密码至少 8 位');
    if (DWL.pwd !== DWL.pwd2) return toast('两次输入的密码不一致');
    if (DWL.mode === 'shared-data' && (!DWL.s3.path || !DWL.s3.region)) return toast('存算分离需要填写存储路径与 Region');
    if (DWL.mode === 'shared-data' && DWL.s3.ak && !DWL.s3.sk) return toast('请填写 Secret Key');
  }
  if (DWL.step === 2) {
    const nodes = dwlNodes(), wk = DWL.mode === 'shared-data' ? 'CN' : 'BE';
    if (!nodes.some(n => n.roles.includes('FE') && !n.observer)) return toast('至少选择 1 台 Leader / Follower FE');
    if (!nodes.some(n => n.roles.includes(wk))) return toast(`至少选择 1 台 ${wk}`);
  }
  if (DWL.step === 3) { if (!DWL.pc) return toast('请先执行环境预检'); if (dwlBlockers().length) return toast('存在不通过项，请修复后重新预检'); }
  DWL.step++; rerender();
};
function dwlPrecheck() {
  const nodes = dwlNodes(), pkg = dwlPkg();
  DWL.pcBusy = true; rerender();
  api('POST', '/api/deploy/precheck', {hosts: nodes.map(n => ({ip: n.host, roles: n.roles})), home: DWL.home, version: pkg.ver, arch: pkg.arch, javaHome: dwlJavaHome()})
    .then(r => { DWL.pc = r; }).catch(liveFail).finally(() => { DWL.pcBusy = false; rerender(); });
}
const _dwlAct = {precheck: ACTIONS['deploy.precheck'], fix: ACTIONS['deploy.fix'], execute: ACTIONS['deploy.execute']};
ACTIONS['deploy.precheck'] = d => {
  if (!LIVE.on) return _dwlAct.precheck(d);
  if (ROUTE.sub === 'hosts') return toast('请在「部署向导」的预检步骤选择主机后执行预检');
  dwlPrecheck();
};
/* 一键修复只需点「确认修复」，不要求输入文字；开启双人复核时按钮为「提交复核」 */
function confirmPlain(title, detail, okText, run, code) {
  openModal({title: '⚠ ' + title, okCls: 'danger primary', ok: SETTINGS.dualReview ? '提交复核' : okText, code, body: `<div class="notice bad"><span>⚠</span><div>${detail}</div></div>
    ${SETTINGS.dualReview ? '<div class="notice warn"><span>👥</span><div>平台已开启<b>双人复核</b>：提交后需另一名拥有同权限的用户在「操作审计 → 待复核」中批准后才会执行。</div></div>' : ''}`,
    onOk() { run(); return false; }});
}
ACTIONS['deploy.fix'] = () => {
  if (!LIVE.on) return _dwlAct.fix();
  const pc = DWL.pc; if (!pc) return;
  const items = pc.items.filter(it => it.fixable && pc.hosts.some(ip => ['bad', 'warn'].includes((pc.results[it.id][ip] || {}).st)));
  const hosts = pc.hosts.filter(ip => items.some(it => ['bad', 'warn'].includes((pc.results[it.id][ip] || {}).st)));
  const jdk = dwlJdk(), jdkItem = items.some(it => it.id === 'java'), dirItem = items.some(it => it.id === 'dir'), sysItem = items.some(it => it.id !== 'java' && it.id !== 'dir');
  confirmPlain('一键修复环境项', `将在 <b>${hosts.length}</b> 台主机上修复以下项：<b>${items.map(i => esc(i.item)).join('、')}</b>。<br>${sysItem ? '会修改内核参数（sysctl）、THP、limits.conf，并写入开机配置' : ''}${dirItem ? `${sysItem ? '；' : ''}创建部署目录 <code>${esc(DWL.home)}</code> 并把属主改为 SSH 用户（只改这个目录本身，已存在且属于其他用户的目录不会改动）` : ''}${jdkItem ? `${sysItem || dirItem ? '；并' : ''}上传安装 JDK 到 <code>${esc(dwlJavaHome())}</code>` : ''}。${dirItem || sysItem ? '需要 root 账号或免密 sudo。' : ''}`, '确认修复', () => {
    api('POST', '/api/deploy/fix', {hosts, items: items.map(i => i.id), javaHome: dwlJavaHome(), jdkPackage: jdk ? jdk.name : '', home: DWL.home}).then(res => {
      if (livePending(res)) return;
      closeModal(); taskDrawer(res.taskId, '一键修复环境项', t => { if (t.state === 'done') dwlPrecheck(); dpReset('tasks'); });
    }).catch(liveFail);
  }, 'deploy.fix');
};
ACTIONS['deploy.execute'] = d => {
  if (!LIVE.on) return _dwlAct.execute(d);
  if (d.retry) return confirmTyped('重试部署 ' + d.retry, '将从头再执行一遍部署流程，已完成的步骤（安装包上传、已运行的进程、已加入的节点）会自动跳过。', d.retry.slice(-6), '确认重试', () =>
    api('POST', `/api/tasks/${encodeURIComponent(d.retry)}/retry`, {}).then(res => { if (livePending(res)) return; closeModal(); dpReset('tasks'); taskDrawer(res.taskId, '重试部署', () => dpReset('tasks')); }).catch(liveFail), 'deploy.execute');
  if (d.cancel) return api('POST', `/api/tasks/${encodeURIComponent(d.cancel)}/cancel`, {}).then(() => { toast('已请求取消，将在当前步骤结束后停止'); dpReset('tasks'); }).catch(liveFail);
  if (DWL.taskId) return ON.dwlLog();
  const nodes = dwlNodes(), pkg = dwlPkg();
  const spec = {name: DWL.name, env: DWL.env, desc: DWL.desc, version: pkg.ver, package: pkg.name, jdkPackage: DWL.jdk, mode: DWL.mode, home: DWL.home, rootPassword: DWL.pwd, nodes,
    feConf: parseConfText(DWL.feConf), beConf: parseConfText(DWL.beConf)};
  if (DWL.mode === 'shared-data') spec.s3 = {path: DWL.s3.path, endpoint: DWL.s3.endpoint, region: DWL.s3.region, accessKey: DWL.s3.ak, secretKey: DWL.s3.sk};
  confirmTyped('执行部署 ' + DWL.name, `将在 ${nodes.length} 台主机上安装 StarRocks ${esc(pkg.ver)}（${MODE_TXT[DWL.mode]}），并启动服务、修改 root 密码。过程中会向主机写入文件、启动进程。`, DWL.name, '开始部署', () => {
    api('POST', '/api/deploy/tasks', spec).then(res => {
      if (livePending(res)) return;
      closeModal(); DWL.taskId = res.taskId; dpReset('tasks'); rerender(); ON.dwlLog();
    }).catch(liveFail);
  }, 'deploy.execute');
};

/* ---------- 节点扩容（节点管理页的「扩容」按钮） ---------- */
const SC = {plan: null, pc: null, kind: ''};
ACTIONS['node.scaleout'] = d => {
  if (!LIVE.on) return _nodeAct.scaleout(d);
  const kind = d.t || 'be';
  SC.plan = null; SC.pc = null; SC.kind = kind;
  openModal({title: `扩容 ${kind.toUpperCase()}`, wide: true, code: 'node.scaleout', ok: '开始扩容', body: '<div class="muted" style="padding:20px;text-align:center">⏳ 正在读取集群与主机信息…</div>', onOk: scaleSubmit});
  api('GET', `${opsBase()}/nodes/scaleout/plan?kind=${kind}`).then(p => { SC.plan = p; scalePaint(); }).catch(e => { const b = $('#modal .mbody'); if (b) b.innerHTML = `<div class="notice bad"><span>!</span><div>${esc(e.message)}</div></div>`; else toast(e.message); });
};
function scalePaint() {
  const p = SC.plan, kind = SC.kind, body = $('#modal .mbody'); if (!body) return;
  body.innerHTML = `<div class="form">
    ${kind === 'fe' ? `<label class="req">FE 角色</label><select id="scRole"><option value="OBSERVER">OBSERVER</option><option value="FOLLOWER">FOLLOWER</option></select>` : ''}
    <label class="req">目标主机</label><div>${p.hosts.map(h => `<label class="chk" style="margin-right:12px"><input type="checkbox" class="scH" value="${esc(h.ip)}">${esc(h.ip)} <span class="small muted">${h.cpu}C/${h.mem}G · ${esc(h.arch)}</span></label>`).join('') || '<span class="muted">没有可用主机：请先在「安装部署 → 主机管理」添加（SSH 已连通且不是该集群的同类型节点）</span>'}</div>
    <label class="req">安装包</label><select id="scPkg">${p.packages.map(x => `<option value="${esc(x.name)}">${esc(x.name)}</option>`).join('') || '<option value="">（没有与集群版本 ' + esc(p.version) + ' 一致的安装包，请先上传）</option>'}</select>
    <label>安装目录</label><div class="mono small">${esc(p.dir)}（沿用集群的部署目录）</div>
    <label>配置</label><div class="small">沿用 ${esc(p.refHost)} 上的 ${kind}.conf，仅修改 priority_networks${p.javaHome ? `；JAVA_HOME = <code>${esc(p.javaHome)}</code> 需要在新主机上存在` : ''}</div></div>
    <div class="row" style="margin-top:10px"><button class="btn sm" id="scPc">▶ 环境预检</button><span class="small muted">选好主机后先预检（只读）；未通过时可到「安装部署」页一键修复</span></div><div id="scRes" style="margin-top:8px"></div>
    <div class="small muted" style="margin:10px 0 4px">执行计划：</div>${code(kind === 'fe' ? `ALTER SYSTEM ADD <FOLLOWER|OBSERVER> "<新主机>:edit_log_port";\nbin/start_fe.sh --helper ${p.helper} --daemon` : `bin/start_${kind}.sh --daemon\nALTER SYSTEM ADD ${kind === 'cn' ? 'COMPUTE NODE' : 'BACKEND'} "<新主机>:heartbeat_service_port";`)}`;
  $('#scPc').onclick = scalePrecheck;
}
const scHosts = () => $$('.scH').filter(x => x.checked).map(x => x.value);
function scalePrecheck() {
  const hs = scHosts(); if (!hs.length) return toast('请选择主机');
  const pkg = SC.plan.packages.find(x => x.name === $('#scPkg').value), role = SC.kind.toUpperCase();
  $('#scRes').innerHTML = '<div class="muted">⏳ 预检中…</div>';
  api('POST', '/api/deploy/precheck', {hosts: hs.map(ip => ({ip, roles: [role]})), home: SC.plan.home, version: SC.plan.version, arch: pkg ? pkg.arch : '', javaHome: SC.plan.javaHome}).then(r => {
    SC.pc = r;
    const bad = r.items.filter(it => r.hosts.some(ip => r.results[it.id][ip].st === 'bad')), warn = r.items.filter(it => !bad.includes(it) && r.hosts.some(ip => r.results[it.id][ip].st === 'warn'));
    $('#scRes').innerHTML = `<div class="notice ${bad.length ? 'bad' : warn.length ? 'warn' : 'ok'}"><span>${bad.length ? '!' : warn.length ? '!' : '✓'}</span><div>${bad.length ? `不通过 ${bad.length} 项` : '没有不通过项'}${warn.length ? `，警告 ${warn.length} 项` : ''}</div></div>` +
      (bad.length + warn.length ? `<div class="tbl-wrap"><table><tbody>${bad.concat(warn).map(it => `<tr><td><b>${esc(it.item)}</b></td>${r.hosts.map(ip => { const c = r.results[it.id][ip]; return `<td class="small">${c.st === 'ok' ? '<span class="tag ok">通过</span>' : `<span class="tag ${c.st === 'bad' ? 'bad' : 'warn'}">${esc(ip)}</span> ${esc(c.val)}`}</td>`; }).join('')}</tr>`).join('')}</tbody></table></div>` : '');
  }).catch(e => { $('#scRes').innerHTML = `<div class="notice bad"><span>!</span><div>${esc(e.message)}</div></div>`; });
}
function scaleSubmit() {
  const hs = scHosts(), pkg = $('#scPkg') && $('#scPkg').value;
  if (!hs.length) { toast('请选择主机'); return false; }
  if (!pkg) { toast('请选择安装包'); return false; }
  if (!SC.pc) { toast('请先执行环境预检'); return false; }
  if (SC.pc.items.some(it => SC.pc.hosts.some(ip => SC.pc.results[it.id][ip].st === 'bad'))) { toast('预检存在不通过项，请先修复'); return false; }
  api('POST', `${opsBase()}/nodes/scaleout`, {kind: SC.kind, role: $('#scRole') ? $('#scRole').value : '', hosts: hs, package: pkg}).then(res => {
    closeModal(); taskDrawer(res.taskId, `扩容 ${SC.kind.toUpperCase()} ×${hs.length}`, () => nodeRefresh()); nodeRefresh();
  }).catch(liveFail);
  return false;
}

LIVE_PAGES.push('deploy');
