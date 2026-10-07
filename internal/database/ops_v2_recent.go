// file: internal/database/ops_v2_recent.go
// version: 1.0.0
// guid: 0c8e5a7d-3b21-4f96-9e4a-6d2f1b8c7a53
// last-edited: 2026-10-06

package database

import "time"

// OperationsV2SinceLister is the one store method ListRecentOperationsV2
// reads through.
type OperationsV2SinceLister interface {
	ListOperationsV2Since(since time.Time, limit int) ([]OperationV2Row, error)
}

// opsV2TimelineTrustReporter is PebbleStore's OpsV2TimelineIndexTrusted,
// resolved through any decorator chain.
type opsV2TimelineTrustReporter interface {
	OpsV2TimelineIndexTrusted() bool
}

const (
	// recentOpsFirstWindow is the first window ListRecentOperationsV2 asks
	// for. Production writes about 1,780 operations a day, so an hour is
	// roughly 75 rows: enough for a five-row panel almost always.
	recentOpsFirstWindow = time.Hour
	// recentOpsMaxWindow is the widest doubling step before giving up and
	// asking for all history (a zero since).
	recentOpsMaxWindow = 366 * 24 * time.Hour
)

// ListRecentOperationsV2 returns the newest `limit` operations in the
// timeline's order (started_at DESC NULLS LAST, queued_at DESC, id DESC):
// exactly what ListOperationsV2Since(time.Time{}, limit) returns, without
// reading every operation ever written.
//
// Why it exists: GET /system/status asked ListOperationsV2Since for all of
// history to show five rows. A zero since makes the indexed read's ULID leg
// start at the bare opv2:op: prefix, so every call JSON-decoded and sorted the
// whole operation table (5.5-6 s on production, growing by ~1,780 rows a day),
// on an endpoint the Library and Dashboard call on every load.
//
// How it stays exact: it asks for a window [now-W, ...) and doubles W until
// the window holds at least `limit` rows that STARTED inside it. A row the
// window leaves out has CompletedAt and QueuedAt before the window's start,
// so its StartedAt is either nil (sorted last) or, given StartedAt <=
// CompletedAt, before the window's start too -- below every row that started
// inside the window. So once `limit` such rows are in hand, no row outside
// can reach the top `limit`. ASSUMPTION: StartedAt <= CompletedAt, which every
// writer satisfies by setting both from time.Now() on one host in that order;
// a wall-clock step backwards between the two could hide that one row until
// the window widens past its CompletedAt. If the widest window still falls
// short, it asks for all of history, which is the old answer by definition.
//
// While the store's timeline index is not trusted yet (between boot and the
// reconcile), every ListOperationsV2Since call is a full scan whatever the
// window, so doubling would only multiply the scans: it asks for all history
// once instead.
func ListRecentOperationsV2(store OperationsV2SinceLister, limit int, now time.Time) ([]OperationV2Row, error) {
	if limit <= 0 {
		limit = 5
	}
	if tr, ok := AsCapability[opsV2TimelineTrustReporter](store); !ok || !tr.OpsV2TimelineIndexTrusted() {
		return store.ListOperationsV2Since(time.Time{}, limit)
	}
	for w := recentOpsFirstWindow; w <= recentOpsMaxWindow; w *= 2 {
		since := now.Add(-w)
		// Ask for every row in the window, not just `limit`: the proof needs
		// to count the rows that started inside it, and the window is small
		// by construction. The cap only bounds a pathological burst.
		rows, err := store.ListOperationsV2Since(since, recentOpsWindowCap)
		if err != nil {
			return nil, err
		}
		if len(rows) >= recentOpsWindowCap {
			// The window is saturated; counting inside it would be counting
			// a truncated set. Fall through to the exact all-history read.
			break
		}
		startedInside := 0
		for i := range rows {
			if s := rows[i].StartedAt; s != nil && !s.Before(since) {
				startedInside++
			}
		}
		if startedInside >= limit {
			if len(rows) > limit {
				rows = rows[:limit]
			}
			return rows, nil
		}
	}
	return store.ListOperationsV2Since(time.Time{}, limit)
}

// recentOpsWindowCap bounds one windowed read. A window holding this many
// rows is not small, and the all-history read is the exact fallback.
const recentOpsWindowCap = 20_000
