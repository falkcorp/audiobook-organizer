// file: cmd/child_mode_test.go
// version: 1.0.0
// guid: 5e2b9c71-3f4a-4d86-a0e7-8c1f6b2d9a34
// last-edited: 2026-10-10

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
		initializeStore = func(string, string) (database.Store, error) {
			return mocks.NewMockStore(t), nil
		}
		closeStore = func() error { return nil }
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
	newServer = func(database.Store) *server.Server {
		os.Exit(reachedServerExitCode)
		return nil
	}

	RunOperationRunner()
	t.Fatal("RunOperationRunner returned")
}

func runChildModeHelper(t *testing.T, mode string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildModeHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		childModeHelperEnv+"="+mode,
		registry.EnvChildDBType+"=pebble",
		registry.EnvChildDBPath+"="+filepath.Join(t.TempDir(), "audiobooks.pebble"),
		registry.EnvChildRootDir+"="+t.TempDir())
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
