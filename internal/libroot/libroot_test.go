// file: internal/libroot/libroot_test.go
// version: 1.5.0
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
	if _, err := ReportPath(root, "", "x.tsv"); err != nil {
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

func TestReportPath(t *testing.T) {
	root := t.TempDir()
	got, err := ReportPath(root, "", "backfill-file-hashes-op1.tsv")
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
	if got, err := ReportPath("", explicit, "ignored.tsv"); err != nil || got != explicit {
		t.Fatalf("explicit path: got %q, %v", got, err)
	}

	if _, err := ReportPath("", "", "r.tsv"); err == nil {
		t.Fatal("no root and no explicit path must fail")
	}
	if _, err := ReportPath("relative/root", "", "r.tsv"); err == nil {
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
	if _, err := ReportPath(root, "", "r.tsv"); err == nil {
		t.Fatal("a read-only report dir must be refused before any work")
	}
}

// A report directory the helper creates has no group or world write, whatever
// the umask: the directory is inside the library share and others may write it.
func TestReportPathCreatesTheDirWithoutGroupOrWorldWrite(t *testing.T) {
	old := syscall.Umask(0) // so only the requested mode decides
	t.Cleanup(func() { syscall.Umask(old) })
	root := t.TempDir()
	if _, err := ReportPath(root, "", "r.tsv"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, ReportDirName))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != ReportDirPerm {
		t.Fatalf("report dir mode = %o, want %o", got, ReportDirPerm)
	}
	if info.Mode().Perm()&0o027 != 0 {
		t.Fatalf("report dir mode %o gives group write or world access", info.Mode().Perm())
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
	_, err := ReportPath(root, "", "r.tsv")
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
	if _, err := ReportPath(root, "", "r.tsv"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("a regular file where the report dir should be must be refused as such, got %v", err)
	}
}

// An existing report dir this user owns that group or others can write (an older
// build made it 0775) is tightened to 0750, reported through the callback with
// the mode it had, and the write then proceeds. 0750, 0755 and 0700 are left
// alone and not reported.
func TestReportPathTightensAGroupOrWorldWritableReportsDir(t *testing.T) {
	for _, tc := range []struct {
		mode      os.FileMode
		tightened bool
	}{{0o750, false}, {0o755, false}, {0o700, false}, {0o775, true}, {0o757, true}, {0o777, true}, {0o770, true}, {0o702, true}} {
		root := t.TempDir()
		dir := filepath.Join(root, ReportDirName)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, tc.mode); err != nil { // Chmod is not subject to the umask
			t.Fatal(err)
		}
		var calls []os.FileMode
		var gotDir string
		path, err := ReportPathNotify(root, "", "r.tsv", func(d string, was os.FileMode) {
			gotDir = d
			calls = append(calls, was)
		})
		if err != nil {
			t.Fatalf("mode %o: an owned dir must be accepted, got %v", tc.mode, err)
		}
		info, serr := os.Stat(dir)
		if serr != nil {
			t.Fatal(serr)
		}
		want := tc.mode
		if tc.tightened {
			want = ReportDirPerm
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode %o: dir ended up %o, want %o", tc.mode, info.Mode().Perm(), want)
		}
		if tc.tightened {
			if len(calls) != 1 || calls[0] != tc.mode || gotDir != dir {
				t.Fatalf("mode %o: callback got %v for %q, want exactly one call with the old mode for %q", tc.mode, calls, gotDir, dir)
			}
		} else if len(calls) != 0 {
			t.Fatalf("mode %o: nothing was tightened but the callback ran %v", tc.mode, calls)
		}
		// The write goes ahead: a report file can be created where the path points.
		if werr := os.WriteFile(path, []byte("x"), 0o640); werr != nil {
			t.Fatalf("mode %o: writing the report after the check failed: %v", tc.mode, werr)
		}
	}
}

// ReportPath, which takes no callback, tightens the same way without panicking
// on the absent notification.
func TestReportPathTightensWithoutACallback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ReportDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPath(root, "", "r.tsv"); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != ReportDirPerm {
		t.Fatalf("dir mode %o, want %o", info.Mode().Perm(), ReportDirPerm)
	}
}

// When the chmod fails the dir is refused, naming the mode and the fix target,
// and the callback is not told it was tightened.
func TestReportPathRefusesWhenTheDirCannotBeTightened(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ReportDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o775); err != nil {
		t.Fatal(err)
	}
	prev := chmodDir
	chmodDir = func(string, os.FileMode) error { return errors.New("operation not permitted") }
	defer func() { chmodDir = prev }()
	called := false
	_, err := ReportPathNotify(root, "", "r.tsv", func(string, os.FileMode) { called = true })
	if err == nil || !strings.Contains(err.Error(), "could not be tightened") || !strings.Contains(err.Error(), "775") {
		t.Fatalf("a dir that cannot be tightened must be refused, got %v", err)
	}
	if called {
		t.Fatal("the callback must not run when the chmod failed")
	}
}

// An explicit report path keeps its old behaviour: its directory is created and
// probed, not vetted, because it is the caller's own choice.
func TestReportPathExplicitPathDirIsNotVetted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mine")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPath("", filepath.Join(dir, "r.tsv"), "ignored"); err != nil {
		t.Fatalf("an explicit path's dir must still be accepted: %v", err)
	}
}

type fakeOwnerInfo struct {
	os.FileInfo
	sys any
}

func (i fakeOwnerInfo) Sys() any { return i.sys }

func TestOwnedByCurrentUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !OwnedByCurrentUser(info) {
		t.Fatal("a file this process made must be owned by it")
	}
	if OwnedByCurrentUser(fakeOwnerInfo{info, nil}) {
		t.Fatal("an unreadable owner must not count as owned")
	}
}

// A report dir owned by someone else is refused. Changing an owner needs root,
// so this runs only there; the ownership test itself is TestOwnedByCurrentUser.
func TestReportPathRefusesAReportsDirOwnedBySomeoneElse(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chown to another user needs root")
	}
	root := t.TempDir()
	dir := filepath.Join(root, ReportDirName)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, os.Geteuid()+1, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := ReportPath(root, "", "r.tsv"); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("a report dir owned by another user must be refused, got %v", err)
	}
}

// A group-writable dir that is not ours is refused and left as it was: the
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
	if _, err := ReportPath(root, "", "r.tsv"); err != nil {
		t.Fatal(err) // creates the dir as the real user
	}
	restore := SetEUIDForTest(os.Geteuid() + 1)
	defer restore()
	if _, err := ReportPath(root, "", "r.tsv"); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("a report dir owned by another user must be refused, got %v", err)
	}
}
