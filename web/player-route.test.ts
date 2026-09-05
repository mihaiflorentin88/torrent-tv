import { describe, expect, it } from 'vitest';
import { browserPlaybackURL } from './src';
import type { Download } from '@torrent-tv/shared';

const streamDownload: Download = {
  id: 'abc', releaseId: 'r', engineId: 'qb:x', fileIndex: 0, filePath: 'a.mkv',
  mimeType: 'video/x-matroska', sizeBytes: 1, state: 'downloading', progress: 0.1,
  downloadedBytes: 0, speedBytesPerSecond: 0, etaSeconds: 0, peers: 0, seeds: 0,
  leased: false, streamUrl: '/api/v1/streams/abc', browserStreamUrl: '/api/v1/streams/abc/browser', playbackMode: 'progressive',
};

const bareDownload: Download = { ...streamDownload, browserStreamUrl: undefined };

describe('browserPlaybackURL', () => {
  it('targets the compatibility stream with the selected track', () => {
    const url = browserPlaybackURL(streamDownload, 1, 0);
    expect(url).toBe('/api/v1/streams/abc/browser?audioTrack=1');
  });

  it('re-requests at a position with startMs for live-transcode seeking', () => {
    const url = browserPlaybackURL(streamDownload, 2, 61_500);
    expect(url).toBe('/api/v1/streams/abc/browser?audioTrack=2&startMs=61500');
  });

  it('omits parameters at the defaults', () => {
    expect(browserPlaybackURL(streamDownload, -1, 0)).toBe('/api/v1/streams/abc/browser');
  });

  it('falls back to the progressive stream when the server has no ffmpeg', () => {
    expect(browserPlaybackURL(bareDownload, 1, 5_000)).toBe('/api/v1/streams/abc');
  });
});
