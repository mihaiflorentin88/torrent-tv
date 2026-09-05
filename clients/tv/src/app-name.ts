// The platform shell injects the client's display identity before the bundle
// loads (Tizen leaves it unset and gets the default). UI code only ever reads
// this — never hardcodes a brand string (spec: Parity contract, named
// exceptions).
export interface AppIdentity { name: string; monogram: string }

declare global {
  interface Window {
    FileListTVIdentity?: { name?: string; monogram?: string };
  }
}

export function appIdentity(): AppIdentity {
  const injected = window.FileListTVIdentity;
  if (!injected || typeof injected.name !== 'string' || !injected.name.trim()) return { name: 'Torrent TV', monogram: 'TT' };
  const name = injected.name.trim();
  if (typeof injected.monogram === 'string' && injected.monogram.trim()) return { name, monogram: injected.monogram.trim() };
  const words = name.split(/\s+/);
  const monogram = (words.length > 1 ? words.map(word => word[0] || '').join('') : name.slice(0, 2)).slice(0, 2).toUpperCase();
  return { name, monogram };
}
