/**
 * webOS AVPlay HTML Media Adapter.
 *
 * Implements the window.webapis.avplay seam on top of HTMLMediaElement for
 * LG webOS TV devices (Chromium 53+). All operations are event-driven and
 * honest: no fake state, no constructor mocks, no invented methods.
 *
 * Runtime constraints:
 * - Chromium 53 target: promise-chained ES2016 code, no async/await.
 * - Element created lazily at prepareAsync; decoder is never reserved at module import.
 * - Generation counter drops late/stale callbacks across close and source replacement.
 * - Times in ms at boundary, converted and rounded to/from seconds.
 */

export interface AVPlayListener {
 onbufferingstart?(): void;
 onbufferingprogress?(percent: number): void;
 onbufferingcomplete?(): void;
 onstreamcompleted?(): void;
 oncurrentplaytime?(milliseconds: number): void;
 onsubtitlechange?(duration: number, text: string): void;
 onerror?(error: string): void;
 ontrackschanged?(): void;
}

export interface RawTrack {
 index: number;
 type: 'AUDIO' | 'TEXT' | 'VIDEO';
 extra_info: { track_lang?: string; track_title?: string; codec?: string };
}

export interface WebOSAVPlay {
 open(url: string): void;
 setDisplayRect(x: number, y: number, width: number, height: number): void;
 setDisplayMethod(mode: string): void;
 setListener(listener: AVPlayListener): void;
 prepareAsync(success: () => void, error: (message: string) => void): void;
 play(): void;
 pause(): void;
 seekTo(milliseconds: number): void;
 stop(): void;
 close(): void;
 getDuration(): number;
 getTotalTrackInfo(): RawTrack[];
 setSelectTrack(type: 'AUDIO' | 'TEXT', index: number): void;
 setSilentSubtitle(silent: boolean): void;
 setSubtitlePosition(milliseconds: number): void;
}

function formatMediaError(err: MediaError | null): string {
 if (!err) return 'Unknown media error';
 switch (err.code) {
  case 1:
   return 'Media playback aborted by client';
  case 2:
   return 'Network error while loading media';
  case 3:
   return 'Media decoding failed';
  case 4:
   return 'Media format or codec not supported';
  default:
   return 'Media error ' + err.code;
 }
}
interface AudioTrackLike {
 enabled?: boolean;
 language?: string;
 languageCode?: string;
 label?: string;
 title?: string;
 id?: string;
}

interface CueLike {
 startTime: number;
 endTime: number;
 text?: string;
}

interface TextTrackLike {
 mode?: string;
 language?: string;
 languageCode?: string;
 label?: string;
 title?: string;
 cues?: ArrayLike<CueLike> | null;
 oncuechange?: (() => void) | null;
}

interface TrackListLike<T> {
 length: number;
 [index: number]: T | undefined;
 addEventListener?(type: string, fn: () => void): void;
 removeEventListener?(type: string, fn: () => void): void;
}

interface AVMediaElement extends HTMLVideoElement {
 audioTracks?: TrackListLike<AudioTrackLike>;
 textTracks: TrackListLike<TextTrackLike> & TextTrackList;
}

export function createAVPlay(): WebOSAVPlay {
 let sourceUrl = '';
 let displayMethod = 'PLAYER_DISPLAY_MODE_AUTO_ASPECT_RATIO';
 let listener: AVPlayListener | null = null;
 let video: AVMediaElement | null = null;
 let hiddenSamsungObject: HTMLElement | null = null;
 let selectedTextTrack: TextTrackLike | null = null;
 let subtitleOffsetMs = 0;
 let lastForwardedKey: string | null = null;
 let lastTrackSignature = '';
 let wiredAudioList: TrackListLike<AudioTrackLike> | null = null;
 let wiredTextList: TrackListLike<TextTrackLike> | null = null;
 let generation = 0;
 let endedFired = false;
 let bufferingStarted = false;
 let bufferingCompleted = false;
 let prepared = false;
 function applyDisplayMethod(): void {
  if (video) {
   video.style.objectFit = displayMethod === 'PLAYER_DISPLAY_MODE_FULL_SCREEN' ? 'cover' : 'contain';
  }
 }

 function getAudioList(): TrackListLike<AudioTrackLike> | null {
  if (!video) return null;
  const list = video.audioTracks;
  return list && typeof list.length === 'number' ? list : null;
 }

 function getTextList(): TrackListLike<TextTrackLike> | null {
  if (!video) return null;
  const list = video.textTracks;
  return list && typeof list.length === 'number' ? list : null;
 }

 function computeTrackSignature(): string {
  const audio = getAudioList();
  const text = getTextList();
  let sig = 'a:' + (audio ? audio.length : 'none');
  if (audio) {
   for (let i = 0; i < audio.length; i++) {
    const t = audio[i];
    sig += '_' + (t && t.enabled ? '1' : '0') + '_' + (t && t.language ? t.language : '');
   }
  }
  sig += '|t:' + (text ? text.length : 'none');
  if (text) {
   for (let i = 0; i < text.length; i++) {
    const t = text[i];
    sig += '_' + (t && t.mode ? t.mode : '') + '_' + (t && t.language ? t.language : '');
   }
  }
  return sig;
 }

 function onTracksChange(): void {
  const nextSig = computeTrackSignature();
  if (nextSig !== lastTrackSignature) {
   lastTrackSignature = nextSig;
   if (listener && typeof listener.ontrackschanged === 'function') {
    listener.ontrackschanged();
   }
  }
 }

 function wireTrackListeners(): void {
  const audio = getAudioList();
  if (audio && audio !== wiredAudioList) {
   wiredAudioList = audio;
   if (typeof audio.addEventListener === 'function') {
    audio.addEventListener('change', onTracksChange);
    audio.addEventListener('addtrack', onTracksChange);
    audio.addEventListener('removetrack', onTracksChange);
   }
  }
  const text = getTextList();
  if (text && text !== wiredTextList) {
   wiredTextList = text;
   if (typeof text.addEventListener === 'function') {
    text.addEventListener('change', onTracksChange);
    text.addEventListener('addtrack', onTracksChange);
    text.addEventListener('removetrack', onTracksChange);
   }
  }
 }

 function evaluateNativeCues(): void {
  if (!selectedTextTrack || !video) return;
  const cues = selectedTextTrack.cues;
  if (cues === null || typeof cues === 'undefined') {
   return;
  }
  const currentMs = Math.round(video.currentTime * 1000);
  const targetMs = currentMs - subtitleOffsetMs;
  let activeCue: CueLike | null = null;
  for (let i = 0; i < cues.length; i++) {
   const cue = cues[i];
   if (!cue) continue;
   const startMs = Math.round(cue.startTime * 1000);
   const endMs = Math.round(cue.endTime * 1000);
   if (startMs <= targetMs && targetMs <= endMs) {
    activeCue = cue;
    break;
   }
  }
  const duration = activeCue ? Math.round((activeCue.endTime - activeCue.startTime) * 1000) : 0;
  const text = activeCue && typeof activeCue.text === 'string' ? activeCue.text : '';
  const key = duration + ':' + text;
  if (key !== lastForwardedKey) {
   lastForwardedKey = key;
   if (listener && typeof listener.onsubtitlechange === 'function') {
    listener.onsubtitlechange(duration, text);
   }
  }
 }

 function teardownVideo(): void {
  if (wiredAudioList && typeof wiredAudioList.removeEventListener === 'function') {
   wiredAudioList.removeEventListener('change', onTracksChange);
   wiredAudioList.removeEventListener('addtrack', onTracksChange);
   wiredAudioList.removeEventListener('removetrack', onTracksChange);
  }
  if (wiredTextList && typeof wiredTextList.removeEventListener === 'function') {
   wiredTextList.removeEventListener('change', onTracksChange);
   wiredTextList.removeEventListener('addtrack', onTracksChange);
   wiredTextList.removeEventListener('removetrack', onTracksChange);
  }
  wiredAudioList = null;
  wiredTextList = null;
  selectedTextTrack = null;
  subtitleOffsetMs = 0;
  lastForwardedKey = null;
  lastTrackSignature = '';
  if (video) {
   video.pause();
   video.removeAttribute('src');
   video.load();
   if (video.parentElement) {
    video.parentElement.removeChild(video);
   }
   video = null;
  }
  if (hiddenSamsungObject) {
   hiddenSamsungObject.style.display = '';
   hiddenSamsungObject = null;
  }
  prepared = false;
  bufferingStarted = false;
  bufferingCompleted = false;
  endedFired = false;
 }


 return {
  open(url: string): void {
   generation++;
   teardownVideo();
   sourceUrl = String(url || '');
  },

  setDisplayRect(_x: number, _y: number, _width: number, _height: number): void {
   // Direct geometry is controlled via CSS inside .player-shell.
  },

  setDisplayMethod(mode: string): void {
   displayMethod = String(mode || '');
   applyDisplayMethod();
  },

  setListener(l: AVPlayListener): void {
   listener = l || null;
  },

  prepareAsync(success: () => void, error: (message: string) => void): void {
   const currentGen = generation;
   teardownVideo();

   const shell = document.querySelector('.player-shell') || document.body;
   const samsung = document.getElementById('av-player');
   if (samsung && samsung.style.display !== 'none') {
    samsung.style.display = 'none';
    hiddenSamsungObject = samsung;
   }

   const el = document.createElement('video');
   el.style.position = 'absolute';
   el.style.top = '0px';
   el.style.left = '0px';
   el.style.width = '100%';
   el.style.height = '100%';
   el.preload = 'auto';
   const targetVideo: AVMediaElement = el as AVMediaElement;
   let audioBacking: TrackListLike<AudioTrackLike> | undefined = targetVideo.audioTracks;
   let textBacking: TrackListLike<TextTrackLike> | undefined = targetVideo.textTracks;

   if (!audioBacking) {
    Object.defineProperty(el, 'audioTracks', {
     configurable: true,
     enumerable: true,
     get: function() { return audioBacking; },
     set: function(val: TrackListLike<AudioTrackLike> | undefined) {
      audioBacking = val;
      wireTrackListeners();
      onTracksChange();
     },
    });
   }

   if (!textBacking) {
    Object.defineProperty(el, 'textTracks', {
     configurable: true,
     enumerable: true,
     get: function() { return textBacking; },
     set: function(val: TrackListLike<TextTrackLike> | undefined) {
      textBacking = val;
      wireTrackListeners();
      onTracksChange();
     },
    });
   }

   video = targetVideo;
   applyDisplayMethod();
   shell.appendChild(el);
   wireTrackListeners();
   lastTrackSignature = computeTrackSignature();

   let prepareSettled = false;

   function onLoadedMetadata(): void {
    if (currentGen !== generation || prepareSettled) return;
    prepareSettled = true;
    el.removeEventListener('loadedmetadata', onLoadedMetadata);
    el.removeEventListener('error', onInitialError);
    prepared = true;
    if (typeof success === 'function') {
     success();
    }
   }

   function onInitialError(): void {
    if (currentGen !== generation || prepareSettled) return;
    prepareSettled = true;
    el.removeEventListener('loadedmetadata', onLoadedMetadata);
    el.removeEventListener('error', onInitialError);
    const msg = formatMediaError(el.error);
    if (typeof error === 'function') {
     error(msg);
    }
   }

   el.addEventListener('loadedmetadata', onLoadedMetadata);
   el.addEventListener('error', onInitialError);

   // Playback progress and timeupdate
   el.addEventListener('timeupdate', function() {
    if (currentGen !== generation) return;
    if (listener && typeof listener.oncurrentplaytime === 'function') {
     listener.oncurrentplaytime(Math.round(el.currentTime * 1000));
    }
    evaluateNativeCues();
   });

   // Buffering start
   function handleBufferingStart(): void {
    if (currentGen !== generation) return;
    bufferingCompleted = false;
    if (!bufferingStarted) {
     bufferingStarted = true;
     if (listener && typeof listener.onbufferingstart === 'function') {
      listener.onbufferingstart();
     }
    }
   }

   el.addEventListener('waiting', handleBufferingStart);
   el.addEventListener('stall', handleBufferingStart);

   // Buffering progress: approximate from HTMLMediaElement buffered TimeRanges
   el.addEventListener('progress', function() {
    if (currentGen !== generation || !bufferingStarted) return;
    const duration = el.duration;
    if (typeof duration === 'number' && isFinite(duration) && duration > 0 && el.buffered && el.buffered.length > 0) {
     const current = el.currentTime;
     let bufferedEnd = 0;
     for (let i = 0; i < el.buffered.length; i++) {
      const start = el.buffered.start(i);
      const end = el.buffered.end(i);
      if (start <= current && end >= current) {
       bufferedEnd = end;
       break;
      }
      if (end > bufferedEnd) {
       bufferedEnd = end;
      }
     }
     const pct = Math.min(100, Math.max(0, Math.round((bufferedEnd / duration) * 100)));
     if (listener && typeof listener.onbufferingprogress === 'function') {
      listener.onbufferingprogress(pct);
     }
    }
   });

   // Buffering complete
   function handleBufferingComplete(): void {
    if (currentGen !== generation) return;
    if (bufferingStarted && !bufferingCompleted) {
     bufferingStarted = false;
     bufferingCompleted = true;
     if (listener && typeof listener.onbufferingcomplete === 'function') {
      listener.onbufferingcomplete();
     }
    }
   }

   el.addEventListener('playing', handleBufferingComplete);
   el.addEventListener('canplay', handleBufferingComplete);

   // Stream completion (ended)
   el.addEventListener('ended', function() {
    if (currentGen !== generation) return;
    if (!endedFired) {
     endedFired = true;
     if (listener && typeof listener.onstreamcompleted === 'function') {
      listener.onstreamcompleted();
     }
    }
   });

   // Post-prepare runtime errors
   el.addEventListener('error', function() {
    if (currentGen !== generation || !prepared) return;
    const msg = formatMediaError(el.error);
    if (listener && typeof listener.onerror === 'function') {
     listener.onerror(msg);
    }
   });

   el.src = sourceUrl;
   el.load();
  },

  play(): void {
   if (!video) return;
   endedFired = false;
   try {
    const result = video.play();
    if (result && typeof result.then === 'function') {
     result.then(
      function() {
       // Playback started successfully
      },
      function(error: Error | DOMException) {
       if (error && error.name === 'AbortError') {
        // Benign: rapid play/pause or source switch interrupted the request.
        return;
       }
       if (listener && typeof listener.onerror === 'function') {
        const message = (error && (error.message || error.name)) || 'Playback rejected';
        listener.onerror(String(message));
       }
      },
     );
    }
   } catch (error) {
    if (listener && typeof listener.onerror === 'function') {
     listener.onerror(String((error as Error).message || error));
    }
   }
  },

  pause(): void {
   if (video) {
    video.pause();
   }
  },

  seekTo(milliseconds: number): void {
   if (!video) return;
   endedFired = false;
   const targetSeconds = Math.max(0, Number(milliseconds || 0) / 1000);
   video.currentTime = targetSeconds;
   evaluateNativeCues();
  },

  stop(): void {
   if (video) {
    video.pause();
    video.currentTime = 0;
   }
   endedFired = false;
  },

  close(): void {
   generation++;
   teardownVideo();
   sourceUrl = '';
  },

  getDuration(): number {
   if (!video) return 0;
   const d = video.duration;
   if (typeof d !== 'number' || !isFinite(d) || isNaN(d) || d <= 0) {
    return 0;
   }
   return Math.round(d * 1000);
  },

  getTotalTrackInfo(): RawTrack[] {
   wireTrackListeners();
   const result: RawTrack[] = [];
   const audio = getAudioList();
   if (audio) {
    for (let i = 0; i < audio.length; i++) {
     const t = audio[i];
     if (!t) continue;
     result.push({
      index: i,
      type: 'AUDIO',
      extra_info: {
       track_lang: t.language || t.languageCode || '',
       track_title: t.title || t.label || '',
       codec: '',
      },
     });
    }
   }
   const text = getTextList();
   if (text) {
    for (let i = 0; i < text.length; i++) {
     const t = text[i];
     if (!t) continue;
     result.push({
      index: i,
      type: 'TEXT',
      extra_info: {
       track_lang: t.language || t.languageCode || '',
       track_title: t.title || t.label || '',
       codec: '',
      },
     });
    }
   }
   return result;
  },

  setSelectTrack(type: 'AUDIO' | 'TEXT', index: number): void {
   if (!video) {
    throw new RangeError('Track selection not supported: no media prepared');
   }
   if (type !== 'AUDIO' && type !== 'TEXT') {
    throw new RangeError('Unknown track type: ' + String(type));
   }
   wireTrackListeners();
   if (type === 'AUDIO') {
    const list = getAudioList();
    if (!list || typeof list.length !== 'number') {
     throw new RangeError('Track selection not supported: no track collections exposed');
    }
    if (!Number.isInteger(index) || index < 0 || index >= list.length) {
     throw new RangeError('Track index out of range: ' + index);
    }
    const prevEnabled: boolean[] = [];
    for (let i = 0; i < list.length; i++) {
     prevEnabled.push(Boolean(list[i] && list[i]?.enabled));
    }
    for (let i = 0; i < list.length; i++) {
     const track = list[i];
     if (track) {
      track.enabled = i === index;
     }
    }
    let verified = Boolean(list[index] && list[index]?.enabled);
    for (let i = 0; i < list.length; i++) {
     if (i !== index && list[i] && list[i]?.enabled) {
      verified = false;
      break;
     }
    }
    if (!verified) {
     for (let i = 0; i < list.length; i++) {
      const track = list[i];
      if (track) {
       track.enabled = prevEnabled[i];
      }
     }
     throw new Error('webOS refused the audio track selection: flags could not be verified');
    }
    lastTrackSignature = computeTrackSignature();
    return;
   }
   if (type === 'TEXT') {
    const list = getTextList();
    if (!list || typeof list.length !== 'number') {
     throw new RangeError('Native subtitle tracks unavailable');
    }
    if (!Number.isInteger(index) || index < 0 || index >= list.length) {
     throw new RangeError('Track index out of range: ' + index);
    }
    const prevModes: string[] = [];
    for (let i = 0; i < list.length; i++) {
     prevModes.push(String((list[i] && list[i]?.mode) || 'disabled'));
    }
    for (let i = 0; i < list.length; i++) {
     const track = list[i];
     if (track) {
      track.mode = i === index ? 'hidden' : 'disabled';
     }
    }
    const targetTrack = list[index];
    if (!targetTrack || targetTrack.mode !== 'hidden') {
     for (let i = 0; i < list.length; i++) {
      const track = list[i];
      if (track) {
       track.mode = prevModes[i];
      }
     }
     throw new Error('webOS refused the subtitle track selection: mode could not be verified');
    }
    selectedTextTrack = targetTrack;
    lastForwardedKey = null;
    lastTrackSignature = computeTrackSignature();
   }
  },

  setSilentSubtitle(silent: boolean): void {
   const list = getTextList();
   if (silent) {
    selectedTextTrack = null;
    lastForwardedKey = null;
    if (list) {
     for (let i = 0; i < list.length; i++) {
      const track = list[i];
      if (track) {
       track.mode = 'disabled';
      }
     }
    }
    if (listener && typeof listener.onsubtitlechange === 'function') {
     listener.onsubtitlechange(0, '');
    }
    lastTrackSignature = computeTrackSignature();
    return;
   }
   if (!list || list.length === 0) {
    throw new RangeError('Native subtitle tracks unavailable');
   }
  },

  setSubtitlePosition(milliseconds: number): void {
   const next = Math.round(Number(milliseconds || 0));
   if (selectedTextTrack && next !== 0) {
    const cues = selectedTextTrack.cues;
    if (cues === null || typeof cues === 'undefined') {
     throw new RangeError('Native cue access unavailable; the requested subtitle shift is unsupported');
    }
   }
   subtitleOffsetMs = next;
   lastForwardedKey = null;
   evaluateNativeCues();
  },
 };
}
if (typeof window !== 'undefined') {
 const win = window as unknown as { webapis?: { avplay?: WebOSAVPlay } };
 win.webapis = win.webapis || {};
 win.webapis.avplay = createAVPlay();
}
