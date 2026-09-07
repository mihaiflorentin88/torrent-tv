package application

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestSurveyUncertaintyBlocksAdmissionWithoutZeroCounting(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 20, 0)
	seedRetentionRelease(t, repo, "native-release", "Native.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "qb-release", "QBit.S01.1080p.WEB-DL")
	updated := time.Now().UTC().Add(-time.Hour)
	seedRetentionDownload(t, repo, "qb-ep", "qb-release", "qb:h2", updated, false, 1)
	native := newMultiEngine(map[string]domain.DownloadStatus{
		"h1": {Hash: "h1", State: "downloading", TotalBytes: 10 << 30},
	}, nil)
	playable := filepath.Join(t.TempDir(), "native-ep.mkv")
	if err := os.WriteFile(playable, make([]byte, 128), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveDownload(context.Background(), domain.Download{
		ID: "native-ep", ReleaseID: canonicalReleaseID("native-release"), EngineID: "native:h1",
		FilePath: "native-ep.mkv", State: "downloading", Progress: 1, AbsolutePath: playable,
		SizeBytes: 128, CreatedAt: updated, UpdatedAt: updated,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(testRegistry(openCatalog{}), engineSetFor(t, "native:", map[string]TorrentEngine{"native:": native}), repo, settings)
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	ctx := context.Background()

	plan, err := service.retentionSurvey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.uncertainOwners) != 1 || plan.uncertainOwners[0] != "qb:" {
		t.Fatalf("the absent qb owner must be reported uncertain, got %v", plan.uncertainOwners)
	}

	err = service.ensureAllocationRoom(ctx, domain.TorrentRelease{Name: "New.Release.2025"}, 1<<30)
	if !errors.Is(err, domain.ErrEngineUnavailable) {
		t.Fatalf("uncertainty must refuse new admission with ErrEngineUnavailable, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "qb:") {
		t.Fatalf("the refusal must name the uncertain owner, got %v", err)
	}

	job, err := service.RunRetention()
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "completed" || job.Error != "" {
		t.Fatalf("uncertainty must not fail retention, got state=%q error=%q", job.State, job.Error)
	}
	rows, err := repo.ListDownloads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("uncertain routes must never be evicted or forgotten, got %d rows", len(rows))
	}
	if _, err := service.Prepare(ctx, canonicalReleaseID("native-release"), 0); err != nil {
		t.Fatalf("existing playback must survive admission refusal: %v", err)
	}
}

// dyingEngine answers Status successfully failAfter times, then fails —
// a deterministic stand-in for an engine that becomes unreachable mid-run.
type dyingEngine struct {
	TorrentEngine
	mu        sync.Mutex
	status    domain.DownloadStatus
	calls     int
	failAfter int
}

func newDyingEngine(status domain.DownloadStatus, failAfter int) *dyingEngine {
	return &dyingEngine{status: status, failAfter: failAfter}
}

func (e *dyingEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.calls > e.failAfter {
		return domain.DownloadStatus{}, domain.ErrTorrentNotFound
	}
	return e.status, nil
}

func TestAdmissionRefusesWhenEvictionReSurveyTurnsAnOwnerUncertain(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 5, 0)
	seedRetentionRelease(t, repo, "big-release", "Big.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "small-release", "Small.S01.1080p.WEB-DL")
	updated := time.Now().UTC()
	seedRetentionDownload(t, repo, "big", "big-release", "qb:bighash", updated.Add(-2*time.Hour), false, 1)
	seedRetentionDownload(t, repo, "small", "small-release", "native:smallhash", updated, false, 0.5)
	qb := newMultiEngine(map[string]domain.DownloadStatus{
		"bighash": {Hash: "bighash", State: "pausedUP", Progress: 1, TotalBytes: 10 << 30},
	}, nil)
	// The native owner answers the two pre-eviction surveys (the assertion
	// probe and ensureAllocationRoom's entry gate) but fails the re-survey
	// that follows the qb eviction — the gate must catch it mid-loop.
	native := newDyingEngine(domain.DownloadStatus{Hash: "smallhash", State: "downloading", Progress: 0.5, TotalBytes: 1 << 30}, 2)
	service := NewService(testRegistry(openCatalog{}), engineSetFor(t, "native:", map[string]TorrentEngine{"native:": native, "qb:": qb}), repo, settings)
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	ctx := context.Background()

	plan, err := service.retentionSurvey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.uncertainOwners) != 0 {
		t.Fatalf("initial survey must be clean, got %v", plan.uncertainOwners)
	}

	err = service.ensureAllocationRoom(ctx, domain.TorrentRelease{Name: "New.Release.2025"}, 1<<30)
	if !errors.Is(err, domain.ErrEngineUnavailable) {
		t.Fatalf("the re-survey uncertainty must refuse admission with ErrEngineUnavailable, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "native:") {
		t.Fatalf("the refusal must name the newly uncertain owner, got %v", err)
	}
	if removed := qb.snapshot().removed; len(removed) != 1 {
		t.Fatalf("exactly one eviction must precede the refusal, got %v", removed)
	}
}
