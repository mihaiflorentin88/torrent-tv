import { describe, expect, it } from 'vitest';
import { chooseDirectionalTarget, chooseStructuredTarget, RectLike, remoteAction } from './navigation';
import { PROJECTS_MENU_ROW, UPDATE_DIALOG_REGION } from './portal';

const rect = (left: number, top: number, width = 100, height = 100): RectLike => ({ left, top, right: left + width, bottom: top + height, width, height });

describe('chooseDirectionalTarget', () => {
  const current = rect(100, 100);
  const candidates = [
    { value: 'left', rect: rect(-50, 100) },
    { value: 'right', rect: rect(250, 100) },
    { value: 'up', rect: rect(100, -50) },
    { value: 'down', rect: rect(100, 250) },
    { value: 'diagonal', rect: rect(220, 220) },
  ];

  it.each([
    ['left', 'left'],
    ['right', 'right'],
    ['up', 'up'],
    ['down', 'down'],
  ] as const)('moves %s to the aligned target', (direction, expected) => {
    expect(chooseDirectionalTarget(current, candidates, direction)).toBe(expected);
  });

  it('prefers the next aligned rail over a closer diagonal control', () => {
    const result = chooseDirectionalTarget(current, [
      { value: 'next-rail', rect: rect(100, 500) },
      { value: 'diagonal', rect: rect(450, 250) },
    ], 'down');
    expect(result).toBe('next-rail');
  });

  it('returns null when there is no target in that direction', () => {
    expect(chooseDirectionalTarget(current, [{ value: 'left', rect: rect(-50, 100) }], 'right')).toBeNull();
  });
});

describe('chooseStructuredTarget', () => {
  const item = (row: number, col: number) => ({ dataset: { focusRegion: 'content', focusRow: String(row), focusCol: String(col) } } as unknown as HTMLElement);
  it('moves to the adjacent row and clamps to the closest available column', () => {
    const current = item(1, 3); const upper = item(0, 0); const lowerA = item(2, 0); const lowerB = item(2, 2);
    expect(chooseStructuredTarget(current, [upper, current, lowerA, lowerB], 'down')).toBe(lowerB);
  });
  it('does not skip a row even when a farther item is geometrically closer', () => {
    const current = item(0, 0); const next = item(1, 4); const farther = item(2, 0);
    expect(chooseStructuredTarget(current, [current, next, farther], 'down')).toBe(next);
  });
  it('moves horizontally only within the current physical row', () => { const left = item(1, 0); const current = item(1, 1); const right = item(1, 2); const below = item(2, 1); expect(chooseStructuredTarget(current, [left, current, right, below], 'left')).toBe(left); expect(chooseStructuredTarget(current, [left, current, right, below], 'right')).toBe(right); expect(chooseStructuredTarget(left, [left, current, right, below], 'left')).toBeNull() });
  it('does not turn left or right into vertical movement in a dialog', () => { const first = item(0, 0); const second = item(1, 0); expect(chooseStructuredTarget(first, [first, second], 'left')).toBeNull(); expect(chooseStructuredTarget(first, [first, second], 'right')).toBeNull(); expect(chooseStructuredTarget(first, [first, second], 'down')).toBe(second); expect(chooseStructuredTarget(second, [first, second], 'up')).toBe(first) });
  it('chooses the closest column in ragged rows in both vertical directions', () => { const upper = item(0, 4); const current = item(1, 3); const lowerLeft = item(2, 0); const lowerNear = item(2, 2); expect(chooseStructuredTarget(current, [upper, current, lowerLeft, lowerNear], 'up')).toBe(upper); expect(chooseStructuredTarget(current, [upper, current, lowerLeft, lowerNear], 'down')).toBe(lowerNear) });
  it('keeps the series playback action in the primary slot when Play changes to Resume', () => { const back = item(0, 0); const playback = item(1, 0); const favorite = item(1, 1); const season = item(2, 0); const elements = [back, playback, favorite, season]; expect(chooseStructuredTarget(back, elements, 'down')).toBe(playback); expect(chooseStructuredTarget(playback, elements, 'right')).toBe(favorite); expect(chooseStructuredTarget(favorite, elements, 'left')).toBe(playback); expect(chooseStructuredTarget(playback, elements, 'down')).toBe(season); expect(chooseStructuredTarget(season, elements, 'up')).toBe(playback) });
  it('navigates season tabs, pack alternatives, expanded controls, and episodes in visual order', () => { const seasonOne = item(2, 0); const seasonTwo = item(2, 1); const packA = item(3, 0); const packB = item(3, 1); const pause = item(4, 0); const remove = item(4, 1); const episode = item(10, 0); const elements = [seasonOne, seasonTwo, packA, packB, pause, remove, episode]; expect(chooseStructuredTarget(seasonOne, elements, 'right')).toBe(seasonTwo); expect(chooseStructuredTarget(seasonTwo, elements, 'down')).toBe(packB); expect(chooseStructuredTarget(packB, elements, 'left')).toBe(packA); expect(chooseStructuredTarget(packA, elements, 'down')).toBe(pause); expect(chooseStructuredTarget(pause, elements, 'right')).toBe(remove); expect(chooseStructuredTarget(remove, elements, 'down')).toBe(episode); expect(chooseStructuredTarget(episode, elements, 'up')).toBe(pause) });
});

describe('player toolbar focus grid', () => {
  const control = (row: number, col: number) => ({ dataset: { focusRegion: 'player-controls', focusRow: String(row), focusCol: String(col) } } as unknown as HTMLElement);
  const timeline = control(0, 0);
  const toolbar = [0, 1, 2, 3, 4, 5, 6, 7, 8].map(col => control(1, col));
  it('keeps the hide cell reachable at the end of the button row and the timeline above', () => {
    expect(chooseStructuredTarget(toolbar[7], toolbar, 'right')).toBe(toolbar[8]);
    expect(chooseStructuredTarget(toolbar[8], toolbar, 'left')).toBe(toolbar[7]);
    expect(chooseStructuredTarget(toolbar[8], [timeline, ...toolbar], 'up')).toBe(timeline);
    expect(chooseStructuredTarget(timeline, [timeline, ...toolbar], 'down')).toBe(toolbar[0]);
  });
});

describe('remoteAction', () => {
  it('normalizes remote and keyboard navigation keys', () => {
    expect(remoteAction('ArrowLeft', 0)).toBe('left');
    expect(remoteAction('', 40)).toBe('down');
    expect(remoteAction('Return', 0)).toBe('enter');
    expect(remoteAction('XF86Back', 0)).toBe('back');
    expect(remoteAction('', 10009)).toBe('back');
  });

  it('recognizes Samsung IME completion keys', () => {
    expect(remoteAction('', 65376)).toBe('ime-done');
    expect(remoteAction('', 65385)).toBe('ime-cancel');
  });
});

describe('TVSettings update rows', () => {
  const control = (row: number) => ({ dataset: { focusRegion: 'content', focusRow: String(row), focusCol: '0' } } as unknown as HTMLElement);
  const forget = control(15);
  const check = control(16);
  const apply = control(17);
  const elements = [control(14), forget, check, apply];
  it('walks from the existing rows onto the appended check and apply rows', () => {
    expect(chooseStructuredTarget(forget, elements, 'down')).toBe(check);
    expect(chooseStructuredTarget(check, elements, 'down')).toBe(apply);
  });
  it('walks back up without losing the appended rows', () => {
    expect(chooseStructuredTarget(apply, elements, 'up')).toBe(check);
    expect(chooseStructuredTarget(check, elements, 'up')).toBe(forget);
  });
});

describe('Other projects sidebar entry', () => {
  const menuItem = (row: number) => ({ dataset: { focusRegion: 'sidebar', focusRow: String(row), focusCol: '0' } } as unknown as HTMLElement);
  const settings = menuItem(32);
  const projects = menuItem(PROJECTS_MENU_ROW);
  it('sits below the last fixed menu row and never renumbers it', () => {
    expect(chooseStructuredTarget(settings, [settings, projects], 'down')).toBe(projects);
    expect(chooseStructuredTarget(projects, [settings, projects], 'up')).toBe(settings);
  });
});

describe('modal dialog focus traps', () => {
  const dialogItem = (region: string, row: number, col: number) => ({ dataset: { focusRegion: region, focusRow: String(row), focusCol: String(col) } } as unknown as HTMLElement);
  const contentItem = (row: number) => ({ dataset: { focusRegion: 'content', focusRow: String(row), focusCol: '0' } } as unknown as HTMLElement);
  const hero = contentItem(1);
  const menuSettings = { dataset: { focusRegion: 'sidebar', focusRow: '32', focusCol: '0' } } as unknown as HTMLElement;
  it('keeps the update confirmation dialog isolated from the projects page and the page behind', () => {
    const projectsCard = dialogItem('content', 3, 0);
    const cancel = dialogItem(UPDATE_DIALOG_REGION, 0, 0);
    const elements = [cancel, projectsCard, hero, menuSettings];
    // Nothing inside the dialog region can reach the page, and the projects
    // card can never land on the dialog region from the page behind.
    for (const direction of ['left', 'right', 'up', 'down'] as const) {
      expect(chooseStructuredTarget(cancel, elements, direction)).toBeNull();
      expect(chooseStructuredTarget(projectsCard, [cancel, projectsCard], direction)).toBeNull();
    }
  });
  it('moves only between the update confirmation buttons and never into the page behind', () => {
    const cancel = dialogItem(UPDATE_DIALOG_REGION, 0, 0);
    const confirm = dialogItem(UPDATE_DIALOG_REGION, 0, 1);
    const elements = [cancel, confirm, contentItem(16), contentItem(17)];
    expect(chooseStructuredTarget(cancel, elements, 'right')).toBe(confirm);
    expect(chooseStructuredTarget(confirm, elements, 'left')).toBe(cancel);
    for (const direction of ['up', 'down'] as const) {
      expect(chooseStructuredTarget(cancel, elements, direction)).toBeNull();
      expect(chooseStructuredTarget(confirm, elements, direction)).toBeNull();
    }
  });
});

describe('remoteAction Android back arrivals', () => {
  it('treats GoBack, BrowserBack, and Escape as back', () => {
    expect(remoteAction('GoBack', 0)).toBe('back');
    expect(remoteAction('BrowserBack', 0)).toBe('back');
    expect(remoteAction('Escape', 27)).toBe('back');
  });
});
