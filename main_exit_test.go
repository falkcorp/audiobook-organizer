// file: main_exit_test.go
// version: 0.1.0
// guid: 5b0e7c3a-91d4-4f62-8a1e-c47d2f9b6e15
// last-edited: 2026-11-21

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// exitChildEnv selects the refusal the re-executed test binary's main returns.
const exitChildEnv = "ABO_MAIN_EXIT_CHILD"

// exitChildErrors are the errors serve's Execute hands main in the child, by
// exitChildEnv value: each wrapped the way serve wraps a boot's error.
var exitChildErrors = map[string]error{
	"cutover-blocked": fmt.Errorf("serve: app.New: %w", storage.ErrCutoverBlocked),
	"level-too-new":   fmt.Errorf("serve: app.New: %w", &storage.LevelTooNewError{Owner: "books", Store: 2, Supported: 1}),
	"upgrade-floor":   fmt.Errorf("serve: app.New: %w", &storage.ErrUpgradeFloor{Owner: "books", Have: 1, Floor: 3, InstallFirst: "v9.9.9"}),
	"other":           errors.New("serve: the Joker stole the config file"),
}

// TestMainExitChild is the re-executed half of TestMain_RefusalExitStatus: it
// runs main itself, so os.Exit's status is the one the parent observes. It
// does nothing in a normal test run.
func TestMainExitChild(t *testing.T) {
	which := os.Getenv(exitChildEnv)
	if which == "" {
		t.Skip("the child half of TestMain_RefusalExitStatus")
	}
	err, ok := exitChildErrors[which]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown %s %q\n", exitChildEnv, which)
		os.Exit(2)
	}
	os.Args = os.Args[:1] // no fast-exit argument: run reaches executeCmd
	executeCmd = func() error { return err }
	main()
}

// TestMain_RefusalExitStatus (R3-02b; 09 §9.2.2, row "Store stamped above the
// build's level"): the process exit for a boot refusal is 78, which the unit's
// RestartPreventExitStatus=78 keeps from restart-looping, and a floor refusal's
// last stderr line is "install <tag> first". Any other failure exits 1. The
// child runs without XPC_SERVICE_NAME, so ExitStatus does not take launchd's
// 60-second backoff.
func TestMain_RefusalExitStatus(t *testing.T) {
	if os.Getenv(exitChildEnv) != "" {
		t.Skip("in the child")
	}
	for _, tc := range []struct {
		which    string
		code     int
		lastLine string
	}{
		{"cutover-blocked", 78, ""},
		{"level-too-new", 78, ""},
		{"upgrade-floor", 78, "install v9.9.9 first"},
		{"other", 1, "serve: the Joker stole the config file"},
	} {
		t.Run(tc.which, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMainExitChild$", "-test.count=1")
			cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
				return strings.HasPrefix(kv, "XPC_SERVICE_NAME=") || strings.HasPrefix(kv, exitChildEnv+"=")
			}), exitChildEnv+"="+tc.which)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("the child did not exit non-zero: %v; stderr:\n%s", err, stderr.String())
			}
			if got := exit.ExitCode(); got != tc.code {
				t.Fatalf("exit status %d, want %d; stderr:\n%s", got, tc.code, stderr.String())
			}
			lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
			last := lines[len(lines)-1]
			if tc.lastLine != "" && last != tc.lastLine {
				t.Fatalf("last stderr line %q, want %q; stderr:\n%s", last, tc.lastLine, stderr.String())
			}
			want := exitChildErrors[tc.which].Error()
			if !slices.Contains(lines, want) {
				t.Fatalf("stderr does not carry the error %q as one line:\n%s", want, stderr.String())
			}
		})
	}
}

// TestUnit_RestartPreventsRefusalExit: the shipped unit keeps a 78 exit from
// restarting, so a refused boot stops instead of looping (the exit status
// TestMain_RefusalExitStatus pins is only half of that promise).
func TestUnit_RestartPreventsRefusalExit(t *testing.T) {
	b, err := os.ReadFile("deploy/audiobook-organizer.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && k == "RestartPreventExitStatus" && slices.Contains(strings.Fields(v), "78") {
			return
		}
	}
	t.Fatal("deploy/audiobook-organizer.service has no RestartPreventExitStatus listing 78")
}
