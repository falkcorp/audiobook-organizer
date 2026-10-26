// file: internal/libroot/libroot.go
// version: 1.6.0
// guid: 4d8b2f61-a3c7-4e90-b512-9e6f0c1d7a38
// last-edited: 2026-10-26

// Package libroot holds the two checks every library-wide maintenance op makes
// against the library root before it trusts what it finds there: that the root
// is really mounted, and that a per-row report can be written under it. It
// imports only the standard library and the permission constants in
// internal/util (a layer-0 leaf), so the maintenance plugin and the legacy
// maintenance jobs share one copy instead of each keeping their own.
package libroot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/util"
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
// unset or not absolute and no explicit path was given.
//
// onTightened, when not nil, is called when the default report directory already
// existed with world write and that bit was removed: it gets the directory and
// the permission bits it had. libroot imports no module package other than the
// permission constants in internal/util, so it cannot log; the caller does.
func ReportPathNotify(root, explicit, fileName string, onTightened func(dir string, was fs.FileMode)) (string, error) {
	path := explicit
	if path == "" {
		root = strings.TrimSpace(root)
		if root == "" {
			return "", fmt.Errorf("no report path: root_dir is not configured, so there is no default "+
				"report directory; pass reportPath explicitly (report %s)", fileName)
		}
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("no report path: root_dir %q is not absolute, so the default report "+
				"directory would depend on the process working directory; pass reportPath explicitly "+
				"(report %s)", root, fileName)
		}
		path = filepath.Join(root, ReportDirName, fileName)
	}
	var err error
	if explicit == "" {
		err = ensureReportsDir(filepath.Dir(path), onTightened)
	} else {
		err = EnsureDirWritable(path)
	}
	if err != nil {
		return "", fmt.Errorf("report path %s is not writable: %w", path, err)
	}
	return path, nil
}

// ensureReportsDir prepares the default report directory, {root}/.reports. That
// directory is inside the library share, which other users may be able to write,
// so before anything is written to it, it must be what this process made: it is
// created (util.DirMode) when absent, and when it exists it must be a real
// directory (not a symlink somebody planted to redirect the reports) owned by the
// user this process runs as. Lstat is used so a symlink is seen as a symlink.
//
// An owned directory that anyone can write (an older build created it 0777-ish)
// has world write, and only world write, removed, and onTightened is told. Group
// bits, the setgid and sticky bits, and any POSIX ACL are left exactly as they
// are: on a POSIX-ACL filesystem the group bits ARE the ACL mask (see
// util.DirMode), so clearing group write would silently strip the write access of
// every named user and group the operator granted, and the setgid bit keeps new
// files in the share's group. The directory is refused only when it is not ours,
// is not a directory, or the chmod fails. There is a small window between this
// check and the later file open; the open itself refuses a symlink (O_NOFOLLOW)
// and checks the file's owner.
func ensureReportsDir(dir string, onTightened func(dir string, was fs.FileMode)) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if merr := os.Mkdir(dir, util.DirMode); merr != nil && !errors.Is(merr, fs.ErrExist) {
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
	case !OwnedByCurrentUser(info):
		return fmt.Errorf("%s is not owned by the user this process runs as; refusing to trust it", dir)
	}
	if was := info.Mode().Perm(); was&worldWrite != 0 {
		// Perm bits plus the setuid, setgid and sticky bits, minus world write.
		// Dropping the special bits here would be a second change nobody asked for.
		next := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky) &^ worldWrite
		if cerr := chmodDir(dir, next); cerr != nil {
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

// chmodDir changes a directory's mode; a variable so a test can make it fail.
var chmodDir = os.Chmod

// EnsureDirWritable creates the directory that will hold path and proves a
// file can be created in it, by creating and removing a probe file. MkdirAll
// alone succeeds on an existing read-only directory, so it is not proof the
// later write will land.
func EnsureDirWritable(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, util.DirMode); err != nil {
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
