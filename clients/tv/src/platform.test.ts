// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest';
import { exitApplication, registerMediaKeys } from './platform';

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
