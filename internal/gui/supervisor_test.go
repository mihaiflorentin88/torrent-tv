package gui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/composition"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// fakeApp is a controllable stand-in for composition.App. A nil serve
// channel means ListenAndServe returns serveErr immediately; a non-nil
// serve channel blocks until a value is sent, simulating a running
// server. closeCalls counts Close invocations (exactly one per app);
// closed reports the first Close; startups records StartupUpdate triggers.
type fakeApp struct {
	addr       string
	serveErr   error
	serve      chan error
	closed     chan struct{}
	closeCalls atomic.Int32
}

func (f *fakeApp) ListenAndServe() error {
	if f.serve != nil {
		return <-f.serve
	}
	return f.serveErr
}

func (f *fakeApp) Shutdown(ctx context.Context) error { return ctx.Err() }

func (f *fakeApp) Close(context.Context) error {
	if f.closeCalls.Add(1) == 1 {
		close(f.closed)
	}
	return nil
}

func (f *fakeApp) ListenAddress() string { return f.addr }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testStore(t *testing.T) *config.Store {
	t.Helper()
	s, err := config.LoadAt(t.TempDir() + "/settings.json")
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	return s
}

// newTestSupervisor builds a supervisor whose appFactory always yields
// the given appLike, recording each state transition it observes.
func newTestSupervisor(t *testing.T, app appLike, events *[]State, mu *sync.Mutex) *Supervisor {
	t.Helper()
	sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: testStore(t)})
	sup.appFactory = func() (appLike, error) { return app, nil }
	if events != nil {
		sup.OnStateChange(func(s State, _ error) {
			mu.Lock()
			*events = append(*events, s)
			mu.Unlock()
		})
	}
	return sup
}

func waitForState(t *testing.T, sup *Supervisor, want State) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for sup.State() != want {
		select {
		case <-deadline:
			t.Fatalf("never reached %s (currently %s)", want, sup.State())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSupervisorTransitionsAndFailure(t *testing.T) {
	var mu sync.Mutex
	var events []State
	app := &fakeApp{addr: "127.0.0.1:0", serveErr: errors.New("listen tcp 127.0.0.1:9999: bind: address already in use"), closed: make(chan struct{})}
	sup := newTestSupervisor(t, app, &events, &mu)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	waitForState(t, sup, StateFailed)

	mu.Lock()
	defer mu.Unlock()
	want := []State{StateStarting, StateRunning, StateFailed}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i, s := range want {
		if events[i] != s {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
	if sup.Error() == nil || !strings.Contains(sup.Error().Error(), "address already in use") {
		t.Fatalf("failed state must carry the bind error, got %v", sup.Error())
	}
	// The failed serve attempt must not leak the app: Close is called
	// exactly once even though s.app was already cleared.
	select {
	case <-app.closed:
	default:
		t.Fatal("serve failure must Close the app")
	}
}

func TestSupervisorRefusesMissingSettings(t *testing.T) {
	var mu sync.Mutex
	var events []State
	sup := NewSupervisor(SupervisorDeps{
		Log:      testLogger(),
		Settings: testStore(t),
		CanStart: func() error { return errors.New("required settings missing: fileListUsername") },
	})
	sup.OnStateChange(func(s State, _ error) {
		mu.Lock()
		events = append(events, s)
		mu.Unlock()
	})

	err := sup.Start()
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Start must refuse: %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatalf("refusal leaves state stopped — setup, not failed; got %s", sup.State())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 0 {
		t.Fatalf("refusal must not fire state events, got %v", events)
	}
}

func TestSupervisorStopFromRunning(t *testing.T) {
	var mu sync.Mutex
	var events []State
	serve := make(chan error)
	app := &fakeApp{addr: "127.0.0.1:8080", serve: serve, closed: make(chan struct{})}
	sup := newTestSupervisor(t, app, &events, &mu)

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForState(t, sup, StateRunning)
	if got := sup.Address(); got != "127.0.0.1:8080" {
		t.Fatalf("Address() = %q, want 127.0.0.1:8080", got)
	}

	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatalf("state = %s, want stopped", sup.State())
	}
	select {
	case <-app.closed:
	default:
		t.Fatal("Stop must call app.Close")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []State{StateStarting, StateRunning, StateStopping, StateStopped}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i, s := range want {
		if events[i] != s {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}

func TestSupervisorStopRefusesWhenNotRunning(t *testing.T) {
	sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: testStore(t)})
	if err := sup.Stop(); err == nil {
		t.Fatal("Stop must refuse when not running")
	}
	if sup.State() != StateStopped {
		t.Fatalf("state = %s, want stopped", sup.State())
	}
}

func TestSupervisorStartRefusesWhenRunning(t *testing.T) {
	serve := make(chan error)
	app := &fakeApp{serve: serve, closed: make(chan struct{})}
	sup := newTestSupervisor(t, app, nil, nil)
	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForState(t, sup, StateRunning)
	if err := sup.Start(); err == nil {
		t.Fatal("second Start must refuse while running")
	}
	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestSupervisorRestartRunsStopThenStart(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	serve := make(chan error)
	app := &fakeApp{serve: serve, closed: make(chan struct{})}
	sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: testStore(t)})
	sup.appFactory = func() (appLike, error) {
		mu.Lock()
		calls = append(calls, "factory")
		mu.Unlock()
		return app, nil
	}
	sup.OnStateChange(func(s State, _ error) {
		mu.Lock()
		calls = append(calls, "state:"+string(s))
		mu.Unlock()
	})

	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForState(t, sup, StateRunning)
	if err := sup.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	// The second start's factory and running event fire on the serve
	// goroutine; wait for them before asserting the recorded order.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		count := 0
		for _, c := range calls {
			if c == "state:running" {
				count++
			}
		}
		mu.Unlock()
		if count == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("second running event never fired: %v", calls)
		case <-time.After(10 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	stoppingIdx, stoppedIdx, factory2Idx := -1, -1, -1
	factoryCount := 0
	for i, c := range calls {
		switch {
		case c == "factory":
			factoryCount++
			if factoryCount == 2 {
				factory2Idx = i
			}
		case c == "state:stopping" && stoppingIdx == -1:
			stoppingIdx = i
		case c == "state:stopped" && stoppedIdx == -1:
			stoppedIdx = i
		}
	}
	if stoppingIdx == -1 || stoppedIdx == -1 || factory2Idx == -1 {
		t.Fatalf("missing phases in calls %v", calls)
	}
	if !(stoppedIdx < factory2Idx) || !(stoppingIdx < stoppedIdx) {
		t.Fatalf("Restart must run stop phases before re-creating the app: %v", calls)
	}
	if factoryCount != 2 {
		t.Fatalf("factory called %d times, want 2", factoryCount)
	}
}

func TestSupervisorRestartFailsWhenStartRefuses(t *testing.T) {
	serve := make(chan error)
	app := &fakeApp{serve: serve, closed: make(chan struct{})}
	var mu sync.Mutex
	var events []State
	sup := newTestSupervisor(t, app, &events, &mu)
	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForState(t, sup, StateRunning)
	sup.mu.Lock()
	sup.deps.CanStart = func() error { return errors.New("required settings missing: x") }
	sup.mu.Unlock()
	if err := sup.Restart(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Restart must report the Start refusal: %v", err)
	}
	if sup.State() != StateStopped {
		t.Fatalf("after failed Restart state = %s, want stopped", sup.State())
	}
}

func TestSupervisorRestartWhenStopped(t *testing.T) {
	serve := make(chan error)
	app := &fakeApp{serve: serve, closed: make(chan struct{})}
	sup := newTestSupervisor(t, app, nil, nil)
	if err := sup.Restart(); err != nil {
		t.Fatalf("Restart from stopped: %v", err)
	}
	waitForState(t, sup, StateRunning)
	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestSupervisorFailedIsRestartable pins the contract that Start is
// allowed from failed (the GUI's retry button), and the bind error is
// cleared once the restart succeeds.
func TestSupervisorFailedIsRestartable(t *testing.T) {
	bad := &fakeApp{serveErr: errors.New("bind: address already in use"), closed: make(chan struct{})}
	sup := newTestSupervisor(t, bad, nil, nil)
	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForState(t, sup, StateFailed)

	serve := make(chan error)
	good := &fakeApp{serve: serve, closed: make(chan struct{})}
	sup.appFactory = func() (appLike, error) { return good, nil }
	if err := sup.Start(); err != nil {
		t.Fatalf("Start after failed: %v", err)
	}
	waitForState(t, sup, StateRunning)
	if sup.Error() != nil {
		t.Fatalf("error must clear on successful start, got %v", sup.Error())
	}
	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestSupervisorFactoryErrorFails pins that an appFactory failure lands
// in failed with the error, not stuck in starting.
func TestSupervisorFactoryErrorFails(t *testing.T) {
	var events []State
	var mu sync.Mutex
	sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: testStore(t)})
	factoryErr := errors.New("database locked")
	sup.appFactory = func() (appLike, error) { return nil, factoryErr }
	sup.OnStateChange(func(s State, _ error) {
		mu.Lock()
		events = append(events, s)
		mu.Unlock()
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForState(t, sup, StateFailed)
	if !errors.Is(sup.Error(), factoryErr) {
		t.Fatalf("Error() = %v, want %v", sup.Error(), factoryErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0] != StateStarting || events[1] != StateFailed {
		t.Fatalf("events = %v, want [starting failed]", events)
	}
}

// TestSupervisorOnStateChangeCallbackCanReadState guards the deadlock
// contract: the callback runs outside the supervisor lock, so it may
// call back into State()/Error().
func TestSupervisorOnStateChangeCallbackCanReadState(t *testing.T) {
	serve := make(chan error)
	app := &fakeApp{serve: serve, closed: make(chan struct{})}
	sup := newTestSupervisor(t, app, nil, nil)
	done := make(chan struct{})
	sup.OnStateChange(func(s State, _ error) {
		_ = sup.State()
		_ = sup.Error()
		if s == StateRunning {
			close(done)
		}
	})
	if err := sup.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("running callback never fired (possible deadlock)")
	}
	if err := sup.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestSupervisorConcurrentStartStop hammers Start/Stop concurrently to
// prove the state check is serialized: at most one goroutine may win
// the running transition at a time and no stop races a failed write.
func TestSupervisorConcurrentStartStop(t *testing.T) {
	for i := range 20 {
		// Every Start cycle gets a fresh fake app; sequential cycles on
		// one supervisor are legal, and each instance must be closed
		// exactly once.
		serve := make(chan error)
		var appsMu sync.Mutex
		var apps []*fakeApp
		sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: testStore(t)})
		sup.appFactory = func() (appLike, error) {
			app := &fakeApp{serve: serve, closed: make(chan struct{})}
			appsMu.Lock()
			apps = append(apps, app)
			appsMu.Unlock()
			return app, nil
		}
		n := 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		for g := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if g%2 == 0 {
					errs[g] = sup.Start()
				} else {
					errs[g] = sup.Stop()
				}
			}()
		}
		wg.Wait()
		stopOK, startOK := 0, 0
		for g, err := range errs {
			if err != nil {
				continue
			}
			if g%2 == 0 {
				startOK++
			} else {
				stopOK++
			}
		}
		// Starts are serialized against each other: a successful Start
		// leaves starting/running behind it, so a later Start can only win
		// after an intervening successful Stop. Sequential start/stop
		// cycles are legal; overlapping ones are not.
		if startOK > stopOK+1 {
			t.Fatalf("iteration %d: %d starts succeeded vs %d stops — start check not serialized", i, startOK, stopOK)
		}
		// A successful Start must eventually Close its app, whether the
		// teardown came from Stop or the supervisor releasing the app.
		// With no successful Start there is no goroutine to release.
		if startOK == 0 {
			continue
		}
		// Drain to a clean stopped state for the next iteration.
		deadline := time.After(2 * time.Second)
		for sup.State() == StateStarting {
			select {
			case <-deadline:
				t.Fatal("stuck starting")
			case <-time.After(5 * time.Millisecond):
			}
		}
		if sup.State() == StateRunning {
			if err := sup.Stop(); err != nil {
				t.Fatalf("cleanup Stop: %v", err)
			}
		}
		closeDeadline := time.After(2 * time.Second)
		for {
			appsMu.Lock()
			done, total := 0, len(apps)
			for _, app := range apps {
				if app.closeCalls.Load() == 1 {
					done++
				}
			}
			appsMu.Unlock()
			if done == total && total > 0 {
				break
			}
			select {
			case <-closeDeadline:
				t.Fatalf("iteration %d: only %d/%d apps closed", i, done, total)
			case <-time.After(5 * time.Millisecond):
			}
		}
		// The serve-failure path and Stop must never both close the
		// same app instance.
		for _, app := range apps {
			if got := app.closeCalls.Load(); got != 1 {
				t.Fatalf("iteration %d: app closed %d times, want 1", i, got)
			}
		}
	}
}

// compile-time proof that the default composition.App satisfies the
// supervisor's internal app interface.
var _ appLike = appAdapter{}

func TestAppAdapterWrapsCompositionApp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	a := &composition.App{Server: srv, ListenAddress: ln.Addr().String()}
	wrapped := appAdapter{app: a}
	if wrapped.ListenAddress() != a.ListenAddress {
		t.Fatalf("ListenAddress = %q, want %q", wrapped.ListenAddress(), a.ListenAddress)
	}
	go func() { _ = srv.Serve(ln) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request through wrapped app: %v", err)
	}
	resp.Body.Close()
}
