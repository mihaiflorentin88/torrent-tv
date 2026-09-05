import { describe, expect, it } from 'vitest';
import { clampVolume, DEFAULT_PLAYER_SETTINGS, loadPlayerSettings, PLAYER_MUTED_KEY, PLAYER_VOLUME_KEY, savePlayerSettings } from '@torrent-tv/shared';
import type { PlayerSettingsStorage } from '@torrent-tv/shared';

// — Persisted player settings: pure load/save over an injected storage so the
// browser passes localStorage and tests pass a plain object. Absent or corrupt
// entries fall back to the defaults (100%, unmuted) instead of breaking
// playback; values are clamped into the slider's 0..1 range on both paths.

const storage = (entries: Record<string, string> = {}) => ({
  entries,
  getItem: (key: string) => (key in entries ? entries[key] : null),
  setItem: (key: string, value: string) => { entries[key] = value },
});

const throwingStorage = (): PlayerSettingsStorage => ({ getItem: () => null, setItem: () => { throw new Error('QuotaExceededError') } });

describe('Player settings persistence', () => {
  it('defaults to full volume and unmuted when nothing is stored yet (first use)', () => {
    expect(DEFAULT_PLAYER_SETTINGS).toEqual({ volume: 1, muted: false });
    expect(loadPlayerSettings(storage())).toEqual(DEFAULT_PLAYER_SETTINGS);
  });

  it('round-trips volume and mute through the namespaced keys', () => {
    const store = storage();
    savePlayerSettings(store, { volume: 0.4, muted: true });
    expect(store.entries[PLAYER_VOLUME_KEY]).toBe('0.4');
    expect(store.entries[PLAYER_MUTED_KEY]).toBe('true');
    expect(loadPlayerSettings(store)).toEqual({ volume: 0.4, muted: true });
  });

  it('clamps stored volume into the 0..1 slider range on load', () => {
    expect(loadPlayerSettings(storage({ [PLAYER_VOLUME_KEY]: '1.7' })).volume).toBe(1);
    expect(loadPlayerSettings(storage({ [PLAYER_VOLUME_KEY]: '-2' })).volume).toBe(0);
  });

  it('normalizes values on save', () => {
    const high = storage();
    savePlayerSettings(high, { volume: 3, muted: false });
    expect(high.entries[PLAYER_VOLUME_KEY]).toBe('1');
    const low = storage();
    savePlayerSettings(low, { volume: -1, muted: false });
    expect(low.entries[PLAYER_VOLUME_KEY]).toBe('0');
    const odd = storage();
    savePlayerSettings(odd, { volume: 0.5, muted: 1 as unknown as boolean });
    expect(odd.entries[PLAYER_MUTED_KEY]).toBe('true');
  });

  it('falls back to defaults on corrupt entries instead of breaking playback', () => {
    expect(loadPlayerSettings(storage({ [PLAYER_VOLUME_KEY]: 'not-a-number' }))).toEqual({ volume: 1, muted: false });
    expect(loadPlayerSettings(storage({ [PLAYER_VOLUME_KEY]: '' }))).toEqual({ volume: 1, muted: false });
    expect(loadPlayerSettings(storage({ [PLAYER_MUTED_KEY]: 'yes' }))).toEqual({ volume: 1, muted: false });
  });

  it('keeps a stored value on one key while the other is absent or corrupt', () => {
    expect(loadPlayerSettings(storage({ [PLAYER_MUTED_KEY]: 'true' }))).toEqual({ volume: 1, muted: true });
    expect(loadPlayerSettings(storage({ [PLAYER_VOLUME_KEY]: '0.3' }))).toEqual({ volume: 0.3, muted: false });
  });

  it('treats a blocked store (quota, private mode) as best-effort', () => {
    expect(() => savePlayerSettings(throwingStorage(), { volume: 0.5, muted: false })).not.toThrow();
  });

  it('clamps arbitrary inputs to the slider range', () => {
    expect(clampVolume(0.5)).toBe(0.5);
    expect(clampVolume(0)).toBe(0);
    expect(clampVolume(1)).toBe(1);
    expect(clampVolume('0.3')).toBe(0.3);
    expect(clampVolume(Number.NaN)).toBe(1);
    expect(clampVolume(Number.POSITIVE_INFINITY)).toBe(1);
    expect(clampVolume('')).toBe(1);
    expect(clampVolume(null)).toBe(1);
  });
});
