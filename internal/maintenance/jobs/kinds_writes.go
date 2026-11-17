// file: internal/maintenance/jobs/kinds_writes.go
// version: 1.0.0
// guid: 6d1a4e9c-2b7f-4c35-9e80-a3f5c7d1b284
// last-edited: 2026-11-16

package jobs

import (
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// jobTrace is what one job's Run writes, in two parts, so that no resource of
// jobWrites is typed twice:
//
//   - Methods are the store write methods the job reaches (its body and every
//     function it calls, closures and in-module helpers included) that have a row
//     in the shared method-to-resource table (resources.Table). The job's set
//     takes the whole row, never a subset of it (THE RULE in that package): a
//     ModifyBook caller declares books, authors, metadata_cache and indexes
//     whatever fields its closure sets, and a file-row method (UpdateBookFile,
//     BatchCreateBookFiles, ...) is book_files plus that same book row.
//   - Outside are the writes no row of the table covers: files the job changes
//     on disk, and the store methods the table does not list. Each carries the
//     method it comes from (Via) and the resource the job declared for it before
//     the table was shared. That resource is kept, not re-derived: a row for the
//     method has to be traced in the store and added to the table first, and
//     TestJobWrites_OutsideTableMethodsHaveNoRow fails the day one is added, so
//     the method moves to Methods and the table decides.
//
// The names are checked against the code: TestJobWrites_TraceMatchesCode derives
// the store methods each job reaches from its SSA and compares them with
// Methods and the store-method Via names of Outside.
type jobTrace struct {
	Methods []string
	Outside []outsideWrite
}

// outsideWrite is one write that has no row in the shared table.
type outsideWrite struct {
	// Via is the store method it comes from, or viaDisk for the files the job
	// changes with os and fileops calls.
	Via string
	// Res is the resource the job declares for it.
	Res ops.Resource
}

// viaDisk is the Via of a write to files on disk (os.Remove, os.Chmod,
// os.MkdirAll, fileops tag writes): no store method, so no table row.
const viaDisk = "os"

// jobTraces is every job's trace, keyed by job ID. A job missing here is a job
// without declared Effects, which registration refuses.
//
// Methods lists the store methods by the table's own key. The interface a job
// calls them on does not matter (maintenance.JobStore, dedup.Store,
// versionprimary.EnsureStore, a private one-method interface): the row is the
// method's. Where the same name is a different key family on a different
// interface the table splits it (SetRaw/user_state), and a job that reaches the
// unsplit name lists it under Outside instead: retention-and-hygiene's
// DeleteRaw deletes the dead book:series: and book:author: prefixes, not the
// user_state keys the table's DeleteRaw row means.
//
// Outside notes, per store method the table does not list: SaveOperationState,
// DeleteOperationState and SaveOperationSummaryLog are the operation rows and
// the checkpoint blob (operations); CreateOperationResult is the per-op result
// row (operations); the two sync-file methods are the sync identity;
// AddBookUserTag and CreateBookmark are listening state; ReassignExternalIDs
// (plural, old book to new book) is not ReassignExternalID (one source key).
var jobTraces = map[string]jobTrace{
	"backfill-book-files": {
		Methods: []string{"BatchCreateBookFiles"},
		Outside: []outsideWrite{{"SaveOperationSummaryLog", ops.ResOperations}},
	},
	"backfill-file-hashes": {
		Methods: []string{"ModifyBookFile", "ModifyBookFileDeferAggregates", "RecomputeBookAggregates"},
		Outside: []outsideWrite{
			{"DeleteOperationState", ops.ResOperations},
			{"SaveOperationState", ops.ResOperations},
			{"SaveOperationSummaryLog", ops.ResOperations},
			{viaDisk, ops.ResFiles},
		},
	},
	"backfill-itunes-positions": {
		Methods: []string{"SetUserBookState", "SetUserPosition"},
		Outside: []outsideWrite{{"CreateBookmark", ops.ResUserState}},
	},
	"backfill-merge-undo-pointers": {
		Methods: []string{"RecordOperationChange"},
		Outside: []outsideWrite{{"CreateOperationResult", ops.ResOperations}},
	},
	"backfill-metadata-source-hash": {
		Methods: []string{"ModifyBook"},
	},
	"backfill-sync-ids": {
		Methods: []string{"MintOrGetSyncID"},
		Outside: []outsideWrite{{"MintOrGetSyncFileID", ops.ResSyncIdentity}},
	},
	"bulk-deluge-import": {
		Methods: []string{"ModifyBookFile", "UpdateBookFile"},
		Outside: []outsideWrite{
			{"CreateOperationResult", ops.ResOperations},
			{viaDisk, ops.ResFiles},
		},
	},
	"cleanup-empty-folders": {
		Outside: []outsideWrite{{viaDisk, ops.ResFiles}},
	},
	"cleanup-organize-mess": {
		Outside: []outsideWrite{{viaDisk, ops.ResFiles}},
	},
	"cleanup-series": {
		Methods: []string{"ModifyBook"},
		Outside: []outsideWrite{{"DeleteSeries", ops.ResSeries}},
	},
	"dedup-books": {
		Methods: []string{"ModifyBook", "MoveBookFilesToBook"},
		Outside: []outsideWrite{
			{"AddBookUserTag", ops.ResUserState},
			{"ReassignExternalIDs", ops.ResExternalIDs},
		},
	},
	"enrich-book-files": {
		Methods: []string{"UpdateBookFile"},
	},
	"fix-author-narrator-swap": {
		Methods: []string{"ModifyBook"},
	},
	"fix-file-modes": {
		Outside: []outsideWrite{{viaDisk, ops.ResFiles}},
	},
	"fix-read-by-narrator": {
		Methods: []string{"ModifyBook"},
	},
	"fix-version-groups": {
		Methods: []string{"CreateBookFile", "ModifyBook", "UpdateBookFile"},
	},
	"merge-chapter-groups": {
		Methods: []string{
			"ClearUserPositions", "CopyBookmarkIfAbsent", "DeleteRaw", "MintOrGetSyncID", "ModifyBook",
			"MoveBookFilesToBook", "RecordOperationChange", "RecordSyncMerge", "SetBookAuthors",
			"SetUserBookState", "SetUserPosition", "SetUserPositionAt", "UpdateBookFile",
		},
		Outside: []outsideWrite{
			{"CreateOperationResult", ops.ResOperations},
			{"ReassignExternalIDs", ops.ResExternalIDs},
			{"RepointSyncFileToBook", ops.ResSyncIdentity},
			// The pending-repair and combine-journal keys: a raw-key family the
			// table's SetRaw rows (authority_lists, cover_text, user_state) do
			// not name.
			{"SetRaw", ops.ResOperations},
		},
	},
	"normalize-primary-flags": {
		Methods: []string{"ModifyBook"},
	},
	"prune-book-snapshots": {
		Outside: []outsideWrite{{"PruneBookSnapshots", ops.ResBooks}},
	},
	"purge-unknown-author-duplicates": {
		Methods: []string{"ModifyBook"},
	},
	"recompute-book-aggregates": {
		Methods: []string{"RecomputeBookAggregates"},
		Outside: []outsideWrite{
			{"DeleteOperationState", ops.ResOperations},
			{"MarkBookAggregatesBackfillDone", ops.ResOperations},
			{"SaveOperationState", ops.ResOperations},
			{"SaveOperationSummaryLog", ops.ResOperations},
		},
	},
	"recompute-itunes-paths": {
		Methods: []string{"UpdateBookFile"},
	},
	"refetch-missing-authors": {
		Methods: []string{"CreateAuthor", "ModifyBook", "ModifyBookAuthors"},
	},
	"relink-missing-to-itunes": {
		Methods: []string{"ModifyBook", "UpdateBookFile"},
	},
	"relink-report": {},
	"repoint-version-primary": {
		Methods: []string{"ModifyBook"},
	},
	"retention-and-hygiene": {
		Outside: []outsideWrite{
			{"DeleteOperationState", ops.ResOperations},
			{"DeleteOperationWithLogs", ops.ResOperations},
			{"DeleteRaw", ops.ResOperations},
			{"SetSetting", ops.ResSettings},
		},
	},
	"revert-metadata-fetch": {
		Methods: []string{"ModifyBook"},
	},
	"scan-chapter-groups": {},
	"scan-composer-tags": {
		// UpdateBookFile is what tagger.LibraryImporter.ImportPath reaches: its one
		// implementation (deluge.LibraryImporterAdapter) repoints the protected
		// file's book_file row to the library copy through Store.UpdateBookFile.
		Methods: []string{"AppendFileEvent", "UpdateBookFile"},
		Outside: []outsideWrite{
			{"CreateOperationResult", ops.ResOperations},
			{"UpdateBookFileHashes", ops.ResBookFiles},
			{viaDisk, ops.ResFiles},
		},
	},
	"scan-duplicate-files":     {},
	"scan-duration-mismatch":   {},
	"scan-metadata-hash-dups":  {},
	"sweep-pebble-metrics-ttl": {Outside: []outsideWrite{{"SweepExpiredMetrics", ops.ResMetrics}}},
}

// deriveJobWrites is jobTraces crossed with the shared table: per job, the sorted
// union of the table row of every Methods entry and every Outside resource. A
// Methods entry with no row in the table is a bug in jobTraces, so it panics at
// package init rather than quietly declaring less than the job writes.
func deriveJobWrites() map[string][]ops.Resource {
	table := resources.Table()
	out := make(map[string][]ops.Resource, len(jobTraces))
	for id, tr := range jobTraces {
		rows := make([][]ops.Resource, 0, len(tr.Methods)+1)
		for _, m := range tr.Methods {
			row, ok := table[m]
			if !ok {
				panic("jobs: job " + id + " lists store method " + m + ", which has no row in the shared resource table; list it under Outside")
			}
			rows = append(rows, row)
		}
		var extra []ops.Resource
		for _, o := range tr.Outside {
			extra = append(extra, o.Res)
		}
		rows = append(rows, extra)
		out[id] = slices.Clone(resources.Union(rows...))
	}
	return out
}
