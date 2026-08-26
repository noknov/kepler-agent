(() => {
  const supported = new Set(["zh", "en"]);
  const query = new URLSearchParams(window.location.search).get("lang");
  let stored = "";
  try {
    stored = window.localStorage.getItem("architecture-language") || "";
  } catch (_) {
    // The site also works when storage is unavailable.
  }
  const browserLanguage = navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";

  function setLanguage(language, updateURL) {
    const next = supported.has(language) ? language : "zh";
    document.body.dataset.lang = next;
    document.documentElement.lang = next === "zh" ? "zh-CN" : "en";
    document.title = next === "zh"
      ? "架构指南 · Kepler Agent"
      : "Architecture Guide · Kepler Agent";

    document.querySelectorAll("[data-set-lang]").forEach((button) => {
      const active = button.dataset.setLang === next;
      button.setAttribute("aria-pressed", String(active));
    });

    try {
      window.localStorage.setItem("architecture-language", next);
    } catch (_) {
      // Persistence is optional.
    }

    if (updateURL) {
      const url = new URL(window.location.href);
      url.searchParams.set("lang", next);
      window.history.replaceState({}, "", url);
    }
  }

  document.querySelectorAll("[data-set-lang]").forEach((button) => {
    button.addEventListener("click", () => setLanguage(button.dataset.setLang, true));
  });

  setLanguage(supported.has(query) ? query : (supported.has(stored) ? stored : browserLanguage), false);

  const sections = Array.from(document.querySelectorAll("[data-locale] .doc-section"));
  const observer = new IntersectionObserver((entries) => {
    const visible = entries
      .filter((entry) => entry.isIntersecting)
      .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
    if (!visible) return;
    document.querySelectorAll(".toc a").forEach((link) => {
      link.classList.toggle("is-active", link.hash === `#${visible.target.id}`);
    });
  }, { rootMargin: "-20% 0px -70%", threshold: 0 });

  sections.forEach((section) => observer.observe(section));
})();
