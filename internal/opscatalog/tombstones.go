// file: internal/opscatalog/tombstones.go
// version: 1.0.1
// guid: edcda6da-4fec-40a7-9b72-643a33849960
// last-edited: 2026-10-16

package opscatalog

// Tombstones are op-ID ledger IDs (internal/server/testdata/op_ids.golden)
// that deliberately resolve to NO op, each with why no FormerIDs alias points
// it somewhere. The ledger line stays (it is append-only); an entry here is
// the explicit decision the ledger check asks for. The startup check
// (ValidateCatalog) and internal/server TestOpIDs_NoRenameWithoutAlias both
// read this map; census PRs that retire an op append to it.
//
// A timeline filter on a retired ID still finds its old rows: an ID the
// registry does not know is matched verbatim against each row's def_id
// (canonicalDefID), which is what an alias would break by widening the
// filter to every row of the op it points at.
//
// Moved here from internal/server/op_id_aliases_test.go (05-PR7) so the
// production startup check can read it.
var Tombstones = map[string]string{
	// Retired 2026-10-15 (04-P12, D26). The op never ran: nothing enqueued it
	// and its declared */5 Schedule was never driven (OperationDef.Schedule is
	// read by nothing). The scheduler's inline batch_poller task calls the
	// poller service directly and is unchanged. Not aliased: there is no successor op; the inline task is the poller.
	"maintenance.batch-poller": "unreached duplicate of the inline batch_poller task; no successor op",
	// Absorbed 2026-09-28 into the Repairs-lane fixer of the SAME id
	// (internal/plugins/maintenance/junk_title_fixer.go), planned and applied
	// through repairs.plan / repairs.apply with fixer_id set to it. Aliasing
	// the old op to repairs.plan would send an old-style enqueue ({dryRun})
	// to an op that needs fixer_id, and the alias guard below would then
	// forbid the fixer from naming its own id.
	"maintenance.repair-junk-titles": "absorbed into the junk-title Repairs fixer",
	// Removed 2026-10-07 with iTunes write-back (iTunes is import-only). Both
	// rewrote locations in the iTunes library; nothing replaces them.
	"itunes.path-reconcile": "removed with iTunes write-back; no successor",
	"itunes.path-repair":    "removed with iTunes write-back; no successor",
	// It generated synthetic .itl files for testing the removed writers.
	"maintenance.generate-itl-tests": "removed with iTunes write-back; no successor",
	// Removed 2026-10-08: iTunes import is manual only. Re-running
	// itunes.import links each album to its existing book, which is what the
	// incremental sync did. Not aliased to itunes.import: an old sync enqueue
	// ({library_path, path_mappings}) is not an import request.
	"itunes.sync": "removed; itunes.import (manual, re-runnable) replaces it",
	// Stubs since 2026-10-02 that only returned a "retired" error; removed
	// 2026-10-09. Not aliased to metafetch.asin-backfill: it takes different
	// params, so an old enqueue is not an asin-backfill request.
	"scheduler.isbn-enrichment":   "stub since 2026-10-02; metafetch.asin-backfill replaces it, params differ",
	"maintenance.isbn-enrichment": "stub since 2026-10-02; metafetch.asin-backfill replaces it, params differ",
	// Retired 2026-10-13 (04-P3b). The manual job deleted every *.backup / *.bak
	// file under the library root with no age test (dry_run default true); the
	// surviving maintenance.cleanup-old-backups deletes only the .bak-<unix>[-N]
	// names fileops writes, and only past the retention window. Not aliased: an
	// old enqueue would run a different deleter with different params. The
	// suffix cleanup is dropped; whether it returns as an opt-in, age-gated,
	// preview-default param is an open owner question (todo.d).
	"maintenance.cleanup-backups": "retired; maintenance.cleanup-old-backups is a different, age-gated deleter, so no alias",
	// Retired 2026-10-14 (04-P14d). The manual job only ever set Missing=true
	// (dry_run default true); maintenance.mark-missing-files reconciles the flag
	// both ways, takes {apply} instead of {dry_run}, and writes through a scan
	// stand-down. Not aliased: an old enqueue would run a different contract
	// (it also CLEARS flags) under params it never sent, so a loud unknown-op
	// error is safer than a silent reinterpretation.
	"maintenance.fix-book-file-paths": "retired; maintenance.mark-missing-files takes {apply} and also clears stale flags, so no alias",
	// Retired 2026-10-14 (04-P14e). The manual job tried five escalating
	// strategies to relocate a missing file. Two newer ops replace it, each
	// covering a different slice (maintenance.missing-file-repoint derives the
	// flattened/padded name; maintenance.recover-missing-files matches by size),
	// so no single FormerIDs alias target is right. An old enqueue ({dry_run})
	// would also run a different contract ({apply}), so it fails loudly instead.
	"maintenance.repair-missing-files": "retired; maintenance.missing-file-repoint and maintenance.recover-missing-files each cover part of it, so no single alias",
	// Retired 2026-10-15 (04-P14f). The manual job fetched and cached candidates
	// for the whole library. Two newer ops cover its halves: library.bulk-metadata-fetch
	// (selection-driven fetch, takes prefer_audible and skip_cached) and
	// metadata.candidate-fetch (candidate cache, plus its unfetched selection).
	// library.bulk-metadata-fetch with no selection is the whole-library fetch, but
	// the job's params ({prefer_audible, skip_cached}, dry_run) do not map onto
	// either op 1:1 (skip_cached defaults differently), so no single FormerIDs
	// alias target is right.
	"maintenance.bulk-fetch-metadata": "retired; library.bulk-metadata-fetch and metadata.candidate-fetch each cover part of it, so no single alias",
	// Never existed in a production build. It was a blocking probe job that
	// only internal/server/maintenance_orphan_row_test.go registered, from a
	// test init(), so the ledger picked it up from a test boot. The startup
	// check runs in the real binary, where nothing registers it, and refused to
	// boot on it. The probe now uses a maintenance.test-probe-* ID, which
	// TestOpIDs_NoRenameWithoutAlias already treats as test-only.
	"maintenance.orphan-row-blocking-probe": "test-only probe that no production build registers; now maintenance.test-probe-orphan-row-blocking",
}
