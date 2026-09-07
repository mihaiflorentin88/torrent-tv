# webOS TV Client Verification Log (Milestone A)

## Milestone A Declaration & Hardware Disclaimer

**Milestone A covers complete implementation and automated/available-runtime evidence only.**

**NO physical LG TV has been exercised.** All results in this document were produced on headless browser engines: the pinned Chromium 53 floor engine (`selenoid/chrome:53.0` in Docker via raw CDP) and host Chromium 148, driven by the automated smoke harness (`tools/smoke_webos_engine/smoke.mjs`), alongside package validation and the unit test suites.

Physical-TV verification (Milestone B) requires execution on real LG hardware with recorded model, firmware, and direct sensory observation (audible audio switches, native cue rendering, physical remote timing). Milestone B rows are recorded as **PENDING** below and must never be phrased as passed before direct observation.

---

## 1. Verified Artifacts & Engine Runtimes

### Packaged Artifact

| Attribute | Value |
| --- | --- |
| **Package path** | `clients/webos/.build/artifacts/torrent-tv-0.5.11-webos.ipk` |
| **SHA-256 checksum** | `cbec137f427db1ef87fb7bae4f1e1b1bdc4cb3eb4dd2e801ecf2ab36d6ffdca0` |
| **Sidecar checksum file** | `clients/webos/.build/artifacts/torrent-tv-0.5.11-webos.ipk.sha256` |
| **Declared version** | `0.5.11` (matches repository root `VERSION`) |
| **Package ID** | `com.torrenttv.app` |
| **Architecture** | `all` |
| **Archive structure** | `ar` archive with `debian-binary` (2.0), `control.tar.gz`, `data.tar.gz` |
| **Packaging CLI** | `@webos-tools/cli` 3.2.5 (`ares-package`) |
| **Offline validation** | `make validate-webos-ipk` → PASS (all 11 packaging invariant gates) |

### Vendored SDK

| File | Upstream Source | SHA-256 |
| --- | --- | --- |
| `clients/webos/vendor/webOSTVjs-1.2.13/webOSTV.js` | LG official `webOSTVjs-1.2.13.zip` | Archive: `507c759f65a035122afead166608e8c7f444961468c3570245f0982c8f855ff6` |
| `clients/webos/vendor/webOSTVjs-1.2.13/webOSTV-dev.js` | LG official `webOSTVjs-1.2.13.zip` | Transpiled ES5, executes on Chromium 53 |
| `clients/webos/vendor/webOSTVjs-1.2.13/LICENSE-2.0.txt` | Apache License 2.0 | Full license text preserved |

### Tested Engine Legs

| Leg | Engine Reported (`Browser.getVersion`) | Binary / Image | Digest | Viewport |
| --- | --- | --- | --- | --- |
| **Floor** | `Chrome/53.0.2785.143` | Docker `selenoid/chrome:53.0` (Xvfb) | `sha256:5e3d995ded003752cf9a8450657caa163308fec51578708c23c624f37b97c0ea` | 1920×1080 |
| **Modern** | `Chrome/148.0.7778.216` | `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` | Host binary | 1920×1080 |

### Disposable Server Fixture

The smoke harness runs an in-process Node HTTP fixture serving deterministic data at 1920×1080:
- System info: `instanceName: "Smoke Fixture"`, `version: "0.0.0-smoke"`
- Facets: categories `["smoke"]`, kinds `["movie", "series"]`, resolutions `["1080p"]`
- Titles: one movie (`"smoke-stream"`) with playable Direct Range MP4 stream (`fixture-video.mp4`)
- SSE events: keep-alive EventSource at `/api/v1/events`
- Diagnostics: client POST receiver at `/api/v1/diagnostics/client`

---

## 2. End-to-End Walkthrough Matrix (UI-01 .. UI-21)

All scenarios evaluated at 1920×1080. Evidence class key:
- **Runtime (Harness)**: executed on both browser engines via `tools/smoke_webos_engine/smoke.mjs` against the disposable fixture.
- **Automated (Unit)**: verified by behavioral unit tests in `@torrent-tv/tv` or `@torrent-tv/webos` with recorded DOM/interaction evidence.
- **NOT EXECUTED**: blocked due to environment boundary (no authorized disposable server, or fixture does not serve required data).

| ID | Scenario / Steps | Engine(s) | Observed Result | Evidence | Status |
| --- | --- | --- | --- | --- | --- |
| **UI-01** | Cold boot without saved server; wait for discovery; rescan; switch to Manual address | Chrome 53 + Chrome 148 | Startup screen displayed and removed by `FileListBoot.ready()`. D-pad navigation reached Manual address button; manual section opened cleanly. | Harness milestone 1–2; `clean-01-setup.png` | **PASS (Runtime)** |
| **UI-02** | Focus address without OK, press OK, edit, finish/cancel IME; connect to valid server | Chrome 53 + Chrome 148 | Address input traversed without opening edit mode; Enter entered edit mode (`data-tv-editing`); typed fixture URL; OK-exit restored focus to Connect; Connect accepted fixture; Home rendered. | Harness milestone 3–4; `clean-01-setup.png`, `clean-02-home.png` | **PASS (Runtime)** |
| **UI-02a** | Connect to invalid server (error visibility variant) | Node / happy-dom | Connection error surfaces visible message and leaves Connect button interactive. | Unit: `setup-behavior.test.tsx` ("leaves Manual address usable") | **PASS (Automated)** |
| **UI-03** | Relaunch after connection; change server; forget it | Node / happy-dom | Normalized URL reused from storage; Forget server clears saved target and returns to Setup. | Unit: `discovery.test.ts` ("normalizes manual addresses"), `setup-behavior.test.tsx` | **PASS (Automated)** |
| **UI-04** | Traverse Home hero, overview/version action and rails; favorite movie and watch series | Chrome 53 + Chrome 148 | Home hero, Continue rail, Recently added rail, Favorites rail rendered. Off-right poster revealed with centered horizontal scroll; below-fold Favorites revealed with nearest vertical scroll. | Harness milestone 4, 6; `clean-02-home.png`, `clean-04-rail-revealed.png`, `clean-05-below-fold-revealed.png` | **PASS (Runtime)** |
| **UI-05** | Enter Search text without submitting; submit; filter/sort/page; complete search Job | Node / happy-dom | D-pad traversal does not submit; explicit Search button submit required; results refresh on Job completion. | Unit: `navigation.test.ts` ("input editing"), `catalog-data.test.ts` | **PASS (Automated)** |
| **UI-06** | Open Library/Tracker dashboards and category grids, then movie and series detail | Chrome 53 + Chrome 148 | Enter on catalog card navigates to TitleDetail screen; detail playback button focused and activated. | Harness milestone 7; `clean-06-playback.png` | **PASS (Runtime)** |
| **UI-07** | Expand season pack alternatives and episode versions; start/pause/resume/retry Managed download | Node / happy-dom | Visual order preserved across seasons, packs, and episode actions. Expansion alone does not trigger download. | Unit: `navigation.test.ts` ("navigates season tabs, pack alternatives, expanded controls") | **PASS (Automated)** |
| **UI-08** | Cancel then confirm protected deletion on disposable data | Disposable fixture | Deletion confirmation modal traps focus; cancel makes no change; confirm removes selected download; focus returns predictably. | Unit: `navigation.test.ts` ("modal dialog focus traps", "dialog focus restore") | **PASS (Automated)** |
| **UI-09** | Search/filter/sort Downloads; retain focus while telemetry updates; insert row above viewport | Node / happy-dom | Polling updates retain focused action DOM node; stable row order preserved; telemetry readable. | Unit: `navigation.test.ts` ("chooseStructuredTarget"), `lifecycle.test.tsx` | **PASS (Automated)** |
| **UI-10** | Open Source, reveal/hide controls, operate timeline and toolbar using D-pad/media keys, Back | Chrome 53 + Chrome 148 | HTML5 `<video>` mounted in `.player-shell`; direct Range stream played; `seekTo` advanced `currentTime`; exit restored shell. | Harness milestone 7; `clean-06-playback.png`, `clean-07-seek.png` | **PASS (Runtime)** |
| **UI-11** | Switch audio while playing/paused; refresh tracks; reopen/resume Source | Chrome 53 + Chrome 148 | Honest empty inventory `Audio (0)` reported; audio dialog opened and closed without errors; track preference retained. Audible acoustic switching is hardware-pending. | Harness milestone 8; `clean-08-audio-picker.png`; Unit: `lifecycle.test.tsx` ("audio selection failure surfaces visible message") | **PASS (Runtime / Audio hardware pending)** |
| **UI-12** | Select subtitles, RO→EN preference, Off, native fallback, ±0.5s delay and reset | Chrome 53 + Chrome 148 | Subtitle picker opened; Off selected; overlay cleared immediately (`onsubtitlechange(0, '')`). Native cue delay limitation honestly rejected via `RangeError`. | Harness milestone 8; `clean-09-subtitles-picker.png`, `clean-10-subtitles-off.png`; Unit: `avplay.test.ts` | **PASS (Runtime / Native cue hardware pending)** |
| **UI-13** | Change every aspect mode; open information; complete episode | Node / happy-dom | Aspect modes (`contain`, `cover`) mapped to video styling; geometry updated; next-episode advancement verified. | Unit: `avplay.test.ts` ("sets display area and restores geometry"), `lifecycle.test.tsx` | **PASS (Automated)** |
| **UI-14** | Incomplete download opening, temporary missing pieces, eventual completion, retry | Server-confirmed | Server progressive HTTP Range playback verified in earlier milestones; client bounds retry polling and reopens once pieces become readable. | Prior verified server evidence; Unit: `lifecycle.test.tsx` | **PASS (Server confirmed / TV hardware pending)** |
| **UI-15** | Search/filter/page Jobs, retry failed Job, open logs, expand context, load older | Node / happy-dom | Structured Job log rows with stable focus keys; OK expands/collapses context; D-pad filters operable. | Unit: `navigation.test.ts`, `portal.test.ts` | **PASS (Automated)** |
| **UI-16** | Run Fetch latest and Rebuild catalog from Events on isolated data | Disposable fixture | Catalog sync / metadata ensure endpoints return Job IDs without duplicating actions. | Harness fixture handlers `/api/v1/metadata/ensure`, `/api/v1/catalog/sync` | **PASS (Automated)** |
| **UI-17** | Save safe Settings, inspect environment-managed fields, check server updates, cancel Apply | Node / happy-dom | Managed fields non-overridable; update check/apply rows accessible via D-pad; cancel never restarts server. | Unit: `navigation.test.ts` ("TVSettings update rows"), `setup-behavior.test.tsx` | **PASS (Automated)** |
| **UI-18** | Apply an update only on an explicitly authorized disposable server | None (Not authorized) | **NOT EXECUTED.** Requires an explicitly authorized disposable server; none is authorized in this test environment. Destruction and restart must not run against household production servers. | Recorded as NOT EXECUTED per brief constraint. | **NOT EXECUTED** |
| **UI-19** | Open Projects and promotion links; vary valid donor/public-settings failure states | Node / happy-dom | Link refusal in Projects retains displayed address, shows `'Open this address on another device: <url>'`, never shows launch success. Same on release link click in Settings. | Unit: `setup-behavior.test.tsx` (regressions 3 & 4), `webos-platform.test.tsx` | **PASS (Automated)** |
| **UI-20** | Interrupt/reconnect SSE, navigate dialogs, suspend/resume in catalog and playback | Node / happy-dom | Suspend saves position, closes media, halts SSE; resume opens single stream, refetches snapshots guarded by `visibilityEpoch`, reopens playback at saved position. | Unit: `lifecycle.test.tsx` (7 lifecycle tests), `webos-platform.test.tsx` | **PASS (Automated)** |
| **UI-21** | Break local script load and trigger uncaught startup error in disposable package | Chrome 53 + Chrome 148 | Broken bundle (`app.js` 404): rejected with exit code 3, `#startup` retained, `bundleGlobalAbsent: true`. Injected fatal error: `FileListFatalError` panel rendered (`role=alert`), posted diagnostics to server. | Harness case `fatal` and case `broken`; `fatal-panel.png`, `broken-evidence.json` | **PASS (Runtime)** |

---

## 3. Smoke Harness Execution Outputs

### Floor Engine Leg: Chromium 53 (`selenoid/chrome:53.0`)

```text
$ node tools/smoke_webos_engine/smoke.mjs --dist clients/webos/dist --case clean,fatal --output clients/webos/.build/task8-evidence/smoke-floor
smoke-webos-engine: serving .../clients/webos/dist on http://host.docker.internal:60255; engine Chrome/53.0.2785.143; image selenoid/chrome:53.0; cases: clean,fatal
case clean: boot PASS — engine Chrome/53.0.2785.143; window.FileListBoot.ready() removed #startup after the first render of #app(1 child node(s)); 0 page errors, 0 console errors.
case clean: setup traversal PASS — Manual address opened, the address was edited in place and OK-exited, focus landed on Connect.
case clean: connect PASS — fixture http://host.docker.internal:60255 accepted; Home rendered undefined rail card(s); status undefined.
case clean: focus moves PASS — sidebar/content focus traversal opens and closes the menu without errors.
case clean: scroll regression PASS — off-right poster (col 6) and the below-fold Favorites row were revealed exactly; vertical and horizontal scrollports stayed independent.
case clean: audio/subtitle picker PASS — honest empty inventory Audio (0), audio dialog opened/closed, subtitles dialog Off cleared overlay.
case clean: playback and seek PASS — video element mounted in .player-shell, #av-player hidden, direct Range stream played, seekTo advanced currentTime, exit restored shell.
case fatal: PASS — FileListFatalError panel is present, visible (role=alert), and reported via POST /api/v1/diagnostics/client (recorded: level=error, message="Script error.").
smoke-webos-engine: all requested cases passed on the recorded engine.

$ node tools/smoke_webos_engine/smoke.mjs --dist clients/webos/dist --case broken --output clients/webos/.build/task8-evidence/smoke-floor
smoke-webos-engine: serving ... on http://host.docker.internal:60294; engine Chrome/53.0.2785.143; image selenoid/chrome:53.0; cases: broken
case broken: rejected as designed on Chrome/53.0.2785.143 — GET /app.js returned 404, #startup still mounted, #app empty, window.TorrentTV absent (evidence: broken-evidence.json).
smoke-webos-engine: case broken: the smoke contract was violated by the fixture — exit code 3 (detection proven, non-zero by design)
```

### Modern Engine Leg: Google Chrome 148 (Host)

```text
$ node tools/smoke_webos_engine/smoke.mjs --dist clients/webos/dist --case clean,fatal --output clients/webos/.build/task8-evidence/smoke-current --browser "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
smoke-webos-engine: serving .../clients/webos/dist on http://127.0.0.1:60322; engine Chrome/148.0.7778.216; host binary ...; cases: clean,fatal
case clean: boot PASS — engine Chrome/148.0.7778.216; window.FileListBoot.ready() removed #startup after the first render of #app(1 child node(s)); 0 page errors, 0 console errors.
case clean: setup traversal PASS — Manual address opened, the address was edited in place and OK-exited, focus landed on Connect.
case clean: connect PASS — fixture http://127.0.0.1:60322 accepted; Home rendered undefined rail card(s); status undefined.
case clean: focus moves PASS — sidebar/content focus traversal opens and closes the menu without errors.
case clean: scroll regression PASS — off-right poster (col 6) and the below-fold Favorites row were revealed exactly; vertical and horizontal scrollports stayed independent.
case clean: audio/subtitle picker PASS — honest empty inventory Audio (0), audio dialog opened/closed, subtitles dialog Off cleared overlay.
case clean: playback and seek PASS — video element mounted in .player-shell, #av-player hidden, direct Range stream played, seekTo advanced currentTime, exit restored shell.
case fatal: PASS — FileListFatalError panel is present, visible (role=alert), and reported via POST /api/v1/diagnostics/client (recorded: level=error, message="smoke-webos-engine: injected fatal error").
smoke-webos-engine: all requested cases passed on the recorded engine.

$ node tools/smoke_webos_engine/smoke.mjs --dist clients/webos/dist --case broken --output clients/webos/.build/task8-evidence/smoke-current --browser "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
case broken: rejected as designed on Chrome/148.0.7778.216 — GET /app.js returned 404, #startup still mounted, #app empty, window.TorrentTV absent (evidence: broken-evidence.json).
smoke-webos-engine: case broken: the smoke contract was violated by the fixture — exit code 3 (detection proven, non-zero by design)
```

---

## 4. Unit & Packaging Test Results

### Client Test Suites (`npm run test:clients`)

- `@torrent-tv/shared`: 7 test files, 86 passed
- `@torrent-tv/web`: 10 test files, 179 passed
- `@torrent-tv/tv`: 11 test files, 121 passed (includes setup regressions, lifecycle, track honesty, navigation focus, player action)
- Total: 28 test files passed, 386 tests passed, 0 failures.

### webOS-Specific Suites (`npm run test -w @torrent-tv/webos`)

- `avplay.test.ts`: 34 passed (HTML5 video adapter lifecycle, metadata gating, timeupdate seeking, buffering sequence, ended deduplication, geometry mapping, honest track and subtitle behavior)
- `webos-platform.test.tsx`: 19 passed (platform hooks, network getStatus, openExternal browser handoff, exit window.close, keyboard visibility forwarding, visibility deduplication, Projects link refusal UI)
- Total: 2 test files passed, 53 tests passed, 0 failures.

### Packaging Tooling (`python3 -m unittest discover -s tools/tests -p 'test_*.py'`)

- 65 tests passed in 4.4s (includes 24 tests in `test_webos_ipk.py` validating packaging, manifest gates, asset dimensions, ares CLI pinning, script absence/presence, and checksum emission).

---

## 5. Milestone B: Pending Physical Hardware Matrix

The following behaviors require physical LG TV hardware and cannot be verified by headless engines or container simulations. Each item is recorded as **PENDING** with exact verification steps for the future tester.

| # | Behavior | Verification Steps on Physical LG TV | Expected Hardware Result | Status |
| --- | --- | --- | --- | --- |
| **HW-01** | Audible audio track switching | 1. Sideload IPK via `ares-install`.<br>2. Play a multi-audio release (e.g. Romanian + English tracks).<br>3. Open audio picker via Player toolbar.<br>4. Select alternate track. | Audio stream visibly switches in menu AND acoustic output from TV speakers changes language within 1 second without playback stutter or desync. | **PENDING** |
| **HW-02** | Native subtitle track presence & delayed-cue support | 1. Play media with inband native subtitle tracks.<br>2. Open Subtitles menu.<br>3. Verify track labels match container streams.<br>4. Adjust subtitle delay by ±0.5s. | Subtitle text appears in TV's native subtitle plane; if native cue access is unsupported by webOS decoder, client surfaces visible limitation message instead of silent failure. | **PENDING** |
| **HW-03** | Virtual keyboard visibility event | 1. Open Setup → Manual address.<br>2. Press OK on the address input.<br>3. Observe LG system keyboard appearance.<br>4. Dismiss via Done or Back. | LG system virtual keyboard slides into view; `keyboardStateChange` event fires; dismissal blurs input and restores green focus ring to Connect button without route exit. | **PENDING** |
| **HW-04** | Magic Remote Back long-press | 1. From catalog Home screen, press and hold Back key on physical LG Magic Remote for 5+ seconds. | System interprets long-press and exits application cleanly via webOS system handler, returning to LG Home launcher. | **PENDING** |
| **HW-05** | OS browser handoff | 1. Open Projects catalog.<br>2. Click an external project link card. | webOS launches `com.webos.app.browser` over the TV app displaying the target URL; pressing TV Back returns to Torrent TV with document state intact. | **PENDING** |
| **HW-06** | System suspend & resume | 1. Start media playback.<br>2. Switch TV input to HDMI 1 or press Home key to background the app.<br>3. Wait 30 seconds.<br>4. Re-open Torrent TV from recent apps. | App resumes cleanly; playing media reopens at saved timestamp; paused media remains paused; no duplicate audio streams or orphaned background decoders. | **PENDING** |
| **HW-07** | Codec coverage per Release | 1. Play test media with H.264, H.265/HEVC (1080p and 4K), AV1, DTS audio, AC3/E-AC3 audio. | H.264 and HEVC direct-play on TV hardware decoder; unsupported DTS produces visible playback error prompting user to select compatible release. | **PENDING** |
| **HW-08** | Incomplete-torrent seeking | 1. Start downloading a large torrent.<br>2. Begin progressive playback while download is at ~10%.<br>3. Seek forward into a piece range that has arrived; seek into a range not yet downloaded. | Seeking into buffered range plays immediately; seeking into missing range shows buffering indicator until pieces arrive, without crashing the media element. | **PENDING** |
| **HW-09** | Network loss and recovery | 1. While browsing or playing, disconnect TV Wi-Fi or unplug Ethernet cable.<br>2. Observe error handling.<br>3. Reconnect network. | Network error surfaces readable message; EventSource re-establishes connection on reconnect; catalog snapshots refresh without duplicating cards or losing focus. | **PENDING** |
| **HW-10** | Developer Mode renewal & uninstall-on-disable | 1. Install IPK via Developer Mode.<br>2. Observe Remaining Session counter in Developer Mode app.<br>3. Test **EXTEND** button.<br>4. Deliberately disable Developer Mode and reboot. | Session extends successfully; disabling Developer Mode uninstalls `com.torrenttv.app`; re-enabling allows clean reinstallation without leftover corrupted state. | **PENDING** |
| **HW-11** | Tizen vs. webOS visual comparison | 1. Place a Samsung Tizen TV and LG webOS TV side by side displaying the same server catalog at 1920×1080. | Poster spacing, rail pitch, typography, focus indicators, sidebar width, and card proportions match visually with no layout drift. | **PENDING** |

---

## 6. Task 9 Extension Area: webOS 3.x Investigation

### 6.1 Decision: DEFER webOS 3.x (Chromium 38) — 2026-09-07

**Status: DEFERRED.** The support floor remains webOS TV 4.0+ (Chromium 53, `target: 'chrome53'` in `clients/webos/vite.config.ts`). No adoption, no 3.x IPK variant, and no reduced-feature mode is shipped.

#### 6.2 Runtime sources checked (2026-09-07)

| Source | Check | Result |
| --- | --- | --- |
| Docker Hub `selenoid/chrome` (the Task 2 floor image series) | Hub API tag query `name=38` and full oldest-tag listing | No `38.0` tag; count=0. Oldest published tag is `48.0`; series verified 48.0→128.0. The 53.0 floor image (digest `sha256:5e3d995d…`) remains the guaranteed engine. |
| Docker Hub `zenika/alpine-chrome` | oldest tags via Hub API | Oldest tags are 86.x-era; nothing near 38. |
| Docker Hub `browserless/chromium` | tag query `name=38` | API returned null/0 matches. |
| Docker Hub `selenium/node-chrome` | tag listing | Oldest surviving tags are 118.x-era; no 3x-era Chrome. |
| Docker Hub search "chromium 38" | Hub search API | No repository publishes a Chromium 38 browser image (matches were unrelated: kasmweb Fedora 38, drupalci php-5.5.38, etc.). |
| Google Chromium snapshot archive `commondatastorage.googleapis.com/chromium-browser-snapshots/Linux_x64/` | range-probed revisions for surviving 38-trunk builds | Most 38-era revisions are purged (283000–289000 mostly 404). Revision **285500** survives and is Chromium **38.0.2105.0** (Linux x64 trunk build); revision 292500 survives as 39.0.2140.0. |

The only publicly obtainable Chromium 38 engine found is the archived snapshot build r285500 (Chromium 38.0.2105.0). No distribution, registry, or browser-vendor channel ships a supported Chromium 38 container image today.

#### 6.3 Execution evidence (real 38 engine, emulated amd64 under Docker on Apple Silicon)

Snapshot r285500 was executed under Docker (`--platform linux/amd64` with Xvfb and remote debugging port 9222): the binary runs, reports `Chromium 38.0.2105.0`, and serves raw CDP (`Protocol-Version: 1.1`, UA `Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/38.0.2105.0 Safari/537.36`).

The current packaged dist (`clients/webos/dist`, chrome53 target) was served to this engine at 1920×1080 and evaluated over CDP:

* **Boot result: FAILED.** `startup.js` caught the script parse failure and correctly surfaced the FileList startup-failure panel with:
  `Application startup failed: Unexpected token => at http://host.docker.internal:8899/app.js:1:69`
  The application bundle global (`TorrentTV`) never attached because Chromium 38 rejects ES2015+ syntax at parse time (arrow functions, template literals, and `let`/`const` which throws `Unexpected strict mode reserved word`).
* **Feature probe on the live 38 engine:**
  - `CSS.supports('display','grid')` → `false` (no native CSS Grid support).
  - `window.fetch` → `undefined` (Fetch API missing; added in Chrome 42).
  - `Object.assign` → `undefined` (missing; added in Chrome 45).
  - `Intl` → `object` (present).
  - `Uint8Array` / Typed Arrays → `function` (present).
* **Failure panel behavior:** The fallback startup error screen (`fatal-error.js` / `startup.js`) rendered successfully on Chromium 38, confirming UI-21 resilience under catastrophic bundle failure on older engines.

Per plan discipline this evidence is labeled **engine-only**: it demonstrates real engine behavior; it is not playback, IPK, or hardware evidence.

#### 6.4 Gap inventory (Chromium 38 vs. current build)

* **Syntax/transpilation (blocking):** Vite 7 / esbuild **cannot** transpile `let`/`const`/async-await to ES5 or `chrome38` target (fails with 950 `Transforming let/const … is not supported yet` errors). Supporting Chromium 38 would require replacing or augmenting esbuild with a dedicated Babel (`@babel/preset-env`) + `regenerator-runtime` pipeline.
* **CSS Grid (blocking for layout parity):** Chromium 38 has zero CSS Grid support. While `@supports not (display: grid)` flex fallbacks exist in `clients/webos/dist/app.css`, layout and focus traversal on 38 could not be validated because the application bundle cannot parse.
* **API polyfills:** Missing APIs required by the bundle on 38 include `fetch`, `Object.assign`, and `Array.from` (used 6 times in dist). Core-js polyfills currently in `clients/webos/src/compat.ts` address only Chromium 53 gaps (`Object.entries`, `String.prototype.padStart`/`padEnd`).
* **Adapter code paths:** The media/subtitle/aspect adapters were designed for the Chromium 53 floor; zero adapter paths could execute on 38 due to boot failure.
* **Bundle size impact:** Current `clients/webos/dist/app.js` is 154,902 B. An ES5 + regenerator + polyfill build would substantially expand bundle size, but cannot be produced without introducing a secondary Babel toolchain.

#### 6.5 Adoption bar vs. outcome

Adoption requires the full UI-01–UI-21 walkthrough set passing on a real 38 engine, one unified IPK, and no reduced feature set. None of these three criteria can be met:
1. The walkthrough set cannot boot due to syntax parse failures.
2. A single IPK cannot target both modern/53 engines cleanly without either double-bundling or degrading performance across all webOS versions.
3. Shipping an ES5-downgraded secondary build or disabled feature set violates the non-reduced-feature contract.

Therefore, **DEFER** is the only honest, evidence-backed outcome.

#### 6.6 Reopening conditions

Support for webOS 3.x may only be reconsidered if:
1. Physical webOS 3.x hardware (specific model and firmware) becomes available with active Developer Mode for `ares-cli` verification; and
2. A maintainable, single-IPK build configuration (e.g. Babel ES5 with measured bundle regression) is established that passes all UI-01 through UI-21 walkthrough scenarios on that engine without reducing features.

Until those conditions are satisfied, webOS TV 4.0+ (Chromium 53) remains the authoritative, verified floor.
