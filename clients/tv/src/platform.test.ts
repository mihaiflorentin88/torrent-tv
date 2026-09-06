// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest';
import { exitApplication, openExternalURL, registerMediaKeys } from './platform';

const tizenWindow = (window as any);
afterEach(() => { delete tizenWindow.tizen; delete tizenWindow.FileListTVNative; });

describe('exitApplication', () => {
  it('exits through the Tizen API when present', () => {
    let exited = 0;
    tizenWindow.tizen = { application: { getCurrentApplication: () => ({ exit: () => { exited++; } }) } };
    exitApplication();
    expect(exited).toBe(1);
  });
  it('falls back to the native bridge on Android', () => {
    let exited = 0;
    tizenWindow.FileListTVNative = { exit: () => { exited++; } };
    exitApplication();
    expect(exited).toBe(1);
  });
  it('survives both channels being absent or throwing', () => {
    tizenWindow.tizen = { application: { getCurrentApplication: () => { throw new Error('no tizen'); } } };
    expect(() => exitApplication()).not.toThrow();
  });
});

describe('registerMediaKeys', () => {
  it('registers every media key through the Tizen input device', () => {
    const keys: string[] = [];
    tizenWindow.tizen = { tvinputdevice: { registerKey: (key: string) => keys.push(key) } };
    registerMediaKeys();
    expect(keys).toEqual(['MediaPlayPause', 'MediaPlay', 'MediaPause', 'MediaStop', 'MediaRewind', 'MediaFastForward', 'MediaTrackPrevious', 'MediaTrackNext']);
  });
  it('is a silent no-op without the Tizen API', () => {
    expect(() => registerMediaKeys()).not.toThrow();
  });
});

describe('openExternalURL', () => {
  it('hands the URL to the Tizen browser through the VIEW app-control', () => {
    let launched: unknown;
    tizenWindow.tizen = {
      ApplicationControl: function(this: Record<string, string>, operation: string, uri: string) { this.operation = operation; this.uri = uri; },
      application: { launchAppControl: (control: unknown) => { launched = control; } },
    };
    expect(openExternalURL('https://example.invalid/p')).toBe(true);
    expect(launched).toEqual({ operation: 'http://tizen.org/appcontrol/operation/view', uri: 'https://example.invalid/p' });
  });

  it('falls back to the Android shell intent and reports its verdict', () => {
    tizenWindow.FileListTVNative = { openExternal: (url: string) => url.startsWith('https://') };
    expect(openExternalURL('https://example.invalid/p')).toBe(true);
    expect(openExternalURL('ftp://example.invalid/p')).toBe(false);
  });

  it('reports false when neither channel exists or both throw', () => {
    expect(openExternalURL('https://example.invalid/p')).toBe(false);
    tizenWindow.tizen = { application: { launchAppControl: () => { throw new Error('no tizen'); } } };
    expect(openExternalURL('https://example.invalid/p')).toBe(false);
  });
});
