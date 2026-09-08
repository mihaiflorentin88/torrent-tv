package qbittorrent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// hashGate coordinates concurrent operations on a single info hash so
// normal Add and ResolveMagnet do not collide, and multiple resolvers for
// the same hash do not leak or double-drop transient torrents.
type hashGate struct {
	mu   sync.Mutex
	refs int
}

func (c *Client) acquireHashGate(hash string) *hashGate {
	c.gateMu.Lock()
	if c.hashGates == nil {
		c.hashGates = make(map[string]*hashGate)
	}
	gate := c.hashGates[hash]
	if gate == nil {
		gate = &hashGate{}
		c.hashGates[hash] = gate
	}
	gate.refs++
	c.gateMu.Unlock()
	gate.mu.Lock()
	return gate
}

func (c *Client) releaseHashGate(hash string, gate *hashGate) {
	gate.mu.Unlock()
	c.gateMu.Lock()
	gate.refs--
	if gate.refs <= 0 {
		delete(c.hashGates, hash)
	}
	c.gateMu.Unlock()
}

type qbVersion struct {
	major int
	minor int
	patch int
}

func parseVersion(v string) (qbVersion, bool) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	if v == "" {
		return qbVersion{}, false
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 {
		return qbVersion{}, false
	}
	parsePart := func(s string) (int, bool) {
		var digits strings.Builder
		for _, r := range s {
			if r >= '0' && r <= '9' {
				digits.WriteRune(r)
			} else {
				break
			}
		}
		if digits.Len() == 0 {
			return 0, false
		}
		n, err := strconv.Atoi(digits.String())
		if err != nil {
			return 0, false
		}
		return n, true
	}

	maj, ok := parsePart(parts[0])
	if !ok {
		return qbVersion{}, false
	}
	ver := qbVersion{major: maj}
	if len(parts) > 1 {
		min, ok := parsePart(parts[1])
		if !ok {
			return qbVersion{}, false
		}
		ver.minor = min
	}
	if len(parts) > 2 {
		patch, ok := parsePart(parts[2])
		if !ok {
			return qbVersion{}, false
		}
		ver.patch = patch
	}
	return ver, true
}

func versionSupportsMagnets(v qbVersion) bool {
	if v.major > 4 {
		return true
	}
	if v.major == 4 && v.minor >= 5 {
		return true
	}
	return false
}

func magnetInfoHash(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse magnet uri: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "magnet") {
		return "", errors.New("not a magnet uri")
	}
	xts := u.Query()["xt"]
	for _, xt := range xts {
		if !strings.HasPrefix(strings.ToLower(xt), "urn:btih:") {
			continue
		}
		rawHash := xt[len("urn:btih:"):]
		if len(rawHash) == 40 && isHex(rawHash) {
			return strings.ToLower(rawHash), nil
		}
		if len(rawHash) == 32 {
			decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(rawHash))
			if err == nil && len(decoded) == 20 {
				return hex.EncodeToString(decoded), nil
			}
		}
	}
	return "", errors.New("magnet has no usable v1 info hash")
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

func tagsContain(tagsVal any, target string) bool {
	if tagsVal == nil {
		return false
	}
	switch v := tagsVal.(type) {
	case string:
		for _, part := range strings.Split(v, ",") {
			if strings.TrimSpace(part) == target {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) == target {
				return true
			}
		}
	case []string:
		for _, item := range v {
			if strings.TrimSpace(item) == target {
				return true
			}
		}
	}
	return false
}

func (c *Client) getTorrentsByHash(ctx context.Context, hash string) ([]map[string]any, error) {
	var rows []map[string]any
	err := c.getJSON(ctx, "api/v2/torrents/info?hash="+url.QueryEscape(hash), &rows)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, nil
		}
		return nil, err
	}
	return rows, nil
}

func (c *Client) stopTorrent(ctx context.Context, ver qbVersion, hash string) error {
	path := "pause"
	if ver.major >= 5 {
		path = "stop"
	}
	return c.command(ctx, path, url.Values{"hashes": {hash}})
}

func (c *Client) exportRaw(ctx context.Context, hash string) ([]byte, int, error) {
	r, err := c.do(ctx, http.MethodGet, "api/v2/torrents/export?hash="+url.QueryEscape(hash), nil, "")
	if err != nil {
		return nil, 0, err
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusConflict {
		return nil, http.StatusConflict, nil
	}
	if r.StatusCode/100 != 2 {
		return nil, r.StatusCode, nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20+1))
	if err != nil {
		return nil, r.StatusCode, err
	}
	return raw, r.StatusCode, nil
}

func (c *Client) exportAndVerify(ctx context.Context, hash string) ([]byte, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		raw, status, err := c.exportRaw(ctx, hash)
		if err != nil {
			return nil, err
		}
		if status == http.StatusConflict {
			// Metadata not ready yet: transient in this bounded stage
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("export torrent %s returned HTTP %d", hash, status)
		}

		if len(raw) == 0 || len(raw) >= 16<<20 {
			return nil, errors.New("resolved metadata is empty or too large")
		}
		h, err := infoHash(raw)
		if err != nil {
			return nil, fmt.Errorf("verify resolved metadata: %w", err)
		}
		if h != hash {
			return nil, fmt.Errorf("resolved metadata hash %s does not match magnet info hash %s", h, hash)
		}
		return raw, nil
	}
}

func (c *Client) exportPreexisting(ctx context.Context, hash string) ([]byte, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		files, err := c.Files(ctx, hash)
		if err == nil && len(files) > 0 {
			return c.exportAndVerify(ctx, hash)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// ResolveMagnet fetches metadata for a magnet URI via qBittorrent 4.5.0+ and
// returns verified bencoded metainfo bytes. Older daemon versions fail with
// domain.ErrMagnetUnsupported before initiating any add request. Preexisting
// torrents are exported without mutation or deletion. Resolver-owned torrents
// are cleaned up completely upon completion, error, or cancellation.
func (c *Client) ResolveMagnet(ctx context.Context, uri string, downloadRoot string, discovery domain.MagnetDiscovery) (meta []byte, finalErr error) {
	if discovery != domain.MagnetDiscoveryPublic {
		return nil, domain.ErrMagnetDiscoveryDenied
	}

	hash, err := magnetInfoHash(uri)
	if err != nil {
		return nil, fmt.Errorf("parse magnet info hash: %w", err)
	}

	verStr, err := c.Test(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: unable to read daemon version: %v", domain.ErrMagnetUnsupported, err)
	}
	ver, ok := parseVersion(verStr)
	if !ok || !versionSupportsMagnets(ver) {
		return nil, fmt.Errorf("%w: daemon reports %q", domain.ErrMagnetUnsupported, verStr)
	}

	gate := c.acquireHashGate(hash)
	defer c.releaseHashGate(hash, gate)

	existingRows, err := c.getTorrentsByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("preflight torrent info: %w", err)
	}
	if len(existingRows) > 0 {
		return c.exportPreexisting(ctx, hash)
	}

	tag := fmt.Sprintf("torrent-tv-resolve-%s", randomHex(8))
	relDir := fmt.Sprintf("resolve-%s", randomHex(8))
	resolverPath := filepath.Join(downloadRoot, ".metadata", relDir)
	if err := os.MkdirAll(resolverPath, 0o755); err != nil {
		return nil, fmt.Errorf("create resolver temp dir: %w", err)
	}

	form := url.Values{
		"urls":          {uri},
		"savepath":      {resolverPath},
		"category":      {"torrent-tv"},
		"tags":          {tag},
		"stopCondition": {"MetadataReceived"},
	}
	r, err := c.do(ctx, http.MethodPost, "api/v2/torrents/add", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		_ = os.RemoveAll(resolverPath)
		return nil, fmt.Errorf("add magnet: %w", err)
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
	_ = r.Body.Close()
	accepted := addAccepted(r.StatusCode, bodyBytes)

	rows, _ := c.getTorrentsByHash(ctx, hash)
	if len(rows) == 0 {
		if !accepted {
			_ = os.RemoveAll(resolverPath)
			return nil, fmt.Errorf("qBittorrent rejected magnet: HTTP %d (%s)", r.StatusCode, strings.TrimSpace(string(bodyBytes)))
		}
		for retry := 0; retry < 5 && len(rows) == 0; retry++ {
			select {
			case <-ctx.Done():
				_ = os.RemoveAll(resolverPath)
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			rows, _ = c.getTorrentsByHash(ctx, hash)
		}
		if len(rows) == 0 {
			_ = os.RemoveAll(resolverPath)
			return nil, fmt.Errorf("torrent %s not found after add", hash)
		}
	}

	firstRow := rows[0]
	isOwned := tagsContain(firstRow["tags"], tag) || firstRow["save_path"] == resolverPath
	if !isOwned {
		// Found foreign torrent: do not adopt or delete, export preexisting
		_ = os.RemoveAll(resolverPath)
		return c.exportPreexisting(ctx, hash)
	}

	cleanup := func() error {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		var errs []error
		if err := c.Remove(cleanupCtx, hash, true); err != nil {
			errs = append(errs, fmt.Errorf("remove owned torrent %s: %w", hash, err))
		}

		removalTicker := time.NewTicker(50 * time.Millisecond)
		defer removalTicker.Stop()
	waitRemoval:
		for {
			tRows, err := c.getTorrentsByHash(cleanupCtx, hash)
			if err == nil && len(tRows) == 0 {
				break waitRemoval
			}
			select {
			case <-cleanupCtx.Done():
				errs = append(errs, fmt.Errorf("timed out waiting for removal of %s: %w", hash, cleanupCtx.Err()))
				break waitRemoval
			case <-removalTicker.C:
			}
		}

		if err := os.RemoveAll(resolverPath); err != nil {
			errs = append(errs, fmt.Errorf("remove resolver directory %s: %w", resolverPath, err))
		}

		return errors.Join(errs...)
	}

	defer func() {
		if isOwned {
			cErr := cleanup()
			if cErr != nil {
				if finalErr != nil {
					finalErr = errors.Join(finalErr, cErr)
				} else {
					finalErr = fmt.Errorf("resolved metadata for %s but cleanup failed: %w", hash, cErr)
					meta = nil
				}
			}
		}
	}()

	pollTicker := time.NewTicker(200 * time.Millisecond)
	defer pollTicker.Stop()

	for {
		files, err := c.Files(ctx, hash)
		if err == nil && len(files) > 0 {
			if stopErr := c.stopTorrent(ctx, ver, hash); stopErr != nil {
				return nil, fmt.Errorf("stop resolver torrent: %w", stopErr)
			}

			metaBytes, expErr := c.exportAndVerify(ctx, hash)
			if expErr != nil {
				return nil, expErr
			}
			mi, err := metainfo.Load(bytes.NewReader(metaBytes))
			if err != nil {
				return nil, fmt.Errorf("decode resolved metainfo: %w", err)
			}
			info, err := mi.UnmarshalInfo()
			if err != nil {
				return nil, fmt.Errorf("decode resolved metainfo info: %w", err)
			}
			if info.Private != nil && *info.Private {
				return nil, domain.ErrPrivateMagnet
			}
			return metaBytes, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pollTicker.C:
		}
	}
}
