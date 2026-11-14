// @author: kevinkelly
// @license: Apache Licence
// @file: p_nodes.js
// @time: 2026/10/28

/* 节点管理：FE / BE / CN / 黑名单 / 扩容 */
pages.nodes = {
  render(sub) {
    const sd = CUR.mode === 'shared-data';
    sub = sub || 'fe';
    const cnt = t => NODES.filter(n => n.type === t).length;
    return tabs('nodes', [['fe', `FE (${cnt('fe')})`], ...(sd ? [] : [['be', `BE (${cnt('be')})`]]), ['cn', `CN (${cnt('cn')})`], ['blacklist', `黑名单 (${BLACKLIST.length})`]], sub) +
      `<div class="toolbar"><span class="small muted">${sub === 'blacklist' ? '黑名单中的 BE / CN 不参与查询调度（数据副本与导入不受影响）' : '节点操作通过平台 Agent / SSH 执行启停脚本，SQL 类操作通过 FE 执行'}</span><span class="sp"></span>
      ${sub === 'blacklist' ? B('node.blacklist', '＋ 加入黑名单', {cls: 'primary'}) : B('node.scaleout', '＋ 扩容 ' + sub.toUpperCase(), {cls: 'primary', data: {t: sub}})}</div>` +
      (sub === 'blacklist' ? this.blacklist() : this.list(sub));
  },
  list(t) {
    const list = NODES.filter(n => n.type === t);
    const extra = {fe: ['角色', 'JVM 堆', 'Journal 落后'], be: ['Tablet', '数据量', 'Compaction'], cn: ['Warehouse', '缓存命中', '—']}[t];
    return `<div class="card"><div class="tbl-wrap"><table><thead><tr><th>节点</th><th>状态</th><th>版本</th><th>${extra[0]}</th><th>CPU</th><th>内存</th><th>磁盘</th><th>${extra[1]}</th><th>${extra[2]}</th><th>操作</th></tr></thead><tbody>
    ${list.map(n => `<tr><td><a data-on="node" data-id="${n.id}" class="mono">${n.host}:${t === 'fe' ? n.editLog : n.port}</a><div class="small muted">${n.id}</div></td><td>${stDot(n.status)}${n.status === 'decommission' ? `<div class="small muted">迁移 ${n.prog || 0}%</div>` : ''}</td><td>${n.version}</td>
      <td>${t === 'fe' ? `<span class="tag ${n.role === 'Leader' ? 'brand' : ''}">${n.role}</span>` : t === 'be' ? fmt(n.tablets) : n.warehouse}</td>
      <td style="width:90px">${meter(n.cpu)}</td><td style="width:90px">${meter(n.mem)}</td><td style="width:90px">${meter(n.disk, 75, 85)}</td>
      <td>${t === 'fe' ? n.jvm + '%' : t === 'be' ? `${n.dataUsed} / ${n.dataTotal} TB` : (n.cacheHit ? n.cacheHit + '%' : '—')}</td>
      <td>${t === 'fe' ? n.journal - n.replayed : t === 'be' ? `<span class="${n.compaction > 100 ? 'up b' : ''}">${n.compaction}</span>` : '—'}</td>
      <td class="ops">${n.status === 'down' ? B('node.start', '启动', {cls: 'sm', data: {id: n.id}}) : B('node.stop', '停止', {cls: 'sm', data: {id: n.id}})}${B('node.restart', '重启', {cls: 'sm', data: {id: n.id}})}
        ${t === 'fe' ? (n.role === 'Leader' ? '' : B('node.drop', '移除', {cls: 'sm danger', data: {id: n.id}})) : B('node.decommission', '下线', {cls: 'sm danger', data: {id: n.id}})}
        ${t !== 'fe' ? B('node.blacklist', '拉黑', {cls: 'sm', data: {id: n.id}}) : ''}${B('node.log', '日志', {cls: 'sm', data: {id: n.id}})}</td></tr>`).join('')}
    </tbody></table></div></div>
    <div class="notice" style="margin-top:12px"><span>ℹ</span><div>${{fe: 'FE 扩容：新节点首次启动需带 <code>--helper &lt;leader_ip&gt;:9010</code>，再执行 <code>ALTER SYSTEM ADD FOLLOWER/OBSERVER</code>。移除 Follower 后需保证剩余 Follower 为奇数个。',
      be: 'BE 缩容请使用 <b>DECOMMISSION</b>（先迁移 Tablet 副本再移除，安全）；<b>DROP</b> 会直接移除节点，副本数不足的数据可能丢失，仅在节点已不可恢复时使用。',
      cn: 'CN 无状态，可直接 DROP；存算分离下下线前建议先停止调度（加入黑名单）以等待运行中查询结束。'}[t]} ${docLink('administration/management/Scale_up_down/', '扩缩容 ↗')} ${docLink('administration/management/graceful_exit/', '优雅退出 ↗')}</div></div>`;
  },
  blacklist() {
    return `<div class="card"><div class="tbl-wrap"><table><thead><tr><th>ID</th><th>节点</th><th>类型</th><th>原因</th><th>来源</th><th>操作人</th><th>时间</th><th>操作</th></tr></thead><tbody>
    ${BLACKLIST.map(b => `<tr><td>${b.id}</td><td class="mono">${b.host}</td><td>${b.type}</td><td>${b.reason}</td><td>${b.auto ? '自动' : '手动'}</td><td>${b.by}</td><td>${b.at}</td><td>${B('node.blacklist', '移出', {cls: 'sm', data: {rm: b.id}})}</td></tr>`).join('') || '<tr><td colspan="8" class="muted">暂无</td></tr>'}
    </tbody></table></div>${code('SHOW BACKEND BLACKLIST;\nADD BACKEND BLACKLIST <be_id>;\nDELETE BACKEND BLACKLIST <be_id>;')}${docLink('administration/management/BE_blacklist/', 'BE / CN 黑名单 ↗')}</div>`;
  }
};
const nodeOf = id => NODES.find(n => n.id === id);
const ctl = n => `${n.dir.split(/[ ,]/)[0].replace(/\/(be|fe|cn)$/, '')}/${n.type}/bin`;
ACTIONS['node.start'] = d => { const n = nodeOf(d.id); n.status = 'up'; audit('node.start', `启动 ${n.id} ${n.host}`, '成功', `${ctl(n)}/start_${n.type}.sh --daemon`); toast('已启动'); rerender(); };
ACTIONS['node.stop'] = d => { const n = nodeOf(d.id); risky('node.stop', `停止 ${n.type.toUpperCase()} ${n.host}`, n.role === 'Leader' ? '停止 Leader 会触发 FE 重新选主，期间 DDL / 导入短暂不可用。' : n.type === 'be' ? '停止 BE 后其上副本不可用，持续超过 tablet_sched 阈值后会触发副本补齐。' : '停止后该节点不再提供服务。', `${ctl(n)}/stop_${n.type}.sh${n.type !== 'fe' ? ' --graceful' : ''}`, () => { n.status = 'down'; audit('node.stop', `停止 ${n.id} ${n.host}`, '成功', `${ctl(n)}/stop_${n.type}.sh`); rerender(); }); };
ACTIONS['node.restart'] = d => { const n = nodeOf(d.id); risky('node.restart', `重启 ${n.type.toUpperCase()} ${n.host}`, '重启过程中该节点上的查询会失败并由客户端重试；建议在低峰期并逐台执行。', `${ctl(n)}/stop_${n.type}.sh --graceful && ${ctl(n)}/start_${n.type}.sh --daemon`, () => { audit('node.restart', `重启 ${n.id} ${n.host}`); toast('重启指令已下发'); }); };
ACTIONS['node.decommission'] = d => {
  const n = nodeOf(d.id);
  const sql = n.type === 'be' ? `ALTER SYSTEM DECOMMISSION BACKEND "${n.host}:${n.port}";` : `ALTER SYSTEM DROP COMPUTE NODE "${n.host}:${n.port}";`;
  risky('node.decommission', `下线 ${n.type.toUpperCase()} ${n.host}`, n.type === 'be' ? `将迁移该节点上 ${fmt(n.tablets || 0)} 个 Tablet 副本到其他 BE，完成后自动移除。请确认剩余 BE 磁盘空间足够（迁移后预计平均使用率 ${Math.min(99, Math.round(NODES.filter(x => x.type === 'be').reduce((a, b) => a + b.disk, 0) / (NODES.filter(x => x.type === 'be').length - 1)))}%）。` : 'CN 无状态，将直接移除。', sql, () => {
    if (n.type === 'be') { n.status = 'decommission'; n.prog = 0; const t = setInterval(() => { n.prog += 20; if (n.prog >= 100) { clearInterval(t); NODES = NODES.filter(x => x !== n); toast(n.host + ' 下线完成'); } if (ROUTE.page === 'nodes') rerender(); }, 1500); }
    else NODES = NODES.filter(x => x !== n);
    audit('node.decommission', `下线 ${n.id} ${n.host}`, '成功', sql); rerender(); });
};
ACTIONS['node.drop'] = d => { const n = nodeOf(d.id); const sql = n.type === 'fe' ? `ALTER SYSTEM DROP ${n.role.toUpperCase()} "${n.host}:${n.editLog}";` : `ALTER SYSTEM DROP BACKEND "${n.host}:${n.port}";`;
  risky('node.drop', `移除 ${n.type.toUpperCase()} ${n.host}`, n.type === 'fe' ? (n.role === 'Follower' ? `移除后 Follower 数为 ${NODES.filter(x => x.type === 'fe' && x.role !== 'Observer').length - 1}，<b>请保证为奇数</b>，否则影响选主可用性。` : 'Observer 不参与选主，可安全移除。') : '直接移除节点，副本不足的数据可能丢失。', sql, () => { NODES = NODES.filter(x => x !== n); audit('node.drop', `移除 ${n.id} ${n.host}`, '成功', sql); rerender(); }); };
ACTIONS['node.blacklist'] = d => {
  if (d.rm) { const i = BLACKLIST.findIndex(b => b.id == d.rm); const b = BLACKLIST[i]; BLACKLIST.splice(i, 1); audit('node.blacklist', '移出黑名单 ' + b.host, '成功', `DELETE BACKEND BLACKLIST ${b.id};`); toast('已移出'); return rerender(); }
  const opts = NODES.filter(n => n.type !== 'fe' && !BLACKLIST.some(b => b.host === n.host));
  openModal({title: '加入黑名单', code: 'node.blacklist', body: `<div class="form"><label class="req">节点</label><select id="blN">${opts.map(n => `<option value="${n.id}" ${n.id === d.id ? 'selected' : ''}>${n.type.toUpperCase()} ${n.host}</option>`).join('')}</select>
    <label class="req">原因</label><input id="blR" placeholder="例如：磁盘 IO 抖动"></div><div class="notice warn" style="margin-top:10px"><span>!</span><div>黑名单节点不参与查询调度，但仍承载数据副本与导入。</div></div>`,
    onOk() { const n = nodeOf($('#blN').value); BLACKLIST.push({id: 10000 + NODES.indexOf(n), host: n.host, type: n.type.toUpperCase(), reason: $('#blR').value || '—', by: ME.login, at: now(), auto: false}); audit('node.blacklist', '加入黑名单 ' + n.host, '成功', `ADD ${n.type === 'cn' ? 'COMPUTE NODE' : 'BACKEND'} BLACKLIST ${10000 + NODES.indexOf(n)};`); go('nodes', 'blacklist'); rerender(); }});
};
ACTIONS['node.log'] = d => { const n = nodeOf(d.id); const f = n.type === 'fe' ? 'fe.log' : n.type + '.INFO';
  openDrawer(`${n.host} · ${f}`, `<div class="toolbar"><select><option>${f}</option><option>${n.type === 'fe' ? 'fe.warn.log' : n.type + '.WARNING'}</option><option>${n.type === 'fe' ? 'fe.audit.log' : n.type + '.out'}</option></select><input type="search" placeholder="关键字"><span class="small muted">实时尾随 200 行</span></div>
  <div class="log" style="height:60vh">${esc(n.type === 'fe' ? `2026-10-06 10:42:01,112 INFO (tablet scheduler|42) [TabletScheduler.schedule():312] tablet scheduler: pending 12, running 4
2026-10-06 10:42:03,904 INFO (heartbeat mgr|31) [HeartbeatMgr.runAfterCatalogReady():160] backend 10005 heartbeat ok
2026-10-06 10:42:05,551 WARN (thrift-server-pool-8|512) [Coordinator.exec():874] query 8c1f7a2e… exec time 120s exceeds 60s
2026-10-06 10:42:07,210 INFO (leaderCheckpointer|88) [Checkpoint.doCheckpoint():98] checkpoint skipped: memory not enough, used 82%` :
  `I1006 10:42:01.112 tablet_manager.cpp:1021] start to do cumulative compaction. tablet=128931 score=186
W1006 10:42:02.441 compaction_scheduler.cpp:233] compaction task num exceed limit. running=8
I1006 10:42:04.900 load_channel_mgr.cpp:118] open load channel txn_id=992312 db=ods tables=1
W1006 10:42:06.008 storage_engine.cpp:612] disk /data2/starrocks/be usage 0.87 exceeds warning level 0.85`)}</div>`, true); };

/* 扩容向导 */
ACTIONS['node.scaleout'] = d => {
  const t = d.t || 'be';
  const free = HOSTS.filter(h => h.ssh === '已连通');
  openModal({title: `扩容 ${t.toUpperCase()}`, wide: true, code: 'node.scaleout', ok: '开始扩容', body: `
    <div class="steps"><div class="st on"><b>1</b>选择主机与角色</div><div class="st"><b>2</b>环境预检</div><div class="st"><b>3</b>分发安装包 & 启动</div><div class="st"><b>4</b>加入集群</div></div>
    <div class="form">
      ${t === 'fe' ? `<label class="req">FE 角色</label><select id="soRole"><option>OBSERVER</option><option>FOLLOWER</option></select>` : ''}
      <label class="req">目标主机</label><div>${free.map(h => `<label class="chk" style="margin-right:12px"><input type="checkbox" class="soH" value="${h.ip}">${h.ip} <span class="small muted">${h.cpu}C/${h.mem}G</span></label>`).join('')}</div>
      <label class="req">安装包</label><select>${PACKAGES.filter(p => p.ver === CUR.version || p.ver.startsWith('JDK')).map(p => `<option>${p.name}</option>`).join('') || '<option>（无匹配版本安装包，请先上传）</option>'}</select>
      <label class="req">部署目录</label><input value="/data/starrocks">
      ${t !== 'fe' ? '<label>数据目录</label><input value="/data1/starrocks/' + t + ';/data2/starrocks/' + t + '">' : ''}
      <label>配置模板</label><select><option>复用集群现有 ${t}.conf（推荐）</option><option>默认模板</option></select>
    </div>
    <div class="small muted" style="margin:10px 0 4px">执行计划：</div>
    ${code(t === 'fe' ? `# 新 FE 首次启动需指定 helper（Leader 的 edit_log_port）\nbin/start_fe.sh --helper ${NODES[0].host}:9010 --daemon\nALTER SYSTEM ADD OBSERVER "<new_ip>:9010";` : t === 'be' ? `bin/start_be.sh --daemon\nALTER SYSTEM ADD BACKEND "<new_ip>:9050";\n-- 加入后 FE 自动均衡 Tablet；可在 SHOW PROC '/cluster_balance' 观察` : `bin/start_cn.sh --daemon\nALTER SYSTEM ADD COMPUTE NODE "<new_ip>:9050";`)}`,
    onOk() {
      const hs = $$('.soH').filter(x => x.checked).map(x => x.value); if (!hs.length) { toast('请选择主机'); return false; }
      hs.forEach((ip, i) => { HOSTS.find(h => h.ip === ip).ssh = '已使用'; NODES.push({id: t + '-' + (NODES.filter(n => n.type === t).length + 1), type: t, role: t === 'fe' ? (($('#soRole') || {}).value === 'FOLLOWER' ? 'Follower' : 'Observer') : undefined, host: ip, port: 9050, editLog: 9010, http: t === 'fe' ? 8030 : 8040, rpc: 9020, brpc: 8060, status: 'joining', version: CUR.version, cpu: 3, mem: 8, disk: 1, jvm: 10, journal: 0, replayed: 0, tablets: 0, dataUsed: 0, dataTotal: 4.2, compaction: 0, warehouse: 'default_warehouse', startTime: now(), hb: now(), dir: '/data/starrocks/' + t}); });
      audit('node.scaleout', `扩容 ${t.toUpperCase()} ×${hs.length}：${hs.join(', ')}`, '成功', hs.map(ip => t === 'fe' ? `ALTER SYSTEM ADD OBSERVER "${ip}:9010";` : t === 'be' ? `ALTER SYSTEM ADD BACKEND "${ip}:9050";` : `ALTER SYSTEM ADD COMPUTE NODE "${ip}:9050";`).join('\n'));
      toast('扩容任务已提交'); rerender();
      setTimeout(() => { NODES.filter(n => n.status === 'joining').forEach(n => n.status = 'up'); if (ROUTE.page === 'nodes') rerender(); }, 4000);
    }});
};
