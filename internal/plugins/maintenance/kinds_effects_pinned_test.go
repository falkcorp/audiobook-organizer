// file: internal/plugins/maintenance/kinds_effects_pinned_test.go
// version: 1.0.1
// guid: bbc0869b-3caf-4c34-96f7-b5383fa30423
// last-edited: 2026-11-15

package maintenance

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"
)

// This file is the one place the write sets of the converted kinds are NOT
// derived from t6WriterResources (kinds_resources.go). Every other Effects test
// builds its expectation from the table, by method name, so a resource dropped
// from a row shrinks the declaration and the expectation together and nothing
// fails: the F5-11 mutation run dropped sync_identity from the purge row and no
// test noticed. The two literals below are typed by hand from what the kinds and
// the table declared when they were reviewed, and the tests compare the live
// values to them, so a resource lost from ANY row of the table, or from any
// kind's Effects, fails here by name.
//
// Changing a row or a kind's Effects on purpose now means editing the literal in
// the same change. That edit is the point: a narrowing shows up in review as a
// removed word, and has to be justified there (THE RULE in kinds_resources.go: a
// row errs wide). Widening is the same edit the other way.

// t6EffectsPin is one kind's declared write, read and delete sets: space
// separated pkg/ops resource names in module "books", in any order (the tests
// sort both sides before comparing, so a pinned entry never has to mirror the
// order the code happens to build a set in).
type t6EffectsPin struct{ writes, reads, deletes string }

// t6KindEffectsPinned is every native kind of the six converted groups
// (t6AllNativeKinds), by kind.
var t6KindEffectsPinned = map[tasks.Kind]t6EffectsPin{
	"books.itunes.clone-into-library":                      {writes: "authors book_files books embeddings field_states files fingerprint_index indexes metadata_cache operations sync_identity user_state", reads: "", deletes: ""},
	"books.itunes.heal":                                    {writes: "ai_journal authors books external_ids files indexes metadata_cache operations sync_identity user_state", reads: "", deletes: ""},
	"books.itunes.playlist-import":                         {writes: "user_state", reads: "", deletes: ""},
	"books.itunes.regroup":                                 {writes: "authors book_files books embeddings external_ids field_states indexes metadata_cache sync_identity user_state", reads: "", deletes: ""},
	"books.maintenance.activity-filter-index-backfill":     {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.ai-dedup-batch":                     {writes: "review_items", reads: "", deletes: ""},
	"books.maintenance.author-conjunction-repair":          {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-duplicate-merge":             {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-id-repair":                   {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-path-link":                   {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-split-scan":                  {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.author-strip-merge":                 {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.author-title-fragment-scan":         {writes: "", reads: "authors", deletes: ""},
	"books.maintenance.author-whitespace-collision-report": {writes: "", reads: "authors", deletes: ""},
	"books.maintenance.authority-build":                    {writes: "authority_lists", reads: "", deletes: ""},
	"books.maintenance.auto-match-transcribed":             {writes: "authors book_files books field_states indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.book-atpath-index-backfill":         {writes: "books", reads: "", deletes: ""},
	"books.maintenance.book-atpath-index-verify":           {writes: "", reads: "", deletes: ""},
	"books.maintenance.book-hash-index-repair":             {writes: "books", reads: "books", deletes: ""},
	"books.maintenance.book-shape-report":                  {writes: "", reads: "", deletes: ""},
	"books.maintenance.bookfile-acoustid-index-repair":     {writes: "book_files", reads: "book_files", deletes: ""},
	"books.maintenance.booksig-recovery-audit":             {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.booksig-sidecar-migrate":            {writes: "books", reads: "", deletes: ""},
	"books.maintenance.build-folder-book-files":            {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.chapters-backfill":                  {writes: "books", reads: "", deletes: ""},
	"books.maintenance.cleanup-activity-log":               {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.cleanup-old-backups":                {writes: "files", reads: "", deletes: ""},
	"books.maintenance.clear-apply-rename-failures":        {writes: "user_state", reads: "", deletes: ""},
	"books.maintenance.combine-journal-check":              {writes: "", reads: "", deletes: ""},
	"books.maintenance.compact-activity-log":               {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.cover-text-read":                    {writes: "cover_text", reads: "", deletes: ""},
	"books.maintenance.credit-census":                      {writes: "", reads: "", deletes: ""},
	"books.maintenance.credits-backfill":                   {writes: "authors books indexes metadata_cache operations", reads: "authors books series", deletes: ""},
	"books.maintenance.dedup-exact-triage":                 {writes: "embeddings", reads: "", deletes: ""},
	"books.maintenance.dedupe-book-file-rows":              {writes: "authors book_files books files fingerprint_index indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.duration-backfill":                  {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.external-id-backfill":               {writes: "external_ids settings", reads: "", deletes: ""},
	"books.maintenance.extract-wav-clips":                  {writes: "authors book_files books files indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.file-integrity-check":               {writes: "", reads: "", deletes: ""},
	"books.maintenance.file-provenance-capture":            {writes: "file_provenance", reads: "", deletes: ""},
	"books.maintenance.file-provenance-export":             {writes: "file_provenance files", reads: "", deletes: ""},
	"books.maintenance.filepath-collision-report":          {writes: "", reads: "", deletes: ""},
	"books.maintenance.folder-cover-backfill":              {writes: "authors books files indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.fs-regroup-xml":                     {writes: "authors book_files books external_ids indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.ident-index-rebuild":                {writes: "books indexes", reads: "", deletes: ""},
	"books.maintenance.intro-migrate-single-file":          {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.library-optimize":                   {writes: "", reads: "", deletes: ""},
	"books.maintenance.malformed-m4b-remux":                {writes: "authors book_files books files indexes metadata_cache settings", reads: "", deletes: ""},
	"books.maintenance.malformed-m4b-transcode":            {writes: "authors book_files books files indexes metadata_cache settings", reads: "", deletes: ""},
	"books.maintenance.mark-missing-files":                 {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.merge-same-path-dupes":              {writes: "authors book_files books external_ids indexes metadata_cache operations sync_identity user_state", reads: "", deletes: ""},
	"books.maintenance.metadata-cache-reap":                {writes: "indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.metadata-refresh":                   {writes: "", reads: "", deletes: ""},
	"books.maintenance.missing-file-audit":                 {writes: "", reads: "", deletes: ""},
	"books.maintenance.missing-file-repair":                {writes: "", reads: "", deletes: ""},
	"books.maintenance.missing-file-repoint":               {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.move-book-file-rows":                {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.movement-atom-cleanup":              {writes: "authors book_files books file_provenance files indexes metadata_cache settings", reads: "", deletes: ""},
	"books.maintenance.nightly-compact-activity-log":       {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.opchange-book-index-rebuild":        {writes: "operations", reads: "", deletes: ""},
	"books.maintenance.orphan-book-files-cleanup":          {writes: "", reads: "", deletes: ""},
	"books.maintenance.orphan-book-files-repoint-plan":     {writes: "", reads: "", deletes: ""},
	"books.maintenance.probe-directory-books":              {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.prune-ai-journal":                   {writes: "ai_journal", reads: "", deletes: ""},
	"books.maintenance.purge-deleted":                      {writes: "activity authors books embeddings external_ids field_states files indexes metadata_cache sync_identity user_state", reads: "", deletes: ""},
	"books.maintenance.purge-empty-authors":                {writes: "authors books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.purge-empty-narrators":              {writes: "books operations", reads: "", deletes: ""},
	"books.maintenance.recompact-activity-digests":         {writes: "activity", reads: "", deletes: ""},
	"books.maintenance.recover-missing-files":              {writes: "authors book_files books files indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.regroup-shattered-ai":               {writes: "review_items", reads: "", deletes: ""},
	"books.maintenance.relink-unlinked-books":              {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.repair-library-state":               {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.repair-merged-user-state":           {writes: "authors books indexes metadata_cache sync_identity user_state", reads: "", deletes: ""},
	"books.maintenance.repair-transcribe-status":           {writes: "authors books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.repoint-book-file-rows":             {writes: "authors book_files books indexes metadata_cache operations", reads: "", deletes: ""},
	"books.maintenance.resolve-production-authors":         {writes: "authors book_files books field_states indexes metadata_cache operations series", reads: "", deletes: ""},
	"books.maintenance.split-joined-narrators":             {writes: "books operations", reads: "", deletes: ""},
	"books.maintenance.temp-file-cleanup":                  {writes: "activity files", reads: "", deletes: ""},
	"books.maintenance.tombstone-cleanup":                  {writes: "authors", reads: "", deletes: ""},
	"books.maintenance.transcode-temp-cleanup":             {writes: "files", reads: "", deletes: ""},
	"books.maintenance.transcribe-book-intros":             {writes: "ai_journal authors book_files books files indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.transcribe-file-intros":             {writes: "authors book_files books indexes metadata_cache", reads: "", deletes: ""},
	"books.maintenance.trash-cleanup":                      {writes: "books files settings", reads: "", deletes: ""},
}

// t6WriterResourcesPinned is every row of t6WriterResources: the resources a
// store or writer method can write, space separated, in any order.
var t6WriterResourcesPinned = map[string]string{
	"AIJournalPruner":                     "ai_journal",
	"ActivityFlushOp":                     "activity",
	"AppendFileEvent":                     "file_provenance",
	"BackfillBookAtPathIndex":             "books",
	"BackfillExternalIDs":                 "external_ids settings",
	"BackfillFilterIndexWindow":           "activity",
	"BatchCreateBookFiles":                "authors book_files books indexes metadata_cache",
	"CarryOverFingerprintWindows":         "fingerprint_index",
	"CarryStateBeforeHardDelete":          "authors books indexes metadata_cache sync_identity user_state",
	"CarryStateBetweenLiveBooks":          "authors books indexes metadata_cache sync_identity user_state",
	"CarryStateThenHardDelete":            "authors books indexes metadata_cache sync_identity user_state",
	"CleanupOrphanedTempFiles":            "activity files",
	"CleanupStaleTempFiles":               "files",
	"CleanupTrashedVersions":              "books files settings",
	"ClearBookFileIntro":                  "book_files",
	"ClearClassLists":                     "",
	"ClearFilterIndexBackfillDone":        "activity",
	"ClearIntroTimeouts":                  "book_files",
	"ClearUserPositions":                  "user_state",
	"CloneBookIntoLibrary":                "books book_files files authors user_state sync_identity operations indexes metadata_cache field_states embeddings",
	"CollapseHealDuplicates":              "authors books external_ids indexes metadata_cache operations sync_identity user_state",
	"CompactActivityEntries":              "activity",
	"CopyBookmarkIfAbsent":                "user_state",
	"CreateAuthor":                        "authors",
	"CreateBook":                          "books authors indexes",
	"CreateBookFile":                      "authors book_files books indexes metadata_cache",
	"CreateNarrator":                      "books",
	"CreateOperationChange":               "operations",
	"DedupTriageApply":                    "embeddings",
	"DeleteAuthor":                        "authors books indexes metadata_cache",
	"DeleteBook":                          "books authors field_states embeddings metadata_cache user_state indexes",
	"DeleteBookFilesByIDs":                "authors book_files books fingerprint_index indexes metadata_cache",
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
	"MergeBooks":                          "authors books external_ids indexes metadata_cache operations sync_identity user_state",
	"MergeDeleteAuthor":                   "authors books indexes metadata_cache operations",
	"MigrateBookSigToSidecar":             "books",
	"MintOrGetSyncID":                     "sync_identity",
	"ModifyBook":                          "books authors metadata_cache indexes",
	"ModifyBookAuthors":                   "authors",
	"ModifyBookCredits":                   "books authors metadata_cache indexes",
	"ModifyBookFile":                      "authors book_files books indexes metadata_cache",
	"ModifyBookFileDeferAggregates":       "book_files",
	"ModifyBookFileReportingRecompute":    "authors book_files books indexes metadata_cache",
	"MoveBookFilesToBook":                 "authors book_files books indexes metadata_cache",
	"MoveBookFilesToBookBulk":             "authors book_files books indexes metadata_cache",
	"MoveCredits":                         "authors books indexes metadata_cache operations",
	"NewUserStateProbe":                   "",
	"OpenWhisperJournal":                  "ai_journal",
	"QuarantineKnownBadFiles":             "authors book_files books files indexes metadata_cache settings",
	"ReassignExternalID":                  "external_ids",
	"RebuildBookAtPathIndex":              "books",
	"RebuildIdentIndex":                   "indexes",
	"RebuildOpChangeByBookIndex":          "operations",
	"RecompactActivityDigests":            "activity",
	"RecomputeBookAggregates":             "books authors metadata_cache indexes",
	"ReconcileFilterIndexCoverage":        "activity",
	"RecordIntroTimeout":                  "book_files",
	"RecordOperationChange":               "operations",
	"RecordSyncMerge":                     "sync_identity",
	"Reflink":                             "files",
	"RemoveOldBackupFiles":                "files",
	"RemuxMalformedM4BFiles":              "authors book_files books files indexes metadata_cache settings",
	"RenameAuthorJournaled":               "authors operations",
	"RepairBookHashIndex":                 "books",
	"ReplaceOpRowsPrefix":                 "",
	"ResolveTombstoneChains":              "authors",
	"RunAutoPurgeSoftDeleted":             "authors books embeddings external_ids field_states files indexes metadata_cache sync_identity user_state",
	"SaveChaptersForBook":                 "books",
	"SaveClassLists":                      "",
	"SetBookAuthors":                      "authors",
	"SetBookFileHash":                     "authors book_files books indexes metadata_cache",
	"SetBookFileIntro":                    "book_files",
	"SetBookNarrators":                    "books",
	"SetFileProvExportCursor":             "file_provenance",
	"SetLibraryStateOrganized":            "books authors metadata_cache indexes",
	"SetRaw/authority_lists":              "authority_lists",
	"SetRaw/cover_text":                   "cover_text",
	"SetRaw/user_state":                   "user_state",
	"SetUserBookState":                    "user_state",
	"SetUserPosition":                     "user_state",
	"SetUserPositionAt":                   "user_state",
	"SetUserPreferenceForUser":            "user_state",
	"StripMovementAtoms":                  "authors book_files books file_provenance files indexes metadata_cache settings",
	"SubmitAuthorDedupJob":                "review_items",
	"TranscodeMalformedM4BFiles":          "authors book_files books files indexes metadata_cache settings",
	"UpdateBook":                          "books authors metadata_cache indexes",
	"UpdateBookFile":                      "authors book_files books indexes metadata_cache",
	"UpdateBookFiles":                     "authors book_files books indexes metadata_cache",
	"UpsertReviewItem":                    "review_items",
}

// t6PinnedNames is the sorted resource names of refs, which must be in module
// "books".
func t6PinnedNames(t *testing.T, kind tasks.Kind, refs []entity.Ref) string {
	t.Helper()
	names := make([]string, 0, len(refs))
	for _, r := range refs {
		require.Equal(t, "books", r.Module, "%s", kind)
		names = append(names, r.Kind)
	}
	slices.Sort(names)
	return strings.Join(names, " ")
}

// t6PinSorted is the resource names of s in sorted order, duplicates kept: two
// sides are the same list only if they hold the same names the same number of
// times, so a resource listed twice on either side fails the comparison.
func t6PinSorted(s string) string {
	names := strings.Fields(s)
	slices.Sort(names)
	return strings.Join(names, " ")
}

// t6PinDiff names what got lacks of want and what it has beyond it. When both are
// empty the two hold the same names with different counts (a duplicate).
func t6PinDiff(want, got string) string {
	w, g := strings.Fields(want), strings.Fields(got)
	var missing, extra []string
	for _, x := range w {
		if !slices.Contains(g, x) {
			missing = append(missing, x)
		}
	}
	for _, x := range g {
		if !slices.Contains(w, x) {
			extra = append(extra, x)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return "the same names, listed a different number of times"
	}
	return fmt.Sprintf("lost %v, gained %v", missing, extra)
}

// Every native kind declares exactly the write, read and delete sets pinned
// above, and the pinned list has no kind the groups no longer register.
func TestT6Kinds_EffectsEqualThePinnedSets(t *testing.T) {
	seen := map[tasks.Kind]bool{}
	for _, d := range t6AllNativeKinds(t) {
		kind := d.Kind()
		seen[kind] = true
		want, ok := t6KindEffectsPinned[kind]
		if !ok {
			t.Errorf("%s is a native kind with no entry in t6KindEffectsPinned: add one (its declared sets, typed by hand)", kind)
			continue
		}
		e := tasks.CommonOf(d).Effects
		want = t6EffectsPin{writes: t6PinSorted(want.writes), reads: t6PinSorted(want.reads), deletes: t6PinSorted(want.deletes)}
		got := t6EffectsPin{
			writes:  t6PinnedNames(t, kind, e.WriteSet()),
			reads:   t6PinnedNames(t, kind, e.ReadSet()),
			deletes: t6PinnedNames(t, kind, e.DeleteSet()),
		}
		if got.writes != want.writes {
			t.Errorf("%s writes %q, pinned %q (%s)", kind, got.writes, want.writes, t6PinDiff(want.writes, got.writes))
		}
		if got.reads != want.reads {
			t.Errorf("%s reads %q, pinned %q (%s)", kind, got.reads, want.reads, t6PinDiff(want.reads, got.reads))
		}
		if got.deletes != want.deletes {
			t.Errorf("%s deletes %q, pinned %q (%s)", kind, got.deletes, want.deletes, t6PinDiff(want.deletes, got.deletes))
		}
	}
	for kind := range t6KindEffectsPinned {
		if !seen[kind] {
			t.Errorf("t6KindEffectsPinned names %s, which no group registers any more: remove it", kind)
		}
	}
}

// Every row of the shared method-to-resource table is exactly the pinned row, and
// the pinned list has no method the table no longer has. A kind that reaches the
// row inherits the loss, so this is the check on the rows no kind's Effects test
// reads directly (the writers F5-18 gates, the composites' parts).
func TestT6WriterResources_EqualThePinnedRows(t *testing.T) {
	for method, row := range t6WriterResources {
		names := make([]string, 0, len(row))
		for _, r := range row {
			names = append(names, string(r))
		}
		got := t6PinSorted(strings.Join(names, " "))
		want, ok := t6WriterResourcesPinned[method]
		if !ok {
			t.Errorf("t6WriterResources has a row for %s with no entry in t6WriterResourcesPinned: add one (typed by hand)", method)
			continue
		}
		want = t6PinSorted(want)
		if got != want {
			t.Errorf("t6WriterResources[%s] is %q, pinned %q (%s)", method, got, want, t6PinDiff(want, got))
		}
	}
	for method := range t6WriterResourcesPinned {
		if _, ok := t6WriterResources[method]; !ok {
			t.Errorf("t6WriterResourcesPinned names %s, which the table no longer has: remove it", method)
		}
	}
}
