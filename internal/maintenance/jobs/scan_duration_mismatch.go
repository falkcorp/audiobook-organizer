// file: internal/maintenance/jobs/scan_duration_mismatch.go
// version: 1.2.0
// guid: a1000018-0000-0000-0000-000000000018
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog"
)

func init() { maintenance.Register(&scanDurationMismatchJob{}) }

type scanDurationMismatchJob struct{}

func (j *scanDurationMismatchJob) ID() string       { return "scan-duration-mismatch" }
func (j *scanDurationMismatchJob) Name() string     { return "Scan Duration Mismatch" }
func (j *scanDurationMismatchJob) Category() string { return "files" }
func (j *scanDurationMismatchJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}
func (j *scanDurationMismatchJob) Description() string {
	return "Report books whose local duration differs significantly from Audible runtime"
}
func (j *scanDurationMismatchJob) CanResume() bool { return false }
func (j *scanDurationMismatchJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, _ bool) error {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return err
	}
	reporter.SetTotal(len(books))
	mismatches := 0
	for i := range books {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		b := &books[i]
		if b.Duration == nil || b.AudibleRuntimeMin == nil {
			continue
		}
		audibleSec := (*b.AudibleRuntimeMin) * 60
		delta := *b.Duration - audibleSec
		if delta < 0 {
			delta = -delta
		}
		if delta > 120 {
			mismatches++
			detail := fmt.Sprintf("local=%ds audible=%ds delta=%ds", *b.Duration, audibleSec, delta)
			slog.Warn("duration mismatch"+b.Title, "details", detail)
		}
	}
	slog.Info("scan-duration-mismatch complete mismatches", "mismatches", mismatches)
	return nil
}
