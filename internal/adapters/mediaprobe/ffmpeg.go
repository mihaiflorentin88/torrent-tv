package mediaprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

type Adapter struct {
	settings *config.Store
	slots    chan struct{}
}

func New(settings *config.Store) *Adapter {
	return &Adapter{settings: settings, slots: make(chan struct{}, 1)}
}

func (a *Adapter) ProbeMedia(ctx context.Context, path string) (domain.MediaInfo, error) {
	if err := a.acquire(ctx); err != nil {
		return domain.MediaInfo{}, err
	}
	defer a.release()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, a.settings.Get().FFprobePath,
		"-v", "error", "-select_streams", "a", "-show_streams", "-show_format", "-of", "json", path,
	).Output()
	if err != nil {
		return domain.MediaInfo{}, fmt.Errorf("ffprobe media information: %w", err)
	}
	return decodeMediaInfo(out)
}

func decodeMediaInfo(out []byte) (domain.MediaInfo, error) {
	var payload struct {
		Streams []struct {
			Index       int               `json:"index"`
			Codec       string            `json:"codec_name"`
			Channels    int               `json:"channels"`
			Duration    string            `json:"duration"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return domain.MediaInfo{}, fmt.Errorf("decode ffprobe media information: %w", err)
	}
	durationSeconds, _ := strconv.ParseFloat(payload.Format.Duration, 64)
	tracks := make([]domain.MediaAudioTrack, 0, len(payload.Streams))
	for _, stream := range payload.Streams {
		if durationSeconds <= 0 {
			if value, err := strconv.ParseFloat(stream.Duration, 64); err == nil && value > durationSeconds {
				durationSeconds = value
			}
		}
		tracks = append(tracks, domain.MediaAudioTrack{Index: stream.Index, Language: stream.Tags["language"], Title: stream.Tags["title"], Codec: stream.Codec, Channels: stream.Channels, Default: stream.Disposition["default"] == 1})
	}
	if durationSeconds <= 0 {
		return domain.MediaInfo{}, fmt.Errorf("ffprobe did not report a stable media duration")
	}
	if len(tracks) == 0 {
		return domain.MediaInfo{}, fmt.Errorf("ffprobe did not find an audio stream")
	}
	return domain.MediaInfo{DurationMS: int64(durationSeconds*1000 + .5), AudioTracks: tracks, ProbedAt: time.Now().UTC()}, nil
}

func (a *Adapter) ProbeSubtitles(ctx context.Context, path string) ([]domain.MediaSubtitleTrack, error) {
	if err := a.acquire(ctx); err != nil {
		return nil, err
	}
	defer a.release()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, a.settings.Get().FFprobePath, "-v", "error", "-select_streams", "s", "-show_streams", "-of", "json", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe subtitle streams: %w", err)
	}
	var payload struct {
		Streams []struct {
			Index       int               `json:"index"`
			Codec       string            `json:"codec_name"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
	}
	if err = json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("decode ffprobe output: %w", err)
	}
	now := time.Now().UTC()
	tracks := make([]domain.MediaSubtitleTrack, 0, len(payload.Streams))
	for _, stream := range payload.Streams {
		tracks = append(tracks, domain.MediaSubtitleTrack{Index: stream.Index, Language: stream.Tags["language"], Title: stream.Tags["title"], Codec: stream.Codec, Default: stream.Disposition["default"] == 1, Forced: stream.Disposition["forced"] == 1, HearingImpaired: stream.Disposition["hearing_impaired"] == 1, ProbedAt: now})
	}
	return tracks, nil
}

func (a *Adapter) ExtractSubtitle(ctx context.Context, path string, index int, target string) error {
	if err := a.acquire(ctx); err != nil {
		return err
	}
	defer a.release()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, a.settings.Get().FFmpegPath, "-v", "error", "-nostdin", "-i", path, "-map", "0:"+strconv.Itoa(index), "-f", "webvtt", "-y", target).CombinedOutput()
	if err != nil {
		if len(output) > 2048 {
			output = output[len(output)-2048:]
		}
		return fmt.Errorf("extract embedded subtitle: %w: %s", err, string(output))
	}
	return nil
}
func (a *Adapter) acquire(ctx context.Context) error {
	select {
	case a.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *Adapter) release() { <-a.slots }
