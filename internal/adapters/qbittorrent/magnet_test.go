package qbittorrent

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// Fixed bencode fixture: the same info dictionary the existing client tests
// use, so the info hash is a known constant.
const (
	magnetTestInfoDict = "d6:lengthi5e4:name9:video.mp412:piece lengthi4e6:pieces20:aaaaaaaaaaaaaaaaaaaaee"
	magnetTestHash     = "52e0ec3afc6723a6be6a2dad955dc4027babc55c"
)

var magnetTestMetainfo = []byte("d8:announce13:https://test/4:info" + magnetTestInfoDict + "e")

func magnetTestURI() string {
	return "magnet:?xt=urn:btih:" + magnetTestHash + "&dn=video.mp4&tr=" + url.QueryEscape("https://tracker.example/announce")
}

func magnetMismatchMetainfo(t *testing.T) ([]byte, string) {
	t.Helper()
	raw := []byte("d8:announce13:https://test/4:info" +
		"d6:lengthi5e4:name10:video2.mp412:piece lengthi4e6:pieces20:aaaaaaaaaaaaaaaaaaaaee" + "e")
	h, err := infoHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, h
}

// fakeTorrent is the stateful daemon-side torrent record.
type fakeTorrent struct {
	hash        string
	name        string
	state       string
	savePath    string
	category    string
	tags        []string
	files       []map[string]any
	stopped     bool
	deleteFiles bool
}

// resolveServer is a stateful qBittorrent WebUI daemon for resolver tests.
type resolveServer struct {
	*httptest.Server
	t *testing.T

	mu            sync.Mutex
	version       string
	ready         bool
	torrents      map[string]*fakeTorrent
	removed       map[string]fakeTorrent
	events        []string
	addCount      int
	exportCalls   map[string]int
	hadFiles      bool
	pausedEarly   bool
	dupMode       string // "", "fails" (old daemon), "conflict" (4.4+)
	onAdd         func(s *resolveServer, form url.Values)
	exportHook    func(hash string, call int) (int, []byte)
	lastAddForm   url.Values
	metainfoBytes []byte
}

func newResolveServer(t *testing.T, version string) *resolveServer {
	t.Helper()
	s := &resolveServer{
		t:             t,
		version:       version,
		torrents:      map[string]*fakeTorrent{},
		removed:       map[string]fakeTorrent{},
		exportCalls:   map[string]int{},
		metainfoBytes: magnetTestMetainfo,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/app/version", func(w http.ResponseWriter, r *http.Request) {
		s.record("version")
		fmt.Fprint(w, s.version)
	})
	mux.HandleFunc("/api/v2/torrents/add", s.handleAdd)
	for _, name := range []string{"pause", "stop"} {
		mux.HandleFunc("/api/v2/torrents/"+name, func(w http.ResponseWriter, r *http.Request) {
			s.record(name)
			s.stopTorrents(r)
		})
	}
	for _, name := range []string{"resume", "start"} {
		mux.HandleFunc("/api/v2/torrents/"+name, func(w http.ResponseWriter, r *http.Request) {
			s.record(name)
		})
	}
	mux.HandleFunc("/api/v2/torrents/delete", s.handleDelete)
	mux.HandleFunc("/api/v2/torrents/info", s.handleInfo)
	mux.HandleFunc("/api/v2/torrents/files", s.handleFiles)
	mux.HandleFunc("/api/v2/torrents/export", s.handleExport)
	// Mutation endpoints the resolver must never use on torrents it does not
	// own; recording them makes "no mutation" an observable assertion.
	for _, name := range []string{"setCategory", "setTags", "setTrackers"} {
		mux.HandleFunc("/api/v2/torrents/"+name, func(w http.ResponseWriter, r *http.Request) {
			s.record(name)
		})
	}
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Server.Close)
	return s
}

func (s *resolveServer) record(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, name)
}

func (s *resolveServer) setReady(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = v
}

func (s *resolveServer) snapshotEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *resolveServer) countEvent(name string) int {
	n := 0
	for _, e := range s.snapshotEvents() {
		if e == name {
			n++
		}
	}
	return n
}

func (s *resolveServer) addCountValue() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addCount
}

func (s *resolveServer) torrentSnapshot(hash string) (fakeTorrent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.torrents[hash]
	if !ok {
		return fakeTorrent{}, false
	}
	cp := *cur
	cp.tags = append([]string(nil), cur.tags...)
	return cp, true
}

func (s *resolveServer) removedSnapshot(hash string) (fakeTorrent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	got, ok := s.removed[hash]
	return got, ok
}

func (s *resolveServer) pausedBeforeFiles() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pausedEarly
}

func (s *resolveServer) lastAdd() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAddForm
}

func (s *resolveServer) newClient() *Client {
	return New(func() (string, string, string) { return s.URL, "", "" })
}

func cloneValues(v url.Values) url.Values {
	if v == nil {
		return nil
	}
	cp := make(url.Values, len(v))
	for k, slice := range v {
		cp[k] = append([]string(nil), slice...)
	}
	return cp
}

func (s *resolveServer) handleAdd(w http.ResponseWriter, r *http.Request) {
	s.record("add")
	s.mu.Lock()
	s.addCount++
	s.mu.Unlock()

	var hash string
	if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		_ = r.ParseMultipartForm(32 << 20)
		file, _, err := r.FormFile("torrents")
		if err == nil {
			defer file.Close()
			b, _ := io.ReadAll(file)
			hash, _ = infoHash(b)
		}
	} else {
		_ = r.ParseForm()
		s.mu.Lock()
		s.lastAddForm = cloneValues(r.Form)
		s.mu.Unlock()
		raw := r.Form.Get("urls")
		hash, _ = magnetInfoHash(raw)
	}
	if hash == "" {
		fmt.Fprint(w, "Fails.")
		return
	}
	s.mu.Lock()
	_, exists := s.torrents[hash]
	dup := s.dupMode
	existing := exists
	s.mu.Unlock()
	if existing {
		switch dup {
		case "conflict":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, "Torrent is already downloaded.")
		case "fails":
			fmt.Fprint(w, "Fails.")
		default:
			fmt.Fprint(w, "Ok.")
		}
		return
	}
	if s.onAdd != nil {
		s.onAdd(s, r.Form)
	}
	s.mu.Lock()
	if _, exists := s.torrents[hash]; !exists {
		s.torrents[hash] = &fakeTorrent{
			hash:     hash,
			name:     "video.mp4",
			state:    "stoppedUP", // stopCondition=MetadataReceived halts the torrent
			savePath: r.Form.Get("savepath"),
			category: r.Form.Get("category"),
			tags:     strings.Split(r.Form.Get("tags"), ","),
			files:    []map[string]any{{"name": "video.mp4", "size": 5, "progress": 1, "priority": 0, "index": 0}},
		}
	}
	s.mu.Unlock()
	fmt.Fprint(w, "Ok.")
}

func (s *resolveServer) stopTorrents(r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hadFiles {
		s.pausedEarly = true
	}
	for _, h := range strings.Split(r.Form.Get("hashes"), ",") {
		if cur, ok := s.torrents[h]; ok {
			cur.stopped = true
			cur.state = "stoppedUP"
		}
	}
}

func (s *resolveServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	s.record("delete")
	_ = r.ParseForm()
	deleteFiles := r.Form.Get("deleteFiles") == "true"
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range strings.Split(r.Form.Get("hashes"), ",") {
		if cur, ok := s.torrents[h]; ok {
			cur.deleteFiles = deleteFiles
			cur.stopped = true
			s.removed[h] = *cur
			delete(s.torrents, h)
		}
	}
}

func (s *resolveServer) handleInfo(w http.ResponseWriter, r *http.Request) {
	s.record("info")
	hash := r.URL.Query().Get("hash")
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []map[string]any{}
	for _, cur := range s.torrents {
		if hash != "" && cur.hash != hash {
			continue
		}
		rows = append(rows, map[string]any{
			"hash":      cur.hash,
			"name":      cur.name,
			"state":     cur.state,
			"save_path": cur.savePath,
			"category":  cur.category,
			"tags":      cur.tags,
			"progress":  1,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

func (s *resolveServer) handleFiles(w http.ResponseWriter, r *http.Request) {
	s.record("files")
	hash := r.URL.Query().Get("hash")
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []map[string]any{}
	if cur, ok := s.torrents[hash]; ok && s.ready {
		rows = cur.files
	}
	if len(rows) > 0 {
		s.hadFiles = true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

func (s *resolveServer) handleExport(w http.ResponseWriter, r *http.Request) {
	s.record("export")
	hash := r.URL.Query().Get("hash")
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.torrents[hash]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	call := s.exportCalls[hash] + 1
	s.exportCalls[hash] = call
	if s.exportHook != nil {
		status, body := s.exportHook(hash, call)
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	if !s.ready {
		w.WriteHeader(http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	_, _ = w.Write(s.metainfoBytes)
	_ = cur
}

// assertResolverCleanup verifies the owned torrent is gone and the unique
// temporary directory tree is empty again.
func assertResolverCleanup(t *testing.T, s *resolveServer, hash, downloadRoot string) {
	t.Helper()
	if _, still := s.torrentSnapshot(hash); still {
		t.Fatalf("resolver torrent %s survived cleanup", hash)
	}
	metadataDir := filepath.Join(downloadRoot, ".metadata")
	entries, err := os.ReadDir(metadataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("resolver left residue below %s: %v", metadataDir, names)
	}
}

func TestMagnetVersion(t *testing.T) {
	cases := []struct {
		version   string
		supported bool
	}{
		{"v4.3.9", false},
		{"v4.4.5", false},
		{"v4.5.0", true},
		{"v4.6.5", true},
		{"v5.0.0", true},
		// Numeric component comparison: 4.10 > 4.5, which lexicographic
		// string ordering would misjudge.
		{"v4.10.0", true},
		{"v5.1.2", true},
		{"4.5", true},
		{"v4.4", false},
		{"", false},
		{"garbage", false},
		{"v4.x.0", false},
	}
	for _, tc := range cases {
		v, ok := parseVersion(tc.version)
		if got := ok && versionSupportsMagnets(v); got != tc.supported {
			t.Errorf("version %q: supported = %v, want %v", tc.version, got, tc.supported)
		}
	}
}

func TestMagnetInfoHash(t *testing.T) {
	raw, err := hex.DecodeString(magnetTestHash)
	if err != nil {
		t.Fatal(err)
	}
	b32 := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	cases := []struct {
		uri string
		ok  bool
	}{
		{magnetTestURI(), true},
		{"magnet:?xt=urn:btih:" + magnetTestHash, true},
		{"MAGNET:?xt=urn:btih:" + magnetTestHash, true},
		{"magnet:?xt=urn:btih:" + b32 + "&dn=x", true},
		{"magnet:?dn=x", false},
		{"magnet:?xt=urn:btmh:1220abcdef", false},
		{"https://example.com/file.torrent", false},
		{"magnet:?xt=urn:btih:zzzz", false},
	}
	for _, tc := range cases {
		h, err := magnetInfoHash(tc.uri)
		if tc.ok && (err != nil || h != magnetTestHash) {
			t.Errorf("magnetInfoHash(%q) = %q, %v; want %s", tc.uri, h, err, magnetTestHash)
		}
		if !tc.ok && err == nil {
			t.Errorf("magnetInfoHash(%q) = %q, want error", tc.uri, h)
		}
	}
}

func TestResolveMagnetRejectsUnsupportedBeforeAdd(t *testing.T) {
	for _, version := range []string{"v4.3.9", "v4.4.5"} {
		t.Run(version, func(t *testing.T) {
			s := newResolveServer(t, version)
			c := s.newClient()
			_, err := c.ResolveMagnet(context.Background(), magnetTestURI(), t.TempDir())
			if !errors.Is(err, domain.ErrMagnetUnsupported) {
				t.Fatalf("ResolveMagnet on %s: err = %v, want domain.ErrMagnetUnsupported", version, err)
			}
			if got := s.addCountValue(); got != 0 {
				t.Fatalf("unsupported daemon saw %d add requests, want 0", got)
			}
			for _, e := range s.snapshotEvents() {
				if e == "add" || e == "pause" || e == "stop" || e == "delete" {
					t.Fatalf("unsupported daemon saw %q event: %v", e, s.snapshotEvents())
				}
			}
		})
	}
}

func TestResolveMagnetResolvesAndExports(t *testing.T) {
	s := newResolveServer(t, "v4.5.0")
	s.setReady(true)
	downloadRoot := t.TempDir()
	c := s.newClient()
	raw, err := c.ResolveMagnet(context.Background(), magnetTestURI(), downloadRoot)
	if err != nil {
		t.Fatal(err)
	}
	h, err := infoHash(raw)
	if err != nil || h != magnetTestHash {
		t.Fatalf("resolved metainfo hash = %q, %v; want %s", h, err, magnetTestHash)
	}
	form := s.lastAdd()
	if form == nil {
		t.Fatal("resolver never issued an add request")
	}
	if got := form.Get("urls"); got != magnetTestURI() {
		t.Errorf("add urls = %q, want the magnet uri", got)
	}
	if got := form.Get("category"); got != "torrent-tv" {
		t.Errorf("add category = %q, want torrent-tv", got)
	}
	if got := form.Get("stopCondition"); got != "MetadataReceived" {
		t.Errorf("add stopCondition = %q, want MetadataReceived", got)
	}
	tag := form.Get("tags")
	if !strings.HasPrefix(tag, "torrent-tv-resolve-") || len(tag) <= len("torrent-tv-resolve-") {
		t.Errorf("add tags = %q, want a unique torrent-tv-resolve-* tag", tag)
	}
	savepath := form.Get("savepath")
	wantPrefix := filepath.Join(downloadRoot, ".metadata") + string(os.PathSeparator)
	if !strings.HasPrefix(savepath, wantPrefix) || len(savepath) <= len(wantPrefix) {
		t.Errorf("add savepath = %q, want a unique directory below %s", savepath, wantPrefix)
	}
	assertResolverCleanup(t, s, magnetTestHash, downloadRoot)
	if s.pausedBeforeFiles() {
		t.Fatal("resolver paused the torrent before any files existed")
	}
	if s.countEvent("pause") != 1 || s.countEvent("stop") != 0 {
		t.Fatalf("4.x daemon events = %v, want exactly one pause and no stop", s.snapshotEvents())
	}
}

func TestResolveMagnetUsesFiveXStopNaming(t *testing.T) {
	s := newResolveServer(t, "v5.0.0")
	s.setReady(true)
	c := s.newClient()
	raw, err := c.ResolveMagnet(context.Background(), magnetTestURI(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := infoHash(raw); h != magnetTestHash {
		t.Fatalf("hash = %q", h)
	}
	if s.countEvent("stop") != 1 || s.countEvent("pause") != 0 {
		t.Fatalf("5.x daemon events = %v, want stop and no pause", s.snapshotEvents())
	}
}

func TestResolveMagnetRetriesMetadata409(t *testing.T) {
	s := newResolveServer(t, "v4.6.5")
	// Metadata is received shortly after the add; the first export attempt
	// still reports not-ready with 409.
	time.AfterFunc(250*time.Millisecond, func() { s.setReady(true) })
	s.exportHook = func(hash string, call int) (int, []byte) {
		if call == 1 {
			return http.StatusConflict, nil
		}
		return http.StatusOK, magnetTestMetainfo
	}
	c := s.newClient()
	raw, err := c.ResolveMagnet(context.Background(), magnetTestURI(), t.TempDir())
	if err != nil {
		t.Fatalf("409 then success should resolve: %v", err)
	}
	if h, _ := infoHash(raw); h != magnetTestHash {
		t.Fatalf("hash = %q", h)
	}
}

func TestResolveMagnetDeadlineCancelsAndCleansUp(t *testing.T) {
	s := newResolveServer(t, "v5.0.0")
	// Metadata never arrives inside the request window.
	downloadRoot := t.TempDir()
	c := s.newClient()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_, err := c.ResolveMagnet(ctx, magnetTestURI(), downloadRoot)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	assertResolverCleanup(t, s, magnetTestHash, downloadRoot)
	if got, _ := s.removedSnapshot(magnetTestHash); !got.deleteFiles {
		t.Fatal("deadline cleanup removed the torrent without its race-downloaded files")
	}
}

func TestResolveMagnetRejectsMalformedExport(t *testing.T) {
	s := newResolveServer(t, "v4.5.0")
	s.setReady(true)
	s.exportHook = func(hash string, call int) (int, []byte) {
		return http.StatusOK, []byte("this is not bencode")
	}
	downloadRoot := t.TempDir()
	c := s.newClient()
	_, err := c.ResolveMagnet(context.Background(), magnetTestURI(), downloadRoot)
	if err == nil {
		t.Fatal("malformed export must fail")
	}
	assertResolverCleanup(t, s, magnetTestHash, downloadRoot)
}

func TestResolveMagnetRejectsHashMismatch(t *testing.T) {
	s := newResolveServer(t, "v4.5.0")
	s.setReady(true)
	bad, _ := magnetMismatchMetainfo(t)
	s.exportHook = func(hash string, call int) (int, []byte) {
		return http.StatusOK, bad
	}
	downloadRoot := t.TempDir()
	c := s.newClient()
	_, err := c.ResolveMagnet(context.Background(), magnetTestURI(), downloadRoot)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v, want a hash mismatch error", err)
	}
	assertResolverCleanup(t, s, magnetTestHash, downloadRoot)
}

func TestResolveMagnetPreflightExportsExistingWithoutMutation(t *testing.T) {
	s := newResolveServer(t, "v4.6.5")
	s.setReady(true)
	s.mu.Lock()
	s.torrents[magnetTestHash] = &fakeTorrent{
		hash:     magnetTestHash,
		name:     "video.mp4",
		state:    "pausedUP",
		savePath: "/srv/downloads",
		category: "movies",
		tags:     []string{"keep"},
		files:    []map[string]any{{"name": "video.mp4", "size": 5, "progress": 1, "priority": 0, "index": 0}},
	}
	s.mu.Unlock()
	c := s.newClient()
	raw, err := c.ResolveMagnet(context.Background(), magnetTestURI(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := infoHash(raw); h != magnetTestHash {
		t.Fatalf("hash = %q", h)
	}
	if got := s.addCountValue(); got != 0 {
		t.Fatalf("preexisting torrent resolved with %d add requests, want 0", got)
	}
	for _, e := range s.snapshotEvents() {
		switch e {
		case "pause", "stop", "delete", "setCategory", "setTags", "setTrackers", "resume", "start":
			t.Fatalf("preexisting torrent was mutated: %v", s.snapshotEvents())
		}
	}
	got, ok := s.torrentSnapshot(magnetTestHash)
	if !ok {
		t.Fatal("preexisting torrent was deleted")
	}
	if got.category != "movies" || strings.Join(got.tags, ",") != "keep" || got.state != "pausedUP" || got.stopped {
		t.Fatalf("preexisting torrent changed: %+v", got)
	}
}

func TestResolveMagnetForeignTorrentAfterDuplicateResponse(t *testing.T) {
	s := newResolveServer(t, "v4.5.0")
	s.setReady(true)
	s.dupMode = "conflict"
	// A torrent with the same hash appears concurrently, but it belongs to
	// the user: different save path and no resolver tag.
	s.onAdd = func(s *resolveServer, form url.Values) {
		hash, _ := magnetInfoHash(form.Get("urls"))
		s.mu.Lock()
		defer s.mu.Unlock()
		s.torrents[hash] = &fakeTorrent{
			hash:     hash,
			name:     "video.mp4",
			state:    "uploading",
			savePath: "/srv/user-torrents",
			category: "user-stuff",
			tags:     []string{"user-tag"},
			files:    []map[string]any{{"name": "video.mp4", "size": 5, "progress": 1, "priority": 0, "index": 0}},
		}
	}
	downloadRoot := t.TempDir()
	c := s.newClient()
	raw, err := c.ResolveMagnet(context.Background(), magnetTestURI(), downloadRoot)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := infoHash(raw); h != magnetTestHash {
		t.Fatalf("hash = %q", h)
	}
	for _, e := range s.snapshotEvents() {
		switch e {
		case "pause", "stop", "delete", "setCategory", "setTags", "setTrackers":
			t.Fatalf("foreign torrent was mutated or deleted: %v", s.snapshotEvents())
		}
	}
	if got, ok := s.torrentSnapshot(magnetTestHash); !ok || got.category != "user-stuff" {
		t.Fatalf("foreign torrent altered: %+v ok=%v", got, ok)
	}
	metadataDir := filepath.Join(downloadRoot, ".metadata")
	entries, err := os.ReadDir(metadataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("resolver left its temporary directory behind: %d entries", len(entries))
	}
}

func TestResolveMagnetOwnedTorrentAfterAmbiguousAdd(t *testing.T) {
	s := newResolveServer(t, "v4.5.0")
	s.setReady(true)
	// The daemon answers the add with the older duplicate shape ("Fails.")
	// while the torrent was actually created as ours: save path and tag both
	// match what the resolver sent.
	s.dupMode = "fails"
	s.onAdd = func(s *resolveServer, form url.Values) {
		hash, _ := magnetInfoHash(form.Get("urls"))
		s.mu.Lock()
		defer s.mu.Unlock()
		s.torrents[hash] = &fakeTorrent{
			hash:     hash,
			name:     "video.mp4",
			state:    "downloading",
			savePath: form.Get("savepath"),
			category: form.Get("category"),
			tags:     strings.Split(form.Get("tags"), ","),
			files:    []map[string]any{{"name": "video.mp4", "size": 5, "progress": 1, "priority": 0, "index": 0}},
		}
	}
	downloadRoot := t.TempDir()
	c := s.newClient()
	raw, err := c.ResolveMagnet(context.Background(), magnetTestURI(), downloadRoot)
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := infoHash(raw); h != magnetTestHash {
		t.Fatalf("hash = %q", h)
	}
	assertResolverCleanup(t, s, magnetTestHash, downloadRoot)
}

func TestResolveMagnetSharesHashGateWithAdd(t *testing.T) {
	s := newResolveServer(t, "v5.0.0")
	// Metadata never arrives: the resolver holds the per-hash gate for its
	// whole bounded wait and cleanup, so Add must queue behind it instead of
	// colliding with the placeholder torrent.
	c := s.newClient()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	type result struct {
		raw []byte
		err error
	}
	resolveDone := make(chan result, 1)
	go func() {
		raw, err := c.ResolveMagnet(ctx, magnetTestURI(), t.TempDir())
		resolveDone <- result{raw, err}
	}()
	time.Sleep(60 * time.Millisecond)
	addDone := make(chan error, 1)
	go func() {
		_, err := c.Add(context.Background(), strings.NewReader(string(magnetTestMetainfo)), "/srv/downloads")
		addDone <- err
	}()
	res := <-resolveDone
	if !errors.Is(res.err, context.DeadlineExceeded) {
		t.Fatalf("resolve err = %v, want DeadlineExceeded", res.err)
	}
	if err := <-addDone; err != nil {
		t.Fatalf("Add after gated resolve: %v", err)
	}
	events := s.snapshotEvents()
	lastAdd, firstDelete := -1, -1
	for i, e := range events {
		switch e {
		case "add":
			lastAdd = i
		case "delete":
			if firstDelete < 0 {
				firstDelete = i
			}
		}
	}
	if firstDelete < 0 || lastAdd <= firstDelete {
		t.Fatalf("Add bypassed the per-hash gate: events = %v", events)
	}
	if _, still := s.torrentSnapshot(magnetTestHash); !still {
		// Add re-created the torrent after cleanup; both operations landed.
		return
	}
	if s.addCountValue() != 2 {
		t.Fatalf("add count = %d, want resolve + Add", s.addCountValue())
	}
}

// Compile-time guard for the resolver constants used above.
var _ = strconv.Itoa
