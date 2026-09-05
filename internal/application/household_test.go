package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

type removeEngine struct {
	TorrentEngine
	deleteFiles bool
	err         error
}

type streamingEngine struct {
	TorrentEngine
	prepared bool
	resumed  bool
}

func (e *streamingEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	state := "pausedDL"
	if e.resumed {
		state = "downloading"
	}
	return domain.DownloadStatus{State: state, Progress: 0.25, PieceSize: 4, Sequential: true, FirstLastPriority: true, SavePath: "/srv/filelist-downloads", ContentPath: "/srv/filelist-downloads/.incomplete/movie.mkv"}, nil
}

func (e *streamingEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	return []domain.TorrentFile{{Index: 3, Path: "movie.mkv", Playable: true}}, nil
}

func (e *streamingEngine) PrepareFile(context.Context, string, int, []int) error {
	e.prepared = true
	return nil
}

func (e *streamingEngine) PrepareFiles(context.Context, string, []int, []int) error {
	e.prepared = true
	return nil
}

func (e *streamingEngine) PrepareRange(context.Context, string, int, int64, int64) error { return nil }

func (e *streamingEngine) Resume(context.Context, string) error { e.resumed = true; return nil }

type failingCatalog struct {
	TrackerCatalog
	opens int
}

func (c *failingCatalog) OpenTorrent(context.Context, string) (io.ReadCloser, error) {
	c.opens++
	return nil, fmt.Errorf("FileList rate limit")
}

func (e *removeEngine) Remove(_ context.Context, _ string, deleteFiles bool) error {
	e.deleteFiles = deleteFiles
	return e.err
}

func TestHouseholdStateAndRemovalLifecycle(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	settings, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	now := time.Now().UTC()
	ctx := context.Background()
	release := domain.TorrentRelease{ID: "release", Name: "Movie", Category: "Movies"}
	if err := repo.UpsertReleases(ctx, []domain.TorrentRelease{release}); err != nil {
		t.Fatal(err)
	}
	download := domain.Download{ID: "source", ReleaseID: release.ID, EngineID: "qb:hash", FileIndex: 2, FilePath: "movie.mkv", AbsolutePath: "/downloads/movie.mkv", State: "complete", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	engine := &removeEngine{}
	service := NewService(nil, engine, repo, settings)
	state, err := service.UpdatePlayback(ctx, download.ID, 899, 1000)
	if err != nil || state.Watched {
		t.Fatalf("89.9%% must not be watched: %#v %v", state, err)
	}
	state, err = service.UpdatePlayback(ctx, download.ID, 900, 1000)
	if err != nil || !state.Watched {
		t.Fatalf("90%% must be watched: %#v %v", state, err)
	}
	if err := service.SetFavorite(ctx, release.ID, true); err != nil {
		t.Fatal(err)
	}
	household, err := service.HouseholdState(ctx)
	if err != nil || len(household.Favorites) != 1 || len(household.Watched) != 1 || len(household.Recent) != 1 {
		t.Fatalf("bad household state %#v %v", household, err)
	}
	if err := service.Manage(ctx, download.ID, "remove", true); err != nil {
		t.Fatal(err)
	}
	if !engine.deleteFiles {
		t.Fatal("deleteFiles was not forwarded")
	}
	if _, err := repo.GetDownload(ctx, download.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("managed row survived removal: %v", err)
	}
	if _, err := repo.GetPlayback(ctx, householdProfile, download.ID); err != nil {
		t.Fatalf("history did not survive removal: %v", err)
	}
	missing := download
	missing.ID = "missing"
	missing.Leased = true
	if err := repo.SaveDownload(ctx, missing); err != nil {
		t.Fatal(err)
	}
	if err := service.Manage(ctx, missing.ID, "remove", false); err == nil {
		t.Fatal("leased download removal should be rejected")
	}
	missing.Leased = false
	if err := repo.SaveDownload(ctx, missing); err != nil {
		t.Fatal(err)
	}
	engine.err = domain.ErrTorrentNotFound
	if err := service.Manage(ctx, missing.ID, "remove", false); err != nil {
		t.Fatalf("already-absent torrent should be forgotten: %v", err)
	}
	if _, err := repo.GetDownload(ctx, missing.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("already-absent torrent record survived: %v", err)
	}
}

func TestPrepareReusesManagedDownloadWithoutTrackerLookup(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	settings, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.Open(filepath.Join(dir, "prepare.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	release := domain.TorrentRelease{ID: "release", Name: "Movie.2026.1080p.WEB-DL", Category: "Movies HD"}
	if err := repo.UpsertReleases(ctx, []domain.TorrentRelease{release}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := settings.Get()
	value.DownloadRoot = dir
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(mediaPath, []byte("data"), 0o640); err != nil {
		t.Fatal(err)
	}
	download := domain.Download{ID: "download", ReleaseID: release.ID, EngineID: "qb:hash", FileIndex: 3, FilePath: "movie.mkv", AbsolutePath: mediaPath, SizeBytes: 4, Progress: 1, State: "uploading", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	catalog := &failingCatalog{}
	service := NewService(catalog, &removeEngine{}, repo, settings)

	got, err := service.Prepare(ctx, release.ID, download.FileIndex)
	if err != nil || got.ID != download.ID {
		t.Fatalf("managed download was not reused: %#v %v", got, err)
	}
	if catalog.opens != 0 {
		t.Fatalf("FileList was contacted %d times", catalog.opens)
	}
	legacy, err := service.Prepare(ctx, release.ID, -1)
	if err != nil || legacy.ID != download.ID || catalog.opens != 0 {
		t.Fatalf("legacy selection did not reuse the managed download: %#v %v opens=%d", legacy, err, catalog.opens)
	}
}

func TestPrepareReappliesStreamingSettingsForIncompleteManagedDownload(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	settings, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.Open(filepath.Join(dir, "progressive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	release := domain.TorrentRelease{ID: "release", Name: "Movie.2026.1080p.WEB-DL", Category: "Movies HD"}
	if err := repo.UpsertReleases(ctx, []domain.TorrentRelease{release}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := domain.Download{ID: "download", ReleaseID: release.ID, EngineID: "qb:hash", FileIndex: 3, FilePath: "movie.mkv", AbsolutePath: "/srv/filelist-downloads/movie.mkv", SizeBytes: 100, Progress: 0.25, State: "pausedDL", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	engine := &streamingEngine{}
	service := NewService(&failingCatalog{}, engine, repo, settings)
	if _, err := service.Prepare(ctx, release.ID, download.FileIndex); err != nil {
		t.Fatal(err)
	}
	if !engine.prepared || !engine.resumed {
		t.Fatalf("streaming preparation incomplete: prepared=%v resumed=%v", engine.prepared, engine.resumed)
	}
	before, err := repo.GetDownload(ctx, download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Downloads(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetDownload(ctx, download.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("passive telemetry refresh changed ordering timestamp: before=%s after=%s", before.UpdatedAt, after.UpdatedAt)
	}
}

func TestFavoritePrefersManagedSourceForCanonicalTitle(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	settings, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.Open(filepath.Join(dir, "favorite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	releases := []domain.TorrentRelease{
		{ID: "first", Name: "Movie.2026.1080p.WEB-DL", Category: "Movies HD"},
		{ID: "downloaded", Name: "Movie.2026.2160p.WEB-DL", Category: "Movies 4K"},
	}
	if err := repo.UpsertReleases(ctx, releases); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := domain.Download{ID: "managed-source", ReleaseID: "downloaded", EngineID: "qb:hash", FileIndex: 4, FilePath: "Movie/movie.mkv", AbsolutePath: "/downloads/Movie/movie.mkv", Progress: 1, State: "uploading", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	titleID := domain.CatalogTitleID(releases[0], domain.ParseRelease(releases[0]))
	if err := repo.SetFavorite(ctx, householdProfile, titleID, true); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, &removeEngine{}, repo, settings)
	state, err := service.HouseholdState(ctx)
	if err != nil || len(state.Favorites) != 1 {
		t.Fatalf("favorite was not returned: %#v %v", state, err)
	}
	got := state.Favorites[0]
	if got.SourceID != download.ID || got.Release.ID != download.ReleaseID || got.FileIndex != download.FileIndex || got.FilePath != download.FilePath {
		t.Fatalf("favorite did not resolve to the managed source: %#v", got)
	}
}

func TestHouseholdStateGroupsSeriesEpisodesByCanonicalTitle(t *testing.T) {
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	settings, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.Open(filepath.Join(dir, "grouped-household.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	releases := []domain.TorrentRelease{
		{ID: "silo-e1", Name: "Silo.S01E01.1080p.WEB-DL", Category: "TV-Series HD"},
		{ID: "silo-e2", Name: "Silo.S01E02.1080p.WEB-DL", Category: "TV-Series HD"},
		{ID: "movie", Name: "A.Movie.2026.1080p.WEB-DL", Category: "Movies HD"},
	}
	if err := repo.UpsertReleases(ctx, releases); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	playback := []domain.PlaybackState{
		{ProfileID: householdProfile, SourceID: "silo-source-1", ReleaseID: "silo-e1", FileIndex: 0, FilePath: "Silo.S01E01.mkv", PositionMS: 1_000, DurationMS: 10_000, UpdatedAt: now.Add(-time.Hour)},
		{ProfileID: householdProfile, SourceID: "silo-source-2", ReleaseID: "silo-e2", FileIndex: 0, FilePath: "Silo.S01E02.mkv", PositionMS: 2_000, DurationMS: 10_000, UpdatedAt: now},
		{ProfileID: householdProfile, SourceID: "movie-source", ReleaseID: "movie", FileIndex: 0, FilePath: "movie.mkv", PositionMS: 3_000, DurationMS: 10_000, UpdatedAt: now.Add(-2 * time.Hour)},
	}
	for _, item := range playback {
		if err := repo.SavePlayback(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(nil, &removeEngine{}, repo, settings)
	state, err := service.HouseholdState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ContinueWatching) != 2 || len(state.Recent) != 2 {
		t.Fatalf("expected one Silo card and one movie card, got continue=%#v recent=%#v", state.ContinueWatching, state.Recent)
	}
	if state.ContinueWatching[0].TitleID != state.Recent[0].TitleID || state.ContinueWatching[0].EpisodeNumber != 2 || state.ContinueWatching[0].SourceID != "silo-source-2" {
		t.Fatalf("newest Silo episode was not the representative card: %#v", state.ContinueWatching[0])
	}
}
