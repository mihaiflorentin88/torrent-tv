# Android TV Client Design (TorrentTV)

Date: 2026-09-05
Status: Approved design, pending implementation plan

## Summary

The service gains a second TV client: an Android TV application titled
**TorrentTV**, running on Android 8.0 (API 26) through the newest Android TV
platforms — the 2018-and-newer window the household wants covered. The client
is a thin Kotlin shell around the existing Tizen web application: the same
Preact bundle, the same screens, the same 1,675-line CSS design, the same
5-way D-pad focus engine, rendered in a WebView with video playing on a
native surface behind it. Only the four Tizen-specific seams are re-implemented
natively — the AVPlay-shaped player API (backed by media3 ExoPlayer), the
network info that feeds LAN discovery, app exit, and remote-control key
codes. The app display name becomes a platform-provided constant so both
shells share one web app: Tizen stays "FileList TV", Android presents as
"TorrentTV".

The approach mirrors ADR-0006's philosophy one platform over: one artifact, a
pure support floor, runtime feature detection instead of per-era builds, and
the same austerity rules (`es2017` bundle, no CSS `gap`, guarded
`AbortController`) that the Tizen build already enforces — a factory-fresh
Android 8.0 WebView is the same Chromium era as the Tizen 5.0 engine floor.

## Goals

- Feature-identical port: every screen, player control, subtitle flow,
  download action, and portal surface the Tizen client has, driven by the
  same web code.
- Pixel-identical design: the Tizen UI runs unmodified at 1920×1080; the
  only visible difference is the app name/monogram.
- Support floor Android 8.0 (API 26, 2018 hardware); one APK for every newer
  platform; no per-era builds.
- Sideloading as the distribution path: a release APK with checksum on the
  repository's GitHub releases, installed by hand — consistent with ADR-0008
  ("TV applications update manually").
- The Tizen old-engine smoke keeps validating the same web bundle that
  Android ships.

## Non-goals

- Google Play distribution or any store presence.
- A fully native (Leanback/Compose) UI rewrite.
- Voice search, launcher recommendations rows, or other Android-TV-only
  platform integrations beyond what the Tizen app has.
- Server-side changes beyond the CORS middleware this design requires.
- Automatic updates of the TV client itself (matches the Tizen client and
  ADR-0008).

## Decisions

| Decision | Choice |
| --- | --- |
| Shell | Kotlin Activity hosting a WebView + native video Surface behind it |
| Player engine | media3 ExoPlayer behind an AVPlay-shaped JS bridge |
| Web app source | One shared package `clients/tv` (moved from `clients/tizen/src`), consumed by both platform shells |
| Support floor | `minSdk 26` (Android 8.0, 2018 TVs); `targetSdk` current stable at implementation start |
| Distribution | Release APK + checksum on GitHub releases; manual sideload |
| App identity | Title "TorrentTV"; `applicationId com.torrenttv.app`; initial version 0.1.0 |
| Display name plumbing | Platform-injected constant (shell → web app), not hardcoded strings |
| Server change | Permissive CORS on `/api/v1` so a locally-packaged client can call the API |

## Parity contract

"Identical to the Tizen app" is a testable requirement, not an aspiration.
Three rules make it hold:

1. **One bundle.** Both TVs run the same web artifact built from `clients/tv`
   by the same Vite config. The Android shell ships those bytes as-is; it
   injects no page styling and no UI code of its own. Any visual or
   interaction difference that is not a named exception below is a defect.
2. **No platform branching in UI code.** The shared web app keeps resolving
   runtime differences through feature detection and the platform bridge
   objects, exactly as ADR-0006 prescribes for engine generations. No
   `isAndroid`-style conditionals in screens, components, or CSS — ever.
   Platform behavior lives behind the four seams, never inside the UI.
3. **The inventory below is the acceptance checklist.** The implementation
   plan must verify every line on an Android emulator (API 26 ATV image)
   against the same walkthrough executed on the Tizen old-engine smoke or a
   physical Tizen set.

Inventory (from `clients/tv` as it exists today — every item identical on
TorrentTV):

- **Setup:** LAN discovery scan with progress, discovered-server cards,
  manual address entry through the system IME, connect, rescan, forget
  saved server, status line, saved-server persistence across launches.
- **Home:** backdrop hero with eyebrow/overview/"View versions", Continue
  watching rail with progress bars, Favorites rail, Recently added rail,
  portal promotions rotation with "Advertisement" labeling.
- **Search:** IME query entry, submit, clear, results, Newest/Most
  seeded/Rating/A–Z sort, category filter chip, cursor paging.
- **Library:** dashboard, continue watching, favorites, watched, downloads,
  categories grid → per-category item grid; watched/in-progress badges and
  resume positions on cards.
- **Tracker:** dashboard (recently added + strong swarms), browse with sort
  and paging, categories grid.
- **Title detail:** backdrop, metadata, state badges, overview, resume
  button honoring saved position and watched state, favorite toggle, season
  tabs, season-pack cards (expand, download, pause, resume, retry, delete
  with confirmation dialog), episode rows expanding to per-version source
  buttons, automatic episode-list expansion with live refresh.
- **Downloads:** search, filter (all / still downloading / downloaded /
  paused / errors), sort (recent, title, progress, size, speed), per-download
  play and transfer actions, delete with confirmation dialog, live polling
  with scroll-anchor preservation.
- **Player:** opening/progressive/downloaded messaging, buffering OSD with
  percentage, play/pause/stop, restart, ±10 s, timeline scrub with debounce,
  hidden-controls behavior (any key reveals; left/right scrub; up/down/enter
  refocus), audio track menu with refresh and preference persistence,
  subtitle menu (off, grouped local/built-in/provider candidates, native
  fallback), find-online-subtitles dialog, automatic RO→EN subtitle
  selection chain, subtitle delay ±0.5 s and reset, aspect auto/letterbox/
  full, playback info pane, retry after failure, waiting-for-segments
  recovery with live download progress, completion → next-episode chaining,
  position save at the same cadence, transient OSD messages.
- **Jobs:** list with search, state/kind/retryable/updated filters, paging,
  retry action, detail pane with logs (level/attempt filters, expandable
  entries, load older).
- **Events:** catalog coverage numbers, fetch latest, rebuild catalog.
- **Settings:** preferred audio/subtitle languages, watched threshold,
  worker count, title refresh timeout, environment-managed field display,
  save, dependency tests (filelist, qbittorrent, storage, tmdb, subdl),
  change/forget server, server update check/apply with confirmation dialog,
  status panel, and releases link.
- **Portal & live updates:** "Other projects" menu entry and dialog; SSE
  events (portal, update status/failure, catalog and metadata updates,
  search completion, job updates) with reconnect snapshot reconciliation.
- **Global:** 5-way D-pad navigation with structured focus everywhere,
  long-press-back to exit, focus restoration after dialogs and route
  changes, boot splash, fatal-error panel, client diagnostics reporting.

Named exceptions (the complete list):

- App display name and monogram: "TorrentTV" / "TT" instead of
  "FileList TV" / "FL" (the user-requested rebrand of the Android client).
- The four platform seams, implemented natively behind the same logical
  contracts (player, discovery network info, key codes, exit).

## Approaches rejected

- **Fully native Kotlin (Leanback/Compose) rewrite** — re-implements every
  screen, drifts from the Tizen design by construction, and leaves two TV
  clients to keep in sync forever, for zero feature gain.
- **WebView shell with HTML5 `<video>` playback (no native player)** — the
  smallest shell, but Chromium exposes no JS API for audio-track selection in
  `<video>`, so the player's Audio menu would silently die. Rejected on
  feature-parity grounds.
- **WebView shell loading the server-hosted browser client (`web/`)** — that
  is the browser player with a different interaction model and design, not
  the TV client.

## Architecture

### Repo layout

```
clients/
  shared/       unchanged — platform-agnostic API client, routes, helpers
  tv/           NEW package @filelist/tv — the TV web app, moved by git mv
                from clients/tizen/src (+ index.html, startup.js,
                fatal-error.js); builds dist/ with the existing Vite config,
                same es2017 target and validator rules
  tizen/        packaging only — config.xml, certificates, scripts/package.sh,
                the $WEBAPIS script-tag transform for index.html, icons
  android-tv/   NEW — Gradle/Kotlin shell, its index.html variant (no
                $WEBAPIS tag, TorrentTV branding), banner assets,
                scripts/package.sh equivalent
```

The move is mechanical; the web app keeps its own vitest suites and gains no
build-time platform branching. Per-platform differences resolve at runtime
through feature detection, exactly as ADR-0006 prescribes.

### The four platform seams

1. **Player.** The Android shell exposes a `window.webapis.avplay`-shaped
   object backed by ExoPlayer: `open`, `prepareAsync`, `play`, `pause`,
   `seekTo`, `getDuration`, `getTotalTrackInfo`, `setSelectTrack`,
   `setSilentSubtitle`, `setExternalSubtitlePath`, `setSubtitlePosition`,
   `setDisplayMethod`, `setDisplayRect`, `stop`, `close`, plus the listener
   callbacks (`onbufferingstart`/`onbufferingprogress`/`onbufferingcomplete`,
   `onstreamcompleted`, `oncurrentplaytime`, `onsubtitlechange`, `onerror`).
   The Player component in the web app is untouched — it already programs
   exclusively against this API surface. Video renders on a `SurfaceView`
   behind a transparent WebView in player mode, mirroring Tizen's
   video-plane-behind-web model, so the HTML controls and the HTML-rendered
   VTT subtitle overlay draw on top unchanged. All subtitle candidates
   already arrive from the server as prepared VTT assets, so the existing
   `parseVTT`/`subtitleAt` overlay path works as-is.

   Mapping highlights: ExoPlayer `STATE_BUFFERING` drives the buffering
   callbacks; `STATE_ENDED` drives `onstreamcompleted`; `PlayerError` drives
   `onerror`; a ~500 ms position ticker drives `oncurrentplaytime` (the web
   app keeps its own 10 s playback-save cadence on top of it); audio track
   selection maps to ExoPlayer track-selection overrides; aspect
   AUTO/LETTER_BOX/FULL_SCREEN maps to ExoPlayer video scaling plus surface
   sizing.

2. **Discovery.** A tiny bridge exposes `getIp`/`getSubnetMask`
   (WifiManager/NetworkInterface); `discovery.ts` is reused verbatim,
   including its probe concurrency and progress reporting.

3. **Keys.** Android remotes deliver D-pad as standard arrow keys + Enter
   through WebView, so the focus engine works unchanged. The Activity
   intercepts BACK and forwards it into JS as Tizen's keyCode 10009,
   preserving the hold-5-seconds-to-exit behavior; Android media keycodes
   (85 play/pause, 126 play, 127 pause, 86 stop, 89 rewind, 90
   fast-forward) are added to the existing key tables alongside the Tizen
   codes — no collisions, harmless on Tizen. The Leanback system IME serves
   the search/downloads/jobs inputs; the existing readOnly-until-OK pattern
   applies.

4. **Exit and registration.** `exitApplication()` tries the Tizen API first,
   then the bridge's `exit()`; `tvinputdevice.registerKey` stays guarded and
   no-ops on Android.

### Server CORS

The Tizen widget's `<access origin="*">` bypasses CORS at the platform
level; Android WebView does not. The Go server gains an
`Access-Control-Allow-Origin: *` middleware on `/api/v1` — covering the JSON
API, the SSE event stream, and stream endpoints — so a locally-packaged TV
client can call the server it discovers. Consistent with the single-household
LAN threat model every client already relies on.

### App identity (TorrentTV)

The web app's brand spots (Setup heading, sidebar brand block, header
eyebrow, startup splash, document title) read a platform-provided display
name instead of the hardcoded "FileList TV": Tizen supplies "FileList TV"
with monogram "FL"; Android supplies "TorrentTV" with monogram "TT". Design,
layout, and all other copy are untouched. The Android application label,
launcher banner, and `<title>` say TorrentTV; `applicationId` is
`com.torrenttv.app`; initial version 0.1.0.

## Support floor and compatibility policy

- `minSdk 26` covers 2018 Android TVs (Sony X850F-class, TCL 2018 line;
  Mi Box S on 8.1). One APK, no ceiling — the ADR-0006 "pure floor" posture.
- The existing authoring constraints carry over unchanged: `es2017` bundle
  target, no CSS `gap` (the shared package already builds with the same Vite
  config), `AbortController` already guarded. A factory-fresh Android 8.0
  WebView and a Play-updated modern WebView both run the same bundle.
- Codec reality matches ADR-0006: direct play only (ADR-0001); ExoPlayer
  decodes what the platform MediaCodec provides, so DTS-class audio and AV1
  fail on 2018-era hardware the same way they do on Tizen sets, avoided by
  picking another release at selection time — never ranked away by the
  server.
- WebView localStorage persists the saved server URL exactly as the Tizen
  app store does; cleartext HTTP is permitted (network security config) for
  LAN discovery, API calls, SSE, and streams. The `width=1920,height=1080`
  viewport meta works as-is in Android WebView. Overscan margins on
  misconfigured sets are a physical-verification checklist item, not a code
  path.

## Packaging, updates, and error handling

- Gradle release build produces one universal APK; `clients/android-tv`
  gains a packaging script (assemble + SHA256 checksum, mirroring the Tizen
  script's shape) and the artifact ships on GitHub releases next to the
  other release assets.
- Leanback launcher intent category + banner, landscape, no touch-screen
  requirement, keep-screen-on while the player is open.
- The boot error panel (`startup.js`, `fatal-error.js`) moves with the web
  app unchanged; native-level WebView failures surface through the shell's
  own error state and the existing client-diagnostics endpoint
  (`/api/v1/diagnostics/client`) where the API client is reachable. The
  client never fails silently.
- Server self-update and portal surfaces arrive through the same API/SSE as
  on Tizen; the TV client installs nothing itself.

## Testing

- Existing vitest suites move with `clients/tv` and keep passing unchanged.
- New unit tests: Android keycode mappings in the shared key tables; the
  AVPlay bridge contract, specified as a table of calls/events exercised
  against a fake bridge from the web side.
- CI: the Tizen old-engine smoke keeps booting the same bundle; an Android
  emulator (API 26 ATV system image) installs the APK and smoke-boots the
  app to the setup screen — the analogue of the Tizen headless smoke.
- Same-bundle check: CI asserts the web asset bytes packaged into the APK
  are identical to the dist the Tizen WGT packaging consumes, so "one
  bundle" stays a build fact, not an intention.
- Parity walkthrough: the Parity contract inventory becomes the
  implementation plan's verification script, executed on the Android
  emulator with the Tizen app as the reference behavior; every line checked
  or the difference is either fixed or promoted to a named exception in
  this spec.
- Physical verification stays best-effort until the household names its
  Android TV hardware; per ADR-0006's posture, behavior counts as confirmed
  only by direct observation on a named device. Naming those sets is an
  open follow-up, not a blocker for the floor decision.

## Consequences

- One web app serves two TV platforms; a UI change lands on both by
  construction. Platform regressions are bounded by the four seams and their
  tests.
- The server becomes callable by locally-packaged browsers (CORS `*` on the
  API). This is accepted: the LAN already exposes unauthenticated-by-CORS
  surfaces to every same-origin client, and the household threat model
  (CONTEXT.md) is a single trusted network.
- The AVPlay-shaped bridge is deliberately retained rather than renamed to a
  neutral API: it keeps the Player component byte-identical to Tizen's. A
  neutral rename can come later behind the same contract if a third platform
  ever needs it.
- Versioning starts at 0.1.0 for the Android client regardless of the Tizen
  client's 0.3.0: feature parity is claimed by the design, proven by the
  implementation plan's checks, not by a shared version number.
