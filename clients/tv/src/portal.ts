// Tizen-local decision helpers for the portal promotion slot, the Other
// projects dialog, and the server update controls. Pure on purpose: the TV
// vitest suite runs in a plain node environment, so the behaviors the TV
// must honor are pinned here without a DOM, and main.tsx consumes the same
// functions and identity constants the tests assert against.
import type { PortalState, UpdateStatus } from '@torrent-tv/shared';

// Sidebar focus row of the Other projects entry, which opens the projects
// route page. Appended after the fixed menu rows (the last menu row is 32),
// so portal links appearing or disappearing never renumbers an existing
// control.
export const PROJECTS_MENU_ROW = 33;

// Focus region of the update confirmation dialog; directional movement
// never leaves the region of the focused element, which is the trap.
export const UPDATE_DIALOG_REGION = 'update-dialog';

// TVSettings focus rows. Rows 1-6 stay the safe playback fields; the two
// tracker toggles follow (7-8), then the two Pirate Bay URL inputs (9-10),
// the save control (11), six connection tests (12-17), and change/forget
// server (18-19). The update controls append after them so inserting a row
// renumbers only these constants.
export const SETTINGS_SAVE_ROW = 11;
export const SETTINGS_TEST_FIRST_ROW = 12;
export const SETTINGS_CHANGE_SERVER_ROW = 18;
export const SETTINGS_FORGET_SERVER_ROW = 19;
export const UPDATE_CHECK_ROW = 20;
export const UPDATE_APPLY_ROW = 21;

// The promotion slot exists only while the server advertises ads and the
// household is not a donor; a donor hides the slot entirely, and an absent
// snapshot (fetch failure, portal disabled) leaves zero trace.
export function promotionsVisible(state: Pick<PortalState, 'adsEnabled' | 'donor'> | null): boolean {
  return Boolean(state?.adsEnabled) && !state?.donor;
}

// Replayed snapshot events that arrive while a reconnect is refetching the
// current snapshots must not override the fresher refetched state, so
// portal and update events are dropped until the refetch settles. Catalog
// and metadata events keep applying: replayed copies are idempotent.
export function snapshotEventAllowed(recovering: boolean, kind: string): boolean {
  if (!recovering) return true;
  return kind !== 'portal.state' && kind !== 'updates.status' && kind !== 'updates.failed';
}

// The notice with the releases URL stays visible for the available and the
// manual-only states; a plain current installation shows no update card.
export function updateNoticeVisible(status: UpdateStatus | null): boolean {
  if (!status) return false;
  return status.available || !status.selfUpdate;
}

// The install control stays unusable unless the server offers a
// self-updatable release and is not already applying one. The pending flag
// covers the client-local in-flight POST.
export function updateApplyDisabled(status: UpdateStatus | null, pending: boolean): boolean {
  if (pending) return true;
  if (!status) return true;
  return status.applying || !status.available || !status.selfUpdate;
}

export type UpdateApplyOutcome = 'conflict' | 'failed';

// The server answers busy and manual-only refusals with 409; every other
// failure is a neutral problem. The response detail carries the reason.
export function updateApplyOutcome(httpStatus: number | undefined): UpdateApplyOutcome {
  return httpStatus === 409 ? 'conflict' : 'failed';
}

// Focus target after a modal dialog closes: the control that had focus when
// the dialog opened, falling back to a supplied key when that control is
// gone (gated items can disappear while a dialog is open).
export function dialogRestoreKey(openerKey: string | null, fallbackKey: string): string {
  return openerKey || fallbackKey;
}

// The update confirmation dialog is bound to a live status: a reconnect
// refetch can null the snapshot while the dialog is open. When the status is
// gone the open flag is stale and must be cleared, so a later updates.status
// event cannot resurrect an uninvited modal and Back reaches the page again.
export function confirmDialogStale(confirmOpen: boolean, status: UpdateStatus | null): boolean {
  return confirmOpen && status === null;
}

// Reconnect refetches are generational: a stale generation settling must not
// end recovery while a newer refetch is still in flight, or replayed
// snapshot events would apply before the fresh snapshots land.
export function recoverySettles(generation: number, currentGeneration: number): boolean {
  return generation === currentGeneration;
}
