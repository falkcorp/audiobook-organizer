// file: internal/plugins/maintenance/report_path.go
// version: 1.5.3
// guid: 5591f89d-611e-4e24-83c2-e02a887bd301
// last-edited: 2026-11-18

package maintenance

import (
	"io/fs"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/library"
)

// resolveReportPath returns where an op writes its report -- the caller's
// explicit path when one was given, otherwise {RootDir}/.reports/<fileName> --
// and proves that path's directory exists and is writable before returning it.
// An explicit path is confined to {RootDir}/.reports: it must be a bare file name
// in that directory, or an absolute path whose directory is that one. A name with a
// directory in it, a path anywhere else and any explicit path when RootDir is unset
// are refused, so a caller-supplied reportPath can never make an op write outside
// the report directory.
//
// The path this returns is the decision, not the write. Write it with
// library.WriteReportFile (or OpenReportFile), never os.WriteFile or os.Create: those
// follow a symlink planted at the name after this was checked, and the library
// writers do not.
//
// The logic lives in core/library, shared with the legacy maintenance jobs;
// see library.ReportPathNotify for why it fails rather than guessing, and why ops call
// it BEFORE doing any work. A default report directory that was world-writable
// has that bit removed by the helper (and nothing else changed) and is logged here.
func (p *Plugin) resolveReportPath(explicit, fileName string) (string, error) {
	return library.ReportPathNotify(p.deps.RootDir(), explicit, fileName, func(dir string, was fs.FileMode) {
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
