# webOS TV Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Deliver one sideloadable LG webOS TV client with current Tizen feature/design parity, targeting webOS 4.x through webOS 26 without an artificial version ceiling.

**Architecture:** Keep screens, styling, focus and server access in clients/tv and clients/shared. Add a Chromium 53-compatible build and public LG adapters under clients/webos; retain the existing AVPlay-shaped playback boundary and Tizen/Android shared-bundle identity.

**Tech Stack:** Existing Preact 10.29.8, TypeScript 5.9.2, Vite 7.3.6 and Vitest 4.1.10; LG public web APIs, HTML media, Developer Mode CLI, Python package validation, existing GitHub Actions.

**Spec:** docs/superpowers/specs/2026-09-07-webos-tv-client-design.md, committed as bce50cc and subsequently approved in conversation. Read both documents before execution.

## Global constraints

- Required platform target: webOS 4.x (2018–2019) through webOS 26 (2026); all executable assets must support Chromium 53.
- One IPK, no artificial upper-version restriction. webOS 3.x (Chromium 38, 2016–2017) is an optional evidence-backed extension, not a release blocker. webOS 1.x–2.x is out of scope.
- Same current Tizen branding, screens, styles and interactions at the 1920×1080 reference viewport. No separate LG theme or browser-client substitution.
- Same source for all TV clients. Preserve byte-identical Tizen/Android app bundles; webOS may compile different bytes for its older engine.
- Public LG APIs only. No private Luna media APIs, rooted installation, store submission, automatic TV updates, server media conversion or successful no-op adapter methods.
- Preserve Direct play Range URLs, existing Progressive playback recovery, WebVTT overlay, native subtitle fallback and all controls.
- Publish one torrent-tv-<VERSION>-webos.ipk plus SHA-256 checksum through existing release automation, with manual Developer Mode installation/updates.
- No production code has been authorized by approval of this plan document alone. Execution, device pairing/install/settings changes, deployment and release publication require the applicable authorization.
- No LG TV is available. Milestone A (complete implementation plus package/automated/available-runtime evidence) is distinct from Milestone B (hardware-verified parity). Do not convert missing evidence or feature failures into passing results.
- Use repository domain vocabulary. Update all affected callers and preserve existing behavior; do not introduce aliases or parallel UI abstractions.

## Execution discipline

Each task is a reviewable behavior increment, not a scaffolding phase. Before modifying exported symbols, obtain language-server references and inspect all callers. Run meaningful regression cases red before the change and green afterward; use throwaway runtime exercises for broad new behavior instead of permanent wiring tests. Do not add tests that pin source text, copy, incidental defaults or mock forwarding. Commit each completed task with only its owned paths after its checks pass.

Use an isolated worktree at execution time through the using-git-worktrees skill. Keep installs/launches of old browser runtimes isolated and bound to loopback. Never run tools/progressive_stream_smoke.py against the household server merely to prove this client: it can create/delete downloads and alter download-engine settings. Use disposable authorized data for mutating walkthroughs.

---

## File map and dependency order

| Task | Owned files | Deliverable |
| --- | --- | --- |
| 1 | root package.json/package-lock.json; clients/webos/package.json, tsconfig.json, vite.config.ts, index.html, src/compat.ts, src/entry-webos.ts; .gitignore | Chromium 53 classic build using shared screens |
| 2 | clients/tv/src/tv.css, navigation.ts; tools/smoke_tizen_engine/smoke.mjs; tools/smoke_webos_engine/smoke.mjs | Shared layout/scroll compatibility with old/current rendered evidence |
| 3 | clients/tv/src/platform.ts, globals.d.ts, main.tsx, platform.test.ts; clients/webos/src/webos-platform.ts, webos-platform.test.ts, webos.d.ts; clients/webos/vendor/ | Discovery, OS link handoff and exit |
| 4 | clients/tv/src/navigation.ts, navigation.test.ts, player.ts, player.test.ts, main.tsx, platform.ts; clients/webos/src/webos-platform.ts | Remote/IME ownership and suspend/return reconciliation |
| 5 | clients/webos/src/avplay.ts, avplay.test.ts, entry-webos.ts; clients/tv/src/main.tsx | Media lifecycle, actual-event timing and Progressive playback |
| 6 | clients/webos/src/avplay.ts, avplay.test.ts; clients/tv/src/main.tsx; clients/android-tv/app/assets/platform-bridge.js | Real audio/native-text selection, subtitle delay and failure visibility |
| 7 | clients/webos/appinfo.json, icon.png, largeIcon.png; tools/webos_ipk.py, tools/tests/test_webos_ipk.py; Makefile; .github/workflows/ci.yml, release.yml | Validated IPK/checksum and release integration |
| 8 | docs/WEBOS.md, docs/WEBOS-VERIFICATION.md, CONTEXT.md, docs/adr/0010-webos-tv-client.md; README.md; docs/TIZEN.md, docs/ANDROIDTV.md | Complete walkthrough, documented installation and separate acceptance gates |
| 9 | docs/WEBOS-VERIFICATION.md; compatibility files only if the investigation supports adoption | Optional Chromium 38 conclusion with evidence |

Task dependencies: 1 → 2; 1 → 3 → 4; 1 → 5 → 6; 2+3+4+6 → 7 → 8 → 9. Tasks 2, 3 and 5 are independent after Task 1, but owners must coordinate shared main.tsx changes. No concurrent edit ownership for that file. Check the next unused ADR number before writing Task 8; 0010 is the intended name, not permission to overwrite an existing decision.

## Shared interfaces fixed by this plan

Add these structural types in clients/tv/src/platform.ts and use type-only imports in globals.d.ts. LG implements the focused hook, not Android's complete native bridge. No LG global checks occur in components.

```ts
export interface NetworkInfo { ip: string; subnetMask: string }
export interface TVPlatformHooks {
  getNetworkInfo(): Promise<NetworkInfo | null>;
  openExternal(url: string): Promise<boolean>;
  exit(): void;
  onKeyboardVisibility(listener: (visible: boolean) => void): () => void;
  onVisibility(listener: (visible: boolean) => void): () => void;
}
// Added Window field: FileListTVPlatform?: TVPlatformHooks
export function getNetworkInfo(): Promise<NetworkInfo | null>;
export function openExternalURL(url: string): Promise<boolean>;
export function onVirtualKeyboardChange(listener: (visible: boolean) => void): () => void;
export function onAppVisibilityChange(listener: (visible: boolean) => void): () => void;
// Existing exitApplication(): void and registerMediaKeys(): void remain.
```

These are signature declarations describing the contract, not production function stubs. Absent subscription hooks return an unsubscribe closure and emit no synthetic lifecycle events; that is optional capability detection, not a fake media operation. Tizen and Android network methods remain the existing synchronous implementations wrapped by the shared async helper. All external-link callers migrate to await the Promise result. Tizen resolves from launchAppControl callbacks; Android wraps its existing boolean in a resolved Promise. LG resolves from webOSDev.launch callbacks. A timeout or rejected launch resolves false and retains the visible URL.

The media seam remains window.webapis.avplay. Export createAVPlay(): WebOSAVPlay from clients/webos/src/avplay.ts. Its implementation creates the video lazily; module import never reserves a decoder. The type is defined there with exactly these methods:

```ts
export interface AVPlayListener {
  onbufferingstart?(): void;
  onbufferingprogress?(percent: number): void;
  onbufferingcomplete?(): void;
  onstreamcompleted?(): void;
  oncurrentplaytime?(milliseconds: number): void;
  onsubtitlechange?(duration: number, text: string): void;
  onerror?(error: string): void;
}
export interface RawTrack {
  index: number;
  type: 'AUDIO' | 'TEXT' | 'VIDEO';
  extra_info: { track_lang?: string; track_title?: string; codec?: string };
}
export interface WebOSAVPlay {
  open(url: string): void;
  setDisplayRect(x: number, y: number, width: number, height: number): void;
  setDisplayMethod(mode: string): void;
  setListener(listener: AVPlayListener): void;
  prepareAsync(success: () => void, error: (message: string) => void): void;
  play(): void;
  pause(): void;
  seekTo(milliseconds: number): void;
  stop(): void;
  close(): void;
  getDuration(): number;
  getTotalTrackInfo(): RawTrack[];
  setSelectTrack(type: 'AUDIO' | 'TEXT', index: number): void;
  setSilentSubtitle(silent: boolean): void;
  setSubtitlePosition(milliseconds: number): void;
}
```

No getState/getCurrentTime/setSpeed/prepare/selectTrack aliases. No fabricated track rows. The unused setExternalSubtitlePath call/ref is removed in Task 6 rather than supplied as a no-op.

### Task 1: Build the shared application for Chromium 53

**Files:** First row of file map. **Consumes:** existing TV Vite lib-IIFE output and startup files. **Produces:** clients/webos/dist/{index.html,fatal-error.js,startup.js,app.js,app.css}; later tasks add the packaged SDK and adapter through this same build.

- [ ] Capture the current Tizen startup screenshot at 1920×1080 and run npm run build:tv. Record existing failures without treating this as webOS verification.
- [ ] Add workspace @torrent-tv/webos with test = vitest run --passWithNoTests initially, build = tsc --noEmit && vite build. Remove --passWithNoTests when Task 3 adds actual tests. Reuse exact TV dependency versions and shared workspace package. Add build:webos to root scripts. Keep VERSION authoritative; do not add an independently maintained release version.
- [ ] Use the existing Vite lib shape, not multiple IIFE entries or a module-to-classic rewrite plugin:

```ts
build: {
  target: 'chrome53', cssTarget: 'chrome53', cssMinify: false,
  outDir: 'dist', emptyOutDir: true,
  lib: {
    entry: resolve(root, 'src/entry-webos.ts'),
    name: 'TorrentTV', formats: ['iife'],
    fileName: () => 'app.js', cssFileName: 'app'
  }
}
```

Use @preact/preset-vite, base './', and the same static-asset emission pattern as clients/tv/vite.config.ts. Copy fatal-error.js/startup.js from clients/tv, never fork them. Derive the local index from clients/tv/index.html: remove only the Samsung $WEBAPIS script, preserve branding/style/viewport/CSP and diagnostics, insert the LG SDK script after diagnostics in Task 3. Script order is fatal-error.js → startup.js → SDK → app.js. Compatibility imports execute before platform imports and main:

```ts
import './compat';
import '../../tv/src/main';
```

- [ ] Add standards-compliant, feature-detected Object.entries and String.padStart/padEnd compatibility before any shared module evaluates. Use core-js 3.50.0 (exact pin; latest registry version verified 2026-09-07), importing only core-js/modules/es.object.entries, core-js/modules/es.string.pad-start and core-js/modules/es.string.pad-end. Do not hand-roll Unicode/string coercion and property-descriptor semantics. Preserve existing AbortController capability detection/no-abort discovery path; do not add a second timeout policy.
- [ ] Check the built dependency graph for additional unsupported APIs before adopting more polyfills. Existing usage includes fetch, URL/URLSearchParams, EventSource, Map/Set, CustomEvent, Intl and CSS custom properties. Syntax target is necessary, not runtime proof. Run each actual used operation in the Task 2 floor runtime; add a standards polyfill only for an observed gap. For scrollIntoView options, use Task 2's capability fallback rather than a blanket browser shim.
- [ ] Execute npm run build:webos and npm run build:tv; both must build classic scripts. Verify startup missing-app failure using the packaged local index, not a development server module loader. No permanent source-text test; package validation belongs in Task 7.
- [ ] Commit owned files after these checks: feat: build shared TV client for webOS Chromium 53.

### Task 2: Prove old-engine boot, layout and focus without a separate LG stylesheet

**Files:** second file-map row. **Consumes:** Task 1 dist and existing raw-CDP smoke harness. **Produces:** make smoke-webos-engine entrypoint in Task 7; CLI node tools/smoke_webos_engine/smoke.mjs --dist clients/webos/dist --case clean|fatal|broken --output clients/webos/.build/smoke.

- [ ] Obtain the published selenoid/chrome:53.0 linux/amd64 image in an isolated environment. Docker Hub tag existence was checked during planning; image contents and execution have not been verified. First run:

```sh
docker run --rm --platform linux/amd64 --entrypoint sh selenoid/chrome:53.0 -c 'command -v Xvfb; command -v google-chrome; google-chrome --version'
```

Pin the resolved image digest in the harness after this succeeds. Chrome 53 has no --headless: run Xvfb and plain Chrome, bind CDP to loopback, use the existing raw-CDP driver. Query Browser.getVersion and record the actual engine; reject 63 or current Chrome as the floor leg. If this image cannot run, stop this verification gate and record the exact missing runtime prerequisite; do not silently substitute a newer engine.
- [ ] Extend the existing smoke harness by extracting only its reusable HTTP fixture/CDP runner if needed; keep Tizen's command/output/negative-case behavior intact. webOS clean case loads the packaged index at 1920×1080, traverses Setup, opens Manual address, edits and exits the input, connects to the fixture, moves sidebar/content focus, and captures Home. Fatal case triggers a runtime exception and verifies readable Error diagnostics. Broken case removes app.js in a temporary dist copy and must exit nonzero with diagnostic evidence. Store engine version, screenshots and console errors with the output; remove temporary dist copies after the run.
- [ ] Reproduce unsupported Grid and place-items in Chrome 53 before CSS changes. Add feature-query fallbacks to shared tv.css, not webos53.css and not a UA selector. Preserve current Grid on capable runtimes. Use explicit child widths and existing margins rather than gap:

```css
@supports not (display: grid) {
  .tv-app { display: flex; }
  .tv-sidebar { flex: 0 0 104px; width: 104px; }
  .tv-app.menu-open .tv-sidebar { flex-basis: 350px; width: 350px; }
  .tv-app > main { flex: 1 1 0; min-width: 0; }
  .poster-rail { display: flex; }
  .poster-rail > * { flex: 0 0 280px; }
  .tv-library-grid { display: flex; flex-wrap: wrap; }
  .tv-library-grid > * { flex: 0 0 280px; }
  .season-pack-grid { display: flex; align-items: flex-start; }
  .season-pack-grid > * { flex: 0 0 456px; }
  .setup-results { display: flex; flex-wrap: wrap; }
  .setup-results > * { flex: 0 0 50%; min-width: 0; box-sizing: border-box; }
  .tv-list article { display: flex; }
  .tv-list article > :first-child { flex: 1 1 0; min-width: 0; }
  .job-log-expanded dl { display: flex; flex-wrap: wrap; }
  .job-log-expanded dt { flex: 0 0 148px; }
  .job-log-expanded dd { flex: 1 1 calc(100% - 148px); min-width: 0; }
}
```

Finish centering fallback for the actual selectors using place-items (.tv-brand > span, .tv-sidebar button i and the poster placeholder selector): replace their centering with display:flex; align-items:center; justify-content:center universally; no visual behavior depends on Grid there. Check the selectors' actual markup and inherited box sizing when applying the snippet: keep the existing rail/card margins and width totals, including five Library columns. Do not leave an overflowed sixth column or cut off expanded season-pack actions. Compare screenshots and D-pad navigation for menu collapsed/expanded, two discovery columns, posters, downloads and expanded Job logs.
- [ ] In navigation.ts, retain the current native scrollIntoView options where supported. For engines that ignore options, use a capability-detected shared helper that walks scrollable ancestors, computes item bounds relative to each scrollport, and adjusts only the axes needed to bring the item into view. Clamp to scrollWidth-clientWidth / scrollHeight-clientHeight; preserve nearest vertical movement and centered horizontal rail movement. Call it from both focusElement and the focusin handler. Do not detect LG or alter route navigation. The runtime regression is a below-fold row and an off-right poster: moving focus must reveal that exact target without resetting unrelated ancestor scroll positions.
- [ ] Run clean/fatal/broken cases on actual 53 and current Chromium. Compare Tizen and webOS screenshots on identical fixture data and viewport. Existing make smoke-tizen-engine must retain its expected negative-case failure behavior. Commit: fix: preserve TV layout and focus on Chromium 53.

### Task 3: Add asynchronous discovery and accurate OS handoff

**Files:** third row. **Consumes/produces:** TVPlatformHooks and helpers above; discoverServers signature stays unchanged.

- [ ] Add a behavioral regression: a delayed network result must not start scanning early; a failure/timeout leaves Manual address usable. Drive Setup through its visible states with an injected service response rather than testing mock argument forwarding. Add a link-refusal case that retains the displayed address and never shows launch success.
- [ ] Vendor the official webOSTV.js v1.2.13 SDK (download https://webostv.developer.lge.com/assets/library/webOSTVjs-1.2.13.zip; LG states full support for webOS TV 4.0+, exactly the Chromium 53 floor; pin the zip, record upstream URL and SHA-256 in the repo) with its supplied license under clients/webos/vendor; package locally, never fetch code on the TV. Compile or verify this executable asset against the same Chrome 53 floor. Missing SDK is a build failure. Load diagnostics before SDK so an SDK load failure remains visible.
- [ ] Define webos.d.ts only for consumed SDK APIs: webOSDev.connection.getStatus, webOSDev.launch, their success/failure payloads, and document keyboardStateChange detail.visibility. Connection status has wired/wifi objects with state, ipAddress and netmask. Do not add the speculative netmaks alias.
- [ ] Implement network acquisition with a single public SDK path:

```ts
function getNetworkInfo(): Promise<NetworkInfo | null> {
  return new Promise(resolve => {
    let finished = false;
    const finish = (value: NetworkInfo | null) => {
      if (finished) return;
      finished = true; clearTimeout(timer); resolve(value);
    };
    const timer = window.setTimeout(() => finish(null), 3000);
    try {
      window.webOSDev.connection.getStatus({
        subscribe: false,
        onSuccess: response => {
          const active = response.wired?.state === 'connected' ? response.wired
            : response.wifi?.state === 'connected' ? response.wifi : null;
          finish(active?.ipAddress && active.netmask
            ? {ip: active.ipAddress, subnetMask: active.netmask} : null);
        },
        onFailure: () => finish(null)
      });
    } catch { finish(null); }
  });
}
```

The shared platform helper first uses FileListTVPlatform when present; otherwise it reads the existing webapis.network getIp/getSubnetMask and returns null on failure. Setup.scan awaits it before discoverServers(info.ip, info.subnetMask, ports, progress). Retain the exact manual-entry fallback and existing server validation/persistence. Avoid stale scan writes after Setup unmount with a scan generation ref, without adding cancellation/retry policies.
- [ ] Install window.FileListTVPlatform before importing main. Implement LG exit as explicit window.close(). Keep existing Tizen/Android exit paths untouched and return immediately after one dispatch.
- [ ] Change openExternalURL to Promise<boolean> for all platforms. LG launch uses id 'com.webos.app.browser', params {target:url}; resolve true only on success and false on failure/exception/timeout. Tizen resolves from its existing application-control callbacks, not immediately after dispatch. Android keeps its native boolean result. Reject non-http(s) destinations through the same helper, retaining displayed text rather than navigating the TV document. Migrate Projects/promotions and release-link callers through language-server references. Visible URL remains on both success and failure; refusal messages use existing message regions.

```ts
const launched = await openExternalURL(url);
if (!launched) setProjectsMessage('Open this address on another device: ' + url);
```

Use each caller's existing state setter; this snippet is the Projects path, not a new global notification framework. Guard a late result if the caller unmounted. Avoid synthesizing DOM click/keydown events around native launches.
- [ ] Run npm run test -w @torrent-tv/tv and npm run test -w @torrent-tv/webos; run packaged Setup and failed/successful OS callback exercises. Mock callback evidence is UI-only; actual browser launch remains an LG-runtime/hardware check. Commit: feat: integrate webOS discovery and application services.

### Task 4: Preserve remote editing and application lifecycle

**Files:** fourth row. **Consumes:** Task 3 subscription hooks, current session/save/recovery/SSE flow. **Produces:** same logical key actions and reconciled suspended sessions.

- [ ] Add regression cases for keycode 461 in both remoteAction and playerAction. Add a DOM interaction regression: editing an input then receiving keyboard dismissal returns focus to inputExitTarget once; the same Back does not also leave the route. Preserve existing Tizen IME 65376/65385 behavior and media codes 415/19/413/412/417.

```ts
expect(remoteAction('', 461)).toBe('back');
expect(playerAction('', 461)).toBe('back');
```

- [ ] Extend the existing Back conditions with keyCode === 461. Subscribe to document keyboardStateChange inside the LG hook and forward only boolean detail.visibility. In useTVNavigation, factor the existing edit-finish sequence into one local function used by Back/IME keys and visibility false. Track the editing element, not document.activeElement after OS dismissal; ignore events unless this navigation owner started that edit. Clear editing ownership before blur/focus to dedupe events. Do not submit Search when keyboard closes: explicit Search submit remains unchanged.
- [ ] Forward document visibilitychange through onVisibility and dedupe consecutive identical states. Use public webOSRelaunch only to request visible reconciliation when document.hidden is false; never auto-resume because of a duplicate launch event. Clear listeners on unsubscribe. Magic Remote clicks retain normal click handling; no new pointer model.
- [ ] Add Player suspension handling in its existing download/session effect: save first, remember playing.current and current.current, invalidate session, clear recovery/auto-hide/audio-workaround timers, stop/close media. On visible return reopen the same download at the saved position through openPlayer. Add parameter openPlayer(position:number, autoplay = true), use it in prepare success instead of unconditional play, and retain paused state if the user paused before suspension. Do not treat canplay as playing during paused prepare or seek. Keep the ten-second persistence path for hard shutdowns that emit no lifecycle event.
- [ ] Wire App's existing SSE effect explicitly: hidden closes the stream, clears retry timers, invalidates snapshot generation and suppresses onerror retries; visible invokes the existing open() plus loadState/refreshDownloads and refreshes titles/facets/jobs through the same API calls used in connect, without replacing api or remounting Player. Check generation before applying each returned snapshot so a late hidden/server-old response cannot overwrite newer data. Preserve existing portal/update replay reconciliation and promotion visibility behavior; do not create a second reconnect policy.

```ts
const disposeVisibility = onAppVisibilityChange(visible => {
  window.clearTimeout(timer);
  recoveryGeneration++;
  if (!visible) { suspended = true; stream?.close(); return; }
  suspended = false;
  open();
  void loadState();
  void refreshDownloads();
});
```

Declare suspended in that effect, guard open/onerror with stopped || suspended, include disposeVisibility in its cleanup, and integrate the specified titles/facets/jobs refresh beside these calls with the same generation check. The snippet shows the lifecycle edge; existing snapshots remain the source of truth.
- [ ] Exercise hidden→visible while playing, paused, preparing and recovering. Confirm saved position, one reopened media instance, no stale next-episode event, no duplicate SSE stream, and preserved dialog/focus return. Run shared tests plus Tizen and Android UI checks; LG long-press Back and system keyboard behavior remain recorded hardware cases. Commit: feat: preserve TV input and playback lifecycle on webOS.

### Task 5: Supply actual LG media through the existing playback seam

**Files:** fifth row. **Consumes:** WebOSAVPlay above, existing Player flows/recovery. **Produces:** real event-driven progress, duration, aspect, buffering and Progressive playback on webOS; no new success contracts.

- [ ] Write behavioral tests first: metadata-gated duration; seek observable only through actual time events (no immediate requested echo); close/source replacement suppresses stale events, including late metadata/seek/ended; buffering start/progress/complete before playable; one ended per segment, re-armed by play/seek for next-episode completion; recorded metadata error fails prepareAsync with a message; object hidden during playback and restored on close; video geometry matches the shell and aspect mapping AUTO/LETTER_BOX to contain, FULL_SCREEN to cover. No tests for open/getDuration/setDisplayRect argument forwarding or constructor mocking.
- [ ] Build the adapter as promise-chained ES2016-compatible code (Chromium 53 has no async functions): element created only at prepare; play() rejection AbortError swallowed as benign, others surfaced as onerror; MS/s conversions rounded at the boundary; buffering progress from buffered ranges with documented HTML approximation, complete per playable episode; error events mapped to messages with recovery; visible retry reuses existing recover flow. Exact method set above; no getState/getCurrentTime/setSpeed or empty externalSubtitlePath method. Cancellation token semantics remain explicit.
- [ ] Render a positioned video with inline style top:0;left:0;width:100%;height:100% inside .player-shell, hidden Samsung object restored on close. Reapply setDisplayMethod on live changeAspect. Keep subtitle overlay/controls above with existing z-index and set native text rendering off. No platform checks or LG-specific CSS in shared components.
- [ ] Install window.webapis.avplay before main imports; Player missing-avplay failure path remains unchanged. Preserve direct Range URLs, existing SSE/download-state recovery (2-second reopens, completed single retry), saved position seek, ten-second saves, session token stop/close, next-episode completion. addEventListener error ordering stays idempotent.
- [ ] Integrate entry: import avplay module, install adapter, then main, honoring Task 1 order.
- [ ] Run TV/webos tests plus Tizen/Android UI checks; smoke current engine and Chrome 53 floor including playback and seek assertions in both. Simulator/device runtime remains Milestone B. Commit: feat: play direct media through webOS adapter.

### Task 6: Make track selection and subtitle behavior honest

**Files:** sixth row. **Consumes/produces:** real WebOSAVPlay and shared Player/Android callers.

- [ ] Tests first: no audioTracks collection or out-of-range index throws rather than silently keeping old audio; successful selection toggles exactly one enabled track; delayed audio-track arrival after metadata triggers one refreshTracks; native text selection uses an actual hidden text track and forwards cue text through onsubtitlechange with real durations; Off disables every native track and clears overlay; setSubtitlePosition shifts native cue selection by delay and reports unsupported accurately; shared chooseTrack failure visibly surfaces errors without preference save/success text while Android continues optimistic; TextTrack availability/cue timing handled at boundary.
- [ ] In avplay.ts setSelectTrack(type,index): inventory from video.audioTracks (LG-documented webOS 3.0+) or video.textTracks; throw RangeError when collection/type/index invalid; set audioTracks[i].enabled selection or textTracks[i].mode='hidden'; confirm flags before returning; on failure restore previous enabled/mode and throw before shared success reporting. Surface onerror for decoder/media failures. Observe AudioTrackList change events without audible claims; hardware audition remains Milestone B. No fabricated tracks; render selected hidden TextTrack cues through existing overlay with real cue timing; no native rendering duplication. Remove dead externalSubtitlePath call/ref from shared/Android code after reference scan; existing prepare flow unchanged.
- [ ] In main.tsx chooseTrack: track callback success/failure; observed failures surface message and skip saves/toast while closing menu for existing UX; success/current behavior remains. Keep audio 120ms refresh; it is already position-preserving. Keep native subtitle text in overlay.

```ts
catch (error) {
  setMessage('Could not select the ' + (type === 'AUDIO' ? 'audio track' : 'subtitle') + ': ' + (error as Error).message);
  closeMenu();
  return;
}
```

- [ ] Mirror the same callback-observed semantics in Android bridge selectTrack: exception callback on failure, no silent swallowed setSelectTrack, existing optimistic UI remains when no exception. Shared adaptation covers both without an LG-specific branch.
- [ ] setSubtitlePosition(milliseconds): shift native text cue evaluation by the shared ±10s delay for webOS-backed playback instead of no-op. If MediaTrack cue access cannot support negative delay on a runtime, record that limitation in verification docs rather than claiming success; Android current passthrough remains unchanged. Preserve delay/reset UI. Re-evaluate cues on seek/timeupdate.
- [ ] Run TV, Android unit checks where present, and webos tests; run packaged current/53 smoke including audio/native-text assertions and picker interaction. Audible switching and old-engine codec fallback stay documented hardware cases. Commit: fix: report honest track and subtitle behavior.

### Task 7: Package, validate and release one IPK

**Files:** seventh row. **Consumes:** Task 1-6 dist/webos-platform and pinned deps. **Produces:** make webos-ipk, validate-webos-ipk; release assets torrent-tv-<VERSION>-webos.ipk + .sha256; CI webos job.

- [ ] Pin the official CLI at @webos-tools/cli 3.2.5 (exact registry version verified 2026-09-07, pure JS, node >=14.15.1, provides ares-package which emits <id>_<version>_all.ipk and auto-validates appinfo.json); never invent versions. Record CLI in package.json and install only inside the packaging job; document the download source in docs/WEBOS.md. Continue if registry is unreachable by noting the unresolved step rather than fabricating.
- [ ] Author appinfo.json per official LG packaging documentation: required id, version (injected from root VERSION at pack time), vendor, type, main, icon/largeIcon, bgImage/splashBackground, disableBackHistoryAPI:true, no Samsung/wgt fields. Validate required fields/types/asset presence, no Samsung keys, and declared version==VERSION (mirroring existing "Verify declared versions").
- [ ] Derive icons from the existing Tizen identity asset, verifying exact source names/sizes against LG docs before generation. Do not invent colors/branding. Record derivation commands.
- [ ] Implement tools/webos_ipk.py pack/validate in existing tizen_wgt.py style. Pack through the official ares CLI when available: ares-package dist --appinfo appinfo.json -o clients/webos/.build. Verify the generated archive layout against that official tool output; never hand-roll ipk internals. Validate: exact ar members debian-binary/control.tar.gz/data.tar.gz, control fields/versions, packaged file list, declared version, SHA-256 checksum emission, all scripts present, no ES module syntax, no $WEBAPIS, vendored SDK present, no dev artifacts. Python unittests in tools/tests/test_webos_ipk.py using fixture archives; no network in tests. Failure exit codes/messages match tizen_wgt.py conventions.
- [ ] Makefile targets webos-ipk and validate-webos-ipk following tizen-wgt/validate-tizen-wgt patterns and OS-guarded recipe rules. CI adds webos job: npm ci, npm run build:webos, python3 -m unittest tools.tests.test_webos_ipk, packaging and validation, smoke-webos-engine with digest-pinned image, cmp same-bundle app.js/app.css after webos build to prove untouched bytes, upload artifacts. Node 24 on existing runners. Release workflow gains webos artifacts/checksum/version verification. Avoid unsupported make line continuations.
- [ ] Run make webos-ipk validate-webos-ipk, package smoke on floor/current runtimes, existing tizen/android checks plus python3 -m unittest discover -s tools/tests -p 'test_*.py' proving unchanged behavior, and record a manual validation transcript for Milestone A. Commit: feat: package and release webOS client.

### Task 8: Complete verification, documentation and glossary

**Files:** eighth row. **Consumes:** Tasks 1-7 evidence and commands.

- [ ] Execute the full walkthrough matrix at 1920×1080 with identical server fixture data on current Chromium and actual Chrome 53 engine, recording per-scenario engine, data, steps, observed result and screenshot. Automated-only cases (failure dialogs, SSE interruption, protected deletion on disposable data) use an explicitly authorized disposable server. Never run destructive scenarios against household production data. Capture loading/empty/error/dialog states and focus restoration in both engines. Attach command outputs/log excerpts.
- [ ] Docs/WEBOS.md: requirements, Developer Mode activation/renewal warnings (disable/expiry uninstall behavior), pairing/install/update/uninstall via ares CLI and TV Developer Mode app, packaging commands, verification commands, runtime notes, deliberate differences (system keyboard, OS browser handoff, package metadata). No store/auto-update/rooted claims. Credentials/pairing keys stay out of the repository.
- [ ] Docs/WEBOS-VERIFICATION.md: walkthrough evidence, Milestone A automated/available-runtime results, explicit Pending hardware matrix (audio audition, native subtitle track presence, delayed-cue support, keyboard visibility, Back long-press, browser handoff, suspend/resume, codec coverage, incomplete-torrent seeking, update/asset behaviors) with exact steps, plus engine/fixture/IPK checksum.
- [ ] CONTEXT.md glossary: webOS entry alongside Tizen/Android defining scope, package ID and milestone split. ADR 0010: context/decision (shared source, Chromium 53 floor, one IPK, honest playback contract, LG exit/network/back/keyboard behaviors, release flow, deferred 3.x, hardware pending) and consequences, cross-referencing ADR-0006/0008/0009 and the spec. README clients table lists the new client with build/package/verify commands. TIZEN.md/ANDROIDTV.md gain pointers where they describe shared behavior, without changing their own instructions.
- [ ] Confirm checks: npm run test:clients, npm run build, webos build, Python tooling suite, make targets, smoke-webos-engine. No unverified claims anywhere: no LG TV executed, no Milestone B assertions. Distinguish Milestone A/B explicitly. Commit: docs: document webOS client verification and packaging.

### Task 9: Optional webOS 3.x investigation

**Files:** ninth row (conditional). **Consumes:** Milestone A completion. **Produces:** evidence-backed adopt/defer decision recorded in docs/WEBOS-VERIFICATION.md.

- [ ] Build/validate a candidate package against an actual Chromium 38 runtime with the same harness discipline as Task 2 (publicly obtainable image required; no success without execution). Inventory remaining gaps: CSS Grid support timeline, typed array/Intl availability, any adapter code path, ES5 transpilation size. Evaluate actual perf/memory on representative hardware only if real hardware becomes available.
- [ ] Adopt 3.x coverage only with passing the same walkthrough set on a real 38 engine and separately recorded evidence, keeping one IPK and no reduced feature set. Defer explicitly otherwise; do not ship speculative partial support. Either outcome updates docs/WEBOS-VERIFICATION.md and ADR 0010 in a separate commit. No claims about unsupported futures. Commit: docs: record webOS 3.x investigation decision.

## End-to-end walkthrough matrix

Run these scenarios against an isolated server and owned test media, not the household's production downloads. Use the same server snapshot, viewport, and initial Household state for both clients. Mutating scenarios must start from independent resettable data. Browser service mocks may exercise error/loading states, but label that evidence as UI-only rather than end-to-end server or LG playback evidence.
Tasks 1–7 each exercise the scenarios their slice touches; Task 8 records the full evidence rows and physical-pending items.

| ID | Steps | Observable acceptance |
| --- | --- | --- |
| UI-01 | Cold boot without a saved server; wait for discovery; rescan; switch to Manual address | Startup remains readable, discovery reaches a terminal result, every action is D-pad reachable |
| UI-02 | Focus address without OK, press OK, edit, finish/cancel IME; connect to an invalid then valid local server | Traversal does not open IME, one keypress triggers one action, errors remain visible, focus returns to Connect |
| UI-03 | Relaunch after connection; change server; forget it | The correct server is reused, change validates before acceptance, forgetting returns to Setup |
| UI-04 | Traverse Home hero, overview/version action and every rail; favorite a movie and partially watch a series | Artwork/copy/spacing match Tizen; rails and badges reflect Household state without duplicate canonical titles |
| UI-05 | Enter Search text without submitting; submit; filter/sort/page; complete the search Job | No request before submit, cached and completed results refresh correctly without losing focus |
| UI-06 | Open My Library/Tracker dashboards and category grids, then a movie and series | Correct category data, watched/download badges, canonical detail navigation and resume episode |
| UI-07 | Expand season pack alternatives and episode versions; start/pause/resume/retry a Managed download | Expansion alone does not download, state belongs to the selected Release, other versions remain selectable |
| UI-08 | Cancel then confirm protected deletion on disposable data | Cancel changes nothing; confirm removes only the selected Managed download; focus returns predictably |
| UI-09 | Search/filter/sort Downloads; retain focus while telemetry updates and a row is inserted above the viewport | Focus stays attached to the same action and scroll position remains anchored; telemetry is readable |
| UI-10 | Open a Source, reveal/hide controls, operate timeline and toolbar using D-pad and media keys, then Back | ±10s seeks and restart preserve focus; panel Back precedes player exit; original card regains focus |
| UI-11 | Switch audio while playing and paused; refresh tracks; reopen/resume the Source | Real available labels/identities persist, preference is retained; actual audible switching is a hardware check |
| UI-12 | Select contained/embedded/provider subtitles, use RO→EN preference, Off, native fallback, ±0.5s delay and reset | Correct overlay cues and saved selection; no duplicate native overlay; native behavior remains hardware-pending until observed |
| UI-13 | Change every aspect mode; open information; complete an episode | Layout remains beneath shared controls, information is real, next episode and saved position follow existing behavior |
| UI-14 | Exercise incomplete download opening, temporary missing pieces, eventual completion, terminal media failure and Retry | Existing progress/recovery policy remains visible; no new conversion URL or hidden successful failure |
| UI-15 | Search/filter/page Jobs, retry a failed Job, open logs, filter levels/attempts, expand context and load older | Server state and pagination are correct, disclosure is operable with OK, focus remains stable |
| UI-16 | Run Fetch latest and Rebuild catalog from Events on isolated data | Correct Jobs/status are shown; actions are not duplicated |
| UI-17 | Save safe Settings, inspect environment-managed fields and dependencies, check server updates, cancel Apply | Managed fields cannot be overridden; failures are visible; cancellation never restarts the server |
| UI-18 | Apply an update only on an explicitly authorized disposable server | Confirmation explains interruption and manual TV updates; returned server state reconciles after restart |
| UI-19 | Open Projects and promotion links; vary valid donor/public-settings failure states | Current Projects page and clickable promotions match Tizen; visibility follows server state; refused OS handoff retains the URL |
| UI-20 | Interrupt/reconnect SSE, navigate dialogs, suspend/resume in catalog and playback | State reconciles once; focus is restored; closed media cannot emit stale completion or recover in the background |
| UI-21 | Break a local script load and trigger an uncaught startup error in the disposable package | Readable startup/Error panel survives instead of an unexplained blank surface |

For each scenario record package version, runtime/engine, server fixture, result, screenshot or interaction evidence, and triage for failures. Test critical access/playback paths first, but do not omit later rows. Loading, empty and API-error variants belong to their owning row. Compare actual screenshots; source identity and a successful build do not establish visual parity.

Physical-only acceptance additionally records model, firmware, IPK checksum, container/video/audio/subtitle codecs and exact actions. Confirm audible audio switching, native cue access, aspect/video layering, progressive seeking after pieces arrive, codec failure behavior, system keyboard ownership, OS exit/link handoff and suspend/resume. No named LG hardware currently exists, so record these as pending rather than importing simulator results. A 2018-era result and a current result do not verify every intervening firmware.


## Prerequisites and open decisions

Resolved: platform/network/LG exit behavior, playback adapter contract, Chromium 53 build shape, IPK packaging flow, runtime source (pending execution), SDK vendoring source (pending license/record), walkthrough matrix, docs scope.

Execution-time verifications (blockers if unmet, never silently skipped): obtain selenoid/chrome:53.0 image and confirm Xvfb path; obtain official webOSTV.js SDK with license; confirm ares CLI availability/registry versions; confirm public IPK structure via official tool output; confirm official SDK API signatures against LG docs during implementation.

Requires user decision before implementation execution: none outstanding; app identity com.torrenttv.app resolved from Android package. If any verification cannot complete, record the missing prerequisite visibly and continue only with unrelated work.

## Completion criteria

Milestone A: complete implementation; both engine smokes pass including setup/fatal/broken and media assertions; full walkthrough matrix executed on available runtimes with evidence; IPK/checksum validated and release-integrated; Tizen/Android checks unchanged; documentation/glossary/ADR complete; pending hardware matrix explicit. Milestone B: physical TV execution of hardware matrix with recorded model/firmware/evidence and no unverified claims. Neither milestone is complete with hidden failures or omissions; discovered defects return to implementation rather than being waived.

---

*Plan version 1, 2026-09-07. Spec: docs/superpowers/specs/2026-09-07-webos-tv-client-design.md (commit bce50cc). Plan tasks verified against current repository state and public LG documentation; no production code, builds, or hardware runs occurred during planning.*
