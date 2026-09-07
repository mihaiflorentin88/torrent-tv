// @vitest-environment happy-dom
import { render } from 'preact';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { API, CatalogFacets, HouseholdState, PortalState } from '@torrent-tv/shared';
import { exitApplication } from '../../tv/src/platform';

// Polyfill localStorage if needed in happy-dom test runner before main boots
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

// Static import cannot work here: main.tsx bootstraps and asserts the presence
// of document.getElementById('app') during module evaluation. The #app container
// must be mounted to the DOM before main.tsx evaluates.
const appRoot = document.createElement('div');
appRoot.id = 'app';
document.body.appendChild(appRoot);

const { Catalog } = await import('../../tv/src/main');
import {
  exit,
  getNetworkInfo,
  onKeyboardVisibility,
  onVisibility,
  openExternal,
  webOSPlatform,
} from './webos-platform';
import type {
  WebOSConnectionStatusParameters,
  WebOSConnectionStatusResponse,
  WebOSDev,
  WebOSLaunchParameters,
} from './webos.d.ts';

const testWindow = window as unknown as {
  webOSDev?: WebOSDev;
  close: () => void;
};


describe('webOS platform hooks contract', () => {
  let closeCalls = 0;

  beforeEach(() => {
    closeCalls = 0;
    testWindow.close = () => { closeCalls++; };
  });

  afterEach(() => {
    delete testWindow.webOSDev;
    vi.useRealTimers();
  });

  describe('exit', () => {
    it('dispatches window.close() exactly once', () => {
      exit();
      expect(closeCalls).toBe(1);
    });

    it('shared exitApplication dispatches window.close() once through FileListTVPlatform', () => {
      // webos-platform exports and installs webOSPlatform on window.FileListTVPlatform
      expect(window.FileListTVPlatform).toBe(webOSPlatform);
      exitApplication();
      expect(closeCalls).toBe(1);
    });
  });

  describe('getNetworkInfo', () => {
    it('acquires active wired network status with ipAddress and netmask', async () => {
      testWindow.webOSDev = {
        connection: {
          getStatus: (params: WebOSConnectionStatusParameters) => {
            params.onSuccess?.({
              wired: {
                state: 'connected',
                ipAddress: '192.168.1.120',
                netmask: '255.255.255.0',
              },
              wifi: { state: 'disconnected' },
            });
          },
        },
        launch: () => { },
      };

      const info = await getNetworkInfo();
      expect(info).toEqual({
        ip: '192.168.1.120',
        subnetMask: '255.255.255.0',
      });
    });

    it('falls back to wifi network status when wired is disconnected', async () => {
      testWindow.webOSDev = {
        connection: {
          getStatus: (params: WebOSConnectionStatusParameters) => {
            params.onSuccess?.({
              wired: { state: 'disconnected' },
              wifi: {
                state: 'connected',
                ipAddress: '192.168.1.125',
                netmask: '255.255.255.0',
              },
            });
          },
        },
        launch: () => { },
      };

      const info = await getNetworkInfo();
      expect(info).toEqual({
        ip: '192.168.1.125',
        subnetMask: '255.255.255.0',
      });
    });

    it('returns null when neither interface is connected', async () => {
      testWindow.webOSDev = {
        connection: {
          getStatus: (params: WebOSConnectionStatusParameters) => {
            params.onSuccess?.({
              wired: { state: 'disconnected' },
              wifi: { state: 'disconnected' },
            });
          },
        },
        launch: () => { },
      };

      expect(await getNetworkInfo()).toBeNull();
    });

    it('returns null when SDK calls onFailure', async () => {
      testWindow.webOSDev = {
        connection: {
          getStatus: (params: WebOSConnectionStatusParameters) => {
            params.onFailure?.({ errorCode: 500, errorText: 'Service unavailable' });
          },
        },
        launch: () => { },
      };

      expect(await getNetworkInfo()).toBeNull();
    });

    it('returns null when getStatus throws synchronously', async () => {
      testWindow.webOSDev = {
        connection: {
          getStatus: () => {
            throw new Error('Luna service failure');
          },
        },
        launch: () => { },
      };

      expect(await getNetworkInfo()).toBeNull();
    });

    it('resolves null on 3-second timeout when SDK neither succeeds nor fails', async () => {
      vi.useFakeTimers();

      testWindow.webOSDev = {
        connection: {
          getStatus: () => {
            // Hanging call: neither callback is ever invoked
          },
        },
        launch: () => { },
      };

      const infoPromise = getNetworkInfo();
      vi.advanceTimersByTime(3000);
      const info = await infoPromise;
      expect(info).toBeNull();
    });

    it('does not read speculative netmaks alias if netmask is missing', async () => {
      testWindow.webOSDev = {
        connection: {
          getStatus: (params: WebOSConnectionStatusParameters) => {
            // Pass an object with only the speculative 'netmaks' property
            const rawResponse = {
              wired: {
                state: 'connected',
                ipAddress: '192.168.1.120',
                netmaks: '255.255.255.0',
              },
            } as unknown as WebOSConnectionStatusResponse;
            params.onSuccess?.(rawResponse);
          },
        },
        launch: () => { },
      };

      // Since netmask is missing, it must return null rather than reading netmaks
      expect(await getNetworkInfo()).toBeNull();
    });
  });

  describe('openExternal', () => {
    it('launches com.webos.app.browser with target url and resolves true on success', async () => {
      let launchedParams: WebOSLaunchParameters | null = null;
      testWindow.webOSDev = {
        connection: { getStatus: () => { } },
        launch: (params: WebOSLaunchParameters) => {
          launchedParams = params;
          params.onSuccess?.({ returnValue: true });
        },
      };

      const result = await openExternal('https://example.com/shows');
      expect(result).toBe(true);
      expect(launchedParams).toEqual(expect.objectContaining({
        id: 'com.webos.app.browser',
        params: { target: 'https://example.com/shows' },
      }));
    });

    it('resolves false on onFailure callback', async () => {
      testWindow.webOSDev = {
        connection: { getStatus: () => { } },
        launch: (params: WebOSLaunchParameters) => {
          params.onFailure?.({ errorCode: -1, errorText: 'Browser app missing' });
        },
      };

      const result = await openExternal('https://example.com/shows');
      expect(result).toBe(false);
    });

    it('rejects non-http(s) destinations immediately without invoking webOSDev.launch', async () => {
      let launchCalled = false;
      testWindow.webOSDev = {
        connection: { getStatus: () => { } },
        launch: () => { launchCalled = true; },
      };

      expect(await openExternal('ftp://ftp.example.com')).toBe(false);
      expect(await openExternal('javascript:alert(1)')).toBe(false);
      expect(await openExternal('')).toBe(false);
      expect(launchCalled).toBe(false);
    });

    it('resolves false on 3-second timeout when launch hangs', async () => {
      vi.useFakeTimers();

      testWindow.webOSDev = {
        connection: { getStatus: () => { } },
        launch: () => {
          // Hanging launch
        },
      };

      const launchPromise = openExternal('https://example.com/hang');
      vi.advanceTimersByTime(3000);
      expect(await launchPromise).toBe(false);
    });

    it('resolves false when webOSDev.launch throws', async () => {
      testWindow.webOSDev = {
        connection: { getStatus: () => { } },
        launch: () => { throw new Error('Luna bus unavailable'); },
      };

      expect(await openExternal('https://example.com/error')).toBe(false);
    });
  });

  describe('onKeyboardVisibility', () => {
    it('forwards only boolean detail.visibility on keyboardStateChange', () => {
      const received: boolean[] = [];
      const unsub = onKeyboardVisibility(visible => {
        received.push(visible);
      });

      document.dispatchEvent(new CustomEvent('keyboardStateChange', {
        detail: { visibility: true },
      }));
      document.dispatchEvent(new CustomEvent('keyboardStateChange', {
        detail: { visibility: false },
      }));
      // Non-boolean detail must be ignored
      document.dispatchEvent(new CustomEvent('keyboardStateChange', {
        detail: { visibility: 'yes' },
      }));
      document.dispatchEvent(new CustomEvent('keyboardStateChange', {
        detail: {},
      }));

      expect(received).toEqual([true, false]);

      unsub();
      document.dispatchEvent(new CustomEvent('keyboardStateChange', {
        detail: { visibility: true },
      }));
      // No further events after unsubscribe
      expect(received).toEqual([true, false]);
    });
  });

  describe('onVisibility', () => {
    it('forwards !document.hidden and dedupes consecutive identical states', () => {
      const received: boolean[] = [];
      const unsub = onVisibility(visible => {
        received.push(visible);
      });

      // Simulate visibility change to hidden
      Object.defineProperty(document, 'hidden', { value: true, configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));

      // Duplicate hidden event (should be deduped)
      document.dispatchEvent(new Event('visibilitychange'));

      // Simulate visibility change to visible
      Object.defineProperty(document, 'hidden', { value: false, configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));

      expect(received).toEqual([false, true]);
      unsub();
      Object.defineProperty(document, 'hidden', { value: true, configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));
      // No further events after unsubscribe
      expect(received).toEqual([false, true]);
    });

    it('reconciles visible state on webOSRelaunch only when document.hidden is false', () => {
      const received: boolean[] = [];
      const unsub = onVisibility(visible => {
        received.push(visible);
      });

      // Move to hidden
      Object.defineProperty(document, 'hidden', { value: true, configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));
      expect(received).toEqual([false]);

      // webOSRelaunch arrives while document is still hidden (duplicate launch event): MUST ignore
      document.dispatchEvent(new Event('webOSRelaunch'));
      expect(received).toEqual([false]);

      // Document becomes visible and webOSRelaunch reconciles
      Object.defineProperty(document, 'hidden', { value: false, configurable: true });
      document.dispatchEvent(new Event('webOSRelaunch'));
      expect(received).toEqual([false, true]);

      // Duplicate webOSRelaunch while already visible: deduped
      document.dispatchEvent(new Event('webOSRelaunch'));
      expect(received).toEqual([false, true]);

      // Clean unsubscription removes relaunch listener
      unsub();
      Object.defineProperty(document, 'hidden', { value: true, configurable: true });
      document.dispatchEvent(new Event('visibilitychange'));
      Object.defineProperty(document, 'hidden', { value: false, configurable: true });
      document.dispatchEvent(new Event('webOSRelaunch'));
      expect(received).toEqual([false, true]);
    });
  });
});

describe('webOS behavioral regression: link refusal and exit', () => {
  let container: HTMLDivElement;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
  });

  afterEach(() => {
    render(null, container);
    container.remove();
    delete testWindow.webOSDev;
  });

  it('retains displayed address and never shows launch success on link refusal', async () => {
    const targetUrl = 'https://portal.example.org/tracker';

    // Mock webOSDev.launch to refuse the launch
    testWindow.webOSDev = {
      connection: { getStatus: () => { } },
      launch: (params: WebOSLaunchParameters) => {
        // Asynchronously simulate OS rejecting the browser launch
        setTimeout(() => {
          params.onFailure?.({ errorCode: 404, errorText: 'App not found' });
        }, 10);
      },
    };

    const portal: PortalState = {
      accountsEnabled: false,
      adsEnabled: false,
      donor: false,
      links: [
        {
          id: 1,
          title: 'webOS Portal',
          url: targetUrl,
          description: 'Platform link',
        },
      ],
    };

    const mockApi = {
      titles: async () => ({ items: [], nextCursor: null }),
      ensureMetadata: async () => ({ queued: 0 }),
      streamURL: (p: string) => p,
      call: async () => ({}),
    };

    render(
      <Catalog
        api={mockApi as unknown as API}
        status="Ready"
        titles={[]}
        facets={{} as CatalogFacets}
        household={{ favorites: [], continueWatching: [], recent: [], watched: [] }}
        downloads={[]}
        jobs={[]}
        restoreFocus={null}
        portal={portal}
        updateStatus={null}
        onUpdateStatus={() => { }}
        onFocus={() => { }}
        onRetry={() => { }}
        onChangeServer={() => { }}
        onForgetServer={() => { }}
        onPlay={() => { }}
        onPlayDownload={() => { }}
        onManageDownload={async () => { }}
        onManageSeasonPack={async () => { }}
        onRefreshDownloads={async () => { }}
        onFavorite={() => { }}
      />,
      container
    );

    // Switch to Projects route
    const projectsBtn = container.querySelector<HTMLButtonElement>('[data-menu-route="projects"]');
    projectsBtn?.click();
    await new Promise(r => setTimeout(r, 20));

    // Confirm the link is displayed with the exact URL
    const linkCard = container.querySelector<HTMLButtonElement>('[data-focus-key="project-1"]');
    expect(linkCard).not.toBeNull();
    expect(linkCard?.querySelector('code')?.textContent).toBe(targetUrl);

    // Click the link — triggers webOS launch which fails
    linkCard?.click();
    await new Promise(r => setTimeout(r, 40));

    // Refusal requirements:
    // 1. Displayed address remains visible on the card
    expect(linkCard?.querySelector('code')?.textContent).toBe(targetUrl);

    // 2. Refusal message appears in existing message region
    const refusalHint = container.querySelector<HTMLParagraphElement>('.tv-projects .focus-hint');
    expect(refusalHint?.textContent).toBe(`Open this address on another device: ${targetUrl}`);

    // 3. Never shows launch success
    expect(container.textContent).not.toContain('Success');
    expect(container.textContent).not.toContain('Launched');
    expect(container.textContent).not.toContain('Browser opened');
  });

  it('LG exit dispatches window.close exactly once', () => {
    let closes = 0;
    testWindow.close = () => { closes++; };

    exitApplication();
    expect(closes).toBe(1);
  });
});
