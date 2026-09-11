package nativetorrent

import (
	"bytes"
	"os"
	"slices"
	"testing"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// udpTrackerMetainfo builds a small public metainfo carrying the dual-stack
// udp:// announce set a Pirate Bay magnet resolves to.
func udpTrackerMetainfo(t *testing.T) metainfo.MetaInfo {
	t.Helper()
	root := t.TempDir()
	file := root + "/sample.bin"
	if err := os.WriteFile(file, bytes.Repeat([]byte{0}, 16<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	var info metainfo.Info
	if err := info.BuildFromFilePath(file); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return metainfo.MetaInfo{
		Announce: "udp://tracker.opentrackr.org:1337",
		AnnounceList: [][]string{
			{"udp://tracker.opentrackr.org:1337", "udp://open.stealth.si:80/announce"},
			{"udp://exodus.desync.com:6969/announce"},
		},
		InfoBytes: infoBytes,
	}
}

func TestPreferUDP4(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"udp://tracker.opentrackr.org:1337", "udp4://tracker.opentrackr.org:1337"},
		{"udp://open.stealth.si:80/announce", "udp4://open.stealth.si:80/announce"},
		{"UDP://h:1", "udp4://h:1"},
		{"udp4://h:1", "udp4://h:1"},
		{"udp6://h:1", "udp6://h:1"},
		{"http://h/a", "http://h/a"},
		{"", ""},
	} {
		if got := preferUDP4(tc.in); got != tc.want {
			t.Errorf("preferUDP4(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestForceUDP4TrackersPreservesInfoHash(t *testing.T) {
	mi := udpTrackerMetainfo(t)
	beforeHash := mi.HashInfoBytes()
	beforeInfo := bytes.Clone(mi.InfoBytes)

	forceUDP4Trackers(&mi)

	if mi.HashInfoBytes() != beforeHash {
		t.Fatal("announce rewrite must not change the info hash")
	}
	if !bytes.Equal(mi.InfoBytes, beforeInfo) {
		t.Fatal("announce rewrite must not touch InfoBytes")
	}
	if mi.Announce != "udp4://tracker.opentrackr.org:1337" {
		t.Fatalf("Announce = %q", mi.Announce)
	}
	if !slices.Equal(mi.AnnounceList[0], []string{
		"udp4://tracker.opentrackr.org:1337", "udp4://open.stealth.si:80/announce",
	}) {
		t.Fatalf("AnnounceList tier 0 = %v", mi.AnnounceList[0])
	}

	spec, err := torrent.TorrentSpecFromMetaInfoErr(&mi)
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range spec.Trackers {
		for _, u := range tier {
			if len(u) < 7 || u[:7] != "udp4://" {
				t.Fatalf("spec tracker must carry the udp4 scheme, got %q", u)
			}
		}
	}

	// Idempotent: a second pass is a no-op.
	forceUDP4Trackers(&mi)
	if mi.Announce != "udp4://tracker.opentrackr.org:1337" {
		t.Fatalf("second pass must not touch a rewritten Announce, got %q", mi.Announce)
	}
	if !slices.Equal(mi.AnnounceList[0], []string{
		"udp4://tracker.opentrackr.org:1337", "udp4://open.stealth.si:80/announce",
	}) {
		t.Fatalf("second pass must not touch rewritten tiers, got %v", mi.AnnounceList[0])
	}
}

func TestAddRewritesAnnounceScheme(t *testing.T) {
	c := newTestClient(t)
	mi := udpTrackerMetainfo(t)
	raw, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := c.Add(t.Context(), bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}

	// The engine's live announce target is udp4.
	tt := c.torrent(hash)
	if tt == nil {
		t.Fatal("engine must hold the added torrent")
	}
	live := tt.Metainfo().AnnounceList
	if len(live) == 0 {
		t.Fatal("live torrent must carry the spec trackers")
	}
	for _, tier := range live {
		for _, u := range tier {
			if len(u) < 7 || u[:7] != "udp4://" {
				t.Fatalf("live tracker must carry the udp4 scheme, got %q", u)
			}
		}
	}

	// The persisted record keeps the original bytes, keyed by the unchanged hash.
	if got := string(c.session.entries[hash].Metainfo); got != string(raw) {
		t.Fatalf("putMeta must persist the original metainfo bytes")
	}

	// Reloading the persisted record through the loadSession path heals it.
	reloaded, err := metainfo.Load(bytes.NewReader(c.session.entries[hash].Metainfo))
	if err != nil {
		t.Fatal(err)
	}
	forceUDP4Trackers(reloaded)
	if reloaded.HashInfoBytes().HexString() != hash {
		t.Fatal("reload must key by the same info hash")
	}
	if reloaded.Announce != "udp4://tracker.opentrackr.org:1337" {
		t.Fatalf("reloaded Announce = %q", reloaded.Announce)
	}

	if _, err := c.Status(t.Context(), hash); err != nil {
		t.Fatal(err)
	}
}
