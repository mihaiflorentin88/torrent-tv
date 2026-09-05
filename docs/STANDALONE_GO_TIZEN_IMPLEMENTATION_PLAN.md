# Standalone FileList Platform: Go Server and Samsung Tizen Client

## Current implementation checkpoint (2026-08-02)

Phases 2, 4, and the principal phase-5 client shell are now materially implemented: canonical release parsing/grouping, explicit submitted tracker search plus hourly-deduplicated title expansion, demand-driven TMDB artwork/cache, browser and Tizen title hierarchy, server-backed household pages and canonical favorites, managed downloads, WebVTT subtitles, deterministic TV focus, stateful player timeline seeking/focus memory, Settings diagnostics/help, catalog Events, persistent job details/retry/recovery, payload-based SSE card patches, bounded TV reconnect, file logging/rotation, and an Apps2Samsung-ready 0.2.0 WGT. Ordinary UI reads are cache-only. Hourly incremental and weekly full catalog sync use a tracker-neutral application port and a conservative FileList request budget; all cache writes remain append-only.

Still open before release 1: full compatibility probing/ranking; ratings and uncached extended metadata; complete collection pagination/filtering; dispatcher cancellation/leases/resource budgets; retention/free-space enforcement; and Pi/Tizen end-to-end hardening. Season-pack file expansion, append-only live-search cache growth, library category grouping, searchable job pagination, and server-side progressive playback are implemented. Physical Samsung playback below 100% remains pending. See [Known issues](KNOWN_ISSUES.md) and the [Tizen verification log](TIZEN.md).

## 1. Goal and hard constraints

Build a self-contained monorepo that replaces the Jellyfin plugin with:

- a portable Go server using hexagonal architecture;
- a modern web UI served by that server;
- a Samsung Tizen TV web application packaged as a signed `.wgt` and installable through Apps2Samsung;
- a shared versioned protocol prepared for a later Android TV client, without implementing Android now.

The server must never transcode video for Tizen. On Tizen, playback means direct hardware decoding through Samsung AVPlay and source compatibility selection. Arbitrary real-time transcoding with JavaScript/`ffmpeg.wasm` is not viable on a TV: it is too slow and memory-heavy and bypasses normal hardware decode paths. The TV therefore remains direct play only. Desktop browsers may use the later audio-only AAC compatibility route; it copies video unchanged and does not alter the Tizen contract.

The Tizen app connects without login. Treat the private LAN as the security boundary: bind to configured private interfaces, reject clients outside trusted CIDRs, never port-forward the service, and provide optional future pairing without enabling it in release 1. The app remembers the server URL locally, reconnects automatically, and always offers Retry, Change Server, and Forget Server.

Resource safety outranks throughput. The Raspberry Pi must remain SSH-accessible. Every background activity must be persisted, bounded, cancellable, observable, and assigned a concurrency/memory/I/O budget.

## 2. Full feature inventory

All functionality attempted in the previous plugin belongs in the new product backlog.

### Catalog and browsing

- FileList API authentication using username and passkey; never require the account password.
- Maintain the full current category ID registry and a configurable blacklist for incompatible categories.
- Newest torrents, category browsing, and cursor pagination.
- Live tracker search after three characters, debounced and cancellable.
- Sorting everywhere: newest, oldest, title, size, seeders, leechers, completed/popularity.
- Filters everywhere: category, media kind, quality, freeleech, internal, double-up, moderated, minimum seeders, size range, and upload-date range.
- Stable lower-camel-case contracts. Every page is `{items, nextCursor, total}`.
- Conservative FileList rate limiting and visible quota/request diagnostics.

### Media hierarchy and metadata

- Group releases by IMDb ID where available, otherwise normalized title and year.
- TV-Series HD/SD/4K, Anime, Cartoons, K-Drama, RO Dubbed, and configurable episodic categories display one series, then seasons, episodes, and source variants.
- Expand season packs from torrent file metadata. Parse `S01E02`, `1x02`, multi-episode releases, specials/season 0, and unmatched files.
- Rank multiple sources by Tizen compatibility, seeders, quality, freeleech, and size.
- Fetch/cache movie, series, season and episode metadata: synopsis, cast, runtime, ratings, genres, IDs, posters, backdrops, and logos.
- Use provider ports: TMDB primary; optional OMDb/TVDB/Fanart adapters. Settings contain each required key.
- Category artwork is fallback only; title-specific images are cached with ETag, size limits, expiry, and negative caching.

### Household state

- Favorites/watchlist.
- Continue Watching and exact resume position.
- Recently viewed and previously watched.
- Watched badges and progress bars for movies and episodes.
- Series-level favorite plus episode-level watch state.
- One anonymous household profile in release 1, but include `profile_id` in schema/API for future profiles.
- Web and Tizen use server state, never browser-only local storage.

### Downloading and streaming

- qBittorrent Web API is the release-1 engine behind a `TorrentEngine` port.
- Optional later embedded engine using `anacrolix/torrent`; enable only after private-tracker compliance and whitelist tests. Never spoof a client identity.
- Optional Transmission adapter, documented as lacking reliable range/piece priority.
- Persist engine routes (`qb:<hash>`, `go:<hash>`) so restarts never guess the engine.
- Select only the requested video and matching subtitle files; deprioritize unrelated files.
- Enforce and verify sequential/first-last mode, not merely add-time hints.
- Piece-aware HTTP HEAD/Range streaming from a growing file.
- Defaults: 128 MiB initial buffer, 256 MiB read-ahead, 2 GiB hard maximum, configurable piece timeout.
- Playback starts when requested pieces exist, not after full completion.
- Persist state, progress, bytes, speed, ETA, peers, seeds, tracker response, buffered range, errors, timestamps and stream leases.
- Pause, resume, cancel, retry, and remove with/without files.
- Continue downloading/seeding after playback according to settings.
- Retention by total bytes and total files. Evict one oldest completed, unleased item at a time. Never evict incomplete or actively streamed media. (Superseded by ADR-0004: eviction is user-configured and protections are toggles, not guarantees; the defaults preserve this safe posture.)
- Reserve free disk and reject new downloads before the filesystem reaches critical capacity.

### Subtitles

- Discover matching subtitles inside torrents.
- Preferred/fallback language, default Romanian then English.
- SubDL direct-file adapter with API key. This supersedes the earlier Subs.ro RAR and subscription-locked OpenSubtitles proposals.
- Search by IMDb/TMDB ID, title, year, season, episode and normalized release.
- Auto-rank/select the best result and allow manual replacement.
- Safe archive extraction: no traversal, bounded compressed/uncompressed size, format validation.
- Convert SRT/ASS/SSA only as needed for the Tizen subtitle path; never transcode video.
- Cache searches/files and clear per provider/title.

### Jobs, events, cache and diagnostics

- Background latest sync and fair rotating category sync.
- Jobs for metadata, artwork, torrent metadata, subtitles, compatibility probes, retention and cleanup.
- Every task is visible in web and TV: queued/running/retrying/completed/failed/cancelled, phase, progress, timestamps and errors.
- Server-Sent Events initially; use WebSocket only if bidirectional events become necessary.
- Tests for FileList, qBittorrent, metadata providers, SubDL, database, storage access/free space and worker health.
- Cache refresh/clear by layer, category, canonical title/series, metadata provider and subtitle provider.
- Default maximum catalog age is 24 hours. Missing, failed or zero-item sync state triggers direct refresh. If remote fetch fails, return stale data marked stale.

## 3. Monorepo structure

```text
torrent-tv/
├── README.md
├── go.work / go.mod
├── Makefile / Taskfile.yml
├── .env.example
├── api/
│   ├── openapi.yaml
│   ├── asyncapi.yaml
│   └── generated/{go,typescript}/
├── cmd/{server,admin}/
├── internal/
│   ├── domain/{catalog,media,playback,downloads,subtitles,userstate,jobs}/
│   ├── application/{commands,queries,services,ports}/
│   ├── adapters/
│   │   ├── inbound/{httpapi,sse,scheduler}/
│   │   └── outbound/{filelist,qbittorrent,gotorrent,transmission,sqlite,tmdb,subdl,filesystem,mediaprobe}/
│   ├── platform/{config,logging,metrics,queue,resources,shutdown}/
│   └── composition/container.go
├── migrations/
├── web/                         # Preact/TypeScript/Vite browser UI
├── clients/
│   ├── shared/{api-client,event-client,models,design-tokens}/
│   ├── tizen/{src,scripts,config.xml,package.json,certificates/README.md}
│   └── android-tv/README.md     # placeholder only
├── deploy/{systemd,docker,compose,apps2samsung}/
├── docs/{architecture,api,operations,tizen,adr}/
├── test/{contracts,integration,fixtures,load,e2e}/
└── tools/
```

Dependency direction is inward: domain imports no adapters; application imports domain and declares ports; adapters implement ports; the composition root injects concrete implementations. Avoid service locators and mutable global clients.

## 4. Core ports and data

Aggregates: `TorrentRelease`, `CanonicalTitle`, `Season`, `Episode`, `MediaSource`, `MediaFile`, `SubtitleCandidate`, `Download`, `PlaybackState`, `Favorite`, `Job`, and `CacheRecord`.

Essential ports include `TrackerCatalog`, `TorrentEngine`, `CatalogRepository`, `UserStateRepository`, `DownloadRepository`, `JobRepository`, `MetadataProvider`, `SubtitleProvider`, `ArtworkStore`, `MediaProbe`, `EventPublisher`, `Clock`, and `DiskInspector`.

`TorrentEngine` must expose add, files, selected-file priorities, streaming-mode enforcement, status, piece map, location, pause/resume/remove and health. Application code deals with an engine-qualified ID and never qB-specific JSON.

Use SQLite WAL with a pure-Go driver such as `modernc.org/sqlite` to preserve `CGO_ENABLED=0` cross-builds. Keep transactions short, set busy timeout, and bound connections.

Tables: tracker releases/files, torrent metadata, canonical titles/seasons/episodes/source links, metadata/artwork/subtitle cache, favorites/playback/history, downloads/files/leases, sync state with actual counts, jobs/attempts/event journal, settings and migrations.

## 5. Persistent queue, goroutines and observer pattern

Never launch one goroutine per catalog record. SQLite is the queue source of truth; a bounded dispatcher feeds supervised worker pools.

Job lifecycle: `queued → claimed → running → succeeded | retry_wait | failed | cancelled`. Store deduplication key, kind, versioned payload, priority, resource class, progress, attempt/backoff, lease owner/expiry, timestamps and structured error.

Pi defaults:

- one catalog worker;
- one metadata worker;
- one artwork worker;
- one subtitle worker;
- one low-priority probe worker;
- one cleanup worker;
- maximum three simultaneous jobs, with at most one CPU-heavy or disk-heavy job;
- bounded channels (for example 64), HTTP connection pools and provider token buckets;
- RSS/load/free-space watchdogs pause nonessential jobs;
- all calls use context deadlines/cancellation;
- crashed worker leases expire and jobs recover after restart.

Publish events only after database commit. Observers update SSE clients, schedule dependent jobs and audit state. A slow observer cannot block producers: bounded buffers disconnect it, then it reconnects with `GET /api/v1/events?after=<id>` and replays a short persisted journal.

Events include catalog/metadata/artwork/subtitle job transitions, torrent tracker/progress, stream buffering/range-ready, retention eviction, favorite/playback changes and resource pressure.

## 6. FileList, metadata and subtitle adapters

Follow the current Jackett-compatible API semantics: Basic auth is `username:passkey`; latest uses `action=latest-torrents`; search uses `action=search-torrents&type=name&query=...`; category is a tracker category ID/list. Never expose passkeys, Basic headers or torrent URLs to clients.

Defensively parse boolean/number/date variants and detect HTML/error JSON/429 before decoding. Apply `singleflight` to duplicate requests and per-host rate limits.

Metadata and subtitle adapters must bound response bodies, cache failures briefly, retry only safe transient failures with jitter, and expose provider quota/status. Archive handling must block path traversal and archive bombs.

## 7. qBittorrent and progressive Range implementation

Release 1 should target a FileList-whitelisted qBittorrent. Prior testing found:

- Ubuntu qBittorrent 4.4.1 was rejected: “client is not on the whitelist.”
- qBittorrent 4.3.9 was accepted.
- The tested ARM64 build used libtorrent 2.0.5. Pin URL/checksum and document rollback.

Keep qB authenticated/private even though the TV API has no login. Reauthenticate cookies after qB restart without mutating a previously used HTTP client's base URL.

Read qB `total_size` with `size` fallback. Reading only `size` previously returned zero and constructed `movie.mkv/movie.mkv`, leaving streaming in Buffering at 100%. Validate every resolved path is inside the download root.

Sequential mode does not guarantee opening pieces are instantly available from connected peers. Track the requested piece range independently from overall progress and display “waiting for opening pieces,” seeds, peers and tracker message. A healthy high-seed regression should deliver a 1 MiB HTTP 206 while progress is below 100%; the previous corrected path delivered it in 2.44 seconds at 3%. Do not promise this for an unhealthy swarm.

`GET|HEAD /api/v1/streams/{sourceID}` must:

- support one RFC byte range and return 416 for invalid/multiple ranges initially;
- acquire a persisted/in-memory stream lease;
- map file offsets to global torrent piece indices;
- wait only for requested data plus bounded read-ahead;
- stream from the growing file using a bounded 512 KiB–1 MiB buffer pool;
- send correct Accept-Ranges, Content-Range, Content-Length/type;
- release leases on disconnect/cancel/panic;
- treat client cancellation as normal telemetry, not an error;
- preserve last meaningful buffered state rather than resetting it to zero.

An embedded `anacrolix/torrent` engine is phase-later. Enable only after proving FileList allows its identity, private torrents disable forbidden discovery, reader deadlines prioritize active ranges, resume survives restart, and memory/goroutines/peers remain bounded. Never spoof another client.

## 8. Media compatibility without server transcoding

Cache container, codecs, profile/level, bit depth/chroma/HDR, resolution/frame rate, audio channels/sample rate, bitrate, duration and stream indexes.

Use `ffprobe` only for selected/visible sources: one process at a time, low CPU/I/O priority, strict timeout/output limit, cached results, no recursive scan, kill process group on cancellation. It inspects only; it never transcodes.

Maintain Tizen capability profiles by model/year and accept runtime client capability reports. Rank supported AVPlay sources first; unknown is “unverified.” If no source works, explain incompatibility and offer another release.

## 9. API and UI

Version under `/api/v1`; publish OpenAPI and generate Go/TypeScript clients. Route groups: system/settings/dependencies, catalog latest/categories/titles/search/hierarchy, favorites/history/playback, downloads/actions, streams, subtitles, jobs/cancel, sync and scoped cache, SSE events.

The browser UI uses lightweight Preact + TypeScript + Vite and can be embedded with `embed.FS`. Required UX:

- cinematic responsive rows/grids with real poster art and fallback category art;
- newest, favorites, continue, recent, watched, downloads and jobs;
- search debounce/cancel, sorting/filtering/pagination;
- series/season/episode/source flow;
- compatibility and tracker state on each source;
- subtitle auto/manual selection;
- download progress/actions and a persistent job drawer;
- settings with a separate Test button for every dependency;
- scoped cache controls;
- semantic controls, clear focus, reduced motion and no pointer-only action.

Prefer SSE. Polling fallback is two seconds only while active and 15–60 seconds idle.

## 10. Samsung Tizen client

Build a Tizen Web Application using TypeScript, Preact/Vite and Samsung AVPlay. Use shared generated protocol clients. Package as signed `.wgt`.

First run:

1. Ask for server URL/IP and port; optionally discover via mDNS/SSDP.
2. Test `/api/v1/system/info` and show server/version/capabilities.
3. Save URL in Tizen application storage/local storage.
4. Auto-reconnect later; never infinite-spin when unavailable.
5. Settings always offers Retry, Change Server and Forget Server.
6. No username/password/token.

Screens: Home (continue/favorites/new/recent/downloads/jobs), Categories, Search, series hierarchy, movie/details, AVPlay player, Jobs and Settings.

Implement spatial focus and remote keys: arrows, Enter, Back, Play/Pause/Stop/Rewind/FastForward. Restore focus predictably after Back.

AVPlay:

- pass the Range-capable stream URL;
- handle buffering/time/duration/errors/subtitles/state callbacks;
- update resume every ~10 seconds plus pause/seek/stop/suspend;
- mark watched at configurable ~90%;
- use external subtitle APIs where the model supports them;
- map AVPlay failures to compatibility messages;
- never ship `ffmpeg.wasm` transcoding.

WGT/Apps2Samsung workflow:

- install Tizen Studio CLI and Samsung TV extensions;
- create author/distributor certificates and register TV DUID where required;
- never commit certificates/private keys/passwords;
- declare network, input-device, product-info and AVPlay privileges/access domains in `config.xml`, verified against target SDK/model;
- Vite build, copy assets, Tizen package/sign to `torrent-tv-<version>.wgt`;
- test with SDB/Tizen CLI on physical TV first;
- publish WGT, checksum, supported years/minimum Tizen and changelog in GitHub Releases;
- integrate the release repository with the current Apps2Samsung provider/community manifest flow. Apps2Samsung currently discovers GitHub releases and WGT assets;
- physical install is a release gate, not merely successful compilation.

## 11. Cross-platform server and deployment

Default `CGO_ENABLED=0`; pure-Go SQLite; isolate Linux priority controls behind build tags. Embed migrations/web assets.

Build/smoke-test Linux amd64/arm64/armv7, Windows amd64/arm64 and macOS amd64/arm64; optionally FreeBSD. Publish tar/zip, checksums, SBOM and multi-arch Docker images. qB remains separately installed.

Use YAML plus environment overrides. Secrets live in ignored files/environment/OS store. Include empty `.env.example` only.

Pi systemd starts conservatively (tune with tests):

```ini
MemoryHigh=900M
MemoryMax=1200M
CPUQuota=250%
TasksMax=256
IOWeight=50
OOMPolicy=stop
Restart=on-failure
RestartSec=10
```

Also implement RSS/load/free-space watchdogs, streamed I/O instead of `io.ReadAll`, bounded DB/HTTP pools, no request-time scans, graceful shutdown, loopback-only pprof and safe mode after resource-pressure shutdown.

## 12. Previous failures to prevent

1. qB 4.4.1 whitelist rejection: surface tracker response and deploy/test a whitelisted client.
2. “Unregistered torrent”: release removed, not permissions; mark source unavailable and offer alternatives.
3. Duplicated single-file path: support `total_size`/`size`, test single/multi-file path containment.
4. qB restart broke cookie login: immutable base URL and replace session/cookies only.
5. Progress advanced while piece 0 was unavailable: requested-range buffer status is separate from overall progress.
6. Sequential was only an add hint: verify/enforce current engine state on every prepare.
7. Transmission cannot guarantee piece order: do not claim progressive guarantees.
8. Permission assumptions hid tracker errors: diagnostics independently test filesystem, tracker and peers.
9. 500 GiB retention exceeded free disk: validate against live free space plus reserve.
10. Sync showed zero despite data: persist actual count; zero triggers direct refresh.
11. Global latest produced sparse categories: category-specific background/on-demand sync.
12. Search only used stale cache: direct debounced FileList search and upsert.
13. `undefined` and `page.items` UI failures: generated clients and contract tests for casing/page shape.
14. Favorites/history were browser-local: persist on server for web/TV consistency.
15. Jellyfin custom page absent on Samsung: dedicated first-class Tizen client.
16. Plugin GUID/repository/startup 503: semantic standalone API/version discovery independent of startup callbacks.
17. Pi froze for about an hour: start HTTP first; delay jobs; strict worker/RSS/CPU/I/O limits and pressure events.
18. Subtitle keys had nowhere to go: explicit fields/tests for every provider.
19. Generic art looked poor: real provider posters/backdrops with bounded cache.
20. Idle two-second polling: SSE and adaptive fallback.
21. Cancelled streams logged as errors: cancellation is normal.
22. Secrets could leak: redact headers/URLs, ignore secrets/DB/logs/media, scan every release.
23. qB bound only to LAN while a test used 127.0.0.1: test the configured URL and report bind mismatch clearly.
24. Jellyfin startup took 45–80 seconds under load: server health has explicit startup phases and essential API starts before workers.

## 13. Testing and release gates

Unit: category registry, grouping/parser, filters/cursors, zero-count freshness, piece arithmetic, qB size/path fallback, containment, retention/leases, subtitle ranking/archive safety, queue leases/retry/dedupe and compatibility ranking.

Integration: FileList sanitized fixtures plus opt-in live tests; qB Docker with legal torrents; migration from every schema; OpenAPI contracts; SSE replay; Range/HEAD/416/disconnect; provider 429/500/invalid/oversized responses.

E2E: browse/search/group, cross-client favorite/resume/watched, incomplete-download Range, subtitles, visible jobs, pause/resume/remove, retention and restart recovery.

Resource/failure: Pi-class memory cgroup, 24-hour soak, full/read-only disk, qB restart/cookie expiry, no peers, tracker rejection/unregistered torrent, malicious paths/archives, slow observers; continuously verify SSH responsiveness.

Physical Tizen gates: signed WGT installation via SDB and Apps2Samsung, cold launch/reconnect/IP change, remote focus/Back, AVPlay compatibility/failure, incomplete-download seek, subtitles, suspend/network loss and full-movie memory use.

## 14. Delivery phases

0. ADRs and spike: trusted-LAN no-auth, no server transcoding, SQLite queue, qB-first, SSE, AVPlay. Prove signed WGT and AVPlay Range playback on physical TV immediately.
1. Foundation: config/logging/migrations/health, composition, queue/events/resource monitor, cross-builds.
2. Catalog/metadata: FileList, cache/sync/search/filter/sort, hierarchy, TMDB/artwork, modern web catalog.
3. Progressive playback: qB, persisted routes/downloads, pieces/ranges/leases/retention, progress/actions; prove HTTP 206 before completion.
4. State/subtitles: favorites/continue/recent/watched/resume, torrent/SubDL and manual choice.
5. Tizen production client: setup, home/catalog/search/details/jobs/settings, AVPlay, remote focus, signed WGT/Apps2Samsung.
6. Hardening: Pi soak/failure/load, systemd limits, backup/import/export, SBOM, docs and release.

Release 1 is done only when all listed features pass or have an accepted documented platform limitation; Pi remains responsive for a 24-hour soak; Tizen installs through Apps2Samsung and directly plays a compatible torrent while incomplete; no secrets/certificates/DB/logs/media are tracked.

## 15. Primary references to re-check when implementation starts

- Apps2Samsung repository/provider manifest: https://github.com/Apps2Samsung/Apps2Samsung
- Samsung AVPlay API: https://developer.samsung.com/smarttv/develop/api-references/samsung-product-api-references/avplay-api.html
- Samsung model-year media specifications: https://developer.samsung.com/smarttv/develop/specifications/media-specifications/2024-tv-video-specifications.html
- Samsung TV device/Tizen SDK setup: https://developer.samsung.com/smarttv/develop/getting-started/using-sdk/tv-device.html
- Tizen package guidance: https://docs.tizen.org/application/web/guides/app-management/package/
- Jackett FileList adapter/category/API behavior: https://github.com/Jackett/Jackett/blob/master/src/Jackett.Common/Indexers/Definitions/FileList.cs
- qBittorrent Web API: https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-(qBittorrent-4.1)
- anacrolix/torrent: https://github.com/anacrolix/torrent

These are temporally changeable. Verify current SDK versions, privileges, media tables, Apps2Samsung schema and tracker rules rather than copying old values blindly.

## 16. Skills recommended on the new machine

Install/create narrow skills and read each `SKILL.md` fully before use:

- Go backend and hexagonal architecture;
- Go concurrency, persistent queues, cancellation/backpressure and race testing;
- BitTorrent/private tracker/qB/anacrolix piece streaming;
- frontend design/UI-UX for Preact/TypeScript and TV focus accessibility;
- Samsung Tizen Web/AVPlay/remote/lifecycle/WGT/certificates;
- Apps2Samsung GitHub release/provider integration;
- OpenAPI/AsyncAPI generation and compatibility;
- Playwright browser E2E;
- SDB/Tizen physical-device testing;
- pprof/cgroups/systemd resource engineering and soak/load testing;
- trusted-LAN API threat modeling, SSRF/path/archive/secret safety;
- TMDB/SubDL metadata/subtitle adapters;
- optional image generation for original category placeholders only.

Do not make builds depend on a developer's private Codex directory; pin tool versions in repository docs.

## 17. Required documentation in the new repository

Provide root quick start, architecture/ADRs, user setup/use guide, credential/provider guide, Tizen SDK/certificate/DUID/WGT/SDB/Apps2Samsung guide, operations/resource/backup/upgrade guide, OpenAPI/AsyncAPI reference, contributing/testing/release guide, Samsung model/codec/subtitle compatibility matrix, and troubleshooting covering every failure above.

## 18. First actions on the new PC

1. Create this monorepo and place this plan under `docs/architecture/`.
2. Write ADRs before choosing libraries.
3. Install Tizen tooling and prove a minimal signed WGT on the physical TV first; do not postpone signing/device risk.
4. Define OpenAPI/AsyncAPI and generated clients.
5. Implement domain/application ports, migrations, persisted queue and resource monitor.
6. Implement FileList catalog without downloads.
7. Implement web catalog and jobs/SSE visibility.
8. Implement qB and Range streaming with legal synthetic torrents, then opt-in live FileList tests.
9. Add shared state, metadata/artwork and subtitles through bounded workers.
10. Build production Tizen screens/AVPlay and Apps2Samsung release.
11. Run Pi resource/soak tests before completion.

Do not begin Android TV until the shared protocol is stable and the Tizen release passes every release-1 gate.
