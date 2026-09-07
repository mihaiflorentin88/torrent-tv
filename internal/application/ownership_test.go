package application

import (
	"context"
	"io"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

type engineSnapshot struct {
	adds    []string
	removed []string
}

type multiEngine struct {
	TorrentEngine
	mu      sync.Mutex
	status  map[string]domain.DownloadStatus
	files   map[string][]domain.TorrentFile
	adds    []string
	removed []string
}

func newMultiEngine(status map[string]domain.DownloadStatus, files map[string][]domain.TorrentFile) *multiEngine {
	if status == nil {
		status = map[string]domain.DownloadStatus{}
	}
	if files == nil {
		files = map[string][]domain.TorrentFile{}
	}
	return &multiEngine{status: status, files: files}
}

func (e *multiEngine) Add(_ context.Context, _ io.Reader, _ string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	hash := "h" + strconv.Itoa(len(e.adds)+1)
	e.adds = append(e.adds, hash)
	return hash, nil
}

func (e *multiEngine) Files(_ context.Context, hash string) ([]domain.TorrentFile, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if files, ok := e.files[hash]; ok {
		return files, nil
	}
	return nil, domain.ErrTorrentNotFound
}

func (e *multiEngine) Status(_ context.Context, hash string) (domain.DownloadStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st, ok := e.status[hash]; ok {
		return st, nil
	}
	return domain.DownloadStatus{}, domain.ErrTorrentNotFound
}

func (e *multiEngine) PrepareFiles(context.Context, string, []int, []int) error { return nil }

func (e *multiEngine) Remove(_ context.Context, hash string, _ bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removed = append(e.removed, hash)
	return nil
}

func (e *multiEngine) snapshot() engineSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	adds := make([]string, len(e.adds))
	copy(adds, e.adds)
	removed := make([]string, len(e.removed))
	copy(removed, e.removed)
	return engineSnapshot{adds: adds, removed: removed}
}

func engineSetFor(t *testing.T, defaultPrefix string, engines map[string]TorrentEngine) *EngineSet {
	t.Helper()
	set, err := NewEngineSet(defaultPrefix, engines)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestPrepareSeasonReusesLegacyQBSeasonPackUnderNativeDefault(t *testing.T) {
	repo, settings := retryHarness(t)
	ctx := context.Background()

	stored, err := repo.UpsertReleases(ctx, []domain.TorrentRelease{{
		ID: "filelist:silo", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "silo",
		Name: "Show.S01.1080p.WEB-DL", Category: "Series", FileCount: 4,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected one stored release, got %d", len(stored))
	}

	qb := newMultiEngine(
		map[string]domain.DownloadStatus{
			"h1": {Hash: "h1", State: "pausedUP", SavePath: settings.Get().DownloadRoot},
		},
		map[string][]domain.TorrentFile{
			"h1": {
				{Index: 0, Path: "Show.S01E01.mkv", Playable: true, SizeBytes: 100, Offset: 0},
				{Index: 1, Path: "Show.S01E02.mkv", Playable: true, SizeBytes: 100, Offset: 100},
				{Index: 2, Path: "Show.S01.en.srt"},
			},
		},
	)
	native := newMultiEngine(nil, nil)

	now := time.Now().UTC()
	if err := repo.SaveDownload(ctx, domain.Download{
		ID: "ep1", ReleaseID: stored[0].ID, EngineID: "qb:h1", FileIndex: 0,
		FilePath: "Show.S01E01.mkv", State: "pausedUP", Progress: 1,
		AbsolutePath: filepath.Join(settings.Get().DownloadRoot, "Show.S01E01.mkv"),
		SizeBytes:    100, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	service := NewService(testRegistry(openCatalog{}), engineSetFor(t, "native:", map[string]TorrentEngine{"native:": native, "qb:": qb}), repo, settings)
	t.Cleanup(func() { _ = service.Close(context.Background()) })

	downloads, err := service.PrepareSeason(ctx, stored[0].ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) == 0 {
		t.Fatal("PrepareSeason returned no downloads")
	}
	for _, download := range downloads {
		if len(download.EngineID) < 3 || download.EngineID[:3] != "qb:" {
			t.Fatalf("download %s engine %q is not qb-owned", download.ID, download.EngineID)
		}
	}
	if adds := native.snapshot().adds; len(adds) != 0 {
		t.Fatalf("native default engine must not acquire the managed pack: adds=%v", adds)
	}
	if adds := qb.snapshot().adds; len(adds) != 0 {
		t.Fatalf("owner retention must not re-add the managed pack: adds=%v", adds)
	}
}
