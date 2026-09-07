// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest';
import { exitApplication, getNetworkInfo, onAppVisibilityChange, onVirtualKeyboardChange, openExternalURL, registerMediaKeys } from './platform';

const platformWindow = window as unknown as {
  tizen?: {
    ApplicationControl?: new (operation: string, uri: string) => { operation: string; uri: string };
    application?: {
      getCurrentApplication?: () => { exit: () => void };
      launchAppControl?: (control: unknown, id: unknown, onSuccess: () => void, onFailure: () => void) => void;
    };
    tvinputdevice?: { registerKey: (key: string) => void };
  };
  FileListTVNative?: {
    exit?: () => void;
    openExternal?: (url: string) => boolean;
  };
  FileListTVPlatform?: {
    getNetworkInfo?: () => Promise<{ ip: string; subnetMask: string } | null>;
    openExternal?: (url: string) => Promise<boolean>;
    exit?: () => void;
    onKeyboardVisibility?: (listener: (visible: boolean) => void) => () => void;
    onVisibility?: (listener: (visible: boolean) => void) => () => void;
  };
  webapis?: {
    network?: {
      getIp?: () => string;
      getSubnetMask?: () => string;
    };
  };
};

afterEach(() => {
  delete platformWindow.tizen;
  delete platformWindow.FileListTVNative;
  delete platformWindow.FileListTVPlatform;
  delete platformWindow.webapis;
});

describe('exitApplication', () => {
  it('exits through FileListTVPlatform when present and stops dispatch', () => {
    let platformExits = 0;
    let tizenExits = 0;
    platformWindow.FileListTVPlatform = {
      getNetworkInfo: async () => null,
      openExternal: async () => false,
      exit: () => { platformExits++; },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };
    platformWindow.tizen = { application: { getCurrentApplication: () => ({ exit: () => { tizenExits++; } }) } };
    exitApplication();
    expect(platformExits).toBe(1);
    expect(tizenExits).toBe(0);
  });

  it('exits through the Tizen API when present', () => {
    let exited = 0;
    platformWindow.tizen = { application: { getCurrentApplication: () => ({ exit: () => { exited++; } }) } };
    exitApplication();
    expect(exited).toBe(1);
  });

  it('falls back to the native bridge on Android', () => {
    let exited = 0;
    platformWindow.FileListTVNative = { exit: () => { exited++; } };
    exitApplication();
    expect(exited).toBe(1);
  });

  it('survives all channels being absent or throwing', () => {
    platformWindow.FileListTVPlatform = {
      getNetworkInfo: async () => null,
      openExternal: async () => false,
      exit: () => { throw new Error('platform exit failed'); },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };
    platformWindow.tizen = { application: { getCurrentApplication: () => { throw new Error('no tizen'); } } };
    expect(() => exitApplication()).not.toThrow();
  });
});

describe('registerMediaKeys', () => {
  it('registers every media key through the Tizen input device', () => {
    const keys: string[] = [];
    platformWindow.tizen = { tvinputdevice: { registerKey: (key: string) => keys.push(key) } };
    registerMediaKeys();
    expect(keys).toEqual(['MediaPlayPause', 'MediaPlay', 'MediaPause', 'MediaStop', 'MediaRewind', 'MediaFastForward', 'MediaTrackPrevious', 'MediaTrackNext']);
  });
  it('is a silent no-op without the Tizen API', () => {
    expect(() => registerMediaKeys()).not.toThrow();
  });
});

describe('getNetworkInfo', () => {
  it('delegates to FileListTVPlatform when present', async () => {
    platformWindow.FileListTVPlatform = {
      getNetworkInfo: async () => ({ ip: '192.168.1.50', subnetMask: '255.255.255.0' }),
      openExternal: async () => false,
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };
    const info = await getNetworkInfo();
    expect(info).toEqual({ ip: '192.168.1.50', subnetMask: '255.255.255.0' });
  });

  it('falls back to webapis.network getIp and getSubnetMask', async () => {
    platformWindow.webapis = {
      network: {
        getIp: () => '10.0.0.5',
        getSubnetMask: () => '255.0.0.0',
      },
    };
    const info = await getNetworkInfo();
    expect(info).toEqual({ ip: '10.0.0.5', subnetMask: '255.0.0.0' });
  });

  it('returns null when network is unavailable or fields are empty', async () => {
    platformWindow.webapis = {
      network: {
        getIp: () => '',
        getSubnetMask: () => '',
      },
    };
    expect(await getNetworkInfo()).toBeNull();
    delete platformWindow.webapis;
    expect(await getNetworkInfo()).toBeNull();
  });
});

describe('openExternalURL', () => {
  it('delegates to FileListTVPlatform when present', async () => {
    let openedUrl = '';
    platformWindow.FileListTVPlatform = {
      getNetworkInfo: async () => null,
      openExternal: async (url: string) => { openedUrl = url; return true; },
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };
    expect(await openExternalURL('https://example.invalid/path')).toBe(true);
    expect(openedUrl).toBe('https://example.invalid/path');
  });

  it('rejects non-http(s) destinations immediately without delegating', async () => {
    let called = false;
    platformWindow.FileListTVPlatform = {
      getNetworkInfo: async () => null,
      openExternal: async () => { called = true; return true; },
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };
    expect(await openExternalURL('ftp://example.invalid/p')).toBe(false);
    expect(await openExternalURL('javascript:alert(1)')).toBe(false);
    expect(called).toBe(false);
  });

  it('resolves from Tizen launchAppControl callbacks', async () => {
    let launched: unknown;
    platformWindow.tizen = {
      ApplicationControl: function(this: { operation: string; uri: string }, operation: string, uri: string) { this.operation = operation; this.uri = uri; } as unknown as new (operation: string, uri: string) => { operation: string; uri: string },
      application: {
        launchAppControl: (control: unknown, _id: unknown, onSuccess: () => void) => {
          launched = control;
          onSuccess();
        },
      },
    };
    expect(await openExternalURL('https://example.invalid/p')).toBe(true);
    expect(launched).toEqual({ operation: 'http://tizen.org/appcontrol/operation/view', uri: 'https://example.invalid/p' });
  });

  it('resolves false on Tizen launchAppControl failure callback', async () => {
    platformWindow.tizen = {
      ApplicationControl: function(this: { operation: string; uri: string }, operation: string, uri: string) { this.operation = operation; this.uri = uri; } as unknown as new (operation: string, uri: string) => { operation: string; uri: string },
      application: {
        launchAppControl: (_control: unknown, _id: unknown, _onSuccess: () => void, onFailure: () => void) => {
          onFailure();
        },
      },
    };
    expect(await openExternalURL('https://example.invalid/p')).toBe(false);
  });

  it('falls back to the Android shell intent', async () => {
    platformWindow.FileListTVNative = { openExternal: (url: string) => url.startsWith('https://') };
    expect(await openExternalURL('https://example.invalid/p')).toBe(true);
    expect(await openExternalURL('ftp://example.invalid/p')).toBe(false);
  });

  it('reports false when neither channel exists or both throw', async () => {
    expect(await openExternalURL('https://example.invalid/p')).toBe(false);
    platformWindow.tizen = { application: { launchAppControl: () => { throw new Error('no tizen'); } } };
    expect(await openExternalURL('https://example.invalid/p')).toBe(false);
  });
});

describe('subscription hooks', () => {
  it('forwards onVirtualKeyboardChange to FileListTVPlatform and provides unsubscribe', () => {
    let unsubscribed = false;
    let registeredListener: ((visible: boolean) => void) | null = null;
    platformWindow.FileListTVPlatform = {
      getNetworkInfo: async () => null,
      openExternal: async () => false,
      exit: () => { },
      onKeyboardVisibility: listener => {
        registeredListener = listener;
        return () => { unsubscribed = true; };
      },
      onVisibility: () => () => { },
    };
    const unsub = onVirtualKeyboardChange(visible => { expect(visible).toBe(true); });
    expect(registeredListener).not.toBeNull();
    registeredListener!(true);
    unsub();
    expect(unsubscribed).toBe(true);
  });

  it('returns clean no-op unsubscribe when hooks are absent', () => {
    const unsubKeyboard = onVirtualKeyboardChange(() => { });
    const unsubVisibility = onAppVisibilityChange(() => { });
    expect(() => unsubKeyboard()).not.toThrow();
    expect(() => unsubVisibility()).not.toThrow();
  });
});
