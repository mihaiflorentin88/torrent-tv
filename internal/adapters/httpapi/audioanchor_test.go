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

type anchorHTTPProbe struct {
	application.MediaProbe
	span domain.AudioSpan
}

func (p *anchorHTTPProbe) AudioSpan(context.Context, string, int64, int64, int, int64) (domain.AudioSpan, error) {
	return p.span, nil
}

func anchorCompleteDownload() domain.Download {
	return domain.Download{ID: "source", ReleaseID: "release", EngineID: "qb:abc", FileIndex: 0, FilePath: "movie.mkv", State: "complete", Progress: 1, SizeBytes: 5}
}

func newAnchorHTTPTest(t *testing.T, probe application.MediaProbe, download domain.Download) http.Handler {
	t.Helper()
	dir := t.TempDir()
	settingsFile := filepath.Join(dir, "settings.json")
	b, err := json.Marshal(map[string]any{
		"databasePath": filepath.Join(dir, "test.db"),
		"downloadRoot": dir,
		"trustedCidrs": []string{"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "192.0.2.0/24"},
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
	media := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	download.AbsolutePath = media
	ctx := context.Background()
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(nil, nil, repo, store)
	service.SetMediaProbe(probe)
	return New(service, store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
}

func TestAudioAnchorRoute(t *testing.T) {
	span := domain.AudioSpan{StreamIndex: 1, StartByte: 2097152, LengthBytes: 16777216, FirstPTSMS: 60000, LastPTSMS: 73892}
	handler := newAnchorHTTPTest(t, &anchorHTTPProbe{span: span}, anchorCompleteDownload())

	t.Run("measures the requested window", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/downloads/source/audio-anchor?startByte=2097152&lengthBytes=16777216&streamIndex=1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		var got domain.AudioSpan
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got != span {
			t.Fatalf("span = %+v, want %+v", got, span)
		}
	})

	t.Run("unknown source is gone", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/downloads/missing/audio-anchor?startByte=2097152&lengthBytes=16777216&streamIndex=1", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("partial source is retryable", func(t *testing.T) {
		partial := anchorCompleteDownload()
		partial.ID = "partial"
		partial.Progress = 0
		partialHandler := newAnchorHTTPTest(t, &anchorHTTPProbe{span: span}, partial)
		rec := httptest.NewRecorder()
		partialHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/downloads/partial/audio-anchor?startByte=2097152&lengthBytes=16777216&streamIndex=1", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if rec.Header().Get("Retry-After") == "" {
			t.Fatal("503 response lost Retry-After")
		}
	})

	t.Run("invalid window is unprocessable", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/downloads/source/audio-anchor?startByte=-1&lengthBytes=16777216&streamIndex=1", nil))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
	})

	t.Run("non-numeric query is bad request", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/downloads/source/audio-anchor?startByte=abc&lengthBytes=16777216&streamIndex=1", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}
