// file: internal/plugins/maintenance/fragment_consolidation_fixer.go
// version: 1.18.0
// guid: 5c9e1a47-2b8d-4f63-a0e7-8d3b6f1c4e92
// last-edited: 2026-10-03

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
//     the fragment was also imported from the parent row's folder. An equal
//     duration proves nothing more: for a constant-bitrate file the size
//     already determines it.
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
//     a parent row two fragments claim. Listed, never applied. One exception
//     (owner, 2026-10-01): a fragment that is not itself an iTunes copy and
//     matches exactly one non-iTunes parent plus iTunes copies of it takes
//     the non-iTunes one as its single parent. The iTunes copies are listed
//     in the evidence and are never in the row's books, so never written.
//
// A plan with the read-only assume_retired param is a what-if: the snapshot
// is edited as if a duplicate-copies apply had retired its losers, and every
// row is skipped with its would-be outcome in Current["what_if"].
//   - ghost: a fragment whose own file is NOT on disk and that exactly one
//     parent row claims by proof (import path, hash, or the parent row
//     already pointing at the fragment's file). The parent's row is the
//     record of that file, so the fragment is a duplicate record of it: Apply
//     retires the fragment into the parent. Nothing is repointed (a missing
//     file is never marked present) and no row is deleted; the fragment keeps
//     its own missing row. Measured on prod 2026-10-03: 4,918 rows had sat in
//     "held" with exactly this proof.
//   - held: a fragment that matches a parent but whose own file is missing
//     or unreadable, and the proof above is not there (name and size only,
//     or two candidate parents). Listed, never applied.
//
// SAME PARENT ROW, SEVERAL CLAIMANTS (2026-10-03). A parent row that two
// fragments claim used to make both ambiguous — 7,627 rows on prod, every one
// against a single parent book (two copies of the same chapter file, one of
// them the organized copy the parent already points at). The claimants that
// prove their claim now pair with the parent as a lone claimant would (ghost,
// copy or moved); of the unproven ones, a single survivor takes the normal
// path and lands in copy-unproven / moved-unproven, while two or more stay
// ambiguous as before.
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
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/boilerplate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/linkintegrity"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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
	// fragClassGhost: a fragment whose own file is gone and that exactly one
	// parent row claims by proof; Apply retires it into the parent without
	// touching any row (see the file comment).
	fragClassGhost = "ghost"
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
	// fragSkipCoOwner: a file the row would fold is also a row of another
	// LIVE book that is not part of the row and is no path twin (a real
	// title, or facts that contradict the fragment's). Which book keeps the
	// file is the owner's decision; the row lists both.
	fragSkipCoOwner = "skipped_co_owner"
	// fragSkipNumberedUnsure: a folder's numbered files look like one
	// serial's chapters but fail a test that tells a chapter set from
	// several works in one folder (the numbering, the authors, the folder).
	// Listed so the owner sees the cluster; never applied.
	fragSkipNumberedUnsure = "skipped_numbered_set_unsure"
)

// Evidence kinds of a fragment-to-parent match, strongest first.
const (
	fragEvImportPath     = "import path equals the parent row's path"
	fragEvHash           = "file hash equals the parent row's"
	fragEvNameSizeFolder = "original filename and size equal the parent row's, imported from the parent row's folder"
	fragEvNameSize       = "original filename and size equal the parent row's"
	fragEvDone           = "parent row already points at the fragment's file (finished step)"
	// fragEvTwinPrefix opens the evidence of a fragment that matched no parent
	// row itself but shares its exact path with a fragment that did: two
	// single-row books registered for one file. The twin's evidence follows.
	fragEvTwinPrefix = "same path as fragment "
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
	// AssumeRetired makes the plan a read-only what-if: the library snapshot
	// is edited as if a duplicate-copies apply had already run (each loser
	// retired into its survivor, the fold rows moved onto the survivor)
	// before the parent index is built. Every row of such a plan is skipped
	// (fragSkipWhatIf) and carries the outcome it would have had in
	// Current["what_if"], so the unlock can be counted and nothing can be
	// applied from it.
	AssumeRetired *fragAssumeRetired `json:"assume_retired,omitempty"`
}

// fragAssumeRetired is the what-if of fragParams.AssumeRetired.
type fragAssumeRetired struct {
	// Retire maps each loser book to the survivor it would be retired into.
	Retire map[string]string `json:"retire"`
	// Fold lists the book_file row ids that would move from a loser onto its
	// survivor.
	Fold []string `json:"fold,omitempty"`
}

// fragSkipWhatIf marks every row of an assume_retired plan: never applied.
const fragSkipWhatIf = "skipped_what_if"

// fragWhatIfKey is the Row.Current key holding a what-if row's would-be
// outcome: "applicable", or the skip kind it would have had.
const fragWhatIfKey = "what_if"

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
	ASIN      string // for the iTunes-parent rule's identity gate (dcJudge)
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
	fb := fragBookFrom(b.ID, b.Title, b.FilePath, b.AuthorID, b.SeriesID, b.IsSoftDeleted(), b.LibraryState,
		b.IsPrimaryVersion, b.ITunesPersistentID, b.MergedIntoBookID)
	fb.ASIN = dcStr(b.ASIN)
	return fb
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
	ITunesPath       string
}

func fragFileOf(bookID string, r *database.BookFile) fragFile {
	return fragFile{ID: r.ID, BookID: bookID, Path: r.FilePath, OriginalFilename: r.OriginalFilename, Size: r.FileSize,
		Hash: r.FileHash, OrigHash: r.OriginalFileHash, Duration: r.Duration, Track: r.TrackNumber, Missing: r.Missing,
		ITunesPID: r.ITunesPersistentID, ITunesPath: r.ITunesPath}
}

func fragFileOfCore(r *database.BookFileCore) fragFile {
	return fragFile{ID: r.ID, BookID: r.BookID, Path: r.FilePath, OriginalFilename: r.OriginalFilename, Size: r.FileSize,
		Hash: r.FileHash, OrigHash: r.OriginalFileHash, Duration: r.Duration, Track: r.TrackNumber, Missing: r.Missing,
		ITunesPID: r.ITunesPersistentID, ITunesPath: r.ITunesPath}
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
// folder). Size alone never matches. Rows of the
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
		if c.ImportPath != "" && filepath.Dir(c.ImportPath) == filepath.Dir(r.Path) {
			ev = fragEvNameSizeFolder
		}
		out = append(out, fragMatch{Row: r, Evidence: ev})
	}
	return out
}

// fragEvTwin is the evidence a path twin adopts from fragment id.
func fragEvTwin(id, evidence string) string {
	return fragEvTwinPrefix + id + ", whose " + evidence
}

// twinEvidence returns the adopted evidence inside a twin's, if it is one.
func twinEvidence(evidence string) (inner string, ok bool) {
	rest, ok := strings.CutPrefix(evidence, fragEvTwinPrefix)
	if !ok {
		return "", false
	}
	_, inner, ok = strings.Cut(rest, ", whose ")
	return inner, ok
}

// twinContradicts reports whether anything the twin carries refutes it being
// the same chapter of the same parent as the donor at its path: hashes that
// share nothing, differing known sizes, differing original names, or import
// folders that differ. Facts the twin lacks contradict nothing.
func twinContradicts(twin, donor *fragCandidate) bool {
	switch {
	case hashesDisagree(twin.File, donor.File):
		return true
	case twin.File.Size > 0 && donor.File.Size > 0 && twin.File.Size != donor.File.Size:
		return true
	case twin.OrigName != "" && donor.OrigName != "" && !strings.EqualFold(twin.OrigName, donor.OrigName):
		return true
	case twin.ImportPath != "" && twin.ImportPath != twin.File.Path && donor.ImportPath != "" &&
		filepath.Dir(twin.ImportPath) != filepath.Dir(donor.ImportPath):
		return true
	}
	return false
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
// copy. A name-and-size match on its own never is, nor with an equal
// duration (a constant-bitrate file's size already fixes its duration). For a
// moved row a shared import folder also proves it (the parent's file is gone,
// so there is nothing else left to compare); a copy, whose parent file is
// still on disk, is proven only by the import path (with an equal size on
// disk) or a hash.
func provenMatch(kind, evidence string) bool {
	if inner, ok := twinEvidence(evidence); ok {
		return provenMatch(kind, inner)
	}
	switch evidence {
	case fragEvImportPath, fragEvHash, fragEvDone:
		return true
	case fragEvNameSizeFolder:
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
	// paths memoizes the guard's symlink resolution per folder for this
	// plan or re-plan.
	paths *repairs.PathResolver
	// itunes holds, for each candidate parent of a fragment matching two or
	// more parents, whether it is an iTunes copy (non-empty reason) and
	// itunesDoubt the parents that could not be told (an unreadable external
	// id list or path): the iTunes-parent rule (disregardITunesParents) only
	// acts when every parent is known.
	itunes      map[string]string
	itunesDoubt map[string]bool
	// verdicts are the owner's pair rejections (dcOwnerVerdicts) the rule's
	// identity gate honours; verdictsErr is why they could not be read, which
	// turns the rule off (the fragments stay ambiguous, and say why).
	verdicts    dcRejections
	verdictsErr string
	// roots are the library root and the import paths, cleaned: a folder
	// that IS one of them holds whatever was dropped there, never one work.
	roots []string
	// libraryRoot is the organized library's root ("" unset). A folder
	// directly under it is an author folder, whatever its name.
	libraryRoot string
}

// folderNamesAnyAuthor returns the author the folder is named for, among the
// authors the snapshot holds ("" none). The lowest id wins, so the answer
// does not depend on map order.
func (lib *fragLibrary) folderNamesAnyAuthor(folder string) string {
	best, name := 0, ""
	for id, a := range lib.authors {
		if (name == "" || id < best) && folderNamesAuthor(folder, a) {
			best, name = id, a
		}
	}
	return name
}

// loadRoots reads the library root and the import paths into the snapshot.
func (lib *fragLibrary) loadRoots(store OpsStore) error {
	if r := strings.TrimSpace(config.AppConfig.RootDir); r != "" {
		lib.libraryRoot = filepath.Clean(r)
		lib.roots = append(lib.roots, lib.libraryRoot)
	}
	imports, err := store.GetAllImportPaths()
	if err != nil {
		// Fail closed: a numbered set must not be formed in a root unseen.
		return fmt.Errorf("list import paths: %w", err)
	}
	for i := range imports {
		if p := strings.TrimSpace(imports[i].Path); p != "" {
			lib.roots = append(lib.roots, filepath.Clean(p))
		}
	}
	return nil
}

func newFragLibrary() *fragLibrary {
	return &fragLibrary{books: map[string]fragBook{}, files: map[string][]fragFile{}, series: map[int]string{},
		authors: map[int]string{}, paths: repairs.NewPathResolver(), itunes: map[string]string{},
		itunesDoubt: map[string]bool{}}
}

func (f *fragmentFixer) loadLibrary(store OpsStore) (*fragLibrary, error) {
	lib := newFragLibrary()
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("list books: %w", err)
	}
	for i := range books {
		b := &books[i]
		fb := fragBookFrom(b.ID, b.Title, b.FilePath, b.AuthorID, b.SeriesID, b.IsSoftDeleted(),
			b.LibraryState, b.IsPrimaryVersion, b.ITunesPersistentID, b.MergedIntoBookID)
		fb.ASIN = dcStr(b.ASIN)
		lib.books[b.ID] = fb
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
	authors, err := store.GetAllAuthors()
	if err != nil {
		// The numbered-set rule compares a folder with its files' author:
		// fail rather than plan with that test silently off.
		return nil, fmt.Errorf("list authors: %w", err)
	}
	for _, a := range authors {
		lib.authors[a.ID] = a.Name
	}
	if err := lib.loadRoots(store); err != nil {
		return nil, err
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
	if fp.AssumeRetired != nil {
		lib.assumeRetired(fp.AssumeRetired)
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
	if err := f.resolveITunesParents(ctx, rep, store, lib, ix, live); err != nil {
		return nil, err
	}
	rows := f.buildRows(lib, ix, live)
	holdCoOwned(lib, rows)
	rows = append(rows, strandedRows(lib, rows)...)
	if fp.AssumeRetired != nil {
		whatIf(rows)
	}
	return rows, nil
}

// assumeRetired edits the snapshot (only) as if a duplicate-copies apply had
// run: each fold row moves from the book holding it onto that book's
// survivor, and each loser is soft-deleted, so it drops out of the parent
// index. Read-only: nothing is written.
func (lib *fragLibrary) assumeRetired(a *fragAssumeRetired) {
	fold := map[string]bool{}
	for _, id := range a.Fold {
		fold[id] = true
	}
	losers := make([]string, 0, len(a.Retire))
	for loser := range a.Retire {
		losers = append(losers, loser)
	}
	sort.Strings(losers)
	for _, loser := range losers {
		survivor := a.Retire[loser]
		var move []string
		for _, r := range lib.files[loser] {
			if fold[r.ID] {
				move = append(move, r.ID)
			}
		}
		for _, id := range move {
			lib.moveRow(id, loser, survivor)
		}
		if b, ok := lib.books[loser]; ok {
			b.SoftDeleted = true
			b.MergedInto = survivor
			lib.books[loser] = b
		}
	}
}

// whatIf turns every row of an assume_retired plan into a never-applied row
// that records the outcome it would have had.
func whatIf(rows []repairs.Row) {
	for i := range rows {
		r := &rows[i]
		would := "applicable"
		if r.Skipped != "" {
			would = r.Skipped
		}
		if r.Current == nil {
			r.Current = map[string]string{}
		}
		r.Current[fragWhatIfKey] = would
		r.Skipped = fragSkipWhatIf
		r.SkipReason = "what-if plan (assume_retired): would be " + would + "; never applied from this plan"
	}
}

// resolveITunesParents settles, on a bounded pool, whether each candidate
// parent of a fragment that matches two or more parent books is an iTunes
// copy (lib.itunes) or cannot be told (lib.itunesDoubt). Only those parents
// are read: the iTunes-parent rule needs nothing else.
func (f *fragmentFixer) resolveITunesParents(ctx context.Context, rep registry.Reporter, store OpsStore, lib *fragLibrary, ix *fragIndex, cands []*fragCandidate) error {
	want := map[string]bool{}
	for _, c := range cands {
		ms := ix.match(c)
		parents := map[string]bool{}
		for _, m := range ms {
			parents[m.Row.BookID] = true
		}
		if len(parents) > 1 {
			for p := range parents {
				want[p] = true
			}
		}
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		f.loadVerdicts(store, lib, nil)
	}
	type verdict struct {
		why   string
		doubt bool
	}
	out := make([]verdict, len(ids))
	var done atomic.Int64
	// Each worker writes only out[i]; the PathResolver is safe for
	// concurrent use, the snapshot is read-only here.
	if err := registry.RunItems(ctx, rep, fbIndexes(len(ids)), func(_ context.Context, i int) error {
		defer done.Add(1)
		why, doubt := lib.itunesParentWhy(store, ids[i])
		out[i] = verdict{why: why, doubt: doubt}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("iTunes parents %d/%d", done.Load(), total) },
	}); err != nil {
		return fmt.Errorf("%s: iTunes parents: %w", fragFixerID, err)
	}
	for i, id := range ids {
		switch {
		case out[i].doubt:
			lib.itunesDoubt[id] = true
		case out[i].why != "":
			lib.itunes[id] = out[i].why
		}
	}
	return nil
}

// loadVerdicts reads the owner's pair verdicts the iTunes-parent rule's
// identity gate honours: the whole library's (ids nil, Plan) or those of ids
// (a Replan). A read failure turns the rule off; it is logged once here and
// named on each row the rule would have acted on.
func (f *fragmentFixer) loadVerdicts(store OpsStore, lib *fragLibrary, ids []string) {
	v, err := dcOwnerVerdicts(f.p.deps.DedupVerdictReader(), store, ids)
	if err != nil {
		lib.verdictsErr = err.Error()
		fragLog.Warn("%s: the owner's dedup verdicts are unreadable, so the iTunes-parent rule is off for this plan: %s",
			fragFixerID, lib.verdictsErr)
		return
	}
	lib.verdicts = v
}

// itunesParentWhy tells whether parent book id is an iTunes copy, from the
// snapshot and its external ids. doubt: it cannot be told.
func (lib *fragLibrary) itunesParentWhy(store OpsStore, id string) (why string, doubt bool) {
	b := lib.books[id]
	paths := []string{b.FilePath}
	var rows []fragFile
	for _, r := range lib.files[id] {
		paths = append(paths, r.Path)
		rows = append(rows, r)
	}
	exts, err := store.GetExternalIDsForBook(id)
	if err != nil {
		return "", true
	}
	return itunesCopyWhy(lib.paths, id, b.ITunesPID, paths, rows, exts)
}

// dcBookOf is parent book id as the duplicate-copies identity gate reads it.
func (lib *fragLibrary) dcBookOf(id string) *dcBook {
	b := lib.books[id]
	asin := b.ASIN
	d := &dcBook{Core: database.BookCore{ID: id, ASIN: &asin}, Title: dcTitleKey(b.Title), Author: dcAuthorKey(lib.authors, b.AuthorID)}
	for _, r := range lib.files[id] {
		d.Rows = append(d.Rows, database.BookFileCore{ID: r.ID, BookID: id, FilePath: r.Path, Duration: r.Duration,
			FileHash: r.Hash, OriginalFileHash: r.OrigHash})
	}
	return d
}

// disregardITunesParents is the owner's 2026-10-01 rule: when a fragment
// that is not itself an iTunes copy matches exactly one non-iTunes parent
// book plus one or more iTunes copies of it, the non-iTunes parent is its
// single parent. It returns the matches to that parent and the iTunes
// parents set aside (never written: they are not in the row's books).
//
// "Copies of it" is proven, not assumed: every iTunes parent set aside must
// pass the duplicate-copies identity gate against the kept parent (dcJudge:
// the same track-stripped title, a compatible author, no ASIN conflict, no
// owner rejection, 90%+ of the smaller copy hash-matched). Two different works
// sharing one file (an intro, a sting) are not copies, and the fragment stays
// ambiguous. Neither does the rule act for a fragment that is no evidence of
// a work: under 60 s, or an intro/credits title.
//
// ok is false when the rule does not apply (a parent the plan could not tell,
// two non-iTunes parents, no iTunes parent, an iTunes or non-chapter
// fragment, an iTunes parent not proven a copy, or verdicts unreadable).
// note says why when the rule would have acted but could not read the
// owner's verdicts; the fragment's ambiguous row carries it.
func (f *fragmentFixer) disregardITunesParents(lib *fragLibrary, c *fragCandidate, ms []fragMatch) (kept []fragMatch, ignored []string, ok bool, note string) {
	if c.itunesPID() != "" || c.File.ITunesPath != "" {
		return nil, nil, false, ""
	}
	if !dcLinkRow(database.BookFileCore{ID: c.File.ID, FilePath: c.File.Path, Duration: c.File.Duration}) ||
		boilerplate.IsBoilerplateTitle(c.Book.Title) {
		return nil, nil, false, ""
	}
	if k, _ := f.guard(lib, []fragBook{c.Book}, map[string][]string{c.Book.ID: {c.ImportPath}}); k == repairs.SkipITunes || k == repairs.SkipGuardUnreadable {
		return nil, nil, false, ""
	}
	keep := ""
	seen := map[string]bool{}
	for _, m := range ms {
		p := m.Row.BookID
		if seen[p] {
			continue
		}
		seen[p] = true
		switch {
		case lib.itunesDoubt[p]:
			return nil, nil, false, ""
		case lib.itunes[p] != "":
			ignored = append(ignored, p)
		case keep != "":
			return nil, nil, false, "" // two non-iTunes parents
		default:
			keep = p
		}
	}
	if keep == "" || len(ignored) == 0 {
		return nil, nil, false, ""
	}
	// The rule would act here: say so on the row when it cannot (S2), rather
	// than leave a fragment ambiguous with no word of why.
	if lib.verdictsErr != "" {
		return nil, nil, false, "the iTunes-parent rule could not run: the owner's dedup verdicts are unreadable (" + lib.verdictsErr + ")"
	}
	kb := lib.dcBookOf(keep)
	for _, it := range ignored {
		if dcJudge(kb, lib.dcBookOf(it), lib.verdicts).Kind != dcEdgeProven {
			return nil, nil, false, ""
		}
	}
	for _, m := range ms {
		if m.Row.BookID == keep {
			kept = append(kept, m)
		}
	}
	sort.Strings(ignored)
	return kept, ignored, true, ""
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
	// IgnoredITunes are the iTunes copies of the parent the fragment also
	// matched, set aside by the iTunes-parent rule (disregardITunesParents).
	// They are listed, never in the row's books and never written.
	IgnoredITunes []string
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
	// matchOf is each candidate's matches after the iTunes-parent rule, and
	// ignoredOf the iTunes parents that rule set aside.
	matchOf := map[*fragCandidate][]fragMatch{}
	ignoredOf := map[*fragCandidate][]string{}

	for _, c := range cands {
		ms := ix.match(c)
		parents := map[string]bool{}
		for _, m := range ms {
			parents[m.Row.BookID] = true
		}
		note := ""
		if len(parents) > 1 {
			kept, ignored, ok, why := f.disregardITunesParents(lib, c, ms)
			if ok {
				ms, ignoredOf[c] = kept, ignored
				parents = map[string]bool{kept[0].Row.BookID: true}
			} else {
				note = why
			}
		}
		matchOf[c] = ms
		switch {
		case len(ms) == 0:
			unmatched = append(unmatched, c)
		case c.StatErr != "":
			rows = append(rows, f.holdRow(lib, c, fragClassHeld, fragClassHeld, fragSkipUnreadable,
				fmt.Sprintf("file %s is unreadable (%s)", c.File.Path, c.StatErr), ms))
		case !c.Present && len(ms) == 1 && provenMatch(fragClassGhost, ms[0].Evidence):
			// A ghost: the parent's row is the record of this file. Paired
			// below like any lone claim; the kind is decided in pairFor.
			claims[ms[0].Row.ID] = append(claims[ms[0].Row.ID], c)
		case !c.Present:
			rows = append(rows, f.holdRow(lib, c, fragClassHeld, fragClassHeld, fragSkipFilesMissing,
				fmt.Sprintf("file %s is not on disk", c.File.Path), ms))
		case len(parents) > 1 || len(ms) > 1:
			why := fmt.Sprintf("matches %d rows of %d parent books", len(ms), len(parents))
			if note != "" {
				why += "; " + note
			}
			rows = append(rows, f.ambiguousRow(lib, c, why, ms))
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
			// Proven claimants pair as a lone claimant would; the unproven
			// ones are ambiguous among themselves unless only one is left.
			var proven, unproven []*fragCandidate
			for _, c := range cs {
				if provenMatch(fragClassGhost, matchOf[c][0].Evidence) {
					proven = append(proven, c)
				} else {
					unproven = append(unproven, c)
				}
			}
			if len(unproven) > 1 {
				for _, c := range unproven {
					rows = append(rows, f.ambiguousRow(lib, c, fmt.Sprintf("parent row %s is claimed by %d fragments", rid, len(cs)), matchOf[c]))
				}
				unproven = nil
			}
			cs = append(proven, unproven...)
		}
		moved := 0
		for _, c := range cs {
			m := matchOf[c][0]
			kind, p, hold := f.pairFor(c, m, lib, ignoredOf[c])
			if hold != "" {
				rows = append(rows, f.ambiguousRow(lib, c, hold, []fragMatch{m}))
				continue
			}
			if kind == fragClassMoved || kind == fragRowMovedUnproven {
				// One parent row can be repointed at one file. A second
				// present fragment claiming the same gone row is listed for
				// the next plan: once the first is repointed it is a plain
				// copy of a file the parent has again.
				if moved++; moved > 1 && !p.Done {
					rows = append(rows, f.ambiguousRow(lib, c,
						fmt.Sprintf("parent row %s is claimed by %d present fragments; one is repointed per plan, plan again for this one", rid, len(cs)), []fragMatch{m}))
					continue
				}
			}
			k := parentKey{m.Row.BookID, kind}
			pairs[k] = append(pairs[k], p)
		}
	}
	// A path twin: an unmatched fragment whose row names the exact path of a
	// fragment paired as a ghost or a copy joins that pair's row. Two
	// single-row books registered for one file ("02" beside "Eldest - 02")
	// carry one file's evidence between them; the one without it used to fall
	// out of the plan silently and then, as a live co-owner of the path, make
	// its sibling's row refused (checkOwners: "also owned by book") — 21 of
	// 48 applicable rows on prod 2026-10-03. Only proven kinds take a twin
	// (ghost, copy, moved — never an unproven row). A moved row repoints the
	// parent row at the donor's file, which is the twin's file too, so the
	// twin adds nothing to repoint: Apply repoints a parent row once per row
	// and retires both. The twin's own facts must not contradict the donor's
	// (hash, size, original name, import folder), and exactly one donor pair
	// must name the path.
	donors := map[string][]struct {
		k parentKey
		p fragPair
	}{}
	for k, ps := range pairs {
		if k.kind != fragClassGhost && k.kind != fragClassCopy && k.kind != fragClassMoved {
			continue
		}
		for _, p := range ps {
			donors[p.Frag.File.Path] = append(donors[p.Frag.File.Path], struct {
				k parentKey
				p fragPair
			}{k, p})
		}
	}
	var stillUnmatched []*fragCandidate
	for _, c := range unmatched {
		ds := donors[c.File.Path]
		if c.StatErr != "" || len(ds) != 1 || twinContradicts(c, ds[0].p.Frag) {
			stillUnmatched = append(stillUnmatched, c)
			continue
		}
		d := ds[0]
		if (d.k.kind == fragClassGhost) != !c.Present {
			// The same path cannot be both on disk and gone; a disagreement
			// means the snapshot moved under the plan.
			stillUnmatched = append(stillUnmatched, c)
			continue
		}
		twin := d.p
		twin.Frag, twin.Done = c, false
		twin.Evidence = fragEvTwin(d.p.Frag.Book.ID, d.p.Evidence)
		pairs[d.k] = append(pairs[d.k], twin)
	}
	unmatched = stillUnmatched
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

// pairFor decides the kind of one fragment's claim on one parent row: ghost
// when the fragment's own file is gone, moved when the parent row's path is
// gone, copy when both are on disk; an unproven moved or copy claim gets its
// unproven row kind. A non-empty hold is a reason the claim cannot be decided
// (the parent row's path is unreadable) and the fragment is listed ambiguous.
func (f *fragmentFixer) pairFor(c *fragCandidate, m fragMatch, lib *fragLibrary, ignored []string) (kind string, p fragPair, hold string) {
	p = fragPair{Frag: c, Parent: m.Row, Evidence: m.Evidence, Done: m.Evidence == fragEvDone,
		Slice: sliceIn(lib.files[m.Row.BookID], m.Row), IgnoredITunes: ignored}
	if !c.Present {
		return fragClassGhost, p, ""
	}
	kind = fragClassMoved
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
			return "", p, "parent row path unreadable: " + err.Error()
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
	return kind, p, ""
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
		if k, w := repairs.GuardBookPathsWith(lib.paths, b.ID, paths, lib.seriesName(b)); k != "" {
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
	var ignored []string
	for _, p := range pairs {
		ignored = append(ignored, p.IgnoredITunes...)
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
		for _, it := range p.IgnoredITunes {
			r.Evidence = append(r.Evidence, fmt.Sprintf("fragment %s also matches iTunes copy %s (%s): disregarded, never written",
				p.Frag.Book.ID, it, lib.itunes[it]))
		}
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
			r.SkipReason = fmt.Sprintf("%d match(es) rest on the original name and size only (no import path, hash or shared import folder): check by hand before repointing", n)
		}
	case fragClassGhost:
		r.Proposed = map[string]string{"action": fmt.Sprintf("retire %d fragment book(s) into the parent: their files are not on disk and the parent's rows are the record of them (nothing is repointed, no row is deleted)", n)}
		r.Reason = fmt.Sprintf("%d fragment book(s) duplicate the record of a file the parent already has a row for; the fragments' own files are gone", n)
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
	if len(ignored) > 0 {
		// Replan re-reads these and re-runs the rule's identity gate on them.
		ignored = uniqueSorted(ignored)
		st, err := json.Marshal(fragParentState{IgnoredITunes: ignored})
		if err != nil {
			r.Skipped, r.SkipReason = fragSkipUnreadable, "cannot store the iTunes parents set aside: "+err.Error()
		}
		r.State = st
		fpParts = append(fpParts, "itunes-set-aside|"+strings.Join(ignored, ","))
	}
	r.Fingerprint = fragFingerprint(append([]string{rowKind, parentID}, fpParts...)...)
	return r
}

// fragParentState is a parent row's Row.State: the iTunes copies of the
// parent the iTunes-parent rule set aside, which Replan reloads so the rule
// (and its identity gate, and the owner's verdicts) is decided again under
// the apply's lock.
type fragParentState struct {
	IgnoredITunes []string `json:"ignored_itunes,omitempty"`
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
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

// importChapterSec is the IMPORT scanner's chapter threshold in seconds
// (chapter_consolidation_threshold_min, default 10). The folder-books fixer
// reads it as "a group shorter than this is a fragment, not a book".
func importChapterSec() int {
	mins := config.AppConfig.ChapterConsolidationThresholdMin
	if mins <= 0 {
		mins = 10 // config's documented default; the scanner uses the same fallback
	}
	return mins * 60
}

// repairChapterMaxSec is the longest a file may run and still be a chapter
// when this fixer folds fragments into one book (repair_chapter_max_min,
// default 120). It used to be the import threshold above; the owner split
// them 2026-10-03 so the jobs could use 120 minutes without changing what
// the scanner groups on import.
func repairChapterMaxSec() int {
	mins := config.AppConfig.RepairChapterMaxMin
	if mins <= 0 {
		mins = 120 // the documented default; never "disabled"
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

// fragNumberedKey is the group key of a numbered set: a folder's leading-
// numbered files taken together whatever follows the number. It cannot
// collide with a chapter key, which is lower-case text.
const fragNumberedKey = "\x01numbered"

// numberedSet is one folder's leading-numbered fragments taken as a serial's
// chapters. problem is "" when every test passed, else why the folder may
// hold something other than one work's chapters.
type numberedSet struct {
	dir     string
	members []*fragCandidate
	problem string
	// small is set instead of problem when the only objection is the size.
	small string
	// sideBySide: the problem is that the folder holds works side by side
	// (two files at one position, a key block, only multi-part names). Those
	// works are what the key groups find, so the key groups decide. Any
	// other problem doubts the folder as a whole, key groups included.
	sideBySide bool
	// keyGroups are the chapter keys the key-group rule may still take from a
	// sideBySide folder: three or more files with consecutive numbers ("01-03
	// - Book A"), or any key when the folder has disc folders (the tested
	// disc path). Scattered same-named chapters ("04, 06, 08 - Intro") are
	// not a work of their own and are not among them.
	keyGroups map[string]bool
}

// fragNumberedMin is the fewest files a numbered set is applied with. Three
// short numbered files with different names are as likely a shelf of short
// works ("1 - Green Eggs and Ham", "2 - The Cat in the Hat") or a few
// podcast episodes as a serial, and nothing recorded tells them apart; the
// serials this rule is for have dozens to hundreds of chapters.
const fragNumberedMin = 8

// authorFolderSuffixRe drops what a folder adds to an author's name.
var authorFolderSuffixRe = regexp.MustCompile(`(?i)[\s,_-]+(?:collection|collected works|complete works|works|short stories|stories|anthology|omnibus)$`)

// folderNamesAuthor reports whether a folder is named for the author rather
// than for a work: the same name tokens in any order ("Author, Jane"), or the
// surname with the other names as initials ("J. Author"), with a collection
// suffix ignored ("Jane Author Collection").
func folderNamesAuthor(folder, author string) bool {
	tokens := func(s string) []string {
		return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	}
	at := tokens(author)
	ft := tokens(authorFolderSuffixRe.ReplaceAllString(strings.TrimSpace(folder), ""))
	if len(at) == 0 || len(ft) == 0 || len(ft) > len(at) {
		return false
	}
	surname := at[len(at)-1]
	if !slices.Contains(ft, surname) {
		return false
	}
	for _, f := range ft {
		ok := false
		for _, a := range at {
			if f == a || (len(f) == 1 && strings.HasPrefix(a, f)) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// numberedSets finds, per import folder, the candidate numbered chapter
// sets: at least fragMinGroup unmatched fragments whose stems open with a
// chapter number and carry at least two different chapter keys
// ("070 - Skating", "047 - Core", "002 - Arc Part 1"). A serial's chapters
// each have their own title, so ChapterGroupKey keys them apart and no key
// group ever forms; 4,966 such books were left on prod on 2026-10-03
// (SenescentSoul 262, Anansi Boys 55).
//
// Numbers alone do not make a serial. Each test below answers a shape that
// merged different works in review (2026-10-03), and a set that fails one
// carries the reason in problem:
//
//   - a library root or import path as the folder: whatever was dropped
//     there ("downloads/01 - Green Eggs", "02 - Some Podcast");
//   - a folder directly under the library root, or named for the files'
//     author: an author folder holds several works;
//   - a file in a disc folder: "CD1/01 - Alpha", "CD2/01 - Beta" never
//     collide on position, so nothing would tell three works apart;
//   - two files with one chapter number: a duplicate file, or two works
//     that both start at 01;
//   - members by different authors or of different series;
//   - numbering that does not run from 0 or 1 without large gaps: years and
//     title numbers ("1632 - …", "1984 - …", "2001 - …") are not chapters;
//   - a chapter key whose files sit together as one block covering half the
//     set or more ("01-03 - Book A", then "04 - Book B"), or every key with
//     two or more files and no key alone ("01-02 - Book A", "03-04 - Book
//     B"): works side by side. Three chapters named alike inside a long
//     serial are not such a block and stay in the set;
//   - a member that is a published work in its own right: it carries an
//     ASIN, or a title that is not its file name;
//   - fewer than fragNumberedMin files.
func numberedSets(lib *fragLibrary, cands []*fragCandidate) []numberedSet {
	type entry struct {
		c   *fragCandidate
		key string
		pos metadata.ChapterPos
		num int
	}
	byDir := map[string][]entry{}
	discDir := map[string]bool{}
	for _, c := range cands {
		if !metadata.HasLeadingChapterNumber(c.origStem()) {
			continue
		}
		key, _ := metadata.ChapterGroupKey(c.origStem())
		pos, ok := chapterPos(c)
		if !ok || len(pos.Parts) == 0 {
			continue
		}
		dir, disc := groupDir(c)
		if disc > 0 || pos.Disc > 0 {
			discDir[dir] = true
		}
		byDir[dir] = append(byDir[dir], entry{c, key, pos, pos.Parts[0]})
	}
	var dirs []string
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	var sets []numberedSet
	for _, dir := range dirs {
		es := byDir[dir]
		if len(es) < fragMinGroup {
			continue
		}
		keyN := map[string]int{}
		for _, e := range es {
			keyN[e.key]++
		}
		if len(keyN) < 2 {
			continue // one key: the key group already covers it
		}
		// The order noParentRow gives its members: position, stem, book id.
		sort.SliceStable(es, func(i, j int) bool {
			if cmp := es[i].pos.Compare(es[j].pos); cmp != 0 {
				return cmp < 0
			}
			if a, b := es[i].c.origStem(), es[j].c.origStem(); a != b {
				return a < b
			}
			return es[i].c.Book.ID < es[j].c.Book.ID
		})
		set := numberedSet{dir: dir}
		for _, e := range es {
			set.members = append(set.members, e.c)
		}
		first := es[0].c.Book
		lo, hi := es[0].num, es[len(es)-1].num
		clean := filepath.Clean(dir)
		switch {
		case slices.Contains(lib.roots, clean):
			set.problem = fmt.Sprintf("%s is a library root or import path: it holds whatever was put there, not one work", dir)
		case lib.libraryRoot != "" && filepath.Dir(clean) == lib.libraryRoot:
			set.problem = fmt.Sprintf("%s sits directly under the library root: an author folder holds several works", dir)
		case folderNamesAuthor(filepath.Base(clean), lib.authorName(first)):
			set.problem = fmt.Sprintf("the folder %q is named for the files' author (%q): an author folder holds several works", filepath.Base(clean), lib.authorName(first))
		case first.AuthorID == nil && lib.folderNamesAnyAuthor(filepath.Base(clean)) != "":
			// Files with no author linked, in a folder named like an author
			// the library knows: still an author folder.
			set.problem = fmt.Sprintf("the folder %q is named like the author %q: an author folder holds several works", filepath.Base(clean), lib.folderNamesAnyAuthor(filepath.Base(clean)))
		case discDir[dir]:
			// Disc folders keep the behaviour they had: the key groups
			// decide (a book's discs share one key), never a numbered set.
			set.sideBySide = true
			set.problem = "some of the files sit in disc folders or carry a disc number: a numbered set is not formed across discs"
		}
		for i := 1; i < len(es) && set.problem == ""; i++ {
			e := es[i]
			switch {
			case e.num == es[i-1].num && e.pos.Disc == es[i-1].pos.Disc:
				// The leading number, not the whole position: "05 - Ash" and
				// "05 - Ash (1)" are one chapter twice (a second download),
				// "01 - Book A" and "01 - Book B" two works.
				set.sideBySide = true
				set.problem = fmt.Sprintf("%q and %q carry the same chapter number: a duplicate file, or more than one work in the folder", es[i-1].c.origStem(), e.c.origStem())
			case !sameIntPtr(e.c.Book.AuthorID, first.AuthorID):
				set.problem = fmt.Sprintf("the files are by different authors (%q, %q)", lib.authorName(first), lib.authorName(e.c.Book))
			case !sameIntPtr(e.c.Book.SeriesID, first.SeriesID):
				set.problem = fmt.Sprintf("the files belong to different series (%q, %q)", lib.seriesName(first), lib.seriesName(e.c.Book))
			}
		}
		if set.problem == "" && (lo > 1 || float64(hi-lo+1) > 1.25*float64(len(es))) {
			set.problem = fmt.Sprintf("the numbers run %d to %d over %d files: not a chapter run from 0 or 1 without large gaps", lo, hi, len(es))
		}
		if set.problem == "" {
			// Works side by side: one key's files all adjacent and at least
			// half the set, or no key standing alone.
			single := 0
			for _, n := range keyN {
				if n == 1 {
					single++
				}
			}
			if single == 0 {
				set.sideBySide = true
				set.problem = fmt.Sprintf("every one of the %d names in the folder is carried by two or more files: several multi-part works, not one work's chapters", len(keyN))
			}
			for i := 0; i < len(es) && set.problem == ""; {
				j := i
				for j < len(es) && es[j].key == es[i].key {
					j++
				}
				if run := j - i; run >= fragMinGroup && run == keyN[es[i].key] && 2*run >= len(es) {
					set.sideBySide = true
					set.problem = fmt.Sprintf("the %d files keyed %q sit together as one block (%q … %q) and are half the folder or more: a work of its own beside the others",
						run, es[i].key, es[i].c.origStem(), es[j-1].c.origStem())
				}
				i = j
			}
		}
		for _, e := range es {
			if set.problem != "" {
				break
			}
			b := e.c.Book
			stem := e.c.origStem()
			switch {
			case b.ASIN != "":
				set.problem = fmt.Sprintf("%q carries its own ASIN (%s): a published work, not a chapter", stem, b.ASIN)
			case !metadata.IsChapterOnlyTitle(b.Title) && !chapterTitleIsStem(b.Title, stem):
				set.problem = fmt.Sprintf("%q is titled %q, not after its file: a work with a title of its own, not a chapter", stem, b.Title)
			}
		}
		if set.sideBySide {
			set.keyGroups = map[string]bool{}
			nums := map[string][]int{}
			for _, e := range es {
				nums[e.key] = append(nums[e.key], e.num)
			}
			for key, ns := range nums {
				if len(ns) < fragMinGroup {
					continue
				}
				run := true
				sort.Ints(ns)
				for i := 1; i < len(ns); i++ {
					if ns[i] != ns[i-1]+1 {
						run = false
					}
				}
				if run || discDir[dir] {
					set.keyGroups[key] = true
				}
			}
		}
		if set.problem == "" && len(es) < fragNumberedMin {
			// Not a failed test: the run may well be one work. It is held,
			// and its files are not handed to the key groups either.
			set.small = fmt.Sprintf("only %d files: fewer than %d cannot be told from a shelf of short works; merge by hand if they are one work", len(es), fragNumberedMin)
		}
		sets = append(sets, set)
	}
	return sets
}

// chapterTitleIsStem reports whether a book's title is just its file's name:
// the stem itself, or the stem without its leading number, by letters and
// digits. That is what the filename fallback gives a chapter file; a tagged
// work carries a title of its own.
func chapterTitleIsStem(title, stem string) bool {
	key := func(s string) string {
		var sb strings.Builder
		for _, r := range strings.ToLower(s) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				sb.WriteRune(r)
			}
		}
		return sb.String()
	}
	t := key(title)
	if t == key(stem) {
		return true
	}
	rest, _ := metadata.ChapterGroupKey(stem)
	return rest != "" && t == key(rest)
}

// noParentRows groups unmatched fragments by import folder and chapter key,
// and a folder's differently-titled numbered files as one numbered set.
//
// A set found to be works side by side (numberedSet.sideBySide) is left to
// the key groups, exactly as before the rule existed; the key rows then say
// how many other numbered files the folder holds. Every other set keeps all
// its files in ONE row whatever that row's state: applicable, held for a
// test it failed (an author folder, mixed authors, too few files, …), held
// for missing, unknown or long files, or for a folder that gives no title.
// Three same-named chapters of a numbered run are never handed to a key
// group to become a partial book under the folder's name; before this rule
// they were, wherever a folder's numbered files carried two or more names.
func (f *fragmentFixer) noParentRows(lib *fragLibrary, cands []*fragCandidate) []repairs.Row {
	var rows []repairs.Row
	inSet := map[*fragCandidate]bool{}
	besides := map[string][]*fragCandidate{} // dir -> numbered files of a set left to the key groups
	for _, set := range numberedSets(lib, cands) {
		if set.sideBySide && len(set.keyGroups) > 0 {
			// The works side by side are the key groups' to take. The other
			// numbered files of the folder go to no group: scattered
			// same-named chapters are not a work.
			besides[set.dir] = set.members
			for _, c := range set.members {
				if key, _ := metadata.ChapterGroupKey(c.origStem()); !set.keyGroups[key] {
					inSet[c] = true
				}
			}
			continue
		}
		row := f.noParentRow(lib, set.dir, fragNumberedKey, set.members)
		if row.Class != fragClassManual {
			switch {
			case set.problem != "":
				row.Skipped, row.SkipReason = fragSkipNumberedUnsure, set.problem
			case set.small != "":
				row.Skipped, row.SkipReason = fragSkipNumberedUnsure, set.small
			case row.Proposed["title"] == "":
				row.Skipped, row.SkipReason = fragSkipNumberedUnsure,
					"the folder gives no title for the work (a generic folder, or one directly under a root); the survivor would keep one chapter's name"
			}
		}
		for _, c := range set.members {
			inSet[c] = true
		}
		rows = append(rows, row)
	}
	groups := map[string][]*fragCandidate{}
	for _, c := range cands {
		if inSet[c] {
			continue
		}
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
	for _, gk := range keys {
		cs := groups[gk]
		if len(cs) < fragMinGroup {
			continue
		}
		dir, key, _ := strings.Cut(gk, "\x00")
		row := f.noParentRow(lib, dir, key, cs)
		in := map[*fragCandidate]bool{}
		for _, c := range cs {
			in[c] = true
		}
		others := 0
		for _, c := range besides[dir] {
			if !in[c] {
				others++
			}
		}
		if others > 0 {
			row.Evidence = append(row.Evidence, fmt.Sprintf(
				"%d other numbered file(s) from this folder are not in this row: the folder was not taken as one numbered set", others))
		}
		rows = append(rows, row)
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
	limit := repairChapterMaxSec()
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
	shared := fmt.Sprintf("%d fragment books imported from %s share the chapter key %q", len(cs), dir, key)
	var stems []string
	if key == fragNumberedKey {
		keyN := map[string]int{}
		bigKey := ""
		for _, m := range plan.Members {
			k, _ := metadata.ChapterGroupKey(m.Frag.origStem())
			if keyN[k]++; keyN[k] > keyN[bigKey] || bigKey == "" {
				bigKey = k
			}
			if len(stems) < 12 {
				stems = append(stems, m.Frag.origStem())
			}
		}
		first, last := plan.Members[0].Frag, plan.Members[len(plan.Members)-1].Frag
		shared = fmt.Sprintf("%d fragment books imported from %s are numbered chapters with titles of their own (%q … %q): %d different chapter keys, the commonest %q on %d file(s)",
			len(cs), dir, first.origStem(), last.origStem(), len(keyN), bigKey, keyN[bigKey])
	}
	r.Evidence = []string{
		shared,
		fmt.Sprintf("durations: %d known, %d unknown, %d at or over %d min", len(cs)-unknown, unknown, long, limit/60),
		"no existing book owns any of these files",
	}
	if len(stems) > 0 {
		more := ""
		if len(cs) > len(stems) {
			more = fmt.Sprintf(" … and %d more", len(cs)-len(stems))
		}
		r.Evidence = append(r.Evidence, "in order: "+strings.Join(stems, " | ")+more)
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
	if err := lib.loadRoots(store); err != nil {
		return repairs.Row{}, err
	}
	for _, id := range planned.BookIDs {
		if err := ctx.Err(); err != nil {
			return repairs.Row{}, err
		}
		if err := replanLoad(store, lib, id); err != nil {
			return repairs.Row{}, err
		}
	}
	class, rest, _ := strings.Cut(planned.RowID, ":")
	switch class {
	case fragClassMoved, fragClassCopy, fragClassGhost, fragRowCopyUnproven, fragRowMovedUnproven:
		return f.replanParent(store, lib, hist, planned, rest)
	case fragClassNoParent:
		return f.replanGroup(store, lib, hist, planned)
	default:
		return planned, nil // held and ambiguous rows are never applicable; return as planned
	}
}

// replanLoad reads book id and its rows into lib (a book that is gone is
// left out).
func replanLoad(store OpsStore, lib *fragLibrary, id string) error {
	b, err := store.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("read %s: %w", id, err)
	}
	if b == nil {
		return nil
	}
	lib.books[id] = fragBookOf(b)
	if b.AuthorID != nil {
		a, aerr := store.GetAuthorByID(*b.AuthorID)
		if aerr != nil {
			return fmt.Errorf("read author %d of %s: %w", *b.AuthorID, id, aerr)
		}
		if a != nil {
			lib.authors[a.ID] = a.Name
		}
	}
	rows, err := store.GetBookFiles(id)
	if err != nil {
		return fmt.Errorf("read files of %s: %w", id, err)
	}
	for i := range rows {
		lib.files[id] = append(lib.files[id], fragFileOf(id, &rows[i]))
	}
	return nil
}

func (f *fragmentFixer) replanParent(store OpsStore, lib *fragLibrary, hist FragmentRepairReader, planned repairs.Row, parentID string) (repairs.Row, error) {
	// The iTunes copies the rule set aside at plan time come back as
	// candidate parents, so the rule is decided again here: each is
	// re-classified, its identity gate re-run against the parent, and the
	// owner's verdicts on those books re-read (a not_dup or dismissal added
	// since the plan makes the fragment ambiguous, and the row changes).
	var ps fragParentState
	if len(planned.State) > 0 {
		if err := json.Unmarshal(planned.State, &ps); err != nil {
			return changedRow(planned, "the plan's stored iTunes parents are unreadable; plan again"), nil
		}
	}
	for _, it := range ps.IgnoredITunes {
		if err := replanLoad(store, lib, it); err != nil {
			return repairs.Row{}, err
		}
		if _, ok := lib.books[it]; !ok {
			continue
		}
		why, doubt := lib.itunesParentWhy(store, it)
		switch {
		case doubt:
			lib.itunesDoubt[it] = true
		case why != "":
			lib.itunes[it] = why
		}
	}
	if len(ps.IgnoredITunes) > 0 {
		f.loadVerdicts(store, lib, append([]string{parentID}, ps.IgnoredITunes...))
	}
	// The parent must still be a live multi-file book: another fixer (the
	// duplicate-copies apply retires losers) may have retired it since the
	// plan, and a fragment must never be folded into a dead book.
	if pb, ok := lib.books[parentID]; !ok || pb.SoftDeleted {
		return changedRow(planned, fmt.Sprintf("parent %s is gone or retired", parentID)), nil
	} else if len(lib.files[parentID]) < 2 {
		return changedRow(planned, fmt.Sprintf("parent %s no longer owns 2+ rows", parentID)), nil
	}
	ix := newFragIndex()
	for _, r := range lib.files[parentID] {
		ix.add(r)
	}
	for _, it := range ps.IgnoredITunes {
		for _, r := range lib.files[it] {
			ix.add(r)
		}
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
			return f.checkOwners(store, hist, planned, r)
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
		return f.checkOwners(store, hist, planned, r)
	}
	return changedRow(planned, "the fragments no longer form this group"), nil
}

// checkOwners re-checks, with the strict lookup, that every file the fresh
// row touches is owned only by books of the row. A file some other book also
// claims makes the row changed (the plan's whole-library listing no longer
// holds); an incomplete lookup fails the row rather than guessing.
func (f *fragmentFixer) checkOwners(store OpsStore, look database.BookFilePathLookup, planned, fresh repairs.Row) (repairs.Row, error) {
	if !fresh.Applicable() {
		return fresh, nil
	}
	paths := rowPaths(fresh)
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
			if allowed[o.BookID] {
				continue
			}
			// A retired book keeps its rows (no book_file row is ever deleted),
			// so a soft-deleted owner is history, not a live claim. Measured
			// on prod 2026-10-03: 26 of 29 rows were refused because an
			// iTunes copy retired by an earlier apply still named the path.
			ob, err := store.GetBookByID(o.BookID)
			if err != nil {
				return repairs.Row{}, fmt.Errorf("owner %s of %s: %w", o.BookID, path, err)
			}
			if ob == nil || ob.IsSoftDeleted() {
				continue
			}
			return changedRow(planned, fmt.Sprintf("%s is also owned by book %s", path, o.BookID)), nil
		}
	}
	return fresh, nil
}

// rowPaths lists the fragment files a row folds: the paths checkOwners
// guards at apply and holdCoOwned reads at plan.
func rowPaths(r repairs.Row) []string {
	var paths []string
	switch d := r.Detail.(type) {
	case []fragPair:
		for _, p := range d {
			paths = append(paths, p.Frag.File.Path)
		}
	case *fragGroupPlan:
		for _, m := range d.Members {
			paths = append(paths, m.Frag.File.Path)
		}
	}
	return paths
}

// holdCoOwned turns an applicable row into a held one when a file it folds
// is also a row of a live book outside the row. checkOwners refuses exactly
// these at apply ("also owned by book X"), where the reason was visible only
// in the apply op's result; five rows (about 400 fragment books: Eldest 313,
// Foundation 74) were planned applicable and refused on every apply of
// 2026-10-03. A co-owner that is a path twin is already in the row; what is
// left carries a real title ("Prelude to Foundation") or facts that
// contradict the fragment's, so it may be a second edition, and which book
// keeps the file is the owner's decision (owner, 2026-10-03). The co-owner is
// listed as a member with its role, never in BookIDs: the row writes nothing
// to it. The row keeps its class; the skip kind is what holds it. Plan only: it needs the whole library, and a held row is never
// re-planned.
func holdCoOwned(lib *fragLibrary, rows []repairs.Row) {
	owners := map[string][]string{} // path -> live books holding a row at it
	for id, files := range lib.files {
		if b, ok := lib.books[id]; !ok || b.SoftDeleted {
			continue
		}
		for _, r := range files {
			owners[r.Path] = append(owners[r.Path], id)
		}
	}
	for i := range rows {
		r := &rows[i]
		if !r.Applicable() {
			continue
		}
		in := map[string]bool{}
		for _, id := range r.BookIDs {
			in[id] = true
		}
		at := map[string][]string{} // co-owner -> the row's paths it also holds
		for _, path := range rowPaths(*r) {
			for _, id := range owners[path] {
				if !in[id] && !contains(at[id], path) {
					at[id] = append(at[id], path)
				}
			}
		}
		if len(at) == 0 {
			continue
		}
		var ids []string
		for id := range at {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		listed := map[string]bool{}
		for _, m := range r.Members {
			listed[m.BookID] = true
		}
		shared := map[string]bool{}
		var names []string
		for _, id := range ids {
			b := lib.books[id]
			if !listed[id] {
				r.Members = append(r.Members, member(lib, b, "co-owner"))
			}
			sort.Strings(at[id])
			for _, path := range at[id] {
				shared[path] = true
				r.Evidence = append(r.Evidence, fmt.Sprintf("%s is also a file of book %s (%q)", path, id, b.Title))
			}
			names = append(names, fmt.Sprintf("%s (%q)", id, b.Title))
		}
		// The class stays what it was (moved, copy, no-parent): the class
		// chips keep counting the row where the owner looks for it, and the
		// skip kind says why it is held.
		r.Risk, r.Skipped = repairs.RiskReview, fragSkipCoOwner
		r.SkipReason = fmt.Sprintf("%d file(s) of this row are also owned by %d live book(s) outside it: %s; decide which book keeps each file (merge or retire the other by hand), then plan again",
			len(shared), len(ids), strings.Join(names, ", "))
	}
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
	var steps int
	partial := func(err error) error {
		if steps == 0 {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %w", repairs.ErrPartiallyApplied, steps, err)
	}
	switch plan := locked.Detail.(type) {
	case []fragPair:
		_, parentID, _ := strings.Cut(locked.RowID, ":")
		// A parent row is repointed once per row, and from the pair that
		// carries the file's own facts: a path twin's pair names the same
		// parent row and the same file as its donor's but has no hash or
		// size of its own (that is what made it a twin), and the row's pairs
		// are sorted by book id, so the twin may come first.
		target := map[string]fragPair{}
		for _, p := range plan {
			if _, twin := twinEvidence(p.Evidence); twin {
				if _, ok := target[p.Parent.ID]; !ok {
					target[p.Parent.ID] = p
				}
				continue
			}
			target[p.Parent.ID] = p
		}
		repointed := map[string]bool{}
		for _, p := range plan {
			if err := ctx.Err(); err != nil {
				return partial(err)
			}
			if locked.Class == fragClassMoved && !p.Done && !repointed[p.Parent.ID] {
				from := target[p.Parent.ID].Frag.File
				to := undo.BookFileLocation{Path: from.Path, Missing: false, Hash: from.Hash, Size: from.Size}
				if err := w.RepointBookFile(parentID, p.Parent.ID, p.Parent.location(), to); err != nil {
					return partial(err)
				}
				repointed[p.Parent.ID] = true
				steps++
			}
			did, err := f.retire(ctx, store, w, p.Frag.Book.ID, parentID, p.Slice)
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
				merge.SliceMapping{OffsetSeconds: m.Offset, Mappable: true})
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

// retire folds fragment id into target (retireInto, the shared retire), as a
// slice of target's timeline.
func (f *fragmentFixer) retire(ctx context.Context, store OpsStore, w *repairs.Writer, id, target string, slice merge.SliceMapping) (int, error) {
	return retireInto(ctx, f.p, store, w, f.now, fragFixerID, id, target, &slice)
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
