// file: internal/libroot/libroot.go
// version: 1.0.0
// guid: 4d8b2f61-a3c7-4e90-b512-9e6f0c1d7a38
// last-edited: 2026-10-25

// Package libroot holds the two checks every library-wide maintenance op makes
// against the library root before it trusts what it finds there: that the root
// is really mounted, and that a per-row report can be written under it. It
// imports only the standard library, so the maintenance plugin and the legacy
// maintenance jobs share one copy instead of each keeping their own.
package libroot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mounted refuses to proceed when the library root is unset, unreadable or
// empty. An unmounted share (a ZFS dataset, an NFS or autofs mount) leaves an
// empty directory behind, so every path under it answers ENOENT; an op that
// reads "not found" as "the file is gone" would then conclude the whole library
// is gone. Call it before acting on any not-found, not only before a write.
//
// Call it BEFORE ReportPath: ReportPath creates {root}/.reports, and that one
// directory is enough to make an empty, unmounted root read as populated here.
func Mounted(root string) error {
	if root == "" {
		return errors.New("library root is not configured")
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("library root %s unreadable: %w", root, err)
	}
	if len(ents) == 0 {
		return fmt.Errorf("library root %s is empty (share not mounted?)", root)
	}
	return nil
}

// ReportDirName is the directory, under the library root, where a maintenance
// op writes its per-row TSV report when the caller did not pass an explicit
// path. It is app-owned state on the large library volume, dot-prefixed so
// directory walkers that call pathutil.ShouldSkipDir stay out of it. A relative
// default is deliberately not offered: it would resolve against the process
// working directory, which in production is the credentials state dir and
// under `go test` is the package source directory.
const ReportDirName = ".reports"

// ReportPath returns where an op writes its report: the caller's explicit path
// when one was given, otherwise {root}/.reports/<fileName>. It proves the
// path's directory exists and is writable before returning, so an op can call
// it BEFORE doing any work and fail with nothing done rather than apply and
// then lose its record. It returns an error, rather than guessing, when root is
// unset or not absolute and no explicit path was given.
func ReportPath(root, explicit, fileName string) (string, error) {
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
	if err := EnsureDirWritable(path); err != nil {
		return "", fmt.Errorf("report path %s is not writable: %w", path, err)
	}
	return path, nil
}

// EnsureDirWritable creates the directory that will hold path and proves a
// file can be created in it, by creating and removing a probe file. MkdirAll
// alone succeeds on an existing read-only directory, so it is not proof the
// later write will land.
func EnsureDirWritable(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return err
	}
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
