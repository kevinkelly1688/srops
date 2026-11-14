// @author: kevinkelly
// @license: Apache Licence
// @file: live_upgrade.js
// @time: 2026/10/28

/* ============================================================================
   真实模式：升级回滚（滚动升级、暂停 / 继续、回滚；升级前检查）
   顺序：BE → CN → FE（Observer → Follower → Leader）；回滚相反。服务端逐台执行并等待节点以新版本存活、Tablet 健康。
   ========================================================================== */
const UPL = {sel: '', checks: null, checking: false, timer: 0};
const UP_ST = {pending: ['', '待执行'], running: ['info', '升级中'], paused: ['warn', '已暂停'], done: ['ok', '已完成'], failed: ['bad', '失败'], rollingback: ['info', '回滚中'], rolledback: ['warn', '已回滚']};
const upTag = s => { const t = UP_ST[s] || ['', s]; return `<span class="tag ${t[0]}">${t[1]}</span>`; };
const upActive = d => (d.plans || []).find(p => ['running', 'paused', 'rollingback'].includes(p.state));
const upSelected = d => (d.plans || []).find(p => p.id === UPL.sel) || upActive(d) || (d.plans || []).find(p => p.state !== 'rolledback') || (d.plans || [])[0];
const upReload = () => { delete (OPS.upgrade || {})[CUR.id]; UPL.checks = null; if (ROUTE.page === 'upgrade') rerender(); };
ON.upRetry = () => upReload();

/* 执行中的计划：每 3 秒刷新一次，结束后自动停止 */
function upWatch(d) {
  const act = d && upActive(d);
  if (act && !UPL.timer) {
    UPL.timer = setInterval(() => {
      if (ROUTE.page !== 'upgrade' || !LIVE.on) { clearInterval(UPL.timer); UPL.timer = 0; return; }
      api('GET', `${opsBase()}/upgrade`).then(r => { OPS.upgrade = OPS.upgrade || {}; OPS.upgrade[CUR.id] = r; if (!upActive(r)) { clearInterval(UPL.timer); UPL.timer = 0; delete LIVE.topo[CUR.id]; } if (ROUTE.page === 'upgrade' && !$('#modal').offsetParent && !$('#drawer').classList.contains('open')) rerender(); }).catch(() => {});
    }, 3000);
  } else if (!act && UPL.timer) { clearInterval(UPL.timer); UPL.timer = 0; }
}

const _upgradeDemo = {render: pages.upgrade.render};
pages.upgrade.render = function () {
  if (!LIVE.on) return _upgradeDemo.render.call(this);
  const d = opsGet('upgrade', '/upgrade');
  if (!d) return opsLoading('upgrade', '正在读取节点版本与升级计划…', 'upRetry');
  upWatch(d);
  const plan = upSelected(d), act = upActive(d);
  const nst = n => {
    if (!plan) return '<span class="tag">—</span>';
    const to = plan.to, from = plan.from, rolling = plan.state === 'rollingback';
    if (plan.cur === n.key && (plan.state === 'running' || plan.state === 'paused' || rolling)) return `<span class="tag info">${plan.state === 'paused' ? '已暂停' : rolling ? '回滚中…' : '升级中…'}</span>`;
    if (n.version === to) return '<span class="tag ok">已是 ' + esc(to) + '</span>';
    if (n.version === from) return '<span class="tag">待升级</span>';
    return `<span class="tag warn">${esc(n.version || '?')}</span>`;
  };
  const canRun = plan && ['pending', 'failed', 'paused', 'rolledback'].includes(plan.state) && !act;
  const canRollback = plan && ['done', 'failed', 'paused'].includes(plan.state) && !act && d.nodes.some(n => n.version === plan.to);
  return `<div class="grid g-main"><div class="card"><h2>${esc(CUR.name)} 当前版本 <span class="tag brand">${esc(d.version || CUR.version)}</span><span class="right">${B('upgrade.create', '＋ 创建升级计划', {cls: 'primary sm'})}</span></h2>
      ${warnNotice(d.warnings)}<div class="notice"><span>ℹ</span><div><b>升级顺序</b>：先 BE → CN → 最后 FE（FE 内按 Observer → Follower → Leader）。<b>回滚（降级）顺序相反</b>：先 FE 再 CN / BE。同一大版本内须逐个小版本升级（如 3.3 → 3.4 → 3.5）。每个节点：优雅停止 → 备份旧 bin / lib → 换新版本 → 启动 → 等待以新版本存活 → 等待 Tablet 健康。${docLink('deployment/upgrade/', '升级 ↗')} ${docLink('deployment/downgrade/', '降级 ↗')}</div></div>
      ${plan ? `<div class="row between" style="margin:8px 0"><span>当前计划 <b class="mono">${esc(plan.id)}</b> ${upTag(plan.state)} <span class="small muted">${esc(plan.from)} → ${esc(plan.to)} · ${esc(plan.package)}</span></span><span class="small muted">${plan.note ? '<span class="up">' + esc(plan.note) + '</span>' : ''}</span></div>` : ''}
      <div class="tbl-wrap"><table><thead><tr><th>#</th><th>节点</th><th>角色</th><th>当前版本</th><th>存活</th><th>状态</th></tr></thead><tbody>
      ${d.nodes.map(n => `<tr><td>${n.order}</td><td class="mono">${esc(n.key.split('|')[1] || n.host)}</td><td>${n.type.toUpperCase()}${n.role ? ' · ' + esc(n.role) : ''}</td><td>${esc(n.version || '—')}</td><td>${n.alive ? '<span class="tag ok">是</span>' : '<span class="tag bad">否</span>'}</td><td>${nst(n)}</td></tr>`).join('')}
      </tbody></table></div>
      <div class="row" style="margin-top:12px;gap:8px;flex-wrap:wrap">
        ${plan ? `<button class="btn" data-on="upCheck" ${UPL.checking ? 'disabled' : ''}>${UPL.checking ? '检查中…' : '☰ 升级前检查'}</button>` : ''}
        ${plan ? B('upgrade.execute', canRun ? `▶ ${plan.state === 'pending' ? '执行' : '继续执行'} ${plan.from} → ${plan.to}` : (act ? '升级进行中…' : '▶ 执行'), {cls: 'primary', data: {id: plan.id}}) : '<span class="small muted">没有升级计划，请先创建</span>'}
        ${act ? B('upgrade.pause', d.paused ? '继续' : '暂停', {data: {id: act.id}}) + B('upgrade.execute', '取消', {cls: 'danger', data: {cancel: act.taskId}}) + `<button class="btn" data-on="upLog" data-id="${esc(act.taskId)}">查看日志</button>` : (plan && plan.taskId ? `<button class="btn" data-on="upLog" data-id="${esc(plan.taskId)}">上次任务日志</button>` : '')}
        ${plan ? B('upgrade.rollback', '回滚 / 降级', {cls: 'danger', data: {id: plan.id}}) : ''}
        <button class="btn" data-on="upRetry">⟳ 刷新</button></div></div>
      <div class="card"><h2>升级前检查</h2>${UPL.checks ? upChecksHtml(UPL.checks) : '<div class="muted small" style="padding:6px 0">选择计划后点击「升级前检查」。执行升级时平台也会自动检查，存在「不通过」项会拒绝开始。</div>'}
        ${code('ADMIN SET FRONTEND CONFIG ("tablet_sched_disable_balance" = "true");\n-- 创建计划时勾选「暂停 Tablet 均衡」，平台会在升级期间设置，结束后恢复')}</div></div>
    <div class="card" style="margin-top:16px"><h2>升级计划</h2><div class="tbl-wrap"><table><thead><tr><th>编号</th><th>版本</th><th>状态</th><th>已处理</th><th>每台间隔</th><th>创建</th><th>操作</th></tr></thead><tbody>
      ${d.plans.map(p => `<tr class="${plan && p.id === plan.id ? 'sel' : ''}"><td class="mono small">${esc(p.id)}</td><td>${esc(p.from)} → ${esc(p.to)}<div class="small muted">${esc(p.package)}</div></td><td>${upTag(p.state)}${p.note ? `<div class="small up" style="max-width:280px">${esc(p.note)}</div>` : ''}</td><td>${p.done.length} / ${d.nodes.length}</td><td>${p.interval} s${p.disableBalance ? ' · 暂停均衡' : ''}</td><td class="small">${esc(p.by)} · ${fmtTime(p.at)}</td>
        <td class="ops"><button class="btn sm" data-on="upSel" data-id="${esc(p.id)}">选择</button>${p.taskId ? `<button class="btn sm" data-on="upLog" data-id="${esc(p.taskId)}">日志</button>` : ''}${p.state === 'pending' ? B('upgrade.create', '删除', {cls: 'sm danger', data: {del: p.id}}) : ''}</td></tr>`).join('') || '<tr><td colspan="7" class="muted">还没有升级计划</td></tr>'}
    </tbody></table></div></div>`;
};
function upChecksHtml(r) {
  const tag = st => ({ok: '<span class="tag ok">通过</span>', warn: '<span class="tag warn">警告</span>', bad: '<span class="tag bad">不通过</span>', info: '<span class="tag info">提示</span>'}[st] || st);
  return `<div class="notice ${r.bad ? 'bad' : 'ok'}" style="margin-bottom:6px"><span>${r.bad ? '!' : '✓'}</span><div>${r.bad ? `${r.bad} 项不通过，需处理后才能升级` : '没有不通过项，可以升级'}</div></div>` +
    r.items.map(it => `<div class="row between small" style="padding:6px 0;border-bottom:1px solid var(--border);gap:8px"><span style="flex:1">${esc(it.item)}${it.val ? `<div class="muted" style="word-break:break-all">${esc(it.val)}</div>` : ''}</span>${tag(it.st)}</div>`).join('');
}
ON.upSel = d => { UPL.sel = d.id; UPL.checks = null; rerender(); };
ON.upLog = d => taskDrawer(d.id, '升级回滚任务', () => upReload());
ON.upCheck = () => {
  const d = OPS.upgrade && OPS.upgrade[CUR.id], plan = d && upSelected(d); if (!plan) return;
  UPL.checking = true; rerender();
  api('POST', `${opsBase()}/upgrade/plans/${opsEnc(plan.id)}/precheck`, {}).then(r => { UPL.checks = r; }).catch(liveFail).finally(() => { UPL.checking = false; rerender(); });
};

const _upAct = {create: ACTIONS['upgrade.create'], execute: ACTIONS['upgrade.execute'], pause: ACTIONS['upgrade.pause'], rollback: ACTIONS['upgrade.rollback']};
ACTIONS['upgrade.create'] = d => {
  if (!LIVE.on) return _upAct.create(d);
  const ov = OPS.upgrade && OPS.upgrade[CUR.id];
  if (d && d.del) return confirmTyped('删除升级计划', '将删除尚未执行的计划 ' + esc(d.del) + '。', d.del.slice(-6), '确认删除', () =>
    api('DELETE', `${opsBase()}/upgrade/plans/${opsEnc(d.del)}`).then(() => { closeModal(); toast('已删除'); upReload(); }).catch(liveFail), 'upgrade.create');
  api('GET', '/api/packages').then(r => {
    const cur = (ov && ov.version) || CUR.version, pk = r.items.filter(p => p.kind === 'starrocks' && p.ver !== cur);
    openModal({title: '创建升级计划', code: 'upgrade.create', ok: '创建', body: `<div class="form"><label>集群</label><input value="${esc(CUR.name)}（当前 ${esc(cur)}）" disabled>
      <label class="req">目标版本</label><select id="upPkg">${pk.map(p => `<option value="${esc(p.name)}">${esc(p.ver)} · ${esc(p.name)}</option>`).join('') || '<option value="">（没有其他版本的安装包，请先在「安装部署 → 安装包」上传）</option>'}</select>
      <label>每台间隔</label><div class="row"><input id="upIv" type="number" min="0" max="3600" value="60" style="width:90px"> 秒（每个节点升级完成并 Tablet 健康后再等待）</div>
      <label>Tablet 均衡</label><label class="chk"><input type="checkbox" id="upBal" checked> 升级期间暂停 Tablet 均衡（<code>tablet_sched_disable_balance</code>），结束后恢复</label></div>
      <div class="notice warn" style="margin-top:10px"><span>!</span><div>同一大版本内不能跨小版本（3.3 → 3.5 须先升到 3.4）；不能选择比当前更低的版本——降级请用「回滚」。创建后先做「升级前检查」。</div></div>`,
      onOk() {
        const pkg = $('#upPkg').value; if (!pkg) { toast('请选择目标版本'); return false; }
        api('POST', `${opsBase()}/upgrade/plans`, {package: pkg, interval: +$('#upIv').value || 0, disableBalance: $('#upBal').checked}).then(p => { closeModal(); toast('已创建计划 ' + p.id); UPL.sel = p.id; upReload(); }).catch(liveFail);
        return false;
      }});
  }).catch(liveFail);
};
function upRun(rollback, id) {
  const ov = OPS.upgrade && OPS.upgrade[CUR.id], plan = ov && (ov.plans.find(p => p.id === id) || upSelected(ov)); if (!plan) return;
  const fn = (path, body) => api('POST', `${opsBase()}/upgrade/plans/${opsEnc(plan.id)}/${path}`, body || {}).then(res => {
    if (livePending(res)) return;
    closeModal(); upReload(); taskDrawer(res.taskId, rollback ? '回滚 ' + plan.id : '滚动升级 ' + plan.id, () => { upReload(); delete LIVE.topo[CUR.id]; });
  }).catch(liveFail);
  if (rollback) liveRisky('upgrade.rollback', `回滚 ${CUR.name}：${plan.to} → ${plan.from}`, `降级顺序：先 FE（Leader 最后）再 CN / BE，把每个节点换回升级前备份的 bin / lib。<br>仅支持回到升级前的版本；<b>如果升级后使用过新版本的新特性，旧版本可能无法识别新产生的元数据 / 数据</b>。已经是 ${esc(plan.from)} 的节点会被跳过。`, '', () => fn('rollback'));
  else liveRisky('upgrade.execute', `滚动升级 ${CUR.name}：${plan.from} → ${plan.to}`, `将按 BE → CN → FE 的顺序逐台重启 <b>${(ov.nodes || []).length}</b> 个节点；每台升级期间该节点不可用，查询可能有短暂失败重试。FE 升级前会备份元数据目录。<br>已处理的节点（再次执行时）会被跳过；升级前检查有「不通过」项时平台会拒绝开始。`, '', () => fn('execute'));
}
ACTIONS['upgrade.execute'] = d => {
  if (!LIVE.on) return _upAct.execute(d);
  if (d.cancel) return confirmTyped('取消升级', '将在<b>当前节点处理完成后</b>停止，已处理的节点保持新版本，计划标记为失败（备注“已取消”），之后可以继续执行或回滚。', CUR.name, '确认取消', () =>
    api('POST', `/api/tasks/${opsEnc(d.cancel)}/cancel`, {}).then(() => { closeModal(); toast('已请求取消，当前节点完成后停止'); upReload(); }).catch(liveFail), 'upgrade.execute');
  upRun(false, d.id);
};
ACTIONS['upgrade.rollback'] = d => { if (!LIVE.on) return _upAct.rollback(d); upRun(true, d && d.id); };
ACTIONS['upgrade.pause'] = d => {
  if (!LIVE.on) return _upAct.pause(d);
  const ov = OPS.upgrade && OPS.upgrade[CUR.id], act = ov && upActive(ov); if (!act) return;
  api('POST', `${opsBase()}/upgrade/plans/${opsEnc(act.id)}/pause`, {paused: !ov.paused}).then(r => { toast(r.paused ? '已请求暂停：当前节点完成后暂停' : '已继续'); upReload(); }).catch(liveFail);
};

LIVE_PAGES.push('upgrade');
