package mediaprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
)

// anchorPacket is one audio packet occurrence reported by ffprobe packet
// data: a measured timestamp, its stream, and the byte position ffprobe
// reported for it (input-relative; valid for classifying probe artifacts,
// never a fetch anchor).
type anchorPacket struct {
	StreamIndex int
	PTSMS       int64
	ByteOffset  int64
}

type anchorPacketsJSON struct {
	Packets []struct {
		StreamIndex int     `json:"stream_index"`
		PTSTime     *string `json:"pts_time"`
		Pos         *string `json:"pos"`
	} `json:"packets"`
}

// decodeAnchorPackets parses ffprobe packet JSON. Entries without a readable
// timestamp are dropped; a missing byte position is kept as zero because the
// timestamp is still a valid measurement (the packet is simply unclassifiable
// for window attribution).
func decodeAnchorPackets(out []byte) ([]anchorPacket, error) {
	var parsed anchorPacketsJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("ffprobe anchor packets: %w", err)
	}
	packets := make([]anchorPacket, 0, len(parsed.Packets))
	for _, p := range parsed.Packets {
		if p.PTSTime == nil {
			continue
		}
		seconds, err := strconv.ParseFloat(*p.PTSTime, 64)
		if err != nil {
			continue
		}
		packet := anchorPacket{StreamIndex: p.StreamIndex, PTSMS: int64(math.Round(seconds * 1000))}
		if p.Pos != nil {
			if pos, err := strconv.ParseInt(*p.Pos, 10, 64); err == nil {
				packet.ByteOffset = pos
			}
		}
		packets = append(packets, packet)
	}
	return packets, nil
}

// anchorSpan measures the selected stream's audio content inside the fetch
// window of a concatenated probe artifact (container head plus fetch window).
// Packets belong to the window by byte position in the artifact (pos >=
// boundary, the bytes copied before the window); PTS is never used for
// classification because head and window PTS ranges can overlap across
// discontinuities. First and last PTS follow packet order, not PTS extrema,
// because discontinuous streams can step backwards. The measured span is
// what the decoder will produce from that artifact, expressed in timestamps
// (ADR-0002).
func anchorSpan(packets []anchorPacket, streamIndex int, boundary int64) (firstMS, lastMS int64, ok bool) {
	for _, p := range packets {
		if p.StreamIndex != streamIndex || p.ByteOffset < boundary {
			continue
		}
		if !ok {
			firstMS = p.PTSMS
			ok = true
		}
		lastMS = p.PTSMS
	}
	return firstMS, lastMS, ok
}

// AudioSpan measures the decoded-audio content span of one fetch window of
// path by building the exact artifact the web decoder consumes — container
// head followed by the window bytes — and reading its packet timestamps.
// The measured span is returned in media timestamps; no byte ever masquerades
// as a content position (ADR-0002).
func (a *Adapter) AudioSpan(ctx context.Context, path string, startByte, lengthBytes int64, streamIndex int, headerByteLength int64) (domain.AudioSpan, error) {
	if err := a.acquire(ctx); err != nil {
		return domain.AudioSpan{}, err
	}
	defer a.release()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	file, err := os.Open(path)
	if err != nil {
		return domain.AudioSpan{}, fmt.Errorf("open source for audio span: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return domain.AudioSpan{}, fmt.Errorf("stat source for audio span: %w", err)
	}
	windowEnd := startByte + lengthBytes
	if windowEnd > info.Size() {
		windowEnd = info.Size()
	}
	if startByte >= windowEnd {
		return domain.AudioSpan{}, fmt.Errorf("audio span window is empty: start %d end %d size %d", startByte, windowEnd, info.Size())
	}
	headLength := headerByteLength
	boundary := headLength
	if headLength > windowEnd {
		headLength = windowEnd
	}
	if startByte < headLength {
		// The window begins inside the head: the artifact is the file from
		// zero and every byte before the window is bytes already copied.
		headLength = 0
		boundary = startByte
	}

	// The artifact is fed to ffprobe through stdin, never via a temp file:
	// the web decoder consumes the same bytes through a pipe (ffmpeg -i
	// pipe:0), and the two demuxer paths disagree at partial-cluster seams —
	// the file path reported packets the decoder skips (a measured 736 ms
	// placement error on real titles). Measuring over stdin keeps the scan
	// on the decoder's side of that boundary.
	var artifact bytes.Buffer
	if headLength > 0 {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return domain.AudioSpan{}, fmt.Errorf("read container head: %w", err)
		}
		if _, err := io.CopyN(&artifact, file, headLength); err != nil {
			return domain.AudioSpan{}, fmt.Errorf("read container head: %w", err)
		}
	}
	if _, err := file.Seek(startByte, io.SeekStart); err != nil {
		return domain.AudioSpan{}, fmt.Errorf("read audio window: %w", err)
	}
	if _, err := io.CopyN(&artifact, file, windowEnd-startByte); err != nil {
		return domain.AudioSpan{}, fmt.Errorf("read audio window: %w", err)
	}

	probeCtx, cancelProbe := context.WithTimeout(ctx, 20*time.Second)
	defer cancelProbe()
	cmd := exec.CommandContext(
		probeCtx, a.settings.Get().FFprobePath,
		"-show_entries", "packet=stream_index,pts_time,pos",
		"-of", "json", "pipe:0",
	)
	cmd.Stdin = &artifact
	out, err := cmd.Output()
	if err != nil {
		return domain.AudioSpan{}, fmt.Errorf("ffprobe audio span: %w", err)
	}
	packets, err := decodeAnchorPackets(out)
	if err != nil {
		return domain.AudioSpan{}, err
	}
	first, last, ok := anchorSpan(packets, streamIndex, boundary)
	if !ok {
		return domain.AudioSpan{}, fmt.Errorf("audio span found no audio for stream %d in the measured window", streamIndex)
	}
	return domain.AudioSpan{StreamIndex: streamIndex, StartByte: startByte, LengthBytes: windowEnd - startByte, FirstPTSMS: first, LastPTSMS: last}, nil
}
