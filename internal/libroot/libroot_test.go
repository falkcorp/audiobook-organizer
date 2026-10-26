// file: internal/libroot/libroot_test.go
// version: 1.6.0
// guid: 8e1c5a73-d2b4-4f69-a0e8-3b7f9d1c6a25
// last-edited: 2026-10-26

package libroot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/util"
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

	explicit := filepath.Join(t.TempDir(), "sub", "r.tsv")
	if got, err := ReportPathNotify("", explicit, "ignored.tsv", nil); err != nil || got != explicit {
		t.Fatalf("explicit path: got %q, %v", got, err)
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

// A report directory the helper creates is util.DirMode (group write kept, so a
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
	if got := info.Mode().Perm(); got != util.DirMode {
		t.Fatalf("report dir mode = %o, want %o", got, util.DirMode)
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
			if werr := os.WriteFile(path, []byte("x"), util.FileMode); werr != nil {
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
	prev := chmodDir
	chmodDir = func(string, os.FileMode) error { return errors.New("operation not permitted") }
	defer func() { chmodDir = prev }()
	called := false
	_, err := ReportPathNotify(root, "", "r.tsv", func(string, os.FileMode) { called = true })
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
	restore := SetEUIDForTest(os.Geteuid() + 1)
	defer restore()
	called := false
	_, err := ReportPathNotify(root, "", "r.tsv", func(string, os.FileMode) { called = true })
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
	restore := SetEUIDForTest(os.Geteuid() + 1)
	defer restore()
	if _, err := ReportPathNotify(root, "", "r.tsv", nil); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("a report dir owned by another user must be refused, got %v", err)
	}
}
