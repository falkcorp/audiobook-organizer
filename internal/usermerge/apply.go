// file: internal/usermerge/apply.go
// version: 1.0.0
// guid: 3f6a0c82-d417-4b95-8e23-5a9c1e7d4b06
// last-edited: 2026-10-22

package usermerge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// ApplyRequest is a confirmed merge: the ids, the digest of the preview the admin
// looked at, and whether they acknowledged the owner-identity stop.
type ApplyRequest struct {
	ActorID    string
	SurvivorID string
	AbsorbedID string
	Digest     string
	// AcknowledgeOwnerIdentity lets the merge proceed when the account being
	// merged away holds the owner's sign-in (Plan.OwnerIdentityOnAbsorbed).
	AcknowledgeOwnerIdentity bool
}

// Progress reports done of total ops with a short message. It may be nil.
type Progress func(done, total int, msg string)

func (p Progress) report(done, total int, msg string) {
	if p != nil {
		p(done, total, msg)
	}
}

// lockKeys is every (user, book) pair the ops write, for both accounts: the
// stripes the ABS, web and iTunes writers of listening state also take.
func lockKeys(ops []database.UserMergeOp, a, b string) []database.UserBookKey {
	seen := map[string]bool{}
	var out []database.UserBookKey
	for _, op := range ops {
		var rest string
		switch {
		case strings.HasPrefix(op.Key, "ubs:"):
			rest = strings.TrimPrefix(op.Key, "ubs:")
		case strings.HasPrefix(op.Key, "upos:"):
			rest = strings.TrimPrefix(op.Key, "upos:")
		case strings.HasPrefix(op.Key, "idx:ubs:status:"):
			rest = strings.TrimPrefix(op.Key, "idx:ubs:status:")
			// idx:ubs:status:<user>:<status>:<book>; drop the status segment.
			parts := strings.SplitN(rest, ":", 3)
			if len(parts) == 3 {
				rest = parts[0] + ":" + parts[2]
			}
		default:
			continue
		}
		parts := strings.SplitN(rest, ":", 3)
		if len(parts) < 2 {
			continue
		}
		book := parts[1]
		if seen[book] {
			continue
		}
		seen[book] = true
		out = append(out, database.UserBookKey{UserID: a, BookID: book}, database.UserBookKey{UserID: b, BookID: book})
	}
	return out
}

// Apply merges the absorbed account into the survivor. It re-plans, refuses on
// a refusal or a digest that is not the preview's, locks the absorbed account
// and ends its sessions, takes the merge lock and the per-(user, book) stripes,
// re-plans under them and compares the digest again, then writes the journal and
// the ops in chunks, retires the absorbed account, and sweeps for rows its
// in-flight requests wrote meanwhile.
//
// A refusal or a digest mismatch writes nothing except that the absorbed account
// is left locked (its sessions ended): the admin previews again.
func (s *Service) Apply(ctx context.Context, req ApplyRequest, prog Progress) (*Journal, error) {
	runMu.Lock()
	defer runMu.Unlock()
	if err := s.refuseIfOpenJournal(req.SurvivorID, req.AbsorbedID); err != nil {
		return nil, err
	}
	plan, err := s.BuildPlan(ctx, PlanRequest{ActorID: req.ActorID, SurvivorID: req.SurvivorID, AbsorbedID: req.AbsorbedID})
	if err != nil {
		return nil, err
	}
	if !plan.Applicable() {
		return nil, &RefusedError{Plan: plan}
	}
	if req.Digest == "" || req.Digest != plan.Digest {
		return nil, ErrDigestMismatch
	}
	if plan.OwnerIdentityOnAbsorbed && !req.AcknowledgeOwnerIdentity {
		return nil, ErrOwnerIdentityNotAcknowledged
	}

	abs, err := s.mustUser(req.AbsorbedID, "absorbed account")
	if err != nil {
		return nil, err
	}
	statusBefore := abs.Status
	prog.report(0, plan.Ops, "locking the account being merged away")
	if err := s.lockAbsorbed(abs); err != nil {
		return nil, err
	}

	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	unlock := database.LockUserBookStates(lockKeys(plan.ops, req.SurvivorID, req.AbsorbedID)...)
	defer unlock()

	plan2, err := s.BuildPlan(ctx, PlanRequest{ActorID: req.ActorID, SurvivorID: req.SurvivorID, AbsorbedID: req.AbsorbedID})
	if err != nil {
		return nil, err
	}
	if !plan2.Applicable() {
		return nil, &RefusedError{Plan: plan2}
	}
	if plan2.Digest != req.Digest {
		return nil, ErrDigestMismatch
	}

	now := s.now()
	j := &Journal{
		ID: s.newID(), Status: StatusApplying, SurvivorID: req.SurvivorID, AbsorbedID: req.AbsorbedID,
		ActorID: req.ActorID, Digest: plan2.Digest, Retirement: plan2.retire, AbsorbedStatusBefore: statusBefore,
		Ops: plan2.ops, Counts: plan2.Counts, ConflictTotal: plan2.ConflictTotal, Warnings: plan2.Warnings,
		CreatedAt: now, UpdatedAt: now,
	}
	j.Retirement.MergedAt = now
	if err := s.save(j); err != nil {
		return nil, fmt.Errorf("write merge journal: %w", err)
	}
	if err := s.drive(ctx, j, prog); err != nil {
		return j, err
	}
	return j, nil
}

// Resume continues an unfinished apply from its journal's cursor. It is only
// ever reached by an admin's click (a merge is never resumed automatically).
func (s *Service) Resume(ctx context.Context, journalID string, prog Progress) (*Journal, error) {
	runMu.Lock()
	defer runMu.Unlock()
	j, err := s.LoadJournal(journalID)
	if err != nil {
		return nil, err
	}
	if j.Status != StatusApplying {
		return nil, fmt.Errorf("%w: merge %s is %s, only an applying merge can be resumed", ErrJournalState, j.ID, j.Status)
	}
	abs, err := s.mustUser(j.AbsorbedID, "absorbed account")
	if err != nil {
		return nil, err
	}
	if err := s.lockAbsorbed(abs); err != nil {
		return nil, err
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	unlock := database.LockUserBookStates(lockKeys(j.Ops, j.SurvivorID, j.AbsorbedID)...)
	defer unlock()
	// A resume gets a fresh sweep budget: rows left over from a run that gave up
	// are exactly what it is for.
	j.Error, j.Sweeps = "", 0
	if err := s.drive(ctx, j, prog); err != nil {
		return j, err
	}
	return j, nil
}

// lockAbsorbed locks the absorbed account (a field-scoped write that bumps its
// credential epoch, which refuses every ABS session it has) and ends its web
// sessions. It does not go through the Deactivate handler, which also revokes
// the account's guest and Cloudflare Access grant: that is a different decision.
func (s *Service) lockAbsorbed(abs *database.User) error {
	if abs.Status != "locked" {
		if _, err := s.creds.SetUserStatus(abs.ID, "locked"); err != nil {
			return fmt.Errorf("lock the account being merged away: %w", err)
		}
	}
	sessions, err := s.store.ListUserSessions(abs.ID)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	for _, sess := range sessions {
		if sess.Revoked {
			continue
		}
		if err := s.store.RevokeSession(sess.ID); err != nil {
			return fmt.Errorf("revoke session %s: %w", sess.ID, err)
		}
	}
	if s.abs != nil {
		if _, err := s.abs.RevokeAllABSSessionsForUser(abs.ID); err != nil {
			return fmt.Errorf("revoke Audiobookshelf sessions: %w", err)
		}
	}
	return nil
}

// drive runs a journal to the end: the remaining ops, the retire step, the
// straggler sweep, then the applied mark. The caller holds the locks.
func (s *Service) drive(ctx context.Context, j *Journal, prog Progress) error {
	fail := func(err error) error {
		j.Error = err.Error()
		_ = s.save(j) // best effort: the journal still says where it stopped
		return err
	}
	if err := s.applyRemaining(ctx, j, prog); err != nil {
		return fail(err)
	}
	if !j.Retired {
		prog.report(j.Cursor, len(j.Ops), "retiring the merged-away account")
		if err := s.merger.RetireMergedUser(j.Retirement); err != nil {
			return fail(err)
		}
		j.Retired = true
		if err := s.save(j); err != nil {
			return fail(err)
		}
	}
	clean := false
	for j.Sweeps < maxSweeps {
		more, err := s.sweepOnce(ctx, j)
		if err != nil {
			return fail(err)
		}
		if !more {
			clean = true
			break
		}
		if err := s.applyRemaining(ctx, j, prog); err != nil {
			return fail(err)
		}
	}
	if !clean {
		// The sweep limit was reached with the last sweep's ops applied: look once
		// more before calling the merge clean.
		more, err := s.hasStragglers(ctx, j)
		if err != nil {
			return fail(err)
		}
		if more {
			j.Warnings = append(j.Warnings, "rows were still appearing under the merged-away account after the final sweep; resume the merge to move them")
			return fail(fmt.Errorf("%w: rows keep appearing under the merged-away account", ErrJournalState))
		}
	}
	now := s.now()
	j.Status, j.AppliedAt, j.Error = StatusApplied, &now, ""
	if err := s.save(j); err != nil {
		return err
	}
	prog.report(len(j.Ops), len(j.Ops), "merge applied")
	if s.audit != nil {
		s.audit(fmt.Sprintf("merged user %s into %s", j.AbsorbedID, j.SurvivorID), map[string]any{
			"journal_id": j.ID, "survivor_id": j.SurvivorID, "absorbed_id": j.AbsorbedID,
			"actor_id": j.ActorID, "ops": len(j.Ops), "conflicts": j.ConflictTotal,
		})
	}
	return nil
}

// applyRemaining writes Ops[Cursor:] in chunks, each chunk advancing the cursor
// in the same batch.
func (s *Service) applyRemaining(ctx context.Context, j *Journal, prog Progress) error {
	for j.Cursor < len(j.Ops) {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(j.Cursor+chunkSize, len(j.Ops))
		next := *j
		next.Cursor, next.UpdatedAt = end, s.now()
		data, err := json.Marshal(&next)
		if err != nil {
			return err
		}
		if err := s.merger.ApplyUserMergeOps(j.Ops[j.Cursor:end], journalKey(j.ID), data); err != nil {
			return err
		}
		j.Cursor, j.UpdatedAt = end, next.UpdatedAt
		prog.report(j.Cursor, len(j.Ops), "merging")
	}
	return nil
}

// sweepOnce plans whatever the absorbed account holds now and appends it to the
// journal. It reports whether it found anything.
func (s *Service) sweepOnce(ctx context.Context, j *Journal) (bool, error) {
	ops, err := s.stragglerOps(ctx, j)
	if err != nil || len(ops) == 0 {
		return false, err
	}
	j.Ops = append(j.Ops, ops...)
	j.Sweeps++
	return true, s.save(j)
}

func (s *Service) hasStragglers(ctx context.Context, j *Journal) (bool, error) {
	ops, err := s.stragglerOps(ctx, j)
	return len(ops) > 0, err
}

func (s *Service) stragglerOps(ctx context.Context, j *Journal) ([]database.UserMergeOp, error) {
	surv, err := s.mustUser(j.SurvivorID, "survivor")
	if err != nil {
		return nil, err
	}
	abs, err := s.mustUser(j.AbsorbedID, "absorbed account")
	if err != nil {
		return nil, err
	}
	d, err := s.merger.ScanUserData(abs.ID)
	if err != nil {
		return nil, err
	}
	scratch := &Plan{Counts: map[string]FamilyCount{}}
	b := newBuilder(s, scratch)
	if err := b.planData(ctx, surv, abs, d); err != nil {
		return nil, err
	}
	if len(b.refused) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrJournalState, strings.Join(b.refused, "; "))
	}
	return b.finalize(), nil
}

// mustUser reads a user row that has to exist.
func (s *Service) mustUser(id, label string) (*database.User, error) {
	u, err := s.store.GetUserByID(id)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if u == nil {
		return nil, fmt.Errorf("read %s: %w: %s", label, database.ErrUserNotFound, id)
	}
	return u, nil
}
