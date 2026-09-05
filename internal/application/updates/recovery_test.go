package updates

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func hostFixture(t *testing.T, version string) string {
	t.Helper()
	return fixtureBinary(t, runtime.GOOS, runtime.GOARCH, 0, nil, version)
}

func fileAssetName(version string) string {
	arch := runtime.GOARCH
	if runtime.GOOS == "linux" {
		return "torrent-tv-" + version + "-linux-" + arch + ".tar.gz"
	}
	return "torrent-tv-" + version + "-" + runtime.GOOS + "-" + arch + ".tar.gz"
}

func journalPath(installDir string) string {
	return filepath.Join(installDir, journalName)
}

func readJournal(t *testing.T, installDir string) Operation {
	t.Helper()
	data, err := os.ReadFile(journalPath(installDir))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var op Operation
	if err := json.Unmarshal(data, &op); err != nil {
		t.Fatalf("parse journal: %v", err)
	}
	return op
}

// waitForContent polls path until its content satisfies matches.
func waitForContent(t *testing.T, path string, matches func(string) bool, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && matches(string(data)) {
			return string(data)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("%s never reached the expected content", path)
	return ""
}

func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	return waitForContent(t, path, func(string) bool { return true }, timeout)
}

// waitJournal polls until the journal reports the wanted phase.
func waitJournal(t *testing.T, installDir string, timeout time.Duration, wantedPhase string) Operation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(journalPath(installDir)); err == nil {
			op := readJournal(t, installDir)
			if op.Phase == wantedPhase {
				return op
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("journal never reached phase %q", wantedPhase)
	return Operation{}
}

func killLater(t *testing.T, pid int) {
	t.Helper()
	t.Cleanup(func() {
		if pid > 0 {
			exec.Command("kill", "-9", strconv.Itoa(pid)).Run() //nolint:errcheck // best-effort cleanup
		}
	})
}

// cleanupLiveProcesses kills any installation left running from livePath
// when the test ends; launched installations idle until signalled.
func cleanupLiveProcesses(t *testing.T, livePath string) {
	t.Helper()
	t.Cleanup(func() {
		exec.Command("pkill", "-f", livePath).Run() //nolint:errcheck // best-effort cleanup
	})
}

// markerPid parses the trailing pid out of a fixture marker file.
func markerPid(t *testing.T, content string) int {
	t.Helper()
	fields := strings.Fields(content)
	if len(fields) < 2 {
		t.Fatalf("marker %q carries no pid", content)
	}
	pid, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil {
		t.Fatalf("marker %q pid: %v", content, err)
	}
	return pid
}

// applyEnv assembles the environment for one fixture apply-mode process.
func applyEnv(installDir, assetFile string, sel Selection, target Target, kind, livePath, bundlePath string, healthTimeout time.Duration, extra map[string]string) []string {
	env := []string{
		"FIXTURE_APPLY=1",
		"FIXTURE_INSTALL_DIR=" + installDir,
		"FIXTURE_KIND=" + kind,
		"FIXTURE_LIVE_PATH=" + livePath,
		"FIXTURE_BUNDLE_PATH=" + bundlePath,
		"FIXTURE_ASSET_FILE=" + assetFile,
		"FIXTURE_ASSET_NAME=" + sel.AssetName,
		"FIXTURE_ASSET_VERSION=" + sel.Version,
		"FIXTURE_ASSET_SHA=" + sel.SHA256,
		"FIXTURE_TARGET_GOOS=" + target.GOOS,
		"FIXTURE_TARGET_GOARCH=" + target.GOARCH,
		"FIXTURE_TARGET_FLAVOR=" + target.Flavor,
		"FIXTURE_HEALTH_TIMEOUT_MS=" + strconv.FormatInt(healthTimeout.Milliseconds(), 10),
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

// runApplyProcess drives one full installation transaction in a fixture
// subprocess that exits after the handoff, exactly like the real
// installing process.
func runApplyProcess(t *testing.T, installDir string, archive []byte, sel Selection, target Target, kind, livePath, bundlePath string, healthTimeout time.Duration, extra map[string]string) {
	t.Helper()
	assetFile := filepath.Join(t.TempDir(), sel.AssetName)
	if err := os.WriteFile(assetFile, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	applyResult := filepath.Join(t.TempDir(), "apply-result")
	env := applyEnv(installDir, assetFile, sel, target, kind, livePath, bundlePath, healthTimeout, extra)
	env = append(env, "FIXTURE_APPLY_RESULT="+applyResult)

	cmd := exec.Command(hostFixture(t, "0.4.0"))
	cmd.Env = append(os.Environ(), env...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply process failed: %v\n%s", err, output)
	}
	if text := waitForFile(t, applyResult, 5*time.Second); text != "" {
		t.Fatalf("apply transaction failed: %s", text)
	}
}

func writeLiveExecutable(t *testing.T, installDir string, fixturePath string) string {
	t.Helper()
	livePath := filepath.Join(installDir, "torrent-tv")
	if err := os.WriteFile(livePath, fileBytes(t, fixturePath), 0o755); err != nil {
		t.Fatal(err)
	}
	return livePath
}

func hostTarget() Target {
	return Identity{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Flavor: FlavorGUI}.Target()
}

func TestJournalExclusiveOwnership(t *testing.T) {
	dir := t.TempDir()
	journal, err := OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	defer journal.Close()
	second, err := OpenJournal(dir)
	if err == nil {
		second.Close()
		t.Fatal("second OpenJournal acquired exclusive ownership concurrently")
	}
}

func TestJournalSaveLoadAcknowledgeAndClear(t *testing.T) {
	dir := t.TempDir()
	journal, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	op := newOperation(dir, Selection{Version: "0.4.0"}, Target{Flavor: FlavorHeadless}, "staged-a", "staged-b")
	if err := journal.Save(op); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, found, err := journal.Load()
	if err != nil || !found {
		t.Fatalf("Load: found=%v err=%v", found, err)
	}
	if loaded.ID != op.ID || loaded.Phase != PhaseStaged || loaded.Backup == "" {
		t.Errorf("loaded operation = %+v", loaded)
	}
	if len(loaded.StagedPaths) != 2 {
		t.Errorf("staged paths = %v", loaded.StagedPaths)
	}

	// Health acknowledgement must identify the operation and its version.
	op.Phase = PhaseActivated
	if err := journal.Save(op); err != nil {
		t.Fatal(err)
	}
	if err := journal.Acknowledge(op.ID, "0.9.9"); !errors.Is(err, ErrOperationMismatch) {
		t.Errorf("Acknowledge with wrong version = %v, want ErrOperationMismatch", err)
	}
	if err := journal.Acknowledge("op-other", "0.4.0"); !errors.Is(err, ErrOperationMismatch) {
		t.Errorf("Acknowledge with wrong id = %v, want ErrOperationMismatch", err)
	}
	if err := journal.Acknowledge(op.ID, "0.4.0"); err != nil {
		t.Errorf("Acknowledge: %v", err)
	}
	loaded, _, _ = journal.Load()
	if loaded.Phase != PhaseConfirmed {
		t.Errorf("phase after acknowledge = %q, want confirmed", loaded.Phase)
	}

	if err := journal.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, found, err := journal.Load(); err != nil || found {
		t.Errorf("Load after Clear: found=%v err=%v", found, err)
	}
}

func TestEvaluateRecoveryDecidesStartupActions(t *testing.T) {
	deadline := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	now := deadline.Add(-time.Second)
	op := func(phase string) Operation {
		return Operation{ID: "op-1", Version: "0.4.0", Phase: phase, Deadline: deadline}
	}
	cases := []struct {
		name       string
		op         Operation
		state      InstallState
		wantAction RecoveryAction
	}{
		{
			name: "staged without mutation cleans up",
			op:   op(PhaseStaged), state: InstallState{CurrentVersion: "0.3.0", Now: now},
			wantAction: RecoveryCleanup,
		},
		{
			name: "staged with mutated live rolls back",
			op:   op(PhaseStaged), state: InstallState{CurrentVersion: "0.4.0", Now: now, Activated: true},
			wantAction: RecoveryRollback,
		},
		{
			name: "activated new version acknowledges before deadline",
			op:   op(PhaseActivated), state: InstallState{CurrentVersion: "0.4.0", Now: now, Activated: true},
			wantAction: RecoveryAcknowledge,
		},
		{
			name: "activated past deadline rolls back",
			op:   op(PhaseActivated), state: InstallState{CurrentVersion: "0.4.0", Now: deadline.Add(time.Second), Activated: true},
			wantAction: RecoveryRollback,
		},
		{
			name: "activated with old version rolls back",
			op:   op(PhaseActivated), state: InstallState{CurrentVersion: "0.3.0", Now: now, Activated: true},
			wantAction: RecoveryRollback,
		},
		{
			name: "activated without provable pristineness rolls back",
			op:   op(PhaseActivated), state: InstallState{CurrentVersion: "0.3.0", Now: now},
			wantAction: RecoveryRollback,
		},
		{
			name: "activated with pristine live content cleans up",
			op:   op(PhaseActivated), state: InstallState{CurrentVersion: "0.3.0", Now: now, Pristine: true},
			wantAction: RecoveryCleanup,
		},
		{
			name: "confirmed cleans up",
			op:   op(PhaseConfirmed), state: InstallState{CurrentVersion: "0.4.0", Now: now},
			wantAction: RecoveryCleanup,
		},
		{
			name: "rolled back suppresses once",
			op:   op(PhaseRolledBack), state: InstallState{CurrentVersion: "0.3.0", Now: now},
			wantAction: RecoverySuppress,
		},
		{
			name:       "rollback failure demands manual repair",
			op:         Operation{ID: "op-1", Version: "0.4.0", Phase: PhaseRolledBack, FailedError: "restore: no such file"},
			state:      InstallState{CurrentVersion: "0.4.0", Now: now},
			wantAction: RecoveryManual,
		},
		{
			name:       "unknown phase is manual",
			op:         Operation{ID: "op-1", Phase: "bogus"},
			state:      InstallState{CurrentVersion: "0.4.0", Now: now},
			wantAction: RecoveryManual,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recovery := EvaluateRecovery(testCase.op, testCase.state)
			if recovery.Action != testCase.wantAction {
				t.Errorf("action = %q, want %q", recovery.Action, testCase.wantAction)
			}
		})
	}
}

func TestFileInstallHappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file transaction")
	}
	installDir := t.TempDir()
	oldFixture := hostFixture(t, "0.3.0")
	newFixture := hostFixture(t, "0.4.0")
	newPayload := fileBytes(t, newFixture)

	livePath := writeLiveExecutable(t, installDir, oldFixture)
	cleanupLiveProcesses(t, livePath)
	archive := buildTarGz(t, happyTarMembers(newPayload))
	sel := testSelection(fileAssetName("0.4.0"), "0.4.0", archive)

	tmp := t.TempDir()
	childMarker := filepath.Join(tmp, "child-marker")
	helperResult := filepath.Join(tmp, "helper-result")
	runApplyProcess(t, installDir, archive, sel, hostTarget(), "file", livePath, "", 10*time.Second, map[string]string{
		"FIXTURE_ACK_DIR":      installDir,
		"FIXTURE_ACK_VERSION":  "0.4.0",
		"FIXTURE_CHILD_MARKER": childMarker,
		"FIXTURE_RESULT":       helperResult,
	})

	// The healthy new installation acknowledges; the helper confirms and
	// removes every transaction artifact.
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
	if !equalDigests(mustDigest(t, livePath), mustDigest(t, newFixture)) {
		t.Error("live executable does not carry the new installation")
	}
	waitForContent(t, childMarker, func(s string) bool { return strings.HasPrefix(s, "acked") }, 20*time.Second)
	if _, err := os.Stat(journalPath(installDir)); !errors.Is(err, os.ErrNotExist) {
		t.Error("journal survived successful health acknowledgement")
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
	marker := waitForContent(t, childMarker, func(string) bool { return true }, time.Second)
	if strings.HasPrefix(marker, "ready") {
		killLater(t, markerPid(t, marker))
	}
}

func TestRecoverExecutesCleanupRollbackAndSuppression(t *testing.T) {
	oldFixture := hostFixture(t, "0.3.0")
	newFixture := hostFixture(t, "0.4.0")

	t.Run("confirmed journal cleans all debris", func(t *testing.T) {
		installDir := t.TempDir()
		livePath := writeLiveExecutable(t, installDir, newFixture)
		backup := filepath.Join(installDir, ".filelist-backup-old")
		if err := os.WriteFile(backup, fileBytes(t, oldFixture), 0o700); err != nil {
			t.Fatal(err)
		}
		debris := filepath.Join(installDir, ".filelist-extract-debris")
		if err := os.MkdirAll(debris, 0o755); err != nil {
			t.Fatal(err)
		}
		journal, err := OpenJournal(installDir)
		if err != nil {
			t.Fatal(err)
		}
		installer := NewInstaller(journal, PayloadFile, livePath, "", DefaultHealthTimeout)
		if err := journal.Save(Operation{ID: "op-1", Version: "0.4.0", Phase: PhaseConfirmed, Backup: backup, StagedPaths: []string{debris}}); err != nil {
			t.Fatal(err)
		}
		recovery, err := installer.Recover("0.4.0")
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if recovery.Action != RecoveryCleanup {
			t.Fatalf("action = %q, want cleanup", recovery.Action)
		}
		if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("backup survived cleanup: %v", err)
		}
		if _, err := os.Stat(debris); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("debris survived cleanup: %v", err)
		}
		if _, err := os.Stat(journalPath(installDir)); !errors.Is(err, os.ErrNotExist) {
			t.Error("journal survived cleanup")
		}
	})

	t.Run("staged crash before swap only cleans", func(t *testing.T) {
		installDir := t.TempDir()
		livePath := writeLiveExecutable(t, installDir, oldFixture)
		journal, err := OpenJournal(installDir)
		if err != nil {
			t.Fatal(err)
		}
		op := newOperation(installDir, Selection{Version: "0.4.0"}, hostTarget())
		if err := journal.Save(op); err != nil {
			t.Fatal(err)
		}
		recovery, err := NewInstaller(journal, PayloadFile, livePath, "", DefaultHealthTimeout).Recover("0.3.0")
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if recovery.Action != RecoveryCleanup {
			t.Fatalf("action = %q, want cleanup", recovery.Action)
		}
		if !equalDigests(mustDigest(t, livePath), mustDigest(t, oldFixture)) {
			t.Error("live content changed during staged cleanup")
		}
	})

	t.Run("pre-mutation crash with recorded prior state cleans up", func(t *testing.T) {
		installDir := t.TempDir()
		livePath := writeLiveExecutable(t, installDir, oldFixture)
		journal, err := OpenJournal(installDir)
		if err != nil {
			t.Fatal(err)
		}
		// Windows-shaped window: activated persisted before the helper took
		// the backup, so no backup exists and the live path is untouched.
		installer := NewInstaller(journal, PayloadFile, livePath, "", DefaultHealthTimeout)
		if err := journal.Save(Operation{
			ID: "op-1", Version: "0.4.0", Phase: PhaseActivated,
			PriorState: hex.EncodeToString(mustDigest(t, livePath)),
			Backup:     filepath.Join(installDir, ".filelist-backup-absent"),
			Deadline:   time.Now().Add(time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		recovery, err := installer.Recover("0.3.0")
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if recovery.Action != RecoveryCleanup {
			t.Fatalf("action = %q, want cleanup", recovery.Action)
		}
		if !equalDigests(mustDigest(t, livePath), mustDigest(t, oldFixture)) {
			t.Error("pre-mutation cleanup disturbed the live path")
		}
		if _, err := os.Stat(journalPath(installDir)); !errors.Is(err, os.ErrNotExist) {
			t.Error("journal survived pre-mutation cleanup")
		}
	})

	t.Run("crash between backup and rename-in leaves live intact", func(t *testing.T) {
		installDir := t.TempDir()
		livePath := writeLiveExecutable(t, installDir, oldFixture)
		backup := filepath.Join(installDir, ".filelist-backup-linked")
		if err := os.Link(livePath, backup); err != nil {
			t.Fatalf("hard-link backup: %v", err)
		}
		journal, err := OpenJournal(installDir)
		if err != nil {
			t.Fatal(err)
		}
		installer := NewInstaller(journal, PayloadFile, livePath, "", DefaultHealthTimeout)
		if err := journal.Save(Operation{
			ID: "op-1", Version: "0.4.0", Phase: PhaseActivated,
			PriorState: hex.EncodeToString(mustDigest(t, livePath)),
			Backup:     backup,
			Deadline:   time.Now().Add(time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		recovery, err := installer.Recover("0.3.0")
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if recovery.Action != RecoveryCleanup {
			t.Fatalf("action = %q, want cleanup", recovery.Action)
		}
		if !equalDigests(mustDigest(t, livePath), mustDigest(t, oldFixture)) {
			t.Error("crash between backup and rename-in disturbed the live path")
		}
		if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("backup survived cleanup: %v", err)
		}
	})

	t.Run("activated past deadline rolls back with suppression", func(t *testing.T) {
		installDir := t.TempDir()
		livePath := writeLiveExecutable(t, installDir, newFixture) // swap already landed
		backup := filepath.Join(installDir, ".filelist-backup-old")
		if err := os.WriteFile(backup, fileBytes(t, oldFixture), 0o700); err != nil {
			t.Fatal(err)
		}
		journal, err := OpenJournal(installDir)
		if err != nil {
			t.Fatal(err)
		}
		installer := NewInstaller(journal, PayloadFile, livePath, "", DefaultHealthTimeout)
		if err := journal.Save(Operation{ID: "op-1", Version: "0.4.0", Phase: PhaseActivated, Backup: backup, Deadline: time.Now().Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
		recovery, err := installer.Recover("0.4.0")
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if recovery.Action != RecoveryRollback {
			t.Fatalf("action = %q, want rollback", recovery.Action)
		}
		if !equalDigests(mustDigest(t, livePath), mustDigest(t, oldFixture)) {
			t.Error("rollback did not restore the previous installation")
		}
		if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("backup survived rollback: %v", err)
		}
		op := readJournal(t, installDir)
		if op.Phase != PhaseRolledBack || !op.SuppressNext {
			t.Errorf("rolled-back journal = %+v, want rolled-back with suppression", op)
		}
		if err := installer.ConsumeSuppression(); err != nil {
			t.Fatalf("ConsumeSuppression: %v", err)
		}
		if _, err := os.Stat(journalPath(installDir)); !errors.Is(err, os.ErrNotExist) {
			t.Error("journal survived suppression consumption")
		}
	})
}

func mustDigest(t *testing.T, path string) []byte {
	t.Helper()
	digest, err := digestFile(path)
	if err != nil {
		t.Fatalf("digest %s: %v", path, err)
	}
	return digest
}

// fileTransactionCase drives the real end-to-end file install transaction:
// the apply subprocess stages, activates, and hands off; the helper waits
// for the apply process to exit, launches the new installation, waits for
// its health acknowledgement, and rolls back on failure.
func runFileTransaction(t *testing.T, extra map[string]string, healthTimeout time.Duration) (installDir string) {
	t.Helper()
	oldFixture := hostFixture(t, "0.3.0")
	newFixture := hostFixture(t, "0.4.0")
	newPayload := fileBytes(t, newFixture)

	installDir = t.TempDir()
	livePath := writeLiveExecutable(t, installDir, oldFixture)
	cleanupLiveProcesses(t, livePath)
	archive := buildTarGz(t, happyTarMembers(newPayload))
	sel := testSelection(fileAssetName("0.4.0"), "0.4.0", archive)

	tmp := t.TempDir()
	childMarker := filepath.Join(tmp, "child-marker")
	helperResult := filepath.Join(tmp, "helper-result")
	exitFile := filepath.Join(tmp, "exit-file")
	base := map[string]string{
		"FIXTURE_ACK_DIR":           installDir,
		"FIXTURE_ACK_VERSION":       "0.4.0",
		"FIXTURE_CHILD_MARKER":      childMarker,
		"FIXTURE_RESULT":            helperResult,
		"FIXTURE_EXIT_FILE":         exitFile,
		"FIXTURE_HEALTH_TIMEOUT_MS": strconv.FormatInt(healthTimeout.Milliseconds(), 10),
	}
	for key, value := range extra {
		base[key] = value
	}
	runApplyProcess(t, installDir, archive, sel, hostTarget(), "file", livePath, "", healthTimeout, base)
	return installDir
}

func fileInstallHappyPath(t *testing.T) {
	installDir := t.TempDir()
	oldFixture := hostFixture(t, "0.3.0")
	newFixture := hostFixture(t, "0.4.0")
	newPayload := fileBytes(t, newFixture)

	livePath := writeLiveExecutable(t, installDir, oldFixture)
	cleanupLiveProcesses(t, livePath)
	archive := buildTarGz(t, happyTarMembers(newPayload))
	sel := testSelection(fileAssetName("0.4.0"), "0.4.0", archive)

	tmp := t.TempDir()
	childMarker := filepath.Join(tmp, "child-marker")
	helperResult := filepath.Join(tmp, "helper-result")
	runApplyProcess(t, installDir, archive, sel, hostTarget(), "file", livePath, "", 10*time.Second, map[string]string{
		"FIXTURE_ACK_DIR":      installDir,
		"FIXTURE_ACK_VERSION":  "0.4.0",
		"FIXTURE_CHILD_MARKER": childMarker,
		"FIXTURE_RESULT":       helperResult,
	})

	// The healthy new installation acknowledges; the helper confirms and
	// removes every transaction artifact.
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
	if !equalDigests(mustDigest(t, livePath), mustDigest(t, newFixture)) {
		t.Error("live executable does not carry the new installation")
	}
	waitForContent(t, childMarker, func(s string) bool { return strings.HasPrefix(s, "acked") }, 20*time.Second)
	if _, err := os.Stat(journalPath(installDir)); !errors.Is(err, os.ErrNotExist) {
		t.Error("journal survived successful health acknowledgement")
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
	if marker := waitForContent(t, childMarker, func(string) bool { return true }, time.Second); strings.HasPrefix(marker, "ready") {
		if pid := markerPid(t, marker); pid > 0 {
			killLater(t, pid)
		}
	}
}

func TestFileInstallRollsBackOnAcknowledgementTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file transaction")
	}
	installDir := runFileTransaction(t, map[string]string{
		"FIXTURE_ACK_DELAY_MS": "60000", // never acknowledges within the window
	}, 1500*time.Millisecond)

	// The helper must restore the backup and relaunch the previous
	op := waitJournal(t, installDir, 15*time.Second, PhaseRolledBack)
	if op.Phase != PhaseRolledBack || !op.SuppressNext {
		t.Fatalf("journal after timeout = %+v, want rolled-back with suppression", op)
	}
	if op.FailedError != "" {
		t.Errorf("unexpected failure evidence: %q", op.FailedError)
	}
	livePath := filepath.Join(installDir, "torrent-tv")
	waitForContent(t, livePath, func(string) bool { return true }, 5*time.Second)
	if !equalDigests(mustDigest(t, livePath), mustDigest(t, hostFixture(t, "0.3.0"))) {
		t.Error("timeout did not restore the previous installation")
	}
	backupGone(t, installDir, op.Backup)

	// The relaunched previous installation must not silently clear the
	// suppression: an acknowledgement attempt against a rolled-back
	// operation fails and the journal stays.
	marker := filepath.Join(t.TempDir(), "unused")
	_ = marker
	waitForFile(t, journalPath(installDir), 10*time.Second) // still present
	op2 := readJournal(t, installDir)
	if op2.Phase != PhaseRolledBack || !op2.SuppressNext {
		t.Errorf("suppression not preserved: %+v", op2)
	}
}

func backupGone(t *testing.T, installDir, backup string) {
	t.Helper()
	if backup == "" {
		return
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup %s survived rollback: %v", backup, err)
	}
}

func TestFileInstallRollsBackOnEarlyDeath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file transaction")
	}
	installDir := runFileTransaction(t, map[string]string{
		"FIXTURE_EXIT_IMMEDIATELY": "1",
	}, 10*time.Second)
	op := waitJournal(t, installDir, 15*time.Second, PhaseRolledBack)
	if op.Phase != PhaseRolledBack || !op.SuppressNext {
		t.Fatalf("journal after early death = %+v, want rolled-back with suppression", op)
	}
	livePath := filepath.Join(installDir, "torrent-tv")
	waitForContent(t, livePath, func(string) bool { return true }, 5*time.Second)
	if !equalDigests(mustDigest(t, livePath), mustDigest(t, hostFixture(t, "0.3.0"))) {
		t.Error("early death did not restore the previous installation")
	}
	backupGone(t, installDir, op.Backup)
}

func TestFileInstallRollbackFailureKeepsEvidenceAndLivePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix file transaction")
	}
	newFixture := hostFixture(t, "0.4.0")
	oldFixture := hostFixture(t, "0.3.0")

	installDir := t.TempDir()
	livePath := writeLiveExecutable(t, installDir, oldFixture)
	cleanupLiveProcesses(t, livePath)
	newPayload := fileBytes(t, newFixture)
	archive := buildTarGz(t, happyTarMembers(newPayload))
	sel := testSelection(fileAssetName("0.4.0"), "0.4.0", archive)

	tmp := t.TempDir()
	helperResult := filepath.Join(tmp, "helper-result")
	healthTimeout := 1200 * time.Millisecond
	runApplyProcess(t, installDir, archive, sel, hostTarget(), "file", livePath, "", healthTimeout, map[string]string{
		"FIXTURE_ACK_DIR":      installDir,
		"FIXTURE_ACK_VERSION":  "0.4.0",
		"FIXTURE_CHILD_MARKER": filepath.Join(tmp, "child-marker"),
		"FIXTURE_RESULT":       helperResult,
		"FIXTURE_ACK_DELAY_MS": "60000",
	})

	// Sabotage the rollback: remove the durable backup while the helper is
	// inside its health window. The restore must then fail closed, record
	op := waitJournal(t, installDir, 5*time.Second, PhaseActivated)
	if err := os.Remove(op.Backup); err != nil {
		t.Fatalf("remove backup: %v", err)
	}
	result := waitForFile(t, helperResult, 20*time.Second)
	if !strings.Contains(result, "restore previous installation") {
		t.Fatalf("helper result %q does not report the rollback failure", result)
	}
	op = readJournal(t, installDir)
	if op.FailedError == "" {
		t.Error("rollback failure left no durable evidence")
	}
	if recovery := EvaluateRecovery(op, InstallState{CurrentVersion: "0.4.0", Now: time.Now()}); recovery.Action != RecoveryManual {
		t.Errorf("recovery action = %q, want manual", recovery.Action)
	}
	// The live path still runs the new installation: nothing was destroyed.
	if !equalDigests(mustDigest(t, livePath), mustDigest(t, newFixture)) {
		t.Error("failed rollback corrupted the live path")
	}
}

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
