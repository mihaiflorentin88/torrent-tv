# Architecture

## Dependency direction

`internal/domain` contains data types. `internal/application` contains use cases and declares ports. Inbound HTTP and outbound FileList, qBittorrent, and SQLite adapters implement those ports. `internal/composition` is the only package that chooses concrete adapters.

The server starts the essential HTTP surface before any future background synchronization. SQLite is configured for WAL, a five-second busy timeout, foreign keys, and a four-connection ceiling suitable for the Raspberry Pi.

## Canonical catalog and metadata

Tracker releases remain the durable source records. Every upsert also writes a parsed `catalog_releases` projection containing the canonical title ID and display/technical fields. Existing databases are backfilled on startup, so the migration does not require a destructive rebuild or a fresh FileList request.

The grouped API derives movies and series from that projection and exposes a show → season → episode → source hierarchy. IMDb identity is preferred for grouping; normalized Unicode title/year/kind is the offline fallback. The parser deliberately does not infer media kind from category alone.

TMDB enrichment is optional. Cached metadata is returned immediately. Clients explicitly ensure metadata for at most 24 visible titles, and an SSE completion event carries the updated card so the screen can patch in place; pagination no longer queues the entire catalog. Title-detail requests are cache-only and never block on TMDB. Romanian (`ro-RO`) is preferred, English (`en-US`) fills missing fields, and the original title is retained. Provider image paths stay server-side; clients receive same-origin artwork URLs whose responses are size-limited and atomically cached below `data/artwork`.

The application layer declares a tracker-neutral `Tracker` port and capability description. FileList is the first adapter. Only explicit submitted searches and background event jobs call the tracker. Submitted searches are persistent asynchronous jobs: the HTTP request returns cached matches immediately, the worker permanently merges every eventual result into SQLite, queues expansion for each canonical title, and publishes an SSE completion event. Successful title expansion is suppressed for one hour. All ordinary navigation, filters, pagination, title details, settings, jobs, and library reads use SQLite only. FileList requests are serialized, throttled, and bounded to 140 per rolling hour. Hourly latest sync and weekly/manual rebuild only upsert: old observations are never deleted. Rebuild refreshes each enabled category's API-visible latest window and reconstructs projections over all retained rows; it cannot retrieve never-observed historical releases because the supported FileList API has no history pagination. Zero-seeder rows remain durable but are excluded from discovery.

Title-expansion jobs download each unseen season-pack `.torrent`, parse bounded bencoded metainfo without adding it to qBittorrent, validate paths, and store the playable file manifest in SQLite. Detail navigation only reads those cached manifests. Episode parsing creates virtual sources carrying `fileIndex`, path, and file size so preparation selects the requested episode rather than the whole pack.

Preparing a whole season enables every playable episode file in the chosen pack, retains one qBittorrent torrent, and persists one managed `downloads` row per episode. Reconciliation derives each row's byte count and progress from that selected qBittorrent file instead of the torrent-wide total. The clients can therefore list and play individual episodes while pause, resume, and deletion deliberately apply to all sibling rows sharing the engine hash.

## Runtime configuration

The browser reads and updates `/api/v1/settings`. The server atomically replaces `data/settings.json` through a same-directory temporary file and enforces mode `0600`. Empty secret fields preserve an existing value. Responses never return stored secret values; they return `...Configured` booleans instead.

Listener, database-path, maximum-concurrent-job, and title-refresh-timeout changes are saved but require restart. Dependency clients read current settings for every authentication cycle, so FileList, qBittorrent, TMDB, and SubDL settings take effect without restart.

`.env` is deliberately outside the runtime configuration system. It exists only for developer-controlled diagnostics.

## Torrent ownership

Adding a source creates a durable `downloads` row containing a stable source ID, FileList release ID, `qb:<info-hash>` engine route, selected qB file index/path, global file offset, absolute contained path, size, piece size, state, progress, lease, errors, and timestamps.

The UI lists and manages these rows rather than enumerating all qBittorrent content. This prevents the application from adopting or deleting unrelated torrents. On restart, status is reconciled from qB using the persisted engine route.

Preparation first resolves an existing managed row by release and explicit file index. It can also materialize a requested sibling episode directly from the file list of an already-managed qBittorrent torrent. Legacy requests without an index prefer a completed row, then the newest row for that release. Only a true managed-torrent cache miss downloads torrent metadata from FileList, so rate limiting cannot block playback of an already-downloaded source or the next episode in an active pack. Reused incomplete rows are resumed and have their streaming/file priorities reasserted. Canonical favorites likewise prefer an exact managed source when their previous playback source is unavailable.

## Progressive playback

Playback selects one of two server-side strategies and does not wait for torrent completion. Completed media is read from its persisted final path without qBittorrent or FileList; incomplete media is resumed when paused and read from qBittorrent's effective temporary path until completion moves it to the final content path.

1. Download the `.torrent` metadata and calculate its canonical SHA-1 info hash.
2. Add it to qB with sequential download and first/last-piece priority enabled.
3. Select normal priority `1` for the requested video and contained subtitle files; set unrelated files to priority `0`. Maximum priority `7` is deliberately avoided because applying it to the whole media file defeats special first/last-piece priority.
4. Re-read `seq_dl` and `f_l_piece_prio`. Reapply both off/on once after add or application restart, and after any file-priority change; later range requests leave stable settings untouched.
5. Convert a requested HTTP file byte range into global torrent piece indexes using the file offset and qB piece size.
6. Read `piece_size` from `torrents/properties` (qBittorrent 4.3.x does not include it in `torrents/info`) and poll `pieceStates` until only the requested pieces report state `2`, independent of overall progress.
7. While progress is below 100%, read qBittorrent's effective `temp_path` from `app/preferences` and resolve the selected file beneath it. At completion use `content_path`; every temporary and final candidate must remain beneath the configured download root.
8. Before committing HTTP headers, verify the configured daemon account can open the growing file and read the final byte in the requested startup range. This turns mount/path/permission failures into a visible 503 diagnostic rather than a broken 206 response.
9. Re-resolve the path between read-ahead chunks so qBittorrent can move completed content from temporary to final storage without breaking playback, then return HTTP 206 with correct Range headers and media type.

For an in-progress download the stream commits headers as soon as the leading `streamStartBytes` (default 2 MiB) of the requested range are readable, then serves adaptively growing chunks capped at 256 MiB read-ahead windows, so playback starts inside a player's request patience on slow swarms. Media-info probing still waits for the larger `initialBufferBytes` head window because demuxers need deep container indexes. Multiple ranges return 416 in release 1. Disconnect cancellation is normal and releases the persisted stream lease.

The qB endpoints and field semantics follow the official [qBittorrent WebUI API](https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-%28qBittorrent-4.1%29): torrent contents/file priorities, piece states, sequential download, and first/last-piece priority.

qBittorrent 4.3.x add responses are accepted on any 2xx status unless the response explicitly contains `Fails.`; some builds return an empty success body rather than `Ok.`. A `Fails.` response is treated as a duplicate only when a follow-up lookup confirms the exact calculated info hash already exists; that torrent is then reused and recorded as managed.


### Measured audio anchoring

For codecs the browser cannot decode, the client fetches and decodes byte windows itself. Which window plays at a given time is decided only by measured timestamps (ADR-0002): `GET /downloads/{id}/audio-anchor` ffprobes the exact concatenated artifact (2 MiB container head plus the fetch window) the decoder consumes and reports the window's first and last audio PTS in packet order. The client planner probes at most five windows — moving by each window's own measured byte density — and trims the decoded front so the first sample lands on the video clock; average-bitrate arithmetic only ever chooses where probing starts. Windows whose bytes have not arrived answer 503 with `Retry-After`, and the controller re-probes instead of failing playback.

## Household state

Release 1 uses one server-side `household` profile while retaining `profile_id` for later profiles. Favorites use canonical title IDs so every release version of a movie or show stays grouped; startup migration maps older release-keyed favorites to their canonical title. Playback records retain release/file identity, exact millisecond position, duration, watched state, and update time. They intentionally outlive download rows so removing a torrent does not erase viewing history. Household dashboard sections collapse those file-level records to their newest representative per canonical title before applying limits, so a series never fills a rail with episode cards; Downloads intentionally keeps every managed file. Browser video, Tizen AVPlay, and TorrentTV's ExoPlayer bridge update the same records approximately every ten seconds and on lifecycle boundaries; the server, not the client, applies the configured watched threshold.

Per-source playback preferences persist the selected audio language/index and subtitle mode/language/provider/candidate. Default selection is English audio and Romanian then English subtitles. The web player fetches all subtitle scopes when playback opens so the menu shows Local, Built-in, and provider candidates together; the TV player keeps provider results behind its find-subtitles action. Preparation writes the converted result to the content-addressed cache before playback. Episode completion asks the server for the immediate next cached episode, reuses an existing pack torrent when possible, and carries language intent forward while avoiding reuse of an episode-specific subtitle candidate.

## Canonical library projection

Library, Tracker, and category cards navigate by canonical title ID. Household rows also carry a server-derived season/episode location, so a pack file or watched episode opens its show with the correct episode selected instead of starting playback or navigating to Downloads.

Catalog detail joins cached sources with managed download rows and household playback history. Each source receives ownership, progress, and watch state; episodes consider one completed version sufficient; seasons are complete only when every known episode is complete. Every season-pack source also receives an exact-release aggregate derived only from its matching episode sources, with progress weighted by selected-file size. This lets clients mark one downloaded/downloading pack without disabling alternative releases. Pack manifests keep one qBittorrent engine while their exact file indexes appear independently in the canonical episode hierarchy.

Downloads polling uses immutable identity and order. The server persists fresh torrent telemetry without changing the lifecycle timestamp, and clients reconcile records by ID. Existing records keep their object identity when unchanged and their relative order when telemetry changes. Web retains the first visible row as a scroll anchor; Tizen retains both the row anchor and focused control. Stable telemetry slots prevent changing numbers or errors from shifting rows, while semantic file/torrent facts may wrap and expand instead of being clipped.

## Persistent jobs and events

SQLite owns durable catalog, title-expansion, tracker-search, and metadata job state with deduplication keys and `queued`, `running`, `retry_wait`, `completed`, or `failed` state. The global execution ceiling defaults to 10; a separate one-slot FileList gate preserves tracker ordering and rate safety. A title refresh receives 30 minutes of active execution by default after acquiring its slots, so queue/rate waiting is excluded. Explicit HTTP 429 responses receive short bounded retries and persist the provider reset time when the wait is longer. Other transient failures are marked retryable and retried automatically every hour. Terminal jobs can also be retried manually, including completed metadata work, which deliberately bypasses the cached-success short circuit.

Each job attempt appends structured phase logs to `job_logs`; the newest 500 entries per job and 30 days globally are retained. Details and paginated older logs are available in both clients without exposing credentials. State transitions and metadata/catalog/search completion are appended to `event_journal` and broadcast live. Cold SSE connections do not replay history; reconnect clients may request at most 200 missed events with `Last-Event-ID` or `after=`. Cancellation and general crash-safe leases remain future hardening.

## Retention and eviction

Retention enforces the user-configured Managed download allocation and the free-space reserve (binary GiB; zero disables each check). An hourly persisted `retention` job — also run at boot and by manual retry — surveys distinct engine routes once per pass, counting each season-pack torrent once, and evicts one torrent at a time through the same engine remove path as a manual delete until the deficit clears or only protected downloads remain. Order follows the configured rule list (default oldest-completed); protections are user toggles defaulting to incomplete and actively-streamed (leased) downloads. Every eviction publishes `downloads.evicted` with its reason (`cap` or `reserve`). Catalog rows and Household state survive eviction, and prepare refuses a download the allocation cannot hold even after evicting everything unprotected (ADR-0004).

## Logging and resource envelope

The server writes structured JSON to both stdout/journald and `data/logs/server.log`. The Pi deployment installs a daily/10 MiB logrotate rule retaining 14 compressed rotations. Trusted TV clients may report bounded warning/error diagnostics through the HTTP API. The systemd unit uses a 1.5 GiB soft memory watermark and a 2 GiB hard ceiling; these are guardrails, not an application heap target.

## Security model

Release 1 has no client login. Remote addresses must fall within the configured trusted CIDRs; forwarded-address headers are ignored. FileList passkeys, Basic headers, torrent download URLs, qB credentials, settings contents, and absolute media paths are never returned to clients or written to normal logs.

Incomplete torrent paths use qBittorrent's effective `temp_path`; completed paths use `content_path`, falling back to `save_path` for older responses. Temporary and final locations must resolve beneath the configured download root or the request is rejected. The production service runs as `torrent-tv` with supplementary membership in the `qbittorrent` group so it can traverse and read the download tree.
