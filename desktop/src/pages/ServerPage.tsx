import { useEffect, useRef, useState } from 'preact/hooks';
import {
  AutostartStatus,
  ChangeDataDir,
  DataDirInfo,
  DisableAutostart,
  EnableAutostart,
  LoadSettings,
  OpenPath,
  OpenWebUI,
  ReadLogs,
  StartServer,
  StopServer,
  Version,
} from '../bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings';

import { applyServerUpdate, checkForServerUpdate, usePortal, useServerState } from '../lib/state';

// renderLogLine formats one JSONL log record as "time LEVEL message" —
// the server's slog JSON handler keys — with HTTP access records rendered
// pretty as "time LEVEL GET /api/v1/jobs 200 12ms" from their attributes.
// Anything unparsable, foreign, or missing an access attribute renders raw
// or in the generic shape.
function renderLogLine(line: string): string {
  try {
    const parsed = JSON.parse(line) as Record<string, unknown>;
    if (typeof parsed.time === 'string' || typeof parsed.msg === 'string') {
      const head = [parsed.time, parsed.level].filter(value => typeof value === 'string');
      if (parsed.msg === 'http request') {
        const attrs = [parsed.method, parsed.path, parsed.status, typeof parsed.durationMs === 'number' ? `${parsed.durationMs}ms` : undefined]
          .filter(value => value !== undefined)
          .map(value => String(value));
        if (attrs.length === 4) {
          return [...head, ...attrs].join(' ');
        }
      }
      return [...head, parsed.msg].filter(value => typeof value === 'string').join(' ');
    }
  } catch { /* not JSON: raw */ }
  return line;
}

// Server page: the status card (the page's one bold element: state line +
// Start/Stop + Open web UI), the Start-at-login card (toggle reflects the OS
// read-back, never memory), and the details row (version, settings file,
// data folder with reveal buttons).
export function ServerPage() {
  const server = useServerState();
  const portal = usePortal();
  const [version, setVersion] = useState('');
  const [settingsPath, setSettingsPath] = useState('');
  const [dataDir, setDataDir] = useState('');
  const [dataDirSource, setDataDirSource] = useState('');
  // null = not read back yet; the toggle stays disabled until the OS answers.
  const [autostart, setAutostart] = useState<boolean | null>(null);
  const [error, setError] = useState('');
  // The Change data folder dialog: open/closed, the entered path, the
  // backend's verbatim refusal (after submit), and the in-flight flag.
  const [changeOpen, setChangeOpen] = useState(false);
  const [changeTarget, setChangeTarget] = useState('');
  const [changeError, setChangeError] = useState('');
  const [moving, setMoving] = useState(false);
  // The live log viewer: open/closed, the raw lines read so far, and —
  // outside React state, they are call parameters, not render input — the
  // byte offset the next poll passes and whether the view is pinned to
  // the tail (auto-scroll) or the user scrolled up to read history.
  const [logsOpen, setLogsOpen] = useState(false);
  const [logLines, setLogLines] = useState<string[]>([]);
  // Set when a poll fails so the panel says so instead of sitting there
  // silently empty; cleared by the next successful read.
  const [logError, setLogError] = useState('');
  const logOffset = useRef(0);
  const logPinned = useRef(true);
  const logView = useRef<HTMLPreElement>(null);

  // Opening the panel renders it below the fold (the Details fieldset ends
  // the page); bring it into view once it exists. requestAnimationFrame
  // waits one frame so the pre has layout to scroll to.
  useEffect(() => {
    if (!logsOpen) return;
    const raf = requestAnimationFrame(() => logView.current?.scrollIntoView?.({ block: 'nearest' }));
  }, [logsOpen]);

  useEffect(() => {
    let alive = true;
    void Version().then(v => { if (alive) setVersion(v) }).catch(() => { });
    void DataDirInfo().then(([dir, source]) => {
      if (!alive) return;
      setDataDir(dir);
      setDataDirSource(source);
    }).catch(() => { });
    void LoadSettings().then(view => { if (alive) setSettingsPath(view.settingsPath) }).catch(() => { });
    void refreshAutostart();
    return () => { alive = false };
  }, []);

  // While the viewer is open, poll the backend's incremental tail every
  // 1.5 s. A log truncated or rotated underneath us (our offset is past
  // the reported size) replaces the view instead of appending — the
  // backend restarted from byte 0. Transient read errors keep the tail;
  // the next tick retries.
  useEffect(() => {
    if (!logsOpen) return;
    let alive = true;
    const poll = async () => {
      try {
        const tail = await ReadLogs(logOffset.current);
        if (!alive) return;
        const reset = logOffset.current > tail.size;
        logOffset.current = tail.nextOffset;
        if (reset) logPinned.current = true;
        setLogError('');
        setLogLines(lines => reset ? tail.lines : [...lines, ...tail.lines]);
      } catch (e) {
        setLogError(`Log read failed: ${(e as Error)?.message ?? e}`);
      }
    };
    void poll();
    const timer = setInterval(() => void poll(), 1500);
    return () => { alive = false; clearInterval(timer) };
  }, [logsOpen]);

  // Follow the tail only while the view is pinned to the bottom; a
  // scroll-up freezes it so history can be read in peace.
  useEffect(() => {
    const el = logView.current;
    if (logPinned.current && el) el.scrollTop = el.scrollHeight;
  }, [logLines, logsOpen]);

  // A scroll event re-arms the tail only from the bottom edge (a small
  // tolerance absorbs sub-pixel rounding), never from mid-history.
  function onLogScroll() {
    const el = logView.current;
    if (el) logPinned.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 24;
  }

  async function refreshAutostart() {
    try {
      const enabled = await AutostartStatus();
      setAutostart(enabled);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function toggleAutostart() {
    setError('');
    try {
      if (autostart) await DisableAutostart();
      else await EnableAutostart();
    } catch (e) {
      setError((e as Error).message);
    }
    // The OS artifact is the source of truth: read it back after any change.
    await refreshAutostart();
  }

  async function run(action: () => Promise<void>) {
    setError('');
    try {
      await action();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  function openChange() {
    setChangeError('');
    setChangeTarget('');
    setChangeOpen(true);
  }

  // Submit calls the backend once; only a resolved change refreshes the
  // path (from a fresh DataDirInfo read — the backend may normalize it).
  // A rejection shows the backend's error verbatim and keeps the dialog
  // open: the move rolled back Go-side, so the user can correct the path.
  async function submitChange() {
    const target = changeTarget.trim();
    if (!target || moving) return;
    setChangeError('');
    setMoving(true);
    try {
      await ChangeDataDir(target);
      const [dir, source] = await DataDirInfo();
      setDataDir(dir);
      setDataDirSource(source);
      setChangeOpen(false);
      setChangeTarget('');
    } catch (e) {
      setChangeError((e as Error).message);
    } finally {
      setMoving(false);
    }
  }


  const transitioning = server.state === 'starting' || server.state === 'stopping';
  const stateLine = server.state === 'running'
    ? `Running on http://${server.address || '127.0.0.1:8097'}`
    : server.state === 'failed' ? `Failed — ${server.error || 'unknown error'}`
      : server.state === 'stopped' ? 'Stopped' : server.state[0].toUpperCase() + server.state.slice(1) + '…';

  return (
    <section class="settings">
      <fieldset>
        <legend>Server</legend>
        <p class="server-state-line">
          <span class={`dot dot-${server.state}`} aria-hidden="true" />
          {stateLine}
        </p>
        {server.state === 'running' && (
          <p class="update-check">
            <button type="button" disabled={transitioning} onClick={() => void run(async () => { await checkForServerUpdate() })}>Check for updates</button>
            {portal.status?.available && portal.status.latest ? `Latest: version ${portal.status.latest}.` : ''}
          </p>
        )}
        {server.state === 'running' && portal.status?.available && (
          <p class="update-state" data-state="available">
            {portal.status.latest ? `Update available: version ${portal.status.latest}.` : 'A new version is available.'}
            {portal.status.selfUpdate && <button class="primary" type="button" disabled={transitioning} onClick={() => void run(async () => { await applyServerUpdate() })}>Download and install — Torrent TV restarts</button>}
          </p>
        )}
        <div class="fields">
          {server.state === 'running' || server.state === 'starting'
            ? <button class="primary" type="button" disabled={transitioning} onClick={() => void run(StopServer)}>Stop server</button>
            : <button class="primary" type="button" disabled={transitioning} onClick={() => void run(StartServer)}>Start server</button>}
          <button type="button" disabled={server.state !== 'running'} onClick={() => void run(OpenWebUI)}>Open web UI</button>
        </div>
        {error && <p class="settings-status" role="alert">{error}</p>}
      </fieldset>
      <fieldset>
        <legend>Start at login</legend>
        <label class="switch-field">
          <input
            type="checkbox"
            checked={autostart === true}
            disabled={autostart === null}
            onInput={() => void toggleAutostart()}
          />
          <span class="switch-track" aria-hidden="true"><span class="switch-knob" /></span>
          <span class="switch-copy">Start Torrent TV when you log in</span>
        </label>
        <p class="supporting">Starts minimized to the tray. The switch always reflects the operating system's launch-on-boot entry.</p>
      </fieldset>
      <fieldset>
        <legend>Details</legend>
        <p class="supporting">Version {version || '…'}</p>
        <p class="supporting">Settings file: {settingsPath || '…'}</p>
        <p class="supporting">
          Data folder: {dataDir || '…'}{dataDirSource ? ` (from ${dataDirSource})` : ''}
          {' '}
          <button type="button" disabled={!dataDir} onClick={() => void run(() => OpenPath('data'))}>Open data folder</button>
          {' '}
          <button type="button" disabled={!dataDir} onClick={() => void run(() => OpenPath('logs'))}>Open logs folder</button>
          {' '}
          <button type="button" disabled={!dataDir} aria-expanded={logsOpen} onClick={() => setLogsOpen(open => !open)}>View logs</button>
          {' '}
          <button type="button" disabled={!dataDir} onClick={openChange}>Change…</button>
        </p>
        {logError && <p class="settings-status" role="alert">{logError}</p>}
        {logsOpen && (
          <pre class="log-view" ref={logView} onScroll={onLogScroll} aria-label="Server log tail">
            {logLines.length ? logLines.map(line => renderLogLine(line) + '\n') : 'No log lines yet.'}
          </pre>
        )}
      </fieldset>
      {changeOpen && (
        <div class="overlay" role="dialog" aria-modal="true" aria-labelledby="change-data-dir-heading">
          <section class="removal-confirm">
            <h2 id="change-data-dir-heading">Change data folder</h2>
            <p class="supporting">The server will restart; your data moves to the new location.</p>
            <div class="fields">
              <label>
                New location
                <input
                  value={changeTarget}
                  onInput={e => setChangeTarget(e.currentTarget.value)}
                  placeholder="/absolute/path/for/the/data"
                  aria-label="New data folder path"
                />
              </label>
            </div>
            {changeError && <p class="settings-status" role="alert">{changeError}</p>}
            <div class="confirm-actions">
              <button type="button" disabled={moving} onClick={() => setChangeOpen(false)}>Cancel</button>
              <button type="button" class="primary" disabled={moving || !changeTarget.trim()} onClick={() => void submitChange()}>Move data</button>
            </div>
          </section>
        </div>
      )}
    </section>
  );
}
