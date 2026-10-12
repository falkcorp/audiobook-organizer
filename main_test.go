// file: main_test.go
// version: 1.2.0
// guid: 9c3cc5d7-3d49-4e97-a0c1-9b2e38a9986f
// last-edited: 2026-10-12

package main

import (
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
