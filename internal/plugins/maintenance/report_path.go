// file: internal/plugins/maintenance/report_path.go
// version: 1.0.0
// guid: 5591f89d-611e-4e24-83c2-e02a887bd301
// last-edited: 2026-09-12

package maintenance

import (
	"fmt"
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
// app-owned state on the large library volume, dot-prefixed so pathutil's
// hidden-dir rule keeps every library walker out of it.
const maintenanceReportDirName = ".reports"

// resolveReportPath returns where an op writes its report: the caller's
// explicit path when one was given, otherwise {RootDir}/.reports/<fileName>.
//
// It returns an error rather than guessing when RootDir is unset or not
// absolute. Ops call it BEFORE doing any work, so a misconfigured run fails
// with nothing written -- instead of applying changes whose only complete
// record then lands in an unknown directory, or nowhere.
func (p *Plugin) resolveReportPath(explicit, fileName string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
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
	return filepath.Join(root, maintenanceReportDirName, fileName), nil
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
