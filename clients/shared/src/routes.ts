// Client routes: every web screen owns a URL, so a refresh or a shared link
// lands on the same view. parsePath and buildPath are pure inverses over the
// adopted map; unknown paths parse to home, because the server renders the app
// for any path and the app decides what to show. Three parameterized routes
// reach past the sections: a Canonical title's Detail overlay, a Managed
// download in the player (source = Source file index, t = resume position in
// ms; t = 0 deliberately skips Household resume), and a Job's detail pane.
// Ids parse through even when the view no longer knows them; views handle
// absence.

export type View = 'home' | 'search' | 'library' | 'continue' | 'favorites' | 'watched' | 'downloads' | 'library-categories' | 'tracker' | 'browse' | 'categories' | 'jobs' | 'events' | 'settings' | 'projects' | 'title' | 'watch';
export interface Route { view: View; query?: string; id?: string; source?: number; t?: number }

const viewPaths: Partial<Record<View, string>> = {
  search: '/search',
  library: '/library/dashboard',
  continue: '/library/continue',
  favorites: '/library/favorites',
  watched: '/library/watched',
  downloads: '/library/downloads',
  'library-categories': '/library/categories',
  tracker: '/tracker/dashboard',
  browse: '/tracker/browse',
  categories: '/tracker/categories',
  jobs: '/jobs',
  events: '/events',
  settings: '/settings',
  projects: '/projects',
};

const pathViews: Record<string, View> = { '/': 'home' };
for (const [view, path] of Object.entries(viewPaths)) pathViews[path] = view as View;

// Prefix stems of the parameterized routes; the rest of the segment is the id.
const parameterized: [stem: string, view: View][] = [['/title/', 'title'], ['/watch/', 'watch'], ['/jobs/', 'jobs']];

export function parsePath(pathname: string, search = ''): Route {
  const path = pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname;
  const staticView = pathViews[path];
  if (staticView) return { view: staticView, query: staticView === 'search' ? new URLSearchParams(search).get('q') || '' : '' };
  const match = parameterized.find(([stem]) => path.startsWith(stem));
  if (!match) return { view: 'home', query: '' };
  let id = '';
  try { id = decodeURIComponent(path.slice(match[0].length)) } catch { }
  if (!id || id.includes('/')) return { view: 'home', query: '' };
  if (match[1] !== 'watch') return { view: match[1], id, query: '' };
  const params = new URLSearchParams(search);
  const route: Route = { view: 'watch', id, query: '' };
  const source = Number(params.get('source'));
  if (params.has('source') && Number.isInteger(source) && source >= 0) route.source = source;
  const resume = Number(params.get('t'));
  if (params.has('t') && Number.isInteger(resume) && resume >= 0) route.t = resume;
  return route;
}

export function buildPath(route: Route): string {
  if (route.view === 'search' && route.query) return `/search?q=${encodeURIComponent(route.query)}`;
  const id = route.id ? encodeURIComponent(route.id) : '';
  if (route.view === 'title' && id) return `/title/${id}`;
  if (route.view === 'watch' && id) {
    const params = new URLSearchParams();
    if (route.source !== undefined && route.source >= 0) params.set('source', String(route.source));
    if (route.t !== undefined && route.t >= 0) params.set('t', String(route.t));
    const query = params.toString();
    return `/watch/${id}${query ? `?${query}` : ''}`;
  }
  if (route.view === 'jobs' && id) return `/jobs/${id}`;
  return viewPaths[route.view] || '/';
}
