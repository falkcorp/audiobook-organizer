// file: internal/plugins/maintenance/folder_books_fixer.go
// version: 2.11.1
// guid: 3b8e5d17-9c2a-4f60-8e41-6a7d2c9f0b35
// last-edited: 2026-10-06

// Repairs-lane fixer "folder-books": retire book rows that are really whole
// author, series or library folders, and give each file only such a row held a
// proper book of its own.
//
// HOW THEY CAME TO BE (census 2026-09-30, 511 rows). The scanner's 3-file
// album sample (fixed in d1431f5b8) and the 2026-08-26 backfill-book-files job
// (non-recursive ReadDir) attached a whole folder's files to one book: "Gene
// Wolfe" with 1,494 files, "iTunes Media", "Prologue", a series folder.
//
// DETECTION (ported from the census's fold_final.py, tightened after the
// 2026-10-01 adversarial review). A candidate is a live book with at least
// fbMinFiles book_file rows whose files all sit under ONE shelf (config
// RootDir, an import path, or an iTunes Media/Audiobooks folder). From its
// rows:
//
//   - D: the rows' durations sum to at least 80h.
//   - P: other live books holding a strict subset of its paths, each with two
//     or more files or at least three hours, under a different title.
//   - multi-work: the files are two or more works. Evidence is folders, not
//     filenames: two or more sub-folders of the common root (a disc folder --
//     "CD1", "Disc 1 of 3", "Dune CD2", a bare number -- counts as its
//     parent), or flat files whose filename stems repeat (two stems with two
//     or more files each). Distinct stems alone are NOT evidence: a book's
//     chapter files are commonly named after their chapters ("01 - An
//     Unexpected Party.mp3").
//   - an author folder: the common root sits directly under the shelf AND its
//     name is an author's (the book's own author or any author row). A book
//     folder directly under an import path ("Dune/", "The Hobbit/") is not.
//   - scattered: the title is neither the author folder nor the root folder,
//     and at least half of the groups are named after it: ONE real book whose
//     files organize scattered (the fragment fixer's ground). Never flagged.
//
// Tiers, first match wins:
//
//   - shelf: the common root is at or above its shelf, or is an author
//     folder, and a generic title ("Prologue", "iTunes Media"), P>=2, D and
//     multi-work, or titled with its folder's name and multi-work. Neither
//     duration nor multi-work alone is enough: one long work stored flat is
//     one book, and a book's files named by point-of-view chapter ("Holden",
//     "Naomi", "Holden") repeat stems.
//   - author-copy: below an author folder, titled with the author folder's
//     name, multi-work, and D, P>=2 or 100+ files.
//   - deep: P>=3, or D and multi-work, anywhere else. Listed, never applied.
//
// A purchased box set ("The Expanse Box Set/<book>/") sits directly under an
// import path with a non-author name, so it is never a shelf; at most deep.
//
// Folder-books with the same file set are one row and retire together.
//
// APPLY (database only: nothing on disk moves, no book_file row and no book
// row is deleted). Each path a row's folder-books hold is:
//
//   - held: a live book outside the applicable folder-book set holds it with
//     a book_file row (a book's file_path counts only for a book with no rows:
//     the scanner's ownership check reads rows, scanner/file_ownership.go). A
//     deep-tier book counts. Nothing to do.
//   - held by another folder-book row: an applicable folder-book with a
//     smaller file set (ties: the lower row id) holds it too; that row owns it.
//   - missing orphan: no other holder and every member row says Missing. No
//     book is built from a missing row; the retired folder-book keeps it.
//   - orphan: grouped by work (fbGroupKey). Each group becomes ONE new book:
//     title from its folder or stem, author the folder-book's when the folder
//     is an author shelf, library_state the folder-book's, file_path the
//     group's folder (a dir group, so a rescan of that folder finds this book
//     at its path) or first file, and new book_file rows at the same paths
//     copying track, size, hash and duration but never an iTunes persistent
//     id. Books are created ONLY when every file of the group is under
//     books/itunes/**, where organize never moves a file
//     (organizer/inplace_ownership.go OutcomeFrozenITunes): a created book
//     must never be moved. Elsewhere the row is skipped (needs a new book).
//
// Then every folder-book of the row is retired. When one is primary in a
// version group, the group's heir is elected FIRST among the other members
// (versionprimary.Elect, the group never headless): the folder-book's demote
// is journaled, versionprimary.Crown makes the heir primary (and demotes the rest of the group), and
// only then is the folder-book soft-deleted (its file_path cleared). Its rows
// stay on it: the purge refuses a book that owns rows, so it is hidden (ABS,
// library, dedup, search, the fragment fixer's parent index) and never purged.
//
// SKIPPED rows (listed, never applied): deep; a work split between a proper
// book and orphans (never a second partial book); an orphan group whose title
// and author already name a live book (review it, never duplicate); a
// fragmentary group shorter than the chapter-consolidation threshold; a new
// book needed outside books/itunes/**; a primary folder-book with no heir in
// its version group; an iTunes persistent id on the folder-book itself or
// among its external ids; listening progress; Doctor Who / Big Finish /
// Torchwood (framework guard).
//
// iTunes. The owner allowed this fixer, DATABASE ONLY, on books under
// books/itunes/** (2026-10-01; ITunesDatabaseOnly opts it out of the
// framework's iTunes path guard). Row-level PIDs stay on the retired
// folder-book's rows; the created rows carry none. The iTunes writeback was
// read for what that does: the ITL rebuild keys on book-level PIDs of live
// primary books (a folder-book carrying one is refused here), the track
// provisioner runs only at import, and the merged-track cleanup -- which
// removes non-primary PIDs -- now keeps a PID that is the only track of a file
// a live primary book holds (itunes/cleanup_merged.go PathHeldSkipped).
//
// UNDO. Every write is journaled first: repair_book_create (revert
// soft-deletes the created book, refused when its rows moved or it joined a
// version group), book_file_create (record-only), book_primary_demote,
// book_primary_handoff, book_path_update, book_soft_delete.
//
// CONCURRENCY. Plan evaluates candidates and builds rows on bounded RunItems
// pools over one library read. Replan (run twice per applied row, once under
// merge.LockMergeRMW) reads only the row's books, the books holding their
// paths, those books' rows and the version groups involved.
package maintenance

import (
	"bytes"
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

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

const fbFixerID = "folder-books"

// Tiers (Row.Class).
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
	// fbSkipNoHeir: a primary folder-book whose version group has no other
	// member versionprimary could crown.
	fbSkipNoHeir = "skipped_no_primary_heir"
	// fbSkipSplitWork: a work whose files are partly held by a proper book and
	// partly orphaned. A new book would be a second, partial copy of it.
	fbSkipSplitWork = "skipped_split_work"
	// fbSkipDupTitle: a new book's title and author already name a live book.
	fbSkipDupTitle = "skipped_duplicate_title"
	// fbSkipNeedsNewBook: a new book is needed for files outside
	// books/itunes/**, where organize could later move it.
	fbSkipNeedsNewBook = "skipped_needs_new_book"
	// fbSkipUnclearWork: an orphan group's title names no work (a bare
	// number, a disc marker, a generic title) or two groups share a title:
	// the grouping cannot tell where one work ends.
	fbSkipUnclearWork = "skipped_unclear_work"
	// fbSkipRootFolder: the folder-book's folder is a library root, an import
	// root or the iTunes media root, and its orphans would become new books:
	// one row would create a book per orphan album of the whole library.
	// Such a row only applies when it creates nothing (it only retires).
	fbSkipRootFolder = "skipped_root_folder"
	// fbSkipTooLarge: the row would create more than fbMaxNewBooks books or
	// spans more than fbMaxRowFiles files, too much for one row under one
	// merge-lock hold; review it by hand.
	fbSkipTooLarge = "skipped_too_large"
	// fbSkipResolved: a soft-deleted book (a merge loser, a book a user
	// deleted, a book an earlier apply created and a revert hid) holds one
	// of the orphans: that file was resolved once, and the scanner and the
	// iTunes importer both refuse to bring it back. Never re-created here.
	fbSkipResolved = "skipped_previously_resolved"
	// fbSkipOnlyPresentCopy: a file the row would leave to another holder
	// (a proper book, or a smaller folder-book's row) has a present copy only
	// on the row's own folder-books: every other live row at the path says
	// Missing. Retiring would leave the file on disk with no live book that
	// knows it is there, and the scanner ignores a path soft-deleted books
	// hold. The row waits until a live holder's row is present (the smaller
	// folder-book's apply creates one).
	fbSkipOnlyPresentCopy = "skipped_only_present_copy"
)

// Per-row caps (fbSkipTooLarge).
const (
	fbMaxNewBooks = 25
	fbMaxRowFiles = 2000
)

// Detection thresholds (the census's).
const (
	fbMinFiles       = 5
	fbDeepHours      = 80
	fbAuthorCopyMinN = 100
	// fbProperMinSec: a single-file book counts as a proper book holding part
	// of the folder only from three hours up. At one hour (the census's
	// figure) a real book whose chapters an old scan imported one book each
	// ("Metro 2033": eleven one-hour chapter books) read as a shelf of works.
	fbProperMinSec = 3 * 3600
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
	fbDigitsRe   = regexp.MustCompile(`^\d*$`)
	fbOfNRe      = regexp.MustCompile(`(?i)\s*of\s*\d+\s*$`)
	fbBareDiscRe = regexp.MustCompile(`^\d{1,2}$`)
)

// fbIsDisc reports whether a folder name is a disc folder of a multi-disc
// work, and the name without the marker. It is metadata.DiscFolder (cd,
// disc, disk; "Dune CD1", "Dune (Disc 1)", "Dune [CD 2]", "Dune, Disc 1")
// plus a trailing "of M" ("Disc 1 of 3") and a bare one- or two-digit name
// ("1"; "1984" is a title). "Vol N" and "Part N" are never discs, at any
// depth: they are works.
func fbIsDisc(name string) (rest string, ok bool) {
	name = strings.TrimSpace(name)
	if fbBareDiscRe.MatchString(name) {
		return "", true
	}
	rest, _, ok = metadata.DiscFolder(fbOfNRe.ReplaceAllString(name, ""))
	return strings.Trim(rest, " ,-_.([)]"), ok
}

// fbUnclearTitle reports whether a group title names no work: empty, a bare
// number, a generic title, or only a disc marker.
func fbUnclearTitle(title string) bool {
	n := fbNorm(title)
	if fbDigitsRe.MatchString(n) || fbGeneric[n] {
		return true
	}
	rest, ok := fbIsDisc(title)
	return ok && fbNorm(rest) == ""
}

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
// parent), else its filename stem. dir is the group's folder for a folder
// group ("" for a stem group); label its display title.
func fbGroupKey(p, root string) (key, label, dir string) {
	d := filepath.Dir(p)
	for d != root && pathutil.IsWithin(d, root) {
		rest, ok := fbIsDisc(filepath.Base(d))
		if !ok {
			break
		}
		if filepath.Dir(d) == root {
			// A disc folder directly under root ("Frank Herbert/Dune CD1"):
			// its work is the folder's name without the marker, shared by
			// its sibling discs. No single folder holds it (dir "").
			return "disc:" + root + "|" + fbNorm(rest), rest, ""
		}
		d = filepath.Dir(d)
	}
	if d != root && pathutil.IsWithin(d, root) {
		return "dir:" + d, filepath.Base(d), d
	}
	st := fbStem(p)
	if fbNorm(st) == "" {
		st = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	}
	return "stem:" + d + "|" + fbNorm(st), st, ""
}

// fbCreatedPath is the file_path of the book a group becomes: the work's own
// folder (Dir) for a folder group; for a stem group or a disc group directly
// under root, the files' common folder when that is below root (one disc
// folder), else the first file's path, as a single-file book's. Never root:
// every stem and disc group of a row shares root, so several created books
// would share one file_path, and the single-owner book:path key
// (GetBookByFilePath) names only the last one written.
//
// The iTunes importer (internal/itunes/service/importer.go, the album
// group's bookFilePath) uses commonParentDir for a multi-track album, which
// for these groups is root. It does not re-find a created book by path: it
// matches by path (LiveBookIDsAtPath, GetBookByFilePath) and by track PID
// (bookIDsByTrackPIDs), and the PIDs stay on the retired folder-book's rows,
// so a re-import names the soft-deleted folder-book and skips with "restore
// or purge" rather than making a second book.
func fbCreatedPath(g fbGroup, root string) string {
	if g.Dir != "" {
		return g.Dir
	}
	if len(g.Files) == 0 {
		return ""
	}
	if len(g.Files) > 1 {
		paths := make([]string, len(g.Files))
		for i, nf := range g.Files {
			paths[i] = nf.Path
		}
		if d := commonDir(paths); d != root && pathutil.IsWithin(d, root) {
			return d
		}
	}
	return g.Files[0].Path
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

// fbLib is one read of (part of) the library: every live book it loaded, its
// rows, and who holds each path among them.
type fbLib struct {
	books      map[string]database.BookCore // live books only
	files      map[string][]database.BookFileCore
	holders    map[string][]string // path -> live books holding it
	sets       map[string]map[string]bool
	byVG       map[string][]string // version group -> live member ids
	authors    map[int]string
	authorNorm map[string]bool
	shelves    []string
	// titles maps fbNorm(title)+"|"+author id to live book ids, titlesAny
	// fbNorm(title) alone. Built only by a full read (Plan); a Replan carries
	// the plan's verdict in fbState and Apply re-checks under the lock.
	titles    map[string][]string
	titlesAny map[string][]string
	// dead maps a path to the soft-deleted books holding it.
	dead map[string][]string
	// byPath maps a live book's file_path to the book, rows or none: a book
	// at a group's folder is that work's book even when it holds no row
	// there (the book_file-gap shape).
	byPath map[string][]string
}

func newFBLib() *fbLib {
	return &fbLib{books: map[string]database.BookCore{}, files: map[string][]database.BookFileCore{},
		holders: map[string][]string{}, sets: map[string]map[string]bool{}, byVG: map[string][]string{},
		authors: map[int]string{}, authorNorm: map[string]bool{}, dead: map[string][]string{},
		byPath: map[string][]string{}}
}

// add registers one live book and its rows. A book's file_path makes it a
// holder only when it has no rows (a single-file book): a stale file_path on a
// book with rows owns nothing, the scanner's ownership check reads rows.
func (lib *fbLib) add(b database.BookCore, rows []database.BookFileCore) {
	if b.IsSoftDeleted() {
		seen := map[string]bool{}
		for _, r := range rows {
			if r.FilePath != "" && !seen[r.FilePath] {
				seen[r.FilePath] = true
				lib.dead[r.FilePath] = append(lib.dead[r.FilePath], b.ID)
			}
		}
		if len(seen) == 0 && b.FilePath != "" {
			lib.dead[b.FilePath] = append(lib.dead[b.FilePath], b.ID)
		}
		return
	}
	if _, done := lib.books[b.ID]; done {
		return
	}
	lib.books[b.ID] = b
	if b.FilePath != "" {
		lib.byPath[b.FilePath] = append(lib.byPath[b.FilePath], b.ID)
	}
	set := map[string]bool{}
	for _, r := range rows {
		if r.FilePath == "" {
			continue
		}
		lib.files[b.ID] = append(lib.files[b.ID], r)
		set[r.FilePath] = true
	}
	if len(set) == 0 && b.FilePath != "" {
		set[b.FilePath] = true
	}
	lib.sets[b.ID] = set
	for p := range set {
		lib.holders[p] = append(lib.holders[p], b.ID)
	}
	if b.VersionGroupID != nil && *b.VersionGroupID != "" {
		lib.byVG[*b.VersionGroupID] = append(lib.byVG[*b.VersionGroupID], b.ID)
	}
}

// drop forgets the books ids, live or soft-deleted: the books this row's own
// cut-off apply created (fbCreatedID), which a resumed Replan must not read as
// holders.
func (lib *fbLib) drop(ids map[string]bool) {
	if len(ids) == 0 {
		return
	}
	strip := func(m map[string][]string) {
		for p, hs := range m {
			kept := hs[:0]
			for _, h := range hs {
				if !ids[h] {
					kept = append(kept, h)
				}
			}
			if len(kept) == 0 {
				delete(m, p)
			} else {
				m[p] = kept
			}
		}
	}
	strip(lib.holders)
	strip(lib.dead)
	strip(lib.byVG)
	strip(lib.byPath)
	for id := range ids {
		delete(lib.books, id)
		delete(lib.files, id)
		delete(lib.sets, id)
	}
}

func (lib *fbLib) finish() {
	for p := range lib.holders {
		sort.Strings(lib.holders[p])
	}
}

// fbCommon is the authors and shelves, read once per plan and reused by the
// Replans of the apply that follows (fbCommonTTL).
type fbCommon struct {
	at         time.Time
	authors    map[int]string
	authorNorm map[string]bool
	shelves    []string
}

const fbCommonTTL = 15 * time.Minute

// common returns the authors and shelves, reading them when force is set or
// the cached copy is older than fbCommonTTL. The maps are never written after
// the read, so every lib shares them.
func (f *folderBooksFixer) common(store OpsStore, force bool) (*fbCommon, error) {
	f.commonMu.Lock()
	defer f.commonMu.Unlock()
	if !force && f.commonCache != nil && f.now().Sub(f.commonCache.at) < fbCommonTTL {
		return f.commonCache, nil
	}
	c := &fbCommon{at: f.now(), authors: map[int]string{}, authorNorm: map[string]bool{}}
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("list authors: %w", err)
	}
	for _, a := range authors {
		c.authors[a.ID] = a.Name
		if n := fbNorm(a.Name); n != "" {
			c.authorNorm[n] = true
		}
	}
	if root := config.AppConfig.RootDir; root != "" {
		c.shelves = append(c.shelves, filepath.Clean(root))
	}
	ips, err := store.GetAllImportPaths()
	if err != nil {
		return nil, fmt.Errorf("list import paths: %w", err)
	}
	for _, ip := range ips {
		if ip.Path != "" {
			c.shelves = append(c.shelves, filepath.Clean(ip.Path))
		}
	}
	f.commonCache = c
	return c, nil
}

func (lib *fbLib) use(c *fbCommon) {
	lib.authors, lib.authorNorm, lib.shelves = c.authors, c.authorNorm, c.shelves
}

// loadFull reads the whole library (Plan).
func (f *folderBooksFixer) loadFull(store OpsStore) (*fbLib, error) {
	lib := newFBLib()
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("list book files: %w", err)
	}
	rows := map[string][]database.BookFileCore{}
	for i := range cores {
		rows[cores[i].BookID] = append(rows[cores[i].BookID], cores[i])
	}
	lib.titles, lib.titlesAny = map[string][]string{}, map[string][]string{}
	listed := make(map[string]bool, len(books))
	for i := range books {
		b := books[i]
		listed[b.ID] = true
		lib.add(b, rows[b.ID])
		if !b.IsSoftDeleted() {
			k := fbTitleKey(b.Title, b.AuthorID)
			lib.titles[k] = append(lib.titles[k], b.ID)
			lib.titlesAny[fbNorm(b.Title)] = append(lib.titlesAny[fbNorm(b.Title)], b.ID)
		}
	}
	// Rows whose book the listing left out (soft-deleted, or gone): their
	// paths were resolved once (fbSkipResolved).
	for id, rs := range rows {
		if !listed[id] {
			lib.add(database.BookCore{ID: id, MarkedForDeletion: &fbTrue}, rs)
		}
	}
	lib.finish()
	c, err := f.common(store, true)
	if err != nil {
		return nil, err
	}
	lib.use(c)
	return lib, nil
}

func fbTitleKey(title string, author *int) string {
	a := 0
	if author != nil {
		a = *author
	}
	return fbNorm(title) + "|" + strconv.Itoa(a)
}

// loadPartial reads only what one row needs (Replan): its folder-books, every
// live book holding one of their paths, for each such book that is itself a
// candidate the holders of ITS paths (its P count), and the version groups of
// the folder-books. Point reads only.
func (f *folderBooksFixer) loadPartial(store OpsStore, hist FragmentRepairReader, members []string) (*fbLib, error) {
	lib := newFBLib()
	seen := map[string]bool{}
	load := func(id string) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		b, err := store.GetBookByID(id)
		if err != nil {
			return fmt.Errorf("read book %s: %w", id, err)
		}
		if b == nil {
			return nil
		}
		files, err := store.GetBookFiles(id)
		if err != nil {
			return fmt.Errorf("files of %s: %w", id, err)
		}
		rows := make([]database.BookFileCore, len(files))
		for i := range files {
			rows[i] = files[i].Core()
		}
		lib.add(b.Core(), rows)
		return nil
	}
	pathsDone := map[string]bool{}
	holdersOf := func(p string) error {
		if pathsDone[p] {
			return nil
		}
		pathsDone[p] = true
		owners, err := database.BookFileRowsAtPathStrict(hist, p)
		if err != nil {
			return fmt.Errorf("who owns %s: %w", p, err)
		}
		for i := range owners {
			if err := load(owners[i].BookID); err != nil {
				return err
			}
		}
		ids, err := store.LiveBookIDsAtPath(p)
		if err != nil {
			return fmt.Errorf("live books at %s: %w", p, err)
		}
		for _, id := range ids {
			if err := load(id); err != nil {
				return err
			}
		}
		return nil
	}
	for _, m := range members {
		if err := load(m); err != nil {
			return nil, err
		}
	}
	for _, m := range members {
		for p := range lib.sets[m] {
			if err := holdersOf(p); err != nil {
				return nil, err
			}
		}
	}
	// Second hop: a holder that is itself a candidate needs its own holders.
	hop := make([]string, 0, len(lib.books))
	for id := range lib.books {
		if !contains(members, id) && len(lib.files[id]) >= fbMinFiles {
			hop = append(hop, id)
		}
	}
	sort.Strings(hop)
	for _, id := range hop {
		for p := range lib.sets[id] {
			if err := holdersOf(p); err != nil {
				return nil, err
			}
		}
	}
	for _, m := range members {
		b, ok := lib.books[m]
		if !ok || b.VersionGroupID == nil || *b.VersionGroupID == "" {
			continue
		}
		group, err := store.GetBooksByVersionGroup(*b.VersionGroupID)
		if err != nil {
			return nil, fmt.Errorf("version group %s: %w", *b.VersionGroupID, err)
		}
		for i := range group {
			if err := load(group[i].ID); err != nil {
				return nil, err
			}
		}
	}
	lib.finish()
	c, err := f.common(store, false)
	if err != nil {
		return nil, err
	}
	lib.use(c)
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
	ID    string
	Tier  string // "" when not a folder-book
	Root  string
	Shelf string // the shelf the files sit under
	// AtOrAbove: Root is the shelf itself or above it (a library, import or
	// iTunes media root).
	AtOrAbove bool
	Author    string // the folder directly under the shelf ("" none)
	N         int
	Hours     float64
	Proper    int
	Works     int
	Evidence  []string
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
	dirs := 0
	stemFiles := map[string]int{}
	for _, p := range paths {
		k, label, dir := fbGroupKey(p, ev.Root)
		if _, ok := groups[k]; !ok && dir != "" {
			dirs++
		}
		groups[k] = label
		if dir == "" && !fbDigitsRe.MatchString(fbNorm(label)) {
			stemFiles[k]++
		}
	}
	ev.Works = len(groups)
	repeated := 0
	for _, n := range stemFiles {
		if n >= 2 {
			repeated++
		}
	}
	multi := dirs >= 2 || repeated >= 2 || (dirs >= 1 && repeated >= 1)

	atOrAbove := shelf != "" && (ev.Root == shelf || pathutil.IsWithin(shelf, ev.Root))
	ev.Shelf, ev.AtOrAbove = shelf, atOrAbove
	if shelf != "" && !atOrAbove && pathutil.IsWithin(ev.Root, shelf) {
		if rel, err := filepath.Rel(shelf, ev.Root); err == nil {
			ev.Author = strings.Split(rel, string(filepath.Separator))[0]
		}
	}
	a := fbNorm(ev.Author)
	bookAuthor := ""
	if b.AuthorID != nil {
		bookAuthor = fbNorm(lib.authors[*b.AuthorID])
	}
	authorFolder := ev.Author != "" && filepath.Dir(ev.Root) == shelf && a != "" &&
		(a == bookAuthor || lib.authorNorm[a])
	base := fbNorm(filepath.Base(ev.Root))
	authorNamed := ev.Author != "" && t == a
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
	addEv := func(s string, on bool) {
		if on {
			ev.Evidence = append(ev.Evidence, s)
		}
	}
	addEv(fmt.Sprintf("%.0fh of audio (>= %dh)", ev.Hours, fbDeepHours), deep)
	addEv(fmt.Sprintf("%d other books hold strict subsets of its files", ev.Proper), ev.Proper >= 2)
	addEv("generic title", generic)
	addEv(fmt.Sprintf("files are %d works (%d folders)", ev.Works, dirs), multi)
	addEv(fmt.Sprintf("its folder %q is an author's", ev.Author), authorFolder)
	addEv("the folder is a library root or above one", atOrAbove)
	switch {
	case (atOrAbove || authorFolder) && (generic || ev.Proper >= 2 || (deep && multi) || (t == base && multi)):
		ev.Tier = fbTierShelf
	case !atOrAbove && !authorFolder && authorNamed && multi && (deep || ev.Proper >= 2 || ev.N >= fbAuthorCopyMinN):
		ev.Tier = fbTierAuthorCopy
		addEv(fmt.Sprintf("titled with its author folder %q, %d files", ev.Author, ev.N), true)
	case ev.Proper >= 3 || (deep && multi):
		ev.Tier = fbTierDeep
	}
	return ev
}

func fbApplicableTier(t string) bool { return t == fbTierShelf || t == fbTierAuthorCopy }

// ---- fixer --------------------------------------------------------------------

var fbTrue = true

// fbCrashHook, when set (tests only), is called at fixed points of
// applyGroup: stage "book" after CreateBook (n 0); "row" after the n-th
// CreateBookFile; "reindex" after the n-th CreateBookFile, before that new
// row's source row takes the path key back (n counts the group's files, as
// for "row"); "claim" before the n-th created row's re-index after the
// un-hide (n counts the created book's rows);
// an error it returns stops the apply there, as a server stop would.
var fbCrashHook func(stage string, n int) error

type folderBooksFixer struct {
	p *Plugin
	// now and newID replace time.Now and the ULID mint in tests.
	now   func() time.Time
	newID func() string

	commonMu    sync.Mutex
	commonCache *fbCommon

	// titleMu guards the title indexes (titleIDs). It never contends in
	// practice: every apply that reads them holds the merge lock, which
	// serializes them; the mutex only makes the cache safe on its own terms.
	titleMu  sync.Mutex
	titleIdx map[string]*fbTitleCache // by key name (titleIDs)
}

// fbTitleCache is one title index, the library generation it is current at,
// and each indexed book's key (to move a book whose title changed).
type fbTitleCache struct {
	gen   uint64
	idx   fbTitleIndex
	keyOf map[string]string
}

// fbTitleIndex maps a title key (fbNorm(title) for folder-books) to every
// live book with that key.
type fbTitleIndex map[string][]string

// titleIndex is titleIDs keyed by fbNorm, for folder-books' duplicate check.
func (f *folderBooksFixer) titleIndex(store OpsStore, k string) ([]string, error) {
	return f.titleIDs(store, "fbNorm", fbNorm, k)
}

// titleIDs returns the live books whose key(title) is k, from an index of the
// whole live library cached under name and kept current with the library
// generation (database.LibraryGenerationOf: bumped by every book create,
// update and delete). It returns a copy; the index itself never leaves the
// lock.
//
// COST. The index is built once from every book (GetAllBooksCoreComplete:
// it authorizes an irreversible create, so a memdb known to be missing rows
// must not answer it and the store falls through to the authoritative Pebble
// scan). After that it is caught up, not rebuilt: the store logs which book
// each generation bump was for (database.BooksChangedSinceOf), and only those
// books are re-read (GetBookByID) and moved to their current key. In a bulk
// apply every applied row bumps the generation, and the old design rebuilt
// from every book on each following row, under the global merge lock; now a
// row pays for the books written since the previous one. A full rebuild
// happens only when the store cannot list the changes completely (no change
// log, a generation bumped without a record, or more writes than the log
// holds) — the same answer, by the slow road.
//
// FRESHNESS. The store bumps the generation only AFTER the memdb
// write-through (pebble_store.go writeThroughThenBump), and the bump and its
// log record are one step, so a book written before the generation a caller
// catches up to is re-read in its written state. A write landing during the
// catch-up is logged past the recorded generation and re-read next time. Under
// the merge lock every merge-lock holder (fragment consolidation, split-book
// merge, fs-regroup retitles) has finished; a writer that does not take the
// merge lock (scanner, importer) can still race the check itself, as it could
// race any check, since the lock never excluded it. Callers re-check each id
// against the book as it is now.
func (f *folderBooksFixer) titleIDs(store OpsStore, name string, key func(string) string, k string) ([]string, error) {
	gen, tracked := database.LibraryGenerationOf(store)
	f.titleMu.Lock()
	defer f.titleMu.Unlock()
	c := f.titleIdx[name]
	if c != nil && tracked && c.gen != gen.Value() {
		if ids, upTo, ok := database.BooksChangedSinceOf(store, c.gen); ok {
			for _, id := range ids {
				b, err := store.GetBookByID(id)
				if err != nil {
					// One unreadable book (a corrupt sidecar, say) must not
					// wedge the cache: returning here would leave c.gen
					// behind, so every later call would re-read the same
					// book and fail the same way. Drop the half-caught-up
					// cache and take the full rebuild, which lists books
					// without that per-book read.
					dcLog.Warn("folder-books: re-read of book %s for the %s title index failed, rebuilding it: %s",
						logger.SanitizeLogValue(id), name, logger.SanitizeLogValue(err.Error()))
					delete(f.titleIdx, name)
					c = nil
					break
				}
				c.move(id, b, key)
			}
			if c != nil {
				c.gen = upTo
			}
		} else {
			c = nil
		}
	}
	if c == nil || !tracked {
		cur := gen.Value() // read BEFORE the build: a write during it moves past cur
		books, err := store.GetAllBooksCoreComplete(0, 0)
		if err != nil {
			return nil, fmt.Errorf("list books for the duplicate check: %w", err)
		}
		c = &fbTitleCache{gen: cur, idx: make(fbTitleIndex, len(books)), keyOf: make(map[string]string, len(books))}
		for i := range books {
			if books[i].IsSoftDeleted() {
				continue
			}
			kk := key(books[i].Title)
			c.idx[kk] = append(c.idx[kk], books[i].ID)
			c.keyOf[books[i].ID] = kk
		}
		if tracked {
			if f.titleIdx == nil {
				f.titleIdx = map[string]*fbTitleCache{}
			}
			f.titleIdx[name] = c
		}
	}
	return append([]string(nil), c.idx[k]...), nil
}

// move re-files book id under its current key: out of its old key, and back
// in unless it is gone (b nil) or hidden.
func (c *fbTitleCache) move(id string, b *database.Book, key func(string) string) {
	if old, ok := c.keyOf[id]; ok {
		ids := c.idx[old]
		for i, x := range ids {
			if x == id {
				ids = append(ids[:i:i], ids[i+1:]...)
				break
			}
		}
		if len(ids) == 0 {
			delete(c.idx, old)
		} else {
			c.idx[old] = ids
		}
		delete(c.keyOf, id)
	}
	if b == nil || b.IsSoftDeleted() {
		return
	}
	kk := key(b.Title)
	c.idx[kk] = append(c.idx[kk], id)
	c.keyOf[id] = kk
}

func newFolderBooksFixer(p *Plugin) *folderBooksFixer {
	return &folderBooksFixer{p: p, now: time.Now, newID: func() string { return ulid.Make().String() }}
}

var (
	_ repairs.Fixer              = (*folderBooksFixer)(nil)
	_ repairs.ITunesDatabaseOnly = (*folderBooksFixer)(nil)
)

func (f *folderBooksFixer) ID() string    { return fbFixerID }
func (f *folderBooksFixer) Title() string { return "Folder-sized books" }
func (f *folderBooksFixer) Description() string {
	return "Books that are really a whole author, series or library folder (an old scan and the 2026-08-26 " +
		"book-file backfill attached every file in the folder). Apply gives each file no proper book holds a new " +
		"book of its own (iTunes files only, grouped by folder or filename stem, same paths, nothing moved), then " +
		"hides the folder-book (soft-deleted with its file rows kept; primacy handed to the real book first). " +
		"Database only, including for iTunes books. Deep-tier, split, fragmentary and Doctor Who / Big Finish / " +
		"Torchwood rows are listed only. Every step is undoable from the apply operation."
}

// ITunesDatabaseOnly: the owner cleared this fixer (2026-10-01) to write the
// database rows of books under books/itunes/**. It never moves, renames or
// deletes a file and never changes an iTunes persistent id.
func (f *folderBooksFixer) ITunesDatabaseOnly() bool { return true }

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

// fbState is stored with the plan: the row's folder-books, and the plan-time
// duplicate-title verdict (only a full read can make it).
type fbState struct {
	Members   []string `json:"members"`
	DupTitles []string `json:"dup_titles,omitempty"`
}

// fbNewFile is one book_file row a group copies.
type fbNewFile struct {
	SrcBook, SrcRow, Path string
	Track                 int
}

// fbGroup is one orphan work. Path is the created book's file_path
// (fbCreatedPath).
type fbGroup struct {
	Key, Title, Dir, Path string
	AuthorID              *int
	Files                 []fbNewFile
	Secs                  int
}

// fbPlan is Row.Detail.
type fbPlan struct {
	Members []string // the row's folder-books
	State   string   // library_state for created books
	Groups  []fbGroup
}

// Plan evaluates every candidate on a bounded pool and builds one row per
// set of folder-books sharing a file set, also on a bounded pool.
func (f *folderBooksFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	params, err := decodeFBParams(raw)
	if err != nil {
		return nil, err
	}
	store, _, err := f.store()
	if err != nil {
		return nil, err
	}
	lib, err := f.loadFull(store)
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
	var done atomic.Int64
	// Each worker writes only evals[i]; lib is read-only here.
	if err := registry.RunItems(ctx, rep, fbIndexes(len(ids)), func(_ context.Context, i int) error {
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
	var groups [][]string
	for _, members := range fbDupGroups(lib, ev) {
		if only == nil || anyIn(members, only) {
			groups = append(groups, members)
		}
	}
	rows := make([]repairs.Row, len(groups))
	var built atomic.Int64
	// Each worker writes only rows[i]; lib and ev are read-only, and
	// buildRow's own reads (external ids, listening progress) are point reads.
	if err := registry.RunItems(ctx, rep, fbIndexes(len(groups)), func(_ context.Context, i int) error {
		defer built.Add(1)
		rows[i] = f.buildRow(lib, store, ev, groups[i], nil)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Folder-book rows %d/%d", built.Load(), total) },
	}); err != nil {
		return nil, fmt.Errorf("%s: rows: %w", fbFixerID, err)
	}
	return rows, nil
}

func fbIndexes(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
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

func fbSetPaths(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// fbCreatedID is the id of the book row rowID creates for group key: a ULID
// with the lead folder-book's timestamp and entropy from the row and key, so
// a resumed apply finds the book its cut-off run created (and a Replan can
// leave it out) instead of minting a second one.
func fbCreatedID(rowID, leadID, key string) string {
	var ms uint64
	if u, err := ulid.Parse(leadID); err == nil {
		ms = u.Time()
	}
	h := sha256.Sum256([]byte(rowID + "\x00" + key))
	id, err := ulid.New(ms, bytes.NewReader(h[:]))
	if err != nil {
		return "fb" + hex.EncodeToString(h[:12]) // unreachable: 32 bytes of entropy
	}
	return id.String()
}

// ownsBefore reports whether folder-book row a (file count na, first id ida)
// owns a shared orphan before row b.
func ownsBefore(na int, ida string, nb int, idb string) bool {
	if na != nb {
		return na < nb
	}
	return ida < idb
}

func fbPrimary(b database.BookCore) bool { return b.IsPrimaryVersion == nil || *b.IsPrimaryVersion }

// buildRow classifies every path of the row and decides what apply writes.
// carried is the plan's state on a Replan (nil on Plan, which decides the
// duplicate-title verdict from lib.titles).
func (f *folderBooksFixer) buildRow(lib *fbLib, store OpsStore, ev map[string]fbEval, members []string, carried *fbState) repairs.Row {
	lead := ev[members[0]]
	head := lib.books[members[0]]
	set := lib.sets[members[0]]
	row := repairs.Row{RowID: fbRowID(members), BookIDs: append([]string(nil), members...), Title: head.Title,
		Class: lead.Tier, Risk: repairs.RiskReview}
	if head.AuthorID != nil {
		row.Author = lib.authors[*head.AuthorID]
	}
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

	// The row's copy of each path: a member's row, a present one first.
	src := map[string]database.BookFileCore{}
	for _, m := range members {
		for _, r := range lib.files[m] {
			if cur, ok := src[r.FilePath]; !ok || (cur.Missing && !r.Missing) {
				src[r.FilePath] = r
			}
		}
	}
	// presentAt is a non-member live holder's present row at p: a row there
	// not marked Missing, or a book with no rows holding p by file_path
	// (zero-row single-file book, whose presence no row records).
	presentAt := func(h, p string) (database.BookFileCore, bool) {
		if len(lib.files[h]) == 0 {
			return database.BookFileCore{BookID: h, FilePath: p}, lib.sets[h][p]
		}
		for _, r := range lib.files[h] {
			if r.FilePath == p && !r.Missing {
				return r, true
			}
		}
		return database.BookFileCore{}, false
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var held, byFolder, missingOrphans int
	var resolved, onlyOurs []string
	type orphan struct {
		row           database.BookFileCore
		key, lbl, dir string
	}
	var orphans []orphan
	keyHeld := map[string]bool{} // a group key with a path a proper book holds
	fp := []string{"tier=" + lead.Tier, "members=" + strings.Join(members, ",")}
	for _, p := range paths {
		k, lbl, dir := fbGroupKey(p, lead.Root)
		cls := "orphan"
		for _, h := range lib.holders[p] {
			if isMember[h] {
				continue
			}
			he, flagged := ev[h]
			if !flagged || !fbApplicableTier(he.Tier) {
				cls = "held"
				keyHeld[k] = true
				break
			}
			if ownsBefore(len(lib.sets[h]), h, len(set), members[0]) {
				cls = "folder"
			}
		}
		// Missing is per row, so one copy's flag decides nothing. A path is
		// missing only when EVERY live row at it says so; while another live
		// holder (a larger folder-book) has a present row, that row is the
		// copy a new book takes. A path left to another holder (held, folder)
		// needs a present copy on a holder that outlives this row: a proper
		// book, a deep-tier one, or the smaller folder-book that owns the
		// path (it keeps it, or builds the new book from a present row).
		// Otherwise retiring this row strands the file (fbSkipOnlyPresentCopy):
		// the scanner ignores a path only soft-deleted books hold.
		var other database.BookFileCore
		anyPresent, keeperPresent := false, false
		for _, h := range lib.holders[p] {
			if isMember[h] {
				continue
			}
			r, ok := presentAt(h, p)
			if !ok {
				continue
			}
			anyPresent = true
			if other.ID == "" && r.ID != "" {
				other = r
			}
			// The keeper restriction (a present copy on a LARGER folder-book
			// does not count) is conservative, not load-bearing: that larger
			// row runs this same check on its own copy. It keeps this row's
			// verdict from leaning on a holder that will retire too.
			he, flagged := ev[h]
			if !flagged || !fbApplicableTier(he.Tier) || ownsBefore(len(lib.sets[h]), h, len(set), members[0]) {
				keeperPresent = true
			}
		}
		allMissing := src[p].Missing && !anyPresent
		if cls == "orphan" && allMissing {
			cls = "missing"
		}
		if cls == "orphan" && src[p].Missing && other.ID != "" {
			src[p] = other
		}
		// Only a present copy on the row's own folder-books can be stranded:
		// when theirs is Missing too, retiring them loses nothing (a larger
		// folder-book's present copy is its own row's concern).
		if (cls == "held" || cls == "folder") && !keeperPresent && !src[p].Missing {
			var waiting []string
			for _, h := range lib.holders[p] {
				if !isMember[h] {
					waiting = append(waiting, h)
				}
			}
			onlyOurs = append(onlyOurs, fmt.Sprintf("%s (held by book %s, with no present copy on a holder that outlives this row)", p,
				strings.Join(fbFirst(waiting, 3), ", ")))
			fp = append(fp, "p:"+p)
		}
		if cls == "orphan" {
			for _, h := range lib.dead[p] {
				if !isMember[h] {
					resolved = append(resolved, fmt.Sprintf("%s (book %s)", p, h))
					fp = append(fp, "r:"+p+"|"+h)
					break
				}
			}
		}
		switch cls {
		case "held":
			held++
		case "folder":
			byFolder++
		case "missing":
			missingOrphans++
		default:
			orphans = append(orphans, orphan{row: src[p], key: k, lbl: lbl, dir: dir})
		}
		// held and held-by-folder fingerprint alike: a sibling row applying
		// moves a path from one to the other without changing this row.
		switch cls {
		case "orphan":
			fp = append(fp, "o:"+p+"|"+k)
		case "missing":
			fp = append(fp, "x:"+p)
		default:
			fp = append(fp, "h:"+p)
		}
	}

	plan := &fbPlan{State: "imported", Members: append([]string(nil), members...)}
	if head.LibraryState != nil && *head.LibraryState != "" {
		plan.State = *head.LibraryState
	}
	for _, m := range members {
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
			plan.Groups = append(plan.Groups, fbGroup{Key: o.key, Title: o.lbl, Dir: o.dir, AuthorID: authorID})
		}
		g := &plan.Groups[i]
		g.Files = append(g.Files, fbNewFile{SrcBook: o.row.BookID, SrcRow: o.row.ID, Path: o.row.FilePath, Track: o.row.TrackNumber})
		g.Secs += o.row.Duration
	}
	var fragmentary, split, outside, unclear []string
	seenTitle := map[string]bool{}
	dup := []string{}
	if carried != nil {
		dup = carried.DupTitles
	}
	for i := range plan.Groups {
		g := &plan.Groups[i]
		sort.SliceStable(g.Files, func(a, b int) bool {
			if g.Files[a].Track != g.Files[b].Track {
				return g.Files[a].Track < g.Files[b].Track
			}
			return g.Files[a].Path < g.Files[b].Path
		})
		g.Path = fbCreatedPath(*g, lead.Root)
		name := fmt.Sprintf("%q (%d file(s))", g.Title, len(g.Files))
		if keyHeld[g.Key] {
			split = append(split, name)
		}
		underShelf := g.Dir != "" && filepath.Dir(g.Dir) == lead.Shelf
		if t := fbNorm(g.Title); fbUnclearTitle(g.Title) || seenTitle[t] || underShelf || lib.authorNorm[t] {
			unclear = append(unclear, name)
		} else {
			seenTitle[t] = true
		}
		if g.Secs < importChapterSec() {
			fragmentary = append(fragmentary, fmt.Sprintf("%q (%d file(s), %ds)", g.Title, len(g.Files), g.Secs))
		}
		for _, nf := range g.Files {
			if !pathutil.UnderFrozenITunesTree(nf.Path) {
				outside = append(outside, name)
				break
			}
		}
		if carried == nil && lib.titles != nil {
			// An authorless group matches its title under any author; an
			// authored one its own author's and an authorless book's (the
			// match is symmetric). A live book at the group's folder or at
			// the created file_path is the work's book whatever its title.
			var same []string
			if g.AuthorID == nil {
				same = lib.titlesAny[fbNorm(g.Title)]
			} else {
				same = append(append(same, lib.titles[fbTitleKey(g.Title, g.AuthorID)]...),
					lib.titles[fbTitleKey(g.Title, nil)]...)
			}
			for _, id := range same {
				if !isMember[id] {
					dup = append(dup, fmt.Sprintf("%q is already book %s", g.Title, id))
					break
				}
			}
			for _, at := range []string{g.Dir, g.Path} {
				if at == "" {
					continue
				}
				for _, id := range lib.byPath[at] {
					if !isMember[id] {
						dup = append(dup, fmt.Sprintf("%q: book %s already sits at %s", g.Title, id, at))
						break
					}
				}
			}
		}
		fp = append(fp, fmt.Sprintf("g:%s|%s|%s|%s|%d", g.Key, g.Title, g.Dir, g.Path, len(g.Files)))
		if a := g.AuthorID; a != nil {
			fp = append(fp, "a:"+strconv.Itoa(*a))
		}
	}
	st, err := json.Marshal(fbState{Members: members, DupTitles: dup})
	if err != nil {
		row.Skipped, row.SkipReason = fbSkipUnreadable, fmt.Sprintf("encode the row's state: %v", err)
		// Unique per row like every other fingerprint, never a shared constant.
		row.Fingerprint = "unencodable:" + fbRowID(members)
		return row
	}
	row.State = st
	row.Proposed = map[string]string{"held": strconv.Itoa(held), "held_by_folder_row": strconv.Itoa(byFolder),
		"missing_orphans": strconv.Itoa(missingOrphans), "new_books": strconv.Itoa(len(plan.Groups)),
		"retire": strings.Join(members, ",")}
	for i, g := range plan.Groups {
		if i == 20 {
			row.Evidence = append(row.Evidence, fmt.Sprintf("... and %d more new books", len(plan.Groups)-20))
			break
		}
		row.Evidence = append(row.Evidence, fmt.Sprintf("new book %q: %d file(s)", g.Title, len(g.Files)))
	}
	row.Reason = fmt.Sprintf("%s folder-book over %s: %d files, %d already held by proper books, %d held by a "+
		"smaller folder-book, %d missing with no other holder (kept on the retired book), %d orphan file(s) into %d "+
		"new book(s)", lead.Tier, lead.Root, len(set), held, byFolder, missingOrphans, len(orphans), len(plan.Groups))
	row.Detail = plan
	row.Fingerprint = fragFingerprint(fp...)
	// Every live member of each version group a folder-book of the row is in:
	// Crown writes them, so the guards and the apply partitioning see them.
	inRow := map[string]bool{}
	for _, id := range row.BookIDs {
		inRow[id] = true
	}
	for _, m := range members {
		if vg := lib.books[m].VersionGroupID; vg != nil && *vg != "" {
			for _, id := range lib.byVG[*vg] {
				if !inRow[id] {
					inRow[id] = true
					row.BookIDs = append(row.BookIDs, id)
				}
			}
		}
	}
	sort.Strings(row.BookIDs)

	switch {
	case !fbApplicableTier(lead.Tier):
		row.Skipped, row.SkipReason = fbSkipDeep, "deep tier: listed for review only"
	case lead.AtOrAbove && len(plan.Groups) > 0:
		row.Skipped = fbSkipRootFolder
		row.SkipReason = fmt.Sprintf("the folder-book's folder %s is a library, import or iTunes media root; its %d "+
			"orphan group(s) would become books from across the whole root", lead.Root, len(plan.Groups))
	case len(plan.Groups) > fbMaxNewBooks || len(set) > fbMaxRowFiles:
		row.Skipped = fbSkipTooLarge
		row.SkipReason = fmt.Sprintf("%d new book(s) over %d files exceeds the per-row cap (%d books, %d files)",
			len(plan.Groups), len(set), fbMaxNewBooks, fbMaxRowFiles)
	case len(onlyOurs) > 0:
		row.Skipped = fbSkipOnlyPresentCopy
		row.SkipReason = fmt.Sprintf("%d file(s) another live book holds have a present copy only on this row's "+
			"folder-books (every other live row there says Missing); retiring would strand them: %s",
			len(onlyOurs), strings.Join(fbFirst(onlyOurs, 5), "; "))
	case len(resolved) > 0:
		row.Skipped = fbSkipResolved
		row.SkipReason = fmt.Sprintf("%d orphan file(s) are held by a soft-deleted book (merged away, deleted, or "+
			"created and reverted): %s", len(resolved), strings.Join(fbFirst(resolved, 5), "; "))
	case len(unclear) > 0:
		row.Skipped = fbSkipUnclearWork
		row.SkipReason = fmt.Sprintf("%d orphan group(s) are titled with no work's name (a number, a disc marker, a "+
			"generic title) or repeat another group's title: %s", len(unclear), strings.Join(fbFirst(unclear, 5), "; "))
	case len(split) > 0:
		row.Skipped = fbSkipSplitWork
		row.SkipReason = fmt.Sprintf("%d work(s) are partly held by a proper book and partly orphaned; a new book "+
			"would be a second, partial copy: %s", len(split), strings.Join(fbFirst(split, 5), "; "))
	case len(dup) > 0:
		row.Skipped = fbSkipDupTitle
		row.SkipReason = fmt.Sprintf("a new book's title and author already name a live book: %s",
			strings.Join(fbFirst(dup, 5), "; "))
	case len(fragmentary) > 0:
		row.Skipped = fbSkipFragmentary
		row.SkipReason = fmt.Sprintf("%d orphan group(s) shorter than the chapter-consolidation threshold would become "+
			"chapter-sized books: %s", len(fragmentary), strings.Join(fbFirst(fragmentary, 5), "; "))
	case len(outside) > 0:
		row.Skipped = fbSkipNeedsNewBook
		row.SkipReason = fmt.Sprintf("%d new book(s) needed for files outside books/itunes/**, where organize could "+
			"later move a created book: %s", len(outside), strings.Join(fbFirst(outside, 5), "; "))
	default:
		if kind, why := f.refusal(lib, store, members); kind != "" {
			row.Skipped, row.SkipReason = kind, why
		} else if why := lib.noHeir(members); why != "" {
			row.Skipped, row.SkipReason = fbSkipNoHeir, why
		}
	}
	return row
}

// noHeir names a primary member whose version group has other live members
// but none versionprimary could crown (its rule, rank.go ineligibleReason:
// organized, at least one active row, every active row under the library
// root; apply re-checks with the full rule, on-disk checks included, before
// writing anything). "" when every primary member has an heir.
func (lib *fbLib) noHeir(members []string) string {
	isMember := map[string]bool{}
	for _, m := range members {
		isMember[m] = true
	}
	root := filepath.Clean(config.AppConfig.RootDir)
	for _, m := range members {
		b := lib.books[m]
		if !fbPrimary(b) || b.VersionGroupID == nil || *b.VersionGroupID == "" {
			continue
		}
		others, heir := 0, false
		for _, id := range lib.byVG[*b.VersionGroupID] {
			if isMember[id] {
				continue
			}
			others++
			o := lib.books[id]
			if o.LibraryState == nil || *o.LibraryState != "organized" {
				continue
			}
			active, under := 0, true
			for _, r := range lib.files[id] {
				if r.Missing {
					continue
				}
				active++
				if config.AppConfig.RootDir == "" || !pathutil.IsWithin(r.FilePath, root) {
					under = false
				}
			}
			if active > 0 && under {
				heir = true
				break
			}
		}
		if others > 0 && !heir {
			return fmt.Sprintf("folder-book %s is primary in version group %s and none of its %d other member(s) "+
				"can be crowned (organized, every file under the library root); retiring it would leave the real "+
				"book without a primary", m, *b.VersionGroupID, others)
		}
	}
	return ""
}

func fbFirst(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("and %d more", len(s)-n))
	}
	return s
}

// refusal checks what holds a row back: an iTunes persistent id on a
// folder-book itself or among its external ids (the ITL rebuild keys on
// book-level ids: retiring such a book would drop its track), or anyone's
// listening progress on it. Row-level ids are allowed: they stay on the
// retired book's rows and the merged-track cleanup keeps a file's only track.
func (f *folderBooksFixer) refusal(lib *fbLib, store OpsStore, members []string) (kind, why string) {
	um := f.p.deps.MergeUserStateStore()
	for _, m := range members {
		b := lib.books[m]
		if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
			return fbSkipITunesID, fmt.Sprintf("folder-book %s carries iTunes id %s", m, *b.ITunesPersistentID)
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

// Replan re-reads only the row's neighbourhood (loadPartial) and rebuilds it.
func (f *folderBooksFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	var st fbState
	if err := json.Unmarshal(planned.State, &st); err != nil || len(st.Members) == 0 {
		return repairs.Row{}, fmt.Errorf("%s: row %s carries no members", fbFixerID, planned.RowID)
	}
	store, hist, err := f.store()
	if err != nil {
		return repairs.Row{}, err
	}
	lib, err := f.loadPartial(store, hist, st.Members)
	if err != nil {
		return repairs.Row{}, err
	}
	// The books this row's own cut-off apply created are not holders: the
	// resumed apply extends them (applyGroup), so the row must re-plan as it
	// was planned.
	if set := lib.sets[st.Members[0]]; set != nil {
		root := commonDir(fbSetPaths(set))
		ours := map[string]bool{}
		for p := range set {
			k, _, _ := fbGroupKey(p, root)
			ours[fbCreatedID(planned.RowID, st.Members[0], k)] = true
		}
		lib.drop(ours)
	}
	gone := func(why string) repairs.Row {
		r := planned
		r.Fingerprint, r.Detail = "changed:"+why, nil
		r.Skipped, r.SkipReason = fbSkipUnreadable, why
		return r
	}
	ev := map[string]fbEval{}
	for id := range lib.books {
		if e := lib.evaluate(id); e.Tier != "" {
			ev[id] = e
		}
	}
	key := ""
	for _, m := range st.Members {
		if _, live := lib.books[m]; !live {
			return gone(fmt.Sprintf("folder-book %s is no longer live", m)), nil
		}
		if _, ok := ev[m]; !ok {
			return gone(fmt.Sprintf("book %s is no longer a folder-book", m)), nil
		}
		k := fbSetKey(lib.sets[m])
		if key == "" {
			key = k
		} else if k != key {
			return gone("the folder-books no longer share one file set"), nil
		}
	}
	members := append([]string(nil), st.Members...)
	for id := range ev {
		if !contains(members, id) && fbSetKey(lib.sets[id]) == key {
			members = append(members, id)
		}
	}
	sort.Strings(members)
	if fbRowID(members) != planned.RowID {
		return gone("another folder-book with this file set now sorts first"), nil
	}
	return f.buildRow(lib, store, ev, members, &st), nil
}

// Apply writes one row under the merge lock: elect each version group's heir
// (refusing the row before any write when one cannot be elected), create each
// orphan group's book, hand primacy to the heir, then retire every
// folder-book.
func (f *folderBooksFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	store, _, err := f.store()
	if err != nil {
		return err
	}
	// The lock is process-wide: an outside holder (a dedup merge) may keep
	// it past the scan stand-down lease, so the wait renews the lease and
	// refuses the row (ErrStandDownLost, nothing written) if it lapses.
	if err := w.LockWaiting(ctx, "the merge lock", merge.LockMergeRMW, merge.UnlockMergeRMW); err != nil {
		return err
	}
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
	for _, g := range plan.Groups {
		if why, err := f.dupNow(store, plan.Members, fbCreatedID(locked.RowID, plan.Members[0], g.Key), g); err != nil {
			return err
		} else if why != "" {
			return fmt.Errorf("%w: under the merge lock: %s", repairs.ErrChangedSincePlan, why)
		}
	}
	heirs, err := f.electHeirs(ctx, store, plan.Members, nil)
	if err != nil {
		return err // nothing written yet
	}
	var steps int
	partial := func(err error) error {
		if steps == 0 {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %w", repairs.ErrPartiallyApplied, steps, err)
	}
	for _, g := range plan.Groups {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		did, err := f.applyGroup(store, w, plan, locked.RowID, g)
		steps += did
		if err != nil {
			return partial(err)
		}
	}
	gids := make([]string, 0, len(heirs))
	for gid := range heirs {
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	for _, gid := range gids {
		did, err := f.handOff(store, w, plan.Members, gid, heirs[gid])
		steps += did
		if err != nil {
			return partial(err)
		}
	}
	for _, m := range plan.Members {
		if err := ctx.Err(); err != nil {
			return partial(err)
		}
		did, err := f.retire(store, w, m)
		steps += did
		if err != nil {
			return partial(err)
		}
	}
	return nil
}

// electHeirs elects, for every version group in which a folder-book of the
// row is primary, the member that takes over: versionprimary's own election
// (Elect) over the group's OTHER members, with their on-disk signals. A group
// with no other live member needs no heir. A held election refuses the row.
// notHeir, when set, keeps a member out of the election (the duplicate-copies
// fixer never crowns an iTunes copy); nil lets every other member stand.
func (f *folderBooksFixer) electHeirs(ctx context.Context, store OpsStore, members []string, notHeir func(id string) bool) (map[string]string, error) {
	vps := f.p.deps.VersionPrimaryStore()
	heirs := map[string]string{}
	for _, m := range members {
		b, err := store.GetBookByID(m)
		if err != nil || b == nil {
			return nil, fmt.Errorf("%w: read folder-book %s: %v", repairs.ErrChangedSincePlan, m, err)
		}
		primary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
		if !primary || b.VersionGroupID == nil || *b.VersionGroupID == "" {
			continue
		}
		gid := *b.VersionGroupID
		if _, done := heirs[gid]; done {
			continue
		}
		group, err := store.GetBooksByVersionGroup(gid)
		if err != nil {
			return nil, fmt.Errorf("read version group %s: %w", gid, err)
		}
		var others []database.Book
		for i := range group {
			if !contains(members, group[i].ID) && !group[i].IsSoftDeleted() && (notHeir == nil || !notHeir(group[i].ID)) {
				others = append(others, group[i])
			}
		}
		if len(others) == 0 {
			continue
		}
		if vps == nil {
			return nil, errors.New("version-primary store unavailable: cannot elect the group's heir")
		}
		loader := versionprimary.Loader{Files: store, Chapters: vps, RootDir: config.AppConfig.RootDir}
		alive := func(id string) bool { return !contains(members, id) }
		ms, err := loader.LoadMembers(ctx, others, alive)
		if err != nil {
			return nil, fmt.Errorf("signals of version group %s: %w", gid, err)
		}
		d := versionprimary.Elect(ms)
		if d.Kind == versionprimary.DecisionHeld || d.WinnerID == "" {
			return nil, fmt.Errorf("%w: version group %s has no member to crown in place of folder-book %s (%s)",
				repairs.ErrChangedSincePlan, gid, m, d.Reason)
		}
		heirs[gid] = d.WinnerID
	}
	return heirs, nil
}

// handOff demotes every primary folder-book of the row in group gid and
// crowns heir, BEFORE any of them is soft-deleted, so the group is never
// without a primary. The demotes are journaled (OldValue "true") before Crown
// writes and the hand-off notes after: the revert of a demote re-crowns the
// folder-book (versionprimary.Crown), which also demotes the heir.
func (f *folderBooksFixer) handOff(store OpsStore, w *repairs.Writer, members []string, gid, heir string) (int, error) {
	vps := f.p.deps.VersionPrimaryStore()
	if vps == nil {
		return 0, errors.New("version-primary store unavailable")
	}
	var demoted []string
	for _, m := range members {
		b, err := store.GetBookByID(m)
		if err != nil || b == nil {
			return 0, fmt.Errorf("read folder-book %s: %v", m, err)
		}
		if b.VersionGroupID == nil || *b.VersionGroupID != gid || (b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion) {
			continue
		}
		if err := w.Journal(m, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", "true", "false"); err != nil {
			return 0, err
		}
		demoted = append(demoted, m)
	}
	if len(demoted) == 0 {
		return 0, nil
	}
	w.Touch()
	res, err := versionprimary.Crown(fragEnsureStore{OpsStore: store, chapters: vps}, gid, heir)
	if err != nil {
		return 0, fmt.Errorf("crown %s in group %s: %w", heir, gid, err)
	}
	if res.PrimaryID != heir {
		return 1, fmt.Errorf("crown in group %s left %q primary, not %s", gid, res.PrimaryID, heir)
	}
	// The hand-off note is evidence the group's flags changed, so it is
	// written only once Crown has (undo.ChangeTypeBookPrimaryHandoff). It
	// says whether Crown wrote the heir's true or found it already there
	// (undo.HandOffNoteValue): the revert never demotes a heir whose true
	// predates the operation.
	note := undo.HandOffNoteValue(heir, res.WrotePrimary())
	for _, m := range demoted {
		if err := w.Journal(m, undo.ChangeTypeBookPrimaryHandoff, "version_group_id", note, gid); err != nil {
			return 1, err
		}
	}
	return 1, nil
}

// dupNow re-checks, under the merge lock, that no live book other than the
// row's own (members, ours) already is group g's work: a live book at the
// group's folder or at the created file_path (LiveBookIDsAtPath), or one with
// its title (fbNorm equality) whose author is the group's or none, or any
// author when the group has none, matched symmetrically with the plan's
// lib.titles check. Two rows of one apply creating "Dune" meet here, and so
// does a book created after the plan.
//
// Candidates come from titles (titleIndex, current for the library
// generation under the lock; an earlier row's created book is in it, its
// un-hide having moved the generation) and, for an authored group, the
// author's books; each is re-read by id.
// It does not page a substring search: SearchBooksFiltered ranks the whole
// library per page and matches authors and narrators too, so a short title
// ("It") meant hundreds of full scans under the lock, and its raw-substring
// candidates missed fbNorm-equal titles spelled with other punctuation.
func (f *folderBooksFixer) dupNow(store OpsStore, members []string, ours string, g fbGroup) (string, error) {
	skip := func(id string) bool { return id == ours || contains(members, id) }
	for _, at := range []string{g.Dir, g.Path} {
		if at == "" {
			continue
		}
		ids, err := store.LiveBookIDsAtPath(at)
		if err != nil {
			return "", fmt.Errorf("live books at %s: %w", at, err)
		}
		for _, id := range ids {
			if !skip(id) {
				return fmt.Sprintf("%q: book %s already sits at %s", g.Title, id, at), nil
			}
		}
	}
	want := fbNorm(g.Title)
	if g.AuthorID != nil {
		books, err := store.GetBooksByAuthorIDWithRoleCore(*g.AuthorID)
		if err != nil {
			return "", fmt.Errorf("books of author %d: %w", *g.AuthorID, err)
		}
		for i := range books {
			b := books[i]
			if b.IsSoftDeleted() || skip(b.ID) || fbNorm(b.Title) != want {
				continue
			}
			return fmt.Sprintf("%q is already book %s", g.Title, b.ID), nil
		}
	}
	seen := map[string]bool{}
	titles, err := f.titleIndex(store, want) // lazily: the checks above refuse without it
	if err != nil {
		return "", err
	}
	for _, id := range titles {
		if seen[id] || skip(id) {
			continue
		}
		seen[id] = true
		b, err := store.GetBookByID(id)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", id, err)
		}
		// Re-checked: the index is a snapshot, and the row may have been
		// hidden or retitled by a writer the merge lock does not exclude.
		if b == nil || b.IsSoftDeleted() || fbNorm(b.Title) != want {
			continue
		}
		if g.AuthorID != nil && b.AuthorID != nil && *b.AuthorID != *g.AuthorID {
			continue
		}
		return fmt.Sprintf("%q is already book %s", g.Title, b.ID), nil
	}
	return "", nil
}

// applyGroup creates (or, on a resumed apply, finishes) one orphan group's
// book. The id is fbCreatedID's, so a cut-off run's book is found again: it
// is extended only when THIS operation journaled creating it; a book with
// that id from another operation refuses the row. Journal first: the create
// row (its NewValue the created title, author and file_path) is written
// before CreateBook, and each new book_file row (record-only) before
// CreateBookFile. The book is created soft-deleted and un-hidden as the last
// write, so a cut-off run never leaves a half-built live book.
func (f *folderBooksFixer) applyGroup(store OpsStore, w *repairs.Writer, plan *fbPlan, rowID string, g fbGroup) (int, error) {
	steps := 0
	target := fbCreatedID(rowID, plan.Members[0], g.Key)
	path := g.Path // fbCreatedPath
	if path == "" {
		return steps, fmt.Errorf("%s: group %q carries no file_path", fbFixerID, g.Title)
	}
	val, err := undo.RepairBookCreateValue{ID: target, Title: g.Title, AuthorID: g.AuthorID, FilePath: path}.Encode()
	if err != nil {
		return steps, err
	}
	cur, err := store.GetBookByID(target)
	if err != nil {
		return steps, fmt.Errorf("read %s: %w", target, err)
	}
	if cur != nil {
		if _, ours, err := w.JournaledValue(target, undo.ChangeTypeRepairBookCreate, "book"); err != nil {
			return steps, err
		} else if !ours {
			return steps, fmt.Errorf("%w: book %s for %q exists and this operation did not create it",
				repairs.ErrChangedSincePlan, target, g.Title)
		}
	} else {
		if err := w.Journal(target, undo.ChangeTypeRepairBookCreate, "book", "", val); err != nil {
			return steps, err
		}
		w.Touch()
		state := plan.State
		hidden := true
		at := f.now().UTC()
		nb := &database.Book{ID: target, Title: g.Title, AuthorID: g.AuthorID, FilePath: path, LibraryState: &state,
			MarkedForDeletion: &hidden, MarkedForDeletionAt: &at}
		if _, err := store.CreateBook(nb); err != nil {
			return steps, fmt.Errorf("create book %q: %w", g.Title, err)
		}
		steps++
		if fbCrashHook != nil {
			if err := fbCrashHook("book", 0); err != nil {
				return steps, err
			}
		}
	}
	if g.AuthorID != nil {
		// A direct store write the Writer cannot route, reached with no
		// journal row in front of it on a resumed apply (the book already
		// exists): renew the scan stand-down lease first.
		if err := w.Beat("author credit of " + target); err != nil {
			return steps, err
		}
		if err := store.SetBookAuthors(target, []database.BookAuthor{{BookID: target, AuthorID: *g.AuthorID,
			Role: "author", Position: 0}}); err != nil {
			return steps, fmt.Errorf("credit author of %s: %w", target, err)
		}
	}
	have, err := store.GetBookFiles(target)
	if err != nil {
		return steps, fmt.Errorf("rows of %s: %w", target, err)
	}
	done := map[string]bool{}
	for _, r := range have {
		done[r.FilePath] = true
	}
	for n, nf := range g.Files {
		if done[nf.Path] {
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
		row.ITunesPersistentID = "" // never moved: CreateBookFile would take the id off the source row
		row.CreatedAt, row.UpdatedAt = time.Time{}, time.Time{}
		if err := w.Journal(target, undo.ChangeTypeBookFileCreate, "book_file:"+row.ID, "", row.FilePath); err != nil {
			return steps, err
		}
		w.Touch()
		if err := store.CreateBookFile(&row); err != nil {
			return steps, fmt.Errorf("create row for %s on %s: %w", nf.Path, target, err)
		}
		steps++
		// The single-owner book_file_path key now names the hidden book's
		// row (last writer wins), and GetBookFileByPath readers do not skip
		// soft-deleted books. Hand it back to the live source row until the
		// un-hide below claims it, so it never names a hidden row while a
		// live one exists. (Teaching readers to skip hidden rows instead
		// would make the single-owner read answer "no row" for a path a live
		// book holds, the false-free answer the multi-valued reads exist to
		// prevent.)
		// ModifyBookFile with a no-op re-reads the row under its stripe and
		// re-writes it as stored (indexes included), so a concurrent writer's
		// update of the source row is never overwritten by this snapshot.
		if fbCrashHook != nil {
			if err := fbCrashHook("reindex", n+1); err != nil {
				return steps, err
			}
		}
		if again, err := store.ModifyBookFile(src.BookID, src.ID, fbKeepAt(nf.Path)); err != nil {
			if errors.Is(err, errFBRowMoved) {
				return steps, fmt.Errorf("%w: source row %s of %s %v", repairs.ErrChangedSincePlan, src.ID, src.BookID, err)
			}
			return steps, fmt.Errorf("re-index %s on its source row %s: %w", nf.Path, src.ID, err)
		} else if again == nil {
			return steps, fmt.Errorf("%w: source row %s of %s vanished", repairs.ErrChangedSincePlan, src.ID, src.BookID)
		}
		if fbCrashHook != nil {
			if err := fbCrashHook("row", n+1); err != nil {
				return steps, err
			}
		}
	}
	if err := w.Recompute(target); err != nil {
		return steps, fmt.Errorf("recompute %s: %w", target, err)
	}
	// Last: un-hide. The create row's revert hides it again.
	if _, err := w.Modify(target, func(b *database.Book) error {
		if !b.IsSoftDeleted() {
			return database.ErrSkipBookWrite
		}
		no := false
		b.MarkedForDeletion, b.MarkedForDeletionAt = &no, nil
		return nil
	}); err != nil && !errors.Is(err, database.ErrSkipBookWrite) {
		return steps, fmt.Errorf("un-hide %s: %w", target, err)
	}
	steps++
	// Now live, the created book's rows take the path keys (idempotent on a
	// resumed apply). The folder-books that held them retire next.
	rows, err := store.GetBookFiles(target)
	if err != nil {
		return steps, fmt.Errorf("rows of %s: %w", target, err)
	}
	for i := range rows {
		if fbCrashHook != nil {
			if err := fbCrashHook("claim", i+1); err != nil {
				return steps, err
			}
		}
		// A direct store write the Writer cannot route: renew the scan
		// stand-down lease before each, as every Writer write does.
		if err := w.Beat("path key of row " + rows[i].ID); err != nil {
			return steps, err
		}
		got, err := store.ModifyBookFile(target, rows[i].ID, fbKeepAt(rows[i].FilePath))
		if err != nil {
			if errors.Is(err, errFBRowMoved) {
				return steps, fmt.Errorf("%w: created row %s of %s %v", repairs.ErrChangedSincePlan, rows[i].ID, target, err)
			}
			return steps, fmt.Errorf("index %s on %s: %w", rows[i].FilePath, target, err)
		}
		if got == nil {
			return steps, fmt.Errorf("%w: created row %s of %s vanished", repairs.ErrChangedSincePlan, rows[i].ID, target)
		}
	}
	return steps, nil
}

// errFBRowMoved: a row re-indexed by fbKeepAt no longer names its path.
var errFBRowMoved = errors.New("no longer names the expected path")

// fbKeepAt changes nothing when the row still names path: ModifyBookFile then
// re-writes the stored row and its secondary indexes, which hands the
// single-owner book_file_path key to that row. A row a writer outside the
// merge lock moved meanwhile is refused (errFBRowMoved), never re-indexed.
func fbKeepAt(path string) func(*database.BookFile) error {
	return func(bf *database.BookFile) error {
		if bf.FilePath != path {
			return fmt.Errorf("%w: names %s, not %s", errFBRowMoved, bf.FilePath, path)
		}
		return nil
	}
}

// retire hides one folder-book: a primary one not already handed off is
// demoted (a group of its own), then file_path is cleared and it is
// soft-deleted in one journaled write. Its book_file rows stay. A book already
// soft-deleted counts none.
func (f *folderBooksFixer) retire(store OpsStore, w *repairs.Writer, id string) (int, error) {
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
	if b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
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
	return steps, nil
}
