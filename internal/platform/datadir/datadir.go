// Package datadir resolves the application data directory and relocates it.
// Resolution order (spec: Data directory): --data-dir flag, then the
// data.location pointer file next to the executable, then either data/ next
// to the executable (serve) or the platform default (GUI).
package datadir

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const pointerName = "data.location"

// Relocate moves the data directory from → to and records the new location.
// The caller must have stopped the server first.
func Relocate(exePath, from, to string) error {
	absTo, err := filepath.Abs(to)
	if err != nil {
		return err
	}
	if absTo == from {
		return fmt.Errorf("new data location is the current location")
	}
	// A target inside the source would make the copy walk its own
	// destination — a self-copy recursion leaving a junk tree inside the
	// live data dir. Refuse like the equality case above.
	if prefix := from + string(filepath.Separator); strings.HasPrefix(absTo, prefix) {
		return fmt.Errorf("target %s is inside the current data directory", absTo)
	}
	if entries, err := os.ReadDir(absTo); err == nil && len(entries) > 0 {
		return fmt.Errorf("target %s is not empty", absTo)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := moveTree(from, absTo); err != nil {
		return err
	}
	return SetPointer(exePath, absTo)
}

func moveTree(from, to string) error {
	// Same volume: rename is atomic and instant.
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	// Cross volume: copy, verify each file by SHA-256, delete only after all
	// copies verified; on any error leave the source untouched.
	if err := copyTree(from, to); err != nil {
		return err
	}
	return os.RemoveAll(from)
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := copyVerified(path, target, info.Mode()); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode())
	})
}

func copyVerified(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// Verify the copy: re-open dst and compare its digest against the
	// source digest captured during the write.
	vf, err := os.Open(dst)
	if err != nil {
		os.Remove(dst)
		return err
	}
	hDst := sha256.New()
	_, err = io.Copy(hDst, vf)
	vf.Close()
	if err != nil {
		os.Remove(dst)
		return err
	}
	if !bytesEqual(h.Sum(nil), hDst.Sum(nil)) {
		os.Remove(dst)
		return fmt.Errorf("verification failed copying %s", src)
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	return hex.EncodeToString(a) == hex.EncodeToString(b)
}

// DefaultLocation selects the fallback used when no flag and no pointer
// apply.
type DefaultLocation int

const (
	// BinaryAdjacent falls back to data/ next to the executable (serve).
	BinaryAdjacent DefaultLocation = iota
	// PlatformGUI falls back to the per-platform standard location (GUI).
	PlatformGUI
)

// Platform seams, injectable so tests never touch /var/lib, %APPDATA%, or
// ~/Library.
var (
	varLibProbe   = defaultVarLibProbe
	userConfigDir = os.UserConfigDir
	userHomeDir   = os.UserHomeDir
	xdgDataHome   = func() string { return os.Getenv("XDG_DATA_HOME") }
)

const varLibDir = "/var/lib/torrent-tv"

func defaultVarLibProbe(dir string) (bool, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !fi.IsDir() {
		return false, nil
	}
	probe := filepath.Join(dir, ".probe-tmp")
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, nil
	}
	f.Close()
	os.Remove(probe)
	return true, nil
}

// ResolveFor resolves the data directory: flag → pointer → def
// (BinaryAdjacent: data/ next to the executable; PlatformGUI: the platform
// default). It returns the resolved path and its source ("flag",
// "pointer", "platform", or "default"). Paths are returned uncreated; the
// caller mkdirs.
func ResolveFor(flagDir, exePath string, def DefaultLocation) (string, string, error) {
	if strings.TrimSpace(flagDir) != "" {
		abs, err := filepath.Abs(flagDir)
		return abs, "flag", err
	}
	if p := readPointer(exePath); p != "" {
		return p, "pointer", nil
	}
	switch def {
	case PlatformGUI:
		dir, err := platformDefault()
		return dir, "platform", err
	default:
		return filepath.Join(filepath.Dir(exePath), "data"), "default", nil
	}
}

func platformDefault() (string, error) {
	switch runtime.GOOS {
	case "linux":
		return platformDefaultLinux()
	default:
		return platformDefaultConfig()
	}
}

// platformDefaultConfig is the Windows (%APPDATA%) and macOS
// (~/Library/Application Support) default; both are os.UserConfigDir's
// base plus the app dir name.
func platformDefaultConfig() (string, error) {
	base, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "Torrent TV"), nil
}

// platformDefaultLinux prefers /var/lib/torrent-tv when it
// already exists and is writable (admin-created, shared use), else
// $XDG_DATA_HOME/torrent-tv (default ~/.local/share/...).
func platformDefaultLinux() (string, error) {
	if ok, err := varLibProbe(varLibDir); err == nil && ok {
		return varLibDir, nil
	}
	base := xdgDataHome()
	if strings.TrimSpace(base) == "" {
		home, err := userHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "torrent-tv"), nil
}

// Resolve resolves with the serve fallback (BinaryAdjacent) and is
// unchanged from the pre-platform behavior.
func Resolve(flagDir, exePath string) (string, string, error) {
	return ResolveFor(flagDir, exePath, BinaryAdjacent)
}

func PointerPath(exePath string) string {
	return filepath.Join(filepath.Dir(exePath), pointerName)
}

func readPointer(exePath string) string {
	b, err := os.ReadFile(PointerPath(exePath))
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(b))
	if !filepath.IsAbs(p) {
		return ""
	}
	return p
}

func SetPointer(exePath, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	tmp := PointerPath(exePath) + ".tmp"
	if err := os.WriteFile(tmp, []byte(abs+"\n"), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, PointerPath(exePath))
}

func ClearPointer(exePath string) error {
	err := os.Remove(PointerPath(exePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
