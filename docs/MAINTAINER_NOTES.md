# Maintainer and agent notes

This file records constraints and invariants that must survive context resets and future implementation passes.

## Environment safety

- Do not install software or system packages on a developer workstation without the user's explicit permission. In particular, do not install Node.js locally. Use the pinned frontend Docker image for browser/Tizen tests and builds.
- Compiling the applications is allowed. Runtime and device mutations are separate approval boundaries.
- Raspberry Pi integration testing and deployment must use an explicitly supplied `PI_HOST`; never commit a private username, hostname, IP address, SSH key, provider credential, Tizen certificate, database, log, media file, or generated binary.
- `deploy/bootstrap-server.sh` is for a newly cloned Linux server only. Never run it on a workstation. Routine `make deploy-pi` installs no packages.
- `.env` is a local diagnostic aid only. The daemon must remain browser-configured and persist settings atomically in `data/settings.json` with restrictive permissions.
- Routine deployment stores only non-secret prompt defaults in ignored `deploy/.deploy.local.conf`. Every deployment creates a new protected qBittorrent config backup and may merge only the four keys in the sanitized streaming template; never copy a live config or backup into Git.

## Data and provider invariants

- The observed tracker cache is append-only: upsert newer values, but never invalidate or remove older cached releases during refresh or rebuild.
- Normal browsing, library pages, categories, sorting, filters, and pagination read the local cache. FileList is contacted only by an explicit search or a scheduled/manual event job.
- Preparing a source must reuse an exact managed release/file before opening torrent metadata. Household favorites prefer a still-managed playback/download source so provider limits cannot block local media.
- Completed playback must not depend on qBittorrent. Incomplete playback must keep selected files at normal priority, reapply sequential/first-last scheduling once, and resolve qBittorrent's effective `temp_path` beneath the configured root between read-ahead chunks.
- A title-expansion request is suppressed when the title was refreshed less than one hour ago. FileList requests remain serialized even when the global background worker limit is higher.
- Metadata is queued only for visible/searched media and patches clients through SSE. A parsed movie/series kind is a preference during TMDB lookup, not authority; the Find API may return the valid record in the other bucket.
- SubDL has a limited daily quota. The web player lists all subtitle sources automatically when playback opens (contained, embedded, provider); the server's one-hour search cache bounds provider calls. The TV player keeps automatic search to torrent-contained and server-probed embedded subtitles and requires the explicit **Find online subtitles** action for providers. Prepared subtitle assets are persisted and reused.
- Server-side progressive playback is confirmed only by a below-100% HTTP 206 plus media parsing. Keep browser and physical-TV playback status separate until each client is observed below 100%.

## TV interaction invariants

- Every navigable collection control has stable `data-focus-region`, `data-focus-row`, `data-focus-col`, and `data-focus-key` attributes.
- Read-only TV inputs enter Samsung IME edit mode only after OK. Short Back manages the current UI/sidebar; holding Back for five seconds exits the main application.
- The player preserves logical focus across repeated seek/button actions. Completed media uses server-prepared WebVTT for contained and embedded text streams; Samsung native TEXT tracks are only a labeled fallback.
- Job log entries are buttons: OK expands/collapses the chosen entry, while D-pad navigation keeps every row reachable and scrolls it into view.

## Release and repository rules

- `VERSION` is the release source of truth. It must equal the Tizen package and manifest versions; release tags use `v<VERSION>`. The Android release artifact is named from it (`TorrentTV-<VERSION>.apk`), while the app's own `versionName`/`versionCode` live in `clients/android-tv/app/build.gradle.kts`.
- Generated web bundles, WGTs, binaries, SBOMs, certificates, runtime data, logs, editor state, and local design scratch files stay ignored.
- Run `make check`, the Docker frontend build, WGT validation, and the secret audit before publishing. GitHub CI repeats unit/compiler/package checks; Security runs Gitleaks, govulncheck, Trivy, CodeQL, actionlint, Zizmor, and dependency review.
- Pin GitHub Actions to the commit behind an annotated release tag (`refs/tags/<tag>^{}`), not the tag-object SHA. Trivy SARIF must set `limit-severities-for-sarif: true`; otherwise its exit code includes severities outside the configured HIGH/CRITICAL release gate.
- CycloneDX Go application SBOM generation receives the repository module root (`.`) and identifies the executable with `-main cmd/server`; a package directory is not itself a Go module.
- A master push builds the complete release matrix without publishing. Only an exact version tag publishes Linux amd64/arm64/armv7, Windows amd64, macOS amd64/arm64, the unsigned Apps2Samsung WGT, checksums, CycloneDX/SPDX SBOMs, and provenance attestations.
- A tagged release additionally notifies the update feed service: the
  `notify-update-feed` job runs `scripts/notify-release.mjs` after a
  successful `publish` (GitHub environment `prod`, secret `FL_ADS_APIKEY` —
  already configured by the user; never query or overwrite it). A failed
  notification must never delete or recreate the release or rerun
  `gh release create`; the service's five-minute reconciliation is the
  automatic recovery. The matching receiver credential and endpoint
  deployment on the update-feed service are still pending, and `prod`
  environment approvals can delay the notification push.

## Next verification/implementation checkpoints

1. Install the new `0.3.0` WGT and confirm persisted LAN discovery, safe complete-season disclosure/actions, progressive playback below 100%, unified deletion, data parity, and contained/embedded server WebVTT subtitles on the physical TV.
2. Verify the complete 0.3.0 D-pad graph on the TV: discovery/manual setup, pack header → inner actions → episode rows, Downloads controls, player timeline/audio/subtitle menus, and focus restoration after dialogs and playback.
3. On the TV Jobs detail page, D-pad to several log entries, press OK repeatedly to expand/collapse them, inspect long context, load older logs, and return without losing focus.
4. Continue the remaining items in `KNOWN_ISSUES.md` and the implementation plan, preserving confirmed UX and data invariants above.

## Published baseline

- `v0.2.0` was published from commit `c94f5945f816c03de1fe34456c5a3db1dc4f3c1a` on 2026-08-02.
- Hosted CI, CodeQL, Gitleaks, govulncheck, Trivy, actionlint, Zizmor, the six-platform release matrix, Tizen WGT validation, SBOM generation, checksums, provenance, and release publication all completed successfully.
- The release contains Linux amd64/arm64/armv7, Windows amd64, macOS amd64/arm64, `FileListTV-0.2.0.wgt`, SHA-256 checksums, CycloneDX Go/npm SBOMs, and an SPDX release SBOM.
- The repository secret audit found no committed credentials. Keep `.env`, runtime settings/data, keys, certificates, logs, downloaded media, and generated artifacts outside Git; use `.env.example` and documented placeholders only.
