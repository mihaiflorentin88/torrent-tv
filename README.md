# Torrent TV

A self-hosted Go media server with a built-in torrent engine, a responsive web application, a Samsung Tizen TV client, and a desktop app for browsing FileList and streaming downloads as they arrive. It is designed for a trusted private LAN and a small server such as a Raspberry Pi 4.

Version **0.3.0** adds the built-in torrent engine — no qBittorrent or Docker required — first-run setup prompts, and automatic ffmpeg/ffprobe detection. Progressive HTTP Range playback from an incomplete download is server-verified; physical Samsung AVPlay verification below 100% remains pending.

**[Install](docs/INSTALLATION.md)** — download a binary for Linux, Windows, or macOS from the [releases page](https://github.com/mihaiflorentin88/torrent-tv/releases) and run it. Nothing else is required to start.

## Features

- **Stream torrents while they download.** The built-in engine prioritizes the pieces playback needs, so playback starts before the download finishes — no qBittorrent or Docker required (qBittorrent stays available as an optional external engine).
- **Subtitles handled end to end.** Embedded and torrent-packed subtitles are discovered automatically, SubDL downloads are cached and reused, and track preferences (English audio, Romanian subtitles with English fallback) are remembered per file.
- **Automatic torrent eviction.** Finished downloads clean themselves up when storage runs low, by the rule you pick (default: oldest completed), with protection for incomplete, actively streaming, favorited, and never-watched items.
- **Watch anywhere on your network.** A responsive web app, the Samsung Tizen TV with native direct playback, and a desktop app — with resume, auto-advance to the next episode, and watch state shared across all of them.
- **The whole FileList catalog, fast.** Cached browsing, search, and filtering, with canonical movie and series pages that map every season-pack file to its episode.
- **Precise download control.** Live progress, speed, and peer state, per-file selection inside season packs, and one action that removes a torrent together with its files.

## Screenshots

Select a thumbnail to open the optimized full-size image.

<table>
  <tr>
    <td align="center"><a href="docs/img/homepage-preview.webp"><img src="docs/img/thumbs/homepage-preview.webp" width="320" alt="Home dashboard"></a><br><sub>Home and continue watching</sub></td>
    <td align="center"><a href="docs/img/my-lib-dashboard-preview.webp"><img src="docs/img/thumbs/my-lib-dashboard-preview.webp" width="320" alt="My Library dashboard"></a><br><sub>My Library dashboard</sub></td>
  </tr>
  <tr>
    <td align="center"><a href="docs/img/tracker-dashboard-preview.webp"><img src="docs/img/thumbs/tracker-dashboard-preview.webp" width="320" alt="Tracker dashboard"></a><br><sub>Tracker dashboard</sub></td>
    <td align="center"><a href="docs/img/browse-preview.webp"><img src="docs/img/thumbs/browse-preview.webp" width="320" alt="Browse catalog"></a><br><sub>Browse and filter</sub></td>
  </tr>
  <tr>
    <td align="center"><a href="docs/img/tv-show-preview.webp"><img src="docs/img/thumbs/tv-show-preview.webp" width="320" alt="TV show season and episode page"></a><br><sub>Series, seasons, and episodes</sub></td>
    <td align="center"><a href="docs/img/downloads-preview.webp"><img src="docs/img/thumbs/downloads-preview.webp" width="320" alt="Download management"></a><br><sub>Download management</sub></td>
  </tr>
  <tr>
    <td align="center"><a href="docs/img/categories-preview.webp"><img src="docs/img/thumbs/categories-preview.webp" width="320" alt="FileList categories"></a><br><sub>Categories</sub></td>
    <td align="center"><a href="docs/img/jobs-preview.webp"><img src="docs/img/thumbs/jobs-preview.webp" width="320" alt="Background jobs"></a><br><sub>Background jobs</sub></td>
  </tr>
</table>

The README uses 360 px WebP thumbnails and optimized WebP full views so the gallery stays lightweight while retaining readable full-size screenshots.

## Documentation

| Guide | Contents |
| --- | --- |
| [Installation and upgrades](docs/INSTALLATION.md) | Requirements and where to get them, per-OS install and run instructions, credential/API-key acquisition, automated and manual installs, GitHub release artifacts, Raspberry Pi deployment, backup, rollback, and Tizen and Android TV installation. |
| [User guide](docs/USER_GUIDE.md) | Browsing, season packs, downloads, playback, resume, subtitles, TV operation, and troubleshooting. |
| [Configuration reference](docs/CONFIGURATION.md) | Settings, paths, limits, language preferences, and provider configuration. |
| [Tizen guide](docs/TIZEN.md) | Build, signing, Developer Mode, Apps2Samsung installation, D-pad behavior, and physical-TV verification. |
| [Subtitle architecture](docs/SUBTITLES.md) | Existing subtitle discovery, preparation, selection, storage, and playback behavior. |
| [API reference](docs/API.md) | HTTP endpoints and response contracts for clients and integrations. |
| [Architecture](docs/ARCHITECTURE.md) | Boundaries, domain model, adapters, storage, streaming, and client structure. |
| [Development](docs/DEVELOPMENT.md) | Build/test workflows, integration testing, checkpoints, and implementation notes. |
| [Known issues](docs/KNOWN_ISSUES.md) | Remaining limitations and physical-device verification items. |
| [Maintainer notes](docs/MAINTAINER_NOTES.md) | Release, operational, dependency, security, and deployment invariants. |
| [Security policy](SECURITY.md) | Supported versions, reporting, and deployment security boundaries. |

## Local quick start

```bash
go test ./...
go run ./cmd/server
```

Open `http://127.0.0.1:8097`, enter your FileList settings, and save them. Runtime settings are stored in `data/settings.json` with mode `0600`. Browser Settings includes dependency-specific Test buttons and copyable field help with links to every provider. The repository `.env` is for developer diagnostics only and is never read by the application.

The first start asks for the three settings it cannot guess: a writable download root, the FileList username, and the passkey. Answers are stored into `data/settings.json`; headless environments without a terminal skip the questions, keep the defaults, and log a warning, so set `TORRENT_TV_DOWNLOAD_ROOT` (or a settings file) for systemd.

The default trusted networks are loopback and RFC1918 private address ranges. Narrow them in Settings when practical. Do not expose this no-login service to the internet.


## Frontend and TV package

`make frontend` builds and tests the browser and Tizen clients in Docker, then creates and validates the unsigned Apps2Samsung artifact at `clients/tizen/.build/artifacts/torrent-tv-<version>-samsung-tizen.wgt`. Apps2Samsung signs it for the selected TV during installation. See [the Tizen build and installation guide](docs/TIZEN.md), including the living physical-TV verification log.

## Raspberry Pi deployment

`make deploy-pi PI_HOST=user@server.lan` cross-compiles the headless ARM64 server, then prompts for the server and non-secret qBittorrent/application paths. Answers are remembered in ignored `deploy/.deploy.local.conf`. The binary is installed under the service-owned `/var/lib/torrent-tv/bin` directory so the in-application updater can replace it without root; installs that still live in `/usr/local/bin` are migrated, while an explicitly configured custom path is kept (reported as manual-update-only when its directory cannot be owned by the service user). Every run creates a new protected qBittorrent config backup, merges only the credential-free streaming template, and restores the previous unit, application binary, and qBittorrent configuration if deployment fails. The target operation uses `sudo` and therefore requires explicit approval.

## Server dependencies and fresh-machine setup

| Dependency | Required for | Installation/runtime notes |
| --- | --- | --- |
| Linux with systemd | Daemon isolation and restart | Raspberry Pi OS/Debian/Ubuntu, Fedora/RHEL/Rocky/Alma, Arch, and openSUSE package families are supported. |
| qBittorrent-nox (optional) | Alternative download engine | Skip it unless you switch the download engine to qBittorrent in Settings. Fresh setup binds the Web UI to `127.0.0.1:8080` and uses `/srv/filelist-downloads`. |
| FFmpeg and ffprobe | Embedded subtitle probing/extraction and browser audio compatibility | Video is never transcoded. Desktop-browser fallback converts incompatible audio to AAC; Tizen and TorrentTV stay direct-play. Paths are browser-configurable. |
| SQLite | Catalog, jobs, playback, ratings, subtitle associations | Embedded through the pure-Go driver; no system SQLite package is required. |
| Exact Go version in `go.mod` | Fresh-clone server build | Installed as a private verified toolchain; system Go is not replaced. |
| CA certificates, curl, tar | Verified toolchain download | Used only for setup/build and HTTPS trust. |
| logrotate | Server log retention | Rotates daily or at 10 MiB and keeps 14 compressed generations. |
| Node.js | Not required at runtime | Frontend builds use the pinned Docker image; assets are embedded in the Go binary. |

For a fresh cloned server only, preview the operation before approving it:

```bash
sudo sh deploy/bootstrap-server.sh --confirm-server-install --dry-run
sudo sh deploy/bootstrap-server.sh --confirm-server-install
```

The idempotent script supports `apt`, `dnf`, `pacman`, and `zypper`. It never changes firewall rules or writes tracker/provider secrets. It creates dedicated service users, configures qBittorrent and the application services, verifies the official Go archive SHA-256, builds locally, and prints the qBittorrent temporary-password journal command. Routine `make deploy-pi` stays package-install-free. Do not run this bootstrap on a workstation or an already configured production Pi.

## Safety boundaries

- No host package installation is part of a normal build.
- Project dependencies are downloaded by Go or pinned build containers.
- Pi service/user/group installation and Tizen device changes require explicit approval.
- A real FileList torrent is never selected automatically for tests.
- Only server-owned torrents persisted in SQLite are exposed in the management UI.

## CI, security, and releases

Every push to `master` runs Go tests/race/vet, browser and TV compiler/tests, WGT validation, and packaging tests. A separate security workflow runs Gitleaks, govulncheck, Trivy, CodeQL, actionlint, Zizmor, and pull-request dependency review; Dependabot checks Go, npm, Actions, and Docker dependencies weekly.

`VERSION` is the single release version. A matching `v<VERSION>` tag builds and publishes Linux amd64/arm64/armv7, Windows amd64/arm64, macOS amd64/arm64 (plus a universal `Torrent TV.app`), and an unsigned Apps2Samsung WGT. Releases also contain SHA-256 checksums, CycloneDX/SPDX SBOMs, and build-provenance attestations. See [maintainer notes](docs/MAINTAINER_NOTES.md) and [security policy](SECURITY.md).
