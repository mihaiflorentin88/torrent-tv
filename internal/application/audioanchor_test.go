package application

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

type anchorProbeStub struct {
	MediaProbe
	firstMS, lastMS     int64
	gotPath             string
	gotStart, gotLength int64
	gotStreamIndex      int
	gotHeader           int64
}

func (s *anchorProbeStub) AudioSpan(_ context.Context, path string, startByte, lengthBytes int64, streamIndex int, headerByteLength int64) (domain.AudioSpan, error) {
	s.gotPath = path
	s.gotStart = startByte
	s.gotLength = lengthBytes
	s.gotStreamIndex = streamIndex
	s.gotHeader = headerByteLength
	return domain.AudioSpan{StreamIndex: streamIndex, StartByte: startByte, LengthBytes: lengthBytes, FirstPTSMS: s.firstMS, LastPTSMS: s.lastMS}, nil
}

// bareProbeStub intentionally implements only MediaProbe: it must be rejected
// as lacking the audio span capability.
type bareProbeStub struct {
	MediaProbe
}

func newAnchorTestServiceWithRepo(t *testing.T, probe MediaProbe, download domain.Download) (*Service, *sqlite.Repository) {
	t.Helper()
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvironmentPrefix+"DOWNLOAD_ROOT", dir)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	settings, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.Open(filepath.Join(dir, "state.db"))
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
	service := NewService(nil, nil, repo, settings)
	service.SetMediaProbe(probe)
	return service, repo
}

func newAnchorTestService(t *testing.T, probe MediaProbe, download domain.Download) *Service {
	service, _ := newAnchorTestServiceWithRepo(t, probe, download)
	return service
}

func completeDownload() domain.Download {
	return domain.Download{ID: "source", ReleaseID: "release", EngineID: "qb:abc", FileIndex: 0, FilePath: "movie.mkv", State: "complete", Progress: 1, SizeBytes: 5}
}

func TestAudioSpanMeasuresRequestedWindow(t *testing.T) {
	probe := &anchorProbeStub{firstMS: 60000, lastMS: 73892}
	download := completeDownload()
	service := newAnchorTestService(t, probe, download)
	span, retryable, err := service.AudioSpan(context.Background(), download.ID, 2097152, 16777216, 1)
	if retryable {
		t.Fatal("happy path must not be marked retryable")
	}
	if err != nil {
		t.Fatal(err)
	}
	if span.StreamIndex != 1 || span.StartByte != 2097152 || span.LengthBytes != 16777216 {
		t.Fatalf("span identity = %+v, want the requested window echoed", span)
	}
	if span.FirstPTSMS != 60000 || span.LastPTSMS != 73892 {
		t.Fatalf("span = [%d, %d], want the measured PTS values", span.FirstPTSMS, span.LastPTSMS)
	}
	if !strings.HasSuffix(probe.gotPath, "movie.mkv") {
		t.Fatalf("probe path = %q, want the source file", probe.gotPath)
	}
	if probe.gotStart != 2097152 || probe.gotLength != 16777216 || probe.gotStreamIndex != 1 {
		t.Fatalf("probe saw window = %d+%d stream %d", probe.gotStart, probe.gotLength, probe.gotStreamIndex)
	}
	if probe.gotHeader != AudioHeaderBytes {
		t.Fatalf("probe header length = %d, want the decoder head length", probe.gotHeader)
	}
}

func TestAudioSpanValidatesWindowInputs(t *testing.T) {
	cases := []struct {
		name      string
		start     int64
		length    int64
		stream    int
		wantErr   string
		wantStart int64
	}{
		{"negative start", -1, 16777216, 1, "invalid audio window", 0},
		{"zero length", 2097152, 0, 1, "invalid audio window", 0},
		{"negative length", 2097152, -5, 1, "invalid audio window", 0},
		{"oversize length", 2097152, 16777217, 1, "invalid audio window", 0},
		{"negative stream index", 2097152, 16777216, -1, "invalid audio window", 0},
		{"sub-header start normalizes to zero", 1048576, 16777216, 1, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &anchorProbeStub{firstMS: 60000, lastMS: 73892}
			download := completeDownload()
			service, repo := newAnchorTestServiceWithRepo(t, probe, download)
			span, _, err := service.AudioSpan(context.Background(), download.ID, tc.start, tc.length, tc.stream)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				if state, getErr := repo.GetDownload(context.Background(), download.ID); getErr == nil && state.Leased {
					t.Fatal("lease taken despite invalid input")
				}
				if probe.gotPath != "" {
					t.Fatal("probe called despite invalid input")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if span.StartByte != tc.wantStart || probe.gotStart != tc.wantStart {
				t.Fatalf("start = span %d / probe %d, want normalized %d", span.StartByte, probe.gotStart, tc.wantStart)
			}
		})
	}
}

func TestAudioSpanRejectsPartialAsRetryable(t *testing.T) {
	probe := &anchorProbeStub{firstMS: 60000, lastMS: 73892}
	download := completeDownload()
	download.Progress = 0
	service := newAnchorTestService(t, probe, download)
	_, retryable, err := service.AudioSpan(context.Background(), download.ID, 2097152, 16777216, 1)
	if err == nil || !retryable || !strings.Contains(err.Error(), "audio span is not readable yet") {
		t.Fatalf("err = %v retryable = %v, want retryable not-readable-yet error", err, retryable)
	}
}

func TestAudioSpanRequiresCapability(t *testing.T) {
	download := completeDownload()
	service := newAnchorTestService(t, &bareProbeStub{}, download)
	if _, retryable, err := service.AudioSpan(context.Background(), download.ID, 2097152, 16777216, 1); err == nil || retryable || !strings.Contains(err.Error(), "audio span probing is unavailable") {
		t.Fatalf("err = %v, want non-retryable capability error", err)
	}
}
