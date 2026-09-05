# Android TV Client (TorrentTV) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship TorrentTV — an Android TV client (Android 8.0/API 26 and newer) that runs the same web bundle as the Tizen client behind a Kotlin WebView shell with an ExoPlayer-backed AVPlay bridge, verified feature-identical per the spec's parity contract.

**Architecture:** One shared web app (`clients/tv`) is consumed by two thin platform shells: the existing Tizen WGT packaging and a new Gradle/Kotlin shell (`clients/android-tv`) that renders it in a WebView with video on a native SurfaceView behind it. Four platform seams (player, network info, keys, exit) are implemented natively behind the exact API shapes the web app already programs against; a server CORS middleware makes the API callable from a locally-packaged client.

**Tech Stack:** Preact + Vite (existing web app, `es2017` target), Kotlin + Android Gradle Plugin 8.7.3 + Gradle 8.9, androidx.webkit `WebViewAssetLoader`, androidx.media3 ExoPlayer 1.5.1, Go stdlib `net/http` (CORS middleware), GitHub Actions (API 26 ATV emulator).

**Spec:** `docs/superpowers/specs/2026-09-05-android-tv-client-design.md` — read it first; the Parity contract section is this plan's acceptance bar.

## Global Constraints

- Support floor: `minSdk 26` (Android 8.0, 2018 TVs). One APK, no ceiling.
- App identity: title **TorrentTV**, monogram **TT**, `applicationId com.torrenttv.app`, `versionName "0.1.0"`.
- The shared web app keeps the Tizen austerity rules: `es2017` bundle target, **no CSS `gap`** (margin-based spacing), `cssMinify: false`, `AbortController` guarded.
- **No platform branching in UI code** (spec Parity contract rule 2): no `isAndroid` conditionals in screens, components, or CSS. Platform behavior lives only in the seams: `window.webapis` shim, `window.FileListTVNative`, `window.FileListTVIdentity`, and the key tables' added key codes.
- Named exceptions to parity (the complete list, from the spec): app display name/monogram, and the four seams.
- Every Android-facing artifact says TorrentTV; the Tizen app stays "FileList TV".
- Commits: one commit per task (or per step where marked), imperative subject, no co-author trailers.

---

### Task 1: Extract `clients/tv` — the shared TV web app package

**Files:**
- Move: `clients/tizen/src` → `clients/tv/src`; `clients/tizen/index.html`, `startup.js`, `fatal-error.js`, `tsconfig.json`, `vite.config.ts`, `dist` → `clients/tv/…`
- Create: `clients/tv/package.json`
- Keep in `clients/tizen`: `config.xml`, `icon.png`, `icon.svg`, `certificates/`, `scripts/package.sh`, `.build/`
- Modify: `clients/tizen/scripts/package.sh`, root `package.json`, `Makefile`, `.github/workflows/ci.yml` (path references only)

**Interfaces:**
- Produces: workspace package `@filelist/tv` with `npm run test` (vitest) and `npm run build` (Vite → `clients/tv/dist/{index.html,app.js,app.css,startup.js,fatal-error.js}`). Later tasks and the Tizen packaging both consume `clients/tv/dist`.

- [ ] **Step 1: Move the web app with git mv (preserves history)**

```bash
cd /Users/mihai.mihaila/Workspace/filelist-streaming-service
mkdir -p clients/tv
git mv clients/tizen/src clients/tv/src
git mv clients/tizen/index.html clients/tv/index.html
git mv clients/tizen/startup.js clients/tv/startup.js
git mv clients/tizen/fatal-error.js clients/tv/fatal-error.js
git mv clients/tizen/tsconfig.json clients/tv/tsconfig.json
git mv clients/tizen/vite.config.ts clients/tv/vite.config.ts
git mv clients/tizen/dist clients/tv/dist
```

- [ ] **Step 2: Create `clients/tv/package.json`**

```json
{
  "name": "@filelist/tv",
  "version": "0.3.0",
  "private": true,
  "type": "module",
  "scripts": {
    "test": "vitest run",
    "build": "npm run test && tsc --noEmit && vite build"
  },
  "dependencies": {
    "@filelist/shared": "0.1.0",
    "preact": "10.29.8"
  },
  "devDependencies": {
    "@preact/preset-vite": "2.10.2",
    "typescript": "5.9.2",
    "vite": "7.3.6",
    "vitest": "4.1.10"
  }
}
```

- [ ] **Step 3: Delete the now-empty Tizen workspace manifest**

Delete `clients/tizen/package.json` (the Tizen directory becomes pure packaging: config.xml, icon, certificates, scripts). Remove `clients/tizen/tsconfig.json` only if the move in Step 1 left a duplicate; it was moved in Step 1.

- [ ] **Step 4: Rewire the root workspace and scripts**

Root `package.json` — replace the `clients/tizen` workspace with `clients/tv` and update the scripts:

```json
{
  "workspaces": [
    "web",
    "clients/shared",
    "clients/tv",
    "desktop"
  ],
  "scripts": {
    "build": "npm run build -w @filelist/web && npm run build -w @filelist/tv",
    "build:web": "npm run build -w @filelist/web",
    "build:tv": "npm run build -w @filelist/tv",
    "build:desktop": "npm run build -w @filelist/desktop",
    "test:clients": "npm run test -w @filelist/shared && npm run test -w @filelist/web && npm run test -w @filelist/tv"
  }
}
```

(Keep `engines` and any other keys exactly as they are.)

- [ ] **Step 5: Rewire path references outside the clients dirs**

Run `grep -rn "clients/tizen" --include="*.yml" --include="*.yaml" --include="Makefile" --include="*.sh" --include="*.py" --include="*.md" . | grep -v node_modules | grep -v clients/tizen/` and apply this mapping:

- References to `clients/tizen/dist` / `clients/tizen/src` / `clients/tizen/node_modules` → `clients/tv/dist` / `clients/tv/src` / `clients/tv/node_modules`. Known hits to fix: `Makefile` (`frontend` docker mount `-v /src/clients/tizen/node_modules` → `-v /src/clients/tv/node_modules`; `tizen-wgt` and `validate-tizen-wgt` `--source clients/tizen/dist` → `--source clients/tv/dist`; any `build:tizen` references → `build:tv`), `.github/workflows/ci.yml` (workspace name or path references), `tools/` scripts if any.
- References to `clients/tizen/config.xml`, `clients/tizen/icon.png`, `clients/tizen/certificates`, `clients/tizen/.build` stay unchanged.

- [ ] **Step 6: Update `clients/tizen/scripts/package.sh` to consume the shared dist**

Change the dist copy line (keep everything else byte-identical):

```sh
cp -R "$root/../tv/dist/." "$stage/"
```

- [ ] **Step 7: Reinstall workspaces and verify green**

```bash
npm install
npm run test -w @filelist/tv        # all moved vitest suites pass
npm run build -w @filelist/tv       # tsc + vite build emit clients/tv/dist
sh -n clients/tizen/scripts/package.sh   # script still parses
```

Expected: all existing tests pass; `clients/tv/dist/app.js` and `app.css` are regenerated.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor(clients): extract the TV web app into @filelist/tv"
```

---

### Task 2: Platform display-name plumbing (`appIdentity`)

**Files:**
- Create: `clients/tv/src/app-name.ts`, `clients/tv/src/app-name.test.ts`
- Modify: `clients/tv/src/main.tsx` (three brand spots), `clients/tv/src/globals.d.ts`

**Interfaces:**
- Consumes: `window.FileListTVIdentity` (optional; set later by the Android shell's `platform-bridge.js`).
- Produces: `appIdentity(): AppIdentity` where `AppIdentity = { name: string; monogram: string }`; defaults to `{ name: 'FileList TV', monogram: 'FL' }`.

- [ ] **Step 1: Write the failing tests**

```ts
// clients/tv/src/app-name.test.ts
import { afterEach, describe, expect, it } from 'vitest';
import { appIdentity } from './app-name';

afterEach(() => { delete (window as any).FileListTVIdentity; });

describe('appIdentity', () => {
  it('defaults to FileList TV with the FL monogram', () => {
    expect(appIdentity()).toEqual({ name: 'FileList TV', monogram: 'FL' });
  });
  it('prefers the platform-injected identity', () => {
    (window as any).FileListTVIdentity = { name: 'TorrentTV', monogram: 'TT' };
    expect(appIdentity()).toEqual({ name: 'TorrentTV', monogram: 'TT' });
  });
  it('derives the monogram when only a name is injected', () => {
    (window as any).FileListTVIdentity = { name: 'TorrentTV' };
    expect(appIdentity()).toEqual({ name: 'TorrentTV', monogram: 'TO' });
  });
  it('falls back when the injected name is blank', () => {
    (window as any).FileListTVIdentity = { name: '   ' };
    expect(appIdentity().name).toBe('FileList TV');
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm run test -w @filelist/tv -- app-name`
Expected: FAIL — cannot resolve `./app-name`.

- [ ] **Step 3: Implement `app-name.ts`**

```ts
// clients/tv/src/app-name.ts
// The platform shell injects the client's display identity before the bundle
// loads (Tizen leaves it unset and gets the default). UI code only ever reads
// this — never hardcodes a brand string (spec: Parity contract, named
// exceptions).
export interface AppIdentity { name: string; monogram: string }

declare global {
  interface Window {
    FileListTVIdentity?: { name?: string; monogram?: string };
  }
}

export function appIdentity(): AppIdentity {
  const injected = window.FileListTVIdentity;
  const name = typeof injected?.name === 'string' && injected.name.trim() ? injected.name.trim() : 'FileList TV';
  const monogram = typeof injected?.monogram === 'string' && injected.monogram.trim()
    ? injected.monogram.trim()
    : name.split(/\s+/).map(word => word[0] || '').join('').slice(0, 2).toUpperCase();
  return { name, monogram };
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run test -w @filelist/tv -- app-name`
Expected: PASS (4 tests).

- [ ] **Step 5: Replace the hardcoded brand strings in `main.tsx`**

Three exact replacements in `clients/tv/src/main.tsx`:

1. Setup heading — `<h1>FileList TV</h1>` becomes `<h1>{appIdentity().name}</h1>`
2. Sidebar brand — `<div class="tv-brand"><span>FL</span><b>FileList TV</b></div>` becomes `<div class="tv-brand"><span>{appIdentity().monogram}</span><b>{appIdentity().name}</b></div>`
3. Header eyebrow — `{route === 'home' ? 'PRIVATE SCREENING ARCHIVE' : 'FILELIST TV'}` becomes `{route === 'home' ? 'PRIVATE SCREENING ARCHIVE' : appIdentity().name.toUpperCase()}`

Add to the imports at the top of `main.tsx`:

```ts
import { appIdentity } from './app-name';
```

- [ ] **Step 6: Run the full suite and build**

Run: `npm run build -w @filelist/tv`
Expected: tests pass, `tsc --noEmit` clean, vite build succeeds. Grep to confirm no other hardcoded brand strings remain in app code: `grep -n "FileList TV\|FL</span>" clients/tv/src/main.tsx` — expected: no output.

- [ ] **Step 7: Commit**

```bash
git add clients/tv/src/app-name.ts clients/tv/src/app-name.test.ts clients/tv/src/main.tsx
git commit -m "feat(tv): read the app display name from the platform shell"
```

---

### Task 3: Android remote-key mappings in the shared key tables

**Files:**
- Modify: `clients/tv/src/player.ts` (`playerAction`), `clients/tv/src/navigation.ts` (`remoteAction`)
- Test: `clients/tv/src/player.test.ts`, `clients/tv/src/navigation.test.ts`

**Interfaces:**
- Produces: `playerAction(key, keyCode)` additionally maps Android/Chromium media key names (`MediaPlay`, `MediaPause`, `MediaPlayPause`, `MediaStop`, `MediaRewind`, `MediaFastForward`, `MediaTrackPrevious`, `MediaTrackNext`) and back-arrivals (`GoBack`, `BrowserBack`, keyCode 27 Escape) to the existing `PlayerAction` values; `remoteAction` additionally maps `GoBack`/`BrowserBack`/keyCode 27 to `'back'`. No existing mapping changes (Tizen keyCodes 415/19/10252/413/412/417/10232/10233 and 10009 stay).

- [ ] **Step 1: Write the failing tests**

Append to `clients/tv/src/player.test.ts`:

```ts
describe('playerAction Android media keys', () => {
  it('maps Android media key names', () => {
    expect(playerAction('MediaPlay', 0)).toBe('play');
    expect(playerAction('MediaPause', 0)).toBe('pause');
    expect(playerAction('MediaPlayPause', 179)).toBe('play-pause');
    expect(playerAction('MediaStop', 178)).toBe('stop');
    expect(playerAction('MediaRewind', 177)).toBe('rewind');
    expect(playerAction('MediaFastForward', 228)).toBe('fast-forward');
    expect(playerAction('MediaTrackPrevious', 227)).toBe('previous');
    expect(playerAction('MediaTrackNext', 226)).toBe('next');
  });
  it('maps Android back arrivals', () => {
    expect(playerAction('GoBack', 0)).toBe('back');
    expect(playerAction('BrowserBack', 0)).toBe('back');
    expect(playerAction('Escape', 27)).toBe('back');
  });
});
```

Append to `clients/tv/src/navigation.test.ts` (match the file's existing import style):

```ts
describe('remoteAction Android back arrivals', () => {
  it('treats GoBack, BrowserBack, and Escape as back', () => {
    expect(remoteAction('GoBack', 0)).toBe('back');
    expect(remoteAction('BrowserBack', 0)).toBe('back');
    expect(remoteAction('Escape', 27)).toBe('back');
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm run test -w @filelist/tv`
Expected: FAIL — the new cases return `null`.

- [ ] **Step 3: Implement the mappings**

In `clients/tv/src/player.ts`, add at the top of `playerAction`, before the existing checks (these key names cannot collide with Tizen keyCodes):

```ts
  if (key === 'MediaPlay') return 'play';
  if (key === 'MediaPause') return 'pause';
  if (key === 'MediaPlayPause') return 'play-pause';
  if (key === 'MediaStop') return 'stop';
  if (key === 'MediaRewind') return 'rewind';
  if (key === 'MediaFastForward') return 'fast-forward';
  if (key === 'MediaTrackPrevious') return 'previous';
  if (key === 'MediaTrackNext') return 'next';
  if (key === 'GoBack' || key === 'BrowserBack' || keyCode === 27) return 'back';
```

In `clients/tv/src/navigation.ts`, add at the top of `remoteAction`:

```ts
  if (key === 'GoBack' || key === 'BrowserBack' || keyCode === 27) return 'back';
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npm run build -w @filelist/tv`
Expected: full suite PASS (the `Escape → back` mapping is also what makes emulator/keyboard testing work later).

- [ ] **Step 5: Commit**

```bash
git add clients/tv/src/player.ts clients/tv/src/player.test.ts clients/tv/src/navigation.ts clients/tv/src/navigation.test.ts
git commit -m "feat(tv): map Android media and back keys in the shared key tables"
```

---

### Task 4: Platform exit and media-key registration module

**Files:**
- Create: `clients/tv/src/platform.ts`, `clients/tv/src/platform.test.ts`
- Modify: `clients/tv/src/main.tsx`, `clients/tv/src/globals.d.ts`

**Interfaces:**
- Consumes: `window.tizen` (guarded, existing), `window.FileListTVNative.exit()` (Android bridge, Task 7).
- Produces: `exitApplication(): void` (Tizen API first, then `FileListTVNative.exit()`), `registerMediaKeys(): void` (Tizen `tvinputdevice.registerKey`, guarded no-op elsewhere).

- [ ] **Step 1: Write the failing tests**

```ts
// clients/tv/src/platform.test.ts
import { afterEach, describe, expect, it } from 'vitest';
import { exitApplication, registerMediaKeys } from './platform';

const tizenWindow = (window as any);
afterEach(() => { delete tizenWindow.tizen; delete tizenWindow.FileListTVNative; });

describe('exitApplication', () => {
  it('exits through the Tizen API when present', () => {
    let exited = 0;
    tizenWindow.tizen = { application: { getCurrentApplication: () => ({ exit: () => { exited++; } }) } };
    exitApplication();
    expect(exited).toBe(1);
  });
  it('falls back to the native bridge on Android', () => {
    let exited = 0;
    tizenWindow.FileListTVNative = { exit: () => { exited++; } };
    exitApplication();
    expect(exited).toBe(1);
  });
  it('survives both channels being absent or throwing', () => {
    tizenWindow.tizen = { application: { getCurrentApplication: () => { throw new Error('no tizen'); } } };
    expect(() => exitApplication()).not.toThrow();
  });
});

describe('registerMediaKeys', () => {
  it('registers every media key through the Tizen input device', () => {
    const keys: string[] = [];
    tizenWindow.tizen = { tvinputdevice: { registerKey: (key: string) => keys.push(key) } };
    registerMediaKeys();
    expect(keys).toEqual(['MediaPlayPause', 'MediaPlay', 'MediaPause', 'MediaStop', 'MediaRewind', 'MediaFastForward', 'MediaTrackPrevious', 'MediaTrackNext']);
  });
  it('is a silent no-op without the Tizen API', () => {
    expect(() => registerMediaKeys()).not.toThrow();
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npm run test -w @filelist/tv -- platform`
Expected: FAIL — `./platform` cannot be resolved.

- [ ] **Step 3: Implement `platform.ts`**

```ts
// clients/tv/src/platform.ts
// The two exit/registration surfaces that differ per TV platform. Everything
// is feature-detected and guarded: Tizen APIs when present, the Android
// native bridge otherwise, silence when neither exists (spec Parity contract
// rule 2 — feature detection, never platform branching in UI code).
const MEDIA_KEYS = ['MediaPlayPause', 'MediaPlay', 'MediaPause', 'MediaStop', 'MediaRewind', 'MediaFastForward', 'MediaTrackPrevious', 'MediaTrackNext'];

export function exitApplication(): void {
  try { window.tizen?.application?.getCurrentApplication().exit(); return; } catch { }
  try { window.FileListTVNative?.exit(); } catch { }
}

export function registerMediaKeys(): void {
  for (const key of MEDIA_KEYS) {
    try { window.tizen?.tvinputdevice?.registerKey(key); } catch { }
  }
}
```

- [ ] **Step 4: Wire into `main.tsx`**

1. Delete the local `exitApplication` function (lines 26–28 of the current file: `function exitApplication() { try { window.tizen?…` and its body).
2. Add to imports: `import { exitApplication, registerMediaKeys } from './platform';`
3. Replace the media-key registration effect:

```ts
useEffect(() => { registerMediaKeys(); if (server) void connect(server); }, []);
```

- [ ] **Step 5: Extend `globals.d.ts`**

Read `clients/tv/src/globals.d.ts` (9 lines declaring `window.tizen`/`window.webapis`) and add alongside them:

```ts
interface Window {
  FileListTVNative?: {
    exit(): void;
    getIp(): string;
    getSubnetMask(): string;
    open(url: string): void;
    setDisplayRect(x: number, y: number, width: number, height: number): void;
    setDisplayMethod(mode: string): void;
    prepareAsync(successToken: string, errorToken: string): void;
    play(): void;
    pause(): void;
    seekTo(milliseconds: number): void;
    stop(): void;
    close(): void;
    getDuration(): number;
    getTotalTrackInfo(): string;
    setSelectTrack(type: string, index: number): void;
    setSilentSubtitle(silent: boolean): void;
  };
  FileListTVBridge?: { dispatch(payload: string): void };
}
```

(Merge into the existing `interface Window` declaration if one is already present rather than declaring it twice.)

- [ ] **Step 6: Run the suite and build**

Run: `npm run build -w @filelist/tv`
Expected: PASS, build clean.

- [ ] **Step 7: Commit**

```bash
git add clients/tv/src/platform.ts clients/tv/src/platform.test.ts clients/tv/src/main.tsx clients/tv/src/globals.d.ts
git commit -m "feat(tv): route exit and media-key registration through a platform module"
```

---

### Task 5: Server CORS middleware for locally-packaged clients

**Files:**
- Modify: `internal/adapters/httpapi/api.go` (middleware + wiring into `New`'s return)
- Test: `internal/adapters/httpapi/cors_test.go` (new)

**Interfaces:**
- Produces: `corsAPI(next http.Handler) http.Handler` — sets `Access-Control-Allow-Origin: *` on every `/api/v1*` response, answers `OPTIONS /api/v1*` preflights with 204 + `Access-Control-Allow-Methods: GET, POST, PUT, DELETE, HEAD, OPTIONS` + `Access-Control-Allow-Headers: Content-Type` + `Access-Control-Max-Age: 600`; everything else passes through untouched. `New(...)` returns `recoverer(log, access(log, trusted(settings, corsAPI(mux))))`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/adapters/httpapi/cors_test.go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func passThrough(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func TestCORSPreflightForAPIPaths(t *testing.T) {
	handler := corsAPI(http.HandlerFunc(passThrough))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/v1/settings", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Fatalf("Allow-Headers = %q, want Content-Type", got)
	}
}

func TestCORSHeadersOnAPIResponses(t *testing.T) {
	handler := corsAPI(http.HandlerFunc(passThrough))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Allow-Origin = %q, want *", got)
	}
}

func TestCORSLeavesNonAPIPathsUntouched(t *testing.T) {
	handler := corsAPI(http.HandlerFunc(passThrough))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Allow-Origin = %q, want empty", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 pass-through", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/adapters/httpapi/ -run TestCORS -v`
Expected: FAIL — `corsAPI` undefined.

- [ ] **Step 3: Implement the middleware and wire it**

In `internal/adapters/httpapi/api.go`, add near the other middleware helpers:

```go
// corsAPI lets locally-packaged TV clients (Tizen's widget runtime bypasses
// CORS; a WebView does not) call the API the server advertises on the home
// LAN. Preflights are answered here because the method-specific mux routes
// would otherwise drop OPTIONS on the SPA handler.
func corsAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1") {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, HEAD, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
```

Change `New`'s return statement (currently `return recoverer(log, access(log, trusted(settings, mux)))`) to:

```go
	return recoverer(log, access(log, trusted(settings, corsAPI(mux))))
```

- [ ] **Step 4: Run tests to verify they pass, then the whole package**

Run: `go test ./internal/adapters/httpapi/ -v`
Expected: new CORS tests PASS; all existing httpapi tests still PASS.

- [ ] **Step 5: Vet and commit**

```bash
go vet ./internal/adapters/httpapi/
git add internal/adapters/httpapi/api.go internal/adapters/httpapi/cors_test.go
git commit -m "feat(httpapi): allow cross-origin API calls for locally-packaged TV clients"
```

---

### Task 6: Android shell skeleton (Gradle, manifest, resources)

**Files:**
- Create: `clients/android-tv/settings.gradle.kts`, `clients/android-tv/build.gradle.kts`, `clients/android-tv/gradle.properties`, `clients/android-tv/app/build.gradle.kts`, `clients/android-tv/app/src/main/AndroidManifest.xml`, `clients/android-tv/app/src/main/java/com/torrenttv/app/MainActivity.kt` (placeholder), `clients/android-tv/app/src/main/res/values/{strings.xml,colors.xml,themes.xml}`, `clients/android-tv/app/src/main/res/drawable/{banner.xml,ic_launcher_foreground.xml}`, `clients/android-tv/app/src/main/res/mipmap-anydpi-v26/ic_launcher.xml`, `clients/android-tv/app/src/main/res/xml/network_security_config.xml`
- Modify: `.gitignore` (add `clients/android-tv/app/src/main/assets/www/`)

**Interfaces:**
- Produces: a buildable Gradle project `clients/android-tv` with application `com.torrenttv.app` (minSdk 26, targetSdk 35, versionName 0.1.0), LEANBACK_LAUNCHER entry, TorrentTV branding. Later tasks fill `MainActivity` and add bridge classes in `com.torrenttv.app`.

- [ ] **Step 1: Create the Gradle project files**

`clients/android-tv/settings.gradle.kts`:

```kotlin
pluginManagement {
    repositories { google(); mavenCentral(); gradlePluginPortal() }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories { google(); mavenCentral() }
}
rootProject.name = "TorrentTV"
include(":app")
```

`clients/android-tv/build.gradle.kts`:

```kotlin
plugins {
    id("com.android.application") version "8.7.3" apply false
    id("org.jetbrains.kotlin.android") version "2.0.21" apply false
}
```

`clients/android-tv/gradle.properties`:

```properties
org.gradle.jvmargs=-Xmx2048m -Dfile.encoding=UTF-8
android.useAndroidX=true
android.nonTransitiveRClass=true
```

`clients/android-tv/app/build.gradle.kts`:

```kotlin
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.torrenttv.app"
    compileSdk = 35
    defaultConfig {
        applicationId = "com.torrenttv.app"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
    }
    buildTypes {
        named("release") {
            isMinifyEnabled = false
            // Sideloading artifact: the debug key keeps `adb install` and
            // manual updates working without a distribution certificate,
            // mirroring the deliberately unsigned Tizen WGT.
            signingConfig = signingConfigs.getByName("debug")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

dependencies {
    implementation("androidx.media3:media3-exoplayer:1.5.1")
    implementation("androidx.webkit:webkit:1.12.1")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20240303")
}
```

- [ ] **Step 2: Create the manifest and resources**

`clients/android-tv/app/src/main/AndroidManifest.xml`:

```xml
<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.INTERNET" />
    <uses-permission android:name="android.permission.ACCESS_NETWORK_STATE" />
    <uses-permission android:name="android.permission.ACCESS_WIFI_STATE" />
    <uses-feature android:name="android.software.leanback" android:required="false" />
    <uses-feature android:name="android.hardware.touchscreen" android:required="false" />
    <application
        android:label="@string/app_name"
        android:icon="@mipmap/ic_launcher"
        android:banner="@drawable/banner"
        android:theme="@style/TorrentTVTheme"
        android:networkSecurityConfig="@xml/network_security_config"
        android:allowBackup="true">
        <activity
            android:name=".MainActivity"
            android:exported="true"
            android:screenOrientation="landscape"
            android:configChanges="orientation|screenSize|keyboardHidden|smallestScreenSize">
            <intent-filter>
                <action android:name="android.intent.action.MAIN" />
                <category android:name="android.intent.category.LAUNCHER" />
                <category android:name="android.intent.category.LEANBACK_LAUNCHER" />
            </intent-filter>
        </activity>
    </application>
</manifest>
```

`res/values/strings.xml`:

```xml
<resources>
    <string name="app_name">TorrentTV</string>
</resources>
```

`res/values/colors.xml`:

```xml
<resources>
    <color name="ink">#FF090D10</color>
    <color name="teal">#FF55DFC1</color>
</resources>
```

(Colors match the Tizen design tokens in `clients/tv/src/tv.css` — `--ink` and `--teal`.)

`res/values/themes.xml`:

```xml
<resources>
    <style name="TorrentTVTheme" parent="android:Theme.Material.NoActionBar.Fullscreen">
        <item name="android:windowBackground">@color/ink</item>
    </style>
</resources>
```

`res/drawable/banner.xml` (TV banner: ink field with the centered monogram icon):

```xml
<layer-list xmlns:android="http://schemas.android.com/apk/res/android">
    <item android:drawable="@color/ink" />
    <item android:drawable="@drawable/ic_launcher_foreground" android:gravity="center" />
</layer-list>
```

`res/drawable/ic_launcher_foreground.xml` (TT monogram, teal on transparent; the two T glyphs are bars and stems):

```xml
<vector xmlns:android="http://schemas.android.com/apk/res/android"
    android:width="108dp" android:height="108dp"
    android:viewportWidth="108" android:viewportHeight="108">
    <path android:fillColor="@color/teal"
        android:pathData="M24,36h28v8h-10v28h-8v-28h-10z M56,36h28v8h-10v28h-8v-28h-10z" />
</vector>
```

`res/mipmap-anydpi-v26/ic_launcher.xml`:

```xml
<adaptive-icon xmlns:android="http://schemas.android.com/apk/res/android">
    <background android:drawable="@color/ink" />
    <foreground android:drawable="@drawable/ic_launcher_foreground" />
</adaptive-icon>
```

`res/xml/network_security_config.xml` (the server is plain HTTP on the home LAN — discovery, API, SSE, and streams all need cleartext):

```xml
<network-security-config>
    <base-config cleartextTrafficPermitted="true">
        <trust-anchors>
            <certificates src="system" />
        </trust-anchors>
    </base-config>
</network-security-config>
```

- [ ] **Step 3: Placeholder MainActivity and .gitignore**

`clients/android-tv/app/src/main/java/com/torrenttv/app/MainActivity.kt`:

```kotlin
package com.torrenttv.app

import android.app.Activity
import android.os.Bundle

class MainActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
    }
}
```

Append to the repo root `.gitignore`:

```
clients/android-tv/app/src/main/assets/www/
clients/android-tv/.build/
clients/android-tv/.gradle/
```

- [ ] **Step 4: Generate the wrapper and build**

```bash
cd clients/android-tv
(gradle wrapper --gradle-version 8.9 || echo "install gradle first: brew install gradle")
./gradlew :app:assembleDebug
```

Expected: BUILD SUCCESSFUL; `app/build/outputs/apk/debug/app-debug.apk` exists.

- [ ] **Step 5: Commit**

```bash
git add -A clients/android-tv .gitignore
git commit -m "feat(android-tv): add the TorrentTV Gradle shell skeleton"
```

---

### Task 7: WebView shell — web-app sync, Android index, bridge shim, back key

**Files:**
- Create: `clients/android-tv/app/assets/index.html` (Android page variant), `clients/android-tv/app/assets/platform-bridge.js`, `clients/android-tv/app/src/main/java/com/torrenttv/app/BackKeys.kt`, `clients/android-tv/app/src/test/java/com/torrenttv/app/BackKeysTest.kt`
- Modify: `clients/android-tv/app/build.gradle.kts` (sync task), `clients/android-tv/app/src/main/java/com/torrenttv/app/MainActivity.kt` (replace placeholder)

**Interfaces:**
- Consumes: `clients/tv/dist` (Task 1), `window.FileListTVIdentity` / `window.FileListTVNative` / `window.FileListTVBridge` (Tasks 2, 4, 9).
- Produces: `platform-bridge.js` defining `window.webapis.avplay` (AVPlay-shaped wrapper over `FileListTVNative`, with listener/callback dispatch through `window.FileListTVBridge.dispatch`) and `window.webapis.network` (`getIp`/`getSubnetMask`); `assets/www/` synced at build time; `BackKeys.downScript()/upScript()` producing the synthetic key events the JS key tables already understand (keyCode 10009).

- [ ] **Step 1: Write the failing test for the back-key scripts**

```kotlin
// clients/android-tv/app/src/test/java/com/torrenttv/app/BackKeysTest.kt
package com.torrenttv.app

import org.junit.Assert.assertTrue
import org.junit.Test

class BackKeysTest {
    @Test fun `down script dispatches the Tizen back key`() {
        val script = BackKeys.downScript()
        assertTrue(script.contains("keydown"))
        assertTrue(script.contains("XF86Back"))
        assertTrue(script.contains("10009"))
    }
    @Test fun `up script dispatches the matching keyup`() {
        val script = BackKeys.upScript()
        assertTrue(script.contains("keyup"))
        assertTrue(script.contains("10009"))
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd clients/android-tv && ./gradlew :app:testDebugUnitTest`
Expected: FAIL — `BackKeys` unresolved.

- [ ] **Step 3: Implement `BackKeys.kt`**

```kotlin
package com.torrenttv.app

/**
 * Synthetic key events that forward the Android BACK key into the page as the
 * Tizen Return key (keyCode 10009), so the web app's long-press-to-exit and
 * back-stack behavior run unchanged. keydown starts the page's 5 s exit
 * timer; keyup inside the window fires the ordinary back action.
 */
object BackKeys {
    fun downScript(): String = keyScript("keydown")
    fun upScript(): String = keyScript("keyup")

    private fun keyScript(type: String): String =
        "(function(){document.dispatchEvent(new KeyboardEvent('$type'," +
            "{key:'XF86Back',keyCode:10009,which:10009,bubbles:true,cancelable:true}));})();"
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd clients/android-tv && ./gradlew :app:testDebugUnitTest`
Expected: PASS.

- [ ] **Step 5: Create the Android page variant**

`clients/android-tv/app/assets/index.html` — same DOM as `clients/tv/index.html` (startup splash, fatal-error + boot scripts) with three deltas: no `$WEBAPIS` script tag, TorrentTV branding, `platform-bridge.js` loaded before `app.js`:

```html
<!doctype html>
<html>
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=1920,height=1080">
    <meta http-equiv="Content-Security-Policy" content="default-src 'self' data: blob: http: https:; script-src 'self'; connect-src http: https:; media-src http: https: blob:; style-src 'self' 'unsafe-inline'">
    <title>TorrentTV</title>
    <style>
      html,body{margin:0;width:100%;height:100%;background:#071018;color:#f5f8fa;font-family:Arial,sans-serif}
      #startup{position:fixed;top:0;right:0;bottom:0;left:0;display:flex;flex-direction:column;align-items:center;justify-content:center;padding:100px;box-sizing:border-box;text-align:center;background:#071018;z-index:100}
      #startup h1{font-size:64px;margin:0 0 26px;color:#52d3a2}#startup-message{font-size:28px;line-height:1.5;white-space:pre-wrap;max-width:1500px}
    </style>
    <link rel="stylesheet" href="app.css">
  </head>
  <body>
    <div id="startup"><h1>TorrentTV</h1><div id="startup-message">Starting application…</div></div>
    <div id="app"></div>
    <script type="text/javascript" src="fatal-error.js"></script>
    <script type="text/javascript" src="startup.js"></script>
    <script type="text/javascript" src="platform-bridge.js"></script>
    <script type="text/javascript" src="app.js"></script>
  </body>
</html>
```

- [ ] **Step 6: Create `platform-bridge.js`**

`clients/android-tv/app/assets/platform-bridge.js`:

```js
(function () {
  'use strict';
  // Android shell glue for the shared TV web app. This file, plus the page
  // above, are the only Android-specific bytes in the package; app.js/app.css
  // ship byte-identical to the Tizen WGT (spec: Parity contract rule 1).
  window.FileListTVIdentity = { name: 'TorrentTV', monogram: 'TT' };

  var native = window.FileListTVNative || null;
  var listener = null;
  var callbacks = {};

  window.FileListTVBridge = {
    // Entry point for Kotlin's evaluateJavascript callbacks: player events
    // route to the registered AVPlay listener, prepare results to their
    // tokens.
    dispatch: function (payload) {
      var event;
      try { event = JSON.parse(payload); } catch (error) { return; }
      if (event.kind === 'event' && listener && typeof listener[event.name] === 'function') {
        try { listener[event.name].apply(null, event.args || []); } catch (error) { }
        return;
      }
      if (event.kind === 'callback' && typeof callbacks[event.token] === 'function') {
        var callback = callbacks[event.token];
        delete callbacks[event.token];
        try { callback.apply(null, event.args || []); } catch (error) { }
      }
    }
  };

  function registerCallback(callback) {
    var token = 'cb' + Math.random().toString(36).slice(2);
    callbacks[token] = callback;
    return token;
  }

  // Same shape as Samsung's webapis.avplay: the Player component in app.js
  // programs against this API and must not know the difference.
  var avplay = {
    open: function (url) { native.open(String(url)); },
    setDisplayRect: function (x, y, w, h) { native.setDisplayRect(Number(x), Number(y), Number(w), Number(h)); },
    setDisplayMethod: function (mode) { native.setDisplayMethod(String(mode)); },
    setListener: function (value) { listener = value || null; },
    prepareAsync: function (onSuccess, onError) {
      native.prepareAsync(registerCallback(onSuccess), registerCallback(onError));
    },
    play: function () { native.play(); },
    pause: function () { native.pause(); },
    seekTo: function (ms) { native.seekTo(Number(ms)); },
    stop: function () { native.stop(); },
    close: function () { native.close(); },
    getDuration: function () { return Number(native.getDuration()) || 0; },
    getTotalTrackInfo: function () {
      try { return JSON.parse(native.getTotalTrackInfo()); } catch (error) { return []; }
    },
    setSelectTrack: function (type, index) { native.setSelectTrack(String(type), Number(index)); },
    // Server-prepared VTT subtitles render in the page's HTML overlay, and
    // the overlay applies its own delay, so these two are deliberate no-ops.
    setSilentSubtitle: function (value) { native.setSilentSubtitle(Boolean(value)); },
    setExternalSubtitlePath: function () { },
    setSubtitlePosition: function () { }
  };

  window.webapis = {
    avplay: avplay,
    network: {
      getIp: function () { return native ? String(native.getIp() || '') : ''; },
      getSubnetMask: function () { return native ? String(native.getSubnetMask() || '') : ''; }
    }
  };

  // Video plays on a native SurfaceView behind the WebView; while the player
  // is on screen the page must go transparent where the video should show
  // (the Tizen engine composites AVPlay inside the object element natively;
  // a WebView does not). Player mode is detected from the DOM, not from UI
  // code, so app.js stays platform-neutral.
  function watchPlayerMode() {
    var style = document.createElement('style');
    style.textContent = 'html.video-behind,html.video-behind body{background:transparent !important}' +
      'html.video-behind .player-shell{background:transparent !important}';
    document.head.appendChild(style);
    var observer = new MutationObserver(function () {
      document.documentElement.classList.toggle('video-behind', Boolean(document.querySelector('.player-shell')));
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }
  if (document.body) watchPlayerMode();
  else document.addEventListener('DOMContentLoaded', watchPlayerMode);
}());
```

- [ ] **Step 7: Add the web-app sync task and wire the real MainActivity**

Append inside the `android { }` block is not needed; add at the bottom of `clients/android-tv/app/build.gradle.kts`:

```kotlin
// Sync the shared TV web app into WebView assets. index.html is the Android
// page variant (TorrentTV branding, no $WEBAPIS tag, platform-bridge.js);
// everything else — app.js, app.css, boot scripts — ships byte-identical to
// the Tizen WGT.
val syncWebApp = tasks.register<Copy>("syncWebApp") {
    from("../../tv/dist") { exclude("index.html") }
    from("assets/index.html")
    from("assets/platform-bridge.js")
    into("src/main/assets/www")
}
tasks.named("preBuild") { dependsOn(syncWebApp) }
```

Replace `MainActivity.kt`:

```kotlin
package com.torrenttv.app

import android.annotation.SuppressLint
import android.app.Activity
import android.graphics.Color
import android.os.Bundle
import android.util.Log
import android.view.KeyEvent
import android.view.WindowManager
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.webkit.RenderProcessGoneDetail
import androidx.webkit.WebViewAssetLoader
import androidx.webkit.WebViewClientCompat

class MainActivity : Activity() {
    private lateinit var webView: WebView

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        webView = WebView(this)
        // Transparent wherever the page is transparent: the video surface
        // (Task 9) shows through the player shell exactly like Tizen's
        // AVPlay plane shows through the object element.
        webView.setBackgroundColor(Color.TRANSPARENT)
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        webView.settings.mediaPlaybackRequiresUserGesture = false
        // The page origin is https (appassets domain) and the LAN server is
        // http: without this, mixed-content blocking kills every API call.
        webView.settings.mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
        val loader = WebViewAssetLoader.Builder()
            .addPathHandler("/assets/", WebViewAssetLoader.AssetsPathHandler(this))
            .build()
        webView.webViewClient = object : WebViewClientCompat() {
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? =
                loader.shouldInterceptRequest(request.url)

            override fun onPageFinished(view: WebView, url: String) {
                Log.i("TorrentTV", "ready")
            }

            // The client never fails silently (spec): a dead renderer
            // restarts the shell instead of leaving a black screen.
            override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                Log.e("TorrentTV", "render process gone; restarting the shell")
                recreate()
                return true
            }
        }
        webView.addJavascriptInterface(Bridge(this), "FileListTVNative")
        setContentView(webView)
        webView.loadUrl("https://appassets.androidplatform.net/assets/www/index.html")
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent?): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            if (event?.repeatCount == 0) webView.evaluateJavascript(BackKeys.downScript(), null)
            return true
        }
        return super.onKeyDown(keyCode, event)
    }

    override fun onKeyUp(keyCode: Int, event: KeyEvent?): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            webView.evaluateJavascript(BackKeys.upScript(), null)
            return true
        }
        return super.onKeyUp(keyCode, event)
    }
}
```

Add a temporary `Bridge.kt` (Task 8 completes it, Task 9 adds the player methods):

```kotlin
package com.torrenttv.app

import android.webkit.JavascriptInterface

class Bridge(private val activity: MainActivity) {
    @JavascriptInterface
    fun exit() {
        activity.runOnUiThread { activity.finish() }
    }
}
```

- [ ] **Step 8: Sync, build, and sanity-check the sync output**

```bash
cd clients/android-tv
./gradlew :app:syncWebApp :app:testDebugUnitTest :app:assembleDebug
cmp ../tv/dist/app.js app/src/main/assets/www/app.js && echo "bundle identical"
grep -c "webapis" app/src/main/assets/www/index.html || true   # 0: Android page has no $WEBAPIS
grep -q "TorrentTV" app/src/main/assets/www/index.html && echo "branded"
```

Expected: tests PASS, BUILD SUCCESSFUL, "bundle identical", "branded".

- [ ] **Step 9: Commit**

```bash
git add -A clients/android-tv
git commit -m "feat(android-tv): run the shared TV web app in a WebView with a platform bridge"
```

---

### Task 8: Network info bridge (LAN discovery)

**Files:**
- Create: `clients/android-tv/app/src/main/java/com/torrenttv/app/NetworkInfo.kt`, `clients/android-tv/app/src/test/java/com/torrenttv/app/SubnetMaskTest.kt`
- Modify: `clients/android-tv/app/src/main/java/com/torrenttv/app/Bridge.kt`, `MainActivity.kt` (construction)

**Interfaces:**
- Consumes: `NetworkInfoSource` (produced here), `Bridge` (Task 7).
- Produces: `interface NetworkInfoSource { fun ip(): String?; fun subnetMask(): String? }`; `LinkNetworkInfo(context)` implementation (ConnectivityManager primary, WifiManager DhcpInfo fallback); `SubnetMask.forPrefix(prefixLength: Int): String`; `Bridge(activity, network)` with `@JavascriptInterface getIp()/getSubnetMask()` returning `""` when unavailable (the web app degrades to manual address entry).

- [ ] **Step 1: Write the failing test**

```kotlin
// clients/android-tv/app/src/test/java/com/torrenttv/app/SubnetMaskTest.kt
package com.torrenttv.app

import org.junit.Assert.assertEquals
import org.junit.Test

class SubnetMaskTest {
    @Test fun `prefix 24 renders the familiar mask`() = assertEquals("255.255.255.0", SubnetMask.forPrefix(24))
    @Test fun `prefix 16 renders a class B mask`() = assertEquals("255.255.0.0", SubnetMask.forPrefix(16))
    @Test fun `prefix 32 is the host mask`() = assertEquals("255.255.255.255", SubnetMask.forPrefix(32))
    @Test fun `prefix 0 is the zero mask`() = assertEquals("0.0.0.0", SubnetMask.forPrefix(0))
    @Test fun `out of range prefixes clamp instead of throwing`() {
        assertEquals("255.255.255.255", SubnetMask.forPrefix(99))
        assertEquals("0.0.0.0", SubnetMask.forPrefix(-3))
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd clients/android-tv && ./gradlew :app:testDebugUnitTest`
Expected: FAIL — `SubnetMask` unresolved.

- [ ] **Step 3: Implement `NetworkInfo.kt`**

```kotlin
package com.torrenttv.app

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.wifi.WifiManager

/** Pure mask math so the discovery subnet derives testably from a prefix. */
object SubnetMask {
    fun forPrefix(prefixLength: Int): String {
        val prefix = prefixLength.coerceIn(0, 32)
        val mask = if (prefix == 0) 0 else (-1 shl (32 - prefix))
        return listOf(mask ushr 24 and 0xff, mask ushr 16 and 0xff, mask ushr 8 and 0xff, mask and 0xff)
            .joinToString(".")
    }
}

interface NetworkInfoSource {
    fun ip(): String?
    fun subnetMask(): String?
}

/**
 * What the Setup screen's LAN scan needs: this device's IPv4 address and
 * subnet mask. ConnectivityManager link properties are the API-correct
 * source on both Wi-Fi and Ethernet (most 2018+ TVs use Ethernet); the
 * legacy WifiManager DHCP block is the fallback for builds that report no
 * link address. Nulls mean "unknown" and the page degrades to manual entry.
 */
class LinkNetworkInfo(context: Context) : NetworkInfoSource {
    private val connectivity = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
    private val wifi = context.applicationContext.getSystemService(Context.WIFI_SERVICE) as? WifiManager

    override fun ip(): String? = linkAddress()?.address?.hostAddress ?: dhcp()?.let { dhcpIntToDotted(it.ipAddress) }

    override fun subnetMask(): String? {
        linkAddress()?.let { address -> return SubnetMask.forPrefix(address.prefixLength) }
        val mask = dhcp()?.netmask ?: return null
        return dhcpIntToDotted(mask)
    }

    private fun linkAddress(): android.net.LinkAddress? {
        val properties: LinkProperties = connectivity.getLinkProperties(connectivity.activeNetwork) ?: return null
        return properties.linkAddresses.firstOrNull { it.address.address.size == 4 }
    }

    private fun dhcp(): android.net.wifi.DhcpInfo? = wifi?.dhcpInfo

    private fun dhcpIntToDotted(value: Int): String =
        listOf(value and 0xff, value ushr 8 and 0xff, value ushr 16 and 0xff, value ushr 24 and 0xff)
            .joinToString(".")
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd clients/android-tv && ./gradlew :app:testDebugUnitTest`
Expected: PASS.

- [ ] **Step 5: Extend `Bridge.kt` and construct it with a network source**

`Bridge.kt`:

```kotlin
package com.torrenttv.app

import android.webkit.JavascriptInterface

class Bridge(private val activity: MainActivity, private val network: NetworkInfoSource) {
    @JavascriptInterface
    fun exit() {
        activity.runOnUiThread { activity.finish() }
    }

    // Empty strings mean "unknown" — Setup falls back to the manual address
    // field, exactly like the Tizen path when webapis.network is missing.
    @JavascriptInterface
    fun getIp(): String = network.ip() ?: ""

    @JavascriptInterface
    fun getSubnetMask(): String = network.subnetMask() ?: ""
}
```

In `MainActivity.kt`, change the registration line to:

```kotlin
webView.addJavascriptInterface(Bridge(this, LinkNetworkInfo(this)), "FileListTVNative")
```

- [ ] **Step 6: Build and commit**

```bash
cd clients/android-tv && ./gradlew :app:testDebugUnitTest :app:assembleDebug
git add -A clients/android-tv
git commit -m "feat(android-tv): expose network info to the LAN discovery bridge"
```

---

### Task 9: ExoPlayer-backed AVPlay bridge and the video surface

**Files:**
- Create: `clients/android-tv/app/src/main/java/com/torrenttv/app/avplay/AvPlayStateMapper.kt`, `…/avplay/TrackInfo.kt`, `…/avplay/AvPlayBridge.kt`
- Test: `clients/android-tv/app/src/test/java/com/torrenttv/app/avplay/AvPlayStateMapperTest.kt`, `…/TrackInfoTest.kt`
- Modify: `clients/android-tv/app/src/main/java/com/torrenttv/app/Bridge.kt`, `MainActivity.kt` (FrameLayout: SurfaceView behind the WebView)

**Interfaces:**
- Consumes: `window.FileListTVBridge.dispatch` (Task 7), media3 `ExoPlayer`.
- Produces: the native half of the AVPlay shape — `open/setDisplayRect/setDisplayMethod/prepareAsync/play/pause/seekTo/stop/close/getDuration/getTotalTrackInfo/setSelectTrack/setSilentSubtitle` on `Bridge`; events (`onbufferingstart`, `onbufferingprogress`, `onbufferingcomplete`, `onstreamcompleted`, `oncurrentplaytime`, `onerror`) dispatched to the JS-registered listener; `AvPlayStateMapper.bufferingCallback(state)` and `TrackInfo.toJson(tracks)` as pure, unit-tested helpers.

- [ ] **Step 1: Write the failing tests**

```kotlin
// clients/android-tv/app/src/test/java/com/torrenttv/app/avplay/AvPlayStateMapperTest.kt
package com.torrenttv.app.avplay

import androidx.media3.common.Player
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class AvPlayStateMapperTest {
    @Test fun `buffering maps to onbufferingstart`() = assertEquals("onbufferingstart", AvPlayStateMapper.bufferingCallback(Player.STATE_BUFFERING))
    @Test fun `ready maps to onbufferingcomplete`() = assertEquals("onbufferingcomplete", AvPlayStateMapper.bufferingCallback(Player.STATE_READY))
    @Test fun `ended maps to onstreamcompleted`() = assertEquals("onstreamcompleted", AvPlayStateMapper.bufferingCallback(Player.STATE_ENDED))
    @Test fun `idle maps to nothing`() = assertNull(AvPlayStateMapper.bufferingCallback(Player.STATE_IDLE))
}
```

```kotlin
// clients/android-tv/app/src/test/java/com/torrenttv/app/avplay/TrackInfoTest.kt
package com.torrenttv.app.avplay

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class TrackInfoTest {
    @Test fun `serializes to the AVPlay total-track-info shape`() {
        val json = TrackInfo.toJson(listOf(
            TrackInfo.Track(type = "AUDIO", language = "eng", codec = "audio/eac3"),
            TrackInfo.Track(type = "TEXT", language = "ron", codec = "text/vtt"),
        ))
        val parsed = org.json.JSONArray(json)
        assertEquals(2, parsed.length())
        val audio = parsed.getJSONObject(0)
        assertEquals("AUDIO", audio.getString("type"))
        assertEquals(0, audio.getInt("index"))
        val extra = org.json.JSONObject(audio.getString("extra_info"))
        assertEquals("eng", extra.getString("track_lang"))
        assertEquals("audio/eac3", extra.optString("codec"))
        val text = parsed.getJSONObject(1)
        assertEquals("TEXT", text.getString("type"))
        assertEquals(1, text.getInt("index"))
        assertTrue(text.getString("extra_info").contains("ron"))
    }
    @Test fun `empty track list serializes to an empty array`() = assertEquals("[]", TrackInfo.toJson(emptyList()))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd clients/android-tv && ./gradlew :app:testDebugUnitTest`
Expected: FAIL — `AvPlayStateMapper`/`TrackInfo` unresolved.

- [ ] **Step 3: Implement the pure helpers**

```kotlin
// clients/android-tv/app/src/main/java/com/torrenttv/app/avplay/AvPlayStateMapper.kt
package com.torrenttv.app.avplay

import androidx.media3.common.Player

/** ExoPlayer playback state to the AVPlay listener callback name. */
object AvPlayStateMapper {
    fun bufferingCallback(state: Int): String? = when (state) {
        Player.STATE_BUFFERING -> "onbufferingstart"
        Player.STATE_READY -> "onbufferingcomplete"
        Player.STATE_ENDED -> "onstreamcompleted"
        else -> null
    }
}
```

```kotlin
// clients/android-tv/app/src/main/java/com/torrenttv/app/avplay/TrackInfo.kt
package com.torrenttv.app.avplay

import org.json.JSONArray
import org.json.JSONObject

/**
 * Serializes tracks into the shape `normalizeTrack` in the web app parses:
 * a JSON array of { index, type, extra_info }, where extra_info is itself a
 * JSON string carrying track_lang and codec.
 */
object TrackInfo {
    data class Track(val type: String, val language: String, val codec: String)

    fun toJson(tracks: List<Track>): String {
        val array = JSONArray()
        tracks.forEachIndexed { index, track ->
            val extra = JSONObject()
            extra.put("track_lang", track.language)
            if (track.codec.isNotEmpty()) extra.put("codec", track.codec)
            array.put(JSONObject().put("index", index).put("type", track.type).put("extra_info", extra.toString()))
        }
        return array.toString()
    }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd clients/android-tv && ./gradlew :app:testDebugUnitTest`
Expected: PASS.

- [ ] **Step 5: Implement `AvPlayBridge.kt`**

```kotlin
package com.torrenttv.app.avplay

import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.view.SurfaceView
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.trackselection.TrackSelectionOverride
import org.json.JSONArray
import org.json.JSONObject

/**
 * The native half of the AVPlay shape: receives the web app's player
 * commands through Bridge, drives one ExoPlayer on a SurfaceView behind the
 * WebView, and reports progress back as AVPlay listener events through
 * `dispatch` (Kotlin → FileListTVBridge.dispatch → JS listener).
 *
 * Everything stateful runs on the main thread; @JavascriptInterface calls
 * arrive on the WebView's binder thread and are posted. Subtitles render in
 * the page's HTML overlay from server-prepared VTT, so text tracks stay
 * disabled and the subtitle-path/position calls are accepted as no-ops.
 */
class AvPlayBridge(private val surface: SurfaceView, private val dispatch: (String) -> Unit) {
    private val main = Handler(Looper.getMainLooper())
    private var player: ExoPlayer? = null
    private var sourceUrl: String? = null
    private var successToken: String? = null
    private var errorToken: String? = null
    private var lastDurationMs = 0L
    private var buffering = false
    private var ticker: Runnable? = null

    // -- commands (called on the JS bridge thread) --
    fun open(url: String) { sourceUrl = url }

    fun setDisplayRect(x: Int, y: Int, width: Int, height: Int) { /* the surface fills the activity */ }

    fun setDisplayMethod(mode: String) {
        onMain {
            player?.videoScalingMode = when (mode) {
                "PLAYER_DISPLAY_MODE_FULL_SCREEN" -> C.VIDEO_SCALING_MODE_SCALE_TO_FIT_WITH_CROPPING
                else -> C.VIDEO_SCALING_MODE_SCALE_TO_FIT
            }
        }
    }

    fun prepareAsync(success: String, error: String) {
        onMain {
            successToken = success
            errorToken = error
            releasePlayer()
            val url = sourceUrl
            if (url.isNullOrEmpty()) {
                reportError("no source URL was opened")
                return@onMain
            }
            val exo = ExoPlayer.Builder(surface.context).build()
            player = exo
            exo.setVideoSurfaceView(surface)
            exo.addListener(object : Player.Listener {
                override fun onPlaybackStateChanged(state: Int) {
                    AvPlayStateMapper.bufferingCallback(state)?.let { name ->
                        if (state == Player.STATE_BUFFERING) {
                            buffering = true
                            dispatchEvent(name, listOf(0))
                        } else {
                            dispatchEvent(name)
                        }
                    }
                    if (state == Player.STATE_READY) {
                        buffering = false
                        lastDurationMs = exo.duration.coerceAtLeast(0L)
                        successToken?.let { token -> dispatchCallback(token, listOf(lastDurationMs)) }
                        successToken = null
                        errorToken = null
                        startTicker(exo)
                    }
                }
                override fun onPlayerError(error: PlaybackException) {
                    reportError(error.message ?: "playback error")
                }
            })
            exo.setMediaItem(MediaItem.fromUri(Uri.parse(url)))
            exo.prepare()
        }
    }

    fun play() = withPlayer { it.play() }
    fun pause() = withPlayer { it.pause() }
    fun seekTo(milliseconds: Double) = withPlayer { it.seekTo(milliseconds.toLong()) }
    fun stop() = withPlayer { it.stop() }

    fun close() {
        onMain { releasePlayer() }
    }

    fun getDuration(): Double = lastDurationMs.toDouble()

    fun getTotalTrackInfo(): String {
        val exo = player ?: return "[]"
        return TrackInfo.toJson(tracksOf(exo))
    }

    fun setSelectTrack(type: String, index: Int) {
        if (type != "AUDIO") return
        withPlayer { exo ->
            var audioIndex = 0
            for (group in exo.currentTracks.groups) {
                if (group.type != C.TRACK_TYPE_AUDIO) continue
                for (trackIndex in 0 until group.mediaTrackGroup.length) {
                    if (audioIndex == index) {
                        exo.trackSelectionParameters = exo.trackSelectionParameters.buildUpon()
                            .setOverrideForType(TrackSelectionOverride(group.mediaTrackGroup, trackIndex))
                            .build()
                        return@withPlayer
                    }
                    audioIndex++
                }
            }
        }
    }

    fun setSilentSubtitle(silent: Boolean) {
        onMain {
            player?.trackSelectionParameters = player?.trackSelectionParameters?.buildUpon()
                ?.setTrackTypeDisabled(C.TRACK_TYPE_TEXT, silent)
                ?.build()
        }
    }

    // -- internals (main thread) --
    private fun tracksOf(exo: ExoPlayer): List<TrackInfo.Track> {
        val tracks = mutableListOf<TrackInfo.Track>()
        for (group in exo.currentTracks.groups) {
            val format = group.mediaTrackGroup.getFormat(0)
            val type = when (group.type) {
                C.TRACK_TYPE_AUDIO -> "AUDIO"
                C.TRACK_TYPE_TEXT -> "TEXT"
                C.TRACK_TYPE_VIDEO -> "VIDEO"
                else -> continue
            }
            tracks.add(TrackInfo.Track(type, format.language ?: "", format.sampleMimeType ?: ""))
        }
        return tracks
    }

    private fun startTicker(exo: ExoPlayer) {
        stopTicker()
        val runnable = object : Runnable {
            override fun run() {
                val current = player
                if (current == null || current !== exo) return
                dispatchEvent("oncurrentplaytime", listOf(current.currentPosition))
                if (buffering) dispatchEvent("onbufferingprogress", listOf((current.bufferedPercentage * 100).toInt()))
                main.postDelayed(this, 500)
            }
        }
        ticker = runnable
        main.postDelayed(runnable, 500)
    }

    private fun stopTicker() {
        ticker?.let { main.removeCallbacks(it) }
        ticker = null
    }

    private fun releasePlayer() {
        stopTicker()
        player?.release()
        player = null
    }

    private fun reportError(message: String) {
        val token = errorToken
        successToken = null
        errorToken = null
        if (token != null) dispatchCallback(token, listOf(message))
        else dispatchEvent("onerror", listOf(message))
    }

    private fun dispatchEvent(name: String, args: List<Any> = emptyList()) {
        val payload = JSONObject().put("kind", "event").put("name", name).put("args", JSONArray(args))
        dispatch(payload.toString())
    }

    private fun dispatchCallback(token: String, args: List<Any>) {
        val payload = JSONObject().put("kind", "callback").put("token", token).put("args", JSONArray(args))
        dispatch(payload.toString())
    }

    private fun onMain(block: () -> Unit) {
        if (Looper.myLooper() == Looper.getMainLooper()) block() else main.post(block)
    }

    private fun withPlayer(block: (ExoPlayer) -> Unit) {
        onMain { player?.let(block) }
    }
}
```

Note for the implementer: `prepareAsync` passes the duration to the success callback; `platform-bridge.js` forwards callback args, and the Player component re-reads duration via `getDuration()` in its prepare callback, so the arg list is informational.

- [ ] **Step 6: Wire the surface and bridge into MainActivity and Bridge**

`MainActivity.kt` — replace the `setContentView(webView)` region and add the surface:

```kotlin
        val surface = SurfaceView(this)
        val layout = FrameLayout(this)
        layout.addView(surface, FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT))
        layout.addView(webView, FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT))
        setContentView(layout)
```

(Add imports `android.view.SurfaceView`, `android.widget.FrameLayout`.) The WebView must remain on top; keep `webView.setBackgroundColor(Color.TRANSPARENT)`.

`Bridge.kt` — add the player methods, constructing one `AvPlayBridge` whose `dispatch` evaluates JS on the UI thread:

```kotlin
package com.torrenttv.app

import android.webkit.JavascriptInterface
import com.torrenttv.app.avplay.AvPlayBridge

class Bridge(
    private val activity: MainActivity,
    private val network: NetworkInfoSource,
    private val avplay: AvPlayBridge,
) {
    @JavascriptInterface
    fun exit() {
        activity.runOnUiThread { activity.finish() }
    }

    @JavascriptInterface
    fun getIp(): String = network.ip() ?: ""

    @JavascriptInterface
    fun getSubnetMask(): String = network.subnetMask() ?: ""

    // AVPlay-shaped player commands; see avplay/AvPlayBridge.kt.
    @JavascriptInterface fun open(url: String) = avplay.open(url)
    @JavascriptInterface fun setDisplayRect(x: Int, y: Int, width: Int, height: Int) = avplay.setDisplayRect(x, y, width, height)
    @JavascriptInterface fun setDisplayMethod(mode: String) = avplay.setDisplayMethod(mode)
    @JavascriptInterface fun prepareAsync(successToken: String, errorToken: String) = avplay.prepareAsync(successToken, errorToken)
    @JavascriptInterface fun play() = avplay.play()
    @JavascriptInterface fun pause() = avplay.pause()
    @JavascriptInterface fun seekTo(milliseconds: Double) = avplay.seekTo(milliseconds)
    @JavascriptInterface fun stop() = avplay.stop()
    @JavascriptInterface fun close() = avplay.close()
    @JavascriptInterface fun getDuration(): Double = avplay.getDuration()
    @JavascriptInterface fun getTotalTrackInfo(): String = avplay.getTotalTrackInfo()
    @JavascriptInterface fun setSelectTrack(type: String, index: Int) = avplay.setSelectTrack(type, index)
    @JavascriptInterface fun setSilentSubtitle(silent: Boolean) = avplay.setSilentSubtitle(silent)
}
```

In `MainActivity.kt`, replace the `addJavascriptInterface` line:

```kotlin
        val avplayBridge = AvPlayBridge(surface) { script ->
            runOnUiThread { webView.evaluateJavascript(script, null) }
        }
        webView.addJavascriptInterface(Bridge(this, LinkNetworkInfo(this), avplayBridge), "FileListTVNative")
```

Order matters: the surface must exist before the bridge is constructed. Add import `com.torrenttv.app.avplay.AvPlayBridge`.

- [ ] **Step 7: Build everything**

Run: `cd clients/android-tv && ./gradlew :app:testDebugUnitTest :app:assembleDebug`
Expected: PASS, BUILD SUCCESSFUL.

- [ ] **Step 8: Commit**

```bash
git add -A clients/android-tv
git commit -m "feat(android-tv): drive playback with an ExoPlayer-backed AVPlay bridge"
```

---

### Task 10: Packaging script and Makefile target

**Files:**
- Create: `clients/android-tv/scripts/package.sh` (executable)
- Modify: `Makefile` (new `torrenttv-apk` target + `.PHONY` + help text), `.github/workflows/release.yml` (upload the APK with the existing artifacts)

**Interfaces:**
- Produces: `make torrenttv-apk` → `clients/android-tv/.build/artifacts/TorrentTV-<version>.apk` + `.sha256`, mirroring the Tizen artifact layout.

- [ ] **Step 1: Create the packaging script**

`clients/android-tv/scripts/package.sh`:

```sh
#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$root"
version=$(sed -n 's/.*versionName = "\([^"]*\)".*/\1/p' app/build.gradle.kts | head -1)
[ -n "$version" ] || { echo "could not read versionName from app/build.gradle.kts" >&2; exit 1; }
./gradlew --no-daemon :app:syncWebApp :app:assembleRelease
mkdir -p "$root/.build/artifacts"
cp "$root/app/build/outputs/apk/release/app-release.apk" "$root/.build/artifacts/TorrentTV-$version.apk"
(cd "$root/.build/artifacts" && sha256sum "TorrentTV-$version.apk" > "TorrentTV-$version.apk.sha256")
echo "$root/.build/artifacts/TorrentTV-$version.apk"
```

```bash
chmod +x clients/android-tv/scripts/package.sh
```

- [ ] **Step 2: Add the Makefile target**

In the root `Makefile`, extend the `.PHONY` line with `torrenttv-apk` and add (next to the `tizen-wgt` target, with a `##` help comment in the same style):

```make
## torrenttv-apk: build the TorrentTV Android TV APK -> clients/android-tv/.build/artifacts/
torrenttv-apk:
	clients/android-tv/scripts/package.sh
```

(Remember Makefile tabs, not spaces.)

- [ ] **Step 3: Run it**

```bash
make torrenttv-apk
ls -la clients/android-tv/.build/artifacts/
```

Expected: `TorrentTV-0.1.0.apk` and `TorrentTV-0.1.0.apk.sha256` exist.

- [ ] **Step 4: Publish alongside existing release artifacts**

In `.github/workflows/release.yml`, the build job runs `make tizen-wgt validate-tizen-wgt` (near line 47) and then uploads `clients/tizen/.build/artifacts/*.wgt` with the pinned `actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02`. Add the APK in the same job, immediately after the WGT upload step, reusing the pinned SHA and the same Java setup style the job already uses:

```yaml
      - name: Pack TorrentTV APK
        run: make torrenttv-apk
      - name: Upload TorrentTV APK artifact
        uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4
        with:
          # Match the argument names used by the WGT upload step above
          # (name + path); the path is the Android artifact directory.
          name: torrenttv-apk
          path: clients/android-tv/.build/artifacts/*
```

Then find the release job's expected-assets list (the section near line 409 that enumerates `FileListTV-${version}.wgt` before `gh release create "${GITHUB_REF_NAME}" release/*`) and add the two Android entries to it:

```yaml
            "TorrentTV-${version}.apk"
            "TorrentTV-${version}.apk.sha256"
```

Finally confirm the release job downloads the new artifact into `release/` (it already uses `actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093` with a pattern — add `torrenttv-apk` to whatever pattern/names list the WGT artifact uses). If that job lacks a Java setup, it does not need one: the APK is built in the build job and only downloaded and verified here.

- [ ] **Step 5: Commit**

```bash
git add Makefile clients/android-tv/scripts/package.sh .github/workflows/release.yml
git commit -m "feat(android-tv): package the TorrentTV APK as a release artifact"
```

---

### Task 11: CI — Android build, same-bundle check, API 26 ATV emulator smoke

**Files:**
- Modify: `.github/workflows/ci.yml` (new `android-tv` job)

**Interfaces:**
- Consumes: Task 7's `syncWebApp`, Task 7's `Log.i("TorrentTV", "ready")` marker, Task 10's script (build only).
- Produces: CI enforcement of spec Parity contract rule 1 (byte-identical `app.js`/`app.css`) and the API 26 boot smoke.

- [ ] **Step 1: Add the job**

Append to `.github/workflows/ci.yml` (pin every action by SHA to match this file's existing convention — copy the pin style from the steps above; the refs below show intent):

```yaml
  android-tv:
    name: TorrentTV Android shell
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4
        with:
          persist-credentials: false
      - uses: actions/setup-node@249970729cb0ef3589644e2896645e5dc5ba9c38 # v6
        with:
          node-version: 24
          cache: npm
      - uses: actions/setup-java@v4 # replace with the SHA pin, per this file's convention
        with:
          distribution: temurin
          java-version: 17
      - name: Install locked dependencies
        run: npm ci
      - name: Build the shared TV web app
        run: npm run build -w @filelist/tv
      - name: Sync web app into the APK assets
        run: cd clients/android-tv && ./gradlew :app:syncWebApp
      - name: Same-bundle check (Parity contract rule 1)
        run: |
          cmp clients/tv/dist/app.js clients/android-tv/app/src/main/assets/www/app.js
          cmp clients/tv/dist/app.css clients/android-tv/app/src/main/assets/www/app.css
      - name: Unit tests
        run: cd clients/android-tv && ./gradlew :app:testDebugUnitTest
      - name: Build the APK
        run: cd clients/android-tv && ./gradlew :app:assembleDebug
      - name: Boot smoke on the API 26 ATV emulator (the 2018 floor)
        uses: reactivecircus/android-emulator-runner@v2 # pin by SHA, per this file's convention
        with:
          api-level: 26
          target: android-tv
          arch: x86
          script: |
            adb install -r clients/android-tv/app/build/outputs/apk/debug/app-debug.apk
            adb shell am start -n com.torrenttv.app/.MainActivity
            sleep 15
            adb logcat -d | grep -m1 "TorrentTV: ready" || true
            adb shell uiautomator dump /sdcard/torrenttv.xml
            adb pull /sdcard/torrenttv.xml torrenttv-smoke.xml
            grep -q "TorrentTV" torrenttv-smoke.xml || (adb exec-out screencap -p > torrenttv-failure.png && exit 1)
      - name: Upload smoke evidence
        if: always()
        uses: actions/upload-artifact@v4 # pin by SHA, per this file's convention
        with:
          name: torrenttv-smoke
          path: |
            torrenttv-smoke.xml
            torrenttv-failure.png
          if-no-files-found: ignore
```

- [ ] **Step 2: Verify locally what CI will enforce (everything except the emulator)**

```bash
npm run build -w @filelist/tv
cd clients/android-tv && ./gradlew :app:syncWebApp :app:testDebugUnitTest :app:assembleDebug
cmp ../tv/dist/app.js app/src/main/assets/www/app.js
```

Expected: all green; `cmp` silent.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci(android-tv): build, same-bundle check, and API 26 ATV boot smoke"
```

---

### Task 12: Docs — ADR, client doc, and CONTEXT language

**Files:**
- Create: `docs/adr/0009-android-tv-client-torrenttv.md`, `docs/ANDROIDTV.md`
- Modify: `CONTEXT.md` (Devices section)

**Interfaces:**
- Produces: the repo's standing record of the decision, the TorrentTV build/verification doc mirroring `docs/TIZEN.md`, and updated domain language.

- [ ] **Step 1: Write ADR-0009**

```markdown
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
- Verification stays best-effort until the household names its Android TV hardware, mirroring the Tested-on-mine posture of ADR-0006.
- The AVPlay-shaped bridge is retained (not renamed neutral) to keep the Player component byte-identical; a neutral rename may come later behind the same contract.
```

- [ ] **Step 2: Write `docs/ANDROIDTV.md`** mirroring `docs/TIZEN.md`'s structure

Open `docs/TIZEN.md` and mirror its shape (intro, build, artifacts, verification log) with this content:

```markdown
# Android TV client (TorrentTV)

TorrentTV is the household's Android TV client, built for Android 8.0
(API 26, the 2018 Android TV baseline) through the newest platforms. It runs
the same web application as the Tizen client — identical screens, design,
and behavior (see the Parity contract in `docs/superpowers/specs/2026-09-05-android-tv-client-design.md`) — inside a Kotlin WebView shell that
plays video on a native surface through media3 ExoPlayer behind the same
AVPlay-shaped API the Tizen app uses. The app displays as TorrentTV with the
TT monogram; everything else is byte-identical to the Tizen bundle. Codec
reality matches the Tizen posture: direct play only, so DTS-class audio or
AV1 sources that a 2018-era set cannot decode are avoided by choosing
another release (see `docs/adr/0009`).

## Build the sideload APK

```sh
make torrenttv-apk
```

The build syncs `clients/tv/dist` into the APK assets (replacing only
`index.html` with the Android page variant), runs the Kotlin unit tests,
and produces:

```text
clients/android-tv/.build/artifacts/TorrentTV-0.1.0.apk
clients/android-tv/.build/artifacts/TorrentTV-0.1.0.apk.sha256
```

Install by sideload — Android TV treats unknown sources per device
(Settings → Device Preferences → Security & Restrictions):

```sh
adb install clients/android-tv/.build/artifacts/TorrentTV-0.1.0.apk
```

Updates are manual: install the newer APK over the old one (same
application id, higher version). CI checks that the packaged web assets are
byte-identical to the Tizen bundle and smoke-boots the real APK on an
API 26 Android TV emulator.

## Physical-TV verification log

Behavior counts as confirmed only by direct observation on a named device
(same posture as `docs/TIZEN.md`). No Android TV hardware is named yet;
every API 26+ result so far comes from the CI emulator.

| Device | Platform | Verified versions | Notes |
| ------ | -------- | ----------------- | ----- |
| (none named yet) | — | — | CI emulator `android-tv; API 26` is the only exercised floor |
```

- [ ] **Step 3: Update `CONTEXT.md` Devices language**

In `CONTEXT.md`, under `### Devices`, after the **Support floor** entry, add:

```markdown
**Android floor**:
The oldest Android TV platform TorrentTV, the Android TV client, runs on: Android 8.0, declared as `minSdk 26` — a pure floor; one APK serves every newer platform with no ceiling. Same posture as the Tizen Support floor.
_Avoid_: target SDK (a build setting, not the support promise)
```

And extend the **Verified TV** paragraph's spirit without touching it — add one sentence to the existing entry: `The Android client's log lives in docs/ANDROIDTV.md and starts with no named hardware.`

- [ ] **Step 4: Run the docs consistency grep and commit**

```bash
grep -rn "TorrentTV" CONTEXT.md docs/adr/0009-android-tv-client-torrenttv.md docs/ANDROIDTV.md | head
git add docs/adr/0009-android-tv-client-torrenttv.md docs/ANDROIDTV.md CONTEXT.md
git commit -m "docs: record the TorrentTV Android TV client decision and build"
```

---

## Final verification (after all tasks)

- [ ] `npm run test:clients` — all shared/web/tv suites pass.
- [ ] `go test ./... && go vet ./...` — server green with the CORS middleware.
- [ ] `make torrenttv-apk` — artifact + checksum produced.
- [ ] CI `android-tv` job green locally reproducible: same-bundle `cmp` passes; emulator smoke prints the TorrentTV setup screen.
- [ ] Walk the spec's Parity contract inventory against the emulator (and the Tizen smoke as reference); every line checked or promoted to a named exception.
- [ ] Physical-device verification session scheduled once the household names its Android TV hardware; record it in `docs/ANDROIDTV.md`.
