import { render } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { API } from '@torrent-tv/shared';
import { App } from './src';

// Downloads-page tests: the whole app mounts with the API mocked at the
// class boundary; the tests assert per-torrent cards surface live transfer
// facts (peers, ETA) and tracker errors without the old placeholder text.
// Prior art: settings.test.tsx.

const nativeDownload = {
 id: 'd1', releaseId: 'r1', engineId: 'native:abc123', fileIndex: 0,
 filePath: 'Silo.S01E01.Freedom.Day.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb.mkv', mimeType: 'video/x-matroska',
 sizeBytes: 4879437296, state: 'downloading', progress: 0.42, playbackMode: 'progressive',
 downloadedBytes: 2049363664, speedBytesPerSecond: 5242880, etaSeconds: 540, peers: 12, seeds: 3,
 leased: false, error: 'tracker gave failure reason: "Your client is not allowed!"', streamUrl: '/api/v1/downloads/d1/stream',
};

const qbDownload = {
 id: 'd2', releaseId: 'r2', engineId: 'qb:xyz789', fileIndex: 0,
 filePath: 'Silo.S02E01.1080p.mkv', mimeType: 'video/x-matroska',
 sizeBytes: 3889493092, state: 'seeding', progress: 1, playbackMode: 'local',
 downloadedBytes: 3889493092, speedBytesPerSecond: 0, etaSeconds: 0, peers: 0, seeds: 0,
 leased: false, error: '', streamUrl: '/api/v1/downloads/d2/stream',
};

const mountedHosts: HTMLElement[] = [];

class FakeEventSource { addEventListener() { } close() { } }

async function mountApp() {
 const host = document.createElement('div');
 document.body.appendChild(host);
 mountedHosts.push(host);
 await act(async () => { render(<App />, host) });
 await act(async () => { });
}

const sidebarButton = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>('.sidebar nav button')).find(button => button.textContent?.includes(label))!;

async function settle() {
 for (let i = 0; i < 5; i++) {
  await act(async () => {
   const { promise, resolve } = Promise.withResolvers<void>();
   setTimeout(resolve, 0);
   await promise;
  });
 }
}

beforeEach(() => {
 vi.stubGlobal('EventSource', FakeEventSource);
 vi.spyOn(API.prototype, 'facets').mockResolvedValue({ categories: [], kinds: [], resolutions: [], hdr: [], qualities: [], codecs: [] });
 vi.spyOn(API.prototype, 'titles').mockResolvedValue({ items: [], nextCursor: null, total: 0 });
 vi.spyOn(API.prototype, 'ensureMetadata').mockResolvedValue({ queued: 0 });
 vi.spyOn(API.prototype, 'downloads').mockResolvedValue({ items: [nativeDownload, qbDownload] as never[], nextCursor: null, total: 2 });
});

afterEach(() => {
 vi.restoreAllMocks();
 vi.unstubAllGlobals();
 for (const host of mountedHosts) host.remove();
 mountedHosts.length = 0;
 document.body.innerHTML = '';
});

async function openDownloads() {
 await mountApp();
 await act(async () => { sidebarButton('Downloads').click() });
 await settle();
}

describe('downloads page cards', () => {
 it('surfaces the tracker error on the affected card', async () => {
  await openDownloads();
  const text = document.body.textContent || '';
  expect(text).toContain('Your client is not allowed!');
 });

 it('never renders the old placeholder text', async () => {
  await openDownloads();
  expect(document.body.textContent).not.toContain('No download error');
 });

 it('shows which engine owns each download', async () => {
  await openDownloads();
  const text = document.body.textContent || '';
  expect(text).toContain('native engine');
  expect(text).toContain('qBittorrent');
 });

 it('shows live swarm facts and ETA per card', async () => {
  await openDownloads();
  const text = document.body.textContent || '';
  expect(text).toContain('12 peers');
  expect(text).toContain('3 connected seeds');
  expect(text).toContain('ETA 9 min');
  expect(text).toContain('5.2 MB/s');
 });
});

describe('error modal layering', () => {
 it('surfaces errors as a topmost dialog while other modals are open', async () => {
  vi.spyOn(API.prototype, 'call').mockRejectedValue(new Error('the tracker refused this release'));
  await openDownloads();
  const cards = document.querySelectorAll('.download-list article');
  await act(async () => {
   Array.from(cards[0].querySelectorAll('button')).find(b => b.textContent === 'Retry download')?.click();
  });
  await settle();
  const dialog = document.querySelector('[role="alertdialog"]');
  expect(dialog).not.toBeNull();
  expect(dialog!.textContent).toContain('the tracker refused this release');
  const overlays = Array.from(document.querySelectorAll('.overlay'));
  expect(overlays[overlays.length - 1]).toBe(dialog!.closest('.overlay'));
 });
});
