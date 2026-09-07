import type { NetworkInfo, TVPlatformHooks } from '../../tv/src/platform';
import './webos.d.ts';

export function getNetworkInfo(): Promise<NetworkInfo | null> {
  return new Promise(resolve => {
    let finished = false;
    const finish = (value: NetworkInfo | null) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      resolve(value);
    };
    const timer = window.setTimeout(() => finish(null), 3000);
    try {
      window.webOSDev?.connection.getStatus({
        subscribe: false,
        onSuccess: response => {
          const active = response.wired?.state === 'connected' ? response.wired
            : response.wifi?.state === 'connected' ? response.wifi : null;
          finish(active?.ipAddress && active.netmask
            ? { ip: active.ipAddress, subnetMask: active.netmask } : null);
        },
        onFailure: () => finish(null),
      });
    } catch {
      finish(null);
    }
  });
}

export function openExternal(url: string): Promise<boolean> {
  if (!/^https?:\/\//i.test(url)) return Promise.resolve(false);
  return new Promise(resolve => {
    let finished = false;
    const finish = (value: boolean) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      resolve(value);
    };
    const timer = window.setTimeout(() => finish(false), 3000);
    try {
      if (!window.webOSDev?.launch) {
        finish(false);
        return;
      }
      window.webOSDev.launch({
        id: 'com.webos.app.browser',
        params: { target: url },
        onSuccess: () => finish(true),
        onFailure: () => finish(false),
      });
    } catch {
      finish(false);
    }
  });
}

export function exit(): void {
  try {
    window.close();
  } catch { }
}

export function onKeyboardVisibility(listener: (visible: boolean) => void): () => void {
  const handler = (event: Event) => {
    const detail = (event as CustomEvent<{ visibility?: boolean }>).detail;
    if (detail && typeof detail.visibility === 'boolean') {
      listener(detail.visibility);
    }
  };
  document.addEventListener('keyboardStateChange', handler);
  return () => {
    document.removeEventListener('keyboardStateChange', handler);
  };
}

export function onVisibility(listener: (visible: boolean) => void): () => void {
  let lastVisible: boolean | null = null;
  const emit = (visible: boolean) => {
    if (visible !== lastVisible) {
      lastVisible = visible;
      listener(visible);
    }
  };
  const handler = () => emit(!document.hidden);
  const relaunchHandler = () => {
    if (!document.hidden) emit(true);
  };
  document.addEventListener('visibilitychange', handler);
  document.addEventListener('webOSRelaunch', relaunchHandler);
  return () => {
    document.removeEventListener('visibilitychange', handler);
    document.removeEventListener('webOSRelaunch', relaunchHandler);
  };
}

export const webOSPlatform: TVPlatformHooks = {
  getNetworkInfo,
  openExternal,
  exit,
  onKeyboardVisibility,
  onVisibility,
};

window.FileListTVPlatform = webOSPlatform;
