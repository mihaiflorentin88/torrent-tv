package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// The tracker boundary contract: persisted provenance survives a disabled
// provider, preparation failures are actionable problems rather than
// gateway noise, a metadata deadline is a 504, and a cancelled request
// never persists a failed or dead release.

type stubTracker struct {
	application.Tracker
	id, name    string
	enabled     bool
	configured  bool
	releases    []domain.TorrentRelease
	acquisition domain.TorrentAcquisition
	latestCalls int
}

func (s *stubTracker) ID() string   { return s.id }
func (s *stubTracker) Name() string { return s.name }
func (s *stubTracker) Capabilities() application.TrackerCapabilities {
	return application.TrackerCapabilities{IMDbSearch: s.id == "filelist", Categories: true}
}
func (s *stubTracker) Categories() []domain.TrackerCategory { return nil }
func (s *stubTracker) Latest(context.Context) ([]domain.TorrentRelease, error) {
	s.latestCalls++
	return s.releases, nil
}

func (s *stubTracker) Acquire(context.Context, string) (domain.TorrentAcquisition, error) {
	return s.acquisition, nil
}

// unusedEngine must never be reached: any call panics through the nil
// embedded interface instead of silently returning empty successes.
type unusedEngine struct{ application.TorrentEngine }

type downloadStatusEngine struct{ application.TorrentEngine }

func (downloadStatusEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	return domain.DownloadStatus{State: "pausedUP", Progress: 1}, nil
}

func (downloadStatusEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	return nil, nil
}

// magnetStubEngine resolves magnets through the test-provided function.
type magnetStubEngine struct {
	application.TorrentEngine
	resolve func(context.Context, string, string) ([]byte, error)
}

func (e *magnetStubEngine) ResolveMagnet(ctx context.Context, uri, root string) ([]byte, error) {
	return e.resolve(ctx, uri, root)
}

// blockingFilesEngine adds one torrent and then blocks in Files until the
// request context is cancelled, mirroring a metadata wait the client aborts.
type blockingFilesEngine struct {
	application.TorrentEngine
	entered chan struct{}
}

func (e *blockingFilesEngine) Add(context.Context, io.Reader, string) (string, error) {
	return "blockinghash", nil
}

func (e *blockingFilesEngine) Files(ctx context.Context, _ string) ([]domain.TorrentFile, error) {
	select {
	case e.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type trackerFixture struct {
	handler http.Handler
	service *application.Service
	repo    *sqlite.Repository
}

func newTrackerFixture(t *testing.T, engine application.TorrentEngine, registrations ...application.TrackerRegistration) trackerFixture {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(map[string]any{
		"databasePath": filepath.Join(dir, "test.db"),
		"downloadRoot": filepath.Join(dir, "downloads"),
		"trustedCidrs": []string{"127.0.0.0/8", "::1/128", "192.0.2.0/24"},
		"allocationGb": 1.0,
		"reserveGb":    0.0,
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
	repo, err := sqlite.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	registry, err := application.NewTrackerRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewService(registry, engine, repo, store)
	return trackerFixture{
		handler: New(service, store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test"),
		service: service,
		repo:    repo,
	}
}

func filelistRegistration(tracker *stubTracker) application.TrackerRegistration {
	return application.TrackerRegistration{
		Adapter:    tracker,
		Enabled:    func() bool { return tracker.enabled },
		Configured: func() bool { return tracker.configured },
	}
}

func seedProvenanceRelease(t *testing.T, f trackerFixture, release domain.TorrentRelease) {
	t.Helper()
	if _, err := f.repo.UpsertReleases(context.Background(), []domain.TorrentRelease{release}); err != nil {
		t.Fatal(err)
	}
}

func seedProvenanceDownload(t *testing.T, f trackerFixture, download domain.Download) {
	t.Helper()
	now := time.Now().UTC()
	download.CreatedAt, download.UpdatedAt = now, now
	if err := f.repo.SaveDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}
}

type wireProblem struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func decodeProblem(t *testing.T, body []byte) wireProblem {
	t.Helper()
	var p wireProblem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("problem body %s: %v", body, err)
	}
	return p
}

// A Downloads response shows the persisted tracker of every row, including
// rows whose provider is disabled and migrated rows: labels come from the
// database, never from a FileList fallback.
func TestDisabledTrackerDownloadsKeepTrackerLabels(t *testing.T) {
	filelist := &stubTracker{id: "filelist", name: "FileList", enabled: true, configured: true}
	piratebay := &stubTracker{id: "piratebay", name: "The Pirate Bay", enabled: false, configured: true}
	f := newTrackerFixture(t, downloadStatusEngine{}, filelistRegistration(filelist), filelistRegistration(piratebay))
	seedProvenanceRelease(t, f, domain.TorrentRelease{ID: "filelist:10", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "10", Name: "Alpha.2024.1080p", Category: "Movies HD", CategoryID: "4", BrowseClass: "video", Seeders: 3})
	seedProvenanceRelease(t, f, domain.TorrentRelease{ID: "piratebay:20", TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "20", Name: "Beta.2024.1080p", Category: "Video", CategoryID: "201", BrowseClass: "video", Seeders: 7})
	seedProvenanceDownload(t, f, domain.Download{ID: "dl-fl", ReleaseID: "filelist:10", EngineID: "qb:fl", FilePath: "Alpha.2024.1080p.mkv", State: "pausedUP", Progress: 1, TrackerID: "filelist", TrackerName: "FileList"})
	seedProvenanceDownload(t, f, domain.Download{ID: "dl-pb", ReleaseID: "piratebay:20", EngineID: "qb:pb", FilePath: "Beta.2024.1080p.mkv", State: "downloading", TrackerID: "piratebay", TrackerName: "The Pirate Bay"})

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET downloads status = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []domain.Download `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	byID := map[string]domain.Download{}
	for _, item := range page.Items {
		byID[item.ID] = item
	}
	for id, wantTracker, wantName := "dl-fl", "filelist", "FileList"; ; {
		got, ok := byID[id]
		if !ok {
			t.Fatalf("download %q missing from response: %s", id, rec.Body.String())
		}
		if got.TrackerID != wantTracker || got.TrackerName != wantName {
			t.Fatalf("download %q provenance = %q/%q, want %q/%q", id, got.TrackerID, got.TrackerName, wantTracker, wantName)
		}
		if id == "dl-fl" {
			id, wantTracker, wantName = "dl-pb", "piratebay", "The Pirate Bay"
			continue
		}
		break
	}
}

// A new preparation for a disabled tracker is refused with an actionable
// 409, not a gateway failure.
func TestTrackerDisabledPrepareReturnsActionableConflict(t *testing.T) {
	piratebay := &stubTracker{id: "piratebay", name: "The Pirate Bay", enabled: false, configured: true}
	f := newTrackerFixture(t, unusedEngine{}, filelistRegistration(piratebay))
	seedProvenanceRelease(t, f, domain.TorrentRelease{ID: "piratebay:20", TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "20", Name: "Beta.2024.1080p", Category: "Video", CategoryID: "201", BrowseClass: "video", SizeBytes: 1 << 20})

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/releases/piratebay:20/prepare", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("prepare status = %d: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec.Body.Bytes())
	if p.Status != http.StatusConflict || !strings.Contains(p.Detail, "disabled") {
		t.Fatalf("problem = %+v, want an actionable disabled-tracker 409", p)
	}
}

// An unconfigured tracker refuses new preparation with configuration
// guidance.
func TestTrackerUnconfiguredPrepareReturnsConfigurationGuidance(t *testing.T) {
	piratebay := &stubTracker{id: "piratebay", name: "The Pirate Bay", enabled: true, configured: false}
	f := newTrackerFixture(t, unusedEngine{}, filelistRegistration(piratebay))
	seedProvenanceRelease(t, f, domain.TorrentRelease{ID: "piratebay:20", TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "20", Name: "Beta.2024.1080p", Category: "Video", CategoryID: "201", BrowseClass: "video", SizeBytes: 1 << 20})

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/releases/piratebay:20/prepare", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("prepare status = %d: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec.Body.Bytes())
	if p.Status != http.StatusConflict || !strings.Contains(p.Detail, "not configured") {
		t.Fatalf("problem = %+v, want configuration guidance in a 409", p)
	}
}

// A magnet the engine cannot honor returns the upgrade/native guidance as
// a 409.
func TestMagnetUnsupportedPrepareReturnsUpgradeGuidance(t *testing.T) {
	piratebay := &stubTracker{
		id: "piratebay", name: "The Pirate Bay", enabled: true, configured: true,
		acquisition: domain.TorrentAcquisition{Magnet: "magnet:?xt=urn:btih:0123456789012345678901234567890123456789"},
	}
	engine := &magnetStubEngine{resolve: func(context.Context, string, string) ([]byte, error) {
		return nil, fmt.Errorf("%w: daemon reports %q", domain.ErrMagnetUnsupported, "v4.3.9")
	}}
	f := newTrackerFixture(t, engine, filelistRegistration(piratebay))
	seedProvenanceRelease(t, f, domain.TorrentRelease{ID: "piratebay:20", TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "20", Name: "Beta.2024.1080p", Category: "Video", CategoryID: "201", BrowseClass: "video", SizeBytes: 1 << 20})

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/releases/piratebay:20/prepare", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("prepare status = %d: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec.Body.Bytes())
	if p.Status != http.StatusConflict || !strings.Contains(p.Detail, "4.5.0") {
		t.Fatalf("problem = %+v, want the upgrade-or-native guidance in a 409", p)
	}
}

// A metadata deadline is a 504 gateway timeout, never a removed-release
// response.
func TestDownloadMetadataDeadlineReturnsGatewayTimeout(t *testing.T) {
	piratebay := &stubTracker{
		id: "piratebay", name: "The Pirate Bay", enabled: true, configured: true,
		acquisition: domain.TorrentAcquisition{Magnet: "magnet:?xt=urn:btih:0123456789012345678901234567890123456789"},
	}
	engine := &magnetStubEngine{resolve: func(ctx context.Context, _, _ string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}}
	f := newTrackerFixture(t, engine, filelistRegistration(piratebay))
	seedProvenanceRelease(t, f, domain.TorrentRelease{ID: "piratebay:20", TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "20", Name: "Beta.2024.1080p", Category: "Video", CategoryID: "201", BrowseClass: "video", SizeBytes: 1 << 20})

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/releases/piratebay:20/prepare", nil))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("prepare status = %d: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec.Body.Bytes())
	if p.Status != http.StatusGatewayTimeout {
		t.Fatalf("problem = %+v, want a 504", p)
	}
	if strings.Contains(p.Detail, "no longer available") {
		t.Fatalf("problem = %+v, a metadata deadline must not read as a removed release", p)
	}
}

// A cancelled request must not persist a failed download or remove the
// release as dead.
func TestDownloadCancelledPrepareDoesNotPersistFailedRelease(t *testing.T) {
	filelist := &stubTracker{
		id: "filelist", name: "FileList", enabled: true, configured: true,
		acquisition: domain.TorrentAcquisition{Metainfo: []byte("d4:infod6:lengthi1e4:name4:movieee")},
	}
	engine := &blockingFilesEngine{entered: make(chan struct{}, 1)}
	f := newTrackerFixture(t, engine, filelistRegistration(filelist))
	seedProvenanceRelease(t, f, domain.TorrentRelease{ID: "filelist:10", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "10", Name: "Alpha.2024.1080p", Category: "Movies HD", CategoryID: "4", BrowseClass: "video", SizeBytes: 1 << 20})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/releases/filelist:10/prepare", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		f.handler.ServeHTTP(rec, request)
		close(done)
	}()
	select {
	case <-engine.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("prepare never reached the metadata wait")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("prepare did not observe the cancellation")
	}
	if rec.Code == http.StatusOK || rec.Code == http.StatusAccepted {
		t.Fatalf("cancelled prepare status = %d: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec.Body.Bytes())
	if strings.Contains(p.Detail, "no longer available") {
		t.Fatalf("problem = %+v, a cancelled request must not read as a removed release", p)
	}
	if _, err := f.repo.GetRelease(context.Background(), "filelist:10"); err != nil {
		t.Fatalf("cancelled prepare removed the release: %v", err)
	}
	downloads, err := f.repo.ListDownloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, download := range downloads {
		if download.ReleaseID == "filelist:10" {
			t.Fatalf("cancelled prepare persisted a download row: %+v", download)
		}
	}
}

// GET /trackers is a read-only registry snapshot: flags and capabilities,
// never credentials.
func TestTrackersEndpointReturnsStatuses(t *testing.T) {
	filelist := &stubTracker{id: "filelist", name: "FileList", enabled: true, configured: true}
	piratebay := &stubTracker{id: "piratebay", name: "The Pirate Bay", enabled: false, configured: false}
	f := newTrackerFixture(t, unusedEngine{}, filelistRegistration(filelist), filelistRegistration(piratebay))

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/trackers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET trackers status = %d: %s", rec.Code, rec.Body.String())
	}
	var statuses []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		Enabled      bool   `json:"enabled"`
		Configured   bool   `json:"configured"`
		Capabilities struct {
			IMDbSearch    bool `json:"imdbSearch"`
			SeasonFilter  bool `json:"seasonFilter"`
			EpisodeFilter bool `json:"episodeFilter"`
			Categories    bool `json:"categories"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &statuses); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses = %+v, want both registered trackers", statuses)
	}
	if statuses[0].ID != "filelist" || statuses[1].ID != "piratebay" {
		t.Fatalf("statuses out of registration order: %+v", statuses)
	}
	if !statuses[0].Enabled || !statuses[0].Configured {
		t.Fatalf("filelist flags = %+v, want enabled and configured", statuses[0])
	}
	if statuses[1].Enabled || statuses[1].Configured {
		t.Fatalf("piratebay flags = %+v, want disabled and unconfigured", statuses[1])
	}
	if !statuses[0].Capabilities.IMDbSearch || !statuses[0].Capabilities.Categories {
		t.Fatalf("filelist capabilities = %+v", statuses[0].Capabilities)
	}
	if statuses[0].Capabilities.SeasonFilter || statuses[0].Capabilities.EpisodeFilter {
		t.Fatalf("filelist capabilities = %+v, want unset filters false", statuses[0].Capabilities)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "passkey") || strings.Contains(strings.ToLower(rec.Body.String()), "username") {
		t.Fatalf("tracker statuses leaked credential fields: %s", rec.Body.String())
	}
}

// Registered tracker IDs route through the generic connection test: a
// disabled-but-configured provider is testable, the probe is read-only,
// and the response shape matches the other dependencies.
func TestTrackerDependencyTestRoutesRegisteredIDs(t *testing.T) {
	piratebay := &stubTracker{
		id: "piratebay", name: "The Pirate Bay", enabled: false, configured: true,
		releases: []domain.TorrentRelease{{}, {}, {}},
	}
	filelist := &stubTracker{
		id: "filelist", name: "FileList", enabled: true, configured: true,
		releases: []domain.TorrentRelease{{}},
	}
	f := newTrackerFixture(t, unusedEngine{}, filelistRegistration(filelist), filelistRegistration(piratebay))

	post := func(name string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/dependencies/"+name+"/test", nil))
		return rec
	}

	pb := post("piratebay")
	if pb.Code != http.StatusOK {
		t.Fatalf("piratebay test status = %d: %s", pb.Code, pb.Body.String())
	}
	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Count   int    `json:"count"`
	}
	if err := json.Unmarshal(pb.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Count != 3 || !strings.Contains(result.Message, "The Pirate Bay") {
		t.Fatalf("piratebay test result = %+v, want success naming the tracker with a count", result)
	}
	if piratebay.latestCalls != 1 {
		t.Fatalf("piratebay Latest calls = %d, want exactly one bounded probe", piratebay.latestCalls)
	}

	fl := post("filelist")
	if fl.Code != http.StatusOK {
		t.Fatalf("filelist test status = %d: %s", fl.Code, fl.Body.String())
	}
	if !strings.Contains(fl.Body.String(), `"message":"Connected to FileList"`) || !strings.Contains(fl.Body.String(), `"count":1`) {
		t.Fatalf("filelist test result = %s, want the established shape and wording", fl.Body.String())
	}

	// The probe never ingests releases, even with an explicit eligible set.
	page, err := f.repo.ListReleases(context.Background(), "", "", 20, 0, []string{"filelist", "piratebay"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("connection test upserted %d releases", page.Total)
	}

	// An unconfigured provider cannot be probed.
	piratebay.configured = false
	rec := post("piratebay")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unconfigured piratebay test status = %d: %s", rec.Code, rec.Body.String())
	}
	p := decodeProblem(t, rec.Body.Bytes())
	if !strings.Contains(p.Detail, "not configured") {
		t.Fatalf("problem = %+v, want configuration guidance", p)
	}

	// Unknown dependency names still answer 404.
	if rec := post("nowhere"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown dependency status = %d, want 404", rec.Code)
	}
}

var _ = errors.Is // placate imports if an assertion path stops needing them
