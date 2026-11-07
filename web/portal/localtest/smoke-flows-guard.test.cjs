// Guard spies do not dial or browse. The exchange proof opens loopback sockets
// only, for one fixture origin, and does not launch a browser.
const assert = require('assert');
const net = require('net');
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

function listenLoopback(server) {
  return new Promise((resolve, reject) => {
    const onError = (err) => reject(err);
    server.once('error', onError);
    server.listen(0, '127.0.0.1', () => {
      server.removeListener('error', onError);
      resolve(server.address().port);
    });
  });
}

function closeServer(server) {
  return new Promise((resolve) => {
    if (!server || !server.listening) { resolve(); return; }
    server.close(() => resolve());
  });
}

function onceConnect(socket) {
  return new Promise((resolve, reject) => {
    socket.once('connect', resolve);
    socket.once('error', reject);
  });
}

function readStatus(socket) {
  return new Promise((resolve, reject) => {
    let buf = Buffer.alloc(0);
    const timer = setTimeout(() => { socket.destroy(); reject(new Error('proxy status timeout')); }, 2000);
    const onData = (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      const at = buf.indexOf('\r\n\r\n');
      if (at < 0) {
        if (buf.length > 65536) {
          clearTimeout(timer);
          socket.destroy();
          reject(new Error('proxy status overflow'));
        }
        return;
      }
      clearTimeout(timer);
      socket.off('data', onData);
      resolve(buf.slice(0, at).toString('latin1').split('\r\n')[0]);
    };
    socket.on('data', onData);
    socket.on('error', (err) => { clearTimeout(timer); reject(err); });
    socket.on('end', () => {
      if (buf.indexOf('\r\n\r\n') >= 0) return;
      clearTimeout(timer);
      reject(new Error('proxy status closed: ' + buf.toString('latin1').slice(0, 120)));
    });
  });
}

function readHttpResponse(socket) {
  return new Promise((resolve, reject) => {
    let buf = Buffer.alloc(0);
    const timer = setTimeout(() => { socket.destroy(); reject(new Error('proxy response timeout')); }, 2000);
    const onData = (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      const at = buf.indexOf('\r\n\r\n');
      if (at < 0) return;
      const head = buf.slice(0, at).toString('latin1');
      const match = /^content-length:\s*([0-9]+)/im.exec(head);
      const length = match ? Number(match[1]) : 0;
      if (buf.length < at + 4 + length) return;
      clearTimeout(timer);
      socket.off('data', onData);
      resolve({ status: head.split('\r\n')[0], head, body: buf.slice(at + 4, at + 4 + length).toString('latin1') });
    };
    socket.on('data', onData);
    socket.on('error', (err) => { clearTimeout(timer); reject(err); });
  });
}

function sendClosed(port, payload) {
  return new Promise((resolve, reject) => {
    const socket = net.connect({ host: '127.0.0.1', port });
    const chunks = [];
    const timer = setTimeout(() => { socket.destroy(); reject(new Error('closed exchange timeout')); }, 2000);
    socket.on('data', (chunk) => chunks.push(chunk));
    socket.on('error', (err) => { clearTimeout(timer); reject(err); });
    socket.on('end', () => { clearTimeout(timer); resolve(Buffer.concat(chunks).toString('latin1')); });
    socket.on('connect', () => socket.write(payload));
  });
}

const { EventEmitter } = require('events');

function fakePeer() {
  const peer = new EventEmitter();
  peer.destroyed = false;
  peer.readyState = 'opening';
  peer.written = [];
  peer.write = (chunk) => { peer.written.push(Buffer.from(chunk).toString('latin1')); return true; };
  peer.end = (chunk) => { if (chunk !== undefined) peer.write(chunk); peer.ended = true; };
  peer.resume = () => {};
  peer.pipe = () => peer;
  peer.destroy = () => { peer.destroyed = true; };
  return peer;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function bound(promise, label, ms) {
  return Promise.race([
    promise,
    delay(ms).then(() => { throw new Error(label + ' still pending after ' + ms + 'ms'); }),
  ]);
}

// provePullAndTunnelSettle uses fake emitters only. A partial buffer must wait
// for a later event, and both tunnel failure paths must resolve with no 200.
async function provePullAndTunnelSettle() {
  let checks = 0;
  {
    const socket = fakePeer();
    const pull = flows.createPull(socket);
    const pending = pull.headers(1024);
    let timed = false;
    setTimeout(() => { timed = true; }, 10);
    socket.emit('data', Buffer.from('GET /'));
    setTimeout(() => socket.emit('data', Buffer.from('frag HTTP/1.1\r\n\r\n')), 10);
    await delay(30);
    assert.equal(timed, true);
    checks += 1;
    const head = await bound(pending, 'fragmented headers', 200);
    assert.equal(head, 'GET /frag HTTP/1.1');
    checks += 1;
  }
  {
    const socket = fakePeer();
    const pull = flows.createPull(socket);
    const pending = pull.bytes(4);
    let timed = false;
    setTimeout(() => { timed = true; }, 10);
    socket.emit('data', Buffer.from('ab'));
    setTimeout(() => socket.emit('data', Buffer.from('cd')), 10);
    await delay(30);
    assert.equal(timed, true);
    checks += 1;
    const body = await bound(pending, 'fragmented body', 200);
    assert.equal(body.toString('latin1'), 'abcd');
    checks += 1;
  }
  {
    const socket = fakePeer();
    const pull = flows.createPull(socket);
    const pending = pull.headers(1024);
    socket.emit('data', Buffer.from('GET /ready HTTP/1.1\r\n\r\nleftover'));
    assert.equal(await bound(pending, 'complete headers', 200), 'GET /ready HTTP/1.1');
    assert.equal((await pull.bytes(8)).toString('latin1'), 'leftover');
    checks += 2;
  }
  {
    const socket = fakePeer();
    const pull = flows.createPull(socket);
    const pending = pull.headers(1024);
    socket.emit('end');
    await assert.rejects(bound(pending, 'header end', 200), /ended/);
    checks += 1;
  }
  {
    const socket = fakePeer();
    const pull = flows.createPull(socket);
    const pending = pull.headers(1024);
    socket.emit('error', new Error('header failed'));
    await assert.rejects(bound(pending, 'header error', 200), /header failed/);
    checks += 1;
  }
  {
    const socket = fakePeer();
    const pull = flows.createPull(socket);
    const pending = pull.bytes(3);
    socket.emit('end');
    await assert.rejects(bound(pending, 'body end', 200), /ended/);
    checks += 1;
  }
  {
    const socket = fakePeer();
    const pull = flows.createPull(socket);
    const pending = pull.bytes(3);
    socket.emit('error', new Error('body failed'));
    await assert.rejects(bound(pending, 'body error', 200), /body failed/);
    checks += 1;
  }
  {
    const client = fakePeer();
    const upstream = fakePeer();
    const pending = flows.openTunnel(client, upstream, Buffer.from('early'));
    upstream.emit('error', new Error('refused'));
    await bound(pending, 'preconnect upstream failure', 50);
    assert.equal(client.written.join('').includes('200'), false);
    assert.ok(client.written.join('').includes('403'));
    assert.equal(upstream.destroyed, true);
    checks += 3;
  }
  {
    const client = fakePeer();
    const upstream = fakePeer();
    const pending = flows.openTunnel(client, upstream, Buffer.alloc(0));
    client.emit('close');
    await bound(pending, 'preconnect client close', 50);
    assert.equal(client.written.join('').includes('200'), false);
    assert.equal(upstream.destroyed, true);
    checks += 2;
  }
  {
    const client = fakePeer();
    const upstream = fakePeer();
    const pending = flows.openTunnel(client, upstream, Buffer.from('GET /tun HTTP/1.1\r\n\r\n'));
    await delay(15);
    assert.equal(client.written.join('').includes('200'), false);
    upstream.emit('connect');
    assert.ok(client.written.join('').includes('HTTP/1.1 200 Connection Established'));
    assert.ok(upstream.written.join('').includes('GET /tun HTTP/1.1'));
    upstream.emit('close');
    await bound(pending, 'tunnel connect', 50);
    checks += 3;
  }
  return checks;
}

function proveSubmissionLabels() {
  const sentence = 'Portal authentication unavailable or denied';
  const origin = guard.submissionPhase(403, sentence + '.\n');
  assert.equal(origin.originRefusal, true);
  assert.equal(origin.phase, 'origin');
  const unavailable = guard.submissionPhase(503, 'Local test instance (fixture data, not production).\n' + sentence + '.\n');
  assert.equal(unavailable.ok, false);
  assert.equal(unavailable.originRefusal, false);
  assert.equal(unavailable.phase, 'unavailable');
  assert.equal(unavailable.status, 503);
  const textOnly = guard.submissionPhase(undefined, sentence);
  assert.equal(textOnly.originRefusal, false);
  assert.equal(textOnly.phase, 'unavailable');
  const accepted = guard.submissionPhase(200, 'Recorded answer');
  assert.equal(accepted.ok, true);
  assert.equal(accepted.originRefusal, false);
  return 4;
}

async function proveHeaderBound() {
  const socket = fakePeer();
  const pull = flows.createPull(socket);
  const oversized = 'GET / HTTP/1.1\r\nX-Pad: ' + 'A'.repeat(70000) + '\r\n\r\nbody';
  const pending = pull.headers(65536);
  socket.emit('data', Buffer.from(oversized));
  await assert.rejects(bound(pending, 'oversized complete headers', 200), /headers/);
  const small = fakePeer();
  const smallPull = flows.createPull(small);
  const body = 'B'.repeat(70000);
  const pendingHead = smallPull.headers(65536);
  small.emit('data', Buffer.from('GET /ready HTTP/1.1\r\nContent-Length: ' + body.length + '\r\n\r\n' + body));
  assert.equal(await bound(pendingHead, 'small header beside large body', 200), 'GET /ready HTTP/1.1\r\nContent-Length: ' + body.length);
  assert.equal((await smallPull.bytes(body.length)).toString('latin1'), body);
}

// proveDelayedGetNoEarlyFIN is the bodyless redirected GET: the fixture waits,
// and an early FIN is answered 503. The proxy must keep the write side open
// until that delayed response, while still sending Connection: close.
async function proveDelayedGetNoEarlyFIN() {
  let earlyFIN = false;
  let responded = false;
  let upstreamHead = '';
  const upstream = net.createServer((socket) => {
    let buf = Buffer.alloc(0);
    socket.on('error', () => {});
    socket.on('end', () => { if (!responded) earlyFIN = true; });
    socket.on('data', (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      const at = buf.indexOf('\r\n\r\n');
      if (responded || at < 0) return;
      upstreamHead = buf.slice(0, at).toString('latin1');
      setTimeout(() => {
        const status = earlyFIN ? 503 : 200;
        const payload = earlyFIN ? '' : 'ready';
        responded = true;
        socket.end('HTTP/1.1 ' + status + (status === 200 ? ' OK' : ' Service Unavailable') + '\r\nContent-Length: ' + payload.length + '\r\nConnection: close\r\n\r\n' + payload);
      }, 150);
    });
  });
  const port = await listenLoopback(upstream);
  const origin = 'http://127.0.0.1:' + port;
  const proxy = await flows.startFixtureProxy(origin);
  const proxyPort = Number(new URL(proxy.url).port);
  const client = net.connect({ host: '127.0.0.1', port: proxyPort });
  client.on('error', () => {});
  try {
    await onceConnect(client);
    client.write('GET ' + origin + '/ready HTTP/1.1\r\nHost: 127.0.0.1:' + port + '\r\nConnection: keep-alive\r\n\r\n');
    const response = await readHttpResponse(client);
    assert.equal(earlyFIN, false);
    assert.equal(response.status, 'HTTP/1.1 200 OK');
    assert.equal(response.body, 'ready');
    const raw = response.head + '\r\n\r\n';
    assert.equal(raw.includes('503'), false);
    assert.match(upstreamHead, /^GET \/ready HTTP\/1\.1/);
    assert.ok(upstreamHead.split('\r\n').includes('Connection: close'));
    assert.equal(upstreamHead.includes('http://'), false);
  } finally {
    client.destroy();
    await proxy.close();
    await closeServer(upstream);
  }
}

// proveProxyReuseAndConnect dials only the allowed fixture port. A second
// absolute-form request on the same socket must arrive as origin-form, and a
// CONNECT tunnel answers 200 only after that endpoint accepts.
async function proveProxyReuseAndConnect() {
  const received = [];
  let upstreamConnections = 0;
  const upstream = net.createServer((socket) => {
    upstreamConnections += 1;
    const chunks = [];
    socket.on('error', () => {});
    socket.on('data', (chunk) => {
      chunks.push(chunk);
      const buf = Buffer.concat(chunks);
      const at = buf.indexOf('\r\n\r\n');
      if (at < 0 || socket.handled) return;
      const head = buf.slice(0, at).toString('latin1');
      const lengthMatch = /^content-length:\s*([0-9]+)/im.exec(head);
      const length = lengthMatch ? Number(lengthMatch[1]) : 0;
      if (buf.length < at + 4 + length) return;
      socket.handled = true;
      const start = head.split('\r\n')[0];
      const method = start.split(' ')[0];
      const target = start.split(' ')[1];
      const body = buf.slice(at + 4, at + 4 + length).toString('latin1');
      received.push({ method, target, head, body });
      if (method === 'POST' && target === '/launch') {
        socket.end('HTTP/1.1 303 See Other\r\nLocation: /next\r\nContent-Length: 0\r\nConnection: close\r\n\r\n');
      } else if (method === 'GET' && target === '/next') {
        socket.end('HTTP/1.1 200 OK\r\nContent-Length: 4\r\nConnection: close\r\n\r\nnext');
      } else if (method === 'GET' && target === '/tunnel') {
        socket.end('HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\ntunnel');
      } else {
        socket.end('HTTP/1.1 500 Error\r\nContent-Length: 0\r\nConnection: close\r\n\r\n');
      }
    });
  });
  let decoyConnections = 0;
  const decoy = net.createServer(() => { decoyConnections += 1; });
  const port = await listenLoopback(upstream);
  const decoyPort = await listenLoopback(decoy);
  const origin = 'http://127.0.0.1:' + port;
  const proxy = await flows.startFixtureProxy(origin);
  const proxyPort = Number(new URL(proxy.url).port);
  const closedListener = net.createServer();
  const closedPort = await listenLoopback(closedListener);
  const closedProxy = await flows.startFixtureProxy('http://127.0.0.1:' + closedPort);
  const closedProxyPort = Number(new URL(closedProxy.url).port);
  let client = null;
  let tunnel = null;
  try {
    client = net.connect({ host: '127.0.0.1', port: proxyPort });
    client.on('error', () => {});
    await onceConnect(client);
    const post = 'POST ' + origin + '/launch HTTP/1.1\r\nHost: evil.example\r\nContent-Length: 3\r\nConnection: keep-alive\r\n\r\na=b';
    client.write(post);
    const redirected = await readHttpResponse(client);
    assert.equal(redirected.status, 'HTTP/1.1 303 See Other');
    assert.match(redirected.head, /^Location: \/next/im);
    const follow = 'GET ' + origin + '/next HTTP/1.1\r\nHost: ' + origin.slice('http://'.length) + '\r\nConnection: keep-alive\r\n\r\n';
    client.write(follow);
    const landed = await readHttpResponse(client);
    assert.equal(landed.status, 'HTTP/1.1 200 OK');
    assert.equal(landed.body, 'next');
    assert.equal(received.length, 2);
    assert.equal(received[0].method, 'POST');
    assert.equal(received[0].target, '/launch');
    assert.equal(received[0].body, 'a=b');
    assert.ok(received[0].head.split('\r\n').includes('Host: 127.0.0.1:' + port));
    assert.ok(received[0].head.split('\r\n').includes('Connection: close'));
    assert.equal(received[0].head.includes('evil.example'), false);
    assert.equal(received[0].head.includes('http://'), false);
    assert.equal(received[1].method, 'GET');
    assert.equal(received[1].target, '/next');
    assert.equal(received[1].head.includes('http://'), false);
    assert.equal(upstreamConnections, 2);

    const beforeForbidden = received.length;
    const beforeConnections = upstreamConnections;
    const foreign = await sendClosed(proxyPort, 'CONNECT example.invalid:443 HTTP/1.1\r\nHost: example.invalid:443\r\n\r\n');
    const wrongPort = await sendClosed(proxyPort, 'CONNECT 127.0.0.1:' + decoyPort + ' HTTP/1.1\r\nHost: 127.0.0.1:' + decoyPort + '\r\n\r\n');
    const credentials = await sendClosed(proxyPort, 'CONNECT user:pass@127.0.0.1:' + port + ' HTTP/1.1\r\n\r\n');
    const malformed = await sendClosed(proxyPort, 'CONNECT 127.0.0.1 HTTP/1.1\r\n\r\n');
    assert.match(foreign, /^HTTP\/1\.1 403\r\n/);
    assert.match(wrongPort, /^HTTP\/1\.1 403\r\n/);
    assert.match(credentials, /^HTTP\/1\.1 403\r\n/);
    assert.match(malformed, /^HTTP\/1\.1 403\r\n/);
    assert.equal(foreign.includes('200'), false);
    assert.equal(decoyConnections, 0);
    assert.equal(upstreamConnections, beforeConnections);
    assert.equal(received.length, beforeForbidden);

    await closeServer(closedListener);
    const refusedTunnel = await sendClosed(closedProxyPort, 'CONNECT 127.0.0.1:' + closedPort + ' HTTP/1.1\r\nHost: 127.0.0.1:' + closedPort + '\r\n\r\n');
    assert.match(refusedTunnel, /^HTTP\/1\.1 403\r\n/);
    assert.equal(/HTTP\/1\.[01] 200/.test(refusedTunnel), false);

    tunnel = net.connect({ host: '127.0.0.1', port: proxyPort });
    tunnel.on('error', () => {});
    await onceConnect(tunnel);
    tunnel.write('CONNECT 127.0.0.1:' + port + ' HTTP/1.1\r\nHost: 127.0.0.1:' + port + '\r\n\r\n');
    const established = await readStatus(tunnel);
    assert.equal(established, 'HTTP/1.1 200 Connection Established');
    tunnel.write('GET /tunnel HTTP/1.1\r\nHost: 127.0.0.1:' + port + '\r\nConnection: close\r\n\r\n');
    const tunneled = await readHttpResponse(tunnel);
    assert.equal(tunneled.status, 'HTTP/1.1 200 OK');
    assert.equal(tunneled.body, 'tunnel');
    assert.equal(received.length, beforeForbidden + 1);
    assert.equal(received[beforeForbidden].method, 'GET');
    assert.equal(received[beforeForbidden].target, '/tunnel');
    assert.equal(upstreamConnections, beforeConnections + 1);
    assert.equal(decoyConnections, 0);
  } finally {
    if (client) client.destroy();
    if (tunnel) tunnel.destroy();
    await proxy.close();
    await closedProxy.close();
    await closeServer(upstream);
    await closeServer(decoy);
    await closeServer(closedListener);
  }
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
  const proxyEndpoints = [];
  const proxyDial = (endpoint) => {
    proxyDials += 1;
    proxyEndpoints.push(endpoint.host + ':' + endpoint.port + endpoint.path);
  };
  assert.equal(guard.proxyRequestDecision('CONNECT example.invalid:443 HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('CONNECT 127.0.0.1:1 HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('CONNECT user:pass@127.0.0.1:9 HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('CONNECT 127.0.0.1 HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('CONNECT http://127.0.0.1:9/ HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('CONNECT [::1]:9 HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('GET /hop HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(guard.proxyRequestDecision('GET http://user:pass@127.0.0.1:9/escape HTTP/1.1', origin, proxyDial).ok, false);
  assert.equal(proxyDials, 0);
  assert.deepEqual(proxyEndpoints, []);
  assert.equal(guard.proxyRequestDecision('GET ' + origin + '/hop HTTP/1.1', origin, proxyDial).ok, true);
  const allowedConnect = guard.proxyRequestDecision('CONNECT 127.0.0.1:9 HTTP/1.1', origin, proxyDial);
  assert.equal(allowedConnect.ok, true);
  assert.equal(allowedConnect.tunnel, true);
  assert.equal(allowedConnect.host, '127.0.0.1');
  assert.equal(allowedConnect.port, '9');
  const allowedV6 = guard.proxyRequestDecision('CONNECT [::1]:9 HTTP/1.1', 'http://[::1]:9', proxyDial);
  assert.equal(allowedV6.ok, true);
  assert.equal(allowedV6.tunnel, true);
  assert.equal(allowedV6.host, '::1');
  assert.equal(allowedV6.port, '9');
  assert.equal(proxyDials, 3);
  assert.deepEqual(proxyEndpoints, ['127.0.0.1:9/hop', '127.0.0.1:9', '::1:9']);

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
  const protocolChecks = await provePullAndTunnelSettle();
  console.log('protocol settle tests passed: ' + protocolChecks + ' checks, no socket');
  const labelChecks = proveSubmissionLabels();
  console.log('submission label tests passed: ' + labelChecks + ' checks, no socket');
  await proveHeaderBound();
  console.log('header bound tests passed: oversized complete header rejected, large body kept');
  await proveDelayedGetNoEarlyFIN();
  console.log('delayed bodyless GET passed: no early FIN, HTTP 200, origin-form, Connection: close');
  await proveProxyReuseAndConnect();
  console.log('smoke-flows guard tests passed: no forbidden dial, no browser');
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
