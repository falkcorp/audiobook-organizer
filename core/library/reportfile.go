// file: core/library/reportfile.go
// version: 1.0.0
// guid: 8339ca84-a951-46b8-8fb2-5a3b65f59b65
// last-edited: 2026-11-18

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

// ErrReportFileUnsafe is wrapped by every refusal to open a report file because
// of what is at its name (a symlink, a hard link, a directory, a file owned by
// another user) or because its report directory is not one this process can
// trust. Nothing was created, truncated or written when it is returned.
var ErrReportFileUnsafe = errors.New("report file refused")

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
// symlink, and owned by the user this process runs as, and the handle must be the
// same directory Lstat saw, so a swap in between is refused. The file is then
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
// without directory handles this refuses every path rather than write unchecked.
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
func openReportFile(path string, perm fs.FileMode, mode ReportOpenMode, env reportOpenEnv) (*os.File, error) {
	refuse := func(why string) error {
		return fmt.Errorf("%w: %s: %s", ErrReportFileUnsafe, path, why)
	}
	dir, name, err := splitReportPath(path)
	if err != nil {
		return nil, refuse(err.Error())
	}
	d, closeDir, err := openReportDir(dir, env)
	if err != nil {
		return nil, refuse(err.Error())
	}
	defer closeDir()

	flag := os.O_WRONLY | os.O_CREATE
	if mode == ReportAppend {
		flag |= os.O_APPEND
	}
	f, err := libfs.OpenFileAt(d, name, flag, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, refuse("the report file is a symlink")
		}
		return nil, fmt.Errorf("open report %s: %w", path, err)
	}
	if why := unsafeReportFile(f, env.euid); why != "" {
		return nil, libfs.CloseAfter(f, refuse(why))
	}
	if mode == ReportTruncate {
		if err := f.Truncate(0); err != nil {
			return nil, libfs.CloseAfter(f, fmt.Errorf("truncate report %s: %w", path, err))
		}
	}
	return f, nil
}

// checkExistingReportFile is the vetting OpenReportFile does, for a report that
// may or may not exist yet, without creating or changing anything. A missing file
// is fine. It lets ReportPathNotify refuse a report that could not be written
// before an op has done any work.
func checkExistingReportFile(path string, env reportOpenEnv) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: %s: %s", ErrReportFileUnsafe, path, why)
	}
	dir, name, err := splitReportPath(path)
	if err != nil {
		return refuse(err.Error())
	}
	d, closeDir, err := openReportDir(dir, env)
	if err != nil {
		return refuse(err.Error())
	}
	defer closeDir()
	f, err := libfs.OpenAt(d, name, os.O_RDONLY)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, syscall.ELOOP):
		return refuse("the report file is a symlink")
	case err != nil:
		return fmt.Errorf("inspect report %s: %w", path, err)
	}
	why := unsafeReportFile(f, env.euid)
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
		return "", "", errors.New("the report path is not absolute")
	}
	clean := filepath.Clean(path)
	dir, name = filepath.Dir(clean), filepath.Base(clean)
	if clean != filepath.Join(dir, name) || name == "." || name == ".." || name == string(filepath.Separator) {
		return "", "", errors.New("the report path does not name a file")
	}
	return dir, name, nil
}

// openReportDir opens the report directory as a descriptor and returns it with
// the function that releases it. See OpenReportFile for what it checks.
func openReportDir(dir string, env reportOpenEnv) (*libfs.Dir, func(), error) {
	before, err := os.Lstat(dir)
	switch {
	case err != nil:
		return nil, nil, fmt.Errorf("the report directory cannot be inspected: %w", err)
	case before.Mode()&os.ModeSymlink != 0:
		return nil, nil, errors.New("the report directory is a symlink, not a directory")
	case !before.IsDir():
		return nil, nil, errors.New("the report directory is not a directory")
	case !ownedByUID(before, env.euid):
		return nil, nil, errors.New("the report directory is not owned by the user this process runs as")
	}
	if env.afterDirChecked != nil {
		env.afterDirChecked(dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("the report directory cannot be opened: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(before, opened) {
		return nil, nil, errors.Join(errors.New("the report directory was replaced while it was being opened"), err, root.Close())
	}
	d, err := libfs.OpenDir(root, ".")
	if cerr := root.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return nil, nil, errors.Join(fmt.Errorf("the report directory cannot be opened: %w", err), d.Close())
	}
	return d, func() { _ = d.Close() }, nil
}

// unsafeReportFile says why the opened file may not be written as a report, or ""
// when it may. It reads the descriptor, so it describes the file that was opened,
// not whatever the name points at now.
func unsafeReportFile(f *os.File, euid int) string {
	info, err := f.Stat()
	switch {
	case err != nil:
		return "the report file cannot be inspected: " + err.Error()
	case !info.Mode().IsRegular():
		return "the report file is not a regular file"
	case !ownedByUID(info, euid):
		return "the report file is not owned by the user this process runs as"
	}
	if n, ok := linkCount(info); ok && n != 1 {
		return fmt.Sprintf("the report file has %d hard links; it may be another file's name", n)
	}
	return ""
}
