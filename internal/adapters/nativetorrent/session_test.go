package nativetorrent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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
