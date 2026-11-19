// file: pkg/ops/effects.go
// version: 1.5.0
// guid: 56bbb3ad-7cb3-4962-a476-26ea63bdc9ba
// last-edited: 2026-11-19

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
// without a name. Every name an Effects declaration uses must be one of these
// constants; do not coin a catch-all.
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

	// ResIndexes is the store's ident: rows (the identity index,
	// ident:state/by/dirty/reason/meta): the derived state the identification
	// driver reads. That is the whole definition, and it is exactly what the
	// maintenance writer table (internal/maintenance/resources/resources.go)
	// uses it for in every group (F5-07, F5-09, F5-10, F5-11): a method declares
	// `indexes` when it can stage ident: rows, whether an ident-index rebuild or
	// a book write does it. It does NOT name the search index or the in-memory
	// (memdb) read model: those follow the store's own commits and no kind
	// declares them. Row-family secondary indexes kept in a row's own batch
	// (book_atpath:, book_hash:, book_file_acoustid:, author_books_legacy:, path
	// and version-group indexes) are NOT indexes: they belong to the row family's
	// resource (books, book_files, authors).
	ResIndexes Resource = "indexes"

	// ResAIJournal is the durable AI result journal (aijournal:<kind>:<key>
	// keys, internal/ai/resultjournal): whisper and batch results kept so a
	// restart does not pay for them twice. No other constant names it: it is
	// not the undo journal (ResOperations) and not a metadata cache.
	ResAIJournal Resource = "ai_journal"
	// ResAuthorityLists is the authority lists (ref_person:, ref_pub:,
	// ref_asin:, ref_src: and ref_ovr: keys, internal/authority): reference
	// names rebuilt from the library export, plus the owner overrides. It is not
	// ResCatalog, which is the author catalog (cat:* keys).
	ResAuthorityLists Resource = "authority_lists"
	// ResSyncIdentity is the cross-device sync identity (sync ids and their
	// pointers on book rows), as F5-01's backfill-sync-ids writes it.
	ResSyncIdentity Resource = "sync_identity"
	// ResMetrics is the Pebble metrics store (time-series rows with a TTL).
	ResMetrics Resource = "metrics"
	// ResCoverText is the cover-text store (cover_text:<sha256> records and the
	// cover_text_book:<book id> index, internal/covertext) that the cover-text
	// read kind writes. No F5-06 kind uses it; F5-08 switches to it.
	ResCoverText Resource = "cover_text"
	// ResFileProvenance is the file provenance ledger (file_prov:, file_prov_hash:,
	// file_prov_orphan: and file_prov_seq: keys, internal/books/store) that the
	// provenance capture/export kinds and the quarantine path write. No F5-06
	// kind uses it; the siblings that write it switch to it.
	ResFileProvenance Resource = "file_provenance"
	// ResAuxStores is books' two auxiliary Pebble stores: the AI scan database
	// and the OpenLibrary cache, each opened apart from the main store (their
	// own compaction counters, internal/plugins/maintenance StoreOptimizer).
	// Only books.maintenance.aux-store-compact (F5-03) writes it, by compacting
	// them. It is not ResMetadataCache, which is the cached lookup rows, nor the
	// main store's own resources.
	ResAuxStores Resource = "aux_stores"
	// ResAIScans is the AI scan store (ai_scans: the phases, chunks and
	// proposals of an author scan); the AI author scan writes it and the
	// scan-apply op consumes it. F5-13 coined it for the two server kinds.
	ResAIScans Resource = "ai_scans"
	// ResDedupCandidates is the dedup candidate store the dedup engine's checks
	// file (a library scan, a folder auto-scan and a book merge write it).
	// F5-13 coined it for those kinds.
	ResDedupCandidates Resource = "dedup_candidates"
	// ResCollections is the dynamic-collection member lists that
	// collections.refresh-relative-dynamic rebuilds. HEAD's def named it as a
	// literal; F5-13 gives it a constant.
	ResCollections Resource = "collections"
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
