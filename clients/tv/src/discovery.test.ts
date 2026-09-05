import { afterEach, describe, expect, it, vi } from 'vitest';
import { discoveryHosts, discoverServers, normalizeServerURL } from './discovery';

describe('server discovery', () => {
  it('normalizes manual addresses and preserves custom ports', () => {
    expect(normalizeServerURL('192.168.1.50:8097/')).toBe('http://192.168.1.50:8097');
    expect(normalizeServerURL('https://media.lan:9443/path/')).toBe('https://media.lan:9443/path');
  });

  it('scans only the usable local subnet and omits the television', () => {
    expect(discoveryHosts('192.168.1.2', '255.255.255.252')).toEqual(['192.168.1.1']);
  });

  it('bounds a large LAN to the television local /24', () => {
    const hosts = discoveryHosts('10.2.3.77', '255.0.0.0');
    expect(hosts).toHaveLength(253);
    expect(hosts).toContain('10.2.3.1');
    expect(hosts).not.toContain('10.2.3.77');
    expect(hosts).not.toContain('10.2.4.1');
  });
});

describe('server discovery without AbortController', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('resolves the probe to null within the per-host budget when fetch hangs', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn((_url: string, init?: RequestInit) => new Promise<Response>(() => { }));
    vi.stubGlobal('fetch', fetchMock);
    let settled = false;
    const pending = discoverServers('192.168.1.2', '255.255.255.252', [8097], undefined, { supportsAbortController: false }).then(() => {
      settled = true;
    });
    await vi.advanceTimersByTimeAsync(899);
    expect(settled).toBe(false);
    expect(fetchMock).toHaveBeenCalledWith('http://192.168.1.1:8097/api/v1/system/info', { cache: 'no-store' });
    await vi.advanceTimersByTimeAsync(1);
    await pending;
    expect(settled).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('does not leak an unhandled rejection when fetch fails after the budget', async () => {
    vi.useFakeTimers();
    let rejectFetch!: (reason?: unknown) => void;
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((_resolve, reject) => {
      rejectFetch = reject;
    })));
    const pending = discoverServers('192.168.1.2', '255.255.255.252', [8097], undefined, { supportsAbortController: false });
    await vi.advanceTimersByTimeAsync(900);
    expect(await pending).toEqual([]);
    rejectFetch(new Error('late failure'));
    await vi.advanceTimersByTimeAsync(0);
  });

  it('clears the timeout timer and reports the server when the legacy probe succeeds early', async () => {
    vi.useFakeTimers();
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ name: 'Torrent TV', version: '0.1.0', configured: true }) })));
    const pending = discoverServers('192.168.1.2', '255.255.255.252', [8097], undefined, { supportsAbortController: false });
    await vi.advanceTimersByTimeAsync(0);
    const servers = await pending;
    expect(servers.map(server => server.url)).toEqual(['http://192.168.1.1:8097']);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('resolves to null within the budget when the body read stalls after headers', async () => {
    vi.useFakeTimers();
    // new Promise executor form: clients/tizen lib is ES2020 (engine floor, ADR-0006), so no Promise.withResolvers here
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: () => new Promise<never>(() => { }) })));
    const pending = discoverServers('192.168.1.2', '255.255.255.252', [8097], undefined, { supportsAbortController: false });
    await vi.advanceTimersByTimeAsync(899);
    expect(await Promise.race([pending, Promise.resolve('pending')])).toBe('pending');
    await vi.advanceTimersByTimeAsync(1);
    expect(await pending).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe('server discovery progress reporting', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('reports completed counts after each host probe', async () => {
    const progress: Array<[number, number]> = [];
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, json: async () => ({ name: 'Torrent TV', version: '0.1.0', configured: true }) })));
    const servers = await discoverServers('192.168.1.2', '255.255.255.252', [8097, 8098], (completed, total) => progress.push([completed, total]), { supportsAbortController: true });
    expect(progress).toEqual([[1, 2], [2, 2]]);
    expect(servers).toHaveLength(2);
  });
});

describe('server discovery with AbortController', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('calls fetch with a signal and no-store cache exactly as before', async () => {
    const inits: Array<RequestInit | undefined> = [];
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      inits.push(init);
      return { ok: true, json: async () => ({ name: 'Torrent TV', version: '0.1.0', configured: true }) };
    }));
    const servers = await discoverServers('192.168.1.2', '255.255.255.252', [8097], undefined, { supportsAbortController: true });
    expect(servers.map(server => server.url)).toEqual(['http://192.168.1.1:8097']);
    expect(inits).toEqual([{ signal: expect.anything(), cache: 'no-store' }]);
  });

  it('aborts the in-flight request at the per-host budget', async () => {
    vi.useFakeTimers();
    const signals: Array<AbortSignal | null | undefined> = [];
    vi.stubGlobal('fetch', vi.fn((_url: string, init?: RequestInit) => {
      signals.push(init?.signal);
      return new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new Error('aborted')));
      });
    }));
    const pending = discoverServers('192.168.1.2', '255.255.255.252', [8097], undefined, { supportsAbortController: true });
    await vi.advanceTimersByTimeAsync(899);
    expect(signals[0]).toBeInstanceOf(AbortSignal);
    expect(signals[0]?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(signals[0]?.aborted).toBe(true);
    expect(await pending).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('defaults to runtime capability detection when no presence map is injected', async () => {
    const inits: Array<RequestInit | undefined> = [];
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => {
      inits.push(init);
      return { ok: true, json: async () => ({ name: 'Torrent TV', version: '0.1.0', configured: true }) };
    }));
    const servers = await discoverServers('192.168.1.2', '255.255.255.252');
    expect(servers.map(server => server.url)).toEqual(['http://192.168.1.1:8097']);
    expect(inits[0]?.signal).toBeDefined();
  });
});
