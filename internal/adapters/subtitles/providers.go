package subtitles

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/outbound"
)

const maxResponse = 12 << 20

type SubDL struct {
	settings *config.Store
	client   *http.Client
	mu       sync.Mutex
	cache    map[string]cachedSearch
}

type cachedSearch struct {
	items     []domain.SubtitleCandidate
	expiresAt time.Time
}

func NewSubDL(settings *config.Store) *SubDL {
	return &SubDL{settings: settings, client: &http.Client{Timeout: 45 * time.Second}, cache: map[string]cachedSearch{}}
}
func (*SubDL) Name() string { return "subdl" }

func (p *SubDL) Test(ctx context.Context) (string, error) {
	v := p.settings.Get()
	if v.SubDLAPIKey == "" {
		return "", fmt.Errorf("SubDL API key is not configured")
	}
	body, _, err := p.get(ctx, "/api/v2/me", nil)
	if err != nil {
		return "", err
	}
	var result map[string]any
	if json.Unmarshal(body, &result) != nil {
		return "", fmt.Errorf("decode SubDL account response")
	}
	if usage, ok := result["usage"].(map[string]any); ok {
		if downloads, ok := usage["downloads"].(map[string]any); ok {
			remaining := stringValue(downloads["remaining"])
			if remaining != "" {
				return "Connected to SubDL; " + remaining + " downloads remaining today", nil
			}
		}
	}
	return "Connected to SubDL", nil
}

type candidateID struct {
	NID    string `json:"n"`
	FileID string `json:"f,omitempty"`
	Path   string `json:"p,omitempty"`
	Name   string `json:"m,omitempty"`
	Format string `json:"x,omitempty"`
	// Language keeps the canonical candidate language so a later Download
	// can persist it without re-deriving it from the file name.
	Language string `json:"l,omitempty"`
}

func encodeCandidate(v candidateID) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCandidate(raw string) (candidateID, error) {
	var v candidateID
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || json.Unmarshal(b, &v) != nil {
		return v, fmt.Errorf("invalid SubDL candidate id")
	}
	if !safeID(v.NID) || v.FileID != "" && !safeID(v.FileID) || v.Path != "" && !safeDownloadPath(v.Path) {
		return v, fmt.Errorf("invalid SubDL candidate id")
	}
	return v, nil
}

func safeID(v string) bool {
	if v == "" || len(v) > 160 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func safeDownloadPath(v string) bool {
	return strings.HasPrefix(v, "/subtitle/") && !strings.Contains(v, "..") && !strings.ContainsAny(v, "?#\\")
}

func (p *SubDL) Search(ctx context.Context, q application.SubtitleQuery) ([]domain.SubtitleCandidate, error) {
	v := p.settings.Get()
	if v.SubDLAPIKey == "" {
		return []domain.SubtitleCandidate{}, nil
	}
	values := url.Values{"languages": {subDLLanguages(q.Language, q.FallbackLanguage)}, "unpack": {"1"}, "client": {"custom_integration"}}
	if q.Release.IMDbID != "" {
		values.Set("imdb_id", q.Release.IMDbID)
	} else {
		values.Set("file_name", filepath.Base(q.MediaPath))
	}
	parsed := domain.ParseRelease(q.Release)
	if parsed.Kind == domain.MediaSeries {
		values.Set("type", "tv")
		if parsed.SeasonStart > 0 {
			values.Set("season", strconv.Itoa(parsed.SeasonStart))
		}
		if parsed.EpisodeStart > 0 {
			values.Set("episode", strconv.Itoa(parsed.EpisodeStart))
		}
	} else {
		values.Set("type", "movie")
	}
	cacheKey := v.SubDLURL + "\x00" + v.SubDLAPIKey + "\x00" + values.Encode()
	p.mu.Lock()
	if cached, ok := p.cache[cacheKey]; ok && cached.expiresAt.After(time.Now()) {
		items := append([]domain.SubtitleCandidate(nil), cached.items...)
		p.mu.Unlock()
		return items, nil
	}
	p.mu.Unlock()
	body, _, err := p.get(ctx, "/api/v2/subtitles/search", values)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode SubDL search: %w", err)
	}
	if ok, exists := result["status"].(bool); exists && !ok {
		return nil, fmt.Errorf("SubDL search failed: %s", subDLError(result))
	}
	raw, _ := result["subtitles"].([]any)
	items := []domain.SubtitleCandidate{}
	for _, value := range raw {
		row, ok := value.(map[string]any)
		if !ok {
			continue
		}
		parent := first(row, "n_id", "nid", "id")
		release := first(row, "release_name", "release", "name")
		lang := strings.ToLower(first(row, "language", "lang"))
		uploader := first(row, "author", "uploader")
		hi := boolValue(row["hi"])
		files, _ := row["unpack_files"].([]any)
		matchedFile := false
		for _, fv := range files {
			file, ok := fv.(map[string]any)
			if !ok {
				continue
			}
			season := intValue(file["season"])
			episode := intValue(file["episode"])
			if parsed.SeasonStart > 0 && season > 0 && season != parsed.SeasonStart {
				continue
			}
			if parsed.EpisodeStart > 0 && episode > 0 && episode != parsed.EpisodeStart {
				continue
			}
			name := first(file, "name", "release_name")
			format := strings.TrimPrefix(strings.ToLower(first(file, "format")), ".")
			if format == "" {
				format = strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
			}
			path, pathParent, pathFileID, validPath := unpackFileIdentity(first(file, "url"))
			if parent == "" {
				parent = pathParent
			}
			fileID := first(file, "file_n_id", "id")
			if fileID == "" {
				fileID = pathFileID
			}
			if !validPath || parent != pathParent || fileID != pathFileID || !safeID(parent) || !safeID(fileID) {
				continue
			}
			language := domain.NormalizeLanguage(first(file, "language"))
			if language == "" {
				language = domain.NormalizeLanguage(lang)
			}
			items = append(items, domain.SubtitleCandidate{ID: encodeCandidate(candidateID{NID: parent, FileID: fileID, Path: path, Name: name, Format: format, Language: language}), ProviderLabel: "SubDL", Language: language, Title: name, FileName: name, ReleaseName: release, Format: format, Uploader: uploader, HearingImpaired: hi || boolValue(file["hi"]), Score: similarity(filepath.Base(q.MediaPath), name+" "+release)})
			matchedFile = true
		}
		if matchedFile || !safeID(parent) {
			continue
		}
		name := first(row, "release_name", "name")
		format := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
		language := domain.NormalizeLanguage(lang)
		items = append(items, domain.SubtitleCandidate{ID: encodeCandidate(candidateID{NID: parent, Name: name, Format: format, Language: language}), ProviderLabel: "SubDL", Language: language, Title: name, FileName: name, ReleaseName: release, Format: format, Uploader: uploader, HearingImpaired: hi, Score: similarity(filepath.Base(q.MediaPath), name+" "+release)})
	}
	if len(items) > 30 {
		items = items[:30]
	}
	p.mu.Lock()
	p.cache[cacheKey] = cachedSearch{items: append([]domain.SubtitleCandidate(nil), items...), expiresAt: time.Now().Add(time.Hour)}
	p.mu.Unlock()
	return items, nil
}

func (p *SubDL) Download(ctx context.Context, id string) (application.SubtitleDownload, error) {
	candidate, err := decodeCandidate(id)
	if err != nil {
		return application.SubtitleDownload{}, err
	}
	path := "/api/v2/subtitles/" + url.PathEscape(candidate.NID) + "/download"
	values := url.Values{"format": {"file"}}
	if candidate.Path != "" {
		path = "https://dl.subdl.com" + candidate.Path
		values = nil
	}
	body, header, err := p.get(ctx, path, values)
	if err != nil {
		return application.SubtitleDownload{}, err
	}
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		var result map[string]any
		if json.Unmarshal(body, &result) == nil {
			link := first(result, "url", "link", "download_url")
			if link != "" {
				parsed, e := url.Parse(link)
				if e != nil || !strings.EqualFold(parsed.Host, "dl.subdl.com") || !safeDownloadPath(parsed.Path) {
					return application.SubtitleDownload{}, fmt.Errorf("SubDL returned an invalid download URL")
				}
				body, header, err = p.get(ctx, link, nil)
				if err != nil {
					return application.SubtitleDownload{}, err
				}
			}
		}
	}
	if zipSignature(body) || rarSignature(body) {
		return application.SubtitleDownload{}, fmt.Errorf("SubDL returned an archive instead of a direct subtitle file")
	}
	name := candidate.Name
	if disposition := header.Get("Content-Disposition"); disposition != "" {
		if _, params, e := mime.ParseMediaType(disposition); e == nil && filepath.Base(params["filename"]) == params["filename"] {
			name = params["filename"]
		}
	}
	format := "." + strings.TrimPrefix(strings.ToLower(candidate.Format), ".")
	if candidate.Format == "" {
		format = strings.ToLower(filepath.Ext(name))
	}
	detected := detectSubtitleFormat(body)
	if detected == "" {
		return application.SubtitleDownload{}, fmt.Errorf("SubDL returned an unsupported or binary subtitle")
	}
	if format != ".srt" && format != ".vtt" && format != ".ass" && format != ".ssa" && format != ".smi" {
		format = detected
	}
	if name == "" {
		name = "subdl-" + candidate.NID + detected
	}
	return application.SubtitleDownload{Data: body, Format: format, Name: name, Language: candidate.Language}, nil
}

func (p *SubDL) get(ctx context.Context, path string, values url.Values) ([]byte, http.Header, error) {
	v := p.settings.Get()
	address := strings.TrimRight(v.SubDLURL, "/") + path
	if strings.HasPrefix(path, "https://") {
		address = path
	}
	if values != nil && len(values) > 0 {
		address += "?" + values.Encode()
	}
	response, err := outbound.Do(ctx, p.client, func() (*http.Request, error) {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if e == nil {
			req.Header.Set("Authorization", "Bearer "+v.SubDLAPIKey)
			req.Header.Set("X-API-Key", v.SubDLAPIKey)
			req.Header.Set("Accept", "application/json, text/plain, */*")
			req.Header.Set("User-Agent", "TorrentTV/0.3")
		}
		return req, e
	}, outbound.Policy{Provider: "SubDL", Attempts: 3, MaxInlineDelay: 15 * time.Second})
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxResponse {
		return nil, nil, fmt.Errorf("SubDL response is too large")
	}
	if response.StatusCode/100 != 2 {
		message := strings.ReplaceAll(strings.TrimSpace(string(body)), v.SubDLAPIKey, "[redacted]")
		if len(message) > 400 {
			message = message[:400] + "…"
		}
		return nil, nil, fmt.Errorf("SubDL returned %s: %s", response.Status, message)
	}
	return body, response.Header.Clone(), nil
}

func unpackFileIdentity(raw string) (path, parent, fileID string, ok bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Path == "" || parsed.Fragment != "" {
		return "", "", "", false
	}
	if parsed.IsAbs() && (!strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Host, "dl.subdl.com")) {
		return "", "", "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "subtitle" || !safeID(parts[1]) || !safeID(parts[2]) || strings.Contains(parsed.Path, "..") {
		return "", "", "", false
	}
	path = "/subtitle/" + parts[1] + "/" + parts[2]
	return path, parts[1], parts[2], true
}

func subDLError(result map[string]any) string {
	if value, ok := result["error"].(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	if value, ok := result["error"].(map[string]any); ok {
		if message := first(value, "message", "code"); message != "" {
			return message
		}
	}
	return "provider returned an unsuccessful response"
}

func subDLLanguages(primary, fallback string) string {
	first, second := domain.NormalizeLanguage(primary), domain.NormalizeLanguage(fallback)
	if first == "" {
		return second
	}
	if second == "" || second == first {
		return first
	}
	return first + "," + second
}

func first(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := stringValue(m[key]); v != "" {
			return v
		}
	}
	return ""
}
func intValue(v any) int   { n, _ := strconv.Atoi(stringValue(v)); return n }
func boolValue(v any) bool { s := strings.ToLower(stringValue(v)); return s == "1" || s == "true" }
func zipSignature(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte("PK\x05\x06")) || bytes.HasPrefix(data, []byte("PK\x07\x08"))
}
func rarSignature(data []byte) bool { return bytes.HasPrefix(data, []byte("Rar!\x1a\x07")) }
func detectSubtitleFormat(data []byte) string {
	text := strings.TrimSpace(strings.ToLower(string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))))
	switch {
	case strings.HasPrefix(text, "webvtt"):
		return ".vtt"
	case strings.Contains(text, "[script info]") && strings.Contains(text, "dialogue:"):
		return ".ass"
	case strings.Contains(text, "<sami"):
		return ".smi"
	case strings.Contains(text, "-->"):
		return ".srt"
	}
	return ""
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

func similarity(left, right string) float64 {
	normalize := func(v string) []string {
		return strings.Fields(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(strings.ToLower(v)))
	}
	words := normalize(left)
	haystack := " " + strings.Join(normalize(right), " ") + " "
	score := 0.0
	for _, word := range words {
		if len(word) > 1 && strings.Contains(haystack, " "+word+" ") {
			score += 10
		}
	}
	return score
}
