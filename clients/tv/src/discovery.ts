import { Capabilities, detectCapabilities } from './capability';

export interface ServerInfo {
  name: string;
  instanceName?: string;
  version: string;
  apiVersion?: string;
  configured: boolean;
  capabilities?: string[];
}

export interface DiscoveredServer {
  url: string;
  info: ServerInfo;
}

export function normalizeServerURL(value: string): string {
  const raw = value.trim();
  if (!raw) throw new Error('Enter a server address.');
  const candidate = /^[a-z][a-z0-9+.-]*:\/\//i.test(raw) ? raw : `http://${raw}`;
  const parsed = new URL(candidate);
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') throw new Error('The server address must use HTTP or HTTPS.');
  parsed.pathname = parsed.pathname.replace(/\/+$/, '');
  parsed.search = '';
  parsed.hash = '';
  return parsed.toString().replace(/\/$/, '');
}

function ipv4(value: string): number | null {
  const parts = value.trim().split('.');
  if (parts.length !== 4) return null;
  const bytes = parts.map(Number);
  if (bytes.some(part => !Number.isInteger(part) || part < 0 || part > 255)) return null;
  return (((bytes[0] << 24) >>> 0) + (bytes[1] << 16) + (bytes[2] << 8) + bytes[3]) >>> 0;
}

function formatIPv4(value: number): string {
  return [value >>> 24, value >>> 16 & 255, value >>> 8 & 255, value & 255].join('.');
}

export function discoveryHosts(address: string, subnetMask: string): string[] {
  const local = ipv4(address);
  const mask = ipv4(subnetMask);
  if (local === null || mask === null) return [];
  let network = (local & mask) >>> 0;
  let broadcast = (network | (~mask >>> 0)) >>> 0;
  if (broadcast - network - 1 > 254) {
    network = (local & 0xffffff00) >>> 0;
    broadcast = (network | 255) >>> 0;
  }
  const hosts: string[] = [];
  for (let current = network + 1; current < broadcast && hosts.length < 254; current++) {
    if ((current >>> 0) !== local) hosts.push(formatIPv4(current >>> 0));
  }
  return hosts;
}

type ProbeTimer = ReturnType<typeof setTimeout>;

async function probeWithoutAbortController(url: string, timeoutMs: number): Promise<DiscoveredServer | null> {
  // Executor form is deliberate: Promise.withResolvers does not exist on the Tizen 5.0 engine floor (ADR-0006).
  let timer: ProbeTimer | undefined;
  try {
    return await Promise.race([
      (async () => {
        const response = await fetch(`${url}/api/v1/system/info`, { cache: 'no-store' });
        if (!response.ok) return null;
        const info = await response.json() as ServerInfo;
        if (info.name !== 'FileList Streaming' || !info.version) return null;
        return { url, info };
      })().catch(() => null),
      new Promise<null>(resolve => {
        timer = setTimeout(() => resolve(null), timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

async function probe(url: string, timeoutMs: number, capabilities: Capabilities): Promise<DiscoveredServer | null> {
  if (!capabilities.supportsAbortController) return probeWithoutAbortController(url, timeoutMs);
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(`${url}/api/v1/system/info`, { signal: controller.signal, cache: 'no-store' });
    if (!response.ok) return null;
    const info = await response.json() as ServerInfo;
    if (info.name !== 'FileList Streaming' || !info.version) return null;
    return { url, info };
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

export async function discoverServers(address: string, subnetMask: string, requestedPorts: number[] = [8097], onProgress?: (completed: number, total: number) => void, capabilities: Capabilities = detectCapabilities()): Promise<DiscoveredServer[]> {
  const hosts = discoveryHosts(address, subnetMask);
  const ports = Array.from(new Set(requestedPorts.filter(port => Number.isInteger(port) && port > 0 && port <= 65535)));
  const targets = hosts.reduce<string[]>((all, host) => all.concat(ports.map(port => `http://${host}:${port}`)), []);
  const results: DiscoveredServer[] = [];
  let cursor = 0;
  let completed = 0;
  const worker = async () => {
    while (cursor < targets.length) {
      const target = targets[cursor++];
      const result = await probe(target, 900, capabilities);
      completed++;
      onProgress?.(completed, targets.length);
      if (result && !results.some(item => item.url === result.url)) results.push(result);
    }
  };
  await Promise.all(Array.from({ length: Math.min(32, targets.length) }, worker));
  return results.sort((a, b) => (a.info.instanceName || a.info.name).localeCompare(b.info.instanceName || b.info.name) || a.url.localeCompare(b.url));
}
