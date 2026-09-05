import { render } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { API, type CatalogDetail, type CatalogSource, type Download, type HouseholdState } from '@torrent-tv/shared';
import { App } from './src';

// App-level DOM tests for the non-player overlays (ticket #57): the Detail
// overlay, the source picker, and the Downloads removal confirm close on
// Escape. The Browser player's Escape chain (fullscreen → panel → chrome →
// leave) is owned by player-shortcuts.test.tsx and is not exercised here.

const sourceFixtures: CatalogSource[] = [
  { release: { id: 'r1', name: 'release-r1', category: 'Movies', sizeBytes: 1024, seeders: 8, leechers: 0, freeleech: false }, parsed: { title: 'Alpha', sortTitle: 'alpha', kind: 'movie', resolution: '1080p' }, filePath: '/data/r1.mkv' },
  { release: { id: 'r2', name: 'release-r2', category: 'Movies', sizeBytes: 1024, seeders: 3, leechers: 0, freeleech: false }, parsed: { title: 'Alpha', sortTitle: 'alpha', kind: 'movie', resolution: '1080p' }, filePath: '/data/r2.mkv' },
];

// Two sources so the Detail's play action opens the source picker instead of
// preparing a download and mounting the Browser player.
const detail: CatalogDetail = {
  title: { id: 't1', title: 'Alpha', kind: 'movie', categories: ['Movies'], resolutions: ['1080p'], sourceCount: 2, bestSeeders: 8, largestSizeBytes: 2048 },
  seasons: [],
  sources: sourceFixtures,
};

const download: Download = {
  id: 'dl-1', releaseId: 'r1', engineId: 'qb:x', fileIndex: 0, filePath: 'alpha.mkv',
  displayTitle: 'Alpha', releaseName: 'release-r1', category: 'Movies',
  mimeType: 'video/x-matroska', sizeBytes: 1024, state: 'downloading', progress: 0.5,
  playbackMode: 'progressive', downloadedBytes: 512, speedBytesPerSecond: 0, etaSeconds: 0,
  peers: 0, seeds: 1, leased: false, streamUrl: '/api/v1/streams/dl-1',
};

const emptyHousehold: HouseholdState = { favorites: [], continueWatching: [], recent: [], watched: [] };

// App opens a live EventSource at mount; these tests never exercise it.
class FakeEventSource { addEventListener() { } close() { } }

const mountedHosts: HTMLElement[] = [];

async function mountApp() {
  const host = document.createElement('div');
  document.body.appendChild(host);
  mountedHosts.push(host);
  await act(async () => { render(<App />, host) });
  await act(async () => { }); // flush the initial state/titles/facets loads
}

// Lets history.back()-style closes land: happy-dom dispatches popstate on a
// macrotask, so a single act() flush is not always enough.
async function settle() {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)) });
}

function pressEscape() {
  act(() => { document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })) });
}

const card = () => Array.from(document.querySelectorAll<HTMLButtonElement>('.media-card')).find(button => button.getAttribute('aria-label') === 'Open Alpha')!;

const sidebarButton = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>('.sidebar nav button')).find(button => button.textContent?.includes(label))!;

beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource);
  vi.spyOn(API.prototype, 'state').mockResolvedValue(emptyHousehold);
  vi.spyOn(API.prototype, 'facets').mockResolvedValue({ categories: [], kinds: [], resolutions: [], hdr: [], qualities: [], codecs: [] });
  vi.spyOn(API.prototype, 'titles').mockResolvedValue({ items: [detail.title], nextCursor: null, total: 1 });
  vi.spyOn(API.prototype, 'title').mockResolvedValue(detail);
  vi.spyOn(API.prototype, 'ensureMetadata').mockResolvedValue({ queued: 0 });
  vi.spyOn(API.prototype, 'downloads').mockResolvedValue({ items: [download], nextCursor: null, total: 1 });
});

afterEach(() => {
  // Unmount for real so document-level key listeners run their cleanup.
  while (mountedHosts.length) render(null, mountedHosts.pop()!);
  document.body.innerHTML = '';
  window.history.replaceState(null, '', '/');
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function openDetailOverlay() {
  await mountApp();
  await act(async () => { card().click() });
  expect(document.querySelector('.overlay')).not.toBeNull();
}

describe('overlay Escape', () => {
  it('closes the Detail overlay on Escape', async () => {
    await openDetailOverlay();
    pressEscape();
    await settle();
    expect(document.querySelector('.overlay')).toBeNull();
  });

  it('closes the source picker on Escape while the Detail overlay stays open', async () => {
    await openDetailOverlay();
    const play = Array.from(document.querySelectorAll<HTMLButtonElement>('.overlay .actions button')).find(button => button.textContent?.includes('Play'))!;
    await act(async () => { play.click() });
    expect(document.querySelector('.overlay.picker')).not.toBeNull();
    pressEscape();
    await settle();
    expect(document.querySelector('.overlay.picker')).toBeNull();
    expect(document.querySelector('.overlay')?.getAttribute('aria-label')).toBe('Alpha details');
  });

  it('closes the Downloads removal confirm on Escape', async () => {
    await mountApp();
    await act(async () => { sidebarButton('Downloads').click() });
    await settle(); // downloads load
    await act(async () => { document.querySelector<HTMLButtonElement>('.download-list .danger-button')!.click() });
    expect(document.querySelector('.removal-confirm')).not.toBeNull();
    pressEscape();
    await settle();
    expect(document.querySelector('.overlay')).toBeNull();
  });

  it('keeps the Downloads removal confirm open on Escape while the removal is in flight', async () => {
    const pending = Promise.withResolvers<void>();
    vi.spyOn(API.prototype, 'deleteDownload').mockReturnValueOnce(pending.promise);
    await mountApp();
    await act(async () => { sidebarButton('Downloads').click() });
    await settle(); // downloads load
    await act(async () => { document.querySelector<HTMLButtonElement>('.download-list .danger-button')!.click() });
    await act(async () => { document.querySelector<HTMLButtonElement>('.confirm-actions .danger-button')!.click() });
    expect(document.querySelector('.confirm-actions')!.textContent).toContain('Deleting…');
    pressEscape();
    await settle();
    expect(document.querySelector('.removal-confirm')).not.toBeNull(); // the removing guard holds
    await act(async () => { pending.resolve() });
    await settle();
    expect(document.querySelector('.overlay')).toBeNull();
  });
});
