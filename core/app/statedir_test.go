// file: core/app/statedir_test.go
// version: 1.0.2
// guid: a20ef66a-e2f7-4b90-854b-a8192ad08ee6
// last-edited: 2026-11-10

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/coretest/logcapture"
)

// TestResolveStateDir_Env drives ResolveStateDir through the environment on
// a real filesystem. The rule itself is config.ResolveStateDir's, checked row
// by row against HEAD in core/config (TestStateDirFallbackMatchesHEAD and
// TestStateDirFallbackMatchesHEAD_AppResolver); this test covers what app
// adds: the env names, the 0700 mode, the warning and the error text.
func TestResolveStateDir_Env(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker") // a file: nothing can be created below it
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	capture := logcapture.New(t)
	opts := Options{AppID: "hello", StateDir: filepath.Join(blocker, "state"), Logger: capture.Logger()}

	// An explicit value is used, and created 0700.
	explicit := filepath.Join(tmp, "explicit")
	opts.Env = []string{"HELLO_CORE_PATHS_STATE_DIR=" + explicit, "HELLO_CORE_STORAGE_PATH=/gotham/wayne.db"}
	if dir, err := ResolveStateDir(opts); err != nil || dir != explicit {
		t.Fatalf("explicit = %q, %v", dir, err)
	}
	if fi, err := os.Stat(explicit); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("explicit directory mode = %v, %v; want 0700", fi, err)
	}
	// A padded explicit value is trimmed and cleaned before it is created,
	// as HEAD's EnsureSecureStateDir does: the padded path is never made.
	cleaned := filepath.Join(tmp, "arkham")
	opts.Env = []string{"HELLO_CORE_PATHS_STATE_DIR=  " + filepath.Join(tmp, "arkham", "..", "arkham") + "/ "}
	if dir, err := ResolveStateDir(opts); err != nil || dir != cleaned {
		t.Fatalf("padded explicit = %q, %v; want %q", dir, err, cleaned)
	}
	if fi, err := os.Stat(cleaned); err != nil || !fi.IsDir() {
		t.Fatalf("cleaned explicit directory = %v, %v", fi, err)
	}
	if entries, err := os.ReadDir(tmp); err != nil {
		t.Fatal(err)
	} else {
		for _, e := range entries {
			if strings.TrimSpace(e.Name()) != e.Name() {
				t.Fatalf("the padded path %q was created", e.Name())
			}
		}
	}
	// A blank explicit value is unset: the uncreatable default falls back.
	opts.Env = []string{"HELLO_CORE_PATHS_STATE_DIR=   ", "HELLO_CORE_STORAGE_PATH=" + filepath.Join(tmp, "db", "wayne.db")}
	if dir, err := ResolveStateDir(opts); err != nil || dir != filepath.Join(tmp, "db") {
		t.Fatalf("blank explicit = %q, %v; want the fallback", dir, err)
	}
	capture = logcapture.New(t) // a fresh capture: count only what follows
	opts.Logger = capture.Logger()
	// An explicit value that cannot be used never falls back, and the error
	// names the directory and the variable.
	unusable := filepath.Join(blocker, "x")
	opts.Env = []string{"HELLO_CORE_PATHS_STATE_DIR=" + unusable, "HELLO_CORE_STORAGE_PATH=" + filepath.Join(tmp, "db", "wayne.db")}
	if dir, err := ResolveStateDir(opts); err == nil || dir != unusable ||
		!strings.Contains(err.Error(), "HELLO_CORE_PATHS_STATE_DIR") || !strings.Contains(err.Error(), unusable) {
		t.Fatalf("unusable explicit = %q, %v; want an error naming %s and HELLO_CORE_PATHS_STATE_DIR", dir, err, unusable)
	}
	// An empty value is unset (05-config C3): the uncreatable default falls
	// back beside the absolute database, with the warning.
	opts.Env = []string{"HELLO_CORE_PATHS_STATE_DIR=", "HELLO_CORE_STORAGE_PATH=" + filepath.Join(tmp, "db", "wayne.db")}
	if dir, err := ResolveStateDir(opts); err != nil || dir != filepath.Join(tmp, "db") {
		t.Fatalf("fallback = %q, %v", dir, err)
	}
	if msgs := capture.Messages(); len(msgs) != 1 || !strings.Contains(msgs[0], "beside the database") {
		t.Fatalf("fallback warning = %v", msgs)
	}
	// A database directory that cannot be created is no fallback: the
	// default's error comes back with the default, and nothing is logged.
	capture = logcapture.New(t) // a fresh capture: count only what follows
	opts.Logger = capture.Logger()
	opts.Env = []string{"HELLO_CORE_STORAGE_PATH=" + filepath.Join(blocker, "db", "wayne.db")}
	if dir, err := ResolveStateDir(opts); err == nil || dir != opts.StateDir || !strings.Contains(err.Error(), "HELLO_CORE_PATHS_STATE_DIR") {
		t.Fatalf("uncreatable fallback = %q, %v; want %q and an error naming HELLO_CORE_PATHS_STATE_DIR", dir, err, opts.StateDir)
	}
	if msgs := capture.Messages(); len(msgs) != 0 {
		t.Fatalf("an uncreatable fallback logged %v", msgs)
	}
	// No absolute database: the error names the variable that chooses one.
	opts.Env = []string{}
	if _, err := ResolveStateDir(opts); err == nil || !strings.Contains(err.Error(), "HELLO_CORE_PATHS_STATE_DIR") {
		t.Fatalf("no fallback = %v, want an error naming HELLO_CORE_PATHS_STATE_DIR", err)
	}
	// No default and nothing set: an error, never a directory.
	if dir, err := ResolveStateDir(Options{AppID: "hello", Env: []string{}, Logger: capture.Logger()}); err == nil || dir != "" ||
		!strings.Contains(err.Error(), "declares no default") || !strings.Contains(err.Error(), "HELLO_CORE_PATHS_STATE_DIR") {
		t.Fatalf("no default = %q, %v; want an error saying no default is declared and naming HELLO_CORE_PATHS_STATE_DIR", dir, err)
	}
	// The env prefix is checked.
	opts.EnvPrefix = "hello"
	if _, err := ResolveStateDir(opts); err == nil {
		t.Fatal("a lower-case EnvPrefix was accepted")
	}
}
