// file: core/library/reportfile.go
// version: 1.1.0
// guid: 8339ca84-a951-46b8-8fb2-5a3b65f59b65
// last-edited: 2026-11-19

package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// ErrReportFileUnsafe is what every refusal to open a report file (or a ledger
// file: they are opened the same way) matches under errors.Is, because of what is
// at its name (a symlink, a hard link, a directory, a file owned by another user)
// or because its directory is not one this process can trust. Nothing was
// created, truncated or written when it is returned. The refusal's own text says
// "report" or "ledger" by the directory the file is in; see unsafeFileError.
var ErrReportFileUnsafe = errors.New("report file refused")

// unsafeFileError is one refusal. It matches ErrReportFileUnsafe, and its text
// names what was refused as a report or a ledger, so an operator reading a
// refusal about the provenance ledger is not told about a report.
type unsafeFileError struct {
	noun string // "report" or "ledger"
	path string
	why  string
}

func (e *unsafeFileError) Error() string {
	return fmt.Sprintf("%s file refused: %s: %s", e.noun, e.path, e.why)
}

// Is makes the refusal match the package's sentinel, which is what callers test.
func (e *unsafeFileError) Is(target error) bool { return target == ErrReportFileUnsafe }

// ReportOpenMode says what OpenReportFile does with a report file that already
// exists. None of the modes follows a symlink, and none changes an existing file
// until it has been checked.
type ReportOpenMode int

const (
	// ReportTruncate creates the report or empties an existing one. The
	// truncation happens AFTER the file has been vetted: opening with O_TRUNC
	// would empty a hard link to somebody else's file before the check could
	// refuse it.
	ReportTruncate ReportOpenMode = iota
	// ReportAppend creates the report if it is absent and positions every write
	// at the end (O_APPEND). It never truncates.
	ReportAppend
	// ReportTouch creates the report if it is absent and otherwise leaves it as
	// it is. It proves a later ReportTruncate or ReportAppend can open the file.
	ReportTouch
	// ReportReadAppend creates the report if it is absent and opens it for reading
	// and writing at positions the caller chooses (O_RDWR, no O_APPEND), without
	// truncating it. It is for a job that reads its own earlier report back, cuts
	// a half-written last line off and carries on writing (backfill-file-hashes'
	// resume). It is vetted exactly like the other modes.
	ReportReadAppend
	// ReportAppendAndRead is ReportAppend that can also be read (O_RDWR|O_APPEND):
	// every write goes at the end, whatever was read or seeked to, and the file is
	// never truncated. It is for an append-only file whose end the writer checks
	// before it appends (the provenance ledger export), where ReportReadAppend's
	// positioned writes would be a way to overwrite what is there.
	ReportAppendAndRead
)

// WriteReportFile writes data as the report at path and closes it. path must be
// a report path from ReportPathNotify (or the sibling of one: the same directory,
// a different bare file name). The file is created if absent and replaced if it
// exists; see OpenReportFile for what is refused.
func WriteReportFile(path string, data []byte, perm fs.FileMode) error {
	f, err := OpenReportFile(path, perm, ReportTruncate)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// PreflightReportFile proves the report at path can be opened for writing,
// creating it empty when it is absent, so an op can refuse before it does any
// work rather than after.
func PreflightReportFile(path string, perm fs.FileMode) error {
	f, err := OpenReportFile(path, perm, ReportTouch)
	if err != nil {
		return err
	}
	return f.Close()
}

// OpenReportFile opens the report file at path for writing and returns it. It is
// the write-time half of the report-path confinement: ReportPathNotify decides
// where a report may go, and this refuses to write it anywhere that has been made
// unsafe since.
//
// The directory is opened as a handle (an os.Root, then a directory descriptor),
// after it has been looked at with Lstat: it must be a real directory, not a
// symlink, and owned by the user this process runs as. The handle is then compared
// with what Lstat saw (os.SameFile), which catches a swap of the directory for
// something else between the two calls. It does not prove anything about the
// directory beyond that: the checks above are what say it is a directory this user
// owns, and the file checks below are what keep a link planted in it from
// mattering. The file is then
// opened relative to that descriptor with openat, O_NOFOLLOW and O_CLOEXEC, so
// the kernel never resolves a path again and a link planted at the name is not
// followed. What was opened is checked through its descriptor: it must be a
// regular file, owned by this user, with a link count of one, so a hard link to
// another file (which O_NOFOLLOW does not catch, and which would make the report
// overwrite that file) and a file another user planted are refused. Only then is
// an existing file truncated, in ReportTruncate mode.
//
// path must be absolute and name a file directly in its directory; the directory
// is never created here (ReportPathNotify creates and checks it). On platforms
// without directory handles (anything but unix) every call fails, with an error
// that is NOT ErrReportFileUnsafe (it comes from the missing capability, not from
// what is at the name), rather than write unchecked; the same is true of the
// vetting ReportPathNotify does, so on those platforms no report path resolves at
// all. The maintenance reports are a unix feature.
func OpenReportFile(path string, perm fs.FileMode, mode ReportOpenMode) (*os.File, error) {
	return openReportFile(path, perm, mode, defaultReportOpenEnv())
}

// reportOpenEnv is the process state OpenReportFile reads, passed down so a test
// can stand in for another user, or act inside the window between two checks,
// without mutable global state (the same shape as reportEnv).
type reportOpenEnv struct {
	// euid is the user ID this process is trusted to be: what the directory and
	// the file must be owned by.
	euid int
	// afterDirChecked, when not nil, runs after the report directory passed its
	// Lstat checks and before it is opened. It is the window another process
	// would use to swap the directory for a link, so a test can use it to do so.
	afterDirChecked func(dir string)
}

// defaultReportOpenEnv returns the real process environment.
func defaultReportOpenEnv() reportOpenEnv { return reportOpenEnv{euid: os.Geteuid()} }

// openReportFile is OpenReportFile over an explicit environment.
func openReportFile(path string, perm fs.FileMode, mode ReportOpenMode, env reportOpenEnv) (f *os.File, err error) {
	kind := kindOfDir(filepath.Dir(filepath.Clean(path)))
	refuse := func(why string) error {
		return &unsafeFileError{noun: kind.noun, path: path, why: why}
	}
	dir, name, err := splitReportPath(path)
	if err != nil {
		return nil, refuse(err.Error())
	}
	d, err := openReportDir(kind, dir, env)
	if err != nil {
		return nil, refuse(err.Error())
	}
	// The directory handle is only the anchor for the openat below. A failure
	// to release it still fails the call, and the file opened under it is
	// closed rather than handed back.
	defer func() {
		if cerr := d.Close(); cerr != nil && err == nil {
			err = libfs.CloseAfter(f, fmt.Errorf("close %s directory %s: %w", kind.noun, dir, cerr))
			f = nil
		}
	}()

	flag := os.O_WRONLY | os.O_CREATE
	switch mode {
	case ReportAppend:
		flag |= os.O_APPEND
	case ReportReadAppend:
		flag = os.O_RDWR | os.O_CREATE
	case ReportAppendAndRead:
		flag = os.O_RDWR | os.O_CREATE | os.O_APPEND
	}
	f, err = libfs.OpenFileAt(d, name, flag, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, refuse("the " + kind.noun + " file is a symlink")
		}
		return nil, fmt.Errorf("open %s %s: %w", kind.noun, path, err)
	}
	if why := unsafeReportFile(kind, f, env.euid); why != "" {
		return nil, libfs.CloseAfter(f, refuse(why))
	}
	if mode == ReportTruncate {
		if err := f.Truncate(0); err != nil {
			return nil, libfs.CloseAfter(f, fmt.Errorf("truncate %s %s: %w", kind.noun, path, err))
		}
	}
	return f, nil
}

// checkExistingReportFile is the vetting OpenReportFile does, for a report that
// may or may not exist yet, without creating or changing anything. A missing file
// is fine. It lets ReportPathNotify refuse a report that could not be written
// before an op has done any work.
func checkExistingReportFile(kind confinedDir, path string, env reportOpenEnv) (err error) {
	refuse := func(why string) error {
		return &unsafeFileError{noun: kind.noun, path: path, why: why}
	}
	dir, name, err := splitReportPath(path)
	if err != nil {
		return refuse(err.Error())
	}
	d, err := openReportDir(kind, dir, env)
	if err != nil {
		return refuse(err.Error())
	}
	defer func() {
		if cerr := d.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s directory %s: %w", kind.noun, dir, cerr)
		}
	}()
	f, err := libfs.OpenAt(d, name, os.O_RDONLY)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, syscall.ELOOP):
		return refuse("the " + kind.noun + " file is a symlink")
	case err != nil:
		return fmt.Errorf("inspect %s %s: %w", kind.noun, path, err)
	}
	why := unsafeReportFile(kind, f, env.euid)
	if cerr := f.Close(); why == "" && cerr != nil {
		return cerr
	}
	if why != "" {
		return refuse(why)
	}
	return nil
}

// splitReportPath splits an absolute report path into its directory and its bare
// file name.
func splitReportPath(path string) (dir, name string, err error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", "", errors.New("the path is not absolute")
	}
	clean := filepath.Clean(path)
	dir, name = filepath.Dir(clean), filepath.Base(clean)
	if clean != filepath.Join(dir, name) || name == "." || name == ".." || name == string(filepath.Separator) {
		return "", "", errors.New("the path does not name a file")
	}
	return dir, name, nil
}

// openReportDir opens the report (or ledger) directory as a descriptor and
// returns it; the caller closes it. See OpenReportFile for what it checks.
func openReportDir(kind confinedDir, dir string, env reportOpenEnv) (*libfs.Dir, error) {
	before, err := os.Lstat(dir)
	switch {
	case err != nil:
		return nil, fmt.Errorf("the %s directory cannot be inspected: %w", kind.noun, err)
	case before.Mode()&os.ModeSymlink != 0:
		return nil, fmt.Errorf("the %s directory is a symlink, not a directory", kind.noun)
	case !before.IsDir():
		return nil, fmt.Errorf("the %s directory is not a directory", kind.noun)
	case !ownedByUID(before, env.euid):
		return nil, fmt.Errorf("the %s directory is not owned by the user this process runs as", kind.noun)
	}
	if env.afterDirChecked != nil {
		env.afterDirChecked(dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("the %s directory cannot be opened: %w", kind.noun, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.Join(fmt.Errorf("the %s directory was replaced while it was being opened", kind.noun), err, root.Close())
	}
	d, err := libfs.OpenDir(root, ".")
	if cerr := root.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("the %s directory cannot be opened: %w", kind.noun, err), d.Close())
	}
	return d, nil
}

// unsafeReportFile says why the opened file may not be written as a report (or
// ledger, by kind), or "" when it may. It reads the descriptor, so it describes the
// file that was opened, not whatever the name points at now.
func unsafeReportFile(kind confinedDir, f *os.File, euid int) string {
	info, err := f.Stat()
	switch {
	case err != nil:
		return "the " + kind.noun + " file cannot be inspected: " + err.Error()
	case !info.Mode().IsRegular():
		return "the " + kind.noun + " file is not a regular file"
	case !ownedByUID(info, euid):
		return "the " + kind.noun + " file is not owned by the user this process runs as"
	}
	if n, ok := linkCount(info); ok && n != 1 {
		return fmt.Sprintf("the %s file has %d hard links; it may be another file's name", kind.noun, n)
	}
	return ""
}
