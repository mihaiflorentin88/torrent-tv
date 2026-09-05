import { render } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetPortal, seedServerState } from '../lib/state';
import { ServerPage } from './ServerPage';

// Bindings are stubbed at the module boundary; the Wails event API stays
// silent and each test seeds the lifecycle state it needs.
const fakeBindings = vi.hoisted(() => ({
  serverState: vi.fn(),
  startServer: vi.fn(),
  stopServer: vi.fn(),
  restartServer: vi.fn(),
  loadSettings: vi.fn(),
  settingsSchema: vi.fn(),
  missingRequired: vi.fn(),
  version: vi.fn(),
  autostartStatus: vi.fn(),
  enableAutostart: vi.fn(),
  disableAutostart: vi.fn(),
  dataDirInfo: vi.fn(),
  openPath: vi.fn(),
  openWebUI: vi.fn(),
  readLogs: vi.fn(),
}));

// The generated bindings module exports one function per Go method; the
// factory maps them onto the same camelCase fakes the assertions use.
vi.mock('../bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings', () => ({
  AutostartStatus: fakeBindings.autostartStatus,
  DataDirInfo: fakeBindings.dataDirInfo,
  DisableAutostart: fakeBindings.disableAutostart,
  EnableAutostart: fakeBindings.enableAutostart,
  LoadSettings: fakeBindings.loadSettings,
  OpenPath: fakeBindings.openPath,
  OpenWebUI: fakeBindings.openWebUI,
  ReadLogs: fakeBindings.readLogs,
  StartServer: fakeBindings.startServer,
  StopServer: fakeBindings.stopServer,
  Version: fakeBindings.version,
}));

vi.mock('@wailsio/runtime', () => ({
  Events: { On: () => () => { } },
}));

const fakeApi = vi.hoisted(() => ({
  call: vi.fn(),
  portalState: vi.fn(),
  updatesCurrent: vi.fn(),
  portalMe: vi.fn(),
}));

vi.mock('@torrent-tv/web/shared-api', () => ({
  configureSharedApi: () => { },
  sharedApi: () => fakeApi,
}));

// The shell's portal engine opens an SSE stream per origin; these tests
// never drive it.
class FakeEventSource {
  constructor(public url: string) { }
  addEventListener() { }
  close() { }
}

const mountedHosts: HTMLElement[] = [];

async function mount(): Promise<HTMLElement> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  mountedHosts.push(host);
  await act(async () => { render(<ServerPage />, host) });
  // The portal engine's recovery refetch resolves a few microtasks deep;
  // drain several rounds so every settled render lands before assertions.
  // Plain microtask rounds only — the log-viewer tests run fake timers.
  for (let i = 0; i < 5; i++) {
    await act(async () => { await Promise.resolve() });
  }
  return host;
}

const button = (label: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.trim() === label)!;

beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource);
  seedServerState({ state: 'stopped' });
  fakeBindings.version.mockResolvedValue('v0.1.2');
  fakeBindings.dataDirInfo.mockResolvedValue(['/opt/fs/data', 'pointer']);
  fakeBindings.loadSettings.mockResolvedValue({ settingsPath: '/opt/fs/data/settings.json' });
  fakeBindings.autostartStatus.mockResolvedValue(false);
  fakeApi.call.mockReset();
  fakeApi.portalState.mockReset();
  fakeApi.portalState.mockRejectedValue(new Error('portal routes absent'));
  fakeApi.updatesCurrent.mockReset();
  fakeApi.updatesCurrent.mockRejectedValue(new Error('absent'));
  fakeApi.portalMe.mockReset();
  fakeApi.portalMe.mockRejectedValue(Object.assign(new Error('no session'), { status: 401 }));
  resetPortal();
});

afterEach(() => {
  for (const host of mountedHosts) { render(null, host); host.remove() }
  mountedHosts.length = 0;
  document.body.innerHTML = '';
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  resetPortal();
});

describe('ServerPage status card', () => {
  it('shows Stopped with a working Start and no Stop while stopped', async () => {
    const host = await mount();
    expect(host.textContent).toContain('Stopped');
    expect(button('Start server').disabled).toBe(false);
    expect(button('Stop server')).toBeUndefined();
    await act(async () => { button('Start server').click() });
    expect(fakeBindings.startServer).toHaveBeenCalled();
    expect(fakeBindings.stopServer).not.toHaveBeenCalled();
  });

  it('shows the failure with its error', async () => {
    seedServerState({ state: 'failed', error: 'bind: address in use' });
    const host = await mount();
    expect(host.textContent).toContain('Failed — bind: address in use');
    expect(button('Start server').disabled).toBe(false);
  });

  it('disables the server button while the server is starting or stopping', async () => {
    seedServerState({ state: 'starting', address: '127.0.0.1:8097' });
    const starting = await mount();
    expect(button('Stop server').disabled).toBe(true);
    render(null, starting);
    starting.remove();
    seedServerState({ state: 'stopping', address: '127.0.0.1:8097' });
    const stopping = await mount();
    expect(button('Start server').disabled).toBe(true);
    render(null, stopping);
    stopping.remove();
  });

  it('shows the running address and stops via the running server', async () => {
    seedServerState({ state: 'running', address: '127.0.0.1:8097' });
    const host = await mount();
    expect(host.textContent).toContain('Running on http://127.0.0.1:8097');
    expect(button('Stop server').disabled).toBe(false);
    expect(button('Start server')).toBeUndefined();
    await act(async () => { button('Stop server').click() });
    expect(fakeBindings.stopServer).toHaveBeenCalled();
    await act(async () => { button('Open web UI').click() });
    expect(fakeBindings.openWebUI).toHaveBeenCalled();
  });

  it('surfaces binding errors inline', async () => {
    fakeBindings.startServer.mockRejectedValue(new Error('required settings missing: fileListPasskey'));
    const host = await mount();
    await act(async () => { button('Start server').click() });
    await act(async () => { });
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('required settings missing');
  });
});

describe('ServerPage autostart card', () => {
  it('reflects the OS read-back after toggling, never an optimistic flip', async () => {
    fakeBindings.autostartStatus.mockResolvedValueOnce(false) // initial read
      .mockResolvedValueOnce(true); // read-back after enable
    const host = await mount();
    const toggle = host.querySelector<HTMLInputElement>('.switch-field input')!;
    expect(toggle.checked).toBe(false);
    await act(async () => { toggle.click() });
    expect(fakeBindings.enableAutostart).toHaveBeenCalled();
    expect(fakeBindings.autostartStatus).toHaveBeenCalledTimes(2);
    expect(toggle.checked).toBe(true);
  });

  it('keeps the switch unchecked and shows the error when enabling fails', async () => {
    fakeBindings.autostartStatus.mockResolvedValueOnce(false);
    fakeBindings.enableAutostart.mockRejectedValue(new Error('launchd refused'));
    const host = await mount();
    const toggle = host.querySelector<HTMLInputElement>('.switch-field input')!;
    await act(async () => { toggle.click() });
    await act(async () => { });
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('launchd refused');
    expect(toggle.checked).toBe(false);
  });

  it('disables the toggle until the OS state is read back', async () => {
    fakeBindings.autostartStatus.mockReturnValue(new Promise(() => { }));
    const host = await mount();
    expect(host.querySelector<HTMLInputElement>('.switch-field input')!.disabled).toBe(true);
  });
});

describe('ServerPage details row', () => {
  it('renders version, settings path, and data folder with reveal actions', async () => {
    const host = await mount();
    expect(host.textContent).toContain('Version v0.1.2');
    expect(host.textContent).toContain('/opt/fs/data/settings.json');
    expect(host.textContent).toContain('/opt/fs/data');
    expect(host.textContent).toContain('(from pointer)');
    await act(async () => { button('Open logs folder').click() });
    expect(fakeBindings.openPath).toHaveBeenLastCalledWith('logs');
    await act(async () => { button('Open data folder').click() });
    expect(fakeBindings.openPath).toHaveBeenLastCalledWith('data');
  });
});

// Update state reaches the Server page from the shared portal surface. The
// line only exists while THIS embedded server is running — a stopped (or
// restarting) server must not show availability it cannot act on, even if
// a status from the previous session is still mirrored.
describe('ServerPage update state', () => {
  it('shows the available version on the running server', async () => {
    seedServerState({ state: 'running', address: '127.0.0.1:8097' });
    fakeApi.portalState.mockResolvedValue({ accountsEnabled: false, adsEnabled: false, donor: false, links: [] });
    fakeApi.updatesCurrent.mockResolvedValue({ currentVersion: '1.2.3', available: true, latest: '1.3.0', releasesUrl: 'https://example.invalid/releases', selfUpdate: true, applying: false });
    const host = await mount();
    const line = host.querySelector('.update-state');
    expect(line).not.toBeNull();
    expect(line?.textContent).toContain('Update available: version 1.3.0');
  });

  it('shows no update line while the server is stopped, even with a mirrored status', async () => {
    fakeApi.portalState.mockResolvedValue({ accountsEnabled: false, adsEnabled: false, donor: false, links: [] });
    fakeApi.updatesCurrent.mockResolvedValue({ currentVersion: '1.2.3', available: true, latest: '1.3.0', releasesUrl: 'https://example.invalid/releases', selfUpdate: true, applying: false });
    const host = await mount();
    expect(host.querySelector('.update-state')).toBeNull();
  });

  it('shows no update line when the server runs and nothing is available', async () => {
    seedServerState({ state: 'running', address: '127.0.0.1:8097' });
    fakeApi.portalState.mockResolvedValue({ accountsEnabled: false, adsEnabled: false, donor: false, links: [] });
    fakeApi.updatesCurrent.mockResolvedValue({ currentVersion: '1.2.3', available: false, releasesUrl: '', selfUpdate: true, applying: false });
    const host = await mount();
    expect(host.querySelector('.update-state')).toBeNull();
  });
});


describe('ServerPage log viewer', () => {
  beforeEach(() => { vi.useFakeTimers() });
  afterEach(() => { vi.useRealTimers() });

  it('toggles the panel, renders JSONL as time LEVEL message, and appends new polls', async () => {
    fakeBindings.readLogs.mockResolvedValue({
      lines: ['{"time":"2026-09-05T10:00:00.000Z","level":"INFO","msg":"server started"}', 'not json at all'],
      nextOffset: 82,
      size: 82,
    });
    const host = await mount();
    expect(host.querySelector('.log-view')).toBeNull();
    await act(async () => { button('View logs').click() });
    await act(async () => { });
    expect(fakeBindings.readLogs).toHaveBeenCalledWith(0);
    expect(host.querySelector('.log-view')!.textContent).toContain('2026-09-05T10:00:00.000Z INFO server started');
    expect(host.querySelector('.log-view')!.textContent).toContain('not json at all');

    fakeBindings.readLogs.mockResolvedValue({
      lines: ['{"time":"2026-09-05T10:00:01.500Z","level":"WARN","msg":"appended"}'],
      nextOffset: 130,
      size: 130,
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(1500) });
    expect(fakeBindings.readLogs).toHaveBeenLastCalledWith(82);
    expect(host.querySelector('.log-view')!.textContent).toContain('appended');

    // Closing the panel stops the polling.
    await act(async () => { button('View logs').click() });
    const calls = fakeBindings.readLogs.mock.calls.length;
    await act(async () => { await vi.advanceTimersByTimeAsync(4500) });
    expect(fakeBindings.readLogs.mock.calls.length).toBe(calls);
  });

  it('surfaces a failed poll as a visible alert and clears it on the next success', async () => {
    fakeBindings.readLogs.mockRejectedValue(new Error('data directory is not resolvable yet'));
    const host = await mount();
    await act(async () => { button('View logs').click() });
    await act(async () => { });
    expect(host.querySelector('[role="alert"]')!.textContent).toContain('Log read failed: data directory is not resolvable yet');
    expect(host.querySelector('.log-view')!.textContent).toContain('No log lines yet.');
    fakeBindings.readLogs.mockResolvedValue({ lines: ['{"time":"t1","level":"INFO","msg":"one"}'], nextOffset: 30, size: 30 });
    await act(async () => { await vi.advanceTimersByTimeAsync(1500) });
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(host.querySelector('.log-view')!.textContent).toContain('one');
  });

  it('renders http access records pretty and keeps the generic fallback', async () => {
    fakeBindings.readLogs.mockResolvedValue({
      lines: [
        '{"time":"2026-09-05T10:00:02.000Z","level":"DEBUG","msg":"http request","method":"GET","path":"/api/v1/jobs","status":200,"durationMs":12}',
        '{"time":"2026-09-05T10:00:03.000Z","level":"INFO","msg":"http request"}',
        'still not json',
      ],
      nextOffset: 220,
      size: 220,
    });
    const host = await mount();
    await act(async () => { button('View logs').click() });
    await act(async () => { });
    const view = host.querySelector('.log-view')!.textContent!;
    expect(view).toContain('2026-09-05T10:00:02.000Z DEBUG GET /api/v1/jobs 200 12ms');
    // An access record missing its attributes keeps the generic shape.
    expect(view).toContain('2026-09-05T10:00:03.000Z INFO http request');
    expect(view).toContain('still not json');
  });

  it('pauses the tail on scroll-up and resumes at the bottom', async () => {
    fakeBindings.readLogs.mockResolvedValue({
      lines: ['{"time":"t1","level":"INFO","msg":"one"}'],
      nextOffset: 30,
      size: 30,
    });
    const host = await mount();
    await act(async () => { button('View logs').click() });
    const view = host.querySelector<HTMLElement>('.log-view')!;
    Object.defineProperty(view, 'scrollHeight', { value: 1000, configurable: true });
    Object.defineProperty(view, 'clientHeight', { value: 100, configurable: true });

    // Scrolled up (top of 1000px of scrollable content): the tail must
    // append without moving the view.
    view.scrollTop = 0;
    view.dispatchEvent(new Event('scroll'));
    fakeBindings.readLogs.mockResolvedValue({
      lines: ['{"time":"t2","level":"INFO","msg":"two"}'],
      nextOffset: 60,
      size: 60,
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(1500) });
    expect(view.textContent).toContain('two');
    expect(view.scrollTop).toBe(0);

    // Back at the bottom edge: the tail re-arms and follows the next line.
    view.scrollTop = 900;
    view.dispatchEvent(new Event('scroll'));
    await act(async () => { await vi.advanceTimersByTimeAsync(1500) });
    expect(view.scrollTop).toBe(1000);
  });
});