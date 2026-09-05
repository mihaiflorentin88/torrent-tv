import {canonicalLanguage, languageDisplayName, type Download} from '@filelist/shared';

export type PlayerAction = 'left'|'right'|'up'|'down'|'enter'|'back'|'play'|'pause'|'play-pause'|'stop'|'rewind'|'fast-forward'|'previous'|'next'|null;

export function formatTime(milliseconds: number): string {
  const total = Math.max(0, Math.floor(milliseconds / 1000));
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor(total % 3600 / 60);
  const seconds = total % 60;
  return hours > 0 ? `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}` : `${minutes}:${String(seconds).padStart(2, '0')}`;
}

export function clampSeek(position: number, duration: number): number {
  if (!Number.isFinite(duration) || duration <= 0) return Math.max(0, position);
  return Math.min(Math.max(0, position), Math.max(0, duration - 1000));
}

export function isDownloadComplete(download: Download): boolean {
  return download.progress >= 1 || download.state === 'completed' || download.downloadedBytes >= download.sizeBytes;
}

export function playerAction(key: string, keyCode: number): PlayerAction {
  if (key === 'MediaPlay') return 'play';
  if (key === 'MediaPause') return 'pause';
  if (key === 'MediaPlayPause') return 'play-pause';
  if (key === 'MediaStop') return 'stop';
  if (key === 'MediaRewind') return 'rewind';
  if (key === 'MediaFastForward') return 'fast-forward';
  if (key === 'MediaTrackPrevious') return 'previous';
  if (key === 'MediaTrackNext') return 'next';
  if (key === 'GoBack' || key === 'BrowserBack' || keyCode === 27) return 'back';
  if (key === 'ArrowLeft' || keyCode === 37) return 'left';
  if (key === 'ArrowRight' || keyCode === 39) return 'right';
  if (key === 'ArrowUp' || keyCode === 38) return 'up';
  if (key === 'ArrowDown' || keyCode === 40) return 'down';
  if (key === 'Enter' || key === 'Return' || keyCode === 13) return 'enter';
  if (key === 'XF86Back' || key === 'Back' || keyCode === 10009) return 'back';
  if (keyCode === 415) return 'play';
  if (keyCode === 19) return 'pause';
  if (keyCode === 10252) return 'play-pause';
  if (keyCode === 413) return 'stop';
  if (keyCode === 412) return 'rewind';
  if (keyCode === 417) return 'fast-forward';
  if (keyCode === 10232) return 'previous';
  if (keyCode === 10233) return 'next';
  return null;
}

export type HiddenKeyRoute = 'scrub-left'|'scrub-right'|'refocus'|'route';

/** Decision for the centralized key handler while the controls are hidden: every recognized remote key reveals the controls first; directional keys then scrub or restore control focus, and the remaining media/back keys fall through to their normal routing. */
export function hiddenKeyRoute(action: PlayerAction): HiddenKeyRoute | null {
  if (!action) return null;
  if (action === 'left') return 'scrub-left';
  if (action === 'right') return 'scrub-right';
  if (action === 'up' || action === 'down' || action === 'enter') return 'refocus';
  return 'route';
}

export interface AVTrack {index:number; type:string; language:string; label:string}

export function preferredAudio(tracks:AVTrack[],language:string,index:number):AVTrack|null {
  const audio=tracks.filter(track=>track.type==='AUDIO');
  const wanted=canonicalLanguage(language||'en');
  return audio.find(track=>track.index===index&&canonicalLanguage(track.language)===wanted)||audio.find(track=>canonicalLanguage(track.language)===wanted)||audio.find(track=>canonicalLanguage(track.language)==='en')||audio[0]||null;
}

export function normalizeTrack(track: any): AVTrack {
  let extra: Record<string, unknown> = {};
  try {extra = typeof track.extra_info === 'string' ? JSON.parse(track.extra_info) : track.extra_info || {};} catch {}
  const language = String(extra.track_lang || extra.language || extra.Language || extra.lang || extra.LANGUAGE || '').toLowerCase();
  const rawCodec = String(extra.subtitle_type || extra.fourCC || extra.codec || extra.Codec || extra.codec_name || '');
  const codec = /^(text\/plain|text|unknown|0|-1)$/i.test(rawCodec) ? '' : rawCodec;
  const type = String(track.type || '').toUpperCase();
  const title = String(extra.track_title || extra.title || extra.Title || '').trim();
  const hint = languageDisplayName(canonicalLanguage(language));
  const label = [hint, title, codec].filter(Boolean).join(' · ') || `Unknown ${Number(track.index)+1}`;
  return {index: Number(track.index), type, language, label};
}

export function preferredSubtitle(tracks: AVTrack[]): AVTrack | null {
  const text = tracks.filter(track => track.type === 'TEXT');
  return text.find(track => /^(ro|ron|rum)/.test(track.language)) || text.find(track => /^(en|eng)/.test(track.language)) || null;
}

export interface SubtitleCue {start:number; end:number; text:string}

function cueTime(value:string):number {
  const parts=value.trim().replace(',', '.').split(':').map(Number);
  if(parts.some(part=>!Number.isFinite(part)))return -1;
  const seconds=parts.length===3?parts[0]*3600+parts[1]*60+parts[2]:parts[0]*60+parts[1];
  return Math.round(seconds*1000);
}

export function parseVTT(input:string):SubtitleCue[]{
  const blocks=input.replace(/^\uFEFF/,'').replace(/\r/g,'').split(/\n{2,}/);const cues:SubtitleCue[]=[];
  for(const block of blocks){const lines=block.split('\n').filter(Boolean);if(!lines.length||/^WEBVTT/i.test(lines[0])||/^NOTE(?:\s|$)/.test(lines[0]))continue;const timing=lines.findIndex(line=>line.includes('-->'));if(timing<0)continue;const [rawStart,rawEnd]=lines[timing].split('-->');const start=cueTime(rawStart);const end=cueTime(rawEnd.trim().split(/\s+/)[0]);if(start<0||end<=start)continue;const text=lines.slice(timing+1).join('\n').replace(/<[^>]+>/g,'').trim();if(text)cues.push({start,end,text});}
  return cues.sort((a,b)=>a.start-b.start);
}

export function subtitleAt(cues:SubtitleCue[],position:number,delay=0):string {
  const time=position-delay;return cues.filter(cue=>cue.start<=time&&cue.end>=time).map(cue=>cue.text).join('\n');
}
