// Command updatefixture stands in for the real installation binary in
// update transaction tests. Apply mode drives one installation transaction
// and exits; manager mode drives the coordinator's full Apply pipeline and
// exits through the handoff like the real installing process; helper mode
// runs the updates helper; normal mode performs the readiness health
// acknowledgement and then idles like a serving installation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/application/updates"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Apply mode: drive one staging/activation/handoff transaction, then
	// exit so the helper — which waits for this process — can take over.
	if os.Getenv("FIXTURE_APPLY") == "1" {
		if err := applyErr(); err != nil {
			writeNamed("FIXTURE_APPLY_RESULT", err.Error())
			os.Exit(1)
		}
		writeNamed("FIXTURE_APPLY_RESULT", "")
		return
	}

	// Manager mode: drive the coordinator's Apply — fresh check, accept,
	// barrier, staging, activation, handoff — and exit the process exactly
	// like the real installing binary. A failed operation returns here and
	// is reported through FIXTURE_APPLY_RESULT.
	if os.Getenv("FIXTURE_MANAGER") == "1" {
		os.Unsetenv("FIXTURE_MANAGER")
		if err := managerApply(); err != nil {
			writeNamed("FIXTURE_APPLY_RESULT", err.Error())
			os.Exit(1)
		}
		writeNamed("FIXTURE_APPLY_RESULT", "")
		return
	}

	// Helper mode: the updates helper transaction. The outcome is written
	// to FIXTURE_RESULT for test assertions. Normal installations never
	// touch that file: they only serve.
	handled, err := updates.RunUpdateHelper(ctx)
	if handled {
		writeResult(handled, err)
		if err != nil {
			os.Exit(1)
		}
		return
	}
	serve()
}

// applyErr runs the full transaction described by the FIXTURE_* environment,
// mirroring what the installing process does before exiting.
func applyErr() error {
	installDir := os.Getenv("FIXTURE_INSTALL_DIR")
	kind := updates.PayloadFile
	if os.Getenv("FIXTURE_KIND") == "bundle" {
		kind = updates.PayloadBundle
	}
	archive, err := os.ReadFile(os.Getenv("FIXTURE_ASSET_FILE"))
	if err != nil {
		return fmt.Errorf("read staged asset: %w", err)
	}
	sel := updates.Selection{
		Version:   os.Getenv("FIXTURE_ASSET_VERSION"),
		AssetName: os.Getenv("FIXTURE_ASSET_NAME"),
		SHA256:    os.Getenv("FIXTURE_ASSET_SHA"),
	}
	target := updates.Target{
		GOOS:   os.Getenv("FIXTURE_TARGET_GOOS"),
		GOARCH: os.Getenv("FIXTURE_TARGET_GOARCH"),
		Flavor: os.Getenv("FIXTURE_TARGET_FLAVOR"),
	}
	journal, err := updates.OpenJournal(installDir)
	if err != nil {
		return err
	}
	defer journal.Close()
	installer := updates.NewInstaller(journal, kind,
		os.Getenv("FIXTURE_LIVE_PATH"), os.Getenv("FIXTURE_BUNDLE_PATH"),
		time.Duration(envMillis("FIXTURE_HEALTH_TIMEOUT_MS"))*time.Millisecond)
	staged, err := updates.StageArchive(installDir, sel, newByteReader(archive), updates.DefaultLimits())
	if err != nil {
		return err
	}
	payload, err := staged.Extract(installDir, target, updates.DefaultLimits())
	if err != nil {
		return err
	}
	op, err := installer.Prepare(payload, sel, target, staged.Path)
	if err != nil {
		return err
	}
	op, err = installer.Activate(op, payload)
	if err != nil {
		return err
	}
	// The helper and the launched installation must not re-enter apply mode
	// when they run this same binary.
	os.Unsetenv("FIXTURE_APPLY")
	return installer.Handoff(op, payload)
}

// serve emulates a normal installation: readiness marker, optional startup
// delay, optional early death, health acknowledgement against the journal,
// then idle until signalled.
func serve() {
	if os.Getenv("FIXTURE_EXIT_IMMEDIATELY") == "1" {
		if path := os.Getenv("FIXTURE_EXIT_FILE"); path != "" {
			os.WriteFile(path, []byte(fmt.Sprintf("exited %d", os.Getpid())), 0o644)
		}
		os.Exit(3)
	}
	marker := os.Getenv("FIXTURE_CHILD_MARKER")
	if marker != "" {
		os.WriteFile(marker, []byte(fmt.Sprintf("ready %d", os.Getpid())), 0o644)
	}
	if delay := envMillis("FIXTURE_ACK_DELAY_MS"); delay > 0 {
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}
	ackDir := os.Getenv("FIXTURE_ACK_DIR")
	if ackDir == "" {
		idle()
		return
	}
	op, found, err := updates.LoadOperation(ackDir)
	if err != nil || !found {
		noteAckError(marker, fmt.Sprintf("load operation: %v", err))
		idle()
		return
	}
	// The running installation acknowledges without updater ownership.
	if err := updates.AcknowledgeOperation(ackDir, op.ID, os.Getenv("FIXTURE_ACK_VERSION")); err != nil {
		noteAckError(marker, fmt.Sprintf("acknowledge: %v", err))
		idle()
		return
	}
	if marker != "" {
		os.WriteFile(marker, []byte(fmt.Sprintf("acked %d", os.Getpid())), 0o644)
	}
	idle()
}

func idle() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
	case <-time.After(9 * time.Minute):
	}
}

func noteAckError(marker, text string) {
	if marker != "" {
		os.WriteFile(marker, []byte("ack-error: "+text), 0o644)
		return
	}
	fmt.Fprintln(os.Stderr, text)
}

func writeResult(handled bool, err error) {
	path := os.Getenv("FIXTURE_RESULT")
	if path == "" {
		return
	}
	payload := struct {
		Handled bool   `json:"handled"`
		Error   string `json:"error,omitempty"`
	}{Handled: handled}
	if err != nil {
		payload.Error = err.Error()
	}
	data, mErr := json.Marshal(payload)
	if mErr == nil {
		os.WriteFile(path, data, 0o644)
	}
}

func writeNamed(env, text string) {
	path := os.Getenv(env)
	if path == "" {
		return
	}
	os.WriteFile(path, []byte(text), 0o644)
}

func envMillis(name string) int64 {
	text := os.Getenv(name)
	if text == "" {
		return 0
	}
	var value int64
	fmt.Sscanf(text, "%d", &value)
	return value
}

// managerApply drives the coordinator's full Apply pipeline from the
// FIXTURE_* environment. A successful handoff exits the process from
// inside the pipeline (the default Exit), so WaitIdle returning on this
// path always means the operation finished without a handoff: the event
// journal carries the neutral updates.failed message.
func managerApply() error {
	sel := updates.Selection{
		Version:   os.Getenv("FIXTURE_ASSET_VERSION"),
		AssetName: os.Getenv("FIXTURE_ASSET_NAME"),
		SHA256:    os.Getenv("FIXTURE_ASSET_SHA"),
	}
	identity := updates.Identity{
		Version: os.Getenv("FIXTURE_IDENTITY_VERSION"),
		GOOS:    os.Getenv("FIXTURE_TARGET_GOOS"),
		GOARCH:  os.Getenv("FIXTURE_TARGET_GOARCH"),
		Flavor:  os.Getenv("FIXTURE_TARGET_FLAVOR"),
	}
	events := &eventFile{path: os.Getenv("FIXTURE_MANAGER_EVENTS")}
	manager := updates.NewManager(updates.ManagerDeps{
		Identity: identity,
		Resolver: updates.NewResolver(identity, fixtureSource{sel: sel}),
		Notice: func(context.Context) (string, bool, error) {
			return sel.Version, true, nil
		},
		Assets: func(_ context.Context, selection updates.Selection) (io.ReadCloser, error) {
			return os.Open(os.Getenv("FIXTURE_ASSET_FILE"))
		},
		Sink:         events.emit,
		InstallDir:   os.Getenv("FIXTURE_INSTALL_DIR"),
		Executable:   os.Getenv("FIXTURE_LIVE_PATH"),
		RelaunchArgs: nil,
	})
	result, err := manager.Apply(context.Background())
	if err != nil {
		return err
	}
	if !result.Accepted {
		return fmt.Errorf("apply not accepted: %+v", result.Status)
	}
	// No HTTP response is flushed in the fixture: release the accepted
	// apply's handoff barrier immediately, like the real CLI path.
	manager.ResponseFlushed()
	if err := manager.WaitIdle(context.Background()); err != nil {
		return err
	}
	return fmt.Errorf("operation finished without a handoff: %s", events.lastFailure())
}

// fixtureSource serves one fixed selection from memory, shaped like the
// repository feed the resolver validates against.
type fixtureSource struct {
	sel updates.Selection
}

func (s fixtureSource) LatestRelease(context.Context) (updates.Release, error) {
	return updates.Release{
		Tag: "v" + s.sel.Version,
		URL: "https://github.com/mihaiflorentin88/torrent-tv/releases/tag/v" + s.sel.Version,
		Assets: []updates.Asset{
			{Name: s.sel.AssetName, URL: "https://github.com/mihaiflorentin88/torrent-tv/releases/download/v" + s.sel.Version + "/" + s.sel.AssetName},
			{Name: "SHA256SUMS", URL: "https://github.com/mihaiflorentin88/torrent-tv/releases/download/v" + s.sel.Version + "/SHA256SUMS"},
		},
	}, nil
}

func (s fixtureSource) ChecksumManifest(_ context.Context, _ string) (string, error) {
	return s.sel.SHA256 + "  " + s.sel.AssetName + "\n", nil
}

// eventFile journals emitted events as JSON lines so a test can read the
// coordinator's updates.failed message out of a subprocess.
type eventFile struct {
	mu   sync.Mutex
	path string
}

func (e *eventFile) emit(kind string, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	f, err := os.OpenFile(e.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", kind, body)
}

func (e *eventFile) lastFailure() string {
	data, err := os.ReadFile(e.path)
	if err != nil {
		return ""
	}
	message := ""
	for _, line := range strings.Split(string(data), "\n") {
		kind, body, ok := strings.Cut(line, " ")
		if !ok || kind != updates.EventFailed {
			continue
		}
		var payload struct {
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(body), &payload) == nil && payload.Message != "" {
			message = payload.Message
		}
	}
	return message
}

type sliceReader struct {
	data []byte
	pos  int
}

func newByteReader(data []byte) *sliceReader { return &sliceReader{data: data} }

func (s *sliceReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += n
	return n, nil
}
