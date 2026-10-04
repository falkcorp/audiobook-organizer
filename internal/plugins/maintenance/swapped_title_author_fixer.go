// file: internal/plugins/maintenance/swapped_title_author_fixer.go
// version: 1.8.1
// guid: a80ddfb1-95dc-402f-941a-142b9388bcf0
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// swappedFixerID is the Repairs-lane id of the swapped title/author fixer.
const swappedFixerID = "maintenance.repair-swapped-title-author"

// Skip kinds of the swapped title/author fixer. The fragment, needs-manual,
// user-locked and gone kinds are the junk-title fixer's own, so the two
// fixers' rows read the same in the Repairs tab.
const (
	// swapSkipNoProviderAuthor: the swap is evident (the stored author is the
	// title a provider recorded) but no provider recorded who wrote the book,
	// so there is no author to move in. Listed for the owner.
	swapSkipNoProviderAuthor = "skipped_no_provider_author"
	// swapSkipMultiAuthor: the provider's author credit lists several names
	// the shared splitter (dedup.SplitCompositeAuthorName) will not separate,
	// or carries contributor roles ("Philip Parker - translator"): which of
	// them are authors is a person's call.
	swapSkipMultiAuthor = "skipped_multi_author"
	// swapSkipImplausibleAuthor: a provider author name fails the shared
	// author-creation gate (personname.PrepareAuthorNameForCreation).
	swapSkipImplausibleAuthor = "skipped_implausible_author"
	// swapSkipAmbiguousAuthor: no author has the provider's spelling, and
	// two or more existing authors are spelled like it by their letters and
	// digits ("J.N. Chaney", "J N Chaney"): which one is meant is a person's
	// call, and minting a third would add another duplicate.
	swapSkipAmbiguousAuthor = "skipped_ambiguous_author"
	// swapSkipNotSwapped: what a re-plan reports for a book that no longer
	// has the swapped shape (its title is no longer junk, or its author is
	// no longer the provider's title). A plan never lists one.
	swapSkipNotSwapped = "skipped_not_swapped"
	// swapSkipRelinkSeriesFirst: the stored book row still embeds a series
	// object its SeriesID does not name (SeriesID nil, or another series):
	// a stale row older builds left. Any write of the row drops that object
	// (the store's series invariant; the drop is recorded as a series_object
	// history row), and the stale-series relink reads the live object to
	// offer the series back, so this fixer does not write the book until the
	// owner has chosen the series. A row with SeriesID nil is
	// the relink fixer's (maintenance.relink-stale-series); one whose
	// SeriesID names another series is not, and a person decides.
	swapSkipRelinkSeriesFirst = "skipped_relink_series_first"
)

// swapIndexTTL bounds how long Replan reuses the author index during an apply.
const swapIndexTTL = 2 * time.Minute

// swapRoleRe is a contributor role written into an author credit
// ("Philip Parker - translator", "Gardner Dozois - editor", "Nathan Klausner
// -translated by"). A credit carrying one names people who are not authors.
var swapRoleRe = regexp.MustCompile(`(?i)(?:\s-\s*|\s*\(\s*)(?:translat\w*|editor|edited\b|illustrat\w*|foreword|introduction|narrat\w*|read by|contributor|adapt\w*)`)

// swappedTitleAuthorFixer repairs books whose title and author were stored
// transposed: the stored TITLE is a narrator credit or other junk ("read by
// Jack Voraces") and the stored AUTHOR is the book's real title ("Ultimate
// Level 1_ Divine Creation"), while a metadata provider recorded both the real
// title and the real author in the book's field state. 564 books on prod had
// the shape on 2026-10-03, 390 of them with a provider author on record.
//
// Owner decision 2026-10-03: for a book with a provider author, the title
// becomes the provider title and the author becomes the provider author;
// every other book is listed. iTunes books get their DATABASE rows fixed
// (ITunesDatabaseOnly): this fixer writes no file, no tag and no iTunes id
// anywhere. Doctor Who / Big Finish / Torchwood stays the owner's.
//
// The author record that held the title is never deleted or renamed: the
// book's credit moves off it (the junk-author fixer's journaled credit move,
// reused with its revert), and an emptied record is left for
// maintenance.purge-empty-authors. A series created under that record keeps
// it as its database.Series.AuthorID: this fixer does not re-point series, so
// such a series stays tied to a title-named author, and a purge of the
// emptied record can leave that AuthorID dangling.
//
// Order with the junk-title fixer: this fixer runs first. The junk-title
// fixer skips a book whose author is the provider's title
// (skipped_swapped_title_author) instead of retitling it, and a book it
// retitled before that guard existed (title real, author still the title)
// is planned here in the author-only shape: same author resolution and
// credit move, no title write.
//
// Locks: the title (writeTitleOnly) and the author are locked after they are
// written, so a forced rescan cannot write the file tags' swapped values
// back (scanner.applyScannerFields honours the lock).
//
// Undo: the credit move, any author created, the title and both locks are
// journaled in the apply op's journal, so POST /operations/<id>/revert undoes
// the row. "Undo last apply" refuses these batches (apply_op_journaled):
// reverting the title from history alone would leave the credits moved.
type swappedTitleAuthorFixer struct {
	p *Plugin
	// junk supplies the junk-title index, the fragment test and the author
	// name cache; its Plan is never run from here.
	junk *junkTitleFixer

	authMu      sync.Mutex
	auth        *swapAuthorIndex
	authBuiltAt time.Time

	// mintMu serializes the create path of resolveAuthor, and minted maps the
	// letters-and-digits key of every author this process created to its id.
	// MintAuthor only serializes IDENTICAL names: without this, two rows of
	// one apply bringing "Jo-Ann Pike" and "Jo Ann Pike" (neither in the
	// plan-time author index) would each mint their own author.
	mintMu sync.Mutex
	minted map[string]int
}

func newSwappedTitleAuthorFixer(p *Plugin) *swappedTitleAuthorFixer {
	return &swappedTitleAuthorFixer{p: p, junk: newJunkTitleFixer(p), minted: map[string]int{}}
}

var (
	_ repairs.Fixer              = (*swappedTitleAuthorFixer)(nil)
	_ repairs.ITunesDatabaseOnly = (*swappedTitleAuthorFixer)(nil)
)

func (f *swappedTitleAuthorFixer) ID() string    { return swappedFixerID }
func (f *swappedTitleAuthorFixer) Title() string { return "Swapped title and author" }
func (f *swappedTitleAuthorFixer) Description() string {
	return "Books whose title is a narrator credit or other junk (\"read by Jack Voraces\") while the author field " +
		"holds the book's real title, and a metadata provider recorded the real title and author. Sets the title " +
		"to the provider's title and credits the provider's author (an existing author with that name, or a new " +
		"one), then locks the title and author so a rescan cannot restore the file tags' swapped values. Run it " +
		"BEFORE the junk-title fixer (maintenance.repair-junk-titles), which lists these books for this one; a book " +
		"that fixer already retitled gets its author fixed here, title untouched. A title of one or two words needs " +
		"a narrator, ASIN, runtime or author folder that matches the provider record. Books with no provider author " +
		"on record, fragments, multi-author credits the splitter cannot separate (or of three names or more) and " +
		"user-locked titles or authors are listed, not changed. iTunes books get database fields only; no file is " +
		"touched. Undo with the apply operation's revert."
}

// ITunesDatabaseOnly: the owner cleared this fixer to fix the database rows
// of books under books/itunes/** (2026-10-03). It writes no file and no tag.
func (f *swappedTitleAuthorFixer) ITunesDatabaseOnly() bool { return true }

// swapAuthorIndex groups every author by the letters and digits of the name,
// so a provider spelling ("J.N. Chaney") finds an existing row spelled with
// other punctuation or spacing.
type swapAuthorIndex struct {
	byKey map[string][]database.Author
	// names maps every author id to its name, for picking the author-only
	// candidates (a real title whose author holds that same title).
	names map[int]string
}

func (f *swappedTitleAuthorFixer) buildAuthorIndex() (*swapAuthorIndex, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	all, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("GetAllAuthors: %w", err)
	}
	idx := &swapAuthorIndex{byKey: make(map[string][]database.Author, len(all)), names: make(map[int]string, len(all))}
	for i := range all {
		idx.names[all[i].ID] = all[i].Name
		if k := junkLettersKey(all[i].Name); k != "" {
			idx.byKey[k] = append(idx.byKey[k], all[i])
		}
	}
	return idx, nil
}

func (f *swappedTitleAuthorFixer) cachedAuthorIndex() (*swapAuthorIndex, error) {
	f.authMu.Lock()
	defer f.authMu.Unlock()
	if f.auth != nil && time.Since(f.authBuiltAt) < swapIndexTTL {
		return f.auth, nil
	}
	idx, err := f.buildAuthorIndex()
	if err != nil {
		return nil, err
	}
	f.auth, f.authBuiltAt = idx, time.Now()
	return idx, nil
}

// variants returns the authors spelled like name by letters and digits, other
// than the row excluded (the author record that holds the title).
func (idx *swapAuthorIndex) variants(name string, exclude int) []database.Author {
	var out []database.Author
	for _, a := range idx.byKey[junkLettersKey(name)] {
		if a.ID != exclude {
			out = append(out, a)
		}
	}
	return out
}

// Plan lists every junk-titled book whose stored author is the title a
// provider recorded for it, and every book with a real title whose author is
// still that same title (the author-only shape the junk-title fixer leaves
// when it writes the title first): applicable when the provider also
// recorded a usable author, held otherwise.
func (f *swappedTitleAuthorFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	idx, cands, all, err := f.junk.buildJunkIndexSnapshot()
	if err != nil {
		return nil, err
	}
	f.junk.idxMu.Lock()
	f.junk.idx, f.junk.idxBuiltAt = idx, time.Now()
	f.junk.idxMu.Unlock()
	aidx, err := f.buildAuthorIndex()
	if err != nil {
		return nil, err
	}
	f.authMu.Lock()
	f.auth, f.authBuiltAt = aidx, time.Now()
	f.authMu.Unlock()
	cands = append(cands, swapAuthorOnlyCandidates(all, aidx)...)

	rows := make([]repairs.Row, len(cands))
	keep := make([]bool, len(cands))
	var done atomic.Int64
	// Each worker writes only rows[i] and keep[i] for its own i; idx and
	// aidx are read-only here apart from the junk fixer's author-name cache,
	// which authorName locks. journal is this plan's memo of operation
	// journals (the continuation fallback), locked internally.
	journal := newSwapOpJournalMemo()
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(_ context.Context, i int) error {
		defer done.Add(1)
		r, swapped, err := f.evaluate(idx, aidx, journal, cands[i])
		if err != nil {
			r = repairs.Row{RowID: cands[i].ID, BookIDs: []string{cands[i].ID}, Title: cands[i].Title,
				Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = fingerprintStrings("error", r.RowID, err.Error())
			swapped = true
		}
		rows[i], keep[i] = r, swapped
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Swapped titles %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([]repairs.Row, 0, len(rows))
	for i := range rows {
		if keep[i] {
			out = append(out, rows[i])
		}
	}
	// Two applicable rows giving the same title to the same author are most
	// likely two copies of one book. Correcting both is still correct (it is
	// what lets dedup find them), so neither is held; the reason says so.
	// Only the reason changes, never the fingerprint: a sibling row's apply
	// must not move this row's.
	same := map[string][]int{}
	for i := range out {
		if out[i].Applicable() && out[i].Proposed != nil {
			k := junkLettersKey(out[i].Proposed["title"]) + "|" + junkLettersKey(out[i].Proposed["author"])
			same[k] = append(same[k], i)
		}
	}
	for _, ix := range same {
		if len(ix) < 2 {
			continue
		}
		for _, i := range ix {
			out[i].Reason += fmt.Sprintf("; %d books in this plan get this same title and author (likely copies of one book)", len(ix))
		}
	}
	return out, nil
}

// swapAuthorOnlyCandidates picks, from one listing of every book, the live
// books whose title is not junk and whose author's name is that title by its
// letters and digits. A map lookup per book; evaluate reads the rest.
func swapAuthorOnlyCandidates(all []database.BookCore, aidx *swapAuthorIndex) []database.BookCore {
	var out []database.BookCore
	for i := range all {
		b := &all[i]
		if b.IsSoftDeleted() || b.AuthorID == nil {
			continue
		}
		k := junkLettersKey(b.Title)
		if k == "" || authorCoreKey(aidx.names[*b.AuthorID]) != k {
			continue
		}
		if metadata.ClassifyJunkTitleFor(b.Title, narratorsOf(b.Narrator)) != metadata.JunkNone {
			continue // already a junk-titled candidate
		}
		out = append(out, *b)
	}
	return out
}

// Replan re-reads the book and decides it again. It always returns
// planned.RowID: a book that is gone or no longer swapped comes back as a
// skipped row whose fingerprint differs, so the engine reports
// changed_since_plan, and a re-run of an interrupted apply passes over the
// rows it already wrote.
func (f *swappedTitleAuthorFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	b, err := store.GetBookByID(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", planned.RowID, err)
	}
	if b == nil || b.IsSoftDeleted() {
		r := repairs.Row{RowID: planned.RowID, BookIDs: []string{planned.RowID}, Skipped: junkSkipGone,
			SkipReason: "the book no longer exists", Risk: repairs.RiskLow}
		r.Fingerprint = fingerprintStrings("gone", planned.RowID)
		return r, nil
	}
	idx, err := f.junk.cachedIndex()
	if err != nil {
		return repairs.Row{}, err
	}
	aidx, err := f.cachedAuthorIndex()
	if err != nil {
		return repairs.Row{}, err
	}
	// No journal memo: a re-plan reads the journal fresh (a row voided or
	// reverted since the plan must be seen).
	r, _, err := f.evaluate(idx, aidx, nil, b.Core())
	return r, err
}

// swapDecision travels from Replan to Apply in Row.Detail.
type swapDecision struct {
	bookID      string
	oldTitle    string
	newTitle    string
	oldAuthorID int
	// names are the provider's author names, cleaned by the creation gate,
	// in credit order.
	names []string
	// credits is the book's credit junction as the decision read it; the
	// apply writes only while the book still has exactly this.
	credits []database.BookAuthor
	// creditsDone: an interrupted apply already moved the credits (every
	// provider author is credited as an author and the title record is not)
	// but not the primary author. The apply sets the primary only.
	creditsDone bool
	// continuation: the author carries a repair's lock, which only this
	// fixer sets, first thing in its apply: an apply of it was cut short
	// here, after its plan had already passed every check. The strong-tie
	// requirement is waived for it (the title write can destroy the tie it
	// rested on, a narrator named by a "read by X" title).
	continuation bool
}

// evaluate decides one book. Plan and Replan both call it, so a row planned
// and a row re-planned from the same state carry the same fingerprint.
// swapped is false for a book without the swapped shape: Plan leaves it out.
//
// Nothing that another row's apply changes goes into the fingerprint: whether
// an author with the provider's name already exists flips when a sibling row
// creates it, so the fingerprint carries the names, never their ids.
func (f *swappedTitleAuthorFixer) evaluate(idx *junkIndex, aidx *swapAuthorIndex, journal *swapOpJournalMemo, b database.BookCore) (repairs.Row, bool, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, false, fmt.Errorf("database not initialized")
	}
	author := f.junk.authorName(idx, b.AuthorID)
	oldAuthorID := 0
	if b.AuthorID != nil {
		oldAuthorID = *b.AuthorID
	}
	r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Author: author, Risk: repairs.RiskReview,
		Current: map[string]string{"title": b.Title, "author": author}}
	if oldAuthorID != 0 {
		r.Current["author_id"] = strconv.Itoa(oldAuthorID)
	}
	// fp holds every input of the decision; finish and the applicable path
	// both hash it with the outcome.
	fp := []string{b.ID, b.Title, strconv.Itoa(oldAuthorID), author}
	finish := func(skip, why string, swapped bool) (repairs.Row, bool, error) {
		r.Skipped, r.SkipReason = skip, why
		if r.Reason == "" {
			r.Reason = why
		}
		r.Proposed, r.Detail = nil, nil
		r.Fingerprint = fingerprintStrings(append(fp, "skip", skip, why)...)
		return r, swapped, nil
	}

	ak := junkLettersKey(author)
	// akCore drops a bracketed edition marker from the stored author ("...
	// (Unabridged)"), which the title it becomes does not carry.
	akCore := authorCoreKey(author)
	narrators := narratorsOf(b.Narrator)
	kind := metadata.ClassifyJunkTitleFor(b.Title, narrators)
	// authorOnly: the title is no longer junk (the junk-title fixer, or a
	// person, already gave the book its real title) but the author still
	// holds that same title. Only the author is repaired.
	authorOnly := false
	if kind == metadata.JunkNone {
		if ak == "" || (ak != junkLettersKey(b.Title) && akCore != junkLettersKey(b.Title)) {
			return finish(swapSkipNotSwapped, "the title is no longer junk", false)
		}
		authorOnly = true
	}
	states, err := store.GetMetadataFieldStates(b.ID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read field states of %s: %w", b.ID, err)
	}
	// A book whose state is only in the pre-migration blob has no rows, so
	// no provider title here and never the swapped shape; the blob is
	// migrated by LockFields for the books the junk-title and letter-l
	// fixers write (which hold an unreadable blob, legacyStateUnreadable).
	fetched := swapFetchedValues(states)
	fetchedTitle, fetchedAuthor := fetched["title"].str, fetched["author_name"].str
	mode := string(kind)
	if authorOnly {
		mode = "author-only"
	}
	fp = append(fp, mode, fetchedTitle, fetchedAuthor)

	// The swapped shape: the stored author IS the provider's title, by its
	// letters and digits ("Ultimate Level 1_ Divine Creation" is "Ultimate
	// Level 1: Divine Creation"; a folder or tag cannot hold the colon). A
	// catalog title's bracketed edition marker is dropped first, as the
	// junk-title fixer does.
	catalog := strings.TrimSpace(catalogEditionRe.ReplaceAllString(fetchedTitle, ""))
	if ak == "" || fetchedTitle == "" || (ak != junkLettersKey(fetchedTitle) && ak != junkLettersKey(catalog)) {
		// A near miss is listed, never written: the stored author is the
		// provider's title without its subtitle, or with one the provider
		// lacks ("Ultimate Level 1" against "Ultimate Level 1: Divine
		// Creation"). It looks swapped, but which title is right is a
		// person's call. Anything further off is not this fixer's book. A
		// book whose title is already real is listed only on an exact match.
		if !authorOnly && ak != "" && fetchedTitle != "" && titleAgreesWithAny(author, []string{fetchedTitle, catalog}) {
			r.Reason = fmt.Sprintf("the title %q is junk (%s) and the author %q is close to the title a provider recorded (%q)",
				b.Title, kind, author, fetchedTitle)
			return finish(junkSkipNeedsManual, fmt.Sprintf(
				"the author %q is not exactly the provider's title %q (a subtitle differs); a person decides", author, fetchedTitle), true)
		}
		return finish(swapSkipNotSwapped, "the stored author is not the title a provider recorded for this book", false)
	}
	if authorOnly {
		r.Reason = fmt.Sprintf("the title %q is already the title a provider recorded, and the author %q is that same title", b.Title, author)
		r.Evidence = []string{
			fmt.Sprintf("stored author %q equals the stored title %q and the provider title %q by letters and digits", author, b.Title, fetchedTitle),
		}
	} else {
		r.Reason = fmt.Sprintf("the title %q is junk (%s) and the author %q is the title a provider recorded (%q)",
			b.Title, kind, author, fetchedTitle)
		r.Evidence = []string{
			fmt.Sprintf("stored title %q is junk (%s)", b.Title, kind),
			fmt.Sprintf("stored author %q equals the provider title %q by letters and digits", author, fetchedTitle),
		}
	}
	if fetchedAuthor != "" {
		r.Evidence = append(r.Evidence, fmt.Sprintf("provider author %q", fetchedAuthor))
	}

	for _, s := range []string{b.Title, author, fetchedTitle, fetchedAuthor} {
		if s != "" && junkTitleOwnerManual("", s, "") {
			return finish(repairs.SkipOwnerManual, fmt.Sprintf(
				"%q marks Big Finish / Doctor Who / Torchwood content; owner applies these by hand", s), true)
		}
	}

	// A real author is never a misplaced title. The record the title would
	// sit in credits other real titles ("Edgar Allan Poe" also credits "The
	// Raven"): the provider record titled with the person's name is then a
	// book ABOUT them (a biography, a collection), and moving the credit
	// would rewrite a real author's book. Distinct titles are counted, not
	// books, so copies of one book do not count.
	if others := otherTitles(idx, oldAuthorID, ak, akCore); len(others) > 0 {
		return finish(junkSkipNeedsManual, fmt.Sprintf(
			"the author record %q also credits %d other title(s) (%s): it is a real author, not a misplaced title; a person decides",
			author, len(others), strings.Join(others, "; ")), true)
	}
	// Self-read: the junk title names the stored author as its reader ("read
	// by T. S. Eliot" by T. S. Eliot), so the author field holds a person,
	// not a title. The provider record titled with that name is about them.
	if !authorOnly {
		readers := narrators
		if n, ok := metadata.NarratorCreditName(b.Title); ok {
			readers = append([]string{n}, readers...)
		}
		for _, n := range readers {
			if junkLettersKey(n) == ak {
				return finish(junkSkipNeedsManual, fmt.Sprintf(
					"the book is read by its stored author %q, so the author is a person, not a misplaced title; a person decides", n), true)
			}
		}
	}

	// Locks. A person's lock or override always holds the row. A repair's
	// lock holds a field this row would write (the title of a full row);
	// one on a field this row does not write (the junk-title fixer's title
	// lock on an author-only row), or on the author of a book an interrupted
	// apply of this fixer already locked, does not.
	titleState, authorState := fieldStateOf(states, database.FieldKeyTitle), fieldStateOf(states, database.FieldKeyAuthorName)
	locks, err := database.LoadFieldLocks(store, b.ID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read field locks of %s: %w", b.ID, err)
	}
	if !authorOnly && locks.Locked(database.FieldKeyTitle) {
		kind, why := lockHold(titleState, "title")
		return finish(kind, why, true)
	}
	if locks.Locked(database.FieldKeyAuthorName) && !locks.RepairLocked(database.FieldKeyAuthorName) {
		kind, why := lockHold(authorState, "author")
		return finish(kind, why, true)
	}
	// A stale embedded series is held before anything is written (see
	// swapSkipRelinkSeriesFirst). Read here, once the book has the swapped
	// shape, not for every book in the library.
	if skip, why, err := swapStaleSeriesHold(store, b.ID); err != nil {
		return repairs.Row{}, false, err
	} else if skip != "" {
		return finish(skip, why, true)
	}
	continuation, contOp, err := swapContinuation(store, journal, b.ID, authorState, fetched)
	if err != nil {
		return repairs.Row{}, false, err
	}

	files, err := store.GetBookFiles(b.ID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read files of %s: %w", b.ID, err)
	}
	var paths []string
	for i := range files {
		if p := strings.TrimSpace(files[i].FilePath); p != "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 && b.FilePath != "" {
		paths = []string{b.FilePath}
	}
	// A chapter file of a bigger book is not retitled. The author is passed
	// as "" on purpose: fragmentReason's "author named like its folder" test
	// is the shape of a chapter parsed as its own book, but on this shape the
	// author IS the title, and a book filed in a folder named after its title
	// would match it on every row. An author-only row was already through
	// the junk-title fixer's fragment check when its title was written, and
	// its title is not junk, which is what that check reads.
	if !authorOnly {
		skip, why, err := f.junk.fragmentReason(idx, b, kind, "", paths)
		if err != nil {
			return repairs.Row{}, false, err
		}
		switch skip {
		case junkSkipFragment:
			return finish(skip, "fragment — use the consolidation fixer: "+why, true)
		case junkSkipPossibleFragment:
			return finish(skip, "possible fragment: "+why, true)
		case junkSkipNeedsManual:
			return finish(skip, why, true)
		}
	}

	if fetchedAuthor == "" {
		return finish(swapSkipNoProviderAuthor, "no provider author on record", true)
	}
	if authorname.IsPlaceholderAuthor(fetchedAuthor) {
		return finish(swapSkipNoProviderAuthor, fmt.Sprintf("the provider author on record is a placeholder (%q)", fetchedAuthor), true)
	}
	fk := junkLettersKey(fetchedAuthor)
	if authorOnly && fk == ak {
		// A real title whose author really is named like it ("Madonna" by
		// Madonna), and the provider agrees: nothing is swapped.
		return finish(swapSkipNotSwapped, "the provider author is named like the title too", false)
	}
	if fk == junkLettersKey(catalog) || fk == junkLettersKey(b.Title) || fk == ak {
		return finish(junkSkipNeedsManual, fmt.Sprintf("the provider author %q is the title itself", fetchedAuthor), true)
	}
	// Fetched values are merged field by field (a later fetch whose record
	// has a title but no author replaces the title and leaves the older
	// record's author), so the two must have been recorded together to be
	// one record's title and author.
	if gap := swapStateGap(fetched["title"], fetched["author_name"]); gap > swapSameRecordWindow {
		return finish(junkSkipNeedsManual, fmt.Sprintf(
			"the provider title and author were recorded %s apart, so they may come from different provider records; a person decides",
			gap.Round(time.Second)), true)
	}

	newTitle := ""
	if !authorOnly {
		newTitle = catalog
		if len([]rune(newTitle)) < 2 || metadata.ClassifyJunkTitleFor(newTitle, narrators) != metadata.JunkNone {
			return finish(junkSkipNeedsManual, fmt.Sprintf("the provider title %q is junk itself", fetchedTitle), true)
		}
	}

	names, skip, why := swapAuthorNames(fetchedAuthor)
	if skip != "" {
		return finish(skip, why, true)
	}

	// Display only (never fingerprinted): which existing author each name
	// resolves to, or that it will be created.
	display := make([]string, 0, len(names))
	for _, n := range names {
		existing, err := store.GetAuthorByName(n)
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("read author %q: %w", n, err)
		}
		if existing != nil && existing.ID != oldAuthorID {
			display = append(display, existing.Name)
			continue
		}
		switch vs := aidx.variants(n, oldAuthorID); len(vs) {
		case 0:
			display = append(display, n+" (new author)")
		case 1:
			display = append(display, vs[0].Name)
			r.Evidence = append(r.Evidence, fmt.Sprintf("provider spelling %q resolves to the existing author %q (id %d)", n, vs[0].Name, vs[0].ID))
		default:
			var spell []string
			for _, v := range vs {
				spell = append(spell, fmt.Sprintf("%q (id %d)", v.Name, v.ID))
			}
			return finish(swapSkipAmbiguousAuthor, fmt.Sprintf("no author is named %q and %d existing authors are spelled like it: %s",
				n, len(vs), strings.Join(spell, ", ")), true)
		}
	}

	credits, err := store.GetBookAuthors(b.ID)
	if err != nil {
		// Fail closed: the credit move is a compare-and-set against this list.
		return repairs.Row{}, false, fmt.Errorf("read credits of %s: %w", b.ID, err)
	}
	var creditKeys []string
	holds := false
	for _, ba := range credits {
		creditKeys = append(creditKeys, fmt.Sprintf("%d:%s:%d", ba.AuthorID, ba.Role, ba.Position))
		holds = holds || ba.AuthorID == oldAuthorID
	}
	fp = append(fp, "c", strings.Join(creditKeys, ","))
	creditsDone := false
	if len(credits) > 0 && !holds {
		// An interrupted apply moves the credits before the primary author:
		// a book whose credits already name every provider author as an
		// author, with the title record gone from them, only needs its
		// primary set.
		creditsDone = authorOnly && creditsNameAll(store, credits, names)
		if !creditsDone {
			return finish(junkSkipNeedsManual, fmt.Sprintf(
				"the book's credit list does not include the author %q its title sits in; a person sorts out its credits", author), true)
		}
	}

	// Title equality is the whole identity test, which is weak for a short
	// title: the provider record may be another book called "Descent".
	// What else ties the record to this book goes into the evidence, and a
	// title of one or two words with nothing else is held.
	corr, err := f.corroborate(store, b, fetched, names, paths)
	if err != nil {
		return repairs.Row{}, false, err
	}
	strong := 0
	for _, c := range corr {
		fp = append(fp, "corr", c.text)
		r.Evidence = append(r.Evidence, c.text)
		if c.kind != swapCorrRuntime {
			strong++
		}
	}
	// A runtime within 10% is common between unrelated books, so it never
	// carries a weak row alone. An author-only row always needs a tie: its
	// title is already real, so the provider record matched a title that may
	// well be a person's name or another book's.
	words := titleWords(catalog)
	if continuation {
		r.Evidence = append(r.Evidence, fmt.Sprintf(
			"apply %s of this fixer was cut short on this book (its author lock is set, after the provider record was recorded); the plan that started it passed", contOp))
	}
	switch {
	case continuation:
	case authorOnly && strong == 0:
		return finish(junkSkipNeedsManual, fmt.Sprintf(
			"the title is already %q, so nothing but that title ties the provider record (by %q) to this book "+
				"(no matching narrator, ASIN or author folder; a runtime alone does not count); a person decides", b.Title, fetchedAuthor), true)
	case words <= swapShortTitleWords && strong == 0:
		return finish(junkSkipNeedsManual, fmt.Sprintf(
			"the provider title %q is %d word(s) and nothing but the title ties the provider record to this book "+
				"(no matching narrator, ASIN or author folder; a runtime alone does not count); another book may share the title", catalog, words), true)
	}

	proposedTitle := newTitle
	if authorOnly {
		proposedTitle = b.Title
	}
	r.Proposed = map[string]string{"title": proposedTitle, "author": strings.Join(display, ", ")}
	if authorOnly {
		r.Reason = fmt.Sprintf("the title %q is already the provider's, and the author %q is that same title; "+
			"the provider recorded the author %q (the title is not changed)", b.Title, author, fetchedAuthor)
	} else {
		r.Reason = fmt.Sprintf("the title %q is junk (%s) and the author %q is the title a provider recorded; "+
			"the provider recorded the title %q by %q", b.Title, kind, author, fetchedTitle, fetchedAuthor)
		if newTitle != fetchedTitle {
			r.Reason += fmt.Sprintf("; the edition marker is dropped (%q)", newTitle)
		}
	}
	if len(names) > 1 {
		r.Reason += fmt.Sprintf("; %d authors credited", len(names))
	}
	r.Detail = &swapDecision{bookID: b.ID, oldTitle: b.Title, newTitle: newTitle, oldAuthorID: oldAuthorID,
		names: names, credits: credits, creditsDone: creditsDone, continuation: continuation}
	r.Fingerprint = fingerprintStrings(append(fp, "apply", newTitle, strings.Join(names, "\x1f"),
		strconv.FormatBool(creditsDone), strconv.FormatBool(continuation))...)
	return r, true, nil
}

// swapSameRecordWindow is how far apart a provider title and author may have
// been recorded and still count as one record's. A state row's UpdatedAt is
// when anything on the row last changed: updateFetchedMetadataState stamps
// every field one fetch writes with the same time, and a person's edit of the
// field restamps it (that field is then user-locked and the row held anyway).
// A repair's lock keeps the row's time.
const swapSameRecordWindow = 5 * time.Second

// swapShortTitleWords: a provider title of at most this many words needs
// something besides the title to tie the record to the book.
const swapShortTitleWords = 2

// swapRuntimeTolerance is how far a provider runtime may be from the book's
// duration, as a fraction of the runtime, to corroborate.
const swapRuntimeTolerance = 0.10

// swapContinuation reports whether the book is mid-repair by an earlier apply
// of this fixer that was cut short: its author carries a repair lock (only
// this fixer locks an author, first thing in its apply), that operation's
// journal holds the lock row for this book, and the provider title and author
// were recorded no later than that lock. The last condition ties the waiver
// to the provider record the cut apply's plan passed: a record fetched since
// is judged like any other.
//
// journal memoizes the fallback read of an operation's whole journal for one
// plan; nil reads it fresh.
func swapContinuation(store OpsStore, journal *swapOpJournalMemo, bookID string, author *database.MetadataFieldState, fetched map[string]swapFetched) (bool, string, error) {
	if author == nil || !author.IsRepairLock() {
		return false, "", nil
	}
	op, ok := database.RepairLockOp(author.LockSource)
	if !ok {
		return false, "", nil
	}
	changes, err := swapJournalRows(store, journal, bookID, op)
	if err != nil {
		return false, "", err
	}
	for _, c := range changes {
		if c.Voided || c.OperationID != op || c.BookID != bookID || c.ChangeType != undo.ChangeTypeFieldLock || c.FieldName != database.FieldKeyAuthorName {
			continue
		}
		t, a := fetched["title"], fetched["author_name"]
		if t.at.After(c.CreatedAt) || a.at.After(c.CreatedAt) {
			return false, "", nil
		}
		return true, op, nil
	}
	return false, "", nil
}

// swapJournalRows reads the journal rows of bookID that can name op.
//
// The book's rows first (GetBookChanges reads the opchange_by_book: index, and
// falls back to the journal scan while the index is not trusted), not the
// operation's: an apply's journal holds every row of every book it wrote.
// But GetBookChanges fails when ANY journal row anywhere is undecodable (the
// indexed path's undecodable-row gate and the full scan both do), so one
// corrupt row in an unrelated operation would turn every continuation
// candidate into an error row. On that error the operation's own rows are
// read instead (GetOperationChanges, which decodes only op's rows) and
// filtered to the book; the caller filters by operation, book, change type
// and field either way. When that read fails too, the error is returned:
// fail closed, never "no continuation".
//
// The undecodable-row gate fails GetBookChanges for EVERY book at once, so
// without a memo each repair-locked candidate of the same apply would decode
// that apply's whole journal again. journal (one per Plan) reads each
// operation's journal once, grouped by book; nil reads it fresh.
func swapJournalRows(store OpsStore, journal *swapOpJournalMemo, bookID, op string) ([]*database.OperationChange, error) {
	changes, bookErr := store.GetBookChanges(bookID)
	if bookErr == nil {
		return changes, nil
	}
	byBook, opErr := journal.rows(store, op)
	if opErr != nil {
		return nil, fmt.Errorf("read the journal rows of %s: %w (and of operation %s: %w)", bookID, bookErr, op, opErr)
	}
	return byBook[bookID], nil
}

// swapOpJournalMemo holds each operation's journal rows, grouped by book,
// read at most once per plan (GetOperationChanges). A read error is memoized
// too, so every candidate of that operation fails closed alike without
// re-reading. Safe for the plan's concurrent workers; a nil memo reads fresh
// on every call.
type swapOpJournalMemo struct {
	mu  sync.Mutex
	ops map[string]*swapOpJournalEntry
}

type swapOpJournalEntry struct {
	once   sync.Once
	byBook map[string][]*database.OperationChange
	err    error
}

func newSwapOpJournalMemo() *swapOpJournalMemo {
	return &swapOpJournalMemo{ops: map[string]*swapOpJournalEntry{}}
}

func (m *swapOpJournalMemo) rows(store OpsStore, op string) (map[string][]*database.OperationChange, error) {
	if m == nil {
		return readOpJournalByBook(store, op)
	}
	m.mu.Lock()
	e, ok := m.ops[op]
	if !ok {
		e = &swapOpJournalEntry{}
		m.ops[op] = e
	}
	m.mu.Unlock()
	e.once.Do(func() { e.byBook, e.err = readOpJournalByBook(store, op) })
	return e.byBook, e.err
}

func readOpJournalByBook(store OpsStore, op string) (map[string][]*database.OperationChange, error) {
	rows, err := store.GetOperationChanges(op)
	if err != nil {
		return nil, err
	}
	byBook := map[string][]*database.OperationChange{}
	for _, c := range rows {
		if c != nil {
			byBook[c.BookID] = append(byBook[c.BookID], c)
		}
	}
	return byBook, nil
}

// swapStaleSeriesHold holds a book whose stored row embeds a series object
// its SeriesID does not name: writing the row (the title, or the author id)
// would drop that object (database enforceSeriesInvariant). The drop is
// recorded in the book's change history (series_object), but the
// stale-series relink reads the live object to offer the series back, so the
// owner chooses the series before this fixer writes the book.
func swapStaleSeriesHold(store OpsStore, bookID string) (skip, why string, err error) {
	book, err := store.GetBookByID(bookID)
	if err != nil {
		return "", "", fmt.Errorf("read book %s: %w", bookID, err)
	}
	if book == nil || book.Series == nil {
		return "", "", nil
	}
	emb := book.Series
	switch {
	case book.SeriesID == nil:
		return swapSkipRelinkSeriesFirst, fmt.Sprintf(
			"the book row keeps a stale series object (%q, id %d) with no series id; writing the book would drop it -- "+
				"run the stale-series relink fixer first", emb.Name, emb.ID), nil
	case *book.SeriesID != emb.ID:
		return swapSkipRelinkSeriesFirst, fmt.Sprintf(
			"the book row keeps a series object (%q, id %d) that is not its series id %d; writing the book would drop it -- "+
				"a person decides which series is right", emb.Name, emb.ID, *book.SeriesID), nil
	}
	return "", "", nil
}

// swapHistoryAll asks GetMetadataChangeHistory for every row of a field.
const swapHistoryAll = 1 << 30

// Kinds of corroboration. A runtime alone never carries a weak row.
const (
	swapCorrNarrator = "narrator"
	swapCorrASIN     = "asin"
	swapCorrRuntime  = "runtime"
	swapCorrPath     = "path"
)

// swapCorr is one thing besides the title that ties the provider record to
// the book.
type swapCorr struct{ kind, text string }

// titleWords counts a title's words as runs of letters and digits, so "T.S.
// Eliot" and "T. S. Eliot" are both three.
func titleWords(s string) int {
	n, in := 0, false
	for _, r := range s {
		w := unicode.IsLetter(r) || unicode.IsDigit(r)
		if w && !in {
			n++
		}
		in = w
	}
	return n
}

// authorCoreKey is the letters key of an author name with a bracketed
// edition marker dropped.
func authorCoreKey(name string) string {
	return junkLettersKey(strings.TrimSpace(catalogEditionRe.ReplaceAllString(name, "")))
}

// otherTitles lists, sorted, the real titles credited to authorID whose
// letters key is not key (the title this book should have).
func otherTitles(idx *junkIndex, authorID int, key, coreKey string) []string {
	var out []string
	for k, t := range idx.authorTitles[authorID] {
		if k != key && k != coreKey {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// fieldStateOf returns the state row for field, or nil.
func fieldStateOf(states []database.MetadataFieldState, field string) *database.MetadataFieldState {
	for i := range states {
		if states[i].Field == field {
			return &states[i]
		}
	}
	return nil
}

// lockHold is the skip kind and reason of a lock hold: a repair's lock is
// its own kind (skipped_repair_locked) and names its operation, so it is not
// reported as a person's override.
func lockHold(st *database.MetadataFieldState, what string) (kind, why string) {
	if st != nil && st.IsRepairLock() {
		return junkSkipRepairLocked, fmt.Sprintf("the %s is locked by a repair (%s); revert that operation first to rewrite it", what, st.LockSource)
	}
	return junkSkipUserLocked, fmt.Sprintf("the %s carries a user override; it is never rewritten", what)
}

// creditsNameAll reports whether every provider name resolves to an existing
// author (exact name) that credits holds in an author role: the credit move
// of an interrupted apply already happened.
func creditsNameAll(store OpsStore, credits []database.BookAuthor, names []string) bool {
	asAuthor := map[int]bool{}
	for _, ba := range credits {
		if isPrimaryAuthorRole(ba.Role) {
			asAuthor[ba.AuthorID] = true
		}
	}
	for _, n := range names {
		a, err := store.GetAuthorByName(n)
		if err != nil || a == nil || !asAuthor[a.ID] {
			return false
		}
	}
	return len(names) > 0
}

// swapFetched is one field's provider value: a string (HTML entities decoded:
// some providers return "Magic Tides &amp; Magic Claims") or a number.
type swapFetched struct {
	str string
	num float64
	at  time.Time
	ok  bool
}

func swapFetchedValues(states []database.MetadataFieldState) map[string]swapFetched {
	out := map[string]swapFetched{}
	for i := range states {
		if !states[i].HasProviderValue() {
			continue
		}
		v := swapFetched{at: states[i].UpdatedAt, ok: true}
		switch d := metastate.Decode(states[i].FetchedValue).(type) {
		case string:
			v.str = strings.TrimSpace(html.UnescapeString(d))
			if n, err := strconv.ParseFloat(v.str, 64); err == nil {
				v.num = n
			}
		case float64:
			v.num = d
		case int:
			v.num = float64(d)
		case int64:
			v.num = float64(d)
		default:
			continue
		}
		out[states[i].Field] = v
	}
	return out
}

// swapStateGap is how far apart two fields' provider values were recorded.
func swapStateGap(a, b swapFetched) time.Duration {
	d := a.at.Sub(b.at)
	if d < 0 {
		d = -d
	}
	return d
}

// corroborate lists what besides the title ties the provider record to the
// book. A provider field counts only when it was recorded with the title (one
// record). A narrator or ASIN the book carries counts only when no metadata
// fetch wrote it: the fill-only apply copies the provider record into empty
// fields, and a value copied from the record matches it by construction. The
// narrator a "read by X" title names came from the file.
func (f *swappedTitleAuthorFixer) corroborate(store OpsStore, b database.BookCore, fetched map[string]swapFetched, names, paths []string) ([]swapCorr, error) {
	title := fetched["title"]
	sameRecord := func(field string) (swapFetched, bool) {
		v := fetched[field]
		return v, v.ok && swapStateGap(v, title) <= swapSameRecordWindow
	}
	fetchedByProvider := func(field string) (bool, error) {
		// Every row: the store's default page is 50 rows, and the fetched
		// row that filled the field may be older than the 50 newest.
		hist, err := store.GetMetadataChangeHistory(b.ID, database.HistoryFieldName(field), swapHistoryAll)
		if err != nil {
			return false, fmt.Errorf("read %s history of %s: %w", field, b.ID, err)
		}
		for i := range hist {
			if hist[i].ChangeType == "fetched" {
				return true, nil
			}
		}
		return false, nil
	}
	var out []swapCorr

	if pn, ok := sameRecord("narrator"); ok && pn.str != "" {
		local := map[string]string{}
		if n, ok := metadata.NarratorCreditName(b.Title); ok {
			local[junkLettersKey(n)] = n
		}
		if b.Narrator != nil && strings.TrimSpace(*b.Narrator) != "" {
			filled, err := fetchedByProvider("narrator")
			if err != nil {
				return nil, err
			}
			if !filled {
				for _, n := range narratorsOf(b.Narrator) {
					local[junkLettersKey(n)] = n
				}
			}
		}
		for _, n := range splitCreditNames(pn.str) {
			if k := junkLettersKey(n); k != "" {
				if mine, ok := local[k]; ok {
					out = append(out, swapCorr{swapCorrNarrator, fmt.Sprintf("provider narrator %q matches the book's narrator %q", n, mine)})
					break
				}
			}
		}
	}

	if pa, ok := sameRecord("asin"); ok && pa.str != "" && b.ASIN != nil && strings.EqualFold(strings.TrimSpace(*b.ASIN), pa.str) {
		filled, err := fetchedByProvider("asin")
		if err != nil {
			return nil, err
		}
		if !filled {
			out = append(out, swapCorr{swapCorrASIN, fmt.Sprintf("provider ASIN %s matches the book's ASIN", pa.str)})
		}
	}

	if pr, ok := sameRecord("audible_runtime_min"); ok && pr.num > 0 && b.Duration != nil && *b.Duration > 0 {
		want := pr.num * 60
		if diff := float64(*b.Duration) - want; diff <= want*swapRuntimeTolerance && -diff <= want*swapRuntimeTolerance {
			out = append(out, swapCorr{swapCorrRuntime, fmt.Sprintf("provider runtime %.0f min is within %.0f%% of the book's %d min",
				pr.num, swapRuntimeTolerance*100, *b.Duration/60)})
		}
	}

	// A folder (or file name) spelled like the provider author: the file
	// was filed under that author before it ever reached this library.
pathLoop:
	for _, n := range names {
		k := junkLettersKey(n)
		if len(k) < 4 {
			continue
		}
		for _, p := range paths {
			for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
				if strings.Contains(junkLettersKey(seg), k) {
					out = append(out, swapCorr{swapCorrPath, fmt.Sprintf("the provider author %q is in the file path %s", n, p)})
					break pathLoop
				}
			}
		}
	}
	return out, nil
}

// swapMaxAuthorNames is the shortest provider credit held as possibly naming
// non-authors.
const swapMaxAuthorNames = 3

// swapAuthorNames turns the provider's author credit into the names to
// credit, each cleaned by the shared author-creation gate. A credit listing
// several names is split by the shared splitter, the same one
// maintenance.author-split-scan uses; a credit it refuses to split, or that
// carries contributor roles, is held.
func swapAuthorNames(raw string) (names []string, skip, why string) {
	if swapRoleRe.MatchString(raw) {
		return nil, swapSkipMultiAuthor, fmt.Sprintf(
			"the provider author %q names contributor roles (translator, editor, ...); a person decides who the authors are", raw)
	}
	parts := []string{raw}
	if strings.ContainsAny(raw, ",;&/") || strings.Contains(strings.ToLower(raw), " and ") {
		parts = dedup.SplitCompositeAuthorName(raw)
		if len(parts) < 2 {
			return nil, swapSkipMultiAuthor, fmt.Sprintf(
				"the provider author %q lists several names the shared author splitter will not separate safely", raw)
		}
	}
	// Three or more names is the light-novel credit shape: the provider
	// lists the illustrator (and sometimes the translator) beside the
	// writer with no role marker ("Kumo Kagyu, Noboru Kannatuki"), so which
	// of them wrote the book is a person's call.
	if len(parts) >= swapMaxAuthorNames {
		return nil, swapSkipMultiAuthor, fmt.Sprintf(
			"the provider author %q lists %d names; a credit that long often names illustrators or translators without saying so, "+
				"so a person decides who the authors are", raw, len(parts))
	}
	seen := map[string]bool{}
	for _, p := range parts {
		clean, rej := personname.PrepareAuthorNameForCreation(p)
		if rej != "" {
			return nil, swapSkipImplausibleAuthor, fmt.Sprintf("the provider author %q is not a plausible name (%s)", p, rej)
		}
		if k := junkLettersKey(clean); !seen[k] {
			seen[k] = true
			names = append(names, clean)
		}
	}
	return names, "", ""
}

// Apply writes one fresh row through w, in this order: authors resolved (an
// author created only when no author has the name, journaled); the title
// (journaled, compare-and-set, user lock re-checked inside the write, then
// locked); the author lock; the credit move off the author record that held
// the title (journaled inside the book's author lock, compare-and-set on the
// credit list); the primary author. Every check that can refuse the row runs
// before the first write, so a refusal writes nothing; a failure after the
// first write is reported as partially applied.
//
// The order is chosen so an interrupted apply leaves a book the next plan
// finishes. Cut after the title (or its lock): title real, author still the
// title, which is the author-only shape, and its apply locks whichever of
// the two fields is still unlocked. Cut after the author lock: author-only
// again; the repair's author lock does not hold the row. Cut after the
// credit move: author-only with creditsDone, whose apply sets the primary
// only. Nothing is left that no plan lists.
func (f *swappedTitleAuthorFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*swapDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", swappedFixerID, fresh.RowID)
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	id := d.bookID
	locks, err := database.LoadFieldLocks(store, id)
	if err != nil {
		return fmt.Errorf("read field locks of %s: %w", id, err)
	}
	// A full row writes the title, so any title lock refuses it; an
	// author-only row writes no title, so a person's title lock is left
	// alone and does not refuse it.
	if (d.newTitle != "" && locks.Locked(database.FieldKeyTitle)) ||
		(locks.Locked(database.FieldKeyAuthorName) && !locks.RepairLocked(database.FieldKeyAuthorName)) {
		return fmt.Errorf("%w: book %s title or author was locked since the plan", repairs.ErrChangedSincePlan, id)
	}
	book, err := store.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("read book %s: %w", id, err)
	}
	if book == nil || book.IsSoftDeleted() {
		return fmt.Errorf("%w: book %s is gone", repairs.ErrChangedSincePlan, id)
	}
	if book.Title != d.oldTitle || book.AuthorID == nil || *book.AuthorID != d.oldAuthorID {
		return fmt.Errorf("%w: book %s title or primary author changed", repairs.ErrChangedSincePlan, id)
	}
	cur, err := store.GetBookAuthors(id)
	if err != nil {
		return fmt.Errorf("read credits of %s: %w", id, err)
	}
	if !sameCreditList(cur, d.credits) {
		return fmt.Errorf("%w: book %s credits changed", repairs.ErrChangedSincePlan, id)
	}
	aidx, err := f.cachedAuthorIndex()
	if err != nil {
		return err
	}

	var targets []*database.Author
	seen := map[int]bool{}
	for _, n := range d.names {
		t, err := f.resolveAuthor(w, store, aidx, d, n)
		if err != nil {
			return err
		}
		if t.ID == d.oldAuthorID {
			return fmt.Errorf("%w: book %s: author %q resolves to the record that holds the title", repairs.ErrChangedSincePlan, id, n)
		}
		if !seen[t.ID] {
			seen[t.ID] = true
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("%s: row %s resolved no author", swappedFixerID, fresh.RowID)
	}

	// The author lock first: it marks the book as mid-repair by this fixer
	// (only this fixer locks an author), which is how the next plan knows to
	// finish an interrupted apply (evaluate: continuation). It also stops a
	// forced rescan from putting the title back into the author
	// (scanner.applyScannerFields honours the lock). Journaled, so the op
	// revert lifts it; a lock an interrupted apply left is taken over, so
	// this op owns every lock it relies on.
	if lerr := w.LockFields(id, database.FieldKeyAuthorName); lerr != nil {
		return lerr
	}
	partial := func(what string, err error) error {
		return fmt.Errorf("%w: book %s: %s: %w", repairs.ErrPartiallyApplied, id, what, err)
	}
	if d.newTitle != "" {
		// writeTitleOnly journals the title (so the op revert puts it back
		// with the credits) and locks it.
		if terr := writeTitleOnly(w, store, id, d.oldTitle, d.newTitle); terr != nil {
			return partial("the title was not written", terr)
		}
	} else if !locks.Locked(database.FieldKeyTitle) || locks.RepairLocked(database.FieldKeyTitle) {
		// Author-only: the title is already real; this op locks it under its
		// own id (or finds it already so). A person's title lock is theirs.
		if lerr := w.LockFields(id, database.FieldKeyTitle); lerr != nil {
			return partial("the title was not locked", lerr)
		}
	}

	// creditsDone (an interrupted apply moved the credits but not the
	// primary): the junction is written back as it is, so the journaled move
	// still records the primary change and the op revert puts it back.
	primaryChanged := false
	{
		_, err = w.ModifyCredits(id, func(cur []database.BookAuthor) ([]database.BookAuthor, repairs.UndoEntry, error) {
			if !sameCreditList(cur, d.credits) {
				return nil, repairs.UndoEntry{}, fmt.Errorf("%w: book %s credits changed", repairs.ErrChangedSincePlan, id)
			}
			b, gerr := store.GetBookByID(id)
			if gerr != nil {
				return nil, repairs.UndoEntry{}, fmt.Errorf("read book %s: %w", id, gerr)
			}
			if b == nil {
				return nil, repairs.UndoEntry{}, fmt.Errorf("%w: book %s vanished", repairs.ErrChangedSincePlan, id)
			}
			next := swapCredits(cur, id, d.oldAuthorID, targets)
			if d.creditsDone {
				next = append([]database.BookAuthor(nil), cur...)
			}
			primaryChanged = b.AuthorID != nil && *b.AuthorID == d.oldAuthorID
			var afterID *int
			if primaryChanged {
				pid := targets[0].ID
				afterID = &pid
			}
			after := make([]database.BookAuthor, len(next))
			copy(after, next)
			snap, merr := json.Marshal(undo.TitleRelinkCreditsSnapshot{AuthorID: b.AuthorID, Credits: cur})
			if merr != nil {
				return nil, repairs.UndoEntry{}, fmt.Errorf("encode credits of %s: %w", id, merr)
			}
			// The junk-author fixer's change type: the same move (one credit
			// off a record that is not this book's author, the exact junction
			// after), so the op revert's compare-and-set restore applies as is.
			move, merr := json.Marshal(undo.JunkAuthorCreditsMove{FromAuthorID: d.oldAuthorID, IntoAuthorID: targets[0].ID,
				PrimaryAfter: afterID, PrimaryChanged: primaryChanged, CreditsAfter: &after})
			if merr != nil {
				return nil, repairs.UndoEntry{}, fmt.Errorf("encode credit move of %s: %w", id, merr)
			}
			return next, repairs.UndoEntry{ChangeType: undo.ChangeTypeJunkAuthorCredits, Field: "book_authors",
				Old: string(snap), New: string(move)}, nil
		})
		if err != nil {
			return partial("the credits were not moved", err)
		}
	}
	if primaryChanged {
		if perr := w.SetPrimaryAuthor(id, d.oldAuthorID, targets[0]); perr != nil {
			return fmt.Errorf("%w: book %s credits moved but the primary author was not: %w", repairs.ErrPartiallyApplied, id, perr)
		}
	}
	return nil
}

// resolveAuthor returns the author a provider name credits: the author with
// that name (the store's lookup ignores case and spacing), else the one
// author spelled like it by letters and digits, else a new author, created
// and journaled by mintJournaledAuthor. Two or more same-spelled authors is a
// change since the plan, which held such a row.
func (f *swappedTitleAuthorFixer) resolveAuthor(w *repairs.Writer, store OpsStore, aidx *swapAuthorIndex, d *swapDecision, name string) (*database.Author, error) {
	existing, err := store.GetAuthorByName(name)
	if err != nil {
		return nil, fmt.Errorf("read author %q: %w", name, err)
	}
	if existing != nil {
		return existing, nil
	}
	// The rest decides on a create, so it runs under mintMu: a same-spelled
	// author another row of this apply created is found in f.minted.
	f.mintMu.Lock()
	defer f.mintMu.Unlock()
	key := junkLettersKey(name)
	if id, ok := f.minted[key]; ok {
		a, err := store.GetAuthorByID(id)
		if err != nil {
			return nil, fmt.Errorf("read author %d: %w", id, err)
		}
		if a != nil && a.ID == id {
			return a, nil
		}
		// Reverted or merged away since: decide again from the store.
		delete(f.minted, key)
	}
	switch vs := aidx.variants(name, d.oldAuthorID); len(vs) {
	case 0:
	case 1:
		a, err := store.GetAuthorByID(vs[0].ID)
		if err != nil {
			return nil, fmt.Errorf("read author %d: %w", vs[0].ID, err)
		}
		if a == nil || a.ID != vs[0].ID {
			return nil, fmt.Errorf("%w: book %s: author %q (id %d) is gone", repairs.ErrChangedSincePlan, d.bookID, vs[0].Name, vs[0].ID)
		}
		return a, nil
	default:
		return nil, fmt.Errorf("%w: book %s: %d authors are spelled like %q", repairs.ErrChangedSincePlan, d.bookID, len(vs), name)
	}
	created, err := mintJournaledAuthor(w, store, d.bookID, name)
	if err != nil {
		return nil, err
	}
	f.minted[key] = created.ID
	return created, nil
}

// mintJournaledAuthor creates the author name names (or takes the one another
// writer created meanwhile) and journals a create this call made under
// bookID, AFTER the create, by id: the op revert deletes exactly that row,
// and only while nothing credits it. A journal failure takes the row back
// while nothing credits it, so no author is left that the revert cannot see.
// Shared by the junk-author and swapped title/author fixers.
func mintJournaledAuthor(w *repairs.Writer, store OpsStore, bookID, name string) (*database.Author, error) {
	// MintAuthor writes the store directly, ahead of the journal row below:
	// renew the scan stand-down lease first, so a lapsed one refuses the row
	// before the author exists rather than after.
	if err := w.Beat("create author " + name); err != nil {
		return nil, err
	}
	created, minted, err := store.MintAuthor(name)
	if err != nil {
		return nil, fmt.Errorf("create author %q: %w", name, err)
	}
	if created == nil || created.ID <= 0 {
		return nil, fmt.Errorf("create author %q: no row", name)
	}
	if !minted {
		// Another writer made it since the lookup: use it, journal no create
		// (the undo of one deletes the row), take nothing back.
		return created, nil
	}
	rec, err := json.Marshal(undo.JunkAuthorCreate{AuthorID: created.ID, Name: created.Name})
	if err != nil {
		return nil, fmt.Errorf("encode created author %d: %w", created.ID, err)
	}
	if err := w.RecordChange(bookID, repairs.UndoEntry{ChangeType: undo.ChangeTypeJunkAuthorCreate,
		Field: "author_name", New: string(rec)}); err != nil {
		// Take it back rather than leave an unjournaled author the revert
		// cannot see -- but only while nothing credits it: another worker's
		// MintAuthor resolves the same name to this row (minted=false) and
		// may already have linked a book to it.
		credited, cerr := store.GetBooksByAuthorIDForRelinkCore(created.ID)
		switch {
		case cerr != nil:
			return nil, fmt.Errorf("journal created author %d: %w (kept: reading its credits failed: %v)", created.ID, err, cerr)
		case len(credited) > 0:
			return nil, fmt.Errorf("journal created author %d: %w (kept: %d book(s) already credit it)", created.ID, err, len(credited))
		}
		// Deliberately NOT guarded by w.Beat: err is often ErrStandDownLost,
		// and a lapsed lease must not stop this compensating delete. It only
		// undoes the MintAuthor just above, of an author nothing credits and
		// no journal row describes.
		if derr := store.DeleteAuthor(created.ID); derr != nil {
			return nil, fmt.Errorf("journal created author %d: %w (and removing it failed: %v)", created.ID, err, derr)
		}
		return nil, fmt.Errorf("journal created author %d: %w", created.ID, err)
	}
	return created, nil
}

// swapCredits replaces the credit of the record that held the title with the
// provider's authors, in its position and with its role (a narrator role
// becomes "author": these are the writers). A target already credited in an
// AUTHOR role is not added twice. A target credited only as a narrator (an
// author who reads their own book) keeps that row and also gets an author row
// in the title record's slot: skipping it would leave the book with no author
// credit while SetPrimaryAuthor makes that person its primary author. Other
// credits keep their order. A book whose credit lived only in its primary
// author id (an empty junction) gets the targets as its junction, as a manual
// edit would leave it.
func swapCredits(cur []database.BookAuthor, bookID string, oldID int, targets []*database.Author) []database.BookAuthor {
	have := map[int]bool{}
	for _, ba := range cur {
		if ba.AuthorID != oldID && isPrimaryAuthorRole(ba.Role) {
			have[ba.AuthorID] = true
		}
	}
	add := func(out []database.BookAuthor, role string) []database.BookAuthor {
		for _, t := range targets {
			if have[t.ID] {
				continue
			}
			have[t.ID] = true
			out = append(out, database.BookAuthor{BookID: bookID, AuthorID: t.ID, Role: role})
		}
		return out
	}
	out := make([]database.BookAuthor, 0, len(cur)+len(targets))
	placed := false
	for _, ba := range cur {
		if ba.AuthorID != oldID {
			out = append(out, ba)
			continue
		}
		if placed {
			continue
		}
		placed = true
		role := ba.Role
		if role == "" || isNarratorRole(role) {
			role = "author"
		}
		out = add(out, role)
	}
	if !placed {
		out = add(out, "author")
	}
	for i := range out {
		out[i].Position = i
	}
	return out
}
