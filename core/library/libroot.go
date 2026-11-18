// file: core/library/libroot.go
// version: 1.7.1
// guid: 4d8b2f61-a3c7-4e90-b512-9e6f0c1d7a38
// last-edited: 2026-11-18

// This file holds the two checks every library-wide maintenance op makes
// against the library root before it trusts what it finds there: that the root
// is really mounted, and that a per-row report can be written under it. It
// imports only the standard library and the permission constants in perms.go,
// so the maintenance plugin and the legacy maintenance jobs share one copy
// instead of each keeping their own.

package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Mounted refuses to proceed when the library root is unset, unreadable or
// holds no library content. An unmounted share (a ZFS dataset, an NFS or autofs
// mount) leaves an empty directory behind, so every path under it answers
// ENOENT; an op that reads "not found" as "the file is gone" would then
// conclude the whole library is gone. Call it before acting on any not-found,
// not only before a write.
//
// Dot-prefixed entries do not count as content. They are app- or OS-owned
// state (the ReportDirName directory, the default {root}/.wav-cache, a
// .DS_Store), and a report directory created on an unmounted root would
// otherwise make that mountpoint read as populated on every later run.
//
// This is a coarse guard, not proof of a healthy mount. Some app-created
// directories under the root are not dot-prefixed (a cover apply makes
// {root}/covers/..., the default OpenLibraryDumpDir is {root}/openlibrary-dumps,
// and config.yaml falls back to {root}), so one of those created while the share
// was unmounted makes the bare mountpoint pass, and a nested dataset that failed
// to mount is an empty directory under a root that passes. A caller that acts on
// a not-found must treat this as one safeguard among others, never as the only
// one.
func Mounted(root string) error {
	if root == "" {
		return errors.New("library root is not configured")
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("library root %s unreadable: %w", root, err)
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") {
			return nil
		}
	}
	return fmt.Errorf("library root %s has no library content (share not mounted?)", root)
}

// ReportDirName is the directory, under the library root, where a maintenance
// op writes its per-row TSV report when the caller did not pass an explicit
// path. It is app-owned state on the large library volume, dot-prefixed so
// directory walkers that call pathutil.ShouldSkipDir stay out of it. A relative
// default is deliberately not offered: it would resolve against the process
// working directory, which in production is the credentials state dir and
// under `go test` is the package source directory.
const ReportDirName = ".reports"

// ReportPathNotify returns where an op writes its report: the caller's explicit
// path when one was given, otherwise {root}/.reports/<fileName>. It proves the
// path's directory exists and is writable before returning, so an op can call
// it BEFORE doing any work and fail with nothing done rather than apply and
// then lose its record. It returns an error, rather than guessing, when root is
// unset or not absolute: with no default there is nowhere to write, and with an
// explicit path there is no report directory to confine it to.
//
// An explicit path is never trusted as given. It must end up inside
// {root}/.reports: a relative path is taken relative to that directory, and an
// absolute one must already lie inside it. ".." segments and symlinks (in the
// path's directories, or as the file itself) that would carry the report
// elsewhere are refused, and nothing is created outside the report directory
// when one is. The report directory itself is checked and prepared exactly as
// for the default path (see ensureReportsDir). The checks run against the
// filesystem as it is when this is called; a later write that does not itself
// refuse symlinks can still be redirected by a link planted afterwards, which
// is why the report directory must be one only this user can write.
//
// onTightened, when not nil, is called when the report directory already
// existed with world write and that bit was removed: it gets the directory and
// the permission bits it had. This package imports no module package, so it
// cannot log; the caller does.
func ReportPathNotify(root, explicit, fileName string, onTightened func(dir string, was fs.FileMode)) (string, error) {
	return reportPathNotify(root, explicit, fileName, onTightened, defaultReportEnv())
}

// reportEnv is the process state ReportPathNotify reads: how a directory's mode
// is changed and which user ID owns what this process may trust. It is passed
// down rather than held in package variables so a test can stand in for another
// user, or a failing chmod, without mutable global state.
type reportEnv struct {
	chmod func(name string, mode os.FileMode) error
	euid  func() int
	// afterCreate, when not nil, runs after an explicit report path's directory
	// was created and before that directory is checked again. It is the window a
	// link planted by another process would use, so a test can use it to plant one.
	afterCreate func(parent string)
}

// defaultReportEnv returns the real process environment.
func defaultReportEnv() reportEnv {
	return reportEnv{chmod: os.Chmod, euid: os.Geteuid}
}

func reportPathNotify(root, explicit, fileName string, onTightened func(dir string, was fs.FileMode), env reportEnv) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		if explicit != "" {
			return "", fmt.Errorf("report path %q refused: root_dir is not configured, so there is no "+
				"report directory to keep it inside", explicit)
		}
		return "", fmt.Errorf("no report path: root_dir is not configured, so there is no default "+
			"report directory; pass reportPath explicitly (report %s)", fileName)
	}
	if !filepath.IsAbs(root) {
		if explicit != "" {
			return "", fmt.Errorf("report path %q refused: root_dir %q is not absolute, so the report "+
				"directory it must stay inside would depend on the process working directory", explicit, root)
		}
		return "", fmt.Errorf("no report path: root_dir %q is not absolute, so the default report "+
			"directory would depend on the process working directory; pass reportPath explicitly "+
			"(report %s)", root, fileName)
	}
	reportsDir := filepath.Join(root, ReportDirName)
	if explicit != "" {
		return containedReportPath(reportsDir, explicit, onTightened, env)
	}
	path := filepath.Join(reportsDir, fileName)
	if err := ensureReportsDir(reportsDir, onTightened, env); err != nil {
		return "", fmt.Errorf("report path %s is not writable: %w", path, err)
	}
	return path, nil
}

// containedReportPath resolves a caller-chosen report path and refuses it unless
// the file would be created inside reportsDir. A relative path is joined onto
// reportsDir. The path that is checked is the path that is returned (cleaned, so
// no ".." is left for the kernel to resolve against a symlink the check did not
// see), and it is checked after symlinks are resolved: the parent's real
// location, with the first existing ancestor resolved when the parent does not
// exist yet, must be reportsDir's real location or below it. The file itself
// must not be a symlink (Lstat, so a dangling link that EvalSymlinks would call
// "does not exist" is still seen) and must not be a directory.
//
// Nothing outside reportsDir is created before the check passes. The parent
// directory is created afterwards and the check is made again against what now
// exists, so a link planted between the first check and the creation is not
// followed out.
func containedReportPath(reportsDir, explicit string, onTightened func(dir string, was fs.FileMode), env reportEnv) (string, error) {
	refuse := func(why string) (string, error) {
		return "", fmt.Errorf("report path %q refused: %s (reports must stay inside %s)", explicit, why, reportsDir)
	}
	if strings.ContainsRune(explicit, 0) {
		return refuse("it contains a NUL byte")
	}
	if err := ensureReportsDir(reportsDir, onTightened, env); err != nil {
		return "", fmt.Errorf("report directory %s is not usable: %w", reportsDir, err)
	}
	realReports, err := filepath.EvalSymlinks(reportsDir)
	if err != nil {
		return "", fmt.Errorf("report directory %s cannot be resolved: %w", reportsDir, err)
	}
	realReports = filepath.Clean(realReports)

	path := explicit
	if !filepath.IsAbs(path) {
		path = filepath.Join(reportsDir, path)
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)

	if !isWithinRoot(resolveExistingPrefix(parent), realReports) {
		return refuse("it resolves outside that directory")
	}
	if info, lerr := os.Lstat(path); lerr == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return refuse("the report file is a symlink")
		case info.IsDir():
			return refuse("it is a directory, not a file")
		}
	} else if !errors.Is(lerr, fs.ErrNotExist) {
		return "", fmt.Errorf("report path %s cannot be inspected: %w", path, lerr)
	}

	if err := EnsureDirWritable(path); err != nil {
		return "", fmt.Errorf("report path %s is not writable: %w", path, err)
	}
	if env.afterCreate != nil {
		env.afterCreate(parent)
	}
	// The directory chain now exists: check again against what is really there.
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("report path %s cannot be resolved: %w", path, err)
	}
	if !isWithinRoot(filepath.Clean(realParent), realReports) {
		return refuse("it resolves outside that directory")
	}
	return path, nil
}

// ensureReportsDir prepares the default report directory, {root}/.reports. That
// directory is inside the library share, which other users may be able to write,
// so before anything is written to it, it must be what this process made: it is
// created (DirMode) when absent, and when it exists it must be a real
// directory (not a symlink somebody planted to redirect the reports) owned by the
// user this process runs as. Lstat is used so a symlink is seen as a symlink.
//
// An owned directory that anyone can write (an older build created it 0777-ish)
// has world write, and only world write, removed, and onTightened is told. Group
// bits, the setgid and sticky bits, and any POSIX ACL are left exactly as they
// are: on a POSIX-ACL filesystem the group bits ARE the ACL mask (see
// DirMode), so clearing group write would silently strip the write access of
// every named user and group the operator granted, and the setgid bit keeps new
// files in the share's group. The directory is refused only when it is not ours,
// is not a directory, or the chmod fails. There is a small window between this
// check and the later file open; the open itself refuses a symlink (O_NOFOLLOW)
// and checks the file's owner.
func ensureReportsDir(dir string, onTightened func(dir string, was fs.FileMode), env reportEnv) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if merr := os.Mkdir(dir, DirMode); merr != nil && !errors.Is(merr, fs.ErrExist) {
			return merr
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink, not a directory; refusing to write reports through it", dir)
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case !ownedByUID(info, env.euid()):
		return fmt.Errorf("%s is not owned by the user this process runs as; refusing to trust it", dir)
	}
	if was := info.Mode().Perm(); was&worldWrite != 0 {
		// Perm bits plus the setuid, setgid and sticky bits, minus world write.
		// Dropping the special bits here would be a second change nobody asked for.
		next := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky) &^ worldWrite
		if cerr := env.chmod(dir, next); cerr != nil {
			return fmt.Errorf("%s is writable by everyone (mode %o) and world write could not be removed: %w", dir, was, cerr)
		}
		if onTightened != nil {
			onTightened(dir, was)
		}
	}
	return probeWritable(dir)
}

// worldWrite is the "other" write permission bit.
const worldWrite fs.FileMode = 0o002

// EnsureDirWritable creates the directory that will hold path and proves a
// file can be created in it, by creating and removing a probe file. MkdirAll
// alone succeeds on an existing read-only directory, so it is not proof the
// later write will land.
func EnsureDirWritable(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	return probeWritable(dir)
}

// probeWritable proves a file can be created in dir.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".report-write-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}
