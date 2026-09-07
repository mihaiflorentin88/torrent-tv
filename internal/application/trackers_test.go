package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// strictTracker is a provider adapter whose every method records the call and
// refuses to serve. The disabled-tracker regressions use it to prove that
// managed playback and the disabled-provider guard never reach the adapter.
type strictTracker struct {
	Tracker
	calls atomic.Int64
}

func (s *strictTracker) ID() string   { return "filelist" }
func (s *strictTracker) Name() string { return "FileList" }

func (s *strictTracker) Categories() []domain.TrackerCategory {
	s.calls.Add(1)
	return nil
}

func (s *strictTracker) Latest(context.Context) ([]domain.TorrentRelease, error) {
	s.calls.Add(1)
	return nil, errors.New("provider must not be called")
}

func (s *strictTracker) Category(context.Context, string) ([]domain.TorrentRelease, error) {
	s.calls.Add(1)
	return nil, errors.New("provider must not be called")
}

func (s *strictTracker) Search(context.Context, string) ([]domain.TorrentRelease, error) {
	s.calls.Add(1)
	return nil, errors.New("provider must not be called")
}

func (s *strictTracker) Acquire(context.Context, string) (domain.TorrentAcquisition, error) {
	s.calls.Add(1)
	return domain.TorrentAcquisition{}, errors.New("provider acquisition must not happen")
}

// fakeTracker is a programmable provider for the concurrency tests: Search
// blocks on an optional gate channel, so tests coordinate with channels
// instead of sleeps, and enabled flips live to model a settings change.
type fakeTracker struct {
	Tracker
	id, name     string
	enabled      atomic.Bool
	gate         chan struct{}
	entered      chan struct{}
	enterOnce    sync.Once
	items        []domain.TorrentRelease
	err          error
	acquires     atomic.Int64
	gateConsumed chan struct{}
	consumeOnce  sync.Once
}

func newFakeTracker(id, name string, items []domain.TorrentRelease) *fakeTracker {
	f := &fakeTracker{id: id, name: name, items: items, entered: make(chan struct{}), gateConsumed: make(chan struct{})}
	f.enabled.Store(true)
	return f
}

func (f *fakeTracker) ID() string   { return f.id }
func (f *fakeTracker) Name() string { return f.name }

func (f *fakeTracker) Capabilities() TrackerCapabilities { return TrackerCapabilities{} }

func (f *fakeTracker) Categories() []domain.TrackerCategory { return nil }

func (f *fakeTracker) Latest(ctx context.Context) ([]domain.TorrentRelease, error) {
	return f.Search(ctx, "")
}

func (f *fakeTracker) Category(ctx context.Context, _ string) ([]domain.TorrentRelease, error) {
	return f.Search(ctx, "")
}

func (f *fakeTracker) Search(ctx context.Context, _ string) ([]domain.TorrentRelease, error) {
	f.enterOnce.Do(func() { close(f.entered) })
	if f.gate != nil {
		select {
		case <-f.gate:
			f.consumeOnce.Do(func() { close(f.gateConsumed) })
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func (f *fakeTracker) Acquire(context.Context, string) (domain.TorrentAcquisition, error) {
	f.acquires.Add(1)
	return domain.TorrentAcquisition{}, errors.New("no acquisition expected in search tests")
}

func registryOf(t *testing.T, registrations ...TrackerRegistration) *TrackerRegistry {
	t.Helper()
	registry, err := NewTrackerRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func enabledRegistration(adapter Tracker) TrackerRegistration {
	return TrackerRegistration{Adapter: adapter, Enabled: func() bool { return true }, Configured: func() bool { return true }}
}

func fakeRegistration(f *fakeTracker) TrackerRegistration {
	return TrackerRegistration{
		Adapter:    f,
		Enabled:    func() bool { return f.enabled.Load() },
		Configured: func() bool { return true },
	}
}

// trackerHarness mirrors retryHarness with a configurable job bound and a
// stable database path so tests can close and reopen the stack.
type trackerHarness struct {
	dir      string
	settings *config.Store
	repoPath string
}

func newTrackerHarness(t *testing.T, maxJobs int) *trackerHarness {
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
	value.DownloadRoot = filepath.Join(dir, "downloads")
	value.MaxConcurrentJobs = maxJobs
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(value.DownloadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	return &trackerHarness{dir: dir, settings: settings, repoPath: filepath.Join(dir, "state.db")}
}

func (h *trackerHarness) openRepo(t *testing.T) *sqlite.Repository {
	t.Helper()
	repo, err := sqlite.Open(h.repoPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func (h *trackerHarness) newService(t *testing.T, registry *TrackerRegistry, engine TorrentEngine, repo *sqlite.Repository) *Service {
	t.Helper()
	service := NewService(registry, engine, repo, h.settings)
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	return service
}

func writeMediaFile(t *testing.T, path string, size int) []byte {
	t.Helper()
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i%251 + 1)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return content
}

func seedCompletedDownload(t *testing.T, repo *sqlite.Repository, release domain.TorrentRelease, filePath string, size int64) (domain.TorrentRelease, domain.Download) {
	t.Helper()
	stored, err := repo.UpsertReleases(context.Background(), []domain.TorrentRelease{release})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	download := domain.Download{
		ID:           sourceID(stored[0].ID, filePath),
		ReleaseID:    stored[0].ID,
		EngineID:     "qb:seededhash",
		FileIndex:    0,
		FilePath:     filePath,
		AbsolutePath: filePath,
		SizeBytes:    size,
		Progress:     1,
		State:        "pausedUP",
		TrackerID:    stored[0].TrackerID,
		TrackerName:  stored[0].TrackerName,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.SaveDownload(context.Background(), download); err != nil {
		t.Fatal(err)
	}
	return stored[0], download
}

// waitEvent reads SSE events until payload satisfies match; bounded, channel
// driven, no sleeps.
func waitEvent(t *testing.T, events <-chan domain.Event, kind string, match func(payload map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("event channel closed while waiting for %s", kind)
			}
			if event.Kind != kind {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
				t.Fatalf("%s payload is not JSON: %v", kind, err)
			}
			if match == nil || match(payload) {
				return payload
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %s event", kind)
		}
	}
}

func eventJob(payload map[string]any) domain.Job {
	raw, _ := payload["job"].(map[string]any)
	job := domain.Job{}
	if raw == nil {
		return job
	}
	if id, ok := raw["id"].(string); ok {
		job.ID = id
	}
	if state, ok := raw["state"].(string); ok {
		job.State = state
	}
	return job
}

func waitForTrackerSearchCompletion(t *testing.T, service *Service, events <-chan domain.Event, parentID string) domain.Job {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		job, err := service.Job(context.Background(), parentID)
		if err == nil && (job.State == "completed" || job.State == "failed") {
			return job
		}
		select {
		case <-events:
		case <-deadline:
			t.Fatalf("timed out waiting for tracker search job %s to settle", parentID)
		}
	}
}

func disabledTrackerRegistration(t *testing.T, strict *strictTracker) TrackerRegistration {
	t.Helper()
	return TrackerRegistration{Adapter: strict, Enabled: func() bool { return false }, Configured: func() bool { return true }}
}

func assertPlayableRange(t *testing.T, service *Service, download domain.Download, want []byte) {
	t.Helper()
	path, err := service.ReadableRangePath(context.Background(), download, 0, 4096)
	if err != nil {
		t.Fatalf("completed file must stream without the provider: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 4096 || string(data[:4096]) != string(want[:4096]) {
		t.Fatal("streamed bytes do not match the seeded media file")
	}
}

func assertResumePositionPreserved(t *testing.T, repo *sqlite.Repository, downloadID string, wantMS int64) {
	t.Helper()
	state, err := repo.GetPlayback(context.Background(), householdProfile, downloadID)
	if err != nil {
		t.Fatalf("resume position must survive prepare: %v", err)
	}
	if state.PositionMS != wantMS {
		t.Fatalf("resume position = %d, want %d", state.PositionMS, wantMS)
	}
}

// TestDisabledTrackerManagedMovieRestart is the load-bearing regression: a
// completed managed movie plays (and keeps its resume position) while its
// tracker is disabled, across a full service restart, without a single call
// into the disabled adapter.
func TestDisabledTrackerManagedMovieRestart(t *testing.T) {
	h := newTrackerHarness(t, 4)
	strict := &strictTracker{}
	mediaPath := filepath.Join(h.dir, "downloads", "Movie.2024.1080p.WEB-DL.mkv")
	content := writeMediaFile(t, mediaPath, 1<<20)

	build := func() (*TrackerRegistry, TorrentEngine) {
		return registryOf(t, disabledTrackerRegistration(t, strict)), &strictEngine{t: t}
	}

	run := func(repo *sqlite.Repository, registry *TrackerRegistry, engine TorrentEngine, releaseID string) domain.Download {
		service := h.newService(t, registry, engine, repo)
		download, err := service.Prepare(context.Background(), releaseID, 0)
		if err != nil {
			t.Fatalf("managed playback must not require an enabled tracker: %v", err)
		}
		if download.ID == "" {
			t.Fatal("prepare returned an empty download")
		}
		assertPlayableRange(t, service, download, content)
		return download
	}

	// Seed on the first stack.
	registry, engine := build()
	repo := h.openRepo(t)
	release := domain.TorrentRelease{
		ID: "filelist:movie1", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "movie1",
		Name: "Movie.2024.1080p.WEB-DL", Category: "Movies HD", FileCount: 1,
	}
	stored, download := seedCompletedDownload(t, repo, release, mediaPath, int64(len(content)))
	manifest := domain.TorrentManifest{
		ReleaseID: stored.ID,
		Files:     []domain.TorrentFile{{Index: 0, Path: filepath.Base(mediaPath), SizeBytes: int64(len(content)), Playable: true}},
		Metainfo:  []byte("d4:infod6:lengthi" + itoa64(int64(len(content))) + "ee"),
		FetchedAt: time.Now().UTC(),
	}
	if err := repo.SaveTorrentManifest(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePlayback(context.Background(), domain.PlaybackState{ProfileID: householdProfile, SourceID: download.ID, ReleaseID: stored.ID, PositionMS: 123000, DurationMS: 7200000, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	first := run(repo, registry, engine, stored.ID)
	if strict.calls.Load() != 0 {
		t.Fatalf("disabled adapter was called %d times during managed playback", strict.calls.Load())
	}

	// Reopen the repository and the service on the same database.
	repo.Close()
	registry2, engine2 := build()
	repo2 := h.openRepo(t)
	second := run(repo2, registry2, engine2, stored.ID)
	if second.ID != first.ID {
		t.Fatalf("restarted prepare returned a different source: %s vs %s", second.ID, first.ID)
	}
	assertResumePositionPreserved(t, repo2, first.ID, 123000)
	if strict.calls.Load() != 0 {
		t.Fatalf("disabled adapter was called %d times across restart", strict.calls.Load())
	}
}

// strictEngine fails the test if any engine operation runs; the managed
// regressions must resolve from persisted state alone.
type strictEngine struct {
	TorrentEngine
	t *testing.T
}

func (e *strictEngine) Test(context.Context) (string, error) {
	e.t.Error("engine must not be used")
	return "", errors.New("no engine")
}

func (e *strictEngine) Add(context.Context, io.Reader, string) (string, error) {
	e.t.Error("engine must not be used")
	return "", errors.New("no engine")
}

func (e *strictEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	e.t.Error("engine must not be used")
	return nil, errors.New("no engine")
}

func (e *strictEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	e.t.Error("engine must not be used")
	return domain.DownloadStatus{}, errors.New("no engine")
}

func (e *strictEngine) Pieces(context.Context, string) (domain.PieceMap, error) {
	e.t.Error("engine must not be used")
	return domain.PieceMap{}, errors.New("no engine")
}

func (e *strictEngine) PrepareFile(context.Context, string, int, []int) error {
	e.t.Error("engine must not be used")
	return errors.New("no engine")
}

func (e *strictEngine) PrepareFiles(context.Context, string, []int, []int) error {
	e.t.Error("engine must not be used")
	return errors.New("no engine")
}

func (e *strictEngine) PrepareRange(context.Context, string, int, int64, int64) error {
	e.t.Error("engine must not be used")
	return errors.New("no engine")
}

func (e *strictEngine) Pause(context.Context, string) error {
	e.t.Error("engine must not be used")
	return errors.New("no engine")
}

func (e *strictEngine) ResolveMagnet(context.Context, string, string) ([]byte, error) {
	e.t.Error("engine must not be used")
	return nil, errors.New("no engine")
}

func (e *strictEngine) Resume(context.Context, string) error {
	e.t.Error("engine must not be used")
	return errors.New("no engine")
}

func (e *strictEngine) Remove(context.Context, string, bool) error {
	e.t.Error("engine must not be used")
	return errors.New("no engine")
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

// TestDisabledTrackerManagedEpisodeSiblingRestart covers the season-pack
// sibling: both episode files are managed and completed, so preparing the
// second episode while the tracker is disabled must serve the persisted
// sibling without the provider or the engine.
func TestDisabledTrackerManagedEpisodeSiblingRestart(t *testing.T) {
	h := newTrackerHarness(t, 4)
	strict := &strictTracker{}
	registry := registryOf(t, disabledTrackerRegistration(t, strict))

	repo := h.openRepo(t)
	release := domain.TorrentRelease{
		ID: "filelist:pack1", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "pack1",
		Name: "Show.S01.1080p.WEB-DL", Category: "Series HD", FileCount: 2,
	}
	stored, err := repo.UpsertReleases(context.Background(), []domain.TorrentRelease{release})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	episodes := []struct {
		index int
		name  string
		size  int
	}{
		{0, "Show.S01E01.1080p.mkv", 1 << 20},
		{1, "Show.S01E02.1080p.mkv", 1 << 20},
	}
	downloads := make([]domain.Download, len(episodes))
	files := make([]domain.TorrentFile, len(episodes))
	for i, episode := range episodes {
		path := filepath.Join(h.dir, "downloads", episode.name)
		writeMediaFile(t, path, episode.size)
		downloads[i] = domain.Download{
			ID: sourceID(stored[0].ID, episode.name), ReleaseID: stored[0].ID, EngineID: "qb:packhash",
			FileIndex: episode.index, FilePath: episode.name, AbsolutePath: path,
			SizeBytes: int64(episode.size), Progress: 1, State: "pausedUP",
			TrackerID: "filelist", TrackerName: "FileList", CreatedAt: now, UpdatedAt: now,
		}
		if err := repo.SaveDownload(context.Background(), downloads[i]); err != nil {
			t.Fatal(err)
		}
		files[i] = domain.TorrentFile{Index: episode.index, Path: episode.name, SizeBytes: int64(episode.size), Playable: true}
	}
	if err := repo.SaveTorrentManifest(context.Background(), domain.TorrentManifest{ReleaseID: stored[0].ID, Files: files, Metainfo: []byte("d8:infod5:filesee"), FetchedAt: now}); err != nil {
		t.Fatal(err)
	}

	run := func(service *Service, index int) domain.Download {
		download, err := service.Prepare(context.Background(), stored[0].ID, index)
		if err != nil {
			t.Fatalf("managed sibling episode must play while disabled: %v", err)
		}
		assertPlayableRange(t, service, download, mustRead(t, downloads[index].AbsolutePath))
		return download
	}

	service := h.newService(t, registry, &strictEngine{t: t}, repo)
	second := run(service, 1)
	if strict.calls.Load() != 0 {
		t.Fatalf("disabled adapter was called %d times for a managed sibling", strict.calls.Load())
	}

	repo.Close()
	repo2 := h.openRepo(t)
	service2 := h.newService(t, registryOf(t, disabledTrackerRegistration(t, strict)), &strictEngine{t: t}, repo2)
	first := run(service2, 0)
	if first.ID != downloads[0].ID || second.ID != downloads[1].ID {
		t.Fatalf("sibling prepares returned unexpected sources: %s, %s", first.ID, second.ID)
	}
	if strict.calls.Load() != 0 {
		t.Fatalf("disabled adapter was called %d times across restart", strict.calls.Load())
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestDisabledTrackerMissingContentDoesNotAcquire proves the guard order:
// with no managed download and no engine torrent, preparation must fail at
// RequireEligible with the tracker disabled — never reach provider
// acquisition — for both Prepare and PrepareSeason.
func TestDisabledTrackerMissingContentDoesNotAcquire(t *testing.T) {
	h := newTrackerHarness(t, 4)
	strict := &strictTracker{}
	registry := registryOf(t, disabledTrackerRegistration(t, strict))
	repo := h.openRepo(t)

	release := domain.TorrentRelease{
		ID: "filelist:gone1", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "gone1",
		Name: "Show.S02.1080p.WEB-DL", Category: "Series HD", FileCount: 8,
	}
	stored, err := repo.UpsertReleases(context.Background(), []domain.TorrentRelease{release})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveTorrentManifest(context.Background(), domain.TorrentManifest{ReleaseID: stored[0].ID, Files: []domain.TorrentFile{{Index: 0, Path: "Show.S02E01.mkv", SizeBytes: 1 << 20, Playable: true}}, FetchedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	service := h.newService(t, registry, &strictEngine{t: t}, repo)
	if _, err := service.Prepare(context.Background(), stored[0].ID, 0); !errors.Is(err, domain.ErrTrackerDisabled) {
		t.Fatalf("missing content with a disabled tracker must fail at the eligibility guard, got %v", err)
	}
	if _, err := service.PrepareSeason(context.Background(), stored[0].ID, 2); !errors.Is(err, domain.ErrTrackerDisabled) {
		t.Fatalf("season preparation with a disabled tracker must fail at the eligibility guard, got %v", err)
	}
	if strict.calls.Load() != 0 {
		t.Fatalf("disabled adapter was called %d times; the guard must precede acquisition", strict.calls.Load())
	}
}

// TestConcurrentTrackerProvidersFastVisibleBeforeSlow fans one search out to
// two providers; the fast provider's rows must be discoverable (and its SSE
// merge published) before the gated slow provider is released.
func TestConcurrentTrackerProvidersFastVisibleBeforeSlow(t *testing.T) {
	h := newTrackerHarness(t, 4)
	fast := newFakeTracker("fast", "Fast", []domain.TorrentRelease{{ID: "fast-1", Name: "Fast.Title.2024.1080p.WEB-DL", Seeders: 9, Category: "Movies HD"}})
	slow := newFakeTracker("slow", "Slow", []domain.TorrentRelease{{ID: "slow-1", Name: "Slow.Title.2023.2160p.WEB-DL", Seeders: 7, Category: "Movies 4K"}})
	slow.gate = make(chan struct{})
	registry := registryOf(t, fakeRegistration(fast), fakeRegistration(slow))
	repo := h.openRepo(t)
	service := h.newService(t, registry, nil, repo)

	events, cancel := service.SubscribeEvents()
	defer cancel()
	if _, err := service.QueueTrackerSearch(context.Background(), "shared query", false); err != nil {
		t.Fatal(err)
	}

	// Fast provider merges while slow is still gated: SSE first, then rows.
	waitEvent(t, events, "catalog.search.completed", func(payload map[string]any) bool {
		tracker, _ := payload["tracker"].(string)
		return tracker == "fast"
	})
	page, err := repo.ListReleases(context.Background(), "Fast.Title", "", 100, 0, []string{"fast"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("fast provider rows must be visible before the slow provider finishes, got %d", page.Total)
	}

	close(slow.gate)
	job := waitForTrackerSearchCompletion(t, service, events, searchJobKey("shared query"))
	if job.State != "completed" {
		t.Fatalf("parent search job should complete when both providers merge, got %s (%s)", job.State, job.Error)
	}
	page, err = repo.ListReleases(context.Background(), "Slow.Title", "", 100, 0, []string{"slow"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("slow provider rows must persist after release, got %d", page.Total)
	}
	if fast.acquires.Load() != 0 || slow.acquires.Load() != 0 {
		t.Fatal("search must not acquire torrents")
	}
}

// TestConcurrentTrackerProviderFailurePersistsOtherProvider: a failing
// provider must not roll back the successful one, and the parent job must
// name the failed child.
func TestConcurrentTrackerProviderFailurePersistsOtherProvider(t *testing.T) {
	h := newTrackerHarness(t, 4)
	fast := newFakeTracker("fast", "Fast", []domain.TorrentRelease{{ID: "fast-1", Name: "Kept.Title.2024.1080p.WEB-DL", Seeders: 9, Category: "Movies HD"}})
	slow := newFakeTracker("slow", "Slow", nil)
	slow.err = errors.New("slow provider exploded")
	registry := registryOf(t, fakeRegistration(fast), fakeRegistration(slow))
	repo := h.openRepo(t)
	service := h.newService(t, registry, nil, repo)

	if _, err := service.QueueTrackerSearch(context.Background(), "failure query", false); err != nil {
		t.Fatal(err)
	}
	events, cancel := service.SubscribeEvents()
	defer cancel()
	job := waitForTrackerSearchCompletion(t, service, events, searchJobKey("failure query"))
	if job.State != "completed" {
		t.Fatalf("one failed provider must not fail the parent when another merged, got %s", job.State)
	}
	if !containsAll(job.Error, "slow") {
		t.Fatalf("parent job must name the failed child, got %q", job.Error)
	}
	page, err := repo.ListReleases(context.Background(), "Kept.Title", "", 100, 0, []string{"fast"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("successful provider rows must persist, got %d", page.Total)
	}
}

// TestConcurrentTrackerProvidersSingleJobSlot pins the MaxConcurrentJobs=1
// case: the parent must not hold the only job slot while waiting for
// children, so a gated slow provider cannot deadlock the fast one.
func TestConcurrentTrackerProvidersSingleJobSlot(t *testing.T) {
	h := newTrackerHarness(t, 1)
	fast := newFakeTracker("fast", "Fast", []domain.TorrentRelease{{ID: "fast-1", Name: "Solo.Slot.2024.1080p.WEB-DL", Seeders: 9, Category: "Movies HD"}})
	slow := newFakeTracker("slow", "Slow", []domain.TorrentRelease{{ID: "slow-1", Name: "Solo.Slow.2023.2160p.WEB-DL", Seeders: 7, Category: "Movies 4K"}})
	registry := registryOf(t, fakeRegistration(fast), fakeRegistration(slow))
	repo := h.openRepo(t)
	service := h.newService(t, registry, nil, repo)

	events, cancel := service.SubscribeEvents()
	defer cancel()
	if _, err := service.QueueTrackerSearch(context.Background(), "single slot", false); err != nil {
		t.Fatal(err)
	}
	job := waitForTrackerSearchCompletion(t, service, events, searchJobKey("single slot"))
	if job.State != "completed" {
		t.Fatalf("both providers must complete under MaxConcurrentJobs=1, got %s (%s)", job.State, job.Error)
	}
	pageFast, err := repo.ListReleases(context.Background(), "Solo.Slot", "", 100, 0, []string{"fast"})
	if err != nil || pageFast.Total != 1 {
		t.Fatalf("fast provider rows must persist under MaxConcurrentJobs=1: total=%d, err=%v", pageFast.Total, err)
	}
	pageSlow, err := repo.ListReleases(context.Background(), "Solo.Slow", "", 100, 0, []string{"slow"})
	if err != nil || pageSlow.Total != 1 {
		t.Fatalf("slow provider rows must persist under MaxConcurrentJobs=1: total=%d, err=%v", pageSlow.Total, err)
	}
}

// TestTrackerEligibilityDisableDuringRequest flips a blocked provider to
// disabled before it can respond: it must contribute no discovery rows, no
// new acquisitions, and no new preparation.
func TestTrackerEligibilityDisableDuringRequest(t *testing.T) {
	h := newTrackerHarness(t, 4)
	gated := newFakeTracker("gated", "Gated", []domain.TorrentRelease{{ID: "gated-1", Name: "Late.Title.2024.1080p.WEB-DL", Seeders: 9, Category: "Movies HD"}})
	gated.gate = make(chan struct{})
	registry := registryOf(t, fakeRegistration(gated))
	repo := h.openRepo(t)
	service := h.newService(t, registry, nil, repo)

	if _, err := service.QueueTrackerSearch(context.Background(), "disable mid flight", false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gated.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("gated provider never reached its search")
	}
	gated.enabled.Store(false)
	close(gated.gate)
	events, cancel := service.SubscribeEvents()
	defer cancel()
	job := waitForTrackerSearchCompletion(t, service, events, searchJobKey("disable mid flight"))
	if job.State != "completed" {
		t.Fatalf("a disabled provider is a named skip, not a parent failure: %s (%s)", job.State, job.Error)
	}
	page, err := repo.ListReleases(context.Background(), "Late.Title", "", 100, 0, []string{"gated"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("disabled provider must contribute no discovery rows, got %d", page.Total)
	}
	if gated.acquires.Load() != 0 {
		t.Fatal("disabled provider must not acquire anything")
	}
}

// TestTrackerEligibilityZeroProviders pins that zero enabled trackers is a
// valid steady state: sync and search complete without rows or errors.
func TestTrackerEligibilityZeroProviders(t *testing.T) {
	h := newTrackerHarness(t, 4)
	registry := registryOf(t, disabledTrackerRegistration(t, &strictTracker{}))
	repo := h.openRepo(t)
	service := h.newService(t, registry, nil, repo)

	job, err := service.SyncCatalog("latest")
	if err != nil {
		t.Fatal(err)
	}
	waitForJobTerminal(t, service, job.ID)
	page, err := service.Search(context.Background(), "nothing here")
	if err != nil {
		t.Fatalf("search with zero providers must not error: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("search with zero providers must return no rows, got %d", len(page.Items))
	}
	releases, err := repo.ListReleases(context.Background(), "", "", 100, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if releases.Total != 0 {
		t.Fatalf("zero providers must keep discovery empty, got %d", releases.Total)
	}
}

func waitForJobTerminal(t *testing.T, service *Service, jobID string) domain.Job {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		job, err := service.Job(context.Background(), jobID)
		if err == nil && (job.State == "completed" || job.State == "failed") {
			return job
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for job %s to settle", jobID)
		}
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !stringContains(haystack, needle) {
			return false
		}
	}
	return true
}

func stringContains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

type failListDownloadsRepo struct {
	Repository
	err error
}

func (f failListDownloadsRepo) ListDownloads(ctx context.Context) ([]domain.Download, error) {
	return nil, f.err
}

func TestCatalogDetailAndSetTitleFavoriteSurfaceListDownloadsError(t *testing.T) {
	h := newTrackerHarness(t, 4)
	realRepo := h.openRepo(t)
	injected := errors.New("injected repository failure")
	failRepo := failListDownloadsRepo{Repository: realRepo, err: injected}
	reg, _ := NewTrackerRegistry(nil)
	service := NewService(reg, nil, failRepo, h.settings)

	ctx := context.Background()
	_, err := service.CatalogDetail(ctx, "title-missing")
	if !errors.Is(err, injected) {
		t.Fatalf("CatalogDetail error = %v, want %v", err, injected)
	}

	err = service.SetTitleFavorite(ctx, "title-missing", true)
	if !errors.Is(err, injected) {
		t.Fatalf("SetTitleFavorite error = %v, want %v", err, injected)
	}
}
