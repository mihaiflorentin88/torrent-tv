// @vitest-environment happy-dom
/**
 * Behavior tests for the webOS AVPlay HTML media adapter.
 *
 * Only observable adapter behavior is asserted: listener callbacks driven by
 * real media element events, stale-event suppression, duration gating, and
 * DOM geometry. No argument-forwarding or constructor-mock tests.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createAVPlay } from './avplay';
import type { AVPlayListener, WebOSAVPlay } from './avplay';

interface RecordedListener extends AVPlayListener {
 calls: Record<string, unknown[][]>;
}

function createRecordedListener(): RecordedListener {
 const calls: Record<string, unknown[][]> = {};
 const record = (name: string) => (...args: unknown[]) => {
  if (!calls[name]) calls[name] = [];
  calls[name].push(args);
 };
 return {
  calls,
  onbufferingstart: record('onbufferingstart'),
  onbufferingprogress: record('onbufferingprogress'),
  onbufferingcomplete: record('onbufferingcomplete'),
  onstreamcompleted: record('onstreamcompleted'),
  oncurrentplaytime: record('oncurrentplaytime'),
  onsubtitlechange: record('onsubtitlechange'),
  onerror: record('onerror'),
 };
}

function fire(element: HTMLElement, type: string): void {
 element.dispatchEvent(new Event(type));
}

function forcePlay(element: HTMLVideoElement, result: Promise<void>): void {
 Object.defineProperty(element, 'play', { configurable: true, value: vi.fn(() => result) });
}

function bufferedRanges(element: HTMLMediaElement, end: number, duration: number): void {
 Object.defineProperty(element, 'duration', { configurable: true, value: duration });
 Object.defineProperty(element, 'buffered', {
  configurable: true,
  value: {
   length: 1,
   start: (_index: number) => 0,
   end: (_index: number) => end,
  },
 });
}

let shell: HTMLElement;
let samsungObject: HTMLElement;

beforeEach(() => {
 document.body.innerHTML = '';
 samsungObject = document.createElement('object');
 samsungObject.id = 'av-player';
 shell = document.createElement('div');
 shell.className = 'player-shell';
 shell.appendChild(samsungObject);
 document.body.appendChild(shell);
});

afterEach(() => {
 document.body.innerHTML = '';
});

function openPrepared(
 av: WebOSAVPlay,
 recorded: RecordedListener,
 url = 'http://host/video.mp4',
): HTMLVideoElement {
 av.open(url);
 av.setDisplayRect(0, 0, 1920, 1080);
 av.setListener(recorded);
 av.prepareAsync(() => undefined, () => undefined);
 const video = document.querySelector('video') as HTMLVideoElement | null;
 if (!video) throw new Error('adapter did not create a video element at prepare');
 fire(video, 'loadedmetadata');
 return video;
}

describe('webOS avplay adapter', () => {
 it('creates the video element only at prepare, positioned to fill the shell', () => {
  const av = createAVPlay();
  av.open('http://host/video.mp4');
  expect(document.querySelector('video')).toBeNull();
  av.setDisplayRect(0, 0, 1920, 1080);
  av.setListener(createRecordedListener());
  av.prepareAsync(() => undefined, () => undefined);
  const video = document.querySelector('video') as HTMLVideoElement;
  expect(video.style.position).toBe('absolute');
  expect(video.style.top).toBe('0px');
  expect(video.style.left).toBe('0px');
  expect(video.style.width).toBe('100%');
  expect(video.style.height).toBe('100%');
  expect(video.parentElement).toBe(shell);
 });

 it('resolves prepareAsync on loadedmetadata', () => {
  const av = createAVPlay();
  av.open('http://host/video.mp4');
  av.setListener(createRecordedListener());
  const ok = vi.fn();
  const fail = vi.fn();
  av.prepareAsync(ok, fail);
  const video = document.querySelector('video') as HTMLVideoElement;
  fire(video, 'loadedmetadata');
  expect(ok).toHaveBeenCalled();
  expect(fail).not.toHaveBeenCalled();
 });

 it('rejects prepareAsync with a message when metadata fails', () => {
  const av = createAVPlay();
  av.open('http://host/video.mp4');
  av.setListener(createRecordedListener());
  const ok = vi.fn();
  let message = '';
  av.prepareAsync(ok, (error) => { message = error; });
  const video = document.querySelector('video') as HTMLVideoElement;
  Object.defineProperty(video, 'error', {
   configurable: true,
   value: { code: 4, MEDIA_ERR_SRC_NOT_SUPPORTED: 4 },
  });
  fire(video, 'error');
  expect(ok).not.toHaveBeenCalled();
  expect(typeof message).toBe('string');
  expect(message.length).toBeGreaterThan(0);
 });

 it('maps display methods to contain/contain/cover and reapplies live', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);

  av.setDisplayMethod('PLAYER_DISPLAY_MODE_AUTO_ASPECT_RATIO');
  expect(video.style.objectFit).toBe('contain');
  av.setDisplayMethod('PLAYER_DISPLAY_MODE_LETTER_BOX');
  expect(video.style.objectFit).toBe('contain');
  av.setDisplayMethod('PLAYER_DISPLAY_MODE_FULL_SCREEN');
  expect(video.style.objectFit).toBe('cover');
  av.setDisplayMethod('PLAYER_DISPLAY_MODE_LETTER_BOX');
  expect(video.style.objectFit).toBe('contain');
 });

 it('hides the Samsung object during playback and restores it on close', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  openPrepared(av, recorded);
  expect(samsungObject.style.display).toBe('none');
  av.close();
  expect(samsungObject.style.display).toBe('');
  expect(document.querySelector('video')).toBeNull();
 });

 it('reports duration only from finite metadata, in rounded ms', () => {
  const av = createAVPlay();
  av.open('http://host/video.mp4');
  av.setListener(createRecordedListener());
  av.prepareAsync(() => undefined, () => undefined);
  const video = document.querySelector('video') as HTMLVideoElement;
  expect(av.getDuration()).toBe(0);
  Object.defineProperty(video, 'duration', { configurable: true, value: Number.NaN });
  expect(av.getDuration()).toBe(0);
  Object.defineProperty(video, 'duration', { configurable: true, value: Infinity });
  expect(av.getDuration()).toBe(0);
  Object.defineProperty(video, 'duration', { configurable: true, value: 61.5 });
  expect(av.getDuration()).toBe(61500);
  Object.defineProperty(video, 'duration', { configurable: true, value: 1.23456 });
  expect(av.getDuration()).toBe(1235);
 });

 it('reports position only through real timeupdate events, in rounded ms', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  expect(recorded.calls.oncurrentplaytime).toBeUndefined();
  Object.defineProperty(video, 'currentTime', { configurable: true, value: 3.4567 });
  fire(video, 'timeupdate');
  expect(recorded.calls.oncurrentplaytime).toEqual([[3457]]);
 });

 it('does not echo a requested seek position before time moves', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  av.seekTo(30000);
  expect(recorded.calls.oncurrentplaytime).toBeUndefined();
  Object.defineProperty(video, 'currentTime', { configurable: true, value: 30.0001 });
  fire(video, 'timeupdate');
  expect(recorded.calls.oncurrentplaytime).toEqual([[30000]]);
 });

 it('emits buffering start, progress from buffered ranges, and complete once per episode', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  bufferedRanges(video, 12, 60);
  fire(video, 'waiting');
  expect(recorded.calls.onbufferingstart).toHaveLength(1);
  fire(video, 'progress');
  expect(recorded.calls.onbufferingprogress).toEqual([[20]]);
  bufferedRanges(video, 59, 60);
  fire(video, 'progress');
  expect(recorded.calls.onbufferingprogress?.[1]).toEqual([98]);
  fire(video, 'playing');
  expect(recorded.calls.onbufferingcomplete).toHaveLength(1);
  // Additional progress events after completion never re-fire complete.
  fire(video, 'progress');
  expect(recorded.calls.onbufferingcomplete).toHaveLength(1);
  // Re-entering buffering on a later stall restarts the sequence.
  fire(video, 'stall');
  expect(recorded.calls.onbufferingstart).toHaveLength(2);
  fire(video, 'playing');
  expect(recorded.calls.onbufferingcomplete).toHaveLength(2);
 });

 it('fires onstreamcompleted once per ended, re-armed by play and seek', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  fire(video, 'ended');
  expect(recorded.calls.onstreamcompleted).toHaveLength(1);
  fire(video, 'ended');
  expect(recorded.calls.onstreamcompleted).toHaveLength(1);
  av.play();
  fire(video, 'ended');
  expect(recorded.calls.onstreamcompleted).toHaveLength(2);
  fire(video, 'ended');
  expect(recorded.calls.onstreamcompleted).toHaveLength(2);
  av.seekTo(1000);
  fire(video, 'ended');
  expect(recorded.calls.onstreamcompleted).toHaveLength(3);
 });

 it('suppresses stale metadata, seek, and ended events after close', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  av.open('http://host/video.mp4');
  av.setListener(recorded);
  av.prepareAsync(() => undefined, () => undefined);
  const video = document.querySelector('video') as HTMLVideoElement;
  av.close();
  fire(video, 'loadedmetadata');
  Object.defineProperty(video, 'currentTime', { configurable: true, value: 5 });
  fire(video, 'timeupdate');
  fire(video, 'ended');
  fire(video, 'error');
  expect(recorded.calls.oncurrentplaytime).toBeUndefined();
  expect(recorded.calls.onstreamcompleted).toBeUndefined();
  expect(recorded.calls.onerror).toBeUndefined();
 });

 it('suppresses stale events from the replaced source after re-open', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const first = openPrepared(av, recorded, 'http://host/video-1.mp4');
  // Re-opening with a new source replaces the video element
  const second = openPrepared(av, recorded, 'http://host/video-2.mp4');
  expect(second).not.toBe(first);

  fire(first, 'timeupdate');
  fire(first, 'ended');
  Object.defineProperty(first, 'error', { configurable: true, value: { code: 3 } });
  fire(first, 'error');
  expect(recorded.calls.oncurrentplaytime).toBeUndefined();
  expect(recorded.calls.onstreamcompleted).toBeUndefined();
  expect(recorded.calls.onerror).toBeUndefined();

  // The live element still drives the listener.
  Object.defineProperty(second, 'currentTime', { configurable: true, value: 1 });
  fire(second, 'timeupdate');
  expect(recorded.calls.oncurrentplaytime).toEqual([[1000]]);
 });

 it('swallows play() AbortError but surfaces other play failures as onerror', async () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);

  const abort = new Error('interrupted');
  (abort as Error & { name: string }).name = 'AbortError';
  forcePlay(video, Promise.reject(abort));
  av.play();
  await Promise.resolve();
  await Promise.resolve();
  expect(recorded.calls.onerror).toBeUndefined();

  const denied = new Error('denied');
  (denied as Error & { name: string }).name = 'NotAllowedError';
  forcePlay(video, Promise.reject(denied));
  av.play();
  await Promise.resolve();
  await Promise.resolve();
  expect(recorded.calls.onerror).toHaveLength(1);
  expect(String(recorded.calls.onerror?.[0][0])).toContain('denied');
 });

 it('pause stops the element', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const pause = vi.fn();
  Object.defineProperty(video, 'pause', { configurable: true, value: pause });
  av.pause();
  expect(pause).toHaveBeenCalled();
 });

 it('stop pauses and rewinds to the start', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const pause = vi.fn();
  Object.defineProperty(video, 'pause', { configurable: true, value: pause });
  let time = 42;
  Object.defineProperty(video, 'currentTime', {
   configurable: true,
   get: () => time,
   set: (v: number) => { time = v; },
  });
  av.stop();
  expect(pause).toHaveBeenCalled();
  expect(time).toBe(0);
 });

 it('returns an honest empty track list and throws RangeError for selection without collections', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  openPrepared(av, recorded);
  expect(av.getTotalTrackInfo()).toEqual([]);
  expect(() => av.setSelectTrack('AUDIO', 0)).toThrow(RangeError);
 });

 it('keeps native text rendering off and accepts subtitle timing', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  openPrepared(av, recorded);
  av.setSilentSubtitle(true);
  const video = document.querySelector('video') as HTMLVideoElement;
  for (const track of Array.from(video.textTracks || [])) {
   expect(track.mode).toBe('disabled');
  }
  expect(() => av.setSilentSubtitle(false)).toThrow(RangeError);
  expect(() => av.setSubtitlePosition(1500)).not.toThrow();
 });
});
