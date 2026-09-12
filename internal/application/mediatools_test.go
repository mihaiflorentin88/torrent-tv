package application

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func scriptedMediaTool(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripted stub binaries require a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newMediaToolService(t *testing.T, ffmpegPath, ffprobePath string) *Service {
	t.Helper()
	repo, settings := retryHarness(t)
	value := settings.Get()
	value.FFmpegPath = ffmpegPath
	value.FFprobePath = ffprobePath
	if err := settings.Save(value); err != nil {
		t.Fatal(err)
	}
	return NewService(nil, singleEngineSet(t, "qb:", &dummyEngine{}), repo, settings)
}

func TestTestTranscoderReportsHealthyFFmpeg(t *testing.T) {
	path := scriptedMediaTool(t, "case \"$*\" in\n*-version*) echo \"ffmpeg version 7.1.1 Copyright (c) 2000-2024 the FFmpeg developers\" ;;\n*lavfi*) head -c 2048 /dev/zero | tr '\\0' 'x' ;;\n*) exit 1 ;;\nesac\n")
	service := newMediaToolService(t, path, "/bin/false")
	message, err := service.TestTranscoder(context.Background(), "ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "ffmpeg 7.1") || !strings.Contains(message, "test encode ok") {
		t.Fatalf("unexpected healthy report: %s", message)
	}
}

func TestTestTranscoderReportsHealthyFFProbe(t *testing.T) {
	path := scriptedMediaTool(t, "case \"$*\" in\n*-version*) echo \"ffprobe version 7.0.2 Copyright\" ;;\n*show_entries*) echo '{\"streams\":[{\"codec_name\":\"pcm_s16le\",\"codec_type\":\"audio\"}]}' ;;\n*) exit 1 ;;\nesac\n")
	service := newMediaToolService(t, "/bin/false", path)
	message, err := service.TestTranscoder(context.Background(), "ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "ffprobe 7.0") || !strings.Contains(message, "test probe ok") {
		t.Fatalf("unexpected healthy report: %s", message)
	}
}

func TestTestTranscoderRejectsOldVersions(t *testing.T) {
	path := scriptedMediaTool(t, "case \"$*\" in\n*-version*) echo \"ffmpeg version 4.4.2 Copyright\" ;;\n*) exit 1 ;;\nesac\n")
	service := newMediaToolService(t, path, "/bin/false")
	_, err := service.TestTranscoder(context.Background(), "ffmpeg")
	if err == nil || !strings.Contains(err.Error(), "too old") || !strings.Contains(err.Error(), "4.4") {
		t.Fatalf("an old ffmpeg must be reported as too old, got: %v", err)
	}
}

func TestTestTranscoderReportsMissingPath(t *testing.T) {
	service := newMediaToolService(t, filepath.Join(t.TempDir(), "missing", "ffmpeg"), "/bin/false")
	_, err := service.TestTranscoder(context.Background(), "ffmpeg")
	if err == nil || !strings.Contains(err.Error(), "was not found at the configured path") {
		t.Fatalf("a missing binary must name the bad path, got: %v", err)
	}
}

func TestTestTranscoderReportsFailedEncode(t *testing.T) {
	path := scriptedMediaTool(t, "case \"$*\" in\n*-version*) echo \"ffmpeg version 7.1.1 Copyright\" ;;\n*lavfi*) echo \"encoder exploded\" >&2; exit 1 ;;\n*) exit 1 ;;\nesac\n")
	service := newMediaToolService(t, path, "/bin/false")
	_, err := service.TestTranscoder(context.Background(), "ffmpeg")
	if err == nil || !strings.Contains(err.Error(), "real-pipeline check failed") {
		t.Fatalf("a failing encode must surface as a pipeline failure, got: %v", err)
	}
}

func TestTestTranscoderReportsUnparseableVersion(t *testing.T) {
	path := scriptedMediaTool(t, "case \"$*\" in\n*-version*) echo \"some exotic build\" ;;\n*) exit 1 ;;\nesac\n")
	service := newMediaToolService(t, path, "/bin/false")
	_, err := service.TestTranscoder(context.Background(), "ffmpeg")
	if err == nil || !strings.Contains(err.Error(), "version could not be parsed") {
		t.Fatalf("an unparseable version must be reported, got: %v", err)
	}
}

func TestTestTranscoderRejectsUnknownKind(t *testing.T) {
	service := newMediaToolService(t, "/bin/false", "/bin/false")
	if _, err := service.TestTranscoder(context.Background(), "mediainfo"); err == nil || !strings.Contains(err.Error(), "unknown media tool") {
		t.Fatalf("unknown kinds must be rejected, got: %v", err)
	}
}
