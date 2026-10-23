// file: internal/server/handlers/abs/handler_dummy_warm_test.go
// version: 1.1.0
// guid: 569e34f9-eadb-45af-811f-deecfd60f269
// last-edited: 2026-10-23

package abs_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
)

const warmChildEnv = "AORG_TEST_WARM_CHILD"

// The dummy hash is built once per process, so the parent re-executes the test
// binary to run only the child test below in a process whose dummy is cold.
func TestNew_WarmsTheLoginDummy(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWarmChild_New$", "-test.v")
	cmd.Env = append(os.Environ(), warmChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestWarmChild_New") {
		t.Fatalf("child process failed: %v\n%s", err, out)
	}
}

func TestWarmChild_New(t *testing.T) {
	if os.Getenv(warmChildEnv) != "1" {
		t.Skip("runs only as the child of TestNew_WarmsTheLoginDummy")
	}
	if absauth.LoginDummyReadyForTest() {
		t.Fatal("the dummy hash was already built before the constructor ran: the child does not start cold")
	}
	newHarness(t, "cf,jwt", nil) // builds the handler with abshandler.New
	if !absauth.LoginDummyReadyForTest() {
		t.Fatal("abs.New did not build the dummy hash: the first unknown-name login would pay for it")
	}
}
