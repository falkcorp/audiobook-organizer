// file: internal/server/handlers/auth_dummy_warm_test.go
// version: 1.0.0
// guid: 87f4fc8f-71dd-485a-9f28-a24d8cf860ea
// last-edited: 2026-10-22

package handlers_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

const warmChildEnv = "AORG_TEST_WARM_CHILD"

// The dummy hash is built once per process, so a test in this process cannot
// tell whether the constructor built it. The parent re-executes the test binary
// to run only the child test below, in a process whose dummy is still cold.
func TestNewAuthHandler_WarmsTheLoginDummy(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestWarmChild_NewAuthHandler$", "-test.v")
	cmd.Env = append(os.Environ(), warmChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestWarmChild_NewAuthHandler") {
		t.Fatalf("child process failed: %v\n%s", err, out)
	}
}

func TestWarmChild_NewAuthHandler(t *testing.T) {
	if os.Getenv(warmChildEnv) != "1" {
		t.Skip("runs only as the child of TestNewAuthHandler_WarmsTheLoginDummy")
	}
	if absauth.LoginDummyReady() {
		t.Fatal("the dummy hash was already built before the constructor ran: the child does not start cold")
	}
	handlers.NewAuthHandler(handlersmocks.NewMockAuthStore(t), true)
	if !absauth.LoginDummyReady() {
		t.Fatal("NewAuthHandler did not build the dummy hash: the first unknown-name login would pay for it")
	}
}
