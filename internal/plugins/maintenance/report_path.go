// file: internal/plugins/maintenance/report_path.go
// version: 1.2.0
// guid: 5591f89d-611e-4e24-83c2-e02a887bd301
// last-edited: 2026-09-12

package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenanceReportDirName is the directory, under the library root, where a
// maintenance op writes its per-row TSV report when the caller did not pass an
// explicit reportPath.
//
// Why here and not a relative "reports/": a relative path resolves against the
// process working directory. In production that is the service's
// WorkingDirectory (the secure state dir that holds the credentials, on a
// snapshot-held volume); under `go test` it is the package source directory,
// which is how dedupe-book-file-rows-*.tsv kept appearing untracked in every
// worktree and how metadata-cache-reap-unknown-op.tsv got committed. Neither is
// a place the application owns for artifacts.
//
// {RootDir}/.reports follows the same convention as {RootDir}/.wav-cache
// (intro_transcribe.go) and {RootDir}/.activity (config.ResolveActivityDBPath):
// app-owned state on the large library volume, dot-prefixed so walkers that
// call pathutil.ShouldSkipDir (cleanup.go and file_provenance_capture.go in
// this package, for example) stay out of it.
//
// recover-missing-files' inventory walk calls it too. It did not until
// 2026-09-12: it descended into every directory under RootDir, so a report TSV
// whose size matched a missing row's recorded size could become that row's
// single repoint candidate once requireExtMatch was off.
const maintenanceReportDirName = ".reports"

// resolveReportPath returns where an op writes its report -- the caller's
// explicit path when one was given, otherwise {RootDir}/.reports/<fileName> --
// and proves that path's directory exists and is writable before returning it.
//
// It returns an error rather than guessing when RootDir is unset or not
// absolute, and when the directory cannot be created or written (a read-only
// mount, a FILE squatting on .reports, a permission problem). Ops call it
// BEFORE doing any work, so a run that could not record its decisions fails
// with nothing done. Resolving the string alone is not enough: every op's
// report writer only LOGS a failed write and carries on, so an apply whose
// report directory turned out to be unwritable would delete or rewrite rows
// with no record of which.
func (p *Plugin) resolveReportPath(explicit, fileName string) (string, error) {
	path := explicit
	if path == "" {
		root := strings.TrimSpace(p.deps.RootDir())
		if root == "" {
			return "", fmt.Errorf("no report path: root_dir is not configured, so there is no default "+
				"report directory; pass reportPath explicitly (report %s)", fileName)
		}
		if !filepath.IsAbs(root) {
			return "", fmt.Errorf("no report path: root_dir %q is not absolute, so the default report "+
				"directory would depend on the process working directory; pass reportPath explicitly "+
				"(report %s)", root, fileName)
		}
		path = filepath.Join(root, maintenanceReportDirName, fileName)
	}
	if err := ensureReportDirWritable(path); err != nil {
		return "", fmt.Errorf("report path %s is not writable: %w", path, err)
	}
	return path, nil
}

// ensureReportDirWritable creates the directory that will hold `path` and
// proves a file can be created in it, by creating and removing a probe file.
// MkdirAll alone succeeds on an existing read-only directory, so it is not
// proof the later write will land.
func ensureReportDirWritable(path string) error {
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

// opReportFileName is "<prefix>-<opID>.tsv", the per-run report name the
// repoint/recover/mark/merge/reap ops use. A reporter that carries no op ID
// (direct calls in tests) yields "<prefix>-unknown-op.tsv".
func opReportFileName(reporter sdk.Reporter, prefix string) string {
	name := registry.ReporterOpID(reporter)
	if name == "" {
		name = "unknown-op"
	}
	return prefix + "-" + name + ".tsv"
}
