package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

type retentionEngine struct {
	TorrentEngine
	mu           sync.Mutex
	status       map[string]domain.DownloadStatus
	removed      []string
	removedFiles []bool
	freed        int64
}

func (e *retentionEngine) Status(_ context.Context, hash string) (domain.DownloadStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.status[hash]
	if !ok {
		return domain.DownloadStatus{}, domain.ErrTorrentNotFound
	}
	return st, nil
}

func (e *retentionEngine) Remove(_ context.Context, hash string, deleteFiles bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st, ok := e.status[hash]; ok {
		e.freed += st.TotalBytes
		delete(e.status, hash)
	}
	e.removed = append(e.removed, hash)
	e.removedFiles = append(e.removedFiles, deleteFiles)
	return nil
}

func (e *retentionEngine) PrepareRange(context.Context, string, int, int64, int64) error { return nil }

func (e *retentionEngine) freedSnapshot() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.freed
}

func retentionSettings(t *testing.T, store *config.Store, allocationGB, reserveGB float64) {
	t.Helper()
	value := store.Get()
	value.AllocationGB = allocationGB
	value.ReserveGB = reserveGB
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
}

func seedRetentionDownload(t *testing.T, repo *sqlite.Repository, id, releaseID, engineID string, updated time.Time, leased bool, progress float64) {
	t.Helper()
	row := domain.Download{ID: id, ReleaseID: releaseID, EngineID: engineID, FilePath: id + ".mkv", State: "pausedUP", Progress: progress, Leased: leased, CreatedAt: updated, UpdatedAt: updated}
	if err := repo.SaveDownload(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

func seedRetentionRelease(t *testing.T, repo *sqlite.Repository, releaseID, name string) {
	t.Helper()
	if err := repo.UpsertReleases(context.Background(), []domain.TorrentRelease{{ID: releaseID, Name: name, Category: "Series"}}); err != nil {
		t.Fatal(err)
	}
}

func retentionEvents(t *testing.T, service *Service) []map[string]any {
	t.Helper()
	events, err := service.Events(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	payloads := []map[string]any{}
	for _, event := range events {
		if event.Kind != "downloads.evicted" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			t.Fatalf("eviction event payload is not JSON: %v", err)
		}
		payloads = append(payloads, payload)
	}
	if len(payloads) == 0 {
		t.Fatalf("no downloads.evicted event in journal: %d events", len(events))
	}
	return payloads
}

func evictionReleases(t *testing.T, payload map[string]any) []string {
	t.Helper()
	raw, _ := payload["releases"].([]any)
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		name, _ := item.(string)
		names = append(names, name)
	}
	return names
}

func TestRetentionEvictsOldestCompletedUntilWithinCap(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	seedRetentionRelease(t, repo, "first-release", "First.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "second-release", "Second.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"old":    {Hash: "old", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"middle": {Hash: "middle", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"newest": {Hash: "newest", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-3 * time.Hour)
	seedRetentionDownload(t, repo, "first", "first-release", "qb:old", base, false, 1)
	seedRetentionDownload(t, repo, "second", "second-release", "qb:middle", base.Add(time.Hour), false, 1)
	seedRetentionDownload(t, repo, "third", "third-release", "qb:newest", base.Add(2*time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)

	job, err := service.RunRetention()
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != "retention" || job.DedupeKey != "retention" || job.State != "completed" {
		t.Fatalf("retention job did not complete: %#v", job)
	}
	if len(engine.removed) != 2 || engine.removed[0] != "old" || engine.removed[1] != "middle" {
		t.Fatalf("eviction did not take oldest completed first: %v", engine.removed)
	}
	if slices.Contains(engine.removedFiles, false) {
		t.Fatal("eviction must delete files like the manual remove action")
	}
	for _, id := range []string{"first", "second"} {
		if _, err := repo.GetDownload(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("evicted row %s survived: %v", id, err)
		}
	}
	if _, err := repo.GetDownload(context.Background(), "third"); err != nil {
		t.Fatalf("uninvolved download was evicted: %v", err)
	}
	payload := retentionEvents(t, service)
	found := false
	for _, event := range payload {
		if event["reason"] == "cap" && slices.Contains(evictionReleases(t, event), "First.S01.1080p.WEB-DL") {
			found = true
			titles, _ := event["titles"].([]any)
			if len(titles) != 1 || titles[0] == "" {
				t.Fatalf("eviction event lacks a title: %v", event)
			}
		}
	}
	if !found {
		t.Fatalf("no cap eviction event naming the oldest release: %v", payload)
	}
}

func TestRetentionReserveBreachEvictsWhenCapSatisfied(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 100, 1)
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"only": {Hash: "only", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	seedRetentionDownload(t, repo, "solo", "solo-release", "qb:only", time.Now().UTC().Add(-time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)
	free := int64(512 << 20)
	service.freeSpace = func(string) (int64, error) { return free + engine.freedSnapshot(), nil }

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "only" {
		t.Fatalf("reserve breach did not evict the torrent: %v", engine.removed)
	}
	if _, err := repo.GetDownload(context.Background(), "solo"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("evicted row survived: %v", err)
	}
	payload := retentionEvents(t, service)
	if len(payload) != 1 || payload[0]["reason"] != "reserve" {
		t.Fatalf("eviction event reason = %v, want reserve", payload)
	}
	// Reserve is met now; a second pass must not evict anything further.
	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 1 {
		t.Fatalf("satisfied reserve evicted again: %v", engine.removed)
	}
}

func TestRetentionNeverEvictsIncompleteOrLeased(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"incomplete": {Hash: "incomplete", State: "downloading", Progress: 0.5, TotalBytes: 600 << 20},
		"leased":     {Hash: "leased", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"eligible":   {Hash: "eligible", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-3 * time.Hour)
	seedRetentionDownload(t, repo, "watching", "watching-release", "qb:leased", base, true, 1)
	seedRetentionDownload(t, repo, "fetching", "fetching-release", "qb:incomplete", base.Add(time.Hour), false, 0.5)
	seedRetentionDownload(t, repo, "spare", "spare-release", "qb:eligible", base.Add(2*time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)

	job, err := service.RunRetention()
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "completed" {
		t.Fatalf("retention job state = %q, want completed", job.State)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "eligible" {
		t.Fatalf("protected downloads were evicted: %v", engine.removed)
	}
	for _, id := range []string{"watching", "fetching"} {
		if _, err := repo.GetDownload(context.Background(), id); err != nil {
			t.Fatalf("protected row %s did not survive: %v", id, err)
		}
	}
}

func TestRetentionEvictsSeasonPackSiblingsTogether(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	seedRetentionRelease(t, repo, "pack-release", "Pack.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"pack":  {Hash: "pack", State: "pausedUP", Progress: 1, TotalBytes: 800 << 20},
		"loner": {Hash: "loner", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-2 * time.Hour)
	seedRetentionDownload(t, repo, "ep1", "pack-release", "qb:pack", base, false, 1)
	seedRetentionDownload(t, repo, "ep2", "pack-release", "qb:pack", base.Add(time.Second), false, 1)
	seedRetentionDownload(t, repo, "film", "film-release", "qb:loner", base.Add(time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "pack" {
		t.Fatalf("eviction picked the wrong torrent: %v", engine.removed)
	}
	for _, id := range []string{"ep1", "ep2"} {
		if _, err := repo.GetDownload(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("season-pack sibling %s survived the eviction: %v", id, err)
		}
	}
	if _, err := repo.GetDownload(context.Background(), "film"); err != nil {
		t.Fatalf("uninvolved download was evicted: %v", err)
	}
	payload := retentionEvents(t, service)
	if len(payload) != 1 {
		t.Fatalf("expected one eviction event, got %d", len(payload))
	}
	if releases := evictionReleases(t, payload[0]); len(releases) != 1 || releases[0] != "Pack.S01.1080p.WEB-DL" {
		t.Fatalf("eviction event names the wrong release: %v", payload[0])
	}
}

func TestRetentionZeroSettingsDisableChecks(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 0, 0)
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"big": {Hash: "big", State: "pausedUP", Progress: 1, TotalBytes: 500 << 30},
	}}
	seedRetentionDownload(t, repo, "huge", "huge-release", "qb:big", time.Now().UTC().Add(-time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)
	service.freeSpace = func(string) (int64, error) { return 0, nil }

	job, err := service.RunRetention()
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "completed" || len(engine.removed) != 0 {
		t.Fatalf("zero-valued settings must disable both checks: state=%q removed=%v", job.State, engine.removed)
	}
	if _, err := repo.GetDownload(context.Background(), "huge"); err != nil {
		t.Fatalf("download was evicted despite disabled checks: %v", err)
	}
}

// — Ticket #49: composable eviction rules and protection toggles. Ordering is
// a user-composed rule list evaluated against the settings of the current run;
// protections are user toggles (ADR-0004) with the #48 defaults preserved.

func evictionSettings(t *testing.T, store *config.Store, mutate func(*config.Settings)) {
	t.Helper()
	value := store.Get()
	mutate(&value)
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
}

func seedFavorite(t *testing.T, repo *sqlite.Repository, releaseID string) {
	t.Helper()
	titles, err := repo.CatalogTitleIDsForReleases(context.Background(), []string{releaseID})
	if err != nil {
		t.Fatal(err)
	}
	titleID := titles[releaseID]
	if titleID == "" {
		t.Fatalf("release %s resolved no canonical title", releaseID)
	}
	if err := repo.SetFavorite(context.Background(), householdProfile, titleID, true); err != nil {
		t.Fatal(err)
	}
}

func seedPlayback(t *testing.T, repo *sqlite.Repository, downloadID, releaseID string, watched bool, playedAt time.Time) {
	t.Helper()
	row := domain.PlaybackState{ProfileID: householdProfile, SourceID: downloadID, ReleaseID: releaseID, PositionMS: 1, DurationMS: 100, Watched: watched, UpdatedAt: playedAt}
	if err := repo.SavePlayback(context.Background(), row); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionNewestCompletedRuleEvictsNewestFirst(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	evictionSettings(t, settings, func(value *config.Settings) { value.EvictionRules = []string{"newest-completed"} })
	seedRetentionRelease(t, repo, "first-release", "First.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "second-release", "Second.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "third-release", "Third.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"old":    {Hash: "old", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"middle": {Hash: "middle", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"newest": {Hash: "newest", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-3 * time.Hour)
	seedRetentionDownload(t, repo, "first", "first-release", "qb:old", base, false, 1)
	seedRetentionDownload(t, repo, "second", "second-release", "qb:middle", base.Add(time.Hour), false, 1)
	seedRetentionDownload(t, repo, "third", "third-release", "qb:newest", base.Add(2*time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 2 || engine.removed[0] != "newest" || engine.removed[1] != "middle" {
		t.Fatalf("newest-completed did not evict the newest download first: %v", engine.removed)
	}
}

func TestRetentionLeastRecentlyPlayedFallsBackToDownloadAge(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	evictionSettings(t, settings, func(value *config.Settings) { value.EvictionRules = []string{"least-recently-played"} })
	seedRetentionRelease(t, repo, "stale-release", "Stale.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "played-release", "Played.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "fresh-release", "Fresh.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"stale":  {Hash: "stale", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"played": {Hash: "played", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"fresh":  {Hash: "fresh", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-6 * time.Hour)
	seedRetentionDownload(t, repo, "stale-row", "stale-release", "qb:stale", base, false, 1)
	seedRetentionDownload(t, repo, "played-row", "played-release", "qb:played", base.Add(2*time.Hour), false, 1)
	seedRetentionDownload(t, repo, "fresh-row", "fresh-release", "qb:fresh", base.Add(4*time.Hour), false, 1)
	seedPlayback(t, repo, "played-row", "played-release", false, base.Add(time.Hour))
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	// The never-played stale route competes by its download age; the played
	// route recency comes from the household playback state, so the fresh
	// never-played route survives longest.
	if len(engine.removed) != 2 || engine.removed[0] != "stale" || engine.removed[1] != "played" {
		t.Fatalf("least-recently-played ignored the never-played fallback: %v", engine.removed)
	}
}

func TestRetentionWatchedFirstRuleEvictsWatched(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	evictionSettings(t, settings, func(value *config.Settings) { value.EvictionRules = []string{"watched-first"} })
	seedRetentionRelease(t, repo, "seen-release", "Seen.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "unseen-release", "Unseen.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"seen":   {Hash: "seen", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"unseen": {Hash: "unseen", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-2 * time.Hour)
	seedRetentionDownload(t, repo, "seen-row", "seen-release", "qb:seen", base, false, 1)
	seedRetentionDownload(t, repo, "unseen-row", "unseen-release", "qb:unseen", base.Add(time.Hour), false, 1)
	seedPlayback(t, repo, "seen-row", "seen-release", true, base.Add(30*time.Minute))
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "seen" {
		t.Fatalf("watched-first did not evict the watched download: %v", engine.removed)
	}
}

func TestRetentionLargestAndSmallestRules(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	evictionSettings(t, settings, func(value *config.Settings) { value.EvictionRules = []string{"largest"} })
	seedRetentionRelease(t, repo, "small-release", "Small.720p.WEB-DL")
	seedRetentionRelease(t, repo, "mid-release", "Mid.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "big-release", "Big.2160p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"small": {Hash: "small", State: "pausedUP", Progress: 1, TotalBytes: 400 << 20},
		"mid":   {Hash: "mid", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"big":   {Hash: "big", State: "pausedUP", Progress: 1, TotalBytes: 900 << 20},
	}}
	base := time.Now().UTC().Add(-3 * time.Hour)
	seedRetentionDownload(t, repo, "small-row", "small-release", "qb:small", base, false, 1)
	seedRetentionDownload(t, repo, "mid-row", "mid-release", "qb:mid", base.Add(time.Hour), false, 1)
	seedRetentionDownload(t, repo, "big-row", "big-release", "qb:big", base.Add(2*time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "big" {
		t.Fatalf("largest did not evict the biggest download: %v", engine.removed)
	}

	repo2, settings2 := retryHarness(t)
	retentionSettings(t, settings2, 1, 0)
	evictionSettings(t, settings2, func(value *config.Settings) { value.EvictionRules = []string{"smallest"} })
	seedRetentionRelease(t, repo2, "small-release", "Small.720p.WEB-DL")
	seedRetentionRelease(t, repo2, "mid-release", "Mid.1080p.WEB-DL")
	engine2 := &retentionEngine{status: map[string]domain.DownloadStatus{
		"small": {Hash: "small", State: "pausedUP", Progress: 1, TotalBytes: 400 << 20},
		"mid":   {Hash: "mid", State: "pausedUP", Progress: 1, TotalBytes: 700 << 20},
	}}
	seedRetentionDownload(t, repo2, "small-row", "small-release", "qb:small", base, false, 1)
	seedRetentionDownload(t, repo2, "mid-row", "mid-release", "qb:mid", base.Add(time.Hour), false, 1)
	service2 := NewService(openCatalog{}, engine2, repo2, settings2)

	if _, err := service2.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine2.removed) != 1 || engine2.removed[0] != "small" {
		t.Fatalf("smallest did not evict the least capacious download: %v", engine2.removed)
	}
}

func TestRetentionRuleTiesBreakToOldestCompletedThenEngineID(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	evictionSettings(t, settings, func(value *config.Settings) { value.EvictionRules = []string{"largest"} })
	seedRetentionRelease(t, repo, "elder-release", "Elder.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "younger-release", "Younger.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"elder":   {Hash: "elder", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"younger": {Hash: "younger", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-3 * time.Hour)
	seedRetentionDownload(t, repo, "elder-row", "elder-release", "qb:elder", base, false, 1)
	seedRetentionDownload(t, repo, "younger-row", "younger-release", "qb:younger", base.Add(time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "elder" {
		t.Fatalf("a rule tie did not break to the oldest completed download: %v", engine.removed)
	}

	// Equal rule keys and equal completion ages fall back to the EngineID.
	repo2, settings2 := retryHarness(t)
	retentionSettings(t, settings2, 1, 0)
	evictionSettings(t, settings2, func(value *config.Settings) { value.EvictionRules = []string{"largest"} })
	seedRetentionRelease(t, repo2, "zeta-release", "Zeta.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo2, "alpha-release", "Alpha.S01.1080p.WEB-DL")
	engine2 := &retentionEngine{status: map[string]domain.DownloadStatus{
		"zeta":  {Hash: "zeta", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"alpha": {Hash: "alpha", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	same := time.Now().UTC().Add(-2 * time.Hour)
	seedRetentionDownload(t, repo2, "zeta-row", "zeta-release", "qb:zeta", same, false, 1)
	seedRetentionDownload(t, repo2, "alpha-row", "alpha-release", "qb:alpha", same, false, 1)
	service2 := NewService(openCatalog{}, engine2, repo2, settings2)

	if _, err := service2.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine2.removed) != 1 || engine2.removed[0] != "alpha" {
		t.Fatalf("a full rule tie did not break to the EngineID: %v", engine2.removed)
	}
}

func TestRetentionFavoriteProtectionFollowsTheToggle(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	seedRetentionRelease(t, repo, "fav-release", "Fav.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "plain-release", "Plain.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"fav":   {Hash: "fav", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"plain": {Hash: "plain", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-2 * time.Hour)
	seedRetentionDownload(t, repo, "fav-row", "fav-release", "qb:fav", base, false, 1)
	seedRetentionDownload(t, repo, "plain-row", "plain-release", "qb:plain", base.Add(time.Hour), false, 1)
	seedFavorite(t, repo, "fav-release")
	service := NewService(openCatalog{}, engine, repo, settings)

	// Default (protectFavorites off): the favorite is ordinary eviction stock.
	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 1 || engine.removed[0] != "fav" {
		t.Fatalf("favorites must not be protected by default: %v", engine.removed)
	}

	repo2, settings2 := retryHarness(t)
	retentionSettings(t, settings2, 1, 0)
	evictionSettings(t, settings2, func(value *config.Settings) { value.ProtectFavorites = true })
	seedRetentionRelease(t, repo2, "fav-release", "Fav.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo2, "plain-release", "Plain.S01.1080p.WEB-DL")
	engine2 := &retentionEngine{status: map[string]domain.DownloadStatus{
		"fav":   {Hash: "fav", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"plain": {Hash: "plain", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	seedRetentionDownload(t, repo2, "fav-row", "fav-release", "qb:fav", base, false, 1)
	seedRetentionDownload(t, repo2, "plain-row", "plain-release", "qb:plain", base.Add(time.Hour), false, 1)
	seedFavorite(t, repo2, "fav-release")
	service2 := NewService(openCatalog{}, engine2, repo2, settings2)

	if _, err := service2.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine2.removed) != 1 || engine2.removed[0] != "plain" {
		t.Fatalf("protectFavorites did not spare the favorite: %v", engine2.removed)
	}
	if _, err := repo2.GetDownload(context.Background(), "fav-row"); err != nil {
		t.Fatalf("the favorite row did not survive: %v", err)
	}
}

func TestRetentionNeverWatchedProtectionToggle(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	evictionSettings(t, settings, func(value *config.Settings) { value.ProtectNeverWatched = true })
	seedRetentionRelease(t, repo, "unplayed-release", "Unplayed.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "partial-release", "Partial.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "played-release", "Played.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"unplayed": {Hash: "unplayed", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"partial":  {Hash: "partial", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"finished": {Hash: "finished", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-2 * time.Hour)
	seedRetentionDownload(t, repo, "unplayed-row", "unplayed-release", "qb:unplayed", base, false, 1)
	seedRetentionDownload(t, repo, "partial-row", "partial-release", "qb:partial", base.Add(time.Hour), false, 1)
	seedRetentionDownload(t, repo, "finished-row", "played-release", "qb:finished", base.Add(2*time.Hour), false, 1)
	seedPlayback(t, repo, "partial-row", "partial-release", false, base.Add(20*time.Minute))
	seedPlayback(t, repo, "finished-row", "played-release", true, base.Add(30*time.Minute))
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	// Never-watched covers everything without the watched flag: the untouched
	// download and the partially played one both survive; the finished title
	// alone is evictable.
	if len(engine.removed) != 1 || engine.removed[0] != "finished" {
		t.Fatalf("protectNeverWatched did not spare the never-watched downloads: %v", engine.removed)
	}
	for _, id := range []string{"unplayed-row", "partial-row"} {
		if _, err := repo.GetDownload(context.Background(), id); err != nil {
			t.Fatalf("the never-watched row %s did not survive: %v", id, err)
		}
	}
}

func TestRetentionProtectionTogglesCanReleaseIncompleteAndLeased(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 0.5, 0)
	evictionSettings(t, settings, func(value *config.Settings) {
		value.ProtectIncomplete = false
		value.ProtectLeased = false
	})
	seedRetentionRelease(t, repo, "partial-release", "Partial.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "streamed-release", "Streamed.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"partial":  {Hash: "partial", State: "downloading", Progress: 0.5, TotalBytes: 600 << 20},
		"streamed": {Hash: "streamed", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-2 * time.Hour)
	seedRetentionDownload(t, repo, "partial-row", "partial-release", "qb:partial", base, false, 0.5)
	seedRetentionDownload(t, repo, "streamed-row", "streamed-release", "qb:streamed", base.Add(time.Hour), true, 1)
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 2 || engine.removed[0] != "partial" || engine.removed[1] != "streamed" {
		t.Fatalf("disabled toggles must let incomplete and leased downloads evict: %v", engine.removed)
	}
	for _, id := range []string{"partial-row", "streamed-row"} {
		if _, err := repo.GetDownload(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unprotected row %s survived: %v", id, err)
		}
	}
}

func TestRetentionEmptyRulesFallBackToOldestCompleted(t *testing.T) {
	repo, settings := retryHarness(t)
	retentionSettings(t, settings, 1, 0)
	evictionSettings(t, settings, func(value *config.Settings) { value.EvictionRules = nil })
	seedRetentionRelease(t, repo, "first-release", "First.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "second-release", "Second.S01.1080p.WEB-DL")
	seedRetentionRelease(t, repo, "third-release", "Third.S01.1080p.WEB-DL")
	engine := &retentionEngine{status: map[string]domain.DownloadStatus{
		"old":    {Hash: "old", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"middle": {Hash: "middle", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
		"newest": {Hash: "newest", State: "pausedUP", Progress: 1, TotalBytes: 600 << 20},
	}}
	base := time.Now().UTC().Add(-3 * time.Hour)
	seedRetentionDownload(t, repo, "first", "first-release", "qb:old", base, false, 1)
	seedRetentionDownload(t, repo, "second", "second-release", "qb:middle", base.Add(time.Hour), false, 1)
	seedRetentionDownload(t, repo, "third", "third-release", "qb:newest", base.Add(2*time.Hour), false, 1)
	service := NewService(openCatalog{}, engine, repo, settings)

	if _, err := service.RunRetention(); err != nil {
		t.Fatal(err)
	}
	if len(engine.removed) != 2 || engine.removed[0] != "old" || engine.removed[1] != "middle" {
		t.Fatalf("an empty rule list must fall back to oldest-completed: %v", engine.removed)
	}
}
