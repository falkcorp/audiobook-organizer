// file: internal/dedup/series_prune_core.go
// version: 1.0.0
// guid: 92abb0d6-3922-4a56-a82d-50c26ed23424
// last-edited: 2026-10-11

package dedup

import (
	"fmt"
	"sort"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// The series prune, one group or one orphan at a time.
//
// These are the cores of the nightly "dedup.series-prune" op
// (server.executeSeriesPrune), moved here unchanged so the Repairs fixer of
// the same id (internal/plugins/maintenance/series_prune_fixer.go) applies
// ONE row through the very code the op runs over the whole library. The op is
// a loop over them plus its progress, counters, summary row and cache
// invalidation; it keeps every error string, counter and guard it had.
//
// What the cores do NOT do, on purpose:
//   - read the unfiltered reference counts or the AllVersions membership.
//     The caller reads them once (database.SeriesRefCounts,
//     database.SeriesMembershipAllVersions, both fail-closed) and passes them
//     in. Both resolve the store through database.AsCapability, so a caller
//     handing the cores a wrapper store (the fixer routes its writes through
//     the Repairs Writer) would otherwise miss the capability and refuse.
//   - write the ledger. Every change row goes to the caller's
//     SeriesPruneRecorder AFTER the write it describes, exactly as the op
//     always wrote them; the op stamps its operation id on them, the fixer
//     journals them under its apply op.

// SeriesPruneReader is what the grouping and the canonical vote read.
type SeriesPruneReader interface {
	GetAllSeries() ([]database.Series, error)
	GetBooksBySeriesIDCore(seriesID int) ([]database.BookCore, error)
}

// SeriesPruneWriter is what a group merge and an orphan delete write.
type SeriesPruneWriter interface {
	// No per-series AllVersions read on purpose: the caller hoists the
	// complete membership once through database.SeriesMembershipAllVersions
	// (a capability, resolved through the decorator chain), so a per-series
	// read inside the merge loop cannot come back without widening this
	// interface (SERIES-MERGE-PERSERIES-SCAN-COST).
	DeleteSeries(id int) error
	GetBookByID(id string) (*database.Book, error)
	// ModifyBook, not UpdateBook: the prune repoints one column (SeriesID)
	// and must not revert whatever another writer committed to the row
	// between the hydrate and the write (database.BookMutator.ModifyBook).
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// SeriesPruneStore is what the whole-library prune op calls, measured by
// emptying it and reading the compiler's enumeration. It was an inline
// anonymous interface of database.* embeds (111 methods) in parameter
// position.
type SeriesPruneStore interface {
	SeriesPruneReader
	SeriesPruneWriter
	CreateOperationChange(change *database.OperationChange) error
}

// SeriesPruneRecorder records one ledger row of a prune step, called after
// the write it describes. change carries ChangeType, BookID, FieldName,
// OldValue and NewValue; the recorder fills in the id and the operation. An
// error is collected in the step's result (Unrecorded) and never stops the
// step: the write it describes has already been made.
type SeriesPruneRecorder func(change *database.OperationChange) error

// SeriesGroupKey is what makes series duplicates of each other: the name
// normalised by util.NormalizeString, and the author id (0 for none).
type SeriesGroupKey struct {
	Name     string
	AuthorID int
}

// SeriesGroupKeyOf returns s's group key.
func SeriesGroupKeyOf(s database.Series) SeriesGroupKey {
	aid := 0
	if s.AuthorID != nil {
		aid = *s.AuthorID
	}
	return SeriesGroupKey{Name: util.NormalizeString(s.Name), AuthorID: aid}
}

// DuplicateSeriesGroup is two or more series sharing a group key, in the
// order the series list returned them.
type DuplicateSeriesGroup struct {
	Key     SeriesGroupKey
	Members []database.Series
}

// GroupDuplicateSeries groups all by SeriesGroupKeyOf and returns the groups
// of two or more, sorted by key, and how many held rows it left out.
//
// This is the EXECUTE rule, and it differs from the HTTP preview's
// (computeSeriesPrunePreview) in one way: a held row (database.SeriesHeld)
// is kept for an undo that links its books back, and merging it away would
// delete it, so it is in no group. Its twins still merge among themselves.
func GroupDuplicateSeries(all []database.Series) (groups []DuplicateSeriesGroup, held int) {
	byKey := make(map[SeriesGroupKey][]database.Series)
	var order []SeriesGroupKey
	for _, s := range all {
		if database.SeriesHeld(s) {
			held++
			continue
		}
		key := SeriesGroupKeyOf(s)
		if _, ok := byKey[key]; !ok {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], s)
	}
	for _, key := range order {
		if members := byKey[key]; len(members) >= 2 {
			groups = append(groups, DuplicateSeriesGroup{Key: key, Members: members})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Key.Name != groups[j].Key.Name {
			return groups[i].Key.Name < groups[j].Key.Name
		}
		return groups[i].Key.AuthorID < groups[j].Key.AuthorID
	})
	return groups, held
}

// SeriesVote is the outcome of VoteSeriesCanonical: which member survives,
// and the display (Core) book count of every member it was decided from.
type SeriesVote struct {
	CanonicalIdx int
	CoreCounts   []int
}

// VoteSeriesCanonical picks the member of group that survives the merge:
// most books, then lowest ID.
//
// Core is right here: this is candidate SELECTION, picking which of several
// duplicate series survives. Counting alternate rips would let a series win
// the vote on copies rather than on distinct books. The repoint in
// MergeSeriesGroup reads the complete set; the two getters play different
// roles and that is deliberate.
//
// A failed count DISQUALIFIES the whole group: the error is returned and no
// canonical is chosen. Recording it and carrying on was not enough. A series
// whose count fails to load counts as zero books, so it loses the vote to any
// sibling, and the loser is then DELETED. One transient read error could make
// a 400-book series lose to a 2-book typo of itself: the books survive (they
// get repointed) but the canonical row, its ID and every external reference
// to it are gone, and reversing it means hand-reading the OperationChange
// ledger. Skipping the group costs one deferred merge; getting the vote wrong
// costs a row that cannot be recovered from the summary.
func VoteSeriesCanonical(store SeriesPruneReader, group []database.Series) (SeriesVote, error) {
	vote := SeriesVote{CoreCounts: make([]int, len(group))}
	canonicalBookCount := 0
	for i, s := range group {
		books, err := store.GetBooksBySeriesIDCore(s.ID)
		if err != nil {
			return SeriesVote{}, fmt.Errorf(
				"failed to count books for series %d (%q) while picking which of %d duplicate "+
					"series to keep; REFUSING to merge this group, because an unreadable count "+
					"would silently lose the vote and get the series deleted: %v",
				s.ID, s.Name, len(group), err)
		}
		bc := len(books)
		vote.CoreCounts[i] = bc
		if bc > canonicalBookCount || (bc == canonicalBookCount && s.ID < group[vote.CanonicalIdx].ID) {
			vote.CanonicalIdx = i
			canonicalBookCount = bc
		}
	}
	return vote, nil
}

// SeriesGroupMergeResult is what MergeSeriesGroup did (SeriesMergeResult is
// the dedup.series-merge op's).
type SeriesGroupMergeResult struct {
	// Merged counts the merged-away series rows deleted.
	Merged int
	// Repointed counts books moved onto the canonical series, on the WRITE
	// succeeding. It is counted separately from Merged: a merge can repoint
	// some books and then REFUSE the delete, which changes book->series
	// assignments while deleting nothing.
	Repointed int
	// Errors are the op's error lines, in order, exactly as it logs them.
	Errors []string
	// WriteErrs are the errors ModifyBook and DeleteSeries returned, so a
	// caller can tell a stopped run (a fenced Writer) from a store failure.
	WriteErrs []error
	// Unrecorded are the recorder's errors: writes that were made but whose
	// ledger row was not.
	Unrecorded []error
}

// MergeSeriesGroup moves every book of each series in away onto keep and
// deletes each emptied series row.
//
// refCounts are the UNFILTERED reference counts (database.SeriesRefCounts)
// and members the complete AllVersions membership of every series in away
// (database.SeriesMembershipAllVersions), both read by the caller before the
// first write.
//
// The reference counts are an upper bound on what still references a
// merged-from series: a merge only ever moves books AWAY from it, and each
// series belongs to one group and is visited once, so a stale-high count
// refuses, never permits. Every successful repoint is recorded with Move, so
// members stays equal to what a fresh read would return.
//
// Two separate guards stand before each delete, because they answer
// different questions and fire on different populations:
//   - repointFailed: a row this call was HANDED and could not write. A book
//     still holding the series when the row is deleted is stranded exactly
//     as if the getter had hidden it, and the operator is told the prune
//     succeeded.
//   - the reference guard: a row it was never handed at all. Reaching it
//     means the unfiltered counter sees more references than the AllVersions
//     membership returned: a TRASHED row (both series getters skip
//     soft-deleted books, and a trashed row cannot be repointed) or a row the
//     memdb counts and Pebble can no longer hydrate. That is the surviving
//     half of the hazard that left 6,893 phantom series IDs held by 13,322
//     live books (+702 trashed) on production 2026-08-14; see
//     internal/database/series_bookref.go.
//
// Only the row removal is refused. The books that WERE repointed stay
// repointed: that is strictly an improvement and rolling it back would
// re-strand them. A surviving series row is visible and re-cleanable; a
// deleted one is not.
func MergeSeriesGroup(store SeriesPruneWriter, keep database.Series, away []database.Series,
	refCounts map[int]int, members database.SeriesBooksMap, rec SeriesPruneRecorder) SeriesGroupMergeResult {
	var res SeriesGroupMergeResult
	keepID := keep.ID
	record := func(c *database.OperationChange) {
		if err := rec(c); err != nil {
			res.Unrecorded = append(res.Unrecorded, err)
		}
	}
	for _, ser := range away {
		// Hydrate the full row via GetBookByID and mutate/write THAT. The
		// tempting shortcut -- bookCore.ToBook() then UpdateBook -- would drop
		// the denormalized Author/Series (db:"-"): they are NOT covered by
		// UpdateBook's STOR-1 preserve-on-empty guard (which only restores
		// Description/VersionNotes/BookSig*). Hydrating keeps the write
		// correct and self-contained.
		// See docs/specs/2026-07-05-store-getter-fidelity-unification.md.
		//
		// AllVersions, not the Core listing getter: this loop repoints every
		// row it is handed and then deletes ser.ID below. A non-primary
		// version the listing getter hides is one this loop never repoints,
		// and it is left holding a series that no longer exists.
		books := members[ser.ID]
		repointFailed := 0
		// Rows this iteration actually MOVED off ser.ID, counted on the
		// write succeeding. It is the subtrahend of the reference guard
		// below and must never be len(books): that is what was ATTEMPTED,
		// and it would make the guard pass on exactly the failures it
		// exists to catch.
		moved := 0
		for _, bookCore := range books {
			oldSeriesID := ser.ID
			full, herr := store.GetBookByID(bookCore.ID)
			if herr != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("failed to hydrate book %s: %v", bookCore.ID, herr))
				repointFailed++
				continue
			}
			if full == nil {
				// Reachable: the Pebble store returns (nil, nil) on ErrNotFound,
				// and the membership getter can list a row from the memdb that a
				// later point-get cannot hydrate. This branch used to be entirely
				// silent -- no error, no counter -- and then fell through to the
				// delete.
				res.Errors = append(res.Errors, fmt.Sprintf(
					"book %s is listed under series %d but does not resolve", bookCore.ID, ser.ID))
				repointFailed++
				continue
			}
			// ModifyBook writes SeriesID alone, on the row as it is under
			// the write lock, so a column another writer committed between
			// the hydrate above and this write is not reverted.
			written, err := store.ModifyBook(full.ID, func(b *database.Book) error {
				if b.SeriesID != nil && *b.SeriesID == keepID {
					return database.ErrSkipBookWrite
				}
				b.SeriesID = &keepID
				return nil
			})
			if err == nil && written == nil {
				err = fmt.Errorf("book vanished before its series could be reassigned")
			}
			if err != nil {
				res.WriteErrs = append(res.WriteErrs, err)
				res.Errors = append(res.Errors, fmt.Sprintf("failed to reassign book %s: %v", bookCore.ID, err))
				repointFailed++
				continue
			}
			res.Repointed++
			moved++
			members.Move(bookCore.ID, ser.ID, &keepID)
			record(&database.OperationChange{
				BookID:     bookCore.ID,
				ChangeType: "series_merge",
				FieldName:  "series_id",
				OldValue:   fmt.Sprintf("%d (%s)", oldSeriesID, ser.Name),
				NewValue:   fmt.Sprintf("%d (%s)", keepID, keep.Name),
			})
		}
		if repointFailed > 0 {
			res.Errors = append(res.Errors, fmt.Sprintf(
				"series %d (%q): %d of %d books could not be repointed to %d; REFUSING to "+
					"delete it, which would leave them holding a series row that no longer "+
					"exists. Re-run after resolving the errors above.",
				ser.ID, ser.Name, repointFailed, len(books), keepID))
			continue
		}
		if stranded := refCounts[ser.ID] - moved; stranded > 0 {
			res.Errors = append(res.Errors, fmt.Sprintf(
				"series %d (%q): %d book(s) still reference it after reassigning %d to %d "+
					"(trashed rows, which cannot be repointed, or rows the reference count sees "+
					"and this run could not); REFUSING to delete it, which would leave them "+
					"holding a series row that no longer exists",
				ser.ID, ser.Name, stranded, moved, keepID))
			continue
		}
		if err := store.DeleteSeries(ser.ID); err != nil {
			res.WriteErrs = append(res.WriteErrs, err)
			res.Errors = append(res.Errors, fmt.Sprintf("failed to delete series %d: %v", ser.ID, err))
			continue
		}
		res.Merged++
		record(&database.OperationChange{
			ChangeType: "series_delete",
			FieldName:  "series",
			OldValue:   fmt.Sprintf("%d: %s", ser.ID, ser.Name),
			NewValue:   fmt.Sprintf("merged into %d: %s", keepID, keep.Name),
		})
	}
	return res
}

// OrphanDeleteResult is what DeleteOrphanSeries did.
type OrphanDeleteResult struct {
	// Deleted: the series row was removed.
	Deleted bool
	// Held: the row is held for an undo and was kept.
	Held bool
	// Referenced: some book (in any state) still names the series, so it was
	// kept.
	Referenced bool
	// Err is the store's refusal, worded as the op logs it.
	Err error
	// WriteErr is DeleteSeries' own error, unwrapped.
	WriteErr error
	// Unrecorded is the recorder's error: the row was deleted but its
	// ledger row was not written.
	Unrecorded error
}

// DeleteOrphanSeries deletes ser when NOTHING references it.
//
// refCount is ser's UNFILTERED reference count (database.SeriesRefCounts:
// trashed and non-primary books included), read by the caller AFTER every
// merge it ran, not before. A merge repoints books ONTO the series it keeps,
// so a canonical series that had zero references before the merges can hold
// several by the time this runs. A merge's own guard is RELATIVE (count
// minus what it moved) and is monotone-safe against its own writes; this one
// is ABSOLUTE (== 0) and is not. Deleting on a pre-merge count would delete
// the merge TARGET out from under every book just moved into it.
//
// Counting with the filtered getter (GetBooksBySeriesIDCore) is the bug this
// guard replaced: it skips trashed and non-primary books, which still hold
// the series id, and deleting on its zero left them pointing at a row that
// no longer exists (6,893 phantom series IDs on production 2026-08-14).
//
// A held row (database.SeriesHeld) is kept for an undo that links its books
// back.
func DeleteOrphanSeries(store interface{ DeleteSeries(id int) error }, ser database.Series, refCount int,
	rec SeriesPruneRecorder) OrphanDeleteResult {
	var res OrphanDeleteResult
	if refCount != 0 {
		res.Referenced = true
		return res
	}
	if database.SeriesHeld(ser) {
		res.Held = true
		return res
	}
	if err := store.DeleteSeries(ser.ID); err != nil {
		res.WriteErr = err
		res.Err = fmt.Errorf("failed to delete orphan series %d: %v", ser.ID, err)
		return res
	}
	res.Deleted = true
	res.Unrecorded = rec(&database.OperationChange{
		ChangeType: "series_delete",
		FieldName:  "orphan_series",
		OldValue:   fmt.Sprintf("%d: %s", ser.ID, ser.Name),
		NewValue:   "deleted (0 book references, any state)",
	})
	return res
}

// Kinds of a SeriesPruneCandidate.
const (
	SeriesPruneDuplicate = "duplicate"
	SeriesPruneOrphan    = "orphan"
)

// SeriesPruneCandidate is one unit of the per-row prune: a duplicate group
// (Kind SeriesPruneDuplicate, two or more Members) or one series with no
// listed books (Kind SeriesPruneOrphan, one Member).
type SeriesPruneCandidate struct {
	Kind    string
	Key     SeriesGroupKey
	Members []database.Series
}

// PlanSeriesPrune lists the candidates of a per-row prune by the execute
// rules: every duplicate group of GroupDuplicateSeries (held rows in none),
// then every series in NO such group whose display count (coreCounts, as
// GetAllSeriesBookCounts returns it: trashed and non-primary books not
// counted) is zero, held ones included. The orphan candidate set is the old
// preview's ("0 books"); the unfiltered reference count the caller checks
// next is what decides whether one may be deleted.
//
// Every series is in at most one candidate. A member of a duplicate group is
// never also an orphan candidate, even when it lists no books: rows applied
// in parallel are partitioned by the books they touch, and an orphan row
// touches none, so an orphan row could delete a group's zero-reference
// canonical while the group's merge row moves books into it. The whole-
// library op avoids that by sweeping orphans only after every merge; a
// per-row plan avoids it by never offering the same series twice. A
// canonical a merge leaves empty is an orphan candidate on the next plan.
func PlanSeriesPrune(all []database.Series, coreCounts map[int]int) []SeriesPruneCandidate {
	groups, _ := GroupDuplicateSeries(all)
	inGroup := make(map[int]bool)
	out := make([]SeriesPruneCandidate, 0, len(groups))
	for _, g := range groups {
		for _, s := range g.Members {
			inGroup[s.ID] = true
		}
		out = append(out, SeriesPruneCandidate{Kind: SeriesPruneDuplicate, Key: g.Key, Members: g.Members})
	}
	orphans := make([]database.Series, 0)
	for _, s := range all {
		if inGroup[s.ID] || coreCounts[s.ID] != 0 {
			continue
		}
		orphans = append(orphans, s)
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].ID < orphans[j].ID })
	for _, s := range orphans {
		out = append(out, SeriesPruneCandidate{Kind: SeriesPruneOrphan, Key: SeriesGroupKeyOf(s), Members: []database.Series{s}})
	}
	return out
}
