// file: main_test.go
// version: 1.4.1
// guid: 9c3cc5d7-3d49-4e97-a0c1-9b2e38a9986f
// last-edited: 2026-11-18

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
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

// TestRunPrintStorageLevels (05-storage §5S.3.7; R1-23): the image probe and
// R1-27's rollback guard run a binary with --print-storage-levels and parse
// stdout as the JSON level vector it serves, so stdout must be exactly that
// object and cobra must never run. Release R1 raises no level (09 §9.2.2):
// core and books both serve level 0. A trailing --db PATH (the brief's
// acceptance command) is accepted and ignored: the flag opens nothing.
func TestRunPrintStorageLevels(t *testing.T) {
	for _, args := range [][]string{
		{"audiobook-organizer", "--print-storage-levels"},
		{"audiobook-organizer", "--print-storage-levels", "--db", "/arkham/x.pebble"},
		{"audiobook-organizer", "--print-storage-levels", "--db=/arkham/x.pebble"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) { runPrintStorageLevels(t, args) })
	}
}

// TestFastExitArgs: only a trailing --db is dropped; anything else reaches
// FastExit unchanged, which then declines (cobra reports the argument).
func TestFastExitArgs(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{[]string{"a", "--print-storage-levels", "--db", "x"}, []string{"a", "--print-storage-levels"}},
		{[]string{"a", "--print-storage-levels", "--db=x"}, []string{"a", "--print-storage-levels"}},
		{[]string{"a", "--print-storage-levels", "--verbose"}, []string{"a", "--print-storage-levels", "--verbose"}},
		{[]string{"a", "--print-storage-levels", "--db"}, []string{"a", "--print-storage-levels", "--db"}},
		{[]string{"a", "--print-capabilities", "--db", "x"}, []string{"a", "--print-capabilities", "--db", "x"}},
		{[]string{"a", "serve", "--db", "x"}, []string{"a", "serve", "--db", "x"}},
	} {
		if got := fastExitArgs(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("fastExitArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func runPrintStorageLevels(t *testing.T, args []string) {
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
	os.Args = args
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

// TestPrefreezeTag pins the one reader rule of ci/prefreeze_release.txt, shared
// with scripts/ci/fetch_prefreeze_binary.sh --resolve and F3-08: the single
// line that is neither blank nor starts with '#' is the tag, trimmed; no such
// line or two of them is an error, never a guess.
func TestPrefreezeTag(t *testing.T) {
	const header = "# file: ci/prefreeze_release.txt\n# version: 1.0.0\n# guid: 00000000-0000-4000-8000-000000000000\n# last-edited: 2026-11-18\n"
	for name, tc := range map[string]struct {
		in   string
		want string // "" = must fail
	}{
		"f3-08-shape":      {in: header + "# The pre-freeze release; one tag line.\nv0.232.0\n", want: "v0.232.0"},
		"blank-and-spaces": {in: header + "   \n\t\n  v0.3.0\t\n", want: "v0.3.0"},
		"no-final-newline": {in: header + "v0.4.0", want: "v0.4.0"},
		"crlf":             {in: "# c\r\nv0.5.0\r\n", want: "v0.5.0"},
		"indented-comment": {in: "  # not a tag\nv0.6.0\n", want: "v0.6.0"},
		"no-tag-line":      {in: header + "\n\n"},
		"empty":            {in: ""},
		"two-tags":         {in: header + "v0.1.0\nv0.2.0\n"},
		"tag-then-junk":    {in: header + "v0.1.0\njunk\n"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := prefreezeTag(tc.in)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("prefreezeTag = %q, want an error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("prefreezeTag = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestEmbeddedPrefreezeTag: the build embeds ci/prefreeze_release.txt and the
// tag it reads is the file's tag line, a release tag shape (vX.Y.Z), which is
// what the checkpoint records as from_release.
func TestEmbeddedPrefreezeTag(t *testing.T) {
	raw, err := os.ReadFile("ci/prefreeze_release.txt")
	if err != nil {
		t.Fatal(err)
	}
	want, err := prefreezeTag(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := embeddedPrefreezeTag(); got != want {
		t.Fatalf("embedded tag = %q, file's tag = %q", got, want)
	}
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(want) {
		t.Fatalf("tag %q is not a vX.Y.Z release tag", want)
	}
}

// TestRunHandsServeThePrefreezeTag: run() gives cmd the tag the build embeds
// before cobra parses, so serve can record it as the checkpoint's from_release.
func TestRunHandsServeThePrefreezeTag(t *testing.T) {
	origExecute, origSet := executeCmd, setFromRelease
	t.Cleanup(func() { executeCmd, setFromRelease = origExecute, origSet })
	var got []string
	setFromRelease = func(tag string) { got = append(got, tag) }
	executeCmd = func() error {
		if len(got) != 1 {
			t.Errorf("Execute ran after %d hand-overs of the tag, want 1", len(got))
		}
		return nil
	}
	if code := run(); code != 0 {
		t.Fatalf("run() = %d", code)
	}
	if len(got) != 1 || got[0] != embeddedPrefreezeTag() {
		t.Fatalf("hand-overs = %q, want [%q]", got, embeddedPrefreezeTag())
	}
}
