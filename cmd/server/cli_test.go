package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/application/updates"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

var (
	errFakeGUI   = errors.New("fake gui")
	errFakeServe = errors.New("fake serve")
)

func TestRestartRequiredMovedFromHandler(t *testing.T) {
	// Guards the moved helper: handler contract from api.go stays identical.
	old := config.Defaults()
	next := old
	next.ListenAddress = ":1"
	if !config.RestartRequired(old, next) {
		t.Fatal("listener change must require restart")
	}
}

func TestRootRejectsMinimizedOutsideGUI(t *testing.T) {
	root := newRootCommand(func(opts guiOptions) error {
		return errFakeGUI
	}, func(dataDir string, update bool, l logger) error { return errFakeServe })
	root.SetArgs([]string{"--minimized"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	if err == nil || !strings.Contains(out.String(), "serve") {
		t.Fatalf("bare run without GUI support must direct to serve, got err=%v out=%q", err, out.String())
	}
}

func TestServeResolvesDataDirBeforeRunner(t *testing.T) {
	// The serve path must resolve --data-dir (datadir.Resolve + mkdir) and
	// point the log file under it BEFORE the serve runner starts, so the
	// injected runner observes the resolved directory.
	flagDir := filepath.Join(t.TempDir(), "data")
	var got string
	root := newRootCommand(func(guiOptions) error { return errFakeGUI }, func(dataDir string, update bool, l logger) error {
		got = dataDir
		return errFakeServe
	})
	root.SetArgs([]string{"serve", "--data-dir", flagDir})
	if err := root.Execute(); !errors.Is(err, errFakeServe) {
		t.Fatalf("serve must run the injected serve runner, got err=%v", err)
	}
	if got != flagDir {
		t.Fatalf("runServe must receive the resolved data dir %q, got %q", flagDir, got)
	}
	if _, err := os.Stat(flagDir); err != nil {
		t.Fatalf("data dir must exist before the serve runner starts: %v", err)
	}
}

// TestServeForwardsUpdateFlag pins the --update wiring: the flag reaches
// the serve runner as update-and-serve, and stays false by default.
func TestServeForwardsUpdateFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"plain serve", nil, false},
		{"update and serve", []string{"--update"}, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var got bool
			root := newRootCommand(func(guiOptions) error { return errFakeGUI }, func(dataDir string, update bool, l logger) error {
				got = update
				return errFakeServe
			})
			root.SetArgs(append([]string{"serve", "--data-dir", filepath.Join(t.TempDir(), "data")}, testCase.args...))
			if err := root.Execute(); !errors.Is(err, errFakeServe) {
				t.Fatalf("serve must run the injected runner, got err=%v", err)
			}
			if got != testCase.want {
				t.Fatalf("runServe update flag = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestApplyRelaunchArgsAdoptsOriginalInvocation pins the handoff identity
// contract: a helper-relaunched process resumes the carried command line
// as its own arguments and consumes the marker exactly once; a process
// without the marker is untouched.
func TestApplyRelaunchArgsAdoptsOriginalInvocation(t *testing.T) {
	t.Setenv(updates.RelaunchArgsEnv, "")
	original := os.Args
	t.Cleanup(func() { os.Args = original })

	applyRelaunchArgs()
	if strings.Join(os.Args, " ") != strings.Join(original, " ") {
		t.Fatalf("args changed without a marker: %v", os.Args)
	}

	carried := []string{"serve", "--data-dir", filepath.Join(t.TempDir(), "data")}
	encoded, err := json.Marshal(carried)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(updates.RelaunchArgsEnv, string(encoded))
	os.Args = []string{original[0], "--update"}
	applyRelaunchArgs()
	if strings.Join(os.Args[1:], " ") != strings.Join(carried, " ") {
		t.Fatalf("relaunched args = %v, want %v", os.Args[1:], carried)
	}
	if os.Getenv(updates.RelaunchArgsEnv) != "" {
		t.Fatal("relaunch marker must be consumed, not passed on")
	}
}

// TestApplyUpdateBeforeServingReleasesFlushBarrier pins finding 2: the
// CLI path releases the accepted apply's handoff barrier immediately, so
// the pipeline never sits out the flush window before downloading. With a
// 5s flush wait and a download that fails fast, the step returns promptly;
// a missing release would stall for the whole window.
func TestApplyUpdateBeforeServingReleasesFlushBarrier(t *testing.T) {
	dir := t.TempDir()
	identity := updates.Identity{Version: "0.3.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Flavor: updates.FlavorGUI}
	coordinator := updates.NewManager(updates.ManagerDeps{
		Identity: identity,
		Resolver: updates.NewResolver(identity, stubSource{}),
		Notice: func(context.Context) (string, bool, error) {
			return "0.4.0", true, nil
		},
		Assets: func(context.Context, updates.Selection) (io.ReadCloser, error) {
			return nil, errors.New("download failed immediately")
		},
		Sink:             func(string, any) {},
		InstallDir:       dir,
		Executable:       filepath.Join(dir, "torrent-tv"),
		Exit:             func(int) {},
		FlushWait:        5 * time.Second,
		OperationTimeout: 30 * time.Second,
	})
	started := time.Now()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyUpdateBeforeServing(coordinator, log); err != nil {
		t.Fatalf("applyUpdateBeforeServing: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("accepted apply stalled %s before the pipeline ran: the flush barrier was not released", elapsed)
	}
}

// stubSource serves the stable release the resolver validates against.
type stubSource struct{}

func (stubSource) LatestRelease(context.Context) (updates.Release, error) {
	tag := "v0.4.0"
	url := func(name string) string {
		return "https://github.com/mihaiflorentin88/torrent-tv/releases/download/" + tag + "/" + name
	}
	return updates.Release{
		Tag: tag,
		URL: "https://github.com/mihaiflorentin88/torrent-tv/releases/tag/" + tag,
		Assets: []updates.Asset{
			{Name: "torrent-tv-0.4.0-darwin-arm64.tar.gz", URL: url("torrent-tv-0.4.0-darwin-arm64.tar.gz")},
			{Name: "SHA256SUMS", URL: url("SHA256SUMS")},
		},
	}, nil
}

func (stubSource) ChecksumManifest(_ context.Context, _ string) (string, error) {
	sum := sha256.Sum256([]byte("irrelevant"))
	return hex.EncodeToString(sum[:]) + "  torrent-tv-0.4.0-darwin-arm64.tar.gz\n", nil
}
