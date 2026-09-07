# Engine ownership and canonical title reconciliation

Date: 2026-09-08
Status: Written specification approved by the user on 2026-09-08; implementation planning authorized.

## Goal and scope

Use Native for new Pirate Bay acquisitions without losing access to existing qBittorrent-managed media. Consolidate duplicate catalog titles across trackers while preserving every release and household record. Retain concurrent tracker search using goroutines.

The user explicitly chose to keep existing downloads playable and approved owner-based engine routing and conservative title reconciliation. This reopens ADR-0007's one-active-engine decision. Affected clients are the web application and the shared TV application used by Tizen, Android TV, and webOS.

No media migration, automatic engine fallback, qBittorrent upgrade, new tracker, fuzzy matching, or unrelated UI redesign is included. Pi restart, deployment, publication, and release changes require separate authorization.

## Evidence

Read-only investigation of the Pi and current source established:

- Saved settings select Native, but the running process uses qBittorrent 4.3.9. Engine selection is captured at startup and changing it requires restart.
- The dependency diagnostic incorrectly combines the saved engine's label with the running engine's result: `Connected to native torrent engine: v4.3.9`.
- Native magnet resolution already exists. The reported version gate belongs to the qBittorrent adapter. The gate remains applicable to new qBittorrent magnets below 4.5.0, not legacy playback, torrent-file acquisition, or Native.
- Search already starts a goroutine per eligible tracker, bounded by global job capacity and same-tracker exclusion. Existing regressions cover fast-provider visibility and partial failure.
- The Pi stores three Silo series groups: 121 IMDb-backed releases (55 FileList and 66 Pirate Bay), 32 yearless Pirate Bay releases without IMDb, and two Pirate Bay packs with parsed year 2023 but no IMDb. All 155 records must survive consolidation.
- Current title IDs use either kind plus IMDb, or kind plus normalized title plus parsed year. These identity spaces do not converge. Upserts can erase a nonempty IMDb field when an incoming response omits it.
- Existing Silo downloads use `qb:` routes. Its watched/resume records are release/source-keyed and belong to releases in the IMDb-backed group.

Relevant boundaries: `internal/composition/container.go`, `internal/application/{ports,service,catalog,torrentmeta,streaming,subtitles,retention}.go`, `internal/adapters/sqlite/repository.go`, `internal/platform/config/`, and `internal/adapters/httpapi/`.

## Alternatives considered

### Engines

Selected: one acquisition default, with managed operations routed through each download's persisted owner. Reuse the existing `TorrentEngine` port and `native:` / `qb:` route format.

Rejected: importing qBittorrent state into Native. That requires file verification and migration the user did not request.

Rejected: preserving completed-file playback alone. That leaves incomplete downloads, progressive playback, and torrent controls unavailable.

### Titles

Selected: IMDb-first identity with exact-name, same-kind matching for missing-IMDb releases only when one compatible candidate exists.

Rejected: IMDb-only grouping, which leaves the reported duplicates. Also rejected: aggressive name-only or fuzzy merging, which can combine unrelated works or remakes.

## Engine design

### Acquisition default and ownership

Replace the service's single-engine dependency with an explicit owner lookup using the existing prefixes. The acquisition default is a separate startup selection. Capture it once for each new acquisition so metadata resolution and torrent addition use the same engine.

Composition initializes the selected engine and any other engine required by persisted managed routes. A Native-only installation without qBittorrent downloads does not gain a qBittorrent service dependency. A qBittorrent-only installation without Native downloads need not start an unused native session. qBittorrent construction remains lazy with respect to network connectivity.

Existing managed releases retain their owner, including additional files or episodes from an already-managed season pack. Never overwrite an existing route because the default changed. Do not redownload through another engine when an owner is unavailable.

Keep selection restart-required. Saving changes does not hot-switch the acquisition default. No restart is performed as part of local implementation without separate authorization.

### Managed operations

Resolve each full route into its owning engine and hash for:

- Status, file discovery, selection, retry, pause/resume, and removal.
- Existing-file and season-pack preparation.
- Progressive reads, piece waits, range prioritization, subtitle access, and manifest inspection.
- Download listing, retention surveying, storage admission, and eviction.

Migrate both `route()` callers and direct uses of the former single engine. Remove obsolete single-engine setters and paths rather than retaining compatibility layers. Unknown prefixes report unavailable, never the default engine.

Group sibling files by full route for status sampling and deletion. Equal hashes in different engines are distinct owners. Preserve current physical storage ownership; do not infer that arbitrary external qBittorrent paths are disjoint merely because Native uses hash directories. Do not move files or configure both engines to write the same destination. Existing path-validation protections remain mandatory.

### Failure isolation and shutdown

An unavailable qBittorrent daemon must not prevent Native startup, acquisition, or playback. Incomplete downloads owned by an unavailable engine report that failure. Preserve the existing completed-local-file path when the file exists and passes download-root validation.

Native initialization failure remains fatal when Native is the acquisition default. If Native is needed only for legacy managed routes and qBittorrent is the default, retain the initialization error as an unavailable-owner state without blocking healthy qBittorrent operations. Do not substitute a different acquisition engine.

Stop and join service workers before closing constructed engines and the repository. Close each engine once. Preserve shutdown-timeout safety: never close resources beneath workers that have not stopped.

### Storage and deletion

Allocation covers the combined managed footprint of both engines. Survey each full route once, preserving the current interpretation of engine-reported torrent size. The download-root free-space reserve remains global. Existing eviction protections and ordering apply across both owners.

Do not count an unreachable owner's footprint as zero. If the survey cannot establish the managed footprint, report that uncertainty and refuse new admission dependent on the incomplete allocation calculation. Existing playback remains available. Do not evict unreachable routes or forget their rows after failed deletion.

Removal calls only the owning engine and forgets only exact-route siblings after success or the existing confirmed-not-found handling. Catalog records and household history remain independent of media eviction.

### Settings and diagnostics

Expose the running acquisition default separately from the saved selection through existing system/settings contract surfaces. Derive the selection restart notice from the mismatch so it survives page reload. Dependency tests must name the engine actually tested.

Update web and shared TV settings to explain that selection controls new acquisitions and existing downloads retain their owner. Display running and saved selections when they differ. Preserve tracker labels in all download states. This is a focused settings correction, not a visual redesign; no visual-companion decision was needed for the architecture.

## Canonical title design

### Matching rules

Keep provider facts separate from canonical associations. Preserve every release's tracker/provider ID, source identity, filename, quality, language, season/episode information, and acquisition data.

1. Explicit IMDb identity is authoritative. Different known IMDb identities never merge by name alone.
2. A missing-IMDb release may join an IMDb-backed title only when normalized title and media kind match exactly and exactly one compatible candidate remains. Reuse existing normalization; no fuzzy matching.
3. Conflicting known movie years disqualify candidates. Missing years must not resolve an ambiguity between remakes.
4. A series release year may describe a later season rather than the premiere. Prefer existing canonical metadata when interpreting premiere year. Never split an established IMDb identity by release year. A sole exact-name series anchor may absorb its missing-IMDb season/episode releases; multiple plausible series anchors remain ambiguous.
5. Without an IMDb-backed candidate, retain existing fallback grouping rather than inventing identity from weak evidence.
6. Preserve a nonempty provider IMDb field when a subsequent response merely omits it. A conflicting nonempty incoming identity is changed evidence and must be reconciled, not silently discarded. Never write an inferred canonical match into the release as if its tracker supplied that IMDb ID.

This confidence policy fixes the observed Silo fragments but deliberately permits unresolved cards where evidence is ambiguous. Exact title equality is not a global uniqueness guarantee.

### Durable projection and ordering

Reconcile at the repository/projection boundary, not in the frontend. Catalog queries and source/detail grouping consume the same resolved title identity instead of independently recomputing keys from raw releases.

Resolve affected name/kind families using their complete current evidence, not arrival order or the first row seen. Keep raw evidence so inferred membership can be reconsidered if another conflicting candidate appears. Do not permanently attach ambiguous releases through stale redirects.

If later evidence makes a previous inferred association ambiguous, restore those releases to their evidence-based fallback groups. Existing favorites remain on their established canonical target; do not copy a favorite onto a newly discovered unrelated work. Explicit metadata remains with its explicit identity. Source-keyed history continues to follow its unchanged source.

Rekey catalog projections and title-keyed references transactionally. Move favorites with uniqueness-preserving union. Prefer IMDb-backed canonical metadata on a merge, retaining the freshest compatible record rather than duplicating or orphaning it. Do not alter release/source-keyed downloads, playback state, manifests, subtitle preferences, or engine routes.

Migrate all title-ID producers and consumers, including favorites, detail/source grouping, managed-title fallback, refresh scheduling, and next-episode navigation. Queued title-refresh work must be retargeted or coalesced transactionally when a title is merged; workers re-resolve their target before applying results so in-flight work cannot restore obsolete grouping. Completed job history may remain unchanged.

Run idempotent existing-cache reconciliation after required schema migrations and before serving the catalog. No tracker refresh, release reimport, or media operation is needed. Subsequent ingestion reconciles affected families as part of persistence. Resolve grouping before pagination and counting.

Tracker enablement stays a discovery filter, not an identity rewrite or deletion trigger. Reconciliation must not expose disabled-only discovery rows. Managed access and permanent tracker provenance remain intact.

## Concurrent search

Retain one goroutine per enabled, configured tracker with the existing global job limit. Same-tracker exclusion serializes operations against one provider; distinct providers run concurrently when capacity permits.

Preserve cancellation, mid-flight disable checks, partial-success handling, and per-provider completion events. A fast provider's persisted results remain queryable before a slow provider completes. Reconciliation must converge to the same final catalog for either arrival order.

No additional worker pool, removal of the global bound, or unrelated retry/scheduler change is required.

## Verification and acceptance

Reuse existing coverage. Keep behavioral regressions for uncertain changed contracts and exercise the application with controlled torrent content.

### Engines and clients

- Pirate Bay magnets download through the built-in Native engine: exercise the real tracker acquisition adapter, metadata resolution, file selection, and persisted `native:` ownership without qBittorrent or its version gate.
- FileList `.torrent` acquisitions also download through Native, exercising the real tracker acquisition adapter rather than substituting an engine-only add call.
- For each tracker separately, stream controlled legal media through the application HTTP playback path while the selected file is incomplete. Record incomplete progress when playback begins, verify delivered media bytes, and continue playback as additional pieces arrive. A completed-file read or successful metadata resolution alone does not pass this requirement.
- Both Native acquisition and progressive-streaming checks run without a reachable qBittorrent daemon; no silent fallback is allowed. Local tracker-response fixtures must be reported as fixtures, not as proof of live tracker availability.
- qBittorrent is strictly secondary: a Native-only installation starts, acquires, and streams with no qBittorrent configured, installed, or reachable; nothing in startup, admission, playback, or diagnostics may require or wait on qBittorrent when Native is the default.
- Web, desktop, and shared TV clients (Tizen, Android TV, webOS) remain fully compatible: no save-format breaks, byte-identical desktop/HTTP settings parity, and corrected engine feedback on every surface.
- Existing completed and incomplete `qb:` downloads remain usable with Native selected; requesting another episode from a managed season pack retains its route.
- qBittorrent 4.3.9 legacy operations remain supported, while new magnets through that engine retain the compatibility error.
- qBittorrent downtime isolates its incomplete media without breaking Native or valid completed-local-file playback.
- Deletion and allocation cover both owners with existing protections; equal hashes cannot cause wrong-owner deletion or overwrite managed routes.
- Startup and shutdown cover selected/secondary failure without leaked workers or resource closure under active work.
- Browser verification covers running/saved engine feedback, page reload, correct diagnostic labels, and consolidated catalog/detail navigation. Exercise the shared TV surface with its available runtime and report any hardware-only limitations explicitly.

### Catalog and concurrency

- A fixture matching the three observed Silo groups yields one title while retaining all 155 releases and both tracker labels.
- Different IMDb identities, ambiguous remakes, incompatible kinds, and similar-but-not-equal names remain separate.
- Opposite arrival orders, omitted IMDb fields, new conflicting evidence, repeated reconciliation, and database reopen preserve valid identities.
- Favorites, metadata, watched/resume state, source selections, and engine routes survive migration.
- Counts, pagination, detail sources, next-episode navigation, queued refreshes, and disabled-tracker managed access use reconciled identities consistently.
- Existing concurrency and partial-failure regressions pass; exercise relevant concurrent ingestion under the Go race detector.

Use a local controlled peer/swarm for Native runtime proof, not copyrighted content. Exercise mixed-owner routing with controlled native/qBittorrent media. Local verification is not proof that the Pi is fixed; report deployment verification separately if later authorized.

## Documentation and integration

During implementation, update ADR-0007 and affected existing architecture/domain/API/settings documentation to replace the single-active-engine rule. Synchronize OpenAPI and shared TypeScript contracts for any running-selection fields. Update every affected caller in a clean cutover.

The approved written specification is the input to implementation planning. Application implementation begins only after the written-spec review gate and planning workflow.

## Separate feed-server fix

The separately approved bounded fix in `../filelist-ads-server` is already implemented locally in `domain/updates/sync.go` and `domain/updates/sync_test.go`. It permits matching-version `.webos.ipk` and `.webos.ipk.sha256` assets without relaxing unknown/wrong-version rejection. The new regression failed with the old allowlist and passed after the change; focused verification also passed in the main session.

No feed-server commit, push, or deployment has been performed. This fix is not a dependency of the engine/catalog work. The production notification's HTTP rejection status remains unobserved; local allowlist evidence does not establish an authenticated live HTTP 422.
