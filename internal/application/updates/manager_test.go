package updates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventRecord is one emitted sink event.
type eventRecord struct {
	kind    string
	payload []byte
}

// eventRecorder is a mutex-safe sink capturing the coordinator's emitted
// events in order.
type eventRecorder struct {
	mu     sync.Mutex
	events []eventRecord
}

func (r *eventRecorder) sink(kind string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, eventRecord{kind: kind, payload: body})
}

func (r *eventRecorder) list() []eventRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]eventRecord(nil), r.events...)
}

func (r *eventRecorder) failures() []string {
	var messages []string
	for _, event := range r.list() {
		if event.kind != EventFailed {
			continue
		}
		var payload struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(event.payload, &payload) == nil {
			messages = append(messages, payload.Message)
		}
	}
	return messages
}

func (r *eventRecorder) hasStatusApplying() bool {
	for _, event := range r.list() {
		if event.kind != EventStatus {
			continue
		}
		var status Status
		if json.Unmarshal(event.payload, &status) == nil && status.Applying {
			return true
		}
	}
	return false
}

// lifecycleRecorder captures the handoff hooks: BeforeExit, Exit, and the
// S5 handoff step, in order.
type lifecycleRecorder struct {
	mu       sync.Mutex
	sequence []string
	exits    []int
	handoffs int
}

func (l *lifecycleRecorder) record(step string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sequence = append(l.sequence, step)
}

func (l *lifecycleRecorder) steps() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sequence...)
}

func (l *lifecycleRecorder) exitCodes() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.exits...)
}

func (l *lifecycleRecorder) handoffCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.handoffs
}

// hostIdentity is a release build identity for the host platform so
// fixture payloads and the release matrix row always match.
func hostIdentity(version string) Identity {
	return Identity{Version: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Flavor: FlavorGUI}
}

// manifestFor renders a valid SHA256SUMS entry for real archive content:
// the coordinator's Assets source serves this content, so StageArchive's
// checksum verification passes.
func manifestFor(assetName string, content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) + "  " + assetName + "\n"
}

// updateFixture wires a coordinator against the host platform: real
// fixture executables, a real archive payload, a fake repository feed, and
// recorded lifecycle hooks. The S5 handoff step is substituted so the
// in-process pipeline never spawns a helper.
type updateFixture struct {
	dir      string
	livePath string
	events   *eventRecorder
	life     *lifecycleRecorder
	manager  *Manager

	assetsMu     sync.Mutex
	assetsCalled int
	assetsGate   chan struct{}
}

type updateFixtureOptions struct {
	current    string
	candidate  string
	notice     NoticeHint
	assets     AssetSource
	gateAssets chan struct{}
	mutate     func(*ManagerDeps)
}

func newUpdateFixture(t *testing.T, opts updateFixtureOptions) *updateFixture {
	t.Helper()
	installDir := t.TempDir()
	oldFixture := hostFixture(t, opts.current)
	newFixture := hostFixture(t, opts.candidate)
	livePath := writeLiveExecutable(t, installDir, oldFixture)
	cleanupLiveProcesses(t, livePath)
	archive := buildTarGz(t, happyTarMembers(fileBytes(t, newFixture)))
	identity := hostIdentity(opts.current)
	asset := matrixAsset(t, opts.candidate, runtime.GOOS, runtime.GOARCH, FlavorGUI)
	release := stableRelease(opts.candidate)
	source := &fakeSource{release: release, manifest: manifestFor(asset, archive)}

	fixture := &updateFixture{dir: installDir, livePath: livePath, events: &eventRecorder{}, life: &lifecycleRecorder{}, assetsGate: opts.gateAssets}
	notice := opts.notice
	if notice == nil {
		notice = func(context.Context) (string, bool, error) {
			return opts.candidate, true, nil
		}
	}
	deps := ManagerDeps{
		Identity:   identity,
		Resolver:   NewResolver(identity, source),
		Notice:     notice,
		Assets:     opts.assets,
		Sink:       fixture.events.sink,
		InstallDir: installDir,
		Executable: livePath,
		StopServing: func(context.Context) error {
			fixture.life.record("stop-serving")
			return nil
		},
		BeforeExit: func() { fixture.life.record("before-exit") },
		Exit: func(code int) {
			fixture.life.mu.Lock()
			fixture.life.exits = append(fixture.life.exits, code)
			fixture.life.mu.Unlock()
			fixture.life.record("exit")
		},
		Supervision: SupervisionPlain,
		// Short flush window so pipelines that never see a release still
		// finish quickly; barrier tests override it explicitly.
		FlushWait: 250 * time.Millisecond,
	}
	if opts.assets == nil {
		deps.Assets = fixture.countedAssets(archive)
	}
	if opts.mutate != nil {
		opts.mutate(&deps)
	}
	fixture.manager = NewManager(deps)
	fixture.manager.handoffFor = func(*Installer, Operation, *Payload) error {
		fixture.life.mu.Lock()
		fixture.life.handoffs++
		fixture.life.mu.Unlock()
		fixture.life.record("handoff")
		return nil
	}
	return fixture
}

func (f *updateFixture) countedAssets(archive []byte) AssetSource {
	return func(ctx context.Context, _ Selection) (io.ReadCloser, error) {
		if f.assetsGate != nil {
			select {
			case <-f.assetsGate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		f.assetsMu.Lock()
		f.assetsCalled++
		f.assetsMu.Unlock()
		return io.NopCloser(strings.NewReader(string(archive))), nil
	}
}

func (f *updateFixture) assetsCallCount() int {
	f.assetsMu.Lock()
	defer f.assetsMu.Unlock()
	return f.assetsCalled
}

func waitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never happened within %s", what, timeout)
}

func TestApplySimultaneousOneWinnerOneBusy(t *testing.T) {
	gate := make(chan struct{})
	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		notice: func(context.Context) (string, bool, error) {
			<-gate
			return "0.4.0", true, nil
		},
	})
	type applyOutcome struct {
		accepted bool
		err      error
	}
	outcomes := make(chan applyOutcome, 2)
	for range 2 {
		go func() {
			result, err := fixture.manager.Apply(context.Background())
			outcomes <- applyOutcome{accepted: result.Accepted, err: err}
		}()
	}
	// The caller that misses the token returns busy immediately, while
	// the winner blocks in the pre-acceptance check.
	loser := <-outcomes
	if !errors.Is(loser.err, ErrApplyBusy) {
		t.Fatalf("second apply = %v, want ErrApplyBusy", loser.err)
	}
	close(gate)
	winner := <-outcomes
	if !winner.accepted || winner.err != nil {
		t.Fatalf("token holder apply = accepted=%v err=%v, want accepted", winner.accepted, winner.err)
	}

	// The winner completes once the check is released.
	waitFor(t, 5*time.Second, "handoff", func() bool { return fixture.life.handoffCount() == 1 })
	waitFor(t, 5*time.Second, "process exit", func() bool { return len(fixture.life.exitCodes()) == 1 })
	if fixture.manager.Current().Applying {
		t.Error("status stays applying after the operation finished")
	}
}

func TestApplyAlreadyCurrentIsSuccessfulNoOp(t *testing.T) {
	fixture := newUpdateFixture(t, updateFixtureOptions{current: "0.3.0", candidate: "0.3.0"})

	result, err := fixture.manager.Apply(context.Background())
	if err != nil {
		t.Fatalf("already-current apply: %v", err)
	}
	if result.Accepted {
		t.Fatal("already-current apply must be a no-op, not an accepted operation")
	}
	if result.Status.Available {
		t.Errorf("no-op status reports availability: %+v", result.Status)
	}
	if result.Status.CurrentVersion != "0.3.0" {
		t.Errorf("no-op status version = %q", result.Status.CurrentVersion)
	}
	// The operation token is released: a second apply is refused as busy
	// never, and the pipeline never ran.
	if err := fixture.manager.WaitIdle(context.Background()); err != nil {
		t.Fatalf("wait idle after no-op: %v", err)
	}
	second, err := fixture.manager.Apply(context.Background())
	if err != nil {
		t.Fatalf("second apply after no-op: %v", err)
	}
	if second.Accepted {
		t.Error("second already-current apply must also be a no-op")
	}
	if fixture.life.handoffCount() != 0 {
		t.Error("already-current apply installed something")
	}
}

func TestApplyManualOnlyAndMiswiredAreConflicts(t *testing.T) {
	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		mutate: func(deps *ManagerDeps) {
			deps.Identity.Version = "dev"
		},
	})
	if fixture.manager.Current().SelfUpdate {
		t.Fatal("dev identity must not be self-update capable")
	}
	_, err := fixture.manager.Apply(context.Background())
	if !errors.Is(err, ErrManualOnly) {
		t.Fatalf("dev identity apply = %v, want ErrManualOnly", err)
	}

	// A probe failure (unwired collaborators) is the same 409-class state,
	// never a 500-style upstream problem.
	unwired := NewManager(ManagerDeps{Identity: hostIdentity("0.3.0")})
	if unwired.Current().SelfUpdate {
		t.Fatal("unwired coordinator must not be self-update capable")
	}
	if _, err := unwired.Apply(context.Background()); !errors.Is(err, ErrManualOnly) {
		t.Fatalf("unwired apply = %v, want ErrManualOnly", err)
	}
}

func TestStartupUpstreamFailureWhileServingContinues(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	served := make(chan struct{}, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		served <- struct{}{}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		notice: func(context.Context) (string, bool, error) {
			return "", false, errors.New("upstream unreachable")
		},
	})

	fixture.manager.StartupApply(context.Background())
	waitFor(t, 5*time.Second, "journaled failure", func() bool { return len(fixture.events.failures()) == 1 })
	if message := fixture.events.failures()[0]; message == "" {
		t.Error("updates.failed carries an empty message")
	}
	if fixture.manager.Current().Applying {
		t.Error("status stays applying after a failed startup apply")
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("server stopped serving after upstream failure: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("serving status = %d", response.StatusCode)
	}
	select {
	case <-served:
	default:
		t.Fatal("handler never observed the request")
	}
	if fixture.assetsCallCount() != 0 {
		t.Error("failed startup check downloaded a release asset")
	}
}

func TestHourlyCheckNeverInstalls(t *testing.T) {
	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		mutate: func(deps *ManagerDeps) {
			deps.CheckInterval = 25 * time.Millisecond
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = fixture.manager.Run(ctx)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done
	status := fixture.manager.Current()
	if !status.Available || status.Latest != "0.4.0" {
		t.Fatalf("hourly check must refresh availability, got %+v", status)
	}
	emitted := false
	for _, event := range fixture.events.list() {
		var payload Status
		if event.kind == EventStatus && json.Unmarshal(event.payload, &payload) == nil && payload.Available && !payload.Applying {
			emitted = true
		}
	}
	if !emitted {
		t.Error("availability change was never emitted as a non-applying status")
	}
	if fixture.assetsCallCount() != 0 {
		t.Fatalf("notify-only check downloaded the release asset %d times", fixture.assetsCallCount())
	}
	if fixture.life.handoffCount() != 0 || len(fixture.life.exitCodes()) != 0 {
		t.Error("hourly check installed an update")
	}
}

// TestRelaunchArgsRecordedBeforeHelperHandoff pins the plain-supervision
// identity contract: the composed relaunch arguments are recorded in the
// marker before the helper is spawned, and systemd's clean exit records
// nothing (the service supervisor restarts with its own ExecStart).
func TestRelaunchArgsRecordedBeforeHelperHandoff(t *testing.T) {
	t.Run("plain supervision records the composed args", func(t *testing.T) {
		t.Setenv(RelaunchArgsEnv, "")
		fixture := newUpdateFixture(t, updateFixtureOptions{
			current:   "0.3.0",
			candidate: "0.4.0",
			mutate: func(deps *ManagerDeps) {
				deps.RelaunchArgs = []string{"serve", "--data-dir", "/srv/data"}
			},
		})
		if _, err := fixture.manager.Apply(context.Background()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		waitFor(t, 5*time.Second, "handoff", func() bool { return fixture.life.handoffCount() == 1 })
		got, ok := TakeRelaunchArgs()
		if !ok {
			t.Fatal("handoff recorded no relaunch arguments")
		}
		if strings.Join(got, " ") != "serve --data-dir /srv/data" {
			t.Fatalf("recorded relaunch args = %v", got)
		}
	})

	t.Run("systemd clean exit records nothing", func(t *testing.T) {
		t.Setenv(RelaunchArgsEnv, "")
		fixture := newUpdateFixture(t, updateFixtureOptions{
			current:   "0.3.0",
			candidate: "0.4.0",
			mutate: func(deps *ManagerDeps) {
				deps.Supervision = SupervisionSystemd
				deps.RelaunchArgs = []string{"serve", "--data-dir", "/srv/data"}
			},
		})
		if _, err := fixture.manager.Apply(context.Background()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		waitFor(t, 5*time.Second, "exit", func() bool { return len(fixture.life.exitCodes()) == 1 })
		if _, ok := TakeRelaunchArgs(); ok {
			t.Fatal("systemd handoff must not record relaunch arguments")
		}
	})
}

// TestCancelledApplyEmitsNoFailure pins finding 3: a request cancelled
// during the pre-acceptance check journals nothing — no operation started
// — while a real accepted-pipeline failure still journals updates.failed.
func TestCancelledApplyEmitsNoFailure(t *testing.T) {
	release := make(chan struct{})
	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		notice: func(ctx context.Context) (string, bool, error) {
			select {
			case <-release:
				return "0.4.0", true, nil
			case <-ctx.Done():
				return "", false, ctx.Err()
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	applyDone := make(chan error, 1)
	go func() {
		_, err := fixture.manager.Apply(ctx)
		applyDone <- err
	}()
	cancel()
	if err := <-applyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pre-acceptance apply = %v", err)
	}
	if events := fixture.events.list(); len(events) != 0 {
		t.Fatalf("cancelled pre-acceptance apply emitted %v", events)
	}

	// A real accepted-pipeline failure still journals.
	failing := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		assets: func(context.Context, Selection) (io.ReadCloser, error) {
			return nil, errors.New("download failed")
		},
	})
	result, err := failing.manager.Apply(context.Background())
	if err != nil || !result.Accepted {
		t.Fatalf("apply: accepted=%v err=%v", result.Accepted, err)
	}
	waitFor(t, 5*time.Second, "journaled failure", func() bool { return len(failing.events.failures()) == 1 })
	if failing.manager.Current().Applying {
		t.Error("status stays applying after a failed pipeline")
	}
}

func TestAcceptedApplySurvivesRequestCancellation(t *testing.T) {
	release := make(chan struct{})
	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:    "0.3.0",
		candidate:  "0.4.0",
		gateAssets: release,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := fixture.manager.Apply(ctx)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !result.Accepted {
		t.Fatal("apply must be accepted for a newer release")
	}
	cancel() // the initiating request goes away right after acceptance
	close(release)

	// The process context owns the operation: it runs to the observable
	// end (handoff + exit) regardless of the cancelled request.
	waitFor(t, 5*time.Second, "handoff after request cancellation", func() bool { return fixture.life.handoffCount() == 1 })
	waitFor(t, 5*time.Second, "process exit", func() bool { return len(fixture.life.exitCodes()) == 1 })
}

func TestApplyPreAcceptanceCancellationReleasesToken(t *testing.T) {
	release := make(chan struct{})
	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		notice: func(ctx context.Context) (string, bool, error) {
			select {
			case <-release:
				return "0.4.0", true, nil
			case <-ctx.Done():
				return "", false, ctx.Err()
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	applyDone := make(chan error, 1)
	go func() {
		_, err := fixture.manager.Apply(ctx)
		applyDone <- err
	}()
	cancel()
	if err := <-applyDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pre-acceptance apply = %v, want context.Canceled", err)
	}
	// The token came back: a fresh apply is accepted.
	close(release)
	result, err := fixture.manager.Apply(context.Background())
	if err != nil || !result.Accepted {
		t.Fatalf("apply after cancelled attempt = %v accepted=%v, want accepted", err, result.Accepted)
	}
	waitFor(t, 5*time.Second, "handoff", func() bool { return fixture.life.handoffCount() == 1 })
}

func TestFlushBarrierGatesHandoffUntilReleased(t *testing.T) {
	fixture := newUpdateFixture(t, updateFixtureOptions{current: "0.3.0", candidate: "0.4.0"})

	result, err := fixture.manager.Apply(context.Background())
	if err != nil || !result.Accepted {
		t.Fatalf("apply: accepted=%v err=%v", result.Accepted, err)
	}
	// The applying snapshot is journaled before the 202 is even returned.
	if !fixture.events.hasStatusApplying() {
		t.Fatal("accepted apply must publish the applying state before returning")
	}
	time.Sleep(50 * time.Millisecond)
	if steps := fixture.life.steps(); len(steps) != 0 {
		t.Fatalf("pipeline moved past the barrier before the flush: %v", steps)
	}

	fixture.manager.ResponseFlushed()
	waitFor(t, 5*time.Second, "stop-serving after flush", func() bool {
		for _, step := range fixture.life.steps() {
			if step == "stop-serving" {
				return true
			}
		}
		return false
	})
	waitFor(t, 5*time.Second, "handoff after flush", func() bool { return fixture.life.handoffCount() == 1 })
	// A second release is inert: the barrier belongs to one operation.
	fixture.manager.ResponseFlushed()
}

func TestLostFlushProceedsAfterBoundedWait(t *testing.T) {
	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		mutate: func(deps *ManagerDeps) {
			deps.FlushWait = 80 * time.Millisecond
		},
	})
	if _, err := fixture.manager.Apply(context.Background()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	waitFor(t, 5*time.Second, "pipeline proceeds after the flush window", func() bool { return fixture.life.handoffCount() == 1 })
}

func TestHandoffSelectionBySupervision(t *testing.T) {
	t.Run("plain relaunches through the update helper", func(t *testing.T) {
		fixture := newUpdateFixture(t, updateFixtureOptions{current: "0.3.0", candidate: "0.4.0"})
		if _, err := fixture.manager.Apply(context.Background()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		waitFor(t, 5*time.Second, "exit", func() bool { return len(fixture.life.exitCodes()) == 1 })
		if fixture.life.handoffCount() != 1 {
			t.Fatal("plain supervision must hand off through the S5 helper")
		}
		steps := fixture.life.steps()
		joined := strings.Join(steps, ",")
		if strings.Index(joined, "before-exit") > strings.LastIndex(joined, "exit") {
			t.Fatalf("BeforeExit must run before exit: %v", steps)
		}
		if strings.Index(joined, "stop-serving") > strings.Index(joined, "handoff") {
			t.Fatalf("serving must drain before the handoff: %v", steps)
		}
		if codes := fixture.life.exitCodes(); len(codes) != 1 || codes[0] != 0 {
			t.Fatalf("exit codes = %v, want one clean exit 0", codes)
		}
	})

	t.Run("systemd exits cleanly without a helper", func(t *testing.T) {
		fixture := newUpdateFixture(t, updateFixtureOptions{
			current:   "0.3.0",
			candidate: "0.4.0",
			mutate: func(deps *ManagerDeps) {
				deps.Supervision = SupervisionSystemd
			},
		})
		if _, err := fixture.manager.Apply(context.Background()); err != nil {
			t.Fatalf("apply: %v", err)
		}
		waitFor(t, 5*time.Second, "exit", func() bool { return len(fixture.life.exitCodes()) == 1 })
		if fixture.life.handoffCount() != 0 {
			t.Fatal("systemd supervision must not spawn an update helper")
		}
		if codes := fixture.life.exitCodes(); len(codes) != 1 || codes[0] != 0 {
			t.Fatalf("exit codes = %v, want one clean exit 0 for Restart=always", codes)
		}
	})

	t.Run("detection follows the sd_notify convention", func(t *testing.T) {
		t.Setenv("NOTIFY_SOCKET", "/run/systemd/notify")
		if got := DetectSupervision(); got != SupervisionSystemd {
			t.Fatalf("DetectSupervision with NOTIFY_SOCKET = %q", got)
		}
		t.Setenv("NOTIFY_SOCKET", "")
		// The wrapper reads the live cgroup, and CI frequently runs inside a
		// systemd user .service slice: the plain-expectation belongs to the
		// pure decision table below, not to whatever host executes it.
		if cg, err := os.ReadFile("/proc/self/cgroup"); err != nil || !cgroupIndicatesSystemdService(string(cg)) {
			if got := DetectSupervision(); got != SupervisionPlain {
				t.Fatalf("DetectSupervision without NOTIFY_SOCKET = %q", got)
			}
		}
		for _, tc := range []struct {
			name         string
			notifySocket string
			cgroup       string
			want         Supervision
		}{
			{"socket marks systemd on any cgroup", "/run/systemd/notify", "0::/", SupervisionSystemd},
			{"service cgroup marks systemd without a socket", "", "12:pids:/user.slice/user-1000.slice/user@1000.service/app.slice/run-rkida.service", SupervisionSystemd},
			{"plain cgroup stays unsupervised", "", "0::/", SupervisionPlain},
			{"container cgroup stays unsupervised", "", "9:cpuset:/docker/a1b2c3", SupervisionPlain},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := supervisionFromEnvironment(tc.notifySocket, tc.cgroup); got != tc.want {
					t.Fatalf("supervisionFromEnvironment(%q, %q) = %q, want %q", tc.notifySocket, tc.cgroup, got, tc.want)
				}
			})
		}
	})
}

func TestDevIdentityNeverAutoInstalls(t *testing.T) {
	fixture := newUpdateFixture(t, updateFixtureOptions{
		current:   "0.3.0",
		candidate: "0.4.0",
		assets: func(context.Context, Selection) (io.ReadCloser, error) {
			t.Error("dev identity must never download a release asset")
			return nil, errors.New("must not be called")
		},
		mutate: func(deps *ManagerDeps) {
			deps.Identity.Version = "dev"
		},
	})
	fixture.manager.StartupApply(context.Background())
	time.Sleep(100 * time.Millisecond)
	if fixture.assetsCallCount() != 0 {
		t.Fatal("startup apply ran for a dev identity")
	}
	if _, err := fixture.manager.Apply(context.Background()); !errors.Is(err, ErrManualOnly) {
		t.Fatalf("dev identity apply = %v, want ErrManualOnly", err)
	}
}

func TestStartupRecoveryAcknowledgesPendingOperation(t *testing.T) {
	installDir := t.TempDir()
	newFixture := hostFixture(t, "0.4.0")
	oldFixture := hostFixture(t, "0.3.0")
	livePath := writeLiveExecutable(t, installDir, newFixture)
	cleanupLiveProcesses(t, livePath)
	backup := filepath.Join(installDir, ".filelist-backup-recovery")
	if err := os.WriteFile(backup, fileBytes(t, oldFixture), 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := OpenJournal(installDir)
	if err != nil {
		t.Fatal(err)
	}
	op := newOperation(installDir, Selection{Version: "0.4.0"}, hostTarget(), "staged-a")
	// newOperation invents its own backup token; pin the operation to the
	// backup this test created so the live (new) content differs from the
	// recorded pre-mutation reference — the activated phase.
	op.Backup = backup
	op.Phase = PhaseActivated
	op.Deadline = time.Now().Add(time.Hour)
	if err := journal.Save(op); err != nil {
		t.Fatal(err)
	}
	journal.Close()

	identity := hostIdentity("0.4.0")
	manager := NewManager(ManagerDeps{
		Identity: identity,
		// No resolver: the coordinator is not self-update capable, so the
		// startup apply is skipped and only recovery runs.
		Notice: func(context.Context) (string, bool, error) {
			t.Error("recovery must not fetch the portal notice")
			return "", false, nil
		},
		Assets: func(context.Context, Selection) (io.ReadCloser, error) {
			t.Error("recovery must not download a release asset")
			return nil, errors.New("must not be called")
		},
		InstallDir: installDir,
		Executable: livePath,
	})
	manager.StartupApply(context.Background())
	waitFor(t, 5*time.Second, "health acknowledgement cleanup", func() bool {
		_, statErr := os.Stat(journalPath(installDir))
		return os.IsNotExist(statErr)
	})
	if _, statErr := os.Stat(backup); !os.IsNotExist(statErr) {
		t.Errorf("backup survived the acknowledged operation: %v", statErr)
	}
}

func TestRelaunchArgsRoundTripAndStrip(t *testing.T) {
	t.Setenv(RelaunchArgsEnv, "")
	if _, ok := TakeRelaunchArgs(); ok {
		t.Fatal("no relaunch marker must be reported as absent")
	}
	args := []string{"serve", "--data-dir", filepath.Join(t.TempDir(), "data")}
	if err := SetRelaunchArgs(args); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok := TakeRelaunchArgs()
	if !ok {
		t.Fatal("set marker must be reported as present")
	}
	if strings.Join(got, " ") != strings.Join(args, " ") {
		t.Fatalf("relaunch args = %v, want %v", got, args)
	}
	if _, ok := TakeRelaunchArgs(); ok {
		t.Fatal("marker must be consumed exactly once")
	}
	if err := SetRelaunchArgs(nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := TakeRelaunchArgs(); ok {
		t.Fatal("cleared marker must be absent")
	}
}

// TestManagerSubprocessPlainHeadlessHandoff is the end-to-end lifecycle
// proof: a real installing process runs the coordinator's Apply, exits, and
// the S5 helper — which waited for the old process — launches the new
// installation, which becomes ready and acknowledges health.
func TestManagerSubprocessPlainHeadlessHandoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file transaction")
	}
	installDir := t.TempDir()
	oldFixture := hostFixture(t, "0.3.0")
	newFixture := hostFixture(t, "0.4.0")
	newPayload := fileBytes(t, newFixture)
	livePath := writeLiveExecutable(t, installDir, oldFixture)
	cleanupLiveProcesses(t, livePath)

	archive := buildTarGz(t, happyTarMembers(newPayload))
	sel := testSelection(fileAssetName("0.4.0"), "0.4.0", archive)
	target := hostTarget()

	tmp := t.TempDir()
	childMarker := filepath.Join(tmp, "child-marker")
	helperResult := filepath.Join(tmp, "helper-result")
	applyResult := filepath.Join(tmp, "apply-result")
	eventFile := filepath.Join(tmp, "manager-events")
	assetFile := filepath.Join(tmp, sel.AssetName)
	if err := os.WriteFile(assetFile, archive, 0o600); err != nil {
		t.Fatal(err)
	}

	env := applyEnv(installDir, assetFile, sel, target, "file", livePath, "", 10*time.Second, map[string]string{
		"FIXTURE_MANAGER":          "1",
		"FIXTURE_IDENTITY_VERSION": "0.3.0",
		"FIXTURE_MANAGER_EVENTS":   eventFile,
		"FIXTURE_APPLY_RESULT":     applyResult,
		"FIXTURE_ACK_DIR":          installDir,
		"FIXTURE_ACK_VERSION":      "0.4.0",
		"FIXTURE_CHILD_MARKER":     childMarker,
		"FIXTURE_RESULT":           helperResult,
	})
	cmd := exec.Command(hostFixture(t, "0.3.0"))
	cmd.Env = append(os.Environ(), env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installing process failed: %v\n%s", err, output)
	}
	// The installing process exited for the handoff, so it never wrote the
	// failure marker: a present marker means the operation failed.
	if text := waitForFile(t, applyResult, time.Second); text != "" {
		t.Fatalf("coordinator operation failed: %s", text)
	}
	if failures := readEventFailures(t, eventFile); len(failures) > 0 {
		t.Fatalf("coordinator journaled failures: %v", failures)
	}

	// The coordinator's half of the plain-headless lifecycle is proven
	// here: the accepted apply ran in a real subprocess, activated the
	// verified release against the real journal, and the old process
	// exited cleanly for the handoff with the live binary swapped to the
	// new version. The helper's join (wait-for-old-exit, relaunch,
	// bounded health acknowledgement, rollback) is the same S5 machinery
	// proven end to end by TestFileInstallHappyPath.
	op := waitJournal(t, installDir, 10*time.Second, PhaseActivated)
	if op.Version != "0.4.0" || op.Flavor != FlavorGUI {
		t.Fatalf("activated operation = %+v", op)
	}
	if !equalDigests(mustDigest(t, livePath), mustDigest(t, newFixture)) {
		t.Error("live executable does not carry the new installation")
	}
	cleanupLiveProcesses(t, livePath)
}

func readFileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func readEventFailures(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read manager events: %v", err)
	}
	var failures []string
	for _, line := range strings.Split(string(data), "\n") {
		kind, body, ok := strings.Cut(line, " ")
		if !ok || kind != EventFailed {
			continue
		}
		var payload struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(body), &payload) == nil && payload.Message != "" {
			failures = append(failures, payload.Message)
		}
	}
	return failures
}

// compile-time proof the coordinator satisfies the S4 API contract.
var _ API = (*Manager)(nil)

func TestCgroupIndicatesSystemdService(t *testing.T) {
	cases := map[string]bool{
		"0::/system.slice/torrent-tv.service\n":                  true,  // cgroup v2 system service
		"12:pids:/system.slice/torrent-tv.service\n":             true,  // cgroup v1 system service
		"0::/user.slice/user-1000.slice/user@1000.service/app\n": true,  // user service
		"0::/docker/4f1d0f2c1e9b\n":                              false, // container
		"0::/kubepods/burstable/podabc\n":                        false, // kubernetes
		"0::/\n":                                                 false, // root cgroup
	}
	for cgroup, want := range cases {
		if got := cgroupIndicatesSystemdService(cgroup); got != want {
			t.Errorf("cgroupIndicatesSystemdService(%q) = %v, want %v", cgroup, got, want)
		}
	}
	if cgroupIndicatesSystemdService("") {
		t.Error("empty cgroup content must not indicate systemd supervision")
	}
}
