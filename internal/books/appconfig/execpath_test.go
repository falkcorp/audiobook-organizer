// file: internal/books/appconfig/execpath_test.go
// version: 1.0.0
// guid: b17e5c3a-4d92-4f08-a6c1-8e3f0d2b9a57
// last-edited: 2026-11-19

package appconfig

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

func writeExec(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // chmod, not WriteFile's mode: umask-proof
		t.Fatal(err)
	}
	return p
}

func execUpdate(t *testing.T, key, val string) error {
	t.Helper()
	e, f := foldEngine(t, `[]`)
	_, err := e.Update(t.Context(), adminActor, set(key, `"`+val+`"`))
	if err != nil && len(f.applied) != 0 {
		t.Fatalf("a refused %s wrote", key)
	}
	return err
}

// TestExecPaths_FileKeys: a custom executable path is refused unless it is an
// absolute, clean path to an existing executable regular file that no one but its
// owner and group can write, in a directory the same holds for.
func TestExecPaths_FileKeys(t *testing.T) {
	root := t.TempDir()
	good := writeExec(t, root, "fpcalc", 0o755)
	loose := writeExec(t, root, "loose", 0o777)
	plain := writeExec(t, root, "plain", 0o644)
	openDir := filepath.Join(root, "open")
	if err := os.Mkdir(openDir, 0o700); err != nil {
		t.Fatal(err)
	}
	inOpen := writeExec(t, openDir, "fpcalc", 0o755)
	if err := os.Chmod(openDir, 0o777); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tools.fpcalc.custom_path", "tools.ollama.custom_path"} {
		for name, c := range map[string]struct {
			val  string
			want string // "" = accepted
		}{
			"good":               {good, ""},
			"empty uses managed": {"", ""},
			"relative":           {"bin/fpcalc", "absolute"},
			"dotdot":             {root + "/sub/../fpcalc", "clean"},
			"missing":            {filepath.Join(root, "nope"), "cannot be used"},
			"directory":          {sub, "regular file"},
			"world writable":     {loose, "writable by everyone"},
			"not executable":     {plain, "executable"},
			"in an open dir":     {inOpen, "directory must not be writable"},
		} {
			t.Run(key+"/"+name, func(t *testing.T) {
				err := execUpdate(t, key, c.val)
				if c.want == "" {
					if err != nil {
						t.Fatalf("err = %v", err)
					}
					return
				}
				if !errors.Is(err, coreconfig.ErrInvalid) || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err = %v; want ErrInvalid naming %s and %q", err, key, c.want)
				}
			})
		}
	}
}

// TestExecPaths_ManagedDir: the install directory may not exist yet, but its
// parent must be one nobody else can write, and an existing one must not be open
// or a file.
func TestExecPaths_ManagedDir(t *testing.T) {
	root := t.TempDir()
	file := writeExec(t, root, "afile", 0o755)
	open := filepath.Join(root, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ val, want string }{
		"existing":                 {root, ""},
		"not yet created":          {filepath.Join(root, "tools"), ""},
		"under an open directory":  {filepath.Join(open, "tools"), "directory must not be writable"},
		"an open directory itself": {open, "writable by everyone"},
		"a file":                   {file, "directory"},
		"relative":                 {"tools", "absolute"},
	} {
		t.Run(name, func(t *testing.T) {
			err := execUpdate(t, "tools.managed_dir", c.val)
			if c.want == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errors.Is(err, coreconfig.ErrInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v; want ErrInvalid %q", err, c.want)
			}
		})
	}
}

// TestExecPaths_UnchangedValueIsNotJudgedAgain: a save that sends back the stored
// path (which has since been deleted) is not refused for it, while changing it to
// another bad path still is.
func TestExecPaths_UnchangedValueIsNotJudgedAgain(t *testing.T) {
	dir := t.TempDir()
	was := writeExec(t, dir, "fpcalc", 0o755)
	e, _ := foldEngine(t, `[]`)
	if _, err := e.Update(t.Context(), adminActor, set("tools.fpcalc.custom_path", `"`+was+`"`)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(was); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("tools.fpcalc.custom_path", `"`+was+`"`)); err != nil {
		t.Fatalf("a save that sends the stored path back: %v", err)
	}
	if _, err := e.Update(t.Context(), adminActor, set("tools.fpcalc.custom_path", `"`+was+`2"`)); !errors.Is(err, coreconfig.ErrInvalid) {
		t.Fatalf("changed to another missing path: %v", err)
	}
}

// TestExecPaths_ClearAndModesAreFree: clearing a path and the mode rows (a choice,
// not a path) are never refused by this rule.
func TestExecPaths_ClearAndModesAreFree(t *testing.T) {
	e, _ := foldEngine(t, `[]`)
	if _, err := e.Update(t.Context(), adminActor, set("tools.fpcalc.mode", `"managed"`, "tools.ollama.mode", `"managed"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"tools.fpcalc.custom_path"}}); err != nil {
		t.Fatal(err)
	}
}

// toolsRowsThatAreNotPaths are the tools.* rows that name neither a program nor a
// directory (a switch and a number), so no path rule applies to them.
var toolsRowsThatAreNotPaths = []string{"tools.allow_periodic_ollama", "tools.ollama_debounce_min"}

// TestExecPaths_EveryToolsRowIsClassified: a new tools.* row must be put in
// execPathRows, execModeRows or the list above on purpose, and every path and
// mode row is admin in the rename table.
func TestExecPaths_EveryToolsRowIsClassified(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range cutover.Table().Rows {
		if !strings.HasPrefix(r.Head, "tools.") {
			continue
		}
		seen[r.Head] = true
		_, isPath := execPathRows[r.Head]
		isMode := slices.Contains(execModeRows, r.Head)
		switch {
		case !isPath && !isMode && !slices.Contains(toolsRowsThatAreNotPaths, r.Head):
			t.Errorf("%s is in none of execPathRows, execModeRows, toolsRowsThatAreNotPaths", r.Head)
		case (isPath || isMode) && !r.Admin:
			t.Errorf("%s is not admin in the rename table: a program path or choice must need the admin role", r.Head)
		}
	}
	for h := range execPathRows {
		if !seen[h] {
			t.Errorf("execPathRows names %s, which the rename table does not have", h)
		}
	}
}

// TestExecPaths_NonAdminLearnsNothingAboutTheFilesystem: a caller short of the
// admin role gets the same refusal for a path that exists and one that does not,
// and no filesystem check runs for either. Authorization is decided before the
// path is looked at.
func TestExecPaths_NonAdminLearnsNothingAboutTheFilesystem(t *testing.T) {
	root := t.TempDir()
	exists := writeExec(t, root, "fpcalc", 0o755)
	missing := filepath.Join(root, "nothing-here")
	loose := writeExec(t, root, "loose", 0o777)
	user := config.Actor{Subject: "user:robin", Interactive: true, Can: func(string) bool { return false }}

	calls := 0
	prev := pathChecker
	pathChecker = func(p string, k execPathKind) error { calls++; return prev(p, k) }
	t.Cleanup(func() { pathChecker = prev })

	var msgs []string
	for _, key := range []string{"tools.fpcalc.custom_path", "tools.ollama.custom_path", "tools.managed_dir"} {
		for _, p := range []string{exists, missing, loose, "relative/path"} {
			e, f := foldEngine(t, `[]`)
			_, err := e.Update(t.Context(), user, set(key, `"`+p+`"`))
			if !errors.Is(err, coreconfig.ErrForbidden) {
				t.Fatalf("%s=%s: non-admin got %v, want ErrForbidden", key, p, err)
			}
			if len(f.applied) != 0 {
				t.Fatalf("%s=%s: a refused write wrote", key, p)
			}
			// The refusal names the key and the permission, never the path.
			msgs = append(msgs, strings.ReplaceAll(err.Error(), key, "KEY"))
			if strings.Contains(err.Error(), p) {
				t.Fatalf("the refusal echoes the path: %v", err)
			}
		}
	}
	for _, m := range msgs {
		if m != msgs[0] {
			t.Fatalf("refusals differ by path or kind:\n%q\n%q", msgs[0], m)
		}
	}
	if calls != 0 {
		t.Fatalf("the validator ran %d times for a caller who may not write the key", calls)
	}
}

// TestExecPaths_RefusalNeverEchoesTheOSError: an admin who names a path that cannot
// be stat'ed gets a fixed message; the operating system's text stays in the log.
func TestExecPaths_RefusalNeverEchoesTheOSError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-here")
	err := execUpdate(t, "tools.fpcalc.custom_path", missing)
	if !errors.Is(err, coreconfig.ErrInvalid) || !strings.Contains(err.Error(), "cannot be used") {
		t.Fatalf("got %v", err)
	}
	for _, leak := range []string{"no such file", "permission denied", "not a directory"} {
		if strings.Contains(strings.ToLower(err.Error()), leak) {
			t.Fatalf("the refusal leaks the OS error %q: %v", leak, err)
		}
	}
}

// TestExecPaths_EmptyValueIsTheManagedCopy: an empty path is "use the managed
// binary", not a path to judge; it is accepted for every row.
func TestExecPaths_EmptyValueIsTheManagedCopy(t *testing.T) {
	for _, key := range []string{"tools.fpcalc.custom_path", "tools.ollama.custom_path", "tools.managed_dir"} {
		for _, v := range []string{"", "   "} {
			if got := execPathViolations(config.Config{}, set(key, `"`+v+`"`)); len(got) != 0 {
				t.Errorf("%s=%q judged: %v", key, v, got)
			}
		}
	}
}
