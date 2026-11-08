// file: main_test.go
// version: 1.3.0
// guid: 9c3cc5d7-3d49-4e97-a0c1-9b2e38a9986f
// last-edited: 2026-11-08

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestRunSuccess(t *testing.T) {
	origExecute := executeCmd
	executeCmd = func() error { return nil }
	defer func() {
		executeCmd = origExecute
	}()

	if code := run(); code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
}

func TestRunError(t *testing.T) {
	origExecute := executeCmd
	executeCmd = func() error { return fmt.Errorf("boom") }
	defer func() {
		executeCmd = origExecute
	}()

	if code := run(); code == 0 {
		t.Fatal("expected non-zero exit code")
	}
}

// TestRunPrintStorageFormat: `make rollback` runs the previous binary with
// --print-storage-format and parses stdout, so stdout must be exactly the
// supported format and one newline, and cobra must never run.
func TestRunPrintStorageFormat(t *testing.T) {
	origArgs := os.Args
	origStdout := os.Stdout
	origExecute := executeCmd
	t.Cleanup(func() {
		os.Args = origArgs
		os.Stdout = origStdout
		executeCmd = origExecute
	})

	executeCmd = func() error {
		t.Error("executeCmd must not run for --print-storage-format")
		return nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"audiobook-organizer", "--print-storage-format"}
	os.Stdout = w

	code := run()

	os.Stdout = origStdout
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if string(out) != "1\n" {
		t.Fatalf("stdout = %q, want %q", out, "1\n")
	}
}

// TestRunPrintCapabilities: the deploy and rollback recipes run a binary with
// --print-capabilities and look for an exact `sd_notify` line before letting
// it run under the Type=notify unit, so stdout must be exactly the token list
// and cobra must never run.
func TestRunPrintCapabilities(t *testing.T) {
	origArgs := os.Args
	origStdout := os.Stdout
	origExecute := executeCmd
	t.Cleanup(func() {
		os.Args = origArgs
		os.Stdout = origStdout
		executeCmd = origExecute
	})

	executeCmd = func() error {
		t.Error("executeCmd must not run for --print-capabilities")
		return nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"audiobook-organizer", "--print-capabilities"}
	os.Stdout = w

	code := run()

	os.Stdout = origStdout
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if string(out) != "sd_notify\n" {
		t.Fatalf("stdout = %q, want %q", out, "sd_notify\n")
	}
}

// TestRunPrintStorageLevels (05-storage §5S.3.7; R1-23): `make rollback` and
// the image probe run a binary with --print-storage-levels and parse stdout
// as the JSON level vector it serves, so stdout must be exactly that object
// and cobra must never run. Release R1 raises no level (09 §9.2.2): core and
// books both serve level 0.
func TestRunPrintStorageLevels(t *testing.T) {
	origArgs := os.Args
	origStdout := os.Stdout
	origExecute := executeCmd
	t.Cleanup(func() {
		os.Args = origArgs
		os.Stdout = origStdout
		executeCmd = origExecute
	})

	executeCmd = func() error {
		t.Error("executeCmd must not run for --print-storage-levels")
		return nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"audiobook-organizer", "--print-storage-levels"}
	os.Stdout = w

	code := run()

	os.Stdout = origStdout
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var levels map[string]int
	if err := json.Unmarshal(out, &levels); err != nil {
		t.Fatalf("stdout %q is not a JSON object: %v", out, err)
	}
	if len(levels) != 2 || levels["core"] != 0 || levels["books"] != 0 {
		t.Fatalf("levels = %v, want core and books at 0 (R1 raises no level)", levels)
	}
	if _, ok := levels["books"]; !ok {
		t.Fatalf("levels = %v, want a books entry", levels)
	}
}
