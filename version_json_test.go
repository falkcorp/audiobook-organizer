// file: version_json_test.go
// version: 1.0.0
// guid: bc6dd383-0048-4a8d-9c24-1a0f96894e17
// last-edited: 2026-11-08

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/mediacore/buildid"
)

// runWithArgs runs run() with os.Args set and returns its exit code and
// stdout; cobra must never run.
func runWithArgs(t *testing.T, args ...string) (int, []byte) {
	t.Helper()
	origArgs, origStdout, origExecute := os.Args, os.Stdout, executeCmd
	t.Cleanup(func() {
		os.Args, os.Stdout, executeCmd = origArgs, origStdout, origExecute
	})
	executeCmd = func() error {
		t.Errorf("executeCmd must not run for %v", args)
		return nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"audiobook-organizer"}, args...)
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
	return code, out
}

// TestVersionJSONFields (06 §6.10, FE6-4): --version-json prints exactly the
// five fields of the one list 05-updater's step-3 self-test reads, each
// present, one line, with cobra never run. A go test binary carries no
// vcs.* build settings, so commit is "" here and must still be present;
// buildid's commitFrom table (TestCommitEmptyWithoutVCS) pins the "+dirty"
// suffix for a modified tree. The test binary embeds no bundle, so
// ui_build_id is "dev".
func TestVersionJSONFields(t *testing.T) {
	code, out := runWithArgs(t, "--version-json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !bytes.HasSuffix(out, []byte("}\n")) || bytes.Count(out, []byte("\n")) != 1 {
		t.Fatalf("stdout = %q, want one JSON object on one line", out)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("stdout is not a JSON object of strings: %v: %q", err, out)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"app", "binary_id", "commit", "ui_build_id", "version"}
	if len(keys) != len(want) {
		t.Fatalf("fields = %v, want exactly %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("fields = %v, want exactly %v", keys, want)
		}
	}

	binaryID, err := buildid.Binary(version)
	if err != nil {
		t.Fatal(err)
	}
	for field, wantVal := range map[string]string{
		"app":         books.AppID,
		"version":     version,
		"commit":      buildid.Commit(),
		"ui_build_id": "dev",
		"binary_id":   binaryID,
	} {
		if got[field] != wantVal {
			t.Errorf("%s = %q, want %q", field, got[field], wantVal)
		}
	}
	if got["commit"] != "" {
		t.Errorf("commit = %q in a test binary, want the empty string present", got["commit"])
	}
}

// TestVersionJSONFieldOrder: the fields come out in the spec's order, so a
// person reading the deploy log sees the same line the self-test parses.
func TestVersionJSONFieldOrder(t *testing.T) {
	var out bytes.Buffer
	handled, err := fastExit([]string{"audiobook-organizer", "--version-json"}, &out)
	if !handled || err != nil {
		t.Fatalf("fastExit = %v, %v, want handled", handled, err)
	}
	prefix := `{"app":"abo","version":`
	if !bytes.HasPrefix(out.Bytes(), []byte(prefix)) {
		t.Fatalf("stdout = %q, want it to start %q", out.String(), prefix)
	}
	var last int
	for _, k := range []string{"app", "version", "commit", "ui_build_id", "binary_id"} {
		i := bytes.Index(out.Bytes(), []byte(`"`+k+`":`))
		if i < last {
			t.Fatalf("field %s out of order in %q", k, out.String())
		}
		last = i
	}
}

// TestPrintUpgradeFloor (05-storage TestPrintUpgradeFloor): no build declares
// a floor until R3, so the answer is empty, and cobra never runs.
func TestPrintUpgradeFloor(t *testing.T) {
	code, out := runWithArgs(t, "--print-upgrade-floor")
	if code != 0 || len(out) != 0 {
		t.Fatalf("exit %d, stdout %q; want 0 and nothing", code, out)
	}
}

// Only the exact one-argument form is a fast exit; anything else is left to
// cobra.
func TestFastExitLeavesOtherArgumentsAlone(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"audiobook-organizer"},
		{"audiobook-organizer", "serve"},
		{"audiobook-organizer", "--version-json", "extra"},
		{"audiobook-organizer", "serve", "--version-json"},
		{"audiobook-organizer", "--print-upgrade-floor", "extra"},
	} {
		var out bytes.Buffer
		handled, err := fastExit(args, &out)
		if handled || err != nil || out.Len() != 0 {
			t.Errorf("fastExit(%q) = %v, %v with stdout %q, want not handled and silent", args, handled, err, out.String())
		}
	}
}
