import { render, type ComponentChild } from 'preact';
import { useEffect, useMemo, useRef, useState } from 'preact/hooks';
import { audioPlaybackRoute, buildPath, canonicalHouseholdItems, CatalogDetail, CatalogSource, CatalogTitle, clampVolume, ControlsVisibility, clearPortalSession, Download, DownloadTransferAction, eventPayload, formatBytes, HouseholdItem, HouseholdState, languageDisplayName, LibraryCategory, loadPlayerSettings, loadPortalSession, logicalPlaybackPosition, MediaAudioTrack, MediaInfo, MediaState, canonicalLanguage, PortalSessionStorage, PortalState, PortalSync, PortalUser, subtitleRank, parsePath, PlaybackPreferences, preferredAudioTrack, reconcileDownloads, Route, resumeActionLabel, resumeForTitle, resumeSummary, savePlayerSettings, seasonPackActionLabel, SettingsField, SubtitleCandidate, subtitleItemLabel, subtitleMenuGroups, SubtitleWarning, UpdateStatus, View } from '@torrent-tv/shared';
import { applyVolumeStep, fractionTarget, resolveEscape, resolveShortcut, ScrubCoalescer, seekTarget, type PlayerCommand } from './shortcuts';
import { OsdLayer, type OsdFeedback } from './osd';
import { CacheCoverage, Events, Settings } from './settings';
import { Icon } from './icons';
import { captureDownloadAnchor, Downloads, restoreDownloadAnchor } from './downloads';
import { Jobs } from './jobs';
import { PortalAccountDialog, PortalSidebarDock, UpdateApplyConfirm, UpdateNotice, UpdateSection, useUpdateController } from './portal';
import { sharedApi } from './shared-api';
import './style.css';

const api = sharedApi();
type ActivePlayer = { download: Download; resumeMs: number; preferences?: PlaybackPreferences };
type DetailTarget = { season?: number; episode?: number };
// The address bar mirrors the view: sidebar navigation pushes an entry,
// search submissions replace in place, and popstate restores from the URL.
// App-pushed entries carry a marker, so closing an overlay can go back in
// history when the current entry is one of ours; a cold-loaded overlay URL
// is instead replaced with the section route.
function pushRoute(route: Route): boolean { const url = buildPath(route); if (location.pathname + location.search === url) return false; window.history.pushState({ filelist: true }, '', url); return true }
function replaceRoute(route: Route) { const url = buildPath(route); if (location.pathname + location.search !== url) window.history.replaceState({ filelist: true }, '', url) }
const emptyState: HouseholdState = { favorites: [], continueWatching: [], recent: [], watched: [] };
const needsEpisodeExpansion = (detail: CatalogDetail) => detail.title.kind === 'series' && detail.seasons.some(season => (season.packSources?.length || 0) > 0 && season.episodes.length === 0);
function formatPlaybackTime(milliseconds: number) { const total = Math.max(0, Math.floor(milliseconds / 1000)); const hours = Math.floor(total / 3600); const minutes = Math.floor(total % 3600 / 60); const seconds = total % 60; return hours ? `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}` : `${minutes}:${String(seconds).padStart(2, '0')}` }
function audioTrackLabel(track: MediaAudioTrack) { const language = languageDisplayName(track.language || '') || track.language?.toUpperCase() || 'Language unavailable'; const channels = track.channels === 1 ? 'Mono' : track.channels === 2 ? 'Stereo' : track.channels && track.channels > 2 ? `${track.channels} channels` : ''; return [track.title, language, track.codec?.toUpperCase(), channels].filter(Boolean).join(' · ') }
// The compatibility stream is a live ffmpeg transcode (video copied, audio
// converted to AAC) served as fragmented MP4. It exists for codecs the
// browser cannot decode (E-AC-3 in most releases). A live transcode is not
// range-addressable, so seeking re-issues the request at the new position.
export function browserPlaybackURL(download: Download, audioTrack = -1, startMs = 0, snapped = false) {
  if (!download.browserStreamUrl) return download.streamUrl;
  const value = new URL(download.browserStreamUrl, location.origin);
  if (audioTrack >= 0) value.searchParams.set('audioTrack', String(audioTrack));
  if (startMs > 0) value.searchParams.set('startMs', String(Math.round(startMs)));
  // startMs is already a keyframe: the server skips its own probe.
  if (snapped) value.searchParams.set('snapped', '1');
  return `${value.pathname}${value.search}`;
}
// Deep links: the player entry records the Managed download, the Source file
// index it plays, and the resume position (only when actually resuming) so a
// refresh or a shared link lands back in the same playback.
export function watchRoute(download: Download, resumeMs: number): Route {
  return { view: 'watch', id: download.id, source: download.fileIndex >= 0 ? download.fileIndex : undefined, t: resumeMs > 0 ? resumeMs : undefined };
}
// Storage access itself can throw or be missing (blocked cookies, odd embeds,
// non-browser runs); the player then runs with defaults and no persistence
// instead of crashing.
function persistedStore(): PortalSessionStorage { try { const store = localStorage; if (store) return store } catch { } return { getItem: () => null, setItem: () => { }, removeItem: () => { } } }
function permanentPersistenceFailure(error: unknown): boolean {
  if (typeof error !== 'object' || error === null || !('status' in error)) return false;
  return error.status === 404 || error.status === 409;
}
const MAX_RECOVER_ATTEMPTS = 3;
const navGroups: { label?: string; items: { id: View; label: string; icon: string }[] }[] = [
  { items: [{ id: 'home', label: 'Home', icon: 'home' }, { id: 'search', label: 'Search', icon: 'search' }] },
  { label: 'My Library', items: [{ id: 'library', label: 'Dashboard', icon: 'library' }, { id: 'continue', label: 'Continue watching', icon: 'play' }, { id: 'favorites', label: 'Favorites', icon: 'heart' }, { id: 'watched', label: 'Watched', icon: 'check' }, { id: 'downloads', label: 'Downloads', icon: 'download' }, { id: 'library-categories', label: 'Categories', icon: 'folder' }] },
  { label: 'Tracker', items: [{ id: 'tracker', label: 'Dashboard', icon: 'tracker' }, { id: 'browse', label: 'Browse', icon: 'grid' }, { id: 'categories', label: 'Categories', icon: 'folder' }] },
  { items: [{ id: 'jobs', label: 'Jobs', icon: 'activity' }, { id: 'events', label: 'Events', icon: 'tracker' }, { id: 'settings', label: 'Settings', icon: 'settings' }] },
];


function Sidebar({ view, onView, dock }: { view: View; onView: (view: View) => void; dock?: ComponentChild }) {
  return <aside class="sidebar"><div class="brand"><span class="brand-mark">FL</span><strong>FileList <span>Streaming</span></strong></div><nav aria-label="Main navigation">{navGroups.map((group, index) => <div class="nav-group" key={index}>{group.label && <p>{group.label}</p>}{group.items.map(item => <button class={view === item.id ? 'selected' : ''} onClick={() => onView(item.id)} aria-current={view === item.id ? 'page' : undefined}><Icon name={item.icon} /><span>{item.label}</span></button>)}</div>)}</nav>{dock}</aside>;
}

function Artwork({ title, kind = 'poster' }: { title: CatalogTitle; kind?: 'poster' | 'backdrop' }) {
  const url = kind === 'poster' ? title.posterUrl : title.backdropUrl;
  return url ? <img src={url} alt="" loading="lazy" /> : <div class="art-fallback"><span>{title.title.slice(0, 1).toUpperCase()}</span></div>;
}

function MediaCard({ title, onOpen, wide = false, progress }: { title: CatalogTitle; onOpen: (title: CatalogTitle) => void; wide?: boolean; progress?: number }) {
  return <button class={`media-card ${wide ? 'wide' : ''}`} onClick={() => onOpen(title)} aria-label={`Open ${title.title}`}><div class="card-art"><Artwork title={title} kind={wide ? 'backdrop' : 'poster'} />{title.ratingVotes ? <span class="rating-badge">★ {title.rating?.toFixed(1)}</span> : null}<MediaBadges state={title.libraryState} />{progress !== undefined && <span class="card-progress"><i style={{ width: `${Math.max(0, Math.min(100, progress))}%` }} /></span>}</div><span class="card-copy"><strong>{title.title}</strong><small>{[title.year || '', title.resolutions[0] || '', title.ratingVotes ? `★ ${title.rating?.toFixed(1)}` : '', `${title.bestSeeders} seeds`].filter(Boolean).join(' · ')}</small></span></button>;
}

function LegacyCard({ item, onOpen }: { item: HouseholdItem; onOpen: (item: HouseholdItem) => void }) {
  if (item.catalog) { const progress = item.durationMs > 0 ? Math.max(0, Math.min(100, item.positionMs / item.durationMs * 100)) : 0; return <button class="media-card wide library-card" onClick={() => onOpen(item)} aria-label={`Open ${item.catalog.title}`}><div class="card-art"><Artwork title={item.catalog} kind="backdrop" /><MediaBadges state={item.catalog.libraryState} />{item.positionMs > 0 && <span class="card-progress"><i style={{ width: `${progress}%` }} /></span>}</div><span class="card-copy"><strong>{item.catalog.title}</strong><small>{[item.catalog.year, item.catalog.resolutions[0], item.watched ? 'Watched' : item.positionMs > 0 ? `${Math.round(progress)}% watched` : 'View details'].filter(Boolean).join(' · ')}</small><small class="library-card-release">{item.seasonNumber && item.episodeNumber ? `S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')} · ` : ''}{item.filePath || item.release.name}</small></span></button> }
  return <button class="media-card wide raw" onClick={() => onOpen(item)}><div class="art-fallback"><span>{item.release.name.slice(0, 1)}</span></div><span class="card-copy"><strong>{item.release.name}</strong><small>{formatBytes(item.release.sizeBytes)} · View details</small></span></button>;
}

function MediaBadges({ state }: { state?: MediaState }) { if (!state?.downloadState || !state.watchState) return null; const download = state.downloadState; const watch = state.watchState; return <span class="media-badges">{download !== 'none' && <span class={`media-badge download ${download}`} title={download === 'downloaded' ? 'Downloaded' : download === 'partial' ? 'Some episodes downloaded' : download === 'error' ? 'Download needs attention' : `Downloading ${Math.round((state.progress || 0) * 100)}%`}><Icon name={download === 'downloaded' ? 'check' : 'download'} /><span>{download === 'downloaded' ? 'Downloaded' : download === 'partial' ? 'Partial' : download === 'error' ? 'Error' : `${Math.round((state.progress || 0) * 100)}%`}</span></span>}{watch !== 'unwatched' && <span class={`media-badge watch ${watch}`} title={watch === 'watched' ? 'Watched' : watch === 'partial' ? 'Some episodes watched' : 'In progress'}><Icon name="check" /><span>{watch === 'watched' ? 'Watched' : watch === 'partial' ? 'Part watched' : 'In progress'}</span></span>}</span> }


function Rail({ title, children, empty, landscape = false }: { title: string; children: any; empty?: string; landscape?: boolean }) { const list = Array.isArray(children) ? children.filter(Boolean) : children; return <section class="rail-section"><div class="section-heading"><h2>{title}</h2></div>{(!list || list.length === 0) ? <p class="empty">{empty || 'Nothing here yet.'}</p> : <div class={`rail ${landscape ? 'landscape' : ''}`}>{list}</div>}</section> }

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

// Tab-trap only: the player's resolveEscape chain owns Escape (fullscreen →
// panel → hide-chrome → close), so no onEscape is declared here. The surface
// is a controls-bearing <video>, a legitimate Tab stop plain overlays lack.
function useModalFocus(root: { current: HTMLElement | null }) {
  useEffect(() => trapFocus(() => root.current, 'button:not([disabled]),input:not([disabled]),select:not([disabled]),video[controls],[tabindex]:not([tabindex="-1"])', () => Array.from(document.querySelectorAll<HTMLElement>('.sidebar,.content'))), []);
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

// Seek commands before the media's duration is known cannot compute a target.
const seekUnavailableHint = 'Seek unavailable — still reading the media.';

export function BrowserPlayer({ active, onClose, onStateChanged, onAdvance }: { active: ActivePlayer; onClose: () => void; onStateChanged: () => void; onAdvance: (preferences: PlaybackPreferences) => Promise<void> }) {
  const defaults: PlaybackPreferences = { audioLanguage: 'en', audioTrackIndex: -1, subtitleLanguage: 'ro', subtitleMode: 'auto' };
  const video = useRef<HTMLVideoElement>(null);
  const root = useRef<HTMLDivElement>(null);
  const lastSaved = useRef(0);
  const lastRendered = useRef(0);
  const retryTimer = useRef(0);
  const mediaRetryTimer = useRef(0);
  const recovering = useRef(false);
  const recoverAttempts = useRef(0);
  const saveFailed = useRef(false);
  const shouldPlay = useRef(true);
  const preferenceRef = useRef<PlaybackPreferences>(active.preferences || defaults);
  const durationRef = useRef(0);
  const [message, setMessage] = useState('Reading media details…');
  const [osd, setOsd] = useState<OsdFeedback | null>(null);
  const [mediaInfo, setMediaInfo] = useState<MediaInfo | null>(null);
  const [selectedAudio, setSelectedAudio] = useState(-1);
  const [position, setPosition] = useState(0);
  const [playing, setPlaying] = useState(false);
  // Volume and mute persist per browser (localStorage): loaded once per mount,
  // saved on every change, applied to whichever audio route is active.
  const [volume, setVolume] = useState(() => loadPlayerSettings(persistedStore()).volume);
  const [muted, setMuted] = useState(() => loadPlayerSettings(persistedStore()).muted);
  const [candidates, setCandidates] = useState<SubtitleCandidate[]>([]);
  const [warnings, setWarnings] = useState<SubtitleWarning[]>([]);
  const [subtitleOpen, setSubtitleOpen] = useState(false);
  const [audioOpen, setAudioOpen] = useState(false);
  // Content position at the head of the current compatibility stream. The
  // element's clock starts at zero for that request, so the logical position
  // is offset + element.currentTime (see logicalPlaybackPosition).
  const offsetRef = useRef(0);
  // Whether offsetRef holds a probed keyframe; forwards snapped=1 so the
  // stream route skips its own probe.
  const snappedRef = useRef(false);
  const [streamOffset, setStreamOffset] = useState(0);
  // True while a compatibility re-request is in flight; suppresses buffering
  // chatter and recovery for the reload window.
  const reloading = useRef(false);
  // Original VTT cue times so offset re-syncs shift from truth, not drift.
  const cueTimes = useRef(new Map<TextTrackCue, { start: number; end: number }>());
  // Pending element seek applied on the next loadedmetadata (track switches
  // that move between native and compatibility routes).
  const pendingSeekRef = useRef(-1);
  // Shortcut transport: the pending scrub commit target (non-null while a
  // coalesced seek is in flight) and the latest restartAt for the coalescer's
  // stable commit callback.
  const scrub = useRef<ScrubCoalescer | null>(null);
  const restartAtRef = useRef<(targetMs: number) => void>(() => { });
  restartAtRef.current = restartAt;
  const [selectedSubtitle, setSelectedSubtitle] = useState('off');
  const [controlsVisible, setControlsVisible] = useState(true);
  const controls = useMemo(() => new ControlsVisibility({ policy: { armWhilePaused: true, statusHolds: true, manualHideSuppressionMs: 500 }, onChange: setControlsVisible }), []);
  controls.setStatus(message !== '');
  controls.setPanelOpen(subtitleOpen || audioOpen);
  useModalFocus(root);

  const currentTrack = mediaInfo?.audioTracks.find(track => track.streamIndex === selectedAudio);
  const browserMode = !!currentTrack && !!mediaInfo && (audioPlaybackRoute(currentTrack.codec) === 'decode' || (mediaInfo.audioTracks.length > 1 && !currentTrack.default));
  const playbackURL = useMemo(() => {
    if (!mediaInfo) return '';
    if (browserMode) return browserPlaybackURL(active.download, selectedAudio, streamOffset, snappedRef.current);
    return active.download.streamUrl;
  }, [active.download.id, mediaInfo, browserMode, selectedAudio, streamOffset]);
  const subtitlePositions = useMemo(() => new Map(candidates.map((candidate, index) => [candidate, index + 1])), [candidates]);
  const subtitleGroups = useMemo(() => subtitleMenuGroups(candidates), [candidates]);
  const save = async () => {
    if (durationRef.current <= 0 || saveFailed.current) return;
    try {
      await api.updatePlayback(active.download.id, logicalPlaybackPosition(offsetRef.current, video.current?.currentTime || 0, durationRef.current), durationRef.current);
      onStateChanged()
    } catch (error) {
      // Position persistence is best-effort: a missing source (404) or a permanent
      // update conflict (409) ends it for this playback instead of hammering the
      // endpoint every ten seconds; transient failures keep the 10s cadence.
      if (permanentPersistenceFailure(error)) saveFailed.current = true;
    }
  };
  const savePreferences = async (value: PlaybackPreferences) => { preferenceRef.current = value; try { preferenceRef.current = await api.updatePlaybackPreferences(active.download.id, value) } catch { } };

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const [saved, info] = await Promise.all([active.preferences ? Promise.resolve(active.preferences) : api.playbackPreferences(active.download.id).catch(() => defaults), api.mediaInfo(active.download.id)]);
        if (cancelled) return;
        preferenceRef.current = saved;
        recoverAttempts.current = 0;
        saveFailed.current = false;
        durationRef.current = info.durationMs;
        const track = preferredAudioTrack(info.audioTracks, saved);
        const initial = Math.min(Math.max(0, active.resumeMs), Math.max(0, info.durationMs - 1000));
        setMediaInfo(info);
        setPosition(initial);
        const chosen = track;
        const useBrowser = !!chosen && (audioPlaybackRoute(chosen.codec) === 'decode' || (info.audioTracks.length > 1 && !chosen.default));
        offsetRef.current = 0;
        snappedRef.current = false;
        if (useBrowser && initial > 0) {
          // The route snaps seeks onto a video keyframe; clock and subtitle
          // offsets must use that effective start, not the requested one.
          try { const snap = await api.snapStreamStart(active.download.id, initial); offsetRef.current = snap.startMs; snappedRef.current = snap.snapped } catch { offsetRef.current = initial }
        }
        setStreamOffset(offsetRef.current);
        setSelectedAudio(track?.streamIndex ?? -1);
        setMessage('Opening stream…');
      } catch (error) {
        if (cancelled) return;
        setMessage(`Preparing media details… ${(error as Error).message}`);
        mediaRetryTimer.current = window.setTimeout(() => void load(), 2000);
      }
    }
    void load();
    return () => { cancelled = true; window.clearTimeout(mediaRetryTimer.current) };
  }, [active.download.id]);

  useEffect(() => () => { window.clearTimeout(retryTimer.current); if (clickTimer.current) window.clearTimeout(clickTimer.current); scrub.current?.cancel(); void save() }, [active.download.id]);
  // Subtitle cues carry content-time timestamps; the compatibility stream's
  // element clock starts at the requested offset, so shift cues to match.
  function syncSubtitleOffset(offset: number) {
    video.current?.querySelectorAll<HTMLTrackElement>('track[data-filelist]').forEach(element => {
      for (const cue of Array.from(element.track?.cues || [])) {
        let original = cueTimes.current.get(cue);
        if (!original) { original = { start: cue.startTime, end: cue.endTime }; cueTimes.current.set(cue, original) }
        cue.startTime = Math.max(0, original.start - offset / 1000);
        cue.endTime = Math.max(cue.startTime + .01, original.end - offset / 1000);
      }
    });
  }

  // Loudness truth lives in persisted React state; this effect pushes it to
  // whichever output owns audio — the decoder gain during a decode session,
  // the element otherwise. The element's muted flag while decoding is the
  // controller's implementation detail and is never mirrored back into the
  // UI (that mirror used to show 0 while the decoder played at full gain).
  useEffect(() => {
    const element = video.current;
    if (!element) return;
    element.volume = volume; element.muted = muted;
  }, [volume, muted]);
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const saved = active.preferences || await api.playbackPreferences(active.download.id);
        if (cancelled) return;
        preferenceRef.current = saved;
        const page = await api.subtitles(active.download.id, saved.subtitleLanguage || 'ro', 'all');
        if (cancelled) return;
        setCandidates(page.items); setWarnings(page.warnings || []);
        if (saved.subtitleMode === 'off') { disableSubtitles(false); return }
        if (saved.subtitleMode === 'selected' && saved.subtitleProvider && saved.subtitleCandidateId) {
          const remembered = page.items.find(candidate => candidate.provider === saved.subtitleProvider && candidate.id === saved.subtitleCandidateId);
          if (remembered && await chooseSubtitle(remembered, true, false)) return;
        }
        const preferred = [...page.items].sort((a, b) => subtitleRank(a.language, preferenceRef.current.subtitleLanguage || 'ro') - subtitleRank(b.language, preferenceRef.current.subtitleLanguage || 'ro'));
        for (const candidate of preferred) if (await chooseSubtitle(candidate, true, true)) return;
        setMessage((page.warnings || []).map(w => `${w.provider}: ${w.message}`).join(' · ') || 'No Romanian or English subtitle was found.');
      } catch (error) { if (!cancelled) setMessage(`Subtitles unavailable: ${(error as Error).message}`) }
    })();
    return () => { cancelled = true };
  }, [active.download.id]);
  // Player chrome auto-hide: the shared controller holds controls while a panel
  // or a status message is shown; otherwise 2 idle seconds hide them until the
  // next mouse move or key press, windowed and fullscreen alike.
  useEffect(() => {
    if (controlsVisible) controls.refresh();
    return () => controls.dispose();
  }, [controlsVisible, subtitleOpen, audioOpen, message, controls]);
  useEffect(() => {
    const element = root.current;
    if (!element) return;
    element.addEventListener('mousemove', revealControls);
    // Shortcuts dispatch at document level: the player is a modal surface
    // (background inert) and focus may legitimately rest on body — e.g. right
    // after clicking the video — where a root-level listener never fires.
    document.addEventListener('keydown', onKey);
    return () => { element.removeEventListener('mousemove', revealControls); document.removeEventListener('keydown', onKey) };
  }, [onKey]);
  // Fullscreen truth: the browser can exit fullscreen without delivering the
  // key to the page, so the Escape chain follows the fullscreenchange event.
  const [fullscreen, setFullscreen] = useState(false);
  useEffect(() => {
    const sync = () => setFullscreen(Boolean(document.fullscreenElement));
    sync();
    document.addEventListener('fullscreenchange', sync);
    return () => document.removeEventListener('fullscreenchange', sync);
  }, []);

  async function chooseSubtitle(candidate: SubtitleCandidate, automatic = false, persist = true) {
    try {
      setMessage(automatic ? 'Selecting subtitles…' : 'Preparing subtitle…');
      const asset = await api.prepareSubtitle(active.download.id, candidate.provider, candidate.id, 'vtt');
      const element = document.createElement('track');
      element.kind = 'subtitles'; element.label = candidate.title || candidate.language || 'Subtitle'; element.srclang = candidate.language || 'und'; element.src = api.streamURL(asset.url); element.default = true; element.dataset.filelist = 'true';
      video.current?.querySelectorAll('track[data-filelist]').forEach(track => track.remove()); video.current?.appendChild(element);
      element.addEventListener('load', () => { if (element.track) { syncSubtitleOffset(offsetRef.current); element.track.mode = 'showing' } setSelectedSubtitle(`${candidate.provider}:${candidate.id}`); setMessage('') }, { once: true });
      element.addEventListener('error', () => setMessage('The subtitle is cached, but this browser could not load it.'), { once: true });
      setSubtitleOpen(false); root.current?.focus();
      if (persist) await savePreferences({ ...preferenceRef.current, subtitleLanguage: candidate.language || 'en', subtitleProvider: candidate.provider, subtitleCandidateId: candidate.id, subtitleMode: 'selected' });
      return true;
    } catch (error) { if (!automatic) setMessage(`Subtitle failed: ${(error as Error).message}`); return false }
  }
  function disableSubtitles(persist = true) { if (video.current) Array.from(video.current.textTracks).forEach(track => track.mode = 'disabled'); setSelectedSubtitle('off'); setSubtitleOpen(false); root.current?.focus(); if (persist) void savePreferences({ ...preferenceRef.current, subtitleMode: 'off', subtitleProvider: '', subtitleCandidateId: '' }) }
  // Playback recovery retries are bounded: after MAX_RECOVER_ATTEMPTS consecutive
  // failures the loop stops and the viewer sees a terminal message instead of the
  // player reloading the stream every two seconds forever.
  async function recover() {
    if (recovering.current || reloading.current) return;
    recovering.current = true;
    setMessage('Waiting for the next downloaded segment…');
    retryTimer.current = window.setTimeout(async () => {
      try {
        const latest = (await api.downloads()).items.find(item => item.id === active.download.id);
        if (!latest) throw new Error('The download is no longer managed.');
        setMessage(latest.playbackMode === 'progressive' ? `Streaming while downloading · ${Math.round(latest.progress * 100)}%` : 'Downloaded file ready · retrying playback…');
        video.current?.load();
        await video.current?.play();
        recovering.current = false
      } catch (error) {
        recovering.current = false;
        recoverAttempts.current++;
        const detail = `Playback retry failed: ${(error as Error).message}`;
        if (recoverAttempts.current >= MAX_RECOVER_ATTEMPTS) { setMessage(`${detail} This stream is no longer available.`); return }
        setMessage(detail);
        void recover()
      }
    }, 2000)
  }
  async function restartAt(value: number) {
    if (!mediaInfo || !video.current) return;
    const target = seekTarget(value, mediaInfo.durationMs, 0);
    if (!browserMode) { video.current.currentTime = target / 1000; setPosition(target); return }
    reloading.current = true;
    let start = target;
    let snapped = false;
    try { const snap = await api.snapStreamStart(active.download.id, target); start = snap.startMs; snapped = snap.snapped } catch { }
    offsetRef.current = start;
    snappedRef.current = snapped;
    syncSubtitleOffset(start);
    setPosition(start);
    setStreamOffset(start);
  }
  async function chooseAudio(track: MediaAudioTrack) {
    if (!mediaInfo) return;
    const nextBrowser = audioPlaybackRoute(track.codec) === 'decode' || (mediaInfo.audioTracks.length > 1 && !track.default);
    if (nextBrowser || browserMode) {
      const target = logicalPlaybackPosition(offsetRef.current, video.current?.currentTime || 0, durationRef.current);
      if (nextBrowser) {
        let start = target;
        let snapped = false;
        try { const snap = await api.snapStreamStart(active.download.id, target); start = snap.startMs; snapped = snap.snapped } catch { }
        offsetRef.current = start;
        snappedRef.current = snapped;
      } else {
        offsetRef.current = 0;
        snappedRef.current = false;
      }
      pendingSeekRef.current = nextBrowser ? -1 : target;
      setStreamOffset(offsetRef.current);
    }
    setSelectedAudio(track.streamIndex);
    setAudioOpen(false);
    await savePreferences({ ...preferenceRef.current, audioTrackIndex: track.streamIndex, audioLanguage: track.language || 'en' });
  }
  function togglePlayback() { const element = video.current; if (!element) return; if (element.paused) { shouldPlay.current = true; void element.play().catch(error => setMessage(`Playback could not start: ${error.message}`)) } else { shouldPlay.current = false; element.pause() } }
  function revealControls() { controls.reveal() }
  function hideControls() { controls.hide() }
  // Mouse conventions on the video surface: single click toggles playback,
  // double click toggles fullscreen; the deferral window tells them apart.
  // Clicks on the chrome or panels never reach these handlers — they are not
  // descendants of the video element.
  const clickTimer = useRef(0);
  function onVideoClick() {
    if (clickTimer.current) { window.clearTimeout(clickTimer.current); clickTimer.current = 0; return }
    clickTimer.current = window.setTimeout(() => { clickTimer.current = 0; runCommand({ kind: 'toggle-playback' }) }, 250);
  }
  function onVideoDoubleClick() {
    if (clickTimer.current) { window.clearTimeout(clickTimer.current); clickTimer.current = 0 }
    void runCommand({ kind: 'toggle-fullscreen' });
  }
  // Player shortcut dispatch: resolve the keydown at the command layer and
  // execute. Bound keys are consumed with preventDefault so a focused chrome
  // button cannot double-fire; stopPropagation cannot order listeners here —
  // this handler and the focus hooks all sit on the same document node.
  // Unbound keys keep today's reveal behavior.
  function onKey(event: KeyboardEvent) {
    // Native slider/stepper keys win while a form control is focused (ARIA
    // contract); the shortcut layer stays out of the way.
    const target = event.target as HTMLElement | null;
    if (target && (target.tagName === 'INPUT' || target.tagName === 'SELECT' || target.tagName === 'TEXTAREA')) { revealControls(); return }
    if (event.metaKey || event.ctrlKey || event.altKey) { revealControls(); return }
    const resolved = resolveShortcut(event.key, event.repeat, subtitleOpen || audioOpen);
    if (!resolved) { revealControls(); return }
    event.preventDefault();
    event.stopPropagation();
    runCommand(resolved.command);
  }
  function runCommand(command: PlayerCommand) {
    if (command.kind === 'toggle-playback') { togglePlayback(); return }
    if (command.kind === 'toggle-mute') { toggleMuted(); setOsd({ kind: 'mute', muted: !muted }); return }
    if (command.kind === 'play') { shouldPlay.current = true; void video.current?.play().catch(() => { }); return }
    if (command.kind === 'pause' || command.kind === 'stop') { shouldPlay.current = false; if (video.current && !video.current.paused) video.current.pause(); return }
    if (command.kind === 'open-subtitles') { setSubtitleOpen(true); revealControls(); return }
    if (command.kind === 'seek-fraction') {
      if (!mediaInfo) { setOsd({ kind: 'hint', text: seekUnavailableHint }); return }
      restartAt(fractionTarget(command.fraction, mediaInfo.durationMs));
      setOsd({ kind: 'seek', fraction: command.fraction, hint: `${Math.round(command.fraction * 100)}%` });
      return;
    }
    if (command.kind === 'volume') {
      // Loudness truth lives in persisted state; ↑/↓ while muted unmutes and
      // then applies the step (sliding to zero mutes), matching the slider.
      const stepped = applyVolumeStep(volume, command.delta);
      setVolume(stepped.volume);
      setMuted(stepped.muted);
      savePlayerSettings(persistedStore(), stepped);
      setOsd({ kind: 'volume', percent: Math.round(stepped.volume * 100) }); return;
    }
    if (command.kind === 'toggle-fullscreen') { void toggleFullscreen(); return }
    if (command.kind === 'escape') {
      // Graceful exit: peel one layer per press instead of closing outright.
      const step = resolveEscape({ fullscreen, panelOpen: subtitleOpen || audioOpen, controlsVisible });
      if (step === 'exit-fullscreen') { if (document.fullscreenElement) void document.exitFullscreen().catch(() => { }); return }
      if (step === 'close-panel') { setSubtitleOpen(false); setAudioOpen(false); root.current?.focus(); return }
      if (step === 'hide-chrome') { hideControls(); return }
      onClose();
      return;
    }
    if (command.kind === 'seek') {
      if (!mediaInfo) { setOsd({ kind: 'hint', text: seekUnavailableHint }); return }
      const base = scrub.current?.target ?? position;
      const target = seekTarget(base, mediaInfo?.durationMs ?? 0, command.deltaMs);
      const hint = `${command.deltaMs > 0 ? '+' : '−'}${Math.abs(command.deltaMs) / 1000}s`;
      if (!scrub.current) scrub.current = new ScrubCoalescer(targetMs => restartAtRef.current(targetMs));
      scrub.current.nudge(target);
      setPosition(target);
      setOsd(seekOsd(target, hint));
    }
  }
  // Loudness changes update persisted React state; the sync effect pushes them
  // to the decoder or the element, and the browser store keeps them for the
  // next mount. Sliding to zero mutes, sliding up from zero unmutes.
  function setPlayerVolume(value: number) { const next = clampVolume(value); setVolume(next); setMuted(next === 0); savePlayerSettings(persistedStore(), { volume: next, muted: next === 0 }) }
  function toggleMuted() { setMuted(!muted); savePlayerSettings(persistedStore(), { volume, muted: !muted }) }
  async function toggleFullscreen() { try { if (document.fullscreenElement) await document.exitFullscreen(); else await root.current?.requestFullscreen() } catch (error) { setMessage(`Fullscreen unavailable: ${(error as Error).message}`) } }
  function seekOsd(targetMs: number, hint: string): OsdFeedback {
    return { kind: 'seek', fraction: durationRef.current > 0 ? targetMs / durationRef.current : 0, hint };
  }
  return <div ref={root} tabindex={-1} class={`video ${controlsVisible ? 'controls-visible' : ''}`} role="dialog" aria-modal="true" aria-label={`Playing ${active.download.displayTitle || active.download.filePath}`}>
    <video ref={video} src={playbackURL ? api.streamURL(playbackURL) : undefined} autoplay playsInline onLoadedMetadata={event => { reloading.current = false; const pending = pendingSeekRef.current >= 0 ? pendingSeekRef.current : (!browserMode && active.resumeMs > 0 ? active.resumeMs : -1); pendingSeekRef.current = -1; if (pending > 0) event.currentTarget.currentTime = pending / 1000; if (shouldPlay.current) void event.currentTarget.play().catch(() => setMessage('Press Play to start playback.')) }} onWaiting={() => { if (!reloading.current) setMessage(active.download.playbackMode === 'progressive' ? 'Buffering the next downloaded segment…' : 'Buffering…') }} onCanPlay={() => { recovering.current = false; recoverAttempts.current = 0; if (!reloading.current) setMessage('') }} onPlaying={() => { recovering.current = false; recoverAttempts.current = 0; setPlaying(true); if (!reloading.current) setMessage('') }} onTimeUpdate={event => { if (scrub.current?.target != null) return; const next = logicalPlaybackPosition(offsetRef.current, event.currentTarget.currentTime, durationRef.current); const now = Date.now(); if (now - lastRendered.current >= 250) { lastRendered.current = now; setPosition(next) } if (now - lastSaved.current > 10000) { lastSaved.current = now; void save() } }} onPause={() => { setPlaying(false); void save() }} onEnded={() => void save().then(() => onAdvance(preferenceRef.current))} onError={() => void recover()} onClick={onVideoClick} onDblClick={onVideoDoubleClick} />
    <div class="player-chrome">
      <div class="player-heading"><strong>{active.download.displayTitle || active.download.filePath}</strong><span>{active.download.playbackMode === 'progressive' ? 'Streaming while downloading' : 'Downloaded file'}</span></div>
      <div class="player-scrubber"><input aria-label="Playback position" type="range" min="0" max={mediaInfo?.durationMs || 1} step="1000" value={Math.min(position, mediaInfo?.durationMs || 1)} disabled={!mediaInfo} onInput={event => setPosition(Number(event.currentTarget.value))} onChange={event => restartAt(Number(event.currentTarget.value))} /><div><time>{formatPlaybackTime(position)}</time><time>{mediaInfo ? formatPlaybackTime(mediaInfo.durationMs) : 'Preparing…'}</time></div></div>
      <div class="player-control-row"><button class="player-play" onClick={togglePlayback}>{playing ? 'Pause' : 'Play'}</button><button onClick={() => restartAt(position - 10000)} disabled={!mediaInfo}>−10 seconds</button><button onClick={() => restartAt(position + 10000)} disabled={!mediaInfo}>+10 seconds</button><label class="player-volume"><span>Volume</span><input aria-label="Volume" type="range" min="0" max="1" step="0.05" value={muted ? 0 : volume} onInput={event => setPlayerVolume(Number(event.currentTarget.value))} /></label><button onClick={toggleMuted}>{muted ? 'Unmute' : 'Mute'}</button><button onClick={() => { setSubtitleOpen(false); setAudioOpen(value => !value); revealControls() }} disabled={!mediaInfo}>Audio{currentTrack ? ` · ${audioTrackLabel(currentTrack).split(' · ')[0]}` : ''}</button><button onClick={() => { setAudioOpen(false); setSubtitleOpen(value => !value); revealControls() }}>Subtitles{selectedSubtitle === 'off' ? ' · Off' : ''}</button><button onClick={() => void toggleFullscreen()}>Fullscreen</button><button onClick={hideControls} aria-label="Hide controls">Hide</button><button onClick={() => { void save(); onClose() }} aria-label="Close player">Close</button></div>
    </div>
    {audioOpen && <section class="subtitle-panel audio-panel"><h2>Audio track</h2>{mediaInfo?.audioTracks.map(track => <button class={track.streamIndex === selectedAudio ? 'selected' : ''} onClick={() => void chooseAudio(track)}><strong>{audioTrackLabel(track)}</strong>{track.default && <span>Default track</span>}</button>)}</section>}
    {subtitleOpen && <section class="subtitle-panel"><h2>Subtitles</h2><button class={selectedSubtitle === 'off' ? 'selected' : ''} onClick={() => disableSubtitles()}>Off</button>{subtitleGroups.map(group => <div class="subtitle-group" key={group.key}><p class="subtitle-group-label">{group.label}</p>{group.items.map(candidate => { const position = subtitlePositions.get(candidate) ?? 0; return <button key={`${candidate.provider}:${candidate.id}`} class={selectedSubtitle === `${candidate.provider}:${candidate.id}` ? 'selected' : ''} onClick={() => void chooseSubtitle(candidate)}><strong>{subtitleItemLabel(candidate, position)}</strong><span>{[languageDisplayName(candidate.language), candidate.format || '', candidate.hearingImpaired ? 'hearing impaired' : ''].filter(Boolean).join(' · ')}</span>{candidate.releaseName && <small>{candidate.releaseName}</small>}</button> })}</div>)}{candidates.length === 0 && <p>No matching downloadable subtitles were found.</p>}{warnings.map(w => <p class="danger"><strong>{w.provider}</strong>: {w.message}</p>)}</section>}
    {message && <p class="player-status" role="status">{message}</p>}
    <OsdLayer feedback={osd} onHidden={() => setOsd(null)} />
  </div>;
}

export function App() {
  const initialRoute = parsePath(location.pathname, location.search);
  const [view, setView] = useState<View>(initialRoute.view === 'title' || initialRoute.view === 'watch' ? 'home' : initialRoute.view); const [titles, setTitles] = useState<CatalogTitle[]>([]); const [nextCursor, setNextCursor] = useState<string | null>(null); const [household, setHousehold] = useState<HouseholdState>(emptyState); const [downloads, setDownloads] = useState<Download[]>([]); const [hero, setHero] = useState<CatalogTitle | null>(null); const [detail, setDetail] = useState<CatalogDetail | null>(null); const [detailTarget, setDetailTarget] = useState<DetailTarget>({}); const [picker, setPicker] = useState<CatalogSource[] | null>(null); const [player, setPlayer] = useState<ActivePlayer | null>(null); const [draftQuery, setDraftQuery] = useState(initialRoute.query || ''); const [query, setQuery] = useState(initialRoute.query || ''); const [searching, setSearching] = useState(false); const [updatesAvailable, setUpdatesAvailable] = useState(false); const [category, setCategory] = useState(''); const [kind, setKind] = useState(''); const [resolution, setResolution] = useState(''); const [sort, setSort] = useState('newest'); const [facets, setFacets] = useState<{ categories: string[]; resolutions: string[] }>({ categories: [], resolutions: [] }); const [libraryCategories, setLibraryCategories] = useState<LibraryCategory[]>([]); const [error, setError] = useState(''); const [loading, setLoading] = useState(true); const [settings, setSettings] = useState<Record<string, unknown> | null>(null); const [settingsFields, setSettingsFields] = useState<SettingsField[]>([]); const loadMore = useRef<HTMLDivElement>(null); const requestGeneration = useRef(0); const inFlightCursor = useRef(''); const viewportInput = useRef(0); const catalogParams = useRef({ query, category, kind, resolution, sort }); catalogParams.current = { query, category, kind, resolution, sort };
  const [jobDeepId, setJobDeepId] = useState<string | undefined>(initialRoute.view === 'jobs' ? initialRoute.id : undefined);
  const [portal, setPortal] = useState<PortalState | null>(null);
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus | null>(null);
  const [portalConnected, setPortalConnected] = useState(true);
  const [identity, setIdentity] = useState<PortalUser | null>(null);
  const [accountOpen, setAccountOpen] = useState(false);
  const [portalFailure, setPortalFailure] = useState('');
  const syncRef = useRef<PortalSync | null>(null);
  const openExternal = (url: string) => { window.open(url, '_blank', 'noopener,noreferrer') };
  const updates = useUpdateController({ client: api, status: updateStatus, onStatus: setUpdateStatus });
  const detailRef = useRef<CatalogDetail | null>(null); const loadDownloadsRef = useRef<() => Promise<void>>(async () => { }); detailRef.current = detail;
  const playerRef = useRef<ActivePlayer | null>(null); playerRef.current = player;
  const loadState = async () => { try { setHousehold(await api.state()); } catch (e) { setError((e as Error).message) } };
  const navigate = (next: View) => { setDetail(null); setView(next); setJobDeepId(undefined); pushRoute({ view: next, query: next === 'search' ? query : '' }) };
  // Sidebar navigation away from a dirty Settings page asks first; Settings
  // reports dirtiness through the ref so the check stays synchronous.
  const settingsDirty = useRef(false); const viewRef = useRef(view); viewRef.current = view;
  const [pendingLeave, setPendingLeave] = useState<Route | null>(null);
  const guardedNavigate = (next: View) => {
    if (view === 'settings' && next !== 'settings' && settingsDirty.current) { setPendingLeave({ view: next, query: next === 'search' ? query : '' }); return }
    navigate(next);
  };
  // Every history entry — popped, or replayed after a canceled pop — lands
  // through this one applier.
  const applyRoute = (route: Route) => {
    if (route.view === 'watch') { if (route.id && playerRef.current?.download.id !== route.id) void startWatch(route.id, route.source, route.t); return }
    setPlayer(null);
    if (route.view === 'title') { if (route.id && detailRef.current?.title.id !== route.id) void loadDetail(route.id); return }
    setDetail(null);
    setJobDeepId(route.view === 'jobs' ? route.id : undefined);
    setView(route.view);
    if (route.view === 'search') { setDraftQuery(route.query || ''); setQuery(route.query || '') }
  };
  const confirmLeave = () => {
    settingsDirty.current = false;
    const target = pendingLeave;
    setPendingLeave(null);
    if (!target) return;
    if (target.view === 'watch' || target.view === 'title') { applyRoute(target); return }
    applyRoute(target); pushRoute(target);
  };
  // Closing an overlay goes back in history when the current entry is one the
  // app pushed, so Back never escapes the app; a cold-loaded overlay URL is
  // replaced with the section route instead.
  const closeOverlay = (clear: () => void) => { if ((window.history.state as { filelist?: boolean } | null)?.filelist === true) { window.history.back(); return } clear(); replaceRoute({ view, query: view === 'search' ? query : '' }) };
  const closeDetail = () => closeOverlay(() => setDetail(null));
  const closePlayer = () => closeOverlay(() => setPlayer(null));
  const closeJobDetail = () => closeOverlay(() => { });
  const openJobDetail = (id: string) => { setJobDeepId(id); pushRoute({ view: 'jobs', id }) };
  useOverlayFocus(Boolean(detail) && !picker, closeDetail); useOverlayFocus(Boolean(picker), () => setPicker(null));
  const loadDownloads = async () => { const anchor = captureDownloadAnchor(); const inputVersion = viewportInput.current; try { const incoming = (await api.downloads()).items; setDownloads(current => reconcileDownloads(current, incoming)); window.requestAnimationFrame(() => { if (inputVersion === viewportInput.current) restoreDownloadAnchor(anchor) }) } catch (e) { setError((e as Error).message) } }; loadDownloadsRef.current = loadDownloads;
  const loadTitles = async (append = false, cursor = '') => { if (append && inFlightCursor.current === cursor) return; const generation = append ? requestGeneration.current : ++requestGeneration.current; if (append) inFlightCursor.current = cursor; setLoading(true); const p = catalogParams.current; try { const page = await api.titles({ search: p.query, category: p.category, kind: p.kind, resolution: p.resolution, sort: p.sort, pageSize: 24, cursor }); if (generation !== requestGeneration.current) return; setTitles(current => { if (!append) return page.items; const seen = new Set(current.map(item => item.id)); return [...current, ...page.items.filter(item => !seen.has(item.id))] }); setNextCursor(page.nextCursor); setHero(h => h || page.items[0] || null); void api.ensureMetadata(page.items.slice(0, 12).map(item => item.id)).catch(() => { }); setError(''); } catch (e) { if (generation === requestGeneration.current) setError((e as Error).message) } finally { if (inFlightCursor.current === cursor) inFlightCursor.current = ''; if (generation === requestGeneration.current) setLoading(false) } };
  useEffect(() => { void Promise.all([loadState(), loadTitles(), api.facets().then(f => setFacets(f)).catch(() => { })]); }, []);
  // Portal snapshot and self-update status: the sync controller owns the
  // initial refetch, SSE events, and reconnect recovery, so a replayed
  // event can never override the fresher HTTP snapshot. A failed optional
  // integration simply leaves the state unknown — the UI renders nothing.
  useEffect(() => {
    const sync = new PortalSync({ loadState: () => api.portalState(), loadStatus: () => api.updatesCurrent() });
    syncRef.current = sync;
    const render = () => { setPortal(sync.state); setUpdateStatus(sync.status); setPortalConnected(sync.connected); setPortalFailure(sync.failure) };
    render();
    const unsubscribe = sync.subscribe(render);
    void sync.recover();
    return () => unsubscribe();
  }, []);
  // Stored supporter identity: a stored token must prove itself against
  // /session/me on boot; only a session-invalid answer (401) clears it — a
  // plain network failure (server restarting, outage) keeps the token for
  // the next boot. This is identity state only — the household donor flag
  // lives in the snapshot.
  useEffect(() => {
    const storage = persistedStore();
    const origin = location.origin;
    const stored = loadPortalSession(storage, origin);
    if (!stored) return;
    const controller = new AbortController();
    api.portalMe(stored.token, controller.signal).then(user => setIdentity(user)).catch(error => { const status = (error as { status?: number }).status; if (status === 401) clearPortalSession(storage, origin) });
    return () => controller.abort();
  }, []);
  // A capability loss must also drop the open-flag: a later re-enable then
  // does not spontaneously re-open the dialog the user lost.
  useEffect(() => { if (accountOpen && portal && !portal.accountsEnabled) setAccountOpen(false) }, [accountOpen, portal]);
  // Detail through the same catalog call a tile uses; a /watch URL runs the
  // playDownload path honoring the URL's Source index and resume position.
  useEffect(() => {
    if (initialRoute.view === 'title' && initialRoute.id) void loadDetail(initialRoute.id);
    if (initialRoute.view === 'watch' && initialRoute.id) void startWatch(initialRoute.id, initialRoute.source, initialRoute.t);
  }, []);
  useEffect(() => { void loadTitles(false, ''); }, [query, category, kind, resolution, sort]);
  useEffect(() => { if (!loadMore.current) return; const observer = new IntersectionObserver(entries => { if (entries[0]?.isIntersecting && nextCursor && !loading) void loadTitles(true, nextCursor) }, { rootMargin: '500px' }); observer.observe(loadMore.current); return () => observer.disconnect() }, [nextCursor, loading, query, category, kind, resolution, sort]);
  useEffect(() => { const stream = new EventSource('/api/v1/events'); const catalog = (event: MessageEvent) => { setUpdatesAvailable(true); try { const envelope = JSON.parse(event.data); const payload = typeof envelope.payload === 'string' ? JSON.parse(envelope.payload) : envelope.payload; const titleId = String(payload.titleId || ''); if (titleId && detailRef.current?.title.id === titleId) void api.title(titleId).then(setDetail).catch(e => setError((e as Error).message)) } catch { } }; const metadata = (event: MessageEvent) => { try { const envelope = JSON.parse(event.data); const payload = typeof envelope.payload === 'string' ? JSON.parse(envelope.payload) : envelope.payload; const title = payload.title as CatalogTitle | undefined; if (!title?.id) return; setTitles(current => current.map(item => item.id === title.id ? title : item)); setHero(current => current?.id === title.id ? title : current); setDetail(current => current?.title.id === title.id ? { ...current, title } : current) } catch { } }; const searchComplete = (event: MessageEvent) => { try { const envelope = JSON.parse(event.data); const payload = typeof envelope.payload === 'string' ? JSON.parse(envelope.payload) : envelope.payload; if (String(payload.query || '').toLowerCase() === catalogParams.current.query.toLowerCase()) void loadTitles(false, '') } catch { } }; let lastStreamEventId = 0; const portalEvent = (event: MessageEvent) => { const parsed = eventPayload(event.data); if (!parsed) return; lastStreamEventId = Number(event.lastEventId) > 0 ? Number(event.lastEventId) : lastStreamEventId; syncRef.current?.absorb({ id: parsed.id, kind: parsed.kind, payload: parsed.payload }) }; stream.addEventListener('catalog.updated', catalog as EventListener); stream.addEventListener('catalog.search.completed', searchComplete as EventListener); stream.addEventListener('metadata.updated', metadata as EventListener); stream.addEventListener('portal.state', portalEvent as EventListener); stream.addEventListener('updates.status', portalEvent as EventListener); stream.addEventListener('updates.failed', portalEvent as EventListener); stream.addEventListener('open', () => { void syncRef.current?.recover(lastStreamEventId > 0 ? lastStreamEventId : undefined) }); stream.addEventListener('error', () => syncRef.current?.disconnect()); return () => stream.close() }, []);
  useEffect(() => { if (view === 'library-categories') void api.libraryCategories().then(page => setLibraryCategories(page.items as LibraryCategory[])).catch(e => setError(e.message)); if (view === 'settings') void Promise.all([api.call<Record<string, unknown>>('/settings').then(setSettings), api.call<{ items: SettingsField[] }>('/settings/schema').then(value => setSettingsFields(value.items))]).catch(e => setError(e.message)); }, [view]);
  useEffect(() => { if (view !== 'downloads') return; let stopped = false; let running = false; const refresh = async () => { if (stopped || running) return; running = true; try { await loadDownloadsRef.current() } finally { running = false } }; void refresh(); const timer = window.setInterval(() => void refresh(), 3000); return () => { stopped = true; window.clearInterval(timer) } }, [view]);
  useEffect(() => { const titleId = detail?.title.kind === 'series' ? detail.title.id : ''; if (!titleId) return; let stopped = false; let running = false; const refresh = async () => { if (stopped || running) return; running = true; try { const next = await api.title(titleId); if (!stopped && detailRef.current?.title.id === titleId) setDetail(next) } catch (e) { if (!stopped) setError((e as Error).message) } finally { running = false } }; const timer = window.setInterval(() => void refresh(), 3000); return () => { stopped = true; window.clearInterval(timer) } }, [detail?.title.id]);
  useEffect(() => { const moved = () => { viewportInput.current++ }; window.addEventListener('wheel', moved, { passive: true }); window.addEventListener('touchmove', moved, { passive: true }); window.addEventListener('keydown', moved); return () => { window.removeEventListener('wheel', moved); window.removeEventListener('touchmove', moved); window.removeEventListener('keydown', moved) } }, []);
  useEffect(() => {
    const restore = () => {
      const route = parsePath(location.pathname, location.search);
      // Popping away from dirty Settings cancels the pop: the settings entry
      // goes back on top (keeping the app's history marker) and the popped
      // route is held for the leave confirm to replay.
      if (viewRef.current === 'settings' && settingsDirty.current && route.view !== 'settings') { pushRoute({ view: 'settings', query: '' }); setPendingLeave(route); return }
      applyRoute(route);
    };
    window.addEventListener('popstate', restore);
    return () => window.removeEventListener('popstate', restore);
  }, []);
  async function loadDetail(id: string, target: DetailTarget = {}, stub?: CatalogTitle) {
    setDetailTarget(target);
    if (stub) setHero(stub);
    try {
      const next = await api.title(id);
      setDetail(next);
      pushRoute({ view: 'title', id });
      if (needsEpisodeExpansion(next)) void api.refreshTitle(id, (stub || next.title).title).catch(e => setError((e as Error).message))
    } catch (e) { setError((e as Error).message) }
  }
  async function openTitle(title: CatalogTitle, target: DetailTarget = {}) { await loadDetail(title.id, target, title) }
  async function openLibraryItem(item: HouseholdItem) { const id = item.titleId || item.catalog?.id; if (!id) { setError('This library item is not linked to a catalog title yet. Refresh the catalog and try again.'); return } const title = item.catalog || { id, title: item.release.name, kind: 'movie', categories: [], resolutions: [], sourceCount: 1, bestSeeders: item.release.seeders, largestSizeBytes: item.release.sizeBytes, libraryState: { downloadState: 'none', watchState: 'unwatched' } } as CatalogTitle; await openTitle(title, { season: item.seasonNumber, episode: item.episodeNumber }) }
  async function prepare(source: CatalogSource, resumeMs = 0) { try { setPicker(null); const d = await api.prepare(source.release.id, source.fileIndex ?? -1); if (!resumeMs) resumeMs = await api.playback(d.id).then(p => p.watched ? 0 : p.positionMs).catch(() => 0); setPlayer({ download: d, resumeMs }); pushRoute(watchRoute(d, resumeMs)); } catch (e) { setError((e as Error).message) } }
  function playDetail(d: CatalogDetail) { const sources = d.title.kind === 'movie' ? d.sources : d.seasons[0]?.episodes[0]?.sources || []; if (sources.length === 1) void prepare(sources[0]); else setPicker(sources) }
  async function playLegacy(item: HouseholdItem) { try { const d = await api.prepare(item.release.id, item.fileIndex); const resumeMs = item.watched ? 0 : item.positionMs; setPlayer({ download: d, resumeMs }); pushRoute(watchRoute(d, resumeMs)) } catch (e) { setError((e as Error).message) } }
  async function playDownload(download: Download) { const resumeMs = await api.playback(download.id).then(value => value.watched ? 0 : value.positionMs).catch(() => 0); setPlayer({ download, resumeMs }); pushRoute(watchRoute(download, resumeMs)) }
  // Cold watch deep link: resolve the Managed download by id, honor an
  // explicit Source index in the URL by re-preparing only when it differs
  // from the download's own, then the playDownload path — Household resume
  // unless the URL carries t.
  async function startWatch(id: string, source?: number, t?: number) {
    try {
      const found = (await api.downloads()).items.find(item => item.id === id);
      if (!found) throw new Error('This playback link points to a download that is no longer managed.');
      let download = found;
      if (source !== undefined && source !== found.fileIndex) { try { download = await api.prepare(found.releaseId, source) } catch { download = found } }
      const resumeMs = t === undefined ? await api.playback(download.id).then(value => value.watched ? 0 : value.positionMs).catch(() => 0) : Math.max(0, t);
      setPlayer({ download, resumeMs });
    } catch (e) { setError((e as Error).message) }
  }
  async function advanceEpisode(preferences: PlaybackPreferences) { if (!player) return; try { const next = await api.nextEpisode(player.download.id); await Promise.all([loadState(), loadDownloads()]); if (next) { setPlayer({ download: next, resumeMs: 0, preferences: { ...preferences, sourceId: next.id, subtitleMode: preferences.subtitleMode === 'off' ? 'off' : 'auto', subtitleProvider: '', subtitleCandidateId: '' } }); replaceRoute(watchRoute(next, 0)) } else closePlayer() } catch (e) { setError(`Could not start the next episode: ${(e as Error).message}`); closePlayer() } }
  async function downloadSeason(source: CatalogSource, season: number) { try { await api.prepareSeason(source.release.id, season); await loadDownloads(); if (detailRef.current) { const next = await api.title(detailRef.current.title.id); setDetail(next); setDetailTarget({ season }) } } catch (e) { setError((e as Error).message) } }
  async function manageSeasonPack(source: CatalogSource, season: number, action: 'download' | 'pause' | 'resume' | 'retry' | 'delete') { try { if (action === 'download' || (action === 'retry' && !source.libraryState?.downloadId)) { await downloadSeason(source, season); return } const id = source.libraryState?.downloadId; if (!id) throw new Error('This season download is not registered yet. Refresh the title and try again.'); if (action === 'delete') await api.deleteDownload(id); else await api.call(`/downloads/${encodeURIComponent(id)}/${action}`, { method: 'POST' }); await loadDownloads(); if (detailRef.current) setDetail(await api.title(detailRef.current.title.id)) } catch (e) { setError((e as Error).message); throw e } }
  async function remove(d: Download) { try { await api.deleteDownload(d.id); await loadDownloads() } catch (e) { setError((e as Error).message); throw e } }
  async function manageDownload(d: Download, action: DownloadTransferAction) { try { await api.call(`/downloads/${encodeURIComponent(d.id)}/${action}`, { method: 'POST' }); await loadDownloads() } catch (e) { setError((e as Error).message); throw e } }
  async function submitSearch(event?: Event, valueOverride?: string) { event?.preventDefault(); const value = (valueOverride ?? draftQuery).trim(); if (value === query) { void loadTitles(false, ''); return } setSearching(true); setError(''); try { if (value) { const page = await api.searchTitles(value); setTitles(page.items); setNextCursor(page.nextCursor); setHero(page.items[0] || null); void api.ensureMetadata(page.items.slice(0, 12).map(item => item.id)).catch(() => { }); } setQuery(value); setUpdatesAvailable(false); if (view === 'search') { replaceRoute({ view: 'search', query: value }) } else { setView('search'); pushRoute({ view: 'search', query: value }) } } catch (e) { setError((e as Error).message) } finally { setSearching(false) } }
  async function applyUpdates() { setUpdatesAvailable(false); await loadTitles(false, '') }
  const pageTitle = navGroups.flatMap(g => g.items).find(i => i.id === view)?.label || 'Home';
  const continueWatching = canonicalHouseholdItems(household.continueWatching); const favorites = canonicalHouseholdItems(household.favorites); const recent = canonicalHouseholdItems(household.recent); const watched = canonicalHouseholdItems(household.watched);
  const libraryItems = view === 'continue' ? continueWatching : view === 'favorites' ? favorites : view === 'watched' ? watched : [];
  const showCatalog = ['tracker', 'browse', 'categories', 'search'].includes(view);
  return <div class="app-shell"><Sidebar view={view} onView={guardedNavigate} dock={<PortalSidebarDock snapshot={portal} client={api} identity={identity} onOpenAccount={() => setAccountOpen(true)} openExternal={openExternal} />} /><main class="content" id="content"><header class="topbar"><div><h1>{pageTitle}</h1><p>{view === 'home' ? 'Your private screening archive' : view === 'browse' ? 'Every title, grouped and ready to compare' : ''}</p></div><button class="avatar" aria-label="Household profile">H</button></header>{error && <div class="error" role="alert"><strong>Something needs attention</strong><span>{error}</span><button onClick={() => setError('')}>Dismiss</button></div>}{updateStatus?.available && <UpdateNotice status={updateStatus} controller={updates} openExternal={openExternal} />}
    {view === 'home' && <><Hero title={hero} onOpen={openTitle} /><Rail title="Continue watching" empty="Start a movie or episode and it will appear here." landscape>{continueWatching.map(i => <LegacyCard item={i} onOpen={openLibraryItem} />)}</Rail><Rail title="Recently added">{titles.slice(0, 12).map(t => <MediaCard title={t} onOpen={openTitle} />)}</Rail><Rail title="Favorites" empty="Favorite a title to keep it close." landscape>{favorites.map(i => <LegacyCard item={i} onOpen={openLibraryItem} />)}</Rail></>}
    {view === 'library' && <><Rail title="Continue watching" landscape>{continueWatching.map(i => <LegacyCard item={i} onOpen={openLibraryItem} />)}</Rail><Rail title="Recently viewed" landscape>{recent.map(i => <LegacyCard item={i} onOpen={openLibraryItem} />)}</Rail><Rail title="Watched" landscape>{watched.map(i => <LegacyCard item={i} onOpen={openLibraryItem} />)}</Rail></>}
    {['continue', 'favorites', 'watched'].includes(view) && <Rail title={pageTitle} empty={`No ${pageTitle.toLowerCase()} yet.`} landscape>{libraryItems.map(i => <LegacyCard item={i} onOpen={openLibraryItem} />)}</Rail>}
    {showCatalog && <><CatalogTools draftQuery={draftQuery} setDraftQuery={setDraftQuery} query={query} searching={searching} onSubmit={submitSearch} category={category} setCategory={setCategory} kind={kind} setKind={setKind} resolution={resolution} setResolution={setResolution} sort={sort} setSort={setSort} facets={facets} />{updatesAvailable && <button class="catalog-update" onClick={() => void applyUpdates()}>Catalog updates available · Refresh</button>}{view === 'categories' ? <CategoryGrid categories={facets.categories} onSelect={c => { setCategory(c); navigate('browse') }} /> : view === 'tracker' ? <><Rail title="Recently added">{titles.slice(0, 12).map(t => <MediaCard title={t} onOpen={openTitle} />)}</Rail><Rail title="Strong swarms">{[...titles].sort((a, b) => b.bestSeeders - a.bestSeeders).slice(0, 12).map(t => <MediaCard title={t} onOpen={openTitle} />)}</Rail></> : <><section class="poster-grid" aria-busy={loading}>{titles.map(t => <MediaCard title={t} onOpen={openTitle} />)}</section>{loading && <section class="poster-grid">{Array.from({ length: 12 }, (_, i) => <div class="skeleton" key={i} />)}</section>}<div ref={loadMore} class="load-more" aria-hidden="true" /></>}</>}
    {view === 'downloads' &&
      <Downloads items={downloads} onRefresh={loadDownloads} onPlay={d => void playDownload(d)} onRemove={remove} onAction={manageDownload} />
    }
    {view === 'library-categories' &&
      <LibraryCategories items={libraryCategories} onOpen={openLibraryItem} />
    }
    {view === 'jobs' && <Jobs onError={setError} deepJobId={jobDeepId} onOpenDetail={openJobDetail} onCloseDetail={closeJobDetail} />
    }
    {view === 'events' && <><CacheCoverage /><Events onError={setError} /></>
    }
    {view === 'settings' && settings && <Settings value={settings} fields={settingsFields} onSaved={setSettings} onError={setError} onDirtyChange={value => { settingsDirty.current = value }} accountsEnabled={portal?.accountsEnabled === true} updateSection={updateStatus ? <UpdateSection client={api} status={updateStatus} connected={portalConnected} failure={portalFailure} controller={updates} openExternal={openExternal} /> : null} />
    }
  </main>{detail && <Detail key={`${detail.title.id}:${detailTarget.season || 0}:${detailTarget.episode || 0}`} detail={detail} target={detailTarget} resume={resumeForTitle(household.continueWatching, detail.title.id)} favorite={household.favorites.some(item => item.titleId === detail.title.id || item.catalog?.id === detail.title.id)} onClose={closeDetail} onPlay={() => playDetail(detail)} onResume={playLegacy} onSource={s => void prepare(s)} onPackAction={manageSeasonPack} onFavorite={async value => { await api.titleFavorite(detail.title.id, value); await loadState(); }} />}{picker && <SourcePicker sources={picker} onClose={() => setPicker(null)} onChoose={s => void prepare(s)} />} {player && <BrowserPlayer key={player.download.id} active={player} onClose={closePlayer} onStateChanged={loadState} onAdvance={advanceEpisode} />}{pendingLeave !== null && <div class="overlay" role="dialog" aria-modal="true" aria-label="Unsaved changes"><section class="help-modal"><h2>Leave with unsaved changes?</h2><p>Changes on the Settings page have not been saved yet.</p><div class="confirm-actions"><button type="button" onClick={() => setPendingLeave(null)}>Keep editing</button><button type="button" class="primary" onClick={confirmLeave}>Discard and leave</button></div></section></div>}{error && <div class="overlay error-overlay" role="alertdialog" aria-modal="true" aria-label="Something needs attention"><section class="error-modal"><h2>Something needs attention</h2><p>{error}</p><div class="confirm-actions"><button type="button" onClick={() => setError('')}>Dismiss</button></div></section></div>}{accountOpen && portal?.accountsEnabled && <PortalAccountDialog client={api} storage={persistedStore()} origin={location.origin} identity={identity} onIdentity={setIdentity} onClose={() => setAccountOpen(false)} />}{updates.phase === 'confirming' && <UpdateApplyConfirm controller={updates} />}</div>;
}

function Hero({ title, onOpen }: { title: CatalogTitle | null; onOpen: (t: CatalogTitle) => void }) { if (!title) return <section class="hero empty-hero"><h2>Connect your catalog</h2><p>Configure FileList in Settings, then return here to browse.</p></section>; return <section class="hero"><div class="hero-art"><Artwork title={title} kind="backdrop" /></div><div class="hero-shade" /><div class="hero-copy"><h2>{title.title}</h2><p class="hero-meta">{[title.year, title.kind === 'series' ? `${title.seasonCount || 0} seasons` : 'Movie', title.resolutions[0], `${title.bestSeeders} seeds`].filter(Boolean).join(' · ')}</p><p>{title.overview || 'Metadata is being prepared. Source facts are available now.'}</p><button class="primary" onClick={() => onOpen(title)}><Icon name="play" /><span>View and play</span></button></div></section> }

function CatalogTools(p: any) { return <form class="catalog-tools" onSubmit={p.onSubmit}><label class="search"><Icon name="search" /><input value={p.draftQuery} onInput={(e: any) => p.setDraftQuery(e.currentTarget.value)} placeholder="Search FileList titles, years or releases" aria-label="Search FileList" /></label><button class="search-submit primary" type="submit" disabled={p.searching}>{p.searching ? 'Searching…' : 'Search'}</button>{p.query && <button type="button" onClick={() => { p.setDraftQuery(''); p.onSubmit(undefined, '') }}>Clear</button>}<select value={p.category} onChange={(e: any) => p.setCategory(e.currentTarget.value)} aria-label="Category"><option value="">All categories</option>{p.facets.categories.map((x: string) => <option>{x}</option>)}</select><select value={p.kind} onChange={(e: any) => p.setKind(e.currentTarget.value)} aria-label="Media type"><option value="">Movies and series</option><option value="movie">Movies</option><option value="series">Series</option></select><select value={p.resolution} onChange={(e: any) => p.setResolution(e.currentTarget.value)} aria-label="Resolution"><option value="">All resolutions</option>{p.facets.resolutions.map((x: string) => <option>{x}</option>)}</select><select value={p.sort} onChange={(e: any) => p.setSort(e.currentTarget.value)} aria-label="Sort"><option value="newest">Recently added</option><option value="title">Title A–Z</option><option value="rating">Highest rated</option><option value="rating-asc">Lowest rated</option><option value="seeders">Most seeders</option><option value="size">Largest size</option></select><span class="search-scope">Search contacts FileList only after submit; filters use the local cache.</span></form> }
function CategoryGrid({ categories, onSelect }: { categories: string[]; onSelect: (c: string) => void }) { return <section class="category-grid">{categories.map(c => <button onClick={() => onSelect(c)}><Icon name="folder" /><strong>{c}</strong><span>Browse category</span></button>)}</section> }

function sourceActionLabel(source: CatalogSource) { return source.libraryState?.downloadState && source.libraryState.downloadState !== 'none' ? 'Play' : 'Play and download' }
type SeasonPackAction = 'download' | 'pause' | 'resume' | 'retry' | 'delete';
function SeasonPackCard({ source, season, open, onToggle, onAction, onDelete }: { source: CatalogSource; season: number; open: boolean; onToggle: () => void; onAction: (source: CatalogSource, season: number, action: SeasonPackAction) => Promise<void>; onDelete: () => void }) {
  const state = source.libraryState; const [busy, setBusy] = useState(''); const managed = Boolean(state?.downloadId); const paused = state?.transferState === 'paused'; const complete = state?.downloadState === 'downloaded'; const error = state?.downloadState === 'error';
  const run = async (action: SeasonPackAction) => { if (busy) return; setBusy(action); try { await onAction(source, season, action) } finally { setBusy('') } };
  return <article class={`season-pack-source ${open ? 'expanded' : ''}`}><button class="season-pack-header" aria-expanded={open} aria-controls={`pack-${source.release.id}`} onClick={onToggle}><span class="season-pack-copy"><strong>{source.parsed.resolution || 'Season pack'}{source.parsed.hdr ? ` · ${source.parsed.hdr}` : ''}</strong><span>{source.release.name}</span><small>{[source.parsed.quality, source.parsed.videoCodec, source.parsed.audio].filter(Boolean).join(' · ') || 'Source details unavailable'}</small></span><span class="season-pack-state"><MediaBadges state={state} /><b>{seasonPackActionLabel(state)}</b><small>{formatBytes(source.release.sizeBytes)} · {source.release.seeders} seeds</small><small>{open ? 'Hide controls' : 'Show controls'}</small></span></button>{open && <div id={`pack-${source.release.id}`} class="season-pack-controls"><progress value={state?.progress || 0} max="1" aria-label="Season download progress" />{!managed && <button class="primary" disabled={Boolean(busy)} onClick={() => void run('download')}>{busy ? 'Starting…' : 'Download season'}</button>}{managed && !complete && !error && <button disabled={Boolean(busy)} onClick={() => void run(paused ? 'resume' : 'pause')}>{busy ? `${paused ? 'Resuming' : 'Pausing'}…` : paused ? 'Resume' : 'Pause'}</button>}{error && <button class="primary" disabled={Boolean(busy)} onClick={() => void run('retry')}>{busy ? 'Retrying…' : 'Retry'}</button>}{managed && <button class="danger-button" disabled={Boolean(busy)} onClick={onDelete}>Delete download</button>}</div>}</article>;
}
function Detail({ detail, target, resume, favorite, onClose, onPlay, onResume, onSource, onPackAction, onFavorite }: { detail: CatalogDetail; target: DetailTarget; resume?: HouseholdItem; favorite: boolean; onClose: () => void; onPlay: () => void; onResume: (item: HouseholdItem) => void; onSource: (s: CatalogSource) => void; onPackAction: (s: CatalogSource, season: number, action: SeasonPackAction) => Promise<void>; onFavorite: (v: boolean) => void }) {
  const initialIndex = Math.max(0, detail.seasons.findIndex(item => item.number === target.season)); const [t, setT] = useState(initialIndex); const [expanded, setExpanded] = useState(target.episode ? `${target.season}:${target.episode}` : ''); const [expandedPack, setExpandedPack] = useState(''); const [pendingPack, setPendingPack] = useState<CatalogSource | null>(null); const [deleting, setDeleting] = useState(false); const season = detail.seasons[t]; const firstSource = detail.title.kind === 'movie' ? detail.sources[0] : season?.episodes[0]?.sources[0]; const canPlay = Boolean(firstSource); useOverlayFocus(Boolean(pendingPack), () => { if (!deleting) setPendingPack(null) }); const confirmDelete = async () => { if (!pendingPack || !season || deleting) return; setDeleting(true); try { await onPackAction(pendingPack, season.number, 'delete'); setPendingPack(null) } finally { setDeleting(false) } };
  return <div class="overlay" role="dialog" aria-modal="true" aria-label={`${detail.title.title} details`}><article class="detail"><button class="close" onClick={onClose}>Close</button><div class="detail-hero"><Artwork title={detail.title} kind="backdrop" /><div /><section><h2>{detail.title.title}</h2><p>{[detail.title.year, detail.title.kind === 'series' ? `${detail.title.seasonCount} seasons` : 'Movie', `${detail.title.bestSeeders} seeds`].filter(Boolean).join(' · ')}</p><MediaBadges state={detail.title.libraryState} /><p>{detail.title.overview || 'Metadata is still being prepared.'}</p><div class="actions">{resume ? <button class="primary" onClick={() => onResume(resume)} aria-label={`${resumeActionLabel(resume, detail.title.kind)} at saved position`}><Icon name="play" />{resumeActionLabel(resume, detail.title.kind)}</button> : canPlay && <button class="primary" onClick={onPlay}><Icon name="play" />{firstSource ? sourceActionLabel(firstSource) : 'Play'}</button>}<button onClick={() => void onFavorite(!favorite)}><Icon name="heart" />{favorite ? 'Remove favorite' : 'Favorite'}</button></div>{resume && <small class="resume-file">{resumeSummary(resume, detail.title.kind)}</small>}</section></div>{detail.title.kind === 'series' ? <><div class="season-tabs">{detail.seasons.map((s, i) => <button class={i === t ? 'selected' : ''} onClick={() => { setT(i); setExpanded(''); setExpandedPack('') }}><span>{s.title}</span><MediaBadges state={s.libraryState} /></button>)}</div>{season?.packSources && season.packSources.length > 0 && <section class="season-pack-actions"><h3>Complete season versions</h3><p>Select a version to review it. Downloads start only from the button inside the expanded version.</p><div>{season.packSources.map(source => <SeasonPackCard key={source.release.id} source={source} season={season.number} open={expandedPack === source.release.id} onToggle={() => setExpandedPack(current => current === source.release.id ? '' : source.release.id)} onAction={onPackAction} onDelete={() => setPendingPack(source)} />)}</div></section>}{season && season.episodes.length > 0 ? <div class="episode-list">{season.episodes.map(e => { const key = `${e.season}:${e.number}`; const open = expanded === key; return <article class={open ? 'expanded' : ''}><button class="episode-select" aria-expanded={open} onClick={() => setExpanded(current => current === key ? '' : key)}><div class="episode-art"><Artwork title={detail.title} kind="backdrop" /></div><span class="episode-copy"><strong>{e.number ? `${e.number}. ` : ''}{e.title}</strong><small>{e.sourceCount} version{e.sourceCount === 1 ? '' : 's'}</small><MediaBadges state={e.libraryState} /><b>{open ? 'Hide versions' : 'Show versions'}</b></span></button>{open && <SourceRows sources={e.sources} onSource={onSource} compact />}</article> })}</div> : <p class="episode-loading" role="status">Preparing the individual episode list. This page updates automatically when it is ready.</p>}</> : <SourceRows sources={detail.sources} onSource={onSource} />}</article>{pendingPack && <div class="overlay" role="dialog" aria-modal="true" aria-labelledby="season-pack-delete-heading"><section class="removal-confirm"><h2 id="season-pack-delete-heading">Delete season download?</h2><p class="release-name">{pendingPack.release.name}</p><p>This removes the shared season torrent from qBittorrent and permanently deletes every episode file in it.</p><div class="confirm-actions"><button disabled={deleting} onClick={() => setPendingPack(null)}>Cancel</button><button class="danger-button" disabled={deleting} onClick={() => void confirmDelete()}>{deleting ? 'Deleting…' : 'Delete download'}</button></div></section></div>}</div>;
}
function SourceRows({ sources, onSource, compact = false }: { sources: CatalogSource[]; onSource: (s: CatalogSource) => void; compact?: boolean }) { const ordered = [...sources].sort((a, b) => { const rank = (source: CatalogSource) => source.libraryState?.downloadState === 'downloaded' ? 0 : source.libraryState?.downloadState && source.libraryState.downloadState !== 'none' ? 1 : 2; return rank(a) - rank(b) || b.release.seeders - a.release.seeders }); return <div class={`source-rows ${compact ? 'compact' : ''}`}>{ordered.map(s => <button onClick={() => onSource(s)}><span class="source-copy"><strong>{s.parsed.resolution || 'Source'}</strong><span class="source-filename">{s.filePath || s.release.name}</span><small>{[s.parsed.hdr, s.parsed.quality, s.parsed.videoCodec].filter(Boolean).join(' · ')}</small></span><span class="source-action"><MediaBadges state={s.libraryState} /><b>{sourceActionLabel(s)}</b><small>{formatBytes(s.fileSizeBytes || s.release.sizeBytes)} · {s.release.seeders} seeds</small></span></button>)}</div> }
function SourcePicker({ sources, onClose, onChoose }: { sources: CatalogSource[]; onClose: () => void; onChoose: (s: CatalogSource) => void }) {
  const [sort, setSort] = useState('seeders'); const ranks: Record<string, number> = { "2160p": 4, "1080p": 3, "720p": 2, "480p": 1 }; const resolution = (value: string) => ranks[value] || 0;
  const sorted = [...sources].sort((a, b) => sort === 'size' ? (b.fileSizeBytes || b.release.sizeBytes) - (a.fileSizeBytes || a.release.sizeBytes) : sort === 'resolution' ? resolution(b.parsed.resolution || '') - resolution(a.parsed.resolution || '') : b.release.seeders - a.release.seeders);
  return <div class="overlay picker" role="dialog" aria-modal="true" aria-label="Choose version"><section><header><h2>Choose version</h2><select value={sort} onChange={event => setSort(event.currentTarget.value)} aria-label="Sort versions"><option value="seeders">Most seeders</option><option value="resolution">Best resolution</option><option value="size">Largest file</option></select><button onClick={onClose}>Close</button></header><SourceRows sources={sorted} onSource={onChoose} /></section></div>
}

function LibraryCategories({ items, onOpen }: { items: LibraryCategory[]; onOpen: (item: HouseholdItem) => void }) { const [selected, setSelected] = useState(''); const [media, setMedia] = useState<HouseholdItem[]>([]); const [message, setMessage] = useState(''); async function open(name: string) { setSelected(name); setMessage('Loading your media…'); try { const page = await api.libraryCategories(name); setMedia(canonicalHouseholdItems(page.items as HouseholdItem[])); setMessage('') } catch (e) { setMessage((e as Error).message) } } return <section><p class="supporting">Downloaded, watched, in-progress, and favorited media grouped by tracker category.</p>{selected && <button onClick={() => { setSelected(''); setMedia([]) }}>All categories</button>}{message && <p role="status">{message}</p>}{selected ? <><div class="section-heading category-heading"><h2>{selected}</h2><span>{media.length} item{media.length === 1 ? '' : 's'}</span></div><div class="library-category-media">{media.map(item => <LegacyCard item={item} onOpen={onOpen} />)}</div>{media.length === 0 && <p class="empty">No media remains in this category.</p>}</> : <section class="category-grid">{items.map(item => <button onClick={() => void open(item.name)}><Icon name="folder" /><strong>{item.name}</strong><span>{item.count} item{item.count === 1 ? '' : 's'}</span></button>)}</section>}</section> }

// Bootstrap when served from index.html; module imports (tests) skip the mount.
const appRoot = document.getElementById('app');
if (appRoot) render(<App />, appRoot);
