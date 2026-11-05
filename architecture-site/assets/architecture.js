(() => {
  const article = document.querySelector('.doc');
  const sections = [...document.querySelectorAll('.chapter2[id]')];
  if (!article || !sections.length) return;

  const pages = [
    ['index.html', '系统概览', '总览'],
    ['reading-guide.html', '术语与文档范围', '总览'],
    ['execution.html', '执行过程', '核心机制'],
    ['models.html', '上下文与模型', '核心机制'],
    ['tools.html', '工具与策略', '核心机制'],
    ['reliability.html', '状态与恢复', '状态与协作'],
    ['delegation.html', '委派执行', '状态与协作'],
    ['operations.html', '队列与观测', '生产与接入'],
    ['production.html', '生产推演', '生产与接入'],
    ['slack.html', 'Slack 接入', '生产与接入'],
    ['reference.html', '实现索引', '查找实现'],
  ].map(([href, title, group]) => ({ href, title, group }));
  const file = location.pathname.split('/').pop() || 'index.html';
  const currentIndex = pages.findIndex((page) => page.href === file);
  const current = pages[currentIndex];
  const side = document.querySelector('.side');
  if (side) {
    side.id = 'chapter-navigation';
    side.setAttribute('aria-label', '章节导航');
    side.innerHTML = '<a class="side-brand" href="index.html">Kepler<span>架构指南 / Architecture</span></a>';
    let group;
    for (const [index, page] of pages.entries()) {
      if (page.group !== group) {
        group = page.group;
        const label = document.createElement('p');
        label.className = 'nav-label';
        label.textContent = group;
        side.append(label);
      }
      const link = document.createElement('a');
      link.href = page.href;
      link.innerHTML = `<span class="nav-number">${String(index).padStart(2, '0')}</span><span>${page.title}</span>`;
      if (page.href === file) link.setAttribute('aria-current', 'page');
      side.append(link);
    }
  }

  const topbar = document.querySelector('.topbar-inner');
  const controls = document.createElement('div');
  controls.className = 'reader-controls';
  controls.innerHTML = '<button class="menu-toggle" type="button" aria-controls="chapter-navigation" aria-expanded="false">目录</button><button class="search-toggle" type="button" aria-haspopup="dialog">搜索文档 <kbd>/</kbd></button><button class="theme-toggle" type="button" aria-label="切换为深色模式" aria-pressed="false">深色</button>';
  topbar?.append(controls);
  // The chapter sidebar replaces the duplicated desktop navigation only when JS runs.
  document.documentElement.classList.add('reader-enhanced');
  const menu = controls.querySelector('.menu-toggle');
  const setMenu = (open) => {
    side?.classList.toggle('is-open', open);
    menu.setAttribute('aria-expanded', String(open));
  };
  menu.addEventListener('click', () => setMenu(menu.getAttribute('aria-expanded') !== 'true'));
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && menu.getAttribute('aria-expanded') === 'true') {
      setMenu(false);
      menu.focus();
    }
  });
  document.addEventListener('click', (event) => {
    if (!side?.contains(event.target) && !menu.contains(event.target)) setMenu(false);
  });

  const themeButton = controls.querySelector('.theme-toggle');
  const systemTheme = window.matchMedia('(prefers-color-scheme: dark)');
  let chosenTheme;
  try { chosenTheme = localStorage.getItem('kepler-theme'); } catch { /* Storage is optional. */ }
  const applyTheme = (theme) => {
    document.documentElement.dataset.theme = theme;
    themeButton.textContent = theme === 'dark' ? '浅色' : '深色';
    themeButton.setAttribute('aria-label', theme === 'dark' ? '切换为浅色模式' : '切换为深色模式');
    themeButton.setAttribute('aria-pressed', String(theme === 'dark'));
  };
  applyTheme(chosenTheme === 'dark' || chosenTheme === 'light' ? chosenTheme : systemTheme.matches ? 'dark' : 'light');
  themeButton.addEventListener('click', () => {
    chosenTheme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    applyTheme(chosenTheme);
    try { localStorage.setItem('kepler-theme', chosenTheme); } catch { /* Reading needs no storage. */ }
  });
  systemTheme.addEventListener('change', () => {
    if (!chosenTheme) applyTheme(systemTheme.matches ? 'dark' : 'light');
  });

  const hero = article.querySelector('.hero2');
  if (current && hero) {
    const breadcrumb = document.createElement('p');
    breadcrumb.className = 'breadcrumb';
    breadcrumb.textContent = `${current.group} / ${current.title}`;
    hero.prepend(breadcrumb);
  }
  const pageNav = document.createElement('aside');
  pageNav.className = 'page-nav';
  pageNav.setAttribute('aria-label', '本页目录');
  pageNav.innerHTML = '<p>本页章节</p>';
  for (const section of sections) {
    const heading = section.querySelector('h2');
    if (!heading) continue;
    const text = heading.textContent;
    const link = document.createElement('a');
    link.href = `#${section.id}`;
    link.textContent = text;
    pageNav.append(link);
    const permalink = document.createElement('a');
    permalink.className = 'heading-link';
    permalink.href = `#${section.id}`;
    permalink.setAttribute('aria-label', `链接到：${text}`);
    permalink.textContent = '#';
    heading.append(permalink);
  }
  const back = document.createElement('a');
  back.href = '#content';
  back.className = 'back-to-top';
  back.textContent = '回到正文顶部 ↑';
  pageNav.append(back);
  article.after(pageNav);
  const tocLinks = [...pageNav.querySelectorAll('a:not(.back-to-top)')];
  const progress = document.createElement('div');
  progress.className = 'reading-progress';
  progress.setAttribute('aria-hidden', 'true');
  document.body.append(progress);
  let scheduled = false;
  const updatePosition = () => {
    const max = document.documentElement.scrollHeight - innerHeight;
    progress.style.transform = `scaleX(${max > 0 ? Math.min(1, Math.max(0, scrollY / max)) : 1})`;
    // Recompute from all sections: works when scrolling in either direction or jumping anchors.
    let active = sections[0];
    for (const section of sections) {
      if (section.getBoundingClientRect().top <= 130) active = section;
    }
    if (max > 0 && scrollY >= max - 2) active = sections.at(-1);
    for (const link of tocLinks) {
      const selected = link.hash === `#${active.id}`;
      link.classList.toggle('is-active', selected);
      if (selected) link.setAttribute('aria-current', 'location');
      else link.removeAttribute('aria-current');
    }
    scheduled = false;
  };
  const schedulePosition = () => {
    if (!scheduled) { scheduled = true; requestAnimationFrame(updatePosition); }
  };
  addEventListener('scroll', schedulePosition, { passive: true });
  addEventListener('resize', schedulePosition);
  updatePosition();

  if (currentIndex >= 0 && !article.querySelector('.next-pages')) {
    const nav = document.createElement('nav');
    nav.className = 'next-pages';
    nav.setAttribute('aria-label', '章节翻页');
    nav.innerHTML = [pages[currentIndex - 1], pages[currentIndex + 1]].map((page, i) => page
      ? `<a href="${page.href}"><span>${i ? '下一章' : '上一章'}</span><b>${i ? '' : '← '}${page.title}${i ? ' →' : ''}</b></a>`
      : '<div></div>').join('');
    article.append(nav);
  }

  const dialog = document.createElement('dialog');
  dialog.className = 'search-dialog';
  dialog.setAttribute('aria-labelledby', 'search-title');
  dialog.innerHTML = '<div class="search-header"><label id="search-title" for="guide-search">搜索架构指南</label><button type="button" class="search-close" aria-label="关闭搜索">关闭 <kbd>Esc</kbd></button></div><input id="guide-search" type="search" placeholder="搜索术语、机制或问题，如：会话锁" autocomplete="off"><p class="search-status" role="status" aria-live="polite"></p><ul class="search-results"></ul>';
  document.body.append(dialog);
  const input = dialog.querySelector('input');
  const status = dialog.querySelector('.search-status');
  const results = dialog.querySelector('.search-results');
  const searchIndex = window.KEPLER_SEARCH_INDEX || [];
  const search = () => {
    const terms = input.value.normalize('NFKC').trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
    results.replaceChildren();
    if (!terms.length) { status.textContent = '搜索全部章节。可用空格组合关键词，如“工具 权限”。'; return; }
    const matches = searchIndex.filter((entry) => terms.every((term) => `${entry.page} ${entry.title} ${entry.text}`.normalize('NFKC').toLocaleLowerCase().includes(term)));
    status.textContent = matches.length ? `找到 ${matches.length} 个章节${matches.length > 30 ? '，显示前 30 个，请增加关键词缩小范围' : ''}。` : '没有找到匹配章节。试试“执行”“重试”或英文类型名。';
    for (const entry of matches.slice(0, 30)) {
      const li = document.createElement('li');
      const link = document.createElement('a');
      link.href = entry.href;
      const label = document.createElement('span');
      label.className = 'result-page';
      label.textContent = entry.page;
      const title = document.createElement('b');
      title.textContent = entry.title;
      const snippet = document.createElement('p');
      const lower = entry.text.normalize('NFKC').toLocaleLowerCase();
      const first = Math.max(0, lower.indexOf(terms[0]) - 28);
      snippet.textContent = `${first ? '…' : ''}${entry.text.slice(first, first + 110)}${entry.text.length > first + 110 ? '…' : ''}`;
      link.append(label, title, snippet);
      li.append(link);
      results.append(li);
    }
  };
  input.addEventListener('input', search);
  const openSearch = () => { setMenu(false); dialog.showModal(); search(); input.focus(); };
  controls.querySelector('.search-toggle').addEventListener('click', openSearch);
  dialog.querySelector('.search-close').addEventListener('click', () => dialog.close());
  // A click on a result in this page still needs to close the modal.
  results.addEventListener('click', (event) => {
    if (event.target.closest('a')) dialog.close();
  });
  dialog.addEventListener('click', (event) => { if (event.target === dialog) {
    const box = dialog.getBoundingClientRect();
    if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) dialog.close();
  } });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && dialog.open) {
      event.preventDefault();
      dialog.close();
      return;
    }
    const editing = event.target.closest('input, textarea, select, [contenteditable="true"]');
    if (event.key === '/' && !editing && !event.ctrlKey && !event.metaKey && !event.altKey) {
      event.preventDefault();
      if (!dialog.open) openSearch();
    }
  });
})();
