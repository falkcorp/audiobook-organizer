// file: internal/operations/registry/external_write_hold.go
// version: 1.0.0
// guid: c4a7d2e8-1f36-4b90-8e5d-92a0b6f3c718
// last-edited: 2026-11-18

package registry

import (
	"fmt"
	"slices"
	"strings"
)

// A write that does not run through the dispatcher is invisible to the
// write-set gate (Gate 3b): the gate only reads RUNNING ops' declared Writes, so
// an HTTP handler that rewrites books and series itself (the operation revert)
// ran beside dedup.series-prune, series merge and normalize, and the dispatcher
// started them beside it. HoldWriteSet is the same gate for such a caller: it
// refuses while a running op (or another hold) declares an overlapping write,
// and while it is held the dispatcher defers every op whose Writes overlap it.
//
// A hold is not a runHandle. It is kept in its own map, never in r.running, so
// nothing that walks the running ops (Shutdown, the abandoned-run watchdog, the
// zombie sweep, the metrics gauges, Cancel) can mistake it for an op.

// externalWriteHold is one HoldWriteSet claim.
type externalWriteHold struct {
	// reason names what holds it, for the deferral log line and the refusal.
	reason string
	writes []Resource
}

// WriteSetConflict is the refusal HoldWriteSet returns when a running op, or
// another hold, declares a write that overlaps the one asked for. Nothing was
// held.
type WriteSetConflict struct {
	// BlockerOpID is the running op's id, or the other hold's holder id.
	BlockerOpID string
	// BlockerDefID is the running op's def id; empty when the blocker is
	// another hold.
	BlockerDefID string
	// Reason is what the blocking hold was taken for; empty when the blocker is
	// a running op.
	Reason string
	// Resources are the overlapping resources.
	Resources []Resource
}

func (e *WriteSetConflict) Error() string {
	names := make([]string, len(e.Resources))
	for i, r := range e.Resources {
		names[i] = string(r)
	}
	who := "operation " + e.BlockerOpID
	switch {
	case e.Reason != "":
		who = e.Reason
	case e.BlockerDefID != "":
		who += " (" + e.BlockerDefID + ")"
	}
	return fmt.Sprintf("%s is writing %s", who, strings.Join(names, ", "))
}

// WriteSetOfDef returns the write-set the def declares, resolving a former
// (renamed) id. A def that is not registered, or declares no Writes, has none:
// the dispatcher's gate treats such an op as invisible in both directions, and
// so does this.
func (r *Registry) WriteSetOfDef(defID string) []Resource {
	def, ok := r.lookupDef(defID)
	if !ok {
		return nil
	}
	return slices.Clone(def.Writes)
}

// HoldWriteSet claims writes for holderID until the returned release is called.
// It returns a *WriteSetConflict, and holds nothing, when a running op or
// another hold declares an overlapping write. The check and the claim happen
// under the registry lock the dispatcher's claim step also takes, so an op the
// dispatcher is claiming and a hold being taken cannot both succeed.
//
// While held, Gate 3b defers any queued op whose Writes overlap (it stays
// queued, as for a running op, and dispatches once the hold is released). An
// empty writes claims nothing and returns a no-op release. The release is
// idempotent. A holder id that already holds is refused as a conflict with its
// own earlier claim, never replaced.
func (r *Registry) HoldWriteSet(holderID, reason string, writes []Resource) (release func(), err error) {
	if holderID == "" {
		return nil, fmt.Errorf("write-set hold: empty holder id")
	}
	if len(writes) == 0 {
		return func() {}, nil
	}
	writes = slices.Clone(writes)
	r.mu.Lock()
	defer r.mu.Unlock()
	// The same holder asking twice is the same work already under way (two
	// reverts of one operation): refused like any other overlap, naming the
	// first claim.
	if held, dup := r.externalWrites[holderID]; dup {
		return nil, &WriteSetConflict{BlockerOpID: holderID, Reason: held.reason, Resources: slices.Clone(held.writes)}
	}
	if h, overlap := r.writeSetConflictLocked(holderID, writes); h != nil {
		c := &WriteSetConflict{BlockerOpID: h.id, Resources: overlap}
		if held, isHold := r.externalWrites[h.id]; isHold {
			c.Reason = held.reason
		} else {
			c.BlockerDefID = h.defID
		}
		return nil, c
	}
	if r.externalWrites == nil {
		r.externalWrites = make(map[string]externalWriteHold)
	}
	r.externalWrites[holderID] = externalWriteHold{reason: reason, writes: writes}
	r.logger.Info("registry: write-set held outside the dispatcher",
		"holder", holderID, "reason", reason, "resources", resourceNames(writes))
	r.pingDispatch()
	return r.releaseWriteSetOnce(holderID), nil
}

func (r *Registry) releaseWriteSetOnce(holderID string) func() {
	var done bool
	return func() {
		r.mu.Lock()
		if done {
			r.mu.Unlock()
			return
		}
		done = true
		delete(r.externalWrites, holderID)
		r.mu.Unlock()
		// An op the hold kept queued is dispatchable now.
		r.pingDispatch()
	}
}

func resourceNames(rs []Resource) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
