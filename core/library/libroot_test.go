// file: core/library/libroot_test.go
// version: 1.8.0
// guid: 8e1c5a73-d2b4-4f69-a0e8-3b7f9d1c6a25
// last-edited: 2026-11-18

package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestMounted(t *testing.T) {
	populated := t.TempDir()
	if err := os.Mkdir(filepath.Join(populated, "Gotham"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		root    string
		wantErr string
	}{
		{"unset", "", "not configured"},
		{"missing directory", filepath.Join(t.TempDir(), "nope"), "unreadable"},
		{"empty directory (unmounted share)", t.TempDir(), "no library content"},
		{"populated directory", populated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Mounted(tc.root)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Mounted(%q) = %v, want nil", tc.root, err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Mounted(%q) = %v, want an error containing %q", tc.root, err, tc.wantErr)
			}
		})
	}
}

// ReportPath creates the report directory under the root. That directory, and
// the default {root}/.wav-cache, are app-owned and must not make a bare
// (unmounted) mountpoint read as populated, or the first report written to an
// unmounted root would defeat the mount check on every later run.
func TestAppOwnedDotEntriesDoNotMakeARootLookMounted(t *testing.T) {
	root := t.TempDir()
	if err := Mounted(root); err == nil {
		t.Fatal("empty root must read as unmounted before any report dir exists")
	}
	if _, err := ReportPathNotify(root, "", "x.tsv", nil); err != nil {
		t.Fatalf("ReportPath: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".wav-cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Mounted(root)
	if err == nil || !strings.Contains(err.Error(), "no library content") {
		t.Fatalf("a root holding only %s, .wav-cache and .DS_Store must be refused, got %v", ReportDirName, err)
	}
	if err := os.Mkdir(filepath.Join(root, "Gotham"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Mounted(root); err != nil {
		t.Fatalf("a root with real content must pass: %v", err)
	}
}

func TestReportPathNotify(t *testing.T) {
	root := t.TempDir()
	got, err := ReportPathNotify(root, "", "backfill-file-hashes-op1.tsv", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ReportDirName, "backfill-file-hashes-op1.tsv"); got != want {
		t.Fatalf("ReportPath = %q, want %q", got, want)
	}
	if st, err := os.Stat(filepath.Dir(got)); err != nil || !st.IsDir() {
		t.Fatalf("report dir not created: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Dir(got)); len(ents) != 0 {
		t.Fatalf("the write probe must be removed, found %d entries", len(ents))
	}

	explicit := filepath.Join(root, ReportDirName, "r.tsv")
	if got, err := ReportPathNotify(root, explicit, "ignored.tsv", nil); err != nil || got != explicit {
		t.Fatalf("explicit path directly in the report directory: got %q, %v", got, err)
	}
	nested := filepath.Join(root, ReportDirName, "sub", "r.tsv")
	if got, err := ReportPathNotify(root, nested, "ignored.tsv", nil); err == nil {
		t.Fatalf("a path in a subdirectory of the report directory must be refused, got %q", got)
	}

	if _, err := ReportPathNotify("", "", "r.tsv", nil); err == nil {
		t.Fatal("no root and no explicit path must fail")
	}
	if _, err := ReportPathNotify("relative/root", "", "r.tsv", nil); err == nil {
		t.Fatal("a relative root must fail")
	}
}

func TestReportPathUnwritableDirFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ReportDirName)
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if _, err := ReportPathNotify(root, "", "r.tsv", nil); err == nil {
		t.Fatal("a read-only report dir must be refused before any work")
	}
}

// A report directory the helper creates is DirMode (group write kept, so a
// POSIX ACL's mask keeps working) whatever the umask, and is never world-writable.
func TestReportPathCreatesTheDirWithTheStandardModeAndNoWorldWrite(t *testing.T) {
	old := syscall.Umask(0) // so only the requested mode decides
	t.Cleanup(func() { syscall.Umask(old) })
	root := t.TempDir()
	if _, err := ReportPathNotify(root, "", "r.tsv", nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, ReportDirName))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != DirMode {
		t.Fatalf("report dir mode = %o, want %o", got, DirMode)
	}
	if info.Mode().Perm()&worldWrite != 0 {
		t.Fatalf("report dir mode %o is world-writable", info.Mode().Perm())
	}
}

// {root}/.reports must be what this process made. A symlink planted there (to
// redirect the reports) is refused, and nothing is written through it.
func TestReportPathRefusesASymlinkedReportsDir(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, ReportDirName)); err != nil {
		t.Fatal(err)
	}
	_, err := ReportPathNotify(root, "", "r.tsv", nil)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a symlinked report dir must be refused, got %v", err)
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("something was written through the symlink: %v", ents)
	}
}

func TestReportPathRefusesAReportsDirThatIsAFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ReportDirName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPathNotify(root, "", "r.tsv", nil); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("a regular file where the report dir should be must be refused as such, got %v", err)
	}
}

// An existing report dir this user owns has world write, and only world write,
// removed; the callback gets the mode it had. Group bits and the setgid and
// sticky bits are left exactly as found (group bits are a POSIX ACL's mask, so
// clearing them would strip the access the operator granted), and a dir without
// world write is not touched or reported at all. The write then proceeds.
func TestReportPathRemovesOnlyWorldWriteFromAnExistingReportsDir(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode // permission bits as chmod(2) would be given them
		want os.FileMode
		// special is the setgid/sticky bits to set on the dir before the call.
		special os.FileMode
	}{
		{"0750 untouched", 0o750, 0o750, 0},
		{"0755 untouched", 0o755, 0o755, 0},
		{"0700 untouched", 0o700, 0o700, 0},
		{"0775 untouched, group write kept", 0o775, 0o775, 0},
		{"0777 loses world write only", 0o777, 0o775, 0},
		{"0757 loses world write only", 0o757, 0o755, 0},
		{"0702 loses world write only", 0o702, 0o700, 0},
		{"2775 untouched, setgid kept", 0o775, 0o775, os.ModeSetgid},
		{"2777 loses world write, setgid kept", 0o777, 0o775, os.ModeSetgid},
		{"1777 loses world write, sticky kept", 0o777, 0o775, os.ModeSticky},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ReportDirName)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, tc.mode|tc.special); err != nil { // Chmod is not subject to the umask
				t.Fatal(err)
			}
			if info, _ := os.Stat(dir); info.Mode()&tc.special != tc.special {
				t.Skipf("this filesystem does not keep %v on a directory", tc.special)
			}
			before, _ := os.Stat(dir)
			var calls []os.FileMode
			var gotDir string
			path, err := ReportPathNotify(root, "", "r.tsv", func(d string, was os.FileMode) {
				gotDir = d
				calls = append(calls, was)
			})
			if err != nil {
				t.Fatalf("an owned dir must be accepted, got %v", err)
			}
			info, serr := os.Stat(dir)
			if serr != nil {
				t.Fatal(serr)
			}
			if info.Mode().Perm() != tc.want {
				t.Fatalf("dir ended up %o, want %o", info.Mode().Perm(), tc.want)
			}
			if got, was := info.Mode()&(os.ModeSetgid|os.ModeSticky), before.Mode()&(os.ModeSetgid|os.ModeSticky); got != was {
				t.Fatalf("special bits changed from %v to %v", was, got)
			}
			if tc.mode&worldWrite != 0 {
				if len(calls) != 1 || calls[0] != tc.mode || gotDir != dir {
					t.Fatalf("callback got %v for %q, want exactly one call with %o for %q", calls, gotDir, tc.mode, dir)
				}
			} else if len(calls) != 0 {
				t.Fatalf("nothing was changed but the callback ran %v", calls)
			}
			// The write goes ahead: a report file can be created where the path points.
			if werr := os.WriteFile(path, []byte("x"), FileMode); werr != nil {
				t.Fatalf("writing the report after the check failed: %v", werr)
			}
		})
	}
}

// With no callback the same change is made, without panicking on the absent
// notification.
func TestReportPathRemovesWorldWriteWithoutACallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ReportDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPathNotify(root, "", "r.tsv", nil); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o775 {
		t.Fatalf("dir mode %o, want 775", info.Mode().Perm())
	}
}

// When the chmod fails the dir is refused, naming the mode, and the callback is
// not told anything was changed.
func TestReportPathRefusesWhenWorldWriteCannotBeRemoved(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ReportDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	env := defaultReportEnv()
	env.chmod = func(string, os.FileMode) error { return errors.New("operation not permitted") }
	called := false
	_, err := reportPathNotify(root, "", "r.tsv", func(string, os.FileMode) { called = true }, env)
	if err == nil || !strings.Contains(err.Error(), "world write could not be removed") || !strings.Contains(err.Error(), "777") {
		t.Fatalf("a dir that cannot be changed must be refused, got %v", err)
	}
	if called {
		t.Fatal("the callback must not run when the chmod failed")
	}
}

// A world-writable dir that is not ours is refused and left as it was: the
// helper corrects only what this user owns.
func TestReportPathDoesNotTightenADirItDoesNotOwn(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ReportDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	env := defaultReportEnv()
	env.euid = func() int { return os.Geteuid() + 1 }
	called := false
	_, err := reportPathNotify(root, "", "r.tsv", func(string, os.FileMode) { called = true }, env)
	if err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("a dir owned by another user must be refused, got %v", err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o777 {
		t.Fatalf("a dir that is not ours was changed to %o", info.Mode().Perm())
	}
	if called {
		t.Fatal("nothing was tightened, the callback must not run")
	}
}

// The ownership check compares the directory's owner with the user this process
// runs as. A process standing in for another user must refuse a directory this
// one made; this works without root, unlike a real chown.
func TestReportPathRefusesAReportsDirOwnedByAnotherUser(t *testing.T) {
	root := t.TempDir()
	if _, err := ReportPathNotify(root, "", "r.tsv", nil); err != nil {
		t.Fatal(err) // creates the dir as the real user
	}
	env := defaultReportEnv()
	env.euid = func() int { return os.Geteuid() + 1 }
	if _, err := reportPathNotify(root, "", "r.tsv", nil, env); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("a report dir owned by another user must be refused, got %v", err)
	}
}

// reportRootFor makes a library root with a real file in it and returns it with
// its report directory path (not yet created).
func reportRootFor(t *testing.T) (root, reports string) {
	t.Helper()
	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Gotham"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, ReportDirName)
}

// An explicit report path is confined to {root}/.reports: it is a bare file
// name in that directory, or an absolute path whose directory is that directory,
// and the accepted path is what the caller gets back.
func TestReportPathNotifyExplicitAcceptedInsideReports(t *testing.T) {
	root, reports := reportRootFor(t)
	cases := []struct {
		name, explicit, want string
	}{
		{"bare file name", "r.tsv", filepath.Join(reports, "r.tsv")},
		{"bare name with a dot in it", "batman.returns.tsv", filepath.Join(reports, "batman.returns.tsv")},
		{"absolute, directly in the report directory", filepath.Join(reports, "r.tsv"), filepath.Join(reports, "r.tsv")},
		{"absolute with a redundant separator", reports + string(filepath.Separator) + string(filepath.Separator) + "r.tsv", filepath.Join(reports, "r.tsv")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReportPathNotify(root, tc.explicit, "ignored.tsv", nil)
			if err != nil {
				t.Fatalf("a path inside the report directory must be accepted: %v", err)
			}
			if got != tc.want {
				t.Fatalf("path = %q, want %q", got, tc.want)
			}
			if st, serr := os.Stat(filepath.Dir(got)); serr != nil || !st.IsDir() {
				t.Fatalf("parent directory not created: %v", serr)
			}
		})
	}
}

// Anything that would land outside {root}/.reports is refused, and a refusal
// creates nothing outside the report directory.
func TestReportPathNotifyExplicitRefusedOutsideReports(t *testing.T) {
	root, reports := reportRootFor(t)
	outside := t.TempDir()
	cases := []struct {
		name, explicit string
	}{
		{"dot-dot traversal", filepath.Join("..", "escaped.tsv")},
		{"deep dot-dot traversal", filepath.Join("a", "..", "..", "..", "escaped.tsv")},
		{"absolute outside the root", filepath.Join(outside, "escaped.tsv")},
		{"absolute in the root but not the report directory", filepath.Join(root, "Gotham", "escaped.tsv")},
		{"absolute sibling that shares the report directory's prefix", reports + "-other" + string(filepath.Separator) + "escaped.tsv"},
		{"the report directory itself", reports},
		{"the library root", root},
		{"the filesystem root", string(filepath.Separator)},
		{"dot-dot after a nested directory", filepath.Join(reports, "..", "escaped.tsv")},
		{"NUL byte", "bad\x00.tsv"},
		{"relative name with a directory", filepath.Join("batman", "r.tsv")},
		{"relative name with a directory that dot-dot leads back out of", "a" + string(filepath.Separator) + ".." + string(filepath.Separator) + "r.tsv"},
		{"relative name with a leading dot segment", "." + string(filepath.Separator) + "r.tsv"},
		{"relative name with a trailing separator", "r.tsv" + string(filepath.Separator)},
		{"absolute path in a subdirectory of the report directory", filepath.Join(reports, "x", "y", "r.tsv")},
		{"absolute path in one subdirectory of the report directory", filepath.Join(reports, "x", "r.tsv")},
		{"dot", "."},
		{"dot-dot", ".."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReportPathNotify(root, tc.explicit, "ignored.tsv", nil)
			if err == nil {
				t.Fatalf("path %q must be refused, got %q", tc.explicit, got)
			}
			if got != "" {
				t.Fatalf("a refusal must return no path, got %q", got)
			}
		})
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("a refused path created %d entries outside the report directory", len(ents))
	}
	if _, err := os.Stat(filepath.Join(root, "escaped.tsv")); err == nil {
		t.Fatal("a refused path wrote into the library root")
	}
	if _, err := os.Stat(reports + "-other"); err == nil {
		t.Fatal("a refused path created a sibling directory next to the report directory")
	}
}

// A directory inside the report directory that is a symlink to somewhere else
// must not carry a report out of it, whether the target exists or not, and
// whether the symlink is the parent, an ancestor of a not-yet-created parent, or
// the file itself (including a dangling link, which EvalSymlinks reports as
// "does not exist").
func TestReportPathNotifyExplicitRefusesSymlinkEscapes(t *testing.T) {
	root, reports := reportRootFor(t)
	if _, err := ReportPathNotify(root, "", "seed.tsv", nil); err != nil {
		t.Fatal(err) // creates reports as the real user
	}
	outside := t.TempDir()
	mustLink := func(target, name string) string {
		t.Helper()
		link := filepath.Join(reports, name)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		return link
	}
	mustLink(outside, "out")
	mustLink(filepath.Join(outside, "not-yet"), "dangling-dir")
	mustLink(filepath.Join(outside, "planted.tsv"), "dangling-file.tsv")
	existing := filepath.Join(outside, "victim.tsv")
	if err := os.WriteFile(existing, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustLink(existing, "victim-link.tsv")

	cases := []struct {
		name, explicit string
	}{
		{"parent is a symlink out", filepath.Join("out", "r.tsv")},
		{"parent is a symlink out, absolute", filepath.Join(reports, "out", "r.tsv")},
		{"ancestor of a new parent is a symlink out", filepath.Join("out", "new", "sub", "r.tsv")},
		{"parent is a dangling symlink", filepath.Join("dangling-dir", "r.tsv")},
		{"ancestor of a new parent is a dangling symlink", filepath.Join("dangling-dir", "new", "r.tsv")},
		{"file is a dangling symlink out", "dangling-file.tsv"},
		{"file is a symlink to an existing file outside", "victim-link.tsv"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReportPathNotify(root, tc.explicit, "ignored.tsv", nil)
			if err == nil {
				t.Fatalf("path %q must be refused, got %q", tc.explicit, got)
			}
		})
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 1 || ents[0].Name() != "victim.tsv" {
		t.Fatalf("a refused path changed the directory it linked to: %v", ents)
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep" {
		t.Fatalf("a refused path changed the file it linked to: %q", b)
	}
}

// A NUL byte is refused by name, not left to whichever system call trips on it.
func TestReportPathNotifyExplicitNamesANULByte(t *testing.T) {
	root, _ := reportRootFor(t)
	if _, err := ReportPathNotify(root, "bad\x00.tsv", "ignored.tsv", nil); err == nil || !strings.Contains(err.Error(), "contains a NUL byte") {
		t.Fatalf("a NUL byte must be refused by name, got %v", err)
	}
}

// A symlink that stays inside the report directory is refused both as a
// directory in the path (no path with a directory in it is accepted) and as the
// file itself (the writer refuses to follow a link).
func TestReportPathNotifyExplicitSymlinkInsideReports(t *testing.T) {
	root, reports := reportRootFor(t)
	if _, err := ReportPathNotify(root, "", "seed.tsv", nil); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(reports, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(reports, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPathNotify(root, filepath.Join("alias", "r.tsv"), "ignored.tsv", nil); err == nil {
		t.Fatal("a path through a directory symlink must be refused, even one that stays inside the report directory")
	}
	if err := os.WriteFile(filepath.Join(real, "f.tsv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "f.tsv"), filepath.Join(reports, "filelink.tsv")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPathNotify(root, "filelink.tsv", "ignored.tsv", nil); err == nil {
		t.Fatal("a report file that is a symlink must be refused")
	}
}

// A root that is itself reached through a symlink (macOS keeps temp directories
// behind /var -> /private/var) must not make a path inside the report directory
// look like an escape, whichever spelling the caller uses.
func TestReportPathNotifyExplicitRootBehindSymlink(t *testing.T) {
	real, _ := reportRootFor(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	realReports := filepath.Join(real, ReportDirName)
	aliasReports := filepath.Join(alias, ReportDirName)
	for _, explicit := range []string{
		"r.tsv",
		filepath.Join(aliasReports, "r.tsv"),
		filepath.Join(realReports, "r.tsv"),
	} {
		if _, err := ReportPathNotify(alias, explicit, "ignored.tsv", nil); err != nil {
			t.Fatalf("explicit %q under a symlinked root must be accepted: %v", explicit, err)
		}
	}
	if _, err := ReportPathNotify(alias, filepath.Join(real, "Gotham", "r.tsv"), "ignored.tsv", nil); err == nil {
		t.Fatal("a path outside the report directory must still be refused behind a symlinked root")
	}
}

// With no usable root there is no report directory to confine an explicit path
// to, so it is refused rather than trusted.
func TestReportPathNotifyExplicitNeedsAnAbsoluteRoot(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "r.tsv")
	for _, root := range []string{"", "   ", "relative/library"} {
		got, err := ReportPathNotify(root, explicit, "ignored.tsv", nil)
		if err == nil || got != "" {
			t.Fatalf("root %q: an explicit path with no absolute root must be refused, got %q, %v", root, got, err)
		}
		if strings.Contains(err.Error(), "pass reportPath explicitly") {
			t.Fatalf("root %q: the refusal must not tell the caller to pass the path it just passed: %v", root, err)
		}
	}
	if _, err := os.Stat(filepath.Dir(explicit)); err != nil {
		t.Fatalf("the test directory vanished: %v", err)
	}
	if _, err := os.Stat(explicit); err == nil {
		t.Fatal("a refused path was written")
	}
}

// The explicit path may name an existing directory inside the report directory;
// the writer cannot write a file there, so it is refused up front.
func TestReportPathNotifyExplicitRefusesADirectory(t *testing.T) {
	root, reports := reportRootFor(t)
	if _, err := ReportPathNotify(root, "", "seed.tsv", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(reports, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPathNotify(root, "sub", "ignored.tsv", nil); err == nil {
		t.Fatal("an existing directory is not a report file")
	}
}

// An explicit path goes through the same report-directory checks as the default
// one: a report directory that is a symlink is refused, not followed.
func TestReportPathNotifyExplicitRefusesASymlinkedReportsDir(t *testing.T) {
	root, reports := reportRootFor(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, reports); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPathNotify(root, "r.tsv", "ignored.tsv", nil); err == nil {
		t.Fatal("a symlinked report directory must be refused")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("a refused path wrote through the symlinked report directory: %v", ents)
	}
}
