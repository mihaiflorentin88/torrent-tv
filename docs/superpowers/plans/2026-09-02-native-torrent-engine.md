# Native Torrent Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed a native BitTorrent engine (anacrolix/torrent, pinned) behind the existing `TorrentEngine` port so qBittorrent becomes an optional engine choice, with canonical download-state DTOs and an engine route registry.

**Architecture:** New adapter `internal/adapters/nativetorrent` implements the same ten-method `TorrentEngine` contract the qBittorrent adapter implements. Playback stays disk-first: pieces land at `<DownloadRoot>/<infohash>/<file path>`, the app polls `Pieces()` and serves file ranges from disk exactly as it does for qBittorrent. The application layer loses its qBittorrent-isms: `engineHash`'s hardcoded `qb:` prefix becomes a configurable route prefix, and both adapters emit canonical download states from their boundary. An optional `PrepareRange` contract method lets the native engine elevate exactly the pieces a seek or probe needs.

**Tech Stack:** Go 1.26, `github.com/anacrolix/torrent v1.61.0` (MPL-2.0, pure Go), existing `modernc.org/sqlite` (pure Go), Docker Compose.

**Spec:** `docs/superpowers/specs/2026-09-02-native-torrent-engine-design.md` — the plan argues from the spec; executors read both.

## Global Constraints

- Every build/test invocation that guards the platform matrix sets `CGO_ENABLED=0`. Six targets: windows/linux/darwin × amd64/arm64. No dependency may introduce cgo.
- `github.com/anacrolix/torrent` is pinned to exactly `v1.61.0` in `go.mod`. Never upgrade without checking its `retract` list.
- Only adapters translate engine states. Application code consumes `domain` state constants and helpers; raw qBittorrent strings stop at the qbit adapter.
- The streaming path (pieces poll → `WaitRange` → serve from disk) does not change. No reader-based serving; the library `Reader` is used only to steer piece priorities.
- Branch: `feature/torrent-client`. Run `make check` before each task's final commit; full suite with `go test -race ./...` at tasks 6, 7, and 10.
- anacrolix v1.61.0 API facts this plan relies on (verified against the module source): `cl.AddTorrent(*metainfo.MetaInfo) (*Torrent, error)`; `t.Files()`, `t.Info()`, `t.NumPieces()`, `t.PieceState(i)`, `t.BytesCompleted()`, `t.Stats()`, `t.Drop()`, `t.AddClientPeer(cl)`, `t.VerifyData()`, `File.SetPriority(PiecePriority)`/`Offset()`/`Length()`/`BytesCompleted()`/`Path()`; `Torrent.DownloadPieces/CancelPieces`; NO public per-piece priority setter; NO `Torrent.Start/Stop`; `metainfo.Load(io.Reader)`, `mi.HashInfoBytes()`, `metainfo.PieceKey{InfoHash, Index}`; `storage.NewBoltPieceCompletion(dir)`, `storage.NewFileOpts(NewFileClientOpts{ClientBaseDir, TorrentDirMaker, UsePartFiles, PieceCompletion})`; default `NewFile` dirs torrents by info *name* and uses part-files — both must be overridden.

## File Structure

- Create `internal/domain/state.go` — canonical download states + `IsPaused`.
- Modify `internal/application/ports.go` — `PrepareRange` on `TorrentEngine`.
- Modify `internal/application/service.go` — route prefix, `TestEngine`, `IsPaused` adoption, `PrepareRange` call site, prefix-based route construction.
- Modify `internal/application/streaming.go`, `internal/application/retention.go` — route() call sites.
- Modify `internal/adapters/qbittorrent/client.go` (+`client_test.go`) — state mapping, `PrepareRange` no-op.
- Create `internal/adapters/nativetorrent/client.go`, `session.go`, `speed.go` — the native adapter.
- Create `internal/adapters/nativetorrent/client_test.go`, `session_test.go` — unit + offline-swarm integration tests.
- Create `internal/application/diskfree_darwin.go`, `internal/application/diskfree_windows.go`; modify `diskfree_other.go` — six-platform Reserve probe.
- Modify `internal/platform/config/config.go` (+`config_test.go`) — `downloadEngine`, `torrentPeerPort`, `torrentSessionDir`.
- Modify `internal/composition/container.go` — engine selection + lifecycle.
- Modify `Makefile` — `build-all` six-target matrix.
- Modify `compose.yml` — qbit sidecar to a profile, native env defaults.
- Create `docs/adr/0007-native-torrent-engine.md`; modify `docs/adr/0005-qbittorrent-sidecar-without-auth.md`, `CONTEXT.md`, and the spec's scheduling sentence.

---

### Task 1: Canonical download states

**Files:**
- Create: `internal/domain/state.go`
- Test: `internal/domain/state_test.go`
- Modify: `internal/application/service.go:1265`, `internal/adapters/qbittorrent/client.go` (Status state field), `internal/adapters/qbittorrent/client_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `domain.StateDownloading/StateSeeding/StatePausedDL/StatePausedUP/StateQueued/StateError` (string constants), `domain.IsPaused(state string) bool`; qbit `Status()` returns canonical states.

- [ ] **Step 1: Write the failing test**

```go
// internal/domain/state_test.go
package domain

import "testing"

func TestIsPaused(t *testing.T) {
	cases := map[string]bool{
		StatePausedDL:    true,
		StatePausedUP:    true,
		"pausedDL":       true,
		"pausedUP":       true,
		"stoppedDL":      true, // qBittorrent 5: stopped means paused
		"stoppedUP":      true,
		"PausedDL":       true,
		StateDownloading: false,
		StateSeeding:     false,
		StateQueued:      false,
		StateError:       false,
		"uploading":      false,
		"stalledUP":      false,
		"":               false,
	}
	for state, want := range cases {
		if got := IsPaused(state); got != want {
			t.Errorf("IsPaused(%q) = %v, want %v", state, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/domain/ -run TestIsPaused -v`
Expected: FAIL (undefined: IsPaused)

- [ ] **Step 3: Write the implementation**

```go
// internal/domain/state.go
package domain

import "strings"

// Canonical download states. Every TorrentEngine adapter emits these from its
// boundary (ports.go). Downloads persisted before the canonical vocabulary
// carry raw qBittorrent strings; the helpers here accept both vocabularies so
// old rows keep working without migration.
const (
	StateDownloading = "downloading"
	StateSeeding     = "seeding"
	StatePausedDL    = "pausedDL"
	StatePausedUP    = "pausedUP"
	StateQueued      = "queued"
	StateError       = "error"
)

// IsPaused reports whether a download must be resumed before playback.
// Accepts the canonical vocabulary and legacy qBittorrent strings, including
// the qBittorrent 5 stoppedDL/stoppedUP forms that mean paused.
func IsPaused(state string) bool {
	s := strings.ToLower(state)
	return strings.HasPrefix(s, "paused") || s == "stoppeddl" || s == "stoppedup"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/domain/ -run TestIsPaused -v`
Expected: PASS

- [ ] **Step 5: Adopt `IsPaused` at the playback resume check and map qbit states at the adapter boundary**

`internal/application/service.go` line 1265 (in the prepare-for-playback path) replace:

```go
	if strings.HasPrefix(strings.ToLower(status.State), "paused") {
```

with:

```go
	if domain.IsPaused(status.State) {
```

In `internal/adapters/qbittorrent/client.go` add the mapping function (package level, near the other helpers):

```go
// canonicalState maps a raw qBittorrent state string onto the canonical
// download-state vocabulary (domain/state.go). Unknown values pass through
// unchanged so novel engine states are never silently reclassified.
func canonicalState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "downloading", "metadl", "forceddl", "stalleddl", "allocating", "checkingdl", "checkingresumedata":
		return domain.StateDownloading
	case "uploading", "stalledup", "forcedup", "checkingup":
		return domain.StateSeeding
	case "pauseddl", "stoppeddl":
		return domain.StatePausedDL
	case "pausedup", "stoppedup":
		return domain.StatePausedUP
	case "queueddl", "queuedup":
		return domain.StateQueued
	case "error", "missingfiles":
		return domain.StateError
	default:
		return raw
	}
}
```

and wrap the state in `Status()` (the `domain.DownloadStatus{...}` literal around line 250):

```go
	State: canonicalState(s(row, "state")),
```

Add the mapping test to `internal/adapters/qbittorrent/client_test.go`:

```go
func TestCanonicalState(t *testing.T) {
	cases := map[string]string{
		"downloading": domain.StateDownloading, "metaDL": domain.StateDownloading,
		"forcedDL": domain.StateDownloading, "stalledDL": domain.StateDownloading,
		"uploading": domain.StateSeeding, "stalledUP": domain.StateSeeding,
		"pausedDL": domain.StatePausedDL, "stoppedDL": domain.StatePausedDL,
		"pausedUP": domain.StatePausedUP, "stoppedUP": domain.StatePausedUP,
		"queuedDL": domain.StateQueued, "queuedUP": domain.StateQueued,
		"error": domain.StateError, "missingFiles": domain.StateError,
		"weird": "weird",
	}
	for raw, want := range cases {
		if got := canonicalState(raw); got != want {
			t.Errorf("canonicalState(%q) = %q, want %q", raw, got, want)
		}
	}
}
```

Update any `client_test.go`/other assertions that now observe mapped states: `"uploading"` from a fake server surfaces as `"seeding"`, `"stalledUP"` as `"seeding"`, `"stoppedDL"` as `"pausedDL"`. Substring-based logic elsewhere (`catalog.go` contains-"paused"/-"queued" aggregation) works unchanged for both vocabularies — leave it; add a one-line comment there: `// matches canonical (domain/state.go) and legacy qBittorrent state strings`.

- [ ] **Step 6: Run the affected suites**

Run: `go test ./internal/domain/ ./internal/adapters/qbittorrent/ ./internal/application/ -v`
Expected: PASS (fix any assertion that observed raw states, using the mapping table above)

- [ ] **Step 7: Commit**

```bash
git add internal/domain/state.go internal/domain/state_test.go internal/application/service.go internal/adapters/qbittorrent/client.go internal/adapters/qbittorrent/client_test.go
git commit -m "feat(domain): canonical download states emitted at adapter boundaries"
```

---

### Task 2: Engine route prefix

**Files:**
- Modify: `internal/application/service.go` (struct field ~line 70, `engineHash` at 1911, `WaitRange` at 1824, prefix constructions at ~945, ~959, ~1131-1202), `internal/application/streaming.go:61`, `internal/application/retention.go:193`
- Test: `internal/application/service_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `(*Service).SetEngineRoutePrefix(prefix string)`, `(*Service).route(engineID string) (hash string, ok bool)`; `engineHash` deleted.

- [ ] **Step 1: Write the failing test**

Append to `internal/application/service_test.go`:

```go
func TestEngineRoutePrefix(t *testing.T) {
	s := &Service{}
	if hash, ok := s.route("qb:abc123"); !ok || hash != "abc123" {
		t.Fatalf("default prefix must resolve qb: routes, got %q %v", hash, ok)
	}
	s.SetEngineRoutePrefix("native:")
	if hash, ok := s.route("native:deadbeef"); !ok || hash != "deadbeef" {
		t.Fatalf("native prefix must resolve, got %q %v", hash, ok)
	}
	if _, ok := s.route("qb:abc123"); ok {
		t.Fatal("a foreign engine route must not resolve")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/application/ -run TestEngineRoutePrefix -v`
Expected: FAIL (no SetEngineRoutePrefix / route)

- [ ] **Step 3: Implement**

In `internal/application/service.go`:

1. Add field to the `Service` struct: `engineRoutePrefix string`.
2. Replace `engineHash` (line 1911) with:

```go
// SetEngineRoutePrefix selects the Engine route prefix the active engine
// issues ("qb:" or "native:"). Composition calls it at startup; the empty
// default keeps the historical qBittorrent routing.
func (s *Service) SetEngineRoutePrefix(prefix string) {
	s.engineRoutePrefix = prefix
}

func (s *Service) enginePrefix() string {
	if s.engineRoutePrefix == "" {
		return "qb:"
	}
	return s.engineRoutePrefix
}

// route splits an Engine route into its engine hash. Routes issued by another
// engine fail the prefix check and surface as unavailable downloads — the
// behavior that already exists when an engine torrent goes missing.
func (s *Service) route(engineID string) (string, bool) {
	return strings.CutPrefix(engineID, s.enginePrefix())
}
```

3. Call sites `hash, ok := engineHash(...)` become `hash, ok := s.route(...)`: `WaitRange` (1824), `waitReadablePath` (streaming.go:61), retention eligibility (retention.go:193). Error text `"unsupported engine route"` stays.
4. Every route *construction* `"qb:" + hash` / `"qb:"+hash` in service.go (grep the literal `"qb:"` — prepare paths around lines 945, 959, 1131-1202) becomes `s.enginePrefix() + hash`. The engineID carried through managed-download preparation must flow from `s.enginePrefix()` so native rows are born `native:<hash>`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/application/ -v`
Expected: PASS (all existing `qb:`-based fixtures keep passing via the default prefix)

- [ ] **Step 5: Commit**

```bash
git add internal/application/service.go internal/application/streaming.go internal/application/retention.go internal/application/service_test.go
git commit -m "feat(application): engine route prefix replaces hardcoded qb: routing"
```

---

### Task 3: Contract — PrepareRange and TestEngine

**Files:**
- Modify: `internal/application/ports.go:32-43`, `internal/application/service.go:810`, `internal/application/streaming.go` (waitReadablePath, ~line 73), `internal/adapters/qbittorrent/client.go`, `internal/adapters/httpapi/api.go:269`
- Test: fake engines in `internal/application/*_test.go`, `internal/adapters/qbittorrent/client_test.go`, `internal/adapters/httpapi/*_test.go`

**Interfaces:**
- Consumes: Task 2's route resolution in `waitReadablePath`.
- Produces: `TorrentEngine.PrepareRange(ctx context.Context, hash string, fileIndex int, start, count int64) error` (global torrent byte offsets); `(*Service).TestEngine(ctx) (string, error)`.

- [ ] **Step 1: Extend the port and make the suite fail to compile**

In `internal/application/ports.go` inside `TorrentEngine` after `PrepareFiles`:

```go
	// PrepareRange elevates download priority for the pieces covering a byte
	// window of the torrent. start and count are torrent-global byte offsets
	// (the download's file offset plus the requested media range). Engines
	// without window scheduling implement this as a no-op; their sequential
	// scheduler eventually covers any range.
	PrepareRange(ctx context.Context, hash string, fileIndex int, start, count int64) error
```

Rename `TestQB` (service.go:810) to:

```go
func (s *Service) TestEngine(ctx context.Context) (string, error) { return s.engine.Test(ctx) }
```

and the httpapi call site (api.go:269) to `a.service.TestEngine(ctx)`. The HTTP route and response shape do not change.

- [ ] **Step 2: Run the suite to enumerate broken fakes**

Run: `go build ./... && go vet ./internal/...`
Expected: compile errors listing every test fake missing `PrepareRange` (e.g. `pieceEngine`, `streamingEngine`, `retentionEngine`, `capacityEngine`, `retryEngine`, `prepareGateEngine`, `streamEngine`).

- [ ] **Step 3: Satisfy every fake with the documented no-op**

For each fake named in step 2 add:

```go
func (e *fakeType) PrepareRange(context.Context, string, int, int64, int64) error { return nil }
```

In `internal/adapters/qbittorrent/client.go` implement the real (documented) no-op:

```go
// PrepareRange is a no-op: qBittorrent exposes no range-priority API and its
// sequential download scheduler already reaches any requested range.
func (c *Client) PrepareRange(context.Context, string, int, int64, int64) error {
	return nil
}
```

- [ ] **Step 4: Wire the call site**

In `internal/application/streaming.go` `waitReadablePath`, immediately before the `WaitRange` call:

```go
	if err = strategy.service.engine.PrepareRange(ctx, hash, d.FileIndex, d.FileOffset+start, count); err != nil {
		return "", err
	}
```

- [ ] **Step 5: Run the suites**

Run: `go test ./internal/... -v`
Expected: PASS — existing stream/prepare/retention behavior unchanged (qbit path no-ops).

- [ ] **Step 6: Commit**

```bash
git add internal/application/ports.go internal/application/service.go internal/application/streaming.go internal/adapters/qbittorrent/client.go internal/adapters/httpapi/api.go internal/application/*_test.go
git commit -m "feat(engine): PrepareRange window contract and engine-neutral TestEngine"
```

---

### Task 4: anacrolix dependency and native adapter skeleton (New, Add, Files, Test)

**Files:**
- Modify: `go.mod` / `go.sum` (add `github.com/anacrolix/torrent v1.61.0`)
- Create: `internal/adapters/nativetorrent/client.go`
- Test: `internal/adapters/nativetorrent/client_test.go`

**Interfaces:**
- Consumes: `domain.TorrentFile`, `domain.ErrTorrentNotFound`.
- Produces (final shapes — later tasks extend, never rename):

```go
type Config struct {
	DataDir     string // media files: <DataDir>/<infohash>/<torrent-relative path>
	SessionDir  string // session.json + bolt piece-completion db
	PeerPort    int    // 0 = OS-assigned; compose default 42069
	Readahead   int64  // seek-window size in bytes (settings.ReadAheadBytes)
	StartWindow int64  // initial window at prepare (settings.InitialBufferBytes)
}
func New(cfg Config) (*Client, error)
func (c *Client) Close() error
func (c *Client) Test(ctx context.Context) (string, error)
func (c *Client) Add(ctx context.Context, r io.Reader, savePath string) (string, error)
func (c *Client) Files(ctx context.Context, hash string) ([]domain.TorrentFile, error)
```

- [ ] **Step 1: Pin the dependency**

Run: `go get github.com/anacrolix/torrent@v1.61.0 && go mod tidy`
Expected: `go.mod` gains `github.com/anacrolix/torrent v1.61.0` exactly; `CGO_ENABLED=0 go build ./...` still succeeds.

- [ ] **Step 2: Write the failing test**

```go
// internal/adapters/nativetorrent/client_test.go
package nativetorrent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/bencode"
)

// buildTestMetainfo builds a real multi-file metainfo from files on disk and
// returns the parsed MetaInfo plus the raw bencode bytes a FileList download
// would deliver.
func buildTestMetainfo(t *testing.T, root string) (mi metainfo.MetaInfo, raw []byte) {
	t.Helper()
	var info metainfo.Info
	info.Private = true
	if err := info.BuildFromFilePath(root); err != nil {
		t.Fatal(err)
	}
	b, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi = metainfo.MetaInfo{InfoBytes: b}
	raw, err = bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	return mi, raw
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		DataDir:     t.TempDir(),
		SessionDir:  t.TempDir(),
		PeerPort:    0,
		Readahead:   1 << 20,
		StartWindow: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func seedContent(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Pack.S01.1080p")
	if err := os.MkdirAll(filepath.Join(root, "Subs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Pack.S01E01.mkv"), bytes.Repeat([]byte("a"), 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Pack.S01E02.mkv"), bytes.Repeat([]byte("b"), 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Subs", "E01.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAddExposesFilesWithOffsets(t *testing.T) {
	root := seedContent(t)
	_, raw := buildTestMetainfo(t, root)
	c := newTestClient(t)

	hash, err := c.Add(t.Context(), bytes.NewReader(raw), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 40 {
		t.Fatalf("expected 40-char infohash hex, got %q", hash)
	}
	files, err := c.Files(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}
	var offset int64
	for i, f := range files {
		if f.Index != i {
			t.Errorf("file %d has Index %d", i, f.Index)
		}
		if f.Offset != offset {
			t.Errorf("file %d Offset = %d, want %d", i, f.Offset, offset)
		}
		offset += f.SizeBytes
	}
	if files[0].Path != "Pack.S01.1080p/Pack.S01E01.mkv" || !files[0].Playable {
		t.Errorf("unexpected first file %+v", files[0])
	}
	if files[2].Playable {
		t.Errorf("srt must not be playable: %+v", files[2])
	}
}

func TestAddIsIdempotent(t *testing.T) {
	root := seedContent(t)
	_, raw := buildTestMetainfo(t, root)
	c := newTestClient(t)
	h1, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("duplicate add returned %q then %q", h1, h2)
	}
	if got := len(c.cl.Torrents()); got != 1 {
		t.Fatalf("expected 1 torrent in client, got %d", got)
	}
}

func TestTestReportsTorrentCount(t *testing.T) {
	c := newTestClient(t)
	msg, err := c.Test(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if msg == "" {
		t.Fatal("Test must return a diagnostic string")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/adapters/nativetorrent/ -v`
Expected: FAIL (package does not exist)

- [ ] **Step 4: Implement the skeleton**

```go
// internal/adapters/nativetorrent/client.go
// Package nativetorrent implements the TorrentEngine port with an embedded
// anacrolix/torrent client. Media bytes land at
// <DataDir>/<infohash-hex>/<torrent-relative path> so the application's
// disk-first progressive serving reads them exactly like qBittorrent content.
package nativetorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	g "github.com/anacrolix/generics"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// Config is the native engine's deployment surface.
type Config struct {
	// DataDir holds media files: <DataDir>/<infohash-hex>/<torrent-relative path>.
	DataDir string
	// SessionDir holds session.json and the bolt piece-completion database.
	SessionDir string
	// PeerPort is the BitTorrent listen port; 0 lets the OS assign one.
	PeerPort int
	// Readahead is the seek-window size in bytes.
	Readahead int64
	// StartWindow is the window elevated when a file is first prepared.
	StartWindow int64
}

type Client struct {
	cl       *torrent.Client
	dataDir  string
	cfg      Config

	mu      sync.Mutex
	windows map[string]*streamWindow
	paused  map[string]bool
	session *sessionStore
	speeds  map[string]*speedMeter
}

// New constructs the engine and reloads every persisted torrent from the
// session store.
func New(cfg Config) (*Client, error) {
	if cfg.DataDir == "" || cfg.SessionDir == "" {
		return nil, errors.New("native engine requires dataDir and sessionDir")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("native engine data dir: %w", err)
	}
	if err := os.MkdirAll(cfg.SessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("native engine session dir: %w", err)
	}
	pc, err := storage.NewBoltPieceCompletion(cfg.SessionDir)
	if err != nil {
		return nil, fmt.Errorf("native engine piece completion db: %w", err)
	}
	impl := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir: cfg.DataDir,
		// Torrent display names are arbitrary tracker bytes and can be invalid
		// path components (a colon on Windows); key the layout by infohash.
		TorrentDirMaker: func(baseDir string, _ *metainfo.Info, ih metainfo.Hash) string {
			return filepath.Join(baseDir, ih.HexString())
		},
		// Pieces must write in place at their final paths: playback reads the
		// files directly, and part-file promotion would hide incomplete bytes
		// behind a .part rename until completion.
		UsePartFiles:    g.Some(false),
		PieceCompletion: pc,
	})
	tcfg := torrent.NewDefaultClientConfig()
	tcfg.DataDir = cfg.DataDir
	tcfg.DefaultStorage = impl
	tcfg.NoDHT = true                   // FileList is a private tracker; torrents are private-flagged
	tcfg.NoDefaultPortForwarding = true // household appliance; never poke the router
	tcfg.Seed = true                    // seed until eviction
	tcfg.ListenPort = cfg.PeerPort
	cl, err := torrent.NewClient(tcfg)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("native torrent client: %w", err)
	}
	c := &Client{
		cl:      cl,
		dataDir: cfg.DataDir,
		cfg:     cfg,
		windows: map[string]*streamWindow{},
		paused:  map[string]bool{},
		speeds:  map[string]*speedMeter{},
		session: newSessionStore(cfg.SessionDir, pc),
	}
	if err := c.loadSession(); err != nil {
		_ = cl.Close()
		_ = pc.Close()
		return nil, fmt.Errorf("native engine session reload: %w", err)
	}
	go c.speedLoop()
	return c, nil
}

func (c *Client) Close() error {
	c.stopSpeedLoop()
	errs := c.cl.Close()
	if c.session.pc != nil {
		if err := c.session.pc.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// torrent returns the live torrent for an infohash hex string, or nil.
func (c *Client) torrent(hash string) *torrent.Torrent {
	ih, err := metainfo.HashFromHexString(hash)
	if err != nil {
		return nil
	}
	for _, t := range c.cl.Torrents() {
		if t.InfoHash() == ih {
			return t
		}
	}
	return nil
}

// Add registers a .torrent (bencode metainfo from the tracker) and returns
// its infohash hex. Adding an infohash the engine already holds is
// idempotent. Nothing downloads until PrepareFiles selects files.
func (c *Client) Add(ctx context.Context, r io.Reader, _ string) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return "", fmt.Errorf("read torrent metainfo: %w", err)
	}
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("torrent metainfo: %w", err)
	}
	hash := mi.HashInfoBytes().HexString()
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := c.torrent(hash); t != nil {
		return hash, nil
	}
	t, err := c.cl.AddTorrent(mi)
	if err != nil {
		return "", fmt.Errorf("add torrent: %w", err)
	}
	// FileList metainfo carries the info dictionary, so metadata is
	// effectively immediate; wait briefly rather than assume.
	if err := waitInfo(ctx, t); err != nil {
		return "", err
	}
	if err := c.session.putMeta(hash, raw); err != nil {
		return "", fmt.Errorf("persist torrent session: %w", err)
	}
	return hash, nil
}

func waitInfo(ctx context.Context, t *torrent.Torrent) error {
	deadline := time.Now().Add(5 * time.Second)
	for t.Info() == nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return errors.New("native engine torrent metadata timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// Files lists the torrent's files in metainfo order with cumulative byte
// offsets. An empty slice means metadata is not ready yet; the caller polls.
func (c *Client) Files(_ context.Context, hash string) ([]domain.TorrentFile, error) {
	t := c.torrent(hash)
	if t == nil {
		return nil, domain.ErrTorrentNotFound
	}
	if t.Info() == nil {
		return []domain.TorrentFile{}, nil
	}
	files := t.Files()
	out := make([]domain.TorrentFile, 0, len(files))
	for i, f := range files {
		size := f.Length()
		progress := 0.0
		if size > 0 {
			progress = float64(f.BytesCompleted()) / float64(size)
		}
		out = append(out, domain.TorrentFile{
			Index:     i,
			Path:      f.Path(),
			SizeBytes: size,
			Offset:    f.Offset(),
			Progress:  progress,
			Playable:  playable(f.Path()),
		})
	}
	return out, nil
}

// Test reports a diagnostic for the settings test endpoint.
func (c *Client) Test(_ context.Context) (string, error) {
	return fmt.Sprintf("native torrent engine: %d torrents, peer port %d", len(c.cl.Torrents()), c.cl.ListenAddrs()[0].Port), nil
}

// playable mirrors the qbit adapter's media-extension test; the adapters stay
// deliberately independent.
func playable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mkv", ".avi", ".webm", ".mov", ".m4v", ".ts", ".mpg", ".mpeg":
		return true
	default:
		return false
	}
}
```

Notes for the implementer:
- `metainfo.HashFromHexString` — if that constructor does not exist under this name in v1.61.0, use `var ih metainfo.Hash; _ = ih.FromHexString(hash)` semantics via `infohash.FromHexString` (alias `metainfo.NewHashFromHex`): `ih, err := metainfo.NewHashFromHex(hash)`. Compile decides; both are the same function.
- `c.cl.ListenAddrs()` in `Test` — guard with `if addrs := c.cl.ListenAddrs(); len(addrs) > 0` and fall back to `"native torrent engine: %d torrents"` if empty.
- `c.session.pc` — the bolt piece-completion handle. In this task the session store is a minimal in-memory stub so Tasks 4-6 compile and run; Task 7 makes it durable. `New` constructs `pc` first and passes the single handle to both the storage impl and the store:

```go
// internal/adapters/nativetorrent/session.go — Task 4 stub; Task 7 replaces the bodies.
type sessionEntry struct {
	MediaIndices    []int
	SubtitleIndices []int
	Paused          bool
}

type sessionStore struct {
	path    string
	pc      storage.PieceCompletion
	entries map[string]sessionEntry
}

func newSessionStore(dir string, pc storage.PieceCompletion) *sessionStore {
	return &sessionStore{path: filepath.Join(dir, "session.json"), pc: pc, entries: map[string]sessionEntry{}}
}

func (s *sessionStore) putMeta(string, []byte) error            { return nil }
func (s *sessionStore) setSelection(string, []int, []int) error { return nil }
func (s *sessionStore) setPaused(string, bool) error            { return nil }
func (s *sessionStore) delete(string, bool) error               { return nil }
func (s *sessionStore) lookup(string) ([]int, []int, bool, bool) { return nil, nil, false, false }
func (c *Client) loadSession() error                            { return nil }
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/adapters/nativetorrent/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/adapters/nativetorrent/
git commit -m "feat(nativetorrent): embedded anacrolix engine skeleton with add/files"
```

---

### Task 5: Native Status and Pieces

**Files:**
- Modify: `internal/adapters/nativetorrent/client.go`
- Create: `internal/adapters/nativetorrent/speed.go`
- Test: `internal/adapters/nativetorrent/client_test.go`

**Interfaces:**
- Consumes: Task 4 `Client`.
- Produces: `Status(ctx, hash) (domain.DownloadStatus, error)`, `Pieces(ctx, hash) (domain.PieceMap, error)`.

- [ ] **Step 1: Write the failing test**

Append to `client_test.go`:

```go
func TestStatusAndPiecesWithoutActivity(t *testing.T) {
	root := seedContent(t)
	_, raw := buildTestMetainfo(t, root)
	c := newTestClient(t)
	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.Status(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hash != hash || st.State != domain.StateDownloading || st.Progress != 0 {
		t.Fatalf("unexpected status %+v", st)
	}
	filesSum, err := c.Files(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	var wantTotal int64
	for _, f := range filesSum {
		wantTotal += f.SizeBytes
	}
	if st.TotalBytes != wantTotal {
		t.Fatalf("TotalBytes = %d, want %d", st.TotalBytes, wantTotal)
	}
	if st.SavePath == "" || !strings.HasSuffix(filepath.Join(st.ContentPath), hash) {
		t.Fatalf("content path must live under the infohash dir: %+v", st)
	}
	if st.TempPathEnabled || !st.Sequential || !st.FirstLastPriority {
		t.Fatalf("native engine reports in-place paths and always-on scheduling: %+v", st)
	}
	pm, err := c.Pieces(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if pm.PieceSize <= 0 || len(pm.States) == 0 {
		t.Fatalf("expected piece map, got %+v", pm)
	}
	for _, s := range pm.States {
		if s != 0 {
			t.Fatalf("fresh torrent must have all pieces missing, got %v", pm.States)
		}
	}
	if _, err := c.Status(t.Context(), "ffffffffffffffffffffffffffffffffffffffff"); !errors.Is(err, domain.ErrTorrentNotFound) {
		t.Fatalf("unknown hash must map to ErrTorrentNotFound, got %v", err)
	}
}
```

Adjust `TotalBytes` expectation to the actual seeded byte count (compute from `buildTestMetainfo` files: 2×4 MiB + len(srt)).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adapters/nativetorrent/ -run TestStatusAndPieces -v`
Expected: FAIL (Status/Pieces not implemented)

- [ ] **Step 3: Implement**

`internal/adapters/nativetorrent/speed.go`:

```go
package nativetorrent

import (
	"sync"
	"time"

	"github.com/anacrolix/torrent"
)

// speedMeter keeps an exponential moving average of per-second download
// deltas for one torrent.
type speedMeter struct {
	prev int64
	ema  float64
}

func (m *speedMeter) sample(read int64) float64 {
	delta := read - m.prev
	if delta < 0 {
		delta = 0 // counters can restart when a torrent is re-verified
	}
	m.prev = read
	m.ema = 0.7*m.ema + 0.3*float64(delta)
	return m.ema
}

// speedLoop samples every torrent's useful data counter once per second.
func (c *Client) speedLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.mu.Lock()
			for _, t := range c.cl.Torrents() {
				hash := t.InfoHash().HexString()
				m := c.speeds[hash]
				if m == nil {
					m = &speedMeter{}
					c.speeds[hash] = m
				}
				m.sample(int64(t.Stats().BytesReadData))
			}
			c.mu.Unlock()
		}
	}
}

func (c *Client) stopSpeedLoop() { close(c.stop) }

func (c *Client) currentSpeed(hash string) int64 {
	if m := c.speeds[hash]; m != nil {
		return int64(m.ema)
	}
	return 0
}
```

Add `stop chan struct{}` to `Client` (initialized `make(chan struct{})` in `New`).

In `client.go` add:

```go
// Status reports the engine-level DTO. Tracker seeder/leecher counts are not
// exposed by anacrolix v1.61.0 and are reported as zero; peers/seeds come
// from live connection gauges.
func (c *Client) Status(_ context.Context, hash string) (domain.DownloadStatus, error) {
	t := c.torrent(hash)
	if t == nil {
		return domain.DownloadStatus{}, domain.ErrTorrentNotFound
	}
	info := t.Info()
	var total, pieceSize int64
	if info != nil {
		total = info.TotalLength()
		pieceSize = info.PieceLength
	}
	done := t.BytesCompleted()
	progress := 0.0
	if total > 0 {
		progress = float64(done) / float64(total)
	}
	c.mu.Lock()
	paused := c.paused[hash]
	speed := c.currentSpeed(hash)
	c.mu.Unlock()
	st := t.Stats()
	eta := int64(0)
	if speed > 0 && total > done {
		eta = (total - done) / speed
	}
	state := domain.StateDownloading
	switch {
	case paused && progress >= 1:
		state = domain.StatePausedUP
	case paused:
		state = domain.StatePausedDL
	case progress >= 1:
		state = domain.StateSeeding
	}
	return domain.DownloadStatus{
		Hash:                hash,
		State:               state,
		Progress:            progress,
		DownloadedBytes:     done,
		TotalBytes:          total,
		SpeedBytesPerSecond: speed,
		ETASeconds:          eta,
		Peers:               st.TotalPeers,
		Seeds:               st.ConnectedSeeders,
		PieceSize:           pieceSize,
		Sequential:          true,
		FirstLastPriority:   true,
		SavePath:            c.dataDir,
		ContentPath:         filepath.Join(c.dataDir, hash),
		TempPathEnabled:     false,
	}, nil
}

// Pieces maps piece state to the qbit-compatible integer convention the
// application consumes: 0 missing, 1 in progress, 2 complete.
func (c *Client) Pieces(_ context.Context, hash string) (domain.PieceMap, error) {
	t := c.torrent(hash)
	if t == nil {
		return domain.PieceMap{}, domain.ErrTorrentNotFound
	}
	n := int(t.NumPieces())
	states := make([]int, n)
	for i := 0; i < n; i++ {
		ps := t.PieceState(i)
		switch {
		case ps.Complete:
			states[i] = 2
		case ps.Partial || ps.Checking || ps.QueuedForHash:
			states[i] = 1
		}
	}
	pieceSize := int64(0)
	if info := t.Info(); info != nil {
		pieceSize = info.PieceLength
	}
	return domain.PieceMap{States: states, PieceSize: pieceSize}, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/adapters/nativetorrent/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/nativetorrent/
git commit -m "feat(nativetorrent): status dto, piece map, download-speed sampling"
```

---

### Task 6: Native PrepareFiles, PrepareRange, Pause/Resume, Remove + offline swarm

**Files:**
- Modify: `internal/adapters/nativetorrent/client.go`
- Test: `internal/adapters/nativetorrent/client_test.go`

**Interfaces:**
- Consumes: Tasks 4-5.
- Produces: `PrepareFile`, `PrepareFiles`, `PrepareRange`, `Pause`, `Resume`, `Remove` — the adapter now fully satisfies `TorrentEngine`.

- [ ] **Step 1: Write the failing test (offline swarm, no tracker, no network)**

Append to `client_test.go`:

```go
func waitForPieceStates(t *testing.T, c *Client, hash string, first, last int, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pm, err := c.Pieces(t.Context(), hash)
		if err != nil {
			t.Fatal(err)
		}
		if last < len(pm.States) {
			ok := true
			for i := first; i <= last; i++ {
				if pm.States[i] != want {
					ok = false
					break
				}
			}
			if ok {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pieces %d-%d never reached state %d", first, last, want)
}

func TestProgressiveSwarmDownloadsOnlySelectedFiles(t *testing.T) {
	root := seedContent(t)
	mi, raw := buildTestMetainfo(t, root)

	seedCfg := torrent.TestingConfig(t)
	seedCfg.DataDir = root
	seedCfg.Seed = true
	seedCl, err := torrent.NewClient(seedCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer seedCl.Close()
	st, err := seedCl.AddTorrent(&mi)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.VerifyData(); err != nil {
		t.Fatal(err)
	}

	c := newTestClient(t)
	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	files, err := c.Files(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	e01, e02 := -1, -1
	for _, f := range files {
		switch filepath.Base(f.Path) {
		case "Pack.S01E01.mkv":
			e01 = f.Index
		case "Pack.S01E02.mkv":
			e02 = f.Index
		}
	}
	if e01 < 0 || e02 < 0 {
		t.Fatalf("expected both episodes in %+v", files)
	}

	if err := c.PrepareFiles(t.Context(), hash, []int{e01}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareRange(t.Context(), hash, e01, files[e01].Offset, 1<<20); err != nil {
		t.Fatal(err)
	}
	// Wire the swarm directly: no tracker involved.
	nt := c.torrent(hash)
	if nt == nil {
		t.Fatal("torrent missing from native client")
	}
	if n := nt.AddClientPeer(seedCl); n == 0 {
		t.Fatal("peer not added")
	}

	e01Len := files[e01].SizeBytes
	pieceSize := func() int64 { pm, _ := c.Pieces(t.Context(), hash); return pm.PieceSize }()
	first := files[e01].Offset / pieceSize()
	last := (files[e01].Offset + e01Len - 1) / pieceSize()
	waitForPieceStates(t, c, hash, int(first), int(last), 2, 60*time.Second)

	// The deselected episode must never have been requested.
	pm, err := c.Pieces(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	e02First := files[e02].Offset / pm.PieceSize
	e02Last := (files[e02].Offset + files[e02].SizeBytes - 1) / pm.PieceSize
	for i := int(e02First); i <= int(e02Last); i++ {
		if pm.States[i] != 0 {
			t.Fatalf("deselected episode piece %d has state %d, want 0", i, pm.States[i])
		}
	}

	// Pause suspends transfers; Resume restores them.
	if err := c.Pause(t.Context(), hash); err != nil {
		t.Fatal(err)
	}
	if st, _ := c.Status(t.Context(), hash); !domain.IsPaused(st.State) {
		t.Fatalf("paused status = %q, want paused", st.State)
	}
	if err := c.Resume(t.Context(), hash); err != nil {
		t.Fatal(err)
	}

	// Eviction deletes the torrent's data dir.
	if err := c.Remove(t.Context(), hash, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.dataDir, hash)); !os.IsNotExist(err) {
		t.Fatalf("data dir must be deleted, got %v", err)
	}
	if _, err := c.Status(t.Context(), hash); !errors.Is(err, domain.ErrTorrentNotFound) {
		t.Fatalf("removed torrent must be gone, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adapters/nativetorrent/ -run TestProgressiveSwarm -v -timeout 120s`
Expected: FAIL (PrepareFiles/PrepareRange/Pause/Resume/Remove not implemented)

- [ ] **Step 3: Implement**

In `client.go`:

```go
// streamWindow is the per-torrent priority steer: a library Reader whose
// position and readahead elevate exactly the byte window playback or probing
// needs above the per-file baselines. It never serves bytes — the app reads
// the files from disk.
type streamWindow struct {
	r         torrent.Reader
	fileIndex int
}

// PrepareFile prepares a single media file plus subtitle sidecars.
func (c *Client) PrepareFile(ctx context.Context, hash string, index int, subtitleIndices []int) error {
	return c.PrepareFiles(ctx, hash, []int{index}, subtitleIndices)
}

// PrepareFiles queues exactly the wanted files (baseline priority) and
// un-queues everything else, then elevates the head window of the first
// selected media file so playback starts on the first pieces, mirroring
// qBittorrent's sequential + first/last-piece scheduling.
func (c *Client) PrepareFiles(_ context.Context, hash string, indices []int, subtitleIndices []int) error {
	t := c.torrent(hash)
	if t == nil {
		return domain.ErrTorrentNotFound
	}
	if t.Info() == nil {
		return errors.New("native engine torrent metadata not ready")
	}
	if len(indices) == 0 {
		return errors.New("no media files selected")
	}
	files := t.Files()
	for _, i := range indices {
		if i < 0 || i >= len(files) {
			return fmt.Errorf("selected file %d unavailable in torrent", i)
		}
	}
	want := map[int]bool{}
	for _, i := range indices {
		want[i] = true
	}
	for _, i := range subtitleIndices {
		want[i] = true
	}
	for i, f := range files {
		if want[i] {
			f.SetPriority(torrent.PiecePriorityNormal)
		} else {
			f.SetPriority(torrent.PiecePriorityNone)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session.setSelection(hash, indices, subtitleIndices)
	if _, ok := c.windows[hash]; !ok {
		if err := c.openWindowLocked(hash, files[indices[0]]); err != nil {
			return err
		}
	}
	return nil
}

// PrepareRange repositions the stream window onto the requested byte range.
// Called from waitReadablePath on every progressive media read, this is what
// makes deep seeks and tail probes (MKV cues, MP4 moov) arrive without
// waiting for the whole file.
func (c *Client) PrepareRange(_ context.Context, hash string, fileIndex int, start, count int64) error {
	if count <= 0 {
		return nil
	}
	t := c.torrent(hash)
	if t == nil {
		return domain.ErrTorrentNotFound
	}
	files := t.Files()
	if fileIndex < 0 || fileIndex >= len(files) {
		return fmt.Errorf("file %d unavailable in torrent", fileIndex)
	}
	f := files[fileIndex]
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.windows[hash]
	if w == nil || w.fileIndex != fileIndex {
		if w != nil {
			_ = w.r.Close()
		}
		w = &streamWindow{r: f.NewReader(), fileIndex: fileIndex}
		w.r.SetReadahead(c.cfg.Readahead)
		c.windows[hash] = w
	}
	pos := start - f.Offset()
	if pos < 0 {
		pos = 0
	}
	if _, err := w.r.Seek(pos, io.SeekStart); err != nil {
		return fmt.Errorf("seek stream window: %w", err)
	}
	return nil
}

func (c *Client) openWindowLocked(hash string, f *torrent.File) error {
	w := &streamWindow{r: f.NewReader(), fileIndex: -1}
	w.r.SetReadahead(c.cfg.StartWindow)
	if _, err := w.r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seed stream window: %w", err)
	}
	c.windows[hash] = w
	return nil
}

// Pause suspends data transfer without removing the torrent or its files.
func (c *Client) Pause(_ context.Context, hash string) error {
	t := c.torrent(hash)
	if t == nil {
		return domain.ErrTorrentNotFound
	}
	t.DisallowDataDownload()
	t.DisallowDataUpload()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused[hash] = true
	c.session.setPaused(hash, true)
	return nil
}

// Resume restores data transfer after Pause.
func (c *Client) Resume(_ context.Context, hash string) error {
	t := c.torrent(hash)
	if t == nil {
		return domain.ErrTorrentNotFound
	}
	t.AllowDataDownload()
	t.AllowDataUpload()
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.paused, hash)
	c.session.setPaused(hash, false)
	return nil
}

// Remove drops the torrent; with deleteFiles it also deletes the torrent's
// data directory and clears its persisted piece-completion rows so a later
// re-add never trusts stale completion bits for deleted bytes.
func (c *Client) Remove(_ context.Context, hash string, deleteFiles bool) error {
	t := c.torrent(hash)
	c.mu.Lock()
	if w := c.windows[hash]; w != nil {
		_ = w.r.Close()
		delete(c.windows, hash)
	}
	paused := c.paused[hash]
	delete(c.paused, hash)
	c.mu.Unlock()
	if t != nil {
		if deleteFiles {
			c.clearPieceCompletion(hash, t)
		}
		t.Drop()
	}
	c.session.delete(hash, paused)
	if deleteFiles {
		if err := os.RemoveAll(filepath.Join(c.dataDir, hash)); err != nil {
			return fmt.Errorf("delete torrent data: %w", err)
		}
	}
	return nil
}

func (c *Client) clearPieceCompletion(hash string, t *torrent.Torrent) {
	n := int(t.NumPieces())
	ih := t.InfoHash()
	for i := 0; i < n; i++ {
		_ = c.session.pc.Set(metainfo.PieceKey{InfoHash: ih, Index: i}, storage.Completion{Ok: true, Complete: false})
	}
}
```

Add `io` to imports if not already present.

- [ ] **Step 4: Run the full suite with race detection**

Run: `go test -race ./internal/adapters/nativetorrent/ -v -timeout 180s`
Expected: PASS (swarm test completes well under 60 s on localhost)

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/nativetorrent/
git commit -m "feat(nativetorrent): file selection, seek windows, pause/resume, eviction"
```

---

### Task 7: Session persistence and reload

**Files:**
- Create: `internal/adapters/nativetorrent/session.go`
- Modify: `internal/adapters/nativetorrent/client.go` (replace the Task 4 stub)
- Test: `internal/adapters/nativetorrent/session_test.go`

**Interfaces:**
- Consumes: `metainfo.PieceKey`, `storage.Completion` (held as `session.pc`).
- Produces:

```go
type sessionStore struct{ /* path, entries, mu, pc */ }
func newSessionStore(dir string, pc storage.PieceCompletion) *sessionStore // loads session.json if present; reuses the caller's bolt handle
func (s *sessionStore) putMeta(hash string, raw []byte) error
func (s *sessionStore) setSelection(hash string, media, subs []int) error
func (s *sessionStore) setPaused(hash string, paused bool) error
func (s *sessionStore) delete(hash string, paused bool) error // paused param satisfies the Pause/Resume signature flow; removal always drops the entry
func (s *sessionStore) lookup(hash string) (media, subs []int, paused bool, ok bool)
```

- [ ] **Step 1: Write the failing test**

```go
// internal/adapters/nativetorrent/session_test.go
package nativetorrent

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSessionRoundTripReloadsTorrents(t *testing.T) {
	root := seedContent(t)
	_, raw := buildTestMetainfo(t, root)

	dir := t.TempDir()
	c := newTestClientAt(t, dir)
	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	files, err := c.Files(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	e01 := files[0].Index
	if err := c.PrepareFiles(t.Context(), hash, []int{e01}, []int{2}); err != nil {
		t.Fatal(err)
	}
	if err := c.Pause(t.Context(), hash); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	// A fresh engine over the same session dir must re-add the torrent and
	// re-apply the selection without re-fetching anything.
	c2, err := New(Config{DataDir: c.dataDir, SessionDir: filepath.Join(dir, "session"), PeerPort: 0, Readahead: 1 << 20, StartWindow: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if got := len(c2.cl.Torrents()); got != 1 {
		t.Fatalf("reloaded engine must hold 1 torrent, got %d", got)
	}
	media, subs, paused, ok := c2.session.lookup(hash)
	if !ok || len(media) != 1 || media[0] != e01 || len(subs) != 1 || !paused {
		t.Fatalf("session entry lost or wrong: media=%v subs=%v paused=%v ok=%v", media, subs, paused, ok)
	}
}
```

Add the `newTestClientAt(t, dir)` helper (variant of `newTestClient` with `DataDir: filepath.Join(dir, "data")`, `SessionDir: filepath.Join(dir, "session")`) and refactor `newTestClient` to call it with `t.TempDir()`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/adapters/nativetorrent/ -run TestSessionRoundTrip -v`
Expected: FAIL (session store stub)

- [ ] **Step 3: Implement**

```go
// internal/adapters/nativetorrent/session.go
package nativetorrent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// sessionEntry is everything the engine needs to restore a torrent after a
// restart: its metainfo, the file selection, and the paused flag. Piece
// completion lives in the bolt db beside this file, so completed pieces are
// never re-verified.
type sessionEntry struct {
	Metainfo        []byte `json:"metainfo"`
	MediaIndices    []int  `json:"mediaIndices,omitempty"`
	SubtitleIndices []int  `json:"subtitleIndices,omitempty"`
	Paused          bool   `json:"paused,omitempty"`
}

type sessionStore struct {
	path    string
	pc      storage.PieceCompletion
	mu      sync.Mutex
	entries map[string]sessionEntry
}

func newSessionStore(dir string, pc storage.PieceCompletion) *sessionStore {
	s := &sessionStore{
		path:    filepath.Join(dir, "session.json"),
		pc:      pc,
		entries: map[string]sessionEntry{},
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(raw, &s.entries)
	}
	return s
}

func (s *sessionStore) save() error {
	raw, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *sessionStore) putMeta(hash string, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[hash]
	e.Metainfo = raw
	s.entries[hash] = e
	return s.save()
}

func (s *sessionStore) setSelection(hash string, media, subs []int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[hash]
	e.MediaIndices, e.SubtitleIndices = media, subs
	s.entries[hash] = e
	return s.save()
}

func (s *sessionStore) setPaused(hash string, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[hash]
	e.Paused = paused
	s.entries[hash] = e
	return s.save()
}

func (s *sessionStore) delete(hash string, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, hash)
	return s.save()
}

func (s *sessionStore) lookup(hash string) (media, subs []int, paused bool, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[hash]
	return e.MediaIndices, e.SubtitleIndices, e.Paused, ok
}
```

In `client.go`, add `loadSession` and call it from `New` (the Task 4 skeleton already calls it):

```go
// loadSession re-adds every persisted torrent and re-applies its selection
// and paused flag. Piece completion comes from the bolt db, so nothing is
// re-verified from disk.
func (c *Client) loadSession() error {
	for hash, entry := range c.session.entries {
		if len(entry.Metainfo) == 0 {
			continue
		}
		mi, err := metainfo.Load(bytes.NewReader(entry.Metainfo))
		if err != nil {
			return err
		}
		if c.torrent(hash) != nil {
			continue
		}
		t, err := c.cl.AddTorrent(mi)
		if err != nil {
			return err
		}
		if err := waitInfo(context.Background(), t); err != nil {
			return err
		}
		if len(entry.MediaIndices) > 0 {
			if err := c.PrepareFiles(context.Background(), hash, entry.MediaIndices, entry.SubtitleIndices); err != nil {
				return err
			}
		}
		if entry.Paused {
			if err := c.Pause(context.Background(), hash); err != nil {
				return err
			}
		}
	}
	return nil
}
```

`newSessionStore` takes the caller's bolt handle: `New` constructs `pc` once and passes it to both `newSessionStore(cfg.SessionDir, pc)` and `storage.NewFileClientOpts{PieceCompletion: pc}` — exactly one bolt db owns the session directory, so Tasks 4-6 and this task differ only in whether the store's methods persist.

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/adapters/nativetorrent/ -v -timeout 180s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapters/nativetorrent/
git commit -m "feat(nativetorrent): session persistence with bolt piece completion"
```

---

### Task 8: Free-space probe for darwin and windows

**Files:**
- Create: `internal/application/diskfree_darwin.go`
- Create: `internal/application/diskfree_windows.go`
- Modify: `internal/application/diskfree_other.go` (build tag)

**Interfaces:**
- Consumes: `freeDiskBytes(path string) (int64, error)` — existing contract.
- Produces: same function on every supported platform.

- [ ] **Step 1: Implement darwin**

```go
//go:build darwin

package application

import "syscall"

// freeDiskBytes reports the space available to unprivileged users on the
// volume holding path — the live signal behind the Reserve check.
func freeDiskBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
```

- [ ] **Step 2: Implement windows**

```go
//go:build windows

package application

import "golang.org/x/sys/windows"

// freeDiskBytes reports the space available to unprivileged users on the
// volume holding path — the live signal behind the Reserve check.
func freeDiskBytes(path string) (int64, error) {
	var free, total, avail uint64
	if err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(path), &free, &total, &avail); err != nil {
		return 0, err
	}
	return int64(avail), nil
}
```

Run `go get golang.org/x/sys` if the direct dependency is not yet recorded.

- [ ] **Step 3: Narrow the fallback's build tag**

`internal/application/diskfree_other.go` first line becomes:

```go
//go:build !linux && !darwin && !windows
```

(keep the warning implementation and its comment unchanged).

- [ ] **Step 4: Verify all six targets compile**

Run: `for os in linux darwin windows; do for arch in amd64 arm64; do CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build ./... || exit 1; done; done`
Expected: all six builds succeed.

- [ ] **Step 5: Commit**

```bash
git add internal/application/diskfree_darwin.go internal/application/diskfree_windows.go internal/application/diskfree_other.go go.mod go.sum
git commit -m "feat(app): reserve free-space probe on darwin and windows"
```

---

### Task 9: Settings for engine selection

**Files:**
- Modify: `internal/platform/config/config.go` (Settings struct ~line 24-40, defaults ~line 70-74, validate ~line 232)
- Modify: the settings-schema builder in `internal/adapters/httpapi` (grep `settingsSchema` / the `/settings/schema` handler; mirror the `qbittorrentUrl` entry)
- Test: `internal/platform/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Settings.DownloadEngine string` (`native`|`qbittorrent`, default `native`), `Settings.TorrentPeerPort int` (default `42069`), `Settings.TorrentSessionDir string` (default `data/torrent-session`); env `FILELIST_STREAMING_DOWNLOAD_ENGINE`, `FILELIST_STREAMING_TORRENT_PEER_PORT`, `FILELIST_STREAMING_TORRENT_SESSION_DIR` via the existing `EnvironmentPrefix` mapping.

- [ ] **Step 1: Write the failing test**

Append to `config_test.go`, following the file's existing validation-test pattern:

```go
func TestDownloadEngineValidation(t *testing.T) {
	base := DefaultSettings()
	base.DownloadEngine = "transmission"
	if err := (&Store{}).validate(base); err == nil {
		t.Fatal("unknown downloadEngine must fail validation")
	}
	for _, engine := range []string{"native", "qbittorrent"} {
		base.DownloadEngine = engine
		if err := (&Store{}).validate(base); err != nil {
			t.Fatalf("downloadEngine %q must be valid: %v", engine, err)
		}
	}
	base.DownloadEngine = "native"
	base.TorrentPeerPort = 70000
	if err := (&Store{}).validate(base); err == nil {
		t.Fatal("torrentPeerPort above 65535 must fail validation")
	}
	base.TorrentPeerPort = 42069
	base.TorrentSessionDir = "  "
	if err := (&Store{}).validate(base); err == nil {
		t.Fatal("blank torrentSessionDir must fail validation")
	}
}
```

(Adjust the constructor/validate receiver to match how existing tests in the file invoke validation.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/config/ -run TestDownloadEngineValidation -v`
Expected: FAIL (fields absent)

- [ ] **Step 3: Implement**

In `Settings` (after the QBittorrent block):

```go
	DownloadEngine    string `json:"downloadEngine"`
	TorrentPeerPort   int    `json:"torrentPeerPort"`
	TorrentSessionDir string `json:"torrentSessionDir"`
```

Defaults (alongside `DownloadRoot`):

```go
		DownloadEngine: "native", TorrentPeerPort: 42069, TorrentSessionDir: "data/torrent-session",
```

Validation additions:

```go
	switch v.DownloadEngine {
	case "native", "qbittorrent":
	default:
		return fmt.Errorf("downloadEngine must be native or qbittorrent")
	}
	if v.TorrentPeerPort < 0 || v.TorrentPeerPort > 65535 {
		return fmt.Errorf("torrentPeerPort must be between 0 and 65535")
	}
	if strings.TrimSpace(v.TorrentSessionDir) == "" {
		return fmt.Errorf("torrentSessionDir is required")
	}
```

Extend the settings-schema builder in `internal/adapters/httpapi` with the three fields, mirroring the existing `qbittorrentUrl` entry's shape (the schema endpoint advertises kinds, defaults, secret flags, and acquisition guidance — `torrentPeerPort` gets an acquisition note: "fixed port improves seeding reachability; publish it in compose to receive inbound peers").

- [ ] **Step 4: Run tests**

Run: `go test ./internal/platform/config/ ./internal/adapters/httpapi/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/platform/config/config.go internal/platform/config/config_test.go internal/adapters/httpapi/
git commit -m "feat(config): downloadEngine, torrentPeerPort, torrentSessionDir settings"
```

---

### Task 10: Composition wiring and engine lifecycle

**Files:**
- Modify: `internal/composition/container.go`

**Interfaces:**
- Consumes: `nativetorrent.New/Close`, `(*Service).SetEngineRoutePrefix`, `Settings.DownloadEngine/TorrentPeerPort/TorrentSessionDir/ReadAheadBytes/InitialBufferBytes`.
- Produces: `App` gains `Engine io.Closer` (nil for qBittorrent); `Close` releases engine then repository.

- [ ] **Step 1: Rewrite the engine wiring**

In `internal/composition/container.go`, replace the `qb := qbittorrent.New(...)` + `NewService` block:

```go
	var engine application.TorrentEngine
	var engineCloser io.Closer
	routePrefix := "qb:"
	switch current.DownloadEngine {
	case "", "native":
		nt, err := nativetorrent.New(nativetorrent.Config{
			DataDir:     current.DownloadRoot,
			SessionDir:  current.TorrentSessionDir,
			PeerPort:    current.TorrentPeerPort,
			Readahead:   current.ReadAheadBytes,
			StartWindow: current.InitialBufferBytes,
		})
		if err != nil {
			return nil, fmt.Errorf("native torrent engine: %w", err)
		}
		engine, engineCloser, routePrefix = nt, nt, "native:"
	case "qbittorrent":
		engine = qbittorrent.New(func() (string, string, string) {
			v := settings.Get()
			return v.QBittorrentURL, v.QBittorrentUsername, v.QBittorrentPassword
		})
	default:
		return nil, fmt.Errorf("unknown download engine %q", current.DownloadEngine)
	}
	service := application.NewService(fl, engine, repo, settings, subtitles.NewSubDL(settings))
	service.SetEngineRoutePrefix(routePrefix)
```

`App` gains the field `Engine io.Closer`, is constructed with it, and `Close` releases in dependency order (engine holds open bolt/file handles the repository does not need):

```go
func (a *App) Close() {
	if a.Engine != nil {
		_ = a.Engine.Close()
	}
	_ = a.Repository.Close()
}
```

Add imports `io`, `fmt`, and the `nativetorrent` adapter.

- [ ] **Step 2: Verify**

Run: `CGO_ENABLED=0 go build ./... && make check`
Expected: build clean; full suite green. Boot proof: `go run ./cmd/server` with a temp settings path starts, logs no engine error, and exits cleanly on SIGINT (the engine closes without hanging).

- [ ] **Step 3: Commit**

```bash
git add internal/composition/container.go
git commit -m "feat(composition): selectable torrent engine with lifecycle ownership"
```

---

### Task 11: Build matrix and compose profile

**Files:**
- Modify: `Makefile` (.PHONY line 1, new target after `build-arm64`)
- Modify: `compose.yml`

- [ ] **Step 1: Add build-all**

Add `build-all` to `.PHONY`, then after `build-arm64`:

```makefile
# Six-platform release binaries (windows/linux/darwin x amd64/arm64). The
# binary is cgo-free everywhere; the free-space probe carries per-OS builds.
build-all: 
	GOCACHE="$(GO_CACHE)" CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="$(GO_LDFLAGS)" -o bin/filelist-streaming-linux-amd64 ./cmd/server
	GOCACHE="$(GO_CACHE)" CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags="$(GO_LDFLAGS)" -o bin/filelist-streaming-linux-arm64 ./cmd/server
	GOCACHE="$(GO_CACHE)" CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags="$(GO_LDFLAGS)" -o bin/filelist-streaming-darwin-amd64 ./cmd/server
	GOCACHE="$(GO_CACHE)" CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags="$(GO_LDFLAGS)" -o bin/filelist-streaming-darwin-arm64 ./cmd/server
	GOCACHE="$(GO_CACHE)" CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(GO_LDFLAGS)" -o bin/filelist-streaming-windows-amd64.exe ./cmd/server
	GOCACHE="$(GO_CACHE)" CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="$(GO_LDFLAGS)" -o bin/filelist-streaming-windows-arm64.exe ./cmd/server
```

- [ ] **Step 2: Move the qBittorrent sidecar to a profile**

In `compose.yml`:

1. Under the `qbittorrent` service (after `restart:`), add:

```yaml
    profiles: ["qbittorrent"]
```

2. The `server` `depends_on` becomes optional so the native default boots without the sidecar:

```yaml
    depends_on:
      qbittorrent:
        condition: service_healthy
        required: false
```

3. Server environment additions:

```yaml
      FILELIST_STREAMING_DOWNLOAD_ENGINE: ${DOWNLOAD_ENGINE:-native}
      FILELIST_STREAMING_TORRENT_SESSION_DIR: /var/lib/filelist-streaming/data/torrent-session
      FILELIST_STREAMING_TORRENT_PEER_PORT: ${TORRENT_PEER_PORT:-42069}
```

4. Server port publishing gains the peer port (inbound peers reach the seeder; uTP uses UDP on the same port):

```yaml
      - "${TORRENT_BIND_IP:-0.0.0.0}:${TORRENT_PEER_PORT:-42069}:${TORRENT_PEER_PORT:-42069}/tcp"
      - "${TORRENT_BIND_IP:-0.0.0.0}:${TORRENT_PEER_PORT:-42069}:${TORRENT_PEER_PORT:-42069}/udp"
```

- [ ] **Step 3: Verify**

Run: `make build-all && ls bin/ && docker compose config -q && DOWNLOAD_ENGINE=qbittorrent docker compose --profile qbittorrent config -q`
Expected: six binaries in `bin/`; compose validates with and without the profile.

- [ ] **Step 4: Commit**

```bash
git add Makefile compose.yml
git commit -m "build: six-platform matrix and qbittorrent compose profile"
```

---

### Task 12: Domain docs and spec amendment

**Files:**
- Create: `docs/adr/0007-native-torrent-engine.md`
- Modify: `docs/adr/0005-qbittorrent-sidecar-without-auth.md` (scope note after the status block)
- Modify: `CONTEXT.md` (Playback section, after the Engine route entry)
- Modify: `docs/superpowers/specs/2026-09-02-native-torrent-engine-design.md` (two implementation-mechanism sentences)

- [ ] **Step 1: Write ADR-0007**

```markdown
# The native torrent engine is the default; qBittorrent is an optional engine

---
status: accepted
---

The server embeds a BitTorrent engine (anacrolix/torrent v1.61.0, pinned, MPL-2.0,
pure Go) implementing the same TorrentEngine port the qBittorrent adapter
implements. Settings select one active engine per deployment (`downloadEngine`:
`native` default, `qbittorrent` restores the sidecar stack); a download is
forever tied to its creating engine through its Engine route (`native:<hash>` /
`qb:<hash>`), and downloads belonging to the inactive engine surface as
unavailable. The native engine writes pieces in place under
`<DownloadRoot>/<infohash>/`, seeds until eviction, keeps its session (metainfo,
file selection, piece-completion bolt db) under `data/torrent-session`, and
elevates exactly the byte window a seek or probe needs (`PrepareRange`), which
qBittorrent cannot do and no-ops.

## Evidence

- cenkalti/rain was disqualified on its own source: no per-file selection, and
  every file preallocates — season-pack exclusion is impossible.
- Hand-rolling a BEP-3 client was rejected: protocol-edge stall risk for a
  dependency saving that is compile-time only.
- The dependency argument is operational: the native default removes the
  qBittorrent container entirely; go.mod weight is not runtime weight.

## Considered options

- **Both engines live simultaneously** — rejected: retention and allocation
  accounting across two engines buys nothing for a single household.
- **rain (cenkalti)** — rejected: no file selection.
- **cgo libtorrent bindings** — rejected: stale, and cgo breaks the
  six-platform matrix (windows/linux/darwin x amd64/arm64).

## Consequences

- The compose default is single-container; `--profile qbittorrent` restores the
  sidecar stack; external qBittorrent keeps serving bare-metal Pi deployments
  (ADR-0005 governs those).
- Anacrolix upgrades require checking its retract history; the pinned version
  is the stability boundary.
- Per-tracker seeder counts are unavailable from anacrolix v1.61.0's public
  API; native-mode downloads report tracker stats as zero.
```

- [ ] **Step 2: Scope-note ADR-0005 and add the CONTEXT.md term**

In `docs/adr/0005-qbittorrent-sidecar-without-auth.md`, directly under the status block, add:

```markdown
Scope note (0007): this ADR governs deployments whose active download engine is
qBittorrent — the compose `qbittorrent` profile and external-qBittorrent setups.
The native engine default needs neither the sidecar nor its no-auth WebUI.
```

In `CONTEXT.md`, after the **Engine route** entry in the Playback section, add:

```markdown
**Download engine**:
The torrent client the server drives for Managed downloads: the embedded native engine or an external qBittorrent. One engine is active per deployment; a download belongs to the engine that created it, through its Engine route.
_Avoid_: torrent client (unqualified), backend
```

- [ ] **Step 3: Amend the spec's mechanism sentences (implementation truth)**

In `docs/superpowers/specs/2026-09-02-native-torrent-engine-design.md`, in the Data flow section replace the PrepareFiles bullet with:

```markdown
- **PrepareFiles(indices, subtitleIndices):** wanted files get a baseline download priority (whole file queued, unwanted files never requested); a per-torrent library reader positioned by the app elevates the exact byte window above the baseline — head window at prepare, seek/probe windows on every `PrepareRange`. This delivers the same sequential-within-file and early head/tail scheduling semantics that qBittorrent's sequential + first/last-piece flags provide, which the buffering knobs (`StreamStartBytes`, `InitialBufferBytes`, `ReadAheadBytes`) were tuned against. (v1.61.0 exposes no public per-piece priority setter; the reader window is the library's intended steering mechanism.)
```

and the Pause/Resume bullet with:

```markdown
- **Pause/Resume:** maps to suspending/resuming data transfer (`DisallowDataDownload/Upload` and their allow counterparts — v1.61.0 has no per-torrent start/stop); the existing resume-on-playback logic applies.
```

- [ ] **Step 4: Commit**

```bash
git add docs/adr/0007-native-torrent-engine.md docs/adr/0005-qbittorrent-sidecar-without-auth.md CONTEXT.md docs/superpowers/specs/2026-09-02-native-torrent-engine-design.md
git commit -m "docs: adr 0007 native engine default, qbittorrent scope note, download engine term"
```

---

## Final verification (after Task 12)

- [ ] `make check` — full Go + python suite, vet, whitespace.
- [ ] `go test -race ./...`
- [ ] `make build-all` — six binaries.
- [ ] `DOWNLOAD_ENGINE=native docker compose config -q` and `--profile qbittorrent` variant.
- [ ] Real-world smoke (manual, per spec): `make docker-up` + `verify.sh`, add a real FileList release, progressive playback on browser and Tizen, seek deep into a file (PrepareRange path), evict and confirm files gone. Playback is the proof — do not ship on unit tests alone.
