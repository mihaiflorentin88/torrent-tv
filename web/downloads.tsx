// The Downloads view: search/filter/sort toolbar, per-download transfer
// cards, and the removal confirm. Extracted verbatim from src.tsx so the
// desktop GUI can mount it; data flows in through props and action
// callbacks, so the component needs no API client of its own. The modal
// focus machinery below is duplicated from src.tsx (pending a shared
// module) because the removal confirm owns an Escape-closable overlay.
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { Download, DownloadSort, downloadTransferActions, DownloadTransferAction, formatBytes, orderDownloadIDs } from '@torrent-tv/shared';
import { Icon } from './icons';

// Canonical reconcileDownloads lives in @torrent-tv/shared; re-exported here
// so desktop consumers resolve it from this module alongside the view.
export { reconcileDownloads } from '@torrent-tv/shared';

type ViewportAnchor = { id: string; top: number };
export function captureDownloadAnchor(): ViewportAnchor | null { const rows = Array.from(document.querySelectorAll<HTMLElement>('[data-download-id]')); const row = rows.find(item => item.getBoundingClientRect().bottom > 0); return row ? { id: row.dataset.downloadId || '', top: row.getBoundingClientRect().top } : null }
export function restoreDownloadAnchor(anchor: ViewportAnchor | null) { if (!anchor) return; const row = document.querySelector<HTMLElement>(`[data-download-id="${CSS.escape(anchor.id)}"]`); if (!row) return; const delta = row.getBoundingClientRect().top - anchor.top; if (Math.abs(delta) > 0.5) window.scrollBy(0, delta) }

// Shared modal focus machinery: inert the page background, move focus into
// the surface, Tab-cycle inside it, and restore focus on teardown. Escape is
// an explicit per-caller policy: surfaces that own Escape pass onEscape
// (preventDefault, then invoke); surfaces whose Escape belongs to another
// chain omit it and get a Tab-only trap.
function trapFocus(surface: () => HTMLElement | null, focusableSelector: string, background: (surface: HTMLElement | null) => HTMLElement[], onEscape?: () => void): () => void {
  const previous = document.activeElement as HTMLElement | null;
  const root = surface();
  const inertElements = background(root);
  inertElements.forEach(element => element.setAttribute('inert', ''));
  const focusable = () => Array.from(surface()?.querySelectorAll<HTMLElement>(focusableSelector) || []);
  const timer = window.setTimeout(() => focusable()[0]?.focus(), 0);
  const key = (event: KeyboardEvent) => {
    if (event.key === 'Escape') { if (!onEscape) return; event.preventDefault(); onEscape(); return }
    if (event.key !== 'Tab') return;
    const items = focusable();
    if (!items.length) return;
    const index = items.indexOf(document.activeElement as HTMLElement);
    event.preventDefault();
    items[(index + (event.shiftKey ? -1 : 1) + items.length) % items.length].focus();
  };
  document.addEventListener('keydown', key);
  return () => { window.clearTimeout(timer); document.removeEventListener('keydown', key); inertElements.forEach(element => element.removeAttribute('inert')); previous?.focus() };
}

function useOverlayFocus(active: boolean, onClose: () => void) {
  // Escape runs the latest onClose: consumers capture state (the Downloads
  // removal confirm stays open while a removal is in flight), so the listener
  // registered when the overlay opened must not go stale.
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!active) return;
    // The trap follows the topmost overlay: nested overlays (detail → source
    // picker) cycle within the one actually on top.
    const surface = () => { const overlays = document.querySelectorAll<HTMLElement>('.overlay'); return overlays[overlays.length - 1] || null };
    return trapFocus(surface, 'button:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])', overlay => Array.from(document.querySelectorAll<HTMLElement>('.sidebar,.content')).filter(element => !overlay || !element.contains(overlay)), () => closeRef.current());
  }, [active]);
}

export function Downloads({ items, onRefresh, onPlay, onRemove, onAction }: { items: Download[]; onRefresh: () => void; onPlay: (d: Download) => void; onRemove: (d: Download) => Promise<void>; onAction: (d: Download, action: DownloadTransferAction) => Promise<void> }) {
  const [pending, setPending] = useState<Download | null>(null); const [removing, setRemoving] = useState(false); const [query, setQuery] = useState(''); const [filter, setFilter] = useState('all'); const [sort, setSort] = useState<DownloadSort>('recent'); const [order, setOrder] = useState<string[]>(() => orderDownloadIDs(items, 'recent')); useOverlayFocus(Boolean(pending), () => { if (!removing) setPending(null) });
  const [busy, setBusy] = useState(''); const runAction = async (download: Download, action: DownloadTransferAction) => { const key = `${download.id}:${action}`; if (busy) return; setBusy(key); try { await onAction(download, action) } catch { } finally { setBusy('') } };
  const idsKey = items.map(item => item.id).join('\u0000');
  useEffect(() => setOrder(current => { const available = new Set(items.map(item => item.id)); const retained = current.filter(id => available.has(id)); const known = new Set(retained); const added = items.filter(item => !known.has(item.id)).map(item => item.id); return [...added, ...retained] }), [idsKey]);
  const changeSort = (next: DownloadSort) => { setSort(next); setOrder(orderDownloadIDs(items, next)) };
  const facts = (download: Download) => [download.parsed?.resolution, download.parsed?.quality, download.parsed?.videoCodec, download.parsed?.audio, download.category].filter(Boolean).join(' · ');
  const visible = useMemo(() => { const term = query.trim().toLocaleLowerCase(); const byID = new Map(items.map(item => [item.id, item])); const ordered = (order.length ? order : items.map(item => item.id)).map(id => byID.get(id)).filter((item): item is Download => Boolean(item)); return ordered.filter(download => { const text = [download.displayTitle, download.releaseName, download.filePath, download.category, download.state].filter(Boolean).join(' ').toLocaleLowerCase(); const matchesFilter = filter === 'all' || filter === 'streaming' && download.playbackMode === 'progressive' || filter === 'complete' && download.playbackMode === 'local' || filter === 'paused' && /^(paused|stopped)/.test(download.state.toLocaleLowerCase()) || filter === 'errors' && Boolean(download.error); return (!term || text.includes(term)) && matchesFilter }) }, [items, order, query, filter]);
  const confirmRemoval = async () => { if (!pending || removing) return; setRemoving(true); try { await onRemove(pending); setPending(null) } finally { setRemoving(false) } };
  return <section><div class="catalog-tools download-tools"><label class="search"><Icon name="search" /><input value={query} onInput={event => setQuery(event.currentTarget.value)} placeholder="Search downloaded titles, releases, or files" aria-label="Search downloads" /></label><select value={filter} onChange={event => setFilter(event.currentTarget.value)} aria-label="Filter downloads"><option value="all">All downloads</option><option value="streaming">Still downloading</option><option value="complete">Downloaded</option><option value="paused">Paused</option><option value="errors">Needs attention</option></select><select value={sort} onChange={event => changeSort(event.currentTarget.value as DownloadSort)} aria-label="Sort downloads"><option value="recent">Recently added</option><option value="title">Title A–Z</option><option value="progress">Most progress</option><option value="size">Largest file</option><option value="speed">Fastest download</option></select><button onClick={onRefresh}>Refresh</button><span class="search-scope" aria-live="polite">{visible.length} of {items.length} downloads shown</span></div><div class="download-list">{visible.length === 0 ? <p class="empty">{items.length === 0 ? 'Downloads you start will appear here.' : 'No downloads match this search and filter.'}</p> : visible.map(download => <article key={download.id} data-download-id={download.id}><div class="download-identity"><h2 title={download.displayTitle || download.filePath}>{download.displayTitle || download.filePath}</h2><p class="release-name" title={download.releaseName || download.filePath}>{download.releaseName || download.filePath}</p><span class={`stream-mode ${download.playbackMode}`}>{download.playbackMode === 'progressive' ? 'Progressive stream' : 'Downloaded file'}</span><dl><div><dt>Selected file</dt><dd title={download.filePath}>{download.filePath}</dd></div><div><dt>Source</dt><dd>{facts(download) || 'Source details unavailable'}</dd></div><div><dt>Selected file size</dt><dd>{formatBytes(download.sizeBytes)} · index {download.fileIndex}</dd></div><div><dt>Complete torrent</dt><dd>{download.releaseId} · {download.trackerSeeders ?? '—'} tracker seeders{download.releaseSizeBytes ? ` · ${formatBytes(download.releaseSizeBytes)} total` : ''}</dd></div></dl><p class="download-telemetry">{download.state} · {(download.progress * 100).toFixed(1)}% · {formatBytes(download.downloadedBytes)} / {formatBytes(download.sizeBytes)} selected</p><progress value={download.progress} max="1" /><p class="download-telemetry"><span class={`engine-tag ${download.engineId.startsWith('native:') ? 'native' : 'qbittorrent'}`}>{download.engineId.startsWith('native:') ? 'native engine' : 'qBittorrent'}</span> · {formatBytes(download.speedBytesPerSecond)}/s · {download.seeds} connected seeds · {download.peers} peers · {download.seeds + download.peers} known in swarm · {download.uploadSpeedBytesPerSecond ? `↑ ${formatBytes(download.uploadSpeedBytesPerSecond)}/s` : '↑ idle'} · {download.etaSeconds > 0 ? `ETA ${Math.max(1, Math.round(download.etaSeconds / 60))} min` : 'ETA —'}</p><p class={`download-error ${download.error ? 'visible' : ''}`} aria-live="polite">{download.error}</p></div><div class="download-actions"><button class="primary" onClick={() => onPlay(download)}>Play</button>{downloadTransferActions(download).map(item => <button key={item.action} disabled={busy.startsWith(download.id + ':')} onClick={() => void runAction(download, item.action)}>{busy === download.id + ':' + item.action ? item.pendingLabel : item.label}</button>)}<button class="danger-button" onClick={() => setPending(download)}>Delete download</button></div></article>)}</div>{pending && <div class="overlay" role="dialog" aria-modal="true" aria-labelledby="web-download-delete-heading"><section class="removal-confirm"><h2 id="web-download-delete-heading">Delete download?</h2><p class="release-name">{pending.releaseName || pending.filePath}</p><dl><div><dt>Selected file</dt><dd>{pending.filePath}</dd></div><div><dt>Tracker release ID</dt><dd>{pending.releaseId}</dd></div><div><dt>Selected file size</dt><dd>{formatBytes(pending.sizeBytes)}</dd></div></dl><p>This removes the torrent from qBittorrent and permanently deletes its incomplete and downloaded files.</p><div class="confirm-actions"><button disabled={removing} onClick={() => setPending(null)}>Cancel</button><button class="danger-button" disabled={removing} onClick={() => void confirmRemoval()}>{removing ? 'Deleting…' : 'Delete download'}</button></div></section></div>}</section>
}
