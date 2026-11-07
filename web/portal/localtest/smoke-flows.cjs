// Headless flow smoke for the portal local test instance (DESIGN-055, OP479).
// It starts one fresh instance per width (320, 390, 1280), so every flow
// begins from the fixtures, and drives send, launch, history and the Asks
// answer form in an unmodified Chromium, including a double submit and a
// reload after submit. It also drives back/forward on a send receipt, an
// abort before the POST is sent, a dropped response after fixture acceptance,
// a wrong-target refusal, and fixture logout.
//
// Default run (acceptance): the production pages are served unchanged. Session
// routes set Referrer-Policy: same-origin (PR #948), so Chromium sends the
// page origin and the production origin checks can accept the posts. The
// historical #480 finding is the opposite, recorded on 2026-11-05 against
// Referrer-Policy: no-referrer. Attention still sets no-referrer and is not
// part of this smoke. A POST flow is never reported as PASS unless the
// unmodified instance accepts it. A refused post still prints
// "NOT ACCEPTED (#480)" and the run exits 1. History is a GET flow.
//
// --diagnostic-referrer-same-origin: serves every document with
// Referrer-Policy: same-origin through a harness override. Every line is
// labelled DIAGNOSTIC, a refused post included, and a refused post fails the
// run. It exits 3 when every diagnostic check holds and 1 otherwise. It is
// never acceptance evidence. The server already sends same-origin; the flag
// does not change that fact.
//
// Usage, from the repository root:
//   go build -tags portallocaltest -o ~/.local/bin/portal-localtest ./web/portal/localtest
//   NODE_PATH=/usr/local/lib/node_modules/@playwright/mcp/node_modules \
//     node web/portal/localtest/smoke-flows.cjs [--diagnostic-referrer-same-origin] ~/.local/bin/portal-localtest
const { spawn } = require('child_process');
const net = require('net');
const path = require('path');
const readline = require('readline');
const {
  parseFixtureBase, announcedFixtureBase, localRequest, applyFixtureResponse, serviceWorkerClaim,
  fixtureWorkerPath, fixtureWorkerSource, fixtureContextOptions, fixtureBrowserLaunch, sameFixtureOrigin, proxyRequestDecision,
} = require('./smoke-flows-guard.cjs');

const args = process.argv.slice(2);
const diagnostic = args.includes('--diagnostic-referrer-same-origin');
const binary = args.find((a) => !a.startsWith('--'));
const unknown = args.filter((a) => a.startsWith('--') && a !== '--diagnostic-referrer-same-origin');
const fixtures = path.resolve(__dirname, '../../../scripts/trials/portal_product');
const root = '/portal/projects/dev-env/environments/internal/';
const sendPath = root + 'session-messages';
const operatorSend = '/v1/portal/session-messages';
const witnessPath = '/portal-localtest/fixture-calls';
const receiptText = 'Delivered to the agent';
const refused = 'Portal authentication unavailable or denied';
let failed = 0;
let notAccepted = 0;

function otherLoopbackURL(allowedOrigin) {
  const url = new URL(allowedOrigin);
  url.port = url.port === '1' ? '2' : '1';
  return url.origin + '/portal-localtest-wrong-port';
}

function postedID(request) {
  return new URLSearchParams(request.postData() || '').get('requestID') || '';
}

function headerValues(rawHeaders, name) {
  const want = name.toLowerCase();
  const values = [];
  for (let i = 0; i < rawHeaders.length; i += 2) {
    if (rawHeaders[i].toLowerCase() === want) values.push(rawHeaders[i + 1]);
  }
  return values;
}

function check(width, name, ok, detail) {
  if (!ok) failed++;
  const status = diagnostic ? (ok ? 'DIAGNOSTIC PASS' : 'DIAGNOSTIC FAIL') : (ok ? 'PASS' : 'FAIL');
  console.log(`${status} ${width}px ${name}${!ok && detail ? ': ' + detail : ''}`);
}

// accepted reports whether the server took a form post. A refusal by the
// production origin check is NOT ACCEPTED (#480), never a pass; in the
// diagnostic run it is also a failed diagnostic check.
function accepted(width, flow, body, origin) {
  if (!body.includes(refused)) return true;
  notAccepted++;
  if (diagnostic) failed++;
  console.log(`${diagnostic ? 'DIAGNOSTIC ' : ''}NOT ACCEPTED (#480) ${width}px ${flow}: the production origin check refused Chromium's form post (Origin=${origin})`);
  return false;
}

function start() {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, ['-listen', '127.0.0.1:0', '-fixtures', fixtures], { stdio: ['ignore', 'pipe', 'inherit'] });
    const timer = setTimeout(() => { child.kill(); reject(new Error('no URL line')); }, 15000);
    readline.createInterface({ input: child.stdout }).once('line', (line) => {
      clearTimeout(timer);
      const announced = announcedFixtureBase(line);
      if (!announced.ok) {
        child.kill();
        reject(new Error('refused fixture base: ' + announced.reason));
        return;
      }
      resolve({ child, base: announced.origin });
    });
    child.once('error', (err) => {
      clearTimeout(timer);
      try { child.kill(); } catch (killErr) { /* spawn already failed */ }
      reject(err);
    });
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

async function composerFields(page) {
  const composer = page.locator('form.composer:has(input[name="session"][value="fixture-coordinator"])');
  return {
    composer,
    requestID: await composer.locator('input[name="requestID"]').inputValue(),
    csrf: await composer.locator('input[name="csrf"]').inputValue(),
    session: await composer.locator('input[name="session"]').inputValue(),
    sessionUID: await composer.locator('input[name="sessionUID"]').inputValue(),
  };
}

async function flows(browser, base, width) {
  const refusedBefore = notAccepted;
  const allowed = parseFixtureBase(base);
  if (!allowed.ok) throw new Error('refused fixture base: ' + allowed.reason);
  const allowedOrigin = allowed.origin;
  const wrongPortURL = otherLoopbackURL(allowedOrigin);
  const swURL = allowedOrigin + fixtureWorkerPath;
  const context = await browser.newContext(Object.assign({ viewport: { width, height: 800 } }, fixtureContextOptions));
  const page = await context.newPage();
  const errors = [];
  const failedRequests = [];
  const refusedURLs = [];
  const abortInterceptions = [];
  const dropInterceptions = [];
  const redirectRefusals = [];
  const issuedURLs = [];
  let postOrigin = '';
  let postMode = '';
  let abortRequestID = '';
  let dropRequestID = '';
  let dropBaseline = -1;
  let wrongPortWait = null;
  function watch(target) {
    target.on('console', (m) => { if (m.type() === 'error') errors.push({ text: m.text(), url: (m.location() && m.location().url) || '' }); });
    target.on('dialog', (dialog) => dialog.accept());
  }
  watch(page);
  context.on('page', (next) => { if (next !== page) watch(next); });
  context.on('request', async (r) => {
    issuedURLs.push(r.url());
    if (r.method() === 'POST') postOrigin = (await r.allHeaders()).origin;
  });
  context.on('requestfailed', (r) => { failedRequests.push({ url: r.url(), errorText: (r.failure() && r.failure().errorText) || '' }); });
  await context.route('**/*', async (route) => {
    const request = route.request();
    if (!sameFixtureOrigin(request.url(), allowedOrigin)) {
      refusedURLs.push(request.url());
      if (wrongPortWait && request.url() === wrongPortURL) wrongPortWait(request.url());
      await route.abort('blockedbyclient');
      return;
    }
    if (postMode === 'abort-before' && request.method() === 'POST' && new URL(request.url()).pathname === sendPath) {
      const id = postedID(request);
      const matched = id !== '' && id === abortRequestID;
      abortInterceptions.push({ url: request.url(), id, matched });
      if (matched) postMode = '';
      await route.abort('failed');
      return;
    }
    if (postMode === 'drop-response' && request.method() === 'POST' && new URL(request.url()).pathname === sendPath) {
      const id = postedID(request);
      const matched = id !== '' && id === dropRequestID;
      if (!matched) {
        dropInterceptions.push({ url: request.url(), id, matched: false, acceptedBeforeAbort: false });
        await route.abort('failed');
        return;
      }
      const response = await route.fetch({ maxRedirects: 0 });
      const status = response.status();
      const location = response.headers()['location'] || '';
      const plan = applyFixtureResponse(status, location, request.url(), allowedOrigin, refuseRedirectFollow);
      if (plan.action === 'refuse' || plan.next) {
        if (plan.action === 'refuse') redirectRefusals.push({ from: request.url(), location, next: plan.next });
        dropInterceptions.push({ url: request.url(), id, matched: true, acceptedBeforeAbort: false });
        await route.abort('failed');
        return;
      }
      let acceptedBeforeAbort = false;
      try {
        const receipt = await localRequest(base, { method: 'GET', path: sendPath + '/' + id });
        acceptedBeforeAbort = await sendCount() === dropBaseline + 1 && receipt.status === 200 && receipt.body.includes(receiptText) && receipt.body.includes(id);
      } catch (err) {
        acceptedBeforeAbort = false;
      }
      dropInterceptions.push({ url: request.url(), id, matched: true, acceptedBeforeAbort });
      postMode = '';
      await route.abort('failed');
      return;
    }
    if (request.method() === 'GET' && request.url() === swURL) {
      await route.fulfill({ status: 200, contentType: 'application/javascript', body: fixtureWorkerSource });
      return;
    }
    const response = await route.fetch({ maxRedirects: 0 });
    const status = response.status();
    const location = response.headers()['location'] || '';
    const plan = applyFixtureResponse(status, location, request.url(), allowedOrigin, refuseRedirectFollow);
    if (plan.action === 'refuse') {
      redirectRefusals.push({ from: request.url(), location, next: plan.next });
      await route.abort('failed');
      return;
    }
    if (diagnostic && request.resourceType() === 'document') {
      await route.fulfill({ response, headers: { ...response.headers(), 'referrer-policy': 'same-origin' } });
      return;
    }
    await route.fulfill({ response });
  });
  function refuseRedirectFollow(target) {
    throw new Error('redirect follow refused: ' + target);
  }
  async function sendCount() {
    const res = await localRequest(base, { method: 'GET', path: witnessPath });
    if (res.status !== 200) throw new Error('fixture witness ' + res.status);
    const snap = JSON.parse(res.body);
    const row = (snap.calls || []).find((call) => call.method === 'POST' && call.path === operatorSend);
    return row ? row.count : 0;
  }

  const wrongPortSeen = new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('wrong-port popup was not intercepted')), 3000);
    wrongPortWait = (url) => { clearTimeout(timer); resolve(url); };
  });
  const opened = await page.evaluate((url) => { const child = window.open(url); return child ? 'opened' : 'blocked'; }, wrongPortURL);
  let seenURL = '';
  try { seenURL = await wrongPortSeen; } catch (err) { seenURL = err.message; }
  check(width, 'isolation: wrong-port popup is refused before a connection', seenURL === wrongPortURL && refusedURLs.filter((url) => url === wrongPortURL).length === 1, seenURL + ' ' + opened);
  check(width, 'isolation: non-fixture destinations are refused without a dial', !parseFixtureBase('https://example.invalid/portal').ok && !parseFixtureBase('http://example.invalid:7777').ok && !parseFixtureBase(wrongPortURL).ok && parseFixtureBase(base).ok);
  const wrongHost = await localRequest(base, { method: 'GET', path: root + 'sessions', headers: { Host: 'dev-env-v2.haynesops.com' } });
  check(width, 'isolation: production host on the fixture port is refused', wrongHost.status === 403 && wrongHost.body.includes('Refused:') && !wrongHost.body.includes('fixture-portal-polish'), String(wrongHost.status));
  for (const item of [['sessions', root + 'sessions'], ['asks', root + 'asks?ask=ask-0000000000000001'], ['logout', '/auth/logout']]) {
    const res = await localRequest(base, { method: 'GET', path: item[1] });
    const policy = headerValues(res.rawHeaders, 'referrer-policy');
    check(width, item[0] + ': one Referrer-Policy same-origin', res.status === 200 && policy.length === 1 && policy[0] === 'same-origin', policy.join(','));
  }

  // Send: a double-clicked submit, back/forward, a reload of the receipt, the receipt link.
  await page.goto(base + root + 'sessions?session=fixture-uid-coordinator');
  const swResult = await page.evaluate(async (url) => {
    if (!('serviceWorker' in navigator)) return { api: 'absent', result: 'absent', active: false };
    try {
      const registration = await navigator.serviceWorker.register(url);
      const active = Boolean(registration && (registration.active || registration.installing || registration.waiting));
      return { api: 'present', result: active ? 'active' : 'blocked', active };
    } catch (err) {
      return { api: 'present', result: 'blocked', active: false };
    }
  }, swURL);
  const claim = serviceWorkerClaim(page.url(), swURL, allowedOrigin, { script: fixtureWorkerSource, documentOrigin: new URL(page.url()).origin, api: swResult.api, result: swResult.result, active: swResult.active });
  check(width, 'isolation: fixture-origin service worker registration is blocked', claim.label === 'browser' && claim.proved && claim.blocked && context.serviceWorkers().length === 0, claim.label + ' ' + claim.reason);
  const composer = page.locator('form.composer:has(input[name="session"][value="fixture-coordinator"])');
  const requestID = await composer.locator('input[name="requestID"]').inputValue();
  const csrf = await composer.locator('input[name="csrf"]').inputValue();
  const sessionUID = await composer.locator('input[name="sessionUID"]').inputValue();
  const beforeSend = await sendCount();
  const evil = await localRequest(base, {
    method: 'POST', path: sendPath,
    headers: { Origin: 'https://evil.example', 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ csrf, requestID, session: 'fixture-coordinator', sessionUID, text: 'Fixture message from the flow smoke.' }).toString(),
  });
  check(width, 'send: wrong origin refused before acceptance', evil.status === 403 && await sendCount() === beforeSend && !evil.body.includes(receiptText), String(evil.status));
  await composer.locator('textarea[name="text"]').fill('Fixture message from the flow smoke.');
  await submit(page, composer.locator('button[type="submit"]'), true);
  let body = await text(page);
  if (accepted(width, 'send', body, postOrigin)) {
    const sends = await sendCount();
    check(width, 'send: double submit shows one Delivered receipt', body.includes('Delivered') && body.includes(requestID) && sends === beforeSend + 1 && await marked(page), 'sends ' + String(sends) + ' ' + body.slice(0, 160));
    let navErr = '';
    try {
      await page.goBack({ waitUntil: 'load' });
      await page.goForward({ waitUntil: 'load' });
    } catch (err) {
      navErr = err.message.split('\n')[0];
    }
    body = await text(page);
    check(width, 'send: back and forward keep one receipt', navErr === '' && body.includes(receiptText) && body.includes(requestID) && await sendCount() === sends && await marked(page), navErr || body.slice(0, 160));
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

  // Abort before the browser sends the POST. The fixture must not accept it.
  await page.goto(base + root + 'sessions?session=fixture-uid-coordinator');
  let fields = await composerFields(page);
  await fields.composer.locator('textarea[name="text"]').fill('Fixture message aborted before dispatch.');
  const beforeAbort = await sendCount();
  abortRequestID = fields.requestID;
  postMode = 'abort-before';
  let abortErr = '';
  try {
    await submit(page, fields.composer.locator('button[type="submit"]'), false);
  } catch (err) {
    abortErr = err.message.split('\n')[0];
  } finally {
    postMode = '';
  }
  const abortHit = abortInterceptions.filter((hit) => hit.matched && hit.id === fields.requestID);
  const abortReceipt = await localRequest(base, { method: 'GET', path: sendPath + '/' + fields.requestID });
  check(width, 'send: abort before dispatch accepts nothing', abortErr !== '' && abortInterceptions.length === 1 && abortHit.length === 1 && abortHit[0].url === base + sendPath && await sendCount() === beforeAbort && !(await text(page)).includes(receiptText) && !abortReceipt.body.includes(receiptText), abortErr || 'interceptions ' + abortInterceptions.length);

  // The fixture accepts the POST, then only the response is dropped. The
  // receipt GET is the acceptance witness. Replaying that request stays one send.
  await page.goto(base + root + 'sessions?session=fixture-uid-coordinator');
  fields = await composerFields(page);
  const droppedID = fields.requestID;
  const dropText = 'Fixture message accepted before its response was dropped.';
  await fields.composer.locator('textarea[name="text"]').fill(dropText);
  const beforeDrop = await sendCount();
  dropRequestID = droppedID;
  dropBaseline = beforeDrop;
  postMode = 'drop-response';
  let dropErr = '';
  try {
    await submit(page, fields.composer.locator('button[type="submit"]'), false);
  } catch (err) {
    dropErr = err.message.split('\n')[0];
  } finally {
    postMode = '';
  }
  const hidden = await text(page);
  const acceptedSends = await sendCount();
  const stored = await localRequest(base, { method: 'GET', path: sendPath + '/' + droppedID });
  const dropHit = dropInterceptions.filter((hit) => hit.matched && hit.id === droppedID && hit.acceptedBeforeAbort);
  check(width, 'send: browser loses the response only after fixture acceptance', dropErr !== '' && dropInterceptions.length === 1 && dropHit.length === 1 && dropHit[0].url === base + sendPath && !hidden.includes(receiptText) && acceptedSends === beforeDrop + 1 && stored.status === 200 && stored.body.includes(receiptText) && stored.body.includes(droppedID), dropErr || hidden.slice(0, 120));
  await page.goto(base + root + 'sessions?session=fixture-uid-coordinator');
  fields = await composerFields(page);
  await fields.composer.evaluate((form, id) => { form.querySelector('input[name="requestID"]').value = id; }, droppedID);
  await fields.composer.locator('textarea[name="text"]').fill(dropText);
  await submit(page, fields.composer.locator('button[type="submit"]'), false);
  body = await text(page);
  check(width, 'send: replay after a dropped response is the same receipt', body.includes(receiptText) && body.includes(droppedID) && await sendCount() === acceptedSends && await marked(page), body.slice(0, 160));

  // A refused post logs its 403 in the console; that refusal is already
  // reported as NOT ACCEPTED (#480), so only other errors count here. The
  // intentional abort also logs one failed load.
  const forbidden = 'Failed to load resource: the server responded with a status of 403 (Forbidden)';
  const intentionalURLs = new Set([
    ...abortInterceptions.filter((hit) => hit.matched).map((hit) => hit.url),
    ...dropInterceptions.filter((hit) => hit.matched).map((hit) => hit.url),
    ...refusedURLs,
    swURL,
  ]);
  const skippedFailures = new Set();
  const other = errors.filter((entry) => {
    if (notAccepted > refusedBefore && entry.text === forbidden) return false;
    const failure = failedRequests.find((item, index) => !skippedFailures.has(index) && intentionalURLs.has(item.url) && item.errorText && entry.url === item.url && entry.text.includes(item.errorText));
    if (failure) { skippedFailures.add(failedRequests.indexOf(failure)); return false; }
    return true;
  });
  check(width, 'no console errors' + (other.length < errors.length ? ` (${errors.length - other.length} intentional request failures excluded)` : ''), other.length === 0, other.map((entry) => entry.url + ' ' + entry.text).join(' | '));

  // Fixture logout is last. It revokes this instance's fixed session only.
  await page.goto(base + '/auth/logout');
  await submit(page, page.locator('form[action="/auth/logout"] button[type="submit"]'), false);
  const revoked = await text(page);
  check(width, 'logout: fixture session revoked', revoked.includes('Local test instance (fixture data, not production).') && revoked.includes('Portal session revoked.') && revoked.includes('Identity-provider SSO status is separate.') && !revoked.includes('fixture-portal-polish'), revoked.slice(0, 180));
  console.log(`OBSERVED ${width}px logout revokes the fixture store only; it is not production login, identity-provider logout, or production session invalidation`);
  await page.goto(base + root + 'sessions');
  check(width, 'logout: a later session request is refused', (await text(page)).includes(refused) && !(await text(page)).includes('fixture-portal-polish'));
  let backObserved = 'not run';
  try {
    await page.goBack({ waitUntil: 'load' });
    backObserved = (await text(page)).includes('fixture-portal-polish') ? 'back still showed fixture session text' : 'back showed no fixture session text';
  } catch (err) {
    backObserved = err.message.split('\n')[0];
  }
  console.log(`OBSERVED ${width}px logout back: ${backObserved}`);
  await page.goto(base + root + 'sessions');
  check(width, 'logout: a fresh request after back is still refused', (await text(page)).includes(refused) && !(await text(page)).includes('fixture-portal-polish'), backObserved);
  const leaked = issuedURLs.filter((url) => !sameFixtureOrigin(url, allowedOrigin) && !refusedURLs.includes(url));
  const followed = redirectRefusals.filter((item) => item.next && item.next !== allowedOrigin && issuedURLs.some((url) => sameFixtureOrigin(url, item.next)));
  check(width, 'isolation: browser traffic stayed on the fixture origin', leaked.length === 0 && followed.length === 0, leaked.concat(followed.map((item) => item.location)).join(' | '));
  await context.close();
}

const proxyCloseDeadlineMs = 500;

function trackProxyPeer(tracked, peer, kind) {
  const set = kind === 'upstream' ? tracked.upstreams : tracked.sockets;
  set.add(peer);
  if (peer && typeof peer.on === 'function') peer.on('close', () => set.delete(peer));
  return peer;
}

// closeTrackedProxy destroys every acquired socket and upstream, then stops
// waiting at deadlineMs even if server.close never returns.
function closeTrackedProxy(server, tracked, deadlineMs) {
  const sockets = tracked && tracked.sockets ? tracked.sockets : [];
  const upstreams = tracked && tracked.upstreams ? tracked.upstreams : [];
  for (const peer of sockets) {
    try { if (peer && typeof peer.destroy === 'function') peer.destroy(); } catch (err) { /* keep closing the rest */ }
  }
  for (const peer of upstreams) {
    try { if (peer && typeof peer.destroy === 'function') peer.destroy(); } catch (err) { /* keep closing the rest */ }
  }
  return new Promise((resolve) => {
    let settled = false;
    let timer;
    const finish = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve();
    };
    timer = setTimeout(finish, deadlineMs);
    if (!server || typeof server.close !== 'function') {
      finish();
      return;
    }
    try { server.close(finish); } catch (err) { finish(); }
  });
}

const proxyHeaderLimit = 65536;
const proxyBodyLimit = 1048576;

function destroyPeer(peer) {
  try { if (peer && typeof peer.destroy === 'function') peer.destroy(); } catch (err) { /* already closing */ }
}

function proxyStatus(socket, status) {
  if (!socket || socket.destroyed) return;
  socket.resume();
  socket.end('HTTP/1.1 ' + status + '\r\nConnection: close\r\nContent-Length: 0\r\n\r\n');
}

// requestContentLength accepts one decimal length and refuses chunked or
// duplicated lengths before any of the body is forwarded.
function requestContentLength(head) {
  let seen = false;
  let length = 0;
  for (const line of head.split('\r\n').slice(1)) {
    if (/^transfer-encoding:/i.test(line)) return { ok: false };
    const match = /^content-length:\s*([0-9]+)\s*$/i.exec(line);
    if (!match) continue;
    if (seen) return { ok: false };
    seen = true;
    length = Number(match[1]);
    if (!Number.isInteger(length) || String(length) !== match[1] || length > proxyBodyLimit) return { ok: false };
  }
  return { ok: true, length };
}

function rewriteOriginRequest(head, decision) {
  const lines = head.split('\r\n');
  const request = lines[0].split(' ');
  if (request.length < 2 || !decision.path || !decision.path.startsWith('/')) return '';
  request[1] = decision.path;
  const kept = [request.join(' ')];
  let sawHost = false;
  for (let i = 1; i < lines.length; i++) {
    if (/^(?:connection|proxy-connection|keep-alive):/i.test(lines[i])) continue;
    if (/^host:/i.test(lines[i])) {
      kept.push('Host: ' + decision.host + ':' + decision.port);
      sawHost = true;
      continue;
    }
    kept.push(lines[i]);
  }
  if (!sawHost) kept.splice(1, 0, 'Host: ' + decision.host + ':' + decision.port);
  kept.push('Connection: close');
  return kept.join('\r\n') + '\r\n\r\n';
}

function createPull(socket) {
  let buf = Buffer.alloc(0);
  let ended = false;
  let failed = null;
  let notify = null;
  const onData = (chunk) => {
    buf = Buffer.concat([buf, chunk]);
    if (notify) { const fn = notify; notify = null; fn(); }
  };
  const onEnd = () => {
    ended = true;
    if (notify) { const fn = notify; notify = null; fn(); }
  };
  const onError = (err) => {
    failed = err || new Error('socket');
    if (notify) { const fn = notify; notify = null; fn(); }
  };
  socket.on('data', onData);
  socket.on('end', onEnd);
  socket.on('error', onError);
  // Wait for a newer data, end, or error event. Bytes already in buf are not
  // a new event: headers() and bytes() call this only after judging them
  // incomplete, and resolving again would spin without reaching the socket.
  function waitForMore() {
    return new Promise((resolve) => {
      if (ended || failed) { resolve(); return; }
      notify = resolve;
    });
  }
  return {
    detach() {
      socket.off('data', onData);
      socket.off('end', onEnd);
      socket.off('error', onError);
      notify = null;
    },
    remainder() {
      const out = buf;
      buf = Buffer.alloc(0);
      return out;
    },
    async headers(limit) {
      for (;;) {
        if (failed) throw failed;
        const at = buf.indexOf('\r\n\r\n');
        if (at >= 0) {
          const head = buf.slice(0, at).toString('latin1');
          buf = buf.slice(at + 4);
          return head;
        }
        if (buf.length > limit) throw new Error('headers');
        if (ended) throw new Error('ended');
        await waitForMore();
      }
    },
    async bytes(count) {
      if (count === 0) return Buffer.alloc(0);
      for (;;) {
        if (failed) throw failed;
        if (buf.length >= count) {
          const body = buf.slice(0, count);
          buf = buf.slice(count);
          return body;
        }
        if (ended) throw new Error('ended');
        await waitForMore();
      }
    },
  };
}

function exchangeClose(upstream, gate, head, body) {
  return new Promise((resolve) => {
    if (gate.error || upstream.destroyed) { resolve(null); return; }
    const chunks = [];
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      resolve(value);
    };
    upstream.on('data', (chunk) => chunks.push(chunk));
    upstream.on('error', () => finish(null));
    upstream.on('end', () => finish(Buffer.concat(chunks)));
    try {
      upstream.write(Buffer.concat([head, body]));
      upstream.end();
    } catch (err) {
      finish(null);
    }
  });
}

// openTunnel writes 200 only after the fixture TCP connection succeeds, then
// carries the client's bytes unchanged. A refused dial never writes 200.
function openTunnel(socket, upstream, rest) {
  return new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      resolve();
    };
    const refuse = () => {
      if (settled) return;
      proxyStatus(socket, 403);
      destroyPeer(upstream);
      finish();
    };
    const onClientGone = () => {
      if (settled) return;
      destroyPeer(upstream);
      finish();
    };
    let started = false;
    const startTunnel = () => {
      if (settled || started) return;
      started = true;
      socket.removeListener('close', onClientGone);
      upstream.removeListener('error', refuse);
      upstream.on('error', () => { destroyPeer(socket); destroyPeer(upstream); });
      socket.on('error', () => { destroyPeer(socket); destroyPeer(upstream); });
      socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');
      if (rest.length) upstream.write(rest);
      socket.pipe(upstream);
      upstream.pipe(socket);
      socket.resume();
      upstream.once('close', finish);
      socket.once('close', finish);
    };
    socket.once('close', onClientGone);
    upstream.once('error', refuse);
    upstream.once('connect', startTunnel);
    if (upstream.readyState === 'open') startTunnel();
  });
}

async function serveProxyClient(socket, allowedOrigin, tracked) {
  trackProxyPeer(tracked, socket, 'socket');
  const pull = createPull(socket);
  let current = null;
  try {
    for (;;) {
      const head = await pull.headers(proxyHeaderLimit);
      const decision = proxyRequestDecision(head.split('\r\n', 1)[0], allowedOrigin, (endpoint) => {
        return trackProxyPeer(tracked, net.connect({ host: endpoint.host, port: Number(endpoint.port) }), 'upstream');
      });
      if (!decision.ok || !decision.socket) {
        proxyStatus(socket, 403);
        return;
      }
      current = decision.socket;
      if (decision.tunnel) {
        socket.pause();
        pull.detach();
        const rest = pull.remainder();
        await openTunnel(socket, current, rest);
        return;
      }
      const gate = { error: null };
      current.on('error', (err) => { gate.error = err || new Error('upstream'); });
      const length = requestContentLength(head);
      const rewritten = rewriteOriginRequest(head, decision);
      if (!length.ok || !rewritten) {
        destroyPeer(current);
        current = null;
        proxyStatus(socket, 403);
        return;
      }
      const body = await pull.bytes(length.length);
      const response = await exchangeClose(current, gate, Buffer.from(rewritten, 'latin1'), body);
      current = null;
      if (!response || !response.length) {
        proxyStatus(socket, 403);
        return;
      }
      socket.write(response);
    }
  } catch (err) {
    destroyPeer(current);
    destroyPeer(socket);
  }
}

// Chromium continues a fulfilled redirect outside page.route. This proxy is the
// gate for that later hop: only the exact fixture origin is dialed, and
// <-loopback stops Chromium from bypassing the proxy for 127.0.0.1. Each
// absolute-form request on a kept-alive connection is rewritten to origin-form
// before a new fixture exchange. CONNECT becomes a tunnel only after that
// fixed endpoint connects.
function startFixtureProxy(allowedOrigin) {
  const tracked = { sockets: new Set(), upstreams: new Set() };
  const server = net.createServer((socket) => {
    serveProxyClient(socket, allowedOrigin, tracked);
  });
  return new Promise((resolve, reject) => {
    const onError = (err) => { server.close(() => reject(err)); };
    server.once('error', onError);
    server.listen(0, '127.0.0.1', () => {
      server.removeListener('error', onError);
      server.on('error', () => {});
      resolve({
        url: 'http://127.0.0.1:' + server.address().port,
        close: () => closeTrackedProxy(server, tracked, proxyCloseDeadlineMs),
      });
    });
  });
}

function launchBrowser(proxyURL) {
  const { chromium } = require('playwright');
  return chromium.launch(fixtureBrowserLaunch(proxyURL));
}

async function releaseOwned(owned) {
  if (owned.browser && typeof owned.browser.close === 'function') {
    try { await owned.browser.close(); } catch (err) { /* still close the proxy and fixture */ }
  }
  if (owned.proxy && typeof owned.proxy.close === 'function') {
    try { await owned.proxy.close(); } catch (err) { /* still terminate the fixture */ }
  }
  if (owned.child && typeof owned.child.kill === 'function') {
    try { owned.child.kill('SIGTERM'); } catch (err) { /* acquired resources already closed */ }
  }
}

async function run(width, deps) {
  const openFixture = deps && deps.openFixture ? deps.openFixture : start;
  const openProxy = deps && deps.openProxy ? deps.openProxy : startFixtureProxy;
  const launch = deps && deps.launch ? deps.launch : launchBrowser;
  const drive = deps && deps.drive ? deps.drive : flows;
  let child = null;
  let proxy = null;
  let browser = null;
  try {
    const started = await openFixture();
    child = started && started.child ? started.child : null;
    const base = started && started.base;
    proxy = await openProxy(base);
    browser = await launch(proxy.url);
    await drive(browser, base, width);
  } catch (err) {
    const message = err && err.message ? err.message : String(err);
    check(width, 'flow run', false, message.split('\n')[0]);
  } finally {
    await releaseOwned({ browser, proxy, child });
  }
}

if (require.main === module) {
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
      console.log(`DIAGNOSTIC ${failed ? 'FAILED' : 'COMPLETE'}: ${failed} failed checks, ${notAccepted} refused posts; never acceptance.`);
      process.exit(failed ? 1 : 3);
    }
    if (notAccepted) console.log(`NOT ACCEPTED (#480): ${notAccepted} POST-flow submissions (send, launch, Asks answer) refused by the production origin check; history (GET) is not affected.`);
    process.exit(failed || notAccepted ? 1 : 0);
  })();
}

module.exports = { run, closeTrackedProxy, trackProxyPeer, startFixtureProxy, createPull, openTunnel };
