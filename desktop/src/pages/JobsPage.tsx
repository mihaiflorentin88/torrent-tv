import { useState } from 'preact/hooks';
import { Jobs } from '@torrent-tv/web/jobs';
import { useServerState } from '../lib/state';

// Jobs over the shared self-fetching view. The open/close detail callbacks
// exist for the web client's deep links; the desktop shell has no URL bar,
// so they stay no-ops, and load failures surface in an inline alert.
export function JobsPage() {
  const server = useServerState();
  const [error, setError] = useState('');
  if (server.state !== 'running') {
    return <section class="empty-state"><h2>Server is {server.state}</h2><p>Start the server to see jobs.</p></section>;
  }
  return (
    <>
      {error && <p class="error" role="alert"><span>{error}</span></p>}
      <Jobs onError={setError} onOpenDetail={() => { }} onCloseDetail={() => { }} />
    </>
  );
}
