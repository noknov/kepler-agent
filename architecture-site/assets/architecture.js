(() => {
  const article = document.querySelector('.doc-content');
  if (!article) return;
  const pages = [
    [
        "index.html",
        "系统架构",
        "系统基础"
    ],
    [
        "reading-guide.html",
        "概念与标识",
        "系统基础"
    ],
    [
        "execution.html",
        "执行过程",
        "执行机制"
    ],
    [
        "context.html",
        "上下文与证据",
        "执行机制"
    ],
    [
        "models.html",
        "模型协议与重试",
        "执行机制"
    ],
    [
        "tools.html",
        "工具与策略",
        "执行机制"
    ],
    [
        "reliability.html",
        "持久化与恢复",
        "状态与数据"
    ],
    [
        "memory.html",
        "记忆生命周期",
        "状态与数据"
    ],
    [
        "delegation.html",
        "任务委派",
        "调度与协作"
    ],
    [
        "operations.html",
        "队列与生命周期",
        "调度与协作"
    ],
    [
        "slack.html",
        "Slack 接入",
        "产品边界"
    ],
    [
        "benchmark.html",
        "Benchmark 接入",
        "产品边界"
    ],
    [
        "observability.html",
        "运行观测",
        "观测与诊断"
    ],
    [
        "production.html",
        "故障诊断",
        "观测与诊断"
    ],
    [
        "reference.html",
        "实现索引",
        "实现参考"
    ]
].map(([href, title, group]) => ({ href, title, group }));
  const file = location.pathname.split('/').pop() || 'index.html';
  const chapterNav = document.querySelector('.chapter-nav');
  chapterNav.replaceChildren();
  let lastGroup;
  let groupNode;
  pages.forEach((page, index) => {
    if (lastGroup !== page.group) {
      groupNode = document.createElement('div');
      groupNode.className = 'nav-group';
      const label = document.createElement('p');
      label.className = 'nav-label';
      label.textContent = page.group;
      groupNode.append(label);
      chapterNav.append(groupNode);
      lastGroup = page.group;
    }
    const link = document.createElement('a');
    link.href = page.href;
    link.innerHTML = `<span class="chapter-number">${String(index + 1).padStart(2, '0')}</span><span>${page.title}</span>`;
    if (page.href === file) link.setAttribute('aria-current', 'page');
    groupNode.append(link);
  });
  const navFooter = document.createElement('div');
  navFooter.className = 'nav-footer';
  navFooter.innerHTML = '<a href="https://github.com/noknov/kepler-agent">GitHub 源码 ↗</a>';
  chapterNav.append(navFooter);

  const actions = document.querySelector('.header-actions');
  actions.innerHTML = '<button class="control menu-trigger" type="button" aria-haspopup="dialog">目录</button><button class="control search-trigger" type="button" aria-haspopup="dialog">搜索文档 <kbd>/</kbd></button><button class="control theme-toggle" type="button" aria-pressed="false">深色</button>';
  const themeButton = actions.querySelector('.theme-toggle');
  const system = matchMedia('(prefers-color-scheme: dark)');
  let preference;
  try { preference = localStorage.getItem('kepler-theme'); } catch { /* Optional preference. */ }
  if (preference !== 'light' && preference !== 'dark') preference = null;
  const applyTheme = (theme) => {
    document.documentElement.dataset.theme = theme;
    themeButton.textContent = theme === 'dark' ? '浅色' : '深色';
    themeButton.setAttribute('aria-label', theme === 'dark' ? '切换为浅色模式' : '切换为深色模式');
    themeButton.setAttribute('aria-pressed', String(theme === 'dark'));
  };
  applyTheme(preference || (system.matches ? 'dark' : 'light'));
  themeButton.addEventListener('click', () => {
    preference = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    applyTheme(preference);
    try { localStorage.setItem('kepler-theme', preference); } catch { /* Reading requires no storage. */ }
  });
  system.addEventListener('change', () => { if (!preference) applyTheme(system.matches ? 'dark' : 'light'); });

  function makeDialog(className, title, id) {
    const dialog = document.createElement('dialog');
    dialog.className = className;
    dialog.setAttribute('aria-labelledby', id);
    dialog.innerHTML = `<div class="dialog-header"><h2 id="${id}">${title}</h2><button type="button" class="control dialog-close" aria-label="关闭${title}">关闭</button></div>`;
    dialog.querySelector('.dialog-close').addEventListener('click', () => dialog.close());
    dialog.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') { event.preventDefault(); dialog.close(); }
    });
    dialog.addEventListener('click', (event) => {
      const rect = dialog.getBoundingClientRect();
      if (event.target === dialog && (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom)) dialog.close();
    });
    document.body.append(dialog);
    return dialog;
  }
  const mobile = makeDialog('mobile-dialog', '章节目录', 'mobile-title');
  const mobileChapters = document.createElement('nav');
  mobileChapters.className = 'mobile-chapters';
  mobileChapters.setAttribute('aria-label', '移动端章节目录');
  mobileChapters.innerHTML = chapterNav.innerHTML;
  mobile.append(mobileChapters);
  mobileChapters.addEventListener('click', (event) => { if (event.target.closest('a')) mobile.close(); });
  actions.querySelector('.menu-trigger').addEventListener('click', () => mobile.showModal());
  matchMedia('(min-width: 821px)').addEventListener('change', (event) => { if (event.matches && mobile.open) mobile.close(); });

  const toc = document.querySelector('.toc');
  const sections = [...article.querySelectorAll('.doc-section[id]')];
  toc.innerHTML = '<p>本页内容</p>';
  sections.forEach((section) => {
    const heading = section.querySelector('h2');
    const title = heading.textContent;
    const link = document.createElement('a');
    link.href = `#${section.id}`;
    link.textContent = title;
    toc.append(link);
    const anchor = document.createElement('a');
    anchor.className = 'heading-link';
    anchor.href = `#${section.id}`;
    anchor.textContent = '#';
    anchor.setAttribute('aria-label', `链接到${title}`);
    heading.append(anchor);
  });
  const tocLinks = [...toc.querySelectorAll('a')];
  const top = document.createElement('a');
  top.className = 'back-top';
  top.href = '#content';
  top.textContent = '返回顶部 ↑';
  toc.append(top);
  const progress = document.createElement('div');
  progress.className = 'read-progress';
  progress.setAttribute('aria-hidden', 'true');
  document.body.append(progress);
  let pending = false;
  function position() {
    const max = document.documentElement.scrollHeight - innerHeight;
    progress.style.transform = `scaleX(${max > 0 ? Math.min(1, Math.max(0, scrollY / max)) : 1})`;
    let active = sections[0];
    for (const section of sections) if (section.getBoundingClientRect().top <= 120) active = section;
    if (max > 0 && scrollY >= max - 2) active = sections.at(-1);
    tocLinks.forEach((link) => {
      if (link.hash === `#${active.id}`) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    });
    pending = false;
  }
  const schedule = () => { if (!pending) { pending = true; requestAnimationFrame(position); } };
  addEventListener('scroll', schedule, { passive: true });
  addEventListener('resize', schedule);
  position();

  const current = pages.findIndex((page) => page.href === file);
  if (current >= 0) {
    const pagination = document.createElement('nav');
    pagination.className = 'page-pagination';
    pagination.setAttribute('aria-label', '章节翻页');
    pagination.innerHTML = [pages[current - 1], pages[current + 1]].map((page, index) => page ? `<a href="${page.href}"><span>${index ? '下一章' : '上一章'}</span><strong>${index ? '' : '← '}${page.title}${index ? ' →' : ''}</strong></a>` : '<div></div>').join('');
    article.append(pagination);
  }

  const searchDialog = makeDialog('search-dialog', '搜索文档', 'search-title');
  const input = document.createElement('input');
  input.type = 'search';
  input.className = 'search-input';
  input.placeholder = '输入术语、机制或代码标识';
  input.setAttribute('aria-labelledby', 'search-title');
  input.autocomplete = 'off';
  const status = document.createElement('p');
  status.className = 'search-status';
  status.setAttribute('role', 'status');
  const results = document.createElement('ul');
  results.className = 'search-results';
  searchDialog.append(input, status, results);
  const normalize = (text) => text.normalize('NFKC').toLocaleLowerCase();
  const index = (window.KEPLER_SEARCH_INDEX || []).map((entry) => ({ ...entry, normalized: normalize(`${entry.page} ${entry.title} ${entry.text}`) }));
  function search() {
    const terms = normalize(input.value).trim().split(/\s+/).filter(Boolean);
    results.replaceChildren();
    if (!terms.length) { status.textContent = '搜索全部章节；多个关键词用空格分隔。'; return; }
    const matches = index.filter((entry) => terms.every((term) => entry.normalized.includes(term))).map((entry) => ({ entry, score: terms.reduce((sum, term) => sum + (normalize(entry.title).includes(term) ? 4 : 0) + (normalize(entry.page).includes(term) ? 2 : 0), 0) })).sort((a, b) => b.score - a.score);
    status.textContent = matches.length ? `${matches.length} 个匹配章节${matches.length > 30 ? '，显示前 30 项' : ''}` : '没有匹配章节。可缩短关键词，或使用代码标识。';
    for (const { entry } of matches.slice(0, 30)) {
      const item = document.createElement('li');
      const link = document.createElement('a');
      link.href = entry.href;
      const pageLabel = document.createElement('span');
      pageLabel.className = 'result-page';
      pageLabel.textContent = entry.page;
      const heading = document.createElement('strong');
      heading.textContent = entry.title;
      const snippet = document.createElement('p');
      const offset = Math.max(0, normalize(entry.text).indexOf(terms[0]) - 25);
      snippet.textContent = `${offset ? '…' : ''}${entry.text.slice(offset, offset + 110)}${entry.text.length > offset + 110 ? '…' : ''}`;
      link.append(pageLabel, heading, snippet);
      item.append(link);
      results.append(item);
    }
  }
  input.addEventListener('input', search);
  results.addEventListener('click', (event) => { if (event.target.closest('a')) searchDialog.close(); });
  const openSearch = () => { if (mobile.open) mobile.close(); searchDialog.showModal(); search(); input.focus(); };
  actions.querySelector('.search-trigger').addEventListener('click', openSearch);
  document.addEventListener('keydown', (event) => {
    if (event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey && !event.target.closest('input, textarea, select, [contenteditable="true"]') && !searchDialog.open && !mobile.open) {
      event.preventDefault();
      openSearch();
    }
  });
  document.documentElement.classList.add('is-enhanced');
})();
