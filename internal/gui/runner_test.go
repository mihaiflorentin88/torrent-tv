//go:build !headless && !(linux && arm)

package gui

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/composition"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// newRunnerStore mirrors the store Run builds: LoadAt over an explicit
// settings path, exactly as the runner does.
func newRunnerStore(t *testing.T, path string) *config.Store {
	t.Helper()
	settings, err := config.LoadAt(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return settings
}

// runnerSupervisor wires the supervisor exactly the way Run does now:
// bindings holder first, then wireSupervisor (CanStart gate over the
// CURRENT store, factory that anchors before delegating to NewAt). It is
// the testable seam of Run's boot path (no window needed).
func runnerSupervisor(settings *config.Store, dir string) *Supervisor {
	bind := &Bindings{settings: settings, dataDir: dir, dataDirSource: "default"}
	sup := wireSupervisor(bind, slog.New(slog.NewTextHandler(io.Discard, nil)))
	bind.sup = sup
	return sup
}

// waitRunning polls until the supervisor leaves starting; the factory and
// serve loop are asynchronous.
func waitRunning(t *testing.T, sup *Supervisor) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for sup.State() == StateStarting {
		if time.Now().After(deadline) {
			t.Fatalf("supervisor never left starting; state=%s err=%v", sup.State(), sup.Error())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRunnerBootDoesNotAnchorOrSave pins fix C1, case 1: a fresh boot with
// incomplete config must NOT write the settings file (no boot-time anchor
// Save), and MissingRequired still lists all three keys — the setup banner
// under-asking was the regression.
func TestRunnerBootDoesNotAnchorOrSave(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work) // relative default paths mkdir under the temp CWD, not the repo
	dir := t.TempDir()
	settings := newRunnerStore(t, filepath.Join(dir, "settings.json"))

	sup := runnerSupervisor(settings, dir)
	err := sup.Start()
	if err == nil || !strings.Contains(err.Error(), "required settings missing") {
		t.Fatalf("incomplete config must refuse Start, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("boot must not create or rewrite the settings file, stat err=%v", err)
	}
	if got := settings.MissingRequired(); len(got) != 3 {
		t.Fatalf("MissingRequired must still list all three keys, got %v", got)
	}
}

// TestRunnerStartRunsOnLoadAnchoredPaths pins the load-time anchoring
// contract end to end: a settings file with relative paths (the historic
// serve-mode layout) resolves against the data dir — never the process
// CWD — so Start succeeds from an arbitrary working directory, opens the
// database at <data dir>/filelist.db, and does not rewrite the file.
func TestRunnerStartRunsOnLoadAnchoredPaths(t *testing.T) {
	t.Chdir(t.TempDir()) // an arbitrary CWD: anchoring must not consult it
	dir := t.TempDir()
	downloads := filepath.Join(dir, "downloads")
	body := `{` +
		`"listenAddress": ":0",` +
		`"databasePath": "data/filelist.db",` +
		`"downloadRoot": "` + downloads + `",` +
		`"fileListUsername": "user",` +
		`"fileListPasskey": "pass"` +
		`}`
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := newRunnerStore(t, settingsPath)

	if want := filepath.Join(dir, "filelist.db"); settings.Get().DatabasePath != want {
		t.Fatalf("load must anchor databasePath to %q, got %q", want, settings.Get().DatabasePath)
	}
	if got := settings.Get().DownloadRoot; got != downloads {
		t.Fatalf("absolute file-provided downloadRoot must stay untouched, got %q", got)
	}

	sup := runnerSupervisor(settings, dir)
	if err := sup.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = sup.Stop() })
	waitRunning(t, sup)

	if _, err := os.Stat(filepath.Join(dir, "filelist.db")); err != nil {
		t.Fatalf("database must open at the anchored location: %v", err)
	}
}

// TestChangeDataDirPostRelocationStartUsesNewStore pins the relocation
// contract through the production wiring: ChangeDataDir (stopped), then
// Start — the real wireSupervisor factory must anchor and NewAt against
// the NEW settings path, so the server reopens its database at the moved
// location (canary: the sqlite file appears only under the new dir).
func TestChangeDataDirPostRelocationStartUsesNewStore(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	body := fmt.Sprintf(`{
		"listenAddress": ":0",
		"databasePath": %q,
		"downloadRoot": %q,
		"torrentSessionDir": %q,
		"fileListUsername": "user",
		"fileListPasskey": "pass"
	}`,
		filepath.Join(oldDir, "filelist.db"),
		filepath.Join(oldDir, "downloads"),
		filepath.Join(oldDir, "torrent-session"))
	settingsPath := filepath.Join(oldDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := newRunnerStore(t, settingsPath)

	bind := &Bindings{settings: settings, dataDir: oldDir, dataDirSource: "default"}
	sup := wireSupervisor(bind, testLogger())
	bind.sup = sup

	if err := bind.ChangeDataDir(newDir); err != nil {
		t.Fatalf("ChangeDataDir: %v", err)
	}
	store, dir, source := bind.snapshot()
	if dir != newDir || source != "pointer" {
		t.Fatalf("holder = (%s, %s), want (%s, pointer)", dir, source, newDir)
	}
	if want := filepath.Join(newDir, "filelist.db"); store.Get().DatabasePath != want {
		t.Fatalf("anchored databasePath must remap to %q, got %q", want, store.Get().DatabasePath)
	}
	if err := sup.Start(); err != nil {
		t.Fatalf("start after relocation: %v", err)
	}
	t.Cleanup(func() { _ = sup.Stop() })
	waitRunning(t, sup)

	if _, err := os.Stat(filepath.Join(newDir, "filelist.db")); err != nil {
		t.Fatalf("database must reopen at the moved location: %v", err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old data dir %s must be gone", oldDir)
	}
}

// TestCanStartRefusesWhileRelocating pins the move-window guard on the
// production wiring: while a ChangeDataDir holds the relocating flag,
// Start is refused with a relocation-in-progress error and the state stays
// untouched.
func TestCanStartRefusesWhileRelocating(t *testing.T) {
	bind := &Bindings{settings: testStore(t), dataDir: t.TempDir(), dataDirSource: "default"}
	sup := wireSupervisor(bind, testLogger())
	bind.sup = sup

	bind.mu.Lock()
	bind.relocating = true
	bind.mu.Unlock()
	err := sup.Start()
	if err == nil || !strings.Contains(err.Error(), "data directory change in progress") {
		t.Fatalf("start during relocation must be refused, got %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatalf("refusal must leave the state untouched, got %s", sup.State())
	}
}

// TestWireSupervisorRunsConfigureAppOnConstructedApp pins the update
// handoff wiring: the supervisor's factory runs the registered configure
// hook on every freshly constructed app, so the runner can register the
// single-instance lock release (BeforeHandoffExit) before serving.
func TestWireSupervisorRunsConfigureAppOnConstructedApp(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	downloads := filepath.Join(dir, "downloads")
	body := `{` +
		`"listenAddress": "127.0.0.1:0",` +
		`"databasePath": "data/filelist.db",` +
		`"downloadRoot": "` + downloads + `",` +
		`"fileListUsername": "user",` +
		`"fileListPasskey": "pass"` +
		`}`
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	bind := &Bindings{settings: newRunnerStore(t, settingsPath), dataDir: dir, dataDirSource: "default"}
	sup := wireSupervisor(bind, testLogger())
	configured := make(chan struct{}, 1)
	sup.configureApp = func(app *composition.App) {
		app.BeforeHandoffExit = func() {}
		configured <- struct{}{}
	}
	t.Cleanup(func() { _ = sup.Stop() })
	if err := sup.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitRunning(t, sup)
	select {
	case <-configured:
	case <-time.After(2 * time.Second):
		t.Fatal("factory never ran the configure hook on the constructed app")
	}
}

// TestMinimizedHidesOnlyWithCompleteConfig pins the boot fix: autostart
// pins --minimized, so a wiped settings file must still open the setup
// window instead of stranding the app as a silent tray-only process.
func TestMinimizedHidesOnlyWithCompleteConfig(t *testing.T) {
	if !minimizedHides(true, nil) {
		t.Fatal("--minimized with complete config must hide the window")
	}
	if minimizedHides(true, []string{"fileListPasskey"}) {
		t.Fatal("--minimized with incomplete config must show the setup window")
	}
	if minimizedHides(false, []string{"fileListPasskey"}) {
		t.Fatal("a non-minimized launch always shows the window")
	}
}
