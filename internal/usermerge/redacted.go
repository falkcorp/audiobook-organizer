// file: internal/usermerge/redacted.go
// version: 1.0.0
// guid: 7d3b9e41-6a5c-4f28-b0e7-2c84a1f93d60
// last-edited: 2026-10-23

package usermerge

import (
	"fmt"
	"slices"
	"time"
)

// The full Plan and Journal carry usernames, email addresses and linked
// sign-ins, and name individual rows. Operation rows are readable by anyone
// with library.view through the operations read routes, so the operation never
// stores either one: it stores the redacted forms below. The full detail is
// served only by the admin-gated Users merge routes (preview rebuilds the plan
// on demand, so nothing PII-bearing is persisted a second time).

// PlanSummary is a Plan without accounts, sign-ins, conflict rows or any
// refusal that quotes a row. Account ids are opaque and stay.
type PlanSummary struct {
	SurvivorID              string                 `json:"survivor_id"`
	AbsorbedID              string                 `json:"absorbed_id"`
	Applicable              bool                   `json:"applicable"`
	Digest                  string                 `json:"digest"`
	Ops                     int                    `json:"ops"`
	Counts                  map[string]FamilyCount `json:"counts"`
	ConflictTotal           int                    `json:"conflict_total"`
	OwnerIdentityHolder     Holder                 `json:"owner_identity_holder"`
	OwnerIdentityOnAbsorbed bool                   `json:"owner_identity_on_absorbed"`
	WarningCount            int                    `json:"warning_count"`
	RefusalCount            int                    `json:"refusal_count"`
	// Refusals holds the refusals that say nothing about a particular row or
	// account; RowRefusalCount is how many more were left out.
	Refusals        []string `json:"refusals"`
	RowRefusalCount int      `json:"row_refusal_count"`
}

// Summary returns the redacted form of p.
func (p *Plan) Summary() PlanSummary {
	s := PlanSummary{
		SurvivorID: p.SurvivorID, AbsorbedID: p.AbsorbedID, Applicable: p.Applicable(),
		Digest: p.Digest, Ops: p.Ops, Counts: p.Counts, ConflictTotal: p.ConflictTotal,
		OwnerIdentityHolder: p.OwnerIdentityHolder, OwnerIdentityOnAbsorbed: p.OwnerIdentityOnAbsorbed,
		WarningCount: len(p.Warnings), RefusalCount: len(p.Refusals), Refusals: []string{},
	}
	for _, r := range p.Refusals {
		if slices.Contains(p.rowRefusals, r) {
			s.RowRefusalCount++
			continue
		}
		s.Refusals = append(s.Refusals, r)
	}
	return s
}

// PublicError is the error text safe for an operation row: RefusedError's
// message without the refusals that quote rows.
func (p *Plan) PublicError() string {
	s := p.Summary()
	return fmt.Sprintf("%s: %v (+%d refusal(s) naming specific rows; see the preview on the Users page)",
		ErrRefused, s.Refusals, s.RowRefusalCount)
}

// JournalResult is a Journal without its ops, undo-conflict keys, warnings and
// error text. The Users page's merge list (admin-gated) has those.
type JournalResult struct {
	ID                string                 `json:"id"`
	Status            string                 `json:"status"`
	SurvivorID        string                 `json:"survivor_id"`
	AbsorbedID        string                 `json:"absorbed_id"`
	Ops               int                    `json:"ops"`
	Cursor            int                    `json:"cursor"`
	Retired           bool                   `json:"retired"`
	Sweeps            int                    `json:"sweeps"`
	Counts            map[string]FamilyCount `json:"counts,omitempty"`
	ConflictTotal     int                    `json:"conflict_total"`
	UndoConflictCount int                    `json:"undo_conflict_count"`
	WarningCount      int                    `json:"warning_count"`
	Failed            bool                   `json:"failed"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
	AppliedAt         *time.Time             `json:"applied_at,omitempty"`
	UndoneAt          *time.Time             `json:"undone_at,omitempty"`
}

// OpResult returns the redacted form of j.
func (j *Journal) OpResult() JournalResult {
	return JournalResult{ID: j.ID, Status: j.Status, SurvivorID: j.SurvivorID, AbsorbedID: j.AbsorbedID,
		Ops: len(j.Ops), Cursor: j.Cursor, Retired: j.Retired, Sweeps: j.Sweeps, Counts: j.Counts,
		ConflictTotal: j.ConflictTotal, UndoConflictCount: len(j.UndoConflicts), WarningCount: len(j.Warnings),
		Failed: j.Error != "", CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, AppliedAt: j.AppliedAt, UndoneAt: j.UndoneAt}
}
