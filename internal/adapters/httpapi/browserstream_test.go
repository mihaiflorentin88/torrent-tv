package httpapi

import (
	"strings"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

func browserStreamFixture() domain.MediaInfo {
	return domain.MediaInfo{
		DurationMS: 3_113_728,
		AudioTracks: []domain.MediaAudioTrack{
			{Index: 1, Language: "jpn", Codec: "eac3", Channels: 6, Default: true},
			{Index: 2, Language: "eng", Codec: "ac3", Channels: 2},
		},
	}
}

func TestBrowserStreamArgsTranscodesSelectedAudioOnly(t *testing.T) {
	args, track, err := browserStreamArgs("http://127.0.0.1:8097/api/v1/streams/x", browserStreamFixture(), "", "")
	if err != nil {
		t.Fatalf("browserStreamArgs returned error: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-c:v copy", "-c:a aac", "-map 0:v:0", "-map 0:2", "-copypriorss 0", "-movflags frag_keyframe+empty_moov+default_base_moof", "-f mp4 pipe:1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-ss") {
		t.Fatalf("no -ss expected without startMs: %s", joined)
	}
	if track.Index != 2 {
		t.Fatalf("expected English track selection, got index %d", track.Index)
	}
}

func TestBrowserStreamArgsExplicitTrackAndStart(t *testing.T) {
	args, track, err := browserStreamArgs("http://127.0.0.1:8097/api/v1/streams/x", browserStreamFixture(), "1", "61000")
	if err != nil {
		t.Fatalf("browserStreamArgs returned error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:1") {
		t.Fatalf("explicit track not mapped: %s", joined)
	}
	if track.Index != 1 {
		t.Fatalf("explicit track ignored, got index %d", track.Index)
	}
	ss := strings.Index(joined, "-ss ")
	if ss < 0 {
		t.Fatalf("missing -ss with startMs: %s", joined)
	}
	if !strings.HasPrefix(joined[ss:], "-ss 61.000") {
		t.Fatalf("unexpected seek value: %s", joined[ss:ss+16])
	}
}

func TestBrowserStreamArgsRejectsBadTrackAndStart(t *testing.T) {
	if _, _, err := browserStreamArgs("x", browserStreamFixture(), "9", ""); err == nil {
		t.Fatal("expected error for unknown audioTrack")
	}
	if _, _, err := browserStreamArgs("x", browserStreamFixture(), "-3", ""); err == nil {
		t.Fatal("expected error for negative audioTrack")
	}
	if _, _, err := browserStreamArgs("x", browserStreamFixture(), "", "999999999"); err == nil {
		t.Fatal("expected error for startMs beyond duration")
	}
	if _, _, err := browserStreamArgs("x", browserStreamFixture(), "", "abc"); err == nil {
		t.Fatal("expected error for non-numeric startMs")
	}
	empty := domain.MediaInfo{AudioTracks: []domain.MediaAudioTrack{}}
	if _, _, err := browserStreamArgs("x", empty, "", ""); err == nil {
		t.Fatal("expected error for media without audio tracks")
	}
}
