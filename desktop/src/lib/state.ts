import { useEffect, useState } from 'preact/hooks';
import { Events } from '@wailsio/runtime';
import { configureSharedApi, sharedApi } from '@torrent-tv/web/shared-api';
import {
  PortalState,
  PortalSync,
  PortalUser,
  UpdateStatus,
  clearPortalSession,
  eventPayload,
  loadPortalSession,
  type PortalSessionStorage,
} from '@torrent-tv/shared';
import { OpenURL } from '../bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings';

export type ServerState = 'stopped' | 'starting' | 'running' | 'stopping' | 'failed';

// StateEvent mirrors the Go gui.StateEvent the Wails runner (Task 6) emits
// on the 'server:state' topic.
export type StateEvent = { state: ServerState; error?: string; address?: string };

const IS_SERVER_STATE: Record<ServerState, true> = { stopped: true, starting: true, running: true, stopping: true, failed: true };

// The runner marshals the Go event payload to JSON over the Wails bridge,
// so it is validated once at this boundary before any view consumes it.
export function isStateEvent(value: unknown): value is StateEvent {
  if (typeof value !== 'object' || value === null || !('state' in value)) return false;
  const { state } = value;
  return typeof state === 'string' && state in IS_SERVER_STATE;
}

// Boot-time wiring for the shared components' API origin. The desktop's
// main.tsx points it at the app origin (proxied same-origin) before the
// first render; the web client keeps location.origin. Re-pointing the
// origin (e.g. the embedded server moving behind a new address) re-routes
// every portal consumer: the previous origin's subscriptions are torn down
// and rebuilt, and its stored session is dropped — a token issued by one
// server must never survive a switch to another.
let activeOrigin = location.origin;
const originListeners = new Set<(origin: string, previous: string) => void>();

export function setServerOrigin(origin: string): void {
  configureSharedApi(origin);
  if (origin === activeOrigin) return;
  const previous = activeOrigin;
  activeOrigin = origin;
  if (portalStarted) {
    clearPortalSession(sessionStore(), previous);
    startPortal(origin);
    notifyPortal();
  }
  for (const listener of [...originListeners]) listener(origin, previous);
}

// The origin the shared client and the portal stream currently target.
export function sharedOrigin(): string { return activeOrigin }

export function onServerOriginChange(listener: (origin: string, previous: string) => void): () => void {
  originListeners.add(listener);
  return () => { originListeners.delete(listener) };
}

// Portal and self-update surface shared by the shell and the pages. One
// PortalSync and one SSE stream serve the whole app (module-level, started
// on first usePortal subscriber), mirroring the web app's single sync: the
// stream delivers portal.state / updates.status / updates.failed events,
// reconnection replays are absorbed only outside the recovery drop window,
// and every origin rebuild replaces the stream outright so a restarted
// server can never leave a stale subscription behind.
export type PortalSurface = {
  snapshot: PortalState | null;
  status: UpdateStatus | null;
  connected: boolean;
  failure: string;
  identity: PortalUser | null;
};

let portalStarted = false;
let portalOrigin = '';
let portalSync: PortalSync | null = null;
let portalStream: EventSource | null = null;
let identityValue: PortalUser | null = null;
const portalListeners = new Set<() => void>();

function notifyPortal(): void {
  for (const listener of [...portalListeners]) listener();
}

// localStorage can throw or be missing in odd embeds; the portal then runs
// with no stored session instead of crashing the shell.
export function sessionStore(): PortalSessionStorage {
  try {
    if (localStorage) return localStorage;
  } catch { /* blocked storage: no persistence */ }
  return { getItem: () => null, setItem: () => { }, removeItem: () => { } };
}

// A stored token must prove itself against /session/me before it becomes
// identity; only an invalid session (401) clears it, matching web: a plain
// network failure (server restarting, outage) keeps the token for the next
// boot. A late answer from a replaced origin is discarded.
// call() attaches the numeric HTTP status to thrown fetch failures.
function failureStatus(error: unknown): number | undefined {
  if (typeof error !== 'object' || error === null || !('status' in error)) return undefined;
  const status: unknown = (error as { status: unknown }).status; // shape guarded by 'in' above
  return typeof status === 'number' ? status : undefined;
}

function revalidateIdentity(origin: string): void {
  const stored = loadPortalSession(sessionStore(), origin);
  if (!stored) {
    identityValue = null;
    notifyPortal();
    return;
  }
  const controller = new AbortController();
  sharedApi().portalMe(stored.token, controller.signal).then(user => {
    if (portalOrigin !== origin) return;
    identityValue = user;
    notifyPortal();
  }).catch((error: unknown) => {
    if (failureStatus(error) === 401) clearPortalSession(sessionStore(), origin);
    if (portalOrigin !== origin) return;
    identityValue = null;
    notifyPortal();
  });
}

function startPortal(origin: string): void {
  stopPortal();
  portalOrigin = origin;
  const api = sharedApi();
  // Deferred property reads: a stubbed client without portal methods
  // rejects inside the promise (allSettled swallows it) instead of throwing
  // during stream setup.
  const sync = new PortalSync({
    loadState: () => Promise.resolve().then(() => api.portalState()),
    loadStatus: () => Promise.resolve().then(() => api.updatesCurrent()),
  });
  portalSync = sync;
  sync.subscribe(notifyPortal);
  void sync.recover();
  let lastEventId = 0;
  // The configured origin IS the stream base (the webview reaches the
  // server through the same-origin proxy), so the URL is built from the
  // origin this engine instance was started for — never from a stale base.
  const stream = new EventSource(`${origin}/api/v1/events`);
  portalStream = stream;
  const portalEvent = (event: MessageEvent) => {
    const parsed = eventPayload(event.data);
    if (!parsed) return;
    lastEventId = Number(event.lastEventId) > 0 ? Number(event.lastEventId) : lastEventId;
    sync.absorb({ id: parsed.id, kind: parsed.kind, payload: parsed.payload });
  };
  stream.addEventListener('portal.state', portalEvent as EventListener);
  stream.addEventListener('updates.status', portalEvent as EventListener);
  stream.addEventListener('updates.failed', portalEvent as EventListener);
  stream.addEventListener('open', () => {
    // Reconnect (server restart, sleep/wake, proxy 503 recovery): the
    // recovery refetch is the freshest write; replayed events older than
    // the last seen id cannot override it.
    void sync.recover(lastEventId > 0 ? lastEventId : undefined);
  });
  stream.addEventListener('error', () => sync.disconnect());
  revalidateIdentity(origin);
}

export function stopPortal(): void {
  portalStream?.close();
  portalStream = null;
  portalSync = null;
  portalOrigin = '';
  identityValue = null;
}

function ensurePortal(): void {
  if (portalStarted) return;
  portalStarted = true;
  startPortal(activeOrigin);
}

function portalSurface(): PortalSurface {
  return {
    snapshot: portalSync?.state ?? null,
    status: portalSync?.status ?? null,
    connected: portalSync?.connected ?? true,
    failure: portalSync?.failure ?? '',
    identity: identityValue,
  };
}

export function usePortal(): PortalSurface {
  const [surface, setSurface] = useState<PortalSurface>(portalSurface);
  useEffect(() => {
    ensurePortal();
    const render = () => setSurface(portalSurface());
    portalListeners.add(render);
    render();
    return () => { portalListeners.delete(render) };
  }, []);
  return surface;
}

// The account dialog's sign-in/out lands here so every consumer sees one
// identity.
export function setPortalIdentity(user: PortalUser | null): void {
  identityValue = user;
  notifyPortal();
}

// The update controller's check/apply responses feed the same store the
// SSE events do — one source of truth, like the web app's status state.
export function pushUpdateStatus(status: UpdateStatus): void {
  portalSync?.absorb({ kind: 'updates.status', payload: status });
}

// External links open through the native binding (http(s)-validated on the
// Go side); failures keep the same quiet convention as the Downloads Play
// handoff.
export function openExternal(url: string): void {
  void OpenURL(url).catch(() => { });
}

// Test seam: tear the module-level engine down so each test starts cold.
export function resetPortal(): void {
  stopPortal();
  portalStarted = false;
  activeOrigin = location.origin;
  notifyPortal();
}

let seeded: StateEvent = { state: 'stopped' };

// Called once at boot, before the first render, when the Go side hands over
// its current lifecycle state instead of waiting for the next emit. The
// subscription below also keeps it current so components mounted after an
// emit (view switches) initialize from the latest state, not the boot seed.
export function seedServerState(event: StateEvent) { seeded = event }

export function useServerState(): StateEvent {
  const [state, setState] = useState<StateEvent>(seeded);
  useEffect(() => {
    const off = Events.On('server:state', event => {
      if (isStateEvent(event.data)) {
        seeded = event.data;
        setState(event.data);
      }
    });
    return off;
  }, []);
  return state;
}
