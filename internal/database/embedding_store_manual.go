// file: internal/database/embedding_store_manual.go
// version: 1.1.0
// guid: e91eddf1-7da1-4f28-a63f-8fe992e52838
// last-edited: 2026-09-25

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// CandidateSourceManual marks a dedup candidate a human put in the review queue
// by hand (POST /api/v1/dedup/candidates) rather than one a scanner produced.
//
// Scanners own their rows: the purge passes delete or reclassify a candidate
// when the scanner that produced it would no longer emit it. A manual
// candidate has no producing scanner, so under those rules it looks stale on
// the very first pass. Every automated pass that deletes, dismisses,
// reclassifies or merges candidates therefore skips rows where
// IsManualCandidate is true; only a human verdict (dismiss or merge in the
// review UI) or the deletion of one of its books ends one.
const CandidateSourceManual = "manual"

// CandidateLayerManual is the Layer written on a candidate created by hand.
// It is what the review UI shows in the layer chip. A scanner row a human
// pins keeps its scanner layer and carries only the Source mark.
const CandidateLayerManual = "manual"

// ErrManualCandidateProtected is returned by DeleteCandidate for a manual
// candidate. Automated passes must skip those rows before calling it.
var ErrManualCandidateProtected = errors.New("candidate was enqueued by hand and is not deleted by automated passes")

// ErrCandidateStatusChanged is returned by the guarded automated writes
// (ReclassifyCandidate, UpdateCandidateLLM) when the row's status is no longer
// the one the pass read: a human decided it, or another pass moved it, or it
// was deleted. The row is left alone.
var ErrCandidateStatusChanged = errors.New("candidate status changed since it was read")

// ReclassifyCandidate is the status write every automated pass (drain-stale,
// purge-legacy-fp, triage and dataset-backfill dismissals) uses instead of
// UpdateCandidateStatus. Under s.mu, the lock EnqueueManualCandidate holds,
// it re-reads the row and moves it from fromStatus to toStatus only if:
//
//   - the row is not a manual candidate (else ErrManualCandidateProtected), and
//   - its status is still fromStatus (else ErrCandidateStatusChanged; a
//     missing row reports the same).
//
// Automated passes list the backlog first and write later, sometimes much
// later. Checking IsManualCandidate on the listed snapshot alone leaves the
// whole scan as a window in which a human can pin the row, and the pass then
// reclassifies it after the human was told it was pinned. Doing the check
// here, at write time and under the pin's own lock, closes that window.
//
// UpdateCandidateStatus stays unguarded on purpose: it is also how a human's
// dismiss or merge verdict ends a manual candidate.
func (s *EmbeddingStore) ReclassifyCandidate(id int64, fromStatus, toStatus string) error {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if err := s.checkClosed(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, found, err := s.readCandRecForUpdate(id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("reclassify candidate %d: row no longer exists: %w", id, ErrCandidateStatusChanged)
	}
	if rec.Source == CandidateSourceManual {
		return fmt.Errorf("reclassify candidate %d to %q: %w", id, toStatus, ErrManualCandidateProtected)
	}
	if rec.Status != fromStatus {
		return fmt.Errorf("reclassify candidate %d: status is %q, expected %q: %w", id, rec.Status, fromStatus, ErrCandidateStatusChanged)
	}
	return s.writeCandidateStatusLocked(id, rec, toStatus)
}

// IsManualCandidate reports whether c was enqueued or pinned by a human. It is
// the one predicate every automated purge / dismiss / merge path checks.
func IsManualCandidate(c DedupCandidate) bool {
	return c.Source == CandidateSourceManual
}

// ManualCandidateOutcome says what a manual enqueue did, or would do, to a pair.
type ManualCandidateOutcome string

const (
	// ManualCandidateCreated: no row existed for the pair; a pending manual
	// candidate was (or would be) created.
	ManualCandidateCreated ManualCandidateOutcome = "created"
	// ManualCandidateAlreadyOpen: a pending row already exists. It is returned
	// as is, except that a scanner row is pinned (given the manual Source mark)
	// so the purge passes leave it in the queue.
	ManualCandidateAlreadyOpen ManualCandidateOutcome = "already_open"
	// ManualCandidateReopened: the row carried a machine reclassification
	// (stale-drain, stale-fp). Those are not verdicts, so the human's request
	// puts it back to pending and pins it.
	ManualCandidateReopened ManualCandidateOutcome = "reopened"
	// ManualCandidateDecided: the row carries a terminal verdict (dismissed or
	// merged). It is reported and left alone: a manual enqueue does not
	// overturn a decision. The reviewer can find it on the Dismissed / Merged
	// tabs.
	ManualCandidateDecided ManualCandidateOutcome = "decided"
)

// ManualCandidateResult reports one manual enqueue.
type ManualCandidateResult struct {
	// Candidate is the row as it stands after the call. For a dry-run create
	// it is the row that would be written, with ID 0.
	Candidate DedupCandidate
	Outcome   ManualCandidateOutcome
	// Pinned is true when an existing scanner row gained (or, on a dry run,
	// would gain) the manual Source mark.
	Pinned bool
}

// PlanManualCandidate reports what EnqueueManualCandidate would do for the
// pair, without writing anything.
func (s *EmbeddingStore) PlanManualCandidate(entityType, aID, bID, note string) (*ManualCandidateResult, error) {
	return s.manualCandidate(entityType, aID, bID, note, false)
}

// EnqueueManualCandidate puts the pair in the dedup review queue as a pending
// manual candidate. It is idempotent: an open row for the pair is returned
// (pinned if a scanner produced it), a machine-reclassified row is reopened,
// and a decided row is reported untouched. See ManualCandidateOutcome.
//
// The pair-key check and the write happen under s.mu, the lock
// UpsertCandidateNew holds, so a scanner upsert of the same pair cannot race
// this into two rows.
func (s *EmbeddingStore) EnqueueManualCandidate(entityType, aID, bID, note string) (*ManualCandidateResult, error) {
	return s.manualCandidate(entityType, aID, bID, note, true)
}

func (s *EmbeddingStore) manualCandidate(entityType, aID, bID, note string, write bool) (*ManualCandidateResult, error) {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if entityType == "" || aID == "" || bID == "" {
		return nil, fmt.Errorf("manual candidate: entity type and both IDs are required")
	}
	if aID == bID {
		return nil, fmt.Errorf("manual candidate: a pair needs two different entities")
	}
	if aID > bID {
		aID, bID = bID, aID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pairKey := dedupPairKey(entityType, aID, bID)
	idBytes, closer, err := s.db.Get(pairKey)
	if err != nil && err != pebble.ErrNotFound {
		return nil, fmt.Errorf("manual candidate pair lookup: %w", err)
	}
	if err == pebble.ErrNotFound {
		return s.createManualCandidateLocked(entityType, aID, bID, note, pairKey, write)
	}
	idHex := string(idBytes)
	closer.Close()
	id, err := strconv.ParseInt(idHex, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("manual candidate: parse candidate id %q: %w", idHex, err)
	}
	rec, ok := readCandRec(s.db, id)
	if !ok {
		// A pair key naming a missing or unreadable record: refuse rather than
		// write a second record for the pair or overwrite one we cannot read.
		return nil, fmt.Errorf("manual candidate: pair %s/%s points at candidate %d, which cannot be read", aID, bID, id)
	}

	res := &ManualCandidateResult{}
	oldStatus := rec.Status
	switch {
	case IsTerminalCandidateStatus(rec.Status):
		res.Outcome = ManualCandidateDecided
		res.Candidate = candRecToCandidate(id, rec)
		return res, nil
	case rec.Status == "pending":
		res.Outcome = ManualCandidateAlreadyOpen
	default:
		res.Outcome = ManualCandidateReopened
		rec.Status = "pending"
	}
	if rec.Source != CandidateSourceManual {
		res.Pinned = true
		rec.Source = CandidateSourceManual
		rec.SourceNote = note
	}
	if !res.Pinned && oldStatus == rec.Status {
		// Already an open manual candidate: nothing to write.
		res.Candidate = candRecToCandidate(id, rec)
		return res, nil
	}
	rec.UpdatedAt = time.Now().UnixNano()
	res.Candidate = candRecToCandidate(id, rec)
	if !write {
		return res, nil
	}

	data, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("manual candidate marshal %d: %w", id, err)
	}
	b := s.db.NewBatch()
	defer b.Close()
	if err := b.Set(dedupRecKey(id), data, nil); err != nil {
		return nil, err
	}
	if oldStatus != rec.Status {
		if oldStatus != "" {
			if err := b.Delete(dedupStatusIdxKey(oldStatus, id), nil); err != nil {
				return nil, err
			}
		}
		if err := b.Set(dedupStatusIdxKey(rec.Status, id), nil, nil); err != nil {
			return nil, err
		}
	}
	// A human asked for this row; make it durable now rather than riding the
	// NoSync scanner path (candidateWriteOpts), which a hard crash may drop.
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, fmt.Errorf("manual candidate commit %d: %w", id, err)
	}
	return res, nil
}

// createManualCandidateLocked writes a new pending manual candidate: the
// record, the pair key, both entity-index sides and the status index, in one
// batch, exactly as UpsertCandidateNew's new-pair branch does. s.mu must be
// held. With write=false it only reports the row it would write.
func (s *EmbeddingStore) createManualCandidateLocked(entityType, aID, bID, note string, pairKey []byte, write bool) (*ManualCandidateResult, error) {
	now := time.Now().UnixNano()
	rec := candRec{
		EntityType: entityType,
		EntityAID:  aID,
		EntityBID:  bID,
		Layer:      CandidateLayerManual,
		Status:     "pending",
		CreatedAt:  now,
		UpdatedAt:  now,
		Source:     CandidateSourceManual,
		SourceNote: note,
	}
	if !write {
		return &ManualCandidateResult{Outcome: ManualCandidateCreated, Candidate: candRecToCandidate(0, rec)}, nil
	}

	b := s.db.NewBatch()
	defer b.Close()
	id, err := s.nextID(b)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("manual candidate marshal: %w", err)
	}
	if err := b.Set(dedupRecKey(id), data, nil); err != nil {
		return nil, err
	}
	if err := b.Set(pairKey, []byte(fmt.Sprintf("%016x", id)), nil); err != nil {
		return nil, err
	}
	if err := b.Set(dedupEntityKey(entityType, aID, id), nil, nil); err != nil {
		return nil, err
	}
	if err := b.Set(dedupEntityKey(entityType, bID, id), nil, nil); err != nil {
		return nil, err
	}
	if err := b.Set(dedupStatusIdxKey(rec.Status, id), nil, nil); err != nil {
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, fmt.Errorf("manual candidate commit: %w", err)
	}
	return &ManualCandidateResult{Outcome: ManualCandidateCreated, Candidate: candRecToCandidate(id, rec)}, nil
}
