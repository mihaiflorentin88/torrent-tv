# FileList and Pirate Bay multi-tracker support

Date: 2026-09-07
Status: Design approved in conversation; written specification awaiting user review.

## Goal

Support FileList and The Pirate Bay concurrently through direct provider adapters. Keep discovery fast, make provider identity visible wherever media can be downloaded or played, and preserve access to managed media independently of whether its originating tracker is enabled.

This is an architectural extension of the existing application, not a new tracker framework. The four supported clients are the web app, Tizen, Android TV, and LG webOS. The three TV platforms share the TV application.

## Explicit user requirements

- Retain FileList and add Pirate Bay; allow multiple trackers to be enabled simultaneously.
- Use interfaces, DTOs, and adapters that allow future trackers without provider-specific client logic.
- Make the Pirate Bay website URL configurable, defaulting to `https://thepiratebay.org`.
- Provide settings to enable, disable, configure, and test trackers.
- Search and listing features aggregate releases from enabled, configured trackers.
- Display tracker provenance throughout search, listing, download, and playback surfaces on all four clients.
- **Every row or card in Downloads displays its tracker name**, including downloading, paused, completed, and failed items. Disabling the tracker does not remove the label.
- **Media already downloaded before its tracker was disabled remains playable**, including after a server restart. Disabling does not delete media or require the tracker to be contacted for existing managed playback.

The user selected direct adapters rather than an external Jackett/Prowlarr service and selected hiding disabled trackers from discovery while preserving managed downloads and history.

## Research and evidence

Pirate Bay's current frontend script, `https://thepiratebay.org/static/main.js`, uses a separate JSON backend at `https://apibay.org`. Its search HTML is a JavaScript shell, not populated release markup. A direct JSON adapter avoids browser automation and an extra service.

Read-only metadata probes during this research returned:

| Request | Result | Observed elapsed time |
| --- | --- | --- |
| `https://apibay.org/q.php?q=ubuntu` | HTTP 200, 100 records | 296, 48, and 53 ms across three requests |
| `https://apibay.org/t.php?id=59191690` | HTTP 200, Ubuntu release metadata and info hash | 815 ms; independent repeat 48 ms |
| `https://apibay.org/precompiled/data_top100_recent.json` | HTTP 200, 50 records | 44 ms |
| `https://apibay.org/precompiled/data_top100_200.json` | HTTP 200, 100 records | 50 ms |

These are point-in-time observations, not availability or latency guarantees. No torrent content was downloaded. Research found no published API stability contract or official developer documentation. Search and detail responses differ in numeric field representation; an empty search can return a synthetic record with ID zero rather than an empty array. The adapter must normalize these response differences.

Both maintained indexer definitions use the same JSON backend and distinguish the website address from the API host:

- https://github.com/Jackett/Jackett/blob/master/src/Jackett.Common/Definitions/thepiratebay.yml
- https://github.com/Prowlarr/Indexers/blob/master/definitions/v11/thepiratebay.yml

Search and listing endpoints expose bounded windows. The integration must not promise exhaustive remote pagination or a complete Pirate Bay mirror.

### Alternatives considered

1. **Direct provider adapters, selected.** Reuse the application's ports, keep deployment self-contained, and own provider compatibility in this repository.
2. **External indexer through Torznab.** Offers broader provider coverage but requires another service to operate and configure. It does not remove local identity, aggregation, magnet ingestion, or UI work.
3. **HTML scraping, rejected for this integration.** Current search HTML lacks populated records; executing the frontend to recover data would add complexity while still depending on the same backend.

## Existing architecture and constraints

Relevant boundaries and implementations:

- `internal/application/ports.go`: existing `Tracker`, `TrackerCapabilities`, `TorrentEngine`, and `Repository` ports.
- `internal/adapters/filelist/client.go`: FileList adapter, authentication, provider response mapping, and torrent-file retrieval.
- `internal/composition/container.go`: concrete dependency assembly, currently selecting one FileList tracker.
- `internal/application/service.go`: search and sync jobs, preparation, managed downloads, and household operations.
- `internal/application/catalog.go` and `torrentmeta.go`: canonical-title projections, release choices, and playable-file manifests.
- `internal/adapters/sqlite/repository.go`: durable releases, projections, manifests, downloads, jobs, and playback state.
- `internal/platform/config/` and `internal/adapters/httpapi/schema.go`: persisted configuration and schema-driven settings.
- `api/openapi.yaml` and `clients/shared/src/index.ts`: API description and hand-maintained shared TypeScript contract.
- `web/` and `clients/tv/`: the two UI implementations.

Ordinary browsing reads SQLite. Search jobs enrich that catalog. Releases currently use FileList's provider-local ID as their durable ID, and categories are FileList-specific. No explicit tracker provenance is carried through the current wire DTOs.

Both engine adapters currently ingest torrent-file bytes. Although the native engine's underlying library supports broader BitTorrent capabilities, this application's ports and adapters do not yet implement magnet ingestion. Adding Pirate Bay therefore includes the complete metadata-resolution and preparation flow for both engines.

Keep the decisions in ADR-0007: one active download engine per deployment; managed downloads retain their creating Engine route; inactive-engine downloads remain unavailable for that reason. Tracker disablement must not introduce another availability restriction. Normal retention from ADR-0004 remains unchanged.

## Vocabulary

A **Tracker** in this feature is the release-index provider, such as FileList or The Pirate Bay. It is distinct from a BitTorrent announce endpoint or per-engine tracker statistics.

A **Release** is a durable record belonging to a tracker. A **Canonical title** groups releases representing the same movie or series. A **Source** retains its existing meaning: a playable file or virtual episode entry within a release.

Use `trackerId` for provenance and **Tracker** for user-facing labels. Do not overload the existing Source concept or quality attributes. Update the FileList-specific Release and Category definitions in `CONTEXT.md` during implementation.

## Provider boundary

Extend the existing tracker port and introduce a registry of direct adapters. The registry resolves stable tracker IDs and exposes provider descriptors, capabilities, configuration readiness, and enabled state. It does not become a dynamic plugin system.

Responsibilities:

| Component | Responsibility |
| --- | --- |
| FileList adapter | Existing authentication, remote categories, API response mapping, and torrent-file acquisition |
| Pirate Bay adapter | Configured JSON endpoint, remote categories, response normalization, and info-hash/magnet acquisition reference |
| Tracker registry | Resolve the correct adapter and describe available providers |
| Application services | Enabled-provider selection, concurrent jobs, aggregate catalog behavior, preparation authorization, and failures |
| Repository | Persist identity, provenance, provider-scoped state, and existing application data |
| API and client DTOs | Stable provider-neutral data for UI rendering and actions |

Provider response DTOs stay inside adapters. Common DTOs and domain types must not expose FileList or Pirate Bay wire quirks. Capabilities describe supported operations so unsupported filters are not silently treated as supported. FileList credentials are sent only through its own adapter; a configurable Pirate Bay host never receives them.

The acquisition contract distinguishes torrent metainfo from a magnet reference. The provider adapter resolves what to acquire; the active download engine owns the BitTorrent session and metadata acquisition. Reuse existing manifest validation and preparation logic once metadata is available.

## Configuration and lifecycle

Add a Trackers section using the existing settings schema and persistence conventions:

- Independent enable toggles for FileList and Pirate Bay.
- Configuration readiness and a connection test per provider.
- Existing FileList URL, username, and passkey.
- Pirate Bay website URL default `https://thepiratebay.org`.
- Pirate Bay API URL default `https://apibay.org`, presented as an advanced setting.

Website and API URLs remain independently configurable. Website URLs support provider links; API URLs drive metadata requests. Configuration changes do not change release identity. Validate URLs through the existing settings validation boundary and preserve secret redaction and environment-managed setting behavior.

Settings apply to the single Household across devices. Enable toggles are available in web and TV settings. Do not add accounts or per-device tracker preferences. Keep the existing desktop settings binding compatible with the same configuration contract.

On upgrade, FileList retains its current enabled behavior and saved credentials. Pirate Bay is disabled until the user enables it. FileList credentials are required only while FileList is enabled; Pirate Bay-only operation is supported. Zero enabled trackers is valid and leaves existing managed media usable. Readiness means required configuration exists; a temporary connection failure does not silently disable a provider.

### Disabling

Disabling a tracker:

- Stops scheduling its remote searches and syncs and prevents queued work from starting new provider requests.
- Cancels in-flight provider work where possible; a late response must not make disabled releases visible.
- Removes its contributions from discovery results, category facets, counts, and new-download choices.
- Rejects new preparation from that tracker when no corresponding managed content can be reused.
- Retains catalog rows, provenance, manifests, downloads, favorites, and history.
- Does not pause or delete engine content or alter retention policy.

Already-managed media remains available through Downloads and existing library/history entry points. Playback reuses stored files, manifests, and the Engine route without requiring the originating provider to be enabled or reachable. This applies to movies, episodes, season packs, and other supported playable media and survives restart.

History whose content has been removed remains visible, but re-downloading requires enabling the tracker again. Existing engine activity, including peer and announce traffic for managed downloads, is unaffected: disabling an index provider is not a network firewall or a download-pause operation.

Re-enabling restores discovery visibility of its retained catalog rows and makes it eligible for subsequent search and sync work.

## Durable identity and migration

Each Release has:

- A globally unique internal identity used by application references.
- A stable `trackerId` independent of its configured URLs.
- The provider-local release ID used only with its originating adapter.
- Provider-local category identity, with normalized browsing metadata separate from that identity.

Uniqueness is scoped by tracker plus provider-local ID. IDs that happen to match across FileList and Pirate Bay must never overwrite or resolve to one another. A URL change must not create a second identity for the same release.

Migrate existing records as FileList releases. Preserve all relationships through catalog projections, manifests, managed downloads, playback state, derived Source identities, and persisted work referring to a release. Preserve favorites and canonical-title associations. Existing downloads display FileList after migration.

The schema migration must be atomic and restart-safe. If internal release identifiers change, migrate every dependent reference together rather than introducing an API alias or dual identity path. Resolve actual references, including serialized job payloads, before implementation. The observable contract is lossless playback and resume, not a particular ID encoding.

Persist provenance with managed-download data so listing or playing an existing download does not depend on a live provider query. Provider URL changes and disablement cannot erase it. Do not deduplicate releases by name. Canonical-title grouping continues to use existing matching rules, keeping separately labeled release choices. Existing engine-level reuse must preserve the requested release's provenance and must not mix private and public discovery settings.

## Search, listings, and synchronization

Preserve local-catalog browsing as the fast path:

1. Search returns currently indexed matches from enabled, configured providers.
2. The existing background-job mechanism searches eligible providers concurrently.
3. Each provider's results enter the catalog independently and become visible without waiting for all providers.
4. Existing job/result refresh behavior reports completion, partial failure, or no matches.

Use provider-scoped job deduplication, progress, and sync state. A failed provider does not roll back another's results. Respect each adapter's existing request limits and the application's outbound conventions; do not run provider calls serially through a global FileList throttle.

Latest and category synchronization collect the available windows from each eligible provider. Rebuild refreshes supported windows and local projections; it does not claim full remote-index coverage. Unsupported remote pagination must not produce fake pages. Local aggregate pagination remains available over indexed data.

Apply provider eligibility before grouping, sorting, counting, faceting, and pagination. A disabled-only title is absent from discovery; a title with enabled releases remains and displays only its eligible discovery choices. Managed Downloads and household history are separate from this discovery filter.

Normalize provider categories into shared browsing filters while retaining provider-local category identity. Equal category numbers from different trackers have no implied equivalence. Continue treating categories as hints, not authoritative movie/series classification.

Preserve current ranking behavior over eligible releases rather than adding an unrequested FileList-versus-Pirate-Bay priority system. Grouped titles may show multiple tracker names; individual releases always identify their actual provider.

## Magnet metadata and preparation

Both native and qBittorrent engines must handle torrent-file and magnet acquisition through the common contract. Do not rely on a third-party torrent-file cache.

Preparation stages:

1. Reuse existing managed media and persisted metadata when available, before consulting provider enablement for a new acquisition.
2. For a genuinely new acquisition, verify provider eligibility and resolve its acquisition reference.
3. If metainfo is missing, acquire it through the active engine with an explicit metadata-wait state, bounded timeout, and cancellation.
4. Validate the manifest through the existing path-safety rules and enumerate playable files.
5. Apply allocation and file/episode selection rules before normal payload downloading.
6. Create or reuse managed-download records and prepare the existing playback route.

Do not report playback readiness or selectable file indices before metadata exists. Avoid uncontrolled full-payload downloads merely to discover a file list. A cancelled or failed metadata-only session must not leave untracked, indefinitely active content.

Metadata timeout or temporary peer unavailability is a retryable acquisition failure, not proof that the release was removed. Persist successfully resolved manifests and engine session information so subsequent preparation and restarts reuse them.

Preserve private-torrent restrictions for FileList while providing appropriate public peer discovery for Pirate Bay. The current private-tracker-oriented native configuration must be reviewed rather than globally relaxed. Private torrent flags govern private-torrent behavior; provider provenance is not permission to leak private swarm discovery. Verify this boundary with the pinned engine library during implementation planning.

Movie playback, explicit file selection, season-pack preparation, episode selection, and resume must work through both engines. Keep existing progressive-playback and compatibility-stream behavior unchanged after preparation.

## API and UI contract

Update Go contract owners, HTTP projections, `api/openapi.yaml`, and the hand-maintained shared TypeScript client together. Add provenance to Release and Download data and expose aggregate tracker information where a canonical title represents multiple providers. Embedded releases in detail and household responses carry the same identity contract. Preserve provenance through prepare responses and playback context.

Use existing components and visual conventions. This is not a redesign.

| Surface | Required behavior |
| --- | --- |
| Release search results | Show the release's tracker name |
| Grouped title listings | Show eligible tracker names without implying one origin for all releases |
| Details and version chooser | Label each release choice by tracker |
| Episode and season-pack actions | Show the tracker beside download/play choices |
| Downloads | Label every row/card in every state; preserve label when disabled |
| Completed downloads | Keep Play available with tracker disabled |
| Continue watching, recent, and history | Show original tracker; preserve managed playback |
| Playback media information | Carry and show the selected release's tracker using the existing information surface |
| Settings | Independent toggles, configuration status, URLs/credentials, and provider tests |

Coverage applies to the web app and shared TV app, packaged for Tizen, Android TV, and LG webOS. Update FileList-only discovery copy to tracker-neutral wording. Do not confuse indexer provenance with existing seeder or announce statistics.

Required distinguishable states:

- No trackers enabled, with settings guidance and existing Downloads still accessible.
- Enabled provider missing required configuration.
- No matching releases.
- One or more providers failed while others returned usable data.
- Magnet metadata resolving, cancelled, or failed.
- Disabled provider with playable managed media.
- Disabled provider in history whose content must be downloaded again before playback.

Errors name the affected provider without exposing credentials. UI refresh behavior must react to provenance and provider eligibility changes, not just download progress changes.

## Verification and acceptance

Preserve regression tests where they defend observable contracts. Use live metadata smoke probes and actual application flows as proof rather than source-text assertions. Do not download arbitrary copyrighted media for validation; use controlled fixtures or authorized/publicly distributable content.

| ID | Acceptance scenario |
| --- | --- |
| MT-01 | FileList and Pirate Bay return the same provider-local ID; both remain distinct and prepare through the correct adapter. |
| MT-02 | Both enabled providers contribute to search and listings; faster results appear before a delayed provider finishes. |
| MT-03 | One provider fails; successful results remain usable and the failing provider is identified. |
| MT-04 | Disabling removes only that provider's discovery contributions, including counts, facets, pagination, and new-download choices. |
| MT-05 | A queued or late provider job cannot restore disabled releases to discovery or start prohibited new preparation. |
| MT-06 | A completed movie and season-pack episode remain playable with their tracker disabled, before and after restart, without a provider request. |
| MT-07 | Downloading, paused, completed, and failed rows/cards show the correct tracker in Downloads on web and all TV packages, including after disablement. |
| MT-08 | Existing FileList records migrate without loss of downloads, manifests, favorites, or resume positions; existing Downloads show FileList. |
| MT-09 | Torrent-file and magnet acquisition support movie/file selection and season-pack playback through native and qBittorrent engines. |
| MT-10 | Metadata timeout/cancellation yields an actionable state without false release removal or an indefinitely active unmanaged download. |
| MT-11 | FileList private-torrent behavior remains restricted while public magnet metadata acquisition works. |
| MT-12 | Website/API URL changes preserve identities and managed media; requests use the configured endpoint and keep credentials provider-scoped. |
| MT-13 | Pirate Bay-only and zero-enabled-provider configurations work; the latter still permits existing managed playback. |
| MT-14 | Re-enabling restores retained discovery contributions without duplicating releases. |
| MT-15 | Search, title details, version/episode/pack choices, Downloads, resume/history, and playback information show correct provenance in both UI implementations. |
| MT-16 | Empty-result sentinels and differing numeric representations normalize correctly; limited upstream windows are not represented as exhaustive results. |

Run migration verification against a populated pre-change database fixture, not only a fresh database. Exercise restart behavior rather than inferring persistence from serialization tests. Verify failed-provider and metadata-wait states through controllable delays and failures.

Use browser verification for web and the shared TV surface and platform-specific runtime smoke coverage where available. Build/package coverage alone is not physical-TV verification. Record which Tizen, Android TV, and webOS runtime or device was exercised; explicitly identify unavailable hardware without claiming it passed.

## Documentation and delivery boundary

Implementation updates the relevant domain definitions, architecture/API/configuration guides, client guidance, and changelog. Document the two Pirate Bay URLs, provider enablement semantics, bounded catalog windows, and unchanged retention and engine-routing rules. Reuse existing documentation rather than adding a parallel documentation hierarchy.

This specification does not implement the feature. After user review of this written artifact, invoke the writing-plans workflow to produce the implementation plan with migration, engine, backend, and client verification checkpoints.

## Non-goals

- External Jackett/Prowlarr deployment, Torznab integration, dynamic plugins, or additional tracker adapters.
- Multiple download engines active at once.
- Per-user accounts or per-device provider preferences.
- Exhaustive Pirate Bay mirroring, CAPTCHA bypass, browser scraping, or automatic mirror discovery.
- Third-party torrent-file cache fallbacks.
- A new recommendation/ranking system or unrelated UI redesign.
- Deleting managed media or overriding retention when a tracker is disabled.
