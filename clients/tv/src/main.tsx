import { Fragment, render } from 'preact';
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { API, canonicalHouseholdItems, canonicalLanguage, ControlsVisibility, subtitleRank, CatalogDetail, CatalogFacets, CatalogSource, CatalogTitle, Download, DownloadSort, downloadTransferActions, DownloadTransferAction, formatBytes, HouseholdItem, HouseholdState, Job, JobLog, LibraryCategory, MediaState, orderDownloadIDs, PlaybackPreferences, PortalPromotion, PortalState, promotionScreenTimeMs, reconcileDownloads, Release, resumeActionLabel, resumeForTitle, resumeSummary, seasonPackActionLabel, SettingsField, SubtitleCandidate, subtitleItemLabel, subtitleMenuGroups, UpdateStatus } from '@torrent-tv/shared';
import { chooseStructuredTarget, focusElement, remoteAction, useTVNavigation } from './navigation';
import { PROJECTS_DIALOG_REGION, PROJECTS_MENU_ROW, UPDATE_APPLY_ROW, UPDATE_CHECK_ROW, UPDATE_DIALOG_REGION, confirmDialogStale, dialogRestoreKey, promotionsVisible, recoverySettles, snapshotEventAllowed, updateApplyDisabled, updateApplyOutcome, updateNoticeVisible } from './portal';
import { AVTrack, clampSeek, formatTime, hiddenKeyRoute, isDownloadComplete, normalizeTrack, parseVTT, playerAction, preferredAudio, SubtitleCue, subtitleAt } from './player';
import { householdSections, trackerCategories } from './catalog-data';
import { discoverServers, DiscoveredServer, normalizeServerURL } from './discovery';
import { appIdentity } from './app-name';
import { exitApplication, registerMediaKeys } from './platform';
import './tv.css';
import './performance.css';

const STORAGE = 'filelist.serverUrl';
const emptyState: HouseholdState = { favorites: [], continueWatching: [], recent: [], watched: [] };
const emptyFacets: CatalogFacets = { categories: [], kinds: [], resolutions: [], hdr: [], qualities: [], codecs: [] };
const needsEpisodeExpansion = (detail: CatalogDetail) => detail.title.kind === 'series' && detail.seasons.some(season => (season.packSources?.length || 0) > 0 && season.episodes.length === 0);
type DetailTarget = { season?: number; episode?: number };

function TVStateBadges({ state }: { state?: MediaState }) { if (!state?.downloadState || !state.watchState) return null; const download = state.downloadState; const watch = state.watchState; return <span class="tv-state-badges">{download !== 'none' && <span class={`tv-state-badge download ${download}`}><i aria-hidden="true" /><b>{download === 'downloaded' ? 'Downloaded' : download === 'partial' ? 'Some downloaded' : download === 'error' ? 'Download error' : `${Math.round((state.progress || 0) * 100)}% downloaded`}</b></span>}{watch !== 'unwatched' && <span class={`tv-state-badge watch ${watch}`}><i aria-hidden="true" /><b>{watch === 'watched' ? 'Watched' : watch === 'partial' ? 'Some watched' : 'In progress'}</b></span>}</span> }

type TVDownloadAnchor = { id: string; top: number; scrollTop: number };
function captureTVDownloadAnchor(): TVDownloadAnchor | null { const container = document.querySelector<HTMLElement>('.tv-content'); if (!container) return null; const rows = Array.from(document.querySelectorAll<HTMLElement>('[data-download-id]')); const row = rows.find(item => item.getBoundingClientRect().bottom > container.getBoundingClientRect().top); return row ? { id: row.dataset.downloadId || '', top: row.getBoundingClientRect().top, scrollTop: container.scrollTop } : null }
function restoreTVDownloadAnchor(anchor: TVDownloadAnchor | null) { if (!anchor) return; const container = document.querySelector<HTMLElement>('.tv-content'); const row = document.querySelector<HTMLElement>(`[data-download-id="${CSS.escape(anchor.id)}"]`); if (!container || !row) return; const delta = row.getBoundingClientRect().top - anchor.top; if (Math.abs(delta) > 0.5) container.scrollTop += delta }

window.FileListBoot?.stage('Rendering interface');

type SubtitleMenuRow = { key: string; row: number; position: number; heading: string; candidate: SubtitleCandidate; detail: string };
function subtitleMenuRows(candidates: SubtitleCandidate[], firstRow: number, keyPrefix: string): SubtitleMenuRow[] {
  const rows: SubtitleMenuRow[] = [];
  let row = firstRow;
  for (const group of subtitleMenuGroups(candidates)) {
    let headingPending = true;
    for (const candidate of group.items) {
      const detail = [candidate.format, candidate.hearingImpaired ? 'hearing impaired' : ''].filter(Boolean).join(' · ');
      rows.push({ key: `${keyPrefix}-${candidate.provider}-${candidate.id}`, row: row++, position: rows.length + 1, heading: headingPending ? group.label : '', candidate, detail });
      headingPending = false;
    }
  }
  return rows;
}

function Player({ api, download, resumeMs, preferences, onClose, onStateChanged, onComplete }: { api: API; download: Download; resumeMs: number; preferences?: PlaybackPreferences; onClose: () => void; onStateChanged: () => void; onComplete: (preferences: PlaybackPreferences) => void }) {
  const [message, setMessage] = useState('Opening stream…');
  const [phase, setPhase] = useState<'opening' | 'playing' | 'paused' | 'buffering' | 'waiting' | 'failed'>('opening');
  const [position, setPosition] = useState(resumeMs);
  const [total, setTotal] = useState(0);
  const [controlsVisible, setControlsVisible] = useState(true);
  const [tracks, setTracks] = useState<AVTrack[]>([]);
  const [menu, setMenu] = useState<null | 'audio' | 'subtitles' | 'find-subtitles' | 'options' | 'info'>(null);
  const [subtitleCandidates, setSubtitleCandidates] = useState<SubtitleCandidate[]>([]);
  const [subtitleDelay, setSubtitleDelay] = useState(0);
  const [externalSubtitle, setExternalSubtitle] = useState('');
  const [aspect, setAspect] = useState('PLAYER_DISPLAY_MODE_AUTO_ASPECT_RATIO');
  const [liveDownload, setLiveDownload] = useState(download);
  const current = useRef(resumeMs);
  const duration = useRef(0);
  const lastSaved = useRef(0);
  const session = useRef(0);
  const completedRetryUsed = useRef(false);
  const pollTimer = useRef(0);
  const scrubTimer = useRef(0);
  const messageTimer = useRef(0);
  const scrubTarget = useRef<number | null>(null);
  const controlsVisibleRef = useRef(true);
  const subtitleDelayRef = useRef(0);
  const subtitleCues = useRef<SubtitleCue[]>([]);
  const playing = useRef(false);
  const externalSubtitlePath = useRef('');
  const autoSubtitleAttempted = useRef(false);
  const preferenceRef = useRef<PlaybackPreferences>(preferences || { audioLanguage: 'en', audioTrackIndex: -1, subtitleLanguage: 'ro', subtitleMode: 'auto' });
  const phaseRef = useRef(phase);
  const messageRef = useRef(message);
  const menuRef = useRef(menu);
  const lastControlFocus = useRef('play');
  const menuLauncher = useRef('play');
  phaseRef.current = phase;
  messageRef.current = message;
  menuRef.current = menu;
  controlsVisibleRef.current = controlsVisible;
  subtitleDelayRef.current = subtitleDelay;
  const controls = useMemo(() => new ControlsVisibility({ policy: { armWhilePaused: true, statusHolds: false }, onChange: value => { controlsVisibleRef.current = value; setControlsVisible(value); } }), []);
  const save = async () => { if (duration.current > 0) try { await api.updatePlayback(download.id, current.current, duration.current); onStateChanged() } catch { } };
  const savePreferences = async (value: PlaybackPreferences) => { preferenceRef.current = value; try { preferenceRef.current = await api.updatePlaybackPreferences(download.id, value) } catch { } };

  function stopAVPlay() {
    const av = window.webapis?.avplay;
    try { av?.stop(); } catch { }
    try { av?.close(); } catch { }
  }

  function revealControls(sticky = false) {
    controlsVisibleRef.current = true;
    setControlsVisible(true);
    controls.reveal(sticky);
  }
  function hideControls() {
    controlsVisibleRef.current = false;
    setControlsVisible(false);
    controls.hide();
  }

  function showTransientMessage(value: string, duration = 3000) {
    window.clearTimeout(messageTimer.current);
    setMessage(value);
    messageTimer.current = window.setTimeout(() => { if (messageRef.current === value) setMessage('') }, duration);
  }

  function focusControl(key = lastControlFocus.current) {
    window.setTimeout(() => focusElement(document.querySelector<HTMLElement>(`[data-player-control="${key}"]`)), 0);
  }
  function keepControl(key: string, action: () => void) { lastControlFocus.current = key; action(); focusControl(key) }

  function openMenu(next: Exclude<typeof menu, null>, launcher: string) {
    menuLauncher.current = launcher;
    lastControlFocus.current = launcher;
    setMenu(next);
    controls.setPanelOpen(true);
    revealControls(true);
    window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('.player-dialog button')), 0);
  }

  function closeMenu() {
    const launcher = menuLauncher.current;
    setMenu(null);
    revealControls();
    controls.setPanelOpen(false);
    focusControl(launcher);
  }
  function switchMenu(next: Exclude<typeof menu, null>, openerKey: string) {
    setMenu(next);
    controls.setPanelOpen(true);
    revealControls(true);
    window.setTimeout(() => focusElement(document.querySelector<HTMLElement>(`[data-focus-key="${openerKey}"]`)), 0);
  }

  function seek(target: number) {
    const av = window.webapis?.avplay;
    const next = clampSeek(target, duration.current);
    try { av?.seekTo(next); current.current = next; setPosition(next); revealControls(); } catch { }
  }

  function scrub(delta: number) {
    const next = clampSeek((scrubTarget.current ?? current.current) + delta, duration.current); scrubTarget.current = next; setPosition(next); revealControls(true); focusControl('timeline'); window.clearTimeout(scrubTimer.current); scrubTimer.current = window.setTimeout(() => { const target = scrubTarget.current; scrubTarget.current = null; if (target !== null) seek(target); focusControl('timeline'); }, 550);
  }

  function refreshTracks() { const av = window.webapis?.avplay; try { const all = (av?.getTotalTrackInfo?.() || []).map(normalizeTrack); if (all.length) setTracks(all); } catch { } }

  function togglePlayback(force?: boolean) {
    const av = window.webapis?.avplay;
    try {
      const shouldPlay = force ?? !playing.current;
      if (shouldPlay) { av.play(); playing.current = true; setPhase('playing'); setMessage(''); controls.setPlaying(true); revealControls(); }
      else { av.pause(); playing.current = false; setPhase('paused'); setMessage('Paused'); controls.setPlaying(false); revealControls(); save(); }
    } catch { }
  }

  async function recover(error: string, token: number) {
    if (token !== session.current) return;
    const recoveryToken = ++session.current;
    stopAVPlay();
    let latest = liveDownload;
    try { latest = (await api.downloads()).items.find(item => item.id === download.id) || latest; setLiveDownload(latest); } catch { }
    const terminal = /error|missing|unavailable|unknown/i.test(latest.state);
    if (!isDownloadComplete(latest) && !terminal) {
      playing.current = false;
      controls.setPlaying(false);
      setPhase('waiting');
      setMessage('Waiting for the next downloaded segment…');
      revealControls(true);
      const poll = async () => {
        if (recoveryToken !== session.current) return;
        try {
          const next = (await api.downloads()).items.find(item => item.id === download.id);
          if (next) {
            setLiveDownload(next);
            if (/error|missing|unavailable|unknown/i.test(next.state)) { setPhase('failed'); setMessage(`Playback failed: ${next.error || next.state}`); return; }
            setMessage(`${next.playbackMode === 'progressive' ? 'Streaming while downloading' : 'Download complete'} · ${Math.round(next.progress * 100)}% · ${formatBytes(next.downloadedBytes)} / ${formatBytes(next.sizeBytes)}${next.speedBytesPerSecond > 0 ? ` · ${formatBytes(next.speedBytesPerSecond)}/s` : ''}${next.etaSeconds > 0 ? ` · ${formatTime(next.etaSeconds * 1000)} left` : ''}`);
            void openPlayer(current.current); return;
          }
        } catch { }
        pollTimer.current = window.setTimeout(poll, 2000);
      };
      pollTimer.current = window.setTimeout(poll, 2000);
      return;
    }
    if (isDownloadComplete(latest) && !completedRetryUsed.current) { completedRetryUsed.current = true; void openPlayer(current.current); return; }
    playing.current = false;
    controls.setPlaying(false);
    setPhase('failed');
    setMessage(`Playback failed: ${error}`);
    revealControls(true);
  }

  function openPlayer(startAt: number, shouldPlay = true) {
    const av = window.webapis?.avplay;
    if (!av) { setPhase('failed'); setMessage('AVPlay is unavailable on this device.'); return; }
    window.clearTimeout(pollTimer.current);
    const token = ++session.current;
    stopAVPlay();
    setPhase('opening'); setMessage(liveDownload.playbackMode === 'progressive' ? 'Opening progressive stream…' : 'Opening downloaded file…'); revealControls(true);
    try {
      av.open(api.streamURL(download.streamUrl));
      av.setDisplayRect(0, 0, 1920, 1080);
      av.setDisplayMethod(aspect);
      if (externalSubtitlePath.current) av.setExternalSubtitlePath(externalSubtitlePath.current);
      av.setListener({
        onbufferingstart: () => { if (token === session.current) { playing.current = false; setPhase('buffering'); setMessage('Buffering…'); controls.setPlaying(false); revealControls(true); } },
        onbufferingprogress: (progress: number) => { if (token === session.current) setMessage(`Buffering ${progress}%`); },
        onbufferingcomplete: () => { if (token === session.current) { playing.current = true; setPhase('playing'); setMessage(''); refreshTracks(); controls.setPlaying(true); revealControls(); } },
        onstreamcompleted: () => { if (token === session.current) { current.current = duration.current; void save().then(() => onComplete(preferenceRef.current)); } },
        oncurrentplaytime: (value: number) => { if (token === session.current) { current.current = value; if (scrubTarget.current === null) setPosition(value); if (subtitleCues.current.length) setExternalSubtitle(subtitleAt(subtitleCues.current, value, subtitleDelayRef.current)); if (Date.now() - lastSaved.current >= 10_000) { lastSaved.current = Date.now(); save(); } } },
        onsubtitlechange: (_duration: number, text: string) => { if (token === session.current) setExternalSubtitle(String(text || '')); },
        onerror: (error: string) => void recover(error, token),
      });
      av.prepareAsync(() => {
        if (token !== session.current) return;
        duration.current = av.getDuration(); setTotal(duration.current);
        const allTracks = (av.getTotalTrackInfo?.() || []).map(normalizeTrack); setTracks(allTracks); applyAudioPreference(allTracks);
        if (externalSubtitlePath.current) { try { av.setSilentSubtitle(false); } catch { } }
        else if (!autoSubtitleAttempted.current) { autoSubtitleAttempted.current = true; void autoSelectSubtitles(allTracks); }
        if (startAt > 0 && startAt < duration.current) av.seekTo(clampSeek(startAt, duration.current));
        if (shouldPlay) { av.play(); playing.current = true; setPhase('playing'); setMessage(''); controls.setPlaying(true); revealControls(); }
        else { playing.current = false; setPhase('paused'); setMessage('Paused'); controls.setPlaying(false); revealControls(); }
      }, (error: string) => void recover(error || 'AVPlay could not prepare this source.', token));
    } catch (error) { void recover((error as Error).message, token); }
  }

  useEffect(() => {
    let cancelled = false; void (async () => { try { preferenceRef.current = preferences || await api.playbackPreferences(download.id) } catch { } if (!cancelled) openPlayer(resumeMs) })();
    const focusTimer = window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('[data-player-control="play"]')), 0);
    return () => { cancelled = true; session.current++; window.clearTimeout(focusTimer); window.clearTimeout(pollTimer.current); controls.dispose(); window.clearTimeout(scrubTimer.current); window.clearTimeout(messageTimer.current); void save(); stopAVPlay(); };
  }, [download.id]);
  useEffect(() => {
    const remember = (event: FocusEvent) => {
      const target = event.target as HTMLElement | null;
      const key = target?.dataset.playerControl;
      if (key && key !== 'timeline') lastControlFocus.current = key;
    };
    const key = (event: KeyboardEvent) => {
      const action = playerAction(event.key, event.keyCode);
      if (!action) return;
      event.preventDefault();
      if (!controlsVisibleRef.current && !menuRef.current) {
        const hiddenRoute = hiddenKeyRoute(action);
        if (hiddenRoute === 'scrub-left') { revealControls(); scrub(-10_000); return; }
        if (hiddenRoute === 'scrub-right') { revealControls(); scrub(10_000); return; }
        if (hiddenRoute === 'refocus') { revealControls(); focusControl(); return; }
        revealControls();
      }
      if (action === 'back' || action === 'stop') { if (menuRef.current) closeMenu(); else { save(); onClose(); } return; }
      if (action === 'play') { togglePlayback(true); return; }
      if (action === 'pause') { togglePlayback(false); return; }
      if (action === 'play-pause') { togglePlayback(); return; }
      if (action === 'rewind' || action === 'previous') { scrub(-10_000); return; }
      if (action === 'fast-forward' || action === 'next') { scrub(10_000); return; }
      revealControls(menuRef.current !== null);
      const selector = menuRef.current ? '.player-dialog button' : '[data-player-control]';
      const elements = Array.from(document.querySelectorAll<HTMLElement>(selector)).filter(element => element.offsetWidth > 0);
      const active = document.activeElement as HTMLElement | null;
      if (action === 'enter') { if (active && elements.includes(active)) active.click(); else focusElement(elements[0] || null); return; }
      if (!menuRef.current && active?.dataset.playerControl === 'timeline' && (action === 'left' || action === 'right')) {
        scrub(action === 'left' ? -10_000 : 10_000);
        return;
      }
      if (!active || !elements.includes(active)) { focusElement(elements[0] || null); return; }
      if (menuRef.current && (action === 'left' || action === 'right')) return;
      if (!menuRef.current && active.dataset.playerControl === 'timeline' && action === 'down') { focusControl(lastControlFocus.current === 'timeline' ? 'play' : lastControlFocus.current); return; }
      if (!menuRef.current && active.dataset.focusRow === '1' && action === 'up') { focusElement(document.querySelector<HTMLElement>('[data-player-control="timeline"]')); return; }
      const target = chooseStructuredTarget(active, elements, action);
      if (target) focusElement(target);
    };
    addEventListener('focusin', remember);
    addEventListener('keydown', key);
    return () => { removeEventListener('focusin', remember); removeEventListener('keydown', key); };
  }, [download.id]);

  function chooseTrack(type: 'AUDIO' | 'TEXT', index: number | null) {
    const av = window.webapis?.avplay;
    try { if (type === 'TEXT' && index === null) { av.setSilentSubtitle(true); subtitleCues.current = []; setExternalSubtitle(''); } else { if (type === 'TEXT') { av.setSilentSubtitle(false); subtitleCues.current = []; setExternalSubtitle(''); } av.setSelectTrack(type, index); if (type === 'AUDIO') { const wasPlaying = playing.current; if (!wasPlaying) av.play(); window.setTimeout(() => { try { av.seekTo(clampSeek(current.current, duration.current)); if (!wasPlaying) av.pause(); refreshTracks(); } catch { } }, 120); } } } catch { }
    closeMenu();
    const selected = index === null ? null : tracks.find(track => track.type === type && track.index === index);
    if (type === 'AUDIO' && selected) void savePreferences({ ...preferenceRef.current, audioLanguage: selected.language || 'en', audioTrackIndex: selected.index });
    if (type === 'TEXT') void savePreferences({ ...preferenceRef.current, subtitleLanguage: selected?.language || preferenceRef.current.subtitleLanguage, subtitleProvider: selected ? 'native' : '', subtitleCandidateId: selected ? String(selected.index) : '', subtitleMode: selected ? 'selected' : 'off' });
    showTransientMessage(index === null ? 'Subtitles off' : `${selected?.label || (type === 'AUDIO' ? 'Audio track' : 'Subtitle')} selected`);
  }
  function applyAudioPreference(allTracks: AVTrack[]) { const wanted = preferenceRef.current; const selected = preferredAudio(allTracks, wanted.audioLanguage, wanted.audioTrackIndex); if (selected) try { window.webapis?.avplay.setSelectTrack('AUDIO', selected.index) } catch { } }
  async function autoSelectSubtitles(allTracks: AVTrack[]) { const saved = preferenceRef.current; if (saved.subtitleMode === 'off') { try { window.webapis?.avplay.setSilentSubtitle(true) } catch { } return } if (saved.subtitleMode === 'selected' && saved.subtitleProvider === 'native') { const track = allTracks.find(item => item.type === 'TEXT' && String(item.index) === saved.subtitleCandidateId); if (track) { try { window.webapis?.avplay.setSilentSubtitle(false); window.webapis?.avplay.setSelectTrack('TEXT', track.index); return } catch { } } } if (saved.subtitleMode === 'selected' && saved.subtitleProvider && saved.subtitleCandidateId) { if (await installSubtitle({ id: saved.subtitleCandidateId, provider: saved.subtitleProvider, language: saved.subtitleLanguage, title: 'Saved subtitle', score: 1000, cached: true }, false)) return } try { const local = await api.subtitles(download.id, saved.subtitleLanguage || 'ro', 'local'); setSubtitleCandidates(local.items); const ordered = [...local.items].sort((a, b) => subtitleRank(a.language, preferenceRef.current.subtitleLanguage || 'ro') - subtitleRank(b.language, preferenceRef.current.subtitleLanguage || 'ro')); for (const candidate of ordered) if (await installSubtitle(candidate, true)) return; const native = allTracks.filter(track => track.type === 'TEXT').sort((a, b) => subtitleRank(a.language, preferenceRef.current.subtitleLanguage || 'ro') - subtitleRank(b.language, preferenceRef.current.subtitleLanguage || 'ro'))[0]; if (native) { try { window.webapis?.avplay.setSilentSubtitle(false); window.webapis?.avplay.setSelectTrack('TEXT', native.index); await savePreferences({ ...saved, subtitleLanguage: native.language || 'en', subtitleProvider: 'native', subtitleCandidateId: String(native.index), subtitleMode: 'selected' }); return } catch { } } const remote = await api.subtitles(download.id, 'en', 'remote'); setSubtitleCandidates(current => [...current, ...remote.items]); for (const candidate of remote.items) if (await installSubtitle(candidate, true)) return; setMessage('No Romanian or English subtitle was found.') } catch (error) { setMessage(`Subtitles unavailable: ${(error as Error).message}`) } }
  async function findSubtitles(automatic = false, scope: 'local' | 'remote' | 'all' = 'remote') {
    if (!automatic && scope === 'remote') openMenu('find-subtitles', 'subtitles');
    setMessage(scope === 'local' ? 'Checking included subtitles…' : 'Searching for Romanian subtitles…');
    try {
      const page = await api.subtitles(download.id, 'ro', scope);
      const candidates = page.items;
      setSubtitleCandidates(candidates);
      if (automatic && candidates.length > 0) { await installSubtitle(candidates[0]); return; }
      setMessage(candidates.length > 0 ? '' : page.warnings?.length ? page.warnings.map(w => `${w.provider}: ${w.message}`).join(' · ') : automatic ? 'No included subtitle was found.' : 'No matching online subtitles were found.');
    } catch (error) { setMessage(`Subtitle search failed: ${(error as Error).message}`); }
  }
  async function installSubtitle(candidate: SubtitleCandidate, persist = true): Promise<boolean> {
    setMessage(`Preparing ${candidate.title}…`); if (menuRef.current) closeMenu(); else revealControls(true);
    try {
      const asset = await api.prepareSubtitle(download.id, candidate.provider, candidate.id, 'vtt');
      const response = await fetch(api.streamURL(asset.url)); if (!response.ok) throw new Error(`server returned ${response.status}`); const cues = parseVTT(await response.text()); if (!cues.length) throw new Error('the downloaded subtitle contained no readable cues'); subtitleCues.current = cues; externalSubtitlePath.current = ''; try { window.webapis?.avplay.setSilentSubtitle(true); } catch { } setExternalSubtitle(subtitleAt(cues, current.current, subtitleDelayRef.current)); if (persist) await savePreferences({ ...preferenceRef.current, subtitleLanguage: candidate.language || 'en', subtitleProvider: candidate.provider, subtitleCandidateId: candidate.id, subtitleMode: 'selected' }); showTransientMessage(`${candidate.language || 'Subtitle'} selected`); revealControls(); return true;
    } catch (error) { setMessage(`Subtitle preparation failed: ${(error as Error).message}`); return false; }
  }
  function changeDelay(delta: number) { const next = Math.max(-10_000, Math.min(10_000, subtitleDelay + delta)); setSubtitleDelay(next); try { window.webapis?.avplay.setSubtitlePosition(next); } catch { } }
  function changeAspect(value: string) { setAspect(value); try { window.webapis?.avplay.setDisplayMethod(value); } catch { } }
  const percent = total > 0 ? Math.min(100, position / total * 100) : liveDownload.progress * 100;
  const audioTracks = tracks.filter(track => track.type === 'AUDIO');
  const subtitleTracks = tracks.filter(track => track.type === 'TEXT');
  const subtitleRows = subtitleMenuRows(subtitleCandidates, 1, 'player-subtitle');
  const foundRows = subtitleMenuRows(subtitleCandidates, 0, 'player-found-subtitle');
  return <div class="player-shell"><object id="av-player" type="application/avplayer"></object><div class={`player ${controlsVisible ? 'controls-visible' : ''}`}>
    {externalSubtitle && <div class="external-subtitle">{externalSubtitle}</div>}
    {message && <div class="player-message" aria-live="polite">{message}</div>}
    <div class="player-controls">
      <div class="player-title">{download.filePath}</div>
      <button class="player-timeline" data-player-control="timeline" data-focus-region="player-controls" data-focus-row="0" data-focus-col="0" data-focus-key="player-timeline" onClick={() => { revealControls(true); focusControl('timeline') }} aria-label="Playback timeline; use left and right to seek"><span style={{ width: `${percent}%` }}></span></button>
      <div class="player-time"><span>{formatTime(position)}</span><span>{formatTime(total)}</span></div>
      <div class="player-toolbar">
        <button data-player-control="restart" data-focus-region="player-controls" data-focus-row="1" data-focus-col="0" data-focus-key="player-restart" onClick={() => keepControl('restart', () => seek(0))}>Restart</button><button data-player-control="back-10" data-focus-region="player-controls" data-focus-row="1" data-focus-col="1" data-focus-key="player-back-10" onClick={() => keepControl('back-10', () => seek(current.current - 10_000))}>−10s</button>
        <button data-player-control="play" data-focus-region="player-controls" data-focus-row="1" data-focus-col="2" data-focus-key="player-play" class="primary" onClick={() => keepControl('play', () => togglePlayback())}>{playing.current ? 'Pause' : 'Play'}</button><button data-player-control="forward-10" data-focus-region="player-controls" data-focus-row="1" data-focus-col="3" data-focus-key="player-forward-10" onClick={() => keepControl('forward-10', () => seek(current.current + 10_000))}>+10s</button>
        <button data-player-control="audio" data-focus-region="player-controls" data-focus-row="1" data-focus-col="4" data-focus-key="player-audio" onClick={() => openMenu('audio', 'audio')}>Audio ({audioTracks.length})</button><button data-player-control="subtitles" data-focus-region="player-controls" data-focus-row="1" data-focus-col="5" data-focus-key="player-subtitles" onClick={() => { openMenu('subtitles', 'subtitles'); void findSubtitles(false, 'local') }}>Subtitles ({subtitleTracks.length + subtitleCandidates.length})</button>
        <button data-player-control="options" data-focus-region="player-controls" data-focus-row="1" data-focus-col="6" data-focus-key="player-options" onClick={() => openMenu('options', 'options')}>Options</button><button data-player-control="back" data-focus-region="player-controls" data-focus-row="1" data-focus-col="7" data-focus-key="player-back" onClick={() => { save(); onClose(); }}>Back</button><button data-player-control="hide" data-focus-region="player-controls" data-focus-row="1" data-focus-col="8" data-focus-key="player-hide" onClick={hideControls}>Hide</button>
      </div>
      {phase === 'failed' && <button class="player-retry" data-player-control="retry" data-focus-region="player-controls" data-focus-row="2" data-focus-col="2" data-focus-key="player-retry" onClick={() => { completedRetryUsed.current = false; openPlayer(current.current); }}>Retry playback</button>}
    </div>
    {menu && <div class="player-dialog">
      {menu === 'audio' && <><h2>Audio track</h2><button data-focus-region="player-menu" data-focus-row="0" data-focus-col="0" data-focus-key="player-audio-refresh" onClick={refreshTracks}>Refresh tracks</button>{audioTracks.map((track, index) => <button data-focus-region="player-menu" data-focus-row={index + 1} data-focus-col="0" data-focus-key={`player-audio-${track.index}`} onClick={() => chooseTrack('AUDIO', track.index)}>{track.label}</button>)}</>}
      {menu === 'subtitles' && <><h2>Subtitles</h2><button data-focus-region="player-menu" data-focus-row="0" data-focus-col="0" data-focus-key="player-subtitles-off" onClick={() => chooseTrack('TEXT', null)}>Off</button>{subtitleRows.map(item => <Fragment key={item.key}>{item.heading ? <h3>{item.heading}</h3> : null}<button data-focus-region="player-menu" data-focus-row={item.row} data-focus-col="0" data-focus-key={item.key} onClick={() => void installSubtitle(item.candidate)}><strong>{subtitleItemLabel(item.candidate, item.position)}</strong>{item.detail ? <><br /><small>{item.detail}</small></> : null}</button></Fragment>)}{subtitleTracks.length > 0 && <><h3>Native AVPlay fallback</h3>{subtitleTracks.map((track, index) => <button data-focus-region="player-menu" data-focus-row={subtitleRows.length + index + 1} data-focus-col="0" data-focus-key={`player-native-subtitle-${track.index}`} onClick={() => chooseTrack('TEXT', track.index)}>{track.label}</button>)}</>}<button data-focus-region="player-menu" data-focus-row={subtitleRows.length + subtitleTracks.length + 1} data-focus-col="0" data-focus-key="player-subtitles-find" onClick={() => void findSubtitles(false, 'remote')}>Find online subtitles…</button></>}
      {menu === 'find-subtitles' && <><h2>Download subtitles</h2>{foundRows.length === 0 ? <p>No matching provider subtitles are available.</p> : foundRows.map(item => <Fragment key={item.key}>{item.heading ? <h3>{item.heading}</h3> : null}<button data-focus-region="player-menu" data-focus-row={item.row} data-focus-col="0" data-focus-key={item.key} onClick={() => void installSubtitle(item.candidate)}><strong>{subtitleItemLabel(item.candidate, item.position)}</strong>{item.detail ? <><br /><small>{item.detail}</small></> : null}{item.candidate.releaseName ? <><br /><small>{item.candidate.releaseName}</small></> : null}</button></Fragment>)}</>}
      {menu === 'options' && <><h2>Playback options</h2><button data-focus-region="player-menu" data-focus-row="0" data-focus-col="0" data-focus-key="player-subtitle-earlier" onClick={() => changeDelay(-500)}>Subtitle earlier (−0.5s)</button><button data-focus-region="player-menu" data-focus-row="1" data-focus-col="0" data-focus-key="player-subtitle-later" onClick={() => changeDelay(500)}>Subtitle later (+0.5s)</button><button data-focus-region="player-menu" data-focus-row="2" data-focus-col="0" data-focus-key="player-subtitle-reset" onClick={() => changeDelay(-subtitleDelay)}>Reset subtitle delay ({subtitleDelay / 1000}s)</button><button data-focus-region="player-menu" data-focus-row="3" data-focus-col="0" data-focus-key="player-aspect-auto" onClick={() => changeAspect('PLAYER_DISPLAY_MODE_AUTO_ASPECT_RATIO')}>Aspect: Auto</button><button data-focus-region="player-menu" data-focus-row="4" data-focus-col="0" data-focus-key="player-aspect-letterbox" onClick={() => changeAspect('PLAYER_DISPLAY_MODE_LETTER_BOX')}>Aspect: Letterbox</button><button data-focus-region="player-menu" data-focus-row="5" data-focus-col="0" data-focus-key="player-aspect-full" onClick={() => changeAspect('PLAYER_DISPLAY_MODE_FULL_SCREEN')}>Aspect: Full screen</button><button data-focus-region="player-menu" data-focus-row="6" data-focus-col="0" data-focus-key="player-info" onClick={() => openMenu('info', 'options')}>Playback information</button></>}
      {menu === 'info' && <><h2>Playback information</h2><p>{download.mimeType}</p><p>{formatBytes(download.sizeBytes)} · {tracks.length} tracks</p><p>{formatTime(position)} / {formatTime(total)} · {aspect.replace('PLAYER_DISPLAY_MODE_', '')}</p><button data-focus-region="player-menu" data-focus-row="0" data-focus-col="0" data-focus-key="player-info-back" onClick={() => switchMenu('options', 'player-info')}>Back to options</button></>}
      <button data-focus-region="player-menu" data-focus-row="99" data-focus-col="0" data-focus-key="player-menu-close" onClick={closeMenu}>Close</button>
    </div>}
  </div></div>;
}

function Setup({ draft, server, status, onDraft, onConnect, onForget }: { draft: string; server: string; status: string; onDraft: (value: string) => void; onConnect: (url: string) => void; onForget: () => void }) {
  const address = useRef<HTMLInputElement>(null);
  const connect = useRef<HTMLButtonElement>(null);
  const rescan = useRef<HTMLButtonElement>(null);
  const [manual, setManual] = useState(Boolean(server));
  const [searching, setSearching] = useState(false);
  const [results, setResults] = useState<DiscoveredServer[]>([]);
  const [discoveryStatus, setDiscoveryStatus] = useState('');
  const scan = async () => { if (searching) return; setSearching(true); setResults([]); try { const network = window.webapis?.network; if (!network?.getIp || !network?.getSubnetMask) throw new Error('Automatic discovery is unavailable on this device. Use Manual address.'); const ip = String(network.getIp() || ''); const mask = String(network.getSubnetMask() || ''); let customPort = 0; try { customPort = Number(new URL(normalizeServerURL(draft)).port || (/^https:/i.test(draft) ? 443 : 80)) } catch { } const found = await discoverServers(ip, mask, [8097, customPort], (done, total) => { if (done === total || done % 32 === 0) setDiscoveryStatus(`Searching your network… ${done} of ${total}`) }); setResults(found); setDiscoveryStatus(found.length ? `${found.length} Torrent TV server${found.length === 1 ? '' : 's'} found.` : 'No Torrent TV server was found. Check that it is running or use Manual address.') } catch (error) { setDiscoveryStatus((error as Error).message); setManual(true) } finally { setSearching(false) } };
  useEffect(() => { if (!server) void scan() }, []);
  useEffect(() => { if (!results.length) return; const timer = window.setTimeout(() => { if (document.activeElement === rescan.current) focusElement(document.querySelector<HTMLElement>('[data-focus-key="discovered-server-0"]')) }, 0); return () => window.clearTimeout(timer) }, [results.length]);
  useTVNavigation({ getInitialFocus: () => rescan.current || connect.current, inputExitTarget: () => connect.current, onBack: () => { if (manual && !server) { setManual(false); window.setTimeout(() => focusElement(rescan.current), 0) } else exitApplication() } });
  return <main class="setup"><h1>{appIdentity().name}</h1><p>Choose a Torrent TV server on this private network.</p><div class="setup-mode-actions"><button ref={rescan} data-focus-region="setup" data-focus-row="0" data-focus-col="0" data-focus-key="setup-rescan" class={!manual ? 'primary' : ''} disabled={searching} onClick={() => void scan()}>{searching ? 'Searching…' : 'Rescan'}</button><button data-focus-region="setup" data-focus-row="0" data-focus-col="1" data-focus-key="setup-manual" class={manual ? 'primary' : ''} onClick={() => setManual(value => !value)}>Manual address</button></div><p class="focus-hint" aria-live="polite">{discoveryStatus || 'Automatic discovery checks only the television’s local network.'}</p>{results.length > 0 && <section class="setup-results" aria-label="Discovered servers">{results.map((item, index) => <button data-focus-region="setup" data-focus-row={1 + index} data-focus-col="0" data-focus-key={`discovered-server-${index}`} onClick={() => onConnect(item.url)}><strong>{item.info.instanceName || item.info.name}</strong><span>{item.url}</span><small>Version {item.info.version}{item.info.configured ? ' · Ready' : ' · Setup required'}</small></button>)}</section>}{manual && <section class="setup-manual"><h2>Manual address</h2><div class="setup-controls"><input ref={address} readOnly data-focus-region="setup" data-focus-row="40" data-focus-col="0" data-focus-key="setup-address" type="url" inputMode="url" autoComplete="off" spellcheck={false} value={draft} onInput={event => onDraft(event.currentTarget.value)} placeholder="http://server.lan:8097" /><button ref={connect} data-focus-region="setup" data-focus-row="40" data-focus-col="1" data-focus-key="setup-connect" class="primary" onClick={() => onConnect(draft)}>Connect</button></div><p class="focus-hint">Select the address and press OK to edit it. Select Connect when ready.</p></section>}<p class="setup-status" aria-live="polite">{status}</p>{server && <button data-focus-region="setup" data-focus-row="41" data-focus-col="0" data-focus-key="setup-forget" onClick={onForget}>Forget saved server</button>}</main>;
}

type TVRoute = 'home' | 'search' | 'library' | 'continue' | 'favorites' | 'watched' | 'downloads' | 'library-categories' | 'tracker' | 'browse' | 'categories' | 'jobs' | 'events' | 'settings';
const menuGroups: Array<{ label: string; items: Array<{ id: TVRoute; label: string; icon: string }> }> = [
  { label: '', items: [{ id: 'home', label: 'Home', icon: '⌂' }, { id: 'search', label: 'Search', icon: '⌕' }] },
  { label: 'My Library', items: [{ id: 'library', label: 'Dashboard', icon: '◫' }, { id: 'continue', label: 'Continue watching', icon: '▶' }, { id: 'favorites', label: 'Favorites', icon: '★' }, { id: 'watched', label: 'Watched', icon: '✓' }, { id: 'downloads', label: 'Downloads', icon: '↓' }, { id: 'library-categories', label: 'Categories', icon: '≡' }] },
  { label: 'Tracker', items: [{ id: 'tracker', label: 'Dashboard', icon: '◉' }, { id: 'browse', label: 'Browse', icon: '▦' }, { id: 'categories', label: 'Categories', icon: '≡' }] },
  { label: '', items: [{ id: 'jobs', label: 'Jobs', icon: '↻' }, { id: 'events', label: 'Events', icon: '!' }, { id: 'settings', label: 'Settings', icon: '⚙' }] },
];

function Catalog({ api, status, titles, facets, household, downloads, jobs, restoreFocus, portal, updateStatus, onUpdateStatus, onFocus, onRetry, onChangeServer, onForgetServer, onPlay, onPlayDownload, onManageDownload, onManageSeasonPack, onRefreshDownloads, onFavorite }: { api: API; status: string; titles: CatalogTitle[]; facets: CatalogFacets; household: HouseholdState; downloads: Download[]; jobs: Job[]; restoreFocus: string | null; portal: PortalState | null; updateStatus: UpdateStatus | null; onUpdateStatus: (status: UpdateStatus) => void; onFocus: (key: string) => void; onRetry: () => void; onChangeServer: () => void; onForgetServer: () => void; onPlay: (release: Release, fileIndex?: number, resumeMs?: number) => void; onPlayDownload: (download: Download) => void; onManageDownload: (download: Download, action: string) => Promise<void>; onManageSeasonPack: (source: CatalogSource, season: number, action: SeasonPackAction) => Promise<void>; onRefreshDownloads: () => Promise<void>; onFavorite: (title: CatalogTitle, value: boolean) => void }) {
  const [route, setRoute] = useState<TVRoute>('home');
  const [menuOpen, setMenuOpen] = useState(false);
  const [projectsOpen, setProjectsOpen] = useState(false);
  const [updateConfirm, setUpdateConfirm] = useState(false);
  const projectsReturnKey = useRef<string | null>(null);
  const updateReturnKey = useRef<string | null>(null);
  const [draftQuery, setDraftQuery] = useState('');
  const [query, setQuery] = useState('');
  const [searching, setSearching] = useState(false);
  const [category, setCategory] = useState('');
  const [sort, setSort] = useState<'newest' | 'seeders' | 'title' | 'rating'>('newest');
  const [remoteTitles, setRemoteTitles] = useState(titles.slice(0, 12));
  const [pageCursor, setPageCursor] = useState('');
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [previousCursors, setPreviousCursors] = useState<string[]>([]);
  const [pageNumber, setPageNumber] = useState(1);
  const [detail, setDetail] = useState<CatalogDetail | null>(null);
  const [detailTarget, setDetailTarget] = useState<DetailTarget>({});
  const [detailMessage, setDetailMessage] = useState('');
  const first = useRef<HTMLButtonElement>(null);
  const lastContent = useRef<string | null>(restoreFocus);
  const detailRef = useRef<CatalogDetail | null>(null);
  const refreshDownloadsRef = useRef(onRefreshDownloads);
  detailRef.current = detail;
  refreshDownloadsRef.current = onRefreshDownloads;
  const favoriteIDs = new Set(household.favorites.map(item => item.titleId || item.catalog?.id).filter(Boolean));
  useEffect(() => setRemoteTitles(current => current.map(item => titles.find(updated => updated.id === item.id) || item)), [titles]);
  const fetchPage = (cursor = '', remember = false) => api.titles({ search: query.trim().length >= 3 ? query.trim() : undefined, category, sort, pageSize: 12, cursor }).then(page => { if (remember) setPreviousCursors(current => [...current, pageCursor].slice(-20)); setPageCursor(cursor); setRemoteTitles(page.items); setNextCursor(page.nextCursor); void api.ensureMetadata(page.items.map(item => item.id)); return page; });
  useEffect(() => { setPreviousCursors([]); setPageNumber(1); void fetchPage('').catch(() => { }) }, [query, category, sort]);
  useEffect(() => { const refresh = (event: Event) => { const searched = String((event as CustomEvent).detail?.query || '').toLowerCase(); if (query && searched === query.toLowerCase()) { setDetailMessage('FileList search completed.'); void fetchPage('').catch(error => setDetailMessage((error as Error).message)); } }; window.addEventListener('catalog-search-completed', refresh); return () => window.removeEventListener('catalog-search-completed', refresh) }, [query, category, sort]);
  const submitSearch = async () => { const value = draftQuery.trim(); setSearching(true); try { if (value) { const page = await api.searchTitles(value); setRemoteTitles(page.items); setNextCursor(page.nextCursor); void api.ensureMetadata(page.items.map(item => item.id)).catch(() => { }); } setQuery(value); setPreviousCursors([]); setPageNumber(1); } catch (error) { setDetailMessage((error as Error).message); } finally { setSearching(false) } };
  const nextPage = () => { if (!nextCursor) return; void fetchPage(nextCursor, true).then(() => setPageNumber(value => value + 1)).catch(() => { }) };
  const previousPage = () => { const cursor = previousCursors[previousCursors.length - 1]; if (cursor === undefined) return; setPreviousCursors(current => current.slice(0, -1)); void fetchPage(cursor).then(() => setPageNumber(value => Math.max(1, value - 1))).catch(() => { }) };
  useEffect(() => { const updated = (event: Event) => { const titleId = String((event as CustomEvent).detail?.titleId || ''); if (!titleId || detailRef.current?.title.id !== titleId) return; void api.title(titleId).then(next => { setDetail(next); setDetailMessage(needsEpisodeExpansion(next) ? 'Preparing the individual episode list…' : '') }).catch(error => setDetailMessage((error as Error).message)) }; window.addEventListener('catalog-title-updated', updated); return () => window.removeEventListener('catalog-title-updated', updated) }, [api]);
  useEffect(() => { const titleId = detail?.title.kind === 'series' ? detail.title.id : ''; if (!titleId) return; let stopped = false; let running = false; const refresh = async () => { if (stopped || running) return; running = true; try { const next = await api.title(titleId); if (!stopped && detailRef.current?.title.id === titleId) { setDetail(next); setDetailMessage(needsEpisodeExpansion(next) ? 'Preparing the individual episode list…' : '') } } catch (error) { if (!stopped) setDetailMessage((error as Error).message) } finally { running = false } }; const timer = window.setInterval(() => void refresh(), 3000); return () => { stopped = true; window.clearInterval(timer) } }, [api, detail?.title.id]);
  useEffect(() => { if (route !== 'downloads') return; let stopped = false; let running = false; const refresh = async () => { if (stopped || running) return; running = true; try { await refreshDownloadsRef.current() } finally { running = false } }; void refresh(); const timer = window.setInterval(() => void refresh(), 3000); return () => { stopped = true; window.clearInterval(timer) } }, [route]);
  const openTitle = async (title: CatalogTitle, target: DetailTarget = {}) => { setDetailMessage('Loading versions…'); setDetailTarget(target); try { const next = await api.title(title.id); setDetail(next); if (needsEpisodeExpansion(next)) { setDetailMessage('Preparing the individual episode list…'); void api.refreshTitle(title.id, title.title).catch(error => setDetailMessage((error as Error).message)) } else setDetailMessage(''); } catch (error) { setDetailMessage((error as Error).message); } };
  const openLibraryItem = async (item: HouseholdItem) => { const id = item.titleId || item.catalog?.id; if (!id) { setDetailMessage('This library item is not linked to a catalog title yet. Refresh the catalog and try again.'); return } const title = item.catalog || { id, title: item.release.name, kind: 'movie', categories: [], resolutions: [], sourceCount: 1, bestSeeders: item.release.seeders, largestSizeBytes: item.release.sizeBytes } as CatalogTitle; await openTitle(title, { season: item.seasonNumber, episode: item.episodeNumber }) };
  const manageSeasonPack = async (source: CatalogSource, season: number, action: SeasonPackAction) => { setDetailMessage(action === 'download' ? `Starting season ${season}…` : `Updating season ${season} download…`); try { await onManageSeasonPack(source, season, action); if (detailRef.current) { const next = await api.title(detailRef.current.title.id); setDetail(next); setDetailTarget({ season }) } setDetailMessage(action === 'delete' ? `Season ${season} download deleted.` : action === 'pause' ? `Season ${season} download paused.` : action === 'resume' ? `Season ${season} download resumed.` : `Season ${season} is downloading. Episode tiles will update here.`) } catch (error) { setDetailMessage((error as Error).message); throw error } };
  const chooseRoute = (next: TVRoute) => { setRoute(next); setDetail(null); setMenuOpen(false); window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('[data-focus-region="content"]')), 0); };
  useEffect(() => { if (projectsOpen && portal !== null && portal.links.length === 0) closeProjects(false) }, [projectsOpen, portal]);
  useEffect(() => { if (confirmDialogStale(updateConfirm, updateStatus)) setUpdateConfirm(false) }, [updateConfirm, updateStatus]);
  useEffect(() => {
    if (document.activeElement instanceof HTMLElement && document.activeElement !== document.body) return;
    const timer = window.setTimeout(() => {
      const active = document.activeElement;
      if (active instanceof HTMLElement && active !== document.body) return;
      const restored = lastContent.current ? document.querySelector<HTMLElement>(`[data-focus-key="${CSS.escape(lastContent.current)}"]`) : null;
      focusElement(restored || document.querySelector<HTMLElement>('[data-focus-region="content"]'));
    }, 0);
    return () => window.clearTimeout(timer);
  }, [portal, updateStatus]);
  const restoreAfterDialog = (key: string) => { const target = document.querySelector<HTMLElement>(`[data-focus-key="${CSS.escape(key)}"]`); window.setTimeout(() => focusElement(target || document.querySelector<HTMLElement>('[data-focus-region="content"]')), 0) };
  const openProjects = () => { projectsReturnKey.current = document.activeElement instanceof HTMLElement ? document.activeElement.dataset.focusKey || null : null; setProjectsOpen(true); window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('[data-focus-key="projects-close"]')), 0) };
  const closeProjects = (restore = true) => { setProjectsOpen(false); if (restore) restoreAfterDialog(dialogRestoreKey(projectsReturnKey.current, 'menu-projects')) };
  const openUpdateConfirm = () => { updateReturnKey.current = document.activeElement instanceof HTMLElement ? document.activeElement.dataset.focusKey || null : null; setUpdateConfirm(true); window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('[data-focus-key="update-confirm-cancel"]')), 0) };
  const closeUpdateConfirm = () => { setUpdateConfirm(false); restoreAfterDialog(dialogRestoreKey(updateReturnKey.current, 'update-apply')) };
  const onBack = () => { if (projectsOpen) { closeProjects(); return; } if (updateConfirm) { closeUpdateConfirm(); return; } if (detail) { setDetail(null); window.setTimeout(() => focusElement(document.querySelector<HTMLElement>(lastContent.current ? `[data-focus-key="${lastContent.current}"]` : '[data-focus-region="content"]')), 0); return; } if (menuOpen) { setMenuOpen(false); window.setTimeout(() => focusElement(lastContent.current ? document.querySelector<HTMLElement>(`[data-focus-key="${lastContent.current}"]`) : document.querySelector<HTMLElement>('[data-focus-region="content"]')), 0); return; } setMenuOpen(true); window.setTimeout(() => focusElement(document.querySelector<HTMLElement>(`[data-menu-route="${route}"]`)), 0); };
  useTVNavigation({
    getInitialFocus: () => first.current || document.querySelector<HTMLElement>('[data-focus-region="content"]'), restoreKey: restoreFocus, onFocusKey: key => { onFocus(key); if (document.activeElement instanceof HTMLElement && document.activeElement.dataset.focusRegion === 'content') lastContent.current = key; }, onBack, onLongBack: exitApplication, onDirection: (direction, current) => {
      if (detail || projectsOpen || updateConfirm) return false;
      if (route === 'events' && current.dataset.focusKey === 'event-latest' && direction === 'down') { focusElement(document.querySelector<HTMLElement>('[data-focus-key="event-rebuild"]')); return true; }
      if (route === 'events' && current.dataset.focusKey === 'event-rebuild' && direction === 'up') { focusElement(document.querySelector<HTMLElement>('[data-focus-key="event-latest"]')); return true; }
      const region = current.dataset.focusRegion;
      if (region === 'content' && direction === 'left' && current.dataset.focusCol === '0') { setMenuOpen(true); window.setTimeout(() => focusElement(document.querySelector<HTMLElement>(`[data-menu-route="${route}"]`) || document.querySelector<HTMLElement>('[data-focus-region="sidebar"]')), 0); return true; }
      if (region === 'sidebar' && direction === 'right') { setMenuOpen(false); window.setTimeout(() => focusElement(lastContent.current ? document.querySelector<HTMLElement>(`[data-focus-key="${lastContent.current}"]`) : document.querySelector<HTMLElement>('[data-focus-region="content"]')), 0); return true; }
      return false;
    }
  });
  const librarySections = householdSections(route, household);
  let visible = remoteTitles;
  if (category) visible = visible.filter(title => title.categories.includes(category));
  if (sort === 'seeders') visible = [...visible].sort((a, b) => b.bestSeeders - a.bestSeeders); if (sort === 'title') visible = [...visible].sort((a, b) => a.title.localeCompare(b.title)); if (sort === 'rating') visible = [...visible].sort((a, b) => Number(Boolean(b.ratingVotes)) - Number(Boolean(a.ratingVotes)) || (b.rating || 0) - (a.rating || 0));
  const heading = menuGroups.reduce<Array<{ id: TVRoute; label: string; icon: string }>>((items, group) => items.concat(group.items), []).find(item => item.id === route)?.label || 'Home';
  const hero = visible[0] || titles[0];
  const rows: Array<{ key: string; title: string; items: CatalogTitle[] }> = route === 'home' ? [
    { key: 'new', title: 'Recently added', items: titles.slice(0, 12) },
  ] : route === 'tracker' ? [{ key: 'tracker-new', title: 'Recently added', items: visible.slice(0, 6) }, { key: 'tracker-seeded', title: 'Strong swarms', items: [...visible].sort((a, b) => b.bestSeeders - a.bestSeeders).slice(0, 6) }] : ['continue', 'favorites', 'watched', 'library', 'downloads', 'library-categories', 'jobs', 'events', 'settings', 'categories'].includes(route) ? [] : [{ key: route, title: heading, items: visible.slice(0, 12) }];
  if (detail) return <TitleDetail key={`${detail.title.id}:${detailTarget.season || 0}:${detailTarget.episode || 0}`} api={api} detail={detail} target={detailTarget} message={detailMessage} resume={resumeForTitle(household.continueWatching, detail.title.id)} favorite={favoriteIDs.has(detail.title.id)} onClose={onBack} onFavorite={onFavorite} onResume={item => onPlay(item.release, item.fileIndex, item.positionMs)} onPlay={onPlay} onPackAction={manageSeasonPack} />;
  return <div class={`tv-app ${menuOpen ? 'menu-open' : ''}`}>
    <aside class="tv-sidebar"><div class="tv-brand"><span>{appIdentity().monogram}</span><b>{appIdentity().name}</b></div>{menuGroups.map((group, groupIndex) => <div class="tv-menu-group">{group.label && <small>{group.label}</small>}{group.items.map((item, index) => <button data-menu-route={item.id} data-focus-region="sidebar" data-focus-row={groupIndex * 10 + index} data-focus-col="0" data-focus-key={`menu-${item.id}`} class={route === item.id ? 'active' : ''} onClick={() => chooseRoute(item.id)}><i>{item.icon}</i><span>{item.label}</span></button>)}</div>)}{portal && portal.links.length > 0 && <button data-menu-route="projects" data-focus-region="sidebar" data-focus-row={PROJECTS_MENU_ROW} data-focus-col="0" data-focus-key="menu-projects" onClick={openProjects}><i>↗</i><span>Other projects</span></button>}</aside>
    <main class="tv-content">
      <header class="tv-top"><div><small>{route === 'home' ? 'PRIVATE SCREENING ARCHIVE' : appIdentity().name.toUpperCase()}</small><h1>{heading}</h1></div><span aria-live="polite">{status}</span><button data-focus-region="content" data-focus-row="0" data-focus-col="0" data-focus-key="header-retry" onClick={onRetry}>Refresh</button></header>
      {route === 'search' && <div class="tv-search"><input readOnly data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="search-input" value={draftQuery} onInput={event => setDraftQuery(event.currentTarget.value)} placeholder="Search FileList; press OK to type" /><button data-focus-region="content" data-focus-row="1" data-focus-col="1" data-focus-key="search-submit" class="primary" disabled={searching} onClick={() => void submitSearch()}>{searching ? 'Searching…' : 'Search'}</button>{query && <button data-focus-region="content" data-focus-row="1" data-focus-col="2" data-focus-key="search-clear" onClick={() => { setDraftQuery(''); setQuery('') }}>Clear</button>}</div>}
      {['tracker', 'browse', 'categories', 'search'].includes(route) && <div class="tv-filters"><button data-focus-region="content" data-focus-row="2" data-focus-col="0" data-focus-key="sort-newest" class={sort === 'newest' ? 'active' : ''} onClick={() => setSort('newest')}>Newest</button><button data-focus-region="content" data-focus-row="2" data-focus-col="1" data-focus-key="sort-seeders" class={sort === 'seeders' ? 'active' : ''} onClick={() => setSort('seeders')}>Most seeded</button><button data-focus-region="content" data-focus-row="2" data-focus-col="2" data-focus-key="sort-rating" class={sort === 'rating' ? 'active' : ''} onClick={() => setSort('rating')}>Rating</button><button data-focus-region="content" data-focus-row="2" data-focus-col="3" data-focus-key="sort-title" class={sort === 'title' ? 'active' : ''} onClick={() => setSort('title')}>A–Z</button>{category && <button data-focus-region="content" data-focus-row="2" data-focus-col="4" data-focus-key="clear-category" onClick={() => setCategory('')}>Clear {category}</button>}</div>}
      {route === 'settings' ? <TVSettings api={api} onChangeServer={onChangeServer} onForgetServer={onForgetServer} updateStatus={updateStatus} onUpdateStatus={onUpdateStatus} confirmOpen={updateConfirm} onConfirmOpen={openUpdateConfirm} onConfirmClose={closeUpdateConfirm} /> : route === 'events' ? <TVEvents api={api} /> : route === 'downloads' ? <TVDownloads items={downloads} onPlay={onPlayDownload} onManage={onManageDownload} /> : route === 'library-categories' ? <TVLibraryCategories api={api} onOpen={openLibraryItem} /> : route === 'jobs' ? <TVJobs api={api} items={jobs} /> : route === 'categories' ? <section class="tv-category-grid">{trackerCategories(facets).map((name, index) => <button data-focus-region="content" data-focus-row={3 + Math.floor(index / 4)} data-focus-col={index % 4} data-focus-key={`category-${name}`} onClick={() => { setCategory(name); setRoute('browse') }}><strong>{name}</strong><span>Browse titles</span></button>)}</section> : <>
        {route === 'home' && hero && <section class="tv-hero" style={hero.backdropUrl ? { backgroundImage: `linear-gradient(90deg,#090d10 3%,rgba(9,13,16,.82) 42%,rgba(9,13,16,.15)),url(${api.streamURL(hero.backdropUrl)})` } : undefined}><div><span class="eyebrow">{hero.kind === 'series' ? 'Series' : 'Movie'} · {hero.year || 'Year unknown'}</span><h2>{hero.title}</h2><p>{hero.overview || `${hero.sourceCount} available version${hero.sourceCount === 1 ? '' : 's'} · up to ${hero.bestSeeders} seeders`}</p><button ref={first} data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key={`hero-${hero.id}`} class="primary" onClick={() => void openTitle(hero)}>View versions</button></div></section>}
        {librarySections.filter(section => route !== 'home' || section.key === 'continue').map((section, index) => <HouseholdRail api={api} title={section.title} items={section.items} row={3 + index} onOpen={openLibraryItem} />)}
        <div class="tv-rows">{rows.filter(row => row.items.length > 0).map((row, rowIndex) => <section><div class="row-heading"><h2>{row.title}</h2><span>{row.items.length} titles</span></div><div class="poster-rail">{row.items.map((title, col) => <TitleCard api={api} title={title} row={rowIndex + 10} col={col} focusRef={!hero && rowIndex === 0 && col === 0 ? first : undefined} onOpen={() => void openTitle(title)} />)}</div></section>)}</div>
        {route === 'home' && librarySections.filter(section => section.key === 'favorites').map(section => <HouseholdRail api={api} title={section.title} items={section.items} row={20} onOpen={openLibraryItem} />)}
        {['search', 'browse'].includes(route) && <nav class="tv-pager" aria-label="Catalog pages"><button disabled={previousCursors.length === 0} data-focus-region="content" data-focus-row="90" data-focus-col="0" data-focus-key="page-previous" onClick={previousPage}>Previous</button><span>Page {pageNumber}</span><button disabled={!nextCursor} data-focus-region="content" data-focus-row="90" data-focus-col="1" data-focus-key="page-next" onClick={nextPage}>Next</button></nav>}
        {['search', 'browse', 'tracker'].includes(route) && rows.every(row => row.items.length === 0) && <div class="tv-empty"><h2>Nothing here yet</h2><p>Try another section or refresh the catalog.</p></div>}
      </>}
    </main>
    {route === 'home' && promotionsVisible(portal) && <TVPromotions api={api} />}
    {projectsOpen && <section role="dialog" aria-modal="true" aria-labelledby="tv-projects-heading" class="tv-settings tv-projects-dialog"><h2 id="tv-projects-heading">Other projects</h2><p>Project sites published with this server. The full address of each one is shown below its name.</p>{portal?.links.map(link => <div class="tv-project" key={link.id}><strong>{link.title}</strong>{link.description && <p>{link.description}</p>}<code>{link.url}</code></div>)}<button data-focus-region={PROJECTS_DIALOG_REGION} data-focus-row="0" data-focus-col="0" data-focus-key="projects-close" onClick={() => closeProjects()}>Close</button></section>}
  </div>;
}

function HouseholdRail({ api, title, items, row, onOpen }: { api: API; title: string; items: HouseholdState['continueWatching']; row: number; onOpen: (item: HouseholdItem) => void }) {
  if (items.length === 0) return <section><div class="row-heading"><h2>{title}</h2></div><p class="tv-muted">Nothing here yet.</p></section>;
  return <section><div class="row-heading"><h2>{title}</h2><span>{items.length} item{items.length === 1 ? '' : 's'}</span></div><div class="poster-rail">{items.map((item, col) => {
    const label = item.catalog?.title || item.release.name;
    const progress = item.durationMs > 0 ? Math.max(0, Math.min(100, Math.round(item.positionMs / item.durationMs * 100))) : 0;
    const metadata = [item.catalog?.year, item.catalog?.resolutions?.[0], item.release.category].filter(Boolean).join(' · ');
    const status = item.watched ? 'Watched' : item.positionMs > 0 ? `${progress}% watched` : 'Ready to play';
    return <button class="poster-card library-poster-card" data-focus-region="content" data-focus-row={row} data-focus-col={col} data-focus-key={`library-${item.titleId || item.catalog?.id || item.release.id}-${item.seasonNumber || 0}-${item.episodeNumber || 0}-${col}`} onClick={() => onOpen(item)}>{item.catalog?.posterUrl ? <img src={api.streamURL(item.catalog.posterUrl)} alt="" loading="lazy" /> : <div class="poster-fallback">{label.slice(0, 1)}</div>}<TVStateBadges state={item.catalog?.libraryState} /><div class="poster-copy"><strong>{label}</strong><span>{metadata || status}</span><small>{item.seasonNumber && item.episodeNumber ? `S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')} · ` : ''}{metadata ? status : item.filePath || item.release.name}</small></div>{item.positionMs > 0 && !item.watched && <i class="tv-card-progress" aria-label={`${progress}% watched`}><b style={{ width: `${progress}%` }} /></i>}</button>
  })}</div></section>
}

function TVDownloads({ items, onPlay, onManage }: { items: Download[]; onPlay: (download: Download) => void; onManage: (download: Download, action: string) => Promise<void> }) {
  const [pending, setPending] = useState<Download | null>(null);
  const [removing, setRemoving] = useState(false);
  const [busyAction, setBusyAction] = useState('');
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState<'all' | 'streaming' | 'complete' | 'paused' | 'errors'>('all');
  const [sort, setSort] = useState<DownloadSort>('recent');
  const [order, setOrder] = useState<string[]>(() => orderDownloadIDs(items, 'recent'));
  const cancelButton = useRef<HTMLButtonElement>(null);
  const confirmButton = useRef<HTMLButtonElement>(null);
  const closeConfirmation = () => { if (removing) return; const key = pending ? `download-${pending.id}-delete` : ''; setPending(null); window.setTimeout(() => focusElement(document.querySelector<HTMLElement>(`[data-focus-key="${key}"]`) || document.querySelector<HTMLElement>('[data-focus-key="downloads-search"]')), 0) };
  useEffect(() => {
    if (!pending) return;
    const timer = removing ? 0 : window.setTimeout(() => focusElement(cancelButton.current), 0);
    const key = (event: KeyboardEvent) => {
      const action = remoteAction(event.key, event.keyCode);
      if (!action || action === 'ime-done' || action === 'ime-cancel') return;
      event.preventDefault();
      event.stopImmediatePropagation();
      if (removing) return;
      if (action === 'back') { closeConfirmation(); return; }
      if (action === 'up') { focusElement(cancelButton.current); return; }
      if (action === 'down') { focusElement(confirmButton.current); return; }
      if (action === 'left' || action === 'right') return;
      const active = document.activeElement;
      if (active instanceof HTMLButtonElement && (active === cancelButton.current || active === confirmButton.current)) active.click();
      else focusElement(cancelButton.current);
    };
    document.addEventListener('keydown', key, true);
    return () => { window.clearTimeout(timer); document.removeEventListener('keydown', key, true) };
  }, [pending?.id, removing]);
  const idsKey = items.map(item => item.id).join('\u0000');
  useEffect(() => setOrder(current => { const available = new Set(items.map(item => item.id)); const retained = current.filter(id => available.has(id)); const known = new Set(retained); const added = items.filter(item => !known.has(item.id)).map(item => item.id); return [...added, ...retained] }), [idsKey]);
  const visible = useMemo(() => { const term = query.trim().toLocaleLowerCase(); const byID = new Map(items.map(item => [item.id, item])); const ordered = (order.length ? order : items.map(item => item.id)).map(id => byID.get(id)).filter((item): item is Download => Boolean(item)); return ordered.filter(download => { const text = [download.displayTitle, download.releaseName, download.filePath, download.category, download.state].filter(Boolean).join(' ').toLocaleLowerCase(); const matchesFilter = filter === 'all' || filter === 'streaming' && download.playbackMode === 'progressive' || filter === 'complete' && download.playbackMode === 'local' || filter === 'paused' && /^(paused|stopped)/.test(download.state.toLocaleLowerCase()) || filter === 'errors' && Boolean(download.error); return (!term || text.includes(term)) && matchesFilter }) }, [items, order, query, filter]);
  const cycle = <T extends string,>(value: T, values: T[], set: (value: T) => void) => set(values[(values.indexOf(value) + 1) % values.length]);
  const cycleSort = () => { const values: DownloadSort[] = ['recent', 'title', 'progress', 'size', 'speed']; const next = values[(values.indexOf(sort) + 1) % values.length]; setSort(next); setOrder(orderDownloadIDs(items, next)) };
  const runAction = async (download: Download, action: DownloadTransferAction) => { if (busyAction) return; setBusyAction(`${download.id}:${action}`); try { await onManage(download, action) } finally { setBusyAction('') } };
  const confirmRemoval = async () => { if (!pending || removing) return; setRemoving(true); try { await onManage(pending, 'remove'); setPending(null); window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('[data-focus-key="downloads-search"]')), 0) } finally { setRemoving(false) } };
  if (pending) return <section role="dialog" aria-modal="true" aria-labelledby="download-delete-heading" aria-describedby="download-delete-description" class="tv-settings tv-removal-confirm"><h2 id="download-delete-heading">Delete download?</h2><strong>{pending.releaseName || pending.filePath}</strong><p>Selected file: {pending.filePath}</p><p>{formatBytes(pending.sizeBytes)} · FileList release {pending.releaseId}</p><p id="download-delete-description">This removes the torrent from qBittorrent and permanently deletes its incomplete and downloaded files.</p><button ref={cancelButton} disabled={removing} data-focus-region="download-dialog" data-focus-row="1" data-focus-col="0" data-focus-key="download-cancel" onClick={closeConfirmation}>Cancel</button><button ref={confirmButton} disabled={removing} class="danger-button" data-focus-region="download-dialog" data-focus-row="2" data-focus-col="0" data-focus-key="download-confirm" onClick={() => void confirmRemoval()}>{removing ? 'Deleting…' : 'Delete download'}</button></section>;
  const filterLabel = { all: 'All', streaming: 'Still downloading', complete: 'Downloaded', paused: 'Paused', errors: 'Needs attention' }[filter];
  const sortLabel = { recent: 'Recent', title: 'Title A–Z', progress: 'Progress', size: 'File size', speed: 'Speed' }[sort];
  return <section class="tv-list tv-download-list"><h2>Application downloads</h2><div class="tv-filters tv-download-tools"><input readOnly data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="downloads-search" value={query} onInput={event => setQuery(event.currentTarget.value)} placeholder="Search downloads; press OK to type" /><button data-focus-region="content" data-focus-row="1" data-focus-col="1" data-focus-key="downloads-filter" onClick={() => cycle(filter, ['all', 'streaming', 'complete', 'paused', 'errors'], setFilter)}>Filter: {filterLabel}</button><button data-focus-region="content" data-focus-row="1" data-focus-col="2" data-focus-key="downloads-sort" onClick={cycleSort}>Sort: {sortLabel}</button></div><p class="tv-download-count" aria-live="polite">{visible.length} of {items.length} downloads shown</p>{visible.length === 0 ? <p>{items.length === 0 ? 'No managed downloads yet.' : 'No downloads match this search and filter.'}</p> : visible.map((download, index) => <article key={download.id} data-download-id={download.id} class="tv-download-row"><div><strong title={download.displayTitle || download.filePath}>{download.displayTitle || download.filePath}</strong><span class="tv-release-name" title={download.releaseName || download.filePath}>{download.releaseName || download.filePath}</span><span class={`tv-stream-mode ${download.playbackMode}`}>{download.playbackMode === 'progressive' ? 'Progressive stream' : 'Downloaded file'}</span><small>{[download.parsed?.resolution, download.parsed?.quality, download.parsed?.videoCodec, download.parsed?.audio, download.category].filter(Boolean).join(' · ') || 'Source details unavailable'}</small><small title={download.filePath}>Selected file: {download.filePath} · index {download.fileIndex} · {formatBytes(download.sizeBytes)}</small><small>Complete torrent: FileList release {download.releaseId} · {download.trackerSeeders ?? '—'} tracker seeders{download.releaseSizeBytes ? ` · ${formatBytes(download.releaseSizeBytes)} total` : ''}</small><span class="tv-download-telemetry">{download.state} · {(download.progress * 100).toFixed(1)}% · {formatBytes(download.downloadedBytes)} / {formatBytes(download.sizeBytes)} selected</span><small class="tv-download-telemetry">{formatBytes(download.speedBytesPerSecond)}/s · {download.seeds} connected seeds · {download.peers} peers</small><small class={`tv-download-error ${download.error ? 'visible' : ''}`}>{download.error || 'No download error'}</small></div><div><button data-focus-region="content" data-focus-row={2 + index} data-focus-col="0" data-focus-key={`download-${download.id}-play`} class="primary" onClick={() => onPlay(download)}>Play</button>{downloadTransferActions(download).map(item => <button key={item.action} disabled={busyAction.startsWith(download.id + ':')} data-focus-region="content" data-focus-row={2 + index} data-focus-col="1" data-focus-key={`download-${download.id}-${item.action}`} onClick={() => void runAction(download, item.action)}>{busyAction === download.id + ':' + item.action ? item.pendingLabel : item.label}</button>)}<button class="danger-button" data-focus-region="content" data-focus-row={2 + index} data-focus-col="2" data-focus-key={`download-${download.id}-delete`} onClick={() => setPending(download)}>Delete download</button></div></article>)}</section>
}
function TVLibraryCategories({ api, onOpen }: { api: API; onOpen: (item: HouseholdItem) => void }) { const [categories, setCategories] = useState<LibraryCategory[]>([]); const [items, setItems] = useState<HouseholdItem[]>([]); const [selected, setSelected] = useState(''); const [message, setMessage] = useState('Loading library categories…'); useEffect(() => { api.libraryCategories().then(page => { setCategories(page.items as LibraryCategory[]); setMessage('') }).catch(error => setMessage(error.message)) }, []); async function open(name: string) { setSelected(name); setMessage('Loading category…'); try { const page = await api.libraryCategories(name); setItems(canonicalHouseholdItems(page.items as HouseholdItem[])); setMessage('') } catch (error) { setMessage((error as Error).message) } } if (selected) return <section><button data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="library-category-back" onClick={() => { setSelected(''); setItems([]) }}>All categories</button><div class="row-heading"><h2>{selected}</h2><span>{items.length} item{items.length === 1 ? '' : 's'}</span></div>{message && <p aria-live="polite">{message}</p>}<div class="tv-library-grid">{items.map((item, index) => <button class="poster-card" data-focus-region="content" data-focus-row={2 + Math.floor(index / 5)} data-focus-col={index % 5} data-focus-key={`library-item-${item.titleId || item.catalog?.id || item.release.id}`} onClick={() => onOpen(item)}>{item.catalog?.posterUrl ? <img src={api.streamURL(item.catalog.posterUrl)} alt="" loading="lazy" /> : <div class="poster-fallback">{(item.catalog?.title || item.release.name).slice(0, 1)}</div>}<TVStateBadges state={item.catalog?.libraryState} /><div class="poster-copy"><strong>{item.catalog?.title || item.release.name}</strong><span>{[item.catalog?.year, item.catalog?.resolutions?.[0], item.release.category].filter(Boolean).join(' · ')}</span><small>{item.seasonNumber && item.episodeNumber ? `S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')} · ` : ''}{item.watched ? 'Watched' : item.positionMs > 0 ? 'In progress' : `${item.release.seeders} seeders`}</small></div></button>)}</div>{items.length === 0 && !message && <p class="tv-muted">No media remains in this category.</p>}</section>; return <section class="tv-category-grid">{message && <p aria-live="polite">{message}</p>}{categories.map((category, index) => <button data-focus-region="content" data-focus-row={1 + Math.floor(index / 4)} data-focus-col={index % 4} data-focus-key={`library-category-${category.name}`} onClick={() => void open(category.name)}><strong>{category.name}</strong><span>{category.count} item{category.count === 1 ? '' : 's'}</span></button>)}</section> }
function TVJobs({ api, items: initial }: { api: API; items: Job[] }) {
  const [items, setItems] = useState(initial); const [query, setQuery] = useState(''); const [state, setState] = useState(''); const [kind, setKind] = useState(''); const [retryable, setRetryable] = useState(''); const [updatedHours, setUpdatedHours] = useState(''); const [cursor, setCursor] = useState(''); const [next, setNext] = useState<string | null>(null); const [history, setHistory] = useState<string[]>([]); const [message, setMessage] = useState(''); const [detail, setDetail] = useState<{ job: Job; logs: JobLog[]; next: string | null } | null>(null);
  async function load(target = '', remember = false) { try { const page = await api.jobs({ search: query, state, kind, retryable, updatedHours, pageSize: 12, cursor: target }); if (remember) setHistory(value => [...value, cursor]); setCursor(target); setItems(page.items); setNext(page.nextCursor) } catch (error) { setMessage((error as Error).message) } }
  async function open(job: Job) { try { const [result, logs] = await Promise.all([api.job(job.id), api.jobLogs(job.id)]); setDetail({ job: result.job, logs: logs.items, next: logs.nextCursor }) } catch (error) { setMessage((error as Error).message) } }
  async function older() { if (!detail?.next) return; try { const page = await api.jobLogs(detail.job.id, detail.next); setDetail({ ...detail, logs: [...detail.logs, ...page.items], next: page.nextCursor }) } catch (error) { setMessage((error as Error).message) } }
  useEffect(() => { const timer = window.setTimeout(() => { setHistory([]); void load('') }, 400); return () => clearTimeout(timer) }, [query, state, kind, retryable, updatedHours]);
  if (detail) return <TVJobDetail detail={detail} onBack={() => setDetail(null)} onOlder={older} />;
  const cycle = (value: string, values: string[], set: (value: string) => void) => set(values[(values.indexOf(value) + 1) % values.length]);
  return <section class="tv-list"><h2>Background jobs</h2><div class="tv-filters"><input readOnly data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="jobs-search" value={query} onInput={event => setQuery(event.currentTarget.value)} placeholder="Search; press OK to type" /><button data-focus-region="content" data-focus-row="1" data-focus-col="1" data-focus-key="jobs-state" onClick={() => cycle(state, ['', 'failed', 'completed', 'running', 'queued'], setState)}>State: {state || 'all'}</button><button data-focus-region="content" data-focus-row="1" data-focus-col="2" data-focus-key="jobs-kind" onClick={() => cycle(kind, ['', 'metadata', 'catalog-title-refresh', 'catalog-sync'], setKind)}>Kind: {kind || 'all'}</button><button data-focus-region="content" data-focus-row="1" data-focus-col="3" data-focus-key="jobs-retry" onClick={() => cycle(retryable, ['', 'true', 'false'], setRetryable)}>Retry: {retryable || 'any'}</button><button data-focus-region="content" data-focus-row="1" data-focus-col="4" data-focus-key="jobs-time" onClick={() => cycle(updatedHours, ['', '24', '168', '720'], setUpdatedHours)}>Updated: {updatedHours ? `${updatedHours}h` : 'any'}</button></div>{message && <p>{message}</p>}{items.length === 0 ? <p>No matching jobs.</p> : items.map((job, index) => <article><div><strong>{job.label || job.kind}</strong><span>{job.state} · {(job.progress * 100).toFixed(0)}%</span><small>{job.id} · {job.error || job.nextAttemptAt && `retry ${new Date(job.nextAttemptAt).toLocaleString()}` || new Date(job.updatedAt).toLocaleString()}</small></div><div><button data-focus-region="content" data-focus-row={2 + index} data-focus-col="0" data-focus-key={`job-${job.id}`} onClick={() => void open(job)}>Details</button><button disabled={job.state === 'queued' || job.state === 'running' || job.state === 'retry_wait'} data-focus-region="content" data-focus-row={2 + index} data-focus-col="1" data-focus-key={`job-${job.id}-retry`} onClick={() => void api.retryJob(job.id).then(() => { setMessage('Job queued again.'); void load(cursor) }).catch(error => setMessage(error.message))}>Retry</button></div></article>)}<nav class="tv-pager"><button disabled={history.length === 0} data-focus-region="content" data-focus-row="90" data-focus-col="0" data-focus-key="jobs-previous" onClick={() => { const target = history[history.length - 1] || ''; setHistory(value => value.slice(0, -1)); void load(target) }}>Previous</button><button disabled={!next} data-focus-region="content" data-focus-row="90" data-focus-col="1" data-focus-key="jobs-next" onClick={() => next && void load(next, true)}>Next</button></nav></section>
}

function TVJobDetail({ detail, onBack, onOlder }: { detail: { job: Job; logs: JobLog[]; next: string | null }; onBack: () => void; onOlder: () => void }) {
  const [level, setLevel] = useState(''); const [attempt, setAttempt] = useState(''); const [expanded, setExpanded] = useState<number | null>(null);
  const logs = detail.logs.filter(log => (!level || log.level === level) && (!attempt || String(log.attempt) === attempt));
  return <section class="tv-list job-detail"><button data-focus-region="content" data-focus-row="0" data-focus-col="0" data-focus-key="job-detail-back" onClick={onBack}>← Jobs</button><h2>{detail.job.label}</h2><p>{detail.job.state} · attempt {detail.job.attempt}</p><div class="tv-filters"><button data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="log-level" onClick={() => setLevel(value => value === '' ? 'error' : value === 'error' ? 'warn' : '')}>Level: {level || 'all'}</button><button data-focus-region="content" data-focus-row="1" data-focus-col="1" data-focus-key="log-attempt" onClick={() => setAttempt(value => value === '' ? String(detail.job.attempt) : '')}>Attempt: {attempt || 'all'}</button></div>{logs.length === 0 ? <p>No logs match these filters.</p> : logs.map((log, index) => { const open = expanded === log.id; return <button type="button" aria-expanded={open} data-focus-region="content" data-focus-row={2 + index} data-focus-col="0" data-focus-key={`job-log-${log.id}`} class={`job-log ${log.level} ${open ? 'expanded' : ''}`} onClick={() => setExpanded(value => value === log.id ? null : log.id)}><span class="job-log-heading"><strong>{log.phase} · {log.level}</strong><i>{open ? 'Hide details' : 'Show details'}</i></span><span>{new Date(log.createdAt).toLocaleString()} · attempt {log.attempt}</span><small>{log.message}</small>{open && <div class="job-log-expanded"><dl><dt>Job</dt><dd>{log.jobId}</dd><dt>Log entry</dt><dd>{log.id}</dd><dt>Attempt</dt><dd>{log.attempt}</dd></dl>{log.context && Object.keys(log.context).length > 0 ? <pre>{JSON.stringify(log.context, null, 2)}</pre> : <p>No structured context was recorded.</p>}</div>}</button> })}{detail.next && <button data-focus-region="content" data-focus-row={3 + logs.length} data-focus-col="0" data-focus-key="job-logs-older" onClick={() => void onOlder()}>Load older logs</button>}</section>
}

function TVEvents({ api }: { api: API }) { const [message, setMessage] = useState(''); const [coverage, setCoverage] = useState<Record<string, unknown> | null>(null); useEffect(() => { api.call<Record<string, unknown>>('/catalog/status').then(setCoverage).catch(error => setMessage(error.message)) }, []); async function run(mode: 'latest' | 'rebuild') { try { const job = await api.syncCatalog(mode); setMessage(`${job.label} queued. Follow it on Jobs.`) } catch (error) { setMessage((error as Error).message) } } return <section class="tv-settings"><h2>Server events</h2>{coverage && <><p><strong>{Number(coverage.observedReleases).toLocaleString()}</strong> releases retained · <strong>{Number(coverage.discoverableReleases).toLocaleString()}</strong> currently seeded</p><p>{Number(coverage.hiddenZeroSeeders).toLocaleString()} zero-seeder releases are retained but hidden from discovery.</p></>}<p>Run the same safe catalog actions available in the browser.</p><button data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="event-latest" class="primary" onClick={() => void run('latest')}>Fetch latest data</button><button data-focus-region="content" data-focus-row="2" data-focus-col="0" data-focus-key="event-rebuild" onClick={() => void run('rebuild')}>Rebuild catalog cache</button><p aria-live="polite">{message}</p></section> }

function TVSettings({ api, onChangeServer, onForgetServer, updateStatus, onUpdateStatus, confirmOpen, onConfirmOpen, onConfirmClose }: { api: API; onChangeServer: () => void; onForgetServer: () => void; updateStatus: UpdateStatus | null; onUpdateStatus: (status: UpdateStatus) => void; confirmOpen: boolean; onConfirmOpen: () => void; onConfirmClose: () => void }) {
  const [value, setValue] = useState<Record<string, unknown> | null>(null); const [managed, setManaged] = useState<Set<string>>(new Set()); const [message, setMessage] = useState('Loading settings…');
  const [checking, setChecking] = useState(false); const [applying, setApplying] = useState(false); const [updateMessage, setUpdateMessage] = useState('');
  useEffect(() => { Promise.all([api.call<Record<string, unknown>>('/settings'), api.call<{ items: SettingsField[] }>('/settings/schema')]).then(([settings, schema]) => { setValue(settings); setManaged(new Set(schema.items.filter(field => field.readOnly).map(field => field.key))); setMessage('') }).catch(error => setMessage(error.message)) }, []);
  useEffect(() => {
    if (!updateStatus?.applying) return;
    const timer = window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('[data-focus-key="update-check"]')), 0);
    return () => window.clearTimeout(timer);
  }, [updateStatus?.applying]);
  async function save() { if (!value) return; const out = { ...value }; Object.keys(out).filter(key => key.endsWith('Configured') || key === 'settingsPath').forEach(key => delete out[key]); try { await api.call('/settings', { method: 'PUT', body: JSON.stringify(out) }); setMessage('Settings saved. Restart the server to apply worker-limit changes.') } catch (error) { setMessage((error as Error).message) } }
  async function test(name: string) { setMessage(`Testing ${name}…`); try { const result = await api.call<{ message: string }>(`/dependencies/${name}/test`, { method: 'POST' }); setMessage(result.message) } catch (error) { setMessage((error as Error).message) } }
  async function checkUpdate() {
    if (checking) return;
    setChecking(true); setUpdateMessage('Checking for updates…');
    try { const next = await api.call<UpdateStatus>('/updates/check', { method: 'POST' }); onUpdateStatus(next); setUpdateMessage(next.available ? `Version ${next.latest} is available.` : 'The server is up to date.') } catch (error) { setUpdateMessage(`Update check failed: ${(error as Error).message}`) } finally { setChecking(false) }
  }
  async function applyUpdate() {
    if (applying) return;
    setApplying(true);
    try {
      const next = await api.call<UpdateStatus>('/updates/apply', { method: 'POST' });
      onUpdateStatus(next);
      setUpdateMessage(next.applying ? 'Update accepted. The server restarts; playback resumes when it is back.' : 'The server is already up to date.');
    } catch (error) {
      const outcome = updateApplyOutcome((error as Error & { status?: number }).status);
      setUpdateMessage(outcome === 'conflict' ? `Update refused: ${(error as Error).message}` : `Update failed: ${(error as Error).message}`);
    } finally { setApplying(false); onConfirmClose() }
  }
  return <section class="tv-settings"><h2>Playback and connection</h2><p>API secrets and filesystem paths stay in browser Settings.</p>{value && <div class="tv-safe-fields">
    <label>Preferred audio language{managed.has('preferredAudioLanguage') && <small>Environment managed</small>}<input disabled={managed.has('preferredAudioLanguage')} data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="setting-audio-primary" value={String(value.preferredAudioLanguage || 'en')} onInput={event => setValue({ ...value, preferredAudioLanguage: event.currentTarget.value })} /></label>
    <label>Preferred subtitle language{managed.has('preferredSubtitleLanguage') && <small>Environment managed</small>}<input disabled={managed.has('preferredSubtitleLanguage')} data-focus-region="content" data-focus-row="2" data-focus-col="0" data-focus-key="setting-subtitle-primary" value={String(value.preferredSubtitleLanguage || '')} onInput={event => setValue({ ...value, preferredSubtitleLanguage: event.currentTarget.value })} /></label>
    <label>Fallback subtitle language{managed.has('fallbackSubtitleLanguage') && <small>Environment managed</small>}<input disabled={managed.has('fallbackSubtitleLanguage')} data-focus-region="content" data-focus-row="3" data-focus-col="0" data-focus-key="setting-subtitle-fallback" value={String(value.fallbackSubtitleLanguage || '')} onInput={event => setValue({ ...value, fallbackSubtitleLanguage: event.currentTarget.value })} /></label>
    <label>Watched threshold{managed.has('watchedThresholdPercent') && <small>Environment managed</small>}<input disabled={managed.has('watchedThresholdPercent')} type="number" data-focus-region="content" data-focus-row="4" data-focus-col="0" data-focus-key="setting-watched" value={String(value.watchedThresholdPercent || 90)} onInput={event => setValue({ ...value, watchedThresholdPercent: Number(event.currentTarget.value) })} /></label>
    <label>Concurrent background jobs{managed.has('maxConcurrentJobs') && <small>Environment managed</small>}<input disabled={managed.has('maxConcurrentJobs')} type="number" min="1" max="20" data-focus-region="content" data-focus-row="5" data-focus-col="0" data-focus-key="setting-workers" value={String(value.maxConcurrentJobs || 10)} onInput={event => setValue({ ...value, maxConcurrentJobs: Number(event.currentTarget.value) })} /></label>
    <label>Title refresh timeout (minutes){managed.has('titleRefreshTimeoutMinutes') && <small>Environment managed</small>}<input disabled={managed.has('titleRefreshTimeoutMinutes')} type="number" min="5" max="120" data-focus-region="content" data-focus-row="6" data-focus-col="0" data-focus-key="setting-title-timeout" value={String(value.titleRefreshTimeoutMinutes || 30)} onInput={event => setValue({ ...value, titleRefreshTimeoutMinutes: Number(event.currentTarget.value) })} /></label>
    <button class="primary" data-focus-region="content" data-focus-row="7" data-focus-col="0" data-focus-key="settings-save" onClick={() => void save()}>Save preferences</button>
  </div>}<div class="tv-test-buttons">{['filelist', 'qbittorrent', 'storage', 'tmdb', 'subdl'].map((name, index) => <button data-focus-region="content" data-focus-row={8 + index} data-focus-col="0" data-focus-key={`test-${name}`} onClick={() => void test(name)}>Test {name}</button>)}</div><button data-focus-region="content" data-focus-row="14" data-focus-col="0" data-focus-key="change-server" onClick={onChangeServer}>Change server address</button><button data-focus-region="content" data-focus-row="15" data-focus-col="0" data-focus-key="forget-server" onClick={onForgetServer}>Forget this server</button>
    {updateStatus && <div class="tv-update-panel"><p>Server version {updateStatus.currentVersion}{updateStatus.applying ? ' · installing an update' : ''}</p>{updateNoticeVisible(updateStatus) && <div class="tv-update-notice"><strong>{updateStatus.available ? `Version ${updateStatus.latest} is available.` : 'This server updates only by hand.'}</strong><p>Updates install on the server machine and interrupt playback on every connected player; this TV installs nothing itself.</p><a href={updateStatus.releasesUrl}>{updateStatus.releasesUrl}</a></div>}</div>}
    <button data-focus-region="content" data-focus-row={UPDATE_CHECK_ROW} data-focus-col="0" data-focus-key="update-check" disabled={checking} onClick={() => void checkUpdate()}>{checking ? 'Checking…' : 'Check for server updates'}</button>
    <button data-focus-region="content" data-focus-row={UPDATE_APPLY_ROW} data-focus-col="0" data-focus-key="update-apply" disabled={updateApplyDisabled(updateStatus, applying)} onClick={onConfirmOpen}>{updateStatus?.applying || applying ? 'Installing…' : 'Install server update'}</button>
    <p aria-live="polite">{updateMessage}</p>
    {confirmOpen && updateStatus && <section role="dialog" aria-modal="true" aria-labelledby="tv-update-confirm-heading" class="tv-settings tv-update-confirm"><h2 id="tv-update-confirm-heading">Install version {updateStatus.latest}?</h2><p>The server downloads the release, installs it, and restarts. Playback is interrupted on every connected device, including this TV.</p><div><button data-focus-region={UPDATE_DIALOG_REGION} data-focus-row="0" data-focus-col="0" data-focus-key="update-confirm-cancel" onClick={onConfirmClose}>Cancel</button><button class="danger-button" data-focus-region={UPDATE_DIALOG_REGION} data-focus-row="0" data-focus-col="1" data-focus-key="update-confirm-apply" disabled={applying} onClick={() => void applyUpdate()}>{applying ? 'Starting…' : 'Install and restart'}</button></div></section>}
    <p aria-live="polite">{message}</p></section>;
}

// Display-only promotion slot. It owns no focus stop and records no clicks;
// a hidden, failed, or empty delivery leaves nothing in the DOM. Delivery
// advances after the creative's screen time and stops on unmount or a
// hidden document.
function TVPromotions({ api }: { api: API }) {
  const [promotion, setPromotion] = useState<PortalPromotion | null>(null);
  useEffect(() => {
    let stopped = false;
    let timer = 0;
    let controller: AbortController | null = null;
    const cancelDelivery = () => { window.clearTimeout(timer); timer = 0; if (controller) { controller.abort(); controller = null } };
    const deliver = async () => {
      if (stopped || document.hidden) return;
      cancelDelivery();
      controller = new AbortController();
      try {
        const [creative] = await api.call<PortalPromotion[]>('/portal/promotions?count=1', { signal: controller.signal });
        if (stopped || document.hidden) return;
        setPromotion(creative || null);
        if (creative) timer = window.setTimeout(() => { timer = 0; void deliver() }, promotionScreenTimeMs(creative.screenTime));
      } catch { if (!stopped) setPromotion(null) }
    };
    const visibility = () => { if (document.hidden) cancelDelivery(); else void deliver() };
    void deliver();
    document.addEventListener('visibilitychange', visibility);
    return () => { stopped = true; cancelDelivery(); document.removeEventListener('visibilitychange', visibility) };
  }, [api]);
  if (!promotion) return null;
  return <div class="tv-promo"><small>Advertisement</small>{/^https?:\/\//.test(promotion.image) && <img src={promotion.image} alt="" />}{promotion.title && <strong>{promotion.title}</strong>}{promotion.text && <p>{promotion.text}</p>}</div>;
}

function TitleCard({ api, title, row, col, focusRef, onOpen }: { api: API; title: CatalogTitle; row: number; col: number; focusRef?: { current: HTMLButtonElement | null }; onOpen: () => void }) {
  return <button ref={focusRef} class="poster-card" data-focus-region="content" data-focus-row={row} data-focus-col={col} data-focus-key={`title-${title.id}`} onClick={onOpen}>{title.posterUrl ? <img src={api.streamURL(title.posterUrl)} alt="" loading="lazy" /> : <div class="poster-fallback">{title.title.slice(0, 1)}</div>}<TVStateBadges state={title.libraryState} /><div class="poster-copy"><strong>{title.title}</strong><span>{title.year || '—'} · {title.resolutions[0] || title.kind}{title.ratingVotes ? ` · ★ ${title.rating?.toFixed(1)}` : ''}</span><small>{title.bestSeeders} seeders · {title.sourceCount} source{title.sourceCount === 1 ? '' : 's'}</small></div></button>;
}

function sourceActionLabel(source: CatalogSource) { return source.libraryState?.downloadState && source.libraryState.downloadState !== 'none' ? 'Play' : 'Play and download' }
function SourceButton({ source, row, onPlay }: { source: CatalogSource; row: number; onPlay: (release: Release, fileIndex?: number) => void }) { return <button class="source-button" data-focus-region="content" data-focus-row={row} data-focus-col="0" data-focus-key={`source-${source.release.id}-${source.fileIndex ?? -1}`} onClick={() => onPlay(source.release, source.fileIndex)}><span class="source-copy"><strong>{source.parsed.resolution || 'Source'}{source.parsed.hdr ? ` · ${source.parsed.hdr}` : ''}</strong><small class="source-filename">{source.filePath || source.release.name}</small><small>{source.parsed.quality || source.release.category} · {source.parsed.videoCodec || 'codec unknown'}</small></span><span class="source-action"><TVStateBadges state={source.libraryState} /><b class="source-action-label">{sourceActionLabel(source)}</b><small>{formatBytes(source.fileSizeBytes || source.release.sizeBytes)} · {source.release.seeders} seeders</small></span></button> }

type SeasonPackAction = 'download' | 'pause' | 'resume' | 'retry' | 'delete';
function TVSeasonPackCard({ source, season, index, open, onToggle, onAction, onDelete }: { source: CatalogSource; season: number; index: number; open: boolean; onToggle: () => void; onAction: (source: CatalogSource, season: number, action: SeasonPackAction) => Promise<void>; onDelete: () => void }) {
  const state = source.libraryState; const [busy, setBusy] = useState(''); const managed = Boolean(state?.downloadId); const paused = state?.transferState === 'paused'; const complete = state?.downloadState === 'downloaded'; const error = state?.downloadState === 'error';
  const run = async (action: SeasonPackAction) => { if (busy) return; setBusy(action); try { await onAction(source, season, action) } finally { setBusy('') } };
  return <article class={`season-pack-card ${open ? 'expanded' : ''}`}>
    <button class="season-pack-button" data-focus-region="content" data-focus-row="3" data-focus-col={index} data-focus-key={`season-pack-${source.release.id}`} aria-expanded={open} aria-controls={`tv-pack-${source.release.id}`} onClick={onToggle} aria-label={`${source.parsed.resolution || 'Season pack'} · ${seasonPackActionLabel(state)} · ${open ? 'hide' : 'show'} controls`}>
      <span class="season-pack-copy"><strong>{source.parsed.resolution || 'Season pack'}{source.parsed.hdr ? ` · ${source.parsed.hdr}` : ''}</strong><small class="source-filename">{source.release.name}</small><small>{[source.parsed.quality, source.parsed.videoCodec, source.parsed.audio].filter(Boolean).join(' · ') || 'Source details unavailable'}</small></span>
      <span class="season-pack-action"><TVStateBadges state={state} /><b>{seasonPackActionLabel(state)}</b><small>{formatBytes(source.release.sizeBytes)} · {source.release.seeders} seeders</small><small>{open ? 'Hide controls' : 'Show controls'}</small></span>
    </button>
    {open && <div id={`tv-pack-${source.release.id}`} class="season-pack-controls"><progress value={state?.progress || 0} max="1" aria-label="Season download progress" />{!managed && <button class="primary" disabled={Boolean(busy)} data-focus-region="content" data-focus-row="4" data-focus-col="0" data-focus-key={`season-pack-${source.release.id}-download`} onClick={() => void run('download')}>{busy ? 'Starting…' : 'Download season'}</button>}{managed && !complete && !error && <button disabled={Boolean(busy)} data-focus-region="content" data-focus-row="4" data-focus-col="0" data-focus-key={`season-pack-${source.release.id}-toggle`} onClick={() => void run(paused ? 'resume' : 'pause')}>{busy ? `${paused ? 'Resuming' : 'Pausing'}…` : paused ? 'Resume' : 'Pause'}</button>}{error && <button class="primary" disabled={Boolean(busy)} data-focus-region="content" data-focus-row="4" data-focus-col="0" data-focus-key={`season-pack-${source.release.id}-retry`} onClick={() => void run('retry')}>{busy ? 'Retrying…' : 'Retry'}</button>}{managed && <button class="danger-button" disabled={Boolean(busy)} data-focus-region="content" data-focus-row="4" data-focus-col="1" data-focus-key={`season-pack-${source.release.id}-delete`} onClick={onDelete}>Delete download</button>}</div>}
  </article>;
}

function TitleDetail({ api, detail, target, message, resume, favorite, onClose, onFavorite, onResume, onPlay, onPackAction }: { api: API; detail: CatalogDetail; target: DetailTarget; message: string; resume?: HouseholdItem; favorite: boolean; onClose: () => void; onFavorite: (title: CatalogTitle, value: boolean) => void; onResume: (item: HouseholdItem) => void; onPlay: (release: Release, fileIndex?: number) => void; onPackAction: (source: CatalogSource, season: number, action: SeasonPackAction) => Promise<void> }) {
  const [season, setSeason] = useState(target.season || detail.seasons[0]?.number || 0);
  const [expanded, setExpanded] = useState(target.episode ? `${target.season}:${target.episode}` : '');
  const [expandedPack, setExpandedPack] = useState('');
  const [pendingPack, setPendingPack] = useState<CatalogSource | null>(null);
  const [deleting, setDeleting] = useState(false);
  const selected = detail.seasons.find(item => item.number === season);
  const firstSource = detail.sources[0] || selected?.episodes[0]?.sources[0];
  useEffect(() => { const timer = window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('[data-detail-initial]')), 0); return () => window.clearTimeout(timer); }, []);
  useEffect(() => { if (!pendingPack) return; const timer = window.setTimeout(() => focusElement(document.querySelector<HTMLElement>('[data-focus-key="season-pack-delete-cancel"]')), 0); return () => window.clearTimeout(timer) }, [pendingPack]);
  useEffect(() => { if (!expanded && !expandedPack && !pendingPack) return; const key = (event: KeyboardEvent) => { if (remoteAction(event.key, event.keyCode) !== 'back') return; event.preventDefault(); event.stopImmediatePropagation(); if (pendingPack && !deleting) setPendingPack(null); else if (expandedPack) setExpandedPack(''); else setExpanded('') }; document.addEventListener('keydown', key, true); return () => document.removeEventListener('keydown', key, true) }, [expanded, expandedPack, pendingPack, deleting]);
  const confirmDelete = async () => { if (!pendingPack || !selected || deleting) return; setDeleting(true); try { await onPackAction(pendingPack, selected.number, 'delete'); setPendingPack(null) } finally { setDeleting(false) } };
  return <main class="detail-screen" style={detail.title.backdropUrl ? { backgroundImage: `linear-gradient(90deg,#090d10 5%,rgba(9,13,16,.9) 55%,rgba(9,13,16,.4)),url(${api.streamURL(detail.title.backdropUrl)})` } : undefined}>
    <button data-detail-initial data-focus-region="content" data-focus-row="0" data-focus-col="0" data-focus-key="detail-back" onClick={onClose}>Back</button>
    <div class="detail-copy">
      <h1>{detail.title.title}</h1>
      <p class="detail-meta">{detail.title.kind} · {detail.title.year || 'Year unknown'}</p>
      <TVStateBadges state={detail.title.libraryState} />
      <p>{detail.title.overview || 'Choose the version that best matches your display and connection.'}</p>
      <div class="detail-actions">
        {resume ? <button class="primary" data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="detail-playback" onClick={() => onResume(resume)} aria-label={`${resumeActionLabel(resume, detail.title.kind)} at saved position`}>{resumeActionLabel(resume, detail.title.kind)}</button> : firstSource && <button class="primary" data-focus-region="content" data-focus-row="1" data-focus-col="0" data-focus-key="detail-playback" onClick={() => onPlay(firstSource.release, firstSource.fileIndex)}>{sourceActionLabel(firstSource)}</button>}
        <button data-focus-region="content" data-focus-row="1" data-focus-col="1" data-focus-key="detail-favorite" onClick={() => onFavorite(detail.title, !favorite)}>{favorite ? 'In favorites' : 'Add to favorites'}</button>
      </div>
      {resume && <small class="resume-file">{resumeSummary(resume, detail.title.kind)}</small>}
    </div>
    {message && <p class="detail-message" aria-live="polite">{message}</p>}
    {detail.seasons.length > 0 && <section class="season-browser">
      <h2>Seasons</h2>
      <div class="season-tabs">{detail.seasons.map((item, index) => <button key={item.number} data-focus-region="content" data-focus-row="2" data-focus-col={index} data-focus-key={`season-${item.number}`} class={season === item.number ? 'active' : ''} onClick={() => { setSeason(item.number); setExpanded(''); setExpandedPack('') }}><span>Season {item.number}</span><TVStateBadges state={item.libraryState} /></button>)}</div>
      {selected?.packSources && selected.packSources.length > 0 && <div class="season-pack-downloads">
        <h3>Complete season versions</h3>
        <p>Select a version to review it. Downloads start only from the button inside the expanded version.</p>
        <div class="season-pack-grid">{selected.packSources.map((source, index) => <TVSeasonPackCard key={source.release.id} source={source} season={selected.number} index={index} open={expandedPack === source.release.id} onToggle={() => setExpandedPack(current => current === source.release.id ? '' : source.release.id)} onAction={onPackAction} onDelete={() => setPendingPack(source)} />)}</div>
      </div>}
      {selected && selected.episodes.length === 0 ? <p class="episode-loading" role="status">Preparing the individual episode list. This page updates automatically when it is ready.</p> : selected?.episodes.map((episode, index) => {
        const key = `${episode.season}:${episode.number}`;
        const open = expanded === key;
        const row = 10 + index * 20;
        return <article key={key} class={`episode-row ${open ? 'expanded' : ''}`}>
          <button class="episode-tile" aria-expanded={open} data-focus-region="content" data-focus-row={row} data-focus-col="0" data-focus-key={`episode-${key}`} onClick={() => setExpanded(current => current === key ? '' : key)}>
            <span><b>{episode.number ? `${episode.number}. ` : ''}{episode.title}</b><small>{episode.sourceCount} version{episode.sourceCount === 1 ? '' : 's'} · {open ? 'Hide versions' : 'Show versions'}</small></span>
            <TVStateBadges state={episode.libraryState} />
          </button>
          {open && episode.sources.map((source, sourceIndex) => <SourceButton key={`${source.release.id}:${source.fileIndex ?? -1}`} source={source} row={row + 1 + sourceIndex} onPlay={onPlay} />)}
        </article>;
      })}
    </section>}
    {detail.seasons.length === 0 && <section class="source-list"><h2>Available versions</h2>{detail.sources.map((source, index) => <SourceButton key={`${source.release.id}:${source.fileIndex ?? -1}`} source={source} row={2 + index} onPlay={onPlay} />)}</section>}
    {pendingPack && <section role="dialog" aria-modal="true" aria-labelledby="tv-season-pack-delete-heading" class="tv-settings tv-removal-confirm tv-season-pack-confirm"><h2 id="tv-season-pack-delete-heading">Delete season download?</h2><strong>{pendingPack.release.name}</strong><p>This removes the shared season torrent from qBittorrent and permanently deletes every episode file in it.</p><button disabled={deleting} data-focus-region="season-pack-dialog" data-focus-row="0" data-focus-col="0" data-focus-key="season-pack-delete-cancel" onClick={() => setPendingPack(null)}>Cancel</button><button disabled={deleting} class="danger-button" data-focus-region="season-pack-dialog" data-focus-row="1" data-focus-col="0" data-focus-key="season-pack-delete-confirm" onClick={() => void confirmDelete()}>{deleting ? 'Deleting…' : 'Delete download'}</button></section>}
  </main>;
}

function App() {
  const [server, setServer] = useState(localStorage.getItem(STORAGE) || '');
  const [draft, setDraft] = useState(server || 'http://server.lan:8097');
  const [api, setAPI] = useState<API | null>(null);
  const [titles, setTitles] = useState<CatalogTitle[]>([]);
  const [facets, setFacets] = useState<CatalogFacets>(emptyFacets);
  const [household, setHousehold] = useState<HouseholdState>(emptyState);
  const [downloads, setDownloads] = useState<Download[]>([]);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [status, setStatus] = useState('');
  const [portal, setPortal] = useState<PortalState | null>(null);
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus | null>(null);
  const [player, setPlayer] = useState<{ download: Download; resumeMs: number; preferences?: PlaybackPreferences } | null>(null);
  const catalogFocus = useRef<string | null>(null);
  const viewportInput = useRef(0);
  const loadState = async (client = api) => { if (client) try { setHousehold(await client.state()); } catch (error) { setStatus((error as Error).message); } };
  async function connect(url = draft) { setStatus('Connecting…'); try { const normalized = normalizeServerURL(url); const client = new API(normalized); const info = await client.info(); const [titlePage, catalogFacets, downloadPage, jobPage] = await Promise.all([client.titles({ pageSize: 12, sort: 'newest' }), client.facets(), client.downloads().catch(() => ({ items: [], nextCursor: null, total: 0 })), client.jobs({ pageSize: 24 }).catch(() => ({ items: [], nextCursor: null, total: 0 }))]); localStorage.setItem(STORAGE, normalized); setServer(normalized); setDraft(normalized); setAPI(client); setStatus(`${info.instanceName || info.name} ${info.version}`); setTitles(titlePage.items); setFacets(catalogFacets); void client.ensureMetadata(titlePage.items.map(item => item.id)).catch(() => { }); setDownloads(downloadPage.items); setJobs(jobPage.items); await loadState(client); } catch (error) { setStatus((error as Error).message); } }
  async function play(release: Release, fileIndex = -1, resumeMs = 0) { if (!api) return; setStatus('Preparing source…'); try { const download = await api.prepare(release.id, fileIndex); if (!resumeMs) resumeMs = await api.playback(download.id).then(value => value.watched ? 0 : value.positionMs).catch(() => 0); setPlayer({ download, resumeMs }); } catch (error) { setStatus((error as Error).message); } }
  async function favorite(title: CatalogTitle, value: boolean) { if (!api) return; try { await api.titleFavorite(title.id, value); await loadState(); } catch (error) { setStatus((error as Error).message); } }
  const refreshDownloads = async () => { if (!api) return; const anchor = captureTVDownloadAnchor(); const inputVersion = viewportInput.current; try { const incoming = (await api.downloads()).items; setDownloads(current => reconcileDownloads(current, incoming)); window.requestAnimationFrame(() => { if (inputVersion === viewportInput.current) restoreTVDownloadAnchor(anchor) }) } catch (error) { setStatus((error as Error).message) } };
  async function downloadSeason(source: CatalogSource, season: number) { if (!api) throw new Error('Server is not connected.'); setStatus(`Starting season ${season}…`); try { await api.prepareSeason(source.release.id, season); await refreshDownloads(); setStatus(`Season ${season} added to Downloads.`) } catch (error) { setStatus((error as Error).message); throw error } }
  async function manageSeasonPack(source: CatalogSource, season: number, action: SeasonPackAction) { if (!api) throw new Error('Server is not connected.'); if (action === 'download' || (action === 'retry' && !source.libraryState?.downloadId)) { await downloadSeason(source, season); return } const id = source.libraryState?.downloadId; if (!id) throw new Error('This season download is not registered yet. Refresh the title and try again.'); try { if (action === 'delete') await api.deleteDownload(id); else await api.call(`/downloads/${encodeURIComponent(id)}/${action}`, { method: 'POST' }); await refreshDownloads() } catch (error) { setStatus((error as Error).message); throw error } }
  async function advanceEpisode(preferences: PlaybackPreferences) { if (!api || !player) return; try { const next = await api.nextEpisode(player.download.id); await Promise.all([loadState(), refreshDownloads()]); if (next) setPlayer({ download: next, resumeMs: 0, preferences: { ...preferences, sourceId: next.id, subtitleMode: preferences.subtitleMode === 'off' ? 'off' : 'auto', subtitleProvider: '', subtitleCandidateId: '' } }); else setPlayer(null) } catch (error) { setStatus(`Could not start the next episode: ${(error as Error).message}`); setPlayer(null) } }
  async function manageDownload(download: Download, action: string) { if (!api) throw new Error('Server is not connected.'); try { if (action === 'remove') await api.deleteDownload(download.id); else await api.call(`/downloads/${encodeURIComponent(download.id)}/${action}`, { method: 'POST' }); const incoming = (await api.downloads()).items; setDownloads(current => reconcileDownloads(current, incoming)); } catch (error) { setStatus((error as Error).message); throw error } }
  useEffect(() => { registerMediaKeys(); if (server) void connect(server); }, []);
  useEffect(() => { const input = () => { viewportInput.current++ }; window.addEventListener('keydown', input); return () => window.removeEventListener('keydown', input) }, []);
  useEffect(() => {
    if (!api) return;
    let stream: EventSource | null = null;
    let timer = 0;
    let stopped = false;
    let failures = 0;
    let recovering = false;
    let recoveryGeneration = 0;
    const eventPayload = (event: MessageEvent) => { const envelope = JSON.parse(event.data); return typeof envelope.payload === 'string' ? JSON.parse(envelope.payload) : envelope.payload };
    const loadPortal = () => api.call<PortalState>('/portal/state').then(value => setPortal(value)).catch(() => setPortal(null));
    const loadUpdate = () => api.call<UpdateStatus>('/updates/current').then(value => setUpdateStatus(value)).catch(() => setUpdateStatus(null));
    const metadata = (event: MessageEvent) => { try { const payload = eventPayload(event); const title = payload.title as CatalogTitle | undefined; if (!title?.id) return; setTitles(current => current.some(item => item.id === title.id) ? current.map(item => item.id === title.id ? title : item) : current) } catch (error) { void api.diagnostic('warn', 'Could not process metadata event', { error: String(error) }).catch(() => { }) } };
    const catalogUpdated = (event: MessageEvent) => { try { const payload = eventPayload(event); const titleId = String(payload.titleId || payload.title?.id || ''); if (titleId) window.dispatchEvent(new CustomEvent('catalog-title-updated', { detail: { titleId } })); setStatus(titleId ? 'Episode list updated.' : 'Catalog updated.') } catch (error) { void api.diagnostic('warn', 'Could not process catalog event', { error: String(error) }).catch(() => { }) } };
    const searchCompleted = (event: MessageEvent) => { try { const payload = eventPayload(event); window.dispatchEvent(new CustomEvent('catalog-search-completed', { detail: payload })); setStatus(`Search for ${payload.query || 'title'} completed.`) } catch (error) { void api.diagnostic('warn', 'Could not process search event', { error: String(error) }).catch(() => { }) } };
    const portalStateEvent = (event: MessageEvent) => { if (!snapshotEventAllowed(recovering, 'portal.state')) return; try { setPortal(eventPayload(event) as PortalState) } catch (error) { void api.diagnostic('warn', 'Could not process portal event', { error: String(error) }).catch(() => { }) } };
    const updateStatusEvent = (event: MessageEvent) => { if (!snapshotEventAllowed(recovering, 'updates.status')) return; try { setUpdateStatus(eventPayload(event) as UpdateStatus) } catch (error) { void api.diagnostic('warn', 'Could not process update event', { error: String(error) }).catch(() => { }) } };
    const updateFailedEvent = (event: MessageEvent) => { if (!snapshotEventAllowed(recovering, 'updates.failed')) return; try { const payload = eventPayload(event) as { message?: string }; setStatus(payload.message || 'A server update had a problem.') } catch (error) { void api.diagnostic('warn', 'Could not process update failure', { error: String(error) }).catch(() => { }) } };
    const open = () => {
      if (stopped) return;
      stream?.close();
      stream = new EventSource(`${api.base}/api/v1/events`);
      stream.onopen = () => {
        failures = 0;
        setStatus('Server connected');
        // Reconnect recovery: refetch both snapshots and drop replayed
        // portal/update events until the fresh state has landed, so a stale
        // replay can never override what the server reports now.
        recovering = true;
        const generation = ++recoveryGeneration;
        void Promise.all([loadPortal(), loadUpdate()]).then(() => { if (recoverySettles(generation, recoveryGeneration)) recovering = false });
      };
      stream.addEventListener('catalog.updated', catalogUpdated as EventListener);
      stream.addEventListener('catalog.search.completed', searchCompleted as EventListener);
      stream.addEventListener('metadata.updated', metadata as EventListener);
      stream.addEventListener('job.updated', () => setStatus('A background job was updated.'));
      stream.addEventListener('portal.state', portalStateEvent as EventListener);
      stream.addEventListener('updates.status', updateStatusEvent as EventListener);
      stream.addEventListener('updates.failed', updateFailedEvent as EventListener);
      stream.onerror = () => { stream?.close(); if (stopped) return; failures++; const delay = Math.min(30_000, 1000 * Math.pow(2, Math.min(5, failures - 1))); setStatus(`Server connection lost. Reconnecting in ${Math.ceil(delay / 1000)}s…`); void api.diagnostic('warn', 'TV event stream disconnected', { attempt: failures }).catch(() => { }); window.clearTimeout(timer); timer = window.setTimeout(open, delay); };
    };
    void loadPortal();
    void loadUpdate();
    open();
    return () => { stopped = true; window.clearTimeout(timer); stream?.close() };
  }, [api]);
  if (player && api) return <Player key={player.download.id} api={api} download={player.download} resumeMs={player.resumeMs} preferences={player.preferences} onStateChanged={() => loadState()} onComplete={advanceEpisode} onClose={() => setPlayer(null)} />;
  if (!api) return <Setup draft={draft} server={server} status={status} onDraft={setDraft} onConnect={url => void connect(url)} onForget={() => { localStorage.removeItem(STORAGE); setServer(''); setDraft(''); setStatus('Saved server forgotten.') }} />;
  return <Catalog api={api} status={status} titles={titles} facets={facets} household={household} downloads={downloads} jobs={jobs} restoreFocus={catalogFocus.current} portal={portal} updateStatus={updateStatus} onUpdateStatus={setUpdateStatus} onFocus={key => { catalogFocus.current = key; }} onRetry={() => void connect(server)} onChangeServer={() => setAPI(null)} onForgetServer={() => { localStorage.removeItem(STORAGE); setAPI(null); setServer(''); setDraft(''); }} onPlay={play} onPlayDownload={download => void api.playback(download.id).then(value => setPlayer({ download, resumeMs: value.watched ? 0 : value.positionMs })).catch(() => setPlayer({ download, resumeMs: 0 }))} onManageDownload={manageDownload} onManageSeasonPack={manageSeasonPack} onRefreshDownloads={refreshDownloads} onFavorite={favorite} />;
}

try {
  const root = document.getElementById('app');
  if (!root) throw new Error('The application root element is missing.');
  render(<App />, root);
  window.FileListBoot?.ready();
} catch (error) {
  window.FileListBoot?.fail(error);
  throw error;
}
