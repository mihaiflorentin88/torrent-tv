import {describe, expect, it} from 'vitest';
import {clampSeek, formatTime, hiddenKeyRoute, isDownloadComplete, normalizeTrack, parseVTT, playerAction, preferredAudio, preferredSubtitle, subtitleAt} from './player';

describe('player helpers', () => {
  it('formats player time', () => {expect(formatTime(65_400)).toBe('1:05'); expect(formatTime(3_665_000)).toBe('1:01:05');});
  it('clamps seeking inside the playable duration', () => {expect(clampSeek(-10, 100_000)).toBe(0); expect(clampSeek(100_000, 100_000)).toBe(99_000);});
  it('recognizes complete downloads', () => {expect(isDownloadComplete({progress:1} as any)).toBe(true); expect(isDownloadComplete({progress:.5,state:'downloading',downloadedBytes:5,sizeBytes:10} as any)).toBe(false);});
  it('maps remote media and navigation keys', () => {expect(playerAction('ArrowRight', 0)).toBe('right'); expect(playerAction('', 10252)).toBe('play-pause'); expect(playerAction('', 417)).toBe('fast-forward');});
  it('normalizes tracks and prefers Romanian then English subtitles', () => {
    const english = normalizeTrack({index:1,type:'TEXT',extra_info:JSON.stringify({language:'eng',codec:'srt'})});
    const romanian = normalizeTrack({index:2,type:'TEXT',extra_info:JSON.stringify({track_lang:'ron',fourCC:'srt'})});
    expect(english.label).toBe('English · srt');
    expect(preferredSubtitle([english, romanian])?.index).toBe(2);
    expect(preferredSubtitle([english])?.index).toBe(1);
  });
  it('keeps an audio index only when its saved language still matches',()=>{const english={index:1,type:'AUDIO',language:'eng',label:'English'};const romanian={index:3,type:'AUDIO',language:'ron',label:'Romanian'};expect(preferredAudio([english,romanian],'ro',3)?.index).toBe(3);expect(preferredAudio([english,romanian],'ro',1)?.index).toBe(3);expect(preferredAudio([english,romanian],'de',-1)?.index).toBe(1)});
  it('parses and displays WebVTT cues at the requested playback time',()=>{const cues=parseVTT('WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.000\nSalut!\n\n00:03.500 --> 00:00:05.000\nHello');expect(cues).toHaveLength(2);expect(subtitleAt(cues,2_000)).toBe('Salut!');expect(subtitleAt(cues,4_000)).toBe('Hello');expect(subtitleAt(cues,500)).toBe('');});
  it('reveals hidden controls before routing every recognized remote key', () => {
    expect(hiddenKeyRoute('left')).toBe('scrub-left');
    expect(hiddenKeyRoute('right')).toBe('scrub-right');
    expect(hiddenKeyRoute('up')).toBe('refocus');
    expect(hiddenKeyRoute('down')).toBe('refocus');
    expect(hiddenKeyRoute('enter')).toBe('refocus');
    expect(hiddenKeyRoute('play')).toBe('route');
    expect(hiddenKeyRoute('pause')).toBe('route');
    expect(hiddenKeyRoute('play-pause')).toBe('route');
    expect(hiddenKeyRoute('rewind')).toBe('route');
    expect(hiddenKeyRoute('fast-forward')).toBe('route');
    expect(hiddenKeyRoute('previous')).toBe('route');
    expect(hiddenKeyRoute('next')).toBe('route');
    expect(hiddenKeyRoute('back')).toBe('route');
    expect(hiddenKeyRoute('stop')).toBe('route');
    expect(hiddenKeyRoute(null)).toBeNull();
  });
});

describe('playerAction Android media keys', () => {
  it('maps Android media key names', () => {
    expect(playerAction('MediaPlay', 0)).toBe('play');
    expect(playerAction('MediaPause', 0)).toBe('pause');
    expect(playerAction('MediaPlayPause', 179)).toBe('play-pause');
    expect(playerAction('MediaStop', 178)).toBe('stop');
    expect(playerAction('MediaRewind', 177)).toBe('rewind');
    expect(playerAction('MediaFastForward', 228)).toBe('fast-forward');
    expect(playerAction('MediaTrackPrevious', 227)).toBe('previous');
    expect(playerAction('MediaTrackNext', 226)).toBe('next');
  });
  it('maps Android back arrivals', () => {
    expect(playerAction('GoBack', 0)).toBe('back');
    expect(playerAction('BrowserBack', 0)).toBe('back');
    expect(playerAction('Escape', 27)).toBe('back');
  });
});
