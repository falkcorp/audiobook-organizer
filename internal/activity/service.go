// file: internal/activity/service.go
// version: 1.6.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-08

package activity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Service wraps an ActivityStorer and provides business-level methods
// for recording and querying unified activity log entries.
type Service struct {
	store database.ActivityStorer
}

// NewService creates a new Service backed by the given store.
func NewService(store database.ActivityStorer) *Service {
	return &Service{store: store}
}

// Record inserts an activity entry into the store. Automatically enriches entry
// with derived tags (op:, book:, outcome:, source:, action:, scope:) before
// storing. The entry ID is discarded; callers that need it should call the
// store directly.
func (s *Service) Record(entry database.ActivityEntry) error {
	EnrichTags(&entry)
	_, err := s.store.Record(entry)
	return err
}

// Query returns entries matching the filter plus the total matching count.
// Callers on a request path must pass the request's context: the scan aborts
// as soon as it is cancelled, which is what stops an abandoned request from
// scanning the whole log after the client has disconnected.
func (s *Service) Query(ctx context.Context, filter database.ActivityFilter) ([]database.ActivityEntry, int, error) {
	return s.store.Query(ctx, filter)
}

// Summarize collapses old entries in the given tier that are older than olderThan.
// Returns the count of original rows deleted.
func (s *Service) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	return s.store.Summarize(ctx, olderThan, tier)
}

// Prune hard-deletes all entries of the given tier older than olderThan.
// Returns the number of rows deleted.
func (s *Service) Prune(olderThan time.Time, tier string) (int, error) {
	return s.store.Prune(olderThan, tier)
}

// CompactByDay groups old change/debug entries by UTC day into digest rows.
func (s *Service) CompactByDay(ctx context.Context, olderThan time.Time) (database.CompactResult, error) {
	return s.store.CompactByDay(ctx, olderThan)
}

// GetDistinctSources returns all unique sources with their entry counts,
// narrowed by the given filter's tier/level/since/until/search fields.
// As with Query, request-path callers must pass the request's context.
func (s *Service) GetDistinctSources(ctx context.Context, filter database.ActivityFilter) ([]database.SourceCount, error) {
	return s.store.GetDistinctSources(ctx, filter)
}

// RecompactDigests re-derives type, tier, and tags on all stored daily-digest
// entries that were compacted before tag enrichment was added (2026-05-20).
// Returns the count of digests touched and skipped.
func (s *Service) RecompactDigests(ctx context.Context) (database.RecompactResult, error) {
	return s.store.RecompactDigests(ctx)
}

// Store returns the underlying ActivityStorer (e.g. for close or direct access).
func (s *Service) Store() database.ActivityStorer {
	return s.store
}

// ErrSummaryClampUnsupported is returned when the active activity backend has no
// SQLite side to clamp (ActivityBackend=pebble). It is a distinct error rather
// than a zero result so a caller can tell "nothing needed clamping" from "this
// backend cannot be clamped at all" — the two look identical in the counters and
// mean opposite things about whether the work still has to happen.
var ErrSummaryClampUnsupported = errors.New("activity: summary clamp requires the SQLite backend")

// ClampSummaries retroactively applies the write-path summary cap to rows
// written before that cap existed, optionally VACUUMing afterwards to hand the
// freed pages back to the filesystem.
//
// vacuum is a separate switch because clamping alone shrinks values without
// shrinking the file: a run that skips it truthfully reports gigabytes reclaimed
// while df does not move. It is skipped only for a dry run, which has nothing to
// compact.
//
// It is deliberately NOT conditioned on this pass having clamped anything. That
// guard existed until 2026-09-08 and made the operation unable to fix the exact
// situation it was written for: after the first production clamp reclaimed
// 10.66 GB, the WAL held 11,514,065,152 bytes that VACUUM would have released —
// but every subsequent call found zero rows left to clamp and therefore skipped
// the vacuum, so no request could reach the reclaim path. Restarting does not
// help either: SQLite only deletes the -wal on a clean last-connection close.
// `vacuum` already defaults to false, so passing it is an explicit request and
// honouring it unconditionally is what the caller asked for.
func (s *Service) ClampSummaries(ctx context.Context, max int, dryRun, vacuum bool) (database.ClampSummariesResult, error) {
	var zero database.ClampSummariesResult
	clamper, ok := database.FindSummaryClamper(s.store)
	if !ok {
		return zero, ErrSummaryClampUnsupported
	}

	res, err := clamper.ClampOversizedSummaries(ctx, database.ClampSummariesOptions{Max: max, DryRun: dryRun})
	if err != nil {
		return res, err
	}
	if vacuum && !dryRun {
		if _, verr := clamper.VacuumActivity(ctx); verr != nil {
			// The clamp already committed. Surface the vacuum failure without
			// discarding a successful pass that may have taken hours.
			return res, fmt.Errorf("clamp committed but vacuum failed: %w", verr)
		}
	}
	return res, nil
}

// ReclaimMigratedActivity deletes Pebble-side activity rows that the SQLite
// cutover has made redundant, freeing space in the main database.
//
// It takes no store parameter on purpose: the Service already holds the store,
// so threading one through the signature would widen the coupling for nothing
// (see the worked example in CLAUDE.md). When the wired store is not the
// migration wrapper — the Pebble-only escape hatch, or a SQLite-open fallback —
// the assertion yields nil and the reclaim reports a refusal rather than
// failing, because "there is no duplicate copy" is an answer, not an error.
func (s *Service) ReclaimMigratedActivity(
	ctx context.Context,
	retain time.Duration,
	dryRun bool,
	onTier database.ActivityReclaimProgress,
) (database.ActivityReclaimResult, error) {
	mig, _ := s.store.(*database.MigratingActivityStore)
	return database.ReclaimMigratedActivity(ctx, mig, retain, dryRun, onTier)
}
