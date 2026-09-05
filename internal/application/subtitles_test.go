package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

func TestUnpackSubtitleAcceptsPlainTextMislabeledAsZip(t *testing.T) {
	data := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	got, format, err := unpackSubtitle(data, ".zip", "provider.zip", "Movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if format != ".srt" || !strings.Contains(string(got), "Hello") {
		t.Fatalf("format=%q data=%q", format, got)
	}
}

func TestParseSubtitleSearchScope(t *testing.T) {
	tests := []struct {
		input string
		want  SubtitleSearchScope
		err   bool
	}{
		{input: "", want: SubtitleScopeAll},
		{input: " LOCAL ", want: SubtitleScopeLocal},
		{input: "remote", want: SubtitleScopeRemote},
		{input: "all", want: SubtitleScopeAll},
		{input: "provider", err: true},
	}
	for _, test := range tests {
		got, err := ParseSubtitleSearchScope(test.input)
		if (err != nil) != test.err {
			t.Fatalf("ParseSubtitleSearchScope(%q) error = %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("ParseSubtitleSearchScope(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

type subtitleEngineStub struct {
	TorrentEngine
	files []domain.TorrentFile
}

func (e *subtitleEngineStub) Files(context.Context, string) ([]domain.TorrentFile, error) {
	return e.files, nil
}

type subtitleProbeStub struct {
	MediaProbe
	tracks  []domain.MediaSubtitleTrack
	content string
}

func (p *subtitleProbeStub) ProbeSubtitles(context.Context, string) ([]domain.MediaSubtitleTrack, error) {
	return p.tracks, nil
}

func (p *subtitleProbeStub) ExtractSubtitle(_ context.Context, _ string, _ int, target string) error {
	return os.WriteFile(target, []byte(p.content), 0o600)
}

type subtitleProviderStub struct {
	SubtitleProvider
	items []domain.SubtitleCandidate
}

func (p *subtitleProviderStub) Name() string { return "subdl" }

func (p *subtitleProviderStub) Search(context.Context, SubtitleQuery) ([]domain.SubtitleCandidate, error) {
	return p.items, nil
}

func newSubtitleTestServiceWithRepo(t *testing.T, engine *subtitleEngineStub, probe *subtitleProbeStub, providers ...SubtitleProvider) (*Service, *sqlite.Repository) {
	t.Helper()
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
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
	ctx := context.Background()
	release := domain.TorrentRelease{ID: "release", Name: "Movie.2023.JAPANESE.1080p.WEB-DL"}
	if err := repo.UpsertReleases(ctx, []domain.TorrentRelease{release}); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(media, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := domain.Download{ID: "source", ReleaseID: release.ID, EngineID: "qb:abc", FileIndex: 0, FilePath: "movie.mkv", AbsolutePath: media, State: "complete", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, engine, repo, settings, providers...)
	service.SetMediaProbe(probe)
	return service, repo
}

func newSubtitleTestService(t *testing.T, engine *subtitleEngineStub, probe *subtitleProbeStub, providers ...SubtitleProvider) *Service {
	service, _ := newSubtitleTestServiceWithRepo(t, engine, probe, providers...)
	return service
}

func candidateByProvider(items []domain.SubtitleCandidate, provider string) domain.SubtitleCandidate {
	for _, item := range items {
		if item.Provider == provider {
			return item
		}
	}
	return domain.SubtitleCandidate{}
}

func TestSearchSubtitlesNormalizesLanguageFromEverySource(t *testing.T) {
	service := newSubtitleTestService(
		t,
		&subtitleEngineStub{files: []domain.TorrentFile{{Index: 7, Path: "Movie.jpn.srt"}}},
		&subtitleProbeStub{tracks: []domain.MediaSubtitleTrack{{Index: 3, Language: "jpn", Codec: "subrip"}}},
		&subtitleProviderStub{items: []domain.SubtitleCandidate{{ID: "p1", Language: "jpn", Title: "Movie.jpn.srt", Score: 10}}},
	)
	items, _, err := service.SearchSubtitles(context.Background(), "source", "ja", SubtitleScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	contained := candidateByProvider(items, "contained")
	embedded := candidateByProvider(items, "embedded")
	provider := candidateByProvider(items, "subdl")
	if contained.ID != "7" || contained.Language != "ja" {
		t.Fatalf("contained candidate = %#v, want id 7 language ja", contained)
	}
	if embedded.ID != "3" || embedded.Language != "ja" {
		t.Fatalf("embedded candidate = %#v, want id 3 language ja", embedded)
	}
	if provider.ID != "p1" || provider.Language != "ja" {
		t.Fatalf("subdl candidate = %#v, want id p1 language ja", provider)
	}
}

func TestPrepareSubtitleEmbeddedAssetKeepsCandidateLanguage(t *testing.T) {
	probe := &subtitleProbeStub{
		tracks:  []domain.MediaSubtitleTrack{{Index: 3, Language: "jpn", Codec: "subrip"}},
		content: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nHello\n",
	}
	service := newSubtitleTestService(t, &subtitleEngineStub{}, probe)
	asset, err := service.PrepareSubtitle(context.Background(), "source", "embedded", "3", "vtt")
	if err != nil {
		t.Fatal(err)
	}
	if asset.Language != "ja" {
		t.Fatalf("prepared embedded asset language = %q, want the candidate's canonical language ja", asset.Language)
	}
	reused, err := service.PrepareSubtitle(context.Background(), "source", "embedded", "3", "vtt")
	if err != nil {
		t.Fatal(err)
	}
	if reused.Language != "ja" {
		t.Fatalf("persisted embedded asset language = %q, want ja to survive a restart", reused.Language)
	}
}

// subtitleDownloadStub serves fixed subtitle bytes from the provider path so
// tests can prepare real conversions without a network provider.
type subtitleDownloadStub struct {
	subtitleProviderStub
	download SubtitleDownload
}

func (p *subtitleDownloadStub) Download(context.Context, string) (SubtitleDownload, error) {
	return p.download, nil
}

func TestPrepareSubtitleVTTPositionsEveryCueAboveTheControlBar(t *testing.T) {
	probe := &subtitleProbeStub{
		tracks:  []domain.MediaSubtitleTrack{{Index: 3, Language: "jpn", Codec: "subrip"}},
		content: "WEBVTT\n\nintro\n00:00:01.000 --> 00:00:02.000\nHello\n\n00:00:03.500 --> 00:00:05.000 align:start line:10% position:10%\nRepositioned\n",
	}
	service := newSubtitleTestService(t, &subtitleEngineStub{}, probe)
	asset, err := service.PrepareSubtitle(context.Background(), "source", "embedded", "3", "vtt")
	if err != nil {
		t.Fatal(err)
	}
	// The subtitles route serves the converted cache file byte-for-byte, so
	// what a browser <track> downloads is exactly the file at asset.Path.
	body, err := os.ReadFile(asset.Path)
	if err != nil {
		t.Fatal(err)
	}
	const suffix = " align:center line:80% position:50%"
	cues := 0
	for _, block := range strings.Split(string(body), "\n\n") {
		timing := ""
		for _, line := range strings.Split(block, "\n") {
			if strings.Contains(line, "-->") {
				timing = line
				break
			}
		}
		if timing == "" {
			continue
		}
		cues++
		if !strings.HasSuffix(timing, suffix) {
			t.Fatalf("cue timing %q, want it to end with %q", timing, suffix)
		}
		if remainder := strings.TrimSuffix(timing, suffix); strings.Contains(remainder, "align:") || strings.Contains(remainder, "line:") {
			t.Fatalf("cue timing %q keeps settings beyond the single positioning suffix", timing)
		}
	}
	if cues != 2 {
		t.Fatalf("document produced %d cues, want both cues of the multi-cue source", cues)
	}
	if got := strings.Count(string(body), "align:"); got != cues {
		t.Fatalf("document carries %d align settings for %d cues, want exactly one per cue (source settings replaced, not appended)", got, cues)
	}
	if !strings.Contains(string(body), "Hello") || !strings.Contains(string(body), "Repositioned") {
		t.Fatalf("cue text lost while positioning cues: %q", body)
	}
}

func TestPrepareSubtitleSRTConversionPositionsEveryCue(t *testing.T) {
	provider := &subtitleDownloadStub{download: SubtitleDownload{
		Data:     []byte("1\n00:00:01,000 --> 00:00:02,000\nSalut\n\n2\n00:00:03,000 --> 00:00:04,000\nPa\n"),
		Format:   ".srt",
		Name:     "Movie.ro.srt",
		Language: "ron",
	}}
	service := newSubtitleTestService(t, &subtitleEngineStub{}, &subtitleProbeStub{}, provider)
	asset, err := service.PrepareSubtitle(context.Background(), "source", "subdl", "p1", "vtt")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(asset.Path)
	if err != nil {
		t.Fatal(err)
	}
	const suffix = " align:center line:80% position:50%"
	cues := 0
	for _, line := range strings.Split(string(body), "\n") {
		if strings.Contains(line, "-->") {
			cues++
			if !strings.HasSuffix(line, suffix) {
				t.Fatalf("converted cue timing %q, want it to end with %q", line, suffix)
			}
		}
	}
	if cues != 2 {
		t.Fatalf("converted document produced %d cues, want 2", cues)
	}
	if !strings.HasPrefix(string(body), "WEBVTT\n\n") {
		t.Fatalf("converted document lost the WebVTT header: %q", body)
	}
}

func TestPrepareSubtitleSAMIStaysByteIdentical(t *testing.T) {
	provider := &subtitleDownloadStub{download: SubtitleDownload{
		Data:     []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n\n2\n00:00:03,000 --> 00:00:04,000\nBye\n"),
		Format:   ".srt",
		Name:     "Movie.ro.srt",
		Language: "ron",
	}}
	service := newSubtitleTestService(t, &subtitleEngineStub{}, &subtitleProbeStub{}, provider)
	asset, err := service.PrepareSubtitle(context.Background(), "source", "subdl", "p1", "sami")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(asset.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := "<SAMI><HEAD><STYLE><!-- P { font-family: sans-serif; color: white; text-align: center; } --></STYLE></HEAD><BODY>\n" +
		"<SYNC Start=1000><P Class=SUBTTL>Hello</P>\n<SYNC Start=2000><P Class=SUBTTL>&nbsp;</P>\n" +
		"<SYNC Start=3000><P Class=SUBTTL>Bye</P>\n<SYNC Start=4000><P Class=SUBTTL>&nbsp;</P>\n" +
		"</BODY></SAMI>\n"
	if string(body) != want {
		t.Fatalf("sami output drifted from the pre-positioning bytes:\n got %q\nwant %q", body, want)
	}
}

func TestSearchSubtitlesMarksPreparedProviderCandidatesCached(t *testing.T) {
	service, repo := newSubtitleTestServiceWithRepo(
		t,
		&subtitleEngineStub{},
		&subtitleProbeStub{},
		&subtitleProviderStub{items: []domain.SubtitleCandidate{{ID: "p1", Language: "jpn", Title: "Movie.jpn.srt", Score: 10}}},
	)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.SaveSubtitleAsset(ctx, domain.SubtitleAsset{ID: "asset", SourceID: "source", Provider: "subdl", CandidateID: "p1", Name: "Movie.jpn.srt", Language: "ja", Format: "vtt", MimeType: "text/vtt", Path: "/tmp/asset.vtt", CreatedAt: now, LastUsedAt: now}); err != nil {
		t.Fatal(err)
	}
	items, _, err := service.SearchSubtitles(ctx, "source", "ja", SubtitleScopeAll)
	if err != nil {
		t.Fatal(err)
	}
	provider := candidateByProvider(items, "subdl")
	if provider.ID != "p1" || !provider.Cached {
		t.Fatalf("subdl candidate = %#v, want cached=true once an asset is prepared", provider)
	}
}

func TestSubtitleAssetIDIsScopedToTheSource(t *testing.T) {
	repo := newSubtitleTestRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Two downloads share the same subtitle candidate and identical converted
	// content; the asset id must still be per-source or the second insert
	// collides on the primary key (bug: UNIQUE constraint failed:
	// subtitle_assets.id).
	first := prepareAssetForTest(t, repo, "download-a", now)
	second := prepareAssetForTest(t, repo, "download-b", now)

	if first.ID == second.ID {
		t.Fatalf("asset ids must differ across sources, both %q", first.ID)
	}
	for source, asset := range map[string]domain.SubtitleAsset{"download-a": first, "download-b": second} {
		got, err := repo.GetSubtitleAsset(ctx, source, asset.Provider, asset.CandidateID, asset.Format)
		if err != nil {
			t.Fatalf("asset for %s missing after save: %v", source, err)
		}
		if got.Path != asset.Path {
			t.Fatalf("asset for %s points at %q, want %q", source, got.Path, asset.Path)
		}
	}
}

func prepareAssetForTest(t *testing.T, repo *sqlite.Repository, sourceID string, now time.Time) domain.SubtitleAsset {
	t.Helper()
	asset := domain.SubtitleAsset{
		ID:          subtitleAssetIDForTest(sourceID, "subtitle content"),
		SourceID:    sourceID,
		Provider:    "subdl",
		CandidateID: "p1",
		Name:        "Movie.ro.srt",
		Language:    "ro",
		Format:      "vtt",
		MimeType:    "text/vtt",
		Path:        "/tmp/asset-" + sourceID + ".vtt",
		CreatedAt:   now,
		LastUsedAt:  now,
	}
	if err := repo.SaveSubtitleAsset(context.Background(), asset); err != nil {
		t.Fatalf("SaveSubtitleAsset(%s): %v", sourceID, err)
	}
	return asset
}

func subtitleAssetIDForTest(source, content string) string {
	sum := sha256.Sum256([]byte(source + "\x00content\x00" + content))
	return hex.EncodeToString(sum[:16])
}

func newSubtitleTestRepository(t *testing.T) *sqlite.Repository {
	t.Helper()
	repo, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}
