import { render } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PortalState, UpdateStatus } from '@torrent-tv/shared';
import type { UpdateController } from '@torrent-tv/web/portal';
import { resetPortal, seedServerState } from '../lib/state';
import { SettingsPage } from './SettingsPage';

// The shared web Settings component is driven through its real DOM; the
// bindings and the shared API client are stubbed at the module boundary so
// the page transport (LoadSettings → save → SaveSettings) is what's under
// test.
const fakeBindings = vi.hoisted(() => ({
  loadSettings: vi.fn(),
  settingsSchema: vi.fn(),
  missingRequired: vi.fn(),
  saveSettings: vi.fn(),
  restartServer: vi.fn(),
}));

const fakeApi = vi.hoisted(() => ({
  call: vi.fn(),
  portalState: vi.fn(),
  portalMe: vi.fn(),
  updatesCurrent: vi.fn(),
}));

// Inert stream: the portal engine opens one per origin; tests never drive it.
class FakeEventSource {
  constructor(public url: string) { }
  addEventListener() { }
  close() { }
}

// A controller stub with the shared controller's exact shape: the gating
// tests assert placement and availability, not the apply orchestration
// (covered by the web suite that owns the shared component).
const stubUpdates: UpdateController = {
  phase: 'idle', outcome: null, reconnectedCurrent: false,
  check: async () => { }, requestApply: () => { }, cancelApply: () => { }, confirmApply: async () => { },
};

const enabledSnapshot: PortalState = { accountsEnabled: true, adsEnabled: false, donor: false, links: [] };
const disabledSnapshot: PortalState = { accountsEnabled: false, adsEnabled: false, donor: false, links: [] };
const updateStatus: UpdateStatus = { currentVersion: '1.2.3', available: true, latest: '1.3.0', releasesUrl: 'https://example.invalid/releases', selfUpdate: true, applying: false };

vi.mock('../bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings', () => ({
  LoadSettings: fakeBindings.loadSettings,
  MissingRequired: fakeBindings.missingRequired,
  RestartServer: fakeBindings.restartServer,
  SaveSettings: fakeBindings.saveSettings,
  SettingsSchema: fakeBindings.settingsSchema,
}));

vi.mock('@torrent-tv/web/shared-api', () => ({
  configureSharedApi: () => { },
  sharedApi: () => fakeApi,
}));

// Records 'server:state' subscribers so tests can emit lifecycle events the
// way the Task 6 runner will.
const fakeEvents = vi.hoisted(() => {
  const subscribers = new Map<string, Array<(event: { data: unknown }) => void>>();
  return {
    subscribers,
    emit(topic: string, data: unknown) {
      for (const handler of subscribers.get(topic) ?? []) handler({ data });
    },
    reset() { subscribers.clear() },
  };
});

vi.mock('@wailsio/runtime', () => ({
  Events: {
    On: (topic: string, handler: (event: { data: unknown }) => void) => {
      const list = fakeEvents.subscribers.get(topic) ?? [];
      list.push(handler);
      fakeEvents.subscribers.set(topic, list);
      return () => { fakeEvents.subscribers.set(topic, list.filter(fn => fn !== handler)) };
    },
  },
}));

const settingsValue = {
  settingsPath: '/opt/fs/data/settings.json',
  instanceName: 'filelist', listenAddress: ':8097', databasePath: '/opt/fs/data/filelist.db',
  downloadRoot: '/opt/fs/downloads', fileListUrl: 'https://filelist.io', fileListUsername: 'user',
  fileListPasskey: '', fileListPasskeyConfigured: true, tmdbApiKey: '', tmdbApiKeyConfigured: true,
  qbittorrentUrl: 'http://127.0.0.1:8080', qbittorrentUsername: '', qbittorrentPassword: '', qbittorrentPasswordConfigured: false,
  downloadEngine: 'native', torrentPeerPort: 42069, torrentSessionDir: '/opt/fs/data/torrent-session',
  trustedCidrs: ['127.0.0.0/8'], evictionRules: ['oldest-completed'],
};

const schemaFields = [
  { key: 'fileListUrl', label: 'FileList URL', help: 'Tracker address.', tvVisible: false, sensitive: false, restartRequired: false, readOnly: false },
  { key: 'fileListPasskey', label: 'FileList passkey', help: 'Private credential.', tvVisible: false, sensitive: true, restartRequired: false, readOnly: false },
  { key: 'listenAddress', label: 'Listen address', help: 'HTTP listen address.', tvVisible: false, sensitive: false, restartRequired: true, readOnly: false },
];

const mountedHosts: HTMLElement[] = [];

async function mount(): Promise<HTMLElement> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  mountedHosts.push(host);
  await act(async () => { render(<SettingsPage updates={stubUpdates} />, host) });
  for (let i = 0; i < 5; i++) {
    await act(async () => {
      const { promise, resolve } = Promise.withResolvers<void>();
      setTimeout(resolve, 0);
      await promise;
    });
  }
  return host;
}


beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource);
  seedServerState({ state: 'running', address: '127.0.0.1:8097' });
  history.replaceState(null, '', '#');
  fakeBindings.loadSettings.mockResolvedValue(settingsValue);
  fakeBindings.settingsSchema.mockResolvedValue(schemaFields);
  fakeBindings.missingRequired.mockResolvedValue([]);
  fakeBindings.saveSettings.mockResolvedValue({ saved: true, restartRequired: false, autoStarted: false });
  fakeApi.call.mockReset();
  fakeApi.call.mockResolvedValue({});
  fakeApi.portalState.mockReset();
  fakeApi.portalState.mockResolvedValue(disabledSnapshot);
  fakeApi.portalMe.mockReset();
  fakeApi.portalMe.mockRejectedValue(Object.assign(new Error('no session'), { status: 401 }));
  fakeApi.updatesCurrent.mockReset();
  fakeApi.updatesCurrent.mockResolvedValue(updateStatus);
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

const settingsTabs = () => Array.from(document.querySelectorAll<HTMLButtonElement>('.settings-tabs button'));
const selectedTab = () => settingsTabs().find(button => button.getAttribute('aria-selected') === 'true')?.textContent;

beforeEach(() => {
  seedServerState({ state: 'running', address: '127.0.0.1:8097' });
  history.replaceState(null, '', '#');
  fakeBindings.loadSettings.mockResolvedValue(settingsValue);
  fakeBindings.settingsSchema.mockResolvedValue(schemaFields);
  fakeBindings.missingRequired.mockResolvedValue([]);
  fakeBindings.saveSettings.mockResolvedValue({ saved: true, restartRequired: false, autoStarted: false });
  fakeApi.call.mockReset();
  fakeApi.call.mockResolvedValue({});
});

afterEach(() => {
  for (const host of mountedHosts) { render(null, host); host.remove() }
  mountedHosts.length = 0;
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

describe('SettingsPage transport', () => {
  it('loads settings and schema through the bindings and renders the shared form', async () => {
    const host = await mount();
    expect(fakeBindings.loadSettings).toHaveBeenCalled();
    expect(fakeBindings.settingsSchema).toHaveBeenCalled();
    expect(host.querySelector('form.settings')).not.toBeNull();
    expect(host.textContent).toContain('Stored securely at /opt/fs/data/settings.json');
  });

  it('shows the load error when the bindings fail', async () => {
    fakeBindings.loadSettings.mockRejectedValue(new Error('bridge unavailable'));
    const host = await mount();
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('bridge unavailable');
  });

  it('routes the save bar through SaveSettings instead of the HTTP PUT and flags restart-required changes', async () => {
    fakeBindings.saveSettings.mockResolvedValue({ saved: true, restartRequired: true, autoStarted: false });
    const host = await mount();
    const input = Array.from(host.querySelectorAll<HTMLInputElement>('.settings-panel label')).find(
      item => item.querySelector('span')?.textContent?.startsWith('FileList URL'),
    )!.querySelector('input')!;
    await act(async () => {
      input.value = 'https://filelist.example';
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => { host.querySelector<HTMLButtonElement>('.settings-actions button[type="submit"]')!.click() });
    for (let i = 0; i < 5; i++) {
      await act(async () => {
        const { promise, resolve } = Promise.withResolvers<void>();
        setTimeout(resolve, 0);
        await promise;
      });
    }
    expect(fakeBindings.saveSettings).toHaveBeenCalled();
    expect(fakeApi.call).not.toHaveBeenCalled(); // the transport replaced the HTTP PUT
    const payload = fakeBindings.saveSettings.mock.calls[0][0] as Record<string, unknown>;
    expect(payload.fileListUrl).toBe('https://filelist.example');
    expect(host.textContent).toContain('Restart to apply');
    await act(async () => {
      Array.from(host.querySelectorAll('button')).find(button => button.textContent === 'Restart to apply')!.click();
    });
    expect(fakeBindings.restartServer).toHaveBeenCalled();
  });

  it('saves through bindings while stopped; a completing save clears the banner and the note reacts to the running event', async () => {
    seedServerState({ state: 'stopped' });
    fakeBindings.saveSettings.mockResolvedValue({ saved: true, restartRequired: false, autoStarted: true });
    fakeBindings.missingRequired.mockReset();
    fakeBindings.missingRequired
      .mockResolvedValueOnce(['fileListPasskey', 'downloadRoot']) // mount read
      .mockResolvedValue([]); // post-save read: setup completed
    const host = await mount();
    expect(host.querySelector('[role="note"]')?.textContent).toContain('Start the server to run tests');
    expect(host.textContent).toContain('Required settings missing');
    const input = Array.from(host.querySelectorAll<HTMLInputElement>('.settings-panel label')).find(
      item => item.querySelector('span')?.textContent?.startsWith('FileList passkey'),
    )!.querySelector('input')!;
    await act(async () => {
      input.value = 'secret-passkey';
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => { host.querySelector<HTMLButtonElement>('.settings-actions button[type="submit"]')!.click() });
    for (let i = 0; i < 5; i++) {
      await act(async () => {
        const { promise, resolve } = Promise.withResolvers<void>();
        setTimeout(resolve, 0);
        await promise;
      });
    }
    expect(fakeBindings.saveSettings).toHaveBeenCalled();
    expect(fakeApi.call).not.toHaveBeenCalled(); // works with the server stopped
    const result = await fakeBindings.saveSettings.mock.results[0].value;
    expect(result).toEqual({ saved: true, restartRequired: false, autoStarted: true });
    expect(host.textContent).not.toContain('Required settings missing');
    // The Go side auto-started; the emitted state event moves the page out of
    // the stopped-server note (the shell pill flips the same way).
    expect(host.querySelector('[role="note"]')).not.toBeNull();
    await act(async () => { fakeEvents.emit('server:state', { state: 'running', address: '127.0.0.1:8097' }) });
    expect(host.querySelector('[role="note"]')).toBeNull();
  });

  it('does not flag restart when the save changed nothing restart-required', async () => {
    const host = await mount();
    expect(host.textContent).not.toContain('Restart to apply');
  });

  it('reports a failed SaveSettings inline without losing the form', async () => {
    fakeBindings.saveSettings.mockRejectedValue(new Error('torrent session directory is not writable'));
    const host = await mount();
    await act(async () => {
      host.querySelector('form.settings')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });
    for (let i = 0; i < 3; i++) {
      await act(async () => {
        const { promise, resolve } = Promise.withResolvers<void>();
        setTimeout(resolve, 0);
        await promise;
      });
    }
    expect(host.querySelector('[role="alert"]')?.textContent).toContain('not writable');
    expect(host.querySelector('form.settings')).not.toBeNull();
  });
});

describe('SettingsPage banners', () => {
  it('banners missing required settings and deep-links the Tracker tab', async () => {
    fakeBindings.missingRequired.mockResolvedValue(['fileListPasskey']);
    history.replaceState(null, '', '#storage');
    const host = await mount();
    const banner = host.querySelector('[role="alert"]');
    expect(banner?.textContent).toContain('Required settings missing: fileListPasskey');
    expect(selectedTab()).toBe('Storage');
    await act(async () => {
      Array.from(host.querySelectorAll('button')).find(button => button.textContent?.includes('Tracker tab'))!.click();
    });
    expect(selectedTab()).toBe('Tracker');
  });

  it('clears the missing banner once a completing save is mirrored', async () => {
    fakeBindings.missingRequired
      .mockResolvedValueOnce(['fileListPasskey'])
      .mockResolvedValue([]);
    const host = await mount();
    expect(host.textContent).toContain('Required settings missing');
    await act(async () => {
      host.querySelector('form.settings')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });
    for (let i = 0; i < 3; i++) {
      await act(async () => {
        const { promise, resolve } = Promise.withResolvers<void>();
        setTimeout(resolve, 0);
        await promise;
      });
    }
    expect(host.textContent).not.toContain('Required settings missing');
  });

  it('explains stopped-server behavior above the form without hiding it', async () => {
    seedServerState({ state: 'stopped' });
    const host = await mount();
    const note = host.querySelector('[role="note"]');
    expect(note?.textContent).toContain('Start the server to run tests');
    expect(host.querySelector('form.settings')).not.toBeNull();
    // The note renders above the shared component.
    const noteIndex = Array.from(host.children).indexOf(note!);
    const formIndex = Array.from(host.children).findIndex(child => child.querySelector('form.settings'));
    expect(noteIndex).toBeLessThan(formIndex);
  });
});

// Account and update gating must behave exactly like the web app: the
// account group exists only while the snapshot says accounts are enabled
// (an outage — an unknown snapshot — is identical to a disabled server),
// and the update controls render only while THIS embedded server is
// running. The stored settings are carried by the Go store (SaveSettings
// transport) and are never touched by portal state, so the stored server
// key survives capability loss by construction.
describe('SettingsPage portal gating', () => {
  it('shows the Account tab and the update section while accounts are enabled and the server runs', async () => {
    fakeApi.portalState.mockResolvedValue(enabledSnapshot);
    const host = await mount();
    expect(settingsTabs().some(button => button.textContent === 'Account')).toBe(true);
    const section = host.querySelector('.update-section');
    expect(section).not.toBeNull();
    expect(section?.querySelector('.update-actions button:not([disabled])')).not.toBeNull();
  });

  it('unmounts the entire account group with zero trace when accounts are disabled', async () => {
    fakeApi.portalState.mockResolvedValue(disabledSnapshot);
    const host = await mount();
    expect(settingsTabs().some(button => button.textContent === 'Account')).toBe(false);
    expect(host.textContent).not.toContain('Account');
  });

  it('treats an unknown snapshot (portal outage) as accounts disabled', async () => {
    fakeApi.portalState.mockRejectedValue(new Error('portal routes absent'));
    const host = await mount();
    expect(settingsTabs().some(button => button.textContent === 'Account')).toBe(false);
  });

  it('keeps update controls unavailable while the embedded server is stopped', async () => {
    fakeApi.portalState.mockResolvedValue(enabledSnapshot);
    seedServerState({ state: 'stopped' });
    const host = await mount();
    // Status still recovers from the last session, but the section must not
    // pretend a stopped server can check or apply anything.
    expect(host.querySelector('.update-section')).toBeNull();
    expect(host.textContent).toContain('The server is stopped');
  });
});
