// file: internal/scanner/scan_failures.go
// version: 1.0.0
// guid: 77ddee5a-7784-422a-85fd-9948ecd881e3
// last-edited: 2026-09-12

package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// Per-file scan failures, surfaced into the operation log.
//
// Until 2026-09-12 a file the scan could not read was logged as free text
// ("ProcessFile failed for <path>: <err>") through a logger that, for every
// operation built on operations.LoggerFromReporter, writes to stdout only. The
// operation's own log never saw it, nothing counted it, and the Settings ->
// Paths "View Errors" button (which reads the op log) had nothing to show: a
// scan that hit ten unreadable files reported "Scan complete" and nothing else.
//
// FileFailures fixes that in two parts. Every failure is COUNTED. The first
// MaxFileFailureSamples are also written to the operation log individually, at
// warn, with file_path / stage / reason as structured attrs; the rest are
// counted only. At the end of the run one summary line carries the total, so a
// run that hits 40,000 bad files writes 25 lines plus a summary instead of
// flooding the op log -- and the registry reporter's 1,000-entry buffer, which
// drops oldest-first.
//
// What counts as a failure: a file whose read failed outright
// (ProcessFileWithTimeout returned an error -- open/stat/hash failure or the
// per-file timeout) or whose book row could not be saved. A file whose TAGS
// could not be read is not counted: ProcessFile falls back to filename
// metadata and the file is imported, degraded rather than failed.

// MaxFileFailureSamples bounds how many per-file failures are listed
// individually in the operation log. Every failure is still counted.
const MaxFileFailureSamples = 25

// Stage values for FileFailure.Stage.
const (
	// FileFailureStageRead: the file could not be read (open, stat, hash or
	// the per-file timeout). The book is still saved with path-derived
	// metadata, exactly as before.
	FileFailureStageRead = "read"
	// FileFailureStageSave: the book row for this path could not be saved.
	FileFailureStageSave = "save"
)

// FileFailure is one file the scan could not process.
type FileFailure struct {
	Path   string `json:"file_path"`
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
}

// FileFailures accumulates the per-file failures of one scan run. It is safe
// for concurrent use by ProcessBooksParallel's workers.
type FileFailures struct {
	mu      sync.Mutex
	total   int
	samples []FileFailure
}

// Record counts f and, while fewer than MaxFileFailureSamples have been
// listed, writes it to scanLog at warn with structured attrs. The first
// failure past the cap writes one notice that further failures are counted
// but not listed; those still reach the process log at debug.
func (c *FileFailures) Record(scanLog logger.Logger, f FileFailure) {
	if scanLog == nil {
		scanLog = logger.New("scanner")
	}
	c.mu.Lock()
	c.total++
	n := c.total
	if n <= MaxFileFailureSamples {
		c.samples = append(c.samples, f)
	}
	c.mu.Unlock()

	if n <= MaxFileFailureSamples {
		logger.LogWithAttrs(scanLog, slog.LevelWarn, "scan: file failed",
			slog.String("file_path", f.Path),
			slog.String("stage", f.Stage),
			slog.String("reason", f.Reason))
		return
	}
	if n == MaxFileFailureSamples+1 {
		logger.LogWithAttrs(scanLog, slog.LevelWarn,
			fmt.Sprintf("scan: more than %d files failed; further failures are counted in the end-of-scan summary but not listed",
				MaxFileFailureSamples),
			slog.Int("files_listed", MaxFileFailureSamples))
	}
	scanLog.Debug("scan: file failed (not listed in the op log) %s [%s]: %s", f.Path, f.Stage, f.Reason)
}

// Total is the number of failures recorded, listed or not.
func (c *FileFailures) Total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// Samples returns a copy of the listed failures (at most MaxFileFailureSamples).
func (c *FileFailures) Samples() []FileFailure {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]FileFailure(nil), c.samples...)
}

// ReportSummary writes one warn line carrying the total when any file failed.
// A run with no failures writes nothing.
func (c *FileFailures) ReportSummary(scanLog logger.Logger) {
	total, samples := c.Total(), c.Samples()
	if total == 0 {
		return
	}
	logger.LogWithAttrs(scanLog, slog.LevelWarn,
		fmt.Sprintf("scan finished with %d file failure(s); %d listed individually", total, len(samples)),
		slog.Int("files_failed", total),
		slog.Int("files_listed", len(samples)),
		slog.Int("files_omitted", total-len(samples)))
}

type fileFailuresKey struct{}

// withFileFailures attaches c to ctx so every ProcessBooksParallel call made
// under it (one per folder chunk) records into the same run-wide collector.
func withFileFailures(ctx context.Context, c *FileFailures) context.Context {
	return context.WithValue(ctx, fileFailuresKey{}, c)
}

// fileFailuresFrom returns the collector attached to ctx, or nil.
func fileFailuresFrom(ctx context.Context) *FileFailures {
	c, _ := ctx.Value(fileFailuresKey{}).(*FileFailures)
	return c
}
