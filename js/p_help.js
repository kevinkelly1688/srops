// @author: kevinkelly
// @license: Apache Licence
// @file: p_help.js
// @time: 2026/10/28

/* 使用手册：内容来自 help/*.md（唯一来源），由 tools/build_help.py 打包进 js/help_content.js，
   这样直接双击 index.html（file://）也能阅读。顶栏「? 帮助」打开当前页面对应章节。 */
const HELP_TOPICS = [
  {g: '入门', id: 'quickstart', t: '快速入门'},
  {g: '入门', id: 'install', t: '平台部署与集群导入'},
  {g: '入门', id: 'login', t: '登录、账号与主题'},
  {g: '概览', id: 'overview', t: '集群总览 / 集群架构'},
  {g: '性能与诊断', id: 'inspect', t: '性能巡检'},
  {g: '性能与诊断', id: 'query', t: '查询分析 / 资源隔离'},
  {g: '集群运维', id: 'nodes', t: '节点管理'},
  {g: '集群运维', id: 'deploy', t: '安装部署'},
  {g: '集群运维', id: 'upgrade', t: '升级回滚'},
  {g: '集群运维', id: 'backup', t: '备份恢复'},
  {g: '集群运维', id: 'load', t: '导入作业 / 物化视图'},
  {g: '集群运维', id: 'param', t: '参数配置'},
  {g: '告警与审计', id: 'alert', t: '告警中心 / 操作审计'},
  {g: '平台配置', id: 'permission', t: '用户、角色与权限'},
  {g: '平台配置', id: 'platform', t: '集群接入 / 平台设置'},
  {g: '附录', id: 'permcodes', t: '权限码清单'},
  {g: '附录', id: 'faq', t: '常见问题'},
  {g: '附录', id: 'changelog', t: '更新记录'},
];
const PAGE_HELP = {overview: 'overview', topology: 'overview', inspect: 'inspect', query: 'query', resource: 'query', nodes: 'nodes', deploy: 'deploy', upgrade: 'upgrade', backup: 'backup', load: 'load', mv: 'load', param: 'param', alert: 'alert', audit: 'alert', users: 'permission', roles: 'permission', clusters: 'install', settings: 'platform'};

function mdInline(s) {
  const codes = [];
  let h = s.replace(/`([^`]+)`/g, (m, c) => { codes.push(`<code>${esc(c)}</code>`); return '\u0000' + (codes.length - 1) + '\u0000'; });
  h = esc(h).replace(/\*\*(.+?)\*\*/g, '<b>$1</b>').replace(/==(.+?)==/g, '<mark>$1</mark>');
  h = h.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (m, t, u) => {
    u = u.replace(/&amp;/g, '&'); let x;
    if ((x = /^#(.+)$/.exec(u))) return `<a href="#${x[1]}">${t}</a>`;
    if (/^https?:\/\//.test(u)) return `<a href="${esc(u)}" target="_blank" rel="noopener">${t}</a>`;
    return t;
  });
  return h.replace(/\u0000(\d+)\u0000/g, (m, i) => codes[+i]);
}
function mdRender(src) {
  const L = src.replace(/\r\n?/g, '\n').split('\n'), out = [], toc = [], para = [];
  let i = 0, hn = 0;
  const flush = () => { if (para.length) { out.push(`<p>${mdInline(para.join(' '))}</p>`); para.length = 0; } };
  const cells = l => { let s = l.trim(); if (s[0] === '|') s = s.slice(1); if (s.endsWith('|')) s = s.slice(0, -1); return s.split('|').map(c => c.trim()); };
  while (i < L.length) {
    const ln = L[i]; let m;
    if (/^```/.test(ln)) { flush(); const b = []; i++; while (i < L.length && !/^```/.test(L[i])) b.push(L[i++]); i++; out.push(code(b.join('\n'))); continue; }
    if (!ln.trim()) { flush(); i++; continue; }
    if ((m = /^(#{1,4})\s+(.*)$/.exec(ln))) { flush(); const lv = m[1].length, id = 'h' + (++hn); if (lv === 2) toc.push({id, t: m[2].replace(/[`*]/g, '')}); out.push(lv === 1 ? `<h1>${mdInline(m[2])}</h1>` : `<h${lv + 1} id="${id}">${mdInline(m[2])}</h${lv + 1}>`); i++; continue; }
    if (/^\s*\|/.test(ln) && L[i + 1] && /^\s*\|?\s*:?-{2,}/.test(L[i + 1])) { flush(); const head = cells(ln); i += 2; const rows = []; while (i < L.length && /^\s*\|/.test(L[i])) rows.push(cells(L[i++]));
      out.push(`<div class="tbl-wrap" style="margin:8px 0"><table><thead><tr>${head.map(c => `<th>${mdInline(c)}</th>`).join('')}</tr></thead><tbody>${rows.map(r => `<tr>${head.map((_, k) => `<td>${mdInline(r[k] || '')}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`); continue; }
    if (/^>/.test(ln)) { flush(); const b = []; while (i < L.length && /^>/.test(L[i])) b.push(L[i++].replace(/^>\s?/, '')); const t = b.join(' '); out.push(`<div class="tip ${/^(⚠|注意|警告)/.test(t) ? 'warn' : ''}">${mdInline(t)}</div>`); continue; }
    if (/^\s*([-*]|\d+\.)\s+/.test(ln)) { flush(); const ol = /^\s*\d+\./.test(ln), b = []; while (i < L.length && /^\s*([-*]|\d+\.)\s+/.test(L[i])) b.push(L[i++].replace(/^\s*([-*]|\d+\.)\s+/, '')); out.push(`<${ol ? 'ol' : 'ul'}>${b.map(x => `<li>${mdInline(x)}</li>`).join('')}</${ol ? 'ol' : 'ul'}>`); continue; }
    para.push(ln.trim()); i++;
  }
  flush();
  return {html: out.join('\n'), toc};
}
const HP = {q: ''};
pages.help = {
  title: '使用手册',
  render(sub) {
    const id = HELP_TOPICS.some(t => t.id === sub) ? sub : 'quickstart';
    const src = (window.HELP_MD || {})[id];
    let g = '';
    const nav = HELP_TOPICS.map(t => { const s = (t.g !== g ? `<div class="g">${t.g}</div>` : '') + `<a href="#help/${t.id}" class="${t.id === id ? 'on' : ''}">${t.t}</a>`; g = t.g; return s; }).join('');
    if (HP.q) {
      const hits = [];
      HELP_TOPICS.forEach(t => ((window.HELP_MD || {})[t.id] || '').split('\n').forEach(l => { if (l.toLowerCase().includes(HP.q.toLowerCase()) && hits.length < 60) hits.push({t, l}); }));
      return `<div class="help"><div class="card topics"><input type="search" id="hpQ" placeholder="搜索手册" value="${esc(HP.q)}" style="width:100%;margin-bottom:8px">${nav}</div>
        <div class="card hp"><h1>搜索「${esc(HP.q)}」 <span class="small muted">${hits.length} 条</span></h1>${hits.map(h => `<div style="padding:6px 0;border-bottom:1px solid var(--border)"><a href="#help/${h.t.id}" data-on="hpClear">${h.t.t}</a><div class="small">${mdInline(h.l.replace(/^[#>|\-*\s]+/, '')).replace(new RegExp(HP.q.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi'), m => `<mark>${m}</mark>`)}</div></div>`).join('') || '<div class="muted">无结果</div>'}</div></div>`;
    }
    const r = src ? mdRender(src) : {html: '<div class="notice warn"><span>!</span><div>未找到手册内容：请运行 <code>python tools/build_help.py</code> 生成 js/help_content.js。</div></div>', toc: []};
    return `<div class="help"><div class="card topics"><input type="search" id="hpQ" placeholder="搜索手册" style="width:100%;margin-bottom:8px">${nav}<div class="hr"></div><a href="docs/用户使用手册.md" target="_blank">⇩ 完整手册（Markdown）</a></div>
      <div class="card hp">${r.html}</div>
      <div class="toc">${r.toc.length ? '<div class="small b" style="margin-bottom:4px">本页目录</div>' + r.toc.map(t => `<a data-on="hpToc" data-id="${t.id}">${t.t}</a>`).join('') : ''}</div></div>`;
  },
  init() { const q = $('#hpQ'); if (q) { q.onchange = () => { HP.q = q.value.trim(); rerender(); }; if (HP.q) { q.focus(); } } }
};
ON.hpToc = d => { const el = document.getElementById(d.id); if (el) el.scrollIntoView({behavior: 'smooth', block: 'start'}); };
ON.hpClear = () => { HP.q = ''; };
