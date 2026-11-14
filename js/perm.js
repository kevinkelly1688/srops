// @author: kevinkelly
// @license: Apache Licence
// @file: perm.js
// @time: 2026/10/28

/* ============================================================================
   权限模型（原型）
   - 菜单 = 页面；每个页面有一个「查看」权限（<page>:view）和若干按钮权限。
   - 按钮权限码格式：<模块>.<动作>，例如 node.decommission。页面里每个按钮都通过 B() 渲染，
     B() 根据当前用户是否拥有该权限码决定：正常 / 置灰（带锁）/ 隐藏（平台设置可切换）。
   - risk: high 表示高危操作，执行时需要二次确认（输入集群名），可配置为需要双人复核。
   - 角色 = 权限码集合 + 数据范围（可操作的集群）；用户可以拥有多个角色，权限取并集。
   ========================================================================== */
const PERM_TREE = [
  {group: '概览', menus: [
    {id: 'overview', ico: '◧', name: '集群总览', btns: [
      ['overview.refresh', '刷新数据'], ['overview.export', '导出概览报告']]},
    {id: 'topology', ico: '◈', name: '集群架构', btns: [
      ['topology.detail', '查看节点详情']]},
  ]},
  {group: '性能与诊断', menus: [
    {id: 'inspect', ico: '✚', name: '性能巡检', btns: [
      ['inspect.run', '立即巡检'], ['inspect.schedule', '定时巡检配置'], ['inspect.rule', '巡检规则 / 阈值编辑'],
      ['inspect.export', '导出巡检报告'], ['inspect.ignore', '忽略巡检项']]},
    {id: 'query', ico: '⌘', name: '查询分析', btns: [
      ['query.kill', '终止查询 (KILL)', 'high'], ['query.profile', '查看 Query Profile'], ['query.explain', '执行计划分析'],
      ['query.export', '导出慢查询'], ['query.blacklist', 'SQL 黑名单管理', 'high']]},
    {id: 'resource', ico: '◫', name: '资源隔离', btns: [
      ['rg.create', '新建资源组'], ['rg.edit', '编辑资源组'], ['rg.delete', '删除资源组', 'high'], ['queue.edit', '查询队列配置']]},
  ]},
  {group: '集群运维', menus: [
    {id: 'nodes', ico: '▤', name: '节点管理', btns: [
      ['node.start', '启动节点'], ['node.stop', '停止节点', 'high'], ['node.restart', '重启节点', 'high'],
      ['node.scaleout', '扩容节点'], ['node.decommission', '缩容下线 (DECOMMISSION)', 'high'], ['node.drop', '强制删除 (DROP)', 'high'],
      ['node.blacklist', '黑名单管理'], ['node.log', '查看节点日志']]},
    {id: 'deploy', ico: '⬇', name: '安装部署', btns: [
      ['deploy.host', '主机管理（添加 / 删除主机）'], ['deploy.precheck', '环境预检'], ['deploy.fix', '一键修复环境项', 'high'],
      ['deploy.create', '新建部署任务'], ['deploy.execute', '执行部署', 'high'], ['deploy.pkg', '安装包管理（上传 / 删除）']]},
    {id: 'upgrade', ico: '⇪', name: '升级回滚', btns: [
      ['upgrade.create', '创建升级计划'], ['upgrade.execute', '执行滚动升级', 'high'], ['upgrade.pause', '暂停 / 继续'], ['upgrade.rollback', '回滚 / 降级', 'high']]},
    {id: 'backup', ico: '⛁', name: '备份恢复', btns: [
      ['backup.repo', '仓库管理 (REPOSITORY)'], ['backup.create', '发起备份'], ['backup.restore', '恢复数据', 'high'],
      ['backup.cancel', '取消作业'], ['backup.policy', '备份策略配置'], ['backup.clean', '清理备份快照', 'high']]},
    {id: 'load', ico: '⇣', name: '导入作业', btns: [
      ['load.pause', '暂停 Routine Load'], ['load.resume', '恢复 Routine Load'], ['load.stop', '停止作业', 'high'], ['load.detail', '查看错误详情']]},
    {id: 'mv', ico: '◎', name: '物化视图', btns: [
      ['mv.refresh', '手动刷新'], ['mv.toggle', '启用 / 停用'], ['mv.cancel', '取消刷新任务']]},
    {id: 'param', ico: '⚙', name: '参数配置', btns: [
      ['param.edit', '修改参数', 'high'], ['param.persist', '持久化到配置文件'], ['param.history', '查看变更历史'], ['param.compare', '节点参数对比']]},
  ]},
  {group: '告警与审计', menus: [
    {id: 'alert', ico: '⚑', name: '告警中心', btns: [
      ['alert.ack', '认领 / 处理告警'], ['alert.silence', '静默告警'], ['alert.rule', '告警规则管理'], ['alert.channel', '通知渠道管理']]},
    {id: 'audit', ico: '☷', name: '操作审计', btns: [
      ['audit.export', '导出审计日志'], ['audit.sql', '查看 SQL 审计']]},
  ]},
  {group: '平台配置', menus: [
    {id: 'users', ico: '👤', name: '用户管理', btns: [
      ['user.create', '新建用户'], ['user.edit', '编辑用户'], ['user.delete', '删除用户', 'high'],
      ['user.reset', '重置密码'], ['user.lock', '锁定 / 解锁']]},
    {id: 'roles', ico: '⛨', name: '角色权限', btns: [
      ['role.create', '新建角色'], ['role.edit', '编辑角色权限'], ['role.delete', '删除角色', 'high'], ['role.copy', '复制角色']]},
    {id: 'clusters', ico: '⧉', name: '集群接入', btns: [
      ['cluster.add', '导入集群'], ['cluster.edit', '编辑连接'], ['cluster.delete', '移除集群', 'high'], ['cluster.test', '连通性测试']]},
    {id: 'settings', ico: '☰', name: '平台设置', btns: [
      ['setting.security', '安全策略'], ['setting.notify', '通知设置'], ['setting.basic', '基础设置']]},
  ]},
  {group: '帮助', menus: [
    {id: 'help', ico: '?', name: '使用手册', btns: [], public: true},
  ]},
];

/* 扁平化索引：菜单 / 按钮码 → 元信息 */
const MENUS = {}; const BTNS = {};
PERM_TREE.forEach(g => g.menus.forEach(m => {
  MENUS[m.id] = {...m, group: g.group};
  m.btns.forEach(([code, name, risk]) => { BTNS[code] = {code, name, risk: risk || '', menu: m.id}; });
}));
const ALL_CODES = () => Object.keys(MENUS).map(id => id + ':view').concat(Object.keys(BTNS));
const viewsOf = ids => ids.map(id => id + ':view');
const btnsOf = (...ids) => ids.flatMap(id => MENUS[id].btns.map(b => b[0]));

/* ---------- 预置角色 ---------- */
const ROLES = [
  {id: 'r_admin', name: '超级管理员', builtin: true, desc: '平台全部功能与全部集群，含用户与权限管理。',
    clusters: ['*'], perms: ALL_CODES()},
  {id: 'r_ops', name: '运维管理员', builtin: false, desc: '集群全部运维操作；不能管理平台用户与角色。',
    clusters: ['*'],
    perms: viewsOf(['overview','topology','inspect','query','resource','nodes','deploy','upgrade','backup','load','mv','param','alert','audit','clusters'])
      .concat(btnsOf('overview','topology','inspect','query','resource','nodes','deploy','upgrade','backup','load','mv','param','alert'), ['audit.sql','cluster.test','cluster.edit'])},
  {id: 'r_dba', name: 'DBA', builtin: false, desc: '查询、资源组、参数、导入、物化视图、备份；节点只读。',
    clusters: ['prod-sr-01', 'stg-sr-01'],
    perms: viewsOf(['overview','topology','inspect','query','resource','nodes','backup','load','mv','param','alert','audit'])
      .concat(btnsOf('overview','topology','query','resource','load','mv'),
        ['inspect.run','inspect.export','backup.create','backup.cancel','backup.repo','param.history','param.compare','param.edit','alert.ack','node.log','audit.sql'])},
  {id: 'r_oncall', name: '值班只读', builtin: false, desc: '只看监控、巡检与告警，可认领告警。',
    clusters: ['*'],
    perms: viewsOf(['overview','topology','inspect','query','nodes','load','mv','alert']).concat(['topology.detail','inspect.export','alert.ack','query.profile','load.detail','node.log'])},
  {id: 'r_audit', name: '审计员', builtin: true, desc: '只读审计日志与用户 / 角色配置，便于合规检查。',
    clusters: ['*'],
    perms: viewsOf(['overview','audit','users','roles']).concat(['audit.export','audit.sql'])},
];

/* ---------- 平台用户 ---------- */
const USERS = [
  {id: 1, login: 'admin', name: '系统管理员', email: 'admin@example.com', phone: '138****0001', dept: '基础架构部', roles: ['r_admin'], status: 'active', last: '2026-10-06 09:12', created: '2026-01-05'},
  {id: 2, login: 'ops_zhang', name: '张运维', email: 'zhang@example.com', phone: '138****0002', dept: '运维部', roles: ['r_ops'], status: 'active', last: '2026-10-06 10:31', created: '2026-02-11'},
  {id: 3, login: 'dba_li', name: '李 DBA', email: 'li@example.com', phone: '138****0003', dept: '数据平台部', roles: ['r_dba'], status: 'active', last: '2026-10-05 22:47', created: '2026-02-11'},
  {id: 4, login: 'oncall_wang', name: '王值班', email: 'wang@example.com', phone: '138****0004', dept: '运维部', roles: ['r_oncall'], status: 'active', last: '2026-10-06 03:15', created: '2026-03-02'},
  {id: 5, login: 'auditor_chen', name: '陈审计', email: 'chen@example.com', phone: '138****0005', dept: '风控合规部', roles: ['r_audit'], status: 'active', last: '2026-09-30 15:20', created: '2026-03-20'},
  {id: 6, login: 'dev_zhao', name: '赵开发', email: 'zhao@example.com', phone: '138****0006', dept: '业务研发部', roles: ['r_oncall'], status: 'locked', last: '2026-08-12 11:02', created: '2026-04-08'},
];

/* ---------- 平台安全设置（部分影响权限展示） ---------- */
const SETTINGS = {
  noPermBtn: 'disable',       // disable 置灰带锁 / hide 隐藏
  highRiskConfirm: true,      // 高危操作需输入集群名确认
  dualReview: false,          // 高危操作需第二人复核
  sessionTimeout: 30, pwdMinLen: 10, pwdExpireDays: 90, loginFailLock: 5, ipWhitelist: '10.0.0.0/8, 192.168.0.0/16',
};

const DEMO_PWD = 'StarRocks@2026';   // 原型：所有演示账号的登录密码

/* ---------- 当前登录用户（原型可在顶栏切换身份演示权限效果） ---------- */
let ME = USERS[0];
function myPerms() {
  const set = new Set();
  ME.roles.forEach(rid => { const r = ROLES.find(x => x.id === rid); if (r) r.perms.forEach(p => set.add(p)); });
  return set;
}
function myClusters() {
  const s = new Set();
  ME.roles.forEach(rid => { const r = ROLES.find(x => x.id === rid); if (r) r.clusters.forEach(c => s.add(c)); });
  return s.has('*') ? null : s;   // null = 全部集群
}
const can = code => code === 'help:view' || myPerms().has(code);
const canView = page => (MENUS[page] && MENUS[page].public) || can(page + ':view');

/* 统一的按钮渲染：B(权限码, 文本, {cls, data:{...}, title}) */
function B(code, label, o = {}) {
  const meta = BTNS[code];
  const data = Object.entries(o.data || {}).map(([k, v]) => ` data-${k}="${esc(v)}"`).join('');
  const cls = 'btn ' + (o.cls || '');
  if (!can(code)) {
    if (SETTINGS.noPermBtn === 'hide') return '';
    return `<button class="${cls} noperm" disabled title="无权限：需要「${meta ? meta.name : code}」(${code})">${label}</button>`;
  }
  const miss = meta && CLUSTER_PAGES.includes(meta.menu) && !OPS_READONLY.has(code) ? clusterMissing(CUR) : [];
  if (miss.length) return `<button class="${cls} noperm" disabled title="集群「${esc(CUR.name)}」必填信息未完善（缺少：${miss.join('、')}），请在「集群接入」中补全后再操作">${label}</button>`;
  return `<button class="${cls}" data-act="${code}"${data} title="${esc(o.title || (meta ? meta.name : ''))}">${label}</button>`;
}
/* 集群必填信息不完整时仍允许的只读 / 平台侧操作 */
const OPS_READONLY = new Set(['overview.refresh', 'overview.export', 'topology.detail', 'inspect.export', 'query.profile', 'query.explain', 'query.export',
  'load.detail', 'param.history', 'param.compare', 'alert.ack', 'alert.silence', 'alert.rule', 'alert.channel',
  'inspect.run', 'inspect.schedule', 'inspect.rule', 'inspect.ignore']);   // 巡检只读取集群，不需要 SSH 也可执行（系统环境项会跳过）
