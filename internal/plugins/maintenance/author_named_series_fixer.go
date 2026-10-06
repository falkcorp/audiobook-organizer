// file: internal/plugins/maintenance/author_named_series_fixer.go
// version: 1.1.0
// guid: 51518f40-1a71-43e8-bb8c-bb056522bedd
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/foldernames"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// authorNamedSeriesFixerID is the Repairs-lane id of the author-named series
// fixer. It is also the holder name on the series rows it empties
// (database.SeriesHolder).
const authorNamedSeriesFixerID = "maintenance.author-named-series"

// Skip kinds of an author-named series row. A held row is listed with its
// reason and never written.
const (
	ansSkipRealSeries      = "skipped_real_series"
	ansSkipAuthorRowJunk   = "skipped_author_row_junk"
	ansSkipEmptySeries     = "skipped_empty_series"
	ansSkipNoOutsideBooks  = "skipped_no_distinct_outside_books"
	ansSkipNumberedSeries  = "skipped_numbered_series"
	ansSkipApplied         = "skipped_metadata_applied"
	ansSkipNoMatch         = "skipped_marked_no_match"
	ansSkipCoauthored      = "skipped_coauthored"
	ansSkipNotLinked       = "skipped_not_linked"
	ansSkipAuthorChanged   = "skipped_author_changed"
	ansMinNearTitleLetters = 4
)

// authorNamedSeriesFixer unlinks books from a series whose name is just their
// author's name ("Brandon Sanderson" as a series holding Sanderson's books).
// Production (2026-10-06) has 11,713 series rows whose name letters-equals
// an author row's; they break search and identification of easy "Author -
// Series - Title" books (FETCH-QUERY-6).
//
// Which series rows are junk starts from the ONE rule #3775 gave the folder
// parse (foldernames.JudgeSeriesBooks): a series row sharing an author row's
// name is junk only when every book in it is credited to a same-named author
// who also has books outside it. A series that credits anyone else is real
// ("Star Wars"); one whose same-named author has no books elsewhere is a real
// series under a junk AUTHOR row ("Rogue Merchant") and stands. This fixer
// repairs the series side only.
//
// Because it WRITES on that verdict, it asks two more things before a row is
// applicable, and holds the row for review when either fails:
//   - "books outside it" must be real books: at least one live book credited
//     (primary) to the author, outside the series, whose title is not a
//     near-duplicate of a title in the series. The rule counts rows, and a
//     fragment, a second copy or a co-credit of a series book is a row too.
//   - the series must not be numbered: two or more of its books carrying
//     distinct positions. A pen-name house series ("Nick Carter", "Ellery
//     Queen") is a series named after its author, credited to that author,
//     with the author's other books elsewhere, so the rule calls it junk; its
//     numbers are what tell it apart. The authority lists cannot: they know
//     Brandon Sanderson and Nick Carter alike as people and carry no
//     pen-name or house-name flag, so a person check would hold the main
//     case along with the house series. A junk series minted from "Author -
//     01 - Title" folders is numbered too and is held with them; the owner
//     reads those.
//
// A row is one book credited to a same-named author in a series row. It is
// also held when the book's metadata was applied or marked "no match" by the
// owner, when it is co-authored (the rule reads the primary credit, and a
// series naming one of several credited authors is not judged here), or when
// its series name or position is locked. iTunes-owned and Doctor Who / Big
// Finish / Torchwood books are skipped by the framework guards.
//
// Apply first marks the series row held (database.SeriesHolder): a held row
// is never deleted (DeleteSeries refuses it; the orphan prune passes it over),
// because an undo can link a book back only while its series row exists. It
// then clears series_id and series_sequence through the Writer's ModifyBook
// (a compare-and-set against the planned link), whose metadata-history rows
// are written after the write, so "undo last apply" restores both; the two
// fields are then journaled under the apply op, so the op revert restores
// them too. Both undo paths put the position back only into the series it
// was numbered in. After the run, one metadata candidate fetch is enqueued
// for the changed books (AfterApply): fetch only, nothing is applied.
type authorNamedSeriesFixer struct{ p *Plugin }

func newAuthorNamedSeriesFixer(p *Plugin) *authorNamedSeriesFixer {
	return &authorNamedSeriesFixer{p: p}
}

var (
	_ repairs.Fixer        = (*authorNamedSeriesFixer)(nil)
	_ repairs.AfterApplier = (*authorNamedSeriesFixer)(nil)
)

func (f *authorNamedSeriesFixer) ID() string { return authorNamedSeriesFixerID }
func (f *authorNamedSeriesFixer) Title() string {
	return "Series named after the author"
}
func (f *authorNamedSeriesFixer) Description() string {
	return "Books linked to a series whose name is just their author's name (a \"Brandon Sanderson\" series " +
		"holding Sanderson's books), judged by the same rule the folder parse uses: every book in the series is " +
		"by that author, who also has distinct books outside it. One row per book. Real series that share a name " +
		"with an author, series whose author row is the junk side, numbered series (a pen-name house series looks " +
		"like this), co-authored books, locked series and books with applied metadata are listed held. Apply " +
		"removes the series link and position (undoable; the emptied series row is kept for the undo), then " +
		"starts a metadata candidate fetch for the changed books; nothing is applied."
}

// ansState is what Replan needs from plan time: the series the book was
// planned in and the author rows sharing its name.
type ansState struct {
	SeriesID  int   `json:"series_id"`
	AuthorIDs []int `json:"author_ids"`
}

// ansDecision is what Apply writes for one row: the link it expects to clear.
type ansDecision struct {
	bookID   string
	seriesID int
	seq      *int
}

// ansSeries is one series row as a row decision reads it: the verdict and
// the two extra checks, read once per series.
type ansSeries struct {
	series   database.Series
	ids      map[int]bool
	verdict  foldernames.SeriesRowVerdict
	outside  bool // a distinct, primary-credited book outside the series
	numbered bool // two or more distinct positions in the series
}

// Plan judges every series row whose name letters-equals an author row's and
// lists the books credited to that author.
func (f *authorNamedSeriesFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	snap, err := foldernames.Load(store)
	if err != nil {
		return nil, err
	}
	cands := snap.AuthorNamedSeries()
	perSeries := make([][]repairs.Row, len(cands))
	var done, emptied atomic.Int64
	// Each worker writes only perSeries[i] for its own i; a book is in one
	// series, so no two workers produce a row for the same book.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(_ context.Context, i int) error {
		defer done.Add(1)
		rows, empties, err := f.planSeries(cands[i])
		if err != nil {
			s := cands[i].Series
			r := repairs.Row{RowID: "series-" + strconv.Itoa(s.ID), Title: s.Name, Skipped: "error",
				SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = ansFingerprint(r, "error")
			rows = []repairs.Row{r}
		}
		if empties {
			emptied.Add(1)
		}
		perSeries[i] = rows
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Author-named series %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var rows []repairs.Row
	for _, rs := range perSeries {
		rows = append(rows, rs...)
	}
	if rep != nil {
		_ = rep.Log(slog.LevelInfo, fmt.Sprintf("%s: %d series rows share an author's name; applying every applicable row "+
			"would leave at most %d of them with no books. They are kept (held): an undo links books back only to a series row that exists.",
			authorNamedSeriesFixerID, len(cands), emptied.Load()))
	}
	return rows, nil
}

// judgeSeries reads one series row's books and makes the series-level
// decisions every row of it shares.
func (f *authorNamedSeriesFixer) judgeSeries(s database.Series, ids map[int]bool) (ansSeries, []database.BookCore, error) {
	store := f.p.deps.OpsStore()
	books, err := store.GetBooksBySeriesIDCore(s.ID)
	if err != nil {
		return ansSeries{}, nil, fmt.Errorf("books of series %d: %w", s.ID, err)
	}
	verdict, err := foldernames.JudgeSeriesBooks(store, s.ID, books, ids)
	if err != nil {
		return ansSeries{}, nil, err
	}
	js := ansSeries{series: s, ids: ids, verdict: verdict, numbered: ansNumbered(books)}
	if verdict == foldernames.SeriesRowAuthorJunk {
		if js.outside, err = f.distinctOutside(s.ID, ids, books); err != nil {
			return ansSeries{}, nil, err
		}
	}
	return js, books, nil
}

// planSeries returns the rows of one author-named series row, and whether
// every live book in it is an applicable row (applying them all would leave
// the row empty).
func (f *authorNamedSeriesFixer) planSeries(c foldernames.AuthorNamedSeries) ([]repairs.Row, bool, error) {
	js, books, err := f.judgeSeries(c.Series, c.AuthorIDs)
	if err != nil {
		return nil, false, err
	}
	var rows []repairs.Row
	live, applicable := 0, 0
	for _, b := range books {
		if b.IsSoftDeleted() {
			continue
		}
		live++
		if b.AuthorID == nil || !c.AuthorIDs[*b.AuthorID] {
			// A book by someone else is what makes the series real; it is
			// not a row.
			continue
		}
		r, err := f.evaluate(b, js)
		if err != nil {
			r = repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Skipped: "error",
				SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = ansFingerprint(r, "error")
		}
		if r.Applicable() {
			applicable++
		}
		rows = append(rows, r)
	}
	return rows, live > 0 && applicable == live, nil
}

// distinctOutside reports whether a same-named author has a live book,
// primary-credited to them, outside series seriesID, whose title is not a
// near-duplicate of a title in the series (inSeries).
func (f *authorNamedSeriesFixer) distinctOutside(seriesID int, ids map[int]bool, inSeries []database.BookCore) (bool, error) {
	store := f.p.deps.OpsStore()
	keys := make([]string, 0, len(inSeries))
	for _, b := range inSeries {
		if k := personname.LettersKey(b.Title); k != "" {
			keys = append(keys, k)
		}
	}
	authorIDs := make([]int, 0, len(ids))
	for id := range ids {
		authorIDs = append(authorIDs, id)
	}
	sort.Ints(authorIDs)
	for _, id := range authorIDs {
		authored, err := store.GetBooksByAuthorIDCore(id)
		if err != nil {
			return false, fmt.Errorf("books of author %d: %w", id, err)
		}
		for _, b := range authored {
			if b.IsSoftDeleted() || (b.SeriesID != nil && *b.SeriesID == seriesID) ||
				b.AuthorID == nil || !ids[*b.AuthorID] {
				continue
			}
			if !ansNearTitle(personname.LettersKey(b.Title), keys) {
				return true, nil
			}
		}
	}
	return false, nil
}

// ansNearTitle reports whether title key k equals, contains or is contained
// in any of keys (a fragment, a copy or a re-titled edition of a series book).
func ansNearTitle(k string, keys []string) bool {
	if k == "" {
		return true
	}
	for _, o := range keys {
		if k == o {
			return true
		}
		if len(k) >= ansMinNearTitleLetters && len(o) >= ansMinNearTitleLetters &&
			(strings.Contains(k, o) || strings.Contains(o, k)) {
			return true
		}
	}
	return false
}

// ansNumbered reports whether two or more live books of a series carry
// distinct positive positions.
func ansNumbered(books []database.BookCore) bool {
	seen := map[int]bool{}
	for _, b := range books {
		if !b.IsSoftDeleted() && b.SeriesSequence != nil && *b.SeriesSequence > 0 {
			seen[*b.SeriesSequence] = true
		}
	}
	return len(seen) >= 2
}

// Replan re-reads the book, its series and that series' books, and judges
// them again against the author rows the plan named (Row.State), so a row
// whose link, credits, locks or verdict moved comes back with a different
// fingerprint (changed_since_plan).
func (f *authorNamedSeriesFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	var st ansState
	if err := json.Unmarshal(planned.State, &st); err != nil || st.SeriesID == 0 {
		return repairs.Row{}, fmt.Errorf("row %s carries no plan state", planned.RowID)
	}
	held := func(kind, why string) repairs.Row {
		r := repairs.Row{RowID: planned.RowID, BookIDs: []string{planned.RowID}, Title: planned.Title,
			Skipped: kind, SkipReason: why, Reason: why, Risk: repairs.RiskLow, State: planned.State}
		r.Fingerprint = ansFingerprint(r, kind)
		return r
	}
	b, err := store.GetBookByID(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", planned.RowID, err)
	}
	if b == nil || b.IsSoftDeleted() {
		return held(junkSkipGone, "the book no longer exists"), nil
	}
	if b.SeriesID == nil || *b.SeriesID != st.SeriesID {
		return held(ansSkipNotLinked, fmt.Sprintf("the book is no longer in series %d", st.SeriesID)), nil
	}
	ids := make(map[int]bool, len(st.AuthorIDs))
	for _, id := range st.AuthorIDs {
		ids[id] = true
	}
	if b.AuthorID == nil || !ids[*b.AuthorID] {
		return held(ansSkipAuthorChanged, "the book is no longer credited to the author the series is named after"), nil
	}
	s, err := store.GetSeriesByID(st.SeriesID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read series %d: %w", st.SeriesID, err)
	}
	if s == nil {
		return held(ansSkipNotLinked, fmt.Sprintf("series %d no longer exists", st.SeriesID)), nil
	}
	js, _, err := f.judgeSeries(*s, ids)
	if err != nil {
		return repairs.Row{}, err
	}
	return f.evaluate(b.Core(), js)
}

// evaluate builds the row of book b in the judged series js.
func (f *authorNamedSeriesFixer) evaluate(b database.BookCore, js ansSeries) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	s, ids := js.series, js.ids
	credits, err := store.GetBookAuthors(b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read credits of %s: %w", b.ID, err)
	}
	locked, err := database.LockedUserFields(store, b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read field locks of %s: %w", b.ID, err)
	}
	authorName := ""
	if b.AuthorID != nil {
		a, aerr := store.GetAuthorByID(*b.AuthorID)
		if aerr != nil {
			return repairs.Row{}, fmt.Errorf("read author %d: %w", *b.AuthorID, aerr)
		}
		if a != nil {
			authorName = a.Name
		}
	}
	authorIDs := make([]int, 0, len(ids))
	for id := range ids {
		authorIDs = append(authorIDs, id)
	}
	sort.Ints(authorIDs)
	state, err := json.Marshal(ansState{SeriesID: s.ID, AuthorIDs: authorIDs})
	if err != nil {
		return repairs.Row{}, err
	}
	seq := intPtrString(b.SeriesSequence)
	r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Author: authorName,
		Class: js.verdict.String(), Risk: repairs.RiskLow, State: state,
		Current: map[string]string{"series": s.Name, "series_id": strconv.Itoa(s.ID), "series_sequence": seq},
		Evidence: []string{
			fmt.Sprintf("series row %d %q has the name of author row(s) %v", s.ID, s.Name, authorIDs),
			"the book is credited to that author (author row " + intPtrString(b.AuthorID) + ")",
		},
	}
	var others []int
	creditIDs := make([]int, 0, len(credits))
	for _, c := range credits {
		creditIDs = append(creditIDs, c.AuthorID)
		if !ids[c.AuthorID] && !slices.Contains(others, c.AuthorID) {
			others = append(others, c.AuthorID)
		}
	}
	sort.Ints(creditIDs)
	status := ""
	if b.MetadataReviewStatus != nil {
		status = *b.MetadataReviewStatus
	}
	lockSeries, lockPos := locked[database.FieldKeySeriesName], locked[database.FieldKeySeriesPosition]
	switch {
	case js.verdict == foldernames.SeriesRowReal:
		r.Skipped, r.SkipReason = ansSkipRealSeries,
			"the series also holds books credited to other authors: it is a real series that shares the author's name"
	case js.verdict == foldernames.SeriesRowAuthorRowJunk:
		r.Skipped, r.SkipReason = ansSkipAuthorRowJunk,
			"the author has no books outside this series: the series stands and the author row is the junk side, which this fixer does not repair"
	case js.verdict != foldernames.SeriesRowAuthorJunk:
		r.Skipped, r.SkipReason = ansSkipEmptySeries, "the series holds no books to judge"
	case !js.outside:
		r.Skipped, r.SkipReason = ansSkipNoOutsideBooks,
			"the author's books outside the series are only copies, fragments or co-credits of the series' own books; nothing shows the series is just the author's name"
		r.Risk = repairs.RiskReview
	case js.numbered:
		r.Skipped, r.SkipReason = ansSkipNumberedSeries,
			"the series' books carry their own numbers; a pen-name house series (Nick Carter, Ellery Queen) looks like this, so it is held for review"
		r.Risk = repairs.RiskReview
	case database.MetadataApplied(b.MetadataReviewStatus):
		r.Skipped, r.SkipReason = ansSkipApplied, "the book's metadata was applied; its series is the owner's"
	case status == "no_match":
		r.Skipped, r.SkipReason = ansSkipNoMatch, "the book is marked \"no match\" by the owner"
	case len(others) > 0:
		r.Skipped, r.SkipReason = ansSkipCoauthored, fmt.Sprintf(
			"the book is co-authored (author rows %v besides the series' name); a series naming one of several credited authors is held for review", others)
		r.Risk = repairs.RiskReview
	case lockSeries || lockPos:
		r.Skipped, r.SkipReason = junkSkipUserLocked, "the series is locked; it is never rewritten"
	}
	if r.Skipped != "" {
		r.Reason = r.SkipReason
	} else {
		r.Evidence = append(r.Evidence, "every book in the series is by that author, who also has distinct books outside it")
		r.Reason = fmt.Sprintf("the series %q is the author's name, not a series; the link and position are removed", s.Name)
		r.Proposed = map[string]string{"series": "", "series_id": "", "series_sequence": ""}
		r.Detail = &ansDecision{bookID: b.ID, seriesID: s.ID, seq: copyIntPtr(b.SeriesSequence)}
	}
	// Only values a sibling row's apply cannot move: unlinking a sibling only
	// adds books outside the series and removes positions from it, so
	// outside stays true and numbered stays false once they are.
	extra := strings.Join([]string{strconv.Itoa(s.ID), s.Name, seq, intPtrString(b.AuthorID), fmt.Sprint(creditIDs),
		status, strconv.FormatBool(lockSeries), strconv.FormatBool(lockPos),
		strconv.FormatBool(js.outside), strconv.FormatBool(js.numbered)}, "\n")
	r.Fingerprint = ansFingerprint(r, extra)
	return r, nil
}

// ansFingerprint hashes a row's decision. It holds nothing a sibling row's
// apply changes (no book counts), so applying one book of a series leaves
// the others' rows applicable.
func ansFingerprint(r repairs.Row, extra string) string {
	sum := sha256.Sum256([]byte(r.RowID + "\n" + r.Class + "\n" + r.Skipped + "\n" + extra))
	return hex.EncodeToString(sum[:])[:32]
}

// errNoSeriesHolder: the store cannot hold a series row, so the row is not
// written (its undo could be lost to the orphan prune).
var errNoSeriesHolder = errors.New("the store cannot hold series rows")

// Apply holds the series row, then clears the book's series link and
// position.
//
// The hold comes first: a hold set after the unlink leaves a moment in which
// the orphan prune may delete the emptied row, and the undo could never link
// the book back. The hold is a marker, not a claimed change, so setting it
// before the write breaks no ledger rule; a hold that cannot be set refuses
// the row.
//
// Unlike the reparse fixer's series write (RecordChange, then Modify), the
// undo rows are written AFTER the write they describe: the metadata-history
// rows by Writer.Modify itself, the op-journal rows below once Modify has
// committed. A journal row for a write that never happened is the
// ledger-before-write bug class; a journal failure after the write is
// reported partially_applied, and "undo last apply" still restores the book
// from its history rows.
func (f *authorNamedSeriesFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*ansDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", authorNamedSeriesFixerID, fresh.RowID)
	}
	store := f.p.deps.OpsStore()
	holder := database.AsSeriesHolder(store)
	if holder == nil {
		return fmt.Errorf("%s: row %s: %w", authorNamedSeriesFixerID, fresh.RowID, errNoSeriesHolder)
	}
	if err := holder.HoldSeries(d.seriesID, authorNamedSeriesFixerID); err != nil {
		return fmt.Errorf("%s: row %s: hold series %d: %w", authorNamedSeriesFixerID, fresh.RowID, d.seriesID, err)
	}
	var prevSID, prevSeq *int
	changed, err := w.Modify(d.bookID, func(b *database.Book) error {
		if b.SeriesID == nil || *b.SeriesID != d.seriesID || !sameIntPtr(b.SeriesSequence, d.seq) {
			return fmt.Errorf("%w: book %s series changed", repairs.ErrChangedSincePlan, d.bookID)
		}
		locked, lerr := database.LockedUserFields(store, d.bookID)
		if lerr != nil {
			return fmt.Errorf("read field locks of %s: %w", d.bookID, lerr)
		}
		if locked[database.FieldKeySeriesName] || locked[database.FieldKeySeriesPosition] {
			return fmt.Errorf("%w: the series is now locked", repairs.ErrChangedSincePlan)
		}
		// Read under the write stripe, copied: the pointers belong to the row.
		prevSID, prevSeq = copyIntPtr(b.SeriesID), copyIntPtr(b.SeriesSequence)
		b.SeriesID, b.SeriesSequence = nil, nil
		return nil
	})
	if err != nil {
		return err
	}
	if !slices.Contains(changed, "series_id") {
		return fmt.Errorf("book %s: the write committed but recorded no series change", d.bookID)
	}
	if jerr := w.Journal(d.bookID, "metadata_update", "series_id", intPtrString(prevSID), ""); jerr != nil {
		return fmt.Errorf("%w: book %s unlinked from series %d but not journaled: %w", repairs.ErrPartiallyApplied, d.bookID, d.seriesID, jerr)
	}
	if prevSeq != nil {
		if jerr := w.Journal(d.bookID, "metadata_update", "series_sequence", intPtrString(prevSeq), ""); jerr != nil {
			return fmt.Errorf("%w: book %s position %d cleared but not journaled: %w", repairs.ErrPartiallyApplied, d.bookID, *prevSeq, jerr)
		}
	}
	return nil
}

// errNoEnqueuer: the plugin's deps cannot start an operation.
var errNoEnqueuer = errors.New("no operation enqueuer")

// AfterApply enqueues one metadata candidate fetch for the books the apply
// unlinked. It is NOT forced: the batch fetch neither searches nor scores by
// the book's series row (it passes no series, and scoring reads the series
// parsed from the title), so a forced fetch would re-ask the providers the
// same question and spend their daily budgets. Unforced, it fetches the
// books that have no current answer and serves the rest from the cache. The
// fetch only stores candidates for review; nothing is applied.
func (f *authorNamedSeriesFixer) AfterApply(ctx context.Context, bookIDs []string) (string, error) {
	if f.p.deps == nil {
		return "", errNoEnqueuer
	}
	opID, err := f.p.deps.EnqueueOp(ctx, metabatch.CandidateFetchDefID, metabatch.FetchOpParams{
		BookIDs: bookIDs, TotalBooks: len(bookIDs),
	})
	if err != nil {
		return "", fmt.Errorf("enqueue %s for %d books: %w", metabatch.CandidateFetchDefID, len(bookIDs), err)
	}
	return opID, nil
}

// copyIntPtr returns a copy of the int p points at, or nil.
func copyIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
