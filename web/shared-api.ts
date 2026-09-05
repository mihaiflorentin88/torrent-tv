import { API } from '@torrent-tv/shared';
// Shared components must not hardcode location.origin (spec: Reuse
// boundary): the desktop app points them at the loopback server.
let api = new API(location.origin);
export function configureSharedApi(origin: string): void { api = new API(origin); }
export function sharedApi(): API { return api; }
