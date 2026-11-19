// file: core/library/libroot.go
// version: 1.9.0
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

// LedgerDirName is the directory, under the library root, that holds append-only
// ledgers the application keeps a copy of outside its database (today the file
// provenance export). It is separate from ReportDirName on purpose: every report
// writer truncates the file it is given and reports-prune deletes by name, so a
// ledger kept among the reports is one wrong report name away from being emptied.
// Nothing that resolves a report path can reach this directory, and no report
// pruner looks in it. It is dot-prefixed like ReportDirName, so Mounted ignores it
// and directory walkers skip it.
const LedgerDirName = ".ledger"

// confinedDir names one of the application-owned directories under the library
// root that ReportPathNotify and LedgerPathNotify confine a file to, and the word
// its refusals use for what is kept there.
type confinedDir struct {
	name string // directory name under the root
	noun string // "report" or "ledger"
}

// reportDir and ledgerDir return the two confined directories. Functions, not
// package-level values, so there is no mutable package state to reassign.
func reportDir() confinedDir { return confinedDir{name: ReportDirName, noun: "report"} }
func ledgerDir() confinedDir { return confinedDir{name: LedgerDirName, noun: "ledger"} }

// LedgerPathNotify is ReportPathNotify for a ledger: the same confinement, the
// same checks on an existing file, the same directory preparation, over
// {root}/.ledger instead of {root}/.reports. A path that names a file in
// {root}/.reports is refused here, and a path in {root}/.ledger is refused by
// ReportPathNotify, so the two kinds of file cannot be pointed at each other. The
// returned path is written with OpenReportFile (ReportAppend); see there for the
// write-time checks.
func LedgerPathNotify(root, explicit, fileName string, onTightened func(dir string, was fs.FileMode)) (string, error) {
	return confinedPathNotify(ledgerDir(), root, explicit, fileName, onTightened, defaultReportEnv())
}

// ReportPathNotify returns where an op writes its report: the caller's explicit
// path when one was given, otherwise {root}/.reports/<fileName>. It proves the
// report directory exists and is writable before returning, so an op can call it
// BEFORE doing any work and fail with nothing done rather than apply and then
// lose its record. It returns an error, rather than guessing, when root is unset
// or not absolute: with no default there is nowhere to write, and with an
// explicit path there is no report directory to confine it to.
//
// An explicit path is never trusted as given. It must name a file directly in
// {root}/.reports: either a bare file name ("r.tsv"), or an absolute path whose
// directory is that report directory (spelled through the root or through its
// real location). A name with a directory in it, in any form ("sub/r.tsv",
// "./r.tsv", "../r.tsv"), is refused, so there are no subdirectories for a
// report to be created in and nothing is created outside the report directory.
// A report file that already exists must pass the same checks the writer makes
// (see OpenReportFile): not a symlink, not a directory, a regular file owned by
// this user with one link. The report directory itself is checked and prepared
// exactly as for the default path (see ensureReportsDir).
//
// This is the decision, made when it is called; it is not a lock. The file is
// written later, by WriteReportFile or OpenReportFile, and those repeat the
// checks on the descriptor they open, which is what stops a link planted between
// this call and the write. Writing the returned path with anything else (os.Create,
// os.WriteFile) follows a symlink planted there and gets none of that.
//
// onTightened, when not nil, is called when the report directory already
// existed with world write and that bit was removed: it gets the directory and
// the permission bits it had. This package imports no module package, so it
// cannot log; the caller does.
func ReportPathNotify(root, explicit, fileName string, onTightened func(dir string, was fs.FileMode)) (string, error) {
	return reportPathNotify(root, explicit, fileName, onTightened, defaultReportEnv())
}

// isSingleComponent reports whether name is one plain file name: not empty, not
// "." or "..", with no separator and nothing that makes it a path. It is what a
// default file name must be for a directory join to stay inside that directory.
func isSingleComponent(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && filepath.IsLocal(name) &&
		!strings.ContainsRune(name, 0)
}

// reportEnv is the process state ReportPathNotify reads: how a directory's mode
// is changed and which user ID owns what this process may trust. It is passed
// down rather than held in package variables so a test can stand in for another
// user, or a failing chmod, without mutable global state.
type reportEnv struct {
	chmod func(name string, mode os.FileMode) error
	euid  func() int
}

// defaultReportEnv returns the real process environment.
func defaultReportEnv() reportEnv {
	return reportEnv{chmod: os.Chmod, euid: os.Geteuid}
}

func reportPathNotify(root, explicit, fileName string, onTightened func(dir string, was fs.FileMode), env reportEnv) (string, error) {
	return confinedPathNotify(reportDir(), root, explicit, fileName, onTightened, env)
}

func confinedPathNotify(kind confinedDir, root, explicit, fileName string, onTightened func(dir string, was fs.FileMode), env reportEnv) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		if explicit != "" {
			return "", fmt.Errorf("%s path %q refused: root_dir is not configured, so there is no "+
				"%s directory to keep it inside", kind.noun, explicit, kind.noun)
		}
		return "", fmt.Errorf("no %s path: root_dir is not configured, so there is no default "+
			"%s directory; configure an absolute root_dir (%s %s)", kind.noun, kind.noun, kind.noun, fileName)
	}
	if !filepath.IsAbs(root) {
		if explicit != "" {
			return "", fmt.Errorf("%s path %q refused: root_dir %q is not absolute, so the %s "+
				"directory it must stay inside would depend on the process working directory", kind.noun, explicit, root, kind.noun)
		}
		return "", fmt.Errorf("no %s path: root_dir %q is not absolute, so the default %s "+
			"directory would depend on the process working directory; configure an absolute root_dir "+
			"(%s %s)", kind.noun, root, kind.noun, kind.noun, fileName)
	}
	reportsDir := filepath.Join(root, kind.name)
	if explicit != "" {
		return containedReportPath(kind, reportsDir, explicit, onTightened, env)
	}
	// The default name is joined onto the directory, so it must not be able to
	// leave it: one plain file name, never a path.
	if !isSingleComponent(fileName) {
		return "", fmt.Errorf("default %s name %q is not a single file name", kind.noun, fileName)
	}
	path := filepath.Join(reportsDir, fileName)
	if err := ensureReportsDir(reportsDir, onTightened, env); err != nil {
		return "", fmt.Errorf("%s path %s is not writable: %w", kind.noun, path, err)
	}
	if err := checkExistingReportFile(path, reportOpenEnv{euid: env.euid()}); err != nil {
		return "", err
	}
	return path, nil
}

// containedReportPath resolves a caller-chosen report path and refuses it unless
// it names a file directly in reportsDir. A bare file name is joined onto
// reportsDir. An absolute path is accepted only when its directory is reportsDir:
// lexically, or, for a caller who spelled the root another way (a symlinked mount
// point), when the directory resolves to reportsDir's real location. Anything with
// a directory component is refused before the filesystem is consulted, so no "..",
// no symlinked subdirectory and no not-yet-created subdirectory can carry the
// report out, and nothing is created here except reportsDir itself (ensureReportsDir).
//
// The path returned is reportsDir joined with the bare name, never the caller's
// spelling. An existing file at that name is vetted the way the writer vets it, so
// a symlink, a directory, a file owned by someone else or a hard link is refused
// now, before any work.
func containedReportPath(kind confinedDir, reportsDir, explicit string, onTightened func(dir string, was fs.FileMode), env reportEnv) (string, error) {
	refuse := func(why string) (string, error) {
		return "", fmt.Errorf("%s path %q refused: %s (a %s must be a file directly in %s)", kind.noun, explicit, why, kind.noun, reportsDir)
	}
	if strings.ContainsRune(explicit, 0) {
		return refuse("it contains a NUL byte")
	}
	if err := ensureReportsDir(reportsDir, onTightened, env); err != nil {
		return "", fmt.Errorf("%s directory %s is not usable: %w", kind.noun, reportsDir, err)
	}

	name := explicit
	if filepath.IsAbs(explicit) {
		clean := filepath.Clean(explicit)
		dir := filepath.Dir(clean)
		if dir != reportsDir {
			realDir, derr := filepath.EvalSymlinks(dir)
			realReports, rerr := filepath.EvalSymlinks(reportsDir)
			if derr != nil || rerr != nil || filepath.Clean(realDir) != filepath.Clean(realReports) {
				return refuse("it is not directly inside that directory")
			}
		}
		name = filepath.Base(clean)
	} else if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		return refuse("it has a directory in it")
	}
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		return refuse("it does not name a file")
	}

	path := filepath.Join(reportsDir, name)
	if err := checkExistingReportFile(path, reportOpenEnv{euid: env.euid()}); err != nil {
		return "", err
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
// is not a directory, or the chmod fails.
//
// The directory stays group-writable by design, so another user in the group can
// plant a symlink or a hard link at a report's name at any time. This function
// cannot prevent that and does not try: the writers (WriteReportFile and
// OpenReportFile) re-open the directory by handle and open the file with
// O_NOFOLLOW relative to it, and refuse a file that is not a regular file owned by
// this user with a single link.
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
