export type TrackerRef = { id: string; name: string };
export type TrackerStatus = TrackerRef & {
 enabled: boolean;
 configured: boolean;
 capabilities: { imdbSearch: boolean; seasonFilter: boolean; episodeFilter: boolean; categories: boolean };
};
export interface Page<T> { items: T[]; nextCursor: string | null; total: number; stale?: boolean }
export interface Release { id: string; trackerId: string; trackerName: string; providerId: string; categoryId: string; browseClass: string; name: string; category: string; sizeBytes: number; seeders: number; leechers: number; freeleech: boolean; imdbId?: string }
export type MediaKind = 'movie' | 'series'
export interface ParsedRelease { title: string; sortTitle: string; kind: MediaKind; year?: number; seasonStart?: number; seasonEnd?: number; episodeStart?: number; episodeEnd?: number; episodeTitle?: string; resolution?: string; quality?: string; videoCodec?: string; audio?: string; hdr?: string; edition?: string; releaseGroup?: string }
export type DownloadState = 'none' | 'queued' | 'downloading' | 'partial' | 'downloaded' | 'error'
export type TransferState = 'idle' | 'queued' | 'active' | 'paused' | 'complete' | 'error'
export type WatchState = 'unwatched' | 'inProgress' | 'partial' | 'watched'
export interface MediaState { downloadState: DownloadState; transferState?: TransferState; watchState: WatchState; downloadId?: string; progress?: number; positionMs?: number; durationMs?: number }
export interface CatalogSource { release: Release; parsed: ParsedRelease; fileIndex?: number; filePath?: string; fileSizeBytes?: number; libraryState?: MediaState }
export interface CatalogTitle { id: string; title: string; originalTitle?: string; kind: MediaKind; year?: number; imdbId?: string; overview?: string; posterUrl?: string; backdropUrl?: string; rating?: number; ratingVotes?: number; ratingProvider?: string; trackers: TrackerRef[]; categories: string[]; resolutions: string[]; sourceCount: number; seasonCount?: number; episodeCount?: number; bestSeeders: number; largestSizeBytes: number; newestUpload?: string; sources?: CatalogSource[]; libraryState?: MediaState }
export interface CatalogEpisode { number: number; title: string; season: number; sourceCount: number; sources: CatalogSource[]; libraryState?: MediaState }
export interface CatalogSeason { number: number; title: string; episodeCount: number; episodes: CatalogEpisode[]; packSources?: CatalogSource[]; libraryState?: MediaState }
export interface CatalogDetail { title: CatalogTitle; seasons: CatalogSeason[]; sources: CatalogSource[] }
export interface CatalogFacets { categories: string[]; kinds: string[]; resolutions: string[]; hdr: string[]; qualities: string[]; codecs: string[] }
export interface Download { id: string; releaseId: string; trackerId: string; trackerName: string; titleId?: string; displayTitle?: string; releaseName?: string; category?: string; releaseSizeBytes?: number; trackerSeeders?: number; rating?: number; ratingVotes?: number; ratingProvider?: string; parsed?: ParsedRelease; engineId: string; fileIndex: number; filePath: string; mimeType: string; sizeBytes: number; state: string; progress: number; playbackMode: 'local' | 'progressive'; downloadedBytes: number; speedBytesPerSecond: number; uploadSpeedBytesPerSecond?: number; etaSeconds: number; peers: number; seeds: number; leased: boolean; error?: string; createdAt?: string; updatedAt?: string; streamUrl: string; browserStreamUrl?: string }
export interface MediaAudioTrack { streamIndex: number; language?: string; title?: string; codec?: string; channels?: number; default?: boolean }
export interface MediaInfo { durationMs: number; audioTracks: MediaAudioTrack[]; probedAt?: string }
const downloadRenderFingerprint = (item: Download) => [item.releaseId, item.trackerId, item.trackerName, item.titleId, item.displayTitle, item.releaseName, item.category, item.releaseSizeBytes, item.trackerSeeders, item.rating, item.ratingVotes, item.ratingProvider, item.engineId, item.fileIndex, item.filePath, item.mimeType, item.sizeBytes, item.state, item.progress, item.playbackMode, item.downloadedBytes, item.speedBytesPerSecond, item.etaSeconds, item.peers, item.seeds, item.leased, item.error, item.createdAt, item.updatedAt, item.streamUrl, item.parsed?.title, item.parsed?.seasonStart, item.parsed?.episodeStart, item.parsed?.resolution, item.parsed?.quality, item.parsed?.videoCodec, item.parsed?.audio].join('\u0000')
export function reconcileDownloads(current: Download[], incoming: Download[]): Download[] { const unique: Download[] = []; const byID = new Map<string, Download>(); for (const item of incoming) { if (byID.has(item.id)) continue; byID.set(item.id, item); unique.push(item) } if (current.length === 0) return unique; const currentIDs = new Set(current.map(item => item.id)); const added = unique.filter(item => !currentIDs.has(item.id)); const retained: Download[] = []; for (const old of current) { const next = byID.get(old.id); if (!next) continue; retained.push(downloadRenderFingerprint(old) === downloadRenderFingerprint(next) ? old : { ...old, ...next }) } return [...added, ...retained] }
export type DownloadSort = 'recent' | 'title' | 'progress' | 'size' | 'speed'
export function orderDownloadIDs(items: Download[], sort: DownloadSort): string[] { return [...items].sort((a, b) => { const difference = sort === 'title' ? (a.displayTitle || a.filePath).localeCompare(b.displayTitle || b.filePath) : sort === 'progress' ? b.progress - a.progress : sort === 'size' ? b.sizeBytes - a.sizeBytes : sort === 'speed' ? b.speedBytesPerSecond - a.speedBytesPerSecond : Date.parse(b.createdAt || '') - Date.parse(a.createdAt || ''); return difference || a.id.localeCompare(b.id) }).map(item => item.id) }
export type DownloadTransferAction = 'pause' | 'resume' | 'retry';
export interface DownloadTransferActionItem { action: DownloadTransferAction; label: string; pendingLabel: string }
// Which transfer controls a managed download row exposes, decided purely from
// the raw engine state plus the surfaced error. Errored rows retry (a plain
// resume cannot clear a tracker/engine failure), halted rows resume, rows
// transferring or queued to transfer pause; seeding, checking, and unknown
// rows (including the server's transient action markers) expose nothing.
const ACTIVE_TRANSFER_STATES: Record<string, true> = { allocating: true, downloading: true, forceddl: true, forcedmetadl: true, metadl: true, queueddl: true, stalleddl: true };
const HALTED_TRANSFER_STATES: Record<string, true> = { pauseddl: true, pausedup: true, stoppeddl: true, stoppedup: true };
export function downloadTransferActions(download: Pick<Download, 'state' | 'error'>): DownloadTransferActionItem[] { const state = (download.state || '').trim().toLowerCase(); if (download.error || state === 'error' || state === 'missingfiles') return [{ action: 'retry', label: 'Retry download', pendingLabel: 'Retrying…' }]; if (HALTED_TRANSFER_STATES[state]) return [{ action: 'resume', label: 'Resume', pendingLabel: 'Resuming…' }]; if (ACTIVE_TRANSFER_STATES[state]) return [{ action: 'pause', label: 'Pause', pendingLabel: 'Pausing…' }]; return [] }
export interface Job { id: string; trackerId?: string; kind: string; state: string; label: string; dedupeKey: string; progress: number; attempt: number; error?: string; retryable: boolean; nextAttemptAt?: string; createdAt: string; updatedAt: string }
export interface JobLog { id: number; jobId: string; attempt: number; level: string; phase: string; message: string; context?: Record<string, unknown>; createdAt: string }
export interface SearchResult extends Page<CatalogTitle> { job: Job; trackers: TrackerStatus[] }
export interface SettingsField { key: string; label: string; help: string; obtain?: string; tvVisible: boolean; sensitive: boolean; restartRequired: boolean; readOnly?: boolean }
export interface PlaybackState { profileId: string; sourceId: string; releaseId: string; fileIndex: number; filePath: string; positionMs: number; durationMs: number; watched: boolean; updatedAt: string }
export interface PlaybackPreferences { profileId?: string; sourceId?: string; audioLanguage: string; audioTrackIndex: number; subtitleLanguage: string; subtitleProvider?: string; subtitleCandidateId?: string; subtitleMode: 'auto' | 'off' | 'selected'; updatedAt?: string }
export function subtitleRank(language: string, preference = 'ro'): number { const value = canonicalLanguage(language); const wanted = canonicalLanguage(preference); return value === wanted ? 0 : value === 'ro' ? 1 : value === 'en' ? 2 : 3 }
export function preferredAudioTrack(tracks: MediaAudioTrack[], preferences: Pick<PlaybackPreferences, 'audioLanguage' | 'audioTrackIndex'>): MediaAudioTrack | undefined { const wanted = canonicalLanguage(preferences.audioLanguage); return tracks.find(track => track.streamIndex === preferences.audioTrackIndex && (!wanted || canonicalLanguage(track.language) === wanted)) || tracks.find(track => Boolean(wanted) && canonicalLanguage(track.language) === wanted) || tracks.find(track => canonicalLanguage(track.language) === 'en') || tracks.find(track => track.default) || tracks[0] }
// Codec decision (pure): which audio codecs the browser plays through the video element (native) and which are handed to the client decoder.
const NATIVE_AUDIO_CODECS: Record<string, true> = { aac: true, mp3: true, opus: true, flac: true, vorbis: true };
export function audioPlaybackRoute(codec: string | undefined | null): 'native' | 'decode' { const value = (codec ?? '').trim().toLowerCase(); return NATIVE_AUDIO_CODECS[value] ? 'native' : 'decode' }
// Decode-failure chooser: the best natively playable replacement once the given stream indexes have failed to decode, in the preferredAudioTrack order (saved language/index, then default flag, then first). undefined means nothing playable is left and the decode error must stay visible.
export function fallbackAudioTrack(tracks: MediaAudioTrack[], preferences: Pick<PlaybackPreferences, 'audioLanguage' | 'audioTrackIndex'>, failedStreamIndices: readonly number[]): MediaAudioTrack | undefined { const failed = new Set(failedStreamIndices); return preferredAudioTrack(tracks.filter(item => !failed.has(item.streamIndex) && audioPlaybackRoute(item.codec) === 'native'), preferences) }
export function logicalPlaybackPosition(streamOffsetMs: number, currentTimeSeconds: number, durationMs: number): number { const value = Math.max(0, streamOffsetMs + Math.round(Math.max(0, currentTimeSeconds) * 1000)); return durationMs > 0 ? Math.min(value, durationMs) : value }
export interface HouseholdItem extends PlaybackState { release: Release; catalog?: CatalogTitle; favorite: boolean; titleId?: string; seasonNumber?: number; episodeNumber?: number }
export interface HouseholdState { favorites: HouseholdItem[]; continueWatching: HouseholdItem[]; recent: HouseholdItem[]; watched: HouseholdItem[] }
export function canonicalHouseholdItems(items: HouseholdItem[]): HouseholdItem[] { const selected = new Map<string, HouseholdItem>(); const order: string[] = []; for (const item of items) { const key = item.titleId || item.catalog?.id || item.sourceId || item.release.id; const current = selected.get(key); if (!current) { selected.set(key, item); order.push(key); continue } const currentTime = Date.parse(current.updatedAt); const itemTime = Date.parse(item.updatedAt); if ((Number.isFinite(itemTime) ? itemTime : 0) > (Number.isFinite(currentTime) ? currentTime : 0)) selected.set(key, item) } return order.map(key => selected.get(key) as HouseholdItem) }
export function resumeForTitle(items: HouseholdItem[], titleId: string): HouseholdItem | undefined { let selected: HouseholdItem | undefined; let selectedAt = -1; for (const item of items) { if (item.watched || item.positionMs <= 0) continue; if (item.titleId !== titleId && item.catalog?.id !== titleId) continue; const updatedAt = Date.parse(item.updatedAt); const timestamp = Number.isFinite(updatedAt) ? updatedAt : 0; if (!selected || timestamp > selectedAt) { selected = item; selectedAt = timestamp } } return selected }
function formatResumeTime(milliseconds: number): string { const total = Math.max(0, Math.floor(milliseconds / 1000)); const hours = Math.floor(total / 3600); const minutes = Math.floor(total % 3600 / 60); const seconds = total % 60; return hours ? `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}` : `${minutes}:${String(seconds).padStart(2, '0')}` }
export function resumeActionLabel(item: HouseholdItem, kind: MediaKind): string { if (kind !== 'series') return 'Resume'; return item.seasonNumber && item.episodeNumber ? `Resume S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')}` : 'Resume episode' }
export function resumeSummary(item: HouseholdItem, kind: MediaKind): string { const episode = kind === 'series' && item.seasonNumber && item.episodeNumber ? `S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')}` : ''; return [episode, `Continue at ${formatResumeTime(item.positionMs)}`, item.filePath || item.release.name].filter(Boolean).join(' · ') }
export function seasonPackActionLabel(state?: MediaState): string { if (state?.transferState === 'paused') return `Paused at ${Math.round((state.progress || 0) * 100)}%`; switch (state?.downloadState) { case 'downloaded': return 'Downloaded'; case 'error': return 'Retry season download'; case 'downloading': return `Downloading ${Math.round((state.progress || 0) * 100)}%`; case 'queued': return 'Queued'; case 'partial': return 'Continue season download'; default: return 'Download season' } }
export interface LibraryCategory { name: string; count: number }
export interface SubtitleCandidate { id: string; provider: string; providerLabel?: string; language: string; title: string; fileName?: string; releaseName?: string; format?: string; uploader?: string; hearingImpaired?: boolean; description?: string; score: number; cached: boolean }
export interface SubtitleWarning { provider: string; message: string }
export interface SubtitlePage extends Page<SubtitleCandidate> { warnings: SubtitleWarning[] }
export interface SubtitleAsset { id: string; language: string; url: string; format: string; mimeType: string }
// Exported for the settings UI's searchable language dropdowns; the map
// itself stays the canonical subtitle/audio display-name source.
export const LANGUAGE_NAMES: Record<string, string> = { aa: 'Afar', ab: 'Abkhazian', ae: 'Avestan', af: 'Afrikaans', ak: 'Akan', am: 'Amharic', an: 'Aragonese', ar: 'Arabic', as: 'Assamese', av: 'Avaric', ay: 'Aymara', az: 'Azerbaijani', ba: 'Bashkir', be: 'Belarusian', bg: 'Bulgarian', bi: 'Bislama', bm: 'Bambara', bn: 'Bengali', bo: 'Tibetan', br: 'Breton', bs: 'Bosnian', ca: 'Catalan', ce: 'Chechen', ch: 'Chamorro', co: 'Corsican', cr: 'Cree', cs: 'Czech', cu: 'Church Slavic', cv: 'Chuvash', cy: 'Welsh', da: 'Danish', de: 'German', dv: 'Divehi', dz: 'Dzongkha', ee: 'Ewe', el: 'Greek', en: 'English', eo: 'Esperanto', es: 'Spanish', et: 'Estonian', eu: 'Basque', fa: 'Persian', ff: 'Fulah', fi: 'Finnish', fj: 'Fijian', fo: 'Faroese', fr: 'French', fy: 'Western Frisian', ga: 'Irish', gd: 'Scottish Gaelic', gl: 'Galician', gn: 'Guarani', gu: 'Gujarati', gv: 'Manx', ha: 'Hausa', he: 'Hebrew', hi: 'Hindi', ho: 'Hiri Motu', hr: 'Croatian', ht: 'Haitian', hu: 'Hungarian', hy: 'Armenian', hz: 'Herero', ia: 'Interlingua', id: 'Indonesian', ie: 'Interlingue', ig: 'Igbo', ii: 'Sichuan Yi', ik: 'Inupiaq', io: 'Ido', is: 'Icelandic', it: 'Italian', iu: 'Inuktitut', ja: 'Japanese', jv: 'Javanese', ka: 'Georgian', kg: 'Kongo', ki: 'Kikuyu', kj: 'Kuanyama', kk: 'Kazakh', kl: 'Kalaallisut', km: 'Khmer', kn: 'Kannada', ko: 'Korean', kr: 'Kanuri', ks: 'Kashmiri', ku: 'Kurdish', kv: 'Komi', kw: 'Cornish', ky: 'Kirghiz', la: 'Latin', lb: 'Luxembourgish', lg: 'Ganda', li: 'Limburgan', ln: 'Lingala', lo: 'Lao', lt: 'Lithuanian', lu: 'Luba-Katanga', lv: 'Latvian', mg: 'Malagasy', mh: 'Marshallese', mi: 'Maori', mk: 'Macedonian', ml: 'Malayalam', mn: 'Mongolian', mr: 'Marathi', ms: 'Malay', mt: 'Maltese', my: 'Burmese', na: 'Nauru', nv: 'Navajo', nd: 'North Ndebele', ne: 'Nepali', ng: 'Ndonga', nl: 'Dutch', nn: 'Norwegian Nynorsk', nb: 'Norwegian Bokmål', no: 'Norwegian', nr: 'South Ndebele', ny: 'Chichewa', oc: 'Occitan', oj: 'Ojibwa', om: 'Oromo', or: 'Oriya', os: 'Ossetian', pa: 'Panjabi', pi: 'Pali', pl: 'Polish', ps: 'Pushto', pt: 'Portuguese', qu: 'Quechua', rm: 'Romansh', rn: 'Rundi', ro: 'Romanian', ru: 'Russian', rw: 'Kinyarwanda', sa: 'Sanskrit', sc: 'Sardinian', sd: 'Sindhi', se: 'Northern Sami', sg: 'Sango', si: 'Sinhala', sk: 'Slovak', sl: 'Slovenian', sm: 'Samoan', sn: 'Shona', so: 'Somali', sq: 'Albanian', sr: 'Serbian', ss: 'Swati', st: 'Southern Sotho', su: 'Sundanese', sv: 'Swedish', sw: 'Swahili', ta: 'Tamil', te: 'Telugu', tg: 'Tajik', th: 'Thai', ti: 'Tigrinya', tk: 'Turkmen', tl: 'Tagalog', tn: 'Tswana', to: 'Tonga', tr: 'Turkish', ts: 'Tsonga', tt: 'Tatar', tw: 'Twi', ty: 'Tahitian', ug: 'Uighur', uk: 'Ukrainian', ur: 'Urdu', uz: 'Uzbek', ve: 'Venda', vi: 'Vietnamese', vo: 'Volapük', wa: 'Walloon', wo: 'Wolof', xh: 'Xhosa', yi: 'Yiddish', yo: 'Yoruba', za: 'Zhuang', zh: 'Chinese', zu: 'Zulu' }
const LANGUAGE_639_2: Record<string, string> = { aar: 'aa', abk: 'ab', ave: 'ae', afr: 'af', aka: 'ak', amh: 'am', arg: 'an', ara: 'ar', asm: 'as', ava: 'av', aym: 'ay', aze: 'az', bak: 'ba', bel: 'be', bul: 'bg', bis: 'bi', bam: 'bm', ben: 'bn', bod: 'bo', tib: 'bo', bre: 'br', bos: 'bs', cat: 'ca', che: 'ce', cha: 'ch', cos: 'co', cre: 'cr', ces: 'cs', cze: 'cs', chu: 'cu', chv: 'cv', cym: 'cy', wel: 'cy', dan: 'da', deu: 'de', ger: 'de', div: 'dv', dzo: 'dz', ewe: 'ee', ell: 'el', gre: 'el', eng: 'en', epo: 'eo', spa: 'es', est: 'et', eus: 'eu', baq: 'eu', fas: 'fa', per: 'fa', ful: 'ff', fin: 'fi', fij: 'fj', fao: 'fo', fra: 'fr', fre: 'fr', fry: 'fy', gle: 'ga', gla: 'gd', glg: 'gl', grn: 'gn', guj: 'gu', glv: 'gv', hau: 'ha', heb: 'he', hin: 'hi', hmo: 'ho', hrv: 'hr', scr: 'hr', hat: 'ht', hun: 'hu', hye: 'hy', arm: 'hy', her: 'hz', ina: 'ia', ind: 'id', ile: 'ie', ibo: 'ig', iii: 'ii', ipk: 'ik', ido: 'io', isl: 'is', ice: 'is', ita: 'it', iku: 'iu', jpn: 'ja', jav: 'jv', kat: 'ka', geo: 'ka', kon: 'kg', kik: 'ki', kua: 'kj', kaz: 'kk', kal: 'kl', khm: 'km', kan: 'kn', kor: 'ko', kau: 'kr', kas: 'ks', kur: 'ku', kom: 'kv', cor: 'kw', kir: 'ky', lat: 'la', ltz: 'lb', lug: 'lg', lim: 'li', lin: 'ln', lao: 'lo', lit: 'lt', lub: 'lu', lav: 'lv', mlg: 'mg', mah: 'mh', mri: 'mi', mao: 'mi', mkd: 'mk', mac: 'mk', mal: 'ml', mon: 'mn', mar: 'mr', msa: 'ms', may: 'ms', mlt: 'mt', mya: 'my', bur: 'my', nau: 'na', nav: 'nv', nde: 'nd', nep: 'ne', ndo: 'ng', nld: 'nl', dut: 'nl', nno: 'nn', nob: 'nb', nor: 'no', nbl: 'nr', nya: 'ny', oci: 'oc', oji: 'oj', orm: 'om', ori: 'or', oss: 'os', pan: 'pa', pli: 'pi', pol: 'pl', pus: 'ps', por: 'pt', que: 'qu', roh: 'rm', run: 'rn', ron: 'ro', rum: 'ro', rus: 'ru', kin: 'rw', san: 'sa', srd: 'sc', snd: 'sd', sme: 'se', sag: 'sg', sin: 'si', slk: 'sk', slo: 'sk', slv: 'sl', smo: 'sm', sna: 'sn', som: 'so', sqi: 'sq', alb: 'sq', srp: 'sr', scc: 'sr', ssw: 'ss', sot: 'st', sun: 'su', swe: 'sv', swa: 'sw', tam: 'ta', tel: 'te', tgk: 'tg', tha: 'th', tir: 'ti', tuk: 'tk', tgl: 'tl', tsn: 'tn', ton: 'to', tur: 'tr', tso: 'ts', tat: 'tt', twi: 'tw', tah: 'ty', uig: 'ug', ukr: 'uk', urd: 'ur', uzb: 'uz', ven: 've', vie: 'vi', vol: 'vo', wln: 'wa', wol: 'wo', xho: 'xh', yid: 'yi', yor: 'yo', zha: 'za', zho: 'zh', chi: 'zh', zul: 'zu' }
export function canonicalLanguage(value = ''): string { const primary = value.trim().toLowerCase().split(/[-_]/)[0]; if (/^[a-z]{2}$/.test(primary)) return LANGUAGE_NAMES[primary] ? primary : ''; if (/^[a-z]{3}$/.test(primary)) return LANGUAGE_639_2[primary] || ''; return '' }
export function languageDisplayName(code: string): string { const canonical = canonicalLanguage(code); return canonical ? LANGUAGE_NAMES[canonical] : '' }
export interface SubtitleMenuGroup { key: string; label: string; items: SubtitleCandidate[] }
export function subtitleMenuGroups(candidates: SubtitleCandidate[]): SubtitleMenuGroup[] { const groups: SubtitleMenuGroup[] = []; const byKey = new Map<string, SubtitleMenuGroup>(); for (const candidate of candidates) { const key = candidate.provider === 'embedded' ? 'embedded' : candidate.provider === 'contained' || candidate.cached ? 'local' : candidate.provider; let group = byKey.get(key); if (!group) { group = { key, label: key === 'local' ? 'Local' : key === 'embedded' ? 'Built-in' : candidate.providerLabel || candidate.provider, items: [] }; byKey.set(key, group); groups.push(group) } else if (!group.label && candidate.providerLabel) group.label = candidate.providerLabel; group.items.push(candidate) } return groups.sort((a, b) => (a.key === 'local' ? 0 : a.key === 'embedded' ? 1 : 2) - (b.key === 'local' ? 0 : b.key === 'embedded' ? 1 : 2)) }
export function subtitleItemLabel(candidate: SubtitleCandidate, position: number): string { const name = languageDisplayName(candidate.language); if (name) return name; const title = candidate.title.trim(); if (title) return title; const fileName = candidate.fileName?.trim(); if (fileName) return fileName; const format = candidate.format?.trim(); if (format) return format; return `Unknown ${position}` }
export function formatBytes(value: number): string { if (!Number.isFinite(value) || value < 0) return '—'; const units = ['B', 'KB', 'MB', 'GB', 'TB']; let amount = value, index = 0; while (amount >= 1000 && index < units.length - 1) { amount /= 1000; index++ } const digits = index === 0 ? 0 : 1; return `${new Intl.NumberFormat(undefined, { maximumFractionDigits: digits }).format(amount)} ${units[index]}` }
// Portal integration and self-update DTOs. Tags mirror the Go contract
// owners (internal/application/portal/types.go, internal/application/updates/types.go)
// exactly — including the snake-case session and user fields — so a local
// response decodes without reshaping.
export interface PortalLink { id: number; title: string; url: string; description: string }
export interface PortalState { accountsEnabled: boolean; adsEnabled: boolean; donor: boolean; links: PortalLink[] }
export interface PortalPromotion { id: string; provider: string; title: string; text: string; image: string; screenTime: number }
export interface PortalSession { token: string; expires_at: string }
export interface PortalUser { id: number; email: string; display_name: string; role: string }
export interface UpdateStatus { currentVersion: string; available: boolean; latest?: string; notes?: string; releasedAt?: string; releasesUrl: string; selfUpdate: boolean; applying: boolean }
// Upstream rotation windows read as seconds in practice while the local
// contract says milliseconds; a value below 1000 cannot be a sane
// millisecond hold, so it is read as seconds. The one-second floor keeps a
// bad value from turning the delivery rotation into a refetch storm.
export function promotionScreenTimeMs(screenTime: number): number { const value = Number.isFinite(screenTime) && screenTime > 0 ? screenTime : 0; return Math.max(1000, value < 1000 ? value * 1000 : value) }
// SSE envelope (domain.Event): the wire payload is a JSON string, so both
// layers need parsing. Malformed input answers null instead of throwing —
// a bad frame must never take the stream listener down.
export interface EventEnvelope { id?: number; kind: string; payload: string; createdAt?: string }
export function eventPayload<T = unknown>(raw: string): { id?: number; kind: string; payload: T; createdAt?: string } | null {
 try {
  const envelope = JSON.parse(raw) as EventEnvelope;
  if (typeof envelope?.kind !== 'string') return null;
  const payload = typeof envelope.payload === 'string' ? JSON.parse(envelope.payload) : envelope.payload;
  return { id: typeof envelope.id === 'number' ? envelope.id : undefined, kind: envelope.kind, payload: payload as T, createdAt: envelope.createdAt };
 } catch { return null }
}
// Portal identity (JWT) storage. The key carries the configured server
// origin so a token is never replayed against a different server, an
// expired record clears on sight instead of resending a dead credential,
// and the household donor flag — a snapshot field, not an identity — stays
// a deliberately separate mechanism.
export interface PortalSessionStorage { getItem(key: string): string | null; setItem(key: string, value: string): void; removeItem(key: string): void }
export interface StoredPortalSession { token: string; expiresAt: number }
export const portalSessionKey = (origin: string): string => `filelist.portal.session:${origin}`;
export function savePortalSession(storage: PortalSessionStorage, origin: string, session: Pick<PortalSession, 'token' | 'expires_at'>): StoredPortalSession { const stored: StoredPortalSession = { token: session.token, expiresAt: Date.parse(session.expires_at) }; try { storage.setItem(portalSessionKey(origin), JSON.stringify(stored)) } catch { } return stored }
export function clearPortalSession(storage: PortalSessionStorage, origin: string): void { try { storage.removeItem(portalSessionKey(origin)) } catch { } }
export function loadPortalSession(storage: PortalSessionStorage, origin: string, now = Date.now()): StoredPortalSession | null {
 let raw: string | null = null;
 try { raw = storage.getItem(portalSessionKey(origin)) } catch { }
 if (!raw) return null;
 let stored: StoredPortalSession | null = null;
 try { const parsed = JSON.parse(raw) as StoredPortalSession; if (typeof parsed?.token === 'string' && parsed.token) stored = { token: parsed.token, expiresAt: Number(parsed.expiresAt) } } catch { }
 if (!stored) return null;
 if (Number.isFinite(stored.expiresAt) && stored.expiresAt > 0 && stored.expiresAt <= now) { clearPortalSession(storage, origin); return null }
 return stored;
}
// Reconnect-safe mirror of the portal snapshot and self-update status.
// A reconnect replays recent events while the recovery refetch is in
// flight, so recovery opens a drop window (absorb refuses during it) and
// remembers the stream's last event id; the HTTP refetch is the newest
// write and a stale replay can never override it. Updates failures carry
// a neutral message that the next status event clears.
export type PortalSyncEvent = { id?: number; kind: string; payload: unknown };
// Neutral failure text from an updates.failed payload, narrowed with an
// in-guard instead of a cast so an unexpected shape answers empty.
function updatesFailureMessage(payload: unknown): string {
 if (typeof payload !== 'object' || payload === null || !('message' in payload)) return '';
 const message = payload.message;
 return typeof message === 'string' ? message : '';
}
export class PortalSync {
 private stateValue: PortalState | null = null;
 private statusValue: UpdateStatus | null = null;
 private failureValue = '';
 private connectedValue = true;
 private refreshingValue = false;
 private lastEventId: number | undefined;
 private staleBefore: number | undefined;
 private listeners = new Set<() => void>();
 constructor(private readonly io: { loadState(): Promise<PortalState>; loadStatus(): Promise<UpdateStatus> }) { }
 get state(): PortalState | null { return this.stateValue }
 get status(): UpdateStatus | null { return this.statusValue }
 get failure(): string { return this.failureValue }
 get connected(): boolean { return this.connectedValue }
 get refreshing(): boolean { return this.refreshingValue }
 subscribe(listener: () => void): () => void { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
 private changed(): void { this.listeners.forEach(listener => listener()) }
 absorb(event: PortalSyncEvent): boolean {
  if (this.refreshingValue) return false;
  if (event.id !== undefined && this.staleBefore !== undefined && event.id <= this.staleBefore) return false;
  if (event.id !== undefined) this.lastEventId = this.lastEventId === undefined ? event.id : Math.max(this.lastEventId, event.id);
  if (event.kind === 'portal.state') this.stateValue = event.payload as PortalState;
  else if (event.kind === 'updates.status') { this.statusValue = event.payload as UpdateStatus; this.failureValue = '' }
  else if (event.kind === 'updates.failed') this.failureValue = updatesFailureMessage(event.payload);
  else return false;
  this.changed();
  return true;
 }
 disconnect(): void {
  if (!this.connectedValue) return;
  this.connectedValue = false;
  if (this.lastEventId !== undefined) this.staleBefore = this.staleBefore === undefined ? this.lastEventId : Math.max(this.staleBefore, this.lastEventId);
  this.changed();
 }
 async recover(staleBefore?: number): Promise<void> {
  this.refreshingValue = true;
  if (staleBefore !== undefined) this.staleBefore = this.staleBefore === undefined ? staleBefore : Math.max(this.staleBefore, staleBefore);
  this.changed();
  const [state, status] = await Promise.allSettled([this.io.loadState(), this.io.loadStatus()]);
  if (state.status === 'fulfilled') this.stateValue = state.value;
  if (status.status === 'fulfilled') { this.statusValue = status.value; this.failureValue = '' }
  this.refreshingValue = false;
  this.connectedValue = true;
  this.changed();
 }
}
// Classify an updates/apply failure for the UI: the server maps busy and
// manual-only rejections to 409 and everything else is a neutral problem.
export function updateApplyOutcome(status: number | undefined, message: string): 'busy' | 'manual-only' | 'failed' { if (status === 409) return /manual-only|manual only/i.test(message) ? 'manual-only' : 'busy'; return 'failed' }
export class API {
 base: string;
 constructor(base: string) { this.base = base.replace(/\/$/, '') }
 async call<T>(path: string, init?: RequestInit): Promise<T> { const r = await fetch(`${this.base}/api/v1${path}`, { ...init, headers: { 'Content-Type': 'application/json', ...init?.headers } }); if (!r.ok) { const p = await r.json().catch(() => ({ detail: r.statusText })); throw Object.assign(new Error(p.detail || r.statusText), { status: r.status }) } if (r.status === 204 || r.status === 201) return undefined as T; return r.json() }
 info() { return this.call<{ name: string; instanceName?: string; version: string; apiVersion?: string; configured: boolean; capabilities?: string[] }>('/system/info') }
 latest(category = '') { return this.call<Page<Release>>('/catalog/latest?category=' + encodeURIComponent(category)) }
 search(q: string) { return this.call<Page<Release>>('/catalog/search?query=' + encodeURIComponent(q)) }
 searchTitles(query: string) { return this.call<SearchResult>('/catalog/search', { method: 'POST', body: JSON.stringify({ query }) }) }
 refreshTitle(titleId: string, query = '') { return this.call<Job>(`/catalog/titles/${encodeURIComponent(titleId)}/refresh`, { method: 'POST', body: JSON.stringify({ query }) }) }
 titles(query: Record<string, string | number | boolean | undefined> = {}) { const params = new URLSearchParams(); for (const [key, value] of Object.entries(query)) { if (value !== undefined && value !== '') params.set(key, String(value)) } return this.call<Page<CatalogTitle>>('/catalog/titles?' + params.toString()) }
 title(id: string) { return this.call<CatalogDetail>(`/catalog/titles/${encodeURIComponent(id)}`) }
 facets() { return this.call<CatalogFacets>('/catalog/facets') }
 trackers() { return this.call<TrackerStatus[]>('/trackers') }
 prepare(id: string, fileIndex = -1, signal?: AbortSignal) {
  return this.call<Download>(`/releases/${encodeURIComponent(id)}/prepare`, {
   method: 'POST', body: JSON.stringify({ fileIndex }), signal,
  });
 }
 prepareSeason(id: string, season: number, signal?: AbortSignal) {
  return this.call<Page<Download>>(`/releases/${encodeURIComponent(id)}/prepare-season`, {
   method: 'POST', body: JSON.stringify({ season }), signal,
  });
 }
 downloads() { return this.call<Page<Download>>('/downloads') }
 mediaInfo(id: string) { return this.call<MediaInfo>(`/downloads/${encodeURIComponent(id)}/media-info`) }
 nextEpisode(id: string) { return this.call<Download | null>(`/downloads/${encodeURIComponent(id)}/next-episode`, { method: 'POST' }) }
 deleteDownload(id: string) { return this.call<void>(`/downloads/${encodeURIComponent(id)}`, { method: 'DELETE' }) }
 jobs(query: Record<string, string | number | undefined> = {}) { const params = new URLSearchParams(); for (const [key, value] of Object.entries(query)) { if (value !== undefined && value !== '') params.set(key, String(value)) } return this.call<Page<Job>>('/jobs?' + params.toString()) }
 job(id: string) { return this.call<{ job: Job; logs: Array<JobLog & { at: string }> }>(`/jobs/${encodeURIComponent(id)}`) }
 jobLogs(id: string, cursor = '') { return this.call<Page<JobLog>>(`/jobs/${encodeURIComponent(id)}/logs?pageSize=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`) }
 retryJob(id: string) { return this.call<Job>(`/jobs/${encodeURIComponent(id)}/retry`, { method: 'POST' }) }
 syncCatalog(mode: 'latest' | 'rebuild') { return this.call<Job>('/catalog/sync', { method: 'POST', body: JSON.stringify({ mode }) }) }
 ensureMetadata(titleIds: string[]) { return this.call<{ queued: number }>('/metadata/ensure', { method: 'POST', body: JSON.stringify({ titleIds }) }) }
 diagnostic(level: string, message: string, context: Record<string, unknown> = {}) { return this.call<void>('/diagnostics/client', { method: 'POST', body: JSON.stringify({ level, message, context }) }) }
 snapStreamStart(sourceId: string, startMs: number) { return this.call<{ requested: number; startMs: number; snapped: boolean }>(`/streams/${encodeURIComponent(sourceId)}/snap?startMs=${Math.round(startMs)}`) }
 subtitles(downloadId: string, language = '', scope: 'local' | 'remote' | 'all' = 'all') { return this.call<SubtitlePage>(`/downloads/${encodeURIComponent(downloadId)}/subtitles?language=${encodeURIComponent(language)}&scope=${scope}`) }
 prepareSubtitle(downloadId: string, provider: string, id: string, format: 'sami' | 'vtt' = 'sami') { return this.call<SubtitleAsset>(`/downloads/${encodeURIComponent(downloadId)}/subtitles/prepare`, { method: 'POST', body: JSON.stringify({ provider, id, format }) }) }
 state() { return this.call<HouseholdState>('/state') }
 library(section: 'dashboard' | 'continue-watching' | 'favorites' | 'watched' | 'recent') { return section === 'dashboard' ? this.call<HouseholdState>('/library/dashboard') : this.call<Page<HouseholdItem>>(`/library/${section}`) }
 libraryCategories(category = '') { return category ? this.call<Page<HouseholdItem>>('/library/categories?category=' + encodeURIComponent(category)) : this.call<Page<LibraryCategory>>('/library/categories') }
 favorite(releaseId: string, value: boolean) { return this.call<void>(`/favorites/${encodeURIComponent(releaseId)}`, { method: value ? 'PUT' : 'DELETE' }) }
 titleFavorite(titleId: string, value: boolean) { return this.call<void>(`/library/favorites/${encodeURIComponent(titleId)}`, { method: value ? 'PUT' : 'DELETE' }) }
 playback(sourceId: string) { return this.call<PlaybackState>(`/playback/${encodeURIComponent(sourceId)}`) }
 updatePlayback(sourceId: string, positionMs: number, durationMs: number) { return this.call<PlaybackState>(`/playback/${encodeURIComponent(sourceId)}`, { method: 'PUT', body: JSON.stringify({ positionMs, durationMs }) }) }
 playbackPreferences(sourceId: string) { return this.call<PlaybackPreferences>(`/playback/${encodeURIComponent(sourceId)}/preferences`) }
 updatePlaybackPreferences(sourceId: string, value: PlaybackPreferences) { return this.call<PlaybackPreferences>(`/playback/${encodeURIComponent(sourceId)}/preferences`, { method: 'PUT', body: JSON.stringify(value) }) }
 setWatched(sourceId: string, watched: boolean) { return this.call<PlaybackState>(`/playback/${encodeURIComponent(sourceId)}/watched`, { method: 'PUT', body: JSON.stringify({ watched }) }) }
 portalState() { return this.call<PortalState>('/portal/state') }
 portalPromotions(count = 1, signal?: AbortSignal) { return this.call<PortalPromotion[]>(`/portal/promotions?count=${Math.max(0, Math.round(count))}`, { signal }) }
 promotionClickURL(provider: string, id: string) { return this.streamURL(`/api/v1/portal/promotions/${encodeURIComponent(provider)}/${encodeURIComponent(id)}/click`) }
 portalSession(email: string, password: string, signal?: AbortSignal) { return this.call<PortalSession>('/portal/session', { method: 'POST', body: JSON.stringify({ email, password }), signal }) }
 portalSessionRegister(email: string, password: string, displayName: string, signal?: AbortSignal) { return this.call<void>('/portal/session/register', { method: 'POST', body: JSON.stringify({ email, password, displayName }), signal }) }
 portalMe(token: string, signal?: AbortSignal) { return this.call<PortalUser>('/portal/session/me', { headers: { Authorization: `Bearer ${token}` }, signal }) }
 updatesCurrent() { return this.call<UpdateStatus>('/updates/current') }
 updatesCheck() { return this.call<UpdateStatus>('/updates/check', { method: 'POST' }) }
 updatesApply() { return this.call<UpdateStatus>('/updates/apply', { method: 'POST' }) }
 streamURL(path: string) { return new URL(path, this.base).toString() }
}
export { ControlsVisibility } from './controls-visibility';
export type { ControlsVisibilityOptions, ControlsVisibilityPolicy } from './controls-visibility';
export { DEFAULT_PLAYER_SETTINGS, PLAYER_MUTED_KEY, PLAYER_VOLUME_KEY, clampVolume, loadPlayerSettings, savePlayerSettings } from './player-settings';
export type { PlayerSettings, PlayerSettingsStorage } from './player-settings';
export { buildPath, parsePath } from './routes';
export type { Route, View } from './routes';
