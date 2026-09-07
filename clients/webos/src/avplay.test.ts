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
  ontrackschanged: record('ontrackschanged'),
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

 it('does not report buffering progress during stable playback without a buffering episode', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  bufferedRanges(video, 30, 60);
  // Progressive stream downloads ahead while playing: plain progress events
  // with no waiting/stall since the last buffering start must stay silent.
  fire(video, 'progress');
  expect(recorded.calls.onbufferingprogress).toBeUndefined();
  expect(recorded.calls.onbufferingstart).toBeUndefined();
  expect(recorded.calls.onbufferingcomplete).toBeUndefined();
  // Once a real buffering episode begins, progress is delivered again.
  fire(video, 'waiting');
  fire(video, 'progress');
  expect(recorded.calls.onbufferingstart).toHaveLength(1);
  expect(recorded.calls.onbufferingprogress).toEqual([[50]]);
 });

 it('stops listening for prepare settle events after prepareAsync resolves once', () => {
  const av = createAVPlay();
  av.open('http://host/video.mp4');
  av.setListener(createRecordedListener());
  const ok = vi.fn();
  const fail = vi.fn();
  av.prepareAsync(ok, fail);
  const video = document.querySelector('video') as HTMLVideoElement;
  fire(video, 'loadedmetadata');
  expect(ok).toHaveBeenCalledTimes(1);
  // Late duplicate metadata and errors must not settle prepare again.
  Object.defineProperty(video, 'error', {
   configurable: true,
   value: { code: 4, MEDIA_ERR_SRC_NOT_SUPPORTED: 4 },
  });
  fire(video, 'error');
  fire(video, 'loadedmetadata');
  expect(ok).toHaveBeenCalledTimes(1);
  expect(fail).not.toHaveBeenCalled();
 });
});
type FakeAudioTrack = { enabled: boolean; language?: string; title?: string; stuck?: boolean };
type FakeTextTrack = { mode: string; cues?: Array<{ startTime: number; endTime: number; text: string }> | null; language?: string };
type FakeTrackList<T> = {
 length: number;
 addEventListener: (type: string, fn: () => void) => void;
 removeEventListener: (type: string, fn: () => void) => void;
 dispatch: (type: string) => void;
} & Record<number, T>;

function fakeTrackList<T>(tracks: T[]): FakeTrackList<T> {
 const listeners: Record<string, Array<() => void>> = {};
 const list: FakeTrackList<T> = {
  length: tracks.length,
  addEventListener: (type, fn) => { (listeners[type] = listeners[type] || []).push(fn); },
  removeEventListener: () => undefined,
  dispatch: (type) => { for (const fn of listeners[type] || []) fn(); },
 } as FakeTrackList<T>;
 tracks.forEach((track, index) => { list[index] = track; });
 return list;
}
type VideoWithAudioTracks = HTMLVideoElement & { audioTracks?: FakeTrackList<FakeAudioTrack> };

function attachAudioTracks(video: HTMLVideoElement, tracks: FakeAudioTrack[]): FakeTrackList<FakeAudioTrack> {
 const list = fakeTrackList(tracks);
 const target: VideoWithAudioTracks = video;
 target.audioTracks = list;
 return list;
}

function attachTextTracks(video: HTMLVideoElement, tracks: FakeTextTrack[]): FakeTrackList<FakeTextTrack> {
 const list = fakeTrackList(tracks);
 Object.defineProperty(video, 'textTracks', { configurable: true, value: list });
 return list;
}

function setTime(video: HTMLVideoElement, seconds: number): void {
 Object.defineProperty(video, 'currentTime', { configurable: true, value: seconds });
 fire(video, 'timeupdate');
}

describe('honest track selection and native subtitles', () => {
 it('inventories real audio and text tracks from the live element with per-type indices', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  attachAudioTracks(video, [
   { enabled: true, language: 'eng', title: 'English' },
   { enabled: false, language: 'ron' },
  ]);
  attachTextTracks(video, [{ mode: 'disabled', cues: [], language: 'ron' }]);
  expect(av.getTotalTrackInfo()).toEqual([
   { index: 0, type: 'AUDIO', extra_info: { track_lang: 'eng', track_title: 'English', codec: '' } },
   { index: 1, type: 'AUDIO', extra_info: { track_lang: 'ron', track_title: '', codec: '' } },
   { index: 0, type: 'TEXT', extra_info: { track_lang: 'ron', track_title: '', codec: '' } },
  ]);
 });

 it('throws RangeError for selection when the element exposes no audioTracks collection', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  openPrepared(av, recorded);
  expect(() => av.setSelectTrack('AUDIO', 0)).toThrow(RangeError);
 });

 it('throws RangeError for an unsupported track type even with collections present', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const audio = attachAudioTracks(video, [{ enabled: true }]);
  expect(() => av.setSelectTrack('VIDEO' as 'AUDIO', 0)).toThrow(RangeError);
  expect(audio[0].enabled).toBe(true);
 });

 it('throws RangeError for an out-of-range audio index and keeps the previous selection', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const audio = attachAudioTracks(video, [
   { enabled: true, language: 'eng' },
   { enabled: false, language: 'ron' },
  ]);
  expect(() => av.setSelectTrack('AUDIO', 7)).toThrow(RangeError);
  expect(audio[0].enabled).toBe(true);
  expect(audio[1].enabled).toBe(false);
 });

 it('successful audio selection enables exactly the chosen track', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const audio = attachAudioTracks(video, [
   { enabled: true, language: 'eng' },
   { enabled: false, language: 'ron' },
   { enabled: false, language: 'jpn' },
  ]);
  av.setSelectTrack('AUDIO', 2);
  expect(audio[0].enabled).toBe(false);
  expect(audio[1].enabled).toBe(false);
  expect(audio[2].enabled).toBe(true);
  expect([audio[0], audio[1], audio[2]].filter(track => track.enabled)).toHaveLength(1);
 });

 it('restores the previous audio selection and rethrows when the engine refuses the change', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const stuck = { enabled: false, language: 'jpn', stuck: true };
  const audio = attachAudioTracks(video, [
   { enabled: true, language: 'eng' },
   { enabled: false, language: 'ron' },
   stuck,
  ]);
  Object.defineProperty(stuck, 'enabled', {
   configurable: true,
   get: () => false,
   set: () => { /* the engine ignores writes: the flag never sticks */ },
  });
  expect(() => av.setSelectTrack('AUDIO', 2)).toThrow(/refused|did not apply|selection/i);
  expect(audio[0].enabled).toBe(true);
  expect(audio[1].enabled).toBe(false);
 });

 it('hides the chosen native text track and forwards cue text with real durations', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const text = attachTextTracks(video, [
   { mode: 'disabled', cues: [{ startTime: 1, endTime: 3, text: 'Salut!' }], language: 'ron' },
  ]);
  av.setSelectTrack('TEXT', 0);
  expect(text[0].mode).toBe('hidden');
  setTime(video, 2);
  expect(recorded.calls.onsubtitlechange).toEqual([[2000, 'Salut!']]);
 });

 it('treats the cue end boundary inclusively at ms precision and clears the overlay after it', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  attachTextTracks(video, [{ mode: 'disabled', cues: [{ startTime: 1.5, endTime: 3, text: 'Bine' }] }]);
  av.setSelectTrack('TEXT', 0);
  setTime(video, 3);
  setTime(video, 3.001);
  expect(recorded.calls.onsubtitlechange?.[0]).toEqual([1500, 'Bine']);
  expect(recorded.calls.onsubtitlechange?.[1]).toEqual([0, '']);
 });

 it('throws RangeError for TEXT selection without a collection or with an out-of-range index', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  expect(() => av.setSelectTrack('TEXT', 0)).toThrow(RangeError);
  const text = attachTextTracks(video, [{ mode: 'disabled', cues: [] }]);
  expect(() => av.setSelectTrack('TEXT', 3)).toThrow(RangeError);
  expect(text[0].mode).toBe('disabled');
 });

 it('setSubtitlePosition shifts native cue evaluation by the shared delay and re-evaluates on seek and delay change', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  attachTextTracks(video, [
   { mode: 'disabled', cues: [{ startTime: 1, endTime: 3, text: 'A' }, { startTime: 4, endTime: 6, text: 'B' }] },
  ]);
  av.setSelectTrack('TEXT', 0);
  setTime(video, 3.5);
  expect(recorded.calls.onsubtitlechange).toEqual([[0, '']]);
  av.setSubtitlePosition(-500);
  expect(recorded.calls.onsubtitlechange?.[1]).toEqual([2000, 'B']);
  setTime(video, 1);
  expect(recorded.calls.onsubtitlechange?.[2]).toEqual([2000, 'A']);
  av.setSubtitlePosition(0);
  expect(recorded.calls.onsubtitlechange?.[3]).toEqual([2000, 'A']);
 });

 it('reports an unsupported shift instead of faking it when native cue access is unavailable', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  attachTextTracks(video, [{ mode: 'disabled', cues: null }]);
  av.setSelectTrack('TEXT', 0);
  expect(() => av.setSubtitlePosition(500)).toThrow(/cue|shift|unsupported/i);
  expect(() => av.setSubtitlePosition(0)).not.toThrow();
 });

 it('Off disables every native text track and stops cue forwarding', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const text = attachTextTracks(video, [
   { mode: 'disabled', cues: [{ startTime: 0, endTime: 100, text: 'A' }] },
   { mode: 'disabled', cues: [] },
  ]);
  av.setSelectTrack('TEXT', 0);
  expect(text[0].mode).toBe('hidden');
  av.setSilentSubtitle(true);
  expect(text[0].mode).toBe('disabled');
  expect(text[1].mode).toBe('disabled');
  const before = recorded.calls.onsubtitlechange?.length || 0;
  setTime(video, 50);
  expect(recorded.calls.onsubtitlechange?.length).toBe(before);
 });

 it('fires ontrackschanged once per real inventory change and never claims audible output', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  const audio = attachAudioTracks(video, [{ enabled: false, language: 'eng' }]);
  audio.dispatch('change');
  expect(recorded.calls.ontrackschanged).toHaveLength(1);
  // A redundant change event with an unchanged inventory stays silent.
  audio.dispatch('change');
  expect(recorded.calls.ontrackschanged).toHaveLength(1);
  // A real late arrival (second track) notifies exactly once.
  audio[1] = { enabled: false, language: 'ron' };
  audio.length = 2;
  audio.dispatch('change');
  expect(recorded.calls.ontrackschanged).toHaveLength(2);
  expect(recorded.calls.onerror).toBeUndefined();
  expect(recorded.calls.onsubtitlechange).toBeUndefined();
 });

 it('notifies ontrackschanged when the audio collection arrives after prepare without a manual event', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  expect(recorded.calls.ontrackschanged).toBeUndefined();
  attachAudioTracks(video, [{ enabled: true, language: 'eng', title: 'English' }]);
  expect(recorded.calls.ontrackschanged).toHaveLength(1);
  expect(av.getTotalTrackInfo()).toEqual([
   { index: 0, type: 'AUDIO', extra_info: { track_lang: 'eng', track_title: 'English', codec: '' } },
  ]);
 });

 it('a refused subtitle shift does not poison subsequent timeupdate playback', () => {
  const av = createAVPlay();
  const recorded = createRecordedListener();
  const video = openPrepared(av, recorded);
  attachTextTracks(video, [{ mode: 'disabled', cues: null }]);
  av.setSelectTrack('TEXT', 0);
  expect(() => av.setSubtitlePosition(500)).toThrow(/cue|shift|unsupported/i);
  // Subsequent timeupdates must not throw uncaught errors even though
  // native cue access remains unavailable.
  expect(() => setTime(video, 10)).not.toThrow();
  expect(recorded.calls.onerror).toBeUndefined();
 });
});
