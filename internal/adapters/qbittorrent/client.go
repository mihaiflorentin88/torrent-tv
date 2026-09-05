package qbittorrent

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/outbound"
)

type Client struct {
	settings func() (string, string, string)
	base     string
	http     *http.Client
	mu       sync.Mutex
	prepare  sync.Map
	ready    sync.Map
	logged   bool
	authless bool
}

func New(settings func() (string, string, string)) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{settings: settings, http: &http.Client{Timeout: 45 * time.Second, Jar: jar}}
}

func (c *Client) login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	base, user, pass := c.settings()
	base = strings.TrimRight(base, "/") + "/"
	if c.base != base {
		c.base = base
		c.logged = false
	}
	if c.logged {
		return nil
	}
	switch {
	case user == "" && pass == "":
		// The bundled no-auth sidecar (ADR-0005) bypasses WebUI
		// authentication for the household LAN, so no session is needed.
		c.authless = true
		c.logged = true
		return nil
	case user == "" || pass == "":
		return fmt.Errorf("qBittorrent credentials are misconfigured: set both username and password, or leave both empty for the credential-free sidecar")
	}
	form := url.Values{"username": {user}, "password": {pass}}
	r, err := outbound.Do(ctx, c.http, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"api/v2/auth/login", strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Referer", strings.TrimRight(c.base, "/"))
		}
		return req, err
	}, outbound.Policy{Provider: "qBittorrent", Attempts: 3, MaxInlineDelay: 10 * time.Second})
	if err != nil {
		return err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
	authenticated := strings.Contains(strings.ToLower(string(b)), "ok")
	if parsed, parseErr := url.Parse(c.base); parseErr == nil && len(c.http.Jar.Cookies(parsed)) > 0 {
		authenticated = true
	}
	if r.StatusCode/100 != 2 || !authenticated {
		return fmt.Errorf("qBittorrent rejected URL or credentials")
	}
	c.logged = true
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	var data []byte
	var err error
	if body != nil {
		data, err = io.ReadAll(io.LimitReader(body, 20<<20))
		if err != nil {
			return nil, err
		}
	}
	makeReq := func() (*http.Request, error) {
		var reader io.Reader
		if data != nil {
			reader = bytes.NewReader(data)
		}
		r, e := http.NewRequestWithContext(ctx, method, c.base+path, reader)
		if e != nil {
			return nil, e
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		r.Header.Set("Referer", strings.TrimRight(c.base, "/"))
		return r, nil
	}
	resp, err := outbound.Do(ctx, c.http, makeReq, outbound.Policy{Provider: "qBittorrent", Attempts: 3, MaxInlineDelay: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusForbidden {
		return resp, nil
	}
	resp.Body.Close()
	c.mu.Lock()
	authless := c.authless
	c.logged = false
	c.mu.Unlock()
	if authless {
		return nil, fmt.Errorf("qBittorrent rejected the credential-free request with HTTP 403")
	}
	if err := c.login(ctx); err != nil {
		return nil, err
	}
	return outbound.Do(ctx, c.http, makeReq, outbound.Policy{Provider: "qBittorrent", Attempts: 3, MaxInlineDelay: 10 * time.Second})
}

func (c *Client) Test(ctx context.Context) (string, error) {
	r, err := c.do(ctx, http.MethodGet, "api/v2/app/version", nil, "")
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 128))
	if r.StatusCode/100 != 2 {
		return "", fmt.Errorf("qBittorrent HTTP %d", r.StatusCode)
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *Client) Add(ctx context.Context, reader io.Reader, savePath string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 16<<20))
	if err != nil {
		return "", err
	}
	hash, err := infoHash(data)
	if err != nil {
		return "", err
	}
	// A torrent re-added with the same info hash must receive a fresh streaming
	// scheduler setup even when this client prepared an earlier instance.
	c.ready.Delete(hash)
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("torrents", "filelist.torrent")
	part.Write(data)
	w.WriteField("savepath", savePath)
	w.WriteField("category", "torrent-tv")
	w.WriteField("sequentialDownload", "true")
	w.WriteField("firstLastPiecePrio", "true")
	w.Close()
	r, err := c.do(ctx, http.MethodPost, "api/v2/torrents/add", bytes.NewReader(body.Bytes()), w.FormDataContentType())
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	result, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
	if !addAccepted(r.StatusCode, result) {
		// Two rejection shapes mean "this torrent is already in the list":
		// a 2xx body containing "Fails." (older qBittorrent) and HTTP 409
		// Conflict (qBittorrent >= 4.4, including the 5.x nox image). The
		// existing torrent is exactly the one wanted, so confirming it via
		// Status turns a re-add into an idempotent success instead of a
		// failed playback on every container restart.
		duplicate := r.StatusCode == http.StatusConflict ||
			(r.StatusCode/100 == 2 && strings.Contains(strings.ToLower(string(result)), "fail"))
		if duplicate {
			if _, statusErr := c.Status(ctx, hash); statusErr == nil {
				return hash, nil
			}
		}
		return "", fmt.Errorf("qBittorrent rejected torrent: HTTP %d", r.StatusCode)
	}
	return hash, nil
}

func addAccepted(status int, body []byte) bool {
	return status/100 == 2 && !strings.Contains(strings.ToLower(string(body)), "fail")
}

func (c *Client) Files(ctx context.Context, hash string) ([]domain.TorrentFile, error) {
	var rows []map[string]any
	if err := c.getJSON(ctx, "api/v2/torrents/files?hash="+url.QueryEscape(hash), &rows); err != nil {
		return nil, err
	}
	out := make([]domain.TorrentFile, 0, len(rows))
	var offset int64
	for n, x := range rows {
		path := s(x, "name")
		size := l(x, "size")
		idx := n
		if v, ok := x["index"]; ok {
			idx = int(num(v))
		}
		out = append(out, domain.TorrentFile{Index: idx, Path: path, SizeBytes: size, Progress: f(x, "progress"), Priority: int(l(x, "priority")), Offset: offset, Playable: playable(path)})
		offset += size
	}
	return out, nil
}

func (c *Client) Status(ctx context.Context, hash string) (domain.DownloadStatus, error) {
	var rows []map[string]any
	if err := c.getJSON(ctx, "api/v2/torrents/info?hashes="+url.QueryEscape(hash), &rows); err != nil {
		return domain.DownloadStatus{}, err
	}
	if len(rows) == 0 {
		return domain.DownloadStatus{}, domain.ErrTorrentNotFound
	}
	x := rows[0]
	// qBittorrent's total_size includes files that were explicitly deselected.
	// amount_left and progress describe the selected download set, so pair them
	// with size and only fall back to total_size for older API responses.
	total := l(x, "size")
	if total <= 0 {
		total = l(x, "total_size")
	}
	properties, err := c.properties(ctx, hash)
	if err != nil {
		return domain.DownloadStatus{}, fmt.Errorf("qBittorrent torrent properties: %w", err)
	}
	pieceSize := l(properties, "piece_size")
	if pieceSize <= 0 {
		pieceSize = l(x, "piece_size")
	}
	d := domain.DownloadStatus{Hash: hash, State: canonicalState(s(x, "state")), Progress: f(x, "progress"), TotalBytes: total, DownloadedBytes: total - l(x, "amount_left"), SpeedBytesPerSecond: l(x, "dlspeed"), UploadSpeedBytesPerSecond: l(x, "upspeed"), ETASeconds: l(x, "eta"), Peers: int(l(x, "num_leechs")), Seeds: int(l(x, "num_seeds")), PieceSize: pieceSize, Sequential: bo(x, "seq_dl"), FirstLastPriority: bo(x, "f_l_piece_prio"), SavePath: s(x, "save_path"), ContentPath: s(x, "content_path")}
	if d.Progress < 1 {
		var preferences map[string]any
		if c.getJSON(ctx, "api/v2/app/preferences", &preferences) == nil {
			d.TempPathEnabled = bo(preferences, "temp_path_enabled")
			d.TempPath = s(preferences, "temp_path")
		}
	}
	var trackers []map[string]any
	if c.getJSON(ctx, "api/v2/torrents/trackers?hash="+url.QueryEscape(hash), &trackers) == nil {
		for _, t := range trackers {
			u := s(t, "url")
			if !strings.HasPrefix(u, "**") {
				d.Trackers = append(d.Trackers, domain.TrackerStatus{URL: u, Status: int(l(t, "status")), Message: s(t, "msg"), Peers: int(l(t, "num_peers")), Seeds: int(l(t, "num_seeds"))})
			}
		}
	}
	return d, nil
}

func (c *Client) Pieces(ctx context.Context, hash string) (domain.PieceMap, error) {
	var states []int
	if err := c.getJSON(ctx, "api/v2/torrents/pieceStates?hash="+url.QueryEscape(hash), &states); err != nil {
		return domain.PieceMap{}, err
	}
	properties, err := c.properties(ctx, hash)
	if err != nil {
		return domain.PieceMap{}, fmt.Errorf("qBittorrent torrent properties: %w", err)
	}
	return domain.PieceMap{States: states, PieceSize: l(properties, "piece_size")}, nil
}

func (c *Client) properties(ctx context.Context, hash string) (map[string]any, error) {
	var result map[string]any
	err := c.getJSON(ctx, "api/v2/torrents/properties?hash="+url.QueryEscape(hash), &result)
	return result, err
}

func (c *Client) PrepareFile(ctx context.Context, hash string, index int, subtitleIndices []int) error {
	return c.PrepareFiles(ctx, hash, []int{index}, subtitleIndices)
}

func (c *Client) PrepareFiles(ctx context.Context, hash string, indices []int, subtitleIndices []int) error {
	lockAny, _ := c.prepare.LoadOrStore(hash, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	files, err := c.Files(ctx, hash)
	if err != nil {
		return err
	}
	wanted := map[int]bool{}
	media := map[int]bool{}
	for _, index := range indices {
		wanted[index] = true
		media[index] = true
	}
	for _, i := range subtitleIndices {
		wanted[i] = true
	}
	unwanted := []string{}
	selected := []string{}
	selectedCount := 0
	prioritiesChanged := false
	for _, f := range files {
		if wanted[f.Index] {
			if media[f.Index] {
				selectedCount++
			}
			// Keep wanted files at normal priority. Setting the whole media file
			// to maximum priority gives every piece the same priority and defeats
			// qBittorrent's first/last-piece scheduling.
			if f.Priority != 1 {
				selected = append(selected, strconv.Itoa(f.Index))
			}
		} else {
			if f.Priority != 0 {
				unwanted = append(unwanted, strconv.Itoa(f.Index))
			}
		}
	}
	if len(unwanted) > 0 {
		if err := c.command(ctx, "filePrio", url.Values{"hash": {hash}, "id": {strings.Join(unwanted, "|")}, "priority": {"0"}}); err != nil {
			return err
		}
		prioritiesChanged = true
	}
	if len(media) == 0 || selectedCount != len(media) {
		return fmt.Errorf("one or more qBittorrent selected files are unavailable")
	}
	if len(selected) > 0 {
		if err := c.command(ctx, "filePrio", url.Values{"hash": {hash}, "id": {strings.Join(selected, "|")}, "priority": {"1"}}); err != nil {
			return err
		}
		prioritiesChanged = true
	}
	_, preparedBefore := c.ready.Load(hash)
	reapplySchedulers := prioritiesChanged || !preparedBefore

	// File priority changes can flatten qBittorrent's special first/last
	// piece priorities. qBittorrent 4.3 also sometimes reports both streaming
	// flags after add without scheduling the edge pieces, so reapply each flag
	// on the first preparation after add or application startup.
	st, err := c.Status(ctx, hash)
	if err != nil {
		return err
	}
	if reapplySchedulers && st.Sequential {
		if err := c.command(ctx, "toggleSequentialDownload", url.Values{"hashes": {hash}}); err != nil {
			return err
		}
		st.Sequential = false
	}
	if !st.Sequential {
		if err := c.command(ctx, "toggleSequentialDownload", url.Values{"hashes": {hash}}); err != nil {
			return err
		}
	}
	if reapplySchedulers && st.FirstLastPriority {
		if err := c.command(ctx, "toggleFirstLastPiecePrio", url.Values{"hashes": {hash}}); err != nil {
			return err
		}
		st.FirstLastPriority = false
	}
	if !st.FirstLastPriority {
		if err := c.command(ctx, "toggleFirstLastPiecePrio", url.Values{"hashes": {hash}}); err != nil {
			return err
		}
	}
	verified, err := c.Status(ctx, hash)
	if err != nil {
		return err
	}
	if !verified.Sequential || !verified.FirstLastPriority {
		return fmt.Errorf("qBittorrent did not retain progressive streaming priorities")
	}
	c.ready.Store(hash, struct{}{})
	return nil
}

// PrepareRange is a no-op: qBittorrent exposes no range-priority API and its
// sequential download scheduler already reaches any requested range.
func (c *Client) PrepareRange(context.Context, string, int, int64, int64) error {
	return nil
}

func (c *Client) Pause(ctx context.Context, hash string) error {
	return c.command(ctx, "pause", url.Values{"hashes": {hash}})
}

func (c *Client) Resume(ctx context.Context, hash string) error {
	code, err := c.commandStatus(ctx, "resume", url.Values{"hashes": {hash}})
	if err != nil {
		return err
	}
	if code == http.StatusNotFound {
		return fmt.Errorf("qBittorrent resume: %w", domain.ErrTorrentNotFound)
	}
	if code/100 != 2 {
		return fmt.Errorf("qBittorrent resume returned HTTP %d", code)
	}
	return nil
}

func (c *Client) Remove(ctx context.Context, hash string, deleteFiles bool) error {
	return c.command(ctx, "delete", url.Values{"hashes": {hash}, "deleteFiles": {strconv.FormatBool(deleteFiles)}})
}

func (c *Client) command(ctx context.Context, path string, v url.Values) error {
	code, err := c.commandStatus(ctx, path, v)
	if err != nil {
		return err
	}
	if code/100 != 2 {
		return fmt.Errorf("qBittorrent %s returned HTTP %d", path, code)
	}
	return nil
}

func (c *Client) commandStatus(ctx context.Context, path string, v url.Values) (int, error) {
	r, err := c.do(ctx, http.MethodPost, "api/v2/torrents/"+path, strings.NewReader(v.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return 0, err
	}
	defer r.Body.Close()
	return r.StatusCode, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	r, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		return fmt.Errorf("qBittorrent returned HTTP %d", r.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(out)
}

func s(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return fmt.Sprint(m[k])
}
func l(m map[string]any, k string) int64   { return int64(num(m[k])) }
func f(m map[string]any, k string) float64 { return num(m[k]) }
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		n, _ := x.Float64()
		return n
	case string:
		n, _ := strconv.ParseFloat(x, 64)
		return n
	}
	return 0
}
func bo(m map[string]any, k string) bool { v, _ := m[k].(bool); return v }

// playable is shared-by-copy with the native torrent adapter's
// media-extension test: the two copies must move together.
func playable(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".webm", ".m4v", ".ts", ".m2ts":
		return true
	}
	return false
}

// canonicalState maps a raw qBittorrent state string onto the canonical
// download-state vocabulary (domain/state.go). Unknown values pass through
// unchanged so novel engine states are never silently reclassified.
func canonicalState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "downloading", "metadl", "forceddl", "stalleddl", "allocating", "checkingdl", "checkingresumedata":
		return domain.StateDownloading
	case "uploading", "stalledup", "forcedup", "checkingup":
		return domain.StateSeeding
	case "pauseddl", "stoppeddl":
		return domain.StatePausedDL
	case "pausedup", "stoppedup":
		return domain.StatePausedUP
	case "queueddl", "queuedup":
		return domain.StateQueued
	case "error", "missingfiles":
		return domain.StateError
	default:
		return raw
	}
}

func infoHash(b []byte) (string, error) {
	start, end, err := findInfo(b)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(b[start:end])
	return hex.EncodeToString(sum[:]), nil
}

func findInfo(b []byte) (int, int, error) {
	if len(b) == 0 || b[0] != 'd' {
		return 0, 0, fmt.Errorf("invalid torrent metadata")
	}
	i := 1
	for i < len(b) && b[i] != 'e' {
		ks, ke, n, err := scanString(b, i)
		if err != nil {
			return 0, 0, err
		}
		i = n
		end, err := scanValue(b, i)
		if err != nil {
			return 0, 0, err
		}
		if string(b[ks:ke]) == "info" {
			return i, end, nil
		}
		i = end
	}
	return 0, 0, fmt.Errorf("torrent info dictionary missing")
}

func scanString(b []byte, i int) (int, int, int, error) {
	colon := bytes.IndexByte(b[i:], ':')
	if colon < 0 {
		return 0, 0, 0, fmt.Errorf("invalid bencode string")
	}
	colon += i
	n, err := strconv.Atoi(string(b[i:colon]))
	if err != nil || n < 0 || colon+1+n > len(b) {
		return 0, 0, 0, fmt.Errorf("invalid bencode string")
	}
	return colon + 1, colon + 1 + n, colon + 1 + n, nil
}

func scanValue(b []byte, i int) (int, error) {
	if i >= len(b) {
		return 0, fmt.Errorf("unexpected bencode end")
	}
	switch b[i] {
	case 'i':
		j := bytes.IndexByte(b[i+1:], 'e')
		if j < 0 {
			return 0, fmt.Errorf("invalid integer")
		}
		return i + 1 + j + 1, nil
	case 'l':
		i++
		for i < len(b) && b[i] != 'e' {
			n, e := scanValue(b, i)
			if e != nil {
				return 0, e
			}
			i = n
		}
		if i >= len(b) {
			return 0, fmt.Errorf("unterminated bencode")
		}
		return i + 1, nil
	case 'd':
		i++
		for i < len(b) && b[i] != 'e' {
			_, _, n, e := scanString(b, i)
			if e != nil {
				return 0, e
			}
			i = n
			n, e = scanValue(b, i)
			if e != nil {
				return 0, e
			}
			i = n
		}
		if i >= len(b) {
			return 0, fmt.Errorf("unterminated bencode")
		}
		return i + 1, nil
	default:
		_, _, n, e := scanString(b, i)
		return n, e
	}
}
