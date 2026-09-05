// The Settings surface: a tabbed editor for server configuration plus the
// catalog sync maintenance actions and observed catalog coverage. The app
// renders these on both the Settings and Events views.
import { useEffect, useRef, useState } from 'preact/hooks';
import type { ComponentChild } from 'preact';
import { SettingsField } from '@torrent-tv/shared';
import { sharedApi } from './shared-api';

export function Events({ onError, confirmRebuild = false }: { onError: (value: string) => void; confirmRebuild?: boolean }) {
  const [message, setMessage] = useState('');
  const [pendingRebuild, setPendingRebuild] = useState(false);
  async function run(mode: 'latest' | 'rebuild') { try { const job = await sharedApi().syncCatalog(mode); setMessage(`${job.label} queued. Follow progress on Jobs.`) } catch (e) { onError((e as Error).message) } finally { setPendingRebuild(false) } }
  // The standalone Events page keeps the one-click behavior; the Settings
  // Maintenance tab asks first because the rebuild sweeps every category.
  const requestRebuild = () => { if (confirmRebuild) setPendingRebuild(true); else void run('rebuild') };
  return <section class="events-page"><p class="supporting">Run safe server maintenance without waiting for the schedule.</p><div class="event-actions"><article><h2>Fetch latest</h2><p>Append the newest FileList releases to the existing catalog.</p><button type="button" class="primary" onClick={() => void run('latest')}>Fetch latest</button></article><article><h2>Rebuild catalog</h2><p>Refresh the maximum API-visible results from every enabled category. Existing discoveries are retained.</p><button type="button" onClick={requestRebuild}>Rebuild catalog</button></article></div>{message && <p role="status" class="success">{message}</p>}{pendingRebuild && <div class="overlay" role="dialog" aria-modal="true" aria-label="Rebuild catalog"><section class="help-modal"><h2>Rebuild catalog?</h2><p>Refreshes every enabled category's latest window and rebuilds local projections. Nothing is removed; the work runs as a background job you can follow on the Jobs page.</p><div class="confirm-actions"><button type="button" onClick={() => setPendingRebuild(false)}>Cancel</button><button type="button" class="primary" onClick={() => void run('rebuild')}>Rebuild now</button></div></section></div>}</section>
}

export function CacheCoverage() { const [status, setStatus] = useState<Record<string, unknown> | null>(null); useEffect(() => { sharedApi().call<Record<string, unknown>>('/catalog/status').then(setStatus).catch(() => { }) }, []); if (!status) return null; return <section class="cache-coverage"><h2>Observed catalog coverage</h2><p><strong>{Number(status.observedReleases).toLocaleString()}</strong> releases retained · <strong>{Number(status.discoverableReleases).toLocaleString()}</strong> currently seeded · {Number(status.hiddenZeroSeeders).toLocaleString()} zero-seeder releases hidden</p><p class="supporting">FileList exposes at most {String(status.fileListLatestWindowLimit)} recent releases per latest request and no historical pagination. Searches and future syncs continue growing this append-only cache.</p></section> }

type SettingsRow = [string, string, string?, string?];

// Connection checks live beside the fields they validate and gather on the
// Test tab. LED state is session-scoped: it starts untested and changes only
// from tests actually run in this session.
const CONNECTIONS = [
  { name: 'filelist', label: 'FileList', tab: 'tracker' },
  { name: 'tmdb', label: 'TMDB', tab: 'tracker' },
  { name: 'qbittorrent', label: 'qBittorrent', tab: 'storage' },
  { name: 'storage', label: 'Storage', tab: 'storage' },
  { name: 'subdl', label: 'SubDL', tab: 'playback' },
];

const connectionsFor = (tab: string) => CONNECTIONS.filter(connection => connection.tab === tab);

const TABS: Array<{ id: string; label: string }> = [
  { id: 'tracker', label: 'Tracker' },
  { id: 'storage', label: 'Storage' },
  { id: 'playback', label: 'Playback' },
  { id: 'server', label: 'Server' },
  { id: 'maintenance', label: 'Maintenance' },
  { id: 'test', label: 'Test' },
];
// The account tab (supporter key) exists only while the portal reports a
// working account capability; capability loss removes every trace of the
// tab and its fields without touching the stored server key.
const ACCOUNT_TAB = { id: 'account', label: 'Account' };
const tabsFor = (accountsEnabled: boolean) => accountsEnabled ? [...TABS.slice(0, 4), ACCOUNT_TAB, ...TABS.slice(4)] : TABS;

const TAB_GROUPS: Record<string, Array<{ title: string; fields: SettingsRow[]; when?: (current: Record<string, unknown>) => boolean }>> = {
  tracker: [
    { title: 'Tracker and metadata', fields: [['FileList URL', 'fileListUrl'], ['FileList username', 'fileListUsername'], ['FileList passkey', 'fileListPasskey', 'password'], ['TMDB API key or token', 'tmdbApiKey', 'password'], ['Metadata language', 'metadataLanguage'], ['Metadata fallback language', 'metadataFallbackLanguage']] },
  ],
  storage: [
    { title: 'Download engine', fields: [['Download engine', 'downloadEngine', 'engine-toggle']] },
    { title: 'Built-in torrent engine', fields: [['Torrent peer port', 'torrentPeerPort', 'number'], ['Torrent session directory', 'torrentSessionDir']], when: current => current.downloadEngine === 'native' },
    { title: 'qBittorrent', fields: [['qBittorrent URL', 'qbittorrentUrl'], ['qBittorrent username', 'qbittorrentUsername'], ['qBittorrent password', 'qbittorrentPassword', 'password']], when: current => current.downloadEngine === 'qbittorrent' },
    { title: 'Storage', fields: [['Download root', 'downloadRoot'], ['Allocation (GB)', 'allocationGb', 'number', '0.5'], ['Free-space reserve (GB)', 'reserveGb', 'number', '0.5'], ['Eviction rules (comma separated)', 'evictionRules'], ['Protect incomplete downloads', 'protectIncomplete', 'checkbox'], ['Protect actively streamed downloads', 'protectLeased', 'checkbox'], ['Protect favorites', 'protectFavorites', 'checkbox'], ['Protect never-watched downloads', 'protectNeverWatched', 'checkbox'], ['Artwork cache path', 'artworkCachePath'], ['Artwork cache maximum bytes', 'artworkCacheMaxBytes', 'number']] },
  ],
  playback: [
    { title: 'Playback and subtitles', fields: [['Initial buffer bytes', 'initialBufferBytes', 'number'], ['Read-ahead bytes', 'readAheadBytes', 'number'], ['Piece timeout seconds', 'pieceWaitTimeoutSeconds', 'number'], ['SubDL API URL', 'subDLUrl'], ['SubDL API key', 'subDLApiKey', 'password'], ['Preferred audio language', 'preferredAudioLanguage'], ['Preferred subtitle language', 'preferredSubtitleLanguage'], ['Fallback subtitle language', 'fallbackSubtitleLanguage'], ['Watched threshold percent', 'watchedThresholdPercent', 'number'], ['Subtitle cache path', 'subtitleCachePath'], ['Subtitle cache maximum bytes', 'subtitleCacheMaxBytes', 'number'], ['ffprobe path', 'ffprobePath'], ['FFmpeg path', 'ffmpegPath']] },
  ],
  server: [
    { title: 'Server and background work', fields: [['Server name', 'instanceName'], ['Listen address', 'listenAddress'], ['Database path', 'databasePath'], ['Catalog max age hours', 'catalogMaxAgeHours', 'number'], ['Maximum concurrent jobs', 'maxConcurrentJobs', 'number'], ['Title refresh timeout minutes', 'titleRefreshTimeoutMinutes', 'number'], ['Trusted CIDRs (comma separated)', 'trustedCidrs']] },
  ],
  account: [
    { title: 'Supporter account', fields: [['Supporter API key', 'portalAPIKey', 'password']] },
  ],
};

// The active tab rides the URL hash so refresh and shared links reopen the
// same section; anything unknown falls back to the first tab.
function tabFromHash(accountsEnabled: boolean): string {
  const id = location.hash.replace(/^#/, '');
  return tabsFor(accountsEnabled).some(tab => tab.id === id) ? id : 'tracker';
}

const tabFieldKeys = (id: string): string[] => (TAB_GROUPS[id] || []).flatMap(group => group.fields.map(field => field[1]));
const isConfigTab = (id: string) => Boolean(TAB_GROUPS[id]);
// The server stores list-shaped fields as arrays; the form edits them as
// canonical comma strings, so every dirty check and draft revert compares
// against this form shape.
const formValue = (key: string, value: unknown) => (key === 'trustedCidrs' || key === 'evictionRules') && Array.isArray(value) ? (value as string[]).join(', ') : value;

// Render plain help text with bare URLs as clickable links.
const linkify = (text: string) =>
  text.split(/(https?:\/\/[^\s,)]+)/).map((part, i) =>
    /^https?:\/\//.test(part) ? <a key={i} href={part} target="_blank" rel="noreferrer">{part}</a> : part
  );

export function Settings({ value, fields, onSaved, onError, onDirtyChange, accountsEnabled, updateSection, save: saveTransport }: {
  value: Record<string, unknown>; fields: SettingsField[]; onSaved: (v: Record<string, unknown>) => void; onError: (s: string) => void; onDirtyChange?: (dirty: boolean) => void; accountsEnabled?: boolean; updateSection?: ComponentChild;
  // Alternate save transport for embedded hosts (the desktop GUI): the save
  // bar calls it with the submitted body instead of the storage PUT, and a
  // thrown error takes the normal error path. Absent, the webapp behaves
  // exactly as before.
  save?: (value: Record<string, unknown>) => Promise<{ saved: boolean; restartRequired?: boolean } | void>
}) {
  const [current, setCurrent] = useState(() => { const draft = { ...value }; Object.keys(draft).forEach(key => { draft[key] = formValue(key, draft[key]) }); return draft });
  const [message, setMessage] = useState('');
  const [help, setHelp] = useState<SettingsField | null>(null);
  const [tests, setTests] = useState<Record<string, string>>({});
  const [connState, setConnState] = useState<Record<string, string>>({});
  const [tab, setTabState] = useState(() => tabFromHash(accountsEnabled === true));
  // The account tab can disappear under the user (capability loss mid-edit):
  // an unknown tab renders the first one, so no orphan field group remains.
  const visibleTabs = tabsFor(accountsEnabled === true);
  const activeTab = visibleTabs.some(entry => entry.id === tab) ? tab : 'tracker';
  const accountsEnabledRef = useRef(accountsEnabled === true);
  accountsEnabledRef.current = accountsEnabled === true;
  const setTab = (id: string) => { setTabState(id); history.replaceState(null, '', `#${id}`) };
  const tabEdits = (id: string) => tabFieldKeys(id).filter(key => current[key] !== formValue(key, value[key]));
  const [pendingTab, setPendingTab] = useState<string | null>(null);
  const anyDirty = () => visibleTabs.some(entry => tabEdits(entry.id).length > 0);
  // Tab switches ask first while anything on the page is dirty; the
  // beforeunload prompt covers browser close and refresh.
  const requestTab = (id: string) => {
    if (!visibleTabs.some(entry => entry.id === id)) return;
    if (id === activeTab || !anyDirty()) { setTab(id); return }
    setPendingTab(id);
  };
  // The hashchange listener lives for the component's life but the guard
  // reads fresh state, so it dispatches through a ref.
  const requestTabRef = useRef(requestTab); requestTabRef.current = requestTab;
  useEffect(() => {
    onDirtyChange?.(anyDirty());
  });
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (!anyDirty()) return;
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  });
  const tabLed = (id: string) => {
    const states = connectionsFor(id).map(connection => connState[connection.name]);
    if (states.includes('fail')) return 'fail';
    if (states.includes('testing')) return 'testing';
    if (states.includes('pass')) return 'pass';
    return '';
  };
  // Hash edits are navigation too: they go through the same guarded switch
  // so Back/forward and manual hash changes cannot bypass the prompt.
  useEffect(() => {
    const followHash = () => requestTabRef.current(tabFromHash(accountsEnabledRef.current));
    window.addEventListener('hashchange', followHash);
    return () => window.removeEventListener('hashchange', followHash);
  }, []);
  const descriptor = (key: string, label: string) => fields.find(field => field.key === key) || { key, label, help: `Controls ${label.toLowerCase()}.`, obtain: '', tvVisible: false, sensitive: false, restartRequired: false, readOnly: false };
  async function save(e: Event) {
    e.preventDefault();
    // One PUT carries the whole settings object, but only the active tab's
    // edits ride on top of the last-saved values — edits made on other tabs
    // stay pending until their own tab is saved.
    const merged: Record<string, unknown> = { ...value };
    tabFieldKeys(activeTab).forEach(key => { merged[key] = current[key] });
    if (typeof merged.trustedCidrs === 'string') merged.trustedCidrs = (merged.trustedCidrs as string).split(',').map((x: string) => x.trim()).filter(Boolean);
    if (typeof merged.evictionRules === 'string') merged.evictionRules = (merged.evictionRules as string).split(',').map((x: string) => x.trim().toLowerCase()).filter(Boolean);
    const out = { ...merged };
    Object.keys(out).filter(k => k.endsWith('Configured') || k === 'settingsPath').forEach(k => delete out[k]);
    try {
      if (saveTransport) await saveTransport(out);
      else await sharedApi().call('/settings', { method: 'PUT', body: JSON.stringify(out) });
      setMessage('Settings saved. Environment-managed values remain controlled by .env.docker.');
      onSaved(merged);
      // The saved tab's draft snaps to the canonical form shape so the tab
      // reads clean immediately — no leave-and-return required.
      const canonical = { ...current };
      tabFieldKeys(activeTab).forEach(key => { canonical[key] = formValue(key, merged[key]) });
      setCurrent(canonical);
    } catch (e) { onError((e as Error).message) }
  }
  function discard() {
    const reverted = { ...current };
    tabFieldKeys(activeTab).forEach(key => { reverted[key] = formValue(key, value[key]) });
    setCurrent(reverted);
    setMessage('');
  }
  async function test(name: string) {
    setTests(current => ({ ...current, [name]: 'Testing…' }));
    setConnState(current => ({ ...current, [name]: 'testing' }));
    try {
      const result = await sharedApi().call<{ message: string }>(`/dependencies/${name}/test`, { method: 'POST' });
      setTests(current => ({ ...current, [name]: result.message }));
      setConnState(current => ({ ...current, [name]: 'pass' }));
    } catch (e) {
      setTests(current => ({ ...current, [name]: (e as Error).message }));
      setConnState(current => ({ ...current, [name]: 'fail' }));
    }
  }
  const renderField = ([label, key, type, step]: SettingsRow) => {
    const info = descriptor(key, label);
    if (type === 'engine-toggle') {
      // The engine choice is a two-way segmented toggle, not free text: the
      // value is one of two known engines, and the selection drives which
      // field groups render below it. Disabled when the environment owns it.
      const options: Array<[string, string]> = [['native', 'Native'], ['qbittorrent', 'qBittorrent']];
      const active = String(current[key] ?? 'native');
      return <label class="engine-toggle"><span>{label}{info.restartRequired && <small> restart required</small>}{info.readOnly && <small> environment managed</small>}{info.help && <button type="button" class="help-button" aria-label={`Help for ${label}`} title={info.help} onClick={() => setHelp(info)}>?</button>}</span><span class="engine-toggle-options" role="group" aria-label={label}>{options.map(([engineValue, engineLabel]) => <button type="button" key={engineValue} disabled={info.readOnly} aria-pressed={active === engineValue} onClick={e => { e.preventDefault(); setCurrent({ ...current, [key]: engineValue }) }}>{engineLabel}</button>)}</span></label>;
    }
    if (type === 'checkbox') {
      // Protection flags render as switches: a real checkbox stays in the
      // DOM (visually hidden, still focusable) for keyboard and assistive
      // tech, with the track and knob as decoration.
      return <label class="switch-field"><input disabled={info.readOnly} type="checkbox" checked={Boolean(current[key])} onInput={e => setCurrent({ ...current, [key]: e.currentTarget.checked })} /><span class="switch-track" aria-hidden="true"><span class="switch-knob" /></span><span class="switch-copy">{label}{info.help && <button type="button" class="help-button" aria-label={`Help for ${label}`} title={info.help} onClick={() => setHelp(info)}>?</button>}</span></label>;
    }
    return <label><span>{label}{info.restartRequired && <small> restart required</small>}{info.readOnly && <small> environment managed</small>}<button type="button" class="help-button" aria-label={`Help for ${label}`} title={info.help} onClick={() => setHelp(info)}>?</button></span><input disabled={info.readOnly} type={type || 'text'} step={type === 'number' ? (step || undefined) : undefined} value={String(current[key] ?? '')} placeholder={type === 'password' && value[`${key}Configured`] ? 'Configured — leave blank to keep' : key === 'evictionRules' ? 'oldest-completed' : ''} onInput={e => setCurrent({ ...current, [key]: type === 'number' ? Number(e.currentTarget.value) : e.currentTarget.value })} /></label>;
  };
  const diagnostics = (connections: typeof CONNECTIONS) => <section class="diagnostics"><h2>Connection checks</h2>{connections.map(connection => <div><button type="button" onClick={() => void test(connection.name)}>Test {connection.label}</button><span role="status">{tests[connection.name]}</span></div>)}</section>;
  const panelContent = () => {
    if (activeTab === 'maintenance') return <><CacheCoverage /><Events onError={onError} confirmRebuild /></>;
    if (activeTab === 'test') return diagnostics(CONNECTIONS);
    const visibleGroups = () => (TAB_GROUPS[activeTab] || []).filter(group => !group.when || group.when(current));
    return <>{visibleGroups().map(renderGroup)}{connectionsFor(activeTab).length > 0 && diagnostics(connectionsFor(activeTab))}</>;
  };
  const renderGroup = (group: { title: string; fields: SettingsRow[]; when?: (current: Record<string, unknown>) => boolean }) => {
    // Switches read best as their own full-width list under the value fields.
    const inputs = group.fields.filter(field => field[2] !== 'checkbox' && field[2] !== 'engine-toggle');
    const switches = group.fields.filter(field => field[2] === 'checkbox');
    const toggles = group.fields.filter(field => field[2] === 'engine-toggle');
    return <fieldset><legend>{group.title}</legend>{toggles.length > 0 && <div class="fields">{toggles.map(renderField)}</div>}{inputs.length > 0 && <div class="fields">{inputs.map(renderField)}</div>}{switches.length > 0 && <div class="switch-list">{switches.map(renderField)}</div>}</fieldset>;
  };
  return <>
    <form class="settings" onSubmit={save}>
      <p class="supporting">Stored securely at {String(value.settingsPath || 'data/settings.json')}. Blank secrets keep their current value. Fields supplied by the process environment are shown read-only.</p>
      <div class="settings-tabs" role="tablist" aria-label="Settings sections">
        {visibleTabs.map(t => <button type="button" role="tab" class={[t.id === 'maintenance' ? 'ops-start' : '', tabEdits(t.id).length > 0 ? 'dirty' : ''].filter(Boolean).join(' ')} aria-selected={activeTab === t.id} onClick={() => requestTab(t.id)}>{connectionsFor(t.id).length > 0 && <span class={`led ${tabLed(t.id)}`} aria-hidden="true" />}{t.label}</button>)}
      </div>
      <div class="settings-panel" role="tabpanel">{panelContent()}</div>
      {isConfigTab(activeTab) && <><div class="settings-actions"><span class="dirty-count" role="status">{tabEdits(activeTab).length > 0 ? `${tabEdits(activeTab).length} unsaved ${tabEdits(activeTab).length === 1 ? 'change' : 'changes'}` : ''}</span><button type="button" disabled={tabEdits(activeTab).length === 0} onClick={discard}>Discard changes</button><button class="primary" type="submit" disabled={tabEdits(activeTab).length === 0}>Save changes</button></div>{message && <p class="settings-status" role="status">{message}</p>}</>}
    </form>
    {updateSection}
    {help && <div class="overlay" role="dialog" aria-modal="true" aria-label={`Help for ${help.label}`}><section class="help-modal"><button class="close" onClick={() => setHelp(null)}>Close</button><h2>{help.label}</h2><p>{help.help}</p>{help.readOnly && <p><strong>This setting is managed by the process environment and cannot be edited here.</strong></p>}{help.restartRequired && <p><strong>Restart required after changing this setting.</strong></p>}{help.obtain && <><h3>Where to get it</h3><p>{linkify(help.obtain)}</p></>}<button onClick={() => void navigator.clipboard.writeText([help.help, help.obtain].filter(Boolean).join('\n\n')).then(() => setMessage('Help copied.'))}>Copy help</button></section></div>}
    {pendingTab !== null && <div class="overlay" role="dialog" aria-modal="true" aria-label="Unsaved tab changes"><section class="help-modal"><h2>Tab has unsaved changes</h2><p>Unsaved changes on this tab stay pending — the tab label keeps its dot until you save or discard them.</p><div class="confirm-actions"><button type="button" onClick={() => { history.replaceState(null, '', `#${activeTab}`); setPendingTab(null) }}>Keep editing</button><button type="button" class="primary" onClick={() => { const target = pendingTab; setPendingTab(null); setTab(target) }}>Switch anyway</button></div></section></div>}
  </>
}
