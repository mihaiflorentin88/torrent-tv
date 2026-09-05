package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// snapFixture builds a 12s synthetic MKV with keyframes every 2s and returns
// its path; the test skips when ffmpeg is unavailable.
func snapFixture(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
	path := filepath.Join(t.TempDir(), "snap.mkv")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=duration=12:size=128x72:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=12",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-g", "48", "-keyint_min", "48",
		"-c:a", "aac",
		path,
	).CombinedOutput()
	if err != nil {
		t.Skipf("fixture encode failed: %v: %s", err, out)
	}
	return path
}

func TestSnapStartToVideoKeyframe(t *testing.T) {
	path := snapFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// 5s target snaps back onto the 4s keyframe; sync demands both streams
	// start on the same content point, and copied video cannot start mid-GOP.
	if got, ok := snapStartToVideoKeyframe(context.Background(), "ffprobe", path, 5_000, log); got != 4_000 || !ok {
		t.Fatalf("snapped start = %dms ok=%v, want 4000ms true", got, ok)
	}
	// A target on the keyframe stays put; zero stays zero.
	if got, _ := snapStartToVideoKeyframe(context.Background(), "ffprobe", path, 8_000, log); got != 8_000 {
		t.Fatalf("snapped start = %dms, want 8000ms", got)
	}
	if got, ok := snapStartToVideoKeyframe(context.Background(), "ffprobe", path, 0, log); got != 0 || ok {
		t.Fatalf("snapped start = %dms ok=%v, want 0 false", got, ok)
	}
	// A broken probe degrades to the raw target instead of blocking playback.
	if got, ok := snapStartToVideoKeyframe(context.Background(), "ffprobe", filepath.Join(t.TempDir(), "missing.mkv"), 5_000, log); got != 5_000 || ok {
		t.Fatalf("probe failure should fall back to target, got %dms ok=%v", got, ok)
	}
}

func TestParseStartQuery(t *testing.T) {
	if got := parseStartQuery("", 100_000); got != 0 {
		t.Fatalf("empty query = %d, want 0", got)
	}
	if got := parseStartQuery("61500", 100_000); got != 61500 {
		t.Fatalf("query = %d, want 61500", got)
	}
	if got := parseStartQuery("999999", 100_000); got != 0 {
		t.Fatalf("beyond duration = %d, want 0", got)
	}
	if got := parseStartQuery("abc", 100_000); got != 0 {
		t.Fatalf("non-numeric = %d, want 0", got)
	}
	if got := parseStartQuery("-5", 100_000); got != 0 {
		t.Fatalf("negative = %d, want 0", got)
	}
}

func TestStreamSnapEndpointFallsBackToTarget(t *testing.T) {
	handler := newStreamHTTPTest(t, &streamEngine{status: domain.DownloadStatus{
		State: "downloading", Progress: 0.05, Sequential: true, FirstLastPriority: true,
		TempPathEnabled: true, TempPath: t.TempDir(), SavePath: t.TempDir(), PieceSize: 1 << 20,
	}}, domain.Download{
		ID: "source", ReleaseID: "release", EngineID: "qb:abc", FileIndex: 0,
		FilePath: "movie.mkv", State: "downloading", Progress: 0.05, SizeBytes: 200 << 20,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/streams/source/snap?startMs=61000", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("snap status = %d, want 200", rec.Code)
	}
	var payload struct {
		Requested int64 `json:"requested"`
		StartMs   int64 `json:"startMs"`
		Snapped   bool  `json:"snapped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("snap payload: %v", err)
	}
	// The harness media file is not a real container: the probe degrades and
	// the endpoint must still answer with a usable, unsnapped start.
	if payload.Requested != 61000 || payload.StartMs != 61000 || payload.Snapped {
		t.Fatalf("snap payload = %+v, want requested=startMs=61000 snapped=false", payload)
	}
}
