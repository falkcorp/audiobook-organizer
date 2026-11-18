// file: internal/maintenance/jobs/kinds_writes.go
// version: 2.0.1
// guid: 6d1a4e9c-2b7f-4c35-9e80-a3f5c7d1b284
// last-edited: 2026-11-18

package jobs

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// jobTrace is what one job's Run writes, as store write methods plus one flag, so
// that no resource of jobWrites is typed anywhere but the shared table:
//
//   - Methods are the store write methods the job reaches (its body and every
//     function it calls, closures and in-module helpers included), each a key of
//     the shared method-to-resource table (resources.Table). The job's set takes
//     the whole row of each, never a subset of it (THE RULE in that package): a
//     ModifyBook caller declares books, authors, metadata_cache and indexes
//     whatever fields its closure sets, and a file-row method (UpdateBookFile,
//     BatchCreateBookFiles, UpdateBookFileHashes, ...) is book_files plus that
//     same book row. A method whose name is a different key family on a different
//     interface (SetRaw, DeleteRaw) is listed by its qualified key
//     ("SetRaw/merge_journal"); writes_trace_test.go maps the receiver the code
//     calls it on to the key.
//   - Files is a job that changes files on disk (os and fileops calls): `files` is
//     not a store method, so it has no row.
//
// The names are checked against the code: TestJobWrites_TraceMatchesCode derives
// the store methods each job reaches from its SSA and compares them with Methods.
type jobTrace struct {
	Methods []string
	Files   bool
}

// jobTraces is every job's trace, keyed by job ID. A job missing here is a job
// without declared Effects, which registration refuses.
//
// The interface a job calls a method on does not matter (maintenance.JobStore,
// dedup.Store, versionprimary.EnsureStore, a private one-method interface): the
// row is the method's. Every store write method a job reaches has a row, so a
// method with none is a bug in the trace and an error from jobWritesFor.
var jobTraces = map[string]jobTrace{"backfill-book-files": {Methods: []string{"BatchCreateBookFiles", "SaveOperationSummaryLog"}},
	"backfill-file-hashes": {
		Methods: []string{
			"DeleteOperationState", "ModifyBookFile", "ModifyBookFileDeferAggregates",
			"RecomputeBookAggregates", "SaveOperationState", "SaveOperationSummaryLog",
		},
		Files: true,
	},
	"backfill-itunes-positions":     {Methods: []string{"CreateBookmark", "SetUserBookState", "SetUserPosition"}},
	"backfill-merge-undo-pointers":  {Methods: []string{"CreateOperationResult", "RecordOperationChange"}},
	"backfill-metadata-source-hash": {Methods: []string{"ModifyBook"}},
	"backfill-sync-ids":             {Methods: []string{"MintOrGetSyncFileID", "MintOrGetSyncID"}},
	"bulk-deluge-import": {
		Methods: []string{"CreateOperationResult", "ModifyBookFile", "UpdateBookFile"},
		Files:   true,
	},
	"cleanup-empty-folders":    {Files: true},
	"cleanup-organize-mess":    {Files: true},
	"cleanup-series":           {Methods: []string{"DeleteSeriesIfUnreferenced", "ModifyBook"}},
	"dedup-books":              {Methods: []string{"AddBookUserTag", "ModifyBook", "MoveBookFilesToBook", "ReassignExternalIDs"}},
	"enrich-book-files":        {Methods: []string{"UpdateBookFile"}},
	"fix-author-narrator-swap": {Methods: []string{"ModifyBook"}},
	"fix-file-modes":           {Files: true},
	"fix-read-by-narrator":     {Methods: []string{"ModifyBook"}},
	"fix-version-groups":       {Methods: []string{"CreateBookFile", "ModifyBook", "UpdateBookFile"}},
	"merge-chapter-groups": {
		// DeleteRaw/user_state and SetRaw/user_state are the pending user-state
		// repair (merge_user_state_pending:), SetRaw/merge_journal the combine
		// journal (merge:combine-journal:); which family each receiver writes is
		// traceAliases in writes_trace_test.go.
		Methods: []string{
			"ClearUserPositions", "CopyBookmarkIfAbsent", "CreateOperationResult", "DeleteRaw/user_state",
			"MintOrGetSyncID", "ModifyBook", "MoveBookFilesToBook", "ReassignExternalIDs", "RecordOperationChange",
			"RecordSyncMerge", "RepointSyncFileToBook", "SetBookAuthors", "SetRaw/merge_journal",
			"SetRaw/user_state", "SetUserBookState", "SetUserPosition", "SetUserPositionAt", "UpdateBookFile",
		},
	},
	"normalize-primary-flags":         {Methods: []string{"ModifyBook"}},
	"prune-book-snapshots":            {Methods: []string{"PruneBookSnapshots"}},
	"purge-unknown-author-duplicates": {Methods: []string{"ModifyBook"}},
	"recompute-book-aggregates": {Methods: []string{
		"DeleteOperationState", "MarkBookAggregatesBackfillDone", "RecomputeBookAggregates",
		"SaveOperationState", "SaveOperationSummaryLog",
	}},
	"recompute-itunes-paths":   {Methods: []string{"UpdateBookFile"}},
	"refetch-missing-authors":  {Methods: []string{"CreateAuthor", "ModifyBook", "ModifyBookAuthors"}},
	"relink-missing-to-itunes": {Methods: []string{"ModifyBook", "UpdateBookFile"}},
	"relink-report":            {},
	"repoint-version-primary":  {Methods: []string{"ModifyBook"}},
	// DeleteRaw/dead_book_index: the sweep deletes the dead book:series: and
	// book:author: prefixes, children of the book: key family (books).
	"retention-and-hygiene": {Methods: []string{
		"DeleteOperationState", "DeleteOperationWithLogs", "DeleteRaw/dead_book_index", "SetSetting",
	}},
	"revert-metadata-fetch": {Methods: []string{"ModifyBook"}},
	"scan-chapter-groups":   {},
	"scan-composer-tags": {
		// UpdateBookFile is what tagger.LibraryImporter.ImportPath reaches: its one
		// implementation (deluge.LibraryImporterAdapter) repoints the protected
		// file's book_file row to the library copy through Store.UpdateBookFile.
		Methods: []string{"AppendFileEvent", "CreateOperationResult", "UpdateBookFile", "UpdateBookFileHashes"},
		Files:   true,
	},
	"scan-duplicate-files":     {},
	"scan-duration-mismatch":   {},
	"scan-metadata-hash-dups":  {},
	"sweep-pebble-metrics-ttl": {Methods: []string{"SweepExpiredMetrics"}},
}

// jobWritesDerivations counts how many times the derivation has run, so a test can
// prove importing the package does none.
var jobWritesDerivations atomic.Int32

// deriveWrites is traces crossed with table: per job, the sorted union of the
// table row of every Methods entry, plus files for a job that changes files on
// disk. A Methods entry with no row in the table is a bug in the trace; it is an
// error, not a panic, so a missing row refuses the kinds at registration instead
// of crashing every boot of a binary that links this package.
func deriveWrites(traces map[string]jobTrace, table map[string][]ops.Resource) (map[string][]ops.Resource, error) {
	out := make(map[string][]ops.Resource, len(traces))
	for id, tr := range traces {
		rows := make([][]ops.Resource, 0, len(tr.Methods)+1)
		for _, m := range tr.Methods {
			row, ok := table[m]
			if !ok {
				return nil, fmt.Errorf("jobs: job %s lists store method %s, which has no row in the shared resource table (internal/maintenance/resources)", id, m)
			}
			rows = append(rows, row)
		}
		if tr.Files {
			rows = append(rows, []ops.Resource{ops.ResFiles})
		}
		out[id] = resources.Union(rows...)
	}
	return out, nil
}

// deriveJobWrites is deriveWrites over the real traces and the shared table.
func deriveJobWrites() (map[string][]ops.Resource, error) {
	jobWritesDerivations.Add(1)
	return deriveWrites(jobTraces, resources.Table())
}

// jobWritesFor is every job's declared write set, derived on first use and not at
// package init: nothing reads it until the kinds are built (Kinds, which F5-18
// registers), so a binary that merely links this package does no work and cannot
// fail here. It is a variable so a test can make the derivation fail.
var jobWritesFor = sync.OnceValues(deriveJobWrites)
