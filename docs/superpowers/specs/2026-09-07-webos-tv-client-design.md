# webOS TV client design

Date: 2026-09-07
Status: Design sections approved; written spec awaiting user review

## Decision

Add a Developer Mode sideload client for LG webOS TV using the existing application in `clients/tv`. Require the same features, design, branding, and interactions as the current Tizen client. Add LG integration and IPK packaging in `clients/webos`; do not fork screens or load the separate browser client in `web/`.

The required compatibility target is webOS 4.x (2018–2019) through webOS 26 (2026). Ship one IPK with no artificial upper-version restriction. Future versions are compatibility targets, not verified platforms. Investigate webOS 3.x (2016–2017) as an optional extension without weakening the required feature contract. webOS 1.x–2.x is outside this implementation scope.

No LG TV is available. The user approved complete implementation with automated and available runtime evidence while keeping hardware-verified parity as a separate acceptance milestone. No feature may be removed to make that distinction disappear.

## Goals and non-goals

Goals:
- Share all Tizen screens, styles, focus behavior, player controls, and server-backed features.
- Build one locally packaged application for the required version range.
- Use public LG APIs and platform-backed media playback.
- Deliver a versioned IPK and checksum through the existing release process, with manual installation and updates.
- Preserve Tizen and Android TV behavior and checks.

Non-goals:
- LG Content Store submission, automatic TV-app updates, rooted-TV installation, or private Luna media APIs.
- A separate LG theme, native UI rewrite, voice-search integration, or new Magic Remote interaction model.
- New server transcoding/remuxing or routing webOS through the browser Compatibility stream.
- Guaranteed decoding of every codec on every TV or claims about untested future firmware.
- Device installation, credential registration, deployment, or release publication during design work.

## Architecture and alternatives

`clients/tv` contains the Preact TV application, player controls, focus engine, discovery, platform helpers, and startup diagnostics. `clients/shared` contains API access, types, synchronization, and domain helpers. Tizen supplies AVPlay; Android supplies an AVPlay-shaped ExoPlayer bridge. The current TV Vite configuration builds a classic IIFE targeting `es2017`.

Chosen: shared TV source plus a webOS adapter and compatibility build. UI changes remain shared; platform behavior stays isolated.

Rejected alternatives:
- Forking the TV source would require copying every future feature and design fix.
- Loading the server browser client would deliver a different design and interaction model.

ADR-0006's feature-detection and one-package posture carries over. ADR-0008's manual TV updates and server-owned external integration remain unchanged. ADR-0009's byte-identity requirement remains intact between Tizen and Android. webOS uses the same source but may have different compiled bytes for Chromium 53. ADR-0009's rejection of generic Android HTML video does not rule out LG's documented `audioTracks` extension.

## Compatibility contract

LG documents these app engines:

| Platform | Model years | Engine |
| --- | --- | --- |
| webOS 3.x | 2016–2017 | Chromium 38 |
| webOS 4.x | 2018–2019 | Chromium 53 |
| webOS 5.x | 2020 | Chromium 68 |
| webOS 6.x | 2021 | Chromium 79 |
| webOS 22 | 2022 | Chromium 87 |
| webOS 23 | 2023 | Chromium 94 |
| webOS 24 | 2024 | Chromium 108 |
| webOS 25 | 2025 | Chromium 120 |
| webOS 26 | 2026 | Chromium 132 |

Build all executable assets, including the platform bootstrap and diagnostics, for Chromium 53. Use a classic launcher, not an ES-module dependency at startup. Syntax lowering alone is insufficient: check runtime APIs and CSS support and supply the compatibility behavior actually required. Keep compatibility logic outside route/component decisions. Do not introduce per-era packages.

Preserve the Tizen design at the same 1920×1080 reference viewport. CSS compatibility changes must preserve appearance and behavior on all TV clients. Shared CSS source is not evidence of identical rendering; compare rendered output.

Investigate Chromium 38 after establishing the required path. Adopt older coverage only with separately recorded evidence. If adopted, retain one IPK and the same features, not a reduced old-TV edition.

## Platform boundaries and data flow

The package boots local diagnostics, compatibility support, and LG integration before the shared application. Loading failures must leave a readable startup stage or Error panel.

The application obtains network information through the platform adapter, discovers or accepts a server URL, verifies it, and persists it through the existing saved-server flow. It uses `clients/shared` for the existing `/api/v1` API and SSE stream. Portal data and promotions remain server-owned. No upstream integration or TV account feature is added.

The adapter owns:
1. Playback: the AVPlay-shaped contract already consumed by the shared Player.
2. Discovery network information: acquire the active IPv4 address and subnet through supported LG APIs before starting the existing scan. Bridge asynchronous acquisition without fabricating synchronous values. Discovery failure leaves manual connection usable.
3. Remote and keyboard integration: map LG keys into existing logical actions, preserve D-pad focus, and avoid duplicate OK/Back events. Preserve read-only-until-OK editing, system keyboard ownership, and focus restoration.
4. Lifecycle and exit: release media on player exit/suspension, preserve position-save semantics where lifecycle timing permits, and reconcile state on return. Preserve application Back behavior where the platform delivers it; verify long-press behavior on hardware.
5. External links: use supported LG handoff through the existing helper contract. Failed handoff must not claim successful launch; retain visible URL behavior.

Keep platform decisions behind these boundaries, not inside screens or CSS. Reuse existing bridge hooks where their contracts fit. Magic Remote clicks use existing clickable controls; D-pad remains a complete navigation path.

## Playback design

Use a platform-backed HTML media element without native controls, beneath the shared controls and subtitle overlay. Create it only for playback and release it on close. Adapt the playback host within the platform boundary rather than treating the Samsung player object as an LG implementation. Do not introduce platform-specific player UI.

Map the existing open/prepare/play/pause/stop/close, seek, duration/time, track, display/aspect, and listener contracts into actual media behavior. Use metadata/readiness, playing/waiting, seek, time, ended, and error events. Convert milliseconds and seconds at the boundary. Cancel stale callbacks after close or source replacement and prevent duplicate completion events.

Audio selection uses the public `audioTracks` collection and `enabled` property documented by LG. Preserve real track identities and available language/codec metadata. Do not invent tracks or equate a successful property assignment with an audibly successful switch. Physical acceptance must verify the sound.

Keep server-prepared WebVTT Subtitle assets and the shared cue overlay as the primary subtitle path, including delay and Romanian-then-English preference. Map available native text tracks for the existing explicit fallback, avoiding duplicate native rendering when the overlay owns cues. A required fallback that cannot be implemented is a blocker, not a silently omitted feature.

Preserve the original Range-capable Direct play URL, existing Progressive playback recovery, and download-progress behavior. Do not add unrelated retry policies or report successful seeking before observing resulting media state. The existing ±10-second controls are seeks, not variable-speed playback.

Aspect modes retain their existing visible meanings through media sizing and layout beneath the same overlay. Playback information reports real available values, using existing unavailable-value behavior where necessary.

Required adapter methods must not be success-returning no-ops. If public APIs cannot satisfy a required behavior, report the concrete blocker and request a design change. A server-selected audio/remux fallback is not approved. Hardware codec differences follow the Tizen Direct play posture: choose a compatible Release rather than introduce conversion.

## Feasibility evidence and limits

LG staff document `audioTracks` on webOS 3.0 and higher, including selection by `enabled`. Jellyfin issue #4729 reports direct playback working on an LG C9 after allowing secondary audio on webOS 4. This supports the public-API path but does not prove switching on every required device.

LG forum reports describe unsuccessful switching in live HLS. These are not proof that the application's Direct play Range path fails. They demonstrate why enumeration and property state alone cannot establish actual switching.

LG documents HTTP playback, seeking, and resuming through `currentTime` or `mediaOption`. This supports the design, not a guarantee about incomplete torrent pieces, sparse media, buffering, or hardware decoding.

The investigation found no webOS CLI on `PATH` and no installed LG runtime in the searched locations. No LG playback probe ran. Modern macOS ARM64 simulators are available, but LG states that simulator audio/video specifications differ from devices and `mediaOption` is unsupported. Generic Chromium or simulator results cannot establish physical media parity.

## Parity acceptance inventory

Current `clients/tv` behavior is authoritative. Implementation planning must expand this inventory into explicit walkthrough scenarios and include current controls not individually named below. An unexplained difference is a defect, not an implicit exception.

| Surface | Required coverage |
| --- | --- |
| Setup | Discovery progress/results, rescan, manual address and system keyboard, connection failures, saved-server persistence, change/forget server |
| Home | Hero, overview and version action, Continue Watching progress, Favorites, recently added, promotion rotation and labels |
| Search | Explicit submit, clear, results, category filters, sort/paging, asynchronous completion refresh |
| Library and Tracker | Dashboards, categories/grids, favorites/watched/in-progress badges, canonical-title deduplication, resume positions |
| Title detail | Metadata, overview, resume, favorite/watched actions, seasons, expandable packs/episodes, source selection, download/pause/resume/retry, protected deletion, live refresh and focus restoration |
| Downloads | Search/filter/sort, play and transfer actions, protected deletion, stable keyed rows, telemetry and scroll anchoring |
| Player | Opening/buffering/progressive states, play/pause/stop/restart, ±10-second and timeline seeking, controls hide/reveal, audio menu/refresh/preferences, subtitles off/local/built-in/provider/native fallback, provider search, language preference chain, delay/reset, aspect modes, information, error/retry/recovery, position save and next episode |
| Jobs | Search/filters, paging, retries, detail logs, level/attempt filters, expansion and load older |
| Events | Coverage, Fetch latest, Rebuild catalog |
| Settings | Safe preferences, managed fields, dependency checks, server change/forget, server update check/apply confirmation, status and release links |
| Projects and promotions | Current Projects navigation/page, project links, clickable promotions, household donor visibility and upstream-failure behavior through server state |
| Global | D-pad/OK/media/Back, text-edit ownership, existing Magic Remote click targets, route/dialog focus restoration, SSE reconnect reconciliation, startup diagnostics, client error reporting, suspend/resume |

Branding follows Tizen. System keyboard appearance, OS link handoff, packaging metadata, and hardware codec capabilities differ by platform. These differences do not authorize changing shared screen design or removing features.

## Packaging, updates, and documentation

Add LG app metadata, launcher assets derived from the existing identity, and packaging under `clients/webos`. Keep a stable application ID for replacement updates and derive the package version from repository `VERSION`. Produce `torrent-tv-<VERSION>-webos.ipk` plus a SHA-256 checksum and include the artifact in the existing release checksum/publication flow. Validate LG metadata rather than copying Samsung manifest fields.

Use pinned build dependencies and a reproducible packaging command integrated with existing Makefile and client CI conventions. Validate metadata/version, referenced launch assets, boot order, and packaged compatibility output. Preserve Tizen/Android same-bundle checks.

Document Developer Mode setup, pairing, install, replacement update, and session renewal. LG warns that disabling Developer Mode uninstalls apps installed through it, including expiry followed by reboot. Do not describe sideloading as permanent store installation. Credentials and pairing keys remain outside the repository. TV mutations require separate authorization.

Implementation includes developer/install/release documentation, a glossary update and ADR recording the webOS decision, and a webOS verification log beginning with no named hardware. This design commit adds only this spec; it does not prematurely change implementation documentation.

## Verification and completion gates

1. Compile and exercise packaged UI output in an appropriate Chromium 53-era runtime and a current runtime. Check APIs, CSS, inputs, and focus, not just syntax. Missing runtimes are verification prerequisites, not passing checks.
2. Compare Tizen and webOS screenshots and interaction walkthroughs with the same viewport, server data, and state. Cover the inventory, including loading/error/empty states, dialogs, and focus restoration.
3. Validate the IPK and boot it in an available LG runtime. Simulator claims cover only exercised behavior.
4. Run existing shared/Tizen/Android checks after shared changes. Keep durable tests for meaningful adapter risks such as lifecycle ordering, stale events, failures, track identity, and key ownership; prove the feature through runtime exercises.
5. Record physical results by named LG model, firmware, app version, media/container/codecs, steps, and outcome. Cover multiple audio tracks, subtitles, aspect/video layering, completed/incomplete Sources, seek/resume, recovery, network loss, and suspend/resume. Prefer an old required-generation TV and a current TV; neither proves all intervening firmware.
6. Investigate optional webOS 3.x coverage separately and publish only obtained evidence. Future platforms remain unverified until exercised.

Milestone A is a complete implementation with package, automated, and available UI-runtime evidence. Milestone B is hardware-verified parity. The user approved separating these because no LG TV is available. Milestone A must not be described as verified full playback parity. Pending checks remain visible; discovered failures must be fixed or brought back for explicit approval. The split does not authorize omissions.

## References

Repository evidence: `clients/tv/vite.config.ts`; `clients/tv/src/main.tsx`, `navigation.ts`, `player.ts`, `platform.ts`, `discovery.ts`; `clients/shared`; `docs/TIZEN.md`; `docs/ANDROIDTV.md`; ADR-0006, ADR-0008, ADR-0009.

- [LG app engines](https://webostv.developer.lge.com/develop/specifications/web-api-and-web-engine)
- [LG audioTracks guidance](https://forum.webostv.developer.lge.com/t/video-multi-audio/24156)
- [LG live-HLS audio report](https://forum.webostv.developer.lge.com/t/channel-multi-audio-selection/24666)
- [Jellyfin webOS 4 report](https://github.com/jellyfin/jellyfin-web/issues/4729)
- [LG protocol and seek support](https://webostv.developer.lge.com/develop/specifications/streaming-protocol-drm)
- [LG resume guidance](https://webostv.developer.lge.com/develop/guides/resuming-media-with-mediaoption)
- [LG simulator limitations](https://webostv.developer.lge.com/develop/tools/simulator-introduction)
- [LG Developer Mode lifecycle](https://webostv.developer.lge.com/develop/getting-started/developer-mode-app)

## Review record

The user approved the shared-source architecture, required 2018+ scope with older coverage if feasible, Developer Mode sideloading, public-API playback with hardware verification pending, and packaging/acceptance requirements. The user authorized writing, self-reviewing, and committing this document alone. Implementation planning begins only after review of the written spec.
