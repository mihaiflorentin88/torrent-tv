import { describe, expect, it } from 'vitest';
import { buildPath, parsePath } from '@torrent-tv/shared';
import type { View } from '@torrent-tv/shared';

// — Client routes: every web screen owns a URL, so a refresh or a shared link
// lands on the same view. buildPath and parsePath are pure inverses over the
// adopted map; anything unroutable parses to home, because the server renders
// the app for unknown paths and the app decides what to show.

const adopted: [View, string][] = [
  ['home', '/'],
  ['search', '/search'],
  ['library', '/library/dashboard'],
  ['continue', '/library/continue'],
  ['favorites', '/library/favorites'],
  ['watched', '/library/watched'],
  ['downloads', '/library/downloads'],
  ['library-categories', '/library/categories'],
  ['tracker', '/tracker/dashboard'],
  ['browse', '/tracker/browse'],
  ['categories', '/tracker/categories'],
  ['jobs', '/jobs'],
  ['events', '/events'],
  ['settings', '/settings'],
];

describe('Client routes', () => {
  it('round-trips every adopted route (build then parse)', () => {
    for (const [view, path] of adopted) {
      expect(buildPath({ view })).toBe(path);
      expect(parsePath(path, '')).toEqual({ view, query: '' });
    }
  });

  it('parses a search query and builds it back escaped', () => {
    expect(parsePath('/search', '?q=star+wars')).toEqual({ view: 'search', query: 'star wars' });
    expect(buildPath({ view: 'search', query: 'star wars' })).toBe('/search?q=star%20wars');
    expect(buildPath({ view: 'search', query: 'a&b=c' })).toBe('/search?q=a%26b%3Dc');
    expect(parsePath('/search', '?q=a%26b%3Dc').query).toBe('a&b=c');
  });

  it('round-trips a search through a full URL', () => {
    const url = buildPath({ view: 'search', query: 'interstellar 4K' });
    const [pathname, search] = url.split('?');
    expect(parsePath(pathname, search || '')).toEqual({ view: 'search', query: 'interstellar 4K' });
  });

  it('omits an empty search query from the URL', () => {
    expect(buildPath({ view: 'search', query: '' })).toBe('/search');
    expect(parsePath('/search', '?q=')).toEqual({ view: 'search', query: '' });
  });

  it('keeps the query out of views that do not read one', () => {
    expect(parsePath('/settings', '?q=x')).toEqual({ view: 'settings', query: '' });
  });

  it('round-trips a title deep link for a Canonical title', () => {
    expect(buildPath({ view: 'title', id: 'tt15398776' })).toBe('/title/tt15398776');
    expect(parsePath('/title/tt15398776', '')).toEqual({ view: 'title', id: 'tt15398776', query: '' });
    expect(buildPath({ view: 'title', id: 'tt 123' })).toBe('/title/tt%20123');
    expect(parsePath('/title/tt%20123', '')).toEqual({ view: 'title', id: 'tt 123', query: '' });
    expect(parsePath('/title/tt15398776/', '')).toEqual({ view: 'title', id: 'tt15398776', query: '' });
  });

  it('round-trips a watch deep link with source and resume position', () => {
    const url = buildPath({ view: 'watch', id: 'dl-42', source: 2, t: 61_500 });
    expect(url).toBe('/watch/dl-42?source=2&t=61500');
    expect(parsePath('/watch/dl-42', '?source=2&t=61500')).toEqual({ view: 'watch', id: 'dl-42', query: '', source: 2, t: 61_500 });
    const [pathname, search] = url.split('?');
    expect(parsePath(pathname, search || '')).toEqual({ view: 'watch', id: 'dl-42', query: '', source: 2, t: 61_500 });
  });

  it('keeps an explicit t=0 (skip Household resume) and drops absent watch params', () => {
    expect(parsePath('/watch/dl-42', '?t=0')).toEqual({ view: 'watch', id: 'dl-42', query: '', t: 0 });
    expect(parsePath('/watch/dl-42', '?source=0')).toEqual({ view: 'watch', id: 'dl-42', query: '', source: 0 });
    expect(parsePath('/watch/dl-42', '')).toEqual({ view: 'watch', id: 'dl-42', query: '' });
    expect(parsePath('/watch/dl-42', '?source=-1&t=bogus')).toEqual({ view: 'watch', id: 'dl-42', query: '' });
    expect(buildPath({ view: 'watch', id: 'dl-42', source: -1 })).toBe('/watch/dl-42');
  });

  it('round-trips a job deep link to the Jobs detail pane', () => {
    expect(buildPath({ view: 'jobs', id: 'job-9' })).toBe('/jobs/job-9');
    expect(parsePath('/jobs/job-9', '')).toEqual({ view: 'jobs', id: 'job-9', query: '' });
    expect(parsePath('/jobs/job-9/', '')).toEqual({ view: 'jobs', id: 'job-9', query: '' });
  });

  it('parses deep links with empty, nested, or malformed ids to home', () => {
    for (const path of ['/title', '/watch', '/title/', '/watch/', '/title/%zz', '/title/x/y', '/watch/x/y', '/jobsx']) {
      expect(parsePath(path, '')).toEqual({ view: 'home', query: '' });
    }
    expect(buildPath({ view: 'title' })).toBe('/');
    expect(buildPath({ view: 'watch', id: '' })).toBe('/');
  });

  it('falls back to home for unknown paths (the app decides)', () => {
    for (const path of ['/unknown', '/library/unknown', '/library/dashboard/nested/path', '/LIBRARY/DOWNLOADS', '/jobsx']) {
      expect(parsePath(path, '')).toEqual({ view: 'home', query: '' });
    }
    expect(buildPath({ view: 'gibberish' as View })).toBe('/');
  });

  it('tolerates a trailing slash on routed paths', () => {
    expect(parsePath('/library/downloads/', '')).toEqual({ view: 'downloads', query: '' });
    expect(parsePath('/', '')).toEqual({ view: 'home', query: '' });
  });
});
