// @author: kevinkelly
// @license: Apache Licence
// @file: live_node.js
// @time: 2026/10/28

/* ============================================================================
   真实模式：节点管理（启动 / 停止 / 重启走 SSH，下线 / 移除 / 黑名单走 SQL，日志走 SSH）
   长耗时操作在服务端以“后台任务”执行：请求返回任务号，这里轮询任务日志。
   同时提供 taskDrawer()：安装部署、升级回滚也用它查看任务日志。
   ========================================================================== */

/* ---------- 后台任务日志抽屉 ---------- */
const TV = {id: '', timer: 0};
const TASK_ST = {running: ['info', '执行中'], paused: ['warn', '已暂停'], done: ['ok', '已完成'], failed: ['bad', '失败'], canceled: ['', '已取消']};
const taskTag = s => { const t = TASK_ST[s] || ['', s]; return `<span class="tag ${t[0]}">${t[1]}</span>`; };
function taskDrawer(id, title, onDone) {
  clearInterval(TV.timer);
  TV.id = id;
  openDrawer(title, `<div class="row between" style="margin-bottom:8px"><span id="tkState">${taskTag('running')}</span><span class="small muted" id="tkMeta"></span></div>
    <div class="meter" style="height:8px;margin-bottom:10px"><b id="tkBar" style="width:0%"></b></div><div class="log" id="tkLog" style="height:62vh"></div><div id="tkErr"></div>`, true);
  let from = 0, lines = [], busy = false, finished = false;
  const tick = async () => {
    if (busy || finished) return;
    if (TV.id !== id || !$('#drawer').classList.contains('open')) { clearInterval(TV.timer); return; }
    busy = true;
    try {
      const r = await api('GET', `/api/tasks/${encodeURIComponent(id)}?from=${from}`);
      lines = lines.concat(r.task.log || []); from = r.total;
      const log = $('#tkLog'); if (!log) return;
      const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 30;
      log.textContent = lines.join('\n') || '（暂无日志）';
      if (atEnd) log.scrollTop = log.scrollHeight;
      $('#tkState').innerHTML = taskTag(r.task.state);
      $('#tkBar').style.width = (r.task.state === 'done' ? 100 : r.task.prog) + '%';
      $('#tkMeta').textContent = `${r.task.id} · ${r.task.by} · ${fmtTime(r.task.start)}`;
      if (r.task.state === 'failed') $('#tkErr').innerHTML = `<div class="notice bad" style="margin-top:10px"><span>!</span><div>${esc(r.task.err)}</div></div>`;
      if (r.task.state === 'done' || r.task.state === 'failed' || r.task.state === 'canceled') {
        finished = true; clearInterval(TV.timer);
        if (onDone) onDone(r.task);
      } else if (onDone && onDone.progress) onDone.progress(r.task);
    } catch (e) { const el = $('#tkErr'); if (el) el.innerHTML = `<div class="notice warn" style="margin-top:10px"><span>!</span><div>读取任务失败：${esc(e.message)}</div></div>`; }
    finally { busy = false; }
  };
  tick(); TV.timer = setInterval(tick, 1000);
}

/* ---------- 节点工具 ---------- */
const nodeKeyOf = n => `${n.host}:${n.type === 'fe' ? n.editLog : n.port}`;
const nodePathOf = (n, op) => `${opsBase()}/nodes/${n.type}/${opsEnc(nodeKeyOf(n))}/${op}`;
const nodeById = id => NODES.find(n => n.id === id);
const nodeLabel = n => `${n.type.toUpperCase()} ${nodeKeyOf(n)}`;
const noSub = v => v == null ? '—' : v;
function nodeRefresh() { delete LIVE.topo[CUR.id]; delete OPS['node-bl']; if (OPS['node-tasks']) delete OPS['node-tasks'][CUR.id]; if (ROUTE.page === 'nodes') rerender(); }
ON.nodeRetry = () => nodeRefresh();

/* 与 liveRisky 相同，但弹窗里可以带额外的选项（优雅退出、强制等） */
function nodeRisky(pcode, title, detail, sql, extraHtml, run) {
  const needInput = SETTINGS.highRiskConfirm;
  openModal({title: '⚠ ' + title, okCls: 'danger primary', ok: SETTINGS.dualReview ? '提交复核' : '确认执行', code: pcode,
    body: `<div class="notice bad"><span>⚠</span><div>${detail}</div></div>${extraHtml || ''}
      ${sql ? `<div class="small muted">将执行：</div>${code(sql)}` : ''}
      ${SETTINGS.dualReview ? '<div class="notice warn"><span>👥</span><div>平台已开启<b>双人复核</b>：提交后需另一名拥有同权限的用户在「操作审计 → 待复核」中批准后才会执行。</div></div>' : ''}
      ${needInput ? `<div class="form" style="margin-top:8px"><label class="req">集群名确认</label><input id="riskInput" placeholder="请输入 ${esc(CUR.name)}"></div>` : ''}`,
    onOk() {
      if (needInput && $('#riskInput').value.trim() !== CUR.name) { toast('集群名不匹配'); return false; }
      run(); return false;
    }});
}

/* 提交一个会产生后台任务的节点操作：待复核则提示，否则打开任务日志 */
function nodeRunTask(n, op, body, title) {
  api('POST', nodePathOf(n, op), body || {}).then(res => {
    if (livePending(res)) return;
    closeModal();
    taskDrawer(res.taskId, title, () => nodeRefresh());
    nodeRefresh();
  }).catch(liveFail);
}

/* ---------- 页面 ---------- */
const _nodesDemo = {render: pages.nodes.render, list: pages.nodes.list, blacklist: pages.nodes.blacklist};
pages.nodes.render = function (sub) {
  if (!LIVE.on) return _nodesDemo.render.call(this, sub);
  sub = sub || 'fe';
  const t = LIVE.topo[CUR.id], err = LIVE.err['topo' + CUR.id];
  if (!t) { loadTopo(CUR.id); return err ? errCard('采集失败：' + err, 'nodeRetry') : loadingCard('正在读取 SHOW FRONTENDS / BACKENDS / COMPUTE NODES…'); }
  NODES = t.nodes;
  const sd = CUR.mode === 'shared-data';
  const cnt = k => NODES.filter(n => n.type === k).length;
  const hint = {blacklist: '黑名单中的 BE / CN 不参与查询调度（数据副本与导入不受影响）', tasks: '节点启停等操作在服务端后台执行，这里保留最近的任务与日志'}[sub] || '启动 / 停止 / 重启通过 SSH 执行安装目录下的脚本，下线 / 移除 / 黑名单通过 SQL 执行';
  return tabs('nodes', [['fe', `FE (${cnt('fe')})`], ...(sd ? [] : [['be', `BE (${cnt('be')})`]]), ['cn', `CN (${cnt('cn')})`], ['blacklist', '黑名单'], ['tasks', '操作记录']], sub) +
    `<div class="toolbar"><span class="small muted">采集于 ${fmtTime(t.collectedAt)} · ${esc(hint)}</span><span class="sp"></span><button class="btn sm" data-on="nodeRetry">⟳ 刷新</button>
    ${['fe', 'be', 'cn'].includes(sub) ? B('node.scaleout', '＋ 扩容 ' + sub.toUpperCase(), {cls: 'primary', data: {t: sub}}) : sub === 'blacklist' ? B('node.blacklist', '＋ 加入黑名单', {cls: 'primary'}) : ''}</div>` +
    ((t.warnings || []).length ? `<div class="notice warn"><span>!</span><div>${t.warnings.map(esc).join('；')}</div></div>` : '') +
    (sub === 'blacklist' ? this.blacklist() : sub === 'tasks' ? this.tasks() : this.list(sub));
};
pages.nodes.list = function (t) {
  if (!LIVE.on) return _nodesDemo.list.call(this, t);
  const list = NODES.filter(n => n.type === t);
  const extra = {fe: ['角色', 'JVM 堆', 'Journal 落后'], be: ['Tablet', '数据量', 'Compaction'], cn: ['Warehouse', '运行中查询', '—']}[t];
  const addr = n => `${esc(n.host)}:${t === 'fe' ? n.editLog : n.port}`;
  return `<div class="card"><div class="tbl-wrap"><table><thead><tr><th>节点</th><th>状态</th><th>版本</th><th>${extra[0]}</th><th>CPU</th><th>内存</th><th>磁盘</th><th>${extra[1]}</th><th>${extra[2]}</th><th>操作</th></tr></thead><tbody>
    ${list.map(n => `<tr><td><a data-on="node" data-id="${n.id}" class="mono">${addr(n)}</a><div class="small muted">${n.id}</div></td>
      <td>${stDot(n.status)}${n.status === 'down' && n.errMsg ? `<div class="small muted" style="max-width:180px" title="${esc(n.errMsg)}">${esc(n.errMsg.slice(0, 40))}</div>` : ''}${n.status === 'decommission' ? `<div class="small muted">迁移中，剩余 ${fmt(n.tablets || 0)} Tablet</div>` : ''}</td><td>${esc(n.version || '—')}</td>
      <td>${t === 'fe' ? `<span class="tag ${n.role === 'Leader' ? 'brand' : ''}">${esc(n.role)}</span>` : t === 'be' ? fmt(n.tablets || 0) : esc(n.warehouse || '—')}</td>
      <td style="width:90px">${meter(n.cpu)}</td><td style="width:90px">${meter(n.mem)}</td><td style="width:90px">${meter(n.disk, 75, 85)}</td>
      <td>${t === 'fe' ? (n.jvm == null ? '—' : n.jvm + '%') : t === 'be' ? (n.dataTotal ? `${n.dataUsed} / ${n.dataTotal} TB` : '—') : (n.runningQueries == null ? '—' : n.runningQueries)}</td>
      <td>${t === 'fe' ? Math.max(0, (n.journal || 0) - (n.replayed || 0)) : t === 'be' ? (n.compaction == null ? '—' : `<span class="${n.compaction > 100 ? 'up b' : ''}">${n.compaction}</span>`) : '—'}</td>
      <td class="ops">${n.status === 'down' ? B('node.start', '启动', {cls: 'sm', data: {id: n.id}}) : B('node.stop', '停止', {cls: 'sm', data: {id: n.id}})}${B('node.restart', '重启', {cls: 'sm', data: {id: n.id}})}
        ${t === 'fe' ? (n.role === 'Leader' ? '' : B('node.drop', '移除', {cls: 'sm danger', data: {id: n.id}})) : (t === 'be' ? B('node.decommission', '下线', {cls: 'sm danger', data: {id: n.id}}) + B('node.drop', '强制删除', {cls: 'sm danger', data: {id: n.id}}) : B('node.decommission', '下线', {cls: 'sm danger', data: {id: n.id}}))}
        ${t !== 'fe' ? B('node.blacklist', '拉黑', {cls: 'sm', data: {id: n.id}}) : ''}${B('node.log', '日志', {cls: 'sm', data: {id: n.id}})}</td></tr>`).join('') || '<tr><td colspan="10" class="muted">没有该类型节点</td></tr>'}
    </tbody></table></div></div>
    <div class="notice" style="margin-top:12px"><span>ℹ</span><div>${{fe: 'FE 扩容：新节点首次启动需带 <code>--helper &lt;leader_ip&gt;:edit_log_port</code>，再执行 <code>ALTER SYSTEM ADD FOLLOWER/OBSERVER</code>。移除 Follower 后需保证剩余 Follower 为奇数个。停止 Leader 会触发重新选主。',
      be: 'BE 缩容请使用 <b>DECOMMISSION</b>（先迁移 Tablet 副本再移除，安全）；<b>强制删除（DROP）</b>会直接移除节点，副本不足的数据可能丢失，仅在节点已不可恢复时使用。',
      cn: 'CN 无状态，可直接 DROP；存算分离下下线前建议先停止调度（加入黑名单）以等待运行中查询结束。'}[t]} ${docLink('administration/management/Scale_up_down/', '扩缩容 ↗')} ${docLink('administration/management/graceful_exit/', '优雅退出 ↗')}</div></div>`;
};
pages.nodes.blacklist = function () {
  if (!LIVE.on) return _nodesDemo.blacklist.call(this);
  const d = opsGet('node-bl', '/node-blacklist');
  if (!d) return opsLoading('node-bl', '正在读取 SHOW BACKEND / COMPUTE NODE BLACKLIST…', 'nodeRetry');
  return `${warnNotice(d.warnings)}<div class="card"><div class="tbl-wrap"><table><thead><tr><th>ID</th><th>节点</th><th>类型</th><th>原因</th><th>来源</th><th>操作人</th><th>时间</th><th>操作</th></tr></thead><tbody>
    ${d.items.map(b => `<tr><td>${esc(b.id)}</td><td class="mono">${esc(b.node || '—')}</td><td>${b.kind.toUpperCase()}</td><td>${esc(b.reason)}</td><td>${b.auto ? '自动（连接失败）' : '手动'}</td><td>${esc(b.by || '—')}</td><td class="small">${esc(fmtTime(b.at))}</td><td>${B('node.blacklist', '移出', {cls: 'sm', data: {rm: b.kind + ':' + b.id}})}</td></tr>`).join('') || '<tr><td colspan="8" class="muted">暂无</td></tr>'}
    </tbody></table></div>${code('SHOW BACKEND BLACKLIST;\nADD BACKEND BLACKLIST <be_id>;\nDELETE BACKEND BLACKLIST <be_id>;\n-- CN 使用 COMPUTE NODE 语句')}${docLink('administration/management/BE_blacklist/', 'BE / CN 黑名单 ↗')}</div>`;
};
pages.nodes.tasks = function () {
  OPS['node-tasks'] = OPS['node-tasks'] || {};
  const id = CUR.id, d = OPS['node-tasks'][id];
  if (!d) {
    liveLoad('ops-node-tasks', id, async () => { OPS['node-tasks'][id] = await api('GET', `/api/tasks?cluster=${opsEnc(id)}&kind=node.&limit=100`); });
    return opsLoading('node-tasks', '正在读取操作记录…', 'nodeRetry');
  }
  return `<div class="card"><div class="tbl-wrap"><table><thead><tr><th>任务</th><th>操作</th><th>状态</th><th>发起人</th><th>开始</th><th>耗时</th><th>操作</th></tr></thead><tbody>
    ${d.items.map(t => `<tr><td class="mono small">${esc(t.id)}</td><td>${esc(t.title)}</td><td>${taskTag(t.state)}${t.err ? `<div class="small up" style="max-width:260px">${esc(t.err)}</div>` : ''}</td><td>${esc(t.by)}</td><td class="small">${fmtTime(t.start)}</td>
      <td class="small">${t.state === 'running' ? '—' : Math.max(0, Math.round((new Date(t.end) - new Date(t.start)) / 1000)) + ' s'}</td><td><button class="btn sm" data-on="nodeTaskLog" data-id="${esc(t.id)}" data-t="${esc(t.title)}">日志</button></td></tr>`).join('') || '<tr><td colspan="7" class="muted">还没有通过平台执行过节点操作</td></tr>'}
    </tbody></table></div></div>`;
};
ON.nodeTaskLog = d => taskDrawer(d.id, d.t, () => {});

/* ---------- 操作 ---------- */
const _nodeAct = {start: ACTIONS['node.start'], stop: ACTIONS['node.stop'], restart: ACTIONS['node.restart'], decommission: ACTIONS['node.decommission'], drop: ACTIONS['node.drop'], blacklist: ACTIONS['node.blacklist'], log: ACTIONS['node.log'], scaleout: ACTIONS['node.scaleout']};
const gracefulDefault = n => n.type !== 'fe' && /^(\d+)\.(\d+)/.test(n.version || '') && (+RegExp.$1 > 3 || (+RegExp.$1 === 3 && +RegExp.$2 >= 4));
const stopOptions = n => `<div class="form" style="margin-top:8px">
  <label>优雅退出</label><label class="chk"><input type="checkbox" id="ndGr" ${gracefulDefault(n) ? 'checked' : ''}> 使用 <code>-g</code>：等待运行中的查询完成后再退出（3.4+ 推荐；较慢）</label>
  ${n.type === 'fe' ? '<label>强制</label><label class="chk"><input type="checkbox" id="ndForce"> 即使存活的 Leader / Follower 不足多数也继续（可能导致无法选主）</label>' : ''}</div>`;
const stopBody = n => ({graceful: $('#ndGr').checked, force: $('#ndForce') ? $('#ndForce').checked : false});
ACTIONS['node.start'] = d => {
  if (!LIVE.on) return _nodeAct.start(d);
  const n = nodeById(d.id); if (!n) return;
  nodeRunTask(n, 'start', {}, '启动 ' + nodeLabel(n));
};
ACTIONS['node.stop'] = d => {
  if (!LIVE.on) return _nodeAct.stop(d);
  const n = nodeById(d.id); if (!n) return;
  nodeRisky('node.stop', '停止 ' + nodeLabel(n), n.role === 'Leader' ? '停止 Leader 会触发 FE 重新选主，期间 DDL / 导入短暂不可用。' : n.type === 'be' ? '停止 BE 后其上副本不可用，持续超过 tablet_sched 阈值后会触发副本补齐。' : '停止后该节点不再提供服务。',
    `${esc(n.dir || '<部署目录>')}/bin/stop_${n.type}.sh`, stopOptions(n), () => nodeRunTask(n, 'stop', stopBody(n), '停止 ' + nodeLabel(n)));
};
ACTIONS['node.restart'] = d => {
  if (!LIVE.on) return _nodeAct.restart(d);
  const n = nodeById(d.id); if (!n) return;
  nodeRisky('node.restart', '重启 ' + nodeLabel(n), '重启过程中该节点上的查询会失败并由客户端重试；建议在低峰期并逐台执行。平台会等待节点重新上线后才视为成功。',
    `bin/stop_${n.type}.sh && bin/start_${n.type}.sh --daemon`, stopOptions(n), () => nodeRunTask(n, 'restart', stopBody(n), '重启 ' + nodeLabel(n)));
};
function nodeSqlOp(n, op, okMsg) {
  api('POST', nodePathOf(n, op), {}).then(res => {
    if (livePending(res)) return;
    closeModal(); toast(okMsg); nodeRefresh();
  }).catch(liveFail);
}
ACTIONS['node.decommission'] = d => {
  if (!LIVE.on) return _nodeAct.decommission(d);
  const n = nodeById(d.id); if (!n) return;
  const bes = NODES.filter(x => x.type === 'be'), avg = bes.length > 1 ? Math.min(99, Math.round(bes.reduce((a, b) => a + (b.disk || 0), 0) / (bes.length - 1))) : null;
  const sql = n.type === 'be' ? `ALTER SYSTEM DECOMMISSION BACKEND "${n.host}:${n.port}";` : `ALTER SYSTEM DROP COMPUTE NODE "${n.host}:${n.port}";`;
  liveRisky('node.decommission', '下线 ' + nodeLabel(n), n.type === 'be' ? `将迁移该节点上 ${fmt(n.tablets || 0)} 个 Tablet 副本到其他 BE，完成后自动移除。请确认剩余 BE 磁盘空间足够${avg != null ? `（迁移后预计平均使用率 ${avg}%）` : ''}。` : 'CN 无状态，将直接移除。', sql,
    () => nodeSqlOp(n, 'decommission', n.type === 'be' ? '已开始下线，Tablet 迁移完成后节点自动移除' : '已移除'));
};
ACTIONS['node.drop'] = d => {
  if (!LIVE.on) return _nodeAct.drop(d);
  const n = nodeById(d.id); if (!n) return;
  const sql = n.type === 'fe' ? `ALTER SYSTEM DROP ${n.role.toUpperCase()} "${n.host}:${n.editLog}";` : n.type === 'be' ? `ALTER SYSTEM DROP BACKEND "${n.host}:${n.port}";` : `ALTER SYSTEM DROP COMPUTE NODE "${n.host}:${n.port}";`;
  const voters = NODES.filter(x => x.type === 'fe' && x.role !== 'Observer').length;
  liveRisky('node.drop', '移除 ' + nodeLabel(n), n.type === 'fe' ? (n.role === 'Follower' ? `移除后 Follower / Leader 数为 ${voters - 1}，<b>请保证为奇数</b>，否则影响选主可用性。` : 'Observer 不参与选主，可安全移除。') : '直接移除节点，其上副本不足的数据可能丢失。仅在节点已不可恢复时使用，正常缩容请用“下线”。', sql,
    () => nodeSqlOp(n, 'drop', '已移除 ' + nodeLabel(n)));
};
ACTIONS['node.blacklist'] = d => {
  if (!LIVE.on) return _nodeAct.blacklist(d);
  if (d.rm) {
    const [kind, id] = d.rm.split(':');
    return api('DELETE', `${opsBase()}/node-blacklist/${kind}/${opsEnc(id)}`).then(() => { toast('已移出黑名单'); nodeRefresh(); }).catch(liveFail);
  }
  const bl = ((OPS['node-bl'] || {})[CUR.id] || {items: []}).items.map(b => b.node);
  const opts = NODES.filter(n => n.type !== 'fe' && !bl.includes(nodeKeyOf(n)));
  if (!opts.length) return toast('没有可加入黑名单的节点');
  openModal({title: '加入黑名单', code: 'node.blacklist', body: `<div class="form"><label class="req">节点</label><select id="blN">${opts.map(n => `<option value="${n.id}" ${n.id === d.id ? 'selected' : ''}>${n.type.toUpperCase()} ${esc(nodeKeyOf(n))}</option>`).join('')}</select>
    <label class="req">原因</label><input id="blR" maxlength="100" placeholder="例如：磁盘 IO 抖动"></div><div class="notice warn" style="margin-top:10px"><span>!</span><div>黑名单节点不参与查询调度，但仍承载数据副本与导入。</div></div>`,
    onOk() {
      const n = nodeById($('#blN').value), r = $('#blR').value.trim(); if (!r) { toast('请填写原因'); return false; }
      api('POST', opsBase() + '/node-blacklist', {kind: n.type, node: nodeKeyOf(n), reason: r}).then(() => { closeModal(); toast('已加入黑名单'); nodeRefresh(); }).catch(liveFail);
      return false;
    }});
};
ACTIONS['node.log'] = d => {
  if (!LIVE.on) return _nodeAct.log(d);
  const n = nodeById(d.id); if (!n) return;
  const files = {fe: ['fe.log', 'fe.warn.log', 'fe.audit.log', 'fe.out'], be: ['be.INFO', 'be.WARNING', 'be.out'], cn: ['cn.INFO', 'cn.WARNING', 'cn.out']}[n.type];
  openDrawer(`${n.host} · ${n.type.toUpperCase()} 日志`, `<div class="toolbar"><select id="lgF">${files.map(f => `<option>${f}</option>`).join('')}</select><input type="search" id="lgQ" placeholder="关键字（在最近 5000 行中过滤）"><select id="lgN"><option>200</option><option>500</option><option>1000</option></select><button class="btn sm" id="lgGo">⟳ 读取</button><span class="small muted" id="lgInfo"></span></div>
    <div class="log" id="lgBody" style="height:62vh">读取中…</div>`, true);
  const load = () => {
    $('#lgBody').textContent = '读取中…';
    api('GET', `${nodePathOf(n, 'log')}?file=${opsEnc($('#lgF').value)}&lines=${$('#lgN').value}&q=${opsEnc($('#lgQ').value.trim())}`).then(r => {
      $('#lgBody').textContent = r.text || '（没有匹配的内容）'; $('#lgBody').scrollTop = 1e9; $('#lgInfo').textContent = `${r.lines} 行 · ${r.path}`;
    }).catch(e => { $('#lgBody').textContent = '读取失败：' + e.message; });
  };
  $('#lgGo').onclick = load; $('#lgF').onchange = load; $('#lgQ').onkeydown = e => { if (e.key === 'Enter') load(); };
  load();
};
/* 扩容在“安装部署”的主机与安装包就绪后接入（见 live_deploy.js） */

LIVE_PAGES.push('nodes');
