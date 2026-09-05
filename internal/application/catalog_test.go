package application

import (
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

func TestGroupCatalogBuildsSeriesHierarchySummary(t *testing.T) {
	now := time.Now().UTC()
	one := domain.TorrentRelease{ID: "1", Name: "Show.S01E01.1080p.WEB-DL", IMDbID: "tt42", Category: "TV-Series HD", Seeders: 8, SizeBytes: 100, UploadedAt: &now}
	two := domain.TorrentRelease{ID: "2", Name: "Show.S01E02.2160p.WEB-DL", IMDbID: "tt42", Category: "TV-Series 4K", Seeders: 12, SizeBytes: 200, UploadedAt: &now}
	items := []domain.CatalogSource{{Release: one, Parsed: domain.ParseRelease(one)}, {Release: two, Parsed: domain.ParseRelease(two)}}
	titles := groupCatalog(items, false)
	if len(titles) != 1 || titles[0].EpisodeCount != 2 || titles[0].SeasonCount != 1 || titles[0].SourceCount != 2 || titles[0].BestSeeders != 12 || titles[0].LargestSizeBytes != 200 {
		t.Fatalf("unexpected grouped title %#v", titles)
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
	game := domain.TorrentRelease{Name: "Naruto game", Category: "Games PC", Seeders: 20}
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
