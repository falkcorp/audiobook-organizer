// file: internal/database/activity_partial_query.go
// version: 1.0.0
// guid: efd7ba68-8c39-42ce-8254-130d1f7e22a8
// last-edited: 2026-09-19

package database

import "context"

// QueryWithPartialOf asks store for a Query answer with its partial flag,
// forwarding through a store that implements ActivityPartialQuerier and
// falling back to Query (Partial=false: that backend's answer is complete, or
// at least it has no way to say otherwise) for one that does not.
//
// Wrappers call this on the store they wrap, so the flag survives any stack of
// them; activity.Service calls it on whatever store it was handed.
func QueryWithPartialOf(ctx context.Context, store ActivityReader, f ActivityFilter) (ActivityQueryResult, error) {
	if q, ok := store.(ActivityPartialQuerier); ok {
		return q.QueryWithPartial(ctx, f)
	}
	entries, total, err := store.Query(ctx, f)
	if err != nil {
		return ActivityQueryResult{}, err
	}
	return ActivityQueryResult{Entries: entries, Total: total}, nil
}
