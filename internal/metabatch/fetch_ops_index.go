// file: internal/metabatch/fetch_ops_index.go
// version: 1.0.0
// guid: 8c1d4f60-2a97-4e35-b8d1-6f0e3a7c95b2
// last-edited: 2026-08-22

package metabatch

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// CandidateFetchDefID is the v2 OperationDef id for metadata candidate fetches.
const CandidateFetchDefID = "metadata.candidate-fetch"

// candidateFetchOpType is the v1 operations-row type string for the same work.
//
// New runs no longer write a v1 row at all — the handler returns the id
// EnqueueOp minted and results key on that. This constant exists ONLY to keep
// historical fetches visible; see CandidateFetchOps.
const candidateFetchOpType = "metadata_candidate_fetch"

// CandidateFetchOp identifies one metadata candidate-fetch run, from either
// keyspace. Status vocabularies differ slightly between the two ("pending" is
// v1's queued state, "queued" is v2's), so callers that filter on status should
// accept both spellings.
type CandidateFetchOp struct {
	ID        string
	Status    string
	CreatedAt time.Time
	// CompletedAt is nil while the run is in flight. Carried through because
	// the Resume Review picker renders it; dropping it here would have blanked
	// the column for every run without anything failing.
	CompletedAt *time.Time
	// Legacy is true for a v1 operations row. Callers do not normally care;
	// it exists so the eventual v1 retirement can measure what is still out
	// there rather than guess.
	Legacy bool
}

// CandidateFetchOpLister is the store slice CandidateFetchOps reads: one
// listing call per keyspace.
type CandidateFetchOpLister interface {
	GetRecentOperations(limit int) ([]database.Operation, error)
	ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error)
}

// CandidateFetchOps returns every metadata candidate-fetch run visible in
// EITHER keyspace, newest first.
//
// WHY THE UNION. Four readers used to hand-roll this same scan —
// GetRecentOperations plus an `op.Type != "metadata_candidate_fetch"` filter —
// in the dedup guard, the Resume Review picker, latestMetadataResultsByBook and
// LatestMatchedBookIDs. Once new runs stopped writing a v1 row, a v2-only
// rewrite of those four would have dropped every fetch already keyed under a v1
// id. That is precisely the bug the Resume Review picker was written to fix: its
// own comment records that back-to-back fetches left the first one's results
// invisible because the id lived only in React state. Reintroducing it for the
// existing backlog is not an acceptable cutover cost, so both keyspaces are read
// until the v1 rows age out.
//
// Keeping it in ONE function is the point. When the v1 rows are finally dropped,
// this is a single delete rather than four separate edits with four chances to
// leave one behind.
//
// Errors are swallowed per-keyspace on purpose: a store that cannot answer one
// listing should still surface what the other knows. A total failure yields an
// empty slice, which every caller already treats as "nothing to show".
func CandidateFetchOps(store CandidateFetchOpLister, limit int) []CandidateFetchOp {
	if limit <= 0 {
		limit = 5000
	}
	var out []CandidateFetchOp
	seen := make(map[string]bool)

	// v2 — the keyspace new runs land in. A zero `since` means "all history";
	// ListOperationsV2Since additionally always includes non-terminal rows, so a
	// long-running fetch cannot fall out of its own listing.
	if rows, err := store.ListOperationsV2Since(time.Time{}, limit); err == nil {
		for _, row := range rows {
			if row.DefID != CandidateFetchDefID || seen[row.ID] {
				continue
			}
			seen[row.ID] = true
			out = append(out, CandidateFetchOp{
				ID:          row.ID,
				Status:      row.Status,
				CreatedAt:   row.QueuedAt,
				CompletedAt: row.CompletedAt,
			})
		}
	}

	// v1 — history only.
	if ops, err := store.GetRecentOperations(limit); err == nil {
		for _, op := range ops {
			if op.Type != candidateFetchOpType || seen[op.ID] {
				continue
			}
			seen[op.ID] = true
			out = append(out, CandidateFetchOp{
				ID:          op.ID,
				Status:      op.Status,
				CreatedAt:   op.CreatedAt,
				CompletedAt: op.CompletedAt,
				Legacy:      true,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// CandidateFetchParamsReader is the store slice CandidateFetchBookIDs reads.
type CandidateFetchParamsReader interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
	GetOperationParams(opID string) ([]byte, error)
}

// CandidateFetchBookIDs returns the books a fetch was asked to cover.
//
// The two keyspaces store this differently and neither shape is going to change
// retroactively: a v2 row carries marshalled FetchOpParams in its own Params
// column, while a v1 run had the handler write a bare []string through
// SaveOperationParams. Reading the right one is keyed off op.Legacy rather than
// guessed by trying both, so a malformed blob reports as empty instead of
// silently matching the other decoder.
//
// An empty result means "we cannot tell what this run covers". Callers use this
// for the dedup guard, where that degrades to re-fetching a book — wasteful but
// correct — rather than to skipping one.
func CandidateFetchBookIDs(store CandidateFetchParamsReader, op CandidateFetchOp) []string {
	if op.Legacy {
		raw, err := store.GetOperationParams(op.ID)
		if err != nil || len(raw) == 0 {
			return nil
		}
		var ids []string
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil
		}
		return ids
	}

	row, err := store.GetOperationV2(op.ID)
	if err != nil || row == nil || row.Params == "" {
		return nil
	}
	var p FetchOpParams
	if err := json.Unmarshal([]byte(row.Params), &p); err != nil {
		return nil
	}
	return p.BookIDs
}

// RemainingBooksToFetch returns the entries of want that have no result row in
// existing, preserving want's order.
//
// This is what makes metadata.candidate-fetch idempotent, and it is load-bearing
// for ResumePolicy=ResumeRestart: the registry re-enters Run from the top with
// the ORIGINAL book list, so without this filter every restart re-fetches every
// book — up to ~10K external API calls for a full-library run.
//
// It is a free function, and pure, so that guarantee can be tested without
// standing up a Server, a registry and a store. It previously lived inline in a
// startup-only helper, where nothing exercised it.
func RemainingBooksToFetch(existing []database.OperationResult, want []string) []string {
	if len(existing) == 0 {
		return want
	}
	fetched := make(map[string]bool, len(existing))
	for _, r := range existing {
		fetched[r.BookID] = true
	}
	remaining := make([]string, 0, len(want))
	for _, id := range want {
		if fetched[id] {
			continue
		}
		remaining = append(remaining, id)
	}
	return remaining
}

// IsActiveFetchStatus reports whether a CandidateFetchOp status means the run is
// still going to produce results.
//
// It spans both vocabularies deliberately: v1 queues at "pending", v2 at
// "queued", and the dedup guard has to treat either as work already claimed. A
// guard that knew only one spelling would let a second fetch re-request every
// book the first had queued but not yet fetched.
func IsActiveFetchStatus(status string) bool {
	switch status {
	case "pending", "queued", "running":
		return true
	}
	return false
}
