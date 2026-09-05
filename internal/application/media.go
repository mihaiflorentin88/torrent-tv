package application

import (
	"context"
	"fmt"
	"os"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// MediaInfo probes the original selected file. Completed media is cached by
// file identity; progressive media is intentionally retried because its
// headers and final index may become readable as pieces arrive.
func (s *Service) MediaInfo(ctx context.Context, sourceID string) (domain.MediaInfo, bool, error) {
	if s.mediaProbe == nil {
		return domain.MediaInfo{}, false, fmt.Errorf("media probe is not configured")
	}
	download, err := s.Acquire(ctx, sourceID)
	if err != nil {
		return domain.MediaInfo{}, false, err
	}
	defer s.Release(download.ID)
	complete := completedLocalFile(download)
	path := download.AbsolutePath
	identity := ""
	if complete {
		if stat, statErr := os.Stat(path); statErr == nil {
			identity = fmt.Sprintf("%s:%d:%d", path, stat.Size(), stat.ModTime().UnixNano())
			s.mediaInfoMu.Lock()
			cached, ok := s.mediaInfoCache[sourceID]
			s.mediaInfoMu.Unlock()
			if ok && cached.identity == identity {
				return cached.info, true, nil
			}
		}
	} else {
		count := s.settings.Get().InitialBufferBytes
		if count < 1 || count > download.SizeBytes {
			count = download.SizeBytes
		}
		path, err = s.ReadableRangePath(ctx, download, 0, count)
		if err != nil {
			return domain.MediaInfo{}, false, fmt.Errorf("media details are not readable yet: %w", err)
		}
	}
	if err = s.ValidateSourcePath(download); err != nil {
		return domain.MediaInfo{}, complete, err
	}
	info, err := s.mediaProbe.ProbeMedia(ctx, path)
	if err != nil {
		if !complete {
			return domain.MediaInfo{}, false, fmt.Errorf("media details are not readable yet: %w", err)
		}
		return domain.MediaInfo{}, true, err
	}
	if complete && identity != "" {
		s.mediaInfoMu.Lock()
		s.mediaInfoCache[sourceID] = cachedMediaInfo{identity: identity, info: info}
		s.mediaInfoMu.Unlock()
	}
	return info, complete, nil
}
