package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/application/portal"
	"github.com/mihaiflorentin88/torrent-tv/internal/application/updates"
	"github.com/mihaiflorentin88/torrent-tv/internal/domain"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRequiredEnginesMatrix pins the composition-sizing matrix: the
// default engine is always required, the other prefix only when persisted
// rows reference it, and duplicate persisted prefixes collapse.
func TestRequiredEnginesMatrix(t *testing.T) {
	cases := []struct {
		name          string
		defaultPrefix string
		persisted     []string
		want          []string
	}{
		{"native default, no rows", "native:", nil, []string{"native:"}},
		{"qb default, qb rows only", "qb:", []string{"qb:"}, []string{"qb:"}},
		{"qb default, native legacy rows", "qb:", []string{"native:"}, []string{"native:", "qb:"}},
		{"native default, both rows", "native:", []string{"qb:", "native:"}, []string{"native:", "qb:"}},
		{"duplicate persisted rows", "qb:", []string{"native:", "native:"}, []string{"native:", "qb:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := requiredEngines(tc.defaultPrefix, tc.persisted)
			if len(got) != len(tc.want) {
				t.Fatalf("requiredEngines(%q, %v) = %v; want %v", tc.defaultPrefix, tc.persisted, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("requiredEngines(%q, %v) = %v; want %v", tc.defaultPrefix, tc.persisted, got, tc.want)
				}
			}
		})
	}
}

// TestLegacyNativeFailurePolicy exercises the exact policy wiring the
// container applies (requiredEngines -> NewEngineSet -> MarkUnavailable):
// with qb as default and a legacy native row whose engine failed to
// construct, startup proceeds, the default stays qb:, the native prefix
// reports its construction error, and native routes stop resolving.
// nativetorrent.New has no injectable failure seam and none is added to
// production, so the failing engine is simulated at the same
// application.EngineSet seam the container feeds.
func TestLegacyNativeFailurePolicy(t *testing.T) {
	defaultPrefix := "qb:"
	persisted := []string{"native:"}

	// Mirrors the container: build only required engines. qb: always
	// constructs; native: "fails" — the container's policy is
	// MarkUnavailable + warn rather than abort.
	var engines map[string]application.TorrentEngine
	for _, prefix := range requiredEngines(defaultPrefix, persisted) {
		if engines == nil {
			engines = map[string]application.TorrentEngine{}
		}
		engines[prefix] = &stubEngine{}
	}

	set, err := application.NewEngineSet(defaultPrefix, engines)
	if err != nil {
		t.Fatalf("NewEngineSet: %v", err)
	}
	nativeErr := errors.New("nativetorrent: session open failed")
	set.MarkUnavailable("native:", nativeErr)

	if got := set.DefaultPrefix(); got != "qb:" {
		t.Fatalf("DefaultPrefix() = %q; want qb:", got)
	}
	initErr, ok := set.InitError("native:")
	if !ok || !errors.Is(initErr, nativeErr) {
		t.Fatalf("InitError(native:) = %v, %v; want nativeErr, true", initErr, ok)
	}
	if _, _, ok := set.Resolve("native:abc"); ok {
		t.Fatal("Resolve(native:abc) ok after failed native construction; want false")
	}
	if set.Default() == nil {
		t.Fatal("Default() nil; qb default engine must remain routable")
	}
}

// stubEngine satisfies application.TorrentEngine for composition tests.
type stubEngine struct{}

func (stubEngine) Test(context.Context) (string, error) { return "", errors.New("stub") }
func (stubEngine) Add(context.Context, io.Reader, string) (string, error) {
	return "", errors.New("stub")
}

func (stubEngine) Files(context.Context, string) ([]domain.TorrentFile, error) {
	return nil, errors.New("stub")
}

func (stubEngine) Status(context.Context, string) (domain.DownloadStatus, error) {
	return domain.DownloadStatus{}, errors.New("stub")
}

func (stubEngine) Pieces(context.Context, string) (domain.PieceMap, error) {
	return domain.PieceMap{}, errors.New("stub")
}

func (stubEngine) PrepareFile(context.Context, string, int, []int) error { return errors.New("stub") }

func (stubEngine) PrepareFiles(context.Context, string, []int, []int) error {
	return errors.New("stub")
}

func (stubEngine) PrepareRange(context.Context, string, int, int64, int64) error {
	return errors.New("stub")
}
func (stubEngine) Pause(context.Context, string) error        { return errors.New("stub") }
func (stubEngine) Resume(context.Context, string) error       { return errors.New("stub") }
func (stubEngine) Remove(context.Context, string, bool) error { return errors.New("stub") }
func (stubEngine) ResolveMagnet(context.Context, string, string, domain.MagnetDiscovery) ([]byte, error) {
	return nil, errors.New("stub")
}

// TestNewAtLoadsExplicitPath pins the constructor serve and the GUI
// supervisor share: the explicit path wins over the environment, so the
// settings store is exactly the file the caller resolved.
func TestNewAtLoadsExplicitPath(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	body := `{"databasePath": "` + filepath.Join(dir, "test.db") + `",` +
		` "torrentSessionDir": "` + filepath.Join(dir, "torrent") + `",` +
		` "artworkCachePath": "` + filepath.Join(dir, "artwork") + `",` +
		` "downloadRoot": "` + filepath.Join(dir, "downloads") + `"}`
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvironmentPrefix+"SETTINGS_PATH", filepath.Join(dir, "not-this.json"))

	app, err := NewAt(settingsPath, testLogger())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	defer app.Close(context.Background())
	if got := app.Settings.Path(); got != settingsPath {
		t.Fatalf("NewAt must load the explicit path %q, got %q", settingsPath, got)
	}
}

// TestNewAtEnvManagedPathsWin pins the other half of the precedence
// contract: an env-set setting overrides the file's value at runtime
// (LoadAt semantics), while the file keeps carrying what was written.
func TestNewAtEnvManagedPathsWin(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	body := `{"databasePath": "` + filepath.Join(dir, "from-file.db") + `",` +
		` "torrentSessionDir": "` + filepath.Join(dir, "torrent") + `",` +
		` "artworkCachePath": "` + filepath.Join(dir, "artwork") + `",` +
		` "downloadRoot": "` + filepath.Join(dir, "downloads") + `"}`
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	envDB := filepath.Join(dir, "from-env.db")
	t.Setenv(config.EnvironmentPrefix+"DATABASE_PATH", envDB)

	app, err := NewAt(settingsPath, testLogger())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	defer app.Close(context.Background())
	if got := app.Settings.Get().DatabasePath; got != envDB {
		t.Fatalf("env-managed databasePath must win at runtime, got %q", got)
	}
	if !strings.Contains(app.Settings.Path(), "settings.json") {
		t.Fatalf("settings path must stay %q", settingsPath)
	}
}

// TestAssembleWiresIntegrationBeforeBinding pins the construction order:
// NewAt returns the app with the portal hub and the self-update
// coordinator fully constructed — the coordinator's status is the cached
// initialization snapshot, self-update capability probed once, never on
// GET.
func TestAssembleWiresIntegrationBeforeBinding(t *testing.T) {
	app := newTestApp(t)
	if app.Service == nil || app.Portal == nil || app.Updates == nil {
		t.Fatalf("assemble must wire service, portal hub, and update coordinator: %+v", app)
	}
	status := app.Updates.Current()
	if status.CurrentVersion != Version {
		t.Fatalf("current version = %q, want the ldflags-injected %q", status.CurrentVersion, Version)
	}
	if status.Applying || status.Available {
		t.Errorf("initial status must be idle: %+v", status)
	}
	if status.ReleasesURL == "" {
		t.Error("status must carry the repository releases URL")
	}
	snapshot := app.Portal.Snapshot()
	if snapshot.AccountsEnabled || snapshot.AdsEnabled || snapshot.Donor || len(snapshot.Links) != 0 {
		t.Errorf("initial portal snapshot must be inactive: %+v", snapshot)
	}
}

// TestAppCloseJoinsIntegrationsAndClosesThroughService pins the shutdown
// contract on a really serving app: Close cancels and joins the
// integration loops, drains the listener, and routes engine/repository
// closure through Service.Close (the repository refuses queries after it).
func TestAppCloseJoinsIntegrationsAndClosesThroughService(t *testing.T) {
	app := newTestApp(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	address := listener.Addr().String()
	listener.Close()
	app.Server.Addr = address
	serveDone := make(chan error, 1)
	go func() { serveDone <- app.ListenAndServe() }()
	waitFor(t, 5*time.Second, "listener to accept", func() bool {
		conn, dialErr := net.DialTimeout("tcp", address, 250*time.Millisecond)
		if dialErr != nil {
			return false
		}
		conn.Close()
		return true
	})

	// Production shutdown order: the HTTP server drains first (signal
	// path or supervisor Stop), then Close joins loops and service.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := app.Server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("server shutdown: %v", err)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve error: %v", err)
	}
	if _, err := app.Repository.ListEvents(context.Background(), 0, 1); err == nil {
		t.Error("repository must be closed after App.Close")
	}
	if err := app.Service.Close(context.Background()); err != nil {
		t.Errorf("Service.Close after App.Close must be an idempotent no-op, got %v", err)
	}
	if err := app.Close(context.Background()); err != nil {
		t.Errorf("second Close must be idempotent, got %v", err)
	}
}

// TestListenAndServeTriggersStartupUpdateAtReadiness pins the trigger
// point: the startup update fires exactly once the live listener accepts —
// not at construction, not at shutdown.
func TestListenAndServeTriggersStartupUpdateAtReadiness(t *testing.T) {
	app := newTestApp(t)
	fired := make(chan struct{}, 1)
	app.onStartupUpdate = func() { fired <- struct{}{} }
	app.Server.Addr = "127.0.0.1:0"
	serveDone := make(chan error, 1)
	go func() { serveDone <- app.ListenAndServe() }()
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		t.Fatal("listener readiness never triggered the startup update")
	}
	select {
	case <-fired:
		t.Fatal("startup update triggered twice")
	case <-time.After(100 * time.Millisecond):
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serve error: %v", err)
	}
}

// TestAssembledAppServesPortalAndUpdateRoutes pins the handler wiring:
// the assembled app's HTTP surface mounts the integration routes backed
// by the very hub and coordinator that Close joins.
func TestAssembledAppServesPortalAndUpdateRoutes(t *testing.T) {
	app := newTestApp(t)
	server := httptest.NewServer(app.Server.Handler)
	t.Cleanup(server.Close)

	res, err := http.Get(server.URL + "/api/v1/portal/state")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/portal/state = %d, body %s", res.StatusCode, body)
	}
	var snapshot portal.Snapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.AccountsEnabled || snapshot.AdsEnabled || snapshot.Donor || snapshot.Links == nil {
		t.Fatalf("absent gates must hide the surfaces with non-null links: %+v", snapshot)
	}

	res, err = http.Get(server.URL + "/api/v1/updates/current")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/updates/current = %d, body %s", res.StatusCode, body)
	}
	var status updates.Status
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != Version || status.ReleasesURL == "" {
		t.Fatalf("current status = %+v, want the build identity and a releases URL", status)
	}
}

// newTestApp assembles an app against a temporary settings file. The peer
// port pins to 0 (the engine's documented OS-assigned mode): the 42069
// default binds a global fixed port and collides across parallel test
// binaries and packages.
func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	body := `{"listenAddress": "127.0.0.1:0",` +
		` "torrentPeerPort": 0,` +
		` "databasePath": "` + filepath.Join(dir, "test.db") + `",` +
		` "torrentSessionDir": "` + filepath.Join(dir, "torrent") + `",` +
		` "artworkCachePath": "` + filepath.Join(dir, "artwork") + `",` +
		` "downloadRoot": "` + filepath.Join(dir, "downloads") + `"}`
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := NewAt(settingsPath, testLogger())
	if err != nil {
		t.Fatalf("NewAt: %v", err)
	}
	t.Cleanup(func() { _ = app.Close(context.Background()) })
	return app
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

func TestNewReleaseFeedRequestCarriesOptionalGitHubToken(t *testing.T) {
	t.Setenv("TORRENT_TV_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	anonymous, err := newReleaseFeedRequest(context.Background())
	if err != nil {
		t.Fatalf("anonymous request: %v", err)
	}
	if anonymous.Header.Get("Authorization") != "" {
		t.Fatalf("anonymous request carried Authorization %q", anonymous.Header.Get("Authorization"))
	}

	t.Setenv("TORRENT_TV_GITHUB_TOKEN", "app-specific-token")
	appSpecific, err := newReleaseFeedRequest(context.Background())
	if err != nil {
		t.Fatalf("app-specific request: %v", err)
	}
	if got := appSpecific.Header.Get("Authorization"); got != "Bearer app-specific-token" {
		t.Fatalf("Authorization = %q, want the app-specific token", got)
	}

	t.Setenv("TORRENT_TV_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gh-convention-token")
	convention, err := newReleaseFeedRequest(context.Background())
	if err != nil {
		t.Fatalf("convention request: %v", err)
	}
	if got := convention.Header.Get("Authorization"); got != "Bearer gh-convention-token" {
		t.Fatalf("Authorization = %q, want the GH_TOKEN fallback", got)
	}
}

// The release CDN occasionally closes the asset body a couple of hundred
// bytes before Content-Length (observed deterministically from a Raspberry
// Pi while curl on the same box receives the full file). The asset source
// completes the tail with a ranged request instead of handing the updater
// a truncated archive.
func TestAssetBodyCompletesShortTail(t *testing.T) {
	payload := bytes.Repeat([]byte("torrent-tv-update-payload-"), 400) // ~10KiB
	total := int64(len(payload))
	shortAt := total - 200

	mux := http.NewServeMux()
	served := map[string]int{}
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		served[r.Header.Get("Range")]++
		body := payload
		if fl := r.Header.Get("Range"); fl != "" {
			start, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(fl, "bytes="), "-"), 10, 64)
			if err != nil || start > total {
				http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			body = payload[start:]
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, total-1, total))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			// The CDN advertises the full length but the connection dies
			// before the tail: the header lies, the body ends early.
			w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
			body = payload[:shortAt]
		}
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/asset", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body := &completingAssetBody{
		ctx: context.Background(), client: client, url: srv.URL + "/asset",
		length: res.ContentLength, body: res.Body, read: 0,
	}

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if int64(len(got)) != total {
		t.Fatalf("completed body = %d bytes, want %d", len(got), total)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("completed body content mismatch")
	}
	if served[""] != 1 {
		t.Fatalf("initial requests = %d, want 1", served[""])
	}
	if served["bytes=0-"] != 0 && len(rangesSeen(served)) == 0 {
		t.Fatal("no ranged resume request was made")
	}
}

func rangesSeen(served map[string]int) []string {
	out := []string{}
	for k := range served {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

// Chunked CDN responses carry no Content-Length; the published asset size
// from the release metadata then drives the tail completion.
func TestAssetBodyCompletesChunkedShortTail(t *testing.T) {
	payload := bytes.Repeat([]byte("chunked-updater-payload-"), 400) // ~9600 bytes
	total := int64(len(payload))
	shortAt := total - 150

	mux := http.NewServeMux()
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		if fl := r.Header.Get("Range"); fl != "" {
			start, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(fl, "bytes="), "-"), 10, 64)
			if err != nil || start > total {
				http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[start:])
			return
		}
		// Chunked transfer, early clean close, no Content-Length at all.
		_, _ = w.Write(payload[:shortAt])
		// Closing the handler ends the chunked body with its terminal
		// chunk (a clean EOF for the client) before the full length.
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/chunked", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body := &completingAssetBody{
		ctx: context.Background(), client: client, url: srv.URL + "/chunked",
		length: total, body: res.Body, read: 0,
	}

	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if int64(len(got)) != total {
		t.Fatalf("completed body = %d bytes, want %d", len(got), total)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("chunked completion content mismatch")
	}
}
