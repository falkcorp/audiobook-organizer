// file: internal/usermerge/journal.go
// version: 1.0.0
// guid: 91e5d3a0-6b47-4c82-a1f9-d2b70c4e8365
// last-edited: 2026-10-22

package usermerge

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Journal statuses.
const (
	StatusApplying = "applying" // started; resumable (Resume) or reversible (Undo)
	StatusApplied  = "applied"
	StatusUndoing  = "undoing"
	StatusUndone   = "undone"
)

// Journal is the record of one merge: every op with its before-image, how far
// the apply got, and what undo has done. It is the only way back, so it is
// written in the same batch as the ops it covers (database.UserMerger.
// ApplyUserMergeOps) and never pruned.
type Journal struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	SurvivorID string `json:"survivor_id"`
	AbsorbedID string `json:"absorbed_id"`
	// ActorID is the admin who started the merge.
	ActorID string `json:"actor_id"`
	Digest  string `json:"digest"`

	Retirement database.UserRetirement `json:"retirement"`
	// AbsorbedStatusBefore is the status undo puts the absorbed account back to.
	AbsorbedStatusBefore string `json:"absorbed_status_before"`

	Ops []database.UserMergeOp `json:"ops"`
	// Cursor: Ops[:Cursor] are written. Retired: RetireMergedUser ran. Sweeps:
	// how many straggler sweeps appended ops.
	Cursor  int  `json:"cursor"`
	Retired bool `json:"retired"`
	Sweeps  int  `json:"sweeps"`

	// UndoCursor counts ops already processed by undo, from the end of Ops[:Cursor].
	UndoCursor    int      `json:"undo_cursor,omitempty"`
	UndoConflicts []string `json:"undo_conflicts,omitempty"`

	Counts        map[string]FamilyCount `json:"counts"`
	ConflictTotal int                    `json:"conflict_total"`
	Warnings      []string               `json:"warnings,omitempty"`
	Error         string                 `json:"error,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	AppliedAt *time.Time `json:"applied_at,omitempty"`
	UndoneAt  *time.Time `json:"undone_at,omitempty"`
}

// JournalSummary is a Journal without its ops, for lists and operation results.
type JournalSummary struct {
	ID            string                 `json:"id"`
	Status        string                 `json:"status"`
	SurvivorID    string                 `json:"survivor_id"`
	AbsorbedID    string                 `json:"absorbed_id"`
	ActorID       string                 `json:"actor_id"`
	Ops           int                    `json:"ops"`
	Cursor        int                    `json:"cursor"`
	Retired       bool                   `json:"retired"`
	Sweeps        int                    `json:"sweeps"`
	UndoConflicts []string               `json:"undo_conflicts,omitempty"`
	Counts        map[string]FamilyCount `json:"counts,omitempty"`
	ConflictTotal int                    `json:"conflict_total"`
	Warnings      []string               `json:"warnings,omitempty"`
	Error         string                 `json:"error,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	AppliedAt     *time.Time             `json:"applied_at,omitempty"`
	UndoneAt      *time.Time             `json:"undone_at,omitempty"`
}

// Summary returns j without its ops.
func (j *Journal) Summary() JournalSummary {
	return JournalSummary{ID: j.ID, Status: j.Status, SurvivorID: j.SurvivorID, AbsorbedID: j.AbsorbedID,
		ActorID: j.ActorID, Ops: len(j.Ops), Cursor: j.Cursor, Retired: j.Retired, Sweeps: j.Sweeps,
		UndoConflicts: j.UndoConflicts, Counts: j.Counts, ConflictTotal: j.ConflictTotal,
		Warnings: j.Warnings, Error: j.Error, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
		AppliedAt: j.AppliedAt, UndoneAt: j.UndoneAt}
}

func journalKey(id string) string { return database.UserMergeJournalPrefix + id }

// LoadJournal reads one journal.
func (s *Service) LoadJournal(id string) (*Journal, error) {
	raw, err := s.store.GetRaw(journalKey(id))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: %s", ErrJournalNotFound, id)
	}
	var j Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("decode merge journal %s: %w", id, err)
	}
	return &j, nil
}

// ListJournals returns every merge journal, newest first.
func (s *Service) ListJournals() ([]JournalSummary, error) {
	pairs, err := s.store.ScanPrefix(database.UserMergeJournalPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]JournalSummary, 0, len(pairs))
	for _, kv := range pairs {
		var j Journal
		if err := json.Unmarshal(kv.Value, &j); err != nil {
			return nil, fmt.Errorf("decode merge journal %s: %w", kv.Key, err)
		}
		out = append(out, j.Summary())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// save writes the journal on its own (a status change with no ops).
func (s *Service) save(j *Journal) error {
	j.UpdatedAt = s.now()
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return s.store.SetRaw(journalKey(j.ID), data)
}

// refuseIfOpenJournal refuses to start a merge while another merge involving
// either account is unfinished (applying or undoing): finish or undo that one.
func (s *Service) refuseIfOpenJournal(ids ...string) error {
	list, err := s.ListJournals()
	if err != nil {
		return err
	}
	for _, j := range list {
		if j.Status != StatusApplying && j.Status != StatusUndoing {
			continue
		}
		for _, id := range ids {
			if j.SurvivorID == id || j.AbsorbedID == id {
				return fmt.Errorf("%w: merge %s (%s) is unfinished for one of these accounts; resume or undo it first", ErrJournalState, j.ID, j.Status)
			}
		}
	}
	return nil
}
