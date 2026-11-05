// Headless smoke for the portal local test instance (DESIGN-055).
// Usage: NODE_PATH=/usr/local/lib/node_modules/@playwright/mcp/node_modules \
//   node web/portal/localtest/smoke.cjs http://127.0.0.1:8099
const { chromium } = require('playwright');
const base = process.argv[2] || 'http://127.0.0.1:8099';
const root = '/portal/projects/dev-env/environments/internal/';
const pages = { sessions: 'fixture-portal-polish', tasks: 'Fixture: portal visual refresh', pages: 'Fixture sample alert', asks: 'Which fixture should the agent use', claims: 'fixture-coordinator' };
(async () => {
  const browser = await chromium.launch({ headless: true });
  let failed = 0;
  for (const width of [320, 390, 1280]) {
    const page = await browser.newPage({ viewport: { width, height: 800 } });
    const errors = [];
    page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
    for (const [name, row] of Object.entries(pages)) {
      const response = await page.goto(base + root + name);
      const banner = page.locator('#portal-localtest-banner');
      const ok = response.status() === 200 && (await banner.isVisible()) &&
        (await page.locator('body').innerText()).includes(row) &&
        (await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth));
      if (!ok) failed++;
      console.log(`${ok ? 'PASS' : 'FAIL'} ${width}px ${name}`);
    }
    if (errors.length) { failed++; console.log(`FAIL ${width}px console errors: ${errors.length}`); }
    await page.close();
  }
  await browser.close();
  process.exit(failed ? 1 : 0);
})();
