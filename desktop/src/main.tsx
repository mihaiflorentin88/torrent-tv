import { render } from 'preact';
import { Events } from '@wailsio/runtime';
import { App } from './App';
import { ServerState } from './bindings/github.com/mihaiflorentin88/torrent-tv/internal/gui/bindings';
import { isStateEvent, seedServerState, setServerOrigin } from './lib/state';

// All view traffic stays same-origin: the shared API points at the app's
// own origin, whose /api/ paths the wails asset server reverse-proxies to
// the supervised server's current address (internal/gui/proxy.go). The
// webview origin is wails://…, so pointing the API at the loopback server
// directly — as the running event's address might suggest — would cross
// origins and be blocked; the event's address is display-only here.
setServerOrigin(location.origin);

// Live events keep the seeded state current (the address feeds the pill and
// the Server page's running line); they also reach every mounted component
// through its own subscription.
Events.On('server:state', event => {
  if (isStateEvent(event.data)) seedServerState(event.data);
});

// The runner's boot emit fires before this webview loads, so a page that
// (re)loads while the server already runs would otherwise miss it: fetch
// the current state once, then render with the seed in place.
ServerState()
  .then(event => { if (isStateEvent(event)) seedServerState(event) })
  .catch(() => { })
  .finally(() => { render(<App />, document.getElementById('app')!) });
