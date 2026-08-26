(() => {
  const supported = new Set(["zh", "en"]);
  const supportedVersions = new Set(["v1", "v2"]);
  const query = new URLSearchParams(window.location.search).get("lang");
  const versionQuery = new URLSearchParams(window.location.search).get("version");
  let stored = "";
  let storedVersion = "";
  try {
    stored = window.localStorage.getItem("architecture-language") || "";
    storedVersion = window.localStorage.getItem("architecture-version") || "";
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

  function setVersion(version, updateURL) {
    const next = supportedVersions.has(version) ? version : "v2";
    document.body.dataset.version = next;

    document.querySelectorAll("[data-set-version]").forEach((button) => {
      const active = button.dataset.setVersion === next;
      button.setAttribute("aria-pressed", String(active));
    });

    try {
      window.localStorage.setItem("architecture-version", next);
    } catch (_) {
      // Persistence is optional.
    }

    if (updateURL) {
      const url = new URL(window.location.href);
      url.searchParams.set("version", next);
      window.history.replaceState({}, "", url);
    }
  }

  document.querySelectorAll("[data-set-lang]").forEach((button) => {
    button.addEventListener("click", () => setLanguage(button.dataset.setLang, true));
  });
  document.querySelectorAll("[data-set-version]").forEach((button) => {
    button.addEventListener("click", () => {
      setVersion(button.dataset.setVersion, true);
      const language = document.body.dataset.lang;
      const target = document.querySelector(`[data-locale="${language}"][data-version="${document.body.dataset.version}"] .hero`);
      target?.scrollIntoView({ behavior: "smooth", block: "start" });
    });
  });

  setLanguage(supported.has(query) ? query : (supported.has(stored) ? stored : browserLanguage), false);
  setVersion(supportedVersions.has(versionQuery) ? versionQuery : (supportedVersions.has(storedVersion) ? storedVersion : "v2"), false);

  const sections = Array.from(document.querySelectorAll("[data-locale][data-version] .doc-section"));
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
