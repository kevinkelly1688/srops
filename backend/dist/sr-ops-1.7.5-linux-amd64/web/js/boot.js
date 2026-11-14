/* 启动 */
(function () {
  try { const t = localStorage.getItem('sr_theme'); if (t) document.documentElement.dataset.theme = t; } catch (e) {}
  $('#helpBtn').onclick = () => go('help', PAGE_HELP[ROUTE.page] || 'quickstart');
  $('#clusterSel').onchange = e => {
    CUR = CLUSTERS.find(c => c.id === e.target.value); NODES = genNodes(CUR);
    toast('已切换到 ' + CUR.name); render();
  };
  $('#userSel').onchange = e => {   // 原型演示：不退出直接切换身份
    ME = USERS.find(u => u.id == e.target.value);
    try { sessionStorage.setItem('sr_uid', ME.id); } catch (err) {}
    const cl = myClusters();
    if (cl && CUR && !cl.has(CUR.id)) { CUR = CLUSTERS.find(c => cl.has(c.id)) || CUR; NODES = genNodes(CUR); }
    buildNav(); buildTop(); toast(`已切换为 ${ME.login}（${ME.roles.map(r => roleName(r)).join('/')}）`); render();
  };
  document.addEventListener('keydown', e => { if (e.key === 'Escape') { if (!$('#modal').dataset.force) closeModal(); closeDrawer(); $('#umenu').classList.remove('open'); } });
  window.addEventListener('hashchange', () => { if ($('#login').hidden) render(); });
  loginInit();
})();
