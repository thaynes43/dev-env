// No-network guards for the portal flow smoke. This file does not open a
// socket, launch a browser, or follow a redirect.
const http = require('http');

function sameFixtureOrigin(raw, allowedOrigin) {
  let url;
  try { url = new URL(raw); } catch (err) { return false; }
  return url.origin === allowedOrigin;
}

function redirectOrigin(location, requestURL) {
  if (!location) return '';
  try { return new URL(location, requestURL).origin; } catch (err) { return ''; }
}

// parseFixtureBase accepts only an http origin with a numeric loopback host
// and an explicit usable port. Credentials, a path, a query, and a hash are
// refused before any socket is opened.
function parseFixtureBase(raw) {
  let url;
  try { url = new URL(raw); } catch (err) { return { ok: false, reason: 'unparsed' }; }
  if (url.protocol !== 'http:') return { ok: false, reason: 'protocol' };
  if (url.username || url.password) return { ok: false, reason: 'credentials' };
  if (url.pathname !== '/' && url.pathname !== '') return { ok: false, reason: 'path' };
  if (url.search || url.hash) return { ok: false, reason: 'query' };
  const host = url.hostname === '[::1]' || url.hostname === '::1' ? '::1' : url.hostname;
  if (host !== '127.0.0.1' && host !== '::1') return { ok: false, reason: 'host' };
  if (!/^[1-9][0-9]*$/.test(url.port)) return { ok: false, reason: 'port' };
  const port = Number(url.port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) return { ok: false, reason: 'port' };
  const origin = url.origin;
  const expect = host === '::1' ? 'http://[::1]:' + String(port) : 'http://127.0.0.1:' + String(port);
  if (origin !== expect) return { ok: false, reason: 'origin' };
  return { ok: true, origin, hostname: host, port: String(port) };
}

function announcedFixtureBase(line) {
  const at = String(line || '').indexOf('http://');
  if (at < 0) return { ok: false, reason: 'missing' };
  let url;
  try { url = new URL(String(line).slice(at).trim().split(/\s/)[0]); } catch (err) { return { ok: false, reason: 'unparsed' }; }
  if (url.username || url.password) return { ok: false, reason: 'credentials' };
  if (url.search || url.hash) return { ok: false, reason: 'query' };
  if (url.protocol !== 'http:') return { ok: false, reason: 'protocol' };
  if (url.hostname !== '127.0.0.1' && url.hostname !== '::1' && url.hostname !== '[::1]') return { ok: false, reason: 'host' };
  if (!/^[1-9][0-9]*$/.test(url.port)) return { ok: false, reason: 'port' };
  return parseFixtureBase(url.origin);
}

const fixtureWorkerPath = '/portal-localtest-sw.js';
const fixtureWorkerSource = "self.addEventListener('install', function () {});\n";
const fixtureContextOptions = { serviceWorkers: 'block' };

function fixtureBrowserLaunch(proxyURL) {
  return { headless: true, proxy: { server: proxyURL, bypass: '<-loopback>' } };
}

// applyFixtureResponse never asks transport to fetch a Location. A same-origin
// redirect is fulfilled unchanged so the browser applies 302/303/307 method
// rules on a new request. Any other redirect is refused.
function applyFixtureResponse(status, location, requestURL, allowedOrigin, transport) {
  if (typeof transport !== 'function') throw new Error('redirect transport required');
  const redirect = status >= 300 && status < 400;
  const next = redirect ? redirectOrigin(location, requestURL) : '';
  if (redirect && next !== allowedOrigin) return { action: 'refuse', status, location, next };
  return { action: 'fulfill', status, location, next };
}

// serviceWorkerClaim is a browser proof only when registration was attempted
// from a fixture-origin document for the fixed worker script. An absent API
// or an about:blank document stays source-only and is not a block.
function serviceWorkerClaim(documentURL, workerURL, allowedOrigin, registration) {
  let doc;
  try { doc = new URL(documentURL); } catch (err) { return { proved: false, blocked: false, label: 'source-only', reason: 'document' }; }
  const docBase = parseFixtureBase(doc.origin);
  if (!docBase.ok || doc.origin !== allowedOrigin || doc.protocol === 'about:') return { proved: false, blocked: false, label: 'source-only', reason: 'document' };
  let worker;
  try { worker = new URL(workerURL); } catch (err) { return { proved: false, blocked: false, label: 'source-only', reason: 'worker' }; }
  if (worker.origin !== allowedOrigin || worker.pathname !== fixtureWorkerPath) return { proved: false, blocked: false, label: 'source-only', reason: 'worker-origin' };
  if (!registration || registration.api === 'absent' || registration.script !== fixtureWorkerSource || registration.documentOrigin !== allowedOrigin) {
    return { proved: false, blocked: false, label: 'source-only', reason: registration && registration.api === 'absent' ? 'api-absent' : 'unproved' };
  }
  if (registration.active) return { proved: true, blocked: false, label: 'browser', reason: 'active' };
  if (registration.result === 'blocked') return { proved: true, blocked: true, label: 'browser', reason: 'blocked' };
  return { proved: false, blocked: false, label: 'source-only', reason: 'unproved' };
}

// localRequest dials only a base that parseFixtureBase has already accepted.
// Host and Origin headers do not choose the socket. transport defaults to
// node:http and is called only after that check.
function localRequest(base, options, transport) {
  const parsed = parseFixtureBase(base);
  if (!parsed.ok) return Promise.reject(new Error('refused fixture base: ' + parsed.reason));
  const dial = transport || http.request;
  if (!options || typeof options.path !== 'string' || !options.path.startsWith('/') || options.path.startsWith('//')) return Promise.reject(new Error('refused path'));
  let target;
  try { target = new URL(options.path, parsed.origin); } catch (err) { return Promise.reject(new Error('refused path')); }
  if (target.origin !== parsed.origin || target.username || target.password || target.protocol !== 'http:') return Promise.reject(new Error('refused target'));
  return new Promise((resolve, reject) => {
    const req = dial({
      host: parsed.hostname, port: parsed.port, method: options.method, path: target.pathname + target.search,
      headers: Object.assign({ Host: parsed.hostname + ':' + parsed.port, Connection: 'close' }, options.headers || {}),
    }, (res) => {
      const chunks = [];
      res.on('data', (chunk) => chunks.push(chunk));
      res.on('end', () => resolve({ status: res.statusCode, rawHeaders: res.rawHeaders, body: Buffer.concat(chunks).toString('utf8') }));
    });
    req.on('error', reject);
    if (options.body) req.write(options.body);
    req.end();
  });
}

// connectFixtureTarget is the browser redirect-chain gate. Chromium follows a
// fulfilled same-origin redirect outside page.route, so every later hop is
// accepted or refused here, before dial. The dial callback is not used for a
// foreign host, a wrong port, or credentials.
function connectFixtureTarget(target, allowedOrigin, dial) {
  if (typeof dial !== 'function') throw new Error('dial required');
  const base = parseFixtureBase(allowedOrigin);
  if (!base.ok) return { ok: false, reason: base.reason };
  let url;
  try { url = new URL(target); } catch (err) { return { ok: false, reason: 'unparsed' }; }
  if (url.username || url.password) return { ok: false, reason: 'credentials' };
  if (url.protocol !== 'http:' || url.origin !== base.origin || url.port !== base.port) return { ok: false, reason: 'origin' };
  const endpoint = { host: base.hostname, port: base.port, path: url.pathname + url.search };
  return { ok: true, host: endpoint.host, port: endpoint.port, path: endpoint.path, socket: dial(endpoint) };
}

// proxyRequestDecision is what the loopback proxy calls for a browser request
// line, including a hop Chromium continued from a redirect.
function proxyRequestDecision(startLine, allowedOrigin, dial) {
  const parts = String(startLine || '').split(' ');
  const method = parts[0] || '';
  const target = parts[1] || '';
  if (method === 'CONNECT' || !target.startsWith('http://')) return { ok: false, reason: 'form' };
  return connectFixtureTarget(target, allowedOrigin, dial);
}

// followBrowserRedirects applies browser 301/302/303/307 rules and gates every
// hop, including the hop after a fulfilled same-origin redirect, through
// proxyRequestDecision. It does not rewrite Location or the final URL.
function followBrowserRedirects(startURL, method, hop, allowedOrigin, dial) {
  let url = startURL;
  let current = method;
  const attempts = [];
  for (let i = 0; i < 8; i++) {
    const decision = proxyRequestDecision(current + ' ' + url + ' HTTP/1.1', allowedOrigin, (endpoint) => {
      attempts.push(endpoint.host + ':' + endpoint.port + endpoint.path);
      return dial(endpoint);
    });
    if (!decision.ok) return { refused: url, reason: decision.reason, attempts, method: current };
    const response = hop(url, current);
    if (!(response.status >= 300 && response.status < 400)) return { finalURL: url, status: response.status, method: current, attempts };
    if (response.status === 301 || response.status === 302 || response.status === 303) current = 'GET';
    url = new URL(response.location, url).href;
  }
  return { refused: url, reason: 'limit', attempts, method: current };
}

module.exports = {
  sameFixtureOrigin, redirectOrigin, parseFixtureBase, announcedFixtureBase,
  fixtureWorkerPath, fixtureWorkerSource, fixtureContextOptions, fixtureBrowserLaunch,
  applyFixtureResponse, serviceWorkerClaim, localRequest,
  connectFixtureTarget, proxyRequestDecision, followBrowserRedirects,
};
