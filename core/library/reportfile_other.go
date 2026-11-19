//go:build !unix

// file: core/library/reportfile_other.go
// version: 1.0.0
// guid: 7617f022-c2fe-484c-a2d3-6a6f4fad01a0
// last-edited: 2026-11-18

package library

import "os"

// linkCount reports that a link count is not available: without Unix stat
// there is nothing to compare. OpenReportFile refuses every path on these
// platforms before it gets here, because it has no directory handles.
func linkCount(os.FileInfo) (uint64, bool) { return 0, false }
