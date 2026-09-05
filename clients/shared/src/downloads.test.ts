import { describe, expect, it } from 'vitest';
import { type Download, downloadTransferActions } from '@torrent-tv/shared';

const row = (state: string, error?: string): Pick<Download, 'state' | 'error'> => ({ state, error });
const actions = (state: string, error?: string) => downloadTransferActions(row(state, error)).map(item => item.action);

// Table tests for the pure transfer-control seam shared by the web and TV
// Downloads screens: which of pause/resume/retry a row exposes, decided from
// the raw qBittorrent state string plus the surfaced engine/tracker error.
describe('Download transfer action decision', () => {
  it('offers pause for actively transferring rows', () => {
    const cases = ['downloading', 'metaDL', 'forcedDL', 'forcedMetaDL', 'stalledDL', 'queuedDL', 'allocating'];
    for (const state of cases) expect(actions(state)).toEqual(['pause']);
  });
  it('offers resume for halted rows in both qBittorrent naming schemes', () => {
    const cases = ['pausedDL', 'pausedUP', 'stoppedDL', 'stoppedUP'];
    for (const state of cases) expect(actions(state)).toEqual(['resume']);
  });
  it('offers retry when a tracker or engine error is surfaced regardless of state', () => {
    expect(actions('downloading', 'tracker announce failed')).toEqual(['retry']);
    expect(actions('pausedDL', 'engine unreachable')).toEqual(['retry']);
    expect(actions('unavailable', 'connection refused')).toEqual(['retry']);
  });
  it('offers retry for engine error states even before a message propagates', () => {
    expect(actions('error')).toEqual(['retry']);
    expect(actions('missingFiles')).toEqual(['retry']);
  });
  it('offers nothing for seeding, checking, or unknown rows', () => {
    const cases = ['uploading', 'stalledUP', 'queuedUP', 'forcedUP', 'checkingDL', 'checkingUP', 'checkingResumeData', 'moving', 'unknown', ''];
    for (const state of cases) expect(actions(state)).toEqual([]);
  });
  it('labels each action for idle and in-flight rendering', () => {
    expect(downloadTransferActions(row('downloading'))).toEqual([{ action: 'pause', label: 'Pause', pendingLabel: 'Pausing…' }]);
    expect(downloadTransferActions(row('pausedUP'))).toEqual([{ action: 'resume', label: 'Resume', pendingLabel: 'Resuming…' }]);
    expect(downloadTransferActions(row('error'))).toEqual([{ action: 'retry', label: 'Retry download', pendingLabel: 'Retrying…' }]);
  });
  it('normalizes case and surrounding whitespace before deciding', () => {
    expect(actions(' StalledDL ')).toEqual(['pause']);
    expect(actions('PAUSEDDL')).toEqual(['resume']);
  });
});
