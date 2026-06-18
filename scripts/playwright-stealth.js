/**
 * Playwright stealth init script.
 *
 * Loaded via --init-script in the Playwright MCP Docker container so it runs
 * in every new page context BEFORE the page's own JavaScript. This prevents
 * common headless/automation detection heuristics from firing.
 *
 * Mount into the container and reference with:
 *   docker run ... -v "$(pwd)/scripts/playwright-stealth.js:/stealth.js" \
 *     mcr.microsoft.com/playwright/mcp:latest --init-script /stealth.js ...
 */
(function () {
  'use strict';

  // 1. Hide navigator.webdriver — the primary automation signal checked by
  //    virtually every bot-detection library (Cloudflare, DataDome, etc.).
  try {
    Object.defineProperty(navigator, 'webdriver', {
      get: () => undefined,
      configurable: true,
    });
  } catch (_) {}

  // 2. Restore window.chrome — absent or incomplete in headless Chromium.
  //    Many sites require this object to exist with a valid runtime field.
  try {
    if (!window.chrome || !window.chrome.runtime) {
      window.chrome = {
        app: {
          isInstalled: false,
          InstallState: { DISABLED: 'disabled', INSTALLED: 'installed', NOT_INSTALLED: 'not_installed' },
          RunningState: { CANNOT_RUN: 'cannot_run', READY_TO_RUN: 'ready_to_run', RUNNING: 'running' },
          getDetails: function () {},
          getIsInstalled: function () {},
          installState: function () {},
          runningState: function () {},
        },
        runtime: {
          id: undefined,
          connect: function () {},
          sendMessage: function () {},
          PlatformOs: { ANDROID: 'android', CROS: 'cros', LINUX: 'linux', MAC: 'mac', OPENBSD: 'openbsd', WIN: 'win' },
          PlatformArch: { ARM: 'arm', X86_32: 'x86-32', X86_64: 'x86-64' },
          RequestUpdateCheckStatus: { NO_UPDATE: 'no_update', THROTTLED: 'throttled', UPDATE_AVAILABLE: 'update_available' },
          OnInstalledReason: { CHROME_UPDATE: 'chrome_update', INSTALL: 'install', SHARED_MODULE_UPDATE: 'shared_module_update', UPDATE: 'update' },
          OnRestartRequiredReason: { APP_UPDATE: 'app_update', OS_UPDATE: 'os_update', PERIODIC: 'periodic' },
        },
        csi: function () {},
        loadTimes: function () {},
      };
    }
  } catch (_) {}

  // 3. Spoof navigator.plugins — empty array in headless is a strong signal.
  try {
    const fakeMime = (type, desc, suffixes) => {
      const m = Object.create(MimeType.prototype);
      Object.defineProperty(m, 'type', { get: () => type });
      Object.defineProperty(m, 'description', { get: () => desc });
      Object.defineProperty(m, 'suffixes', { get: () => suffixes });
      return m;
    };
    const fakePlugin = (name, desc, filename, mimes) => {
      const p = Object.create(Plugin.prototype);
      Object.defineProperty(p, 'name', { get: () => name });
      Object.defineProperty(p, 'description', { get: () => desc });
      Object.defineProperty(p, 'filename', { get: () => filename });
      Object.defineProperty(p, 'length', { get: () => mimes.length });
      mimes.forEach((m, i) => { p[i] = m; });
      p.item = (i) => p[i];
      p.namedItem = (n) => mimes.find(m => m.type === n) || null;
      return p;
    };
    const pdfMime = fakeMime('application/x-google-chrome-pdf', 'Portable Document Format', 'pdf');
    const plugins = [
      fakePlugin('Chrome PDF Plugin', 'Portable Document Format', 'internal-pdf-viewer', [pdfMime]),
      fakePlugin('Chrome PDF Viewer', '', 'mhjfbmdgcfjbbpaeojofohoefgiehjai', []),
      fakePlugin('Native Client', '', 'internal-nacl-plugin', []),
    ];
    const arr = Object.create(PluginArray.prototype);
    plugins.forEach((p, i) => { arr[i] = p; });
    Object.defineProperty(arr, 'length', { get: () => plugins.length });
    arr.item = (i) => arr[i];
    arr.namedItem = (n) => plugins.find(p => p.name === n) || null;
    arr.refresh = () => {};
    Object.defineProperty(navigator, 'plugins', { get: () => arr, configurable: true });
  } catch (_) {}

  // 4. Spoof navigator.languages — headless often returns [] or a single entry.
  try {
    Object.defineProperty(navigator, 'languages', {
      get: () => ['en-US', 'en'],
      configurable: true,
    });
  } catch (_) {}

  // 5. Permissions API — headless returns 'prompt' for notifications; real
  //    Chrome returns 'default'. Align the response with Notification.permission.
  try {
    const origQuery = window.navigator.permissions && window.navigator.permissions.query.bind(window.navigator.permissions);
    if (origQuery) {
      window.navigator.permissions.query = (params) =>
        params.name === 'notifications'
          ? Promise.resolve({ state: Notification.permission, onchange: null })
          : origQuery(params);
    }
  } catch (_) {}

  // 6. Hide the HeadlessChrome token that sometimes leaks via navigator.userAgent
  //    even when --user-agent is set (internal APIs can still expose it).
  try {
    const origUA = navigator.userAgent;
    if (origUA.includes('HeadlessChrome')) {
      const patchedUA = origUA.replace(/HeadlessChrome\/[\d.]+\s?/g, '');
      Object.defineProperty(navigator, 'userAgent', { get: () => patchedUA, configurable: true });
    }
  } catch (_) {}
})();
