import {useEffect, useRef} from 'preact/hooks';

export type Direction = 'left' | 'right' | 'up' | 'down';

export interface RectLike {
  left: number;
  right: number;
  top: number;
  bottom: number;
  width: number;
  height: number;
}

export interface NavigationCandidate<T> {
  value: T;
  rect: RectLike;
}

export function remoteAction(key: string, keyCode: number): Direction | 'enter' | 'back' | 'ime-done' | 'ime-cancel' | null {
  if (key === 'GoBack' || key === 'BrowserBack' || keyCode === 27) return 'back';
  if (keyCode === 65376) return 'ime-done';
  if (keyCode === 65385) return 'ime-cancel';
  if (key === 'ArrowLeft' || keyCode === 37) return 'left';
  if (key === 'ArrowRight' || keyCode === 39) return 'right';
  if (key === 'ArrowUp' || keyCode === 38) return 'up';
  if (key === 'ArrowDown' || keyCode === 40) return 'down';
  if (key === 'Enter' || key === 'Return' || keyCode === 13) return 'enter';
  if (key === 'XF86Back' || key === 'Back' || keyCode === 10009) return 'back';
  return null;
}

export function chooseDirectionalTarget<T>(current: RectLike, candidates: NavigationCandidate<T>[], direction: Direction): T | null {
  const currentX = current.left + current.width / 2;
  const currentY = current.top + current.height / 2;
  let best: {value: T; score: number} | null = null;
  for (const candidate of candidates) {
    const x = candidate.rect.left + candidate.rect.width / 2;
    const y = candidate.rect.top + candidate.rect.height / 2;
    const primary = direction === 'left' ? currentX - x : direction === 'right' ? x - currentX : direction === 'up' ? currentY - y : y - currentY;
    if (primary <= 1) continue;
    const cross = direction === 'left' || direction === 'right' ? Math.abs(y - currentY) : Math.abs(x - currentX);
    const overlaps = direction === 'left' || direction === 'right'
      ? candidate.rect.bottom >= current.top && candidate.rect.top <= current.bottom
      : candidate.rect.right >= current.left && candidate.rect.left <= current.right;
    const score = primary + cross * 4 + (overlaps ? 0 : 1000);
    if (!best || score < best.score) best = {value: candidate.value, score};
  }
  return best?.value ?? null;
}

const focusableSelector = 'button:not([disabled]),input:not([disabled]),select:not([disabled]),[tabindex]:not([tabindex="-1"])';

function visibleFocusables(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>(focusableSelector)).filter(element => element.offsetWidth > 0 && element.offsetHeight > 0);
}

function numberAttribute(element: HTMLElement, name: 'focusRow'|'focusCol'): number | null {
  const value = Number(element.dataset[name]);
  return Number.isFinite(value) ? value : null;
}

export function chooseStructuredTarget(current: HTMLElement, elements: HTMLElement[], direction: Direction): HTMLElement | null {
  const region = current.dataset.focusRegion;
  const row = numberAttribute(current, 'focusRow');
  const col = numberAttribute(current, 'focusCol');
  if (!region || row === null || col === null) return null;
  const peers = elements.filter(element => element !== current && element.dataset.focusRegion === region);
  if (direction === 'left' || direction === 'right') {
    const candidates = peers.filter(element => numberAttribute(element, 'focusRow') === row)
      .map(element => ({element, col: numberAttribute(element, 'focusCol')}))
      .filter((candidate): candidate is {element: HTMLElement; col: number} => candidate.col !== null)
      .filter(candidate => direction === 'left' ? candidate.col < col : candidate.col > col)
      .sort((a, b) => Math.abs(a.col - col) - Math.abs(b.col - col));
    return candidates[0]?.element || null;
  }
  const rows = Array.from(new Set(peers.map(element => numberAttribute(element, 'focusRow')).filter((value): value is number => value !== null)))
    .filter(value => direction === 'up' ? value < row : value > row)
    .sort((a, b) => Math.abs(a - row) - Math.abs(b - row));
  const targetRow = rows[0];
  if (targetRow === undefined) return null;
  return peers.filter(element => numberAttribute(element, 'focusRow') === targetRow)
    .map(element => ({element, distance: Math.abs((numberAttribute(element, 'focusCol') || 0) - col)}))
    .sort((a, b) => a.distance - b.distance)[0]?.element || null;
}

function isTextInput(element: Element | null): element is HTMLInputElement | HTMLTextAreaElement {
  return element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement;
}

const supportsScrollIntoViewOptions = typeof document !== 'undefined'
  && typeof document.documentElement?.style !== 'undefined'
  && 'scrollBehavior' in document.documentElement.style;

// Engines that ignore scrollIntoView options (such as Chromium 53 on webOS 4.x)
// treat the options argument as truthy and align the target to the container top.
// This fallback walks scrollable ancestors, computes item bounds relative to each
// scrollport, and adjusts only the axes needed: nearest vertical movement and
// centered horizontal rail movement, clamped to scroll bounds.
export function revealElement(element: HTMLElement | null): void {
  if (!element) return;
  if (supportsScrollIntoViewOptions) {
    element.scrollIntoView({ block: 'nearest', inline: 'center' });
    return;
  }
  const ancestors: HTMLElement[] = [];
  for (let parent = element.parentElement; parent && parent !== document.body && parent !== document.documentElement; parent = parent.parentElement) {
    const style = parent.ownerDocument?.defaultView?.getComputedStyle(parent);
    if (!style) continue;
    const canScrollX = /(auto|scroll)/.test(style.overflowX) && parent.scrollWidth > parent.clientWidth;
    const canScrollY = /(auto|scroll)/.test(style.overflowY) && parent.scrollHeight > parent.clientHeight;
    if (canScrollX || canScrollY) ancestors.push(parent);
  }
  ancestors.reverse();
  for (const parent of ancestors) {
    const style = parent.ownerDocument?.defaultView?.getComputedStyle(parent);
    if (!style) continue;
    const box = parent.getBoundingClientRect();
    const rect = element.getBoundingClientRect();
    if (parent.scrollHeight > parent.clientHeight && /(auto|scroll)/.test(style.overflowY)) {
      if (rect.height > box.height || rect.top < box.top) {
        parent.scrollTop = Math.max(0, Math.min(parent.scrollTop + (rect.top - box.top), parent.scrollHeight - parent.clientHeight));
      } else if (rect.bottom > box.bottom) {
        parent.scrollTop = Math.max(0, Math.min(parent.scrollTop + (rect.bottom - box.bottom), parent.scrollHeight - parent.clientHeight));
      }
    }
    if (parent.scrollWidth > parent.clientWidth && /(auto|scroll)/.test(style.overflowX)) {
      const delta = (rect.left + rect.width / 2) - (box.left + parent.clientWidth / 2);
      parent.scrollLeft = Math.max(0, Math.min(parent.scrollLeft + delta, parent.scrollWidth - parent.clientWidth));
    }
  }
}

export function focusElement(element: HTMLElement | null): void {
  if (!element) return;
  if (isTextInput(element) && element.dataset.tvEditing !== 'true') element.readOnly = true;
  element.focus();
  revealElement(element);
}

export function useTVNavigation(options: {
  getInitialFocus: () => HTMLElement | null;
  restoreKey?: string | null;
  inputExitTarget?: () => HTMLElement | null;
  onBack: () => void;
  onFocusKey?: (key: string) => void;
  onDirection?: (direction: Direction, current: HTMLElement) => boolean;
  onLongBack?: () => void;
}): void {
  const latest = useRef(options);
  latest.current = options;
  useEffect(() => {
    const timer = window.setTimeout(() => {
      const current = latest.current;
      const restored = current.restoreKey ? visibleFocusables().find(element => element.dataset.focusKey === current.restoreKey) || null : null;
      focusElement(restored || current.getInitialFocus());
    }, 0);
    const focus = (event: FocusEvent) => {
      const element = event.target as HTMLElement | null;
      if (!element) return;
      revealElement(element);
      const key = element.dataset.focusKey;
      if (key) latest.current.onFocusKey?.(key);
    };
    let backStarted=0;let backTimer=0;
    const keydown = (event: KeyboardEvent) => {
      const action = remoteAction(event.key, event.keyCode);
      const active = document.activeElement;
      if (isTextInput(active) && active.dataset.tvEditing !== 'true' && action === 'enter') {
        event.preventDefault();
        active.dataset.tvEditing = 'true';
        active.readOnly = false;
        active.blur();
        window.setTimeout(() => { active.focus(); if (typeof active.select === 'function') active.select(); }, 0);
        return;
      }
      if (isTextInput(active) && active.dataset.tvEditing === 'true') {
        if (action === 'ime-done' || action === 'ime-cancel' || action === 'back') {
          active.dataset.tvEditing = 'false';
          active.readOnly = true;
          active.blur();
          window.setTimeout(() => focusElement(latest.current.inputExitTarget?.() || active), 0);
        }
        return;
      }
      if (!action) return;
      if (action === 'back') {
        event.preventDefault();
        if(latest.current.onLongBack){if(!backStarted){backStarted=Date.now();backTimer=window.setTimeout(()=>{backStarted=0;latest.current.onLongBack?.()},5000)};return}
        latest.current.onBack();
        return;
      }
      if (action === 'enter') {
        if (active instanceof HTMLElement && active.matches(focusableSelector)) {
          event.preventDefault();
          active.click();
        }
        return;
      }
      if (action === 'ime-done' || action === 'ime-cancel') return;
      event.preventDefault();
      const elements = visibleFocusables();
      const current = active instanceof HTMLElement && elements.includes(active) ? active : latest.current.getInitialFocus();
      if (!current) return;
      if (latest.current.onDirection?.(action, current)) return;
      const structured=Boolean(current.dataset.focusRegion&&current.dataset.focusRow!==undefined&&current.dataset.focusCol!==undefined);
      const target = chooseStructuredTarget(current, elements, action) || (!structured?chooseDirectionalTarget(current.getBoundingClientRect(),elements.filter(element => element !== current).map(element => ({value: element, rect: element.getBoundingClientRect()})),action):null);
      focusElement(target);
    };
    const keyup=(event:KeyboardEvent)=>{if(remoteAction(event.key,event.keyCode)!=='back'||!latest.current.onLongBack)return;event.preventDefault();if(backStarted){window.clearTimeout(backTimer);backStarted=0;latest.current.onBack()}};
    document.addEventListener('focusin', focus);
    document.addEventListener('keydown', keydown);
    document.addEventListener('keyup',keyup);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener('focusin', focus);
      document.removeEventListener('keydown', keydown);
      document.removeEventListener('keyup',keyup);window.clearTimeout(backTimer);
    };
  }, []);
}
