// Searchable language dropdown for the settings form. A suggesting combobox,
// not a restrictive select: the input keeps whatever text the field holds
// (any previously stored code renders and saves verbatim), while the filtered
// list offers canonical codes to pick — clicking or pressing Enter commits the
// option's exact code. Filter matches on the language name or the code.
import { useEffect, useId, useMemo, useRef, useState } from 'preact/hooks';
import { LANGUAGE_NAMES, canonicalLanguage, languageDisplayName } from '@torrent-tv/shared';

export interface LanguageOption { code: string; label: string }

// The exact set TMDB serves from /configuration/primary_translations (the
// codes its `language` parameter and site language picker accept). Snapshot
// cross-checked against Radarr/Radarr#10482; do not hand-add region pairs.
const TMDB_PRIMARY_TRANSLATIONS = [
 'af-ZA', 'ar-AE', 'ar-BH', 'ar-EG', 'ar-IQ', 'ar-JO', 'ar-LY', 'ar-MA', 'ar-QA', 'ar-SA', 'ar-TD', 'ar-YE',
 'be-BY', 'bg-BG', 'bn-BD', 'br-FR', 'ca-AD', 'ca-ES', 'ch-GU', 'cs-CZ', 'cy-GB', 'da-DK', 'de-AT', 'de-CH',
 'de-DE', 'el-CY', 'el-GR', 'en-AG', 'en-AU', 'en-BB', 'en-BZ', 'en-CA', 'en-CM', 'en-GB', 'en-GG', 'en-GH',
 'en-GI', 'en-GY', 'en-IE', 'en-JM', 'en-KE', 'en-LC', 'en-MW', 'en-NZ', 'en-PG', 'en-TC', 'en-US', 'en-ZM',
 'en-ZW', 'eo-EO', 'es-AR', 'es-CL', 'es-DO', 'es-EC', 'es-ES', 'es-GQ', 'es-GT', 'es-HN', 'es-MX', 'es-NI',
 'es-PA', 'es-PE', 'es-PY', 'es-SV', 'es-UY', 'et-EE', 'eu-ES', 'fa-IR', 'fi-FI', 'fr-BF', 'fr-CA', 'fr-CD',
 'fr-CI', 'fr-FR', 'fr-GF', 'fr-GP', 'fr-MC', 'fr-ML', 'fr-MU', 'fr-PF', 'ga-IE', 'gd-GB', 'gl-ES', 'he-IL',
 'hi-IN', 'hr-HR', 'hu-HU', 'id-ID', 'it-IT', 'it-VA', 'ja-JP', 'ka-GE', 'kk-KZ', 'kn-IN', 'ko-KR', 'ku-TR',
 'ky-KG', 'lt-LT', 'lv-LV', 'ml-IN', 'mr-IN', 'ms-MY', 'ms-SG', 'nb-NO', 'nl-BE', 'nl-NL', 'no-NO', 'pa-IN',
 'pl-PL', 'pt-AO', 'pt-BR', 'pt-MZ', 'pt-PT', 'ro-MD', 'ro-RO', 'ru-RU', 'si-LK', 'sk-SK', 'sl-SI', 'so-SO',
 'sq-AL', 'sq-XK', 'sr-ME', 'sr-RS', 'sv-SE', 'sw-TZ', 'ta-IN', 'te-IN', 'th-TH', 'tl-PH', 'tr-TR', 'uk-UA',
 'ur-PK', 'uz-UZ', 'vi-VN', 'zh-CN', 'zh-HK', 'zh-SG', 'zh-TW', 'zu-ZA',
];

const optionLabel = (code: string) => `${languageDisplayName(code) || canonicalLanguage(code).toUpperCase() || code} — ${code}`;

// TMDB variant: exactly TMDB's primary translations — the region-coded set
// its language parameter accepts — so a picked option is always compatible.
// The list is a suggestion, not a gate: any existing value, including bare
// codes, stays editable and saveable.
export function languageOptions(variant: 'base' | 'tmdb'): LanguageOption[] {
 if (variant === 'base') {
  return Object.keys(LANGUAGE_NAMES).map(code => ({ code, label: optionLabel(code) })).sort((a, b) => a.label.localeCompare(b.label));
 }
 return TMDB_PRIMARY_TRANSLATIONS.map(code => ({ code, label: optionLabel(code) })).sort((a, b) => a.label.localeCompare(b.label));
}

// One-shot option list per variant; the datasets are static so the settings
// form can build them once at module load instead of per render.
const OPTION_CACHE: Record<string, LanguageOption[]> = {};
const optionsFor = (variant: 'base' | 'tmdb'): LanguageOption[] => (OPTION_CACHE[variant] ||= languageOptions(variant));

export function LanguageSelect({ value, variant = 'base', disabled = false, ariaLabel, onChange }: {
 value: string; variant?: 'base' | 'tmdb'; disabled?: boolean; ariaLabel: string; onChange: (code: string) => void;
}) {
 const options = useMemo(() => optionsFor(variant), [variant]);
 const [open, setOpen] = useState(false);
 const [query, setQuery] = useState('');
 const [highlight, setHighlight] = useState(0);
 const rootRef = useRef<HTMLDivElement | null>(null);
 const inputRef = useRef<HTMLInputElement | null>(null);
 const listId = useId();
 const display = languageDisplayName(value) || canonicalLanguage(value).toUpperCase();
 // The query only filters; an untouched field (or a cleared one) shows every
 // option, so opening the dropdown is a browse, not a pre-filtered view.
 const filtered = query ? options.filter(option => option.code.toLowerCase().includes(query.toLowerCase()) || option.label.toLowerCase().includes(query.toLowerCase())) : options;

 useEffect(() => {
  if (!open) return;
  const close = (event: MouseEvent) => {
   if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
  };
  document.addEventListener('mousedown', close);
  return () => document.removeEventListener('mousedown', close);
 }, [open]);

 useEffect(() => { setHighlight(0) }, [query, open]);

 const commit = (code: string) => { onChange(code); setQuery(''); setOpen(false); inputRef.current?.focus() };
 const select = (offset: number) => setHighlight(current => Math.max(0, Math.min(filtered.length - 1, current + offset)));

 const onKeyDown = (event: KeyboardEvent) => {
  if (event.key === 'ArrowDown') { event.preventDefault(); if (!open) { setQuery(''); setOpen(true) } else select(1) }
  else if (event.key === 'ArrowUp' && open) { event.preventDefault(); select(-1) }
  else if (event.key === 'Enter' && open && filtered.length > 0) { event.preventDefault(); commit(filtered[highlight].code) }
  else if (event.key === 'Escape' && open) { event.preventDefault(); setOpen(false); setQuery('') }
 };

 return (
  <span class="language-combobox" ref={rootRef}>
   <input
    ref={inputRef} type="text" disabled={disabled} value={value} title={display} autoComplete="off" spellcheck={false}
    role="combobox" aria-expanded={open} aria-controls={listId} aria-autocomplete="list" aria-label={ariaLabel}
    onFocus={() => { if (!open) { setQuery(''); setOpen(true) } }}
    onKeyDown={onKeyDown}
    onInput={event => { const next = event.currentTarget.value; setQuery(next); onChange(next); setOpen(true) }}
   />
   {open && !disabled && (
    <ul class="language-list" role="listbox" id={listId} aria-label={ariaLabel}>
     {filtered.map((option, index) => (
      <li key={option.code} role="option" aria-selected={option.code === value} class={index === highlight ? 'active' : ''}
       onMouseDown={event => event.preventDefault()}
       // preventDefault cancels the wrapping label's activation behavior (a
       // click on a non-interactive label child is forwarded to the label's
       // first form control — the help button — which would open the help
       // modal on every selection); stopPropagation keeps the label from
       // seeing the click at all.
       onClick={event => { event.preventDefault(); event.stopPropagation(); commit(option.code) }}
       onMouseEnter={() => setHighlight(index)}>
       <span>{option.label}</span>
      </li>
     ))}
     {filtered.length === 0 && <li class="language-empty" onClick={event => { event.preventDefault(); event.stopPropagation() }}>No language matches “{query}” — the typed code is kept.</li>}
    </ul>
   )}
  </span>
 );
}

