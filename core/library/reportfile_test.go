//go:build unix

// file: core/library/reportfile_test.go
// version: 1.2.0
// guid: 5d0c7b0e-3f4a-4b7e-9a52-6c1e8d2f7a14
// last-edited: 2026-11-19

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

// allReportModes is every ReportOpenMode: each one must vet the file the same way.
var allReportModes = []ReportOpenMode{ReportTruncate, ReportAppend, ReportTouch, ReportReadAppend}

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
		for _, mode := range allReportModes {
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
	for _, mode := range allReportModes {
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
	if why := unsafeReportFile(reportDir(), f, os.Geteuid()+1); !strings.Contains(why, "not owned") {
		t.Fatalf("a file owned by another user must be refused, got %q", why)
	}
	if why := unsafeReportFile(reportDir(), f, os.Geteuid()); why != "" {
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

// ReportReadAppend opens for reading and writing at chosen positions, never
// truncates, and creates when absent: what a job needs to read its earlier report
// back, cut off a half-written last line and carry on.
func TestOpenReportFile_ReadAppendReadsSeeksAndNeverTruncates(t *testing.T) {
	reports := reportsDirFor(t)
	path := filepath.Join(reports, "resume.tsv")
	if err := os.WriteFile(path, []byte("row1\nrow2\npartial"), FileMode); err != nil {
		t.Fatal(err)
	}
	f, err := OpenReportFile(path, FileMode, ReportReadAppend)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := f.Read(buf)
	if err != nil || string(buf[:n]) != "row1\nrow2\npartial" {
		t.Fatalf("could not read the earlier report back: %q %v", buf[:n], err)
	}
	if err := f.Truncate(10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(10, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("row3\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	mustUnchanged(t, path, "row1\nrow2\nrow3\n")

	// It is not an append-mode descriptor: a positioned write over earlier bytes
	// works (O_APPEND would make WriteAt fail, and a Seek would be ignored).
	w, err := OpenReportFile(path, FileMode, ReportReadAppend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteAt([]byte("ROW1"), 0); err != nil {
		t.Fatalf("ReportReadAppend must allow a positioned write: %v", err)
	}
	w.Close()
	mustUnchanged(t, path, "ROW1\nrow2\nrow3\n")
	if err := os.WriteFile(path, []byte("row1\nrow2\nrow3\n"), FileMode); err != nil {
		t.Fatal(err)
	}

	// Opening again does not empty it, and a fresh name is created empty.
	g, err := OpenReportFile(path, FileMode, ReportReadAppend)
	if err != nil {
		t.Fatal(err)
	}
	g.Close()
	mustUnchanged(t, path, "row1\nrow2\nrow3\n")
	fresh := filepath.Join(reports, "fresh-resume.tsv")
	h, err := OpenReportFile(fresh, FileMode, ReportReadAppend)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	if st, err := os.Stat(fresh); err != nil || st.Size() != 0 || !st.Mode().IsRegular() {
		t.Fatalf("a missing report must be created empty: %v", err)
	}
}

// ReportReadAppend is vetted like the others: a hard link to another file is
// refused with that file's bytes intact (O_RDWR, so a vetting after a truncating
// open would be too late), and a directory at the name is refused.
func TestOpenReportFile_ReadAppendRefusesALinkAndADirectory(t *testing.T) {
	reports := reportsDirFor(t)
	victim := victimFile(t)
	hard := filepath.Join(reports, "hard.tsv")
	if err := os.Link(victim, hard); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	f, err := OpenReportFile(hard, FileMode, ReportReadAppend)
	if f != nil {
		f.Close()
	}
	mustRefuse(t, err, "hard links")
	sym := filepath.Join(reports, "sym.tsv")
	if err := os.Symlink(victim, sym); err != nil {
		t.Fatal(err)
	}
	f, err = OpenReportFile(sym, FileMode, ReportReadAppend)
	if f != nil {
		f.Close()
	}
	mustRefuse(t, err, "symlink")
	dir := filepath.Join(reports, "adir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err = OpenReportFile(dir, FileMode, ReportReadAppend)
	if f != nil {
		f.Close()
	}
	if err == nil {
		t.Fatal("a directory at the report name was opened")
	}
	mustUnchanged(t, victim, "keep me")
}

// The ledger has a directory of its own. A path in .reports is refused as a
// ledger path, a path in .ledger is refused as a report path, and a bare name goes
// to the right directory, so a report cannot be pointed at a ledger by any name.
func TestLedgerPathNotify_IsSeparateFromReports(t *testing.T) {
	root, reports := reportRootFor(t)
	ledger := filepath.Join(root, LedgerDirName)

	got, err := LedgerPathNotify(root, "ledger.jsonl", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(ledger, "ledger.jsonl") {
		t.Fatalf("ledger path = %q", got)
	}
	if st, err := os.Stat(ledger); err != nil || !st.IsDir() {
		t.Fatalf("the ledger directory was not created: %v", err)
	}
	if _, err := os.Stat(reports); err == nil {
		t.Fatal("resolving a ledger path created the report directory")
	}

	// Each directory refuses the other's files, spelled any way.
	if _, err := ReportPathNotify(root, "x", "x", nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(reports, "ledger.jsonl"),
		filepath.Join("..", LedgerDirName, "ledger.jsonl"),
		filepath.Join(root, "ledger.jsonl"),
	} {
		if g, err := LedgerPathNotify(root, p, "", nil); err == nil {
			t.Fatalf("ledger path %q was accepted as %q", p, g)
		}
	}
	for _, p := range []string{
		filepath.Join(ledger, "ledger.jsonl"),
		filepath.Join("..", LedgerDirName, "ledger.jsonl"),
	} {
		if g, err := ReportPathNotify(root, p, "", nil); err == nil {
			t.Fatalf("report path %q was accepted as %q", p, g)
		}
	}

	// A ledger written, then a report named like it: the ledger is untouched.
	if err := WriteReportFile(got, []byte("history\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	rp, err := ReportPathNotify(root, "ledger.jsonl", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteReportFile(rp, []byte("report\n"), FileMode); err != nil {
		t.Fatal(err)
	}
	mustUnchanged(t, got, "history\n")

	// The ledger directory does not count as library content, so it cannot make a
	// bare mountpoint look mounted.
	bare := t.TempDir()
	if _, err := LedgerPathNotify(bare, "l.jsonl", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := Mounted(bare); err == nil {
		t.Fatal("a ledger directory made an empty root look mounted")
	}
}

// A refusal about a ledger says ledger, not report.
func TestLedgerPathNotify_RefusalsNameTheLedger(t *testing.T) {
	root, _ := reportRootFor(t)
	_, err := LedgerPathNotify(root, filepath.Join("a", "b.jsonl"), "", nil)
	if err == nil || !strings.Contains(err.Error(), "ledger") || strings.Contains(err.Error(), "report") {
		t.Fatalf("refusal should speak of the ledger: %v", err)
	}
	if _, err := LedgerPathNotify("", "l.jsonl", "", nil); err == nil || !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("no-root refusal should speak of the ledger: %v", err)
	}
}

// The default file name is joined onto the directory, so a name that is a path
// would walk out of it. Only a single plain file name is accepted.
func TestReportPathNotify_RefusesADefaultNameThatIsNotAFileName(t *testing.T) {
	root, _ := reportRootFor(t)
	outside := filepath.Join(filepath.Dir(root), "escaped.tsv")
	for _, name := range []string{
		"", ".", "..", "../escaped.tsv", "a/b.tsv", "/abs.tsv", "a" + string(filepath.Separator) + "b",
		"nul\x00.tsv", "../../" + filepath.Base(root) + "/x.tsv",
	} {
		// The refusal must come from the file-name check itself, not from whatever
		// the later existing-file vet happens to say about a path that does not exist.
		if got, err := ReportPathNotify(root, "", name, nil); err == nil {
			t.Errorf("default name %q was accepted as %q", name, got)
		} else if !strings.Contains(err.Error(), "not a single file name") {
			t.Errorf("default name %q was refused for another reason: %v", name, err)
		}
		if got, err := LedgerPathNotify(root, "", name, nil); err == nil {
			t.Errorf("default ledger name %q was accepted as %q", name, got)
		} else if !strings.Contains(err.Error(), "not a single file name") {
			t.Errorf("default ledger name %q was refused for another reason: %v", name, err)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("a default name escaped the report directory")
	}
	if got, err := ReportPathNotify(root, "", "plain-1.tsv", nil); err != nil || filepath.Base(got) != "plain-1.tsv" {
		t.Fatalf("a plain default name must still work: %q %v", got, err)
	}
}

// A refusal speaks of what was refused. A file in the ledger directory is a ledger,
// so the provenance export is not told a report was refused; a file in the report
// directory is a report. Every refusal still matches ErrReportFileUnsafe.
func TestOpenReportFile_RefusalsNameTheKindOfFile(t *testing.T) {
	for _, tc := range []struct {
		noun, other, dir string
		notify           func(root string) error
	}{
		{"report", "ledger", ReportDirName, func(root string) error { _, err := ReportPathNotify(root, "", "seed.tsv", nil); return err }},
		{"ledger", "report", LedgerDirName, func(root string) error { _, err := LedgerPathNotify(root, "", "seed.jsonl", nil); return err }},
	} {
		t.Run(tc.noun, func(t *testing.T) {
			root, _ := reportRootFor(t)
			if err := tc.notify(root); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, tc.dir)
			victim := victimFile(t)
			if err := os.Symlink(victim, filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(victim, filepath.Join(dir, "hard")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
				t.Fatal(err)
			}
			want := func(err error, why string) {
				t.Helper()
				if !errors.Is(err, ErrReportFileUnsafe) {
					t.Fatalf("%s: not ErrReportFileUnsafe: %v", why, err)
				}
				msg := err.Error()
				if !strings.Contains(msg, tc.noun+" ") || strings.Contains(msg, tc.other) {
					t.Fatalf("%s: a refusal about a %s should say %s, and not %s: %v", why, tc.noun, tc.noun, tc.other, err)
				}
			}
			for _, mode := range allReportModes {
				_, err := OpenReportFile(filepath.Join(dir, "link"), 0o644, mode)
				want(err, "symlink")
				_, err = OpenReportFile(filepath.Join(dir, "hard"), 0o644, mode)
				want(err, "hard link")
				_, err = OpenReportFile(filepath.Join(dir, "adir"), 0o644, mode)
				if err == nil {
					t.Fatal("a directory at the name was opened")
				}
			}
			_, err := OpenReportFile(filepath.Join(t.TempDir(), tc.dir, "x"), 0o644, ReportTruncate)
			want(err, "missing directory")
			mustUnchanged(t, victim, "keep me")
			// The path-resolution stage speaks the same way.
			explicit := "link"
			var err2 error
			if tc.noun == "report" {
				_, err2 = ReportPathNotify(root, explicit, "", nil)
			} else {
				_, err2 = LedgerPathNotify(root, explicit, "", nil)
			}
			want(err2, "resolving a symlink")
		})
	}
}
