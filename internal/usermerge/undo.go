// file: internal/usermerge/undo.go
// version: 1.1.0
// guid: e2b94a57-0c31-4d86-b7f5-6a3d8c1f9e20
// last-edited: 2026-10-23

package usermerge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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
// value op wrote and writes the value op replaced.
func invert(op database.UserMergeOp) database.UserMergeOp {
	return database.UserMergeOp{
		Key: op.Key, Family: op.Family, Reason: "undo: " + op.Reason,
		Before: op.After, BeforeAbsent: op.Delete,
		After: op.Before, Delete: op.BeforeAbsent,
	}
}

// Undo reverses a merge from its journal. Every written op is classified by what
// its key holds now: still the value the merge wrote (restored to its
// before-image), already the before-image (left), or something else, because
// something wrote it after the merge (left as it is and reported, never
// overwritten). Then the two user rows are put back field by field
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
		j.Error = err.Error()
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

// undoOps reverses written (the ops the merge wrote), last first, in chunks,
// advancing UndoCursor with each chunk's batch.
func (s *Service) undoOps(ctx context.Context, j *Journal, written []database.UserMergeOp, prog Progress) error {
	rev := slices.Clone(written)
	slices.Reverse(rev)
	for j.UndoCursor < len(rev) {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(j.UndoCursor+chunkSize, len(rev))
		chunk := rev[j.UndoCursor:end]
		// A key can change between classifying it and the batch; the batch then
		// reports the conflict and the next pass classifies that key as changed.
		for attempt := 0; ; attempt++ {
			inverse, conflicts, err := s.classify(chunk)
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
		prog.report(j.UndoCursor, len(rev), "undoing")
	}
	return nil
}

// classify splits chunk into the inverse ops to apply and the keys to leave.
func (s *Service) classify(chunk []database.UserMergeOp) (inverse []database.UserMergeOp, conflicts []string, err error) {
	for _, op := range chunk {
		cur, err := s.store.GetRaw(op.Key)
		if err != nil {
			return nil, nil, err
		}
		present := cur != nil
		switch database.UserMergeValueHash(cur, present) {
		case op.AfterHash():
			inverse = append(inverse, invert(op))
		case op.BeforeHash():
			// Already back to what it was (an undo resumed after a crash).
		default:
			conflicts = append(conflicts, fmt.Sprintf("%s (%s): changed after the merge; left as it is", op.Key, op.Family))
		}
	}
	return inverse, conflicts, nil
}
