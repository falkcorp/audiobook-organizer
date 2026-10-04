// file: internal/plugins/maintenance/combined_author_fixer.go
// version: 1.0.0
// guid: 5c0f4a3e-2b7d-4e61-9a8c-3f1d6b2e7a90
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// --- Repairs lane: combined author credits ---
//
// WHY. Authors are individual rows linked to a book through its credits
// (book_authors, positions 0, 1, 2...); the combined "A, B" string is produced
// only when tags are written. Production breaks that: a census on 2026-10-04
// (.claude/notes/overnight-2026-10-03/combined-author-credit-census.json)
// found 1,597 author rows whose name joins names that each exist as their own
// author ("J.N. Chaney, Jonathan P. Brazee", "Shirtaloon, Travis Deverell"
// with 92 books), linked on 3,120 (book, record) pairs: 1,454 where the
// separate authors are credited too, often at colliding positions, and 1,666
// where the combined row is the only credit. Every creation path minted these
// by looking up and creating the WHOLE credit string; internal/authorcredit
// is the root-cause fix, this fixer repairs what is already stored.
//
// WHAT A ROW IS. One book (row id = book id), covering every combined record
// the book credits in an author role or holds as its primary AuthorID. Classes:
//
//   - duplicate_link: every part of every combined record is already
//     credited. The combined credit is removed and the positions renumbered.
//   - combined_only: no part is credited. The combined credit is replaced by
//     its parts, each resolved to its existing author.
//   - partial_link: some parts are credited, some not. Same write as
//     combined_only for the missing parts.
//   - split_new_authors: the shared splitter splits the name but at least one
//     part is no author yet; apply creates it (journaled). These are records
//     the census did not count (it required every part to exist), kept apart
//     so the census classes reconcile.
//
// ORDER. The credits are stably sorted by their stored position; where two
// rows share a position (the "J. N. Chaney @0, Jonathan P. Brazee @0" shape)
// the parts of the combined name keep the combined string's order. A missing
// part takes the combined credit's place, in the combined string's order.
// Positions are then renumbered 0..n-1. When the book's primary AuthorID is
// the combined record it moves to the first part of that record.
//
// HELD (never written):
//   - the shared splitter (personname.SplitCompositeAuthorName) refuses the
//     name, or drops a piece of it (skipped_split_refused): titles split on
//     "and", "Last, First" names, junk;
//   - a part fails the creation gate (personname.CleanAuthorNameForCreation:
//     positional shrapnel, publishers, role credits) (skipped_implausible_part);
//   - the name carries contributor roles ("- translator", "- editor")
//     (skipped_contributor_role);
//   - every part is one name ("A. G. Riddle, A. G. Riddle")
//     (skipped_doubled_author): collapsing it to one credit is a separate
//     owner decision;
//   - more than combinedMaxAuthorParts distinct names (skipped_anthology);
//   - a part matches two or more existing authors by letters and digits with
//     none spelled exactly like it (skipped_ambiguous_author);
//   - the user locked the book's author (skipped_user_locked);
//   - the primary would move but the book row still embeds a stale series
//     object (skipped_relink_series_first), which the primary write would drop;
//   - books/itunes/** and Doctor Who / Big Finish / Torchwood: the framework
//     guards (skipped_itunes, skipped_owner_manual). This fixer does NOT
//     implement repairs.ITunesDatabaseOnly: iTunes books wait for the owner.
//
// ROLES. Only author-role credits ("", author, co-author) are read or
// rewritten; a narrator-role credit is left exactly as it is, even one on a
// combined record (which then stays referenced, so the purge keeps it).
//
// THE COMBINED ROW IS NOT DELETED. Once no book credits it,
// maintenance.purge-empty-authors removes it (dry run first, journaled).
//
// APPLY re-plans the row under the scan stand-down, re-checks the user lock,
// the primary and the exact credit list (compare-and-set inside the book's
// author lock), journals the credit write as undo.ChangeTypeJunkAuthorCredits
// (the snapshot and the exact junction written, so the op revert restores
// it while nothing changed since) and any author it creates as
// undo.ChangeTypeJunkAuthorCreate, then moves the primary through
// Writer.SetPrimaryAuthor. Undo with the apply operation's revert.

const combinedAuthorFixerID = "maintenance.repair-combined-author-credits"

// Row classes.
const (
	combinedClassDuplicate  = "duplicate_link"
	combinedClassOnly       = "combined_only"
	combinedClassPartial    = "partial_link"
	combinedClassNewAuthors = "split_new_authors"
)

// Skip kinds of this fixer (the framework adds skipped_itunes and
// skipped_owner_manual; skipped_ambiguous_author, skipped_user_locked,
// skipped_relink_series_first and skipped_gone are shared with the swapped
// and junk-title fixers so they read the same in the Repairs tab).
const (
	combinedSkipSplitRefused    = "skipped_split_refused"
	combinedSkipImplausiblePart = "skipped_implausible_part"
	combinedSkipRole            = "skipped_contributor_role"
	combinedSkipDoubled         = "skipped_doubled_author"
	combinedSkipAnthology       = "skipped_anthology"
	// combinedSkipTitle: the record's name is (or begins, or is the start
	// of) the title of a book or the name of a series in the library: "Jonathan
	// Strange and Mr Norrell" is person-shaped on both sides of its "and".
	combinedSkipTitle = "skipped_names_a_title"
	// combinedSkipNotCombined: what a re-plan reports for a book that no
	// longer credits a combined record. A plan never lists one.
	combinedSkipNotCombined = "skipped_not_combined"
)

// combinedMaxAuthorParts is the most distinct names a combined record may
// join and still be split. The shared splitter itself has no limit; this
// follows the swapped title/author fixer (swapMaxAuthorNames), which holds a
// provider credit of three or more names because long credits name
// illustrators, translators and cast without saying so. A combined record is
// held from one name more than that: its parts already exist as authors
// (the census rule), which is evidence the provider credit lacks. Credits of
// four or more names are anthologies and cast lists ("Lisa Bowerman, Thomas
// Grant, Miles Richardson, & Harry Myers") and wait for a person.
const combinedMaxAuthorParts = 3

// combinedIndexTTL bounds how long Replan reuses the author index in an apply.
const combinedIndexTTL = 2 * time.Minute

type combinedAuthorFixer struct {
	p *Plugin

	idxMu      sync.Mutex
	idx        *combinedAuthorIndex
	idxBuiltAt time.Time

	// mintMu serializes the create path of resolvePart; minted maps the
	// letters key of every author this process created to its id, so two
	// rows of one apply bringing two spellings of a new name mint one row.
	mintMu sync.Mutex
	minted map[string]int
}

func newCombinedAuthorFixer(p *Plugin) *combinedAuthorFixer {
	return &combinedAuthorFixer{p: p, minted: map[string]int{}}
}

var _ repairs.Fixer = (*combinedAuthorFixer)(nil)

func (f *combinedAuthorFixer) ID() string    { return combinedAuthorFixerID }
func (f *combinedAuthorFixer) Title() string { return "Combined author credits" }
func (f *combinedAuthorFixer) Description() string {
	return "Books credited to an author record whose name joins several authors (\"J.N. Chaney, Jonathan P. " +
		"Brazee\"). Where the separate authors are already credited the combined credit is removed and the " +
		"positions renumbered; where it is the only credit it is replaced by its parts, each resolved to its " +
		"existing author (created only when none exists). A primary author that is the combined record moves " +
		"to the first part. Names the shared splitter will not split, parts that look like titles or junk, " +
		"contributor roles, a doubled name, credits of more than three names, user-locked authors, iTunes " +
		"books and Doctor Who / Big Finish / Torchwood are listed, not changed. Only author credits are " +
		"touched; narrator credits stay. The emptied records are left for maintenance.purge-empty-authors. " +
		"Undo with the apply operation's revert."
}

// combinedAuthorIndex is every author by letters key and the set of combined
// records.
type combinedAuthorIndex struct {
	byKey    map[string][]database.Author
	names    map[int]string
	combined map[int]bool
	// titles holds the letters key of every live book title and series name.
	titles map[string]bool
}

// combinedTitlePrefixMin is the shortest letters key a title-prefix match
// counts for, so a short title ("Dune") is not a prefix of a credit.
const combinedTitlePrefixMin = 10

// namesTitle reports whether name is a book title or series name in the
// library, or one begins with the other (a truncated tag, "A Dark and Drowning
// Tide_ A D"), with the matching title for the reason.
func (idx *combinedAuthorIndex) namesTitle(name string) bool {
	k := authorcredit.LettersKey(name)
	if k == "" {
		return false
	}
	if idx.titles[k] {
		return true
	}
	if len(k) < combinedTitlePrefixMin {
		return false
	}
	for i := combinedTitlePrefixMin; i < len(k); i++ {
		if idx.titles[k[:i]] {
			return true
		}
	}
	return false
}

// combinedRecordOf reports whether an author name is a combined record:
// every loosely split piece is another existing author (the census rule), or
// the shared splitter splits it.
func (idx *combinedAuthorIndex) isCombinedName(id int, name string) bool {
	parts := authorcredit.LooseParts(name)
	if len(parts) < 2 {
		return false
	}
	all := true
	for _, p := range parts {
		found := false
		for _, a := range idx.byKey[authorcredit.LettersKey(p)] {
			if a.ID != id {
				found = true
				break
			}
		}
		if !found {
			all = false
			break
		}
	}
	return all || len(personname.SplitCompositeAuthorName(name)) >= 2
}

func (f *combinedAuthorFixer) buildIndex() (*combinedAuthorIndex, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	all, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("GetAllAuthors: %w", err)
	}
	idx := &combinedAuthorIndex{byKey: make(map[string][]database.Author, len(all)),
		names: make(map[int]string, len(all)), combined: map[int]bool{}}
	for i := range all {
		idx.names[all[i].ID] = all[i].Name
		if k := authorcredit.LettersKey(all[i].Name); k != "" {
			idx.byKey[k] = append(idx.byKey[k], all[i])
		}
	}
	for i := range all {
		if idx.isCombinedName(all[i].ID, all[i].Name) {
			idx.combined[all[i].ID] = true
		}
	}
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	series, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("GetAllSeries: %w", err)
	}
	idx.titles = make(map[string]bool, len(books)+len(series))
	for i := range books {
		if books[i].IsSoftDeleted() {
			continue
		}
		if k := authorcredit.LettersKey(books[i].Title); k != "" {
			idx.titles[k] = true
		}
	}
	for i := range series {
		if k := authorcredit.LettersKey(series[i].Name); k != "" {
			idx.titles[k] = true
		}
	}
	return idx, nil
}

func (f *combinedAuthorFixer) cachedIndex() (*combinedAuthorIndex, error) {
	f.idxMu.Lock()
	defer f.idxMu.Unlock()
	if f.idx != nil && time.Since(f.idxBuiltAt) < combinedIndexTTL {
		return f.idx, nil
	}
	idx, err := f.buildIndex()
	if err != nil {
		return nil, err
	}
	f.idx, f.idxBuiltAt = idx, time.Now()
	return idx, nil
}

// Plan lists every live book (every version) that credits a combined record
// in an author role or holds one as its primary author.
func (f *combinedAuthorFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	idx, err := f.buildIndex()
	if err != nil {
		return nil, err
	}
	f.idxMu.Lock()
	f.idx, f.idxBuiltAt = idx, time.Now()
	f.idxMu.Unlock()

	ids := make([]int, 0, len(idx.combined))
	for id := range idx.combined {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	// One lookup per combined record (junction credits and legacy AuthorID,
	// primary and non-primary versions, the trash excluded). Each worker
	// writes only books[i].
	books := make([][]database.BookCore, len(ids))
	var looked atomic.Int64
	if err := registry.RunItems(ctx, rep, indexesOf(len(ids)), func(_ context.Context, i int) error {
		defer looked.Add(1)
		bs, berr := store.GetBooksByAuthorIDWithRoleCore(ids[i])
		if berr != nil {
			return fmt.Errorf("books of author %d: %w", ids[i], berr)
		}
		books[i] = bs
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeFail,
		Label:       func(_, total int) string { return fmt.Sprintf("Combined authors %d/%d", looked.Load(), total) },
	}); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var bookIDs []string
	for i := range books {
		for _, b := range books[i] {
			if !seen[b.ID] && !b.IsSoftDeleted() {
				seen[b.ID] = true
				bookIDs = append(bookIDs, b.ID)
			}
		}
	}
	sort.Strings(bookIDs)

	rows := make([]repairs.Row, len(bookIDs))
	keep := make([]bool, len(bookIDs))
	var done atomic.Int64
	// Each worker writes only rows[i] and keep[i] for its own i; idx is
	// read-only.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(bookIDs)), func(_ context.Context, i int) error {
		defer done.Add(1)
		r, ok, eerr := f.evaluate(store, idx, bookIDs[i])
		if eerr != nil {
			r = repairs.Row{RowID: bookIDs[i], BookIDs: []string{bookIDs[i]}, Skipped: "error",
				SkipReason: eerr.Error(), Reason: eerr.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = fingerprintStrings("error", bookIDs[i], eerr.Error())
			ok = true
		}
		rows[i], keep[i] = r, ok
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Combined author books %d/%d", done.Load(), total) },
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
	return out, nil
}

// Replan re-reads the book and decides it again; it always returns
// planned.RowID, so a book that is gone or no longer credits a combined
// record comes back as a skipped row whose fingerprint differs.
func (f *combinedAuthorFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	idx, err := f.cachedIndex()
	if err != nil {
		return repairs.Row{}, err
	}
	r, _, err := f.evaluate(store, idx, planned.RowID)
	return r, err
}

// combinedPart is one name of a combined record, as the plan resolved it.
type combinedPart struct {
	name string
	// creditedID is the author already credited (author role) under this
	// name's letters key; 0 when the part is not credited.
	creditedID int
}

// combinedRecordPlan is one combined record a book credits.
type combinedRecordPlan struct {
	id    int
	name  string
	parts []combinedPart
}

// combinedDecision travels from Replan to Apply in Row.Detail.
type combinedDecision struct {
	bookID  string
	primary *int
	credits []database.BookAuthor
	records []combinedRecordPlan
}

// combinedClassify splits a combined record's name, or says why it is held.
// names are cleaned by the creation gate and de-duplicated by letters key,
// in the combined string's order.
func combinedClassify(name string) (names []string, skip, why string) {
	if swapRoleRe.MatchString(name) {
		return nil, combinedSkipRole, fmt.Sprintf("%q names contributor roles (translator, editor, ...); a person decides who the authors are", name)
	}
	loose := authorcredit.LooseParts(name)
	keys := map[string]bool{}
	for _, p := range loose {
		keys[authorcredit.LettersKey(p)] = true
	}
	if len(keys) == 1 {
		return nil, combinedSkipDoubled, fmt.Sprintf("%q repeats one name; collapsing it to a single credit is a separate decision", name)
	}
	split := personname.SplitCompositeAuthorName(name)
	if len(split) < 2 {
		return nil, combinedSkipSplitRefused, fmt.Sprintf("the shared author splitter will not split %q (a title, a \"Last, First\" name, or junk)", name)
	}
	splitKeys := map[string]bool{}
	for _, p := range split {
		splitKeys[authorcredit.LettersKey(p)] = true
	}
	if len(splitKeys) != len(keys) {
		return nil, combinedSkipSplitRefused, fmt.Sprintf("the shared author splitter splits %q into %d name(s) but it lists %d; a piece would be dropped",
			name, len(splitKeys), len(keys))
	}
	for _, p := range split {
		if personname.LooksLikeWorkTitle(p) {
			return nil, combinedSkipImplausiblePart, fmt.Sprintf("the part %q of %q reads as a title (an article or a series marker)", p, name)
		}
		if _, ok := authorcredit.CleanGate(p); !ok {
			return nil, combinedSkipImplausiblePart, fmt.Sprintf("the part %q of %q is not a plausible author name (a title, publisher or junk)", p, name)
		}
	}
	names = authorcredit.SplitNames(name, authorcredit.CleanGate)
	if len(names) < 2 {
		return nil, combinedSkipSplitRefused, fmt.Sprintf("the shared author splitter will not split %q", name)
	}
	if len(names) > combinedMaxAuthorParts {
		return nil, combinedSkipAnthology, fmt.Sprintf("%q joins %d names; a credit that long is usually an anthology or a cast list, so a person decides",
			name, len(names))
	}
	return names, "", ""
}

// combinedIsAuthorRole is the credit roles this fixer reads and writes.
func combinedIsAuthorRole(role string) bool { return isPrimaryAuthorRole(role) }

// evaluate decides one book. Plan and Replan both call it, so the same state
// yields the same fingerprint. ok is false for a book that credits no
// combined record (Plan leaves it out).
//
// The fingerprint holds the credit list, the primary, every combined
// record's id and name and the outcome by NAME, never whether a part's author
// exists: a sibling row's apply that creates that author must not move this
// row's fingerprint.
func (f *combinedAuthorFixer) evaluate(store OpsStore, idx *combinedAuthorIndex, bookID string) (repairs.Row, bool, error) {
	b, err := store.GetBookByID(bookID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read book %s: %w", bookID, err)
	}
	if b == nil || b.IsSoftDeleted() {
		r := repairs.Row{RowID: bookID, BookIDs: []string{bookID}, Skipped: junkSkipGone,
			SkipReason: "the book no longer exists", Risk: repairs.RiskLow}
		r.Fingerprint = fingerprintStrings("gone", bookID)
		return r, false, nil
	}
	credits, err := store.GetBookAuthors(bookID)
	if err != nil {
		// Fail closed: the write is a compare-and-set against this list.
		return repairs.Row{}, false, fmt.Errorf("read credits of %s: %w", bookID, err)
	}
	nameOf := func(id int) string {
		if n, ok := idx.names[id]; ok {
			return n
		}
		if a, gerr := store.GetAuthorByID(id); gerr == nil && a != nil {
			return a.Name
		}
		return fmt.Sprintf("author %d", id)
	}

	var recIDs []int
	inRec := map[int]bool{}
	var creditKeys, creditShow []string
	narratorCombined := false
	for _, ba := range credits {
		creditKeys = append(creditKeys, fmt.Sprintf("%d:%s:%d", ba.AuthorID, ba.Role, ba.Position))
		creditShow = append(creditShow, fmt.Sprintf("%s @%d", nameOf(ba.AuthorID), ba.Position))
		if !idx.combined[ba.AuthorID] {
			continue
		}
		if !combinedIsAuthorRole(ba.Role) {
			narratorCombined = true
			continue
		}
		if !inRec[ba.AuthorID] {
			inRec[ba.AuthorID] = true
			recIDs = append(recIDs, ba.AuthorID)
		}
	}
	primary := 0
	if b.AuthorID != nil {
		primary = *b.AuthorID
	}
	if primary != 0 && idx.combined[primary] && !inRec[primary] {
		inRec[primary] = true
		recIDs = append(recIDs, primary)
	}

	r := repairs.Row{RowID: bookID, BookIDs: []string{bookID}, Title: b.Title, Risk: repairs.RiskLow,
		Current: map[string]string{"credits": strings.Join(creditShow, ", ")}}
	if primary != 0 {
		r.Author = nameOf(primary)
		r.Current["primary"] = r.Author
	}
	fp := []string{bookID, strconv.Itoa(primary), "c", strings.Join(creditKeys, ",")}
	for _, id := range recIDs {
		fp = append(fp, "rec", strconv.Itoa(id), nameOf(id))
	}
	finish := func(skip, why string, ok bool) (repairs.Row, bool, error) {
		r.Skipped, r.SkipReason = skip, why
		if r.Reason == "" {
			r.Reason = why
		}
		r.Proposed, r.Detail = nil, nil
		r.Fingerprint = fingerprintStrings(append(fp, "skip", skip, why)...)
		return r, ok, nil
	}
	if len(recIDs) == 0 {
		return finish(combinedSkipNotCombined, "the book no longer credits a combined author record", false)
	}
	if narratorCombined {
		r.Evidence = append(r.Evidence, "a combined record is also credited as narrator; that credit is left as it is")
	}

	// Credited author-role rows by letters key (other than combined
	// records): a part spelled "JN Chaney" is credited when "J. N. Chaney"
	// is, even as a separate author row.
	creditedByKey := map[string]int{}
	for _, ba := range credits {
		if inRec[ba.AuthorID] || !combinedIsAuthorRole(ba.Role) {
			continue
		}
		if k := authorcredit.LettersKey(nameOf(ba.AuthorID)); k != "" {
			if _, dup := creditedByKey[k]; !dup {
				creditedByKey[k] = ba.AuthorID
			}
		}
	}

	var recs []combinedRecordPlan
	for _, id := range recIDs {
		name := nameOf(id)
		if idx.namesTitle(name) {
			return finish(combinedSkipTitle, fmt.Sprintf("%q is (or begins) the title of a book or series in the library, not a list of authors", name), true)
		}
		names, skip, why := combinedClassify(name)
		if skip != "" {
			return finish(skip, why, true)
		}
		rec := combinedRecordPlan{id: id, name: name}
		for _, n := range names {
			rec.parts = append(rec.parts, combinedPart{name: n, creditedID: creditedByKey[authorcredit.LettersKey(n)]})
		}
		recs = append(recs, rec)
		fp = append(fp, "names", strings.Join(names, "\x1f"))
	}

	locks, err := database.LoadFieldLocks(store, bookID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read field locks of %s: %w", bookID, err)
	}
	if locks.Locked(database.FieldKeyAuthorName) && !locks.RepairLocked(database.FieldKeyAuthorName) {
		return finish(junkSkipUserLocked, "the author carries a user override; it is never rewritten", true)
	}

	// Display only (never fingerprinted): what each uncredited part resolves
	// to. Ambiguity holds the row; its reason (in the fingerprint) names the
	// spellings, so a change in them is a change of decision.
	credited, uncredited, created := 0, 0, 0
	display := map[int]string{}
	for ri := range recs {
		for pi := range recs[ri].parts {
			p := recs[ri].parts[pi]
			if p.creditedID != 0 {
				credited++
				continue
			}
			uncredited++
			existing, gerr := store.GetAuthorByName(p.name)
			if gerr != nil {
				return repairs.Row{}, false, fmt.Errorf("read author %q: %w", p.name, gerr)
			}
			if existing != nil && !inRec[existing.ID] && !idx.combined[existing.ID] {
				display[existing.ID] = existing.Name
				continue
			}
			vs := combinedVariants(idx, p.name)
			switch len(vs) {
			case 0:
				created++
				r.Evidence = append(r.Evidence, fmt.Sprintf("no author is named %q; apply creates it", p.name))
			case 1:
				display[vs[0].ID] = vs[0].Name
				r.Evidence = append(r.Evidence, fmt.Sprintf("%q resolves to the existing author %q (id %d)", p.name, vs[0].Name, vs[0].ID))
			default:
				var spell []string
				for _, v := range vs {
					spell = append(spell, fmt.Sprintf("%q (id %d)", v.Name, v.ID))
				}
				return finish(swapSkipAmbiguousAuthor, fmt.Sprintf("no author is named %q and %d existing authors are spelled like it: %s",
					p.name, len(vs), strings.Join(spell, ", ")), true)
			}
		}
	}

	primaryMoves := primary != 0 && inRec[primary]
	if primaryMoves {
		if skip, why, serr := swapStaleSeriesHold(store, bookID); serr != nil {
			return repairs.Row{}, false, serr
		} else if skip != "" {
			return finish(skip, why, true)
		}
	}

	switch {
	case created > 0:
		r.Class = combinedClassNewAuthors
		r.Risk = repairs.RiskReview
	case uncredited == 0:
		r.Class = combinedClassDuplicate
	case credited == 0:
		r.Class = combinedClassOnly
	default:
		r.Class = combinedClassPartial
	}

	// The proposed credits, with a placeholder id per author apply creates.
	targets := make([][]int, len(recs))
	newNames := map[int]string{}
	next := -1
	for ri := range recs {
		for _, p := range recs[ri].parts {
			switch {
			case p.creditedID != 0:
				targets[ri] = append(targets[ri], p.creditedID)
			default:
				id := 0
				if existing, _ := store.GetAuthorByName(p.name); existing != nil && !inRec[existing.ID] && !idx.combined[existing.ID] {
					id = existing.ID
				} else if vs := combinedVariants(idx, p.name); len(vs) == 1 {
					id = vs[0].ID
				}
				if id == 0 {
					id = next
					newNames[id] = p.name + " (new author)"
					next--
				}
				targets[ri] = append(targets[ri], id)
			}
		}
	}
	proposed := combinedNextCredits(credits, bookID, recIDsOf(recs), targets)
	var show []string
	for _, ba := range proposed {
		n := newNames[ba.AuthorID]
		if n == "" {
			if d, ok := display[ba.AuthorID]; ok {
				n = d
			} else {
				n = nameOf(ba.AuthorID)
			}
		}
		show = append(show, fmt.Sprintf("%s @%d", n, ba.Position))
	}
	r.Proposed = map[string]string{"credits": strings.Join(show, ", ")}
	var recNames []string
	for _, rec := range recs {
		recNames = append(recNames, fmt.Sprintf("%q (id %d)", rec.name, rec.id))
	}
	switch r.Class {
	case combinedClassDuplicate:
		r.Reason = fmt.Sprintf("credits the combined record %s and every author in it separately; the combined credit is removed",
			strings.Join(recNames, ", "))
	default:
		r.Reason = fmt.Sprintf("credits the combined record %s; it is replaced by its authors", strings.Join(recNames, ", "))
	}
	if primaryMoves {
		for ri, rec := range recs {
			if rec.id == primary {
				pn := recs[ri].parts[0].name
				r.Proposed["primary"] = pn
				r.Reason += fmt.Sprintf("; the primary author moves to %q", pn)
				if len(proposed) > 0 && proposed[0].AuthorID != targets[ri][0] {
					r.Reason += " (not the first credit: the credit order of the other authors is kept)"
				}
			}
		}
	}
	r.Detail = &combinedDecision{bookID: bookID, primary: b.AuthorID, credits: credits, records: recs}
	r.Fingerprint = fingerprintStrings(append(fp, "apply", r.Class, strconv.FormatBool(primaryMoves))...)
	return r, true, nil
}

func recIDsOf(recs []combinedRecordPlan) []int {
	out := make([]int, len(recs))
	for i := range recs {
		out[i] = recs[i].id
	}
	return out
}

// combinedVariants returns the authors spelled like name by letters and
// digits, other than combined records.
func combinedVariants(idx *combinedAuthorIndex, name string) []database.Author {
	var out []database.Author
	for _, a := range idx.byKey[authorcredit.LettersKey(name)] {
		if !idx.combined[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

// combinedNextCredits returns the credit list with the combined records
// removed and their authors in place, positions renumbered 0..n-1.
//
// recIDs are the combined records; targets[i] the author ids of recIDs[i] in
// the combined string's order (already-credited parts are their credited
// ids). Rows are stably sorted by position; inside a run of equal positions
// the rows that are parts of a combined record are put in the combined
// string's order among their own slots (other rows keep theirs). A combined
// author-role credit is replaced, in place, by those of its parts no
// author-role row credits; a combined record held only as the primary
// AuthorID puts its missing parts first. Non-author-role rows (a narrator
// credit, even of a combined record) are kept as they are.
func combinedNextCredits(cur []database.BookAuthor, bookID string, recIDs []int, targets [][]int) []database.BookAuthor {
	isRec := map[int]int{}
	for i, id := range recIDs {
		isRec[id] = i
	}
	// rank of a part: (record order, part order).
	type rank struct{ rec, part int }
	partRank := map[int]rank{}
	for ri := range targets {
		for pi, id := range targets[ri] {
			if _, ok := partRank[id]; !ok {
				partRank[id] = rank{ri, pi}
			}
		}
	}
	rows := append([]database.BookAuthor(nil), cur...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Position < rows[j].Position })
	for start := 0; start < len(rows); {
		end := start
		for end < len(rows) && rows[end].Position == rows[start].Position {
			end++
		}
		var slots []int
		var parts []database.BookAuthor
		for i := start; i < end; i++ {
			if _, ok := partRank[rows[i].AuthorID]; ok && combinedIsAuthorRole(rows[i].Role) {
				slots = append(slots, i)
				parts = append(parts, rows[i])
			}
		}
		sort.SliceStable(parts, func(a, b int) bool {
			ra, rb := partRank[parts[a].AuthorID], partRank[parts[b].AuthorID]
			if ra.rec != rb.rec {
				return ra.rec < rb.rec
			}
			return ra.part < rb.part
		})
		for k, s := range slots {
			rows[s] = parts[k]
		}
		start = end
	}

	credited := map[int]bool{}
	for _, ba := range rows {
		if _, rec := isRec[ba.AuthorID]; !rec && combinedIsAuthorRole(ba.Role) {
			credited[ba.AuthorID] = true
		}
	}
	placed := map[int]bool{}
	missing := func(ri int, role string, out []database.BookAuthor) []database.BookAuthor {
		if role == "" {
			role = "author"
		}
		for _, id := range targets[ri] {
			if credited[id] {
				continue
			}
			credited[id] = true
			out = append(out, database.BookAuthor{BookID: bookID, AuthorID: id, Role: role})
		}
		return out
	}
	out := make([]database.BookAuthor, 0, len(rows)+4)
	for _, ba := range rows {
		ri, rec := isRec[ba.AuthorID]
		if !rec || !combinedIsAuthorRole(ba.Role) {
			out = append(out, ba)
			continue
		}
		if placed[ba.AuthorID] {
			continue
		}
		placed[ba.AuthorID] = true
		out = missing(ri, ba.Role, out)
	}
	var front []database.BookAuthor
	for ri, id := range recIDs {
		if !placed[id] {
			placed[id] = true
			front = missing(ri, "author", front)
		}
	}
	out = append(front, out...)
	for i := range out {
		out[i].Position = i
	}
	return out
}

// Apply writes one fresh row: the parts resolved (an author created only when
// none exists, journaled), the credit list rewritten under the book's author
// lock (compare-and-set, journaled first), then the primary moved. Every
// check that can refuse the row runs before the first write.
func (f *combinedAuthorFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*combinedDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", combinedAuthorFixerID, fresh.RowID)
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
	if locks.Locked(database.FieldKeyAuthorName) && !locks.RepairLocked(database.FieldKeyAuthorName) {
		return fmt.Errorf("%w: book %s author was locked since the plan", repairs.ErrChangedSincePlan, id)
	}
	book, err := store.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("read book %s: %w", id, err)
	}
	if book == nil || book.IsSoftDeleted() {
		return fmt.Errorf("%w: book %s is gone", repairs.ErrChangedSincePlan, id)
	}
	if !sameIntPtr(book.AuthorID, d.primary) {
		return fmt.Errorf("%w: book %s primary author changed", repairs.ErrChangedSincePlan, id)
	}
	cur, err := store.GetBookAuthors(id)
	if err != nil {
		return fmt.Errorf("read credits of %s: %w", id, err)
	}
	if !sameCreditList(cur, d.credits) {
		return fmt.Errorf("%w: book %s credits changed", repairs.ErrChangedSincePlan, id)
	}
	idx, err := f.cachedIndex()
	if err != nil {
		return err
	}
	isRec := map[int]bool{}
	for _, rec := range d.records {
		isRec[rec.id] = true
	}

	targets := make([][]int, len(d.records))
	byID := map[int]*database.Author{}
	mintedHere := false
	for ri, rec := range d.records {
		for _, p := range rec.parts {
			if p.creditedID != 0 {
				targets[ri] = append(targets[ri], p.creditedID)
				continue
			}
			a, minted, rerr := f.resolvePart(w, store, idx, id, p.name, isRec)
			if rerr != nil {
				if mintedHere {
					return fmt.Errorf("%w: book %s: an author was created before: %w", repairs.ErrPartiallyApplied, id, rerr)
				}
				return rerr
			}
			mintedHere = mintedHere || minted
			byID[a.ID] = a
			targets[ri] = append(targets[ri], a.ID)
		}
	}

	var oldPrimary int
	if d.primary != nil {
		oldPrimary = *d.primary
	}
	var newPrimary *database.Author
	primaryChanged := false
	for ri, rec := range d.records {
		if rec.id == oldPrimary && len(targets[ri]) > 0 {
			primaryChanged = true
			pid := targets[ri][0]
			if a, ok := byID[pid]; ok {
				newPrimary = a
			} else {
				a, gerr := store.GetAuthorByID(pid)
				if gerr != nil {
					return fmt.Errorf("read author %d: %w", pid, gerr)
				}
				if a == nil {
					return fmt.Errorf("%w: book %s: author %d is gone", repairs.ErrChangedSincePlan, id, pid)
				}
				newPrimary = a
			}
		}
	}
	recIDs := recIDsOf(d.records)
	partial := func(what string, err error) error {
		return fmt.Errorf("%w: book %s: %s: %w", repairs.ErrPartiallyApplied, id, what, err)
	}
	_, err = w.ModifyCredits(id, func(cur []database.BookAuthor) ([]database.BookAuthor, repairs.UndoEntry, error) {
		if !sameCreditList(cur, d.credits) {
			return nil, repairs.UndoEntry{}, fmt.Errorf("%w: book %s credits changed", repairs.ErrChangedSincePlan, id)
		}
		b, gerr := store.GetBookByID(id)
		if gerr != nil {
			return nil, repairs.UndoEntry{}, fmt.Errorf("read book %s: %w", id, gerr)
		}
		if b == nil || !sameIntPtr(b.AuthorID, d.primary) {
			return nil, repairs.UndoEntry{}, fmt.Errorf("%w: book %s primary author changed", repairs.ErrChangedSincePlan, id)
		}
		next := combinedNextCredits(cur, id, recIDs, targets)
		after := make([]database.BookAuthor, len(next))
		copy(after, next)
		var afterID *int
		if primaryChanged {
			pid := newPrimary.ID
			afterID = &pid
		}
		snap, merr := json.Marshal(undo.TitleRelinkCreditsSnapshot{AuthorID: b.AuthorID, Credits: cur})
		if merr != nil {
			return nil, repairs.UndoEntry{}, fmt.Errorf("encode credits of %s: %w", id, merr)
		}
		into := 0
		if len(targets[0]) > 0 {
			into = targets[0][0]
		}
		// The junk-author fixer's change type: credits off a record that is
		// not a person, with the exact junction after, so the op revert's
		// compare-and-set restore applies as is.
		move, merr := json.Marshal(undo.JunkAuthorCreditsMove{FromAuthorID: recIDs[0], IntoAuthorID: into,
			PrimaryAfter: afterID, PrimaryChanged: primaryChanged, CreditsAfter: &after})
		if merr != nil {
			return nil, repairs.UndoEntry{}, fmt.Errorf("encode credit move of %s: %w", id, merr)
		}
		return next, repairs.UndoEntry{ChangeType: undo.ChangeTypeJunkAuthorCredits, Field: "book_authors",
			Old: string(snap), New: string(move)}, nil
	})
	if err != nil {
		if mintedHere {
			return partial("the credits were not rewritten (an author was created)", err)
		}
		return err
	}
	if primaryChanged {
		if perr := w.SetPrimaryAuthor(id, oldPrimary, newPrimary); perr != nil {
			return partial("credits rewritten but the primary author was not moved", perr)
		}
	}
	return nil
}

// resolvePart returns the author a part names: the author with that name (the
// store's lookup ignores case and spacing), else the one non-combined author
// spelled like it by letters and digits, else a new author, created and
// journaled by mintJournaledAuthor. Two or more same-spelled authors is a
// change since the plan, which held such a row.
func (f *combinedAuthorFixer) resolvePart(w *repairs.Writer, store OpsStore, idx *combinedAuthorIndex, bookID, name string, isRec map[int]bool) (*database.Author, bool, error) {
	existing, err := store.GetAuthorByName(name)
	if err != nil {
		return nil, false, fmt.Errorf("read author %q: %w", name, err)
	}
	if existing != nil && !isRec[existing.ID] && !idx.combined[existing.ID] {
		return existing, false, nil
	}
	f.mintMu.Lock()
	defer f.mintMu.Unlock()
	key := authorcredit.LettersKey(name)
	if id, ok := f.minted[key]; ok {
		a, gerr := store.GetAuthorByID(id)
		if gerr != nil {
			return nil, false, fmt.Errorf("read author %d: %w", id, gerr)
		}
		if a != nil && a.ID == id {
			return a, false, nil
		}
		delete(f.minted, key)
	}
	switch vs := combinedVariants(idx, name); len(vs) {
	case 0:
	case 1:
		a, gerr := store.GetAuthorByID(vs[0].ID)
		if gerr != nil {
			return nil, false, fmt.Errorf("read author %d: %w", vs[0].ID, gerr)
		}
		if a == nil || a.ID != vs[0].ID {
			return nil, false, fmt.Errorf("%w: book %s: author %q (id %d) is gone", repairs.ErrChangedSincePlan, bookID, vs[0].Name, vs[0].ID)
		}
		return a, false, nil
	default:
		return nil, false, fmt.Errorf("%w: book %s: %d authors are spelled like %q", repairs.ErrChangedSincePlan, bookID, len(vs), name)
	}
	if existing != nil {
		// The exact-name row is itself a combined record: never credit it.
		return nil, false, fmt.Errorf("%w: book %s: %q names a combined record", repairs.ErrChangedSincePlan, bookID, name)
	}
	a, err := mintJournaledAuthor(w, store, bookID, name)
	if err != nil {
		return nil, false, err
	}
	f.minted[key] = a.ID
	return a, true, nil
}
