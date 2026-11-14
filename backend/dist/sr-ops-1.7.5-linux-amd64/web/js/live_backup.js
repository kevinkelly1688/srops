/* ============================================================================
   真实模式：备份恢复 / 参数配置。依赖 live.js、live_ops.js、live_query.js。
   ========================================================================== */

/* ============================ 备份恢复 ============================ */
const _bk = {render: pages.backup.render, jobs: pages.backup.jobs, repos: pages.backup.repos, policy: pages.backup.policy};
const BK_ACTIVE = {PENDING: 1, SNAPSHOTING: 1, UPLOAD_SNAPSHOT: 1, UPLOADING: 1, SAVE_META: 1, UPLOAD_INFO: 1, DOWNLOAD: 1, DOWNLOADING: 1, COMMIT: 1, COMMITTING: 1};
const bkReload = () => opsReload('backup');
pages.backup.render = function (sub) {
  if (!LIVE.on) return _bk.render.call(this, sub);
  sub = sub || 'jobs';
  return tabs('backup', [['jobs', '备份 / 恢复作业'], ['repos', '仓库'], ['policy', '备份策略']], sub) + this[sub]();
};
const bkData = () => opsGet('backup', '/backup');
pages.backup.jobs = function () {
  if (!LIVE.on) return _bk.jobs.call(this);
  const d = bkData();
  if (!d) return opsLoading('backup', '正在读取仓库与备份作业…', 'opsRetry');
  const act = d.jobs.some(j => j.active);
  clearTimeout(bkTimer); if (act) bkTimer = setTimeout(() => { if (ROUTE.page === 'backup') bkReload(); }, 4000);
  return `${warnNotice(d.warnings)}<div class="toolbar">${B('backup.create', '＋ 发起备份', {cls: 'primary'})}${B('backup.restore', '⟲ 从快照恢复', {cls: 'danger'})}<button class="btn sm" data-on="opsRetry">⟳ 刷新</button><span class="sp"></span><span class="small muted">${docLink('administration/management/Backup_and_restore/', '备份与恢复 ↗')}</span></div>
    <div class="notice"><span>ℹ</span><div>每个数据库只显示最近一次备份和最近一次恢复作业（<code>SHOW BACKUP</code> / <code>SHOW RESTORE</code> 的行为）；同一数据库同时只能有一个备份 / 恢复作业，备份期间不能对相关表做 Schema Change。</div></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>作业</th><th>类型</th><th>快照</th><th>数据库</th><th>对象</th><th>状态</th><th>进度</th><th>开始</th><th>结束</th><th>操作</th></tr></thead><tbody>
    ${d.jobs.map(j => `<tr><td class="mono small">${esc(j.id)}</td><td>${j.kind === 'backup' ? '备份' : '恢复'}</td><td class="mono small">${esc(j.snapshot)}</td><td>${esc(j.db)}</td><td class="small" style="max-width:220px;word-break:break-all">${esc(j.objects)}</td>
      <td>${jobTagL(j.state)}${j.err ? `<div class="small up" style="max-width:200px">${esc(j.err)}</div>` : ''}</td><td class="small">${esc(j.progress)}</td><td class="small">${esc(j.createTime)}</td><td class="small">${esc(j.finishTime || '—')}</td>
      <td>${j.active ? B('backup.cancel', '取消', {cls: 'sm danger', data: {db: j.db, kind: j.kind}}) : ''}</td></tr>`).join('') || '<tr><td colspan="10" class="muted">没有备份或恢复作业</td></tr>'}
    </tbody></table></div>${code('SHOW BACKUP FROM <db>;\nSHOW RESTORE FROM <db>;\nSHOW SNAPSHOT ON <repo>;')}</div>`;
};
let bkTimer = 0;
pages.backup.repos = function () {
  if (!LIVE.on) return _bk.repos.call(this);
  const d = bkData();
  if (!d) return opsLoading('backup', '正在读取仓库…', 'opsRetry');
  return `${warnNotice(d.warnings)}<div class="toolbar">${B('backup.repo', '＋ 创建仓库', {cls: 'primary'})}<button class="btn sm" data-on="opsRetry">⟳ 刷新</button></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>仓库</th><th>位置</th><th>只读</th><th>Broker</th><th>创建</th><th>状态</th><th>操作</th></tr></thead><tbody>
    ${d.repos.map(r => `<tr><td><b>${esc(r.name)}</b></td><td class="mono small">${esc(r.location)}</td><td>${r.readOnly ? '是' : '否'}</td><td class="small">${esc(r.broker || '—')}</td><td class="small">${esc(r.createTime)}</td><td>${r.err ? `<span class="small up">${esc(r.err)}</span>` : '<span class="tag ok">正常</span>'}</td>
      <td class="ops"><button class="btn sm" data-on="bkSnaps" data-n="${esc(r.name)}">快照列表</button>${B('backup.clean', '清理设置', {cls: 'sm', data: {cfg: r.name}})}${B('backup.repo', '删除', {cls: 'sm danger', data: {del: r.name}})}</td></tr>`).join('') || '<tr><td colspan="7" class="muted">还没有仓库：备份需要先创建仓库（S3 / HDFS）</td></tr>'}
    </tbody></table></div></div>`;
};
ON.bkSnaps = d => {
  api('GET', `${opsBase()}/backup/repos/${opsEnc(d.n)}/snapshots`).then(r => openDrawer(`仓库 ${d.n} 的快照`, `<div class="toolbar"><span class="small muted">共 ${r.items.length} 个快照</span><span class="sp"></span>${B('backup.clean', '删除所选快照', {cls: 'sm danger', data: {delsel: d.n}})}</div><div class="tbl-wrap"><table><thead><tr><th></th><th>快照</th><th>时间戳（恢复时使用）</th><th>状态</th></tr></thead><tbody>
    ${r.items.map(s => `<tr><td><input type="checkbox" class="bkSnapChk" value="${esc(s.name)}"></td><td class="mono small">${esc(s.name)}</td><td class="mono small">${esc(s.timestamp)}</td><td>${esc(s.status)}</td></tr>`).join('') || '<tr><td colspan="4" class="muted">仓库里还没有快照</td></tr>'}</tbody></table></div>
    <div class="small muted" style="margin-top:8px">SHOW SNAPSHOT ON ${esc(d.n)}。StarRocks 没有删除快照的 SQL 命令：S3 协议的仓库可在「清理设置」配置凭据后由平台删除快照目录（永久删除，无法恢复）；HDFS 等仓库请在存储上自行清理。</div>`, true)).catch(liveFail);
};
const DOW = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
pages.backup.policy = function () {
  if (!LIVE.on) return _bk.policy.call(this);
  const d = bkData();
  if (!d) return opsLoading('backup', '正在读取备份策略…', 'opsRetry');
  const when = p => p.every === 'weekly' ? `每${DOW[p.weekday]} ${p.at}` : `每天 ${p.at}`;
  return `<div class="toolbar">${B('backup.policy', '＋ 新建策略', {cls: 'primary'})}<button class="btn sm" data-on="opsRetry">⟳ 刷新</button><span class="sp"></span><span class="small muted">平台按策略定时对每个库发起 BACKUP SNAPSHOT（快照名 auto_策略ID_时间）；已有进行中作业的库会跳过。</span></div>
    <div class="notice warn"><span>!</span><div>定时备份由<b>平台服务</b>发起，平台服务停止期间不会备份；错过的周期不会补跑。旧快照可以设置<b>保留策略</b>自动清理（需先在「仓库 → 清理设置」配置 S3 清理凭据）；未设置保留策略的策略不会清理任何快照。</div></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>策略</th><th>数据库</th><th>周期</th><th>仓库</th><th>保留策略</th><th>状态</th><th>下次执行</th><th>上次执行</th><th>操作</th></tr></thead><tbody>
    ${d.policies.map(p => `<tr><td><b>${esc(p.name)}</b></td><td class="small">${p.dbs.map(esc).join('、')}</td><td>${when(p)}</td><td>${esc(p.repo)}</td><td class="small">${retText(p) ? esc(retText(p)) : '<span class="muted">不清理</span>'}</td><td>${p.enabled ? '<span class="tag ok">启用</span>' : '<span class="tag">停用</span>'}</td><td class="small">${p.enabled ? fmtTime(p.next) : '—'}</td>
      <td class="small" style="max-width:260px">${p.lastResult ? `${fmtTime(p.lastRun)}<div class="muted">${esc(p.lastResult)}</div>` : '—'}${p.cleanResult ? `<div style="margin-top:4px">清理 ${fmtTime(p.lastClean)}<div class="${p.cleanResult.includes('失败') || p.cleanResult.includes('未执行') ? 'up' : 'muted'}">${esc(p.cleanResult)}</div></div>` : ''}</td>
      <td class="ops">${retText(p) ? B('backup.clean', '清理', {cls: 'sm', data: {cprev: p.id}}) : ''}${B('backup.policy', p.enabled ? '停用' : '启用', {cls: 'sm', data: {pid: p.id, on: p.enabled ? '0' : '1'}})}${B('backup.policy', '编辑', {cls: 'sm', data: {edit: p.id}})}${B('backup.policy', '删除', {cls: 'sm danger', data: {del: p.id, n: p.name}})}</td></tr>`).join('') || '<tr><td colspan="9" class="muted">还没有备份策略</td></tr>'}
    </tbody></table></div></div>`;
};

const dbOpts = (d, cur) => (d.databases || []).map(x => `<option ${x === cur ? 'selected' : ''}>${esc(x)}</option>`).join('');
const rwRepos = d => d.repos.filter(r => !r.readOnly);
const _bkA = {create: ACTIONS['backup.create'], restore: ACTIONS['backup.restore'], cancel: ACTIONS['backup.cancel'], repo: ACTIONS['backup.repo'], policy: ACTIONS['backup.policy']};
ACTIONS['backup.create'] = () => {
  if (!LIVE.on) return _bkA.create();
  const d = (OPS.backup || {})[CUR.id]; if (!d) return;
  if (!rwRepos(d).length) return toast('请先在「仓库」中创建一个可写的仓库');
  const stamp = new Date().toISOString().slice(0, 10).replace(/-/g, '');
  openModal({title: '发起备份', code: 'backup.create', body: `<div class="form"><label class="req">数据库</label><select id="bkDb">${dbOpts(d)}</select>
    <label>备份对象</label><input id="bkObj" placeholder="留空表示整库；多个表用逗号分隔"><label class="req">快照名</label><input id="bkSnap" class="mono" value="snap_${stamp}"><div class="hint">字母、数字、下划线</div>
    <label class="req">仓库</label><select id="bkRepo">${rwRepos(d).map(r => `<option>${esc(r.name)}</option>`).join('')}</select></div>
    <div class="notice warn" style="margin-top:10px"><span>!</span><div>同一数据库同时只能有一个备份 / 恢复作业。备份期间不能对相关表做 Schema Change。</div></div>`,
    onOk() {
      const tables = $('#bkObj').value.split(',').map(x => x.trim()).filter(Boolean);
      opsCall('POST', '/backup/jobs', {db: $('#bkDb').value, snapshot: $('#bkSnap').value.trim(), repo: $('#bkRepo').value, tables}, '备份已提交', ['backup']); return false;
    }});
};
ACTIONS['backup.restore'] = () => {
  if (!LIVE.on) return _bkA.restore();
  const d = (OPS.backup || {})[CUR.id]; if (!d) return;
  if (!d.repos.length) return toast('没有可用的仓库');
  openModal({title: '从快照恢复', code: 'backup.restore', ok: '下一步', okCls: 'danger primary', body: `<div class="form"><label class="req">仓库</label><select id="rsRepo">${d.repos.map(r => `<option>${esc(r.name)}</option>`).join('')}</select>
    <label class="req">快照</label><select id="rsSnap"><option value="">（先选择仓库）</option></select>
    <label class="req">恢复到数据库</label><select id="rsDb">${dbOpts(d)}</select>
    <label>恢复对象</label><input id="rsObj" class="mono" placeholder="dim_user AS dim_user_restore, dim_region"><div class="hint">留空 = 恢复快照里的全部表（会覆盖同名表）；建议用 <code>AS 新表名</code> 恢复为新表后再核对</div>
    <label>副本数</label><input id="rsRep" type="number" min="0" max="10" value="0"><div class="hint">0 = 使用快照中的副本数</div></div>`,
    onOk() {
      const sel = $('#rsSnap'), snap = sel.value, ts = sel.selectedOptions[0] && sel.selectedOptions[0].dataset.ts;
      if (!snap) { toast('请选择快照'); return false; }
      const tables = $('#rsObj').value.split(',').map(x => x.trim()).filter(Boolean).map(x => { const m = x.split(/\s+as\s+/i); return {name: m[0].trim(), as: (m[1] || '').trim()}; });
      const body = {db: $('#rsDb').value, snapshot: snap, repo: $('#rsRepo').value, timestamp: ts, tables, replication: +$('#rsRep').value || 0};
      setTimeout(() => liveRisky('backup.restore', '恢复数据', `将把快照 <code>${esc(snap)}</code> 恢复到数据库 <code>${esc(body.db)}</code>${tables.length ? '' : '，<b>快照内全部表</b>，同名表的数据会被覆盖'}。`,
        `RESTORE SNAPSHOT ${body.db}.${snap} FROM ${body.repo}${tables.length ? ' ON (' + tables.map(t => t.name + (t.as ? ' AS ' + t.as : '')).join(', ') + ')' : ''};`,
        () => opsCall('POST', '/backup/restore', body, '恢复已提交', ['backup'])), 50);
    }});
  const load = () => { const repo = $('#rsRepo').value; api('GET', `${opsBase()}/backup/repos/${opsEnc(repo)}/snapshots`).then(r => { $('#rsSnap').innerHTML = r.items.map(s => `<option value="${esc(s.name)}" data-ts="${esc(s.timestamp)}">${esc(s.name)}（${esc(s.timestamp)}）</option>`).join('') || '<option value="">该仓库没有快照</option>'; }).catch(liveFail); };
  $('#rsRepo').onchange = load; load();
};
ACTIONS['backup.cancel'] = d => {
  if (!LIVE.on) return _bkA.cancel(d);
  opsCall('POST', '/backup/cancel', {db: d.db, kind: d.kind}, '已取消作业', ['backup']);
};
ACTIONS['backup.repo'] = d => {
  if (!LIVE.on) return _bkA.repo(d);
  if (d.del) return liveRisky('backup.repo', '删除仓库 ' + esc(d.del), '只删除仓库映射，不删除远端存储中的快照数据；之后该仓库中的快照将无法通过平台恢复。', 'DROP REPOSITORY `' + d.del + '`;', () => opsCall('DELETE', '/backup/repos/' + opsEnc(d.del), undefined, '已删除仓库', ['backup']));
  openModal({title: '创建仓库', code: 'backup.repo', wide: true, body: `<div class="form"><label class="req">名称</label><input id="rpN" placeholder="repo_s3_prod"><label class="req">类型</label><select id="rpT"><option value="s3">S3 / 兼容 S3（OSS / COS / MinIO）</option><option value="hdfs">HDFS（需要 Broker）</option></select>
    <label class="req">位置</label><input id="rpL" class="mono" placeholder="s3a://bucket/path"><label class="s3 req">Access Key</label><input class="s3" id="rpAK" autocomplete="off"><label class="s3 req">Secret Key</label><input class="s3" id="rpSK" type="password" autocomplete="new-password">
    <label class="s3">Endpoint</label><input class="s3" id="rpEP" placeholder="https://oss-cn-hangzhou.aliyuncs.com"><label class="s3">Region</label><input class="s3" id="rpRG" placeholder="cn-hangzhou"><label class="s3">路径风格</label><label class="chk s3"><input type="checkbox" id="rpPS"> enable_path_style_access（MinIO 等需要）</label>
    <label class="hd req" style="display:none">Broker 名称</label><input class="hd" id="rpBR" style="display:none" placeholder="SHOW BROKER 中的名称"><label class="hd" style="display:none">用户名</label><input class="hd" id="rpHU" style="display:none"><label class="hd" style="display:none">密码</label><input class="hd" id="rpHP" type="password" style="display:none" autocomplete="new-password">
    <label>只读</label><label class="chk"><input type="checkbox" id="rpRO"> 只读仓库（仅用于恢复）</label></div>
    <div class="notice" style="margin-top:10px"><span>🔒</span><div>访问密钥只用于本次创建仓库的 SQL，<b>平台不保存</b>，审计日志中也只记录 ***。</div></div>`,
    onOk() {
      const t = $('#rpT').value, b = {name: $('#rpN').value.trim(), type: t, location: $('#rpL').value.trim(), readOnly: $('#rpRO').checked};
      if (t === 's3') Object.assign(b, {accessKey: $('#rpAK').value.trim(), secretKey: $('#rpSK').value.trim(), endpoint: $('#rpEP').value.trim(), region: $('#rpRG').value.trim(), pathStyle: $('#rpPS').checked});
      else Object.assign(b, {broker: $('#rpBR').value.trim(), username: $('#rpHU').value.trim(), password: $('#rpHP').value});
      opsCall('POST', '/backup/repos', b, '已创建仓库 ' + b.name, ['backup']); return false;
    }});
};
document.addEventListener('change', e => { if (e.target.id === 'rpT') { const s3 = e.target.value === 's3'; $$('.s3').forEach(x => x.style.display = s3 ? '' : 'none'); $$('.hd').forEach(x => x.style.display = s3 ? 'none' : ''); } });
function policyForm(p) {
  const d = (OPS.backup || {})[CUR.id] || {databases: [], repos: []}; p = p || {every: 'daily', at: '02:00', weekday: 0, dbs: [], name: '', repo: ''};
  return `<div class="form"><label class="req">名称</label><input id="bpN" maxlength="40" value="${esc(p.name)}"><label class="req">数据库</label><div id="bpD" style="display:flex;flex-wrap:wrap;gap:4px 14px">${(d.databases || []).map(x => `<label class="chk"><input type="checkbox" value="${esc(x)}" ${p.dbs.includes(x) ? 'checked' : ''}> ${esc(x)}</label>`).join('') || '<span class="muted small">未读取到数据库</span>'}</div>
    <label class="req">周期</label><div class="row"><select id="bpE"><option value="daily" ${p.every === 'daily' ? 'selected' : ''}>每天</option><option value="weekly" ${p.every === 'weekly' ? 'selected' : ''}>每周</option></select><select id="bpW" style="${p.every === 'weekly' ? '' : 'display:none'}">${DOW.map((x, i) => `<option value="${i}" ${p.weekday === i ? 'selected' : ''}>${x}</option>`).join('')}</select><input id="bpA" type="time" value="${esc(p.at)}" style="width:auto"></div>
    <label class="req">仓库</label><select id="bpR">${d.repos.filter(r => !r.readOnly).map(r => `<option ${r.name === p.repo ? 'selected' : ''}>${esc(r.name)}</option>`).join('')}</select>
    <label>保留策略</label><div class="row small">保留最近 <input id="bpKL" type="number" min="0" max="1000" value="${p.keepLast || 0}" style="width:80px"> 份，且 <input id="bpKD" type="number" min="0" max="3650" value="${p.keepDays || 0}" style="width:80px"> 天内的（0 = 不限；两项都为 0 = 不清理）</div>
    <label></label><div class="small muted">只清理本策略创建的快照；同时满足“超出份数”和“超过天数”才删除，最新的一个永远保留。需要先在「仓库 → 清理设置」配置 S3 清理凭据，平台每小时自动清理一次。</div></div>`;
}
document.addEventListener('change', e => { if (e.target.id === 'bpE') $('#bpW').style.display = e.target.value === 'weekly' ? '' : 'none'; });
const readPolicy = () => ({name: $('#bpN').value.trim(), dbs: $$('#bpD input:checked').map(i => i.value), repo: $('#bpR').value, every: $('#bpE').value, at: $('#bpA').value, weekday: +$('#bpW').value || 0, keepLast: +$('#bpKL').value || 0, keepDays: +$('#bpKD').value || 0});
ACTIONS['backup.policy'] = d => {
  if (!LIVE.on) return _bkA.policy(d);
  const dd = (OPS.backup || {})[CUR.id] || {policies: []};
  if (d.pid) return opsCall('PUT', '/backup/policies/' + opsEnc(d.pid), {enabled: d.on === '1'}, d.on === '1' ? '已启用' : '已停用', ['backup']);
  if (d.del) return confirmTyped('删除备份策略 ' + esc(d.n), '删除后不再定时备份；已生成的快照不受影响。', d.n, '删除', () => opsCall('DELETE', '/backup/policies/' + opsEnc(d.del), undefined, '已删除策略', ['backup']), 'backup.policy');
  if (d.edit) {
    const p = dd.policies.find(x => x.id === d.edit); if (!p) return;
    return openModal({title: '编辑备份策略', code: 'backup.policy', wide: true, body: policyForm(p), onOk() { opsCall('PUT', '/backup/policies/' + opsEnc(p.id), readPolicy(), '已保存', ['backup']); return false; }});
  }
  if (!dd.repos || !dd.repos.some(r => !r.readOnly)) return toast('请先在「仓库」中创建一个可写的仓库');
  openModal({title: '新建备份策略', code: 'backup.policy', wide: true, body: policyForm(), onOk() { opsCall('POST', '/backup/policies', readPolicy(), '已创建策略', ['backup']); return false; }});
};

/* ----- 备份快照清理（backup.clean）：StarRocks 没有删除快照的 SQL，平台用对象存储凭据直接删除快照目录 ----- */
const retText = p => p.keepLast > 0 && p.keepDays > 0 ? `最近 ${p.keepLast} 份且 ${p.keepDays} 天内` : p.keepLast > 0 ? `最近 ${p.keepLast} 份` : p.keepDays > 0 ? `${p.keepDays} 天内` : '';
function bkRunClean(repo, body, title, detail, sql) {
  liveRisky('backup.clean', title, detail, sql, () => api('POST', `${opsBase()}/backup/repos/${opsEnc(repo)}/cleanup`, body).then(res => {
    if (livePending(res)) return;
    closeModal(); toast('已开始清理');
    taskDrawer(res.taskId, title, () => { opsReload('backup'); });
  }).catch(liveFail));
}
ACTIONS['backup.clean'] = d => {
  if (!LIVE.on) return toast('演示模式不支持');
  if (d.delsel) {
    const names = $$('.bkSnapChk:checked').map(x => x.value);
    if (!names.length) return toast('请先勾选要删除的快照');
    return bkRunClean(d.delsel, {snapshots: names}, '删除备份快照', `将<b>永久删除</b>仓库 <code>${esc(d.delsel)}</code> 中的 ${names.length} 个快照：<br><span class="mono small">${names.map(esc).join('<br>')}</span><br>删除后无法再从这些快照恢复数据。`, names.map(n => `S3 DELETE …/__ss_${n}/*`).join('\n'));
  }
  if (d.crun) {
    return bkRunClean(d.repo, {policy: d.crun}, '按保留策略清理', `将按备份策略 <b>${esc(d.n)}</b> 的保留策略（${esc(d.ret)}）<b>永久删除</b> ${d.cnt} 个过期快照，最新的一个快照永远保留。`, `S3 DELETE …/__starrocks_repository_${d.repo}/__ss_<过期快照>/*`);
  }
  if (d.cprev) {
    return api('GET', `${opsBase()}/backup/policies/${opsEnc(d.cprev)}/clean-preview`).then(r => {
      const p = ((OPS.backup || {})[CUR.id] || {policies: []}).policies.find(x => x.id === d.cprev) || {};
      const list = (arr, cls) => arr.map(s => `<tr><td class="mono small">${esc(s.name)}</td><td class="small">${fmtTime(s.at)}</td><td>${cls}</td></tr>`).join('');
      openDrawer(`保留策略预览 · ${esc(p.name || '')}`, `<div class="notice"><span>ℹ</span><div>保留策略：<b>${esc(retText(r) || '未设置')}</b>。只处理本策略创建的快照（名称以 <span class="mono">${esc(r.prefix)}</span> 开头），最新的一个永远保留；状态不是 OK 或时间无法识别的快照（${r.skipped} 个）不处理。平台每小时自动执行一次，每次最多删除 10 个最旧的。</div></div>
        ${r.credReady ? '' : '<div class="notice bad"><span>!</span><div>还没有配置该仓库的清理凭据，无法执行清理。</div></div>'}
        <div class="toolbar">${B('backup.clean', `立即清理（${r.remove.length} 个）`, {cls: 'sm danger', data: {crun: d.cprev, repo: p.repo, n: p.name, ret: retText(r), cnt: Math.min(r.remove.length, 50)}})}<span class="small muted">将删除 ${r.remove.length} 个，保留 ${r.keep.length} 个</span></div>
        <div class="tbl-wrap"><table><thead><tr><th>快照</th><th>时间</th><th>处理</th></tr></thead><tbody>${list(r.remove, '<span class="tag bad">删除</span>')}${list(r.keep, '<span class="tag ok">保留</span>')}${!r.remove.length && !r.keep.length ? '<tr><td colspan="3" class="muted">还没有该策略创建的快照</td></tr>' : ''}</tbody></table></div>`, true);
    }).catch(liveFail);
  }
  if (!d.cfg) return;
  api('GET', `${opsBase()}/backup/repos/${opsEnc(d.cfg)}/cleanup`).then(r => {
    const c = r.cred || {};
    if (!r.supported) return openModal({title: `清理设置 · ${d.cfg}`, ok: '', cancel: '关闭', body: `<div class="notice warn"><span>!</span><div>${esc(r.reason)}</div></div><div class="small muted" style="margin-top:8px">这类仓库里的旧快照需要在存储上自行清理。</div>`});
    openModal({title: `清理设置 · ${d.cfg}`, code: 'backup.clean', wide: true, ok: '验证并保存', body: `<div class="notice"><span>ℹ</span><div>StarRocks 没有删除快照的 SQL 命令（<code>DROP REPOSITORY</code> 只删除仓库映射）。平台通过 S3 协议直接删除快照目录 <span class="mono">${esc(r.dir)}__ss_&lt;快照名&gt;/</span>。创建仓库时填写的密钥平台不保存，这里单独配置，<b>加密保存</b>；建议使用只对该目录有读写权限的子账号。</div></div>
      <div class="form" style="margin-top:10px"><label class="req">访问端点</label><input id="bcE" class="mono" value="${esc(c.endpoint || '')}" placeholder="https://s3.cn-north-1.amazonaws.com.cn 或 http://minio:9000"><label>地域</label><input id="bcR" value="${esc(c.region || '')}" placeholder="默认 us-east-1，如 cn-north-1">
      <label>路径风格</label><label class="chk"><input type="checkbox" id="bcP" ${c.pathStyle ? 'checked' : ''}> 使用路径风格访问（MinIO 等需要；关闭则用 桶名.端点 的虚拟主机风格）</label>
      <label class="req">AccessKey</label><input id="bcA" class="mono" autocomplete="off" placeholder="${c.configured ? '已设置 ' + esc(c.akMasked) + '（留空保持不变）' : ''}"><label class="req">SecretKey</label><input id="bcS" type="password" autocomplete="new-password" placeholder="${c.configured ? '已设置（留空保持不变）' : ''}"></div>
      <div class="small muted" style="margin-top:8px">保存前会用该凭据列出仓库目录来验证连通性和读取权限；删除权限在第一次清理时才会检验。${c.configured ? `<br>已配置：${esc(c.by)} 于 ${fmtTime(c.at)}。` : ''}</div>
      ${c.configured ? `<div style="margin-top:8px"><button class="btn sm danger" data-on="bkCredDel" data-n="${esc(d.cfg)}">删除清理凭据</button></div>` : ''}`,
      onOk() {
        const body = {endpoint: $('#bcE').value.trim(), region: $('#bcR').value.trim(), pathStyle: $('#bcP').checked, accessKey: $('#bcA').value.trim(), secretKey: $('#bcS').value};
        if (!body.endpoint) { toast('请填写访问端点'); return false; }
        api('PUT', `${opsBase()}/backup/repos/${opsEnc(d.cfg)}/cleanup`, body).then(() => { closeModal(); toast('已验证并保存'); }).catch(liveFail); return false;
      }});
  }).catch(liveFail);
};
ON.bkCredDel = d => {
  if (!confirm('删除仓库 ' + d.n + ' 的清理凭据？使用该仓库保留策略的备份策略需要先关闭保留策略。')) return;
  api('DELETE', `${opsBase()}/backup/repos/${opsEnc(d.n)}/cleanup`).then(() => { closeModal(); toast('已删除清理凭据'); }).catch(liveFail);
};

/* ============================ 参数配置 ============================ */
const _pm = {render: pages.param.render, list: pages.param.list, history: pages.param.history, init: pages.param.init, edit: ACTIONS['param.edit'], compare: ACTIONS['param.compare']};
const PM = {q: '', onlyChanged: false};
const PM_KIND = {fe: 'FE 配置', be: 'BE / CN 配置', var: '全局变量'};
pages.param.render = function (sub) {
  if (!LIVE.on) return _pm.render.call(this, sub);
  sub = sub || 'fe';
  return tabs('param', [['fe', 'FE 配置'], ['be', CUR.mode === 'shared-data' ? 'CN 配置' : 'BE / CN 配置'], ['var', '全局变量'], ['history', '变更历史']], sub) + (sub === 'history' ? this.history() : this.list(sub));
};
pages.param.list = function (t) {
  if (!LIVE.on) return _pm.list.call(this, t);
  const d = opsGet('param-' + t, '/params/' + t);
  if (!d) return opsLoading('param-' + t, '正在读取参数…', 'opsRetry');
  const how = {fe: '<code>ADMIN SET FRONTEND CONFIG</code>：平台会在<b>每个存活的 FE</b> 上逐台执行，默认仅内存生效，FE 重启后恢复为 fe.conf 中的值', be: 'BE HTTP 接口 <code>POST /api/update_config?参数=值</code>：平台对每个节点逐台调用，默认仅内存生效，重启后恢复为 be.conf / cn.conf 中的值', var: '<code>SET GLOBAL</code>：立即对新会话生效并持久化'}[t];
  const q = PM.q.toLowerCase(), rows = d.items.filter(p => (!q || p.key.toLowerCase().includes(q)) && (!PM.onlyChanged || p.changed || p.diff));
  return `<div class="toolbar"><input type="search" placeholder="搜索参数名" id="pmQ" value="${esc(PM.q)}">${t === 'var' ? `<label class="chk"><input type="checkbox" id="pmC" ${PM.onlyChanged ? 'checked' : ''}> 只看已修改</label>` : `<label class="chk"><input type="checkbox" id="pmC" ${PM.onlyChanged ? 'checked' : ''}> 只看节点间不一致</label>`}<button class="btn sm" data-on="opsRetry">⟳ 刷新</button><span class="sp"></span>${t !== 'var' ? B('param.compare', '节点参数对比', {data: {t}}) : ''}</div>
    <div class="notice"><span>ℹ</span><div>修改方式：${how}。${t !== 'var' ? '勾选「同时写入配置文件」时，平台通过 SSH 修改各节点的 conf 文件（修改前自动备份为 *.bak-时间）。' : ''}${docLink(t === 'var' ? 'sql-reference/System_variable/' : `administration/management/${t === 'be' ? 'BE' : 'FE'}_configuration/`, '参数说明 ↗')}</div></div>
    <div class="card"><div class="small muted" style="margin-bottom:6px">共 ${d.items.length} 项${rows.length !== d.items.length ? `，当前显示 ${rows.length} 项` : ''}${t === 'be' ? ` · ${d.nodes.length} 个节点` : ''}</div><div class="tbl-wrap"><table id="pmT"><thead><tr><th>参数</th><th>当前值</th>${t === 'var' ? '<th>默认值</th>' : ''}<th>可在线修改</th><th>${t === 'fe' ? '类型 / 说明' : '类型'}</th><th>操作</th></tr></thead><tbody>
    ${rows.slice(0, 600).map(p => `<tr><td class="mono"><b>${esc(p.key)}</b></td><td class="mono ${p.changed ? 'b' : ''}" style="max-width:260px;word-break:break-all">${esc(p.value)}${p.changed ? ' <span class="tag info">已修改</span>' : ''}${p.diff ? ' <span class="tag warn">节点不一致</span>' : ''}</td>${t === 'var' ? `<td class="mono small muted">${esc(p.default || '—')}</td>` : ''}
      <td>${p.mutable ? '<span class="tag ok">是</span>' : '<span class="tag warn">需重启</span>'}</td><td class="small">${esc(p.type || '—')}${p.comment ? `<div class="muted">${esc(p.comment)}</div>` : ''}</td>
      <td>${p.mutable ? B('param.edit', '修改', {cls: 'sm', data: {t, k: p.key}}) : (t === 'var' ? '<span class="small muted">静态参数</span>' : B('param.persist', '写入配置文件', {cls: 'sm', data: {t, k: p.key, f: 1}}))}</td></tr>`).join('') || '<tr><td colspan="6" class="muted">没有匹配的参数</td></tr>'}
    </tbody></table></div>${rows.length > 600 ? '<div class="small muted" style="margin-top:6px">仅显示前 600 项，请用搜索缩小范围。</div>' : ''}</div>`;
};
pages.param.history = function () {
  if (!LIVE.on) return _pm.history.call(this);
  if (!can('param.history')) return '<div class="card"><div class="muted">🔒 需要「查看变更历史」(param.history) 权限</div></div>';
  const d = opsGet('param-history', '/params/history');
  if (!d) return opsLoading('param-history', '正在读取变更历史…', 'opsRetry');
  return `<div class="card"><div class="tbl-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>类型</th><th>范围</th><th>参数</th><th>旧值</th><th>新值</th><th>持久化</th><th>状态</th></tr></thead><tbody>
    ${d.items.map(h => `<tr><td class="small">${fmtTime(h.at)}</td><td>${esc(h.user)}</td><td>${esc(PM_KIND[h.kind] || h.kind)}</td><td class="small">${esc(h.scope)}</td><td class="mono">${esc(h.key)}</td><td class="mono small">${esc(h.from)}</td><td class="mono b small">${esc(h.to)}</td><td class="small">${esc(h.persist)}</td><td>${h.status === '已生效' ? '<span class="tag ok">已生效</span>' : `<span class="tag bad" title="${esc(h.status)}">失败</span><div class="small muted" style="max-width:200px">${esc(h.status.replace(/^失败：/, ''))}</div>`}</td></tr>`).join('') || '<tr><td colspan="9" class="muted">平台还没有记录过参数修改（只记录通过平台做的修改）</td></tr>'}
    </tbody></table></div></div>`;
};
pages.param.init = function (sub) {
  if (!LIVE.on) return _pm.init && _pm.init.call(this, sub);
  const q = $('#pmQ'); if (q) q.onchange = () => { PM.q = q.value.trim(); rerender(); };
  const c = $('#pmC'); if (c) c.onchange = () => { PM.onlyChanged = c.checked; rerender(); };
};
ACTIONS['param.edit'] = d => {
  if (!LIVE.on) return _pm.edit(d);
  const list = ((OPS['param-' + d.t] || {})[CUR.id] || {items: []}).items, p = list.find(x => x.key === d.k); if (!p) return;
  const names = d.t === 'fe' ? 'FE' : 'BE / CN';
  openModal({title: '修改参数 ' + p.key, code: 'param.edit', body: `<div class="form"><label>当前值</label><input value="${esc(p.value)}" disabled><label class="req">新值</label><input id="pmV" class="mono" value="${esc(p.value)}">
    ${d.t !== 'var' ? `<label>作用范围</label><div class="mono small">全部 ${names} 节点（逐台执行）</div><label>持久化</label><label class="chk"><input type="checkbox" id="pmP" ${d.f ? 'checked disabled' : can('param.persist') ? '' : 'disabled'}> ${d.f ? '写入配置文件（静态参数不能在线修改，重启节点后生效）' : '同时写入配置文件（重启后保留）'}${can('param.persist') ? '' : ' <span class="small muted">（🔒 需 param.persist）</span>'}</label>` : ''}</div>
    ${p.diff ? '<div class="notice warn" style="margin-top:10px"><span>!</span><div>该参数在各节点上的取值<b>不一致</b>，修改后所有节点将统一为新值。</div></div>' : ''}`,
    onOk() {
      const v = $('#pmV').value; if (v === p.value) { toast('值未变化'); return false; }
      const persist = !!($('#pmP') && $('#pmP').checked);
      const conf = d.t === 'fe' ? 'fe.conf' : 'be.conf / cn.conf';
      const sql = (d.f ? '' : d.t === 'fe' ? `ADMIN SET FRONTEND CONFIG ("${p.key}" = "${v}");` : d.t === 'be' ? `curl -XPOST http://<节点>/api/update_config?${p.key}=${v}` : `SET GLOBAL ${p.key} = ${v};`) + (persist ? `${d.f ? '' : '\n'}# 各节点 conf/${conf}: ${p.key} = ${v}` : '');
      setTimeout(() => liveRisky('param.edit', (d.f ? '写入配置文件 ' : '修改参数 ') + esc(p.key), `${esc(p.value)} → <b>${esc(v)}</b>，范围：${d.t === 'var' ? 'GLOBAL' : '全部 ' + names + ' 节点'}${persist ? `，并写入 ${conf}${d.f ? '（需重启节点后生效）' : ''}` : ''}`, sql,
        () => opsCall('PUT', `/params/${d.t}/${opsEnc(p.key)}`, {value: v, persist}, '已修改 ' + p.key, ['param-' + d.t, 'param-history'])), 50);
    }});
};
ACTIONS['param.persist'] = d => ACTIONS['param.edit'](d);
ACTIONS['param.compare'] = d => {
  if (!LIVE.on) return _pm.compare(d);
  const t = d.t || (ROUTE.sub === 'be' ? 'be' : 'fe');
  api('GET', `${opsBase()}/params/${t}/compare`).then(r => openDrawer('节点参数对比', `${warnNotice(r.warnings)}<div class="small muted" style="margin-bottom:8px">仅列出各节点取值不一致的参数（共 ${r.items.length} 项，${r.nodes.length} 个节点）</div>
    ${r.items.length ? `<div class="tbl-wrap"><table><thead><tr><th>参数</th>${r.nodes.map(n => `<th class="small">${esc(n)}</th>`).join('')}</tr></thead><tbody>
    ${r.items.map(p => { const cnt = {}; Object.values(p.nodeValues).forEach(v => cnt[v] = (cnt[v] || 0) + 1); const major = Object.keys(cnt).sort((a, b) => cnt[b] - cnt[a])[0];
      return `<tr><td class="mono small">${esc(p.key)}</td>${r.nodes.map(n => `<td class="mono small" style="${p.nodeValues[n] !== major ? 'background:var(--warn-soft);font-weight:600' : ''}">${esc(p.nodeValues[n] ?? '—')}</td>`).join('')}</tr>`; }).join('')}
    </tbody></table></div>` : '<div class="notice"><span>✓</span><div>各节点的参数取值一致。</div></div>'}<div class="small muted" style="margin-top:8px">高亮表示与多数节点不一致。</div>`, true)).catch(liveFail);
};

LIVE_PAGES.push('backup', 'param');
