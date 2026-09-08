package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

type bnode struct {
	value []byte
	list  []bnode
	dict  map[string]bnode
}

func (s *Service) releaseMetainfo(ctx context.Context, release domain.TorrentRelease) ([]byte, error) {
	if cached, err := s.repo.GetTorrentManifest(ctx, release.ID); err == nil && len(cached.Metainfo) > 0 {
		return cached.Metainfo, nil
	} else if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	tracker, err := s.trackers.RequireEligible(release.TrackerID)
	if err != nil {
		return nil, err
	}
	providerID := release.ProviderID
	if providerID == "" {
		providerID = release.ID
	}
	acquisition, err := tracker.Acquire(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if err := acquisition.Validate(); err != nil {
		return nil, err
	}
	if acquisition.Magnet == "" {
		return acquisition.Metainfo, nil
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	data, err := s.engines.Default().ResolveMagnet(resolveCtx, acquisition.Magnet, s.settings.Get().DownloadRoot, acquisition.MagnetDiscovery)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("%w: %w", domain.ErrMetadataDeadline, err)
		}
		return nil, err
	}
	return data, nil
}

func parseTorrentManifest(releaseID string, data []byte) (domain.TorrentManifest, error) {
	if len(data) == 0 || len(data) >= 16<<20 {
		return domain.TorrentManifest{}, fmt.Errorf("torrent metadata is empty or too large")
	}
	root, _, err := readBNode(data, 0)
	if err != nil {
		return domain.TorrentManifest{}, err
	}
	info, ok := root.dict["info"]
	if !ok {
		return domain.TorrentManifest{}, fmt.Errorf("torrent metadata has no info dictionary")
	}
	files := []domain.TorrentFile{}
	var offset int64
	if multi, ok := info.dict["files"]; ok {
		for index, row := range multi.list {
			length, e := bint(row.dict["length"])
			if e != nil {
				return domain.TorrentManifest{}, e
			}
			parts := []string{}
			for _, part := range row.dict["path"].list {
				parts = append(parts, string(part.value))
			}
			path, ok := safeTorrentPath(strings.Join(parts, "/"))
			if !ok {
				return domain.TorrentManifest{}, fmt.Errorf("torrent contains an unsafe file path")
			}
			files = append(files, domain.TorrentFile{Index: index, Path: path, SizeBytes: length, Offset: offset, Playable: isPlayable(path)})
			offset += length
		}
	} else {
		length, e := bint(info.dict["length"])
		if e != nil {
			return domain.TorrentManifest{}, e
		}
		name := string(info.dict["name"].value)
		if name == "" {
			name = "content"
		}
		path, ok := safeTorrentPath(name)
		if !ok {
			return domain.TorrentManifest{}, fmt.Errorf("torrent contains an unsafe file path")
		}
		files = append(files, domain.TorrentFile{Index: 0, Path: path, SizeBytes: length, Playable: isPlayable(path)})
	}
	return domain.TorrentManifest{ReleaseID: releaseID, Files: files, Metainfo: data, FetchedAt: time.Now().UTC()}, nil
}

func (s *Service) torrentManifest(ctx context.Context, release domain.TorrentRelease) (domain.TorrentManifest, error) {
	if cached, err := s.repo.GetTorrentManifest(ctx, release.ID); err == nil {
		if len(cached.Files) > 0 || len(cached.Metainfo) > 0 {
			return cached, nil
		}
	} else if err != sql.ErrNoRows {
		return domain.TorrentManifest{}, err
	}
	data, err := s.releaseMetainfo(ctx, release)
	if err != nil {
		return domain.TorrentManifest{}, err
	}
	manifest, err := parseTorrentManifest(release.ID, data)
	if err != nil {
		return domain.TorrentManifest{}, err
	}
	if err = s.repo.SaveTorrentManifest(ctx, manifest); err != nil {
		return domain.TorrentManifest{}, err
	}
	return manifest, nil
}

func readBNode(data []byte, pos int) (bnode, int, error) {
	if pos < 0 || pos >= len(data) {
		return bnode{}, pos, fmt.Errorf("invalid bencoded data")
	}
	switch data[pos] {
	case 'i':
		end := strings.IndexByte(string(data[pos+1:]), 'e')
		if end < 0 {
			return bnode{}, pos, fmt.Errorf("unterminated bencoded integer")
		}
		end += pos + 1
		return bnode{value: data[pos+1 : end]}, end + 1, nil
	case 'l':
		node := bnode{list: []bnode{}}
		pos++
		for pos < len(data) && data[pos] != 'e' {
			child, next, err := readBNode(data, pos)
			if err != nil {
				return bnode{}, pos, err
			}
			node.list = append(node.list, child)
			pos = next
		}
		if pos >= len(data) {
			return bnode{}, pos, fmt.Errorf("unterminated bencoded list")
		}
		return node, pos + 1, nil
	case 'd':
		node := bnode{dict: map[string]bnode{}}
		pos++
		for pos < len(data) && data[pos] != 'e' {
			key, next, err := readBNode(data, pos)
			if err != nil {
				return bnode{}, pos, err
			}
			value, next2, err := readBNode(data, next)
			if err != nil {
				return bnode{}, pos, err
			}
			node.dict[string(key.value)] = value
			pos = next2
		}
		if pos >= len(data) {
			return bnode{}, pos, fmt.Errorf("unterminated bencoded dictionary")
		}
		return node, pos + 1, nil
	}
	colon := pos
	for colon < len(data) && data[colon] != ':' {
		colon++
	}
	if colon >= len(data) {
		return bnode{}, pos, fmt.Errorf("invalid bencoded string")
	}
	length, err := strconv.Atoi(string(data[pos:colon]))
	if err != nil || length < 0 || colon+1+length > len(data) {
		return bnode{}, pos, fmt.Errorf("invalid bencoded string length")
	}
	return bnode{value: data[colon+1 : colon+1+length]}, colon + 1 + length, nil
}

func bint(node bnode) (int64, error) {
	value, err := strconv.ParseInt(string(node.value), 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid torrent file length")
	}
	return value, nil
}

func safeTorrentPath(path string) (string, bool) {
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	if path == "" {
		return "", false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return path, true
}

func isPlayable(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".webm", ".ts", ".mp3", ".m4a", ".flac", ".ogg", ".opus":
		return true
	}
	return false
}

func episodeSource(base domain.CatalogSource, file domain.TorrentFile) (domain.CatalogSource, bool) {
	release := base.Release
	release.Name = file.Path
	release.SizeBytes = file.SizeBytes
	parsed := domain.ParseRelease(release)
	if parsed.EpisodeStart <= 0 {
		return domain.CatalogSource{}, false
	}
	for value, target := range map[string]*string{base.Parsed.Resolution: &parsed.Resolution, base.Parsed.Quality: &parsed.Quality, base.Parsed.VideoCodec: &parsed.VideoCodec, base.Parsed.Audio: &parsed.Audio, base.Parsed.HDR: &parsed.HDR} {
		if *target == "" {
			*target = value
		}
	}
	index := file.Index
	return domain.CatalogSource{Release: base.Release, Parsed: parsed, FileIndex: &index, FilePath: file.Path, FileSizeBytes: file.SizeBytes}, true
}
