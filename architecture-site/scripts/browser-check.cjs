// Optional real-browser checks; the site itself has no JavaScript dependencies.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const root = path.resolve(__dirname, '..');
const mime = { '.html':'text/html', '.css':'text/css', '.js':'text/javascript', '.svg':'image/svg+xml' };
const server = http.createServer((req,res) => {
  const target = path.resolve(root, '.' + new URL(req.url, 'http://localhost').pathname);
  if (!target.startsWith(root + path.sep)) { res.writeHead(404).end(); return; }
  fs.readFile(target, (error, data) => {
    if (error) { res.writeHead(404).end(); return; }
    res.setHeader('Content-Type', `${mime[path.extname(target)] || 'text/plain'}; charset=utf-8`);
    res.end(data);
  });
});
(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = `http://127.0.0.1:${server.address().port}`;
  let browser;
  try {
    browser = await chromium.launch({ headless:true, executablePath:process.env.CHROMIUM_PATH || undefined, args:process.env.CHROMIUM_NO_SANDBOX === '1' ? ['--no-sandbox'] : [] });
    const context = await browser.newContext({ viewport:{width:1440,height:1000}, colorScheme:'light' });
    const page = await context.newPage();
    const errors=[]; const external=[];
    page.on('pageerror', error=>errors.push(error.message));
    page.on('request', request=>{ if (!request.url().startsWith(origin)) external.push(request.url()); });
    const files=fs.readdirSync(root).filter(file=>file.endsWith('.html'));
    let layouts=0;
    for (const width of [1440,1024,768,390,320]) {
      await page.setViewportSize({width,height:900});
      for (const file of files) {
        await page.goto(`${origin}/${file}`);
        assert.equal(await page.locator('h1').count(),1,file);
        assert.equal(await page.locator('.site-header img, .site-header svg, .brand, .brand-mark').count(),0,file);
        assert.doesNotMatch(await page.locator('body').innerText(), /\bKepler\b/i, file);
        assert.doesNotMatch(await page.title(), /\bKepler\b/i, file);
        assert.equal(await page.locator('.chapter-nav [aria-current="page"]').count(),1,file);
        assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`${file}: overflow at ${width}`);
        const missing=await page.evaluate(()=>[...document.querySelectorAll('a[href^="#"]')].filter(a=>!document.getElementById(a.hash.slice(1))).map(a=>a.href));
        assert.deepEqual(missing,[],file);
        layouts++;
      }
    }
    await page.setViewportSize({width:1440,height:1000});
    await page.goto(`${origin}/index.html`);
    if (process.env.SITE_SCREENSHOTS) await page.screenshot({path:path.join(process.env.SITE_SCREENSHOTS,'architecture-light.png')});
    await page.locator('.search-trigger').click();
    await page.locator('.search-input').fill('工具 权限');
    assert.ok(await page.locator('.search-results a').count()>0);
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('.search-dialog').evaluate(d=>d.open),false);
    assert.equal(await page.locator('.search-trigger').evaluate(e=>e===document.activeElement),true);
    await page.keyboard.press('/');
    await page.locator('.search-input').fill('记忆生命周期');
    assert.match(await page.locator('.search-results a').first().getAttribute('href'), /^memory\.html#/);
    await page.locator('.search-input').fill('JSONL');
    assert.ok(await page.locator('.search-results a').count()>0);
    const href=await page.locator('.search-results a').first().getAttribute('href');
    await page.locator('.search-results a').first().click(); await page.waitForURL(`**/${href}`);
    assert.equal(await page.locator('.search-dialog').evaluate(d=>d.open),false);
    await page.locator('.search-trigger').click(); await page.locator('.search-input').fill('<img src=x onerror=alert(1)>');
    assert.equal(await page.locator('.search-results a').count(),0); assert.equal(await page.locator('.search-results img').count(),0);
    await page.locator('.search-input').fill(''); assert.match(await page.locator('.search-status').innerText(),/全部章节/);
    await page.keyboard.press('Escape');
    await page.locator('.theme-toggle').click(); assert.equal(await page.locator('html').getAttribute('data-theme'),'dark');
    for (const file of files) { await page.goto(`${origin}/${file}`); assert.equal(await page.locator('html').getAttribute('data-theme'),'dark'); }
    await page.goto(`${origin}/index.html`);
    if (process.env.SITE_SCREENSHOTS) await page.screenshot({path:path.join(process.env.SITE_SCREENSHOTS,'architecture-dark.png')});
    await page.locator('.toc a[href="#products"]').click();
    await page.waitForFunction(()=>document.querySelector('.toc [aria-current="location"]')?.getAttribute('href') === '#products');
    assert.equal(await page.locator('.toc [aria-current="location"]').getAttribute('href'),'#products');
    await page.evaluate(()=>scrollTo(0,document.documentElement.scrollHeight));
    await page.waitForFunction(()=>document.querySelector('.toc [aria-current="location"]')?.getAttribute('href') === '#source');
    assert.equal(await page.locator('.toc [aria-current="location"]').getAttribute('href'),'#source');
    await page.setViewportSize({width:390,height:844}); await page.goto(`${origin}/index.html`); await page.locator('.theme-toggle').click();
    if (process.env.SITE_SCREENSHOTS) await page.screenshot({path:path.join(process.env.SITE_SCREENSHOTS,'architecture-mobile.png')});
    await page.locator('.menu-trigger').click(); assert.equal(await page.locator('.mobile-dialog').evaluate(d=>d.open),true);
    await page.keyboard.press('Escape'); assert.equal(await page.locator('.menu-trigger').evaluate(e=>e===document.activeElement),true);
    await page.locator('.menu-trigger').click(); await page.locator('.mobile-chapters a[href="web.html"]').click(); await page.waitForURL('**/web.html');
    assert.equal(await page.locator('.mobile-dialog').evaluate(d=>d.open),false);
    const blockedStorage=await browser.newContext({colorScheme:'dark'});
    await blockedStorage.addInitScript(()=>Object.defineProperty(window,'localStorage',{get(){throw new Error('Storage unavailable')}}));
    const blockedPage=await blockedStorage.newPage(); await blockedPage.goto(`${origin}/index.html`);
    assert.equal(await blockedPage.locator('html').getAttribute('data-theme'),'dark'); await blockedPage.locator('.theme-toggle').click();
    await blockedStorage.close();
    const nojs=await browser.newContext({javaScriptEnabled:false,viewport:{width:390,height:844}});
    const staticPage=await nojs.newPage(); await staticPage.goto(`${origin}/index.html`);
    assert.equal(await staticPage.locator('.chapter-nav a').count(),files.length);
    assert.ok((await staticPage.locator('main').innerText()).includes('模块职责'));
    assert.equal(await staticPage.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);
    await nojs.close();
    await page.emulateMedia({media:'print'}); assert.equal(await page.locator('.site-header').isVisible(),false);
    assert.deepEqual(errors,[]);assert.deepEqual(external,[]);
    console.log(`PASS: ${layouts} page/viewport combinations; ${files.length} dark pages; search, focus, navigation, storage failure, no-JS, print and zero external requests.`);
  } finally { if (browser) await browser.close(); server.close(); }
})().catch(error=>{console.error(error);server.close();process.exitCode=1});
