package piratebay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/outbound"
)

// Active public announce endpoints observed in https://thepiratebay.org/static/main.js.
// Endpoint presence is observed; individual service uptime is not promised.
var publicAnnounceURLs = [...]string{
	"udp://tracker.opentrackr.org:1337",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.bittor.pw:1337/announce",
	"udp://public.popcorn-tracker.org:6969/announce",
	"udp://tracker.dler.org:6969/announce",
	"udp://exodus.desync.com:6969",
	"udp://open.demonii.com:1337/announce",
	"udp://glotorrents.pw:6969/announce",
	"udp://tracker.coppersurfer.tk:6969",
	"udp://torrent.gresille.org:80/announce",
	"udp://p4p.arenabg.com:1337",
	"udp://tracker.internetwarriors.net:1337",
}

var pirateBayCategories = []domain.TrackerCategory{
	// 100s: Audio (discoverable)
	{ID: "100", Name: "Audio", BrowseClass: "audio", Excluded: false},
	{ID: "101", Name: "Music", BrowseClass: "audio", Excluded: false},
	{ID: "102", Name: "Audio Books", BrowseClass: "audio", Excluded: false},
	{ID: "103", Name: "Sound clips", BrowseClass: "audio", Excluded: false},
	{ID: "104", Name: "FLAC", BrowseClass: "audio", Excluded: false},
	{ID: "199", Name: "Other Audio", BrowseClass: "audio", Excluded: false},

	// 200s: Video (discoverable)
	{ID: "200", Name: "Video", BrowseClass: "video", Excluded: false},
	{ID: "201", Name: "Movies", BrowseClass: "video", Excluded: false},
	{ID: "202", Name: "Movies DVDR", BrowseClass: "video", Excluded: false},
	{ID: "203", Name: "Music videos", BrowseClass: "video", Excluded: false},
	{ID: "204", Name: "Movie Clips", BrowseClass: "video", Excluded: false},
	{ID: "205", Name: "TV-Shows", BrowseClass: "video", Excluded: false},
	{ID: "206", Name: "Handheld", BrowseClass: "video", Excluded: false},
	{ID: "207", Name: "HD Movies", BrowseClass: "video", Excluded: false},
	{ID: "208", Name: "HD TV-Shows", BrowseClass: "video", Excluded: false},
	{ID: "209", Name: "3D", BrowseClass: "video", Excluded: false},
	{ID: "210", Name: "CAM/TS", BrowseClass: "video", Excluded: false},
	{ID: "211", Name: "UHD/4k Movies", BrowseClass: "video", Excluded: false},
	{ID: "212", Name: "UHD/4k TV-Shows", BrowseClass: "video", Excluded: false},
	{ID: "299", Name: "Other Video", BrowseClass: "video", Excluded: false},

	// 300s: Applications / Software (excluded from discovery)
	{ID: "300", Name: "Applications", BrowseClass: "software", Excluded: true},
	{ID: "301", Name: "Windows", BrowseClass: "software", Excluded: true},
	{ID: "302", Name: "Mac/Apple", BrowseClass: "software", Excluded: true},
	{ID: "303", Name: "UNIX", BrowseClass: "software", Excluded: true},
	{ID: "304", Name: "Handheld", BrowseClass: "software", Excluded: true},
	{ID: "305", Name: "IOS(iPad/iPhone)", BrowseClass: "software", Excluded: true},
	{ID: "306", Name: "Android", BrowseClass: "software", Excluded: true},
	{ID: "399", Name: "Other OS", BrowseClass: "software", Excluded: true},

	// 400s: Games (excluded from discovery)
	{ID: "400", Name: "Games", BrowseClass: "games", Excluded: true},
	{ID: "401", Name: "PC", BrowseClass: "games", Excluded: true},
	{ID: "402", Name: "Mac/Apple", BrowseClass: "games", Excluded: true},
	{ID: "403", Name: "PSx", BrowseClass: "games", Excluded: true},
	{ID: "404", Name: "XBOX360", BrowseClass: "games", Excluded: true},
	{ID: "405", Name: "Wii", BrowseClass: "games", Excluded: true},
	{ID: "406", Name: "Handheld", BrowseClass: "games", Excluded: true},
	{ID: "407", Name: "IOS(iPad/iPhone)", BrowseClass: "games", Excluded: true},
	{ID: "408", Name: "Android", BrowseClass: "games", Excluded: true},
	{ID: "499", Name: "Other OS", BrowseClass: "games", Excluded: true},

	// 500s: Adult / Porn (adult content != ordinary video, excluded from discovery)
	{ID: "500", Name: "Porn", BrowseClass: "adult", Excluded: true},
	{ID: "501", Name: "Movies", BrowseClass: "adult", Excluded: true},
	{ID: "502", Name: "Movies DVDR", BrowseClass: "adult", Excluded: true},
	{ID: "503", Name: "Pictures", BrowseClass: "adult", Excluded: true},
	{ID: "504", Name: "Games", BrowseClass: "adult", Excluded: true},
	{ID: "505", Name: "HD Movies", BrowseClass: "adult", Excluded: true},
	{ID: "506", Name: "Movie Clips", BrowseClass: "adult", Excluded: true},
	{ID: "507", Name: "UHD/4k Movies", BrowseClass: "adult", Excluded: true},
	{ID: "599", Name: "Other Adult", BrowseClass: "adult", Excluded: true},

	// 600s: Other (excluded from discovery)
	{ID: "600", Name: "Other", BrowseClass: "other", Excluded: true},
	{ID: "601", Name: "E-books", BrowseClass: "other", Excluded: true},
	{ID: "602", Name: "Comics", BrowseClass: "other", Excluded: true},
	{ID: "603", Name: "Pictures", BrowseClass: "other", Excluded: true},
	{ID: "604", Name: "Covers", BrowseClass: "other", Excluded: true},
	{ID: "605", Name: "Physibles", BrowseClass: "other", Excluded: true},
	{ID: "699", Name: "Other", BrowseClass: "other", Excluded: true},
}

var categoryByID = func() map[string]domain.TrackerCategory {
	m := make(map[string]domain.TrackerCategory, len(pirateBayCategories))
	for _, c := range pirateBayCategories {
		m[c.ID] = c
	}
	return m
}()

type Client struct {
	settings func() (websiteURL, apiURL string)
	http     *http.Client
}

func New(settings func() (websiteURL, apiURL string)) *Client {
	return &Client{
		settings: settings,
		http:     &http.Client{Timeout: 45 * time.Second},
	}
}

func (c *Client) ID() string { return "piratebay" }

func (c *Client) Name() string { return "Pirate Bay" }

func (c *Client) Capabilities() application.TrackerCapabilities {
	return application.TrackerCapabilities{Categories: true}
}

func (c *Client) Categories() []domain.TrackerCategory {
	out := make([]domain.TrackerCategory, len(pirateBayCategories))
	copy(out, pirateBayCategories)
	return out
}

func (c *Client) Latest(ctx context.Context) ([]domain.TorrentRelease, error) {
	return c.list(ctx, "/precompiled/data_top100_recent.json")
}

func (c *Client) Category(ctx context.Context, providerLocal string) ([]domain.TorrentRelease, error) {
	cat, ok := categoryByID[providerLocal]
	if !ok {
		return nil, fmt.Errorf("unknown Pirate Bay category ID: %q", providerLocal)
	}
	return c.list(ctx, "/precompiled/data_top100_"+cat.ID+".json")
}

func (c *Client) Search(ctx context.Context, q string) ([]domain.TorrentRelease, error) {
	values := url.Values{}
	values.Set("q", q)
	return c.list(ctx, "/q.php?"+values.Encode())
}

func (c *Client) Acquire(ctx context.Context, providerID string) (domain.TorrentAcquisition, error) {
	if strings.TrimSpace(providerID) == "" {
		return domain.TorrentAcquisition{}, errors.New("provider ID is required")
	}
	values := url.Values{}
	values.Set("id", providerID)
	r, err := c.request(ctx, "/t.php?"+values.Encode())
	if err != nil {
		return domain.TorrentAcquisition{}, err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return domain.TorrentAcquisition{}, fmt.Errorf("Pirate Bay returned HTTP %d", r.StatusCode)
	}
	body := io.LimitReader(r.Body, 2<<20)
	dec := json.NewDecoder(body)
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return domain.TorrentAcquisition{}, fmt.Errorf("decode Pirate Bay detail: %w", err)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		if arr, isArr := raw.([]any); isArr && len(arr) > 0 {
			m, _ = arr[0].(map[string]any)
		}
	}
	if m == nil {
		return domain.TorrentAcquisition{}, fmt.Errorf("Pirate Bay detail was not an object")
	}
	rel, keep, err := parseRow(m)
	if err != nil {
		return domain.TorrentAcquisition{}, err
	}
	if !keep {
		return domain.TorrentAcquisition{}, fmt.Errorf("%w: torrent %s not found on Pirate Bay", domain.ErrTorrentRemoved, providerID)
	}

	hash := parseString(m["info_hash"])
	name := rel.Name

	q := url.Values{}
	q.Set("xt", "urn:btih:"+strings.ToLower(hash))
	q.Set("dn", name)
	for _, announce := range publicAnnounceURLs {
		q.Add("tr", announce)
	}
	uri := "magnet:?" + q.Encode()

	acq := domain.TorrentAcquisition{Magnet: uri, MagnetDiscovery: domain.MagnetDiscoveryPublic}
	if err := acq.Validate(); err != nil {
		return domain.TorrentAcquisition{}, err
	}
	return acq, nil
}

func (c *Client) list(ctx context.Context, path string) ([]domain.TorrentRelease, error) {
	r, err := c.request(ctx, path)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Pirate Bay returned HTTP %d", r.StatusCode)
	}
	body := io.LimitReader(r.Body, 8<<20)
	dec := json.NewDecoder(body)
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode Pirate Bay response: %w", err)
	}
	arr, ok := raw.([]any)
	if !ok {
		if m, isMap := raw.(map[string]any); isMap {
			rel, keep, err := parseRow(m)
			if err != nil {
				return nil, err
			}
			if keep {
				return []domain.TorrentRelease{rel}, nil
			}
			return []domain.TorrentRelease{}, nil
		}
		return nil, fmt.Errorf("Pirate Bay response was not an array")
	}
	out := make([]domain.TorrentRelease, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rel, keep, err := parseRow(m)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, rel)
		}
	}
	return out, nil
}

func (c *Client) request(ctx context.Context, path string) (*http.Response, error) {
	_, api := c.settings()
	api = strings.TrimRight(strings.TrimSpace(api), "/")
	if api == "" {
		api = "https://apibay.org"
	}
	return outbound.Do(ctx, c.http, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "TorrentTV/0.2")
		return req, nil
	}, outbound.Policy{Provider: "PirateBay", Attempts: 3, MaxInlineDelay: 15 * time.Second})
}

func parseRow(m map[string]any) (domain.TorrentRelease, bool, error) {
	idStr := parseString(m["id"])
	nameStr := parseString(m["name"])
	hashStr := parseString(m["info_hash"])

	// Sentinel no-results row filtering: apibay emits the exact synthetic record
	// {"id":"0","name":"No results returned","info_hash":"0000000000000000000000000000000000000000"}
	// when a query yields no items. Any other record missing an ID is an error.
	if idStr == "0" && hashStr == "0000000000000000000000000000000000000000" && nameStr == "No results returned" {
		return domain.TorrentRelease{}, false, nil
	}

	if idStr == "" {
		return domain.TorrentRelease{}, false, errors.New("missing release ID")
	}

	if err := validateInfoHash(hashStr); err != nil {
		return domain.TorrentRelease{}, false, fmt.Errorf("release %s: %w", idStr, err)
	}

	size, err := requireUint(m, "size")
	if err != nil {
		return domain.TorrentRelease{}, false, fmt.Errorf("release %s: %w", idStr, err)
	}
	seeders, err := parseUint(m["seeders"], "seeders")
	if err != nil {
		return domain.TorrentRelease{}, false, fmt.Errorf("release %s: %w", idStr, err)
	}
	leechers, err := parseUint(m["leechers"], "leechers")
	if err != nil {
		return domain.TorrentRelease{}, false, fmt.Errorf("release %s: %w", idStr, err)
	}
	numFiles, err := parseUint(m["num_files"], "num_files")
	if err != nil {
		return domain.TorrentRelease{}, false, fmt.Errorf("release %s: %w", idStr, err)
	}
	uploadedAt, err := parseAdded(m["added"])
	if err != nil {
		return domain.TorrentRelease{}, false, fmt.Errorf("release %s: %w", idStr, err)
	}

	catID := parseString(m["category"])
	catName, browseClass, excluded := categoryProps(catID)
	imdbID := parseIMDb(m["imdb"])

	rel := domain.TorrentRelease{
		ID:                idStr,
		TrackerID:         "piratebay",
		TrackerName:       "Pirate Bay",
		ProviderID:        idStr,
		CategoryID:        catID,
		BrowseClass:       browseClass,
		DiscoveryExcluded: excluded,
		Name:              nameStr,
		Category:          catName,
		SizeBytes:         int64(size),
		IMDbID:            imdbID,
		Seeders:           int(seeders),
		Leechers:          int(leechers),
		FileCount:         int(numFiles),
		UploadedAt:        uploadedAt,
	}
	return rel, true, nil
}

func validateInfoHash(hash string) error {
	if len(hash) != 40 {
		return fmt.Errorf("invalid info hash length %d: expected 40", len(hash))
	}
	isZero := true
	for i := range hash {
		b := hash[i]
		isHex := (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
		if !isHex {
			return fmt.Errorf("invalid info hash character %q", b)
		}
		if b != '0' {
			isZero = false
		}
	}
	if isZero {
		return errors.New("info hash cannot be all zeros")
	}
	return nil
}

func parseUint(v any, fieldName string) (uint64, error) {
	var s string
	switch val := v.(type) {
	case json.Number:
		s = val.String()
	case string:
		s = strings.TrimSpace(val)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("%s has unsupported type %T", fieldName, v)
	}
	if s == "" {
		return 0, nil
	}
	if strings.ContainsAny(s, ".eE") {
		return 0, fmt.Errorf("%s cannot be fractional: %q", fieldName, s)
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s invalid unsigned integer %q: %w", fieldName, s, err)
	}
	return n, nil
}

func requireUint(m map[string]any, fieldName string) (uint64, error) {
	v, ok := m[fieldName]
	if !ok || v == nil {
		return 0, fmt.Errorf("%s is required", fieldName)
	}
	if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
		return 0, fmt.Errorf("%s is required", fieldName)
	}
	return parseUint(v, fieldName)
}

func parseAdded(v any) (*time.Time, error) {
	ts, err := parseUint(v, "added")
	if err != nil {
		return nil, err
	}
	if ts == 0 {
		return nil, nil
	}
	t := time.Unix(int64(ts), 0).UTC()
	return &t, nil
}

func parseString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case json.Number:
		return val.String()
	default:
		return ""
	}
}

func parseIMDb(v any) string {
	s := strings.TrimSpace(parseString(v))
	if s == "" || s == "0" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(s), "tt") {
		return s
	}
	return "tt" + s
}

func categoryProps(catID string) (name string, browseClass string, excluded bool) {
	if cat, ok := categoryByID[catID]; ok {
		return cat.Name, cat.BrowseClass, cat.Excluded
	}
	if len(catID) >= 1 {
		switch catID[0] {
		case '1':
			return "Audio", "audio", false
		case '2':
			return "Video", "video", false
		case '3':
			return "Applications", "software", true
		case '4':
			return "Games", "games", true
		case '5':
			return "Porn", "adult", true
		case '6':
			return "Other", "other", true
		}
	}
	return "Other", "other", true
}
