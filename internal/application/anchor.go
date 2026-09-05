package application

import (
	"context"
	"fmt"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// AudioHeaderBytes mirrors the web decoder's container-head length: the
// decoder always fetches the first bytes of the file whole, so audio spans
// are measured on windows that begin at or after this offset. Windows that
// start inside the head are normalized to zero for measurement.
const AudioHeaderBytes int64 = 2 << 20

// maxAudioWindowBytes mirrors the decoder's largest fetch window.
const maxAudioWindowBytes int64 = 16 << 20

// InvalidAudioWindowError marks a client-supplied window that can never be
// measured, so the HTTP layer maps it to 422 instead of a retry.
type InvalidAudioWindowError struct {
	Start  int64
	Length int64
}

func (e *InvalidAudioWindowError) Error() string {
	return fmt.Sprintf("invalid audio window: start %d length %d", e.Start, e.Length)
}

// AudioSpanProbe measures decoded-audio content spans of fetch windows on the
// original file, expressed in media timestamps (ADR-0002). The probe needs
// the decoder's head length to build the exact artifact the decoder
// consumes. Optional capability: media probe adapters implement it when
// their tooling supports packet positioning.
type AudioSpanProbe interface {
	AudioSpan(ctx context.Context, path string, startByte, lengthBytes int64, streamIndex int, headerByteLength int64) (domain.AudioSpan, error)
}

// AudioSpan measures the audio content of one fetch window of a Source so
// the client can anchor decoded audio by measured PTS instead of estimates
// (ADR-0002). Invalid windows are rejected before any lease is taken;
// partial Sources return retryable=true because their windows may become
// readable as pieces arrive.
func (s *Service) AudioSpan(ctx context.Context, sourceID string, startByte, lengthBytes int64, streamIndex int) (domain.AudioSpan, bool, error) {
	if s.mediaProbe == nil {
		return domain.AudioSpan{}, false, fmt.Errorf("media probe is not configured")
	}
	spanProbe, ok := s.mediaProbe.(AudioSpanProbe)
	if !ok {
		return domain.AudioSpan{}, false, fmt.Errorf("audio span probing is unavailable")
	}
	if startByte < 0 || streamIndex < 0 || lengthBytes < 1 || lengthBytes > maxAudioWindowBytes {
		return domain.AudioSpan{}, false, &InvalidAudioWindowError{Start: startByte, Length: lengthBytes}
	}
	if startByte < AudioHeaderBytes {
		// Same normalization as the decoder: the container head is always
		// fetched whole, so a window inside it measures from zero.
		startByte = 0
	}
	download, err := s.repo.GetDownload(ctx, sourceID)
	if err != nil {
		return domain.AudioSpan{}, false, err
	}
	if !completedLocalFile(download) {
		return domain.AudioSpan{}, true, fmt.Errorf("audio span is not readable yet: source is still downloading")
	}
	// The probe is read-only and only ever reaches here for completed
	// sources, so it takes no lease: leasing turned every probe into two
	// database writes plus a qBittorrent round-trip, and under concurrent
	// playback those writes contended with lease/reconcile traffic until
	// probes failed with database-is-locked.
	if err := s.ValidateSourcePath(download); err != nil {
		return domain.AudioSpan{}, false, err
	}
	span, err := spanProbe.AudioSpan(ctx, download.AbsolutePath, startByte, lengthBytes, streamIndex, AudioHeaderBytes)
	return span, false, err
}
