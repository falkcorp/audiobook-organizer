// file: core/examples/hello/cmd/hello/main_test.go
// version: 1.0.0
// guid: c14a83a3-9b72-41fa-8f32-5ceb73536a02
// last-edited: 2026-11-07

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenAPI_Golden runs `hello openapi --out <tmp>`, the command line
// R1-30's generator and the openapi-check gate run, and diffs the result
// against the committed core/openapi/core.json (06 §6.4.5, 05-http-api §4.6).
// A route added to core without regenerating the file fails here.
func TestOpenAPI_Golden(t *testing.T) {
	out := filepath.Join(t.TempDir(), "core.json")
	var stdout bytes.Buffer
	if code, err := run(t.Context(), []string{"openapi", "--out", out}, &stdout); code != 0 || err != nil {
		t.Fatalf("hello openapi = %d, %v", code, err)
	}
	if want := "wrote " + out + "\n"; stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := coreRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(root, "openapi", "core.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("core/openapi/core.json is stale; regenerate it with `go -C core run ./examples/hello/cmd/hello openapi`.\ngot:\n%s", got)
	}
}

func TestRun_Arguments(t *testing.T) {
	for _, args := range [][]string{{"bogus"}, {"openapi", "--bogus"}, {"openapi", "--out", "a", "extra"}} {
		var stdout bytes.Buffer
		if code, err := run(t.Context(), args, &stdout); code != 2 || err == nil {
			t.Errorf("run(%q) = %d, %v; want 2 and an error", args, code, err)
		}
	}
	if _, err := coreRoot(t.TempDir()); err == nil || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("coreRoot outside the core module = %v, want an error suggesting --out", err)
	}
}
