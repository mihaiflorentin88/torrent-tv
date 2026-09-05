import { describe, expect, it } from 'vitest';
import type { UpdateStatus } from '@torrent-tv/shared';
import {
  PROJECTS_DIALOG_REGION,
  UPDATE_APPLY_ROW,
  UPDATE_CHECK_ROW,
  UPDATE_DIALOG_REGION,
  confirmDialogStale,
  dialogRestoreKey,
  promotionsVisible,
  recoverySettles,
  snapshotEventAllowed,
  updateApplyDisabled,
  updateApplyOutcome,
  updateNoticeVisible,
} from './portal';

const status = (overrides: Partial<UpdateStatus>): UpdateStatus => ({
  currentVersion: '1.2.3',
  available: false,
  releasesUrl: 'https://example.invalid/releases',
  selfUpdate: true,
  applying: false,
  ...overrides,
});

describe('promotion slot gating', () => {
  it('is hidden without an enabled snapshot, without ads, and for donors', () => {
    expect(promotionsVisible(null)).toBe(false);
    expect(promotionsVisible({ adsEnabled: false, donor: false })).toBe(false);
    expect(promotionsVisible({ adsEnabled: true, donor: true })).toBe(false);
  });

  it('is visible only when ads are enabled and the household is not a donor', () => {
    expect(promotionsVisible({ adsEnabled: true, donor: false })).toBe(true);
  });
});

describe('SSE recovery gating', () => {
  it('applies every event kind while the stream is not recovering', () => {
    for (const kind of ['portal.state', 'updates.status', 'updates.failed', 'metadata.updated', 'catalog.updated']) {
      expect(snapshotEventAllowed(false, kind)).toBe(true);
    }
  });

  it('drops replayed snapshot events so they cannot override the refetched state', () => {
    expect(snapshotEventAllowed(true, 'portal.state')).toBe(false);
    expect(snapshotEventAllowed(true, 'updates.status')).toBe(false);
    expect(snapshotEventAllowed(true, 'updates.failed')).toBe(false);
  });

  it('keeps idempotent catalog and metadata replay during recovery', () => {
    expect(snapshotEventAllowed(true, 'metadata.updated')).toBe(true);
    expect(snapshotEventAllowed(true, 'catalog.search.completed')).toBe(true);
  });
});

describe('update notice and install availability', () => {
  it('shows the notice with the releases URL when an update is available', () => {
    expect(updateNoticeVisible(status({ available: true, latest: '1.3.0' }))).toBe(true);
  });

  it('shows the manual-only notice even when nothing is available', () => {
    expect(updateNoticeVisible(status({ available: false, selfUpdate: false }))).toBe(true);
  });

  it('shows no notice for a plain current installation', () => {
    expect(updateNoticeVisible(status({}))).toBe(false);
    expect(updateNoticeVisible(null)).toBe(false);
  });

  it('disables install without a status, while pending, while the server applies, and when manual-only', () => {
    expect(updateApplyDisabled(null, false)).toBe(true);
    expect(updateApplyDisabled(status({ available: true }), true)).toBe(true);
    expect(updateApplyDisabled(status({ available: true, applying: true }), false)).toBe(true);
    expect(updateApplyDisabled(status({ available: true, selfUpdate: false }), false)).toBe(true);
  });

  it('enables install only for a self-updatable available release', () => {
    expect(updateApplyDisabled(status({ available: true }), false)).toBe(false);
  });
});

describe('apply outcome classification', () => {
  it('separates the 409 refusal from neutral failures', () => {
    expect(updateApplyOutcome(409)).toBe('conflict');
    expect(updateApplyOutcome(502)).toBe('failed');
    expect(updateApplyOutcome(500)).toBe('failed');
    expect(updateApplyOutcome(undefined)).toBe('failed');
  });
});

describe('dialog focus restore', () => {
  it('returns focus to the control that opened the dialog', () => {
    expect(dialogRestoreKey('menu-projects', 'header-retry')).toBe('menu-projects');
    expect(dialogRestoreKey('update-apply', 'header-retry')).toBe('update-apply');
  });

  it('falls back to an always-present control when the opener disappeared', () => {
    expect(dialogRestoreKey(null, 'header-retry')).toBe('header-retry');
  });
});

describe('focus identities', () => {
  it('pins the appended TVSettings rows and dialog regions', () => {
    expect(UPDATE_CHECK_ROW).toBe(16);
    expect(UPDATE_APPLY_ROW).toBe(17);
    expect(PROJECTS_DIALOG_REGION).toBe('projects-dialog');
    expect(UPDATE_DIALOG_REGION).toBe('update-dialog');
  });
});

describe('update confirmation lifecycle', () => {
  it('flags the open dialog stale once the status snapshot is gone', () => {
    expect(confirmDialogStale(true, null)).toBe(true);
    expect(confirmDialogStale(true, status({ available: true }))).toBe(false);
    expect(confirmDialogStale(false, null)).toBe(false);
  });

  it('does not resurrect the dialog when a status event arrives after the flag cleared', () => {
    // Recovery nulls the status, the guard clears the flag, and every later
    // updates.status evaluation with a cleared flag stays closed.
    expect(confirmDialogStale(true, null)).toBe(true);
    expect(confirmDialogStale(false, null)).toBe(false);
    expect(confirmDialogStale(false, status({ available: true, latest: '1.3.0' }))).toBe(false);
  });
});

describe('reconnect recovery generations', () => {
  it('settles recovery only for the newest refetch generation', () => {
    expect(recoverySettles(1, 1)).toBe(true);
    expect(recoverySettles(2, 2)).toBe(true);
  });

  it('keeps recovery running when a stale generation settles out of order', () => {
    expect(recoverySettles(1, 2)).toBe(false);
    expect(recoverySettles(3, 2)).toBe(false);
  });
});
