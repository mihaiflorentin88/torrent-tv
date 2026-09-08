# Native Public and Private Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the built-in Native engine real public peer discovery (DHT + PEX) for Pirate Bay magnets while keeping every private torrent out of DHT and peer exchange, without disturbing existing downloads, media paths, session state, or `native:`/`qb:` ownership.

**Architecture:** Two underlying `torrent.Client` instances inside the existing `nativetorrent.Client` facade — a private client (`NoDHT`, `DisablePEX`) and a public client (DHT + PEX on, library-managed announcements). Admission classifies complete metainfo by `Info.Private`; saved session metainfo re-classifies on restore. Magnets require explicit public-source authorization (`MagnetDiscovery` on the acquisition path; Pirate Bay is the only authorized source). One session store, one bolt piece-completion owner, one per-hash gate, shared infohash layout; a per-hash owner map routes every operation to exactly one underlying client.

**Tech Stack:** Go (anacrolix/torrent v1.61.0 pinned, anacrolix/dht/v2 v2.23.0), platform config + HTTP settings schema + web/desktop settings surfaces, `make check` / `make test`.

**Spec:** `docs/superpowers/specs/2026-09-08-native-public-private-discovery-design.md` (approved). This plan argues from the spec; executors read both.

## Global Constraints

- No new application engines, owner prefixes (`native:`/`qb:` unchanged), download roots, media copies, reacquisition, or qBittorrent fallback. Native stays the acquisition default.
- Private torrents must never touch DHT or PEX. The pinned library has NO runtime private-flag enforcement (verified in v1.61.0 source: `client.go:297` builds the LTEP map from `DisablePEX`; `peerconn.go:1049-1126` gates PEX on `!DisablePEX` and `pex.IsEnabled()`; injected `DefaultStorage` is never closed by `Client.Close()` — `client.go:303-314`).
- Classification is by complete metainfo only (`Info.Private` *bool); never by tracker hostname, settings, or warning strings.
- Magnet resolution without explicit public-source authorization is denied before ANY network/library activity (`domain.ErrMagnetDiscoveryDenied`). Pirate Bay sets `MagnetDiscoveryPublic`; FileList supplies metainfo (no magnet) and stays zero.
- Preexisting hash export never mutates, moves between clients, or drops anything. Unexpected private metadata on an authorized public resolve → drop transient + `domain.ErrPrivateMagnet`. The source-trust limitation (public lookup precedes metadata) is documented, never described as retroactive protection.
- Ports: private keeps `torrentPeerPort` (42069 default); public adds `torrentPublicPeerPort` (default 0 = OS-assigned), validated 0..65535, rejected when equal to a nonzero private port, restart-required. No router forwarding, no blanket IPv6 disable.
- Keep deadlines unchanged (90 s metadata, 5 s waitInfo, 600 s piece wait); no timeout inflation, no tracker pruning, no tracker-warning presentation changes, no UI redesign.
- Tests are behavioral: real loopback transfers, wire-observed DHT/PEX, no config-field assertions or source-text pinning. `NoSecurity` is REQUIRED on loopback DHT nodes (DHT-sec rejects loopback-derived IDs).
- Deployment is a local Pi binary swap only (env-provided host, never committed): backup kept, no qBittorrent reconfiguration, never `deploy/pi-deploy.sh` (it stops/reconfigures qBittorrent and deletes its rollback backup). No release, image, or feed announcement.
- One commit per task; working tree stays free of unrelated edits.

---

### Task 1: Magnet discovery policy through the engine boundary

**Files:**
- Modify: `internal/domain/tracker.go`, `internal/application/ports.go`, `internal/application/torrentmeta.go`, `internal/adapters/piratebay/client.go`, `internal/adapters/qbittorrent/magnet.go`
- Modify (mechanical stub/signature migration): `internal/adapters/nativetorrent/magnet.go` (signature only this task), `internal/httpapi/stream_test.go`, `internal/httpapi/trackers_test.go`, `internal/application/engine_test.go`, `internal/application/trackers_test.go`, `internal/composition/container_test.go`, plus existing direct test callsites in `internal/adapters/nativetorrent/magnet_test.go` and `internal/adapters/qbittorrent/magnet_test.go`

**Interfaces:**
- Produces: `domain.MagnetDiscovery` (uint8, zero = denied), `domain.MagnetDiscoveryPublic`, `domain.ErrMagnetDiscoveryDenied`, `domain.ErrPrivateMagnet`, `TorrentAcquisition.MagnetDiscovery`
- Changes: `TorrentEngine.ResolveMagnet(ctx, uri, downloadRoot string, discovery domain.MagnetDiscovery) ([]byte, error)` — every implementor and caller migrated in this task.

- [ ] **Step 1: Write failing tests.**
  - `internal/adapters/qbittorrent/magnet_test.go` — `TestResolveMagnetDeniesUnsetDiscovery`:
    ```go
    func TestResolveMagnetDeniesUnsetDiscovery(t *testing.T) {
        s := newResolveServer(t, "v4.5.0")
        s.setReady(true)
        c := s.newClient()
        _, err := c.ResolveMagnet(context.Background(), magnetTestURI(), t.TempDir(), 0)
        if !errors.Is(err, domain.ErrMagnetDiscoveryDenied) {
            t.Fatalf("err = %v, want domain.ErrMagnetDiscoveryDenied", err)
        }
        if got := s.addCountValue(); got != 0 {
            t.Fatalf("denied resolve issued %d add requests, want 0", got)
        }
    }
    ```
    And `TestResolveMagnetRejectsPrivateMetadata`: build a private info dict bencode by hand (dict key order length < name < piece length < pieces < private is already sorted):
    ```go
    raw := []byte("d8:announce13:https://test/4:info" +
        "d6:lengthi5e4:name9:video.mp412:piece lengthi4e6:pieces20:aaaaaaaaaaaaaaaaaaaa8:privatei1eee" + "e")
    ```
    Register it on the fake daemon (mirror `magnetTestMetainfo` plumbing via `s.exportHook`/`s.mu torrents` as existing tests do), resolve with `domain.MagnetDiscoveryPublic`, expect `errors.Is(err, domain.ErrPrivateMagnet)` and `assertResolverCleanup(t, s, hash, downloadRoot)`.
  - `internal/adapters/piratebay/client_test.go` — extend `TestAcquireBuildsMagnetFromNumberValuedDetail` after the existing assertions (line ~105):
    ```go
    if acq.MagnetDiscovery != domain.MagnetDiscoveryPublic {
        t.Fatalf("piratebay acquisition must authorize public discovery, got %d", acq.MagnetDiscovery)
    }
    ```
- [ ] **Step 2: Run** `go test ./internal/adapters/qbittorrent ./internal/adapters/piratebay -count=1` — expect FAIL (signature mismatch / missing sentinel).
- [ ] **Step 3: Implement.**
  - `internal/domain/tracker.go`: add to `TorrentAcquisition` the field `MagnetDiscovery MagnetDiscovery`; next to the error block (line 32-37):
    ```go
    // MagnetDiscovery authorizes peer discovery for magnet resolution. The
    // zero value denies resolution before any network activity; only a
    // recognized public source (currently Pirate Bay) grants it.
    type MagnetDiscovery uint8

    const MagnetDiscoveryPublic MagnetDiscovery = 1

    var (
        ErrMagnetDiscoveryDenied = errors.New("magnet source did not authorize discovery")
        ErrPrivateMagnet         = errors.New("public discovery resolved a private torrent")
    )
    ```
  - `internal/application/ports.go:44`: `ResolveMagnet(ctx context.Context, uri string, downloadRoot string, discovery domain.MagnetDiscovery) ([]byte, error)`.
  - `internal/application/torrentmeta.go:48`: pass `acquisition.MagnetDiscovery` as fourth argument.
  - `internal/adapters/piratebay/client.go:207`: `acq := domain.TorrentAcquisition{Magnet: uri, MagnetDiscovery: domain.MagnetDiscoveryPublic}`.
  - `internal/adapters/qbittorrent/magnet.go:294`: signature gains `discovery domain.MagnetDiscovery`; FIRST statement (before `c.Test(ctx)` and before parsing, so denial causes zero daemon requests): `if discovery != domain.MagnetDiscoveryPublic { return nil, domain.ErrMagnetDiscoveryDenied }`. In the owned-resolver path after `exportAndVerify` (line 428-432) classify before returning: decode `metaBytes` via `metainfo.Load` + `UnmarshalInfo` (imports `github.com/anacrolix/torrent/metainfo`), and when `info.Private != nil && *info.Private` return `domain.ErrPrivateMagnet` (the existing `defer` cleanup removes the transient torrent and resolver dir). Preexisting export paths (`exportPreexisting` line 317/369) stay untouched — preservation precedes admission. Add a comment: discovery authorization is a boundary guard; the daemon's own DHT config governs actual qBittorrent discovery.
  - Mechanical 4th-parameter migration (compile-enforced cutover) of every stub and direct test call listed under Files: `stream_test.go:62`, `trackers_test.go:74` (+ closures ~269/293; set `MagnetDiscoveryPublic` in fixtures for realism), `engine_test.go:48`, `trackers_test.go:436`, `container_test.go:140`, and all existing native/qb direct `ResolveMagnet(...)` test callsites.
- [ ] **Step 4: Verify** `go build ./... && go vet ./internal/... && go test ./internal/domain/... ./internal/adapters/nativetorrent/... ./internal/adapters/qbittorrent/... ./internal/adapters/piratebay/... ./internal/application/... ./internal/adapters/httpapi/... ./internal/composition/... -count=1` — expect PASS (new tests green, all callsites migrated).
- [ ] **Step 5: Commit** `git commit -m "feat(domain): authorize magnet discovery at the engine boundary"`.

---

### Task 2: Public peer port configuration and settings propagation

**Files:**
- Modify: `internal/platform/config/config.go` (struct ~line 41, defaults ~line 81, validate ~line 395, `RestartRequired` ~line 553), `internal/platform/config/config_test.go` (`TestDownloadEngineValidation` ~line 344), `internal/adapters/httpapi/schema.go` (after `torrentPeerPort` line 68), `internal/adapters/httpapi/api_test.go` (`TestSettingsSchemaMarksEngineFieldsRestartRequired` ~line 480), `internal/composition/container.go` (native construction line 184-190, gains `PublicPeerPort` in Task 3 — wire here but the field lands in Task 3; to keep this task compiling, add the field to `nativetorrent.Config` here as part of this task), `internal/composition/container_test.go` (~line 360 JSON body gains `"torrentPublicPeerPort": 0`), `web/settings.tsx` (line 85 fields array), `web/settings.test.tsx` (settingsValue line 18, schemaFields ~line 35, hidden-fields PUT assertion ~line 569), `desktop/src/pages/settings-page.test.tsx` (fixture line 90), regenerate `desktop/src/bindings/.../models.ts` (both files) via `wails3 generate bindings -d desktop/src/bindings -ts`.

**Interfaces:**
- Produces: `Settings.TorrentPublicPeerPort int` (`json:"torrentPublicPeerPort"`), default `0`; `nativetorrent.Config.PublicPeerPort int` (consumed in Task 3).
- Validation: `0..65535`, and `TorrentPublicPeerPort != TorrentPeerPort` when both nonzero (`torrentPublicPeerPort must differ from torrentPeerPort`). Restart-required group extended.

- [ ] **Step 1: Write failing tests.**
  ```go
  // config_test.go, inside TestDownloadEngineValidation after the session-dir check:
  base.TorrentSessionDir = "data/torrent-session"
  base.TorrentPublicPeerPort = -1
  if err := (&Store{}).validate(base); err == nil {
      t.Fatal("negative torrentPublicPeerPort must fail validation")
  }
  base.TorrentPublicPeerPort = 42069
  if err := (&Store{}).validate(base); err == nil {
      t.Fatal("duplicate fixed peer ports must fail validation")
  }
  base.TorrentPublicPeerPort = 0
  if err := (&Store{}).validate(base); err != nil {
      t.Fatalf("OS-assigned public port must validate: %v", err)
  }
  ```
  `api_test.go`: add `"torrentPublicPeerPort"` to the restart-required key list. `web/settings.test.tsx`: add `torrentPublicPeerPort: 0` to `settingsValue`, a schemaFields entry with `restartRequired: true`, and `expect(put.body.torrentPublicPeerPort).toBe(0)` in the hidden-fields PUT test.
- [ ] **Step 2: Run** `go test ./internal/platform/config ./internal/adapters/httpapi -count=1` — expect FAIL.
- [ ] **Step 3: Implement** exactly per Interfaces: struct field after `TorrentPeerPort`; `TorrentPublicPeerPort: 0` in `Defaults()`; validation block mirroring the private port plus the duplicate check; `RestartRequired` gains `|| old.TorrentPublicPeerPort != new.TorrentPublicPeerPort`; schema entry `{Key: "torrentPublicPeerPort", Label: "Torrent public peer port", Help: "Port the built-in engine's public-discovery listener uses. 0 lets the OS pick one. Changing it requires restart.", RestartRequired: true}`; composition passes `PublicPeerPort: current.TorrentPublicPeerPort`; web fields array gains `['Torrent public peer port', 'torrentPublicPeerPort', 'number']`; regenerate bindings; desktop fixture gains `torrentPublicPeerPort: 0`. Env `TORRENT_TV_TORRENT_PUBLIC_PEER_PORT` works automatically via reflection (`applyEnvironment`).
- [ ] **Step 4: Verify** `go test ./internal/platform/config ./internal/adapters/httpapi ./internal/composition -count=1` and (if node_modules available) the web/desktop vitest suites — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(config): torrentPublicPeerPort setting for public discovery listener"`.

---

### Task 3: Split the Native adapter into isolated private/public clients

**Files:**
- Modify: `internal/adapters/nativetorrent/client.go` (Config, Client struct, `New`, `newClientConfig`, `Close`, `loadSession`, `torrent`, `Add`, `Test`, owner routing), `internal/adapters/nativetorrent/magnet.go` (routes transient adds to the public client; full behavior lands in Task 4), `internal/adapters/nativetorrent/speed.go` (iterate both clients), `internal/adapters/nativetorrent/client_test.go` (migrate helpers), `internal/adapters/nativetorrent/magnet_test.go` (existing tests compile; Task 4 completes behavior).

**Interfaces:**
- `Client` gains `privateClient, publicClient *torrent.Client` plus `owners map[metainfo.Hash]*torrent.Client` (guarded by `c.mu`; `c.cl` is REMOVED — a missed routing callsite must fail compilation).
- `func newClientConfig(cfg Config, public bool, capture *announceCapture) *torrent.ClientConfig`: fresh `torrent.NewDefaultClientConfig()` per client (never share mutable config objects). Both: `DefaultStorage = storage.NewFileOpts(...)` (fresh impl per client over the SHARED bolt pc, same `TorrentDirMaker`/`UsePartFiles: g.Some(false)`), `Seed`, `NoDefaultPortForwarding`, `newTrackerIdentity()`, `Slogger` from the one shared `capture`. Private: `NoDHT=true`, `DisablePEX=true`, `ListenPort=cfg.PeerPort`. Public: `ListenPort=cfg.PublicPeerPort`, DHT/PEX on with library defaults (periodic announcements included).
- `New(cfg)`: `newWithTorrentClient(cfg, factory)` where `factory func(*torrent.ClientConfig) (*torrent.Client, error)` defaults to `torrent.NewClient`; builds private then public; any failure closes everything already built then pc (mirrors current error paths at lines 137/156-160); `loadSession` classifies before `AddTorrent`; `Close()` closes private, public, then pc exactly once.
- `torrent(hash)` resolves via `owners`, falling back to scanning both clients (no duplicate admission is possible: every admission path — `Add`, `loadSession`, resolve — writes `owners[ih]`).
- `Test()` reports private torrent count + private listen port as today, plus public port.

- [ ] **Step 1: Write failing test** in `client_test.go`:
  ```go
  func TestPrivateTorrentRoutedToPrivateClient(t *testing.T) {
      root := seedContent(t)
      _, raw := buildTestMetainfo(t, root) // private=true fixture
      c := newTestClient(t)
      hash, err := c.Add(context.Background(), bytes.NewReader(raw), t.TempDir())
      if err != nil { t.Fatal(err) }
      if n := len(c.publicClient.Torrents()); n != 0 {
          t.Fatalf("public client holds %d torrents, want 0", n)
      }
      if n := len(c.privateClient.Torrents()); n != 1 {
          t.Fatalf("private client holds %d torrents, want 1", n)
      }
      // Public-flag metainfo (absent private bit) routes public:
      _, publicRaw := buildPublicTestMetainfo(t, root)
      phash, err := c.Add(context.Background(), bytes.NewReader(publicRaw), t.TempDir())
      if err != nil { t.Fatal(err) }
      if phash == hash { t.Fatal("fixture collision") }
      if n := len(c.publicClient.Torrents()); n != 1 {
          t.Fatalf("public client holds %d torrents after public add, want 1", n)
      }
      // Full facade surface still resolves both:
      if _, err := c.Files(context.Background(), hash); err != nil { t.Fatal(err) }
      if _, err := c.Files(context.Background(), phash); err != nil { t.Fatal(err) }
  }
  ```
  Plus a helper `buildPublicTestMetainfo` identical to `buildTestMetainfo` without setting `info.Private`. Existing tests using `c.cl.Torrents()` assertions (client_test.go:132, magnet_test.go:267) migrate to `c.privateClient.Torrents()` (their fixtures are private).
- [ ] **Step 2: Run** `go test ./internal/adapters/nativetorrent -count=1` — expect FAIL.
- [ ] **Step 3: Implement** per Interfaces. `loadSession` decodes each entry's metainfo (`metainfo.Load` + `UnmarshalInfo`; malformed metainfo still fails startup exactly as today), routes private-flagged entries to `privateClient`, others to `publicClient`, records `owners`. `Add` classifies after parsing and before `AddTorrentSpec` and records `owners[ih]`. `speedLoop` samples `append(c.privateClient.Torrents(), c.publicClient.Torrents()...)`. `Remove`/`Pause`/`Resume`/`PrepareFiles`/`PrepareRange`/`Pieces`/`Status`/`Files` all keep calling `c.torrent(hash)` — unchanged once `torrent()` consults `owners`. The per-hash gate and lock ordering (gateMu before mu) are untouched.
- [ ] **Step 4: Verify** `go test ./internal/adapters/nativetorrent -count=1` and `go build ./...` — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(nativetorrent): isolate private and public torrent clients behind one facade"`.

---

### Task 4: Public-source magnet resolution with private-metadata rejection

**Files:**
- Modify: `internal/adapters/nativetorrent/magnet.go` (signature from Task 1, routing + classification).

**Interfaces:**
- `ResolveMagnet(ctx, uri, _, discovery)`: zero/unknown `discovery` → `domain.ErrMagnetDiscoveryDenied` as the FIRST statement (before parse, gate, session, or library calls). Otherwise behavior mirrors today: preexisting hash (either client, via `c.torrent`) exports metadata without mutation; new hash adds a transient torrent on `publicClient` (discovery explicitly authorized), waits `GotInfo` bounded by ctx, classifies resolved metainfo, then exports and drops the transient (`defer t.Drop()` unchanged).
- New helper `rejectPrivateMetainfo(t *torrent.Torrent) error`: `mi := t.Metainfo(); info, err := mi.UnmarshalInfo(); if err != nil { return fmt.Errorf("decode resolved metainfo info: %w", err) }; if info.Private != nil && *info.Private { return domain.ErrPrivateMagnet }; return nil`. Applied ONLY on the two new-resolution paths (`!new` shared-wait and owned transient) before `exportMetainfo` — never on the preexisting-export path.

- [ ] **Step 1: Write failing tests** in `magnet_test.go`:
  ```go
  func TestResolveMagnetDeniesUnsetDiscovery(t *testing.T) {
      c := newTestClient(t)
      ih := "abababababababababababababababababababab"
      _, err := c.ResolveMagnet(t.Context(),
          "magnet:?xt=urn:btih:"+ih+"&x.pe=127.0.0.1:1", t.TempDir(), 0)
      if !errors.Is(err, domain.ErrMagnetDiscoveryDenied) {
          t.Fatalf("err = %v, want domain.ErrMagnetDiscoveryDenied", err)
      }
      if c.torrent(ih) != nil {
          t.Fatal("denied resolve must not touch the session or clients")
      }
  }

  func TestResolveMagnetRejectsPrivateMetadata(t *testing.T) {
      root := seedContent(t)
      mi, _ := buildTestMetainfo(t, root) // private=true fixture
      addr := startMetadataSeeder(t, mi, root)
      c := newTestClient(t)
      ih := mi.HashInfoBytes()
      ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
      defer cancel()
      _, err := c.ResolveMagnet(ctx, magnetFor(ih, addr), t.TempDir(), domain.MagnetDiscoveryPublic)
      if !errors.Is(err, domain.ErrPrivateMagnet) {
          t.Fatalf("err = %v, want domain.ErrPrivateMagnet", err)
      }
      if c.torrent(ih.HexString()) != nil {
          t.Fatal("rejected private magnet must not be admitted to any client")
      }
  }
  ```
  Existing fetch/cancel/preserve tests migrate to `buildPublicTestMetainfo` + `domain.MagnetDiscoveryPublic` (Task 3 helper); the preserve-existing-session test keeps preexisting-path expectations with NO classification there.
- [ ] **Step 2: Run** `go test ./internal/adapters/nativetorrent -run 'TestResolveMagnet' -count=1` — expect new tests FAIL (denial not yet first), existing PASS.
- [ ] **Step 3: Implement** per Interfaces. Keep the gate span, bounded wait, drop-only-owned-transient semantics, and size guards exactly as today.
- [ ] **Step 4: Verify** `go test ./internal/adapters/nativetorrent -count=1` — expect PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(nativetorrent): resolve authorized public magnets on the public client"`.

---

### Task 5: Controlled DHT discovery and privacy regression

**Files:**
- Create: `internal/adapters/nativetorrent/discovery_test.go` (loopback DHT node, PEX harness, four tests).

**Interfaces:**
- `newWithTorrentClient` factory seam (from Task 3) injects, BEFORE `torrent.NewClient`: `SetListenAddr("127.0.0.1:0")`, `DisableIPv6`, `DisableTrackers=true` (DHT provably the sole path), and for the public client `DhtStartingNodes` returning the controlled node and `Callbacks.PeerConnReadExtensionMessage` recording `(ext id, payload)` for ut_pex wire observation. No globals, production `New` unchanged.
- `controlledDHTNode(t)`: `dht.NewServer(&dht.ServerConfig{Conn: <udp 127.0.0.1:0>, NoSecurity: true, StartingNodes: static self-addr, OnAnnouncePeer: recorder})` (single node; `Server.Announce(ih, port, impliedPort)` seeds its peer store — verified signature `announce.go:80`; fallback is a second node announcing into the first if single-node announce proves flaky).
- Seeder: bare library client on loopback (`startMetadataSeeder` pattern) whose client exposes a real DHT server via `torrent.NewAnacrolixDhtServer(conn)`; `t.AnnounceToDht(s)` puts it in the controlled node's peer store.

- [ ] **Step 1: Write the tests.**
  - `TestPublicClientDiscoversSeederViaDHTAndDownloads`: tracker-less, `x.pe`-less magnet (`magnet:?xt=urn:btih:<ih>` only); `ResolveMagnet(..., MagnetDiscoveryPublic)` must obtain metadata purely through the controlled DHT (get_peers → peer → ut_metadata), then re-Add the raw metainfo (public classification), `PrepareFiles`, `waitForPieceStates(..., 2)`, and byte-compare payload against `seedContent`. This is the test that FAILS pre-fix (NoDHT) — prove it by stashing the split temporarily if desired, but its red state against the old binary is recorded, not asserted in code.
  - `TestPrivateTorrentNeverTouchesDHT`: private-flagged metainfo added via facade Add routes to the private client; over a window, the controlled node's recorder logs ZERO packets sourced from the engine's UDP addresses and the private torrent gains no peers; additionally a private-only client built via the factory asserts `DhtServers()` empty.
  - `TestPublicClientExchangesUtPex`: raw-TCP BitTorrent peer harness (~150 lines, test-local): dial the public client's listen addr, standard handshake with reserved bit 0x10, extended handshake `{'m':{'ut_pex':11},'p':<harness port>,'v':'pex-harness'}`; two harness peers connect; A sends crafted ut_pex `{'added':<6B decoy 127.0.0.1:port>,'added.f':'\x00'}` → assert the client dials the decoy listener AND B receives outbound ut_pex containing the learned address (first PEX flush fires immediately — `pexconn.go:51` `time.AfterFunc(0)`).
  - `TestPrivateClientIgnoresUtPex`: same harness against the private client: its extended handshake m-dict omits ut_pex (wire-observable), a crafted inbound ut_pex is ignored (decoy listener sees NO dial; recorder shows zero outbound ut_pex payloads).
- [ ] **Step 2: Run** `go test ./internal/adapters/nativetorrent -run 'TestPublicClientDiscoversSeederViaDHTAndDownloads|TestPrivateTorrentNeverTouchesDHT|TestPublicClientExchangesUtPex|TestPrivateClientIgnoresUtPex' -count=1 -v` — expect ALL PASS. Any flakiness in single-node announce → implement the two-node fallback documented above, never sleeps-based timing hacks beyond bounded `waitFor` polling.
- [ ] **Step 3: Verify no production code changed in this task** (`git diff --stat` shows only the new test file); `go vet ./internal/adapters/nativetorrent`.
- [ ] **Step 4: Commit** `git commit -m "test(nativetorrent): prove loopback DHT discovery and private DHT/PEX isolation"`.

---

### Task 6: Persistence, concurrency, and streaming regressions

**Files:**
- Create/Modify: `internal/adapters/nativetorrent/client_test.go` (new tests), possibly `session_test.go` (mixed-restore).

- [ ] **Step 1: Write the tests.**
  - Mixed restore: build a facade, Add one private + one public torrent with selections and one paused; construct a SECOND facade over the same dirs; assert each hash landed on the same classified client, selections and paused state re-applied, and piece-completion bytes reused (verified pieces stay complete — no re-download, no rehash-only path).
  - Concurrent Add/Resolve for one hash: run `Add(public metainfo)` and `ResolveMagnet(public magnet for same ih)` concurrently; exactly one session write, no duplicate torrent on either client, no gate deadlock (test completes under `-race`).
  - Cancellation: cancel the resolve context mid-wait; transient torrent dropped, no session entry, managed torrent untouched.
  - Removal across clients: Remove(deleteFiles=true) on a public torrent and a private torrent clears the right windows/selected/writeErrs/session rows and data dirs; bolt pc still owned by the facade (both clients closed first in `Close`).
  - Progressive serving: after a verified-partial public download, serve via the real HTTP range path (existing `stream_test.go` stub pattern or direct `PrepareRange` + file read) and confirm a mid-file range returns 206 with verified bytes before completion.
- [ ] **Step 2: Run** `go test -race ./internal/adapters/nativetorrent -count=1` — expect PASS.
- [ ] **Step 3: Run the full gate:** `make check && make test` — expect PASS (this is the whole-repo green gate).
- [ ] **Step 4: Commit** `git commit -m "test(nativetorrent): mixed-client restore, concurrency, and streaming regressions"`.

---

### Task 7: Real-network smoke proof and local Pi rollout

**Files:**
- Modify: `docs/adr/0007-native-torrent-engine.md`, `docs/CONFIGURATION.md` (post-proof only). No changelog exists — do not create one.

- [ ] **Step 1: Build and local smoke.** `make build-arm64-headless` → `bin/torrent-tv-linux-arm64-headless`. Run the binary locally (headless) against a scratch data dir; acquire a known-live legally distributable public torrent through the Pirate Bay path (e.g. a Debian netinst or similar authorized swarm): metadata resolves, peers arrive via DHT (not x.pe/tracker alone), verified bytes transfer, `/api/v1/downloads` reports progress, and an HTTP range request serves real bytes. Keep this data isolated and delete it after proof.
- [ ] **Step 2: Pi binary swap** (host from session env `PI_HOST`; NEVER `deploy/pi-deploy.sh`; never commit host/IP):
  ```sh
  scp bin/torrent-tv-linux-arm64-headless "$PI_HOST:/tmp/torrent-tv-fix"
  ssh "$PI_HOST" 'set -e
    cp /var/lib/torrent-tv/bin/torrent-tv /var/lib/torrent-tv/bin/torrent-tv.previous
    install -m 0755 /tmp/torrent-tv-fix /var/lib/torrent-tv/bin/torrent-tv.new
    mv /var/lib/torrent-tv/bin/torrent-tv.new /var/lib/torrent-tv/bin/torrent-tv
    sudo systemctl restart torrent-tv
    sleep 3; systemctl is-active torrent-tv'
  ```
  Verify startup health via the existing HTTP surface (base URL from `PI_BASE_URL`): `/api/v1/downloads` lists all 17 existing downloads unchanged (no deletion/reacquisition, `native:`/`qb:` ids intact, Fauda still seeding, Silo rows metadata-present).
- [ ] **Step 3: Existing-download behavior.** Record separately for each stalled Silo row: metadata availability, peers, received bytes, and whether progressive serving produces HTTP 206 below 100% with parseable media. Resume/Resume only through the user-visible surface if useful; NEVER delete or switch ownership. Check qB-owned and FileList private media remain accessible.
- [ ] **Step 4: Rollback readiness.** If startup fails or existing-download access regresses: `ssh "$PI_HOST" 'cp /var/lib/torrent-tv/bin/torrent-tv.previous /var/lib/torrent-tv/bin/torrent-tv && sudo systemctl restart torrent-tv'`. Never roll back by deleting household state.
- [ ] **Step 5: Honest reporting gate.** A DHT handshake or added peers alone is NOT acceptance. If the stalled swarms still yield no usable peers, report the proven engine capability (controlled DHT/PEX regression green, real-network smoke green) alongside the unproven live-swarm diagnosis and continue investigating that path per the spec.
- [ ] **Step 6: Documentation.** After proof: update ADR-0007 (two isolated clients, classification boundary, source-eligibility policy and its trust limitation, `torrentPublicPeerPort`) and `docs/CONFIGURATION.md` line 26 (replace "without DHT" prose with public/private discovery description + new setting). Commit `git commit -m "docs: describe isolated public/private native torrent discovery"`.
- [ ] **Step 7: Cleanup.** Remove only task-created verification artifacts (scratch dirs, /tmp binary); keep the retained `torrent-tv.previous` rollback binary; leave household data untouched.

---

## Self-Review Notes

- **Spec coverage:** every spec section maps to a task — policy boundary (1), ports/config + restart + no-IPv6-disable (2), two-client isolation/ownership/close-order/identity (3), magnet lifecycle incl. preexisting preservation + private rejection (4), controlled DHT + PEX wire evidence + red-proven discovery (5), mixed restore/concurrency/removal/streaming/HTTP-range (6), real network + Pi swap + rollback + honest-diagnosis gate + docs (7).
- **Placeholders:** none — every implementation step names symbols, files, and line anchors verified in the current tree; every test step contains concrete test code or a concrete fixture recipe with named existing helpers (`buildTestMetainfo`, `startMetadataSeeder`, `magnetFor`, `newResolveServer`, `assertResolverCleanup`, `waitForPieceStates`).
- **Cross-task consistency:** `MagnetDiscovery`/sentinels (Task 1) consumed by 4; `Config.PublicPeerPort` defined in 2, consumed in 3; `newWithTorrentClient` factory defined in 3, used by 5; `buildPublicTestMetainfo` defined in 3, used in 4-5; `owners` map semantics fixed in 3 and relied on by 4-6; `c.cl` removal in 3 makes missed routing a compile error.
