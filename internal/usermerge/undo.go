// file: internal/usermerge/undo.go
// version: 1.2.0
// guid: e2b94a57-0c31-4d86-b7f5-6a3d8c1f9e20
// last-edited: 2026-10-23

package usermerge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// UndoRequest names the journal to reverse.
type UndoRequest struct {
	JournalID string
}

// mergeLockedStatus is the status Apply leaves the absorbed account in.
const mergeLockedStatus = "locked"

// hasCredentialOps reports whether any written op moved a sign-in or an API key.
func hasCredentialOps(ops []database.UserMergeOp) bool {
	return slices.ContainsFunc(ops, func(op database.UserMergeOp) bool {
		return op.Family == FamOAuthIdentities || op.Family == FamAPIKeys
	})
}

// invert is the op that puts op's key back to its before-image: it expects the
// value op wrote and writes the value op replaced. An owner-field op's inverse
// swaps the two owners and nothing else.
func invert(op database.UserMergeOp) database.UserMergeOp {
	if op.IsOwnerOp() {
		inv := op.OwnerInverse()
		inv.Reason = "undo: " + op.Reason
		return inv
	}
	return database.UserMergeOp{
		Key: op.Key, Family: op.Family, Reason: "undo: " + op.Reason,
		Before: op.After, BeforeAbsent: op.Delete,
		After: op.Before, Delete: op.BeforeAbsent,
		Unit: op.Unit, Derived: op.Derived,
	}
}

// Undo reverses a merge from its journal. Ops that stand alone are classified
// by what their key holds now: still the value the merge wrote (restored to its
// before-image), already the before-image (left), or something else, because
// something wrote it after the merge (left as it is and reported, never
// overwritten). Ops that belong to a unit (a book's listening state with its
// status index entries and positions; a sign-in or API key with its index
// entries) are classified as a whole: the unit is undone only while every one
// of its rows is still what the merge left, because reverting an index entry
// while keeping the row it indexes (or the other way round) splits the two.
// A unit with a row written since is left entirely as it is, reported once, and
// the survivor's status index for that book is rebuilt from the row that is
// there. An owner-field op (sign-ins, API keys, playlists, collections,
// invites, invite grants) puts only the owner field back, in the row as it is
// now, so a key's use counters survive. Then the two user rows are put back field by field
// (database.UserMerger.UnretireMergedUser, which also leaves a field alone that
// changed since), and the absorbed account gets its prior status back with a
// fresh credential epoch.
//
// It works on an applied merge and on one stuck mid-apply (applying), and it
// resumes an undo that was interrupted (undoing).
func (s *Service) Undo(ctx context.Context, req UndoRequest, prog Progress) (*Journal, error) {
	runMu.Lock()
	defer runMu.Unlock()
	j, err := s.LoadJournal(req.JournalID)
	if err != nil {
		return nil, err
	}
	switch j.Status {
	case StatusApplying, StatusApplied, StatusUndoing:
	default:
		return nil, fmt.Errorf("%w: merge %s is %s", ErrJournalState, j.ID, j.Status)
	}
	surv, err := s.mustUser(j.SurvivorID, "survivor")
	if err != nil {
		return nil, err
	}
	if surv.MergedIntoUserID != "" {
		return nil, fmt.Errorf("%w: the surviving account was itself merged away since; undo that merge first", ErrJournalState)
	}
	if _, err := s.mustUser(j.AbsorbedID, "absorbed account"); err != nil {
		return nil, err
	}

	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	unlock := database.LockUserBookStates(lockKeys(j.Ops, j.SurvivorID, j.AbsorbedID)...)
	defer unlock()

	fail := func(err error) (*Journal, error) {
		j.Error = failText(err)
		_ = s.save(j)
		return j, err
	}
	j.Status, j.Error = StatusUndoing, ""
	if err := s.save(j); err != nil {
		return nil, err
	}
	written := j.Ops[:j.Cursor]
	if err := s.undoOps(ctx, j, written, prog); err != nil {
		return fail(err)
	}
	if j.Retired {
		conflicts, err := s.merger.UnretireMergedUser(j.Retirement)
		if err != nil {
			return fail(err)
		}
		j.UndoConflicts = append(j.UndoConflicts, conflicts...)
		j.Retired = false
		if err := s.save(j); err != nil {
			return fail(err)
		}
	}
	// The merge moved linked sign-ins and API keys onto the survivor and undo has
	// just moved them back. A web session the survivor opened through a sign-in
	// that is now gone, or an ABS session stamped with the epoch of that moment,
	// must not outlive it: bump the survivor's credential epoch (which refuses
	// every ABS session) and revoke its web and ABS sessions. The survivor signs
	// in again, which includes the admin who clicked undo when that is them.
	if hasCredentialOps(written) {
		if _, err := s.creds.BumpUserCredentialEpoch(j.SurvivorID); err != nil {
			return fail(fmt.Errorf("end the surviving account's sessions: %w", err))
		}
		if err := s.revokeSessions(j.SurvivorID); err != nil {
			return fail(err)
		}
	}
	// Back to the status the account had before the merge locked it, but only
	// while it still has the status the merge left it in. Anything else is an
	// admin's decision made after the merge (deactivated, reactivated by hand);
	// undo must not turn a deactivated account back on, so it keeps that status
	// and says so.
	if want := j.AbsorbedStatusBefore; want != "" {
		abs, err := s.mustUser(j.AbsorbedID, "absorbed account")
		if err != nil {
			return fail(err)
		}
		switch {
		case abs.Status == want:
		case abs.Status == mergeLockedStatus:
			if _, err := s.creds.SetUserStatus(j.AbsorbedID, want); err != nil {
				return fail(fmt.Errorf("restore the merged-away account's status: %w", err))
			}
		default:
			j.UndoConflicts = append(j.UndoConflicts, fmt.Sprintf(
				"the merged-away account's status is %q now, not the %q the merge left it in; it keeps %q (it was %q before the merge)",
				abs.Status, mergeLockedStatus, abs.Status, want))
		}
	}
	now := s.now()
	j.Status, j.UndoneAt, j.Error = StatusUndone, &now, ""
	if err := s.save(j); err != nil {
		return nil, err
	}
	prog.report(len(written), len(written), "merge undone")
	if s.audit != nil {
		s.audit(fmt.Sprintf("undid the merge of user %s into %s", j.AbsorbedID, j.SurvivorID), map[string]any{
			"journal_id": j.ID, "survivor_id": j.SurvivorID, "absorbed_id": j.AbsorbedID,
			"conflicts": len(j.UndoConflicts),
		})
	}
	return j, nil
}

// undoGroup is a run of ops undone together: one whole unit, or one op that
// stands alone.
type undoGroup []database.UserMergeOp

// undoGroups splits written (in journal order) into groups, last first. A unit's
// ops are contiguous in the journal (finalize and chunkEnd keep them so).
func undoGroups(written []database.UserMergeOp) []undoGroup {
	var out []undoGroup
	for i := len(written) - 1; i >= 0; {
		j := i
		for written[i].Unit != "" && j > 0 && written[j-1].Unit == written[i].Unit {
			j--
		}
		g := slices.Clone(written[j : i+1])
		slices.Reverse(g)
		out = append(out, g)
		i = j - 1
	}
	return out
}

// undoOps reverses written (the ops the merge wrote), last first, in chunks of
// whole groups, advancing UndoCursor (a count of ops) with each chunk's batch.
func (s *Service) undoOps(ctx context.Context, j *Journal, written []database.UserMergeOp, prog Progress) error {
	groups := undoGroups(written)
	gi, pos := 0, 0
	for gi < len(groups) && pos < j.UndoCursor {
		pos += len(groups[gi])
		gi++
	}
	for gi < len(groups) {
		if err := ctx.Err(); err != nil {
			return err
		}
		var chunk []undoGroup
		n := 0
		for gi < len(groups) && (n == 0 || n+len(groups[gi]) <= chunkSize) {
			chunk = append(chunk, groups[gi])
			n += len(groups[gi])
			gi++
		}
		end := pos + n
		// A key can change between classifying it and the batch; the batch then
		// reports the conflict and the next pass classifies that key as changed.
		for attempt := 0; ; attempt++ {
			inverse, conflicts, err := s.classify(chunk, j.SurvivorID)
			if err != nil {
				return err
			}
			next := *j
			next.UndoCursor, next.UpdatedAt = end, s.now()
			next.UndoConflicts = append(slices.Clone(j.UndoConflicts), conflicts...)
			data, err := json.Marshal(&next)
			if err != nil {
				return err
			}
			err = s.merger.ApplyUserMergeOps(inverse, journalKey(j.ID), data)
			if errors.Is(err, database.ErrUserMergeConflict) && attempt < 3 {
				continue
			}
			if err != nil {
				return err
			}
			j.UndoCursor, j.UndoConflicts, j.UpdatedAt = end, next.UndoConflicts, next.UpdatedAt
			break
		}
		pos = end
		prog.report(j.UndoCursor, len(written), "undoing")
	}
	return nil
}

type opState int

const (
	atBefore opState = iota // already what it was before the merge
	atAfter                 // still what the merge wrote
	changed                 // something else wrote it since
)

// stateOf classifies op's key against the merge's before- and after-image.
func (s *Service) stateOf(op database.UserMergeOp) (opState, []byte, error) {
	cur, err := s.store.GetRaw(op.Key)
	if err != nil {
		return changed, nil, err
	}
	if op.IsOwnerOp() {
		switch op.OwnerState(cur) {
		case "to":
			return atAfter, cur, nil
		case "from":
			return atBefore, cur, nil
		}
		return changed, cur, nil
	}
	switch database.UserMergeValueHash(cur, cur != nil) {
	case op.AfterHash():
		return atAfter, cur, nil
	case op.BeforeHash():
		return atBefore, cur, nil
	}
	return changed, cur, nil
}

// classify splits chunk into the inverse ops to apply and the groups to leave.
func (s *Service) classify(chunk []undoGroup, survivorID string) (inverse []database.UserMergeOp, conflicts []string, err error) {
	for _, g := range chunk {
		inv, conf, err := s.classifyGroup(g, survivorID)
		if err != nil {
			return nil, nil, err
		}
		inverse = append(inverse, inv...)
		conflicts = append(conflicts, conf...)
	}
	return inverse, conflicts, nil
}

func (s *Service) classifyGroup(g undoGroup, survivorID string) (inverse []database.UserMergeOp, conflicts []string, err error) {
	unitLess := g[0].Unit == ""
	hasPrimary := slices.ContainsFunc(g, func(op database.UserMergeOp) bool { return !op.Derived })
	states := make([]opState, len(g))
	curs := make([][]byte, len(g))
	someChanged := false
	for i, op := range g {
		if states[i], curs[i], err = s.stateOf(op); err != nil {
			return nil, nil, err
		}
		if states[i] == changed && (unitLess || !op.Derived || !hasPrimary) {
			someChanged = true
		}
	}
	if !unitLess && hasPrimary && someChanged {
		// A row of the unit was written after the merge: the whole unit stays.
		label := g[0].Unit
		if at := strings.IndexByte(label, '@'); at >= 0 {
			label = label[:at]
		}
		conflicts = append(conflicts, fmt.Sprintf("%s (%s): a row changed after the merge; the whole group is left as it is", label, g[0].Family))
		rebuild, err := s.rebuildBookIndex(g, survivorID)
		if err != nil {
			return nil, nil, err
		}
		return rebuild, conflicts, nil
	}
	for i, op := range g {
		switch {
		case unitLess || !hasPrimary:
			switch states[i] {
			case atAfter:
				inverse = append(inverse, invert(op))
			case changed:
				conflicts = append(conflicts, fmt.Sprintf("%s (%s): changed after the merge; left as it is", op.Key, op.Family))
			}
		case op.Derived:
			// An index entry is not compared: with its unit's rows put back it is
			// set to what it was before the merge, whatever it holds now.
			if database.UserMergeValueHash(curs[i], curs[i] != nil) == op.BeforeHash() {
				continue
			}
			inverse = append(inverse, database.UserMergeOp{
				Key: op.Key, Family: op.Family, Reason: "undo: " + op.Reason,
				Before: curs[i], BeforeAbsent: curs[i] == nil,
				After: op.Before, Delete: op.BeforeAbsent, Unit: op.Unit, Derived: true,
			})
		case states[i] == atAfter:
			inverse = append(inverse, invert(op))
		}
	}
	return inverse, conflicts, nil
}

// rebuildBookIndex makes the survivor's status index entries for the book of a
// skipped unit agree with the ubs: row that is there: exactly the entry for its
// status, or none when the row is gone. It returns nothing for a unit that is
// not a book's listening state, and when the row cannot be read.
func (s *Service) rebuildBookIndex(g undoGroup, survivorID string) ([]database.UserMergeOp, error) {
	var rowKey, bookID string
	prefix := "ubs:" + survivorID + ":"
	for _, op := range g {
		if id, ok := strings.CutPrefix(op.Key, prefix); ok {
			rowKey, bookID = op.Key, id
			break
		}
	}
	if rowKey == "" {
		return nil, nil
	}
	row, err := s.store.GetRaw(rowKey)
	if err != nil {
		return nil, err
	}
	status := ""
	if row != nil {
		var st database.UserBookState
		if err := json.Unmarshal(row, &st); err != nil {
			return nil, nil
		}
		status = st.Status
	}
	var out []database.UserMergeOp
	if row != nil {
		// A guard: the batch refuses if the row moved since it was read, so the
		// index is never rebuilt from a row that is already out of date.
		out = append(out, database.UserMergeOp{Key: rowKey, Family: FamBookState, Reason: "undo: guard", Before: row, After: row})
	}
	for _, st := range []string{database.UserBookStatusUnstarted, database.UserBookStatusInProgress, database.UserBookStatusFinished, database.UserBookStatusAbandoned} {
		key := database.UserBookStateStatusKey(survivorID, st, bookID)
		cur, err := s.store.GetRaw(key)
		if err != nil {
			return nil, err
		}
		want := row != nil && st == status
		switch {
		case want && string(cur) == "1":
		case want:
			out = append(out, database.UserMergeOp{Key: key, Family: FamBookState, Reason: "undo: rebuild the status index",
				Before: cur, BeforeAbsent: cur == nil, After: []byte("1"), Derived: true})
		case cur != nil:
			out = append(out, database.UserMergeOp{Key: key, Family: FamBookState, Reason: "undo: rebuild the status index",
				Before: cur, Delete: true, Derived: true})
		}
	}
	return out, nil
}
