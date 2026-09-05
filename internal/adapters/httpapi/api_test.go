package httpapi

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/portalclient"
	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/application/portal"
	"github.com/mihaiflorentin88/torrent-tv/internal/application/updates"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

func TestBrowserTranscodeRouteIsRegistered(t *testing.T) {
	handler := newStreamHTTPTest(t, &streamEngine{status: domain.DownloadStatus{
		State: "downloading", Progress: 0.05, Sequential: true, FirstLastPriority: true,
		TempPathEnabled: true, TempPath: t.TempDir(), SavePath: t.TempDir(), PieceSize: 1 << 20,
	}}, domain.Download{
		ID: "source", ReleaseID: "release", EngineID: "qb:abc", FileIndex: 0,
		FilePath: "movie.mkv", State: "downloading", Progress: 0.05, SizeBytes: 200 << 20,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/streams/unknown/browser", nil))
	// An unknown source is refused by the media-info service (503, retryable),
	// not by routing (404): the compatibility route exists.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/streams/unknown/browser status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("compatibility route lost the Retry-After hint for in-flight sources")
	}
}

func TestDownloadDTOExposesCompatibilityStream(t *testing.T) {
	b, err := json.Marshal(downloadDTO(domain.Download{ID: "abc", State: "downloading"}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, `"browserStreamUrl":"/api/v1/streams/abc/browser"`) {
		t.Fatal("download DTO lost the browser compatibility stream URL")
	}
	if !strings.Contains(text, `"streamUrl":"/api/v1/streams/abc"`) {
		t.Fatal("download DTO lost the progressive playback stream URL")
	}
}

func TestDownloadDTOSeedingStateKeepsLocalPlayback(t *testing.T) {
	// A partially selected season pack surfaces seeding with progress below
	// one: the selected episodes finished, deselected files skew progress.
	// Legacy rows keep raw qBittorrent strings with the same meaning.
	for _, state := range []string{domain.StateSeeding, "stalledUP"} {
		d := downloadDTO(domain.Download{ID: "pack", State: state, Progress: 0.42})
		if d["playbackMode"] != "local" {
			t.Errorf("playbackMode for state %q = %v, want local", state, d["playbackMode"])
		}
	}
	d := downloadDTO(domain.Download{ID: "pack", State: domain.StateDownloading, Progress: 0.42})
	if d["playbackMode"] != "progressive" {
		t.Errorf("playbackMode for downloading = %v, want progressive", d["playbackMode"])
	}
}

func TestDownloadDTOQueuedStatePlaybackMode(t *testing.T) {
	// canonicalState folds queuedUP/queuedDL into StateQueued. A completed
	// partially selected pack (progress >= 1) is served from disk; queued
	// below one still streams progressively.
	d := downloadDTO(domain.Download{ID: "pack", State: domain.StateQueued, Progress: 1})
	if d["playbackMode"] != "local" {
		t.Errorf("playbackMode for queued at progress 1 = %v, want local", d["playbackMode"])
	}
	d = downloadDTO(domain.Download{ID: "pack", State: domain.StateQueued, Progress: 0.5})
	if d["playbackMode"] != "progressive" {
		t.Errorf("playbackMode for queued at progress 0.5 = %v, want progressive", d["playbackMode"])
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		header      string
		start, end  int64
		partial, ok bool
	}{{"", 0, 99, false, true}, {"bytes=0-9", 0, 9, true, true}, {"bytes=90-", 90, 99, true, true}, {"bytes=-10", 90, 99, true, true}, {"bytes=0-1,4-5", 0, 0, true, false}, {"bytes=100-", 0, 0, true, false}}
	for _, tt := range tests {
		s, e, p, ok := parseRange(tt.header, 100)
		if s != tt.start || e != tt.end || p != tt.partial || ok != tt.ok {
			t.Errorf("%q got %d,%d,%v,%v", tt.header, s, e, p, ok)
		}
	}
}

func TestPortalAPIKeyRedactedAndSchemaSensitive(t *testing.T) {
	v := config.Defaults()
	v.PortalAPIKey = "portal-secret"
	v.FileListPasskey = "filelist-secret"
	v.QBittorrentPassword = "qb-secret"
	v.TMDBAPIKey = "tmdb-secret"
	b, err := json.Marshal(RedactedSettings(v, "data/settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, secret := range []string{"portal-secret", "filelist-secret", "qb-secret", "tmdb-secret"} {
		if strings.Contains(text, secret) {
			t.Fatal("response leaked " + secret)
		}
	}
	if !strings.Contains(text, `"portalAPIKeyConfigured":true`) || !strings.Contains(text, `"fileListPasskeyConfigured":true`) || !strings.Contains(text, `"qbittorrentPasswordConfigured":true`) {
		t.Fatal("configured indicators missing")
	}
	blank := RedactedSettings(config.Defaults(), "data/settings.json")
	if blank.PortalAPIKeyConfigured {
		t.Fatal("portalAPIKeyConfigured must be false for a blank key")
	}

	store, err := config.LoadAt(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range SettingsSchema(store) {
		if field.Key == "portalAPIKey" {
			if !field.Sensitive {
				t.Error("portalAPIKey must be sensitive")
			}
			if field.RestartRequired {
				t.Error("portalAPIKey must not be restart-required")
			}
			return
		}
	}
	t.Fatal("settings schema lost portalAPIKey")
}

func TestContentTypeUsesBrowserMediaTypes(t *testing.T) {
	for path, want := range map[string]string{
		"movie.mkv":  "video/matroska",
		"movie.mp4":  "video/mp4",
		"movie.webm": "video/webm",
	} {
		if got := contentType(path); got != want {
			t.Errorf("contentType(%q) = %q, want %q", path, got, want)
		}
	}
}

// — Acquire/GetDownload error mapping: a removed source is permanent (404),
// anything the server can transiently fail to resolve (database restart, locked
// database, torrent engine hiccup) is retryable (503 + Retry-After). Clients use
// the split to stop hammering on 404 and keep best-effort persistence otherwise.

type stubRepo struct {
	application.Repository
	downloadErr error
}

func (r stubRepo) GetDownload(context.Context, string) (domain.Download, error) {
	return domain.Download{}, r.downloadErr
}

func newStubHandler(t *testing.T, downloadErr error) http.Handler {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(map[string]any{
		"databasePath":      filepath.Join(dir, "test.db"),
		"downloadRoot":      filepath.Join(dir, "downloads"),
		"trustedCidrs":      []string{"127.0.0.0/8", "::1/128", "192.0.2.0/24"},
		"torrentSessionDir": filepath.Join(dir, "torrent-session"),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvironmentPrefix+"SETTINGS_PATH", path)
	store, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewService(nil, nil, stubRepo{Repository: nil, downloadErr: downloadErr}, store)
	return New(service, store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
}

func TestStreamAcquireMissingSourceIs404(t *testing.T) {
	handler := newStubHandler(t, sql.ErrNoRows)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/streams/abc", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/streams/abc status = %d, want 404", rec.Code)
	}
}

func TestStreamAcquireTransientErrorIs503(t *testing.T) {
	handler := newStubHandler(t, fmt.Errorf("get download: %w", errors.New("database is locked")))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/streams/abc", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/v1/streams/abc status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("transient acquire error lost the Retry-After hint")
	}
	var problem struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != http.StatusServiceUnavailable || !strings.Contains(problem.Detail, "database is locked") {
		t.Fatalf("problem body = %s, want 503 with the original detail", rec.Body.String())
	}
}

func TestPlaybackUpdateMissingSourceIs404(t *testing.T) {
	handler := newStubHandler(t, sql.ErrNoRows)
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/playback/abc", strings.NewReader(`{"positionMs":1000,"durationMs":5000}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT /api/v1/playback/abc status = %d, want 404", rec.Code)
	}
}

func TestPlaybackUpdateTransientErrorIs503(t *testing.T) {
	handler := newStubHandler(t, fmt.Errorf("get download: %w", errors.New("database is locked")))
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/playback/abc", strings.NewReader(`{"positionMs":1000,"durationMs":5000}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT /api/v1/playback/abc status = %d, want 503", rec.Code)
	}
}

func TestPlaybackUpdateNegativeInputIs400(t *testing.T) {
	handler := newStubHandler(t, nil)
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/playback/abc", strings.NewReader(`{"positionMs":-1,"durationMs":5000}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/v1/playback/abc status = %d, want 400", rec.Code)
	}
}

// — Allocation (GB) and free-space reserve (GB) settings round-trip through the
// settings API; invalid values fail with the standard validation problem.

func getSettingsBody(t *testing.T, handler http.Handler) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func putSettingsBody(t *testing.T, handler http.Handler, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	for key := range body {
		if strings.HasSuffix(key, "Configured") || key == "settingsPath" {
			delete(body, key)
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(string(b)))
	request.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	return rec
}

func TestSettingsRoundTripsAllocationAndReserve(t *testing.T) {
	handler := newStubHandler(t, nil)
	current := getSettingsBody(t, handler)
	current["allocationGb"] = 0.5
	current["reserveGb"] = 8.0
	if rec := putSettingsBody(t, handler, current); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/v1/settings status = %d, body = %s", rec.Code, rec.Body.String())
	}
	saved := getSettingsBody(t, handler)
	if saved["allocationGb"] != 0.5 || saved["reserveGb"] != 8.0 {
		t.Fatalf("GET lost the persisted allocation/reserve: %v/%v", saved["allocationGb"], saved["reserveGb"])
	}

	current = getSettingsBody(t, handler)
	current["allocationGb"] = -1
	rec := putSettingsBody(t, handler, current)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative allocation status = %d, want 400", rec.Code)
	}
	var problemBody struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problemBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(problemBody.Detail, "allocationGb") {
		t.Fatalf("validation problem did not name the field: %s", problemBody.Detail)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"allocationGb":NaN}`))
	request.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("NaN allocation status = %d, want 400", rec.Code)
	}

	current = getSettingsBody(t, handler)
	current["allocationGb"] = 0
	current["reserveGb"] = 0
	if rec := putSettingsBody(t, handler, current); rec.Code != http.StatusOK {
		t.Fatalf("zero (disabled) values status = %d, body = %s", rec.Code, rec.Body.String())
	}
	saved = getSettingsBody(t, handler)
	if saved["allocationGb"] != float64(0) || saved["reserveGb"] != float64(0) {
		t.Fatalf("disabled values did not persist: %v/%v", saved["allocationGb"], saved["reserveGb"])
	}
}

func TestSettingsSchemaListsRetentionFields(t *testing.T) {
	handler := newStubHandler(t, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/schema", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings/schema status = %d", rec.Code)
	}
	var page struct {
		Items []struct {
			Key       string `json:"key"`
			Help      string `json:"help"`
			Sensitive bool   `json:"sensitive"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	descriptors := map[string]struct {
		Help      string
		Sensitive bool
	}{}
	for _, item := range page.Items {
		descriptors[item.Key] = struct {
			Help      string
			Sensitive bool
		}{item.Help, item.Sensitive}
	}
	for key, phrase := range map[string]string{
		"allocationGb": "0 disables retention",
		"reserveGb":    "0 disables the reserve check",
	} {
		descriptor, ok := descriptors[key]
		if !ok {
			t.Fatalf("settings schema lost %s", key)
		}
		if !strings.Contains(descriptor.Help, phrase) {
			t.Errorf("%s help = %q, want it to mention %q", key, descriptor.Help, phrase)
		}
		if descriptor.Sensitive {
			t.Errorf("%s must not be sensitive", key)
		}
	}
}

// — Ticket #49: eviction rules and protection toggles round-trip through the
// settings API; unknown atoms fail validation; the schema describes the new
// fields for the browser and keeps them off the TV.

func TestSettingsRoundTripEvictionRulesAndProtections(t *testing.T) {
	handler := newStubHandler(t, nil)
	current := getSettingsBody(t, handler)
	if saved, ok := current["protectIncomplete"].(bool); !ok || !saved {
		t.Fatalf("protectIncomplete default = %v, want true", current["protectIncomplete"])
	}
	if rules, ok := current["evictionRules"].([]any); !ok || len(rules) != 1 || rules[0] != "oldest-completed" {
		t.Fatalf("evictionRules default = %v, want [oldest-completed]", current["evictionRules"])
	}

	current = getSettingsBody(t, handler)
	current["evictionRules"] = []any{"newest-completed", "largest"}
	current["protectIncomplete"] = false
	current["protectFavorites"] = true
	if rec := putSettingsBody(t, handler, current); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/v1/settings status = %d, body = %s", rec.Code, rec.Body.String())
	}
	saved := getSettingsBody(t, handler)
	if rules, ok := saved["evictionRules"].([]any); !ok || len(rules) != 2 || rules[0] != "newest-completed" || rules[1] != "largest" {
		t.Fatalf("GET lost the persisted eviction rules: %v", saved["evictionRules"])
	}
	if saved["protectIncomplete"] != false || saved["protectFavorites"] != true {
		t.Fatalf("GET lost the persisted protection toggles: %v/%v", saved["protectIncomplete"], saved["protectFavorites"])
	}

	current = getSettingsBody(t, handler)
	current["evictionRules"] = []any{"largest", "shiniest"}
	rec := putSettingsBody(t, handler, current)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown eviction atom status = %d, want 400", rec.Code)
	}
	var problemBody struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problemBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(problemBody.Detail, "evictionRules") || !strings.Contains(problemBody.Detail, "shiniest") {
		t.Fatalf("validation problem did not name the field and atom: %s", problemBody.Detail)
	}
}

func TestSettingsSchemaMarksEngineFieldsRestartRequired(t *testing.T) {
	handler := newStubHandler(t, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/schema", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings/schema status = %d", rec.Code)
	}
	var page struct {
		Items []struct {
			Key             string `json:"key"`
			RestartRequired bool   `json:"restartRequired"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	restart := map[string]bool{}
	for _, item := range page.Items {
		restart[item.Key] = item.RestartRequired
	}
	for _, key := range []string{"downloadEngine", "torrentPeerPort", "torrentSessionDir"} {
		if !restart[key] {
			t.Fatalf("schema field %q must be marked restartRequired", key)
		}
	}
}

func TestSettingsSchemaDescribesEvictionFieldsForBrowserOnly(t *testing.T) {
	handler := newStubHandler(t, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/settings/schema", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings/schema status = %d", rec.Code)
	}
	var page struct {
		Items []struct {
			Key       string `json:"key"`
			Help      string `json:"help"`
			TVVisible bool   `json:"tvVisible"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	help := map[string]string{}
	tvVisible := map[string]bool{}
	for _, item := range page.Items {
		help[item.Key] = item.Help
		tvVisible[item.Key] = item.TVVisible
	}
	for _, key := range []string{"evictionRules", "protectIncomplete", "protectLeased", "protectFavorites", "protectNeverWatched"} {
		if _, ok := help[key]; !ok {
			t.Fatalf("settings schema lost %s", key)
		}
		if tvVisible[key] {
			t.Errorf("%s must stay off the TV settings screen", key)
		}
	}
	for _, atom := range []string{"oldest-completed", "newest-completed", "least-recently-played", "most-recently-played", "watched-first", "never-watched-first", "largest", "smallest"} {
		if !strings.Contains(help["evictionRules"], atom) {
			t.Errorf("evictionRules help does not mention %q: %s", atom, help["evictionRules"])
		}
	}
	if !strings.Contains(help["protectNeverWatched"], "never") {
		t.Errorf("protectNeverWatched help is missing its meaning: %s", help["protectNeverWatched"])
	}
}

func TestPutSettingsRejectsUnwritableNativeSessionDir(t *testing.T) {
	handler := newStubHandler(t, nil)
	base := getSettingsBody(t, handler)
	base["downloadEngine"] = "native"
	blocker := filepath.Join(t.TempDir(), "not-a-dir.txt")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	base["torrentSessionDir"] = filepath.Join(blocker, "session")
	rec := putSettingsBody(t, handler, base)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unwritable session dir status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not writable") && !strings.Contains(rec.Body.String(), "create") {
		t.Fatalf("error must name the failing path operation, body = %s", rec.Body.String())
	}
	saved := getSettingsBody(t, handler)
	if saved["torrentSessionDir"] == base["torrentSessionDir"] {
		t.Fatal("rejected save must not persist the failing path")
	}
}

func TestPutSettingsCreatesMissingNativeSessionDir(t *testing.T) {
	handler := newStubHandler(t, nil)
	base := getSettingsBody(t, handler)
	created := filepath.Join(t.TempDir(), "made-on-save")
	base["downloadEngine"] = "native"
	base["torrentSessionDir"] = created
	if rec := putSettingsBody(t, handler, base); rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/v1/settings status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("session dir must be created on save: %v", err)
	}
}

// — Portal and update surfaces (plan S8). Every regression drives the real
// HTTP server (httptest.Server) around the real integration hub, real
// upstream adapter, and a journalling service. The fixed integration host
// is intercepted onto a fake upstream whose transport observes every
// outbound call, so tests pin zero-outbound caching and bearer routing.

// journalRepo satisfies application.Repository with a memory event
// journal; the portal SSE surfaces only touch events.
type journalRepo struct {
	application.Repository
	mu     sync.Mutex
	nextID int64
	events []domain.Event
}

func (r *journalRepo) AppendEvent(_ context.Context, kind, payload string) (domain.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	event := domain.Event{ID: r.nextID, Kind: kind, Payload: payload, CreatedAt: time.Now().UTC()}
	r.events = append(r.events, event)
	return event, nil
}

func (r *journalRepo) ListEvents(_ context.Context, after int64, limit int) ([]domain.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found []domain.Event
	for _, event := range r.events {
		if len(found) == limit {
			break
		}
		if event.ID > after {
			found = append(found, event)
		}
	}
	return found, nil
}

// upstreamObserver records every outbound call the integration adapter
// makes against the fake upstream host.
type upstreamObserver struct {
	mu    sync.Mutex
	calls []upstreamCall
}

type upstreamCall struct {
	method        string
	path          string
	query         string
	authorization string
}

func (o *upstreamObserver) observe(r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, upstreamCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, authorization: r.Header.Get("Authorization")})
}

func (o *upstreamObserver) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.calls)
}

func (o *upstreamObserver) authorizationFor(path string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, call := range o.calls {
		if call.path == path {
			return call.authorization
		}
	}
	return ""
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// portalFixture is the real HTTP server wired to a real integration hub.
type portalFixture struct {
	server   *httptest.Server
	upstream *upstreamObserver
	hub      *portal.Hub
	service  *application.Service
}

func newPortalSettings(t *testing.T) *config.Store {
	t.Helper()
	dir := t.TempDir()
	body, err := json.Marshal(map[string]any{
		"databasePath":      filepath.Join(dir, "test.db"),
		"downloadRoot":      filepath.Join(dir, "downloads"),
		"trustedCidrs":      []string{"127.0.0.0/8", "::1/128"},
		"torrentSessionDir": filepath.Join(dir, "torrent-session"),
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.LoadAt(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newPortalFixture(t *testing.T, upstream http.HandlerFunc) *portalFixture {
	t.Helper()
	observer := &upstreamObserver{}
	fakeHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observer.observe(r)
		upstream(w, r)
	}))
	t.Cleanup(fakeHost.Close)
	target, err := url.Parse(fakeHost.URL)
	if err != nil {
		t.Fatalf("parse fake upstream url: %v", err)
	}
	store := newPortalSettings(t)
	service := application.NewService(nil, nil, &journalRepo{}, store)
	adapter := portalclient.New(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host, r.Host = target.Scheme, target.Host, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})})
	hub := portal.NewHub(adapter, func() string { return store.Get().PortalAPIKey }, nil, nil, func(kind string, payload any) {
		service.PublishEvent(kind, payload)
	})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(New(service, store, logger, "test", WithPortal(hub)))
	t.Cleanup(server.Close)
	return &portalFixture{server: server, upstream: observer, hub: hub, service: service}
}

// noRedirectClient keeps 302 answers observable: click redirects must be
// asserted, never followed.
var noRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func portalGet(t *testing.T, rawURL, authorization string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	res, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res, payload
}

func portalPost(t *testing.T, rawURL, body, authorization string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	res, err := noRedirectClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res, payload
}

func TestPortalStateServesCachedSnapshotAndHidesAbsentGates(t *testing.T) {
	fixture := newPortalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	// Absent gates before any refresh: every surface is hidden and
	// arrays are never null.
	res, body := portalGet(t, fixture.server.URL+"/api/v1/portal/state", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/portal/state = %d, body %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"accountsEnabled":false`) || !strings.Contains(string(body), `"adsEnabled":false`) || !strings.Contains(string(body), `"donor":false`) {
		t.Fatalf("state must hide absent gates with the agreed tags: %s", body)
	}
	if !strings.Contains(string(body), `"links":[]`) {
		t.Fatalf("links must be an empty array, never null: %s", body)
	}
	var snapshot portal.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}

	before := fixture.upstream.count()
	_, _ = portalGet(t, fixture.server.URL+"/api/v1/portal/state", "")
	if fixture.upstream.count() != before {
		t.Fatal("cached state GET made an outbound call")
	}

	// The hidden promotion slot answers an empty array, never a
	// fabricated creative, still with zero outbound calls.
	res, body = portalGet(t, fixture.server.URL+"/api/v1/portal/promotions", "")
	if res.StatusCode != http.StatusOK || string(body) != "[]\n" {
		t.Fatalf("hidden promotions = %d %q, want 200 []", res.StatusCode, body)
	}
	if fixture.upstream.count() != before {
		t.Fatal("hidden promotions GET made an outbound call")
	}

	// Click tracking refuses while the slot is hidden, again without
	// any outbound call.
	res, _ = portalGet(t, fixture.server.URL+"/api/v1/portal/promotions/p1/ad1/click", "")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("click on a hidden slot = %d, want 503", res.StatusCode)
	}
	if fixture.upstream.count() != before {
		t.Fatal("click on a hidden slot made an outbound call")
	}
}

func TestPortalStateExposesRefreshedSurfacesWithExactTags(t *testing.T) {
	fixture := newPortalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/settings":
			w.Write([]byte(`{"ads":{"enabled":true},"accounts":{"enabled":true}}`))
		case r.URL.Path == "/api/v1/links":
			w.Write([]byte(`[{"id":2,"title":"Second","url":"https://b.example","description":""},{"id":1,"title":"First","url":"https://a.example","description":"d"}]`))
		case r.URL.Path == "/api/v1/account/status":
			w.Write([]byte(`{"donor":false}`))
		case r.URL.Path == "/api/v1/ads/weights":
			w.Write([]byte(`[{"provider":"p1","id":"ad1"}]`))
		case r.URL.Path == "/api/v1/ads":
			w.Write([]byte(`[{"provider":"p1","id":"ad1","title":"Hello","text":"World","image":"https://img.example/1.png","screen_time":8},{"provider":"p2","id":"ad2","title":"Other","text":"","image":"","screen_time":5}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := fixture.hub.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	afterRefresh := fixture.upstream.count()

	res, body := portalGet(t, fixture.server.URL+"/api/v1/portal/state", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/portal/state = %d, body %s", res.StatusCode, body)
	}
	var snapshot portal.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.AccountsEnabled || !snapshot.AdsEnabled || snapshot.Donor {
		t.Fatalf("refreshed state = %+v", snapshot)
	}
	if len(snapshot.Links) != 2 || snapshot.Links[0].Title != "Second" || snapshot.Links[1].Title != "First" {
		t.Fatalf("links must preserve upstream order, got %+v", snapshot.Links)
	}

	res, body = portalGet(t, fixture.server.URL+"/api/v1/portal/promotions?count=1", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET promotions?count=1 = %d, body %s", res.StatusCode, body)
	}
	var promotions []portal.Promotion
	if err := json.Unmarshal(body, &promotions); err != nil {
		t.Fatal(err)
	}
	if len(promotions) != 1 || promotions[0].Provider != "p1" || promotions[0].ScreenTime != 8 {
		t.Fatalf("count must cap the delivered creatives, got %s", body)
	}
	if !strings.Contains(string(body), `"screenTime":8`) {
		t.Fatalf("promotions must carry the local screenTime tag: %s", body)
	}
	res, body = portalGet(t, fixture.server.URL+"/api/v1/portal/promotions?count=nonsense", "")
	if err := json.Unmarshal(body, &promotions); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || len(promotions) != 2 {
		t.Fatalf("a malformed count must deliver the cached creatives, got %d %s", res.StatusCode, body)
	}
	if fixture.upstream.count() != afterRefresh {
		t.Fatal("promotion and state GETs must serve the hub cache with zero outbound calls")
	}
}

func TestPortalSessionEndpointsForwardCredentialsSafely(t *testing.T) {
	fixture := newPortalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			w.Write([]byte(`{"token":"jwt-value","expires_at":"2030-01-01T00:00:00Z"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/register":
			w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
			w.Write([]byte(`{"id":7,"email":"me@example","display_name":"Me","role":"user"}`))
		case r.URL.Path == "/api/v1/settings":
			w.Write([]byte(`{"ads":{"enabled":false},"accounts":{"enabled":true}}`))
		case r.URL.Path == "/api/v1/links":
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/account/status":
			w.Write([]byte(`{"donor":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	// The identity operations are gated: the account surface must be
	// open before they reach upstream.
	if err := fixture.hub.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Registration needs no JWT, creates the account, and returns 201
	// with an empty body: registration does not imply login.
	res, body := portalPost(t, fixture.server.URL+"/api/v1/portal/session/register", `{"email":"new@example","password":"pw","displayName":"New"}`, "")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d, body %q, want 201", res.StatusCode, body)
	}
	if len(strings.TrimSpace(string(body))) != 0 {
		t.Fatalf("register must not return a token, got %q", body)
	}
	if got := fixture.upstream.authorizationFor("/api/v1/auth/register"); got != "" {
		t.Fatalf("register forwarded an authorization header %q", got)
	}

	// Sign-in exchanges credentials for a session with the agreed tags.
	res, body = portalPost(t, fixture.server.URL+"/api/v1/portal/session", `{"email":"me@example","password":"pw"}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, body %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"token":"jwt-value"`) || !strings.Contains(string(body), `"expires_at":`) {
		t.Fatalf("session must carry the agreed tags: %s", body)
	}
	if got := fixture.upstream.authorizationFor("/api/v1/auth/login"); got != "" {
		t.Fatalf("login forwarded an authorization header %q", got)
	}

	// Only the identity endpoint forwards the bearer upstream.
	res, body = portalGet(t, fixture.server.URL+"/api/v1/portal/session/me", "Bearer tv-token")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("me = %d, body %s", res.StatusCode, body)
	}
	if !strings.Contains(string(body), `"display_name":"Me"`) {
		t.Fatalf("user must carry the agreed tags: %s", body)
	}
	if got := fixture.upstream.authorizationFor("/api/v1/auth/me"); got != "Bearer tv-token" {
		t.Fatalf("me must forward the bearer verbatim, got %q", got)
	}
	before := fixture.upstream.count()
	res, _ = portalGet(t, fixture.server.URL+"/api/v1/portal/session/me", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me without a bearer = %d, want 401", res.StatusCode)
	}
	if fixture.upstream.count() != before {
		t.Fatal("me without a bearer must not reach upstream")
	}
}

func TestPortalCredentialRejectionIsFormErrorAndOutageClosesGate(t *testing.T) {
	loginStatus := http.StatusUnauthorized
	fixture := newPortalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login":
			if loginStatus == http.StatusOK {
				w.Write([]byte(`{}`))
				return
			}
			w.WriteHeader(loginStatus)
		case r.URL.Path == "/api/v1/settings":
			w.Write([]byte(`{"ads":{"enabled":false},"accounts":{"enabled":true}}`))
		case r.URL.Path == "/api/v1/links":
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/account/status":
			w.Write([]byte(`{"donor":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := fixture.hub.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// A credential rejection is a form error (400), not an outage, and
	// the account surface survives it.
	res, body := portalPost(t, fixture.server.URL+"/api/v1/portal/session", `{"email":"me@example","password":"pw"}`, "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("credential rejection = %d, body %s, want 400", res.StatusCode, body)
	}
	loginStatus = http.StatusOK
	res, _ = portalPost(t, fixture.server.URL+"/api/v1/portal/session", `{"email":"me@example","password":"pw"}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login after a rejection = %d, want 200", res.StatusCode)
	}

	// A transport outage is a neutral 503 that closes the account gate:
	// later account calls answer 503 with zero outbound calls, and the
	// state reflects the hidden surface.
	loginStatus = http.StatusInternalServerError
	res, body = portalPost(t, fixture.server.URL+"/api/v1/portal/session", `{"email":"me@example","password":"pw"}`, "")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("outage login = %d, body %s, want 503", res.StatusCode, body)
	}
	afterOutage := fixture.upstream.count()
	_, body = portalGet(t, fixture.server.URL+"/api/v1/portal/state", "")
	var snapshot portal.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.AccountsEnabled {
		t.Fatalf("a transport outage must hide the account surface, got %s", body)
	}
	res, _ = portalPost(t, fixture.server.URL+"/api/v1/portal/session", `{"email":"me@example","password":"pw"}`, "")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("gated login = %d, want 503", res.StatusCode)
	}
	if fixture.upstream.count() != afterOutage {
		t.Fatal("gated account calls must not reach upstream")
	}
}

func TestPortalClickFollowsOnlyValidatedDestinations(t *testing.T) {
	location := ""
	fixture := newPortalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/settings":
			w.Write([]byte(`{"ads":{"enabled":true},"accounts":{"enabled":true}}`))
		case r.URL.Path == "/api/v1/links":
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/account/status":
			w.Write([]byte(`{"donor":false}`))
		case r.URL.Path == "/api/v1/ads/weights":
			w.Write([]byte(`[{"provider":"p1","id":"ad1"}]`))
		case r.URL.Path == "/api/v1/ads":
			w.Write([]byte(`[{"provider":"p1","id":"ad1","title":"t","text":"","image":"","screen_time":5}]`))
		case r.URL.Path == "/api/v1/ads/p1/ad1/click":
			if location == "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Location", location)
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := fixture.hub.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// A validated http(s) destination is served as the redirect target.
	location = "https://destination.example/landing"
	res, body := portalGet(t, fixture.server.URL+"/api/v1/portal/promotions/p1/ad1/click", "")
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != location {
		t.Fatalf("click = %d Location %q body %s, want 302 to %q", res.StatusCode, res.Header.Get("Location"), body, location)
	}

	// A non-http(s) or relative upstream answer must never become a
	// redirect: the client gets a neutral problem instead.
	for _, unsafe := range []string{"javascript:alert(1)", "/relative/path", "ftp://host/file"} {
		location = unsafe
		res, _ = portalGet(t, fixture.server.URL+"/api/v1/portal/promotions/p1/ad1/click", "")
		if res.StatusCode != http.StatusBadGateway {
			t.Fatalf("unsafe destination %q = %d, want 502", unsafe, res.StatusCode)
		}
	}
}

// scriptedCoordinator is a Coordinator fake recording the handler's call
// order so the accepted-apply barrier ordering is observable.
type scriptedCoordinator struct {
	mu          sync.Mutex
	steps       []string
	current     updates.Status
	checkResult updates.Status
	checkErr    error
	applyResult updates.ApplyResult
	applyErr    error
	flushed     chan struct{}
	flushOnce   sync.Once
}

func (c *scriptedCoordinator) record(step string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.steps = append(c.steps, step)
}

func (c *scriptedCoordinator) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.steps...)
}

func (c *scriptedCoordinator) Current() updates.Status {
	c.record("current")
	return c.current
}

func (c *scriptedCoordinator) Check(context.Context) (updates.Status, error) {
	c.record("check")
	return c.checkResult, c.checkErr
}

func (c *scriptedCoordinator) Apply(context.Context) (updates.ApplyResult, error) {
	c.record("apply")
	return c.applyResult, c.applyErr
}

func (c *scriptedCoordinator) ResponseFlushed() {
	c.record("flush")
	c.flushOnce.Do(func() { close(c.flushed) })
}

func newUpdatesFixture(t *testing.T, coordinator *scriptedCoordinator) *httptest.Server {
	t.Helper()
	store := newPortalSettings(t)
	service := application.NewService(nil, nil, &journalRepo{}, store)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(New(service, store, logger, "test", WithUpdates(coordinator)))
	t.Cleanup(server.Close)
	return server
}

func TestUpdateRoutesServeCachedCurrentFreshCheckAndApplyMapping(t *testing.T) {
	releases := "https://github.com/mihaiflorentin88/torrent-tv/releases"
	coord := &scriptedCoordinator{
		flushed:     make(chan struct{}),
		current:     updates.Status{CurrentVersion: "1.2.3", ReleasesURL: releases, SelfUpdate: true},
		checkResult: updates.Status{CurrentVersion: "1.2.3", Available: true, Latest: "1.3.0", Notes: "Server-only; TV applications update manually.", ReleasedAt: "2026-09-01T00:00:00Z", ReleasesURL: releases, SelfUpdate: true},
		applyResult: updates.ApplyResult{Accepted: true, Status: updates.Status{CurrentVersion: "1.2.3", Applying: true, ReleasesURL: releases, SelfUpdate: true}},
	}
	server := newUpdatesFixture(t, coord)

	// current is the cached status with the agreed tags.
	res, body := portalGet(t, server.URL+"/api/v1/updates/current", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET updates/current = %d, body %s", res.StatusCode, body)
	}
	for _, tag := range []string{`"currentVersion":"1.2.3"`, `"releasesUrl":`, `"selfUpdate":true`, `"applying":false`} {
		if !strings.Contains(string(body), tag) {
			t.Fatalf("status must carry %s: %s", tag, body)
		}
	}

	// check is a fresh fetch.
	res, body = portalPost(t, server.URL+"/api/v1/updates/check", "", "")
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"latest":"1.3.0"`) || !strings.Contains(string(body), `"releasedAt":"2026-09-01T00:00:00Z"`) {
		t.Fatalf("POST updates/check = %d, body %s", res.StatusCode, body)
	}

	// A failed check is a neutral problem; no stale availability.
	coord.checkErr = errors.New("resolution failed: upstream feed unreachable")
	res, body = portalPost(t, server.URL+"/api/v1/updates/check", "", "")
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), `"status":502`) {
		t.Fatalf("failed check = %d, body %s, want a 502 problem", res.StatusCode, body)
	}

	// apply: accepted 202, no-op 200, busy/manual-only 409, other
	// failures a neutral problem.
	res, body = portalPost(t, server.URL+"/api/v1/updates/apply", "", "")
	if res.StatusCode != http.StatusAccepted || !strings.Contains(string(body), `"applying":true`) {
		t.Fatalf("accepted apply = %d, body %s, want 202", res.StatusCode, body)
	}
	steps := coord.recorded()
	if len(steps) < 2 || steps[len(steps)-2] != "apply" || steps[len(steps)-1] != "flush" {
		t.Fatalf("accepted apply must release the barrier after Apply returned, got %v", steps)
	}
	coord.applyResult = updates.ApplyResult{Accepted: false, Status: coord.current}
	res, _ = portalPost(t, server.URL+"/api/v1/updates/apply", "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("already-current apply = %d, want 200", res.StatusCode)
	}
	coord.applyErr = updates.ErrApplyBusy
	res, body = portalPost(t, server.URL+"/api/v1/updates/apply", "", "")
	if res.StatusCode != http.StatusConflict || !strings.Contains(string(body), `"status":409`) {
		t.Fatalf("busy apply = %d, body %s, want 409", res.StatusCode, body)
	}
	coord.applyErr = fmt.Errorf("%w: atomic directory exchange unavailable", updates.ErrManualOnly)
	res, _ = portalPost(t, server.URL+"/api/v1/updates/apply", "", "")
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("manual-only apply = %d, want 409", res.StatusCode)
	}
	coord.applyErr = errors.New("verification failed: checksum mismatch")
	res, body = portalPost(t, server.URL+"/api/v1/updates/apply", "", "")
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), `"status":502`) {
		t.Fatalf("failed apply = %d, body %s, want a neutral problem", res.StatusCode, body)
	}
}

func TestUpdateApplyFlushBarrierSurvivesLostClient(t *testing.T) {
	releases := "https://github.com/mihaiflorentin88/torrent-tv/releases"

	// A connected client: the barrier releases after the response was
	// written, and the body completes.
	coord := &scriptedCoordinator{
		flushed:     make(chan struct{}),
		applyResult: updates.ApplyResult{Accepted: true, Status: updates.Status{CurrentVersion: "1.2.3", Applying: true, ReleasesURL: releases, SelfUpdate: true}},
	}
	server := newUpdatesFixture(t, coord)
	res, body := portalPost(t, server.URL+"/api/v1/updates/apply", "", "")
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("accepted apply = %d, body %s, want 202", res.StatusCode, body)
	}
	select {
	case <-coord.flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("accepted apply never released the response barrier")
	}

	// A client that vanishes before the flush must never abort or
	// strand the accepted operation: the handler still releases the
	// barrier.
	coord = &scriptedCoordinator{
		flushed:     make(chan struct{}),
		applyResult: updates.ApplyResult{Accepted: true, Status: updates.Status{CurrentVersion: "1.2.3", Applying: true, ReleasesURL: releases, SelfUpdate: true}},
	}
	server = newUpdatesFixture(t, coord)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	request := "POST /api/v1/updates/apply HTTP/1.1\r\nHost: " + server.Listener.Addr().String() + "\r\nContent-Length: 0\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	select {
	case <-coord.flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("a lost client must not strand the accepted operation")
	}
	if steps := coord.recorded(); len(steps) != 2 || steps[0] != "apply" || steps[1] != "flush" {
		t.Fatalf("handler sequence = %v, want [apply flush]", steps)
	}
}

// sseFrame is one parsed event-stream frame.
type sseFrame struct {
	kind string
	data string
}

func TestSSEEnvelopeCarriesPortalAndUpdateEvents(t *testing.T) {
	fixture := newPortalFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/settings":
			w.Write([]byte(`{"ads":{"enabled":true},"accounts":{"enabled":true}}`))
		case r.URL.Path == "/api/v1/links":
			w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/account/status":
			w.Write([]byte(`{"donor":false}`))
		case r.URL.Path == "/api/v1/ads/weights":
			w.Write([]byte(`[{"provider":"p1","id":"ad1"}]`))
		case r.URL.Path == "/api/v1/ads":
			w.Write([]byte(`[{"provider":"p1","id":"ad1","title":"t","text":"","image":"","screen_time":5}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := fixture.hub.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	fixture.service.PublishEvent("updates.status", updates.Status{CurrentVersion: "1.2.3", ReleasesURL: "https://github.com/mihaiflorentin88/torrent-tv/releases"})

	res, err := http.Get(fixture.server.URL + "/api/v1/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events content type = %q", res.Header.Get("Content-Type"))
	}
	frames := make(chan sseFrame, 8)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(res.Body)
		var frame sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				frame.kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frame.data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if frame.data != "" {
					frames <- frame
				}
				frame = sseFrame{}
			}
		}
	}()

	readFrame := func(what string) sseFrame {
		t.Helper()
		select {
		case frame, open := <-frames:
			if !open {
				t.Fatalf("event stream closed before %s", what)
			}
			return frame
		case <-time.After(5 * time.Second):
			t.Fatalf("event stream never delivered %s", what)
			return sseFrame{}
		}
	}

	// The envelope is {id, kind, payload(string), createdAt}; the payload
	// is the JSON-encoded event body parsed in a second step.
	var envelope struct {
		ID        int64     `json:"id"`
		Kind      string    `json:"kind"`
		Payload   string    `json:"payload"`
		CreatedAt time.Time `json:"createdAt"`
	}
	frame := readFrame("portal.state")
	if frame.kind != "portal.state" {
		t.Fatalf("first event = %q, want portal.state", frame.kind)
	}
	if err := json.Unmarshal([]byte(frame.data), &envelope); err != nil {
		t.Fatalf("envelope decode: %v (%s)", err, frame.data)
	}
	if envelope.Kind != "portal.state" || envelope.Payload == "" || envelope.ID == 0 || envelope.CreatedAt.IsZero() {
		t.Fatalf("envelope = %+v", envelope)
	}
	var snapshot portal.Snapshot
	if err := json.Unmarshal([]byte(envelope.Payload), &snapshot); err != nil {
		t.Fatalf("portal.state payload decode: %v (%s)", err, envelope.Payload)
	}
	if !snapshot.AccountsEnabled || !snapshot.AdsEnabled || snapshot.Links == nil {
		t.Fatalf("portal.state payload = %+v", snapshot)
	}

	frame = readFrame("updates.status")
	if frame.kind != "updates.status" {
		t.Fatalf("second event = %q, want updates.status", frame.kind)
	}
	if err := json.Unmarshal([]byte(frame.data), &envelope); err != nil {
		t.Fatalf("envelope decode: %v (%s)", err, frame.data)
	}
	var status updates.Status
	if err := json.Unmarshal([]byte(envelope.Payload), &status); err != nil {
		t.Fatalf("updates.status payload decode: %v (%s)", err, envelope.Payload)
	}
	if status.CurrentVersion != "1.2.3" || status.ReleasesURL == "" {
		t.Fatalf("updates.status payload = %+v", status)
	}
	res.Body.Close()
}
