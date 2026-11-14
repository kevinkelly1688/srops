// @author: kevinkelly
// @license: Apache Licence
// @file: live_alert.js
// @time: 2026/10/28

/* ============================================================================
   真实模式：告警中心（当前告警 / 告警规则 / 静默 / 通知渠道）。依赖 live.js、live_ops.js。
   平台每分钟评估一次内置规则；本文件负责展示与操作，并让侧栏角标、总览「活跃告警」使用真实告警。
   ========================================================================== */
const AL = {only: false, history: false, hist: [], rules: null, sil: null, ch: null, err: '', at: 0, timer: 0};
const lvTag = lv => `<span class="tag ${sevCls[lv] || ''}">${sevText[lv] || lv}</span>`;
const lvList = ['critical', 'warning', 'info'];
const ago = t => { const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000); return s < 90 ? '刚刚' : s < 3600 ? Math.round(s / 60) + ' 分钟' : s < 172800 ? (s / 3600).toFixed(1) + ' 小时' : Math.round(s / 86400) + ' 天'; };

/* ----- 数据加载 ----- */
function alRefreshActive(quiet) {
  if (!LIVE.on || !ME || !can('alert:view')) return Promise.resolve();
  return api('GET', '/api/alerts').then(r => {
    ALERTS.length = 0; r.items.forEach(a => ALERTS.push({...a, lv: a.level, inst: a.inst, time: fmtTime(a.firstAt).slice(5, 16), state: a.silenced && a.state === 'firing' ? 'silenced' : a.state}));
    AL.at = Date.now(); AL.err = '';
    buildNav();
    if (ROUTE.page === 'alert' && (ROUTE.sub || 'active') === 'active' && !AL.history && !quiet) rerender();
    else if (ROUTE.page === 'overview') rerender();
  }).catch(e => { AL.err = e.message; });
}
function alLoad(key, path, set) {
  if (AL[key]) return AL[key];
  liveLoad('al-' + key, CUR ? CUR.id : 'x', async () => { AL[key] = await api('GET', path); if (set) set(AL[key]); });
  return null;
}
const alDrop = (...k) => { k.forEach(x => AL[x] = null); };
const alErrKey = key => 'al-' + key + (CUR ? CUR.id : 'x');
const alWait = (key, msg) => LIVE.err[alErrKey(key)] ? errCard(LIVE.err[alErrKey(key)], 'alRetry') : loadingCard(msg);
ON.alRetry = () => { alDrop('rules', 'sil', 'ch', 'custom'); ['rules', 'sil', 'ch', 'custom'].forEach(k => delete LIVE.err[alErrKey(k)]); AL.hist = null; alRefreshActive(); rerender(); };
function alStart() {
  clearInterval(AL.timer);
  AL.timer = setInterval(() => { if (LIVE.on && $('#login').hidden) alRefreshActive(true).then(() => { if (ROUTE.page === 'alert' && (ROUTE.sub || 'active') === 'active') rerender(); }); }, 30000);
}

/* ============================ 页面 ============================ */
const _alertDemo = {render: pages.alert.render, active: pages.alert.active, rules: pages.alert.rules, silence: pages.alert.silence, channels: pages.alert.channels};
pages.alert.render = function (sub) {
  if (!LIVE.on) return _alertDemo.render.call(this, sub);
  sub = sub || 'active';
  const n = ALERTS.filter(a => a.state !== 'resolved').length;
  return tabs('alert', [['active', `当前告警 (${n})`], ['rules', '告警规则'], ['silence', '静默'], ['channels', '通知渠道']], sub) + this[sub]();
};
pages.alert.active = function () {
  if (!LIVE.on) return _alertDemo.active.call(this);
  let list;
  if (AL.history) {
    if (!AL.hist) { api('GET', '/api/alerts?history=1').then(r => { AL.hist = r.items; rerender(); }).catch(e => { AL.hist = []; toast(e.message); }); return loadingCard('正在读取历史告警…'); }
    list = AL.hist;
  } else list = ALERTS;
  if (AL.only && CUR) list = list.filter(a => a.cluster === CUR.id);
  const multi = CLUSTERS.length > 1;
  const stText = a => a.state === 'resolved' ? '<span class="tag ok">已恢复</span>' : a.silenced ? '<span class="tag">已静默</span>' : a.state === 'acked' ? `<span class="tag warn">处理中 · ${esc(a.ackBy)}</span>` : '<span class="tag bad">告警中</span>';
  return `<div class="toolbar"><div class="seg"><button class="btn sm ${AL.history ? '' : 'primary'}" data-on="alHist" data-h="0">当前</button><button class="btn sm ${AL.history ? 'primary' : ''}" data-on="alHist" data-h="1">历史（7 天内已恢复）</button></div>
      ${multi ? `<label class="chk"><input type="checkbox" id="alOnly" ${AL.only ? 'checked' : ''}> 只看当前集群</label>` : ''}<button class="btn sm" data-on="alRefresh">⟳ 刷新</button><span class="sp"></span>
      <span class="small muted">平台每分钟评估一次内置规则${AL.at ? '，上次读取 ' + new Date(AL.at).toLocaleTimeString() : ''}</span></div>
    ${AL.err ? `<div class="notice bad"><span>!</span><div>读取告警失败：${esc(AL.err)}</div></div>` : ''}
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>级别</th><th>告警</th>${multi ? '<th>集群</th>' : ''}<th>对象</th><th>触发</th><th>${AL.history ? '恢复' : '持续'}</th><th>说明</th><th>状态</th><th>通知</th><th>操作</th></tr></thead><tbody>
    ${list.map(a => `<tr><td>${lvTag(a.level)}</td><td><b>${esc(a.name)}</b></td>${multi ? `<td class="small">${esc(a.clusterName || a.cluster)}</td>` : ''}<td class="small mono">${esc(a.inst)}</td><td class="small">${fmtTime(a.firstAt).slice(5, 16)}</td><td class="small">${AL.history ? fmtTime(a.resolvedAt).slice(5, 16) : ago(a.firstAt)}</td>
      <td class="small" style="max-width:340px">${esc(a.desc)}</td><td>${stText(a)}</td>
      <td class="small">${a.notifyErr ? `<span class="up" title="${esc(a.notifyErr)}">发送失败</span>` : a.notified && !String(a.notified).startsWith('0001') ? `已通知 ${a.notifyN || 1} 次<div class="muted">${fmtTime(a.notified).slice(5, 16)}</div>` : '<span class="muted">未通知</span>'}</td>
      <td class="ops">${!AL.history && a.state === 'firing' ? B('alert.ack', '认领', {cls: 'sm', data: {id: a.id}}) : ''}${!AL.history ? B('alert.silence', '静默', {cls: 'sm', data: {fa: a.id}}) : ''}</td></tr>`).join('') || `<tr><td colspan="${multi ? 10 : 9}" class="muted">${AL.history ? '最近 7 天没有已恢复的告警' : '✓ 当前没有告警'}</td></tr>`}
    </tbody></table></div></div>`;
};
ON.alHist = d => { AL.history = d.h === '1'; AL.hist = null; rerender(); };
ON.alRefresh = () => { AL.hist = null; alRefreshActive().then(() => rerender()); };
document.addEventListener('change', e => { if (e.target.id === 'alOnly') { AL.only = e.target.checked; rerender(); } });

pages.alert.rules = function () {
  if (!LIVE.on) return _alertDemo.rules.call(this);
  const rules = alLoad('rules', '/api/alert-rules'); alLoad('ch', '/api/alert-channels'); const custom = alLoad('custom', '/api/alert-rules/custom');
  if (!rules) return alWait('rules', '正在读取告警规则…');
  const chName = id => ((AL.ch || []).find(c => c.id === id) || {name: id}).name;
  const chText = r => (r.channels || []).length ? r.channels.map(c => esc(chName(c))).join('、') : '<span class="muted">全部启用的渠道</span>';
  const cn = id => id ? ((CLUSTERS.find(c => c.id === id) || {}).name || id) : '所有配置了 Prometheus 的集群';
  return `<div class="toolbar"><span class="small muted">平台内置 ${rules.length} 条规则，直接读取集群总览数据评估（不依赖 Prometheus）。阈值、持续时间、通知渠道可调，对<b>所有集群</b>生效；需要监控其他指标时，在下方新建 PromQL 自定义规则。</span></div>
    <div class="card"><h2>内置规则</h2><div class="tbl-wrap"><table><thead><tr><th>规则</th><th>对象</th><th>说明</th><th>警告</th><th>严重</th><th>持续</th><th>通知渠道</th><th>状态</th><th>操作</th></tr></thead><tbody>
    ${rules.map(r => `<tr><td><b>${esc(r.name)}</b>${r.custom ? '<div class="small muted">已自定义</div>' : ''}</td><td class="small">${esc(r.scope)}</td><td class="small" style="max-width:300px">${esc(r.desc)}</td><td>${r.hasWarn ? `≥ ${r.warn}${esc(r.unit)}` : '—'}</td><td>${r.id === 'node_down' || r.id === 'cluster_unreachable' ? '发生即告警' : `≥ ${r.crit}${esc(r.unit)}`}</td><td>${r.durMin ? r.durMin + ' 分钟' : '立即'}</td>
      <td class="small">${chText(r)}</td><td>${r.enabled ? '<span class="tag ok">启用</span>' : '<span class="tag">停用</span>'}</td>
      <td class="ops">${B('alert.rule', '编辑', {cls: 'sm', data: {id: r.id}})}${B('alert.rule', r.enabled ? '停用' : '启用', {cls: 'sm', data: {tg: r.id}})}${r.custom ? B('alert.rule', '恢复默认', {cls: 'sm', data: {reset: r.id}}) : ''}</td></tr>`).join('')}
    </tbody></table></div></div>
    <div class="card"><h2>自定义规则（PromQL） <span class="sub">${custom ? custom.length : '…'} 条</span><span class="right">${B('alert.rule', '＋ 新建自定义规则', {cls: 'sm primary', data: {cnew: '1'}})}</span></h2>
    <div class="small muted" style="margin-bottom:8px">平台每分钟对配置了 Prometheus 的集群执行一次即时查询，<b>返回的每条序列就是一个告警对象</b>，按“比较方式 + 阈值”判级别。表达式里的 <span class="mono">$sel</span> 会替换为集群的 Prometheus 选择器（如 job="StarRocks_prod"）。</div>
    ${!custom ? alWait('custom', '正在读取自定义规则…') : `<div class="tbl-wrap"><table><thead><tr><th>规则</th><th>PromQL</th><th>适用集群</th><th>条件</th><th>持续</th><th>通知渠道</th><th>最近评估</th><th>状态</th><th>操作</th></tr></thead><tbody>
    ${custom.map(r => `<tr><td><b>${esc(r.name)}</b>${r.desc ? `<div class="small muted" style="max-width:200px">${esc(r.desc)}</div>` : ''}</td><td class="mono small" style="max-width:320px;word-break:break-all">${esc(r.expr)}</td><td class="small">${esc(cn(r.cluster))}</td>
      <td class="small">${r.warn != null ? `警告 ${opSym(r.op)} ${r.warn}${esc(r.unit || '')}<br>` : ''}严重 ${opSym(r.op)} ${r.crit}${esc(r.unit || '')}</td><td>${r.durMin ? r.durMin + ' 分钟' : '立即'}</td><td class="small">${chText(r)}</td>
      <td class="small" style="max-width:220px">${(r.status || []).length ? r.status.map(x => `<div class="${x.includes('失败') ? 'up' : 'muted'}">${esc(x)}</div>`).join('') : '<span class="muted">尚未评估</span>'}</td><td>${r.enabled ? '<span class="tag ok">启用</span>' : '<span class="tag">停用</span>'}</td>
      <td class="ops">${B('alert.rule', '编辑', {cls: 'sm', data: {cid: r.id}})}${B('alert.rule', r.enabled ? '停用' : '启用', {cls: 'sm', data: {ctg: r.id}})}${B('alert.rule', '删除', {cls: 'sm danger', data: {cdel: r.id, n: r.name}})}</td></tr>`).join('') || '<tr><td colspan="9" class="muted">还没有自定义规则</td></tr>'}
    </tbody></table></div>`}</div>`;
};
const opSym = op => ({'>=': '≥', '>': '>', '<=': '≤', '<': '<'})[op] || op;
pages.alert.silence = function () {
  if (!LIVE.on) return _alertDemo.silence.call(this);
  const sil = alLoad('sil', '/api/silences'); alLoad('rules', '/api/alert-rules'); alLoad('custom', '/api/alert-rules/custom');
  if (!sil) return alWait('sil', '正在读取静默规则…');
  const cn = id => id ? ((CLUSTERS.find(c => c.id === id) || {}).name || id) : '全部集群';
  return `<div class="notice"><span>ℹ</span><div>被静默的告警<b>不再发送通知</b>（仍在列表中显示为“已静默”）。静默到期后自动失效；恢复通知同样不发送。</div></div>
    <div class="card"><h2>静默规则 <span class="sub">${sil.length} 条有效</span><span class="right">${B('alert.silence', '＋ 新建静默', {cls: 'sm primary'})}</span></h2><div class="tbl-wrap"><table><thead><tr><th>匹配条件</th><th>原因</th><th>创建人</th><th>截止</th><th>操作</th></tr></thead><tbody>
    ${sil.map(s => `<tr><td class="small">集群：${esc(cn(s.cluster))}；规则：${s.rule ? esc((AL.rules || []).concat(AL.custom || []).find(r => r.id === s.rule)?.name || s.rule) : '全部'}；对象：${s.inst ? '包含 ' + esc(s.inst) : '全部'}</td><td>${esc(s.reason)}</td><td>${esc(s.by)}</td><td class="small">${fmtTime(s.until)}</td><td>${B('alert.silence', '解除', {cls: 'sm', data: {rm: s.id}})}</td></tr>`).join('') || '<tr><td colspan="5" class="muted">暂无</td></tr>'}
    </tbody></table></div></div>`;
};
pages.alert.channels = function () {
  if (!LIVE.on) return _alertDemo.channels.call(this);
  const ch = alLoad('ch', '/api/alert-channels', list => { CHANNELS.length = 0; list.forEach(c => CHANNELS.push(c)); });
  if (!ch) return alWait('ch', '正在读取通知渠道…');
  return `<div class="notice"><span>ℹ</span><div>支持企业微信、钉钉、飞书、Lark、Slack、通用 Webhook、邮件（经「平台设置 → 通知」中的 SMTP 发送）与短信（经「平台设置 → 通知 → 短信网关」发送）。发送策略（合并、重复提醒、恢复通知、免打扰）在「平台设置 → 通知」配置。</div></div>
    <div class="card"><h2>通知渠道 <span class="right">${B('alert.channel', '＋ 新建渠道', {cls: 'sm primary'})}</span></h2><div class="tbl-wrap"><table><thead><tr><th>类型</th><th>名称</th><th>地址</th><th>接收级别</th><th>状态</th><th>最近发送</th><th>操作</th></tr></thead><tbody>
    ${ch.map(c => `<tr><td>${esc(c.type)}</td><td><b>${esc(c.name)}</b></td><td class="mono small">${esc(c.url)}${c.hasSecret ? ' <span class="tag ok">签名校验</span>' : ''}</td><td>${c.levels.map(l => lvTag(l)).join(' ')}</td><td>${c.enabled ? '<span class="tag ok">启用</span>' : '<span class="tag">停用</span>'}</td>
      <td class="small">${c.lastAt && !String(c.lastAt).startsWith('0001') ? `${fmtTime(c.lastAt).slice(5, 16)}<div class="${c.lastResult === '成功' ? 'muted' : 'up'}" style="max-width:220px">${esc(c.lastResult)}</div>` : '—'}</td>
      <td class="ops">${B('alert.channel', '测试', {cls: 'sm', data: {test: c.id}})}${B('alert.channel', '编辑', {cls: 'sm', data: {id: c.id}})}${B('alert.channel', '删除', {cls: 'sm danger', data: {del: c.id, n: c.name}})}</td></tr>`).join('') || '<tr><td colspan="7" class="muted">还没有通知渠道：告警只会显示在平台里，不会通知任何人</td></tr>'}
    </tbody></table></div></div>`;
};
pages.alert.init = function () {
  if (LIVE.on && ROUTE.page === 'alert' && (ROUTE.sub || 'active') === 'active' && Date.now() - AL.at > 10000) alRefreshActive(false);   // 超过 10 秒未更新才重新读取，读取完成后重绘一次
};

/* ============================ 操作 ============================ */
const _al = {ack: ACTIONS['alert.ack'], silence: ACTIONS['alert.silence'], rule: ACTIONS['alert.rule'], channel: ACTIONS['alert.channel']};
const alDone = (msg, ...k) => r => { closeModal(); toast(msg); alDrop(...k); rerender(); alRefreshActive(true).then(() => rerender()); return r; };
ACTIONS['alert.ack'] = d => {
  if (!LIVE.on) return _al.ack(d);
  api('POST', `/api/alerts/${opsEnc(d.id)}/ack`).then(alDone('已认领', 'hist')).catch(liveFail);
};
ACTIONS['alert.silence'] = async d => {
  if (!LIVE.on) return _al.silence(d);
  if (d.rm) return api('DELETE', '/api/silences/' + opsEnc(d.rm)).then(alDone('已解除静默', 'sil')).catch(liveFail);
  const a = d.fa ? ALERTS.find(x => x.id === d.fa) : null;
  if (!AL.rules) { try { AL.rules = await api('GET', '/api/alert-rules'); } catch (e) { return liveFail(e); } }
  if (!AL.custom) { try { AL.custom = await api('GET', '/api/alert-rules/custom'); } catch (e) { return liveFail(e); } }
  const rules = AL.rules.concat(AL.custom.map(c => ({id: c.id, name: c.name + '（自定义）'})));
  const canAll = (ME.clusters || []).includes('*') || (ROLES || []).some(r => (r.clusters || []).includes('*'));
  openModal({title: '新建静默', code: 'alert.silence', body: `<div class="form"><label class="req">集群</label><select id="slC">${canAll ? '<option value="">全部集群</option>' : ''}${CLUSTERS.map(c => `<option value="${esc(c.id)}" ${a ? (a.cluster === c.id ? 'selected' : '') : (CUR && CUR.id === c.id ? 'selected' : '')}>${esc(c.name)}</option>`).join('')}</select>
    <label>规则</label><select id="slR"><option value="">全部规则</option>${rules.map(r => `<option value="${esc(r.id)}" ${a && a.rule === r.id ? 'selected' : ''}>${esc(r.name)}</option>`).join('')}</select>
    <label>对象包含</label><input id="slI" placeholder="留空 = 全部对象，如 10.10.1.25" value="${a ? esc(a.inst.split(' ').pop()) : ''}">
    <label class="req">时长</label><select id="slD"><option value="60">1 小时</option><option value="240">4 小时</option><option value="1440">24 小时</option><option value="10080">7 天</option></select>
    <label class="req">原因</label><input id="slW" maxlength="100" placeholder="例如：计划内维护"></div>`,
    onOk() {
      const reason = $('#slW').value.trim(); if (!reason) { toast('请填写原因'); return false; }
      api('POST', '/api/silences', {cluster: $('#slC').value, rule: $('#slR').value, inst: $('#slI').value.trim(), reason, minutes: +$('#slD').value}).then(alDone('已静默', 'sil')).catch(liveFail);
      return false;
    }});
};
ACTIONS['alert.rule'] = d => {
  if (!LIVE.on) return _al.rule(d);
  if (d.cnew || d.cid || d.ctg || d.cdel) return customRuleAction(d);
  const rules = AL.rules || [];
  if (d.tg) { const r = rules.find(x => x.id === d.tg); return api('PUT', '/api/alert-rules/' + opsEnc(d.tg), {enabled: !r.enabled}).then(alDone((r.enabled ? '已停用 ' : '已启用 ') + r.name, 'rules')).catch(liveFail); }
  if (d.reset) return api('PUT', '/api/alert-rules/' + opsEnc(d.reset), {reset: true}).then(alDone('已恢复默认', 'rules')).catch(liveFail);
  const r = rules.find(x => x.id === d.id); if (!r) return;
  const chs = AL.ch || [];
  openModal({title: '编辑告警规则 · ' + r.name, code: 'alert.rule', body: `<div class="small muted" style="margin-bottom:8px">${esc(r.desc)}</div><div class="form">
    ${r.hasWarn ? `<label>警告阈值（≥）</label><input id="arW" type="number" min="0" step="any" value="${r.warn}"><div class="hint">默认 ${r.defWarn}${esc(r.unit)}</div>` : ''}
    ${r.id === 'node_down' || r.id === 'cluster_unreachable' ? '' : `<label>严重阈值（≥）</label><input id="arC" type="number" min="0" step="any" value="${r.crit}"><div class="hint">默认 ${r.defCrit}${esc(r.unit)}</div>`}
    <label>持续时间（分钟）</label><input id="arD" type="number" min="0" max="1440" value="${r.durMin}"><div class="hint">连续异常达到该时间才告警，默认 ${r.defDur}；0 = 立即</div>
    <label>通知渠道</label><div>${chs.length ? chs.map(c => `<label class="chk" style="margin-right:10px"><input type="checkbox" class="arCh" value="${esc(c.id)}" ${r.channels.includes(c.id) ? 'checked' : ''}> ${esc(c.name)}</label>`).join('') : '<span class="muted small">还没有渠道</span>'}<div class="hint">不勾选 = 发送到全部启用的渠道（再按各渠道的接收级别过滤）</div></div></div>`,
    onOk() {
      const body = {durMin: +$('#arD').value}; if ($('#arW')) body.warn = +$('#arW').value; if ($('#arC')) body.crit = +$('#arC').value; body.channels = $$('.arCh:checked').map(x => x.value);
      api('PUT', '/api/alert-rules/' + opsEnc(r.id), body).then(alDone('已保存', 'rules')).catch(liveFail); return false;
    }});
};
function customRuleAction(d) {
  const list = AL.custom || [];
  if (d.ctg) { const r = list.find(x => x.id === d.ctg); return api('PUT', '/api/alert-rules/custom/' + opsEnc(d.ctg), {enabled: !r.enabled}).then(alDone((r.enabled ? '已停用 ' : '已启用 ') + r.name, 'custom')).catch(liveFail); }
  if (d.cdel) return confirmTyped('删除自定义规则 ' + esc(d.n), '删除后该规则产生的告警立即关闭，不可恢复。', d.n, '删除', () => api('DELETE', '/api/alert-rules/custom/' + opsEnc(d.cdel)).then(alDone('已删除规则', 'custom', 'hist')).catch(liveFail), 'alert.rule');
  const r = d.cid ? list.find(x => x.id === d.cid) : {name: '', expr: '', desc: '', unit: '', cluster: '', op: '>=', warn: null, crit: '', durMin: 5, channels: []};
  if (!r) return;
  const chs = AL.ch || [];
  openModal({title: d.cid ? '编辑自定义规则 · ' + r.name : '新建自定义规则（PromQL）', code: 'alert.rule', wide: true, body: `<div class="form">
    <label class="req">规则名称</label><input id="crN" maxlength="40" value="${esc(r.name)}">
    <label class="req">PromQL</label><textarea id="crE" class="mono" rows="3" spellcheck="false" placeholder="例如：max by (instance) (starrocks_be_disks_data_used_pct{$sel})">${esc(r.expr)}</textarea>
    <label></label><div class="small muted">必须返回<b>即时向量</b>（或标量），每条序列是一个告警对象，对象名取 instance 等标签。<span class="mono">$sel</span> 会替换为集群的 Prometheus 选择器；集群没有选择器时连同后面的逗号一起去掉。</div>
    <label>适用集群</label><select id="crC"><option value="">所有配置了 Prometheus 的集群</option>${CLUSTERS.map(c => `<option value="${esc(c.id)}" ${r.cluster === c.id ? 'selected' : ''}>${esc(c.name)}${c.prom ? '' : '（未配置 Prometheus）'}</option>`).join('')}</select>
    <label>试运行</label><div class="row"><button class="btn sm" type="button" id="crT">在所选集群上试运行</button><span class="small muted">未选集群时用第一个配置了 Prometheus 的集群</span></div>
    <label></label><div id="crTR" class="small"></div>
    <label class="req">比较方式</label><select id="crO">${['>=', '>', '<=', '<'].map(o => `<option value="${o}" ${r.op === o ? 'selected' : ''}>值 ${opSym(o)} 阈值时告警</option>`).join('')}</select>
    <label>警告阈值</label><input id="crW" type="number" step="any" value="${r.warn == null ? '' : r.warn}" placeholder="可选；留空 = 只有严重级别">
    <label class="req">严重阈值</label><input id="crX" type="number" step="any" value="${r.crit}">
    <label>单位</label><input id="crU" maxlength="10" value="${esc(r.unit || '')}" placeholder="例如 % 或 ms，仅用于显示">
    <label>持续时间（分钟）</label><input id="crD" type="number" min="0" max="1440" value="${r.durMin}"><div class="hint">连续异常达到该时间才告警；0 = 立即</div>
    <label>说明</label><input id="crS" maxlength="200" value="${esc(r.desc || '')}" placeholder="显示在规则列表里，可选">
    <label>通知渠道</label><div>${chs.length ? chs.map(c => `<label class="chk" style="margin-right:10px"><input type="checkbox" class="crCh" value="${esc(c.id)}" ${(r.channels || []).includes(c.id) ? 'checked' : ''}> ${esc(c.name)}</label>`).join('') : '<span class="muted small">还没有渠道</span>'}<div class="hint">不勾选 = 发送到全部启用的渠道</div></div></div>`,
    onOk() {
      const num = id => { const v = $(id).value.trim(); return v === '' ? null : +v; };
      const body = {name: $('#crN').value.trim(), expr: $('#crE').value.trim(), cluster: $('#crC').value, op: $('#crO').value, crit: num('#crX'), unit: $('#crU').value.trim(), desc: $('#crS').value.trim(), durMin: +$('#crD').value, channels: $$('.crCh:checked').map(x => x.value)};
      const w = num('#crW'); if (w != null) body.warn = w;
      if (!body.name || !body.expr) { toast('请填写名称和 PromQL'); return false; }
      if (body.crit == null) { toast('请填写严重阈值'); return false; }
      api(d.cid ? 'PUT' : 'POST', '/api/alert-rules/custom' + (d.cid ? '/' + opsEnc(d.cid) : ''), body).then(alDone('已保存', 'custom')).catch(liveFail); return false;
    }});
  $('#crT').onclick = () => {
    const expr = $('#crE').value.trim(); if (!expr) return toast('请先填写 PromQL');
    const cid = $('#crC').value || (CLUSTERS.find(c => c.prom) || {}).id; if (!cid) return toast('没有配置 Prometheus 的集群，无法试运行');
    $('#crTR').innerHTML = '<span class="muted">查询中…</span>';
    api('POST', '/api/alert-rules/custom/test', {expr, cluster: cid}).then(res => {
      $('#crTR').innerHTML = `<div class="muted">实际执行：<span class="mono">${esc(res.expr)}</span></div><div>返回 <b>${res.count}</b> 条序列${res.count > res.series.length ? '，下面是前 ' + res.series.length + ' 条' : ''}：</div>` +
        (res.series.map(x => `<div class="mono small">${esc(x.inst)} = <b>${x.value}</b></div>`).join('') || '<div class="muted">（没有数据：表达式没有匹配到序列，或条件过滤后为空）</div>');
    }).catch(e => { $('#crTR').innerHTML = `<span class="up">${esc(e.message)}</span>`; });
  };
  if (!AL.ch) alLoad('ch', '/api/alert-channels');
}
ACTIONS['alert.channel'] = d => {
  if (!LIVE.on) return _al.channel(d);
  const list = AL.ch || [];
  if (d.test) { toast('正在发送测试消息…'); return api('POST', `/api/alert-channels/${opsEnc(d.test)}/test`).then(() => { toast('测试消息已发送，请到对应群 / 邮箱确认'); alDrop('ch'); rerender(); }).catch(e => { alDrop('ch'); toast('发送失败：' + e.message); rerender(); }); }
  if (d.del) return confirmTyped('删除通知渠道 ' + esc(d.n), '删除后，引用该渠道的规则将改为发送到全部启用的渠道。', d.n, '删除', () => api('DELETE', '/api/alert-channels/' + opsEnc(d.del)).then(alDone('已删除渠道', 'ch', 'rules')).catch(liveFail), 'alert.channel');
  const c = d.id ? list.find(x => x.id === d.id) : {type: '企业微信', name: '', url: '', levels: ['critical', 'warning'], enabled: true, hasSecret: false};
  if (!c) return;
  const types = ['企业微信', '钉钉', '飞书', 'Lark', 'Slack', 'Webhook', '邮件', '短信'];
  const ph = {企业微信: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…', 钉钉: 'https://oapi.dingtalk.com/robot/send?access_token=…', 飞书: 'https://open.feishu.cn/open-apis/bot/v2/hook/…', Lark: 'https://open.larksuite.com/open-apis/bot/v2/hook/…', Slack: 'https://hooks.slack.com/services/…', Webhook: 'https://your-service/alert', 邮件: 'dba@example.com, ops@example.com', 短信: '13800000000, 13900000000'};
  openModal({title: d.id ? '编辑通知渠道' : '新建通知渠道', code: 'alert.channel', body: `<div class="form"><label class="req">类型</label><select id="chT">${types.map(t => `<option ${t === c.type ? 'selected' : ''}>${t}</option>`).join('')}</select>
    <label class="req">名称</label><input id="chN" maxlength="40" value="${esc(c.name)}">
    <label class="req" id="chAL">${c.type === '邮件' ? '收件人' : c.type === '短信' ? '手机号' : 'Webhook 地址'}</label><input id="chA" class="mono" placeholder="${esc(d.id ? '留空 = 保持不变（' + c.url + '）' : ph[c.type])}">
    <label class="chS">签名密钥</label><input id="chS" class="chS" type="password" autocomplete="new-password" placeholder="${c.hasSecret ? '留空 = 保持不变' : '钉钉加签 / 飞书签名校验 / Webhook HMAC，可选'}">
    ${c.hasSecret ? '<label class="chS">清除密钥</label><label class="chS chk"><input type="checkbox" id="chX"> 清除已保存的签名密钥</label>' : ''}
    <label>接收级别</label><div>${lvList.map(l => `<label class="chk" style="margin-right:10px"><input type="checkbox" class="chL" value="${l}" ${c.levels.includes(l) ? 'checked' : ''}> ${sevText[l]}</label>`).join('')}</div>
    <label>状态</label><label class="chk"><input type="checkbox" id="chE" ${c.enabled ? 'checked' : ''}> 启用</label></div>
    <div class="notice" style="margin-top:10px"><span>🔒</span><div>地址与密钥加密保存，页面只显示脱敏后的地址。</div></div>`,
    onOk() {
      const type = $('#chT').value, body = {type, name: $('#chN').value.trim(), url: $('#chA').value.trim(), levels: $$('.chL:checked').map(x => x.value), enabled: $('#chE').checked};
      if (type !== '邮件' && type !== '短信') { body.secret = $('#chS').value; body.clearSecret = !!($('#chX') && $('#chX').checked); }
      api(d.id ? 'PUT' : 'POST', '/api/alert-channels' + (d.id ? '/' + opsEnc(d.id) : ''), body).then(alDone('已保存', 'ch', 'rules')).catch(liveFail); return false;
    }});
  const sync = () => { const t = $('#chT').value; $$('.chS').forEach(x => x.style.display = t === '邮件' || t === '短信' ? 'none' : ''); $('#chAL').textContent = t === '邮件' ? '收件人' : t === '短信' ? '手机号' : 'Webhook 地址'; if (!d.id) $('#chA').placeholder = ph[t]; };
  $('#chT').onchange = sync; sync();
  if (!AL.ch) alLoad('ch', '/api/alert-channels');
};

/* ============================ 总览「活跃告警」与角标 ============================ */
function liveAlertCardReal(o) {
  const mine = ALERTS.filter(a => CUR && a.cluster === CUR.id && a.state !== 'resolved');
  return `<div class="card"><h2>活跃告警 <span class="sub">${mine.length}</span><span class="right"><a href="#alert">告警中心 →</a></span></h2>
    ${mine.slice(0, 6).map(a => `<div style="padding:8px 0;border-bottom:1px solid var(--border)"><div class="row">${lvTag(a.level)}<b class="small">${esc(a.name)}</b>${a.state === 'acked' ? '<span class="tag warn">处理中</span>' : ''}${a.silenced ? '<span class="tag">已静默</span>' : ''}</div><div class="small muted mono">${esc(a.inst)}</div></div>`).join('') || '<div class="muted small" style="padding:8px 0">✓ 当前无告警</div>'}
    ${mine.length > 6 ? `<div class="small muted" style="padding-top:6px">另有 ${mine.length - 6} 项，请到告警中心查看</div>` : ''}</div>`;
}
const _liveEnterAl = liveEnter;
liveEnter = async function (me) {
  AL.hist = null; alDrop('rules', 'sil', 'ch', 'custom'); CHANNELS.length = 0; SILENCES.length = 0; ALERT_RULES.length = 0;
  await _liveEnterAl(me);
  alStart(); alRefreshActive(true);
  if (can('alert:view')) api('GET', '/api/alert-channels').then(l => { AL.ch = l; CHANNELS.length = 0; l.forEach(c => CHANNELS.push(c)); }).catch(() => {});
};
LIVE_PAGES.push('alert');
