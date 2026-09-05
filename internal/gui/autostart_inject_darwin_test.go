//go:build darwin

package gui

import (
	"testing"

	"github.com/mihaiflorentin88/torrent-tv/internal/platform/autostart"
)

// setAutostartTestDir points the darwin autostart artifact at a temp dir for
// the duration of the test and returns the dir.
func setAutostartTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := autostart.DarwinPlistDir
	autostart.DarwinPlistDir = func() string { return dir }
	t.Cleanup(func() { autostart.DarwinPlistDir = prev })
	return dir
}
