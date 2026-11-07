// No-network guards. The transport spy must not be called for a refused base,
// a wrong port, or an off-origin redirect. This file does not dial or browse.
const assert = require('assert');
const guard = require('./smoke-flows-guard.cjs');
const flows = require('./smoke-flows.cjs');

function spyTransport(calls) {
  return function (options, callback) {
    calls.push({ host: options.host, port: String(options.port), path: options.path, method: options.method });
    const req = {
      on() { return req; },
      write() {},
      end() {
        const res = { statusCode: 204, rawHeaders: [], on(event, fn) { if (event === 'end') fn(); return res; } };
        if (callback) callback(res);
      },
    };
    return req;
  };
}

async function rejectsBeforeDial(base, options, calls) {
  await assert.rejects(() => guard.localRequest(base, options || { method: 'GET', path: '/' }, spyTransport(calls)));
  assert.deepEqual(calls, []);
}

(async () => {
  const calls = [];
  await rejectsBeforeDial('http://example.invalid:7777', { method: 'GET', path: '/' }, calls);
  await rejectsBeforeDial('https://127.0.0.1:7777', { method: 'GET', path: '/' }, calls);
  await rejectsBeforeDial('http://127.0.0.1', { method: 'GET', path: '/' }, calls);
  await rejectsBeforeDial('http://127.0.0.1:0', { method: 'GET', path: '/' }, calls);
  await rejectsBeforeDial('http://user:pass@127.0.0.1:9', { method: 'GET', path: '/' }, calls);
  await rejectsBeforeDial('http://127.0.0.1:9/extra', { method: 'GET', path: '/' }, calls);
  await rejectsBeforeDial('http://127.0.0.1:9/?q=1', { method: 'GET', path: '/' }, calls);
  await rejectsBeforeDial('http://localhost:9', { method: 'GET', path: '/' }, calls);
  await rejectsBeforeDial('http://127.0.0.1:9', { method: 'GET', path: 'http://127.0.0.1:1/other' }, calls);
  await rejectsBeforeDial('http://127.0.0.1:9', { method: 'GET', path: 'http://example.invalid:9/other' }, calls);

  const allowed = await guard.localRequest('http://127.0.0.1:9', { method: 'GET', path: '/portal-localtest/fixture-calls' }, spyTransport(calls));
  assert.equal(allowed.status, 204);
  assert.deepEqual(calls, [{ host: '127.0.0.1', port: '9', path: '/portal-localtest/fixture-calls', method: 'GET' }]);

  const announced = guard.announcedFixtureBase('Portal local test instance: http://127.0.0.1:9/portal/projects/dev-env/environments/internal/sessions');
  assert.equal(announced.ok, true);
  assert.equal(announced.origin, 'http://127.0.0.1:9');
  assert.equal(guard.announcedFixtureBase('http://example.invalid:7777/portal').ok, false);
  assert.equal(guard.announcedFixtureBase('https://127.0.0.1:9/portal').ok, false);
  assert.equal(guard.parseFixtureBase('http://[::1]:9').ok, true);

  const followed = [];
  const transport = (target) => { followed.push(target); };
  const off = guard.applyFixtureResponse(302, 'http://example.invalid/leave', 'http://127.0.0.1:9/send', 'http://127.0.0.1:9', transport);
  assert.equal(off.action, 'refuse');
  assert.equal(off.status, 302);
  assert.equal(off.next, 'http://example.invalid');
  const wrongPort = guard.applyFixtureResponse(302, 'http://127.0.0.1:1/next', 'http://127.0.0.1:9/send', 'http://127.0.0.1:9', transport);
  assert.equal(wrongPort.action, 'refuse');
  assert.equal(wrongPort.next, 'http://127.0.0.1:1');
  const seeOther = guard.applyFixtureResponse(303, '/portal/projects/dev-env/environments/internal/sessions?session=fixture-uid-launch-1', 'http://127.0.0.1:9/launch', 'http://127.0.0.1:9', transport);
  assert.equal(seeOther.action, 'fulfill');
  assert.equal(seeOther.status, 303);
  assert.equal(seeOther.location, '/portal/projects/dev-env/environments/internal/sessions?session=fixture-uid-launch-1');
  const keepMethod = guard.applyFixtureResponse(307, 'http://127.0.0.1:9/again', 'http://127.0.0.1:9/send', 'http://127.0.0.1:9', transport);
  assert.equal(keepMethod.action, 'fulfill');
  assert.equal(keepMethod.status, 307);
  assert.equal(keepMethod.location, 'http://127.0.0.1:9/again');
  assert.deepEqual(followed, []);

  const origin = 'http://127.0.0.1:9';
  const documentURL = origin + '/portal/projects/dev-env/environments/internal/sessions?session=fixture-uid-coordinator';
  const workerURL = origin + guard.fixtureWorkerPath;
  const absent = guard.serviceWorkerClaim(documentURL, workerURL, origin, { api: 'absent', script: guard.fixtureWorkerSource, documentOrigin: origin });
  assert.equal(absent.label, 'source-only');
  assert.equal(absent.blocked, false);
  assert.equal(absent.reason, 'api-absent');
  const blank = guard.serviceWorkerClaim('about:blank', workerURL, origin, { api: 'present', result: 'blocked', active: false, script: guard.fixtureWorkerSource, documentOrigin: origin });
  assert.equal(blank.label, 'source-only');
  assert.equal(blank.blocked, false);
  const blocked = guard.serviceWorkerClaim(documentURL, workerURL, origin, { api: 'present', result: 'blocked', active: false, script: guard.fixtureWorkerSource, documentOrigin: origin });
  assert.equal(blocked.label, 'browser');
  assert.equal(blocked.proved, true);
  assert.equal(blocked.blocked, true);
  const active = guard.serviceWorkerClaim(documentURL, workerURL, origin, { api: 'present', result: 'active', active: true, script: guard.fixtureWorkerSource, documentOrigin: origin });
  assert.equal(active.blocked, false);
  assert.equal(active.reason, 'active');
  assert.equal(guard.fixtureContextOptions.serviceWorkers, 'block');
  assert.deepEqual(guard.fixtureBrowserLaunch(origin), { headless: true, proxy: { server: origin, bypass: '<-loopback>' } });

  const chainDial = (endpoint) => {
    chainDials.push(endpoint.host + ':' + endpoint.port + endpoint.path);
    return { fake: true };
  };
  const chainDials = [];
  const firstHop = guard.applyFixtureResponse(302, '/hop', origin + '/start', origin, transport);
  assert.equal(firstHop.action, 'fulfill');
  assert.equal(firstHop.status, 302);
  assert.equal(firstHop.location, '/hop');
  const foreign = guard.followBrowserRedirects(origin + '/start', 'GET', (url) => {
    assert.equal(url.includes('example.invalid'), false);
    if (url === origin + '/start') return { status: 302, location: '/hop' };
    assert.equal(url, origin + '/hop');
    return { status: 302, location: 'http://example.invalid/escape' };
  }, origin, chainDial);
  assert.equal(foreign.refused, 'http://example.invalid/escape');
  assert.deepEqual(foreign.attempts, ['127.0.0.1:9/start', '127.0.0.1:9/hop']);
  assert.deepEqual(chainDials, foreign.attempts);
  const wrongPortChain = guard.followBrowserRedirects(origin + '/start', 'GET', (url) => {
    assert.equal(url.includes(':1/'), false);
    if (url === origin + '/start') return { status: 302, location: '/hop' };
    return { status: 302, location: 'http://127.0.0.1:1/escape' };
  }, origin, chainDial);
  assert.equal(wrongPortChain.refused, 'http://127.0.0.1:1/escape');
  assert.equal(chainDials.some((item) => item.startsWith('127.0.0.1:1')), false);
  const launchChain = guard.followBrowserRedirects(origin + '/launch', 'POST', (url, method) => {
    if (url === origin + '/launch') {
      assert.equal(method, 'POST');
      return { status: 303, location: '/portal/projects/dev-env/environments/internal/sessions?session=fixture-uid-launch-1' };
    }
    assert.equal(method, 'GET');
    assert.equal(url, origin + '/portal/projects/dev-env/environments/internal/sessions?session=fixture-uid-launch-1');
    return { status: 200, location: '' };
  }, origin, chainDial);
  assert.equal(launchChain.finalURL, origin + '/portal/projects/dev-env/environments/internal/sessions?session=fixture-uid-launch-1');
  assert.equal(launchChain.status, 200);
  assert.equal(launchChain.method, 'GET');
  const keepChain = guard.followBrowserRedirects(origin + '/send', 'POST', (url, method) => {
    assert.equal(method, 'POST');
    if (url === origin + '/send') return { status: 307, location: origin + '/again' };
    assert.equal(url, origin + '/again');
    return { status: 200, location: '' };
  }, origin, chainDial);
  assert.equal(keepChain.finalURL, origin + '/again');
  assert.equal(keepChain.method, 'POST');
  let proxyDials = 0;
  const proxyDial = () => { proxyDials += 1; };
  assert.equal(guard.proxyRequestDecision('CONNECT example.invalid:443 HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('GET /hop HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('GET http://user:pass@127.0.0.1:9/escape HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(proxyDials, 0);
  assert.equal(guard.proxyRequestDecision('GET ' + origin + '/hop HTTP/1.1', origin, proxyDial).ok, true);
  assert.equal(proxyDials, 1);

  async function ownedRun(failAt) {
    const calls = { kill: [], proxyClose: 0, browserClose: 0, launch: 0, openedProxy: 0 };
    await flows.run('guard', {
      openFixture: async () => {
        if (failAt === 'fixture') throw new Error('fixture failed');
        return { child: { kill(sig) { calls.kill.push(sig); } }, base: origin };
      },
      openProxy: async (base) => {
        assert.equal(base, origin);
        calls.openedProxy += 1;
        if (failAt === 'proxy') throw new Error('proxy failed');
        return {
          url: origin,
          close() {
            calls.proxyClose += 1;
            if (failAt === 'proxyClose') throw new Error('proxy close failed');
            return Promise.resolve();
          },
        };
      },
      launch: async (url) => {
        assert.equal(url, origin);
        calls.launch += 1;
        if (failAt === 'launch') throw new Error('launch failed');
        return { close() { calls.browserClose += 1; if (failAt === 'browserClose') throw new Error('browser close failed'); } };
      },
      drive: async () => { if (failAt === 'drive') throw new Error('drive failed'); },
    });
    return calls;
  }
  const fixtureFail = await ownedRun('fixture');
  assert.deepEqual(fixtureFail.kill, []);
  assert.equal(fixtureFail.openedProxy, 0);
  assert.equal(fixtureFail.launch, 0);
  assert.equal(fixtureFail.proxyClose, 0);
  const proxyFail = await ownedRun('proxy');
  assert.deepEqual(proxyFail.kill, ['SIGTERM']);
  assert.equal(proxyFail.launch, 0);
  assert.equal(proxyFail.proxyClose, 0);
  const launchFail = await ownedRun('launch');
  assert.deepEqual(launchFail.kill, ['SIGTERM']);
  assert.equal(launchFail.proxyClose, 1);
  assert.equal(launchFail.browserClose, 0);
  const driveFail = await ownedRun('drive');
  assert.deepEqual(driveFail.kill, ['SIGTERM']);
  assert.equal(driveFail.proxyClose, 1);
  assert.equal(driveFail.browserClose, 1);
  const browserCloseFail = await ownedRun('browserClose');
  assert.equal(browserCloseFail.browserClose, 1);
  assert.equal(browserCloseFail.proxyClose, 1);
  assert.deepEqual(browserCloseFail.kill, ['SIGTERM']);
  const proxyCloseFail = await ownedRun('proxyClose');
  assert.equal(proxyCloseFail.proxyClose, 1);
  assert.deepEqual(proxyCloseFail.kill, ['SIGTERM']);
  const hung = { sockets: new Set(), upstreams: new Set() };
  const hungDestroyed = [];
  flows.trackProxyPeer(hung, { destroy() { hungDestroyed.push('client'); }, on() {} }, 'socket');
  const hungStart = Date.now();
  await flows.run('guard', {
    openFixture: async () => ({ child: { kill() {} }, base: origin }),
    openProxy: async () => ({ url: origin, close: () => flows.closeTrackedProxy({ close() {} }, hung, 40) }),
    launch: async () => { throw new Error('launch failed'); },
    drive: async () => {},
  });
  assert.ok(Date.now() - hungStart < 300);
  assert.deepEqual(hungDestroyed, ['client']);

  const tracked = { sockets: new Set(), upstreams: new Set() };
  const destroyed = [];
  let clientClosed;
  flows.trackProxyPeer(tracked, {
    destroy() { destroyed.push('client'); },
    on(event, fn) { if (event === 'close') clientClosed = fn; },
  }, 'socket');
  flows.trackProxyPeer(tracked, { destroy() { destroyed.push('up'); }, on() {} }, 'upstream');
  assert.equal(tracked.sockets.size, 1);
  clientClosed();
  assert.equal(tracked.sockets.size, 0);
  const stalled = Date.now();
  await flows.closeTrackedProxy({ close() {} }, tracked, 40);
  assert.ok(Date.now() - stalled < 300);
  assert.deepEqual(destroyed, ['up']);
  const immediate = { sockets: new Set(), upstreams: new Set() };
  let closed = false;
  const began = Date.now();
  await flows.closeTrackedProxy({ close(cb) { closed = true; cb(); } }, immediate, 500);
  assert.equal(closed, true);
  assert.ok(Date.now() - began < 100);
  console.log('smoke-flows guard tests passed: no dial, no browser');
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
