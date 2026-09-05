//go:build !headless && !(linux && arm)

package gui

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/mihaiflorentin88/torrent-tv/internal/composition"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/datadir"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/listenaddr"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/singleinstance"
)

// Run assembles and runs the Wails desktop app: data-dir resolution,
// settings load (which anchors the relative native paths to the data dir),
// single-instance forwarding, the supervisor, the window, the tray, and
// the state-event wiring. All Wails usage is confined to this package so a
// framework migration touches one boundary (spec: Risks).

// minimizedHides reports whether a launch starts the window hidden:
// --minimized hides it, but only with complete configuration — with
// required settings missing the setup window shows regardless, so
// autostart's pinned --minimized can never strand a wiped config as a
// silent tray-only app (spec: CLI).
func minimizedHides(minimized bool, missingRequired []string) bool {
	return minimized && len(missingRequired) == 0
}

func Run(opts Options) error {
	// Headless Linux must exit with the serve direction, never a raw GTK
	// init error. Windows/macOS always have a session; failures surface
	// from Run below.
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return ErrNoDisplay
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, source, err := datadir.ResolveFor(opts.DataDir, exe, datadir.PlatformGUI)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create data dir %s: %w", dir, err)
	}
	settingsPath := settingsPathFor(dir)
	settings, err := config.LoadAt(settingsPath)
	if err != nil {
		return err
	}

	lock, err := singleinstance.Acquire(dir)
	if err != nil {
		if errors.Is(err, singleinstance.ErrAlreadyRunning) {
			// The "show" forward already reached the running instance; a
			// second launch has done its job.
			return nil
		}
		return err
	}
	defer lock.Close()

	log, closeLog, err := newGUILogger(dir)
	if err != nil {
		return err
	}
	defer closeLog()

	bind := &Bindings{settings: settings, dataDir: dir, dataDirSource: source}
	sup := wireSupervisor(bind, log)
	// Update handoff: the helper relaunches the whole application, so the
	// single-instance lock must be released before this process exits —
	// otherwise the relaunched instance cannot acquire it.
	sup.configureApp = func(app *composition.App) {
		app.BeforeHandoffExit = func() { _ = lock.Close() }
	}
	bind.setSupervisor(sup)
	app := application.New(application.Options{
		Name: "Torrent TV",
		// Dock/taskbar icon for raw runs (make package-darwin stamps the
		// .app bundle's own icon; this covers `go run ./cmd/server gui`).
		// Options.Icon — not SetIcon: the framework applies it during
		// startup, while a direct SetIcon call would no-op because the
		// platform impl only exists once Run starts.
		Icon:   appIcon,
		Assets: application.AssetOptions{Handler: newServerProxy(assetHandler(), sup.RunningAddress, log)},
		Services: []application.Service{
			application.NewService(bind),
		},
		// The pinned beta's teardown hook: every quit path (tray Quit,
		// Cmd+Q via the app menu, window-less termination) funnels into
		// App.cleanup, which runs OnShutdown first and blocks until it
		// returns — so the server (engine + sqlite) closes before the
		// process dies. lock.Close removes gui.lock even though Run never
		// returns on macOS ([NSApp terminate:] exits first).
		OnShutdown: func() {
			_ = sup.Stop()
			_ = lock.Close()
		},
	})

	// The pinned beta installs no application menu: without this, macOS
	// has no Cmd+Q / app menu Quit at all. The role menu carries the
	// standard Quit item (NewQuitMenuItem -> globalApplication.Quit()).
	if runtime.GOOS == "darwin" {
		app.Menu.SetApplicationMenu(application.NewMenu().
			AddRole(application.AppMenu).
			AddRole(application.EditMenu).
			AddRole(application.WindowMenu))
	}

	// --minimized boots to the tray only, but minimized-to-tray applies
	// only once the server can run: autostart pins --minimized, so a wiped
	// settings file would otherwise sit as a silent tray-only app with no
	// path back to setup. Incomplete configuration opens the window
	// regardless (spec: CLI); the tray's click-to-show path stays as is.
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "Torrent TV",
		Width:     1100,
		Height:    720,
		MinWidth:  960,
		MinHeight: 600,
		URL:       "/",
		Hidden:    minimizedHides(opts.Minimized, settings.MissingRequired()),
	})
	// Close-to-tray: the pinned beta registers its own WindowClosing
	// listener in NewWindow that unconditionally destroys the window, and
	// listener order would make it win over a plain OnWindowEvent hide.
	// Hooks run before listeners and a cancelled event skips them all
	// (webview_window.go HandleWindowEvent), so cancel + hide keeps the
	// app alive with only the tray left.
	win.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		e.Cancel()
		win.Hide()
	})

	tray := newTray(app, win, sup, bind)
	lock.OnShow(func() { win.Show() })
	sup.OnStateChange(func(s State, sErr error) {
		app.Event.Emit("server:state", newStateEvent(s, sErr, listenaddr.DisplayAddress(sup.Address())))
		tray.Refresh(s)
	})
	// Boot emit: arrives before the webview loads, so the frontend also
	// seeds from the ServerState binding at startup (desktop/src/main.tsx).
	app.Event.Emit("server:state", newStateEvent(sup.State(), sup.Error(), listenaddr.DisplayAddress(sup.Address())))
	// Configured launches auto-start the embedded server exactly once
	// through the supervisor's own Start path; incomplete settings stay in
	// the setup flow (the completing-save auto-start handles those).
	if len(settings.MissingRequired()) == 0 {
		_ = sup.Start()
	}

	app.Run()
	return nil
}

// wireSupervisor builds the GUI supervisor: CanStart checks the bindings'
// CURRENT store and the factory composes NewAt against the CURRENT store's
// settings path. Both closures consult the mutex-guarded holder on every
// call, so a ChangeDataDir relocation is picked up by the very next Start
// without rebuilding the supervisor (spec: Data directory). Relative native
// paths need no start-time anchoring: LoadAt anchors them at load.
func wireSupervisor(bind *Bindings, log *slog.Logger) *Supervisor {
	sup := NewSupervisor(SupervisorDeps{
		Log: log,
		CanStart: func() error {
			// The relocation guard keeps any Start — including the
			// SaveSettings completing-save auto-start — out of the
			// move window between Stop and the holder swap.
			if bind.relocatingServer() {
				return errors.New("data directory change in progress; try again when it finishes")
			}
			store, _, _ := bind.snapshot()
			if missing := store.MissingRequired(); len(missing) > 0 {
				return fmt.Errorf("required settings missing: %s", strings.Join(missing, ", "))
			}
			return nil
		},
	})
	// The default appFactory reads deps.Settings, frozen at boot; this
	// replacement is the one the supervisor ever uses.
	sup.appFactory = func() (appLike, error) {
		store, _, _ := bind.snapshot()
		app, err := composition.NewAt(store.Path(), log)
		if err != nil {
			return nil, err
		}
		if sup.configureApp != nil {
			sup.configureApp(app)
		}
		return appAdapter{app: app}, nil
	}
	return sup
}

// newStateEvent shapes the payload carried by the 'server:state' topic;
// the TS mirror is desktop/src/lib/state.ts.
func newStateEvent(s State, sErr error, address string) StateEvent {
	ev := StateEvent{State: s, Address: address}
	if sErr != nil {
		ev.Error = sErr.Error()
	}
	return ev
}

// newGUILogger writes JSON lines to <data dir>/logs/server.jsonl. The GUI
// has no meaningful console; the file is the only sink.
func newGUILogger(dir string) (*slog.Logger, func(), error) {
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return nil, nil, fmt.Errorf("create log dir %s: %w", logDir, err)
	}
	f, err := os.OpenFile(filepath.Join(logDir, "server.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, nil, fmt.Errorf("open gui log: %w", err)
	}
	return slog.New(slog.NewJSONHandler(f, nil)), func() { _ = f.Close() }, nil
}
