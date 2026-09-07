#!/usr/bin/env node
/**
 * Old-engine boot/layout/focus smoke for the webOS TV client (parent #79).
 *
 * Pinned floor engine: `selenoid/chrome:53.0` — Google Chrome 53.0.2785.143
 * (verified with `google-chrome --version` inside the image; digest pinned
 * below). webOS 4.x TVs ship Chromium 53, which has no --headless mode, so the
 * floor leg runs Xvfb plus plain Chrome inside the container and drives it over
 * raw CDP published on host loopback only (-p 127.0.0.1:...). The container
 * reaches the host fixture server via host.docker.internal (Docker Desktop);
 * the server itself still binds 127.0.0.1.
 *
 * Two engine legs:
 *   floor  — dockerized Chrome 53 (default; the engine under guarantee)
 *   host   — a locally installed modern Chrome via --browser <path>
 *            (proves capable engines keep the authoritative Grid layout and
 *            native scrollIntoView behavior)
 * Browser.getVersion is queried from CDP and recorded with the output; the
 * floor leg REFUSES to run against anything but Chrome/53.* so a newer engine
 * can never silently stand in for the floor.
 *
 * Three cases:
 *   clean  — the packaged clients/webos/dist boots at 1920x1080 with zero page
 *            errors, the Setup screen is traversed with D-pad keys (Manual
 *            address, in-place edit of the server URL, OK-exit), the fixture
 *            server is connected, Home renders, sidebar/content focus moves,
 *            and the scroll regression proves a below-fold row and an
 *            off-right poster are revealed exactly (nearest vertical,
 *            centered horizontal) without disturbing unrelated scrollports.
 *   fatal  — after boot, an uncaught error must surface the FileListFatalError
 *            panel (role=alert) and POST level=error diagnostics to the
 *            fixture's /api/v1/diagnostics/client.
 *   broken — a temporary dist copy WITHOUT app.js must never boot; detection
 *            evidence (404 for app.js, startup overlay still mounted, bundle
 *            global absent) is recorded and the case exits with code 3 by
 *            design (the Make gate in Task 7 requires that non-zero exit).
 *
 * Exit codes: 0 all requested cases passed; 1 infrastructure failure; 2 the
 * broken fixture booted or stayed inconclusive; 3 broken fixture rejected as
 * designed.
 *
 * Zero npm dependencies: Node >= 22 (native fetch/WebSocket) drives raw CDP.
 * Screenshots, engine version, console/page errors and the scroll audit are
 * written under --output (default clients/webos/.build/smoke); temporary dist
 * copies are removed after every run.
 */
import { spawn, spawnSync } from 'node:child_process';
import http from 'node:http';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

const DEFAULT_IMAGE = 'selenoid/chrome:53.0'; // Google Chrome 53.0.2785.143 (verified via `--version` inside the image)
const DEFAULT_IMAGE_DIGEST = 'sha256:5e3d995ded003752cf9a8450657caa163308fec51578708c23c624f37b97c0ea';
const DEFAULT_HOST_BROWSER = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const BOOT_TIMEOUT_MS = 60_000; // emulated amd64 Chrome 53 on Apple Silicon is slow to first render
const CONNECT_TIMEOUT_MS = 60_000;
const MILESTONE_TIMEOUT_MS = 45_000;
const BROKEN_DETECT_TIMEOUT_MS = 30_000;
const DIAGNOSTICS_TIMEOUT_MS = 10_000;
const POLL_INTERVAL_MS = 250;
const KEY_PACING_MS = 60;

const MIME = {
 '.html': 'text/html; charset=utf-8',
 '.js': 'text/javascript; charset=utf-8',
 '.css': 'text/css; charset=utf-8',
 '.png': 'image/png',
 '.svg': 'image/svg+xml',
 '.json': 'application/json',
 '.ico': 'image/x-icon',
};

/** A case outcome that must fail the run with a specific exit code after cleanup. */
class SmokeFailure extends Error {
 constructor(message, code = 1) {
  super(message);
  this.code = code;
 }
}

function parseArgs(argv) {
 const state = {
  cases: ['clean', 'fatal'],
  image: DEFAULT_IMAGE,
  dist: 'clients/webos/dist',
  output: 'clients/webos/.build/smoke',
  browser: null, // set => host engine leg
 };
 for (let i = 0; i < argv.length; i++) {
  const name = argv[i];
  const value = argv[i + 1];
  if ((name === '--case' || name === '--cases') && value) {
   state.cases = value.split(',').map(item => item.trim()).filter(Boolean);
   i++;
  } else if (name === '--image' && value) {
   state.image = value;
   i++;
  } else if (name === '--dist' && value) {
   state.dist = value;
   i++;
  } else if (name === '--output' && value) {
   state.output = value;
   i++;
  } else if (name === '--browser' && value) {
   state.browser = value;
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
  throw new SmokeFailure('the broken fixture must run in its own invocation: --case broken');
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

// ---------------------------------------------------------------------------
// Fixture server: packaged dist + the API subset the TV app uses after
// "Connect", an SSE event stream, and a diagnostics POST recorder.
// ---------------------------------------------------------------------------

function smokeTitle(index) {
 const number = String(index + 1).padStart(2, '0');
 const kind = index % 3 === 2 ? 'series' : 'movie';
 return {
  id: `smoke-title-${number}`,
  title: `Smoke Feature ${number}`,
  kind,
  year: 1990 + (index % 30),
  overview: `Fixture feature number ${number} used by the webOS engine smoke.`,
  categories: ['smoke'],
  resolutions: index % 2 ? ['1080p'] : ['2160p', '1080p'],
  sourceCount: 3,
  bestSeeders: 40 + index,
  largestSizeBytes: 4_000_000_000 + index,
  seasonCount: kind === 'series' ? 2 : undefined,
  episodeCount: kind === 'series' ? 20 : undefined,
 };
}

function smokeHouseholdItem(title, positionRatio) {
 const release = {
  id: `smoke-release-${title.id}`,
  name: `${title.title} 1080p`,
  category: 'smoke',
  sizeBytes: title.largestSizeBytes,
  seeders: title.bestSeeders,
  leechers: 1,
  freeleech: false,
 };
 return {
  profileId: 'smoke-profile',
  sourceId: `smoke-source-${title.id}`,
  releaseId: release.id,
  fileIndex: 0,
  filePath: `/smoke/${title.id}.mkv`,
  positionMs: Math.round(title.largestSizeBytes * positionRatio) % 60_000,
  durationMs: 60_000,
  watched: false,
  updatedAt: '2026-09-07T00:00:00Z',
  release,
  catalog: title,
  favorite: true,
  titleId: title.id,
 };
}

const TITLES = Array.from({ length: 24 }, (_, index) => smokeTitle(index));

const API_FIXTURE = {
 '/api/v1/system/info': () => ({ name: 'Torrent TV', instanceName: 'Smoke Fixture', version: '0.0.0-smoke', apiVersion: '1', configured: true, capabilities: [] }),
 '/api/v1/catalog/facets': () => ({ categories: ['smoke'], kinds: ['movie', 'series'], resolutions: ['1080p'], hdr: [], qualities: [], codecs: [] }),
 '/api/v1/downloads': () => ({ items: [], nextCursor: null, total: 0 }),
 '/api/v1/jobs': () => ({
  items: [{ id: 'smoke-job-1', label: 'Smoke catalog sync', state: 'completed', attempt: 1, kind: 'catalog-sync', updatedAt: '2026-09-07T00:00:00Z' }],
  nextCursor: null,
  total: 1,
 }),
 '/api/v1/state': () => ({
  favorites: TITLES.slice(0, 6).map(title => smokeHouseholdItem(title, 0.1)),
  continueWatching: TITLES.slice(6, 10).map(title => smokeHouseholdItem(title, 0.4)),
  recent: [],
  watched: [],
 }),
 '/api/v1/portal/state': () => ({ accountsEnabled: false, adsEnabled: false, donor: false, links: [] }),
 '/api/v1/updates/current': () => ({ currentVersion: '0.0.0-smoke', available: false, releasesUrl: 'https://example.invalid/releases', selfUpdate: false, applying: false }),
};

function createServer(state, root) {
 state.diagnosticsPosts = [];
 state.requests = []; // { pathname, status }
 state.eventStreams = new Set();
 return http.createServer((req, res) => {
  const pathname = decodeURIComponent(new URL(req.url, 'http://127.0.0.1').pathname);
  if (pathname === '/favicon.ico') {
   state.requests.push({ pathname, status: 204 });
   res.writeHead(204);
   res.end();
   return;
  }
  if (pathname === '/$WEBAPIS/webapis/webapis.js') {
   res.writeHead(200, { 'Content-Type': MIME['.js'] });
   res.end(`window.webapis = { avplay: { open: function(){}, close: function(){}, stop: function(){}, prepareAsync: function(){}, play: function(){}, seekTo: function(){}, setListener: function(){}, setDisplayArea: function(){}, setSelectTrack: function(){}, setSilentSubtitle: function(){}, setSubtitlePosition: function(){}, getDuration: function(){return 0;}, getCurrentTime: function(){return 0;}, getTotalTrackInfo: function(){return [];} }, network: { getIp: function(){return '127.0.0.1';}, getSubnetMask: function(){return '255.255.255.0';} } };`);
   return;
  }
  if (req.method === 'POST' && pathname === '/api/v1/diagnostics/client') {
   let body = '';
   req.on('data', chunk => { body += chunk; });
   req.on('end', () => {
    try { state.diagnosticsPosts.push(JSON.parse(body)); } catch { state.diagnosticsPosts.push({ raw: body }); }
    state.requests.push({ pathname, status: 200 });
    res.writeHead(200, { 'Content-Type': MIME['.json'] });
    res.end('{}');
   });
   return;
  }
  if (pathname === '/api/v1/events') {
   res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', Connection: 'keep-alive' });
   res.write('retry: 3000\n\n');
   state.eventStreams.add(res);
   res.on('close', () => state.eventStreams.delete(res));
   return;
  }
  if (pathname === '/api/v1/metadata/ensure' || pathname === '/api/v1/catalog/sync') {
   let body = '';
   req.on('data', chunk => { body += chunk; });
   req.on('end', () => {
    state.requests.push({ pathname, status: 200 });
    res.writeHead(200, { 'Content-Type': MIME['.json'] });
    res.end(pathname === '/api/v1/metadata/ensure' ? '{"queued":0}' : '{"id":"smoke-job-1","label":"Smoke catalog sync","state":"completed","attempt":1,"kind":"catalog-sync","updatedAt":"2026-09-07T00:00:00Z"}');
   });
   return;
  }
  const fixed = API_FIXTURE[pathname.split('?')[0]];
  if (req.method === 'GET' && fixed) {
   state.requests.push({ pathname, status: 200 });
   res.writeHead(200, { 'Content-Type': MIME['.json'] });
   res.end(JSON.stringify(fixed()));
   return;
  }
  if (req.method === 'GET' && pathname.startsWith('/api/v1/catalog/titles/')) {
   const id = pathname.split('/').pop();
   const title = TITLES.find(t => t.id === id) || smokeTitle(0);
   const release = {
    id: `smoke-release-${title.id}`,
    name: `${title.title} 1080p`,
    category: 'smoke',
    sizeBytes: title.largestSizeBytes,
    seeders: title.bestSeeders,
    leechers: 1,
    freeleech: false,
   };
   const source = {
    release,
    fileIndex: 0,
    streamUrl: '/api/v1/stream/smoke-stream.mp4',
    parsed: { resolution: '1080p', quality: 'BluRay', videoCodec: 'H.264', audio: 'AAC' },
    libraryState: {
     downloadId: `smoke-download-${title.id}`,
     downloadState: 'downloaded',
     transferState: 'completed',
     watchState: 'unwatched',
     progress: 1.0,
    },
   };
   state.requests.push({ pathname, status: 200 });
   res.writeHead(200, { 'Content-Type': MIME['.json'] });
   res.end(JSON.stringify({
    title,
    seasons: [],
    sources: [source],
   }));
   return;
  }
  if (req.method === 'GET' && pathname.startsWith('/api/v1/catalog/titles')) {
   state.requests.push({ pathname, status: 200 });
   res.writeHead(200, { 'Content-Type': MIME['.json'] });
   res.end(JSON.stringify({ items: TITLES, nextCursor: null, total: TITLES.length }));
   return;
  }
  if (pathname === '/api/v1/stream/smoke-stream.mp4') {
   const videoPath = path.resolve('tools/smoke_webos_engine/fixture-video.mp4');
   const stat = fs.statSync(videoPath);
   const range = req.headers.range;
   if (range) {
    const parts = range.replace(/bytes=/, '').split('-');
    const start = parseInt(parts[0], 10);
    const end = parts[1] ? parseInt(parts[1], 10) : stat.size - 1;
    res.writeHead(206, {
     'Content-Range': `bytes ${start}-${end}/${stat.size}`,
     'Accept-Ranges': 'bytes',
     'Content-Length': (end - start) + 1,
     'Content-Type': 'video/mp4',
    });
    fs.createReadStream(videoPath, { start, end }).pipe(res);
   } else {
    res.writeHead(200, {
     'Content-Length': stat.size,
     'Accept-Ranges': 'bytes',
     'Content-Type': 'video/mp4',
    });
    fs.createReadStream(videoPath).pipe(res);
   }
   return;
  }
  if (req.method === 'POST' && pathname.includes('/prepare')) {
   let body = '';
   req.on('data', chunk => { body += chunk; });
   req.on('end', () => {
    state.requests.push({ pathname, status: 200 });
    res.writeHead(200, { 'Content-Type': MIME['.json'] });
    res.end(JSON.stringify({
     id: 'smoke-download-1',
     sourceId: 'smoke-source-1',
     releaseId: 'smoke-release-1',
     fileIndex: 0,
     filePath: '/smoke/smoke.mp4',
     sizeBytes: 78848,
     downloadedBytes: 78848,
     progress: 1.0,
     state: 'completed',
     playbackMode: 'direct',
     streamUrl: '/api/v1/stream/smoke-stream.mp4',
     createdAt: '2026-09-07T00:00:00Z',
     updatedAt: '2026-09-07T00:00:00Z',
    }));
   });
   return;
  }
  if (req.method === 'GET' && pathname.startsWith('/api/v1/playback/')) {
   state.requests.push({ pathname, status: 200 });
   res.writeHead(200, { 'Content-Type': MIME['.json'] });
   if (pathname.endsWith('/preferences')) {
    res.end(JSON.stringify({
     sourceId: 'smoke-download-1',
     audioLanguage: 'en',
     audioTrackIndex: 0,
     subtitleMode: 'off',
     subtitleLanguage: '',
     subtitleProvider: '',
     subtitleCandidateId: '',
     subtitleDelay: 0,
    }));
   } else {
    res.end(JSON.stringify({
     sourceId: 'smoke-download-1',
     positionMs: 0,
     durationMs: 15000,
     watched: false,
     updatedAt: '2026-09-07T00:00:00Z',
    }));
   }
   return;
  }
  if (req.method === 'PUT' && pathname.startsWith('/api/v1/playback/')) {
   let body = '';
   req.on('data', chunk => { body += chunk; });
   req.on('end', () => {
    state.requests.push({ pathname, status: 200 });
    res.writeHead(200, { 'Content-Type': MIME['.json'] });
    res.end('{}');
   });
   return;
  }
  if (pathname.includes('/subtitles')) {
   state.requests.push({ pathname, status: 200 });
   res.writeHead(200, { 'Content-Type': MIME['.json'] });
   res.end(JSON.stringify({ items: [] }));
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
    state.requests.push({ pathname, status: 404 });
    res.writeHead(404).end('not found');
    return;
   }
   state.requests.push({ pathname, status: 200 });
   let body = data;
   if (pathname === '/index.html' || pathname === '/') {
    const stub = '<script>if(!window.PalmServiceBridge){window.PalmServiceBridge=function(){this.onservicecallback=null;};window.PalmServiceBridge.prototype.call=function(){var s=this;setTimeout(function(){if(s.onservicecallback)s.onservicecallback(JSON.stringify({returnValue:true,isInternetConnectionAvailable:true,wired:{state:"connected",ipAddress:"127.0.0.1",netmask:"255.255.255.0"}}));},0);};window.PalmServiceBridge.prototype.cancel=function(){};}</script>';
    body = Buffer.from(data.toString('utf8').replace('<head>', '<head>' + stub));
   }
   res.writeHead(200, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream', 'Content-Length': body.length });
   res.end(req.method === 'HEAD' ? undefined : body);
  });
 });
}
// ---------------------------------------------------------------------------
// Engine legs
// ---------------------------------------------------------------------------

function assertPinnedDigest(state) {
 if (state.image !== DEFAULT_IMAGE) return; // explicit override: caller owns the pin
 const result = spawnSync('docker', ['image', 'inspect', '--format', '{{join .RepoDigests ","}}', state.image], { encoding: 'utf8' });
 const digests = (result.stdout || '').trim();
 if (result.status !== 0 || !digests) {
  throw new SmokeFailure(`could not read the pulled digest of ${state.image}: ${result.stderr || result.error || 'image not found — docker pull it first'}`);
 }
 if (!digests.split(',').some(digest => digest.endsWith(DEFAULT_IMAGE_DIGEST))) {
  throw new SmokeFailure(`${state.image} digest ${digests} does not match the pinned ${DEFAULT_IMAGE_DIGEST}; the floor guarantee follows the pinned image`);
 }
}

function startFloorChrome(state, cdpPort) {
 assertPinnedDigest(state);
 const name = `fls-smoke-webos-engine-${process.pid}-${Date.now()}`;
 // Chrome 53 predates --headless: Xvfb provides the display, plain Chrome
 // binds CDP to the container's loopback and docker publishes exactly that
 // port onto the host's loopback (-p 127.0.0.1:...).
 const proxyPy = Buffer.from(`
import socket, select
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('0.0.0.0', ${cdpPort}))
s.listen(16)
socks = [s]
pairs = {}
while True:
 r, _, _ = select.select(socks, [], [], 1.0)
 for x in r:
  if x is s:
   c, _ = s.accept()
   t = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
   try:
    t.connect(('127.0.0.1', 9222))
    socks.extend([c, t])
    pairs[c] = t
    pairs[t] = c
   except Exception:
    c.close()
  else:
   peer = pairs.get(x)
   try:
    d = x.recv(65536)
    if d and peer: peer.sendall(d)
    else: raise Exception()
   except Exception:
    for p in [x, peer]:
     if p:
      try: socks.remove(p)
      except Exception: pass
      try: del pairs[p]
      except Exception: pass
      try: p.close()
      except Exception: pass
`).toString('base64');
 const result = spawnSync('docker', [
  'run', '--rm', '-d', '--name', name, '--platform', 'linux/amd64',
  '-p', `127.0.0.1:${cdpPort}:${cdpPort}`,
  '--entrypoint', '/bin/sh', state.image,
  '-c',
  `Xvfb :99 -screen 0 1920x1080x24 -nolisten tcp & ` +
  `sleep 1; ` +
  `DISPLAY=:99 /usr/bin/google-chrome ` +
  `--no-sandbox --disable-gpu --disable-dev-shm-usage ` +
  `--remote-debugging-port=9222 ` +
  `--user-data-dir=/tmp/fls-smoke-webos-profile --no-first-run --no-default-browser-check ` +
  `--disable-background-networking --disable-component-update --mute-audio --hide-scrollbars ` +
  `--window-size=1920,1080 about:blank & ` +
  `echo "${proxyPy}" | base64 -d | exec python3`,
 ], { encoding: 'utf8' });
 if (result.error || result.status !== 0) {
  throw new SmokeFailure(`docker run ${state.image} failed: ${result.stderr || result.stdout || result.error}`);
 }
 state.container = name;
}

function startHostChrome(state, cdpPort) {
 if (!fs.existsSync(state.browser)) {
  throw new SmokeFailure(`--browser ${state.browser} does not exist on this machine`);
 }
 state.hostProfile = fs.mkdtempSync(path.join(os.tmpdir(), 'fls-smoke-webos-profile-'));
 state.child = spawn(state.browser, [
  '--headless',
  `--remote-debugging-address=127.0.0.1`, `--remote-debugging-port=${cdpPort}`,
  `--user-data-dir=${state.hostProfile}`,
  '--no-first-run', '--no-default-browser-check',
  '--disable-background-networking', '--disable-component-update', '--mute-audio', '--hide-scrollbars',
  '--window-size=1920,1080',
  'about:blank',
 ], { stdio: 'ignore' });
}

function cleanup(state) {
 if (state.container) {
  try { spawnSync('docker', ['rm', '-f', state.container], { encoding: 'utf8' }); } catch { }
  state.container = null;
 }
 if (state.child) {
  try { state.child.kill('SIGKILL'); } catch { }
  state.child = null;
 }
 if (state.server) {
  for (const stream of state.eventStreams || []) { try { stream.end(); } catch { } }
  try { state.server.close(); } catch { }
  state.server = null;
 }
 if (state.fixtureDir) {
  try { fs.rmSync(state.fixtureDir, { recursive: true, force: true }); } catch { }
  state.fixtureDir = null;
 }
 if (state.hostProfile) {
  try { fs.rmSync(state.hostProfile, { recursive: true, force: true }); } catch { }
  state.hostProfile = null;
 }
}

async function waitCDP(cdpPort) {
 const deadline = Date.now() + BOOT_TIMEOUT_MS;
 let lastError = '';
 while (Date.now() < deadline) {
  try {
   const response = await fetch(`http://127.0.0.1:${cdpPort}/json/version`);
   if (response.ok) {
    const info = await response.json();
    return { browser: info.Browser || 'unknown', userAgent: info['User-Agent'] || '' };
   }
   lastError = `HTTP ${response.status}`;
  } catch (error) {
   lastError = String(error.cause || error.message || error);
  }
  await delay(300);
 }
 throw new SmokeFailure(`Chrome CDP on 127.0.0.1:${cdpPort} did not become ready within ${BOOT_TIMEOUT_MS / 1000}s (${lastError})`);
}

// ---------------------------------------------------------------------------
// Raw CDP driver (Node >= 22 native WebSocket)
// ---------------------------------------------------------------------------

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
 // Modern Chrome requires PUT for /json/new; Chrome 53 only knows GET.
 let response = await fetch(`http://127.0.0.1:${cdpPort}/json/new?url=${encodeURIComponent('about:blank')}`, { method: 'PUT' });
 if (!response.ok) response = await fetch(`http://127.0.0.1:${cdpPort}/json/new?url=${encodeURIComponent('about:blank')}`);
 if (!response.ok) throw new SmokeFailure(`/json/new returned HTTP ${response.status}`);
 const tab = await response.json();
 const session = await CDP.connect(tab.webSocketDebuggerUrl);
 await session.send('Runtime.enable');
 await session.send('Page.enable');
 try {
  await session.send('Log.enable'); // absent on some engines: console errors stay covered by Runtime
 } catch { /* Log domain unavailable */ }
 try {
  await session.send('Emulation.setDeviceMetricsOverride', { width: 1920, height: 1080, deviceScaleFactor: 1, mobile: false, fitWindow: false });
 } catch {
  await session.send('Emulation.setDeviceMetricsOverride', { width: 1920, height: 1080, deviceScaleFactor: 1, mobile: false });
 }
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

async function evaluate(session, expression) {
 const { result, exceptionDetails } = await session.send('Runtime.evaluate', { expression, returnByValue: true });
 if (exceptionDetails) throw new SmokeFailure(`page evaluation failed: ${exceptionDetails.exception?.description || exceptionDetails.text}`);
 return result.value;
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

const KEY_CODES = { ArrowLeft: 37, ArrowRight: 39, ArrowUp: 38, ArrowDown: 40, Enter: 13, Escape: 27 };

async function pressKey(session, key) {
 const params = { key, code: key, windowsVirtualKeyCode: KEY_CODES[key], nativeVirtualKeyCode: KEY_CODES[key] };
 await session.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...params });
 await session.send('Input.dispatchKeyEvent', { type: 'keyUp', ...params });
 await delay(KEY_PACING_MS);
}

async function pressOK(session) {
 // The TV layer reads keyCode 65376 as the remote's OK/IME-done key.
 const params = { key: 'XF86OK', code: 'NXSOK', windowsVirtualKeyCode: 65376, nativeVirtualKeyCode: 65376 };
 await session.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', ...params });
 await session.send('Input.dispatchKeyEvent', { type: 'keyUp', ...params });
 await delay(KEY_PACING_MS);
}

async function typeText(session, text) {
 for (const character of text) {
  await session.send('Input.dispatchKeyEvent', { type: 'char', text: character, key: character, unmodifiedText: character });
  await delay(20);
 }
 await delay(200);
}

async function screenshot(session, outputDir, name) {
 const { data } = await session.send('Page.captureScreenshot', { format: 'png' });
 const file = path.join(outputDir, name);
 fs.writeFileSync(file, Buffer.from(data, 'base64'));
 return file;
}

function reportErrors(label, errors) {
 for (const error of errors.slice(0, 10)) {
  console.error(`smoke-webos-engine: ${label} saw a ${error.kind}: ${error.text.split('\n').slice(0, 4).join(' | ')}`);
 }
 if (errors.length > 10) console.error(`smoke-webos-engine: ${label} saw ${errors.length} errors total (first 10 shown)`);
}

// ---------------------------------------------------------------------------
// Page probes
// ---------------------------------------------------------------------------

const SCROLL_AUDIT_JS = `(function () {
  var active = document.activeElement;
  var content = document.querySelector('.tv-content');
  var rails = Array.prototype.map.call(document.querySelectorAll('.poster-rail'), function (rail, index) {
    return { index: index, scrollLeft: rail.scrollLeft, scrollWidth: rail.scrollWidth, clientWidth: rail.clientWidth };
  });
  var rect = active && active.getBoundingClientRect ? active.getBoundingClientRect() : null;
  var railElement = active && active.closest ? active.closest('.poster-rail') : null;
  var railRect = railElement ? railElement.getBoundingClientRect() : null;
  var centerDelta = (railElement && railRect && rect)
    ? Math.abs((rect.left + rect.width / 2) - (railRect.left + railElement.clientWidth / 2))
    : null;
  return {
    focusKey: active ? active.getAttribute('data-focus-key') : null,
    col: active ? active.getAttribute('data-focus-col') : null,
    rect: rect ? { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width, height: rect.height } : null,
    viewport: { width: window.innerWidth, height: window.innerHeight },
    contentScrollTop: content ? content.scrollTop : null,
    contentScrollLeft: content ? content.scrollLeft : null,
    rails: rails,
    activeRail: railElement ? { centerDelta: centerDelta } : null
  };
})()`;


async function scrollAudit(session) {
 return evaluate(session, SCROLL_AUDIT_JS);
}

const LAYOUT_AUDIT_JS = `(function() {
 var computed = function(selector) {
  var element = document.querySelector(selector);
  if (!element) return null;
  var style = getComputedStyle(element);
  return { display: style.display, alignItems: style.alignItems, justifyItems: style.justifyItems };
 };
 var glyph = (function() {
  var fallback = document.querySelector('.poster-fallback');
  if (!fallback) return null;
  var box = fallback.getBoundingClientRect();
  var range = document.createRange();
  range.selectNodeContents(fallback);
  var text = range.getBoundingClientRect();
  return {
   boxWidth: box.width, boxHeight: box.height,
   offsetLeft: text.left - box.left,
   offsetRight: box.right - text.right,
   offsetTop: text.top - box.top,
   offsetBottom: box.bottom - text.bottom
  };
 })();
 return {
  supportsGrid: window.CSS && CSS.supports ? CSS.supports('display', 'grid') : false,
  tvApp: computed('.tv-app'),
  posterRail: computed('.poster-rail'),
  brandBadge: computed('.tv-brand > span'),
  menuIcon: computed('.tv-sidebar button i'),
  fallbackGlyph: glyph
 };
})()`;

async function bootCompleted(session) {
 return poll(async () => {
  const view = await evaluate(session, `(function() {
 var startup = document.getElementById('startup');
 var app = document.getElementById('app');
 var message = document.getElementById('startup-message');
 var panel = document.getElementById('fatal-error');
 return {
  startupGone: !startup,
  startupMessage: message ? message.textContent : null,
  appChildren: app ? app.children.length : -1,
  fatalPanelPresent: Boolean(panel),
  bundleRan: typeof window.TorrentTV !== 'undefined'
 };
})()`);
  return { done: Boolean(view && view.startupGone), view };
 }, BOOT_TIMEOUT_MS);
}

function assertCleanViewport(audit, stage) {
 if (!audit.viewport || audit.viewport.width !== 1920 || audit.viewport.height !== 1080) {
  throw new SmokeFailure(`case clean FAILED at ${stage} — viewport is ${JSON.stringify(audit.viewport)}, expected 1920x1080`);
 }
}

function assertFullyVisible(audit, stage, axis) {
 const rect = audit.rect;
 if (!rect) throw new SmokeFailure(`case clean FAILED at ${stage} — no focused element rect`);
 if (axis === 'horizontal' && (rect.left < 0 || rect.right > audit.viewport.width + 0.5)) {
  throw new SmokeFailure(`case clean FAILED at ${stage} — focused ${audit.focusKey} spans x ${rect.left}..${rect.right}, outside the 1920px scrollport; the engine did not reveal the off - right poster`);
 }
 if (axis === 'vertical' && (rect.top < 0 || rect.bottom > audit.viewport.height + 0.5)) {
  throw new SmokeFailure(`case clean FAILED at ${stage} — focused ${audit.focusKey} spans y ${rect.top}..${rect.bottom}, outside the 1080px scrollport; the engine did not reveal the below - fold row`);
 }
}

// ---------------------------------------------------------------------------
// Cases
// ---------------------------------------------------------------------------

async function runCleanAndFatal(state, session, errors, appBase, browser, outputDir) {
 const artifacts = { screenshots: [], audits: {} };

 // -- milestone 1: boot with zero errors
 const boot = await bootCompleted(session);
 if (!boot.done) {
  const view = boot.last?.view ?? {};
  reportErrors('clean boot', errors);
  throw new SmokeFailure(`case clean FAILED — the startup handoff never completed within ${BOOT_TIMEOUT_MS / 1000}s${view.startupMessage ? `; startup screen message: ${JSON.stringify(view.startupMessage)}` : ''} `);
 }
 if (errors.length > 0) {
  reportErrors('clean boot', errors);
  throw new SmokeFailure(`case clean FAILED — the page reached first render but produced ${errors.length} page / console error(s)`);
 }
 if (!boot.view.appChildren) throw new SmokeFailure('case clean FAILED — #startup was removed but #app has no rendered children');
 console.log(`case clean: boot PASS — engine ${browser}; window.FileListBoot.ready() removed #startup after the first render of #app(${boot.view.appChildren} child node(s)); 0 page errors, 0 console errors.`);

 // -- milestone 2: Setup screen, D-pad to Manual address
 const setupProbe = await poll(async () => {
  const view = await evaluate(session, `(function() {
 var focusables = document.querySelectorAll('[data-focus-region="setup"]');
 var manual = document.querySelector('[data-focus-key="setup-manual"]');
 return { setupButtons: focusables.length, manualPresent: Boolean(manual), activeKey: document.activeElement ? document.activeElement.getAttribute('data-focus-key') : null };
})()`);
  return { done: view.setupButtons > 0, view };
 }, MILESTONE_TIMEOUT_MS);
 if (!setupProbe.done) throw new SmokeFailure('case clean FAILED — the Setup screen never rendered its D-pad controls');
 const setupFile = await screenshot(session, outputDir, 'clean-01-setup.png');
 artifacts.screenshots.push(setupFile);

 await pressKey(session, 'ArrowRight');
 const afterRight = await evaluate(session, `document.activeElement ? document.activeElement.getAttribute('data-focus-key') : null`);
 if (afterRight !== 'setup-manual') {
  throw new SmokeFailure(`case clean FAILED — ArrowRight from Rescan focused "${afterRight}", expected setup - manual`);
 }
 const alreadyOpen = await evaluate(session, `Boolean(document.querySelector('.setup-manual'))`);
 if (!alreadyOpen) {
  await pressKey(session, 'Enter');
 }

 const manualOpen = await poll(async () => {
  const view = await evaluate(session, `Boolean(document.querySelector('.setup-manual'))`);
  return { done: view === true };
 }, MILESTONE_TIMEOUT_MS);
 if (!manualOpen.done) throw new SmokeFailure('case clean FAILED — could not open the manual section');

 // -- milestone 3: edit the server address in place, then OK-exit the input
 await pressKey(session, 'ArrowDown'); // col 1 (manual) -> col 1 (connect)
 await pressKey(session, 'ArrowLeft'); // col 1 (connect) -> col 0 (address input)
 let activeKey = await evaluate(session, `document.activeElement ? document.activeElement.getAttribute('data-focus-key') : null`);
 if (activeKey !== 'setup-address') {
  throw new SmokeFailure(`case clean FAILED — navigation to address input focused "${activeKey}", expected setup-address`);
 }
 await pressKey(session, 'Enter'); // OK on a readOnly input starts tvEditing and selects the value
 await typeText(session, appBase);
 const typed = await evaluate(session, `(function() { var input = document.querySelector('[data-focus-key="setup-address"]'); return { value: input ? input.value : null, editing: input ? input.dataset.tvEditing : null }; })()`);
 if (typed.value !== appBase) {
  throw new SmokeFailure(`case clean FAILED — the address input holds ${JSON.stringify(typed.value)}, expected ${JSON.stringify(appBase)} `);
 }
 if (typed.editing !== 'true') {
  throw new SmokeFailure('case clean FAILED — OK on the address input did not enter edit mode (data-tv-editing)');
 }
 await pressOK(session); // OK-exit: tvEditing off, focus moves to Connect
 activeKey = await evaluate(session, `document.activeElement ? document.activeElement.getAttribute('data-focus-key') : null`);
 if (activeKey !== 'setup-connect') {
  throw new SmokeFailure(`case clean FAILED — OK - exit from the address input focused "${activeKey}", expected setup - connect(the focusin scroll path must land on the next control)`);
 }
 console.log('case clean: setup traversal PASS — Manual address opened, the address was edited in place and OK-exited, focus landed on Connect.');

 // -- milestone 4: connect to the fixture, Home renders
 await pressKey(session, 'Enter');
 const home = await poll(async () => {
  const view = await evaluate(session, `(function () {
    var app = document.querySelector('.tv-app');
    var rail = document.querySelector('.poster-rail');
    var cards = document.querySelectorAll('.poster-rail .poster-card').length;
    var setupStatus = (document.querySelector('.setup-status') || {}).textContent || null;
    var tvStatus = (document.querySelector('.tv-top span') || {}).textContent || null;
    return { appRendered: Boolean(app), railCards: cards, status: tvStatus || setupStatus };
  })()`);
  return { done: Boolean(view.appRendered && view.railCards > 0), view };
 }, CONNECT_TIMEOUT_MS);
 if (!home.done) {
  const view = home.last?.view ?? {};
  reportErrors('connect', errors);
  const recentRequests = (state.requests || []).slice(-10).map(r => `${r.status} ${r.pathname}`).join(', ');
  throw new SmokeFailure(`case clean FAILED — the app never reached Home after Connect (rail cards seen: ${view.railCards ?? 0}; status: ${JSON.stringify(view.status)}; requests: [${recentRequests}])`);
 }
 if (errors.length > 0) {
  reportErrors('connect', errors);
  throw new SmokeFailure(`case clean FAILED — Home render produced ${errors.length} page / console error(s)`);
 }
 const homeView = home.last?.view ?? {};
 const homeFile = await screenshot(session, outputDir, 'clean-02-home.png');
 artifacts.screenshots.push(homeFile);
 artifacts.audits.homeLayout = await evaluate(session, LAYOUT_AUDIT_JS);
 console.log(`case clean: connect PASS — fixture ${appBase} accepted; Home rendered ${homeView.railCards} rail card(s); status ${JSON.stringify(homeView.status)}.`);
 // -- milestone 5: sidebar/content focus moves (menu collapsed -> expanded -> collapsed)
 await pressKey(session, 'ArrowLeft'); // content col0 -> sidebar, menu opens
 const menuOpen = await poll(async () => {
  const view = await evaluate(session, `(function() {
 var app = document.querySelector('.tv-app');
 var active = document.activeElement;
 return { menuOpen: Boolean(app && app.classList.contains('menu-open')), inSidebar: Boolean(active && active.closest && active.closest('.tv-sidebar')) };
})()`);
  return { done: view.menuOpen && view.inSidebar, view };
 }, MILESTONE_TIMEOUT_MS);
 if (!menuOpen.done) {
  throw new SmokeFailure(`case clean FAILED — ArrowLeft from content did not open the menu and focus the sidebar(${JSON.stringify(menuOpen.view)})`);
 }
 const menuFile = await screenshot(session, outputDir, 'clean-03-menu-open.png');
 artifacts.screenshots.push(menuFile);
 await pressKey(session, 'ArrowRight'); // back into content, menu closes
 const menuClosed = await poll(async () => {
  const view = await evaluate(session, `(function() {
 var app = document.querySelector('.tv-app');
 return { menuOpen: Boolean(app && app.classList.contains('menu-open')), activeKey: document.activeElement ? document.activeElement.getAttribute('data-focus-key') : null };
})()`);
  return { done: !view.menuOpen && view.activeKey !== null && !/menu-/.test(view.activeKey || ''), view };
 }, MILESTONE_TIMEOUT_MS);
 if (!menuClosed.done) {
  throw new SmokeFailure(`case clean FAILED — ArrowRight from the sidebar did not return focus to content(${JSON.stringify(menuClosed.view)})`);
 }
 console.log('case clean: focus moves PASS — sidebar/content focus traversal opens and closes the menu without errors.');

 // -- milestone 6: scroll regression — off-right poster + below-fold row
 // Home structure under the fixture: hero (row 1), Continue rail (row 3),
 // Recently added rail (row 10, 12 cards -> off-right), Favorites rail
 // (row 20 -> below the fold).
 await pressKey(session, 'ArrowDown'); // hero -> Continue rail col 0
 let audit = await scrollAudit(session);
 assertCleanViewport(audit, 'rail entry');
 assertFullyVisible(audit, 'Continue rail reveal', 'vertical');

 await pressKey(session, 'ArrowDown'); // Continue -> Recently added col 0
 audit = await scrollAudit(session);
 if (audit.col !== '0') throw new SmokeFailure(`case clean FAILED — expected the first Recently added card after ArrowDown, got col ${audit.col} (${audit.focusKey})`);

 const recentRail = audit.rails.reduce((best, rail) => (rail.scrollWidth - rail.clientWidth > (best ? best.scrollWidth - best.clientWidth : 0) ? rail : best), null);
 if (!recentRail || recentRail.scrollWidth - recentRail.clientWidth <= 0) {
  throw new SmokeFailure('case clean FAILED — fixture error: no poster rail overflows horizontally, the regression scenario is broken');
 }
 const contentTopBaseline = audit.contentScrollTop;
 const unrelatedRailLefts = audit.rails.filter(rail => rail !== recentRail).map(rail => rail.scrollLeft);

 for (let index = 0; index < 6; index++) await pressKey(session, 'ArrowRight');
 audit = await scrollAudit(session);
 assertCleanViewport(audit, 'off-right poster');
 if (audit.col !== '6') {
  throw new SmokeFailure(`case clean FAILED — six ArrowRights from col 0 landed on col ${audit.col}, expected 6(${audit.focusKey})`);
 }
 assertFullyVisible(audit, 'off-right poster reveal', 'horizontal');
 const overflowingRailAfter = audit.rails.find(rail => rail.scrollWidth - rail.clientWidth === recentRail.scrollWidth - recentRail.clientWidth);
 if (!overflowingRailAfter || overflowingRailAfter.scrollLeft <= 0) {
  throw new SmokeFailure(`case clean FAILED — the overflowing rail did not scroll horizontally(scrollLeft ${overflowingRailAfter ? overflowingRailAfter.scrollLeft : 'n/a'}); the off - right poster was not revealed`);
 }
 if (audit.activeRail?.centerDelta !== null && audit.activeRail.centerDelta > 15) {
  throw new SmokeFailure(`case clean FAILED — off-right poster is not centered in the rail (center delta ${audit.activeRail.centerDelta.toFixed(1)}px > 15px; centered horizontal rail movement not honored)`);
 }
 if (audit.contentScrollTop !== contentTopBaseline) {
  throw new SmokeFailure(`case clean FAILED — horizontal rail movement changed the vertical scroll of .tv-content (${contentTopBaseline} -> ${audit.contentScrollTop}); unrelated axis was reset`);
 }

 const railFile = await screenshot(session, outputDir, 'clean-04-rail-revealed.png');
 artifacts.screenshots.push(railFile);

 await pressKey(session, 'ArrowDown'); // Recently added -> Favorites rail (below the fold)
 audit = await scrollAudit(session);
 assertCleanViewport(audit, 'below-fold row');
 assertFullyVisible(audit, 'below-fold row reveal', 'vertical');
 if (!(audit.contentScrollTop > contentTopBaseline)) {
  throw new SmokeFailure(`case clean FAILED — focusing the below-fold Favorites rail did not scroll .tv-content down (${contentTopBaseline} -> ${audit.contentScrollTop})`);
 }
 if (audit.rect.top < 250) {
  throw new SmokeFailure(`case clean FAILED — below-fold row top is at y=${audit.rect.top.toFixed(1)}px (slammed to container top); nearest vertical movement was not honored`);
 }

 const overflowingRailFinal = audit.rails.find(rail => rail.scrollWidth - rail.clientWidth === recentRail.scrollWidth - recentRail.clientWidth);
 if (overflowingRailFinal && overflowingRailFinal.scrollLeft !== overflowingRailAfter.scrollLeft) {
  throw new SmokeFailure(`case clean FAILED — the vertical move reset the overflowing rail's horizontal position (${overflowingRailAfter.scrollLeft} -> ${overflowingRailFinal.scrollLeft})`);
 }
 const belowFoldFile = await screenshot(session, outputDir, 'clean-05-below-fold-revealed.png');
 artifacts.screenshots.push(belowFoldFile);
 console.log(`case clean: scroll regression PASS — off-right poster (col 6) and the below-fold Favorites row were revealed exactly; vertical and horizontal scrollports stayed independent.`);
 // -- milestone 7: webOS playback and seek through the AVPlay seam
 await pressKey(session, 'Enter');
 const detailProbe = await poll(async () => {
  const active = await evaluate(session, `document.activeElement ? document.activeElement.getAttribute('data-focus-key') : null`);
  return { done: active === 'detail-back' || active === 'detail-playback', view: { active } };
 }, MILESTONE_TIMEOUT_MS);
 if (!detailProbe.done) {
  const recent = (state.requests || []).slice(-10).map(r => `${r.status} ${r.pathname}`).join(', ');
  const diag = JSON.stringify(state.diagnosticsPosts || []);
  throw new SmokeFailure(`case clean FAILED — Enter on Favorites card did not focus TitleDetail (${JSON.stringify(detailProbe.last?.view || detailProbe.view)}; requests: [${recent}]; diag: ${diag})`);
 }

 const currentActive = detailProbe.view?.active || detailProbe.last?.view?.active;
 if (currentActive !== 'detail-playback') {
  await pressKey(session, 'ArrowDown');
 }
 const playBtnActive = await poll(async () => {
  const active = await evaluate(session, `document.activeElement ? document.activeElement.getAttribute('data-focus-key') : null`);
  return { done: active === 'detail-playback', view: { active } };
 }, MILESTONE_TIMEOUT_MS);
 if (!playBtnActive.done) {
  throw new SmokeFailure(`case clean FAILED — could not focus detail-playback button (${JSON.stringify(playBtnActive.last?.view || playBtnActive.view)})`);
 }

 await pressKey(session, 'Enter');
 const playbackProbe = await poll(async () => {
  const info = await evaluate(session, `(function() {
   var shell = document.querySelector('.player-shell');
   var video = shell ? shell.querySelector('video') : null;
   var obj = document.getElementById('av-player');
   return {
    hasShell: Boolean(shell),
    hasVideo: Boolean(video),
    objHidden: obj ? obj.style.display === 'none' : false,
    position: video ? video.style.position : null,
    currentTime: video ? video.currentTime : 0,
    duration: video ? video.duration : 0,
    paused: video ? video.paused : true,
   };
  })()`);
  return {
   done: info.hasVideo && info.objHidden && info.currentTime > 0.05,
   view: info,
  };
 }, MILESTONE_TIMEOUT_MS);
 if (!playbackProbe.done) {
  throw new SmokeFailure(`case clean FAILED — media playback never started (${JSON.stringify(playbackProbe.view)})`);
 }

 const playFile = await screenshot(session, outputDir, 'clean-06-playback.png');
 artifacts.screenshots.push(playFile);

 await evaluate(session, `(function() {
  if (window.webapis && window.webapis.avplay) {
   window.webapis.avplay.seekTo(5000);
  }
 })()`);

 const seekProbe = await poll(async () => {
  const info = await evaluate(session, `(function() {
   var video = document.querySelector('.player-shell video');
   return { currentTime: video ? video.currentTime : 0 };
  })()`);
  return {
   done: info.currentTime >= 4.5,
   view: info,
  };
 }, MILESTONE_TIMEOUT_MS);
 if (!seekProbe.done) {
  throw new SmokeFailure(`case clean FAILED — seekTo(5000) did not advance video currentTime (${JSON.stringify(seekProbe.view)})`);
 }

 const seekFile = await screenshot(session, outputDir, 'clean-07-seek.png');
 artifacts.screenshots.push(seekFile);

 await pressKey(session, 'Escape');
 const exitProbe = await poll(async () => {
  const info = await evaluate(session, `(function() {
   var video = document.querySelector('video');
   var shell = document.querySelector('.player-shell');
   var obj = document.getElementById('av-player');
   return {
    hasVideo: Boolean(video),
    hasShell: Boolean(shell),
    objRestored: obj ? obj.style.display !== 'none' : true,
   };
  })()`);
  return {
   done: !info.hasVideo && !info.hasShell,
   view: info,
  };
 }, MILESTONE_TIMEOUT_MS);
 if (!exitProbe.done) {
  throw new SmokeFailure(`case clean FAILED — Escape did not exit player and unmount video (${JSON.stringify(exitProbe.last?.view)})`);
 }

 console.log('case clean: playback and seek PASS — video element mounted in .player-shell, #av-player hidden, direct Range stream played, seekTo advanced currentTime, exit restored shell.');

 if (errors.length > 0) {
  reportErrors('clean', errors);
  throw new SmokeFailure(`case clean FAILED — ${errors.length} page/console error(s) appeared during traversal`);
 }

 // -- case fatal: reload the saved-server boot, inject an uncaught error
 await session.send('Page.navigate', { url: `${appBase}/index.html` });
 await waitForEvent(session, 'Page.loadEventFired', MILESTONE_TIMEOUT_MS);
 const reboot = await bootCompleted(session);
 if (!reboot.done) throw new SmokeFailure('case fatal FAILED — the saved-server reload never reached first render');
 await session.send('Runtime.evaluate', {
  expression: `setTimeout(function () { throw new Error('smoke-webos-engine: injected fatal error'); }, 0);`,
 });
 const shown = await poll(async () => {
  const view = await evaluate(session, `(function () {
    var panel = document.getElementById('fatal-error');
    if (!panel) return null;
    var rect = panel.getBoundingClientRect();
    var style = getComputedStyle(panel);
    return { present: true, role: panel.getAttribute('role'), text: (panel.textContent || '').slice(0, 200), display: style.display, visibility: style.visibility, width: rect.width, height: rect.height };
  })()`);
  const visible = Boolean(view && view.display !== 'none' && view.visibility !== 'hidden' && view.width > 0 && view.height > 0);
  return { done: visible, view };
 }, MILESTONE_TIMEOUT_MS);
 if (!shown.done) {
  reportErrors('fatal', errors);
  throw new SmokeFailure(`case fatal FAILED — the FileListFatalError panel never appeared or is not visible within ${MILESTONE_TIMEOUT_MS / 1000}s after an uncaught error`);
 }
 if (shown.view.role !== 'alert') {
  throw new SmokeFailure(`case fatal FAILED — the panel exists but carries role="${shown.view.role}" instead of "alert"`);
 }
 const reported = await poll(async () => {
  const hit = state.diagnosticsPosts.some(post => post.level === 'error');
  return { done: hit };
 }, DIAGNOSTICS_TIMEOUT_MS);
 if (!reported.done) {
  throw new SmokeFailure(`case fatal FAILED — the panel ("${shown.view.text.slice(0, 80)}…") is visible but no level=error diagnostics POST reached /api/v1/diagnostics/client`);
 }
 const report = state.diagnosticsPosts.find(post => post.level === 'error');
 const fatalFile = await screenshot(session, outputDir, 'fatal-panel.png');
 artifacts.screenshots.push(fatalFile);
 console.log(`case fatal: PASS — FileListFatalError panel is present, visible (role=alert), and reported via POST /api/v1/diagnostics/client (recorded: level=${report.level}, message=${JSON.stringify(String(report.message).slice(0, 80))}).`);
 return artifacts;
}

async function runBroken(state, session, appBase, browser, outputDir) {
 // The temp dist copy has no app.js: the script tag 404s, the bundle global
 // never appears and the startup overlay must stay mounted.
 const evidence = await poll(async () => {
  const view = await evaluate(session, `(function () {
    var startup = document.getElementById('startup');
    return {
      startupGone: !startup,
      startupMessage: (document.getElementById('startup-message') || {}).textContent || null,
      appChildren: (document.getElementById('app') || {}).children ? document.getElementById('app').children.length : -1,
      bundleRan: typeof window.TorrentTV !== 'undefined',
      fatalPanelPresent: Boolean(document.getElementById('fatal-error'))
    };
  })()`);
  const missingRequest = state.requests.find(request => request.pathname === '/app.js' && request.status === 404);
  return { done: Boolean(missingRequest), view, missingRequest };
 }, BROKEN_DETECT_TIMEOUT_MS);

 const firstError = null;
 if (!evidence.done) {
  throw new SmokeFailure(`case broken FAILED — the server never recorded the 404 for the removed app.js within ${BROKEN_DETECT_TIMEOUT_MS / 1000}s; inconclusive`, 2);
 }
 // Give any (unexpected) boot a full window before judging.
 await delay(3_000);
 const final = await evaluate(session, `(function () {
    var startup = document.getElementById('startup');
    return {
      startupGone: !startup,
      appChildren: (document.getElementById('app') || {}).children ? document.getElementById('app').children.length : -1,
      bundleRan: typeof window.TorrentTV !== 'undefined'
    };
  })()`);
 const proof = {
  engine: browser,
  appJsRequest404: evidence.missingRequest,
  startupStillMounted: !final.startupGone,
  appChildren: final.appChildren,
  bundleGlobalAbsent: !final.bundleRan,
 };
 fs.writeFileSync(path.join(outputDir, 'broken-evidence.json'), JSON.stringify(proof, null, 2));
 if (final.startupGone || final.bundleRan) {
  throw new SmokeFailure(`case broken FAILED — the fixture without app.js reached the startup handoff on ${browser}; the harness did not detect the breakage`, 2);
 }
 if (final.appChildren !== 0) {
  throw new SmokeFailure(`case broken FAILED — #app unexpectedly rendered ${final.appChildren} child node(s) without a bundle`, 2);
 }
 console.log(`case broken: rejected as designed on ${browser} — GET /app.js returned 404, #startup still mounted, #app empty, window.TorrentTV absent (evidence: broken-evidence.json).`);
 throw new SmokeFailure('case broken: the smoke contract was violated by the fixture — exit code 3 (detection proven, non-zero by design)', 3);
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main() {
 if (typeof WebSocket !== 'function') {
  throw new SmokeFailure('this smoke needs native WebSocket — use Node >= 22');
 }
 activeState = parseArgs(process.argv.slice(2));
 const state = activeState;

 const distRoot = path.resolve(state.dist);
 if (!fs.existsSync(path.join(distRoot, 'index.html'))) {
  throw new SmokeFailure(`no index.html under ${state.dist} — build the client first (npm run build:webos)`);
 }
 const outputDir = path.resolve(state.output);
 fs.mkdirSync(outputDir, { recursive: true });

 let root = distRoot;
 if (state.cases.includes('broken')) {
  const fixture = fs.mkdtempSync(path.join(os.tmpdir(), 'fls-smoke-webos-broken-'));
  fs.cpSync(distRoot, fixture, { recursive: true });
  fs.rmSync(path.join(fixture, 'app.js')); // the whole bundle is gone, not just corrupted
  state.fixtureDir = fixture;
  root = fixture;
 }

 state.server = createServer(state, root);
 const servePort = await new Promise((resolve, reject) => {
  state.server.once('error', reject);
  state.server.listen(0, '127.0.0.1', () => resolve(state.server.address().port));
 });
 const cdpPort = await freePort();
 if (state.browser) startHostChrome(state, cdpPort);
 else startFloorChrome(state, cdpPort);
 const { browser, userAgent } = await waitCDP(cdpPort);
 if (!state.browser && !/^Chrome\/53\./.test(browser)) {
  throw new SmokeFailure(`the floor leg refused to run — dockerized engine reported "${browser}", but the webOS floor guarantee is Chrome/53.*; do not substitute a newer engine`);
 }

 // The page origin as the ENGINE sees it: inside the container only
 // host.docker.internal routes back to the host's loopback fixture.
 const pageHost = state.browser ? '127.0.0.1' : 'host.docker.internal';
 const appBase = `http://${pageHost}:${servePort}`;
 const engine = {
  mode: state.browser ? 'host' : 'docker53-floor',
  browser,
  userAgent,
  image: state.browser ? null : state.image,
  imageDigest: state.browser ? null : DEFAULT_IMAGE_DIGEST,
  hostBrowserBinary: state.browser,
  viewport: '1920x1080',
  startedAt: new Date().toISOString(),
 };
 fs.writeFileSync(path.join(outputDir, 'engine.json'), JSON.stringify(engine, null, 2));
 console.log(`smoke-webos-engine: serving ${root} on ${appBase} (host loopback fixture; engine reaches it via ${pageHost}); engine ${browser}; ${state.browser ? `host binary ${state.browser}` : `image ${state.image}`}; cases: ${state.cases.join(',')}`);

 const results = {};
 try {
  if (state.cases.includes('broken')) {
   const { session, errors } = await openPage(cdpPort, `${appBase}/index.html`);
   try {
    await runBroken(state, session, appBase, browser, outputDir);
   } finally {
    session.close();
   }
  } else {
   const { session, errors } = await openPage(cdpPort, `${appBase}/index.html`);
   try {
    results.cleanAndFatal = await runCleanAndFatal(state, session, errors, appBase, browser, outputDir);
   } finally {
    session.close();
   }
   console.log('smoke-webos-engine: all requested cases passed on the recorded engine.');
   fs.writeFileSync(path.join(outputDir, 'results.json'), JSON.stringify({ engine, cases: { clean: 'PASS', fatal: 'PASS' }, audits: results.cleanAndFatal?.audits ?? {}, screenshots: results.cleanAndFatal?.screenshots ?? [] }, null, 2));
  }
 } finally {
  cleanup(state);
 }
}
let activeState = null;
for (const signal of ['SIGINT', 'SIGTERM']) {
 process.on(signal, () => {
  if (activeState) cleanup(activeState);
  process.exit(1);
 });
}
main().catch(error => {
 if (activeState) cleanup(activeState);
 console.error(`smoke-webos-engine: ${error?.message || error}`);
 process.exit(error instanceof SmokeFailure ? error.code : 1);
});
