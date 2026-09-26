// file: internal/plugins/maintenance/author_strip_merge_relink.go
// version: 1.1.0
// guid: 771dc90a-8f91-40e6-93bc-60611ebe58b5
// last-edited: 2026-09-26

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/oklog/ulid/v2"
)

// --- author-strip-merge: title-as-author relink ---
//
// 🔴 WHY. A title-as-author row ("Arcane Chef 2", and its numbered twin
// "01 Arcane Chef 2") is a book title the importer stored as the credit. The
// delete path removes the bogus credit and leaves the book with no author at
// all. The owner's decision (2026-09-26) is that such a book should instead
// be credited to its REAL author (T.J. Ward, for Arcane Chef), found from
// evidence the library already holds. Nothing is fetched and nothing is
// guessed:
//
//   - provider: the book's stored provider value for author_name (the
//     metadata field state's FetchedValue);
//   - version: the primary author of another book in the same version group;
//   - file-tag: an artist / album-artist tag on the book's own files;
//   - series: the primary author of another book in the same series.
//
// Every source's answer must pass the same gates: the author creation gate
// (dedup.CleanAuthorNameForCreation and database.CheckAuthorNameForCreation),
// not a placeholder or track artifact, not the junk name, not the book's own
// title (bookTitleNamesAuthor, the title-as-author test itself), not the
// book's narrator (audiobook artist tags often carry the reader), not a
// multi-name credit string, and, for rows found by ID, not itself one of the
// run's junk rows. Answers are compared by dedup.NormalizeAuthorName.
//
// Exactly one distinct answer relinks the book (the junk credit is replaced
// in the junction and, when it named the junk row, the primary AuthorID;
// relinkBookCredit), resolving the author by name and creating it through
// the creation gate when no row exists. No answer, or two different answers,
// leaves the book alone and reports it. A book under books/itunes/** or in
// Doctor Who / Big Finish / Torchwood is never touched.
//
// Opt-in: relink_title_as_author=true. With apply=false (the default) that
// is the preview: one log line per book (book, junk author, chosen author,
// the sources that named it, outcome) and no write. Writes need apply=true
// as well.
//
// 🔴 JOURNAL FIRST (owner decision 2026-09-26). Every write is preceded by an
// OperationChange row that holds what is needed to put the book back:
//
//   - ChangeTypeTitleRelinkAuthorCreate, before an author row is created:
//     the name, and the book whose relink needed it;
//   - ChangeTypeTitleRelinkCredits, before a book's credit is moved:
//     OldValue is the book's full credit list IN ORDER (co-authors, roles,
//     positions) plus its primary AuthorID, read immediately before the
//     write; NewValue names the junk row and the real author.
//
// A journal write that fails skips that book: nothing is written that the
// journal cannot undo. The row is written BEFORE the change it describes, so
// a failure between the two leaves a journal row for a change that did not
// happen; replaying it restores credits the book still has, a no-op.
// replayTitleRelinkJournal is that replay. These change types are not wired
// into the undo engine (internal/undo), which counts them not-restorable.
//
// Sequential on purpose: the population is the books credited to
// title-as-author rows (tens to hundreds on this library, not the library),
// and each is a few point reads.

const (
	titleRelinkSourceProvider = "provider"
	titleRelinkSourceVersion  = "version"
	titleRelinkSourceFileTag  = "file-tag"
	titleRelinkSourceSeries   = "series"
)

const (
	titleRelinkOutcomeRelink      = "relink"
	titleRelinkOutcomeNoCandidate = "no-candidate"
	titleRelinkOutcomeConflict    = "conflict"
	titleRelinkOutcomeITunes      = "skipped-itunes"
	titleRelinkOutcomeOwnerManual = "skipped-owner-manual"
	titleRelinkOutcomeFailed      = "failed"
	// titleRelinkOutcomeAmbiguous: the one answer names two or more existing
	// author rows (duplicate rows normalize to one name). Picking one would
	// be a guess, the same reason the op reports an ambiguous merge.
	titleRelinkOutcomeAmbiguous = "ambiguous"
	// titleRelinkOutcomeDeferred: a relink past the run's limit; left for a
	// later run.
	titleRelinkOutcomeDeferred = "deferred-limit"
)

// Journal change types (see the file comment).
const (
	ChangeTypeTitleRelinkCredits      = "title_relink_credits"
	ChangeTypeTitleRelinkAuthorCreate = "title_relink_author_create"
)

// titleRelinkCreditsSnapshot is a book's credits as the journal stores them.
type titleRelinkCreditsSnapshot struct {
	AuthorID *int                  `json:"author_id"`
	Credits  []database.BookAuthor `json:"credits"`
}

// titleRelinkCreditsChange is NewValue of a credits journal row.
type titleRelinkCreditsChange struct {
	FromAuthorID int `json:"from_author_id"`
	IntoAuthorID int `json:"into_author_id"`
}

// titleRelinkFileTagKeys are the raw tag keys, lower-cased, that name a
// file's artist or album artist across the tag readers (normalized names,
// ID3 frames, MP4 atoms).
var titleRelinkFileTagKeys = map[string]bool{
	"artist": true, "album_artist": true, "albumartist": true, "album artist": true,
	"tpe1": true, "tpe2": true, "©art": true, "aart": true,
}

// titleAuthorRelink is one book's relink decision.
type titleAuthorRelink struct {
	BookID string
	Title  string
	Junk   database.Author
	// Chosen is the real author's name; ChosenID its row, 0 when a report-only
	// run would create it.
	Chosen   string
	ChosenID int
	Sources  []string
	// Candidates lists every distinct answer, "name [sources]", when they
	// conflict.
	Candidates []string
	Outcome    string
}

// titleRelinkIndex is the in-memory evidence the planner reads without a
// store call: siblings by version group and by series, and author rows by ID.
type titleRelinkIndex struct {
	byVersionGroup map[string][]database.BookCore
	bySeries       map[int][]database.BookCore
	authorsByID    map[int]database.Author
	seriesByID     map[int]database.Series
	// byName is the op's own author index, keyed by dedup.NormalizeAuthorName,
	// with every row of a duplicated name.
	byName map[string][]database.Author
	// junk are rows that are never an answer: this run's title-as-author rows
	// and their numbered twins.
	junk map[int]bool
}

func newTitleRelinkIndex(books []database.BookCore, authors []database.Author, byName map[string][]database.Author, seriesByID map[int]database.Series, junk map[int]bool) titleRelinkIndex {
	idx := titleRelinkIndex{
		byName:         byName,
		byVersionGroup: map[string][]database.BookCore{},
		bySeries:       map[int][]database.BookCore{},
		authorsByID:    make(map[int]database.Author, len(authors)),
		seriesByID:     seriesByID,
		junk:           junk,
	}
	for i := range books {
		if books[i].IsSoftDeleted() {
			continue
		}
		if g := books[i].VersionGroupID; g != nil && *g != "" {
			idx.byVersionGroup[*g] = append(idx.byVersionGroup[*g], books[i])
		}
		if s := books[i].SeriesID; s != nil {
			idx.bySeries[*s] = append(idx.bySeries[*s], books[i])
		}
	}
	for _, a := range authors {
		idx.authorsByID[a.ID] = a
	}
	return idx
}

// titleRelinkCandidates collects distinct answers for one book.
type titleRelinkCandidates struct {
	book  database.BookCore
	junk  database.Author
	idx   *titleRelinkIndex
	order []string
	byKey map[string]*titleRelinkCandidate
}

type titleRelinkCandidate struct {
	name    string
	sources map[string]bool
}

func (c *titleRelinkCandidates) add(name, source string) {
	name = strings.TrimSpace(name)
	if !acceptableRelinkName(name, c.book, c.junk, c.idx.seriesByID) {
		return
	}
	key := dedup.NormalizeAuthorName(name)
	cand, ok := c.byKey[key]
	if !ok {
		cand = &titleRelinkCandidate{name: name, sources: map[string]bool{}}
		c.byKey[key] = cand
		c.order = append(c.order, key)
	}
	cand.sources[source] = true
}

// addSiblings adds the primary author of every other live book in sibs whose
// author row is real (not one of the run's junk rows).
func (c *titleRelinkCandidates) addSiblings(sibs []database.BookCore, source string) {
	for i := range sibs {
		if sibs[i].ID == c.book.ID || sibs[i].AuthorID == nil || c.idx.junk[*sibs[i].AuthorID] {
			continue
		}
		if a, ok := c.idx.authorsByID[*sibs[i].AuthorID]; ok {
			c.add(a.Name, source)
		}
	}
}

// acceptableRelinkName is the gate every source's answer must pass (see the
// file comment).
func acceptableRelinkName(name string, book database.BookCore, junk database.Author, seriesByID map[int]database.Series) bool {
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	if strings.ContainsAny(name, ";&/") || strings.Contains(name, ", ") || strings.Contains(lower, " and ") {
		return false // a credit list, not one author
	}
	if _, ok := dedup.CleanAuthorNameForCreation(name); !ok {
		return false
	}
	if database.CheckAuthorNameForCreation(name) != nil {
		return false
	}
	if isPlaceholderAuthorName(name) || isTrackOrTimecodeArtifact(name) {
		return false
	}
	key := dedup.NormalizeAuthorName(name)
	if key == dedup.NormalizeAuthorName(junk.Name) {
		return false
	}
	if bookTitleNamesAuthor(name, book, seriesByID) {
		return false
	}
	if book.Narrator != nil && dedup.NormalizeAuthorName(*book.Narrator) == key {
		return false
	}
	return true
}

// titleRelinkStore is the store surface the relink reads and writes.
type titleRelinkStore interface {
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	GetBookByID(id string) (*database.Book, error)
	CreateOperationChange(change *database.OperationChange) error
}

// titleRelinkJournal writes the journal rows for one run.
type titleRelinkJournal struct {
	store titleRelinkStore
	opID  string
	rows  int
}

func (j *titleRelinkJournal) write(bookID, changeType, field, oldValue, newValue string) error {
	if err := j.store.CreateOperationChange(&database.OperationChange{
		ID:          ulid.Make().String(),
		OperationID: j.opID,
		BookID:      bookID,
		ChangeType:  changeType,
		FieldName:   field,
		OldValue:    oldValue,
		NewValue:    newValue,
	}); err != nil {
		return err
	}
	j.rows++
	return nil
}

// journalCredits records book's current credits (junction in order, and the
// primary) before its credit moves from `from` to `into`.
func (j *titleRelinkJournal) journalCredits(bookID string, from, into int) error {
	credits, err := j.store.GetBookAuthors(bookID)
	if err != nil {
		return fmt.Errorf("read credits for journal: %w", err)
	}
	full, err := j.store.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("read book for journal: %w", err)
	}
	if full == nil {
		return fmt.Errorf("read book for journal: %s not found", bookID)
	}
	snap := titleRelinkCreditsSnapshot{AuthorID: full.AuthorID, Credits: credits}
	oldJSON, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	newJSON, err := json.Marshal(titleRelinkCreditsChange{FromAuthorID: from, IntoAuthorID: into})
	if err != nil {
		return err
	}
	return j.write(bookID, ChangeTypeTitleRelinkCredits, "book_authors", string(oldJSON), string(newJSON))
}

// replayTitleRelinkJournal puts a book's credits back to the OldValue of a
// ChangeTypeTitleRelinkCredits journal row: the junction exactly as recorded
// (order, roles, co-authors), then the primary AuthorID under the book's
// write lock.
func replayTitleRelinkJournal(store interface {
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	GetAuthorByID(id int) (*database.Author, error)
}, c *database.OperationChange) error {
	if c.ChangeType != ChangeTypeTitleRelinkCredits {
		return fmt.Errorf("change %s is %q, not a title relink credits row", c.ID, c.ChangeType)
	}
	var snap titleRelinkCreditsSnapshot
	if err := json.Unmarshal([]byte(c.OldValue), &snap); err != nil {
		return fmt.Errorf("decode journal %s: %w", c.ID, err)
	}
	var primary *database.Author
	if snap.AuthorID != nil {
		a, err := store.GetAuthorByID(*snap.AuthorID)
		if err != nil {
			return fmt.Errorf("load primary author %d: %w", *snap.AuthorID, err)
		}
		primary = a
	}
	if err := store.SetBookAuthors(c.BookID, snap.Credits); err != nil {
		return fmt.Errorf("restore credits of %s: %w", c.BookID, err)
	}
	written, err := store.ModifyBook(c.BookID, func(full *database.Book) error {
		full.AuthorID = nil
		full.Author = nil
		if snap.AuthorID != nil {
			id := *snap.AuthorID
			full.AuthorID = &id
			full.Author = primary
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("restore primary of %s: %w", c.BookID, err)
	}
	if written == nil {
		return fmt.Errorf("restore primary of %s: book not found", c.BookID)
	}
	return nil
}

// titleRelinkResult is the relink step's outcome for the whole run.
type titleRelinkResult struct {
	Relinks []titleAuthorRelink
	// AllRelinked holds the junk rows every one of whose live books got a
	// relink decision (at least one book): nothing is left for their delete
	// to strand.
	AllRelinked map[int]bool
	// RelinkedBooks are the books with a relink decision.
	RelinkedBooks map[string]bool
	// NewAuthors are the distinct author rows the relink created (apply) or
	// would create (report-only).
	NewAuthors []authorPathLinkCreatedAuthor
	// JournalRows counts the journal rows written.
	JournalRows int
}

// relinkTitleAsAuthorBooks decides, and with write set performs, the relink
// of every live book credited to a row in junkRows. limit > 0 caps the
// relinks (decided, and with write set, written) at the first limit books in
// junk-row-ID then book-ID order, so a limited preview lists exactly the
// prefix a limited apply writes; the rest are reported as deferred.
func relinkTitleAsAuthorBooks(ctx context.Context, store titleRelinkStore, creator *authorPathLinkCreator, junkRows []database.Author, idx *titleRelinkIndex, write bool, limit int, opID string, log *slog.Logger) (titleRelinkResult, error) {
	res := titleRelinkResult{AllRelinked: map[int]bool{}, RelinkedBooks: map[string]bool{}}
	journal := &titleRelinkJournal{store: store, opID: opID}
	defer func() { res.JournalRows = journal.rows }()
	junkRows = append([]database.Author{}, junkRows...)
	sort.Slice(junkRows, func(i, j int) bool { return junkRows[i].ID < junkRows[j].ID })
	decided := 0
	for _, junk := range junkRows {
		books, err := store.GetBooksByAuthorIDForRelinkCore(junk.ID)
		if err != nil {
			log.Warn("author-strip-merge relink: cannot read credits; leaving the row's books",
				"author_id", junk.ID, "err", err)
			continue
		}
		sort.Slice(books, func(i, j int) bool { return books[i].ID < books[j].ID })
		live, relinked := 0, 0
		for i := range books {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if books[i].IsSoftDeleted() {
				continue // the delete path unlinks trashed books; nothing to credit
			}
			live++
			bookWrite, over := write, limit > 0 && decided >= limit
			if over {
				bookWrite = false
			}
			r := relinkOneTitleBook(store, creator, journal, books[i], junk, idx, bookWrite, !over, log)
			if r.Outcome == titleRelinkOutcomeRelink {
				if over {
					r.Outcome = titleRelinkOutcomeDeferred
				} else {
					decided++
				}
			}
			res.Relinks = append(res.Relinks, r)
			if r.Outcome == titleRelinkOutcomeRelink {
				relinked++
				res.RelinkedBooks[r.BookID] = true
			}
		}
		if live > 0 && relinked == live {
			res.AllRelinked[junk.ID] = true
		}
	}
	res.NewAuthors = creator.created()
	res.JournalRows = journal.rows
	return res, nil
}

// write performs the relink; allowCreate lets the creator mint (or, in a
// preview, count) a missing author row. A book past the limit has neither.
func relinkOneTitleBook(store titleRelinkStore, creator *authorPathLinkCreator, journal *titleRelinkJournal, book database.BookCore, junk database.Author, idx *titleRelinkIndex, write, allowCreate bool, log *slog.Logger) titleAuthorRelink {
	r := titleAuthorRelink{BookID: book.ID, Title: book.Title, Junk: junk}
	defer func() {
		log.Info("author-strip-merge relink",
			"book_id", r.BookID, "title", r.Title,
			"junk_author_id", r.Junk.ID, "junk_author", r.Junk.Name,
			"chosen_author", r.Chosen, "chosen_author_id", r.ChosenID,
			"sources", strings.Join(r.Sources, ","), "candidates", strings.Join(r.Candidates, "; "),
			"outcome", r.Outcome, "write", write)
	}()

	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		r.Outcome = titleRelinkOutcomeFailed
		log.Warn("author-strip-merge relink: GetBookFiles failed", "book_id", book.ID, "err", err)
		return r
	}
	seriesName := ""
	if book.SeriesID != nil {
		seriesName = idx.seriesByID[*book.SeriesID].Name
	}
	// book.file_path can be stale, so the file rows are checked too.
	paths := []string{book.FilePath}
	for i := range files {
		paths = append(paths, files[i].FilePath)
	}
	for _, p := range paths {
		if pathutil.UnderFrozenITunesTree(p) {
			r.Outcome = titleRelinkOutcomeITunes
			return r
		}
	}
	if junkTitleOwnerManual(book.FilePath, book.Title, seriesName) {
		r.Outcome = titleRelinkOutcomeOwnerManual
		return r
	}
	for _, p := range paths {
		if junkTitleOwnerManual(p, "", "") {
			r.Outcome = titleRelinkOutcomeOwnerManual
			return r
		}
	}

	states, err := store.GetMetadataFieldStates(book.ID)
	if err != nil {
		r.Outcome = titleRelinkOutcomeFailed
		log.Warn("author-strip-merge relink: GetMetadataFieldStates failed", "book_id", book.ID, "err", err)
		return r
	}

	cands := &titleRelinkCandidates{book: book, junk: junk, idx: idx, byKey: map[string]*titleRelinkCandidate{}}
	for i := range states {
		if states[i].Field != "author_name" {
			continue
		}
		if v, ok := metastate.Decode(states[i].FetchedValue).(string); ok {
			cands.add(v, titleRelinkSourceProvider)
		}
	}
	if g := book.VersionGroupID; g != nil && *g != "" {
		cands.addSiblings(idx.byVersionGroup[*g], titleRelinkSourceVersion)
	}
	for i := range files {
		for k, v := range files[i].RawTags {
			if titleRelinkFileTagKeys[strings.ToLower(k)] {
				cands.add(v, titleRelinkSourceFileTag)
			}
		}
	}
	if book.SeriesID != nil {
		cands.addSiblings(idx.bySeries[*book.SeriesID], titleRelinkSourceSeries)
	}

	switch len(cands.order) {
	case 0:
		r.Outcome = titleRelinkOutcomeNoCandidate
		return r
	case 1:
	default:
		for _, k := range cands.order {
			c := cands.byKey[k]
			r.Candidates = append(r.Candidates, c.name+" ["+strings.Join(sortedKeys(c.sources), ",")+"]")
		}
		r.Outcome = titleRelinkOutcomeConflict
		return r
	}
	chosen := cands.byKey[cands.order[0]]
	r.Chosen = chosen.name
	r.Sources = sortedKeys(chosen.sources)

	// Resolve against the op's own name index first: it keeps every row of a
	// duplicated name, where GetAuthorByName would silently pick one.
	var author database.Author
	switch rows := idx.byName[dedup.NormalizeAuthorName(chosen.name)]; {
	case len(rows) > 1:
		for _, a := range rows {
			r.Candidates = append(r.Candidates, fmt.Sprintf("%s [id %d]", a.Name, a.ID))
		}
		r.Outcome = titleRelinkOutcomeAmbiguous
		return r
	case len(rows) == 1:
		author = rows[0]
	default:
		// No row yet: resolve-or-create through the creation gate. The
		// creator is in dry-run mode for a preview (and for a book past the
		// limit): a missing row is counted as would-create and returns ID 0.
		// For a write, look first WITHOUT creating, so the creation can be
		// journaled before it happens.
		a, _, err := creator.resolveOrCreate(chosen.name, allowCreate && !write)
		if err == nil && a.ID <= 0 && write && allowCreate {
			if jErr := journal.write(book.ID, ChangeTypeTitleRelinkAuthorCreate, "author_name", "", chosen.name); jErr != nil {
				r.Outcome = titleRelinkOutcomeFailed
				log.Warn("author-strip-merge relink: journal write failed; author not created, book skipped",
					"book_id", book.ID, "name", chosen.name, "err", jErr)
				return r
			}
			a, _, err = creator.resolveOrCreate(chosen.name, true)
		}
		if err != nil {
			r.Outcome = titleRelinkOutcomeFailed
			log.Warn("author-strip-merge relink: resolve author failed", "book_id", book.ID, "name", chosen.name, "err", err)
			return r
		}
		author = a
	}
	if author.ID > 0 && idx.junk[author.ID] {
		// The name resolves to one of this run's junk rows: not an answer.
		r.Outcome = titleRelinkOutcomeNoCandidate
		return r
	}
	if author.ID <= 0 && write {
		// The creation gate refused the name after all.
		r.Outcome = titleRelinkOutcomeNoCandidate
		return r
	}
	r.ChosenID = author.ID
	r.Outcome = titleRelinkOutcomeRelink
	if !write {
		return r
	}
	if jErr := journal.journalCredits(book.ID, junk.ID, author.ID); jErr != nil {
		r.Outcome = titleRelinkOutcomeFailed
		log.Warn("author-strip-merge relink: journal write failed; book skipped",
			"book_id", book.ID, "from_id", junk.ID, "into_id", author.ID, "err", jErr)
		return r
	}
	primaryErr, err := relinkBookCredit(store, book, junk, author, false)
	if err == nil {
		err = primaryErr
	}
	if err != nil {
		r.Outcome = titleRelinkOutcomeFailed
		log.Warn("author-strip-merge relink: write failed", "book_id", book.ID, "from_id", junk.ID, "into_id", author.ID, "err", err)
	}
	return r
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
