package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// Starvation path of ADR-0004: a new download must fit the Allocation. The
// gate surveys stored bytes exactly like the retention job, evicts one
// unprotected torrent at a time, and rejects with a visible Allocation
// problem when nothing evictable remains.

type capacityEngine struct {
	TorrentEngine
	mu      sync.Mutex
	status  map[string]domain.DownloadStatus
	removed []string
	adds    int
	files   []domain.TorrentFile
}

func (e *capacityEngine) Add(_ context.Context, _ io.Reader, _ string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.adds++
	if _, exists := e.status["incoming"]; !exists {
		e.status["incoming"] = domain.DownloadStatus{Hash: "incoming", State: "downloading"}
	}
	return "incoming", nil
}

func (e *capacityEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	return e.files, nil
}

func (e *capacityEngine) Status(_ context.Context, hash string) (domain.DownloadStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	status, ok := e.status[hash]
	if !ok {
		return domain.DownloadStatus{}, domain.ErrTorrentNotFound
	}
	return status, nil
}

func (e *capacityEngine) PrepareFiles(context.Context, string, []int, []int) error { return nil }

func (e *capacityEngine) PrepareRange(context.Context, string, int, int64, int64) error { return nil }

func (e *capacityEngine) Remove(_ context.Context, hash string, _ bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.status, hash)
	e.removed = append(e.removed, hash)
	return nil
}

type capacityCatalog struct {
	TrackerCatalog
	torrent string
	openErr error
}

func (c capacityCatalog) OpenTorrent(context.Context, string) (io.ReadCloser, error) {
	if c.openErr != nil {
		return nil, c.openErr
	}
	return io.NopCloser(strings.NewReader(c.torrent)), nil
}

// capacityTorrent is a single-file bencoded torrent whose payload weighs the
// given number of bytes — the manifest the admission gate reads pre-add.
func capacityTorrent(length int64) string {
	return fmt.Sprintf("d4:infod6:lengthi%de4:name5:movieee", length)
}

func TestPrepareEvictsUnprotectedToFitAllocation(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	seedRetentionRelease(t, repo, "incoming-release", "New.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "old-release", "Old.S01.1080p.WEB-DL")
	seedRetentionDownload(t, repo, "old", "old-release", "qb:old", time.Now().UTC().Add(-2*time.Hour), false, 1)
	engine := &capacityEngine{
		status: map[string]domain.DownloadStatus{"old": {Hash: "old", State: "pausedUP", Progress: 1, TotalBytes: 800 << 20}},
		files:  []domain.TorrentFile{{Index: 0, Path: "New.S01E01.1080p.mkv", SizeBytes: 500 << 20, Playable: true}},
	}
	service := NewService(capacityCatalog{torrent: capacityTorrent(500 << 20)}, engine, repo, settings)

	download, err := service.Prepare(context.Background(), "incoming-release", -1)
	if err != nil {
		t.Fatal(err)
	}
	if download.ID == "" {
		t.Fatal("prepare did not register the managed download")
	}
	if engine.adds != 1 {
		t.Fatalf("the fitting download was not added exactly once: adds=%d", engine.adds)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "old" {
		t.Fatalf("the gate did not evict the one unprotected torrent: %v", engine.removed)
	}
}

func TestPrepareRejectsWhenAllocationCannotFit(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	seedRetentionRelease(t, repo, "incoming-release", "New.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "old-release", "Old.S01.1080p.WEB-DL")
	seedRetentionDownload(t, repo, "old", "old-release", "qb:old", time.Now().UTC().Add(-2*time.Hour), false, 1)
	seedRetentionDownload(t, repo, "leased", "old-release", "qb:leased", time.Now().UTC(), true, 1)
	engine := &capacityEngine{status: map[string]domain.DownloadStatus{
		"old":    {Hash: "old", State: "pausedUP", Progress: 1, TotalBytes: 200 << 20},
		"leased": {Hash: "leased", State: "pausedUP", Progress: 1, TotalBytes: 900 << 20},
	}}
	service := NewService(capacityCatalog{torrent: capacityTorrent(500 << 20)}, engine, repo, settings)

	_, err := service.Prepare(context.Background(), "incoming-release", -1)
	var fit *domain.AllocationError
	if !errors.As(err, &fit) {
		t.Fatalf("prepare should fail with the Allocation problem, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Allocation") || !strings.Contains(err.Error(), "New.S01.1080p.WEB-DL") || !strings.Contains(err.Error(), "0.5 GiB") {
		t.Fatalf("the problem must name the Allocation, the release, and the space required: %v", err)
	}
	if engine.adds != 0 {
		t.Fatalf("a download that cannot fit must never reach the engine: adds=%d", engine.adds)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "old" {
		t.Fatalf("the gate must stop after the unprotected torrent is gone: %v", engine.removed)
	}
}

func TestPrepareNeverEvictsProtectedToFit(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	seedRetentionRelease(t, repo, "incoming-release", "New.S01.1080p.WEB-DL")
	seedRetentionDownload(t, repo, "leased", "leased-release", "qb:leased", time.Now().UTC(), true, 1)
	engine := &capacityEngine{status: map[string]domain.DownloadStatus{
		"leased": {Hash: "leased", State: "pausedUP", Progress: 1, TotalBytes: 900 << 20},
	}}
	service := NewService(capacityCatalog{torrent: capacityTorrent(500 << 20)}, engine, repo, settings)

	_, err := service.Prepare(context.Background(), "incoming-release", -1)
	var fit *domain.AllocationError
	if !errors.As(err, &fit) {
		t.Fatalf("prepare should fail with the Allocation problem, got: %v", err)
	}
	if len(engine.removed) != 0 {
		t.Fatalf("protected downloads must never be evicted to fit: %v", engine.removed)
	}
	if engine.adds != 0 {
		t.Fatalf("the rejected download must not be added: adds=%d", engine.adds)
	}
}

func TestPrepareSkipsAllocationCheckWhenDisabled(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 0, 0)
	seedRetentionRelease(t, repo, "incoming-release", "New.S01.1080p.WEB-DL")
	seedRetentionDownload(t, repo, "leased", "leased-release", "qb:leased", time.Now().UTC(), true, 1)
	engine := &capacityEngine{
		status: map[string]domain.DownloadStatus{"leased": {Hash: "leased", State: "pausedUP", Progress: 1, TotalBytes: 900 << 20}},
		files:  []domain.TorrentFile{{Index: 0, Path: "New.S01E01.1080p.mkv", SizeBytes: 500 << 20, Playable: true}},
	}
	service := NewService(capacityCatalog{torrent: capacityTorrent(500 << 20)}, engine, repo, settings)

	if _, err := service.Prepare(context.Background(), "incoming-release", -1); err != nil {
		t.Fatalf("a zero Allocation disables the admission gate: %v", err)
	}
	if engine.adds != 1 || len(engine.removed) != 0 {
		t.Fatalf("disabled gate must add without evicting: adds=%d removed=%v", engine.adds, engine.removed)
	}
}

func TestPrepareUsesTrackerSizeWhenManifestUnavailable(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	if err := repo.UpsertReleases(context.Background(), []domain.TorrentRelease{{ID: "incoming-release", Name: "New.S01.1080p.WEB-DL", Category: "Series", SizeBytes: 500 << 20}}); err != nil {
		t.Fatal(err)
	}
	seedRetentionDownload(t, repo, "leased", "leased-release", "qb:leased", time.Now().UTC(), true, 1)
	engine := &capacityEngine{status: map[string]domain.DownloadStatus{
		"leased": {Hash: "leased", State: "pausedUP", Progress: 1, TotalBytes: 900 << 20},
	}}
	service := NewService(capacityCatalog{openErr: errors.New("tracker unreachable")}, engine, repo, settings)

	_, err := service.Prepare(context.Background(), "incoming-release", -1)
	var fit *domain.AllocationError
	if !errors.As(err, &fit) {
		t.Fatalf("without a manifest the tracker Release size must drive the gate, got: %v", err)
	}
	if engine.adds != 0 {
		t.Fatalf("the rejected download must not be added: adds=%d", engine.adds)
	}
}

func TestPrepareSeasonEvictsUnprotectedToFitAllocation(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	if err := repo.UpsertReleases(context.Background(), []domain.TorrentRelease{{ID: "pack-release", Name: "Pack.S01.1080p.WEB-DL", Category: "Series", FileCount: 2}}); err != nil {
		t.Fatal(err)
	}
	seedRetentionRelease(t, repo, "old-release", "Old.S01.1080p.WEB-DL")
	seedRetentionDownload(t, repo, "old", "old-release", "qb:old", time.Now().UTC().Add(-2*time.Hour), false, 1)
	engine := &capacityEngine{
		status: map[string]domain.DownloadStatus{"old": {Hash: "old", State: "pausedUP", Progress: 1, TotalBytes: 800 << 20}},
		files: []domain.TorrentFile{
			{Index: 0, Path: "Pack.S01E01.1080p.mkv", SizeBytes: 300 << 20, Playable: true},
			{Index: 1, Path: "Pack.S01E02.1080p.mkv", SizeBytes: 300 << 20, Playable: true},
		},
	}
	service := NewService(capacityCatalog{torrent: capacityTorrent(400 << 20)}, engine, repo, settings)

	downloads, err := service.PrepareSeason(context.Background(), "pack-release", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 2 {
		t.Fatalf("the season download did not complete: %d downloads", len(downloads))
	}
	if engine.adds != 1 {
		t.Fatalf("the fitting season pack was not added exactly once: adds=%d", engine.adds)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "old" {
		t.Fatalf("the gate did not evict the one unprotected torrent: %v", engine.removed)
	}
}
