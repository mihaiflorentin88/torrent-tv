package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

func TestCatalogPaginationAndDownloadPersistence(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	items := []domain.TorrentRelease{
		{ID: "filelist:1", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "1", Name: "Old", Category: "Movies HD", UploadedAt: &now},
		{ID: "filelist:2", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "2", Name: "New", Category: "Movies 4K", UploadedAt: &now},
	}
	stored, err := r.UpsertReleases(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := r.ListCatalogSources(ctx, []string{"filelist"})
	if err != nil || len(catalog) != 2 || catalog[0].Parsed.Title == "" {
		t.Fatalf("parsed catalog was not persisted: %#v %v", catalog, err)
	}
	page, err := r.ListReleases(ctx, "", "", 1, 0, []string{"filelist"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Total != 2 || page.NextCursor == nil {
		t.Fatalf("bad page %#v", page)
	}
	offset, err := DecodeCursor(*page.NextCursor)
	if err != nil || offset != 1 {
		t.Fatalf("bad cursor %d %v", offset, err)
	}
	d := domain.Download{ID: "source", ReleaseID: stored[0].ID, EngineID: "qb:hash", FilePath: "movie.mkv", AbsolutePath: "/safe/movie.mkv", SizeBytes: 10, CreatedAt: now, UpdatedAt: now}
	if err = r.SaveDownload(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetDownload(ctx, "source")
	if err != nil || got.EngineID != "qb:hash" {
		t.Fatalf("download not durable: %#v %v", got, err)
	}
	got, err = r.FindDownload(ctx, stored[0].ID, 0)
	if err != nil || got.ID != "source" {
		t.Fatalf("download was not found by release and file: %#v %v", got, err)
	}
	p := domain.PlaybackState{ProfileID: "household", SourceID: "source", ReleaseID: stored[0].ID, FileIndex: 0, FilePath: "movie.mkv", PositionMS: 900, DurationMS: 1000, Watched: true, UpdatedAt: now}
	if err = r.SavePlayback(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = r.SetFavorite(ctx, "household", stored[0].ID, true); err != nil {
		t.Fatal(err)
	}
	states, err := r.ListPlayback(ctx, "household")
	if err != nil || len(states) != 1 || !states[0].Watched {
		t.Fatalf("bad playback state %#v %v", states, err)
	}
	prefs := domain.PlaybackPreferences{ProfileID: "household", SourceID: "source", AudioLanguage: "en", AudioTrackIndex: 2, SubtitleLanguage: "ro", SubtitleProvider: "contained", SubtitleCandidateID: "4", SubtitleMode: "selected", UpdatedAt: now}
	if err = r.SavePlaybackPreferences(ctx, prefs); err != nil {
		t.Fatal(err)
	}
	savedPrefs, err := r.GetPlaybackPreferences(ctx, "household", "source")
	if err != nil || savedPrefs.AudioTrackIndex != 2 || savedPrefs.SubtitleCandidateID != "4" || savedPrefs.SubtitleMode != "selected" {
		t.Fatalf("bad playback preferences %#v %v", savedPrefs, err)
	}
	favorites, err := r.ListFavorites(ctx, "household")
	if err != nil || len(favorites) != 1 || favorites[0].ReleaseID != stored[0].ID {
		t.Fatalf("bad favorites %#v %v", favorites, err)
	}
	if err = r.DeleteDownload(ctx, "source"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetDownload(ctx, "source"); err == nil {
		t.Fatal("download should have been deleted")
	}
	if _, err = r.GetPlayback(ctx, "household", "source"); err != nil {
		t.Fatalf("playback history should survive download removal: %v", err)
	}
	job := domain.Job{ID: "metadata:title", Kind: "metadata", State: "queued", Label: "Fetch metadata", DedupeKey: "metadata:title", Progress: 0, UpdatedAt: now}
	if err = r.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.State, job.Progress = "completed", 1
	if err = r.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	jobs, err := r.ListJobs(ctx, 10)
	if err != nil || len(jobs) != 1 || jobs[0].State != "completed" || jobs[0].Progress != 1 {
		t.Fatalf("bad persistent jobs %#v %v", jobs, err)
	}
	manifest := domain.TorrentManifest{ReleaseID: stored[0].ID, Files: []domain.TorrentFile{{Index: 3, Path: "Film.mkv", SizeBytes: 10, Playable: true}}, FetchedAt: now}
	if err = r.SaveTorrentManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	gotManifest, err := r.GetTorrentManifest(ctx, stored[0].ID)
	if err != nil || len(gotManifest.Files) != 1 || gotManifest.Files[0].Index != 3 {
		t.Fatalf("bad torrent manifest %#v %v", gotManifest, err)
	}
	for i := 0; i < 230; i++ {
		id := fmt.Sprintf("job-%03d", i)
		if err = r.SaveJob(ctx, domain.Job{ID: id, Kind: "metadata", State: "completed", Label: "Naruto metadata", DedupeKey: id, UpdatedAt: now.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	jobPage, err := r.QueryJobs(ctx, "Naruto", "completed", "metadata", "", 0, 24, 216)
	if err != nil || jobPage.Total != 230 || len(jobPage.Items) != 14 {
		t.Fatalf("job pagination did not reach old rows: %#v %v", jobPage, err)
	}
	if _, err = r.GetJob(ctx, "job-000"); err != nil {
		t.Fatalf("direct job lookup failed: %v", err)
	}
	retryAt := now.Add(-time.Minute)
	due := domain.Job{ID: "retry-due", Kind: "metadata", State: "failed", Label: "Retry me", DedupeKey: "retry-due", Retryable: true, NextAttemptAt: &retryAt, UpdatedAt: now}
	if err = r.SaveJob(ctx, due); err != nil {
		t.Fatal(err)
	}
	dueJobs, err := r.ListDueJobs(ctx, now, 500)
	if err != nil || len(dueJobs) != 1 || dueJobs[0].ID != due.ID {
		t.Fatalf("due retry query failed: %#v %v", dueJobs, err)
	}
	entry, err := r.AppendJobLog(ctx, domain.JobLog{JobID: due.ID, Attempt: 2, Level: "error", Phase: "metadata", Message: "provider timeout", Context: map[string]any{"provider": "tmdb"}})
	if err != nil || entry.ID == 0 {
		t.Fatalf("append job log failed: %#v %v", entry, err)
	}
	logs, err := r.ListJobLogs(ctx, due.ID, 0, 100)
	if err != nil || len(logs.Items) != 1 || logs.Items[0].Context["provider"] != "tmdb" {
		t.Fatalf("persistent job log failed: %#v %v", logs, err)
	}
}

func TestProviderIDsDoNotCollide(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	input := []domain.TorrentRelease{
		{ID: "filelist:42", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "42", Name: "Film.2024.1080p", Category: "Movies HD", CategoryID: "4", BrowseClass: "video", Seeders: 3},
		{ID: "piratebay:42", TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "42", Name: "Film.2024.1080p", Category: "Video", CategoryID: "201", BrowseClass: "video", Seeders: 7},
	}
	stored, err := r.UpsertReleases(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 || stored[0].ID == stored[1].ID {
		t.Fatalf("collision: %+v", stored)
	}
	for _, want := range stored {
		got, err := r.GetRelease(context.Background(), want.ID)
		if err != nil || got.TrackerID != want.TrackerID || got.ProviderID != "42" {
			t.Fatalf("wrong release: %+v, %v", got, err)
		}
	}
	empty, err := r.ListReleases(context.Background(), "", "", 20, 0, nil)
	if err != nil || empty.Total != 0 {
		t.Fatalf("empty eligibility exposed releases: %+v, %v", empty, err)
	}
}

func TestPreTrackerMigration(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "pre-trackers.db"))
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "migrated.db")
	if err := os.WriteFile(dbPath, data, 0o644); err != nil {
		t.Fatalf("failed to copy fixture: %v", err)
	}

	assertState := func(t *testing.T, r *Repository) {
		t.Helper()
		ctx := context.Background()

		// Original FileList releases survive with original IDs byte-for-byte
		rel1, err := r.GetRelease(ctx, "100001")
		if err != nil {
			t.Fatalf("release 100001 missing: %v", err)
		}
		if rel1.ID != "100001" || rel1.TrackerID != "filelist" || rel1.TrackerName != "FileList" || rel1.ProviderID != "100001" || rel1.CategoryID != "4" || rel1.BrowseClass != "video" || rel1.DiscoveryExcluded {
			t.Fatalf("release 100001 backfill wrong: %+v", rel1)
		}

		rel2, err := r.GetRelease(ctx, "100002")
		if err != nil {
			t.Fatalf("release 100002 missing: %v", err)
		}
		if rel2.ID != "100002" || rel2.TrackerID != "filelist" || rel2.TrackerName != "FileList" || rel2.ProviderID != "100002" || rel2.CategoryID != "21" || rel2.BrowseClass != "video" || rel2.DiscoveryExcluded {
			t.Fatalf("release 100002 backfill wrong: %+v", rel2)
		}

		// Manifest survives
		manifest, err := r.GetTorrentManifest(ctx, "100001")
		if err != nil || len(manifest.Files) != 1 || manifest.Files[0].Index != 0 {
			t.Fatalf("manifest lookup failed: %+v, %v", manifest, err)
		}
		if len(manifest.Metainfo) != 0 {
			t.Fatalf("expected empty metainfo for migrated manifest, got %d bytes", len(manifest.Metainfo))
		}

		// Download survives with backfilled provenance
		dl, err := r.GetDownload(ctx, "fixture-source")
		if err != nil || dl.ReleaseID != "100001" || dl.State != "complete" {
			t.Fatalf("download lookup failed: %+v, %v", dl, err)
		}
		if dl.TrackerID != "filelist" || dl.TrackerName != "FileList" {
			t.Fatalf("download provenance not backfilled: %+v", dl)
		}

		// Playback state survives
		pb, err := r.GetPlayback(ctx, "household", "fixture-source")
		if err != nil || pb.ReleaseID != "100001" || pb.PositionMS != 42000 {
			t.Fatalf("playback lookup failed: %+v, %v", pb, err)
		}

		// Playback preferences survive
		prefs, err := r.GetPlaybackPreferences(ctx, "household", "fixture-source")
		if err != nil || prefs.AudioTrackIndex != 1 || prefs.SubtitleLanguage != "ro" {
			t.Fatalf("playback preferences lookup failed: %+v, %v", prefs, err)
		}

		// Favorite survives (mapped to catalog title ID by backfillCatalog)
		favs, err := r.ListFavorites(ctx, "household")
		parsed := domain.ParseRelease(rel1)
		wantTitleID := domain.CatalogTitleID(rel1, parsed)
		if err != nil || len(favs) != 1 || favs[0].TitleID != wantTitleID {
			t.Fatalf("favorite lookup failed: %+v, want title %s, err: %v", favs, wantTitleID, err)
		}

		// Subtitle asset survives
		sub, err := r.GetSubtitleAsset(ctx, "fixture-source", "fixturesubs", "fixture-candidate-1", "srt")
		if err != nil || sub.Language != "ro" {
			t.Fatalf("subtitle asset lookup failed: %+v, %v", sub, err)
		}

		// Scoped discovery
		emptyPage, err := r.ListReleases(ctx, "", "", 20, 0, nil)
		if err != nil || emptyPage.Total != 0 {
			t.Fatalf("empty eligibility should return 0, got %+v, %v", emptyPage, err)
		}
		scopedPage, err := r.ListReleases(ctx, "", "", 20, 0, []string{"filelist"})
		if err != nil || scopedPage.Total != 2 {
			t.Fatalf("filelist eligibility should return 2, got %+v, %v", scopedPage, err)
		}
	}

	// Pass 1: Open and migrate fixture
	r1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open fixture: %v", err)
	}
	assertState(t, r1)
	if err := r1.Close(); err != nil {
		t.Fatalf("failed to close repository: %v", err)
	}

	// Pass 2: Reopen, asserting idempotency and durable state
	r2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen migrated fixture: %v", err)
	}
	defer r2.Close()
	assertState(t, r2)
}

func TestDisabledTrackerDoesNotAffectDiscovery(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "discovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	input := []domain.TorrentRelease{
		// Low-seeder FileList release for "Film 2024"
		{ID: "filelist:10", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "10", Name: "Film.2024.1080p", Category: "Movies HD", CategoryID: "4", BrowseClass: "video", Seeders: 2, UploadedAt: &now},
		// High-seeder PirateBay release for the SAME title "Film 2024"
		{ID: "piratebay:20", TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "20", Name: "Film.2024.1080p.Remux", Category: "Video", CategoryID: "201", BrowseClass: "video", Seeders: 500, UploadedAt: &now},
		// PirateBay-only title "Pirate Exclusive"
		{ID: "piratebay:30", TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "30", Name: "Pirate.Exclusive.2024.1080p", Category: "Video", CategoryID: "201", BrowseClass: "video", Seeders: 100, UploadedAt: &now},
	}
	if _, err := r.UpsertReleases(ctx, input); err != nil {
		t.Fatal(err)
	}

	// 1. Scoped to FileList only (Pirate Bay disabled)
	filelistOnly := []string{"filelist"}
	page, err := r.QueryCatalogTitleIDs(ctx, domain.CatalogQuery{TrackerIDs: filelistOnly, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("expected exactly 1 title with filelist only, got %d (items: %v)", page.Total, page.Items)
	}
	titleID := page.Items[0]

	// Sources for the title must only include the FileList source (count 1, not 2)
	sources, err := r.ListCatalogSourcesByTitleIDs(ctx, []string{titleID}, filelistOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source for title, got %d: %+v", len(sources), sources)
	}
	if sources[0].Release.TrackerID != "filelist" || sources[0].Release.Seeders != 2 {
		t.Fatalf("wrong source returned: %+v", sources[0])
	}

	// Facets must not contain Pirate Bay category
	facets, err := r.CatalogFacets(ctx, filelistOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range facets.Categories {
		if c == "Video" {
			t.Fatalf("disabled tracker category 'Video' leaked into facets: %v", facets.Categories)
		}
	}

	// Counts reflect only filelist
	total, discoverable, err := r.CatalogCounts(ctx, filelistOnly)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || discoverable != 1 {
		t.Fatalf("expected counts (1, 1), got (%d, %d)", total, discoverable)
	}

	// 2. Scoped to both (both enabled)
	both := []string{"filelist", "piratebay"}
	pageBoth, err := r.QueryCatalogTitleIDs(ctx, domain.CatalogQuery{TrackerIDs: both, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if pageBoth.Total != 2 {
		t.Fatalf("expected 2 titles when both enabled, got %d", pageBoth.Total)
	}
	sourcesBoth, err := r.ListCatalogSourcesByTitleIDs(ctx, []string{titleID}, both)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourcesBoth) != 2 {
		t.Fatalf("expected 2 sources for title when both enabled, got %d", len(sourcesBoth))
	}
}

func TestTorrentManifestMetainfoCapAndPersistence(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "manifests.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	items := []domain.TorrentRelease{
		{ID: "filelist:99", TrackerID: "filelist", TrackerName: "FileList", ProviderID: "99", Name: "Manifest.Test.1080p", Category: "Movies HD", CategoryID: "4", BrowseClass: "video", Seeders: 1, UploadedAt: &now},
	}
	if _, err := r.UpsertReleases(ctx, items); err != nil {
		t.Fatal(err)
	}

	// 1. Valid metainfo persistence
	metaBytes := []byte("d8:announce27:http://tracker.example/announcee")
	manifest := domain.TorrentManifest{
		ReleaseID: "filelist:99",
		Files:     []domain.TorrentFile{{Index: 0, Path: "file.mkv", SizeBytes: 100, Playable: true}},
		Metainfo:  metaBytes,
		FetchedAt: now,
	}
	if err := r.SaveTorrentManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetTorrentManifest(ctx, "filelist:99")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Metainfo) != string(metaBytes) {
		t.Fatalf("metainfo bytes not durable: got %q, want %q", got.Metainfo, metaBytes)
	}

	// 2. Reject metainfo >= 16 MiB
	tooLarge := domain.TorrentManifest{
		ReleaseID: "filelist:99",
		Files:     []domain.TorrentFile{{Index: 0, Path: "file.mkv", SizeBytes: 100, Playable: true}},
		Metainfo:  make([]byte, 16<<20), // 16 MiB
		FetchedAt: now,
	}
	if err := r.SaveTorrentManifest(ctx, tooLarge); err == nil {
		t.Fatal("expected error for metainfo >= 16 MiB, got nil")
	}
}

func TestSaveJobAdoptsRetargetedRowByID(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	original := domain.Job{ID: "metadata:old", Kind: "metadata", State: "queued", Label: "Fetch metadata", DedupeKey: "metadata:old", UpdatedAt: now}
	if err = r.SaveJob(ctx, original); err != nil {
		t.Fatal(err)
	}
	if _, err = r.AppendJobLog(ctx, domain.JobLog{JobID: original.ID, Level: "info", Message: "history marker", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// moveExtinctGroupDependents: id preserved, dedupe key retargeted to the
	// surviving canonical identity.
	if _, err = r.db.ExecContext(ctx, `UPDATE jobs SET dedupe_key='metadata:new' WHERE id='metadata:old'`); err != nil {
		t.Fatal(err)
	}

	// Identity "metadata:old" resurrects under its original dedupe key: the
	// husk row must be adopted in place, keeping its job_logs history.
	resurrected := domain.Job{ID: "metadata:old", Kind: "metadata", State: "queued", Label: "Fetch metadata", DedupeKey: "metadata:old", UpdatedAt: now.Add(time.Minute)}
	if err = r.SaveJob(ctx, resurrected); err != nil {
		t.Fatal(err)
	}
	adopted, err := r.GetJob(ctx, "metadata:old")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.DedupeKey != "metadata:old" || adopted.State != "queued" {
		t.Fatalf("resurrected identity must adopt the husk row: %#v", adopted)
	}
	logs, err := r.ListJobLogs(ctx, "metadata:old", 0, 10)
	if err != nil || len(logs.Items) != 1 || logs.Items[0].Message != "history marker" {
		t.Fatalf("job_logs history must survive adoption: %#v %v", logs, err)
	}
}
