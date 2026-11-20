// file: internal/maintenance/resources/resources_test.go
// version: 1.0.8
// guid: 7b3c9e52-1d84-4a6f-9c07-e5a2f81d3b46
// last-edited: 2026-11-19

package resources

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// rowsPinned is every row of Table(): the resources a
// store or writer method can write, space separated, in any order.
var rowsPinned = map[string]string{
	"AIJournalPruner":                     "ai_journal",
	"ActivityFlushOp":                     "activity",
	"AppendFileEvent":                     "file_provenance",
	"BackfillBookAtPathIndex":             "books",
	"BackfillExternalIDs":                 "external_ids settings",
	"BackfillFilterIndexWindow":           "activity",
	"BatchCreateBookFiles":                "book_files books indexes metadata_cache",
	"CarryOverFingerprintWindows":         "fingerprint_index",
	"CarryStateBeforeHardDelete":          "books indexes metadata_cache sync_identity user_state",
	"CarryStateBetweenLiveBooks":          "books indexes metadata_cache sync_identity user_state",
	"CarryStateThenHardDelete":            "books indexes metadata_cache sync_identity user_state",
	"CleanupOrphanedTempFiles":            "activity files",
	"CleanupStaleTempFiles":               "files",
	"CleanupTrashedVersions":              "books files settings",
	"ClearBookFileIntro":                  "book_files",
	"ClearClassLists":                     "",
	"ClearFilterIndexBackfillDone":        "activity",
	"ClearIntroTimeouts":                  "book_files",
	"ClearUserPositions":                  "user_state",
	"CloneBookIntoLibrary":                "books book_files files authors user_state sync_identity operations indexes metadata_cache field_states embeddings",
	"CollapseHealDuplicates":              "books external_ids indexes metadata_cache operations sync_identity user_state",
	"CompactActivityEntries":              "activity",
	"CopyBookmarkIfAbsent":                "user_state",
	"CreateAuthor":                        "authors",
	"CreateBook":                          "books indexes",
	"CreateBookFile":                      "book_files books indexes metadata_cache",
	"CreateNarrator":                      "books",
	"CreateOperationChange":               "operations",
	"CreateSeries":                        "series",
	"DedupTriageApply":                    "embeddings",
	"DeleteAuthor":                        "authors books indexes metadata_cache",
	"DeleteBook":                          "books authors field_states embeddings metadata_cache user_state indexes",
	"DeleteBookFilesByIDs":                "book_files books fingerprint_index indexes metadata_cache",
	"DeleteMetadataCache":                 "metadata_cache indexes",
	"DeleteNarrator":                      "books",
	"DeleteOrphanBookFileAcoustIDEntries": "book_files",
	"DeleteRaw":                           "user_state",
	"DeleteRawBatch":                      "authority_lists",
	"DeleteReviewItemIfPending":           "review_items",
	"EnsureOpChangeByBookIndexWith":       "operations",
	"HardDeleteWithoutUserState":          "",
	"HealTrack":                           "files",
	"HealWhisperJournal":                  "ai_journal",
	"ImportSmartPlaylists":                "user_state",
	"InvalidateAuthorsCache":              "",
	"JournalStep":                         "operations",
	"MaintainActivityLog":                 "activity",
	"MarkFilterIndexBackfillDone":         "activity",
	"MarkOperationChangesReverted":        "operations",
	"MergeBooks":                          "books external_ids indexes metadata_cache operations sync_identity user_state",
	"MergeDeleteAuthor":                   "authors books indexes metadata_cache operations",
	"MigrateBookSigToSidecar":             "books",
	"MintOrGetSyncID":                     "sync_identity",
	"ModifyBook":                          "books metadata_cache indexes",
	"ModifyBookAuthors":                   "authors",
	"ModifyBookCredits":                   "books authors metadata_cache indexes",
	"ModifyBookFile":                      "book_files books indexes metadata_cache",
	"ModifyBookFileDeferAggregates":       "book_files",
	"ModifyBookFileReportingRecompute":    "book_files books indexes metadata_cache",
	"MoveBookFilesToBook":                 "book_files books indexes metadata_cache",
	"MoveBookFilesToBookBulk":             "book_files books indexes metadata_cache",
	"MoveCredits":                         "authors books indexes metadata_cache operations",
	"NewUserStateProbe":                   "",
	"OpenWhisperJournal":                  "ai_journal",
	"QuarantineKnownBadFiles":             "book_files books files indexes metadata_cache settings",
	"ReassignExternalID":                  "external_ids",
	"RebuildBookAtPathIndex":              "books",
	"RebuildIdentIndex":                   "indexes",
	"RebuildOpChangeByBookIndex":          "operations",
	"RecompactActivityDigests":            "activity",
	"RecomputeBookAggregates":             "books metadata_cache indexes",
	"ReconcileFilterIndexCoverage":        "activity",
	"RecordIntroTimeout":                  "book_files",
	"RecordMetadataChange":                "books",
	"RecordPathChange":                    "books",
	"RecordOperationChange":               "operations",
	"RecordSyncMerge":                     "sync_identity",
	"BatchUpsertBookFiles":                "authors book_files books indexes metadata_cache",
	"ApplyUserMerge":                      "auth_credentials auth_sessions collections operations user_state users",
	"ResumeUserMerge":                     "auth_credentials auth_sessions collections operations user_state users",
	"UndoUserMerge":                       "auth_credentials auth_sessions collections operations user_state users",
	"RebuildReviewStatusIndex":            "review_items",
	"RunBulkWriteBack":                    "activity authors book_files books file_provenance files indexes metadata_cache operations user_state",
	"Reflink":                             "files",
	"RemoveOldBackupFiles":                "files",
	"RemuxMalformedM4BFiles":              "book_files books files indexes metadata_cache settings",
	"RenameAuthorJournaled":               "authors operations",
	"RepairBookHashIndex":                 "books",
	"ReplaceOpRowsPrefix":                 "",
	"ResolveTombstoneChains":              "authors",
	"RetireAuthorBooksLegacy":             "authors",
	"RunAutoPurgeSoftDeleted":             "authors books embeddings external_ids field_states files indexes metadata_cache sync_identity user_state",
	"SaveChaptersForBook":                 "books",
	"SaveClassLists":                      "",
	"SetBookAuthors":                      "authors",
	"SetBookFileHash":                     "book_files books indexes metadata_cache",
	"SetBookFileIntro":                    "book_files",
	"SetBookNarrators":                    "books",
	"SetFileProvExportCursor":             "file_provenance",
	"SetLibraryStateOrganized":            "books metadata_cache indexes",
	"SetRaw/authority_lists":              "authority_lists",
	"SetRaw/cover_text":                   "cover_text",
	"SetRaw/user_state":                   "user_state",
	"SetUserBookState":                    "user_state",
	"SetUserPosition":                     "user_state",
	"SetUserPositionAt":                   "user_state",
	"SetUserPreferenceForUser":            "user_state",
	"StripMovementAtoms":                  "book_files books file_provenance files indexes metadata_cache settings",
	"SubmitAuthorDedupJob":                "review_items",
	"TranscodeMalformedM4BFiles":          "book_files books files indexes metadata_cache settings",
	"UpdateBook":                          "books metadata_cache indexes",
	"UpdateBookFile":                      "book_files books indexes metadata_cache",
	"UpdateBookFiles":                     "book_files books indexes metadata_cache",
	"UpsertReviewItem":                    "review_items",

	// Added with the jobs' write sets (every store write method a maintenance job
	// reaches has a row): typed by hand from the store code.
	"AddBookUserTag":                 "user_state",
	"CreateBookmark":                 "user_state",
	"CreateOperationResult":          "operations",
	"DeleteOperationState":           "operations",
	"DeleteOperationWithLogs":        "operations",
	"DeleteRaw/dead_book_index":      "books",
	"DeleteRaw/group_redirect":       "books",
	"DeleteRaw/merge_journal":        "operations",
	"DeleteRaw/user_state":           "user_state",
	"DeleteSeries":                   "series",
	"DeleteSeriesIfUnreferenced":     "series",
	"MarkBookAggregatesBackfillDone": "",
	"MintOrGetSyncFileID":            "sync_identity",
	"PruneBookSnapshots":             "books",
	"ReassignExternalIDs":            "external_ids",
	"RepointSyncFileToBook":          "sync_identity",
	"SaveOperationState":             "operations",
	"SaveOperationSummaryLog":        "operations",
	"SetRaw/group_redirect":          "books",
	"SetRaw/merge_journal":           "operations",
	"SetSetting":                     "settings",
	"SweepExpiredMetrics":            "metrics",
	"UpdateBookFileHashes":           "book_files books indexes metadata_cache",
}

// TestTable_EqualsThePinnedRows: every row of the table is exactly the pinned
// row, and the pinned list has no method the table no longer has. A kind that
// reaches the row inherits the loss, so this is the check on the rows no kind's
// Effects test reads directly (the writers F5-18 gates, the composites' parts,
// every job's trace in internal/maintenance/jobs).
func TestTable_EqualsThePinnedRows(t *testing.T) {
	table := Table()
	for method, row := range table {
		names := make([]string, 0, len(row))
		for _, r := range row {
			names = append(names, string(r))
		}
		got := pinSorted(strings.Join(names, " "))
		want, ok := rowsPinned[method]
		if !ok {
			t.Errorf("Table has a row for %s with no entry in rowsPinned: add one (typed by hand)", method)
			continue
		}
		if want = pinSorted(want); got != want {
			t.Errorf("Table[%s] is %q, pinned %q", method, got, want)
		}
	}
	for method := range rowsPinned {
		if _, ok := table[method]; !ok {
			t.Errorf("rowsPinned names %s, which the table no longer has: remove it", method)
		}
	}
}

// pinSorted is the names of s in sorted order, duplicates kept: two sides are the
// same list only if they hold the same names the same number of times.
func pinSorted(s string) string {
	names := strings.Fields(s)
	slices.Sort(names)
	return strings.Join(names, " ")
}

// TestTable_RowsAreNonNilAndFresh: an empty row is an explicit empty list, never
// nil, and every call hands out rows nobody else holds, so a caller that edits a
// row cannot change the next caller's.
func TestTable_RowsAreNonNilAndFresh(t *testing.T) {
	a, b := Table(), Table()
	for method, row := range a {
		if row == nil {
			t.Errorf("Table[%s] is nil, want an explicit empty row", method)
		}
		if len(row) > 0 {
			row[0] = ops.Resource("scribbled")
			if b[method][0] == "scribbled" {
				t.Errorf("Table[%s] shares its backing array between calls", method)
			}
		}
	}
}

// TestUnion_SortedDeduplicatedNonNil pins the contract the jobs' derivation relies
// on: a sorted, duplicate-free, non-nil result.
func TestUnion_SortedDeduplicatedNonNil(t *testing.T) {
	got := Union([]ops.Resource{ops.ResBooks, ops.ResAuthors}, nil, []ops.Resource{ops.ResBooks, ops.ResAuthors})
	if want := []ops.Resource{ops.ResAuthors, ops.ResBooks}; !slices.Equal(got, want) {
		t.Errorf("Union = %v, want %v", got, want)
	}
	if empty := Union(); empty == nil || len(empty) != 0 {
		t.Errorf("Union() = %#v, want a non-nil empty slice", empty)
	}
}

// TestTable_BareSetRawHasNoRow: SetRaw is a different key family on every
// interface, so the table has only qualified SetRaw rows and a caller that cannot
// name its family gets no row (and fails loudly) instead of a guess.
func TestTable_BareSetRawHasNoRow(t *testing.T) {
	if _, ok := Table()["SetRaw"]; ok {
		t.Error("Table has a bare SetRaw row; SetRaw means a different key family per interface")
	}
}
