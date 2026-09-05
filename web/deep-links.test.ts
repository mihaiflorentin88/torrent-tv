import { describe, expect, it } from 'vitest';
import { buildPath, parsePath } from '@torrent-tv/shared';
import type { Download } from '@torrent-tv/shared';
import { watchRoute } from './src';

const download: Download = {
  id: 'dl-9', releaseId: 'r', engineId: 'qb:x', fileIndex: 2, filePath: 'a.mkv',
  mimeType: 'video/x-matroska', sizeBytes: 1, state: 'downloading', progress: 0.1,
  downloadedBytes: 0, speedBytesPerSecond: 0, etaSeconds: 0, peers: 0, seeds: 0,
  leased: false, streamUrl: '/api/v1/streams/dl-9', playbackMode: 'progressive',
};

// Watch deep links: the player entry point records the Managed download, the
// Source file index, and the resume position so a refresh or a shared link
// lands back in the same playback (resume behavior stays the player's own).
describe('watchRoute', () => {
  it('records the download, Source index, and resume position', () => {
    expect(buildPath(watchRoute(download, 61_500))).toBe('/watch/dl-9?source=2&t=61500');
  });

  it('omits the Source index for whole-release downloads and the absent resume', () => {
    expect(buildPath(watchRoute({ ...download, fileIndex: -1 }, 0))).toBe('/watch/dl-9');
  });

  it('round-trips through parsePath for cold load', () => {
    const [pathname, search] = buildPath(watchRoute(download, 61_500)).split('?');
    expect(parsePath(pathname, search || '')).toEqual({ view: 'watch', id: 'dl-9', query: '', source: 2, t: 61_500 });
  });
});
