// @vitest-environment happy-dom
import { afterEach, describe, expect, it } from 'vitest';
import { appIdentity } from './app-name';

afterEach(() => { delete (window as any).FileListTVIdentity; });

describe('appIdentity', () => {
  it('defaults to FileList TV with the FL monogram', () => {
    expect(appIdentity()).toEqual({ name: 'FileList TV', monogram: 'FL' });
  });
  it('prefers the platform-injected identity', () => {
    (window as any).FileListTVIdentity = { name: 'TorrentTV', monogram: 'TT' };
    expect(appIdentity()).toEqual({ name: 'TorrentTV', monogram: 'TT' });
  });
  it('derives the monogram when only a name is injected', () => {
    (window as any).FileListTVIdentity = { name: 'TorrentTV' };
    expect(appIdentity()).toEqual({ name: 'TorrentTV', monogram: 'TO' });
  });
  it('falls back when the injected name is blank', () => {
    (window as any).FileListTVIdentity = { name: '   ' };
    expect(appIdentity().name).toBe('FileList TV');
  });
});
