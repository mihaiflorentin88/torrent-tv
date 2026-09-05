//go:build linux

package gui

import (
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/platform/autostart"
)

// setAutostartTestDir points the linux autostart artifact at a temp dir for
// the duration of the test and returns the dir.
func setAutostartTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := autostart.LinuxAutostartDir
	autostart.LinuxAutostartDir = func() string { return dir }
	t.Cleanup(func() { autostart.LinuxAutostartDir = prev })
	return dir
}
