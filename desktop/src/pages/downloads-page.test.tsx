import { render } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { seedServerState } from '../lib/state';
import { DownloadsPage } from './DownloadsPage';

// The Wails bindings are stubbed at the module boundary alongside the
// shared API: Play must reach OpenURL, never a real browser.
const fakeBindings = vi.hoisted(() => ({
  openURL: vi.fn(),
}));
const fakeApi = vi.hoisted(() => ({
  downloads: vi.fn(),
  deleteDownload: vi.fn(),
  call: vi.fn(),
}));

vi.mock('../bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings', () => ({
  OpenURL: fakeBindings.openURL,
}));

vi.mock('@torrent-tv/web/shared-api', () => ({
  configureSharedApi: () => { },
  sharedApi: () => fakeApi,
}));

vi.mock('@wailsio/runtime', () => ({
  Events: { On: () => () => { } },
}));

const downloading = {
  id: 'd1', releaseId: 'r1', engineId: 'native:abc123', fileIndex: 0,
  filePath: 'Silo.S01E01.1080p.mkv', mimeType: 'video/x-matroska',
  sizeBytes: 4879437296, state: 'downloading', progress: 0.42, playbackMode: 'progressive',
  downloadedBytes: 2049363664, speedBytesPerSecond: 5242880, etaSeconds: 540, peers: 12, seeds: 3,
  leased: false, error: '', streamUrl: '/api/v1/downloads/d1/stream',
};

const mountedHosts: HTMLElement[] = [];

async function mount(): Promise<HTMLElement> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  mountedHosts.push(host);
  // Render inside act so the page's poll effect runs before assertions —
  // matching the web client's test convention.
  await act(async () => { render(<DownloadsPage />, host) });
  return host;
}

async function settle() {
  await act(async () => { });
  await act(async () => { });
}

beforeEach(() => {
  seedServerState({ state: 'stopped' });
  fakeApi.downloads.mockReset();
  fakeApi.downloads.mockResolvedValue({ items: [] });
});

afterEach(() => {
  for (const host of mountedHosts) { render(null, host); host.remove() }
  mountedHosts.length = 0;
  document.body.innerHTML = '';
});

describe('DownloadsPage gating', () => {
  it('renders the not-running empty state and never touches a dead server', async () => {
    const host = await mount();
    await settle();
    const empty = host.querySelector('.empty-state');
    expect(empty?.textContent).toContain('Server is stopped');
    expect(empty?.textContent).toContain('Start the server to see downloads.');
    expect(fakeApi.downloads).not.toHaveBeenCalled();
    expect(host.querySelector('[data-download-id]')).toBeNull();
  });

  it('mounts the shared Downloads view once the server reports running', async () => {
    seedServerState({ state: 'running' });
    fakeApi.downloads.mockResolvedValue({ items: [downloading] });
    const host = await mount();
    await settle();
    expect(fakeApi.downloads).toHaveBeenCalled();
    expect(host.querySelector('.empty-state')).toBeNull();
    expect(host.querySelector('[data-download-id="d1"]')).not.toBeNull();
  });
});

describe('DownloadsPage Play handoff', () => {
  beforeEach(() => { fakeBindings.openURL.mockResolvedValue(undefined) });

  it('opens the download watch URL in the browser through OpenURL', async () => {
    seedServerState({ state: 'running', address: '192.168.1.10:8097' });
    fakeApi.downloads.mockResolvedValue({ items: [downloading] });
    const host = await mount();
    await settle();
    const play = Array.from(host.querySelectorAll<HTMLButtonElement>('[data-download-id="d1"] button'))
      .find(b => b.textContent?.trim() === 'Play')!;
    expect(play).toBeDefined();
    await act(async () => { play.click() });
    expect(fakeBindings.openURL).toHaveBeenCalledWith('http://192.168.1.10:8097/watch/d1');
  });

  it('falls back to the loopback address when the state carries none', async () => {
    seedServerState({ state: 'running' });
    fakeApi.downloads.mockResolvedValue({ items: [downloading] });
    const host = await mount();
    await settle();
    const play = Array.from(host.querySelectorAll<HTMLButtonElement>('[data-download-id="d1"] button'))
      .find(b => b.textContent?.trim() === 'Play')!;
    await act(async () => { play.click() });
    expect(fakeBindings.openURL).toHaveBeenCalledWith('http://127.0.0.1:8097/watch/d1');
  });
});
