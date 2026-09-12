(() => {
  const article = document.querySelector('.doc');
  const sections = [...document.querySelectorAll('.chapter2[id]')];
  if (!article || sections.length === 0) return;

  const side = document.querySelector('.side');
  if (side) {
    side.setAttribute('aria-label', '文档目录');
    side.innerHTML = [
      '<div class="nav-group"><p>开始了解</p><a href="index.html">系统概览</a><a href="execution.html">一条请求的执行过程</a></div>',
      '<div class="nav-group"><p>核心机制</p><a href="models.html">提示内容、上下文与模型</a><a href="tools.html">工具、权限与执行</a><a href="reliability.html">记录、并发与恢复</a><a href="delegation.html">委派执行与代码审查</a></div>',
      '<div class="nav-group"><p>产品入口</p><a href="surfaces.html">Slack 和 Web</a><a href="cli.html">本地命令行</a></div>',
      '<div class="nav-group"><p>运行维护</p><a href="operations.html">队列、观测与关闭</a></div>',
      '<div class="nav-group"><p>实现</p><a href="reference.html">源码参考</a></div>'
    ].join('');
  }

  const pageNav = document.createElement('aside');
  pageNav.className = 'page-nav';
  pageNav.setAttribute('aria-label', '本页目录');
  pageNav.innerHTML = '<p>本页内容</p>' + sections.map((section) => {
    const heading = section.querySelector('h2');
    return heading ? `<a href="#${section.id}">${heading.textContent}</a>` : '';
  }).join('');
  article.insertAdjacentElement('afterend', pageNav);

  const pageLinks = [...pageNav.querySelectorAll('a')];
  if ('IntersectionObserver' in window) {
    const observer = new IntersectionObserver((entries) => {
      const current = entries.filter((entry) => entry.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
      if (current) pageLinks.forEach((link) => link.classList.toggle('is-active', link.hash === `#${current.target.id}`));
    }, { rootMargin: '-18% 0px -72%', threshold: 0 });
    sections.forEach((section) => observer.observe(section));
  }

  const file = location.pathname.split('/').pop() || 'index.html';
  document.querySelectorAll('.side a').forEach((link) => {
    if (link.getAttribute('href') === file) link.setAttribute('aria-current', 'page');
  });
})();
