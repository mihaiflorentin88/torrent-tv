import { render } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetPortal, seedServerState } from './lib/state';
import { ServerPage } from './pages/ServerPage';

// Bindings are stubbed at the module boundary; the dialog tests drive the
// Change… flow end to end and assert the path refresh comes from a fresh
// DataDirInfo read, never from guessing.
const fakeBindings = vi.hoisted(() => ({
  serverState: vi.fn(),
  startServer: vi.fn(),
  stopServer: vi.fn(),
  loadSettings: vi.fn(),
  version: vi.fn(),
  autostartStatus: vi.fn(),
  dataDirInfo: vi.fn(),
  openPath: vi.fn(),
  changeDataDir: vi.fn(),
}));

vi.mock('./bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings', () => ({
  AutostartStatus: fakeBindings.autostartStatus,
  ChangeDataDir: fakeBindings.changeDataDir,
  DataDirInfo: fakeBindings.dataDirInfo,
  DisableAutostart: vi.fn(),
  EnableAutostart: vi.fn(),
  LoadSettings: fakeBindings.loadSettings,
  OpenPath: fakeBindings.openPath,
  OpenWebUI: vi.fn(),
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
  await act(async () => { });
  return host;
}

const button = (label: string) =>
  Array.from(document.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.trim() === label)!;

const pathInput = (host: HTMLElement) =>
  host.querySelector<HTMLInputElement>('input[aria-label="New data folder path"]')!;

async function type(host: HTMLElement, value: string) {
  await act(async () => {
    pathInput(host).value = value;
    pathInput(host).dispatchEvent(new Event('input', { bubbles: true }));
  });
}

beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource);
  seedServerState({ state: 'stopped' });
  fakeBindings.version.mockResolvedValue('v0.1.2');
  fakeBindings.loadSettings.mockResolvedValue({ settingsPath: '/opt/fs/data/settings.json' });
  fakeBindings.autostartStatus.mockResolvedValue(false);
  fakeBindings.dataDirInfo.mockResolvedValue(['/opt/fs/data', 'pointer']);
  fakeApi.portalState.mockRejectedValue(new Error('portal routes absent'));
  fakeApi.updatesCurrent.mockRejectedValue(new Error('absent'));
  fakeApi.portalMe.mockRejectedValue(Object.assign(new Error('no session'), { status: 401 }));
  resetPortal();
});

afterEach(() => {
  for (const host of mountedHosts) { render(null, host); host.remove() }
  mountedHosts.length = 0;
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  resetPortal();
});

describe('ServerPage change data dir dialog', () => {
  it('opens with the restart warning before any submit', async () => {
    const host = await mount();
    expect(host.textContent).not.toContain('your data moves to the new location');
    await act(async () => { button('Change…').click() });
    expect(host.querySelector('[role="dialog"]')).toBeTruthy();
    expect(host.textContent).toContain('The server will restart; your data moves to the new location.');
    expect(button('Move data').disabled).toBe(true);
  });

  it('submits the entered path and refreshes the resolved location from DataDirInfo', async () => {
    fakeBindings.dataDirInfo
      .mockResolvedValueOnce(['/opt/fs/data', 'pointer']) // mount reads the old location
      .mockResolvedValue(['/srv/media/filelist-data', 'pointer']); // post-change re-read
    fakeBindings.changeDataDir.mockResolvedValue(undefined);
    const host = await mount();
    expect(host.textContent).toContain('/opt/fs/data');

    await act(async () => { button('Change…').click() });
    await type(host, '/srv/media/filelist-data');
    await act(async () => { button('Move data').click() });
    await act(async () => { });

    expect(fakeBindings.changeDataDir).toHaveBeenCalledWith('/srv/media/filelist-data');
    // The dialog closes and the details row shows the path as DataDirInfo
    // resolved it after the move.
    expect(host.querySelector('[role="dialog"]')).toBeFalsy();
    expect(host.textContent).toContain('/srv/media/filelist-data (from pointer)');
    expect(fakeBindings.dataDirInfo).toHaveBeenCalledTimes(2);
  });


  it('renders the backend refusal verbatim and keeps the dialog open', async () => {
    fakeBindings.changeDataDir.mockRejectedValue(new Error('target /srv/occupied is not empty'));
    const host = await mount();

    await act(async () => { button('Change…').click() });
    await type(host, '/srv/occupied');
    await act(async () => { button('Move data').click() });

    expect(host.textContent).toContain('target /srv/occupied is not empty');
    expect(host.querySelector('[role="dialog"]')).toBeTruthy();
    // The stale path stays displayed: the backend refused, nothing moved.
    expect(host.textContent).toContain('/opt/fs/data (from pointer)');
  });
});
