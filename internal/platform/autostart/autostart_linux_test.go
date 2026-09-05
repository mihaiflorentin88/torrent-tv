//go:build linux

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxEnableDisableRoundTrip(t *testing.T) {
	dir := t.TempDir()
	LinuxAutostartDir = func() string { return dir }
	defer func() { LinuxAutostartDir = defaultLinuxAutostartDir }()

	if err := Enable(Options{ExePath: "/opt/fs/torrent-tv", Args: []string{"--minimized", "--data-dir", "/opt/fs/data"}}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "torrent-tv.desktop"))
	if err != nil {
		t.Fatalf("desktop entry: %v", err)
	}
	text := string(b)
	for _, want := range []string{"[Desktop Entry]", `Exec="/opt/fs/torrent-tv" --minimized --data-dir "/opt/fs/data"`, "Type=Application"} {
		if !strings.Contains(text, want) {
			t.Fatalf("desktop entry missing %q in:\n%s", want, text)
		}
	}
	if ok, err := Enabled(); err != nil || !ok {
		t.Fatalf("Enabled after Enable = %v, %v", ok, err)
	}
	if err := Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if ok, err := Enabled(); err != nil || ok {
		t.Fatalf("Enabled after Disable = %v, %v", ok, err)
	}
	if err := Disable(); err != nil { // idempotent
		t.Fatalf("Disable must tolerate absence: %v", err)
	}
}
