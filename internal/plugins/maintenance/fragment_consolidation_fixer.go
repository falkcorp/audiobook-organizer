// file: internal/plugins/maintenance/fragment_consolidation_fixer.go
// version: 1.1.0
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
//     row at the file's current path (same row id, track and history) and
//     retires the fragment into the parent. A match resting on the original
//     name and size alone is a separate, skipped "moved-unproven" row unless
//     the fragment was also imported from the parent row's folder or the two
//     durations agree.
//   - copy: the fragment's file duplicates a file the parent still has on
//     disk. A proven match (imported FROM the parent row's path with the same
//     size on disk, or the same hash) is retired into the parent; an
//     unproven one is a separate, skipped "copy-unproven" row.
//   - no-parent: three or more fragments imported from one folder (sibling
//     CD1/CD2/Disc N folders count as one) that share a chapter key
//     (metadata.ChapterGroupKey, the scanner's rule), each shorter than the
//     chapter-consolidation threshold. The survivor is the lowest-id member
//     that is organized and primary (none: the row is skipped). Apply moves
//     every other member's row onto it in chapter order (disc first, then
//     the chapter number ChapterGroupKey stripped; a repeated or unknown
//     position skips the row), titles it from the folder (unless the title
//     is locked), points its path at the folder only when that folder's
//     audio is exactly the group's files, and retires the emptied members.
//   - manual-only: anything with a path under books/itunes/** (symlinks
//     resolved), Doctor Who / Big Finish / Torchwood by path or series, or a
//     fragment carrying an iTunes persistent id (book, row or itunes
//     external id): retiring it would queue an iTunes remove at the purge.
//     Listed, never applied.
//   - ambiguous: a fragment that matches two parents or two parent rows, or
//     a parent row two fragments claim. Listed, never applied.
//   - held: a fragment that matches a parent but whose own file is missing
//     or unreadable. Listed, never applied.
//
// RETIRING a fragment into its parent or survivor is what merge.Service does
// for an absorbed book: every user's listening state and positions follow it
// (merge.FollowAbsorbedJournaled, as a slice of the survivor's timeline), its
// external ids move over, it is demoted, and it is soft-deleted with
// merged_into_book_id set and its file_path CLEARED (that path is now a file
// the survivor owns, and the purge deletes a purged book's file_path).
//
// EVERY STEP IS UNDOABLE, AND JOURNALED FIRST. Each write goes through
// repairs.Writer, which records its OperationChange under the apply op's id
// BEFORE the write (see repairs/writer_files.go): book_file_repoint_location,
// book_file_reassign, book_file_track, book_path_update, metadata_update
// (title), user_state_follow, external_id_reassign, book_primary_demote,
// book_merged_into, book_soft_delete. POST /operations/<apply op>/revert
// undoes the lot; it restores a retired book only after the rows that moved
// its file elsewhere are back, and refuses it if one of them is refused.
// "Undo last apply" on a single book refuses a Repairs batch.
//
// RE-CHECKED UNDER THE LOCK. Apply holds merge.LockMergeRMW for the whole row
// and re-plans the row inside it (the whole-library listing, the strict
// ownership check and every plan-time read), refusing the row if its
// fingerprint moved. The title and folder writes compare against the values
// stored with the plan (Row.State).
//
// RESUME. The fingerprint hashes the DECISION (the class, the members, each
// planned file pairing, the survivor and its state, the title), not the raw
// row state, and Replan recognises its own finished steps. A no-parent row
// finds its members' rows by the row ids stored with the plan (Row.State),
// wherever a cut-off run left them. A plan made after an abandoned group run
// re-attributes each emptied member's row (journaled book_file_reassign) back
// to it, together with the rows of the members that same run already retired
// into the survivor, so the group re-forms rather than strands its emptied
// books; one that still lands in no row is listed as a held "stranded" row.
//
// CONCURRENCY. Plan evaluates fragment candidates on a bounded RunItems pool
// (point reads of path history, external ids and os.Stat). Apply runs through
// the framework engine, which partitions rows so that rows sharing any book
// land in one partition: no book is ever written by two workers.
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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
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

// Row id prefixes of a parent's UNPROVEN matches, so they never share a row
// (and a skip) with the proven ones.
const (
	fragRowCopyUnproven  = "copy-unproven"
	fragRowMovedUnproven = "moved-unproven"
)

// Skip kinds this fixer sets itself (the framework adds the guard kinds).
const (
	fragSkipAmbiguous       = "skipped_ambiguous"
	fragSkipCopyUnproven    = "skipped_copy_unproven"
	fragSkipMovedUnproven   = "skipped_moved_unproven"
	fragSkipDurationGate    = "skipped_duration_gate"
	fragSkipDurationUnknown = "skipped_duration_unknown"
	fragSkipFilesMissing    = "skipped_files_missing"
	fragSkipUnreadable      = "skipped_unreadable"
	fragSkipTrackOrder      = "skipped_track_order"
	fragSkipNoSurvivor      = "skipped_no_survivor"
	fragSkipStranded        = "skipped_stranded"
)

// Evidence kinds of a fragment-to-parent match, strongest first.
const (
	fragEvImportPath       = "import path equals the parent row's path"
	fragEvHash             = "file hash equals the parent row's"
	fragEvNameSizeFolder   = "original filename and size equal the parent row's, imported from the parent row's folder"
	fragEvNameSizeDuration = "original filename, size and duration equal the parent row's"
	fragEvNameSize         = "original filename and size equal the parent row's"
	fragEvDone             = "parent row already points at the fragment's file (finished step)"
)

// fragMinGroup is the scanner's consolidation minimum: fewer same-key files
// than this are not evidence of a chapter sequence.
const fragMinGroup = 3

// fragmentFixer implements repairs.Fixer.
type fragmentFixer struct {
	p *Plugin
	// statFn replaces os.Stat in tests.
	statFn func(string) (os.FileInfo, error)
	// readDir replaces os.ReadDir in tests.
	readDir func(string) ([]os.DirEntry, error)
	// now replaces time.Now in tests.
	now func() time.Time
}

func newFragmentFixer(p *Plugin) *fragmentFixer {
	return &fragmentFixer{p: p, statFn: os.Stat, readDir: os.ReadDir, now: time.Now}
}

var _ repairs.Fixer = (*fragmentFixer)(nil)

func (f *fragmentFixer) ID() string    { return fragFixerID }
func (f *fragmentFixer) Title() string { return "Chapter fragments" }
func (f *fragmentFixer) Description() string {
	return "Chapter and disc files an old scan imported as separate books. Moved: the parent's row points " +
		"at a path organize emptied — repoint it at the file's new path and retire the fragment into the parent. " +
		"Copy: the parent still has the file — retire the proven duplicate. No parent: 3+ short same-key chapters " +
		"from one folder — move every chapter onto one organized primary book in track order. Listening progress " +
		"and external ids follow each retired fragment. iTunes (including fragments with an iTunes id) and " +
		"Doctor Who / Big Finish / Torchwood are listed for manual action only. Every step is undoable from the " +
		"apply operation."
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
	// Organized: library_state == "organized". Primary: is_primary_version
	// is unset or true. Together they make a book electable as a survivor.
	Organized bool
	Primary   bool
	ITunesPID string // the book-level iTunes persistent id
	// MergedInto is merged_into_book_id ("" unset): which book a retired
	// fragment was folded into.
	MergedInto string
}

func fragBookFrom(id, title, path string, author, series *int, softDeleted bool, state *string, primary *bool, pid, merged *string) fragBook {
	b := fragBook{ID: id, Title: title, FilePath: path, AuthorID: author, SeriesID: series, SoftDeleted: softDeleted}
	if merged != nil {
		b.MergedInto = *merged
	}
	b.Organized = state != nil && *state == "organized"
	b.Primary = primary == nil || *primary
	if pid != nil {
		b.ITunesPID = *pid
	}
	return b
}

func fragBookOf(b *database.Book) fragBook {
	return fragBookFrom(b.ID, b.Title, b.FilePath, b.AuthorID, b.SeriesID, b.IsSoftDeleted(), b.LibraryState,
		b.IsPrimaryVersion, b.ITunesPersistentID, b.MergedIntoBookID)
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
	ITunesPID        string
}

func fragFileOf(bookID string, r *database.BookFile) fragFile {
	return fragFile{ID: r.ID, BookID: bookID, Path: r.FilePath, OriginalFilename: r.OriginalFilename, Size: r.FileSize,
		Hash: r.FileHash, OrigHash: r.OriginalFileHash, Duration: r.Duration, Track: r.TrackNumber, Missing: r.Missing,
		ITunesPID: r.ITunesPersistentID}
}

func fragFileOfCore(r *database.BookFileCore) fragFile {
	return fragFile{ID: r.ID, BookID: r.BookID, Path: r.FilePath, OriginalFilename: r.OriginalFilename, Size: r.FileSize,
		Hash: r.FileHash, OrigHash: r.OriginalFileHash, Duration: r.Duration, Track: r.TrackNumber, Missing: r.Missing,
		ITunesPID: r.ITunesPersistentID}
}

func (x fragFile) location() undo.BookFileLocation {
	return undo.BookFileLocation{Path: x.Path, Missing: x.Missing, Hash: x.Hash, Size: x.Size}
}

// fragCandidate is one fragment book: a live single-file book with chapter
// evidence.
type fragCandidate struct {
	Book       fragBook
	File       fragFile
	ExtIDs     []database.ExternalIDMapping
	ImportPath string // where the file was when the book was imported ("" unknown)
	OrigName   string // the pre-organize basename ("" unknown)
	Present    bool   // the fragment's own file is on disk
	DiskSize   int64  // its size on disk (-1 unknown)
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

// itunesPID names an iTunes persistent id the fragment carries, "" when it
// carries none. Retiring such a fragment would queue an iTunes remove for
// that id when the purge reaches the retired book.
func (c *fragCandidate) itunesPID() string {
	switch {
	case c.Book.ITunesPID != "":
		return "book iTunes id " + c.Book.ITunesPID
	case c.File.ITunesPID != "":
		return "file iTunes id " + c.File.ITunesPID
	}
	for _, e := range c.ExtIDs {
		if e.Source == "itunes" && e.ExternalID != "" && !e.Tombstoned {
			return "itunes external id " + e.ExternalID
		}
	}
	return ""
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
	name := strings.ToLower(filepath.Base(r.Path))
	ix.byName[name] = append(ix.byName[name], r)
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
// a finished step, the import path, the hash, then the pre-organize basename
// plus size (upgraded when the fragment was imported from the parent row's
// folder or the durations agree). Size alone never matches. Rows of the
// candidate's own book are ignored. A tier that yields a match ends the
// search.
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
		ev := fragEvNameSize
		switch {
		case c.ImportPath != "" && filepath.Dir(c.ImportPath) == filepath.Dir(r.Path):
			ev = fragEvNameSizeFolder
		case r.Duration > 0 && r.Duration == c.File.Duration:
			ev = fragEvNameSizeDuration
		}
		out = append(out, fragMatch{Row: r, Evidence: ev})
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

// provenMatch: evidence strong enough to repoint a parent row or retire a
// copy. A name-and-size match on its own never is. For a moved row a shared
// import folder or an equal duration also proves it (the parent's file is
// gone, so there is nothing else left to compare); a copy, whose parent file
// is still on disk, is proven only by the import path (with an equal size on
// disk) or a hash.
func provenMatch(kind, evidence string) bool {
	switch evidence {
	case fragEvImportPath, fragEvHash, fragEvDone:
		return true
	case fragEvNameSizeFolder, fragEvNameSizeDuration:
		return kind == fragClassMoved
	}
	return false
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
func (f *fragmentFixer) candidateFrom(store OpsStore, b fragBook, file fragFile, hist FragmentRepairReader) (*fragCandidate, bool, error) {
	c := &fragCandidate{Book: b, File: file, DiskSize: -1}
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
	exts, err := store.GetExternalIDsForBook(b.ID)
	if err != nil {
		// The iTunes-id guard reads these: fail rather than plan past it.
		return nil, false, fmt.Errorf("external ids of %s: %w", b.ID, err)
	}
	c.ExtIDs = exts
	if fi, err := f.statFn(file.Path); err == nil {
		c.Present = true
		c.DiskSize = fi.Size()
	} else if !errors.Is(err, fs.ErrNotExist) {
		c.StatErr = err.Error()
	}
	return c, true, nil
}

// fragLibrary is the library snapshot a plan reads.
type fragLibrary struct {
	books   map[string]fragBook
	files   map[string][]fragFile // by book id
	series  map[int]string
	authors map[int]string
	// reattributed lists the books attributeEmptied gave a row back to.
	reattributed []string
}

func newFragLibrary() *fragLibrary {
	return &fragLibrary{books: map[string]fragBook{}, files: map[string][]fragFile{}, series: map[int]string{}, authors: map[int]string{}}
}

func (f *fragmentFixer) loadLibrary(store OpsStore) (*fragLibrary, error) {
	lib := newFragLibrary()
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	for i := range books {
		b := &books[i]
		lib.books[b.ID] = fragBookFrom(b.ID, b.Title, b.FilePath, b.AuthorID, b.SeriesID, b.IsSoftDeleted(),
			b.LibraryState, b.IsPrimaryVersion, b.ITunesPersistentID, b.MergedIntoBookID)
	}
	cores, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("list book files: %w", err)
	}
	for i := range cores {
		r := &cores[i]
		lib.files[r.BookID] = append(lib.files[r.BookID], fragFileOfCore(r))
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

// moveRow re-attributes row id from one book to another in the snapshot.
func (lib *fragLibrary) moveRow(id, from, to string) bool {
	rows := lib.files[from]
	for i, r := range rows {
		if r.ID == id {
			lib.files[from] = append(rows[:i:i], rows[i+1:]...)
			r.BookID = to
			lib.files[to] = append(lib.files[to], r)
			return true
		}
	}
	return false
}

// fragReassign is one journaled, unreverted move of a book_file row onto
// owner: the row and the book it came from.
type fragReassign struct{ row, from string }

// emptiedRowOn finds the row a partly applied consolidation moved off book z:
// the row now owned by another live book at z's own path, whose move from z
// is journaled (book_file_reassign, OldValue z) and not reverted. It also
// returns every other unreverted reassign onto that owner under the SAME
// operation (the rest of the group's moves). owner is "" when there is none.
func emptiedRowOn(lib *fragLibrary, hist FragmentRepairReader, z fragBook) (owner, rowID string, same []fragReassign, err error) {
	owners, err := database.BookFileRowsAtPathStrict(hist, z.FilePath)
	if err != nil {
		return "", "", nil, fmt.Errorf("who owns %s: %w", z.FilePath, err)
	}
	for _, o := range owners {
		if o.BookID == z.ID {
			continue
		}
		ob, ok := lib.books[o.BookID]
		if !ok || ob.SoftDeleted {
			continue
		}
		changes, err := hist.GetBookChanges(o.BookID)
		if err != nil {
			return "", "", nil, fmt.Errorf("changes of %s: %w", o.BookID, err)
		}
		reassign := func(c *database.OperationChange) (string, bool) {
			if c.ChangeType != undo.ChangeTypeBookFileReassign || c.BookID != o.BookID || c.RevertedAt != nil {
				return "", false
			}
			return strings.CutPrefix(c.FieldName, "book_file:")
		}
		for _, c := range changes {
			if id, ok := reassign(c); !ok || id != o.ID || c.OldValue != z.ID {
				continue
			}
			for _, d := range changes {
				if id, ok := reassign(d); ok && d.OperationID == c.OperationID && d.OldValue != z.ID {
					same = append(same, fragReassign{row: id, from: d.OldValue})
				}
			}
			return o.BookID, o.ID, same, nil
		}
	}
	return "", "", nil, nil
}

// ---- plan -----------------------------------------------------------------

// Plan reads the whole library once, re-attributes the rows a partly applied
// group moved off its emptied members, evaluates every fragment candidate on
// a bounded pool, and builds one row per parent-and-class, per held fragment
// and per no-parent folder group.
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
	if err := f.attributeEmptied(ctx, rep, lib, store, hist, only); err != nil {
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
		c, ok, cerr := f.candidateFrom(store, singles[i].b, singles[i].file, hist)
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
	rows := f.buildRows(lib, ix, live)
	return append(rows, strandedRows(lib, rows)...), nil
}

// strandedRows lists, as held rows, every book attributeEmptied gave a row
// back to that no row of the plan includes: an emptied member of a partly
// applied group that no longer re-forms. It is listed so the owner can see it,
// never applied.
func strandedRows(lib *fragLibrary, rows []repairs.Row) []repairs.Row {
	in := map[string]bool{}
	for _, r := range rows {
		for _, id := range r.BookIDs {
			in[id] = true
		}
	}
	var out []repairs.Row
	for _, id := range lib.reattributed {
		if in[id] {
			continue
		}
		in[id] = true
		b := lib.books[id]
		why := "an interrupted consolidation moved this book's file onto another book and did not finish; the group no longer re-forms"
		out = append(out, repairs.Row{RowID: "stranded:" + id, Class: fragClassHeld, BookIDs: []string{id},
			Title: b.Title, Author: lib.authorName(b), Risk: repairs.RiskReview,
			Members: []repairs.RowMember{member(lib, b, "fragment")}, Reason: why,
			Skipped: fragSkipStranded, SkipReason: why + "; revert that apply operation or resolve by hand",
			Fingerprint: fragFingerprint("stranded", id)})
	}
	return out
}

// attributeEmptied puts back, in the snapshot only, each row a partly applied
// no-parent group moved off a member it did not get to retire: that member is
// then a single-file candidate again and the group re-forms with the same
// survivor, instead of the member being stranded as a live empty book.
func (f *fragmentFixer) attributeEmptied(ctx context.Context, rep registry.Reporter, lib *fragLibrary, store OpsStore, hist FragmentRepairReader, only map[string]bool) error {
	var empties []fragBook
	for id, b := range lib.books {
		if !b.SoftDeleted && b.FilePath != "" && len(lib.files[id]) == 0 && (only == nil || only[id]) {
			empties = append(empties, b)
		}
	}
	if len(empties) == 0 {
		return nil
	}
	sort.Slice(empties, func(i, j int) bool { return empties[i].ID < empties[j].ID })
	type retiredMember struct {
		row  string
		book fragBook
	}
	type found struct {
		owner, row string
		retired    []retiredMember
	}
	res := make([]found, len(empties))
	idx := make([]int, len(empties))
	for i := range idx {
		idx[i] = i
	}
	var done atomic.Int64
	// Each worker writes only res[i]; the snapshot is changed afterwards.
	if err := registry.RunItems(ctx, rep, idx, func(_ context.Context, i int) error {
		defer done.Add(1)
		owner, row, same, err := emptiedRowOn(lib, hist, empties[i])
		if err != nil {
			return err
		}
		fd := found{owner: owner, row: row}
		// The same run may already have retired other members into the
		// owner. Only members that the SAME operation moved onto the owner
		// and that name it as merged_into qualify, never a book some other
		// merge folded into it. They are read one by one: the library listing
		// leaves soft-deleted books out.
		for _, m := range same {
			b, err := store.GetBookByID(m.from)
			if err != nil {
				return fmt.Errorf("read %s: %w", m.from, err)
			}
			if b == nil || !b.IsSoftDeleted() || b.MergedIntoBookID == nil || *b.MergedIntoBookID != owner {
				continue
			}
			fd.retired = append(fd.retired, retiredMember{row: m.row, book: fragBookOf(b)})
		}
		res[i] = fd
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Emptied books %d/%d", done.Load(), total) },
	}); err != nil {
		return fmt.Errorf("%s: emptied books: %w", fragFixerID, err)
	}
	for i, r := range res {
		if r.row == "" {
			continue
		}
		lib.moveRow(r.row, r.owner, empties[i].ID)
		lib.reattributed = append(lib.reattributed, empties[i].ID)
		// Members the same run already retired are put back too (in the
		// snapshot only), so the group re-forms whole.
		for _, m := range r.retired {
			if lib.moveRow(m.row, r.owner, m.book.ID) {
				m.book.SoftDeleted = false
				lib.books[m.book.ID] = m.book
				lib.reattributed = append(lib.reattributed, m.book.ID)
			}
		}
	}
	return nil
}

// fragPair is one fragment matched to one parent row.
type fragPair struct {
	Frag     *fragCandidate
	Parent   fragFile
	Evidence string
	Done     bool // the parent row already points at the fragment's file
	// Slice is where the fragment's audio sits in the parent's timeline, for
	// carrying listening positions.
	Slice merge.SliceMapping
}

// sliceIn is where row target starts in a book whose rows are rows: the sum
// of the durations of the rows before it in track order. Not mappable when
// the track numbers are missing or repeated, or a preceding duration is
// unknown.
func sliceIn(rows []fragFile, target fragFile) merge.SliceMapping {
	if target.Track <= 0 {
		return merge.SliceMapping{}
	}
	seen := map[int]bool{}
	var off float64
	for _, r := range rows {
		if r.Track <= 0 || seen[r.Track] {
			return merge.SliceMapping{}
		}
		seen[r.Track] = true
		if r.Track < target.Track {
			if r.Duration <= 0 {
				return merge.SliceMapping{}
			}
			off += float64(r.Duration)
		}
	}
	return merge.SliceMapping{OffsetSeconds: off, Mappable: true}
}

// buildRows turns the evaluated candidates into rows. It is shared by Plan
// (over the whole library) and Replan (over one row's books), so both reach
// the same decision from the same state.
func (f *fragmentFixer) buildRows(lib *fragLibrary, ix *fragIndex, cands []*fragCandidate) []repairs.Row {
	type parentKey struct{ parent, kind string }
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
		p := fragPair{Frag: c, Parent: m.Row, Evidence: m.Evidence, Done: m.Evidence == fragEvDone,
			Slice: sliceIn(lib.files[m.Row.BookID], m.Row)}
		kind := fragClassMoved
		if !p.Done {
			fi, err := f.statFn(m.Row.Path)
			switch {
			case err == nil:
				kind = fragClassCopy
				// A path-proven copy must also be the same size on disk: the
				// parent's file may have been replaced since the import.
				if p.Evidence == fragEvImportPath && c.DiskSize >= 0 && fi.Size() != c.DiskSize {
					p.Evidence = fmt.Sprintf("%s, but the files differ in size on disk (%d vs %d bytes)", fragEvImportPath, fi.Size(), c.DiskSize)
				}
			case !errors.Is(err, fs.ErrNotExist):
				rows = append(rows, f.ambiguousRow(lib, c, "parent row path unreadable: "+err.Error(), []fragMatch{m}))
				continue
			}
		}
		if !provenMatch(kind, p.Evidence) {
			switch kind {
			case fragClassCopy:
				kind = fragRowCopyUnproven
			case fragClassMoved:
				kind = fragRowMovedUnproven
			}
		}
		k := parentKey{m.Row.BookID, kind}
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
		return keys[i].kind < keys[j].kind
	})
	for _, k := range keys {
		rows = append(rows, f.parentRow(lib, k.parent, k.kind, pairs[k]))
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
	if k, _ := f.guard(lib, books, map[string][]string{c.Book.ID: {c.ImportPath}}); k != "" || c.itunesPID() != "" {
		r.Class = fragClassManual
	}
	r.Fingerprint = fragFingerprint(idPrefix, c.Book.ID, why)
	return r
}

// parentRow is a moved or copy row: one parent and every fragment matched to
// it in that kind (proven or not).
func (f *fragmentFixer) parentRow(lib *fragLibrary, parentID, rowKind string, pairs []fragPair) repairs.Row {
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Frag.Book.ID < pairs[j].Frag.Book.ID })
	parent := lib.books[parentID]
	class := rowKind
	switch rowKind {
	case fragRowCopyUnproven:
		class = fragClassCopy
	case fragRowMovedUnproven:
		class = fragClassMoved
	}
	r := repairs.Row{RowID: rowKind + ":" + parentID, Class: class, Title: parent.Title,
		Author: lib.authorName(parent), Risk: repairs.RiskLow, Detail: pairs}
	r.BookIDs = []string{parentID}
	r.Members = []repairs.RowMember{member(lib, parent, "parent")}
	books := []fragBook{parent}
	extra := map[string][]string{}
	var fpParts []string
	done, withPID := 0, ""
	for _, p := range pairs {
		r.BookIDs = append(r.BookIDs, p.Frag.Book.ID)
		r.Members = append(r.Members, member(lib, p.Frag.Book, "fragment"))
		books = append(books, p.Frag.Book)
		extra[p.Frag.Book.ID] = []string{p.Frag.ImportPath}
		if p.Done {
			done++
		}
		if pid := p.Frag.itunesPID(); pid != "" && withPID == "" {
			withPID = fmt.Sprintf("fragment %s carries %s", p.Frag.Book.ID, pid)
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf("%s ← parent row %s (%s): %s",
			p.Frag.File.Path, p.Parent.ID, filepath.Base(p.Parent.Path), p.Evidence))
		// The pairing and whether it is proven, not the evidence text: a
		// finished repoint turns "import path" into "already points at it"
		// for the same decision.
		fpParts = append(fpParts, strings.Join([]string{p.Frag.Book.ID, p.Frag.File.ID, p.Frag.File.Path, p.Parent.ID,
			strconv.FormatBool(provenMatch(rowKind, p.Evidence))}, "|"))
	}
	sort.Strings(r.BookIDs)
	n := len(pairs)
	r.Current = map[string]string{
		"parent_files": strconv.Itoa(len(lib.files[parentID])),
		"fragments":    strconv.Itoa(n),
	}
	switch rowKind {
	case fragClassMoved, fragRowMovedUnproven:
		r.Current["stale_parent_rows"] = strconv.Itoa(n - done)
		r.Proposed = map[string]string{
			"action": fmt.Sprintf("repoint %d parent row(s) at the fragments' files; retire %d fragment book(s) into the parent", n-done, n),
		}
		r.Reason = fmt.Sprintf("%d of the parent's rows name paths that are gone; their files now sit under %d fragment book(s)", n, n)
		if rowKind == fragRowMovedUnproven {
			r.Risk = repairs.RiskReview
			r.Skipped = fragSkipMovedUnproven
			r.SkipReason = fmt.Sprintf("%d match(es) rest on the original name and size only (no import path, hash, shared folder or duration): check by hand before repointing", n)
		}
	case fragClassCopy, fragRowCopyUnproven:
		r.Proposed = map[string]string{"action": fmt.Sprintf("retire %d fragment book(s) into the parent (the parent keeps its own files; nothing is repointed)", n)}
		r.Reason = fmt.Sprintf("%d fragment book(s) duplicate files the parent still has on disk", n)
		if rowKind == fragRowCopyUnproven {
			r.Risk = repairs.RiskReview
			r.Skipped = fragSkipCopyUnproven
			r.SkipReason = fmt.Sprintf("%d match(es) are not proven by an import path with an equal size on disk, or a hash: check by hand before retiring", n)
		}
	}
	if k, why := f.guard(lib, books, extra); k != "" {
		r.Class, r.Skipped, r.SkipReason = fragClassManual, k, why
	} else if withPID != "" {
		r.Class, r.Skipped, r.SkipReason = fragClassManual, repairs.SkipITunes,
			withPID+"; retiring it would queue an iTunes remove at the purge"
	}
	r.Fingerprint = fragFingerprint(append([]string{rowKind, parentID}, fpParts...)...)
	return r
}

// fragGroupMember is one fragment of a no-parent group, in track order.
type fragGroupMember struct {
	Frag  *fragCandidate
	Track int
	// Offset is where the member's audio starts in the survivor's timeline.
	Offset float64
}

// fragGroupPlan is a no-parent row's decision.
type fragGroupPlan struct {
	Dir, Key   string
	SurvivorID string
	Title      string // "" keeps the survivor's title
	// Folder is the one folder every member's file sits in now, whose audio
	// files are exactly the group's, which becomes the survivor's book path
	// (a multi-file book's path is its folder). "" leaves the path alone.
	Folder string
	// WasTitle / WasPath are the survivor's title and path when the plan was
	// made (Row.State): the title and folder writes are compare-and-sets
	// against them.
	WasTitle, WasPath string
	Members           []fragGroupMember
}

// fragGroupState is a no-parent row's Row.State: what Replan needs from plan
// time.
type fragGroupState struct {
	// Files maps each member book to the book_file row it was planned with,
	// so Replan finds each member's row by id wherever a cut-off run left it.
	Files         map[string]string `json:"files"`
	SurvivorTitle string            `json:"survivor_title"`
	SurvivorPath  string            `json:"survivor_path"`
}

func thresholdSec() int {
	mins := config.AppConfig.ChapterConsolidationThresholdMin
	if mins <= 0 {
		mins = 10 // config's documented default; the scanner uses the same fallback
	}
	return mins * 60
}

// groupDir is the folder a fragment is grouped under: its import folder, or,
// for a disc folder ("CD1", "Book - Disc 2"), the parent folder joined with
// the name minus the marker, so sibling disc folders form one group. disc is
// the folder's disc number (0 for none).
func groupDir(c *fragCandidate) (dir string, disc int) {
	d := c.origDir()
	if rest, n, ok := metadata.DiscFolder(filepath.Base(d)); ok {
		parent := filepath.Dir(d)
		if rest == "" {
			return parent, n
		}
		return filepath.Join(parent, rest), n
	}
	return d, 0
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
		dir, _ := groupDir(c)
		gk := dir + "\x00" + key
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

// chapterPos is a member's position: the stem's, with a disc folder's number
// as the disc when the stem names none.
func chapterPos(c *fragCandidate) (metadata.ChapterPos, bool) {
	pos, ok := metadata.ChapterPosition(c.origStem())
	if _, disc := groupDir(c); disc > 0 && pos.Disc == 0 {
		pos.Disc = disc
		ok = true
	}
	return pos, ok
}

// exactFolder reports whether dir's audio files are exactly paths.
func (f *fragmentFixer) exactFolder(dir string, paths []string) bool {
	entries, err := f.readDir(dir)
	if err != nil {
		return false
	}
	want := map[string]bool{}
	for _, p := range paths {
		want[p] = true
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !linkintegrity.IsAudioFile(e.Name()) {
			continue
		}
		if !want[filepath.Join(dir, e.Name())] {
			return false
		}
		n++
	}
	return n == len(want)
}

func (f *fragmentFixer) noParentRow(lib *fragLibrary, dir, key string, cs []*fragCandidate) repairs.Row {
	type placed struct {
		c   *fragCandidate
		pos metadata.ChapterPos
		ok  bool
	}
	ps := make([]placed, len(cs))
	for i, c := range cs {
		pos, ok := chapterPos(c)
		ps[i] = placed{c, pos, ok}
	}
	sort.SliceStable(ps, func(i, j int) bool {
		if cmp := ps[i].pos.Compare(ps[j].pos); cmp != 0 {
			return cmp < 0
		}
		if ps[i].c.origStem() != ps[j].c.origStem() {
			return ps[i].c.origStem() < ps[j].c.origStem()
		}
		return ps[i].c.Book.ID < ps[j].c.Book.ID
	})
	orderProblem := ""
	for i, p := range ps {
		if !p.ok {
			orderProblem = fmt.Sprintf("%q carries no chapter position", p.c.origStem())
			break
		}
		if i > 0 && p.pos.Compare(ps[i-1].pos) == 0 {
			orderProblem = fmt.Sprintf("%q and %q claim the same chapter position", ps[i-1].c.origStem(), p.c.origStem())
			break
		}
	}

	plan := &fragGroupPlan{Dir: dir, Key: key}
	ids := make([]string, 0, len(cs))
	books := make([]fragBook, 0, len(cs))
	extra := map[string][]string{}
	var off float64
	for i, p := range ps {
		plan.Members = append(plan.Members, fragGroupMember{Frag: p.c, Track: i + 1, Offset: off})
		off += float64(p.c.File.Duration)
		ids = append(ids, p.c.Book.ID)
		books = append(books, p.c.Book)
		extra[p.c.Book.ID] = []string{p.c.ImportPath}
	}
	sort.Strings(ids)
	// The survivor: the lowest-id member that is organized and primary, so it
	// is a book ABS shows and a version group can keep.
	for _, id := range ids {
		if b := lib.books[id]; b.Organized && b.Primary {
			plan.SurvivorID = id
			break
		}
	}
	survivor := lib.books[plan.SurvivorID]
	survivorState := ""
	if plan.SurvivorID != "" {
		survivorState = fmt.Sprintf("organized=%t primary=%t", survivor.Organized, survivor.Primary)
	}
	// Title and Folder are decided from the group alone, never from whether
	// the survivor already has them: a run cut off after the retitle must
	// re-plan to the same fingerprint (the write is then a no-op).
	if t, _, ok := metadata.ChapterTitleFromDirectory(filepath.Join(dir, "x"), ""); ok {
		plan.Title = t
	}
	var paths []string
	for _, m := range plan.Members {
		paths = append(paths, m.Frag.File.Path)
	}
	plan.Folder = filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		if filepath.Dir(p) != plan.Folder {
			plan.Folder = ""
			break
		}
	}
	if plan.Folder != "" && !f.exactFolder(plan.Folder, paths) {
		// The folder holds other audio: pointing the book at it would let a
		// rescan's directory-book arrival absorb files that are not its own.
		plan.Folder = ""
	}
	r := repairs.Row{RowID: noParentRowID(dir, key), Class: fragClassNoParent, BookIDs: ids,
		Title: survivor.Title, Author: lib.authorName(survivor), Risk: repairs.RiskReview, Detail: plan}
	if plan.SurvivorID == "" {
		r.Title = lib.books[ids[0]].Title
	}
	if plan.Title != "" {
		r.Title = plan.Title
	}
	state := fragGroupState{Files: map[string]string{}, SurvivorTitle: survivor.Title, SurvivorPath: survivor.FilePath}
	var fpParts []string
	missing, unknown, long, withPID := 0, 0, 0, ""
	limit := thresholdSec()
	for _, m := range plan.Members {
		role := "fragment"
		if m.Frag.Book.ID == plan.SurvivorID {
			role = "survivor"
		}
		r.Members = append(r.Members, member(lib, m.Frag.Book, role))
		state.Files[m.Frag.Book.ID] = m.Frag.File.ID
		if !m.Frag.Present {
			missing++
		}
		switch d := m.Frag.File.Duration; {
		case d <= 0:
			unknown++
		case d >= limit:
			long++
		}
		if pid := m.Frag.itunesPID(); pid != "" && withPID == "" {
			withPID = fmt.Sprintf("member %s carries %s", m.Frag.Book.ID, pid)
		}
		fpParts = append(fpParts, fmt.Sprintf("%s|%s|%s|%d", m.Frag.Book.ID, m.Frag.File.ID, m.Frag.File.Path, m.Track))
	}
	if raw, err := json.Marshal(state); err == nil {
		r.State = raw
	}
	r.Evidence = []string{
		fmt.Sprintf("%d fragment books imported from %s share the chapter key %q", len(cs), dir, key),
		fmt.Sprintf("durations: %d known, %d unknown, %d at or over %d min", len(cs)-unknown, unknown, long, limit/60),
		"no existing book owns any of these files",
	}
	r.Current = map[string]string{"fragments": strconv.Itoa(len(cs)), "folder": dir}
	r.Proposed = map[string]string{
		"action":   fmt.Sprintf("move %d fragment row(s) onto %s in track order; retire %d emptied book(s) into it", len(cs)-1, plan.SurvivorID, len(cs)-1),
		"survivor": plan.SurvivorID,
	}
	if plan.Title != "" {
		r.Proposed["title"] = plan.Title
	}
	if plan.Folder != "" {
		r.Proposed["book_path"] = plan.Folder
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
	case orderProblem != "":
		r.Skipped, r.SkipReason = fragSkipTrackOrder, orderProblem+": the chapter order cannot be told"
	case plan.SurvivorID == "":
		r.Skipped, r.SkipReason = fragSkipNoSurvivor, "no member is both organized and primary, so none can take the others' files"
	}
	if k, why := f.guard(lib, books, extra); k != "" {
		r.Class, r.Skipped, r.SkipReason = fragClassManual, k, why
	} else if withPID != "" {
		r.Class, r.Skipped, r.SkipReason = fragClassManual, repairs.SkipITunes,
			withPID+"; retiring it would queue an iTunes remove at the purge"
	}
	r.Fingerprint = fragFingerprint(append([]string{fragClassNoParent, dir, key, plan.SurvivorID, survivorState,
		plan.Title, plan.Folder}, fpParts...)...)
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
	lib := newFragLibrary()
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
		lib.books[id] = fragBookOf(b)
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
			lib.files[id] = append(lib.files[id], fragFileOf(id, &rows[i]))
		}
	}
	class, rest, _ := strings.Cut(planned.RowID, ":")
	switch class {
	case fragClassMoved, fragClassCopy, fragRowCopyUnproven, fragRowMovedUnproven:
		return f.replanParent(store, lib, hist, planned, rest)
	case fragClassNoParent:
		return f.replanGroup(store, lib, hist, planned)
	default:
		return planned, nil // held and ambiguous rows are never applicable; return as planned
	}
}

func (f *fragmentFixer) replanParent(store OpsStore, lib *fragLibrary, hist FragmentRepairReader, planned repairs.Row, parentID string) (repairs.Row, error) {
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
		c, ok, err := f.candidateFrom(store, b, lib.files[id][0], hist)
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
	return changedRow(planned, "the fragments no longer match the parent in this kind"), nil
}

func (f *fragmentFixer) replanGroup(store OpsStore, lib *fragLibrary, hist FragmentRepairReader, planned repairs.Row) (repairs.Row, error) {
	var st fragGroupState
	if len(planned.State) == 0 || json.Unmarshal(planned.State, &st) != nil || len(st.Files) == 0 {
		return changedRow(planned, "the plan carries no stored member rows; plan again"), nil
	}
	survivorID := planned.Proposed["survivor"]
	// A partial run moved some members' rows onto the survivor. Each member
	// is rebuilt from the row it was PLANNED with, found by id wherever it
	// sits now (on the member, or on the survivor). A member holding any row
	// other than its own, or a survivor holding rows no member was planned
	// with, is a change.
	planned2 := map[string]bool{}
	for _, fid := range st.Files {
		planned2[fid] = true
	}
	for _, r := range lib.files[survivorID] {
		if !planned2[r.ID] {
			return changedRow(planned, fmt.Sprintf("survivor %s holds row %s the plan did not include", survivorID, r.ID)), nil
		}
	}
	var cands []*fragCandidate
	for _, id := range planned.BookIDs {
		b, ok := lib.books[id]
		if !ok {
			return changedRow(planned, fmt.Sprintf("fragment %s is gone", id)), nil
		}
		b.SoftDeleted = false
		fid := st.Files[id]
		var file *fragFile
		for _, owner := range []string{id, survivorID} {
			for i := range lib.files[owner] {
				if lib.files[owner][i].ID == fid {
					file = &lib.files[owner][i]
				}
			}
		}
		if file == nil {
			return changedRow(planned, fmt.Sprintf("member %s's planned row %s is on neither it nor the survivor", id, fid)), nil
		}
		if id != survivorID {
			for _, r := range lib.files[id] {
				if r.ID != fid {
					return changedRow(planned, fmt.Sprintf("member %s now has row %s besides its planned one", id, r.ID)), nil
				}
			}
		}
		one := *file
		one.BookID = id
		c, ok, err := f.candidateFrom(store, b, one, hist)
		if err != nil {
			return repairs.Row{}, err
		}
		if !ok {
			return changedRow(planned, fmt.Sprintf("fragment %s carries no chapter evidence now", id)), nil
		}
		cands = append(cands, c)
	}
	// The survivor's own snapshot lists only its planned row, so the member
	// counts and the folder check see the group as it was planned.
	for _, r := range f.noParentRows(lib, cands) {
		if r.RowID != planned.RowID {
			continue
		}
		if plan, ok := r.Detail.(*fragGroupPlan); ok {
			plan.WasTitle, plan.WasPath = st.SurvivorTitle, st.SurvivorPath
			// The survivor's title and path must still be what the plan saw,
			// or what this row's own finished step set, before anything moves.
			sb := lib.books[survivorID]
			if sb.Title != st.SurvivorTitle && (plan.Title == "" || sb.Title != plan.Title) {
				return changedRow(planned, fmt.Sprintf("survivor %s title is %q, not %q as planned", survivorID, sb.Title, st.SurvivorTitle)), nil
			}
			if sb.FilePath != st.SurvivorPath && (plan.Folder == "" || sb.FilePath != plan.Folder) {
				return changedRow(planned, fmt.Sprintf("survivor %s path is %q, not %q as planned", survivorID, sb.FilePath, st.SurvivorPath)), nil
			}
		}
		r.State = planned.State
		return f.checkOwners(hist, planned, r)
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

// Apply writes one fresh row. It takes merge.LockMergeRMW for the whole row
// and re-plans the row under it, so nothing a merge could change lands
// between the check and the writes. Every step is compare-and-set and
// journaled first; a step already done (by a run cut off mid-row) is skipped.
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
	var steps int
	partial := func(err error) error {
		if steps == 0 {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %v", repairs.ErrPartiallyApplied, steps, err)
	}
	switch plan := locked.Detail.(type) {
	case []fragPair:
		_, parentID, _ := strings.Cut(locked.RowID, ":")
		for _, p := range plan {
			if err := ctx.Err(); err != nil {
				return partial(err)
			}
			if locked.Class == fragClassMoved && !p.Done {
				to := undo.BookFileLocation{Path: p.Frag.File.Path, Missing: false, Hash: p.Frag.File.Hash, Size: p.Frag.File.Size}
				if err := w.RepointBookFile(parentID, p.Parent.ID, p.Parent.location(), to); err != nil {
					return partial(err)
				}
				steps++
			}
			did, err := f.retire(ctx, store, w, p.Frag.Book.ID, parentID, p.Slice, "fragment of "+parentID)
			steps += did
			if err != nil {
				return partial(err)
			}
		}
		if locked.Class == fragClassMoved {
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
			did, err := f.setBookFolder(store, w, plan.SurvivorID, plan.WasPath, plan.Folder)
			if err != nil {
				return partial(err)
			}
			if did {
				steps++
			}
		}
		if plan.Title != "" {
			did, err := f.retitle(store, w, plan.SurvivorID, plan.WasTitle, plan.Title)
			if err != nil {
				return partial(err)
			}
			if did {
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
			did, err := f.retire(ctx, store, w, m.Frag.Book.ID, plan.SurvivorID,
				merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true}, "consolidated into "+plan.SurvivorID)
			steps += did
			if err != nil {
				return partial(err)
			}
		}
		if err := w.Recompute(plan.SurvivorID); err != nil {
			return partial(fmt.Errorf("recompute %s: %w", plan.SurvivorID, err))
		}
		return nil
	default:
		return fmt.Errorf("%s: row %s carries no plan", fragFixerID, locked.RowID)
	}
}

// setBookFolder points the survivor's book path at the folder its files now
// share, journaled as a restorable book_path_update (nothing moves on disk),
// while the path is still the one the plan saw.
func (f *fragmentFixer) setBookFolder(store OpsStore, w *repairs.Writer, id, was, folder string) (bool, error) {
	b, err := store.GetBookByID(id)
	if err != nil || b == nil {
		return false, fmt.Errorf("read %s: %v", id, err)
	}
	if b.FilePath == folder {
		return false, nil
	}
	if b.FilePath != was {
		return false, fmt.Errorf("%w: survivor %s path is %q, not %q as planned", repairs.ErrChangedSincePlan, id, b.FilePath, was)
	}
	return true, w.Step(id, undo.ChangeTypeBookPathUpdate, "file_path", was, folder, func() error {
		_, err := w.Modify(id, func(cur *database.Book) error {
			if cur.FilePath != was {
				return repairs.ErrChangedSincePlan
			}
			cur.FilePath = folder
			return nil
		})
		return err
	})
}

// retitle sets the survivor's title unless it is locked, journaled as a
// restorable metadata_update, while the title is still the one the plan saw.
func (f *fragmentFixer) retitle(store OpsStore, w *repairs.Writer, id, was, title string) (bool, error) {
	locks, err := database.LoadFieldLocks(store, id)
	if err != nil {
		return false, fmt.Errorf("field locks of %s unreadable: %w", id, err)
	}
	if locks.Locked(database.FieldKeyTitle) {
		return false, nil
	}
	b, err := store.GetBookByID(id)
	if err != nil || b == nil {
		return false, fmt.Errorf("read %s: %v", id, err)
	}
	if b.Title == title {
		return false, nil
	}
	if b.Title != was {
		return false, fmt.Errorf("%w: survivor %s title is %q, not %q as planned", repairs.ErrChangedSincePlan, id, b.Title, was)
	}
	return true, w.Step(id, "metadata_update", "title", was, title, func() error {
		_, err := w.Modify(id, func(cur *database.Book) error {
			if cur.Title != was {
				return repairs.ErrChangedSincePlan
			}
			cur.Title = title
			return nil
		})
		return err
	})
}

// retire folds fragment id into target the way merge.Service retires an
// absorbed book, each step journaled first so the op revert restores it:
//
//  1. every user's listening state and positions follow onto target, as a
//     slice of its timeline (user_state_follow);
//  2. the fragment's external ids move to target (external_id_reassign); an
//     un-tombstoned iTunes id refuses the row instead;
//  3. a primary fragment is demoted (book_primary_demote, OldValue "true"
//     for an unset flag too, so the revert crowns it back and the group's
//     hand-off is undone with it);
//  4. one write sets merged_into_book_id (book_merged_into), CLEARS
//     file_path (book_path_update: the path is now a file target owns, and
//     the purge deletes a purged book's file_path) and soft-deletes it
//     (book_soft_delete).
//
// Its version group is then handed a primary. The fragment's book_file row,
// if it still has one, is kept. The count is the steps written; a book
// already soft-deleted counts none (the last step of an earlier run).
func (f *fragmentFixer) retire(ctx context.Context, store OpsStore, w *repairs.Writer, id, target string, slice merge.SliceMapping, note string) (int, error) {
	b, err := store.GetBookByID(id)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", id, err)
	}
	if b == nil {
		return 0, fmt.Errorf("%w: fragment %s vanished", repairs.ErrChangedSincePlan, id)
	}
	if b.IsSoftDeleted() {
		return 0, nil
	}
	if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
		return 0, fmt.Errorf("%w: fragment %s now carries an iTunes id", repairs.ErrChangedSincePlan, id)
	}
	steps := 0
	// 1. listening state
	did, err := f.followUserState(w, target, id, slice)
	if err != nil {
		return steps, fmt.Errorf("carry listening state of %s: %w", id, err)
	}
	steps += did
	// 2. external ids
	exts, err := store.GetExternalIDsForBook(id)
	if err != nil {
		return steps, fmt.Errorf("external ids of %s: %w", id, err)
	}
	for _, e := range exts {
		if e.Source == "itunes" && !e.Tombstoned {
			return steps, fmt.Errorf("%w: fragment %s now carries iTunes id %s", repairs.ErrChangedSincePlan, id, e.ExternalID)
		}
	}
	for _, e := range exts {
		if err := w.Step(id, undo.ChangeTypeExternalIDReassign, "external_id:"+e.Source+"/"+e.ExternalID, id, target, func() error {
			return store.ReassignExternalID(e.Source, e.ExternalID, target)
		}); err != nil {
			return steps, fmt.Errorf("move external id %s/%s to %s: %w", e.Source, e.ExternalID, target, err)
		}
		steps++
	}
	// 3. demote
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
	// 4. merged into, path cleared, soft-deleted: journaled, then one write.
	prevMerged := ""
	if b.MergedIntoBookID != nil {
		prevMerged = *b.MergedIntoBookID
	}
	if err := w.Journal(id, undo.ChangeTypeBookMergedInto, "merged_into_book_id", prevMerged, target); err != nil {
		return steps, err
	}
	if b.FilePath != "" {
		if err := w.Journal(id, undo.ChangeTypeBookPathUpdate, "file_path", b.FilePath, ""); err != nil {
			return steps, err
		}
	}
	now := f.now()
	if err := w.Step(id, undo.ChangeTypeBookSoftDelete, "marked_for_deletion", "", note, func() error {
		_, err := w.Modify(id, func(cur *database.Book) error {
			if cur.FilePath != b.FilePath {
				return fmt.Errorf("%w: fragment %s path changed during the retire", repairs.ErrChangedSincePlan, id)
			}
			t := true
			cur.MarkedForDeletion = &t
			cur.MarkedForDeletionAt = &now
			cur.MergedIntoBookID = &target
			cur.FilePath = ""
			return nil
		})
		return err
	}); err != nil {
		return steps, fmt.Errorf("soft-delete %s: %w", id, err)
	}
	steps++
	if wasPrimary && b.VersionGroupID != nil && *b.VersionGroupID != "" {
		f.handOff(ctx, store, *b.VersionGroupID)
	}
	return steps, nil
}

// followUserState carries every user's state and positions on the fragment
// onto target (merge.FollowAbsorbedJournaled, as a slice of target's
// timeline). The before-snapshot is journaled BEFORE anything moves and the
// before-and-after one after, both as user_state_follow rows; the revert of
// the newer one restores everything and the older one then finds nothing
// left to do. A store that cannot follow refuses only a fragment somebody has
// listened to.
func (f *fragmentFixer) followUserState(w *repairs.Writer, target, id string, slice merge.SliceMapping) (int, error) {
	um := f.p.deps.MergeUserStateStore()
	if um == nil {
		return 0, errors.New("user-state store unavailable")
	}
	record := func(progress []merge.CombineUserProgress) error {
		if len(progress) == 0 {
			return nil
		}
		raw, err := json.Marshal(progress)
		if err != nil {
			return err
		}
		v, err := json.Marshal(undo.UserStateFollowRecord{SyncRedirected: true, Progress: raw})
		if err != nil {
			return err
		}
		return w.Journal(id, undo.ChangeTypeUserStateFollow, undo.SurvivorField(target), "", string(v))
	}
	progress, _, err := merge.FollowAbsorbedJournaled(um, target, id, &slice, record)
	if errors.Is(err, merge.ErrNoSyncFollower) {
		has, herr := merge.BookHasUserProgress(um, id)
		if herr != nil {
			return 0, herr
		}
		if has {
			return 0, err
		}
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(progress) == 0 {
		return 0, nil
	}
	return 1, record(progress)
}

// handOff gives a retired fragment's version group a primary again, as
// fs-regroup-xml's retire does. The demote was journaled with OldValue
// "true", so its revert crowns the fragment (versionprimary.Crown) and
// demotes whichever sibling this hand-off promoted. A failure is logged, not
// returned: the retirement itself is done and journaled.
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
