import { render } from 'preact';
import { act } from 'preact/test-utils';
import { type Mock, type MockInstance, afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { API, type Download, type MediaInfo, type PlaybackPreferences } from '@torrent-tv/shared';
import { BrowserPlayer } from './src';

// Component-seam tests for Player shortcuts: real BrowserPlayer in happy-dom,
// real keydown events at the player surface, observable video/chrome state only.

const download: Download = {
  id: 'dl-1', releaseId: 'r', engineId: 'qb:x', fileIndex: 0, filePath: 'a.mkv',
  mimeType: 'video/x-matroska', sizeBytes: 1, state: 'downloading', progress: 0.1,
  downloadedBytes: 0, speedBytesPerSecond: 0, etaSeconds: 0, peers: 0, seeds: 0,
  leased: false, streamUrl: '/api/v1/streams/dl-1', playbackMode: 'progressive',
};

const preferences: PlaybackPreferences = { audioLanguage: 'en', audioTrackIndex: -1, subtitleLanguage: 'ro', subtitleMode: 'off' };

const mediaInfo: MediaInfo = {
  durationMs: 600_000,
  audioTracks: [{ streamIndex: 0, codec: 'aac', default: true, language: 'en' }],
} as MediaInfo;

let playSpy: MockInstance<typeof HTMLMediaElement.prototype.play>;
let pauseSpy: MockInstance<typeof HTMLMediaElement.prototype.pause>;

// happy-dom under vitest does not install storage; the player reads the
// global localStorage (best-effort) — give tests a deterministic in-memory one.
const memoryStorage = (() => {
  const map = new Map<string, string>();
  return {
    getItem: (key: string) => (map.has(key) ? (map.get(key) as string) : null),
    setItem: (key: string, value: string) => { map.set(key, String(value)) },
    removeItem: (key: string) => { map.delete(key) },
    clear: () => map.clear(),
  };
})();
Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage, configurable: true });

const mountedHosts: HTMLElement[] = [];

beforeEach(() => {
  localStorage.clear();
  vi.useFakeTimers();
  // happy-dom media elements are inert: fake the transport and mirror state
  // through the same events the real element would fire. `playing` (not
  // `play`) is what the player's JSX listens for.
  playSpy = vi.spyOn(HTMLMediaElement.prototype, 'play').mockImplementation(function(this: HTMLMediaElement) {
    Object.defineProperty(this, 'paused', { value: false, configurable: true });
    this.dispatchEvent(new Event('playing'));
    return Promise.resolve();
  });
  pauseSpy = vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(function(this: HTMLMediaElement) {
    Object.defineProperty(this, 'paused', { value: true, configurable: true });
    this.dispatchEvent(new Event('pause'));
    return undefined;
  });
  vi.spyOn(API.prototype, 'mediaInfo').mockResolvedValue(mediaInfo);
  vi.spyOn(API.prototype, 'subtitles').mockResolvedValue({ items: [], warnings: [], nextCursor: null, total: 0 });
  vi.spyOn(API.prototype, 'updatePlayback').mockResolvedValue({} as never);
  vi.spyOn(API.prototype, 'updatePlaybackPreferences').mockImplementation((_sourceId: string, value: PlaybackPreferences) => Promise.resolve(value));
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
  // Unmount for real: document-level listeners must run their cleanup, which
  // innerHTML wiping would skip.
  while (mountedHosts.length) render(null, mountedHosts.pop()!);
  document.body.innerHTML = '';
});

type Mounted = { host: HTMLElement; onClose: Mock; video: HTMLVideoElement; root: HTMLElement };

async function mountPlayer(resumeMs = 0): Promise<Mounted> {
  const host = document.createElement('div');
  document.body.appendChild(host);
  mountedHosts.push(host);
  const onClose = vi.fn();
  await act(async () => {
    render(
      <BrowserPlayer active={{ download, resumeMs, preferences }} onClose={onClose} onStateChanged={vi.fn()} onAdvance={vi.fn()} />,
      host,
    );
  });
  await act(async () => { }); // flush the media-info / subtitles effects
  return { host, onClose, video: host.querySelector('video')!, root: host.querySelector('.video')! };
}

function pressKey(target: HTMLElement, key: string, init: KeyboardEventInit = {}) {
  act(() => {
    target.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init }));
  });
}

const playButton = (mounted: Mounted) => mounted.host.querySelector<HTMLButtonElement>('.player-play')!;

describe('transport shortcuts', () => {
  it('Space toggles playback: paused → playing → paused', async () => {
    const mounted = await mountPlayer();
    expect(playButton(mounted).textContent).toBe('Play');
    pressKey(mounted.root, ' ');
    expect(playButton(mounted).textContent).toBe('Pause');
    pressKey(mounted.root, ' ');
    expect(playButton(mounted).textContent).toBe('Play');
  });

  it('Space does not activate a focused chrome button', async () => {
    const mounted = await mountPlayer();
    playButton(mounted).focus();
    pressKey(mounted.root, ' ');
    // Exactly one transport toggle happened (the shortcut), not two.
    expect(playSpy).toHaveBeenCalledTimes(1);
  });

  it('ArrowRight seeks forward 5s: optimistic position, one committed seek', async () => {
    const mounted = await mountPlayer();
    mounted.video.dispatchEvent(new Event('loadedmetadata'));
    await act(async () => { vi.advanceTimersByTime(0) });
    pressKey(mounted.root, 'ArrowRight');
    const scrubber = mounted.host.querySelector<HTMLInputElement>('.player-scrubber input')!;
    expect(scrubber.value).toBe('5000'); // optimistic target is visible immediately
    expect(mounted.video.currentTime).toBe(0); // no network seek yet
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(mounted.video.currentTime).toBe(5); // committed exactly once
  });

  it('held ArrowRight coalesces repeats into one committed seek', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowRight');
    await act(async () => { vi.advanceTimersByTime(100) });
    pressKey(mounted.root, 'ArrowRight', { repeat: true });
    await act(async () => { vi.advanceTimersByTime(100) });
    pressKey(mounted.root, 'ArrowRight', { repeat: true });
    const scrubber = mounted.host.querySelector<HTMLInputElement>('.player-scrubber input')!;
    expect(scrubber.value).toBe('15000'); // optimistic target followed all three ticks
    expect(mounted.video.currentTime).toBe(0);
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(mounted.video.currentTime).toBe(15); // single commit with the final target
  });

  it('ArrowLeft seeks back 5s and J/L seek ∓10s', async () => {
    const mounted = await mountPlayer(60_000);
    mounted.video.dispatchEvent(new Event('loadedmetadata'));
    await act(async () => { vi.advanceTimersByTime(0) });
    pressKey(mounted.root, 'ArrowLeft');
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(mounted.video.currentTime).toBe(55);
    pressKey(mounted.root, 'l');
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(mounted.video.currentTime).toBe(65);
    pressKey(mounted.root, 'J');
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(mounted.video.currentTime).toBe(55);
  });

  it('ArrowRight before the duration is known: hint, no optimistic jump, no commit', async () => {
    const mediaInfoPending = Promise.withResolvers<MediaInfo>();
    vi.spyOn(API.prototype, 'mediaInfo').mockImplementation(() => mediaInfoPending.promise);
    const mounted = await mountPlayer(60_000);
    pressKey(mounted.root, 'ArrowRight');
    const scrubber = mounted.host.querySelector<HTMLInputElement>('.player-scrubber input')!;
    expect(scrubber.value).toBe('0'); // no optimistic jump while the duration is unknown
    expect(mounted.host.querySelector('.osd-ghost')).toBeNull(); // no +5s ghost at 0%
    expect(mounted.host.querySelector('.osd')!.textContent).toContain('Seek unavailable');
    // Metadata arrives inside the coalescing window: the stale press must not
    // commit a seek that lands on 0:00 over the resume position.
    await act(async () => { mediaInfoPending.resolve(mediaInfo) });
    mounted.video.dispatchEvent(new Event('loadedmetadata'));
    await act(async () => { vi.advanceTimersByTime(0) });
    expect(scrubber.value).toBe('60000'); // resume position untouched
    expect(mounted.video.currentTime).toBe(60);
    await act(async () => { vi.advanceTimersByTime(250) }); // coalescing window elapses
    expect(mounted.video.currentTime).toBe(60); // no seek ever landed at 0
    expect(scrubber.value).toBe('60000');
  });

  it('J before the duration is known: hint, position untouched, no commit', async () => {
    const mediaInfoPending = Promise.withResolvers<MediaInfo>();
    vi.spyOn(API.prototype, 'mediaInfo').mockImplementation(() => mediaInfoPending.promise);
    const mounted = await mountPlayer(60_000);
    pressKey(mounted.root, 'J');
    expect(mounted.host.querySelector('.osd-ghost')).toBeNull(); // no −10s ghost at 0%
    expect(mounted.host.querySelector('.osd')!.textContent).toContain('Seek unavailable');
    await act(async () => { mediaInfoPending.resolve(mediaInfo) });
    mounted.video.dispatchEvent(new Event('loadedmetadata'));
    await act(async () => { vi.advanceTimersByTime(0) });
    expect(mounted.video.currentTime).toBe(60); // resume position untouched
    await act(async () => { vi.advanceTimersByTime(250) }); // coalescing window elapses
    expect(mounted.video.currentTime).toBe(60); // no seek ever landed at 0
    expect(mounted.host.querySelector<HTMLInputElement>('.player-scrubber input')!.value).toBe('60000');
  });

  it('unbound keys do nothing harmful', async () => {
    const mounted = await mountPlayer();
    expect(() => pressKey(mounted.root, 'x')).not.toThrow();
    expect(playSpy).not.toHaveBeenCalled();
  });

  it('shortcuts fire with focus on body, as after clicking the video', async () => {
    const mounted = await mountPlayer();
    (document.activeElement as HTMLElement | null)?.blur?.();
    pressKey(document.body, 'ArrowRight');
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(mounted.video.currentTime).toBe(5);
  });

  it('Escape with body focus follows the chain instead of closing', async () => {
    const mounted = await mountPlayer();
    (document.activeElement as HTMLElement | null)?.blur?.();
    pressKey(document.body, 'Escape');
    expect(mounted.onClose).not.toHaveBeenCalled(); // chrome visible → hidden, not closed
    expect(mounted.root.classList.contains('controls-visible')).toBe(false);
  });
});

describe('volume and mute shortcuts', () => {
  const volumeSlider = (mounted: Mounted) => mounted.host.querySelector<HTMLInputElement>('.player-volume input')!;

  it('ArrowUp raises volume by 2% and persists it', async () => {
    localStorage.setItem('filelist.player.volume', '0.5');
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowUp');
    expect(Number(volumeSlider(mounted).value)).toBeCloseTo(0.52);
    expect(localStorage.getItem('filelist.player.volume')).toBe('0.52');
  });

  it('ArrowDown lowers volume by 2%', async () => {
    localStorage.setItem('filelist.player.volume', '0.5');
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowDown');
    expect(Number(volumeSlider(mounted).value)).toBeCloseTo(0.48);
  });

  it('volume steps clamp to the slider bounds', async () => {
    localStorage.setItem('filelist.player.volume', '0.99');
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowUp');
    pressKey(mounted.root, 'ArrowUp');
    expect(volumeSlider(mounted).value).toBe('1');
  });

  it('M toggles mute and unmute restores the pre-mute volume', async () => {
    localStorage.setItem('filelist.player.volume', '0.5');
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'm');
    expect(volumeSlider(mounted).value).toBe('0'); // muted shows zero
    pressKey(mounted.root, 'm');
    expect(Number(volumeSlider(mounted).value)).toBeCloseTo(0.5);
  });

  it('ArrowUp while muted unmutes first and then applies the step', async () => {
    localStorage.setItem('filelist.player.volume', '0.5');
    localStorage.setItem('filelist.player.muted', 'true');
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowUp');
    expect(Number(volumeSlider(mounted).value)).toBeCloseTo(0.52);
    expect(localStorage.getItem('filelist.player.muted')).toBe('false');
  });

  it('stepping down to zero mutes', async () => {
    localStorage.setItem('filelist.player.volume', '0.02');
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowDown');
    expect(localStorage.getItem('filelist.player.muted')).toBe('true');
    expect(volumeSlider(mounted).value).toBe('0');
  });

  it('arrow keys stay native while a slider is focused', async () => {
    localStorage.setItem('filelist.player.volume', '0.5');
    const mounted = await mountPlayer();
    volumeSlider(mounted).focus();
    pressKey(volumeSlider(mounted), 'ArrowUp');
    // The shortcut layer skipped the event: no persisted 2% step happened.
    expect(localStorage.getItem('filelist.player.volume')).toBe('0.5');
  });
});

describe('fullscreen key and Escape chain', () => {
  function stubEnterFullscreen(mounted: Mounted) {
    mounted.root.requestFullscreen = vi.fn().mockImplementation(function(this: HTMLElement) {
      Object.defineProperty(document, 'fullscreenElement', { value: this, configurable: true });
      document.dispatchEvent(new Event('fullscreenchange'));
      return Promise.resolve();
    });
  }
  function stubExitFullscreen(mounted: Mounted) {
    document.exitFullscreen = vi.fn().mockImplementation(() => {
      Object.defineProperty(document, 'fullscreenElement', { value: null, configurable: true });
      document.dispatchEvent(new Event('fullscreenchange'));
      return Promise.resolve();
    });
  }
  const subtitlesButton = (mounted: Mounted) =>
    Array.from(mounted.host.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent?.startsWith('Subtitles'))!;

  it('F toggles fullscreen', async () => {
    const mounted = await mountPlayer();
    stubEnterFullscreen(mounted);
    stubExitFullscreen(mounted);
    pressKey(mounted.root, 'f');
    expect(mounted.root.requestFullscreen).toHaveBeenCalledTimes(1);
    pressKey(mounted.root, 'f');
    expect(document.exitFullscreen).toHaveBeenCalledTimes(1);
  });

  it('Escape in fullscreen only exits fullscreen', async () => {
    const mounted = await mountPlayer();
    stubEnterFullscreen(mounted);
    stubExitFullscreen(mounted);
    pressKey(mounted.root, 'f');
    pressKey(mounted.root, 'Escape');
    expect(document.exitFullscreen).toHaveBeenCalledTimes(1);
    expect(mounted.onClose).not.toHaveBeenCalled();
  });

  it('Escape with a Player panel open only closes the panel', async () => {
    const mounted = await mountPlayer();
    await act(async () => { subtitlesButton(mounted).click() });
    expect(mounted.host.querySelector('.subtitle-panel')).not.toBeNull();
    pressKey(mounted.root, 'Escape');
    expect(mounted.host.querySelector('.subtitle-panel')).toBeNull();
    expect(mounted.onClose).not.toHaveBeenCalled();
  });

  it('Escape with the chrome visible only hides the chrome', async () => {
    const mounted = await mountPlayer();
    expect(mounted.root.classList.contains('controls-visible')).toBe(true);
    pressKey(mounted.root, 'Escape');
    expect(mounted.root.classList.contains('controls-visible')).toBe(false);
    expect(mounted.onClose).not.toHaveBeenCalled();
  });

  it('Escape with everything closed leaves the player', async () => {
    const mounted = await mountPlayer();
    await act(async () => { vi.advanceTimersByTime(2000) }); // idle auto-hide
    pressKey(mounted.root, 'Escape');
    expect(mounted.onClose).toHaveBeenCalledTimes(1);
  });

  it('transport shortcuts are inert while a Player panel is open', async () => {
    const mounted = await mountPlayer();
    await act(async () => { subtitlesButton(mounted).click() });
    pressKey(mounted.root, ' ');
    expect(playSpy).not.toHaveBeenCalled();
  });
});

describe('subtitle shortcut and media keys', () => {
  it('S opens the subtitle panel like the Subtitles button', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 's');
    expect(mounted.host.querySelector('.subtitle-panel')).not.toBeNull();
  });

  it('S fires once per press; holding it does not toggle the panel', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'S');
    pressKey(mounted.root, 'S', { repeat: true });
    expect(mounted.host.querySelectorAll('.subtitle-panel')).toHaveLength(1);
  });

  it('MediaPlayPause toggles playback', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'MediaPlayPause');
    expect(playSpy).toHaveBeenCalledTimes(1);
    pressKey(mounted.root, 'MediaPlayPause');
    expect(pauseSpy).toHaveBeenCalledTimes(1);
  });

  it('MediaPlay plays and MediaPause pauses', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'MediaPause'); // already paused: no-op
    expect(pauseSpy).not.toHaveBeenCalled();
    pressKey(mounted.root, 'MediaPlay');
    expect(playSpy).toHaveBeenCalledTimes(1);
    pressKey(mounted.root, 'MediaPause');
    expect(pauseSpy).toHaveBeenCalledTimes(1);
  });

  it('MediaStop pauses playback', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'MediaPlay');
    pressKey(mounted.root, 'MediaStop');
    expect(pauseSpy).toHaveBeenCalledTimes(1);
  });

  it('unknown media keys do nothing harmful', async () => {
    const mounted = await mountPlayer();
    expect(() => pressKey(mounted.root, 'MediaEject')).not.toThrow();
    expect(playSpy).not.toHaveBeenCalled();
    expect(pauseSpy).not.toHaveBeenCalled();
  });
});

describe('digit percent jumps', () => {
  it('0-9 seek to that tenth of the duration', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, '5');
    const scrubber = mounted.host.querySelector<HTMLInputElement>('.player-scrubber input')!;
    expect(scrubber.value).toBe('300000'); // 50% of 600s
    await act(async () => { vi.advanceTimersByTime(0) });
    expect(mounted.video.currentTime).toBe(300);
    pressKey(mounted.root, '9');
    expect(mounted.video.currentTime).toBe(540);
    pressKey(mounted.root, '0');
    expect(mounted.video.currentTime).toBe(0);
  });

  it('digits fire once per press; key auto-repeat does not machine-gun seeks', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, '5', { repeat: true });
    expect(mounted.video.currentTime).toBe(0); // repeat ignored entirely
  });

  it('digits before the duration is known do nothing harmful', async () => {
    vi.spyOn(API.prototype, 'mediaInfo').mockImplementation(() => Promise.withResolvers<MediaInfo>().promise);
    const mounted = await mountPlayer();
    expect(() => pressKey(mounted.root, '5')).not.toThrow();
    expect(mounted.video.currentTime).toBe(0);
  });
});

describe('mouse conventions', () => {
  function stubEnterFullscreen(mounted: Mounted) {
    mounted.root.requestFullscreen = vi.fn().mockImplementation(function(this: HTMLElement) {
      Object.defineProperty(document, 'fullscreenElement', { value: this, configurable: true });
      document.dispatchEvent(new Event('fullscreenchange'));
      return Promise.resolve();
    });
  }

  it('a single click on the video toggles playback after the deferral window', async () => {
    const mounted = await mountPlayer();
    await act(async () => { mounted.video.click() });
    expect(playSpy).not.toHaveBeenCalled(); // waiting out the double-click window
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(playSpy).toHaveBeenCalledTimes(1);
  });

  it('a double click goes fullscreen without toggling playback', async () => {
    const mounted = await mountPlayer();
    stubEnterFullscreen(mounted);
    await act(async () => { mounted.video.click() });
    await act(async () => { mounted.video.click() });
    await act(async () => { mounted.video.dispatchEvent(new Event('dblclick')) });
    expect(mounted.root.requestFullscreen).toHaveBeenCalledTimes(1);
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(playSpy).not.toHaveBeenCalled();
  });

  it('clicks on the chrome never reach the video surface handlers', async () => {
    const mounted = await mountPlayer();
    const chrome = mounted.host.querySelector<HTMLElement>('.player-chrome')!;
    await act(async () => { chrome.dispatchEvent(new MouseEvent('click', { bubbles: false })) });
    await act(async () => { vi.advanceTimersByTime(250) });
    expect(playSpy).not.toHaveBeenCalled();
  });
});

describe('OSD feedback', () => {
  it('seek shows a ghost marker and signed hint', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowRight');
    expect(mounted.host.querySelector('.osd-ghost')).not.toBeNull();
    expect(mounted.host.querySelector('.osd')!.textContent).toContain('+5s');
  });

  it('volume shows the percent readout', async () => {
    localStorage.setItem('filelist.player.volume', '0.5');
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowUp');
    expect(mounted.host.querySelector('.osd')!.textContent).toBe('52%');
  });

  it('mute shows the mute icon', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'm');
    expect(mounted.host.querySelector('.osd-mute-label svg')).not.toBeNull();
    expect(mounted.host.querySelector('.osd-mute-label')!.getAttribute('aria-label')).toBe('Muted');
  });

  it('digits before the duration is known show a hint', async () => {
    vi.spyOn(API.prototype, 'mediaInfo').mockImplementation(() => Promise.withResolvers<MediaInfo>().promise);
    const mounted = await mountPlayer();
    pressKey(mounted.root, '5');
    expect(mounted.host.querySelector('.osd')!.textContent).toContain('Seek unavailable');
  });

  it('OSD auto-hides after 2s and held-key repeats refresh it', async () => {
    const mounted = await mountPlayer();
    pressKey(mounted.root, 'ArrowRight');
    await act(async () => { vi.advanceTimersByTime(1500) });
    pressKey(mounted.root, 'ArrowRight', { repeat: true });
    await act(async () => { vi.advanceTimersByTime(1500) });
    expect(mounted.host.querySelector('.osd')).not.toBeNull(); // refreshed, not hidden
    await act(async () => { vi.advanceTimersByTime(500) });
    expect(mounted.host.querySelector('.osd')).toBeNull(); // 2s after the last tick
  });
});
