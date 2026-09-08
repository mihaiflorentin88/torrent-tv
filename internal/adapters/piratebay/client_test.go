package piratebay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// apibay serves search rows with string fields (q.php) and detail objects
// with number fields (t.php). Both real shapes must normalize identically.

const searchRows = `[{"id":"42","name":"Example.2024.1080p.WEB-DL","info_hash":"0123456789abcdef0123456789abcdef01234567","seeders":"9","leechers":"2","size":"4096","num_files":"1","category":"201","added":"1652877231","imdb":""}]`

const detailRow = `{"id":42,"name":"Example.2024.1080p.WEB-DL","info_hash":"0123456789abcdef0123456789abcdef01234567","seeders":9,"leechers":2,"size":4096,"num_files":1,"category":201,"added":1652877231,"imdb":null}`

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	api := server.URL
	return New(func() (string, string) { return "https://thepiratebay.org", api }), server
}

func TestSearchNormalizesStringValuedRows(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/q.php" || r.URL.Query().Get("q") != "Example 2024" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(searchRows))
	})
	releases, err := c.Search(context.Background(), "Example 2024")
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 {
		t.Fatalf("expected 1 release, got %d", len(releases))
	}
	got := releases[0]
	if got.ID != "42" || got.Name != "Example.2024.1080p.WEB-DL" {
		t.Fatalf("identity not normalized: %+v", got)
	}
	if got.TrackerID != "piratebay" || got.TrackerName != "Pirate Bay" {
		t.Fatalf("provenance not populated: tracker=%q/%q", got.TrackerID, got.TrackerName)
	}
	if got.ProviderID != "42" || got.CategoryID != "201" {
		t.Fatalf("provider/category ids not populated: %+v", got)
	}
	if got.BrowseClass != "video" || got.DiscoveryExcluded {
		t.Fatalf("video must be discoverable: class=%q excluded=%v", got.BrowseClass, got.DiscoveryExcluded)
	}
	if got.SizeBytes != 4096 || got.Seeders != 9 || got.Leechers != 2 || got.FileCount != 1 {
		t.Fatalf("numeric fields not normalized: %+v", got)
	}
	if got.IMDbID != "" {
		t.Fatalf("empty imdb must stay empty, got %q", got.IMDbID)
	}
	if got.UploadedAt == nil || got.UploadedAt.UTC() != time.Unix(1652877231, 0).UTC() {
		t.Fatalf("uploaded timestamp not normalized: %+v", got.UploadedAt)
	}
}

func TestAcquireBuildsMagnetFromNumberValuedDetail(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/t.php" || r.URL.Query().Get("id") != "42" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(detailRow))
	})
	acq, err := c.Acquire(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(acq.Metainfo) != 0 {
		t.Fatal("acquire must never download payload files")
	}
	if err := acq.Validate(); err != nil {
		t.Fatalf("acquisition must validate: %v", err)
	}
	u, err := url.Parse(acq.Magnet)
	if err != nil || u.Scheme != "magnet" {
		t.Fatalf("magnet URI must parse: %q (%v)", acq.Magnet, err)
	}
	q := u.Query()
	if q.Get("xt") != "urn:btih:0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("xt must carry the lowercase v1 info hash, got %q", q.Get("xt"))
	}
	if q.Get("dn") != "Example.2024.1080p.WEB-DL" {
		t.Fatalf("dn must carry the display name, got %q", q.Get("dn"))
	}
	trs := q["tr"]
	if len(trs) != len(publicAnnounceURLs) {
		t.Fatalf("expected %d announce endpoints, got %d: %v", len(publicAnnounceURLs), len(trs), trs)
	}
	for i, want := range publicAnnounceURLs {
		if trs[i] != want {
			t.Fatalf("announce %d: got %q want %q", i, trs[i], want)
		}
	}
	if acq.MagnetDiscovery != domain.MagnetDiscoveryPublic {
		t.Fatalf("piratebay acquisition must authorize public discovery, got %d", acq.MagnetDiscovery)
	}
}

func TestSentinelNoResultsRowIsFiltered(t *testing.T) {
	sentinel := `[{"id":"0","name":"No results returned","info_hash":"0000000000000000000000000000000000000000","seeders":"0","leechers":"0","size":"0","num_files":"0","category":"0","added":"0","imdb":""}]`
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(sentinel))
	})
	releases, err := c.Search(context.Background(), "nothing")
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 0 {
		t.Fatalf("sentinel row must produce zero releases, got %+v", releases)
	}
}

func TestSentinelRowMixedWithRealRowKeepsOnlyRealRow(t *testing.T) {
	mixed := `[{"id":"0","name":"No results returned","info_hash":"0000000000000000000000000000000000000000","seeders":"0","leechers":"0","size":"0","num_files":"0","category":"0","added":"0","imdb":""},` + searchRows[1:]
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(mixed))
	})
	releases, err := c.Search(context.Background(), "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || releases[0].ID != "42" {
		t.Fatalf("only the real row must survive: %+v", releases)
	}
}

func TestEmptyIDWithZeroHashAndRealNameErrorsMissingID(t *testing.T) {
	// A real-named row with missing ID and zero hash must not be silently swallowed as a sentinel
	row := `[{"id":"","name":"Real.Movie.2024.1080p","info_hash":"0000000000000000000000000000000000000000","seeders":"1","leechers":"1","size":"100","num_files":"1","category":"201","added":"1652877231","imdb":""}]`
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(row))
	})
	if _, err := c.Search(context.Background(), "x"); err == nil {
		t.Fatal("row with real name and missing ID must error via missing-ID, not be silently dropped as sentinel")
	} else if !strings.Contains(err.Error(), "missing release ID") {
		t.Fatalf("expected missing release ID error, got %v", err)
	}
}

func TestMalformedSizeIsAdapterError(t *testing.T) {
	cases := []struct {
		name string
		row  string
	}{
		{"letters", strings.Replace(searchRows, `"size":"4096"`, `"size":"abc"`, 1)},
		{"fractional", strings.Replace(searchRows, `"size":"4096"`, `"size":"40.5"`, 1)},
		{"negative", strings.Replace(searchRows, `"size":"4096"`, `"size":"-1"`, 1)},
		{"explicit null", strings.Replace(searchRows, `"size":"4096"`, `"size":null`, 1)},
		{"missing", strings.Replace(searchRows, `"size":"4096",`, ``, 1)},
		{"empty string", strings.Replace(searchRows, `"size":"4096"`, `"size":""`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Write([]byte(tc.row))
			})
			if _, err := c.Search(context.Background(), "x"); err == nil {
				t.Fatalf("malformed size (%s) must be an adapter error, not a fabricated zero-size release", tc.name)
			}
		})
	}
}

func TestMalformedInfoHashIsAdapterError(t *testing.T) {
	for _, bad := range []string{`"info_hash":"nothex"`, `"info_hash":"0123456789abcdef0123456789abcdef0123456z"`, `"info_hash":"0000000000000000000000000000000000000000"`} {
		row := strings.Replace(searchRows, `"info_hash":"0123456789abcdef0123456789abcdef01234567"`, bad, 1)
		c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(row))
		})
		if _, err := c.Search(context.Background(), "x"); err == nil {
			t.Fatalf("malformed info hash %s must be an adapter error", bad)
		}
	}
}

func TestSettingsCallbackSwitchRoutesToNewAPIServer(t *testing.T) {
	var firstHits, secondHits atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		if q := r.URL.Query().Get("q"); q != "query-a" {
			t.Errorf("first server received unexpected query %q (cached URL bug)", q)
		}
		w.Write([]byte(searchRows))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		if q := r.URL.Query().Get("q"); q != "query-b" {
			t.Errorf("second server received unexpected query %q", q)
		}
		w.Write([]byte(searchRows))
	}))
	defer second.Close()

	api := first.URL
	c := New(func() (string, string) { return "https://thepiratebay.org", api })
	if _, err := c.Search(context.Background(), "query-a"); err != nil {
		t.Fatal(err)
	}
	api = second.URL
	if _, err := c.Search(context.Background(), "query-b"); err != nil {
		t.Fatal(err)
	}
	if got := firstHits.Load(); got != 1 {
		t.Fatalf("first server expected 1 request, got %d", got)
	}
	if got := secondHits.Load(); got != 1 {
		t.Fatalf("second server expected 1 request, got %d", got)
	}
}

func TestNoAuthorizationHeaderSentToPirateBay(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization header must never be sent to Pirate Bay")
		}
		w.Write([]byte(searchRows))
	})
	if _, err := c.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}

func TestCategoryRejectsUnknownProviderLocalID(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unknown category must be rejected before any request")
	})
	if _, err := c.Category(context.Background(), "999"); err == nil {
		t.Fatal("category outside Categories() must error")
	}
	if _, err := c.Category(context.Background(), ""); err == nil {
		t.Fatal("empty category must error")
	}
}

func TestCategoryRequestsTop100Window(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/precompiled/data_top100_201.json" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(searchRows))
	})
	releases, err := c.Category(context.Background(), "201")
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || releases[0].CategoryID != "201" {
		t.Fatalf("category browse not normalized: %+v", releases)
	}
}

func TestLatestRequestsRecentTop100(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/precompiled/data_top100_recent.json" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(searchRows))
	})
	if _, err := c.Latest(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCategoriesExcludeNonMediaFamilies(t *testing.T) {
	c := New(func() (string, string) { return "", "" })
	seen := map[string]domain.TrackerCategory{}
	for _, cat := range c.Categories() {
		seen[cat.ID] = cat
	}
	for _, id := range []string{"101", "201", "207", "208"} {
		if cat, ok := seen[id]; !ok || cat.Excluded || cat.BrowseClass != "video" && cat.BrowseClass != "audio" {
			t.Fatalf("category %s must be discoverable media: %+v", id, cat)
		}
	}
	for _, id := range []string{"301", "401", "501", "601"} {
		if cat, ok := seen[id]; !ok || !cat.Excluded {
			t.Fatalf("category %s must be excluded from discovery: %+v", id, cat)
		}
	}
	if seen["501"].BrowseClass != "adult" {
		t.Fatalf("500 family is adult content, got class %q", seen["501"].BrowseClass)
	}
}
