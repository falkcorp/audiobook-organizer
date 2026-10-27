// file: internal/plugins/maintenance/credits_backfill.go
// version: 1.2.3
// guid: c57e24de-1f92-4c65-ab7d-c7938aef4df2
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- credits-backfill ---
//
// WHY THIS EXISTS. PR 3 of docs/plans/2026-10-04-author-narrator-credit-lists-audit.md
// (owner-approved 2026-10-04, re-confirmed 2026-10-26 as D54). The UI, the list
// endpoints and ABS read the credit lists (book_authors, book_narrators), but
// the production census of 2026-10-26 (maintenance.credit-census) found 28,349
// books whose narrator credit lives only in the Narrator column and 25,800
// whose author lives only in Book.AuthorID: they look narrator-less or
// author-less to every list reader. This op fills and repairs the lists from
// the flat fields, so the lists can become the only truth (PR 4-6).
//
// SOURCES, in the plan's order: the existing join; Book.AuthorID (position 0
// when the join lacks it, the tie-breaker when positions tie); the Book.Author
// snapshot (only when there is neither, and only by id: no name lookup); then
// for narrators util.CleanNarratorCredit(Narrator), the rule the column ->
// junction sync applies (database.syncNarratorJunctionAfterWrite), and
// NarratorsJSON when the column is empty. The census (credit_census.go)
// classifies every book; this op plans from the same classification
// (classifyCreditBook) and the same frozen author/narrator index, so the two
// cannot disagree about which books are broken.
//
// WHAT IT NEVER DOES.
//   - It never creates an author. Every author it credits is a record the book
//     already names by id (AuthorID, the join, the snapshot).
//   - It never creates a narrator from a SPLIT unless the owner opts that class
//     in (allow: ["narrator_new_from_split"]), the rule
//     maintenance.repair-combined-author-credits follows for authors (new
//     people from a split only through an owner-reviewed class). A credit that
//     is one name ("Jim Dale") may create that one narrator record, as the
//     sync does on every write; that is not a split.
//   - It never writes a book in a REVIEW class: ids that dangle or resolve
//     through a tombstone, combined-name records, a narrator column that is not
//     a list of people, a junction that disagrees with the column, a junction
//     with no column, NarratorsJSON that disagrees with both, AuthorID in the
//     join but not first. Those are counted and listed for a person; the plan
//     specifies no fix for them. The same holds for a placeholder author
//     ("Unknown Author": author_placeholder), a junk narrator ("N/A",
//     "Various": narrator_junk_name), a narrator name that folds (case and
//     accents) onto two records (narrator_name_ambiguous), and an AuthorID the
//     Book.Author snapshot contradicts, which a rescan may have moved on
//     purpose (author_id_snapshot_conflict): the snapshot names a join member
//     other than AuthorID where AuthorID would be prepended or would win the
//     tie-break of tied positions that it reorders, and, where the join is
//     empty (the organizer then files under the snapshot), any live author
//     other than AuthorID: by id when the snapshot's id resolves, and by the
//     snapshot's NAME when it does not (id 0, or no such record), compared
//     with the record names of the join members other than AuthorID (with a
//     join) or with AuthorID's record name (empty join); a comparison of
//     names the book carries, not a lookup. The signal is the SNAPSHOT only. The join's stored
//     order alone is not one: a book with all-zero positions and no
//     contradicting snapshot is renumbered with AuthorID first, as the plan's
//     tie-break rule says, and listed in filing_author_changes when that moves
//     the filing author. Where there is no snapshot signal the plan's rule
//     stands: AuthorID is prepended and the book is listed in
//     filing_author_changes.
//   - It never writes Doctor Who / Big Finish / Torchwood books (owner rule:
//     author and narrator are often swapped there and the owner applies those
//     by hand), books with a file under books/itunes/** unless the owner opts
//     in (allow: ["itunes"]; the write is database-only), a book whose author
//     field a person locked (the whole author side is held, not only the
//     writes that move the filing author), or a book whose narrator field a
//     person locked (narrator side). Repair locks do not hold.
//
// WRITES go through ModifyBookCredits (credits_store.go): the row and both
// lists in one Pebble batch under the book and both credit stripes. Before the
// write the book's pre-image (both lists exactly as stored and the four flat
// fields) and the exact post-image are journaled as one
// undo.ChangeTypeCreditsBackfill row; the write itself re-checks the pre-image
// under the stripes and refuses if anything moved since the plan. A write that
// is refused or fails voids its own journal row, so a revert never acts on a
// write that did not land. Undo with the apply operation's revert
// (POST /operations/:id/revert), which is compare-and-set on the whole state
// (audiobooks.revertCreditsBackfill) and restores the credit lists in their
// STORED order (undo.CreditsBackfillState.Restored), not the canonical one:
// the organizer files under the first stored row.
//
// STAND-DOWN. An apply holds the library scan down (registry.HoldScanStandDown)
// from before it verifies earlier attempts' journal rows to the end of the
// last write: the plan is made from rows a running scan could rewrite, and a
// full-library planning pass takes longer than the registry's 5-minute lease.
// The hold renews the lease on every item of both passes (RunItems beats a
// hold its context carries) and before each write; a lease that lapses
// anyway cancels the run with registry.ErrScanStandDownLost, which is the
// error the result reports. A dry run holds nothing.
//
// COST. Each write rewrites the book row (a book_ver snapshot and a search
// reindex). A write that changes the author a book is searched under also
// invalidates its metadata candidates, so the next metadata run re-searches it
// and spends Google Books quota (1,000 requests a day). The result reports
// that as books_to_reidentify (class search_identity_changes), in a dry run
// too. The op declares Writes: ResBooks (also in a dry run, like the other ops
// that declare writes), so it is gated against other book writers.
//
// CONCURRENCY. Three phases over ids that are sorted and de-duplicated, so every
// book is planned and written by exactly one worker: a disjoint partition by
// book id, no two workers ever touch the same row.
//  1. Plan and guard every book concurrently (errgroup bounded by NumCPU).
//  2. Pick the limit cohort serially: the first N eligible books by sorted id,
//     N reduced by the books earlier attempts of this operation already wrote.
//     A dry run picks the same cohort, so it shows what the apply would write.
//  3. Write the cohort concurrently (registry.RunItems with Concurrency set:
//     the default is one worker).
//
// Shared state is the class accumulator (mutex), the plan map (mutex) and
// atomics; Label runs inside the workers and reads atomics only. Narrator
// creation is serialised under one run-level mutex and keyed by the folded
// name, so two books naming the same new narrator (even spelled with different
// accents) get one record.
//
// RESUME. ResumeRestart re-runs the whole pass; that is the resume. Every
// item is idempotent: a book the earlier attempt wrote now plans clean (no
// second journal row), so the re-run costs one read pass and writes only what
// is left. The written lists of earlier attempts come back from this
// operation's own journal rows (GetOperationChanges), so the result covers the
// whole operation. A position watermark (chapters_backfill's pattern) is
// deliberately not used: it would drop the review and held lists of every book
// below it from the result, and every count here must click through.
//
// EVERY COUNT CLICKS THROUGH. The result (GET /operations/:id/result) carries
// every class with its count and a few before/after samples per write class;
// the sorted book ids of each class are paged from
// GET /operations/:id/class-lists/:class?offset=&limit=. A dry run writes
// nothing at all, not even journal rows (the stored result's id lists are the
// one exception: they are the run's output).
//
// RESULT SIZE. The id lists are deliberately not capped: a cap would break
// "every count clicks through". A book sits in several classes at once (a write
// class or two, usually filing_author_changes or search_identity_changes, and
// any census-derived review or held class), so a full apply over the 55,000 book
// census lists roughly 150,000 to 300,000 ids: about 5-9 MB, up to 12 MB. Kept
// in the operation's own opv2 row that was read whole by GetOperationV2, by the
// /system/status poll (ListRecentOperationsV2 decodes every row of the last
// hour), by the boot scan and by the diagnostics bundle, and the v3 mirror
// stored it a second time. So the lists are stored as op rows
// (database.OpRowStore, the way internal/repairs/planstore.go keeps a plan's
// rows; database.SaveClassLists, one row per 1,000 ids, in its own row
// namespace, tagged with the save's attempt nonce) and the operation's
// result keeps the summary, the counts and the samples only
// (storeCreditsBackfillResult). The store must be able to keep op rows: the run
// refuses to start without it. A run with a limit still plans every book, so it
// stores the full-library review and held lists again for its own operation id;
// that is deliberate (they are that run's evidence), and a limited run's lists
// are the same size as a full run's. RETENTION: a row set goes when its
// operation row is deleted, and the only thing that does so on request is a
// person discarding the operation (registry.Discard, DeleteOperationV2).
// Nothing prunes operation rows or their row sets by age, so every run's lists
// (5-12 MB each, limited runs included) stay until that operation is discarded.
// A resumed apply reads this operation's journal rows once
// (GetOperationChanges, one operation's rows); there is no per-operation
// iterator, and ScanOperationChanges walks the whole journal, so it is not used.
//
// DURABILITY OF A SPELLING MOVE. A narrator name linked to a record under
// another spelling ("Renee" to "Renée") is respelled inside the Narrator column
// (and NarratorsJSON) so the written book plans clean. The tag still says what
// it said: a rescan with an UNLOCKED narrator field reads the tag's spelling back
// into the column, and the column -> junction sync
// (database.syncNarratorJunctionAfterWrite, narrator_credit_sync.go) resolves
// names by exact name, so it can mint a second, unaccented record for the name
// and re-credit the book to it. This op does not change the scanner or the sync
// (a separate change: todo.d/2026-10-27-narrator-sync-fold-match.md); until
// then the census after a rescan is the check, and re-running this op finds the
// book again.

const creditsBackfillOpID = "maintenance.credits-backfill"

// Opt-in keys accepted in params.allow.
const (
	// cbAllowNewFromSplit lets a narrator fill create records for names that
	// came out of a split credit.
	cbAllowNewFromSplit = "narrator_new_from_split"
	// cbAllowSelfRead lets a column that names only the book's own authors
	// (a self-read, or a mis-tag) become the narrator credit.
	cbAllowSelfRead = "narrator_self_read"
	// cbAllowITunes lets the op write (database only) books with a file under
	// books/itunes/**.
	cbAllowITunes = "itunes"
)

var creditsBackfillAllowKeys = []string{cbAllowNewFromSplit, cbAllowSelfRead, cbAllowITunes}

// Class kinds in the result.
const (
	cbKindWrite         = "write"         // written (apply) or would be written (dry run)
	cbKindReview        = "review"        // left for a person; not written
	cbKindHeld          = "held"          // a guard held the book; not written
	cbKindInformational = "informational" // context on books in other classes
	cbKindError         = "error"         // the book could not be read or written
)

// Class keys. Review classes that mirror a census class use the census key.
const (
	// writes: authors
	cbAuthorFillFromAuthorID    = "author_fill_from_author_id"
	cbAuthorFillFromSnapshot    = "author_fill_from_snapshot"
	cbAuthorIDPrepended         = "author_id_prepended"
	cbAuthorIDFromJoin          = "author_id_from_join"
	cbAuthorPositionsRenumbered = "author_positions_renumbered"
	cbAuthorSnapshotRefreshed   = "author_snapshot_refreshed"
	// writes: narrators
	cbNarratorFillFromColumn        = "narrator_fill_from_column"
	cbNarratorFillFromNarratorsJSON = "narrator_fill_from_narrators_json"
	cbNarratorPositionsRenumbered   = "narrator_positions_renumbered"
	// informational: a spelling move (a name linked to a record under another
	// spelling) respelled the Narrator column or NarratorsJSON in place.
	cbNarratorColumnRespelled = "narrator_column_respelled"
	cbNarratorsJSONRespelled  = "narrators_json_respelled"
	// opt-in (write when allowed, review otherwise)
	cbNarratorSelfRead     = cbAllowSelfRead
	cbNarratorNewFromSplit = cbAllowNewFromSplit
	// review: backfill-specific
	cbAuthorSourceCombined     = "author_source_combined"
	cbAuthorSnapshotUnresolved = "author_snapshot_unresolved"
	cbAuthorIDDuplicateName    = "author_id_duplicate_name_in_join"
	cbAuthorJoinNoAuthorRole   = "author_join_no_author_role"
	cbNarratorsJSONNotPeople   = "narrators_json_not_people"
	cbNarratorsJSONUnjoinable  = "narrators_json_unjoinable"
	// cbNarratorSpellingUnreconcilable and cbNarratorSpellingJSONConflict are
	// this op's own refusals of a spelling move. They used to reuse the census
	// keys narrators_json_disagrees_both and narrators_json_unjoinable, which
	// a dry run could not tell apart from the books the census already counts.
	cbNarratorSpellingUnreconcilable = "narrator_spelling_unreconcilable"
	cbNarratorSpellingJSONConflict   = "narrator_spelling_json_conflict"
	// cbAuthorPlaceholder: the author the write would credit or file under is
	// the placeholder ("Unknown Author").
	cbAuthorPlaceholder = "author_placeholder"
	// cbAuthorIDSnapshotConflict: AuthorID (the flat field a rescan reverts
	// from tags) disagrees with the Book.Author snapshot, which names another
	// member of the join (or, for an empty join, another live author).
	cbAuthorIDSnapshotConflict = "author_id_snapshot_conflict"
	// cbNarratorJunkName: the narrator fill would credit a stand-in
	// ("Unknown", "Various", "Full Cast"...), not a person.
	cbNarratorJunkName = "narrator_junk_name"
	// cbNarratorNameAmbiguous: a narrator name folds (case, diacritics,
	// punctuation) to more than one existing record and matches none exactly.
	cbNarratorNameAmbiguous = "narrator_name_ambiguous"
	// held
	cbHeldOwnerManual     = "held_owner_manual"
	cbHeldITunes          = "held_itunes"
	cbHeldGuardUnreadable = "held_guard_unreadable"
	cbHeldUserLocked      = "held_user_locked"
	cbHeldNarratorLocked  = "held_narrator_user_locked"
	// informational
	cbFilingAuthorChanges        = "filing_author_changes"
	cbNarratorCreatesRecord      = "narrator_creates_record"
	cbNarratorCleanDroppedAuthor = "narrator_clean_dropped_author"
	cbSearchIdentityChanges      = "search_identity_changes"
	cbITunesDatabaseOnly         = "itunes_database_only"
	cbLimitReached               = "limit_reached"
	cbJournalUnverified          = "journal_unverified"
	// errors
	cbReadError        = "read_error"
	cbChangedSincePlan = "changed_since_plan"
	cbWriteFailed      = "write_failed"
)

// cbClassDef is one class as the payload describes it.
type cbClassDef struct {
	Key        string
	Kind       string
	Definition string
}

// creditsBackfillClasses is every class in report order. An opt-in class's
// kind is decided per run (write when allowed, review otherwise).
var creditsBackfillClasses = []cbClassDef{
	{cbAuthorFillFromAuthorID, cbKindWrite,
		"book_authors was empty and Book.AuthorID names a live author: the join becomes [AuthorID] at position 0. " +
			"Fixes census author_id_join_empty."},
	{cbAuthorFillFromSnapshot, cbKindWrite,
		"book_authors was empty, Book.AuthorID unset, and the Book.Author snapshot names a live author by id: the join " +
			"becomes [snapshot] and AuthorID is set to it. No name lookup, no author created."},
	{cbAuthorIDPrepended, cbKindWrite,
		"Book.AuthorID names a live author the join lacks: it is credited at position 0 and the rest follow in their " +
			"order (plan PR 3: 'AuthorID always position 0 when it is missing from the join'). The organizer files a book " +
			"under its lowest-position join row, so these books usually also appear under filing_author_changes. " +
			"Fixes census author_id_not_in_join."},
	{cbAuthorIDFromJoin, cbKindWrite,
		"The join credits authors but Book.AuthorID is unset: AuthorID is set to the first author-role row in " +
			"position order (the row the organizer already files under). Fixes census author_join_no_author_id."},
	{cbAuthorPositionsRenumbered, cbKindWrite,
		"book_authors positions were not 0..n-1 (duplicates such as every row at 0, gaps, negatives): renumbered in " +
			"position order, Book.AuthorID first among tied rows (plan: AuthorID is the tie-breaker). Fixes census " +
			"author_positions_not_normalized and author_positions_all_zero."},
	{cbAuthorSnapshotRefreshed, cbKindWrite,
		"The persisted Book.Author snapshot named another author or an old name: it is refreshed to the primary " +
			"author's current record. Fixes census author_snapshot_stale."},
	{cbNarratorFillFromColumn, cbKindWrite,
		"book_narrators was empty and the Narrator column is a list of people: the junction becomes " +
			"util.CleanNarratorCredit(column, the book's authors), the rule the column->junction sync uses (first row " +
			"'narrator', the rest 'co-narrator'). A name that matches an existing record only by its fold key (case, " +
			"accents, punctuation, spaces: \"Renee\" for \"Renée\", \"Jean Paul\" for \"Jean-Paul\") is linked to " +
			"that record, and only that name inside the column is respelled to the record's spelling in the same write " +
			"(narrator_column_respelled); the rest of the column (a \"Read by\" prefix, a role credit, the book's own " +
			"author) is left as it was, so the book plans clean. Fixes census narrator_column_no_junction."},
	{cbNarratorFillFromNarratorsJSON, cbKindWrite,
		"book_narrators and the Narrator column were empty and NarratorsJSON names narrators: the junction is filled " +
			"from it (each entry one person, the book's own authors dropped) and the column is set to the entries joined " +
			"(\"A and B\"), so the column, the junction and the JSON agree. An entry that links to a record under " +
			"another spelling (fold key: case, accents, punctuation, spaces) is credited under the record's spelling, " +
			"and the matching NarratorsJSON entry is respelled in the same write (narrators_json_respelled). Fixes " +
			"census narrators_json_junction_empty."},
	{cbNarratorColumnRespelled, cbKindInformational,
		"A narrator name linked to an existing record under another spelling (fold key: case, accents, punctuation, " +
			"spaces), so the name inside the Narrator column was replaced with the record's spelling. Only that name " +
			"changes: a \"Read by\" prefix, role credits (\"- translator\") and a co-narrating author stay. The journal " +
			"keeps the old column; the revert restores it. A dry run lists the books an apply would respell."},
	{cbNarratorsJSONRespelled, cbKindInformational,
		"A spelling move also respelled the matching NarratorsJSON entries to the record's spelling in the same " +
			"write, so the JSON still matches the junction and the column. The journal keeps the old JSON; the revert " +
			"restores it."},
	{cbNarratorPositionsRenumbered, cbKindWrite,
		"book_narrators positions were not 0..n-1: renumbered in position order. Fixes census narrator_positions_not_normalized."},
	{cbNarratorSelfRead, cbKindReview,
		"OPT-IN (allow: [\"narrator_self_read\"]). book_narrators is empty and the Narrator column names only the " +
			"book's own authors (util.CleanNarratorCredit verdict all_authors): a self-read, or a mis-tag that copied the " +
			"author into the narrator tag. The two cannot be told apart, so it is written only when the owner allows it; " +
			"then the junction credits the named authors as narrators."},
	{cbNarratorNewFromSplit, cbKindReview,
		"OPT-IN (allow: [\"narrator_new_from_split\"]). The narrator fill needs a NEW narrator record for a name that " +
			"came out of splitting a credit (\"A, B\"). Never created without the owner's allow (same rule as " +
			"repair-combined-author-credits); fills whose names all exist are written without it."},
	{ccAuthorIDDangling, cbKindReview,
		"Book.AuthorID or a book_authors row names an author id that resolves to no record. Author side not written."},
	{ccAuthorIDTombstoned, cbKindReview,
		"Book.AuthorID or a book_authors row names a merged-away author id. Author side not written."},
	{ccAuthorJoinCombined, cbKindReview,
		"book_authors credits a record whose name reads as several people: repair-combined-author-credits owns these. " +
			"Author side not written."},
	{ccAuthorIDNotPrimary, cbKindReview,
		"Book.AuthorID is in the join but not first. The plan does not say which wins; reordering the join changes " +
			"the filing author and resetting AuthorID changes the primary every AuthorID reader shows. Author side not written."},
	{cbAuthorSourceCombined, cbKindReview,
		"The author the backfill would credit (AuthorID or the snapshot) reads as several people (authorcredit.LooksCombined): " +
			"crediting it would add a combined record to the join. Author side not written."},
	{cbAuthorSnapshotUnresolved, cbKindReview,
		"No AuthorID and no join; the Book.Author snapshot carries no id that resolves to a live author. " +
			"The backfill does not look authors up by name."},
	{cbAuthorIDDuplicateName, cbKindReview,
		"Book.AuthorID is missing from the join, but the join credits a different author record with the same " +
			"normalized name: prepending would credit one person twice. Author side not written."},
	{cbAuthorJoinNoAuthorRole, cbKindReview,
		"Book.AuthorID is unset and the join holds no author-role row (only narrators, editors and the like)."},
	{ccNarratorColumnNotPeople, cbKindReview,
		"The Narrator column is set but is not a list of people (junk, or only the book's own authors with a junction " +
			"already present). The sync leaves these alone by design. Narrator side not written."},
	{ccNarratorColumnNotPeopleJunctionDiffers, cbKindReview,
		"The Narrator column is not a list of people and book_narrators names someone else. Narrator side not written."},
	{ccNarratorColumnDiffers, cbKindReview,
		"book_narrators is not empty and differs from the cleaned column: either can be the newer edit (the UI writes " +
			"the junction without the column). Narrator side not written."},
	{ccNarratorJunctionNoColumn, cbKindReview,
		"book_narrators is not empty and the Narrator column is empty: a cleared column or a junction written directly. " +
			"Narrator side not written."},
	{ccNarratorsJSONDisagrees, cbKindReview,
		"NarratorsJSON names narrators that match neither book_narrators nor the column. Narrator side not written."},
	{ccNarratorIDDangling, cbKindReview,
		"A book_narrators row names a narrator id that resolves to no record. Narrator side not written."},
	{ccNarratorJunctionCombined, cbKindReview,
		"book_narrators credits a narrator record named after several people: split-joined-narrators owns these. " +
			"Narrator side not written."},
	{cbNarratorsJSONNotPeople, cbKindReview,
		"The NarratorsJSON fill was refused: an entry is not one person, or every entry is one of the book's authors."},
	{cbAuthorPlaceholder, cbKindReview,
		"The author the write would credit (AuthorID, the snapshot, the join's first author) or file the book under " +
			"is the placeholder (\"Unknown Author\", authorname.IsPlaceholder). Crediting it at position 0 would hide " +
			"the real author behind it, and an AuthorID taken from a placeholder-only join would make the organizer " +
			"treat the book as resolved. Author side not written."},
	{cbAuthorIDSnapshotConflict, cbKindReview,
		"Book.AuthorID is not the author the Book.Author snapshot names. Either the snapshot names a member of the " +
			"join other than AuthorID, and writing AuthorID at position 0 (author_id_prepended) or as the tie-break of " +
			"tied positions it reorders would put it ahead of that author; or the join is empty, the organizer files " +
			"the book under the snapshot, and the snapshot names a live author other than AuthorID, so filling the join " +
			"from AuthorID (author_fill_from_author_id) would flip the filing author and the snapshot refresh would " +
			"overwrite the evidence. A snapshot whose id is 0 or resolves to no record is compared by name (a " +
			"comparison, never a lookup): with the record names of the join members other than AuthorID when the " +
			"join is not empty, with AuthorID's record name when it is; a name that matches is the same conflict. The " +
			"signal is the snapshot only: all-zero positions with no contradicting " +
			"snapshot are renumbered with AuthorID first. A rescan reverts AuthorID from file tags when the author is " +
			"unlocked and never touches the join or the snapshot, so AuthorID is the signal most likely to be the " +
			"stale one. Author side not written, snapshot not refreshed."},
	{cbNarratorJunkName, cbKindReview,
		"The narrator fill would credit a stand-in, not a person (\"Unknown\", \"Various\", \"N/A\", \"Full Cast\", " +
			"\"Narrator\", \"Anonymous\"; an author-junk placeholder; the \"Unknown Author\" placeholder). Never " +
			"created or linked. Narrator side not written."},
	{cbNarratorNameAmbiguous, cbKindReview,
		"A narrator name to credit matches no record exactly but folds (case, diacritics, punctuation) to more than " +
			"one existing record (\"Jose\" with both \"José\" and \"Josè\"). Which one is meant is for a person. " +
			"Narrator side not written."},
	{cbNarratorsJSONUnjoinable, cbKindReview,
		"The NarratorsJSON fill was refused: an entry contains a separator (\",\", \"&\", \"and\"), so the joined column " +
			"would not split back into the same people."},
	{cbNarratorSpellingUnreconcilable, cbKindReview,
		"A narrator name links to an existing record under another spelling (fold key: case, accents, punctuation, " +
			"spaces), but replacing it inside the Narrator column does not read back as the record's names through " +
			"util.CleanNarratorCredit (the name is not found in the column as written, or the record's name carries a " +
			"separator). Writing it would leave the census counting the book. Narrator side not written."},
	{cbNarratorSpellingJSONConflict, cbKindReview,
		"A narrator name links to an existing record under another spelling and the book has NarratorsJSON, but " +
			"respelling the matching JSON entries would still leave it matching neither the junction nor the column. " +
			"Narrator side not written."},
	{cbHeldOwnerManual, cbKindHeld,
		"Doctor Who / Big Finish / Torchwood (path, series, title, credits, transcription or franchise tag): the owner " +
			"applies author/narrator fixes on these by hand. Not written."},
	{cbHeldITunes, cbKindHeld,
		"A file of the book is under books/itunes/**. Not written unless the run allows \"itunes\" (the write is " +
			"database-only; no file, tag or ITL is touched)."},
	{cbHeldGuardUnreadable, cbKindHeld,
		"The guard could not settle where a file of the book lives (fail closed). Not written."},
	{cbHeldUserLocked, cbKindHeld,
		"The book's author field carries a user lock and the plan has an author-side write (any of the author " +
			"write classes: a fill, a prepend, AuthorID from the join, a renumber, a snapshot refresh), whether or not " +
			"it changes the author the book is filed under. The whole author side is held; the narrator side is " +
			"planned on its own. A lock a person sets after the plan (checked again right before the write) holds the " +
			"whole book for this run; a re-run plans it with the lock in place."},
	{cbHeldNarratorLocked, cbKindHeld,
		"The book's narrator field carries a user lock and the plan has a narrator-side write. A person who cleared " +
			"or set the narrator in the UI locks the field (column \"\", junction []); filling it again from the " +
			"column or NarratorsJSON would undo that. The whole narrator side is held. A lock set after the plan " +
			"(checked again right before the write) holds the whole book for this run."},
	{cbFilingAuthorChanges, cbKindInformational,
		"The write changes the author the organizer files the book under (its lowest-position join row, else the " +
			"snapshot, else AuthorID): the next organize may move the book's folder."},
	{cbNarratorCreatesRecord, cbKindInformational,
		"The narrator fill creates at least one narrator record (or would, in a dry run)."},
	{cbNarratorCleanDroppedAuthor, cbKindInformational,
		"util.CleanNarratorCredit dropped one of the book's own authors from the narrator credit (a self-reading " +
			"co-narrator); the junction credits the rest, as the sync does."},
	{cbSearchIdentityChanges, cbKindInformational,
		"The write changes the author the metadata search asks about (AuthorID nil to a value, or to an author of a " +
			"different name): the store marks the book's cached candidates stale and queues it for re-identification " +
			"(database.SearchIdentityChanged, the same predicate the write path uses). Each such book costs provider " +
			"lookups; Google Books allows 1,000 a day. The count is in books_to_reidentify."},
	{cbITunesDatabaseOnly, cbKindInformational,
		"Written (or would be) although a file is under books/itunes/**: the run allowed \"itunes\". Database only."},
	{cbLimitReached, cbKindInformational,
		"Eligible but outside the run's cohort: the limit was reached. The cohort is the first limit eligible book ids " +
			"in sorted order (after planning every book), in a dry run and an apply alike, so a dry run with limit N " +
			"lists exactly the books an apply with limit N writes."},
	{cbJournalUnverified, cbKindInformational,
		"An earlier attempt of this operation journaled this book, but when this attempt started the book did not " +
			"hold the state that row wrote: the run stopped between the journal and the write, or the book was edited " +
			"since. (A write the run itself refused or that failed voids its row and is not listed here.) It is not " +
			"counted as written. If this attempt " +
			"wrote it, it is listed under the write classes too and has a second journal row; reverting the operation " +
			"restores it from the row that matches."},
	{cbReadError, cbKindError, "The book or its credits could not be read or planned. The run is incomplete. In a " +
		"limited cohort the book still takes its place in the sorted order (it may have been eligible), so the next " +
		"eligible book does not take its slot; a re-run with the read healthy writes it."},
	{cbChangedSincePlan, cbKindError,
		"The book changed between the plan and the write (checked under the book's locks); nothing was written. Its " +
			"journal row is voided (undo rows the revert and the resume ignore). The run is incomplete; re-run (or " +
			"resume) to pick it up. A book that fails keeps its place in a limited cohort: the next eligible book does " +
			"not take it."},
	{cbWriteFailed, cbKindError, "The journal row or the write failed. A journal row written for a refused write is " +
		"voided. The run is incomplete."},
}

// creditsBackfillParams are the op's params.
type creditsBackfillParams struct {
	// Apply writes; the default is a dry run that writes nothing.
	Apply bool `json:"apply"`
	// BookIDs restricts the run to these books (a cohort for a first apply).
	BookIDs []string `json:"book_ids,omitempty"`
	// Limit caps the cohort (0 = no cap): every book is planned first, then the
	// first Limit eligible book ids in sorted order are the cohort, in a dry
	// run and an apply alike (earlier attempts' written books count toward it).
	Limit int `json:"limit,omitempty"`
	// Allow opts review classes in: narrator_new_from_split,
	// narrator_self_read, itunes.
	Allow []string `json:"allow,omitempty"`
	// Attempt counts runs of this operation (the checkpoint carries it, so
	// a resumed run knows it is one).
	Attempt int `json:"attempt,omitempty"`
}

// creditsBackfillStore is the op's store surface. creditCensusStore (all
// reads) plus the guard's reads, field locks, narrator creation, the journal
// and the series names. The guard's tag reader comes from its own accessor
// (Plugin.repairsGuardTags), as for every Repairs guard. The credit write itself is ModifyBookCredits,
// resolved separately (database.AsCapability), so a store without it can still
// dry-run.
type creditsBackfillStore interface {
	creditCensusStore
	repairs.GuardReader
	database.MetadataFieldStateReader
	creditsBackfillWriter
	GetAllSeries() ([]database.Series, error)
}

// creditsBackfillWriter is the op's write surface besides ModifyBookCredits.
type creditsBackfillWriter interface {
	CreateNarrator(name string) (*database.Narrator, error)
	CreateOperationChange(change *database.OperationChange) error
	GetOperationChanges(operationID string) ([]*database.OperationChange, error)
}

// narratorByNameReader resolves a narrator name through the store's name
// index (resolved with database.AsCapability; the frozen index answers when
// the store lacks it).
type narratorByNameReader interface {
	GetNarratorByName(name string) (*database.Narrator, error)
}

// cbCreditsView is a book's credits as a sample shows them: names, in order.
type cbCreditsView struct {
	Authors        []string `json:"authors"`
	Narrators      []string `json:"narrators"`
	AuthorID       *int     `json:"author_id,omitempty"`
	AuthorSnapshot string   `json:"author_snapshot,omitempty"`
	Narrator       string   `json:"narrator,omitempty"`
	NarratorsJSON  string   `json:"narrators_json,omitempty"`
}

// cbSample is one book's before/after.
type cbSample struct {
	BookID  string        `json:"book_id"`
	Title   string        `json:"title"`
	Classes []string      `json:"classes"`
	Before  cbCreditsView `json:"before"`
	After   cbCreditsView `json:"after"`
	// NewNarrators names the narrator records the write creates.
	NewNarrators []string `json:"new_narrators,omitempty"`
}

// cbClass is one class in the result.
type cbClass struct {
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	Definition string `json:"definition"`
	Count      int    `json:"count"`
	// BookIDs are the class's book ids, sorted. The scan returns them whole;
	// the stored result leaves them out (the lists live in op rows, see
	// storeCreditsBackfillResult) and a client pages them from
	// GET /operations/:id/class-lists/:class.
	BookIDs []string    `json:"book_ids,omitempty"`
	Samples []*cbSample `json:"samples,omitempty"`
}

// creditsBackfillResult is the op's result payload.
type creditsBackfillResult struct {
	// Mode is "dry_run" or "apply".
	Mode string `json:"mode"`
	// Complete is false when the run was canceled, lost the scan stand-down,
	// or any book could not be read or written.
	Complete bool     `json:"complete"`
	Allowed  []string `json:"allowed"`
	Attempt  int      `json:"attempt"`
	Limit    int      `json:"limit,omitempty"`

	BooksListed  int `json:"books_listed"`
	BooksScanned int `json:"books_scanned"`
	BooksGone    int `json:"books_gone"`
	// BooksEligible is the books the plan wanted to write, before the limit.
	BooksEligible int `json:"books_eligible"`
	// BooksToWrite is the books in at least one write class: would be
	// written (dry run) or written (apply, every attempt of the operation).
	BooksToWrite int `json:"books_to_write"`
	// BooksToReidentify is the books of the cohort whose write changes the
	// search identity (AuthorID nil to a value, or to an author of another
	// name): the store marks their cached candidates stale and queues them for
	// re-identification, which spends provider quota (Google Books: 1,000 a
	// day). The ids are the search_identity_changes class.
	BooksToReidentify int `json:"books_to_reidentify"`
	// BooksWrittenEarlierAttempts is the books an earlier attempt of this
	// operation journaled and wrote: each still held its row's post-image when
	// this attempt started. Journaled books that did not are listed under
	// journal_unverified instead.
	BooksWrittenEarlierAttempts int `json:"books_written_earlier_attempts,omitempty"`
	BooksInReview               int `json:"books_in_review"`
	BooksHeld                   int `json:"books_held"`
	// NarratorsToCreate is the distinct narrator names the run creates (or
	// would); NarratorsCreated the records it did create.
	NarratorsToCreate int      `json:"narrators_to_create"`
	NarratorNames     []string `json:"narrator_names_to_create"`
	NarratorsCreated  int      `json:"narrators_created"`
	// CensusBefore is the credit-census class counts over the books this run
	// read, before it wrote anything.
	CensusBefore map[string]int `json:"census_before"`
	Counts       map[string]int `json:"counts"`
	Classes      []cbClass      `json:"classes"`
	// ClassLists says where the stored result's class id lists are (op rows
	// beside the operation) and how many ids each class has; set on the stored
	// result only.
	ClassLists *database.ClassListSummary `json:"class_lists,omitempty"`
	StartedAt  time.Time                  `json:"started_at"`
	FinishedAt time.Time                  `json:"finished_at"`
	Error      string                     `json:"error,omitempty"`
}

// cbSamplesPerClass bounds the before/after samples kept per write class:
// the smallest book ids, so a re-run shows the same books.
const cbSamplesPerClass = 3

// errCreditsBackfillChanged aborts a write whose book moved since the plan.
var errCreditsBackfillChanged = errors.New("credits backfill: book changed since the plan")

// errCreditsBackfillLocked aborts a write whose book gained a user lock on a
// side the plan writes since the plan (a person locked the field while the run
// was between its read pass and this write).
var errCreditsBackfillLocked = errors.New("credits backfill: a field the plan writes was locked since the plan")

func (p *Plugin) creditsBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          creditsBackfillOpID,
		Preview:     sdk.PreviewSpec{Params: creditsBackfillParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Author/narrator credits backfill",
		Description: "DRY RUN unless {\"apply\": true}. Fills and repairs the book_authors / book_narrators credit lists " +
			"from the flat fields so every list reader sees the same credits: an empty join gets Book.AuthorID (or the " +
			"Book.Author snapshot), a missing AuthorID is put first, positions are renumbered with AuthorID as the " +
			"tie-breaker, an empty narrator junction gets util.CleanNarratorCredit(Narrator) (or NarratorsJSON), and a " +
			"missing AuthorID or stale snapshot is set from the join. Never creates an author; creates a narrator from a " +
			"split credit only with allow:[\"narrator_new_from_split\"]. Dangling, tombstoned and combined ids, non-people " +
			"narrator credits and junction/column disagreements are listed for review, not written. Doctor Who / Big " +
			"Finish / Torchwood books are held; iTunes books are held unless allow:[\"itunes\"]. Every write is journaled " +
			"(pre-image of both lists and the four flat fields) and undone by the operation's revert. book_ids and limit " +
			"apply a cohort first (the first limit eligible ids in sorted order, dry run and apply alike). The result " +
			"(GET /operations/:id/result) carries every class's count and before/after samples, and each class's book ids " +
			"are paged from GET /operations/:id/class-lists/:class; books_to_reidentify counts the " +
			"books whose AuthorID change makes the store re-identify them (provider quota). Declares a books write, so " +
			"the dispatcher runs it alone against other book-writing operations (scan, organize, metadata apply), a dry " +
			"run included (the registry declares writes per operation, not per mode); a full apply can hold that gate " +
			"for hours.",
		// ResumeRestart: every item is idempotent (a written book plans clean),
		// so a restart re-runs the pass and writes only what is left; the
		// written lists of earlier attempts come back from the journal.
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		// Shares the census's key: a census running while the backfill writes
		// would count a half-written library.
		ConcurrencyKey: "maintenance.credit-census",
		Reads:          []sdk.Resource{sdk.ResBooks, sdk.ResAuthors},
		// Writes is declared for the op, not per mode: the registry has no
		// per-mode resource set. A DRY RUN therefore also holds the books write
		// slot, and while a preview runs (a full library read pass) the
		// dispatcher keeps the other book-writing operations (scan, organize,
		// metadata apply) waiting, although the preview writes nothing. Only an
		// apply takes the scan stand-down.
		Writes:      []sdk.Resource{sdk.ResBooks},
		Cancellable: true,
		Isolate:     false,
		// A full apply rewrites ~55k book rows (one synced batch each); the
		// read pass alone is the census's cost. Generous, and a timeout loses
		// nothing: the re-run is idempotent.
		Timeout:      12 * time.Hour,
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:          p.runCreditsBackfill,
	}
}

func (p *Plugin) runCreditsBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params creditsBackfillParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	ops := p.deps.OpsStore()
	if ops == nil {
		return fmt.Errorf("database not initialized")
	}
	// The class id lists are stored beside the operation, not in its result
	// (storeCreditsBackfillResult), so the store must be able to keep them:
	// found out here, before an apply writes a book, not after hours of it.
	rows, ok := database.AsCapability[database.OpRowStore](p.deps.OperationQueueStore())
	if !ok {
		return fmt.Errorf("credits backfill: the operation store %T cannot keep the class id lists (database.OpRowStore)",
			p.deps.OperationQueueStore())
	}
	var store creditsBackfillStore = ops
	var mod database.BookCreditsModifier
	if params.Apply {
		m, ok := database.AsCapability[database.BookCreditsModifier](ops)
		if !ok {
			return fmt.Errorf("cannot apply: store is %T, which cannot write a book's credits and row together "+
				"(ModifyBookCredits), and no store in its decorator chain can", ops)
		}
		mod = m
	}
	env := creditsBackfillEnv{
		store:   store,
		mod:     mod,
		tags:    p.repairsGuardTags(),
		scan:    p.deps,
		opID:    registry.ReporterOpID(reporter),
		workers: runtime.NumCPU(),
	}
	res, err := runCreditsBackfillScan(ctx, env, params, reporter)
	if res != nil {
		if params.Apply && res.Counts != nil && res.BooksToWrite > 0 {
			p.deps.InvalidateAuthorsCache()
		}
		if serr := storeCreditsBackfillResult(reporter, rows, env.opID, res); serr != nil {
			if err == nil {
				return fmt.Errorf("store credits backfill result: %w", serr)
			}
			_ = reporter.Log(slog.LevelError, "credits-backfill: result not stored", slog.String("error", serr.Error()))
		}
	}
	return err
}

// storeCreditsBackfillResult stores the run's result: the class id lists as op
// rows under the operation's id (database.SaveClassLists), then the summary (the
// result without the lists, carrying their ClassListSummary) as the operation's
// result. The rows go first, so a stored result that names the row store has all
// of its rows; a summary that fails to store takes the rows back.
func storeCreditsBackfillResult(reporter sdk.Reporter, rows database.OpRowStore, opID string, res *creditsBackfillResult) error {
	if rows == nil || opID == "" {
		return errors.New("no operation id or row store to keep the class id lists under")
	}
	lists := make(map[string][]string, len(res.Classes))
	for _, c := range res.Classes {
		lists[c.Key] = c.BookIDs
	}
	sum, err := database.SaveClassLists(rows, opID, lists)
	if err != nil {
		return err
	}
	if err := registry.ReporterSetResult(reporter, res.summary(sum)); err != nil {
		if cerr := database.ClearClassLists(rows, opID); cerr != nil {
			_ = reporter.Log(slog.LevelWarn, "credits-backfill: class lists not cleared after a failed result write",
				slog.String("error", cerr.Error()))
		}
		return err
	}
	return nil
}

// summary is res as the operation stores it: the classes without their id
// lists (counts and samples stay), and the summary of where the lists are.
func (res *creditsBackfillResult) summary(lists *database.ClassListSummary) *creditsBackfillResult {
	out := *res
	out.Classes = make([]cbClass, len(res.Classes))
	for i, c := range res.Classes {
		c.BookIDs = nil
		out.Classes[i] = c
	}
	out.ClassLists = lists
	return &out
}

// creditsBackfillEnv is what one run needs besides its params.
type creditsBackfillEnv struct {
	store creditsBackfillStore
	// mod writes; nil in a dry run.
	mod database.BookCreditsModifier
	// tags is the guard's franchise-tag reader (nil: the tag check is
	// skipped, as repairs.GuardOwnerManualBooks documents).
	tags repairs.GuardTagReader
	// scan is the stand-down controller (nil: no interlock, as in tests).
	scan ScanController
	// opID is this operation's id ("" when the reporter cannot say: then no
	// journal can be written, so an apply refuses).
	opID    string
	workers int
}

// cbAccumulator collects book ids and samples per class across workers.
type cbAccumulator struct {
	mu       sync.Mutex
	byClass  map[string]map[string]bool
	samples  map[string][]*cbSample
	census   map[string]int
	newNames map[string]string // fold key -> first spelling seen
	// createdIDs are the narrator records CreateNarrator returned for names
	// the plan did not find: two books naming one new person share one.
	createdIDs map[int]bool
	// writtenCreatedIDs are the created records a SUCCESSFUL book write
	// credits (narrators_created counts these, not every create call).
	writtenCreatedIDs map[int]bool
}

func newCBAccumulator() *cbAccumulator {
	return &cbAccumulator{
		byClass:    map[string]map[string]bool{},
		samples:    map[string][]*cbSample{},
		census:     map[string]int{},
		newNames:   map[string]string{},
		createdIDs: map[int]bool{},

		writtenCreatedIDs: map[int]bool{},
	}
}

// markCreatedWritten records that a successful write credits these created
// narrator records.
func (a *cbAccumulator) markCreatedWritten(ids []int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		a.writtenCreatedIDs[id] = true
	}
}

func (a *cbAccumulator) add(bookID string, classes ...string) {
	if len(classes) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range classes {
		set := a.byClass[c]
		if set == nil {
			set = map[string]bool{}
			a.byClass[c] = set
		}
		set[bookID] = true
	}
}

func (a *cbAccumulator) addCensus(classes []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range classes {
		a.census[c]++
	}
}

// claimCreated records the narrator records CreateNarrator returned for one
// book and returns the ones this run had not seen before. CreateNarrator is
// resolve-or-create, so when several workers plan the same new name, each gets
// the one record back; only the first to claim it is told it created it, and
// only that book's journal row names it in CreatedNarrators.
func (a *cbAccumulator) claimCreated(created []database.Narrator) []database.Narrator {
	a.mu.Lock()
	defer a.mu.Unlock()
	var claimed []database.Narrator
	for _, n := range created {
		if a.createdIDs[n.ID] {
			continue
		}
		a.createdIDs[n.ID] = true
		claimed = append(claimed, n)
	}
	return claimed
}

func (a *cbAccumulator) addNewNames(names []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, n := range names {
		k := authorjunk.FoldKey(n)
		if _, ok := a.newNames[k]; !ok {
			a.newNames[k] = n
		}
	}
}

// sample keeps s for each of its write classes when its book id is among the
// cbSamplesPerClass smallest seen for that class.
func (a *cbAccumulator) sample(s *cbSample, writeClasses []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range writeClasses {
		list := append(a.samples[c], s)
		sort.Slice(list, func(i, j int) bool { return list[i].BookID < list[j].BookID })
		if len(list) > cbSamplesPerClass {
			list = list[:cbSamplesPerClass]
		}
		a.samples[c] = list
	}
}

// cbRun is one run's shared state.
type cbRun struct {
	env     creditsBackfillEnv
	params  creditsBackfillParams
	allow   map[string]bool
	idx     *creditCensusIndex
	narrIdx map[string]database.Narrator // normalized name -> record (frozen)
	// foldIdx is every record by fold key (authorjunk.FoldKey), ids ascending
	// (frozen): a name that misses narrIdx may still be one record's spelling
	// without its diacritics.
	foldIdx map[string][]database.Narrator
	byName  narratorByNameReader // nil: frozen index only
	// createMu serialises narrator creation: the fold map of records this run
	// created (createdFold) is checked, and CreateNarrator called, under it,
	// so "Jose" and "José" planned by two workers become one record.
	createMu    sync.Mutex
	createdFold map[string]database.Narrator
	series      repairs.SeriesNamer
	resolver    *repairs.PathResolver
	acc         *cbAccumulator

	// written counts this attempt's writes.
	written atomic.Int64
	// plansMu guards plans: the books phase 1 planned with something to
	// write, by id. Phase 2 (cohort) and phase 3 (apply) read it after phase 1
	// has finished.
	plansMu sync.Mutex
	plans   map[string]*cbPlanned
	// unplanned are the books phase 1 could not read or plan (by id), also
	// under plansMu. Whether they had anything to write is unknown, so each
	// holds its place in a limit cohort (selectCohort).
	unplanned map[string]bool
	// priorRun is the books earlier attempts of this operation wrote whose
	// post-image the book still held when this attempt started (verified
	// against the journal); it counts toward the limit. priorWritten holds
	// their write classes, priorUnverified the journaled books that did not
	// hold their post-image. Both are read before the items run and not
	// written after, so workers may read them without a lock.
	priorRun        int64
	priorWritten    map[string][]string
	priorUnverified map[string]bool

	scanned, gone, failed, planned atomic.Int64

	firstErrMu sync.Mutex
	firstErr   error
}

func (r *cbRun) noteErr(err error) {
	r.firstErrMu.Lock()
	if r.firstErr == nil {
		r.firstErr = err
	}
	r.firstErrMu.Unlock()
}

// runCreditsBackfillScan does the work. It returns a result whenever it got
// as far as listing books, including on cancel or failure (Complete=false).
func runCreditsBackfillScan(ctx context.Context, env creditsBackfillEnv, params creditsBackfillParams, reporter sdk.Reporter) (*creditsBackfillResult, error) {
	started := time.Now()
	allow := map[string]bool{}
	for _, k := range params.Allow {
		k = strings.TrimSpace(k)
		if !slices.Contains(creditsBackfillAllowKeys, k) {
			return nil, fmt.Errorf("unknown allow key %q (known: %s)", k, strings.Join(creditsBackfillAllowKeys, ", "))
		}
		allow[k] = true
	}
	if params.Limit < 0 {
		return nil, fmt.Errorf("limit must be >= 0")
	}
	if params.Apply {
		if env.mod == nil {
			return nil, fmt.Errorf("apply needs ModifyBookCredits")
		}
		if env.opID == "" {
			return nil, fmt.Errorf("apply needs an operation id to journal its writes under, and the reporter has none")
		}
	}
	params.Attempt++
	// The checkpoint carries every param: on ResumeRestart it is merged back
	// into the params, so a resumed apply stays an apply with the same cohort,
	// limit and allows, and knows it is a later attempt.
	_ = reporter.Checkpoint(params)

	mode := "dry_run"
	if params.Apply {
		mode = "apply"
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("credits-backfill: start (%s, attempt %d)", mode, params.Attempt))
	_ = reporter.UpdateProgress(0, 1, "Indexing authors and narrators…")

	// The scan stand-down is held for the whole apply, reading and planning
	// included: the plan is made from rows a running scan could rewrite, and a
	// full-library phase 1 outlasts the registry's 5-minute lease. The hold
	// renews the lease on every item (registry.RunItems beats a hold its
	// context carries) and on every progress stamp; work ctx and work reporter
	// below are the hold's, so a lapsed lease cancels the run with
	// registry.ErrScanStandDownLost. A dry run writes nothing and holds
	// nothing.
	//
	// The hold comes BEFORE the author and narrator index is built: the index
	// is a frozen copy of every record, and one built before the hold could be
	// stale by the time the scan is down (a scan that was still running
	// creates authors and narrators).
	workCtx, workRep := ctx, reporter
	var hold *registry.ScanStandDownHold
	if params.Apply {
		h, sdErr := registry.HoldScanStandDown(ctx, env.scan, reporter, "credits-backfill apply")
		if sdErr != nil {
			return nil, sdErr
		}
		hold = h
		defer hold.Release()
		workCtx, workRep = hold.Context(), hold.Reporter()
	}

	idx, err := buildCreditCensusIndex(env.store)
	if err != nil {
		return nil, err
	}
	allSeries, err := env.store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	run := &cbRun{
		env:      env,
		params:   params,
		allow:    allow,
		idx:      idx,
		narrIdx:  make(map[string]database.Narrator, len(idx.narrators)),
		series:   repairs.SeriesNamesFrom(allSeries),
		resolver: repairs.NewPathResolver(),
		acc:      newCBAccumulator(),
		plans:    map[string]*cbPlanned{},

		foldIdx:     map[string][]database.Narrator{},
		createdFold: map[string]database.Narrator{},
	}
	// Ids in ascending order: a name shared by two records resolves to the
	// lower id, deterministically. The store's name index (byName) wins when
	// the store has one.
	nids := make([]int, 0, len(idx.narrators))
	for id := range idx.narrators {
		nids = append(nids, id)
	}
	sort.Ints(nids)
	for _, id := range nids {
		n := idx.narrators[id]
		if k := util.NormalizeAuthor(strings.TrimSpace(n.Name)); k != "" {
			if _, dup := run.narrIdx[k]; !dup {
				run.narrIdx[k] = n
			}
		}
		if k := authorjunk.FoldKey(n.Name); k != "" {
			run.foldIdx[k] = append(run.foldIdx[k], n)
		}
	}
	if r, ok := database.AsCapability[narratorByNameReader](env.store); ok {
		run.byName = r
	}

	var ids []string
	if len(params.BookIDs) > 0 {
		ids = slices.Clone(params.BookIDs)
	} else {
		ids, err = env.store.ListBookIDs()
		if err != nil {
			return nil, fmt.Errorf("list book ids: %w", err)
		}
	}
	// Sorted and de-duplicated: one item per book, so no two workers ever
	// plan or write the same book (the disjoint partition).
	sort.Strings(ids)
	ids = slices.Compact(ids)

	if params.Apply {
		if perr := run.verifyPriorAttempts(workCtx); perr != nil {
			return nil, perr
		}
	}

	runCtx, cancel := context.WithCancelCause(workCtx)
	defer cancel(nil)

	workers := max(env.workers, 1)
	// PHASE 1: plan and guard every book, concurrently and without writing.
	// Planning everything first is what makes the limit's cohort the same in a
	// dry run and an apply: the first N ELIGIBLE ids in sorted order, not the
	// first N ids that happened to be eligible while a worker pool raced.
	//
	// ErrModeFail: no item returns an error of its own (a book that cannot be
	// planned or written is recorded in the result and counted by r.failed), so
	// the only errors RunItems sees are a cancel and a lost stand-down. The
	// first one stops the pass; ErrModeCollect would run every remaining item
	// into the same error and join thousands of copies of it.
	runErr := registry.RunItems(runCtx, workRep, ids, func(_ context.Context, id string) error {
		run.planBook(id)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: workers,
		ErrMode:     registry.ErrModeFail,
		// Label runs inside each worker goroutine: it reads atomics only.
		Label: func(i, total int) string {
			return fmt.Sprintf("Credits backfill: planning %d/%d (errors %d)", i+1, total, run.failed.Load())
		},
	})

	// PHASE 2: the cohort. A canceled planning pass has not seen every book,
	// so an apply stops there; a dry run reports what it saw.
	eligible := run.planIDs()
	var selected []string
	if runErr == nil || !params.Apply {
		selected = run.selectCohort(eligible)
	}

	// PHASE 3: apply the cohort (a dry run already recorded it in phase 2).
	if params.Apply && runErr == nil && len(selected) > 0 {
		runErr = registry.RunItems(runCtx, workRep, selected, func(itemCtx context.Context, id string) error {
			// Beat the lease and stop before this book's write once it is
			// lost: a lapsed lease means the scanner may be writing again.
			// (RunItems beats a ctx-carried hold before each item too; this
			// stays so the guarantee does not depend on that.)
			if err := registry.ScanStandDownCheckpoint(itemCtx); err != nil {
				return err
			}
			run.apply(run.plans[id])
			return nil
		}, registry.RunItemsOptions{
			Concurrency: workers,
			ErrMode:     registry.ErrModeFail, // see phase 1
			Label: func(i, total int) string {
				return fmt.Sprintf("Credits backfill: writing %d/%d (written %d, errors %d)",
					i+1, total, run.written.Load(), run.failed.Load())
			},
		})
	}
	// A lost stand-down is the real reason the run stopped, whatever error the
	// pool reported on the way out (usually a bare "context canceled", the
	// hold's cancel seen by a worker that had not yet beaten the lease).
	if hold != nil && hold.Lost() {
		runErr = registry.ErrScanStandDownLost
	}

	earlier := 0
	if params.Apply {
		earlier = run.mergeJournal()
	}
	res := run.result(ids, started)
	res.BooksWrittenEarlierAttempts = earlier
	res.BooksEligible = len(eligible)
	res.BooksToReidentify = res.Counts[cbSearchIdentityChanges]

	var outErr error
	switch {
	case runErr != nil:
		outErr = fmt.Errorf("credits backfill stopped: %w", runErr)
	case run.failed.Load() > 0:
		run.firstErrMu.Lock()
		first := run.firstErr
		run.firstErrMu.Unlock()
		outErr = fmt.Errorf("credits backfill incomplete: %d book(s) failed, first: %w", run.failed.Load(), first)
	}
	res.Complete = outErr == nil
	if outErr != nil {
		res.Error = outErr.Error()
	}
	res.FinishedAt = time.Now()

	summary := fmt.Sprintf("credits-backfill (%s, complete=%t): listed=%d scanned=%d to_write=%d review=%d held=%d "+
		"narrators_to_create=%d created=%d counts=%v",
		res.Mode, res.Complete, res.BooksListed, res.BooksScanned, res.BooksToWrite, res.BooksInReview, res.BooksHeld,
		res.NarratorsToCreate, res.NarratorsCreated, res.Counts)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(ids), len(ids), summary)
	return res, outErr
}

// cbJournalBook is what this operation's journal holds for one book: the
// write classes of every row, and each row's post-image.
type cbJournalBook struct {
	classes []string
	afters  []undo.CreditsBackfillState
}

// creditsBackfillJournalBooks reads this operation's credits_backfill rows by
// book. A row that does not decode is skipped (its revert refuses it too).
func creditsBackfillJournalBooks(store creditsBackfillWriter, opID string) (map[string]*cbJournalBook, error) {
	out := map[string]*cbJournalBook{}
	if opID == "" {
		return out, nil
	}
	rows, err := store.GetOperationChanges(opID)
	if err != nil {
		return nil, fmt.Errorf("read this operation's journal: %w", err)
	}
	for _, c := range rows {
		// A voided row is a write that was refused and never made.
		if c == nil || c.Voided || c.ChangeType != undo.ChangeTypeCreditsBackfill || c.BookID == "" {
			continue
		}
		_, w, derr := undo.DecodeCreditsBackfill(c)
		if derr != nil {
			continue
		}
		jb := out[c.BookID]
		if jb == nil {
			jb = &cbJournalBook{}
			out[c.BookID] = jb
		}
		jb.classes = append(jb.classes, w.Classes...)
		jb.afters = append(jb.afters, w.After)
	}
	return out, nil
}

// verifyPriorAttempts reads this operation's journal before a resumed apply
// and checks each journaled book against its rows: a book that still holds
// a row's post-image was written; one that holds none of them was not (a
// refused write, a stop between the journal and the write) or was edited
// since, and is not counted. Only verified books count toward the limit and
// as written by earlier attempts. Reads run on the run's worker count; a
// book that cannot be read fails the attempt rather than guessing.
func (r *cbRun) verifyPriorAttempts(ctx context.Context) error {
	journal, err := creditsBackfillJournalBooks(r.env.store, r.env.opID)
	if err != nil {
		return err
	}
	r.priorWritten = map[string][]string{}
	r.priorUnverified = map[string]bool{}
	if len(journal) == 0 {
		return nil
	}
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(r.env.workers, 1))
	for _, bookID := range slices.Sorted(maps.Keys(journal)) {
		jb := journal[bookID]
		g.Go(func() error {
			// Beats the stand-down lease the caller's ctx carries (a resumed
			// apply verifies every journaled book before phase 1 starts, and
			// that is a full-library read when the earlier attempt got far)
			// and stops at a cancel or a lost lease.
			if err := registry.ScanStandDownCheckpoint(gctx); err != nil {
				return err
			}
			holds, err := r.holdsAnyPostImage(bookID, jb.afters)
			if err != nil {
				return fmt.Errorf("verify earlier attempt's write of book %s: %w", bookID, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if holds {
				r.priorWritten[bookID] = jb.classes
			} else {
				r.priorUnverified[bookID] = true
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	r.priorRun = int64(len(r.priorWritten))
	return nil
}

// holdsAnyPostImage reports whether the book currently holds one of afters.
// A book that no longer exists holds none.
func (r *cbRun) holdsAnyPostImage(bookID string, afters []undo.CreditsBackfillState) (bool, error) {
	book, err := r.env.store.GetBookByID(bookID)
	if err != nil {
		return false, err
	}
	if book == nil {
		return false, nil
	}
	join, err := r.env.store.GetBookAuthors(bookID)
	if err != nil {
		return false, err
	}
	junction, err := r.env.store.GetBookNarrators(bookID)
	if err != nil {
		return false, err
	}
	cur := undo.CreditsBackfillStateOf(book, join, junction)
	for _, a := range afters {
		if undo.SameCreditsBackfillState(cur, a) {
			return true, nil
		}
	}
	return false, nil
}

// mergeJournal adds the books earlier attempts of this operation wrote
// (verified at the start of this attempt) to the write classes, so the result
// covers the whole operation, and returns how many of them this attempt did
// not write itself. Journaled books that did not hold their post-image are
// listed under journal_unverified.
func (r *cbRun) mergeJournal() int {
	r.acc.mu.Lock()
	thisAttempt := func(bookID string, classes []string) bool {
		for _, c := range classes {
			if r.acc.byClass[c][bookID] {
				return true
			}
		}
		return false
	}
	type pending struct {
		id      string
		classes []string
	}
	var adds []pending
	earlier := 0
	for bookID, classes := range r.priorWritten {
		if !thisAttempt(bookID, classes) {
			earlier++
		}
		adds = append(adds, pending{bookID, classes})
	}
	r.acc.mu.Unlock()
	for _, a := range adds {
		r.acc.add(a.id, a.classes...)
	}
	for bookID := range r.priorUnverified {
		r.acc.add(bookID, cbJournalUnverified)
	}
	return earlier
}

// result builds the payload from the accumulator.
func (r *cbRun) result(ids []string, started time.Time) *creditsBackfillResult {
	mode := "dry_run"
	if r.params.Apply {
		mode = "apply"
	}
	res := &creditsBackfillResult{
		Mode:         mode,
		Allowed:      cbSortedAllow(r.allow),
		Attempt:      r.params.Attempt,
		Limit:        r.params.Limit,
		BooksListed:  len(ids),
		BooksScanned: int(r.scanned.Load()),
		BooksGone:    int(r.gone.Load()),
		Counts:       map[string]int{},
		StartedAt:    started,
	}
	r.acc.mu.Lock()
	defer r.acc.mu.Unlock()
	res.CensusBefore = make(map[string]int, len(r.acc.census))
	for k, v := range r.acc.census {
		res.CensusBefore[k] = v
	}
	for _, n := range r.acc.newNames {
		res.NarratorNames = append(res.NarratorNames, n)
	}
	sort.Strings(res.NarratorNames)
	res.NarratorsToCreate = len(res.NarratorNames)
	res.NarratorsCreated = len(r.acc.writtenCreatedIDs)
	toWrite, review, held := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, def := range creditsBackfillClasses {
		kind := r.kindOf(def)
		set := r.acc.byClass[def.Key]
		bookIDs := make([]string, 0, len(set))
		for id := range set {
			bookIDs = append(bookIDs, id)
		}
		sort.Strings(bookIDs)
		res.Classes = append(res.Classes, cbClass{
			Key: def.Key, Kind: kind, Definition: def.Definition,
			Count: len(bookIDs), BookIDs: bookIDs, Samples: r.acc.samples[def.Key],
		})
		res.Counts[def.Key] = len(bookIDs)
		for _, id := range bookIDs {
			switch kind {
			case cbKindWrite:
				toWrite[id] = true
			case cbKindReview:
				review[id] = true
			case cbKindHeld:
				held[id] = true
			}
		}
	}
	res.BooksToWrite, res.BooksInReview, res.BooksHeld = len(toWrite), len(review), len(held)
	return res
}

// kindOf is def's kind in this run: an opt-in class is a write class when the
// run allows it.
func (r *cbRun) kindOf(def cbClassDef) string {
	if (def.Key == cbNarratorSelfRead || def.Key == cbNarratorNewFromSplit) && r.allow[def.Key] {
		return cbKindWrite
	}
	return def.Kind
}

func cbSortedAllow(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// cbNarratorSlot is one planned narrator credit: an existing record (ID > 0)
// or a name the write creates.
type cbNarratorSlot struct {
	Name string
	ID   int
}

// cbPlan is what the backfill would do to one book.
type cbPlan struct {
	writes  []string
	reviews []string
	held    []string
	infos   []string

	authorWrite bool
	authors     []database.BookAuthor // full new join (normalised by the write)
	authorID    *int
	snapshot    *database.Author
	snapChanged bool

	narratorWrite bool
	narrators     []cbNarratorSlot
	narratorRows  []database.BookNarrator // set when narrators are rows already (renumber)
	column        *string
	columnChanged bool
	// newNames are the narrator names the write creates.
	newNames []string
	// json is the NarratorsJSON the write leaves when a spelling move
	// respelled its entries (jsonChanged).
	json        *string
	jsonChanged bool
	// authorNames and jsonNames are what the narrator side planned against
	// (the book's live authors after the write; NarratorsJSON's names). The
	// write re-derives the column and NarratorsJSON from them when a record's
	// spelling turns out to differ from the name the plan asked for
	// (settleNarratorSpelling).
	authorNames []string
	jsonNames   []string
	// requested are the names the plan asked for, aligned with narrators and
	// never changed afterwards: a slot whose Name no longer matches its
	// requested name (normalized) is a spelling move. columnText is the
	// Narrator column exactly as stored when the fill came from it (the text a
	// spelling move edits in place); empty when the column is built from
	// NarratorsJSON. selfRead: the fill is the opt-in self-read. rawJSON is
	// NarratorsJSON as stored.
	requested  []string
	columnText string
	selfRead   bool
	rawJSON    *string

	filingChanges bool
}

func (pl *cbPlan) hasWrite() bool { return pl.authorWrite || pl.narratorWrite }

// cbPlanned is one book's plan, kept from phase 1 to the cohort and the
// write: the pre-image as read (the write re-checks it under the book's
// stripes), the plan, and the facts the result needs.
type cbPlanned struct {
	id     string
	title  string
	before undo.CreditsBackfillState
	plan   *cbPlan
}

// planBook is phase 1 for one book: read it, classify it, plan it, run the
// hands-off checks, and file the book under its review/held classes. A book
// with something to write is kept in r.plans for the cohort; nothing is
// written here, in either mode.
func (r *cbRun) planBook(id string) {
	book, err := r.env.store.GetBookByID(id)
	if err != nil {
		r.failPlan(id, fmt.Errorf("book %s: read book: %w", id, err))
		return
	}
	if book == nil {
		r.gone.Add(1)
		return
	}
	join, err := r.env.store.GetBookAuthors(id)
	if err != nil {
		r.failPlan(id, fmt.Errorf("book %s: read book_authors: %w", id, err))
		return
	}
	junction, err := r.env.store.GetBookNarrators(id)
	if err != nil {
		r.failPlan(id, fmt.Errorf("book %s: read book_narrators: %w", id, err))
		return
	}
	census, err := classifyCreditBook(r.idx, book, join, junction)
	if err != nil {
		r.failPlan(id, fmt.Errorf("book %s: classify: %w", id, err))
		return
	}
	r.scanned.Add(1)
	r.acc.addCensus(census)

	plan, err := planCreditsBackfill(r.idx, r.lookupNarrator, r.allow, book, join, junction, census, "", "")
	if err != nil {
		r.failPlan(id, fmt.Errorf("book %s: plan: %w", id, err))
		return
	}
	if plan.hasWrite() {
		g, err := r.guard(book, plan)
		if err != nil {
			r.failPlan(id, fmt.Errorf("book %s: guard: %w", id, err))
			return
		}
		switch {
		case g.holdAll != "":
			plan.holdAll(g.holdAll)
		case g.itunes && !r.allow[cbAllowITunes]:
			plan.holdAll(cbHeldITunes)
		default:
			if g.authorLocked || g.narratorLocked {
				// Re-plan with the locked side held: the other side is planned
				// on its own (the narrator credit is cleaned against the
				// authors the book keeps).
				authorHold, narratorHold := "", ""
				if g.authorLocked {
					authorHold = cbHeldUserLocked
				}
				if g.narratorLocked {
					narratorHold = cbHeldNarratorLocked
				}
				if plan, err = planCreditsBackfill(r.idx, r.lookupNarrator, r.allow, book, join, junction, census, authorHold, narratorHold); err != nil {
					r.failPlan(id, fmt.Errorf("book %s: plan: %w", id, err))
					return
				}
			}
			if g.itunes && plan.hasWrite() {
				plan.infos = append(plan.infos, cbITunesDatabaseOnly)
			}
		}
	}
	r.acc.add(id, plan.reviews...)
	r.acc.add(id, plan.held...)
	if !plan.hasWrite() {
		return
	}
	if plan.authorWrite && !sameAuthorIDPtr(book.AuthorID, plan.authorID) &&
		database.SearchIdentityChanged(r.env.store.GetAuthorByID,
			&database.Book{Title: book.Title, AuthorID: book.AuthorID},
			&database.Book{Title: book.Title, AuthorID: plan.authorID}) {
		plan.infos = append(plan.infos, cbSearchIdentityChanges)
	}
	r.plansMu.Lock()
	r.plans[id] = &cbPlanned{id: id, title: book.Title, before: undo.CreditsBackfillStateOf(book, join, junction), plan: plan}
	r.plansMu.Unlock()
}

// planIDs is the ids with something to write, sorted. Phase 1 has finished,
// so nothing writes r.plans any more.
func (r *cbRun) planIDs() []string {
	r.plansMu.Lock()
	defer r.plansMu.Unlock()
	return slices.Sorted(maps.Keys(r.plans))
}

// selectCohort is phase 2: the first Limit - (books earlier attempts of this
// operation already wrote) eligible ids in sorted order; every other eligible
// id is filed under limit_reached. In a dry run the cohort is recorded as
// would-be writes here (an apply records each book as its write succeeds), so
// a dry run with a limit lists exactly the books an apply with that limit
// writes. A book in the cohort whose write later fails keeps its place: the
// cohort is decided before any write, so it does not depend on write order.
// So does a book phase 1 could not read or plan (r.unplanned): it holds its
// place in the sorted order, unwritten.
func (r *cbRun) selectCohort(eligible []string) []string {
	selected := eligible
	if r.params.Limit > 0 {
		room := max(r.params.Limit-int(r.priorRun), 0)
		// A book phase 1 could not read or plan takes its place in the sorted
		// order too: it may well have been eligible, so letting the next
		// eligible book take its slot would make the cohort depend on a
		// transient read error. It is not written (it has no plan); the
		// re-run, with the read healthy, finds it with room left for it.
		r.plansMu.Lock()
		unplanned := slices.Sorted(maps.Keys(r.unplanned))
		r.plansMu.Unlock()
		candidates := slices.Clone(eligible)
		candidates = append(candidates, unplanned...)
		sort.Strings(candidates)
		if len(candidates) > room {
			inCohort := make(map[string]bool, room)
			for _, id := range candidates[:room] {
				inCohort[id] = true
			}
			selected = make([]string, 0, room)
			for _, id := range eligible {
				if inCohort[id] {
					selected = append(selected, id)
				} else {
					r.acc.add(id, cbLimitReached)
				}
			}
		}
	}
	selected = r.settleNewSpellings(eligible, selected)
	if r.params.Apply {
		return selected
	}
	for _, id := range selected {
		pb := r.plans[id]
		r.planned.Add(1)
		r.acc.add(id, pb.plan.writes...)
		r.acc.add(id, pb.plan.infos...)
		r.acc.addNewNames(pb.plan.newNames)
		r.acc.sample(r.sampleOf(pb), pb.plan.writes)
	}
	return selected
}

// settleNewSpellings is the second half of phase 2: when books name one NEW
// narrator under spellings of one fold key ("Renee Montoya", "Renée Montoya"),
// the record the run creates takes the most frequent spelling among those
// books (ties broken by the lexically smallest), and every book whose own
// spelling differs has that name respelled in its column and NarratorsJSON
// (settleNarratorSpelling), exactly as for a name linked to an existing record.
// Before this the record took whichever spelling the first worker to reach it
// happened to carry, so a dry run could not say which, and two runs over one
// library could disagree. It runs serially, after the cohort is chosen and
// before the first write, in a dry run and an apply alike.
//
// The spellings are counted over EVERY eligible book (eligible, before the
// limit cut), not only the cohort (selected): the record's spelling is a fact
// about the library, and a run with a limit that counted its own cohort would
// create the record under whichever spelling that slice favoured, so a full dry
// run and the cohorts that follow it could disagree about the record (and the
// later cohorts, linking to the existing record, would respell the books the
// first cohort wrote). Only the books of the cohort are rewritten.
//
// A book whose respelling cannot be settled is filed under the review class
// that says why and loses its narrator side only: its author-side write, if it
// has one, stays in the cohort, as planNarratorSide's own refusal leaves it. A
// book with nothing left to write leaves the cohort; like a book that fails, it
// keeps its place in a limited cohort (the cohort was decided before this).
func (r *cbRun) settleNewSpellings(eligible, selected []string) []string {
	counts := map[string]map[string]int{} // fold key -> spelling -> books
	for _, id := range eligible {
		pl := r.plans[id].plan
		if !pl.narratorWrite {
			continue
		}
		for _, slot := range pl.narrators {
			if k := authorjunk.FoldKey(slot.Name); slot.ID == 0 && k != "" {
				if counts[k] == nil {
					counts[k] = map[string]int{}
				}
				counts[k][slot.Name]++
			}
		}
	}
	canon := map[string]string{}
	for k, spellings := range counts {
		if len(spellings) < 2 {
			continue
		}
		best, most := "", 0
		for spelling, n := range spellings {
			if n > most || (n == most && spelling < best) {
				best, most = spelling, n
			}
		}
		canon[k] = best
	}
	if len(canon) == 0 {
		return selected
	}
	kept := make([]string, 0, len(selected))
	for _, id := range selected {
		pl := r.plans[id].plan
		changed := false
		for i := range pl.narrators {
			slot := &pl.narrators[i]
			if c, ok := canon[authorjunk.FoldKey(slot.Name)]; ok && slot.ID == 0 && slot.Name != c {
				slot.Name, changed = c, true
			}
		}
		if changed {
			pl.newNames = pl.newNames[:0]
			for _, slot := range pl.narrators {
				if slot.ID == 0 {
					pl.newNames = append(pl.newNames, slot.Name)
				}
			}
			if refuse := pl.applySpelling(pl.narrators); refuse != "" {
				r.acc.add(id, refuse)
				pl.dropNarratorSide()
				if !pl.hasWrite() {
					r.plansMu.Lock()
					delete(r.plans, id)
					r.plansMu.Unlock()
					continue
				}
			}
		}
		kept = append(kept, id)
	}
	return kept
}

// narratorSideClasses are the write and informational classes only the narrator
// side of a plan files (planNarratorSide and applySpelling), which
// dropNarratorSide takes back out.
var narratorSideClasses = map[string]bool{
	cbNarratorFillFromColumn:        true,
	cbNarratorFillFromNarratorsJSON: true,
	cbNarratorSelfRead:              true,
	cbNarratorNewFromSplit:          true,
	cbNarratorPositionsRenumbered:   true,
	cbNarratorCreatesRecord:         true,
	cbNarratorColumnRespelled:       true,
	cbNarratorsJSONRespelled:        true,
	cbNarratorCleanDroppedAuthor:    true,
}

// dropNarratorSide removes the narrator half of a plan and leaves the author
// half as it was: the state planNarratorSide leaves when it refuses a spelling
// (the narrator write, its slots, the column and NarratorsJSON it would set, the
// names it would create, and the write and informational classes only it
// files). The classes the author side filed (and the book-level ones, such as
// the iTunes note) stay. Slot names settleNewSpellings renamed in place go with
// the slots.
func (pl *cbPlan) dropNarratorSide() {
	pl.narratorWrite = false
	pl.narrators, pl.narratorRows, pl.newNames = nil, nil, nil
	pl.column, pl.columnChanged = nil, false
	pl.json, pl.jsonChanged = nil, false
	pl.authorNames, pl.jsonNames = nil, nil
	pl.requested, pl.rawJSON, pl.columnText, pl.selfRead = nil, nil, "", false
	pl.writes = slices.DeleteFunc(pl.writes, func(c string) bool { return narratorSideClasses[c] })
	pl.infos = slices.DeleteFunc(pl.infos, func(c string) bool { return narratorSideClasses[c] })
}

// failPlan records a phase-1 failure: the book is filed under read_error and
// keeps its place in a limited cohort.
func (r *cbRun) failPlan(id string, err error) {
	r.plansMu.Lock()
	if r.unplanned == nil {
		r.unplanned = map[string]bool{}
	}
	r.unplanned[id] = true
	r.plansMu.Unlock()
	r.fail(id, cbReadError, err)
}

func (r *cbRun) fail(id, class string, err error) {
	r.failed.Add(1)
	r.acc.add(id, class)
	r.noteErr(err)
}

// cbGuardDecision is what the hands-off checks decided for one book.
type cbGuardDecision struct {
	// holdAll, when set, holds the whole book under that class.
	holdAll string
	// itunes: a file of the book is under books/itunes/**.
	itunes bool
	// authorLocked: the author field carries a user lock and the plan has an
	// author-side write.
	authorLocked bool
	// narratorLocked: the narrator field carries a user lock and the plan has
	// a narrator-side write.
	narratorLocked bool
}

// guard runs the hands-off checks on a book with something to write. A read
// error is returned (fail closed: the book is not written).
func (r *cbRun) guard(book *database.Book, plan *cbPlan) (cbGuardDecision, error) {
	var g cbGuardDecision
	files, err := r.env.store.GetBookFiles(book.ID)
	if err != nil {
		return g, fmt.Errorf("read files: %w", err)
	}
	paths := make([]string, 0, len(files)+1)
	paths = append(paths, book.FilePath)
	for _, f := range files {
		paths = append(paths, f.FilePath)
	}
	seriesName := ""
	if book.SeriesID != nil && r.series != nil {
		seriesName = r.series(*book.SeriesID)
	}
	switch kind, _ := repairs.GuardBookPathsWith(r.resolver, book.ID, paths, seriesName); kind {
	case "":
	case repairs.SkipITunes:
		g.itunes = true
	case repairs.SkipOwnerManual:
		g.holdAll = cbHeldOwnerManual
		return g, nil
	default:
		g.holdAll = cbHeldGuardUnreadable
		return g, nil
	}
	// The owner-manual rules beyond the path (title, credits, transcription,
	// franchise tag); the iTunes path check is lifted here and decided above.
	kind, _, err := repairs.GuardOwnerManualBooks(r.env.store, r.env.tags, r.series, r.resolver, []string{book.ID})
	if err != nil {
		return g, err
	}
	switch kind {
	case "":
	case repairs.SkipOwnerManual:
		g.holdAll = cbHeldOwnerManual
		return g, nil
	default:
		g.holdAll = cbHeldGuardUnreadable
		return g, nil
	}
	// A person's lock on a side holds the whole side, whatever the write would
	// do to the filing author: the lock says the person owns that field. A
	// repair's bare lock (it stops machines from restoring junk) does not.
	if plan.authorWrite || plan.narratorWrite {
		locks, err := database.LoadFieldLocks(r.env.store, book.ID)
		if err != nil {
			return g, fmt.Errorf("read field locks: %w", err)
		}
		userLocked := func(key string) bool { return locks.Locked(key) && !locks.RepairLocked(key) }
		g.authorLocked = plan.authorWrite && userLocked(database.FieldKeyAuthorName)
		g.narratorLocked = plan.narratorWrite && userLocked(database.FieldKeyNarrator)
	}
	return g, nil
}

// holdAll moves every write of the plan to held under class.
func (pl *cbPlan) holdAll(class string) {
	pl.authorWrite, pl.narratorWrite = false, false
	pl.writes, pl.infos, pl.newNames = nil, nil, nil
	pl.held = append(pl.held, class)
}

// errNarratorNameAmbiguous is returned by lookupNarrator for a name that
// matches no record exactly and folds to more than one.
var errNarratorNameAmbiguous = errors.New("narrator name folds to more than one existing record")

// lookupNarrator resolves a narrator name to an existing record. An exact
// match comes first: the store's name index when it has one, else the frozen
// index. A miss is retried on the fold key (authorjunk.FoldKey: case,
// diacritics, punctuation), so "Jose" links to the one record named "José"
// instead of minting a second; when several records fold to it (and none is an
// exact match) it is ambiguous and errNarratorNameAmbiguous comes back. ok is
// false when no record holds the name.
func (r *cbRun) lookupNarrator(name string) (database.Narrator, bool, error) {
	if r.byName != nil {
		n, err := r.byName.GetNarratorByName(name)
		if err != nil {
			return database.Narrator{}, false, fmt.Errorf("look up narrator %q: %w", name, err)
		}
		if n != nil {
			return *n, true, nil
		}
	} else if n, ok := r.narrIdx[util.NormalizeAuthor(strings.TrimSpace(name))]; ok {
		return n, true, nil
	}
	switch folded := r.foldIdx[authorjunk.FoldKey(name)]; len(folded) {
	case 0:
		return database.Narrator{}, false, nil
	case 1:
		return folded[0], true, nil
	default:
		return database.Narrator{}, false, errNarratorNameAmbiguous
	}
}

// createNarrator is CreateNarrator for a name the plan found no record for,
// except that a record this run already created under the same fold key is
// reused: two books naming "Jose" and "José" planned before either record
// existed get one record, not two. Creation is serialised (createMu), which is
// cheap: a run creates few records.
func (r *cbRun) createNarrator(name string) (*database.Narrator, error) {
	r.createMu.Lock()
	defer r.createMu.Unlock()
	key := authorjunk.FoldKey(name)
	if rec, ok := r.createdFold[key]; ok && key != "" {
		return &rec, nil
	}
	n, err := r.env.store.CreateNarrator(name)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, errors.New("store returned no narrator")
	}
	if key != "" {
		r.createdFold[key] = *n
	}
	return n, nil
}

// voidJournal takes back a journal row whose write was refused and never made,
// the way every journaling writer does (authormerge.Step, repairs.Writer): the
// row is rewritten under its id with Voided and RevertedAt set, so no revert
// acts on it and the resume does not read it as a written book.
func (r *cbRun) voidJournal(row *database.OperationChange) error {
	v := *row
	now := time.Now()
	v.RevertedAt, v.Voided = &now, true
	return r.env.store.CreateOperationChange(&v)
}

// apply writes one planned book of the cohort: create its new narrators,
// journal the pre- and post-image, then ModifyBookCredits, which re-checks the
// pre-image under the book's stripes. A write that is refused or fails voids
// its journal row.
func (r *cbRun) apply(pb *cbPlanned) {
	id, plan, before := pb.id, pb.plan, pb.before
	// A lock a person set after the plan holds the side: checked here, before
	// anything is created or journaled, and again inside the write's callback
	// right before the commit (a lock can land in between). The callback runs
	// under the book's stripes, but a lock is written by UpsertMetadataFieldState,
	// which takes no book stripe, so the second check narrows the window and
	// does not close it: a lock set after it lands after this write, and a
	// re-run holds the book.
	if held, err := r.lockedSince(id, plan); err != nil {
		r.fail(id, cbWriteFailed, fmt.Errorf("book %s: read field locks (book NOT written): %w", id, err))
		return
	} else if len(held) > 0 {
		r.acc.add(id, held...)
		return
	}
	var created []database.Narrator
	var newIDs []int
	spellingMoved := false
	if plan.narratorWrite && len(plan.newNames) > 0 {
		for i, slot := range plan.narrators {
			if slot.ID > 0 {
				continue
			}
			n, err := r.createNarrator(slot.Name)
			if err != nil {
				r.fail(id, cbWriteFailed, fmt.Errorf("book %s: create narrator %q: %w", id, slot.Name, err))
				return
			}
			// CreateNarrator and createNarrator are resolve-or-create: the
			// record may be one another book (or a worker) created for a name
			// of the same fold key, spelled differently ("José" for "Jose").
			// The junction credits the record, so the column must follow its
			// spelling, as at plan time.
			if util.NormalizeAuthor(strings.TrimSpace(n.Name)) != util.NormalizeAuthor(strings.TrimSpace(slot.Name)) {
				spellingMoved = true
			}
			plan.narrators[i].ID = n.ID
			plan.narrators[i].Name = n.Name
			newIDs = append(newIDs, n.ID)
			created = append(created, *n)
		}
		created = r.acc.claimCreated(created)
		if spellingMoved {
			if refuse := plan.applySpelling(plan.narrators); refuse != "" {
				r.fail(id, cbWriteFailed, fmt.Errorf("book %s: narrator record spelling (%s): not written, a re-run lists it for review", id, refuse))
				return
			}
		}
	}

	after := r.afterState(before, plan, id)
	oldJSON, err := json.Marshal(before)
	if err != nil {
		r.fail(id, cbWriteFailed, fmt.Errorf("book %s: encode pre-image: %w", id, err))
		return
	}
	newJSON, err := json.Marshal(undo.CreditsBackfillWrite{After: after, Classes: plan.writes, CreatedNarrators: created})
	if err != nil {
		r.fail(id, cbWriteFailed, fmt.Errorf("book %s: encode post-image: %w", id, err))
		return
	}
	row := &database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: r.env.opID,
		BookID:      id,
		ChangeType:  undo.ChangeTypeCreditsBackfill,
		FieldName:   "book_credits",
		OldValue:    string(oldJSON),
		NewValue:    string(newJSON),
	}
	if err := r.env.store.CreateOperationChange(row); err != nil {
		r.fail(id, cbWriteFailed, fmt.Errorf("book %s: journal (book NOT written): %w", id, err))
		return
	}
	var lockedHeld []string
	updated, edit, err := r.env.mod.ModifyBookCredits(id, func(b *database.Book, credits *database.BookCreditsEdit) error {
		cur := undo.CreditsBackfillStateOf(b, credits.Authors, credits.Narrators)
		if !undo.SameCreditsBackfillState(cur, before) {
			return errCreditsBackfillChanged
		}
		held, lerr := r.lockedSince(id, plan)
		if lerr != nil {
			return fmt.Errorf("read field locks: %w", lerr)
		}
		if len(held) > 0 {
			lockedHeld = held
			return errCreditsBackfillLocked
		}
		credits.Authors = slices.Clone(after.Authors)
		credits.Narrators = slices.Clone(after.Narrators)
		after.ApplyFlatFields(b)
		return nil
	})
	// voided takes the row back when the write did not happen; a failure to
	// void is part of the error (the row then stands, and its revert refuses it
	// as changed-since).
	voided := func(cause error) error {
		if verr := r.voidJournal(row); verr != nil {
			return fmt.Errorf("%w; the journal row %s could not be voided: %w", cause, row.ID, verr)
		}
		return cause
	}
	switch {
	case errors.Is(err, errCreditsBackfillLocked):
		// Held, not failed: the book is skipped whole and keeps its place in
		// the cohort; a re-run plans it with the lock in place. Nothing was
		// written; the journal row is voided.
		if verr := r.voidJournal(row); verr != nil {
			r.fail(id, cbWriteFailed, fmt.Errorf("book %s: locked since the plan; the journal row %s could not be voided: %w", id, row.ID, verr))
			return
		}
		r.acc.add(id, lockedHeld...)
		return
	case errors.Is(err, errCreditsBackfillChanged), err == nil && updated == nil:
		// Counted as a failure: the book is still dirty and the run must not
		// read as complete until a re-run has picked it up.
		r.fail(id, cbChangedSincePlan, voided(fmt.Errorf("book %s changed between the plan and the write; not written", id)))
		return
	case err != nil:
		r.fail(id, cbWriteFailed, voided(fmt.Errorf("book %s: write: %w", id, err)))
		return
	}
	got := undo.CreditsBackfillStateOf(updated, edit.Authors, edit.Narrators)
	if !undo.SameCreditsBackfillState(got, after) {
		// The journaled post-image is not what the store kept: the revert of
		// this row would refuse. Counted as a failure so the run says so.
		r.fail(id, cbWriteFailed, fmt.Errorf("book %s: stored credits differ from the journaled post-image", id))
		return
	}
	r.written.Add(1)
	r.planned.Add(1)
	r.acc.add(id, plan.writes...)
	r.acc.add(id, plan.infos...)
	r.acc.addNewNames(plan.newNames)
	r.acc.markCreatedWritten(newIDs)
	r.acc.sample(r.sampleOf(pb), plan.writes)
}

// lockedSince re-reads the book's field locks and returns the held classes for
// every side the plan writes that a person has locked (a repair's bare lock
// does not hold, as in guard). It fails closed: a read error is returned and
// the book must not be written.
func (r *cbRun) lockedSince(bookID string, plan *cbPlan) ([]string, error) {
	if !plan.authorWrite && !plan.narratorWrite {
		return nil, nil
	}
	locks, err := database.LoadFieldLocks(r.env.store, bookID)
	if err != nil {
		return nil, err
	}
	userLocked := func(key string) bool { return locks.Locked(key) && !locks.RepairLocked(key) }
	var held []string
	if plan.authorWrite && userLocked(database.FieldKeyAuthorName) {
		held = append(held, cbHeldUserLocked)
	}
	if plan.narratorWrite && userLocked(database.FieldKeyNarrator) {
		held = append(held, cbHeldNarratorLocked)
	}
	return held, nil
}

// afterState is the exact state the write leaves: before with the plan's
// lists and flat fields, the lists in the form the store keeps them.
func (r *cbRun) afterState(before undo.CreditsBackfillState, plan *cbPlan, bookID string) undo.CreditsBackfillState {
	after := before
	after.Authors = slices.Clone(before.Authors)
	after.Narrators = slices.Clone(before.Narrators)
	if plan.authorWrite {
		after.AuthorID = plan.authorID
		after.Author = plan.snapshot
		after.Authors = slices.Clone(plan.authors)
	}
	if plan.narratorWrite {
		after.Narrators = plan.narratorRowsOut()
		if plan.columnChanged {
			after.Narrator = plan.column
		}
		if plan.jsonChanged {
			after.NarratorsJSON = plan.json
		}
	}
	after = after.Canonical()
	for i := range after.Authors {
		after.Authors[i].BookID = bookID
	}
	for i := range after.Narrators {
		after.Narrators[i].BookID = bookID
	}
	return after
}

// narratorRowsOut is the plan's narrator rows: the renumbered junction, or
// the filled slots with the sync's roles.
func (pl *cbPlan) narratorRowsOut() []database.BookNarrator {
	if pl.narratorRows != nil {
		return slices.Clone(pl.narratorRows)
	}
	rows := make([]database.BookNarrator, 0, len(pl.narrators))
	for _, s := range pl.narrators {
		role := "narrator"
		if len(rows) > 0 {
			role = "co-narrator"
		}
		rows = append(rows, database.BookNarrator{NarratorID: s.ID, Role: role, Position: len(rows)})
	}
	return rows
}

// sampleOf renders a book's before/after for the result.
func (r *cbRun) sampleOf(pb *cbPlanned) *cbSample {
	before, plan := pb.before, pb.plan
	s := &cbSample{
		BookID:       pb.id,
		Title:        pb.title,
		Classes:      slices.Clone(plan.writes),
		Before:       r.viewOf(before, nil),
		NewNarrators: slices.Clone(plan.newNames),
	}
	after := before
	if plan.authorWrite {
		after.AuthorID, after.Author = plan.authorID, plan.snapshot
		after.Authors = database.NormalizeBookAuthors(plan.authors, plan.authorID)
	}
	var names []string
	if plan.narratorWrite {
		if plan.narratorRows != nil {
			after.Narrators = database.NormalizeBookNarrators(plan.narratorRows)
		} else {
			after.Narrators = nil
			for _, slot := range plan.narrators {
				names = append(names, slot.Name)
			}
		}
		if plan.columnChanged {
			after.Narrator = plan.column
		}
		if plan.jsonChanged {
			after.NarratorsJSON = plan.json
		}
	}
	s.After = r.viewOf(after, names)
	return s
}

// viewOf names a state's credits. narratorNames, when not nil, replaces the
// narrator rows (a dry run's fill has no ids for new records yet).
func (r *cbRun) viewOf(s undo.CreditsBackfillState, narratorNames []string) cbCreditsView {
	v := cbCreditsView{Authors: []string{}, Narrators: []string{}, AuthorID: s.AuthorID}
	for _, row := range database.NormalizeBookAuthors(s.Authors, s.AuthorID) {
		a, state, err := r.idx.resolveAuthor(row.AuthorID)
		switch {
		case err != nil || state == authorDangling:
			v.Authors = append(v.Authors, fmt.Sprintf("(missing author %d)", row.AuthorID))
		default:
			v.Authors = append(v.Authors, a.Name)
		}
	}
	if narratorNames != nil {
		v.Narrators = append(v.Narrators, narratorNames...)
	} else {
		for _, row := range database.NormalizeBookNarrators(s.Narrators) {
			n, ok, err := r.idx.resolveNarrator(row.NarratorID)
			if err != nil || !ok {
				v.Narrators = append(v.Narrators, fmt.Sprintf("(missing narrator %d)", row.NarratorID))
				continue
			}
			v.Narrators = append(v.Narrators, n.Name)
		}
	}
	if s.Author != nil {
		v.AuthorSnapshot = fmt.Sprintf("%d:%s", s.Author.ID, s.Author.Name)
	}
	if s.Narrator != nil {
		v.Narrator = *s.Narrator
	}
	if s.NarratorsJSON != nil {
		v.NarratorsJSON = *s.NarratorsJSON
	}
	return v
}

// narratorLookup resolves a narrator name to an existing record.
type narratorLookup func(name string) (database.Narrator, bool, error)

// planCreditsBackfill decides what the backfill does to one book. census is
// the book's credit-census classes (classifyCreditBook). It reads nothing but
// the frozen index (and lookup, for narrator names) and writes nothing.
//
// authorHold and narratorHold, when set, hold that side under that class
// instead of planning it (a guard's decision on a re-plan).
func planCreditsBackfill(idx *creditCensusIndex, lookup narratorLookup, allow map[string]bool, book *database.Book,
	join []database.BookAuthor, junction []database.BookNarrator, census []string, authorHold, narratorHold string) (*cbPlan, error) {
	has := make(map[string]bool, len(census))
	for _, c := range census {
		has[c] = true
	}
	plan := &cbPlan{}
	postAuthorID, postJoin := book.AuthorID, join
	if authorHold != "" {
		plan.held = append(plan.held, authorHold)
	} else {
		var err error
		if postAuthorID, postJoin, err = planAuthorSide(idx, plan, book, join, has); err != nil {
			return nil, err
		}
	}
	authorNames, err := liveAuthorNames(idx, postAuthorID, postJoin)
	if err != nil {
		return nil, err
	}
	if narratorHold != "" {
		plan.held = append(plan.held, narratorHold)
		return plan, nil
	}
	if err := planNarratorSide(idx, lookup, allow, plan, book, junction, has, authorNames); err != nil {
		return nil, err
	}
	return plan, nil
}

// planAuthorSide fills the author half of plan. It returns the AuthorID and
// join the book will have afterwards (unchanged when the side is not
// written), which the narrator side cleans its credit against.
func planAuthorSide(idx *creditCensusIndex, plan *cbPlan, book *database.Book, join []database.BookAuthor, has map[string]bool) (*int, []database.BookAuthor, error) {
	authorID := 0
	if book.AuthorID != nil && *book.AuthorID > 0 {
		authorID = *book.AuthorID
	}
	review := func(c string) (*int, []database.BookAuthor, error) {
		plan.reviews = append(plan.reviews, c)
		return book.AuthorID, join, nil
	}
	for _, c := range []string{ccAuthorIDDangling, ccAuthorIDTombstoned, ccAuthorJoinCombined} {
		if has[c] {
			plan.reviews = append(plan.reviews, c)
		}
	}
	if len(plan.reviews) > 0 {
		return book.AuthorID, join, nil
	}

	var (
		newJoin  = join
		newID    = book.AuthorID
		writes   []string
		sourceID int
	)
	switch {
	case authorID == 0 && len(join) == 0:
		snap := book.Author
		if snap == nil {
			// no_author: nothing to build from.
			return book.AuthorID, join, nil
		}
		a, state, err := idx.resolveAuthor(snap.ID)
		if err != nil {
			return nil, nil, err
		}
		if snap.ID <= 0 || state != authorLive || a.ID != snap.ID {
			return review(cbAuthorSnapshotUnresolved)
		}
		sourceID = a.ID
		newJoin = []database.BookAuthor{{BookID: book.ID, AuthorID: a.ID, Role: "author", Position: 0}}
		id := a.ID
		newID = &id
		writes = append(writes, cbAuthorFillFromSnapshot)
	case authorID != 0 && len(join) == 0:
		// With no join the organizer files the book under the snapshot, not
		// under AuthorID (filingAuthorName). AuthorID is the flat field a
		// rescan reverts from file tags, and the snapshot is the one signal a
		// rescan never touches: when the snapshot names ANOTHER author the
		// fill would credit the reverted value and the refresh below would
		// overwrite the snapshot, destroying the only evidence of who the
		// book is filed under. Held for review, the snapshot never refreshed.
		// A snapshot whose id resolves to nothing (id 0, or a dangling id) still
		// carries a name: snapshotNamesOtherAuthor compares it with AuthorID's
		// record name, so a snapshot naming someone else without a usable id is
		// held too.
		if conflict, err := snapshotNamesOtherAuthor(idx, book.Author, authorID); err != nil {
			return nil, nil, err
		} else if conflict {
			return review(cbAuthorIDSnapshotConflict)
		}
		sourceID = authorID
		newJoin = []database.BookAuthor{{BookID: book.ID, AuthorID: authorID, Role: "author", Position: 0}}
		writes = append(writes, cbAuthorFillFromAuthorID)
	case authorID == 0:
		first := 0
		for _, r := range database.NormalizeBookAuthors(join, nil) {
			if database.IsAuthorCreditRole(r.Role) {
				first = r.AuthorID
				break
			}
		}
		if first == 0 {
			return review(cbAuthorJoinNoAuthorRole)
		}
		id := first
		newID = &id
		sourceID = first
		writes = append(writes, cbAuthorIDFromJoin)
	case has[ccAuthorIDNotInJoin]:
		a, _, err := idx.resolveAuthor(authorID)
		if err != nil {
			return nil, nil, err
		}
		norm := util.NormalizeAuthor(strings.TrimSpace(a.Name))
		for _, r := range join {
			other, state, err := idx.resolveAuthor(r.AuthorID)
			if err != nil {
				return nil, nil, err
			}
			if state != authorDangling && other.ID != a.ID && norm != "" && util.NormalizeAuthor(strings.TrimSpace(other.Name)) == norm {
				return review(cbAuthorIDDuplicateName)
			}
		}
		// AuthorID is the flat field a rescan reverts from file tags (the
		// scanner never touches the join or the snapshot), so when the
		// snapshot names a join member other than AuthorID, AuthorID is the
		// signal most likely to be stale: prepending it would flip the
		// organizer's filing author onto the reverted value.
		if conflict, err := snapshotNamesOtherJoinMember(idx, book.Author, authorID, join); err != nil {
			return nil, nil, err
		} else if conflict {
			return review(cbAuthorIDSnapshotConflict)
		}
		sourceID = authorID
		minPos := 0
		for i, r := range join {
			if i == 0 || r.Position < minPos {
				minPos = r.Position
			}
		}
		newJoin = append([]database.BookAuthor{{BookID: book.ID, AuthorID: authorID, Role: "author", Position: minPos - 1}}, join...)
		writes = append(writes, cbAuthorIDPrepended)
	case has[ccAuthorIDNotPrimary]:
		return review(ccAuthorIDNotPrimary)
	}
	if sourceID != 0 {
		a, _, err := idx.resolveAuthor(sourceID)
		if err != nil {
			return nil, nil, err
		}
		if idx.looksCombined(a.Name) {
			return review(cbAuthorSourceCombined)
		}
	}
	if has[ccAuthorPositionsNotNorm] {
		// The renumber breaks position ties with AuthorID (the plan's rule).
		// When that reorders the join and the snapshot names a different join
		// member, AuthorID is contradicted: the book goes to review instead.
		if authorID != 0 && len(join) > 0 && tieBreakReorders(join, authorID) {
			if conflict, err := snapshotNamesOtherJoinMember(idx, book.Author, authorID, join); err != nil {
				return nil, nil, err
			} else if conflict {
				return review(cbAuthorIDSnapshotConflict)
			}
		}
		writes = append(writes, cbAuthorPositionsRenumbered)
	}

	// The snapshot follows the primary when the book carries one: refreshed
	// when the census reads it as stale or the write moves the primary. A
	// book with no snapshot keeps none.
	snapshot := book.Author
	snapChanged := false
	primaryMoves := !sameAuthorIDPtr(newID, book.AuthorID)
	if book.Author != nil && newID != nil && (has[ccAuthorSnapshotStale] || primaryMoves) {
		a, state, err := idx.resolveAuthor(*newID)
		if err != nil {
			return nil, nil, err
		}
		if state == authorLive && (book.Author.ID != a.ID || book.Author.Name != a.Name) {
			rec := database.Author{ID: a.ID, Name: a.Name}
			snapshot, snapChanged = &rec, true
			writes = append(writes, cbAuthorSnapshotRefreshed)
		}
	}
	if len(writes) == 0 {
		return book.AuthorID, join, nil
	}

	// The placeholder is never credited, never made the primary, and never the
	// source of AuthorID: a placeholder at position 0 hides the real author,
	// and an AuthorID taken from a placeholder-only join would make the
	// organizer read the book as resolved (organizer.HasResolvedAuthor).
	candidates := []int{sourceID}
	if newID != nil {
		candidates = append(candidates, *newID)
	}
	if primary, ok := organizer.PrimaryJoinAuthor(database.NormalizeBookAuthors(newJoin, newID)); ok {
		candidates = append(candidates, primary.AuthorID)
	}
	for _, id := range candidates {
		if id <= 0 {
			continue
		}
		a, state, err := idx.resolveAuthor(id)
		if err != nil {
			return nil, nil, err
		}
		if state != authorDangling && authorname.IsPlaceholder(a.Name) {
			return review(cbAuthorPlaceholder)
		}
	}

	plan.authorWrite = true
	plan.writes = append(plan.writes, writes...)
	plan.authors = slices.Clone(newJoin)
	plan.authorID = newID
	plan.snapshot = snapshot
	plan.snapChanged = snapChanged

	before, err := filingAuthorName(idx, join, book.Author, book.AuthorID)
	if err != nil {
		return nil, nil, err
	}
	after, err := filingAuthorName(idx, database.NormalizeBookAuthors(newJoin, newID), snapshot, newID)
	if err != nil {
		return nil, nil, err
	}
	if util.NormalizeAuthor(before) != util.NormalizeAuthor(after) {
		plan.filingChanges = true
		plan.infos = append(plan.infos, cbFilingAuthorChanges)
	}
	return newID, database.NormalizeBookAuthors(newJoin, newID), nil
}

func sameAuthorIDPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// filingAuthorName is the author the organizer files a book under, by the
// organizer's own rules (organizer.resolveAuthorName, whose pure parts are
// organizer.PrimaryJoinAuthor, JoinAuthorName and ScalarAuthorName): the
// join's first stored row at the lowest position when its record is a real
// name (a dangling, blank or placeholder primary is skipped), else the
// snapshot's trimmed name when a snapshot is present (a blank one yields ""
// and does NOT fall through), else AuthorID's record. join must be in stored
// order, as GetBookAuthors returns it.
func filingAuthorName(idx *creditCensusIndex, join []database.BookAuthor, snap *database.Author, authorID *int) (string, error) {
	author := func(id int) (*database.Author, error) {
		a, state, err := idx.resolveAuthor(id)
		if err != nil || state == authorDangling {
			return nil, err
		}
		return &a, nil
	}
	if primary, ok := organizer.PrimaryJoinAuthor(join); ok {
		a, err := author(primary.AuthorID)
		if err != nil {
			return "", err
		}
		if name := organizer.JoinAuthorName(a); name != "" {
			return name, nil
		}
	}
	var scalar *database.Author
	if snap == nil && authorID != nil && *authorID > 0 {
		a, err := author(*authorID)
		if err != nil {
			return "", err
		}
		scalar = a
	}
	return organizer.ScalarAuthorName(snap, scalar), nil
}

// snapshotNamesOtherJoinMember reports whether the Book.Author snapshot names a
// join member other than authorID.
//
// A snapshot whose id resolves to a record is compared by record (as canonical
// records, so a tombstoned id counts as the author it was merged into): it
// names another member when that record is in the join and is not authorID's.
// A snapshot naming authorID itself, or an author outside the join, is not a
// second signal against AuthorID.
//
// A snapshot whose id does not resolve (no id, id 0, or an id with no record)
// still carries a name, and a name is the only evidence left of who the book
// is filed under: it is compared with the record names of the join members
// other than authorID, normalized as the census compares. A match is the same
// contradiction as the id form, and prepending AuthorID would flip the filing
// author and the refresh would overwrite the snapshot. That is a comparison of
// names the book already carries, never a lookup: no author is searched for or
// matched by name, and none is credited because of it. A blank snapshot name,
// or a name that is also AuthorID's own, is no signal.
func snapshotNamesOtherJoinMember(idx *creditCensusIndex, snap *database.Author, authorID int, join []database.BookAuthor) (bool, error) {
	if snap == nil {
		return false, nil
	}
	primary, err := canonicalAuthorID(idx, authorID)
	if err != nil {
		return false, err
	}
	if snap.ID > 0 {
		snapID, err := canonicalAuthorID(idx, snap.ID)
		if err != nil {
			return false, err
		}
		if snapID != 0 {
			if snapID == primary {
				return false, nil
			}
			for _, r := range join {
				id, err := canonicalAuthorID(idx, r.AuthorID)
				if err != nil {
					return false, err
				}
				if id == snapID {
					return true, nil
				}
			}
			return false, nil
		}
	}
	snapName := util.NormalizeAuthor(strings.TrimSpace(snap.Name))
	if snapName == "" {
		return false, nil
	}
	if primary != 0 {
		a, _, err := idx.resolveAuthor(primary)
		if err != nil {
			return false, err
		}
		if util.NormalizeAuthor(strings.TrimSpace(a.Name)) == snapName {
			return false, nil
		}
	}
	for _, r := range join {
		id, err := canonicalAuthorID(idx, r.AuthorID)
		if err != nil {
			return false, err
		}
		if id == 0 || id == primary {
			continue
		}
		a, _, err := idx.resolveAuthor(id)
		if err != nil {
			return false, err
		}
		if util.NormalizeAuthor(strings.TrimSpace(a.Name)) == snapName {
			return true, nil
		}
	}
	return false, nil
}

// canonicalAuthorID is the id of the record id resolves to (a tombstoned id as
// the author it was merged into), 0 for an id that resolves to nothing.
func canonicalAuthorID(idx *creditCensusIndex, id int) (int, error) {
	a, state, err := idx.resolveAuthor(id)
	if err != nil || state == authorDangling {
		return 0, err
	}
	return a.ID, nil
}

// snapshotNamesOtherAuthor reports whether the Book.Author snapshot names a
// live author other than authorID. It is the empty-join form of
// snapshotNamesOtherJoinMember: there is no join to be a member of, and the
// snapshot is what the organizer files the book under.
//
// A snapshot id that resolves to a live author is compared by record (as
// canonical records, so a tombstoned id counts as the author it was merged
// into). A snapshot whose id does not resolve (no id, id 0, or an id with no
// record: the only evidence left is the name the snapshot carries) is compared
// by NAME against AuthorID's own record: a different normalized name is the
// same contradiction, and the fill would flip the filing author onto AuthorID
// and the refresh would overwrite the snapshot. That is a comparison of two
// names the book already carries, never a name lookup: no author is searched
// for or matched by name, and none is credited because of it. A blank snapshot
// name is no signal.
func snapshotNamesOtherAuthor(idx *creditCensusIndex, snap *database.Author, authorID int) (bool, error) {
	if snap == nil {
		return false, nil
	}
	primary, err := canonicalAuthorID(idx, authorID)
	if err != nil {
		return false, err
	}
	if snap.ID > 0 {
		snapID, err := canonicalAuthorID(idx, snap.ID)
		if err != nil {
			return false, err
		}
		if snapID != 0 {
			return snapID != primary, nil
		}
	}
	snapName := util.NormalizeAuthor(strings.TrimSpace(snap.Name))
	if snapName == "" || primary == 0 {
		return false, nil
	}
	a, _, err := idx.resolveAuthor(primary)
	if err != nil {
		return false, err
	}
	return snapName != util.NormalizeAuthor(strings.TrimSpace(a.Name)), nil
}

// tieBreakReorders reports whether breaking position ties with authorID
// (NormalizeBookAuthors' tie-breaker) orders the join differently from the
// stored order the organizer reads it in.
func tieBreakReorders(join []database.BookAuthor, authorID int) bool {
	ids := func(rows []database.BookAuthor) []int {
		out := make([]int, len(rows))
		for i, r := range rows {
			out[i] = r.AuthorID
		}
		return out
	}
	stored := database.NormalizeBookAuthors(slices.Clone(join), nil)
	broken := database.NormalizeBookAuthors(slices.Clone(join), &authorID)
	return !slices.Equal(ids(stored), ids(broken))
}

// liveAuthorNames returns the book's live author names in credit order:
// AuthorID first, then the join in position order, each record once, a
// tombstoned id as its canonical record, a dangling one skipped. It is the
// list the census (and database.LiveBookAuthorNames) hands
// util.CleanNarratorCredit.
func liveAuthorNames(idx *creditCensusIndex, authorID *int, join []database.BookAuthor) ([]string, error) {
	var ids []int
	if authorID != nil && *authorID > 0 {
		ids = append(ids, *authorID)
	}
	sorted := slices.Clone(join)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	for _, r := range sorted {
		ids = append(ids, r.AuthorID)
	}
	seen := map[int]bool{}
	var names []string
	for _, id := range ids {
		a, state, err := idx.resolveAuthor(id)
		if err != nil {
			return nil, err
		}
		if state == authorDangling || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		if n := strings.TrimSpace(a.Name); n != "" {
			names = append(names, n)
		}
	}
	return names, nil
}

// planNarratorSide fills the narrator half of plan. authorNames are the
// book's live authors after the author side's write.
func planNarratorSide(idx *creditCensusIndex, lookup narratorLookup, allow map[string]bool, plan *cbPlan, book *database.Book,
	junction []database.BookNarrator, has map[string]bool, authorNames []string) error {
	for _, c := range []string{ccNarratorIDDangling, ccNarratorJunctionCombined} {
		if has[c] {
			plan.reviews = append(plan.reviews, c)
		}
	}
	if has[ccNarratorIDDangling] || has[ccNarratorJunctionCombined] {
		return nil
	}

	var junctionNames []string
	for _, r := range database.NormalizeBookNarrators(junction) {
		n, ok, err := idx.resolveNarrator(r.NarratorID)
		if err != nil {
			return err
		}
		if ok && strings.TrimSpace(n.Name) != "" {
			junctionNames = append(junctionNames, strings.TrimSpace(n.Name))
		}
	}
	column := ""
	if book.Narrator != nil {
		column = strings.TrimSpace(*book.Narrator)
	}
	var jsonNames []string
	jsonIsArray := false
	if book.NarratorsJSON != nil {
		jsonNames = parseCreditCensusNarratorsJSON(*book.NarratorsJSON)
		var arr []string
		jsonIsArray = json.Unmarshal([]byte(strings.TrimSpace(*book.NarratorsJSON)), &arr) == nil
	}

	var (
		fillNames  []string
		fillClass  string
		fromSplit  bool
		newColumn  *string
		renumber   bool
		cleanedCol []string
		// rawJunk: the raw source of a fill (the column, or an entry of
		// NarratorsJSON) is a stand-in as written, before it is split: "N/A"
		// splits on its slash into two one-letter "people".
		rawJunk bool
	)
	switch {
	case column != "":
		people, verdict := util.CleanNarratorCredit(column, authorNames)
		cleanedCol = people
		switch {
		case verdict != util.NarratorCreditPeople && len(junction) > 0:
			if !sameNameSet(junctionNames, util.SplitCreditNames(column)) {
				plan.reviews = append(plan.reviews, ccNarratorColumnNotPeopleJunctionDiffers)
				return nil
			}
			// A self-read whose junction already credits the named author.
			renumber = true
		case verdict == util.NarratorCreditAllAuthors:
			// Empty junction, the column names only the book's authors.
			var names []string
			for _, piece := range util.SplitCreditNames(column) {
				if ppl, v := util.CleanNarratorCredit(piece, nil); v == util.NarratorCreditPeople {
					names = append(names, ppl...)
				}
			}
			if !allow[cbAllowSelfRead] || len(names) == 0 {
				plan.reviews = append(plan.reviews, cbNarratorSelfRead)
				return nil
			}
			fillNames, fillClass = names, cbNarratorSelfRead
			fromSplit = len(util.SplitCreditNames(column)) > 1
		case verdict != util.NarratorCreditPeople:
			plan.reviews = append(plan.reviews, ccNarratorColumnNotPeople)
			return nil
		case len(junction) == 0:
			rawJunk = isJunkNarratorName(column)
			fillNames, fillClass = people, cbNarratorFillFromColumn
			fromSplit = len(util.SplitCreditNames(column)) > 1
			if dropsAuthor(column, people) {
				plan.infos = append(plan.infos, cbNarratorCleanDroppedAuthor)
			}
		case !sameOrderedNames(people, junctionNames):
			plan.reviews = append(plan.reviews, ccNarratorColumnDiffers)
			return nil
		default:
			renumber = true
		}
	case len(junction) > 0:
		plan.reviews = append(plan.reviews, ccNarratorJunctionNoColumn)
		return nil
	case len(jsonNames) > 0:
		var raw []string
		for _, entry := range jsonNames {
			rawJunk = rawJunk || isJunkNarratorName(entry)
			ppl, v := util.CleanNarratorCredit(entry, nil)
			if v != util.NarratorCreditPeople || len(ppl) != 1 {
				plan.reviews = append(plan.reviews, cbNarratorsJSONNotPeople)
				return nil
			}
			raw = append(raw, ppl[0])
		}
		joined := database.JoinCreditNames(raw)
		people, verdict := util.CleanNarratorCredit(joined, authorNames)
		if verdict != util.NarratorCreditPeople {
			plan.reviews = append(plan.reviews, cbNarratorsJSONNotPeople)
			return nil
		}
		// The column must split back into exactly these people, or the
		// census would read the book as column != junction.
		if !sameOrderedNames(util.SplitCreditNames(joined), raw) {
			plan.reviews = append(plan.reviews, cbNarratorsJSONUnjoinable)
			return nil
		}
		fillNames, fillClass = people, cbNarratorFillFromNarratorsJSON
		fromSplit = !jsonIsArray && len(jsonNames) > 1
		newColumn = &joined
		cleanedCol = people
	}

	// NarratorsJSON that matches neither the (resulting) junction nor the
	// column is a disagreement the census keeps counting: left for review.
	if len(jsonNames) > 0 && fillClass != cbNarratorFillFromNarratorsJSON {
		result := junctionNames
		if fillNames != nil {
			result = fillNames
		}
		matchesJunction := len(result) > 0 && sameNameSet(jsonNames, result)
		matchesColumn := column != "" &&
			(sameNameSet(jsonNames, util.SplitCreditNames(column)) || (len(cleanedCol) > 0 && sameNameSet(jsonNames, cleanedCol)))
		if !matchesJunction && !matchesColumn {
			plan.reviews = append(plan.reviews, ccNarratorsJSONDisagrees)
			plan.infos = removeClass(plan.infos, cbNarratorCleanDroppedAuthor)
			return nil
		}
	}

	switch {
	case fillNames != nil:
		// A stand-in is never credited, created or linked (an existing
		// "Unknown" record included): checked before any lookup.
		if rawJunk || slices.ContainsFunc(fillNames, isJunkNarratorName) {
			plan.reviews = append(plan.reviews, cbNarratorJunkName)
			plan.infos = removeClass(plan.infos, cbNarratorCleanDroppedAuthor)
			return nil
		}
		// Two names of one fold key ("Jose", "José") are one person: crediting
		// both would list one narrator twice, collapsing them would leave the
		// column and the junction disagreeing. For a person to settle.
		folds := map[string]bool{}
		for _, name := range fillNames {
			if k := authorjunk.FoldKey(name); k != "" {
				if folds[k] {
					plan.reviews = append(plan.reviews, cbNarratorNameAmbiguous)
					plan.infos = removeClass(plan.infos, cbNarratorCleanDroppedAuthor)
					return nil
				}
				folds[k] = true
			}
		}
		var slots []cbNarratorSlot
		var newNames []string
		for _, name := range fillNames {
			n, ok, err := lookup(name)
			if errors.Is(err, errNarratorNameAmbiguous) {
				plan.reviews = append(plan.reviews, cbNarratorNameAmbiguous)
				plan.infos = removeClass(plan.infos, cbNarratorCleanDroppedAuthor)
				return nil
			}
			if err != nil {
				return err
			}
			if ok {
				slots = append(slots, cbNarratorSlot{Name: n.Name, ID: n.ID})
				continue
			}
			slots = append(slots, cbNarratorSlot{Name: name})
			newNames = append(newNames, name)
		}
		if len(newNames) > 0 && fromSplit && !allow[cbAllowNewFromSplit] {
			plan.reviews = append(plan.reviews, cbNarratorNewFromSplit)
			plan.infos = removeClass(plan.infos, cbNarratorCleanDroppedAuthor)
			return nil
		}
		plan.authorNames, plan.jsonNames = authorNames, jsonNames
		plan.narrators = slots
		plan.requested = fillNames
		plan.rawJSON = book.NarratorsJSON
		plan.selfRead = fillClass == cbNarratorSelfRead
		if (fillClass == cbNarratorFillFromColumn || plan.selfRead) && book.Narrator != nil {
			plan.columnText = *book.Narrator
		}
		// A name linked to a record by its fold key ("Renee" to the record
		// "Renée") is credited under the RECORD's spelling, and the census
		// compares column and junction by normalized spelling, so the name is
		// respelled inside the column (and the NarratorsJSON entries that carry
		// it) in the same write, or the book would plan as
		// narrator_column_differs_junction after it. Only the matched name
		// changes; the rest of the column stays as written.
		if moved := narratorSpellingMoved(slots, fillNames); moved {
			if refuse := plan.applySpelling(slots); refuse != "" {
				plan.reviews = append(plan.reviews, refuse)
				plan.infos = removeClass(plan.infos, cbNarratorCleanDroppedAuthor)
				plan.narrators, plan.authorNames, plan.jsonNames = nil, nil, nil
				plan.requested, plan.rawJSON, plan.columnText = nil, nil, ""
				return nil
			}
			newColumn = plan.column
		}
		plan.narratorWrite = true
		plan.newNames = newNames
		plan.writes = append(plan.writes, fillClass)
		if len(newNames) > 0 {
			plan.infos = append(plan.infos, cbNarratorCreatesRecord)
			if fromSplit {
				plan.writes = append(plan.writes, cbNarratorNewFromSplit)
			}
		}
		if newColumn != nil {
			plan.column, plan.columnChanged = newColumn, true
		}
	case renumber && has[ccNarratorPositionsNotNorm]:
		plan.narratorWrite = true
		plan.narratorRows = database.NormalizeBookNarrators(junction)
		plan.writes = append(plan.writes, cbNarratorPositionsRenumbered)
	}
	return nil
}

// narratorSpellingMoved reports whether any slot linked to an existing record
// carries a spelling that differs, normalized as the census compares, from the
// name the plan asked for at the same position.
func narratorSpellingMoved(slots []cbNarratorSlot, requested []string) bool {
	for i, slot := range slots {
		if i < len(requested) && slot.ID > 0 &&
			util.NormalizeAuthor(strings.TrimSpace(slot.Name)) != util.NormalizeAuthor(strings.TrimSpace(requested[i])) {
			return true
		}
	}
	return false
}

// applySpelling settles a spelling move on the plan: it sets the plan's column
// and NarratorsJSON to what the write leaves and files the informational
// classes, or returns the review class that refuses the book (the plan is left
// as it was then).
func (pl *cbPlan) applySpelling(slots []cbNarratorSlot) (refuse string) {
	column, newJSON, refuse := settleNarratorSpelling(pl, slots)
	if refuse != "" {
		return refuse
	}
	pl.column, pl.columnChanged = &column, true
	pl.json, pl.jsonChanged = newJSON, newJSON != nil
	pl.infos = removeClass(removeClass(pl.infos, cbNarratorColumnRespelled), cbNarratorsJSONRespelled)
	if pl.columnText != "" {
		pl.infos = append(pl.infos, cbNarratorColumnRespelled)
	}
	if newJSON != nil {
		pl.infos = append(pl.infos, cbNarratorsJSONRespelled)
	}
	return ""
}

// settleNarratorSpelling returns the Narrator column and NarratorsJSON a write
// leaves when the junction credits slots under their records' spellings, or the
// review class that refuses the book.
//
// A slot whose name differs (normalized, as the census compares) from the name
// the plan asked for is a spelling move. Only that name changes: it is replaced
// inside the column as stored (a fill from the column keeps its "Read by"
// prefix, its role credits and a co-narrating author), and inside the
// NarratorsJSON entries that carry it (newJSON is nil when nothing changed). A
// column built from NarratorsJSON has no text to preserve and is the slot names
// joined ("A and B").
//
// The respelled column must read back, through the same util.CleanNarratorCredit
// call the census makes, as exactly the slot names: otherwise the census would
// keep counting the book after the write (cbNarratorSpellingUnreconcilable: the
// name is not in the column as written, or the record's name carries a
// separator). The respelled NarratorsJSON must still match the junction or the
// column (cbNarratorSpellingJSONConflict).
func settleNarratorSpelling(plan *cbPlan, slots []cbNarratorSlot) (column string, newJSON *string, refuse string) {
	names := make([]string, len(slots))
	var pairs [][2]string
	for i, slot := range slots {
		names[i] = slot.Name
		if i < len(plan.requested) &&
			util.NormalizeAuthor(strings.TrimSpace(slot.Name)) != util.NormalizeAuthor(strings.TrimSpace(plan.requested[i])) {
			pairs = append(pairs, [2]string{plan.requested[i], slot.Name})
		}
	}
	authors := plan.authorNames
	if plan.selfRead {
		// A self-read names only the book's own authors: the cleaner would drop
		// every one of them, so it reads the column without them.
		authors = nil
	}
	fromColumn := plan.columnText != ""
	if fromColumn {
		column = respellCreditText(plan.columnText, pairs)
	} else {
		column = database.JoinCreditNames(names)
	}
	people, verdict := util.CleanNarratorCredit(column, authors)
	if verdict != util.NarratorCreditPeople || !sameOrderedNames(people, names) ||
		(!fromColumn && !sameOrderedNames(util.SplitCreditNames(column), names)) {
		return "", nil, cbNarratorSpellingUnreconcilable
	}
	if plan.rawJSON == nil || len(plan.jsonNames) == 0 {
		return column, nil, ""
	}
	respelled := respellNarratorsJSON(*plan.rawJSON, pairs)
	jsonNames := plan.jsonNames
	if respelled != *plan.rawJSON {
		newJSON = &respelled
		jsonNames = parseCreditCensusNarratorsJSON(respelled)
	}
	agrees := sameNameSet(jsonNames, names)
	if fromColumn {
		agrees = agrees || sameNameSet(jsonNames, util.SplitCreditNames(column)) || sameNameSet(jsonNames, people)
	}
	if !agrees {
		return "", nil, cbNarratorSpellingJSONConflict
	}
	return column, newJSON, ""
}

// respellCreditText replaces each pair's old name in text with its new one,
// in the credited name it belongs to. The text is read as the credit pieces
// util.SplitCreditNames cuts it into (the cut the census and the read-back
// make), and the old name is looked for piece by piece: first a piece that is
// exactly the name, then a piece that holds it as whole words (a "Read by"
// prefix or a role credit around it). A name that is only part of another
// credited name does not count: "Renee Montoya" is not found inside "Renee
// Montoya-Sawyer" or "Anne Renee Montoya's", so the respelling lands on the
// credit that names the person, wherever it stands in the text. A name not
// found leaves the text as it was; the caller verifies the result reads back
// (settleNarratorSpelling), which stays the backstop.
func respellCreditText(text string, pairs [][2]string) string {
	for _, p := range pairs {
		if i := creditNameOffset(text, p[0]); i >= 0 {
			text = text[:i] + p[1] + text[i+len(p[0]):]
		}
	}
	return text
}

// creditNameOffset is the byte offset in text of the occurrence of name that
// belongs to the credit naming that person (see respellCreditText), or -1.
func creditNameOffset(text, name string) int {
	if name == "" {
		return -1
	}
	type piece struct {
		start int
		text  string
	}
	var pieces []piece
	cursor := 0
	for _, p := range util.SplitCreditNames(text) {
		i := strings.Index(text[cursor:], p)
		if i < 0 || p == "" {
			continue
		}
		pieces = append(pieces, piece{cursor + i, p})
		cursor += i + len(p)
	}
	for _, p := range pieces {
		if p.text == name {
			return p.start
		}
	}
	for _, p := range pieces {
		for from := 0; from < len(p.text); {
			j := strings.Index(p.text[from:], name)
			if j < 0 {
				break
			}
			j += from
			if wholeWords(p.text, j, j+len(name)) {
				return p.start + j
			}
			from = j + 1
		}
	}
	return -1
}

// wholeWords reports whether s[start:end] stands as whole words in s: the rune
// before it and the rune after it (when there are any) are not part of a name.
// A letter, digit or mark continues a word, and so do a hyphen and an
// apostrophe that touch the name ("Montoya-Sawyer", "Montoya's").
func wholeWords(s string, start, end int) bool {
	joins := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '-' || r == '\'' || r == '\u2019'
	}
	if start > 0 {
		if r, _ := utf8.DecodeLastRuneInString(s[:start]); joins(r) {
			return false
		}
	}
	if end < len(s) {
		if r, _ := utf8.DecodeRuneInString(s[end:]); joins(r) {
			return false
		}
	}
	return true
}

// respellNarratorsJSON respells the names inside NarratorsJSON: each entry of a
// JSON array of strings, else the bare credit string (the two forms the census
// parses, parseCreditCensusNarratorsJSON).
func respellNarratorsJSON(raw string, pairs [][2]string) string {
	var arr []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &arr); err != nil {
		return respellCreditText(raw, pairs)
	}
	changed := false
	for i := range arr {
		if r := respellCreditText(arr[i], pairs); r != arr[i] {
			arr[i], changed = r, true
		}
	}
	if !changed {
		// Untouched, byte for byte: no re-encoding of an array that holds
		// none of the names.
		return raw
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(arr); err != nil {
		return raw
	}
	return strings.TrimSpace(buf.String())
}

// narratorJunkNames are the stand-ins a narrator credit can carry instead of a
// person, by authorjunk.FoldKey (case, diacritics, punctuation and spaces
// folded away, so "N/A", "n.a." and "NA" are one key). authorjunk's own lists
// are author-shaped (collective credits such as "Various Authors" are
// legitimate AUTHOR credits there), so the narrator list is its own; the
// placeholder checks below it are shared with the author side.
var narratorJunkNames = map[string]bool{
	"unknown": true, "unknownnarrator": true, "unknownnarrators": true,
	"various": true, "variousnarrators": true, "variousartists": true, "variousreaders": true,
	"na": true, "none": true, "null": true, "tbd": true, "untitled": true,
	"fullcast": true, "cast": true, "castmembers": true, "ensemblecast": true, "ensemble": true,
	"narrator": true, "narrators": true, "thenarrator": true, "reader": true, "readers": true,
	"anonymous": true, "anon": true, "uncredited": true, "multiplenarrators": true,
	"multiplereaders": true, "multicast": true, "audiobook": true, "audiobooks": true,
}

// isJunkNarratorName reports whether name is a stand-in, not a person: a
// narratorJunkNames entry, the "Unknown Author" placeholder, or a name
// authorjunk judges a placeholder.
func isJunkNarratorName(name string) bool {
	if narratorJunkNames[authorjunk.FoldKey(name)] || authorname.IsPlaceholder(name) {
		return true
	}
	return authorjunk.ClassifyName(name).Class == authorjunk.ClassPlaceholder
}

// dropsAuthor reports whether cleaning the column dropped a person: the raw
// split has a person the cleaned list lacks that is not a role credit.
func dropsAuthor(column string, cleaned []string) bool {
	kept := map[string]bool{}
	for _, n := range cleaned {
		kept[util.NormalizeAuthor(n)] = true
	}
	for _, piece := range util.SplitCreditNames(column) {
		ppl, v := util.CleanNarratorCredit(piece, nil)
		if v != util.NarratorCreditPeople {
			continue
		}
		for _, p := range ppl {
			if !kept[util.NormalizeAuthor(p)] {
				return true
			}
		}
	}
	return false
}

func removeClass(classes []string, c string) []string {
	out := classes[:0]
	for _, x := range classes {
		if x != c {
			out = append(out, x)
		}
	}
	return out
}
