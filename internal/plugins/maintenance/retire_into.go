// file: internal/plugins/maintenance/retire_into.go
// version: 1.16.0
// guid: dadb4da5-0f2d-4678-abf3-4ac97f3ecb66
// last-edited: 2026-10-07

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

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunesguard"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// retireInto folds book id (a fragment, a duplicate copy) into target the way
// merge.Service retires an absorbed book, each step journaled first so the op
// revert restores it. The fragment-consolidation, duplicate-copies and
// consolidation-leftovers fixers share it (fixerID names the caller in logs):
//
//  1. every user's listening state and positions follow onto target
//     (user_state_follow): as a slice of its timeline when slice is set, by
//     the whole-book rule (merge/user_state_merge.go) when it is nil;
//  2. the retired book's external ids move to target (external_id_reassign); an
//     un-tombstoned iTunes id refuses the row instead;
//  3. a primary retired book is demoted (book_primary_demote, OldValue "true"
//     for an unset flag too, so the revert crowns it back and the group's
//     hand-off is undone with it -- unless the hand-off's note shows another
//     member's true predates the op, which the book then yields to; see
//     retireHandOff);
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
// Every refusal (an iTunes id on the book, an iTunes id or (unless
// retireOpts.AllowITunesPath) an iTunes path on one of its rows, or an
// iTunes id among its external ids) is checked BEFORE step 1, so a refused retire has written
// nothing. The count is the steps written, a persisted follow snapshot
// included, so a failure after it reports partially_applied; a book already
// soft-deleted counts none (the last step of an earlier run).
//
// A follow that could not move every user leaves merge's pending-repair
// record behind. Its sweep defers while the retired book is live
// (merge.ErrPendingLoserLive), so it never drains progress off a book a
// failed retire left live; the op revert drops the record.
func retireInto(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, clock func() time.Time, fixerID, id, target string, slice *merge.SliceMapping) (int, error) {
	return retireIntoWith(ctx, p, store, w, clock, fixerID, id, target, slice, retireOpts{})
}

// retireIntoExpecting is retireInto whose primary hand-off writes nothing
// unless versionprimary, deciding under its group lock, leaves expect
// primary (versionprimary.Env.Expect; "" expects nothing). A different
// winner stops the row with versionprimary.ErrUnexpectedWinner after the
// retire's earlier steps: partially applied, nobody else crowned. The
// consolidation-leftovers same-path class passes the owner it predicted
// under the merge lock, closing the window between that prediction and the
// hand-off (which runs after the slower user-state follow). rules also
// carries the caller's never-write-an-iTunes-book check (handOffRules). Its
// one caller is consolidation-leftovers, so it allows a row iTunes path
// (retireOpts.AllowITunesPath).
func retireIntoExpecting(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, clock func() time.Time, fixerID, id, target string, slice *merge.SliceMapping, rules handOffRules) (int, error) {
	return retireIntoWith(ctx, p, store, w, clock, fixerID, id, target, slice, retireOpts{Expect: rules.expect, MayWrite: rules.mayWrite, AllowITunesPath: true})
}

// retireIntoAllowingITunesPath is retireInto for duplicate-copies, whose own
// rules decide a row iTunes path (retireOpts.AllowITunesPath).
func retireIntoAllowingITunesPath(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, clock func() time.Time, fixerID, id, target string, slice *merge.SliceMapping) (int, error) {
	return retireIntoWith(ctx, p, store, w, clock, fixerID, id, target, slice, retireOpts{AllowITunesPath: true})
}

// handOffRules are what a retire's primary hand-off must respect, checked
// by versionprimary under the group lock before it writes anything:
//
//   - expect: the member it must leave primary (versionprimary.Env.Expect;
//     "" expects nothing). Any other decision writes nothing and returns
//     versionprimary.ErrUnexpectedWinner.
//   - mayWrite: asked about every member whose flag it would write
//     (versionprimary.Env.MayWrite; nil allows all). A refusal writes
//     nothing and returns versionprimary.ErrWriteRefused.
//
// Either refusal stops the row after the retire's earlier steps (partially
// applied), with a ledger note (undo.ChangeTypeBookPrimaryHandoffRefused)
// so the op revert knows the hand-off wrote no member's flag.
type handOffRules struct {
	expect   string
	mayWrite func(*database.Book) error
}

// retireOpts adjust the shared retire. The zero value is the safe one: its
// hand-off carries the iTunes guard (itunesguard.MayWrite) unless MayWrite
// overrides it.
type retireOpts struct {
	// Expect is the member the primary hand-off must leave primary ("" none;
	// retireIntoExpecting).
	Expect string
	// MayWrite is asked, under the group lock, about every member whose
	// primary flag the hand-off would write (handOffRules.mayWrite). nil
	// means itunesguard.MayWrite over the retired book's group; a caller
	// passes its own only to add to that rule (or a test, to fail it).
	MayWrite func(*database.Book) error
	// Only, when set, is why target must not be written (retireIntoOnly).
	Only string
	// AllowITunesPath lets a book with an iTunes path on one of its rows be
	// retired. By default such a book is refused as an iTunes book
	// (itunesCopyWhy's "row iTunes path"; review 2026-10-06). Set only for
	// two callers: consolidation-leftovers, through retireIntoExpecting
	// (owner decision 2026-10-06: a bare iTunes path reference does not make
	// a leftover iTunes-owned), and duplicate-copies, through
	// retireIntoAllowingITunesPath (its losers are judged by its own iTunes
	// rules before the apply).
	AllowITunesPath bool
}

// retireIntoOnly is retireInto for a target that must not be written
// (owner decision 2026-10-06: a copy retired into an iTunes-linked parent;
// targetWhy says why it is iTunes-linked). Only the retired book is
// written: steps 3 and 4 and its own group's hand-off. Steps 1 and 2 are
// not run, so no listening state, positions, bookmarks or sync redirect
// follow onto target and no external id moves to it; a book that has any
// of those to carry is refused BEFORE the first write (ErrChangedSincePlan),
// as is one whose user state cannot be read. The caller has checked that
// the retired book's version group holds no iTunes book.
func retireIntoOnly(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, clock func() time.Time, fixerID, id, target, targetWhy string) (int, error) {
	return retireIntoWith(ctx, p, store, w, clock, fixerID, id, target, nil, retireOpts{Only: targetWhy})
}

// retireIntoWith is retireInto with opts: the hand-off's expected winner
// (opts.Expect), writing book id alone when opts.Only is set
// (retireIntoOnly), refusing a row iTunes path unless opts.AllowITunesPath
// is set.
func retireIntoWith(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, clock func() time.Time, fixerID, id, target string, slice *merge.SliceMapping, opts retireOpts) (int, error) {
	only := opts.Only
	rules := handOffRules{expect: opts.Expect, mayWrite: opts.MayWrite}
	b, err := store.GetBookByID(id)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", id, err)
	}
	if b == nil {
		return 0, fmt.Errorf("%w: book %s vanished", repairs.ErrChangedSincePlan, id)
	}
	if rules.mayWrite == nil && b.VersionGroupID != nil {
		// Every retire's hand-off refuses to write an iTunes book's
		// primary flag (itunesguard.MayWrite), asked under the group lock
		// about exactly the members it would write. This is about the
		// members the hand-off WRITES, not the retired book's own rows, so
		// it holds with AllowITunesPath (which only lets a book whose row
		// carries an iTunes path reference be retired) and with Only.
		rules.mayWrite = itunesguard.MayWrite(store, *b.VersionGroupID)
	}
	if b.IsSoftDeleted() {
		// Retired by an earlier run, which may have been cut off (a lost
		// scan stand-down lease) before its primary hand-off.
		return 0, resumeHandOff(ctx, p, store, w, fixerID, id, target, rules)
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
		// A row iTunes path makes the book an iTunes book too
		// (itunesCopyWhy), and iTunes books are never written.
		if !opts.AllowITunesPath && r.ITunesPath != "" {
			return 0, fmt.Errorf("%w: book %s row %s now carries an iTunes path", repairs.ErrChangedSincePlan, id, r.ID)
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
	if only != "" {
		if err := onlyRetireRefusal(p, id, target, only, exts); err != nil {
			return 0, err
		}
	}
	steps := 0
	// 1. listening state
	if only == "" {
		did, err := followUserStateInto(p, w, target, id, slice)
		steps += did
		if err != nil {
			return steps, fmt.Errorf("carry listening state of %s: %w", id, err)
		}
	}
	// 2. external ids
	for _, e := range exts {
		if e.Tombstoned || only != "" {
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
			err = retireHandOff(ctx, p, store, w, fixerID, id, *b.VersionGroupID, rules)
		} else {
			err = resumeHandOff(ctx, p, store, w, fixerID, id, target, rules)
		}
		if err != nil {
			return steps, err
		}
	}
	return steps, nil
}

// onlyRetireRefusal refuses a retireIntoOnly of book id whose retire would
// have to carry something onto target: a live external id, or listening
// state, positions or bookmarks (merge.BookHasCarryableUserState). State
// that cannot be read refuses too: fail closed.
func onlyRetireRefusal(p *Plugin, id, target, targetWhy string, exts []database.ExternalIDMapping) error {
	for _, e := range exts {
		if !e.Tombstoned {
			return fmt.Errorf("%w: parent %s is iTunes-linked (%s); external id %s/%s of %s would have to move onto it",
				repairs.ErrChangedSincePlan, target, targetWhy, e.Source, e.ExternalID, id)
		}
	}
	um := p.deps.MergeUserStateStore()
	if um == nil {
		return fmt.Errorf("user-state store unavailable: listening state of %s cannot be ruled out, and parent %s is iTunes-linked", id, target)
	}
	has, err := merge.BookHasCarryableUserState(um, id)
	if err != nil {
		return fmt.Errorf("read listening state of %s: %w", id, err)
	}
	if has {
		return fmt.Errorf("%w: parent %s is iTunes-linked (%s); listening state, positions or bookmarks of %s would have to move onto it",
			repairs.ErrChangedSincePlan, target, targetWhy, id)
	}
	// A window remains: a sync client is not under the merge lock, so state
	// can land on the book between this read and the soft-delete. It is not
	// lost: it stays on the retired book, the op revert brings the book back
	// with it, and the purge refuses a book with carryable state
	// (merge.BookHasCarryableUserState) rather than drop it.
	return nil
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
// demotes whichever sibling this hand-off promoted -- but never a member
// whose true the hand-off did not write (a kept incumbent, or any member of
// a refused hand-off): the retired book yields to it instead
// (audiobooks.settleGroup's laterPick). Once the hand-off succeeded it is
// journaled (undo.ChangeTypeBookPrimaryHandoff): that row is the revert's
// evidence the group's flags were changed, the only case in which it
// re-crowns over an already-restored demote.
//
// A failed hand-off (EnsureSinglePrimary's error) is RETURNED, and the row
// stops: the book is demoted and retired with its group owed a primary.
// Until 2026-10-03 it was logged and the row went on, so the group could
// stay without a primary with no note ever written, and a re-plan read the
// owed hand-off as still pending for as long as the demote stood (a crown by
// anyone in that group then passed as ours). Returned, the row reports
// partially applied and its resume re-runs the hand-off (resumeHandOff).
//
// A lost scan stand-down lease (repairs.ErrStandDownLost) at the note is
// returned as well: EnsureSinglePrimary writes the store directly, so the
// lease is renewed (Writer.Beat) before it runs, and a refusal there or at
// the journal must stop the row. Any other failure to journal the note is
// logged: the crown is written, and a missing note only keeps a revert from
// touching the group's flags.
//
// The note says whether the hand-off wrote its primary's true or kept a
// member that already carried it (undo.HandOffNoteValue): the revert never
// demotes a kept incumbent to re-crown the retired book, since the
// operation never wrote that incumbent's flag.
//
// rules (handOffRules) may refuse the hand-off: nothing is written, a
// refusal note (undo.ChangeTypeBookPrimaryHandoffRefused) is journaled so
// the revert likewise leaves every other member's flag alone, and the
// refusal is returned, logged with the group. A hand-off that fails before
// its first write (a failed read, or a failed guard read) is noted the same
// way: it wrote nothing either.
func retireHandOff(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, fixerID, id, groupID string, rules handOffRules) error {
	vps := p.deps.VersionPrimaryStore()
	if vps == nil {
		return nil
	}
	if err := w.Beat("primary hand-off of " + id + " in group " + groupID); err != nil {
		return fmt.Errorf("primary hand-off of %s: %w", id, err)
	}
	es := fragEnsureStore{OpsStore: store, chapters: vps}
	res, err := versionprimary.EnsureSinglePrimary(ctx, es, groupID,
		versionprimary.Env{RootDir: p.deps.RootDir(), Expect: rules.expect, MayWrite: rules.mayWrite})
	if err != nil {
		fragLog.Warn("%s: primary hand-off in group %s: %s", fixerID,
			logger.SanitizeLogValue(groupID), logger.SanitizeLogValue(err.Error()))
		err = fmt.Errorf("primary hand-off of %s in group %s: %w", id, groupID, err)
		if !res.WriteAttempted {
			// Refused, or failed, before any write (an unexpected winner, a
			// guard refusal, a failed read or guard read): say so, so the
			// revert of this op does not demote a member whose true it
			// never wrote. A failure after the first write attempt is not
			// noted: that write may have committed.
			if jerr := w.Journal(id, undo.ChangeTypeBookPrimaryHandoffRefused, "version_group_id", "", groupID); jerr != nil {
				if errors.Is(jerr, repairs.ErrStandDownLost) {
					return errors.Join(err, fmt.Errorf("journal the refused primary hand-off of %s: %w", id, jerr))
				}
				fragLog.Warn("%s: journal the refused primary hand-off of %s in group %s: %s", fixerID,
					logger.SanitizeLogValue(id), logger.SanitizeLogValue(groupID), logger.SanitizeLogValue(jerr.Error()))
			}
		}
		return err
	}
	if err := w.Journal(id, undo.ChangeTypeBookPrimaryHandoff, "version_group_id", undo.HandOffNoteValue(res.PrimaryID, res.WrotePrimary()), groupID); err != nil {
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
//   - its group has exactly one live primary (livePrimaries: the
//     versionprimary.Incumbent rule, so a nil-flag primary counts). Checked
//     before any history:
//     a duplicate-copies loser demoted by Crown, whose hand-off note was cut
//     off, has no Writer history row, and its group is whole;
//   - no un-reverted demote row for it is in any op's journal (never
//     demoted by a retire), or an un-reverted hand-off row is newer than the
//     newest such demote (handed off already).
//
// It is owed when, besides, the newest is_primary_version history row of
// the book is a retire's demote to false (any fixer in retireFixerIDs, not
// only fixerID: one fixer's retire that lost its lease after the demote can
// leave the book to another fixer's row, which must finish that hand-off
// rather than refuse it forever with the group left without a primary), the book is still explicit false,
// and no live member of its group is primary. It is then made by THIS op and
// journaled in this op's journal. Reverting the two ops converges to one
// live primary in either order, through the revert engine
// (internal/audiobooks/revert.go), not through the hand-off row, which is a
// note:
//   - this op first: its soft-delete revert brings the book back and yields
//     it to the member this hand-off crowned (YieldToIncumbent), so it is
//     non-primary; the first op's demote revert then finds it live and
//     re-crowns it with versionprimary.Crown, which demotes that member.
//   - the first op first: the book is still retired, so its demote revert
//     writes its true but Crown leaves the group alone (a retired book is
//     not Electable); this op's soft-delete revert then brings it back and
//     yields it to the member this hand-off crowned, or, when that member is
//     gone by then, keeps the true or hands the group off
//     (EnsureSinglePrimary), so the group has one live primary either way.
//
// Reverting this op alone leaves the group with the member it crowned.
//
// Anything it cannot tell (history or journal unreadable, the flag changed
// by someone else since the demote, two or more live primaries after a
// demote of ours) refuses the row as changed since plan rather than guess.
//
// GetBookChanges reads PebbleStore's opchange_by_book: index only once this
// boot's startup verify (or a rebuild) has trusted it, and scans every
// opchange row until then, so the lease is still renewed before it, and the
// live-primary count above keeps the read off the common path.
func resumeHandOff(ctx context.Context, p *Plugin, store OpsStore, w *repairs.Writer, fixerID, id, target string, rules handOffRules) error {
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
	if len(members) == 0 {
		// Nobody is left to crown: the retired book was the group's only
		// live member (a lone fragment). Nothing is owed, and the journal
		// read below (a full opchange scan until this boot's verify trusts
		// the by-book index) is not paid.
		return nil
	}
	live, explicit := livePrimaries(store, members, b.ID)
	if live == 1 && explicit == 1 {
		return nil // the group has its one explicit primary: nothing owed
	}
	// A group whose one primary is a nil flag (Incumbent's rule) is not
	// settled by it when a retire's demote of b is still owed its hand-off:
	// the uninterrupted run would have run EnsureSinglePrimary there, which
	// writes an explicit winner (and may elect another member than the nil
	// one). Returning early left a resumed run with a different group than
	// an uninterrupted one (2026-10-03, the cut test's hand-off shapes).
	// The cheap reads decide first, so a book no retire demoted (the common
	// first-run retire of a non-primary copy) still pays no journal scan:
	// b must be explicit false with a retire's demote as its newest
	// primary-flag history row. Anything else owes nothing here: the group
	// has its primary.
	if live == 1 {
		if storedPrimaryFlag(b.IsPrimaryVersion) != "false" {
			return nil
		}
		// The flag's newest row only (OpsStore.GetMetadataChangeHistory, as
		// the check below reads it; see resumesOwnWrite on why not a window
		// of the whole book's history).
		rows, err := store.GetMetadataChangeHistory(b.ID, "is_primary_version", 1)
		if err != nil {
			return refuse("change history unreadable: %v", err)
		}
		retireDemote := false
		for i := range rows {
			if rows[i].Field == "is_primary_version" {
				retireDemote = retireFixerIDs[rows[i].Source] && rows[i].NewValue != nil && *rows[i].NewValue == `"false"`
				break
			}
		}
		if !retireDemote {
			return nil
		}
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
	// The flag's newest row only (see resumesOwnWrite on why not a window).
	rows, err := store.GetMetadataChangeHistory(b.ID, "is_primary_version", 1)
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
	case !retireFixerIDs[newest.Source]:
		return refuse("its primary flag was last changed by %q, not by a retire", newest.Source)
	case newest.NewValue == nil || *newest.NewValue != string(falseJSON) || storedPrimaryFlag(b.IsPrimaryVersion) != "false":
		return refuse("its primary flag is %s, not the false %s wrote", storedPrimaryFlag(b.IsPrimaryVersion), newest.Source)
	case live > 1:
		return refuse("group %s has %d live primaries", gid, live)
	}
	return retireHandOff(ctx, p, store, w, fixerID, b.ID, gid, rules)
}

// retireFixerIDs are the fixers that retire through retireInto: a demote
// recorded under any of them is a retire's demote, whose owed hand-off any
// of them may finish (resumeHandOff).
var retireFixerIDs = map[string]bool{fragFixerID: true, dcFixerID: true, leftoverFixerID: true}

// livePrimaries counts the live primaries of a version group, excluding
// book except, by the rule versionprimary.Incumbent reads: the Electable
// members whose flag is explicitly true, or, when there is none, exactly one
// Electable member whose flag is nil (every visibility path reads a nil flag
// as primary, database.EffectiveIsPrimaryVersion). Two or more nil members
// and no explicit true count as none: Incumbent cannot tell them apart.
// explicit is the explicit-true count alone. resumeHandOff skips its
// journal read only when both are 1; a group with a nil-flag primary and a
// book a retire demoted still pays it (see there).
func livePrimaries(store OpsStore, members []database.Book, except string) (live, explicit int) {
	others := make([]database.Book, 0, len(members))
	for i := range members {
		if members[i].ID != except {
			others = append(others, members[i])
		}
	}
	inGroup := make(map[string]bool, len(others))
	for i := range others {
		inGroup[others[i].ID] = !others[i].IsSoftDeleted()
	}
	alive := func(id string) bool {
		if live, ok := inGroup[id]; ok {
			return live
		}
		// versionprimary.StoreAlive's answer: a failed read counts the
		// survivor alive, so a loser is never made Electable by it.
		sb, err := store.GetBookByID(id)
		return err != nil || (sb != nil && !sb.IsSoftDeleted())
	}
	for i := range others {
		m := &others[i]
		if versionprimary.Electable(m, alive) && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			explicit++
		}
	}
	if explicit > 0 {
		return explicit, explicit
	}
	if versionprimary.Incumbent(others, alive) != nil {
		return 1, 0
	}
	return 0, 0
}

// handOffSettled reports whether a group (members, except excluded) has
// exactly one live primary, and it explicit: a retire's owed hand-off is
// then certainly not owed (resumeHandOff's first check).
func handOffSettled(store OpsStore, members []database.Book, except string) bool {
	live, explicit := livePrimaries(store, members, except)
	return live == 1 && explicit == 1
}

// bookJournalReader reads every operation's journal rows for one book.
// Asserted on the ops store rather than widening OpsStore.
type bookJournalReader interface {
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}
