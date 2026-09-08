package application

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/sqlite"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

func TestGroupCatalogBuildsSeriesHierarchySummary(t *testing.T) {
	now := time.Now().UTC()
	one := domain.TorrentRelease{ID: "1", Name: "Show.S01E01.1080p.WEB-DL", IMDbID: "tt42", Category: "TV-Series HD", Seeders: 8, SizeBytes: 100, UploadedAt: &now}
	two := domain.TorrentRelease{ID: "2", Name: "Show.S01E02.2160p.WEB-DL", IMDbID: "tt42", Category: "TV-Series 4K", Seeders: 12, SizeBytes: 200, UploadedAt: &now}
	// Sources carry the projected canonical identity; grouping follows it.
	items := []domain.CatalogSource{{Release: one, Parsed: domain.ParseRelease(one), TitleID: "t1"}, {Release: two, Parsed: domain.ParseRelease(two), TitleID: "t1"}}
	titles := groupCatalog(items, false)
	if len(titles) != 1 || titles[0].ID != "t1" || titles[0].EpisodeCount != 2 || titles[0].SeasonCount != 1 || titles[0].SourceCount != 2 || titles[0].BestSeeders != 12 || titles[0].LargestSizeBytes != 200 {
		t.Fatalf("unexpected grouped title %#v", titles)
	}
	split := domain.TorrentRelease{ID: "3", Name: "Show.S01E03.1080p.WEB-DL", IMDbID: "tt42", Category: "TV-Series HD", Seeders: 5, SizeBytes: 150}
	sources := append(items, domain.CatalogSource{Release: split, Parsed: domain.ParseRelease(split), TitleID: "t2"})
	splitTitles := groupCatalog(sources, false)
	if len(splitTitles) != 2 {
		t.Fatalf("sources projecting distinct titles must not merge: %#v", splitTitles)
	}
}

func TestFilterCatalogSources(t *testing.T) {
	release := domain.TorrentRelease{Name: "Film.2024.2160p.HDR.WEB-DL", Category: "Movies 4K", Seeders: 7, Freeleech: true}
	item := domain.CatalogSource{Release: release, Parsed: domain.ParseRelease(release)}
	yes := true
	if got := filterCatalogSources([]domain.CatalogSource{item}, domain.CatalogQuery{Kind: domain.MediaMovie, Resolution: "2160p", MinSeeders: 5, Freeleech: &yes}); len(got) != 1 {
		t.Fatal("matching source was filtered out")
	}
	if got := filterCatalogSources([]domain.CatalogSource{item}, domain.CatalogQuery{MinSeeders: 8}); len(got) != 0 {
		t.Fatal("minimum seeder filter was ignored")
	}
	game := domain.TorrentRelease{Name: "Naruto game", Category: "Games PC", Seeders: 20, DiscoveryExcluded: true}
	if got := filterCatalogSources([]domain.CatalogSource{{Release: game, Parsed: domain.ParseRelease(game)}}, domain.CatalogQuery{Search: "naruto"}); len(got) != 0 {
		t.Fatal("default-blacklisted category leaked into media discovery")
	}
}

func TestSeasonPackEpisodeSourceUsesTorrentFileIndex(t *testing.T) {
	baseRelease := domain.TorrentRelease{ID: "pack", Name: "Show.S02.1080p.WEB-DL", Category: "TV-Series HD", Seeders: 4}
	base := domain.CatalogSource{Release: baseRelease, Parsed: domain.ParseRelease(baseRelease)}
	source, ok := episodeSource(base, domain.TorrentFile{Index: 7, Path: "Show.S02E03.1080p.mkv", SizeBytes: 1234, Playable: true})
	if !ok || source.FileIndex == nil || *source.FileIndex != 7 || source.Parsed.SeasonStart != 2 || source.Parsed.EpisodeStart != 3 || source.FileSizeBytes != 1234 {
		t.Fatalf("season pack file was not expanded correctly: %#v", source)
	}
}

func TestReadBencodedTorrentFiles(t *testing.T) {
	data := []byte("d4:infod5:filesld6:lengthi12e4:pathl15:Show.S01E01.mkveed6:lengthi34e4:pathl15:Show.S01E02.mkveeeee")
	root, _, err := readBNode(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := root.dict["info"].dict["files"].list
	if len(files) != 2 || string(files[1].dict["path"].list[0].value) != "Show.S01E02.mkv" {
		t.Fatalf("unexpected files: %#v", files)
	}
}

func TestCatalogStateAggregatesEpisodeAndSeasonCoverage(t *testing.T) {
	episodeOne := domain.CatalogEpisode{LibraryState: domain.MediaState{DownloadState: "downloaded", WatchState: "watched"}}
	episodeTwo := domain.CatalogEpisode{LibraryState: domain.MediaState{DownloadState: "downloading", WatchState: "inProgress"}}
	partial := aggregateEpisodeState([]domain.CatalogEpisode{episodeOne, episodeTwo})
	if partial.DownloadState != "partial" || partial.WatchState != "partial" {
		t.Fatalf("expected partial season state, got %#v", partial)
	}
	episodeTwo.LibraryState = domain.MediaState{DownloadState: "downloaded", WatchState: "watched"}
	complete := aggregateEpisodeState([]domain.CatalogEpisode{episodeOne, episodeTwo})
	if complete.DownloadState != "downloaded" || complete.WatchState != "watched" {
		t.Fatalf("expected complete season state, got %#v", complete)
	}
}

func TestCatalogEpisodeNeedsOnlyOneDownloadedVersion(t *testing.T) {
	ready := domain.CatalogSource{LibraryState: domain.MediaState{DownloadState: "downloaded", WatchState: "watched"}}
	remote := domain.CatalogSource{LibraryState: domain.MediaState{DownloadState: "none", WatchState: "unwatched"}}
	state := aggregateSourceState([]domain.CatalogSource{remote, ready})
	if state.DownloadState != "downloaded" || state.WatchState != "watched" {
		t.Fatalf("one playable downloaded version should complete the episode: %#v", state)
	}
}

func TestSeasonPackStateUsesOnlyMatchingReleaseFiles(t *testing.T) {
	packAOne := domain.CatalogSource{Release: domain.TorrentRelease{ID: "pack-a"}, FileSizeBytes: 100, LibraryState: domain.MediaState{DownloadState: "downloaded", DownloadID: "a1", Progress: 1, WatchState: "watched"}}
	packATwo := domain.CatalogSource{Release: domain.TorrentRelease{ID: "pack-a"}, FileSizeBytes: 300, LibraryState: domain.MediaState{DownloadState: "downloading", DownloadID: "a2", Progress: .5, WatchState: "inProgress"}}
	packBOne := domain.CatalogSource{Release: domain.TorrentRelease{ID: "pack-b"}, FileSizeBytes: 100, LibraryState: domain.MediaState{DownloadState: "downloaded", DownloadID: "b1", Progress: 1, WatchState: "unwatched"}}
	packBTwo := domain.CatalogSource{Release: domain.TorrentRelease{ID: "pack-b"}, FileSizeBytes: 100, LibraryState: domain.MediaState{DownloadState: "downloaded", DownloadID: "b2", Progress: 1, WatchState: "unwatched"}}
	episodes := []domain.CatalogEpisode{{Sources: []domain.CatalogSource{packAOne, packBOne}}, {Sources: []domain.CatalogSource{packATwo, packBTwo}}}
	a := packSourceState("pack-a", episodes)
	if a.DownloadState != "downloading" || a.Progress != .625 || a.WatchState != "partial" {
		t.Fatalf("unexpected pack-a state: %#v", a)
	}
	b := packSourceState("pack-b", episodes)
	if b.DownloadState != "downloaded" || b.Progress != 1 {
		t.Fatalf("unexpected pack-b state: %#v", b)
	}
	if other := packSourceState("other-season-pack", episodes); other.DownloadState != "none" {
		t.Fatalf("unrelated release leaked into pack state: %#v", other)
	}
}

func TestSourceStateReportsPausedTransferWithoutLosingDownloadState(t *testing.T) {
	fileIndex := 2
	index := catalogStateIndex{downloadsByRelease: map[string][]domain.Download{
		"pack": {{ID: "download", ReleaseID: "pack", FileIndex: fileIndex, State: "pausedDL", Progress: .42}},
	}, playbackBySource: map[string]domain.PlaybackState{}, playbackByRelease: map[string][]domain.PlaybackState{}}
	state := index.sourceState(domain.CatalogSource{Release: domain.TorrentRelease{ID: "pack"}, FileIndex: &fileIndex})
	if state.DownloadState != "downloading" || state.TransferState != "paused" || state.DownloadID != "download" {
		t.Fatalf("unexpected paused media state: %#v", state)
	}
}

type bayCatalog struct{ Tracker }

func (bayCatalog) ID() string   { return "piratebay" }
func (bayCatalog) Name() string { return "The Pirate Bay" }

func dualTrackerRegistry(t *testing.T) *TrackerRegistry {
	t.Helper()
	reg, err := NewTrackerRegistry([]TrackerRegistration{
		{Adapter: openCatalog{}, Enabled: func() bool { return true }, Configured: func() bool { return true }},
		{Adapter: bayCatalog{}, Enabled: func() bool { return true }, Configured: func() bool { return true }},
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func projectionHarness(t *testing.T) (*Service, *sqlite.Repository) {
	t.Helper()
	repo, settings := retryHarness(t)
	service := NewService(dualTrackerRegistry(t), singleEngineSet(t, "qb:", &streamingEngine{}), repo, settings)
	return service, repo
}

func projectedTitleOf(t *testing.T, repo *sqlite.Repository, releaseID string) string {
	t.Helper()
	ids, err := repo.CatalogTitleIDsForReleases(context.Background(), []string{releaseID})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := ids[releaseID]
	if !ok {
		t.Fatalf("release %q has no catalog projection", releaseID)
	}
	return id
}

// siloFamilyFixture mirrors the live Silo shape in miniature: one missing-IMDb
// episode that starts in a fallback group and two tt14688458 episodes from
// different trackers whose arrival merges the family into the IMDb anchor.
func siloFamilyFixture() (domain.TorrentRelease, []domain.TorrentRelease) {
	fallback := domain.TorrentRelease{
		TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "pb-e-1",
		Name: "Silo S03E01 1080p WEBRip x264-GROUP", Category: "TV-Series HD",
		Seeders: 3, Leechers: 1, SizeBytes: 1 << 30,
	}
	anchors := []domain.TorrentRelease{
		{
			TrackerID: "filelist", TrackerName: "FileList", ProviderID: "fl-1",
			Name: "Silo S01E01 1080p WEB-DL DD5.1 H.264-FLUX", Category: "TV-Series HD",
			IMDbID: "tt14688458", Seeders: 3, Leechers: 1, SizeBytes: 1 << 30,
		},
		{
			TrackerID: "piratebay", TrackerName: "The Pirate Bay", ProviderID: "pb-1",
			Name: "Silo.S02E01.720p.WEBRip.x264-GROUP", Category: "TV-Series HD",
			IMDbID: "tt14688458", Seeders: 3, Leechers: 1, SizeBytes: 1 << 30,
		},
	}
	return fallback, anchors
}

func TestMergedFamilyConsumersUseProjectedIdentity(t *testing.T) {
	service, repo := projectionHarness(t)
	ctx := context.Background()
	fallback, anchors := siloFamilyFixture()
	storedFallback, err := repo.UpsertReleases(ctx, []domain.TorrentRelease{fallback})
	if err != nil {
		t.Fatal(err)
	}
	deadGroup := projectedTitleOf(t, repo, storedFallback[0].ID)
	storedAnchors, err := repo.UpsertReleases(ctx, anchors)
	if err != nil {
		t.Fatal(err)
	}
	anchor := projectedTitleOf(t, repo, storedAnchors[0].ID)
	if anchor == deadGroup {
		t.Fatal("fixture expected the missing-imdb release to merge into the imdb anchor")
	}
	for _, rel := range storedAnchors {
		if projectedTitleOf(t, repo, rel.ID) != anchor {
			t.Fatalf("anchor release %q did not join the anchor group", rel.ID)
		}
	}
	if projectedTitleOf(t, repo, storedFallback[0].ID) != anchor {
		t.Fatalf("merged release %q still projects the dead group %q", storedFallback[0].ID, deadGroup)
	}

	// Catalog detail through the anchor id exposes every family source across trackers.
	detail, err := service.CatalogDetail(ctx, anchor)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title.ID != anchor || len(detail.Title.Sources) != 3 {
		t.Fatalf("detail for the anchor must carry all three family sources: id=%q sources=%d", detail.Title.ID, len(detail.Title.Sources))
	}
	trackers := map[string]bool{}
	for _, source := range detail.Title.Sources {
		trackers[source.Release.TrackerID] = true
	}
	if !trackers["filelist"] || !trackers["piratebay"] {
		t.Fatalf("detail must mix both trackers, got %v", trackers)
	}

	// Download listing reports the projected title id, not a recomputed one.
	now := time.Now().UTC()
	download := domain.Download{ID: "silo-source", ReleaseID: storedFallback[0].ID, EngineID: "qb:hash", FileIndex: 2, FilePath: "Silo.S03E01.mkv", AbsolutePath: "/downloads/Silo.S03E01.mkv", State: "complete", CreatedAt: now, UpdatedAt: now}
	if err := repo.SaveDownload(ctx, download); err != nil {
		t.Fatal(err)
	}
	items, err := service.Downloads(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("download listing failed: %d items %v", len(items), err)
	}
	if items[0].TitleID != anchor {
		t.Fatalf("download reports title %q, want projected anchor %q", items[0].TitleID, anchor)
	}

	// Household state for a family release reports the projected anchor id.
	if _, err := service.UpdatePlayback(ctx, download.ID, 100, 1000); err != nil {
		t.Fatal(err)
	}
	state, err := service.HouseholdState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Recent) != 1 || state.Recent[0].TitleID != anchor {
		t.Fatalf("household recent reports %#v, want the projected anchor %q", state.Recent, anchor)
	}
	if state.Recent[0].Catalog == nil || state.Recent[0].Catalog.ID != anchor {
		t.Fatalf("household catalog card must be the projected anchor: %#v", state.Recent[0].Catalog)
	}

	// Favoriting a merged release through the public path lands on the anchor.
	if err := service.SetFavorite(ctx, storedFallback[0].ID, true); err != nil {
		t.Fatal(err)
	}
	favs, err := repo.ListFavorites(ctx, householdProfile)
	if err != nil || len(favs) != 1 {
		t.Fatalf("favorite lookup failed: %d rows %v", len(favs), err)
	}
	if favs[0].TitleID != anchor {
		t.Fatalf("favorite landed on %q, want projected anchor %q", favs[0].TitleID, anchor)
	}
}

func TestSetFavoriteResolvesCurrentProjection(t *testing.T) {
	service, repo := projectionHarness(t)
	ctx := context.Background()
	fallback, anchors := siloFamilyFixture()
	storedFallback, err := repo.UpsertReleases(ctx, []domain.TorrentRelease{fallback})
	if err != nil {
		t.Fatal(err)
	}
	deadGroup := projectedTitleOf(t, repo, storedFallback[0].ID)
	storedAnchors, err := repo.UpsertReleases(ctx, anchors)
	if err != nil {
		t.Fatal(err)
	}
	anchor := projectedTitleOf(t, repo, storedAnchors[0].ID)
	if anchor == deadGroup {
		t.Fatal("fixture expected the family to merge")
	}
	// The caller passes the release id; SetFavorite must resolve the CURRENT
	// projection at write time so the favorite never lands on a dead group.
	if err := service.SetFavorite(ctx, storedFallback[0].ID, true); err != nil {
		t.Fatal(err)
	}
	favs, err := repo.ListFavorites(ctx, householdProfile)
	if err != nil || len(favs) != 1 {
		t.Fatalf("favorite lookup failed: %d rows %v", len(favs), err)
	}
	if favs[0].TitleID != anchor {
		t.Fatalf("favorite landed on %q, want the projected anchor %q (dead group %q)", favs[0].TitleID, anchor, deadGroup)
	}
	if err := service.SetFavorite(ctx, storedFallback[0].ID, false); err != nil {
		t.Fatal(err)
	}
	favs, err = repo.ListFavorites(ctx, householdProfile)
	if err != nil || len(favs) != 0 {
		t.Fatalf("unfavorite must clear the row: %d rows %v", len(favs), err)
	}
}

func TestSearchDedupeKeepsDistinctReleases(t *testing.T) {
	service, _ := projectionHarness(t)
	ctx := context.Background()
	items := []domain.TorrentRelease{
		{TrackerID: "filelist", TrackerName: "FileList", ProviderID: "fl-series", Name: "Foundation.S01E01.1080p.WEB-DL", Category: "TV-Series HD", Seeders: 3, SizeBytes: 1 << 30},
		{TrackerID: "filelist", TrackerName: "FileList", ProviderID: "fl-movie", Name: "Foundation.2021.1080p.WEB-DL", Category: "Movies HD", Seeders: 3, SizeBytes: 1 << 30},
		{TrackerID: "filelist", TrackerName: "FileList", ProviderID: "fl-dune-1984", Name: "Dune.1984.1080p.WEB-DL", Category: "Movies HD", Seeders: 3, SizeBytes: 1 << 30},
		{TrackerID: "filelist", TrackerName: "FileList", ProviderID: "fl-dune-2021", Name: "Dune.2021.2160p.WEB-DL", Category: "Movies HD", Seeders: 3, SizeBytes: 1 << 30},
	}
	if _, err := service.upsertTrackerReleases(ctx, "filelist", "FileList", items); err != nil {
		t.Fatal(err)
	}
	page, err := service.CatalogTitles(ctx, domain.CatalogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || len(page.Items) != 4 {
		t.Fatalf("same-name releases differing in kind or year must survive ingestion as separate groups: total=%d items=%d", page.Total, len(page.Items))
	}
	seen := map[string]bool{}
	for _, title := range page.Items {
		key := fmt.Sprintf("%s:%s:%d", title.Kind, title.Title, title.Year)
		if seen[key] {
			t.Fatalf("distinct releases collapsed into one group: %s", key)
		}
		seen[key] = true
	}
	for _, want := range []string{"series:Foundation:0", "movie:Foundation:2021", "movie:Dune:1984", "movie:Dune:2021"} {
		if !seen[want] {
			t.Fatalf("missing expected group %s, got %v", want, seen)
		}
	}
}
