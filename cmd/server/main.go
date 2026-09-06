package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mihaiflorentin88/torrent-tv/internal/application/updates"
	"github.com/mihaiflorentin88/torrent-tv/internal/composition"
	"github.com/mihaiflorentin88/torrent-tv/internal/gui"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/datadir"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/listenaddr"
)

// settingsPathEnv keeps its historic precedence (spec: Data directory): when
// set, composition loads the settings file it points at; otherwise the file
// lives at <resolved data dir>/settings.json.
const settingsPathEnv = "TORRENT_TV_SETTINGS_PATH"

type guiOptions struct {
	Minimized bool
	DataDir   string
}

type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// runGUI launches the desktop GUI (window, tray, supervisor). On Linux
// without a display session it returns gui.ErrNoDisplay.
func runGUI(opts guiOptions) error {
	return gui.Run(gui.Options{Minimized: opts.Minimized, DataDir: opts.DataDir})
}

// newRootCommand separates command wiring from effects so tests can inject
// the GUI and serve runners. The serve runner receives the --update flag:
// update-and-serve, never check-only.
func newRootCommand(runGUI func(guiOptions) error, runServe func(string, bool, logger) error) *cobra.Command {
	var dataDir string
	var minimized bool
	root := &cobra.Command{
		Use:     "torrent-tv",
		Short:   "Torrent TV media server",
		Version: versionString(),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := guiOptions{Minimized: minimized, DataDir: dataDir}
			// gui.ErrNoDisplay's text already points at `serve`, so the
			// error is returned as-is; cobra adds the usage line listing
			// the subcommand.
			return runGUI(opts)
		},
	}
	root.PersistentFlags().StringVar(&dataDir, "data-dir", "", "data directory (default: data/ next to the executable)")
	root.Flags().BoolVar(&minimized, "minimized", false, "start minimized to the system tray")
	var update bool
	serve := &cobra.Command{
		Use:   "serve",
		Short: "run the headless streaming server",
		RunE: func(cmd *cobra.Command, args []string) error {
			attachParentConsole()
			dir, err := resolveDataDir(dataDir)
			if err != nil {
				return err
			}
			log, closeLog := newLogger(os.Stdout, isTerminal(os.Stdout), logFilePath(dir))
			defer closeLog()
			return runServe(dir, update, log)
		},
	}
	serve.Flags().BoolVar(&update, "update", false, "check for and install an update before serving, then continue from the new installation")
	root.AddCommand(serve)
	return root
}

// resolveDataDir resolves the effective data directory and makes sure it
// exists before anything writes into it.
func resolveDataDir(flagDir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir, _, err := datadir.Resolve(flagDir, exe)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create data dir %s: %w", dir, err)
	}
	return dir, nil
}

// versionString prefers the linker-injected release version; a plain `go run`
// (no ldflags) falls back to the repo's VERSION file so dev runs report the
// release version instead of the compile-time "dev".
func versionString() string {
	if v := composition.Version; v != "" && v != "dev" {
		return v
	}
	if b, err := os.ReadFile("VERSION"); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return composition.Version
}

// runServe is the headless server — optional update-and-serve step,
// settings resolution, composition, signal handling, startup update
// trigger after listener readiness, and ListenAndServe.
func runServe(dataDir string, update bool, log logger) error {
	settingsPath := os.Getenv(settingsPathEnv)
	if settingsPath == "" {
		settingsPath = filepath.Join(dataDir, "settings.json")
	}
	if update {
		// The CLI update step owns the restart: a successful apply exits
		// the process through the handoff, and the relaunched installation
		// serves without --update. Any failure is reported and serving
		// proceeds — an update failure never takes the server down.
		if err := runUpdateStep(log); err != nil {
			return err
		}
	}
	app, err := openComposition(settingsPath, log)
	if err != nil {
		log.Error("startup failed", "error", err)
		return err
	}
	defer func() { _ = app.Close(context.Background()) }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := app.Server.Shutdown(shutdownCtx); err != nil {
			log.Error("graceful shutdown failed", "error", err)
		}
	}()

	log.Info("server listening", "address", listenaddr.DisplayAddress(app.ListenAddress), "version", composition.Version, "settingsFile", app.Settings.Path())
	if err := app.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
	// The listener also closes when the startup auto-apply drains serving
	// to stage and swap an update. Falling through to the deferred Close
	// would kill the process mid-operation — every restart would then
	// re-apply and exit the same way, an update that never lands. Hold the
	// process until the operation ends: on success the handoff exits the
	// process from inside the apply, and a failed or absent operation
	// releases the wait for the normal shutdown path. Bounded so a wedged
	// operation can still be stopped by the supervisor.
	if app.Updates != nil {
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelWait()
		if err := app.Updates.WaitIdle(waitCtx); err != nil {
			log.Warn("update operation still running at shutdown", "error", err)
		}
	}
	return nil
}

// runUpdateStep performs the blocking --update transaction with the same
// coordinator the running server uses. An accepted apply blocks until the
// handoff exits the process; WaitIdle returning always means the operation
// failed observably, and serving proceeds.
func runUpdateStep(log logger) error {
	coordinator, err := openUpdateCoordinator(log)
	if err != nil {
		log.Warn("update step unavailable; serving anyway", "error", err)
		return nil
	}
	return applyUpdateBeforeServing(coordinator, log)
}

// applyUpdateBeforeServing runs one coordinator apply to its observable
// end. An accepted apply blocks until the handoff exits the process;
// WaitIdle returning always means the operation failed observably (the
// failure is journaled), and serving proceeds.
func applyUpdateBeforeServing(coordinator *updates.Manager, log logger) error {
	result, err := coordinator.Apply(context.Background())
	if err != nil {
		log.Warn("update check failed; serving anyway", "error", err)
		return nil
	}
	if !result.Accepted {
		log.Info("update check: already current", "version", result.Status.CurrentVersion)
		return nil
	}
	log.Info("update accepted; handing off to the new installation", "version", result.Status.Latest)
	// No HTTP response is flushed on this path, so the accepted apply's
	// handoff barrier is released right away — otherwise the pipeline
	// would sit out the whole flush window before downloading.
	coordinator.ResponseFlushed()
	if err := coordinator.WaitIdle(context.Background()); err != nil {
		log.Error("update operation failed", "error", err)
	}
	return nil
}

// openUpdateCoordinator builds the standalone --update coordinator, which
// needs the process *slog.Logger for its degradation warnings.
func openUpdateCoordinator(log logger) (*updates.Manager, error) {
	sl, ok := log.(*slog.Logger)
	if !ok {
		return nil, fmt.Errorf("openUpdateCoordinator needs the process *slog.Logger, got %T", log)
	}
	return composition.NewUpdateCoordinator(sl), nil
}

// applyRelaunchArgs adopts the invocation an update handoff carried over:
// the relaunched installation resumes the original command line (data-dir
// identity included, --update stripped), and the marker is consumed — the
// only update plumbing that survives the restart.
func applyRelaunchArgs() {
	if args, ok := updates.TakeRelaunchArgs(); ok {
		os.Args = append([]string{os.Args[0]}, args...)
	}
}

// openComposition assembles the application against settingsPath, keeping
// the settings path explicit: the env bridge (os.Setenv) is gone —
// composition.NewAt takes the path directly, and runServe passes
// env-if-set-else-resolved so the historic precedence is unchanged.
func openComposition(settingsPath string, log logger) (*composition.App, error) {
	sl, ok := log.(*slog.Logger)
	if !ok {
		return nil, fmt.Errorf("openComposition needs the process *slog.Logger, got %T", log)
	}
	return composition.NewAt(settingsPath, sl)
}

func main() {
	// Adopt a helper-carried invocation before any command wiring: the
	// relaunched installation resumes as if started with those arguments.
	applyRelaunchArgs()
	root := newRootCommand(runGUI, runServe)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
