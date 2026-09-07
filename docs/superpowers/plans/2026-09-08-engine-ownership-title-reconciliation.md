# Engine Ownership and Canonical Title Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the built-in Native engine download and progressively stream new Pirate Bay and FileList acquisitions while existing qBittorrent downloads stay fully manageable, and consolidate duplicate catalog titles without losing any release or household state.

**Architecture:** Owner-based engine routing: one startup-selected acquisition default plus a persisted `native:`/`qb:` owner per download row, resolved through a new `EngineSet`. Canonical titles become a SQLite-projected identity (IMDb-first, conservative exact-name matching) consumed by every catalog path. Client surfaces report running-vs-saved engine to derive the restart notice.

**Tech Stack:** Go (stdlib `net/http`, `database/sql` SQLite, anacrolix/torrent), Preact web + shared TV clients (vitest), OpenAPI YAML.

**Spec:** `docs/superpowers/specs/2026-09-08-engine-ownership-title-reconciliation-design.md` (approved). This plan argues from the spec; executors read both.

## Global Constraints

- No media migration, no route rewriting, no automatic engine fallback, no qBittorrent upgrade.
- qBittorrent 4.3.9 stays supported for existing media and `.torrent` acquisition; the >=4.5.0 gate stays scoped to new magnets through the qB adapter only.
- Keep published `v0.7.0` untouched; no deployment, Pi restart, push, or release actions.
- Selection is restart-required; JSON surfaces use `native`/`qbittorrent` spellings, `native:`/`qb:` stay route-internal.
- Unreachable owner footprint is never counted as zero; uncertainty blocks new admission only.
- Keep existing goroutine-per-tracker search and its bounds; no new worker pools.
- Tests are behavioral; no wording-pinning, no plumbing assertions, no mock-only contracts.
- Runtime proof uses controlled legal content over loopback; local results never prove the Pi fixed.
- Working tree must stay clean of unrelated edits; commit per task.

---

### Task 1: EngineSet routing core

**Files:**
- Create: `internal/application/engine.go`, `internal/application/engine_test.go`
- Modify: `internal/domain/models.go` (add `ErrEngineUnavailable` next to `ErrTorrentNotFound`)

**Interfaces:**
- Produces: `NewEngineSet(defaultPrefix string, engines map[string]TorrentEngine) (*EngineSet, error)`; `(*EngineSet).Resolve(route string) (TorrentEngine, string, bool)`; `.Default()`, `.DefaultPrefix()`, `.MarkUnavailable(prefix string, err error)`, `.InitError(prefix string) (error, bool)`, `.Each(fn func(prefix string, engine TorrentEngine))`, `.Prefixes() []string` (fixed order `native:`, `qb:`); `domain.ErrEngineUnavailable`.

- [ ] **Step 1: Write failing tests** in `engine_test.go`: Resolve returns hash+engine for `native:h1` and `qb:h2`; unknown prefix `foo:h` -> `ok=false` and default engine receives zero calls; `MarkUnavailable`+`InitError` round-trip; `NewEngineSet` rejects empty map, default missing from map, and prefix without trailing `:`.

- [ ] **Step 2: Run** `go test ./internal/application -run EngineSet -count=1` — expect FAIL (undefined).

- [ ] **Step 3: Implement** (contract, not suggestion):

```go
func NewEngineSet(defaultPrefix string, engines map[string]TorrentEngine) (*EngineSet, error) {
	if len(engines) == 0 {
		return nil, errors.New("engines map cannot be empty")
	}
	if !strings.HasSuffix(defaultPrefix, ":") {
		return nil, fmt.Errorf("default prefix %q must end with ':'", defaultPrefix)
	}
	if _, ok := engines[defaultPrefix]; !ok {
		return nil, fmt.Errorf("default engine %q not registered in engines map", defaultPrefix)
	}
	return &EngineSet{defaultPrefix: defaultPrefix, engines: engines, initErrs: map[string]error{}}, nil
}
```

`Resolve` splits the route with `strings.Cut(route, ":")`; `ok=false` means unknown prefix or unconstructed engine. Callers report unavailable; never fall back to the default. `MarkUnavailable` records the original construction error for a required-but-failed engine (never a no-op engine).

- [ ] **Step 4: Run** Step 2 command — expect PASS. **Step 5: Commit** `feat: add engine set with owner-prefix routing`.

### Task 2: Service cutover to owned routing

**Files:**
- Modify: `internal/application/service.go`, `catalog.go`, `streaming.go`, `subtitles.go`, `retention.go`; `internal/application/service_test.go` (add `singleEngineSet(t, prefix, engine)` helper; nil engine -> `&dummyEngine{}`)
- Modify: `internal/application/service.go:76` — `NewService(trackers *TrackerRegistry, engines *EngineSet, r Repository, s *config.Store, subtitles ...SubtitleProvider)`

**Interfaces:**
- Consumes: Task 1 `EngineSet`.
- Produces: `func (s *Service) owner(route string) (TorrentEngine, string, bool)` replacing `route()`; `func (s *Service) EngineDefault() string` (`native:`->`"native"`, `qb:`->`"qbittorrent"` via new constants `EngineNative`/`EngineQbittorrent` in `ports.go`); `func (s *Service) TestEngine(ctx context.Context) (string, error)` (unchanged signature, probes `Default()`).

- [ ] **Step 1: Failing test** (existing fakes): `Downloads` with rows on two engines routes each row to its own fake (per-engine `Status` observed); a row whose engine is absent reports `State: "unavailable"` with an `ErrEngineUnavailable`-wrapped error naming the prefix, other rows unaffected, tracker labels preserved.

- [ ] **Step 2: Run** `go test ./internal/application -run Downloads -count=1` — FAIL.

- [ ] **Step 3: Apply the complete cutover in one pass.** Delete `route()` (2562-2567), `enginePrefix()` (2555-2560), `SetEngineRoutePrefix()` (2548-2553), fields `engine`/`engineRoutePrefix` (27-28). Migrate all 13 `route()` callers to `owner()` + per-row engine: `catalog.go:515-519`; `retention.go:179,193`; `service.go:1519,1666,1839,1908,2024,2081,2460`; `streaming.go:63`; `subtitles.go:61,177`. Migrate direct `s.engine` uses: `Prepare` Add/Files/PrepareFiles/Status (1434/1441/1472/1475) capture `engine := s.engines.Default()` once after the allocation gate; issuance 1471/1488 -> `s.engines.DefaultPrefix()+hash`; `TestEngine` 1308; `catalog.go:504` nil-guard -> `s.engines.Default() == nil`; `torrentmeta.go:48` `ResolveMagnet` -> captured acquisition default engine. Make error strings engine-neutral (1446/1724/1888/2476) and replace `"unsupported engine route"` errors with `fmt.Errorf("%w: %s (%v)", domain.ErrEngineUnavailable, route, initErr-or-"not constructed")`. Rewrite `container.go:163-191` to build the map + `NewEngineSet`; delete `App.Engine io.Closer` and the `engineCloser` Close fallback (332-334). Migrate ~30 test constructor sites through `singleEngineSet`.

- [ ] **Step 4: Run** `go build ./... && go test ./internal/application -count=1` — PASS. **Step 5: Commit** `feat: route managed operations through persisted engine owners`.

### Task 3: Owner-retention and qB 4.3.9 season-pack regression

**Files:**
- Create: `internal/application/ownership_test.go`

**Interfaces:** Consumes Task 1/2. Fakes (embedding `TorrentEngine`, mutex-guarded): `multiEngine` with `Add`/`Files`/`Status`/`PrepareFiles`/`Remove` and `snapshot() engineSnapshot{adds, removed []string}`; `engineSetFor(t, defaultPrefix, engines)`. Reuse `retryHarness`, `testRegistry`, `openCatalog`, `seedRetentionRelease`, `seedRetentionDownload`, `canonicalReleaseID`, `retentionSettings` from existing tests.

- [ ] **Step 1: Write `TestPrepareSeasonReusesLegacyQBSeasonPackUnderNativeDefault`**: seed release `Show.S01.1080p.WEB-DL` (FileCount 4) via `repo.UpsertReleases`; `qb` fake holds `h1` with files `Show.S01E01.mkv`/`S01E02.mkv`/`srt`; `repo.SaveDownload` row `EngineID: "qb:h1"` file 0 under `DownloadRoot`; service default `native:` with both engines; call `service.PrepareSeason(ctx, stored[0].ID, 1)`; assert every returned row keeps `qb:` prefix and both fakes record zero adds. Real adapter magnet-gate coverage stays in `internal/adapters/qbittorrent/magnet_test.go` (run as-is; no adapter changes).

- [ ] **Step 2: Run** `go test ./internal/application -run PrepareSeasonReuses -count=1` — must PASS only after Task 2 cutover (pre-cutover it cannot express two engines).

- [ ] **Step 3: Commit** `test: retain legacy qb owner for managed season packs`.

### Task 4: Composition sizing and init-failure policy

**Files:**
- Modify: `internal/composition/container.go`, `internal/adapters/sqlite/repository.go`, `internal/composition/container_test.go`

**Interfaces:**
- Produces: `func (r *Repository) DistinctEnginePrefixes(ctx context.Context) ([]string, error)` (concrete SQLite method, not the port); `func requiredEngines(defaultPrefix string, persisted []string) []string` in `container.go`.

- [ ] **Step 1: Failing `requiredEngines` matrix test**: `native:` default + no rows -> one engine; `qb:` default + `native:` rows -> both.

- [ ] **Step 2: Implement**:

```go
func (r *Repository) DistinctEnginePrefixes(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT substr(engine_id, 1, instr(engine_id, ':')) FROM downloads WHERE instr(engine_id, ':') > 0 ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, rows.Err()
}
```

Composition: `persisted, err := repo.DistinctEnginePrefixes(ctx)` — return the error, never ignore it. Build each required engine; `nativetorrent.New` failure is fatal iff that prefix is the default, otherwise `set.MarkUnavailable("native:", err)` + `slog.Warn` and startup proceeds.

- [ ] **Step 3: Regression** (unit-level): qb default + `native:` row + failing native constructor -> startup OK, `EngineDefault()=="qbittorrent"`, `InitError("native:")` non-nil, `owner("native:x")` false.

- [ ] **Step 4: Run** `go test ./internal/composition ./internal/adapters/sqlite -count=1` — PASS. **Step 5: Commit** `feat: initialize only required engines with isolated failures`.

### Task 5: Shutdown ordering and retention uncertainty

**Files:**
- Modify: `internal/application/service.go` (`joinAndClose` 662-689, `retentionPlan` 160-165, `retentionSurvey` 171-204, `ensureAllocationRoom` 1327, `runRetention` 362-370), `internal/application/retention_test.go`/`ownership_test.go`

- [ ] **Step 1: Failing close test** (two `closerEngine`s, `service_test.go:411` pattern): each closed exactly once, both before repo, single `Close` idempotent; existing abort-on-timeout semantics preserved (workers-not-stopped leaves engines+repo open).

- [ ] **Step 2: Implement**: after worker join, close engines via `s.engines.Each` (type-assert `io.Closer`), first error kept, then `s.repo.Close()`.

- [ ] **Step 3: Failing uncertainty test** `TestSurveyUncertaintyBlocksAdmissionWithoutZeroCounting` (from `local://engine-ownership-plan.md`, verified): native default, `native:h1` live, `qb:h2` row with no qb engine; `retentionSettings(t, settings, 20, 0)`; assert `plan.uncertainOwners == ["qb:"]`; `ensureAllocationRoom` returns `ErrEngineUnavailable`-wrapped refusal; `RunRetention` completes with a warn log, both rows intact; `Prepare` on the native-owned row still succeeds (existing playback available).

- [ ] **Step 4: Implement**: `retentionSurvey` resolves each route's owner — unknown route or `Status` error appends the prefix to `uncertainOwners` and skips (route never enters `plan.routes`, so eviction cannot pick it); `ensureAllocationRoom` refuses when `len(plan.uncertainOwners) > 0`; `runRetention` logs the warn and continues (reserve enforced via real `freeBytes`; under-count can only under-evict).

- [ ] **Step 5: Run** `go test -race ./internal/application -run 'Close|Survey' -count=1` — PASS. **Step 6: Commit** `feat: isolate unavailable engine owners in shutdown retention and admission`.

### Task 6: Running-vs-saved engine contract (server + GUI parity)

**Files:**
- Modify: `internal/adapters/httpapi/schema.go` (`SettingsView` + `RedactedSettings(v, path, engineRunning string)`, callers `api.go:186`, `internal/gui/bindings.go:131-135`, `api_test.go:115,128`), `internal/gui/supervisor.go` (`appLike.EngineDefault() string`, stopped -> `""`), `internal/gui/bindings_test.go` parity test (extend `fakeApp`; add stopped-server case asserting `"engineRunning":""` on both sides)

- [ ] **Step 1: Failing tests** in `internal/adapters/httpapi/api_test.go`:
  - `TestSettingsExposeRunningEngineAlongsideSaved`: store saved `qbittorrent`, running default `native` -> GET `/api/v1/settings` serves `{"downloadEngine":"qbittorrent","engineRunning":"native"}`.
  - `TestTorrentEngineDiagnosticNamesRunningEngine`: POST `/api/v1/dependencies/qbittorrent/test` -> `success:true`, `engine:"native"` (the engine actually probed; no message-wording assertion — the identity field is the contract).
  Fixture: settings JSON in `t.TempDir()` via `config` env prefix; `application.NewEngineSet("native:", {"native:": versionEngine{}, "qb:": versionEngine{}})` with `versionEngine` embedding `application.TorrentEngine` and implementing `Test(ctx) ("v4.3.9", nil)`; `application.NewService(nil, engineSet, stubRepo{}, store)`; `New(service, store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")`.

- [ ] **Step 2: Implement**: add `EngineRunning string `json:"engineRunning"`` to `SettingsView` (empty when no server running); handler passes `a.service.EngineDefault()`; diagnostic case builds `{"success": true, "engine": engine, "message": "Connected to " + engine + " torrent engine: " + v}` from `TestEngine` + `EngineDefault()` (startup-fixed identity cannot mislabel); update schema help copy for `downloadEngine` (selection controls new acquisitions; restart required).

- [ ] **Step 3: Run** `go test ./internal/adapters/httpapi ./internal/gui -count=1` — PASS. **Step 4: Commit** `feat: expose running acquisition engine alongside saved selection`.

### Task 7: Web and TV engine feedback

**Files:**
- Modify: `web/settings.tsx:192` strip list adds `|| k === 'engineRunning'`; engine group note (ownership explanation) + mismatch notice in the `renderField` engine-toggle branch comparing `value.engineRunning` to `value.downloadEngine` (never the draft), rendered with the existing `supporting` class (no new CSS)
- Modify: `clients/tv/src/main.tsx:746` strip list adds `|| key === 'engineRunning'`; read-only `<p class="tv-muted">` copy + "Running now" notice after tracker controls (plain text, no focus-map change)
- Modify: `api/openapi.yaml` (version bump; `/settings` GET documents `engineRunning`; dependency-test 200 documents `engine`); `docs/API.md` bullets
- Test: `web/settings.test.tsx` (fixture gains `engineRunning: 'native'`; new cases: mismatch shows "Running now: …" + PUT body has no `engineRunning`; agreement hides notice), new `clients/tv/src/settings.test.tsx` following `setup-behavior.test.tsx` conventions (notice appears only on mismatch; unexpected PUT throws)

- [ ] **Step 1: Write the failing client tests** (both files). **Step 2: Run** `npm run test -w @torrent-tv/web` and `npm run test -w @torrent-tv/tv` — new cases FAIL.
- [ ] **Step 3: Implement** the strip lists, notices, and copy.
- [ ] **Step 4: Re-run** both suites plus `go test ./internal/gui -run TestBindingsSettingsSurfaceParityWithHTTP` — PASS (strip is load-bearing: server uses `DisallowUnknownFields`; without it every save 400s).
- [ ] **Step 5: Commit** `feat: surface running engine feedback in web and tv settings`.

### Task 8: Domain family resolver

**Files:**
- Modify: `internal/domain/catalog.go` (+ `internal/domain/catalog_test.go`)

**Interfaces:**
- Produces: `type CandidateAnchor struct { IMDbID string; CanonicalID string; KnownYears map[int]bool }`; `type ReleaseEvidence struct { ReleaseID, Title, SortTitle, IMDbID string; Year int }`; `func ResolveFamilyTitleIDs(kind MediaKind, sortTitle string, evidence []ReleaseEvidence) map[string]string`.

Pure, deterministic, order-independent. Anchors from distinct IMDb IDs get `CanonicalID = CatalogTitleID(TorrentRelease{IMDbID: id}, ParsedRelease{Kind: kind, SortTitle: sortTitle})` — always the base64url SHA256 hash, never a raw key. Missing-IMDb releases join an anchor only when exactly one compatible candidate remains: series -> single-anchor only; movie with year -> anchors whose known years are empty or contain it; movie without year -> single-anchor only. Otherwise the release keeps its fallback `CatalogTitleID(TorrentRelease{}, ParsedRelease{Kind, Title, SortTitle, Year})`. Known years derive strictly from anchor release years (no schema inventions). Distinct IMDb identities never merge; a second late series anchor makes missing-IMDb rows fall back again (reversible, no redirects).

- [ ] **Step 1: Table tests**: real IMDb IDs (`tt0087182` Dune 1984, `tt1160419` Dune 2021); 1984-year release joins the 1984 anchor, year-less stays fallback; two series anchors -> fallback; hash format equals `CatalogTitleID` output.
- [ ] **Step 2: Run** `go test ./internal/domain -count=1`. **Step 3: Commit** `feat: add deterministic imdb-first family title resolution`.

### Task 9: SQLite reconciliation

**Files:**
- Create: `internal/adapters/sqlite/reconcile.go`, `internal/adapters/sqlite/reconcile_test.go`
- Modify: `internal/adapters/sqlite/repository.go` (`UpsertReleases` 283-343, `migrate` 122, `catalogSourceSelect` 354-357, `scanCatalogSources`)

**Interfaces:**
- Produces: `func (r *Repository) ReconcileAllCatalogProjections(ctx context.Context) error`; internal `reconcileFamiliesTx(ctx, tx, families map[familyKey]struct{}) error`; `CatalogSource.TitleID string `json:"titleId,omitempty"`` (assess exposure; keep internal if no consumer needs it).

- [ ] **Step 1: Failing Silo regression** (`TestSiloFixtureConsolidatesToOneCard`, fixture verified against live shapes): 121 IMDb `tt14688458` releases (55 FileList + 66 Pirate Bay), 32 year-less missing-IMDb episodes, 2 missing-IMDb 2023 packs -> `QueryCatalogTitleIDs` total 1; `ListCatalogSourcesByTitleIDs` returns 155 sources, all with the projected `TitleID`, both tracker labels present. Plus: `TestUpsertArrivalOrderInversion` (completely reversed batch order yields identical canonical ID), `TestReopenPreservesConsolidation`, `TestMovieRemakesResolveCompatibleCandidates`, `TestLateAmbiguityRestoresFallbackWithoutRedirects` (favorite + metadata stay on the established anchor), `TestOmittedIMDbIsPinnedConflictIsReconciled`, `TestMergeMovesFavoritesMetadataAndQueuedJobs`.

- [ ] **Step 2: Run** `go test ./internal/adapters/sqlite -run 'TestSiloFixture|TestUpsertArrival|TestReopen|TestMovieRemakes|TestLateAmbiguity|TestOmittedIMDb|TestMergeMoves' -count=1` — FAIL.

- [ ] **Step 3: Implement**:
  1. `UpsertReleases`: pin provider IMDb on omission — `imdb_id = CASE WHEN excluded.imdb_id = '' THEN releases.imdb_id ELSE excluded.imdb_id END` (conflicting nonempty incoming IMDb still overwrites and re-triggers reconciliation).
  2. Gather **both** incoming families and the prior (pre-update) families of affected releases; inside the existing upsert transaction call `reconcileFamiliesTx`.
  3. `reconcileFamiliesTx`: resolve via `ResolveFamilyTitleIDs`; per-release updates only — `UPDATE catalog_releases SET title_id = ? WHERE release_id = ?` (never blanket `WHERE title_id = ?old`, so splits stay correct). Preserve the pre-reset assignments to compute extinct sources.
  4. `moveExtinctGroupDependents(oldID, newID)` only when `oldID` has 0 remaining releases AND exactly one destination: metadata transfers only if the target has none, else the extinct row is deleted (never refresh timestamps on stale content); favorites via `UPDATE OR IGNORE` + cleanup; queued/running jobs keep `job.id` and get **both** `dedupe_key` (exact equality per known key shape — no `LIKE`, since base64url IDs contain `_`) **and** their typed payload `TitleID` retargeted by unmarshal/marshal in Go; in-flight workers re-resolve the projected title ID before applying results; `sync_state` names retargeted the same way. On `UNIQUE` conflict the coalesced duplicate is deleted explicitly.
  5. `migrate`: after `backfillCatalog`, run `ReconcileAllCatalogProjections` (idempotent; second run writes nothing).
  6. `catalogSourceSelect` appends `c.title_id`; scan into `CatalogSource.TitleID`.

- [ ] **Step 4: Re-run** Step 2 command — PASS. **Step 5: Commit** `feat: project canonical title identities with transactional reconciliation`.

### Task 10: Consumer cutover to projected identities

**Files:**
- Modify: `internal/application/catalog.go` (692 `groupCatalog`, 495 `sourcesForTitle`), `internal/application/service.go` (2305 `titleSources`, 1246 manifest fan-out, 1588 detail-from-release, 1993 download title, 2416 household projection, 2214-2221 `SetFavorite`)

- [ ] **Step 1: Failing tests** (application-level, existing fixtures): a merged family's detail/favorites/downloads/household all resolve through the projected ID; `SetFavorite` resolves the current projected ID at write time (a stale raw ID from a caller must land on the projected group, not wait for repair). Batch release->title lookups via `CatalogTitleIDsForReleases(ctx, []string)` where a loop would query per item. Pre-persistence search dedupe at `service.go:909,951` keeps evidence keys (cannot discard distinct releases; projection converges later) — cover with a test that two same-name different-kind/different-year search results both survive ingestion.

- [ ] **Step 2: Implement** the callsite table, replacing every raw `CatalogTitleID` recomputation with the projected `TitleID`.
- [ ] **Step 3: Run** `go test ./internal/application -run Catalog -count=1 && go test -race ./internal/application ./internal/adapters/sqlite ./internal/domain -count=1` — PASS.
- [ ] **Step 4: Static cutover check** (specialized grep over `internal/`): no remaining `CatalogTitleID(` outside `domain`, reconcile seed logic, and the search-dedupe evidence keys.
- [ ] **Step 5: Commit** `feat: consume projected canonical titles across catalog surfaces`.

### Task 11: Native both-tracker download-and-stream verification harness

User acceptance: Native must download Pirate Bay magnets AND FileList `.torrent` files, and progressively stream both WHILE INCOMPLETE, with no qBittorrent reachable and no fallback.

**Files:**
- Create (throwaway, deleted after proof): `tools/smoke_native_trackers.go` (`//go:build ignore`)

**Ground truth (all verified in-repo):** fixture content `seedContent`/`buildTestMetainfo` shape from `internal/adapters/nativetorrent/client_test.go:22-78` (8 MiB two-episode pack + subs, deterministic infohash); loopback metadata seeder pattern `startMetadataSeeder` from `magnet_test.go:27-60` (`NoDHT`, ut_metadata over 127.0.0.1); progressive swarm pattern from `client_test.go:237-259` (`TestingConfig`, `Seed`, `AddPeers` to Native's `TorrentPeerPort`); real adapters `filelist.Client.Acquire` (`/download.php?id=…&passkey=…`, `client.go:145`) and `piratebay.Client.Acquire` (`/t.php?id=…`, `client.go:158`, builds magnet from the detail `info_hash`); acquisition flow `torrentmeta.go:22-48` (`.torrent` bytes pass through; magnet -> `ResolveMagnet` on the captured default engine); endpoints `POST /api/v1/releases/{id}/prepare` (`api.go:84`) and ranged `GET /api/v1/streams/{id}` (`api.go:124`); reference assertions in `tools/progressive_stream_smoke.py`.

- [ ] **Step 1: Build the harness**: loopback HTTP mock on 127.0.0.1:8099 serving a generated `.torrent` at `/filelist/download.php` and apibay detail JSON at `/piratebay/t.php` (same infohash as the fixture metainfo); loopback seeder client serving the fixture data to Native's peer port; boots the real server (`go run ./cmd/server`) with a temp settings file: `downloadEngine: native`, loopback tracker URLs, temp DB/download/session dirs, `trustedCidrs: 127.0.0.0/8`. Seeds `releases` rows `fl-101` and `pb-202` via the repository before startup.
- [ ] **Step 2: FileList leg**: `POST /api/v1/releases/fl-101/prepare` -> 202, `engineId` has `native:` prefix, progress < 1; `GET /api/v1/streams/{id}` with `Range: bytes=0-1048575` -> 206, `Content-Range: bytes 0-1048575/4194304`, body equals `bytes.Repeat([]byte("a"), 1<<20)`; `GET /api/v1/downloads` shows progress < 1.0 and `trackerId: "filelist"`; the deselected episode has zero downloaded bytes in the piece map.
- [ ] **Step 3: Pirate Bay leg**: same assertions after `POST /api/v1/releases/pb-202/prepare` — magnet metadata resolves over loopback from the seeder (no DHT, no public network), `trackerId: "piratebay"`, 206 body bytes exact, progress < 1.0 while streaming.
- [ ] **Step 4: Isolation assertions**: throughout both legs nothing listens on qBittorrent's port, qBittorrent is never constructed (Native-only settings: no `qb:` engine required and none built), every new row is `native:`-owned, and the engine diagnostic reports the native engine. Record incomplete progress at stream start for both legs (acceptance evidence). Streaming while downloading is the product highlight: this task's evidence is the primary acceptance gate.
- [ ] **Step 5: Run** the harness; then delete `tools/smoke_native_trackers.go` and record outcomes in the task notes. Tracker HTTP responses are local fixtures — report them as such, not as live-tracker proof.
- [ ] **Step 6: Commit** (nothing lands; harness is throwaway — this task's deliverable is the recorded verification evidence).

### Task 12: Mixed-owner compatibility re-check and full gate

- [ ] **Step 1: Run** `go test ./internal/application -run 'Concurrent|TrackerEligibility' -count=1` (goroutine search survives reconciliation untouched) and `go test ./internal/adapters/qbittorrent ./internal/adapters/nativetorrent -count=1` (4.3.9 magnet gate + native adapter regressions).
- [ ] **Step 2: Full gate**: `make check` then `npm run test:clients`; `git diff --check`.
- [ ] **Step 3: Docs cutover**: update ADR-0007 (replace one-active-engine rule), `CONTEXT.md`/`docs` engine sections, OpenAPI version, and settings help copy. Remove any text the change obsoletes.
- [ ] **Step 4: Commit** `docs: replace single-engine rule with owner-based routing`.

## Self-Review

- Spec coverage: engine design -> Tasks 1-5; settings/diagnostics + clients -> 6-7; canonical design -> 8-10; verification matrix -> 3, 5, 9, 11, 12; documentation -> 12; no deployment/feed scope.
- Placeholders: none — every code step carries its contract or exact SQL/test names; fixture signatures verified via language server (`UpsertReleases` returns `[]domain.TorrentRelease`; `QueryCatalogTitleIDs` returns `domain.Page[string]`; `SetFavorite(ctx, profileID, releaseID, favorite)`; `CatalogTitleIDsForReleases(ctx, []string) (map[string]string, error)`).
- Type consistency: `EngineSet`/`owner`/`EngineDefault`/`TestEngine` pinned identically across Tasks 1, 2, 4, 6; `TitleID` projection consistent across 9-10; feedback fields `engineRunning`/`engine` consistent across 6-7.
