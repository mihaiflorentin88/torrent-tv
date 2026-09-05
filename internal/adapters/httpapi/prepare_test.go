package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// The starvation path of ADR-0004 must reach the client as a 409 problem whose
// detail names the exhausted Allocation and the space required.

type prepareGateEngine struct {
	application.TorrentEngine
	mu      sync.Mutex
	status  map[string]domain.DownloadStatus
	removed []string
}

func (e *prepareGateEngine) Status(_ context.Context, hash string) (domain.DownloadStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	status, ok := e.status[hash]
	if !ok {
		return domain.DownloadStatus{}, domain.ErrTorrentNotFound
	}
	return status, nil
}

type prepareGateCatalog struct {
	application.TrackerCatalog
	torrent string
}

func (e *prepareGateEngine) Remove(_ context.Context, hash string, _ bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.status, hash)
	e.removed = append(e.removed, hash)
	return nil
}

func (e *prepareGateEngine) PrepareRange(context.Context, string, int, int64, int64) error {
	return nil
}

func (c prepareGateCatalog) OpenTorrent(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(c.torrent)), nil
}

func newPrepareGateHandler(t *testing.T, engine application.TorrentEngine) http.Handler {
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
	ctx := context.Background()
	release := domain.TorrentRelease{ID: "incoming-release", Name: "New.S01.1080p.WEB-DL", Category: "Series", SizeBytes: 500 << 20, FileCount: 2}
	if err := repo.UpsertReleases(ctx, []domain.TorrentRelease{release, {ID: "stored-release", Name: "Old.S01.1080p.WEB-DL", Category: "Series"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := domain.Download{ID: "leased", ReleaseID: "stored-release", EngineID: "qb:leased", FilePath: "Old.S01E01.mkv", State: "pausedUP", Progress: 1, Leased: true, CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(prepareGateCatalog{torrent: "d4:infod6:lengthi524288000e4:name5:movieee"}, engine, repo, store)
	return New(service, store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
}

func TestPrepareRouteReturnsAllocationProblem(t *testing.T) {
	engine := &prepareGateEngine{status: map[string]domain.DownloadStatus{
		"leased": {Hash: "leased", State: "pausedUP", Progress: 1, TotalBytes: 900 << 20},
	}}
	handler := newPrepareGateHandler(t, engine)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/releases/incoming-release/prepare", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST prepare status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var problem struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Status != http.StatusConflict || !strings.Contains(problem.Detail, "Allocation") {
		t.Fatalf("problem body = %s, want 409 naming the Allocation", rec.Body.String())
	}
}

func TestPrepareSeasonRouteReturnsAllocationProblem(t *testing.T) {
	engine := &prepareGateEngine{status: map[string]domain.DownloadStatus{
		"leased": {Hash: "leased", State: "pausedUP", Progress: 1, TotalBytes: 900 << 20},
	}}
	handler := newPrepareGateHandler(t, engine)
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/releases/incoming-release/prepare-season", strings.NewReader(`{"season":1}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST prepare-season status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Allocation") {
		t.Fatalf("problem body = %s, want the Allocation explanation", rec.Body.String())
	}
}
