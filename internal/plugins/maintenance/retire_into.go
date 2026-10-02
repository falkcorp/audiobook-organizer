// file: internal/plugins/maintenance/retire_into.go
// version: 1.3.0
// guid: dadb4da5-0f2d-4678-abf3-4ac97f3ecb66
// last-edited: 2026-10-02

// The shared retire of the Repairs-lane merge fixers: fold one book into
// another as merge.Service retires an absorbed book, every step journaled
// through repairs.Writer before it is written. Lifted out of
// fragment_consolidation_fixer.go on 2026-10-01 so the duplicate-copies fixer
// retires a whole copy with the same code a fragment retire uses.

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// retireInto folds book id (a fragment, a duplicate copy) into target the way
// merge.Service retires an absorbed book, each step journaled first so the op
// revert restores it. The fragment-consolidation and duplicate-copies fixers
// share it (fixerID names the caller in logs):
//
//  1. every user's listening state and positions follow onto target
//     (user_state_follow): as a slice of its timeline when slice is set, by
//     the whole-book rule (merge/user_state_merge.go) when it is nil;
//  2. the retired book's external ids move to target (external_id_reassign); an
//     un-tombstoned iTunes id refuses the row instead;
//  3. a primary retired book is demoted (book_primary_demote, OldValue "true"
//     for an unset flag too, so the revert crowns it back and the group's
//     hand-off is undone with it);
//  4. one write sets merged_into_book_id (book_merged_into), CLEARS
//     file_path (book_path_update: the path is now a file target owns, and
//     the purge deletes a purged book's file_path) and soft-deletes it
//     (book_soft_delete).
//
// Its version group is then handed a primary: by retireHandOff when the
// book was primary at the read, else by resumeHandOff when the evidence says
// an earlier run demoted it and never handed off (see owedHandOff). The
// fragment's book_file row,
// if it still has one, is kept.
//
// Every refusal (an iTunes id on the book, on one of its rows or among its
// external ids) is checked BEFORE step 1, so a refused retire has written
// nothing. The count is the steps written, a persisted follow snapshot
// included, so a failure after it reports partially_applied; a book already
// soft-deleted counts none (the last step of an earlier run).
//
// A follow that could not move every user leaves merge's pending-repair
// record behind. Its sweep defers while the retired book is live
// (merge.ErrPendingLoserLive), so it never drains progress off a book a
// failed retire left live; the op revert drops the record.
func retireInto(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, clock func() time.Time, fixerID, id, target string, slice *merge.SliceMapping) (int, error) {
	b, err := store.GetBookByID(id)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", id, err)
	}
	if b == nil {
		return 0, fmt.Errorf("%w: book %s vanished", repairs.ErrChangedSincePlan, id)
	}
	if b.IsSoftDeleted() {
		// Retired by an earlier run, which may have been cut off (a lost
		// scan stand-down lease) before its primary hand-off.
		return 0, resumeHandOff(ctx, p, store, w, fixerID, id, target)
	}
	// Refusals first: nothing is written for a refused retire.
	if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
		return 0, fmt.Errorf("%w: book %s now carries an iTunes id", repairs.ErrChangedSincePlan, id)
	}
	rows, err := store.GetBookFiles(id)
	if err != nil {
		return 0, fmt.Errorf("files of %s: %w", id, err)
	}
	for _, r := range rows {
		if r.ITunesPersistentID != "" {
			return 0, fmt.Errorf("%w: book %s row %s now carries an iTunes id", repairs.ErrChangedSincePlan, id, r.ID)
		}
	}
	exts, err := store.GetExternalIDsForBook(id)
	if err != nil {
		return 0, fmt.Errorf("external ids of %s: %w", id, err)
	}
	for _, e := range exts {
		if e.Source == "itunes" && !e.Tombstoned {
			return 0, fmt.Errorf("%w: book %s now carries iTunes id %s", repairs.ErrChangedSincePlan, id, e.ExternalID)
		}
	}
	steps := 0
	// 1. listening state
	did, err := followUserStateInto(p, w, target, id, slice)
	steps += did
	if err != nil {
		return steps, fmt.Errorf("carry listening state of %s: %w", id, err)
	}
	// 2. external ids
	for _, e := range exts {
		if e.Tombstoned {
			// A tombstoned mapping records an id taken off this book (an
			// iTunes track removed, a mismatch undone): it stays on the
			// retired book rather than land on the survivor.
			continue
		}
		if err := w.Step(id, undo.ChangeTypeExternalIDReassign, "external_id:"+e.Source+"/"+e.ExternalID, id, target, func() error {
			return store.ReassignExternalID(e.Source, e.ExternalID, target)
		}); err != nil {
			return steps, fmt.Errorf("move external id %s/%s to %s: %w", e.Source, e.ExternalID, target, err)
		}
		steps++
	}
	// 3. demote
	wasPrimary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
	if wasPrimary {
		notPrimary := false
		if err := w.Step(id, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false", func() error {
			_, err := w.Modify(id, func(cur *database.Book) error {
				if cur.IsPrimaryVersion != nil && !*cur.IsPrimaryVersion {
					return database.ErrSkipBookWrite
				}
				cur.IsPrimaryVersion = &notPrimary
				return nil
			})
			return err
		}); err != nil {
			return steps, fmt.Errorf("demote %s: %w", id, err)
		}
		steps++
	}
	// 4. merged into, path cleared, soft-deleted: journaled, then one write.
	prevMerged := ""
	if b.MergedIntoBookID != nil {
		prevMerged = *b.MergedIntoBookID
	}
	if err := w.Journal(id, undo.ChangeTypeBookMergedInto, "merged_into_book_id", prevMerged, target); err != nil {
		return steps, err
	}
	if b.FilePath != "" {
		if err := w.Journal(id, undo.ChangeTypeBookPathUpdate, "file_path", b.FilePath, ""); err != nil {
			return steps, err
		}
	}
	// The stamp is journaled as NewValue: the revert restores the book only
	// while it still carries this stamp (undo.CheckSoftDeleteCurrent), so a
	// row left by a retire that never wrote cannot un-delete a book the user
	// deleted later. Truncated to the microsecond so the stamp compares equal
	// after any store round trip.
	//
	// A resumed retire whose soft-delete was journaled and never written
	// reuses the journaled stamp: the Step then dedupes onto that row and
	// writes its stamp, so one retire never journals two.
	now := clock().UTC().Truncate(time.Microsecond)
	if prev, ok, err := w.JournaledValue(id, undo.ChangeTypeBookSoftDelete, "marked_for_deletion"); err != nil {
		return steps, fmt.Errorf("read the journaled soft-delete of %s: %w", id, err)
	} else if ok {
		if t, ok := undo.ParseSoftDeleteStamp(prev); ok {
			now = t
		}
	}
	if err := w.Step(id, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(now), func() error {
		_, err := w.Modify(id, func(cur *database.Book) error {
			if cur.FilePath != b.FilePath {
				return fmt.Errorf("%w: book %s path changed during the retire", repairs.ErrChangedSincePlan, id)
			}
			t := true
			cur.MarkedForDeletion = &t
			cur.MarkedForDeletionAt = &now
			cur.MergedIntoBookID = &target
			cur.FilePath = ""
			return nil
		})
		return err
	}); err != nil {
		return steps, fmt.Errorf("soft-delete %s: %w", id, err)
	}
	steps++
	if b.VersionGroupID != nil && *b.VersionGroupID != "" {
		// wasPrimary is the flag at the read above. An earlier run of this
		// retire may have demoted the book (step 3) and lost the lease before
		// the soft-delete landed: the flag then reads false here although the
		// group still owes a primary, so a book that was not primary at the
		// read goes through the evidence check (resumeHandOff), never a
		// silent skip that would leave the group with no live primary.
		if wasPrimary {
			err = retireHandOff(ctx, p, store, w, fixerID, id, *b.VersionGroupID)
		} else {
			err = resumeHandOff(ctx, p, store, w, fixerID, id, target)
		}
		if err != nil {
			return steps, err
		}
	}
	return steps, nil
}

// followUserStateInto carries every user's state and positions on the retired book
// onto target (merge.FollowAbsorbedJournaled, as a slice of target's
// timeline). The before-snapshot is journaled BEFORE anything moves and the
// before-and-after one after, both as user_state_follow rows; the revert of
// the newer one restores everything and the older one then finds nothing
// left to do. A store that cannot follow refuses only a book somebody has
// listened to. The count is the snapshots journaled, also on an error: a
// persisted before-snapshot is a written step.
func followUserStateInto(p *Plugin, w *repairs.Writer, target, id string, slice *merge.SliceMapping) (int, error) {
	um := p.deps.MergeUserStateStore()
	if um == nil {
		return 0, errors.New("user-state store unavailable")
	}
	persisted := 0
	record := func(progress []merge.CombineUserProgress) error {
		if len(progress) == 0 {
			return nil
		}
		raw, err := json.Marshal(progress)
		if err != nil {
			return err
		}
		v, err := json.Marshal(undo.UserStateFollowRecord{SyncRedirected: true, Progress: raw})
		if err != nil {
			return err
		}
		if err := w.Journal(id, undo.ChangeTypeUserStateFollow, undo.SurvivorField(target), "", string(v)); err != nil {
			return err
		}
		persisted++
		return nil
	}
	// FollowAbsorbedJournaled writes the store directly (the pending-repair
	// record, then the progress moves) before its first record call, and
	// record journals nothing for a book nobody listened to: renew here so
	// none of those writes runs on a lapsed lease.
	if err := w.Beat("user state of " + id); err != nil {
		return 0, err
	}
	progress, _, err := merge.FollowAbsorbedJournaled(um, target, id, slice, record)
	if errors.Is(err, merge.ErrNoSyncFollower) {
		has, herr := merge.BookHasUserProgress(um, id)
		if herr != nil {
			return 0, herr
		}
		if has {
			return 0, err
		}
		return 0, nil
	}
	if err != nil {
		return persisted, err
	}
	if len(progress) == 0 {
		return persisted, nil
	}
	err = record(progress)
	return persisted, err
}

// retireHandOff gives a retired book's version group a primary again, as
// fs-regroup-xml's retire does. The demote was journaled with OldValue
// "true", so its revert crowns the retired book (versionprimary.Crown) and
// demotes whichever sibling this hand-off promoted. Once the hand-off
// succeeded it is journaled (undo.ChangeTypeBookPrimaryHandoff): that row is
// the revert's evidence the group's flags were changed, the only case in
// which it re-crowns over an already-restored demote. A failure is logged,
// not returned: the retirement itself is done and journaled, and a missing
// hand-off row only keeps a revert from touching the group's flags.
//
// EXCEPT a lost scan stand-down lease (repairs.ErrStandDownLost), which is
// returned: EnsureSinglePrimary writes the store directly, so the lease is
// renewed (Writer.Beat) before it runs, and a refusal there or at the
// journal must stop the row. Swallowed, it let the rest of the row write on
// a lapsed lease and a row's last retire report applied with no hand-off;
// returned, the row aborts and resumeHandOff finishes it on the retry.
func retireHandOff(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, fixerID, id, groupID string) error {
	vps := p.deps.VersionPrimaryStore()
	if vps == nil {
		return nil
	}
	if err := w.Beat("primary hand-off of " + id + " in group " + groupID); err != nil {
		return fmt.Errorf("primary hand-off of %s: %w", id, err)
	}
	es := fragEnsureStore{OpsStore: store, chapters: vps}
	if _, err := versionprimary.EnsureSinglePrimary(ctx, es, groupID,
		versionprimary.Env{RootDir: config.AppConfig.RootDir}); err != nil {
		fragLog.Warn("%s: primary hand-off in group %s: %s", fixerID,
			logger.SanitizeLogValue(groupID), logger.SanitizeLogValue(err.Error()))
		return nil
	}
	if err := w.Journal(id, undo.ChangeTypeBookPrimaryHandoff, "version_group_id", "", groupID); err != nil {
		if errors.Is(err, repairs.ErrStandDownLost) {
			return fmt.Errorf("journal the primary hand-off of %s: %w", id, err)
		}
		fragLog.Warn("%s: journal the primary hand-off of %s in group %s: %s", fixerID,
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(groupID), logger.SanitizeLogValue(err.Error()))
	}
	return nil
}

// resumeHandOff finishes the primary hand-off of book id, retired into
// target, when an earlier run of fixer fixerID demoted it and never handed
// off, from ANY operation: a lease-lost apply ends failed, and its retry
// (POST /operations/v2/:id/retry) is a new op whose journal holds none of the
// first run's rows, so the evidence is the book's state, every op's journal
// and the fixer's history, never the op id. Shared by every fixer that
// retires through retireInto, both for a book met already retired and for a
// book retired just now that was not primary at the read (its demote may be
// an earlier run's).
//
// In order, nothing is owed (nil) when:
//   - the book has no version group, or was merged into another target;
//   - its group has exactly one live primary. Checked before any history:
//     a duplicate-copies loser demoted by Crown, whose hand-off note was cut
//     off, has no Writer history row, and its group is whole;
//   - no un-reverted demote row for it is in any op's journal (never
//     demoted by a retire), or an un-reverted hand-off row is newer than the
//     newest such demote (handed off already).
//
// It is owed when, besides, the newest is_primary_version history row of
// the book is fixerID's demote to false, the book is still explicit false,
// and no live member of its group is primary. It is then made by THIS op and
// journaled in this op's journal. Reverts converge in either order: the
// hand-off row is a note (the revert of the first op's demote re-crowns the
// book with versionprimary.Crown, which demotes the member this hand-off
// crowned), and reverting this op alone leaves the group with the member it
// crowned, its one primary.
//
// Anything it cannot tell (history or journal unreadable, the flag changed
// by someone else since the demote, two or more live primaries after a
// demote of ours) refuses the row as changed since plan rather than guess.
//
// GetBookChanges has no by-book index (PebbleStore scans every opchange
// row), so the lease is renewed before it, and the live-primary count above
// keeps the scan off the common path.
func resumeHandOff(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, fixerID, id, target string) error {
	b, err := store.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("read %s: %w", id, err)
	}
	if b == nil || b.VersionGroupID == nil || *b.VersionGroupID == "" || b.MergedIntoBookID == nil || *b.MergedIntoBookID != target {
		return nil
	}
	gid := *b.VersionGroupID
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%w: hand-off of retired book %s: %s", repairs.ErrChangedSincePlan, b.ID, fmt.Sprintf(format, args...))
	}
	members, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		return refuse("group %s unreadable: %v", gid, err)
	}
	live := 0
	for i := range members {
		m := &members[i]
		if m.ID == b.ID || m.IsSoftDeleted() {
			continue
		}
		if m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			live++
		}
	}
	if live == 1 {
		return nil // the group has its one primary: nothing owed
	}
	journal, ok := store.(bookJournalReader)
	if !ok {
		return refuse("the store has no operation journal to check it with")
	}
	if err := w.Beat("hand-off check of " + id); err != nil {
		return err
	}
	changes, err := journal.GetBookChanges(b.ID)
	if err != nil {
		return refuse("operation journal unreadable: %v", err)
	}
	demote, handOff := "", ""
	for _, c := range changes {
		if c == nil || c.RevertedAt != nil {
			continue
		}
		switch c.ChangeType {
		case undo.ChangeTypeBookPrimaryDemote:
			if c.ID > demote {
				demote = c.ID
			}
		case undo.ChangeTypeBookPrimaryHandoff:
			if c.ID > handOff {
				handOff = c.ID
			}
		}
	}
	if demote == "" || handOff > demote {
		return nil // never demoted by a retire, or handed off since
	}
	hist, ok := store.(bookHistoryReader)
	if !ok {
		return refuse("the store has no change history to check it with")
	}
	rows, err := hist.GetBookChangeHistory(b.ID, vgHistoryWindow)
	if err != nil {
		return refuse("change history unreadable: %v", err)
	}
	var newest *database.MetadataChangeRecord
	for i := range rows {
		if rows[i].Field == "is_primary_version" {
			newest = &rows[i]
			break
		}
	}
	falseJSON, err := json.Marshal("false")
	if err != nil {
		return refuse("encode: %v", err)
	}
	switch {
	case newest == nil:
		return refuse("a demote is journaled but no history row records it")
	case newest.Source != fixerID:
		return refuse("its primary flag was last changed by %q, not by %s", newest.Source, fixerID)
	case newest.NewValue == nil || *newest.NewValue != string(falseJSON) || storedPrimaryFlag(b.IsPrimaryVersion) != "false":
		return refuse("its primary flag is %s, not the false %s wrote", storedPrimaryFlag(b.IsPrimaryVersion), fixerID)
	case live > 1:
		return refuse("group %s has %d live primaries", gid, live)
	}
	return retireHandOff(ctx, p, store, w, fixerID, b.ID, gid)
}

// bookJournalReader reads every operation's journal rows for one book.
// Asserted on the ops store rather than widening OpsStore.
type bookJournalReader interface {
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}
