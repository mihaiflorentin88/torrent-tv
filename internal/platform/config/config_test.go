package config

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentOverlayIsAuthoritativeButNotPersisted(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	databasePath := filepath.Join(dir, "environment.db")
	t.Setenv(EnvironmentPrefix+"SETTINGS_PATH", settingsPath)
	t.Setenv(EnvironmentPrefix+"INSTANCE_NAME", "Living room")
	t.Setenv(EnvironmentPrefix+"DATABASE_PATH", databasePath)
	t.Setenv(EnvironmentPrefix+"TRUSTED_CIDRS", "127.0.0.0/8, 192.168.50.0/24")

	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got.InstanceName != "Living room" || got.DatabasePath != databasePath || len(got.TrustedCIDRs) != 2 {
		t.Fatalf("environment overlay was not applied: %#v", got)
	}
	if !store.EnvironmentManaged("instanceName") || !store.EnvironmentManaged("databasePath") {
		t.Fatal("environment-managed settings were not exposed")
	}

	next := store.Get()
	next.InstanceName = "Browser edit"
	if err := store.Save(next); err != nil {
		t.Fatal(err)
	}
	if store.Get().InstanceName != "Living room" {
		t.Fatal("an environment-managed setting was overwritten")
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Living room") || strings.Contains(string(data), databasePath) {
		t.Fatalf("environment values leaked into persisted settings: %s", data)
	}
}

func TestCamelToEnvironmentKeepsReadableSettingNames(t *testing.T) {
	for input, want := range map[string]string{
		"fileListPasskey": "FILE_LIST_PASSKEY",
		"qbittorrentUrl":  "QBITTORRENT_URL",
		"tmdbApiKey":      "TMDB_API_KEY",
	} {
		if got := camelToEnvironment(input); got != want {
			t.Errorf("camelToEnvironment(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAllocationAndReserveRoundTripFileAndFractionalValues(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	t.Setenv(EnvironmentPrefix+"SETTINGS_PATH", settingsPath)
	t.Setenv(EnvironmentPrefix+"DATABASE_PATH", filepath.Join(dir, "retention.db"))
	file := Defaults()
	file.AllocationGB = 0.5
	file.ReserveGB = 8
	b, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got.AllocationGB != 0.5 || got.ReserveGB != 8 {
		t.Fatalf("gigabyte settings did not survive the file round-trip: %#v", got)
	}
}

func TestAllocationEnvironmentOverrideWinsAndNeverPersists(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	t.Setenv(EnvironmentPrefix+"SETTINGS_PATH", settingsPath)
	t.Setenv(EnvironmentPrefix+"DATABASE_PATH", filepath.Join(dir, "retention.db"))
	file := Defaults()
	file.AllocationGB = 0.5
	b, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvironmentPrefix+"ALLOCATION_GB", "25")
	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get(); got.AllocationGB != 25 {
		t.Fatalf("environment allocation override lost: %#v", got)
	}
	if !store.EnvironmentManaged("allocationGb") {
		t.Fatal("allocationGb was not reported environment-managed")
	}
	next := store.Get()
	next.ReserveGB = 9
	if err := store.Save(next); err != nil {
		t.Fatal(err)
	}
	if store.Get().AllocationGB != 25 {
		t.Fatal("an environment-managed allocation was overwritten")
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "25") || !strings.Contains(string(data), `"allocationGb": 0.5`) {
		t.Fatalf("environment allocation leaked into persisted settings: %s", data)
	}
}

func TestAllocationAndReserveRangeValidation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvironmentPrefix+"SETTINGS_PATH", filepath.Join(dir, "settings.json"))
	t.Setenv(EnvironmentPrefix+"DATABASE_PATH", filepath.Join(dir, "retention.db"))
	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name                string
		allocation, reserve float64
		ok                  bool
	}{{"zero disables", 0, 0, true}, {"fractional", 0.5, 8, true}, {"range floor", 0.1, 0.1, true}, {"range ceiling", 100000, 100000, true}, {"negative", -1, 8, false}, {"below floor", 0.05, 8, false}, {"absurd ceiling", 100001, 8, false}, {"not a number", math.NaN(), 8, false}} {
		next := store.Get()
		next.AllocationGB = tt.allocation
		next.ReserveGB = tt.reserve
		err := store.Save(next)
		if tt.ok && err != nil {
			t.Errorf("%s: unexpected error %v", tt.name, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("%s: accepted invalid retention settings", tt.name)
		}
	}
}

func writeSettingsFile(t *testing.T, payload map[string]any) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvironmentPrefix+"SETTINGS_PATH", filepath.Join(dir, "settings.json"))
	t.Setenv(EnvironmentPrefix+"DATABASE_PATH", filepath.Join(dir, "eviction.db"))
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileWithoutEvictionKeys(t *testing.T) map[string]any {
	t.Helper()
	b, err := json.Marshal(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"evictionRules", "protectIncomplete", "protectLeased", "protectFavorites", "protectNeverWatched"} {
		delete(payload, key)
	}
	return payload
}

func TestEvictionDefaultsApplyWhenKeysAreAbsent(t *testing.T) {
	writeSettingsFile(t, fileWithoutEvictionKeys(t))
	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if len(got.EvictionRules) != 1 || got.EvictionRules[0] != "oldest-completed" {
		t.Fatalf("absent evictionRules did not default to oldest-completed: %v", got.EvictionRules)
	}
	if !got.ProtectIncomplete || !got.ProtectLeased || got.ProtectFavorites || got.ProtectNeverWatched {
		t.Fatalf("absent protection toggles did not default to true/true/false/false: %#v", got)
	}
}

func TestEvictionFileValuesAreHonored(t *testing.T) {
	payload := fileWithoutEvictionKeys(t)
	payload["evictionRules"] = []any{"Largest", "watched-first"}
	payload["protectIncomplete"] = false
	payload["protectFavorites"] = true
	writeSettingsFile(t, payload)
	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if len(got.EvictionRules) != 2 || got.EvictionRules[0] != "Largest" || got.EvictionRules[1] != "watched-first" {
		t.Fatalf("explicit evictionRules were lost: %v", got.EvictionRules)
	}
	if got.ProtectIncomplete {
		t.Fatal("an explicitly false protectIncomplete was overridden by the default")
	}
	if !got.ProtectFavorites {
		t.Fatal("an explicitly true protectFavorites was lost")
	}
}

func TestEvictionRulesValidationRejectsUnknownAtoms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvironmentPrefix+"SETTINGS_PATH", filepath.Join(dir, "settings.json"))
	t.Setenv(EnvironmentPrefix+"DATABASE_PATH", filepath.Join(dir, "eviction.db"))
	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	next := store.Get()
	next.EvictionRules = []string{"largest", "shiniest"}
	if err := store.Save(next); err == nil {
		t.Fatal("an unknown eviction atom was accepted")
	} else if !strings.Contains(err.Error(), "evictionRules") || !strings.Contains(err.Error(), "shiniest") {
		t.Fatalf("validation error did not name the field and atom: %v", err)
	}
	next.EvictionRules = []string{" Oldest-Completed ", "NEWEST-COMPLETED", "least-recently-played", "most-recently-played", "watched-first", "never-watched-first", "largest", "smallest"}
	if err := store.Save(next); err != nil {
		t.Fatalf("valid atoms were rejected: %v", err)
	}
	next.EvictionRules = nil
	if err := store.Save(next); err != nil {
		t.Fatalf("an empty rule list must stay saveable: %v", err)
	}
}

func TestProtectionTogglesPersist(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	t.Setenv(EnvironmentPrefix+"SETTINGS_PATH", settingsPath)
	t.Setenv(EnvironmentPrefix+"DATABASE_PATH", filepath.Join(dir, "eviction.db"))
	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	next := store.Get()
	next.ProtectIncomplete = false
	next.ProtectLeased = false
	next.ProtectFavorites = true
	next.ProtectNeverWatched = true
	if err := store.Save(next); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get(); got.ProtectIncomplete || got.ProtectLeased || !got.ProtectFavorites || !got.ProtectNeverWatched {
		t.Fatalf("protection toggles did not persist: %#v", got)
	}
}

func TestEvictionEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	t.Setenv(EnvironmentPrefix+"SETTINGS_PATH", settingsPath)
	t.Setenv(EnvironmentPrefix+"DATABASE_PATH", filepath.Join(dir, "eviction.db"))
	if err := os.WriteFile(settingsPath, func() []byte { b, _ := json.Marshal(Defaults()); return b }(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvironmentPrefix+"EVICTION_RULES", "newest-completed,largest")
	t.Setenv(EnvironmentPrefix+"PROTECT_FAVORITES", "true")
	store, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := store.Get()
	if len(got.EvictionRules) != 2 || got.EvictionRules[0] != "newest-completed" || got.EvictionRules[1] != "largest" {
		t.Fatalf("environment eviction rules were not applied: %v", got.EvictionRules)
	}
	if !got.ProtectFavorites {
		t.Fatal("environment protectFavorites override was not applied")
	}
	if !store.EnvironmentManaged("evictionRules") || !store.EnvironmentManaged("protectFavorites") {
		t.Fatal("eviction settings were not reported environment-managed")
	}
	next := store.Get()
	next.ProtectFavorites = false
	if err := store.Save(next); err != nil {
		t.Fatal(err)
	}
	if store.Get().ProtectFavorites != true {
		t.Fatal("an environment-managed protection toggle was overwritten")
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "newest-completed") {
		t.Fatalf("environment eviction rules leaked into persisted settings: %s", data)
	}
}

func TestNormalizeEvictionRulesFallsBackToOldestCompleted(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input []string
		want  []string
	}{{"nil falls back", nil, []string{"oldest-completed"}}, {"empty falls back", []string{}, []string{"oldest-completed"}}, {"blank entries fall back", []string{"  ", ""}, []string{"oldest-completed"}}, {"trims and lowercases", []string{" Largest ", "SMALLEST"}, []string{"largest", "smallest"}}, {"keeps known atoms", []string{"watched-first", "never-watched-first"}, []string{"watched-first", "never-watched-first"}}, {"passes unknown through", []string{"shiniest"}, []string{"shiniest"}}, {"mixed blank and known", []string{"", "  ", "oldest-completed"}, []string{"oldest-completed"}}} {
		got := NormalizeEvictionRules(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("%s: NormalizeEvictionRules = %v, want %v", tt.name, got, tt.want)
			continue
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("%s: NormalizeEvictionRules = %v, want %v", tt.name, got, tt.want)
				break
			}
		}
	}
}

func TestDownloadEngineValidation(t *testing.T) {
	base := Defaults()
	base.DownloadEngine = "transmission"
	if err := (&Store{}).validate(base); err == nil {
		t.Fatal("unknown downloadEngine must fail validation")
	}
	for _, engine := range []string{"native", "qbittorrent"} {
		base.DownloadEngine = engine
		if err := (&Store{}).validate(base); err != nil {
			t.Fatalf("downloadEngine %q must be valid: %v", engine, err)
		}
	}
	base.DownloadEngine = "native"
	base.TorrentPeerPort = 70000
	if err := (&Store{}).validate(base); err == nil {
		t.Fatal("torrentPeerPort above 65535 must fail validation")
	}
	base.TorrentPeerPort = 42069
	base.TorrentSessionDir = "  "
	if err := (&Store{}).validate(base); err == nil {
		t.Fatal("blank torrentSessionDir must fail validation")
	}
	base.TorrentSessionDir = "data/torrent-session"
	base.TorrentPublicPeerPort = -1
	if err := (&Store{}).validate(base); err == nil {
		t.Fatal("negative torrentPublicPeerPort must fail validation")
	}
	base.TorrentPublicPeerPort = 42069
	if err := (&Store{}).validate(base); err == nil {
		t.Fatal("duplicate fixed peer ports must fail validation")
	}
	base.TorrentPublicPeerPort = 0
	if err := (&Store{}).validate(base); err != nil {
		t.Fatalf("OS-assigned public port must validate: %v", err)
	}
}

func TestLoadAtUsesGivenPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"fileListUsername":"u","fileListPasskey":"p"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	s, err := LoadAt(path)
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if s.Path() != path {
		t.Fatalf("Path() = %q, want %q", s.Path(), path)
	}
	if s.Get().FileListUsername != "u" {
		t.Fatal("settings from the given path must load")
	}
}

func TestRestartRequiredTracksListenerAndEngine(t *testing.T) {
	old := Defaults()
	next := old
	if RestartRequired(old, next) {
		t.Fatal("identical settings never require restart")
	}
	next.ListenAddress = ":9999"
	next.DownloadEngine = "qbittorrent"
	if !RestartRequired(old, next) {
		t.Fatal("listener/engine changes require restart")
	}
	next.InstanceName = "other"
	if !RestartRequired(old, next) {
		t.Fatal("instance name alone never requires restart")
	}
}

func TestPortalAPIKeyPreservationAndRedaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	store, err := LoadAt(path)
	if err != nil {
		t.Fatal(err)
	}

	next := store.Get()
	next.PortalAPIKey = "portal-secret"
	if err := store.Save(next); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("settings file permissions = %v, want 0600", perm)
	}

	reloaded, err := LoadAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get().PortalAPIKey; got != "portal-secret" {
		t.Fatalf("key did not persist: %q", got)
	}

	blank := reloaded.Get()
	blank.PortalAPIKey = ""
	if err := reloaded.Save(blank); err != nil {
		t.Fatal(err)
	}
	after, err := LoadAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.Get().PortalAPIKey; got != "portal-secret" {
		t.Fatalf("blank submission clobbered the stored key: %q", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"portalAPIKey"`) {
		t.Fatalf("key missing from persisted settings: %s", data)
	}
}

func TestTrackerDefaultsAndURLValidation(t *testing.T) {
	d := Defaults()
	if !d.FileListEnabled {
		t.Fatal("FileListEnabled default must be true")
	}
	if d.PirateBayEnabled {
		t.Fatal("PirateBayEnabled default must be false")
	}
	if d.PirateBayWebsiteURL != "https://thepiratebay.org" {
		t.Fatalf("PirateBayWebsiteURL default = %q, want https://thepiratebay.org", d.PirateBayWebsiteURL)
	}
	if d.PirateBayAPIURL != "https://apibay.org" {
		t.Fatalf("PirateBayAPIURL default = %q, want https://apibay.org", d.PirateBayAPIURL)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	s, err := LoadAt(path)
	if err != nil {
		t.Fatal(err)
	}

	// Test that false booleans are not omitted in JSON
	v := s.Get()
	v.FileListEnabled = false
	v.PirateBayEnabled = false
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"fileListEnabled":false`) {
		t.Fatalf("fileListEnabled:false was omitted: %s", data)
	}
	if !strings.Contains(string(data), `"pirateBayEnabled":false`) {
		t.Fatalf("pirateBayEnabled:false was omitted: %s", data)
	}

	// URL validation test cases
	invalidURLs := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"no scheme", "thepiratebay.org"},
		{"ftp scheme", "ftp://thepiratebay.org"},
		{"no host", "https://"},
		{"userinfo", "https://user:pass@thepiratebay.org"},
	}

	for _, tc := range invalidURLs {
		t.Run("website_"+tc.name, func(t *testing.T) {
			bad := s.Get()
			bad.PirateBayWebsiteURL = tc.url
			if err := s.Validate(bad); err == nil {
				t.Fatalf("expected validation error for PirateBayWebsiteURL=%q", tc.url)
			}
		})
		t.Run("api_"+tc.name, func(t *testing.T) {
			bad := s.Get()
			bad.PirateBayAPIURL = tc.url
			if err := s.Validate(bad); err == nil {
				t.Fatalf("expected validation error for PirateBayAPIURL=%q", tc.url)
			}
		})
	}
}
