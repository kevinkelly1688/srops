// @author: kevinkelly
// @license: Apache Licence
// @file: p_data.js
// @time: 2026/10/28

/* 备份恢复 / 导入作业 / 物化视图 / 参数配置 */
const jobTag = s => `<span class="tag ${{FINISHED: 'ok', SUCCESS: 'ok', RUNNING: 'info', UPLOADING: 'info', PENDING: 'info', PAUSED: 'warn', FAILED: 'bad', CANCELLED: 'bad'}[s] || ''}">${s}</span>`;

pages.backup = {
  render(sub) {
    sub = sub || 'jobs';
    return tabs('backup', [['jobs', '备份 / 恢复作业'], ['repos', '仓库'], ['policy', '备份策略']], sub) + this[sub]();
  },
  jobs() {
    return `<div class="toolbar">${B('backup.create', '＋ 发起备份', {cls: 'primary'})}${B('backup.restore', '⟲ 从快照恢复', {cls: 'danger'})}<span class="sp"></span><span class="small muted">${docLink('administration/management/Backup_and_restore/', '备份与恢复 ↗')}</span></div>
    <div class="card"><div class="tbl-wrap"><table><thead><tr><th>作业</th><th>类型</th><th>快照</th><th>数据库</th><th>对象</th><th>仓库</th><th>状态</th><th>进度</th><th>大小</th><th>开始</th><th>发起</th><th>操作</th></tr></thead><tbody>
    ${BACKUPS.map(b => `<tr><td class="mono">${b.id}</td><td>${b.kind}</td><td class="mono small">${b.snap}</td><td>${b.db}</td><td class="small">${b.objs}</td><td>${b.repo}</td><td>${jobTag(b.state)}</td><td style="width:90px">${meter(b.prog, 101, 101)}</td><td>${b.size}</td><td class="small">${b.start}</td><td>${b.by}</td>
      <td>${['UPLOADING', 'PENDING', 'RUNNING'].includes(b.state) ? B('backup.cancel', '取消', {cls: 'sm danger', data: {id: b.id}}) : ''}</td></tr>`).join('')}
    </tbody></table></div>${code("SHOW BACKUP FROM dws;\nSHOW RESTORE FROM dim;\nSHOW SNAPSHOT ON repo_s3_prod;")}</div>`;
  },
  repos() {
    return `<div class="toolbar">${B('backup.repo', '＋ 创建仓库', {cls: 'primary'})}</div><div class="card"><div class="tbl-wrap"><table><thead><tr><th>仓库</th><th>类型</th><th>位置</th><th>只读</th><th>快照数</th><th>创建</th><th>操作</th></tr></thead><tbody>
    ${REPOS.map(r => `<tr><td><b>${r.name}</b></td><td>${r.type}</td><td class="mono small">${r.loc}</td><td>${r.ro ? '是' : '否'}</td><td>${r.snaps}</td><td>${r.created}</td><td>${B('backup.repo', '删除', {cls: 'sm danger', data: {del: r.name}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  policy() {
    return `<div class="toolbar">${B('backup.policy', '＋ 新建策略', {cls: 'primary'})}<span class="sp"></span><span class="small muted">平台按策略定时发起 BACKUP SNAPSHOT，并按保留份数清理旧快照。</span></div><div class="card"><div class="tbl-wrap"><table><thead><tr><th>策略</th><th>数据库</th><th>周期</th><th>仓库</th><th>保留</th><th>状态</th><th>操作</th></tr></thead><tbody>
    ${BACKUP_POLICIES.map((p, i) => `<tr><td><b>${p.name}</b></td><td>${p.dbs}</td><td>${p.cron}</td><td>${p.repo}</td><td>${p.keep}</td><td>${p.on ? '<span class="tag ok">启用</span>' : '<span class="tag">停用</span>'}</td><td>${B('backup.policy', p.on ? '停用' : '启用', {cls: 'sm', data: {i}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  }
};
ACTIONS['backup.create'] = () => openModal({title: '发起备份', code: 'backup.create', body: `<div class="form"><label class="req">数据库</label><select id="bkDb"><option>dws</option><option>ads</option><option>dim</option><option>ods</option></select>
  <label class="req">备份对象</label><input id="bkObj" placeholder="留空表示整库；多个表用逗号分隔">
  <label class="req">快照名</label><input id="bkSnap" value="snap_dws_${now().slice(0, 10).replace(/-/g, '')}"><label class="req">仓库</label><select id="bkRepo">${REPOS.filter(r => !r.ro).map(r => `<option>${r.name}</option>`).join('')}</select></div>
  <div class="notice warn" style="margin-top:10px"><span>!</span><div>同一数据库同时只能有一个备份 / 恢复作业。备份期间不能对相关表做 Schema Change。</div></div>`,
  onOk() { const db = $('#bkDb').value, obj = $('#bkObj').value, snap = $('#bkSnap').value, repo = $('#bkRepo').value;
    const sql = `BACKUP SNAPSHOT ${db}.${snap}\nTO ${repo}${obj ? `\nON (${obj})` : ''};`;
    BACKUPS.unshift({id: 'BK-' + (25 + BACKUPS.length), kind: '备份', snap, db, objs: obj || '整库', repo, state: 'PENDING', prog: 0, size: '—', start: now(), by: ME.login});
    audit('backup.create', '发起备份 ' + snap, '成功', sql); rerender(); }});
ACTIONS['backup.restore'] = () => openModal({title: '从快照恢复', code: 'backup.restore', okCls: 'danger primary', ok: '下一步', body: `<div class="form"><label class="req">仓库</label><select>${REPOS.map(r => `<option>${r.name}</option>`).join('')}</select>
  <label class="req">快照</label><select id="rsSnap"><option>snap_ads_20261006</option><option>snap_dws_20261005</option><option>snap_dim_20261001</option></select>
  <label class="req">恢复对象</label><input id="rsObj" value="dim_user AS dim_user_restore"><div class="hint">建议用 AS 恢复为新表再核对，避免覆盖线上表。</div>
  <label>副本数</label><input value="3"></div>`,
  onOk() { const snap = $('#rsSnap').value, obj = $('#rsObj').value; const sql = `RESTORE SNAPSHOT dim.${snap}\nFROM repo_s3_prod\nON (${obj})\nPROPERTIES ("backup_timestamp" = "2026-10-01-02-00-00-123", "replication_num" = "3");`;
    setTimeout(() => risky('backup.restore', '恢复数据', '恢复会写入目标表；若目标表已存在且非 AS 新表，将覆盖其数据。', sql, () => { BACKUPS.unshift({id: 'RS-0' + (8 + BACKUPS.length), kind: '恢复', snap, db: 'dim', objs: obj, repo: 'repo_s3_prod', state: 'RUNNING', prog: 5, size: '—', start: now(), by: ME.login}); audit('backup.restore', '恢复 ' + snap, '成功', sql); rerender(); }), 50); }});
ACTIONS['backup.cancel'] = d => { const b = BACKUPS.find(x => x.id === d.id); b.state = 'CANCELLED'; audit('backup.cancel', '取消作业 ' + b.id, '成功', `CANCEL BACKUP FROM ${b.db};`); rerender(); };
ACTIONS['backup.repo'] = d => {
  if (d.del) return risky('backup.repo', '删除仓库 ' + d.del, '只删除仓库映射，不删除远端存储中的快照数据。', `DROP REPOSITORY ${d.del};`, () => { REPOS.splice(REPOS.findIndex(r => r.name === d.del), 1); audit('backup.repo', '删除仓库 ' + d.del, '成功', `DROP REPOSITORY ${d.del};`); rerender(); });
  openModal({title: '创建仓库', code: 'backup.repo', body: `<div class="form"><label class="req">名称</label><input id="rpN" value="repo_oss_bak"><label class="req">类型</label><select><option>S3 / 兼容 S3（OSS / COS / MinIO）</option><option>HDFS</option><option>GCS</option><option>Azure Blob</option></select>
    <label class="req">位置</label><input id="rpL" class="mono" value="s3://sr-backup/prod-sr-01-v2"><label class="req">Access Key</label><input type="password" placeholder="平台加密保存"><label class="req">Secret Key</label><input type="password"><label>Endpoint</label><input value="https://oss-cn-hangzhou.aliyuncs.com"><label>只读</label><label class="chk"><input type="checkbox"> 只读仓库（仅用于恢复）</label></div>`,
    onOk() { const n = $('#rpN').value, l = $('#rpL').value; REPOS.push({name: n, type: 'S3', loc: l, ro: false, snaps: 0, created: now().slice(0, 10)}); audit('backup.repo', '创建仓库 ' + n, '成功', `CREATE REPOSITORY ${n}\nWITH BROKER\nON LOCATION "${l}"\nPROPERTIES ("aws.s3.access_key" = "***", "aws.s3.secret_key" = "***", "aws.s3.endpoint" = "...");`); rerender(); }});
};
ACTIONS['backup.policy'] = d => {
  if (d.i != null) { const p = BACKUP_POLICIES[d.i]; p.on = !p.on; audit('backup.policy', (p.on ? '启用' : '停用') + '备份策略 ' + p.name); return rerender(); }
  openModal({title: '新建备份策略', code: 'backup.policy', body: `<div class="form"><label class="req">名称</label><input id="bpN" value="明细库每日备份"><label class="req">数据库</label><input id="bpD" value="dwd"><label class="req">周期</label><select id="bpC"><option>每天 02:00</option><option>每天 04:00</option><option>每周日 03:00</option></select><label class="req">仓库</label><select id="bpR">${REPOS.map(r => `<option>${r.name}</option>`).join('')}</select><label>保留份数</label><input type="number" id="bpK" value="14"></div>`,
    onOk() { BACKUP_POLICIES.push({name: $('#bpN').value, dbs: $('#bpD').value, cron: $('#bpC').value, repo: $('#bpR').value, keep: '保留 ' + $('#bpK').value + ' 份', on: true}); audit('backup.policy', '新建备份策略 ' + $('#bpN').value); rerender(); }});
};

/* ---------- 导入作业 ---------- */
pages.load = {
  render() {
    const rl = LOADS.filter(l => l.type === 'Routine Load');
    return `<div class="grid g4">${[['Routine Load', `${rl.filter(l => l.state === 'RUNNING').length} 运行 / ${rl.filter(l => l.state === 'PAUSED').length} 暂停`], ['导入速率', '4.6 万行/s'], ['24h 导入失败', '17 / 2,431 (0.7%)'], ['运行中事务', '312（ods 库最高）']].map(([l, v]) => `<div class="card kpi"><div class="l">${l}</div><div class="v" style="font-size:20px">${v}</div></div>`).join('')}</div>
    <div class="card" style="margin-top:16px"><h2>导入作业 <span class="sub">SHOW ALL ROUTINE LOAD · SHOW PIPES · information_schema.loads</span></h2>
    <div class="tbl-wrap"><table><thead><tr><th>作业</th><th>库</th><th>类型</th><th>数据源</th><th>状态</th><th>延迟（Lag）</th><th>速率</th><th>错误率</th><th>操作</th></tr></thead><tbody>
    ${LOADS.map(l => `<tr><td><b>${l.name}</b>${l.reason ? `<div class="small up">${esc(l.reason)}</div>` : ''}</td><td>${l.db}</td><td>${l.type}</td><td class="small mono">${l.src}</td><td>${jobTag(l.state)}</td><td>${l.lag}</td><td>${l.rate}</td><td>${l.err}</td>
      <td class="ops">${l.type === 'Routine Load' || l.type === 'Pipe' ? (l.state === 'RUNNING' ? B('load.pause', '暂停', {cls: 'sm', data: {n: l.name}}) : l.state === 'PAUSED' ? B('load.resume', '恢复', {cls: 'sm', data: {n: l.name}}) : '') + B('load.stop', '停止', {cls: 'sm danger', data: {n: l.name}}) : ''}${l.reason || l.err !== '0%' ? B('load.detail', '错误详情', {cls: 'sm', data: {n: l.name}}) : ''}</td></tr>`).join('')}
    </tbody></table></div></div>
    <div class="notice" style="margin-top:12px"><span>ℹ</span><div>Routine Load 暂停后修复数据源问题再 RESUME；STOP 后作业不可恢复，需要重新创建。${docLink('loading/RoutineLoad/', 'Routine Load ↗')} ${docLink('loading/', '导入总览 ↗')}</div></div>`;
  }
};
const ld = n => LOADS.find(l => l.name === n);
const ldSql = (op, l) => l.type === 'Pipe' ? `ALTER PIPE ${l.db}.${l.name} ${op === 'STOP' ? 'DROP' : op};` : `${op} ROUTINE LOAD FOR ${l.db}.${l.name};`;
ACTIONS['load.pause'] = d => { const l = ld(d.n); l.state = 'PAUSED'; l.rate = '0'; audit('load.pause', '暂停 ' + l.name, '成功', ldSql('PAUSE', l)); rerender(); };
ACTIONS['load.resume'] = d => { const l = ld(d.n); l.state = 'RUNNING'; l.reason = ''; l.rate = '—'; audit('load.resume', '恢复 ' + l.name, '成功', ldSql('RESUME', l)); toast('已恢复'); rerender(); };
ACTIONS['load.stop'] = d => { const l = ld(d.n); risky('load.stop', '停止 ' + l.name, 'STOP 后作业不可恢复，只能重新创建；已消费 offset 记录将丢失。', ldSql('STOP', l), () => { LOADS.splice(LOADS.indexOf(l), 1); audit('load.stop', '停止 ' + l.name, '成功', ldSql('STOP', l)); rerender(); }); };
ACTIONS['load.detail'] = d => { const l = ld(d.n); openDrawer(l.name + ' 错误详情', `<div class="notice bad"><span>!</span><div>${esc(l.reason || '存在被过滤行')}</div></div>${code(l.type === 'Routine Load' ? `SHOW ROUTINE LOAD FOR ${l.db}.${l.name}\\G\n-- ReasonOfStateChanged / ErrorLogUrls\nSHOW ROUTINE LOAD TASK WHERE JobName = "${l.name}";` : `SELECT * FROM information_schema.loads WHERE LABEL = '${l.name}';\n-- 查看 ERROR_MSG / TRACKING_URL`)}
  <h4>常见原因</h4><ul class="small"><li>offset out of range：Kafka 数据已过期，ALTER ROUTINE LOAD 设置 kafka_offsets 后 RESUME</li><li>too many filtered rows：数据格式 / 类型不匹配，调大 max_filter_ratio 或修正源数据</li><li>too many versions：导入过于频繁，Compaction 跟不上</li></ul>`); };

/* ---------- 物化视图 ---------- */
pages.mv = {
  render() {
    return `<div class="card"><h2>异步物化视图 <span class="sub">information_schema.materialized_views · task_runs</span></h2><div class="tbl-wrap"><table><thead><tr><th>物化视图</th><th>刷新方式</th><th>分区</th><th>状态</th><th>最近刷新</th><th>耗时</th><th>行数 / 大小</th><th>7 天改写命中</th><th>操作</th></tr></thead><tbody>
    ${MVS.map(m => `<tr><td><b>${m.name}</b>${m.err ? `<div class="small up">${esc(m.err)}</div>` : ''}</td><td class="small mono">${m.refresh}</td><td>${m.part}</td><td>${m.active ? '' : '<span class="tag bad">INACTIVE</span> '}${jobTag(m.state)}</td><td>${m.last}</td><td>${m.dur}</td><td class="small">${m.rows} / ${m.size}</td><td>${fmt(m.hit)}</td>
      <td class="ops">${m.state === 'RUNNING' ? B('mv.cancel', '取消', {cls: 'sm', data: {n: m.name}}) : B('mv.refresh', '刷新', {cls: 'sm', data: {n: m.name}})}${B('mv.toggle', m.active ? '停用' : '启用', {cls: 'sm', data: {n: m.name}})}</td></tr>`).join('')}
    </tbody></table></div></div>
    <div class="notice" style="margin-top:12px"><span>ℹ</span><div>基表 Schema Change 会使物化视图变为 INACTIVE，需 <code>ALTER MATERIALIZED VIEW &lt;mv&gt; ACTIVE</code> 重新激活。刷新内存不足可设置 <code>"session.query_mem_limit"</code> 或开启 spill。${docLink('using_starrocks/async_mv/Materialized_view/', '异步物化视图 ↗')}</div></div>`;
  }
};
const mvOf = n => MVS.find(m => m.name === n);
ACTIONS['mv.refresh'] = d => openModal({title: '刷新 ' + d.n, code: 'mv.refresh', body: `<div class="form"><label>刷新范围</label><select id="mvR"><option value="">全部分区</option><option value=" PARTITION START ('2026-10-01') END ('2026-10-07')">指定分区区间</option></select><label>强制</label><label class="chk"><input type="checkbox" id="mvF"> FORCE（忽略基表是否变化）</label><label>同步执行</label><label class="chk"><input type="checkbox"> WITH SYNC MODE</label></div>`,
  onOk() { const m = mvOf(d.n); m.state = 'RUNNING'; m.err = ''; const sql = `REFRESH MATERIALIZED VIEW ${m.name}${$('#mvR').value}${$('#mvF').checked ? ' FORCE' : ''};`; audit('mv.refresh', '刷新 ' + m.name, '成功', sql); rerender(); setTimeout(() => { m.state = 'SUCCESS'; m.last = now().slice(11); m.dur = '1m48s'; if (ROUTE.page === 'mv') rerender(); }, 3000); }});
ACTIONS['mv.toggle'] = d => { const m = mvOf(d.n); m.active = !m.active; if (m.active) m.err = ''; const sql = `ALTER MATERIALIZED VIEW ${m.name} ${m.active ? 'ACTIVE' : 'INACTIVE'};`; audit('mv.toggle', sql, '成功', sql); rerender(); };
ACTIONS['mv.cancel'] = d => { const m = mvOf(d.n); m.state = 'CANCELLED'; audit('mv.cancel', '取消刷新 ' + m.name, '成功', `CANCEL REFRESH MATERIALIZED VIEW ${m.name};`); rerender(); };

/* ---------- 参数配置 ---------- */
pages.param = {
  render(sub) {
    sub = sub || 'fe';
    return tabs('param', [['fe', 'FE 配置'], ['be', BE_LABEL()], ['var', '全局变量'], ['history', '变更历史']], sub) + (sub === 'history' ? this.history() : this.list(sub));
  },
  list(t) {
    const how = {fe: 'ADMIN SET FRONTEND CONFIG（仅对执行的 FE 生效 / 平台对全部 FE 逐台执行），不持久化；需持久化请同时写入 fe.conf', be: 'BE HTTP 接口 POST /api/update_config?k=v（加 &persist=true 持久化到 be.conf）', var: 'SET GLOBAL 立即对新会话生效并持久化'}[t];
    return `<div class="toolbar"><input type="search" placeholder="搜索参数名" id="pmQ"><span class="sp"></span>${t !== 'var' ? B('param.compare', '节点参数对比') : ''}</div>
    <div class="notice"><span>ℹ</span><div>修改方式：${how}。${docLink(t === 'var' ? 'sql-reference/System_variable/' : `administration/management/${t.toUpperCase()}_configuration/`, '参数说明 ↗')}</div></div>
    <div class="card"><div class="tbl-wrap"><table id="pmT"><thead><tr><th>参数</th><th>当前值</th><th>默认值</th><th>动态</th><th>说明</th><th>操作</th></tr></thead><tbody>
    ${PARAMS[t].map(p => `<tr data-k="${p.k}"><td class="mono"><b>${p.k}</b></td><td class="mono ${p.v !== p.def ? 'b' : ''}">${p.v}${p.v !== p.def ? ' <span class="tag info">已修改</span>' : ''}</td><td class="mono small muted">${p.def}</td><td>${p.mutable ? '<span class="tag ok">是</span>' : '<span class="tag warn">需重启</span>'}</td><td class="small">${p.desc}</td>
      <td>${B('param.edit', '修改', {cls: 'sm', data: {t, k: p.k}})}</td></tr>`).join('')}
    </tbody></table></div></div>`;
  },
  history() {
    return `<div class="card">${can('param.history') ? `<div class="tbl-wrap"><table><thead><tr><th>时间</th><th>用户</th><th>范围</th><th>参数</th><th>旧值</th><th>新值</th><th>持久化</th><th>状态</th></tr></thead><tbody>
    ${PARAM_HISTORY.map(h => `<tr><td>${h.at}</td><td>${h.user}</td><td>${h.scope}</td><td class="mono">${h.k}</td><td class="mono">${h.from}</td><td class="mono b">${h.to}</td><td class="small">${h.persist}</td><td><span class="tag ok">${h.st}</span></td></tr>`).join('')}
    </tbody></table></div>` : '<div class="muted">🔒 需要「查看变更历史」(param.history) 权限</div>'}</div>`;
  },
  init() { const q = $('#pmQ'); if (q) q.oninput = () => $$('#pmT tbody tr').forEach(tr => tr.style.display = tr.dataset.k.includes(q.value) ? '' : 'none'); }
};
const BE_LABEL = () => CUR.mode === 'shared-data' ? 'CN 配置' : 'BE 配置';
ACTIONS['param.edit'] = d => {
  const p = PARAMS[d.t].find(x => x.k === d.k);
  const nodes = NODES.filter(n => n.type === (d.t === 'fe' ? 'fe' : CUR.mode === 'shared-data' ? 'cn' : 'be'));
  openModal({title: '修改参数 ' + p.k, code: 'param.edit', body: `<div class="form"><label>当前值</label><input value="${esc(p.v)}" disabled><label class="req">新值</label><input id="pmV" class="mono" value="${esc(p.v)}">
    ${d.t !== 'var' ? `<label>作用范围</label><select id="pmS"><option value="all">全部 ${d.t.toUpperCase()}（${nodes.length}）</option>${nodes.map(n => `<option value="${n.id}">${n.id} ${n.host}</option>`).join('')}</select>
    <label>持久化</label><label class="chk"><input type="checkbox" id="pmP" ${can('param.persist') ? '' : 'disabled'}> 同时写入 ${d.t}.conf ${can('param.persist') ? '' : '<span class="small muted">（🔒 需 param.persist）</span>'}</label>` : ''}</div>
    ${p.mutable ? '' : '<div class="notice warn" style="margin-top:10px"><span>!</span><div>静态参数：只能写入配置文件，<b>重启节点后生效</b>。</div></div>'}`,
    onOk() {
      const v = $('#pmV').value; if (v === p.v) { toast('值未变化'); return false; }
      const scope = $('#pmS') ? $('#pmS').value : 'GLOBAL', persist = $('#pmP') && $('#pmP').checked;
      const sql = d.t === 'fe' ? `ADMIN SET FRONTEND CONFIG ("${p.k}" = "${v}");` : d.t === 'be' ? `curl -XPOST http://<be_host>:8040/api/update_config?${p.k}=${v}${persist ? '&persist=true' : ''}` : `SET GLOBAL ${p.k} = ${v};`;
      setTimeout(() => risky('param.edit', '修改参数 ' + p.k, `${p.v} → <b>${esc(v)}</b>，范围：${scope === 'all' ? '全部节点' : scope}`, sql, () => {
        PARAM_HISTORY.unshift({at: now(), user: ME.login, scope: d.t === 'var' ? 'GLOBAL 变量' : (scope === 'all' ? `${d.t.toUpperCase()}（全部）` : scope), k: p.k, from: p.v, to: v, persist: d.t === 'var' ? '—' : persist ? `已写入 ${d.t}.conf` : '仅内存', st: p.mutable ? '已生效' : '待重启'});
        p.v = v; audit('param.edit', `${p.k} ${PARAM_HISTORY[0].from} → ${v}`, '成功', sql); rerender(); }), 50);
    }});
};
ACTIONS['param.compare'] = () => {
  const t = ROUTE.sub === 'be' ? (CUR.mode === 'shared-data' ? 'cn' : 'be') : 'fe';
  const ns = NODES.filter(n => n.type === t), key = t === 'fe' ? PARAMS.fe : PARAMS.be;
  openDrawer('节点参数对比', `<div class="small muted" style="margin-bottom:8px">高亮表示与多数节点不一致</div><div class="tbl-wrap"><table><thead><tr><th>参数</th>${ns.map(n => `<th>${n.id}</th>`).join('')}</tr></thead><tbody>
  ${key.map(p => `<tr><td class="mono small">${p.k}</td>${ns.map(n => { const diff = p.k === 'cumulative_compaction_num_threads_per_disk' && n.id === 'be-4'; return `<td class="mono small" style="${diff ? 'background:var(--warn-soft);font-weight:600' : ''}">${diff ? '2' : p.v}</td>`; }).join('')}</tr>`).join('')}
  </tbody></table></div>`, true);
};
