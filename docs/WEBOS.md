# LG webOS TV client

One webOS client serves the whole supported span: a single IPK targets the webOS 4.x floor (Chromium 53) and runs across webOS 4.0 through the latest webOS releases. The client uses standard HTML5 video elements for progressive/completed torrent streaming via direct HTTP Range requests, avoiding Samsung AVPlay-specific interfaces or unnecessary transcode overhead.

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
