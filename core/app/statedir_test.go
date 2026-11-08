// file: core/app/statedir_test.go
// version: 1.0.1
// guid: a20ef66a-e2f7-4b90-854b-a8192ad08ee6
// last-edited: 2026-11-07

package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/coretest/logcapture"
)

func TestResolveStateDir_Rule(t *testing.T) {
	denied := errors.New("read-only file system")
	mkdir := func(ok ...string) func(string) error {
		return func(d string) error {
			if slices.Contains(ok, d) {
				return nil
			}
			return denied
		}
	}
	cases := []struct {
		name              string
		explicit, def, db string
		mkdir             func(string) error
		want              string
		fellBack, wantErr bool
	}{
		{"explicit wins", "/batcave/state", "/manor/state", "/gotham/wayne.db", mkdir("/batcave/state", "/manor/state"), "/batcave/state", false, false},
		{"explicit never falls back", "/batcave/state", "/manor/state", "/gotham/wayne.db", mkdir("/manor/state"), "", false, true},
		{"default", "", "/manor/state", "/gotham/wayne.db", mkdir("/manor/state"), "/manor/state", false, false},
		{"beside an absolute database", "", "/manor/state", "/gotham/wayne.db", mkdir(), "/gotham", true, false},
		{"no default, absolute database", "", "", "/gotham/wayne.db", mkdir(), "/gotham", true, false},
		{"relative database fails", "", "/manor/state", "wayne.db", mkdir(), "", false, true},
		{"nothing at all fails", "", "", "", mkdir(), "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, fellBack, err := resolveStateDir(tc.explicit, tc.def, tc.db, tc.mkdir)
			if dir != tc.want || fellBack != tc.fellBack || (err != nil) != tc.wantErr {
				t.Fatalf("resolveStateDir = %q, %v, %v; want %q, %v, error %v", dir, fellBack, err, tc.want, tc.fellBack, tc.wantErr)
			}
		})
	}
}

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
	// An explicit value that cannot be used never falls back.
	opts.Env = []string{"HELLO_CORE_PATHS_STATE_DIR=" + filepath.Join(blocker, "x"), "HELLO_CORE_STORAGE_PATH=/gotham/wayne.db"}
	if _, err := ResolveStateDir(opts); err == nil {
		t.Fatal("an unusable explicit state directory fell back")
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
	// No absolute database: the error names the variable that chooses one.
	opts.Env = []string{}
	if _, err := ResolveStateDir(opts); err == nil || !strings.Contains(err.Error(), "HELLO_CORE_PATHS_STATE_DIR") {
		t.Fatalf("no fallback = %v, want an error naming HELLO_CORE_PATHS_STATE_DIR", err)
	}
	// The env prefix is checked.
	opts.EnvPrefix = "hello"
	if _, err := ResolveStateDir(opts); err == nil {
		t.Fatal("a lower-case EnvPrefix was accepted")
	}
}
