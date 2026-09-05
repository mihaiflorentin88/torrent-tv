# Desktop GUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One binary per platform/arch that opens a Wails v3 GUI (window + tray + server control + reused webapp Downloads/Jobs/Settings views) when launched bare, and runs today's headless server via `filelist-streaming serve`.

**Architecture:** The cobra CLI becomes the entrypoint (`serve` preserves `main.go` exactly; bare launches the GUI). A `Supervisor` state machine in `internal/gui` owns server lifecycle and fans state out to the tray, window header, and pages. The desktop frontend is a Preact app (`desktop/`) embedding shared leaf components extracted from `web/` (`Settings`, `Events`, `CacheCoverage`, `Downloads`, `Jobs`) with a parametrized API origin; assets embed via `go:embed`. Autostart and data-dir relocation are per-OS packages under `internal/platform/`.

**Tech Stack:** Go 1.26, Wails v3 (`github.com/wailsapp/wails/v3` pinned to the beta current at implementation start — v3.0.0-beta.16 as of planning), cobra, Preact + Vite (existing stack), `golang.org/x/sys` (registry, AttachConsole).

**Spec:** `docs/superpowers/specs/2026-09-04-desktop-gui-design.md` — the plan argues from the spec; executors read both.

## Global Constraints

- One binary per platform/arch; artifact names unchanged: `filelist-streaming-<os>-<arch>[.exe]`. `linux-armv7` stays a pure `CGO_ENABLED=0` headless build (GUI code excluded via build tag).
- Bare launch attempts the GUI; with no display it exits non-zero with an error directing to `filelist-streaming serve`. Never silently falls back to serving (except the spec's armv7 exception, which logs a line when it does so).
- Data dir resolution order everywhere: `--data-dir` flag → `data.location` file next to the executable → `data/` next to the executable. `FILELIST_STREAMING_SETTINGS_PATH` still wins for the settings file itself.
- Everything runs embedded: GUI frontend (`internal/gui/static`), web UI (`internal/adapters/httpapi/static`), tray/app icons. No runtime downloads.
- Window close always hides to tray; quit only via tray menu / app quit. Tray icon: teal=running, gray=stopped, red=failed (derived from `clients/tizen/icon.png`).
- Shared components never render webapp shell (no `nav`, sidebar, header, footer) — guarded by a vitest test.
- `make check` and existing tests stay green throughout; `serve` semantics never change.
- Go tests follow repo conventions: `GOCACHE="$(GO_CACHE)" go test ./...`, per-OS files like `diskfree_{windows,darwin,linux}.go`.

## File Structure

```
cmd/server/main.go                       cobra root + serve (rewritten; current flow moves into serve)
cmd/server/console_windows.go            AttachConsole helper (windows only)
internal/gui/app.go                      Wails app assembly: window, tray, events, Run()
internal/gui/supervisor.go               server lifecycle state machine (no Wails imports)
internal/gui/supervisor_test.go
internal/gui/bindings.go                 service consumed by the frontend
internal/gui/bindings_test.go
internal/gui/singleinstance.go           lock file + loopback show-forward (in gui: needs net only)
internal/gui/singleinstance_test.go
internal/gui/assets.go                   go:embed static/* + tray icons
internal/gui/static/                     desktop frontend build output (git-ignored)
internal/gui/guifallback.go              //go:build linux && arm — bare/serve fallback stub
internal/platform/datadir/datadir.go     resolution, pointer file, relocation
internal/platform/datadir/datadir_test.go
internal/platform/autostart/autostart.go interface + docs
internal/platform/autostart/autostart_{windows,darwin,linux}.go
internal/platform/autostart/autostart_{windows,darwin,linux}_test.go
internal/adapters/httpapi/schema.go      exported SettingsSchema() + RedactedSettings() (moved from api.go)
internal/platform/config/config.go       + LoadAt, + Validate, + RestartRequired, + EnsureNativePathsWritable
web/icons.tsx                            Icon component (extracted from src.tsx)
web/shared-api.ts                        configureSharedApi(origin) — parametrized API origin
web/downloads.tsx                        Downloads view (extracted)
web/jobs.tsx                             Jobs view (extracted)
web/settings.tsx                         Settings/Events/CacheCoverage (existing; uses shared-api)
web/src.tsx                              imports extracted modules back
desktop/                                 @filelist/desktop workspace (Preact shell)
  index.html  vite.config.ts  package.json
  src/main.tsx  src/App.tsx  src/shell.css
  src/pages/{ServerPage,DownloadsPage,JobsPage,SettingsPage}.tsx
  src/lib/{state.ts,api.ts}
build/                                   wails3 build assets: appicon.png, darwin/Info.plist, windows/manifest.xml, linux/
tools/make_tray_icons.py                 one-time tray icon generation (committed output)
Makefile                                 build/build-all/build-arm64/desktop assets, wails invocations
.github/workflows/{ci,release}.yml       dev packages; per-OS runner matrix
deploy/systemd/filelist-streaming.service ExecStart gains serve --data-dir
deploy/bootstrap-server.sh               GUI runtime packages
deploy/pi-deploy.sh                      remote WebKitGTK preflight
docs/{INSTALLATION,DEVELOPMENT,CONFIGURATION}.md, README.md
```

---

### Task 1: Data-dir resolution package

**Files:**
- Create: `internal/platform/datadir/datadir.go`
- Test: `internal/platform/datadir/datadir_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces: `func Resolve(flagDir, exePath string) (dir string, source string, err error)` (source ∈ `flag`, `pointer`, `default`); `func PointerPath(exePath string) string`; `func SetPointer(exePath, dir string) error`; `func ClearPointer(exePath string) error`; `func Relocate(exePath, from, to string) error`.

- [ ] **Step 1: Write the failing tests**

```go
package datadir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "bin", "app") // binary dir: root/bin
	os.MkdirAll(filepath.Dir(exe), 0o750)
	os.WriteFile(PointerPath(exe), []byte(filepath.Join(root, "pointed")+"\n"), 0o640)

	dir, source, err := Resolve(filepath.Join(root, "flagged"), exe)
	if err != nil || dir != filepath.Join(root, "flagged") || source != "flag" {
		t.Fatalf("flag must win: dir=%q source=%q err=%v", dir, source, err)
	}
	dir, source, err = Resolve("", exe)
	if err != nil || source != "pointer" || dir != filepath.Join(root, "pointed") {
		t.Fatalf("pointer second: dir=%q source=%q err=%v", dir, source, err)
	}
	os.Remove(PointerPath(exe))
	dir, source, err = Resolve("", exe)
	if err != nil || source != "default" || dir != filepath.Join(filepath.Dir(exe), "data") {
		t.Fatalf("default last: dir=%q source=%q err=%v", dir, source, err)
	}
}

func TestRelocateRefusesNonEmptyTarget(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "app")
	from := filepath.Join(root, "data")
	to := filepath.Join(root, "elsewhere")
	os.MkdirAll(from, 0o750)
	os.WriteFile(filepath.Join(from, "settings.json"), []byte("{}"), 0o640)
	os.MkdirAll(to, 0o750)
	os.WriteFile(filepath.Join(to, "occupied.txt"), []byte("x"), 0o640)
	if err := Relocate(exe, from, to); err == nil {
		t.Fatal("relocation into a non-empty dir must be refused")
	}
	if _, err := os.Stat(filepath.Join(from, "settings.json")); err != nil {
		t.Fatal("source must stay untouched on refusal")
	}
}

func TestRelocateSameVolumeMovesAndWritesPointer(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "app")
	from := filepath.Join(root, "data")
	to := filepath.Join(root, "data2")
	os.MkdirAll(filepath.Join(from, "logs"), 0o750)
	os.WriteFile(filepath.Join(from, "settings.json"), []byte("{}"), 0o640)
	os.WriteFile(filepath.Join(from, "logs", "server.log"), []byte("hi"), 0o640)
	if err := Relocate(exe, from, to); err != nil {
		t.Fatalf("Relocate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to, "logs", "server.log")); err != nil {
		t.Fatalf("contents must move: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("source dir must be gone after same-volume move: %v", err)
	}
	if got, _ := Resolve("", exe); got != to {
		t.Fatalf("pointer must name the new dir, got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/platform/datadir/`
Expected: FAIL (package does not exist).

- [ ] **Step 3: Implement**

```go
// Package datadir resolves the application data directory and relocates it.
// Resolution order (spec: Data directory): --data-dir flag, then the
// data.location pointer file next to the executable, then data/ next to the
// executable.
package datadir

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const pointerName = "data.location"

// Relocate moves the data directory from → to and records the new location.
// The caller must have stopped the server first.
func Relocate(exePath, from, to string) error {
	absTo, err := filepath.Abs(to)
	if err != nil {
		return err
	}
	if absTo == from {
		return fmt.Errorf("new data location is the current location")
	}
	if entries, err := os.ReadDir(absTo); err == nil && len(entries) > 0 {
		return fmt.Errorf("target %s is not empty", absTo)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := moveTree(from, absTo); err != nil {
		return err
	}
	return SetPointer(exePath, absTo)
}

func moveTree(from, to string) error {
	// Same volume: rename is atomic and instant.
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	// Cross volume: copy, verify each file by SHA-256, delete only after all
	// copies verified; on any error leave the source untouched.
	if err := copyTree(from, to); err != nil {
		return err
	}
	return os.RemoveAll(from)
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := copyVerified(path, target, info.Mode()); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode())
	})
}

func copyVerified(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	in.Seek(0, io.SeekStart)
	h2 := sha256.New()
	if _, err := io.Copy(h2, in); err != nil {
		return err
	}
	if !bytesEqual(h.Sum(nil), h2.Sum(nil)) {
		os.Remove(dst)
		return fmt.Errorf("verification failed copying %s", src)
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	return hex.EncodeToString(a) == hex.EncodeToString(b)
}

func Resolve(flagDir, exePath string) (string, string, error) {
	if strings.TrimSpace(flagDir) != "" {
		abs, err := filepath.Abs(flagDir)
		return abs, "flag", err
	}
	if p := readPointer(exePath); p != "" {
		return p, "pointer", nil
	}
	return filepath.Join(filepath.Dir(exePath), "data"), "default", nil
}

func PointerPath(exePath string) string {
	return filepath.Join(filepath.Dir(exePath), pointerName)
}

func readPointer(exePath string) string {
	b, err := os.ReadFile(PointerPath(exePath))
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(b))
	if !filepath.IsAbs(p) {
		return ""
	}
	return p
}

func SetPointer(exePath, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	tmp := PointerPath(exePath) + ".tmp"
	if err := os.WriteFile(tmp, []byte(abs+"\n"), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, PointerPath(exePath))
}

func ClearPointer(exePath string) error {
	err := os.Remove(PointerPath(exePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/platform/datadir/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/datadir
git commit -m "feat(datadir): data-dir resolution and verified relocation"
```

---

### Task 2: Config loader gains an explicit path; shared validation helpers

**Files:**
- Modify: `internal/platform/config/config.go:94-98` (Load), append helpers at file end
- Modify: `internal/platform/config/config_test.go`
- Modify: `internal/adapters/httpapi/api.go:184-202` (putSettings uses shared helpers)

**Interfaces:**
- Consumes: existing `Store`, `Settings`, `Save`, `validate`.
- Produces: `func LoadAt(path string) (*Store, error)`; `Load()` keeps today's behavior; `func (s *Store) Validate(v Settings) error`; `func RestartRequired(old, new Settings) bool`; `func EnsureNativePathsWritable(engine, downloadRoot, sessionDir string) error` (moved from httpapi, identical logic).

- [ ] **Step 1: Write the failing test** (in `config_test.go`)

```go
func TestLoadAtUsesGivenPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"fileListUsername":"u","fileListPasskey":"p"}`), 0o640)
	s, err := LoadAt(path)
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if s.Path() != path {
		t.Fatalf("Path() = %q, want %q", s.Path(), path)
	}
	if s.Get().FileListUsername != "u" {
		t.Fatal("settings from the given path must load")
	}
}

func TestRestartRequiredTracksListenerAndEngine(t *testing.T) {
	old := Defaults()
	next := old
	if RestartRequired(old, next) {
		t.Fatal("identical settings never require restart")
	}
	next.ListenAddress = ":9999"
	next.DownloadEngine = "qbittorrent"
	if !RestartRequired(old, next) {
		t.Fatal("listener/engine changes require restart")
	}
	next.InstanceName = "other"
	if !RestartRequired(old, next) {
		t.Fatal("instance name alone never requires restart")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/platform/config/`
Expected: FAIL — `LoadAt` / `RestartRequired` undefined.

- [ ] **Step 3: Implement.** In `config.go`, replace `Load()` lines 94-98 with:

```go
func Load() (*Store, error) {
	path := strings.TrimSpace(os.Getenv(EnvironmentPrefix + "SETTINGS_PATH"))
	if path == "" {
		path = DefaultSettingsPath
	}
	return LoadAt(path)
}

// LoadAt loads the store from an explicit settings file path. The data-dir
// layer calls this after resolving the directory.
func LoadAt(path string) (*Store, error) {
	s := &Store{path: path, envManaged: map[string]bool{}, fileProvided: map[string]bool{}}
	base := Defaults()
	// … body of the former Load() from here down unchanged
	// (b, err := os.ReadFile(s.path) … through `return s, nil`).
```

Delete the old path-resolution lines from the moved body. At the end of the file add:

```go
// Validate exposes load-time validation so non-HTTP callers (GUI bindings)
// apply exactly the same rules before Save. Save() validates internally;
// this is for pre-checks.
func (s *Store) Validate(v Settings) error { return s.validate(v) }

// RestartRequired reports whether any setting that the running server read
// at startup differs, mirroring the HTTP PUT response contract.
func RestartRequired(old, new Settings) bool {
	return old.ListenAddress != new.ListenAddress || old.DatabasePath != new.DatabasePath ||
		old.MaxConcurrentJobs != new.MaxConcurrentJobs || old.TitleRefreshTimeoutMinutes != new.TitleRefreshTimeoutMinutes ||
		old.DownloadEngine != new.DownloadEngine || old.TorrentPeerPort != new.TorrentPeerPort ||
		old.TorrentSessionDir != new.TorrentSessionDir
}
```

Move `ensureNativePathsWritable` from `internal/adapters/httpapi` (grep for `func ensureNativePathsWritable`) into `config.go` as exported `EnsureNativePathsWritable(engine, downloadRoot, sessionDir string) error` with identical body; update the httpapi call site to `config.EnsureNativePathsWritable(...)`.

- [ ] **Step 4: Verify refactor is behavior-neutral**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/platform/config/ ./internal/adapters/httpapi/`
Expected: PASS (all pre-existing tests green, new tests green).

- [ ] **Step 5: Commit**

```bash
git add internal/platform/config internal/adapters/httpapi
git commit -m "feat(config): LoadAt, Validate, RestartRequired, EnsureNativePathsWritable"
```

---

### Task 3: Cobra CLI — `serve` plus explicit-path wiring

**Files:**
- Modify: `cmd/server/main.go` (full rewrite)
- Create: `cmd/server/console_windows.go`, `cmd/server/console_other.go`
- Test: `cmd/server/cli_test.go`

**Interfaces:**
- Consumes: `datadir.Resolve`, `config.LoadAt`, `composition.New`, `internal/gui.Run` (arrives in Task 7; root delegates to `serve` until then).
- Produces: `serveCmd` with `--data-dir`; root with `--data-dir`, `--minimized` (validated but unused until Task 7), `--version`. `func runServe(dataDir string, log *slog.Logger) error`.

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
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
	}, func(dataDir string, l logger) error { return errFakeServe })
	root.SetArgs([]string{"--minimized"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	if err == nil || !strings.Contains(out.String(), "serve") {
		t.Fatalf("bare run without GUI support must direct to serve, got err=%v out=%q", err, out.String())
	}
}
```

(`errFakeGUI`, `errFakeServe`, and the injectable `guiOptions`/`logger` shapes are defined by the implementation in Step 3 — the test pins that `--minimized` before Task 7 reports the serve path instead of starting a server.)

- [ ] **Step 2: Run to verify failure**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./cmd/server/`
Expected: FAIL — `newRootCommand` undefined.

- [ ] **Step 3: Implement.** `cmd/server/main.go`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/mihaiflorentin88/torrent-tv/internal/composition"
)

type guiOptions struct {
	Minimized bool
	DataDir   string
}

type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// newRootCommand separates command wiring from effects so tests can inject
// the GUI and serve runners.
func newRootCommand(runGUI func(guiOptions) error, runServe func(string, logger) error) *cobra.Command {
	var dataDir string
	var minimized bool
	root := &cobra.Command{
		Use:     "filelist-streaming",
		Short:   "FileList Streaming media server",
		Version: composition.Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := guiOptions{Minimized: minimized, DataDir: dataDir}
			if err := runGUI(opts); err != nil {
				if isNoDisplay(err) {
					return fmt.Errorf("no display available for the GUI; run 'filelist-streaming serve' instead")
				}
				return err
			}
			return nil
		},
	}
	root.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory (default: data/ next to the executable)")
	root.Flags().BoolVar(&minimized, "minimized", false, "start minimized to the system tray")
	serve := &cobra.Command{
		Use:   "serve",
		Short: "run the headless streaming server",
		RunE: func(cmd *cobra.Command, args []string) error {
			attachParentConsole()
			log, closeLog := newLogger(os.Stdout, isTerminal(os.Stdout), logFilePath(dataDir))
			defer closeLog()
			return runServe(dataDir, log)
		},
	}
	root.AddCommand(serve)
	return root
}

func main() {
	root := newRootCommand(runGUI, runServe)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

`runServe` (same file) is today's `main()` body, parameterized:

```go
func runServe(dataDir string, log logger) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, _, err := datadir.Resolve(dataDir, exe)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create data dir %s: %w", dir, err)
	}
	// FILELIST_STREAMING_SETTINGS_PATH keeps precedence (spec: Data directory).
	settingsPath := os.Getenv("FILELIST_STREAMING_SETTINGS_PATH")
	if settingsPath == "" {
		settingsPath = filepath.Join(dir, "settings.json")
	}
	app, err := openComposition(settingsPath, log)
	if err != nil {
		log.Error("startup failed", "error", err)
		return err
	}
	defer app.Close()
	// … today's signal context + ListenAndServe flow, unchanged.
}

func openComposition(settingsPath string, log logger) (*composition.App, error) {
	var store *config.Store
	if env := os.Getenv("FILELIST_STREAMING_SETTINGS_PATH"); env != "" {
		store, err := config.Load() // env-resolved path, unchanged behavior
		return store, err
	}
	return config.LoadAt(settingsPath)
}
```

`logFilePath(dataDir)` returns `<resolved data dir>/logs` feeding the existing `newLogger` (adjust `logging.go`'s path join from `filepath.Join("data", "logs", "server.log")` to accept the resolved dir). `console_windows.go`:

```go
//go:build windows

package main

import (
	"os"
	"golang.org/x/sys/windows"
)

const attachParentProcess = ^uintptr(0) // ATTACH_PARENT_PROCESS

// attachParentConsole re-attaches stdout/stderr when a windowsgui-subsystem
// binary is started from an existing terminal, so `serve` streams logs.
func attachParentConsole() {
	if windows.AttachConsole(attachParentProcess) != nil {
		return
	}
	if h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); err == nil {
		os.Stdout = os.NewFile(uintptr(h), "stdout")
	}
	if h, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE); err == nil {
		os.Stderr = os.NewFile(uintptr(h), "stderr")
	}
	if h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE); err == nil {
		os.Stdin = os.NewFile(uintptr(h), "stdin")
	}
}
```

`console_other.go`: `//go:build !windows` + empty `func attachParentConsole() {}`. Until Task 7, `runGUI` returns `errNoDisplay` (a sentinel `isNoDisplay` recognizes) so root prints the serve direction; `isTerminal(os.Stdout)` wraps the existing `term.IsTerminal(int(os.Stdout.Fd()))` from `logging.go`.

Add `github.com/spf13/cobra` to go.mod: `go get github.com/spf13/cobra@latest`.

- [ ] **Step 4: Verify tests pass and serve is unchanged**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./cmd/server/ && GOCACHE=/tmp/filelist-streaming-go-cache go build ./...`
Expected: PASS; binary builds. Smoke: `GOCACHE=… go run ./cmd/server serve --help` shows `--data-dir`; `--version` prints the VERSION file content.

- [ ] **Step 5: Commit**

```bash
git add cmd/server go.mod go.sum
git commit -m "feat(cli): cobra root with serve, --data-dir, --version"
```

---

### Task 4: Autostart package (Windows / macOS / Linux)

**Files:**
- Create: `internal/platform/autostart/autostart.go`, `autostart_windows.go`, `autostart_darwin.go`, `autostart_linux.go`
- Test: `internal/platform/autostart/autostart_darwin_test.go`, `autostart_linux_test.go` (Windows tests run only on Windows runners)

**Interfaces:**
- Consumes: nothing.
- Produces: `type Options struct { ExePath string; Args []string }`; `func Enable(opts Options) error`; `func Disable() error`; `func Enabled() (bool, error)`. Paths injectable via package vars `DarwinPlistDir func() string`, `LinuxAutostartDir func() string` (default `os.UserHomeDir`/`os.UserConfigDir` based) so tests use temp dirs.

- [ ] **Step 1: Write the failing tests** (`autostart_linux_test.go`)

```go
//go:build linux

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxEnableDisableRoundTrip(t *testing.T) {
	dir := t.TempDir()
	LinuxAutostartDir = func() string { return dir }
	defer func() { LinuxAutostartDir = defaultLinuxAutostartDir }()

	if err := Enable(Options{ExePath: "/opt/fs/filelist-streaming", Args: []string{"--minimized", "--data-dir", "/opt/fs/data"}}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "filelist-streaming.desktop"))
	if err != nil {
		t.Fatalf("desktop entry: %v", err)
	}
	text := string(b)
	for _, want := range []string{"[Desktop Entry]", `Exec="/opt/fs/filelist-streaming" --minimized --data-dir "/opt/fs/data"`, "Type=Application"} {
		if !strings.Contains(text, want) {
			t.Fatalf("desktop entry missing %q in:\n%s", want, text)
		}
	}
	if ok, err := Enabled(); err != nil || !ok {
		t.Fatalf("Enabled after Enable = %v, %v", ok, err)
	}
	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if ok, err := Enabled(); err != nil || ok {
		t.Fatalf("Enabled after Disable = %v, %v", ok, err)
	}
	if err := Disable(); err != nil { // idempotent
		t.Fatalf("Disable must tolerate absence: %v", err)
	}
}
```

`autostart_darwin_test.go` mirrors this against the plist path (assert `RunAtLoad` present and `ProgramArguments` array contains exe + args), swapping `DarwinPlistDir`.

- [ ] **Step 2: Run to verify failure**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/platform/autostart/`
Expected: FAIL (package undefined on linux).

- [ ] **Step 3: Implement.** `autostart.go`:

```go
// Package autostart manages launch-on-boot per OS. The OS artifact is the
// source of truth: Enabled() reads it back; the GUI never trusts memory.
// Entries always pin --minimized and an explicit --data-dir so launchd/XDG/
// registry launches do not depend on a working directory.
package autostart

type Options struct {
	ExePath string
	Args    []string
}

func Enable(opts Options) error  { return platformEnable(opts) }
func Disable() error             { return platformDisable() }
func Enabled() (bool, error)     { return platformEnabled() }
```

`autostart_linux.go`:

```go
//go:build linux

package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var LinuxAutostartDir = defaultLinuxAutostartDir

func defaultLinuxAutostartDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "autostart")
}

func entryPath() string { return filepath.Join(LinuxAutostartDir(), "filelist-streaming.desktop") }

func quoteExec(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
	}
	return `"` + args[0] + `"` + " " + strings.Join(quoted[1:], " ")
}

func platformEnable(opts Options) error {
	if err := os.MkdirAll(LinuxAutostartDir(), 0o755); err != nil {
		return err
	}
	content := "[Desktop Entry]\nType=Application\nName=FileList Streaming\nExec=" +
		quoteExec(append([]string{opts.ExePath}, opts.Args...)) +
		"\nTerminal=false\nX-GNOME-Autostart-enabled=true\nCategories=Network;AudioVideo;\n"
	return os.WriteFile(entryPath(), []byte(content), 0o644)
}

func platformDisable() error {
	err := os.Remove(entryPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func platformEnabled() (bool, error) {
	_, err := os.Stat(entryPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
```

`autostart_darwin.go`: same shape; `DarwinPlistDir` defaults to `~/Library/LaunchAgents`; plist written with `RunAtLoad=true`, `KeepAlive=false`, `ProgramArguments` array (exe + args), label `com.filelist-streaming`; Disable removes the plist (nil-tolerant); Enabled stats it.

`autostart_windows.go`:

```go
//go:build windows

package autostart

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
const valueName = "FileList Streaming"

func platformEnable(opts Options) error {
	parts := append([]string{opts.ExePath}, opts.Args...)
	for i, p := range parts {
		parts[i] = `"` + p + `"`
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(valueName, strings.Join(parts, " "))
}

func platformDisable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(valueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func platformEnabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	if _, _, err := k.GetStringValue(valueName); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
```

(`filepath` import only if used by arg quoting; drop if unused. Build on a Windows machine/runner to confirm: `GOOS=windows go vet ./internal/platform/autostart/`.)

- [ ] **Step 4: Run tests**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/platform/autostart/ && GOOS=windows GOCACHE=/tmp/filelist-streaming-go-cache go vet ./internal/platform/autostart/ && GOOS=darwin GOCACHE=/tmp/filelist-streaming-go-cache go vet ./internal/platform/autostart/`
Expected: PASS / vet clean on all three targets.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/autostart
git commit -m "feat(autostart): per-OS launch-on-boot with OS-state read-back"
```

---

### Task 5: Single-instance lock with show-forwarding

**Files:**
- Create: `internal/gui/singleinstance.go`
- Test: `internal/gui/singleinstance_test.go`

Note: this file lives in `internal/gui` (net + fs only, no Wails), so Task 1-4 keep the repo Wails-free. `internal/gui` gets the Wails dependency in Task 6.

**Interfaces:**
- Consumes: nothing.
- Produces: `type InstanceLock struct{…}`; `func Acquire(dataDir string) (*InstanceLock, error)` — if a live instance holds the lock, sends "show" to it and returns `(nil, errAlreadyRunning)`; `func (l *InstanceLock) OnShow(fn func())` ; `func (l *InstanceLock) Close() error`.

- [ ] **Step 1: Write the failing test**

```go
package gui

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSecondInstanceForwardsShow(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Close()
	shown := make(chan struct{}, 1)
	first.OnShow(func() { shown <- struct{}{} })

	second, err := Acquire(filepath.Join(dir, "data"))
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquire must report already running, got %v, %v", second, err)
	}
	select {
	case <-shown:
	case <-time.After(3 * time.Second):
		t.Fatal("running instance never received the show notification")
	}
}

func TestStaleLockIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	l.Close() // releases; next Acquire must succeed
	l2, err := Acquire(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("re-acquire after close: %v", err)
	}
	l2.Close()
}
```

- [ ] **Step 2: Run to verify failure**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/gui/`
Expected: FAIL (package undefined).

- [ ] **Step 3: Implement** `internal/gui/singleinstance.go`:

```go
package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

var ErrAlreadyRunning = errors.New("another instance is already running")

type lockContents struct {
	PID       int    `json:"pid"`
	NotifyURL string `json:"notifyUrl"` // 127.0.0.1:<port> the running instance listens on
}

// InstanceLock guards single-instance behavior. Acquire either claims the
// lock (starting a loopback "show" listener) or, when a live instance is
// found, forwards "show" to it and returns ErrAlreadyRunning.
type InstanceLock struct {
	path    string
	ln      net.Listener
	onShow  func()
	closed  bool
}

func Acquire(dataDir string) (*InstanceLock, error) {
	path := filepath.Join(dataDir, "gui.lock")
	if b, err := os.ReadFile(path); err == nil {
		var c lockContents
		if json.Unmarshal(b, &c) == nil && c.NotifyURL != "" {
			if conn, derr := net.DialTimeout("tcp", c.NotifyURL, time.Second); derr == nil {
				fmt.Fprintln(conn, "show")
				conn.Close()
				return nil, ErrAlreadyRunning
			}
		}
		// Stale lock (owner dead or unreadable): take over.
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &InstanceLock{path: path, ln: ln}
	if err := writeLock(path, lockContents{PID: os.Getpid(), NotifyURL: ln.Addr().String()}); err != nil {
		ln.Close()
		return nil, err
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 16)
			conn.Read(buf)
			conn.Close()
			if l.onShow != nil {
				l.onShow()
			}
		}
	}()
	return l, nil
}

func writeLock(path string, c lockContents) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (l *InstanceLock) OnShow(fn func()) { l.onShow = fn }

func (l *InstanceLock) Close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	l.ln.Close()
	os.Remove(l.path)
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/gui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gui
git commit -m "feat(gui): single-instance lock with show-forwarding"
```

---

### Task 6: Wails dependency, build scaffolding, GUI entrypoint

**Files:**
- Modify: `go.mod` (wails v3 pin)
- Create: `internal/gui/app.go`, `internal/gui/assets.go`, `internal/gui/guifallback.go`, `internal/gui/runner.go`
- Create: `build/appicon.png` (copy of `clients/tizen/icon.png`), `build/darwin/Info.plist`, `build/windows/manifest.xml`, `build/linux/` (per `wails3 init` layout)
- Modify: `cmd/server/main.go` (`runGUI` → `gui.Run`)

**Interfaces:**
- Consumes: Tasks 1-5.
- Produces: `func Run(opts gui.Options) error` in package `gui` (opts: `Minimized, DataDir string`) — the real GUI loop; bare command now truly attempts the GUI. Package `gui` guarded `//go:build !(linux && arm)` with `guifallback.go` for armv7.

Wait — the singleinstance file from Task 5 is in package `gui`; with build tags splitting `internal/gui`, `singleinstance.go` must carry the same `!(linux && arm)` tag or move to `internal/platform/singleinstance`. Move it: `internal/platform/singleinstance` (import from gui). Adjust Task 5 paths accordingly at implementation.

- [ ] **Step 1: Add the dependency and scaffold**

```bash
go get github.com/wailsapp/wails/v3@v3.0.0-beta.16
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.16
mkdir -p build/darwin build/windows build/linux
cp clients/tizen/icon.png build/appicon.png
wails3 init -nopkg  # inspect the generated build/ layout only; do not keep its template main
```

Create `build/windows/manifest.xml` (com.releases.create manifest with DPI-aware true), `build/darwin/Info.plist` (CFBundleName FileList Streaming, CFBundleIdentifier com.filelist-streaming.app, CFBundleIconFile appicon, LSMinimumSystemVersion 11.0, NSHighResolutionCapable). Verify the pinned beta's option names against its docs for everything below (`https://v3.wails.io`); they are beta-stable but spellings matter.

- [ ] **Step 2: Implement the GUI runner.** `internal/gui/runner.go`:

```go
//go:build !(linux && arm)

// Package gui assembles the Wails desktop app. All Wails usage is confined
// to this package so a framework migration touches one boundary (spec: Risks).
package gui

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/composition"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/datadir"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/singleinstance"
)

type Options struct {
	Minimized bool
	DataDir   string
}

func Run(opts Options) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, _, err := datadir.Resolve(opts.DataDir, exe)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create data dir %s: %w", dir, err)
	}
	settingsPath := os.Getenv("FILELIST_STREAMING_SETTINGS_PATH")
	if settingsPath == "" {
		settingsPath = filepath.Join(dir, "settings.json")
	}
	settings, err := config.LoadAt(settingsPath)
	if err != nil {
		return err
	}
	lock, err := singleinstance.Acquire(dir)
	if err != nil {
		return err // ErrAlreadyRunning: second launch exits after forwarding "show"
	}
	defer lock.Close()

	log := newGUILogger(dir) // slog to <dataDir>/logs/server.jsonl; JSON, no console
	sup := NewSupervisor(SupervisorDeps{
		Log:      log,
		Settings: settings,
		CanStart: func() error {
			if missing := settings.MissingRequired(); len(missing) > 0 {
				return fmt.Errorf("required settings missing: %s", strings.Join(missing, ", "))
			}
			return nil
		},
	})

	app := application.New(application.Options{
		Name:   "FileList Streaming",
		Assets: application.AssetOptions{Handler: assetHandler()},
		Services: []application.Service{
			application.NewService(&Bindings{settings: settings, sup: sup}),
		},
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "FileList Streaming", Width: 1100, Height: 720, MinWidth: 960, MinHeight: 600,
		URL: "/", Hidden: opts.Minimized,
	})
	// Close always hides to tray; quit only via tray menu (spec: Tray).
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		win.Hide()
	})

	tray := newTray(app, win, sup, settings)
	lock.OnShow(func() { win.Show() })
	sup.OnStateChange(func(s State, sErr error) {
		app.Event.Emit("server:state", StateEvent{State: s.String(), Error: errString(sErr), Address: sup.Address()})
		tray.Refresh(s)
	})
	app.Event.Emit("server:state", StateEvent{State: sup.State().String(), Address: sup.Address()})
	if !opts.Minimized {
		win.Show()
	}
	app.Run()
	return nil
}
```

`assets.go`: `//go:embed static` + `http.FS` handler serving `static`. `guifallback.go`:

```go
//go:build linux && arm

// armv7 (spec: Packaging) ships pure headless: bare run behaves like serve.
package gui

func Run(opts Options) error { return ErrNoDisplay }
```

with `ErrNoDisplay` defined in runner.go (`var ErrNoDisplay = errors.New("no display available for the GUI; run 'filelist-streaming serve' instead")`) and `cmd/server`'s `isNoDisplay(err)` checking `errors.Is`. Update `cmd/server/main.go`: `runGUI := func(o guiOptions) error { return gui.Run(gui.Options{Minimized: o.Minimized, DataDir: o.DataDir}) }`.

- [ ] **Step 3: Verify builds on this machine (darwin/arm64)**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go build ./... && GOOS=windows GOCACHE=/tmp/filelist-streaming-go-cache go build ./... && GOOS=linux GOARCH=arm GOCACHE=/tmp/filelist-streaming-go-cache go build ./...`
Expected: all three compile (Windows stays cgo-free; `GOOS=linux GOARCH=arm64` compiles once webkit dev headers are absent locally — verify only that the arm fallback path does).

- [ ] **Step 4: Smoke the GUI** — `go run ./cmd/server` on this Mac opens an empty window titled FileList Streaming; closing hides it (check it stays alive); `--minimized` opens nothing.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/gui cmd/server build
git commit -m "feat(gui): wails v3 app shell — window, close-to-tray, embed, armv7 fallback"
```

---

### Task 7: Supervisor state machine

**Files:**
- Create: `internal/gui/supervisor.go`
- Test: `internal/gui/supervisor_test.go`

**Interfaces:**
- Consumes: `composition.App` (New/Close/ListenAndServe), `config.Store`.
- Produces: `type State string` (`stopped|starting|running|stopping|failed`); `type SupervisorDeps struct { Log *slog.Logger; Settings *config.Store; CanStart func() error }`; `func NewSupervisor(deps) *Supervisor`; methods `Start() error`, `Stop() error`, `Restart() error`, `State() State`, `Error() error`, `Address() string`; `OnStateChange(func(State, error))`. State starts at `stopped`.

- [ ] **Step 1: Write the failing tests** — fake app with controllable listen error:

```go
type fakeApp struct {
	serveErr error
	serve    chan error
	closed   chan struct{}
}
func (f *fakeApp) ListenAndServe() error { if f.serve != nil { return <-f.serve }; return f.serveErr }
func (f *fakeApp) Close()                { close(f.closed) }

func TestSupervisorTransitionsAndFailure(t *testing.T) {
	events := []State{}
	app := &fakeApp{serveErr: errors.New("bind: address already in use")}
	sup := NewSupervisor(SupervisorDeps{Settings: storeWithAllRequired(t)})
	sup.appFactory = func() (*composition.App, error) { /* returns wrapper around fakeApp */ }
	sup.OnStateChange(func(s State, _ error) { events = append(events, s) })

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// wait until failed
	deadline := time.After(2 * time.Second)
	for sup.State() != StateFailed { select { case <-deadline: t.Fatal("never failed") ; case <-time.After(10 * time.Millisecond): } }
	if sup.Error() == nil || !strings.Contains(sup.Error().Error(), "address already in use") {
		t.Fatalf("failed state must carry the error, got %v", sup.Error())
	}
	if sup.State() != StateFailed || len(events) == 0 {
		t.Fatal("state change events must fire")
	}
}

func TestSupervisorRefusesMissingSettings(t *testing.T) {
	sup := NewSupervisor(SupervisorDeps{Settings: emptyStore(t), CanStart: func() error { return errors.New("required settings missing: fileListUsername") }})
	if err := sup.Start(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Start must refuse: %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatal("refusal leaves state stopped — setup, not failed")
	}
}

func TestSupervisorStopFromRunning(t *testing.T) { /* Start with clean fake, then Stop; assert stopped and app.Close called via closed channel */ }
func TestSupervisorRestartRunsStopThenStart(t *testing.T) { /* order asserted via recorded calls */ }
```

- [ ] **Step 2: Run to verify failure**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/gui/ -run Supervisor`
Expected: FAIL — Supervisor undefined.

- [ ] **Step 3: Implement** (abridged to the contract; full body in the same shape as the tests):

```go
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

type appLike interface {
	ListenAndServe() error
	Close()
}

type Supervisor struct {
	mu         sync.Mutex
	state      State
	err        error
	app        *composition.App
	address    string
	deps       SupervisorDeps
	onChange   func(State, error)
	appFactory func() (*composition.App, error) // test seam; default wraps composition.New
}

func (s *Supervisor) setState(st State, err error) {
	s.state, s.err = st, err
	if s.onChange != nil { s.onChange(st, err) }
}

func (s *Supervisor) Start() error {
	s.mu.Lock()
	if s.state != StateStopped && s.state != StateFailed { s.mu.Unlock(); return errors.New("server is not stopped") }
	if s.deps.CanStart != nil {
		if err := s.deps.CanStart(); err != nil { s.mu.Unlock(); return err }
	}
	s.setState(StateStarting, nil)
	s.mu.Unlock()
	go func() {
		app, err := s.newApp()
		if err != nil { s.mu.Lock(); s.setState(StateFailed, err); s.mu.Unlock(); return }
		s.mu.Lock(); s.app, s.address = app, app.ListenAddress; s.setState(StateRunning, nil); s.mu.Unlock()
		if err := app.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock(); s.setState(StateFailed, err); s.mu.Unlock()
		}
	}()
	return nil
}

func (s *Supervisor) Stop() error {
	s.mu.Lock()
	if s.state != StateRunning { s.mu.Unlock(); return errors.New("server is not running") }
	s.setState(StateStopping, nil)
	app := s.app
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = app.Server.Shutdown(ctx)
	app.Close()
	s.mu.Lock(); s.app = nil; s.setState(StateStopped, nil); s.mu.Unlock()
	return nil
}
```

`Restart()` = Stop then Start (report first error). `newApp()` default: `composition.New(log)` — note `composition.New` prompts on interactive terminals; the GUI process has none, and `CanStart` guarantees required settings exist before Start, so the prompt path never triggers.

- [ ] **Step 4: Run tests**

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/gui/ -race -run Supervisor`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gui
git commit -m "feat(gui): supervisor state machine with failure and refusal states"
```

---

### Task 8: Extract shared frontend modules (icons, API origin, Downloads, Jobs)

**Files:**
- Create: `web/icons.tsx`, `web/shared-api.ts`, `web/downloads.tsx`, `web/jobs.tsx`
- Modify: `web/src.tsx` (import back; delete moved code), `web/settings.tsx` (use shared-api)
- Modify: `web/package.json` (add `exports` map)
- Test: `web/shared.test.tsx` (prop contracts + API origin), `web/chrome-guard.test.tsx`

**Interfaces:**
- Produces: `web/icons.tsx` → `export function Icon({ name }: { name: string })`; `web/shared-api.ts` → `export function configureSharedApi(origin: string): void` and `export function sharedApi(): API`; `web/downloads.tsx` → `export function Downloads(props: { items: Download[]; onRefresh: () => Promise<void> | void; onPlay: (d: Download) => void; onRemove: (d: Download) => Promise<void>; onAction: (d: Download, a: DownloadTransferAction) => Promise<void> | void })` (contract identical to src.tsx:622 today); `web/jobs.tsx` → `export function Jobs(props: { onError: (s: string) => void; deepJobId?: string; onOpenDetail: (id: string) => void; onCloseDetail: () => void })` (contract identical to src.tsx:672).

- [ ] **Step 1: Move the code (mechanical, no behavior change).**
  1. Create `web/icons.tsx`: move `function Icon` and the icon-map it consults from `web/src.tsx:58` verbatim; add `export`.
  2. Create `web/shared-api.ts`:
     ```ts
     import { API } from '@filelist/shared';
     // Shared components must not hardcode location.origin (spec: Reuse
     // boundary): the desktop app points them at the loopback server.
     let api = new API(location.origin);
     export function configureSharedApi(origin: string): void { api = new API(origin); }
     export function sharedApi(): API { return api; }
     ```
  3. Create `web/downloads.tsx`: move `Downloads` and its private subcomponents (download card, removal confirm, filter toolbar) plus `reconcileDownloads`, `captureDownloadAnchor`, `restoreDownloadAnchor` from `src.tsx` verbatim; imports become `import { sharedApi } from './shared-api'` and `import { Icon } from './icons'`; replace internal `api.` references with `sharedApi().`; add `export` on `Downloads` and export the reconcile/anchor helpers (desktop reuses them).
  4. Create `web/jobs.tsx`: move `Jobs` (src.tsx:672) and its private helpers (filter toolbar, detail overlay, log streaming) verbatim; same import swaps. Its internal `new EventSource('/api/v1/events')` becomes `new EventSource(new URL('/api/v1/events', sharedApi().base).href)` — confirm `API` exposes its origin (field `base`; if absent, `configureSharedApi` also stores the origin and `sharedApi` returns `{ api, origin }`; adapt to the actual `@filelist/shared` shape after reading it).
  5. `web/settings.tsx`: replace `const api = new API(location.origin)` (line 7) with `const api = sharedApi()`.
  6. `web/src.tsx`: delete the moved blocks; `import { Icon } from './icons'; import { Downloads } from './downloads'; import { Jobs } from './jobs';` — its own `const api` stays (shell-only uses).
  7. `web/package.json` gains:
     ```json
     "exports": {
       ".": "./src.tsx",
       "./settings": "./settings.tsx",
       "./downloads": "./downloads.tsx",
       "./jobs": "./jobs.tsx",
       "./style.css": "./style.css"
     }
     ```
- [ ] **Step 2: Run the web suite** — `npm run test:clients`
  Expected: PASS — extraction is mechanical; existing tests pin webapp behavior.
- [ ] **Step 3: Write the guard + contract tests** (`web/chrome-guard.test.tsx`):

```tsx
import { render } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';
import { Downloads } from './downloads';
import { Jobs } from './jobs';
import { Settings } from './settings';

// Spec: Reuse boundary — shared components never render webapp shell.
describe.each([
  ['Downloads', <Downloads items={[]} onRefresh={() => {}} onPlay={() => {}} onRemove={async () => {}} onAction={() => {}} />],
  ['Jobs', <Jobs onError={() => {}} />],
])('%s renders no webapp chrome', (name, ui) => {
  it(`has no nav/sidebar/header/footer`, () => {
    const { container } = render(ui);
    expect(container.querySelectorAll('nav, header, footer, .sidebar')).toHaveLength(0);
  });
});
```

(Add a `Settings` row with the same assertion once fed a minimal `value`/`fields`; `Jobs` self-fetches — point `configureSharedApi('http://127.0.0.1:1')` first so its fetch fails quietly into `onError`.)

- [ ] **Step 4: Run** — `npm run test:clients && npm run build:web`
  Expected: PASS; web bundle builds.
- [ ] **Step 5: Commit**

```bash
git add web
git commit -m "refactor(web): extract icons, shared-api, Downloads, Jobs for desktop reuse"
```

---

### Task 9: Desktop workspace — shell, Server page, embedded build

**Files:**
- Create: `desktop/package.json`, `desktop/vite.config.ts`, `desktop/tsconfig.json`, `desktop/index.html`, `desktop/src/main.tsx`, `desktop/src/App.tsx`, `desktop/src/shell.css`, `desktop/src/pages/ServerPage.tsx`, `desktop/src/lib/state.ts`
- Modify: root `package.json` (workspace + `build:desktop`), `internal/gui/assets.go` (embed static), Makefile `web`→`desktop-assets` chain

**Interfaces:**
- Consumes: Task 6 (`app.Event.Emit("server:state", StateEvent{State, Error, Address})`), Task 8 (`@filelist/web/downloads` etc.).
- Produces: `StateEvent` TS type `{ state: 'stopped'|'starting'|'running'|'stopping'|'failed'; error?: string; address?: string }`; `useServerState(): StateEvent` (subscribes via `@wailsio/runtime` `Events.On('server:state')`, seeded by the emitted initial state).

- [ ] **Step 1: Scaffold.** `desktop/package.json`:

```json
{
  "name": "@filelist/desktop",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": { "test": "vitest run", "build": "tsc --noEmit && vite build" },
  "dependencies": {
    "@filelist/shared": "0.1.0",
    "@filelist/web": "0.1.0",
    "@wailsio/runtime": "latest",
    "preact": "10.29.8"
  },
  "devDependencies": {
    "@preact/preset-vite": "2.10.2",
    "happy-dom": "^20.11.12",
    "typescript": "5.9.2",
    "vite": "7.3.6",
    "vitest": "4.1.10"
  }
}
```

Root `package.json`: add `"desktop"` to workspaces, `"build:desktop": "npm run build -w @filelist/desktop"`. `desktop/vite.config.ts` mirrors `web/vite.config.ts` with `outDir: '../internal/gui/static'`, `emptyOutDir: true`, and `server: { proxy: { '/api': 'http://127.0.0.1:8097' } }` for dev. `desktop/tsconfig.json` copies `web/tsconfig.json`.

- [ ] **Step 2: Shell.** `desktop/src/App.tsx`:

```tsx
import { useState } from 'preact/hooks';
import { useServerState } from './lib/state';
import { ServerPage } from './pages/ServerPage';
import { DownloadsPage } from './pages/DownloadsPage';
import { JobsPage } from './pages/JobsPage';
import { SettingsPage } from './pages/SettingsPage';
import './shell.css';
import '@filelist/web/style.css';

type View = 'server' | 'downloads' | 'jobs' | 'settings';
const sections: { id: View; label: string }[] = [
  { id: 'server', label: 'Server' },
  { id: 'downloads', label: 'Downloads' },
  { id: 'jobs', label: 'Jobs' },
  { id: 'settings', label: 'Settings' },
];

export function App() {
  const [view, setView] = useState<View>('server');
  const state = useServerState();
  return (
    <div class="shell">
      <nav class="shell-nav" aria-label="Sections">
        {sections.map(s => (
          <button key={s.id} class={view === s.id ? 'active' : ''} onClick={() => setView(s.id)}>
            <span class={`dot dot-${state.state}`} aria-hidden="true" />
            {s.label}
          </button>
        ))}
      </nav>
      <div class="shell-main">
        <header class="shell-header">
          <h1>FileList Streaming</h1>
          <span class={`pill pill-${state.state}`}>
            <span class="dot dot-${state.state}" aria-hidden="true" />
            {state.state === 'running' ? `Running${state.address ? ` · ${state.address}` : ''}`
              : state.state === 'failed' ? 'Failed' : state.state[0].toUpperCase() + state.state.slice(1)}
          </span>
        </header>
        <main>
          {view === 'server' && <ServerPage />}
          {view === 'downloads' && <DownloadsPage />}
          {view === 'jobs' && <JobsPage />}
          {view === 'settings' && <SettingsPage />}
        </main>
      </div>
    </div>
  );
}
```

(`shell.css` defines only shell chrome — sidebar, header, pill, status dots with the web tokens: `--panel`, `--teal`, ink `#071014`; the shared webapp classes come from `@filelist/web/style.css` so reused views render identically.)

`desktop/src/lib/state.ts`:

```ts
import { useEffect, useState } from 'preact/hooks';
import { Events } from '@wailsio/runtime';

export type StateEvent = { state: 'stopped' | 'starting' | 'running' | 'stopping' | 'failed'; error?: string; address?: string };
let seeded: StateEvent = { state: 'stopped' };
export function seedServerState(s: StateEvent) { seeded = s; } // called once at boot from Go-side initial emit

export function useServerState(): StateEvent {
  const [state, setState] = useState<StateEvent>(seeded);
  useEffect(() => {
    const off = Events.On('server:state', (e: any) => setState(e.data as StateEvent));
    return () => { (off as any)?.(); };
  }, []);
  return state;
}
```

- [ ] **Step 3: Server page (status card + autostart + details).** `ServerPage.tsx` consumes bindings (Task 10): status dot, `Running on {address}` / `Stopped` / `Failed — {error}`, Start/Stop button (`disabled` while `starting|stopping`), *Open web UI*, *Start at login* toggle bound to `AutostartStatus`/`EnableAutostart`/`DisableAutostart` (shows the returned error inline on failure; toggle reflects the read-back value, never optimistic), details row (version via `composition.Version` exposed through `SystemInfo` binding, data dir with *Change…*/*Open*, logs *Open*). Write the component against the Task 10 binding names and mark the task as paired.

- [ ] **Step 4: Pages for reused views.** `DownloadsPage.tsx`:

```tsx
import { useEffect, useRef, useState } from 'preact/hooks';
import { Downloads, reconcileDownloads, captureDownloadAnchor, restoreDownloadAnchor } from '@filelist/web/downloads';
import { configureSharedApi, sharedApi } from '@filelist/web/shared-api';
import { useServerState } from '../lib/state';

export function DownloadsPage() {
  const server = useServerState();
  const [items, setItems] = useState<any[]>([]);
  const anchor = useRef<{ id?: string }>({});
  useEffect(() => { configureSharedApi('http://127.0.0.1:8097'); }, []);
  useEffect(() => {
    if (server.state !== 'running') return;
    let stopped = false;
    const load = async () => {
      const a = anchor.current;
      try {
        const incoming = (await sharedApi().downloads()).items;
        setItems(cur => reconcileDownloads(cur, incoming));
        anchor.current = a;
      } catch { /* surfaced as empty state below */ }
    };
    void load();
    const t = setInterval(load, 3000);
    return () => { stopped = true; clearInterval(t); };
  }, [server.state]);
  if (server.state !== 'running') {
    return <section class="empty-state"><h2>Server is {server.state}</h2><p>Start the server to see downloads.</p></section>;
  }
  return <Downloads items={items} onRefresh={() => {}} onPlay={() => { /* Task 10: open watch URL in browser */ }} onRemove={async d => { await sharedApi().deleteDownload(d.id); }} onAction={async (d, a) => { await sharedApi().call(`/downloads/${encodeURIComponent(d.id)}/${a}`, { method: 'POST' }); }} />;
}
```

`JobsPage.tsx`: `<Jobs onError={...} />` + stopped-state wrapper + `configureSharedApi` once at app boot (move the call into `main.tsx` so all pages share it). `SettingsPage.tsx` in Task 10.

- [ ] **Step 5: Build + wire embed.** `npm install && npm run build:desktop`; `internal/gui/assets.go` serves `static`; `go build ./...`; `go run ./cmd/server` shows the shell (Server page skeleton), downloads/jobs render shared views against a manually started server.

Run: `npm run test -w @filelist/desktop && GOCACHE=/tmp/filelist-streaming-go-cache go build ./...`
Expected: PASS / build clean.

- [ ] **Step 6: Commit**

```bash
git add desktop package.json package-lock.json internal/gui/assets.go
git commit -m "feat(desktop): shell with server/downloads/jobs pages over shared webapp views"
```

---

### Task 10: Bindings service — server control, settings transport, autostart, data dir

**Files:**
- Modify: `internal/gui/bindings.go` (full implementation), `internal/adapters/httpapi/api.go` (export schema + redaction into `httpapi/schema.go`)
- Modify: `desktop/src/pages/ServerPage.tsx`, `desktop/src/pages/SettingsPage.tsx` (consume generated bindings)
- Test: `internal/gui/bindings_test.go`

**Interfaces:**
- Consumes: Task 2 (`LoadAt`, `Validate`, `RestartRequired`, `EnsureNativePathsWritable`), Task 7 (Supervisor), Task 4 (autostart), Task 1 (datadir).
- Produces (Wails service methods, generated TS via `wails3 generate bindings -d desktop/src/bindings`):
  - `ServerState() StateEvent`; `StartServer() error`; `StopServer() error`; `RestartServer() error`
  - `LoadSettings() SettingsView` — identical shape to GET /api/v1/settings (redacted secrets + `*Configured` flags + `settingsPath`)
  - `SaveSettings(next config.Settings) SaveResult{Saved bool; RestartRequired bool; AutoStarted bool}` — decode-checks `EnsureNativePathsWritable`, `settings.Save`, diff via `config.RestartRequired`; if required settings were incomplete before and are complete after and the server is stopped → `sup.Start()` async, `AutoStarted=true` (spec: Settings transport)
  - `SettingsSchema() []SchemaField` — extracted exported `httpapi.SettingsSchema()`
  - `AutostartStatus() (bool, error)`; `EnableAutostart() error`; `DisableAutostart() error` — exe path = `os.Executable()`, args = `["--minimized", "--data-dir", resolvedDir]`
  - `DataDirInfo() (dir string, source string)`; `ChangeDataDir(newDir string) error` — stop-if-running (remember), `datadir.Relocate`, reload `config.LoadAt(newDir/settings.json)` into supervisor + bindings, restart-if-was-running
  - `OpenPath(kind string) error` (logs|data) via OS opener; `OpenWebUI() error` opens `http://127.0.0.1:<port>` in the default browser
  - `Quit() error`

- [ ] **Step 1: Extract httpapi schema + redaction.** Move `settingsSchema`'s field builder (api.go:204-261) into `internal/adapters/httpapi/schema.go` as `func SettingsSchema() []SchemaField` and `redactedSettings`/`settingsView` as exported `RedactedSettings(v config.Settings, path string) SettingsView`; handlers delegate. Run `go test ./internal/adapters/httpapi/` — PASS.

- [ ] **Step 2: Write failing bindings tests** (composition fakes as in Task 7):

```go
func TestSaveSettingsCompletingRequiredAutoStarts(t *testing.T) {
	// store with missing required settings; supervisor started with CanStart
	// passing after save; assert SaveResult.AutoStarted and state → running.
}
func TestSaveSettingsMirrorsHTTPContract(t *testing.T) {
	// bad engine path → error mentioning the path; restartRequired true on
	// listener change; secrets preserved on empty submission (Save's merge).
}
func TestAutostartBindingUsesRealOSState(t *testing.T) {
	// with temp-dir injectors: EnableAutostart → Enabled() true.
}
func TestChangeDataDirStopsMovesRestarts(t *testing.T) {
	// running fake server → dir changed, pointer written, state running again.
}
```

- [ ] **Step 3: Implement** `bindings.go` per the Interfaces contract (composition.New via supervisor's factory; settings swap behind a mutex shared with the supervisor's factory closure so post-relocation Starts use the new store).

- [ ] **Step 4: Generate TS bindings and wire pages**

```bash
wails3 generate bindings -d desktop/src/bindings -ts
```

`SettingsPage.tsx`: loads via `Bindings.LoadSettings` + `SettingsSchema`, renders `@filelist/web/settings`'s `Settings` with `onSaved` → `SaveSettings` (show `restartRequired` as an inline "Restart to apply" button → `RestartServer`); renders the missing-required banner (from `ServerState()` + save errors) deep-linking Tracker-tab focus; when `server.state !== 'running'`, renders the "start the server to run tests" note above the reused component. `ServerPage.tsx` from Task 9 steps 3 wires its buttons to the real bindings.

- [ ] **Step 5: Verify end-to-end on this machine** — `go run ./cmd/server` (config complete) → status runs, Start/Stop toggles, autostart toggle flips the real LaunchAgent file, settings save round-trips with secrets intact (verify `data/settings.json`), Test tab errors while stopped with the banner shown.

Run: `GOCACHE=/tmp/filelist-streaming-go-cache go test ./internal/gui/ -race && npm run test -w @filelist/desktop`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gui internal/adapters/httpapi desktop
git commit -m "feat(gui): bindings for server control, settings, autostart, data relocation"
```

---

### Task 11: Tray controller — icons, menu, state refresh

**Files:**
- Create: `internal/gui/tray.go`, `internal/gui/assets/tray-{running,stopped,failed}.png` (committed), `tools/make_tray_icons.py`
- Modify: `internal/gui/runner.go` (wire `newTray`), `internal/gui/assets.go` (embed tray PNGs)

**Interfaces:**
- Consumes: Task 7 states; Task 4 autostart (menu checkbox).
- Produces: `type trayController struct{…}`; `func newTray(app, win, sup, settings) *trayController`; `(t) Refresh(s State)` — swaps icon + rebuilds menu (Wails v3 practice: `SetMenu`, no partial updates). Menu: *Open* (show window), *Start server*/*Stop server* (label by state; disabled while transitioning), *Open web UI*, *Start at login* (checkbox → autostart.Enable/Disable, reflects `Enabled()` read-back), *Quit* (`app.Quit()`).

- [ ] **Step 1: Generate the three state icons (one-time, committed).** `tools/make_tray_icons.py` (Pillow): load `clients/tizen/icon.png`, square-crop, resize to 16/24/32/64 px; `running` = as-is; `stopped` = grayscale; `failed` = grayscale + 6 px red (#e5484d) dot bottom-right at 64 px. Run it, commit outputs to `internal/gui/assets/`.
- [ ] **Step 2: Implement.**

```go
func (t *trayController) Refresh(s State) {
	t.app.RunOnMain(func() {
		t.systray.SetIcon(trayIconFor(s)) // embedded bytes
		menu := t.app.NewMenu()
		menu.Add("Open FileList Streaming").OnClick(func(*application.ContextMenuContextData) { t.win.Show() })
		if s == StateRunning {
			menu.Add("Stop server").OnClick(func(*application.ContextMenuContextData) { _ = t.sup.Stop() })
		} else {
			menu.Add("Start server").OnClick(func(*application.ContextMenuContextData) { _ = t.sup.Start() })
		}
		menu.Add("Open web UI").OnClick(func(*application.ContextMenuContextData) { _ = t.bindings.OpenWebUI() })
		autostartItem := menu.Add("Start at login")
		autostartItem.Callback = func(*application.ContextMenuContextData) { toggleAutostart(t.settings) }
		// set checked from autostart.Enabled() read-back before SetMenu
		menu.Add("Quit").OnClick(func(*application.ContextMenuContextData) { t.app.Quit() })
		t.systray.SetMenu(menu)
	})
}
```

(Adapt the exact menu-item API to the pinned beta — `menu.Add(...).OnClick(...)` vs `Callback`; the contract above is fixed, the spelling is verified in Step 3.) Left-click `t.systray.OnClick(func() { t.win.Show() })` (Windows/Linux); macOS uses the menu.

- [ ] **Step 3: Manual verification on macOS** — tray icon appears; toggling the server flips teal↔gray; menu items work; *Start at login* creates `~/Library/LaunchAgents/com.filelist-streaming.plist`; Quit exits (lock released).
- [ ] **Step 4: Commit**

```bash
git add internal/gui tools/make_tray_icons.py
git commit -m "feat(gui): tray with state icons, menu, autostart checkbox"
```

---

### Task 12: Data-dir relocation UI flow

**Files:**
- Modify: `desktop/src/pages/ServerPage.tsx` (details row: *Change…* dialog, *Open*)
- Test: `desktop/src/data-dir.test.tsx`

- [ ] **Step 1: Failing vitest** — component with mocked bindings: entering a non-empty dir shows the refusal error text; a valid change calls `ChangeDataDir` and shows the resolved new path from `DataDirInfo()`.

- [ ] **Step 2: Implement.** Dialog with path input + warning "The server will restart; your data moves to the new location."; on submit call `ChangeDataDir`, display backend errors verbatim (rollback already handled Go-side); *Open* buttons call `OpenPath('logs'|'data')`.

- [ ] **Step 3: Run** — `npm run test -w @filelist/desktop` PASS; manual: relocate on macOS to a second dir, verify files moved, pointer written, server restarted, GUI shows new path.

- [ ] **Step 4: Commit**

```bash
git add desktop
git commit -m "feat(desktop): data-dir change flow with move + restart"
```

---

### Task 13: Icons, packaging, Makefile

**Files:**
- Modify: `Makefile` (build, build-all, build-arm64, desktop assets chain, syso generation)
- Create: `build/windows/manifest.xml` (Task 6), `internal/gui/static` (built)

- [ ] **Step 1: Makefile rewrite of build targets.** Keep `web` as-is; add `desktop-assets` (dockerized like `web`, running `npm run build:desktop`); `build` = host cgo build with `-tags production` (plus `-tags gtk3` on Linux hosts), ldflags `-H=windowsgui` only for windows outputs; `build-arm64` = `wails3 build` linux/arm64 through the cross image: run `wails3 task setup:docker` once, then verify `wails3 build -s -tags "production gtk3" GOOS=linux GOARCH=arm64` produces a working binary, and encode that exact invocation; `build-all` = the five GUI-capable targets via the same path (windows ×2 cgo-free straight `go build` with syso + `-H windowsgui`; darwin ×2 via `wails3 build` on a mac host / CI; linux ×2 via cross image) **plus** the pure `linux-armv7` target: `CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build` (build tags already exclude the GUI).
- [ ] **Step 2: Windows .syso.** Before each windows build: `wails3 generate syso -icon build/appicon.png -manifest build/windows/manifest.xml -out rsrc_windows_amd64.syso` (and arm64 variant); verify with `GOOS=windows go build` that the exe carries the icon (Explorer check on a Windows box, or `python3 -c "…" PE parse` in tools tests — add a tools test asserting the syso exists post-build).
- [ ] **Step 3: macOS .app wrapper.** `wails3 package` (darwin) produces
  `FileList Streaming.app` embedding `build/appicon.png` (icns) and
  `build/darwin/Info.plist`. The bundle binary's CWD is undefined, so the
  .app runs the server binary through a tiny launcher (script or
  `Contents/MacOS` shim) that passes `--data-dir` pointing beside the .app
  (spec: Data directory, macOS .app); fall back to
  `~/Library/Application Support/FileListStreaming` when that dir is not
  writable. Verify: copy the .app to /Applications, launch, and confirm the
  data dir lands beside the .app; launch the raw binary from a terminal and
  confirm it still uses `data/` next to it.
- [ ] **Step 4: Verify locally** — `make build` runs and launches with dock icon; `make check` green.

Run: `make check`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add Makefile build
git commit -m "build: cgo-capable build matrix, syso generation, desktop assets"
```

---

### Task 14: CI and release workflows

**Files:**
- Modify: `.github/workflows/ci.yml` (backend job: apt webkit deps), `.github/workflows/release.yml` (servers matrix: runner split)

- [ ] **Step 1: ci.yml.** In the `backend` job before tests:

```yaml
      - name: Install WebKit/GTK dev packages (GUI cgo)
        run: sudo apt-get update && sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev
```

`clients`/`tooling` jobs unchanged; add `npm run test -w @filelist/desktop` to the clients job's test step.

- [ ] **Step 2: release.yml.** Matrix gains `runner` + `cgo` fields: windows (ubuntu-latest, cgo-free, syso step), darwin-amd64/arm64 (macos-latest, `wails3 build`, then universal .app via `lipo` + `wails3 package`), linux-amd64/arm64 (ubuntu-latest + `wails3 task setup:docker` cross), linux-armv7 (ubuntu-latest, `CGO_ENABLED=0`, unchanged). SBOM/checksum/attestation/publish jobs untouched (they consume artifacts).
- [ ] **Step 3: Verify** — push to a branch, confirm workflow_dispatch run builds all seven artifacts; download the darwin one and confirm the .app opens with the icon.
- [ ] **Step 4: Commit**

```bash
git add .github/workflows
git commit -m "ci: per-OS runner matrix for GUI builds, webkit dev deps, desktop tests"
```

---

### Task 15: Deployment continuity (unit, bootstrap, pi-deploy)

**Files:**
- Modify: `deploy/systemd/filelist-streaming.service:12`, `deploy/bootstrap-server.sh:39-42`, `deploy/pi-deploy.sh` (preflight before the binary install)
- Test: `tools/tests/` packaging tests if they assert unit contents

- [ ] **Step 1: Unit file.** `ExecStart=/usr/local/bin/filelist-streaming serve --data-dir /var/lib/filelist-streaming/data` (explicit `serve` — bare must never mean GUI on a server; explicit data dir keeps today's `/var/lib/filelist-streaming/data` path). Everything else untouched.
- [ ] **Step 2: bootstrap-server.sh.** Append to all four `packages=` lists: `libgtk-3-0 libwebkit2gtk-4.1-0 libayatana-appindicator3-1` (apt); `gtk3 webkit2gtk4.1 libayatana-appindicator-gtk3` (dnf); `gtk3 webkit2gtk-4.1 libayatana-appindicator` (pacman); zypper equivalents resolved by running `zypper search webkit2gtk appindicator` — if a name differs, fix the list in this step so `--dry-run` output lists a correct set.
- [ ] **Step 3: pi-deploy.sh preflight.** Before `scp "$binary"`, add:

```sh
if ! ssh "$host" "ldconfig -p 2>/dev/null | grep -q libwebkit2gtk-4.1.so.0"; then
	echo "The GUI-capable binary needs WebKitGTK 4.1 on $host." >&2
	echo "Run deploy/bootstrap-server.sh, or: sudo apt-get install -y libgtk-3-0 libwebkit2gtk-4.1-0 libayatana-appindicator3-1" >&2
	exit 1
fi
```

deploy-pi stays package-install-free (its header contract).
- [ ] **Step 4: Verify** — `make bootstrap-server-dry-run` shows the new packages; run the preflight against a host lacking the lib to confirm the abort message; `make deploy-pi` end-to-end to the Pi (or a staging box) and `systemctl status filelist-streaming` active.

- [ ] **Step 5: Commit**

```bash
git add deploy
git commit -m "deploy: explicit serve unit, GUI runtime packages, WebKitGTK preflight"
```

---

### Task 16: Documentation

**Files:**
- Modify: `docs/INSTALLATION.md`, `docs/DEVELOPMENT.md`, `docs/CONFIGURATION.md`, `README.md`

- [ ] **Step 1: INSTALLATION.md** — new "Desktop app" section: per-OS artifact table, WebKitGTK runtime requirement for headless Linux `serve` (exact package names per distro), WebView2 note for Windows, macOS Gatekeeper one-time bypass (`xattr -cr` / right-click Open), autostart behavior per OS (where the entry lives, starts minimized to tray), data-dir default and relocation. Existing headless/systemd sections gain the `serve` subcommand and the runtime-dep note.
- [ ] **Step 2: DEVELOPMENT.md** — wails3 CLI install + pin, `npm run build:desktop`, local run (`go run ./cmd/server` opens GUI), cgo prerequisites per host OS, cross-build via `wails3 task setup:docker`, armv7 pure-build explanation.
- [ ] **Step 3: CONFIGURATION.md** — "Data directory" section: resolution order, relocation flow, pointer file, `--data-dir`; note env var precedence unchanged.
- [ ] **Step 4: README.md** — add GUI mention + screenshot placeholder slot (no placeholder file in repo; drop the section if no screenshot).
- [ ] **Step 5: Commit**

```bash
git add docs README.md
git commit -m "docs: desktop GUI usage, autostart, data dir, build prerequisites"
```

---

### Task 17: Full verification pass

**Files:** none (verification only)

- [ ] **Step 1:** `make check` — all Go tests (incl. `-race`), Python packaging tests, vet, whitespace.
- [ ] **Step 2:** `npm run test:clients && npm run test -w @filelist/desktop` — webapp + desktop suites.
- [ ] **Step 3:** Manual per-platform checklist (from spec Testing): tray states, close-to-tray, `--minimized` boot, autostart-at-login, `serve` console output on Windows, missing-config → setup → auto-start flow, downloads/jobs identical to webapp, data relocation, Pi deploy end-to-end.
- [ ] **Step 4:** Fix anything found; commit fixes individually.

- [ ] **Commit:** any fixes; tag-ready state.
