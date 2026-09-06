//go:build darwin

// The macOS .app bundle recovery tests: they exercise the darwin-only
// bundle installer (bundleLiveVersion reads the live CFBundleShortVersionString,
// and the exchange/rollback helpers are darwin-shaped).

package updates

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBundleExchangeActivatesAndRollsBackAtomically(t *testing.T) {
	bundleTestCapable(t)
	installDir := t.TempDir()
	liveBundle := filepath.Join(installDir, "Torrent TV.app")
	stagedBundle := filepath.Join(installDir, ".filelist-extract-staged", "Torrent TV.app")
	writeBundleTree(t, liveBundle, "0.3.0")
	writeBundleTree(t, stagedBundle, "0.4.0")

	journal, err := OpenJournal(installDir)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	installer := NewInstaller(journal, PayloadBundle,
		filepath.Join(liveBundle, "Contents", "MacOS", "torrent-tv"), liveBundle,
		DefaultHealthTimeout)
	op := newOperation(installDir, Selection{Version: "0.4.0"}, bundleTarget(), filepath.Dir(stagedBundle))
	op.Backup = stagedBundle // the exchange counterpart is recorded up front
	if err := journal.Save(op); err != nil {
		t.Fatal(err)
	}
	payload := &Payload{Dir: filepath.Dir(stagedBundle), Kind: PayloadBundle, Bundle: stagedBundle}
	op, err = installer.Activate(op, payload)
	if err != nil {
		t.Fatalf("Activate bundle: %v", err)
	}
	if got := bundleLiveVersion(liveBundle); got != "0.4.0" {
		t.Errorf("live bundle version = %q, want 0.4.0 after exchange", got)
	}
	if got := bundleLiveVersion(stagedBundle); got != "0.3.0" {
		t.Errorf("backup bundle version = %q, want 0.3.0 after exchange", got)
	}

	if err := installer.Rollback(op, "test"); err != nil {
		t.Fatalf("Rollback bundle: %v", err)
	}
	if got := bundleLiveVersion(liveBundle); got != "0.3.0" {
		t.Errorf("live bundle version = %q, want 0.3.0 after rollback", got)
	}
	op = readJournal(t, installDir)
	if op.Phase != PhaseRolledBack || !op.SuppressNext {
		t.Errorf("journal = %+v, want rolled-back with suppression", op)
	}
}

// writeBundleTree creates a minimal .app bundle carrying version.
func writeBundleTree(t *testing.T, appDir, version string) {
	t.Helper()
	macOS := filepath.Join(appDir, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0"?><plist><dict>
		<key>CFBundleIdentifier</key><string>%s</string>
		<key>CFBundleExecutable</key><string>torrent-tv</string>
		<key>CFBundleShortVersionString</key><string>%s</string>
		<key>CFBundleVersion</key><string>%s</string>
	</dict></plist>`, bundleIdentifier, version, version)
	if err := os.WriteFile(filepath.Join(appDir, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := hostFixture(t, "0.4.0")
	if version == "0.3.0" {
		inner = hostFixture(t, "0.3.0")
	}
	if err := os.WriteFile(filepath.Join(macOS, "torrent-tv"), fileBytes(t, inner), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestBundleHelperInstallsConfirmsAndCleans(t *testing.T) {
	bundleTestCapable(t)
	archive := signedBundleZip(t, "0.4.0", bundleIdentifier, true, nil)
	sel := bundleSelection(archive)

	installDir := t.TempDir()
	liveBundle := filepath.Join(installDir, "Torrent TV.app")
	writeBundleTree(t, liveBundle, "0.3.0")
	liveExecutable := filepath.Join(liveBundle, "Contents", "MacOS", "torrent-tv")
	cleanupLiveProcesses(t, liveExecutable)

	tmp := t.TempDir()
	childMarker := filepath.Join(tmp, "child-marker")
	helperResult := filepath.Join(tmp, "helper-result")
	runApplyProcess(t, installDir, archive, sel, bundleTarget(), "bundle", liveExecutable, liveBundle, 10*time.Second, map[string]string{
		"FIXTURE_ACK_DIR":      installDir,
		"FIXTURE_ACK_VERSION":  "0.4.0",
		"FIXTURE_CHILD_MARKER": childMarker,
		"FIXTURE_RESULT":       helperResult,
	})

	result := waitForFile(t, helperResult, 20*time.Second)
	var helper struct {
		Handled bool   `json:"handled"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &helper); err != nil {
		t.Fatalf("helper result %q: %v", result, err)
	}
	if !helper.Handled || helper.Error != "" {
		t.Fatalf("helper result = %+v", helper)
	}
	if _, err := os.Stat(filepath.Join(liveBundle, "Contents", "Info.plist")); err != nil {
		t.Fatalf("live bundle plist missing: %v", err)
	}
	if got := bundleLiveVersion(liveBundle); got != "0.4.0" {
		t.Errorf("live bundle version = %q, want 0.4.0", got)
	}
	waitForContent(t, childMarker, func(s string) bool { return strings.HasPrefix(s, "acked") }, 20*time.Second)
	if _, err := os.Stat(journalPath(installDir)); !errors.Is(err, os.ErrNotExist) {
		t.Error("journal survived bundle confirmation")
	}
	entries, err := os.ReadDir(installDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".filelist-") && entry.Name() != journalName+".lock" {
			t.Errorf("transaction debris left behind: %s", entry.Name())
		}
	}
}
