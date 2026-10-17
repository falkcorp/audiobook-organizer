// file: pkg/ops/effects.go
// version: 1.1.0
// guid: 56bbb3ad-7cb3-4962-a476-26ea63bdc9ba
// last-edited: 2026-10-17

package ops

import (
	"slices"
	"strings"
)

// Resource names a logical table (or other shared state) an op reads or
// writes. The v2 names are kept so the executor maps each one 1:1 by name to
// its write-set gate.
type Resource string

// Resources. The first nine are v2's set; the rest are what v2 ops wrote
// without a name.
const (
	ResBooks            Resource = "books"
	ResBookFiles        Resource = "book_files"
	ResAuthors          Resource = "authors"
	ResSeries           Resource = "series"
	ResReviewItems      Resource = "review_items"
	ResEmbeddings       Resource = "embeddings"
	ResOperations       Resource = "operations"
	ResCatalog          Resource = "catalog"
	ResFingerprintIndex Resource = "fingerprint_index"

	ResSettings      Resource = "settings"
	ResActivity      Resource = "activity"
	ResFiles         Resource = "files" // bytes on disk
	ResExternalIDs   Resource = "external_ids"
	ResUserState     Resource = "user_state"
	ResMetadataCache Resource = "metadata_cache"
	ResFieldStates   Resource = "field_states"
	ResIndexes       Resource = "indexes"
)

// deletePrefix marks a Resource built by Deletes.
const deletePrefix = "delete:"

// Readiness is the startup signal a run waits for before it is dispatched
// (D44). The dispatcher holds a waiting run in the queue, where it takes no
// worker, no exclusive key and no timeout or watchdog clock.
type Readiness int

const (
	// ReadyNone is pure housekeeping that does not read the store.
	ReadyNone Readiness = iota
	// ReadyStore waits for the store's warm-up. The default.
	ReadyStore
	// ReadyIndexes waits for the memdb indexes and search. Today the store
	// reports one warm-up signal covering both, so it waits on the same one.
	ReadyIndexes
)

// Effects declares what an op reads, writes and deletes.
type Effects struct {
	reads, writes, deletes []Resource
	readOnly               bool
	declared               bool
	ready                  Readiness
	readySet               bool
}

// ReadOnly declares an op that writes nothing. It gets no Writer.
func ReadOnly(reads ...Resource) Effects {
	return Effects{reads: slices.Clone(reads), readOnly: true, declared: true}
}

// Writes declares the resources an op writes; reads default to the same set.
// A Resource built by Deletes also records the deletion.
func Writes(ws ...Resource) Effects {
	e := Effects{declared: true}
	for _, w := range ws {
		if del, ok := strings.CutPrefix(string(w), deletePrefix); ok {
			for _, d := range strings.Split(del, ",") {
				e.deletes = appendUnique(e.deletes, Resource(d))
				e.writes = appendUnique(e.writes, Resource(d))
			}
			continue
		}
		e.writes = appendUnique(e.writes, w)
	}
	e.reads = slices.Clone(e.writes)
	return e
}

// AlsoReads adds whole-table reads to e.
func (e Effects) AlsoReads(rs ...Resource) Effects {
	out := e.clone()
	for _, r := range rs {
		out.reads = appendUnique(out.reads, r)
	}
	return out
}

// Deletes wraps resources an op deletes rows from. It is only meaningful
// inside Writes(...); the Writer has no delete method, and a def that deletes
// is flagged by oplint for owner review.
func Deletes(rs ...Resource) Resource {
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, string(r))
	}
	return Resource(deletePrefix + strings.Join(names, ","))
}

// NeedsReady sets the readiness signal the op waits for (default ReadyStore).
func (e Effects) NeedsReady(r Readiness) Effects {
	out := e.clone()
	out.ready, out.readySet = r, true
	return out
}

// Ready is the readiness the op waits for: ReadyStore unless NeedsReady said
// otherwise.
func (e Effects) Ready() Readiness {
	if !e.readySet {
		return ReadyStore
	}
	return e.ready
}

// IsReadOnly reports whether the op declared ReadOnly.
func (e Effects) IsReadOnly() bool { return e.readOnly }

// IsWriter reports whether the op gets a Writer: it did not declare ReadOnly.
// An empty Writes() counts, because the runner hands that op a Writer too. The
// scheduler (which picks Live or Preview) and ValidateCatalog (which demands a
// mode of a scheduled writer) both use this one predicate, so a def cannot
// pass validation and then run as a Preview by accident.
func (e Effects) IsWriter() bool { return !e.readOnly }

// Declared reports whether ReadOnly or Writes was called at all.
func (e Effects) Declared() bool { return e.declared }

// ReadSet returns the declared reads.
func (e Effects) ReadSet() []Resource { return slices.Clone(e.reads) }

// WriteSet returns the declared writes (deleted resources included).
func (e Effects) WriteSet() []Resource { return slices.Clone(e.writes) }

// DeleteSet returns the resources the op deletes rows from.
func (e Effects) DeleteSet() []Resource { return slices.Clone(e.deletes) }

// union merges the effects of a pipeline's children.
func (e Effects) union(o Effects) Effects {
	out := e.clone()
	out.declared = e.declared || o.declared
	out.readOnly = e.readOnly && o.readOnly
	for _, r := range o.reads {
		out.reads = appendUnique(out.reads, r)
	}
	for _, w := range o.writes {
		out.writes = appendUnique(out.writes, w)
	}
	for _, d := range o.deletes {
		out.deletes = appendUnique(out.deletes, d)
	}
	if o.Ready() > out.Ready() {
		out.ready, out.readySet = o.Ready(), true
	}
	return out
}

func (e Effects) clone() Effects {
	e.reads, e.writes, e.deletes = slices.Clone(e.reads), slices.Clone(e.writes), slices.Clone(e.deletes)
	return e
}

func appendUnique(s []Resource, r Resource) []Resource {
	if slices.Contains(s, r) {
		return s
	}
	return append(s, r)
}
