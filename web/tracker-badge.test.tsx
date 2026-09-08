import { render } from 'preact';
import type { ComponentChild } from 'preact';
import { act } from 'preact/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import type { Download } from '@torrent-tv/shared';
import { Downloads } from './downloads';
import { TrackerBadge, trackerBadgeClass, trackerSourceKey } from './tracker-badge';

const hosts: HTMLElement[] = [];

const mount = async (jsx: ComponentChild) => {
 const host = document.createElement('div');
 document.body.appendChild(host);
 hosts.push(host);
 await act(async () => { render(jsx, host) });
 return host;
};

afterEach(() => {
 while (hosts.length) render(null, hosts.pop()!);
 document.body.innerHTML = '';
});

describe('tracker source keys', () => {
 it('maps known tracker ids to a color key and unknown ids to neutral', () => {
  expect(trackerSourceKey('filelist')).toBe('filelist');
  expect(trackerSourceKey('piratebay')).toBe('piratebay');
  expect(trackerSourceKey('PirateBay')).toBe('piratebay');
  expect(trackerSourceKey('')).toBe('');
  expect(trackerSourceKey(undefined)).toBe('');
  expect(trackerSourceKey('somewhere-else')).toBe('');
 });

 it('builds badge classes from the id alone', () => {
  expect(trackerBadgeClass('filelist')).toBe('tracker-badge filelist');
  expect(trackerBadgeClass('piratebay')).toBe('tracker-badge piratebay');
  expect(trackerBadgeClass('unknown')).toBe('tracker-badge');
 });
});

describe('TrackerBadge', () => {
 it('renders the display name with the id-keyed class', async () => {
  const host = await mount(<TrackerBadge id="filelist" name="FileList" />);
  const badge = host.querySelector('.tracker-badge')!;
  expect(badge.className).toBe('tracker-badge filelist');
  expect(badge.textContent).toBe('FileList');
 });

 it('falls back to the id and to Unknown tracker when unnamed', async () => {
  const named = await mount(<TrackerBadge id="piratebay" />);
  expect(named.querySelector('.tracker-badge')!.textContent).toBe('piratebay');
  const unnamed = await mount(<TrackerBadge />);
  expect(unnamed.querySelector('.tracker-badge')!.textContent).toBe('Unknown tracker');
 });
});

describe('Downloads tracker badges', () => {
 it('shows the source badge on the download card', async () => {
  const download: Download = {
   id: 'd1', releaseId: 'fl-1', trackerId: 'filelist', trackerName: 'FileList', engineId: 'native:abc',
   fileIndex: 0, filePath: 'Movie.2024.1080p.mkv', mimeType: 'video/x-matroska', sizeBytes: 1024,
   state: 'downloading', progress: 0.5, playbackMode: 'progressive', downloadedBytes: 512,
   speedBytesPerSecond: 0, etaSeconds: 0, peers: 1, seeds: 2, leased: false, streamUrl: '/api/v1/downloads/d1/stream',
  };
  await mount(<Downloads items={[download]} onRefresh={() => { }} onPlay={() => { }} onRemove={async () => { }} onAction={async () => { }} />);
  const badge = document.querySelector('.download-list .tracker-badge')!;
  expect(badge.className).toContain('filelist');
  expect(badge.textContent).toBe('FileList');
 });
});
