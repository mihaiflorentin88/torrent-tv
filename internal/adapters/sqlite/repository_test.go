package sqlite

import (
	"context"
	"fmt"
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
	items := []domain.TorrentRelease{{ID: "1", Name: "Old", Category: "Movies HD", UploadedAt: &now}, {ID: "2", Name: "New", Category: "Movies 4K", UploadedAt: &now}}
	if err = r.UpsertReleases(ctx, items); err != nil {
		t.Fatal(err)
	}
	catalog, err := r.ListCatalogSources(ctx)
	if err != nil || len(catalog) != 2 || catalog[0].Parsed.Title == "" {
		t.Fatalf("parsed catalog was not persisted: %#v %v", catalog, err)
	}
	page, err := r.ListReleases(ctx, "", "", 1, 0)
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
	d := domain.Download{ID: "source", ReleaseID: "1", EngineID: "qb:hash", FilePath: "movie.mkv", AbsolutePath: "/safe/movie.mkv", SizeBytes: 10, CreatedAt: now, UpdatedAt: now}
	if err = r.SaveDownload(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetDownload(ctx, "source")
	if err != nil || got.EngineID != "qb:hash" {
		t.Fatalf("download not durable: %#v %v", got, err)
	}
	got, err = r.FindDownload(ctx, "1", 0)
	if err != nil || got.ID != "source" {
		t.Fatalf("download was not found by release and file: %#v %v", got, err)
	}
	p := domain.PlaybackState{ProfileID: "household", SourceID: "source", ReleaseID: "1", FileIndex: 0, FilePath: "movie.mkv", PositionMS: 900, DurationMS: 1000, Watched: true, UpdatedAt: now}
	if err = r.SavePlayback(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = r.SetFavorite(ctx, "household", "1", true); err != nil {
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
	if err != nil || len(favorites) != 1 || favorites[0].ReleaseID != "1" {
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
	manifest := domain.TorrentManifest{ReleaseID: "1", Files: []domain.TorrentFile{{Index: 3, Path: "Film.mkv", SizeBytes: 10, Playable: true}}, FetchedAt: now}
	if err = r.SaveTorrentManifest(ctx, manifest); err != nil {
		t.Fatal(err)
	}
	gotManifest, err := r.GetTorrentManifest(ctx, "1")
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
