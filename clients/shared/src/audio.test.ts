import { describe, expect, it } from 'vitest';
import { type MediaAudioTrack, type PlaybackPreferences, audioPlaybackRoute, fallbackAudioTrack } from '@torrent-tv/shared';

const track = (streamIndex: number, codec: string, extra: Partial<MediaAudioTrack> = {}): MediaAudioTrack => ({ streamIndex, codec, ...extra });
const preferences = (audioLanguage: string, audioTrackIndex = -1): Pick<PlaybackPreferences, 'audioLanguage' | 'audioTrackIndex'> => ({ audioLanguage, audioTrackIndex });

// Table tests for the pure routing seam: which codecs the browser plays through
// the video element (native) and which are handed to the client decoder.
describe('Audio playback route decision', () => {
  it('routes the natively decodable codecs to the element', () => {
    const cases: [string, 'native' | 'decode'][] = [
      ['aac', 'native'],
      ['mp3', 'native'],
      ['opus', 'native'],
      ['flac', 'native'],
      ['vorbis', 'native'],
    ];
    for (const [codec, expected] of cases) expect(audioPlaybackRoute(codec)).toBe(expected);
  });
  it('routes everything the browser cannot decode itself to the client decoder', () => {
    const cases: [string, 'native' | 'decode'][] = [
      ['ac3', 'decode'],
      ['eac3', 'decode'],
      ['dts', 'decode'],
      ['truehd', 'decode'],
      ['wmav2', 'decode'],
      ['notacodec', 'decode'],
      ['', 'decode'],
    ];
    for (const [codec, expected] of cases) expect(audioPlaybackRoute(codec)).toBe(expected);
  });
  it('normalizes case and surrounding whitespace before deciding', () => {
    expect(audioPlaybackRoute('AAC')).toBe('native');
    expect(audioPlaybackRoute(' Mp3 ')).toBe('native');
    expect(audioPlaybackRoute('EAC3')).toBe('decode');
  });
  it('answers missing codec strings with the decode route', () => {
    expect(audioPlaybackRoute(undefined)).toBe('decode');
    expect(audioPlaybackRoute(null)).toBe('decode');
  });
});

// Table tests for the decode-failure chooser: the best natively playable track
// left after the failed ones are excluded, in the same preference order the
// player uses at start (saved language/index, then default flag, then first).
describe('Fallback audio track chooser', () => {
  it('prefers the saved language over the default flag', () => {
    const tracks = [track(0, 'aac', { language: 'ja' }), track(1, 'mp3', { language: 'it', default: true })];
    expect(fallbackAudioTrack(tracks, preferences('ja'), [2])?.streamIndex).toBe(0);
  });
  it('skips the failed track even when it matches the saved preference', () => {
    const tracks = [track(0, 'aac', { language: 'ja' }), track(1, 'flac', { language: 'ja' })];
    expect(fallbackAudioTrack(tracks, preferences('ja', 0), [0])?.streamIndex).toBe(1);
  });
  it('falls to the default flag when the saved language has no playable track left', () => {
    const tracks = [track(0, 'aac', { language: 'ko' }), track(1, 'mp3', { language: 'pl', default: true })];
    expect(fallbackAudioTrack(tracks, preferences('ko', 0), [0])?.streamIndex).toBe(1);
  });
  it('falls to the first remaining track when nothing else matches', () => {
    const tracks = [track(0, 'aac', { language: 'sv' }), track(1, 'mp3', { language: 'da' })];
    expect(fallbackAudioTrack(tracks, preferences('sv', 0), [0])?.streamIndex).toBe(1);
  });
  it('never suggests a decode-only codec as a replacement', () => {
    const tracks = [track(0, 'ac3', { language: 'en' }), track(1, 'eac3', { language: 'de', default: true })];
    expect(fallbackAudioTrack(tracks, preferences('en', 0), [0])).toBeUndefined();
  });
  it('returns nothing when the failed track was the only playable one', () => {
    expect(fallbackAudioTrack([track(0, 'aac')], preferences('en', 0), [0])).toBeUndefined();
    expect(fallbackAudioTrack([track(0, 'aac')], preferences('en', 0), [])?.streamIndex).toBe(0);
  });
  it('excludes every track that already failed, so repeated failures cannot loop', () => {
    const tracks = [track(0, 'aac', { language: 'en' }), track(1, 'mp3', { default: true }), track(2, 'opus', { language: 'de' })];
    expect(fallbackAudioTrack(tracks, preferences('en', 0), [0, 1])?.streamIndex).toBe(2);
    expect(fallbackAudioTrack(tracks, preferences('en', 0), [0, 1, 2])).toBeUndefined();
  });
  it('answers an empty track list with nothing', () => {
    expect(fallbackAudioTrack([], preferences('en'), [])).toBeUndefined();
  });
});
