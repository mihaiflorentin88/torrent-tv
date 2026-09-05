import { describe, expect, it } from 'vitest';
import { type SubtitleCandidate, subtitleRank, canonicalLanguage, languageDisplayName, subtitleItemLabel, subtitleMenuGroups } from '@torrent-tv/shared';

describe('Canonical language normalization', () => {
  it('maps ISO-639-2 word codes to their canonical 639-1 code', () => {
    expect(canonicalLanguage('ron')).toBe('ro');
    expect(canonicalLanguage('rum')).toBe('ro');
    expect(canonicalLanguage('eng')).toBe('en');
    expect(canonicalLanguage('jpn')).toBe('ja');
    expect(canonicalLanguage('fre')).toBe('fr');
    expect(canonicalLanguage('fra')).toBe('fr');
    expect(canonicalLanguage('ger')).toBe('de');
    expect(canonicalLanguage('deu')).toBe('de');
    expect(canonicalLanguage('zho')).toBe('zh');
    expect(canonicalLanguage('chi')).toBe('zh');
    expect(canonicalLanguage('dut')).toBe('nl');
    expect(canonicalLanguage('nld')).toBe('nl');
    expect(canonicalLanguage('srp')).toBe('sr');
    expect(canonicalLanguage('scc')).toBe('sr');
    expect(canonicalLanguage('hrv')).toBe('hr');
    expect(canonicalLanguage('scr')).toBe('hr');
    expect(canonicalLanguage('grn')).toBe('gn');
    expect(canonicalLanguage('mao')).toBe('mi');
    expect(canonicalLanguage('mri')).toBe('mi');
  });
  it('keeps canonical codes and strips region and script subtags', () => {
    expect(canonicalLanguage('ro')).toBe('ro');
    expect(canonicalLanguage('EN')).toBe('en');
    expect(canonicalLanguage('en-US')).toBe('en');
    expect(canonicalLanguage('pt_BR')).toBe('pt');
    expect(canonicalLanguage(' zh-Hans-CN ')).toBe('zh');
  });
  it('answers unknown input with an empty canonical code', () => {
    expect(canonicalLanguage('')).toBe('');
    expect(canonicalLanguage('  ')).toBe('');
    expect(canonicalLanguage('tlh')).toBe('');
    expect(canonicalLanguage('zz')).toBe('');
    expect(canonicalLanguage('qaa-RO')).toBe('');
    expect(canonicalLanguage('123')).toBe('');
    expect(canonicalLanguage('und')).toBe('');
    expect(canonicalLanguage('in')).toBe('');
    expect(canonicalLanguage('iw')).toBe('');
    expect(canonicalLanguage('ji')).toBe('');
    expect(canonicalLanguage('jw')).toBe('');
    expect(canonicalLanguage('sh')).toBe('');
    expect(canonicalLanguage('bh')).toBe('');
    expect(canonicalLanguage('bih')).toBe('');
    expect(subtitleRank('por', 'pt')).toBe(0);
    expect(subtitleRank('ron', 'ro')).toBe(0);
    expect(subtitleRank('pol', 'pt')).toBe(3);
  });
  it('names canonical codes and leaves unknown ones unnamed', () => {
    expect(languageDisplayName('ro')).toBe('Romanian');
    expect(languageDisplayName('en')).toBe('English');
    expect(languageDisplayName('ja')).toBe('Japanese');
    expect(languageDisplayName('zh')).toBe('Chinese');
    expect(languageDisplayName('')).toBe('');
    expect(languageDisplayName('zz')).toBe('');
    expect(languageDisplayName(canonicalLanguage('rum'))).toBe('Romanian');
  });
});

const candidate = (id: string, provider: string, overrides: Partial<SubtitleCandidate> = {}): SubtitleCandidate => ({ language: '', title: '', score: 0, cached: false, id, provider, ...overrides });

describe('Subtitle menu grouping', () => {
  it('orders Local, Built-in, then provider groups', () => {
    const groups = subtitleMenuGroups([
      candidate('s1', 'subdl', { providerLabel: 'SubDL', title: 'SubDL Romanian' }),
      candidate('e1', 'embedded', { providerLabel: 'Embedded', title: 'Track 3' }),
      candidate('c1', 'contained', { providerLabel: 'Included', title: 'Movie.ro.srt' }),
      candidate('s2', 'subdl', { providerLabel: 'SubDL', title: 'SubDL English' }),
    ]);
    expect(groups.map(group => group.key)).toEqual(['local', 'embedded', 'subdl']);
    expect(groups.map(group => group.label)).toEqual(['Local', 'Built-in', 'SubDL']);
    expect(groups[0].items.map(item => item.id)).toEqual(['c1']);
    expect(groups[2].items.map(item => item.id)).toEqual(['s1', 's2']);
  });
  it('lands cached provider candidates under Local', () => {
    const groups = subtitleMenuGroups([candidate('s7', 'subdl', { providerLabel: 'SubDL', title: 'Cached sidecar', cached: true })]);
    expect(groups.map(group => group.key)).toEqual(['local']);
    expect(groups[0].items.map(item => item.id)).toEqual(['s7']);
  });
  it('keeps stable in-group order and names unlabeled providers by name', () => {
    const groups = subtitleMenuGroups([
      candidate('a', 'subdl', { providerLabel: 'SubDL', title: 'First' }),
      candidate('b', 'subdl', { title: 'Second' }),
      candidate('c', 'opensubtitles', { title: 'Third' }),
    ]);
    expect(groups.map(group => group.key)).toEqual(['subdl', 'opensubtitles']);
    expect(groups.map(group => group.label)).toEqual(['SubDL', 'opensubtitles']);
    expect(groups[0].items.map(item => item.id)).toEqual(['a', 'b']);
  });
  it('answers an empty candidate list with no groups', () => {
    expect(subtitleMenuGroups([])).toEqual([]);
  });
});

describe('Subtitle item labels', () => {
  it('prefers the language hint and resolves its display name', () => {
    expect(subtitleItemLabel(candidate('1', 'subdl', { language: 'ron', title: 'Some release name' }), 1)).toBe('Romanian');
    expect(subtitleItemLabel(candidate('1', 'embedded', { language: 'en-US', title: 'Track 2' }), 1)).toBe('English');
  });
  it('falls back from language to title to file name to codec', () => {
    expect(subtitleItemLabel(candidate('1', 'subdl', { language: 'xx', title: 'Commentary' }), 1)).toBe('Commentary');
    expect(subtitleItemLabel(candidate('1', 'contained', { title: '  ', fileName: 'Silo.S01E03.srt' }), 1)).toBe('Silo.S01E03.srt');
    expect(subtitleItemLabel(candidate('1', 'embedded', { format: 'vtt' }), 1)).toBe('vtt');
  });
  it('labels candidates without hints Unknown with their stable 1-based position', () => {
    expect(subtitleItemLabel(candidate('1', 'subdl', {}), 1)).toBe('Unknown 1');
    expect(subtitleItemLabel(candidate('2', 'subdl', {}), 7)).toBe('Unknown 7');
  });
});
