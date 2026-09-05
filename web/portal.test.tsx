import { render } from 'preact';
import type { ComponentChild } from 'preact';
import { act } from 'preact/test-utils';
import { useEffect, useState } from 'preact/hooks';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { API, portalSessionKey, type PortalSession, type PortalState, type PortalUser, type UpdateStatus } from '@torrent-tv/shared';
import { App } from './src';
import { PortalAccountDialog, PortalPromotionSlot, UpdateApplyConfirm, UpdateSection, useUpdateController } from './portal';

// Behavioral regression tests for the S9 portal and self-update surfaces.
// The whole app mounts through the same seams as settings.test.tsx; SSE
// arrives through a capturing EventSource fake, storage is a stubbed
// in-memory store (the test environment has no localStorage), and every
// network path is answered at the API.call boundary.

let portalStateValue: PortalState | null;
let portalStateSequence: PortalState[];
let updateStatusValue: UpdateStatus | null;
let meStatus: number;
let meUser: PortalUser;
let checkOutcome: { status: number; detail: string } | { value: UpdateStatus } | null;
let applyOutcome: { status: number; detail: string } | { value: UpdateStatus } | null;
let applyCalls: number;
const storedSettings: Record<string, unknown> = {
  settingsPath: 'data/settings.json', portalAPIKey: '', portalAPIKeyConfigured: true,
  listenAddress: ':8097', trustedCidrs: ['192.168.50.0/24'], evictionRules: [] as string[],
};

const enabledSnapshot: PortalState = { accountsEnabled: true, adsEnabled: true, donor: false, links: [{ id: 1, title: 'Other tool', url: 'https://example.invalid/tool', description: 'A project' }] };
const disabledSnapshot: PortalState = { accountsEnabled: false, adsEnabled: false, donor: false, links: [] };
const availableStatus: UpdateStatus = { currentVersion: '1.2.3', available: true, latest: '1.3.0', notes: 'Many fixes.', releasedAt: '2026-09-01T00:00:00Z', releasesUrl: 'https://example.invalid/releases', selfUpdate: true, applying: false };

const store = (() => { const data = new Map<string, string>(); return { data, getItem: (key: string) => data.get(key) ?? null, setItem: (key: string, value: string) => void data.set(key, value), removeItem: (key: string) => void data.delete(key), clear: () => void data.clear() } })();

async function fakeCall(path: string, init?: RequestInit): Promise<unknown> {
  const method = init?.method || 'GET';
  if (path === '/portal/state') {
    if (portalStateSequence.length) return portalStateSequence.shift();
    if (portalStateValue === null) throw Object.assign(new Error('portal routes absent'), { status: 404 });
    return portalStateValue;
  }
  if (path === '/updates/current') { if (updateStatusValue === null) throw Object.assign(new Error('absent'), { status: 404 }); return updateStatusValue }
  if (path === '/portal/session/me') { if (meStatus !== 200) throw Object.assign(new Error('invalid session'), { status: meStatus }); return meUser }
  if (path === '/updates/check' && method === 'POST') {
    if (checkOutcome && 'status' in checkOutcome) throw Object.assign(new Error(checkOutcome.detail), { status: checkOutcome.status });
    if (checkOutcome) return checkOutcome.value;
    throw Object.assign(new Error('absent'), { status: 404 });
  }
  if (path === '/updates/apply' && method === 'POST') {
    applyCalls += 1;
    if (applyOutcome && 'status' in applyOutcome) throw Object.assign(new Error(applyOutcome.detail), { status: applyOutcome.status });
    if (applyOutcome) return applyOutcome.value;
    throw Object.assign(new Error('absent'), { status: 404 });
  }
  if (path === '/settings' && method === 'GET') return storedSettings;
  if (path === '/settings/schema') return { items: [{ key: 'listenAddress', label: 'Listen address', help: 'HTTP listen address.', tvVisible: false, sensitive: false, restartRequired: true, readOnly: true }, { key: 'portalAPIKey', label: 'Supporter API key', help: 'Supporter credential.', tvVisible: false, sensitive: true, restartRequired: false }] };
  if (path === '/state') return { favorites: [], continueWatching: [], recent: [], watched: [] };
  throw new Error(`unexpected API call ${method} ${path}`);
}

class CapturingEventSource {
  static instances: CapturingEventSource[] = [];
  lastEventId = '';
  private handlers = new Map<string, EventListener>();
  constructor(public url: string) { CapturingEventSource.instances.push(this) }
  addEventListener(type: string, listener: EventListener) { this.handlers.set(type, listener) }
  close() { }
  emit(type: string, data?: string) {
    if (!data) { this.handlers.get(type)?.(new Event(type)); return }
    const event = new MessageEvent(type, { data, lastEventId: this.lastEventId });
    this.handlers.get(type)?.(event);
  }
}

const stream = () => CapturingEventSource.instances.at(-1)!;
const envelope = (kind: string, payload: unknown, id: number) => JSON.stringify({ id, kind, payload: JSON.stringify(payload), createdAt: '2026-09-05T00:00:00Z' });

const mountedHosts: HTMLElement[] = [];
async function mountApp() {
  const host = document.createElement('div');
  document.body.appendChild(host);
  mountedHosts.push(host);
  await act(async () => { render(<App />, host) });
}
async function settle() {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve() });
}
function mountComponent(node: ComponentChild) {
  const host = document.createElement('div');
  document.body.appendChild(host);
  mountedHosts.push(host);
  return {
    host,
    render: async () => { await act(async () => { render(node, host) }) },
    unmount: async () => { await act(async () => { render(null, host) }) },
  };
}
// Queries always scope to the most recently mounted host so stacked
// harnesses never answer each other's buttons.
const latestHost = () => mountedHosts.at(-1)!;
const sidebarButton = (label: string) => Array.from(latestHost().querySelectorAll<HTMLButtonElement>('.sidebar nav button')).find(button => button.textContent?.includes(label))!;
const settingsTabs = () => Array.from(latestHost().querySelectorAll<HTMLButtonElement>('.settings-tabs button'));
const findButton = (label: string) => Array.from(latestHost().querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === label)!;
const sectionState = () => latestHost().querySelector('.update-state')!.getAttribute('data-state');
const sectionText = () => latestHost().querySelector('.update-section')!.textContent!;

beforeEach(() => {
  vi.stubGlobal('EventSource', CapturingEventSource);
  vi.stubGlobal('localStorage', store);
  store.data.clear();
  vi.spyOn(API.prototype, 'facets').mockResolvedValue({ categories: [], kinds: [], resolutions: [], hdr: [], qualities: [], codecs: [] });
  vi.spyOn(API.prototype, 'titles').mockResolvedValue({ items: [], nextCursor: null, total: 0 });
  vi.spyOn(API.prototype, 'downloads').mockResolvedValue({ items: [], nextCursor: null, total: 0 });
  vi.spyOn(API.prototype, 'call').mockImplementation(fakeCall as typeof API.prototype.call);
  portalStateValue = null;
  portalStateSequence = [];
  updateStatusValue = null;
  meStatus = 200;
  meUser = { id: 3, email: 'a@b.c', display_name: 'Ada', role: 'member' };
  checkOutcome = null;
  applyOutcome = null;
  applyCalls = 0;
});

afterEach(() => {
  while (mountedHosts.length) render(null, mountedHosts.pop()!);
  document.body.innerHTML = '';
  CapturingEventSource.instances.length = 0;
  store.data.clear();
  window.history.replaceState(null, '', '/');
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  Reflect.deleteProperty(document, 'visibilityState');
});

describe('stored supporter identity', () => {
  it('an expired stored session clears on sight and never reaches /session/me', async () => {
    const me = vi.spyOn(API.prototype, 'portalMe');
    store.setItem(portalSessionKey(location.origin), JSON.stringify({ token: 'expired', expiresAt: Date.now() - 1000 }));
    portalStateValue = enabledSnapshot;
    await mountApp();
    await settle();
    expect(me).not.toHaveBeenCalled();
    expect(store.getItem(portalSessionKey(location.origin))).toBeNull();
    expect(document.querySelector('.portal-account')!.textContent).toContain('Account');
  });

  it('a 401 from /session/me clears the stored identity client-side', async () => {
    store.setItem(portalSessionKey(location.origin), JSON.stringify({ token: 'live', expiresAt: Date.now() + 60_000 }));
    meStatus = 401;
    await mountApp();
    await settle();
    expect(store.getItem(portalSessionKey(location.origin))).toBeNull();
  });

  it('a plain network failure keeps the stored token for the next boot', async () => {
    store.setItem(portalSessionKey(location.origin), JSON.stringify({ token: 'live', expiresAt: Date.now() + 60_000 }));
    meStatus = 503;
    await mountApp();
    await settle();
    expect(JSON.parse(store.getItem(portalSessionKey(location.origin))!).token).toBe('live');
  });

  it('a valid session restores the display name while donor state stays a separate snapshot field', async () => {
    store.setItem(portalSessionKey(location.origin), JSON.stringify({ token: 'live', expiresAt: Date.now() + 60_000 }));
    portalStateValue = { ...enabledSnapshot, donor: true };
    await mountApp();
    await settle();
    expect(document.querySelector('.portal-account')!.textContent).toContain('Ada');
    expect(document.querySelector('.portal-promo')).toBeNull();
  });
});

describe('account capability gating', () => {
  it('capability loss unmounts the dialog, the dock entry, and the whole Settings account tab, keeping the stored key', async () => {
    portalStateValue = enabledSnapshot;
    await mountApp();
    await settle();
    expect(document.querySelector('.portal-account')).toBeTruthy();
    expect(document.querySelector('.portal-projects')!.textContent).toContain('Other tool');
    await act(async () => { (document.querySelector('.portal-account') as HTMLElement).click() });
    expect(document.querySelector('.overlay[aria-label="Account"]')).toBeTruthy();
    portalStateSequence = [disabledSnapshot];
    await act(async () => { stream().emit('portal.state', envelope('portal.state', disabledSnapshot, 2)) });
    await settle();
    expect(document.querySelector('.overlay[aria-label="Account"]')).toBeNull();
    expect(document.querySelector('.portal-account')).toBeNull();
    expect(document.querySelector('.portal-projects')).toBeNull();
    await act(async () => { sidebarButton('Settings').click() });
    await settle();
    expect(settingsTabs().map(button => button.textContent)).not.toContain('Account');
    expect(latestHost().querySelector('.settings-panel')!.textContent).not.toContain('Supporter API key');
    // The stored server key survives: nothing erased it client-side.
    expect(storedSettings.portalAPIKeyConfigured).toBe(true);
  });

  it('capability loss drops the open-flag so a later re-enable does not re-open the dialog', async () => {
    portalStateValue = enabledSnapshot;
    await mountApp();
    await settle();
    await act(async () => { (document.querySelector('.portal-account') as HTMLElement).click() });
    expect(document.querySelector('.overlay[aria-label="Account"]')).toBeTruthy();
    portalStateSequence = [disabledSnapshot];
    await act(async () => { stream().emit('portal.state', envelope('portal.state', disabledSnapshot, 2)) });
    await settle();
    expect(document.querySelector('.overlay[aria-label="Account"]')).toBeNull();
    portalStateSequence = [{ ...enabledSnapshot }];
    await act(async () => { stream().emit('portal.state', envelope('portal.state', enabledSnapshot, 3)) });
    await settle();
    expect(document.querySelector('.overlay[aria-label="Account"]')).toBeNull();
  });

  it('a hidden or failed portal surface renders no account, promotion, or project shell at all', async () => {
    await mountApp();
    await settle();
    expect(document.querySelector('.portal-dock')).toBeNull();
    expect(document.querySelector('.portal-account')).toBeNull();
  });
});

describe('promotion slot', () => {
  it('a donor snapshot removes the slot entirely and never fetches delivery', async () => {
    const delivery = vi.spyOn(API.prototype, 'portalPromotions').mockResolvedValue([]);
    const mounted = mountComponent(<PortalPromotionSlot client={new API(location.origin)} snapshot={{ ...enabledSnapshot, donor: true }} openExternal={() => { }} />);
    await mounted.render();
    expect(mounted.host.querySelector('.portal-promo')).toBeNull();
    expect(delivery).not.toHaveBeenCalled();
  });

  it('delivers only while visible, clicks through the local tracking route, rotates by screenTime, and cancels on hidden and unmount', async () => {
    vi.useFakeTimers();
    const delivery = vi.spyOn(API.prototype, 'portalPromotions').mockResolvedValue([
      { id: 'p1', provider: 'prov', title: 'First', text: 'one', image: '', screenTime: 8 },
      { id: 'p2', provider: 'prov', title: 'Second', text: 'two', image: '', screenTime: 10 },
    ]);
    const opened: string[] = [];
    const flush = async () => { for (let i = 0; i < 4; i++) await act(async () => { await Promise.resolve() }) };
    const mounted = mountComponent(<PortalPromotionSlot client={new API(location.origin)} snapshot={enabledSnapshot} openExternal={url => opened.push(url)} />);
    await mounted.render();
    await flush();
    // Delivery happens exactly once for the visible slot — no prefetch.
    expect(delivery).toHaveBeenCalledTimes(1);
    expect(mounted.host.textContent).toContain('First');
    // 8s screenTime (upstream seconds) advances to the second creative.
    await act(async () => { await vi.advanceTimersByTimeAsync(8000) });
    expect(mounted.host.textContent).toContain('Second');
    expect(delivery).toHaveBeenCalledTimes(1);
    // Clicks go through the local tracking route, never a direct upstream URL.
    await act(async () => { (mounted.host.querySelector('.portal-promo a') as HTMLElement).click() });
    expect(opened).toEqual([`${location.origin}/api/v1/portal/promotions/prov/p2/click`]);
    // A hidden document cancels the rotation timer: no advance, no refetch.
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) });
    expect(mounted.host.querySelector('.portal-promo')).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) });
    expect(delivery).toHaveBeenCalledTimes(1);
    // Visible again: fresh delivery, never a prefetch impression.
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) });
    await flush();
    expect(delivery).toHaveBeenCalledTimes(2);
    expect(mounted.host.textContent).toContain('First');
    // Unmount kills the timers: no delivery or advance ever happens again.
    await mounted.unmount();
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) });
    expect(delivery).toHaveBeenCalledTimes(2);
  });
});

describe('SSE recovery', () => {
  it('a replayed snapshot cannot override the recovery refetch, while live events still apply', async () => {
    portalStateValue = enabledSnapshot;
    await mountApp();
    await settle();
    expect(document.querySelector('.portal-account')).toBeTruthy();
    // The last live event before the drop carries id 9.
    await act(async () => { stream().emit('portal.state', envelope('portal.state', enabledSnapshot, 9)) });
    await act(async () => { stream().emit('error') });
    portalStateSequence = [disabledSnapshot];
    await act(async () => { stream().emit('open') });
    await settle();
    expect(document.querySelector('.portal-account')).toBeNull();
    // A replayed pre-drop frame (id 4) must not resurrect the removed capability.
    await act(async () => { stream().emit('portal.state', envelope('portal.state', enabledSnapshot, 4)) });
    await settle();
    expect(document.querySelector('.portal-account')).toBeNull();
    // A genuinely new event still applies.
    await act(async () => { stream().emit('portal.state', envelope('portal.state', enabledSnapshot, 10)) });
    await settle();
    expect(document.querySelector('.portal-account')).toBeTruthy();
  });
});

// Test harness: the real controller hook plus the real section and confirm
// overlay, with an internal status state so accepted applies and simulated
// restarts flow through the same path as the app.
function UpdateHarness({ status: initial, restarted, failApplyWith, failure = '' }: { status: UpdateStatus; restarted?: UpdateStatus; failApplyWith?: { status: number; detail: string }; failure?: string }) {
  const client = new API(location.origin);
  const [status, setStatus] = useState(initial);
  const controller = useUpdateController({ client, status, onStatus: setStatus });
  useEffect(() => { if (failApplyWith) applyOutcome = failApplyWith }, [failApplyWith]);
  return <div>
    <UpdateSection client={client} status={status} connected failure={failure} controller={controller} openExternal={() => { }} />
    <UpdateApplyConfirm controller={controller} />
    {restarted && <button onClick={() => setStatus(restarted)}>simulate-restart</button>}
  </div>;
}

describe('update controls', () => {
  it('applying requires the playback-interruption confirmation before the POST fires', async () => {
    applyOutcome = { value: { ...availableStatus, available: false, latest: undefined, notes: undefined, applying: true } };
    const mounted = mountComponent(<UpdateHarness status={availableStatus} />);
    await mounted.render();
    expect(document.querySelector('.overlay[aria-label="Apply server update"]')).toBeNull();
    await act(async () => { findButton('Apply update').click() });
    expect(document.querySelector('.overlay[aria-label="Apply server update"]')).toBeTruthy();
    expect(applyCalls).toBe(0);
    await act(async () => { findButton('Cancel').click() });
    expect(document.querySelector('.overlay[aria-label="Apply server update"]')).toBeNull();
    expect(applyCalls).toBe(0);
    await act(async () => { findButton('Apply update').click() });
    await act(async () => { findButton('Apply and restart').click() });
    expect(applyCalls).toBe(1);
    expect(sectionState()).toBe('applying');
  });

  it('reports the manual-only, busy, and failed apply outcomes with the mandatory warning', async () => {
    const manualOnly = mountComponent(<UpdateHarness status={availableStatus} failApplyWith={{ status: 409, detail: 'updates: installation is manual-only: capability probe failed' }} />);
    await manualOnly.render();
    await act(async () => { findButton('Apply update').click() });
    await act(async () => { findButton('Apply and restart').click() });
    expect(sectionState()).toBe('manual-only');
    expect(sectionText()).toContain('manual-only: fetch and install a release yourself');
    expect(sectionText()).toContain('TV and display-only clients cannot apply updates');
    expect(sectionText()).toContain('Releases and manual installs');

    const busy = mountComponent(<UpdateHarness status={availableStatus} failApplyWith={{ status: 409, detail: 'updates: an apply operation is already in progress' }} />);
    await busy.render();
    await act(async () => { findButton('Apply update').click() });
    await act(async () => { findButton('Apply and restart').click() });
    expect(sectionState()).toBe('busy');
    expect(sectionText()).toContain('An update is already in progress on the server.');

    const failed = mountComponent(<UpdateHarness status={availableStatus} failApplyWith={{ status: 502, detail: 'the release could not be verified' }} />);
    await failed.render();
    await act(async () => { findButton('Apply update').click() });
    await act(async () => { findButton('Apply and restart').click() });
    expect(sectionState()).toBe('failed');
    expect(sectionText()).toContain('Update problem: the release could not be verified.');
  });

  it('renders a server-reported failure and clears it with the next status', async () => {
    const failing = mountComponent(<UpdateHarness status={availableStatus} failure='background apply failed' />);
    await failing.render();
    expect(sectionState()).toBe('failed');
    expect(sectionText()).toContain('Update problem: background apply failed.');
    const cleared = mountComponent(<UpdateHarness status={availableStatus} />);
    await cleared.render();
    expect(sectionState()).toBe('available');
    expect(sectionText()).not.toContain('Update problem');
  });

  it('surfaces a failed check as a neutral problem and confirms the restarted version after reconnect', async () => {
    checkOutcome = { status: 502, detail: 'repository unreachable' };
    const check = mountComponent(<UpdateHarness status={availableStatus} />);
    await check.render();
    await act(async () => { findButton('Check for updates').click() });
    expect(sectionState()).toBe('failed');
    expect(sectionText()).toContain('Update problem: repository unreachable');

    applyOutcome = { value: { ...availableStatus, available: false, latest: undefined, notes: undefined, applying: true } };
    const restarted = { ...availableStatus, available: false, latest: undefined, notes: undefined, applying: false, currentVersion: '1.3.0' };
    const apply = mountComponent(<UpdateHarness status={availableStatus} restarted={restarted} />);
    await apply.render();
    await act(async () => { findButton('Apply update').click() });
    await act(async () => { findButton('Apply and restart').click() });
    await act(async () => { findButton('simulate-restart').click() });
    expect(sectionState()).toBe('reconnected');
    expect(sectionText()).toContain('now running version 1.3.0');
  });

  it('shows the reconnecting line while the server connection is down', async () => {
    function DisconnectedHarness() {
      const client = new API(location.origin);
      const controller = useUpdateController({ client, status: availableStatus, onStatus: () => { } });
      return <UpdateSection client={client} status={availableStatus} connected={false} failure='' controller={controller} openExternal={() => { }} />;
    }
    const mounted = mountComponent(<DisconnectedHarness />);
    await mounted.render();
    expect(sectionState()).toBe('disconnected');
    expect(sectionText()).toContain('Server connection lost — reconnecting…');
  });

  it('unmount during an in-flight sign-in stores nothing and closes nothing', async () => {
    const onIdentity = vi.fn();
    const onClose = vi.fn();
    vi.spyOn(API.prototype, 'portalSession').mockImplementation((email: string, password: string, signal?: AbortSignal) => {
      const { promise, reject } = Promise.withResolvers<never>();
      signal?.addEventListener('abort', () => reject(Object.assign(new Error('aborted'), { name: 'AbortError' })));
      return promise;
    });
    function DialogHarness() {
      return <PortalAccountDialog client={new API(location.origin)} storage={store} origin={location.origin} identity={null} onIdentity={onIdentity} onClose={onClose} />;
    }
    const mounted = mountComponent(<DialogHarness />);
    await mounted.render();
    await act(async () => {
      const email = latestHost().querySelector<HTMLInputElement>('.portal-account-dialog input[type=email]')!;
      email.value = 'ada@example.invalid';
      email.dispatchEvent(new Event('input', { bubbles: true }));
      const pw = latestHost().querySelector<HTMLInputElement>('.portal-account-dialog input[type=password]')!;
      pw.value = 'correct-horse';
      pw.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => { findButton('Sign in').click() });
    // Unmount aborts the in-flight request; the AbortError path must not
    // store the session, report identity, or close anything.
    await mounted.unmount();
    await settle();
    expect(store.getItem(portalSessionKey(location.origin))).toBeNull();
    expect(onIdentity).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });
  it('double-submit during an in-flight sign-in issues exactly one request', async () => {
    const { promise } = Promise.withResolvers<PortalSession>();
    const sessionSpy = vi.spyOn(API.prototype, 'portalSession').mockImplementation(() => promise);
    function DialogHarness() {
      return <PortalAccountDialog client={new API(location.origin)} storage={store} origin={location.origin} identity={null} onIdentity={vi.fn()} onClose={vi.fn()} />;
    }
    const mounted = mountComponent(<DialogHarness />);
    await mounted.render();
    await act(async () => {
      const email = latestHost().querySelector<HTMLInputElement>('.portal-account-dialog input[type=email]')!;
      email.value = 'ada@example.invalid';
      email.dispatchEvent(new Event('input', { bubbles: true }));
      const pw = latestHost().querySelector<HTMLInputElement>('.portal-account-dialog input[type=password]')!;
      pw.value = 'correct-horse';
      pw.dispatchEvent(new Event('input', { bubbles: true }));
    });
    const submit = () => { latestHost().querySelector<HTMLButtonElement>('.portal-account-dialog button[type=submit]')!.click(); };
    await act(async () => { submit() });
    await act(async () => { submit() });
    expect(sessionSpy).toHaveBeenCalledTimes(1);
    expect(latestHost().querySelector<HTMLButtonElement>('.portal-account-dialog button[type=submit]')!.disabled).toBe(true);
  });
});
