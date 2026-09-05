// The two exit/registration surfaces that differ per TV platform. Everything
// is feature-detected and guarded: Tizen APIs when present, the Android
// native bridge otherwise, silence when neither exists (spec Parity contract
// rule 2 — feature detection, never platform branching in UI code).
const MEDIA_KEYS = ['MediaPlayPause', 'MediaPlay', 'MediaPause', 'MediaStop', 'MediaRewind', 'MediaFastForward', 'MediaTrackPrevious', 'MediaTrackNext'];

export function exitApplication(): void {
  try {
    const tizenExit = window.tizen?.application?.getCurrentApplication().exit;
    if (typeof tizenExit === 'function') { tizenExit(); return; }
  } catch { }
  try { window.FileListTVNative?.exit(); } catch { }
}

export function registerMediaKeys(): void {
  for (const key of MEDIA_KEYS) {
    try { window.tizen?.tvinputdevice?.registerKey(key); } catch { }
  }
}
