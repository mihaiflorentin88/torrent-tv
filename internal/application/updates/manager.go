// Manager is the update coordinator: it owns the cached update status,
// the notify-only check cadence, the single accepted apply operation, and
// the process handoff that installs the verified release. It implements
// the API contract from types.go — Current is cached only, Check performs
// a fresh fetch through the S4 resolver, Apply turns an accepted request
// into a staged, verified installation — and drives the S5 installer.
//
// The coordinator never blocks serving: startup applies are bounded and
// fail open, hourly checks only notify, and an upstream failure is a
// journaled updates.failed event, never a serving outage.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sink receives updater events. The service publisher is the production
// sink; the payload is the marshalled event body.
type Sink func(kind string, payload any)

// Event kinds published through the sink. updates.status carries a Status,
// updates.failed carries {"message": "<neutral text>"}.
const (
	EventStatus = "updates.status"
	EventFailed = "updates.failed"
)

// NoticeHint fetches the portal announcement hint for Check and Apply.
// ok is false when the upstream publishes no notice: without a hint
// nothing resolves, so availability is cleared instead of going stale.
type NoticeHint func(ctx context.Context) (version string, ok bool, err error)

// AssetSource downloads the resolved release asset. The coordinator
// streams the body straight into StageArchive, so the source must return
// the raw response body, never a buffered copy.
type AssetSource func(ctx context.Context, sel Selection) (io.ReadCloser, error)

// Supervision decides how an activated installation hands the process
// over to the new version.
type Supervision string

const (
	// SupervisionPlain hands off through the S5 update helper: the helper
	// waits for this process to exit, launches the new installation, and
	// rolls back if it never becomes healthy.
	SupervisionPlain Supervision = "plain"
	// SupervisionSystemd exits cleanly with code 0 after activation: the
	// service supervisor (Restart=always) relaunches into the new
	// installation, whose startup recovery acknowledges health.
	SupervisionSystemd Supervision = "systemd"
)

// DetectSupervision reports the process supervision from the environment:
// a NOTIFY_SOCKET is the sd_notify convention that marks a systemd service,
// and a cgroup path ending in a .service unit marks a real systemd service
// of any type — Type=simple units never receive NOTIFY_SOCKET, and the
// update-helper relaunch dies with the service cgroup under systemd's
// default KillMode. Everything else uses the update-helper relaunch.
func DetectSupervision() Supervision {
	if os.Getenv("NOTIFY_SOCKET") != "" {
		return SupervisionSystemd
	}
	if cg, err := os.ReadFile("/proc/self/cgroup"); err == nil &&
		cgroupIndicatesSystemdService(string(cg)) {
		return SupervisionSystemd
	}
	return SupervisionPlain
}

// cgroupIndicatesSystemdService reports whether a /proc/self/cgroup payload
// places the process inside a systemd-managed .service unit — any path
// element ending in .service counts (user services nest deeper, e.g.
// user@1000.service/app.slice/...). Containers, Kubernetes pods, and the
// root cgroup stay unsupervised.
func cgroupIndicatesSystemdService(cgroup string) bool {
	for _, line := range strings.Split(cgroup, "\n") {
		for _, element := range strings.Split(strings.TrimSpace(line), "/") {
			if strings.HasSuffix(element, ".service") {
				return true
			}
		}
	}
	return false
}

// RelaunchArgsEnv carries the JSON-encoded arguments a helper-relaunched
// installation must serve with. It deliberately lives outside the
// FILELIST_UPDATE_* namespace so the helper's environment scrub keeps it
// intact for the launched process; the entry point consumes it once and
// strips it, so it is the only update marker that survives a handoff.
const RelaunchArgsEnv = "TORRENT_TV_RELAUNCH_ARGS"

// SetRelaunchArgs records the invocation a relaunched installation must
// resume. The coordinator calls it before a helper handoff so arguments
// and data-dir identity survive the restart.
func SetRelaunchArgs(args []string) error {
	if len(args) == 0 {
		return os.Unsetenv(RelaunchArgsEnv)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("encode relaunch args: %w", err)
	}
	return os.Setenv(RelaunchArgsEnv, string(encoded))
}

// TakeRelaunchArgs parses and removes the relaunch marker. ok is false
// when this process was not relaunched by an update handoff.
func TakeRelaunchArgs() ([]string, bool) {
	raw := os.Getenv(RelaunchArgsEnv)
	if raw == "" {
		return nil, false
	}
	if err := os.Unsetenv(RelaunchArgsEnv); err != nil {
		return nil, false
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil || len(args) == 0 {
		return nil, false
	}
	return args, true
}

// ErrApplyBusy marks a rejected apply because one operation is already in
// flight. The HTTP layer maps it to 409.
var ErrApplyBusy = errors.New("updates: an apply operation is already in progress")

// Cadence and deadline defaults. The check interval is the plan's hourly
// notify-only cadence; the operation timeout bounds one accepted apply so
// a wedged download can never hold the process hostage; the flush wait
// bounds the response barrier so a lost HTTP flush can never strand the
// pipeline before the handoff.
const (
	defaultCheckInterval    = time.Hour
	defaultOperationTimeout = 30 * time.Minute
	defaultFlushWait        = 30 * time.Second
)

// ManagerDeps carries the coordinator's collaborators. Resolver, Notice,
// and Assets must be non-nil for checks and applies; the rest fall back
// to production defaults.
type ManagerDeps struct {
	Identity Identity
	Resolver *Resolver
	Notice   NoticeHint
	Assets   AssetSource
	Sink     Sink
	Now      func() time.Time
	// Jitter offsets the check cadence; composition injects a bounded
	// nonzero jitter so installations never check in lockstep.
	Jitter func(time.Duration) time.Duration
	// InstallDir is the installation directory: journal, staging, and
	// backups all live here, next to the live executable.
	InstallDir string
	// Executable is the live executable path (the inner launcher for a
	// bundle installation). Empty falls back to os.Executable.
	Executable string
	// StopServing drains HTTP and joins workers/service before the
	// handoff. The handoff aborts — observably — when it fails.
	StopServing func(ctx context.Context) error
	// BeforeExit runs immediately before the process exits for the
	// handoff. The GUI releases its single-instance lock here.
	BeforeExit func()
	// Exit terminates the process; nil falls back to os.Exit. Tests
	// substitute a recorder.
	Exit func(code int)
	// Supervision selects the handoff; empty detects from the environment.
	Supervision Supervision
	// RelaunchArgs travel to a helper-relaunched installation so it
	// resumes the original invocation (minus update markers).
	RelaunchArgs []string
	// CheckInterval is the base notify-only cadence; 0 means hourly.
	CheckInterval time.Duration
	// OperationTimeout bounds one accepted apply; 0 means 30 minutes.
	OperationTimeout time.Duration
	// FlushWait bounds the response barrier wait; 0 means 30 seconds.
	FlushWait time.Duration
}

// Manager implements API. Create one with NewManager; run the notify-only
// cadence with Run; trigger the post-readiness startup sequence with
// StartupApply (once per process).
type Manager struct {
	deps       ManagerDeps
	executable string

	mu       sync.Mutex
	status   Status
	applying bool
	barrier  chan struct{}
	idle     chan struct{}
	cancelOp context.CancelFunc

	startupOnce sync.Once
	// handoffFor installs the S5 handoff step; NewManager binds
	// Installer.Handoff. Tests substitute a recorder so handoff selection
	// is observable without spawning a real helper.
	handoffFor func(i *Installer, op Operation, payload *Payload) error
}

// closedChannel is the released barrier / idle-wait state.
var closedChannel = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// NewManager builds the coordinator. The self-update capability is probed
// once here — build identity, platform support, and a writable
// installation directory — and never on GET: Current serves the cached
// result without disk or network probes.
func NewManager(deps ManagerDeps) *Manager {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Sink == nil {
		deps.Sink = func(string, any) {}
	}
	if deps.Exit == nil {
		deps.Exit = os.Exit
	}
	if deps.Supervision == "" {
		deps.Supervision = DetectSupervision()
	}
	if deps.CheckInterval <= 0 {
		deps.CheckInterval = defaultCheckInterval
	}
	if deps.OperationTimeout <= 0 {
		deps.OperationTimeout = defaultOperationTimeout
	}
	if deps.FlushWait <= 0 {
		deps.FlushWait = defaultFlushWait
	}
	if deps.Executable == "" {
		if exe, err := os.Executable(); err == nil {
			deps.Executable = exe
		}
	}
	m := &Manager{deps: deps, executable: deps.Executable, idle: closedChannel}
	m.handoffFor = func(i *Installer, op Operation, payload *Payload) error {
		return i.Handoff(op, payload)
	}
	m.status = Status{
		CurrentVersion: deps.Identity.Version,
		ReleasesURL:    ReleasesURL,
		SelfUpdate:     m.capable(),
	}
	return m
}

// capable probes whether this installation can resolve and install
// repository releases: a release build identity, wired collaborators, a
// supported payload kind for the detected flavor, and a writable install
// directory. Containers and read-only installations stay notification-only.
func (m *Manager) capable() bool {
	if !SelfUpdateCapable(m.deps.Identity) {
		return false
	}
	if m.deps.Resolver == nil || m.deps.Notice == nil || m.deps.Assets == nil {
		return false
	}
	if m.deps.InstallDir == "" || m.executable == "" {
		return false
	}
	switch m.deps.Identity.Flavor {
	case FlavorBundle:
		if bundlePlatform == nil || m.bundlePath() == "" {
			return false
		}
	default:
		if filePlatform == nil {
			return false
		}
	}
	return dirWritable(m.deps.InstallDir)
}

func dirWritable(dir string) bool {
	file, err := os.CreateTemp(dir, ".filelist-probe-*")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}

// bundlePath resolves the live .app directory for a bundle installation.
func (m *Manager) bundlePath() string {
	dir := filepath.Dir(m.executable)
	for range 4 {
		if strings.HasSuffix(filepath.Base(dir), bundleSuffix) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// Current returns the cached status. It never probes disk or network.
func (m *Manager) Current() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// checkResult pairs the fresh status with the selection an accepted apply
// installs.
type checkResult struct {
	status Status
	sel    Selection
}

// check performs one fresh fetch: the portal notice hint, verified against
// repository release metadata by the S4 resolver. It commits the result to
// the cache and emits updates.status when availability changed. A failed
// fetch clears availability — no stale update is ever offered.
func (m *Manager) check(ctx context.Context) (checkResult, error) {
	if m.deps.Notice == nil || m.deps.Resolver == nil {
		return checkResult{}, errors.New("updates: coordinator is not wired for checks")
	}
	hint, ok, err := m.deps.Notice(ctx)
	if err != nil {
		return checkResult{}, err
	}
	if !ok {
		status := m.baseStatus()
		m.commit(status)
		return checkResult{status: status}, nil
	}
	sel, err := m.deps.Resolver.Resolve(ctx, hint)
	if err != nil {
		return checkResult{}, err
	}
	newer, err := IsNewer(m.deps.Identity.Version, sel.Version)
	if err != nil {
		return checkResult{}, err
	}
	status := m.baseStatus()
	if newer {
		status.Available = true
		status.Latest = sel.Version
		status.Notes = sel.Notes
		if !sel.ReleasedAt.IsZero() {
			status.ReleasedAt = sel.ReleasedAt.UTC().Format(time.RFC3339)
		}
	}
	m.commit(status)
	return checkResult{status: status, sel: sel}, nil
}

// baseStatus builds the fetch-independent part of a status and carries the
// in-flight flag: a check racing an apply must not report applying:false.
func (m *Manager) baseStatus() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Status{
		CurrentVersion: m.deps.Identity.Version,
		ReleasesURL:    ReleasesURL,
		SelfUpdate:     m.status.SelfUpdate,
		Applying:       m.status.Applying,
	}
}

// commit stores the status and emits updates.status when it changed.
func (m *Manager) commit(status Status) {
	m.mu.Lock()
	changed := status != m.status
	if changed {
		m.status = status
	}
	m.mu.Unlock()
	if changed {
		m.emit(EventStatus, status)
	}
}

// Check performs a fresh availability fetch and returns the new status.
func (m *Manager) Check(ctx context.Context) (Status, error) {
	result, err := m.check(ctx)
	if err != nil {
		return Status{}, err
	}
	return result.status, nil
}

// Apply runs the explicit apply flow. Pre-acceptance (notice fetch,
// resolution) honors the caller's context, so a client disconnect cancels
// the check; from acceptance on, installation is owned by the process
// context and survives the request.
//
// An already-current installation is a successful no-op (Accepted false).
// A busy or manual-only installation returns ErrApplyBusy or an
// ErrManualOnly wrap — the HTTP layer maps both to 409. Every other error
// is a neutral upstream, verification, or installation problem.
func (m *Manager) Apply(ctx context.Context) (ApplyResult, error) {
	// HTTP callers get a barrier the handler releases through
	// ResponseFlushed after the accepted response has been flushed; the
	// pipeline never tears anything down before the client heard back.
	return m.beginApply(ctx, make(chan struct{}))
}

// beginApply is Apply with an explicit response barrier: internal callers
// pass a pre-closed channel and the pipeline proceeds without waiting.
func (m *Manager) beginApply(ctx context.Context, barrier chan struct{}) (ApplyResult, error) {
	if err := m.beginOperation(); err != nil {
		return ApplyResult{}, err
	}
	result, err := m.applyAccepted(ctx, barrier)
	if err != nil {
		// A request cancelled during the pre-acceptance check started no
		// operation: release the token silently instead of journaling a
		// failure for a plain context.Canceled.
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			m.finishOperation(nil)
			return ApplyResult{}, err
		}
		m.finishOperation(err)
		return ApplyResult{}, err
	}
	return result, nil
}

// beginOperation takes the single operation token. A refused caller
// leaves the state untouched.
func (m *Manager) beginOperation() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.applying {
		return ErrApplyBusy
	}
	if !m.status.SelfUpdate {
		return fmt.Errorf("%w: capability probe failed or the build identity is not a release version", ErrManualOnly)
	}
	m.applying = true
	m.idle = make(chan struct{})
	return nil
}

// applyAccepted runs the pre-acceptance check and, for a newer release,
// publishes the applying state and starts the installation goroutine on
// the process context.
func (m *Manager) applyAccepted(ctx context.Context, barrier chan struct{}) (ApplyResult, error) {
	checked, err := m.check(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if !checked.status.Available {
		// Already current: a successful no-op — the token is released
		// immediately, with no operation failure to journal.
		m.finishOperation(nil)
		return ApplyResult{Accepted: false, Status: checked.status}, nil
	}
	// Accepted: request cancellation ends here — the installation runs on
	// the process context with its own bounded lifetime.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.deps.OperationTimeout)
	m.mu.Lock()
	m.cancelOp = cancel
	m.barrier = barrier
	applying := m.status
	applying.Applying = true
	m.status = applying
	m.mu.Unlock()
	m.emit(EventStatus, applying)
	go m.install(opCtx, cancel, checked.sel)
	return ApplyResult{Accepted: true, Status: applying}, nil
}

// install is the accepted-apply pipeline: response barrier, HTTP drain and
// service join, download and verification through the S5 stager, activation
// through the S5 installer, then the platform handoff.
func (m *Manager) install(ctx context.Context, cancel context.CancelFunc, sel Selection) {
	defer cancel()
	var failure error
	defer func() { m.finishOperation(failure) }()

	if err := m.awaitBarrier(ctx); err != nil {
		failure = err
		return
	}
	// Drain HTTP and join workers/service before touching the
	// installation: the coordinator closes nothing under live writers,
	// and a failed join aborts the handoff observably.
	if m.deps.StopServing != nil {
		if err := m.deps.StopServing(ctx); err != nil {
			failure = fmt.Errorf("drain serving before handoff: %w", err)
			return
		}
	}
	// A release download occasionally truncates mid-stream and, on flaky
	// storage, an extract can read the staged file short; both surface as
	// digest or archive rejections. Retry the whole download-and-extract
	// sequence a bounded number of times before giving the operation up.
	var payload *Payload
	var stagedPath string
	for attempt := 1; ; attempt++ {
		body, err := m.deps.Assets(ctx, sel)
		if err != nil {
			failure = fmt.Errorf("download release asset: %w", err)
			return
		}
		staged, err := StageArchive(m.deps.InstallDir, sel, body, DefaultLimits())
		body.Close()
		if err != nil {
			failure = fmt.Errorf("stage release asset: %w", err)
		} else {
			stagedPath = staged.Path
			payload, err = staged.Extract(m.deps.InstallDir, m.deps.Identity.Target(), DefaultLimits())
			if err != nil {
				failure = fmt.Errorf("verify staged release: %w", err)
			}
		}
		if err == nil {
			break
		}
		if attempt >= 3 {
			return
		}
		select {
		case <-ctx.Done():
			failure = fmt.Errorf("download release asset: %w", ctx.Err())
			return
		case <-time.After(time.Duration(attempt) * 3 * time.Second):
		}
	}
	journal, err := OpenJournal(m.deps.InstallDir)
	if err != nil {
		failure = fmt.Errorf("acquire update ownership: %w", err)
		return
	}
	defer journal.Close()
	installer := NewInstaller(journal, payload.Kind, m.executable, m.bundlePath(), DefaultHealthTimeout)
	op, err := installer.Prepare(payload, sel, m.deps.Identity.Target(), stagedPath)
	if err != nil {
		failure = err
		return
	}
	if err != nil {
		failure = err
		return
	}
	if op, err = installer.Activate(op, payload); err != nil {
		failure = err
		return
	}
	if m.deps.Supervision == SupervisionSystemd {
		// Clean exit for the service supervisor: Restart=always relaunches
		// into the new installation with the unit's real ExecStart, and
		// its startup recovery acknowledges health. No helper, no relaunch
		// marker.
		m.exitProcess()
		return
	}
	// The S5 helper launches the new installation with no arguments, so
	// the invocation this process served under — data-dir identity
	// included, update markers stripped — must travel in the relaunch
	// marker. A recording failure fails the operation: a headless relaunch
	// without its arguments would die in the root command and roll the
	// server down.
	if err := SetRelaunchArgs(m.deps.RelaunchArgs); err != nil {
		failure = fmt.Errorf("record relaunch arguments: %w", err)
		return
	}
	if err := m.handoffFor(installer, op, payload); err != nil {
		failure = fmt.Errorf("update handoff: %w", err)
		return
	}
	m.exitProcess()
}

// awaitBarrier blocks until the HTTP layer releases the handoff barrier
// (after the accepted response has been flushed), the flush window passes,
// or the operation context dies. A lost flush degrades to proceeding —
// the pipeline must never hang on a missing release.
func (m *Manager) awaitBarrier(ctx context.Context) error {
	m.mu.Lock()
	barrier := m.barrier
	m.mu.Unlock()
	if barrier == nil {
		return nil
	}
	timer := time.NewTimer(m.deps.FlushWait)
	defer timer.Stop()
	select {
	case <-barrier:
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// exitProcess releases host-owned resources (the GUI single-instance
// lock) and exits so the helper — or the service supervisor — can complete
// the relaunch.
func (m *Manager) exitProcess() {
	if m.deps.BeforeExit != nil {
		m.deps.BeforeExit()
	}
	m.deps.Exit(0)
}

// finishOperation clears the token, cancels the operation context, and
// journals the failure — a neutral updates.failed followed by the
// applying:false status snapshot. Handoff errors are observable operation
// failures, not ignored log lines.
func (m *Manager) finishOperation(failure error) {
	m.mu.Lock()
	m.applying = false
	m.barrier = nil
	if m.cancelOp != nil {
		m.cancelOp()
		m.cancelOp = nil
	}
	status := m.status
	wasApplying := status.Applying
	status.Applying = false
	m.status = status
	idle := m.idle
	m.idle = closedChannel
	m.mu.Unlock()
	close(idle)
	if failure != nil {
		m.emit(EventFailed, map[string]string{"message": neutralMessage(failure)})
	}
	if wasApplying {
		m.emit(EventStatus, status)
	}
}

// ResponseFlushed releases the handoff barrier of the accepted operation.
// The HTTP handler calls it after the 202 response has been flushed to the
// client; the coordinator then drains HTTP and proceeds with the handoff.
func (m *Manager) ResponseFlushed() {
	m.mu.Lock()
	barrier := m.barrier
	m.barrier = nil
	m.mu.Unlock()
	if barrier != nil {
		close(barrier)
	}
}

// WaitIdle blocks until no operation is in flight. The --update command
// uses it to stay alive until the handoff exits the process or the
// operation fails observably.
func (m *Manager) WaitIdle(ctx context.Context) error {
	m.mu.Lock()
	idle := m.idle
	m.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartupApply schedules the once-per-process startup sequence: recovery
// of a pending operation with the post-readiness health acknowledgement,
// then — only for self-update-capable installations — one bounded
// automatic apply. The trigger runs independently of notification
// watermarks and fails open: upstream failures are journaled, never
// raised, and a dev identity never auto-installs.
func (m *Manager) StartupApply(ctx context.Context) {
	m.startupOnce.Do(func() {
		base := context.WithoutCancel(ctx)
		go func() {
			m.recoverPending()
			if !m.Current().SelfUpdate {
				return
			}
			opCtx, cancel := context.WithTimeout(base, m.deps.OperationTimeout)
			defer cancel()
			// Internal apply: no HTTP barrier, and every failure already
			// journaled itself through finishOperation.
			_, _ = m.beginApply(opCtx, closedChannel)
		}()
	})
}

// recoverPending evaluates a persisted operation from the running
// installation's side: the healthy new installation acknowledges after
// readiness, a completed rollback consumes its one-shot startup
// suppression, and cleanup/rollback disk effects run through the S5
// installer. No pending operation is the common case.
func (m *Manager) recoverPending() {
	if m.deps.InstallDir == "" {
		return
	}
	if _, found, err := LoadOperation(m.deps.InstallDir); err != nil || !found {
		return
	}
	journal, err := OpenJournal(m.deps.InstallDir)
	if err != nil {
		return
	}
	defer journal.Close()
	op, found, err := journal.Load()
	if err != nil || !found {
		return
	}
	installer := NewInstaller(journal, payloadKindFor(op), m.executable, m.bundlePath(), DefaultHealthTimeout)
	recovery, err := installer.Recover(m.deps.Identity.Version)
	if err != nil {
		return
	}
	switch recovery.Action {
	case RecoveryAcknowledge:
		_ = installer.Acknowledge(m.deps.Identity.Version)
	case RecoverySuppress:
		_ = installer.ConsumeSuppression()
	}
}

// Run drives the notify-only cadence until ctx is cancelled: one jittered
// check per interval, availability committed to the cache and emitted on
// change — never an install. Run is blocking; composition owns and joins
// it through its context.
func (m *Manager) Run(ctx context.Context) error {
	timer := time.NewTimer(m.jittered())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			_, _ = m.check(ctx)
			timer.Reset(m.jittered())
		}
	}
}

// jittered applies the injected bounded nonzero offset to the base
// interval and never collapses to a non-positive cadence.
func (m *Manager) jittered() time.Duration {
	interval := m.deps.CheckInterval
	if m.deps.Jitter != nil {
		if next := interval + m.deps.Jitter(interval); next > 0 {
			return next
		}
	}
	return interval
}

func (m *Manager) emit(kind string, payload any) {
	m.deps.Sink(kind, payload)
}

// neutralMessage maps known lifecycle states to stable, neutral text;
// pipeline errors are already credential-free by construction.
func neutralMessage(err error) string {
	if errors.Is(err, ErrManualOnly) {
		return "this installation cannot update itself automatically"
	}
	return err.Error()
}
