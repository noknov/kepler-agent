(() => {
  const article = document.querySelector('.doc');
  const sections = [...document.querySelectorAll('.chapter2[id]')];
  if (!article || sections.length === 0) return;

  const pages = [
    { href: 'index.html', title: '系统概览', group: 'Core · 核心架构' },
    { href: 'execution.html', title: '执行过程', group: 'Core · 核心架构' },
    { href: 'models.html', title: '上下文与模型', group: 'Core · 核心架构' },
    { href: 'tools.html', title: '工具与策略', group: 'Core · 核心架构' },
    { href: 'reliability.html', title: '状态与恢复', group: 'State · 状态与治理' },
    { href: 'delegation.html', title: '委派执行', group: 'State · 状态与治理' },
    { href: 'operations.html', title: '队列与观测', group: 'State · 状态与治理' },
    { href: 'production.html', title: '生产推演', group: 'State · 状态与治理' },
    { href: 'slack.html', title: 'Slack 接入', group: 'Surface · 表面适配' },
    { href: 'reference.html', title: '实现索引', group: 'Index · 源码映射' },
  ];

  const file = location.pathname.split('/').pop() || 'index.html';

  const side = document.querySelector('.side');
  if (side) {
    side.setAttribute('aria-label', '文档目录');
    const grouped = new Map();
    for (const p of pages) {
      if (!grouped.has(p.group)) grouped.set(p.group, []);
      grouped.get(p.group).push(p);
    }
    const html = [];
    for (const [groupName, items] of grouped.entries()) {
      const links = items
        .map((item) => {
          const current = item.href === file ? ' aria-current="page"' : '';
          return `<a href="${item.href}"${current}>${item.title}</a>`;
        })
        .join('');
      html.push(`<div class="nav-group"><p>${groupName}</p>${links}</div>`);
    }
    side.innerHTML = html.join('');
  }

  const pageNav = document.createElement('aside');
  pageNav.className = 'page-nav';
  pageNav.setAttribute('aria-label', '本页目录');
  pageNav.innerHTML =
    '<p>本页章节</p>' +
    sections
      .map((section) => {
        const heading = section.querySelector('h2');
        return heading ? `<a href="#${section.id}">${heading.textContent}</a>` : '';
      })
      .join('');
  article.insertAdjacentElement('afterend', pageNav);

  const pageLinks = [...pageNav.querySelectorAll('a')];
  if ('IntersectionObserver' in window) {
    const observer = new IntersectionObserver(
      (entries) => {
        const current = entries
          .filter((entry) => entry.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
        if (current) {
          pageLinks.forEach((link) =>
            link.classList.toggle('is-active', link.hash === `#${current.target.id}`),
          );
        }
      },
      { rootMargin: '-18% 0px -72%', threshold: 0 },
    );
    sections.forEach((section) => observer.observe(section));
  }

  // Add Prev / Next chapter cards at the bottom of the article if not present
  const idx = pages.findIndex((p) => p.href === file);
  if (idx >= 0 && !article.querySelector('.next-pages')) {
    const prev = idx > 0 ? pages[idx - 1] : null;
    const next = idx < pages.length - 1 ? pages[idx + 1] : null;
    const nav = document.createElement('nav');
    nav.className = 'next-pages';
    nav.setAttribute('aria-label', '章节翻页');
    nav.innerHTML = [
      prev
        ? `<a href="${prev.href}"><b>← 上一章：${prev.title}</b><span>${prev.group}</span></a>`
        : '<div></div>',
      next
        ? `<a href="${next.href}"><b>下一章：${next.title} →</b><span>${next.group}</span></a>`
        : '<div></div>',
    ].join('');
    article.appendChild(nav);
  }
})();
