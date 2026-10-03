// file: internal/plugins/maintenance/swapped_title_author_fixer.go
// version: 1.0.0
// guid: a80ddfb1-95dc-402f-941a-142b9388bcf0
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
// maintenance.purge-empty-authors.
//
// Undo: the credit move, any author created and the title are all journaled
// in the apply op's journal, so POST /operations/<id>/revert undoes the row.
// "Undo last apply" refuses these batches (apply_op_journaled): reverting the
// title from history alone would leave the credits moved.
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
		"one). Books with no provider author on record, fragments, multi-author credits the splitter cannot " +
		"separate and user-locked titles or authors are listed, not changed. iTunes books get database fields " +
		"only; no file is touched."
}

// ITunesDatabaseOnly: the owner cleared this fixer to fix the database rows
// of books under books/itunes/** (2026-10-03). It writes no file and no tag.
func (f *swappedTitleAuthorFixer) ITunesDatabaseOnly() bool { return true }

// swapAuthorIndex groups every author by the letters and digits of the name,
// so a provider spelling ("J.N. Chaney") finds an existing row spelled with
// other punctuation or spacing.
type swapAuthorIndex struct {
	byKey map[string][]database.Author
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
	idx := &swapAuthorIndex{byKey: make(map[string][]database.Author, len(all))}
	for i := range all {
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
// provider recorded for it: applicable when the provider also recorded a
// usable author, held otherwise.
func (f *swappedTitleAuthorFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	idx, cands, err := f.junk.buildJunkIndex()
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

	rows := make([]repairs.Row, len(cands))
	keep := make([]bool, len(cands))
	var done atomic.Int64
	// Each worker writes only rows[i] and keep[i] for its own i; idx and
	// aidx are read-only here apart from the junk fixer's author-name cache,
	// which authorName locks.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(_ context.Context, i int) error {
		defer done.Add(1)
		r, swapped, err := f.evaluate(idx, aidx, cands[i])
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
	r, _, err := f.evaluate(idx, aidx, b.Core())
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
}

// evaluate decides one book. Plan and Replan both call it, so a row planned
// and a row re-planned from the same state carry the same fingerprint.
// swapped is false for a book without the swapped shape: Plan leaves it out.
//
// Nothing that another row's apply changes goes into the fingerprint: whether
// an author with the provider's name already exists flips when a sibling row
// creates it, so the fingerprint carries the names, never their ids.
func (f *swappedTitleAuthorFixer) evaluate(idx *junkIndex, aidx *swapAuthorIndex, b database.BookCore) (repairs.Row, bool, error) {
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

	narrators := narratorsOf(b.Narrator)
	kind := metadata.ClassifyJunkTitleFor(b.Title, narrators)
	if kind == metadata.JunkNone {
		return finish(swapSkipNotSwapped, "the title is no longer junk", false)
	}
	states, err := store.GetMetadataFieldStates(b.ID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read field states of %s: %w", b.ID, err)
	}
	fetchedTitle, fetchedAuthor := "", ""
	for i := range states {
		v, ok := metastate.Decode(states[i].FetchedValue).(string)
		if !ok || !states[i].HasProviderValue() {
			continue
		}
		switch states[i].Field {
		case "title":
			fetchedTitle = strings.TrimSpace(v)
		case "author_name":
			fetchedAuthor = strings.TrimSpace(v)
		}
	}
	fp = append(fp, string(kind), fetchedTitle, fetchedAuthor)

	// The swapped shape: the stored author IS the provider's title, by its
	// letters and digits ("Ultimate Level 1_ Divine Creation" is "Ultimate
	// Level 1: Divine Creation"; a folder or tag cannot hold the colon). A
	// catalog title's bracketed edition marker is dropped first, as the
	// junk-title fixer does.
	catalog := strings.TrimSpace(catalogEditionRe.ReplaceAllString(fetchedTitle, ""))
	ak := junkLettersKey(author)
	if ak == "" || fetchedTitle == "" || (ak != junkLettersKey(fetchedTitle) && ak != junkLettersKey(catalog)) {
		// A near miss is listed, never written: the stored author is the
		// provider's title without its subtitle, or with one the provider
		// lacks ("Ultimate Level 1" against "Ultimate Level 1: Divine
		// Creation"). It looks swapped, but which title is right is a
		// person's call. Anything further off is not this fixer's book.
		if ak != "" && fetchedTitle != "" && titleAgreesWithAny(author, []string{fetchedTitle, catalog}) {
			r.Reason = fmt.Sprintf("the title %q is junk (%s) and the author %q is close to the title a provider recorded (%q)",
				b.Title, kind, author, fetchedTitle)
			return finish(junkSkipNeedsManual, fmt.Sprintf(
				"the author %q is not exactly the provider's title %q (a subtitle differs); a person decides", author, fetchedTitle), true)
		}
		return finish(swapSkipNotSwapped, "the stored author is not the title a provider recorded for this book", false)
	}
	r.Reason = fmt.Sprintf("the title %q is junk (%s) and the author %q is the title a provider recorded (%q)",
		b.Title, kind, author, fetchedTitle)
	r.Evidence = []string{
		fmt.Sprintf("stored title %q is junk (%s)", b.Title, kind),
		fmt.Sprintf("stored author %q equals the provider title %q by letters and digits", author, fetchedTitle),
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

	locks, err := database.LoadFieldLocks(store, b.ID)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read field locks of %s: %w", b.ID, err)
	}
	if locks.Locked(database.FieldKeyTitle) {
		return finish(junkSkipUserLocked, "the title carries a user override; it is never rewritten", true)
	}
	if locks.Locked(database.FieldKeyAuthorName) {
		return finish(junkSkipUserLocked, "the author carries a user override; it is never rewritten", true)
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
	// would match it on every row.
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

	if fetchedAuthor == "" {
		return finish(swapSkipNoProviderAuthor, "no provider author on record", true)
	}
	if authorname.IsPlaceholderAuthor(fetchedAuthor) {
		return finish(swapSkipNoProviderAuthor, fmt.Sprintf("the provider author on record is a placeholder (%q)", fetchedAuthor), true)
	}
	fk := junkLettersKey(fetchedAuthor)
	if fk == junkLettersKey(catalog) || fk == junkLettersKey(b.Title) || fk == ak {
		return finish(junkSkipNeedsManual, fmt.Sprintf("the provider author %q is the title itself", fetchedAuthor), true)
	}

	newTitle := catalog
	if len([]rune(newTitle)) < 2 || metadata.ClassifyJunkTitleFor(newTitle, narrators) != metadata.JunkNone {
		return finish(junkSkipNeedsManual, fmt.Sprintf("the provider title %q is junk itself", fetchedTitle), true)
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
	if len(credits) > 0 && !holds {
		return finish(junkSkipNeedsManual, fmt.Sprintf(
			"the book's credit list does not include the author %q its title sits in; a person sorts out its credits", author), true)
	}

	r.Proposed = map[string]string{"title": newTitle, "author": strings.Join(display, ", ")}
	r.Reason = fmt.Sprintf("the title %q is junk (%s) and the author %q is the title a provider recorded; "+
		"the provider recorded the title %q by %q", b.Title, kind, author, fetchedTitle, fetchedAuthor)
	if newTitle != fetchedTitle {
		r.Reason += fmt.Sprintf("; the edition marker is dropped (%q)", newTitle)
	}
	if len(names) > 1 {
		r.Reason += fmt.Sprintf("; %d authors credited", len(names))
	}
	r.Detail = &swapDecision{bookID: b.ID, oldTitle: b.Title, newTitle: newTitle, oldAuthorID: oldAuthorID,
		names: names, credits: credits}
	r.Fingerprint = fingerprintStrings(append(fp, "apply", newTitle, strings.Join(names, "\x1f"))...)
	return r, true, nil
}

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
// author created only when no author has the name, journaled); the credit
// move off the author record that held the title (journaled inside the
// book's author lock, compare-and-set on the credit list); the primary
// author; the title (journaled, compare-and-set, user lock re-checked inside
// the write). Every check that can refuse the row runs before the first
// write, so a refusal writes nothing; a failure after the credit move is
// reported as partially applied.
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
	if locks.Locked(database.FieldKeyTitle) || locks.Locked(database.FieldKeyAuthorName) {
		return fmt.Errorf("%w: book %s title or author was locked by the user", repairs.ErrChangedSincePlan, id)
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

	primaryChanged := false
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
		// The junk-author fixer's change type: the same move (one credit off
		// a record that is not this book's author, the exact junction after),
		// so the op revert's compare-and-set restore applies as is.
		move, merr := json.Marshal(undo.JunkAuthorCreditsMove{FromAuthorID: d.oldAuthorID, IntoAuthorID: targets[0].ID,
			PrimaryAfter: afterID, PrimaryChanged: primaryChanged, CreditsAfter: &after})
		if merr != nil {
			return nil, repairs.UndoEntry{}, fmt.Errorf("encode credit move of %s: %w", id, merr)
		}
		return next, repairs.UndoEntry{ChangeType: undo.ChangeTypeJunkAuthorCredits, Field: "book_authors",
			Old: string(snap), New: string(move)}, nil
	})
	if err != nil {
		return err
	}
	if primaryChanged {
		if perr := w.SetPrimaryAuthor(id, d.oldAuthorID, targets[0]); perr != nil {
			return fmt.Errorf("%w: book %s credits moved but the primary author was not: %w", repairs.ErrPartiallyApplied, id, perr)
		}
	}
	// The title is journaled as a metadata_update so the op revert puts it
	// back with the credits (undo-last-apply refuses this book's batches once
	// its credits are journaled). Journaled before the write: if the write
	// is then refused, the row names a value the book never left, which the
	// revert's compare-and-set counts as already restored.
	if jerr := w.RecordChange(id, repairs.UndoEntry{ChangeType: "metadata_update", Field: "title",
		Old: d.oldTitle, New: d.newTitle}); jerr != nil {
		return fmt.Errorf("%w: book %s author moved but the title was not journaled: %w", repairs.ErrPartiallyApplied, id, jerr)
	}
	if terr := writeTitleOnly(w, store, id, d.oldTitle, d.newTitle); terr != nil {
		return fmt.Errorf("%w: book %s author moved but the title was not written: %w", repairs.ErrPartiallyApplied, id, terr)
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
// becomes "author": these are the writers). Authors already credited are not
// added twice; other credits keep their order. A book whose credit lived only
// in its primary author id (an empty junction) gets the targets as its
// junction, as a manual edit would leave it.
func swapCredits(cur []database.BookAuthor, bookID string, oldID int, targets []*database.Author) []database.BookAuthor {
	have := map[int]bool{}
	for _, ba := range cur {
		if ba.AuthorID != oldID {
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
