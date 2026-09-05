# Samsung Tizen client

One Tizen client serves the whole supported span: a single WGT declares the Support floor `required_version="5.0"` — a pure floor with no ceiling — and the same package installs and runs on every Samsung platform from Tizen 5.0 through the latest (see `docs/adr/0006` for the decision). Behavior counts as confirmed only on the household's two Verified TVs, the 2019 premium Tizen 5.0 set and the 2023 S90C (Tizen 7.0); every other Tizen 5.0-or-newer set is best-effort. The app uses AVPlay, not HTML video or `ffmpeg.wasm`, so compatible 4K content stays on the TV hardware-decoding path. Samsung TV sets since 2018 decode no DTS-class audio, so DTS-only sources are unsupported, and AV1 decoding needs a 2021-or-newer set — on an older TV, choose another release.

## Build an Apps2Samsung WGT

The normal frontend build produces the browser assets, the TV assets, and an Apps2Samsung-ready WGT:

```sh
make frontend
```

The build uses the pinned Node 24 Docker image and an ephemeral container volume for `node_modules`, so Node and frontend dependencies do not need to be installed on the host. Packaging uses only Python 3 standard-library modules. It creates:

```text
clients/tizen/.build/artifacts/torrent-tv-0.3.0.wgt
clients/tizen/.build/artifacts/torrent-tv-0.3.0.wgt.sha256
```

The WGT is deliberately unsigned. Apps2Samsung accepts a custom `.wgt`, obtains or reuses a Samsung certificate, and re-signs the package for the selected TV during installation. Signature files from an old build are excluded to prevent a mismatched certificate from leaking into the artifact. No certificate, password, or TV DUID is stored in this repository.

To rebuild only the WGT from an existing `clients/tv/dist`, or validate an existing generated package:

```sh
make tizen-wgt
make validate-tizen-wgt
```

The offline validator checks that the file is a safe ZIP/WGT, all manifest and HTML assets exist, the widget/application/package versions are valid, the Samsung `tv-samsung` profile is selected, the required network, download, and remote-control privileges are present, wildcard WARP access is enabled, the selected target meets `required_version`, and no known partner-only privilege prevents ordinary Apps2Samsung signing. It also rejects ES-module launchers, requires the Samsung WebAPIS and classic app boot scripts, verifies the playback-only AVPlay lifecycle, and checks that `icon.png` is exactly 117×117 pixels. The manifest declares the Support floor `required_version="5.0"`; the validator checks the selected target against that floor, and CI validates the same WGT against both targets — the default `TIZEN_TARGET=7.0` and the floor `TIZEN_TARGET=5.0`.

Offline validation cannot prove certificate/DUID acceptance or runtime behavior. Installation, launch, network access, remote keys, suspend/resume, and AVPlay must still be tested on the physical TV.

## Install with Apps2Samsung

1. Put the computer and TV on the same LAN. The TV must be reachable on its internal development port (26101); that port cannot be enabled independently.
2. On the TV, open **Apps**, then **App Settings**, enter `12345`, enable **Developer mode**, enter the IP address of the computer running Apps2Samsung, and reboot the TV. If that computer's IP changes, update this setting.
3. Start Apps2Samsung and select the TV. Choose the custom WGT option and select `clients/tizen/.build/artifacts/torrent-tv-0.3.0.wgt` from disk.
4. Install it. Apps2Samsung can request a Samsung account login the first time it needs to create signing material; later installs reuse the certificate. Do not copy that signing material into the repository.
5. Launch **Torrent TV**, select the discovered server or choose **Manual address**, and exercise the physical-TV checks above.

If Apps2Samsung cannot discover or connect to the TV, verify Developer Mode after reboot, the authorized computer IP, same-LAN routing and firewall rules, and the TV's IP. A Samsung `1010` installation error commonly indicates a signing/certificate problem; retry Apps2Samsung certificate setup before changing the WGT.

## Startup compatibility and diagnostics

Version 0.1.0 used Vite's default `<script type="module" crossorigin>` launcher and could install successfully but show only the dark application background. Version 0.1.1 packages the client as a single ES2017-compatible classic IIFE. This avoids relying on Samsung's partial module-object support and follows Samsung's documented classic-script loading pattern for `$WEBAPIS/webapis/webapis.js`.

Version 0.1.2 fixes a second black-screen cause: the native `application/avplayer` surface previously existed from startup and was sized to the full screen, allowing its black video plane to cover even the setup and diagnostic UI. The object is now created only as part of the mounted playback view, with controls explicitly layered above it, and is stopped and closed independently when playback exits. This follows Samsung's documented create/append-before-open lifecycle and the hidden-player-scene/layering patterns used by working community TV applications. The validator rejects any future package that puts an AVPlay object back into startup HTML.

Version 0.1.3 adds the missing TV focus system. Connect receives initial focus, Left focuses the address field and opens Samsung IME, IME Done/Cancel returns to Connect, and D-pad/OK navigate every current catalog and player control. The focus engine selects the closest aligned control, scrolls it into view, remembers stable control identities, and restores the originating card after playback. Back closes IME or playback before using the normal application exit behavior.

Version 0.1.4 replaces the one-button playback view with a Smart Remote player overlay. It has a filename, current/duration time, progress bar, 10-second seeking, restart, play/pause, audio and embedded-subtitle selection, Romanian-then-English subtitle preference, subtitle delay, aspect modes, playback information, and a five-second auto-hide timer. Media Play/Pause, Stop, Rewind, Fast Forward, Previous, and Next are registered independently so one unsupported optional key cannot disable the rest. When AVPlay rejects an incomplete source, it polls the server-owned download and performs one automatic reopen after completion.

Version 0.1.5 introduced downloadable subtitles through AVPlay's external SAMI path. Version 0.2.0 replaces that unreliable attachment route: the Go server returns WebVTT, the client parses bounded text cues, and an application overlay renders the active cue from AVPlay's current time. This needs no TV filesystem download or player reopen. The menu retains Off, embedded tracks, Romanian→English provider search, descriptive candidate labels, and subtitle delay.

Version 0.2.0 introduces the Plex-first library shell shared with the browser: a compositor-backed left rail, Home/My Library/Tracker sections, canonical artwork cards, title → season → episode → source selection, bounded 12-card catalog pages, managed-download actions, searchable/paginated jobs, safe TV Settings, My Library categories, and manual Events. Current catalog pages are route-owned and metadata events patch matching cards instead of replacing or reordering the page. Search contacts FileList only after the focused Search button is pressed, returns cache matches immediately, and automatically refreshes when the persistent tracker job publishes completion. Inputs remain read-only during D-pad traversal; OK explicitly enters edit mode and opens the keyboard. Its focus graph uses explicit rows and columns instead of visual guesswork. Hidden-player Left/Right reveals and focuses a stateful timeline; repeated presses update a preview and commit after a short pause without losing focus. AVPlay track lists refresh after buffering and from the audio menu. The event stream closes and reconnects with bounded exponential backoff rather than remaining stuck. Short Back toggles the main rail; a five-second hold exits the app.

Version 0.2.2 retains the website-authoritative datasets from 0.2.1 and adds server-selected completed/progressive playback. AVPlay retries incomplete streams as pieces become readable instead of waiting for full completion. Downloads exposes live search/filter/sort state and one D-pad-safe protected **Delete download** action that removes qBittorrent state and files. Whole-season packs appear as individually playable episode rows. Series detail resumes the unfinished episode, completion advances to the next cached episode, and per-file English-audio/Romanian-then-English subtitle preferences are shared with the browser.

Version 0.2.4 adds a complete spatial-focus audit: every TV button and input has a stable region, row, column, and key. Player toolbar Left/Right follows the physical row, Up reaches the timeline, timeline Left/Right seeks, Down restores the remembered toolbar control, and vertical dialogs ignore Left/Right. Setup, protected deletion, catalog pages, series details, and player dialogs all restore a predictable launcher. Tizen AVPlay remains on the original Range stream and never uses the browser's audio-only compatibility output.

Version 0.2.5 hardens the canonical resume action. A partially watched movie or series replaces Play with Resume, series labels identify the exact `SxxExx`, and the secondary line shows the saved position. Matching accepts either canonical title identity carried directly by household history or its embedded catalog title. Play and Resume share one stable row/column/key, so the Smart Remote focus graph does not change when playback state changes.

Version 0.2.6 groups every household dashboard rail to one card per canonical series title and makes complete-season release alternatives explicit. Each pack card owns its exact download state; a completed version is marked without disabling other releases. The detail focus graph fixes season tabs on row 2, pack alternatives on row 3, and episodes on row 4 and below, so Down follows the screen and Left/Right selects another pack. Series detail polls cache-only state in place while the pack is registered.

Version 0.2.7 makes complete-season cards safe disclosures: OK expands one card at a time, and only the inner Download button starts work. Active, paused, failed, and completed packs expose the relevant Pause, Resume, Retry, and protected Delete controls. The detail graph uses seasons on row 2, pack headers on row 3, expanded actions on row 4, and episodes from row 10, so every D-pad direction follows the visible layout. First launch now scans a bounded local subnet for validated Torrent TV servers; a discovered URL is normalized, verified, saved, and reused exactly like a successful manual connection. Rescan, Manual address, and Forget saved server remain available.

Current canonical navigation opens every My Library and Tracker card in title details. Episode cards expand version actions; unmanaged versions say **Play and download**, while owned versions say **Play**. Season and episode controls expose separate download/watch markers. Back collapses an expanded episode before leaving details. Starting a season pack keeps details open and updates its episode rows.

Downloads telemetry is reconciled by stable download ID. Polling never invokes focus restoration or replaces existing keyed controls, existing rows retain their order, and the focused action remains attached to the same DOM node. Telemetry reserves stable lines; longer selected-file and complete-torrent facts expand instead of being clipped. New rows are allowed and the first visible row is anchored when they are inserted above it.

The HTML now paints **Torrent TV — Starting application…** before JavaScript runs. A successful Preact render removes it. If the bundle is missing, slow, or throws during startup, the screen remains visible and displays the failed stage or exception instead of becoming an unexplained black screen. Report the exact on-screen message when diagnosing a physical-TV failure.

The packaged launcher icon is `icon.png`, a 117×117 RGBA rendering of the repository's editable `icon.svg` source. Samsung identifies this size as the test-installed TV application icon. If the updated icon does not appear immediately after installing 0.1.3, reboot the TV once to clear the launcher cache.

The latest 0.2.0 subtitle repair prefers the reliable server path: completed downloads are searched for torrent-contained and FFprobe-discovered embedded tracks, the chosen stream is extracted to WebVTT, and the TV renders its cues in the application overlay. Automatic search uses the local-only API scope and never contacts SubDL. Samsung native TEXT tracks retain parsed labels as an explicit fallback rather than being auto-selected. Jobs provide D-pad-accessible filters; every detail log row is now a real button whose OK action expands or collapses IDs and structured context.

## Physical-TV verification log

This is the durable source of truth for device results. **Confirmed** means observed on a Verified TV — the household's 2019 premium Tizen 5.0 set or the 2023 S90C; a successful build or offline validation alone is recorded as **Pending TV test**. Results are recorded per TV generation: a result on one Verified TV never transfers to the other until directly observed there.

### 2023 S90C (Tizen 7.0)

| Version | Behavior | Status | Evidence |
| --- | --- | --- | --- |
| 0.1.2 | Install an unsigned custom WGT through Apps2Samsung | Confirmed | User-tested on the S90C on 2026-08-02 |
| 0.1.2 | Show the updated launcher icon | Confirmed | User-tested on the S90C |
| 0.1.2 | Cold-launch into the visible server setup screen without a black overlay | Confirmed | User-tested on the S90C on 2026-08-02 |
| 0.1.2 | Show the expected prefilled Raspberry Pi server address and Connect button | Confirmed | User-tested on the S90C |
| 0.1.2 | Navigate setup with the TV remote | Failed | No control received focus and D-pad/OK gave no feedback |
| 0.1.3 | Initial Connect focus, address entry, D-pad/OK, and catalog navigation | Confirmed | User-tested on the S90C on 2026-08-02; focus order works but needs UX refinement |
| 0.1.3 | AVPlay playback after the torrent completes | Confirmed | User reported successful, good-quality playback after reopening the completed movie |
| 0.1.3 | Seek/time overlay and playback remote controls | Failed | No time bar and the remote could not seek during playback |
| 0.1.5 | Player overlay, remote seeking, track/options menus, and auto-hide | Pending TV test | Tizen unit/compiler build passes; requires installation on the S90C |
| 0.1.5 | Failed-progressive-playback automatic retry after completion | Pending TV test | Bounded recovery implemented; progressive playback itself remains a known issue |
| 0.1.5 | Automatic/manual contained and downloadable subtitles | Pending TV test | Server and client tests pass; provider credentials, TV private download, and AVPlay attachment require end-to-end testing |
| 0.2.0 | Plex-first catalog, canonical hierarchy, sidebar handoff, deterministic vertical focus | Pending TV test | 15 TV unit tests passed in the full Docker build; final TypeScript, Vite, WGT packaging, and offline validation pass |
| 0.2.0 | Hidden/timeline D-pad seeking and remembered toolbar/dialog focus | Pending TV test | Implemented and compiler-tested; install on the S90C to confirm AVPlay behavior |
| 0.2.0 | Stable pages, OK-only input editing, Events focus, live search, metadata patching, library categories, and paginated jobs | Pending TV test | Browser/Tizen compilers and Go API suite pass; requires target-TV interaction test |
| 0.2.0 | Named downloadable subtitle candidates and provider-warning diagnostics | Pending TV test | Browser/Tizen build passes; requires saved credentials and application WebVTT overlay test |
| 0.2.0 | Compositor rail, reduced card work, and 12-card TV payload | Pending TV test | Performance CSS and bounded catalog implemented; actual frame rate must be observed on the S90C |
| 0.2.0 | Apps2Samsung WGT after catalog/subtitle stability repair | Pending TV test | 16 TV tests, TypeScript/Vite build, packaging, and offline validation pass; SHA-256 `04b711c119294b6695693b140159b7787becd1724eb7f9f3b6c98474e40ebdaf` |
| 0.2.0 | Embedded subtitle, rating, and Jobs-filter build | Pending TV test | 16 TV tests, browser/Tizen TypeScript and Vite builds, package validation pass; SHA-256 `0973eb2f8da73bec0a6fa4071ff2ded595ea451cdc9473c4740ee386fddddb09` |
| 0.2.0 | Cache-only server latency and SSE cold-connect behavior used by TV | Server confirmed | Deployed Pi returned title pages in 78 ms, household state in 11 ms, and zero cold replay events in a two-second observation; physical TV reconnect still pending |
| 0.2.0 | New streaming-node launcher icon | Pending TV test | 117×117 RGBA validation and WGT packaging pass; launcher cache/display requires install |
| 0.2.0 | Server-extracted contained/embedded WebVTT preference | Pending TV test | Backend scope tests, 16 TV tests, TypeScript/Vite build, and WGT validation pass; native AVPlay TEXT remains explicit fallback |
| 0.2.0 | OK-expandable Job log rows | Pending TV test | Rows are structured D-pad buttons with stable focus keys and an expanded context panel; compiler/WGT validation pass |
| 0.2.0 | First public-release candidate | Pending TV test | Unsigned Apps2Samsung WGT SHA-256 `a4d3d6c72d6242020279a0036f1a8d7bde7d575bebd446cd44adede285764adc` |
| 0.2.1 | Full cache facets, website-equivalent household data, and offline managed playback | Server confirmed; pending TV test | Pi smoke test exposed 20 facet categories versus 5 on the startup page and reused an existing download in 2 ms; 19 Tizen tests passed; unsigned WGT SHA-256 `744967059d1536b77e8109aa064e7b9d3008663d27928876093d6c68edb7c0c7` |
| 0.2.2 | Progressive qBittorrent Range playback and unified deletion | Server confirmed; pending TV test | At 3.387% completion, Pi returned startup and tail HTTP 206 ranges and `ffprobe` parsed the progressive Matroska stream; qB global download limit remained unlimited; 19 Tizen tests and WGT validation passed; unsigned SHA-256 `c028421d17b294f78f5cf1c5480f0d06eed94e04f7fae3a1b270ad11308199c2` |
| 0.2.2 | Live Downloads controls, season episodes, auto-next/resume, and saved audio/subtitle preferences | Pending TV test | Browser/Tizen production builds and 19 Tizen tests passed in the pinned Raspberry Pi container; offline WGT validation passed; unsigned SHA-256 `aeb48bef082fc9fb4aa7e715fe21b57e20982bd02f551a137ed9cc8343e4857e` |
| 0.2.3 | Canonical series navigation, stable Downloads, deduplicated categories, state markers, and filename-safe actions | Pending TV test | Browser/Tizen production builds and all 19 spatial/player/catalog Tizen tests passed in the pinned Docker build; offline validation passed; unsigned SHA-256 `5d32cc42050e8ddb85f73b17a9925c9ea06395219e63498f3c391c8d60793907` |
| 0.2.4 | Stable browser timeline/audio selection, unclipped Downloads facts, and complete spatial player focus | Pending TV test | Browser/Tizen production builds and all 23 spatial/player/catalog Tizen tests passed in the pinned Docker build; offline validation passed; unsigned SHA-256 `55313bbf797c27b2d16d1a5731bd10b4662783e33cd98b31179c1df6e40d1a01` |
| 0.2.5 | Canonical title-level Resume action and stable primary-action focus | Server confirmed; pending TV test | Deployed Pi reports 0.2.5; browser/Tizen production builds, 24 Tizen tests, shared selector tests, and offline validation passed; unsigned SHA-256 `0fe2436f79d2c9331151efacd8a68e1a20079586fa574b5bc782d3a4a1105e8c` |
| 0.2.6 | Exact season-pack state, selectable alternatives, canonical dashboard grouping, and deterministic pack-row focus | Server confirmed; pending TV test | Pi reports 0.2.6; live Silo data marks the exact 1080p pack downloaded, another pack partial, and alternatives untouched; household sections contain no repeated title IDs. Browser/Tizen production builds, 26 Tizen tests, and WGT validation passed; unsigned SHA-256 `522322e24bc1350162fb03cb7a80af39f19fbbb21d9933e079f604f2c926a6e7` |
| 0.2.7 | Safe season-pack disclosures, explicit lifecycle controls, environment-managed Settings, and persisted LAN discovery | Automated checks passed; pending Pi/TV confirmation | Browser/Tizen production builds, 29 Tizen tests, Docker integration verification, WGT packaging, and offline validation passed; unsigned SHA-256 `41166a397d76530222013a1c0fd5c51db6d3a7462ffb992a88ba38bfa76081a3` |

After installing 0.3.0, verify in order:

1. Connect has an obvious green focus ring immediately after launch; OK connects using the prefilled address.
2. Before connecting, focus the address without opening Samsung IME; press OK to enter edit mode, edit text, choose Done, and confirm D-pad navigation resumes.
3. D-pad traverses header buttons, media cards, favorite/watched actions, and multiple rails without losing focus; off-screen targets scroll into view.
   On a partially watched series, confirm the primary action reads **Resume SxxExx**, Down from Back reaches it, Right reaches Favorite, Left returns, and OK resumes the displayed episode at the displayed position.
   On a series with complete-season sources, move Down from a season tab to the closest pack version and use Left/Right across alternatives. OK must only expand the pack. Down reaches its Pause/Resume/Download and Delete controls; another Down reaches the first episode. Confirm the downloaded/downloading marker belongs only to the chosen release and another release remains selectable.
4. OK invokes only the focused action once. Connection errors remain visible and leave Connect usable.
5. Start playback, use Back to return, and confirm the exact originating card regains focus. Back while editing closes IME first. On the main shell short Back opens/closes the rail; holding it for five seconds exits.
6. During playback, navigate Right to the toolbar **Hide** button and OK it; the controls must disappear immediately. With the controls hidden, any recognized remote key must restore them first: Left/Right reveal the timeline and seek 10 seconds, OK/Up/Down reveal the remembered toolbar control, media keys work, and Back first closes an open menu then exits playback.
7. Verify Restart, repeated ±10 and timeline seeks do not lose focus; then verify audio, embedded/downloadable subtitle Off/selection, Romanian→English preference, subtitle delay, aspect modes, and playback information.
8. With an incomplete torrent, confirm AVPlay begins once startup pieces are readable, displays live progress during any short retry, and supports a seek after the requested pieces arrive without waiting for 100% completion.
9. Configure and test SubDL in the browser, select **Find downloadable subtitles**, confirm results have real language/format/release labels, select one, and verify WebVTT text renders at the correct time without restarting AVPlay.
10. Open Settings, save safe preferences, run Events, inspect/retry a completed Job, and verify live tracker search returns results outside the first screen.
11. Confirm Home, My Library, Continue Watching, Favorites, Watched, and Tracker dashboards show one Silo card rather than one card per episode; selecting it opens the complete Silo hierarchy. Confirm Downloads still shows individual managed episode files.
12. Update this table immediately with the tested WGT version, result, date, and any observed limitation.

### 2019 premium (Tizen 5.0)

The 2019 premium set is where the 0.3.0 Support-floor failure was first observed: after installing 0.3.0, the launch and catalog render died on the TV's old Chromium 63 engine while the S90C (Chromium 94) ran the same package without issue. The root cause and the fix are recorded below; the fix rows stay **Pending TV test** until re-observed on this set.

| Version | Behavior | Status | Evidence |
| --- | --- | --- | --- |
| 0.3.0 | Launch into Connect and render the catalog after discovery | Failed (Confirmed) | Observed by the household after installing 0.3.0: discovery scan and catalog render both died with an unhandled error — the shipped code called `Array.prototype.flatMap` (ES2019, Chromium 69+), which the Tizen 5.0 engine (Chromium 63) lacks. Latent since the LAN discovery feature shipped; never executed on an old engine until 0.3.0 dropped the Support floor to 5.0. The S90C (Chromium 94) was unaffected |
| 0.3.0 | Discovery scan and catalog render after replacing both `flatMap` call sites (discovery target construction and the catalog heading lookup) with ES5-safe equivalents | Pending TV test | Fix implemented and unit-verified; requires reinstall and observation on this 2019 set |
| 0.3.0 | Error panel on unhandled errors: plain-language guidance instead of silence, with the failure reported to the server's client-diagnostics channel | Pending TV test | Implemented; requires observation on this 2019 set |

## Optional manual Tizen CLI signing

Apps2Samsung users do not need this path. If Tizen Studio and the Samsung TV extensions already exist, `clients/tizen/scripts/package.sh` accepts an existing certificate profile in `TIZEN_PROFILE` and creates `torrent-tv-0.3.0-signed.wgt`. This separate filename prevents it from overwriting the unsigned Apps2Samsung artifact. The script never creates certificate material.

## Device behavior

When no server is saved, first launch uses Samsung's network API to scan at most the television's local `/24` for `/api/v1/system/info` on port `8097` and any manually retained port. Results show instance name, URL, version, and setup state. Selecting a result runs the normal connection verification; only success writes the normalized URL to Tizen application storage. Manual address supports arbitrary hostnames and ports, failed attempts do not replace the saved server, and Rescan/Change Server/Forget saved server remain available without credentials.

Arrow, Enter, and Back are mandatory Samsung remote keys and do not require registration. The client handles their DOM key events with spatial focus navigation. Only the optional media keys are registered through `tvinputdevice`. Samsung IME owns key input while the address field is focused; its Done and Cancel events blur the field and restore Connect focus without suppressing text-editing defaults.

AVPlay receives the server's Range-capable URL. The control overlay follows Samsung's five-second auto-hide guidance and gives D-pad and media keys playback-specific meanings rather than using catalog spatial navigation. The player closes AVPlay on exit and restores catalog focus. The package retains the Tizen Download privilege for compatibility with 0.1.5 artifacts, but current external subtitles are fetched as WebVTT over the trusted LAN and rendered in the application overlay.

## Approval gate

Connecting SDB, creating/registering certificates or DUIDs, installing a WGT, and altering TV settings are device mutations. Perform them only after explicit approval. Building and validating the local unsigned WGT does not touch the TV. Public Apps2Samsung publication is not in scope.

## References

- [Apps2Samsung repository and custom WGT workflow](https://github.com/Apps2Samsung/Apps2Samsung)
- [Apps2Samsung community Tizen packages](https://github.com/Apps2Samsung/tizen-community-packages)
- [SmartTV Twitch working Tizen application](https://github.com/fgl27/smarttv-twitch)
- [Samsung: connecting a TV and enabling Developer Mode](https://developer.samsung.com/smarttv/develop/getting-started/using-sdk/tv-device.html)
- [Samsung: configuring TV applications](https://developer.samsung.com/smarttv/develop/guides/fundamentals/configuring-tv-applications.html)
- [Samsung: TV web engine versions and JavaScript support](https://developer.samsung.com/smarttv/develop/specifications/web-engine-specifications.html)
- [Samsung: Product API script loading](https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references.html)
- [Samsung: remote control handling](https://developer.samsung.com/smarttv/develop/guides/user-interaction/remote-control.html)
- [Samsung: keyboard and IME handling](https://developer.samsung.com/smarttv/develop/guides/user-interaction/keyboardime.html)
- [Samsung: TV focus and navigation design](https://developer.samsung.com/smarttv/design/design-principles.html)
- [Samsung: playback using AVPlay](https://developer.samsung.com/smarttv/develop/guides/multimedia/media-playback/using-avplay.html)
- [Samsung: media player interaction design](https://developer.samsung.com/smarttv/design/media-player.html)
- [Samsung: AVPlay API and track/subtitle methods](https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/avplay-api.html)
- [Samsung: subtitle formats and external subtitle paths](https://developer.samsung.com/smarttv/develop/guides/multimedia/subtitles.html)
- [Samsung: sideloaded test application icon size](https://developer.samsung.com/smarttv/design/smart-tv-application-design-qa.html)
