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

// External links: Tizen hands the URL to the TV browser through the VIEW
// app-control; Android forwards to the shell's ACTION_VIEW intent. The
// return value reports whether a launcher took the link — callers keep the
// address visible on the card or tile, so a false (no browser on the box,
// refused launch) still leaves the user the URL. Feature detection only:
// the parity contract forbids platform branching in UI code.
export function openExternalURL(url: string): boolean {
  try {
    if (typeof window.tizen?.application?.launchAppControl === 'function') {
      window.tizen.application.launchAppControl(
        new window.tizen.ApplicationControl('http://tizen.org/appcontrol/operation/view', url),
        null,
        () => { },
        () => { });
      return true;
    }
  } catch { }
  try { return window.FileListTVNative?.openExternal?.(url) === true; } catch { }
  return false;
}

export function registerMediaKeys(): void {
  for (const key of MEDIA_KEYS) {
    try { window.tizen?.tvinputdevice?.registerKey(key); } catch { }
  }
}
