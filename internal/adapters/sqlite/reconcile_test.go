package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

const (
	siloIMDb       = "tt14688458"
	remakeIMDb     = "tt0087182"
	altYearIMDb    = "tt1160419"
	secondSiloIMDb = "tt3581920"
)

func openReconcileRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func catalogRelease(trackerID, trackerName, providerID, category, name, imdb string) domain.TorrentRelease {
	return domain.TorrentRelease{
		TrackerID: trackerID, TrackerName: trackerName, ProviderID: providerID,
		Name: name, Category: category, IMDbID: imdb, Seeders: 3, Leechers: 1, SizeBytes: 1 << 30,
	}
}

func filelistMovie(providerID, name, imdb string) domain.TorrentRelease {
	return catalogRelease("filelist", "FileList", providerID, "Movies HD", name, imdb)
}

func filelistRelease(providerID, name, imdb string) domain.TorrentRelease {
	return catalogRelease("filelist", "FileList", providerID, "TV-Series HD", name, imdb)
}

func piratebayRelease(providerID, name, imdb string) domain.TorrentRelease {
	return catalogRelease("piratebay", "The Pirate Bay", providerID, "TV-Series HD", name, imdb)
}

func piratebayMovie(providerID, name, imdb string) domain.TorrentRelease {
	return catalogRelease("piratebay", "The Pirate Bay", providerID, "Movies HD", name, imdb)
}

// siloFixture mirrors the live Silo shape: 121 tt14688458 releases (55 filelist
// + 66 piratebay), 32 year-less missing-IMDb piratebay episodes and 2
// missing-IMDb 2023 piratebay packs, all parsing into the "silo" series family.
func siloFixture() []domain.TorrentRelease {
	items := make([]domain.TorrentRelease, 0, 155)
	for i := 1; i <= 55; i++ {
		items = append(items, filelistRelease(fmt.Sprintf("fl-%d", i), fmt.Sprintf("Silo S01E%02d 1080p WEB-DL DD5.1 H.264-FLUX", i), siloIMDb))
	}
	for i := 1; i <= 66; i++ {
		items = append(items, piratebayRelease(fmt.Sprintf("pb-%d", i), fmt.Sprintf("Silo.S02E%02d.720p.WEBRip.x264-GROUP", i), siloIMDb))
	}
	for i := 1; i <= 32; i++ {
		items = append(items, piratebayRelease(fmt.Sprintf("pb-e-%d", i), fmt.Sprintf("Silo S03E%02d 1080p WEBRip x264-GROUP", i), ""))
	}
	items = append(
		items,
		piratebayRelease("pb-pack-1", "Silo S01 Complete 2023 1080p WEB-DL DD5.1 H.264-GROUP", ""),
		piratebayRelease("pb-pack-2", "Silo S02 Complete 2023 720p WEBRip x264-GROUP", ""),
	)
	return items
}

func titleOf(t *testing.T, ctx context.Context, r *Repository, releaseID string) string {
	t.Helper()
	ids, err := r.CatalogTitleIDsForReleases(ctx, []string{releaseID})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := ids[releaseID]
	if !ok {
		t.Fatalf("release %q has no catalog projection", releaseID)
	}
	return id
}

func catalogCardIDs(t *testing.T, ctx context.Context, r *Repository) domain.Page[string] {
	t.Helper()
	page, err := r.QueryCatalogTitleIDs(ctx, domain.CatalogQuery{Limit: 20, TrackerIDs: []string{"filelist", "piratebay"}})
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestSiloFixtureConsolidatesToOneCard(t *testing.T) {
	r := openReconcileRepo(t)
	ctx := context.Background()
	stored, err := r.UpsertReleases(ctx, siloFixture())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 155 {
		t.Fatalf("expected 155 stored releases, got %d", len(stored))
	}
	page := catalogCardIDs(t, ctx, r)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("silo family did not consolidate to one card: total=%d items=%v", page.Total, page.Items)
	}
	card := page.Items[0]
	sources, err := r.ListCatalogSourcesByTitleIDs(ctx, page.Items, []string{"filelist", "piratebay"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 155 {
		t.Fatalf("expected 155 sources on the single card, got %d", len(sources))
	}
	trackers := map[string]bool{}
	for _, s := range sources {
		if s.TitleID != card {
			t.Fatalf("source %q projects title %q, want card %q", s.Release.ID, s.TitleID, card)
		}
		trackers[s.Release.TrackerID] = true
	}
	if !trackers["filelist"] || !trackers["piratebay"] {
		t.Fatalf("expected both tracker labels on the card, got %v", trackers)
	}
}

func TestUpsertArrivalOrderInversion(t *testing.T) {
	anchored := []domain.TorrentRelease{
		filelistRelease("fl-a", "Silo S01E01 1080p WEB-DL DD5.1 H.264-FLUX", siloIMDb),
		filelistRelease("fl-b", "Silo S01 1080p WEB-DL DD5.1 H.264-FLUX", siloIMDb),
	}
	missing := []domain.TorrentRelease{
		piratebayRelease("pb-a", "Silo S01E05 720p WEBRip x264-GROUP", ""),
		piratebayRelease("pb-b", "Silo S02 Complete 2023 720p WEBRip x264-GROUP", ""),
	}
	ids := [2]string{}
	for i, batches := range [2][][]domain.TorrentRelease{
		{missing, anchored},
		{anchored, missing},
	} {
		r := openReconcileRepo(t)
		ctx := context.Background()
		for _, batch := range batches {
			if _, err := r.UpsertReleases(ctx, batch); err != nil {
				t.Fatal(err)
			}
		}
		page := catalogCardIDs(t, ctx, r)
		if page.Total != 1 || len(page.Items) != 1 {
			t.Fatalf("order %d did not consolidate to one card: total=%d items=%v", i, page.Total, page.Items)
		}
		ids[i] = page.Items[0]
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("reversed arrival order changed the canonical id: %q vs %q", ids[0], ids[1])
	}
}

func TestReopenPreservesConsolidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	items := []domain.TorrentRelease{
		filelistRelease("fl-1", "Silo S01E01 1080p WEB-DL DD5.1 H.264-FLUX", siloIMDb),
		piratebayRelease("pb-1", "Silo S01E05 720p WEBRip x264-GROUP", ""),
	}
	if _, err := r.UpsertReleases(ctx, items); err != nil {
		t.Fatal(err)
	}
	page := catalogCardIDs(t, ctx, r)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("expected one consolidated card before reopen, got total=%d", page.Total)
	}
	before := page.Items[0]
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := catalogCardIDs(t, ctx, reopened)
	if after.Total != 1 || len(after.Items) != 1 {
		t.Fatalf("reopen split the consolidated card: total=%d", after.Total)
	}
	if after.Items[0] != before {
		t.Fatalf("reopen changed the canonical id: %q vs %q", after.Items[0], before)
	}
}

func TestMovieRemakesResolveCompatibleCandidates(t *testing.T) {
	r := openReconcileRepo(t)
	ctx := context.Background()
	movies := []domain.TorrentRelease{
		filelistMovie("fl-84", "Nineteen Eighty-Four 1984 1080p BluRay x264-GRP", remakeIMDb),
		filelistMovie("fl-56", "Nineteen Eighty-Four 1956 1080p BluRay x264-GRP", altYearIMDb),
		piratebayMovie("pb-84", "Nineteen Eighty-Four 1984 720p WEBRip x264-GROUP", ""),
		piratebayMovie("pb-unknown", "Nineteen Eighty-Four 1080p WEBRip x264-GROUP", ""),
	}
	stored, err := r.UpsertReleases(ctx, movies)
	if err != nil {
		t.Fatal(err)
	}
	anchor1984 := titleOf(t, ctx, r, stored[0].ID)
	anchor1956 := titleOf(t, ctx, r, stored[1].ID)
	if anchor1984 == anchor1956 {
		t.Fatalf("distinct imdb anchors must never merge: %q", anchor1984)
	}
	if joined := titleOf(t, ctx, r, stored[2].ID); joined != anchor1984 {
		t.Fatalf("1984 missing-imdb release joined %q, want the 1984 anchor %q", joined, anchor1984)
	}
	fallback := titleOf(t, ctx, r, stored[3].ID)
	if fallback == anchor1984 || fallback == anchor1956 {
		t.Fatalf("year-0 missing-imdb release must stay separate, landed on %q", fallback)
	}
	page := catalogCardIDs(t, ctx, r)
	if page.Total != 3 {
		t.Fatalf("expected 3 cards (two anchors + fallback), got total=%d items=%v", page.Total, page.Items)
	}
}

func TestLateAmbiguityRestoresFallbackWithoutRedirects(t *testing.T) {
	r := openReconcileRepo(t)
	ctx := context.Background()
	first, err := r.UpsertReleases(ctx, []domain.TorrentRelease{
		filelistRelease("fl-1", "Silo S01E01 1080p WEB-DL DD5.1 H.264-FLUX", siloIMDb),
		piratebayRelease("pb-1", "Silo S01E05 720p WEBRip x264-GROUP", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	anchor := titleOf(t, ctx, r, first[0].ID)
	if joined := titleOf(t, ctx, r, first[1].ID); joined != anchor {
		t.Fatalf("missing-imdb release should join the lone series anchor: %q vs %q", joined, anchor)
	}
	if err := r.SetFavorite(ctx, "household", anchor, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := r.SaveCatalogMetadata(ctx, domain.CatalogMetadata{TitleID: anchor, Provider: "tmdb", Title: "Silo", FetchedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	second, err := r.UpsertReleases(ctx, []domain.TorrentRelease{
		filelistRelease("fl-2", "Silo S02 1080p WEB-DL DD5.1 H.264-FLUX", secondSiloIMDb),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondAnchor := titleOf(t, ctx, r, second[0].ID)
	if secondAnchor == anchor {
		t.Fatalf("second series anchor must hold a distinct identity")
	}
	fallback := titleOf(t, ctx, r, first[1].ID)
	if fallback == anchor || fallback == secondAnchor {
		t.Fatalf("missing-imdb release did not restore the fallback: %q (anchor=%q second=%q)", fallback, anchor, secondAnchor)
	}
	if titleOf(t, ctx, r, first[0].ID) != anchor {
		t.Fatalf("anchor identity moved after the second anchor arrived")
	}
	favorites, err := r.ListFavorites(ctx, "household")
	if err != nil || len(favorites) != 1 {
		t.Fatalf("bad favorites after ambiguity: %#v %v", favorites, err)
	}
	if favorites[0].TitleID != anchor {
		t.Fatalf("favorite was redirected to %q, want anchor %q", favorites[0].TitleID, anchor)
	}
	meta, err := r.GetCatalogMetadata(ctx, anchor)
	if err != nil || meta.TitleID != anchor {
		t.Fatalf("metadata was redirected after ambiguity: %#v %v", meta, err)
	}
}

func TestOmittedIMDbIsPinnedConflictIsReconciled(t *testing.T) {
	r := openReconcileRepo(t)
	ctx := context.Background()
	base := filelistRelease("prov-1", "Silo S01E01 1080p WEB-DL DD5.1 H.264-FLUX", siloIMDb)
	first, err := r.UpsertReleases(ctx, []domain.TorrentRelease{base})
	if err != nil {
		t.Fatal(err)
	}
	anchored := titleOf(t, ctx, r, first[0].ID)

	omitted := base
	omitted.IMDbID = ""
	second, err := r.UpsertReleases(ctx, []domain.TorrentRelease{omitted})
	if err != nil {
		t.Fatal(err)
	}
	if kept := titleOf(t, ctx, r, second[0].ID); kept != anchored {
		t.Fatalf("omitted imdb must keep the stored identity: %q vs %q", kept, anchored)
	}
	pinned, err := r.GetRelease(ctx, second[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.IMDbID != siloIMDb {
		t.Fatalf("omitted imdb must be pinned to the stored provider fact, got %q", pinned.IMDbID)
	}

	conflict := base
	conflict.IMDbID = remakeIMDb
	third, err := r.UpsertReleases(ctx, []domain.TorrentRelease{conflict})
	if err != nil {
		t.Fatal(err)
	}
	if moved := titleOf(t, ctx, r, third[0].ID); moved == anchored {
		t.Fatalf("conflicting nonempty imdb must move the release off %q", anchored)
	}
	updated, err := r.GetRelease(ctx, third[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.IMDbID != remakeIMDb {
		t.Fatalf("conflicting imdb must overwrite the stored fact, got %q", updated.IMDbID)
	}
}

func TestMergeMovesFavoritesMetadataAndQueuedJobs(t *testing.T) {
	r := openReconcileRepo(t)
	ctx := context.Background()
	seed, err := r.UpsertReleases(ctx, []domain.TorrentRelease{
		piratebayRelease("pb-1", "Silo S01E05 720p WEBRip x264-GROUP", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	fallback := titleOf(t, ctx, r, seed[0].ID)
	if err := r.SetFavorite(ctx, "household", fallback, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := r.SaveCatalogMetadata(ctx, domain.CatalogMetadata{TitleID: fallback, Provider: "tmdb", Title: "Silo", FetchedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	jobKey := "catalog-title-refresh:" + fallback
	if err := r.SaveJob(ctx, domain.Job{ID: jobKey, Kind: "catalog-title-refresh", State: "queued", Label: "Refresh all versions of Silo", DedupeKey: jobKey, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordSync(ctx, "catalog-title-refresh:filelist:"+fallback, 5, nil); err != nil {
		t.Fatal(err)
	}

	bulk := []domain.TorrentRelease{
		filelistRelease("fl-1", "Silo S01E01 1080p WEB-DL DD5.1 H.264-FLUX", siloIMDb),
		filelistRelease("fl-2", "Silo S01E02 1080p WEB-DL DD5.1 H.264-FLUX", siloIMDb),
		filelistRelease("fl-3", "Silo S01 1080p WEB-DL DD5.1 H.264-FLUX", siloIMDb),
	}
	if _, err := r.UpsertReleases(ctx, bulk); err != nil {
		t.Fatal(err)
	}

	anchor := titleOf(t, ctx, r, seed[0].ID)
	if anchor == fallback {
		t.Fatalf("imdb bulk arrival must merge the fallback group into the anchor")
	}
	page := catalogCardIDs(t, ctx, r)
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("merged family must be one card, got total=%d", page.Total)
	}

	favorites, err := r.ListFavorites(ctx, "household")
	if err != nil || len(favorites) != 1 {
		t.Fatalf("bad favorites after merge: %#v %v", favorites, err)
	}
	if favorites[0].TitleID != anchor {
		t.Fatalf("favorite stayed on extinct group %q, want anchor %q", favorites[0].TitleID, anchor)
	}
	if _, err := r.GetCatalogMetadata(ctx, fallback); err == nil {
		t.Fatalf("metadata row for the extinct group must not survive the clean merge")
	}
	meta, err := r.GetCatalogMetadata(ctx, anchor)
	if err != nil || meta.TitleID != anchor {
		t.Fatalf("metadata must transfer to the anchor on a clean merge: %#v %v", meta, err)
	}
	job, err := r.GetJob(ctx, jobKey)
	if err != nil {
		t.Fatalf("queued job lost after merge: %v", err)
	}
	if job.ID != jobKey {
		t.Fatalf("job id must be preserved across retarget: %q vs %q", job.ID, jobKey)
	}
	if job.DedupeKey != "catalog-title-refresh:"+anchor {
		t.Fatalf("job dedupe key must be retargeted to the anchor, got %q", job.DedupeKey)
	}
	if job.State != "queued" {
		t.Fatalf("queued job must stay queued after retarget, got %q", job.State)
	}
	mergedAge, err := r.SyncAge(ctx, "catalog-title-refresh:filelist:"+anchor)
	if err != nil || mergedAge < 0 {
		t.Fatalf("sync state must follow the merge: age=%d err=%v", mergedAge, err)
	}
	if extinctAge, _ := r.SyncAge(ctx, "catalog-title-refresh:filelist:"+fallback); extinctAge >= 0 {
		t.Fatalf("sync state for the extinct group must be gone, age=%d", extinctAge)
	}
}

func TestSplitExtinctKeepsDependentsOnOldID(t *testing.T) {
	r := openReconcileRepo(t)
	ctx := context.Background()
	seed, err := r.UpsertReleases(ctx, []domain.TorrentRelease{
		piratebayRelease("pb-a", "Silo S01E01 720p WEBRip x264-GROUP", ""),
		piratebayRelease("pb-b", "Silo S01E02 720p WEBRip x264-GROUP", ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	old := titleOf(t, ctx, r, seed[0].ID)
	if titleOf(t, ctx, r, seed[1].ID) != old {
		t.Fatalf("expected both missing-imdb rows to share one fallback group")
	}
	if err := r.SetFavorite(ctx, "household", old, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := r.SaveCatalogMetadata(ctx, domain.CatalogMetadata{TitleID: old, Provider: "tmdb", Title: "Silo", FetchedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// One batch hands the two rows DISTINCT imdb identities: the shared
	// fallback group goes extinct while its members split to two anchors,
	// so the dependents gate must keep them on the old id.
	if _, err := r.UpsertReleases(ctx, []domain.TorrentRelease{
		piratebayRelease("pb-a", "Silo S01E01 720p WEBRip x264-GROUP", siloIMDb),
		piratebayRelease("pb-b", "Silo S01E02 720p WEBRip x264-GROUP", secondSiloIMDb),
	}); err != nil {
		t.Fatal(err)
	}
	anchorA := titleOf(t, ctx, r, seed[0].ID)
	anchorB := titleOf(t, ctx, r, seed[1].ID)
	if anchorA == old || anchorB == old || anchorA == anchorB {
		t.Fatalf("expected a two-way split away from the extinct group: a=%q b=%q old=%q", anchorA, anchorB, old)
	}
	page := catalogCardIDs(t, ctx, r)
	if page.Total != 2 {
		t.Fatalf("expected two anchor cards after the split, got total=%d", page.Total)
	}
	// Dependents stay on the split-extinct id: preserved, not moved, and not
	// copied onto either anchor.
	favorites, err := r.ListFavorites(ctx, "household")
	if err != nil || len(favorites) != 1 || favorites[0].TitleID != old {
		t.Fatalf("favorites must stay on the split-extinct id: %#v %v", favorites, err)
	}
	meta, err := r.GetCatalogMetadata(ctx, old)
	if err != nil || meta.TitleID != old {
		t.Fatalf("metadata must stay on the split-extinct id: %#v %v", meta, err)
	}
	for _, anchor := range []string{anchorA, anchorB} {
		if _, err := r.GetCatalogMetadata(ctx, anchor); err == nil {
			t.Fatalf("metadata was cross-written to anchor %q", anchor)
		}
	}
}
