// Headless flow smoke for the portal local test instance (DESIGN-055, OP479).
// It starts one fresh instance per width (320, 390, 1280), so every flow
// begins from the fixtures, and drives send, launch, history and the Asks
// answer form in an unmodified Chromium, including a double submit and a
// reload after submit.
//
// Default run (acceptance): the production pages are served unchanged. While
// OpenProject #480 is open, Chromium sends "Origin: null" on the portal's
// form posts (every page sets Referrer-Policy: no-referrer) and the production
// origin checks refuse them. The smoke then prints "NOT ACCEPTED (#480)" for
// send, launch and the Asks answer, and exits 1. It never reports those flows
// as PASS unless the unmodified instance accepts them.
//
// --diagnostic-referrer-same-origin: serves every page with
// Referrer-Policy: same-origin through a harness override, so Chromium sends
// the page origin. Every line is labelled DIAGNOSTIC; the run exits 3 when all
// diagnostic checks hold and 1 otherwise. It is never acceptance evidence.
//
// Usage, from the repository root:
//   go build -tags portallocaltest -o ~/.local/bin/portal-localtest ./web/portal/localtest
//   NODE_PATH=/usr/local/lib/node_modules/@playwright/mcp/node_modules \
//     node web/portal/localtest/smoke-flows.cjs [--diagnostic-referrer-same-origin] ~/.local/bin/portal-localtest
const { chromium } = require('playwright');
const { spawn } = require('child_process');
const path = require('path');
const readline = require('readline');

const args = process.argv.slice(2);
const diagnostic = args.includes('--diagnostic-referrer-same-origin');
const binary = args.find((a) => !a.startsWith('--'));
const unknown = args.filter((a) => a.startsWith('--') && a !== '--diagnostic-referrer-same-origin');
const fixtures = path.resolve(__dirname, '../../../scripts/trials/portal_product');
const root = '/portal/projects/dev-env/environments/internal/';
const refused = 'Portal authentication unavailable or denied';
let failed = 0;
let notAccepted = 0;

function check(width, name, ok, detail) {
  if (!ok) failed++;
  const status = diagnostic ? (ok ? 'DIAGNOSTIC PASS' : 'DIAGNOSTIC FAIL') : (ok ? 'PASS' : 'FAIL');
  console.log(`${status} ${width}px ${name}${!ok && detail ? ': ' + detail : ''}`);
}

// accepted reports whether the server took a form post. A refusal by the
// production origin check is NOT ACCEPTED (#480), never a pass.
function accepted(width, flow, body, origin) {
  if (!body.includes(refused)) return true;
  notAccepted++;
  console.log(`NOT ACCEPTED (#480) ${width}px ${flow}: the production origin check refused Chromium's form post (Origin=${origin})`);
  return false;
}

function start() {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, ['-listen', '127.0.0.1:0', '-fixtures', fixtures], { stdio: ['ignore', 'pipe', 'inherit'] });
    const timer = setTimeout(() => { child.kill(); reject(new Error('no URL line')); }, 15000);
    readline.createInterface({ input: child.stdout }).once('line', (line) => {
      clearTimeout(timer);
      resolve({ child, base: new URL(line.slice(line.indexOf('http://'))).origin });
    });
    child.once('error', reject);
  });
}

async function marked(page) {
  const banner = await page.locator('#portal-localtest-banner').isVisible();
  const wide = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth);
  return banner && !wide;
}

async function text(page) {
  return page.locator('body').innerText();
}

// submit clicks (or double-clicks) a submit button and waits for the page
// that the last submission produced.
async function submit(page, button, twice) {
  await Promise.all([page.waitForNavigation({ waitUntil: 'load' }), twice ? button.dblclick() : button.click()]);
  await page.waitForLoadState('load');
}

async function flows(browser, base, width) {
  const refusedBefore = notAccepted;
  const page = await browser.newPage({ viewport: { width, height: 800 } });
  const errors = [];
  let postOrigin = '';
  page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('request', async (r) => { if (r.method() === 'POST') postOrigin = (await r.allHeaders()).origin; });
  if (diagnostic) {
    await page.route('**/*', async (route) => {
      if (route.request().resourceType() !== 'document') return route.continue();
      const response = await route.fetch({ maxRedirects: 0 });
      await route.fulfill({ response, headers: { ...response.headers(), 'referrer-policy': 'same-origin' } });
    });
  }

  // Send: a double-clicked submit, a reload of the receipt, the receipt link.
  await page.goto(base + root + 'sessions?session=fixture-uid-coordinator');
  const composer = page.locator('form.composer:has(input[name="session"][value="fixture-coordinator"])');
  const requestID = await composer.locator('input[name="requestID"]').inputValue();
  await composer.locator('textarea[name="text"]').fill('Fixture message from the flow smoke.');
  await submit(page, composer.locator('button[type="submit"]'), true);
  let body = await text(page);
  if (accepted(width, 'send', body, postOrigin)) {
    check(width, 'send: double submit shows one Delivered receipt', body.includes('Delivered') && body.includes(requestID) && await marked(page), body.slice(0, 160));
    await page.reload();
    body = await text(page);
    check(width, 'send: reload after submit shows the same receipt', body.includes('Delivered') && body.includes(requestID) && await marked(page), body.slice(0, 160));
    await page.goto(base + root + 'session-messages/' + requestID);
    body = await text(page);
    check(width, 'send: receipt link', body.includes('Delivered') && body.includes(requestID) && await marked(page), body.slice(0, 160));
  }

  // History: the conversation of a history target, then a reload. A GET flow.
  await page.goto(base + root + 'sessions?session=fixture-uid-coordinator');
  check(width, 'history: conversation shown', (await text(page)).includes('Fixture conversation: review the portal composer.') && await marked(page));
  await page.reload();
  check(width, 'history: reload', (await text(page)).includes('Fixture reply: the composer renders.') && await marked(page));

  // Launch: a double-clicked submit, a reload, then the same request again.
  await page.goto(base + root + 'sessions#launch');
  let launch = page.locator('form.launch');
  const launchID = await launch.locator('input[name="requestID"]').inputValue();
  await launch.locator('select[name="repo"]').selectOption('dev-env');
  await launch.locator('select[name="mode"]').selectOption('task');
  await launch.locator('textarea[name="prompt"]').fill('Fixture task from the flow smoke.');
  await submit(page, launch.locator('button[type="submit"]'), true);
  const launched = page.url();
  body = await text(page);
  if (accepted(width, 'launch', body, postOrigin)) {
    check(width, 'launch: double submit selects one new session', /session=fixture-uid-launch-1$/.test(launched) && body.includes('fixture-launch-1') && !body.includes('fixture-launch-2') && await marked(page), launched);
    await page.reload();
    check(width, 'launch: reload after submit', page.url() === launched && (await text(page)).includes('fixture-launch-1'));
    await page.goto(base + root + 'sessions#launch');
    launch = page.locator('form.launch');
    await launch.evaluate((form, id) => { form.querySelector('input[name="requestID"]').value = id; }, launchID);
    await launch.locator('select[name="repo"]').selectOption('dev-env');
    await launch.locator('select[name="mode"]').selectOption('task');
    await launch.locator('textarea[name="prompt"]').fill('Fixture task from the flow smoke.');
    await submit(page, launch.locator('button[type="submit"]'), false);
    check(width, 'launch: the same request again returns the same session', page.url() === launched && !(await text(page)).includes('fixture-launch-2'), page.url());
  }

  // Asks: a double-clicked answer, a reload, then an approval.
  await page.goto(base + root + 'asks?ask=ask-0000000000000001');
  const answer = page.locator('form.ask-answer:has(input[name="askID"][value="ask-0000000000000001"])');
  await answer.locator('input[name="optionId"][value="small"]').check();
  await answer.locator('textarea[name="text"]').fill('Fixture answer from the flow smoke.');
  await submit(page, answer.locator('button[type="submit"]'), true);
  body = await text(page);
  if (accepted(width, 'asks answer', body, postOrigin)) {
    check(width, 'asks: double submit records one answer', /asks\?ask=ask-0000000000000001$/.test(page.url()) && body.includes('Recorded answer') && await marked(page), page.url() + ' ' + body.slice(0, 160));
    await page.reload();
    const reopened = await page.locator('form.ask-answer:has(input[name="askID"][value="ask-0000000000000001"])').count();
    check(width, 'asks: reload after submit keeps the recorded answer', reopened === 0 && (await text(page)).includes('Recorded answer'));
    await page.goto(base + root + 'asks?ask=ask-0000000000000002');
    const approval = page.locator('form.ask-answer:has(input[name="askID"][value="ask-0000000000000002"])');
    await approval.locator('input[name="confirm"]').check();
    await submit(page, approval.locator('button[value="approve"]'), false);
    body = await text(page);
    check(width, 'asks: approval recorded', /asks\?ask=ask-0000000000000002$/.test(page.url()) && body.includes('Approved') && await marked(page), body.slice(0, 160));
  }

  // A refused post logs its 403 in the console; that refusal is already
  // reported as NOT ACCEPTED (#480), so only other errors count here.
  const forbidden = 'Failed to load resource: the server responded with a status of 403 (Forbidden)';
  const other = notAccepted > refusedBefore ? errors.filter((e) => e !== forbidden) : errors;
  check(width, 'no console errors' + (other.length < errors.length ? ` (${errors.length - other.length} 403s from refused posts excluded)` : ''), other.length === 0, other.join(' | '));
  await page.close();
}

async function run(width) {
  const { child, base } = await start();
  const browser = await chromium.launch({ headless: true });
  try {
    await flows(browser, base, width);
  } catch (err) {
    check(width, 'flow run', false, err.message.split('\n')[0]);
  } finally {
    await browser.close();
    child.kill('SIGTERM');
  }
}

(async () => {
  if (!binary || unknown.length) {
    console.log('usage: node smoke-flows.cjs [--diagnostic-referrer-same-origin] <portal-localtest binary>');
    process.exit(2);
  }
  if (diagnostic) {
    console.log('DIAGNOSTIC ONLY: pages are served with Referrer-Policy: same-origin by a harness override. Not acceptance evidence.');
  }
  for (const width of [320, 390, 1280]) await run(width);
  if (diagnostic) {
    console.log(`DIAGNOSTIC ${failed ? 'FAILED' : 'COMPLETE'}: ${failed} failed checks; never acceptance.`);
    process.exit(failed ? 1 : 3);
  }
  if (notAccepted) console.log(`NOT ACCEPTED (#480): ${notAccepted} write-flow posts refused by the production origin check.`);
  process.exit(failed || notAccepted ? 1 : 0);
})();
