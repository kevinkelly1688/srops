// @author: kevinkelly
// @license: Apache Licence
// @file: watermark.js
// @time: 2026/10/28

/* 页面水印：登录后覆盖全部页面（含弹窗、抽屉、打印），用于防截图外泄追溯。
   仅「超级管理员」角色可在「平台设置 → 水印」自定义；其他用户只读。
   变量：{name} 姓名 {login} 账号 {dept} 部门 {phone4} 手机后四位 {ip} 来源 IP {date} 日期 {time} 时间 {cluster} 当前集群 */
const WATERMARK = {on: true, text: '{name} {login}', text2: '{date} {time} · 内部资料 严禁外传', size: 14, opacity: 0.1, angle: -22, gap: 220, color: '#808080'};
const isSuperAdmin = () => ME.roles.includes('r_admin');

function wmText(tpl) {
  const d = now();
  return String(tpl || '').replace(/\{(\w+)\}/g, (m, k) => ({name: ME.name, login: ME.login, dept: ME.dept, phone4: (ME.phone || '').slice(-4), ip: ME.ip || '10.1.1.2',
    date: d.slice(0, 10), time: d.slice(11), cluster: CUR ? CUR.name : '—'}[k] ?? m));
}
function wmImage(w = WATERMARK) {
  const l1 = esc(wmText(w.text)), l2 = esc(wmText(w.text2)), g = +w.gap, s = +w.size;
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${g * 1.6}" height="${g}"><g transform="rotate(${w.angle} ${g * .8} ${g / 2})" fill="${w.color}" fill-opacity="${w.opacity}" font-family="Microsoft YaHei,PingFang SC,sans-serif" text-anchor="middle">
    <text x="${g * .8}" y="${g / 2}" font-size="${s}">${l1}</text>${l2 ? `<text x="${g * .8}" y="${g / 2 + s * 1.5}" font-size="${s * .8}">${l2}</text>` : ''}</g></svg>`;
  return `url('data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg).replace(/'/g, '%27')}')`;   // 单引号：可安全放进 style="..." 属性
}
function paintWatermark() {
  let el = document.getElementById('wm');
  const show = WATERMARK.on && $('#login').hidden;
  if (!show) { if (el) el.remove(); return; }
  if (!el) { el = document.createElement('div'); el.id = 'wm'; document.body.appendChild(el); }
  el.setAttribute('aria-hidden', 'true');
  el.style.cssText = `position:fixed;inset:0;pointer-events:none;z-index:2147483000;background-image:${wmImage()};background-repeat:repeat;-webkit-print-color-adjust:exact;print-color-adjust:exact`;
}
/* 防篡改：水印节点被删除或样式被改时自动恢复 */
new MutationObserver(() => { const el = document.getElementById('wm'); if ((WATERMARK.on && $('#login').hidden) && (!el || el.style.display === 'none' || el.style.opacity === '0')) paintWatermark(); })
  .observe(document.body, {childList: true, subtree: false, attributes: true, attributeFilter: ['style'], attributeOldValue: false});
new MutationObserver(paintWatermark).observe($('#login'), {attributes: true, attributeFilter: ['hidden']});   // 退出登录时移除
/* 每次页面渲染（含登录、切换用户 / 集群）后刷新水印内容 */
const _renderNoWm = render;
render = function (...a) { const r = _renderNoWm.apply(this, a); paintWatermark(); return r; };
setInterval(paintWatermark, 60e3);   // 时间变量每分钟刷新

/* ---------- 平台设置 → 水印 ---------- */
function wmSettingsCard() {
  const w = WATERMARK, row = (k, v) => `<div class="row between" style="padding:8px 0;border-bottom:1px solid var(--border)"><span class="muted">${k}</span><span>${v}</span></div>`;
  return `<div class="card"><h2>页面水印 <span class="right">${isSuperAdmin() ? '<button class="btn sm" data-on="wmEdit">自定义</button>' : '<button class="btn sm noperm" disabled title="仅超级管理员可自定义水印">自定义</button>'}</span></h2>
    ${isSuperAdmin() ? '' : '<div class="notice warn"><span>🔒</span><div>水印由超级管理员统一配置，其他用户只能查看。</div></div>'}
    ${row('状态', w.on ? '<span class="tag ok">已开启</span>' : '<span class="tag">已关闭</span>')}${row('第一行', `<span class="mono">${esc(w.text)}</span>`)}${row('第二行', `<span class="mono">${esc(w.text2) || '—'}</span>`)}
    ${row('样式', `字号 ${w.size}px · 透明度 ${Math.round(w.opacity * 100)}% · 角度 ${w.angle}° · 间距 ${w.gap}px · 颜色 <span style="display:inline-block;width:12px;height:12px;border-radius:3px;background:${w.color};vertical-align:-1px"></span> ${w.color}`)}
    ${row('范围', '登录后的全部页面、弹窗、抽屉与打印 / 导出 PDF；登录页不显示')}
    <div class="small muted" style="margin:12px 0 6px">当前用户看到的效果：</div>
    <div style="height:150px;border:1px solid var(--border);border-radius:10px;background:var(--panel2) ${wmImage()} repeat"></div></div>`;
}
ON.wmEdit = () => {
  if (!isSuperAdmin()) return toast('仅超级管理员可自定义水印');
  const w = WATERMARK;
  const rng = (id, label, min, max, step, v, unit) => `<label>${label}</label><div class="row" style="flex-wrap:nowrap"><input type="range" id="${id}" min="${min}" max="${max}" step="${step}" value="${v}" style="flex:1"><span class="small mono" id="${id}V" style="width:52px">${v}${unit}</span></div>`;
  openModal({title: '自定义水印', wide: true, body: `<div class="grid g2" style="align-items:start"><div class="form">
      <label>开启水印</label><label class="chk"><input type="checkbox" id="wmOn" ${w.on ? 'checked' : ''}> 对全部用户生效</label>
      <label class="req">第一行</label><input id="wmT1" class="mono" value="${esc(w.text)}">
      <label>第二行</label><input id="wmT2" class="mono" value="${esc(w.text2)}">
      <div class="hint">可用变量：{name} {login} {dept} {phone4} {ip} {date} {time} {cluster}</div>
      ${rng('wmSz', '字号', 10, 28, 1, w.size, 'px')}${rng('wmOp', '透明度', 0.04, 0.4, 0.01, w.opacity, '')}${rng('wmAg', '角度', -60, 60, 1, w.angle, '°')}${rng('wmGp', '间距', 140, 360, 10, w.gap, 'px')}
      <label>颜色</label><input type="color" id="wmCl" value="${w.color}" style="width:60px;padding:2px">
      <label>预设</label><div class="row"><button class="btn sm" type="button" data-on="wmPreset" data-p="std">标准</button><button class="btn sm" type="button" data-on="wmPreset" data-p="strong">醒目</button><button class="btn sm" type="button" data-on="wmPreset" data-p="light">轻量</button></div></div>
    <div><div class="small muted" style="margin-bottom:6px">实时预览（以当前用户 ${esc(ME.login)} 为例）</div><div id="wmPrev" style="height:300px;border:1px solid var(--border);border-radius:10px;background-color:var(--panel)"></div></div></div>`,
    onOk() {
      const v = wmForm(); if (!v.text.trim()) { toast('第一行不能为空'); return false; }
      return commitSetting('watermark', v, WATERMARK);
    }});
  const upd = () => { const v = wmForm(); $('#wmPrev').style.backgroundImage = wmImage(v); [['wmSz', 'px'], ['wmOp', ''], ['wmAg', '°'], ['wmGp', 'px']].forEach(([id, u]) => $('#' + id + 'V').textContent = $('#' + id).value + u); };
  $$('#modal input').forEach(i => i.oninput = upd); upd();
};
function wmForm() { return {on: $('#wmOn').checked, text: $('#wmT1').value, text2: $('#wmT2').value, size: +$('#wmSz').value, opacity: +$('#wmOp').value, angle: +$('#wmAg').value, gap: +$('#wmGp').value, color: $('#wmCl').value}; }
ON.wmPreset = d => {
  const p = {std: [14, 0.1, -22, 220, '#808080'], strong: [18, 0.2, -30, 180, '#d23b3b'], light: [12, 0.06, -18, 280, '#808080']}[d.p];
  [['wmSz', p[0]], ['wmOp', p[1]], ['wmAg', p[2]], ['wmGp', p[3]], ['wmCl', p[4]]].forEach(([id, v]) => $('#' + id).value = v);
  $('#wmT1').dispatchEvent(new Event('input'));
};
