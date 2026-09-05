# Tizen TV 5.0 to Latest Platform Compatibility Report

This report provides a primary-source-backed technical assessment for targeting Samsung Tizen TV platform versions from **Tizen 5.0 (2019)** through **Tizen 7.0 (2023)** and up to **Tizen 10.0 (2026)** with a single Web Application package (`.wgt`).

---

## 1. Model Year to Tizen Platform Version Mapping (2019–2026)

Samsung maps each Smart TV release year to a specific baseline Tizen platform version:

| Release Year | Baseline Tizen Platform Version | OS Upgrade Eligible | Reference |
| :--- | :--- | :--- | :--- |
| **2019** | **Tizen 5.0** | No (Fixed at manufacture) | [Samsung General Specs][ref-general-specs] |
| **2020** | **Tizen 5.5** | No (Fixed at manufacture) | [Samsung General Specs][ref-general-specs] |
| **2021** | **Tizen 6.0** | No (Fixed at manufacture) | [Samsung General Specs][ref-general-specs] |
| **2022** | **Tizen 6.5** | No (Fixed at manufacture) | [Samsung General Specs][ref-general-specs] |
| **2023** | **Tizen 7.0** | **Yes** (Upgradable to Tizen 8.0+) | [Samsung General Specs][ref-general-specs] |
| **2024** | **Tizen 8.0** | **Yes** (Eligible for annual OS upgrades) | [Samsung General Specs][ref-general-specs] |
| **2025** | **Tizen 9.0** | **Yes** (Eligible for annual OS upgrades) | [Samsung General Specs][ref-general-specs] |
| **2026** | **Tizen 10.0** | **Yes** (Eligible for annual OS upgrades) | [Samsung General Specs][ref-general-specs] |

### OS Upgrade Policy Note
Historically (2015–2022 models), Samsung TV firmware never changed the major Tizen OS platform version. Starting with the **2023 TV lineup** (e.g., Samsung OLED S90C/S95C, Neo QLED QN90C), Samsung introduced the 7-year Tizen OS upgrade guarantee. Consequently, 2023 models initially shipped with Tizen 7.0 can run Tizen 8.0 or later firmware in the field.

*Citations:*
- Samsung Developer: [General Specifications][ref-general-specs] (`https://developer.samsung.com/smarttv/develop/specifications/general-specifications.html`)
- Samsung Developer: [TV Model Groups][ref-model-groups] (`https://developer.samsung.com/smarttv/develop/specifications/tv-model-groups.html`)

---

## 2. Web Engine (Chromium/Blink) Versions, JS & CSS API Levels

Each Tizen version embeds a specific release of the Chromium/Blink rendering engine and V8 JavaScript engine:

| Tizen Version | TV Year | Web Engine | Chromium Version | V8 Engine |
| :--- | :--- | :--- | :--- | :--- |
| **Tizen 5.0** | 2019 | Chromium | **M63** (Dec 2017) | V8 6.3 |
| **Tizen 5.5** | 2020 | Chromium | **M69** (Sep 2018) | V8 6.9 |
| **Tizen 6.0** | 2021 | Chromium | **M76** (Jul 2019) | V8 7.6 |
| **Tizen 6.5** | 2022 | Chromium | **M85** (Aug 2020) | V8 8.5 |
| **Tizen 7.0** | 2023 | Chromium | **M94** (Sep 2021) | V8 9.4 |
| **Tizen 8.0** | 2024 | Chromium | **M108** (Nov 2022) | V8 10.8 |
| **Tizen 9.0** | 2025 | Chromium | **M120** (Dec 2023) | V8 12.0 |
| **Tizen 10.0** | 2026 | Chromium | **M130** (Oct 2024) | V8 13.0 |

### Key JavaScript & CSS Feature Availability Matrix

| Feature | Min Chrome Milestone | Tizen 5.0 (M63) | Tizen 5.5 (M69) | Tizen 6.0 (M76) | Tizen 6.5 (M85) | Tizen 7.0 (M94) | Tizen 8.0 (M108) | Behavior on Tizen 5.0 |
| :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :--- |
| **Optional Chaining (`?.`)** | Chrome 80 | ❌ No | ❌ No | ❌ No | ✅ Yes | ✅ Yes | ✅ Yes | **SyntaxError** (app crash at parse time) |
| **Nullish Coalescing (`??`)** | Chrome 80 | ❌ No | ❌ No | ❌ No | ✅ Yes | ✅ Yes | ✅ Yes | **SyntaxError** (app crash at parse time) |
| **CSS Grid** | Chrome 57 | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | Fully supported |
| **Flexbox `gap`** | Chrome 84 | ❌ No | ❌ No | ❌ No | ✅ Yes | ✅ Yes | ✅ Yes | **Property ignored** (flex items touch without space) |
| **CSS `aspect-ratio`** | Chrome 88 | ❌ No | ❌ No | ❌ No | ❌ No | ✅ Yes | ✅ Yes | **Property ignored** (element collapses or stretches) |
| **CSS `backdrop-filter`** | Chrome 76 | ❌ No | ❌ No | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | **Property ignored** (fallback solid background required) |
| **CSS `:is()` / `:where()`** | Chrome 88 | ❌ No | ❌ No | ❌ No | ❌ No | ✅ Yes | ✅ Yes | **Rule ignored / invalid selector** |
| **CSS Native Nesting** | Chrome 112/120 | ❌ No | ❌ No | ❌ No | ❌ No | ❌ No | ❌ No | **Rule ignored** (supported only in Tizen 9.0+ / M120) |
| **`fetch()` API** | Chrome 42 | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | Fully supported |
| **`EventSource` (SSE)** | Chrome 6 | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | Fully supported |
| **`WebSocket` (RFC 6455)** | Chrome 16 | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | Fully supported |
| **`IntersectionObserver`** | Chrome 51 | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | ✅ Yes | Fully supported |
| **ES6 Classes & Modules** | Chrome 61 | Partially | Partially | Partially | Partially | Partially | Partially | Classic IIFE bundle required |

### Critical Transpilation & Styling Rules for Tizen 5.0 Floor
1. **JavaScript Target:** Vite / Babel / esbuild target MUST be set to `es2017` (or `chrome63`). All optional chaining (`?.`), nullish coalescing (`??`), and logical assignment operators (`||=`, `&&=`, `??=`) must be transpiled out.
2. **Module Format:** Samsung Tizen Web application entry points load classic `<script>` tags best when packaged as an IIFE or SystemJS bundle rather than native ES modules.
3. **Flexbox Spacing:** Do not rely exclusively on `gap` for flex layouts without a margin fallback (or use CSS Grid `grid-gap` / `gap`, which is supported in M63).
4. **Card Aspect Ratios:** Poster / thumbnail cards must not rely solely on `aspect-ratio: 2/3`; provide a padding-bottom intrinsic sizing trick or explicit heights.

*Citations:*
- Samsung Developer: [Web Engine Specifications][ref-web-engine] (`https://developer.samsung.com/smarttv/develop/specifications/web-engine-specifications.html`)
- Chrome Platform Status: [Chrome Features & Milestones][ref-chromestatus] (`https://chromestatus.com/features`)

---

## 3. Forward Compatibility of One WGT Package

### Can a single WGT with `required_version="5.0"` install and run on 7.0/8.0/10.0 TVs?
**Yes.** Tizen OS enforces backward compatibility for applications. The `required_version` attribute in `<tizen:application>` denotes the **minimum platform floor** required by the package. A package declaring `required_version="5.0"` satisfies all devices with Tizen OS $\ge 5.0$ (including 5.5, 6.0, 6.5, 7.0, 8.0, 9.0, 10.0).

### Known Pitfalls & Best Practices

1. **Device `webapis.js` vs. Bundled SDK Copy (High Risk):**
   - **Requirement:** In `index.html`, load `<script type="text/javascript" src="$WEBAPIS/webapis/webapis.js"></script>`.
   - **Pitfall:** **NEVER** bundle a static copy of `webapis.js` inside the `.wgt` archive. The `$WEBAPIS` virtual URI is dynamically resolved by the TV runtime to the device's native firmware JS bindings (`/usr/share/webapis/webapis.js`). Bundling a static `webapis.js` from Tizen SDK 5.0 or 7.0 will cause broken IPC or missing native hooks when running across different platform versions.
2. **Metadata `devel.api.version`:**
   - In `config.xml`, do not hardcode a development API version higher than the target floor unless conditionally required. Standard Tizen 5.0 metadata is sufficient.
3. **Deprecated DRM / APIs:**
   - Widevine Classic and Verimatrix Web Client have been deprecated/removed in Tizen 7.0+. Standard unencrypted HTTP direct playback and Widevine Modular L1 remain supported across both 5.0 and 7.0+.
4. **GCC Native Plugin Compatibility (Informational):**
   - Pure Web / JS / AVPlay WGT applications are not affected by C/C++ runtime ABI changes. For native plugins, GCC 9.2 was used up to 2025 (Tizen 9.0), transitioning to GCC 14.2 on 2026 (Tizen 10.0).

*Citations:*
- Samsung Developer: [Configuring TV Applications][ref-config-apps] (`https://developer.samsung.com/smarttv/develop/guides/fundamentals/configuring-tv-applications.html`)
- Samsung Developer: [TV Seller Office Launch Checklist][ref-seller-office] (`https://developer.samsung.com/tv-seller-office/checklists-for-distribution/launch-checklist.html`)

---

## 4. AVPlay Differences: Tizen 5.0 vs. 7.0/8.0

The `webapis.avplay` media player is Samsung's native hardware-accelerated playback engine.

### 1. State Machine & Initialization Lifecycle
The state machine lifecycle is identical from Tizen 2.4 through Tizen 8.x:
```
[NONE] ──open(url)──> [IDLE] ──prepareAsync()──> [READY] ──play()──> [PLAYING]
   │                     │                          │                   │
   │                     │                          │                 pause()
   │                     │                          │                   ▼
   │<────close()─────────┴────────stop()────────────┴─────────────── [PAUSED]
```

- **`open(url)`**: Moves player to `IDLE`.
- **`prepareAsync(successCb, errorCb)`**: Buffers headers/stream and moves player to `READY`. (Always use `prepareAsync` rather than synchronous `prepare()` to prevent UI thread blocking).
- **`play()`**: Moves player from `READY` or `PAUSED` to `PLAYING`.
- **`pause()`**: Moves player from `PLAYING` to `PAUSED`.
- **`stop()`**: Resets player back to `IDLE`.
- **`close()`**: Destroys player instance and returns to `NONE`.

### 2. Display Rectangle (`setDisplayRect`)
- **Virtual Coordinate Plane:** `webapis.avplay.setDisplayRect(x, y, width, height)` **ALWAYS** operates in a virtual 1920x1080 coordinate plane across all Tizen TV models (including 4K UHD and 8K panels).
- **Object Element Pairing:** An `<object type="application/avplayer">` DOM element must be attached to `document.body`, and its CSS bounding box (`left, top, width, height`) should match the `setDisplayRect` values.

### 3. Event Handling (`setListener`)
Registered via `webapis.avplay.setListener({...})`:
- `onbufferingstart()`: Stream buffer low / started filling.
- `onbufferingprogress(percent)`: Buffering progress (0–100).
- `onbufferingcomplete()`: Playback buffer primed.
- `oncurrentplaytime(ms)`: Continuous playback position callback.
- `onstreamcompleted()`: End of stream reached (app must call `webapis.avplay.stop()`).
- `onerror(errorType)`: Player error callback.
- `onevent(eventType, eventData)`: Auxiliary hardware events.
- `onsubtitlechange(duration, text, data3, data4)`: In-band subtitle text update.

### 4. Error Codes
The `onerror` callback delivers standard string error constants across 5.0 and 7.0+:
- `PLAYER_ERROR_NONE`
- `PLAYER_ERROR_INVALID_PARAMETER`
- `PLAYER_ERROR_NO_SUCH_FILE`
- `PLAYER_ERROR_SEEK_FAILED`
- `PLAYER_ERROR_INVALID_STATE`
- `PLAYER_ERROR_NOT_SUPPORTED_FILE`
- `PLAYER_ERROR_INVALID_OPERATION`
- `PLAYER_ERROR_CONNECTION_FAILED`
- `PLAYER_ERROR_GENEREIC`

### 5. Subtitle Handling (App-Rendered WebVTT vs. AVPlay Engine)
- While AVPlay supports `setExternalSubtitlePath()` and SAMI/SMPTE-TT subtitles natively, passing external WebVTT or SRT to AVPlay has had historical container/encoding quirks between Tizen minor versions.
- **Architectural Advantage:** torrent-tv renders WebVTT cues directly in the HTML/CSS DOM synchronized with `oncurrentplaytime` or periodic polling of `webapis.avplay.getCurrentTime()`. This bypasses all TV firmware subtitle parser discrepancies and guarantees identical subtitle rendering across Tizen 5.0 through 8.x.

### 6. Streaming Recovery on Network Drops
- AVPlay does not auto-recover from dropped HTTP/TCP sockets or long server stalls.
- **Recovery Pattern:** The application must intercept `onerror("PLAYER_ERROR_CONNECTION_FAILED")` or buffering timeouts, record `getCurrentTime()`, call `stop()`, re-`open(url)`, call `seekTo(resumeTime)` in `IDLE` or `READY`, and re-invoke `prepareAsync()`.

*Citations:*
- Samsung Developer: [Playback Using AVPlay][ref-avplay-guide] (`https://developer.samsung.com/smarttv/develop/guides/multimedia/media-playback/using-avplay.html`)
- Samsung Developer: [AVPlay API Reference][ref-avplay-api] (`https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/avplay-api.html`)

---

## 5. WebAPIs Availability on Tizen 5.0

All required Samsung Product WebAPIs are available on Tizen 5.0 without API-level gates:

| API Namespace | Available Since | Privilege Level | Privilege URI | Key Methods Available on Tizen 5.0 |
| :--- | :--- | :--- | :--- | :--- |
| **`webapis.productinfo`** | Tizen 2.3 | Public | `http://developer.samsung.com/privilege/productinfo` | `getDuid()`, `getModelCode()`, `getFirmware()`, `getVersion()`, `isUdPanelSupported()`, `is8KPanelSupported()` |
| **`webapis.network`** | Tizen 2.3 | Public | `http://developer.samsung.com/privilege/network.public` (or standard `tizen.network`) | `getActiveConnectionType()`, `getNetworkState()`, `getIp()`, `getWiFiEncryptionType()` |
| **`tizen.tvinputdevice`** | Tizen 2.3/2.4 | Public | `http://tizen.org/privilege/tv.inputdevice` | `registerKey()`, `registerKeyBatch()`, `unregisterKey()`, `getSupportedKeys()` |
| **`webapis.appcommon`** | Tizen 2.3 | Public | *(None required for basic screensaver control)* | `setScreenSaver(state)`, `getScreenSaver()` |

*Citations:*
- Samsung Developer: [ProductInfo API Reference][ref-productinfo-api] (`https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/productinfo-api.html`)
- Samsung Developer: [TVInputDevice API Reference][ref-tvinputdevice-api] (`https://developer.samsung.com/smarttv/develop/api-references/tizen-web-device-api-references/tvinputdevice-api.html`)
- Samsung Developer: [Network API Reference][ref-network-api] (`https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/network-api.html`)
- Samsung Developer: [AppCommon API Reference][ref-appcommon-api] (`https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/appcommon-api.html`)

---

## 6. Declared Privileges Analysis

The five declared privileges in the client's `config.xml`:

```xml
<tizen:privilege name="http://tizen.org/privilege/internet" />
<tizen:privilege name="http://tizen.org/privilege/download" />
<tizen:privilege name="http://tizen.org/privilege/tv.inputdevice" />
<tizen:privilege name="http://tizen.org/privilege/network.public" />
<tizen:privilege name="http://developer.samsung.com/privilege/productinfo" />
```

| Privilege Name | Introduced | Privilege Level | Available at 5.0? | Semantic Changes (5.0 → 8.x) |
| :--- | :--- | :--- | :---: | :--- |
| `http://tizen.org/privilege/internet` | Tizen 2.0 | Public | ✅ Yes | None. Standard network socket/fetch permission. |
| `http://tizen.org/privilege/download` | Tizen 2.0 | Public | ✅ Yes | None. Allows background asset/file downloads via `tizen.download`. |
| `http://tizen.org/privilege/tv.inputdevice` | Tizen 2.3 | Public | ✅ Yes | None. Allows remote control key registration (`registerKey`). |
| `http://tizen.org/privilege/network.public` | Tizen 3.0 | Public | ✅ Yes | None. Grants access to public network status queries. |
| `http://developer.samsung.com/privilege/productinfo` | Tizen 2.3 | Public | ✅ Yes | None. Grants access to model name, firmware, and DUID. |

### Semantic Stability
- All 5 privileges are categorized as **Public Level** (do not require Samsung Partner Program signing).
- Note: The historical `http://developer.samsung.com/privilege/avplay` privilege was made obsolete starting with 2015 models and is not required on Tizen 5.0+.

*Citations:*
- Samsung Developer: [Configuring TV Applications][ref-config-apps] (`https://developer.samsung.com/smarttv/develop/guides/fundamentals/configuring-tv-applications.html`)
- Samsung Developer: [Samsung Product API References][ref-product-apis] (`https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references.html`)

---

## 7. Apps2Samsung & Tizen Studio Signing for Multiple TVs

### Certificate Architecture Overview
A Tizen WGT package is signed using XML Digital Signatures with two distinct certificate roles:
1. **Author Certificate (`author-signature.xml`):**
   - Identifies the application author/developer.
   - **Does NOT contain DUIDs.**
   - Valid across all Tizen devices and versions. One author certificate can sign builds for both the Tizen 5.0 TV and the 2023 Tizen 7.0 TV.
2. **Distributor Certificate (`signature1.xml`):**
   - Issued by Samsung to authorize installation on specific devices in Developer Mode.
   - Embeds a whitelist of `<DUID>` (Device Unique Identifier) strings.
   - When sideloading via SDB, Apps2Samsung, or Tizen Studio, the TV's security manager validates that its hardware DUID matches one of the `<DUID>` entries in `signature1.xml`.

### Multi-TV Signing Strategies
- **Single Distributor Certificate with Multiple DUIDs:**
  - In Tizen Studio Certificate Manager (with Samsung Certificate Extension), a developer can add multiple DUIDs (e.g., DUID of the Tizen 5 TV and DUID of the Tizen 7 TV) into the same distributor certificate profile.
  - When packaged with this profile, the exact same `.wgt` binary can be installed directly onto both TVs without re-signing.
- **DUID Limits:**
  - Official Samsung documentation states there is **no hard limit** for registering DUIDs on Individual/Public developer certificates. Standard Certificate Manager profiles comfortably hold up to 50 DUIDs.
- **Apps2Samsung Workflow:**
  - Apps2Samsung communicates with the TV over the local network via SDB bridge, retrieves the TV's DUID, signs the `.wgt` payload on the fly using Samsung Certificate Extension tools, and installs it onto the target device.

*Citations:*
- Samsung Developer: [Creating Certificates][ref-certificates] (`https://developer.samsung.com/smarttv/develop/getting-started/setting-up-sdk/creating-certificates.html`)
- Samsung Developer: [Managing Certificate Profiles][ref-watch-certs] (`https://developer.samsung.com/galaxy-watch-tizen/getting-certificates/create.html`)

---

## 8. Direct-Play Codec Variance: 2019 (Tizen 5.0) vs. 2023 (Tizen 7.0)

| Media Stream / Codec | 2019 Premium UHD (Tizen 5.0) | 2023 Premium UHD/OLED (Tizen 7.0) | Direct-Play Strategy / Impact |
| :--- | :--- | :--- | :--- |
| **HEVC (H.265 Main / Main 10)** | ✅ **Supported** (up to 4K @ 60fps, 80 Mbps; 8K @ 60fps on 8K sets) | ✅ **Supported** (up to 4K @ 120fps, 80 Mbps; 8K @ 60fps on 8K sets) | Direct play 10-bit HDR/SDR HEVC on both models without transcoding. Containers: MKV, MP4, TS. |
| **AVC / H.264 (BP / MP / HP)** | ✅ **Supported** (up to 4K @ 30/60fps, Level 5.1) | ✅ **Supported** (up to 4K @ 60fps, Level 5.1) | Direct play on both models. |
| **VP9 (Profile 0 & Profile 2 10-bit)** | ✅ **Supported** (up to 4K @ 60fps, 80 Mbps in WebM) | ✅ **Supported** (up to 4K @ 60fps, 80 Mbps in WebM) | Direct play on both models. |
| **AV1** | ❌ **NOT Supported** (No hardware AV1 decoder) | ✅ **Supported** (up to 4K @ 120fps / 8K @ 60fps, 80 Mbps) | AV1 direct play requires Tizen 6.0+ (2021+) or 7.0 (2023). On 2019 models, AV1 will fail to prepare. |
| **DTS / DTS-HD / DTS:X** | ❌ **NOT Supported** (Hardware decoder omitted) | ❌ **NOT Supported** (Hardware decoder omitted) | Samsung dropped DTS support starting in 2018 (Tizen 4.0). **Both 2019 and 2023 TVs lack DTS decoding.** Server-side audio transcoding (ADR-0003) to AC3/EAC3/AAC is mandatory. |
| **Dolby Digital (AC-3)** | ✅ **Supported** (up to 5.1 ch) | ✅ **Supported** (up to 5.1 ch) | Direct play on both models. |
| **Dolby Digital Plus (E-AC-3)** | ✅ **Supported** (up to 5.1 ch) | ✅ **Supported** (up to 5.1 ch) | Direct play on both models. |
| **AAC (AAC-LC, HE-AAC v1/v2)** | ✅ **Supported** | ✅ **Supported** | Direct play on both models. |
| **Opus / Vorbis / FLAC** | ✅ **Supported** (Opus in MKV/WebM; FLAC up to 5.1 ch) | ✅ **Supported** | Direct play on both models. |

*Citations:*
- Samsung Developer: [2019 TV Video Specifications][ref-2019-video] (`https://developer.samsung.com/smarttv/develop/specifications/media-specifications/2019-tv-video-specifications.html`)
- Samsung Developer: [2023 TV Video Specifications][ref-2023-video] (`https://developer.samsung.com/smarttv/develop/specifications/media-specifications/2023-tv-video-specifications.html`)
- Samsung Developer: [Media Specifications][ref-media-specs] (`https://developer.samsung.com/smarttv/develop/specifications/media-specifications.html`)

---

## 9. Tizen 5.0 Platform Quirks & Architectural Considerations

### 1. Private-LAN `fetch()` & CORS / WARP Security Policy
- **Origin Context:** Tizen WGT packaged applications execute with origin `file://` (or `http://localhost`).
- **W3C Access Requests Policy (WARP):** By default, Tizen blocks all outbound HTTP network traffic unless declared in `config.xml`. To allow LAN discovery and streaming on port 8097, `config.xml` MUST contain:
  ```xml
  <access origin="*" subdomains="true" />
  ```
- **Chromium Private Network Access (PNA):** Modern Chromium (M96+) introduced PNA preflights (`Access-Control-Request-Private-Network`). Tizen 5.0 runs Chromium M63 where PNA preflighting was not yet introduced. However, standard CORS headers (`Access-Control-Allow-Origin: *`, `Access-Control-Allow-Methods: GET, POST, OPTIONS`, `Access-Control-Allow-Headers: *`) must still be returned by the backend server for all REST endpoints.

### 2. Server-Sent Events (`EventSource`) & WebSockets
- `EventSource` (SSE) and `WebSocket` (RFC 6455) are fully supported in Chromium M63 on Tizen 5.0.
- **Reconnection Handling:** When the TV transitions to screensaver or background standby, TCP sockets may be dropped by the operating system without triggering an immediate TCP FIN packet. Client SSE connections must implement a heartbeat monitor (e.g., ping every 15s) and automatic exponential backoff reconnection.

### 3. `localStorage` Limits
- Standard `localStorage` quota on Chromium M63 is **5 MB per origin**.
- For storing watch state, recent history, and UI settings, `localStorage` is completely adequate. For large catalogs or image blobs, use IndexedDB.

### 4. App Suspend / Resume & Lifecycle Management
- **Visibility Lifecycle:** When the user switches TV input, opens the Smart Hub menu, or turns off the screen, Tizen pauses the web application runtime and fires standard DOM events:
  ```javascript
  document.addEventListener("visibilitychange", function() {
    if (document.hidden) {
      // Application entering background / standby:
      // 1. Pause or close AVPlay instance
      // 2. Suspend active polling / SSE listeners
    } else {
      // Application resuming foreground:
      // 1. Re-verify network connection via webapis.network / fetch
      // 2. Resume SSE stream or UI state
    }
  });
  ```
- **AVPlay State Preservation:** If an AVPlay instance is left in `PLAYING` when the application is suspended, hardware decoder resources may remain locked, leading to `PLAYER_ERROR_INVALID_OPERATION` or black screens upon return. Calling `webapis.avplay.suspend()` / `webapis.avplay.restore()` or cleanly resetting with `webapis.avplay.stop()` / `close()` on visibility hide is recommended.

*Citations:*
- Samsung Developer: [Configuring TV Applications (WARP Policy)][ref-config-apps] (`https://developer.samsung.com/smarttv/develop/guides/fundamentals/configuring-tv-applications.html`)
- Samsung Developer: [Security FAQ][ref-security-faq] (`https://developer.samsung.com/smarttv/develop/faq/security.html`)
- Samsung Developer: [Playback Using AVPlay Lifecycle][ref-avplay-guide] (`https://developer.samsung.com/smarttv/develop/guides/multimedia/media-playback/using-avplay.html`)

---

## 10. Confidence & Gaps

### High-Confidence Verified Claims (Primary Sources)
- **Model Year & Web Engine Mapping:** Exact Chromium milestone versions (M63 for Tizen 5.0 up to M94 for Tizen 7.0 and M108 for 8.0) verified directly from Samsung Web Engine Specifications.
- **JavaScript & CSS Compatibility:** Exact milestone availability for optional chaining (`?.`), flexbox `gap`, `aspect-ratio`, and `backdrop-filter` verified against Chromium engine milestones.
- **WebAPIs & Privileges:** `productinfo`, `network`, `tvinputdevice`, and `appcommon` interfaces and Public privilege status verified from official Samsung TV API references.
- **Codec Matrix:** HEVC 10-bit, VP9, AV1, and DTS support status verified against Samsung 2019 and 2023 TV Video Specifications.

### Platform Gaps & Inferences
- **Private Network Access (PNA) in Tizen 7.0/8.0:** While Tizen 5.0 (M63) does not enforce PNA preflights, newer Chromium runtimes on Tizen 7.0/8.0 (M94/M108) enforce stricter mixed content and private network rules when accessed from public origins. Because the WGT executes locally (`file://`), standard CORS headers satisfy the runtime.
- **Hardware-Specific Display Performance:** While `backdrop-filter` is syntactically supported starting in Chromium M76 (Tizen 6.0+), real-time CSS backdrop blur on 4K TV GPUs can cause frame-rate drops during remote control navigation. Simple semi-transparent solid backgrounds (`rgba(0,0,0,0.85)`) remain the recommended cross-platform design standard.

---

## Addendum (2026-08-31): WARP does bypass CORS for packaged apps — device-observed

**Correction to §9.1 ("Private-LAN `fetch()` & CORS / WARP Security Policy") and to the §10 PNA inference** ("standard CORS headers satisfy the runtime"). Both were written from documentation research only. On-device observation on the household's 2019 premium Tizen 5.0 set **contradicts** the earlier claim that the backend must return CORS headers: the server sends **no** CORS headers at all, yet the packaged WGT (origin `file://`) reaches the LAN server through WARP with no problems — manual connect via **Manual address** and all subsequent catalog fetches succeed on-device.

The accurate statement: for a Tizen *packaged* application, WARP (`<access origin="*" subdomains="true"/>`) — not the HTTP CORS mechanism — governs outbound network access, and a WARP-authorized fetch to a plain-LAN HTTP endpoint is not subject to server-side CORS the way a browser-origin fetch is. The CORS guidance in the original sections remains true only for browser (web) clients of the same server, which do run with an HTTP origin.

This report is a research snapshot; the original sections above are kept verbatim for the record. The TIZEN.md verification log is the durable source of truth for device behavior.

---

<!-- Citation References -->
[ref-general-specs]: https://developer.samsung.com/smarttv/develop/specifications/general-specifications.html
[ref-model-groups]: https://developer.samsung.com/smarttv/develop/specifications/tv-model-groups.html
[ref-web-engine]: https://developer.samsung.com/smarttv/develop/specifications/web-engine-specifications.html
[ref-chromestatus]: https://chromestatus.com/features
[ref-config-apps]: https://developer.samsung.com/smarttv/develop/guides/fundamentals/configuring-tv-applications.html
[ref-seller-office]: https://developer.samsung.com/tv-seller-office/checklists-for-distribution/launch-checklist.html
[ref-avplay-guide]: https://developer.samsung.com/smarttv/develop/guides/multimedia/media-playback/using-avplay.html
[ref-avplay-api]: https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/avplay-api.html
[ref-productinfo-api]: https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/productinfo-api.html
[ref-tvinputdevice-api]: https://developer.samsung.com/smarttv/develop/api-references/tizen-web-device-api-references/tvinputdevice-api.html
[ref-network-api]: https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/network-api.html
[ref-appcommon-api]: https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/appcommon-api.html
[ref-product-apis]: https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references.html
[ref-certificates]: https://developer.samsung.com/smarttv/develop/getting-started/setting-up-sdk/creating-certificates.html
[ref-watch-certs]: https://developer.samsung.com/galaxy-watch-tizen/getting-certificates/create.html
[ref-2019-video]: https://developer.samsung.com/smarttv/develop/specifications/media-specifications/2019-tv-video-specifications.html
[ref-2023-video]: https://developer.samsung.com/smarttv/develop/specifications/media-specifications/2023-tv-video-specifications.html
[ref-media-specs]: https://developer.samsung.com/smarttv/develop/specifications/media-specifications.html
[ref-security-faq]: https://developer.samsung.com/smarttv/develop/faq/security.html
