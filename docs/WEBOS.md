# LG webOS TV client

One webOS client serves the whole supported span: a single IPK targets the webOS 4.x floor (Chromium 53) and runs across webOS 4.0 through the latest webOS releases. The client uses standard HTML5 video elements for progressive/completed torrent streaming via direct HTTP Range requests, avoiding Samsung AVPlay-specific interfaces or unnecessary transcode overhead.

## Requirements

- **TV:** an LG webOS TV running webOS TV 4.0 or newer. One IPK targets the Chromium 53 engine floor and the same package installs across the supported span; webOS TV 3.x is not covered and stays under separate investigation (see `docs/adr/0010-webos-tv-client.md` and `docs/WEBOS-VERIFICATION.md`).
- **Computer:** macOS, Linux, or Windows on the same LAN as the TV, with Node.js >= 14.15.1 and the pinned packaging CLI (`cd clients/webos && npm install`). Docker is required only for the headless engine smoke.
- **Accounts and TV setup:** an LG developer account and the TV's **Developer Mode** app (below). The client is a sideloaded developer application; it is not distributed through any TV app store and nothing in this repository roots, jailbreaks, or auto-updates the TV.
- **Server:** a reachable Torrent TV server on the home LAN (discovered by scan, or entered as a manual address). All household state lives on the server, so installing, updating, or uninstalling the TV application never touches favorites or resume positions.

## TV Developer Mode: enable, renew, and the uninstall warning

Sideloading uses LG's official Developer Mode app — the same mechanism LG documents for web app testing. There is no rooting or developer-server enrollment involved.

1. On the TV, open **LG Apps**, search for **Developer Mode**, and install it.
2. Launch the app and sign in with your LG developer account (email-based ID and password).
3. Select **Dev Mode Status** to turn Developer Mode on. The TV reboots.

Warnings that matter for this client:

- **Developer Mode is time-limited.** The app shows the remaining session time; extend it with the **EXTEND** button while the TV is online, before the session runs out. Once expired, the session cannot be extended.
- **Expiry or disable uninstalls sideloaded apps.** Developer Mode is switched off by LG when the TV reboots with an expired session, after ten reboots without a network connection, or when you turn Dev Mode Status off yourself. When that happens, every application installed through Developer Mode — `com.torrenttv.app` included — is uninstalled automatically. Re-enable Developer Mode and install the IPK again (see [WEBOS-VERIFICATION](WEBOS-VERIFICATION.md) for the current artifact checksum).
- The only state lost to such an uninstall is the application itself. Favorites, resume positions, and downloads live on the Torrent TV server and are picked up again on the next connect.

## Pair, install, update, and uninstall with the ares CLI

Pair the computer with the TV once (device name `tv` below is an example; port `9922` and user `prisoner` are LG's fixed values for Developer Mode):

```sh
ares-setup-device                          # select "add": device name, TV IP address, port 9922, ssh user prisoner
ares-novacom --device tv --getkey          # enter the Passphrase shown by the Key Server button in the Developer Mode app
ares-device --system-info --device tv      # prints modelName/sdkVersion when the pairing works
```

The passphrase exists only on the TV screen and in your head — it is never stored in this repository. The SSH key material `ares-novacom` writes lives in your user's webOS CLI configuration outside the repository.

Build, install, launch:

```sh
npm run build:webos
make webos-ipk && make validate-webos-ipk
ares-install --device tv clients/webos/.build/artifacts/torrent-tv-<VERSION>-webos.ipk
ares-launch --device tv com.torrenttv.app
```

Replacing an installed client is the same install command with a newer IPK: the package id stays `com.torrenttv.app` and the declared version comes from the repository's root `VERSION`. LG replaces the previous version in place; no uninstall step is needed between versions. To remove the client deliberately, run `ares-uninstall --device tv com.torrenttv.app` — or let the Developer Mode uninstall path above do it.

Installation, launch, and remote-control behavior on physical hardware are recorded in [WEBOS-VERIFICATION](WEBOS-VERIFICATION.md); no LG TV has been exercised yet, so those rows are pending.
## Packaging tooling: @webos-tools/cli 3.2.5

The official LG webOS CLI tools provide `ares-package`, which packages the application into the standard webOS `.ipk` format (an `ar` archive with `debian-binary`, `control.tar.gz`, and `data.tar.gz`) and validates `appinfo.json` metadata.

- **Package:** `@webos-tools/cli`
- **Version:** `3.2.5` (exact pin in `clients/webos/package.json` `devDependencies`)
- **Download / Install source:** Official npm registry (`https://registry.npmjs.org/@webos-tools/cli/-/cli-3.2.5.tgz`)
- **Runtime requirements:** Node.js >= 14.15.1 (pure JavaScript)
- **Official documentation:** [LG webOS TV CLI Installation](https://webostv.developer.lge.com/develop/tools/cli-installation)

To install the packaging CLI:

```sh
cd clients/webos && npm install
```

## Vendored LG webOS TV SDK

The client loads the LG webOS TV SDK locally from the package:

- **Upstream source:** `https://webostv.developer.lge.com/assets/library/webOSTVjs-1.2.13.zip`
- **Version:** webOSTVjs 1.2.13 (LG official release, full support for webOS TV 4.0+)
- **SHA-256:** `507c759f65a035122afead166608e8c7f444961468c3570245f0982c8f855ff6`
- **Vendored files:**
  - `clients/webos/vendor/webOSTVjs-1.2.13/webOSTV.js` (`window.webOS`)
  - `clients/webos/vendor/webOSTVjs-1.2.13/webOSTV-dev.js` (`window.webOSDev`)
  - `clients/webos/vendor/webOSTVjs-1.2.13/LICENSE-2.0.txt` (Apache License 2.0)

## Build and package a webOS IPK

To build the client bundle and pack the IPK:

```sh
# 1. Build the webOS web application dist
npm run build:webos

# 2. Pack the IPK package
make webos-ipk

# 3. Validate the generated package
make validate-webos-ipk
```

This creates:

```text
clients/webos/.build/artifacts/torrent-tv-<VERSION>-webos.ipk
clients/webos/.build/artifacts/torrent-tv-<VERSION>-webos.ipk.sha256
```

### Offline package validation

`tools/webos_ipk.py validate` checks:

1. Exact `ar` member layout: `debian-binary` (content `2.0`), `control.tar.gz`, `data.tar.gz`.
2. Control file fields: `Package: com.torrenttv.app`, `Architecture: all`, `Version` matching root `VERSION`.
3. Application metadata (`appinfo.json`):
   - Package ID `com.torrenttv.app` (lowercase, reverse-DNS, no `com.palm`/`com.webos`/`com.lge` prefix).
   - Version matching root `VERSION`.
   - `type: "web"`, `main: "index.html"`, non-empty `vendor`.
   - `disableBackHistoryAPI: true`.
   - No Samsung/Tizen-specific keys.
   - All referenced assets present.
4. Icon dimensions:
   - `icon.png`: 80×80 px PNG
   - `largeIcon.png`: 130×130 px PNG
   - `splashBackground.png`: 1920×1080 px PNG
   - `bgImage.png`: 1920×1080 px PNG
5. Required runtime scripts present: `app.js`, `app.css`, `index.html`, `fatal-error.js`, `startup.js`, `webOSTV.js`, `webOSTV-dev.js`.
6. Vendored SDK files present.
7. Zero ES module syntax (`import`/`export`) in packaged JavaScript.
8. Zero Samsung `$WEBAPIS` references.
9. Zero development artifacts (`.build/`, `node_modules/`, `*.ts`, `*.map`, `tests/`) leaked into the package.

## Headless old-engine boot smoke

The webOS boot smoke test verifies clean boot and error-handling on the pinned Chromium 53 engine floor:

```sh
make smoke-webos-engine
```

- **Engine image:** `selenoid/chrome:53.0` (Google Chrome 53.0.2785.143, digest `sha256:5e3d995ded003752cf9a8450657caa163308fec51578708c23c624f37b97c0ea`).
- **Cases:**
  - `clean`: D-pad navigation through setup, home catalog rendering, rail traversal, player mounting, seek.
  - `fatal`: unhandled exceptions surface `FileListFatalError` panel and post diagnostics.
  - `broken`: corrupt/missing bundle is rejected as designed (exit code 3).

## Icon asset derivation

All webOS visual assets are derived strictly from the shared TV identity asset (`clients/tizen/icon.svg`) and the client background color (`#071018` from `clients/webos/index.html`), introducing zero invented branding or colors:

```sh
# 1. Launcher icon (80x80)
rsvg-convert -w 80 -h 80 clients/tizen/icon.svg -o clients/webos/icon.png

# 2. Large icon (130x130)
rsvg-convert -w 130 -h 130 clients/tizen/icon.svg -o clients/webos/largeIcon.png

# 3. Splash background (1920x1080) and background image (1920x1080)
# Composed using background #071018 with 320x320 centered icon
```

## Runtime notes

- **Remote control and navigation:** D-pad Arrow keys, Enter/OK, and Back navigate the UI using the shared spatial-navigation engine. LG remote keycode `461` is recognized as `back` by the input and player mappers alongside standard key identifiers. Back closes an active panel or input edit before exiting the current route; a physical long-press Back exit remains a pending hardware check.
- **Virtual keyboard:** text inputs are read-only during D-pad traversal; pressing Enter enters edit mode and summons the webOS system keyboard. Dismissal (via Done, Back, or the `keyboardStateChange` event) blurs the input and restores focus to the invoking control. The same Back keypress that dismisses the keyboard does not pop the route.
- **OS browser handoff:** external links (in the Projects catalog and Settings release notices) launch LG's browser app (`com.webos.app.browser`) through `webOSDev.launch`. When handoff fails or is refused, the URL remains visible on screen with an explicit notice to open it on another device; the TV document is never navigated away.
- **Lifecycle and suspend/resume:** `document.hidden` transitions and `webOSRelaunch` events coordinate backgrounding. On suspend (`hidden === true`), the playing position is saved synchronously, the media element/AVPlay instance is closed, and EventSource reconnections are halted. On resume (`hidden === false`), state is re-fetched and playback reopens at the saved position (staying paused if it was paused before suspend).
- **Media adapter:** the client provides `window.webapis.avplay` implemented over a standard HTML5 `<video>` element, allowing the shared TV Player component to run unmodified. Direct HTTP Range requests serve progressive and completed torrent streams. The adapter reports an honest track inventory: audio and subtitle menus display actual available tracks, and native cue operations that cannot be supported without cue access fail visibly rather than faking success.
- **Diagnostic startup:** the client loads ES5 classic scripts in strict order: `fatal-error.js` → `startup.js` → `webOSTV.js` → `webOSTV-dev.js` → `app.js`. If the bundle fails to load or throws during startup, the `FileListFatalError` panel renders an alert and posts diagnostics to the server's `/api/v1/diagnostics/client` endpoint instead of leaving a black screen.

## Deliberate platform differences

The webOS client shares its entire UI codebase (`clients/tv/src`) with the Tizen and Android TV clients. Differences exist only at the platform boundary:

| Area | Samsung Tizen | Android TV (TorrentTV) | LG webOS TV |
| --- | --- | --- | --- |
| **Package format** | Unsigned `.wgt` (ZIP) | `.apk` (Gradle) | `.ipk` (`ar` archive via `ares-package`) |
| **Application ID** | `com.torrenttv.app` (widget) | `com.torrenttv.app` | `com.torrenttv.app` |
| **Video decoding** | Samsung AVPlay native video plane | Media3 ExoPlayer on native surface | HTML5 `<video>` element via AVPlay adapter |
| **Packaging CLI** | Python standard library | Gradle / Android SDK | `@webos-tools/cli` 3.2.5 (`ares-package`) |
| **Sideload tool** | Apps2Samsung | `adb install` | `ares-install` via Developer Mode app |
| **Developer mode** | 5-digit PIN (`12345`) + IP setup | Android developer options / USB debugging | Developer Mode app from LG Apps + login |
| **Session expiry** | Persistent until disabled | Persistent | Time-limited; requires manual renewal |
| **Uninstall on disable** | No | No | Yes: disabling Dev Mode removes sideloaded apps |
| **Keyboard** | Samsung IME keycodes (`65376`/`65385`) | Android system IME | webOS system keyboard (`keyboardStateChange`) |
| **Link handoff** | `launchAppControl` | Native bridge `openExternal` | `webOSDev.launch` (`com.webos.app.browser`) |
| **Back key** | Keycode `10009` | `GoBack` / `BrowserBack` / `Escape` | Keycode `461` / `Back` |

## Verification commands

All commands run from the repository root:

```sh
# 1. Run client unit tests across all packages (includes webOS platform and AVPlay suites)
npm run test:clients

# 2. Run packaging unit tests
python3 -m unittest discover -s tools/tests -p 'test_*.py'

# 3. Build and package the webOS distribution
npm run build:webos
make webos-ipk && make validate-webos-ipk

# 4. Run the headless Chromium 53 floor engine smoke (Docker required)
make smoke-webos-engine

# 5. Run smoke cases directly against a specified browser binary
node tools/smoke_webos_engine/smoke.mjs --dist clients/webos/dist --cases clean,fatal
node tools/smoke_webos_engine/smoke.mjs --dist clients/webos/dist --case broken   # exits 3 as designed
```

See [docs/WEBOS-VERIFICATION.md](WEBOS-VERIFICATION.md) for the complete Milestone A walkthrough results, engine versions, and the pending hardware matrix.
