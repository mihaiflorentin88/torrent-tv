package mediaprobe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// generateSineFixture produces a long audio-only Matroska so that windows can
// start beyond the decoder's container head length.
func generateSineFixture(t *testing.T, dir string) string {
	t.Helper()
	store, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(store.Get().FFmpegPath, "-version").Run(); err != nil {
		t.Skip("ffmpeg unavailable on this machine")
	}
	src := filepath.Join(dir, "sine.mkv")
	generate := exec.Command(store.Get().FFmpegPath,
		"-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=600",
		"-c:a", "ac3", "-b:a", "96k", src)
	if out, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("fixture generation failed: %v %s", err, out)
	}
	return src
}

// independentArtifact builds head + window bytes itself and measures them
// with its own ffprobe run, so the assertion never reuses the adapter's
// internal artifact or classification input.
func independentArtifact(t *testing.T, store *config.Store, src string, start, length, headerBytes int64, stream int) (int64, int64) {
	t.Helper()
	head, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(head)) < start+length {
		t.Fatalf("fixture too small: %d bytes", len(head))
	}
	artifact := head[:headerBytes]
	artifact = append(artifact, head[start:start+length]...)
	path := filepath.Join(t.TempDir(), "artifact.mkv")
	if err := os.WriteFile(path, artifact, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(store.Get().FFprobePath,
		"-v", "error", "-select_streams", "a",
		"-show_entries", "packet=stream_index,pts_time,pos",
		"-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	packets, err := decodeAnchorPackets(out)
	if err != nil {
		t.Fatal(err)
	}
	first, last, ok := anchorSpan(packets, stream, headerBytes)
	if !ok {
		t.Fatal("independent artifact yielded no window audio")
	}
	return first, last
}

func TestAudioSpanMeasuresArtifactOnGeneratedFixture(t *testing.T) {
	dir := t.TempDir()
	src := generateSineFixture(t, dir)
	store, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	adapter := New(store)

	const (
		start       = int64(3) << 20
		length      = int64(1) << 20
		headerBytes = int64(2) << 20
		stream      = 0
	)
	span, err := adapter.AudioSpan(context.Background(), src, start, length, stream, headerBytes)
	if err != nil {
		t.Fatal(err)
	}
	if span.StartByte != start || span.LengthBytes != length || span.StreamIndex != stream {
		t.Fatalf("span identity = %+v, want the requested window echoed", span)
	}
	if span.FirstPTSMS <= 0 || span.LastPTSMS <= span.FirstPTSMS {
		t.Fatalf("span = [%d, %d], want a positive measured span", span.FirstPTSMS, span.LastPTSMS)
	}

	// Independent truth: the same artifact, rebuilt and measured separately.
	// Note the artifact's first content PTS legitimately differs from any
	// original-file packet position: the demuxer drops unparseable frames at
	// the head/slice seam, which is why the artifact is measured at all.
	first, last := independentArtifact(t, store, src, start, length, headerBytes, stream)
	if span.FirstPTSMS != first || span.LastPTSMS != last {
		t.Fatalf("measured span = [%d, %d], want the independently measured [%d, %d]", span.FirstPTSMS, span.LastPTSMS, first, last)
	}
}
