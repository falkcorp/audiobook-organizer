// file: internal/diagnostics/export_result.go
// version: 1.0.0
// guid: 9e3a7c52-0b41-4d68-a2f7-16d8b5c03e49
// last-edited: 2026-10-26

package diagnostics

import (
	"encoding/json"
	"errors"
	"strings"
)

var errNoExportResult = errors.New("diagnostics: the export has no result data")

// ExportResult is the result payload of a finished diagnostics.export
// operation, stored on the op's own row by its Run and read back by the
// download route and its gate.
type ExportResult struct {
	// ZipPath is the finished archive on disk.
	ZipPath string `json:"zip_path"`
	// NarrowedTo is the permissions, out of the ones an export's contents depend
	// on, that the account the export was built for held when the build began:
	// the set the archive was narrowed to. A caller may be served the archive
	// only while it holds every one of them, so the narrowing cannot be undone
	// by asking for an export somebody else built. It is a snapshot taken by the
	// build itself, not re-read from the account later, so a role change after
	// the build neither widens nor narrows it. nil (the key is absent) means the
	// result carries no record, as every export built before the field existed,
	// and no caller is served such an archive. Present and empty means the
	// archive was built for an account that held none of them.
	NarrowedTo *[]string `json:"narrowed_to,omitempty"`
}

// ParseExportResult decodes the stored result of a diagnostics.export op. A
// result that is empty or not the expected JSON object is an error.
func ParseExportResult(raw string) (ExportResult, error) {
	var r ExportResult
	if strings.TrimSpace(raw) == "" {
		return r, errNoExportResult
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return ExportResult{}, err
	}
	return r, nil
}
