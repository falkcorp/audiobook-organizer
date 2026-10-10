// file: internal/opsmetrics/defids.go
// version: 1.0.0
// guid: 5f0a2d41-7c93-4b1e-9a6d-2e84c7b0f135
// last-edited: 2026-10-10

package opsmetrics

import "sync"

// OtherDefID is the def_id label every unregistered id is folded into.
const OtherDefID = "other"

// defIDs is the closed set of operation def ids that may appear as a def_id
// label value. RegisterOp (internal/operations/registry) is the only
// production writer, so the label's cardinality is the number of registered
// operation definitions, never the number of runs (spec 11 rule C1).
var (
	defIDMu sync.RWMutex
	defIDs  = map[string]struct{}{}
)

// RegisterDefID adds id to the closed def_id set. Idempotent; the empty id is
// ignored.
func RegisterDefID(id string) {
	if id == "" {
		return
	}
	defIDMu.Lock()
	defIDs[id] = struct{}{}
	defIDMu.Unlock()
}

// labelFor returns id when it was registered and OtherDefID otherwise, so a
// caller that passes an id nobody registered (a typo, a user-supplied string,
// a run id by mistake) cannot mint a new series.
func labelFor(id string) string {
	defIDMu.RLock()
	_, ok := defIDs[id]
	defIDMu.RUnlock()
	if ok {
		return id
	}
	return OtherDefID
}
