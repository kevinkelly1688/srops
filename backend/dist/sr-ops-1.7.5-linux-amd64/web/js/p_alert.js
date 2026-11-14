/* 告警中心 / 操作审计 */
pages.alert = {
  render(sub) {
    sub = sub || 'active';
    return tabs('alert', [['active', `当前告警 (${ALERTS.filter(a => a.state !== 'resolved').length})`], ['rules', '告警规则'], ['silence', '静默'], ['channels', '通知渠道']], sub) + this[sub]();
  },
  active() {
    return `<div class="card"><div class="tbl-wrap"><table><thead><tr><th>级别</th><th>告警</th><th>对象</th><th>触发</th><th>说明</th><th>状态</th><th>操作</th></tr></thead><tbody>
    ${ALERTS.map(a => `<tr><td><span class="tag ${sevCls[a.lv]}">${sevText[a.lv]}</span></td><td><b>${a.name}</b></td><td class="small mono">${a.inst}</td><td>${a.time}</td><td class="small" style="max-width:380px">${a.desc}</td>
      <td>${a.state === 'firing' ? '<span class="tag bad">告警中</span>' : a.state === 'acked' ? `<span class="tag warn">处理中 · ${a.ackBy}</span>` : a.state === 'silenced' ? '<span class="tag">已静默</span>' : '<span class="tag ok">已恢复</span>'}</td>
      <td class="ops">${a.state === 'firing' ? B('alert.ack', '认领', {cls: 'sm', data: {id: a.id}}) : ''}${a.state !== 'silenced' ? B('alert.silence', '静默', {cls: 'sm', data: {id: a.id}}) : ''}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  rules() {
    return `<div class="toolbar"><span class="small muted">规则基于 Prometheus 指标（FE :8030/metrics、BE :8040/metrics），平台生成 Prometheus rule 文件或内置评估。${docLink('administration/management/monitoring/Monitor_and_Alert/', '监控报警 ↗')}</span><span class="sp"></span>${B('alert.rule', '＋ 新建规则', {cls: 'primary'})}</div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>规则</th><th>表达式</th><th>警告</th><th>严重</th><th>持续</th><th>状态</th><th>操作</th></tr></thead><tbody>
    ${ALERT_RULES.map(r => `<tr><td><b>${r.name}</b></td><td class="mono small" style="max-width:420px">${esc(r.expr)}</td><td>${r.op} ${r.warn}</td><td>${r.op} ${r.crit}</td><td>${r.dur}</td><td>${r.on ? '<span class="tag ok">启用</span>' : '<span class="tag">停用</span>'}</td>
      <td class="ops">${B('alert.rule', '编辑', {cls: 'sm', data: {id: r.id}})}${B('alert.rule', r.on ? '停用' : '启用', {cls: 'sm', data: {tg: r.id}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  silence() {
    return `<div class="card"><h2>静默规则 <span class="right">${B('alert.silence', '＋ 新建静默', {cls: 'sm primary'})}</span></h2><div class="tbl-wrap"><table><thead><tr><th>匹配条件</th><th>原因</th><th>创建人</th><th>截止</th><th>操作</th></tr></thead><tbody>
    ${SILENCES.map(s => `<tr><td class="mono">${esc(s.match)}</td><td>${s.reason}</td><td>${s.by}</td><td>${s.until}</td><td>${B('alert.silence', '解除', {cls: 'sm', data: {rm: s.id}})}</td></tr>`).join('') || '<tr><td colspan="5" class="muted">暂无</td></tr>'}
    </tbody></table></div></div>`;
  },
  channels() {
    return `<div class="card"><h2>通知渠道 <span class="right">${B('alert.channel', '＋ 新建渠道', {cls: 'sm primary'})}</span></h2><div class="tbl-wrap"><table><thead><tr><th>类型</th><th>名称</th><th>地址</th><th>接收级别</th><th>状态</th><th>操作</th></tr></thead><tbody>
    ${CHANNELS.map(c => `<tr><td>${c.type}</td><td><b>${c.name}</b></td><td class="mono small">${esc(c.target)}${c.secret ? ' <span class="tag ok">签名校验</span>' : ''}${c.mention && c.mention !== '—' ? `<div class="small muted">${esc(c.mention)}</div>` : ''}</td><td>${c.lv}</td><td>${c.on ? '<span class="tag ok">启用</span>' : '<span class="tag">停用</span>'}</td><td class="ops">${B('alert.channel', '测试', {cls: 'sm', data: {test: c.id}})}${B('alert.channel', '编辑', {cls: 'sm', data: {id: c.id}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  }
};
ACTIONS['alert.ack'] = d => { const a = ALERTS.find(x => x.id === d.id); a.state = 'acked'; a.ackBy = ME.login; audit('alert.ack', '认领告警 ' + a.name); buildNav(); rerender(); };
ACTIONS['alert.silence'] = d => {
  if (d.rm) { SILENCES.splice(SILENCES.findIndex(s => s.id === d.rm), 1); audit('alert.silence', '解除静默'); return rerender(); }
  const a = d.id ? ALERTS.find(x => x.id === d.id) : null;
  openModal({title: '新建静默', code: 'alert.silence', body: `<div class="form"><label class="req">匹配条件</label><input id="slM" class="mono" value="${a ? `alertname="${a.rule}", inst="${a.inst.split(' ')[0]}"` : ''}"><label class="req">时长</label><select id="slD"><option>1 小时</option><option>4 小时</option><option>24 小时</option></select><label class="req">原因</label><input id="slR" placeholder="例如：计划内维护"></div>`,
    onOk() { SILENCES.push({id: 'S' + (SILENCES.length + 2), match: $('#slM').value, reason: $('#slR').value || '—', by: ME.login, until: $('#slD').value + '后'}); if (a) a.state = 'silenced'; audit('alert.silence', '静默 ' + $('#slM').value); buildNav(); rerender(); }});
};
ACTIONS['alert.rule'] = d => {
  if (d.tg) { const r = ALERT_RULES.find(x => x.id === d.tg); r.on = !r.on; audit('alert.rule', (r.on ? '启用' : '停用') + '告警规则 ' + r.name); return rerender(); }
  const r = d.id ? ALERT_RULES.find(x => x.id === d.id) : {name: '', expr: '', op: '>', warn: '', crit: '', dur: '5m'};
  openModal({title: d.id ? '编辑告警规则' : '新建告警规则', code: 'alert.rule', body: `<div class="form"><label class="req">名称</label><input value="${esc(r.name)}"><label class="req">PromQL</label><textarea rows="3" class="mono">${esc(r.expr)}</textarea>
    <label>比较</label><select><option ${r.op === '>' ? 'selected' : ''}>&gt;</option><option>&lt;</option><option ${r.op === '==' ? 'selected' : ''}>==</option></select><label>警告阈值</label><input value="${r.warn}"><label>严重阈值</label><input value="${r.crit}"><label>持续时间</label><input value="${r.dur}">
    <label>通知渠道</label><div>${CHANNELS.map(c => `<label class="chk" style="margin-right:10px"><input type="checkbox" ${c.on ? 'checked' : ''}>${c.name}</label>`).join('')}</div></div>`,
    onOk() { audit('alert.rule', (d.id ? '编辑' : '新建') + '告警规则 ' + (r.name || '新规则')); toast('已保存'); }});
};
ACTIONS['alert.channel'] = d => {
  if (d.test) { const t = CHANNELS.find(x => x.id === d.test); audit('alert.channel', `测试通知渠道 ${t.type} · ${t.name}`); return openModal({title: `测试消息已发送 · ${t.name}`, ok: '', cancel: '关闭', body: `<div class="notice ok"><span>✓</span><div>已向 ${t.type} 发送测试消息（HTTP 200${t.type === '飞书' || t.type === 'Lark' ? '，code=0' : t.type === 'Slack' ? '，ok' : ''}），请到对应群 / 频道确认是否收到。</div></div>${chPreview(t.type)}`}); }
  const c = d.id ? CHANNELS.find(x => x.id === d.id) : {type: '飞书', name: '', target: '', lv: '严重 / 警告'};
  const lvs = ['严重', '警告', '提示'];
  openModal({title: d.id ? '编辑通知渠道' : '新建通知渠道', code: 'alert.channel', wide: true, body: `<div class="grid g2" style="align-items:start"><div class="form">
      <label class="req">类型</label><select id="chT">${Object.keys(CHANNEL_TYPES).map(t => `<option ${t === c.type ? 'selected' : ''}>${t}</option>`).join('')}</select>
      <label class="req">名称</label><input id="chN" value="${esc(c.name)}">
      <div class="full" id="chF"></div>
      <label>接收级别</label><div>${lvs.map(l => `<label class="chk" style="margin-right:8px"><input type="checkbox" class="chL" value="${l}" ${c.lv.includes(l) ? 'checked' : ''}>${l}</label>`).join('')}</div>
      <label>恢复通知</label><label class="chk"><input type="checkbox" checked> 告警恢复时发送</label></div>
    <div><div class="small muted" style="margin-bottom:6px">消息预览</div><div id="chP"></div></div></div>`,
    onOk() {
      const v = {type: $('#chT').value, name: $('#chN').value.trim(), target: $('#chA').value.trim(), secret: !!($('#chS') && $('#chS').value), mention: $('#chM') ? $('#chM').value : '', lv: $$('.chL').filter(x => x.checked).map(x => x.value).join(' / ')};
      if (!v.name || !v.target) { toast('请填写名称和地址'); return false; }
      if (!['Webhook', '其他'].includes(v.type) && /^https?:/.test(CHANNEL_TYPES[v.type].ph) && !v.target.startsWith(CHANNEL_TYPES[v.type].ph.split('<')[0].replace(/T000.*$/, ''))) { toast(`${v.type} Webhook 地址应以 ${CHANNEL_TYPES[v.type].ph.split('<')[0].replace(/T000.*$/, '')} 开头`); return false; }
      if (d.id) Object.assign(c, v); else CHANNELS.push({id: 'C' + (CHANNELS.length + 1), on: true, ...v});
      audit('alert.channel', `${d.id ? '编辑' : '新建'}通知渠道 ${v.type} · ${v.name}`); toast('已保存'); rerender();
    }});
  const fill = () => {
    const t = $('#chT').value, m = CHANNEL_TYPES[t], same = t === c.type;
    $('#chF').innerHTML = `<div class="form">
      <label class="req">${/^https?:/.test(m.ph) ? 'Webhook 地址' : t === '邮件' ? '收件人' : '手机号'}</label><input id="chA" class="mono" placeholder="${esc(m.ph)}" value="${same ? esc(c.target) : ''}">
      ${m.bot ? '<label>发送方式</label><select><option>Incoming Webhook</option><option>Bot Token（xoxb-）+ 频道</option></select>' : ''}
      ${m.secret ? `<label>${t === 'Webhook' ? '签名' : '安全校验'}</label><input id="chS" type="password" placeholder="${esc(m.secret)}" value="${same && c.secret ? '********' : ''}">` : ''}
      ${m.mention ? `<label>@ 提醒</label><input id="chM" placeholder="${esc(m.mention)}" value="${same && c.mention && c.mention !== '—' ? esc(c.mention) : ''}">` : ''}
      ${m.custom ? `<label>请求方法</label><select><option>POST</option><option>PUT</option><option>GET</option></select>
      <label>请求头</label><textarea rows="2" class="mono small" placeholder="每行一个">Content-Type: application/json</textarea>
      <label>消息模板</label><textarea rows="5" class="mono small" id="chTpl">${esc(CH_TPL)}</textarea>
      <div class="hint" style="grid-column:2">变量：{alert} {level} {cluster} {instance} {desc} {time} {url}</div>` : ''}
      <label>消息格式</label><div class="small">${m.fmt}</div>
      ${m.hint ? `<div class="hint" style="grid-column:1/-1">${m.hint}</div>` : ''}</div>`;
    $('#chP').innerHTML = chPreview(t);
    if ($('#chTpl')) $('#chTpl').oninput = () => { $('#chP').innerHTML = chPreview(t, $('#chTpl').value); };
  };
  $('#chT').onchange = fill; fill();
};
/* 各渠道消息样式预览（示例告警 A1001） */
const CH_TPL = String.raw`{
  "title": "[{level}] {alert}",
  "text": "集群 {cluster} · {instance}\n{desc}\n时间 {time}",
  "link": "{url}"
}`;
function chPreview(t, tpl) {
  const a = ALERTS[0], body = `<b>${a.name}</b><br><span class="small">集群：${CUR.name}　对象：${a.inst}<br>触发：2026-10-06 ${a.time}<br>${a.desc}</span>`;
  const btn = txt => `<span class="btn sm" style="margin-top:8px;display:inline-block">${txt}</span>`;
  if (t === '飞书' || t === 'Lark') return `<div style="border:1px solid var(--border);border-radius:10px;overflow:hidden"><div style="background:var(--bad);color:#fff;padding:8px 12px;font-weight:600">🔴 ${t === 'Lark' ? '[CRITICAL]' : '【严重】'} StarRocks 告警</div><div style="padding:10px 12px">${body}<br>${btn(t === 'Lark' ? 'View details' : '查看详情')} ${btn(t === 'Lark' ? 'Acknowledge' : '认领')}</div></div><div class="small muted" style="margin-top:6px">消息卡片按钮回跳平台告警详情；「认领」需在平台登录后完成。</div>`;
  if (t === 'Slack') return `<div style="border:1px solid var(--border);border-radius:8px;padding:10px 12px;display:flex;gap:10px"><div style="width:4px;border-radius:2px;background:var(--bad)"></div><div><div class="small muted"><b style="color:var(--text)">StarRocks Ops</b> APP 10:31</div><div class="small">@here</div>${body}<br>${btn('View in Ops')} ${btn('Acknowledge')}</div></div>`;
  if (t === '邮件') return `<div style="border:1px solid var(--border);border-radius:8px;padding:10px 12px"><div class="small muted">主题：[严重] ${CUR.name} · ${a.name}</div><div class="hr"></div>${body}</div>`;
  if (t === '短信') return `<div style="border:1px solid var(--border);border-radius:14px;padding:10px 12px;max-width:260px" class="small">【SR运维】严重：${CUR.name} ${a.inst.split(' ')[0]} ${a.name}，请及时处理。</div>`;
  if (t === '企业微信') return `<div style="border:1px solid var(--border);border-radius:8px;padding:10px 12px;background:var(--panel)"><div class="small muted" style="margin-bottom:6px">群机器人 · Markdown</div><div><b style="color:#d23b3b">【严重】StarRocks 告警</b></div><div class="small" style="margin-top:4px">&gt; 集群：<span style="color:#c27c00">${CUR.name}</span><br>&gt; 对象：${a.inst}<br>&gt; 时间：2026-10-06 ${a.time}</div><div class="small" style="margin-top:4px">${a.name}：${a.desc}</div><div class="small" style="margin-top:6px"><a>查看详情</a>　@所有人</div></div>`;
  if (t === '钉钉') return `<div style="border:1px solid var(--border);border-radius:8px;overflow:hidden"><div style="background:#1677ff;color:#fff;padding:6px 12px" class="small">自定义机器人 · ActionCard</div><div style="padding:10px 12px"><b>【严重】StarRocks 告警</b><br><span class="small">${a.name}<br>集群：${CUR.name}　对象：${a.inst}<br>${a.desc}</span><div class="row" style="margin-top:8px;border-top:1px solid var(--border);padding-top:6px;justify-content:space-around"><a class="small">查看详情</a><a class="small">认领</a></div></div></div><div class="small muted" style="margin-top:6px">@ 提醒的手机号需同时写入消息内容才会高亮。</div>`;
  if (t === '其他') { const out = String(tpl || CH_TPL).replace(/\{(\w+)\}/g, (m, k) => ({alert: a.name, level: '严重', cluster: CUR.name, instance: a.inst, desc: a.desc, time: '2026-10-06 ' + a.time, url: 'https://sr-ops.example.com/#alert'}[k] ?? m)); return `<div class="small muted">按模板渲染后的请求体：</div>` + code(out); }
  if (t === 'Webhook') return code(JSON.stringify({status: 'firing', labels: {alertname: a.rule, severity: a.lv, cluster: CUR.name, instance: a.inst}, annotations: {summary: a.name, description: a.desc}, startsAt: '2026-10-06T10:31:00+08:00'}, null, 2));
  return `<div style="border:1px solid var(--border);border-radius:8px;padding:10px 12px"><div style="color:var(--bad);font-weight:600">【严重】StarRocks 告警</div>${body}</div>`;
}

/* ---------- 操作审计 ---------- */
const AUD = {user: '', res: '', q: ''};
pages.audit = {
  render(sub) {
    sub = sub || 'ops';
    return tabs('audit', [['ops', '平台操作审计'], ['review', `待复核 (${AUDIT.filter(a => a.res === '待复核').length})`], ['sql', 'SQL 审计']], sub) + this[sub]();
  },
  ops() {
    const list = AUDIT.filter(a => (!AUD.user || a.user === AUD.user) && (!AUD.res || a.res.startsWith(AUD.res)) && (!AUD.q || (a.act + a.code + a.sql).includes(AUD.q)));
    return `<div class="toolbar"><select id="auU"><option value="">全部用户</option>${USERS.map(u => `<option ${u.login === AUD.user ? 'selected' : ''}>${u.login}</option>`).join('')}</select>
      <select id="auR"><option value="">全部结果</option>${['成功', '失败', '拒绝', '待复核'].map(r => `<option ${r === AUD.res ? 'selected' : ''}>${r}</option>`).join('')}</select><input type="search" id="auQ" placeholder="操作 / 权限码 / SQL" value="${esc(AUD.q)}"><span class="sp"></span>${B('audit.export', '⇩ 导出 CSV')}</div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>来源 IP</th><th>集群</th><th>菜单</th><th>权限码</th><th>操作</th><th>结果</th><th>执行内容</th></tr></thead><tbody>
    ${list.map(a => `<tr><td class="small">${a.at}</td><td>${a.user}</td><td class="mono small">${a.ip}</td><td>${a.cluster}</td><td>${a.menu}</td><td class="mono small">${a.code}</td><td>${esc(a.act)}</td>
      <td><span class="tag ${a.res === '成功' ? 'ok' : a.res === '待复核' ? 'warn' : 'bad'}">${a.res}</span></td><td class="mono small" style="max-width:300px;white-space:pre-wrap">${esc(a.sql)}</td></tr>`).join('')}
    </tbody></table></div><div class="small muted" style="margin-top:8px">平台所有按钮操作（包括因无权限被拒绝的尝试）均记录审计，保留 180 天，不可删除。</div></div>`;
  },
  review() {
    const list = AUDIT.filter(a => a.res === '待复核');
    return `<div class="notice"><span>👥</span><div>开启「平台设置 → 安全策略 → 双人复核」后，高危操作提交到这里，由另一位拥有相同权限码的用户批准后执行。当前：${SETTINGS.dualReview ? '<span class="tag ok">已开启</span>' : '<span class="tag">未开启</span>'}</div></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>提交时间</th><th>提交人</th><th>权限码</th><th>操作</th><th>执行内容</th><th>操作</th></tr></thead><tbody>
    ${list.map(a => `<tr><td>${a.at}</td><td>${a.user}</td><td class="mono small">${a.code}</td><td>${esc(a.act)}</td><td class="mono small">${esc(a.sql)}</td><td class="ops">${a.user === ME.login ? '<span class="small muted">不能复核自己的操作</span>' : can(a.code) ? `<button class="btn sm primary" data-on="rvOk" data-i="${AUDIT.indexOf(a)}">批准</button><button class="btn sm" data-on="rvNo" data-i="${AUDIT.indexOf(a)}">驳回</button>` : '<span class="small muted">无该权限，不能复核</span>'}</td></tr>`).join('') || '<tr><td colspan="6" class="muted">暂无待复核操作</td></tr>'}
    </tbody></table></div></div>`;
  },
  sql() {
    if (!can('audit.sql')) return '<div class="card muted">🔒 需要「查看 SQL 审计」(audit.sql) 权限</div>';
    return `<div class="notice"><span>ℹ</span><div>来自 StarRocks 审计日志（fe.audit.log，经 AuditLoader 写入 <code>starrocks_audit_db__.starrocks_audit_tbl__</code>），记录数据库层所有 SQL。</div></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>客户端</th><th>库</th><th>状态</th><th>耗时</th><th>扫描行</th><th>SQL</th></tr></thead><tbody>
    ${[['10:43:09', 'api_svc', '10.40.2.17', 'ads', 'EOF', '12ms', '23,000', 'SELECT * FROM ads.ads_user_profile WHERE user_id = 100231'], ['10:42:55', 'adhoc_liu', '10.1.9.4', 'dws', 'EOF', '15.2s', '1.1 亿', SLOW[1].sql], ['10:41:02', 'bi_report', '10.40.3.2', 'dws', 'EOF', '128.4s', '3.2 亿', SLOW[0].sql], ['10:31:12', 'sr_ops', '10.1.1.2', '—', 'OK', '3ms', '0', 'ADD BACKEND BLACKLIST 10005'], ['10:12:40', 'etl_user', '10.40.5.5', 'ods', 'ERR', '0.2s', '0', 'INSERT INTO ods.ods_order ... -- too many versions']].map(r => `<tr>${r.map((c, i) => `<td class="${i === 7 ? 'mono small' : i === 4 ? '' : 'small'}">${i === 4 ? `<span class="tag ${c === 'ERR' ? 'bad' : 'ok'}">${c}</span>` : esc(c)}</td>`).join('')}</tr>`).join('')}
    </tbody></table></div></div>`;
  },
  init() {
    const u = $('#auU'), r = $('#auR'), q = $('#auQ');
    if (u) { u.onchange = () => { AUD.user = u.value; rerender(); }; r.onchange = () => { AUD.res = r.value; rerender(); }; q.onchange = () => { AUD.q = q.value; rerender(); }; }
  }
};
ACTIONS['audit.export'] = () => { audit('audit.export', '导出审计日志'); toast('已导出 audit_' + now().slice(0, 10) + '.csv（模拟）'); };
ON.rvOk = d => { const a = AUDIT[d.i]; a.res = '成功'; a.act = a.act.replace('（待复核）', `（复核人 ${ME.login}）`); toast('已批准并执行'); rerender(); };
ON.rvNo = d => { const a = AUDIT[d.i]; a.res = '拒绝'; a.act = a.act.replace('（待复核）', `（${ME.login} 驳回）`); rerender(); };
