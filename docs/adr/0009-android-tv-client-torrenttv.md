# A second TV client puts TorrentTV on Android 8.0 through the latest platform

---
status: accepted
---

The household wants the TV client on Android TV hardware from 2018 onward.
The Tizen client is a web app whose only platform seams are the AVPlay
player API, LAN-discovery network info, app exit, and remote-control key
codes — so the Android client is a thin Kotlin WebView shell around the same
web bundle, not a second implementation of the UI. One web app
(`clients/tv`) serves both TVs; the shell injects the display identity
("TorrentTV"/"TT", the user-chosen Android title) and a native bridge that
re-expresses the AVPlay shape on media3 ExoPlayer, with video on a surface
behind the WebView exactly like Tizen's video plane. The floor is Android
8.0 (`minSdk 26`), what 2018 Android TVs shipped — a pure floor, no
ceiling, one APK, and the same austerity rules ADR-0006 imposed for the
Tizen 5.0 engine floor. The server gains permissive CORS on `/api/v1`
because Tizen's widget runtime bypasses CORS and a WebView does not.
Updates stay manual sideloads from repository releases, matching the Tizen
posture in ADR-0008.

## Considered options

- **Fully native Leanback/Compose rewrite** — rejected: re-implements every screen, drifts from the Tizen design by construction, and leaves two clients to keep in sync for zero feature gain.
- **WebView shell with HTML5 video** — rejected: Chromium exposes no audio-track JS API, so the player's Audio menu would silently die.
- **WebView shell hosting the browser client (`web/`)** — rejected: that is the browser interaction model, not the TV design.
- **Raise the floor above 8.0** — rejected: 2018 hardware ships Android 8.0, and the Tizen experience (ADR-0006) showed the floor's costs are bounded.

## Consequences

- Parity is contractual: the spec's Parity contract (one byte-identical bundle, no platform branching in UI code, inventory-as-checklist) binds both clients; CI enforces the same-bundle rule.
- Codec reality matches ADR-0006 on Android: direct play only (ADR-0001); DTS-class audio and AV1 fail on 2018-era hardware and are avoided by choosing another release.
- Verification stays best-effort until the household names its Android TV hardware, mirroring the tested-on-mine posture of ADR-0006.
- The AVPlay-shaped bridge is retained (not renamed neutral) to keep the Player component byte-identical; a neutral rename may come later behind the same contract.
