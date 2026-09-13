// file: internal/scanner/scan_progress_owner.go
// version: 1.0.0
// guid: 2c532b07-20c8-4a91-8cbb-2a2cced4a0d8
// last-edited: 2026-09-12

package scanner

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// scanProgress is the single owner of a library scan's (current, total).
//
// WHY THIS EXISTS: an operation has exactly one progress row -- current, total,
// message -- and before 2026-09-12 four different writers on library.scan each
// filled it with their own denominator:
//
//   - scanFolder's folder-transition and per-book writes: cumulative books
//     processed / cumulative books discovered (the correct numbers).
//   - ScanDirectoryParallel, handed the op's logger: "Discovering folders: n
//     found" as (n, 0) and "Scanning folders: n/len(dirs)", where len(dirs) is
//     the sub-directory count of the CURRENT import folder only -- 1/1 for a
//     folder holding one book.
//   - runAIBatchPhase inside ProcessBooksParallel: (batch, totalBatches) of one
//     chunk.
//   - The auto-organize hook (organizer.PerformOrganizeStats): (0, 1) for its
//     pre-organize backup and (count, len(booksToOrganize)) for this folder's
//     books.
//
// Every write replaced the last, so the UI's bar flipped between "0/1", "1/1"
// and the real ~61,000-book total as the scan moved between steps. That was
// the owner's "why does the library scan randomly think it's scanning 0/1".
//
// The fix keeps the sub-steps' calls (see subStepLogger for why they cannot be
// dropped) but routes them through here, which substitutes the scan's own
// cumulative counters. The counters are read from the same atomics scanFolder
// maintains, so there is no second copy of the numbers to drift.
//
// mu serializes the advance-and-publish of the per-book callback against every
// other write. Without it two workers could take current=5 and current=6 from
// the atomic and publish them in the opposite order, so the row would step
// backwards. The price is that progress writes from the worker pool are
// serialized; each one is an in-memory update plus a deduplicated log line in
// the registry reporter, small against the tag read and database save each
// book already costs.
type scanProgress struct {
	mu         sync.Mutex
	log        logger.Logger
	processed  *atomic.Int32
	discovered *atomic.Int64
}

func newScanProgress(log logger.Logger, processed *atomic.Int32, discovered *atomic.Int64) *scanProgress {
	return &scanProgress{log: log, processed: processed, discovered: discovered}
}

// report publishes message with the scan's current cumulative counters.
func (p *scanProgress) report(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log.UpdateProgress(int(p.processed.Load()), int(p.discovered.Load()), message)
}

// advance counts one more processed book and publishes it. The increment and
// the write happen under one lock so concurrent workers publish in order.
func (p *scanProgress) advance(message func(current, total int) string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := int(p.processed.Add(1))
	total := int(p.discovered.Load())
	p.log.UpdateProgress(current, total, message(current, total))
}

// subStep returns a logger for code that runs as one step of the scan but was
// written to own an operation's progress by itself: ScanDirectoryParallel,
// ProcessBooksParallel and the auto-organize hook. Those same functions run
// standalone elsewhere (folder.autoscan calls ScanDirectoryParallel directly;
// library.organize calls PerformOrganize directly) and keep their own numbers
// there, which is why the substitution happens here at the hand-off rather
// than inside them.
func (p *scanProgress) subStep(parent logger.Logger, label string) logger.Logger {
	return &subStepLogger{Logger: parent, owner: p, label: label}
}

// subStepLogger keeps a sub-step visible without letting it overwrite the
// scan's counters: its UpdateProgress keeps only the message, prefixed with
// the step's label, and publishes it with the scan's cumulative numbers.
//
// The call is substituted, never dropped or throttled. The registry reporter's
// UpdateProgress is also the stuck-op watchdog's liveness stamp
// (reporter_db.go stamps it before anything else), and the organize hook's
// pre-organize backup can run for minutes with that call as its only sign of
// life. Swallowing it would bring back the 5-minute watchdog kill described in
// operations.LoggerFromReporter.
//
// current_phase (registry Reporter.RunPhase) is not used: it lives on the
// registry Reporter, which PerformScan never receives, and the operations
// indicator renders only the progress message, so the label goes in the
// message where the user already looks.
type subStepLogger struct {
	logger.Logger
	owner *scanProgress
	label string
}

func (l *subStepLogger) UpdateProgress(_, _ int, message string) {
	if l.label != "" {
		message = l.label + ": " + message
	}
	l.owner.report(message)
}

// With keeps the substitution on child loggers. Without the override the
// promoted method would return the parent's plain child and the sub-step's
// numbers would reach the op again at the first .With() call -- the organizer
// and the scanner both call With on the logger they are handed.
func (l *subStepLogger) With(subsystem string) logger.Logger {
	return &subStepLogger{Logger: l.Logger.With(subsystem), owner: l.owner, label: l.label}
}

// LogAttrs forwards structured lines. logger.AttrLogger is an optional
// capability found by type assertion, so embedding the Logger interface alone
// would hide the parent's LogAttrs and the scanner's per-file failure lines
// (scan_failures.go) would lose their file_path/stage/reason fields in the
// operation log. LogWithAttrs keeps them structured when the parent supports
// it and appends them to the message when it does not.
func (l *subStepLogger) LogAttrs(level slog.Level, msg string, attrs ...slog.Attr) {
	logger.LogWithAttrs(l.Logger, level, msg, attrs...)
}

var _ logger.AttrLogger = (*subStepLogger)(nil)
