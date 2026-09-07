// The platform exit, registration, network and link surfaces. Everything is
// feature-detected and guarded: webOS TVPlatformHooks when present, Tizen APIs,
// the Android native bridge, silence when neither exists (spec Parity contract
// rule 2 — feature detection, never platform branching in UI code).
const MEDIA_KEYS = ['MediaPlayPause', 'MediaPlay', 'MediaPause', 'MediaStop', 'MediaRewind', 'MediaFastForward', 'MediaTrackPrevious', 'MediaTrackNext'];

export interface NetworkInfo { ip: string; subnetMask: string; }
export interface TVPlatformHooks {
  getNetworkInfo(): Promise<NetworkInfo | null>;
  openExternal(url: string): Promise<boolean>;
  exit(): void;
  onKeyboardVisibility(listener: (visible: boolean) => void): () => void;
  onVisibility(listener: (visible: boolean) => void): () => void;
}

export function exitApplication(): void {
  try {
    const platformExit = window.FileListTVPlatform?.exit;
    if (typeof platformExit === 'function') { platformExit(); return; }
  } catch { }
  try {
    const tizenExit = window.tizen?.application?.getCurrentApplication().exit;
    if (typeof tizenExit === 'function') { tizenExit(); return; }
  } catch { }
  try { window.FileListTVNative?.exit(); } catch { }
}

export async function getNetworkInfo(): Promise<NetworkInfo | null> {
  try {
    if (typeof window.FileListTVPlatform?.getNetworkInfo === 'function') {
      return await window.FileListTVPlatform.getNetworkInfo();
    }
  } catch {
    return null;
  }
  try {
    const network = window.webapis?.network;
    if (typeof network?.getIp === 'function' && typeof network?.getSubnetMask === 'function') {
      const ip = String(network.getIp() || '');
      const subnetMask = String(network.getSubnetMask() || '');
      if (ip && subnetMask) return { ip, subnetMask };
    }
  } catch { }
  return null;
}

// External links: webOS launches the com.webos.app.browser app via webOSDev;
// Tizen hands the URL to the TV browser through the VIEW app-control; Android
// forwards to the shell's ACTION_VIEW intent. The return value reports whether
// a launcher took the link — callers keep the address visible on the card or
// tile, so a false (no browser on the box, refused launch) still leaves the user
// the URL. Feature detection only: parity forbids platform branching in UI code.
export async function openExternalURL(url: string): Promise<boolean> {
  if (!/^https?:\/\//i.test(url)) return false;
  try {
    if (typeof window.FileListTVPlatform?.openExternal === 'function') {
      return await window.FileListTVPlatform.openExternal(url);
    }
  } catch {
    return false;
  }
  try {
    if (typeof window.tizen?.application?.launchAppControl === 'function') {
      return await new Promise<boolean>(resolve => {
        try {
          window.tizen.application.launchAppControl(
            new window.tizen.ApplicationControl('http://tizen.org/appcontrol/operation/view', url),
            null,
            () => resolve(true),
            () => resolve(false)
          );
        } catch {
          resolve(false);
        }
      });
    }
  } catch { }
  try { return window.FileListTVNative?.openExternal?.(url) === true; } catch { }
  return false;
}

export function onVirtualKeyboardChange(listener: (visible: boolean) => void): () => void {
  try {
    const subscribe = window.FileListTVPlatform?.onKeyboardVisibility;
    if (typeof subscribe === 'function') {
      return subscribe(listener) || (() => { });
    }
  } catch { }
  return () => { };
}

export function onAppVisibilityChange(listener: (visible: boolean) => void): () => void {
  try {
    const subscribe = window.FileListTVPlatform?.onVisibility;
    if (typeof subscribe === 'function') {
      return subscribe(listener) || (() => { });
    }
  } catch { }
  return () => { };
}

export function registerMediaKeys(): void {
  for (const key of MEDIA_KEYS) {
    try { window.tizen?.tvinputdevice?.registerKey(key); } catch { }
  }
}
