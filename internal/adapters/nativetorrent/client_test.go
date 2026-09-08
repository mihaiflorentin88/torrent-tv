package nativetorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// buildPublicTestMetainfo is identical to buildTestMetainfo but leaves the
// private bit unset, producing the metainfo a public-tracker download and
// authorized public magnet discovery would deliver.
func buildPublicTestMetainfo(t *testing.T, root string) (mi metainfo.MetaInfo, raw []byte) {
	t.Helper()
	var info metainfo.Info
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
	return seedContentFill(t, 'a', 'b')
}

// seedContentFill builds the same Pack layout as seedContent but with
// caller-chosen fill bytes, so distinct fixtures hash differently even
// though file names and sizes match.
func seedContentFill(t *testing.T, fillE01, fillE02 byte) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Pack.S01.1080p")
	if err := os.MkdirAll(filepath.Join(root, "Subs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Pack.S01E01.mkv"), bytes.Repeat([]byte{fillE01}, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Pack.S01E02.mkv"), bytes.Repeat([]byte{fillE02}, 4<<20), 0o644); err != nil {
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
	if got := len(c.privateClient.Torrents()); got != 1 {
		t.Fatalf("expected 1 torrent in private client, got %d", got)
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

func TestPrivateTorrentRoutedToPrivateClient(t *testing.T) {
	root := seedContent(t)
	_, raw := buildTestMetainfo(t, root) // private=true fixture
	c := newTestClient(t)
	hash, err := c.Add(context.Background(), bytes.NewReader(raw), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(c.publicClient.Torrents()); n != 0 {
		t.Fatalf("public client holds %d torrents, want 0", n)
	}
	if n := len(c.privateClient.Torrents()); n != 1 {
		t.Fatalf("private client holds %d torrents, want 1", n)
	}
	// Public-flag metainfo (absent private bit) routes public:
	_, publicRaw := buildPublicTestMetainfo(t, root)
	phash, err := c.Add(context.Background(), bytes.NewReader(publicRaw), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if phash == hash {
		t.Fatal("fixture collision")
	}
	if n := len(c.publicClient.Torrents()); n != 1 {
		t.Fatalf("public client holds %d torrents after public add, want 1", n)
	}
	// Full facade surface still resolves both:
	if _, err := c.Files(context.Background(), hash); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Files(context.Background(), phash); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAddAndResolveMagnetOneHash(t *testing.T) {
	root := seedContentFill(t, 'w', 'x')
	pubMI, pubRaw := buildPublicTestMetainfo(t, root)
	ih := pubMI.HashInfoBytes()

	addr := startMetadataSeeder(t, pubMI, root)
	c := newTestClient(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errAdd := make(chan error, 1)
	errResolve := make(chan error, 1)
	var resolvedBytes []byte
	var resolveMu sync.Mutex

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := c.Add(ctx, bytes.NewReader(pubRaw), t.TempDir())
		errAdd <- err
	}()
	go func() {
		defer wg.Done()
		raw, err := c.ResolveMagnet(ctx, magnetFor(ih, addr), t.TempDir(), domain.MagnetDiscoveryPublic)
		resolveMu.Lock()
		resolvedBytes = raw
		resolveMu.Unlock()
		errResolve <- err
	}()

	wg.Wait()

	if err := <-errAdd; err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := <-errResolve; err != nil {
		t.Fatalf("ResolveMagnet failed: %v", err)
	}

	resolveMu.Lock()
	gotMI, err := metainfo.Load(bytes.NewReader(resolvedBytes))
	resolveMu.Unlock()
	if err != nil {
		t.Fatalf("unmarshal resolved metainfo: %v", err)
	}
	if gotMI.HashInfoBytes() != ih {
		t.Fatalf("resolved infohash %s, want %s", gotMI.HashInfoBytes(), ih)
	}

	// Exactly one torrent on public client, none on private
	if n := len(c.publicClient.Torrents()); n != 1 {
		t.Fatalf("public client torrent count = %d, want 1", n)
	}
	if n := len(c.privateClient.Torrents()); n != 0 {
		t.Fatalf("private client torrent count = %d, want 0", n)
	}

	// Exactly one session write
	c.session.mu.Lock()
	entry, ok := c.session.entries[ih.HexString()]
	entriesCount := len(c.session.entries)
	c.session.mu.Unlock()
	if !ok || entriesCount != 1 {
		t.Fatalf("expected 1 session entry, got %d (ok=%v)", entriesCount, ok)
	}
	if len(entry.Metainfo) == 0 {
		t.Fatal("session entry metainfo is empty")
	}

	// Gate is released: a subsequent Add is idempotent and succeeds
	if _, err := c.Add(t.Context(), bytes.NewReader(pubRaw), t.TempDir()); err != nil {
		t.Fatalf("subsequent Add failed: %v", err)
	}
}

func TestResolveMagnetCancellationKeepsManagedTorrents(t *testing.T) {
	// 1. Managed torrent added and prepared first
	managedRoot := seedContentFill(t, 'm', 'n')
	managedMI, managedRaw := buildTestMetainfo(t, managedRoot)
	managedIH := managedMI.HashInfoBytes()
	managedHash := managedIH.HexString()

	c := newTestClient(t)
	if _, err := c.Add(t.Context(), bytes.NewReader(managedRaw), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareFiles(t.Context(), managedHash, []int{0}, []int{2}); err != nil {
		t.Fatal(err)
	}

	beforeTorrent := c.torrent(managedHash)
	if beforeTorrent == nil {
		t.Fatal("managed torrent missing")
	}
	wantFilePrios := filePriorities(beforeTorrent)
	c.mu.Lock()
	wantSelected := slices.Clone(c.selected[managedHash])
	c.mu.Unlock()

	// 2. Cancellation of a different hash's resolve mid-wait behind a silent peer
	cancelRoot := seedContentFill(t, 'c', 'k')
	cancelMI, cancelRaw := buildPublicTestMetainfo(t, cancelRoot)
	cancelIH := cancelMI.HashInfoBytes()
	cancelHash := cancelIH.HexString()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				<-ctx.Done()
				_ = c.Close()
			}(conn)
		}
	}()

	type result struct{ err error }
	res := make(chan result, 1)
	go func() {
		_, err := c.ResolveMagnet(ctx, magnetFor(cancelIH, ln.Addr().String()), t.TempDir(), domain.MagnetDiscoveryPublic)
		res <- result{err: err}
	}()

	select {
	case r := <-res:
		t.Fatalf("ResolveMagnet returned before cancel: %v", r.err)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()

	select {
	case r := <-res:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResolveMagnet did not return after context cancellation")
	}

	// 3. Assert transient dropped, no session entry, managed torrent untouched
	if live := c.torrent(cancelHash); live != nil {
		t.Fatal("transient torrent survived cancellation")
	}

	c.session.mu.Lock()
	_, cancelSessionExists := c.session.entries[cancelHash]
	managedEntry, managedSessionExists := c.session.entries[managedHash]
	sessionCount := len(c.session.entries)
	c.session.mu.Unlock()

	if cancelSessionExists {
		t.Fatal("session entry created for canceled resolve")
	}
	if !managedSessionExists || sessionCount != 1 {
		t.Fatalf("managed session entry lost or contaminated, count = %d", sessionCount)
	}
	if len(managedEntry.MediaIndices) != 1 || managedEntry.MediaIndices[0] != 0 {
		t.Fatalf("managed session media indices changed: %v", managedEntry.MediaIndices)
	}

	// Managed torrent untouched
	afterTorrent := c.torrent(managedHash)
	if afterTorrent != beforeTorrent {
		t.Fatal("managed torrent instance changed")
	}
	if gotPrios := filePriorities(beforeTorrent); !slices.Equal(wantFilePrios, gotPrios) {
		t.Fatalf("managed file priorities changed: want %v, got %v", wantFilePrios, gotPrios)
	}
	c.mu.Lock()
	gotSelected := slices.Clone(c.selected[managedHash])
	c.mu.Unlock()
	if !slices.Equal(wantSelected, gotSelected) {
		t.Fatalf("managed selection changed: want %v, got %v", wantSelected, gotSelected)
	}

	// Gate free: adding the canceled hash now works cleanly
	if _, err := c.Add(t.Context(), bytes.NewReader(cancelRaw), t.TempDir()); err != nil {
		t.Fatalf("Add after canceled resolve failed: %v", err)
	}
}

func TestRemoveAcrossClientsWithDeleteFiles(t *testing.T) {
	privRoot := seedContentFill(t, 'e', 'f')
	pubRoot := seedContentFill(t, 'g', 'h')
	_, privRaw := buildTestMetainfo(t, privRoot)
	pubMI, pubRaw := buildPublicTestMetainfo(t, pubRoot)
	pubIH := pubMI.HashInfoBytes()

	c := newTestClient(t)

	privHash, err := c.Add(t.Context(), bytes.NewReader(privRaw), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var privIH metainfo.Hash
	if err := privIH.FromHexString(privHash); err != nil {
		t.Fatal(err)
	}
	pubHash, err := c.Add(t.Context(), bytes.NewReader(pubRaw), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Set up selections, windows, and errors on BOTH torrents
	if err := c.PrepareFiles(t.Context(), privHash, []int{0}, []int{2}); err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareFiles(t.Context(), pubHash, []int{0}, []int{2}); err != nil {
		t.Fatal(err)
	}

	// PrepareRange on both to establish stream windows
	filesPriv, _ := c.Files(t.Context(), privHash)
	filesPub, _ := c.Files(t.Context(), pubHash)
	if err := c.PrepareRange(t.Context(), privHash, 0, filesPriv[0].Offset, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareRange(t.Context(), pubHash, 0, filesPub[0].Offset, 1<<20); err != nil {
		t.Fatal(err)
	}

	// Write simulated data files to disk in their data dirs
	privDir := filepath.Join(c.dataDir, privHash)
	pubDir := filepath.Join(c.dataDir, pubHash)
	if err := os.MkdirAll(privDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(privDir, "data.bin"), []byte("privdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pubDir, "data.bin"), []byte("pubdata"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Arm simulated write errors on both
	c.mu.Lock()
	privHook := c.writeErrHooks[privHash]
	pubHook := c.writeErrHooks[pubHash]
	c.mu.Unlock()
	if privHook == nil || pubHook == nil {
		t.Fatal("write-chunk hooks must be armed")
	}
	privHook(errors.New("priv disk err"))
	pubHook(errors.New("pub disk err"))

	// Verify both have status StateError and all bookkeeping populated
	stPriv, err := c.Status(t.Context(), privHash)
	if err != nil || stPriv.State != domain.StateError {
		t.Fatalf("priv status = %+v %v, want StateError", stPriv, err)
	}
	stPub, err := c.Status(t.Context(), pubHash)
	if err != nil || stPub.State != domain.StateError {
		t.Fatalf("pub status = %+v %v, want StateError", stPub, err)
	}

	// 1. Remove public torrent with deleteFiles = true
	if err := c.Remove(t.Context(), pubHash, true); err != nil {
		t.Fatalf("Remove public failed: %v", err)
	}

	// Public torrent data and bookkeeping cleared:
	if _, err := os.Stat(pubDir); !os.IsNotExist(err) {
		t.Fatalf("public data dir must be deleted, got %v", err)
	}
	if live := c.torrent(pubHash); live != nil {
		t.Fatal("public torrent must be dropped")
	}
	if _, ok := c.publicClient.Torrent(pubIH); ok {
		t.Fatal("public client still holds public torrent")
	}
	c.mu.Lock()
	_, pubWin := c.windows[pubHash]
	_, pubSel := c.selected[pubHash]
	_, pubErr := c.writeErrs[pubHash]
	_, pubHookExists := c.writeErrHooks[pubHash]
	_, pubOwner := c.owners[pubIH]
	// Private bookkeeping intact:
	_, privWin := c.windows[privHash]
	_, privSel := c.selected[privHash]
	_, privErr := c.writeErrs[privHash]
	_, privHookExists := c.writeErrHooks[privHash]
	_, privOwner := c.owners[privIH]
	c.mu.Unlock()

	if pubWin || pubSel || pubErr || pubHookExists || pubOwner {
		t.Fatal("public bookkeeping not cleared after Remove")
	}
	if !privWin || !privSel || !privErr || !privHookExists || !privOwner {
		t.Fatal("private bookkeeping corrupted by public Remove")
	}

	c.session.mu.Lock()
	_, pubSession := c.session.entries[pubHash]
	_, privSession := c.session.entries[privHash]
	c.session.mu.Unlock()
	if pubSession {
		t.Fatal("public session entry not deleted")
	}
	if !privSession {
		t.Fatal("private session entry deleted prematurely")
	}
	if _, err := os.Stat(privDir); err != nil {
		t.Fatalf("private data dir must still exist, got %v", err)
	}

	// 2. Remove private torrent with deleteFiles = true
	if err := c.Remove(t.Context(), privHash, true); err != nil {
		t.Fatalf("Remove private failed: %v", err)
	}

	if _, err := os.Stat(privDir); !os.IsNotExist(err) {
		t.Fatalf("private data dir must be deleted, got %v", err)
	}
	if live := c.torrent(privHash); live != nil {
		t.Fatal("private torrent must be dropped")
	}
	if _, ok := c.privateClient.Torrent(privIH); ok {
		t.Fatal("private client still holds private torrent")
	}
	c.mu.Lock()
	remWins := len(c.windows)
	remSels := len(c.selected)
	remErrs := len(c.writeErrs)
	remHooks := len(c.writeErrHooks)
	remOwners := len(c.owners)
	c.mu.Unlock()
	if remWins != 0 || remSels != 0 || remErrs != 0 || remHooks != 0 || remOwners != 0 {
		t.Fatalf("all bookkeeping must be empty, got wins=%d sels=%d errs=%d hooks=%d owners=%d",
			remWins, remSels, remErrs, remHooks, remOwners)
	}

	c.session.mu.Lock()
	remSessions := len(c.session.entries)
	c.session.mu.Unlock()
	if remSessions != 0 {
		t.Fatalf("session entries must be empty, got %d", remSessions)
	}

	// 3. Facade Close still owns pc exactly once without errors
	if err := c.Close(); err != nil {
		t.Fatalf("facade Close failed: %v", err)
	}
}

func TestProgressiveServeMidFileOnPartialPublicDownload(t *testing.T) {
	root := seedContentFill(t, 'p', 'q')
	pubMI, pubRaw := buildPublicTestMetainfo(t, root)

	seedCfg := torrent.TestingConfig(t)
	seedCfg.DataDir = filepath.Dir(root)
	seedCfg.Seed = true
	seedCfg.MaxAllocPeerRequestDataPerConn = 1 << 20
	seedCl, err := torrent.NewClient(seedCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer seedCl.Close()
	st, err := seedCl.AddTorrent(&pubMI)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.VerifyData(); err != nil {
		t.Fatal(err)
	}

	c := newTestClient(t)
	hash, err := c.Add(t.Context(), bytes.NewReader(pubRaw), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	files, err := c.Files(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatalf("expected at least 2 files, got %d", len(files))
	}

	// Select both files so the selected set is 8 MiB, leaving the torrent as
	// a whole in StateDownloading (partial) even after the 256 KiB mid-range finishes.
	e01, e02 := files[0].Index, files[1].Index
	if err := c.PrepareFiles(t.Context(), hash, []int{e01, e02}, nil); err != nil {
		t.Fatal(err)
	}

	// Elevate a 256 KiB mid-file range within file 0
	midOffset := files[0].Offset + 1<<20 // 1 MiB into the file
	midCount := int64(256 << 10)         // 256 KiB
	if err := c.PrepareRange(t.Context(), hash, e01, midOffset, midCount); err != nil {
		t.Fatal(err)
	}

	// Connect peer to download
	nt := c.torrent(hash)
	if nt == nil {
		t.Fatal("torrent missing from client")
	}
	if n := nt.AddClientPeer(seedCl); n == 0 {
		t.Fatal("peer not added")
	}

	pm, err := c.Pieces(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	pieceSize := pm.PieceSize
	firstPiece := int(midOffset / pieceSize)
	lastPiece := int((midOffset + midCount - 1) / pieceSize)

	// Wait for the pieces covering the mid-file range to complete
	waitForPieceStates(t, c, hash, firstPiece, lastPiece, 2, 60*time.Second)

	// Verify the torrent as a whole is partial (deselected E02 never downloaded)
	status, err := c.Status(t.Context(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if status.DownloadedBytes >= status.TotalBytes {
		t.Fatalf("torrent completed fully (%d / %d bytes)", status.DownloadedBytes, status.TotalBytes)
	}
	if status.State == domain.StateSeeding {
		t.Fatal("torrent must not be seeding before full completion")
	}

	// Direct PrepareRange + file read: read the verified range from disk
	mediaPath := filepath.Join(c.dataDir, hash, files[e01].Path)
	f, err := os.Open(mediaPath)
	if err != nil {
		t.Fatalf("open media file at %s: %v", mediaPath, err)
	}
	defer f.Close()

	directBytes := make([]byte, midCount)
	if _, err := f.ReadAt(directBytes, 1<<20); err != nil {
		t.Fatalf("read direct bytes from file: %v", err)
	}
	wantBytes := bytes.Repeat([]byte{'p'}, int(midCount))
	if !bytes.Equal(directBytes, wantBytes) {
		t.Fatalf("direct file read bytes mismatch")
	}

	// Real HTTP 206 range path: request the mid-file range
	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", 1<<20, (1<<20)+midCount-1))
	rec := httptest.NewRecorder()
	http.ServeContent(rec, req, filepath.Base(mediaPath), time.Now(), f)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("HTTP status = %d, want 206 Partial Content", rec.Code)
	}
	if rec.Body.Len() != int(midCount) {
		t.Fatalf("HTTP response body length = %d, want %d", rec.Body.Len(), midCount)
	}
	if !bytes.Equal(rec.Body.Bytes(), wantBytes) {
		t.Fatalf("HTTP served bytes mismatch")
	}
}
