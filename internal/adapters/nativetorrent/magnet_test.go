package nativetorrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// startMetadataSeeder starts a bare library client on loopback that serves
// ut_metadata for the synthetic metainfo. It never transfers payload bytes:
// the peer exists so the resolver can fetch metadata only. NoDHT keeps the
// magnet's x.pe as the only discovery path — no trackers, webseeds, or DHT
// nodes are present — so a resolve that returns the matching info hash and
// file manifest necessarily performed a real metadata transfer with this
// peer rather than synthesizing anything locally.
func startMetadataSeeder(t *testing.T, mi metainfo.MetaInfo, root string) (addr string) {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.NoDHT = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableIPv6 = true
	cfg.SetListenAddr("127.0.0.1:0")
	cfg.DataDir = filepath.Dir(root)
	cfg.Seed = true
	cfg.AlwaysWantConns = true
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	spec, err := torrent.TorrentSpecFromMetaInfoErr(&mi)
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := cl.AddTorrentSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-st.GotInfo():
	case <-time.After(5 * time.Second):
		t.Fatal("seeder metadata never became ready")
	}
	addrs := cl.ListenAddrs()
	if len(addrs) == 0 {
		t.Fatal("seeder has no listen address")
	}
	return addrs[0].String()
}

// magnetFor builds a source-free magnet: one v1 infohash and one direct
// peer. Every other discovery path stays disabled by the engine's config.
func magnetFor(ih metainfo.Hash, peer string) string {
	return fmt.Sprintf("magnet:?xt=urn:btih:%s&x.pe=%s", ih.HexString(), peer)
}

func TestResolveMagnetFetchesMetadataOverLoopback(t *testing.T) {
	root := seedContent(t)
	mi, _ := buildPublicTestMetainfo(t, root)
	ih := mi.HashInfoBytes()
	wantInfo, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatal(err)
	}
	addr := startMetadataSeeder(t, mi, root)
	c := newTestClient(t)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// No DownloadAll/PrepareFiles involved: resolution is metadata-only.
	raw, err := c.ResolveMagnet(ctx, magnetFor(ih, addr), t.TempDir(), domain.MagnetDiscoveryPublic)
	if err != nil {
		t.Fatal(err)
	}

	got, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("resolved bytes are not bencode metainfo: %v", err)
	}
	if got.HashInfoBytes() != ih {
		t.Fatalf("resolved info hash %s, want %s", got.HashInfoBytes(), ih)
	}
	gotInfo, err := got.UnmarshalInfo()
	if err != nil {
		t.Fatal(err)
	}
	if gotInfo.Name != wantInfo.Name {
		t.Errorf("resolved name %q, want %q", gotInfo.Name, wantInfo.Name)
	}
	wantFiles, gotFiles := wantInfo.UpvertedFiles(), gotInfo.UpvertedFiles()
	if len(gotFiles) != len(wantFiles) {
		t.Fatalf("resolved %d files, want %d", len(gotFiles), len(wantFiles))
	}
	for i := range wantFiles {
		if !slices.Equal(wantFiles[i].Path, gotFiles[i].Path) || wantFiles[i].Length != gotFiles[i].Length {
			t.Errorf("resolved file %d = %s (%d bytes), want %s (%d bytes)",
				i, gotFiles[i].Path, gotFiles[i].Length, wantFiles[i].Path, wantFiles[i].Length)
		}
	}

	// Metadata-only: the resolver owned nothing that survives, selected no
	// payload pieces, and wrote no payload storage.
	hashHex := ih.HexString()
	if live := c.torrent(hashHex); live != nil {
		t.Fatal("resolver-owned torrent survived the resolve")
	}
	c.mu.Lock()
	_, selected := c.selected[hashHex]
	_, windowed := c.windows[hashHex]
	dataDir := c.dataDir
	c.mu.Unlock()
	if selected {
		t.Error("resolve selected payload files")
	}
	if windowed {
		t.Error("resolve elevated a stream window")
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == hashHex {
			t.Errorf("resolve wrote payload storage at %s", filepath.Join(dataDir, hashHex))
		}
	}
}

func TestResolveMagnetCancellationLeavesNoTorrent(t *testing.T) {
	root := seedContent(t)
	mi, raw := buildPublicTestMetainfo(t, root)
	ih := mi.HashInfoBytes()

	// A peer that accepts TCP but never speaks: the handshake cannot
	// complete, so metadata can never arrive and cancellation must be the
	// only way out of the wait.
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
			go func(conn net.Conn) {
				<-ctx.Done()
				_ = conn.Close()
			}(conn)
		}
	}()

	c := newTestClient(t)
	type result struct{ err error }
	res := make(chan result, 1)
	go func() {
		_, err := c.ResolveMagnet(ctx, magnetFor(ih, ln.Addr().String()), t.TempDir(), domain.MagnetDiscoveryPublic)
		res <- result{err: err}
	}()
	// Let the resolve enter its bounded wait behind the silent peer.
	select {
	case r := <-res:
		t.Fatalf("ResolveMagnet returned before cancellation: %v", r.err)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	select {
	case r := <-res:
		if !errors.Is(r.err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ResolveMagnet ignored cancellation")
	}
	if live := c.torrent(ih.HexString()); live != nil {
		t.Fatal("resolver-owned torrent survived cancellation")
	}
	// The per-hash gate must be free again: a normal Add of the same hash
	// takes ownership and persists its session entry.
	if _, err := c.Add(t.Context(), bytes.NewReader(raw), t.TempDir()); err != nil {
		t.Fatalf("gate not released after cancellation: %v", err)
	}
}

func TestResolveMagnetPreservesExistingSessionTorrent(t *testing.T) {
	root := seedContent(t)
	// The private fixture (info.Private = true) is intentional: preexisting
	// managed torrents must be preserved and exported regardless of classification.
	mi, raw := buildTestMetainfo(t, root)
	ih := mi.HashInfoBytes()
	c := newTestClient(t)

	hash, err := c.Add(t.Context(), bytes.NewReader(raw), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareFiles(t.Context(), hash, []int{0}, []int{2}); err != nil {
		t.Fatal(err)
	}
	if err := c.Pause(t.Context(), hash); err != nil {
		t.Fatal(err)
	}
	before := c.torrent(hash)
	if before == nil {
		t.Fatal("added torrent is missing")
	}
	wantFilePrios := filePriorities(before)
	c.mu.Lock()
	wantSelected := slices.Clone(c.selected[hash])
	c.mu.Unlock()
	c.session.mu.Lock()
	wantEntry := c.session.entries[hash]
	c.session.mu.Unlock()

	// A same-hash magnet with an unusable peer: the existing torrent must be
	// found before any spec merge, so the peer is never dialed and nothing
	// about the session torrent changes.
	got, err := c.ResolveMagnet(t.Context(), magnetFor(ih, "127.0.0.1:1"), t.TempDir(), domain.MagnetDiscoveryPublic)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := metainfo.Load(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("exported bytes are not bencode metainfo: %v", err)
	}
	if parsed.HashInfoBytes() != ih {
		t.Fatalf("exported info hash %s, want %s", parsed.HashInfoBytes(), ih)
	}
	if after := c.torrent(hash); after != before {
		t.Fatal("resolve replaced the session torrent")
	}
	if gotFilePrios := filePriorities(before); !slices.Equal(wantFilePrios, gotFilePrios) {
		t.Fatalf("file priorities changed: want %v, got %v", wantFilePrios, gotFilePrios)
	}
	c.mu.Lock()
	gotSelected := slices.Clone(c.selected[hash])
	_, paused := c.paused[hash]
	c.mu.Unlock()
	if !slices.Equal(wantSelected, gotSelected) {
		t.Errorf("selection changed: want %v, got %v", wantSelected, gotSelected)
	}
	if !paused {
		t.Error("resolve unpaused the session torrent")
	}
	c.session.mu.Lock()
	gotEntry := c.session.entries[hash]
	c.session.mu.Unlock()
	if !bytes.Equal(wantEntry.Metainfo, gotEntry.Metainfo) ||
		!slices.Equal(wantEntry.MediaIndices, gotEntry.MediaIndices) ||
		!slices.Equal(wantEntry.SubtitleIndices, gotEntry.SubtitleIndices) ||
		wantEntry.Paused != gotEntry.Paused {
		t.Errorf("session entry changed: want %+v, got %+v", wantEntry, gotEntry)
	}
	if n := len(c.privateClient.Torrents()); n != 1 {
		t.Errorf("torrent count in private client = %d, want 1", n)
	}
}

// filePriorities snapshots the per-file piece priorities so a later resolve
// can be checked for leaving the selection untouched.
func filePriorities(t *torrent.Torrent) []torrent.PiecePriority {
	files := t.Files()
	prios := make([]torrent.PiecePriority, 0, len(files))
	for _, f := range files {
		prios = append(prios, f.Priority())
	}
	return prios
}

func TestResolveMagnetRejectsMissingV1Hash(t *testing.T) {
	c := newTestClient(t)
	_, err := c.ResolveMagnet(t.Context(), "magnet:?dn=missing-hash", t.TempDir(), domain.MagnetDiscoveryPublic)
	if err == nil {
		t.Fatal("accepted a magnet without a v1 info hash")
	}
}

func TestResolveMagnetRejectsV2OnlyMagnet(t *testing.T) {
	c := newTestClient(t)
	// A syntactically valid BEP 52 multihash (sha256, 32 zero bytes) with no
	// v1 hash: the library's AddTorrentOpt panics on the zero v1 hash, so
	// the resolver must reject it first.
	uri := "magnet:?xt=urn:btmh:1220" + strings.Repeat("0", 64)
	if _, err := c.ResolveMagnet(t.Context(), uri, t.TempDir(), domain.MagnetDiscoveryPublic); err == nil {
		t.Fatal("accepted a v2-only magnet")
	}
}

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
