# Artifact naming standard design

Date: 2026-09-06
Status: approved

## Step 0 — repository swap comes first

Implementation starts with the repository swap: the project lives at
`github.com/mihaiflorentin88/torrent-tv` (imported from
`filelist-streaming-service`). Everything else in this spec assumes that
identity. The swap has already been applied and verified:

- `git remote origin` points at `git@github.com:mihaiflorentin88/torrent-tv.git`.
- The Go module is `github.com/mihaiflorentin88/torrent-tv`; no
  `filelist-streaming-service` import remains outside dated historical docs.
- The update-feed service (filelist-ads-server) points its release adapter at
  the new repository.

Re-verification after any future history rewrite: confirm the remote URL,
`go.mod` module line, a `git grep` for the old module path (dated
`docs/adr/`, `docs/superpowers/plans/`, `docs/superpowers/specs/` excluded),
and a green `go build ./... && go test ./...`.

## Naming standard

Every downloadable release artifact is named
`torrent-tv-<version>-<platform>[-<flavor>].<ext>`. The filename alone tells
the user what a build is and where it runs. `<version>` is required: the
update-feed service validates version-parameterized payload sets.

Flavor markers:

- `app` — the packaged macOS application (a `.app` bundle inside the zip).
- `desktop` — desktop app + server in one archive (GUI included).
- `cli` — a raw binary meant to run from a terminal; it can open the desktop
  UI or run the headless `serve` server. (The macOS CLI binaries are the same
  GUI-capable cgo builds as before — no new build flavor.)
- `headless` — pure server build, GUI excluded at compile time
  (`CGO_ENABLED=0`, `-tags headless`; armv7 by architecture constraint).
- TV clients carry their platform instead of a flavor: `samsung-tizen`
  (unsigned WGT) and `android-tv` (APK + `.apk.sha256`).

The artifact table:

| Artifact | Replaces | Meaning |
| --- | --- | --- |
| `torrent-tv-<v>-macos-universal-app.zip` | `…-darwin-universal.zip` | Torrent TV.app, Apple Silicon + Intel |
| `torrent-tv-<v>-macos-arm64-cli.tar.gz` | `…-darwin-arm64.tar.gz` | terminal binary, Apple Silicon |
| `torrent-tv-<v>-macos-amd64-cli.tar.gz` | `…-darwin-amd64.tar.gz` | terminal binary, Intel |
| `torrent-tv-<v>-linux-amd64-desktop.tar.gz` | `…-linux-amd64.tar.gz` | desktop app + server |
| `torrent-tv-<v>-linux-arm64-desktop.tar.gz` | `…-linux-arm64.tar.gz` | desktop app + server |
| `torrent-tv-<v>-linux-amd64-headless.tar.gz` | unchanged | server only |
| `torrent-tv-<v>-linux-arm64-headless.tar.gz` | unchanged | server only |
| `torrent-tv-<v>-linux-armv7-headless.tar.gz` | `…-linux-armv7.tar.gz` | server only, 32-bit ARM |
| `torrent-tv-<v>-windows-amd64-desktop.zip` | `…-windows-amd64.zip` | desktop app + server |
| `torrent-tv-<v>-windows-arm64-desktop.zip` | `…-windows-arm64.zip` | desktop app + server |
| `torrent-tv-<v>-samsung-tizen.wgt` | `torrent-tv-<v>.wgt` | Samsung Tizen TV client |
| `torrent-tv-<v>-android-tv.apk` (+ `.sha256`) | `torrent-tv-<v>.apk` | Android TV client |

Notes:

- Artifacts say `macos` (user-facing) although Go's `GOOS` is `darwin`; the
  updater maps between them.
- The binary inside every archive stays `torrent-tv` (`torrent-tv.exe` on
  Windows): the installed filename is stable across versions, which the
  updater's payload verification and the systemd `ExecStart` path both rely
  on.
- The manually-signed Tizen artifact (local flow) is
  `torrent-tv-<version>-samsung-tizen-signed.wgt` to stay distinct from the
  unsigned release asset.
- Archives (not raw binaries) remain the payload format: GitHub strips the
  executable bit from raw downloads, tar preserves mode 0755, the `.app`
  bundle is a directory, and the updater's staging/verification pipeline
  (`StageArchive` → digest → extract → verify payload identity) is defined
  over archives. The locally-signed WGT and local `bin/` development outputs
  keep their version-less naming.

## Where the names live

Three consumers must agree on every name change:

1. **Client updater** (`internal/application/updates/release.go`,
   `assetName`): resolves the exact asset for the running flavor and matches
   it by name against the release; the portal (update-feed service)
   contributes only the version hint.
2. **Release pipeline** (`.github/workflows/release.yml` matrix targets,
   checksum payload list, `darwin-app` merge step; `Makefile` `TIZEN_WGT`;
   `clients/android-tv/scripts/package.sh`;
   `clients/tizen/scripts/package.sh`).
3. **Update-feed service** (filelist-ads-server `domain/updates`): the
   `assetPlatforms` suffix table and the auxiliary payload allowlist (WGT,
   APK + digest, SBOMs). Its API platform keys (`linux-amd64`,
   `darwin-universal-bundle`, …) are stable identifiers and do not change.

Documentation surfaces the table in `docs/INSTALLATION.md` (with per-asset
"pick it when" guidance and an explicit macOS ".app or .tar.gz?" note),
`README.md`, `docs/MAINTAINER_NOTES.md`, `docs/ANDROIDTV.md`, and the
migration section for pre-rename installs.

## Compatibility

Pre-rename clients (v0.3.0 and older) already stopped matching release
assets when the repository and asset prefix changed; this standard changes
the suffixes again, so the first release tagged after this spec is the first
one the update feed can publish and the first one post-rename self-updaters
can install. No additional compatibility surface exists: there are no
released clients carrying the intermediate `torrent-tv-<version>.wgt` /
`.apk` names.

## Verification

- `go test ./...` in torrent-tv (updater matrix tests, CLI update flow).
- `go test ./...` in filelist-ads-server (release eligibility: unexpected
  assets, missing payloads, checksum manifest).
- `python3 -m unittest discover -s tools/tests` (WGT packer).
- A `git grep` sweep proving old suffixes (`darwin-universal`,
  `linux-armv7.tar.gz`, `TorrentTV-`, …) survive only in dated historical
  records.
