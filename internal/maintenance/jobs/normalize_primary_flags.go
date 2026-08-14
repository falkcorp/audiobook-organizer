// file: internal/maintenance/jobs/normalize_primary_flags.go
// version: 1.0.0
// guid: 4b8e2d19-7c5a-4f60-9d3b-1e6a8c4f2b07
// last-edited: 2026-08-14

package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"golang.org/x/sync/errgroup"
)

func init() { maintenance.Register(&normalizePrimaryFlagsJob{}) }

// normalizePrimaryFlagsJob makes is_primary_version explicit for books where
// the effective value is unambiguous, so every layer reads the same answer.
//
// The 2026-08-14 census (todo.d / #2438) settled the semantics: a nil flag
// means "primary" — the memdb index (effectiveBoolFieldIndex{Default: true})
// and the summary post-filter already agree on that, but any reader that
// derefs the raw *bool without the nil-means-true convention silently flips
// the answer (the author-path divergence). Writing the value explicitly
// removes the convention dependence instead of asking every future reader to
// know it.
//
// Normalization rules — only books OUTSIDE version groups are touched:
//   - nil flag, no version group   -> explicit true  (census: 5,702 books)
//   - false flag, no version group -> explicit true  (census: 41 books; a
//     non-primary version of nothing is incoherent — C314's population)
//   - nil flag, IN a version group -> COUNTED, never written. Group membership
//     means primary-ness is elected (reconcile's elect-missing-primaries owns
//     it); guessing here could mint a second primary. Census says this set is
//     empty today; the count existing at all is the regression alarm.
//   - explicit true, or explicit false inside a group -> untouched.
type normalizePrimaryFlagsJob struct{}

func (j *normalizePrimaryFlagsJob) ID() string       { return "normalize-primary-flags" }
func (j *normalizePrimaryFlagsJob) Name() string     { return "Normalize is_primary_version flags" }
func (j *normalizePrimaryFlagsJob) Category() string { return "maintenance" }
func (j *normalizePrimaryFlagsJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *normalizePrimaryFlagsJob) Description() string {
	return "Write explicit is_primary_version=true for ungrouped books whose flag is nil (effective-true) or incoherently false"
}
func (j *normalizePrimaryFlagsJob) CanResume() bool { return false } // idempotent single pass

func (j *normalizePrimaryFlagsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	// One limit-0 call = one consistent snapshot; offset pages over the async
	// memdb can skip or repeat rows on snapshot swap (see reconcile #2443).
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("list books: %w", err)
	}

	var toFix []database.BookCore
	var nilGrouped, falseUngrouped, nilUngrouped int
	for i := range books {
		b := &books[i]
		grouped := b.VersionGroupID != nil && *b.VersionGroupID != ""
		switch {
		case b.IsPrimaryVersion == nil && !grouped:
			nilUngrouped++
			toFix = append(toFix, *b)
		case b.IsPrimaryVersion == nil && grouped:
			nilGrouped++ // elected, not guessed — see doc comment
		case !*b.IsPrimaryVersion && !grouped:
			falseUngrouped++
			toFix = append(toFix, *b)
		}
	}

	slog.Info("normalize-primary-flags: classified",
		"books", len(books), "nil_ungrouped", nilUngrouped,
		"false_ungrouped", falseUngrouped, "nil_grouped_left_for_election", nilGrouped,
		"dry_run", dryRun)

	if dryRun {
		slog.Info("normalize-primary-flags: DRY RUN complete",
			"would_write", len(toFix), "dry_run", true)
		return nil
	}

	reporter.SetTotal(len(toFix))
	var written, errCount int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(runtime.NumCPU(), 1))
	for i := range toFix {
		b := toFix[i]
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			// Hydrate the full row before write-back: the Core projection is
			// slim, and UpdateBook persists the whole struct (same pattern as
			// reconcile's elect-missing-primaries).
			full, herr := store.GetBookByID(b.ID)
			if herr != nil || full == nil {
				slog.Warn("normalize-primary-flags: hydrate failed", "book", b.ID, "err", herr)
				atomic.AddInt64(&errCount, 1)
				return nil
			}
			t := true
			full.IsPrimaryVersion = &t
			if _, uerr := store.UpdateBook(full.ID, full); uerr != nil {
				slog.Warn("normalize-primary-flags: write failed", "book", full.ID, "err", uerr)
				atomic.AddInt64(&errCount, 1)
				return nil
			}
			atomic.AddInt64(&written, 1)
			reporter.Increment()
			return nil
		})
	}
	if werr := g.Wait(); werr != nil {
		return werr
	}

	slog.Info("normalize-primary-flags: complete",
		"written", written, "errors", errCount,
		"nil_ungrouped", nilUngrouped, "false_ungrouped", falseUngrouped,
		"nil_grouped_left_for_election", nilGrouped)
	if errCount > 0 {
		return fmt.Errorf("normalize-primary-flags: %d of %d writes failed", errCount, len(toFix))
	}
	return nil
}
