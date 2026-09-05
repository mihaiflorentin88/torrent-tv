package filelist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/outbound"
)

type Client struct {
	settings func() (string, string, string)
	http     *http.Client
	mu       sync.Mutex
	last     time.Time
	requests []time.Time
}

func New(settings func() (string, string, string)) *Client {
	return &Client{settings: settings, http: &http.Client{Timeout: 45 * time.Second}}
}
func (c *Client) ID() string { return "filelist" }
func (c *Client) Capabilities() application.TrackerCapabilities {
	return application.TrackerCapabilities{IMDbSearch: true, SeasonFilter: true, EpisodeFilter: true, Categories: true}
}

func (c *Client) Latest(ctx context.Context) ([]domain.TorrentRelease, error) {
	return c.list(ctx, "/api.php?action=latest-torrents&limit=100")
}

func (c *Client) Category(ctx context.Context, id int) ([]domain.TorrentRelease, error) {
	return c.list(ctx, "/api.php?action=latest-torrents&category="+strconv.Itoa(id)+"&limit=100")
}

func (c *Client) Search(ctx context.Context, q string) ([]domain.TorrentRelease, error) {
	return c.list(ctx, "/api.php?action=search-torrents&type=name&query="+url.QueryEscape(q))
}

func (c *Client) list(ctx context.Context, path string) ([]domain.TorrentRelease, error) {
	r, err := c.request(ctx, path)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return nil, fmt.Errorf("FileList returned HTTP %d", r.StatusCode)
	}
	body := io.LimitReader(r.Body, 8<<20)
	dec := json.NewDecoder(body)
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode FileList response: %w", err)
	}
	arr, ok := raw.([]any)
	if !ok {
		if m, yes := raw.(map[string]any); yes {
			arr, _ = m["torrents"].([]any)
		}
	}
	if arr == nil {
		return nil, fmt.Errorf("FileList response was not a torrent array")
	}
	out := make([]domain.TorrentRelease, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		x := domain.TorrentRelease{ID: str(m, "id", "torrent_id", "guid"), Name: str(m, "name", "title"), Category: str(m, "category", "type"), SizeBytes: i64(m, "size", "size_bytes"), IMDbID: imdb(str(m, "imdb")), Seeders: i(m, "seeders"), Leechers: i(m, "leechers"), TimesCompleted: i(m, "times_completed"), Freeleech: b(m, "freeleech"), DoubleUp: b(m, "doubleup"), Internal: b(m, "internal"), Moderated: b(m, "moderated"), SmallDescription: str(m, "small_description"), FileCount: i(m, "files"), Comments: i(m, "comments")}
		if t, err := time.Parse("2006-01-02 15:04:05", str(m, "upload_date")); err == nil {
			u := t.UTC()
			x.UploadedAt = &u
		}
		if x.ID != "" {
			out = append(out, x)
		}
	}
	return out, nil
}

// OpenTorrent fetches the .torrent for a catalogued release. FileList serves
// its error pages with HTTP 200, so the body is inspected before handing it
// to the torrent engine: a removed release would otherwise surface as an
// opaque bencode syntax error.
func (c *Client) OpenTorrent(ctx context.Context, id string) (io.ReadCloser, error) {
	_, _, pass := c.settings()
	r, err := c.request(ctx, "/download.php?id="+url.QueryEscape(id)+"&passkey="+url.QueryEscape(pass))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return nil, fmt.Errorf("torrent download returned HTTP %d", r.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read torrent download: %w", err)
	}
	if len(raw) == 0 || raw[0] != 'd' {
		if bytes.Contains(raw, []byte("Nu pot gasi fisierul .torrent")) {
			return nil, fmt.Errorf("%w: FileList no longer hosts the .torrent for this release", domain.ErrTorrentRemoved)
		}
		return nil, fmt.Errorf("FileList returned a non-torrent response (HTTP %d) for this release", r.StatusCode)
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

func (c *Client) request(ctx context.Context, path string) (*http.Response, error) {
	base, user, pass := c.settings()
	base = strings.TrimRight(base, "/")
	if user == "" || pass == "" {
		return nil, fmt.Errorf("FileList credentials are not configured")
	}
	c.mu.Lock()
	now := time.Now()
	cutoff := now.Add(-time.Hour)
	kept := c.requests[:0]
	for _, at := range c.requests {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	c.requests = kept
	if len(c.requests) >= 140 {
		retryAt := c.requests[0].Add(time.Hour)
		c.mu.Unlock()
		return nil, &outbound.RateLimitError{Provider: "FileList", RetryAt: retryAt, Attempts: 0, Detail: "local 140 request/hour safety ceiling"}
	}
	wait := 750*time.Millisecond - time.Since(c.last)
	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.mu.Unlock()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	c.last = time.Now()
	c.requests = append(c.requests, c.last)
	c.mu.Unlock()
	return outbound.Do(ctx, c.http, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(user, pass)
		req.Header.Set("User-Agent", "TorrentTV/0.2")
		return req, nil
	}, outbound.Policy{Provider: "FileList", Attempts: 3, MaxInlineDelay: 15 * time.Second})
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case string:
				if x != "" {
					return x
				}
			case json.Number:
				return x.String()
			case float64:
				return strconv.FormatFloat(x, 'f', -1, 64)
			}
		}
	}
	return ""
}

func i64(m map[string]any, k ...string) int64 {
	v, _ := strconv.ParseInt(str(m, k...), 10, 64)
	return v
}
func i(m map[string]any, k ...string) int { return int(i64(m, k...)) }
func b(m map[string]any, k ...string) bool {
	v := strings.ToLower(str(m, k...))
	return v == "1" || v == "true"
}

func imdb(v string) string {
	if v == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(v), "tt") {
		return v
	}
	return "tt" + v
}
