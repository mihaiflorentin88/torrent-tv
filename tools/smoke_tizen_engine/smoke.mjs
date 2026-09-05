#!/usr/bin/env node
/**
 * Headless old-engine boot smoke for the Tizen client (ticket #84, parent #79).
 *
 * Pinned engine: `selenoid/chrome:63.0` — Google Chrome 63.0.3239.84
 * (verified with `google-chrome --version` inside the image).
 * Selenoid (Aerokube) publishes historical official-Chrome images from a
 * reputable, widely used project; tag `63.0` is still pullable from Docker Hub
 * and is the oldest reliably obtainable engine at the Tizen 5.0 floor
 * (`clients/tv/vite.config.ts` calls that floor "Tizen 5.0-era Chromium 63").
 * The pin defines the guarantee's ceiling: engines older than Chrome 63 are
 * outside this smoke's claim.
 *
 * Three cases, one Make target (`make smoke-tizen-engine`):
 *   1. clean boot      — the real clients/tv/dist boots with zero uncaught
 *                        page errors and zero console errors (warnings are
 *                        tolerated), and the startup handoff completes:
 *                        #startup is removed from the DOM by
 *                        window.FileListBoot.ready() (clients/tv/startup.js),
 *                        which the app bundle calls right after the first
 *                        successful Preact render (clients/tv/src/main.tsx).
 *   2. injected error  — after boot, an async uncaught error is thrown via
 *                        Runtime.evaluate; the #fatal-error panel (ticket #80)
 *                        must exist, be visible, carry role="alert", and its
 *                        best-effort diagnostics POST to
 *                        /api/v1/diagnostics/client must be recorded.
 *   3. broken bundle   — a fixture copy of dist with a syntax error appended to
 *                        app.js makes THIS script exit non-zero (code 3) with a
 *                        clear message; the Make target fails unless the
 *                        fixture is rejected.
 *
 * Exit codes: 0 all requested cases passed; 1 infrastructure failure; 2 the
 * broken fixture booted or stayed inconclusive (harness failed to detect);
 * 3 broken fixture rejected (the expected outcome of case 3).
 *
 * Zero npm dependencies: Node >= 22 (native fetch/WebSocket) drives raw CDP;
 * a tiny node:http server serves dist plus the $WEBAPIS/webapis/webapis.js
 * stub (dist/index.html references it; it cannot resolve from disk) and
 * records diagnostics POSTs. Chrome runs with --network host so no published
 * ports are needed.
 */
import { spawnSync } from 'node:child_process';
import http from 'node:http';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const DEFAULT_IMAGE = 'selenoid/chrome:63.0'; // Google Chrome 63.0.3239.84 (verified via `--version` inside the image)
const READY_TIMEOUT_MS = 30_000;
const MILESTONE_TIMEOUT_MS = 30_000;
const BROKEN_DETECT_TIMEOUT_MS = 20_000;
const DIAGNOSTICS_TIMEOUT_MS = 5_000;
const POLL_INTERVAL_MS = 250;

const MIME = {
 '.html': 'text/html; charset=utf-8',
 '.js': 'text/javascript; charset=utf-8',
 '.css': 'text/css; charset=utf-8',
 '.png': 'image/png',
 '.svg': 'image/svg+xml',
 '.json': 'application/json',
 '.ico': 'image/x-icon',
};

// Boot only requires window.webapis to exist; these members mirror the subset
// the client uses (clients/tv/src/discovery.ts network, player avplay).
const WEBAPIS_STUB = `/* Served by tools/smoke_tizen_engine for dist/index.html's $WEBAPIS script tag. */
window.webapis = {
  avplay: {
    open: function () {}, close: function () {}, stop: function () {},
    prepareAsync: function () {}, play: function () {}, seekTo: function () {},
    setListener: function () {}, setDisplayArea: function () {},
    setSelectTrack: function () {}, setSilentSubtitle: function () {},
    setSubtitlePosition: function () {},
    getDuration: function () { return 0; },
    getCurrentTime: function () { return 0; },
    getTotalTrackInfo: function () { return []; }
  },
  network: {
    getIp: function () { return '127.0.0.1'; },
    getSubnetMask: function () { return '255.255.255.0'; }
  }
};
`;

const BROKEN_FIXTURE_SUFFIX = '\n/* smoke-tizen-engine broken-bundle fixture */\nfunction flsSmokeBrokenFixture(]{return 1}\n';

/** A case outcome that must fail the run with a specific exit code after cleanup. */
class SmokeFailure extends Error {
 constructor(message, code = 1) {
  super(message);
  this.code = code;
 }
}

function parseArgs(argv) {
 const state = { cases: ['clean', 'fatal'], image: DEFAULT_IMAGE, dist: 'clients/tv/dist' };
 for (let i = 0; i < argv.length; i++) {
  const name = argv[i];
  const value = argv[i + 1];
  if (name === '--cases' && value) {
   state.cases = value.split(',').map(item => item.trim()).filter(Boolean);
   i++;
  } else if (name === '--image' && value) {
   state.image = value;
   i++;
  } else if (name === '--dist' && value) {
   state.dist = value;
   i++;
  } else {
   throw new SmokeFailure(`unknown or incomplete argument: ${name}`);
  }
 }
 const known = new Set(['clean', 'fatal', 'broken']);
 for (const item of state.cases) {
  if (!known.has(item)) throw new SmokeFailure(`unknown case "${item}" (known: clean, fatal, broken)`);
 }
 if (state.cases.length === 0) throw new SmokeFailure('no cases requested');
 if (state.cases.includes('broken') && state.cases.length !== 1) {
  throw new SmokeFailure('the broken fixture must run in its own invocation: --cases broken');
 }
 return state;
}

function freePort() {
 return new Promise((resolve, reject) => {
  const probe = http.createServer();
  probe.once('error', reject);
  probe.listen(0, '127.0.0.1', () => {
   const port = probe.address().port;
   probe.close(() => resolve(port));
  });
 });
}

function createServer(state, root) {
 state.diagnosticsPosts = [];
 return http.createServer((req, res) => {
  const pathname = decodeURIComponent(new URL(req.url, 'http://127.0.0.1').pathname);
  if (pathname === '/$WEBAPIS/webapis/webapis.js') {
   res.writeHead(200, { 'Content-Type': MIME['.js'] });
   res.end(WEBAPIS_STUB);
   return;
  }
  if (pathname === '/favicon.ico') {
   res.writeHead(204);
   res.end();
   return;
  }
  if (req.method === 'POST' && pathname === '/api/v1/diagnostics/client') {
   let body = '';
   req.on('data', chunk => { body += chunk; });
   req.on('end', () => {
    try { state.diagnosticsPosts.push(JSON.parse(body)); } catch { state.diagnosticsPosts.push({ raw: body }); }
    res.writeHead(200, { 'Content-Type': MIME['.json'] });
    res.end('{}');
   });
   return;
  }
  if (req.method !== 'GET' && req.method !== 'HEAD') {
   res.writeHead(405).end();
   return;
  }
  const rootDir = path.resolve(root);
  const file = path.resolve(rootDir, `.${pathname}`);
  if (file !== rootDir && !file.startsWith(rootDir + path.sep)) {
   res.writeHead(403).end();
   return;
  }
  fs.readFile(file, (error, data) => {
   if (error) {
    res.writeHead(404).end('not found');
    return;
   }
   res.writeHead(200, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream' });
   res.end(req.method === 'HEAD' ? undefined : data);
  });
 });
}

function startChrome(state, cdpPort) {
 const name = `fls-smoke-tizen-engine-${process.pid}-${Date.now()}`;
 const result = spawnSync('docker', [
  'run', '--rm', '-d', '--name', name, '--network', 'host',
  '--entrypoint', '/usr/bin/google-chrome', state.image,
  '--headless', '--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage',
  '--remote-debugging-address=127.0.0.1', `--remote-debugging-port=${cdpPort}`,
  '--user-data-dir=/tmp/fls-smoke-chrome-profile', '--no-first-run', '--no-default-browser-check',
  '--disable-background-networking', '--disable-component-update', '--mute-audio', '--hide-scrollbars',
  '--window-size=1920,1080',
 ], { encoding: 'utf8' });
 if (result.error || result.status !== 0) {
  throw new SmokeFailure(`docker run ${state.image} failed: ${result.stderr || result.stdout || result.error}`);
 }
 state.container = name;
}

function cleanup(state) {
 if (state.container) {
  spawnSync('docker', ['rm', '-f', state.container], { encoding: 'utf8' });
  state.container = null;
 }
 if (state.server) {
  state.server.close();
  state.server = null;
 }
 if (state.fixtureDir) {
  fs.rmSync(state.fixtureDir, { recursive: true, force: true });
  state.fixtureDir = null;
 }
}

async function waitCDP(cdpPort, image) {
 const deadline = Date.now() + READY_TIMEOUT_MS;
 let lastError = '';
 while (Date.now() < deadline) {
  try {
   const response = await fetch(`http://127.0.0.1:${cdpPort}/json/version`);
   if (response.ok) {
    const info = await response.json();
    return { browser: info.Browser || 'unknown' };
   }
   lastError = `HTTP ${response.status}`;
  } catch (error) {
   lastError = String(error.cause || error.message || error);
  }
  await delay(300);
 }
 throw new SmokeFailure(`Chrome CDP on 127.0.0.1:${cdpPort} did not become ready within ${READY_TIMEOUT_MS / 1000}s (${lastError}) for image ${image}`);
}

class CDP {
 constructor(ws) {
  this.ws = ws;
  this.nextId = 1;
  this.pending = new Map();
  this.listeners = new Map();
  ws.addEventListener('message', event => {
   const message = JSON.parse(event.data);
   if (message.id && this.pending.has(message.id)) {
    const { resolve, reject } = this.pending.get(message.id);
    this.pending.delete(message.id);
    if (message.error) reject(new Error(`${message.error.message || 'CDP error'}`));
    else resolve(message.result);
   } else if (message.method) {
    for (const handler of this.listeners.get(message.method) || []) handler(message.params);
   }
  });
  ws.addEventListener('close', () => {
   for (const { reject } of this.pending.values()) reject(new Error('CDP websocket closed'));
   this.pending.clear();
  });
 }

 static connect(url) {
  return new Promise((resolve, reject) => {
   const ws = new WebSocket(url);
   ws.addEventListener('open', () => resolve(new CDP(ws)), { once: true });
   ws.addEventListener('error', () => reject(new Error(`CDP websocket to ${url} failed`)), { once: true });
  });
 }

 on(method, handler) {
  if (!this.listeners.has(method)) this.listeners.set(method, []);
  this.listeners.get(method).push(handler);
 }

 send(method, params = {}) {
  const id = this.nextId++;
  return new Promise((resolve, reject) => {
   this.pending.set(id, { resolve, reject });
   this.ws.send(JSON.stringify({ id, method, params }));
  });
 }

 close() {
  try { this.ws.close(); } catch { /* already closing */ }
 }
}

async function waitForEvent(session, method, timeoutMs) {
 return new Promise((resolve, reject) => {
  const timer = setTimeout(
   () => reject(new SmokeFailure(`timed out after ${timeoutMs / 1000}s waiting for ${method}`)),
   timeoutMs,
  );
  session.on(method, () => {
   clearTimeout(timer);
   resolve();
  });
 });
}

async function openPage(cdpPort, url) {
 const response = await fetch(`http://127.0.0.1:${cdpPort}/json/new?url=${encodeURIComponent('about:blank')}`);
 if (!response.ok) throw new SmokeFailure(`/json/new returned HTTP ${response.status}`);
 const tab = await response.json();
 const session = await CDP.connect(tab.webSocketDebuggerUrl);
 await session.send('Runtime.enable');
 await session.send('Page.enable');
 try {
  await session.send('Log.enable'); // absent on some engines: console errors stay covered by Runtime
 } catch { /* Log domain unavailable */ }
 const errors = [];
 session.on('Runtime.exceptionThrown', params => {
  const detail = params.exceptionDetails;
  const text = detail.exception?.description || detail.exception?.value || detail.text || 'unknown exception';
  errors.push({ kind: 'page-error', text: String(text) });
 });
 session.on('Runtime.consoleAPICalled', params => {
  if (params.type !== 'error') return;
  errors.push({ kind: 'console-error', text: params.args.map(arg => arg.value ?? arg.description ?? '').join(' ') || 'console.error' });
 });
 session.on('Log.entryAdded', params => {
  if (params.entry.level !== 'error') return;
  errors.push({ kind: 'console-error', text: `${params.entry.source}: ${params.entry.text}` });
 });
 await session.send('Page.navigate', { url });
 await waitForEvent(session, 'Page.loadEventFired', MILESTONE_TIMEOUT_MS);
 return { session, errors };
}

async function snapshot(session) {
 try {
  const { result } = await session.send('Runtime.evaluate', {
   expression: `(function () {
        var startup = document.getElementById('startup');
        var app = document.getElementById('app');
        var message = document.getElementById('startup-message');
        var panel = document.getElementById('fatal-error');
        var geometry = null;
        if (panel) {
          var rect = panel.getBoundingClientRect();
          geometry = { display: getComputedStyle(panel).display, visibility: getComputedStyle(panel).visibility, width: rect.width, height: rect.height };
        }
        return {
          startupGone: !startup,
          startupMessage: message ? message.textContent : null,
          appChildren: app ? app.children.length : -1,
          fatalPanel: panel ? { present: true, role: panel.getAttribute('role'), text: (panel.textContent || '').slice(0, 200), geometry: geometry } : null
        };
      })()`,
   returnByValue: true,
  });
  return result.value;
 } catch {
  return null; // e.g. execution context teardown between polls; callers keep polling
 }
}

async function poll(check, timeoutMs) {
 const deadline = Date.now() + timeoutMs;
 let last;
 while (Date.now() < deadline) {
  last = await check();
  if (last.done) return last;
  await delay(POLL_INTERVAL_MS);
 }
 return { done: false, last };
}

function reportErrors(label, errors) {
 for (const error of errors.slice(0, 10)) {
  console.error(`smoke-tizen-engine: ${label} saw a ${error.kind}: ${error.text.split('\n').slice(0, 4).join(' | ')}`);
 }
 if (errors.length > 10) console.error(`smoke-tizen-engine: ${label} saw ${errors.length} errors total (first 10 shown)`);
}

async function runCleanAndFatal(state, cdpPort, servePort, browser) {
 const { session, errors } = await openPage(cdpPort, `http://127.0.0.1:${servePort}/index.html`);
 try {
  const boot = await poll(async () => {
   const view = await snapshot(session);
   return { done: Boolean(view && view.startupGone), view };
  }, MILESTONE_TIMEOUT_MS);

  if (!boot.done) {
   const view = boot.last?.view ?? {};
   reportErrors('clean boot', errors);
   throw new SmokeFailure(`case 1 clean boot FAILED — the startup handoff never completed within ${MILESTONE_TIMEOUT_MS / 1000}s${view.startupMessage ? `; startup screen message: ${JSON.stringify(view.startupMessage)}` : ''}`);
  }
  if (errors.length > 0) {
   reportErrors('clean boot', errors);
   throw new SmokeFailure(`case 1 clean boot FAILED — the page reached first render but produced ${errors.length} page/console error(s)`);
  }
  if (!boot.view.appChildren) {
   throw new SmokeFailure('case 1 clean boot FAILED — #startup was removed but #app has no rendered children');
  }
  console.log(`case 1 clean boot: PASS — engine ${browser}; milestone: window.FileListBoot.ready() removed #startup after the first successful render of #app (${boot.view.appChildren} child node(s)); 0 page errors, 0 console errors.`);

  if (state.cases.includes('fatal')) await runFatal(session, state);
 } finally {
  session.close();
 }
}

async function runFatal(session, state) {
 await session.send('Runtime.evaluate', {
  expression: `setTimeout(function () { throw new Error('smoke-tizen-engine: injected fatal error'); }, 0);`,
 });

 const shown = await poll(async () => {
  const view = await snapshot(session);
  const panel = view?.fatalPanel;
  const visible = Boolean(panel?.geometry
   && panel.geometry.display !== 'none'
   && panel.geometry.visibility !== 'hidden'
   && panel.geometry.width > 0
   && panel.geometry.height > 0);
  return { done: visible, view };
 }, MILESTONE_TIMEOUT_MS);

 if (!shown.done) {
  throw new SmokeFailure(`case 2 injected error FAILED — the #fatal-error panel never appeared or is not visible within ${MILESTONE_TIMEOUT_MS / 1000}s after an uncaught error`);
 }
 const panel = shown.view.fatalPanel;
 if (panel.role !== 'alert') {
  throw new SmokeFailure(`case 2 injected error FAILED — the panel exists but carries role="${panel.role}" instead of "alert"`);
 }

 const reported = await poll(async () => {
  const hit = state.diagnosticsPosts.some(post => post.level === 'error');
  return { done: hit };
 }, DIAGNOSTICS_TIMEOUT_MS);
 if (!reported.done) {
  throw new SmokeFailure(`case 2 injected error FAILED — the panel ("${panel.text.slice(0, 80)}…") is visible but no level=error diagnostics POST reached /api/v1/diagnostics/client`);
 }
 const report = state.diagnosticsPosts.find(post => post.level === 'error');
 console.log(`case 2 injected error: PASS — #fatal-error panel is present, visible (role=alert), and reported via POST /api/v1/diagnostics/client (recorded: level=${report.level}, message=${JSON.stringify(String(report.message).slice(0, 80))}).`);
}

async function runBroken(state, cdpPort, servePort, browser) {
 const { session, errors } = await openPage(cdpPort, `http://127.0.0.1:${servePort}/index.html`);
 try {
  const outcome = await poll(async () => {
   const view = await snapshot(session);
   if (errors.length > 0 || view?.fatalPanel) return { done: true, reason: 'detected', view: view ?? {} };
   if (view?.startupGone) return { done: true, reason: 'booted', view };
   return { done: false, view: view ?? {} };
  }, BROKEN_DETECT_TIMEOUT_MS);

  if (outcome.done && outcome.reason === 'booted') {
   throw new SmokeFailure(`case 3 broken bundle FAILED — the fixture with the corrupted app.js reached the startup handoff on ${browser}; the harness did not detect the breakage`, 2);
  }
  if (!outcome.done) {
   throw new SmokeFailure(`case 3 broken bundle FAILED — no error surfaced within ${BROKEN_DETECT_TIMEOUT_MS / 1000}s and the fixture did not boot; inconclusive`, 2);
  }
  const first = errors[0];
  console.log(`case 3 broken bundle: rejected as designed on ${browser} — ${first ? `${first.kind}: ${first.text.split('\n')[0]}` : 'the fatal panel appeared without a collected error'}.`);
  throw new SmokeFailure('case 3 broken bundle: the smoke contract was violated by the fixture — exit code 3 (detection proven, non-zero by design)', 3);
 } finally {
  session.close();
 }
}

async function main() {
 if (typeof WebSocket !== 'function') {
  throw new SmokeFailure('this smoke needs native WebSocket — use Node >= 22 (CI pins Node 24)');
 }
 const state = parseArgs(process.argv.slice(2));
 const distRoot = path.resolve(state.dist);
 if (!fs.existsSync(path.join(distRoot, 'index.html'))) {
  throw new SmokeFailure(`no index.html under ${distRoot} — build the client first (make frontend)`);
 }

 let root = distRoot;
 if (state.cases.includes('broken')) {
  const fixture = fs.mkdtempSync(path.join(os.tmpdir(), 'fls-smoke-broken-'));
  fs.cpSync(distRoot, fixture, { recursive: true });
  fs.appendFileSync(path.join(fixture, 'app.js'), BROKEN_FIXTURE_SUFFIX);
  state.fixtureDir = fixture;
  root = fixture;
 }

 state.server = createServer(state, root);
 const servePort = await new Promise((resolve, reject) => {
  state.server.once('error', reject);
  state.server.listen(0, '127.0.0.1', () => resolve(state.server.address().port));
 });
 const cdpPort = await freePort();
 startChrome(state, cdpPort);
 const { browser } = await waitCDP(cdpPort, state.image);
 if (!/^HeadlessChrome\/63\./.test(browser)) {
  console.error(`smoke-tizen-engine: WARNING — pinned image reported "${browser}"; the documented guarantee targets Chrome 63 (selenoid/chrome:63.0)`);
 }
 console.log(`smoke-tizen-engine: serving ${root} on http://127.0.0.1:${servePort} with $WEBAPIS stub + diagnostics recorder; engine ${browser}; image ${state.image}; cases: ${state.cases.join(',')}`);

 try {
  if (state.cases.includes('broken')) {
   await runBroken(state, cdpPort, servePort, browser);
  } else {
   await runCleanAndFatal(state, cdpPort, servePort, browser);
   console.log('smoke-tizen-engine: all requested cases passed on the pinned old engine.');
  }
 } finally {
  cleanup(state);
 }
}

const state = parseArgs(process.argv.slice(2));
for (const signal of ['SIGINT', 'SIGTERM']) {
 process.on(signal, () => {
  cleanup(state);
  process.exit(1);
 });
}
main().catch(error => {
 cleanup(state);
 console.error(`smoke-tizen-engine: ${error?.message || error}`);
 process.exit(error instanceof SmokeFailure ? error.code : 1);
});
