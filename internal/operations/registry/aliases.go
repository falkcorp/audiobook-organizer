// file: internal/operations/registry/aliases.go
// version: 1.0.0
// guid: 3e8b1f47-6c2a-4d95-a0e3-7b5f9c2d8e14
// last-edited: 2026-09-25

package registry

import (
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// Op-ID aliases: how a renamed op keeps answering to its old ID.
//
// A def declares the IDs it used to have in OperationDef.FormerIDs. RegisterOp
// records each one here as alias -> canonical ID. From then on an old ID is
// accepted everywhere an ID can ENTER the registry, and is turned into the
// canonical ID at that boundary so nothing past it ever sees the alias:
//
//   - EnqueueOp (POST /operations/v2 {def_id}, the scheduler, every in-process
//     caller) resolves before taking the admission lock, before batching and
//     before the ConcurrencyKey dedupe, so an old-ID and a new-ID enqueue of the
//     same op contend on one lock and dedupe against each other. The row it
//     writes carries the canonical ID.
//   - Stored rows (dispatcher, startup resume, quiesced resume, retry): an
//     operations_v2 row written before the rename keeps its old def_id forever
//     -- rows are history and are never rewritten -- so every read of
//     row.DefID that looks up a def, or compares it with another def ID,
//     resolves it first. The queued run and run handle built from such a row
//     carry the CANONICAL ID, so the watchdog, the scan stand-down check,
//     DependsOn and the per-type metrics never see the alias.
//   - The isolated-op subprocess handshake, and Def() for API callers.
//
// Whether a string names an alias is decided only by this table, so label
// values for the deprecation metric can never come from a raw request string.
//
// Why a metric and not a log line. Every use of an alias increments
// audiobook_organizer_operation_deprecated_def_id_total{alias,entry}. The
// question an alias raises is "can it be deleted yet?", and that needs the
// answer "nothing has used it in N days". A log-once-per-process line cannot
// give it: it resets at every restart, says nothing about frequency, and ages
// out of journald. Logging every use is worse: the dispatcher re-reads a
// queued old-ID row on every cycle, which would flood the log. The counter is
// cheap, bounded (the alias table is a fixed, code-defined set) and survives in
// Prometheus history. The entry label (enqueue | stored_row | retry |
// subprocess | timeline_filter) recovers the one thing a log line would have
// added -- which door the old ID came through. For stored_row the count is
// lookups, not rows: a queued row waiting behind a gate is re-counted each
// dispatch cycle. Zero versus non-zero is the signal, not the magnitude.

// Entry names for the deprecation metric's "entry" label. A fixed set; never
// pass a caller-supplied string.
const (
	aliasEntryEnqueue    = "enqueue"
	aliasEntryStoredRow  = "stored_row"
	aliasEntryRetry      = "retry"
	aliasEntrySubprocess = "subprocess"

	// AliasEntryTimelineFilter is used by the HTTP timeline handler, which
	// resolves a ?def_id= filter outside this package.
	AliasEntryTimelineFilter = "timeline_filter"
)

// aliasTable maps a former def ID to its canonical def ID. A published table is
// never mutated: RegisterOp builds a copy and swaps the pointer, so readers need
// no lock. That matters because several resolution sites already hold r.mu, and
// taking r.mu.RLock again there would deadlock behind a waiting writer.
type aliasTable map[string]string

// aliasMap returns the current alias table (nil when no def declared any).
func (r *Registry) aliasMap() aliasTable {
	if p := r.aliases.Load(); p != nil {
		return *p
	}
	return nil
}

// canonicalDefID returns the canonical def ID for id: the target when id is a
// registered alias, otherwise id unchanged. It does not require the canonical
// def to be registered and takes no lock. Use it for COMPARISONS between two
// def IDs (for example a stored row's def_id against the def being enqueued);
// it does not count toward the deprecation metric, because a comparison is not
// a use of the old ID.
func (r *Registry) canonicalDefID(id string) string {
	if canon, ok := r.aliasMap()[id]; ok {
		return canon
	}
	return id
}

// CanonicalDefID is the exported form of canonicalDefID for callers outside the
// registry that compare stored def IDs (e.g. the scheduler's "is this task's op
// already active" check). It does not count toward the deprecation metric.
func (r *Registry) CanonicalDefID(id string) string {
	return r.canonicalDefID(id)
}

// Aliases returns a copy of the alias table (former ID -> canonical ID). For
// tests and diagnostics; the registry itself resolves through canonicalDefID.
func (r *Registry) Aliases() map[string]string {
	src := r.aliasMap()
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// lookupDefLocked resolves id (canonical or alias) to its registered def. The
// caller must hold r.mu (read or write). It does not count; callers that sit
// on an entry boundary call noteAliasUse with the id they were given.
func (r *Registry) lookupDefLocked(id string) (OperationDef, bool) {
	if d, ok := r.defs[id]; ok {
		return d, true
	}
	if canon, ok := r.aliasMap()[id]; ok {
		d, ok := r.defs[canon]
		return d, ok
	}
	return OperationDef{}, false
}

// lookupDef is lookupDefLocked for callers that do not hold r.mu.
func (r *Registry) lookupDef(id string) (OperationDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lookupDefLocked(id)
}

// noteAliasUse counts one arrival of a former ID at an entry boundary. given is
// the ID as it arrived; it is counted only when it is a key of the alias table,
// so a label value can never be an arbitrary string.
func (r *Registry) noteAliasUse(given, entry string) {
	if _, ok := r.aliasMap()[given]; ok {
		metrics.IncOperationDeprecatedDefID(given, entry)
	}
}

// NoteDeprecatedDefIDUse lets a caller outside the registry (the HTTP timeline
// filter) record that it resolved a former ID. entry must be one of the
// exported AliasEntry* constants.
func (r *Registry) NoteDeprecatedDefIDUse(given, entry string) {
	r.noteAliasUse(given, entry)
}

// validateFormerIDs holds the stateless FormerIDs rules; ValidateOpDef calls it.
// The stateful rule -- no alias may equal any registered ID or any other alias,
// in either direction -- is checked in RegisterOp under the write lock.
func validateFormerIDs(def OperationDef) error {
	if len(def.FormerIDs) == 0 {
		return nil
	}
	// Batch buckets are journaled under the def ID (AddToBatchBucket) and
	// reloaded at startup by the CANONICAL ID only (batchReloadOnStart). A
	// renamed Batchable def would silently strand every subject journaled under
	// its old ID. Supporting it means migrating the bucket on reload; until a
	// batchable op actually needs renaming, refuse rather than half-support it.
	if def.Batchable {
		return fmt.Errorf("registry: OperationDef.FormerIDs is not supported on a Batchable def (id=%s): "+
			"batch buckets are keyed by def ID and would be stranded under the old ID", def.ID)
	}
	seen := make(map[string]struct{}, len(def.FormerIDs))
	for _, old := range def.FormerIDs {
		switch {
		case old == "":
			return fmt.Errorf("registry: OperationDef.FormerIDs contains an empty ID (id=%s)", def.ID)
		case strings.Contains(old, ":"):
			return fmt.Errorf("registry: OperationDef.FormerIDs entry %q must not contain ':' (id=%s)", old, def.ID)
		case old == def.ID:
			return fmt.Errorf("registry: OperationDef.FormerIDs lists the def's own ID (id=%s)", def.ID)
		}
		if _, dup := seen[old]; dup {
			return fmt.Errorf("registry: OperationDef.FormerIDs lists %q twice (id=%s)", old, def.ID)
		}
		seen[old] = struct{}{}
	}
	return nil
}

// checkAliasCollisionsLocked enforces that the ID namespace stays unambiguous:
// the new def's ID must not already be someone's alias, and none of its former
// IDs may be a registered ID or another def's alias. Registration order across
// plugins is arbitrary, so both directions are checked. Caller holds r.mu for
// writing.
func (r *Registry) checkAliasCollisionsLocked(def OperationDef) error {
	current := r.aliasMap()
	if canon, ok := current[def.ID]; ok {
		return fmt.Errorf("registry: OperationDef id %q is already registered as a former ID of %q", def.ID, canon)
	}
	for _, old := range def.FormerIDs {
		if _, ok := r.defs[old]; ok {
			return fmt.Errorf("registry: former ID %q of %q would shadow the registered op %q", old, def.ID, old)
		}
		if canon, ok := current[old]; ok {
			return fmt.Errorf("registry: former ID %q of %q is already a former ID of %q", old, def.ID, canon)
		}
	}
	return nil
}

// publishAliasesLocked adds def's former IDs to the alias table by copy-and-
// swap. Caller holds r.mu for writing and has run checkAliasCollisionsLocked.
func (r *Registry) publishAliasesLocked(def OperationDef) {
	if len(def.FormerIDs) == 0 {
		return
	}
	current := r.aliasMap()
	next := make(aliasTable, len(current)+len(def.FormerIDs))
	for k, v := range current {
		next[k] = v
	}
	for _, old := range def.FormerIDs {
		next[old] = def.ID
	}
	r.aliases.Store(&next)
}
