package gui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/httpapi"
	"github.com/mihaiflorentin88/torrent-tv/internal/composition"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/autostart"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/datadir"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/listenaddr"
)

// StateEvent is the server lifecycle payload the Wails runner emits on the
// 'server:state' topic and the ServerState binding returns. The TS mirror is
// desktop/src/lib/state.ts.
type StateEvent struct {
	State   State  `json:"state"`
	Error   string `json:"error,omitempty"`
	Address string `json:"address,omitempty"`
}

// SaveResult is the SaveSettings response: whether the settings persisted,
// whether any restart-required field changed, and whether the save completed
// setup and auto-started the server.
type SaveResult struct {
	Saved           bool `json:"saved"`
	RestartRequired bool `json:"restartRequired"`
	AutoStarted     bool `json:"autoStarted"`
}

// LogTail is one ReadLogs page: the raw lines read from the GUI session's
// server log, the byte offset the next call must pass, and the log's
// current size.
type LogTail struct {
	Lines      []string `json:"lines"`
	NextOffset int64    `json:"nextOffset"`
	Size       int64    `json:"size"`
}

// Bindings is the Wails service behind the desktop pages: server control,
// the settings transport, autostart, and data-dir helpers. The runner
// (Task 6) injects the store and supervisor it shares with the server;
// wails wraps this struct by reflection at runtime, so it stays plain Go.
//
// The store/data-dir trio is a mutex-guarded holder: ChangeDataDir swaps
// all three after a successful relocation, and every reader goes through
// snapshot, so the supervisor's CanStart/factory closures (wired by
// wireSupervisor) always consult the CURRENT location.
type Bindings struct {
	mu sync.Mutex

	settings      *config.Store
	dataDir       string
	dataDirSource string
	// sup is the server supervisor. The runner installs it once after
	// wireSupervisor and every binding reads it, so access goes through
	// the same holder mutex as the settings trio — desktop supervisor
	// access is synchronized, never a bare field read.
	sup *Supervisor
	// relocating guards the move window of ChangeDataDir: wireSupervisor's
	// CanStart refuses and the SaveSettings completing-save auto-start
	// defers while it is set, so no Start can race the move (see
	// ChangeDataDir).
	relocating bool

	// Test seams; nil falls back to the real platform implementation.
	exePathFn        func() (string, error)
	autostartEnable  func(autostart.Options) error
	autostartDisable func() error
	autostartEnabled func() (bool, error)
	revealFn         func(path string) error
	openURLFn        func(url string) error
	quitFn           func()
	// Relocation seams (nil = the real datadir calls).
	relocateFn   func(exePath, from, to string) error
	remapPathsFn func(path, from, to string) error
}

// ServerState reports the current lifecycle state for page mounts that miss
// the last 'server:state' event. The address gets the same DisplayAddress
// treatment as the event path: a page mounted before the first state event
// would otherwise render a hostless "http://:8097".
func (b *Bindings) ServerState() StateEvent {
	sup := b.supervisor()
	ev := StateEvent{State: sup.State(), Address: listenaddr.DisplayAddress(sup.Address())}
	if err := sup.Error(); err != nil {
		ev.Error = err.Error()
	}
	return ev
}

// setSupervisor installs the supervisor after wiring; supervisor returns
// it under the holder mutex.
func (b *Bindings) setSupervisor(sup *Supervisor) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sup = sup
}

func (b *Bindings) supervisor() *Supervisor {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sup
}

// StartServer brings the server up (refused while required settings are
// missing; that shows setup, not failure).
func (b *Bindings) StartServer() error { return b.supervisor().Start() }

// StopServer gracefully shuts the running server down.
func (b *Bindings) StopServer() error { return b.supervisor().Stop() }

// RestartServer applies restart-required settings: Stop then Start.
func (b *Bindings) RestartServer() error { return b.supervisor().Restart() }

// LoadSettings returns the settings exactly as GET /api/v1/settings serves
// them: secrets blanked, Configured flags, settings file path.
func (b *Bindings) LoadSettings() httpapi.SettingsView {
	store, _, _ := b.snapshot()
	v := store.Get()
	return httpapi.RedactedSettings(v, store.Path())
}

// SaveSettings mirrors the HTTP PUT /api/v1/settings contract: native-path
// probe, secrets-preserving save, restart-required diff. A save that
// completes the required settings while the server is stopped auto-starts
// it (the GUI form of "starts automatically once configuration is set") —
// unless a data-dir relocation is in flight, whose guard keeps the
// auto-start from racing the move; the restarted change performs its own
// Start against the holder's (new) location.
func (b *Bindings) SaveSettings(next config.Settings) (SaveResult, error) {
	store, _, _ := b.snapshot()
	old := store.Get()
	wasIncomplete := len(store.MissingRequired()) > 0
	if err := config.EnsureNativePathsWritable(next.DownloadEngine, next.DownloadRoot, next.TorrentSessionDir); err != nil {
		return SaveResult{}, err
	}
	if err := store.Save(next); err != nil {
		return SaveResult{}, err
	}
	current := store.Get()
	result := SaveResult{Saved: true, RestartRequired: config.RestartRequired(old, current)}
	sup := b.supervisor()
	if wasIncomplete && len(store.MissingRequired()) == 0 && sup.State() == StateStopped && !b.relocatingServer() {
		go func() { _ = sup.Start() }()
		result.AutoStarted = true
	}
	return result, nil
}

// SettingsSchema returns the settings schema, identical to the HTTP
// /api/v1/settings/schema items.
func (b *Bindings) SettingsSchema() []httpapi.SchemaField {
	store, _, _ := b.snapshot()
	return httpapi.SettingsSchema(store)
}

// MissingRequired lists the required settings still absent; the Settings
// page banners it and deep-links the Tracker tab.
func (b *Bindings) MissingRequired() []string {
	store, _, _ := b.snapshot()
	return store.MissingRequired()
}

// Version reports the server version (composition.Version, ldflags-injected
// in release builds).
func (b *Bindings) Version() string { return composition.Version }

// AutostartStatus reads the OS launch-on-boot artifact back; the OS is the
// source of truth, never memory.
func (b *Bindings) AutostartStatus() (bool, error) {
	if b.autostartEnabled != nil {
		return b.autostartEnabled()
	}
	return autostart.Enabled()
}

// EnableAutostart registers launch-on-boot: the running executable with
// --minimized --data-dir <resolved data dir>, so launches never depend on a
// working directory.
func (b *Bindings) EnableAutostart() error {
	exe, err := b.exePath()
	if err != nil {
		return err
	}
	dir, _ := b.dataDirInfo()
	return b.enableAutostart(exe, dir)
}

// enableAutostart writes the launch-on-boot artifact for one explicit
// (exe, data dir) pair — ChangeDataDir's commit phase reuses it to
// re-register the entry against the NEW location after a move.
func (b *Bindings) enableAutostart(exe, dir string) error {
	if b.autostartEnable != nil {
		return b.autostartEnable(autostart.Options{ExePath: exe, Args: []string{"--minimized", "--data-dir", dir}})
	}
	return autostart.Enable(autostart.Options{ExePath: exe, Args: []string{"--minimized", "--data-dir", dir}})
}

// DisableAutostart removes the OS launch-on-boot artifact.
func (b *Bindings) DisableAutostart() error {
	if b.autostartDisable != nil {
		return b.autostartDisable()
	}
	return autostart.Disable()
}

// DataDirInfo returns the resolved data directory and where it came from
// ("flag", "pointer", or "default").
func (b *Bindings) DataDirInfo() (string, string) {
	return b.dataDirInfo()
}

// OpenPath reveals a well-known folder in the OS file manager: kind is
// "logs" (<data dir>/logs) or "data" (the data dir itself).
func (b *Bindings) OpenPath(kind string) error {
	dir, _ := b.dataDirInfo()
	if dir == "" {
		return errors.New("data directory is not resolvable yet")
	}
	var path string
	switch kind {
	case "logs":
		path = filepath.Join(dir, "logs")
	case "data":
		path = dir
	default:
		return fmt.Errorf("unknown path kind %q; want \"logs\" or \"data\"", kind)
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}
	return b.reveal(path)
}

// OpenWebUI opens the server's web surface in the default browser. The port
// follows the running (or most recently run) server; the loopback host is
// fixed: the web UI is this machine's window onto the same server.
func (b *Bindings) OpenWebUI() error {
	address := b.supervisor().Address()
	if address == "" {
		store, _, _ := b.snapshot()
		address = store.Get().ListenAddress
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("server address %q has no port to open", address)
	}
	return b.openURL("http://127.0.0.1:" + port)
}

// OpenURL opens an http(s) URL with a non-empty host in the default
// browser — the Downloads page's Play hands playback off to the web player
// this way, and the portal surfaces route their external links through it.
// The scheme allow-list keeps arbitrary content (file://, custom schemes,
// command strings) away from the OS opener, the host requirement keeps
// hostless http URLs from reaching the opener, and the URL travels as one
// argv element — never a shell string.
func (b *Bindings) OpenURL(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("refusing to open %q: only http and https URLs are allowed", rawURL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("refusing to open %q: a host is required", rawURL)
	}
	return b.openURL(parsed.String())
}

// ReadLogs returns the lines appended to the GUI session's server log
// (<data dir>/logs/server.jsonl, the same file OpenPath("logs") reveals)
// since the byte offset of the previous call. An offset past the log's
// size — truncation or rotation — restarts from the beginning. When the
// unread window exceeds logTailCap the newest logTailCap bytes are
// returned instead, starting just after the line the cap cuts into, so a
// viewer that polls rarely stays bounded. Lines are returned raw; JSONL
// rendering is the client's job. A line still being written is held back
// until it is complete.
func (b *Bindings) ReadLogs(offset int64) (LogTail, error) {
	dir, _ := b.dataDirInfo()
	if dir == "" {
		return LogTail{}, errors.New("data directory is not resolvable yet")
	}
	return readLogTail(filepath.Join(dir, "logs", "server.jsonl"), offset)
}

// Quit shuts the server down and exits the application. The runner may
// inject quitFn (the wails app's Quit) to run its own teardown instead.
func (b *Bindings) Quit() error {
	if b.quitFn != nil {
		b.quitFn()
		return nil
	}
	_ = b.supervisor().Stop()
	os.Exit(0)
	return nil
}

// exePath resolves the executable for autostart entries.
func (b *Bindings) exePath() (string, error) {
	if b.exePathFn != nil {
		return b.exePathFn()
	}
	return os.Executable()
}

// relocatingServer reports whether a ChangeDataDir is between Stop and its
// commit: CanStart and the SaveSettings auto-start edge defer on it so no
// Start races the move.
func (b *Bindings) relocatingServer() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.relocating
}

// snapshot returns the current settings store, data dir, and dir source
// under the same mutex ChangeDataDir commits its swap with. Everything
// that outlives a single call — the supervisor's CanStart and factory
// closures, the Wails method handlers — reads the holder through here.
func (b *Bindings) snapshot() (*config.Store, string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings, b.dataDir, b.dataDirSource
}

// settingsPathEnv keeps its historic precedence (spec: Data directory):
// when set, both serve and the GUI load the settings file it points at;
// otherwise the file lives at <resolved data dir>/settings.json.
const settingsPathEnv = config.EnvironmentPrefix + "SETTINGS_PATH"

// settingsPathFor applies that precedence to a data dir. The env var
// selects the settings file itself, so it keeps winning after a
// relocation: ChangeDataDir moves the data, never an env-pinned file.
func settingsPathFor(dir string) string {
	if p := os.Getenv(settingsPathEnv); strings.TrimSpace(p) != "" {
		return p
	}
	return filepath.Join(dir, "settings.json")
}

// dataDirInfo returns the injected data dir, resolving lazily when the
// runner did not inject one.
func (b *Bindings) dataDirInfo() (string, string) {
	_, dir, source := b.snapshot()
	if dir != "" {
		return dir, source
	}
	exe, err := b.exePath()
	if err != nil {
		return "", ""
	}
	dir, source, err = datadir.Resolve("", exe)
	if err != nil {
		return "", ""
	}
	return dir, source
}

// ChangeDataDir relocates the data directory to target (spec: Data
// directory): a running server is stopped first and its prior state
// remembered; datadir.Relocate moves the contents and writes the
// data.location pointer atomically (a non-empty or nested target is
// refused with the error the dialog shows verbatim; any move failure
// leaves the source untouched); the settings file's data-dir-anchored
// paths are remapped to the new location; the store and dir swap commits
// under the holder mutex; a stale autostart entry is re-registered against
// the new location (its --data-dir flag beats the pointer); the server
// restarts only if it was running before.
//
// Failure ordering: a failure before the move commits (validation, stop,
// Relocate itself with the source still present) restores the prior state —
// resume restarts the remembered server and surfaces the error verbatim.
// Once the move has committed (Relocate returned success, or the source is
// gone under an inside-Relocate failure such as a pointer-write error),
// there is no old location to serve from: every later failure — remap,
// store load, autostart re-registration — leaves the server STOPPED and
// surfaces the error verbatim; data and pointer already name the new
// location, so the next boot lands there.
//
// The whole call holds a relocating guard under the holder mutex:
// CanStart refuses with a relocation-in-progress error and the
// SaveSettings completing-save auto-start defers on the same guard, so no
// Start can slip in between Stop and the swap and serve (or write into) a
// directory being moved. The guard drops just before the restart: the swap
// has committed by then, so the restart — and any racing completing-save —
// lands on the new location through the holder.
//
// The single-instance lock is deliberately not migrated: gui.lock guards
// this process (its loopback show-listener is process-owned, not
// path-owned), and a same-volume rename carries the file along with the
// data anyway — a cross-volume move deletes it with the source. Migrating
// the lock mid-run would only race a concurrent second launch; a lock file
// left behind anywhere self-heals via the NotifyURL takeover check on the
// next boot.
func (b *Bindings) ChangeDataDir(target string) error {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return errors.New("new data directory is empty")
	}
	to, err := filepath.Abs(trimmed)
	if err != nil {
		return err
	}
	from, _ := b.dataDirInfo()
	if from == "" {
		return errors.New("data directory is not resolvable yet")
	}

	// Relocation guard: from here until the swap commits, no Start may
	// race the move. The defer covers every early return below.
	b.mu.Lock()
	b.relocating = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.relocating = false
		b.mu.Unlock()
	}()

	sup := b.supervisor()
	wasRunning := false
	switch sup.State() {
	case StateRunning:
		wasRunning = true
		if err := sup.Stop(); err != nil {
			return err
		}
	case StateStarting, StateStopping:
		return errors.New("server is transitioning; wait for it to stop or start before changing the data directory")
	}
	// resume restores the prior state on a pre-commit failure: the data is
	// still at from (Relocate refuses or fails without deleting it), so
	// restarting the remembered server against the old store is exactly
	// where things stood. If the source is gone the move already committed
	// inside a failing Relocate (e.g. the pointer write): the old store
	// path no longer exists, so the server stays stopped and the error
	// surfaces verbatim. A resume failure joins the relocation error as
	// the first message.
	resume := func(fail error) error {
		if wasRunning {
			if _, statErr := os.Stat(from); os.IsNotExist(statErr) {
				return fail
			}
			if err := b.supervisor().Start(); err != nil {
				return errors.Join(fail, err)
			}
		}
		return fail
	}

	exe, err := b.exePath()
	if err != nil {
		return resume(err)
	}
	relocate := datadir.Relocate
	if b.relocateFn != nil {
		relocate = b.relocateFn
	}
	if err := relocate(exe, from, to); err != nil {
		return resume(err)
	}

	// The move has committed: every failure from here on leaves the server
	// stopped (there is no old location to serve) and surfaces verbatim.
	settingsPath := settingsPathFor(to)
	remap := remapDataDirPaths
	if b.remapPathsFn != nil {
		remap = b.remapPathsFn
	}
	if err := remap(settingsPath, from, to); err != nil {
		return fmt.Errorf("remap settings paths to %s: %w", to, err)
	}
	store, err := config.LoadAt(settingsPath)
	if err != nil {
		return err
	}

	// Commit: the move is verified and the new store loaded. The holder
	// swap is the point of no return — the supervisor factory and CanStart
	// consult it, so the restart below serves the new location.
	b.mu.Lock()
	b.settings = store
	b.dataDir = to
	b.dataDirSource = "pointer"
	b.mu.Unlock()

	// The autostart entry pins --data-dir, and the flag beats the pointer:
	// a stale entry would boot the OLD location (recreated empty, fresh
	// database) while the data sits at the new one. Re-register against the
	// new dir during the commit; a failure surfaces verbatim and leaves the
	// server stopped.
	if enabled, err := b.AutostartStatus(); err != nil {
		return err
	} else if enabled {
		if err := b.enableAutostart(exe, to); err != nil {
			return err
		}
	}

	// Drop the guard before the restart: the swap has committed, so our
	// own Start must pass CanStart, and a racing completing-save now lands
	// on the new location through the holder.
	b.mu.Lock()
	b.relocating = false
	b.mu.Unlock()

	if wasRunning {
		return b.supervisor().Start()
	}
	return nil
}

// remappedPathKeys are the settings fields that may carry a path under the
// data dir. The GUI itself anchors databasePath, artworkCachePath, and
// torrentSessionDir there on first start; downloadRoot and the tool paths
// are user-chosen, and the prefix rewrite only fires when a value actually
// lives under the old location, so pointing them at the moved copy
// preserves the user's intent.
var remappedPathKeys = []string{
	"databasePath",
	"downloadRoot",
	"torrentSessionDir",
	"artworkCachePath",
	"subtitleCachePath",
	"ffprobePath",
	"ffmpegPath",
}

// remapDataDirPaths rewrites the settings file's old-dir-anchored path
// values after a committed move. Literally leaving them would have the
// restarted server create a fresh database at the abandoned path while the
// real one sits in the new location. The rewrite is raw JSON (tmp +
// rename, mirroring the store's own Save) so unknown future keys survive
// untouched; env-managed values are runtime-only and never read from the
// file. A missing file is fine (fresh dir, nothing anchored yet); an
// undecodable one is left for LoadAt to report.
func remapDataDirPaths(path, from, to string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil
	}
	changed := false
	for _, key := range remappedPathKeys {
		value, ok := fields[key].(string)
		if !ok {
			continue
		}
		if remapped, ok := remapUnder(value, from, to); ok {
			fields[key] = remapped
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// remapUnder moves one path value from under the old dir to the same spot
// under the new dir; ok reports whether it matched at all.
func remapUnder(value, from, to string) (string, bool) {
	if value == from {
		return to, true
	}
	if prefix := from + string(filepath.Separator); strings.HasPrefix(value, prefix) {
		return filepath.Join(to, strings.TrimPrefix(value, prefix)), true
	}
	return "", false
}

func (b *Bindings) reveal(path string) error {
	if b.revealFn != nil {
		return b.revealFn(path)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start()
	case "windows":
		return exec.Command("explorer", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

func (b *Bindings) openURL(url string) error {
	if b.openURLFn != nil {
		return b.openURLFn(url)
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// logTailCap bounds one ReadLogs read: when the unread window exceeds it,
// the tail of the window is served (the newest lines are the ones a
// viewer follows), starting just after the line the cap cuts into.
const logTailCap = 256 << 10 // 256 KiB

// readLogTail reads the complete lines of path from byte offset,
// enforcing the logTailCap window and the truncation reset the ReadLogs
// binding promises. It is a free function so tests can drive a temp file
// without the data-dir resolution.
func readLogTail(path string, offset int64) (LogTail, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No log yet (fresh data dir): an empty tail, not an error.
			return LogTail{Lines: []string{}}, nil
		}
		return LogTail{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return LogTail{}, err
	}
	size := info.Size()
	if offset > size || offset < 0 {
		offset = 0 // truncated or rotated: restart from the beginning
	}
	start := offset
	if size-start > logTailCap {
		start = size - logTailCap
	}
	data := make([]byte, size-start)
	n, err := f.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return LogTail{}, err
	}
	data = data[:n]
	if start != offset { // the cap cut into a line: skip its partial head
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			start += int64(i) + 1
			data = data[i+1:]
		} else {
			// One line longer than the whole window: nothing to show.
			data, start = nil, size
		}
	}
	// Hold back a trailing partial line: the append in progress is sent
	// whole by the next call.
	if cut := bytes.LastIndexByte(data, '\n') + 1; cut != len(data) {
		data = data[:cut]
	}
	lines := []string{}
	if len(data) > 0 {
		lines = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	// start names the first kept byte (past the snapped partial head, if
	// any), so the next offset is start plus everything returned.
	return LogTail{Lines: lines, NextOffset: start + int64(len(data)), Size: size}, nil
}
