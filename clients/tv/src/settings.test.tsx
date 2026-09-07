// @vitest-environment happy-dom
import { render } from 'preact';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { API } from '@torrent-tv/shared';
// Static import cannot work here: main.tsx bootstraps and asserts the presence
// of document.getElementById('app') during module evaluation. The #app container
// must be mounted to the DOM before main.tsx evaluates.
if (typeof globalThis.localStorage === 'undefined' || !globalThis.localStorage.getItem) {
  const store = new Map<string, string>();
  globalThis.localStorage = {
    getItem: (key: string) => store.get(key) || null,
    setItem: (key: string, val: string) => { store.set(key, val); },
    removeItem: (key: string) => { store.delete(key); },
    clear: () => { store.clear(); },
    get length() { return store.size; },
    key: () => null,
  } as Storage;
}

const appRoot = document.createElement('div');
appRoot.id = 'app';
document.body.appendChild(appRoot);

const { TVSettings } = await import('./main');

const settingsValue = (engineRunning: string): Record<string, unknown> => ({
  downloadEngine: 'native',
  engineRunning,
  preferredAudioLanguage: 'en',
  preferredSubtitleLanguage: 'en',
  fallbackSubtitleLanguage: 'ro',
  watchedThresholdPercent: 90,
  maxConcurrentJobs: 10,
  titleRefreshTimeoutMinutes: 30,
  fileListEnabled: true,
  pirateBayEnabled: false,
  pirateBayWebsiteUrl: 'https://piratebay.example',
  pirateBayApiUrl: 'https://piratebay.example/api',
});

function fakeApi(value: Record<string, unknown>) {
  return {
    call: async (path: string, init?: RequestInit): Promise<unknown> => {
      const method = init?.method || 'GET';
      if (path === '/settings' && method === 'GET') return value;
      if (path === '/settings/schema') return { items: [] };
      if (path === '/trackers') return [];
      // Belt-and-braces against the strip bug: the strict server decoder
      // rejects unknown fields, so a save that ever fires here must fail loudly.
      throw new Error(`unexpected API call ${method} ${path}`);
    },
  } as unknown as API;
}

describe('TV engine ownership feedback', () => {
  let container: HTMLDivElement;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
  });

  afterEach(() => {
    render(null, container);
    container.remove();
  });

  it('names the running engine when it differs from the saved selection', async () => {
    render(
      <TVSettings
        api={fakeApi(settingsValue('qbittorrent'))}
        onChangeServer={() => { }}
        onForgetServer={() => { }}
        updateStatus={null}
        onUpdateStatus={() => { }}
        confirmOpen={false}
        onConfirmOpen={() => { }}
        onConfirmClose={() => { }}
      />,
      container
    );
    await new Promise(r => setTimeout(r, 20));
    expect(container.textContent).toContain('Running now: qBittorrent');
    expect(container.textContent).toContain('new downloads after restart');
  });

  it('keeps the ownership copy without a running notice when the engine agrees', async () => {
    render(
      <TVSettings
        api={fakeApi(settingsValue('native'))}
        onChangeServer={() => { }}
        onForgetServer={() => { }}
        updateStatus={null}
        onUpdateStatus={() => { }}
        confirmOpen={false}
        onConfirmOpen={() => { }}
        onConfirmClose={() => { }}
      />,
      container
    );
    await new Promise(r => setTimeout(r, 20));
    expect(container.textContent).toContain('Existing downloads keep the engine that owns them');
    expect(container.textContent).not.toContain('Running now');
  });
});
