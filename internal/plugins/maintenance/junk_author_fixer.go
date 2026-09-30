// file: internal/plugins/maintenance/junk_author_fixer.go
// version: 1.18.0
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
	"unicode"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
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
// an author row with books of its own or the provider names it, and then at
// review risk (authors read their own books); a file tag (artist or
// album_artist) naming only the reader holds the row, never falling to a
// sibling, a path or an unlink. Names are matched on one folded key (case, punctuation,
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
// TARGETS THAT ARE NOT AUTHORS (trial 2026-09-29, plan op
// 01M3QT6HZ6843PSZCZ4JNSBYPC: ~290 applicable rows named a non-author). An
// answer is refused, and recorded on the row, when it is:
//
//   - the book's narrator, from a sibling or a path (version group, folder,
//     series, folder parser): no existing-author exception there, since a
//     reader-organized folder or a mis-credited sibling says nothing about
//     who wrote this book. The book's narrators include its files' NARRATOR /
//     PERFORMER / READER tags, and a name the library credits as narrator on
//     more live books than it has as author is refused from those sources too;
//   - a work: a book title or series name in the library ("Theft of
//     Swords", "Vampire Hunter D"), or the book's own title / series or a
//     whole-word part of it ("Stormcaller" of "Successor of Kukulkan_
//     Stormcaller"), except a pen name after "by" in the title. Judged by
//     evidence, not shape ("Vampire Hunter D", "Red Rising" are person-
//     shaped): a mint is always refused; an existing author is exempt only
//     when person-shaped AND credited with a live book whose title and series
//     are not the name itself ("Brent Weeks", who also names a junk series
//     row). workTarget; Apply's backstop asks the same;
//   - not person-shaped, a proper part of the junk name ("Simon" of "Simon &
//     Schuster"), and named by a sibling or a path;
//   - a folder above a deeper path segment that is the junk name's own
//     author ("Pyper Down/Jennsen/Jennsen, GS_ 08 Rubicon/..."): the layout is
//     Narrator/Author/Book, so the outer folder is the reader.
//
// Evidence naming a "Person (Reader)" credit ("Kevin Hearne (Luke Daniels)")
// counts as the person, and evidence naming a junk row that CleanedName
// reduces to a person ("zzJim Butcher") counts as that person; both only
// resolve to an existing author with books, never mint one. A row whose every
// answer was refused is held (skipped_ambiguous) with the refusals as its
// reason, never unlinked.
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

// junkAuthorEvidenceKind groups the sources by what they read, so agreement
// is counted between INDEPENDENT evidence. The version group, the folder and
// the series all read sibling books, and one sibling can be in all three:
// they are one kind, "siblings", however many sibling books name the author
// (neighbours by the same author are one inference, not several). The folder
// parser reads where the file sits: "location".
func junkAuthorEvidenceKind(src string) string {
	switch src {
	case junkAuthorSrcTags:
		return "file"
	case junkAuthorSrcProvider:
		return "provider"
	case junkAuthorSrcCleaned:
		return "name"
	case junkAuthorSrcPath:
		return "location"
	default: // version_group, same_folder, same_series
		return "siblings"
	}
}

// junkAuthorEvidenceKinds counts the distinct evidence kinds whose answers
// include key.
func junkAuthorEvidenceKinds[A any](bySource map[string]map[string]A, key string) int {
	if key == "" {
		return 0
	}
	kinds := map[string]bool{}
	for src, ans := range bySource {
		if _, ok := ans[key]; ok {
			kinds[junkAuthorEvidenceKind(src)] = true
		}
	}
	return len(kinds)
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
	// workNames / seriesWork: workKey of every live book's title (credited or
	// not) and of every series name; the works idx.workTarget refuses.
	workNames  map[string]bool
	seriesWork map[string]bool
	// titleWorkCredits / seriesWorkCredits: workKey of a live book's title /
	// series name -> primary author id -> distinct works (title workKeys)
	// credited: a work split into forty chapter files is one work.
	// idx.workCreditedElsewhere reads them.
	titleWorkCredits  map[string]map[int]int
	seriesWorkCredits map[string]map[int]int
	// authorWorkKeys: author id -> workKey of every title and series name the
	// library credits to it -> whether it is a series name (idx.independentWorks).
	authorWorkKeys map[int]map[string]bool
	// readsForOthers / readsOwn: authorjunk.FoldKey of a name -> books whose
	// Narrator names it and whose primary author is someone else / is that
	// same name (an author reading their own book). idx.isReader reads them.
	readsForOthers map[string]int
	readsOwn       map[string]int
	// narratedBooks: authorjunk.FoldKey of a narrator name -> books that
	// credit it as narrator: the larger of the live books whose Narrator
	// string names it and the books whose book_narrators rows link it.
	narratedBooks map[string]int
	// strongJunk: rows the name alone flags. Never a relink target.
	strongJunk map[int]authorjunk.Verdict
	// weakJunk: rows junk only by library evidence (not yet confirmed by
	// their books). Plan excludes the confirmed ones as targets; Replan
	// excludes all of them except the row's planned target.
	weakJunk map[int]authorjunk.Verdict
	// seriesCorroborated: author id -> true when the row holds books in a
	// series named for it (workKey equal) AND one of those books carries
	// direct evidence naming the row as author (a file tag or a stored
	// provider match). Set once by buildIndex (corroborateNamedSeries);
	// nil (nothing corroborated) for an index built by indexFrom alone.
	seriesCorroborated map[int]bool
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
	// book_narrators credits, read in one pass over the junction. A store
	// that cannot answer (a test double) leaves the Narrator-text count; a
	// read error fails the index (and so the plan) closed.
	var narratorRefs map[int]int
	if rs := database.AsNarratorRefStore(store); rs == nil {
		logger.New("maintenance").Debug("repair-junk-authors: store %T cannot count book_narrators credits; narrator counts use the Narrator field only", store)
	} else {
		refs, err := rs.GetAllNarratorRefs()
		if err != nil {
			return nil, fmt.Errorf("count narrator credits: %w", err)
		}
		narratorRefs = refs.ByID
	}
	idx := indexFrom(authors, books, series, narrators, narratorRefs)
	if err := idx.corroborateNamedSeries(store); err != nil {
		return nil, err
	}
	return idx, nil
}

// junkAuthorCorroborationSample: books read per candidate row by
// corroborateNamedSeries.
const junkAuthorCorroborationSample = 5

// junkAuthorCorroborationReader is what corroborateNamedSeries reads.
type junkAuthorCorroborationReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
}

// corroborateNamedSeries fills idx.seriesCorroborated. A row whose books sit
// in a series named for it is either an author whose series was named after
// them (Brent Weeks in series "Brent Weeks") or a work row filed in its own
// series ("Wraith Knight" holding "Wraith Lord", "Wraith Queen" in series
// "Wraith Knight"). The series structure cannot tell them apart; direct
// evidence on the books can: the Brent Weeks files are tagged "Brent Weeks",
// the Wraith Knight files name C. T. Phipps, and an anthology's name its
// editors or story authors.
//
// Candidates are only the rows holding at least one book in a series whose
// workKey is the row's own; for each, up to junkAuthorCorroborationSample of
// those books (lowest book IDs first, so the verdict is stable) are read: two
// reads per book (files, field states), so at most candidates x 5 x 2 reads,
// on 8 workers (DB reads, not CPU). Any read error fails the index, and so
// the plan, closed. The map is written once, after every worker is done.
func (idx *junkAuthorIndex) corroborateNamedSeries(r junkAuthorCorroborationReader) error {
	type candidate struct {
		id    int
		name  string
		books []database.BookCore
	}
	var cands []candidate
	for id, own := range idx.ownBooks {
		a, ok := idx.authorsByID[id]
		if !ok {
			continue
		}
		k := workKey(a.Name)
		if k == "" {
			continue
		}
		var in []database.BookCore
		for _, b := range own {
			if b.SeriesID != nil && workKey(idx.seriesByID[*b.SeriesID].Name) == k {
				in = append(in, b)
			}
		}
		if len(in) == 0 {
			continue
		}
		sort.Slice(in, func(i, j int) bool { return in[i].ID < in[j].ID })
		if len(in) > junkAuthorCorroborationSample {
			in = in[:junkAuthorCorroborationSample]
		}
		cands = append(cands, candidate{id: id, name: a.Name, books: in})
	}
	found := make([]bool, len(cands))
	var g errgroup.Group
	g.SetLimit(8)
	for i := range cands {
		g.Go(func() error {
			ok, err := namesAuthorDirectly(r, cands[i].name, cands[i].books)
			if err != nil {
				return fmt.Errorf("corroborate named series of %q: %w", cands[i].name, err)
			}
			found[i] = ok
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	idx.seriesCorroborated = make(map[int]bool, len(cands))
	for i, c := range cands {
		if found[i] {
			idx.seriesCorroborated[c.id] = true
		}
	}
	return nil
}

// namesAuthorDirectly reports whether any of books carries direct evidence
// naming name as author: an author file tag (titleRelinkFileTagKeys, the
// keys decideBook reads as its file_tags source) or a stored provider author
// whose fetched title agrees with the book's, either folding
// (authorjunk.FoldKey) to name whole or as one of its credited names.
func namesAuthorDirectly(r junkAuthorCorroborationReader, name string, books []database.BookCore) (bool, error) {
	self := authorjunk.FoldKey(name)
	if self == "" {
		return false, nil
	}
	names := func(v string) bool {
		if authorjunk.FoldKey(v) == self {
			return true
		}
		for _, part := range narratorSplitRe.Split(v, -1) {
			if authorjunk.FoldKey(part) == self {
				return true
			}
		}
		return false
	}
	for _, b := range books {
		files, err := r.GetBookFiles(b.ID)
		if err != nil {
			return false, fmt.Errorf("read files of %s: %w", b.ID, err)
		}
		for i := range files {
			for k, v := range files[i].RawTags {
				if titleRelinkFileTagKeys[strings.ToLower(k)] && names(v) {
					return true, nil
				}
			}
		}
		states, err := r.GetMetadataFieldStates(b.ID)
		if err != nil {
			return false, fmt.Errorf("read field states of %s: %w", b.ID, err)
		}
		var fetchedTitle, fetchedAuthor string
		for i := range states {
			switch states[i].Field {
			case "title":
				if v, ok := metastate.Decode(states[i].FetchedValue).(string); ok {
					fetchedTitle = v
				}
			case "author_name":
				if v, ok := metastate.Decode(states[i].FetchedValue).(string); ok {
					fetchedAuthor = v
				}
			}
		}
		if fetchedAuthor != "" && providerTitleAgrees(fetchedTitle, b.Title) && names(fetchedAuthor) {
			return true, nil
		}
	}
	return false, nil
}

// indexFrom builds the index from the library as read (buildIndex's reads,
// or a test's fixture). narratorRefs: narrator id -> books whose
// book_narrators rows link it (nil: none known). The index is read-only
// once built: RunItems workers read it concurrently.
func indexFrom(authors []database.Author, books []database.BookCore, series []database.Series, narrators []database.Narrator, narratorRefs map[int]int) *junkAuthorIndex {
	idx := &junkAuthorIndex{
		narratorsByID: make(map[int]string, len(narrators)),
		authorsByID:   make(map[int]database.Author, len(authors)), byName: map[string][]database.Author{},
		seriesByID: make(map[int]database.Series, len(series)), seriesByNorm: map[string][]database.Series{},
		byVG: map[string][]database.BookCore{}, bySeries: map[int][]database.BookCore{}, byDir: map[string][]database.BookCore{},
		titleCredits: map[string]map[int]int{}, seriesCredits: map[string]map[int]int{},
		ownBooks: map[int][]database.BookCore{}, strongJunk: map[int]authorjunk.Verdict{},
		workNames: map[string]bool{}, seriesWork: map[string]bool{}, narratedBooks: map[string]int{},
		weakJunk:         map[int]authorjunk.Verdict{},
		titleWorkCredits: map[string]map[int]int{}, seriesWorkCredits: map[string]map[int]int{},
		authorWorkKeys: map[int]map[string]bool{}, readsForOthers: map[string]int{}, readsOwn: map[string]int{},
	}
	for _, n := range narrators {
		idx.narratorsByID[n.ID] = n.Name
	}
	// Series first: the name verdict reads the series table
	// (idx.classifyName).
	for _, s := range series {
		idx.seriesByID[s.ID] = s
		k := authorjunk.Normalize(s.Name)
		idx.seriesByNorm[k] = append(idx.seriesByNorm[k], s)
		if wk := workKey(s.Name); wk != "" {
			idx.seriesWork[wk] = true
		}
	}
	for _, a := range authors {
		idx.authorsByID[a.ID] = a
		if k := authorjunk.FoldKey(a.Name); k != "" {
			idx.byName[k] = append(idx.byName[k], a)
		}
		if v := idx.classifyName(a.Name); v.Junk() {
			idx.strongJunk[a.ID] = v
		}
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
	type workCredit struct {
		key   string
		id    int
		title string
	}
	// One seen set per map: a book titled after its own series ("Cthulhu
	// Armageddon" in series "Cthulhu Armageddon") is one title work AND one
	// series work, and a shared set dropped the second.
	seenTitleWork, seenSeriesWork := map[workCredit]bool{}, map[workCredit]bool{}
	bumpWork := func(m map[string]map[int]int, seen map[workCredit]bool, key string, id int, title string) {
		if key == "" || seen[workCredit{key, id, title}] {
			return
		}
		seen[workCredit{key, id, title}] = true
		bump(m, key, id)
	}
	for i := range books {
		b := books[i]
		if b.IsSoftDeleted() {
			continue
		}
		if g := b.VersionGroupID; g != nil && *g != "" {
			idx.byVG[*g] = append(idx.byVG[*g], b)
		}
		if k := workKey(b.Title); k != "" {
			idx.workNames[k] = true
		}
		if b.Narrator != nil {
			seen := map[string]bool{}
			for _, part := range narratorSplitRe.Split(*b.Narrator, -1) {
				if k := authorjunk.FoldKey(part); k != "" && !seen[k] {
					seen[k] = true
					idx.narratedBooks[k]++
				}
			}
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
			aid := *b.AuthorID
			tk := workKey(b.Title)
			keys := idx.authorWorkKeys[aid]
			if keys == nil {
				keys = map[string]bool{}
				idx.authorWorkKeys[aid] = keys
			}
			if _, have := keys[tk]; tk != "" && !have {
				keys[tk] = false
			}
			bumpWork(idx.titleWorkCredits, seenTitleWork, tk, aid, tk)
			if b.SeriesID != nil {
				sk := workKey(idx.seriesByID[*b.SeriesID].Name)
				if sk != "" {
					keys[sk] = true
				}
				bumpWork(idx.seriesWorkCredits, seenSeriesWork, sk, aid, tk)
			}
		}
		if b.Narrator != nil {
			own := ""
			if b.AuthorID != nil {
				own = authorjunk.FoldKey(idx.authorsByID[*b.AuthorID].Name)
			}
			seen := map[string]bool{}
			for _, part := range narratorSplitRe.Split(*b.Narrator, -1) {
				k := authorjunk.FoldKey(part)
				if k == "" || seen[k] {
					continue
				}
				seen[k] = true
				if k == own {
					idx.readsOwn[k]++
				} else {
					idx.readsForOthers[k]++
				}
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
	for id, n := range narratorRefs {
		if k := authorjunk.FoldKey(idx.narratorsByID[id]); k != "" && n > idx.narratedBooks[k] {
			idx.narratedBooks[k] = n
		}
		// book_narrators counts carry no author: the books the Narrator text
		// shows the name reading for itself are taken off.
		if k := authorjunk.FoldKey(idx.narratorsByID[id]); k != "" && n-idx.readsOwn[k] > idx.readsForOthers[k] {
			idx.readsForOthers[k] = n - idx.readsOwn[k]
		}
	}
	return idx
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

// classifyName is the fixer's one strong (name-decided) verdict:
// authorjunk.ClassifyName plus the series table, which tells "Dante King
// (Dragon Born)" (a series in this library) from "Kevin Hearne (Luke
// Daniels)" (a narrator). Every place that asks "is this name junk?" -- the
// index, each answer, Replan, the pre-create check and Apply's backstop --
// asks this, so a row's verdict does not change between plan and apply.
func (idx *junkAuthorIndex) classifyName(name string) authorjunk.Verdict {
	return authorjunk.ClassifyNameInLibrary(name, func(n string) bool { return len(idx.seriesByNorm[n]) > 0 })
}

// mintable reports whether name may become a NEW author row: person-shaped,
// not junk, and not a work-title shape. personname.LooksLikePersonName is a
// shape test that passes "The Thirteenth Doctor Adventures" (four
// capitalized words); LooksLikeWorkTitle refuses any article-led name, so a
// held row, not a minted author, is the cost of a real "A Lee Martinez".
//
// Nor a name no person carries: square or curly brackets ("Raymond L.
// Weil-Star Cross[1-5]", a folder's volume range) or an author and a title
// hyphen-joined mid-name (authorTitleHyphenJoin).
func (idx *junkAuthorIndex) mintable(name string) bool {
	return personname.LooksLikePersonName(name) && !personname.LooksLikeWorkTitle(name) && !idx.classifyName(name).Junk() &&
		!strings.ContainsAny(name, "[]{}") && !authorTitleHyphenJoin(name)
}

// authorTitleHyphenJoin reports a hyphen joining two words with no space,
// in a word that is neither the name's first nor its last: "Raymond L.
// Weil-Star Cross" is "Raymond L. Weil" + "Star Cross" run together by a
// folder parser. A hyphenated given name ("Jean-Paul Sartre") is the first
// word and a double-barrelled surname ("Hannah Bonam-Young") the last.
func authorTitleHyphenJoin(name string) bool {
	f := strings.Fields(name)
	for i := 1; i < len(f)-1; i++ {
		if strings.Contains(strings.Trim(f[i], "-"), "-") {
			return true
		}
	}
	return false
}

// outerParenthetical splits "Cathfach (A (Not So) Simple Fetch Quest)" into
// its head and the outermost trailing parenthetical (nesting balanced).
func outerParenthetical(name string) (head, inner string, ok bool) {
	s := strings.TrimSpace(name)
	if !strings.HasSuffix(s, ")") {
		return "", "", false
	}
	depth := 0
	for i := len(s) - 1; i >= 0; i-- {
		switch s[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				head, inner = strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:len(s)-1])
				return head, inner, head != "" && inner != ""
			}
		}
	}
	return "", "", false
}

// parenWork reports whether name is "Person (Work)": a head and a trailing
// parenthetical that is a book title or series name in the library, or the
// book's own title or series. The composite is not a person; its head may be
// (decideBook resolves the head, resolve-only; workTarget refuses the whole).
func (idx *junkAuthorIndex) parenWork(name, bookTitle string, bookSeries *int) (string, bool) {
	head, inner, ok := outerParenthetical(name)
	if !ok {
		return "", false
	}
	ik := workKey(inner)
	if ik == "" {
		return "", false
	}
	own := ik == workKey(bookTitle)
	if bookSeries != nil && ik == workKey(idx.seriesByID[*bookSeries].Name) {
		own = true
	}
	if idx.workNames[ik] || idx.seriesWork[ik] || own {
		return head, true
	}
	return "", false
}

// workKey is the key works are compared on: authorjunk.Normalize with a
// leading "the" dropped ("The Dresden Files" and "Dresden Files" are one).
func workKey(s string) string {
	return strings.TrimPrefix(authorjunk.Normalize(s), "the ")
}

// Refusal codes: the fingerprint carries these, never the display text (which
// holds library-wide counts that move with every scan).
const (
	junkRefuseNarrator      = "narrator"
	junkRefuseNarratorCount = "narrator_count"
	junkRefuseWorkTitle     = "work_title"
	junkRefuseWorkCredited  = "work_credited"
	junkRefuseComposite     = "person_work_composite"
	junkRefuseOwnTitle      = "own_title"
	junkRefuseJunkFragment  = "junk_fragment"
	junkRefusePathOuter     = "path_outer"
)

// hasOtherWork reports whether author id is credited with a live book whose
// title (whole, or a whole-word part of it) and series are not name itself:
// evidence that the name is a writer and not the work it names.
func (idx *junkAuthorIndex) hasOtherWork(id int, name string) bool {
	k := workKey(name)
	for _, b := range idx.ownBooks[id] {
		tk := workKey(b.Title)
		if (tk == k || properWordPart(k, tk)) && !dashCredited(name, b.Title) {
			continue
		}
		if b.SeriesID != nil && workKey(idx.seriesByID[*b.SeriesID].Name) == k {
			continue
		}
		return true
	}
	return false
}

// workTarget reports whether name, as the target of a book titled bookTitle
// in series bookSeries, is a work and not a person: a book title or series
// name in the library, or the book's own title / series or a whole-word part
// of it (not a pen name after "by"). id is the existing author row it
// resolves to (<= 0: a mint or no single row). Judged by evidence, not
// shape: "Vampire Hunter D", "Solo Leveling", "Red Rising" are person-shaped.
// A mint is always refused; an existing row only unless it is person-shaped
// AND hasOtherWork ("Brent Weeks", whose name is also a junk series row).
// Plan (decideBook) and Apply's backstop both ask this.
func (idx *junkAuthorIndex) workTarget(name string, id int, bookTitle string, bookSeries *int) (code, why string) {
	k := workKey(name)
	if k == "" {
		return "", ""
	}
	if head, ok := idx.parenWork(name, bookTitle, bookSeries); ok {
		return junkRefuseComposite, fmt.Sprintf("%q with a work title in parentheses, not a person", head)
	}
	library := idx.workNames[k] || idx.seriesWork[k]
	tk := workKey(bookTitle)
	own := (tk == k || properWordPart(k, tk)) && !byNamed(name, bookTitle) && !dashCredited(name, bookTitle)
	if bookSeries != nil {
		if sk := workKey(idx.seriesByID[*bookSeries].Name); sk != "" && (sk == k || properWordPart(k, sk)) {
			own = true
		}
	}
	if !library && !own {
		return "", ""
	}
	// Who the library credits the work to decides, not whether the row has
	// books of its own (a junk row does): series "Cthulhu Armageddon" is C. T.
	// Phipps's, so an author row of that name is the work. A series named
	// after its own author ("Brent Weeks" -> Brent Weeks) names no one else
	// and falls through to the test below.
	if other := idx.workCreditedElsewhere(name, id); other != "" {
		return junkRefuseWorkCredited, fmt.Sprintf("a book or series title in the library credited to %q, not a person", other)
	}
	if id > 0 && personname.LooksLikePersonName(name) && (idx.hasOtherWork(id, name) || idx.seriesCorroborated[id]) {
		return "", ""
	}
	if library {
		return junkRefuseWorkTitle, "a book or series title in the library, not a person"
	}
	return junkRefuseOwnTitle, "the book's own title or series (or a part of it), not a person"
}

// workCreditedElsewhere names an author the library credits with a live book
// or series whose workKey is name's, when that credit outweighs row id's own
// body of work (id <= 0: a mint, no body of work); "" when none. One stray
// credit never outweighs an author's work: a biography titled "Agatha
// Christie" does not make Agatha Christie a work, one C. T. Phipps book filed
// in a series "Glynn Stewart" does not make Glynn Stewart one.
//
// Counts are distinct works (title workKeys), not files. Not counted as
// someone else, since none of them is evidence of who wrote a work:
//   - row id itself, a row spelled the same (authorjunk.FoldKey), or a row
//     carrying the name whole ("Brandon Sanderson (GraphicAudio)");
//   - a strong junk row (a name-alone verdict: the rows this fixer repairs).
//     A weak one still counts: weakJunk holds real authors ("Brent Weeks",
//     "Terry Pratchett") whose names are also junk series rows;
//   - a reader filed as an author (idx.isReader);
//   - for a title (not a series) match, a row with no other work ("Joe
//     Abercrombie" credited to "Before They Are Ha").
//
// The other author must also have more works under the name than row id has
// independent works (idx.independentWorks: its works not named for the name,
// not the other author's own works, and not file labels -- "Disc 07", "01").
// The row's own works named for the name count on the other side: they are
// the work's, not the person's ("American Gods 10th Anniversary" under an
// "American Gods" row; Neil Gaiman has "American Gods").
// A swapped pair -- each credited under the other's name ("Mistborn" in a
// series "Brandon Sanderson", Brandon Sanderson in series "Mistborn") -- goes
// to row id only when it alone has work outside the pair; otherwise the
// credit stands and the row is held.
//
// The lowest qualifying id is named, so the refusal reads the same on every
// plan. Maps are read-only here (RunItems workers call this concurrently).
func (idx *junkAuthorIndex) workCreditedElsewhere(name string, id int) string {
	k := workKey(name)
	if k == "" {
		return ""
	}
	self := authorjunk.FoldKey(name)
	best := 0
	consider := func(oid int) {
		if oid == id || (best != 0 && oid >= best) {
			return
		}
		o, ok := idx.authorsByID[oid]
		if !ok || authorjunk.FoldKey(o.Name) == self || properWordPart(k, workKey(o.Name)) {
			return
		}
		if _, junk := idx.strongJunk[oid]; junk {
			return
		}
		if idx.isReader(oid) {
			return
		}
		inSeries := idx.seriesWorkCredits[k][oid]
		n := inSeries + idx.titleWorkCredits[k][oid]
		if inSeries == 0 && !idx.hasOtherWork(oid, name) {
			return
		}
		if id > 0 {
			ok := workKey(o.Name)
			if idx.seriesWorkCredits[ok][id]+idx.titleWorkCredits[ok][id] > 0 {
				if idx.independentWorks(id, oid, k, ok, 1, false) > 0 && idx.independentWorks(oid, id, ok, k, 1, false) == 0 {
					return // row id is the writer; the other row is its swapped work
				}
				best = oid
				return
			}
			// A series of two or more works, every one of them credited to the
			// other author, with none of row id's books in it: the series is
			// theirs and row id is a work named after it ("Cthulhu Armageddon"
			// holding three series-less titles; C. T. Phipps's series). Row id's
			// own books in the series (Brent Weeks in a series "Brent Weeks")
			// make it the swap artifact instead, and one misfiled book (C. T.
			// Phipps in a series "Glynn Stewart") never decides it.
			if inSeries >= 2 && idx.seriesOnlyBy(k, oid) && !idx.inSeriesNamed(id, k) {
				best = oid
				return
			}
			// The row's own works titled for the name with more words ("American
			// Gods 10th Anniversary" under an "American Gods" row) are the
			// work's too. Its books in a series named exactly k count as its
			// own only when their files or provider match name it
			// (idx.seriesCorroborated: Brent Weeks's tags say "Brent Weeks";
			// a "Wraith Knight" work row's say C. T. Phipps).
			if w := n + idx.namedWorks(id, k); idx.independentWorks(id, oid, k, "", w, idx.seriesCorroborated[id]) >= w {
				return
			}
		}
		best = oid
	}
	for oid := range idx.seriesWorkCredits[k] {
		consider(oid)
	}
	for oid := range idx.titleWorkCredits[k] {
		consider(oid)
	}
	if best == 0 {
		return ""
	}
	return idx.authorsByID[best].Name
}

// isReader reports a reader filed as an author: the library credits the name
// as narrator of other authors' books on at least half as many books as it
// credits it as author (David Tennant, filed as the author of a "Cressida
// Cowell" series he reads). An author reading their own books (Neil Gaiman)
// is not a reader: those books count on neither side.
func (idx *junkAuthorIndex) isReader(id int) bool {
	key := authorjunk.FoldKey(idx.authorsByID[id].Name)
	others := idx.readsForOthers[key]
	if others == 0 {
		return false
	}
	return 2*others >= len(idx.ownBooks[id])-idx.readsOwn[key]
}

// independentWorks counts row id's distinct works (title workKeys), up to
// limit, that are named for neither k1 nor k2 (a title or series equal to
// either, or holding either as whole words) and are not works the library
// credits to row other (a title or series that holds two or more words in a
// row of one of other's titles or series names, or a single word of five or
// more letters that is one of other's series names): the body of work that
// says row id is a writer and not other's folder dump ("Cthulhu Armageddon"'s
// "Wraith Knight Three Worlds, Book" is C. T. Phipps's "Wraith Knight"). One
// shared word with a title is no evidence: Laura Thompson's "Murder" does not
// take "Murder on the Orient Express" from Agatha Christie.
//
// ownSeries: a book in a series named exactly k1 counts as row id's work (its
// title is still checked). Callers pass idx.seriesCorroborated[id]: the
// series is then the author-named artifact, backed by direct evidence.
func (idx *junkAuthorIndex) independentWorks(id, other int, k1, k2 string, limit int, ownSeries bool) int {
	named := func(x string) bool {
		for _, k := range []string{k1, k2} {
			if k != "" && (x == k || properWordPart(k, x)) {
				return true
			}
		}
		return false
	}
	theirs := idx.authorWorkKeys[other]
	otherWork := func(x string) bool {
		if x == "" || len(theirs) == 0 {
			return false
		}
		words := strings.Fields(x)
		for i := range words {
			for j := i + 1; j <= len(words); j++ {
				g := strings.Join(words[i:j], " ")
				isSeries, have := theirs[g]
				if !have {
					continue
				}
				if j-i >= 2 || (isSeries && len(g) >= 5) {
					return true // one word only as a series name: "Mistborn", not "Murder" or "war"
				}
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, b := range idx.ownBooks[id] {
		tk := workKey(b.Title)
		if tk == "" || named(tk) || otherWork(tk) {
			continue
		}
		if b.SeriesID != nil {
			sk := workKey(idx.seriesByID[*b.SeriesID].Name)
			if sk != "" && !(ownSeries && sk == k1) && (named(sk) || otherWork(sk)) {
				continue
			}
		}
		work := workOfTitle(tk)
		if work == "" || seen[work] {
			continue
		}
		seen[work] = true
		if len(seen) >= limit {
			break
		}
	}
	return len(seen)
}

// namedWorks counts row id's distinct works (workOfTitle) whose title holds
// k with more words ("American Gods 10th Anniversary"). A title that is k
// exactly is left out: a book titled with a person's own name is a folder
// parsed the wrong way round ("Roald Dahl" filed as the title of Roald Dahl's
// books), not a work of that name. So is a book's series: a series named k
// holding row id's books is the author-named swap artifact (Brent Weeks's
// books in a series "Brent Weeks"), and counting it for the work would hand
// an author's whole shelf to whoever else has one book in that series.
func (idx *junkAuthorIndex) namedWorks(id int, k string) int {
	seen := map[string]bool{}
	for _, b := range idx.ownBooks[id] {
		tk := workKey(b.Title)
		if w := workOfTitle(tk); properWordPart(k, tk) && w != "" {
			seen[w] = true
		}
	}
	return len(seen)
}

// seriesOnlyBy reports whether every book in a series whose workKey is k is
// credited to row oid, leaving out strong junk rows (the rows this fixer
// repairs: their credits are not authorship).
func (idx *junkAuthorIndex) seriesOnlyBy(k string, oid int) bool {
	for aid := range idx.seriesWorkCredits[k] {
		if _, junk := idx.strongJunk[aid]; aid != oid && !junk {
			return false
		}
	}
	return true
}

// inSeriesNamed reports whether any of row id's books is in a series whose
// workKey is k.
func (idx *junkAuthorIndex) inSeriesNamed(id int, k string) bool {
	for _, b := range idx.ownBooks[id] {
		if b.SeriesID != nil && workKey(idx.seriesByID[*b.SeriesID].Name) == k {
			return true
		}
	}
	return false
}

// fileLabelWords: words that number or label a file rather than name a work.
var fileLabelWords = map[string]bool{
	"disc": true, "disk": true, "cd": true, "part": true, "pt": true, "track": true, "chapter": true, "chap": true,
	"ch": true, "book": true, "vol": true, "volume": true, "side": true, "tape": true, "episode": true, "ep": true,
	"file": true, "copy": true, "copy1": true, "unknown": true, "title": true, "read": true, "by": true, "narrator": true,
	"intro": true, "introduction": true, "prologue": true, "epilogue": true, "opening": true, "closing": true,
	"credits": true, "unabridged": true, "abridged": true, "audiobook": true, "of": true,
}

// workOfTitle reduces a title workKey to the work it names, without file
// labels and numbers ("skinwalker part 2" -> "skinwalker"); "" when nothing
// names a work: no all-letter word of three or more letters is left ("disc
// 07", "01", "0i06yh b 3").
func workOfTitle(tk string) string {
	var keep []string
	named := false
	for _, w := range strings.Fields(tk) {
		if fileLabelWords[w] || strings.IndexFunc(w, func(r rune) bool { return !unicode.IsDigit(r) }) < 0 {
			continue
		}
		keep = append(keep, w)
		if len([]rune(w)) >= 3 && strings.IndexFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }) < 0 {
			named = true
		}
	}
	if !named {
		return ""
	}
	return strings.Join(keep, " ")
}

// junkAuthorSourceRank is src's position in junkAuthorSources (lower
// decides first); len(junkAuthorSources) for an unknown source.
func junkAuthorSourceRank(src string) int {
	for i, s := range junkAuthorSources {
		if s == src {
			return i
		}
	}
	return len(junkAuthorSources)
}

// junkAuthorInferredSource: the sources that transfer another book's credit
// or read a path, and so say nothing direct about who wrote this book.
func junkAuthorInferredSource(src string) bool {
	switch src {
	case junkAuthorSrcVersion, junkAuthorSrcFolder, junkAuthorSrcSeries, junkAuthorSrcPath:
		return true
	}
	return false
}

// properWordPart reports whether part is a whole-word proper part of whole
// (both authorjunk.Normalize forms).
func properWordPart(part, whole string) bool {
	return part != "" && part != whole && strings.Contains(" "+whole+" ", " "+part+" ")
}

// byNamed reports whether title credits name after "by" ("The Last Legend
// Reborn, Book 2 by Borgy60"): the title then names the author, not a work.
func byNamed(name, title string) bool {
	return strings.Contains(" "+authorjunk.Normalize(title)+" ", " by "+authorjunk.Normalize(name)+" ")
}

// dashCredited reports whether title carries name as a whole " - "-delimited
// segment AFTER the first ("The Path of Two - DP Behling - read by X"): a
// credit in the title, like byNamed, not a work named after the person. The
// first segment is the work title ("Red Rising - Pierce Brown", "Solo
// Leveling - Vol 3") and is never read as a credit, so a leading "Author -
// Title" shape stays held -- the safe direction: a held row, never a work
// minted or relinked as an author.
func dashCredited(name, title string) bool {
	k := workKey(name)
	if k == "" || !strings.Contains(title, " - ") {
		return false
	}
	for _, seg := range strings.Split(title, " - ")[1:] {
		if workKey(seg) == k {
			return true
		}
	}
	return false
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
	// Refused lists the answers refused as non-authors ("name [source]:
	// why"): the book's narrator from a sibling or path, a title or series
	// name, a fragment of the junk name or the book's title. A row left with
	// no answer and some refusals is held, not unlinked.
	Refused []string
	// RefusedKeys is Refused as "name [source] code" (junkRefuse*): what the
	// fingerprint hashes.
	RefusedKeys []string

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
	narrators := junkAuthorNarratorKeys(full, credits, bookNarrators, files, idx)
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
	junkNorm := authorjunk.Normalize(a.Name)
	refuse := func(src, name, code, why string) {
		key := fmt.Sprintf("%q [%s] %s", name, src, code)
		for _, r := range d.RefusedKeys {
			if r == key {
				return
			}
		}
		d.RefusedKeys = append(d.RefusedKeys, key)
		d.Refused = append(d.Refused, fmt.Sprintf("%q [%s]: %s", name, src, why))
	}

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
	// creditedRefusedAt: the rank (junkAuthorSources) of the best source whose
	// answer was refused as a work credited to another author. A weighed
	// judgement, not a shape: a lower source may not overrule it.
	creditedRefusedAt, creditedRefusedSrc := len(junkAuthorSources), ""
	// addName weighs one answer. resolveOnly: the name was derived from the
	// evidence (a "Person (Reader)" head, a junk name cleaned), so it may only
	// resolve to an existing author with books, never mint one.
	var addName func(src, name string, resolveOnly bool)
	add := func(src, name string) { addName(src, name, false) }
	addName = func(src, name string, resolveOnly bool) {
		name = strings.TrimSpace(name)
		if src != junkAuthorSrcCleaned && !resolveOnly {
			// "Kevin Hearne (Luke Daniels)": the writer is the head.
			if head, ok := authorjunk.PersonParentheticalHead(name); ok {
				addName(src, head, true)
				return
			}
			// "Cathfach (A (Not So) Simple Fetch Quest)": the writer is the
			// head when the parenthetical is a work (idx.parenWork).
			if head, ok := idx.parenWork(name, d.Book.Title, d.Book.SeriesID); ok {
				addName(src, head, true)
				return
			}
		}
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
		if idx.classifyName(name).Junk() {
			// "zzJim Butcher", "Graphic Audio [Jon Scieszka": the person the
			// junk text carries, resolve-only.
			if src != junkAuthorSrcCleaned && !resolveOnly {
				if c, ok := authorjunk.CleanedName(name); ok {
					addName(src, c, true)
				}
			}
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
			// No row: only a person-shaped name that is not a title shape
			// may be minted (idx.mintable).
			if !idx.mintable(name) {
				return
			}
			ans = answer{row: database.Author{Name: name}, create: true}
		}
		// The cleaned name only RESOLVES, and only to an author who has
		// books: the junk text alone never mints a row.
		if (src == junkAuthorSrcCleaned || resolveOnly) && (ans.create || ans.row.ID <= 0 || len(idx.ownBooks[ans.row.ID]) == 0) {
			return
		}
		// A work, not a person (workTarget: by evidence, mints included).
		if code, why := idx.workTarget(name, ans.row.ID, d.Book.Title, d.Book.SeriesID); code != "" {
			refuse(src, name, code, why)
			if code == junkRefuseWorkCredited {
				if si := junkAuthorSourceRank(src); si < creditedRefusedAt {
					creditedRefusedAt, creditedRefusedSrc = si, src
				}
			}
			return
		}
		// "Simon" of "Simon & Schuster": a piece of the junk name that is not
		// person-shaped, named by a sibling or a path. A file tag or the
		// provider naming a pen name inside a junk credit still resolves
		// ("Shirtaloon" in a "Shirtaloon (He Who Fights With Monsters)" row);
		// a sibling credited to the fragment is the junk split in two.
		if junkAuthorInferredSource(src) && !personname.LooksLikePersonName(name) &&
			properWordPart(authorjunk.Normalize(name), junkNorm) {
			refuse(src, name, junkRefuseJunkFragment, fmt.Sprintf("a fragment of the junk name %q, not a person", a.Name))
			return
		}
		// A sibling or a path naming the book's reader is refused outright:
		// "[PZG]" rips filed by reader and siblings mis-credited to their
		// narrator put the reader there far more often than a self-narrated
		// author (trial: 146 such rows, none a writer). So is a name the
		// library credits as narrator on more books than it has as author
		// (a reader whose own "author" books are themselves mis-credits).
		// Deliberately, both hold a real author-narrator too (Travis Baldree
		// writes and reads): from a sibling or a path the two cannot be told
		// apart, and a held row costs a review, a wrong relink a bad credit.
		// File tags and the provider keep the self-narrated rule below.
		if junkAuthorInferredSource(src) {
			switch {
			case narrators[key]:
				refuse(src, name, junkRefuseNarrator, "the book's narrator")
				return
			case ans.row.ID > 0 && idx.narratedBooks[key] > len(idx.ownBooks[ans.row.ID]):
				refuse(src, name, junkRefuseNarratorCount, fmt.Sprintf("narrates %d books in the library and is credited as author of %d",
					idx.narratedBooks[key], len(idx.ownBooks[ans.row.ID])))
				return
			}
		}
		if narrators[key] {
			// The book's reader. Still the writer (authors read their own
			// books) only when the name is an author row of its own (not this
			// book's narrator credit) that has books of its own -- an empty
			// row is no evidence the reader writes, the same bar as the
			// cleaned source -- or the provider names them as the author. No
			// file tag suffices: rips put the reader in album_artist as often
			// as in artist, and taking it would create an author for them.
			existingAuthor := !ans.create && ans.row.ID > 0 && !narratorCreditIDs[ans.row.ID] && len(idx.ownBooks[ans.row.ID]) > 0
			if !existingAuthor && src != junkAuthorSrcProvider {
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
			add(junkAuthorSrcCleaned, cleaned)
		}
	}
	for i := range files {
		keys := make([]string, 0, len(files[i].RawTags))
		for k := range files[i].RawTags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if titleRelinkFileTagKeys[strings.ToLower(k)] {
				add(junkAuthorSrcTags, files[i].RawTags[k])
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
		add(junkAuthorSrcProvider, fetchedAuthor)
	}
	sibs := func(src string, list []database.BookCore) {
		for i := range list {
			sb := list[i]
			if sb.ID == book.ID || sb.AuthorID == nil || *sb.AuthorID == a.ID {
				continue
			}
			if junk(*sb.AuthorID) {
				// A junk sibling credit that carries a person ("zzJim
				// Butcher"): that person, resolve-only.
				if row, ok := idx.authorsByID[*sb.AuthorID]; ok {
					if c, ok := authorjunk.CleanedName(row.Name); ok {
						addName(src, c, true)
					}
				}
				continue
			}
			if row, ok := idx.authorsByID[*sb.AuthorID]; ok {
				add(src, row.Name)
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
	junkPathKeys := junkAuthorPathKeys(a.Name)
	for _, p := range paths {
		if p == "" {
			continue
		}
		segs := metadata.SplitPathSegments(p)
		for _, c := range authorPathLinkCandidates(p) {
			if below := junkAuthorSegmentBelow(segs, c.Name, junkPathKeys); below != "" {
				refuse(junkAuthorSrcPath, c.Name, junkRefusePathOuter, fmt.Sprintf(
					"a folder above %q, the junk name's own author (a Narrator/Author/Book layout)", below))
				continue
			}
			add(junkAuthorSrcPath, c.Name)
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

	// A surname-first clean ("Jennsen, GS_ 08 Rubicon" -> "G. S. Jennsen")
	// carries initials only: when the file tags or the provider answer and
	// none of them names it, the clean is dropped and the next source decides
	// (so the disagreeing tag / provider answer wins, at that source's risk).
	if cl := bySource[junkAuthorSrcCleaned]; len(cl) > 0 && authorjunk.IsSurnameFirstInitials(a.Name) {
		answered, agree := false, false
		for _, src := range []string{junkAuthorSrcTags, junkAuthorSrcProvider} {
			for k := range bySource[src] {
				answered = true
				if _, ok := cl[k]; ok {
					agree = true
				}
			}
		}
		if answered && !agree {
			delete(bySource, junkAuthorSrcCleaned)
		}
	}

	// chosenKey is the chosen answer's key, for the evidence-kind count.
	chosenKey := ""
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
		chosenKey = key
		d.Source, d.Target, d.Variant, d.NarratorAnswer = src, chosen.row, chosen.variant, chosen.narrator
		d.Decision = junkAuthorDecRelink
		if chosen.create {
			d.Decision = junkAuthorDecCreate
		}
		for _, later := range junkAuthorSources[si+1:] {
			for k, o := range bySource[later] {
				if k != key {
					d.Others = append(d.Others, o.row.Name+" ["+later+"]")
					continue
				}
				if junkAuthorCorroborates(later) {
					d.Corroborated = true
				}
			}
		}
		sort.Strings(d.Others)
		break
	}
	// A better source's answer was refused as a work credited elsewhere: that
	// refusal weighs evidence and can be wrong, so a lower source naming
	// someone else does not decide the row in its place; it is held.
	if d.Skip == "" && d.Decision != "" && junkAuthorSourceRank(d.Source) > creditedRefusedAt {
		d.Decision, d.Target, d.Variant, d.NarratorAnswer, d.Others, d.Corroborated = "", database.Author{}, "", false, nil, false
		d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, fmt.Sprintf("%s's answer was refused as a work credited to another author; not decided from %s instead",
			creditedRefusedSrc, d.Source)
		d.Source = ""
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
	// Every answer was refused as a non-author: hold the row with the
	// refusals as its reason rather than unlink it (the evidence named
	// someone; it was not an author).
	sort.Strings(d.Refused)
	sort.Strings(d.RefusedKeys)
	if d.Decision == "" && d.Skip == "" && len(d.Refused) > 0 {
		d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, "no acceptable author; refused: "+strings.Join(d.Refused, "; ")
	}
	// A relink-only verdict ("The Dalai Lama", "50 Cent": real credits are
	// in the rule's reach) moves a book only on strong evidence: the provider,
	// the name itself cleaned, or two independent evidence kinds naming the
	// same author (junkAuthorEvidenceKinds). It never mints: one file tag
	// naming a co-author ("Howard Cutler") would otherwise replace the real
	// credit.
	if v.RelinkOnly() && d.Skip == "" && (d.Decision == junkAuthorDecRelink || d.Decision == junkAuthorDecCreate) {
		strongSrc := d.Source == junkAuthorSrcProvider || d.Source == junkAuthorSrcCleaned
		if d.Decision == junkAuthorDecCreate || (!strongSrc && junkAuthorEvidenceKinds(bySource, chosenKey) < 2) {
			d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, fmt.Sprintf(
				"%q is flagged only by %s; %q [%s] is one uncorroborated answer, so the credit is kept", a.Name, v.Rule, d.Target.Name, d.Source)
		}
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
		case v.RelinkOnly():
			// A shape rule with real credits in its reach ("The Dalai Lama",
			// "50 Cent"): it may move a book to an author found by evidence,
			// never leave it with none.
			d.Skip, d.SkipReason = junkAuthorSkipAmbiguous, fmt.Sprintf("%q is flagged only by %s; with no evidence of the real author the credit is kept", a.Name, v.Rule)
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
		// "Dante King (Dragon Born)": the series is the parenthetical.
		seriesName := a.Name
		if d.Verdict.Rule == authorjunk.RuleSeriesParenthetical {
			if p, ok := authorjunk.SeriesOfParenthetical(a.Name); ok {
				seriesName = p
			}
		}
		d.Series = pickSeries(idx, seriesName, d.Target.ID)
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

// junkAuthorPathKeys are the folded names under which the junk name's own
// author may appear as a folder: the surname before a comma ("Jennsen, GS_
// 08 Rubicon" -> "jennsen"), the cleaned name and its last word ("G. S.
// Jennsen" -> "gsjennsen", "jennsen"), and the junk name whole (a book folder
// named exactly that). Keys shorter than 3 are dropped.
func junkAuthorPathKeys(junk string) map[string]bool {
	out := map[string]bool{}
	put := func(s string) {
		if k := authorjunk.FoldKey(s); len(k) >= 3 {
			out[k] = true
		}
	}
	if head, _, ok := strings.Cut(junk, ","); ok && !strings.ContainsAny(head, " _") {
		put(head)
	}
	if c, ok := authorjunk.CleanedName(junk); ok {
		put(c)
		if f := strings.Fields(c); len(f) > 1 {
			put(f[len(f)-1])
		}
	}
	// The junk name itself: the book folder is often named exactly that.
	put(junk)
	return out
}

// junkAuthorSegmentBelow returns the path segment BELOW candidate's own
// folder that is one of keys (the junk name's own author), or "". A folder
// above the author's folder is not the author: "Pyper Down/Jennsen/Jennsen,
// GS_ 08 Rubicon (Amaranthe 08)/8-06.mp3" is Narrator/Author/Book.
func junkAuthorSegmentBelow(segs []string, candidate string, keys map[string]bool) string {
	if len(keys) == 0 {
		return ""
	}
	ck := authorjunk.FoldKey(candidate)
	// The candidate IS the junk name's author ("Brandon Sanderson" above
	// "Sanderson, B_ Mistborn"): its fold or its surname is a key.
	if keys[ck] {
		return ""
	}
	if f := strings.Fields(candidate); len(f) > 1 && keys[authorjunk.FoldKey(f[len(f)-1])] {
		return ""
	}
	at := -1
	for i, s := range segs {
		left, _, _ := strings.Cut(s, " - ")
		if authorjunk.FoldKey(s) == ck || authorjunk.FoldKey(left) == ck {
			at = i
		}
	}
	if at < 0 {
		return ""
	}
	// Any deeper segment: the author folder ("Jennsen"), or the book folder
	// itself when it is the junk name or its comma head is the surname
	// ("Pyper Down/Jennsen, GS_ 08 Rubicon (Amaranthe 08)/8-06.mp3").
	for _, s := range segs[at+1:] {
		head, _, _ := strings.Cut(s, ",")
		if keys[authorjunk.FoldKey(s)] || keys[authorjunk.FoldKey(head)] {
			return s
		}
	}
	return ""
}

// junkAuthorNarratorKeys folds every name the book says reads it: its Narrator
// string (split on the usual separators), its narrator-role author credits,
// its book_narrators rows and its files' narrator tags (junkAuthorNarratorTagKeys).
func junkAuthorNarratorKeys(full *database.Book, credits []database.BookAuthor, bns []database.BookNarrator, files []database.BookFile, idx *junkAuthorIndex) map[string]bool {
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
	for i := range files {
		for k, v := range files[i].RawTags {
			if !junkAuthorNarratorTagKeys[strings.ToLower(k)] {
				continue
			}
			put(v)
			for _, part := range narratorSplitRe.Split(v, -1) {
				put(part)
			}
		}
	}
	return out
}

// junkAuthorNarratorTagKeys are the raw tag keys (lower case) that name the
// reader: the NARRATOR / PERFORMER / READER precedence the tag reader uses
// (internal/metadata BuildMetadataFromTaglibMap), with their TXXX and MP4
// forms.
var junkAuthorNarratorTagKeys = map[string]bool{
	"narrator": true, "txxx:narrator": true, "©nrt": true,
	"performer": true, "txxx:performer": true,
	"reader": true, "txxx:reader": true,
}

// narratorSplitRe splits a Narrator string into names.
var narratorSplitRe = regexp.MustCompile(`(?i)\s*(?:[,;&/|]|\band\b|\bwith\b)\s*`)

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
	v := idx.classifyName(a.Name)
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

	idx, err := f.cachedIndex(store)
	if err != nil {
		return err
	}
	// The plan's work test before anything is written: a name that became a
	// library title since Replan must not leave a freshly created, empty
	// author row behind.
	if d.Decision == junkAuthorDecCreate {
		if code, why := idx.workTarget(d.Target.Name, 0, d.Book.Title, d.Book.SeriesID); code != "" {
			return fmt.Errorf("%w: book %s: new author %q is %s", repairs.ErrChangedSincePlan, bookID, d.Target.Name, why)
		}
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
	// nor onto another name-alone junk row (the same verdict the plan used:
	// idx.classifyName).
	if target != nil && (target.ID == junkID || idx.classifyName(target.Name).Junk()) {
		return fmt.Errorf("%w: book %s: target %q (id %d) is the junk row or junk itself", repairs.ErrChangedSincePlan, bookID, target.Name, target.ID)
	}
	// The plan's work test, against the index as it is now. A target created
	// just above has no books, so it answers as the mint it was planned as.
	if target != nil {
		if code, why := idx.workTarget(target.Name, target.ID, d.Book.Title, d.Book.SeriesID); code != "" {
			return fmt.Errorf("%w: book %s: target %q (id %d) is %s", repairs.ErrChangedSincePlan, bookID, target.Name, target.ID, why)
		}
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
	// The plan's mint test again, before the row exists: Apply's backstop
	// below the create runs only after MintAuthor has written it.
	if !idx.mintable(name) {
		return nil, fmt.Errorf("%w: book %s: %q is not a name to create an author for", repairs.ErrChangedSincePlan, d.Book.ID, name)
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
		"r", strings.Join(d.RefusedKeys, ";"),
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
