// file: internal/plugins/maintenance/kinds_resources.go
// version: 1.0.0
// guid: 3f9b6d21-5a84-4e07-b1c3-7d2e08a46f59
// last-edited: 2026-11-15

package maintenance

import (
	"reflect"
	"slices"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6WriterResources is the ONE method-to-resource table of the six converted
// maintenance groups (F5-07 .. F5-11 and F5-06's authority build). Every
// recording spy, every Effects test and F5-18's gated writers read a row of it
// by method name; no group keeps a table of its own. Before the integration
// stack each group typed its own rows, and the rows disagreed (ModifyBook was
// {books} in five groups and {books, metadata_cache} in F5-10; SetRaw is three
// different key families; the file-row methods wrote or did not write the books
// aggregate depending on who asked), so a kind could declare less than the store
// writes and nothing compared the tables.
//
// A row is what the method writes through its whole call chain, not only the
// store method it names, and it is conservative where a callee is. Traced rows
// (the chains are in docs/agent-tasks/platform-core/seams.d/F5-10.md and the
// seams files of the other groups):
//
//   - ModifyBook, UpdateBook: the books row; the stale stamp on metadata_cache:<id>
//     when the write changes the book's search identity (title, author, narrator;
//     SearchIdentityChanged, an AuthorID going from nil to set counts); and the
//     ident: rows the store stages when the identity, the ASIN or the book-side
//     identification inputs (review status, primary flag, trash, quarantine)
//     change. The store decides per call, so the row carries both whoever calls.
//   - ident: is `indexes`. The ident: family is the identification driver's derived
//     state (ident_state.go), the same set RebuildIdentIndex rebuilds, so every
//     method that stages it declares `indexes`: CreateBook, ModifyBook, UpdateBook,
//     DeleteBook, MergeBooks (MarkIdentDirtyBestEffort), DeleteMetadataCache
//     (commitMetadataCacheWrite) and ModifyBookCredits (updateBookLockedMode).
//     F5-10's store-diff table and its vocabulary comment filed ident: under
//     `books` until the stack settled this; F5-07, F5-09 and F5-11 always said
//     `indexes`.
//   - The file-row methods that notify the aggregate recompute (CreateBookFile,
//     UpdateBookFile, UpdateBookFiles, SetBookFileHash, MoveBookFilesToBookBulk,
//     DeleteBookFilesByIDs) write the books row too (notifyBookFileChange ->
//     RecomputeBookAggregates). SetBookFileHash recomputes only when the hash
//     can change an equality between two rows of the book; the row is
//     conservative. The intro and timeout methods and ModifyBookFileDeferAggregates
//     leave the aggregate to the caller and are book_files only.
//   - RecomputeBookAggregates reaches ModifyBook but its closure only moves the
//     aggregate fields (duration, size, counts), which are not search identity,
//     so it is books alone.
//
// Composite rows, the group-level writer methods whose chain is several store
// calls, are built from the base rows here, so a base row that widens widens
// every composite that reaches it (t6WriterComposites).
//
// An empty row is a method that writes nothing itself: it runs a closure whose
// write arrives as its own method call, drops an in-memory cache, or only reads.
// It is written as an explicit empty list, never left nil.
//
// A tasks.Write carries one resource per Do (core/tasks/writer.go), so a row of
// several resources is a contract F5-18's writers must meet by stamping a Do per
// resource (or by declaring a writer per group); seams.d/F5-10.md records it as
// an F5-18 obligation.
//
// Qualified names: a method name that means a different key family on different
// writer interfaces is split by family (SetRaw/authority_lists, SetRaw/cover_text,
// SetRaw/user_state). A group's view maps its interface's method to the qualified
// row with the rename argument of t6WriterView.
var t6WriterResources = t6WriterResourceTable()

// t6RowsUnion is the sorted, de-duplicated union of the given rows, as a new
// non-nil slice.
func t6RowsUnion(rows ...[]ops.Resource) []ops.Resource {
	out := []ops.Resource{}
	for _, row := range rows {
		for _, r := range row {
			if !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	slices.Sort(out)
	return out
}

// t6WriterResourceTable builds t6WriterResources: the base rows, then the
// composites derived from them.
func t6WriterResourceTable() map[string][]ops.Resource {
	rows := map[string][]ops.Resource{
		// --- book rows and their derived state -----------------------------
		"CreateBook":               {ops.ResBooks, ops.ResIndexes},
		"ModifyBook":               {ops.ResBooks, ops.ResMetadataCache, ops.ResIndexes},
		"UpdateBook":               {ops.ResBooks, ops.ResMetadataCache, ops.ResIndexes},
		"RecomputeBookAggregates":  {ops.ResBooks},
		"SaveChaptersForBook":      {ops.ResBooks},
		"SetLibraryStateOrganized": {ops.ResBooks},
		// DeleteBook removes the book's author links, field-state row, embedding
		// and dedup candidates and metadata cache with the book row (and the
		// listening state keyed on it); deleteBook stages the ident: rows.
		"DeleteBook":          {ops.ResBooks, ops.ResAuthors, ops.ResFieldStates, ops.ResEmbeddings, ops.ResMetadataCache, ops.ResUserState, ops.ResIndexes},
		"DeleteMetadataCache": {ops.ResMetadataCache, ops.ResIndexes},
		// MergeBooks: ModifyBook / SoftDeleteBook on every member (books, the
		// stale stamp, the ident: rows), the losers' external ids, their listening
		// state following the survivor, their sync ids and redirects, the undo
		// journal and the sibling-move journal, and MarkIdentDirtyBestEffort.
		"MergeBooks": {ops.ResBooks, ops.ResExternalIDs, ops.ResUserState, ops.ResSyncIdentity, ops.ResOperations, ops.ResMetadataCache, ops.ResIndexes},
		// The clone creates the copy (CreateBook: books, ident:), its files
		// (book_files, and the bytes: files), hands over the source's sync id and
		// listening state, and journals the clone.
		"CloneBookIntoLibrary": {ops.ResBooks, ops.ResBookFiles, ops.ResFiles, ops.ResAuthors, ops.ResUserState, ops.ResSyncIdentity, ops.ResOperations, ops.ResIndexes},
		// updateBookLockedMode: the books row, the junction (authors), the stale
		// stamp and the ident: rows.
		"ModifyBookCredits": {ops.ResBooks, ops.ResAuthors, ops.ResMetadataCache, ops.ResIndexes},
		// The carry moves listening state (user_state), follows the absorbed
		// book's sync id onto the survivor (sync_identity) and, when the loser was
		// finished, stamps the play-count mark on the winner's book row (books).
		"CarryStateBeforeHardDelete": {ops.ResBooks, ops.ResUserState, ops.ResSyncIdentity},
		"CarryStateBetweenLiveBooks": {ops.ResBooks, ops.ResUserState, ops.ResSyncIdentity},
		"CarryStateThenHardDelete":   {ops.ResBooks, ops.ResUserState, ops.ResSyncIdentity},
		"HardDeleteWithoutUserState": {},
		"NewUserStateProbe":          {},
		// --- narrators, authors, credits --------------------------------------
		"CreateNarrator":         {ops.ResBooks},
		"DeleteNarrator":         {ops.ResBooks},
		"SetBookNarrators":       {ops.ResBooks},
		"CreateAuthor":           {ops.ResAuthors},
		"ModifyBookAuthors":      {ops.ResAuthors},
		"SetBookAuthors":         {ops.ResAuthors},
		"RenameAuthorJournaled":  {ops.ResAuthors, ops.ResOperations},
		"ResolveTombstoneChains": {ops.ResAuthors},
		// InvalidateAuthorsCache drops an in-memory cache and writes nothing; the
		// tombstone chain pass that calls it declares authors through
		// ResolveTombstoneChains.
		"InvalidateAuthorsCache": {},
		// --- book_file rows -----------------------------------------------------
		"CreateBookFile":          {ops.ResBookFiles, ops.ResBooks},
		"UpdateBookFile":          {ops.ResBookFiles, ops.ResBooks},
		"UpdateBookFiles":         {ops.ResBookFiles, ops.ResBooks},
		"ModifyBookFile":          {ops.ResBookFiles, ops.ResBooks},
		"SetBookFileHash":         {ops.ResBookFiles, ops.ResBooks},
		"MoveBookFilesToBook":     {ops.ResBookFiles, ops.ResBooks},
		"MoveBookFilesToBookBulk": {ops.ResBookFiles, ops.ResBooks},
		"DeleteBookFilesByIDs":    {ops.ResBookFiles, ops.ResBooks, ops.ResFingerprintIndex},
		"BatchCreateBookFiles":    {ops.ResBookFiles, ops.ResBooks},
		// The intro and timeout methods leave the books aggregate to the caller.
		"ClearBookFileIntro":                  {ops.ResBookFiles},
		"ClearIntroTimeouts":                  {ops.ResBookFiles},
		"RecordIntroTimeout":                  {ops.ResBookFiles},
		"SetBookFileIntro":                    {ops.ResBookFiles},
		"ModifyBookFileDeferAggregates":       {ops.ResBookFiles},
		"CarryOverFingerprintWindows":         {ops.ResFingerprintIndex},
		"DeleteOrphanBookFileAcoustIDEntries": {ops.ResBookFiles},
		// --- files on disk -------------------------------------------------------
		"Reflink":                  {ops.ResFiles},
		"HealTrack":                {ops.ResFiles},
		"CleanupOrphanedTempFiles": {ops.ResActivity, ops.ResFiles},
		"CleanupStaleTempFiles":    {ops.ResFiles},
		"RemoveOldBackupFiles":     {ops.ResFiles},
		// --- external ids, user state, sync ids ---------------------------------
		"ReassignExternalID":       {ops.ResExternalIDs},
		"BackfillExternalIDs":      {ops.ResExternalIDs, ops.ResSettings},
		"ImportSmartPlaylists":     {ops.ResUserState},
		"SetUserPreferenceForUser": {ops.ResUserState},
		"SetRaw/user_state":        {ops.ResUserState},
		"DeleteRaw":                {ops.ResUserState},
		"SetUserBookState":         {ops.ResUserState},
		"SetUserPosition":          {ops.ResUserState},
		"SetUserPositionAt":        {ops.ResUserState},
		"ClearUserPositions":       {ops.ResUserState},
		"CopyBookmarkIfAbsent":     {ops.ResUserState},
		"MintOrGetSyncID":          {ops.ResSyncIdentity},
		"RecordSyncMerge":          {ops.ResSyncIdentity},
		// --- undo journal, review items, journals, stores of one resource ----------
		"RecordOperationChange":         {ops.ResOperations},
		"CreateOperationChange":         {ops.ResOperations},
		"JournalStep":                   {ops.ResOperations},
		"MarkOperationChangesReverted":  {ops.ResOperations},
		"RebuildOpChangeByBookIndex":    {ops.ResOperations},
		"EnsureOpChangeByBookIndexWith": {ops.ResOperations},
		"UpsertReviewItem":              {ops.ResReviewItems},
		"DeleteReviewItemIfPending":     {ops.ResReviewItems},
		"SubmitAuthorDedupJob":          {ops.ResReviewItems},
		"DedupTriageApply":              {ops.ResEmbeddings},
		"AIJournalPruner":               {ops.ResAIJournal},
		"OpenWhisperJournal":            {ops.ResAIJournal},
		"RebuildIdentIndex":             {ops.ResIndexes},
		"AppendFileEvent":               {ops.ResFileProvenance},
		"SetFileProvExportCursor":       {ops.ResFileProvenance},
		"SetRaw/authority_lists":        {ops.ResAuthorityLists},
		"DeleteRawBatch":                {ops.ResAuthorityLists},
		"SetRaw/cover_text":             {ops.ResCoverText},
		// The activity store.
		"ActivityFlushOp":              {ops.ResActivity},
		"CompactActivityEntries":       {ops.ResActivity},
		"MaintainActivityLog":          {ops.ResActivity},
		"RecompactActivityDigests":     {ops.ResActivity},
		"BackfillFilterIndexWindow":    {ops.ResActivity},
		"MarkFilterIndexBackfillDone":  {ops.ResActivity},
		"ClearFilterIndexBackfillDone": {ops.ResActivity},
		"ReconcileFilterIndexCoverage": {ops.ResActivity},
		// The run's own rows (opv2:row:) and the judged markers are op rows, not an
		// Effects resource: mapped, with no resource.
		"ReplaceOpRowsPrefix": {},
		"SaveClassLists":      {},
		"ClearClassLists":     {},
		// The pass index keys of the books group: by-book keys of one book row
		// (book_atpath, book_hash, the signature sidecar), all books.
		"RepairBookHashIndex":     {ops.ResBooks},
		"RebuildBookAtPathIndex":  {ops.ResBooks},
		"BackfillBookAtPathIndex": {ops.ResBooks},
		"MigrateBookSigToSidecar": {ops.ResBooks},
	}
	t6WriterComposites(rows)
	return rows
}

// t6WriterComposites adds the rows of the group-level writer methods whose
// chain is several base rows. Each is built from the base rows it reaches, so
// the chain is stated once and a widened base row widens the composite.
func t6WriterComposites(rows map[string][]ops.Resource) {
	// F5-11's heal collapses duplicates through merge.Service.MergeBooksWithOptions:
	// the same chain as MergeBooks.
	rows["CollapseHealDuplicates"] = t6RowsUnion(rows["MergeBooks"])
	// The purge (audiobooks.PurgeSoftDeletedUnattended): the carry
	// (merge.CarryStateThenHardDelete(Locked)) and then store DeleteBook per book,
	// plus what the purge itself writes: the books row it retires, the external ids
	// it reroutes, the bytes it removes (files) and its undo journal. The carry and
	// the delete are one source with F5-10's rows: a purge excludes every kind
	// that writes what they write.
	rows["RunAutoPurgeSoftDeleted"] = t6RowsUnion(
		[]ops.Resource{ops.ResBooks, ops.ResExternalIDs, ops.ResFiles, ops.ResOperations},
		rows["CarryStateThenHardDelete"], rows["DeleteBook"])
	// Trash cleanup (versions.CleanupTrashedVersions) does NOT go through the
	// carry or DeleteBook: its store surface (versions.TrashedVersionCleaner) writes
	// version rows (UpdateBookVersion: books), the purge unlinks the version's files
	// (files), and the merge-stop streak counter is a raw key
	// (versions:trash_cleanup:merge_stop_streak) kept like a setting (settings).
	rows["CleanupTrashedVersions"] = t6RowsUnion([]ops.Resource{ops.ResBooks, ops.ResFiles, ops.ResSettings})
	// The author merge writers (internal/authormerge): DeleteAuthor and
	// MergeDeleteAuthor run the store's deleteAuthor, whose
	// repointPrimaryScalarsBeforeDelete calls ModifyBook on every book whose
	// primary author is the one deleted; MoveCredits writes through
	// ModifyBookCredits (or SetBookAuthors plus ModifyBook) and journals the move.
	rows["DeleteAuthor"] = t6RowsUnion([]ops.Resource{ops.ResAuthors}, rows["ModifyBook"])
	rows["MergeDeleteAuthor"] = t6RowsUnion([]ops.Resource{ops.ResAuthors, ops.ResOperations}, rows["ModifyBook"])
	rows["MoveCredits"] = t6RowsUnion([]ops.Resource{ops.ResOperations}, rows["ModifyBookCredits"], rows["SetBookAuthors"])
	// F5-07's boot-time writers.
	rows["StripMovementAtoms"] = t6RowsUnion(
		[]ops.Resource{ops.ResFiles, ops.ResSettings, ops.ResFileProvenance}, rows["UpdateBookFile"])
	rows["RemuxMalformedM4BFiles"] = t6RowsUnion([]ops.Resource{ops.ResFiles, ops.ResSettings}, rows["UpdateBookFile"])
	rows["TranscodeMalformedM4BFiles"] = t6RowsUnion([]ops.Resource{ops.ResFiles, ops.ResSettings}, rows["UpdateBookFile"])
	// QuarantineBook renames the files into .failed/ (files), repoints each
	// book_file row through UpdateBookFile, records the move with RecordPathChange
	// (path history lives on the book's key space: books) and calls ModifyBook;
	// then the done flag (settings).
	rows["QuarantineKnownBadFiles"] = t6RowsUnion(
		[]ops.Resource{ops.ResFiles, ops.ResSettings, ops.ResBooks}, rows["UpdateBookFile"], rows["ModifyBook"])
}

// t6WriterView is the rows of t6WriterResources for the methods of the given
// writer interfaces: a group's view of the shared table. rename sends a method
// name to the qualified row it means on these interfaces (SetRaw is a different
// key family per group); a method with no row is left out, so the group's
// coverage test, which compares the view's keys with the interfaces' methods,
// fails for it. The rows are the shared slices: a view never copies a row, so it
// cannot drift from the table.
func t6WriterView(rename map[string]string, writers ...reflect.Type) map[string][]ops.Resource {
	view := map[string][]ops.Resource{}
	for _, w := range writers {
		for i := range w.NumMethod() {
			name := w.Method(i).Name
			key := name
			if q, ok := rename[name]; ok {
				key = q
			}
			if row, ok := t6WriterResources[key]; ok {
				view[name] = row
			}
		}
	}
	return view
}
