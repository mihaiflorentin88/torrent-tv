//go:build linux

package autostart

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var LinuxAutostartDir = defaultLinuxAutostartDir

func defaultLinuxAutostartDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "autostart")
}

func entryPath() string { return filepath.Join(LinuxAutostartDir(), "torrent-tv.desktop") }

// quoteExec quotes every argument, escaping embedded double quotes per the
// XDG desktop entry Exec spec.
func quoteExec(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
	}
	return strings.Join(quoted, " ")
}

func platformEnable(opts Options) error {
	if err := os.MkdirAll(LinuxAutostartDir(), 0o755); err != nil {
		return err
	}
	content := "[Desktop Entry]\nType=Application\nName=Torrent TV\nExec=" +
		quoteExec(append([]string{opts.ExePath}, opts.Args...)) +
		"\nTerminal=false\nX-GNOME-Autostart-enabled=true\nCategories=Network;AudioVideo;\n"
	return os.WriteFile(entryPath(), []byte(content), 0o644)
}

func platformDisable() error {
	err := os.Remove(entryPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func platformEnabled() (bool, error) {
	_, err := os.Stat(entryPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
