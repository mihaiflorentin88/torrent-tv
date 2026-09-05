package gui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/platform/autostart"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/datadir"
)

// relocatableBindings wires a Bindings the way the runner does, but with
// caller-controlled paths: the settings store loads from oldDir, the exe
// seam points at a fake executable (so the data.location pointer lands in
// exeDir), and factoryCalls records every (store path, data dir) pair the
// supervisor's factory consulted — the holder-swap observability the tests
// assert on.
type factoryProbe struct {
	mu    sync.Mutex
	calls []string
}

func (p *factoryProbe) record(storePath, dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, storePath+"|"+dir)
}

func (p *factoryProbe) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func relocatableBindings(t *testing.T, oldDir, exeDir string) (*Bindings, *Supervisor, *factoryProbe) {
	t.Helper()
	store := completeStore(t, oldDir)
	probe := &factoryProbe{}
	exe := filepath.Join(exeDir, "torrent-tv")
	b := &Bindings{settings: store, dataDir: oldDir, dataDirSource: "default"}
	b.exePathFn = func() (string, error) { return exe, nil }
	sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: store})
	sup.appFactory = func() (appLike, error) {
		s, d, _ := b.snapshot()
		probe.record(s.Path(), d)
		return &fakeApp{serve: make(chan error), closed: make(chan struct{})}, nil
	}
	b.sup = sup
	return b, sup, probe
}

// runningServer starts the supervisor and waits until the fake app serves;
// it returns the running fake for Close assertions.
func runningServer(t *testing.T, sup *Supervisor) {
	t.Helper()
	if err := sup.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForState(t, sup, StateRunning)
}

// TestChangeDataDirStopsMovesRestarts pins the full relocation sequence
// with a running server: Stop, move (old dir gone, contents at the target,
// pointer file written next to the exe), holder swap visible to the
// factory, then a restart — and the first app closed exactly once.
func TestChangeDataDirStopsMovesRestarts(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	exeDir := t.TempDir()
	os.WriteFile(filepath.Join(oldDir, "canary.txt"), []byte("moved with the data"), 0o600)

	b, sup, probe := relocatableBindings(t, oldDir, exeDir)
	runningServer(t, sup)

	if err := b.ChangeDataDir(newDir); err != nil {
		t.Fatalf("ChangeDataDir: %v", err)
	}
	waitForState(t, sup, StateRunning) // restarted

	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old data dir %s must be gone after the move", oldDir)
	}
	for _, name := range []string{"settings.json", "canary.txt"} {
		if _, err := os.Stat(filepath.Join(newDir, name)); err != nil {
			t.Fatalf("%s must have moved to the new dir: %v", name, err)
		}
	}
	pointer, err := os.ReadFile(datadir.PointerPath(filepath.Join(exeDir, "torrent-tv")))
	if err != nil {
		t.Fatalf("pointer file: %v", err)
	}
	if got, want := strings.TrimSpace(string(pointer)), newDir; got != want {
		t.Fatalf("pointer file names %q, want %q", got, want)
	}

	store, dir, source := b.snapshot()
	if store.Path() != filepath.Join(newDir, "settings.json") {
		t.Fatalf("swapped store must load from %s, got %s", filepath.Join(newDir, "settings.json"), store.Path())
	}
	if dir != newDir || source != "pointer" {
		t.Fatalf("DataDirInfo after swap = (%s, %s), want (%s, pointer)", dir, source, newDir)
	}
	if probe.count() != 2 {
		t.Fatalf("factory must run once per Start (stop, restart), got %d calls", probe.count())
	}
	if !strings.HasSuffix(probe.calls[1], "|"+newDir) {
		t.Fatalf("post-relocation factory call must consult the new dir, got %q", probe.calls[1])
	}
}

// TestChangeDataDirWhileStoppedSwapsWithoutLifecycle pins the stopped flow:
// no stop, no restart (factory runs zero times), yet the holder swap still
// happens so the next Start serves the new location.
func TestChangeDataDirWhileStoppedSwapsWithoutLifecycle(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	exeDir := t.TempDir()

	b, sup, probe := relocatableBindings(t, oldDir, exeDir)

	if err := b.ChangeDataDir(newDir); err != nil {
		t.Fatalf("ChangeDataDir: %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatalf("server must stay stopped, got %s", sup.State())
	}
	if probe.count() != 0 {
		t.Fatalf("a stopped server must not be started by the change, got %d factory calls", probe.count())
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old data dir %s must be gone after the move", oldDir)
	}
	store, dir, source := b.snapshot()
	if store.Path() != filepath.Join(newDir, "settings.json") || dir != newDir || source != "pointer" {
		t.Fatalf("holder swap = (%s, %s, %s), want new-dir store/pointer", store.Path(), dir, source)
	}
}

// TestChangeDataDirRefusalSurfacesVerbatim pins the refusal contract: a
// non-empty target is refused with datadir's exact error, the data stays at
// the old location, and a running server comes back to running.
func TestChangeDataDirRefusalSurfacesVerbatim(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	exeDir := t.TempDir()
	os.WriteFile(filepath.Join(newDir, "blocker.txt"), []byte("occupied"), 0o600)

	b, sup, probe := relocatableBindings(t, oldDir, exeDir)
	runningServer(t, sup)

	err := b.ChangeDataDir(newDir)
	want := fmt.Sprintf("target %s is not empty", newDir)
	if err == nil || err.Error() != want {
		t.Fatalf("ChangeDataDir error = %v, want %q", err, want)
	}
	waitForState(t, sup, StateRunning) // prior state restored

	if _, err := os.Stat(filepath.Join(oldDir, "settings.json")); err != nil {
		t.Fatalf("source data must stay untouched after a refusal: %v", err)
	}
	entries, err := os.ReadDir(newDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "blocker.txt" {
		t.Fatalf("target must remain exactly as it was, got %v (%v)", entries, err)
	}
	if probe.count() != 2 {
		t.Fatalf("factory must run twice (initial start, restore start), got %d", probe.count())
	}
}

// TestChangeDataDirRefusesEmptyTargetAndTransitioning pins the guard rails:
// a blank target and a transitioning server are refused without touching
// anything.
func TestChangeDataDirRefusesEmptyTargetAndTransitioning(t *testing.T) {
	oldDir := t.TempDir()
	exeDir := t.TempDir()
	newDir := t.TempDir()

	b, sup, probe := relocatableBindings(t, oldDir, exeDir)

	if err := b.ChangeDataDir("   "); err == nil || err.Error() != "new data directory is empty" {
		t.Fatalf("empty target error = %v", err)
	}

	sup.mu.Lock()
	sup.transition(StateStarting, nil)
	sup.mu.Unlock()
	if err := b.ChangeDataDir(newDir); err == nil || !strings.Contains(err.Error(), "transitioning") {
		t.Fatalf("transitioning error = %v", err)
	}
	if probe.count() != 0 || sup.State() != StateStarting {
		t.Fatalf("refusals must not touch the lifecycle (calls=%d, state=%s)", probe.count(), sup.State())
	}
	if _, err := os.Stat(filepath.Join(oldDir, "settings.json")); err != nil {
		t.Fatalf("data must stay put after refusals: %v", err)
	}
}

// TestChangeDataDirRemapsAnchoredSettings pins the settings remap: values
// the GUI anchored under the old dir move to the new location in the
// settings file and in the swapped store, while keys outside the old dir
// survive verbatim.
func TestChangeDataDirRemapsAnchoredSettings(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	exeDir := t.TempDir()

	b, _, _ := relocatableBindings(t, oldDir, exeDir)

	if err := b.ChangeDataDir(newDir); err != nil {
		t.Fatalf("ChangeDataDir: %v", err)
	}
	store, _, _ := b.snapshot()
	for field, want := range map[string]string{
		"DatabasePath":      filepath.Join(newDir, "filelist.db"),
		"DownloadRoot":      filepath.Join(newDir, "downloads"),
		"TorrentSessionDir": filepath.Join(newDir, "torrent-session"),
	} {
		var got string
		switch field {
		case "DatabasePath":
			got = store.Get().DatabasePath
		case "DownloadRoot":
			got = store.Get().DownloadRoot
		case "TorrentSessionDir":
			got = store.Get().TorrentSessionDir
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", field, got, want)
		}
	}
	if store.Get().FileListURL != config.Defaults().FileListURL {
		t.Fatalf("keys outside the old dir must stay untouched, got %q", store.Get().FileListURL)
	}
	persisted, err := os.ReadFile(filepath.Join(newDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), oldDir) {
		t.Fatalf("persisted settings must not reference the old dir: %s", persisted)
	}
}

// TestRemapDataDirPathsEdges pins the raw-file rewrite edges: a missing
// file is a no-op, an unknown key survives, a tool path outside the old dir
// stays, and the data-dir root itself (not just children) remaps.
func TestRemapDataDirPathsEdges(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "old")
	to := filepath.Join(dir, "new")

	if err := remapDataDirPaths(filepath.Join(dir, "missing.json"), from, to); err != nil {
		t.Fatalf("missing file must be a no-op, got %v", err)
	}

	path := filepath.Join(dir, "settings.json")
	body := fmt.Sprintf(`{
		"databasePath": %q,
		"downloadRoot": %q,
		"instanceName": "kept",
		"futureKey": {"nested": true}
	}`, from, "/srv/elsewhere")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := remapDataDirPaths(path, from, to); err != nil {
		t.Fatalf("remap: %v", err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(persisted)
	if !strings.Contains(text, fmt.Sprintf("%q", to)) {
		t.Fatalf("data-dir root value must remap to %q: %s", to, text)
	}
	if strings.Contains(text, "/srv/elsewhere") == false {
		t.Fatalf("path outside the old dir must survive: %s", text)
	}
	if !strings.Contains(text, `"instanceName": "kept"`) || !strings.Contains(text, `"futureKey"`) {
		t.Fatalf("unknown keys must survive the raw rewrite: %s", text)
	}
}

// TestChangeDataDirRefusesNestedTarget pins the bindings-level inheritance
// of datadir's self-copy guard: a target inside the current data dir is
// refused with datadir's error, before anything moves.
func TestChangeDataDirRefusesNestedTarget(t *testing.T) {
	oldDir := t.TempDir()
	exeDir := t.TempDir()

	b, sup, probe := relocatableBindings(t, oldDir, exeDir)

	err := b.ChangeDataDir(filepath.Join(oldDir, "nested"))
	if err == nil || !strings.Contains(err.Error(), "is inside the current data directory") {
		t.Fatalf("nested target error = %v", err)
	}
	if sup.State() != StateStopped || probe.count() != 0 {
		t.Fatalf("refusal must not touch the lifecycle (calls=%d, state=%s)", probe.count(), sup.State())
	}
	if _, err := os.Stat(filepath.Join(oldDir, "settings.json")); err != nil {
		t.Fatalf("source must stay untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(oldDir, "nested")); !os.IsNotExist(err) {
		t.Fatal("nested target must not be created")
	}
}

// TestChangeDataDirReregistersAutostart pins the stale-entry fix: the
// autostart artifact pins --data-dir and the flag beats the pointer, so a
// relocation with autostart enabled must re-register against the NEW dir;
// with autostart disabled nothing may be written.
func TestChangeDataDirReregistersAutostart(t *testing.T) {
	oldDir := t.TempDir()
	midDir := t.TempDir()
	finalDir := t.TempDir()
	exeDir := t.TempDir()
	exe := filepath.Join(exeDir, "torrent-tv")

	var mu sync.Mutex
	var registrations []autostart.Options
	enabled := false
	b, sup, _ := relocatableBindings(t, oldDir, exeDir)
	b.autostartEnabled = func() (bool, error) { mu.Lock(); defer mu.Unlock(); return enabled, nil }
	b.autostartEnable = func(o autostart.Options) error {
		mu.Lock()
		defer mu.Unlock()
		registrations = append(registrations, o)
		return nil
	}
	runningServer(t, sup)

	// Phase 1: autostart off — the relocation must not touch it.
	if err := b.ChangeDataDir(midDir); err != nil {
		t.Fatalf("relocation (autostart off): %v", err)
	}
	waitForState(t, sup, StateRunning)
	mu.Lock()
	if len(registrations) != 0 {
		mu.Unlock()
		t.Fatalf("disabled autostart must not be re-registered, got %d writes", len(registrations))
	}
	mu.Unlock()

	// Phase 2: autostart on — the next relocation re-registers with the
	// new dir.
	enabled = true
	if err := b.ChangeDataDir(finalDir); err != nil {
		t.Fatalf("relocation (autostart on): %v", err)
	}
	waitForState(t, sup, StateRunning)
	mu.Lock()
	defer mu.Unlock()
	if len(registrations) != 1 {
		t.Fatalf("enabled autostart must re-register exactly once, got %d", len(registrations))
	}
	got := registrations[0]
	if got.ExePath != exe {
		t.Fatalf("re-registration exe = %q, want %q", got.ExePath, exe)
	}
	wantArgs := []string{"--minimized", "--data-dir", finalDir}
	if !reflect.DeepEqual(got.Args, wantArgs) {
		t.Fatalf("re-registration args = %v, want %v", got.Args, wantArgs)
	}
}

// TestChangeDataDirPostMoveFailureLeavesStopped pins the ruled post-move
// failure semantics: a remap failure after the move has committed surfaces
// the error verbatim and leaves the (previously running) server STOPPED —
// there is no old location to serve from — while the pointer already names
// the new dir.
func TestChangeDataDirPostMoveFailureLeavesStopped(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	exeDir := t.TempDir()
	exe := filepath.Join(exeDir, "torrent-tv")

	b, sup, probe := relocatableBindings(t, oldDir, exeDir)
	remapErr := errors.New("disk I/O folly")
	b.remapPathsFn = func(path, from, to string) error { return remapErr }
	runningServer(t, sup)

	err := b.ChangeDataDir(newDir)
	if err == nil || !strings.Contains(err.Error(), "remap settings paths") || !strings.Contains(err.Error(), remapErr.Error()) {
		t.Fatalf("post-move failure must surface verbatim, got %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatalf("server must stay stopped after a committed-move failure, got %s", sup.State())
	}
	if probe.count() != 1 {
		t.Fatalf("no restart may be attempted after a committed-move failure, got %d factory calls", probe.count())
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("move must have committed (old dir gone): %v", err)
	}
	pointer, err := os.ReadFile(datadir.PointerPath(exe))
	if err != nil {
		t.Fatalf("pointer file: %v", err)
	}
	if got := strings.TrimSpace(string(pointer)); got != newDir {
		t.Fatalf("pointer must name the new dir %q, got %q", newDir, got)
	}
}

// TestChangeDataDirGuardBlocksConcurrentAutoStart pins the move-window
// guard: while a relocation sits between Stop and its swap, a concurrent
// completing-save must NOT fire the auto-start (previously it could Start
// the old store mid-move); after the change finishes, its own restart runs.
func TestChangeDataDirGuardBlocksConcurrentAutoStart(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	exeDir := t.TempDir()

	store := newRunnerStore(t, filepath.Join(oldDir, "settings.json")) // fresh store: required keys missing
	probe := &factoryProbe{}
	b := &Bindings{settings: store, dataDir: oldDir, dataDirSource: "default"}
	b.exePathFn = func() (string, error) { return filepath.Join(exeDir, "torrent-tv"), nil }
	b.relocateFn = func(exePath, from, to string) error { return datadir.Relocate(exePath, from, to) }
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	b.relocateFn = func(exePath, from, to string) error {
		once.Do(func() { close(entered) })
		<-release
		return datadir.Relocate(exePath, from, to)
	}
	sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: store})
	sup.appFactory = func() (appLike, error) {
		s, d, _ := b.snapshot()
		probe.record(s.Path(), d)
		return &fakeApp{serve: make(chan error), closed: make(chan struct{})}, nil
	}
	b.sup = sup
	runningServer(t, sup)

	errCh := make(chan error, 1)
	go func() { errCh <- b.ChangeDataDir(newDir) }()
	<-entered // Stop happened; the move is now blocked mid-flight
	if sup.State() != StateStopped {
		t.Fatalf("server must be stopped inside the move window, got %s", sup.State())
	}

	// The completing save: required keys filled while the relocation holds
	// the guard. The auto-start edge must defer.
	next := store.Get()
	next.DownloadRoot = filepath.Join(oldDir, "downloads")
	next.FileListUsername = "user"
	next.FileListPasskey = "pass"
	result, err := b.SaveSettings(next)
	if err != nil {
		t.Fatalf("save during relocation: %v", err)
	}
	if result.AutoStarted {
		t.Fatal("auto-start must defer while a relocation is in flight")
	}
	if probe.count() != 1 {
		t.Fatalf("no Start may race the move (initial start only), got %d factory calls", probe.count())
	}

	close(release)
	if err := <-errCh; err != nil {
		t.Fatalf("ChangeDataDir: %v", err)
	}
	waitForState(t, sup, StateRunning)
	if probe.count() != 2 {
		t.Fatalf("the relocation's own restart must run, got %d factory calls", probe.count())
	}
	store2, dir, _ := b.snapshot()
	if dir != newDir || store2.Path() != filepath.Join(newDir, "settings.json") {
		t.Fatalf("holder must be swapped, got (%s, %s)", store2.Path(), dir)
	}
}
