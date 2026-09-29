// file: internal/plugins/maintenance/fragment_consolidation_fixer.go
// version: 1.0.0
// guid: 5c9e1a47-2b8d-4f63-a0e7-8d3b6f1c4e92
// last-edited: 2026-09-28

// Repairs-lane fixer "fragment-consolidation": fold chapter and disc files
// that an old scan imported as their own books ("fragments") back into the
// book they belong to.
//
// HOW THE FRAGMENTS CAME TO BE (investigation 2026-09-28). The scanner looked
// a file up only by the book:path index, so a chapter file already owned by a
// multi-file book's book_file row was imported again as a NEW single-file
// book. Auto-organize then MOVED that file to <Author>/<Series>/<Title>/, so
// the parent book's row names a path with nothing on it (while still saying
// Missing=false). PR #3598 stops new fragments; this fixer repairs the ones
// already in the library. The fragment's book records where its file was
// when it was imported: the "import" path-change row CreateBook writes, and
// book_file.original_filename.
//
// CLASSES, one per row (Row.Class):
//
//   - moved: a parent book's row names a path that is gone from disk, and
//     the file now sits under a fragment book. Apply repoints the parent's
//     row at the file's current path (same row id, track and history; the
//     row's old location is journaled) and soft-retires the fragment. The
//     fragment's own row is kept, on the retired book: no book_file row is
//     ever deleted.
//   - copy: the fragment's file duplicates a file the parent still has on
//     disk. When the match is proven (the fragment was imported FROM the
//     parent row's path, or the two files hash the same) apply soft-retires
//     the fragment; the parent is not written. An unproven match (name and
//     size only) is shown as a skipped row for the owner to decide.
//   - no-parent: three or more fragments imported from one folder that
//     share a chapter key (metadata.ChapterGroupKey, the scanner's rule) and
//     whose every file is shorter than the chapter-consolidation threshold,
//     with no existing parent. Apply elects one of them (the lowest book id,
//     so a re-plan elects the same one), moves every other fragment's row
//     onto it, numbers the tracks in chapter order, titles it from the
//     folder (unless the title is locked) and retires the emptied fragments.
//   - manual-only: anything with a path under books/itunes/** or that is
//     Doctor Who / Big Finish / Torchwood by path or series. Listed, never
//     applied.
//   - ambiguous: a fragment that matches two parents or two parent rows, or
//     a parent row two fragments claim. Listed, never applied.
//   - held: a fragment that matches a parent but whose own file is missing
//     or unreadable. Listed, never applied.
//
// Before a row is applied, Replan re-checks with the strict ownership
// lookup (database.BookFileRowsAtPathStrict) that no book outside the row
// owns any file the row touches; an incomplete lookup (memdb warmup) fails
// the row rather than guessing.
//
// EVERY STEP IS UNDOABLE. Each write goes through repairs.Writer and is
// journaled under the apply op's id AFTER it commits: book_file_repoint_location,
// book_file_reassign, book_file_track, book_primary_demote, book_soft_delete,
// metadata_update (title). POST /operations/<apply op>/revert undoes the lot.
//
// RESUME. The fingerprint hashes the DECISION (the class, the members, each
// planned file pairing, the survivor, the title), not the raw row state, and
// Replan recognises its own finished steps (a parent row already at the
// fragment's path; a fragment already retired; a row already on the
// survivor). A row cut off mid-write therefore re-plans to the same
// fingerprint and the resumed apply finishes it.
//
// CONCURRENCY. Plan evaluates fragment candidates on a bounded RunItems pool
// (point reads of path history plus os.Stat). Apply runs through the
// framework engine, which partitions rows so that rows sharing any book land
// in one partition: no book is ever written by two workers.
package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

const fragFixerID = "fragment-consolidation"

// Row classes.
const (
	fragClassMoved     = "moved"
	fragClassCopy      = "copy"
	fragClassNoParent  = "no-parent"
	fragClassManual    = "manual-only"
	fragClassAmbiguous = "ambiguous"
	// fragClassHeld: a fragment that matches a parent but whose own file is
	// not on disk (or cannot be read). Repointing a parent row at it would
	// mark a missing file present, so it is listed, never applied.
	fragClassHeld = "held"
)

// fragRowCopyUnproven prefixes the row id of a parent's UNPROVEN copies, so
// they never share a row (and a skip) with the proven ones.
const fragRowCopyUnproven = "copy-unproven"

// Skip kinds this fixer sets itself (the framework adds the guard kinds).
const (
	fragSkipAmbiguous       = "skipped_ambiguous"
	fragSkipCopyUnproven    = "skipped_copy_unproven"
	fragSkipDurationGate    = "skipped_duration_gate"
	fragSkipDurationUnknown = "skipped_duration_unknown"
	fragSkipFilesMissing    = "skipped_files_missing"
	fragSkipUnreadable      = "skipped_unreadable"
)

// Evidence kinds of a fragment-to-parent match, strongest first.
const (
	fragEvImportPath = "import path equals the parent row's path"
	fragEvHash       = "file hash equals the parent row's"
	fragEvNameSize   = "original filename and size equal the parent row's"
	fragEvDone       = "parent row already points at the fragment's file (finished step)"
)

// fragMinGroup is the scanner's consolidation minimum: fewer same-key files
// than this are not evidence of a chapter sequence.
const fragMinGroup = 3

// fragmentFixer implements repairs.Fixer.
type fragmentFixer struct {
	p *Plugin
	// statFn replaces os.Stat in tests.
	statFn func(string) (os.FileInfo, error)
	// now replaces time.Now in tests.
	now func() time.Time
}

func newFragmentFixer(p *Plugin) *fragmentFixer {
	return &fragmentFixer{p: p, statFn: os.Stat, now: time.Now}
}

var _ repairs.Fixer = (*fragmentFixer)(nil)

func (f *fragmentFixer) ID() string    { return fragFixerID }
func (f *fragmentFixer) Title() string { return "Chapter fragments" }
func (f *fragmentFixer) Description() string {
	return "Chapter and disc files an old scan imported as separate books. Moved: the parent's row points " +
		"at a path organize emptied — repoint it at the file's new path and retire the fragment. Copy: the " +
		"parent still has the file — retire the proven duplicate. No parent: 3+ short same-key chapters " +
		"from one folder — elect one book and move every chapter onto it in track order. iTunes and " +
		"Doctor Who / Big Finish / Torchwood are listed for manual action only. Every step is undoable " +
		"from the apply operation."
}

// fragParams are the fixer params. BookIDs limits the plan to fragments
// and parents among these ids (tests, spot checks); empty means the library.
type fragParams struct {
	BookIDs []string `json:"book_ids,omitempty"`
}

func decodeFragParams(raw json.RawMessage) (fragParams, error) {
	var fp fragParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &fp); err != nil {
			return fp, fmt.Errorf("%s: invalid params: %w", fragFixerID, err)
		}
	}
	return fp, nil
}

// ---- inputs ---------------------------------------------------------------

// fragBook is the part of a book the fixer reads.
type fragBook struct {
	ID          string
	Title       string
	FilePath    string
	AuthorID    *int
	SeriesID    *int
	SoftDeleted bool
}

// fragFile is the part of a book_file row the fixer reads.
type fragFile struct {
	ID, BookID       string
	Path             string
	OriginalFilename string
	Size             int64
	Hash, OrigHash   string
	Duration         int
	Track            int
	Missing          bool
}

func fragFileFrom(bookID string, id, path, orig string, size int64, hash, origHash string, dur, track int, missing bool) fragFile {
	return fragFile{ID: id, BookID: bookID, Path: path, OriginalFilename: orig, Size: size, Hash: hash,
		OrigHash: origHash, Duration: dur, Track: track, Missing: missing}
}

func (x fragFile) location() undo.BookFileLocation {
	return undo.BookFileLocation{Path: x.Path, Missing: x.Missing, Hash: x.Hash, Size: x.Size}
}

// fragCandidate is one fragment book: a live single-file book with chapter
// evidence.
type fragCandidate struct {
	Book       fragBook
	File       fragFile
	ImportPath string // where the file was when the book was imported ("" unknown)
	OrigName   string // the pre-organize basename ("" unknown)
	Present    bool   // the fragment's own file is on disk
	StatErr    string // a stat error other than not-exist
}

// origStem is the filename stem the chapter key is read from.
func (c *fragCandidate) origStem() string {
	name := c.OrigName
	if name == "" {
		name = filepath.Base(c.File.Path)
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// origDir is the folder the fragment was imported from.
func (c *fragCandidate) origDir() string {
	if c.ImportPath != "" {
		return filepath.Dir(c.ImportPath)
	}
	return filepath.Dir(c.File.Path)
}

// fragIndex indexes parent rows (rows of books with two or more rows).
type fragIndex struct {
	byPath map[string][]fragFile
	byHash map[string][]fragFile
	byName map[string][]fragFile // lower-cased basename
}

func newFragIndex() *fragIndex {
	return &fragIndex{byPath: map[string][]fragFile{}, byHash: map[string][]fragFile{}, byName: map[string][]fragFile{}}
}

func (ix *fragIndex) add(r fragFile) {
	ix.byPath[r.Path] = append(ix.byPath[r.Path], r)
	for _, h := range uniqueNonEmpty(r.Hash, r.OrigHash) {
		ix.byHash[h] = append(ix.byHash[h], r)
	}
	ix.byName[strings.ToLower(filepath.Base(r.Path))] = append(ix.byName[strings.ToLower(filepath.Base(r.Path))], r)
}

func uniqueNonEmpty(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v != "" && !contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// fragMatch is a fragment's match to one parent row.
type fragMatch struct {
	Row      fragFile
	Evidence string
}

// match finds the parent rows the candidate matches, strongest tier first:
// the import path, then the hash, then the pre-organize basename plus size.
// Size alone never matches. Rows of the candidate's own book are ignored. A
// tier that yields a match ends the search. The second result is set when a
// tier found conflicting evidence (a hash on both sides that differs) that
// rejected an otherwise matching row.
func (ix *fragIndex) match(c *fragCandidate) []fragMatch {
	own := c.Book.ID
	var out []fragMatch
	// A finished step: some parent row already names the fragment's file.
	for _, r := range ix.byPath[c.File.Path] {
		if r.BookID != own {
			out = append(out, fragMatch{Row: r, Evidence: fragEvDone})
		}
	}
	if len(out) > 0 {
		return out
	}
	if c.ImportPath != "" && c.ImportPath != c.File.Path {
		for _, r := range ix.byPath[c.ImportPath] {
			if r.BookID != own {
				out = append(out, fragMatch{Row: r, Evidence: fragEvImportPath})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	seen := map[string]bool{}
	for _, h := range uniqueNonEmpty(c.File.Hash, c.File.OrigHash) {
		for _, r := range ix.byHash[h] {
			if r.BookID != own && !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, fragMatch{Row: r, Evidence: fragEvHash})
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	if c.OrigName == "" || c.File.Size <= 0 {
		return nil
	}
	for _, r := range ix.byName[strings.ToLower(c.OrigName)] {
		if r.BookID == own || r.Size != c.File.Size {
			continue
		}
		// Hashes on both sides that share nothing contradict the name match.
		if hashesDisagree(r, c.File) {
			continue
		}
		out = append(out, fragMatch{Row: r, Evidence: fragEvNameSize})
	}
	return out
}

func hashesDisagree(a, b fragFile) bool {
	ah, bh := uniqueNonEmpty(a.Hash, a.OrigHash), uniqueNonEmpty(b.Hash, b.OrigHash)
	if len(ah) == 0 || len(bh) == 0 {
		return false
	}
	for _, x := range ah {
		if contains(bh, x) {
			return false
		}
	}
	return true
}

// ---- stores ---------------------------------------------------------------

func (f *fragmentFixer) stores() (OpsStore, FragmentRepairReader, error) {
	store := f.p.deps.OpsStore()
	hist := f.p.deps.FragmentRepairReader()
	if store == nil || hist == nil {
		return nil, nil, fmt.Errorf("database not initialized")
	}
	return store, hist, nil
}

// importPathOf returns the path the book's file had when it was imported:
// the NewPath of its earliest "import" path-change row, else the OldPath of
// its earliest path change of any kind. "" when there is no history.
func importPathOf(hist []database.BookPathChange) string {
	sort.SliceStable(hist, func(i, j int) bool {
		if !hist[i].CreatedAt.Equal(hist[j].CreatedAt) {
			return hist[i].CreatedAt.Before(hist[j].CreatedAt)
		}
		return hist[i].ID < hist[j].ID
	})
	for _, h := range hist {
		if h.ChangeType == "import" && h.NewPath != "" {
			return h.NewPath
		}
	}
	for _, h := range hist {
		if h.OldPath != "" {
			return h.OldPath
		}
	}
	return ""
}

// candidateFrom builds a candidate from a live single-file book, or returns
// false when the book carries no chapter evidence.
func (f *fragmentFixer) candidateFrom(b fragBook, file fragFile, hist FragmentRepairReader) (*fragCandidate, bool, error) {
	c := &fragCandidate{Book: b, File: file}
	h, err := hist.GetBookPathHistory(b.ID)
	if err != nil {
		return nil, false, fmt.Errorf("path history of %s: %w", b.ID, err)
	}
	c.ImportPath = importPathOf(h)
	switch {
	case file.OriginalFilename != "":
		c.OrigName = filepath.Base(file.OriginalFilename)
	case c.ImportPath != "":
		c.OrigName = filepath.Base(c.ImportPath)
	case len(h) == 0:
		// Never moved: the current name is the original one.
		c.OrigName = filepath.Base(file.Path)
	}
	_, kind := metadata.ChapterGroupKey(c.origStem())
	if kind == metadata.ChapterKeyNone && !metadata.IsChapterOnlyTitle(b.Title) {
		return nil, false, nil
	}
	if _, err := f.statFn(file.Path); err == nil {
		c.Present = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		c.StatErr = err.Error()
	}
	return c, true, nil
}

// fragLibrary is the whole-library snapshot a plan reads.
type fragLibrary struct {
	books   map[string]fragBook
	files   map[string][]fragFile // by book id
	series  map[int]string
	authors map[int]string
}

func (f *fragmentFixer) loadLibrary(store OpsStore) (*fragLibrary, error) {
	lib := &fragLibrary{books: map[string]fragBook{}, files: map[string][]fragFile{}, series: map[int]string{}, authors: map[int]string{}}
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	for i := range books {
		b := &books[i]
		lib.books[b.ID] = fragBook{ID: b.ID, Title: b.Title, FilePath: b.FilePath, AuthorID: b.AuthorID,
			SeriesID: b.SeriesID, SoftDeleted: b.IsSoftDeleted()}
	}
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("list book files: %w", err)
	}
	for i := range cores {
		r := &cores[i]
		lib.files[r.BookID] = append(lib.files[r.BookID], fragFileFrom(r.BookID, r.ID, r.FilePath, r.OriginalFilename,
			r.FileSize, r.FileHash, r.OriginalFileHash, r.Duration, r.TrackNumber, r.Missing))
	}
	all, err := store.GetAllSeries()
	if err != nil {
		// The Doctor Who guard reads series names: fail rather than plan past it.
		return nil, fmt.Errorf("list series: %w", err)
	}
	for _, s := range all {
		lib.series[s.ID] = s.Name
	}
	if authors, aerr := store.GetAllAuthors(); aerr == nil {
		for _, a := range authors {
			lib.authors[a.ID] = a.Name
		}
	}
	return lib, nil
}

func (lib *fragLibrary) seriesName(b fragBook) string {
	if b.SeriesID == nil {
		return ""
	}
	return lib.series[*b.SeriesID]
}

func (lib *fragLibrary) authorName(b fragBook) string {
	if b.AuthorID == nil {
		return ""
	}
	return lib.authors[*b.AuthorID]
}

// ---- plan -----------------------------------------------------------------

// Plan reads the whole library once, evaluates every fragment candidate on a
// bounded pool, and builds one row per parent-and-class, per ambiguous
// fragment and per no-parent folder group.
func (f *fragmentFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	fp, err := decodeFragParams(raw)
	if err != nil {
		return nil, err
	}
	store, hist, err := f.stores()
	if err != nil {
		return nil, err
	}
	var only map[string]bool
	if len(fp.BookIDs) > 0 {
		only = map[string]bool{}
		for _, id := range fp.BookIDs {
			only[id] = true
		}
	}
	lib, err := f.loadLibrary(store)
	if err != nil {
		return nil, err
	}
	ix := newFragIndex()
	type single struct {
		b    fragBook
		file fragFile
	}
	var singles []single
	for id, rows := range lib.files {
		b, ok := lib.books[id]
		if !ok || b.SoftDeleted || (only != nil && !only[id]) {
			continue
		}
		if len(rows) >= 2 {
			for _, r := range rows {
				ix.add(r)
			}
		} else if len(rows) == 1 {
			singles = append(singles, single{b: b, file: rows[0]})
		}
	}
	sort.Slice(singles, func(i, j int) bool { return singles[i].b.ID < singles[j].b.ID })

	cands := make([]*fragCandidate, len(singles))
	idx := make([]int, len(singles))
	for i := range idx {
		idx[i] = i
	}
	var done atomic.Int64
	// Each worker writes only cands[i] for its own i.
	if err := registry.RunItems(ctx, rep, idx, func(_ context.Context, i int) error {
		defer done.Add(1)
		c, ok, cerr := f.candidateFrom(singles[i].b, singles[i].file, hist)
		if cerr != nil {
			return cerr
		}
		if ok {
			cands[i] = c
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Fragment candidates %d/%d", done.Load(), total) },
	}); err != nil {
		return nil, fmt.Errorf("%s: candidates: %w", fragFixerID, err)
	}
	var live []*fragCandidate
	for _, c := range cands {
		if c != nil {
			live = append(live, c)
		}
	}
	return f.buildRows(lib, ix, live), nil
}

// fragPair is one fragment matched to one parent row.
type fragPair struct {
	Frag     *fragCandidate
	Parent   fragFile
	Evidence string
	Done     bool // the parent row already points at the fragment's file
}

// buildRows turns the evaluated candidates into rows. It is shared by Plan
// (over the whole library) and Replan (over one row's books), so both reach
// the same decision from the same state.
func (f *fragmentFixer) buildRows(lib *fragLibrary, ix *fragIndex, cands []*fragCandidate) []repairs.Row {
	type parentKey struct{ parent, class string }
	pairs := map[parentKey][]fragPair{}
	claims := map[string][]*fragCandidate{} // parent row id -> fragments claiming it
	var rows []repairs.Row
	var unmatched []*fragCandidate

	for _, c := range cands {
		ms := ix.match(c)
		parents := map[string]bool{}
		for _, m := range ms {
			parents[m.Row.BookID] = true
		}
		switch {
		case len(ms) == 0:
			unmatched = append(unmatched, c)
		case c.StatErr != "":
			rows = append(rows, f.holdRow(lib, c, fragClassHeld, fragClassHeld, fragSkipUnreadable,
				fmt.Sprintf("file %s is unreadable (%s)", c.File.Path, c.StatErr), ms))
		case !c.Present:
			rows = append(rows, f.holdRow(lib, c, fragClassHeld, fragClassHeld, fragSkipFilesMissing,
				fmt.Sprintf("file %s is not on disk", c.File.Path), ms))
		case len(parents) > 1 || len(ms) > 1:
			rows = append(rows, f.ambiguousRow(lib, c, fmt.Sprintf("matches %d rows of %d parent books", len(ms), len(parents)), ms))
		default:
			claims[ms[0].Row.ID] = append(claims[ms[0].Row.ID], c)
		}
	}
	// A parent row two fragments claim is ambiguous for both.
	var rowIDs []string
	for id := range claims {
		rowIDs = append(rowIDs, id)
	}
	sort.Strings(rowIDs)
	for _, rid := range rowIDs {
		cs := claims[rid]
		if len(cs) > 1 {
			for _, c := range cs {
				rows = append(rows, f.ambiguousRow(lib, c, fmt.Sprintf("parent row %s is claimed by %d fragments", rid, len(cs)), ix.match(c)))
			}
			continue
		}
		c := cs[0]
		m := ix.match(c)[0]
		p := fragPair{Frag: c, Parent: m.Row, Evidence: m.Evidence, Done: m.Evidence == fragEvDone}
		class := fragClassMoved
		if !p.Done {
			if _, err := f.statFn(m.Row.Path); err == nil {
				class = fragClassCopy
			} else if !errors.Is(err, fs.ErrNotExist) {
				rows = append(rows, f.ambiguousRow(lib, c, "parent row path unreadable: "+err.Error(), []fragMatch{m}))
				continue
			}
		}
		if class == fragClassCopy && !provenMatch(p.Evidence) {
			class = fragRowCopyUnproven
		}
		k := parentKey{m.Row.BookID, class}
		pairs[k] = append(pairs[k], p)
	}
	var keys []parentKey
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].parent != keys[j].parent {
			return keys[i].parent < keys[j].parent
		}
		return keys[i].class < keys[j].class
	})
	for _, k := range keys {
		rows = append(rows, f.parentRow(lib, k.parent, k.class, pairs[k]))
	}
	rows = append(rows, f.noParentRows(lib, unmatched)...)
	return rows
}

// guard runs the framework's hands-off check over every path the row names,
// the fragments' import paths included, so a row can be labelled
// manual-only by the fixer itself.
func (f *fragmentFixer) guard(lib *fragLibrary, books []fragBook, extra map[string][]string) (kind, why string) {
	for _, b := range books {
		paths := []string{b.FilePath}
		for _, r := range lib.files[b.ID] {
			paths = append(paths, r.Path)
		}
		paths = append(paths, extra[b.ID]...)
		if k, w := repairs.GuardBookPaths(b.ID, paths, lib.seriesName(b)); k != "" {
			return k, w
		}
	}
	return "", ""
}

func member(lib *fragLibrary, b fragBook, role string) repairs.RowMember {
	m := repairs.RowMember{BookID: b.ID, Title: b.Title, Role: role}
	for _, r := range lib.files[b.ID] {
		m.Files++
		if r.Missing {
			m.MissingFiles++
		}
	}
	return m
}

func (f *fragmentFixer) ambiguousRow(lib *fragLibrary, c *fragCandidate, why string, ms []fragMatch) repairs.Row {
	return f.holdRow(lib, c, "ambiguous", fragClassAmbiguous, fragSkipAmbiguous, why, ms)
}

// holdRow is a never-applied row for one fragment: listed with its candidate
// parents so the owner can act by hand.
func (f *fragmentFixer) holdRow(lib *fragLibrary, c *fragCandidate, idPrefix, class, skip, why string, ms []fragMatch) repairs.Row {
	r := repairs.Row{RowID: idPrefix + ":" + c.Book.ID, Class: class, Title: c.Book.Title,
		Author: lib.authorName(c.Book), Risk: repairs.RiskReview, Reason: "fragment " + why,
		Skipped: skip, SkipReason: "fragment " + why + "; resolve by hand"}
	books := []fragBook{c.Book}
	r.BookIDs = []string{c.Book.ID}
	r.Members = []repairs.RowMember{member(lib, c.Book, "fragment")}
	seen := map[string]bool{}
	for _, m := range ms {
		r.Evidence = append(r.Evidence, fmt.Sprintf("%s → parent %s row %s (%s)", filepath.Base(c.File.Path), m.Row.BookID, m.Row.ID, m.Evidence))
		if !seen[m.Row.BookID] {
			seen[m.Row.BookID] = true
			if pb, ok := lib.books[m.Row.BookID]; ok {
				r.Members = append(r.Members, member(lib, pb, "candidate parent"))
				r.BookIDs = append(r.BookIDs, pb.ID)
			}
		}
	}
	if k, _ := f.guard(lib, books, map[string][]string{c.Book.ID: {c.ImportPath}}); k != "" {
		r.Class = fragClassManual
	}
	r.Fingerprint = fragFingerprint(idPrefix, c.Book.ID, why)
	return r
}

// parentRow is a moved or copy row: one parent and every fragment matched to
// it in that class.
func provenMatch(evidence string) bool {
	return evidence == fragEvImportPath || evidence == fragEvHash || evidence == fragEvDone
}

func (f *fragmentFixer) parentRow(lib *fragLibrary, parentID, rowKind string, pairs []fragPair) repairs.Row {
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Frag.Book.ID < pairs[j].Frag.Book.ID })
	parent := lib.books[parentID]
	class := rowKind
	if rowKind == fragRowCopyUnproven {
		class = fragClassCopy
	}
	r := repairs.Row{RowID: rowKind + ":" + parentID, Class: class, Title: parent.Title,
		Author: lib.authorName(parent), Risk: repairs.RiskLow, Detail: pairs}
	r.BookIDs = []string{parentID}
	r.Members = []repairs.RowMember{member(lib, parent, "parent")}
	books := []fragBook{parent}
	extra := map[string][]string{}
	var fpParts []string
	proven, done := 0, 0
	for _, p := range pairs {
		r.BookIDs = append(r.BookIDs, p.Frag.Book.ID)
		r.Members = append(r.Members, member(lib, p.Frag.Book, "fragment"))
		books = append(books, p.Frag.Book)
		extra[p.Frag.Book.ID] = []string{p.Frag.ImportPath}
		if p.Done {
			done++
		}
		if provenMatch(p.Evidence) {
			proven++
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf("%s ← parent row %s (%s): %s",
			p.Frag.File.Path, p.Parent.ID, filepath.Base(p.Parent.Path), p.Evidence))
		fpParts = append(fpParts, strings.Join([]string{p.Frag.Book.ID, p.Frag.File.ID, p.Frag.File.Path, p.Parent.ID}, "|"))
	}
	sort.Strings(r.BookIDs)
	n := len(pairs)
	r.Current = map[string]string{
		"parent_files": strconv.Itoa(len(lib.files[parentID])),
		"fragments":    strconv.Itoa(n),
	}
	switch class {
	case fragClassMoved:
		r.Current["stale_parent_rows"] = strconv.Itoa(n - done)
		r.Proposed = map[string]string{
			"action": fmt.Sprintf("repoint %d parent row(s) at the fragments' files; retire %d fragment book(s)", n-done, n),
		}
		r.Reason = fmt.Sprintf("%d of the parent's rows name paths that are gone; their files now sit under %d fragment book(s)", n, n)
		if proven < n {
			r.Risk = repairs.RiskReview
		}
	case fragClassCopy:
		r.Proposed = map[string]string{"action": fmt.Sprintf("retire %d fragment book(s) (the parent keeps its own files; nothing is repointed)", n)}
		r.Reason = fmt.Sprintf("%d fragment book(s) duplicate files the parent still has on disk", n)
		if proven < n {
			r.Skipped = fragSkipCopyUnproven
			r.SkipReason = fmt.Sprintf("%d of %d matches rest on name and size only (no import path or hash): check by hand before retiring", n-proven, n)
			r.Risk = repairs.RiskReview
		}
	}
	if k, why := f.guard(lib, books, extra); k != "" {
		r.Class, r.Skipped, r.SkipReason = fragClassManual, k, why
	}
	r.Fingerprint = fragFingerprint(append([]string{rowKind, parentID}, fpParts...)...)
	return r
}

// fragGroupMember is one fragment of a no-parent group, in track order.
type fragGroupMember struct {
	Frag  *fragCandidate
	Track int
}

// fragGroupPlan is a no-parent row's decision.
type fragGroupPlan struct {
	Dir, Key   string
	SurvivorID string
	Title      string // "" keeps the survivor's title
	// Folder is the one folder every member's file sits in now, which
	// becomes the survivor's book path (a multi-file book's path is its
	// folder). "" when the files are spread out: the path is left alone.
	Folder  string
	Members []fragGroupMember
}

var fragLeadingNumRe = regexp.MustCompile(`^\D*?(\d+)`)
var fragTrailingNumRe = regexp.MustCompile(`(\d+)\D*$`)

// chapterNumber is the chapter position a stem carries: the leading number
// for a leading-number key, else the last number in the stem. -1 when none.
func chapterNumber(stem string) int {
	_, kind := metadata.ChapterGroupKey(stem)
	re := fragTrailingNumRe
	if kind == metadata.ChapterKeyLeading {
		re = fragLeadingNumRe
	}
	m := re.FindStringSubmatch(stem)
	if m == nil {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return n
}

func thresholdSec() int {
	mins := config.AppConfig.ChapterConsolidationThresholdMin
	if mins <= 0 {
		mins = 10 // config's documented default; the scanner uses the same fallback
	}
	return mins * 60
}

// noParentRows groups unmatched fragments by import folder and chapter key.
func (f *fragmentFixer) noParentRows(lib *fragLibrary, cands []*fragCandidate) []repairs.Row {
	groups := map[string][]*fragCandidate{}
	for _, c := range cands {
		key, kind := metadata.ChapterGroupKey(c.origStem())
		if kind == metadata.ChapterKeyNone {
			// A chapter-only TITLE with an unkeyed file name: no group rule.
			continue
		}
		gk := c.origDir() + "\x00" + key
		groups[gk] = append(groups[gk], c)
	}
	var keys []string
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows []repairs.Row
	for _, gk := range keys {
		cs := groups[gk]
		if len(cs) < fragMinGroup {
			continue
		}
		dir, key, _ := strings.Cut(gk, "\x00")
		rows = append(rows, f.noParentRow(lib, dir, key, cs))
	}
	return rows
}

func noParentRowID(dir, key string) string {
	sum := sha256.Sum256([]byte(dir + "\x00" + key))
	return "no-parent:" + hex.EncodeToString(sum[:])[:20]
}

func (f *fragmentFixer) noParentRow(lib *fragLibrary, dir, key string, cs []*fragCandidate) repairs.Row {
	sort.Slice(cs, func(i, j int) bool {
		ni, nj := chapterNumber(cs[i].origStem()), chapterNumber(cs[j].origStem())
		if ni != nj {
			return ni < nj
		}
		if cs[i].origStem() != cs[j].origStem() {
			return cs[i].origStem() < cs[j].origStem()
		}
		return cs[i].Book.ID < cs[j].Book.ID
	})
	plan := &fragGroupPlan{Dir: dir, Key: key}
	ids := make([]string, 0, len(cs))
	books := make([]fragBook, 0, len(cs))
	extra := map[string][]string{}
	for i, c := range cs {
		plan.Members = append(plan.Members, fragGroupMember{Frag: c, Track: i + 1})
		ids = append(ids, c.Book.ID)
		books = append(books, c.Book)
		extra[c.Book.ID] = []string{c.ImportPath}
	}
	sort.Strings(ids)
	plan.SurvivorID = ids[0]
	survivor := lib.books[plan.SurvivorID]
	// Title and Folder are decided from the group alone, never from whether
	// the survivor already has them: a run cut off after the retitle must
	// re-plan to the same fingerprint (the write is then a no-op).
	if t, _, ok := metadata.ChapterTitleFromDirectory(filepath.Join(dir, "x"), ""); ok {
		plan.Title = t
	}
	plan.Folder = filepath.Dir(cs[0].File.Path)
	for _, c := range cs[1:] {
		if filepath.Dir(c.File.Path) != plan.Folder {
			plan.Folder = ""
			break
		}
	}
	r := repairs.Row{RowID: noParentRowID(dir, key), Class: fragClassNoParent, BookIDs: ids,
		Title: survivor.Title, Author: lib.authorName(survivor), Risk: repairs.RiskReview, Detail: plan}
	if plan.Title != "" {
		r.Title = plan.Title
	}
	var fpParts []string
	missing, unknown, long := 0, 0, 0
	limit := thresholdSec()
	for _, m := range plan.Members {
		role := "fragment"
		if m.Frag.Book.ID == plan.SurvivorID {
			role = "survivor"
		}
		r.Members = append(r.Members, member(lib, m.Frag.Book, role))
		if !m.Frag.Present {
			missing++
		}
		switch d := m.Frag.File.Duration; {
		case d <= 0:
			unknown++
		case d >= limit:
			long++
		}
		fpParts = append(fpParts, fmt.Sprintf("%s|%s|%s|%d", m.Frag.Book.ID, m.Frag.File.ID, m.Frag.File.Path, m.Track))
	}
	r.Evidence = []string{
		fmt.Sprintf("%d fragment books imported from %s share the chapter key %q", len(cs), dir, key),
		fmt.Sprintf("durations: %d known, %d unknown, %d at or over %d min", len(cs)-unknown, unknown, long, limit/60),
		"no existing book owns any of these files",
	}
	r.Current = map[string]string{"fragments": strconv.Itoa(len(cs)), "folder": dir}
	r.Proposed = map[string]string{
		"action":   fmt.Sprintf("elect %s; move %d fragment row(s) onto it in track order; retire %d emptied book(s)", plan.SurvivorID, len(cs)-1, len(cs)-1),
		"survivor": plan.SurvivorID,
	}
	if plan.Title != "" {
		r.Proposed["title"] = plan.Title
	}
	r.Reason = fmt.Sprintf("%d chapter files from one folder were imported as %d separate books", len(cs), len(cs))
	switch {
	case missing > 0:
		r.Skipped, r.SkipReason = fragSkipFilesMissing, fmt.Sprintf("%d of %d fragments' files are not on disk", missing, len(cs))
	case unknown > 0:
		// The scanner's R-8 rule: an unknown duration is not "short".
		r.Skipped, r.SkipReason = fragSkipDurationUnknown, fmt.Sprintf("%d of %d files have no recorded duration; cannot tell chapters from whole books", unknown, len(cs))
	case long > 0:
		r.Skipped, r.SkipReason = fragSkipDurationGate, fmt.Sprintf("%d of %d files run %d min or longer: likely separate books, not chapters", long, len(cs), limit/60)
	}
	if k, why := f.guard(lib, books, extra); k != "" {
		r.Class, r.Skipped, r.SkipReason = fragClassManual, k, why
	}
	if plan.Folder != "" {
		r.Proposed["book_path"] = plan.Folder
	}
	r.Fingerprint = fragFingerprint(append([]string{fragClassNoParent, dir, key, plan.SurvivorID, plan.Title, plan.Folder}, fpParts...)...)
	return r
}

func fragFingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:32]
}

// ---- replan ---------------------------------------------------------------

// Replan rebuilds one row from its own books only (no library scan): the
// parent's rows form the match index, and each fragment is re-evaluated,
// including one a partial apply already retired.
func (f *fragmentFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store, hist, err := f.stores()
	if err != nil {
		return repairs.Row{}, err
	}
	lib := &fragLibrary{books: map[string]fragBook{}, files: map[string][]fragFile{}, series: map[int]string{}, authors: map[int]string{}}
	all, err := store.GetAllSeries()
	if err != nil {
		return repairs.Row{}, fmt.Errorf("list series: %w", err)
	}
	for _, s := range all {
		lib.series[s.ID] = s.Name
	}
	for _, id := range planned.BookIDs {
		if err := ctx.Err(); err != nil {
			return repairs.Row{}, err
		}
		b, err := store.GetBookByID(id)
		if err != nil {
			return repairs.Row{}, fmt.Errorf("read %s: %w", id, err)
		}
		if b == nil {
			continue
		}
		lib.books[id] = fragBook{ID: b.ID, Title: b.Title, FilePath: b.FilePath, AuthorID: b.AuthorID, SeriesID: b.SeriesID, SoftDeleted: b.IsSoftDeleted()}
		if b.AuthorID != nil {
			if a, aerr := store.GetAuthorByID(*b.AuthorID); aerr == nil && a != nil {
				lib.authors[a.ID] = a.Name
			}
		}
		rows, err := store.GetBookFiles(id)
		if err != nil {
			return repairs.Row{}, fmt.Errorf("read files of %s: %w", id, err)
		}
		for i := range rows {
			r := &rows[i]
			lib.files[id] = append(lib.files[id], fragFileFrom(id, r.ID, r.FilePath, r.OriginalFilename, r.FileSize,
				r.FileHash, r.OriginalFileHash, r.Duration, r.TrackNumber, r.Missing))
		}
	}
	class, rest, _ := strings.Cut(planned.RowID, ":")
	switch class {
	case fragClassMoved, fragClassCopy, fragRowCopyUnproven:
		return f.replanParent(lib, hist, planned, rest)
	case fragClassNoParent:
		return f.replanGroup(lib, hist, planned)
	default:
		return planned, nil // ambiguous rows are never applicable; return as planned
	}
}

func (f *fragmentFixer) replanParent(lib *fragLibrary, hist FragmentRepairReader, planned repairs.Row, parentID string) (repairs.Row, error) {
	ix := newFragIndex()
	for _, r := range lib.files[parentID] {
		ix.add(r)
	}
	var cands []*fragCandidate
	for _, id := range planned.BookIDs {
		if id == parentID {
			continue
		}
		b, ok := lib.books[id]
		if !ok || len(lib.files[id]) != 1 {
			return changedRow(planned, fmt.Sprintf("fragment %s is gone or no longer a single-file book", id)), nil
		}
		// A fragment a partial run already retired is still evaluated: its
		// row is still there, and the parent's finished repoint names it.
		b.SoftDeleted = false
		c, ok, err := f.candidateFrom(b, lib.files[id][0], hist)
		if err != nil {
			return repairs.Row{}, err
		}
		if !ok {
			return changedRow(planned, fmt.Sprintf("fragment %s carries no chapter evidence now", id)), nil
		}
		cands = append(cands, c)
	}
	rows := f.buildRows(lib, ix, cands)
	for _, r := range rows {
		if r.RowID == planned.RowID {
			return f.checkOwners(hist, planned, r)
		}
	}
	return changedRow(planned, "the fragments no longer match the parent in this class"), nil
}

func (f *fragmentFixer) replanGroup(lib *fragLibrary, hist FragmentRepairReader, planned repairs.Row) (repairs.Row, error) {
	// A partial run moved some fragments' rows onto the survivor. Rebuild
	// each fragment's single-file view from the rows as they sit now: a row
	// on the survivor still stands for the fragment it came from, found by
	// that fragment's import path. The decision (group key, survivor, track
	// order, title) is then the same as the plan's, so is the fingerprint.
	survivorID := planned.Proposed["survivor"]
	var cands []*fragCandidate
	for _, id := range planned.BookIDs {
		b, ok := lib.books[id]
		if !ok {
			return changedRow(planned, fmt.Sprintf("fragment %s is gone", id)), nil
		}
		b.SoftDeleted = false
		rows := lib.files[id]
		var file fragFile
		switch {
		case id == survivorID:
			// The survivor's own row: the one whose history is its import.
			own, ok := f.survivorOwnRow(lib, hist, b)
			if !ok {
				return changedRow(planned, "survivor's own row cannot be identified"), nil
			}
			file = own
		case len(rows) == 1:
			file = rows[0]
		case len(rows) == 0:
			// Moved onto the survivor already: find the row by its import path.
			r, ok := f.movedRowFor(lib, hist, b, survivorID)
			if !ok {
				return changedRow(planned, fmt.Sprintf("fragment %s has no rows and none on the survivor stands for it", id)), nil
			}
			file = r
		default:
			return changedRow(planned, fmt.Sprintf("fragment %s now has %d rows", id, len(rows))), nil
		}
		file.BookID = id
		c, ok, err := f.candidateFrom(b, file, hist)
		if err != nil {
			return repairs.Row{}, err
		}
		if !ok {
			return changedRow(planned, fmt.Sprintf("fragment %s carries no chapter evidence now", id)), nil
		}
		cands = append(cands, c)
	}
	rows := f.noParentRows(lib, cands)
	for _, r := range rows {
		if r.RowID == planned.RowID {
			// Rows already on the survivor count as the survivor's: the plan
			// was made before they moved.
			return f.checkOwners(hist, planned, r)
		}
	}
	return changedRow(planned, "the fragments no longer form this group"), nil
}

// checkOwners re-checks, with the strict lookup, that every file the fresh
// row touches is owned only by books of the row. A file some other book also
// claims makes the row changed (the plan's whole-library listing no longer
// holds); an incomplete lookup fails the row rather than guessing.
func (f *fragmentFixer) checkOwners(look database.BookFilePathLookup, planned, fresh repairs.Row) (repairs.Row, error) {
	if !fresh.Applicable() {
		return fresh, nil
	}
	var paths []string
	switch d := fresh.Detail.(type) {
	case []fragPair:
		for _, p := range d {
			paths = append(paths, p.Frag.File.Path)
		}
	case *fragGroupPlan:
		for _, m := range d.Members {
			paths = append(paths, m.Frag.File.Path)
		}
	}
	allowed := map[string]bool{}
	for _, id := range fresh.BookIDs {
		allowed[id] = true
	}
	for _, path := range paths {
		owners, err := database.BookFileRowsAtPathStrict(look, path)
		if err != nil {
			return repairs.Row{}, fmt.Errorf("who owns %s: %w", path, err)
		}
		for _, o := range owners {
			if !allowed[o.BookID] {
				return changedRow(planned, fmt.Sprintf("%s is also owned by book %s", path, o.BookID)), nil
			}
		}
	}
	return fresh, nil
}

// survivorOwnRow is the survivor's original row: the one at its own import
// path or, failing that, whose original filename matches its import path's.
func (f *fragmentFixer) survivorOwnRow(lib *fragLibrary, hist FragmentRepairReader, b fragBook) (fragFile, bool) {
	rows := lib.files[b.ID]
	if len(rows) == 1 {
		return rows[0], true
	}
	h, err := hist.GetBookPathHistory(b.ID)
	if err != nil {
		return fragFile{}, false
	}
	ip := importPathOf(h)
	for _, r := range rows {
		if r.Path == ip || r.Path == b.FilePath {
			return r, true
		}
	}
	return fragFile{}, false
}

// movedRowFor finds the row a partial run moved from fragment b onto the
// survivor: the survivor row at b's import path (or b's own book path).
func (f *fragmentFixer) movedRowFor(lib *fragLibrary, hist FragmentRepairReader, b fragBook, survivorID string) (fragFile, bool) {
	h, err := hist.GetBookPathHistory(b.ID)
	if err != nil {
		return fragFile{}, false
	}
	ip := importPathOf(h)
	for _, r := range lib.files[survivorID] {
		if (ip != "" && r.Path == ip) || r.Path == b.FilePath {
			return r, true
		}
	}
	return fragFile{}, false
}

// changedRow is the planned row with a fingerprint that cannot match, so the
// engine reports changed_since_plan with the reason in the row.
func changedRow(planned repairs.Row, why string) repairs.Row {
	r := planned
	r.Fingerprint = "changed:" + why
	r.Reason = why
	r.Detail = nil
	return r
}

// ---- apply ----------------------------------------------------------------

// Apply writes one fresh row. Every step is compare-and-set and journaled;
// a step already done (by a run cut off mid-row) is skipped.
func (f *fragmentFixer) Apply(ctx context.Context, w *repairs.Writer, fresh repairs.Row) error {
	store, _, err := f.stores()
	if err != nil {
		return err
	}
	// Held for the whole row, as fs-regroup-xml's fragment merge holds it:
	// a dedup merge must not interleave with rows moving between books and
	// books being retired.
	merge.LockMergeRMW()
	defer merge.UnlockMergeRMW()
	var steps int
	partial := func(err error) error {
		// A write that committed but whose undo row failed is a step: the row
		// is not unchanged, so it must not report plain failed.
		if steps == 0 && !errors.Is(err, repairs.ErrNotJournaled) {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %v", repairs.ErrPartiallyApplied, steps, err)
	}
	switch plan := fresh.Detail.(type) {
	case []fragPair:
		_, parentID, _ := strings.Cut(fresh.RowID, ":")
		for _, p := range plan {
			if err := ctx.Err(); err != nil {
				return partial(err)
			}
			if fresh.Class == fragClassMoved && !p.Done {
				to := undo.BookFileLocation{Path: p.Frag.File.Path, Missing: false, Hash: p.Frag.File.Hash, Size: p.Frag.File.Size}
				if err := w.RepointBookFile(parentID, p.Parent.ID, p.Parent.location(), to); err != nil {
					return partial(err)
				}
				steps++
			}
			did, err := f.retire(ctx, store, w, p.Frag.Book.ID, "fragment of "+parentID)
			if err != nil {
				return partial(err)
			}
			if did {
				steps++
			}
		}
		if fresh.Class == fragClassMoved {
			if err := w.Recompute(parentID); err != nil {
				return partial(fmt.Errorf("recompute %s: %w", parentID, err))
			}
		}
		return nil
	case *fragGroupPlan:
		for _, m := range plan.Members {
			if err := ctx.Err(); err != nil {
				return partial(err)
			}
			if m.Frag.Book.ID == plan.SurvivorID {
				continue
			}
			on, err := store.GetBookFiles(m.Frag.Book.ID)
			if err != nil {
				return partial(err)
			}
			if len(on) == 1 && on[0].ID == m.Frag.File.ID {
				if err := w.MoveBookFiles([]string{m.Frag.File.ID}, m.Frag.Book.ID, plan.SurvivorID); err != nil {
					return partial(err)
				}
				steps++
			} else if len(on) != 0 {
				return partial(fmt.Errorf("%w: fragment %s has rows other than the planned one", repairs.ErrChangedSincePlan, m.Frag.Book.ID))
			}
		}
		for _, m := range plan.Members {
			if m.Frag.File.Track != m.Track {
				if err := w.SetTrackNumber(plan.SurvivorID, m.Frag.File.ID, m.Frag.File.Track, m.Track); err != nil {
					return partial(err)
				}
				steps++
			}
		}
		if plan.Folder != "" {
			if did, err := f.setBookFolder(w, plan.SurvivorID, plan.Folder); err != nil {
				return partial(err)
			} else if did {
				steps++
			}
		}
		if plan.Title != "" {
			if did, err := f.retitle(store, w, plan.SurvivorID, plan.Title); err != nil {
				return partial(err)
			} else if did {
				steps++
			}
		}
		for _, m := range plan.Members {
			if m.Frag.Book.ID == plan.SurvivorID {
				continue
			}
			rows, err := store.GetBookFiles(m.Frag.Book.ID)
			if err != nil || len(rows) > 0 {
				// Never retire a book we cannot prove is empty.
				return partial(fmt.Errorf("fragment %s still owns %d row(s) (err=%v); not retired", m.Frag.Book.ID, len(rows), err))
			}
			did, err := f.retire(ctx, store, w, m.Frag.Book.ID, "consolidated into "+plan.SurvivorID)
			if err != nil {
				return partial(err)
			}
			if did {
				steps++
			}
		}
		if err := w.Recompute(plan.SurvivorID); err != nil {
			return partial(fmt.Errorf("recompute %s: %w", plan.SurvivorID, err))
		}
		return nil
	default:
		return fmt.Errorf("%s: row %s carries no plan", fragFixerID, fresh.RowID)
	}
}

// setBookFolder points the survivor's book path at the folder its files now
// share, journaled as a restorable book_path_update (nothing moves on disk).
func (f *fragmentFixer) setBookFolder(w *repairs.Writer, id, folder string) (bool, error) {
	var old string
	changed, err := w.Modify(id, func(b *database.Book) error {
		old = b.FilePath
		b.FilePath = folder
		return nil
	})
	if err != nil {
		return false, err
	}
	if len(changed) == 0 || old == folder {
		return false, nil
	}
	return true, w.Journal(id, undo.ChangeTypeBookPathUpdate, "file_path", old, folder)
}

// retitle sets the survivor's title unless it is locked, journaled as a
// restorable metadata_update.
func (f *fragmentFixer) retitle(store OpsStore, w *repairs.Writer, id, title string) (bool, error) {
	locks, err := database.LoadFieldLocks(store, id)
	if err != nil {
		return false, fmt.Errorf("field locks of %s unreadable: %w", id, err)
	}
	if locks.Locked(database.FieldKeyTitle) {
		return false, nil
	}
	var old string
	changed, err := w.Modify(id, func(b *database.Book) error {
		old = b.Title
		b.Title = title
		return nil
	})
	if err != nil {
		return false, err
	}
	if len(changed) == 0 {
		return false, nil
	}
	return true, w.Journal(id, "metadata_update", "title", old, title)
}

// retire demotes and soft-deletes one fragment book, journaling each step so
// the op revert restores it, and hands its version group on. It reports
// false when the book was already retired (a finished step). The fragment's
// book_file row is kept.
func (f *fragmentFixer) retire(ctx context.Context, store OpsStore, w *repairs.Writer, id, note string) (bool, error) {
	b, err := store.GetBookByID(id)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", id, err)
	}
	if b == nil {
		return false, fmt.Errorf("%w: fragment %s vanished", repairs.ErrChangedSincePlan, id)
	}
	if b.IsSoftDeleted() {
		return false, nil
	}
	wasPrimary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
	if wasPrimary {
		prev := ""
		if b.IsPrimaryVersion != nil {
			prev = "true"
		}
		notPrimary := false
		if _, err := w.Modify(id, func(cur *database.Book) error {
			cur.IsPrimaryVersion = &notPrimary
			return nil
		}); err != nil {
			return false, fmt.Errorf("demote %s: %w", id, err)
		}
		if err := w.Journal(id, undo.ChangeTypeBookPrimaryDemote, "is_primary_version", prev, "false"); err != nil {
			return false, err
		}
	}
	now := f.now()
	if _, err := w.Modify(id, func(cur *database.Book) error {
		t := true
		cur.MarkedForDeletion = &t
		cur.MarkedForDeletionAt = &now
		return nil
	}); err != nil {
		return false, fmt.Errorf("soft-delete %s: %w", id, err)
	}
	if err := w.Journal(id, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", note); err != nil {
		return false, err
	}
	if wasPrimary && b.VersionGroupID != nil && *b.VersionGroupID != "" {
		f.handOff(ctx, store, *b.VersionGroupID)
	}
	return true, nil
}

// handOff gives a retired fragment's version group a primary again, as
// fs-regroup-xml's retire does. Not journaled (undo has no promote kind): a
// revert restores the fragment and revertBookPrimaryDemote re-crowns it, so
// the group still ends with one primary. A failure is logged, not returned:
// the retirement itself is done and journaled.
func (f *fragmentFixer) handOff(ctx context.Context, store OpsStore, groupID string) {
	vps := f.p.deps.VersionPrimaryStore()
	if vps == nil {
		return
	}
	es := fragEnsureStore{OpsStore: store, chapters: vps}
	if _, err := versionprimary.EnsureSinglePrimary(ctx, es, groupID,
		versionprimary.Env{RootDir: config.AppConfig.RootDir}); err != nil {
		fragLog.Warn("%s: primary hand-off in group %s: %s", fragFixerID,
			logger.SanitizeLogValue(groupID), logger.SanitizeLogValue(err.Error()))
	}
}

// fragEnsureStore joins OpsStore and the chapter reader for
// versionprimary.EnsureSinglePrimary.
type fragEnsureStore struct {
	OpsStore
	chapters database.ChapterReader
}

func (s fragEnsureStore) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	return s.chapters.GetChaptersForBook(bookID)
}

var fragLog = logger.New("maintenance")
