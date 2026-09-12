package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

type pieceEngine struct{ TorrentEngine }

func (pieceEngine) Pieces(context.Context, string) (domain.PieceMap, error) {
	return domain.PieceMap{States: []int{2, 0}, PieceSize: 4}, nil
}

func (pieceEngine) PrepareRange(context.Context, string, int, int64, int64) error { return nil }

func TestSafeJoinRejectsTraversal(t *testing.T) {
	if _, err := safeJoin("/srv/downloads", "../../etc/passwd"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	p, err := safeJoin("/srv/downloads", "Show/episode.mkv")
	if err != nil || p != "/srv/downloads/Show/episode.mkv" {
		t.Fatalf("unexpected %q %v", p, err)
	}
}

func TestSafeQBPathUsesContainedSavePath(t *testing.T) {
	p, err := safeQBPath("/srv/downloads", "/srv/downloads/movies", "Movie/video.mkv")
	if err != nil || p != "/srv/downloads/movies/Movie/video.mkv" {
		t.Fatalf("unexpected %q %v", p, err)
	}
	if _, err := safeQBPath("/srv/downloads", "/var/tmp", "video.mkv"); err == nil {
		t.Fatal("expected an outside qBittorrent save path to be rejected")
	}
}

func TestSafeQBContentPathHandlesTempAndFinalLocations(t *testing.T) {
	tests := []struct {
		content string
		name    string
		want    string
	}{
		{"/srv/downloads/.incomplete/Movie", "Movie/video.mkv", "/srv/downloads/.incomplete/Movie/video.mkv"},
		{"/srv/downloads/.incomplete/video.mkv", "video.mkv", "/srv/downloads/.incomplete/video.mkv"},
		{"/srv/downloads/Movie", "Movie/video.mkv", "/srv/downloads/Movie/video.mkv"},
	}
	for _, test := range tests {
		got, err := safeQBContentPath("/srv/downloads", domain.DownloadStatus{ContentPath: test.content}, test.name)
		if err != nil || got != test.want {
			t.Fatalf("safeQBContentPath(%q, %q) = %q, %v", test.content, test.name, got, err)
		}
	}
	if _, err := safeQBContentPath("/srv/downloads", domain.DownloadStatus{ContentPath: "/var/tmp/Movie"}, "Movie/video.mkv"); err == nil {
		t.Fatal("expected outside content path rejection")
	}
}

func TestSafeQBContentPathUsesConfiguredTemporaryPathWhileIncomplete(t *testing.T) {
	status := domain.DownloadStatus{
		Progress:        0.19,
		SavePath:        "/mnt/media/torrent",
		ContentPath:     "/mnt/media/torrent/movie.mkv",
		TempPathEnabled: true,
		TempPath:        "/mnt/media/torrent/.incomplete",
	}
	path, err := safeQBContentPath("/mnt/media/torrent", status, "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/mnt/media/torrent/.incomplete/movie.mkv" {
		t.Fatalf("unexpected temporary path %q", path)
	}
}

func TestSafeQBContentPathUsesFinalPathAtCompletion(t *testing.T) {
	status := domain.DownloadStatus{
		Progress:        1,
		SavePath:        "/mnt/media/torrent",
		ContentPath:     "/mnt/media/torrent/movie.mkv",
		TempPathEnabled: true,
		TempPath:        "/mnt/media/torrent/.incomplete",
	}
	path, err := safeQBContentPath("/mnt/media/torrent", status, "movie.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/mnt/media/torrent/movie.mkv" {
		t.Fatalf("unexpected completed path %q", path)
	}
}

func TestWaitRangeDoesNotRequireTorrentCompletion(t *testing.T) {
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	settings, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{engines: singleEngineSet(t, "qb:", pieceEngine{}), settings: settings}
	download := domain.Download{EngineID: "qb:abc", PieceSize: 4, Progress: 0.5}
	if err := service.WaitRange(t.Context(), download, 0, 4); err != nil {
		t.Fatalf("downloaded range in incomplete torrent should be immediately streamable: %v", err)
	}
}

func TestApplyDownloadStatusUsesSelectedFileMetrics(t *testing.T) {
	download := domain.Download{FileIndex: 2, SizeBytes: 40_474_209_647}
	status := domain.DownloadStatus{State: "downloading", Progress: 0.9, DownloadedBytes: 36_000_000_000, SpeedBytesPerSecond: 12_000_000}
	selected := domain.TorrentFile{Index: 2, SizeBytes: 5_075_031_232, Progress: 0.25, Offset: 10_000_000_000}
	applyDownloadStatus(&download, status, &selected)
	if download.SizeBytes != selected.SizeBytes || download.Progress != 0.25 || download.DownloadedBytes != 1_268_757_808 {
		t.Fatalf("download did not use selected-file metrics: %+v", download)
	}
	if download.FileOffset != selected.Offset || download.SpeedBytesPerSecond != status.SpeedBytesPerSecond {
		t.Fatalf("download lost torrent status fields: %+v", download)
	}
}

func TestCompletedLocalFileRequiresSelectedFileCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "episode.mkv")
	if err := os.WriteFile(path, make([]byte, 16), 0o640); err != nil {
		t.Fatal(err)
	}
	download := domain.Download{AbsolutePath: path, SizeBytes: 16, State: "stalledUP", Progress: 0}
	if completedLocalFile(download) {
		t.Fatal("torrent upload state must not mark a newly selected incomplete file as local")
	}
	download.Progress = 1
	if !completedLocalFile(download) {
		t.Fatal("a complete selected file at its final path should use local playback")
	}
}

func TestDedupeManagedDownloadsKeepsDistinctSeasonEpisodes(t *testing.T) {
	items := []domain.Download{
		{ID: "new", EngineID: "qb:hash", FileIndex: 2, FilePath: "S01E03.mkv"},
		{ID: "legacy-duplicate", EngineID: "qb:hash", FileIndex: 2, FilePath: "renamed/S01E03.mkv"},
		{ID: "next-episode", EngineID: "qb:hash", FileIndex: 3, FilePath: "S01E04.mkv"},
	}
	got := dedupeManagedDownloads(items)
	if len(got) != 2 || got[0].ID != "new" || got[1].ID != "next-episode" {
		t.Fatalf("unexpected download reconciliation: %#v", got)
	}
}

type retryEngine struct {
	TorrentEngine
	resumeErr error
	resumes   int
	adds      int
	prepared  [][]int
}

func (e *retryEngine) Resume(context.Context, string) error { e.resumes++; return e.resumeErr }
func (e *retryEngine) Add(context.Context, io.Reader, string) (string, error) {
	e.adds++
	return "livehash", nil
}

func (e *retryEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	return []domain.TorrentFile{{Index: 2, Path: "Show.S01E02.mkv", SizeBytes: 4096, Playable: true}}, nil
}

func (e *retryEngine) PrepareFiles(_ context.Context, _ string, indices []int, _ []int) error {
	e.prepared = append(e.prepared, indices)
	return nil
}

func (e *retryEngine) PrepareRange(context.Context, string, int, int64, int64) error { return nil }

func (e *retryEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	return domain.DownloadStatus{Hash: "livehash", State: "downloading", TotalBytes: 4096}, nil
}

type openCatalog struct{ Tracker }

func (openCatalog) ID() string   { return "filelist" }
func (openCatalog) Name() string { return "FileList" }
func (openCatalog) Capabilities() TrackerCapabilities {
	return TrackerCapabilities{Categories: true}
}
func (openCatalog) Categories() []domain.TrackerCategory { return nil }
func (openCatalog) Acquire(context.Context, string) (domain.TorrentAcquisition, error) {
	return domain.TorrentAcquisition{Metainfo: []byte("d4:infod6:lengthi4eee")}, nil
}

func testRegistry(adapter Tracker) *TrackerRegistry {
	reg, err := NewTrackerRegistry([]TrackerRegistration{
		{Adapter: adapter, Enabled: func() bool { return true }, Configured: func() bool { return true }},
	})
	if err != nil {
		panic(err)
	}
	return reg
}

func retryHarness(t *testing.T) (*sqlite.Repository, *config.Store) {
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
	value := settings.Get()
	value.DownloadRoot = dir
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	return repo, settings
}

func seedRetryDownload(t *testing.T, repo *sqlite.Repository, releaseID string, seedRelease bool) {
	t.Helper()
	ctx := context.Background()
	storedID := releaseID
	if seedRelease {
		providerID := strings.TrimPrefix(releaseID, "filelist:")
		canonicalID := "filelist:" + providerID
		release := domain.TorrentRelease{ID: canonicalID, TrackerID: "filelist", TrackerName: "FileList", ProviderID: providerID, Name: "Show.S01.1080p.WEB-DL", Category: "Series"}
		stored, err := repo.UpsertReleases(ctx, []domain.TorrentRelease{release})
		if err != nil {
			t.Fatal(err)
		}
		storedID = stored[0].ID
	}
	now := time.Now().UTC()
	download := domain.Download{ID: "episode", ReleaseID: storedID, EngineID: "qb:deadhash", FileIndex: 2, FilePath: "Show.S01E02.mkv", State: "unavailable", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
}

func TestRetryResumesExistingTorrent(t *testing.T) {
	repo, settings := retryHarness(t)
	seedRetryDownload(t, repo, "release", true)
	engine := &retryEngine{}
	service := NewService(testRegistry(openCatalog{}), singleEngineSet(t, "qb:", engine), repo, settings)
	if err := service.Manage(context.Background(), "episode", "retry", false); err != nil {
		t.Fatal(err)
	}
	if engine.resumes != 1 || engine.adds != 0 {
		t.Fatalf("retry of a live torrent must resume in place: resumes=%d adds=%d", engine.resumes, engine.adds)
	}
	row, err := repo.GetDownload(context.Background(), "episode")
	if err != nil || row.State != "retry" {
		t.Fatalf("retry did not stamp the action marker: %#v %v", row, err)
	}
}

func TestRetryRepreparesVanishedTorrent(t *testing.T) {
	repo, settings := retryHarness(t)
	seedRetryDownload(t, repo, "release", true)
	engine := &retryEngine{resumeErr: domain.ErrTorrentNotFound}
	service := NewService(testRegistry(openCatalog{}), singleEngineSet(t, "qb:", engine), repo, settings)
	if err := service.Manage(context.Background(), "episode", "retry", false); err != nil {
		t.Fatal(err)
	}
	if engine.resumes != 1 || engine.adds != 1 || len(engine.prepared) != 1 {
		t.Fatalf("retry of a vanished torrent must re-prepare: resumes=%d adds=%d prepared=%v", engine.resumes, engine.adds, engine.prepared)
	}
	if _, err := repo.GetDownload(context.Background(), "episode"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale row for the vanished torrent survived: %v", err)
	}
	row, err := repo.FindDownload(context.Background(), "filelist:release", 2)
	if err != nil || row.EngineID != "qb:livehash" || row.ReleaseID != "filelist:release" || row.FileIndex != 2 || row.State != "downloading" {
		t.Fatalf("re-prepared row does not carry the cached release and file: %#v %v", row, err)
	}
}

func TestRetrySurfacesErrorWhenReleaseGone(t *testing.T) {
	repo, settings := retryHarness(t)
	seedRetryDownload(t, repo, "gone", false)
	engine := &retryEngine{resumeErr: domain.ErrTorrentNotFound}
	service := NewService(testRegistry(openCatalog{}), singleEngineSet(t, "qb:", engine), repo, settings)
	err := service.Manage(context.Background(), "episode", "retry", false)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("retry without a cached release must surface the lookup error: %v", err)
	}
	if engine.adds != 0 {
		t.Fatalf("missing release was silently re-added: adds=%d", engine.adds)
	}
}

func TestOwnerResolvesRoutesByPrefix(t *testing.T) {
	s := &Service{engines: singleEngineSet(t, "qb:", nil)}
	if _, hash, ok := s.owner("qb:abc123"); !ok || hash != "abc123" {
		t.Fatalf("default prefix must resolve qb: routes, got %q %v", hash, ok)
	}
	s.engines = singleEngineSet(t, "native:", nil)
	if _, hash, ok := s.owner("native:deadbeef"); !ok || hash != "deadbeef" {
		t.Fatalf("native prefix must resolve, got %q %v", hash, ok)
	}
	if _, _, ok := s.owner("qb:abc123"); ok {
		t.Fatal("a foreign engine route must not resolve")
	}
}

func TestDownloadsMarksForeignEngineRouteUnavailable(t *testing.T) {
	repo, settings := retryHarness(t)
	ctx := context.Background()
	release := domain.TorrentRelease{ID: "filelist:release", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "release", Name: "Show.S01.1080p.WEB-DL", Category: "Series"}
	if _, err := repo.UpsertReleases(ctx, []domain.TorrentRelease{release}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stale := domain.Download{ID: "episode", ReleaseID: release.ID, EngineID: "qb:deadhash", FileIndex: 2, FilePath: "Show.S01E02.mkv", State: "downloading", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, stale); err != nil {
		t.Fatal(err)
	}
	service := NewService(testRegistry(openCatalog{}), singleEngineSet(t, "native:", &retryEngine{}), repo, settings)
	items, err := service.Downloads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected the persisted row to list, got %d items", len(items))
	}
	if items[0].State != "unavailable" || items[0].Error == "" {
		t.Fatalf("a foreign qb: row must surface unavailable under native routing, got state=%q error=%q", items[0].State, items[0].Error)
	}
}

type countingEngine struct {
	TorrentEngine
	statuses, filesCalls int
}

func (e *countingEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	e.statuses++
	return domain.DownloadStatus{Hash: "sharehash", State: "downloading", TotalBytes: 100}, nil
}

func (e *countingEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	e.filesCalls++
	return []domain.TorrentFile{
		{Index: 0, Path: "Show.S01E01.mkv", SizeBytes: 60, Playable: true},
		{Index: 1, Path: "Show.S01E02.mkv", SizeBytes: 40, Playable: true},
	}, nil
}

func TestDownloadsFetchesEachTorrentOncePerAggregation(t *testing.T) {
	repo, settings := retryHarness(t)
	ctx := context.Background()
	release := domain.TorrentRelease{ID: "filelist:pack", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "release", Name: "Show.S01.1080p.WEB-DL", Category: "Series"}
	if _, err := repo.UpsertReleases(ctx, []domain.TorrentRelease{release}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, id := range []string{"episode-one", "episode-two"} {
		row := domain.Download{ID: id, ReleaseID: release.ID, EngineID: "qb:sharehash", FileIndex: i, FilePath: fmt.Sprintf("Show.S01E0%d.mkv", i+1), State: "downloading", CreatedAt: now, UpdatedAt: now}
		if err := repo.SaveDownload(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	engine := &countingEngine{}
	service := NewService(testRegistry(openCatalog{}), singleEngineSet(t, "qb:", engine), repo, settings)
	items, err := service.Downloads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected both rows sharing the torrent to list, got %d", len(items))
	}
	if engine.statuses != 1 || engine.filesCalls != 1 {
		t.Fatalf("rows sharing a torrent must fetch it once: statuses=%d files=%d", engine.statuses, engine.filesCalls)
	}
	sizes := map[int]int64{items[0].FileIndex: items[0].SizeBytes, items[1].FileIndex: items[1].SizeBytes}
	if sizes[0] != 60 || sizes[1] != 40 {
		t.Fatalf("each row must reflect its own selected file: %v", sizes)
	}
}

type dummyEngine struct{ TorrentEngine }

func singleEngineSet(t *testing.T, prefix string, e TorrentEngine) *EngineSet {
	t.Helper()
	if prefix == "" {
		prefix = "qb:"
	}
	if e == nil {
		e = &dummyEngine{}
	}
	es, err := NewEngineSet(prefix, map[string]TorrentEngine{prefix: e})
	if err != nil {
		t.Fatal(err)
	}
	return es
}

type routingEngine struct {
	TorrentEngine
	mu    sync.Mutex
	state string
	hits  []string
}

func (e *routingEngine) Status(_ context.Context, hash string) (domain.DownloadStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hits = append(e.hits, hash)
	return domain.DownloadStatus{Hash: hash, State: e.state, TotalBytes: 4096}, nil
}

func (e *routingEngine) statusHits() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.hits...)
}

func (e *routingEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	return []domain.TorrentFile{{Index: 0, Path: "video.mkv", SizeBytes: 4096, Playable: true}}, nil
}

func TestDownloadsRoutesEachRowToItsOwnEngine(t *testing.T) {
	repo, settings := retryHarness(t)
	ctx := context.Background()
	seedRetentionRelease(t, repo, "native-release", "Native.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "qb-release", "QBit.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "ghost-release", "Ghost.S01.1080p.WEB-DL")
	native, qb := &routingEngine{state: "downloading"}, &routingEngine{state: "pausedUP"}
	set, err := NewEngineSet("qb:", map[string]TorrentEngine{"native:": native, "qb:": qb})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, row := range []domain.Download{
		{ID: "native-row", ReleaseID: "filelist:native-release", EngineID: "native:nativehash", TrackerID: "filelist", TrackerName: "FileList", FileIndex: 0, FilePath: "native-row.mkv", CreatedAt: now, UpdatedAt: now},
		{ID: "qb-row", ReleaseID: "filelist:qb-release", EngineID: "qb:qbhash", TrackerID: "filelist", TrackerName: "FileList", FileIndex: 0, FilePath: "qb-row.mkv", CreatedAt: now, UpdatedAt: now},
		{ID: "ghost-row", ReleaseID: "filelist:ghost-release", EngineID: "ghost:ghosthash", TrackerID: "filelist", TrackerName: "FileList", FileIndex: 0, FilePath: "ghost-row.mkv", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repo.SaveDownload(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(testRegistry(openCatalog{}), set, repo, settings)

	items, err := service.Downloads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("expected all three rows to list, got %d", len(items))
	}
	byID := map[string]domain.Download{}
	for _, item := range items {
		byID[item.ID] = item
	}
	if got := byID["native-row"].State; got != "downloading" {
		t.Fatalf("the native row must reflect its own engine state, got %q", got)
	}
	if got := byID["qb-row"].State; got != "pausedUP" {
		t.Fatalf("the qb row must reflect its own engine state, got %q", got)
	}
	if !slices.Equal(native.statusHits(), []string{"nativehash"}) || !slices.Equal(qb.statusHits(), []string{"qbhash"}) {
		t.Fatalf("each engine must be probed only for its own hashes: native=%v qb=%v", native.statusHits(), qb.statusHits())
	}
	ghost := byID["ghost-row"]
	if ghost.State != "unavailable" || ghost.Error == "" {
		t.Fatalf("a row whose engine is absent from the set must surface unavailable, got state=%q error=%q", ghost.State, ghost.Error)
	}
	if !strings.Contains(ghost.Error, domain.ErrEngineUnavailable.Error()) || !strings.Contains(ghost.Error, "ghost:") {
		t.Fatalf("unavailable row must wrap ErrEngineUnavailable and name the prefix, got %q", ghost.Error)
	}
	for _, id := range []string{"native-row", "qb-row"} {
		if byID[id].TrackerID != "filelist" || byID[id].TrackerName != "FileList" {
			t.Fatalf("%s lost its tracker labels: %+v", id, byID[id])
		}
	}
}

type deadCatalog struct{ Tracker }

func (deadCatalog) ID() string   { return "filelist" }
func (deadCatalog) Name() string { return "FileList" }
func (deadCatalog) Capabilities() TrackerCapabilities {
	return TrackerCapabilities{Categories: true}
}
func (deadCatalog) Categories() []domain.TrackerCategory { return nil }
func (deadCatalog) Acquire(context.Context, string) (domain.TorrentAcquisition, error) {
	return domain.TorrentAcquisition{}, fmt.Errorf("%w: FileList no longer hosts the .torrent for this release", domain.ErrTorrentRemoved)
}

func TestPrepareRemovesReleaseDeletedFromTracker(t *testing.T) {
	repo, settings := retryHarness(t)
	movie := domain.TorrentRelease{ID: "filelist:release", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "release", Name: "Minions.and.Monsters.2026.1080p.AMZN.WEB-DL.DD+5.1.H.264-playWEB", Category: "Movies"}
	if _, err := repo.UpsertReleases(context.Background(), []domain.TorrentRelease{movie}); err != nil {
		t.Fatal(err)
	}
	service := NewService(testRegistry(deadCatalog{}), singleEngineSet(t, "qb:", &retryEngine{}), repo, settings)
	_, err := service.Prepare(context.Background(), movie.ID, 0)
	if err == nil || !strings.Contains(err.Error(), "no longer available on FileList") {
		t.Fatalf("prepare must explain the removal to the user, got %v", err)
	}
	if _, getErr := repo.GetRelease(context.Background(), movie.ID); !errors.Is(getErr, sql.ErrNoRows) {
		t.Fatalf("dead release must be removed from the catalog, got %v", getErr)
	}
	events, evErr := repo.ListEvents(context.Background(), 0, 10)
	if evErr != nil || len(events) == 0 || events[0].Kind != "release_removed" {
		t.Fatalf("removal must be journalled for the events page, got %v %v", events, evErr)
	}
}

type idleCatalog struct{ Tracker }

func (idleCatalog) ID() string   { return "filelist" }
func (idleCatalog) Name() string { return "FileList" }
func (idleCatalog) Capabilities() TrackerCapabilities {
	return TrackerCapabilities{Categories: true}
}
func (idleCatalog) Categories() []domain.TrackerCategory { return nil }
func (idleCatalog) Latest(context.Context) ([]domain.TorrentRelease, error) {
	return nil, nil
}

func (idleCatalog) Category(context.Context, string) ([]domain.TorrentRelease, error) {
	return nil, nil
}

func (idleCatalog) Search(context.Context, string) ([]domain.TorrentRelease, error) {
	return nil, nil
}

func (idleCatalog) Acquire(context.Context, string) (domain.TorrentAcquisition, error) {
	return domain.TorrentAcquisition{}, nil
}

type closerEngine struct {
	TorrentEngine
	closed bool
}

func (e *closerEngine) Close() error {
	e.closed = true
	return nil
}

func TestCloseJoinsWorkersClosesEngineAndRepositoryIdempotently(t *testing.T) {
	repo, settings := retryHarness(t)
	engine := &closerEngine{}
	service := NewService(testRegistry(idleCatalog{}), singleEngineSet(t, "qb:", engine), repo, settings)
	service.StartScheduler()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("close must join the workers, scheduler, and in-flight jobs: %v", err)
	}
	if !engine.closed {
		t.Fatal("the engine must close after the workers stop")
	}
	if _, err := repo.ListEvents(context.Background(), 0, 10); err == nil {
		t.Fatal("the repository must be closed after Close returns")
	}
	if err := service.Close(ctx); err != nil {
		t.Fatalf("Close must be idempotent, got %v", err)
	}
}

type blockingCatalog struct {
	Tracker
	release chan struct{}
	mu      sync.Mutex
	entered bool
}

func (c *blockingCatalog) ID() string   { return "filelist" }
func (c *blockingCatalog) Name() string { return "FileList" }
func (c *blockingCatalog) Capabilities() TrackerCapabilities {
	return TrackerCapabilities{Categories: true}
}
func (c *blockingCatalog) Categories() []domain.TrackerCategory { return nil }
func (c *blockingCatalog) Latest(context.Context) ([]domain.TorrentRelease, error) {
	c.mu.Lock()
	c.entered = true
	c.mu.Unlock()
	<-c.release
	return nil, errors.New("aborted")
}

func (c *blockingCatalog) started() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entered
}

func TestCloseTimeoutAbortsHandoffKeepsRepositoryOpenAndBlocksJournal(t *testing.T) {
	repo, settings := retryHarness(t)
	catalog := &blockingCatalog{release: make(chan struct{})}
	engine := &closerEngine{}
	service := NewService(testRegistry(catalog), singleEngineSet(t, "qb:", engine), repo, settings)
	if _, err := service.SyncCatalog("latest"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !catalog.started() {
		time.Sleep(2 * time.Millisecond)
	}
	if !catalog.started() {
		t.Fatal("the catalog sync never entered its tracker call")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := service.Close(ctx); err == nil {
		t.Fatal("a wedged in-flight job must abort the handoff with an error")
	}
	if engine.closed {
		t.Fatal("an aborted handoff must not close the engine")
	}
	if _, err := repo.ListEvents(context.Background(), 0, 10); err != nil {
		t.Fatalf("an aborted handoff must not close the database under active writers: %v", err)
	}
	events, _ := repo.ListEvents(context.Background(), 0, 10)
	before := len(events)
	service.publish("portal.state", map[string]any{"blocked": true})
	service.jobLog(domain.Job{ID: "job"}, "info", "test", "blocked", nil)
	events, _ = repo.ListEvents(context.Background(), 0, 10)
	if len(events) != before {
		t.Fatal("no journal write may occur after Close began")
	}
	close(catalog.release)
}

// closeOrderEngine records every Close and fails if the repository is
// already closed when an engine closes — engines must shut down while the
// repository is still writable.
type closeOrderEngine struct {
	TorrentEngine
	name  string
	repo  Repository
	mu    sync.Mutex
	count int
}

func (e *closeOrderEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.count++
	if _, err := e.repo.ListEvents(context.Background(), 0, 1); err != nil {
		return fmt.Errorf("engine %s closed while the repository was already closed: %w", e.name, err)
	}
	return nil
}

func (e *closeOrderEngine) closeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

func TestCloseClosesEachEngineOnceBeforeRepositoryIdempotently(t *testing.T) {
	repo, settings := retryHarness(t)
	native, qb := &closeOrderEngine{name: "native", repo: repo}, &closeOrderEngine{name: "qb", repo: repo}
	service := NewService(testRegistry(idleCatalog{}), engineSetFor(t, "native:", map[string]TorrentEngine{"native:": native, "qb:": qb}), repo, settings)
	service.StartScheduler()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatalf("close must stop workers before closing engines and the repository: %v", err)
	}
	if native.closeCount() != 1 || qb.closeCount() != 1 {
		t.Fatalf("each engine must close exactly once, got native=%d qb=%d", native.closeCount(), qb.closeCount())
	}
	if _, err := repo.ListEvents(context.Background(), 0, 1); err == nil {
		t.Fatal("the repository must be closed after Close returns")
	}
	if err := service.Close(ctx); err != nil {
		t.Fatalf("a second Close must stay idempotent, got %v", err)
	}
	if native.closeCount() != 1 || qb.closeCount() != 1 {
		t.Fatalf("a second Close must not re-close engines, got native=%d qb=%d", native.closeCount(), qb.closeCount())
	}
}

func TestStorageReportProbesEveryConfiguredFolder(t *testing.T) {
	repo, settings := retryHarness(t)
	root := t.TempDir()
	missing := filepath.Join(root, "artwork")
	value := settings.Get()
	value.DownloadRoot = filepath.Join(root, "downloads")
	value.TorrentSessionDir = value.DownloadRoot
	value.ArtworkCachePath = missing
	value.SubtitleCachePath = filepath.Join(root, "subtitles")
	value.DatabasePath = filepath.Join(root, "data", "filelist.db")
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, singleEngineSet(t, "qb:", &dummyEngine{}), repo, settings)
	report := service.TestStorage()
	if !report.Ok {
		t.Fatalf("every folder must be creatable and writable, got: %s", report.Message)
	}
	// The download root and the session dir resolve to the same path and
	// must collapse into one check.
	if len(report.Folders) != 4 {
		t.Fatalf("expected 4 deduplicated folder checks, got %d", len(report.Folders))
	}
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("a missing cache folder must be created on demand: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data")); err != nil {
		t.Fatalf("the database folder must be created on demand: %v", err)
	}
}

func TestStorageReportNamesUnwritableFolders(t *testing.T) {
	repo, settings := retryHarness(t)
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	value := settings.Get()
	value.DownloadRoot = filepath.Join(root, "downloads")
	value.TorrentSessionDir = filepath.Join(root, "sessions")
	value.ArtworkCachePath = locked
	value.SubtitleCachePath = filepath.Join(root, "subtitles")
	value.DatabasePath = filepath.Join(root, "filelist.db")
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	service := NewService(nil, singleEngineSet(t, "qb:", &dummyEngine{}), repo, settings)
	report := service.TestStorage()
	if report.Ok {
		t.Fatalf("an unwritable folder must fail the report: %s", report.Message)
	}
	if !strings.Contains(report.Message, "Artwork cache") {
		t.Fatalf("summary must name the failing folder: %s", report.Message)
	}
	for _, folder := range report.Folders {
		if folder.Label == "Artwork cache" && folder.Ok {
			t.Fatal("the read-only artwork folder must not report ok")
		}
	}
}
