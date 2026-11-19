// file: core/library/reportfile_test.go
// version: 1.0.0
// guid: 5d0c7b0e-3f4a-4b7e-9a52-6c1e8d2f7a14
// last-edited: 2026-11-18

package library

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// reportsDirFor makes a library root with a prepared {root}/.reports (made the
// way an op makes it, by ReportPathNotify) and returns the report directory.
func reportsDirFor(t *testing.T) string {
	t.Helper()
	root, reports := reportRootFor(t)
	if _, err := ReportPathNotify(root, "", "seed.tsv", nil); err != nil {
		t.Fatal(err)
	}
	return reports
}

// victimFile writes a file outside the report directory that must survive every
// refusal, and returns its path.
func victimFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "victim.tsv")
	if err := os.WriteFile(p, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustUnchanged(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("%s was changed: %q, want %q", path, b, want)
	}
}

func mustRefuse(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if !errors.Is(err, ErrReportFileUnsafe) {
		t.Fatalf("expected ErrReportFileUnsafe, got %v", err)
	}
	if contains != "" && !strings.Contains(err.Error(), contains) {
		t.Fatalf("refusal %q does not mention %q", err, contains)
	}
}

func TestWriteReportFile_CreatesAndReplaces(t *testing.T) {
	reports := reportsDirFor(t)
	path := filepath.Join(reports, "r.tsv")
	if err := WriteReportFile(path, []byte("first, longer content\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	mustUnchanged(t, path, "first, longer content\n")
	// A shorter report replaces a longer one completely: the truncation is real.
	if err := WriteReportFile(path, []byte("two\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	mustUnchanged(t, path, "two\n")
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("report is not a regular file: %v", err)
	}
}

func TestWriteReportFile_PerFileModeAppliesUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })
	reports := reportsDirFor(t)
	path := filepath.Join(reports, "m.tsv")
	if err := WriteReportFile(path, []byte("x"), 0o664); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got != 0o644 {
		t.Fatalf("mode = %o, want 644 (0664 under umask 022)", got)
	}
}

// A symlink planted at the report's name, after the path was approved, must not
// carry the write out of the report directory. Dangling or not, to a file outside
// or inside the directory.
func TestWriteReportFile_RefusesAPlantedSymlink(t *testing.T) {
	reports := reportsDirFor(t)
	victim := victimFile(t)
	outside := filepath.Dir(victim)
	inside := filepath.Join(reports, "real.tsv")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, target string }{
		{"to a file outside", victim},
		{"dangling, outside", filepath.Join(outside, "not-yet.tsv")},
		{"to a file inside the report directory", inside},
	}
	for _, tc := range cases {
		for _, mode := range []ReportOpenMode{ReportTruncate, ReportAppend, ReportTouch} {
			t.Run(tc.name, func(t *testing.T) {
				link := filepath.Join(reports, "link.tsv")
				_ = os.Remove(link)
				if err := os.Symlink(tc.target, link); err != nil {
					t.Fatal(err)
				}
				f, err := OpenReportFile(link, FileMode, mode)
				if f != nil {
					f.Close()
				}
				mustRefuse(t, err, "symlink")
			})
		}
	}
	mustUnchanged(t, victim, "keep me")
	mustUnchanged(t, inside, "inside")
	if _, err := os.Stat(filepath.Join(outside, "not-yet.tsv")); err == nil {
		t.Fatal("a dangling symlink was followed and its target created")
	}
}

// A hard link to a file elsewhere is not a symlink, so O_NOFOLLOW lets it
// through: writing the report would overwrite that file. The file must be
// refused and, the point of opening without O_TRUNC, left with its bytes.
func TestWriteReportFile_RefusesAHardLinkAndLeavesTheFileIntact(t *testing.T) {
	reports := reportsDirFor(t)
	victim := victimFile(t)
	link := filepath.Join(reports, "hard.tsv")
	if err := os.Link(victim, link); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	for _, mode := range []ReportOpenMode{ReportTruncate, ReportAppend, ReportTouch} {
		f, err := OpenReportFile(link, FileMode, mode)
		if f != nil {
			f.Close()
		}
		mustRefuse(t, err, "hard links")
	}
	if err := WriteReportFile(link, []byte("clobber"), FileMode); err == nil {
		t.Fatal("WriteReportFile wrote through a hard link")
	}
	mustUnchanged(t, victim, "keep me")
	mustUnchanged(t, link, "keep me")
}

func TestWriteReportFile_RefusesADirectoryAtTheName(t *testing.T) {
	reports := reportsDirFor(t)
	if err := os.Mkdir(filepath.Join(reports, "d.tsv"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := WriteReportFile(filepath.Join(reports, "d.tsv"), []byte("x"), FileMode)
	if err == nil {
		t.Fatal("a directory is not a report file")
	}
	if ents, _ := os.ReadDir(filepath.Join(reports, "d.tsv")); len(ents) != 0 {
		t.Fatalf("something was written into the directory: %v", ents)
	}
}

// A FIFO at the name would block a plain open for write until a reader came, and
// writing to it is not writing a report. It is refused without hanging.
func TestWriteReportFile_RefusesAFIFOWithoutBlocking(t *testing.T) {
	reports := reportsDirFor(t)
	fifo := filepath.Join(reports, "pipe.tsv")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("no mkfifo here: %v", err)
	}
	// With a reader attached the open succeeds, and the type check is what refuses.
	rd, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	done := make(chan error, 1)
	go func() { done <- WriteReportFile(fifo, []byte("x"), FileMode) }()
	select {
	case err := <-done:
		mustRefuse(t, err, "not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO as a report blocked")
	}
}

// The ownership checks compare with the user this process is trusted to be. A
// process standing in for another user refuses a report file (and a directory)
// this one made; it works without root, unlike a real chown.
func TestOpenReportFile_RefusesAFileOrDirOwnedByAnotherUser(t *testing.T) {
	reports := reportsDirFor(t)
	path := filepath.Join(reports, "mine.tsv")
	if err := os.WriteFile(path, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := reportOpenEnv{euid: os.Geteuid() + 1}
	// The directory is checked first, so make a second process that owns the
	// directory but not the file.
	if _, err := openReportFile(path, FileMode, ReportTruncate, other); err == nil || !strings.Contains(err.Error(), "directory is not owned") {
		t.Fatalf("a directory owned by another user must be refused, got %v", err)
	}
	mustUnchanged(t, path, "mine")

	// The file check on its own: the directory passes, the file does not.
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if why := unsafeReportFile(f, os.Geteuid()+1); !strings.Contains(why, "not owned") {
		t.Fatalf("a file owned by another user must be refused, got %q", why)
	}
	if why := unsafeReportFile(f, os.Geteuid()); why != "" {
		t.Fatalf("a file this user owns was refused: %q", why)
	}
}

// With a real second owner: needs root to chown, so it is skipped otherwise.
// The euid seam above is what runs in CI.
func TestOpenReportFile_RefusesAFileChownedToAnotherUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to chown")
	}
	reports := reportsDirFor(t)
	path := filepath.Join(reports, "planted.tsv")
	if err := os.WriteFile(path, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 54321, 54321); err != nil {
		t.Skipf("cannot chown here: %v", err)
	}
	err := WriteReportFile(path, []byte("clobber"), FileMode)
	mustRefuse(t, err, "not owned")
	mustUnchanged(t, path, "theirs")
}

// Only absolute paths naming a file are written: a relative path would resolve
// against the working directory.
func TestOpenReportFile_RefusesPathsThatAreNotAbsoluteFiles(t *testing.T) {
	reports := reportsDirFor(t)
	cases := []string{"", "r.tsv", filepath.Join("sub", "r.tsv"), "/", reports + string(filepath.Separator) + "."}
	for _, p := range cases {
		f, err := OpenReportFile(p, FileMode, ReportTruncate)
		if f != nil {
			f.Close()
		}
		if err == nil {
			t.Fatalf("path %q must be refused", p)
		}
	}
	if _, err := os.Stat("r.tsv"); err == nil {
		t.Fatal("a relative report path was written in the working directory")
	}
}

// The writer does not create the directory it writes into: a missing report
// directory is an error (ReportPathNotify creates it), not a MkdirAll.
func TestOpenReportFile_DoesNotCreateTheDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".reports", "r.tsv")
	if err := WriteReportFile(path, []byte("x"), FileMode); err == nil {
		t.Fatal("a missing report directory must be an error")
	}
	if _, err := os.Stat(filepath.Join(root, ".reports")); err == nil {
		t.Fatal("the writer created the report directory")
	}
}

// A report directory that is, by the time the file is opened, a symlink out of
// the library is refused and nothing lands where it points.
func TestWriteReportFile_RefusesAReportsDirReplacedBySymlinkAfterApproval(t *testing.T) {
	root, reports := reportRootFor(t)
	path, err := ReportPathNotify(root, "", "r.tsv", nil)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Rename(reports, reports+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, reports); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, WriteReportFile(path, []byte("x"), FileMode), "symlink")
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("the report was written through the planted directory link: %v", ents)
	}
}

// The window between looking at the directory and opening it: a swap in it is
// caught because the opened handle is compared with what was looked at.
func TestOpenReportFile_RefusesAReportsDirSwappedWhileItIsBeingOpened(t *testing.T) {
	reports := reportsDirFor(t)
	path := filepath.Join(reports, "r.tsv")
	outside := t.TempDir()
	env := defaultReportOpenEnv()
	env.afterDirChecked = func(dir string) {
		if err := os.Rename(dir, dir+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, dir); err != nil {
			t.Fatal(err)
		}
	}
	f, err := openReportFile(path, FileMode, ReportTruncate, env)
	if f != nil {
		f.Close()
	}
	mustRefuse(t, err, "replaced while")
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("a swapped directory received the report: %v", ents)
	}
}

func TestOpenReportFile_AppendKeepsWhatIsThereAndNeverTruncates(t *testing.T) {
	reports := reportsDirFor(t)
	path := filepath.Join(reports, "ledger.jsonl")
	for _, line := range []string{"one\n", "two\n"} {
		f, err := OpenReportFile(path, FileMode, ReportAppend)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mustUnchanged(t, path, "one\ntwo\n")
	// A touch leaves the contents alone, and creates when absent.
	if err := PreflightReportFile(path, FileMode); err != nil {
		t.Fatal(err)
	}
	mustUnchanged(t, path, "one\ntwo\n")
	fresh := filepath.Join(reports, "fresh.tsv")
	if err := PreflightReportFile(fresh, FileMode); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(fresh); err != nil || st.Size() != 0 {
		t.Fatalf("preflight must create an empty report: %v", err)
	}
}

// The descriptor is close-on-exec, so a subprocess an op starts while the report
// is open does not inherit a handle to it.
func TestOpenReportFile_IsCloseOnExec(t *testing.T) {
	reports := reportsDirFor(t)
	f, err := OpenReportFile(filepath.Join(reports, "c.tsv"), FileMode, ReportTruncate)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETFD, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	if flags&syscall.FD_CLOEXEC == 0 {
		t.Fatal("the report descriptor is not close-on-exec")
	}
}

// ReportPathNotify refuses, up front, a report whose existing file the writer
// would refuse, so an op fails with nothing done.
func TestReportPathNotify_RefusesAnUnwritableExistingReportUpFront(t *testing.T) {
	root, reports := reportRootFor(t)
	if _, err := ReportPathNotify(root, "", "seed.tsv", nil); err != nil {
		t.Fatal(err)
	}
	victim := victimFile(t)
	if err := os.Link(victim, filepath.Join(reports, "hard.tsv")); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	if got, err := ReportPathNotify(root, "hard.tsv", "ignored.tsv", nil); err == nil {
		t.Fatalf("a report that is a hard link must be refused up front, got %q", got)
	}
	if got, err := ReportPathNotify(root, "", "hard.tsv", nil); err == nil {
		t.Fatalf("the default path must be vetted too, got %q", got)
	}
	mustUnchanged(t, victim, "keep me")
}
