import { useState } from 'preact/hooks';
import { PortalAccountDialog, PortalSidebarDock, UpdateNotice, useUpdateController, type UpdateController } from '@torrent-tv/web/portal';
import { sharedApi } from '@torrent-tv/web/shared-api';
import { openExternal, pushUpdateStatus, sessionStore, setPortalIdentity, sharedOrigin, usePortal, useServerState } from './lib/state';
import { DownloadsPage } from './pages/DownloadsPage';
import { JobsPage } from './pages/JobsPage';
import { ServerPage } from './pages/ServerPage';
import { SettingsPage } from './pages/SettingsPage';
import './shell.css';
import '@torrent-tv/web/style.css';

// Task 10 appended Server and Settings: one View member, one sections
// entry, and one render line each.
type View = 'downloads' | 'jobs' | 'server' | 'settings';
const sections: { id: View; label: string }[] = [
  { id: 'server', label: 'Server' },
  { id: 'downloads', label: 'Downloads' },
  { id: 'jobs', label: 'Jobs' },
  { id: 'settings', label: 'Settings' },
];

export function App() {
  const [view, setView] = useState<View>('server');
  const server = useServerState();
  const portal = usePortal();
  const [accountOpen, setAccountOpen] = useState(false);
  // One controller serves every update surface (notice, Server page,
  // Settings section) so the confirm dialog phase cannot disagree with a
  // trigger in another view — the same shape as the web app.
  const updates: UpdateController = useUpdateController({ client: sharedApi(), status: portal.status, onStatus: pushUpdateStatus });
  const serverRunning = server.state === 'running';
  return (
    <div class="shell">
      <nav class="shell-nav" aria-label="Sections">
        {sections.map(s => (
          <button key={s.id} class={view === s.id ? 'active' : ''} onClick={() => setView(s.id)}>
            <span class={`dot dot-${server.state}`} aria-hidden="true" />
            {s.label}
          </button>
        ))}
        <PortalSidebarDock snapshot={portal.snapshot} client={sharedApi()} identity={portal.identity} onOpenAccount={() => setAccountOpen(true)} openExternal={openExternal} />
      </nav>
      <div class="shell-main">
        <header class="shell-header">
          <h1>Torrent TV</h1>
          <span class={`pill pill-${server.state}`}>
            <span class={`dot dot-${server.state}`} aria-hidden="true" />
            {server.state === 'running' ? `Running${server.address ? ` · ${server.address}` : ''}`
              : server.state === 'failed' ? 'Failed' : server.state[0].toUpperCase() + server.state.slice(1)}
          </span>
        </header>
        <main>
          {serverRunning && portal.status?.available && (
            <UpdateNotice status={portal.status} controller={updates} openExternal={openExternal} />
          )}
          {view === 'downloads' && <DownloadsPage />}
          {view === 'jobs' && <JobsPage />}
          {view === 'server' && <ServerPage />}
          {view === 'settings' && <SettingsPage updates={updates} />}
        </main>
      </div>
      {updates.phase === 'confirming' && (
        // Desktop apply confirmation: the shared two-phase controller, plus
        // the fact the desktop shell itself exits and relaunches — the
        // update helper restarts the whole application, not just the server.
        <div class="overlay" role="dialog" aria-modal="true" aria-label="Apply server update">
          <section class="help-modal">
            <h2>Apply the server update?</h2>
            <p>The server restarts while the update installs. Playback is interrupted on every connected device, including this one.</p>
            <p>This application exits and relaunches to finish the update.</p>
            <div class="confirm-actions">
              <button type="button" onClick={updates.cancelApply}>Cancel</button>
              <button type="button" class="primary" onClick={() => void updates.confirmApply()}>Apply and restart</button>
            </div>
          </section>
        </div>
      )}
      {accountOpen && portal.snapshot?.accountsEnabled && (
        <PortalAccountDialog
          client={sharedApi()}
          origin={sharedOrigin()}
          storage={sessionStore()}
          identity={portal.identity}
          onIdentity={setPortalIdentity}
          onClose={() => setAccountOpen(false)}
        />
      )}
    </div>
  );
}
