// file: internal/plugins/maintenance/junk_title_fixer.go
// version: 1.21.2
// guid: 7c3e9a15-2b6d-4f48-a9e1-5d0b8c4f7a26
// last-edited: 2026-10-24

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"golang.org/x/sync/errgroup"
	"golang.org/x/text/unicode/norm"
)

// junkTitlesFixerID is the Repairs-lane id of the junk-title fixer. It is the
// id the retired maintenance.repair-junk-titles op wrote history under, so a
// book's history reads the same before and after the op was absorbed here.
const junkTitlesFixerID = "maintenance.repair-junk-titles"

// Skip kinds of the junk-title fixer's own rows, named like the framework's
// (repairs.SkipITunes = "skipped_itunes") and the fragment-consolidation
// fixer's ("skipped_ambiguous", ...), so the Repairs tab lists one family.
const (
	// junkSkipFragment: PROVEN to be a piece of another book — another live
	// book has a book_file row at one of this book's paths. The
	// fragment-consolidation fixer folds it back in; it is never retitled.
	junkSkipFragment = "skipped_fragment"
	// junkSkipPossibleFragment: the shape of a chapter file of a bigger book
	// (a folder shared with other books, a lone chapter-titled file, the
	// author named like the folder, a proposal that duplicates another book),
	// but no parent is proven. Left for a person to decide.
	junkSkipPossibleFragment = "skipped_possible_fragment"
	// junkSkipNeedsManual: no trustworthy replacement title was found.
	junkSkipNeedsManual = "skipped_needs_manual"
	// junkSkipUserLocked: the title carries a user override; never touched.
	junkSkipUserLocked = "skipped_user_locked"
	// junkSkipRepairLocked: the field carries the lock a Repairs apply set
	// (database.MetadataFieldState.IsRepairLock), not a person's; reverting
	// that operation lifts it.
	junkSkipRepairLocked = "skipped_repair_locked"
	// junkSkipNotJunk / junkSkipGone: what a re-plan reports for a book
	// whose title stopped being junk, or that no longer exists. A plan never
	// lists either.
	junkSkipNotJunk = "skipped_not_junk"
	junkSkipGone    = "skipped_gone"
	// junkSkipSwapped: the stored author is the title a provider recorded,
	// so title and author are swapped. maintenance.repair-swapped-title-author
	// fixes both; retitling here first would leave the author holding the
	// title.
	junkSkipSwapped = "skipped_swapped_title_author"
)

// junkMinDurationSeconds is the shortest a book may run for a title taken from
// outside its own stored title (a transcription, a folder, a filename, the
// series) to be proposed: 30 minutes. A book under it is almost always one
// track of a bigger book, and the title those sources find is the WHOLE
// work's: the 2026-10-23 review found ~50 rows of a language course whose
// every 4-minute lesson track was about to be retitled with its own lesson
// name, and 37 more "title right, book is a 9 s intro fragment" rows. The
// runtime is the book's own Duration, or the sum of its files' when the book
// has none and every file does (bookRuntimeSec). A book with no known runtime
// fails closed: nothing says it is a whole book, so it goes to a person
// (needs_manual) rather than being retitled from outside evidence.
const junkMinDurationSeconds = 30 * 60

// junkLibraryStateSuspicious is the library_state the scanner gives a file it
// could not read cleanly (scanner.go). Its path and tags are not evidence.
const junkLibraryStateSuspicious = "suspicious"

// Sources of a proposed title. Priority, best first: title_prefix_stripped,
// metadata_candidate, provider_value, then transcription, folder, filename,
// series_and_number. The transcription is the weakest of the evidence that
// names the title itself (the 2026-10-23 review: 261 of its 362 rows wrong),
// so it no longer outranks a stripped or provider title.
const (
	junkSrcTranscribed = "transcription"
	junkSrcCandidate   = "metadata_candidate"
	// junkSrcProvider: the title a metadata provider returned for this book,
	// recorded in the title's field state but never applied (a scheduled
	// fetch only fills empty fields, and the junk title was not empty).
	junkSrcProvider  = "provider_value"
	junkSrcStripped  = "title_prefix_stripped"
	junkSrcFolder    = "folder"
	junkSrcFilename  = "filename"
	junkSrcSeriesNum = "series_and_number"
)

// junkCandidateMinScore is the lowest metafetch candidate score the fixer
// takes a title from (scores are 0..1). The cached search ran on the junk
// title, so a candidate must also agree on the author (candidateTitle).
const junkCandidateMinScore = 0.5

// junkIndexTTL bounds how long Replan reuses the library-wide index during an
// apply. Rebuilding it lists every book, so it is not done per row.
const junkIndexTTL = 2 * time.Minute

// junkTitleFixer finds books whose stored title is not a title (a chapter
// number, a narrator credit, a track tag, a placeholder, a bare roman numeral,
// a junk prefix) and proposes the real one. It absorbed the
// maintenance.repair-junk-titles op, which matched an exact five-string set.
//
// It keeps no memory of its own writes: two rows that would propose the same
// title for the same author are both refused at plan time, so an apply can
// never write one title twice, and an undone row can be applied again.
type junkTitleFixer struct {
	p *Plugin

	// minDurationSec is the shortest book a non-prefix-stripped title may be
	// proposed for (junkMinDurationSeconds; 0 or less turns the gate off).
	minDurationSec int

	idxMu      sync.Mutex
	idx        *junkIndex
	idxBuiltAt time.Time
}

func newJunkTitleFixer(p *Plugin) *junkTitleFixer {
	return &junkTitleFixer{p: p, minDurationSec: junkMinDurationSeconds}
}

var _ repairs.Fixer = (*junkTitleFixer)(nil)

func (f *junkTitleFixer) ID() string    { return junkTitlesFixerID }
func (f *junkTitleFixer) Title() string { return "Junk titles" }
func (f *junkTitleFixer) Description() string {
	return "Books titled with a chapter number (\"01\", \"Chapter 12\", \"Disc 2\"), a narrator credit " +
		"(\"read by narrator\"), a track tag (\"Opening\", \"Intro\"), a placeholder (\"Unknown Title\"), a bare " +
		"roman numeral, or a title behind a track-number or punctuation prefix. Proposes the real title from, in " +
		"order: the title without its prefix, a matching metadata candidate, a title a provider recorded for " +
		"the book, the intro transcription (its series or genre tail dropped; announcer phrases, sentences and " +
		"format markers refused), the folder or filename (chapter, track, credit and placeholder markers and a " +
		"trailing author refused or dropped), the series and number (only with a matching recorded series " +
		"position). A title found anywhere but the stripped one is not proposed for a book that runs under 30 " +
		"minutes or that the scanner marked suspicious: it is likely one track of a bigger book. " +
		"A book another book owns a file of is a fragment for the consolidation " +
		"fixer; one that only looks like a chapter file is listed for a person. A provider's recorded title counts only when " +
		"it agrees with the title's own evidence or was recorded under the book's author (for a book whose stored " +
		"author is junk, when it agrees with the path or the transcription). A book whose author holds the " +
		"title a provider recorded (title and author swapped) is listed for the swapped title/author fixer " +
		"(maintenance.repair-swapped-title-author), which runs first and fixes both. Writes the title only, never a " +
		"user-locked one, and locks the title it wrote so a rescan cannot restore the file tag's junk. Undo with the " +
		"apply operation's revert, which also lifts the lock."
}

// junkIndex is the library-wide state a decision reads besides the book
// itself. It depends only on paths, membership and existing titles, never on
// another junk row's proposal, so applying one row cannot change another's
// fingerprint except through the duplicate-title check it exists for.
type junkIndex struct {
	// dirBooks counts live books per work folder (junkWorkDir); dirChapters
	// counts those among them titled only by a chapter position.
	dirBooks    map[string]int
	dirChapters map[string]int
	// titles holds "<author id>|<TitleSortKey>" of every live book whose
	// title is real, so a proposal that duplicates an existing book of the
	// same author is caught.
	titles  map[string]bool
	authors map[int]string
	series  map[int]string
	// titleAuthors maps util.TitleSortKey of every live real title to the
	// author ids of the books that hold it (0 for a book with no author). The
	// duplicate guard reads it when the stored author is junk and "<author
	// id>|<title>" in titles cannot be keyed.
	titleAuthors map[string][]int
	// authorNames maps util.NormalizeAuthor of every author name that is not
	// junk to the ids of the author rows that carry it, and authorBooks counts
	// the live books credited to each id (isLibraryAuthor): a filename's or
	// folder's " - <name>" credit that is one is stripped or refused. A book
	// is credited to its primary author and to every author-role row of the
	// book_authors junction (a co-author has no other trace), once each;
	// bookAuthors holds the ids each live book was counted under, so the book
	// being judged can be left out of its own authors' counts.
	authorNames map[string][]int
	authorBooks map[int]int
	bookAuthors map[string][]int
	// owners maps each file path of a junk-titled book to every live book
	// with a book_file row at that path. GetBookFileByPath cannot answer
	// this: its index holds one id per path and the last writer wins, which
	// is the fragment itself when the scanner imported a chapter the parent
	// already owned.
	owners map[string][]string
	// roots are the library root and every import path, cleaned. A proposal
	// that names one of their segments is a path, not a title.
	roots []string
	// authorTitles maps an author id to the distinct real titles (by letters
	// key, junk titles left out) of the live books whose primary author it
	// is, each with one spelling. The swapped title/author fixer reads it:
	// an author record that credits other real titles is a real author,
	// never a misplaced title.
	authorTitles map[int]map[string]string
	// authorRoot is the library root when the organizer files books
	// author-first ("{author}/…", the default folder pattern): the folder
	// right below it is an author folder, never a title. Import paths are
	// not in it; their layouts are whatever the source had, often flat.
	authorRoot string
}

// junkWorkDir is the folder that names the work a book path belongs to: the
// book's folder (the file's folder for a file path), climbing past up to two
// disc/part/chapter-number folders ("Eldest/CD1", the organizer-moved
// "Eldest/02/98/98.mp3").
//
// A number folder named like the book's own title is that book's folder, not
// a chapter folder: "Kelley Armstrong/13/13.m4b" is the book "13" filed
// author/title, so the climb stops there — unless another number folder sits
// right above it, which is the organizer-moved chapter shape ("02/98/98").
func junkWorkDir(p, title string) string {
	if p == "" {
		return ""
	}
	d := p
	if _, audio := audioExts[strings.ToLower(filepath.Ext(p))]; audio {
		d = filepath.Dir(p)
	}
	for range 2 {
		base := filepath.Base(d)
		if !metadata.IsChapterOnlyTitle(base) {
			break
		}
		if strings.EqualFold(strings.TrimSpace(base), strings.TrimSpace(title)) &&
			!metadata.IsChapterOnlyTitle(filepath.Base(filepath.Dir(d))) {
			break
		}
		d = filepath.Dir(d)
	}
	return d
}

func junkTitleKey(authorID *int, title string) string {
	a := 0
	if authorID != nil {
		a = *authorID
	}
	return strconv.Itoa(a) + "|" + util.TitleSortKey(title)
}

// isLibraryAuthor reports whether s is the name of an author of the library
// ("Last, First" too) and not also the title of a live book. The author table
// holds rows that are really titles (the transposed title/author problem), and
// treating one of those as a person deletes title text from a filename or
// refuses a real title. Two things mark such a row: a live book holds the name
// as its title, or no book other than the one being judged is credited to it
// (an author row with no books, or whose only book is this one, whose stored
// author is the title text). bookID is the judged book: it is left out of the
// count of every author it is credited to, primary or co-author. Read-only
// after the index is built.
func (idx *junkIndex) isLibraryAuthor(s, bookID string) bool {
	if len(idx.titleAuthors[util.TitleSortKey(s)]) > 0 {
		return false
	}
	own := idx.bookAuthors[bookID]
	for _, form := range personNameForms(s) {
		for _, id := range idx.authorNames[form] {
			n := idx.authorBooks[id]
			if slices.Contains(own, id) {
				n--
			}
			if n > 0 {
				return true
			}
		}
	}
	return false
}

// narratorsOf splits a book's denormalized narrator credit into names, for
// ClassifyJunkTitleFor ("Read by Kate Reading" is junk only on her book).
func narratorsOf(n *string) []string {
	if n == nil || strings.TrimSpace(*n) == "" {
		return nil
	}
	return splitCreditNames(*n)
}

// buildJunkIndex lists every book once (one consistent snapshot) and returns
// the index plus the junk-titled candidates.
func (f *junkTitleFixer) buildJunkIndex() (*junkIndex, []database.BookCore, error) {
	idx, cands, _, err := f.buildJunkIndexSnapshot()
	return idx, cands, err
}

// buildJunkIndexSnapshot is buildJunkIndex that also returns the whole book
// listing it was built from, so the swapped title/author fixer can pick its
// author-only candidates (real titles) from the same snapshot instead of
// listing every book a second time.
func (f *junkTitleFixer) buildJunkIndexSnapshot() (*junkIndex, []database.BookCore, []database.BookCore, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, nil, nil, fmt.Errorf("database not initialized")
	}
	all, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("GetAllSeries: %w", err)
	}
	imports, err := store.GetAllImportPaths()
	if err != nil {
		// Fail closed: without the roots the path-segment refusal is blind.
		return nil, nil, nil, fmt.Errorf("GetAllImportPaths: %w", err)
	}
	idx := &junkIndex{dirBooks: map[string]int{}, dirChapters: map[string]int{}, titles: map[string]bool{},
		authors: map[int]string{}, series: map[int]string{}, owners: map[string][]string{},
		authorTitles: map[int]map[string]string{}, titleAuthors: map[string][]int{},
		authorNames: map[string][]int{}, authorBooks: map[int]int{}, bookAuthors: map[string][]int{}}
	allAuthors, err := store.GetAllAuthors()
	if err != nil {
		// Fail closed: without the author list a filename's author suffix is
		// not recognised and the duplicate guard cannot name an owner.
		return nil, nil, nil, fmt.Errorf("GetAllAuthors: %w", err)
	}
	// Classifying a name (authorjunk.ClassifyName) is CPU-only and touches no
	// shared state, and there are ~16k authors on a real library, so the
	// verdicts are computed on a bounded pool, each worker writing its own
	// slots of realName. The maps are built serially afterwards, in author
	// order, so the index does not depend on the scheduling.
	realName := make([]bool, len(allAuthors))
	var classify errgroup.Group
	classify.SetLimit(titleRepairWorkers())
	const classifyChunk = 512
	for lo := 0; lo < len(allAuthors); lo += classifyChunk {
		hi := min(lo+classifyChunk, len(allAuthors))
		classify.Go(func() error {
			for i := lo; i < hi; i++ {
				n := strings.TrimSpace(allAuthors[i].Name)
				realName[i] = n != "" && !junkStoredAuthor(n)
			}
			return nil
		})
	}
	_ = classify.Wait() // the workers return no error
	for i := range allAuthors {
		idx.authors[allAuthors[i].ID] = allAuthors[i].Name
		if realName[i] {
			k := util.NormalizeAuthor(strings.TrimSpace(allAuthors[i].Name))
			idx.authorNames[k] = append(idx.authorNames[k], allAuthors[i].ID)
		}
	}
	if config.AppConfig.RootDir != "" {
		root := filepath.Clean(config.AppConfig.RootDir)
		idx.roots = append(idx.roots, root)
		pattern := strings.TrimSpace(config.AppConfig.FolderNamingPattern)
		if pattern == "" {
			pattern = config.DefaultFolderNamingPattern
		}
		if strings.HasPrefix(pattern, "{author}") {
			idx.authorRoot = root
		}
	}
	for i := range imports {
		if p := strings.TrimSpace(imports[i].Path); p != "" {
			idx.roots = append(idx.roots, filepath.Clean(p))
		}
	}
	for i := range allSeries {
		idx.series[allSeries[i].ID] = allSeries[i].Name
	}
	var cands []database.BookCore
	// A single pass of map inserts over the snapshot: no per-book I/O here,
	// the per-book reads run on the worker pool in Plan.
	for i := range all {
		b := &all[i]
		if b.IsSoftDeleted() {
			continue
		}
		idx.bookAuthors[b.ID] = nil // a live book; its authors are filled below
		if b.AuthorID != nil {
			idx.bookAuthors[b.ID] = []int{*b.AuthorID}
		}
		kind := metadata.ClassifyJunkTitleFor(b.Title, narratorsOf(b.Narrator))
		if d := junkWorkDir(b.FilePath, b.Title); d != "" {
			idx.dirBooks[d]++
			if kind.IsChapterKind() {
				idx.dirChapters[d]++
			}
		}
		if kind == metadata.JunkNone {
			idx.titles[junkTitleKey(b.AuthorID, b.Title)] = true
			{
				aid := 0
				if b.AuthorID != nil {
					aid = *b.AuthorID
				}
				k := util.TitleSortKey(b.Title)
				if !slices.Contains(idx.titleAuthors[k], aid) {
					idx.titleAuthors[k] = append(idx.titleAuthors[k], aid)
				}
			}
			if b.AuthorID != nil {
				if k := junkLettersKey(b.Title); k != "" {
					if idx.authorTitles[*b.AuthorID] == nil {
						idx.authorTitles[*b.AuthorID] = map[string]string{}
					}
					if _, seen := idx.authorTitles[*b.AuthorID][k]; !seen {
						idx.authorTitles[*b.AuthorID][k] = b.Title
					}
				}
			}
			continue
		}
		cands = append(cands, *b)
	}

	// A co-author is credited only through the book_authors junction. One
	// read of every credit row (not a store read per book) adds each live
	// book to each of its author-role rows' counts; fail closed when the
	// credits cannot be read, since a half-counted author is wrongly judged
	// not to be one.
	if cs, ok := database.AsCapability[database.BookCreditIndexStore](store); ok {
		credits, cerr := cs.GetAllBookAuthorRoleCredits()
		if cerr != nil {
			return nil, nil, nil, fmt.Errorf("GetAllBookAuthorRoleCredits: %w", cerr)
		}
		for bookID, rows := range credits {
			ids, live := idx.bookAuthors[bookID]
			if !live {
				continue
			}
			for _, row := range rows {
				if !slices.Contains(ids, row.AuthorID) {
					ids = append(ids, row.AuthorID)
				}
			}
			idx.bookAuthors[bookID] = ids
		}
	}
	for _, ids := range idx.bookAuthors {
		for _, id := range ids {
			idx.authorBooks[id]++
		}
	}

	// File ownership: two passes over one listing of every book_file row.
	// The first keeps the paths of junk-titled books, the second collects
	// every live owner of those paths. Both are plain map lookups per row.
	live := make(map[string]bool, len(all))
	for i := range all {
		if !all[i].IsSoftDeleted() {
			live[all[i].ID] = true
		}
	}
	candIDs := make(map[string]bool, len(cands))
	for i := range cands {
		candIDs[cands[i].ID] = true
	}
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	for i := range files {
		if candIDs[files[i].BookID] && files[i].FilePath != "" {
			idx.owners[files[i].FilePath] = nil
		}
	}
	for i := range files {
		if _, want := idx.owners[files[i].FilePath]; want && live[files[i].BookID] {
			idx.owners[files[i].FilePath] = append(idx.owners[files[i].FilePath], files[i].BookID)
		}
	}
	return idx, cands, all, nil
}

func (f *junkTitleFixer) cachedIndex() (*junkIndex, error) {
	f.idxMu.Lock()
	defer f.idxMu.Unlock()
	if f.idx != nil && time.Since(f.idxBuiltAt) < junkIndexTTL {
		return f.idx, nil
	}
	idx, _, err := f.buildJunkIndex()
	if err != nil {
		return nil, err
	}
	f.idx, f.idxBuiltAt = idx, time.Now()
	return idx, nil
}

// Plan lists every junk-titled book as a row: applicable when a replacement
// was found, skipped (fragment / possible_fragment / needs_manual /
// user_locked / owner-manual) otherwise.
func (f *junkTitleFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	idx, cands, err := f.buildJunkIndex()
	if err != nil {
		return nil, err
	}
	f.idxMu.Lock()
	f.idx, f.idxBuiltAt = idx, time.Now()
	f.idxMu.Unlock()

	rows := make([]repairs.Row, len(cands))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i; idx is read-only here
	// apart from the author-name cache, which authorName locks.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(_ context.Context, i int) error {
		defer done.Add(1)
		r, err := f.evaluate(idx, cands[i])
		if err != nil {
			r = repairs.Row{RowID: cands[i].ID, BookIDs: []string{cands[i].ID}, Title: cands[i].Title,
				Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = junkFingerprint(r, "")
		}
		rows[i] = r
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Junk titles %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Two junk rows proposing the same title for the same author look like
	// two pieces of one book, but nothing proves a parent: neither is
	// retitled into a duplicate, and a person decides.
	//
	// Rows are grouped by the title alone; within a group a row is held when
	// ANY other row of it may be the same owner's (ownersMayBeSame: the same
	// author id, the same person by name, or either owner unknown), the rule
	// titleTaken applies to a live book. Not clustered: an unknown owner
	// matches two named ones who do not match each other, and each of those
	// pairs is held while the two named rows alone are not.
	byTitle := map[string][]int{}
	for i := range rows {
		if !rows[i].Applicable() || rows[i].Proposed == nil {
			continue
		}
		if d, ok := rows[i].Detail.(*junkDecision); ok && d != nil {
			k := util.TitleSortKey(d.newTitle)
			byTitle[k] = append(byTitle[k], i)
		}
	}
	for _, ix := range byTitle {
		if len(ix) < 2 {
			continue
		}
		clash := map[int]int{} // row -> other rows of the group it may duplicate
		for a := range ix {
			for b := a + 1; b < len(ix); b++ {
				da, _ := rows[ix[a]].Detail.(*junkDecision)
				db, _ := rows[ix[b]].Detail.(*junkDecision)
				if ownersMayBeSame(da.owner, db.owner) {
					clash[ix[a]]++
					clash[ix[b]]++
				}
			}
		}
		for i, n := range clash {
			why := fmt.Sprintf("possible fragment: %d junk-titled books of this author (or of an unknown one) would all be retitled %q",
				n+1, rows[i].Proposed["title"])
			rows[i].Skipped, rows[i].SkipReason, rows[i].Reason = junkSkipPossibleFragment, why, why
			rows[i].Detail = nil
			rows[i].Fingerprint = junkFingerprint(rows[i], why)
		}
	}
	return rows, nil
}

func indexesOf(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// Replan re-reads the book and decides it again with the (short-lived)
// cached index. It always returns planned.RowID: a book that is gone or no
// longer junk comes back as a skipped row whose fingerprint differs, so the
// engine reports changed_since_plan rather than failed, and a re-run of an
// interrupted apply passes over the rows it already wrote.
func (f *junkTitleFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
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
		r.Fingerprint = junkFingerprint(r, "gone")
		return r, nil
	}
	idx, err := f.cachedIndex()
	if err != nil {
		return repairs.Row{}, err
	}
	return f.evaluate(idx, b.Core())
}

// junkDecision travels from Replan to Apply in Row.Detail.
type junkDecision struct {
	bookID, oldTitle, newTitle string
	// owner is whose title the proposal would be: Plan holds two rows that
	// propose one title for owners that may be the same person.
	owner junkOwner
}

// Apply writes the title, and only the title, while it is still the junk one
// the replacement was derived from and no user lock has appeared.
func (f *junkTitleFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*junkDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", junkTitlesFixerID, fresh.RowID)
	}
	return writeTitleOnly(w, f.p.deps.OpsStore(), d.bookID, d.oldTitle, d.newTitle)
}

// titleStateReader reads a book's field provenance, rows and pre-migration
// blob both; writeTitleOnly re-checks the title lock with it inside the write
// (database.LockedUserFields, which reads a blob-only book's blob).
type titleStateReader = database.MetadataFieldStateReader

// writeTitleOnly sets Title through the framework writer, which records the
// history row for the book's history view, journals the change in the apply
// op's journal, and locks the title. Shared by the junk-title and swapped
// title/author fixers.
//
// Journal and lock: the file tags still hold the junk (or swapped) value the
// scanner first read, and a forced rescan writes the tags back over the title
// unless it is locked (scanner.applyScannerFields). So the title is locked
// (repairs.Writer.LockFields) once it is written. The lock lives in the
// field state, which metadata history does not cover, so the title is
// journaled too (a metadata_update row) and the whole row is undone by the op
// revert (POST /operations/<id>/revert), which puts the title back and lifts
// the lock. "Undo last apply" refuses these batches (apply_op_journaled):
// reverting the title from history alone would leave the junk title locked.
//
// The title is journaled before the write (repairs.Writer.JournalStep). A
// write refused as changed_since_plan (the title moved, or a lock landed)
// voids that row: left in place, the op revert would "restore" the junk title
// over whatever the book holds then, another operation's write included. A
// lock that fails after the title was written is reported as a partial apply;
// the lock's own row is voided by LockFields.
//
// A title that moved since the re-plan, or that a user locked since, is
// refused as changed_since_plan: returning database.ErrSkipBookWrite here
// would make Modify report success with nothing written.
//
// The lock re-check runs inside the ModifyBook callback, so it is as late as
// the write can make it. Field-state writes do not take the book stripe, so a
// lock landing between this read and the commit still races; the window is
// the width of one Pebble batch instead of a whole apply.
func writeTitleOnly(w *repairs.Writer, states titleStateReader, bookID, oldTitle, newTitle string) error {
	if states == nil {
		return fmt.Errorf("book %s: no field-state reader to check the title lock with", bookID)
	}
	var changed []string
	err := w.JournalStep(bookID, repairs.UndoEntry{ChangeType: "metadata_update", Field: "title",
		Old: oldTitle, New: newTitle}, func() error {
		var merr error
		changed, merr = w.Modify(bookID, func(cur *database.Book) error {
			if cur.Title != oldTitle {
				return fmt.Errorf("%w: title is now %q", repairs.ErrChangedSincePlan, cur.Title)
			}
			locked, err := database.LockedUserFields(states, bookID)
			if err != nil {
				return fmt.Errorf("read field locks of %s: %w", bookID, err)
			}
			if locked[database.FieldKeyTitle] {
				return fmt.Errorf("%w: the title is now locked", repairs.ErrChangedSincePlan)
			}
			cur.Title = newTitle
			return nil
		})
		return merr
	})
	if err != nil {
		return err
	}
	if !slices.Contains(changed, "title") {
		return fmt.Errorf("book %s: the write committed but recorded no title change", bookID)
	}
	if err := w.LockFields(bookID, database.FieldKeyTitle); err != nil {
		return fmt.Errorf("%w: book %s title written but not locked: %w", repairs.ErrPartiallyApplied, bookID, err)
	}
	return nil
}

func (f *junkTitleFixer) authorName(idx *junkIndex, id *int) string {
	if id == nil {
		return ""
	}
	f.idxMu.Lock()
	n, ok := idx.authors[*id]
	f.idxMu.Unlock()
	if ok {
		return n
	}
	if store := f.p.deps.OpsStore(); store != nil {
		if a, err := store.GetAuthorByID(*id); err == nil && a != nil {
			n = a.Name
		}
	}
	f.idxMu.Lock()
	idx.authors[*id] = n
	f.idxMu.Unlock()
	return n
}

var (
	trailingNumberRe = regexp.MustCompile(`(\d+)\s*$`)
	// bareNumberRe is an unpadded number and nothing else ("13", "300"). It
	// is as likely a real title as a chapter position, so it is never called
	// a possible fragment on shape alone.
	bareNumberRe = regexp.MustCompile(`^[1-9]\d*$`)
)

// evaluate decides one book. Plan and Replan both call it, so a row planned
// and a row re-planned from the same state carry the same fingerprint.
func (f *junkTitleFixer) evaluate(idx *junkIndex, b database.BookCore) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	author := f.authorName(idx, b.AuthorID)
	r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Author: author, Risk: repairs.RiskReview,
		Current: map[string]string{"title": b.Title}}
	finish := func(skip, why string) (repairs.Row, error) {
		r.Skipped, r.SkipReason = skip, why
		if r.Reason == "" {
			r.Reason = why
		}
		r.Fingerprint = junkFingerprint(r, why)
		return r, nil
	}

	narrators := narratorsOf(b.Narrator)
	kind := metadata.ClassifyJunkTitleFor(b.Title, narrators)
	if kind == metadata.JunkNone {
		return finish(junkSkipNotJunk, "the title is no longer junk")
	}
	r.Reason = fmt.Sprintf("junk title (%s)", kind)

	// The old op's owner-manual rule also read the TITLE: "Big Finish
	// Ident" is itself Big Finish content, which the path guard may not see.
	if junkTitleOwnerManual("", b.Title, "") {
		return finish(repairs.SkipOwnerManual, "the title marks Big Finish / Doctor Who / Torchwood content; owner applies these by hand")
	}

	// providerNote ends the row's reason when the junk title is recorded as
	// a provider's value; kept apart because the reason is rebuilt once a
	// proposal is chosen.
	providerNote := ""
	// fetchedTitle and fetchedAuthor are what a provider recorded for the
	// book; providerSaidIt is whether the recorded title IS the stored junk
	// one, providerUnreadable whether a value is on record but is no string.
	fetchedTitle, fetchedAuthor, providerSaidIt, providerUnreadable := "", "", false, false
	states, err := store.GetMetadataFieldStates(b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read field states of %s: %w", b.ID, err)
	}
	for i := range states {
		if states[i].Field == "author_name" {
			if v, ok := metastate.Decode(states[i].FetchedValue).(string); ok {
				fetchedAuthor = strings.TrimSpace(html.UnescapeString(v))
			}
			continue
		}
		if states[i].Field != "title" {
			continue
		}
		if states[i].HasUserOverride() {
			return finish(lockHold(&states[i], "title"))
		}
		// A provider value on the title does NOT stop the repair (owner,
		// 2026-10-03). The book only gets here because its STORED title
		// fails the junk classifier. Measured on prod that day over the
		// 1,233 rows the old skipped_provider_title rule held: in 1,216 the
		// recorded value is a DIFFERENT title ("85 - Echoes of the System…"
		// has "Echoes of the System: A LitRPG Adventure" on record), because
		// a scheduled fetch records the provider's candidate and then only
		// fills empty fields, so the junk title stayed. The stored title is
		// file-derived and the recorded value is evidence, weighed below
		// (the fetch searched on the junk title, and its hit can be another
		// book: "01 The Shadow of Gods 01-34" has "Shadowfall" on record).
		// In 11 the recorded title IS the stored one, by the letters and
		// digits ("3-10 to Yuma" is "3:10 to Yuma"): a provider may have
		// answered with it, or returned no title at all (the fetch then
		// records the book's own). Those are repaired too, never at low risk.
		if states[i].HasProviderValue() {
			// Some providers return HTML entities ("Magic Tides &amp; Magic
			// Claims"): decoded before any comparison, and before the value
			// can become the proposal.
			if v, ok := metastate.Decode(states[i].FetchedValue).(string); ok {
				fetchedTitle = strings.TrimSpace(html.UnescapeString(v))
			}
			switch {
			case fetchedTitle == "":
				providerUnreadable = true
				providerNote = "; a provider value is on record for the title but is not a readable string"
			case junkLettersKey(fetchedTitle) == junkLettersKey(b.Title):
				providerSaidIt = true
				providerNote = "; the title a metadata provider recorded for this book is this same title"
			default:
				providerNote = fmt.Sprintf("; a metadata provider recorded the title %q for this book", fetchedTitle)
			}
			r.Reason += providerNote
		}
	}

	if why := legacyStateUnreadable(store, b.ID, states); why != "" {
		return finish(junkSkipNeedsManual, why)
	}
	// A blob-only book's title lock lives in the blob, which the rows loop
	// above cannot see.
	if len(states) == 0 {
		locks, lerr := database.LoadFieldLocks(store, b.ID)
		if lerr != nil {
			return repairs.Row{}, fmt.Errorf("read field locks of %s: %w", b.ID, lerr)
		}
		if locks.Locked(database.FieldKeyTitle) {
			return finish(junkSkipUserLocked, "the title carries a user override (pre-migration state); it is never rewritten")
		}
	}

	// Title and author stored swapped: the author holds the title a provider
	// recorded. Retitling here would leave the author holding the title, so
	// the book is the swapped fixer's, which writes both. (That fixer also
	// takes a book this fixer retitled before the guard existed, author
	// only.)
	if ak := junkLettersKey(author); ak != "" && fetchedTitle != "" &&
		(ak == junkLettersKey(fetchedTitle) || ak == junkLettersKey(catalogEditionRe.ReplaceAllString(fetchedTitle, ""))) {
		return finish(junkSkipSwapped, fmt.Sprintf(
			"the author %q is the title a provider recorded (%q): title and author are swapped — use %s, which fixes both",
			author, fetchedTitle, swappedFixerID))
	}

	files, err := store.GetBookFiles(b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read files of %s: %w", b.ID, err)
	}
	var paths []string
	transcribed := ""
	if b.TranscribedTitle != nil {
		transcribed = strings.TrimSpace(*b.TranscribedTitle)
	}
	for i := range files {
		if p := strings.TrimSpace(files[i].FilePath); p != "" {
			paths = append(paths, p)
		}
		if transcribed == "" && files[i].TranscribedTitle != nil {
			transcribed = strings.TrimSpace(*files[i].TranscribedTitle)
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 && b.FilePath != "" {
		paths = []string{b.FilePath}
	}
	runtimeSec, runtimeKnown := bookRuntimeSec(b, files)

	// ---- fragments: a chapter file of a bigger book is not retitled ----
	skip, why, err := f.fragmentReason(idx, b, kind, author, paths)
	if err != nil {
		return repairs.Row{}, err
	}
	switch skip {
	case junkSkipFragment:
		return finish(skip, "fragment — use the consolidation fixer: "+why)
	case junkSkipPossibleFragment:
		return finish(skip, "possible fragment: "+why)
	case junkSkipNeedsManual:
		return finish(skip, why)
	}

	// ---- proposals, in priority order ----
	people := []string{author}
	people = append(people, narrators...)
	links, err := store.GetBookAuthors(b.ID)
	if err != nil {
		// Fail closed: without the linked names the person check is incomplete.
		return repairs.Row{}, fmt.Errorf("read linked authors of %s: %w", b.ID, err)
	}
	for _, l := range links {
		id := l.AuthorID
		people = append(people, f.authorName(idx, &id))
	}
	// What a filename's " - <name>" tail may be stripped as (derive.go): the
	// book's own people, minus a stored author that is junk (a chapter label
	// in the author field is title text in the file name), and any name of the
	// library that no live book holds as its title, which only counts behind
	// a "read by" credit. The own people go with no credit in the stem only
	// when they have evidence beyond the stem: an author with other books, or
	// a narrator.
	var ownPeople []string
	for _, p := range people {
		if !junkStoredAuthor(p) {
			ownPeople = append(ownPeople, p)
		}
	}
	libraryAuthor := func(s string) bool { return idx.isLibraryAuthor(s, b.ID) }
	rec := authorRecognisers{
		own:         func(s string) bool { return titleNamesAPerson(s, ownPeople) },
		independent: func(s string) bool { return libraryAuthor(s) || titleNamesAPerson(s, narrators) },
		library:     libraryAuthor,
	}
	pc := junkProposalCheck{stored: b.Title, narrators: narrators, people: people, roots: idx.roots,
		authorRoot: idx.authorRoot, paths: paths, libraryAuthor: libraryAuthor}
	var refused []string
	ownerManual := ""
	accept := func(cand, source string) (string, bool) {
		c := strings.TrimSpace(cand)
		if c != "" && junkTitleOwnerManual("", c, "") {
			// The proposal itself says Doctor Who / Big Finish / Torchwood:
			// the whole book is the owner's, not only this proposal.
			ownerManual = c
			return "", false
		}
		if why := pc.refusal(c, source); why != "" {
			if why != "-" {
				refused = append(refused, why)
			}
			return "", false
		}
		return c, true
	}

	type proposal struct{ title, source, risk string }
	var props []proposal
	// An embedded kind still carries (or sits in a folder that names) the
	// real title; the cached candidate search ran on the junk title and its
	// top hit is often the series' first book ("01 - Eldest" → "Eragon"), so
	// for an embedded kind a candidate must agree with that evidence to count
	// at all.
	embedded := kind.HasRealTitleInside() || kind.IsChapterKind()
	var agreeWith []string
	stripped := ""
	if kind.HasRealTitleInside() {
		if t, ok := metadata.StripJunkTitlePrefix(b.Title); ok {
			agreeWith = append(agreeWith, t)
			stripped, _ = accept(t, junkSrcStripped)
		}
	}
	folderTitle, folderSrc, titleDir := "", "", ""
	if kind.IsChapterKind() && len(paths) > 0 {
		// "" is chapter-only to ChapterTitleFromDirectory, which then reads
		// the folder whatever the stored chapter spelling ("Part IV").
		if t, dir, ok := metadata.ChapterTitleFromDirectory(paths[0], ""); ok {
			folderTitle, folderSrc, titleDir = t, junkSrcFolder, dir
		}
	} else if t, method, ok := deriveJunkTitleReplacement(b.Title, author, paths, rec); ok {
		folderTitle, folderSrc = t, junkSrcFolder
		if method == "filename" {
			folderSrc = junkSrcFilename
			// The stem before any author segment was stripped from it: a
			// chapter, track or format token there is a marker of the file
			// even when the strip took the segment that carried it.
			pc.filenameStem, _ = filenameHead(filepath.Base(paths[0]))
		}
	}
	// A narrator credit kept in the folder or file name ("Harbor Lights (read
	// by Some Narrator)") is not part of the title: dropped, so the title it
	// leaves is judged on its own. One that still carries a chapter, track or
	// credit token after that is refused (junkProposalCheck.refusal).
	seriesName := ""
	if b.SeriesID != nil {
		seriesName = strings.TrimSpace(idx.series[*b.SeriesID])
	}
	// liveTitle: a live book holds s as its title (stripRecordedSeries leaves
	// a series that ends a real title alone).
	liveTitle := func(s string) bool { return len(idx.titleAuthors[util.TitleSortKey(s)]) > 0 }
	if folderSrc != "" {
		folderTitle = stripReadByCredit(folderTitle)
		// The book's own series, named in the folder or file ("Ember Wars,
		// Book 3, Harbor Lights"), is not part of the title.
		folderTitle = stripRecordedSeries(folderTitle, seriesName, liveTitle)
	}
	// A folder named like the book's series ("The Stormlight Archive/") says
	// which series, not which book: it is neither evidence nor a proposal.
	if folderSrc == junkSrcFolder && b.SeriesID != nil && idx.series[*b.SeriesID] != "" &&
		strings.EqualFold(strings.TrimSpace(folderTitle), strings.TrimSpace(idx.series[*b.SeriesID])) {
		folderTitle, folderSrc, titleDir = "", "", ""
	}
	if folderSrc == junkSrcFolder {
		agreeWith = append(agreeWith, folderTitle)
	}
	if len(paths) > 0 {
		agreeWith = append(agreeWith, filepath.Base(junkWorkDir(paths[0], b.Title)))
	}
	folder := ""
	if folderSrc != "" {
		folder, _ = accept(folderTitle, folderSrc)
	}
	// weakFolder: a filename stem that looks like a rip's file name
	// ("hp1", "dune_messiah_64kbps", "final", "audible_download_2019"), not a
	// title. It never outranks a transcription or candidate.
	weakFolder := folder != "" && folderSrc == junkSrcFilename && isWeakFileStem(folder)

	// The transcription window reads on into the publisher's series line:
	// "Orchard Keep, Book Nine in Ember Wars, a Tinker Spindle saga".
	// The tail is dropped before the title is judged, compared or proposed.
	// The book's own recorded series, spoken before or after the title
	// ("Ember Wars Book 3 Harbor Lights"), goes too (stripRecordedSeries).
	spokenCore := stripSeriesTail(transcribed)
	if spokenCore != "" {
		spokenCore = stripRecordedSeries(spokenCore, seriesName, liveTitle)
	}
	if transcribed != "" && spokenCore == "" {
		refused = append(refused, fmt.Sprintf("transcription %q is only a series or book marker", transcribed))
	}
	spoken, _ := accept(spokenCore, junkSrcTranscribed)
	candidate, refusedCandidate := "", ""
	// candidateTitle gates each candidate through CandidateIdentityStale,
	// which reads the book's ID and ASIN; ToBook carries both.
	full := b.ToBook()
	t, score, ok, candErr := f.candidateTitle(&full, author)
	if candErr != nil {
		// Fail closed: with the owner's rejections unreadable, any cached
		// candidate may be one the owner turned down for this book.
		return repairs.Row{}, fmt.Errorf("candidate title of %s: %w", b.ID, candErr)
	}
	if ok {
		switch {
		case embedded && !junkEvidenceAgrees(t, agreeWith):
			refusedCandidate = t
			refused = append(refused, fmt.Sprintf("candidate %q (score %.2f) disagrees with the title's own evidence", t, score))
		default:
			candidate, _ = accept(t, junkSrcCandidate)
		}
	}

	// The provider's recorded title is held to the cached candidate's
	// standard. For an embedded kind it must agree with the title's own
	// evidence. Otherwise nothing in the title vouches for it, so it must
	// have been recorded under the book's own author, as candidateTitle
	// requires of a candidate (the fetch's title-only fallback searches with
	// no author at all). No score is on record, so that gate cannot be kept.
	provided, providerRefusal, providerSkip, providerEchoesPath := "", "", "", ""
	if fetchedTitle != "" && !providerSaidIt {
		a := strings.TrimSpace(author)
		// A catalog title carries its edition in brackets ("Dune
		// (Unabridged)"): the marker is dropped, the title kept. The
		// folder rule's formatWordRe is not used here, it would refuse
		// real catalog titles ("Magnum Opus", "The Abridged History of…").
		catalog := strings.TrimSpace(catalogEditionRe.ReplaceAllString(fetchedTitle, ""))
		switch {
		case embedded && !junkEvidenceAgrees(catalog, agreeWith):
			providerRefusal = fmt.Sprintf("provider value %q disagrees with the title's own evidence", fetchedTitle)
		case !embedded && junkStoredAuthor(a):
			// The stored author is junk (empty, a placeholder, a credit line),
			// so the recorded author has nothing to be compared with and the
			// author gate below would refuse a correct provider title (2026-10-23
			// review). The title must instead agree with the path or the
			// transcription, as an embedded kind's must: the folder, the work
			// folder, and the filename's title (a single-file book's folder is
			// often the poisoned one, so its filename is the only path
			// evidence there, a weak stem excepted). When it does not, the row
			// is skipped: falling back to the folder or filename here picked
			// the very evidence the provider contradicted.
			corroboration := slices.Clone(agreeWith)
			if folder != "" && folderSrc == junkSrcFilename && !weakFolder {
				corroboration = append(corroboration, folder)
			}
			if spoken != "" {
				corroboration = append(corroboration, spoken)
			}
			if junkEvidenceAgrees(catalog, corroboration) {
				provided, _ = accept(catalog, junkSrcProvider)
				// The provider's title is the path's own with other
				// punctuation ("X: Volume II" for "X - Volume II"): it adds
				// nothing the path does not say, and the path's spelling is
				// the one the file carries. Not proposed from the provider.
				if provided != "" && folder != "" && !weakFolder && junkLettersKey(provided) == junkLettersKey(folder) {
					providerEchoesPath = provided
					provided = ""
				}
			} else {
				providerRefusal = fmt.Sprintf("provider value %q does not agree with the path or the transcription, and the stored author is junk, so there is no author to vouch for it", fetchedTitle)
				providerSkip = providerRefusal
			}
		case !embedded && !strings.EqualFold(util.NormalizeAuthor(fetchedAuthor), util.NormalizeAuthor(a)):
			providerRefusal = fmt.Sprintf("provider value %q was not recorded under this book's author (recorded %q)", fetchedTitle, fetchedAuthor)
		default:
			provided, _ = accept(catalog, junkSrcProvider)
		}
		if providerRefusal != "" {
			refused = append(refused, providerRefusal)
		}
	}

	// A catalog title (candidate or provider) that is the title the book's own
	// evidence names plus a marketing tagline subtitle (": A Windswept Harbor
	// Romance") does not outrank that evidence: the owner turned such taglines
	// down 16 times among the transcriptions, and the catalog title reaches the
	// proposal list before the transcription does. It stays in the conflict
	// check below (it still says which book this is) and is not proposed;
	// what the evidence names is. A tagline title nothing else names stays
	// the proposal: a transcription that misheard the title cannot be told
	// from the tagline's other half.
	taglineDropped := ""
	strongFolder := ""
	if folder != "" && !weakFolder {
		strongFolder = folder
	}
	echoesCore := func(t string) bool {
		core := stripCatalogTagline(t)
		if t == "" || core == t || junkLettersKey(core) == "" {
			return false
		}
		for _, ev := range []string{stripped, spoken, strongFolder} {
			if ev != "" && junkLettersKey(ev) == junkLettersKey(core) {
				return true
			}
		}
		return false
	}
	candidateProposed, providedProposed := candidate != "", provided != ""
	if echoesCore(candidate) {
		candidateProposed, taglineDropped = false, candidate
	}
	if echoesCore(provided) {
		providedProposed, taglineDropped = false, provided
	}

	// An owner-manual proposal decides the book before any conflict does.
	if ownerManual != "" {
		return finish(repairs.SkipOwnerManual, fmt.Sprintf(
			"the proposed title %q marks Big Finish / Doctor Who / Torchwood content; owner applies these by hand", ownerManual))
	}

	// A provider title the junk author could not vouch for, and nothing else
	// agrees with: no fallback to the folder or filename (see above).
	if providerSkip != "" {
		return finish(junkSkipNeedsManual, providerSkip)
	}

	// Path evidence is what the book's own path says its title is: the
	// stripped title, the folder, a filename stem that reads like a title.
	// When it and a transcription, candidate or provider value disagree, neither is
	// proposed: each side has a known way to be wrong ("New Folder (2)",
	// "Libation" vs "Audible Studios presents", a series' first book), and
	// picking one would write the other's mistake half the time.
	type evidence struct{ source, title string }
	var pathEv []evidence
	outvoted := "" // a transcription the path and the provider's title overruled
	if stripped != "" {
		pathEv = append(pathEv, evidence{junkSrcStripped, stripped})
	}
	if folder != "" && !weakFolder {
		pathEv = append(pathEv, evidence{folderSrc, folder})
	} else if weakFolder {
		// A weak stem is never proposed, but one that still names something
		// ("The_Final_Empire", "It", "1Q84") vetoes evidence that disagrees
		// with it. A stem of rip tokens only ("hp1", "final",
		// "audible_download_2019") says nothing and vetoes nothing.
		if clean, ok := weakStemVeto(folder); ok {
			pathEv = append(pathEv, evidence{folderSrc, clean})
		}
	}
	if len(pathEv) > 0 {
		pathTitles := make([]string, len(pathEv))
		for i := range pathEv {
			pathTitles[i] = pathEv[i].title
		}
		// The one exception: a transcription that disagrees with the path is
		// overruled when the provider recorded EXACTLY the path's title (same
		// letters and digits). The provider value is not an independent
		// identification of the file: the fetch searched on the path-derived
		// title, so its answer is an echo of that query. What the echo does
		// prove is that a catalog work with that spelling exists, which is the
		// evidence that separates a real title from a transcription that
		// misheard it ("Hildiggers" for "Hilldiggers"), appended the series
		// ("Polity Agent An Agent Cormac novel") or caught a fragment ("ated").
		// 32 number-leading books on prod sat in needs-manual for exactly this,
		// 2026-10-03. The proposal is the one the fixer would have made with no
		// transcription at all; what is given up is the warning, so the row is
		// never low risk and the reason names what was overruled.
		//
		// Exact, not titleAgreesWithAny: its "plus a subtitle" form is the shape
		// of the first-book-of-the-series wrong hit (folder "Mistborn", provider
		// "Mistborn: The Final Empire", transcription "The Well of Ascension"),
		// where the transcription is the only source that has the book right.
		//
		// And not when the transcription has support of its own: a refused
		// candidate that agrees with it, or a book this author already has under
		// that very title. Then it is not a mishearing, it says the file may be
		// mislabelled, and the conflict stands.
		providerBacksPath := false
		if provided != "" {
			pk := junkLettersKey(provided)
			for _, pt := range pathTitles {
				if pk != "" && pk == junkLettersKey(pt) {
					providerBacksPath = true
				}
			}
		}
		spokenHasSupport := spoken != "" &&
			((refusedCandidate != "" && junkEvidenceAgrees(refusedCandidate, []string{spoken})) ||
				idx.titles[junkTitleKey(b.AuthorID, spoken)])
		for _, other := range []evidence{{junkSrcTranscribed, spoken}, {junkSrcCandidate, candidate}, {junkSrcProvider, provided}} {
			// The transcription is compared as heard: its "Book 2 of …" tail is
			// what tells another book of the series from this one. And a comma
			// opens a subtitle here (junkTranscriptionSeps): the window reads
			// on past the title into whatever follows, and a title the path
			// names exactly, read on, is still that title unless a book number
			// differs.
			probe, seps := other.title, junkSubtitleSeps
			if other.source == junkSrcTranscribed {
				// The series tail ("Book 2 of …") stays; the book's own recorded
				// series ("Ember Wars, Book 3, <title>") does not: it is no
				// other book of the series, and left in it turned a correct
				// path into a conflict.
				probe, seps = stripRecordedSeries(transcribed, seriesName, liveTitle), junkTranscriptionSeps
			}
			if other.title == "" || junkEvidenceAgreesSeps(probe, pathTitles, seps) {
				continue
			}
			if other.source == junkSrcTranscribed && providerBacksPath && !spokenHasSupport {
				outvoted, spoken = spoken, ""
				continue
			}
			var sides []string
			for _, e := range pathEv {
				sides = append(sides, fmt.Sprintf("%s %q", e.source, e.title))
			}
			why := fmt.Sprintf("conflicting evidence: %s vs %s %q", strings.Join(sides, ", "), other.source, other.title)
			if outvoted != "" {
				why += fmt.Sprintf(" (and %s %q)", junkSrcTranscribed, outvoted)
			}
			if other.source == junkSrcTranscribed && refusedCandidate != "" && junkEvidenceAgrees(refusedCandidate, []string{other.title}) {
				why += fmt.Sprintf(", which the candidate %q agrees with", refusedCandidate)
			}
			return finish(junkSkipNeedsManual, why)
		}
	}

	// Priority: the title without its prefix, a matching candidate, a title a
	// provider recorded, and only then the transcription. 261 of the 362
	// transcription rows of the 2026-10-23 plan were wrong, among them rows
	// whose stripped and provider titles were right and lost to it.
	if stripped != "" {
		props = append(props, proposal{stripped, junkSrcStripped, repairs.RiskLow})
	}
	if candidateProposed {
		props = append(props, proposal{candidate, junkSrcCandidate, repairs.RiskReview})
	}
	if providedProposed {
		props = append(props, proposal{provided, junkSrcProvider, repairs.RiskReview})
	}
	if spoken != "" {
		props = append(props, proposal{spoken, junkSrcTranscribed, repairs.RiskReview})
	}
	if folder != "" {
		// A folder reached by climbing past a chapter folder that sits right
		// under a library or import root is as likely the AUTHOR folder
		// ("/imports/dl/Robin Hobb/01/a.mp3"): only a transcription or
		// candidate naming the same work makes it a title.
		climbed := titleDir != "" && titleDir != filepath.Dir(paths[0])
		if climbed && slices.Contains(idx.roots, filepath.Dir(titleDir)) {
			corroborated := false
			for _, c := range []string{spoken, candidate, provided} {
				if c != "" && junkEvidenceAgrees(c, []string{folder}) {
					corroborated = true
				}
			}
			if !corroborated {
				return finish(junkSkipNeedsManual, fmt.Sprintf(
					"the folder %q sits directly under the root %s above a chapter folder and may be the author; "+
						"no transcription, candidate or provider value corroborates it", folder, filepath.Dir(titleDir)))
			}
		}
		// A weak filename stem ("hp1", "final") is never a title, not even
		// as a last resort.
		if !weakFolder {
			props = append(props, proposal{folder, folderSrc, repairs.RiskReview})
		}
	}
	if kind.IsChapterKind() && b.SeriesID != nil && idx.series[*b.SeriesID] != "" {
		if why := seriesNumberRefusal(idx.series[*b.SeriesID], b, narrators, agreeWith); why != "" {
			refused = append(refused, why)
		} else if m := trailingNumberRe.FindStringSubmatch(b.Title); m != nil {
			// A digits-only match cannot fail to parse except by overflow,
			// which is no book number: no proposal then.
			if n, perr := strconv.Atoi(m[1]); perr == nil && n == *b.SeriesSequence {
				if t, ok := accept(fmt.Sprintf("%s, Book %d", idx.series[*b.SeriesID], n), junkSrcSeriesNum); ok {
					props = append(props, proposal{t, junkSrcSeriesNum, repairs.RiskReview})
				}
			} else if perr == nil {
				refused = append(refused, fmt.Sprintf(
					"the number %d in the stored title is not the book's series position %d", n, *b.SeriesSequence))
			}
		}
	}
	if ownerManual != "" {
		return finish(repairs.SkipOwnerManual, fmt.Sprintf(
			"the proposed title %q marks Big Finish / Doctor Who / Torchwood content; owner applies these by hand", ownerManual))
	}
	if len(props) == 0 && weakFolder {
		return finish(junkSkipNeedsManual, "only a weak filename stem: "+folder)
	}
	if len(props) == 0 {
		why := "no trustworthy replacement title (no transcription, matching candidate, usable provider value, folder or series)"
		if len(refused) > 0 {
			why += "; refused: " + strings.Join(refused, ", ")
		}
		return finish(junkSkipNeedsManual, why)
	}
	best := props[0]
	// A title taken from outside the stored title (a transcription, a folder, a
	// filename, the series) names the whole work. A book far too short to be
	// one, or one the scanner flagged suspicious, is a piece of it. A stripped
	// prefix is the file's own title and holds either way.
	if best.source != junkSrcStripped {
		if skip, why := f.fragmentEvidenceReason(b, runtimeSec, runtimeKnown); skip != "" {
			if skip == junkSkipPossibleFragment {
				why = "possible fragment: " + why
			}
			return finish(skip, why)
		}
	}
	// The author a provider recorded names this book's owner only when the
	// provider's title was not refused: a wrong-book title (the fetch's
	// title-only fallback) comes with another person's name, and the duplicate
	// guard would then look for the title under the wrong owner. Otherwise the
	// owner is the author folder's, or unknown.
	ownerFetchedAuthor := fetchedAuthor
	if fetchedTitle != "" && !providerSaidIt && provided == "" && providerEchoesPath == "" {
		ownerFetchedAuthor = ""
	}
	owner := f.proposalOwner(idx, b, author, ownerFetchedAuthor, paths)
	if f.titleTaken(idx, owner, best.title) {
		who := "this author already has"
		if owner.junk {
			who = "a book by " + owner.describe() + " already has"
		}
		return finish(junkSkipPossibleFragment, fmt.Sprintf(
			"possible fragment: %s a book titled %q; this one may be part of it", who, best.title))
	}
	r.Proposed = map[string]string{"title": best.title}
	r.Risk = best.risk
	if providerSaidIt || providerUnreadable || providerRefusal != "" {
		// The recorded title is this very title (it may be a real one the
		// classifier misreads: "3-10 to Yuma"), cannot be read, or names
		// something else: a provider on record against the proposal is a
		// reason for a person to look. Never a low-risk row.
		r.Risk = repairs.RiskReview
	}
	var also []string
	for _, p := range props[1:] {
		if p.title != best.title {
			also = append(also, fmt.Sprintf("%s: %q", p.source, p.title))
		}
	}
	r.Reason = fmt.Sprintf("junk title (%s); proposed from %s", kind, best.source)
	if len(also) > 0 {
		r.Reason += "; other evidence: " + strings.Join(also, ", ")
	}
	r.Reason += providerNote
	if providerRefusal != "" {
		r.Reason += "; not used: " + providerRefusal
	}
	if outvoted != "" {
		r.Risk = repairs.RiskReview
		r.Reason += fmt.Sprintf("; the transcription %q disagrees and was overruled: a provider recorded exactly the path's title", outvoted)
	}
	if taglineDropped != "" {
		r.Reason += fmt.Sprintf("; the catalog title %q is this title plus a tagline subtitle and was not used", taglineDropped)
	}
	if providerEchoesPath != "" {
		r.Reason += fmt.Sprintf("; the provider's title %q is this title with other punctuation, so the path's spelling was kept", providerEchoesPath)
	}
	if best.source == junkSrcTranscribed && spokenCore != strings.TrimSpace(transcribed) {
		r.Reason += fmt.Sprintf("; the series or book tail of the transcription %q was dropped", transcribed)
	}
	r.Detail = &junkDecision{bookID: b.ID, oldTitle: b.Title, newTitle: best.title, owner: owner}
	// The provider's recorded title is an input: one that appears or changes
	// between plan and apply is a different decision.
	// So is a transcription that was overruled: one that changes may no longer
	// be overruled, or may now agree.
	r.Fingerprint = junkFingerprint(r, string(kind)+"|"+best.source+"|"+fetchedTitle+"|"+outvoted)
	return r, nil
}

// titleAgreesWithAny reports whether a candidate title names the same work
// as any of the evidence titles: equal sort keys, or one is the other plus a
// subtitle after ":", " (", " - " or "," ("Eldest" vs "Eldest: Inheritance,
// Book 2"). A longer title that merely starts with the word ("Eldest Son")
// is a different work. Apostrophes are ignored: a folder cannot always hold
// one ("Assassins Apprentice" is "Assassin's Apprentice").
func titleAgreesWithAny(cand string, evidence []string) bool {
	c := util.TitleSortKey(dropApostrophes(cand))
	if c == "" {
		return false
	}
	lc := strings.ToLower(strings.TrimSpace(dropApostrophes(cand)))
	for _, e := range evidence {
		k := util.TitleSortKey(dropApostrophes(e))
		if k == "" {
			continue
		}
		le := strings.ToLower(strings.TrimSpace(dropApostrophes(e)))
		if c == k || hasSubtitle(lc, le) || hasSubtitle(le, lc) {
			return true
		}
	}
	return false
}

var apostropheReplacer = strings.NewReplacer("'", "", "\u2019", "", "\u2018", "", "`", "")

func dropApostrophes(s string) string { return apostropheReplacer.Replace(s) }

// weakStemTokenRe: a filename token that marks a rip, not a title: a
// bitrate, a download/final/merged/output marker.
var weakStemTokenRe = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:\d+\s*kbps|kbps|bitrate|download|downloaded|final|merged|output)(?:$|[^a-z])`)

// isWeakFileStem reports whether a filename stem reads like a file name
// rather than a title: short (3 characters or fewer), underscore-joined, a
// word mixing digits and letters ("hp1", "64kbps"), or a rip marker.
func isWeakFileStem(stem string) bool {
	s := strings.TrimSpace(stem)
	if len([]rune(s)) <= 3 || strings.Contains(s, "_") || weakStemTokenRe.MatchString(s) {
		return true
	}
	for w := range strings.FieldsSeq(s) {
		hasDigit := strings.IndexFunc(w, unicode.IsDigit) >= 0
		hasLetter := strings.IndexFunc(w, unicode.IsLetter) >= 0
		if hasDigit && hasLetter {
			return true
		}
	}
	return false
}

// ripTokens are words of a rip's file name that name no work.
var ripTokens = map[string]bool{"kbps": true, "bitrate": true, "download": true, "downloaded": true,
	"final": true, "merged": true, "output": true, "audible": true, "audiobook": true, "mp3": true,
	"m4b": true, "m4a": true, "unabridged": true, "abridged": true, "part": true, "disc": true, "track": true}

var kbpsTokenRe = regexp.MustCompile(`(?i)^\d+kbps$`)

// weakStemVeto decides whether a weak filename stem still names a work, and
// returns it cleaned for the conflict check: underscores, dots and hyphens
// become spaces ("The.Final.Empire", "dune-messiah-64kbps") and rip tokens are trimmed off both ends ("dune_messiah_64kbps" → "dune
// messiah"; a bare year at an end goes too while another word remains).
// Tokens inside the stem stay, so "The_Final_Empire" keeps its "Final".
// It vetoes when the cleaned stem keeps a word of three or more letters
// that is no rip token, or when it is one short word that reads like a
// title rather than an abbreviation: a vowel ("It") or a leading digit
// with a letter ("1Q84"), not "hp1" or "zz". A stem of rip tokens only
// ("final", "audible_download_2019") vetoes nothing.
func weakStemVeto(stem string) (string, bool) {
	words := strings.FieldsFunc(stem, func(r rune) bool {
		return unicode.IsSpace(r) || r == '_' || r == '.' || r == '-'
	})
	noise := func(w string, others bool) bool {
		lw := strings.ToLower(w)
		if ripTokens[lw] || kbpsTokenRe.MatchString(lw) {
			return true
		}
		return others && strings.IndexFunc(w, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
	}
	for len(words) > 0 && noise(words[0], len(words) > 1) {
		words = words[1:]
	}
	for len(words) > 0 && noise(words[len(words)-1], len(words) > 1) {
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		return "", false
	}
	clean := strings.Join(words, " ")
	for _, w := range words {
		if ripTokens[strings.ToLower(w)] {
			continue
		}
		letters := 0
		for _, r := range w {
			if unicode.IsLetter(r) {
				letters++
			}
		}
		if letters >= 3 && letters == len([]rune(w)) {
			return clean, true
		}
	}
	if len(words) == 1 && len([]rune(clean)) <= 4 {
		first := []rune(clean)[0]
		hasLetter := strings.IndexFunc(clean, unicode.IsLetter) >= 0
		if strings.ContainsAny(strings.ToLower(clean), "aeiou") || (unicode.IsDigit(first) && hasLetter) {
			return clean, true
		}
	}
	return "", false
}

// hasSubtitle: s is p followed by a subtitle separator.
func hasSubtitle(s, p string) bool {
	return hasSubtitleSeps(s, p, []string{":", " (", " - ", ","})
}

// junkProposalCheck refuses a proposed title that is not a title.
type junkProposalCheck struct {
	stored     string
	narrators  []string
	people     []string
	roots      []string
	authorRoot string
	paths      []string
	// libraryAuthor recognises the name of an author of the library that is
	// not also a live title (junkIndex.isLibraryAuthor); nil recognises none.
	libraryAuthor func(string) bool
	// filenameStem is the file name's stem before an author segment was
	// stripped from it ("" when the title is not from a filename). The
	// markers are looked for in it too: what the strip took may be the part
	// that carried the marker.
	filenameStem string
}

var (
	// formatWordRe: a container, format or edition word, a side/tape/disc
	// position or range. A folder or filename carrying one names a rip, not
	// a work ("Dune (Unabridged)", "Dune MP3", "Side A", "Tape 3", "CD01-02").
	// chapterHeadingRe: a chapter/part heading with a number or spelled
	// number, or a bare prologue/epilogue heading followed by more words
	// ("Prologue Bobbie Draper"). "Prologue to Murder" is a title: the word
	// must be followed by a capitalised name or a dash, not a lowercase word.
	chapterHeadingRe = regexp.MustCompile(`(?i)^(?:chapter|chap|ch|part|pt)\.?[\s_\-]*(?:\d+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)(?:[^\pL\d]|$)`)
	// prologueHeadingRe is case-sensitive on purpose: the capital after the
	// word is what separates "Prologue Bobbie Draper" from "Prologue to Murder".
	prologueHeadingRe = regexp.MustCompile(`^(?:[Pp]rologue|[Ee]pilogue|[Ii]ntroduction)(?:\s*[-–:.]\s*\S|\s+\p{Lu})`)
	// romanChapterHeadingRe: "CHAPTER XIII …", "Part III <name>": a heading
	// with a roman numeral. Case-sensitive, and a lone I, V or X only before
	// punctuation or the end ("Part I Am Salt" is a title).
	romanChapterHeadingRe = regexp.MustCompile(`^(?:[Cc]hapter|CHAPTER|[Cc]hap|[Pp]art|PART|[Pp]t)\.?[\s_\-]*(?:[IVXLC]{2,}(?:[^\pL\d]|$)|[IVXLC](?:$|[.:,;)\-–]))`)
	// publisherIdentRe: the ident a publisher puts before the title.
	publisherIdentRe = regexp.MustCompile(`(?i)^(?:this is audible|audible (?:presents|studios)|(?:from\s+)?(?:tantor|blackstone|brilliance|podium|hachette|recorded books|simon and schuster|simon & schuster|penguin|random house|harper\s*(?:audio|collins)?|macmillan|graphic\s*audio|full cast audio|books on tape|listening library|audible|recorded books)\b.*\b(?:presents?|resents|production|audio\b.*,)|.*\ba division of\b)`)
	formatWordRe     = regexp.MustCompile(`(?i)\b(?:mp3|m4b|m4a|aac|flac|ogg|opus|wma|unabridged|abridged|side\s+[a-d0-9]|tape\s*\d+|(?:cd|disc|disk)\s*\d+(?:\s*-\s*\d+)?)\b`)
	// idCodeRe: an ASIN (B0 + 8), an ISBN-10/13, or a trailing bracketed
	// 10-character id ("Dune [B002V1OF70]").
	idCodeRe = regexp.MustCompile(`(?i)\bB0[0-9A-Z]{8}\b|\b(?:97[89][- ]?)?\d{9}[\dX]\b|\[[0-9A-Z]{10}\]\s*$`)
	// genericStems are tool-output stems that name no work.
	genericStems = map[string]bool{"merged": true, "output": true, "book": true, "audio": true, "audiobook": true,
		"track": true, "untitled": true, "new folder": true}
)

// refusal returns why c is refused as a proposal from source, "-" for a
// refusal not worth listing (empty, same as stored), or "" to accept it.
func (pc junkProposalCheck) refusal(c, source string) string {
	switch {
	case len([]rune(c)) < 2, strings.EqualFold(c, strings.TrimSpace(pc.stored)):
		return "-"
	case metadata.ClassifyJunkTitleFor(c, pc.narrators) != metadata.JunkNone:
		return fmt.Sprintf("%q is junk itself", c)
	case metadata.IsGenericDirName(c), genericStems[strings.ToLower(c)]:
		// A generic folder ("Books", "Audiobooks", a library root) is what
		// the one-level climb in deriveJunkTitleReplacement lands on above a
		// junk-named folder; it is never a title.
		return fmt.Sprintf("%q is a generic name", c)
	case titleNamesAPerson(c, pc.people) || pc.namesAPersonNormalized(c):
		return fmt.Sprintf("%q names the author or narrator", c)
	case pc.isPathRoot(c):
		return fmt.Sprintf("%q is a library or path root", c)
	case chapterHeadingRe.MatchString(c), prologueHeadingRe.MatchString(c), romanChapterHeadingRe.MatchString(c):
		// "Prologue Marta Quill", "Chapter Two - The Lamplighter", "CHAPTER XIII
		// The Tide": a chapter heading, whatever stripped or spoke it.
		return fmt.Sprintf("%q is a chapter heading", c)
	case bareSectionRe.MatchString(c):
		// "Prologue", "Preface": the name of a section, not of the book.
		return fmt.Sprintf("%q is the name of a section of a book, not a title", c)
	}
	if source == junkSrcTranscribed {
		// The intro transcription is the first thing said, which is often
		// the publisher's ident or the opening sentence of the text, not the
		// title. Measured on prod 2026-10-03: the plan proposed "Chapter 26
		// The apartment was in Asimov, a city at" and "Tantor audio
		// presents, The Legend of Coronair" as book titles.
		//
		// And the announcer, the format word and the sentence: "an unabridged
		// recording of X", "<studio> presents, X", "You're listening to X",
		// "And now, part five of twelve", "Side four, X", "He turned toward
		// the door." The 2026-10-23 review found each of them proposed as a
		// title, 261 wrong rows of 362.
		switch {
		case publisherIdentRe.MatchString(c):
			return fmt.Sprintf("%q is a publisher ident, not the title", c)
		case announcerRe.MatchString(c):
			return fmt.Sprintf("%q is an announcer phrase, not the title", c)
		case formatWordRe.MatchString(c):
			return fmt.Sprintf("%q carries a format, edition or disc marker", c)
		case transcribedProse(c), transcribedSentence(c):
			return fmt.Sprintf("%q reads like transcribed prose, not a title", c)
		}
	}
	if source == junkSrcFolder || source == junkSrcFilename {
		// The markers are looked for in the title and, for a filename, in the
		// stem before its author segment was stripped.
		stems := []string{c}
		if source == junkSrcFilename && pc.filenameStem != "" && pc.filenameStem != c {
			stems = append(stems, pc.filenameStem)
		}
		for i, st := range stems {
			if i > 0 {
				// A placeholder author segment is stripped on purpose, so it
				// is not a marker of the stem; a chapter label that was
				// stripped as "the author" still is.
				st = unknownPlaceholderRe.ReplaceAllString(st, "")
			}
			switch {
			case formatWordRe.MatchString(st):
				return fmt.Sprintf("%q carries a format, edition or disc marker", c)
			case idCodeRe.MatchString(st):
				return fmt.Sprintf("%q carries an ASIN or ISBN", c)
			case trackTokenRe.MatchString(st), romanTrackRe.MatchString(st):
				// Anywhere in the title, not only at the start (chapterHeadingRe):
				// "41.Chapter 9 - 12) Tides - read by narrator".
				return fmt.Sprintf("%q carries a chapter, track, credit or placeholder marker", c)
			}
		}
		isPerson := func(n string) bool {
			return pc.namesAPersonNormalized(n) || (pc.libraryAuthor != nil && pc.libraryAuthor(n))
		}
		// The whole title, or its head before a " - ", is a person's name: an
		// "Author - …" or "Last, First - …" file or folder, not a title. A head
		// that is only a library author's name is a person when it is shaped
		// like one: the author table also holds series and title text (the
		// transposed title/author rows), and the head of "Series - Volume II"
		// is the series.
		if pc.libraryAuthor != nil && pc.libraryAuthor(c) {
			return fmt.Sprintf("%q names an author of the library", c)
		}
		if head, _, ok := strings.Cut(c, " - "); ok && (pc.namesAPersonNormalized(head) ||
			(pc.libraryAuthor != nil && looksLikePersonSegment(head) && pc.libraryAuthor(head))) {
			return fmt.Sprintf("%q starts with a person's name (an \"Author - …\" filename)", c)
		}
		if seg, ok := endsWithAuthorCredit(c, isPerson); ok {
			return fmt.Sprintf("%q ends with an author credit (%q)", c, seg)
		}
	}
	return ""
}

// transcribedProse reports whether a transcription reads like the opening
// sentence(s) of the text rather than a spoken title: more than thirteen words,
// or a sentence break followed by more words in a string of more than six
// words ("Harbor lamps burned low. The wall be"). A title with
// a subtitle after a period ("Harbor Lights, the Salt Mage. Book 3. Ashes…")
// is refused too and left for a person: the cost of a wrong title is higher
// than the cost of a skipped row.
func transcribedProse(c string) bool {
	words := len(strings.Fields(c))
	if words > 13 {
		return true
	}
	if !sentenceBreakRe.MatchString(c) {
		return false
	}
	if words > 6 {
		return true
	}
	// "Harbor Lights, Salt Crusade. Written": the transcription window cut
	// the next sentence off after a word or two. That tail is prose.
	parts := sentenceEndRe.Split(c, -1)
	return len(strings.Fields(parts[len(parts)-1])) <= 2
}

var (
	sentenceBreakRe = regexp.MustCompile(`[.!?]\s+\pL`)
	sentenceEndRe   = regexp.MustCompile(`[.!?]\s+`)
)

// namesAPersonNormalized compares normalized names, and a "Last, First"
// spelling in either position ("Quill, Marta" is Marta Quill), against the
// book's own people.
func (pc junkProposalCheck) namesAPersonNormalized(s string) bool {
	forms := personNameForms
	mine := forms(s)
	for _, p := range pc.people {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		for _, a := range forms(p) {
			for _, b := range mine {
				if a != "" && a == b {
					return true
				}
			}
		}
	}
	return false
}

// isPathRoot: c is the first segment of one of the book's paths ("lib" of
// "/lib/A/intro.mp3"), a segment of a configured library root or import
// path, or the author folder right below an author-first library root.
func (pc junkProposalCheck) isPathRoot(c string) bool {
	eq := func(seg string) bool { return seg != "" && strings.EqualFold(seg, c) }
	for _, p := range pc.paths {
		if first, _, _ := strings.Cut(strings.TrimPrefix(filepath.ToSlash(p), "/"), "/"); eq(first) {
			return true
		}
	}
	for _, root := range pc.roots {
		for seg := range strings.SplitSeq(filepath.ToSlash(root), "/") {
			if eq(seg) {
				return true
			}
		}
	}
	if pc.authorRoot == "" {
		return false
	}
	for _, p := range pc.paths {
		rel, err := filepath.Rel(pc.authorRoot, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		if first, _, more := strings.Cut(filepath.ToSlash(rel), "/"); more && eq(first) {
			return true
		}
	}
	return false
}

// fragmentReason decides whether a junk-titled book is (or may be) a chapter
// file of a bigger book. It returns junkSkipFragment only when another live
// book owns one of its paths; junkSkipPossibleFragment for the shapes that
// suggest it; junkSkipNeedsManual for a bare unpadded number, which is as
// likely a real title ("13", "300"); "" when the book stands on its own.
func (f *junkTitleFixer) fragmentReason(idx *junkIndex, b database.BookCore, kind metadata.JunkTitleKind, author string, paths []string) (skip, why string, err error) {
	store := f.p.deps.OpsStore()
	for _, p := range paths {
		owners := append([]string(nil), idx.owners[p]...)
		sort.Strings(owners)
		for _, o := range owners {
			if o != b.ID {
				return junkSkipFragment, fmt.Sprintf("its file %s is also a file of book %s", p, o), nil
			}
		}
	}
	for _, p := range paths {
		row, err := store.GetBookFileByPath(p)
		if err != nil {
			return "", "", fmt.Errorf("read owner of %s: %w", p, err)
		}
		if row != nil && row.BookID != "" && row.BookID != b.ID {
			return junkSkipFragment, fmt.Sprintf("its file %s is also a file of book %s", p, row.BookID), nil
		}
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		dirs[junkWorkDir(p, b.Title)] = true
	}
	if d := junkWorkDir(b.FilePath, b.Title); d != "" {
		dirs[d] = true
	}
	keys := make([]string, 0, len(dirs))
	for d := range dirs {
		if d != "" {
			keys = append(keys, d)
		}
	}
	sort.Strings(keys)
	// The organizer-moved chapter: author "Eldest", filed at
	// Eldest/02/98/98.mp3 (work folder "Eldest", junkWorkDir). The author
	// named like the work folder is the shape a chapter file parsed as its
	// own book gets. Only the work folder: the folder above it is the author
	// folder of every correctly filed book. And not for a single file that
	// sits directly in its folder: "/lib/Author A/13.m4b" is filed in the
	// author folder by design.
	if a := strings.TrimSpace(author); a != "" && !authorname.IsPlaceholderAuthor(a) {
		for _, p := range append(slices.Clone(paths), b.FilePath) {
			d := junkWorkDir(p, b.Title)
			if d == "" {
				continue
			}
			if _, audio := audioExts[strings.ToLower(filepath.Ext(p))]; audio && d == filepath.Dir(p) {
				continue
			}
			if strings.EqualFold(a, filepath.Base(d)) {
				return junkSkipPossibleFragment, fmt.Sprintf(
					"its author %q is the name of its folder %s, the shape a chapter file parsed as its own book gets", a, d), nil
			}
		}
	}
	bare := bareNumberRe.MatchString(strings.TrimSpace(b.Title))
	for _, d := range keys {
		// A chapter-titled book sharing its folder with any other book may be
		// a piece of it. Any other junk title ("Opening", "read by narrator")
		// is suspect only beside chapter-titled siblings: single-file books
		// of different works often share an author folder.
		others := idx.dirChapters[d]
		// A track-number prefix beside other books is the tracks-of-one-book
		// shape too ("01 - Chapter One", "02 - Chapter Two").
		if kind.IsChapterKind() || kind == metadata.JunkNumberPrefix {
			others = idx.dirBooks[d] - 1
		}
		// A bare unpadded number ("13") is as likely a real title: beside
		// real-titled books of its author it stays on its own; only beside
		// other chapter-titled books ("1".."20") is it a possible fragment.
		if bare {
			others = idx.dirChapters[d] - 1
		}
		if others >= 1 {
			return junkSkipPossibleFragment, fmt.Sprintf("%d books share the folder %s", idx.dirBooks[d], d), nil
		}
	}
	// A bare unpadded number alone in its folder may be the real title
	// ("13", "300"); beside sibling books ("1".."20" in one folder) it was
	// caught above as a possible fragment.
	if bare {
		return junkSkipNeedsManual, "the title is a bare number, which may be the real title; nothing proves it is a chapter", nil
	}
	if kind.IsChapterKind() && len(paths) < 2 {
		return junkSkipPossibleFragment, "a single file titled only by a chapter position", nil
	}
	return "", "", nil
}

// candidateTitle returns the highest-scoring cached metadata candidate's
// title when it is trustworthy: score at least junkCandidateMinScore, no
// runtime mismatch, and the same author as the book (the search ran on the
// junk title, so a candidate that disagrees on the author is noise). Ties
// keep the cache's order.
//
// The book's ASIN (b.ASIN) decides first. A candidate naming another ASIN
// is skipped: it is another record (the bulk gate's asin_conflict). Among the
// trustworthy rest, one carrying the book's own ASIN wins over any higher
// score: the book's candidates survive an ASIN fill, so the record the ASIN
// names is often one of them, and it is the book by definition.
//
// The row is read through CachedMetadataCandidates, the apply paths' read:
// candidates an earlier search version cached are filtered by the current
// position rules there, and a raw MetadataCacheStore read would offer a
// sibling the old ladder pooled as this book's title.
//
// Every candidate also passes metafetch.CandidateIdentityStale, the gate the
// other apply paths use: a row marked Stale (the book was retitled or
// re-credited after the fetch) offers nothing, and a candidate fetched for an
// ASIN the book no longer carries is skipped. Without it a book retitled into
// a junk title would be offered the old title's candidates.
//
// A candidate the owner rejected for the book (metafetch.RejectedSet: its
// source and title, or an ASIN or ISBN the owner rejected under any source)
// is skipped inside the loop too, so the next trustworthy candidate is the
// one chosen and a rejected record never becomes evidence. The row's order
// does not protect this loop: it takes the best candidate wherever it sits,
// so the re-rank that moves a rejected candidate down is no help here. An
// unreadable rejection list is returned as err (the caller fails the row
// closed); no title is chosen then. A nil book offers nothing.
func (f *junkTitleFixer) candidateTitle(b *database.Book, author string) (string, float64, bool, error) {
	if b == nil {
		return "", 0, false, nil
	}
	entry, err := f.p.deps.CachedMetadataCandidates(b.ID)
	if err != nil || entry == nil || entry.Stale {
		return "", 0, false, nil
	}
	a := strings.TrimSpace(author)
	if a == "" || authorname.IsPlaceholderAuthor(a) {
		return "", 0, false, nil
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return "", 0, false, fmt.Errorf("read owner rejections: database not initialized")
	}
	rejected, err := metafetch.LoadRejectedCandidates(store, b.ID)
	if err != nil {
		return "", 0, false, err
	}
	own := ""
	if b.ASIN != nil {
		own = strings.TrimSpace(*b.ASIN)
	}
	best, bestScore, found, bestOwn := "", 0.0, false, false
	for _, raw := range entry.Candidates {
		var c metafetch.MetadataCandidate
		if json.Unmarshal(raw, &c) != nil || c.Score < junkCandidateMinScore || c.DurationMismatch {
			continue
		}
		// An entry the local catalog tagged manual-only (Doctor Who / Big
		// Finish / Torchwood, by fields the candidate's own title and
		// publisher may not show) is never the source of a title this
		// unattended fixer writes: those books are applied by hand.
		if c.ManualOnly {
			continue
		}
		if metafetch.CandidateIdentityStale(entry, b, &c) != nil || rejected.HasCandidate(&c) {
			continue
		}
		if !strings.EqualFold(util.NormalizeAuthor(c.Author), util.NormalizeAuthor(a)) {
			continue
		}
		ca := strings.TrimSpace(c.ASIN)
		if own != "" && ca != "" && !strings.EqualFold(ca, own) {
			continue
		}
		isOwn := own != "" && strings.EqualFold(ca, own)
		switch {
		case !found, isOwn && !bestOwn, isOwn == bestOwn && c.Score > bestScore:
			best, bestScore, found, bestOwn = c.Title, c.Score, true, isOwn
		}
	}
	return best, bestScore, found, nil
}

// catalogEditionRe is a bracketed edition marker on a catalog title:
// "(Unabridged)", "[Abridged]".
var catalogEditionRe = regexp.MustCompile(`(?i)\s*[(\[]\s*(?:un)?abridged\s*[)\]]`)

// legacyStateUnreadable returns why a book whose field state is only in a
// pre-migration blob that does not parse must be held: the apply locks the
// title it writes, which migrates the blob first, and an unreadable blob
// cannot be migrated. "" for every other book. states are the book's rows.
func legacyStateUnreadable(reader database.MetadataFieldStateReader, bookID string, states []database.MetadataFieldState) string {
	if len(states) > 0 {
		return ""
	}
	if _, err := database.ParseLegacyMetadataState(reader, bookID); err != nil {
		return fmt.Sprintf("its pre-migration metadata state cannot be read (%v); open the book once to repair it", err)
	}
	return ""
}

// junkLettersKey is a title reduced to its letters and digits, NFC and lower
// case: what two spellings of one title share whatever punctuation, spacing
// or Unicode form each uses ("3-10 to Yuma", "3:10 to Yuma").
func junkLettersKey(title string) string {
	var sb strings.Builder
	for _, r := range norm.NFC.String(strings.ToLower(title)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// junkFingerprint hashes the decision's inputs and outputs.
func junkFingerprint(r repairs.Row, extra string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s\n%s\n%s\n%s\n", r.RowID, r.Current["title"], r.Skipped, r.Proposed["title"], extra)
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])[:32]
}

// seriesNumberRefusal returns why the series and the number read from the
// stored title may not be proposed as "<series>, Book <N>", "" when they may.
// All 61 series_and_number rows of the 2026-10-23 plan were wrong: the series
// was the book's own title or a junk value ("Disc 04"), and the number was a
// disc, chapter or track number rather than the series position. So the
// stored title must carry no position word (disc, CD, track, part, "N of M"),
// the series must be a real series (not junk, not the stored title, not the
// book's own folder), and the number must be the book's recorded series
// sequence.
func seriesNumberRefusal(series string, b database.BookCore, narrators, agreeWith []string) string {
	switch {
	case b.SeriesSequence == nil:
		return "the book has no recorded series position to check the number in its title against"
	case positionWordRe.MatchString(b.Title):
		return fmt.Sprintf("the stored title %q is a position inside a book (disc, track, part or chapter), not a series position", b.Title)
	case metadata.ClassifyJunkTitleFor(series, narrators) != metadata.JunkNone, positionMarkerRe.MatchString(series),
		trackTokenRe.MatchString(series), authorname.IsPlaceholderTitle(series):
		return fmt.Sprintf("the series %q is not a real series name", series)
	case junkLettersKey(series) == junkLettersKey(b.Title):
		return fmt.Sprintf("the series %q is the book's own stored title", series)
	case junkEvidenceAgrees(series, agreeWith):
		return fmt.Sprintf("the series %q is the book's own folder or title, not a series that holds it", series)
	}
	return ""
}

// bookRuntimeSec is how long the book runs: its own Duration, or, when it has
// none, the sum of its files' durations when EVERY file has one (a partial sum
// would call a whole book short). known is false when neither is available; the
// gate then fails closed (fragmentEvidenceReason). A file's duration is read through
// database.NormalizeDurationSec, which repairs a millisecond value.
func bookRuntimeSec(b database.BookCore, files []database.BookFile) (secs int, known bool) {
	if b.Duration != nil && *b.Duration > 0 {
		return *b.Duration, true
	}
	if len(files) == 0 {
		return 0, false
	}
	sum := 0
	for i := range files {
		d := database.NormalizeDurationSec(files[i].FileSize, files[i].Duration)
		if d <= 0 {
			return 0, false
		}
		sum += d
	}
	return sum, true
}

// fragmentEvidenceReason returns the skip status and the reason when the
// book's own facts say it is, or cannot be shown not to be, a piece of a bigger
// book, and "" when they say it is a whole one:
//
//   - a library_state of "suspicious", or a runtime under the fixer's minimum:
//     a possible fragment;
//   - an unknown runtime (bookRuntimeSec): needs_manual. The gate cannot clear
//     the book, and the books it exists for (one lesson of a language course,
//     an intro track) are the ones a scan often has no duration for, so
//     passing them would retitle each with its whole work's name. A person
//     decides; a book whose duration is filled in later is planned normally.
func (f *junkTitleFixer) fragmentEvidenceReason(b database.BookCore, runtimeSec int, runtimeKnown bool) (skip, why string) {
	if b.LibraryState != nil && strings.EqualFold(strings.TrimSpace(*b.LibraryState), junkLibraryStateSuspicious) {
		return junkSkipPossibleFragment, "the scanner marked the book suspicious, so its path and tags are not evidence of its title"
	}
	if f.minDurationSec <= 0 {
		return "", ""
	}
	if !runtimeKnown {
		return junkSkipNeedsManual, fmt.Sprintf("runtime unknown: neither the book nor every one of its files has a duration, so it cannot be told "+
			"from one track of a bigger book (a whole book runs at least %d s), and a title found outside its own stored title names the whole work",
			f.minDurationSec)
	}
	if runtimeSec < f.minDurationSec {
		return junkSkipPossibleFragment, fmt.Sprintf("the book runs %d s, under the %d s of a whole book; it is likely one track of a bigger book, "+
			"and a title found outside its own stored title names the whole work", runtimeSec, f.minDurationSec)
	}
	return "", ""
}

// junkOwner is whose title a proposal would be. For a real stored author it is
// that author (id and name). For a junk one (empty, a placeholder, a credit
// line) the stored author says nothing, so it is the author a provider
// recorded or the author folder names (name only), and when neither is known
// the name is empty: an owner that may be anybody's.
type junkOwner struct {
	name string
	junk bool
	id   *int
}

func (o junkOwner) describe() string {
	if o.name == "" {
		return "any author"
	}
	return o.name
}

// ownersMayBeSame reports whether two proposal owners may be one person: the
// same author id, an unknown owner (no name: it may be anyone), or names that
// are the same person (util.NormalizeAuthor equal, or authorjunk.SamePersonName:
// "J.M. Quill" and "J. M. Quill"). Grouping and titleTaken share it, so a
// duplicate is held whichever of the two sees it.
func ownersMayBeSame(a, b junkOwner) bool {
	if a.id != nil && b.id != nil && *a.id == *b.id {
		return true
	}
	if a.name == "" || b.name == "" {
		return true
	}
	return util.NormalizeAuthor(a.name) == util.NormalizeAuthor(b.name) || authorjunk.SamePersonName(a.name, b.name)
}

// proposalOwner resolves the junkOwner of a book's proposal.
func (f *junkTitleFixer) proposalOwner(idx *junkIndex, b database.BookCore, author, fetchedAuthor string, paths []string) junkOwner {
	if b.AuthorID != nil && !junkStoredAuthor(author) {
		return junkOwner{name: strings.TrimSpace(author), id: b.AuthorID}
	}
	name := ""
	if fa := strings.TrimSpace(fetchedAuthor); fa != "" && !junkStoredAuthor(fa) {
		name = fa
	} else if fa := folderAuthor(idx, paths); fa != "" && !junkStoredAuthor(fa) {
		name = fa
	}
	return junkOwner{name: name, junk: true}
}

// folderAuthor is the author folder of an author-first library
// ("{root}/{author}/{title}/…"): the segment right below the library root.
// "" when the library is not author-first or no path sits under the root.
func folderAuthor(idx *junkIndex, paths []string) string {
	if idx.authorRoot == "" {
		return ""
	}
	for _, p := range paths {
		rel, err := filepath.Rel(idx.authorRoot, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		if first, _, more := strings.Cut(filepath.ToSlash(rel), "/"); more && first != "" {
			return first
		}
	}
	return ""
}

// titleTaken reports whether a live book with a real title already holds
// title for the proposal's owner. Each holder of the title is matched by
// ownersMayBeSame, the rule Plan's grouping applies between two proposals, so a
// duplicate is held whichever of the two guards sees it: a holder of the same
// author id, of a duplicate row of the same person under another id, or one
// whose author is junk or missing (an unknown owner: it may be the very book
// this one is) counts. A skipped row costs far less than a duplicate.
func (f *junkTitleFixer) titleTaken(idx *junkIndex, o junkOwner, title string) bool {
	for _, id := range idx.titleAuthors[util.TitleSortKey(title)] {
		holder := junkOwner{id: &id}
		if id != 0 {
			// A holder with no real author name (a placeholder, a credit line)
			// is an unknown owner.
			if n := f.authorName(idx, &id); !junkStoredAuthor(n) {
				holder.name = strings.TrimSpace(n)
			}
		}
		if ownersMayBeSame(o, holder) {
			return true
		}
	}
	return false
}
