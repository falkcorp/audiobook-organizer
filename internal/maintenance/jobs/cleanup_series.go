// file: internal/maintenance/jobs/cleanup_series.go
// version: 2.17.0
// guid: a1000002-0000-0000-0000-000000000002
// last-edited: 2026-11-18

package jobs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"log/slog"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&cleanupSeriesJob{}) }

// csLog carries the conditional delete's diagnostics, which name series, through
// the log-injection barrier (a series name is user data). The job's older direct
// slog calls are on the guard's ratchet and stay at their count.
var csLog = logger.New("maintenance.cleanup-series")

// ErrCleanupSeriesStopped is wrapped by the error Run returns when a live run
// stopped because the store cannot count references to ANY series right now (a
// memdb that is not serving or is missing rows: see csStoreWide). The job left
// work undone, so the op must show as failed; the counts and the warn line are
// logged first, so the failed op still says how far it got. The store's own
// error is wrapped alongside it. A delete that failed for a reason about one row
// (one that will not decode) is not a stop: it is counted as failed and the run
// goes on.
var ErrCleanupSeriesStopped = errors.New("cleanup-series stopped: the store cannot count references to a series")

// csStoreWide reports whether a DeleteSeriesIfUnreferenced error says the store
// cannot count references to any series, so every later delete would fail the
// same way: a memdb known to be missing rows, or one that is not serving. Any
// other error (a series row that will not decode, a failed write) is about one
// row, and the next row may well be fine.
func csStoreWide(err error) bool {
	return errors.Is(err, database.ErrMemdbIncomplete) || errors.Is(err, database.ErrSeriesDeleteMemdbNotServing)
}

type cleanupSeriesJob struct{}

func (j *cleanupSeriesJob) ID() string       { return "cleanup-series" }
func (j *cleanupSeriesJob) Name() string     { return "Cleanup Series" }
func (j *cleanupSeriesJob) Category() string { return "library" }
func (j *cleanupSeriesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *cleanupSeriesJob) Description() string {
	return "Remove 1-book series and merge duplicate series"
}
func (j *cleanupSeriesJob) CanResume() bool { return false }

func (j *cleanupSeriesJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	// Both phases end in a series-row delete, and both decide to delete from
	// membership and reference counts read below, once, at the start of the run.
	// A book linked into a series after that read would be left naming a deleted
	// series, so the delete is the store's atomic count-and-delete
	// (DeleteSeriesIfUnreferenced), not a plain DeleteSeries.
	//
	// That method is a capability outside database.Store (see
	// database.SeriesUnreferencedDeleter), resolved through any store decorator.
	// Fails CLOSED and BEFORE any read or write: a store without it cannot
	// delete a series safely, and there is no fallback to the plain delete. A dry
	// run writes nothing and so does not need it.
	var writer cleanupSeriesStore
	if !dryRun {
		deleter, ok := database.AsCapability[database.SeriesUnreferencedDeleter](store)
		if !ok {
			return fmt.Errorf("cleanup-series refusing to run: store %T cannot delete a series only if nothing references it "+
				"(database.SeriesUnreferencedDeleter)", store)
		}
		writer = cleanupSeriesStore{seriesBookModifier: store, SeriesUnreferencedDeleter: deleter}

		// A store that HAS the delete but cannot count right now (its memdb is not
		// serving, or is known to be missing rows) refuses every delete, and phase 1
		// unlinks a series' books BEFORE it deletes. Asked once, here, before any
		// read or write, so such a store is refused the way a store without the
		// delete is, with nothing touched. It is a snapshot: the in-loop stop on the
		// first store-wide refusal below is the backstop for a store that degrades
		// later.
		ready, ok := database.AsCapability[database.SeriesDeleteReadiness](store)
		if !ok {
			return fmt.Errorf("cleanup-series refusing to run: store %T cannot say whether it can count references to a series "+
				"(database.SeriesDeleteReadiness)", store)
		}
		if err := ready.SeriesDeleteReady(); err != nil {
			return fmt.Errorf("cleanup-series refusing to run: the store cannot count references to a series right now, "+
				"so no delete could be verified; nothing was read or written: %w", err)
		}
	}

	allSeries, err := store.GetAllSeries()
	if err != nil {
		return fmt.Errorf("failed to list series: %w", err)
	}

	bookCounts, err := store.GetAllSeriesBookCounts()
	if err != nil {
		return fmt.Errorf("failed to get series book counts: %w", err)
	}

	// bookCounts above is the DISPLAY count: GetAllSeriesBookCounts skips
	// trashed and non-primary rows, which is right for a badge and wrong as an
	// existence test. Both phases below delete series rows, so they also need
	// the UNFILTERED count -- "is anything still pointing at this?" is a
	// different question from "how many books should I show".
	//
	// Using the filtered count as an existence test is what produced the 6,893
	// phantom series IDs held by 13,322 live books recorded in
	// database/series_bookref.go. This job runs unattended, so the damage
	// accumulates silently.
	//
	// Fails CLOSED: a store that cannot answer aborts the job rather than
	// falling back to bookCounts.
	refCounts, err := database.SeriesRefCounts(store)
	if err != nil {
		return fmt.Errorf("cleanup-series refusing to run without unfiltered reference counts: %w", err)
	}

	// Complete (AllVersions) membership of every series, read ONCE for the
	// whole job instead of once per series inside both loops below
	// (SERIES-MERGE-PERSERIES-SCAN-COST). With a tainted memdb each per-series
	// read was a full "book:" Pebble scan; this is one.
	//
	// Kept current, not re-read: every successful unlink (phase 1) and repoint
	// (phase 2) calls members.Move, so a later read of a series this job has
	// already touched sees what a fresh read would. The case that makes this
	// load-bearing: a phase-1 unlink that fails part-way leaves the series row
	// (it is not in phase1Settled), so phase 2 can meet it as a merged-from series.
	// Without the Move, phase 2 would re-home the books phase 1 had ALREADY
	// unlinked onto the keeper -- a write no fresh read would ever have made.
	//
	// Fails CLOSED like refCounts: no membership, no job.
	seriesIDs := make([]int, len(allSeries))
	for i, ser := range allSeries {
		seriesIDs[i] = ser.ID
	}
	members, err := database.SeriesMembershipAllVersions(store, seriesIDs)
	if err != nil {
		return fmt.Errorf("cleanup-series refusing to run without series membership: %w", err)
	}

	reporter.SetTotal(len(allSeries))

	// Phase 1: single-book series
	var singleApplied, singleFound, singleSkipped, singleBooksSkipped, singleBooksHeld int
	// singleKept: single-book series whose row the conditional delete left in
	// place (a book linked in since the read, a row changed since, already gone,
	// or a delete the store could not answer). Separate from singleSkipped, which
	// is the start-of-run reference guard: a different cause with a different
	// remedy.
	var singleKept csKept
	// singleUnlinkFailed: series whose unlink failed part-way, so the delete was
	// never attempted and the row stays. In no singleKept bucket: the store was
	// never asked. With the buckets above, in a run that wrote, singleFound =
	// singleApplied + singleKept (all five) + singleUnlinkFailed +
	// singleNotReached. (A dry run finds candidates and applies none, so none of
	// the buckets fill and the identity does not hold. singleSkipped, the
	// start-of-run guard, is counted before singleFound and is in no bucket.)
	var singleUnlinkFailed int
	// singleNotReached: candidates the selection found after a store-wide refusal
	// stopped phase 1. Their books were not touched. Counted by finishing the
	// selection (reads only) instead of leaving the loop.
	var singleNotReached int
	// singleHeld: one-book series the row read at the start of the run says are
	// held (HeldBy set). A start-of-run skip like singleSkipped: nothing was
	// written, the book keeps its link, and it is in no singleKept bucket (that
	// bucket is the race where the hold lands AFTER the read, which only the
	// conditional delete can see). Counted before singleFound, so the identity
	// above is unchanged, and in dry runs too: a dry run must not report a held
	// series as a candidate.
	var singleHeld int
	// phase1Settled: series phase 1 made a delete decision about (deleted, kept
	// or already gone). Phase 2 must not group them: their books are already
	// unlinked, so its start-of-run reference count would refuse them again and
	// count them a second time, under a cause that is not the real one. Series
	// whose unlink failed (csNotAttempted) are NOT settled: they stay grouped, with
	// the hoisted map updated by the unlinks that did succeed. A series skipped as
	// held is settled too, though phase 1 decided nothing about its row: left out,
	// it would enter phase 2's groups, and a name twin would make phase 2 re-home
	// its book before the conditional delete refused it (the same unlink-then-
	// refused defect), or pick it as the keeper.
	phase1Settled := make(map[int]bool)
	// phase1Stopped: the store cannot count references to any series (csStoreWide),
	// so phase 1 stopped writing instead of unlinking the books of every remaining
	// candidate for deletes that cannot happen, and phase 2 (which re-homes books
	// before it deletes) did not run. The loop still finishes the selection, to
	// count what it did not reach.
	phase1Stopped := false
	// stopCause is the store's error from the delete that stopped the run, for the
	// error Run returns once the counts are logged.
	var stopCause error

	for _, ser := range allSeries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		count := bookCounts[ser.ID]
		if count != 1 {
			reporter.Increment()
			continue
		}

		// Core for CANDIDATE SELECTION: "is this a one-book series?" is a
		// question about distinct books, and alternate rips of the same book must
		// not make a 1-book series look like a 4-book one.
		books, bErr := store.GetBooksBySeriesIDCore(ser.ID)
		if bErr != nil || len(books) == 0 {
			reporter.Increment()
			continue
		}
		book := books[0]
		if book.SeriesSequence != nil && *book.SeriesSequence > 1 {
			reporter.Increment()
			continue
		}

		// AllVersions for the WRITE SET: every row that references this series
		// has to be unlinked before the row is deleted, alternate rips included.
		// Selection and mutation legitimately ask different questions here, which
		// is why this reads a different getter rather than replacing the one
		// above.
		// From the hoisted map; see members above.
		unlink := members[ser.ID]
		if len(unlink) == 0 {
			reporter.Increment()
			continue
		}

		// The filtered count says one book, so this looks like a 1-book series
		// worth collapsing. Only act if the UNFILTERED count agrees -- but the
		// comparison is now against what we are actually about to unlink, not
		// against 1. Against 1, a series with one primary and three non-primary
		// versions refused forever, because refCounts counts all four while the
		// listing getter showed one. Those four are now all unlinked, so the
		// guard fires only on rows this run still cannot see: trashed books
		// (both series getters skip soft-deleted rows) or rows the memdb counts
		// and Pebble can no longer hydrate.
		if refCounts[ser.ID] > len(unlink) {
			// Counted and reported through the reporter, not just slog. A
			// skip here is a series this job DECLINED to collapse; if it were
			// only incremented like the thousands of non-candidates above,
			// the summary could not distinguish "the guard saved a series"
			// from "nothing matched", and the guard would be invisible in the
			// job output an operator actually reads.
			singleSkipped++
			reporter.Log("warn", fmt.Sprintf(
				"Kept 1-book series %d (%q): %d books reference it but only %d can be unlinked "+
					"(trashed or unhydratable rows would have been stranded)",
				ser.ID, ser.Name, refCounts[ser.ID], len(unlink)), nil)
			reporter.Increment()
			continue
		}

		if database.SeriesHeld(ser) {
			singleHeld++
			phase1Settled[ser.ID] = true
			reporter.Log("warn", fmt.Sprintf(
				"Kept 1-book series %d (%q): it is held (%s), so its book was not unlinked",
				ser.ID, ser.Name, ser.HeldBy), nil)
			reporter.Increment()
			continue
		}

		singleFound++
		if phase1Stopped {
			// A store-wide refusal stopped the writes. This candidate was found by
			// the selection above (reads only) and is counted, not touched.
			singleNotReached++
			reporter.Increment()
			continue
		}
		if !dryRun {
			res, applyErr := csUnlinkAndDeleteSeries(writer, unlink, ser, members)
			singleBooksSkipped += res.Skipped
			singleBooksHeld += res.BooksHeld
			switch {
			case applyErr != nil && res.Outcome == csNotAttempted:
				// An unlink failed, so the delete was never attempted and the row
				// stays by design. Its own bucket, and it stays in phase 2's groups.
				// refCounts is the start-of-run count; like members it is kept
				// current for a series phase 1 touched: the books this phase
				// verifiably took off the series (unlinked, or found already filed
				// elsewhere) are no longer references, so phase 2's guard must not
				// count them as stranded when it re-homes the rest.
				singleUnlinkFailed++
				refCounts[ser.ID] -= res.Unlinked + res.Skipped
				csLog.Error("Failed to remove 1-book series %d (%q): %v", ser.ID, ser.Name, applyErr)
			case applyErr != nil && csStoreWide(applyErr):
				// Every unlink succeeded and the store cannot count references to
				// any series. It will not answer the next delete either, so stop
				// writing before unlinking more books for deletes that cannot
				// happen. Settled like any series phase 1 decided about: phase 2
				// does not run its group, and it must not count it again.
				singleKept.add(res.Outcome)
				phase1Settled[ser.ID] = true
				phase1Stopped = true
				stopCause = applyErr
				csLog.Error("Failed to remove 1-book series %d (%q): %v", ser.ID, ser.Name, applyErr)
			case applyErr != nil:
				// Every unlink succeeded and the delete failed for a reason about
				// this row (one that will not decode, a failed write), not about the
				// store: the next series may be fine. Counted failed, logged with its
				// real cause, and the run goes on. Settled: its books are unlinked,
				// so phase 2's start-of-run count would refuse it a second time.
				singleKept.add(res.Outcome)
				phase1Settled[ser.ID] = true
				csLog.Error("Failed to remove 1-book series %d (%q), kept, carrying on: %v", ser.ID, ser.Name, applyErr)
			case res.Outcome == csDeleted:
				phase1Settled[ser.ID] = true
				singleApplied++
			default:
				// The books were unlinked but the row stayed: the conditional delete
				// would not remove it. Counted and reported through the reporter, not
				// only slog, like the start-of-run guard above. Settled: the books
				// are already unlinked and the hoisted map says so, so phase 2 has
				// nothing to merge from it and its reference count (taken before
				// the unlinks) would only refuse it a second time.
				singleKept.add(res.Outcome)
				phase1Settled[ser.ID] = true
				reporter.Log("warn", csKeptLine(ser, res.Outcome), nil)
			}
		}
		reporter.Increment()
	}

	// Phase 2: duplicate series by normalized name
	normGroups := make(map[string][]database.Series)
	for _, ser := range allSeries {
		if phase1Settled[ser.ID] {
			continue
		}
		key := csNormalizeSeriesName(ser.Name)
		normGroups[key] = append(normGroups[key], ser)
	}

	var dupApplied, dupFound, dupRefused, dupBooksSkipped, dupBooksHeld int
	var dupKept csKept // merged-from series the conditional delete left in place; see singleKept
	// Per merged-from series, so the Done line adds up: dupMergeFrom = dupDeleted +
	// dupRefused + dupKept (all five) + dupNotReached, in a run that wrote.
	// dupNotReached is the series of a group left unprocessed: by an error that
	// stopped the group, or because a store-wide refusal had stopped the run
	// (dupGroupsNotReached counts those groups).
	var dupMergeFrom, dupDeleted, dupNotReached, dupGroupsNotReached int
	phase2Stopped := false
	for normName, group := range normGroups {
		if len(group) < 2 {
			continue
		}
		dupFound++
		if phase1Stopped || phase2Stopped {
			// Found, not touched: a store-wide refusal stopped the run. Counted
			// instead of left out, so the Done line still adds up.
			n := len(group) - 1
			dupMergeFrom += n
			dupNotReached += n
			dupGroupsNotReached++
			continue
		}

		keepIdx := 0
		for i, ser := range group {
			if bookCounts[ser.ID] > bookCounts[group[keepIdx].ID] {
				keepIdx = i
			}
		}
		keeper := group[keepIdx]

		// The rows, not just their ids: the conditional delete compares each one's
		// name and author with the live row, so it needs what this run read.
		var mergeFrom []database.Series
		for i, ser := range group {
			if i != keepIdx {
				mergeFrom = append(mergeFrom, ser)
			}
		}
		dupMergeFrom += len(mergeFrom)

		if !dryRun {
			res, mergeErr := csMergeSeriesGroup(writer, keeper.ID, mergeFrom, refCounts, members)
			dupRefused += res.Refused
			dupBooksSkipped += res.Skipped
			dupBooksHeld += res.BooksHeld
			dupKept.LockHeld += res.Kept.LockHeld
			dupKept.Referenced += res.Kept.Referenced
			dupKept.Changed += res.Kept.Changed
			dupKept.Gone += res.Kept.Gone
			dupKept.Failed += res.Kept.Failed
			dupDeleted += res.Merged
			dupNotReached += res.NotReached
			if res.Stopped {
				// The store cannot count references to any series: the same stop as
				// phase 1. Later groups would re-home their books and then meet the
				// same refusal.
				phase2Stopped = true
				stopCause = mergeErr
			}
			switch {
			case mergeErr != nil:
				slog.Error("Failed to merge series group", "normName", normName, "err", mergeErr)
			case res.Merged > 0:
				// Only a group in which at least one series row was actually
				// removed counts as applied. Previously a group where EVERY
				// merge was refused still incremented dupApplied, so the
				// summary reported merges that did not happen.
				dupApplied++
			}
		}
	}

	var kept []string
	if singleSkipped > 0 || dupRefused > 0 {
		kept = append(kept, fmt.Sprintf(
			"Reference guard kept %d single-book series and %d merged-from series holding rows this run "+
				"could not reach (trashed, or counted but unhydratable); rerun once those rows are visible",
			singleSkipped, dupRefused))
	}
	if singleHeld > 0 {
		kept = append(kept, fmt.Sprintf(
			"%d single-book series were held when this run started and were skipped before any write "+
				"(their books keep the link); release the hold to let a rerun settle them",
			singleHeld))
	}
	if singleKept.Referenced > 0 || dupKept.Referenced > 0 {
		kept = append(kept, fmt.Sprintf(
			"the conditional delete found live references in %d single-book series and %d merged-from series "+
				"(a book was linked in after this run read membership); kept, rerun to settle them",
			singleKept.Referenced, dupKept.Referenced))
	}
	if singleKept.Changed > 0 || dupKept.Changed > 0 {
		kept = append(kept, fmt.Sprintf(
			"the conditional delete kept %d single-book series and %d merged-from series whose row was renamed, "+
				"re-authored or held after this run read it",
			singleKept.Changed, dupKept.Changed))
	}
	if singleKept.Gone > 0 || dupKept.Gone > 0 {
		kept = append(kept, fmt.Sprintf(
			"%d single-book series and %d merged-from series were already deleted by someone else before this run's delete",
			singleKept.Gone, dupKept.Gone))
	}
	if singleKept.Failed > 0 || dupKept.Failed > 0 {
		kept = append(kept, fmt.Sprintf(
			"the conditional delete could not verify %d single-book series and %d merged-from series unreferenced "+
				"(it returned an error: the cause of each is in the error log); kept",
			singleKept.Failed, dupKept.Failed))
	}
	if singleKept.LockHeld > 0 || dupKept.LockHeld > 0 {
		kept = append(kept, fmt.Sprintf(
			"%d single-book series and %d merged-from series were kept because a book the user locked to that series "+
				"(series_name) still names it; unlock the series on those books to let a rerun settle them",
			singleKept.LockHeld, dupKept.LockHeld))
	}
	if singleUnlinkFailed > 0 {
		kept = append(kept, fmt.Sprintf(
			"%d single-book series could not be unlinked (a book write failed); the series stay, see the error log",
			singleUnlinkFailed))
	}
	if dupNotReached > 0 {
		kept = append(kept, fmt.Sprintf(
			"%d merged-from series were not processed: an error stopped their group, or the run; see the error log",
			dupNotReached))
	}
	if phase1Stopped || phase2Stopped {
		phase := "phase 1 (single-book series)"
		rest := "no further books were unlinked and the merge phase did not run"
		if phase2Stopped {
			phase = "phase 2 (duplicate merge)"
			rest = "no further groups were merged"
		}
		kept = append(kept, fmt.Sprintf(
			"stopped in %s at the first delete the store could not verify (it cannot count references to a series): %s; "+
				"%d single-book series and %d duplicate groups were not reached; rerun once the store can count references",
			phase, rest, singleNotReached, dupGroupsNotReached))
	}
	if len(kept) > 0 {
		reporter.Log("warn", strings.Join(kept, "; "), nil)
	}
	if singleBooksSkipped > 0 || dupBooksSkipped > 0 {
		reporter.Log("warn", fmt.Sprintf(
			"Skipped %d books in single-book cleanup and %d books in series merges: "+
				"each no longer named the series being cleaned when it was written "+
				"(another writer moved it after this run read membership)",
			singleBooksSkipped, dupBooksSkipped), nil)
	}
	if singleBooksHeld > 0 || dupBooksHeld > 0 {
		reporter.Log("warn", fmt.Sprintf(
			"Left %d books in single-book cleanup and %d books in series merges on their series: "+
				"the user locked the series (series_name) on each, so this run did not move them",
			singleBooksHeld, dupBooksHeld), nil)
	}
	slog.Info("Done single_found single_applied single_skipped dup_groups_found dup_applied dup_refused dryRun",
		"singleFound", singleFound, "singleApplied", singleApplied, "singleSkipped", singleSkipped,
		"singleHeld", singleHeld,
		"singleBooksSkipped", singleBooksSkipped, "dupBooksSkipped", dupBooksSkipped,
		"singleBooksLockHeld", singleBooksHeld, "dupBooksLockHeld", dupBooksHeld,
		"singleKeptLockHeld", singleKept.LockHeld, "dupKeptLockHeld", dupKept.LockHeld,
		"singleRefusedLive", singleKept.Referenced, "dupRefusedLive", dupKept.Referenced,
		"singleKeptChanged", singleKept.Changed, "dupKeptChanged", dupKept.Changed,
		"singleAlreadyGone", singleKept.Gone, "dupAlreadyGone", dupKept.Gone,
		"singleDeleteFailed", singleKept.Failed, "dupDeleteFailed", dupKept.Failed,
		"singleUnlinkFailed", singleUnlinkFailed, "dupNotReached", dupNotReached,
		"singleNotReached", singleNotReached, "dupGroupsNotReached", dupGroupsNotReached,
		"dupMergeFromSeries", dupMergeFrom, "dupSeriesDeleted", dupDeleted,
		"stoppedPhase1", phase1Stopped, "stoppedPhase2", phase2Stopped,
		"dupFound", dupFound, "dupApplied", dupApplied, "dupRefused", dupRefused, "dryRun", dryRun)
	if phase1Stopped || phase2Stopped {
		// After the counts and the warn line, so a failed op still reports them.
		phase := "phase 1 (single-book series)"
		if phase2Stopped {
			phase = "phase 2 (duplicate merge)"
		}
		return fmt.Errorf("%w: in %s: %w", ErrCleanupSeriesStopped, phase, stopCause)
	}
	return nil
}

// csDeleteOutcome is what the conditional delete of one series row came to.
type csDeleteOutcome int

const (
	// csNotAttempted is the zero value: no delete was made (an earlier unlink
	// failed, or there was nothing to unlink).
	csNotAttempted csDeleteOutcome = iota
	// csDeleteFailed: the delete returned an error: a store that cannot count
	// (a memdb that is not serving or is missing rows refuses rather than guess:
	// csStoreWide), or a failure about this row (it will not decode, the write
	// failed). Returned with an error; the series stays.
	csDeleteFailed
	// csDeleted: the row is gone, deleted by this call.
	csDeleted
	// csKeptReferenced: a book row names the series NOW. It was linked in after
	// the membership and counts this run read. The series stays.
	csKeptReferenced
	// csKeptChanged: the row's name or author is not what this run read
	// (renamed since), or it is held by a repair. Not this run's row to delete.
	csKeptChanged
	// csAlreadyGone: there is no row any more; someone else deleted it first.
	csAlreadyGone
	// csKeptLockHeld: a book the user locked to the series (series_name) still
	// names it, so this run did not move that book and did not ask the store to
	// delete the row. The books it could move were moved; the series stays.
	csKeptLockHeld
)

// csKept counts the series rows the conditional delete left in place, by why.
// Failed is the rows whose delete returned an error: the store could not say
// whether anything references them, so they stayed. LockHeld is the rows the
// delete was never asked about, because a user-locked book still names them.
type csKept struct{ Referenced, Changed, Gone, Failed, LockHeld int }

func (k *csKept) add(o csDeleteOutcome) {
	switch o {
	case csKeptReferenced:
		k.Referenced++
	case csKeptChanged:
		k.Changed++
	case csAlreadyGone:
		k.Gone++
	case csDeleteFailed:
		k.Failed++
	case csKeptLockHeld:
		k.LockHeld++
	}
}

// csKeptLine is the job-log line for a 1-book series whose row the conditional
// delete did not remove.
func csKeptLine(ser database.Series, o csDeleteOutcome) string {
	switch o {
	case csKeptReferenced:
		return fmt.Sprintf("Kept 1-book series %d (%q): a book was linked into it after this run read membership "+
			"(the conditional delete found live references)", ser.ID, ser.Name)
	case csKeptChanged:
		return fmt.Sprintf("Kept 1-book series %d (%q): it was renamed, re-authored or held after this run read it", ser.ID, ser.Name)
	case csAlreadyGone:
		return fmt.Sprintf("1-book series %d (%q) was already deleted by someone else; nothing left to remove", ser.ID, ser.Name)
	case csKeptLockHeld:
		return fmt.Sprintf("Kept 1-book series %d (%q): a book the user locked to it (series_name) still names it", ser.ID, ser.Name)
	}
	return fmt.Sprintf("1-book series %d (%q) was not deleted", ser.ID, ser.Name)
}

// csDeleteSeriesIfUnreferenced deletes ser's row only if no book names it at the
// moment of the delete, mapping the store's answers the way series-denumber does:
//
//   - deleted: csDeleted.
//   - not gone (nil error): a book references it now, so the row stays
//     (csKeptReferenced).
//   - ErrSeriesNotFound: csAlreadyGone.
//   - ErrSeriesChanged or ErrSeriesHeld: csKeptChanged, the row stays.
//   - any other error: fail CLOSED. The row stays, csDeleteFailed with the error,
//     and there is no fallback to the plain DeleteSeries.
//
// ser is the row this run read; its name and author are compared with the live
// row under the series lock, so a series renamed since is somebody's edit this
// job does not delete over.
func csDeleteSeriesIfUnreferenced(del database.SeriesUnreferencedDeleter, ser database.Series) (csDeleteOutcome, error) {
	gone, err := del.DeleteSeriesIfUnreferenced(ser.ID, ser.Name, ser.AuthorID)
	switch {
	case errors.Is(err, database.ErrSeriesNotFound):
		csLog.Info("cleanup-series: series %d was already deleted by someone else", ser.ID)
		return csAlreadyGone, nil
	case errors.Is(err, database.ErrSeriesChanged), errors.Is(err, database.ErrSeriesHeld):
		csLog.Warn("cleanup-series: not deleting series %d (%q): it was renamed, re-authored or held after this run read it: %v",
			ser.ID, ser.Name, err)
		return csKeptChanged, nil
	case err != nil:
		return csDeleteFailed, err
	case !gone:
		csLog.Warn("cleanup-series: not deleting series %d (%q): a book was linked into it after this run read membership",
			ser.ID, ser.Name)
		return csKeptReferenced, nil
	}
	return csDeleted, nil
}

// csUnlinkResult is what csUnlinkAndDeleteSeries did: the books it left alone
// and what became of the series row.
type csUnlinkResult struct {
	// Unlinked is the number of books whose SeriesID this call cleared.
	Unlinked int
	// Skipped is the number of books left alone because their row no longer
	// named the series.
	Skipped int
	// BooksHeld is the number of books left on the series because the user
	// locked it on them (series_name). They still name the series, so the delete
	// is not attempted (Outcome csKeptLockHeld).
	BooksHeld int
	// Outcome is the conditional delete's result: csNotAttempted when an unlink
	// failed first, csDeleteFailed (with an error) when the store could not
	// answer.
	Outcome csDeleteOutcome
}

// csUnlinkAndDeleteSeries unlinks EVERY passed-in row from ser and only then
// deletes the series row, and only if no book names it at that moment.
//
// It takes a slice rather than a single book because the caller now hands it the
// complete set (AllVersions), not just the one book the listing getter showed.
// Unlinking one of four and deleting the row is precisely the stranding this
// job's guard existed to refuse.
//
// Only book.ID is read from each passed-in row (BookCore is sufficient — see
// caller); the writeback goes through ModifyBook, which re-reads the full row
// under the book's write lock and clears only SeriesID and SeriesSequence, so
// no heavy-field fidelity is lost and a column another writer commits
// meanwhile is not reverted (audit A1#15).
//
// The delete is DeleteSeriesIfUnreferenced, not DeleteSeries: it counts the
// rows that name the series and deletes under every book write lock, so a book
// another writer linked into the series after this run read its membership
// makes the delete refuse (csKeptReferenced) instead of leaving that book naming
// a deleted series. The refusal is a result, not an error: the unlinks that
// happened stay (strictly an improvement) and the row is left for a later run.
//
// Fail-closed and ordered: the delete happens only after every unlink has
// SUCCEEDED. Returning early on the first failure leaves the series row in
// place, which is the recoverable state -- the rows still point at a series
// that still exists, and a later run retries. Deleting after a partial unlink
// would strand exactly the rows that failed.
//
// A book whose user locked its series (series_name) is not unlinked: the lock is
// read under the same hold as the write (database.ModifyBookRespectingLocks) and
// an unreadable lock is an error that stops the unlinks like any failed write.
// A held book still names the series, so the delete is not attempted
// (csKeptLockHeld) and the books that could move have moved.
//
// members is the job's hoisted membership map; each successful unlink is
// recorded in it so a partial failure leaves the map matching the store.
//
// Each write is a compare-and-set on the series the book is being removed from:
// a book whose row no longer names seriesID (another writer moved it after the
// membership was read) is left alone, counted in the returned skipped, and
// recorded in members where it really is. A skipped book does not block the
// delete: it verifiably does not reference the series.
func csUnlinkAndDeleteSeries(store seriesUnlinker, books []database.BookCore, ser database.Series, members database.SeriesBooksMap) (csUnlinkResult, error) {
	seriesID := ser.ID
	if len(books) == 0 {
		// Deleting here would be a delete with no unlink at all. The caller
		// already skips an empty set; this is the second lock on that door.
		return csUnlinkResult{}, fmt.Errorf("refusing to delete series %d: no books to unlink", seriesID)
	}
	skipped, unlinked, heldBooks := 0, 0, 0
	for i := range books {
		id := books[i].ID
		// Compare-and-set. The books come from membership read once at the start
		// of the run, so by now another writer may have filed this one in a
		// different series (series-denumber moves "Batman 03" into "Batman").
		// Clearing SeriesID then would erase that writer's link. The check
		// runs on the row ModifyBook just re-read under the book's lock, so it
		// is not itself a stale read.
		var movedTo *int
		stale := false
		written, held, err := database.ModifyBookRespectingLocks(store, store, id, func(current *database.Book) error {
			if current.SeriesID == nil || *current.SeriesID != seriesID {
				stale = true
				if current.SeriesID != nil {
					to := *current.SeriesID
					movedTo = &to
				}
				return database.ErrSkipBookWrite
			}
			current.SeriesID = nil
			current.SeriesSequence = nil
			return nil
		})
		if err != nil {
			return csUnlinkResult{Unlinked: unlinked, Skipped: skipped, BooksHeld: heldBooks}, fmt.Errorf("ModifyBook(%s): %w", id, err)
		}
		if len(held) > 0 {
			// The user locked this book's series. It still names seriesID, so it
			// stays in members and the delete below is not made.
			heldBooks++
			csLog.Warn("cleanup-series: leaving book %s on series %d: locked by the user (%s)", id, seriesID, strings.Join(held, ", "))
			continue
		}
		if written == nil {
			return csUnlinkResult{Unlinked: unlinked, Skipped: skipped, BooksHeld: heldBooks}, fmt.Errorf("book %s not found", id)
		}
		if stale {
			// Not ours to write. It no longer references seriesID, so it does not
			// hold the delete back; record where it really is so the hoisted map
			// still matches the store.
			skipped++
			members.Move(id, seriesID, movedTo)
			continue
		}
		members.Move(id, seriesID, nil)
		unlinked++
	}
	if heldBooks > 0 {
		// A book the user locked to the series still names it: the conditional
		// delete would refuse (and report "linked in after this run read it", which
		// is not what happened), so it is not asked. The books that could move have.
		return csUnlinkResult{Unlinked: unlinked, Skipped: skipped, BooksHeld: heldBooks, Outcome: csKeptLockHeld}, nil
	}
	outcome, err := csDeleteSeriesIfUnreferenced(store, ser)
	if err != nil {
		return csUnlinkResult{Unlinked: unlinked, Skipped: skipped, Outcome: csDeleteFailed}, fmt.Errorf("DeleteSeriesIfUnreferenced: %w", err)
	}
	return csUnlinkResult{Unlinked: unlinked, Skipped: skipped, Outcome: outcome}, nil
}

// csMergeSeriesGroup folds the mergeFrom series into keepID. refCounts is the
// UNFILTERED seriesID -> book-count map; it is required, not optional, because the
// reassignment loop below reads membership from the hoisted map, which hides
// trashed rows. Deleting a series whose unfiltered count exceeds what was
// reassigned strands those rows on a series ID that no longer resolves.
//
// A series is deleted only after TWO checks. The refCounts guard below is the
// early one: it reads counts taken at the start of the run and refuses with a
// clear count of the rows this run could not reach. The delete itself is
// DeleteSeriesIfUnreferenced, which counts and deletes under every book write
// lock: refCounts cannot see a book another writer linked into the series after
// it was read, and the conditional delete can.
//
// It returns a csMergeResult: Merged is the number of series rows actually
// deleted, Refused the number kept back by the refCounts guard, Skipped the
// number of books left alone because their row no longer names the series they
// were listed under (another writer moved them since the membership was read;
// the repoint is a compare-and-set on that series, so it never pulls a book onto
// the keeper from a third series; a skipped book verifiably does not reference
// the series, so it counts toward the rows the guard accounts for), and Kept the
// rows the conditional delete left in place: Referenced is a series a book was
// linked into after the read (a separate count from Refused: a different cause),
// Changed one renamed, re-authored or held since, Gone one already deleted by
// someone else. The caller needs all of these -- a group in which every merge was
// refused is not a completed merge, and reporting it as one is how a refusal
// becomes invisible.
//
// Each series is settled on its own: a series the conditional delete keeps does
// not stop the next one, and neither does a delete that failed for a reason about
// that row (Kept.Failed, logged, carried on). A store that cannot count references
// to any series (csStoreWide) stops the group and returns what was settled so far
// with the error and Stopped set; the series stays, and the ones after it are
// counted in NotReached.
//
// mergeFrom carries the rows, not just their ids, because the conditional delete
// compares each row's name and author with the live one.
//
// members is the caller's hoisted AllVersions membership map
// (database.SeriesMembershipAllVersions). It replaced a per-fromID
// GetBooksBySeriesIDAllVersions call, which cost a full Pebble scan per series
// once memdb is tainted; seriesMerger no longer carries that method, so a
// per-series read cannot creep back in here without a compile error. Every
// successful repoint is recorded with members.Move.
func csMergeSeriesGroup(store seriesMerger, keepID int, mergeFrom []database.Series, refCounts map[int]int, members database.SeriesBooksMap) (csMergeResult, error) {
	var res csMergeResult
	for idx, from := range mergeFrom {
		fromID := from.ID
		// AllVersions, not Core. This loop repoints every row it is handed and
		// then deletes fromID, so the getter and the refCounts guard below must
		// answer about the SAME population. They did not: refCounts is unfiltered
		// while Core hides non-primary versions, so any series holding an
		// alternate rip failed refCounts-moved > 0 and was refused -- on EVERY
		// run, not once. The guard was not wrong, it was misaligned; aligning the
		// read leaves it firing only on the rows it still cannot see (trashed).
		books := members[fromID]
		// moved counts rows actually reassigned, mirroring the dedup path. The
		// nil-hydration skip below must NOT count -- and the resulting refusal
		// is NOT merely a one-run deferral. GetAllSeriesBookRefCounts prefers
		// the memdb whenever it is warm, which is the prod default, while
		// ModifyBook reads Pebble directly (see
		// internal/books/store/series_bookref.go and pebble_store.go). A row the
		// memdb still holds but Pebble no longer does is therefore counted in
		// refCounts and unhydratable on EVERY run, so this keeps the series row
		// indefinitely rather than for one pass.
		//
		// That is the side to err on. A surviving series row is visible,
		// harmless, and re-cleanable once the memdb is rebuilt; deleting on a
		// count we could not confirm is what stranded 6,893 series IDs across
		// 13,322 books. Pinned by
		// TestCsMergeSeriesGroup_RefusesWhenARowCannotBeHydrated.
		moved, skippedHere, heldHere := 0, 0, 0
		for _, book := range books {
			// Repoint only SeriesID, under the book's write lock, so a column
			// another writer commits meanwhile is not reverted (audit A1#15).
			//
			// Compare-and-set against fromID, on the row ModifyBook re-read under
			// the book's lock: the membership map is a snapshot from the start of
			// the run, and a book another writer has since filed in a third series
			// must not be pulled onto the keeper.
			var movedTo *int
			stale := false
			written, held, err := database.ModifyBookRespectingLocks(store, store, book.ID, func(current *database.Book) error {
				if current.SeriesID != nil && *current.SeriesID == keepID {
					return database.ErrSkipBookWrite
				}
				if current.SeriesID == nil || *current.SeriesID != fromID {
					stale = true
					if current.SeriesID != nil {
						to := *current.SeriesID
						movedTo = &to
					}
					return database.ErrSkipBookWrite
				}
				current.SeriesID = &keepID
				return nil
			})
			if err != nil {
				res.NotReached = len(mergeFrom) - idx
				return res, fmt.Errorf("ModifyBook(%s): %w", book.ID, err)
			}
			if len(held) > 0 {
				// The user locked this book's series: it stays on fromID, so
				// fromID cannot be deleted below.
				heldHere++
				res.BooksHeld++
				csLog.Warn("cleanup-series: leaving book %s on series %d: locked by the user (%s)", book.ID, fromID, strings.Join(held, ", "))
				continue
			}
			if written == nil {
				continue
			}
			if stale {
				// Verified, on the live row, not to reference fromID: it cannot be
				// stranded by deleting it, so it counts toward the accounted rows
				// below. Record where it really is.
				res.Skipped++
				skippedHere++
				members.Move(book.ID, fromID, movedTo)
				continue
			}
			members.Move(book.ID, fromID, &keepID)
			moved++
		}
		if heldHere > 0 {
			// Not Refused: the guard below is about rows this run could not SEE,
			// and these it saw and was told not to move. The books that could move
			// have moved; the row stays because a locked book still names it.
			res.Kept.add(csKeptLockHeld)
			continue
		}
		if stranded := refCounts[fromID] - moved - skippedHere; stranded > 0 {
			// Books were still reassigned above -- that is strictly an
			// improvement and is not rolled back. Only the row removal is
			// refused, leaving a resolvable series ID for the rows we could
			// not see. A later run, or the reconciler, can finish the job once
			// those rows become visible.
			//
			// Reaching here now means something OTHER than a non-primary
			// version: the getter above sees those. What it still cannot see is
			// a trashed row (both series getters skip soft-deleted books) or a
			// row the memdb counts and Pebble can no longer hydrate. Naming
			// "the filtered getter" here would send the next reader to a cause
			// this change removed.
			slog.Warn("Keeping merged-from series row: more books reference it than this run could reassign (trashed or unhydratable)",
				"seriesID", fromID, "keepID", keepID,
				"reassigned", moved, "stranded_if_deleted", stranded)
			res.Refused++
			continue
		}
		// The second lock: the count above is from the start of the run, this one
		// is taken at the delete.
		outcome, err := csDeleteSeriesIfUnreferenced(store, from)
		if err != nil {
			res.Kept.add(csDeleteFailed)
			if !csStoreWide(err) {
				// A failure about this row (it will not decode, the write failed),
				// not about the store: the next series in the group may be fine.
				// Its books are re-homed and the row stays; counted failed and
				// logged with its real cause.
				csLog.Error("cleanup-series: could not delete merged-from series %d (%q), kept, carrying on: %v",
					fromID, from.Name, err)
				continue
			}
			res.Stopped = true
			res.NotReached = len(mergeFrom) - idx - 1
			return res, fmt.Errorf("DeleteSeriesIfUnreferenced(%d): %w", fromID, err)
		}
		if outcome == csDeleted {
			res.Merged++
			continue
		}
		res.Kept.add(outcome)
	}
	return res, nil
}

// csMergeResult is what csMergeSeriesGroup did across one duplicate group.
type csMergeResult struct {
	Merged  int // series rows deleted
	Refused int // series kept by the start-of-run refCounts guard
	Skipped int // books left alone: their row no longer named the series
	// BooksHeld is the books left on their series because the user locked it
	// (series_name) on them; each such series is in Kept.LockHeld.
	BooksHeld int
	Kept      csKept // series the conditional delete left in place, by cause
	// NotReached is the merged-from series of this group an error left
	// unprocessed: the series whose book write failed and every one after it, or
	// every one after the series whose delete failed store-wide (that one is
	// Kept.Failed). With Merged, Refused and Kept, it accounts for every series
	// in mergeFrom.
	NotReached int
	// Stopped is set when a delete failed because the store cannot count
	// references to any series (csStoreWide): the caller stops the run. A delete
	// that failed for a reason about one row is Kept.Failed and the group goes on.
	Stopped bool
}

var csNonAlphanumRE = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)

func csNormalizeSeriesName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.TrimPrefix(s, "the ")
	for _, suffix := range []string{" series", " saga", " trilogy", " duology", " quartet"} {
		if strings.HasSuffix(s, suffix) {
			s = s[:len(s)-len(suffix)]
			break
		}
	}
	s = csNonAlphanumRE.ReplaceAllString(s, " ")
	fields := strings.FieldsFunc(s, unicode.IsSpace)
	return strings.Join(fields, " ")
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *cleanupSeriesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
