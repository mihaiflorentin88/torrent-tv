package datadir

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "bin", "app") // binary dir: root/bin
	os.MkdirAll(filepath.Dir(exe), 0o750)
	os.WriteFile(PointerPath(exe), []byte(filepath.Join(root, "pointed")+"\n"), 0o640)

	dir, source, err := Resolve(filepath.Join(root, "flagged"), exe)
	if err != nil || dir != filepath.Join(root, "flagged") || source != "flag" {
		t.Fatalf("flag must win: dir=%q source=%q err=%v", dir, source, err)
	}
	dir, source, err = Resolve("", exe)
	if err != nil || source != "pointer" || dir != filepath.Join(root, "pointed") {
		t.Fatalf("pointer second: dir=%q source=%q err=%v", dir, source, err)
	}
	os.Remove(PointerPath(exe))
	dir, source, err = Resolve("", exe)
	if err != nil || source != "default" || dir != filepath.Join(filepath.Dir(exe), "data") {
		t.Fatalf("default last: dir=%q source=%q err=%v", dir, source, err)
	}
}

func TestRelocateRefusesNonEmptyTarget(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "app")
	from := filepath.Join(root, "data")
	to := filepath.Join(root, "elsewhere")
	os.MkdirAll(from, 0o750)
	os.WriteFile(filepath.Join(from, "settings.json"), []byte("{}"), 0o640)
	os.MkdirAll(to, 0o750)
	os.WriteFile(filepath.Join(to, "occupied.txt"), []byte("x"), 0o640)
	if err := Relocate(exe, from, to); err == nil {
		t.Fatal("relocation into a non-empty dir must be refused")
	}
	if _, err := os.Stat(filepath.Join(from, "settings.json")); err != nil {
		t.Fatal("source must stay untouched on refusal")
	}
}

// TestRelocateRefusesTargetInsideSource pins the self-copy guard: a target
// inside the current data dir is refused before anything moves, so the
// copy walk can never descend into its own destination.
func TestRelocateRefusesTargetInsideSource(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "app")
	from := filepath.Join(root, "data")
	to := filepath.Join(from, "nested")
	os.MkdirAll(from, 0o750)
	os.WriteFile(filepath.Join(from, "settings.json"), []byte("{}"), 0o640)
	if err := Relocate(exe, from, to); err == nil {
		t.Fatal("relocation into a nested dir must be refused")
	}
	if _, err := os.Stat(filepath.Join(from, "settings.json")); err != nil {
		t.Fatal("source must stay untouched on refusal")
	}
	if _, err := os.Stat(to); !os.IsNotExist(err) {
		t.Fatal("nested target must not be created")
	}
}

func TestRelocateSameVolumeMovesAndWritesPointer(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "app")
	from := filepath.Join(root, "data")
	to := filepath.Join(root, "data2")
	os.MkdirAll(filepath.Join(from, "logs"), 0o750)
	os.WriteFile(filepath.Join(from, "settings.json"), []byte("{}"), 0o640)
	os.WriteFile(filepath.Join(from, "logs", "server.log"), []byte("hi"), 0o640)
	if err := Relocate(exe, from, to); err != nil {
		t.Fatalf("Relocate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to, "logs", "server.log")); err != nil {
		t.Fatalf("contents must move: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("source dir must be gone after same-volume move: %v", err)
	}
	if got, _, _ := Resolve("", exe); got != to {
		t.Fatalf("pointer must name the new dir, got %q", got)
	}
}

func TestCopyVerifiedProducesIdenticalBytes(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.bin")
	payload := bytes.Repeat([]byte("datadir-verify-payload\n"), 4096)
	os.WriteFile(src, payload, 0o640)
	dst := filepath.Join(root, "dst.bin")
	if err := copyVerified(src, dst, 0o640); err != nil {
		t.Fatalf("copyVerified: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("dst bytes must equal source bytes")
	}
}

func TestCopyVerifiedFailureLeavesNoValidDst(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.bin")
	os.WriteFile(src, []byte("payload"), 0o640)
	dst := filepath.Join(root, "dst-dir") // a directory: opening dst for writing must fail
	os.MkdirAll(dst, 0o750)
	if err := copyVerified(src, dst, 0o640); err == nil {
		t.Fatal("copyVerified must fail when dst cannot be written")
	}
	if info, err := os.Stat(dst); err != nil || !info.IsDir() {
		t.Fatal("failed copy must not leave a valid dst file")
	}
}

// withSeams swaps the platform seams for the duration of the test. The
// probes are injected closures, so tests never touch /var/lib, %APPDATA%,
// or ~/Library.
func withSeams(t *testing.T, probe func(string) (bool, error), cfgDir, home func() (string, error), xdg string) {
	t.Helper()
	oldProbe, oldCfg, oldHome, oldXDG := varLibProbe, userConfigDir, userHomeDir, xdgDataHome
	varLibProbe, userConfigDir, userHomeDir, xdgDataHome = probe, cfgDir, home, func() string { return xdg }
	t.Cleanup(func() { varLibProbe, userConfigDir, userHomeDir, xdgDataHome = oldProbe, oldCfg, oldHome, oldXDG })
}

func TestResolveForFlagAndPointerWinOverPlatform(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "bin", "app")
	os.MkdirAll(filepath.Dir(exe), 0o750)
	os.WriteFile(PointerPath(exe), []byte(filepath.Join(root, "pointed")+"\n"), 0o640)
	withSeams(t, func(string) (bool, error) { return true, nil },
		func() (string, error) { return root, nil },
		func() (string, error) { return root, nil }, "")

	dir, source, err := ResolveFor(filepath.Join(root, "flagged"), exe, PlatformGUI)
	if err != nil || dir != filepath.Join(root, "flagged") || source != "flag" {
		t.Fatalf("flag must win over platform: dir=%q source=%q err=%v", dir, source, err)
	}
	dir, source, err = ResolveFor("", exe, PlatformGUI)
	if err != nil || source != "pointer" || dir != filepath.Join(root, "pointed") {
		t.Fatalf("pointer must win over platform: dir=%q source=%q err=%v", dir, source, err)
	}
}

func TestPlatformDefaultDarwinUsesAppSupportWithSpace(t *testing.T) {
	root := t.TempDir()
	withSeams(t, nil,
		func() (string, error) { return filepath.Join(root, "AppSupport"), nil },
		func() (string, error) { return "", nil }, "")
	dir, err := platformDefaultConfig()
	if err != nil {
		t.Fatalf("platformDefaultConfig: %v", err)
	}
	want := filepath.Join(root, "AppSupport", "Torrent TV")
	if dir != want {
		t.Fatalf("darwin default: got %q want %q", dir, want)
	}
}

func TestPlatformDefaultWindowsUsesAppDataWithSpace(t *testing.T) {
	root := t.TempDir()
	withSeams(t, nil,
		func() (string, error) { return filepath.Join(root, "AppData", "Roaming"), nil },
		func() (string, error) { return "", nil }, "")
	dir, err := platformDefaultConfig()
	if err != nil {
		t.Fatalf("platformDefaultConfig: %v", err)
	}
	want := filepath.Join(root, "AppData", "Roaming", "Torrent TV")
	if dir != want {
		t.Fatalf("windows default: got %q want %q", dir, want)
	}
}

func TestPlatformDefaultLinuxVarLibWhenWritable(t *testing.T) {
	probed := ""
	withSeams(t, func(dir string) (bool, error) { probed = dir; return true, nil },
		nil, nil, "")
	dir, err := platformDefaultLinux()
	if err != nil {
		t.Fatalf("platformDefaultLinux: %v", err)
	}
	if probed != "/var/lib/torrent-tv" {
		t.Fatalf("probe must target /var/lib path, probed %q", probed)
	}
	if dir != "/var/lib/torrent-tv" {
		t.Fatalf("writable var-lib must be used, got %q", dir)
	}
}

func TestPlatformDefaultLinuxFallsBackToXDGWhenVarLibMissing(t *testing.T) {
	root := t.TempDir()
	withSeams(t, func(string) (bool, error) { return false, nil },
		nil, func() (string, error) { return root, nil },
		filepath.Join(root, "xdg-data"))
	dir, err := platformDefaultLinux()
	if err != nil {
		t.Fatalf("platformDefaultLinux: %v", err)
	}
	want := filepath.Join(root, "xdg-data", "torrent-tv")
	if dir != want {
		t.Fatalf("XDG default: got %q want %q", dir, want)
	}
}

func TestPlatformDefaultLinuxXDGDefaultsToHomeLocalShare(t *testing.T) {
	root := t.TempDir()
	withSeams(t, func(string) (bool, error) { return false, os.ErrPermission },
		nil, func() (string, error) { return root, nil }, "")
	dir, err := platformDefaultLinux()
	if err != nil {
		t.Fatalf("platformDefaultLinux: %v", err)
	}
	want := filepath.Join(root, ".local", "share", "torrent-tv")
	if dir != want {
		t.Fatalf("XDG fallback to ~/.local/share: got %q want %q", dir, want)
	}
}

// TestPlatformDefaultDoesNotCreateDirs pins that resolution only returns the
// path; creation stays with the caller's MkdirAll (existing behavior), so a
// nonexistent platform fallback is reported, not silently conjured here.
func TestPlatformDefaultDoesNotCreateDirs(t *testing.T) {
	root := t.TempDir()
	withSeams(t, func(string) (bool, error) { return false, nil },
		func() (string, error) { return filepath.Join(root, "AppSupport"), nil },
		func() (string, error) { return "", nil }, "")
	dir, err := platformDefaultConfig()
	if err != nil {
		t.Fatalf("platformDefaultConfig: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("resolution must not create the dir, stat err=%v", err)
	}
}
