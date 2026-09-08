import { render } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Settings } from './settings';

// Settings form controls beyond plain inputs: the searchable language
// combobox and the fixed-unit byte fields. The contract under test is
// compatibility — a stored value the option lists do not know renders and
// saves verbatim, and byte-backed fields keep saving raw bytes.
const settingsValue: Record<string, unknown> = {
 settingsPath: 'data/settings.json',
 metadataLanguage: 'ro-RO', metadataFallbackLanguage: 'en-US',
 preferredAudioLanguage: 'en', preferredSubtitleLanguage: 'en', fallbackSubtitleLanguage: 'ro',
 initialBufferBytes: 4194304, readAheadBytes: 8388608, pieceWaitTimeoutSeconds: 30,
 subtitleCachePath: 'data/subtitles', subtitleCacheMaxBytes: 536870912,
 downloadRoot: '/data', allocationGb: 15, reserveGb: 8, evictionRules: ['oldest-completed'],
 artworkCachePath: 'data/artwork', artworkCacheMaxBytes: 1073741824,
 fileListUrl: 'https://filelist.io', tmdbApiKey: '', tmdbApiKeyConfigured: true,
 downloadEngine: 'native',
};

const savedValues: Array<Record<string, unknown>> = [];
const hosts: HTMLElement[] = [];

async function mountSettings(value: Record<string, unknown> = settingsValue) {
 const host = document.createElement('div');
 document.body.appendChild(host);
 hosts.push(host);
 await act(async () => {
  render(<Settings value={value} fields={[]} onSaved={() => { }} onError={() => { }} save={async out => { savedValues.push(out); return { saved: true } }} />, host);
 });
}

const settle = async () => {
 for (let i = 0; i < 3; i++) {
  await act(async () => {
   const { promise, resolve } = Promise.withResolvers<void>();
   setTimeout(resolve, 0);
   await promise;
  });
 }
};

const settingsTabs = () => Array.from(document.querySelectorAll<HTMLButtonElement>('.settings-tabs button'));

const openTab = async (label: string) => {
 await act(async () => { settingsTabs().find(button => button.textContent === label)!.click() });
};

const labeled = (label: string) => Array.from(document.querySelectorAll('.settings-panel label')).find(item => item.querySelector('span')?.textContent?.startsWith(label))!;

const fieldInput = (label: string) => labeled(label).querySelector('input')!;

const typeInto = (input: HTMLInputElement, value: string) => {
 input.value = value;
 input.dispatchEvent(new Event('input', { bubbles: true }));
};

const openOptions = async (label: string) => {
 const input = fieldInput(label);
 await act(async () => { input.focus() });
 return Array.from(document.querySelectorAll('.language-list li'));
};

const saveForm = async () => {
 await act(async () => { (document.querySelector('.settings-actions button[type="submit"]') as HTMLButtonElement).click() });
 await settle();
};

beforeEach(() => {
 savedValues.length = 0;
});

afterEach(() => {
 while (hosts.length) render(null, hosts.pop()!);
 document.body.innerHTML = '';
 // Tab switches ride the URL hash; a leftover hash would reopen the wrong
 // tab on the next mount.
 window.history.replaceState(null, '', '/');
});

describe('language combobox fields', () => {
 it('renders the stored value verbatim, even when no option matches it', async () => {
  await mountSettings({ ...settingsValue, metadataFallbackLanguage: 'zz' });
  const input = fieldInput('Metadata fallback language');
  expect(input.value).toBe('zz');
  const options = await openOptions('Metadata fallback language');
  expect(options.some(item => item.textContent?.endsWith('— zz'))).toBe(false);
 });

 it('filters by typed name or code and commits the exact canonical code', async () => {
  await mountSettings({ ...settingsValue, metadataLanguage: 'en-US' });
  const input = fieldInput('Metadata language');
  await act(async () => { input.focus() });
  await act(async () => { typeInto(input, 'roman') });
  const options = Array.from(document.querySelectorAll('.language-list li'));
  expect(options.map(item => item.textContent)).toContain('Romanian — ro-RO');
  expect(options.map(item => item.textContent)).not.toContain('Romanian — ro');
  expect(options.some(item => item.textContent?.includes('Zulu'))).toBe(false);
  await act(async () => { (options.find(item => item.textContent === 'Romanian — ro-RO') as HTMLElement).click() });
  expect(input.value).toBe('ro-RO');
  expect(document.querySelector('.settings-tabs button.dirty')?.textContent).toBe('Tracker');
  await saveForm();
  expect(savedValues[0].metadataLanguage).toBe('ro-RO');
 });

 it('keeps a code typed by hand when no option matches', async () => {
  await mountSettings();
  await openTab('Playback');
  const input = fieldInput('Preferred audio language');
  await act(async () => { typeInto(input, 'xk') });
  await act(async () => { input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })) });
  expect(input.value).toBe('xk');
  await saveForm();
  expect(savedValues[0].preferredAudioLanguage).toBe('xk');
 });

 it('offers region variants on metadata fields and plain codes on playback fields', async () => {
  await mountSettings();
  const metadataOptions = await openOptions('Metadata fallback language');
  expect(metadataOptions.map(item => item.textContent)).toContain('Portuguese — pt-BR');
  // Metadata options are region-coded where TMDB tags a region: no bare ro.
  expect(metadataOptions.map(item => item.textContent)).not.toContain('Romanian — ro');
  expect(metadataOptions.map(item => item.textContent)).toContain('Romanian — ro-RO');
  await act(async () => { document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })) });
  await openTab('Playback');
  const playbackOptions = await openOptions('Preferred subtitle language');
  expect(playbackOptions.map(item => item.textContent)).toContain('Romanian — ro');
  expect(playbackOptions.some(item => /[a-z]{2}-[A-Z]{2}$/.test(item.textContent || ''))).toBe(false);
 });
});

describe('byte-unit fields', () => {
 it('displays stored bytes in the field fixed unit', async () => {
  await mountSettings();
  await openTab('Playback');
  expect(fieldInput('Initial buffer').value).toBe('4');
  expect(labeled('Initial buffer').querySelector('.byte-unit')?.textContent).toBe('MB');
  expect(fieldInput('Read-ahead').value).toBe('8');
  expect(fieldInput('Subtitle cache maximum').value).toBe('0.5');
  expect(labeled('Subtitle cache maximum').querySelector('.byte-unit')?.textContent).toBe('GB');
  await openTab('Storage');
  expect(fieldInput('Artwork cache maximum').value).toBe('1');
  expect(labeled('Artwork cache maximum').querySelector('.byte-unit')?.textContent).toBe('GB');
 });

 it('saves raw bytes converted back from the edited unit value', async () => {
  await mountSettings();
  await openTab('Playback');
  await act(async () => { typeInto(fieldInput('Initial buffer'), '16') });
  await act(async () => { typeInto(fieldInput('Subtitle cache maximum'), '2') });
  await saveForm();
  expect(savedValues[0].initialBufferBytes).toBe(16 * 1024 * 1024);
  expect(savedValues[0].subtitleCacheMaxBytes).toBe(2 * 1024 * 1024 * 1024);
  expect(savedValues[0].readAheadBytes).toBe(8388608);
 });

 it('stays clean when byte fields are untouched', async () => {
  await mountSettings();
  expect(document.querySelector('.settings-tabs button.dirty')).toBeNull();
  expect((document.querySelector('.settings-actions button[type="submit"]') as HTMLButtonElement).disabled).toBe(true);
 });

 it('resnaps the text after a discard reverts the underlying bytes', async () => {
  await mountSettings();
  await openTab('Playback');
  await act(async () => { typeInto(fieldInput('Initial buffer'), '32') });
  await act(async () => { (Array.from(document.querySelectorAll('.settings-actions button')).find(button => button.textContent === 'Discard changes') as HTMLButtonElement).click() });
  expect(fieldInput('Initial buffer').value).toBe('4');
 });
});
