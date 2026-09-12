package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The compatibility stream relies on -copypriorss (FFmpeg 6.1+) and modern
// E-AC-3 probing, so anything older than 6.1 is reported as too old even when
// it runs.
const (
	mediaToolMinMajor = 6
	mediaToolMinMinor = 1
)

var mediaToolVersion = regexp.MustCompile(`(?:ffmpeg|ffprobe) version (\d+)\.(\d+)`)

// TestTranscoder verifies a configured ffmpeg or ffprobe binary end to end:
// the configured path exists, the reported version meets the 6.1 floor, and a
// small real encode (ffmpeg) or media parse (ffprobe) round-trips through the
// same pipeline kinds the app uses.
func (s *Service) TestTranscoder(ctx context.Context, kind string) (string, error) {
	var label, path string
	switch kind {
	case "ffmpeg":
		label, path = "ffmpeg", s.settings.Get().FFmpegPath
	case "ffprobe":
		label, path = "ffprobe", s.settings.Get().FFprobePath
	default:
		return "", fmt.Errorf("unknown media tool")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s path is not configured", label)
	}
	if strings.ContainsRune(path, os.PathSeparator) {
		if stat, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%s was not found at the configured path: %w", path, err)
		} else if stat.IsDir() {
			return "", fmt.Errorf("%s is a directory, not the %s binary", path, label)
		}
	} else if _, err := exec.LookPath(path); err != nil {
		return "", fmt.Errorf("%s was not found on PATH: %w", path, err)
	}
	versionOutput, err := runMediaTool(ctx, path, 10*time.Second, "-version")
	if err != nil {
		return "", fmt.Errorf("%s did not run: %w", path, err)
	}
	match := mediaToolVersion.FindStringSubmatch(versionOutput)
	if match == nil {
		return "", fmt.Errorf("%s ran but its version could not be parsed from: %s", label, firstLine(versionOutput))
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	version := fmt.Sprintf("%s %d.%d", label, major, minor)
	if major < mediaToolMinMajor || (major == mediaToolMinMajor && minor < mediaToolMinMinor) {
		return "", fmt.Errorf("%s %d.%d at %s is too old: version %d.%d or newer is required", label, major, minor, path, mediaToolMinMajor, mediaToolMinMinor)
	}
	var workErr error
	workNote := ""
	if kind == "ffmpeg" {
		workErr = testFFmpegEncode(ctx, path)
		workNote = "test encode ok"
	} else {
		workErr = testFFProbeParse(ctx, path)
		workNote = "test probe ok"
	}
	if workErr != nil {
		return "", fmt.Errorf("%s works but the real-pipeline check failed: %w", version, workErr)
	}
	return fmt.Sprintf("%s at %s: version ok, %s", version, path, workNote), nil
}

func runMediaTool(ctx context.Context, path string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// testFFmpegEncode mirrors the compatibility stream's core pipeline (audio
// decode to AAC in an MP4 pipe) on a synthetic source, so no media library is
// needed and nothing touches the user's downloads.
func testFFmpegEncode(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(
		ctx, path,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.4",
		// The same fragmented-MP4 flags the compatibility stream uses: the
		// plain mp4 muxer refuses non-seekable (pipe) output without them.
		"-c:a", "aac", "-b:a", "64k",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1",
	)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(out.String()))
	}
	if out.Len() < 1024 {
		return fmt.Errorf("the test transcode produced no media data")
	}
	return nil
}

// testFFProbeParse mirrors the media probe's parse path (ffprobe reading a
// stream and reporting format data as JSON) on a synthetic source.
func testFFProbeParse(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(
		ctx, path,
		"-v", "error",
		"-show_entries", "stream=codec_name,codec_type",
		"-of", "json",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.4",
	)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(out.String()))
	}
	var parsed struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		return fmt.Errorf("the test probe returned unparseable output: %w", err)
	}
	if len(parsed.Streams) == 0 || parsed.Streams[0].CodecName == "" {
		return fmt.Errorf("the test probe returned no media streams")
	}
	return nil
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	if line == "" {
		return "(no output)"
	}
	return line
}
