// file: internal/plugins/maintenance/kinds_resources.go
// version: 1.0.4
// guid: 3f9b6d21-5a84-4e07-b1c3-7d2e08a46f59
// last-edited: 2026-11-15

package maintenance

import (
	"reflect"
	"slices"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// THE RULE. A row lists EVERYTHING its store method can write, through its whole
// call chain, whoever calls it. A row is never narrowed to what one caller's
// closure happens to touch: ModifyBook is the same row for duration-backfill (it
// sets Duration only), folder-cover (cover fields) and a title edit, and
// RecomputeBookAggregates, which reaches ModifyBook, is the same row as
// ModifyBook. The store decides per call what it stages (the stale stamp, the
// ident: rows, the author_books_legacy: rows), a declaration cannot know that
// ahead of the call, and the admission gate cannot detect an under-declared kind
// (it compares DECLARED write sets, core/internal/executor/dispatch.go, and the
// writer checks one tag per Do), so a row errs wide. The cost is coarser
// exclusion, never a missed one; narrowing a method needs the store to guarantee
// the narrower write, not a caller's closure.
//
// Consequence worth knowing: every kind that calls ModifyBook, UpdateBook,
// RecomputeBookAggregates, SetLibraryStateOrganized, a CarryState method, or a
// file-row method that recomputes the books aggregate declares the whole book
// write (books, authors, metadata_cache, indexes). Applying the rule widened 25
// kinds by one to three resources each (authors, metadata_cache, indexes;
// docs/agent-tasks/platform-core/seams.d/F5-10.md lists them).
//
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
// Traced rows (the chains are in docs/agent-tasks/platform-core/seams.d/F5-10.md
// and the seams files of the other groups):
//
//   - ModifyBook, UpdateBook: the books row; the stale stamp on metadata_cache:<id>
//     when the write changes the book's search identity (title or author id;
//     SearchIdentityChanged, an AuthorID going from nil to set counts, and the
//     ASIN replacement stamps it too); the ident: rows the store stages when the
//     identity, the ASIN or the book-side identification inputs (review status,
//     primary flag, trash, quarantine) change; and the author_books_legacy: rows
//     it stages on every call where AuthorID is set (authors: the storediff map
//     files the author_ families under authors). BOOKW below is that row.
//   - ident: is `indexes`. The ident: family is the identification driver's derived
//     state (ident_state.go), the same set RebuildIdentIndex rebuilds, so every
//     method that stages it declares `indexes`: CreateBook, ModifyBook, UpdateBook,
//     DeleteBook, MergeBooks (MarkIdentDirtyBestEffort), DeleteMetadataCache
//     (commitMetadataCacheWrite) and ModifyBookCredits (updateBookLockedMode).
//     F5-10's store-diff table and its vocabulary comment filed ident: under
//     `books` until the stack settled this; F5-07, F5-09 and F5-11 always said
//     `indexes`.
//   - The file-row methods that notify the aggregate recompute (CreateBookFile,
//     UpdateBookFile, UpdateBookFiles, ModifyBookFile, SetBookFileHash,
//     MoveBookFilesToBook(Bulk), DeleteBookFilesByIDs, BatchCreateBookFiles) are
//     book_files plus BOOKW (notifyBookFileChange -> RecomputeBookAggregates ->
//     ModifyBook). SetBookFileHash recomputes only when the hash can change an
//     equality between two rows of the book; the row is conservative. The intro
//     and timeout methods and ModifyBookFileDeferAggregates leave the aggregate to
//     the caller and are book_files only.
//   - RecomputeBookAggregates calls ModifyBook with a closure that only moves the
//     aggregate fields (duration, size, counts). Those are not search identity,
//     so today it stamps no cache and stages no ident: rows, but it does write the
//     author_books_legacy: rows through ModifyBook whenever AuthorID is set. Per
//     THE RULE it carries the whole ModifyBook row: that is a guarantee the store
//     does not make for the method, only a fact about this closure.
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
// several resources is a contract F5-18's writers must meet by naming every
// resource of the row in one call; seams.d/F5-10.md records it as an F5-18
// obligation.
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
	// bookW is everything ModifyBook can write (THE RULE above): the books row,
	// the author_books_legacy: rows it stages when AuthorID is set (authors), the
	// stale stamp (metadata_cache) and the ident: rows (indexes). Every method that
	// reaches ModifyBook carries it whole.
	bookW := []ops.Resource{ops.ResBooks, ops.ResAuthors, ops.ResMetadataCache, ops.ResIndexes}
	// fileRow is a file-row method that recomputes the books aggregate: its own
	// book_files row plus BOOKW.
	fileRow := t6RowsUnion([]ops.Resource{ops.ResBookFiles}, bookW)
	rows := map[string][]ops.Resource{
		// --- book rows and their derived state -----------------------------
		// CreateBook stages the ident: rows and the author_books_legacy: rows; a
		// new book has no cache row to stamp.
		"CreateBook":               {ops.ResBooks, ops.ResAuthors, ops.ResIndexes},
		"ModifyBook":               bookW,
		"UpdateBook":               bookW,
		"RecomputeBookAggregates":  bookW,
		"SaveChaptersForBook":      {ops.ResBooks},
		"SetLibraryStateOrganized": bookW,
		// DeleteBook removes the book's author links, field-state row, embedding
		// and dedup candidates and metadata cache with the book row (and its
		// user_tag:book: rows, the only listening-state key it deletes); deleteBook
		// stages the ident: rows.
		"DeleteBook":          {ops.ResBooks, ops.ResAuthors, ops.ResFieldStates, ops.ResEmbeddings, ops.ResMetadataCache, ops.ResUserState, ops.ResIndexes},
		"DeleteMetadataCache": {ops.ResMetadataCache, ops.ResIndexes},
		// MergeBooks: ModifyBook / SoftDeleteBook on every member (the whole bookW
		// row: its closures set no Title, AuthorID or ASIN today, so no cache is
		// stamped today, but the method calls ModifyBook and THE RULE gives it the
		// ModifyBook row), the losers' external ids, their listening state
		// following the survivor, their sync ids and redirects, the undo journal
		// and the sibling-move journal, and MarkIdentDirtyBestEffort.
		"MergeBooks": t6RowsUnion(bookW, []ops.Resource{ops.ResExternalIDs, ops.ResUserState, ops.ResSyncIdentity, ops.ResOperations}),
		// The clone creates the copy (CreateBook: books, authors, ident:), its files
		// (book_files, and the bytes: files), hands over the source's sync id and
		// listening state, and journals the clone. Its rollback runs DeleteBook on
		// the new id, which also removes the field-state row, the embedding and the
		// metadata cache.
		"CloneBookIntoLibrary": {ops.ResBooks, ops.ResBookFiles, ops.ResFiles, ops.ResAuthors, ops.ResUserState, ops.ResSyncIdentity, ops.ResOperations, ops.ResIndexes, ops.ResMetadataCache, ops.ResFieldStates, ops.ResEmbeddings},
		// updateBookLockedMode: the books row, the junction (authors), the stale
		// stamp and the ident: rows.
		"ModifyBookCredits": {ops.ResBooks, ops.ResAuthors, ops.ResMetadataCache, ops.ResIndexes},
		// The carry moves listening state (user_state), follows the absorbed
		// book's sync id onto the survivor (sync_identity) and, when the loser was
		// finished, stamps the play-count mark on the winner's book row through
		// ModifyBook (the whole bookW row).
		"CarryStateBeforeHardDelete": t6RowsUnion(bookW, []ops.Resource{ops.ResUserState, ops.ResSyncIdentity}),
		"CarryStateBetweenLiveBooks": t6RowsUnion(bookW, []ops.Resource{ops.ResUserState, ops.ResSyncIdentity}),
		"CarryStateThenHardDelete":   t6RowsUnion(bookW, []ops.Resource{ops.ResUserState, ops.ResSyncIdentity}),
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
		"CreateBookFile":          fileRow,
		"UpdateBookFile":          fileRow,
		"UpdateBookFiles":         fileRow,
		"ModifyBookFile":          fileRow,
		"SetBookFileHash":         fileRow,
		"MoveBookFilesToBook":     fileRow,
		"MoveBookFilesToBookBulk": fileRow,
		"DeleteBookFilesByIDs":    t6RowsUnion(fileRow, []ops.Resource{ops.ResFingerprintIndex}),
		"BatchCreateBookFiles":    fileRow,
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
		// HealWhisperJournal hands the heal the journal shared with the intro
		// transcribe kinds; the heal's transcription writes aijournal: keys
		// through it.
		"HealWhisperJournal":      {ops.ResAIJournal},
		"RebuildIdentIndex":       {ops.ResIndexes},
		"AppendFileEvent":         {ops.ResFileProvenance},
		"SetFileProvExportCursor": {ops.ResFileProvenance},
		"SetRaw/authority_lists":  {ops.ResAuthorityLists},
		"DeleteRawBatch":          {ops.ResAuthorityLists},
		"SetRaw/cover_text":       {ops.ResCoverText},
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
//
// A capability getter, a no-argument method returning (interface, bool) such as
// UserStateSyncWrites, is not a write itself: it hands out the interface the
// writes go through. The view walks the returned interface's methods too (and
// skips the getter), so a write that only exists behind a capability is in the
// view and in every coverage test built from it.
func t6WriterView(rename map[string]string, writers ...reflect.Type) map[string][]ops.Resource {
	view := map[string][]ops.Resource{}
	for _, name := range t6WriterMethods(writers...) {
		key := name
		if q, ok := rename[name]; ok {
			key = q
		}
		if row, ok := t6WriterResources[key]; ok {
			view[name] = row
		}
	}
	return view
}

// t6WriterMethods is the sorted, de-duplicated names of every write method of the
// given writer interfaces, walking capability getters (see t6WriterView) and not
// listing the getters themselves. A coverage test compares it with a view's keys.
func t6WriterMethods(writers ...reflect.Type) []string {
	var names []string
	var walk func(w reflect.Type)
	walk = func(w reflect.Type) {
		for i := range w.NumMethod() {
			m := w.Method(i)
			if capIface, ok := t6CapabilityGetter(m); ok {
				walk(capIface)
				continue
			}
			names = append(names, m.Name)
		}
	}
	for _, w := range writers {
		walk(w)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// t6CapabilityGetter reports whether the interface method m is a capability
// getter (no arguments, results (interface, bool)) and returns the interface it
// hands out.
func t6CapabilityGetter(m reflect.Method) (reflect.Type, bool) {
	ft := m.Type
	if ft.NumIn() != 0 || ft.NumOut() != 2 || ft.Out(1).Kind() != reflect.Bool || ft.Out(0).Kind() != reflect.Interface {
		return nil, false
	}
	return ft.Out(0), true
}
