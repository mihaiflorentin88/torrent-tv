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
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// streamEngine fakes the torrent engine boundary for progressive-stream
// handler tests: a 200-piece file whose leading pieces are downloaded and the
// rest still missing, exactly the in-flight state a player meets while a
// torrent is downloading.
type streamEngine struct {
	dir    string
	status domain.DownloadStatus
}

func (e *streamEngine) Test(context.Context) (string, error) { return "", nil }
func (e *streamEngine) Add(context.Context, io.Reader, string) (string, error) {
	return "", nil
}

func (e *streamEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	return []domain.TorrentFile{{Index: 0, Path: "movie.mkv", SizeBytes: 200 << 20, Playable: true}}, nil
}

func (e *streamEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	return e.status, nil
}

func (e *streamEngine) Pieces(context.Context, string) (domain.PieceMap, error) {
	states := make([]int, 200)
	states[0], states[1] = 2, 2 // only the first 2 MiB has arrived
	return domain.PieceMap{PieceSize: 1 << 20, States: states}, nil
}
func (e *streamEngine) PrepareFile(context.Context, string, int, []int) error { return nil }
func (e *streamEngine) PrepareFiles(context.Context, string, []int, []int) error {
	return nil
}

func (e *streamEngine) PrepareRange(context.Context, string, int, int64, int64) error {
	return nil
}
func (e *streamEngine) Pause(context.Context, string) error  { return nil }
func (e *streamEngine) Resume(context.Context, string) error { return nil }
func (e *streamEngine) Remove(context.Context, string, bool) error {
	return nil
}

func newStreamHTTPTest(t *testing.T, engine *streamEngine, download domain.Download) http.Handler {
	t.Helper()
	dir := t.TempDir()
	tempDir := filepath.Join(dir, ".incomplete")
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	engine.status.TempPath = tempDir
	settingsFile := filepath.Join(dir, "settings.json")
	b, err := json.Marshal(map[string]any{
		"databasePath":            filepath.Join(dir, "test.db"),
		"downloadRoot":            dir,
		"trustedCidrs":            []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "192.0.2.0/24"},
		"pieceWaitTimeoutSeconds": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvironmentPrefix+"SETTINGS_PATH", settingsFile)
	t.Setenv(config.EnvironmentPrefix+"DOWNLOAD_ROOT", dir)
	store, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	// Sparse 200 MiB media file; only the leading 2 MiB carries a pattern.
	media := filepath.Join(tempDir, "movie.mkv")
	f, err := os.OpenFile(media, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	pattern := make([]byte, 2<<20)
	for i := range pattern {
		pattern[i] = byte(0x41 + i%26)
	}
	if _, err := f.WriteAt(pattern, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0x00}, (200<<20)-1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	download.AbsolutePath = media
	ctx := context.Background()
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(nil, engine, repo, store)
	return New(service, store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
}

func TestStreamRespondsBeforeFullStartupBuffer(t *testing.T) {
	dir := t.TempDir()
	engine := &streamEngine{status: domain.DownloadStatus{
		State: "downloading", Progress: 0.05, Sequential: true, FirstLastPriority: true,
		TempPathEnabled: true, TempPath: dir, SavePath: dir, PieceSize: 1 << 20,
	}}
	download := domain.Download{
		ID: "source", ReleaseID: "release", EngineID: "qb:abc", FileIndex: 0,
		FilePath: "movie.mkv", State: "downloading", Progress: 0.05, SizeBytes: 200 << 20,
	}
	handler := newStreamHTTPTest(t, engine, download)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/streams/source", nil)
	req.Header.Set("Range", "bytes=0-")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The player sent an open-ended range while only the first 2 MiB of the
	// torrent is present. The stream must commit headers and serve the
	// readable leading slice instead of waiting for the whole startup buffer
	// (which no player's patience outlives on a slow swarm).
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d body = %s, want 206", rec.Code, rec.Body.String())
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 2<<20 {
		t.Fatalf("served %d bytes, want at least the 2 MiB leading slice", len(body))
	}
	want := make([]byte, 2<<20)
	for i := range want {
		want[i] = byte(0x41 + i%26)
	}
	if string(body[:2<<20]) != string(want) {
		t.Fatal("served body does not match the media leading slice")
	}
}

func TestStreamServesSmallRangesImmediately(t *testing.T) {
	dir := t.TempDir()
	engine := &streamEngine{status: domain.DownloadStatus{
		State: "downloading", Progress: 0.05, Sequential: true, FirstLastPriority: true,
		TempPathEnabled: true, TempPath: dir, SavePath: dir, PieceSize: 1 << 20,
	}}
	download := domain.Download{
		ID: "source", ReleaseID: "release", EngineID: "qb:abc", FileIndex: 0,
		FilePath: "movie.mkv", State: "downloading", Progress: 0.05, SizeBytes: 200 << 20,
	}
	handler := newStreamHTTPTest(t, engine, download)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/streams/source", nil)
	req.Header.Set("Range", "bytes=0-1048575")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 1<<20 {
		t.Fatalf("served %d bytes, want exactly 1 MiB", len(body))
	}
}
