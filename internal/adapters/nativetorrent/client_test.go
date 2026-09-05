package nativetorrent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// buildTestMetainfo builds a real multi-file metainfo from files on disk and
// returns the parsed MetaInfo plus the raw bencode bytes a FileList download
// would deliver.
func buildTestMetainfo(t *testing.T, root string) (mi metainfo.MetaInfo, raw []byte) {
	t.Helper()
	var info metainfo.Info
	private := true
	info.Private = &private
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

func newTestClientAt(t *testing.T, dir string) *Client {
	t.Helper()
	c, err := New(Config{
		DataDir:     filepath.Join(dir, "data"),
		SessionDir:  filepath.Join(dir, "session"),
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

func newTestClient(t *testing.T) *Client {
	return newTestClientAt(t, t.TempDir())
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
	// The enabled initial piece check transiently reports pieces as
	// queued-for-hash; wait for the marking pass to settle before asserting
	// a fresh torrent is all-missing.
	settle := time.Now().Add(10 * time.Second)
	for {
		pm, err := c.Pieces(t.Context(), hash)
		if err != nil {
			t.Fatal(err)
		}
		if pm.PieceSize <= 0 || len(pm.States) == 0 {
			t.Fatalf("expected piece map, got %+v", pm)
		}
		missing := true
		for _, s := range pm.States {
			if s != 0 {
				missing = false
				break
			}
		}
		if missing {
			break
		}
		if time.Now().After(settle) {
			t.Fatalf("fresh torrent must have all pieces missing, got %v", pm.States)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := c.Status(t.Context(), "ffffffffffffffffffffffffffffffffffffffff"); !errors.Is(err, domain.ErrTorrentNotFound) {
		t.Fatalf("unknown hash must map to ErrTorrentNotFound, got %v", err)
	}
}

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
	seedCfg.DataDir = filepath.Dir(root)
	seedCfg.Seed = true
	// TestingConfig caps per-connection request allocation at 5 bytes; the
	// seeder must accept at least one request chunk.
	seedCfg.MaxAllocPeerRequestDataPerConn = 1 << 20
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
	// Let the initial piece-check marking pass settle so the deselected-file
	// assertion below observes scheduling decisions, not the transient hash
	// queue that Add runs on every piece.
	all, err := c.Pieces(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	waitForPieceStates(t, c, hash, 0, len(all.States)-1, 0, 30*time.Second)
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
	first := files[e01].Offset / pieceSize
	last := (files[e01].Offset + e01Len - 1) / pieceSize
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

	// Completed path: the waited-for E01 range is fully downloaded, so the
	// selected set is done and the torrent is seeding even though the
	// deselected episode never arrived; pausing parks it in pausedUP.
	for i := int(first); i <= int(last); i++ {
		if pm.States[i] != 2 {
			t.Fatalf("E01 piece %d has state %d, want 2", i, pm.States[i])
		}
	}
	if st, err := c.Status(t.Context(), hash); err != nil || st.State != domain.StateSeeding {
		t.Fatalf("completed-selection status = %+v %v, want state %q", st, err, domain.StateSeeding)
	}
	if err := c.Pause(t.Context(), hash); err != nil {
		t.Fatal(err)
	}
	if st, err := c.Status(t.Context(), hash); err != nil || st.State != domain.StatePausedUP || !domain.IsPaused(st.State) {
		t.Fatalf("paused completed status = %+v %v, want state %q", st, err, domain.StatePausedUP)
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

func TestWriteChunkErrorSurfacesAndClears(t *testing.T) {
	root := seedContent(t)
	_, raw := buildTestMetainfo(t, root)
	c := newTestClient(t)
	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	if st, err := c.Status(t.Context(), hash); err != nil || st.State != domain.StateDownloading {
		t.Fatalf("status = %+v %v, want %q", st, err, domain.StateDownloading)
	}
	// The engine arms a write-chunk callback on every torrent entering the
	// client; fire the stored hook to simulate the library reporting a
	// storage failure.
	c.mu.Lock()
	hook := c.writeErrHooks[hash]
	c.mu.Unlock()
	if hook == nil {
		t.Fatal("write-chunk hook must be armed at add")
	}
	hook(errors.New("disk full"))
	if st, err := c.Status(t.Context(), hash); err != nil || st.State != domain.StateError {
		t.Fatalf("status after write-chunk failure = %+v %v, want %q", st, err, domain.StateError)
	}
	// Resume is a user-initiated fresh start and clears the error state.
	if err := c.Resume(t.Context(), hash); err != nil {
		t.Fatal(err)
	}
	if st, err := c.Status(t.Context(), hash); err != nil || st.State != domain.StateDownloading {
		t.Fatalf("status after resume = %+v %v, want %q", st, err, domain.StateDownloading)
	}
	// Remove clears the bookkeeping along with the torrent.
	hook(errors.New("disk full again"))
	if err := c.Remove(t.Context(), hash, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(t.Context(), hash); !errors.Is(err, domain.ErrTorrentNotFound) {
		t.Fatalf("removed torrent must be gone, got %v", err)
	}
	c.mu.Lock()
	errs, hooks := len(c.writeErrs), len(c.writeErrHooks)
	c.mu.Unlock()
	if errs != 0 || hooks != 0 {
		t.Fatalf("Remove must clear error bookkeeping: %d errors, %d hooks", errs, hooks)
	}
}
