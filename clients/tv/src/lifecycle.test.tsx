// @vitest-environment happy-dom
import { h, render } from 'preact';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import type { API, Download, PlaybackPreferences } from '@torrent-tv/shared';
import type { TVPlatformHooks } from './platform';

const mockDownload: Download = {
  id: 'dl-test-1',
  releaseId: 'rel-test-1',
  trackerId: 'filelist',
  trackerName: 'FileList',
  engineId: 'engine-1',
  fileIndex: 0,
  filePath: 'video.mkv',
  mimeType: 'video/x-matroska',
  streamUrl: '/stream/dl-test-1',
  sizeBytes: 1_000_000_000,
  downloadedBytes: 1_000_000_000,
  progress: 1,
  state: 'completed',
  playbackMode: 'progressive',
  speedBytesPerSecond: 0,
  etaSeconds: 0,
  peers: 0,
  seeds: 0,
  leased: false,
  error: '',
  createdAt: '2026-09-07T00:00:00Z',
  updatedAt: '2026-09-07T00:00:00Z',
};

// Global fetch mock installed BEFORE main.tsx evaluation so boot cannot touch network
const defaultFetchMock = vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
  const url = String(input);
  if (url.includes('/catalog/titles')) {
    return { ok: true, status: 200, json: async () => ({ items: [{ id: 'title-1', title: 'Film 1', kind: 'movie', resolutions: ['1080p'], categories: ['film'], bestSeeders: 10, sourceCount: 1 }], nextCursor: null, total: 1 }) };
  }
  if (url.includes('/catalog/facets')) {
    return { ok: true, status: 200, json: async () => ({ categories: [], kinds: [], resolutions: [], hdr: [], qualities: [], codecs: [] }) };
  }
  if (url.includes('/downloads')) {
    return { ok: true, status: 200, json: async () => ({ items: [mockDownload], nextCursor: null, total: 1 }) };
  }
  if (url.includes('/jobs')) {
    return { ok: true, status: 200, json: async () => ({ items: [], nextCursor: null, total: 0 }) };
  }
  if (url.includes('/portal/state')) {
    return { ok: true, status: 200, json: async () => ({ registered: false, email: null, links: [], message: null }) };
  }
  if (url.includes('/updates/current')) {
    return { ok: true, status: 200, json: async () => ({ status: 'idle', updateAvailable: false, currentVersion: '1.0.0' }) };
  }
  if (url.includes('/state')) {
    return { ok: true, status: 200, json: async () => ({ favorites: [], continueWatching: [], recent: [], watched: [] }) };
  }
  return { ok: true, status: 200, json: async () => ({}) };
});
globalThis.fetch = defaultFetchMock;

class MockEventSource {
  static instances: MockEventSource[] = [];
  url: string;
  closed = false;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  listeners = new Map<string, Function[]>();

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }

  close() {
    this.closed = true;
  }

  addEventListener(type: string, fn: Function) {
    const list = this.listeners.get(type) || [];
    list.push(fn);
    this.listeners.set(type, list);
  }

  removeEventListener(type: string, fn: Function) {
    const list = this.listeners.get(type) || [];
    this.listeners.set(type, list.filter(item => item !== fn));
  }
}
(globalThis as unknown as { EventSource: unknown }).EventSource = MockEventSource;
(window as unknown as { EventSource: unknown }).EventSource = MockEventSource;

// Storage polyfill before main imports
if (typeof globalThis.localStorage === 'undefined' || !globalThis.localStorage.getItem) {
  const store = new Map<string, string>();
  globalThis.localStorage = {
    getItem: (key: string) => store.get(key) || null,
    setItem: (key: string, val: string) => { store.set(key, val); },
    removeItem: (key: string) => { store.delete(key); },
    clear: () => { store.clear(); },
    get length() { return store.size; },
    key: () => null,
  } as Storage;
}

// Static import cannot work here: main.tsx asserts the presence of #app container
// during module evaluation, so the container must be attached before main loads.
const appRoot = document.createElement('div');
appRoot.id = 'app';
document.body.appendChild(appRoot);

const { App, Player } = await import('./main');

// Immediately unmount the evaluation-time instance so it cannot run background timers/reconnect loops
render(null, appRoot);
appRoot.remove();

// Helper to let Preact and happy-dom complete microtask turns and effect commits
const tick = (ms = 25) => new Promise(r => setTimeout(r, ms));

interface MockAVPlayListener {
  onbufferingstart?: () => void;
  onbufferingprogress?: (progress: number) => void;
  onbufferingcomplete?: () => void;
  onstreamcompleted?: () => void;
  oncurrentplaytime?: (time: number) => void;
  onsubtitlechange?: (duration: number, text: string) => void;
  onerror?: (error: string) => void;
  ontrackschanged?: () => void;
}

interface MockAVPlayInstance {
  open: Mock;
  setDisplayRect: Mock;
  setDisplayMethod: Mock;
  setListener: Mock;
  prepareAsync: Mock;
  play: Mock;
  pause: Mock;
  seekTo: Mock;
  stop: Mock;
  close: Mock;
  getDuration: Mock;
  getTotalTrackInfo: Mock;
  setSelectTrack: Mock;
  setSilentSubtitle: Mock;
  setSubtitlePosition: Mock;
  setExternalSubtitlePath: Mock;
  readonly listener: MockAVPlayListener;
  triggerPrepareSuccess(): void;
  triggerPrepareError(msg: string): void;
  triggerTracksChanged(): void;
}

function createMockAVPlay(): MockAVPlayInstance {
  let listener: MockAVPlayListener = {};
  let prepareSuccessCallback: (() => void) | null = null;
  let prepareErrorCallback: ((err: string) => void) | null = null;

  return {
    open: vi.fn(),
    setDisplayRect: vi.fn(),
    setDisplayMethod: vi.fn(),
    setListener: vi.fn((l: MockAVPlayListener) => { listener = l; }),
    prepareAsync: vi.fn((success: () => void, error: (msg: string) => void) => {
      prepareSuccessCallback = success;
      prepareErrorCallback = error;
    }),
    play: vi.fn(),
    pause: vi.fn(),
    seekTo: vi.fn(),
    stop: vi.fn(),
    close: vi.fn(),
    getDuration: vi.fn(() => 120_000),
    getTotalTrackInfo: vi.fn(() => []),
    setSelectTrack: vi.fn(),
    setSilentSubtitle: vi.fn(),
    setSubtitlePosition: vi.fn(),
    setExternalSubtitlePath: vi.fn(),

    get listener() { return listener; },
    triggerPrepareSuccess() { prepareSuccessCallback?.(); },
    triggerPrepareError(msg: string) { prepareErrorCallback?.(msg); },
    triggerTracksChanged() { listener.ontrackschanged?.(); },
  };
}

const defaultPreferences: PlaybackPreferences = {
  audioLanguage: 'en',
  audioTrackIndex: -1,
  subtitleLanguage: 'ro',
  subtitleMode: 'auto',
};

describe('Player suspension and visible return lifecycle', () => {
  let container: HTMLDivElement;
  let mockAVPlay: MockAVPlayInstance;
  let visibilityListener: ((visible: boolean) => void) | null = null;
  let mockApi: API;
  let onComplete: Mock;
  let onClose: Mock;
  let onStateChanged: Mock;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);

    mockAVPlay = createMockAVPlay();
    (window as unknown as { webapis?: { avplay: unknown } }).webapis = { avplay: mockAVPlay };

    const testPlatform: TVPlatformHooks = {
      getNetworkInfo: async () => null,
      openExternal: async () => false,
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: listener => {
        visibilityListener = listener;
        return () => { visibilityListener = null; };
      },
    };
    window.FileListTVPlatform = testPlatform;

    onComplete = vi.fn();
    onClose = vi.fn();
    onStateChanged = vi.fn();

    mockApi = {
      base: 'http://server.lan:8097',
      streamURL: (path: string) => `http://server.lan:8097${path}`,
      updatePlayback: vi.fn().mockResolvedValue({}),
      playbackPreferences: vi.fn().mockResolvedValue(defaultPreferences),
      updatePlaybackPreferences: vi.fn().mockResolvedValue({}),
      downloads: vi.fn().mockResolvedValue({ items: [mockDownload], nextCursor: null, total: 1 }),
      subtitles: vi.fn().mockResolvedValue({ items: [] }),
      prepareSubtitle: vi.fn().mockResolvedValue({ url: '/sub.vtt' }),
      diagnostic: vi.fn().mockResolvedValue(undefined),
      call: vi.fn().mockResolvedValue({}),
    } as unknown as API;
  });

  afterEach(() => {
    expect(mockAVPlay.setExternalSubtitlePath).not.toHaveBeenCalled();
    render(null, container);
    container.remove();
    delete (window as unknown as { webapis?: unknown }).webapis;
    delete window.FileListTVPlatform;
  });

  it('preserves saved position and reopens exactly once with autoplay when suspended while playing', async () => {
    render(
      h(Player, {
        api: mockApi,
        download: mockDownload,
        resumeMs: 5_000,
        preferences: defaultPreferences,
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );

    await tick();
    expect(mockAVPlay.open).toHaveBeenCalledTimes(1);

    // Media prepares successfully and begins playing
    mockAVPlay.triggerPrepareSuccess();
    expect(mockAVPlay.play).toHaveBeenCalledTimes(1);

    // Playback advances to 45 seconds
    mockAVPlay.listener.oncurrentplaytime?.(45_000);

    // Application is suspended (e.g. user pressed Home or TV switched inputs)
    visibilityListener?.(false);

    // 1. Position must be saved immediately
    expect(mockApi.updatePlayback).toHaveBeenCalledWith('dl-test-1', 45_000, 120_000);
    // 2. AVPlay must be stopped and closed (once on open, once on suspend)
    expect(mockAVPlay.stop).toHaveBeenCalledTimes(2);
    expect(mockAVPlay.close).toHaveBeenCalledTimes(2);

    // Application returns to foreground
    visibilityListener?.(true);

    // 3. Exactly one reopened instance
    expect(mockAVPlay.open).toHaveBeenCalledTimes(2);

    // Complete preparation on the resumed session
    mockAVPlay.triggerPrepareSuccess();

    // 4. Seeked to saved position
    expect(mockAVPlay.seekTo).toHaveBeenCalledWith(45_000);
    // 5. Retains playing state (autoplay = true because user was playing before suspend)
    expect(mockAVPlay.play).toHaveBeenCalledTimes(2);
  });

  it('retains paused state across suspension and does not treat canplay as playing', async () => {
    render(
      h(Player, {
        api: mockApi,
        download: mockDownload,
        resumeMs: 10_000,
        preferences: defaultPreferences,
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );

    await tick();
    mockAVPlay.triggerPrepareSuccess();
    expect(mockAVPlay.play).toHaveBeenCalledTimes(1);

    // User pauses playback
    const pauseBtn = container.querySelector<HTMLButtonElement>('[data-player-control="play"]')!;
    pauseBtn.click();
    await tick(10);
    expect(mockAVPlay.pause).toHaveBeenCalledTimes(1);

    // Advance playtime while paused
    mockAVPlay.listener.oncurrentplaytime?.(20_000);

    // Suspend while paused
    visibilityListener?.(false);

    expect(mockApi.updatePlayback).toHaveBeenCalledWith('dl-test-1', 20_000, 120_000);
    expect(mockAVPlay.stop).toHaveBeenCalledTimes(2);
    expect(mockAVPlay.close).toHaveBeenCalledTimes(2);

    // Return to visible
    visibilityListener?.(true);

    // Reopened at saved position
    expect(mockAVPlay.open).toHaveBeenCalledTimes(2);

    // In prepareAsync callback, autoplay is false -> must NOT call play()
    mockAVPlay.triggerPrepareSuccess();
    await tick(10);
    expect(mockAVPlay.seekTo).toHaveBeenCalledWith(20_000);
    expect(mockAVPlay.play).toHaveBeenCalledTimes(1);

    // Buffering complete / canplay event arrives during paused prepare/seek
    mockAVPlay.listener.onbufferingcomplete?.();
    await tick(10);

    // Must NOT mark playing
    expect(mockAVPlay.play).toHaveBeenCalledTimes(1);
    const message = container.querySelector('.player-message');
    expect(message?.textContent).toBe('Paused');
  });

  it('cleanly cancels in-flight prepare when suspended and reopens fresh', async () => {
    render(
      h(Player, {
        api: mockApi,
        download: mockDownload,
        resumeMs: 0,
        preferences: defaultPreferences,
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );

    await tick();
    expect(mockAVPlay.open).toHaveBeenCalledTimes(1);

    // Suspend BEFORE prepare completes (still preparing)
    visibilityListener?.(false);
    expect(mockAVPlay.stop).toHaveBeenCalledTimes(2);
    expect(mockAVPlay.close).toHaveBeenCalledTimes(2);

    // Stale prepareSuccess callback arrives late from the cancelled session
    mockAVPlay.triggerPrepareSuccess();
    // Must be ignored (session token invalidated)
    expect(mockAVPlay.play).not.toHaveBeenCalled();

    // Return to visible
    visibilityListener?.(true);
    expect(mockAVPlay.open).toHaveBeenCalledTimes(2);

    // Resumed prepare completes (was not playing before suspend, so prepares to paused)
    mockAVPlay.triggerPrepareSuccess();
    await tick(10);
    expect(mockAVPlay.play).not.toHaveBeenCalled();
  });

  it('clears recovery polling timers on suspension and reopens cleanly on return', async () => {
    const downloadingItem: Download = {
      ...mockDownload,
      progress: 0.5,
      downloadedBytes: 500_000_000,
      state: 'downloading',
    };
    mockApi.downloads = vi.fn().mockResolvedValue({ items: [downloadingItem], nextCursor: null, total: 1 });

    render(
      h(Player, {
        api: mockApi,
        download: downloadingItem,
        resumeMs: 15_000,
        preferences: defaultPreferences,
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );

    await tick();

    // Trigger an error to enter the recovery polling state
    mockAVPlay.listener.onerror?.('Segment download stalled');
    await tick(20);

    const initialPollCalls = (mockApi.downloads as Mock).mock.calls.length;

    // Suspend while in recovery
    visibilityListener?.(false);
    expect(mockAVPlay.stop).toHaveBeenCalled();

    // Polling must NOT continue while hidden
    await tick(100);
    expect((mockApi.downloads as Mock).mock.calls.length).toBe(initialPollCalls);

    // Return to visible
    visibilityListener?.(true);

    // Reopened at current position
    expect(mockAVPlay.open).toHaveBeenCalledTimes(2);
  });

  it('suppresses stale next-episode events delivered after suspension', async () => {
    render(
      h(Player, {
        api: mockApi,
        download: mockDownload,
        resumeMs: 110_000,
        preferences: defaultPreferences,
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );

    await tick();
    mockAVPlay.triggerPrepareSuccess();

    const staleListener = mockAVPlay.listener;

    // Suspend the player
    visibilityListener?.(false);

    // Stale completion event fires from previous media instance
    staleListener.onstreamcompleted?.();
    await tick(20);

    // onComplete must NOT be called
    expect(onComplete).not.toHaveBeenCalled();
  });
});

describe('App SSE lifecycle and visible snapshot reconciliation', () => {
  let container: HTMLDivElement;
  let visibilityListener: ((visible: boolean) => void) | null = null;

  beforeEach(() => {
    MockEventSource.instances = [];
    globalThis.fetch = defaultFetchMock;

    container = document.createElement('div');
    document.body.appendChild(container);

    const testPlatform: TVPlatformHooks = {
      getNetworkInfo: async () => null,
      openExternal: async () => false,
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: listener => {
        visibilityListener = listener;
        return () => { visibilityListener = null; };
      },
    };
    window.FileListTVPlatform = testPlatform;

    localStorage.setItem('filelist.serverUrl', 'http://server.lan:8097');
  });

  afterEach(() => {
    render(null, container);
    container.remove();
    localStorage.clear();
    delete window.FileListTVPlatform;
  });
  it('closes SSE stream on hidden, suppresses retry, and reopens exactly one stream on visible return', async () => {
    let resolveTitles!: (res: Response) => void;
    const titlePromise = new Promise<Response>(resolve => { resolveTitles = resolve; });
    let interceptNextTitles = false;

    globalThis.fetch = vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/catalog/titles') && interceptNextTitles) {
        interceptNextTitles = false;
        return titlePromise;
      }
      return defaultFetchMock(input);
    });

    render(h(App, null), container);

    // Allow connect() and initial SSE open() to execute
    await tick(60);

    // Initial EventSource created
    expect(MockEventSource.instances.length).toBe(1);
    const stream1 = MockEventSource.instances[0];
    expect(stream1.closed).toBe(false);

    // Simulate stream connected
    stream1.onopen?.();

    // App goes to background (hidden)
    visibilityListener?.(false);

    // 1. Hidden closes the stream immediately
    expect(stream1.closed).toBe(true);

    // 2. Suppresses retries while hidden
    await tick(50);
    // No new streams created while hidden
    expect(MockEventSource.instances.length).toBe(1);

    // Arm intercept for the visible return refresh
    interceptNextTitles = true;

    // App returns to foreground (visible)
    visibilityListener?.(true);
    await tick(10);

    // 3. Opens exactly one new stream (no duplicates)
    expect(MockEventSource.instances.length).toBe(2);
    const stream2 = MockEventSource.instances[1];
    expect(stream2.closed).toBe(false);

    // 4. Stream handshake fires onopen BEFORE refresh fetches resolve
    stream2.onopen?.();
    await tick(10);

    // Now titles fetch resolves after the onopen handshake
    resolveTitles({
      ok: true,
      status: 200,
      json: async () => ({
        items: [{ id: 'resumed-title', title: 'Resumed Film', kind: 'movie', resolutions: ['1080p'], categories: ['film'], bestSeeders: 10, sourceCount: 1 }],
        nextCursor: null,
        total: 1,
      }),
    } as unknown as Response);
    await tick(50);

    // Fresh rail data is applied (not discarded by onopen's generation bump)
    expect(container.textContent).toContain('Resumed Film');

    // 5. Refreshes snapshots (titles, facets, jobs, state, downloads)
    const fetchCalls = (globalThis.fetch as unknown as Mock).mock.calls.map((c: unknown[]) => String(c[0]));
    expect(fetchCalls.some((url: string) => url.includes('/catalog/titles'))).toBe(true);
    expect(fetchCalls.some((url: string) => url.includes('/catalog/facets'))).toBe(true);
    expect(fetchCalls.some((url: string) => url.includes('/downloads'))).toBe(true);
  });
  it('drops late snapshot responses when generation changed before response lands', async () => {
    let resolveTitles!: (res: Response) => void;
    const titlePromise = new Promise<Response>(resolve => { resolveTitles = resolve; });
    let interceptNextTitles = false;

    globalThis.fetch = vi.fn().mockImplementation(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/catalog/titles') && interceptNextTitles) {
        interceptNextTitles = false; // only intercept the visible-reconciliation call
        return titlePromise;
      }
      return defaultFetchMock(input);
    });

    render(h(App, null), container);
    await tick(60);
    expect(MockEventSource.instances.length).toBe(1);

    // Suspend app
    visibilityListener?.(false);

    // Return to visible: arm pending titles intercept
    interceptNextTitles = true;
    visibilityListener?.(true);
    await tick(10);

    expect(MockEventSource.instances.length).toBe(2);
    const stream2 = MockEventSource.instances[1];

    // Stream handshake lands first (does not invalidate epoch)
    stream2.onopen?.();
    await tick(10);

    // A fresh titles request is in flight. Now immediately suspend again before it resolves!
    // This newer hidden edge advances visibilityEpoch
    visibilityListener?.(false);

    // Late response now arrives from the previous visible era
    resolveTitles({
      ok: true,
      status: 200,
      json: async () => ({ items: [{ id: 'stale-title', title: 'Stale', kind: 'movie', resolutions: ['1080p'], categories: ['film'], bestSeeders: 10, sourceCount: 1 }], nextCursor: null, total: 1 }),
    } as unknown as Response);
    await tick(30);
    // The stale title was dropped and not applied because generation was bumped by the hidden edge
    const titlesInDOM = container.textContent;
    expect(titlesInDOM).not.toContain('Stale');
  });
});
describe('Player track selection and subtitle behavior honesty', () => {
  let container: HTMLDivElement;
  let mockAVPlay: MockAVPlayInstance;
  let mockApi: API;
  let onClose: Mock;
  let onComplete: Mock;
  let onStateChanged: Mock;

  const mockTracks = [
    { index: 0, type: 'AUDIO', extra_info: JSON.stringify({ track_lang: 'eng', track_title: 'English' }) },
    { index: 1, type: 'AUDIO', extra_info: JSON.stringify({ track_lang: 'ron', track_title: 'Romanian' }) },
    { index: 2, type: 'TEXT', extra_info: JSON.stringify({ track_lang: 'ron', track_title: 'Romanian' }) },
  ];

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);

    mockAVPlay = createMockAVPlay();
    mockAVPlay.getTotalTrackInfo = vi.fn(() => [...mockTracks]);
    (window as unknown as { webapis?: { avplay: unknown } }).webapis = { avplay: mockAVPlay };

    const testPlatform: TVPlatformHooks = {
      getNetworkInfo: async () => null,
      openExternal: async () => false,
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };
    window.FileListTVPlatform = testPlatform;

    onComplete = vi.fn();
    onClose = vi.fn();
    onStateChanged = vi.fn();

    mockApi = {
      base: 'http://server.lan:8097',
      streamURL: (path: string) => `http://server.lan:8097${path}`,
      updatePlayback: vi.fn().mockResolvedValue({}),
      playbackPreferences: vi.fn().mockResolvedValue(defaultPreferences),
      updatePlaybackPreferences: vi.fn().mockResolvedValue({}),
      downloads: vi.fn().mockResolvedValue({ items: [mockDownload], nextCursor: null, total: 1 }),
      subtitles: vi.fn().mockResolvedValue({ items: [] }),
      prepareSubtitle: vi.fn().mockResolvedValue({ url: '/sub.vtt' }),
      diagnostic: vi.fn().mockResolvedValue(undefined),
      call: vi.fn().mockResolvedValue({}),
    } as unknown as API;
  });

  afterEach(() => {
    expect(mockAVPlay.setExternalSubtitlePath).not.toHaveBeenCalled();
    render(null, container);
    container.remove();
    delete (window as unknown as { webapis?: unknown }).webapis;
    delete window.FileListTVPlatform;
  });

  it('audio selection failure surfaces a visible message without saving preferences or success toast', async () => {
    render(
      h(Player, {
        api: mockApi,
        download: mockDownload,
        resumeMs: 0,
        preferences: { ...defaultPreferences, subtitleMode: 'off' },
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );
    await tick();
    mockAVPlay.triggerPrepareSuccess();
    await tick(10);

    // Open audio menu
    const audioBtn = container.querySelector<HTMLButtonElement>('[data-player-control="audio"]')!;
    expect(audioBtn).not.toBeNull();
    audioBtn.click();
    await tick(10);

    const dialog = container.querySelector('.player-dialog');
    expect(dialog).not.toBeNull();

    // Configure setSelectTrack to fail
    mockAVPlay.setSelectTrack.mockImplementationOnce(() => {
      throw new RangeError('refused by webOS');
    });

    // Select Romanian audio track (index 1)
    const trackBtn = container.querySelector<HTMLButtonElement>('[data-focus-key="player-audio-1"]')!;
    expect(trackBtn).not.toBeNull();
    trackBtn.click();
    await tick(10);

    // Visible failure message must surface
    const message = container.querySelector('.player-message');
    expect(message?.textContent).toBe('Could not select the audio track: refused by webOS');

    // Menu must be closed
    expect(container.querySelector('.player-dialog')).toBeNull();

    // Preferences must NOT be saved
    expect(mockApi.updatePlaybackPreferences).not.toHaveBeenCalled();

    // No success toast should appear later
    await tick(150);
    expect(container.querySelector('.player-message')?.textContent).toBe('Could not select the audio track: refused by webOS');
  });

  it('subtitle selection failure surfaces the subtitle message without preference save or success toast', async () => {
    render(
      h(Player, {
        api: mockApi,
        download: mockDownload,
        resumeMs: 0,
        preferences: { ...defaultPreferences, subtitleMode: 'off' },
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );
    await tick();
    mockAVPlay.triggerPrepareSuccess();
    await tick(10);

    // Open subtitles menu
    const subBtn = container.querySelector<HTMLButtonElement>('[data-player-control="subtitles"]')!;
    expect(subBtn).not.toBeNull();
    subBtn.click();
    await tick(10);

    const dialog = container.querySelector('.player-dialog');
    expect(dialog).not.toBeNull();

    // Configure setSelectTrack to fail for TEXT
    mockAVPlay.setSelectTrack.mockImplementationOnce(() => {
      throw new RangeError('Native subtitle tracks unavailable');
    });

    // Select native subtitle track (index 2)
    const nativeBtn = container.querySelector<HTMLButtonElement>('[data-focus-key="player-native-subtitle-2"]')!;
    expect(nativeBtn).not.toBeNull();
    nativeBtn.click();
    await tick(10);

    // Visible failure message must surface
    const message = container.querySelector('.player-message');
    expect(message?.textContent).toBe('Could not select the subtitle: Native subtitle tracks unavailable');

    // Menu must be closed
    expect(container.querySelector('.player-dialog')).toBeNull();

    // Preferences must NOT be saved
    expect(mockApi.updatePlaybackPreferences).not.toHaveBeenCalled();
  });

  it('successful audio selection persists preferences and shows the selected toast', async () => {
    render(
      h(Player, {
        api: mockApi,
        download: mockDownload,
        resumeMs: 0,
        preferences: defaultPreferences,
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );
    await tick();
    mockAVPlay.triggerPrepareSuccess();
    await tick(10);

    const audioBtn = container.querySelector<HTMLButtonElement>('[data-player-control="audio"]')!;
    audioBtn.click();
    await tick(10);

    const trackBtn = container.querySelector<HTMLButtonElement>('[data-focus-key="player-audio-1"]')!;
    trackBtn.click();
    await tick(10);

    expect(mockAVPlay.setSelectTrack).toHaveBeenCalledWith('AUDIO', 1);
    expect(mockApi.updatePlaybackPreferences).toHaveBeenCalledWith('dl-test-1', expect.objectContaining({
      audioTrackIndex: 1,
      audioLanguage: 'ron',
    }));
    expect(container.querySelector('.player-message')?.textContent).toContain('selected');
  });

  it('delayed audio-track arrival after metadata triggers exactly one refreshTracks', async () => {
    let tracksAvailable: Array<{ index: number; type: string; extra_info: string }> = [];
    mockAVPlay.getTotalTrackInfo = vi.fn(() => [...tracksAvailable]);

    render(
      h(Player, {
        api: mockApi,
        download: mockDownload,
        resumeMs: 0,
        preferences: defaultPreferences,
        onClose,
        onStateChanged,
        onComplete,
      }),
      container
    );
    await tick();
    mockAVPlay.triggerPrepareSuccess();
    await tick(10);

    // Initially no audio tracks
    const audioBtn = container.querySelector<HTMLButtonElement>('[data-player-control="audio"]')!;
    expect(audioBtn.textContent).toBe('Audio (0)');

    const callsBefore = (mockAVPlay.getTotalTrackInfo as Mock).mock.calls.length;

    // Late track arrival: populate tracks and trigger change event
    tracksAvailable = [
      { index: 0, type: 'AUDIO', extra_info: JSON.stringify({ track_lang: 'eng', track_title: 'English' }) },
    ];
    mockAVPlay.triggerTracksChanged();
    await tick(10);

    // Exactly one refreshTracks occurred
    expect((mockAVPlay.getTotalTrackInfo as Mock).mock.calls.length).toBe(callsBefore + 1);
    expect(container.querySelector<HTMLButtonElement>('[data-player-control="audio"]')?.textContent).toBe('Audio (1)');
  });
});
