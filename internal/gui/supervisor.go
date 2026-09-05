// Package gui hosts the desktop application's lifecycle wiring. It is a
// pure-Go package (no Wails imports) so it builds on every platform,
// including linux/arm.
package gui

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/composition"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// State is the server lifecycle phase the GUI renders in its status
// pill, tray, and buttons.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

// ErrNotRunning is returned by Stop when there is no running server to
// shut down. Restart tolerates it: restarting from stopped or failed
// simply starts.
var ErrNotRunning = errors.New("server is not running")

// shutdownTimeout bounds a graceful server shutdown before Close.
const shutdownTimeout = 15 * time.Second

// appLike is the server surface the supervisor drives. composition.App
// satisfies it via appAdapter; tests substitute fakes. Close carries the
// shutdown context so the S3 service join can bound its wait, and
// ListenAndServe itself schedules the post-readiness startup update via
// the app's listener hook.
type appLike interface {
	ListenAndServe() error
	Shutdown(ctx context.Context) error
	Close(ctx context.Context) error
	ListenAddress() string
}

// appAdapter lifts composition.App (field-based ListenAddress) into the
// appLike interface.
type appAdapter struct{ app *composition.App }

func (w appAdapter) ListenAndServe() error              { return w.app.ListenAndServe() }
func (w appAdapter) Shutdown(ctx context.Context) error { return w.app.Server.Shutdown(ctx) }
func (w appAdapter) Close(ctx context.Context) error    { return w.app.Close(ctx) }
func (w appAdapter) ListenAddress() string              { return w.app.ListenAddress }

var _ appLike = appAdapter{}

// SupervisorDeps carries everything the supervisor needs from the host
// application.
type SupervisorDeps struct {
	Log      *slog.Logger
	Settings *config.Store
	// CanStart reports whether required settings are present. A
	// non-nil error is a refusal (the GUI shows setup), not a failure.
	CanStart func() error
}

// Supervisor serializes the server lifecycle: Start, Stop, and the
// asynchronous serve loop all mutate state under one mutex, and every
// transition fires OnStateChange exactly once, outside the lock so the
// callback may call back into the supervisor.
type Supervisor struct {
	mu      sync.Mutex
	state   State
	err     error
	app     appLike
	address string
	deps    SupervisorDeps
	// onChange fires on every transition, after the fields are
	// committed and without the lock held.
	onChange func(State, error)
	// appFactory is a test seam; the default wraps composition.New.
	appFactory func() (appLike, error)
	// configureApp runs on every freshly constructed composition.App
	// before it serves. The GUI runner registers the single-instance lock
	// release here so an update handoff never relaunches into a still-
	// held lock.
	configureApp func(*composition.App)
}

// NewSupervisor returns a supervisor in the stopped state.
func NewSupervisor(deps SupervisorDeps) *Supervisor {
	return &Supervisor{
		state: StateStopped,
		deps:  deps,
		appFactory: func() (appLike, error) {
			log := deps.Log
			if log == nil {
				log = slog.Default()
			}
			// Same store as the GUI runner: NewAt re-reads the settings
			// file at the store's path (env precedence already decided by
			// whoever resolved it), so a settings save is picked up by the
			// next Start.
			app, err := composition.NewAt(deps.Settings.Path(), log)
			if err != nil {
				return nil, err
			}
			return appAdapter{app: app}, nil
		},
	}
}

// OnStateChange registers a single callback fired once per transition.
func (s *Supervisor) OnStateChange(fn func(State, error)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// State reports the current lifecycle phase.
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Error reports the failure carried by the failed state, if any.
func (s *Supervisor) Error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Address reports the listen address of the running (or most recently
// run) app.
func (s *Supervisor) Address() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.address
}

// RunningAddress reports the dialable listen address while the server is
// running. ok is false in every other phase — including after a stop,
// where Address keeps reporting the most recent run — so callers such as
// the asset-server proxy can distinguish "no server" from a stale address.
func (s *Supervisor) RunningAddress() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning || s.address == "" {
		return "", false
	}
	return s.address, true
}

// transition commits a state change and returns the callback to fire.
// Callers must hold the lock and fire the returned callback after
// unlocking.
func (s *Supervisor) transition(st State, err error) func(State, error) {
	s.state = st
	s.err = err
	return s.onChange
}

func (s *Supervisor) fire(fn func(State, error), st State, err error) {
	if fn != nil {
		fn(st, err)
	}
}

// Start brings the server from stopped (or failed) to running. A
// refusal from CanStart leaves the state untouched: missing settings
// means setup, not failure. The heavy app construction and serving run
// on a goroutine; construction or serve errors land in failed.
func (s *Supervisor) Start() error {
	s.mu.Lock()
	if s.state != StateStopped && s.state != StateFailed {
		s.mu.Unlock()
		return errors.New("server is not stopped")
	}
	if s.deps.CanStart != nil {
		if err := s.deps.CanStart(); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	onChange := s.transition(StateStarting, nil)
	s.mu.Unlock()
	s.fire(onChange, StateStarting, nil)

	go s.run()

	return nil
}

// run constructs the app and serves it, reporting failures via the
// failed state.
func (s *Supervisor) run() {
	app, err := s.appFactory()
	if err != nil {
		s.mu.Lock()
		onChange := s.transition(StateFailed, err)
		s.mu.Unlock()
		s.fire(onChange, StateFailed, err)
		return
	}

	s.mu.Lock()
	s.app = app
	s.address = app.ListenAddress()
	onChange := s.transition(StateRunning, nil)
	s.mu.Unlock()
	s.fire(onChange, StateRunning, nil)

	if err := app.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.mu.Lock()
		// Only the running state owns this failure: if Stop already
		// moved to stopping, it also owns Shutdown/Close, and a serve
		// error arriving during teardown must not flash failed.
		if s.state == StateRunning {
			s.app = nil
			onChange := s.transition(StateFailed, err)
			s.mu.Unlock()
			app.Close(context.Background()) //nolint:errcheck // failed state already carries the failure
			s.fire(onChange, StateFailed, err)
			return
		}
		s.mu.Unlock()
	}
}

// Stop gracefully shuts the running server and releases its resources.
// It refuses unless running.
func (s *Supervisor) Stop() error {
	s.mu.Lock()
	if s.state != StateRunning {
		s.mu.Unlock()
		return ErrNotRunning
	}
	onChange := s.transition(StateStopping, nil)
	app := s.app
	s.mu.Unlock()
	s.fire(onChange, StateStopping, nil)

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = app.Shutdown(ctx)
	if err := app.Close(ctx); err != nil && s.deps.Log != nil {
		// A join timeout leaves engine and repository open rather than
		// closing them under active writers; the server is down either
		// way, and the error stays observable in the GUI log.
		s.deps.Log.Warn("server close did not finish cleanly", "error", err.Error())
	}

	s.mu.Lock()
	onChange = s.transition(StateStopped, nil)
	s.app = nil
	s.mu.Unlock()
	s.fire(onChange, StateStopped, nil)
	return nil
}

// Restart runs Stop then Start; the first error wins.
func (s *Supervisor) Restart() error {
	if err := s.Stop(); err != nil && !errors.Is(err, ErrNotRunning) {
		return err
	}
	return s.Start()
}
