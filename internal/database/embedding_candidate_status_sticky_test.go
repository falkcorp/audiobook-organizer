// file: internal/database/embedding_candidate_status_sticky_test.go
// version: 1.0.0
// guid: 8f61d0b3-45ac-4e29-91d7-6b2f83c0e5a4
// last-edited: 2026-07-17

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpsertCandidate_TerminalStatusSurvivesRescan is a regression test for the
// dismiss-resurrect bug.
//
// UpsertCandidateNew carefully protects Layer/Similarity/ScoreBreakdown/Band/
// FormulaVersion behind a `protected` check, but assigned Status unconditionally:
//
//	oldStatus := existing.Status
//	existing.Status = c.Status   // <- clobbered a human verdict
//
// Every dedup scan re-upserts the same pairs with Status "pending", so a
// dismissed candidate silently returned to the review queue on each run.
// Measured on a full-fidelity replica of production: a single dedup.full-scan
// flipped exactly 43 candidates dismissed -> pending (all layer=exact,
// similarity=1), while a prod control instance stayed at 1351 dismissed.
//
// This matters beyond annoyance: if dismissals do not survive a scan, the review
// queue can never converge -- the same false positives come back forever.
// The purge path already treats these as sticky ("CRITICAL: Only purge PENDING
// candidates. Merged and dismissed rows..." in dedup/engine.go), so the engine
// already assumed the behavior this test pins down.
func TestUpsertCandidate_TerminalStatusSurvivesRescan(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verdict  string // status a reviewer/auto-resolve recorded
		incoming string // status the rescan re-upserts with
		want     string
	}{
		{"dismissed survives a rescan", "dismissed", "pending", "dismissed"},
		{"merged survives a rescan", "merged", "pending", "merged"},
		{"dismissed may be upgraded to merged", "dismissed", "merged", "merged"},
		{"pending is still updatable", "pending", "dismissed", "dismissed"},
		{"stale-drain stays refreshable", "stale-drain", "pending", "pending"},
		{"stale-fp stays refreshable", "stale-fp", "pending", "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestEmbeddingStore(t)

			base := DedupCandidate{
				EntityType: "book",
				EntityAID:  "01KNDBRDX5A7NPB408V2VTG9XY",
				EntityBID:  "01KQAW4JMN3XXCB8HKD62F3K8W",
				Layer:      "exact",
				Similarity: floatPtr(1),
				Status:     "pending",
			}
			id, isNew, err := store.UpsertCandidateNew(base)
			require.NoError(t, err)
			require.True(t, isNew)

			// Record the verdict (what a reviewer or auto-resolve does).
			verdict := base
			verdict.Status = tc.verdict
			_, _, err = store.UpsertCandidateNew(verdict)
			require.NoError(t, err)

			// A rescan re-upserts the identical pair.
			rescan := base
			rescan.Status = tc.incoming
			gotID, gotNew, err := store.UpsertCandidateNew(rescan)
			require.NoError(t, err)
			require.False(t, gotNew, "rescan must update the existing row, not insert")
			require.Equal(t, id, gotID)

			cands, _, err := store.ListCandidates(CandidateFilter{Limit: 10})
			require.NoError(t, err)
			require.Len(t, cands, 1)
			require.Equal(t, tc.want, cands[0].Status)
		})
	}
}
