package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/adapters/httpapi"
	"github.com/mihaiflorentin88/torrent-tv/internal/application"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/autostart"
	"github.com/mihaiflorentin88/torrent-tv/internal/platform/config"
)

// dynamicCanStart mirrors the runner's CanStart: required settings missing is
// a refusal (setup), not a failure.
func dynamicCanStart(store *config.Store) func() error {
	return func() error {
		if missing := store.MissingRequired(); len(missing) > 0 {
			return fmt.Errorf("required settings missing: %s", strings.Join(missing, ", "))
		}
		return nil
	}
}

// newBindingsFixture wires a Bindings to a supervisor whose appFactory always
// yields the given appLike — the same fake injection supervisor_test uses.
func newBindingsFixture(t *testing.T, app appLike, store *config.Store, canStart func() error) (*Bindings, *Supervisor) {
	t.Helper()
	sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: store, CanStart: canStart})
	sup.appFactory = func() (appLike, error) { return app, nil }
	b := &Bindings{settings: store, sup: sup, dataDir: t.TempDir(), dataDirSource: "default"}
	return b, sup
}

// completeStore loads a store whose settings file already provides every
// required key, so saves never trip the completing-setup auto-start edge.
func completeStore(t *testing.T, dir string) *config.Store {
	t.Helper()
	path := filepath.Join(dir, "settings.json")
	body, err := json.Marshal(map[string]any{
		"databasePath":      filepath.Join(dir, "filelist.db"),
		"downloadRoot":      filepath.Join(dir, "downloads"),
		"torrentSessionDir": filepath.Join(dir, "torrent-session"),
		"fileListUsername":  "user",
		"fileListPasskey":   "stored-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.LoadAt(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSaveSettingsCompletingRequiredAutoStarts(t *testing.T) {
	dir := t.TempDir()
	store, err := config.LoadAt(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if missing := store.MissingRequired(); len(missing) != 3 {
		t.Fatalf("fresh store missing = %v, want all three required keys", missing)
	}
	// A serve channel keeps the fake app blocked in ListenAndServe, so the
	// auto-started server settles in running and stays there.
	app := &fakeApp{addr: "127.0.0.1:8097", serve: make(chan error), closed: make(chan struct{})}
	b, sup := newBindingsFixture(t, app, store, dynamicCanStart(store))

	next := store.Get()
	next.DownloadRoot = filepath.Join(dir, "downloads")
	next.TorrentSessionDir = filepath.Join(dir, "torrent-session")
	next.FileListUsername = "user"
	next.FileListPasskey = "passkey"
	res, err := b.SaveSettings(next)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || !res.AutoStarted {
		t.Fatalf("completing save = %+v, want saved+autoStarted", res)
	}
	waitForState(t, sup, StateRunning)
	if ev := b.ServerState(); ev.State != StateRunning || ev.Address != app.addr {
		t.Fatalf("ServerState after auto-start = %+v", ev)
	}

	// A follow-up ordinary save on a complete, running server must not
	// re-trigger the auto-start edge.
	ordinary := store.Get()
	ordinary.InstanceName = "Renamed"
	res2, err := b.SaveSettings(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Saved || res2.AutoStarted || res2.RestartRequired {
		t.Fatalf("ordinary save = %+v, want saved without auto-start or restart", res2)
	}
}

func TestSaveSettingsMirrorsHTTPContract(t *testing.T) {
	dir := t.TempDir()
	store := completeStore(t, dir)
	b, _ := newBindingsFixture(t, &fakeApp{}, store, nil)

	// An unwritable download root fails the native-path probe with the path
	// in the error, and the rejected save mutates nothing.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := store.Get()
	next.DownloadRoot = filepath.Join(blocker, "sub")
	if _, err := b.SaveSettings(next); err == nil || !strings.Contains(err.Error(), next.DownloadRoot) {
		t.Fatalf("unwritable root error = %v, want it to mention %s", err, next.DownloadRoot)
	}
	if store.Get().DownloadRoot == next.DownloadRoot {
		t.Fatal("rejected save mutated the download root")
	}

	// A listener change is restart-required; a plain rename is not.
	listener := store.Get()
	listener.ListenAddress = ":9999"
	res, err := b.SaveSettings(listener)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || !res.RestartRequired || res.AutoStarted {
		t.Fatalf("listener save = %+v, want saved+restartRequired", res)
	}
	plain := store.Get()
	plain.InstanceName = "Renamed"
	res2, err := b.SaveSettings(plain)
	if err != nil {
		t.Fatal(err)
	}
	if res2.RestartRequired {
		t.Fatalf("rename save = %+v, want no restartRequired", res2)
	}

	// An empty secret keeps the stored value on disk (Save's merge), exactly
	// like the HTTP PUT.
	blank := store.Get()
	blank.FileListPasskey = ""
	res3, err := b.SaveSettings(blank)
	if err != nil || !res3.Saved {
		t.Fatalf("empty-secret save = %+v, %v", res3, err)
	}
	if store.Get().FileListPasskey != "stored-secret" {
		t.Fatalf("empty submission clobbered the stored passkey: %q", store.Get().FileListPasskey)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "stored-secret") {
		t.Fatalf("settings file lost the stored secret: %s", data)
	}
}

func TestAutostartBindingBuildsMinimizedDataDirEntry(t *testing.T) {
	b := &Bindings{dataDir: "/opt/fs/data"}
	b.exePathFn = func() (string, error) { return "/opt/fs/torrent-tv", nil }
	var captured autostart.Options
	b.autostartEnable = func(opts autostart.Options) error { captured = opts; return nil }
	disables := 0
	b.autostartDisable = func() error { disables++; return nil }

	if err := b.EnableAutostart(); err != nil {
		t.Fatal(err)
	}
	if captured.ExePath != "/opt/fs/torrent-tv" {
		t.Fatalf("autostart exe = %q", captured.ExePath)
	}
	want := []string{"--minimized", "--data-dir", "/opt/fs/data"}
	if !reflect.DeepEqual(captured.Args, want) {
		t.Fatalf("autostart args = %v, want %v", captured.Args, want)
	}
	if err := b.DisableAutostart(); err != nil {
		t.Fatal(err)
	}
	if disables != 1 {
		t.Fatalf("DisableAutostart calls = %d, want 1", disables)
	}
}

func TestAutostartBindingUsesRealOSState(t *testing.T) {
	dir := setAutostartTestDir(t) // skips on platforms without injectable dirs
	b := &Bindings{dataDir: dir, dataDirSource: "default"}
	if err := b.EnableAutostart(); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.AutostartStatus(); err != nil || !ok {
		t.Fatalf("AutostartStatus after Enable = %v, %v; want true", ok, err)
	}
	if err := b.DisableAutostart(); err != nil {
		t.Fatal(err)
	}
	if ok, err := b.AutostartStatus(); err != nil || ok {
		t.Fatalf("AutostartStatus after Disable = %v, %v; want false", ok, err)
	}
}

func TestServerStateBindingTracksSupervisor(t *testing.T) {
	store := testStore(t)
	b, sup := newBindingsFixture(t, &fakeApp{addr: "127.0.0.1:8097", serve: make(chan error), closed: make(chan struct{})}, store, nil)
	if ev := b.ServerState(); ev.State != StateStopped || ev.Error != "" {
		t.Fatalf("initial ServerState = %+v, want stopped", ev)
	}
	if err := sup.Start(); err != nil {
		t.Fatal(err)
	}
	waitForState(t, sup, StateRunning)
	if ev := b.ServerState(); ev.State != StateRunning || ev.Address != "127.0.0.1:8097" {
		t.Fatalf("running ServerState = %+v", ev)
	}

	failStore := testStore(t)
	failed, failSup := newBindingsFixture(t, &fakeApp{serveErr: errors.New("bind: address in use"), closed: make(chan struct{})}, failStore, nil)
	if err := failed.StartServer(); err != nil {
		t.Fatal(err)
	}
	waitForState(t, failSup, StateFailed)
	if ev := failed.ServerState(); ev.State != StateFailed || ev.Error != "bind: address in use" {
		t.Fatalf("failed ServerState = %+v", ev)
	}
}

// A wildcard listen (the ":8097" default) must surface as a resolvable
// host:port on page mounts that miss the last server:state event — the
// raw hostless form rendered "Running on http://:8097".
func TestServerStateBindingDisplaysWildcardHost(t *testing.T) {
	store := testStore(t)
	b, sup := newBindingsFixture(t, &fakeApp{addr: ":8097", serve: make(chan error), closed: make(chan struct{})}, store, nil)
	if err := sup.Start(); err != nil {
		t.Fatal(err)
	}
	waitForState(t, sup, StateRunning)
	ev := b.ServerState()
	host, port, err := net.SplitHostPort(ev.Address)
	if err != nil || port != "8097" || host == "" {
		t.Fatalf("running ServerState address = %q, want a resolvable host with port 8097", ev.Address)
	}
}

func TestOpenPathAndWebUIUseOSOpeners(t *testing.T) {
	dir := t.TempDir()
	b := &Bindings{settings: testStore(t), sup: NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: testStore(t)}), dataDir: dir, dataDirSource: "default"}
	var opened []string
	b.revealFn = func(path string) error { opened = append(opened, path); return nil }
	b.openURLFn = func(url string) error { opened = append(opened, url); return nil }

	if err := b.OpenPath("logs"); err != nil {
		t.Fatal(err)
	}
	if err := b.OpenPath("data"); err != nil {
		t.Fatal(err)
	}
	if err := b.OpenPath("bogus"); err == nil {
		t.Fatal("OpenPath(bogus) must be refused")
	}
	want := []string{filepath.Join(dir, "logs"), dir}
	if !reflect.DeepEqual(opened, want) {
		t.Fatalf("revealed = %v, want %v", opened, want)
	}

	// Stopped, the port falls back to the configured listen address; a
	// running server wins with its actual address.
	if err := b.OpenWebUI(); err != nil {
		t.Fatalf("OpenWebUI with configured listener = %v", err)
	}
	if opened[len(opened)-1] != "http://127.0.0.1:8097" {
		t.Fatalf("opened = %v, want loopback URL", opened)
	}

	sup := NewSupervisor(SupervisorDeps{Log: testLogger(), Settings: b.settings})
	sup.appFactory = func() (appLike, error) {
		return &fakeApp{addr: "127.0.0.1:8123", serve: make(chan error), closed: make(chan struct{})}, nil
	}
	b.sup = sup
	if err := sup.Start(); err != nil {
		t.Fatal(err)
	}
	waitForState(t, sup, StateRunning)
	if err := b.OpenWebUI(); err != nil {
		t.Fatal(err)
	}
	if opened[len(opened)-1] != "http://127.0.0.1:8123" {
		t.Fatalf("opened = %v, want running server's URL", opened)
	}
}

// TestOpenURLRestrictsSchemes pins the Downloads Play handoff's safety
// contract: only http(s) reaches the OS opener, and the URL goes through
// the injectable invoker — tests never open anything for real.
func TestOpenURLRestrictsSchemes(t *testing.T) {
	b := &Bindings{}
	var opened []string
	b.openURLFn = func(url string) error { opened = append(opened, url); return nil }

	cases := []struct {
		url string
		ok  bool
	}{
		{"http://127.0.0.1:8097/watch/d1", true},
		{"https://example.com/watch/x?source=2#t=30", true},
		{"HTTP://uppercase.scheme", true},
		{"ftp://example.com/file", false},
		{"file:///etc/passwd", false},
		{"javascript:alert(1)", false},
		{"example.com/no-scheme", false},
		{"://missing-scheme", false},
		{"", false},
		{"http://", false},
		{"https://", false},
		{"http:///path/only", false},
	}
	for _, tc := range cases {
		err := b.OpenURL(tc.url)
		if tc.ok && err != nil {
			t.Fatalf("OpenURL(%q) = %v, want accepted", tc.url, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("OpenURL(%q) accepted, want refused", tc.url)
		}
	}
	if len(opened) != 3 {
		t.Fatalf("opened %d URLs, want only the 3 http(s) ones: %v", len(opened), opened)
	}
	if opened[0] != "http://127.0.0.1:8097/watch/d1" {
		t.Fatalf("opened[0] = %q", opened[0])
	}
}

// parityRepo satisfies application.Repository with panicking defaults; the
// settings surfaces only touch the store.
type parityRepo struct{ application.Repository }

// TestBindingsSettingsSurfaceParityWithHTTP pins that LoadSettings and
// SettingsSchema serve byte-identical JSON shapes to GET /api/v1/settings
// and /api/v1/settings/schema for the same store.
func TestBindingsSettingsSurfaceParityWithHTTP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvironmentPrefix+"LISTEN_ADDRESS", ":9999") // exercises readOnly flags
	path := filepath.Join(dir, "settings.json")
	body, err := json.Marshal(map[string]any{
		"databasePath":      filepath.Join(dir, "filelist.db"),
		"downloadRoot":      filepath.Join(dir, "downloads"),
		"torrentSessionDir": filepath.Join(dir, "torrent-session"),
		"trustedCidrs":      []string{"127.0.0.0/8", "::1/128", "192.0.2.0/24"},
		"fileListUsername":  "user",
		"fileListPasskey":   "stored-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.LoadAt(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(application.NewService(nil, nil, parityRepo{}, store), store, testLogger(), "test")
	b := &Bindings{settings: store}

	getJSON := func(target any, url string, payload any) {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d: %s", url, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}

	var wantSettings map[string]any
	getJSON(&wantSettings, "/api/v1/settings", nil)
	gotSettings, err := json.Marshal(b.LoadSettings())
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(gotSettings, &settings); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantSettings, settings) {
		t.Fatalf("LoadSettings JSON diverges from HTTP:\nwant %v\ngot  %v", wantSettings, settings)
	}

	var wantSchema struct {
		Items []map[string]any `json:"items"`
	}
	getJSON(&wantSchema, "/api/v1/settings/schema", nil)
	gotSchema, err := json.Marshal(map[string]any{"items": b.SettingsSchema()})
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(gotSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantSchema, schema) {
		t.Fatalf("SettingsSchema JSON diverges from HTTP:\nwant %v\ngot  %v", wantSchema, schema)
	}

	if missing := b.MissingRequired(); len(missing) != 0 {
		t.Fatalf("complete store reports missing = %v", missing)
	}
}
