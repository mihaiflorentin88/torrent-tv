// Shared portal and self-update surfaces. Every component takes explicit
// props (snapshot, status, API client, injected external-link opener) so the
// desktop shell can mount them against its own origin and native link
// binding without importing the web app. Remote text renders as text nodes
// — never HTML.
import { useEffect, useRef, useState } from 'preact/hooks';
import { API, PortalPromotion, PortalState, PortalUser, PortalSessionStorage, UpdateStatus, clearPortalSession, loadPortalSession, promotionScreenTimeMs, savePortalSession, updateApplyOutcome } from '@torrent-tv/shared';
import { Icon } from './icons';

export function openExternalURL(url: string): void { window.open(url, '_blank', 'noopener,noreferrer') }

// Sidebar bottom dock: compact promotion, ordered other-projects links, then
// the account entry. Renders nothing while the snapshot is unknown, so a
// failed optional integration leaves no empty shell and no reserved space.
export function PortalSidebarDock({ snapshot, client, identity, onOpenAccount, openExternal }: { snapshot: PortalState | null; client: API; identity: PortalUser | null; onOpenAccount: () => void; openExternal: (url: string) => void }) {
  if (!snapshot) return null;
  return <div class="portal-dock">
    <PortalPromotionSlot client={client} snapshot={snapshot} openExternal={openExternal} />
    <PortalProjects links={snapshot.links} openExternal={openExternal} />
    {snapshot.accountsEnabled && <PortalAccountButton identity={identity} onOpen={onOpenAccount} />}
  </div>;
}

// Compact promotion anchored near the sidebar bottom. Delivery happens only
// while the slot is actually visible (visible prop AND a visible document —
// no prefetch impressions), rotation advances after each creative's
// screenTime, a batch wrap refetches, and timers die on unmount and while
// the document hides. Donors and ad-disabled snapshots render nothing at
// all — the slot itself disappears, with no delivery call.
export function PortalPromotionSlot({ client, snapshot, openExternal, visible = true }: { client: API; snapshot: PortalState | null; openExternal: (url: string) => void; visible?: boolean }) {
  const [creative, setCreative] = useState<PortalPromotion | null>(null);
  const [documentVisible, setDocumentVisible] = useState(() => typeof document === 'undefined' || document.visibilityState !== 'hidden');
  const active = visible && documentVisible && Boolean(snapshot?.adsEnabled) && !snapshot?.donor;
  useEffect(() => {
    const update = () => setDocumentVisible(document.visibilityState !== 'hidden');
    document.addEventListener('visibilitychange', update);
    return () => document.removeEventListener('visibilitychange', update);
  }, []);
  useEffect(() => {
    if (!active) { setCreative(null); return }
    let cancelled = false;
    let timer = 0;
    const controller = new AbortController();
    const deliver = async () => {
      try {
        const batch = await client.portalPromotions(3, controller.signal);
        if (cancelled) return;
        if (!batch.length) { setCreative(null); return }
        let position = 0;
        const show = () => {
          const item = batch[position];
          setCreative(item);
          timer = window.setTimeout(() => {
            position += 1;
            if (position >= batch.length) void deliver();
            else show();
          }, promotionScreenTimeMs(item.screenTime));
        };
        show();
      } catch (e) { if (!cancelled && (e as Error).name !== 'AbortError') setCreative(null) }
    };
    void deliver();
    return () => { cancelled = true; controller.abort(); window.clearTimeout(timer) };
  }, [active, client]);
  if (!snapshot || !snapshot.adsEnabled || snapshot.donor || !creative) return null;
  const url = client.promotionClickURL(creative.provider, creative.id);
  return <aside class="portal-promo">
    <p class="portal-promo-label">Supporters</p>
    <a href={url} rel="noreferrer noopener" onClick={event => { event.preventDefault(); openExternal(url) }}>
      {creative.image ? <img src={creative.image} alt="" loading="lazy" /> : null}
      <strong>{creative.title}</strong>
      <span>{creative.text}</span>
    </a>
  </aside>;
}

// Other Projects links render in the server-delivered order. They point
// outside the app, so activation always routes through the injected opener.
export function PortalProjects({ links, openExternal }: { links: PortalState['links']; openExternal: (url: string) => void }) {
  if (!links.length) return null;
  return <nav class="portal-projects" aria-label="Other projects">
    <p>Other projects</p>
    {links.map(link => <a key={link.id} href={link.url} title={link.description} rel="noreferrer noopener" onClick={event => { event.preventDefault(); openExternal(link.url) }}>{link.title}</a>)}
  </nav>;
}

export function PortalAccountButton({ identity, onOpen }: { identity: PortalUser | null; onOpen: () => void }) {
  return <button class="portal-account" onClick={onOpen}><Icon name="user" /><span>{identity ? identity.display_name || identity.email : 'Account'}</span></button>;
}

// Self-contained dialog focus: inert the page background, focus the first
// control, Tab-cycle inside the surface, Escape closes, and the opener
// regains focus on teardown.
function usePortalDialogFocus(surface: { current: HTMLElement | null }, onClose: () => void) {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const root = surface.current;
    const background = Array.from(document.querySelectorAll<HTMLElement>('.sidebar,.content'));
    background.forEach(element => element.setAttribute('inert', ''));
    const focusable = () => Array.from(root?.querySelectorAll<HTMLElement>('button:not([disabled]),input:not([disabled]),select:not([disabled]),a[href],[tabindex]:not([tabindex="-1"])') || []);
    const timer = window.setTimeout(() => focusable()[0]?.focus(), 0);
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); closeRef.current(); return }
      if (event.key !== 'Tab') return;
      const items = focusable();
      if (!items.length) return;
      const index = items.indexOf(document.activeElement as HTMLElement);
      event.preventDefault();
      items[(index + (event.shiftKey ? -1 : 1) + items.length) % items.length].focus();
    };
    document.addEventListener('keydown', key);
    return () => { window.clearTimeout(timer); document.removeEventListener('keydown', key); background.forEach(element => element.removeAttribute('inert')); previous?.focus() };
  }, []);
}

// Account dialog: sign-in, registration, and the signed-in card. The JWT is
// stored scoped to the server origin through the injected storage; sign-out
// is client-local. Registration deliberately does not auto-login: after the
// 201 the dialog lands on sign-in with a status message.
export function PortalAccountDialog({ client, storage, origin, identity, onIdentity, onClose }: { client: API; storage: PortalSessionStorage; origin: string; identity: PortalUser | null; onIdentity: (user: PortalUser | null) => void; onClose: () => void }) {
  const surface = useRef<HTMLElement | null>(null);
  usePortalDialogFocus(surface, onClose);
  // An in-flight sign-in/register must not resolve after the dialog is
  // gone: unmount aborts the request, and the AbortError guard keeps the
  // late rejection from writing state into the unmounted component.
  const pending = useRef<AbortController | null>(null);
  useEffect(() => () => pending.current?.abort(), []);
  const [mode, setMode] = useState<'sign-in' | 'register'>('sign-in');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const signOut = () => { clearPortalSession(storage, origin); onIdentity(null); setNotice('Signed out.'); setError('') };
  const submit = async (event: Event) => {
    event.preventDefault();
    if (busy) return;
    const mail = email.trim();
    if (!mail || !password) { setError('Email and password are required.'); return }
    if (mode === 'register') {
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(mail)) { setError('Enter a valid email address.'); return }
      if (password.length < 8) { setError('Use at least 8 characters for the password.'); return }
      if (password !== confirm) { setError('The passwords do not match.'); return }
    }
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true);
    setError('');
    try {
      if (mode === 'register') {
        await client.portalSessionRegister(mail, password, displayName.trim(), controller.signal);
        setMode('sign-in');
        setNotice('Account created. Sign in to continue.');
        setPassword('');
        setConfirm('');
      } else {
        const session = await client.portalSession(mail, password, controller.signal);
        savePortalSession(storage, origin, session);
        onIdentity(await client.portalMe(session.token, controller.signal));
      }
    } catch (e) { if ((e as Error).name !== 'AbortError') setError((e as Error).message) }
    finally { setBusy(false); pending.current = null }
  };
  return <div class="overlay" role="dialog" aria-modal="true" aria-label="Account">
    <section class="help-modal portal-account-dialog" ref={surface}>
      <button class="close" onClick={onClose}>Close</button>
      {identity ? <>
        <h2>Signed in</h2>
        <p class="portal-account-identity"><strong>{identity.display_name || identity.email}</strong><span>{identity.email}</span><span class="portal-account-role">{identity.role}</span></p>
        <div class="confirm-actions"><button type="button" onClick={signOut}>Sign out</button></div>
      </> : <>
        <h2>{mode === 'register' ? 'Create an account' : 'Sign in'}</h2>
        <p class="supporting">{mode === 'register' ? 'Registration does not sign you in; you will sign in afterwards.' : 'Your supporter account on this server.'}</p>
        <form onSubmit={submit}>
          <label><span>Email</span><input type="email" autocomplete="email" value={email} onInput={e => setEmail(e.currentTarget.value)} required /></label>
          {mode === 'register' && <label><span>Display name</span><input type="text" autocomplete="nickname" value={displayName} onInput={e => setDisplayName(e.currentTarget.value)} /></label>}
          <label><span>Password</span><input type="password" autocomplete={mode === 'register' ? 'new-password' : 'current-password'} value={password} onInput={e => setPassword(e.currentTarget.value)} required /></label>
          {mode === 'register' && <label><span>Confirm password</span><input type="password" autocomplete="new-password" value={confirm} onInput={e => setConfirm(e.currentTarget.value)} required /></label>}
          {error && <p class="portal-form-error" role="alert">{error}</p>}
          {notice && <p class="portal-form-notice" role="status">{notice}</p>}
          <div class="confirm-actions">
            <button type="button" onClick={() => { setMode(mode === 'register' ? 'sign-in' : 'register'); setError(''); setNotice('') }}>{mode === 'register' ? 'Back to sign in' : 'Create an account'}</button>
            <button type="submit" class="primary" disabled={busy}>{busy ? 'Working…' : mode === 'register' ? 'Register' : 'Sign in'}</button>
          </div>
        </form>
      </>}
    </section>
  </div>;
}

export type UpdatePhase = 'idle' | 'checking' | 'confirming' | 'applying';
export type UpdateOutcome = { kind: 'busy' | 'manual-only' | 'failed'; message?: string } | null;
export type UpdateController = {
  phase: UpdatePhase;
  outcome: UpdateOutcome;
  reconnectedCurrent: boolean;
  check: () => Promise<void>;
  requestApply: () => void;
  cancelApply: () => void;
  confirmApply: () => Promise<void>;
};

// Apply/check orchestration shared by the Settings section and the
// availability notice. Applying is two-phase: requestApply parks the
// controller in `confirming` until the playback-interruption warning is
// confirmed; only confirmApply touches POST /updates/apply. A 409 answers
// busy or manual-only, anything else is a neutral failure message.
export function useUpdateController({ client, status, onStatus }: { client: API; status: UpdateStatus | null; onStatus: (status: UpdateStatus) => void }): UpdateController {
  const [phase, setPhase] = useState<UpdatePhase>('idle');
  const [outcome, setOutcome] = useState<UpdateOutcome>(null);
  const [reconnectedCurrent, setReconnectedCurrent] = useState(false);
  const busyRef = useRef(false);
  const versionBeforeApply = useRef<string | null>(null);
  const check = async () => {
    if (busyRef.current) return;
    busyRef.current = true;
    setOutcome(null);
    setReconnectedCurrent(false);
    setPhase('checking');
    try { onStatus(await client.updatesCheck()) }
    catch (e) { setOutcome({ kind: 'failed', message: (e as Error).message }) }
    finally { busyRef.current = false; setPhase('idle') }
  };
  const requestApply = () => { if (busyRef.current) return; setOutcome(null); setPhase('confirming') };
  const cancelApply = () => { setPhase('idle') };
  const confirmApply = async () => {
    if (phase !== 'confirming' || busyRef.current) return;
    busyRef.current = true;
    versionBeforeApply.current = status?.currentVersion || null;
    setPhase('applying');
    try {
      const next = await client.updatesApply();
      onStatus(next);
      setReconnectedCurrent(false);
    } catch (e) {
      const err = e as Error & { status?: number };
      setOutcome({ kind: updateApplyOutcome(err.status, err.message), message: err.message });
    } finally { busyRef.current = false; setPhase('idle') }
  };
  // Reconnected-current: an accepted apply restarts the server; once a
  // status reports a different current version with nothing available and
  // no apply in flight, the restart has been observed on the far side.
  useEffect(() => {
    if (phase !== 'idle' || !status || status.applying || status.available) return;
    const before = versionBeforeApply.current;
    if (before && status.currentVersion && status.currentVersion !== before) setReconnectedCurrent(true);
  }, [phase, status]);
  return { phase, outcome, reconnectedCurrent, check, requestApply, cancelApply, confirmApply };
}

// Every update notice carries the same three facts: updates install on the
// server and interrupt playback, TV and display-only clients cannot apply
// them, and where the manual releases live.
export function UpdateWarning({ status, openExternal }: { status: UpdateStatus; openExternal: (url: string) => void }) {
  return <div class="update-warning">
    <p>Updates install on the server and briefly interrupt playback on every connected device.</p>
    <p>TV and display-only clients cannot apply updates — install a release manually on the server host when needed.</p>
    {status.releasesUrl ? <a href={status.releasesUrl} rel="noreferrer noopener" onClick={event => { event.preventDefault(); openExternal(status.releasesUrl) }}>Releases and manual installs</a> : null}
  </div>;
}

// Settings-side update surface: current version, live state line, check and
// apply controls, and the mandatory warning block. Renders nothing when the
// server does not expose the update surface.
export function UpdateSection({ client, status, connected, failure, controller, openExternal }: { client: API; status: UpdateStatus | null; connected: boolean; failure: string; controller: UpdateController; openExternal: (url: string) => void }) {
  const { phase, outcome, reconnectedCurrent } = controller;
  if (!status) return null;
  const applying = phase === 'applying' || status.applying;
  const state = applying ? 'applying' : outcome ? outcome.kind : !connected ? 'disconnected' : failure ? 'failed' : reconnectedCurrent ? 'reconnected' : status.available ? 'available' : 'current';
  const stateLine = phase === 'checking' ? 'Checking for updates…'
    : applying ? 'Applying the update — the server will restart.'
      : !connected ? 'Server connection lost — reconnecting…'
        : outcome?.kind === 'busy' ? 'An update is already in progress on the server.'
          : outcome?.kind === 'manual-only' ? 'This installation is manual-only: fetch and install a release yourself.'
            : outcome?.kind === 'failed' ? `Update problem: ${outcome.message || 'the operation did not succeed'}.`
              : failure ? `Update problem: ${failure}.`
                : reconnectedCurrent ? `Connection restored — now running version ${status.currentVersion}.`
                  : status.available ? status.latest ? `Version ${status.latest} is available.` : 'A new version is available.'
                    : `This server runs version ${status.currentVersion}. You are up to date.`;
  return <section class="update-section" aria-label="Server updates">
    <h2>Server updates</h2>
    <p class="update-state" data-state={state}>{stateLine}</p>
    {status.available && status.notes ? <p class="update-notes">{status.notes}</p> : null}
    <div class="update-actions">
      <button type="button" disabled={phase !== 'idle' || !connected} onClick={() => void controller.check()}>{phase === 'checking' ? 'Checking…' : 'Check for updates'}</button>
      {status.available && status.selfUpdate && <button type="button" class="primary" disabled={phase !== 'idle' || !connected} onClick={controller.requestApply}>Apply update</button>}
    </div>
    <UpdateWarning status={status} openExternal={openExternal} />
  </section>;
}

// Availability banner for the main content. Only an actually available
// update renders one; failures live in the Settings section, never as a
// global alert.
export function UpdateNotice({ status, controller, openExternal }: { status: UpdateStatus | null; controller: UpdateController; openExternal: (url: string) => void }) {
  if (!status?.available) return null;
  return <div class="update-notice" role="status">
    <div class="update-notice-copy">
      <strong>{status.latest ? `Update available: version ${status.latest}` : 'Update available'}</strong>
      {status.notes ? <p>{status.notes}</p> : null}
      {status.releasedAt ? <small>Released {new Date(status.releasedAt).toLocaleDateString()}</small> : null}
    </div>
    <div class="update-actions">
      {status.selfUpdate && <button type="button" class="primary" disabled={controller.phase !== 'idle'} onClick={controller.requestApply}>Apply update</button>}
      {status.releasesUrl ? <a href={status.releasesUrl} rel="noreferrer noopener" onClick={event => { event.preventDefault(); openExternal(status.releasesUrl) }}>Releases</a> : null}
    </div>
    <UpdateWarning status={status} openExternal={openExternal} />
  </div>;
}

// Playback-interruption confirmation: applying restarts the server, so the
// POST fires only after this dialog is confirmed.
export function UpdateApplyConfirm({ controller }: { controller: UpdateController }) {
  if (controller.phase !== 'confirming') return null;
  return <div class="overlay" role="dialog" aria-modal="true" aria-label="Apply server update">
    <section class="help-modal">
      <h2>Apply the server update?</h2>
      <p>The server restarts while the update installs. Playback is interrupted on every connected device, including this one.</p>
      <div class="confirm-actions">
        <button type="button" onClick={controller.cancelApply}>Cancel</button>
        <button type="button" class="primary" onClick={() => void controller.confirmApply()}>Apply and restart</button>
      </div>
    </section>
  </div>;
}
