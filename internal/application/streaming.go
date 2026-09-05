package application

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

type playbackStrategy interface {
	waitReadablePath(context.Context, domain.Download, int64, int64) (string, error)
}

type (
	localFileStrategy          struct{ service *Service }
	progressiveTorrentStrategy struct{ service *Service }
)

func (s *Service) playbackStrategy(d domain.Download) playbackStrategy {
	if completedLocalFile(d) {
		return localFileStrategy{service: s}
	}
	return progressiveTorrentStrategy{service: s}
}

func completedLocalFile(d domain.Download) bool {
	// Torrent state is not sufficient here: after a user selects previously
	// skipped files, qBittorrent can briefly retain an upload state while those
	// newly selected files still have zero progress. The selected file's own
	// reconciled progress is authoritative.
	if d.Progress < 1 || d.AbsolutePath == "" || d.SizeBytes <= 0 {
		return false
	}
	info, err := os.Stat(d.AbsolutePath)
	return err == nil && !info.IsDir() && info.Size() >= d.SizeBytes
}

func (strategy localFileStrategy) waitReadablePath(_ context.Context, d domain.Download, start, count int64) (string, error) {
	if err := strategy.service.ValidateSourcePath(d); err != nil {
		return "", err
	}
	if count <= 0 || start < 0 || start+count > d.SizeBytes {
		return "", fmt.Errorf("requested media range is outside the selected file")
	}
	info, err := os.Stat(d.AbsolutePath)
	if err != nil {
		return "", fmt.Errorf("completed media file is unavailable: %w", err)
	}
	if info.IsDir() || info.Size() < start+count {
		return "", fmt.Errorf("completed media file is shorter than the requested range")
	}
	return d.AbsolutePath, nil
}

func (strategy progressiveTorrentStrategy) waitReadablePath(ctx context.Context, d domain.Download, start, count int64) (string, error) {
	if count <= 0 || start < 0 || start+count > d.SizeBytes {
		return "", fmt.Errorf("requested media range is outside the selected file")
	}
	hash, ok := strategy.service.route(d.EngineID)
	if !ok {
		return "", fmt.Errorf("unsupported engine route")
	}
	status, err := strategy.service.engine.Status(ctx, hash)
	if err != nil {
		return "", err
	}
	path, err := safeQBContentPath(strategy.service.settings.Get().DownloadRoot, status, d.FilePath)
	if err != nil {
		return "", err
	}
	if err = strategy.service.engine.PrepareRange(ctx, hash, d.FileIndex, d.FileOffset+start, count); err != nil {
		return "", err
	}
	if err = strategy.service.WaitRange(ctx, d, start, count); err != nil {
		return "", err
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		f, openErr := os.Open(path)
		if openErr == nil {
			var one [1]byte
			_, openErr = f.ReadAt(one[:], start+count-1)
			_ = f.Close()
		}
		if openErr == nil {
			return path, nil
		}
		// Completion can move the content between chunks. Refresh qBittorrent's
		// content path before retrying instead of holding onto a stale temp path.
		if refreshed, statusErr := strategy.service.engine.Status(ctx, hash); statusErr == nil {
			if candidate, pathErr := safeQBContentPath(strategy.service.settings.Get().DownloadRoot, refreshed, d.FilePath); pathErr == nil {
				path = candidate
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("downloaded pieces are not readable from storage: %w", openErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ReadableRangePath selects the completed-file or progressive-torrent
// strategy and returns the current safe path for the requested bytes.
func (s *Service) ReadableRangePath(ctx context.Context, d domain.Download, start, count int64) (string, error) {
	return s.playbackStrategy(d).waitReadablePath(ctx, d, start, count)
}

func safeQBContentPath(root string, status domain.DownloadStatus, name string) (string, error) {
	if status.Progress < 1 && status.TempPathEnabled && status.TempPath != "" {
		path, err := safeQBPath(root, status.TempPath, name)
		if err != nil {
			return "", fmt.Errorf("qBittorrent temporary content path: %w", err)
		}
		return path, nil
	}
	if status.ContentPath == "" {
		return safeQBPath(root, status.SavePath, name)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	contentAbs, err := filepath.Abs(status.ContentPath)
	if err != nil {
		return "", err
	}
	if err = requireContainedPath(rootAbs, contentAbs); err != nil {
		return "", fmt.Errorf("qBittorrent content path is outside the configured download root")
	}
	cleanName := filepath.Clean(filepath.FromSlash(name))
	if cleanName == "." || filepath.IsAbs(cleanName) || cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("torrent path escapes download root")
	}
	parts := strings.Split(cleanName, string(filepath.Separator))
	var candidate string
	if len(parts) == 1 && filepath.Base(contentAbs) == parts[0] {
		candidate = contentAbs
	} else if len(parts) > 1 && filepath.Base(contentAbs) == parts[0] {
		candidate = filepath.Join(filepath.Dir(contentAbs), cleanName)
	} else {
		candidate = filepath.Join(contentAbs, cleanName)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	if err = requireContainedPath(rootAbs, candidate); err != nil {
		return "", fmt.Errorf("torrent path escapes download root")
	}
	return candidate, nil
}

func requireContainedPath(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path is outside root")
	}
	return nil
}
