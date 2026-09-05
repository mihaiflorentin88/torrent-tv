/**
 * Ticket #80: the fatal error panel is classic ES5 shipped verbatim, so these
 * tests execute the real script bytes from disk via indirect eval instead of
 * importing a module. The happy-dom pragma gives that eval a real
 * window/document/fetch surface to run against.
 * @vitest-environment happy-dom
 */
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Window } from 'happy-dom';

// The tizen suite always runs from clients/tizen, so this resolves to the
// shipped file on disk — real bytes, not a bundled import. The script lives
// at the package root beside startup.js (src/*.js is gitignored as transpile
// output) and is copied verbatim into dist.
const source = readFileSync(resolve(process.cwd(), 'fatal-error.js'), 'utf8');

interface FatalErrorOptions {
 endpoint?: string;
 onExit?: () => void;
}

interface FatalErrorModule {
 install(options?: FatalErrorOptions): void;
}

type FetchInit = { method: string; headers: Record<string, string>; body: string };

// Indirect eval runs the shipped bytes in global scope, like the script tag in
// index.html does. Re-evaluating supersedes the previous wiring (the script
// releases its earlier listeners), so every test gets a fresh panel.
function loadScript(): FatalErrorModule {
 (0, eval)(source);
 // The eval'd script publishes window.FileListFatalError; the shipped
 // globals.d.ts (not ours to edit) only declares FileListBoot.
 const boot = window as unknown as { FileListFatalError?: FatalErrorModule };
 const wired = boot.FileListFatalError;
 if (!wired) throw new Error('FileListFatalError missing after running the shipped bytes');
 return wired;
}

function pressBack(key = 'Back', keyCode = 10009): void {
 const event = new KeyboardEvent('keydown', { key });
 Object.defineProperty(event, 'keyCode', { value: keyCode });
 document.dispatchEvent(event);
}

const panel = () => document.getElementById('fatal-error');

afterEach(() => {
 vi.unstubAllGlobals();
 document.body.innerHTML = '';
 window.tizen = undefined;
});

describe('fatal error panel (shipped ES5 bytes)', () => {
 it('is classic ES5: no arrows, no template literals', () => {
  expect(source).not.toMatch(/=>/);
  expect(source).not.toMatch(/`/);
 });

 it('exposes install() once the script bytes have run', () => {
  expect(typeof loadScript().install).toBe('function');
 });

 it('paints an escaped full-screen panel on the first error and reports once', () => {
  const fetchMock = vi.fn<(url: string, init: FetchInit) => void>();
  vi.stubGlobal('fetch', fetchMock);
  loadScript().install({ endpoint: '/api/v1/diagnostics/client' });

  window.dispatchEvent(new ErrorEvent('error', {
   message: 'Boom <img src=x onerror=alert(1)>',
   filename: 'app.js',
   lineno: 42,
   colno: 7,
  }));

  expect(panel()).not.toBeNull();
  expect(panel()!.style.position).toBe('fixed');
  expect(panel()!.textContent).toContain('<img src=x onerror=alert(1)>');
  expect(panel()!.querySelector('img')).toBeNull();
  expect(panel()!.textContent).toContain('household admin');
  expect(panel()!.textContent).toContain('Back');

  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0];
  expect(url).toBe('/api/v1/diagnostics/client');
  expect(init.method).toBe('POST');
  expect(init.headers['Content-Type']).toBe('application/json');
  expect(JSON.parse(init.body)).toEqual({
   level: 'error',
   message: 'Boom <img src=x onerror=alert(1)>',
   context: { source: 'app.js', line: 42, column: 7 },
  });

  window.dispatchEvent(new ErrorEvent('error', { message: 'Second <b>crash</b>' }));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(document.querySelectorAll('#fatal-error')).toHaveLength(1);
  expect(panel()!.textContent).toContain('<img src=x onerror=alert(1)>');
 });

 it('truncates the reported message to the server cap of 1000 characters', () => {
  const fetchMock = vi.fn<(url: string, init: FetchInit) => void>();
  vi.stubGlobal('fetch', fetchMock);
  loadScript().install({ endpoint: '/api/v1/diagnostics/client' });

  window.dispatchEvent(new ErrorEvent('error', { message: 'x'.repeat(5000) }));

  expect(fetchMock).toHaveBeenCalledTimes(1);
  const body = JSON.parse(fetchMock.mock.calls[0][1].body);
  expect(body.level).toBe('error');
  expect(body.message.length).toBeLessThanOrEqual(1000);
 });

 it('treats an unhandled rejection as the fatal error too', () => {
  const fetchMock = vi.fn<(url: string, init: FetchInit) => void>();
  vi.stubGlobal('fetch', fetchMock);
  loadScript().install({ endpoint: '/api/v1/diagnostics/client' });

  const event = new Event('unhandledrejection');
  Object.defineProperty(event, 'reason', { value: new Error('Rejected <script>') });
  window.dispatchEvent(event);

  expect(panel()!.textContent).toContain('Rejected <script>');
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const body = JSON.parse(fetchMock.mock.calls[0][1].body);
  expect(body.level).toBe('error');
  expect(body.message).toBe('Rejected <script>');
 });

 it('keeps the panel up and never duplicates when reporting fails', () => {
  // The rejection is handled the moment the script attaches .catch, so no
  // waiting is needed: an unhandled rejection here fails the suite outright.
  const fetchMock = vi.fn<(url: string, init: FetchInit) => Promise<never>>().mockRejectedValue(new Error('network down'));
  vi.stubGlobal('fetch', fetchMock);
  loadScript().install({ endpoint: '/api/v1/diagnostics/client' });

  window.dispatchEvent(new ErrorEvent('error', { message: 'Offline crash' }));
  expect(panel()!.textContent).toContain('Offline crash');

  window.dispatchEvent(new ErrorEvent('error', { message: 'Second while offline' }));
  expect(fetchMock).toHaveBeenCalledTimes(1);
 });

 it('still shows the panel when fetch is missing', () => {
  vi.stubGlobal('fetch', undefined);
  loadScript().install({ endpoint: '/api/v1/diagnostics/client' });

  expect(() => window.dispatchEvent(new ErrorEvent('error', { message: 'No fetch here' }))).not.toThrow();
  expect(panel()!.textContent).toContain('No fetch here');
 });

 it('exits through the Tizen application API when Back is pressed after the panel shows', () => {
  vi.stubGlobal('fetch', vi.fn());
  const exit = vi.fn();
  window.tizen = { application: { getCurrentApplication: () => ({ exit }) } };
  loadScript().install({ endpoint: '/api/v1/diagnostics/client' });

  pressBack();
  expect(exit).not.toHaveBeenCalled();

  window.dispatchEvent(new ErrorEvent('error', { message: 'crashed' }));
  pressBack();
  expect(exit).toHaveBeenCalledTimes(1);
  pressBack('Return', 13);
  // 'Return' is Select in the app's normalization; only Back exits.
  expect(exit).toHaveBeenCalledTimes(1);
 });

 it('prefers the onExit escape hatch over the Tizen API', () => {
  vi.stubGlobal('fetch', vi.fn());
  const onExit = vi.fn();
  loadScript().install({ endpoint: '/api/v1/diagnostics/client', onExit });

  window.dispatchEvent(new ErrorEvent('error', { message: 'crashed' }));
  pressBack();
  expect(onExit).toHaveBeenCalledTimes(1);
 });

 it('reports to the discovered server root when the endpoint is a path', () => {
  const fetchMock = vi.fn<(url: string, init: FetchInit) => void>();
  vi.stubGlobal('fetch', fetchMock);
  // Node's experimental localStorage global shadows happy-dom's in this
  // environment, so hand the script a real happy-dom Storage to read.
  vi.stubGlobal('localStorage', new Window().localStorage);
  window.localStorage.setItem('filelist.serverUrl', 'http://192.168.50.2:8097/');
  loadScript().install({ endpoint: '/api/v1/diagnostics/client' });

  window.dispatchEvent(new ErrorEvent('error', { message: 'crashed' }));

  expect(fetchMock.mock.calls[0][0]).toBe('http://192.168.50.2:8097/api/v1/diagnostics/client');
 });
});
