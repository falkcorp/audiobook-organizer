// file: internal/plugins/maintenance/junk_author_fixer.go
// version: 1.5.0
// guid: 7a5912c0-2834-48bf-9378-8daadf7755fa
// last-edited: 2026-09-29

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// --- Repairs lane: junk authors ---
//
// WHY. Production holds author rows that are not people: series names ("Demon
// Cycle"), characters ("Harry Potter"), genres ("Science Fiction"), book
// titles ("Before They Are Hanged", 680 books), placeholders ("read by
// narrator", 452 books), studios ("Big Finish Productions", 223 books),
// narrator credits ("Read by Stephen King") and filename shrapnel ("Track01").
// Measured on 2026-09-29; the counts are in the PR that added this file.
//
// WHAT A ROW IS. One (junk author, book) pair: row id "a<author id>:b<book
// id>", the book's credit to that author. One book per row so the framework
// guard skips only the hands-off book (an iTunes or Big Finish book under
// "GraphicAudio" does not hold the other 93), and so apply partitions by book
// and parallel workers never touch the same book.
//
// THE PROPOSAL. The book is relinked to its real author, found from evidence
// the library already holds, in the owner's order (2026-09-29):
//
//  0. the junk name itself, cleaned ("- Arthur C. Clarke", "(c) 2001 Stephen Hawking",
//     "GraphicAudio [R. A. Salvatore]"), for name-alone (strong) verdicts;
//     it only resolves to an existing author who has books, never creates
//     one, and is never the book's own title;
//  1. file tags: artist / album-artist on the book's own files;
//  2. provider: the cached metadata candidate's author, only when the same
//     candidate's title agrees with the book's;
//  3. siblings: the author of another book in the same version group, the
//     same folder, or the same series (in that order);
//  4. folder: a person-shaped segment of the book's path (author-path-link's
//     parser).
//
// The first source with an acceptable answer decides; two different answers
// from that source hold the row (skipped_ambiguous). Every answer passes the
// title-relink gate (acceptableRelinkName) and must not be a junk row itself,
// and a name the book says is its narrator (its Narrator string, its
// book_narrators rows, or a narrator-role credit) is an answer only when it is
// an author row of its own or the album-artist tag or provider names it, and
// then at review risk (authors read their own books); an artist tag naming
// only the reader holds the row, never falling to a sibling, a path or an
// unlink. Names are matched on one folded key (case, punctuation,
// initials spacing, diacritics: authorjunk.FoldKey), so "BRANDON SANDERSON"
// resolves to the existing row, never a new one; a spelling that differs from
// the row it resolves to is review risk ("possible duplicate"). An answer
// resting on the artist tag (or the cleaned name) alone is review risk unless
// the provider or the path agrees. With no answer at all the junk credit is
// removed and the book is left with no author: a needs_manual row, always
// review risk -- except for a collective credit ("Various Authors",
// "Anonymous"), which is real and is never removed, and a weak verdict, which
// never removes anything. A name that is two people joined by "_" is held for
// maintenance.author-split-scan: relinking it to one would drop the other. A junk author that is a
// series name also gets the book linked to an EXISTING series row of that
// name when the book has none; no series row is ever created.
//
// WEAK VERDICTS. A name that is only junk by library evidence (it is another
// author's title or series) is acted on only when its own books agree: no
// book's file tags or provider name the row itself (folded, so "Bryce
// O'Connor" names "Bryce OConnor"), and at least one names someone else who
// is not the book's narrator. Siblings and paths never confirm: a flat
// folder shared with another author's book says nothing about this name.
// The series and title tables are themselves full of swapped fields -- a
// series named "Brandon Sanderson", a book titled "Joe Abercrombie" -- and
// this is what keeps those real authors off the list.
//
// THE AUTHOR ROW IS NOT DELETED. Deleting cannot be undone: the op revert
// would have to recreate the row, and the creation gate refuses most of these
// names ("Book 1 (Unabridged)"), and a recreated row gets a new id. The
// emptied row stays (it disappears from ABS, whose authors come from books)
// and is left for maintenance.purge-empty-authors, which journals its deletes.
//
// APPLY is journaled (undo.ChangeTypeJunkAuthorCredits, written inside the
// book's author lock before the junction write) and undone by the op revert;
// it fails closed on every read error; it re-plans each row under the scan
// stand-down and refuses one whose fingerprint moved (a row already applied
// no longer credits the junk row, so a second apply is changed_since_plan,
// never a second write); and it never writes a book whose author (or, for
// the series link, series) the user locked.
//
// ABSORBS nothing yet: maintenance.author-strip-merge's title-as-author relink
// (relink_title_as_author) covers one class here (OwnTitles) with the same
// evidence sources and is left in place; retiring it is an owner decision.

const junkAuthorFixerID = "maintenance.repair-junk-authors"

// Fixer-owned skip kinds (the framework adds skipped_itunes and
// skipped_owner_manual).
const (
	junkAuthorSkipUserLocked = "skipped_user_locked"
	junkAuthorSkipAmbiguous  = "skipped_ambiguous"
	// junkAuthorSkipComposite: the junk row is two people joined by "_"; that is
	// maintenance.author-split's job (a relink here would drop one of them).
	junkAuthorSkipComposite = "skipped_composite_credit"
)

// junkAuthorSrcCleaned: the junk name itself with its junk removed ("- Arthur C.
// Clarke", "Terry_Brooks", "GraphicAudio [R. A. Salvatore]").
const junkAuthorSrcCleaned = "cleaned_name"

// Evidence sources, in the owner's order.
const (
	junkAuthorSrcTags     = "file_tags"
	junkAuthorSrcProvider = "provider"
	junkAuthorSrcVersion  = "version_group"
	junkAuthorSrcFolder   = "same_folder"
	junkAuthorSrcSeries   = "same_series"
	junkAuthorSrcPath     = "folder_parser"
)

var junkAuthorSources = []string{junkAuthorSrcCleaned, junkAuthorSrcTags, junkAuthorSrcProvider, junkAuthorSrcVersion, junkAuthorSrcFolder, junkAuthorSrcSeries, junkAuthorSrcPath}

// junkAuthorConfirmingSource: the sources whose naming of the row itself, or of
// someone else, confirms or refutes a weak verdict.
func junkAuthorConfirmingSource(src string) bool {
	return src == junkAuthorSrcTags || src == junkAuthorSrcProvider
}

// junkAuthorCorroborates: the sources that lift a tag-only or cleaned-name answer
// to low risk when they name the same author.
func junkAuthorCorroborates(src string) bool {
	return src == junkAuthorSrcProvider || src == junkAuthorSrcPath
}

// Decisions.
const (
	junkAuthorDecRelink = "relink"
	junkAuthorDecCreate = "relink_new_author"
	junkAuthorDecUnlink = "needs_manual"
)

// junkAuthorIndexTTL bounds how long Replan reuses the library index an
// apply built. Per-book state (credits, files, field states, locks) is read
// fresh for every row; the index only holds the siblings and the author and
// series name maps, which move slowly.
const junkAuthorIndexTTL = 2 * time.Minute

type junkAuthorFixer struct {
	p *Plugin

	idxMu sync.Mutex
	idx   *junkAuthorIndex
	idxAt time.Time
}

func newJunkAuthorFixer(p *Plugin) *junkAuthorFixer { return &junkAuthorFixer{p: p} }

var _ repairs.Fixer = (*junkAuthorFixer)(nil)

func (f *junkAuthorFixer) ID() string    { return junkAuthorFixerID }
func (f *junkAuthorFixer) Title() string { return "Junk authors" }
func (f *junkAuthorFixer) Description() string {
	return "Author records that are not people: series names, characters, genres, book titles, " +
		"placeholders, studios, narrator credits and filename leftovers. Each book is relinked to its " +
		"real author from the name itself (\"- Arthur C. Clarke\"), its file tags, a provider match on its " +
		"title, its sibling books or its folder -- never to one of its narrators. With no evidence the junk " +
		"credit is removed and the book needs an author by hand, except for collective credits (Various " +
		"Authors, Anonymous) and names that are junk only because another author's series or title uses " +
		"them, which are kept. Names joined by \"_\" are left for Author split. The emptied author record " +
		"is kept for Purge empty authors."
}

type junkAuthorParams struct {
	// AuthorIDs limits the plan to these author rows.
	AuthorIDs []int `json:"author_ids,omitempty"`
	// IncludeWeak (default true) plans rows for names that are junk only by
	// library evidence (confirmed by their books).
	IncludeWeak *bool `json:"include_weak,omitempty"`
}

func decodeJunkAuthorParams(raw json.RawMessage) (junkAuthorParams, error) {
	var jp junkAuthorParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &jp); err != nil {
			return jp, fmt.Errorf("%s: invalid params: %w", junkAuthorFixerID, err)
		}
	}
	return jp, nil
}

// ---- library index ----

type junkAuthorIndex struct {
	authorsByID map[int]database.Author
	// byName keys every author row by authorjunk.FoldKey: the pre-create
	// lookup and every answer use the same key, so a spelling variant finds
	// the existing row instead of minting a duplicate.
	byName map[string][]database.Author
	// narratorsByID names the narrator rows (book_narrators holds narrator
	// ids, which are not author ids).
	narratorsByID map[int]string
	seriesByID    map[int]database.Series
	// seriesByNorm keys series rows by authorjunk.Normalize(name).
	seriesByNorm map[string][]database.Series
	byVG         map[string][]database.BookCore
	bySeries     map[int][]database.BookCore
	byDir        map[string][]database.BookCore
	// titleCredits / seriesCredits: normalized title / series name -> primary
	// author id -> live books.
	titleCredits  map[string]map[int]int
	seriesCredits map[string]map[int]int
	// ownBooks: primary-author id -> live books (for Evidence.OwnTitles).
	ownBooks map[int][]database.BookCore
	// strongJunk: rows the name alone flags. Never a relink target.
	strongJunk map[int]authorjunk.Verdict
	// weakJunk: rows junk only by library evidence (not yet confirmed by
	// their books). Plan excludes the confirmed ones as targets; Replan
	// excludes all of them except the row's planned target.
	weakJunk map[int]authorjunk.Verdict
}

func (f *junkAuthorFixer) buildIndex(store OpsStore) (*junkAuthorIndex, error) {
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("list authors: %w", err)
	}
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	series, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	narrators, err := store.ListNarrators()
	if err != nil {
		return nil, fmt.Errorf("list narrators: %w", err)
	}
	idx := &junkAuthorIndex{
		narratorsByID: make(map[int]string, len(narrators)),
		authorsByID:   make(map[int]database.Author, len(authors)), byName: map[string][]database.Author{},
		seriesByID: make(map[int]database.Series, len(series)), seriesByNorm: map[string][]database.Series{},
		byVG: map[string][]database.BookCore{}, bySeries: map[int][]database.BookCore{}, byDir: map[string][]database.BookCore{},
		titleCredits: map[string]map[int]int{}, seriesCredits: map[string]map[int]int{},
		ownBooks: map[int][]database.BookCore{}, strongJunk: map[int]authorjunk.Verdict{},
		weakJunk: map[int]authorjunk.Verdict{},
	}
	for _, n := range narrators {
		idx.narratorsByID[n.ID] = n.Name
	}
	for _, a := range authors {
		idx.authorsByID[a.ID] = a
		if k := authorjunk.FoldKey(a.Name); k != "" {
			idx.byName[k] = append(idx.byName[k], a)
		}
		if v := authorjunk.ClassifyName(a.Name); v.Junk() {
			idx.strongJunk[a.ID] = v
		}
	}
	for _, s := range series {
		idx.seriesByID[s.ID] = s
		k := authorjunk.Normalize(s.Name)
		idx.seriesByNorm[k] = append(idx.seriesByNorm[k], s)
	}
	bump := func(m map[string]map[int]int, key string, id int) {
		if key == "" {
			return
		}
		if m[key] == nil {
			m[key] = map[int]int{}
		}
		m[key][id]++
	}
	for i := range books {
		b := books[i]
		if b.IsSoftDeleted() {
			continue
		}
		if g := b.VersionGroupID; g != nil && *g != "" {
			idx.byVG[*g] = append(idx.byVG[*g], b)
		}
		if b.SeriesID != nil {
			idx.bySeries[*b.SeriesID] = append(idx.bySeries[*b.SeriesID], b)
		}
		if b.FilePath != "" {
			d := bookFolder(b.FilePath)
			idx.byDir[d] = append(idx.byDir[d], b)
		}
		if b.AuthorID != nil {
			idx.ownBooks[*b.AuthorID] = append(idx.ownBooks[*b.AuthorID], b)
			bump(idx.titleCredits, authorjunk.Normalize(b.Title), *b.AuthorID)
			if b.SeriesID != nil {
				bump(idx.seriesCredits, authorjunk.Normalize(idx.seriesByID[*b.SeriesID].Name), *b.AuthorID)
			}
		}
	}
	for _, a := range authors {
		if _, strong := idx.strongJunk[a.ID]; strong {
			continue
		}
		if v := authorjunk.Classify(a.Name, idx.evidence(a)); v.Junk() {
			idx.weakJunk[a.ID] = v
		}
	}
	return idx, nil
}

// cachedIndex returns the apply-time index, rebuilt after junkAuthorIndexTTL.
func (f *junkAuthorFixer) cachedIndex(store OpsStore) (*junkAuthorIndex, error) {
	f.idxMu.Lock()
	defer f.idxMu.Unlock()
	if f.idx != nil && time.Since(f.idxAt) < junkAuthorIndexTTL {
		return f.idx, nil
	}
	idx, err := f.buildIndex(store)
	if err != nil {
		return nil, err
	}
	f.idx, f.idxAt = idx, time.Now()
	return idx, nil
}

// evidence builds the library evidence authorjunk.Classify reads for a.
func (idx *junkAuthorIndex) evidence(a database.Author) authorjunk.Evidence {
	n := authorjunk.Normalize(a.Name)
	ev := authorjunk.Evidence{}
	if own := idx.ownBooks[a.ID]; len(own) > 0 {
		all := true
		for i := range own {
			if !bookTitleNamesAuthor(a.Name, own[i], idx.seriesByID) {
				all = false
				break
			}
		}
		ev.OwnTitles = all
	}
	for id, c := range idx.titleCredits[n] {
		if id != a.ID {
			ev.TitleOfOtherAuthor += c
		}
	}
	for id, c := range idx.seriesCredits[n] {
		if id != a.ID {
			ev.SeriesOfOtherAuthor += c
		}
	}
	return ev
}

// ---- per-book decision ----

// junkAuthorBookDecision is one (junk author, book) row's plan. It is also the row's
// Detail from Replan to Apply.
type junkAuthorBookDecision struct {
	Author  database.Author
	Verdict authorjunk.Verdict
	Book    database.BookCore
	// Credits is the junction the decision was made on; Apply refuses a
	// junction that differs.
	Credits []database.BookAuthor
	// Primary is the book's primary AuthorID at plan time.
	Primary *int

	Decision string
	Target   database.Author // ID 0 with Decision create
	Source   string
	// Others lists later sources that named someone else ("name [source]").
	Others []string
	// Candidates lists the distinct answers of an ambiguous source.
	Candidates []string
	// SelfNamed: the file tags or the provider named the junk row itself,
	// however spelled (refutes a weak verdict).
	SelfNamed bool
	// OtherNamed: the file tags or the provider named a real person who is
	// not the book's narrator (confirms a weak verdict).
	OtherNamed bool
	// Corroborated: the provider or the path named the chosen author too.
	Corroborated bool
	// Variant is the evidence's spelling when it differs from the name of the
	// existing row it resolved to (a possible duplicate).
	Variant string
	// NarratorAnswer: the chosen author is also named as the book's reader
	// (a self-narrated book): kept, at review risk.
	NarratorAnswer bool
	// NarratorDropped names the tag / provider answer dropped because it is
	// only the book's narrator. The row is then held, never decided from a
	// sibling or a path and never unlinked.
	NarratorDropped string

	AuthorLocked, SeriesLocked bool
	// Series is the existing series row to link, when proposed.
	Series *database.Series

	Skip, SkipReason string
	// ReadErr is set when a read failed: the row is not applicable.
	ReadErr error
}

type junkAuthorEvidenceReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBookByID(id string) (*database.Book, error)
	database.MetadataFieldStateReader
}

// decideBook reads one book's evidence and decides its row. Every read error
// is fatal to the row (fail closed). junk is the set of author ids no answer
// may resolve to.
func decideBook(r junkAuthorEvidenceReader, idx *junkAuthorIndex, a database.Author, v authorjunk.Verdict, book database.BookCore, junk func(int) bool) junkAuthorBookDecision {
	d := junkAuthorBookDecision{Author: a, Verdict: v, Book: book}
	fail := func(what string, err error) junkAuthorBookDecision {
		d.ReadErr = fmt.Errorf("%s: %w", what, err)
		d.Skip, d.SkipReason = repairs.SkipGuardUnreadable, d.ReadErr.Error()
		return d
	}
	credits, err := r.GetBookAuthors(book.ID)
	if err != nil {
		return fail("read credits", err)
	}
	d.Credits = credits
	full, err := r.GetBookByID(book.ID)
	if err != nil {
		return fail("read book", err)
	}
	if full == nil {
		return fail("read book", errors.New("book not found"))
	}
	d.Primary = full.AuthorID
	d.Book.SeriesID = full.SeriesID
	d.Book.Title = full.Title
	d.Book.Narrator = full.Narrator
	locks, err := database.LoadFieldLocks(r, book.ID)
	if err != nil {
		return fail("read field locks", err)
	}
	d.AuthorLocked = locks.Locked(database.FieldKeyAuthorName)
	d.SeriesLocked = locks.Locked(database.FieldKeySeriesName)
	files, err := r.GetBookFiles(book.ID)
	if err != nil {
		return fail("read files", err)
	}
	states, err := r.GetMetadataFieldStates(book.ID)
	if err != nil {
		return fail("read field states", err)
	}

	bookNarrators, err := r.GetBookNarrators(book.ID)
	if err != nil {
		return fail("read narrators", err)
	}
	// Two people joined by "_" ("Terry Pratchett_ Jacqueline Simpson"): a
	// relink would keep one and drop the other. author-split's job.
	if parts, ok := authorjunk.SplitUnderscoreCredit(a.Name); ok {
		d.Skip, d.SkipReason = junkAuthorSkipComposite, fmt.Sprintf("%q credits %d people joined by \"_\" (%s): split it with maintenance.author-split-scan",
			a.Name, len(parts), strings.Join(parts, "; "))
		return d
	}
	narrators := junkAuthorNarratorKeys(full, credits, bookNarrators, idx)
	// A narrator-role credit's own author row is not "an author row" for
	// this book: it is the reader's.
	narratorCreditIDs := map[int]bool{}
	for _, ba := range credits {
		if isNarratorRole(ba.Role) {
			narratorCreditIDs[ba.AuthorID] = true
		}
	}
	// acceptableRelinkName compares against the whole Narrator string; the
	// narrator rule here is the fixer's own (below), so the gate sees none.
	gateBook := d.Book
	gateBook.Narrator = nil
	titleKey := authorjunk.FoldKey(d.Book.Title)

	selfKey := authorjunk.FoldKey(a.Name)
	weak := v.Strength == authorjunk.Weak
	type answer struct {
		row    database.Author
		create bool
		// variant: the evidence spells the resolved row's name differently.
		variant string
		// narrator: the name is also the book's reader.
		narrator bool
	}
	bySource := map[string]map[string]answer{}
	// add weighs one answer. namesAuthor: the source names the author field
	// itself (the album-artist tag, the provider's author) rather than the
	// performer (the artist tag), so a narrator it names is kept.
	add := func(src, name string, namesAuthor bool) {
		name = strings.TrimSpace(name)
		key := authorjunk.FoldKey(name)
		if key == "" {
			return
		}
		if key == selfKey && src != junkAuthorSrcCleaned {
			// The row itself, however spelled. For a weak verdict that is
			// evidence the name is a person; a strong verdict's variant is
			// the cleaned name again and is weighed like any answer.
			if junkAuthorConfirmingSource(src) {
				d.SelfNamed = true
			}
			if weak {
				return
			}
		}
		if authorjunk.ClassifyName(name).Junk() {
			return
		}
		// The cleaned name folds to the junk name by construction; the gate's
		// same-as-junk check is for evidence, not for the name itself.
		gateJunk := a
		if src == junkAuthorSrcCleaned {
			gateJunk.Name = ""
		}
		if !acceptableRelinkName(name, gateBook, gateJunk, idx.seriesByID) {
			return
		}
		// The cleaned name is the junk text itself: it is not the book's
		// title (or its head) under another name ("- Pet Sematary").
		if src == junkAuthorSrcCleaned && titleKey != "" && strings.HasPrefix(titleKey, key) {
			return
		}
		var others, live []database.Author
		for _, row := range idx.byName[key] {
			if row.ID == a.ID {
				continue // the junk row shares its cleaned name's key
			}
			others = append(others, row)
			if !junk(row.ID) {
				live = append(live, row)
			}
		}
		var ans answer
		switch {
		case len(others) > 0 && len(live) == 0:
			return // resolves only to junk rows
		case len(live) == 1:
			ans = answer{row: live[0]}
			if live[0].Name != name {
				ans.variant = name
			}
		case len(live) > 1:
			// Duplicate rows of one name: pick none (ambiguous), reported by
			// the caller through a sentinel id.
			ans = answer{row: database.Author{ID: -1, Name: name}}
		default:
			// No row: only a person-shaped name may be minted.
			if !personname.LooksLikePersonName(name) {
				return
			}
			ans = answer{row: database.Author{Name: name}, create: true}
		}
		// The cleaned name only RESOLVES, and only to an author who has
		// books: the junk text alone never mints a row.
		if src == junkAuthorSrcCleaned && (ans.create || ans.row.ID <= 0 || len(idx.ownBooks[ans.row.ID]) == 0) {
			return
		}
		if narrators[key] {
			// The book's reader. Still the writer when the name is an author
			// row of its own (not this book's narrator credit) that has books
			// of its own -- an empty row is no evidence the reader writes,
			// the same bar as the cleaned source -- or the source names the
			// author field: authors read their own books.
			existingAuthor := !ans.create && ans.row.ID > 0 && !narratorCreditIDs[ans.row.ID] && len(idx.ownBooks[ans.row.ID]) > 0
			if !existingAuthor && !namesAuthor {
				if junkAuthorConfirmingSource(src) && d.NarratorDropped == "" {
					d.NarratorDropped = name
				}
				return
			}
			ans.narrator = true
		}
		// A narrator answer never confirms a weak verdict: that the file names
		// its reader says nothing about whether the credited name is a person.
		if junkAuthorConfirmingSource(src) && !ans.narrator {
			d.OtherNamed = true
		}
		if bySource[src] == nil {
			bySource[src] = map[string]answer{}
		}
		if prev, ok := bySource[src][key]; ok && prev.variant == "" {
			return // keep the exact spelling when a source has both
		}
		bySource[src][key] = ans
	}

	if !weak {
		if cleaned, ok := authorjunk.CleanedName(a.Name); ok {
			add(junkAuthorSrcCleaned, cleaned, false)
		}
	}
	for i := range files {
		keys := make([]string, 0, len(files[i].RawTags))
		for k := range files[i].RawTags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if lk := strings.ToLower(k); titleRelinkFileTagKeys[lk] {
				add(junkAuthorSrcTags, files[i].RawTags[k], albumArtistTagKeys[lk])
			}
		}
	}
	var fetchedTitle, fetchedAuthor string
	for i := range states {
		switch states[i].Field {
		case "title":
			if s, ok := metastate.Decode(states[i].FetchedValue).(string); ok {
				fetchedTitle = s
			}
		case "author_name":
			if s, ok := metastate.Decode(states[i].FetchedValue).(string); ok {
				fetchedAuthor = s
			}
		}
	}
	if fetchedAuthor != "" && providerTitleAgrees(fetchedTitle, d.Book.Title) {
		add(junkAuthorSrcProvider, fetchedAuthor, true)
	}
	sibs := func(src string, list []database.BookCore) {
		for i := range list {
			sb := list[i]
			if sb.ID == book.ID || sb.AuthorID == nil || *sb.AuthorID == a.ID || junk(*sb.AuthorID) {
				continue
			}
			if row, ok := idx.authorsByID[*sb.AuthorID]; ok {
				add(src, row.Name, false)
			}
		}
	}
	if g := book.VersionGroupID; g != nil && *g != "" {
		sibs(junkAuthorSrcVersion, idx.byVG[*g])
	}
	if book.FilePath != "" {
		sibs(junkAuthorSrcFolder, idx.byDir[bookFolder(book.FilePath)])
	}
	if d.Book.SeriesID != nil {
		sibs(junkAuthorSrcSeries, idx.bySeries[*d.Book.SeriesID])
	}
	paths := []string{book.FilePath}
	for i := range files {
		paths = append(paths, files[i].FilePath)
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		for _, c := range authorPathLinkCandidates(p) {
			add(junkAuthorSrcPath, c.Name, false)
		}
	}

	// A path has many person-shaped segments ("Fantasy/Brent Weeks/Night
	// Angel"): when any of them is an existing author row, the segments that
	// would mint a new row are not answers.
	if ans := bySource[junkAuthorSrcPath]; len(ans) > 1 {
		existing := map[string]answer{}
		for k, v := range ans {
			if !v.create {
				existing[k] = v
			}
		}
		if len(existing) > 0 {
			bySource[junkAuthorSrcPath] = existing
		}
	}

	// The first source with an answer decides.
	for si, src := range junkAuthorSources {
		ans := bySource[src]
		if len(ans) == 0 {
			continue
		}
		if len(ans) > 1 {
			for k := range ans {
				d.Candidates = append(d.Candidates, ans[k].row.Name+" ["+src+"]")
			}
			sort.Strings(d.Candidates)
			d.Source = src
			d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, fmt.Sprintf("%s names more than one author: %s", src, strings.Join(d.Candidates, "; "))
			break
		}
		var key string
		for k := range ans {
			key = k
		}
		chosen := ans[key]
		if chosen.row.ID == -1 {
			d.Source = src
			d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, fmt.Sprintf("%q matches more than one author row", chosen.row.Name)
			break
		}
		d.Source, d.Target, d.Variant, d.NarratorAnswer = src, chosen.row, chosen.variant, chosen.narrator
		d.Decision = junkAuthorDecRelink
		if chosen.create {
			d.Decision = junkAuthorDecCreate
		}
		for _, later := range junkAuthorSources[si+1:] {
			for k, o := range bySource[later] {
				if k != key {
					d.Others = append(d.Others, o.row.Name+" ["+later+"]")
				} else if junkAuthorCorroborates(later) {
					d.Corroborated = true
				}
			}
		}
		sort.Strings(d.Others)
		break
	}
	// The tags or provider named only the book's reader: the answer is theirs
	// to give, so the row is held rather than decided from a sibling or a
	// path ("The Graveyard Book" read by Neil Gaiman is not by the Tim Dorsey
	// book in the same folder) or unlinked.
	if d.NarratorDropped != "" && d.Skip == "" &&
		(d.Decision == "" || (d.Source != junkAuthorSrcCleaned && d.Source != junkAuthorSrcTags && d.Source != junkAuthorSrcProvider)) {
		d.Decision, d.Target, d.Source, d.Variant, d.NarratorAnswer, d.Others, d.Corroborated = "", database.Author{}, "", "", false, nil, false
		d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, fmt.Sprintf("the file tags or provider name only %q, the book's narrator; not decided from siblings or paths", d.NarratorDropped)
	}
	if d.Decision == "" && d.Skip == "" {
		d.Decision = junkAuthorDecUnlink
		switch {
		case weak:
			// A weak verdict is a guess from other authors' titles and series;
			// the books it holds with no evidence keep their credit.
			d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, "no evidence of the real author; a weak (library-evidence) verdict never removes a credit"
		case authorjunk.IsCollectiveCredit(a.Name):
			d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, fmt.Sprintf("no evidence of the real author; %q is a collective credit and is kept", a.Name)
		}
	}
	// The framework guard reads paths and the series name; the author name is
	// evidence too ("Big Finish Productions", "Doctor Who"), and so is the
	// author a relink would give the book. Owner rule: those are applied by
	// hand.
	if d.Skip == "" && (applygate.IsOwnerManualOnly("", a.Name) || applygate.IsOwnerManualOnly("", d.Target.Name)) {
		d.Skip, d.SkipReason = repairs.SkipOwnerManual,
			fmt.Sprintf("credited to %q: Doctor Who / Big Finish / Torchwood; owner applies these by hand", a.Name)
		if d.Target.Name != "" {
			d.SkipReason = fmt.Sprintf("%q -> %q: Doctor Who / Big Finish / Torchwood; owner applies these by hand", a.Name, d.Target.Name)
		}
	}
	// "Unknown Author" is the system's own fallback credit: relinking away
	// from it is fine, leaving the book with no author instead is not.
	if d.Skip == "" && d.Decision == junkAuthorDecUnlink && strings.EqualFold(strings.TrimSpace(a.Name), database.UnknownAuthorName) {
		d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, "no evidence of the real author; the Unknown Author fallback credit is kept"
	}
	if d.AuthorLocked && d.Skip == "" {
		d.Skip, d.SkipReason = junkAuthorSkipUserLocked, "the book's author is locked by the user"
	}
	// A series-name author whose book is relinked to an EXISTING author: link
	// an existing series of that name when the book has none, its series is
	// not locked, and that author already has a live book in the series.
	if d.Skip == "" && d.Decision == junkAuthorDecRelink && d.Verdict.Class == authorjunk.ClassSeriesName &&
		d.Book.SeriesID == nil && !d.SeriesLocked {
		d.Series = pickSeries(idx, a.Name, d.Target.ID)
	}
	return d
}

// bookFolder is the folder a book's audio lives in: its path when that is a
// directory (a multi-file book), else the path's directory. Siblings "in the
// same folder" are books sharing it -- not every book under the same parent,
// which on a flat library root would be the whole library.
func bookFolder(p string) string {
	if linkintegrity.IsAudioFile(p) {
		return filepath.Dir(p)
	}
	return filepath.Clean(p)
}

// junkAuthorNarratorKeys folds every name the book says reads it: its Narrator
// string (split on the usual separators), its narrator-role author credits
// and its book_narrators rows.
func junkAuthorNarratorKeys(full *database.Book, credits []database.BookAuthor, bns []database.BookNarrator, idx *junkAuthorIndex) map[string]bool {
	out := map[string]bool{}
	put := func(name string) {
		if k := authorjunk.FoldKey(name); k != "" {
			out[k] = true
		}
	}
	if full.Narrator != nil {
		put(*full.Narrator)
		for _, part := range narratorSplitRe.Split(*full.Narrator, -1) {
			put(part)
		}
	}
	for _, ba := range credits {
		if isNarratorRole(ba.Role) {
			if a, ok := idx.authorsByID[ba.AuthorID]; ok {
				put(a.Name)
			}
		}
	}
	for _, bn := range bns {
		put(idx.narratorsByID[bn.NarratorID])
	}
	return out
}

// narratorSplitRe splits a Narrator string into names.
var narratorSplitRe = regexp.MustCompile(`(?i)\s*(?:[,;&/|]|\band\b|\bwith\b)\s*`)

// albumArtistTagKeys: the tag keys (of titleRelinkFileTagKeys) that name the
// album's author. The artist tag very often names the performer instead.
var albumArtistTagKeys = map[string]bool{
	"album_artist": true, "albumartist": true, "album artist": true, "tpe2": true, "aart": true,
}

// isNarratorRole: a junction role that credits a reader, not a writer.
func isNarratorRole(role string) bool {
	r := strings.ToLower(strings.TrimSpace(role))
	return strings.Contains(r, "narrat") || r == "reader" || r == "read by" || r == "performer"
}

// isPrimaryAuthorRole: a junction role that may be a book's primary author.
func isPrimaryAuthorRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "author", "co-author", "coauthor":
		return true
	}
	return false
}

// providerTitleAgrees: the provider candidate's title is the book's (whole,
// or the part before a subtitle colon on either side).
func providerTitleAgrees(fetched, title string) bool {
	if fetched == "" || title == "" {
		return false
	}
	head := func(s string) string {
		if i := strings.Index(s, ":"); i > 0 {
			s = s[:i]
		}
		return authorjunk.Normalize(s)
	}
	f, t := authorjunk.Normalize(fetched), authorjunk.Normalize(title)
	return f == t || head(fetched) == head(title)
}

// pickSeries returns the one existing series whose name is the junk name
// (optionally without a trailing "series") AND that already holds a live book
// by the target author. The series table carries its own junk, so a name
// match alone is not enough. nil when there is none or it is not unique.
func pickSeries(idx *junkAuthorIndex, name string, targetID int) *database.Series {
	if targetID <= 0 {
		return nil
	}
	n := authorjunk.Normalize(name)
	cands := append([]database.Series(nil), idx.seriesByNorm[n]...)
	if trimmed, ok := strings.CutSuffix(n, " series"); ok && trimmed != "" {
		cands = append(cands, idx.seriesByNorm[trimmed]...)
	}
	var mine []database.Series
	for _, s := range cands {
		for _, b := range idx.bySeries[s.ID] {
			if b.AuthorID != nil && *b.AuthorID == targetID {
				mine = append(mine, s)
				break
			}
		}
	}
	cands = mine
	if len(cands) != 1 {
		return nil
	}
	s := cands[0]
	return &s
}

// ---- Plan ----

type junkAuthorCandidate struct {
	author  database.Author
	verdict authorjunk.Verdict
	books   []database.BookCore
	readErr error
}

// Plan classifies every author row, reads the evidence of every live book a
// junk row credits (bounded worker pool), confirms weak verdicts against
// that evidence, and returns one row per (junk author, book).
func (f *junkAuthorFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	jp, err := decodeJunkAuthorParams(raw)
	if err != nil {
		return nil, err
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	idx, err := f.buildIndex(store)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", junkAuthorFixerID, err)
	}
	f.idxMu.Lock()
	f.idx, f.idxAt = idx, time.Now()
	f.idxMu.Unlock()

	includeWeak := jp.IncludeWeak == nil || *jp.IncludeWeak
	scope := map[int]bool{}
	for _, id := range jp.AuthorIDs {
		scope[id] = true
	}
	ids := make([]int, 0, len(idx.authorsByID))
	for id := range idx.authorsByID {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var cands []*junkAuthorCandidate
	weakInPlan := map[int]bool{}
	for _, id := range ids {
		if len(scope) > 0 && !scope[id] {
			continue
		}
		v, ok := idx.strongJunk[id]
		if !ok {
			if v, ok = idx.weakJunk[id]; !ok || !includeWeak {
				continue
			}
			weakInPlan[id] = true
		}
		cands = append(cands, &junkAuthorCandidate{author: idx.authorsByID[id], verdict: v})
	}

	// Credits of every candidate row: junction and legacy AuthorID, trash
	// included (the trash is filtered below).
	var done atomic.Int64
	if err := registry.RunItems(ctx, rep, cands, func(_ context.Context, c *junkAuthorCandidate) error {
		defer done.Add(1)
		books, err := store.GetBooksByAuthorIDForRelinkCore(c.author.ID)
		if err != nil {
			c.readErr = err
			return nil
		}
		c.books = books
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(), ErrMode: registry.ErrModeCollect,
		Label: func(_, total int) string { return fmt.Sprintf("Reading credits %d/%d", done.Load(), total) },
	}); err != nil {
		return nil, fmt.Errorf("%s: read credits: %w", junkAuthorFixerID, err)
	}

	type pair struct {
		c   *junkAuthorCandidate
		b   database.BookCore
		out junkAuthorBookDecision
	}
	var pairs []*pair
	for _, c := range cands {
		for _, b := range c.books {
			if !b.IsSoftDeleted() {
				pairs = append(pairs, &pair{c: c, b: b})
			}
		}
	}
	// decideAll decides every pair (bounded worker pool). Each worker writes
	// only its own pair: no shared mutable state.
	decideAll := func(label string, list []*pair, isJunk func(int) bool) error {
		done.Store(0)
		return registry.RunItems(ctx, rep, list, func(_ context.Context, pr *pair) error {
			defer done.Add(1)
			pr.out = decideBook(store, idx, pr.c.author, pr.c.verdict, pr.b, isJunk)
			return nil
		}, registry.RunItemsOptions{
			Concurrency: runtime.NumCPU(), ErrMode: registry.ErrModeCollect,
			Label: func(_, total int) string { return fmt.Sprintf("%s %d/%d", label, done.Load(), total) },
		})
	}

	// Pass 1 gathers the evidence that confirms weak rows. No answer may be a
	// strong row, or a weak row this plan does not weigh (out of scope, or
	// weak rows switched off): whether those are people is unknown here.
	weakUnweighed := func(id int) bool { _, weak := idx.weakJunk[id]; return weak && !weakInPlan[id] }
	pass1 := func(id int) bool { _, strong := idx.strongJunk[id]; return strong || weakUnweighed(id) }
	if err := decideAll("Reading book evidence", pairs, pass1); err != nil {
		return nil, fmt.Errorf("%s: read evidence: %w", junkAuthorFixerID, err)
	}

	// Weak confirmation, per author: no book names the row itself, at least
	// one names someone else, and every read succeeded (fail closed).
	selfNamed, otherNamed, readFailed := map[int]bool{}, map[int]bool{}, map[int]bool{}
	for _, pr := range pairs {
		id := pr.c.author.ID
		selfNamed[id] = selfNamed[id] || pr.out.SelfNamed
		otherNamed[id] = otherNamed[id] || pr.out.OtherNamed
		readFailed[id] = readFailed[id] || pr.out.ReadErr != nil
	}
	confirmed := func(c *junkAuthorCandidate) bool {
		if c.verdict.Strength == authorjunk.Strong {
			return true
		}
		id := c.author.ID
		return c.readErr == nil && !readFailed[id] && !selfNamed[id] && otherNamed[id]
	}
	confirmedWeak := map[int]bool{}
	for _, c := range cands {
		if c.verdict.Strength == authorjunk.Weak && confirmed(c) {
			confirmedWeak[c.author.ID] = true
		}
	}

	// Pass 2: a confirmed weak row is junk too, so it is not a relink target
	// ("Before They Are Hanged" credited on a folder sibling). Only needed
	// when pass 1 confirmed any.
	if len(confirmedWeak) > 0 {
		var again []*pair
		for _, pr := range pairs {
			if confirmed(pr.c) {
				again = append(again, pr)
			}
		}
		pass2 := func(id int) bool { return pass1(id) || confirmedWeak[id] }
		if err := decideAll("Deciding", again, pass2); err != nil {
			return nil, fmt.Errorf("%s: read evidence: %w", junkAuthorFixerID, err)
		}
	}

	var rows []repairs.Row
	for _, c := range cands {
		// A credit read failure is always shown, weak or not: it is also what
		// kept a weak row from being confirmed.
		if c.readErr != nil {
			rows = append(rows, repairs.Row{
				RowID: junkAuthorRowID(c.author.ID, "credits"), Title: c.author.Name, Author: c.author.Name,
				Reason: "credits could not be read", Risk: repairs.RiskReview,
				Skipped: repairs.SkipGuardUnreadable, SkipReason: c.readErr.Error(),
				Fingerprint: fingerprintStrings("err", strconv.Itoa(c.author.ID)),
			})
		}
	}
	for _, pr := range pairs {
		if !confirmed(pr.c) && pr.out.ReadErr == nil {
			continue
		}
		r := junkAuthorRow(&pr.out)
		r.Detail = nil
		rows = append(rows, r)
	}
	return rows, nil
}

// Replan re-reads one row's book and decides it again with the apply-time
// index. The author's class comes from the name again (strong) or from the
// stored row (weak: its library evidence was confirmed over every book of
// the row at plan time and is not re-derived per row).
func (f *junkAuthorFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	authorID, bookID, err := parseJunkAuthorRowID(planned.RowID)
	if err != nil {
		return repairs.Row{}, err
	}
	idx, err := f.cachedIndex(store)
	if err != nil {
		return repairs.Row{}, err
	}
	a, err := store.GetAuthorByID(authorID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read author %d: %w", authorID, err)
	}
	if a == nil || a.ID != authorID {
		// Gone (or tombstoned into another row): nothing of this row is left.
		return repairs.Row{RowID: planned.RowID, Fingerprint: fingerprintStrings("gone"), Skipped: junkAuthorSkipAmbiguous,
			SkipReason: "the author row is gone"}, nil
	}
	v := authorjunk.ClassifyName(a.Name)
	if !v.Junk() {
		v = authorjunk.Verdict{Class: authorjunk.Class(planned.Current["class"]), Strength: authorjunk.Weak, Rule: planned.Current["rule"]}
	}
	full, err := store.GetBookByID(bookID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", bookID, err)
	}
	if full == nil || full.IsSoftDeleted() {
		return repairs.Row{RowID: planned.RowID, Fingerprint: fingerprintStrings("gone-book"), Skipped: junkAuthorSkipAmbiguous,
			SkipReason: "the book is gone"}, nil
	}
	core := bookCoreOf(full)
	// Strong rows are never targets; weak rows are not either, except the
	// target the plan chose (it was weighed then: unconfirmed, so a person).
	// A weak row newly chosen here moves the fingerprint: changed_since_plan.
	plannedTarget := 0
	if v := planned.Proposed["author_id"]; v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil {
			return repairs.Row{}, fmt.Errorf("row %s: planned author_id %q: %w", planned.RowID, v, perr)
		}
		plannedTarget = n
	}
	isJunk := func(id int) bool {
		if _, ok := idx.strongJunk[id]; ok {
			return true
		}
		_, weak := idx.weakJunk[id]
		return weak && id != plannedTarget
	}
	d := decideBook(store, idx, *a, v, core, isJunk)
	if d.ReadErr != nil {
		return repairs.Row{}, d.ReadErr
	}
	r := junkAuthorRow(&d)
	r.RowID = planned.RowID
	return r, nil
}

// Apply writes one fresh row through w: an author row created first when the
// decision needs one (journaled), then the credit move (journaled inside the
// author lock), then the primary, then the series link (journaled).
func (f *junkAuthorFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*junkAuthorBookDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", junkAuthorFixerID, fresh.RowID)
	}
	if d.Skip != "" || d.ReadErr != nil {
		return fmt.Errorf("%s: row %s is not applicable (%s)", junkAuthorFixerID, fresh.RowID, d.Skip)
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	bookID, junkID := d.Book.ID, d.Author.ID

	// The user lock, read fresh and fail-closed, immediately before writing.
	locks, err := database.LoadFieldLocks(store, bookID)
	if err != nil {
		return fmt.Errorf("read field locks of %s: %w", bookID, err)
	}
	if locks.Locked(database.FieldKeyAuthorName) {
		return fmt.Errorf("%w: book %s author was locked by the user", repairs.ErrChangedSincePlan, bookID)
	}

	var target *database.Author
	switch d.Decision {
	case junkAuthorDecRelink:
		t := d.Target
		target = &t
	case junkAuthorDecCreate:
		created, err := f.createTarget(w, store, d)
		if err != nil {
			return err
		}
		target = created
	}
	// Never onto the junk row itself (the store's name lookup ignores case),
	// nor onto another name-alone junk row.
	if target != nil && (target.ID == junkID || authorjunk.ClassifyName(target.Name).Junk()) {
		return fmt.Errorf("%w: book %s: target %q (id %d) is the junk row or junk itself", repairs.ErrChangedSincePlan, bookID, target.Name, target.ID)
	}

	primaryChanged := false
	var primaryAfter *database.Author
	_, err = w.ModifyCredits(bookID, func(cur []database.BookAuthor) ([]database.BookAuthor, repairs.UndoEntry, error) {
		if !sameCreditList(cur, d.Credits) {
			return nil, repairs.UndoEntry{}, fmt.Errorf("%w: book %s credits changed", repairs.ErrChangedSincePlan, bookID)
		}
		book, gerr := store.GetBookByID(bookID)
		if gerr != nil {
			return nil, repairs.UndoEntry{}, fmt.Errorf("read book %s: %w", bookID, gerr)
		}
		if book == nil {
			return nil, repairs.UndoEntry{}, fmt.Errorf("%w: book %s vanished", repairs.ErrChangedSincePlan, bookID)
		}
		next := moveCredit(cur, bookID, junkID, target)
		primaryChanged = book.AuthorID != nil && *book.AuthorID == junkID
		var afterID *int
		primaryAfter = nil
		if primaryChanged {
			pa, perr := primaryAfterMove(next, target, store)
			if perr != nil {
				return nil, repairs.UndoEntry{}, fmt.Errorf("book %s: %w", bookID, perr)
			}
			primaryAfter = pa
			if primaryAfter != nil {
				id := primaryAfter.ID
				afterID = &id
			}
		}
		// The exact junction this write leaves: the revert restores only
		// while the book still has it. Never nil (an emptied junction is []).
		after := make([]database.BookAuthor, len(next))
		copy(after, next)
		snap, merr := json.Marshal(undo.TitleRelinkCreditsSnapshot{AuthorID: book.AuthorID, Credits: cur})
		if merr != nil {
			return nil, repairs.UndoEntry{}, fmt.Errorf("encode credits of %s: %w", bookID, merr)
		}
		into := 0
		if target != nil {
			into = target.ID
		}
		move, merr := json.Marshal(undo.JunkAuthorCreditsMove{FromAuthorID: junkID, IntoAuthorID: into,
			PrimaryAfter: afterID, PrimaryChanged: primaryChanged, CreditsAfter: &after})
		if merr != nil {
			return nil, repairs.UndoEntry{}, fmt.Errorf("encode credit move of %s: %w", bookID, merr)
		}
		return next, repairs.UndoEntry{ChangeType: undo.ChangeTypeJunkAuthorCredits, Field: "book_authors",
			Old: string(snap), New: string(move)}, nil
	})
	if err != nil {
		return err
	}
	if primaryChanged {
		if perr := w.SetPrimaryAuthor(bookID, junkID, primaryAfter); perr != nil {
			return fmt.Errorf("%w: book %s credits moved but the primary was not: %v", repairs.ErrPartiallyApplied, bookID, perr)
		}
	}
	if d.Series != nil {
		if serr := f.linkSeries(w, store, bookID, *d.Series); serr != nil {
			return fmt.Errorf("%w: book %s relinked but the series link failed: %v", repairs.ErrPartiallyApplied, bookID, serr)
		}
	}
	return nil
}

// createTarget resolves or creates a create decision's author. The index may
// be older than the store: a row of this name made since (the store's lookup
// ignores case) is used as is, and no create is journaled for it. A row whose
// folded key matches a live author the apply-time index knows is a possible
// duplicate: refused, so the re-plan shows it for review. A created row is
// journaled by id AFTER the create (the revert deletes exactly that id, and
// only while nothing credits it).
func (f *junkAuthorFixer) createTarget(w *repairs.Writer, store OpsStore, d *junkAuthorBookDecision) (*database.Author, error) {
	name := d.Target.Name
	existing, err := store.GetAuthorByName(name)
	if err != nil {
		return nil, fmt.Errorf("read author %q: %w", name, err)
	}
	if existing != nil {
		return existing, nil
	}
	idx, err := f.cachedIndex(store)
	if err != nil {
		return nil, err
	}
	for _, row := range idx.byName[authorjunk.FoldKey(name)] {
		if row.ID != d.Author.ID {
			return nil, fmt.Errorf("%w: book %s: %q is spelled like existing author %q (id %d)", repairs.ErrChangedSincePlan, d.Book.ID, name, row.Name, row.ID)
		}
	}
	created, minted, err := store.MintAuthor(name)
	if err != nil {
		return nil, fmt.Errorf("create author %q: %w", name, err)
	}
	if created == nil || created.ID <= 0 {
		return nil, fmt.Errorf("create author %q: no row", name)
	}
	if !minted {
		// Another writer made it since the lookup above: use it, journal no
		// create (the undo of one deletes the row), take nothing back.
		return created, nil
	}
	rec, err := json.Marshal(undo.JunkAuthorCreate{AuthorID: created.ID, Name: created.Name})
	if err != nil {
		return nil, fmt.Errorf("encode created author %d: %w", created.ID, err)
	}
	if err := w.RecordChange(d.Book.ID, repairs.UndoEntry{ChangeType: undo.ChangeTypeJunkAuthorCreate,
		Field: "author_name", New: string(rec)}); err != nil {
		// Take it back rather than leave an unjournaled author the revert
		// cannot see -- but only while nothing credits it: another worker's
		// MintAuthor resolves the same name to this row (created=false) and
		// may already have linked a book to it.
		credited, cerr := store.GetBooksByAuthorIDForRelinkCore(created.ID)
		switch {
		case cerr != nil:
			return nil, fmt.Errorf("journal created author %d: %w (kept: reading its credits failed: %v)", created.ID, err, cerr)
		case len(credited) > 0:
			return nil, fmt.Errorf("journal created author %d: %w (kept: %d book(s) already credit it)", created.ID, err, len(credited))
		}
		if derr := store.DeleteAuthor(created.ID); derr != nil {
			return nil, fmt.Errorf("journal created author %d: %w (and removing it failed: %v)", created.ID, err, derr)
		}
		return nil, fmt.Errorf("journal created author %d: %w", created.ID, err)
	}
	return created, nil
}

// linkSeries sets the book's series to s while it has none and its series is
// not locked, journaled as a metadata_update the op revert restores.
func (f *junkAuthorFixer) linkSeries(w *repairs.Writer, store OpsStore, bookID string, s database.Series) error {
	locks, err := database.LoadFieldLocks(store, bookID)
	if err != nil {
		return err
	}
	if locks.Locked(database.FieldKeySeriesName) {
		return errors.New("series locked by the user")
	}
	if err := w.RecordChange(bookID, repairs.UndoEntry{ChangeType: "metadata_update", Field: "series_id",
		Old: "", New: strconv.Itoa(s.ID)}); err != nil {
		return err
	}
	_, err = w.Modify(bookID, func(b *database.Book) error {
		if b.SeriesID != nil {
			return fmt.Errorf("%w: book %s has a series now", repairs.ErrChangedSincePlan, bookID)
		}
		id := s.ID
		b.SeriesID = &id
		return nil
	})
	return err
}

// moveCredit replaces the junk credit with target in place (same position; a
// narrator role becomes "author": the target is the writer the evidence
// names, not a reader), or drops it when target is nil or already credited.
func moveCredit(cur []database.BookAuthor, bookID string, junkID int, target *database.Author) []database.BookAuthor {
	has := false
	if target != nil {
		for _, ba := range cur {
			has = has || ba.AuthorID == target.ID
		}
	}
	out := make([]database.BookAuthor, 0, len(cur))
	for _, ba := range cur {
		if ba.AuthorID != junkID {
			out = append(out, ba)
			continue
		}
		if target != nil && !has {
			ba.AuthorID = target.ID
			ba.BookID = bookID
			if isNarratorRole(ba.Role) {
				ba.Role = "author"
			}
			out = append(out, ba)
			has = true
		}
	}
	for i := range out {
		out[i].Position = i
	}
	return out
}

// primaryAfterMove is the primary a book gets when its primary was the junk
// row: the target, else the first remaining author or co-author credit, else
// none. A narrator (or any other non-author role) is never promoted.
func primaryAfterMove(next []database.BookAuthor, target *database.Author, store OpsStore) (*database.Author, error) {
	if target != nil {
		t := *target
		return &t, nil
	}
	for _, ba := range next {
		if !isPrimaryAuthorRole(ba.Role) {
			continue
		}
		a, err := store.GetAuthorByID(ba.AuthorID)
		if err != nil {
			return nil, fmt.Errorf("read author %d: %w", ba.AuthorID, err)
		}
		if a != nil {
			return a, nil
		}
	}
	return nil, nil
}

func sameCreditList(a, b []database.BookAuthor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].AuthorID != b[i].AuthorID || a[i].Role != b[i].Role || a[i].Position != b[i].Position {
			return false
		}
	}
	return true
}

func bookCoreOf(b *database.Book) database.BookCore {
	return database.BookCore{ID: b.ID, Title: b.Title, AuthorID: b.AuthorID, SeriesID: b.SeriesID,
		SeriesSequence: b.SeriesSequence, FilePath: b.FilePath, Narrator: b.Narrator,
		VersionGroupID: b.VersionGroupID, SeriesPositionRaw: b.SeriesPositionRaw,
		MarkedForDeletion: b.MarkedForDeletion}
}

// ---- rows ----

func junkAuthorRowID(authorID int, bookID string) string {
	return fmt.Sprintf("a%09d:b%s", authorID, bookID)
}

func parseJunkAuthorRowID(id string) (int, string, error) {
	a, b, ok := strings.Cut(id, ":b")
	if !ok || !strings.HasPrefix(a, "a") || b == "" {
		return 0, "", fmt.Errorf("%s: not a junk-author row id: %q", junkAuthorFixerID, id)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(a, "a"))
	if err != nil || n <= 0 {
		return 0, "", fmt.Errorf("%s: not a junk-author row id: %q", junkAuthorFixerID, id)
	}
	return n, b, nil
}

func strengthName(s authorjunk.Strength) string {
	if s == authorjunk.Weak {
		return "weak"
	}
	return "strong"
}

// junkAuthorRow turns a decision into a Repairs row.
func junkAuthorRow(d *junkAuthorBookDecision) repairs.Row {
	r := repairs.Row{
		RowID:   junkAuthorRowID(d.Author.ID, d.Book.ID),
		BookIDs: []string{d.Book.ID},
		Title:   d.Book.Title,
		Author:  d.Author.Name,
		Detail:  d,
		Risk:    repairs.RiskLow,
	}
	var credits []string
	for _, ba := range d.Credits {
		credits = append(credits, fmt.Sprintf("%d:%s", ba.AuthorID, ba.Role))
	}
	r.Current = map[string]string{
		"author":    d.Author.Name,
		"author_id": strconv.Itoa(d.Author.ID),
		"class":     string(d.Verdict.Class),
		"rule":      d.Verdict.Rule,
		"strength":  strengthName(d.Verdict.Strength),
	}
	what := fmt.Sprintf("%q is not a person: %s (%s, %s)", d.Author.Name, strings.ReplaceAll(string(d.Verdict.Class), "_", " "),
		d.Verdict.Rule, strengthName(d.Verdict.Strength))
	switch {
	case d.Skip != "":
		r.Skipped, r.SkipReason = d.Skip, d.SkipReason
		r.Reason = what
		r.Risk = repairs.RiskReview
	case d.Decision == junkAuthorDecUnlink:
		r.Proposed = map[string]string{"author": "(none: needs an author by hand)", "decision": junkAuthorDecUnlink}
		r.Reason = what + "; no evidence of the real author, so the junk credit is removed"
		r.Risk = repairs.RiskReview
	default:
		r.Proposed = map[string]string{"author": d.Target.Name, "decision": d.Decision, "source": d.Source}
		if d.Target.ID > 0 {
			r.Proposed["author_id"] = strconv.Itoa(d.Target.ID)
		}
		r.Reason = fmt.Sprintf("%s; real author %q from %s", what, d.Target.Name, strings.ReplaceAll(d.Source, "_", " "))
		if len(d.Others) > 0 {
			r.Proposed["other_evidence"] = strings.Join(d.Others, "; ")
			r.Risk = repairs.RiskReview
		}
		if d.Decision == junkAuthorDecCreate || d.Verdict.Strength == authorjunk.Weak ||
			d.Source == junkAuthorSrcSeries || d.Source == junkAuthorSrcPath || d.Source == junkAuthorSrcFolder {
			r.Risk = repairs.RiskReview
		}
		// An artist tag very often names the reader; the cleaned name is the
		// junk text itself. Either alone is review unless the provider or the
		// path names the same author.
		if (d.Source == junkAuthorSrcTags || d.Source == junkAuthorSrcCleaned) && !d.Corroborated {
			r.Risk = repairs.RiskReview
		}
		if d.NarratorAnswer {
			r.Proposed["narrator_too"] = "true"
			r.Reason += fmt.Sprintf("; %q is also the book's narrator", d.Target.Name)
			r.Risk = repairs.RiskReview
		}
		if d.Variant != "" {
			r.Proposed["evidence_spelling"] = d.Variant
			r.Reason += fmt.Sprintf("; spelled %q in the evidence: possible duplicate of the existing row", d.Variant)
			r.Risk = repairs.RiskReview
		}
	}
	if d.Series != nil && r.Proposed != nil {
		r.Proposed["series"] = d.Series.Name
		r.Proposed["series_id"] = strconv.Itoa(d.Series.ID)
	}
	parts := []string{
		"a", strconv.Itoa(d.Author.ID), d.Author.Name, string(d.Verdict.Class), strengthName(d.Verdict.Strength),
		"c", strings.Join(credits, ","), "p", intPtrString(d.Primary), "s", intPtrString(d.Book.SeriesID),
		"l", strconv.FormatBool(d.AuthorLocked), strconv.FormatBool(d.SeriesLocked),
		"d", d.Decision, d.Source, d.Target.Name, strconv.Itoa(d.Target.ID), d.Skip,
		"e", strconv.FormatBool(d.Corroborated), d.Variant, strconv.FormatBool(d.NarratorAnswer), d.NarratorDropped,
	}
	if d.Series != nil {
		parts = append(parts, "ser", strconv.Itoa(d.Series.ID))
	}
	r.Fingerprint = fingerprintStrings(parts...)
	return r
}

func intPtrString(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func fingerprintStrings(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])[:32]
}
