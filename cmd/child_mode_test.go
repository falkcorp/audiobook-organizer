// file: cmd/child_mode_test.go
// version: 1.0.4
// guid: 5e2b9c71-3f4a-4d86-a0e7-8c1f6b2d9a34
// last-edited: 2026-11-09

package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
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
	case "empty":
		// A fresh install: the real store on a new, empty database
		// (registry.EnvChildDBPath is set by the parent test to a temp dir).
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	newServer = func(database.Store, ...server.ServerOption) (*server.Server, error) {
		os.Exit(reachedServerExitCode)
		return nil, nil
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
	// The seeding boot opens core.log.file under the state directory it
	// resolves; with the variable unset that is the default (or the working
	// directory's logs/ on a host where the default cannot be created), which
	// a restricted user cannot write. Point it at the test's own directory.
	t.Setenv(config.SecureStateDirEnv, stateDir)
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

// A store whose identity cutover (M3) is done is core's: its user, role,
// session and API-key rows are drained. The child builds its server without
// server.WithIdentity, so HEAD's own gates (including the user-count first-run
// bypass) would run over those empty tables and open. The child refuses before
// it reaches the server. TestRunOperationRunner_EmptySettingsProceeds is the
// other half: a store the cutover has not run on still proceeds.
func TestRunOperationRunner_RefusesAStoreWhoseIdentityCutoverIsDone(t *testing.T) {
	stateDir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	t.Setenv(config.SecureStateDirEnv, stateDir)
	seedStarted(t, dbPath)

	code, out := runChildModeHelperIn(t, "empty", stateDir, dbPath)
	if code == reachedServerExitCode {
		t.Fatalf("child mode built the server over a store whose identity cutover is done; output:\n%s", out)
	}
	if code != 2 {
		t.Fatalf("child mode exit code = %d, want 2 (refused); output:\n%s", code, out)
	}
	if !strings.Contains(out, errChildOverIdentityStore.Error()) {
		t.Errorf("child mode output does not carry the identity-cutover refusal:\n%s", out)
	}
}

// cutoverStateStore is a store that answers the identity cutover's state as
// told.
type cutoverStateStore struct {
	database.Store
	done bool
	err  error
}

func (s cutoverStateStore) DataMigrationDone(context.Context, string) (bool, error) {
	return s.done, s.err
}

// refuseChildOverIdentityStore fails closed: only a store that says its
// identity cutover is not done proceeds. A store that cannot be asked, and a
// read that fails, both refuse.
func TestRefuseChildOverIdentityStore_FailsClosed(t *testing.T) {
	ctx := context.Background()
	if err := refuseChildOverIdentityStore(ctx, cutoverStateStore{done: false}); err != nil {
		t.Errorf("a store whose identity cutover is not done was refused: %v", err)
	}
	if err := refuseChildOverIdentityStore(ctx, cutoverStateStore{done: true}); !errors.Is(err, errChildOverIdentityStore) {
		t.Errorf("a store whose identity cutover is done: err = %v, want errChildOverIdentityStore", err)
	}
	boom := errors.New("storage admin: closed")
	if err := refuseChildOverIdentityStore(ctx, cutoverStateStore{err: boom}); !errors.Is(err, boom) {
		t.Errorf("a failed read of the cutover state did not refuse with the read error: %v", err)
	}
	// A store with no way to answer (database.Store alone has no
	// DataMigrationDone) is refused, not assumed to be unmigrated.
	if err := refuseChildOverIdentityStore(ctx, nil); err == nil {
		t.Error("a store that cannot report its identity cutover was let through")
	}
}
