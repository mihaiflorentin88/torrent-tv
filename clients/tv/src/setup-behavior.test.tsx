// @vitest-environment happy-dom
import { render } from 'preact';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import type { API, CatalogFacets, HouseholdState, PortalState, UpdateStatus } from '@torrent-tv/shared';
import type { TVPlatformHooks } from './platform';
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

const { Catalog, Setup, TVSettings } = await import('./main');

const testWindow = window as unknown as {
  FileListTVPlatform?: TVPlatformHooks;
};

describe('Setup and link handoff behavioral regressions', () => {
  let container: HTMLDivElement;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
  });

  afterEach(() => {
    render(null, container);
    container.remove();
    delete testWindow.FileListTVPlatform;
  });

  it('does not start scanning early when network acquisition is delayed', async () => {
    let resolveNetwork: ((value: { ip: string; subnetMask: string } | null) => void) | null = null;
    const networkPromise = new Promise<{ ip: string; subnetMask: string } | null>(resolve => {
      resolveNetwork = resolve;
    });

    testWindow.FileListTVPlatform = {
      getNetworkInfo: () => networkPromise,
      openExternal: async () => false,
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };

    render(
      <Setup
        draft="http://192.168.1.100:8097"
        server=""
        status=""
        onDraft={() => { }}
        onConnect={() => { }}
        onForget={() => { }}
      />,
      container
    );

    // Give mount effects time to execute
    await new Promise(r => setTimeout(r, 20));

    // Verify visible UI state: scanning has not started host probing early
    const rescanBtn = container.querySelector<HTMLButtonElement>('[data-focus-key="setup-rescan"]');
    expect(rescanBtn?.textContent).toBe('Searching…');

    const hint = container.querySelector<HTMLParagraphElement>('.focus-hint');
    expect(hint?.textContent).not.toContain('Searching your network…');
    expect(hint?.textContent).toContain('Automatic discovery checks only the television’s local network.');

    const discoveredServers = container.querySelectorAll('.setup-results button');
    expect(discoveredServers.length).toBe(0);

    // Resolve network info as empty to cleanly end the scan
    resolveNetwork!(null);
    await new Promise(r => setTimeout(r, 20));
  });

  it('leaves Manual address usable when network discovery fails or times out', async () => {
    testWindow.FileListTVPlatform = {
      getNetworkInfo: async () => null, // simulated timeout or absent network
      openExternal: async () => false,
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };

    let connectedUrl = '';
    let draftValue = 'http://192.168.1.100:8097';

    render(
      <Setup
        draft={draftValue}
        server=""
        status=""
        onDraft={val => { draftValue = val; }}
        onConnect={url => { connectedUrl = url; }}
        onForget={() => { }}
      />,
      container
    );

    // Wait for scan() catch block to execute
    await new Promise(r => setTimeout(r, 30));

    // Verify exact failure message displayed to user
    const hint = container.querySelector<HTMLParagraphElement>('.focus-hint');
    expect(hint?.textContent).toBe('Automatic discovery is unavailable on this device. Use Manual address.');

    // Verify manual address section is automatically opened and usable
    const manualSection = container.querySelector<HTMLElement>('.setup-manual');
    expect(manualSection).not.toBeNull();

    const manualInput = container.querySelector<HTMLInputElement>('[data-focus-key="setup-address"]');
    expect(manualInput).not.toBeNull();
    expect(manualInput?.value).toBe('http://192.168.1.100:8097');

    // Simulate user connecting with manual address
    const connectButton = container.querySelector<HTMLButtonElement>('[data-focus-key="setup-connect"]');
    expect(connectButton).not.toBeNull();
    connectButton?.click();
    expect(connectedUrl).toBe('http://192.168.1.100:8097');
  });

  it('retains displayed address and never shows launch success on link refusal in Projects', async () => {
    const testUrl = 'https://projects.example.org/tracker';

    testWindow.FileListTVPlatform = {
      getNetworkInfo: async () => null,
      openExternal: async () => false, // Browser refused / unavailable
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };

    const mockPortal: PortalState = {
      accountsEnabled: false,
      adsEnabled: false,
      donor: false,
      links: [
        {
          id: 1,
          title: 'Special Tracker',
          url: testUrl,
          description: 'Community tracker site',
        },
      ],
    };

    const mockApi = {
      titles: async () => ({ items: [], nextCursor: null }),
      ensureMetadata: async () => ({ queued: 0 }),
      streamURL: (p: string) => p,
      call: async () => ({}),
    } as unknown as API;

    render(
      <Catalog
        api={mockApi}
        status="Online"
        titles={[]}
        facets={{} as CatalogFacets}
        household={{ favorites: [], continueWatching: [], recent: [], watched: [] }}
        downloads={[]}
        jobs={[]}
        restoreFocus={null}
        portal={mockPortal}
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
    const projectsMenuBtn = container.querySelector<HTMLButtonElement>('[data-menu-route="projects"]');
    projectsMenuBtn?.click();
    await new Promise(r => setTimeout(r, 20));

    // Confirm the project button is visible with the displayed address
    const projectBtn = container.querySelector<HTMLButtonElement>('[data-focus-key="project-1"]');
    expect(projectBtn).not.toBeNull();
    const codeEl = projectBtn?.querySelector('code');
    expect(codeEl?.textContent).toBe(testUrl);

    // Click the project link (triggers openExternalURL which resolves false)
    projectBtn?.click();
    await new Promise(r => setTimeout(r, 20));

    // Address remains visible on the card
    expect(projectBtn?.querySelector('code')?.textContent).toBe(testUrl);

    // Refusal message appears in existing message region
    const hint = container.querySelector<HTMLParagraphElement>('.tv-projects .focus-hint');
    expect(hint?.textContent).toBe(`Open this address on another device: ${testUrl}`);

    // Verify no "success" or "opened" text anywhere
    expect(container.textContent).not.toContain('Browser opened');
    expect(container.textContent).not.toContain('Success');
  });

  it('retains displayed address on release link click refusal in TVSettings', async () => {
    const releaseUrl = 'https://github.com/example/releases/v1.2.3';

    testWindow.FileListTVPlatform = {
      getNetworkInfo: async () => null,
      openExternal: async () => false, // refused
      exit: () => { },
      onKeyboardVisibility: () => () => { },
      onVisibility: () => () => { },
    };

    const mockUpdateStatus: UpdateStatus = {
      currentVersion: '1.2.2',
      latest: '1.2.3',
      available: true,
      releasesUrl: releaseUrl,
      selfUpdate: false,
      applying: false,
    };

    const mockApi = {
      call: async () => ({ items: [] }),
    } as unknown as API;

    render(
      <TVSettings
        api={mockApi}
        onChangeServer={() => { }}
        onForgetServer={() => { }}
        updateStatus={mockUpdateStatus}
        onUpdateStatus={() => { }}
        confirmOpen={false}
        onConfirmOpen={() => { }}
        onConfirmClose={() => { }}
      />,
      container
    );

    await new Promise(r => setTimeout(r, 20));

    const link = container.querySelector<HTMLAnchorElement>('.tv-update-notice a');
    expect(link).not.toBeNull();
    expect(link?.textContent).toBe(releaseUrl);

    link?.click();
    await new Promise(r => setTimeout(r, 20));

    // Address remains visible in the link
    expect(link?.textContent).toBe(releaseUrl);

    // Refusal message appears in TVSettings message region
    const updateMsg = container.querySelector<HTMLParagraphElement>('.tv-settings p[aria-live="polite"]');
    expect(updateMsg?.textContent).toBe(`Open this address on another device: ${releaseUrl}`);
  });
});
