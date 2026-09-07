package domain

import (
	"maps"
	"testing"
)

func TestParseReleaseFixtures(t *testing.T) {
	tests := []struct {
		name, title string
		kind        MediaKind
		season      int
		episode     int
		resolution  string
		year        int
	}{
		{"One.Piece.S23E16.1080p.WEB-DL.AAC2.0.x264-SubsPlease", "One Piece", MediaSeries, 23, 16, "1080p", 0},
		{"Bleach.S17E42.SON.OF.DARKNESS.2160p.WEB-DL.DDP5.1.H.265", "Bleach", MediaSeries, 17, 42, "2160p", 0},
		{"Redakai.S02.1080p.WEB-DL.AAC2.0.H.264", "Redakai", MediaSeries, 2, 0, "1080p", 0},
		{"The.New.Fred.and.Barney.Show.S01-S02.1080p.WEB-DL", "The New Fred and Barney Show", MediaSeries, 1, 0, "1080p", 0},
		{"Severance.2022.1080p.BluRay.x265", "Severance", MediaMovie, 0, 0, "1080p", 2022},
		{"Shogun.1x02.Servants.of.Two.Masters.720p.HDTV", "Shogun", MediaSeries, 1, 2, "720p", 0},
		{"[Shinobi] Naruto Shippuden - Sezonul 01 [480p]", "Naruto Shippuden", MediaSeries, 1, 0, "480p", 0},
		{"Naruto Shippuden - Season 12 [SD]", "Naruto Shippuden", MediaSeries, 12, 0, "", 0},
	}
	for _, tt := range tests {
		p := ParseRelease(TorrentRelease{Name: tt.name})
		if p.Title != tt.title || p.Kind != tt.kind || p.SeasonStart != tt.season || p.EpisodeStart != tt.episode || p.Resolution != tt.resolution || p.Year != tt.year {
			t.Errorf("ParseRelease(%q) = %#v", tt.name, p)
		}
	}
}

func TestCatalogTitleIDPrefersIMDb(t *testing.T) {
	a := TorrentRelease{Name: "Film.2020.1080p", IMDbID: "tt123"}
	b := TorrentRelease{Name: "Completely.Different.2160p", IMDbID: "tt123"}
	if CatalogTitleID(a, ParseRelease(a)) != CatalogTitleID(b, ParseRelease(b)) {
		t.Fatal("same IMDb media should have the same canonical id")
	}
}

func TestResolveFamilyTitleIDsUsesRealHash(t *testing.T) {
	ev := []ReleaseEvidence{{ReleaseID: "r1", Title: "Dune", SortTitle: "dune", IMDbID: "tt14688458", Year: 2021}}
	got := ResolveFamilyTitleIDs(MediaMovie, "dune", ev)
	want := CatalogTitleID(TorrentRelease{IMDbID: "tt14688458"}, ParsedRelease{Kind: MediaMovie, Title: "Dune", SortTitle: "dune"})
	if got["r1"] != want {
		t.Fatalf("got %q, want %q", got["r1"], want)
	}
}

func TestResolveFamilyTitleIDsMovieRemake(t *testing.T) {
	evidence := []ReleaseEvidence{
		{ReleaseID: "a84", Title: "Dune", SortTitle: "dune", IMDbID: "tt0087182", Year: 1984},
		{ReleaseID: "a21", Title: "Dune", SortTitle: "dune", IMDbID: "tt1160419", Year: 2021},
		{ReleaseID: "m84", Title: "Dune", SortTitle: "dune", Year: 1984},
		{ReleaseID: "m0", Title: "Dune Part Two", SortTitle: "dune", Year: 0},
	}
	got := ResolveFamilyTitleIDs(MediaMovie, "dune", evidence)
	anchor84 := CatalogTitleID(TorrentRelease{IMDbID: "tt0087182"}, ParsedRelease{Kind: MediaMovie, Title: "Dune", SortTitle: "dune"})
	anchor21 := CatalogTitleID(TorrentRelease{IMDbID: "tt1160419"}, ParsedRelease{Kind: MediaMovie, Title: "Dune", SortTitle: "dune"})
	fallback84 := CatalogTitleID(TorrentRelease{}, ParsedRelease{Kind: MediaMovie, Title: "Dune", SortTitle: "dune", Year: 1984})
	fallback0 := CatalogTitleID(TorrentRelease{}, ParsedRelease{Kind: MediaMovie, Title: "Dune Part Two", SortTitle: "dune", Year: 0})
	if got["m84"] != anchor84 {
		t.Fatalf("year-1984 release should join the 1984 anchor: got %q", got["m84"])
	}
	if got["m0"] != fallback0 || got["m0"] == anchor84 || got["m0"] == anchor21 {
		t.Fatalf("year-0 release must stay fallback: got %q", got["m0"])
	}
	if got["a84"] != anchor84 || got["a21"] != anchor21 || got["m84"] == fallback84 {
		t.Fatalf("anchor ids wrong: %#v", got)
	}
}

func TestResolveFamilyTitleIDsSeriesSingleAnchor(t *testing.T) {
	evidence := []ReleaseEvidence{
		{ReleaseID: "anchor", Title: "Severance", SortTitle: "severance", IMDbID: "tt11280740"},
		{ReleaseID: "yearless", Title: "Severance", SortTitle: "severance"},
		{ReleaseID: "later", Title: "Severance", SortTitle: "severance", Year: 2025},
	}
	got := ResolveFamilyTitleIDs(MediaSeries, "severance", evidence)
	anchor := CatalogTitleID(TorrentRelease{IMDbID: "tt11280740"}, ParsedRelease{Kind: MediaSeries, Title: "Severance", SortTitle: "severance"})
	for _, id := range []string{"anchor", "yearless", "later"} {
		if got[id] != anchor {
			t.Fatalf("%s should join sole series anchor: got %q", id, got[id])
		}
	}
}

func TestResolveFamilyTitleIDsSeriesTwoAnchors(t *testing.T) {
	base := []ReleaseEvidence{
		{ReleaseID: "a1", Title: "Show", SortTitle: "show", IMDbID: "tt0092455"},
		{ReleaseID: "s1", Title: "Show", SortTitle: "show", Year: 1987},
	}
	joined := ResolveFamilyTitleIDs(MediaSeries, "show", base)
	want := CatalogTitleID(TorrentRelease{IMDbID: "tt0092455"}, ParsedRelease{Kind: MediaSeries, Title: "Show", SortTitle: "show"})
	if joined["s1"] != want {
		t.Fatalf("single-anchor stage should join: got %q", joined["s1"])
	}
	expanded := append(append([]ReleaseEvidence{}, base...),
		ReleaseEvidence{ReleaseID: "a2", Title: "Show", SortTitle: "show", IMDbID: "tt0386676"})
	split := ResolveFamilyTitleIDs(MediaSeries, "show", expanded)
	fallback := CatalogTitleID(TorrentRelease{}, ParsedRelease{Kind: MediaSeries, Title: "Show", SortTitle: "show", Year: 1987})
	if split["s1"] != fallback {
		t.Fatalf("two-anchor stage must stay fallback: got %q", split["s1"])
	}
	if split["a1"] == split["a2"] {
		t.Fatal("distinct series anchors must not merge")
	}
}

func TestResolveFamilyTitleIDsKindSafety(t *testing.T) {
	ev := []ReleaseEvidence{{ReleaseID: "r", Title: "Dune", SortTitle: "dune", Year: 2021}}
	movie := ResolveFamilyTitleIDs(MediaMovie, "dune", ev)
	series := ResolveFamilyTitleIDs(MediaSeries, "dune", ev)
	if movie["r"] == series["r"] {
		t.Fatal("same sort title across kinds must produce different ids")
	}
}

func TestResolveFamilyTitleIDsOrderIndependent(t *testing.T) {
	evidence := []ReleaseEvidence{
		{ReleaseID: "a84", Title: "Dune", SortTitle: "dune", IMDbID: "tt0087182", Year: 1984},
		{ReleaseID: "a21", Title: "Dune", SortTitle: "dune", IMDbID: "tt1160419", Year: 2021},
		{ReleaseID: "m84", Title: "Dune", SortTitle: "dune", Year: 1984},
		{ReleaseID: "m0", Title: "Dune Part Two", SortTitle: "dune", Year: 0},
		{ReleaseID: "m21", Title: "Dune", SortTitle: "dune", Year: 2021},
	}
	first := ResolveFamilyTitleIDs(MediaMovie, "dune", evidence)
	shuffled := []ReleaseEvidence{evidence[3], evidence[0], evidence[4], evidence[2], evidence[1]}
	second := ResolveFamilyTitleIDs(MediaMovie, "dune", shuffled)
	if !maps.Equal(first, second) {
		t.Fatalf("order-dependent resolution: %#v vs %#v", first, second)
	}
}
