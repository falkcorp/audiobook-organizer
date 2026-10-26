// file: internal/plugins/maintenance/report_path.go
// version: 1.5.0
// guid: 5591f89d-611e-4e24-83c2-e02a887bd301
// last-edited: 2026-10-26

package maintenance

import (
	"io/fs"

	"github.com/falkcorp/audiobook-organizer/internal/libroot"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// resolveReportPath returns where an op writes its report -- the caller's
// explicit path when one was given, otherwise {RootDir}/.reports/<fileName> --
// and proves that path's directory exists and is writable before returning it.
// The logic lives in internal/libroot, shared with the legacy maintenance jobs;
// see libroot.ReportPathNotify for why it fails rather than guessing, and why ops call
// it BEFORE doing any work. A default report directory that was world-writable
// has that bit removed by the helper (and nothing else changed) and is logged here.
func (p *Plugin) resolveReportPath(explicit, fileName string) (string, error) {
	return libroot.ReportPathNotify(p.deps.RootDir(), explicit, fileName, func(dir string, was fs.FileMode) {
		logger.New("maintenance").Warn("removed world write from the report directory, its mode was %o dir=%s", was, logger.SanitizeLogValue(dir))
	})
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
