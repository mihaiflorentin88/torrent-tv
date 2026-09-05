import { useEffect, useState } from 'preact/hooks';
import { Download, DownloadTransferAction } from '@torrent-tv/shared';
import { Downloads, captureDownloadAnchor, reconcileDownloads, restoreDownloadAnchor } from '@torrent-tv/web/downloads';
import { sharedApi } from '@torrent-tv/web/shared-api';
import { OpenURL } from '../bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings';
import { useServerState } from '../lib/state';

// watchURL builds the web player's deep link for a download: Play in the
// desktop hands off to the browser (spec: playback stays on the surfaces
// built for it), which resolves resume position and sources itself.
function watchURL(address: string | undefined, id: string): string {
  return `http://${address || '127.0.0.1:8097'}/watch/${encodeURIComponent(id)}`;
}

// Downloads over the shared web view: this page owns only the data loop
// (poll, reconcile, scroll anchor) and the not-running gate; toolbar,
// cards, and the removal confirm come from @torrent-tv/web/downloads.
export function DownloadsPage() {
  const server = useServerState();
  const [items, setItems] = useState<Download[]>([]);
  useEffect(() => {
    if (server.state !== 'running') return;
    let stopped = false;
    const load = async () => {
      try {
        const incoming = (await sharedApi().downloads()).items;
        if (stopped) return;
        const anchor = captureDownloadAnchor();
        setItems(current => reconcileDownloads(current, incoming));
        // Restore after Preact commits the reconciled list so visible rows
        // hold their viewport position across polls.
        requestAnimationFrame(() => restoreDownloadAnchor(anchor));
      } catch { /* keep the last good list; an empty list shows the view's own empty box */ }
    };
    void load();
    const timer = setInterval(load, 3000);
    return () => { stopped = true; clearInterval(timer) };
  }, [server.state]);
  if (server.state !== 'running') {
    return <section class="empty-state"><h2>Server is {server.state}</h2><p>Start the server to see downloads.</p></section>;
  }
  return <Downloads
    items={items}
    onRefresh={() => { }}
    onPlay={d => { void OpenURL(watchURL(server.address, d.id)).catch(() => { }) }}
    onRemove={async download => { await sharedApi().deleteDownload(download.id) }}
    onAction={async (download, action: DownloadTransferAction) => { await sharedApi().call(`/downloads/${encodeURIComponent(download.id)}/${action}`, { method: 'POST' }) }}
  />;
}
