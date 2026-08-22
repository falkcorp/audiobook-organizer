// file: internal/operations/registry/legacy_backfill.go
// version: 1.0.0
// guid: c17d94ae-0b62-4f38-9d51-3ae680f2b7c4
// last-edited: 2026-08-22

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// legacyBackfillStore is the optional store capability the backfill needs on top
// of legacyOpStore. Discovered by type assertion for the same reason
// legacyOpStore is: the registry's store is an OpsV2Store, several test fakes
// implement exactly that and nothing more, and widening it would break them for
// a concern they do not model.
//
// ListOperations rather than GetRecentOperations DELIBERATELY. Every existing
// caller of this data uses GetRecentOperations(500), which does a full scan,
// sorts by CreatedAt descending and then keeps the newest 500 — so a row older
// than the 500 most recent is invisible to it. The rows this backfill exists to
// repair are the OLDEST ones in the store. ListOperations(0, 0) returns every
// row plus a true total, which is the only honest census here.
type legacyBackfillStore interface {
	legacyOpStore
	ListOperations(limit, offset int) ([]database.Operation, int, error)
	ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error)
}

// nonTerminalLegacyStatus reports whether a v1 row is in a state that means
// "still going" — i.e. one the backfill should resolve.
//
// It includes "pending", and that inclusion is the entire point. There are
// already TWO disagreeing predicates for this concept in the codebase:
//
//   - server_lifecycle.go's isStaleOperationStatus matches running/queued/
//     in_progress and NOT pending. It backs GET /operations/stale and the
//     restart reaper, so both are blind to pending rows — which is why the stale
//     view reported count:0 on 2026-08-22 while stranded pending rows existed.
//   - ClearStaleOperations has its own inline pending/running/queued check, so
//     the clear button acts on rows the stale view will not show you.
//
// The stranded rows are stuck at "pending" specifically, because that is what
// CreateOperation writes and nothing wrote them again. A predicate that excludes
// it would make this backfill a no-op against the exact population it targets.
func nonTerminalLegacyStatus(status string) bool {
	switch status {
	case "pending", "running", "queued", "in_progress":
		return true
	default:
		return false
	}
}

// LegacyBackfillDecision is what the backfill concluded about one v1 row.
type LegacyBackfillDecision struct {
	OpID       string    `json:"op_id"`
	Type       string    `json:"type"`
	OldStatus  string    `json:"old_status"`
	NewStatus  string    `json:"new_status"`
	Evidence   string    `json:"evidence"`
	V2OpID     string    `json:"v2_op_id,omitempty"`
	V2Status   string    `json:"v2_status,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Applied    bool      `json:"applied"`
	ApplyError string    `json:"apply_error,omitempty"`
}

// LegacyBackfillReport is the outcome of one backfill pass.
type LegacyBackfillReport struct {
	DryRun bool `json:"dry_run"`
	// TotalV1Rows is every row under the "operation:" prefix, not a page of them.
	TotalV1Rows int `json:"total_v1_rows"`
	NonTerminal int `json:"non_terminal"`
	// ResolvedFromV2 counts rows whose real outcome was recovered from the v2 op
	// that actually ran the work.
	ResolvedFromV2 int `json:"resolved_from_v2"`
	// MarkedInterrupted counts rows with no surviving evidence. "interrupted" is
	// what server_lifecycle.go already writes for an unresumable row, and it is
	// honest: we know the process died, we do not know whether the work finished.
	MarkedInterrupted int            `json:"marked_interrupted"`
	ByNewStatus       map[string]int `json:"by_new_status"`
	ByType            map[string]int `json:"by_type"`
	OldestCreatedAt   *time.Time     `json:"oldest_created_at,omitempty"`
	NewestCreatedAt   *time.Time     `json:"newest_created_at,omitempty"`
	Applied           int            `json:"applied"`
	ApplyErrors       int            `json:"apply_errors"`
	// Decisions is the full per-row plan. Kept whole rather than sampled: the
	// point of a dry run is to be reviewable, and a sample cannot show that the
	// one row you care about was classified correctly.
	Decisions []LegacyBackfillDecision `json:"decisions"`
}

// v2LegacyIndex maps legacy_op_id -> the v2 row that carried it.
func buildV2LegacyIndex(rows []database.OperationV2Row) map[string]database.OperationV2Row {
	idx := make(map[string]database.OperationV2Row, len(rows))
	for _, row := range rows {
		if row.Params == "" {
			continue
		}
		var p legacyOpParams
		if err := json.Unmarshal([]byte(row.Params), &p); err != nil {
			continue
		}
		if p.LegacyOpID == "" {
			continue
		}
		// Newest wins. A legacy id should appear at most once, but if a row was
		// ever re-enqueued the later run is the one whose outcome is current.
		if prev, ok := idx[p.LegacyOpID]; ok && prev.QueuedAt.After(row.QueuedAt) {
			continue
		}
		idx[p.LegacyOpID] = row
	}
	return idx
}

// BackfillLegacyOpStatus resolves v1 operation rows that never left a
// non-terminal status, using the v2 op that actually ran the work as evidence.
//
// It exists because the v1 row's status was write-only after creation until the
// status bridge landed (2026-08-16): every maintenance-job row of 2026-08-14 sat
// at "pending" while the jobs had in fact completed, which misled the operator
// twice in one day. Those rows are still there, still lying, and they also pin
// their opstate blobs forever because the retention sweep treats an unknown
// status as KEEP.
//
// dryRun reports the full plan and writes nothing.
func (r *Registry) BackfillLegacyOpStatus(ctx context.Context, dryRun bool) (*LegacyBackfillReport, error) {
	store, ok := r.store.(legacyBackfillStore)
	if !ok {
		return nil, fmt.Errorf("backfill: store does not support legacy operation access")
	}

	// limit 0 == "no limit"; see ListOperations' doc comment.
	v1Rows, total, err := store.ListOperations(0, 0)
	if err != nil {
		return nil, fmt.Errorf("backfill: list v1 operations: %w", err)
	}

	// A zero `since` means "everything". The limit is generous rather than
	// unbounded so a pathological store cannot balloon memory here; if it is
	// ever hit the count mismatch shows up in the report as unresolved rows
	// rather than as silently wrong verdicts.
	v2Rows, err := store.ListOperationsV2Since(time.Time{}, 100000)
	if err != nil {
		return nil, fmt.Errorf("backfill: list v2 operations: %w", err)
	}
	idx := buildV2LegacyIndex(v2Rows)

	report := &LegacyBackfillReport{
		DryRun:      dryRun,
		TotalV1Rows: total,
		ByNewStatus: map[string]int{},
		ByType:      map[string]int{},
	}

	for _, op := range v1Rows {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if !nonTerminalLegacyStatus(op.Status) {
			continue
		}
		report.NonTerminal++

		d := LegacyBackfillDecision{
			OpID:      op.ID,
			Type:      op.Type,
			OldStatus: op.Status,
			CreatedAt: op.CreatedAt,
		}

		if v2, found := idx[op.ID]; found {
			if mapped := legacyStatusFor(v2.Status); mapped != "" {
				d.NewStatus = mapped
				d.Evidence = "v2 twin terminal status"
				d.V2OpID = v2.ID
				d.V2Status = v2.Status
				report.ResolvedFromV2++
			} else {
				// The twin exists but is itself non-terminal. Nothing is running
				// it now (this row predates the restart), so it is interrupted.
				d.NewStatus = "interrupted"
				d.Evidence = "v2 twin exists but is non-terminal"
				d.V2OpID = v2.ID
				d.V2Status = v2.Status
				report.MarkedInterrupted++
			}
		} else {
			d.NewStatus = "interrupted"
			d.Evidence = "no v2 twin"
			report.MarkedInterrupted++
		}

		report.ByNewStatus[d.NewStatus]++
		report.ByType[op.Type]++
		if report.OldestCreatedAt == nil || op.CreatedAt.Before(*report.OldestCreatedAt) {
			t := op.CreatedAt
			report.OldestCreatedAt = &t
		}
		if report.NewestCreatedAt == nil || op.CreatedAt.After(*report.NewestCreatedAt) {
			t := op.CreatedAt
			report.NewestCreatedAt = &t
		}

		if !dryRun {
			msg := "backfilled from v2 twin"
			if d.Evidence == "no v2 twin" {
				msg = "interrupted; no surviving record of completion"
			}
			if err := store.UpdateOperationStatus(op.ID, d.NewStatus, 0, 0, msg); err != nil {
				d.ApplyError = err.Error()
				report.ApplyErrors++
			} else {
				d.Applied = true
				report.Applied++
			}
		}

		report.Decisions = append(report.Decisions, d)
	}

	sort.Slice(report.Decisions, func(i, j int) bool {
		return report.Decisions[i].CreatedAt.Before(report.Decisions[j].CreatedAt)
	})

	return report, nil
}
