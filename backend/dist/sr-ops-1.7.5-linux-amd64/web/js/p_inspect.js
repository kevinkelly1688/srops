/* 性能巡检：结果 / 历史 / 定时与规则 */
const INS = {cat: '全部', st: '全部', running: false, ignored: new Set(), sched: {on: true, cron: '每天 10:00', cats: INSPECT_CATS.slice(), notify: '运维值班群'}};
const insScore = () => { const it = INSPECT_ITEMS.filter(i => !INS.ignored.has(i.name)); return Math.max(0, 100 - it.filter(i => i.st === 'bad').length * 4 - it.filter(i => i.st === 'warn').length); };

pages.inspect = {
  render(sub) {
    sub = sub || 'result';
    return tabs('inspect', [['result', '巡检结果'], ['history', '巡检历史'], ['rule', '定时与规则']], sub) + (sub === 'history' ? this.history() : sub === 'rule' ? this.rule() : this.result());
  },
  result() {
    const items = INSPECT_ITEMS.filter(i => (INS.cat === '全部' || i.cat === INS.cat) && (INS.st === '全部' || i.st === INS.st));
    const valid = INSPECT_ITEMS.filter(i => !INS.ignored.has(i.name));
    const cnt = st => valid.filter(i => i.st === st).length;
    return `
    <div class="grid g-main">
      <div class="card"><div class="score">${ring(insScore())}
        <div style="flex:1"><div class="row"><b style="font-size:16px">${CUR.name} 性能巡检</b><span class="tag">${INSPECT_HISTORY[0].id}</span></div>
        <div class="small muted">${INSPECT_HISTORY[0].at} · ${INSPECT_HISTORY[0].by} · ${INSPECT_ITEMS.length} 项 / ${INSPECT_CATS.length} 类 · 数据来源：SQL（SHOW PROC / information_schema）、Prometheus 指标、主机 SSH 采集</div>
        <div class="row" style="margin-top:8px"><span class="tag bad">异常 ${cnt('bad')}</span><span class="tag warn">警告 ${cnt('warn')}</span><span class="tag info">建议 ${cnt('info')}</span><span class="tag ok">正常 ${cnt('ok')}</span>${INS.ignored.size ? `<span class="tag">已忽略 ${INS.ignored.size}</span>` : ''}</div></div>
        <div class="row" style="align-self:flex-start">${B('inspect.run', INS.running ? '巡检中…' : '▶ 立即巡检', {cls: 'primary'})}${B('inspect.export', '⇩ 导出报告')}</div></div>
        ${INS.running ? `<div class="log" id="insLog" style="height:150px;margin-top:12px"></div>` : ''}
      </div>
      <div class="card"><h2>分类得分</h2>${INSPECT_CATS.map(c => { const l = INSPECT_ITEMS.filter(i => i.cat === c); const b = l.filter(i => i.st === 'bad').length, w = l.filter(i => i.st === 'warn').length;
        return `<div class="row between small" style="padding:3px 0"><a data-on="insCat" data-c="${c}">${c}</a><span>${b ? `<span class="tag bad">${b}</span> ` : ''}${w ? `<span class="tag warn">${w}</span>` : ''}${!b && !w ? '<span class="tag ok">正常</span>' : ''}</span></div>`; }).join('')}</div>
    </div>
    <div class="card" style="margin-top:16px">
      <div class="toolbar"><b>巡检明细</b>
        <select data-on="noop" id="insCatSel">${['全部'].concat(INSPECT_CATS).map(c => `<option ${c === INS.cat ? 'selected' : ''}>${c}</option>`).join('')}</select>
        <select id="insStSel">${[['全部', '全部状态'], ['bad', '异常'], ['warn', '警告'], ['info', '建议'], ['ok', '正常']].map(([v, t]) => `<option value="${v}" ${v === INS.st ? 'selected' : ''}>${t}</option>`).join('')}</select>
        <span class="sp"></span><span class="small muted">点击行查看检查方法与处置建议</span></div>
      <div class="tbl-wrap"><table><thead><tr><th>分类</th><th>巡检项</th><th>当前值</th><th>阈值</th><th>结果</th><th>处置建议</th><th>操作</th></tr></thead><tbody>
      ${items.map(i => `<tr class="click" data-on="insItem" data-n="${esc(i.name)}" style="${INS.ignored.has(i.name) ? 'opacity:.45' : ''}"><td class="small muted">${i.cat}</td><td><b>${i.name}</b></td><td>${i.val}</td><td class="small muted">${i.th}</td><td>${tagOf(i.st)}</td>
        <td class="small" style="max-width:380px">${i.adv}</td><td>${i.st !== 'ok' ? B('inspect.ignore', INS.ignored.has(i.name) ? '取消忽略' : '忽略', {cls: 'sm', data: {n: i.name}}) : ''}</td></tr>`).join('')}
      </tbody></table></div></div>`;
  },
  history() {
    return `<div class="card"><h2>巡检历史 <span class="sub">保留 90 天</span></h2>
      ${lineChart([{name: '健康分', color: 'var(--c2)', data: INSPECT_HISTORY.map(h => h.score).reverse().concat([insScore()])}], {h: 140, xl: ['10-04', '10-04', '10-05', '10-06', '现在']})}
      <div class="tbl-wrap" style="margin-top:10px"><table><thead><tr><th>报告编号</th><th>时间</th><th>触发</th><th>健康分</th><th>异常</th><th>警告</th><th>耗时</th><th>操作</th></tr></thead><tbody>
      ${INSPECT_HISTORY.map(h => `<tr><td class="mono">${h.id}</td><td>${h.at}</td><td>${h.by}</td><td><b>${h.score}</b></td><td>${h.bad}</td><td>${h.warn}</td><td>${h.dur}</td><td class="ops"><a href="#inspect/result">查看</a> ${B('inspect.export', '导出', {cls: 'sm'})}</td></tr>`).join('')}
      </tbody></table></div></div>`;
  },
  rule() {
    const s = INS.sched;
    return `<div class="grid g2">
      <div class="card"><h2>定时巡检 <span class="right">${B('inspect.schedule', '编辑', {cls: 'sm'})}</span></h2>
        ${[['状态', s.on ? '<span class="tag ok">已启用</span>' : '<span class="tag">已停用</span>'], ['执行周期', s.cron], ['巡检范围', s.cats.length === INSPECT_CATS.length ? '全部分类' : s.cats.join('、')], ['结果通知', s.notify + '（仅有异常时通知）'], ['报告保留', '90 天']].map(([k, v]) => `<div class="row between small" style="padding:6px 0;border-bottom:1px solid var(--border)"><span class="muted">${k}</span><span>${v}</span></div>`).join('')}
      </div>
      <div class="card"><h2>健康分计算</h2><div class="small">满分 100：每个<span class="tag bad">异常</span>扣 4 分，每个<span class="tag warn">警告</span>扣 1 分，<span class="tag info">建议</span>不扣分；被忽略的项不计分。<br>≥ 85 健康 · 70–84 关注 · &lt; 70 风险。</div></div>
    </div>
    <div class="card" style="margin-top:16px"><h2>巡检规则 / 阈值 <span class="sub">${INSPECT_ITEMS.length} 条内置规则</span><span class="right">${B('inspect.rule', '＋ 自定义规则', {cls: 'sm'})}</span></h2>
      <div class="tbl-wrap"><table><thead><tr><th>分类</th><th>规则</th><th>检查方法（数据来源）</th><th>阈值</th><th>参考文档</th><th>操作</th></tr></thead><tbody>
      ${INSPECT_ITEMS.map(i => `<tr><td class="small muted">${i.cat}</td><td>${i.name}</td><td class="mono small">${esc(i.src)}</td><td class="small">${i.th}</td><td>${docLink(i.doc)}</td><td>${B('inspect.rule', '改阈值', {cls: 'sm', data: {n: i.name}})}</td></tr>`).join('')}
      </tbody></table></div></div>`;
  },
  init() {
    const a = $('#insCatSel'), b = $('#insStSel');
    if (a) a.onchange = () => { INS.cat = a.value; rerender(); };
    if (b) b.onchange = () => { INS.st = b.value; rerender(); };
  }
};
ON.insCat = d => { INS.cat = d.c; INS.st = '全部'; rerender(); };
ON.insItem = (d, el, e) => {
  if (e.target.closest('button')) return;
  const i = INSPECT_ITEMS.find(x => x.name === d.n);
  openDrawer(i.name, `<div class="row">${tagOf(i.st)}<span class="tag">${i.cat}</span></div>
    <h4>当前值</h4><div>${i.val}（阈值：${i.th}）</div>
    <h4>检查方法</h4>${code(i.src)}
    <h4>处置建议</h4><div>${i.adv}</div>
    <h4>参考</h4>${docLink(i.doc, 'StarRocks 官方文档 ↗')}`);
};
ACTIONS['inspect.run'] = () => {
  if (INS.running) return;
  INS.running = true; rerender();
  const steps = INSPECT_CATS.map((c, k) => `[${k + 1}/${INSPECT_CATS.length}] 检查 ${c} …`).concat(['汇总结果，计算健康分 …', '完成：已生成报告 IR-' + now().replace(/[-: ]/g, '').slice(0, 12)]);
  let k = 0;
  const t = setInterval(() => {
    const log = $('#insLog'); if (log) { log.textContent += steps[k] + (k < INSPECT_CATS.length ? ' ok\n' : '\n'); log.scrollTop = 1e9; }
    if (++k >= steps.length) {
      clearInterval(t); INS.running = false;
      INSPECT_HISTORY.unshift({id: 'IR-' + now().replace(/[-: ]/g, '').slice(0, 12), at: now(), by: ME.login, score: insScore(), bad: INSPECT_ITEMS.filter(i => i.st === 'bad').length, warn: INSPECT_ITEMS.filter(i => i.st === 'warn').length, dur: '38s'});
      audit('inspect.run', '执行性能巡检（全部 ' + INSPECT_CATS.length + ' 类）'); toast('巡检完成'); setTimeout(rerender, 400);
    }
  }, 280);
};
ACTIONS['inspect.export'] = () => { audit('inspect.export', '导出巡检报告'); toast('导出巡检报告（浏览器打印为 PDF）'); setTimeout(() => window.print(), 300); };
ACTIONS['inspect.ignore'] = d => { INS.ignored.has(d.n) ? INS.ignored.delete(d.n) : INS.ignored.add(d.n); audit('inspect.ignore', (INS.ignored.has(d.n) ? '忽略' : '取消忽略') + '巡检项：' + d.n); rerender(); };
ACTIONS['inspect.schedule'] = () => {
  const s = INS.sched;
  openModal({title: '定时巡检配置', code: 'inspect.schedule', body: `<div class="form">
    <label>启用</label><label class="chk" style="justify-content:flex-start"><input type="checkbox" id="sOn" ${s.on ? 'checked' : ''}> 按周期自动巡检</label>
    <label class="req">执行周期</label><select id="sCron">${['每天 10:00', '每天 02:00', '每 6 小时', '每周一 09:00'].map(x => `<option ${x === s.cron ? 'selected' : ''}>${x}</option>`).join('')}</select>
    <label>巡检范围</label><div>${INSPECT_CATS.map(c => `<label class="chk" style="margin-right:10px"><input type="checkbox" class="sCat" value="${c}" ${s.cats.includes(c) ? 'checked' : ''}>${c}</label>`).join('')}</div>
    <label>结果通知</label><select id="sNt">${CHANNELS.map(c => `<option ${c.name === s.notify ? 'selected' : ''}>${c.name}</option>`).join('')}</select></div>`,
    onOk() { s.on = $('#sOn').checked; s.cron = $('#sCron').value; s.cats = $$('.sCat').filter(x => x.checked).map(x => x.value); s.notify = $('#sNt').value; audit('inspect.schedule', '修改定时巡检：' + s.cron); toast('已保存'); rerender(); }});
};
ACTIONS['inspect.rule'] = d => {
  const i = d.n ? INSPECT_ITEMS.find(x => x.name === d.n) : null;
  openModal({title: i ? '修改阈值：' + i.name : '新建自定义巡检规则', code: 'inspect.rule', body: `<div class="form">
    <label class="req">规则名称</label><input value="${i ? esc(i.name) : ''}" ${i ? 'disabled' : ''}>
    <label>分类</label><select>${INSPECT_CATS.map(c => `<option ${i && i.cat === c ? 'selected' : ''}>${c}</option>`).join('')}</select>
    <label class="req">检查方式</label><select><option>SQL 查询</option><option>Prometheus 表达式</option><option>主机命令（SSH）</option></select>
    <label class="req">表达式 / SQL</label><textarea rows="3" class="mono">${i ? esc(i.src) : "SELECT COUNT(*) FROM information_schema.partitions_meta WHERE REPLICATION_NUM < 3"}</textarea>
    <label>警告阈值</label><input value="${i ? esc(i.th) : '> 0'}"><label>异常阈值</label><input placeholder="例如 > 10">
    <label>处置建议</label><textarea rows="2">${i ? esc(i.adv) : ''}</textarea></div>`,
    onOk() { audit('inspect.rule', (i ? '修改阈值：' + i.name : '新建巡检规则')); toast('已保存'); }});
};
