// file: cmd/child_mode_test.go
// version: 1.0.3
// guid: 5e2b9c71-3f4a-4d86-a0e7-8c1f6b2d9a34
// last-edited: 2026-11-09

package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// childModeHelperEnv selects the scenario TestChildModeHelperProcess runs when
// this test binary is re-executed as a child-mode process.
const childModeHelperEnv = "AORG_TEST_CHILD_MODE_HELPER"

// reachedServerExitCode is what the helper exits with when RunOperationRunner
// gets as far as building the server, i.e. it would go on to run the op.
const reachedServerExitCode = 7

// TestChildModeHelperProcess is not a test on its own: it is the body of the
// re-executed child process for the tests below, because RunOperationRunner
// ends in os.Exit. It is a no-op unless childModeHelperEnv is set.
func TestChildModeHelperProcess(t *testing.T) {
	mode := os.Getenv(childModeHelperEnv)
	if mode == "" {
		t.Skip("helper process only")
	}

	switch mode {
	case "fail":
		initializeChildStore = func(string, string, database.StandaloneOptions) (database.Store, error) {
			return mocks.NewMockStore(t), nil
		}
		closeChildStore = func() error { return nil }
		loadConfigFromDB = func(database.SettingsStore) error {
			return errors.New("read settings: boom")
		}
	case "empty":
		// A fresh install: the real store and the real settings load, on a
		// new, empty database (registry.EnvChildDBPath is set by the parent
		// test to a temp dir).
		loadConfigFromDB = config.LoadConfigFromDatabase
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	newServer = func(database.Store, ...server.ServerOption) *server.Server {
		os.Exit(reachedServerExitCode)
		return nil
	}

	RunOperationRunner()
	t.Fatal("RunOperationRunner returned")
}

func runChildModeHelper(t *testing.T, mode string) (int, string) {
	t.Helper()
	// The child reads the swap slot under the state directory before it
	// opens the store: keep it off the host's.
	return runChildModeHelperIn(t, mode, t.TempDir(), filepath.Join(t.TempDir(), "audiobooks.pebble"))
}

// runChildModeHelperIn runs the helper child over the store at dbPath, with
// stateDir as its secure state directory (where the swap slot is). An empty
// stateDir leaves ABK_STATE_DIR unset in the child, so it resolves the
// directory itself.
func runChildModeHelperIn(t *testing.T, mode, stateDir, dbPath string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildModeHelperProcess$", "-test.count=1")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, config.SecureStateDirEnv+"=") {
			env = append(env, kv)
		}
	}
	env = append(env,
		childModeHelperEnv+"="+mode,
		registry.EnvChildDBType+"=pebble",
		registry.EnvChildDBPath+"="+dbPath,
		registry.EnvChildRootDir+"="+t.TempDir())
	if stateDir != "" {
		env = append(env, config.SecureStateDirEnv+"="+stateDir)
	}
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run helper: %v", err)
	}
	return cmd.ProcessState.ExitCode(), stderr.String()
}

// A child op process must not run on defaults when it cannot read the stored
// settings: an op that saves settings writes the whole config struct, so it
// would overwrite them. It exits non-zero before building the server, the same
// as the server's Fatal loadConfigFromDB step. Until 2026-10-10 the failure
// was a warning and the child went on to run the op.
func TestRunOperationRunner_SettingsReadFailureExitsNonZero(t *testing.T) {
	code, out := runChildModeHelper(t, "fail")
	if code == reachedServerExitCode {
		t.Fatalf("child mode went on to build the server after a settings read failure; output:\n%s", out)
	}
	if code == 0 {
		t.Fatalf("child mode exited 0 after a settings read failure; output:\n%s", out)
	}
	if !strings.Contains(out, "failed to load config from database") {
		t.Errorf("child mode output does not name the settings read failure:\n%s", out)
	}
}

// An empty settings keyspace (a fresh install) is not a failure: the child
// carries on to build the server and run the op.
func TestRunOperationRunner_EmptySettingsProceeds(t *testing.T) {
	code, out := runChildModeHelper(t, "empty")
	if code != reachedServerExitCode {
		t.Fatalf("child mode exit code = %d, want %d (reached server construction); output:\n%s",
			code, reachedServerExitCode, out)
	}
}

// A child's store open must not take the HNSW clean-stop marker beside the
// database: the marker is its parent's (R1-15 follow-up review, MINOR-1).
func TestRunOperationRunner_LeavesTheCleanStopMarker(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	marker := filepath.Join(database.HNSWSnapshotDir(dbPath), "hnsw.clean-stop")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte(`{"version":1,"model":"bge-m3"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := runChildModeHelperIn(t, "empty", t.TempDir(), dbPath)
	if code != reachedServerExitCode {
		t.Fatalf("child mode exit code = %d, want %d (reached server construction); output:\n%s",
			code, reachedServerExitCode, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the child's store open took the parent's clean-stop marker: %v", err)
	}
}

// The child opens the live store with the standalone opener, so the state
// directory it passes is what makes that opener read the swap slot: with a
// restore staged for the next start, the child refuses before it opens the
// store, and never reaches the server. Without StateDir the opener reads no
// slot and the child would run its op on the store the swap is about to
// replace.
func TestRunOperationRunner_StagedSwapRefusesTheOpen(t *testing.T) {
	stateDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	seedStarted(t, dbPath)
	stageSwap(t, stateDir, dbPath)

	code, out := runChildModeHelperIn(t, "empty", stateDir, dbPath)
	if code == reachedServerExitCode {
		t.Fatalf("child mode built the server over a store with a staged swap; output:\n%s", out)
	}
	if code != 2 {
		t.Fatalf("child mode exit code = %d, want 2 (the store open refused); output:\n%s", code, out)
	}
	if !strings.Contains(out, "initializeChildStore") || !strings.Contains(out, restorejournal.ErrSwapPending.Error()) {
		t.Errorf("child mode output does not name the refused open and the staged swap:\n%s", out)
	}
}

// TestRunOperationRunner_ResolvesTheStateDirectory (R1-25 review): the
// child is a fresh process, so nothing has resolved its state directory
// when it opens the store. Where the default cannot be created and
// ABK_STATE_DIR is unset, the parent fell back to the database directory
// (config.EnsureSecureStateDir) and restore-offline staged its swap there;
// the child resolves the directory the same way before it opens, so it
// reads that slot and refuses. Passing the unresolved default instead
// fails the open on the swap lock's mkdir, or, on a host where the default
// exists, reads an empty slot and runs the op on the store the swap
// replaces.
func TestRunOperationRunner_ResolvesTheStateDirectory(t *testing.T) {
	dbDir := fallbackStateDir(t)
	dbPath := filepath.Join(dbDir, "audiobooks.pebble")
	seedStarted(t, dbPath)
	stageSwap(t, dbDir, dbPath)

	code, out := runChildModeHelperIn(t, "empty", "", dbPath)
	if code == reachedServerExitCode {
		t.Fatalf("child mode built the server over a store with a staged swap; output:\n%s", out)
	}
	if code != 2 || !strings.Contains(out, restorejournal.ErrSwapPending.Error()) {
		t.Fatalf("child mode exit code = %d, want 2 with the staged swap named (the slot under the resolved directory %s); output:\n%s", code, dbDir, out)
	}
}
