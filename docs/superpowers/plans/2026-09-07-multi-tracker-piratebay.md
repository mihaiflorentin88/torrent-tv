# FileList and Pirate Bay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Support concurrent FileList and Pirate Bay discovery with tracker provenance throughout all clients, while keeping downloaded media playable after disabling its originating tracker.

**Architecture:** Extend the existing tracker boundary with direct adapters and a registry. Preserve local SQLite browsing, persist collision-free provider identity, and resolve magnets to metainfo through the active engine before using the existing manifest and preparation pipeline. Web and the shared TV app consume one provider-neutral API contract.

**Tech Stack:** Go; modernc SQLite; anacrolix/torrent v1.61.0; qBittorrent WebUI API; TypeScript/Preact; the existing web, shared, TV, webOS, and desktop npm workspaces; Tizen, Android TV, and webOS platform shells.

**Spec:** `docs/superpowers/specs/2026-09-07-multi-tracker-piratebay-design.md`

## Global Constraints

- Pirate Bay website default: `https://thepiratebay.org`; independent API default: `https://apibay.org`.
- FileList and Pirate Bay may both be enabled. Pirate Bay starts disabled; FileList retains its existing enabled behavior on upgrade.
- Settings apply to the single Household. Zero enabled trackers and Pirate Bay-only configurations are valid.
- Disabled trackers contribute no discovery results, counts, facets, or new-download choices.
- **Previously downloaded media remains playable with its tracker disabled, including after restart, without a provider request.** Existing engine-route and retention rules are unchanged.
- **Every Downloads row/card shows the tracker name**, including downloading, paused, completed, and failed states, across web, Tizen, Android TV, and LG webOS.
- Keep one active download engine. New magnets require qBittorrent **4.5.0+**; retain existing older-version torrent-file and managed-playback compatibility. The user explicitly approved this boundary during planning.
- Keep native `NoDHT = true`. Pirate Bay magnets carry public announce endpoints; no second native client or DHT-only resolver is introduced.
- Preserve private-tracker credentials and swarm isolation. Never merge announce lists into a preexisting torrent merely because an acquisition has the same info hash.
- No external indexer, HTML scraping, automatic mirror discovery, third-party torrent-file cache, account system, or unrelated redesign.
- No release-name deduplication. Provider-local release/category IDs are not globally unique.
- Preserve the existing Tizen 5.0, Android 8.0 / minSdk 26, and webOS TV 4.0 floors from `CONTEXT.md` and the relevant ADRs.
- Use LSP references before changing exported contracts. Update every caller and fake; remove the obsolete `TrackerCatalog` compatibility interface at the service cutover.
- Permanent tests must defend behavior. UI work is proved on actual browser/runtime surfaces; do not add source-string or component-wiring tests.
- No implementation began during planning. Repository paths and evidence were inspected at the design commit `9067256`; re-resolve symbols if the branch has moved.

---

## Execution layout and review boundaries

Use an isolated execution workspace before changing production code. Read the spec and `CONTEXT.md` first. This is one integrated feature, not independently deployable partial products.

| Task | Deliverable | Depends on |
| --- | --- | --- |
| 1 | Durable identity, provenance, metainfo persistence, and eligible SQL queries | none |
| 2 | Tracker settings and conditional onboarding | none |
| 3 | Pirate Bay adapter and normalized acquisition data | 1's data contract |
| 4 | Safe native metadata-only resolution | acquisition contract below |
| 5 | Safe qBittorrent metadata-only resolution with version gate | acquisition contract below |
| 6 | Registry cutover, concurrent provider jobs, and managed-first preparation | 1–5 |
| 7 | HTTP/OpenAPI/shared-client contract and cancellation | 6 |
| 8 | Web tracker settings and provenance surfaces | 7 |
| 9 | Shared TV tracker settings and provenance surfaces | 7 |
| 10 | Integrated migration, playback, failure, and platform verification | 8–9 |

After the shared contracts are agreed by the executing agent, Tasks 2–5 can be researched/implemented in separate worktrees. Tasks 8 and 9 can run concurrently. Do not have workers independently edit `ports.go`, `models.go`, or shared TypeScript contracts; their owning tasks integrate those changes once.

Tasks 3–5 can test adapter methods directly before Task 6 installs the new service port. Do not add temporary production no-op implementations or compatibility wrappers to make intermediate builds pass. At Task 6, migrate the entire service boundary and remove obsolete methods in the same integration commit.

### File ownership

| Files | Responsibility |
| --- | --- |
| `internal/domain/models.go`, `catalog.go`, new `tracker.go` | Durable provenance, acquisition DTO, provider descriptors, discovery query scope |
| `internal/adapters/sqlite/repository.go`, `repository_test.go` | Migration, normalized upsert return values, eligible queries, metainfo persistence |
| `internal/platform/config/config.go`, `onboarding.go`, existing tests | Four new settings and conditional credential requirements |
| `internal/adapters/filelist/client.go`, `client_test.go` | Preserve FileList protocol and normalize identity/category/acquisition |
| new `internal/adapters/piratebay/client.go`, `client_test.go` | Pirate Bay JSON protocol only |
| `internal/adapters/nativetorrent/client.go`, new `magnet.go`, `magnet_test.go` | Native metadata resolution and acquisition ownership |
| `internal/adapters/qbittorrent/client.go`, new `magnet.go`, `magnet_test.go` | qB API capability gate, metadata export, resolver lifecycle |
| `internal/application/ports.go`, `service.go`, `catalog.go`, `torrentmeta.go`, new `trackers.go` | Provider registry, jobs, eligibility, managed-first preparation |
| `internal/composition/container.go` | Concrete adapters and live settings callbacks |
| `internal/adapters/httpapi/api.go`, `schema.go`, `api/openapi.yaml` | HTTP provenance, settings, provider test/error contracts |
| `clients/shared/src/index.ts`, existing shared tests | Shared DTOs, download reconciliation, abortable acquisition calls |
| `web/src.tsx`, `downloads.tsx`, `settings.tsx` | Web presentation and interactions |
| `clients/tv/src/main.tsx`, existing TV styles/navigation | Shared Tizen/Android TV/webOS presentation and remote interaction |
| `internal/gui/bindings.go`, existing binding tests | Settings-save notification and desktop contract parity where needed |

Do not split the large application files wholesale. Extract only new provider orchestration into `trackers.go` and new engine resolution into `magnet.go`.

## Contract ledger

These names are the shared contract for all tasks. Existing unrelated fields and engine methods remain unchanged.

### Domain additions

In `internal/domain/tracker.go`:

```go
package domain

import "errors"

type TrackerRef struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

type TrackerCategory struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    BrowseClass string `json:"browseClass"`
    Excluded    bool   `json:"excluded"`
}

type TorrentAcquisition struct {
    Metainfo []byte
    Magnet   string
}

func (a TorrentAcquisition) Validate() error {
    if (len(a.Metainfo) == 0) == (a.Magnet == "") {
        return errors.New("acquisition requires exactly one of metainfo or magnet")
    }
    if len(a.Metainfo) >= 16<<20 {
        return errors.New("torrent metadata is too large")
    }
    return nil
}

var ErrTrackerDisabled = errors.New("tracker is disabled")
var ErrTrackerUnconfigured = errors.New("tracker is not configured")
var ErrMagnetUnsupported = errors.New("new magnets require qBittorrent 4.5.0 or newer, or the native engine")
```

Add these required fields to `TorrentRelease`:

```go
TrackerID  string `json:"trackerId"`
TrackerName string `json:"trackerName"`
ProviderID string `json:"providerId"`
CategoryID string `json:"categoryId"`
BrowseClass string `json:"browseClass"`
```

`Category` remains the display category; `CategoryID` is provider-local. `BrowseClass` is one of `video`, `audio`, `software`, `games`, `adult`, `other`; it never determines movie/series kind. Add server-only `DiscoveryExcluded bool` with `json:"-"` to TorrentRelease and persist `discovery_excluded INTEGER NOT NULL DEFAULT 0`. Adapters set it from TrackerCategory.Excluded. FileList preserves every existing DefaultBlacklisted value rather than replacing its policy with a broad-class guess. Existing quality/resolution metadata remains parsed from release names.

Add `TrackerID` and `TrackerName` to `Download`, persisted rather than requiring a provider call. Add `Trackers []TrackerRef` to `CatalogTitle`. Add `TrackerIDs []string` to `CatalogQuery`; both nil and empty mean **no eligible discovery rows**, never all rows. Add `Metainfo []byte` with `json:"-"` to `TorrentManifest` for reusable metainfo. Manifest bytes never appear in client responses.

### Tracker and engine ports at Task 6 cutover

```go
type Tracker interface {
    ID() string
    Name() string
    Capabilities() TrackerCapabilities
    Categories() []domain.TrackerCategory
    Latest(context.Context) ([]domain.TorrentRelease, error)
    Category(context.Context, string) ([]domain.TorrentRelease, error)
    Search(context.Context, string) ([]domain.TorrentRelease, error)
    Acquire(context.Context, string) (domain.TorrentAcquisition, error)
}
```

`Acquire` takes **ProviderID**, never the internal release ID. Retain the existing `TrackerCapabilities` flags. Add `ResolveMagnet(context.Context, string, string) ([]byte, error)` to `TorrentEngine`; arguments are context, magnet URI, and download root. Keep `Add(context.Context, io.Reader, string) (string, error)` unchanged. Resolve returns bounded, hash-verified metainfo and leaves no resolver-owned active torrent behind; preexisting torrents are not altered or removed.

In `application/trackers.go`:

```go
type TrackerRegistration struct {
    Adapter    Tracker
    Enabled    func() bool
    Configured func() bool
}

type TrackerStatus struct {
    ID           string              `json:"id"`
    Name         string              `json:"name"`
    Enabled      bool                `json:"enabled"`
    Configured   bool                `json:"configured"`
    Capabilities TrackerCapabilities `json:"capabilities"`
}
```

Define concrete `TrackerRegistry` with an ordered registration slice and ID lookup map. Its methods are `NewTrackerRegistry(...TrackerRegistration) (*TrackerRegistry,error)`, `Lookup(id string) (Tracker,bool)`, `Eligible() []Tracker`, `EligibleIDs() []string`, `RequireEligible(id string) (Tracker,error)`, and `Status() []TrackerStatus`. Constructor rejects empty/duplicate IDs and nil adapters/callbacks. Callbacks read current settings, not a construction-time snapshot. Unknown IDs are errors; never fall back to FileList.

Change `NewService`'s first argument to `*TrackerRegistry`. The application owns orchestration; neither clients nor repositories instantiate adapters.

### Repository cutover

Change `UpsertReleases` to return `([]domain.TorrentRelease,error)`. Returned values contain stored canonical IDs. Never mutate the caller's input slice as an undocumented side effect.

Pass explicit eligible IDs to discovery reads:

```go
ListReleases(context.Context, string, string, int, int, []string) (domain.Page[domain.TorrentRelease], error)
ListCatalogSources(context.Context, []string) ([]domain.CatalogSource, error)
ListCatalogSourcesByTitleIDs(context.Context, []string, []string) ([]domain.CatalogSource, error)
CatalogFacets(context.Context, []string) (domain.CatalogFacets, error)
CatalogCounts(context.Context, []string) (int, int, error)
```

`QueryCatalogTitleIDs` consumes `CatalogQuery.TrackerIDs`. Leave `GetRelease`, download reads, playback, favorites, subtitles, and manifest lookup unfiltered. Household logic that previously used an all-catalog query must load its explicitly referenced releases rather than accidentally passing an empty discovery scope.

### Settings and HTTP additions

Settings keys: `fileListEnabled`, `pirateBayEnabled`, `pirateBayWebsiteUrl`, `pirateBayApiUrl`. Go fields: `FileListEnabled`, `PirateBayEnabled`, `PirateBayWebsiteURL`, `PirateBayAPIURL`.

Add `GET /api/v1/trackers` returning `[]TrackerStatus`. Keep `/dependencies/{name}/test` and its current response shape; registered tracker IDs are dispatched through `Service.TestTracker(ctx,id) (int,error)`. A deliberate connection test may contact a disabled but configured provider; it does not ingest releases or turn the provider on.

Keep the existing search response's parent `job`. Provider subjobs use the existing Job model plus a persisted `TrackerID string` field. Extend `SearchResult` with `trackers: TrackerStatus[]`; provider subjob updates are available through the existing job/event surfaces. No second event transport is introduced.

## Task 1: Durable identity, provenance, and discovery scope

**Files:** Domain files in the ledger; SQLite repository/tests; repository-port declarations and all affected call sites/fakes.

**Consumes:** Existing tables and opaque release IDs.

**Produces:** Provenance fields, stable normalized upserts, metainfo storage, explicit eligible discovery reads. Acceptance MT-01, MT-04, MT-08, MT-12, MT-13, MT-14.

- [ ] **Capture the pre-change migration fixture before modifying the repository.** Run a throwaway Go fixture generator inside the execution worktree using current `sqlite.Open`. Seed two FileList releases, a manifest, a completed managed download, playback, playback preferences, a favorite, and a subtitle asset through existing repository methods. Preserve the generated SQLite database as `internal/adapters/sqlite/testdata/pre-trackers.db`; close the connection before copying it so no WAL content is omitted. Use synthetic names and temporary paths, not household data. In `repository_test.go`, copy this fixture to `t.TempDir()` and open the copy; assert all original IDs and relations after migration and reopening.

- [ ] **Write a collision/upsert regression before changing production code.** The new `UpsertReleases` test body uses the contract below; add `context` and `domain` imports alongside existing test imports:

```go
func TestProviderIDsDoNotCollide(t *testing.T) {
    r, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
    if err != nil { t.Fatal(err) }
    defer r.Close()
    input := []domain.TorrentRelease{
        {ID:"filelist:42", TrackerID:"filelist", TrackerName:"FileList", ProviderID:"42", Name:"Film.2024.1080p", Category:"Movies HD", CategoryID:"4", BrowseClass:"video", Seeders:3},
        {ID:"piratebay:42", TrackerID:"piratebay", TrackerName:"The Pirate Bay", ProviderID:"42", Name:"Film.2024.1080p", Category:"Video", CategoryID:"201", BrowseClass:"video", Seeders:7},
    }
    stored, err := r.UpsertReleases(context.Background(), input)
    if err != nil { t.Fatal(err) }
    if len(stored) != 2 || stored[0].ID == stored[1].ID { t.Fatalf("collision: %+v", stored) }
    for _, want := range stored {
        got, err := r.GetRelease(context.Background(), want.ID)
        if err != nil || got.TrackerID != want.TrackerID || got.ProviderID != "42" { t.Fatalf("wrong release: %+v, %v", got, err) }
    }
    empty, err := r.ListReleases(context.Background(), "", "", 20, 0, nil)
    if err != nil || empty.Total != 0 { t.Fatalf("empty eligibility exposed releases: %+v, %v", empty, err) }
}
```

- [ ] **Run the red test.** `go test ./internal/adapters/sqlite -run 'TestProviderIDsDoNotCollide|TestPreTrackerMigration' -count=1`. Before contract changes a compile failure for the new fields/signature is expected; then run again once declarations exist to observe behavioral failure.

- [ ] **Implement the migration transaction.** Use the existing migration style, but perform this feature's ALTER/backfill/index sequence atomically on one connection. Ignore only the known duplicate-column condition on restart. Preserve every existing `releases.id` byte-for-byte. Add `tracker_id`, `tracker_name`, `provider_id`, `category_id`, `browse_class`; backfill existing records as FileList. Add provenance columns to downloads, `metainfo BLOB` to manifests, and `tracker_id` to jobs. Populate download provenance from persisted releases, with FileList assigned to existing orphan download rows because the pre-change application only had that provider.

Core ordering:

```sql
ALTER TABLE releases ADD COLUMN tracker_id TEXT NOT NULL DEFAULT 'filelist';
ALTER TABLE releases ADD COLUMN tracker_name TEXT NOT NULL DEFAULT 'FileList';
ALTER TABLE releases ADD COLUMN provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN category_id TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN browse_class TEXT NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN discovery_excluded INTEGER NOT NULL DEFAULT 0;
UPDATE releases SET provider_id = id WHERE provider_id = '';
CREATE UNIQUE INDEX IF NOT EXISTS releases_tracker_provider
    ON releases(tracker_id, provider_id);
```

Backfill category IDs/classes and discovery_excluded using the existing FileList category table's names, IDs, and DefaultBlacklisted values. Unknown historical categories get `other` without changing the existing unknown-category exclusion behavior. Do not rewrite Source IDs, downloads, playback keys, favorites, or event history: their release IDs remain valid.

- [ ] **Implement normalized upserts.** Generate namespaced candidate IDs for new rows using `trackerID + ":" + url.PathEscape(providerID)`. Validate nonempty provider identity before persistence. Change the conflict target to `(tracker_id,provider_id)` and use `RETURNING id`. Update fields without replacing the existing primary key. Build the catalog projection with the returned ID and return normalized releases to callers. An updated FileList release from a legacy fixture must return its original unprefixed opaque ID. Migrate every `UpsertReleases` caller, including sync, search, title expansion, backfill, and tests; the reconnaissance count is not a substitute for LSP references.

- [ ] **Implement eligibility before aggregation.** Add a parameterized `r.tracker_id IN (...)` term and `r.discovery_excluded = 0` to `catalogFilter`, the raw-release query, facets, counts, and source queries. For an empty provider set append SQL `0`, not an absent predicate. Keep internal release/download lookups unfiltered. Assert that a disabled high-seeder release cannot affect a remaining title's ordering, source count, facets, or tracker labels.

- [ ] **Persist metainfo with manifests.** Keep null/empty metainfo valid for migrated manifests; managed playback still works from old manifests and files. New metainfo is capped below 16 MiB and remains excluded from wire JSON. Save manifest files and bytes together.

- [ ] **Run green and migration reopen checks.** `go test ./internal/adapters/sqlite ./internal/domain ./internal/application -count=1`. Open the populated old fixture, verify all references, close/reopen, and repeat the observable playback/favorite/provenance assertions. Do not merely assert that columns exist.

- [ ] **Commit:** `feat: persist tracker identity and scoped catalog queries`.

## Task 2: Tracker settings and conditional onboarding

**Files:** `internal/platform/config/config.go`, `onboarding.go`, existing config tests; `internal/adapters/httpapi/schema.go`; existing settings/binding tests.

**Consumes:** Existing atomic settings store, secret merge, environment overrides, schema fields.

**Produces:** The four settings keys in the ledger. Acceptance MT-12, MT-13.

- [ ] **Write a regression for Pirate Bay-only startup and a saved disable toggle.** Use `config.LoadAt` with a settings path under `t.TempDir()` rather than changing process cwd in new tests. Set an explicit writable download root, disable FileList, enable Pirate Bay, clear FileList credentials, save, reload, and assert `MissingRequired()` contains no FileList credential keys and both booleans survive. Then enable FileList and assert the missing credentials are reported. Keep root-required semantics unchanged.

A table for `MissingRequired` must distinguish these transitions, not merely default values:

```go
cases := []struct {
    enabled bool
    user, pass string
    wantCredentials bool
}{
    {false, "", "", false},
    {true, "", "", true},
    {true, "user", "pass", false},
}
```

- [ ] **Run red:** `go test ./internal/platform/config -run 'TestTracker|TestMissing|TestPrompt' -count=1`.

- [ ] **Add settings and defaults.** Preserve existing configuration loading from defaults before decoding saved JSON:

```go
FileListEnabled     bool   `json:"fileListEnabled"`
PirateBayEnabled    bool   `json:"pirateBayEnabled"`
PirateBayWebsiteURL string `json:"pirateBayWebsiteUrl"`
PirateBayAPIURL     string `json:"pirateBayApiUrl"`
```

Defaults are `true`, `false`, `https://thepiratebay.org`, and `https://apibay.org`. Do not use `omitempty` on booleans. Validate configured URLs as HTTP(S) URLs with a host and no embedded userinfo. Keep website and API independent. Existing environment-managed/read-only behavior applies by JSON key.

- [ ] **Gate onboarding in both interactive and noninteractive paths.** `MissingRequired` and `PromptRequired` must skip `fileListUsername` and `fileListPasskey` when FileList is disabled. Preserve `downloadRoot` handling and secret preservation when blank form inputs are saved. Do not demand credentials merely to launch the HTTP/settings surface.

- [ ] **Add schema fields.** Toggles are TV-visible, non-sensitive, and not restart-required. Website URL is a normal tracker configuration field; API URL is advanced. Do not invent a schema field-type system: web settings already render checkboxes from row definitions, and TV settings render explicit controls.

- [ ] **Run green:** `go test ./internal/platform/config ./internal/adapters/httpapi ./internal/gui -count=1`. Recheck secret redaction, environment-managed toggles, explicit false persistence, and HTTP/desktop schema parity through existing behavioral tests.

- [ ] **Commit:** `feat: configure independent tracker enablement`.

## Task 3: Pirate Bay adapter and normalized acquisition

**Files:** New `internal/adapters/piratebay/client.go`, `client_test.go`; FileList adapter/tests; domain acquisition types from Task 1.

**Consumes:** `TorrentRelease`, `TrackerCategory`, `TorrentAcquisition` and live settings callbacks.

**Produces:** A direct Pirate Bay client with the final Tracker methods and FileList's corresponding normalized behavior. Acceptance MT-01, MT-12, MT-16.

- [ ] **Write adapter HTTP regressions using `httptest.NewServer`.** Serve both string-valued search fields and number-valued detail fields. Return the synthetic `{id:"0",name:"No results returned",info_hash:"0000000000000000000000000000000000000000"}` record and assert it produces zero Releases. A real row with malformed size/hash is an adapter error, not silently a fabricated zero-size release. Verify that changing the API callback sends the next request to the new test server and that no Authorization header is sent to Pirate Bay.

Use these real-shape payloads:

```json
[{"id":"42","name":"Example.2024.1080p.WEB-DL","info_hash":"0123456789abcdef0123456789abcdef01234567","seeders":"9","leechers":"2","size":"4096","num_files":"1","category":"201","added":"1652877231","imdb":""}]
```

```json
{"id":42,"name":"Example.2024.1080p.WEB-DL","info_hash":"0123456789abcdef0123456789abcdef01234567","seeders":9,"leechers":2,"size":4096,"num_files":1,"category":201,"added":1652877231,"imdb":null}
```

- [ ] **Run red:** `go test ./internal/adapters/piratebay -count=1` after the new package test exists.

- [ ] **Implement the client using existing outbound patterns.** Constructor: `New(settings func() (websiteURL, apiURL string)) *Client`. Use bounded bodies and context-aware HTTP; use URL query encoding, not string concatenation of search terms. Own Pirate Bay wire structs inside the adapter. Normalize numbers with an adapter-local JSON scalar decoder accepting JSON numbers and decimal strings; reject fractional/negative values for byte/count fields. Normalize optional IMDb values and Unix timestamps.

Endpoints: `/q.php?q=...`, `/t.php?id=...`, `/precompiled/data_top100_recent.json`, and category top windows `/precompiled/data_top100_<category>.json`. `Category` receives a provider-local string ID and only accepts IDs in `Categories()`. Do not fabricate upstream pagination. Map 100-family to audio, 200-family to video, 300-family to software, 400-family to games, 500-family to adult, and 600-family to other, retaining individual IDs/display names. The 500 family is adult content, not ordinary video. Pirate Bay discovery permits audio/video and excludes software/games/adult/other, matching the existing media-focused catalog intent; never infer movie/series kind from these classes.

- [ ] **Build magnet references without DHT changes.** Validate a nonzero 40-hex-character v1 info hash and construct the URI with `net/url`. Use the explicit public announce endpoints below, read from Pirate Bay's frontend script during planning; do not fetch random tracker lists at runtime. `Acquire` fetches details when needed and returns `TorrentAcquisition{Magnet: uri}`. It never downloads payload files.

Core URI construction after validation:

```go
q := url.Values{}
q.Set("xt", "urn:btih:"+strings.ToLower(hash))
q.Set("dn", name)
for _, announce := range publicAnnounceURLs { q.Add("tr", announce) }
uri := "magnet:?" + q.Encode()
```

Define the following adapter-local array, using active endpoints observed in `https://thepiratebay.org/static/main.js`. Endpoint presence is observed; announce-service uptime was not verified and is not promised. If announced peers are unreachable, return the normal bounded metadata-acquisition failure downstream.

```go
var publicAnnounceURLs = [...]string{
    "udp://tracker.opentrackr.org:1337",
    "udp://open.stealth.si:80/announce",
    "udp://tracker.torrent.eu.org:451/announce",
    "udp://tracker.bittor.pw:1337/announce",
    "udp://public.popcorn-tracker.org:6969/announce",
    "udp://tracker.dler.org:6969/announce",
    "udp://exodus.desync.com:6969",
    "udp://open.demonii.com:1337/announce",
    "udp://glotorrents.pw:6969/announce",
    "udp://tracker.coppersurfer.tk:6969",
    "udp://torrent.gresille.org:80/announce",
    "udp://p4p.arenabg.com:1337",
    "udp://tracker.internetwarriors.net:1337",
}
```

- [ ] **Normalize FileList without changing its remote protocol.** Populate tracker/provider/category fields from its current response mapping. Keep its request spacing, hourly budget, authentication, bounded bodies, and removed-torrent classification. `Acquire(ctx,providerID)` returns metainfo bytes from the existing validated download path. At Task 6, remove `OpenTorrent` from the public tracker contract and move its HTTP body into `Acquire`; do not retain an exported compatibility wrapper after cutover.

- [ ] **Run green and a live metadata-only probe:** `go test ./internal/adapters/filelist ./internal/adapters/piratebay -count=1`; request the public Ubuntu metadata endpoint and report HTTP status/response shape, not a universal performance claim. Do not use saved FileList credentials for an unrequested bulk scan.

- [ ] **Commit:** `feat: add direct Pirate Bay catalog adapter`.

## Task 4: Native metadata-only magnet resolution

**Files:** `internal/adapters/nativetorrent/client.go`, new `magnet.go`, `magnet_test.go`; existing session tests.

**Consumes:** The existing real native client and pinned v1.61.0 APIs.

**Produces:** `ResolveMagnet(ctx,uri,downloadRoot) ([]byte,error)` without relaxing private discovery or persisting resolver placeholders. Acceptance MT-09, MT-10, MT-11.

Verified APIs: `torrent.TorrentSpecFromMagnetUri`; `Client.AddTorrentSpec(spec) (*Torrent,bool,error)`; `Torrent.GotInfo()`; `Torrent.Metainfo()`; `bencode.Marshal`; `Torrent.Drop()`. The library can request metadata while piece priorities remain None. It does not provide a per-torrent DHT privacy boundary adequate to justify enabling DHT on this application's shared client.

- [ ] **Add a deterministic metadata transfer regression.** Reuse `buildTestMetainfo` and `newTestClientAt`. Start a loopback metadata-bearing peer with synthetic metainfo; add its address via the magnet's `x.pe` parameter. Resolve without calling `DownloadAll` or `PrepareFiles`. Assert exported info hash/file manifest match and no payload pieces were selected. Repeat with cancellation and assert no resolver-owned torrent survives. Separately add the torrent normally first and assert resolving the same hash preserves its session and selection.

Also keep this bounded-input regression; a v2-only magnet must not reach the library's zero-v1-hash panic:

```go
func TestResolveMagnetRejectsMissingV1Hash(t *testing.T) {
    c := newTestClient(t)
    _, err := c.ResolveMagnet(t.Context(), "magnet:?dn=missing-hash", t.TempDir())
    if err == nil { t.Fatal("accepted a magnet without a v1 info hash") }
}
```

Match the existing helper's return/cleanup convention when inserting the test rather than introducing a second client factory.

- [ ] **Run red:** `go test ./internal/adapters/nativetorrent -run 'TestResolveMagnet' -count=1`.

- [ ] **Implement one per-info-hash acquisition gate shared by `Add` and `ResolveMagnet`.** Resolve must hold that gate through wait/export/drop. Do not hold the global client mutex during network waiting. Merely acquiring `c.mu` around AddTorrentSpec is insufficient: normal Add could otherwise reuse a transient resolver torrent and skip session persistence before the resolver drops it.

- [ ] **Implement resolve ownership and wait.** Validate the magnet before the pinned library call; reject a zero v1 hash. Look up an existing torrent before merging any magnet spec. If one already exists with metadata, export it without changing trackers, selection, pause state, or persistence. Otherwise add the spec, require ownership through `new=true`, wait on `GotInfo` versus `ctx.Done`, marshal metainfo, verify its info hash, and drop only the owned torrent before releasing the per-hash gate. Do not use the existing five-second `waitInfo` helper for peer metadata resolution.

Critical wait/export shape inside the owned section:

```go
select {
case <-t.GotInfo():
case <-ctx.Done():
    return nil, ctx.Err()
}
mi := t.Metainfo()
if mi.HashInfoBytes() != spec.InfoHash {
    return nil, errors.New("resolved metadata does not match magnet info hash")
}
raw, err := bencode.Marshal(mi)
if err != nil { return nil, err }
if len(raw) == 0 || len(raw) >= 16<<20 {
    return nil, errors.New("resolved metadata is empty or too large")
}
return raw, nil
```

Install `defer t.Drop()` only after confirmed ownership. The caller supplies the bounded context. No `session.putMeta` occurs in Resolve; the application persists returned bytes and normal Add remains the owner of session persistence. Native storage stays under its configured DataDir; do not reinterpret Add's historically ignored save-path argument.

- [ ] **Run green and session regression:** `go test ./internal/adapters/nativetorrent -count=1`. Verify private `.torrent` fixtures and existing restart/piece-selection tests remain green. Check real loopback metadata transfer, not only malformed inputs or mock forwarding.

- [ ] **Commit:** `feat: resolve native magnet metadata without payload selection`.

## Task 5: qBittorrent metadata resolution and version boundary

**Files:** `internal/adapters/qbittorrent/client.go`, new `magnet.go`, `magnet_test.go`; existing HTTP adapter tests.

**Consumes:** Existing auth/outbound helpers; unchanged Add/Files/Status/Remove methods.

**Produces:** `ResolveMagnet(ctx,uri,downloadRoot) ([]byte,error)` for qB 4.5+, explicit unsupported error otherwise. Acceptance MT-09, MT-10, MT-11 and the approved compatibility boundary.

Verified endpoints: `GET /api/v2/app/version`; `POST /api/v2/torrents/add` with `urls` and `stopCondition=MetadataReceived`; `GET /api/v2/torrents/export?hash=...`. Stop conditions and export arrive in qB 4.5.0. Do not infer support from the daemon's peer identity string or assume 4.3.x export exists.

- [ ] **Add a protocol behavior test with a stateful httptest server.** Make version 4.3.9 reject magnet resolution before any add request; make 4.5.0 resolve/export valid metainfo; make 5.x use its supported stop/start naming. Exercise a missing-metadata 409 followed by success, deadline cancellation, malformed export, and mismatched info hash. For an already-existing torrent, assert the resolver returns metadata without changing pause/selection/category/trackers or deleting it. These defend user-visible ownership, not just form-field forwarding.

Version boundary table:

```go
cases := []struct{ version string; supported bool }{
    {"v4.3.9", false},
    {"v4.4.5", false},
    {"v4.5.0", true},
    {"v4.6.5", true},
    {"v5.0.0", true},
}
```

Parse numeric version components, not lexicographic strings. An unreadable/unsupported version yields the explicit capability error for new magnets; it does not break existing Add or local playback.

- [ ] **Run red:** `go test ./internal/adapters/qbittorrent -run 'TestResolveMagnet|TestMagnetVersion' -count=1`.

- [ ] **Implement a per-hash gate shared with Add.** Preflight the exact info hash. If it exists, export its metadata without mutation; if metadata is pending, wait boundedly without assuming ownership. Never delete a torrent based solely on absence from managed Downloads.

- [ ] **Create resolver-owned torrents with an identifiable temporary location.** Use a unique contained directory below `<DownloadRoot>/.metadata/` and a unique resolver tag. Add only after confirming the hash was absent. Send `urls`, `savepath`, `category=torrent-tv`, and `stopCondition=MetadataReceived`. Do not initially pause the magnet, because that prevents metadata exchange. Treat an ambiguous duplicate or interrupted add response conservatively: inspect the exact torrent and ownership marker; never adopt/delete unrelated content.

Form core:

```go
form := url.Values{
    "urls": {magnet},
    "savepath": {resolverPath},
    "category": {"torrent-tv"},
    "tags": {resolverTag},
    "stopCondition": {"MetadataReceived"},
}
```

`resolverPath` is the freshly created contained temporary directory; `resolverTag` is unique to this resolve attempt. Both are local variables owned by this method, not caller-supplied arbitrary deletion paths.

- [ ] **Wait, stop, export, verify, and clean up.** Poll metadata/files with a context-aware ticker; treat metadata-not-ready 409 as transient only in this bounded stage. Immediately pause/stop the owned torrent once files exist, then export below the 16 MiB cap and verify the info hash. qB has reported fast-metadata stop-condition races: do not rely on the add flag alone as proof of zero payload activity. Cleanup the owned torrent and its unique temporary directory, including any race-downloaded bytes, on success/failure/cancellation. Use a separate short cleanup context after request cancellation. Wait for owned removal before releasing the per-hash gate so subsequent Add cannot reuse a half-deleted placeholder. Cleanup failure is returned/reported, not swallowed as successful resolution.

- [ ] **Preserve old daemon behavior outside magnets.** Do not change the older Add response acceptance rules. Pause/Resume commands used by the new flow must use the actual daemon API naming; add version-sensitive handling only where required, not a silent 5.x failure fallback. Keep existing FileList and completed-local-file smoke checks on an older daemon.

- [ ] **Run green and actual daemon smoke.** `go test ./internal/adapters/qbittorrent -count=1`; run a controlled local magnet against available qB 4.5+ and verify metadata export, file selection, and no residual resolver torrent. Record daemon version. The httptest suite proves old-version gating even when an old daemon is unavailable; do not call that a real old-daemon smoke.

- [ ] **Commit:** `feat: resolve qBittorrent magnets with explicit capability gating`.

## Task 6: Registry cutover, concurrent jobs, and managed-first preparation

**Files:** `internal/application/ports.go`, `service.go`, `catalog.go`, `torrentmeta.go`, new `trackers.go`; `internal/composition/container.go`; adapter interface cutover and affected tests.

**Consumes:** Tasks 1–5.

**Produces:** Full provider orchestration and acquisition through the existing application workflows. Acceptance MT-02–06, MT-09–14.

- [ ] **Write the load-bearing disabled-tracker playback regression first.** Extend the existing household/service fixtures with a registry whose FileList registration is disabled and whose adapter fails the test if any provider method is called. Seed a real completed temporary media file, persisted Download, Release, and manifest. Call `Prepare` and the completed-file stream path; close/reopen repository and service, then repeat. Assert playable bytes and preserved resume position. Repeat the managed season-pack sibling case. A disappeared file must not initiate a new provider acquisition while disabled.

Preserve the guard order:

```go
// Existing managed content is resolved before RequireEligible.
if existing, err := s.repo.FindDownload(ctx, releaseID, fileIndex); err == nil {
    return s.prepareManagedDownload(ctx, existing)
} else if !errors.Is(err, sql.ErrNoRows) {
    return domain.Download{}, err
}
if existing, err := s.prepareExistingTorrentFile(ctx, release, fileIndex); err == nil {
    return existing, nil
} else if !errors.Is(err, sql.ErrNoRows) {
    return domain.Download{}, err
}
tracker, err := s.trackers.RequireEligible(release.TrackerID)
if err != nil { return domain.Download{}, err }
```

`tracker` is then used only for a new acquisition through `Acquire(ctx,release.ProviderID)`. Apply the same ordering to PrepareSeason, next-episode materialization, and fallback/retry paths; a missing engine torrent is not permission to bypass the disabled-provider guard.

- [ ] **Write concurrent-provider and eligibility-race tests.** Two fake trackers use channels: one returns immediately, one waits. Assert the first provider's rows are queryable before releasing the second. Make one error and assert the other's results persist. Disable the blocked provider before releasing it and assert it contributes no discovery rows or new preparation. Use channels, not arbitrary sleeps.

- [ ] **Run red:** `go test ./internal/application -run 'TestDisabledTracker|TestConcurrentTracker|TestTrackerEligibility' -count=1`.

- [ ] **Install the ledger's ports and registry.** Remove `TrackerCatalog`; migrate every embedded fake and concrete caller. Register FileList and Pirate Bay in composition with live enabled/configured callbacks. Replace every `s.catalog` access, including latest/rebuild, Search, title refresh, dependency test, torrentManifest, Prepare, and PrepareSeason. Do not default an unknown tracker to FileList.

- [ ] **Replace the global single-tracker semaphore with per-provider serialization.** Retain the existing overall job bound, but do not let FileList's throttle block Pirate Bay. A coordinator must not hold an overall job slot while waiting for children that need the same slot, especially when MaxConcurrentJobs is 1. Persist provider-specific child job IDs and `TrackerID`; include it in sync and refresh dedupe keys. Keep TMDB jobs provider-neutral. Preserve old persisted nonterminal tracker work by interpreting it once as FileList during migration/recovery, not by retaining old request dispatch paths.

Keys must separate providers:

```go
syncKey := tracker.ID() + ":" + mode
searchKey := "tracker-search:" + tracker.ID() + ":" + queryHash
refreshKey := "catalog-title-refresh:" + tracker.ID() + ":" + titleID
```

`mode` is `latest` or `rebuild`; `queryHash` uses the existing normalized-query SHA-256/base64 scheme. Parent jobs aggregate completion and named child failures; publish `catalog.search.completed`/`catalog.updated` after each successful provider merge so existing SSE clients can refresh incrementally. Failed providers do not roll back others. Check live eligibility immediately before each outbound operation and again before exposing results; cancel owned provider contexts on settings notification when possible.

- [ ] **Filter discovery and preserve managed entry points.** Pass `EligibleIDs()` into all discovery queries before grouping/pagination. Derive `CatalogTitle.Trackers` from eligible sources, uniquely and in stable ID order. Use the adapter-normalized DiscoveryExcluded flag instead of applying FileList numeric categories globally. Keep Household resume/favorite/category paths based on persisted managed/referenced releases rather than the filtered discovery universe. If a disabled-only downloaded title opens detail from history, expose its managed playable sources without exposing new acquisition choices.

- [ ] **Unify metainfo acquisition.** Extract the existing bencode-to-manifest body into `parseTorrentManifest(releaseID string, data []byte) (domain.TorrentManifest,error)`, preserving path validation. Add `Service.releaseMetainfo(ctx,release) ([]byte,error)`: return cached manifest bytes first; otherwise require eligibility, acquire, validate exactly one acquisition form, and resolve a magnet with a 90-second context if needed. Parse and persist bytes/files together. Share the result between allocation calculation and Add so one preparation does not refetch metadata twice. Old cached manifests without bytes remain sufficient for managed playback; only a new acquisition fills missing metainfo.

Acquisition dispatch:

```go
acquisition, err := tracker.Acquire(ctx, release.ProviderID)
if err != nil { return nil, err }
if err := acquisition.Validate(); err != nil { return nil, err }
if acquisition.Magnet == "" { return acquisition.Metainfo, nil }
resolveCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
defer cancel()
return s.engine.ResolveMagnet(resolveCtx, acquisition.Magnet, s.settings.Get().DownloadRoot)
```

Use the existing per-release preparation locks and adapter per-hash gate to avoid duplicate resolution. Do not hold a global registry lock while waiting for peers. Metadata cancellation/timeouts do not call `removeDeadRelease`; only the provider's genuine removed-release classification may do that.

- [ ] **Expose explicit metadata waiting without blocking navigation.** Ordinary detail reads remain cache-only. Existing title-refresh jobs can resolve unseen season-pack manifests in the background and log metadata-wait state. Direct Prepare/PrepareSeason requests may await bounded acquisition; clients show preparing/metadata state and can abort those requests. A client abort must reach the request context and resolver cleanup. Do not start network resolution while merely rendering every title card.

- [ ] **Populate persisted download provenance before SaveDownload.** Read from the stored Release, not from a live descriptor lookup. Keep tracker labels available after restart and disabled-provider filtering. Preserve engine-route deduplication without losing the requested release identity or mixing announce credentials.

- [ ] **Run green:** `go test ./internal/application ./internal/composition ./internal/adapters/httpapi -count=1`. Include MaxConcurrentJobs=1, both providers, zero providers, failed provider, disable-during-request, managed movie restart, managed episode restart, and disabled missing-content cases.

- [ ] **Commit:** `feat: aggregate enabled trackers and preserve managed playback`.

## Task 7: HTTP, OpenAPI, shared DTOs, and acquisition cancellation

**Files:** HTTP API/schema/tests, `api/openapi.yaml`, `clients/shared/src/index.ts`, existing shared tests, GUI settings bindings where notification is required.

**Consumes:** Registry and application behavior from Task 6.

**Produces:** One client contract for provenance, tracker status, provider failure, and cancellable preparation. Acceptance MT-03, MT-05, MT-07, MT-12, MT-13, MT-15.

- [ ] **Add boundary regressions.** Verify a Downloads response includes persisted FileList/Pirate Bay labels even when that provider is disabled. Verify disabled new preparation returns HTTP 409 with an actionable problem, unconfigured provider returns HTTP 409 with configuration guidance, unsupported magnet version returns HTTP 409 with the upgrade/native guidance, and metadata deadline returns HTTP 504 rather than a removed-release response. A cancelled request must not turn into a persisted failed/dead release.

- [ ] **Run red:** `go test ./internal/adapters/httpapi -run 'TestTracker|TestDisabled|TestMagnet|TestDownload' -count=1` and `npm test -w @torrent-tv/shared`.

- [ ] **Add the tracker endpoint and generic tracker connection testing.** `GET /trackers` is read-only. Route registered IDs in `/dependencies/{name}/test` through `TestTracker`, preserving the existing non-tracker dependency cases and response shape. A connection test uses a bounded lightweight request and never upserts releases. Connect HTTP and GUI successful settings saves to provider-work cancellation/refresh through the same application notification method; clients also refetch discovery on settings changes. Do not expose credentials in statuses, errors, or events.

- [ ] **Update every wire owner together.** Add required tracker fields to Release and Download schemas/types, `trackers` to CatalogTitle and SearchResult, and `trackerId` to provider Job data. Update `downloadDTO`, embedded household/detail releases, prepare responses, OpenAPI, and shared TypeScript in the same commit. No optional FileList fallback label is used to hide missing server provenance.

Shared types:

```ts
export type TrackerRef = { id: string; name: string };
export type TrackerStatus = TrackerRef & {
  enabled: boolean;
  configured: boolean;
  capabilities: { imdbSearch: boolean; seasonFilter: boolean; episodeFilter: boolean; categories: boolean };
};
```

`Release` and `Download` carry required `trackerId: string; trackerName: string`; `Release` also carries `providerId`, `categoryId`, and `browseClass`. `CatalogTitle.trackers` is `TrackerRef[]`. Server responses always supply the fields, including migrated data.

- [ ] **Update reconciliation and cancellation.** Add `trackerId` and `trackerName` to `downloadRenderFingerprint`; prove a provenance-only update replaces the stale row object while an unchanged response still reuses it. Add these API methods/signatures:

```ts
trackers() { return this.call<TrackerStatus[]>('/trackers') }
prepare(id: string, fileIndex = -1, signal?: AbortSignal) {
  return this.call<Download>(`/releases/${encodeURIComponent(id)}/prepare`, {
    method: 'POST', body: JSON.stringify({ fileIndex }), signal,
  });
}
prepareSeason(id: string, season: number, signal?: AbortSignal) {
  return this.call<Page<Download>>(`/releases/${encodeURIComponent(id)}/prepare-season`, {
    method: 'POST', body: JSON.stringify({ season }), signal,
  });
}
```

Keep the shared API's existing fetch/error handling; do not introduce another HTTP client. Verify abort behavior in the actual browser during Tasks 8–9, especially the older TV support floor.

- [ ] **Run green:** `go test ./internal/adapters/httpapi ./internal/gui -count=1`; `npm test -w @torrent-tv/shared`; `npm test -w @torrent-tv/desktop`. Existing schema parity remains intact. Remove any in-scope tests that pin obsolete FileList-only wording instead of re-pinning them to new wording.

- [ ] **Commit:** `feat: expose tracker provenance and preparation state to clients`.

## Task 8: Web tracker settings and provenance

**Files:** `web/src.tsx`, `web/downloads.tsx`, `web/settings.tsx`, existing web styles only where needed.

**Consumes:** Task 7's required shared DTO fields and API methods.

**Produces:** Complete web presentation and interaction coverage. Acceptance MT-07, MT-13, MT-15.

- [ ] **Start the existing web/server runtime and record the pre-change surfaces.** Use an isolated data directory and synthetic releases/downloads, not household settings. Open a managed browser tab and inspect search, detail choices, Downloads, resume/history, player information, and tracker settings. This is the baseline failing visual evidence; do not add permanent UI tests solely for new labels.

- [ ] **Add tracker settings using current components.** Extend `TAB_GROUPS`, `CONNECTIONS`, and tracker-tab field membership. Render separate FileList and Pirate Bay checkbox toggles; retain FileList credentials; add website and advanced API URL inputs. Explain that disabling hides discovery without removing downloads. Permit zero trackers and show the server readiness state. Test saved false values and switching tabs without losing dirty edits.

- [ ] **Add provenance at each action surface.** In Downloads, add a semantic Tracker field to the existing row that renders all states:

```tsx
<dt>Tracker</dt><dd>{download.trackerName}</dd>
```

In release choices/episode/season-pack rows, show `source.release.trackerName` beside the release facts and actions. For grouped titles:

```tsx
<span className="tracker-names">
  {title.trackers.map(tracker => tracker.name).join(' · ')}
</span>
```

Use existing text/badge styling. Keep labels distinct from quality attributes and seeder statistics. Cover `SourceRows`, `SourcePicker`, `SeasonPackCard`, `LegacyCard`, `LibraryCategories`, the current player information surface, and download removal confirmation. Remove FileList-only discovery placeholders/empty guidance; do not rebrand unrelated application identity.

- [ ] **Implement preparation states and cancellation.** Before calling abortable Prepare, show the originating tracker and a preparing/metadata message. Keep a Cancel action, cancel on unmount/back, and do not switch to playback until a Download is returned. Distinguish cancellation from acquisition failure. Provider failure does not replace successful search results with a global error. Zero-enabled guidance retains navigation to Downloads.

- [ ] **Verify the actual surface.** Use browser observation and screenshots at normal and narrow web widths. Toggle Pirate Bay, save/reload, inspect mixed-provider search and release choices. Seed all download states and verify labels. Disable the provider of a completed download, play it, restart the server with the same data, and play it again while provider requests are blocked. Verify empty, partial-failure, and metadata-cancel states. Record requests and playable bytes, not just an unchanged Play button.

- [ ] **Run existing web suite/build:** `npm test -w @torrent-tv/web`; `npm run build:web`. New permanent test only if a real uncertain transition cannot be exercised reliably by the runtime smoke.

- [ ] **Commit:** `feat: show tracker provenance throughout the web app`.

## Task 9: Shared TV tracker settings and provenance

**Files:** `clients/tv/src/main.tsx`, existing TV styles/navigation tests as needed; platform shells only if runtime verification exposes a required compatibility change.

**Consumes:** Task 7's shared contract. Tizen/Android TV/webOS continue to share the TV bundle.

**Produces:** Complete TV presentation and remote interaction coverage. Acceptance MT-07, MT-13, MT-15.

- [ ] **Launch the shared TV runtime and capture baseline missing labels.** Use the same synthetic backend states as Task 8. Confirm existing focus navigation before adding controls.

- [ ] **Add tracker settings and tests.** TVSettings is explicitly rendered, not automatically generated from the settings schema. Add independent focusable toggles, readiness text, and Pirate Bay connection testing. Preserve the schema's environment-managed read-only restrictions. Renumber manual `data-focus-row` values consistently for save/test/change-server/forget/update controls; verify navigation behavior rather than asserting literal row numbers.

- [ ] **Add labels everywhere TV media can be selected or played.** Update `TVDownloads`, `SourceButton`, `TVSeasonPackCard`, `HouseholdRail`, `TVLibraryCategories`, grouped title cards, player information, and removal confirmation. Replace hardcoded `FileList release` in Downloads with the actual tracker name.

Download row fragment:

```tsx
<small>Tracker: {download.trackerName}</small>
```

Do not remove completed-media Play when the tracker is disabled. History/detail navigation for managed media must use the server's managed-source exception rather than discovery eligibility to decide whether playback is offered.

- [ ] **Add cancelable preparation with remote-safe focus.** Back cancels pending acquisition and returns focus to the originating choice. A metadata error leaves a visible retry/back path. Avoid APIs unsupported on the platform floors; reuse existing platform/polyfill conventions for AbortController if the oldest webOS runtime needs them. Do not report an aborted request as a fatal Error panel.

- [ ] **Exercise the TV app with keyboard/remote navigation.** Verify focus enters/leaves new toggles, save/test controls remain reachable, labels fit at TV viewing size, partial failures do not steal focus, and all download states show provenance. Disable a completed download's tracker and play before/after server restart. Check actual player state through the active platform seam, not only navigation to a player screen.

- [ ] **Run existing TV/shared/webOS tests and package builds.** `npm test -w @torrent-tv/tv`; `npm test -w @torrent-tv/shared`; `npm test -w @torrent-tv/webos`; `npm run build:tv`; `make tizen-wgt validate-tizen-wgt`; `make torrenttv-apk`; `npm run build:webos`; `make webos-ipk validate-webos-ipk`. Run `make smoke-tizen-engine` and `make smoke-webos-engine` for the available pinned old-engine harnesses. Preserve declared support floors. Record physical device, emulator, browser harness, and package-build evidence separately.

- [ ] **Commit:** `feat: show tracker provenance across TV clients`.

## Task 10: End-to-end verification and delivery gate

**Files:** Existing behavioral test suites and documentation owners in the spec; no new framework or permanent smoke harness solely to prove the feature.

**Consumes:** Fully integrated Tasks 1–9.

**Produces:** Verified end-to-end behavior and an honest runtime/device evidence report for MT-01 through MT-16.

- [ ] **Run the actual application with controlled content and provider fixtures.** Use a local metainfo-bearing peer for deterministic acquisition and a short synthetic video for playback. Exercise both native and available qB 4.5+ engines separately, preserving one-active-engine semantics. Confirm existing FileList-style `.torrent` playback and Pirate Bay-style magnet preparation select only requested files/episodes.

- [ ] **Exercise disable/restart at the byte-serving boundary.** Download a movie and a season-pack episode, record their download IDs and resume positions, disable their tracker, stop/restart the server on the same database/content, request the existing stream and observe successful bytes/playback. Make all provider calls fail during this scenario. Then delete only the test content through the normal managed operation and confirm disabled re-download is rejected while history/provenance remains.

- [ ] **Exercise failure and concurrency.** Make FileList fail while Pirate Bay succeeds, reverse the failure, block one provider, disable it mid-request, and verify eligible rows/counts/facets remain correct. Repeat with MaxConcurrentJobs=1 to detect coordinator deadlocks. Cancel and time out metadata acquisition; inspect the real engine and temporary directory for residual resolver content.

- [ ] **Exercise migration from the populated pre-change fixture and URL changes.** Verify existing IDs, favorites, subtitle/preference references, and resume positions. Upsert the same FileList provider ID after migration and assert the API returns the original internal ID. Change website/API settings and confirm no duplicate releases or lost managed files.

- [ ] **Run full existing checks once centrally:**

```sh
go test -race ./...
go vet ./...
python3 -m unittest discover -s tools/tests
npm run test:clients
npm test -w @torrent-tv/desktop
npm test -w @torrent-tv/webos
```

Run the existing web/TV/platform build/package targets as part of the runtime checks; report exact commands and outputs. Do not claim a platform is verified solely because a shared bundle compiled.

- [ ] **Review against the matrix below.** A missing scenario is not an optional follow-up. Fix reachable failures before handoff; report unavailable hardware explicitly. After smoke proof, update `CONTEXT.md`, `docs/ARCHITECTURE.md`, `docs/API.md`, applicable existing settings/client guides, and changelog with the implemented contract. Document qB 4.5+ only for new magnets, native tracker-based discovery without DHT, both Pirate Bay URLs, disabled-tracker semantics, and bounded catalog windows. Remove throwaway fixture-generation/runtime scripts after their evidence is captured; retain only the regression fixture/tests that defend migration and acquisition behavior.

- [ ] **Commit verified integrated changes and documentation:** `docs: document multi-tracker configuration and playback guarantees`. Present implementation commits, exact checks, exercised devices/runtimes, and remaining hardware-only verification limitations. Do not report this plan itself as feature implementation.

## Acceptance coverage and plan self-review

| Spec scenario | Implementing tasks | Proof |
| --- | --- | --- |
| MT-01 provider-local ID collision | 1, 3, 6 | normalized upsert and correct-adapter preparation |
| MT-02 incremental mixed-provider results | 6, 7, 8, 9 | channel-controlled fast/slow providers and UI refresh |
| MT-03 partial provider failure | 6–9 | named failure with successful results retained |
| MT-04 disabled discovery scope | 1, 6 | query ordering/count/facet/source assertions |
| MT-05 queued/late disabled work | 6, 7 | disable during a blocked provider request |
| MT-06 disabled managed playback after restart | 6, 8–10 | actual movie/episode stream bytes without provider access |
| MT-07 Downloads labels in every state | 1, 7–9 | required DTOs, fingerprint change, browser/TV surfaces |
| MT-08 existing FileList migration | 1, 10 | populated old fixture, reopen, unchanged references |
| MT-09 both acquisition forms/engines | 3–6, 10 | local peer and real engine movie/pack preparation |
| MT-10 metadata timeout/cancel cleanup | 4–9 | no residual owned engine session/content |
| MT-11 private/public isolation | 3–5, 10 | unchanged native NoDHT and non-mutating same-hash reuse |
| MT-12 URL configuration and identity | 1–3, 7, 10 | callback endpoint switch, canonical ID persistence |
| MT-13 Pirate Bay-only/zero providers | 1, 2, 6–9 | onboarding, empty discovery, existing Downloads playback |
| MT-14 re-enable without duplication | 1, 6, 10 | restored visibility and unique provider identity |
| MT-15 all download/play surfaces | 7–9 | web and shared TV coverage tables/smoke |
| MT-16 upstream normalization/windows | 3, 6 | synthetic sentinel/numeric fields; honest bounded results |
| Approved qB compatibility boundary | 5, 7–10 | old-version gate, qB 4.5+ resolution, existing paths unchanged |

Self-review requirements: no missing spec scenario; one consistent set of settings/DTO names; no ID parser/fallback alias; no provider filter on managed playback; no native DHT relaxation; no older-qB silent download fallback; no fake metadata-ready state; no claims of unavailable hardware verification.
