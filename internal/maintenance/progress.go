// file: internal/maintenance/progress.go
// version: 1.1.0
// guid: a3b4c5d6-e7f8-9012-3456-7890abcdef12
// last-edited: 2026-10-26

package maintenance

import (
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// ProgressAdapter adapts operations.ProgressReporter to maintenance.ProgressReporter.
// It bridges the operations layer's richer progress API to the simpler interface
// that maintenance jobs consume.
type ProgressAdapter struct {
	Ops   operations.ProgressReporter
	cur   int
	total int
}

// SetTotal sets the expected total item count for progress reporting.
func (a *ProgressAdapter) SetTotal(n int) { a.total = n }

// Increment advances the current count by one and propagates progress to the
// underlying operations reporter.
func (a *ProgressAdapter) Increment() {
	a.cur++
	_ = a.Ops.UpdateProgress(a.cur, a.total, "")
}

// SeedProgress moves the current count to done and reports it, for a job that
// resumes an operation and skips the rows already finished: reporting from zero
// would drop the progress bar to the start of the run and, because the registry
// counts ops.items from the position it inherited, count the finished rows a
// second time as the job walked past them. Implements ProgressSeeder.
func (a *ProgressAdapter) SeedProgress(done int) {
	a.cur = done
	_ = a.Ops.UpdateProgress(a.cur, a.total, "")
}

// Log forwards a log entry to the underlying operations reporter.
func (a *ProgressAdapter) Log(level, message string, details *string) {
	_ = a.Ops.Log(level, message, details)
}
