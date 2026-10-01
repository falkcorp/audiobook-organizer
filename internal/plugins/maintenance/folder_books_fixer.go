// file: internal/plugins/maintenance/folder_books_fixer.go
// version: 1.0.0
// guid: 3b8e5d17-9c2a-4f60-8e41-6a7d2c9f0b35
// last-edited: 2026-10-01

// Repairs-lane fixer "folder-books": retire book rows that are really whole
// author, series or library folders, and give every file only such a row held
// a proper book of its own.
//
// HOW THEY CAME TO BE (census 2026-09-30, 511 rows). The scanner's 3-file
// album sample (fixed in d1431f5b8) and the 2026-08-26 backfill-book-files
// job (non-recursive ReadDir) attached a whole folder's files to one book:
// "Gene Wolfe" with 1,494 files, "iTunes Media", "Prologue", a series folder.
//
// DETECTION (ported from the census's fold_final.py). A candidate is a live
// book with at least fbMinFiles book_file rows whose files all sit under ONE
// shelf (config RootDir, an import path, or an iTunes Media/Audiobooks
// folder). From its rows:
//
//   - D: the rows' durations sum to at least 80h.
//   - P: other live books holding a strict subset of its paths, each with two
//     or more files or at least an hour, under a different title.
//   - works: the groups its files fall into (fbGroupKey: a file in a
//     sub-folder of the book's common root groups by that folder, a disc
//     folder counting as its parent; a flat file by its filename stem).
//   - scattered: the title is neither the author folder nor the root folder
//     and at least half of the groups are named after it. That is ONE real
//     book whose files organize scattered (the fragment fixer's ground), so it
//     is never flagged.
//
// Tiers, first match wins:
//
//   - shelf: the common root is at or above its shelf, or directly under it
//     (an author folder), and D, P>=2, a generic title ("Prologue", "iTunes
//     Media"), or the title is the root folder's name AND the files are two or
//     more works. A real multi-file book titled with its author's name whose
//     files are one work is NOT flagged (owner rule 2026-10-01): the
//     census's "title equals folder" alone flagged it.
//   - author-copy: deeper than an author folder, titled with the author
//     folder's name, and D, or 100+ files that are two or more works.
//   - deep: D or P>=3 anywhere else. Listed for review, never applied:
//     durations are often corrupt, so the tier is unreliable.
//
// Folder-books with the same file set are one row and retire together.
//
// APPLY (database only: nothing on disk moves, no book_file row and no book
// row is deleted). Each path a row's folder-books hold is:
//
//   - held: a live book outside the applicable folder-book set holds it (a
//     book_file row or its own file_path); a deep-tier book counts. Nothing
//     to do: that book keeps it.
//   - held by another folder-book row: an applicable folder-book with a
//     smaller file set (ties: the lower row id) holds it too. That row owns
//     it; it stays visible there until that row applies.
//   - orphan: grouped by fbGroupKey, each group becomes ONE new book (its
//     title the folder or stem, its author the folder-book's when the folder
//     is an author shelf, its library_state the folder-book's) with new
//     book_file rows at the same paths, copying track order, size, hash and
//     duration but never an iTunes persistent id (CreateBookFile would take
//     the id from the folder-book's row). A group an earlier cut-off apply
//     already started (a live book holding part of the group that this fixer
//     created: an unreverted repair_book_create row) is extended, not created
//     twice.
//
// Then every folder-book of the row is retired: demoted when primary
// (book_primary_demote, then the version group is handed a primary,
// book_primary_handoff), its file_path cleared (book_path_update) and
// soft-deleted (book_soft_delete). Its own book_file rows stay on it. The
// purge refuses a book that still owns rows, so the retired book is hidden
// (ABS, the library, dedup, search, and the fragment fixer's parent index all
// skip soft-deleted books) and never purged. Members that are not primary
// retire first, so a duplicate is never crowned in passing.
//
// The invariant behind every skip: a row applies only when each of its files
// is still held by a live book afterwards. A soft-deleted owner stops the
// scanner re-importing a file (scanner/file_ownership.go), so retiring a
// folder-book that was a file's only holder would hide that file for good.
//
// SKIPPED rows (listed, never applied): deep; manual-only and iTunes paths
// (framework guards; this fixer never bypasses them); an iTunes persistent id
// on a folder-book, its rows or its external ids (retiring it would queue an
// iTunes remove); listening progress on a folder-book; and "fragmentary": an
// orphan group shorter than the chapter-consolidation threshold would become
// a chapter-sized book (a flat folder of separately titled tracks).
//
// UNDO. Every write is journaled first through repairs.Writer:
// repair_book_create (revert: the created book is soft-deleted, its rows
// kept), book_file_create (record-only: no repair deletes a row),
// book_primary_demote, book_primary_handoff, book_path_update,
// book_soft_delete. POST /operations/<apply op>/revert restores every
// folder-book, re-crowns it, and hides the books the apply created.
//
// CONCURRENCY. Plan evaluates candidates on a bounded RunItems pool. Apply
// holds merge.LockMergeRMW for the whole row and re-plans under it; the
// framework engine partitions rows sharing a book.
package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

const fbFixerID = "folder-books"

// Tiers (Row.Class), and the class of a row a refusal holds.
const (
	fbTierShelf      = "shelf"
	fbTierAuthorCopy = "author-copy"
	fbTierDeep       = "deep"
)

// Skip kinds this fixer sets itself (the framework adds the guard kinds).
const (
	fbSkipDeep        = "skipped_deep"
	fbSkipFragmentary = "skipped_fragmentary"
	fbSkipITunesID    = "skipped_itunes_id"
	fbSkipProgress    = "skipped_listening_progress"
	fbSkipUnreadable  = "skipped_unreadable"
)

// Detection thresholds (the census's).
const (
	fbMinFiles       = 5
	fbDeepHours      = 80
	fbAuthorCopyMinN = 100
	fbProperMinSec   = 3600
)

// fbGeneric are titles that name no work (normalized by fbNorm).
var fbGeneric = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range []string{"iTunes Media", "Audiobooks", "Audiobook", "Books", "Prologue", "Audible Opening Message",
		"read by narrator", "GraphicAudio", "Introduction", "Intro", "Unknown Author", "Star Trek", "Epilogue",
		"Opening Credits", "Credits", "Music", "Unknown", "Unknown Album", "Various", "Podcasts", "newbooks", "abooks",
		"audiobook-organizer", "itunes", "Audible"} {
		m[fbNorm(t)] = true
	}
	return m
}()

// fbNorm lower-cases s and keeps only letters and digits (any script, so a
// Cyrillic title does not normalize to "" and match every other one).
func fbNorm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var (
	fbLeadNumRe  = regexp.MustCompile(`^[\d\s\-_.]+`)
	fbTailNumRe  = regexp.MustCompile(`[\s\-_]*\(?\d+\s*(?:of\s*\d+)?\)?\s*$`)
	fbPartTailRe = regexp.MustCompile(`(?i)\s*[-_]?\s*(?:part|pt|track|chapter|disc|cd)\s*\d+.*$`)
	fbDiscDirRe  = regexp.MustCompile(`(?i)^(?:cd|disc|disk|part|pt|vol|volume|side)\s*[-_ ]?\d+$`)
)

// fbStem is the census's per-work filename stem (fold_ex.py): leading track
// numbers, a trailing part/track/chapter/disc marker and a trailing "(N of
// M)" are stripped.
func fbStem(p string) string {
	s := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	s = fbLeadNumRe.ReplaceAllString(s, "")
	// The part marker goes first, so "Title - Part 2" loses "- Part 2"
	// whole (the census stripped the number first and kept "Title - Part").
	s = fbPartTailRe.ReplaceAllString(s, "")
	s = fbTailNumRe.ReplaceAllString(s, "")
	return strings.Trim(s, " -_.")
}

// fbGroupKey is the work a file belongs to inside a folder rooted at root:
// its folder when that is a sub-folder of root (a disc folder counts as its
// parent), else its filename stem. label is the group's display title.
func fbGroupKey(p, root string) (key, label string) {
	d := filepath.Dir(p)
	if fbDiscDirRe.MatchString(filepath.Base(d)) {
		d = filepath.Dir(d)
	}
	if d != root && pathutil.IsWithin(d, root) {
		return "dir:" + d, filepath.Base(d)
	}
	st := fbStem(p)
	if fbNorm(st) == "" {
		st = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	}
	return "stem:" + d + "|" + fbNorm(st), st
}

// commonDir is the deepest folder containing every path.
func commonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	root := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for root != "/" && root != "." && !pathutil.IsWithin(p, root) {
			root = filepath.Dir(root)
		}
	}
	return root
}

// ---- library snapshot -------------------------------------------------------

// fbLib is one read of the library.
type fbLib struct {
	books   map[string]database.BookCore // live books only
	files   map[string][]database.BookFileCore
	holders map[string][]string // path -> live books holding it (rows or file_path)
	sets    map[string]map[string]bool
	authors map[int]string
	shelves []string
	fixer   *folderBooksFixer
}

func (f *folderBooksFixer) loadLib(store OpsStore) (*fbLib, error) {
	lib := &fbLib{books: map[string]database.BookCore{}, files: map[string][]database.BookFileCore{},
		holders: map[string][]string{}, sets: map[string]map[string]bool{}, authors: map[int]string{}, fixer: f}
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	for i := range books {
		if !books[i].IsSoftDeleted() {
			lib.books[books[i].ID] = books[i]
		}
	}
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("list book files: %w", err)
	}
	for i := range cores {
		r := cores[i]
		if _, live := lib.books[r.BookID]; !live || r.FilePath == "" {
			continue
		}
		lib.files[r.BookID] = append(lib.files[r.BookID], r)
	}
	for id, b := range lib.books {
		set := map[string]bool{}
		for _, r := range lib.files[id] {
			set[r.FilePath] = true
		}
		// A book with no rows holds its own file_path (a single-file book).
		if len(set) == 0 && b.FilePath != "" {
			set[b.FilePath] = true
		}
		lib.sets[id] = set
		for p := range set {
			lib.holders[p] = append(lib.holders[p], id)
		}
		// A row-holding book's own file_path is a holder too (LiveBookIDsAtPath).
		if b.FilePath != "" && !set[b.FilePath] {
			lib.holders[b.FilePath] = append(lib.holders[b.FilePath], id)
		}
	}
	for p := range lib.holders {
		sort.Strings(lib.holders[p])
	}
	if authors, aerr := store.GetAllAuthors(); aerr == nil {
		for _, a := range authors {
			lib.authors[a.ID] = a.Name
		}
	}
	if root := config.AppConfig.RootDir; root != "" {
		lib.shelves = append(lib.shelves, filepath.Clean(root))
	}
	ips, err := store.GetAllImportPaths()
	if err != nil {
		return nil, fmt.Errorf("list import paths: %w", err)
	}
	for _, ip := range ips {
		if ip.Path != "" {
			lib.shelves = append(lib.shelves, filepath.Clean(ip.Path))
		}
	}
	return lib, nil
}

// shelfOf is the shelf a file sits under: the deepest configured shelf
// containing it, or the iTunes Media/Audiobooks folder above it. "" for none.
func (lib *fbLib) shelfOf(p string) string {
	best := ""
	for _, s := range lib.shelves {
		if (p == s || pathutil.IsWithin(p, s)) && len(s) > len(best) {
			best = s
		}
	}
	if i := strings.Index(p, "/iTunes Media/Audiobooks/"); i >= 0 {
		if s := p[:i+len("/iTunes Media/Audiobooks")]; len(s) > len(best) {
			best = s
		}
	}
	return best
}

// ---- detection ----------------------------------------------------------------

// fbEval is one candidate's detection result.
type fbEval struct {
	ID       string
	Tier     string // "" when not a folder-book
	Root     string
	Author   string // the author folder's name ("" none)
	N        int
	Hours    float64
	Proper   int
	Works    int
	Evidence []string
}

// evaluate classifies one live book.
func (lib *fbLib) evaluate(id string) fbEval {
	ev := fbEval{ID: id}
	b, ok := lib.books[id]
	rows := lib.files[id]
	if !ok || len(rows) < fbMinFiles {
		return ev
	}
	ev.N = len(rows)
	paths := make([]string, 0, len(rows))
	shelf := ""
	var secs int64
	for i, r := range rows {
		paths = append(paths, r.FilePath)
		secs += int64(r.Duration)
		s := lib.shelfOf(r.FilePath)
		if i == 0 {
			shelf = s
		} else if s != shelf {
			return ev // files span two shelves: not a folder of one tree
		}
	}
	ev.Hours = float64(secs) / 3600
	ev.Root = commonDir(paths)
	set := lib.sets[id]
	t := fbNorm(b.Title)

	// P: other books holding a strict subset under a different title.
	seen := map[string]bool{id: true}
	for _, p := range paths {
		for _, h := range lib.holders[p] {
			if seen[h] {
				continue
			}
			seen[h] = true
			hs := lib.sets[h]
			if len(hs) == 0 || len(hs) >= len(set) || fbNorm(lib.books[h].Title) == t {
				continue
			}
			sub := true
			for q := range hs {
				if !set[q] {
					sub = false
					break
				}
			}
			if !sub {
				continue
			}
			dur := 0
			if lib.books[h].Duration != nil {
				dur = *lib.books[h].Duration
			}
			if len(hs) >= 2 || dur >= fbProperMinSec {
				ev.Proper++
			}
		}
	}
	groups := map[string]string{}
	for _, p := range paths {
		k, label := fbGroupKey(p, ev.Root)
		groups[k] = label
	}
	ev.Works = len(groups)

	if shelf != "" && ev.Root != shelf && pathutil.IsWithin(ev.Root, shelf) {
		rel, err := filepath.Rel(shelf, ev.Root)
		if err == nil {
			ev.Author = strings.Split(rel, string(filepath.Separator))[0]
		}
	}
	base := fbNorm(filepath.Base(ev.Root))
	authorNamed := ev.Author != "" && t == fbNorm(ev.Author)
	deep := ev.Hours >= fbDeepHours
	generic := fbGeneric[t]

	// One real book scattered by organize: most groups carry its title.
	tn := fbNorm(fbLeadNumRe.ReplaceAllString(b.Title, ""))
	if !authorNamed && t != base && len([]rune(tn)) >= 4 && !generic {
		self := 0
		for _, label := range groups {
			if strings.Contains(fbNorm(label), tn) {
				self++
			}
		}
		if self*2 >= len(groups) {
			return ev
		}
	}
	shelfLoc := shelf != "" && (ev.Root == shelf || pathutil.IsWithin(shelf, ev.Root) ||
		(ev.Author != "" && filepath.Dir(ev.Root) == shelf))
	multi := ev.Works >= 2
	addEv := func(s string, on bool) {
		if on {
			ev.Evidence = append(ev.Evidence, s)
		}
	}
	addEv(fmt.Sprintf("%.0fh of audio (>= %dh)", ev.Hours, fbDeepHours), deep)
	addEv(fmt.Sprintf("%d other books hold strict subsets of its files", ev.Proper), ev.Proper >= 2)
	addEv("generic title", generic)
	addEv(fmt.Sprintf("title is the folder name and the files are %d works", ev.Works), t == base && multi)
	switch {
	case shelfLoc && (deep || ev.Proper >= 2 || generic || (t == base && multi)):
		ev.Tier = fbTierShelf
	case !shelfLoc && authorNamed && (deep || (ev.N >= fbAuthorCopyMinN && multi)):
		ev.Tier = fbTierAuthorCopy
		addEv(fmt.Sprintf("titled with its author folder %q, %d files, %d works", ev.Author, ev.N, ev.Works), true)
	case deep || ev.Proper >= 3:
		ev.Tier = fbTierDeep
	}
	return ev
}

func fbApplicableTier(t string) bool { return t == fbTierShelf || t == fbTierAuthorCopy }

// ---- fixer --------------------------------------------------------------------

type folderBooksFixer struct {
	p *Plugin
	// now and newID replace time.Now and the ULID mint in tests.
	now   func() time.Time
	newID func() string
}

func newFolderBooksFixer(p *Plugin) *folderBooksFixer {
	return &folderBooksFixer{p: p, now: time.Now, newID: func() string { return ulid.Make().String() }}
}

var _ repairs.Fixer = (*folderBooksFixer)(nil)

func (f *folderBooksFixer) ID() string    { return fbFixerID }
func (f *folderBooksFixer) Title() string { return "Folder-sized books" }
func (f *folderBooksFixer) Description() string {
	return "Books that are really a whole author, series or library folder (an old scan and the 2026-08-26 " +
		"book-file backfill attached every file in the folder). Apply gives each file no proper book holds a new " +
		"book of its own (grouped by folder or filename stem, same paths, nothing moved), then hides the folder-book " +
		"(soft-deleted with its file rows kept; primacy handed to the real book). Deep-tier, fragmentary, iTunes and " +
		"Doctor Who / Big Finish / Torchwood rows are listed only. Every step is undoable from the apply operation."
}

type fbParams struct {
	BookIDs []string `json:"book_ids,omitempty"`
}

func decodeFBParams(raw json.RawMessage) (fbParams, error) {
	var p fbParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, fmt.Errorf("%s: invalid params: %w", fbFixerID, err)
		}
	}
	return p, nil
}

func (f *folderBooksFixer) store() (OpsStore, FragmentRepairReader, error) {
	store := f.p.deps.OpsStore()
	hist := f.p.deps.FragmentRepairReader()
	if store == nil || hist == nil {
		return nil, nil, fmt.Errorf("database not initialized")
	}
	return store, hist, nil
}

// fbState is stored with the plan: the row's folder-books.
type fbState struct {
	Members []string `json:"members"`
}

// fbNewFile is one book_file row a group copies.
type fbNewFile struct {
	SrcBook, SrcRow, Path string
	Track                 int
}

// fbGroup is one orphan work.
type fbGroup struct {
	Key, Title string
	AuthorID   *int
	// Target is the book an earlier cut-off apply created ("" create one).
	Target string
	Files  []fbNewFile
	Secs   int
}

// fbPlan is Row.Detail.
type fbPlan struct {
	Members []string // retire order: non-primary first
	State   string   // library_state for created books
	Groups  []fbGroup
}

// Plan evaluates every candidate on a bounded pool and builds one row per
// set of folder-books sharing a file set.
func (f *folderBooksFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	params, err := decodeFBParams(raw)
	if err != nil {
		return nil, err
	}
	store, hist, err := f.store()
	if err != nil {
		return nil, err
	}
	lib, err := f.loadLib(store)
	if err != nil {
		return nil, err
	}
	var ids []string
	for id, rows := range lib.files {
		if len(rows) >= fbMinFiles {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	evals := make([]fbEval, len(ids))
	idx := make([]int, len(ids))
	for i := range idx {
		idx[i] = i
	}
	var done atomic.Int64
	// Each worker writes only evals[i]; lib is read-only here.
	if err := registry.RunItems(ctx, rep, idx, func(_ context.Context, i int) error {
		defer done.Add(1)
		evals[i] = lib.evaluate(ids[i])
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Folder-book candidates %d/%d", done.Load(), total) },
	}); err != nil {
		return nil, fmt.Errorf("%s: candidates: %w", fbFixerID, err)
	}
	ev := map[string]fbEval{}
	for _, e := range evals {
		if e.Tier != "" {
			ev[e.ID] = e
		}
	}
	var only map[string]bool
	if len(params.BookIDs) > 0 {
		only = map[string]bool{}
		for _, id := range params.BookIDs {
			only[id] = true
		}
	}
	var rows []repairs.Row
	for _, members := range fbDupGroups(lib, ev) {
		if only != nil && !anyIn(members, only) {
			continue
		}
		rows = append(rows, f.buildRow(lib, hist, store, ev, members))
	}
	return rows, nil
}

func anyIn(ids []string, set map[string]bool) bool {
	for _, id := range ids {
		if set[id] {
			return true
		}
	}
	return false
}

func fbSetKey(set map[string]bool) string {
	ps := make([]string, 0, len(set))
	for p := range set {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	h := sha256.Sum256([]byte(strings.Join(ps, "\x00")))
	return hex.EncodeToString(h[:])
}

// fbDupGroups groups the flagged books by identical file set, each group
// sorted by id, the groups by their first id.
func fbDupGroups(lib *fbLib, ev map[string]fbEval) [][]string {
	by := map[string][]string{}
	for id := range ev {
		k := fbSetKey(lib.sets[id])
		by[k] = append(by[k], id)
	}
	out := make([][]string, 0, len(by))
	for _, g := range by {
		sort.Strings(g)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func fbRowID(members []string) string { return "folder:" + members[0] }

// ownsBefore reports whether folder-book row a (file count na, first id ida)
// owns a shared orphan before row b.
func ownsBefore(na int, ida string, nb int, idb string) bool {
	if na != nb {
		return na < nb
	}
	return ida < idb
}

// buildRow classifies every path of the row and decides what apply writes.
func (f *folderBooksFixer) buildRow(lib *fbLib, hist FragmentRepairReader, store OpsStore, ev map[string]fbEval, members []string) repairs.Row {
	lead := ev[members[0]]
	head := lib.books[members[0]]
	set := lib.sets[members[0]]
	row := repairs.Row{RowID: fbRowID(members), BookIDs: append([]string(nil), members...), Title: head.Title,
		Class: lead.Tier, Risk: repairs.RiskReview}
	if head.AuthorID != nil {
		row.Author = lib.authors[*head.AuthorID]
	}
	st, _ := json.Marshal(fbState{Members: members})
	row.State = st
	isMember := map[string]bool{}
	for _, m := range members {
		isMember[m] = true
		mb := lib.books[m]
		missing := 0
		for _, r := range lib.files[m] {
			if r.Missing {
				missing++
			}
		}
		row.Members = append(row.Members, repairs.RowMember{BookID: m, Title: mb.Title, Role: "folder-book",
			Files: len(lib.files[m]), MissingFiles: missing})
	}
	row.Evidence = append(row.Evidence, lead.Evidence...)
	if len(members) > 1 {
		row.Evidence = append(row.Evidence, fmt.Sprintf("%d folder-books hold this exact file set", len(members)))
	}
	row.Current = map[string]string{"tier": lead.Tier, "root": lead.Root, "files": strconv.Itoa(len(set)),
		"works": strconv.Itoa(lead.Works), "hours": fmt.Sprintf("%.1f", lead.Hours)}

	// Classify each path. srcRow: the lead member's row at the path.
	src := map[string]database.BookFileCore{}
	for _, r := range lib.files[members[0]] {
		if _, ok := src[r.FilePath]; !ok {
			src[r.FilePath] = r
		}
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var held, byFolder int
	type orphan struct {
		row      database.BookFileCore
		key, lbl string
	}
	var orphans []orphan
	// keyHolders: non-folder books holding a path of each group key (resume).
	keyHolders := map[string]map[string]bool{}
	fp := []string{"tier=" + lead.Tier, "members=" + strings.Join(members, ",")}
	for _, p := range paths {
		k, lbl := fbGroupKey(p, lead.Root)
		cls := "orphan"
		for _, h := range lib.holders[p] {
			if isMember[h] {
				continue
			}
			he, flagged := ev[h]
			if !flagged || !fbApplicableTier(he.Tier) {
				cls = "held"
				if keyHolders[k] == nil {
					keyHolders[k] = map[string]bool{}
				}
				keyHolders[k][h] = true
				break
			}
			if ownsBefore(len(lib.sets[h]), h, len(set), members[0]) {
				cls = "folder"
			}
		}
		switch cls {
		case "held":
			held++
		case "folder":
			byFolder++
		default:
			orphans = append(orphans, orphan{row: src[p], key: k, lbl: lbl})
		}
		// held and held-by-folder fingerprint alike: a sibling row applying
		// moves a path from one to the other without changing this row.
		if cls == "orphan" {
			fp = append(fp, "o:"+p+"|"+k)
		} else {
			fp = append(fp, "h:"+p)
		}
	}

	plan := &fbPlan{State: "imported"}
	if head.LibraryState != nil && *head.LibraryState != "" {
		plan.State = *head.LibraryState
	}
	// Retire order: non-primary first, so a duplicate is never crowned.
	ordered := append([]string(nil), members...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return !fbPrimary(lib.books[ordered[i]]) && fbPrimary(lib.books[ordered[j]])
	})
	plan.Members = ordered
	for _, m := range ordered {
		mb := lib.books[m]
		vg := ""
		if mb.VersionGroupID != nil {
			vg = *mb.VersionGroupID
		}
		fp = append(fp, fmt.Sprintf("m:%s|%t|%s|%s|%s", m, fbPrimary(mb), vg, plan.State, mb.FilePath))
	}

	// Author for created books: the folder-book's, when the folder is an
	// author shelf (its title or its author names the author folder).
	var authorID *int
	if lead.Author != "" && head.AuthorID != nil &&
		(fbNorm(head.Title) == fbNorm(lead.Author) || fbNorm(lib.authors[*head.AuthorID]) == fbNorm(lead.Author)) {
		authorID = head.AuthorID
	}
	gi := map[string]int{}
	for _, o := range orphans {
		i, ok := gi[o.key]
		if !ok {
			i = len(plan.Groups)
			gi[o.key] = i
			plan.Groups = append(plan.Groups, fbGroup{Key: o.key, Title: o.lbl, AuthorID: authorID})
		}
		g := &plan.Groups[i]
		g.Files = append(g.Files, fbNewFile{SrcBook: o.row.BookID, SrcRow: o.row.ID, Path: o.row.FilePath, Track: o.row.TrackNumber})
		g.Secs += o.row.Duration
	}
	var fragmentary []string
	created, extended := 0, 0
	for i := range plan.Groups {
		g := &plan.Groups[i]
		sort.SliceStable(g.Files, func(a, b int) bool {
			if g.Files[a].Track != g.Files[b].Track {
				return g.Files[a].Track < g.Files[b].Track
			}
			return g.Files[a].Path < g.Files[b].Path
		})
		if t := f.resumeTarget(lib, hist, keyHolders[g.Key], set); t != "" {
			g.Target = t
			extended++
		} else {
			created++
			if g.Secs < thresholdSec() {
				fragmentary = append(fragmentary, fmt.Sprintf("%q (%d file(s), %ds)", g.Title, len(g.Files), g.Secs))
			}
		}
		fp = append(fp, fmt.Sprintf("g:%s|%s|%s|%d", g.Key, g.Title, g.Target, len(g.Files)))
		if a := g.AuthorID; a != nil {
			fp = append(fp, "a:"+strconv.Itoa(*a))
		}
	}
	row.Proposed = map[string]string{"held": strconv.Itoa(held), "held_by_folder_row": strconv.Itoa(byFolder),
		"new_books": strconv.Itoa(created), "extend_books": strconv.Itoa(extended), "retire": strings.Join(members, ",")}
	for i, g := range plan.Groups {
		if i == 20 {
			row.Evidence = append(row.Evidence, fmt.Sprintf("... and %d more new books", len(plan.Groups)-20))
			break
		}
		row.Evidence = append(row.Evidence, fmt.Sprintf("new book %q: %d file(s)", g.Title, len(g.Files)))
	}
	row.Reason = fmt.Sprintf("%s folder-book over %s: %d files, %d already held by proper books, %d held by a "+
		"smaller folder-book, %d orphan file(s) into %d new book(s)", lead.Tier, lead.Root, len(set), held, byFolder,
		len(orphans), created+extended)
	row.Detail = plan
	row.Fingerprint = fragFingerprint(fp...)
	row.BookIDs = append(row.BookIDs, plan.groupTargets()...)
	sort.Strings(row.BookIDs)

	switch {
	case !fbApplicableTier(lead.Tier):
		row.Skipped, row.SkipReason = fbSkipDeep, "deep tier: listed for review only (durations are unreliable)"
	case len(fragmentary) > 0:
		row.Skipped = fbSkipFragmentary
		row.SkipReason = fmt.Sprintf("%d orphan group(s) shorter than the chapter-consolidation threshold would become "+
			"chapter-sized books: %s", len(fragmentary), strings.Join(fbFirst(fragmentary, 5), "; "))
	default:
		if kind, why := f.refusal(lib, store, members); kind != "" {
			row.Skipped, row.SkipReason = kind, why
		}
	}
	return row
}

func fbFirst(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("and %d more", len(s)-n))
	}
	return s
}

func (p *fbPlan) groupTargets() []string {
	var out []string
	for _, g := range p.Groups {
		if g.Target != "" {
			out = append(out, g.Target)
		}
	}
	return out
}

func fbPrimary(b database.BookCore) bool { return b.IsPrimaryVersion == nil || *b.IsPrimaryVersion }

// resumeTarget finds the book an earlier, cut-off apply of this fixer created
// for a group: exactly one non-folder book holding part of the group, every
// one of its paths inside the row's file set, and an unreverted
// repair_book_create row for it. "" when there is none.
func (f *folderBooksFixer) resumeTarget(lib *fbLib, hist FragmentRepairReader, holders map[string]bool, set map[string]bool) string {
	if len(holders) != 1 {
		return ""
	}
	var id string
	for h := range holders {
		id = h
	}
	for p := range lib.sets[id] {
		if !set[p] {
			return ""
		}
	}
	changes, err := hist.GetBookChanges(id)
	if err != nil {
		return ""
	}
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeRepairBookCreate && c.BookID == id && c.RevertedAt == nil {
			return id
		}
	}
	return ""
}

// refusal checks what holds a row back: an iTunes id anywhere on a
// folder-book (retiring it would queue an iTunes remove) or anyone's
// listening progress on it.
func (f *folderBooksFixer) refusal(lib *fbLib, store OpsStore, members []string) (kind, why string) {
	um := f.p.deps.MergeUserStateStore()
	for _, m := range members {
		b := lib.books[m]
		if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
			return fbSkipITunesID, fmt.Sprintf("folder-book %s carries iTunes id %s", m, *b.ITunesPersistentID)
		}
		for _, r := range lib.files[m] {
			if r.ITunesPersistentID != "" {
				return fbSkipITunesID, fmt.Sprintf("folder-book %s row %s carries iTunes id %s", m, r.ID, r.ITunesPersistentID)
			}
		}
		exts, err := store.GetExternalIDsForBook(m)
		if err != nil {
			return fbSkipUnreadable, fmt.Sprintf("external ids of %s unreadable: %v", m, err)
		}
		for _, e := range exts {
			if e.Source == "itunes" && !e.Tombstoned {
				return fbSkipITunesID, fmt.Sprintf("folder-book %s carries iTunes external id %s", m, e.ExternalID)
			}
		}
		if um == nil {
			return fbSkipUnreadable, "user-state store unavailable: cannot tell whether anyone listened to it"
		}
		has, err := merge.BookHasUserProgress(um, m)
		if err != nil {
			return fbSkipUnreadable, fmt.Sprintf("listening progress of %s unreadable: %v", m, err)
		}
		if has {
			return fbSkipProgress, fmt.Sprintf("folder-book %s has listening progress; retire it by hand", m)
		}
	}
	return "", ""
}

// Replan re-reads the library and rebuilds the row for the planned
// folder-books, evaluating only them and the books holding their files.
func (f *folderBooksFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	var st fbState
	if err := json.Unmarshal(planned.State, &st); err != nil || len(st.Members) == 0 {
		return repairs.Row{}, fmt.Errorf("%s: row %s carries no members", fbFixerID, planned.RowID)
	}
	store, hist, err := f.store()
	if err != nil {
		return repairs.Row{}, err
	}
	lib, err := f.loadLib(store)
	if err != nil {
		return repairs.Row{}, err
	}
	gone := func(why string) repairs.Row {
		r := planned
		r.Fingerprint, r.Detail = "changed:"+why, nil
		r.Skipped, r.SkipReason = fbSkipUnreadable, why
		return r
	}
	ev := map[string]fbEval{}
	add := func(id string) {
		if _, ok := ev[id]; ok {
			return
		}
		if e := lib.evaluate(id); e.Tier != "" {
			ev[id] = e
		} else {
			ev[id] = fbEval{}
		}
	}
	for _, m := range st.Members {
		if _, live := lib.books[m]; !live {
			return gone(fmt.Sprintf("folder-book %s is no longer live", m)), nil
		}
		add(m)
		for p := range lib.sets[m] {
			for _, h := range lib.holders[p] {
				add(h)
			}
		}
	}
	flagged := map[string]fbEval{}
	for id, e := range ev {
		if e.Tier != "" {
			flagged[id] = e
		}
	}
	// The row's members must still be flagged and still share one file set.
	key := fbSetKey(lib.sets[st.Members[0]])
	for _, m := range st.Members {
		if _, ok := flagged[m]; !ok {
			return gone(fmt.Sprintf("book %s is no longer a folder-book", m)), nil
		}
		if fbSetKey(lib.sets[m]) != key {
			return gone("the folder-books no longer share one file set"), nil
		}
	}
	members := append([]string(nil), st.Members...)
	for id := range flagged {
		if !contains(members, id) && fbSetKey(lib.sets[id]) == key {
			members = append(members, id)
		}
	}
	sort.Strings(members)
	if fbRowID(members) != planned.RowID {
		return gone("another folder-book with this file set now sorts first"), nil
	}
	return f.buildRow(lib, hist, store, flagged, members), nil
}

// Apply writes one row under the merge lock: create or extend each orphan
// group's book, then retire every folder-book.
func (f *folderBooksFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	store, _, err := f.store()
	if err != nil {
		return err
	}
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	locked, err := f.Replan(ctx, nil, fresh, nil)
	if err != nil {
		return err
	}
	if locked.Fingerprint != fresh.Fingerprint || !locked.Applicable() {
		why := locked.Reason
		if !locked.Applicable() {
			why = locked.SkipReason
		}
		return fmt.Errorf("%w: under the merge lock: %s", repairs.ErrChangedSincePlan, why)
	}
	plan, ok := locked.Detail.(*fbPlan)
	if !ok {
		return fmt.Errorf("%s: row %s carries no plan", fbFixerID, locked.RowID)
	}
	var steps int
	partial := func(err error) error {
		if steps == 0 {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %v", repairs.ErrPartiallyApplied, steps, err)
	}
	for _, g := range plan.Groups {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		did, err := f.applyGroup(store, w, plan, g)
		steps += did
		if err != nil {
			return partial(err)
		}
	}
	retirer := newFragmentFixer(f.p)
	for _, m := range plan.Members {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		did, err := f.retire(ctx, store, w, retirer, m)
		steps += did
		if err != nil {
			return partial(err)
		}
	}
	return nil
}

// applyGroup creates (or extends) one orphan group's book. Journal first: the
// create row is written under a minted id before CreateBook, and each new
// book_file row (record-only) before CreateBookFile.
func (f *folderBooksFixer) applyGroup(store OpsStore, w *repairs.Writer, plan *fbPlan, g fbGroup) (int, error) {
	steps := 0
	target := g.Target
	if target == "" {
		target = f.newID()
		if err := w.Journal(target, undo.ChangeTypeRepairBookCreate, "book", "", target); err != nil {
			return steps, err
		}
		w.Touch()
		state := plan.State
		nb := &database.Book{ID: target, Title: g.Title, AuthorID: g.AuthorID, FilePath: g.Files[0].Path,
			LibraryState: &state}
		if _, err := store.CreateBook(nb); err != nil {
			return steps, fmt.Errorf("create book %q: %w", g.Title, err)
		}
		steps++
		if g.AuthorID != nil {
			if err := store.SetBookAuthors(target, []database.BookAuthor{{BookID: target, AuthorID: *g.AuthorID,
				Role: "author", Position: 0}}); err != nil {
				return steps, fmt.Errorf("credit author of %s: %w", target, err)
			}
		}
	}
	have := map[string]bool{}
	if g.Target != "" {
		rows, err := store.GetBookFiles(target)
		if err != nil {
			return steps, fmt.Errorf("files of %s: %w", target, err)
		}
		for _, r := range rows {
			have[r.FilePath] = true
		}
	}
	for _, nf := range g.Files {
		if have[nf.Path] {
			continue
		}
		src, err := store.GetBookFileByID(nf.SrcBook, nf.SrcRow)
		if err != nil {
			return steps, fmt.Errorf("read row %s: %w", nf.SrcRow, err)
		}
		if src == nil || src.FilePath != nf.Path {
			return steps, fmt.Errorf("%w: row %s of %s no longer names %s", repairs.ErrChangedSincePlan, nf.SrcRow, nf.SrcBook, nf.Path)
		}
		row := *src
		row.ID = f.newID()
		row.BookID = target
		row.ITunesPersistentID = ""
		row.CreatedAt, row.UpdatedAt = time.Time{}, time.Time{}
		if err := w.Journal(target, undo.ChangeTypeBookFileCreate, "book_file:"+row.ID, "", row.FilePath); err != nil {
			return steps, err
		}
		w.Touch()
		if err := store.CreateBookFile(&row); err != nil {
			return steps, fmt.Errorf("create row for %s on %s: %w", nf.Path, target, err)
		}
		steps++
	}
	if err := w.Recompute(target); err != nil {
		return steps, fmt.Errorf("recompute %s: %w", target, err)
	}
	return steps, nil
}

// retire hides one folder-book: demote (journaled, then the group handed a
// primary), clear file_path and soft-delete in one journaled write. Its
// book_file rows stay. A book already soft-deleted counts none.
func (f *folderBooksFixer) retire(ctx context.Context, store OpsStore, w *repairs.Writer, retirer *fragmentFixer, id string) (int, error) {
	b, err := store.GetBookByID(id)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", id, err)
	}
	if b == nil {
		return 0, fmt.Errorf("%w: folder-book %s vanished", repairs.ErrChangedSincePlan, id)
	}
	if b.IsSoftDeleted() {
		return 0, nil
	}
	if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
		return 0, fmt.Errorf("%w: folder-book %s now carries an iTunes id", repairs.ErrChangedSincePlan, id)
	}
	steps := 0
	wasPrimary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
	if wasPrimary {
		notPrimary := false
		if err := w.Step(id, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false", func() error {
			_, err := w.Modify(id, func(cur *database.Book) error {
				if cur.IsPrimaryVersion != nil && !*cur.IsPrimaryVersion {
					return database.ErrSkipBookWrite
				}
				cur.IsPrimaryVersion = &notPrimary
				return nil
			})
			return err
		}); err != nil {
			return steps, fmt.Errorf("demote %s: %w", id, err)
		}
		steps++
	}
	if b.FilePath != "" {
		if err := w.Journal(id, undo.ChangeTypeBookPathUpdate, "file_path", b.FilePath, ""); err != nil {
			return steps, err
		}
	}
	now := f.now().UTC().Truncate(time.Microsecond)
	if prev, ok, err := w.JournaledValue(id, undo.ChangeTypeBookSoftDelete, "marked_for_deletion"); err != nil {
		return steps, fmt.Errorf("read the journaled soft-delete of %s: %w", id, err)
	} else if ok {
		if t, ok := undo.ParseSoftDeleteStamp(prev); ok {
			now = t
		}
	}
	if err := w.Step(id, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", undo.SoftDeleteStamp(now), func() error {
		_, err := w.Modify(id, func(cur *database.Book) error {
			if cur.FilePath != b.FilePath {
				return fmt.Errorf("%w: folder-book %s path changed during the retire", repairs.ErrChangedSincePlan, id)
			}
			t := true
			cur.MarkedForDeletion = &t
			cur.MarkedForDeletionAt = &now
			cur.FilePath = ""
			return nil
		})
		return err
	}); err != nil {
		return steps, fmt.Errorf("soft-delete %s: %w", id, err)
	}
	steps++
	if wasPrimary && b.VersionGroupID != nil && *b.VersionGroupID != "" {
		retirer.handOff(ctx, store, w, id, *b.VersionGroupID)
	}
	return steps, nil
}
