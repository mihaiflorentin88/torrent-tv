package nativetorrent

import (
	"bytes"
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
	if got := len(c2.privateClient.Torrents()); got != 1 {
		t.Fatalf("reloaded engine must hold 1 torrent, got %d", got)
	}
	media, subs, paused, ok := c2.session.lookup(hash)
	if !ok || len(media) != 1 || media[0] != e01 || len(subs) != 1 || !paused {
		t.Fatalf("session entry lost or wrong: media=%v subs=%v paused=%v ok=%v", media, subs, paused, ok)
	}
	c2.mu.Lock()
	hook := c2.writeErrHooks[hash]
	c2.mu.Unlock()
	if hook == nil {
		t.Fatal("reloaded torrent must have the write-chunk hook armed")
	}
	if !ok || len(media) != 1 || media[0] != e01 || len(subs) != 1 || !paused {
		t.Fatalf("session entry lost or wrong: media=%v subs=%v paused=%v ok=%v", media, subs, paused, ok)
	}
}

func TestSessionPersistFailuresPropagate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; the unwritable-dir negative test cannot fail")
	}
	root := seedContent(t)
	_, raw := buildTestMetainfo(t, root)
	dir := t.TempDir()
	c := newTestClientAt(t, dir)
	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(dir, "session")
	if err := os.Chmod(sessionDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sessionDir, 0o700) })

	// Every session write must surface with context instead of vanishing.
	if err := c.PrepareFiles(t.Context(), hash, []int{0}, nil); err == nil || !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("PrepareFiles error = %v, want persist session failure", err)
	}
	if err := c.Pause(t.Context(), hash); err == nil || !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("Pause error = %v, want persist session failure", err)
	}
	if err := c.Resume(t.Context(), hash); err == nil || !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("Resume error = %v, want persist session failure", err)
	}

	// Remove surfaces the bookkeeping failure and still completes cleanup.
	if err := c.Remove(t.Context(), hash, true); err == nil || !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("Remove error = %v, want persist session failure", err)
	}
	if _, err := os.Stat(filepath.Join(c.dataDir, hash)); !os.IsNotExist(err) {
		t.Fatalf("data dir must be deleted despite the session failure, got %v", err)
	}
	if _, ok := c.session.entries[hash]; ok {
		t.Fatal("session entry must be dropped despite the failed save")
	}
}

func TestMixedRestoreAcrossTwoFacades(t *testing.T) {
	privRoot := seedContentFill(t, 'p', 'q')
	pubRoot := seedContentFill(t, 'u', 'v')
	_, privRaw := buildTestMetainfo(t, privRoot)
	pubMI, pubRaw := buildPublicTestMetainfo(t, pubRoot)
	pubIH := pubMI.HashInfoBytes()

	// Seed client provides the public pieces for Facade A
	seedCfg := torrent.TestingConfig(t)
	seedCfg.DataDir = filepath.Dir(pubRoot)
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

	dir := t.TempDir()
	a := newTestClientAt(t, dir)

	privHash, err := a.Add(t.Context(), bytes.NewReader(privRaw), "")
	if err != nil {
		t.Fatal(err)
	}
	pubHash, err := a.Add(t.Context(), bytes.NewReader(pubRaw), "")
	if err != nil {
		t.Fatal(err)
	}
	if pubHash == privHash {
		t.Fatal("hashes must differ")
	}

	// Prepare public selection (e01 and subtitle) and download via loopback peer
	filesPub, err := a.Files(t.Context(), pubHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PrepareFiles(t.Context(), pubHash, []int{0}, []int{2}); err != nil {
		t.Fatal(err)
	}
	ntPub := a.torrent(pubHash)
	if ntPub == nil {
		t.Fatal("public torrent missing from facade A")
	}
	if n := ntPub.AddClientPeer(seedCl); n == 0 {
		t.Fatal("failed to add loopback seed peer")
	}

	e01Len := filesPub[0].SizeBytes
	pm, err := a.Pieces(t.Context(), pubHash)
	if err != nil {
		t.Fatal(err)
	}
	lastPiece := (filesPub[0].Offset + e01Len - 1) / pm.PieceSize
	waitForPieceStates(t, a, pubHash, 0, int(lastPiece), 2, 60*time.Second)

	// Prepare private selection and pause it
	if err := a.PrepareFiles(t.Context(), privHash, []int{0}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Pause(t.Context(), privHash); err != nil {
		t.Fatal(err)
	}

	// Close facade A and the seeder completely
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_ = seedCl.Close()

	// Construct SECOND facade over the same directories
	b, err := New(Config{
		DataDir:     a.dataDir,
		SessionDir:  filepath.Join(dir, "session"),
		PeerPort:    0,
		Readahead:   1 << 20,
		StartWindow: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// 1. Assert each hash landed on the same classified client
	var privIH metainfo.Hash
	if err := privIH.FromHexString(privHash); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.privateClient.Torrent(privIH); !ok {
		t.Fatal("private torrent did not land on privateClient")
	}
	if _, ok := b.publicClient.Torrent(privIH); ok {
		t.Fatal("private torrent must not be on publicClient")
	}

	if _, ok := b.publicClient.Torrent(pubIH); !ok {
		t.Fatal("public torrent did not land on publicClient")
	}
	if _, ok := b.privateClient.Torrent(pubIH); ok {
		t.Fatal("public torrent must not be on privateClient")
	}
	if n := len(b.privateClient.Torrents()); n != 1 {
		t.Fatalf("privateClient torrent count = %d, want 1", n)
	}
	if n := len(b.publicClient.Torrents()); n != 1 {
		t.Fatalf("publicClient torrent count = %d, want 1", n)
	}

	// 2. Selections and paused state re-applied
	mediaPub, subsPub, pausedPub, okPub := b.session.lookup(pubHash)
	if !okPub || len(mediaPub) != 1 || mediaPub[0] != 0 || len(subsPub) != 1 || subsPub[0] != 2 || pausedPub {
		t.Fatalf("public selection wrong: media=%v subs=%v paused=%v ok=%v", mediaPub, subsPub, pausedPub, okPub)
	}

	mediaPriv, _, pausedPriv, okPriv := b.session.lookup(privHash)
	if !okPriv || len(mediaPriv) != 1 || mediaPriv[0] != 0 || !pausedPriv {
		t.Fatalf("private selection wrong: media=%v paused=%v ok=%v", mediaPriv, pausedPriv, okPriv)
	}

	// Applied state, not just the session echo: facade B must have actually
	// re-run PrepareFiles — the restored selection shows up as real piece
	// priorities on the live torrent (E01 and the subtitle selectable, the
	// deselected episode left at None) and in the applied-selection mirror.
	pubTorrent := b.torrent(pubHash)
	if pubTorrent == nil {
		t.Fatal("public torrent missing on facade B")
	}
	gotPrios := filePriorities(pubTorrent)
	if len(gotPrios) != 3 {
		t.Fatalf("expected 3 files on restored torrent, got %d", len(gotPrios))
	}
	if gotPrios[0] == torrent.PiecePriorityNone {
		t.Fatal("restored E01 selection was not applied: file priority is None")
	}
	if gotPrios[1] != torrent.PiecePriorityNone {
		t.Fatalf("deselected E02 must stay at None, got %v", gotPrios[1])
	}
	if gotPrios[2] == torrent.PiecePriorityNone {
		t.Fatal("restored subtitle selection was not applied: file priority is None")
	}
	b.mu.Lock()
	appliedSel := slices.Clone(b.selected[pubHash])
	b.mu.Unlock()
	if len(appliedSel) != 2 || (appliedSel[0] != 0 && appliedSel[1] != 0) || (appliedSel[0] != 2 && appliedSel[1] != 2) {
		t.Fatalf("applied selection = %v, want [0 2]", appliedSel)
	}
	stPriv, err := b.Status(t.Context(), privHash)
	if err != nil {
		t.Fatal(err)
	}
	if stPriv.State != domain.StatePausedDL {
		t.Fatalf("private status state = %q, want %q", stPriv.State, domain.StatePausedDL)
	}

	// 3. Piece-completion bytes reused (verified pieces stay complete with NO peers)
	// Bounded wait without seeder: pieces must be immediately complete from bolt piece-completion
	waitForPieceStates(t, b, pubHash, 0, int(lastPiece), 2, 5*time.Second)
	stPub, err := b.Status(t.Context(), pubHash)
	if err != nil {
		t.Fatal(err)
	}
	if stPub.DownloadedBytes < e01Len {
		t.Fatalf("downloaded bytes = %d, want >= %d", stPub.DownloadedBytes, e01Len)
	}
}
