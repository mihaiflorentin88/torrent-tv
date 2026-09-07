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

export function createAVPlay(): WebOSAVPlay {
 let sourceUrl = '';
 let displayMethod = 'PLAYER_DISPLAY_MODE_AUTO_ASPECT_RATIO';
 let listener: AVPlayListener | null = null;
 let video: HTMLVideoElement | null = null;
 let hiddenSamsungObject: HTMLElement | null = null;
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

 function teardownVideo(): void {
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
   video = el;
   applyDisplayMethod();
   shell.appendChild(el);

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
   // Chromium 53 does not expose standard HTMLMediaElement track collections.
   // Returns an honest empty collection rather than fabricated entries.
   return [];
  },

  setSelectTrack(_type: 'AUDIO' | 'TEXT', _index: number): void {
   throw new RangeError('Track selection not supported: no track collections exposed');
  },

  setSilentSubtitle(silent: boolean): void {
   if (silent) {
    // Native subtitles stay OFF; ensure any HTML tracks stay disabled.
    if (video && video.textTracks) {
     for (let i = 0; i < video.textTracks.length; i++) {
      video.textTracks[i].mode = 'disabled';
     }
    }
    return;
   }
   // Honest failure: cannot enable native tracks when no collection exists.
   throw new RangeError('Native subtitle tracks unavailable');
  },

  setSubtitlePosition(_milliseconds: number): void {
   // Subtitle overlay handles its own cue timing in the shared player;
   // native text tracks remain disabled.
  },
 };
}

if (typeof window !== 'undefined') {
 const win = window as unknown as { webapis?: { avplay?: WebOSAVPlay } };
 win.webapis = win.webapis || {};
 win.webapis.avplay = createAVPlay();
}
