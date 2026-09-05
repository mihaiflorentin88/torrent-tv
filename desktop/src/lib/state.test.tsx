import { render } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { App } from '../App';
import { configureSharedApi } from '@torrent-tv/web/shared-api';
import { portalSessionKey, savePortalSession, type PortalState, type UpdateStatus } from '@torrent-tv/shared';
import { resetPortal, seedServerState, setServerOrigin, sharedOrigin, usePortal, useServerState } from './state';

// The Wails runtime is mocked at the module boundary: the fake records
// 'server:state' subscriptions so tests can emit lifecycle events the way
// the Task 6 runner will.
const fakeEvents = vi.hoisted(() => {
  const subscribers = new Map<string, Array<(event: { data: unknown }) => void>>();
  return {
    subscribers,
    emit(name: string, data: unknown) {
      for (const handler of subscribers.get(name) ?? []) handler({ data });
    },
    reset() { subscribers.clear() },
  };
});

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: (name: string, handler: (event: { data: unknown }) => void) => {
      const handlers = fakeEvents.subscribers.get(name) ?? [];
      handlers.push(handler);
      fakeEvents.subscribers.set(name, handlers);
      return () => { fakeEvents.subscribers.set(name, handlers.filter(item => item !== handler)) };
    },
  },
}));

// Server is the landing view, so App now mounts ServerPage on boot: the
// generated bindings module is stubbed inert so the shell tests stay
// hermetic. OpenURL is a recorded fake: portal and project links must
// reach the native binding, never a real browser.
const fakeBindings = vi.hoisted(() => ({
  openURL: vi.fn(),
}));

vi.mock('../bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings', () => ({
  AutostartStatus: vi.fn().mockResolvedValue(false),
  DataDirInfo: vi.fn().mockResolvedValue(['', '']),
  DisableAutostart: vi.fn(),
  EnableAutostart: vi.fn(),
  LoadSettings: vi.fn().mockResolvedValue({ settingsPath: '' }),
  MissingRequired: vi.fn().mockResolvedValue([]),
  OpenPath: vi.fn(),
  OpenURL: fakeBindings.openURL,
  OpenWebUI: vi.fn(),
  ReadLogs: vi.fn().mockResolvedValue({ lines: [], nextOffset: 0, size: 0 }),
  SaveSettings: vi.fn().mockResolvedValue({ saved: true, restartRequired: false, autoStarted: false }),
  ServerState: vi.fn().mockRejectedValue(new Error('offline')),
  SettingsSchema: vi.fn().mockResolvedValue([]),
  StartServer: vi.fn(),
  StopServer: vi.fn(),
  Version: vi.fn().mockResolvedValue(''),
}));

const fakeApi = vi.hoisted(() => ({
  call: vi.fn(),
  portalState: vi.fn(),
  portalPromotions: vi.fn().mockResolvedValue([]),
  updatesCurrent: vi.fn(),
  updatesCheck: vi.fn(),
  updatesApply: vi.fn(),
  portalMe: vi.fn(),
  portalSession: vi.fn(),
}));

vi.mock('@torrent-tv/web/shared-api', () => ({
  configureSharedApi: vi.fn(),
  sharedApi: () => fakeApi,
}));

// SSE arrives through a capturing EventSource fake (same harness as the
// web portal suite): tests emit envelope frames and the app under test
// parses them, and origin rebuilds must close the old stream.
class CapturingEventSource {
  static instances: CapturingEventSource[] = [];
  lastEventId = '';
  closed = false;
  private handlers = new Map<string, EventListener>();
  constructor(public url: string) { CapturingEventSource.instances.push(this) }
  addEventListener(type: string, listener: EventListener) { this.handlers.set(type, listener) }
  close() { this.closed = true }
  emit(type: string, data?: string) {
    if (!data) { this.handlers.get(type)?.(new Event(type)); return }
    this.handlers.get(type)?.(new MessageEvent(type, { data, lastEventId: this.lastEventId }));
  }
}

const stream = () => CapturingEventSource.instances.at(-1)!;
const envelope = (kind: string, payload: unknown, id: number) => JSON.stringify({ id, kind, payload: JSON.stringify(payload), createdAt: '2026-09-05T00:00:00Z' });

// The webview's localStorage is stubbed: portal sessions are stored scoped
// to the configured origin, and origin changes must clear them.
const localStorageStub = (() => { const data = new Map<string, string>(); return { data, getItem: (key: string) => data.get(key) ?? null, setItem: (key: string, value: string) => void data.set(key, value), removeItem: (key: string) => void data.delete(key), clear: () => void data.clear() } })();

const enabledSnapshot: PortalState = { accountsEnabled: true, adsEnabled: false, donor: false, links: [{ id: 1, title: 'Other tool', url: 'https://example.invalid/tool', description: 'A project' }] };
const meUser = { id: 3, email: 'a@b.c', display_name: 'Ada', role: 'member' };
const updateStatus: UpdateStatus = { currentVersion: '1.2.3', available: true, latest: '1.3.0', notes: 'Many fixes.', releasedAt: '2026-09-01T00:00:00Z', releasesUrl: 'https://example.invalid/releases', selfUpdate: true, applying: false };

function StateProbe() {
  const server = useServerState();
  return <p>{server.state}{server.address ? ` · ${server.address}` : ''}</p>;
}

// PortalProbe: renders the shared surface so singleton engine tests can
// assert snapshot, identity, and connection without mounting whole pages.
function PortalProbe() {
  const portal = usePortal();
  return <p data-identity={portal.identity?.email ?? ''}>{portal.snapshot ? `links:${portal.snapshot.links.length}` : 'no-snapshot'}:{portal.status ? portal.status.currentVersion : 'no-status'}:{portal.connected ? 'connected' : 'disconnected'}</p>;
}

const mountedHosts: HTMLElement[] = [];

async function mount(ui: Parameters<typeof render>[0]): Promise<HTMLElement> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  mountedHosts.push(host);
  // Render inside act so the hook's subscribe effect runs before tests
  // emit — matching the web client's test convention.
  await act(async () => { render(ui, host) });
  return host;
}

async function settle(rounds = 5): Promise<void> {
  for (let i = 0; i < rounds; i++) {
    await act(async () => {
      const { promise, resolve } = Promise.withResolvers<void>();
      setTimeout(resolve, 0);
      await promise;
    });
  }
}

beforeEach(() => {
  vi.stubGlobal('EventSource', CapturingEventSource);
  vi.stubGlobal('localStorage', localStorageStub);
  localStorageStub.data.clear();
  CapturingEventSource.instances.length = 0;
  seedServerState({ state: 'stopped' });
  fakeApi.call.mockReset();
  fakeApi.call.mockResolvedValue({});
  fakeApi.portalState.mockReset();
  fakeApi.portalState.mockResolvedValue(enabledSnapshot);
  fakeApi.updatesCurrent.mockReset();
  fakeApi.updatesCurrent.mockResolvedValue(updateStatus);
  fakeApi.portalMe.mockReset();
  fakeApi.portalMe.mockResolvedValue(meUser);
  fakeBindings.openURL.mockReset();
  fakeBindings.openURL.mockResolvedValue(undefined);
  resetPortal();
});

afterEach(() => {
  for (const host of mountedHosts) { render(null, host); host.remove() }
  mountedHosts.length = 0;
  document.body.innerHTML = '';
  fakeEvents.reset();
  localStorageStub.data.clear();
  CapturingEventSource.instances.length = 0;
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  resetPortal();
});

describe('useServerState', () => {
  it('seeds stopped before the runner emits anything', async () => {
    const host = await mount(<StateProbe />);
    expect(host.textContent).toBe('stopped');
  });

  it('updates on server:state events from the runner', async () => {
    const host = await mount(<StateProbe />);
    await act(async () => { fakeEvents.emit('server:state', { state: 'running', address: '127.0.0.1:8097' }) });
    expect(host.textContent).toBe('running · 127.0.0.1:8097');
    await act(async () => { fakeEvents.emit('server:state', { state: 'failed', error: 'boom' }) });
    expect(host.textContent).toBe('failed');
  });

  it('ignores payloads without a known state', async () => {
    const host = await mount(<StateProbe />);
    await act(async () => { fakeEvents.emit('server:state', { state: 'bogus' }) });
    expect(host.textContent).toBe('stopped');
  });

  it('initializes late mounts from the latest emitted state', async () => {
    const first = await mount(<StateProbe />);
    await act(async () => { fakeEvents.emit('server:state', { state: 'running', address: '127.0.0.1:8097' }) });
    expect(first.textContent).toBe('running · 127.0.0.1:8097');
    // A second component mounted after the emit (no new emit) must not
    // resurrect the boot seed — JobsPage gating depends on this.
    const second = await mount(<StateProbe />);
    expect(second.textContent).toBe('running · 127.0.0.1:8097');
  });
});

describe('shell chrome', () => {
  it('seeds the status pill from the boot state and tracks emits', async () => {
    seedServerState({ state: 'starting' });
    const host = await mount(<App />);
    const pill = host.querySelector('.pill');
    expect(pill?.className).toContain('pill-starting');
    expect(pill?.textContent).toBe('Starting');
    await act(async () => { fakeEvents.emit('server:state', { state: 'running', address: '127.0.0.1:8097' }) });
    expect(host.querySelector('.pill')?.textContent).toBe('Running · 127.0.0.1:8097');
    expect(host.querySelectorAll('.dot-running').length).toBeGreaterThanOrEqual(3);
  });

  it('carries exactly the shipped sections; Server first, Task 10 appended Settings', async () => {
    const host = await mount(<App />);
    const labels = Array.from(host.querySelectorAll('.shell-nav button')).map(button => button.textContent?.trim());
    expect(labels).toEqual(['Server', 'Downloads', 'Jobs', 'Settings']);
  });
});

// Boot wiring lives in main.tsx and must be exercised INSIDE a test, with
// the #app host already in place: a static import would run main.tsx's
// module body at file load, before any test controls the DOM — so this
// dynamic import is the boundary under test (deliberate module-load
// coverage, not a runtime-resolved module).
// What it pins: the shared API points at the app origin before the first
// render, and lifecycle events never re-point it — the running event's
// loopback address is display-only, and using it as an API origin would
// cross origins from the wails:// webview and be blocked.
describe('boot wiring', () => {
  it('sets the shared API origin once at boot, to the app origin', async () => {
    const app = document.createElement('div');
    app.id = 'app';
    document.body.appendChild(app);
    await import('../main');
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, 0);
    await act(async () => { await promise });

    expect(configureSharedApi).toHaveBeenCalledWith(location.origin);
    expect(configureSharedApi).toHaveBeenCalledTimes(1);

    await act(async () => { fakeEvents.emit('server:state', { state: 'running', address: '127.0.0.1:8097' }) });
    expect(configureSharedApi).toHaveBeenCalledTimes(1);
  });
});

// Origin changes re-route the whole portal surface: the stream for the old
// origin is closed (no stale subscription may survive a server moving to a
// new address), a fresh stream and sync are built against the new origin,
// and the stored session — scoped to the old origin — is cleared so a
// token issued by one server is never replayed against another.
describe('portal origin change', () => {
  it('closes the old stream and rebuilds subscriptions against the new origin', async () => {
    const host = await mount(<PortalProbe />);
    await settle();
    expect(stream().url).toBe('http://localhost:3000/api/v1/events');
    expect(host.textContent).toContain('links:1');

    const oldStream = stream();
    await act(async () => { setServerOrigin('http://127.0.0.1:9999') });
    await settle();

    expect(oldStream.closed).toBe(true);
    expect(CapturingEventSource.instances).toHaveLength(2);
    expect(stream().url).toBe('http://127.0.0.1:9999/api/v1/events');
    expect(sharedOrigin()).toBe('http://127.0.0.1:9999');
    expect(configureSharedApi).toHaveBeenCalledWith('http://127.0.0.1:9999');
    // The rebuilt surface follows the NEW stream only: its recovered
    // snapshot comes from the new origin's client, and a fresh event on
    // the new stream replaces it.
    await act(async () => { stream().emit('portal.state', envelope('portal.state', { accountsEnabled: false, adsEnabled: false, donor: false, links: [] }, 1)) });
    expect(host.textContent).toContain('links:0');
  });

  it('clears the stored session and identity when the origin changes', async () => {
    savePortalSession(localStorageStub, 'http://localhost:3000', { token: 'token-old', expires_at: '2027-01-01T00:00:00Z' });
    const host = await mount(<PortalProbe />);
    await settle();
    // The stored token proved itself against the old origin's /session/me.
    expect(host.querySelector('[data-identity]')?.getAttribute('data-identity')).toBe('a@b.c');

    await act(async () => { setServerOrigin('http://127.0.0.1:9999') });
    await settle();

    expect(localStorageStub.data.has(portalSessionKey('http://localhost:3000'))).toBe(false);
    expect(host.querySelector('[data-identity]')?.getAttribute('data-identity')).toBe('');
  });

  it('keeps subscriptions and session when the origin is re-set to the same value', async () => {
    savePortalSession(localStorageStub, 'http://localhost:3000', { token: 'token-old', expires_at: '2027-01-01T00:00:00Z' });
    await mount(<PortalProbe />);
    await settle();
    await act(async () => { setServerOrigin('http://localhost:3000') });
    await settle();
    expect(CapturingEventSource.instances).toHaveLength(1);
    expect(localStorageStub.data.has(portalSessionKey('http://localhost:3000'))).toBe(true);
  });
});

// A stored token that fails revalidation keeps or loses its session by
// failure kind: only a 401 (session invalid) clears it — a transient
// network failure at boot must not silently sign the household out.
describe.each([
  ["network failure keeps the stored session", Object.assign(new Error("connect refused"), { status: 503 }), true],
  ["plain network failure keeps the stored session", new Error("connect refused"), true],
  ["401 clears the stored session", Object.assign(new Error("session invalid"), { status: 401 }), false],
])("revalidateIdentity: %s", (_name, rejection, keepsSession) => {
  it(`rejection ${keepsSession ? "keeps" : "clears"} the token`, async () => {
    savePortalSession(localStorageStub, "http://localhost:3000", { token: "token-boot", expires_at: "2027-01-01T00:00:00Z" });
    fakeApi.portalMe.mockRejectedValue(rejection);
    const host = await mount(<PortalProbe />);
    await settle();
    expect(localStorageStub.data.has(portalSessionKey("http://localhost:3000"))).toBe(keepsSession);
    expect(host.querySelector("[data-identity]")?.getAttribute("data-identity")).toBe("");
  });
});

// The shell mounts the shared dock, notice, and dialogs: the account entry
// only exists while the snapshot grants the capability, the availability
// notice only while THIS server runs, and external links route through the
// native OpenURL binding (the Go side validates http(s) + host).
describe('shell portal surfaces', () => {
  it('opens the dialog with the origin-scoped identity and drops it wholesale when the origin changes', async () => {
    savePortalSession(localStorageStub, 'http://localhost:3000', { token: 'token-old', expires_at: '2027-01-01T00:00:00Z' });
    const host = await mount(<App />);
    await settle();
    expect(host.querySelector('.portal-account')?.textContent).toContain('Ada');
    await act(async () => { host.querySelector<HTMLElement>('.portal-account')!.click() });
    const dialog = host.querySelector('.portal-account-dialog');
    expect(dialog?.textContent).toContain('a@b.c');

    // An origin change rebuilds the surface: the identity is cleared. The
    // rebuilt engine may re-render the dialog for the NEW origin (its
    // snapshot recovers), but the old identity must be gone — the signed-in
    // card never survives a server switch.
    await act(async () => { setServerOrigin('http://127.0.0.1:9999') });
    await settle();
    expect(host.querySelector('.portal-account-dialog')?.textContent ?? '').not.toContain('a@b.c');
    expect(host.querySelector('.portal-account')?.textContent ?? '').not.toContain('Ada');
  });

  it('leaves the account entry unmounted when the snapshot says accounts are disabled', async () => {
    fakeApi.portalState.mockResolvedValue({ accountsEnabled: false, adsEnabled: false, donor: false, links: [] });
    const host = await mount(<App />);
    await settle();
    expect(host.querySelector('.portal-account')).toBeNull();
  });

  it('shows the update notice only while the server runs and an update is available', async () => {
    const host = await mount(<App />);
    await settle();
    // The shell boots with the runner's stopped seed.
    expect(host.querySelector('.update-notice')).toBeNull();

    await act(async () => { fakeEvents.emit('server:state', { state: 'running', address: '127.0.0.1:8097' }) });
    expect(host.querySelector('.update-notice')).not.toBeNull();

    await act(async () => { fakeEvents.emit('server:state', { state: 'stopped' }) });
    expect(host.querySelector('.update-notice')).toBeNull();
  });

  it('routes other-project links through the native OpenURL binding', async () => {
    const host = await mount(<App />);
    await settle();
    const link = host.querySelector<HTMLAnchorElement>('.portal-projects a');
    expect(link?.getAttribute('href')).toBe('https://example.invalid/tool');
    await act(async () => { link!.click() });
    expect(fakeBindings.openURL).toHaveBeenCalledWith('https://example.invalid/tool');
  });

  it('confirms applies with the whole-app restart warning before the POST', async () => {
    fakeApi.updatesApply.mockResolvedValue(updateStatus);
    const host = await mount(<App />);
    await settle();
    await act(async () => { fakeEvents.emit('server:state', { state: 'running', address: '127.0.0.1:8097' }) });
    await act(async () => {
      const apply = Array.from(host.querySelectorAll<HTMLButtonElement>('.update-notice button')).find(button => button.textContent === 'Apply update')!;
      apply.click();
    });
    const dialog = host.querySelector('[aria-label="Apply server update"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.textContent).toContain('Playback is interrupted');
    expect(dialog?.textContent).toContain('exits and relaunches');
    expect(fakeApi.updatesApply).not.toHaveBeenCalled();
    await act(async () => {
      Array.from(dialog!.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === 'Apply and restart')!.click();
    });
    expect(fakeApi.updatesApply).toHaveBeenCalledTimes(1);
  });
});
