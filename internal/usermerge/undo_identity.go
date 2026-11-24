// file: internal/usermerge/undo_identity.go
// version: 0.1.0
// guid: 3c8f1a65-b2d4-4e97-9a0c-7d5e6f184b23
// last-edited: 2026-11-22

package usermerge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// The undo of an identity-era merge (F4-08 B10p; 05-authn.md §11 "Merges at the
// cutover"). The merge was one batch (applyIdentity), so its undo is one batch
// too: the inverse of the books rows, the journal's move from applied to undone
// and core's Admin.StageUserUnmerge commit together or not at all. Nothing is
// half-undone, so there is no StatusUndoing, no UndoCursor to resume and no
// chunking.
//
// Two rules decide what is undone, one per half, and they differ on purpose:
//
//   - CORE's half is whole or refused. A key or a sign-in the merge moved is
//     put back only while it is still as the merge left it; if any is not, the
//     undo is refused (key_moved, signin_moved, ...), because an account that
//     got back some of its credentials and not others is worse than one that
//     got none.
//   - BOOKS' half keeps HEAD's rules (classify): a row written since the merge
//     is left as it is and reported in UndoConflicts, a listening-state unit
//     stays whole, and every other row is put back. Those rows are the
//     survivor's own data now, and overwriting a later listen position is the
//     one thing an undo must never do.
//
// What is not undone: the open invites and temp-login links the absorbed
// account created (the merge deleted them), and the allow-list pin of its
// address (the merge only pinned it).

// Codes of an UndoRefusal that books decides (core's own come through
// refusalOf and the codes of core's MergeRefusal).
const (
	// CodeSweepPending: the merge's straggler sweep did not finish; run it
	// (resume) before the undo.
	CodeSweepPending = "sweep_pending"
	// CodeReceiptMissing: the journal does not say which sign-ins and keys the
	// merge moved (it was written before the receipt existed), so the undo
	// cannot tell them from the survivor's own and will not guess.
	CodeReceiptMissing = "receipt_missing"
	// CodeUndoConflict: an account or a row changed while the undo was being
	// prepared and the retries ran out.
	CodeUndoConflict = "conflict"
)

// ErrUndoRefused is what an UndoRefusal matches.
var ErrUndoRefused = errors.New("usermerge: the undo is refused")

// UndoRefusal is an undo refused for a reason the admin can act on, with a code
// the handler answers 409 with. Its text names no address, sign-in, key or row:
// it reaches operation rows that non-admins can read.
type UndoRefusal struct {
	Code string
	Text string
}

func (e *UndoRefusal) Error() string { return ErrUndoRefused.Error() + " (" + e.Code + "): " + e.Text }

// Is reports whether target is ErrUndoRefused.
func (e *UndoRefusal) Is(target error) bool { return target == ErrUndoRefused }

// undoRecord is core's UnmergeRecord for j's merge, or the reason j cannot be
// undone: a journal with no core half is a pre-flip one and stays permanent; a
// journal that is not applied is in the wrong state; a merge whose sweep did
// not finish or whose journal lacks the receipt is refused with a code.
func undoRecord(j *Journal) (coreauth.UnmergeRecord, error) {
	c := j.Core
	if c == nil {
		return coreauth.UnmergeRecord{}, ErrMergePermanent
	}
	if j.Status != StatusApplied {
		return coreauth.UnmergeRecord{}, fmt.Errorf("%w: merge %s is %s", ErrJournalState, j.ID, j.Status)
	}
	if c.SweepPending {
		return coreauth.UnmergeRecord{}, &UndoRefusal{Code: CodeSweepPending,
			Text: "the merge's straggler sweep did not finish, so rows may still sit under the merged-away account; resume the merge to finish the sweep, then undo it"}
	}
	if len(c.IdentityIDs) != len(c.IdentityProviders) {
		return coreauth.UnmergeRecord{}, &UndoRefusal{Code: CodeReceiptMissing,
			Text: "the merge journal does not record which sign-ins moved, so they cannot be told from the surviving account's own"}
	}
	versions := make(map[string]int64, len(c.APIKeyIDs))
	for _, id := range c.APIKeyIDs {
		v, ok := c.APIKeyVersions[id]
		if !ok || v <= 0 {
			return coreauth.UnmergeRecord{}, &UndoRefusal{Code: CodeReceiptMissing,
				Text: "the merge journal does not record the version of a key that moved, so a change to it cannot be told"}
		}
		versions[id] = v
	}
	roles := make([]rbac.RoleName, len(c.RolesAdded))
	for i, r := range c.RolesAdded {
		roles[i] = rbac.RoleName(r)
	}
	return coreauth.UnmergeRecord{
		Source: coreauth.UserID(j.AbsorbedID), Target: coreauth.UserID(j.SurvivorID),
		SourceVersionAfter:        c.SourceVersionBefore + 1,
		SourceStatusBefore:        coreauth.UserStatus(c.SourceStatusBefore),
		SourceEmailBefore:         c.SourceEmailBefore,
		SourceEmailVerifiedBefore: c.SourceEmailVerifiedBefore,
		TargetEmailBefore:         c.TargetEmailBefore,
		TargetEmailVerifiedBefore: c.TargetEmailVerifiedBefore,
		EmailOutcome:              coreauth.MergeEmailOutcome(c.EmailOutcome),
		RolesAdded:                roles,
		APIKeys:                   versions,
		Identities:                slices.Clone(c.IdentityIDs),
	}, nil
}

// undoError classifies an error of the undo's core half: a refusal core decided
// becomes an UndoRefusal with its code (the wording says undo, not merge);
// anything else is returned as it is.
func undoError(err error) error {
	code, text, ok := refusalOf(err)
	if !ok {
		return err
	}
	switch code {
	case CodeLastAdmin:
		text = "the undo would leave no active administrator"
	case CodeEmailInUse:
		text = "another account holds the address the undo would give back verified"
	case CodeInactiveUser:
		text = "the surviving account is not active"
	}
	return &UndoRefusal{Code: code, Text: text}
}

// isUndoRace reports whether err is a record changing under the undo: a plan
// that went stale, a commit that lost its Expect, or a books row that moved
// between classifying it and the batch.
func isUndoRace(err error) bool {
	return errors.Is(err, coreauth.ErrMergeConflict) || errors.Is(err, storage.ErrConflict) || errors.Is(err, database.ErrUserMergeConflict)
}

// CheckUndo is the synchronous check the handler runs, with the request's own
// principal in ctx, before it starts an undo: it reads the journal and asks
// core for the unmerge plan, so every refusal is answered on the request with
// its code instead of as a failed background run. It writes nothing. Before the
// identity flip there is nothing to check (HEAD's undo stands) and it returns
// nil.
func (s *Service) CheckUndo(ctx context.Context, journalID string) error {
	if !s.flipped() {
		return nil
	}
	if err := s.CheckAvailable(); err != nil {
		return err
	}
	j, err := s.LoadJournal(journalID)
	if err != nil {
		return err
	}
	rec, err := undoRecord(j)
	if err != nil {
		return err
	}
	if _, _, err := s.core.Admin.PlanUserUnmerge(ctx, rec); err != nil {
		return undoError(err)
	}
	return nil
}

// undoRetries is how many times the undo plans again over a record that changed
// under it, as HEAD's undo does for a row.
const undoRetries = 3

// undoIdentity is Undo once accounts live in core: bind the acting admin's
// principal (re-validated through core, as apply does), then under the merge
// locks classify the books rows, plan core's half and commit the inverse rows,
// the journal and core's StageUserUnmerge in one batch. The caller holds runMu.
func (s *Service) undoIdentity(ctx context.Context, req UndoRequest, prog Progress) (*Journal, error) {
	if err := s.CheckAvailable(); err != nil {
		return nil, err
	}
	// The journal's own answers come first (a pre-flip journal is permanent, an
	// unfinished sweep is a refusal), so the admin is told why before being
	// asked for a session.
	j, err := s.LoadJournal(req.JournalID)
	if err != nil {
		return nil, err
	}
	if _, err := undoRecord(j); err != nil {
		return nil, err
	}
	ctx, err = s.bindPrincipal(ctx, ApplyRequest{ActorID: req.ActorID, Principal: req.Principal})
	if err != nil {
		return nil, err
	}

	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	written := j.Ops[:j.Cursor]
	unlock := database.LockUserBookStates(lockKeys(written, j.SurvivorID, j.AbsorbedID)...)
	defer unlock()

	groups := undoGroups(written)
	var (
		next  Journal
		after coreauth.AfterCommit
	)
	for attempt := 0; ; attempt++ {
		// The journal is read again under the locks: what is undone is what it
		// holds now, and a journal that changed state since the request is refused.
		if j, err = s.LoadJournal(req.JournalID); err != nil {
			return nil, err
		}
		rec, err := undoRecord(j)
		if err != nil {
			return nil, err
		}
		// Core's early refusals, before anything is classified or staged.
		cplan, _, err := s.core.Admin.PlanUserUnmerge(ctx, rec)
		if err != nil {
			if isUndoRace(err) && attempt < undoRetries {
				continue
			}
			return nil, undoError(err)
		}
		inverse, conflicts, err := s.classify(groups, j.SurvivorID)
		if err != nil {
			return nil, err
		}
		// The cap comes before anything is staged or written.
		if n := s.estimateBatchBytes(inverse); n > s.mergeBatchCap() {
			return nil, fmt.Errorf("%w (estimated %d bytes, cap %d)", ErrMergeTooLarge, n, s.mergeBatchCap())
		}
		now := s.now()
		next = *j
		next.Status, next.Error, next.Retired = StatusUndone, "", false
		next.UndoCursor, next.UndoConflicts = len(written), slices.Clone(conflicts)
		next.UpdatedAt, next.UndoneAt = now, &now
		data, err := json.Marshal(&next)
		if err != nil {
			return nil, err
		}
		after = nil
		err = s.idMerger.ApplyUserMergeIdentity(inverse, journalKey(j.ID), data, func(b *storage.Batch) error {
			a, err := s.core.Admin.StageUserUnmerge(ctx, b, cplan)
			if err != nil {
				return err
			}
			after = a
			return nil
		})
		if err == nil {
			break
		}
		if isUndoRace(err) {
			if attempt < undoRetries {
				continue
			}
			return nil, &UndoRefusal{Code: CodeUndoConflict, Text: "an account or a row changed while the undo was being prepared; try again"}
		}
		return nil, undoError(err)
	}
	// The locks are still held and the batch committed: publish what core
	// staged (the survivor's sessions, rbac's assignment change, the absorbed
	// account's lifecycle) exactly once.
	if after != nil {
		after(ctx)
	}
	*j = next
	prog.report(len(written), len(written), "merge undone")
	if s.audit != nil {
		s.audit(fmt.Sprintf("undid the merge of user %s into %s", j.AbsorbedID, j.SurvivorID), map[string]any{
			"journal_id": j.ID, "survivor_id": j.SurvivorID, "absorbed_id": j.AbsorbedID,
			"actor_id": req.ActorID, "conflicts": len(j.UndoConflicts),
		})
	}
	return j, nil
}

// resumeIdentity is Resume once accounts live in core. An identity-era merge is
// one batch and has nothing half-applied, so the only thing left to resume is
// its post-commit straggler sweep: a journal that is applied with
// Core.SweepPending set re-runs the (idempotent) sweep and clears the marker by
// the same path a clean first run does, failing loudly the same way when the
// sweep fails again. Every other journal stays permanent. The caller holds
// runMu.
func (s *Service) resumeIdentity(ctx context.Context, journalID string, prog Progress) (*Journal, error) {
	if err := s.CheckAvailable(); err != nil {
		return nil, err
	}
	j, err := s.LoadJournal(journalID)
	if err != nil {
		return nil, err
	}
	if j.Core == nil || j.Status != StatusApplied || !j.Core.SweepPending {
		return nil, ErrMergePermanent
	}
	prog.report(0, len(j.Ops), "finishing the straggler sweep")
	sweepErr := s.sweepAfterCommit(ctx, j)
	if s.audit != nil {
		s.audit(fmt.Sprintf("resumed the straggler sweep of the merge of user %s into %s", j.AbsorbedID, j.SurvivorID), map[string]any{
			"journal_id": j.ID, "survivor_id": j.SurvivorID, "absorbed_id": j.AbsorbedID,
			"sweep_pending": j.Core.SweepPending, "ops": len(j.Ops),
		})
	}
	if sweepErr != nil {
		return j, sweepErr
	}
	prog.report(len(j.Ops), len(j.Ops), "straggler sweep finished")
	return j, nil
}
