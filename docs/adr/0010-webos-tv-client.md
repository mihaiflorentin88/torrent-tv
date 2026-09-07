# A third TV client puts Torrent TV on LG webOS 4.0 through the latest platform

---
status: accepted
---

The household wants the TV client on LG webOS TVs from 2018 onward. Following
the Tizen client (ADR-0006) and the Android TV client (ADR-0009), the webOS
client is not a separate application: it shares the identical TV client codebase
(`clients/tv/src`) and runs as a web application inside LG's webOS application
runtime. One IPK (`com.torrenttv.app`) targets the webOS 4.x engine floor
(Chromium 53) and serves every newer webOS platform with no ceiling, maintaining
the single-package posture established in ADR-0006.

Media playback reuses the existing AVPlay-shaped player seam via an HTML5
video adapter (`clients/webos/src/avplay.ts`) that drives standard HTML5
`<video>` elements over direct HTTP Range requests (ADR-0001). This eliminates
custom transcode pipelines or Samsung-specific binaries while preserving
the shared Player component byte-for-byte. Track and subtitle selection follow
an honest contract: audio and subtitle menus report only actual available
streams, and native cue operations unsupported on the engine fail visibly
rather than faking success.

Platform integration delegates to the official LG webOSTV.js SDK (vendored ES5
release 1.2.13): network status queries `webOSDev.connection.getStatus`,
external links hand off to LG's browser (`com.webos.app.browser`) via
`webOSDev.launch`, application exit calls `window.close()`, remote Back keycode
`461` maps to the shared navigation Back action, and the webOS virtual keyboard
integrates through `keyboardStateChange` events. Updates remain manual sideloads
from repository releases via the official `@webos-tools/cli` 3.2.5 packaging
tools and LG's Developer Mode app, matching the manual update posture of
ADR-0008 and ADR-0009.

Support for webOS TV 3.x (Chromium 38) was investigated in Task 9 and is
explicitly deferred (see docs/WEBOS-VERIFICATION.md §6): Chromium 38 fails to
parse ES2015+ syntax (rejecting arrow functions, template strings, and let/const),
and supporting it would require a dedicated Babel pipeline and separate package,
violating the single-IPK, non-reduced-feature design.

## Considered options

- **Separate React / Enact UI for webOS** — rejected: re-implements the entire interface, diverges from the Tizen and Android design by construction, and creates three independent UI codebases to maintain for zero user benefit.
- **Transcoded HLS / DASH stream pipeline** — rejected: violates ADR-0001 (the server never transcodes video); webOS HTML5 `<video>` handles direct HTTP Range streams natively.
- **Blanket browser shims for older engines** — rejected: blanket polyfills inflate the bundle and mask runtime failures; layout uses capability-detected `@supports not (display: grid)` CSS fallbacks and behavioral `scrollIntoView` detection without user-agent sniffing.
- **Include webOS 3.x in initial support floor** — deferred following Task 9 investigation: Chromium 38 lacks modern JavaScript syntax and standard CSS Grid; support cannot be achieved under a unified single-IPK build without compromising the verified Chromium 53 floor.
- **LG Content Store / auto-update channel** — rejected: Torrent TV is a private-network application; sideloading via LG's official Developer Mode app avoids store submission overhead, account dependencies, and rooting requirements.

## Consequences

- **Code parity**: the Parity contract binds webOS alongside Tizen and Android TV. The TV application code (`clients/tv/src`) remains single-source; CI enforces that building the webOS package leaves shared bundle bytes untouched.
- **Engine floor**: Chromium 53 is the validated floor engine. Features requiring newer APIs (CSS Grid, modern scroll options, ES2017+) activate standards-compliant fallbacks detected via capability probes.
- **Honest playback**: playback reflects actual engine capabilities. Multi-audio switching operates through standard HTML track collections; absent audio tracks report `Audio (0)` rather than synthetic entries; inband native subtitle delays that cannot be shifted without cue access surface a visible limitation message.
- **Lifecycle & backgrounding**: `document.hidden` and `webOSRelaunch` govern suspension; backgrounding synchronously saves playback position, invalidates in-flight prepares, halts SSE reconnection, and tears down media instances.
- **Manual updates**: sideloaded packages are updated by installing the newer IPK over the existing installation (`ares-install`). Disabling Developer Mode or letting the 50-hour session expire uninstalls sideloaded apps; household state (favorites, resume positions) resides safely on the server.
- **Verification boundary**: Milestone A confirms complete implementation, packaging invariants, and automated evidence across Chromium 53 and current Chromium engines. Milestone B physical TV testing remains pending on named hardware.

## References

- `docs/adr/0006-one-tv-client-spans-tizen-5-to-latest.md` — single TV client spanning platforms from a pure floor.
- `docs/adr/0008-portal-integration-and-self-update.md` — manual TV client update posture.
- `docs/adr/0009-android-tv-client-torrenttv.md` — shared web application bundle across TV platforms.
- `docs/superpowers/specs/2026-09-07-webos-tv-client-design.md` — webOS TV client architecture and parity specification.
- `docs/WEBOS.md` — build, packaging, and Developer Mode installation guide.
- `docs/WEBOS-VERIFICATION.md` — Milestone A walkthrough evidence and pending hardware matrix.
