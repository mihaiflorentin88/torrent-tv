package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadAtAnchorsRelativeDefaultsToSettingsDir pins the load-time
// anchoring: a fresh load's relative default paths resolve to absolute
// paths under the settings file's directory, so the effective store never
// depends on the process working directory (a Finder-launched .app runs
// with CWD=/ and must not mkdir "data" there).
func TestLoadAtAnchorsRelativeDefaultsToSettingsDir(t *testing.T) {
	dir := t.TempDir()
	s, err := LoadAt(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	got := s.Get()
	checks := map[string]struct{ got, want string }{
		"downloadRoot":      {got.DownloadRoot, filepath.Join(dir, "downloads")},
		"torrentSessionDir": {got.TorrentSessionDir, filepath.Join(dir, "torrent-session")},
		"artworkCachePath":  {got.ArtworkCachePath, filepath.Join(dir, "artwork")},
		"subtitleCachePath": {got.SubtitleCachePath, filepath.Join(dir, "subtitles")},
	}
	for key, check := range checks {
		if check.got != check.want {
			t.Errorf("%s must anchor to %q, got %q", key, check.want, check.got)
		}
	}
	if !filepath.IsAbs(got.DatabasePath) {
		t.Fatalf("effective databasePath must be absolute, got %q", got.DatabasePath)
	}
}

// TestLoadAtPiServeFixturePreservesEffectivePaths pins the production
// constraint: the Pi runs `serve --data-dir /var/lib/torrent-tv/data`
// with WorkingDirectory=/var/lib/torrent-tv and a settings file of
// relative default paths, so today every "data/x" value resolves to
// <data dir>/x. The fixture mirrors exactly that layout and proves the
// effective absolute paths are identical under the Pi's working directory
// AND under an arbitrary CWD (which is the point: the old CWD anchoring
// broke wherever CWD differed).
func TestLoadAtPiServeFixturePreservesEffectivePaths(t *testing.T) {
	root := t.TempDir() // stands in for /var/lib/torrent-tv
	dataDir := filepath.Join(root, "data")
	body := `{` +
		`"databasePath": "data/filelist.db",` +
		`"downloadRoot": "/srv/filelist-downloads",` +
		`"torrentSessionDir": "data/torrent-session",` +
		`"artworkCachePath": "data/artwork",` +
		`"subtitleCachePath": "data/subtitles",` +
		`"fileListUsername": "user",` +
		`"fileListPasskey": "pass"` +
		`}`
	settingsPath := filepath.Join(dataDir, "settings.json")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, cwd := range map[string]string{
		"pi working directory": root,
		"arbitrary cwd":        t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(cwd)
			s, err := LoadAt(settingsPath)
			if err != nil {
				t.Fatalf("LoadAt: %v", err)
			}
			got := s.Get()
			checks := map[string]struct{ got, want string }{
				"databasePath":      {got.DatabasePath, filepath.Join(dataDir, "filelist.db")},
				"torrentSessionDir": {got.TorrentSessionDir, filepath.Join(dataDir, "torrent-session")},
				"artworkCachePath":  {got.ArtworkCachePath, filepath.Join(dataDir, "artwork")},
				"subtitleCachePath": {got.SubtitleCachePath, filepath.Join(dataDir, "subtitles")},
			}
			for key, check := range checks {
				if check.got != check.want {
					t.Errorf("%s must resolve to %q exactly as the CWD anchoring did, got %q", key, check.want, check.got)
				}
			}
			if got.DownloadRoot != "/srv/filelist-downloads" {
				t.Errorf("absolute file-provided downloadRoot must stay untouched, got %q", got.DownloadRoot)
			}
			if name == "arbitrary cwd" {
				if _, err := os.Stat(filepath.Join(cwd, "data")); !os.IsNotExist(err) {
					t.Errorf("anchoring must not create a data/ directory under CWD %s", cwd)
				}
			}
		})
	}
}

// TestLoadAtAnchorsRelativeFileProvidedPaths pins that file-provided
// relative values anchor like defaults: a leading "data" element collapses
// onto the settings directory (the historic CWD marker), any other
// relative value lands under it.
func TestLoadAtAnchorsRelativeFileProvidedPaths(t *testing.T) {
	dir := t.TempDir()
	body := `{` +
		`"databasePath": "data/catalog.db",` +
		`"torrentSessionDir": "sessions/tv",` +
		`"fileListUsername": "user",` +
		`"fileListPasskey": "pass"` +
		`}`
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadAt(settingsPath)
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	got := s.Get()
	if want := filepath.Join(dir, "catalog.db"); got.DatabasePath != want {
		t.Fatalf(`"data/catalog.db" must collapse to %q, got %q`, want, got.DatabasePath)
	}
	if want := filepath.Join(dir, "sessions", "tv"); got.TorrentSessionDir != want {
		t.Fatalf(`relative "sessions/tv" must anchor to %q, got %q`, want, got.TorrentSessionDir)
	}
}

// TestLoadAtKeepsEnvManagedRelativePaths pins the env exception:
// environment-managed keys are the operator's explicit word and keep their
// runtime value even when relative, while the untouched defaults around
// them still anchor.
func TestLoadAtKeepsEnvManagedRelativePaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvironmentPrefix+"TORRENT_SESSION_DIR", "relative/session")
	s, err := LoadAt(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("LoadAt: %v", err)
	}
	if got := s.Get().TorrentSessionDir; got != "relative/session" {
		t.Fatalf("env-managed torrentSessionDir must keep its runtime value, got %q", got)
	}
	if want := filepath.Join(dir, "filelist.db"); s.Get().DatabasePath != want {
		t.Fatalf("unmanaged default databasePath must still anchor to %q, got %q", want, s.Get().DatabasePath)
	}
}
