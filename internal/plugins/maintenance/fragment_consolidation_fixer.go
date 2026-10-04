// file: internal/plugins/maintenance/fragment_consolidation_fixer.go
// version: 1.27.0
// guid: 5c9e1a47-2b8d-4f63-a0e7-8d3b6f1c4e92
// last-edited: 2026-10-04

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
// wherever a cut-off run left them. Each apply run of a no-parent row
// journals its plan (a plan record, undo.ChangeTypeRepairPlanRecord) on the
// survivor before its first write, and a plan made after a run that did not
// finish CONTINUES it from that record: the same row, survivor, members and
// plan time, or a held row when it can no longer be continued; never a group
// re-formed around another survivor (continueInterrupted). An emptied member
// no continued row holds is listed as a held "stranded" row.
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

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
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
	// fragClassGhost: a fragment whose own file is gone and that exactly one
	// parent row claims by proof; Apply retires it into the parent without
	// touching any row (see the file comment).
	fragClassGhost = "ghost"
	// fragClassCarry: an interrupted no-parent run whose survivor a dedup
	// merge retired into another book, finished except for the planned
	// files the run had moved onto that survivor: Apply moves them onto
	// the book the survivor went into (owner decision 2026-10-04), so
	// nothing of the work stays on a retired book. The survivor keeps its
	// own original files, as a dedup loser does.
	fragClassCarry = "carry"
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
	// fragSkipSameAudioRows: two rows from one folder hold files that are
	// one chapter twice (same position, same size, no conflicting hash), so
	// applying both would make two books of the same audio (renamed copies
	// whose names put them in a different group than their originals).
	// Both rows are held; the owner decides which files form the book.
	fragSkipSameAudioRows = "skipped_same_audio_other_row"
	// fragSkipInterrupted: an apply of this fixer started consolidating the
	// folder and did not finish, and the row cannot continue it with the
	// survivor that run chose (two survivors in progress, the run's plan no
	// longer holds, or a run that left no plan record). Never applied with
	// another survivor: that is how one work became two live books.
	fragSkipInterrupted = "skipped_interrupted_run"
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
	// VersionGroup is version_group_id ("" unset): replanGroup reads it to
	// tell a primary crowned by this row's own retire hand-off.
	VersionGroup string
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
	fb.VersionGroup = dcStr(b.VersionGroupID)
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
	// pin, set only by replanGroup, is the plan's own choices for one
	// no-parent row: which book survives and which books were set aside as
	// copies. A re-plan then keeps the files the plan kept instead of
	// re-electing them from flags the apply itself changes (see fragPin).
	pin     *fragPin
	books   map[string]fragBook
	files   map[string][]fragFile // by book id
	series  map[int]string
	authors map[int]string
	// emptied lists the live books a consolidation emptied (a journaled move
	// took their row onto another book) that no continued row holds:
	// strandedRows lists each one no row of the plan includes.
	emptied []string
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

// realAuthorID is b's author for the numbered-set checks, or nil when the
// author field is junk copied from a path: a placeholder ("Unknown", "read by
// narrator"), or the files' own folder name when that name cannot be a
// person's ("Jennsen, GS_ 08 Rubicon (Amaranthe 08)/" with that very string as
// the author). Owner decision 2026-10-03: such an author counts as missing, so
// it neither makes the folder an "author folder" nor makes two files "by
// different authors". The shape test matters: "Jane Author/" holding files by
// Jane Author is a real author folder, and must stay one.
func (lib *fragLibrary) realAuthorID(b fragBook, dir string) *int {
	if b.AuthorID == nil {
		return nil
	}
	name := lib.authors[*b.AuthorID]
	if authorname.IsPlaceholderAuthor(name) {
		return nil
	}
	if k := junkLettersKey(name); k != "" && k == junkLettersKey(filepath.Base(filepath.Clean(dir))) && !personShapedName(name) {
		return nil
	}
	return b.AuthorID
}

// realSeriesID is b's series for the numbered-set checks, or nil when the
// series field is junk copied from a file name: a junk title ("read by
// narrator"), a numbered stem ("01.Intro", "41.ASO-7"), or the file's own
// stem or title. Owner decision 2026-10-03, as for realAuthorID.
func (lib *fragLibrary) realSeriesID(b fragBook, stem string) *int {
	if b.SeriesID == nil {
		return nil
	}
	name := strings.TrimSpace(lib.series[*b.SeriesID])
	k := junkLettersKey(name)
	switch {
	case k == "",
		metadata.ClassifyJunkTitle(name) != metadata.JunkNone,
		metadata.HasLeadingChapterNumber(name),
		k == junkLettersKey(stem),
		k == junkLettersKey(b.Title):
		return nil
	}
	return b.SeriesID
}

// personShapedName reports whether name could be a person's: no digits and
// none of the characters a path or title carries ("_", brackets, ":", "#"),
// once one trailing bracketed ROLE is set aside ("Jane Author (Narrator)" is
// Jane Author with a role). Only a role word is set aside: "Rubicon
// (Amaranthe 08)", "The Expanse [Book 3]" and "Light of Other Days
// (Unabridged)" keep their brackets and are not person-shaped. A false
// answer does not mean junk on its own; realAuthorID also requires the name
// to be the folder's own.
func personShapedName(name string) bool {
	name = trailingQualifierRe.ReplaceAllString(strings.TrimSpace(name), "")
	return name != "" && !strings.ContainsAny(name, "0123456789_()[]{}:#")
}

// trailingQualifierRe matches one bracketed contributor role at the end of a
// name: " (Narrator)", " [Editor]", " (ed.)", " {Translator}".
var trailingQualifierRe = regexp.MustCompile(`(?i)\s*[(\[{]\s*(narrator|narrated by|reader|read by|editor|ed\.?|eds\.?|translator|trans\.?|translated by|illustrator|illustrated by|foreword|foreword by|introduction|introduction by|intro|afterword|contributor|author|adaptor|adapter|adapted by|compiler|compiled by)\s*[)\]}]$`)

// sameOrMissing reports whether two optional ids do not contradict: equal,
// or either missing.
func sameOrMissing(a, b *int) bool {
	return a == nil || b == nil || *a == *b
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

// Plan reads the whole library once, continues the no-parent runs that did
// not finish (continueInterrupted), evaluates every fragment candidate on
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
	intr, err := f.continueInterrupted(ctx, lib, store, hist, only)
	if err != nil {
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
		} else if len(rows) == 1 && !intr.exclude[id] {
			// A book a continued (or held) interrupted run holds is in that
			// row only: never a candidate of another.
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
	holdInterruptedFolders(rows, intr)
	holdCoOwned(lib, rows)
	rows = append(rows, intr.rows...)
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

// strandedRows lists, as held rows, every emptied book (lib.emptied) that no
// row of the plan includes: an emptied member of a partly applied group no
// plan record continues. It is listed so the owner can see it, never applied.
func strandedRows(lib *fragLibrary, rows []repairs.Row) []repairs.Row {
	in := map[string]bool{}
	for _, r := range rows {
		for _, id := range r.BookIDs {
			in[id] = true
		}
	}
	var out []repairs.Row
	for _, id := range lib.emptied {
		if in[id] {
			continue
		}
		in[id] = true
		b := lib.books[id]
		why := "an interrupted consolidation moved this book's file onto another book and did not finish, and no plan record of that run continues it"
		out = append(out, repairs.Row{RowID: "stranded:" + id, Class: fragClassHeld, BookIDs: []string{id},
			Title: b.Title, Author: lib.authorName(b), Risk: repairs.RiskReview,
			Members: []repairs.RowMember{member(lib, b, "fragment")}, Reason: why,
			Skipped: fragSkipStranded, SkipReason: why + "; revert that apply operation or resolve by hand",
			Fingerprint: fragFingerprint("stranded", id)})
	}
	return out
}

// ---- interrupted runs -----------------------------------------------------

// fragPlanRecord is the decision one apply run of a no-parent row worked
// from, journaled once per run on the survivor
// (undo.ChangeTypeRepairPlanRecord, FieldName fragRecordField) before the
// run's first write: the row as planned. A later plan rebuilds the row from
// it and CONTINUES the run (continueInterrupted) with the survivor, members,
// roles, flags and plan time that run started with, instead of re-electing
// from state the run itself changed.
type fragPlanRecord struct {
	RowID       string            `json:"row_id"`
	Fingerprint string            `json:"fingerprint"`
	Dir         string            `json:"dir"`
	Key         string            `json:"key"`
	Survivor    string            `json:"survivor"`
	BookIDs     []string          `json:"book_ids"`
	Proposed    map[string]string `json:"proposed"`
	PlannedAt   time.Time         `json:"planned_at"`
	State       json.RawMessage   `json:"state"`
}

// fragRecordField is a plan record's FieldName.
func fragRecordField(rowID string) string { return "row:" + rowID }

// decodePlanRecord reads a plan record row; false for any other row or one
// that does not name its own book as the survivor.
func decodePlanRecord(c *database.OperationChange) (fragPlanRecord, bool) {
	var rec fragPlanRecord
	if c == nil || c.ChangeType != undo.ChangeTypeRepairPlanRecord || json.Unmarshal([]byte(c.NewValue), &rec) != nil {
		return fragPlanRecord{}, false
	}
	if rec.RowID == "" || rec.Survivor == "" || rec.Survivor != c.BookID || rec.PlannedAt.IsZero() || len(rec.State) == 0 {
		return fragPlanRecord{}, false
	}
	return rec, true
}

// planRecordOf is the plan record of locked (a row about to be written).
func planRecordOf(locked repairs.Row, plan *fragGroupPlan, st fragGroupState) (string, error) {
	raw, err := json.Marshal(fragPlanRecord{RowID: locked.RowID, Fingerprint: locked.Fingerprint, Dir: plan.Dir, Key: plan.Key,
		Survivor: plan.SurvivorID, BookIDs: locked.BookIDs, Proposed: locked.Proposed, PlannedAt: st.PlannedAt, State: locked.State})
	return string(raw), err
}

// row is the record as the planned row a re-plan judges: the same row id,
// books, proposal, state (plan time and flags included) and fingerprint.
func (rec fragPlanRecord) row() repairs.Row {
	return repairs.Row{RowID: rec.RowID, Class: fragClassNoParent, BookIDs: append([]string(nil), rec.BookIDs...),
		Proposed: rec.Proposed, State: rec.State, Fingerprint: rec.Fingerprint, Risk: repairs.RiskReview}
}

// survivorStepsDone reports whether the folder and title steps of a row have
// nothing left to write on survivor sb: each is done, not planned, or no
// longer applies (the survivor no longer shows the value the plan saw, so
// the compare-and-set would refuse rather than write).
func survivorStepsDone(store OpsStore, sb *database.Book, st fragGroupState, title, folder string) (bool, error) {
	if folder != "" && sb.FilePath == st.SurvivorPath && sb.FilePath != folder {
		return false, nil
	}
	if title != "" && sb.Title == st.SurvivorTitle && sb.Title != title {
		locks, err := database.LoadFieldLocks(store, sb.ID)
		if err != nil {
			return false, fmt.Errorf("field locks of %s unreadable: %w", sb.ID, err)
		}
		if !locks.Locked(database.FieldKeyTitle) {
			return false, nil
		}
	}
	return true, nil
}

// groupRunDone reports whether a no-parent row has nothing left to write:
// every book but the survivor is retired into it, no retired book may still
// be owed its hand-off (its group has live members but not one explicit live
// primary, and its newest demote is newer than every hand-off note on it:
// resumeHandOff's test), and the survivor's folder and title steps are done
// (survivorStepsDone). Apply journals no plan record for such a row, so a
// resume of a finished row writes nothing at all.
func (f *fragmentFixer) groupRunDone(store OpsStore, ids []string, survivor string, st fragGroupState, title, folder string) (bool, error) {
	sb, err := store.GetBookByID(survivor)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", survivor, err)
	}
	if sb == nil || sb.IsSoftDeleted() {
		return false, nil
	}
	for _, id := range ids {
		if id == survivor {
			continue
		}
		b, err := store.GetBookByID(id)
		if err != nil {
			return false, fmt.Errorf("read %s: %w", id, err)
		}
		if b == nil || !b.IsSoftDeleted() || b.MergedIntoBookID == nil || *b.MergedIntoBookID != survivor {
			return false, nil
		}
		g := dcStr(b.VersionGroupID)
		if g == "" {
			continue
		}
		members, err := store.GetBooksByVersionGroup(g)
		if err != nil {
			return false, fmt.Errorf("read version group %s: %w", g, err)
		}
		if len(members) == 0 || handOffSettled(store, members, id) {
			continue
		}
		journal, ok := store.(bookJournalReader)
		if !ok {
			return false, nil
		}
		changes, err := journal.GetBookChanges(id)
		if err != nil {
			return false, fmt.Errorf("changes of %s: %w", id, err)
		}
		demote, handOff := "", ""
		for _, c := range changes {
			switch {
			case c == nil || c.RevertedAt != nil:
			case c.ChangeType == undo.ChangeTypeBookPrimaryDemote && c.ID > demote:
				demote = c.ID
			case c.ChangeType == undo.ChangeTypeBookPrimaryHandoff && c.ID > handOff:
				handOff = c.ID
			}
		}
		if demote > handOff {
			return false, nil
		}
	}
	return survivorStepsDone(store, sb, st, title, folder)
}

// fragInterrupted is what a plan learned from the journal about apply runs
// of this fixer that did not finish (continueInterrupted).
type fragInterrupted struct {
	// rows continue the runs (or hold them): one per unfinished plan record.
	rows []repairs.Row
	// exclude are the books those rows hold: never a candidate of another row.
	exclude map[string]bool
	// dirs maps a continued row's folder (fragGroupPlan.Dir) to why any other
	// no-parent row of that folder is held.
	dirs map[string]string
	// moved maps a folder (fragFolderKey) to the live books holding a row
	// that a run WITHOUT a plan record moved there: older code, or a run
	// whose record is gone. A new row of that folder is held, never applied
	// beside them.
	moved map[string][]string
	// movedOps names, per such book, the operation(s) whose moves put the
	// rows there: reverting them is one way to clear the hold.
	movedOps map[string][]string
}

// fragPlanJournal is the one-pass read continueInterrupted makes of the
// whole journal: the plan records, the row moves still standing, and per
// book the newest retire, demote and hand-off rows.
type fragPlanJournal struct {
	records []*database.OperationChange
	moves   []fragMove
	// mergedInto is each book's newest un-reverted book_merged_into target
	// written by this fixer.
	mergedInto map[string]string
	mergedID   map[string]string
	// demote / handOff are each book's newest un-reverted demote and
	// hand-off note ids (anyone's, as resumeHandOff reads them).
	demote, handOff map[string]string
	// opFixer answers, for a move journaled before rows carried their
	// fixer (Source ""), which fixer's apply op wrote it.
	ops repairs.OpReader
	// owner is the live library's book_file owner by row id.
	owner map[string]string
	// merged is each book's newest un-reverted book_merged_into row from
	// anyone: who folded a record's survivor into another book.
	merged map[string]*database.OperationChange
	// revertible holds each operation with a row a revert of it restores
	// (undo.CountsTowardRevert, reverted or not): RevertOperation refuses
	// an operation with none (undo.OperationRevertible), so a held row
	// offers reverting only operations listed here.
	revertible map[string]bool
}

// fragMove is one un-reverted book_file_reassign still standing: row now on
// target, moved there from book from by fixer source ("" before Source),
// under operation op.
type fragMove struct{ target, from, row, source, op string }

// unrecordedOurs reports whether move m may be a run of this fixer that
// left no plan record: stamped with this fixer, or written before the stamp
// existed by an op that is gone (a discarded run: unknown, so held) or that
// names this fixer. A legacy move whose op names another fixer (a
// folder-books or duplicate-copies fold) is that fixer's business.
func (pj *fragPlanJournal) unrecordedOurs(m fragMove, cache map[string]bool) bool {
	switch m.source {
	case fragFixerID:
		return true
	case "":
	default:
		return false
	}
	if v, ok := cache[m.op]; ok {
		return v
	}
	ours := true
	if pj.ops != nil {
		if row, err := pj.ops.GetOperationV2(m.op); err == nil && row != nil {
			var p repairs.ApplyParams
			ours = row.DefID == repairs.ApplyOpID && json.Unmarshal([]byte(row.Params), &p) == nil && p.FixerID == fragFixerID
		}
	}
	cache[m.op] = ours
	return ours
}

// scanPlanJournal reads the journal once for continueInterrupted.
func (f *fragmentFixer) scanPlanJournal(ctx context.Context, lib *fragLibrary, hist FragmentRepairReader) (*fragPlanJournal, error) {
	sc, oneScan := database.AsCapability[opChangeScanner](hist)
	scan := func(fn func(*database.OperationChange) error) error {
		if oneScan {
			return sc.ScanOperationChanges(fn)
		}
		// No one-pass scan (a backend other than Pebble): every live book's
		// rows, one book at a time. The records and moves are journaled on
		// live books (a survivor, a move's target); the retirees' rows are
		// read afterwards through the records' own books.
		ids := make([]string, 0, len(lib.books))
		for id, b := range lib.books {
			if !b.SoftDeleted {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			cs, err := hist.GetBookChanges(id)
			if err != nil {
				return fmt.Errorf("changes of %s: %w", id, err)
			}
			for _, c := range cs {
				if err := fn(c); err != nil {
					return err
				}
			}
		}
		return nil
	}
	owner := map[string]string{}
	for id, rows := range lib.files {
		for _, r := range rows {
			owner[r.ID] = id
		}
	}
	live := func(id string) bool {
		b, ok := lib.books[id]
		return ok && !b.SoftDeleted
	}
	pj := &fragPlanJournal{mergedInto: map[string]string{}, mergedID: map[string]string{},
		demote: map[string]string{}, handOff: map[string]string{}}
	ours := f.newFragJournal()
	pj.ops = ours.ops
	pj.owner = owner
	pj.merged = map[string]*database.OperationChange{}
	pj.revertible = map[string]bool{}
	n := 0
	take := func(c *database.OperationChange) error {
		if n++; n%fragJournalBeatEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if undo.CountsTowardRevert(c) {
			pj.revertible[c.OperationID] = true
		}
		if c == nil || c.RevertedAt != nil {
			return nil
		}
		switch c.ChangeType {
		case undo.ChangeTypeRepairPlanRecord:
			// Kept whether or not the survivor is still live: a survivor
			// another merge retired leaves the run's other books behind,
			// and they must stay held, not regroup around a new survivor.
			if c.Source == fragFixerID {
				pj.records = append(pj.records, c)
			}
		case undo.ChangeTypeBookFileReassign:
			if row, ok := undo.BookFileIDFromField(c.FieldName); ok && live(c.BookID) && owner[row] == c.BookID {
				pj.moves = append(pj.moves, fragMove{target: c.BookID, from: c.OldValue, row: row, source: c.Source, op: c.OperationID})
			}
		case undo.ChangeTypeBookMergedInto:
			if prev := pj.merged[c.BookID]; prev == nil || c.ID > prev.ID {
				pj.merged[c.BookID] = c
			}
			if c.ID > pj.mergedID[c.BookID] && ours.ourChange(c) {
				pj.mergedID[c.BookID], pj.mergedInto[c.BookID] = c.ID, c.NewValue
			}
		case undo.ChangeTypeBookPrimaryDemote:
			// Anyone's demote, as resumeHandOff and groupRunDone read it.
			if c.ID > pj.demote[c.BookID] {
				pj.demote[c.BookID] = c.ID
			}
		case undo.ChangeTypeBookPrimaryHandoff:
			if c.ID > pj.handOff[c.BookID] {
				pj.handOff[c.BookID] = c.ID
			}
		}
		return nil
	}
	if err := scan(take); err != nil {
		return nil, fmt.Errorf("%s: read the operation journal: %w", fragFixerID, err)
	}
	if !oneScan {
		seen := map[string]bool{}
		for _, c := range append([]*database.OperationChange(nil), pj.records...) {
			rec, ok := decodePlanRecord(c)
			if !ok {
				continue
			}
			for _, id := range rec.BookIDs {
				if seen[id] || live(id) {
					continue
				}
				seen[id] = true
				cs, err := hist.GetBookChanges(id)
				if err != nil {
					return nil, fmt.Errorf("%s: changes of %s: %w", fragFixerID, id, err)
				}
				for _, c := range cs {
					if err := take(c); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return pj, nil
}

// fragCarry is one planned member file a run left on a retired book of
// the run that is not that file's own member, where the book's work went
// on to terminal: the files this run moved onto its survivor before a dedup
// merge retired the survivor into another book. The carry row moves each
// onto to (journaled, so a revert puts it back).
type fragCarry struct {
	File string `json:"file"`
	From string `json:"from"`
	To   string `json:"to"`
}

// recordDone reports whether the run of record rec has nothing left to do,
// whoever finished it; terminal, the live book its work ended in ("" when
// there is none); outside, set when a planned member file now sits on a
// live book outside the run (the run is then not done, and the hold names
// that book); and carry, set when the only thing left is moving planned
// files off retired books of the run onto terminal (the carry row). Done
// means:
//   - every book of the record resolves (survivorOf: merged_into, or the
//     sync redirect a dedup merge records, in its loser shape) to one live
//     terminal book: the survivor, or the book the survivor went into;
//   - every member's planned file row (copies keep theirs by design) is on
//     the terminal, or on its own member when that member is retired and
//     resolves to the terminal (a dedup merge keeps a loser's own files on
//     the loser, as that version's own). A planned file on any OTHER
//     retired book is not done: it is out of every view while the run looks
//     finished (review 9: a survivor deleted outright hid 5 such files), and
//     when that book resolves to the terminal the file is carried;
//   - no book demoted after its newest hand-off note is in a group still
//     without its one explicit live primary (resumeHandOff's rule);
//   - when this fixer itself retired every book into the survivor, the
//     survivor's folder and title steps are done (a run finished by anyone
//     else leaves those to whoever finished it).
//
// Whoever merged the books counts: an owner who finished an interrupted run
// by hand clears its hold (review 7). Nothing here needs a journal row of
// the run besides its plan record: merges are read off the books, and the
// files off the planned state, so a run whose other rows the 90-day prune
// removed is judged the same (review 8). The common case, this fixer's own
// finished run, is decided from the snapshot and the journal summaries
// without a point read per book.
func (f *fragmentFixer) recordDone(store OpsStore, lib *fragLibrary, pj *fragPlanJournal, rec fragPlanRecord, st fragGroupState) (done bool, terminal, outside string, carry []fragCarry, err error) {
	live := func(id string) bool {
		b, ok := lib.books[id]
		return ok && !b.SoftDeleted
	}
	resolved := map[string]string{}
	resolve := func(id string) (string, error) {
		if live(id) {
			return id, nil
		}
		if t, ok := resolved[id]; ok {
			return t, nil
		}
		t, err := survivorOf(store, id)
		if err != nil {
			return "", err
		}
		resolved[id] = t
		return t, nil
	}
	if terminal, err = resolve(rec.Survivor); err != nil || terminal == "" {
		return false, terminal, "", nil, err
	}
	ids := make([]string, 0, len(st.Files))
	for id := range st.Files {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if st.Roles[id].Copy {
			continue
		}
		fid := st.Files[id]
		o := pj.owner[fid]
		if o == "" {
			return false, terminal, "", nil, nil
		}
		if o == terminal {
			continue
		}
		if live(o) {
			if !slices.Contains(rec.BookIDs, o) {
				outside = fmt.Sprintf("planned file %s of book %s is now on book %s, outside the run", fid, id, o)
			}
			return false, terminal, outside, nil, nil
		}
		t, err := resolve(o)
		if err != nil {
			return false, terminal, "", nil, err
		}
		if t != terminal {
			return false, terminal, "", nil, nil
		}
		if o == id {
			continue
		}
		if !slices.Contains(rec.BookIDs, o) && o != rec.Survivor {
			// A retired book outside the run: not this run's to carry from.
			return false, terminal, "", nil, nil
		}
		carry = append(carry, fragCarry{File: fid, From: o, To: terminal})
	}
	byOthers := false
	for _, id := range rec.BookIDs {
		if id == rec.Survivor {
			continue
		}
		if live(id) {
			if id != terminal {
				return false, terminal, "", nil, nil
			}
			continue
		}
		if terminal != rec.Survivor || pj.mergedInto[id] != rec.Survivor {
			byOthers = true
			t, err := resolve(id)
			if err != nil {
				return false, terminal, "", nil, err
			}
			if t != terminal {
				return false, terminal, "", nil, nil
			}
		}
		if pj.demote[id] > pj.handOff[id] {
			b, err := store.GetBookByID(id)
			if err != nil {
				return false, terminal, "", nil, fmt.Errorf("read %s: %w", id, err)
			}
			if g := bookGroup(b); g != "" {
				members, err := store.GetBooksByVersionGroup(g)
				if err != nil {
					return false, terminal, "", nil, fmt.Errorf("read version group %s: %w", g, err)
				}
				if len(members) > 0 && !handOffSettled(store, members, id) {
					return false, terminal, "", nil, nil
				}
			}
		}
	}
	if len(carry) > 0 {
		return false, terminal, "", carry, nil
	}
	if terminal != rec.Survivor || byOthers {
		// Finished by someone else (an owner merging the rest by hand, or
		// another merge): the folder and title steps were this run's, and
		// whoever finished it decides them.
		return true, terminal, "", nil, nil
	}
	sb, err := store.GetBookByID(rec.Survivor)
	if err != nil {
		return false, terminal, "", nil, fmt.Errorf("read %s: %w", rec.Survivor, err)
	}
	if sb == nil {
		return false, terminal, "", nil, nil
	}
	done, err = survivorStepsDone(store, sb, st, rec.Proposed["title"], rec.Proposed["book_path"])
	return done, terminal, "", nil, err
}

// survivorOf is the live book retired book id's work went into, or "" when
// nothing shows where it went (a book deleted outright: the held row then
// offers a restore). The evidence is what a merge leaves on the record:
//   - merged_into_book_id (every retireInto, a combine): followed as
//     merge.ResolveSurvivor follows it. Undo restores the column, so it is
//     never stale;
//   - otherwise the sync redirect merge.Service.MergeBooks records from a
//     loser to its winner (since 2026-10-04 for every loser; before, only
//     for one a client had seen), trusted only in MergeBooks's loser shape:
//     id is explicitly non-primary in the version group of the book the
//     redirect leads to, or of a book there that merged_into leads on from.
//     A dedup undo (UnmergeAuto) puts the loser's pre-merge row back and
//     does not clear the redirect, so a redirect alone may be stale.
//
// A version group's live incumbent alone is no evidence (review 9): the
// round-9 fallback read a survivor deleted outright as merged into the
// group's primary, offered finishing into it, and that cleared the hold
// with 5 planned files left on the deleted survivor.
func survivorOf(store OpsStore, id string) (string, error) {
	t, err := merge.ResolveSurvivor(store, id)
	switch {
	case errors.Is(err, merge.ErrNoLiveSurvivor):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("resolve %s: %w", id, err)
	}
	b, err := store.GetBookByID(id)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", id, err)
	}
	if b == nil || !b.IsSoftDeleted() || t == id {
		return t, nil
	}
	if dcStr(b.MergedIntoBookID) != "" {
		return t, nil
	}
	g := bookGroup(b)
	if g == "" || b.IsPrimaryVersion == nil || *b.IsPrimaryVersion {
		return "", nil
	}
	members, err := store.GetBooksByVersionGroup(g)
	if err != nil {
		return "", fmt.Errorf("read version group %s: %w", g, err)
	}
	for _, m := range members {
		if m.ID != id && (m.ID == t || dcStr(m.MergedIntoBookID) == t) {
			return t, nil
		}
	}
	return "", nil
}

// bookGroup is b's version group ("" for none or a nil book).
func bookGroup(b *database.Book) string {
	if b == nil {
		return ""
	}
	return dcStr(b.VersionGroupID)
}

// continueInterrupted finds, in ONE pass of the journal, every apply run of
// this fixer that started consolidating a no-parent group and did not
// finish, and makes the row that finishes it. A started group is CONTINUED,
// never re-formed: re-forming it from the live library put the survivor,
// which by then holds several rows, in the parent index, and grouped the
// remaining fragments without it under a new survivor. Applying that row
// left two live books for one work (2026-10-03, probe F: 36 of 122 cut
// points with no version group, 10 of 122 in the prod shape).
//
// Evidence of "started" is the run's plan record (fragPlanRecord), stamped
// with this fixer's Source and journaled before the run's first write, on a
// live survivor. The journal is the only source: a cut-off run's op row is
// discarded (registry.Discard), and the books' live state cannot say which
// file each chapter kept or which member a hand-off crowned.
//
// SET IDENTITY is the record's own: its row id (noParentRowID of the
// folder, groupDir, and the chapter key) and its books. A folder (the
// record's Dir: the import folder with sibling disc folders as one, the
// grouping key noParentRows uses) is the unit for ambiguity and holds,
// because the leftovers of a numbered set can regroup under key-group row
// ids of the same folder.
//
// For each record whose run is not done (recordDone), newest per survivor,
// row and plan time:
//   - exactly one survivor in progress for the folder: the record is
//     re-planned as its own row (replanGroup with the journal read here) and
//     emitted when it is unchanged and applicable. That row IS the run's
//     row: the same row id, books, survivor, roles, flags and plan time, so
//     applying it finishes the original consolidation, and the original
//     plan's resume afterwards finds nothing left to do.
//   - two survivors in progress for the folder, two plans in progress on
//     one survivor, or a record whose row no longer holds (a changed flag,
//     the survivor retired or ineligible, a missing file): a HELD row naming
//     the books and why. Never an applicable row with another survivor.
//
// The record's books and survivor are kept out of every other row, and any
// other no-parent row of the folder is held (holdInterruptedFolders). A run
// that left moves and no record at all (older code, or a record that is
// gone) cannot be continued: its folder's new rows are held too (moved).
// Emptied members no continued row holds are listed as stranded.
func (f *fragmentFixer) continueInterrupted(ctx context.Context, lib *fragLibrary, store OpsStore, hist FragmentRepairReader, only map[string]bool) (*fragInterrupted, error) {
	out := &fragInterrupted{exclude: map[string]bool{}, dirs: map[string]string{}, moved: map[string][]string{}, movedOps: map[string][]string{}}
	pj, err := f.scanPlanJournal(ctx, lib, hist)
	if err != nil {
		return nil, err
	}
	type recKey struct {
		survivor, row string
		at            int64
	}
	newest, newestID := map[recKey]fragPlanRecord{}, map[recKey]string{}
	opsOf := map[recKey][]string{}
	recorded := map[string]bool{}
	for _, c := range pj.records {
		rec, ok := decodePlanRecord(c)
		if !ok {
			// Unreadable: its survivor is not marked recorded, so the
			// folder of that run's moves is held below like any run that
			// left no record.
			continue
		}
		recorded[rec.Survivor] = true
		k := recKey{rec.Survivor, rec.RowID, rec.PlannedAt.UnixNano()}
		if !slices.Contains(opsOf[k], c.OperationID) {
			opsOf[k] = append(opsOf[k], c.OperationID)
		}
		if c.ID > newestID[k] {
			newest[k], newestID[k] = rec, c.ID
		}
	}
	keys := make([]recKey, 0, len(newest))
	for k := range newest {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return newestID[keys[i]] < newestID[keys[j]] })
	// hold keeps a run's books out of every other row and holds the other
	// rows of its folder. It applies to every unfinished run, in or out of
	// a book_ids-scoped plan's scope: a scoped plan must not form a row over
	// the folder either.
	hold := func(rec fragPlanRecord, why string) {
		out.exclude[rec.Survivor] = true
		for _, id := range rec.BookIDs {
			out.exclude[id] = true
		}
		out.dirs[rec.Dir] = why
	}
	// Each unfinished run is classified first and its row made after, so
	// the runs of one folder can be given one action set (review 9: two
	// records of one row id offered "finish into X" on one row and "finish
	// into S2" on the other, and each undid the other's).
	const (
		runUnreadable = iota
		runOutside
		runGone
		runCarry
		runPending
	)
	type unfinished struct {
		rec      fragPlanRecord
		ops      []string
		inScope  bool
		kind     int
		why      string
		terminal string
		carry    []fragCarry
	}
	var runs []*unfinished
	for _, k := range keys {
		rec, ops := newest[k], opsOf[k]
		u := &unfinished{rec: rec, ops: ops,
			inScope: only == nil || only[rec.Survivor] || slices.ContainsFunc(rec.BookIDs, func(id string) bool { return only[id] })}
		var st fragGroupState
		if err := json.Unmarshal(rec.State, &st); err != nil {
			// No decision to continue, and the run may be unfinished: held,
			// with its books kept out of every other row.
			u.kind, u.why = runUnreadable, "the run's plan record is unreadable: "+err.Error()
			hold(rec, fmt.Sprintf("an interrupted consolidation of this folder into %s has an unreadable plan record (row %s, operation(s) %s)",
				rec.Survivor, rec.RowID, strings.Join(ops, ", ")))
			runs = append(runs, u)
			continue
		}
		done, terminal, outside, carry, err := f.recordDone(store, lib, pj, rec, st)
		if err != nil {
			return nil, err
		}
		if done {
			continue
		}
		u.terminal, u.carry = terminal, carry
		switch b, ok := lib.books[rec.Survivor]; {
		case outside != "":
			// A planned file left the run for a live book outside it: the
			// run can be neither continued (the row is not the plan's any
			// more) nor called done (one work, two live books).
			u.kind, u.why = runOutside, outside
			hold(rec, fmt.Sprintf("an interrupted consolidation of this folder into %s is held: %s (row %s, operation(s) %s)",
				terminal, outside, rec.RowID, strings.Join(ops, ", ")))
		case len(carry) > 0:
			// Everything is done but planned files left on a retired book
			// of the run (the survivor a dedup merge retired): the carry
			// row moves them onto the terminal.
			u.kind = runCarry
			hold(rec, fmt.Sprintf("an interrupted consolidation of this folder into %s is finished by carry row %s (operation(s) %s)",
				terminal, fragClassCarry+":"+rec.RowID, strings.Join(ops, ", ")))
		case !ok || b.SoftDeleted:
			// The survivor itself was retired (another fixer, a dedup or a
			// user merge) or deleted with the run unfinished: nothing can
			// continue it (its re-plan refuses a retired survivor), and the
			// remaining fragments must not regroup around another
			// survivor. Held until someone finishes it into the book the
			// survivor became (or restores a deleted one).
			u.kind, u.why = runGone, survivorGoneWhy(pj, rec.Survivor, terminal)
			hold(rec, fmt.Sprintf("an interrupted consolidation of this folder into %s is held: %s (row %s, operation(s) %s)",
				rec.Survivor, u.why, rec.RowID, strings.Join(ops, ", ")))
		default:
			u.kind = runPending
			hold(rec, fmt.Sprintf("an interrupted consolidation of this folder into %s is continued (or held) by row %s (operation(s) %s)",
				rec.Survivor, rec.RowID, strings.Join(ops, ", ")))
		}
		runs = append(runs, u)
	}
	// target is the one book that must end up holding u's work, "" when
	// none can be named (unreadable record, survivor deleted outright).
	target := func(u *unfinished) string {
		switch u.kind {
		case runPending:
			return u.rec.Survivor
		case runUnreadable:
			return ""
		}
		return u.terminal
	}
	byDir := map[string][]*unfinished{}
	for _, u := range runs {
		byDir[u.rec.Dir] = append(byDir[u.rec.Dir], u)
	}
	// conflict is the one action set of a folder whose unfinished runs name
	// different books: the reason, and the action that clears every one of
	// them. "" when the folder has one target (or one run).
	conflict := func(dir string) (why, act string) {
		us := byDir[dir]
		if len(us) < 2 {
			return "", ""
		}
		targets, fixed := []string{}, []string{}
		var parts []string
		for _, u := range us {
			t := target(u)
			if t == "" {
				continue
			}
			parts = append(parts, fmt.Sprintf("row %s into %s", u.rec.RowID, t))
			if !slices.Contains(targets, t) {
				targets = append(targets, t)
			}
			if u.kind != runPending && t != u.rec.Survivor && !slices.Contains(fixed, t) {
				fixed = append(fixed, t)
			}
		}
		if len(targets) < 2 {
			return "", ""
		}
		sort.Strings(targets)
		sort.Strings(fixed)
		sort.Strings(parts)
		why = fmt.Sprintf("interrupted consolidations of %s name different books to keep the work (%s); every one of them is held until one book holds it",
			dir, strings.Join(parts, ", "))
		switch len(fixed) {
		case 0:
			// Every survivor is live and in the folder: any one can be
			// chosen, and merging the folder into it finishes every run.
			act = fmt.Sprintf("pick one of %s and merge every book of the folder into it by hand (that finishes every one of these runs)",
				strings.Join(targets, ", "))
		case 1:
			// One run's survivor already went into a book outside the
			// folder: only that book can finish every run.
			act = actFinish(fixed[0])
		default:
			act = actFinishAll(fixed[0], fixed[1:])
		}
		return why, act
	}
	bySurvivor := map[string]int{}
	for _, u := range runs {
		if u.kind == runPending {
			bySurvivor[u.rec.Survivor]++
		}
	}
	rowsOf := make([]repairs.Row, len(runs))
	var toReplan []int
	for i, u := range runs {
		if !u.inScope {
			continue
		}
		rec := u.rec
		if why, act := conflict(rec.Dir); why != "" {
			rowsOf[i] = interruptedRow(lib, rec, u.ops, why, act)
			continue
		}
		switch u.kind {
		case runUnreadable:
			rowsOf[i] = interruptedRow(lib, rec, u.ops, u.why, orActions(revertAct(pj, u.ops)))
		case runOutside:
			rowsOf[i] = interruptedRow(lib, rec, u.ops, u.why, actFinishOutside(u.terminal))
		case runCarry:
			row, err := carryRow(lib, rec, u.ops, u.terminal, u.carry)
			if err != nil {
				return nil, err
			}
			rowsOf[i] = row
		case runGone:
			why, act := u.why, actRestore(rec.Survivor)
			if u.terminal != "" {
				act = actFinish(u.terminal)
				why += fmt.Sprintf("; once they are, any file of the run left on %s is moved onto %s by a row this fixer offers", rec.Survivor, u.terminal)
			}
			rowsOf[i] = interruptedRow(lib, rec, u.ops, why, act)
		case runPending:
			if bySurvivor[rec.Survivor] > 1 {
				rowsOf[i] = interruptedRow(lib, rec, u.ops,
					fmt.Sprintf("book %s is the survivor of %d interrupted plans", rec.Survivor, bySurvivor[rec.Survivor]),
					actFinish(rec.Survivor))
				continue
			}
			toReplan = append(toReplan, i)
		}
	}
	if len(toReplan) > 0 {
		ids := map[string]bool{}
		for _, i := range toReplan {
			for _, id := range runs[i].rec.BookIDs {
				ids[id] = true
			}
			ids[runs[i].rec.Survivor] = true
		}
		// The rows of these books only (the by-book index, or a second
		// pass): the first pass kept per-book summaries, not rows.
		jr, err := f.loadJournal(ctx, hist, ids, nil)
		if err != nil {
			return nil, err
		}
		for _, i := range toReplan {
			u := runs[i]
			rec := u.rec
			got, err := f.replanWith(ctx, rec.row(), nil, jr)
			if err != nil {
				return nil, err
			}
			why := ""
			switch {
			case got.Fingerprint != rec.Fingerprint:
				why = got.Reason
			case !got.Applicable():
				why = got.SkipReason
			default:
				row := got
				row.Evidence = append(row.Evidence, fmt.Sprintf(
					"continues an interrupted apply (operation(s) %s) of the plan made %s: the same survivor %s, members, chapter order and copies that run started with",
					strings.Join(u.ops, ", "), rec.PlannedAt.UTC().Format(time.RFC3339), rec.Survivor))
				rowsOf[i] = row
				continue
			}
			rowsOf[i] = interruptedRow(lib, rec, u.ops, why, orActions(revertAct(pj, u.ops), actFinish(rec.Survivor)))
		}
	}
	var emitted []fragPlanRecord
	for i, u := range runs {
		if !u.inScope {
			continue
		}
		out.rows = append(out.rows, rowsOf[i])
		emitted = append(emitted, u.rec)
	}
	// Several records can name one row id (two survivors of a folder, two
	// plans on one survivor): each colliding row gets its own id, so the
	// plan never lists one id twice (the engine refuses such a plan whole).
	seen := map[string]int{}
	for _, r := range out.rows {
		seen[r.RowID]++
	}
	for i := range out.rows {
		if seen[out.rows[i].RowID] > 1 {
			rec := emitted[i]
			out.rows[i].RowID = fmt.Sprintf("%s@%s@%d", rec.RowID, rec.Survivor, rec.PlannedAt.UnixNano())
		}
	}
	emptied := map[string]bool{}
	legacyOps := map[string]bool{}
	for _, m := range pj.moves {
		if b, ok := lib.books[m.from]; ok && !b.SoftDeleted && b.FilePath != "" && len(lib.files[m.from]) == 0 &&
			!out.exclude[m.from] && (only == nil || only[m.from]) {
			emptied[m.from] = true
		}
		if recorded[m.target] || out.exclude[m.target] || !pj.unrecordedOurs(m, legacyOps) {
			continue
		}
		for _, r := range lib.files[m.target] {
			if r.ID == m.row {
				k := fragFolderKey(r.Path)
				if !slices.Contains(out.moved[k], m.target) {
					out.moved[k] = append(out.moved[k], m.target)
				}
				if !slices.Contains(out.movedOps[m.target], m.op) {
					out.movedOps[m.target] = append(out.movedOps[m.target], m.op)
				}
			}
		}
	}
	for id := range emptied {
		lib.emptied = append(lib.emptied, id)
	}
	sort.Strings(lib.emptied)
	return out, nil
}

// The actions a held interrupted row offers. Each is one the tests carry out
// (fragment_review8_test.go) and that clears the hold; a held row offers
// only the ones that work for its reason.

// actRevert: a full revert of the run's apply operation(s) marks the plan
// record reverted with them (only a revert that restores every row does).
func actRevert(ops []string) string {
	return fmt.Sprintf("revert apply operation(s) %s", strings.Join(ops, ", "))
}

// actFinish: every book of the run resolving to target, with every planned
// file on it, is a finished run (recordDone).
func actFinish(target string) string {
	return fmt.Sprintf("merge every remaining book of the folder into %s by hand", target)
}

// actFinishOutside: the planned file must come back from the outside book
// first, then the books are merged as actFinish.
func actFinishOutside(target string) string {
	return fmt.Sprintf("move the planned file onto %s, then merge every remaining book of the folder into %s by hand", target, target)
}

// actRestore: a survivor deleted outright (no book it was merged into) is
// restored, and the run can be continued again.
func actRestore(survivor string) string {
	return fmt.Sprintf("restore book %s (undo its deletion), then plan again", survivor)
}

// actFinishAll: runs of one folder whose survivors went into different
// books outside it are finished by merging those books into one, then the
// folder's books into it.
func actFinishAll(target string, others []string) string {
	return fmt.Sprintf("merge %s into %s by hand, then merge every remaining book of the folder into %s by hand",
		strings.Join(others, " and "), target, target)
}

// orActions joins the actions a held row offers, leaving out the ones that
// cannot work ("").
func orActions(acts ...string) string {
	var keep []string
	for _, a := range acts {
		if a != "" {
			keep = append(keep, a)
		}
	}
	if len(keep) == 0 {
		return "nothing this fixer can offer: ask the owner"
	}
	return strings.Join(keep, ", or ")
}

// revertAct is actRevert(ops) when a revert of every one of ops can
// succeed (pj.revertible: the rule RevertOperation refuses by), else "". A
// revert of some of them would leave the others' plan records standing, and
// the hold with them.
func revertAct(pj *fragPlanJournal, ops []string) string {
	for _, op := range ops {
		if !pj.revertible[op] {
			return ""
		}
	}
	return actRevert(ops)
}

// carryRow is the applicable row that finishes run rec by moving carry (its
// planned files on retired books of the run) onto terminal.
func carryRow(lib *fragLibrary, rec fragPlanRecord, ops []string, terminal string, carry []fragCarry) (repairs.Row, error) {
	ids := append([]string(nil), rec.BookIDs...)
	for _, id := range []string{rec.Survivor, terminal} {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	sort.Slice(carry, func(i, j int) bool { return carry[i].File < carry[j].File })
	state, err := json.Marshal(carry)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("carry row %s: %w", rec.RowID, err)
	}
	tb := lib.books[terminal]
	from := map[string]bool{}
	fp := []string{"carry", rec.RowID, terminal}
	for _, c := range carry {
		from[c.From] = true
		fp = append(fp, c.File, c.From)
	}
	var froms []string
	for id := range from {
		froms = append(froms, id)
	}
	sort.Strings(froms)
	r := repairs.Row{RowID: fragClassCarry + ":" + rec.RowID, Class: fragClassCarry, BookIDs: ids,
		Title: tb.Title, Author: lib.authorName(tb), Risk: repairs.RiskReview,
		Proposed: map[string]string{"survivor": terminal, "from": strings.Join(froms, ",")},
		Reason: fmt.Sprintf("finishes an interrupted consolidation of %s: its survivor %s went into %s, and the %d planned file(s) the run left on %s move onto %s (%s keeps its own files, as a dedup merge leaves them)",
			rec.Dir, rec.Survivor, terminal, len(carry), strings.Join(froms, ", "), terminal, rec.Survivor),
		Evidence: []string{fmt.Sprintf("plan record of %s, planned %s, written by apply operation(s) %s; every other book and file of that run already resolves to %s",
			rec.RowID, rec.PlannedAt.UTC().Format(time.RFC3339), strings.Join(ops, ", "), terminal)},
		State:       state,
		Detail:      carry,
		Fingerprint: fragFingerprint(fp...)}
	for _, id := range ids {
		b, ok := lib.books[id]
		if !ok {
			b = fragBook{ID: id}
		}
		role := "fragment"
		switch id {
		case terminal:
			role = "survivor"
		case rec.Survivor:
			role = "retired survivor"
		}
		r.Members = append(r.Members, member(lib, b, role))
	}
	return r, nil
}

// replanCarry re-checks carry row planned: every file is still on the
// retired book it was planned on, that book still resolves (survivorOf) to
// the planned book, and that book is live. Anything else is a change.
func (f *fragmentFixer) replanCarry(store OpsStore, lib *fragLibrary, planned repairs.Row) (repairs.Row, error) {
	var carry []fragCarry
	if err := json.Unmarshal(planned.State, &carry); err != nil || len(carry) == 0 {
		return changedRow(planned, "the carry row's state is unreadable"), nil
	}
	for _, c := range carry {
		if tb, ok := lib.books[c.To]; !ok || tb.SoftDeleted {
			return changedRow(planned, fmt.Sprintf("book %s, which the files were to move onto, is not live", c.To)), nil
		}
		fb, ok := lib.books[c.From]
		if !ok || !fb.SoftDeleted {
			return changedRow(planned, fmt.Sprintf("book %s, which holds the files, is not retired any more", c.From)), nil
		}
		t, err := survivorOf(store, c.From)
		if err != nil {
			return repairs.Row{}, err
		}
		if t != c.To {
			return changedRow(planned, fmt.Sprintf("book %s now resolves to %q, not %s", c.From, t, c.To)), nil
		}
		if !slices.ContainsFunc(lib.files[c.From], func(r fragFile) bool { return r.ID == c.File }) {
			return changedRow(planned, fmt.Sprintf("file %s is no longer on book %s", c.File, c.From)), nil
		}
	}
	got := planned
	got.Detail = carry
	return got, nil
}

// survivorGoneWhy says how a record's survivor left the live library: the
// newest merge of it (who, by which operation, into which book), or that it
// was deleted.
func survivorGoneWhy(pj *fragPlanJournal, survivor, terminal string) string {
	if c := pj.merged[survivor]; c != nil {
		by := c.Source
		if by == "" {
			by = "an operation"
		}
		why := fmt.Sprintf("survivor %s was merged into %s by %s (operation %s)", survivor, c.NewValue, by, c.OperationID)
		if terminal != "" && terminal != c.NewValue {
			why += fmt.Sprintf(", which now lives on as %s", terminal)
		}
		return why
	}
	if terminal != "" {
		return fmt.Sprintf("survivor %s was merged into %s", survivor, terminal)
	}
	return fmt.Sprintf("survivor %s was deleted", survivor)
}

// interruptedRow is the held row for a run continueInterrupted cannot
// continue with the survivor it chose.
func interruptedRow(lib *fragLibrary, rec fragPlanRecord, ops []string, why, action string) repairs.Row {
	ids := append([]string(nil), rec.BookIDs...)
	sort.Strings(ids)
	sb := lib.books[rec.Survivor]
	r := repairs.Row{RowID: rec.RowID, Class: fragClassHeld, BookIDs: ids, Title: sb.Title, Author: lib.authorName(sb),
		Risk: repairs.RiskReview, Proposed: map[string]string{"survivor": rec.Survivor},
		Reason:     fmt.Sprintf("an apply of this fixer started consolidating %s into %s and did not finish", rec.Dir, rec.Survivor),
		Skipped:    fragSkipInterrupted,
		SkipReason: why + "; never applied with another survivor. To clear it: " + action,
		Evidence: []string{fmt.Sprintf("plan record of %s, planned %s, written by apply operation(s) %s",
			rec.RowID, rec.PlannedAt.UTC().Format(time.RFC3339), strings.Join(ops, ", "))},
		Fingerprint: fragFingerprint("interrupted", rec.RowID, rec.Survivor, why)}
	for _, id := range ids {
		b, ok := lib.books[id]
		if !ok {
			b = fragBook{ID: id}
		}
		role := "fragment"
		if id == rec.Survivor {
			role = "survivor"
		}
		r.Members = append(r.Members, member(lib, b, role))
	}
	return r
}

// holdInterruptedFolders holds every no-parent row of a folder an
// interrupted run is consolidating: one a plan record continues (in.dirs),
// or one where a book outside the row holds a file that a run without a
// record moved from that folder (in.moved). Applying it would make a second
// live book of the work.
func holdInterruptedFolders(rows []repairs.Row, in *fragInterrupted) {
	for i := range rows {
		r := &rows[i]
		plan, ok := r.Detail.(*fragGroupPlan)
		if !ok {
			continue
		}
		why, act := in.dirs[plan.Dir], ""
		if why == "" {
			inRow := map[string]bool{}
			for _, id := range r.BookIDs {
				inRow[id] = true
			}
			folders := []string{plan.Dir}
			for _, m := range plan.Members {
				folders = append(folders, fragFolderKey(m.Frag.File.Path))
			}
			for _, cp := range plan.Copies {
				folders = append(folders, fragFolderKey(cp.Frag.File.Path))
			}
			var holders []string
			for _, d := range folders {
				for _, t := range in.moved[d] {
					if !inRow[t] {
						holders = append(holders, t)
					}
				}
			}
			if len(holders) > 0 {
				holders = uniqueSorted(holders)
				var ops []string
				for _, h := range holders {
					ops = append(ops, in.movedOps[h]...)
				}
				why = fmt.Sprintf("book(s) %s outside this row hold files a consolidation moved from this folder, and that run left no plan record to continue it by (older code, or a record that is gone)",
					strings.Join(holders, ", "))
				act = fmt.Sprintf("%s, or merge this row's books into %s by hand", actRevert(uniqueSorted(ops)), holders[0])
			}
		}
		if why == "" {
			continue
		}
		r.Evidence = append(r.Evidence, why)
		if r.Skipped == "" {
			r.Risk, r.Skipped = repairs.RiskReview, fragSkipInterrupted
			r.SkipReason = why + ": applying this row would make a second live book of one work"
			if act != "" {
				r.SkipReason += ". To clear it: " + act
			} else {
				r.SkipReason += "; the run's own row says how to clear it"
			}
		}
	}
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
	// Copies are renamed second copies of a member's chapter: same chapter
	// position, same size ("02_001" and "02_001 - Title - read by narrator").
	// Owner decision 2026-10-03: one chapter, so the copy is not a member; its
	// book is retired into the survivor like the copy class retires a
	// fragment, keeping its own file row (nothing is repointed or deleted).
	Copies []fragGroupCopy
}

// fragGroupCopy is a renamed copy of member Of's chapter.
type fragGroupCopy struct {
	Frag *fragCandidate
	Of   string
	// Offset is the kept member's place in the survivor's timeline, so the
	// copy's listening position maps onto the same chapter.
	Offset float64
}

// keptOfCopies picks which of one chapter's copies stays a member, in this
// order:
//
//  1. the first, in run order, that is organized and primary (the survivor
//     must be such a book when one exists);
//  2. else the one whose chapter key ranks best in keyN (chapterKeyBetter:
//     the key the most files of the folder carry), so every chapter keeps a
//     file of the SAME key and the set reads as one work whatever order the
//     books were created in ("02_001" over "02_001 - Title - read by
//     narrator" when the originals are the majority, even where a copy holds
//     the lower book id);
//  3. else, among files of that key, the lowest book id.
//
// keyN counts chapter keys over every numbered file of the folder, copies
// included. The numbered-set builder and noParentRow must agree, so both call
// this with the counts over the same files.
func keptOfCopies(lib *fragLibrary, run []*fragCandidate, keyN map[string]int) int {
	if lib.pin != nil {
		// A re-plan keeps the file the plan kept: the one book of the run
		// the plan did not set aside. Any other shape (no such book, or
		// two) is not the planned run; the first is returned and the row's
		// role check in replanGroup reports the change.
		for k, c := range run {
			if !lib.pin.copies[c.Book.ID] {
				return k
			}
		}
		return 0
	}
	keys := make([]string, len(run))
	for k, c := range run {
		if b := lib.books[c.Book.ID]; b.Organized && b.Primary {
			return k
		}
		keys[k], _ = metadata.ChapterGroupKey(c.origStem())
	}
	keep := 0
	for k := range run {
		switch {
		case keys[k] == keys[keep]:
			if run[k].Book.ID < run[keep].Book.ID {
				keep = k
			}
		case chapterKeyBetter(keys[k], keys[keep], keyN):
			keep = k
		}
	}
	return keep
}

// chapterKeyBetter reports whether chapter key a ranks before b: carried by
// more files (keyN), then shorter (the bare original over a renamed copy's
// longer name when they tie), then lexically smaller. A total order, so every
// chapter of a folder prefers the same key.
func chapterKeyBetter(a, b string, keyN map[string]int) bool {
	if keyN[a] != keyN[b] {
		return keyN[a] > keyN[b]
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// sameChapterCopy reports whether a and b are one chapter twice: both sizes
// known and equal, and the content hashes equal when both are known. Two
// files at one position with different sizes, or with different hashes (two
// encodes of one length), are not provably the same audio and stay a
// conflict. A missing hash falls back to size and position.
func sameChapterCopy(a, b *fragCandidate) bool {
	return sameChapterFiles(a.File, b.File)
}

func sameChapterFiles(a, b fragFile) bool {
	if a.Size <= 0 || a.Size != b.Size {
		return false
	}
	return a.Hash == "" || b.Hash == "" || a.Hash == b.Hash
}

// sameChapterRun reports whether every file of run is one chapter: two or
// more files, all pairwise sameChapterCopy. Pairwise, not against the first
// file only: with the first file's hash unknown, "hash-x" and "hash-y" would
// each pass against it and both retire as copies of different audio.
func sameChapterRun(run []*fragCandidate) bool {
	if len(run) < 2 {
		return false
	}
	for i := range run {
		for j := i + 1; j < len(run); j++ {
			if !sameChapterCopy(run[i], run[j]) {
				return false
			}
		}
	}
	return true
}

// fragGroupState is a no-parent row's Row.State: what Replan needs from plan
// time.
type fragGroupState struct {
	// Files maps each member book to the book_file row it was planned with,
	// so Replan finds each member's row by id wherever a cut-off run left it.
	Files         map[string]string `json:"files"`
	SurvivorTitle string            `json:"survivor_title"`
	SurvivorPath  string            `json:"survivor_path"`
	// Survivor and Roles are the plan's own choices: the survivor, each
	// member's track, and each copy's kept member. Replan pins them
	// (fragPin) rather than re-electing: the apply itself changes the flags
	// the election and keptOfCopies read (retireInto demotes before it
	// soft-deletes, and its version-group hand-off crowns another member),
	// so a re-election after a cut could keep a different file per chapter
	// or another survivor, and the row would never resume. Rows planned
	// before these fields existed derive them from Row.Members' roles.
	Survivor string                     `json:"survivor,omitempty"`
	Roles    map[string]fragPlannedRole `json:"roles,omitempty"`
	// Flags is each book's organized/primary flags at plan time. The pin
	// above stops a re-plan from re-electing on them; this stops it from
	// ignoring them: a LIVE book whose flags changed since the plan (a
	// member organized in place, say) is a change, unless this row's own
	// apply explains it (its demote of the book, cut off before the
	// soft-delete, or its retire hand-off crowning the book).
	Flags map[string]fragPlannedFlags `json:"flags,omitempty"`
	// PlannedAt is when the row was planned. With the row id it names this
	// row's apply runs: each run journals a plan record carrying both, and
	// a flag change is explained only by a run whose record matches
	// (fragJournal.forRow). A plan continuing an interrupted run keeps that
	// run's PlannedAt, so its own runs and the original's are one row's.
	// Not fingerprinted; Replan carries the planned value forward. A row
	// stored without it (or without Flags) is planned again.
	PlannedAt time.Time `json:"planned_at,omitempty"`
}

// fragPlannedFlags is one book's survivor-election flags as planned.
type fragPlannedFlags struct {
	Organized bool `json:"organized"`
	Primary   bool `json:"primary"`
}

// fragPlannedRole is one book's place in a no-parent row's plan: a member
// at Track (the survivor included), or a copy of member Of.
type fragPlannedRole struct {
	Copy  bool   `json:"copy,omitempty"`
	Track int    `json:"track,omitempty"`
	Of    string `json:"of,omitempty"`
}

// fragPin is a re-plan's view of the plan's choices (fragGroupState).
type fragPin struct {
	survivor string
	copies   map[string]bool
}

// importChapterSec is the IMPORT scanner's chapter threshold in seconds
// (chapter_consolidation_threshold_min, default 10). The folder-books fixer
// reads it as "a group shorter than this is a fragment, not a book". The
// resolver supplies the default for 0 or less, the same rule the scanner uses.
func importChapterSec() int {
	return config.AppConfig.ResolveChapterConsolidationThresholdMin() * 60
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
	return discGroupDir(c.origDir())
}

// discGroupDir is groupDir's rule for folder d.
func discGroupDir(d string) (dir string, disc int) {
	if rest, n, ok := metadata.DiscFolder(filepath.Base(d)); ok {
		parent := filepath.Dir(d)
		if rest == "" {
			return parent, n
		}
		return filepath.Join(parent, rest), n
	}
	return d, 0
}

// fragFolderKey is the folder a file at path groups under (discGroupDir of
// its folder).
func fragFolderKey(path string) string {
	d, _ := discGroupDir(filepath.Dir(path))
	return d
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
		// A leading PAIR ("02_Eldest_002_of_349", "8-02 Rubicon"): when every
		// file shares the first number it is the book or disc, and the second
		// is the chapter. When the first number varies it is a disc, and a
		// numbered set is not formed across discs (see discDir below).
		paired, sameMajor := true, true
		for _, e := range es {
			if !e.pos.LeadPair {
				paired = false
			} else if e.pos.Parts[0] != es[0].pos.Parts[0] {
				sameMajor = false
			}
		}
		if paired && sameMajor {
			for i := range es {
				es[i].num = es[i].pos.Parts[1]
			}
		}
		// Renamed copies (same position, same size; owner 2026-10-03) are one
		// chapter. They stay in set.members, where noParentRow sets them aside
		// again, but the set is judged on one file per chapter. Without this
		// the originals ("02_001") and the copies ("02_001 - Title - read by
		// narrator") read as two works side by side and became two books.
		// all keeps every file, copies included: the author, series, ASIN and
		// title checks below judge them too, since a copy is retired into the
		// survivor and must not carry another work's facts.
		all := es
		allKeyN := keyN
		oneWork, hadCopies := false, false
		{
			kept := es[:0:0]
			nCopies := 0
			for i := 0; i < len(es); {
				j := i + 1
				for j < len(es) && es[j].pos.Compare(es[i].pos) == 0 {
					j++
				}
				run := es[i:j]
				rc := make([]*fragCandidate, len(run))
				for k, q := range run {
					rc[k] = q.c
				}
				if sameChapterRun(rc) {
					kept = append(kept, run[keptOfCopies(lib, rc, allKeyN)])
					nCopies += len(run) - 1
				} else {
					kept = append(kept, run...)
				}
				i = j
			}
			if nCopies > 0 {
				hadCopies = true
				es = kept
				keyN = map[string]int{}
				for _, e := range es {
					keyN[e.key]++
				}
				oneWork = len(keyN) == 1
			}
		}
		// (the set is judged by setAuthor/setSeries below, not by its first file)
		// The set's author and series, ignoring junk fields (realAuthorID,
		// realSeriesID): the first real one any member carries.
		var setAuthor, setSeries *int
		junkAuthorNames := map[string]bool{}
		for _, e := range all {
			if a := lib.realAuthorID(e.c.Book, dir); a != nil && setAuthor == nil {
				setAuthor = a
			} else if a == nil && e.c.Book.AuthorID != nil {
				junkAuthorNames[junkLettersKey(lib.authorName(e.c.Book))] = true
			}
			if sr := lib.realSeriesID(e.c.Book, e.c.origStem()); sr != nil && setSeries == nil {
				setSeries = sr
			}
		}
		namedFor := lib.folderNamesAnyAuthor(filepath.Base(filepath.Clean(dir)))
		lo, hi := es[0].num, es[0].num
		for _, e := range es {
			lo, hi = min(lo, e.num), max(hi, e.num)
		}
		clean := filepath.Clean(dir)
		switch {
		case slices.Contains(lib.roots, clean):
			set.problem = fmt.Sprintf("%s is a library root or import path: it holds whatever was put there, not one work", dir)
		case lib.libraryRoot != "" && filepath.Dir(clean) == lib.libraryRoot:
			set.problem = fmt.Sprintf("%s sits directly under the library root: an author folder holds several works", dir)
		case setAuthor != nil && folderNamesAuthor(filepath.Base(clean), lib.authors[*setAuthor]):
			set.problem = fmt.Sprintf("the folder %q is named for the files' author (%q): an author folder holds several works", filepath.Base(clean), lib.authors[*setAuthor])
		case setAuthor == nil && namedFor != "" && !junkAuthorNames[junkLettersKey(namedFor)]:
			// Files with no (real) author linked, in a folder named like an
			// author the library knows: still an author folder. Not when that
			// "author" is the junk one the files themselves carry, copied from
			// this folder's own name.
			set.problem = fmt.Sprintf("the folder %q is named like the author %q: an author folder holds several works", filepath.Base(clean), namedFor)
		case discDir[dir]:
			// Disc folders keep the behaviour they had: the key groups
			// decide (a book's discs share one key), never a numbered set.
			// Not when the folder holds renamed copies: the key groups take
			// every file of a key, so originals and copies would become two
			// books of the same audio. Such a folder is held whole.
			set.sideBySide = !hadCopies
			set.problem = "some of the files sit in disc folders or carry a disc number: a numbered set is not formed across discs"
			if hadCopies {
				set.problem += "; the folder also holds renamed copies of its chapters, so it is held whole rather than split into books of the same audio"
			}
		case paired && !sameMajor:
			set.sideBySide = !hadCopies
			set.problem = fmt.Sprintf("the files carry disc-track numbers across several discs (%q … %q): a numbered set is not formed across discs",
				es[0].c.origStem(), es[len(es)-1].c.origStem())
		}
		for i := 1; i < len(es) && set.problem == ""; i++ {
			e := es[i]
			switch {
			case e.num == es[i-1].num && e.pos.Disc == es[i-1].pos.Disc &&
				!(e.pos.Compare(es[i-1].pos) == 0 && sameChapterCopy(e.c, es[i-1].c)):
				// The leading number, not the whole position: "05 - Ash" and
				// "05 - Ash (1)" are one chapter twice (a second download),
				// "01 - Book A" and "01 - Book B" two works.
				// A folder that holds renamed copies of its chapters is one
				// work, so a pair that differs in size is a conflict inside it,
				// never two works side by side.
				set.sideBySide = !hadCopies
				set.problem = fmt.Sprintf("%q and %q carry the same chapter number: a duplicate file, or more than one work in the folder", es[i-1].c.origStem(), e.c.origStem())
			}
		}
		for _, e := range all {
			if set.problem != "" {
				break
			}
			switch {
			case !sameOrMissing(lib.realAuthorID(e.c.Book, dir), setAuthor):
				set.problem = fmt.Sprintf("the files are by different authors (%q, %q)", lib.authors[*setAuthor], lib.authorName(e.c.Book))
			case !sameOrMissing(lib.realSeriesID(e.c.Book, e.c.origStem()), setSeries):
				set.problem = fmt.Sprintf("the files belong to different series (%q, %q)", lib.series[*setSeries], lib.seriesName(e.c.Book))
			}
		}
		if set.problem == "" && (lo > 1 || float64(hi-lo+1) > 1.25*float64(len(es))) {
			set.problem = fmt.Sprintf("the numbers run %d to %d over %d files: not a chapter run from 0 or 1 without large gaps", lo, hi, len(es))
		}
		if set.problem == "" && !oneWork {
			// Works side by side: one key's files all adjacent and at least
			// half the set, or no key standing alone. (Not when the copies
			// set aside leave one key: that is one work, kept once.) A folder
			// that held renamed copies is never split into key groups: its
			// keys are originals and copies of ONE work, and splitting them
			// would make two books of the same audio. It is held instead.
			single := 0
			for _, n := range keyN {
				if n == 1 {
					single++
				}
			}
			if single == 0 {
				set.sideBySide = !hadCopies
				set.problem = fmt.Sprintf("every one of the %d names in the folder is carried by two or more files: several multi-part works, not one work's chapters", len(keyN))
			}
			for i := 0; i < len(es) && set.problem == ""; {
				j := i
				for j < len(es) && es[j].key == es[i].key {
					j++
				}
				if run := j - i; run >= fragMinGroup && run == keyN[es[i].key] && 2*run >= len(es) {
					set.sideBySide = !hadCopies
					set.problem = fmt.Sprintf("the %d files keyed %q sit together as one block (%q … %q) and are half the folder or more: a work of its own beside the others",
						run, es[i].key, es[i].c.origStem(), es[j-1].c.origStem())
				}
				i = j
			}
		}
		for _, e := range all {
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
			major := map[string]map[int]bool{}
			for _, e := range es {
				if e.pos.LeadPair {
					if major[e.key] == nil {
						major[e.key] = map[int]bool{}
					}
					major[e.key][e.pos.Parts[0]] = true
				}
			}
			for _, e := range es {
				n := e.num
				// A key whose files all share one disc of a leading pair
				// ("1-01 Book A" … "1-04 Book A"): its run is the tracks.
				if e.pos.LeadPair && len(major[e.key]) == 1 {
					n = e.pos.Parts[1]
				}
				nums[e.key] = append(nums[e.key], n)
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
	holdSameAudioRows(rows)
	return rows
}

// holdSameAudioRows holds every pair of rows from one folder that hold one
// chapter twice between them: a file of one and a file of the other at the
// same chapter position with the same size and no conflicting hash
// (sameChapterFiles). Renamed copies whose names put them in another group
// than their originals ("001 - Arrival" beside "Light of Other Days - Part
// 001", "Chapter 001" beside the same) would otherwise make two applicable
// rows and two books of the same audio. Within one row the copies are set
// aside (noParentRow); across rows nothing could tell which files form the
// book, so both are held and each names the other. Plan only: Replan sees
// one row's books, and a held row is never re-planned.
func holdSameAudioRows(rows []repairs.Row) {
	type placedFile struct {
		pos  metadata.ChapterPos
		file fragFile
		stem string
	}
	files := make([][]placedFile, len(rows))
	byDir := map[string][]int{}
	for i, r := range rows {
		plan, ok := r.Detail.(*fragGroupPlan)
		if !ok {
			continue
		}
		add := func(c *fragCandidate) {
			if pos, ok := chapterPos(c); ok && len(pos.Parts) > 0 {
				files[i] = append(files[i], placedFile{pos, c.File, c.origStem()})
			}
		}
		for _, m := range plan.Members {
			add(m.Frag)
		}
		for _, cp := range plan.Copies {
			add(cp.Frag)
		}
		byDir[plan.Dir] = append(byDir[plan.Dir], i)
	}
	why := map[int][]string{}
	for _, idx := range byDir {
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				i, j := idx[a], idx[b]
			pair:
				for _, x := range files[i] {
					for _, y := range files[j] {
						if x.pos.Compare(y.pos) == 0 && sameChapterFiles(x.file, y.file) {
							why[i] = append(why[i], fmt.Sprintf("%q here and %q in row %s", x.stem, y.stem, rows[j].RowID))
							why[j] = append(why[j], fmt.Sprintf("%q here and %q in row %s", y.stem, x.stem, rows[i].RowID))
							break pair
						}
					}
				}
			}
		}
	}
	for i, ws := range why {
		r := &rows[i]
		sort.Strings(ws)
		r.Evidence = append(r.Evidence, "the same chapter, same size, sits in another row of this folder: "+strings.Join(ws, "; "))
		if r.Class == fragClassManual {
			continue // already held for its guard; the evidence still names the other row
		}
		r.Risk, r.Skipped = repairs.RiskReview, fragSkipSameAudioRows
		r.SkipReason = fmt.Sprintf("files of this row are the same chapters (same position, same size) as files of another row from this folder (%s): applying both would make two books of the same audio; decide which files form the book, then plan again",
			strings.Join(ws, "; "))
	}
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
	// Renamed copies: a run of files at one position that are all one
	// chapter (sameChapterCopy) keeps one file, chosen by keptOfCopies (the
	// first organized-and-primary file in run order if any is, else the
	// folder's commonest chapter key, else the lowest book id), and sets the
	// others aside as copies. keyN counts over every file of the row, as
	// numberedSets counts over the same files.
	var copies []placed
	copyOf := map[string]string{}
	keyN := map[string]int{}
	for _, c := range cs {
		k, _ := metadata.ChapterGroupKey(c.origStem())
		keyN[k]++
	}
	{
		kept := ps[:0:0]
		for i := 0; i < len(ps); {
			j := i + 1
			for j < len(ps) && ps[j].ok && ps[i].ok && ps[j].pos.Compare(ps[i].pos) == 0 {
				j++
			}
			run := ps[i:j]
			rc := make([]*fragCandidate, len(run))
			for k, q := range run {
				rc[k] = q.c
			}
			if !sameChapterRun(rc) {
				kept = append(kept, run...)
				i = j
				continue
			}
			keep := keptOfCopies(lib, rc, keyN)
			kept = append(kept, run[keep])
			for k, q := range run {
				if k != keep {
					copies = append(copies, q)
					copyOf[q.c.Book.ID] = run[keep].c.Book.ID
				}
			}
			i = j
		}
		ps = kept
	}
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
	offsetOf := map[string]float64{}
	for i, p := range ps {
		plan.Members = append(plan.Members, fragGroupMember{Frag: p.c, Track: i + 1, Offset: off})
		offsetOf[p.c.Book.ID] = off
		off += float64(p.c.File.Duration)
		ids = append(ids, p.c.Book.ID)
		books = append(books, p.c.Book)
		extra[p.c.Book.ID] = []string{p.c.ImportPath}
	}
	sort.Strings(ids)
	memberIDs := append([]string(nil), ids...)
	for _, q := range copies {
		plan.Copies = append(plan.Copies, fragGroupCopy{Frag: q.c, Of: copyOf[q.c.Book.ID], Offset: offsetOf[copyOf[q.c.Book.ID]]})
		ids = append(ids, q.c.Book.ID)
		books = append(books, q.c.Book)
		extra[q.c.Book.ID] = []string{q.c.ImportPath}
	}
	sort.Strings(ids)
	sort.Slice(plan.Copies, func(i, j int) bool { return plan.Copies[i].Frag.Book.ID < plan.Copies[j].Frag.Book.ID })
	// The survivor: the lowest-id member that is organized and primary, so it
	// is a book ABS shows and a version group can keep. When no member is
	// organized (every chapter still sits unorganized in an import folder),
	// the lowest-id primary member: the merged book is then exactly as visible
	// as its fragments were, and is organized like any other imported
	// multi-file book. Until 2026-10-03 such a set was held as "no survivor"
	// (63 rows on prod once the 120-minute limit released them).
	if lib.pin != nil {
		// A re-plan keeps the planned survivor; its live flags are still in
		// the fingerprint (survivorState), so a real change to it refuses.
		if slices.Contains(memberIDs, lib.pin.survivor) {
			plan.SurvivorID = lib.pin.survivor
		}
	} else {
		for _, id := range memberIDs {
			if b := lib.books[id]; b.Organized && b.Primary {
				plan.SurvivorID = id
				break
			}
		}
	}
	anyOrganized := false
	for _, id := range ids {
		anyOrganized = anyOrganized || lib.books[id].Organized
	}
	if plan.SurvivorID == "" && !anyOrganized && lib.pin == nil {
		for _, id := range memberIDs {
			if lib.books[id].Primary {
				plan.SurvivorID = id
				break
			}
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
	state := fragGroupState{Files: map[string]string{}, SurvivorTitle: survivor.Title, SurvivorPath: survivor.FilePath,
		Survivor: plan.SurvivorID, Roles: map[string]fragPlannedRole{}, Flags: map[string]fragPlannedFlags{},
		PlannedAt: f.now().UTC()}
	for _, id := range ids {
		b := lib.books[id]
		state.Flags[id] = fragPlannedFlags{Organized: b.Organized, Primary: b.Primary}
	}
	for _, m := range plan.Members {
		state.Roles[m.Frag.Book.ID] = fragPlannedRole{Track: m.Track}
	}
	for _, cp := range plan.Copies {
		state.Roles[cp.Frag.Book.ID] = fragPlannedRole{Copy: true, Of: cp.Of}
	}
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
	for _, cp := range plan.Copies {
		r.Members = append(r.Members, member(lib, cp.Frag.Book, "copy"))
		state.Files[cp.Frag.Book.ID] = cp.Frag.File.ID
		if !cp.Frag.Present {
			missing++
		}
		// A copy is retired like a member, so an iTunes id on it makes the
		// row manual exactly as one on a member does (retireInto refuses it,
		// and the refusal would come after the members had moved).
		if pid := cp.Frag.itunesPID(); pid != "" && withPID == "" {
			withPID = fmt.Sprintf("copy %s carries %s", cp.Frag.Book.ID, pid)
		}
		fpParts = append(fpParts, fmt.Sprintf("copy|%s|%s|%s|%s", cp.Frag.Book.ID, cp.Frag.File.ID, cp.Frag.File.Path, cp.Of))
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
		fmt.Sprintf("durations of the %d kept chapter file(s): %d known, %d unknown, %d at or over %d min", len(plan.Members), len(plan.Members)-unknown, unknown, long, limit/60),
		"no existing book owns any of these files",
	}
	if len(stems) > 0 {
		more := ""
		if len(cs) > len(stems) {
			more = fmt.Sprintf(" … and %d more", len(cs)-len(stems))
		}
		r.Evidence = append(r.Evidence, "in order: "+strings.Join(stems, " | ")+more)
	}
	if len(plan.Copies) > 0 {
		var ex []string
		for _, cp := range plan.Copies {
			if len(ex) < 6 {
				ex = append(ex, fmt.Sprintf("%q (copy of %s)", cp.Frag.origStem(), cp.Of))
			}
		}
		r.Evidence = append(r.Evidence, fmt.Sprintf("%d renamed copies of chapters (same position, same size) are retired, not added: %s",
			len(plan.Copies), strings.Join(ex, ", ")))
	}
	r.Current = map[string]string{"fragments": strconv.Itoa(len(cs)), "folder": dir}
	moving := len(plan.Members) - 1
	action := fmt.Sprintf("move %d fragment row(s) onto %s in track order; retire %d emptied book(s) into it", moving, plan.SurvivorID, moving)
	if len(plan.Copies) > 0 {
		action += fmt.Sprintf("; retire %d renamed copy book(s) into it, each keeping its own file row", len(plan.Copies))
	}
	r.Proposed = map[string]string{
		"action":   action,
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
		r.Skipped, r.SkipReason = fragSkipDurationUnknown, fmt.Sprintf("%d of %d chapter files have no recorded duration; cannot tell chapters from whole books", unknown, len(plan.Members))
	case long > 0:
		r.Skipped, r.SkipReason = fragSkipDurationGate, fmt.Sprintf("%d of %d chapter files run %d min or longer: likely separate books, not chapters", long, len(plan.Members), limit/60)
	case orderProblem != "":
		r.Skipped, r.SkipReason = fragSkipTrackOrder, orderProblem+": the chapter order cannot be told"
	case plan.SurvivorID == "":
		r.Skipped, r.SkipReason = fragSkipNoSurvivor, "no member can take the others' files: none is organized and primary, and none is a primary unorganized book either (an organized member that is not primary blocks the fallback)"
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
	return f.replan(ctx, planned, nil)
}

// replan is Replan with beat, the scan stand-down renewal a long journal
// pass calls (Apply passes its Writer's Beat; nil skips it).
func (f *fragmentFixer) replan(ctx context.Context, planned repairs.Row, beat func(string) error) (repairs.Row, error) {
	return f.replanWith(ctx, planned, beat, nil)
}

// replanWith is replan with pre, a journal already read for the row's books
// (nil reads it here when needed).
func (f *fragmentFixer) replanWith(ctx context.Context, planned repairs.Row, beat func(string) error, pre *fragJournal) (repairs.Row, error) {
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
		return f.replanGroup(ctx, store, lib, hist, planned, beat, pre)
	case fragClassCarry:
		return f.replanCarry(store, lib, planned)
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

func (f *fragmentFixer) replanGroup(ctx context.Context, store OpsStore, lib *fragLibrary, hist FragmentRepairReader, planned repairs.Row, beat func(string) error, pre *fragJournal) (repairs.Row, error) {
	var st fragGroupState
	if len(planned.State) == 0 || json.Unmarshal(planned.State, &st) != nil || len(st.Files) == 0 {
		return changedRow(planned, "the plan carries no stored member rows; plan again"), nil
	}
	survivorID := planned.Proposed["survivor"]
	// A row with no survivor (held for it) has no choices to pin: it is
	// re-evaluated as planned and reports its own skip reason.
	if pin := plannedPin(st, planned); pin != nil {
		if pin.survivor != survivorID {
			return changedRow(planned, fmt.Sprintf("the stored survivor %s is not the proposed %s; plan again", pin.survivor, survivorID)), nil
		}
		lib.pin = pin
	}
	// Flags and PlannedAt are what the checks below judge the live books
	// against. A row stored without them cannot be judged: skipping the
	// check would let a flag changed since the plan through unseen.
	if len(st.Flags) == 0 || st.PlannedAt.IsZero() {
		return changedRow(planned, "the plan carries no stored election flags or plan time; plan again"), nil
	}
	for _, id := range planned.BookIDs {
		if _, ok := st.Flags[id]; !ok {
			return changedRow(planned, fmt.Sprintf("the plan carries no stored flags for book %s; plan again", id)), nil
		}
	}
	// The journal is read once, and only for the books that need it: the
	// retired ones (who retired them, and whose hand-off crowned whom), the
	// live ones whose primary flag changed since the plan (our own demote,
	// cut off before the soft-delete, or our hand-off), and then the
	// survivor, which carries the plan records naming this row's runs
	// (fragJournal.forRow). A fresh row (nothing written yet) reads none of
	// it. pre, when set, is a journal the caller already read for these
	// books (a plan continuing several interrupted rows reads it once).
	need := map[string]bool{}
	for _, id := range planned.BookIDs {
		b, ok := lib.books[id]
		if !ok {
			continue
		}
		if (b.SoftDeleted && id != survivorID) || b.Primary != st.Flags[id].Primary {
			need[id] = true
		}
	}
	var jr *fragJournal
	if len(need) > 0 {
		need[survivorID] = true
		jr = pre
		if jr == nil {
			var err error
			if jr, err = f.loadJournal(ctx, hist, need, beat); err != nil {
				return repairs.Row{}, err
			}
		}
		jr = jr.forRow(survivorID, planned.RowID, st.PlannedAt)
	}
	crowns := fragCrowns{named: map[string]string{}, pending: map[string]bool{}, winners: map[string]string{}}
	for _, id := range planned.BookIDs {
		b, ok := lib.books[id]
		if !ok || !b.SoftDeleted {
			continue
		}
		// Resumable only when THIS fixer's apply retired it into THIS row's
		// survivor: merged_into says where a book went, not who sent it.
		if why := jr.retiredHere(b, survivorID); why != "" {
			return changedRow(planned, why), nil
		}
		jr.noteCrowns(b, &crowns)
	}
	// A book whose journal rows the 90-day prune removed (only plan records
	// are kept) has no demote or hand-off note left to credit a flag change
	// to. Its group is treated as a hand-off of this row's that may have
	// run: the replay below decides it from the planned flags, exactly as
	// in the crash window, so a flag change the run's own hand-off makes is
	// explained and any other is still a change (review 9 follow-up: after
	// a prune, 12 of 24 cut points could not continue).
	for _, id := range planned.BookIDs {
		if b, ok := lib.books[id]; ok && b.VersionGroup != "" && jr.pruned(id) {
			crowns.pending[b.VersionGroup] = true
		}
	}
	for g := range crowns.pending {
		w, err := f.replayHandOff(ctx, store, g, st.Flags)
		if err != nil {
			return changedRow(planned, fmt.Sprintf("the owed hand-off of version group %s cannot be replayed: %v", g, err)), nil
		}
		crowns.winners[g] = w
	}
	// Every live book is judged, the survivor included: since 2026-10-03 a
	// survivor inside a retired member's version group has its own flag
	// changed by this row's hand-off (crowned from a nil flag, or demoted
	// when the hand-off crowns another member), and a change nobody explains
	// must still refuse it.
	for _, id := range planned.BookIDs {
		b, ok := lib.books[id]
		if !ok || b.SoftDeleted {
			continue
		}
		if why := jr.liveFlagsExplained(b, st.Flags[id], crowns, id != survivorID); why != "" {
			return changedRow(planned, why), nil
		}
	}
	// The survivor's flags are explained (or unchanged): the decision is
	// judged on the flags it was planned with, so the fingerprint's survivor
	// state does not move with this row's own hand-off.
	if sb, ok := lib.books[survivorID]; ok {
		fl := st.Flags[survivorID]
		sb.Organized, sb.Primary = fl.Organized, fl.Primary
		lib.books[survivorID] = sb
	}
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
		if id == survivorID && planned.Proposed["title"] != "" && b.Title == planned.Proposed["title"] {
			// This row's own retitle already ran (a run cut off after it):
			// judge the survivor by the title the plan saw, or the numbered
			// set's "titled after its file" test reads our own write as a
			// work with a title of its own and the row never resumes. The
			// title is still checked below against both values.
			b.Title = st.SurvivorTitle
		}
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
			if why := pinnedRolesDiffer(plan, st); why != "" {
				return changedRow(planned, why), nil
			}
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

// plannedPin is the plan's survivor and copies for a re-plan: from the
// stored state, or, for a row planned before Survivor/Roles were stored,
// from the roles Row.Members lists ("survivor", "fragment", "copy"; a
// "co-owner" is not in the row).
func plannedPin(st fragGroupState, planned repairs.Row) *fragPin {
	pin := &fragPin{copies: map[string]bool{}}
	if st.Survivor != "" && len(st.Roles) > 0 {
		pin.survivor = st.Survivor
		for id, role := range st.Roles {
			if role.Copy {
				pin.copies[id] = true
			}
		}
		return pin
	}
	for _, m := range planned.Members {
		switch m.Role {
		case "survivor":
			pin.survivor = m.BookID
		case "copy":
			pin.copies[m.BookID] = true
		}
	}
	if pin.survivor == "" {
		return nil
	}
	return pin
}

// pinnedRolesDiffer compares a re-planned group with the plan's stored
// roles (when it has them): the same members at the same tracks, the same
// copies of the same members. "" when they agree.
func pinnedRolesDiffer(plan *fragGroupPlan, st fragGroupState) string {
	if len(st.Roles) == 0 {
		return ""
	}
	seen := 0
	for _, m := range plan.Members {
		role, ok := st.Roles[m.Frag.Book.ID]
		if !ok || role.Copy || role.Track != m.Track {
			return fmt.Sprintf("book %s is now member track %d, not as planned (%+v)", m.Frag.Book.ID, m.Track, role)
		}
		seen++
	}
	for _, cp := range plan.Copies {
		role, ok := st.Roles[cp.Frag.Book.ID]
		if !ok || !role.Copy || role.Of != cp.Of {
			return fmt.Sprintf("book %s is now a copy of %s, not as planned (%+v)", cp.Frag.Book.ID, cp.Of, role)
		}
		seen++
	}
	if seen != len(st.Roles) {
		return fmt.Sprintf("the group now has %d planned books, not %d", seen, len(st.Roles))
	}
	return ""
}

// fragJournal is one re-plan's view of the operation journal for the books
// it must judge (byBook), read in ONE pass.
//
// A RETIRE is attributed to this fixer on any of its rows, at any time
// (retiredHere): a plan that continues an interrupted run carries that run's
// retires, which predate it.
//
// A FLAG change is explained only by the rows of this row's own apply runs
// (rowOps, set by forRow): the operations that journaled a plan record
// (undo.ChangeTypeRepairPlanRecord) for this row id and plan time. A
// continuation (see continueInterrupted) carries the original plan's time,
// so the original apply, its resumes and the applies of every plan that
// continues it are one row's runs, while an apply of any other plan, earlier
// or later, explains nothing here. Until 2026-10-03 the rule was "this
// fixer's rows written since the plan", which a different plan's apply
// written after it also passed, and which un-explained a continued run's own
// writes (they predate the plan that continues them).
type fragJournal struct {
	byBook map[string][]*database.OperationChange
	ops    repairs.OpReader
	// opOurs caches the op-row fallback (ourChange) per operation id.
	opOurs map[string]bool
	// rowOps are this row's apply operations (forRow); nil until set.
	rowOps map[string]bool
}

// fragCrowns is the evidence, from this row's retired books, for a live
// member's primary flag changed by a hand-off: named maps each member a
// hand-off note credits as crowned to that note's version group; pending
// holds the groups where one of this row's runs demoted and retired a member
// whose hand-off note is not journaled (the crown is written straight to the
// store BEFORE its note, so a run cut between the two leaves a crown with no
// row naming it). winners holds, per pending group, the member a replay of
// that hand-off crowns (replayHandOff; "" when it would crown nobody).
type fragCrowns struct {
	named   map[string]string
	pending map[string]bool
	winners map[string]string
}

// opChangeScanner is PebbleStore's one-pass journal read, for a caller that
// needs the WHOLE journal (Plan's search for interrupted runs). Resolved with
// database.AsCapability.
type opChangeScanner interface {
	ScanOperationChanges(fn func(*database.OperationChange) error) error
}

// opChangeIndexReporter tells whether GetBookChanges is indexed (the
// opchange_by_book index, trusted). When it is, a re-plan reads its few books
// one GetBookChanges each instead of scanning the whole journal; when it is
// not, each GetBookChanges is itself a full scan and the one-pass scan wins.
type opChangeIndexReporter interface {
	OpChangeByBookIndexUsable() bool
}

// fragJournalBeatEvery is how many journal rows a pass reads between lease
// renewals.
const fragJournalBeatEvery = 20000

// newFragJournal is an empty journal view wired to the op-row reader.
func (f *fragmentFixer) newFragJournal() *fragJournal {
	jr := &fragJournal{byBook: map[string][]*database.OperationChange{}, opOurs: map[string]bool{}}
	if r, ok := f.p.deps.OperationQueueStore().(repairs.OpReader); ok {
		jr.ops = r
	}
	return jr
}

// loadJournal reads the journal rows of books ids: one GetBookChanges per
// book when the store's opchange_by_book index is usable (a point read per
// row of those books), else one full ScanOperationChanges pass (cheaper than
// N unindexed GetBookChanges, each a full scan of its own), else one
// GetBookChanges per book on a store with neither. A read runs under the
// merge lock during an apply, so it renews the stand-down lease (beat) as it
// goes rather than let a long journal outlast it.
func (f *fragmentFixer) loadJournal(ctx context.Context, hist FragmentRepairReader, ids map[string]bool, beat func(string) error) (*fragJournal, error) {
	jr := f.newFragJournal()
	tick := func(what string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if beat != nil {
			return beat(what)
		}
		return nil
	}
	keep := func(c *database.OperationChange) {
		if c != nil && ids[c.BookID] {
			jr.byBook[c.BookID] = append(jr.byBook[c.BookID], c)
		}
	}
	indexed := indexStillUsable(hist)
	_, scannable := database.AsCapability[opChangeScanner](hist)
	if scannable && !indexed {
		return f.scanJournal(hist, ids, tick)
	}
	for id := range ids {
		if err := tick("the journal read of " + id); err != nil {
			return nil, err
		}
		if indexed && scannable && !indexStillUsable(hist) {
			// The index stopped being usable mid-read (a rebuild cleared its
			// trust): each further GetBookChanges would scan the whole
			// journal under the merge lock. Read it in one pass instead.
			return f.scanJournal(hist, ids, tick)
		}
		cs, err := hist.GetBookChanges(id)
		if err != nil {
			return nil, fmt.Errorf("changes of %s: %w", id, err)
		}
		for _, c := range cs {
			keep(c)
		}
	}
	return jr, nil
}

// indexStillUsable reports whether hist's GetBookChanges reads the
// opchange_by_book index right now (false for a store that cannot say).
func indexStillUsable(hist FragmentRepairReader) bool {
	ix, ok := database.AsCapability[opChangeIndexReporter](hist)
	return ok && ix.OpChangeByBookIndexUsable()
}

// scanJournal is loadJournal's one-pass read: every opchange row, keeping
// those of books ids. hist must resolve opChangeScanner.
func (f *fragmentFixer) scanJournal(hist FragmentRepairReader, ids map[string]bool, tick func(string) error) (*fragJournal, error) {
	sc, ok := database.AsCapability[opChangeScanner](hist)
	if !ok {
		return nil, fmt.Errorf("%s: the store cannot scan the operation journal", fragFixerID)
	}
	jr := f.newFragJournal()
	n := 0
	err := sc.ScanOperationChanges(func(c *database.OperationChange) error {
		if n++; n%fragJournalBeatEvery == 0 {
			if err := tick("the journal read of a fragment re-plan"); err != nil {
				return err
			}
		}
		if c != nil && ids[c.BookID] {
			jr.byBook[c.BookID] = append(jr.byBook[c.BookID], c)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the operation journal: %w", err)
	}
	return jr, nil
}

// forRow is jr scoped to one row: rowOps are the operations whose plan
// record on survivor names rowID and plannedAt (see fragJournal). The byBook
// rows and the op-row cache are shared, not copied.
func (jr *fragJournal) forRow(survivor, rowID string, plannedAt time.Time) *fragJournal {
	if jr == nil {
		return nil
	}
	cp := *jr
	cp.rowOps = map[string]bool{}
	for _, c := range jr.byBook[survivor] {
		if c.ChangeType != undo.ChangeTypeRepairPlanRecord || c.RevertedAt != nil || c.Source != fragFixerID ||
			c.FieldName != fragRecordField(rowID) {
			continue
		}
		if rec, ok := decodePlanRecord(c); ok && rec.RowID == rowID && rec.PlannedAt.Equal(plannedAt) {
			cp.rowOps[c.OperationID] = true
		}
	}
	return &cp
}

// ourChange reports whether journal row c was written by an apply run of
// this fixer. The row's own Source (repairs.Writer.Journal stamps the
// fixer id) decides when set: fragFixerID is ours, any other fixer is not.
// The operation row is NOT the evidence for those rows: registry.Discard
// deletes a failed or interrupted op's row (exactly how a cut-off apply
// ends) and keeps its journal, so an op-row lookup would disown our own
// half-finished retire.
//
// A row with no Source (written before the field existed) falls back to the
// op row: a repairs apply whose params name fragFixerID. A missing op row is
// never ours, so a discarded legacy run refuses the row (plan again) rather
// than guess. The journal row is written BEFORE the write it describes, so
// either way the attribution survives a process that dies right after the
// write (the Writer's history rows, which carry the fixer id too, are
// written after it and can be missing).
func (jr *fragJournal) ourChange(c *database.OperationChange) bool {
	switch c.Source {
	case fragFixerID:
		return true
	case "":
	default:
		return false
	}
	if v, ok := jr.opOurs[c.OperationID]; ok {
		return v
	}
	ours := false
	if jr.ops != nil {
		if row, err := jr.ops.GetOperationV2(c.OperationID); err == nil && row != nil && row.DefID == repairs.ApplyOpID {
			var p repairs.ApplyParams
			ours = json.Unmarshal([]byte(row.Params), &p) == nil && p.FixerID == fragFixerID
		}
	}
	jr.opOurs[c.OperationID] = ours
	return ours
}

// ourRows returns book id's un-reverted journal rows of type changeType
// written by any apply run of this fixer, at any time.
func (jr *fragJournal) ourRows(id, changeType string) []*database.OperationChange {
	if jr == nil {
		return nil
	}
	var out []*database.OperationChange
	for _, c := range jr.byBook[id] {
		if c.ChangeType == changeType && c.BookID == id && c.RevertedAt == nil && jr.ourChange(c) {
			out = append(out, c)
		}
	}
	return out
}

// runRows is ourRows limited to this row's own apply runs (rowOps).
func (jr *fragJournal) runRows(id, changeType string) []*database.OperationChange {
	if jr == nil {
		return nil
	}
	var out []*database.OperationChange
	for _, c := range jr.ourRows(id, changeType) {
		if jr.rowOps[c.OperationID] {
			out = append(out, c)
		}
	}
	return out
}

// pruned reports whether book id has no journal row left but plan records
// while this row's plan record stands: the run's other rows were removed by
// the 90-day prune, which keeps only plan records
// (database.OpChangeTypesKeptByPrune).
func (jr *fragJournal) pruned(id string) bool {
	if jr == nil || len(jr.rowOps) == 0 {
		return false
	}
	for _, c := range jr.byBook[id] {
		if c.ChangeType != undo.ChangeTypeRepairPlanRecord {
			return false
		}
	}
	return true
}

// retireNote reports whether hand-off note c was written by a fixer that
// retires through retireInto (retireFixerIDs): one of them may finish
// another's owed hand-off (resumeHandOff), so the member its note names is
// crowned by the hand-off this row's demote made owed.
func (jr *fragJournal) retireNote(c *database.OperationChange) bool {
	if c.Source == "" {
		return jr.ourChange(c)
	}
	return retireFixerIDs[c.Source]
}

// retiredHere is "" when retired book b was retired by this fixer into
// survivor: merged_into names survivor and an un-reverted book_merged_into
// row naming it was journaled by one of this fixer's apply runs (at any
// time: see fragJournal). Otherwise it says what is missing.
func (jr *fragJournal) retiredHere(b fragBook, survivor string) string {
	if b.MergedInto != survivor {
		return fmt.Sprintf("book %s was retired into %q, not into the survivor %s", b.ID, b.MergedInto, survivor)
	}
	for _, c := range jr.ourRows(b.ID, undo.ChangeTypeBookMergedInto) {
		if c.NewValue == survivor {
			return ""
		}
	}
	if len(jr.rowOps) > 0 && len(jr.byBook[b.ID]) == 0 {
		// No journal row of this book at all (the 90-day prune removed
		// them) while this row's plan record stands: the run that record
		// starts is the evidence. A retire by anyone else would have left
		// its own merge row, younger than the run's.
		return ""
	}
	return fmt.Sprintf("book %s is merged into the survivor %s, but not by a %s apply (no journaled retire of one into it stands)", b.ID, survivor, fragFixerID)
}

// noteCrowns adds retired book b's hand-off evidence to cr. Only a demote of
// b by one of this row's runs makes a hand-off this row's:
//   - each member a hand-off note on b names as crowned in b's group, when
//     the note was written by one of this row's runs, or by any retire fixer
//     (retireNote) after this row's newest demote of b (that fixer finished
//     the hand-off our demote made owed);
//   - b's group as pending when this row's newest demote of b is newer than
//     every hand-off note on b (of anyone's): the hand-off is owed, and its
//     crown may already be written with the note cut off (the crash window
//     between the two writes).
//
// A retiree this row never demoted (it was not primary) adds nothing, so it
// never holds the window open.
func (jr *fragJournal) noteCrowns(b fragBook, cr *fragCrowns) {
	if jr == nil || b.VersionGroup == "" {
		return
	}
	demote := ""
	for _, c := range jr.runRows(b.ID, undo.ChangeTypeBookPrimaryDemote) {
		if c.ID > demote {
			demote = c.ID
		}
	}
	handedOff := false
	for _, c := range jr.byBook[b.ID] {
		if c.ChangeType != undo.ChangeTypeBookPrimaryHandoff || c.BookID != b.ID || c.RevertedAt != nil {
			continue
		}
		if demote != "" && c.ID > demote {
			handedOff = true
		}
		id, ok := undo.HandOffCrowned(c)
		if !ok || c.NewValue != b.VersionGroup {
			continue
		}
		if (jr.rowOps[c.OperationID] && jr.ourChange(c)) || (demote != "" && c.ID > demote && jr.retireNote(c)) {
			cr.named[id] = b.VersionGroup
		}
	}
	if demote != "" && !handedOff {
		cr.pending[b.VersionGroup] = true
	}
}

// liveFlagsExplained is "" when live book b's election flags are as planned
// (fl), or differ only as this row's own apply runs change them:
//   - the primary flag dropped by this row's demote of b (a retire cut off
//     before its soft-delete), or by one of this row's hand-offs in b's group
//     crowning another member (a hand-off writes explicit false on every
//     other live member: versionprimary's writeWinner and demoteOthers);
//   - the primary flag raised by one of this row's hand-offs crowning b.
//
// A hand-off counts when a note credits it (crowns.named) or, in the crash
// window where the crown is written and its note is not (crowns.pending),
// when a replay of that hand-off on the group as it stood before it crowns
// exactly b (raised), or someone other than b (dropped). The replay is
// versionprimary.ChooseSinglePrimary, EnsureSinglePrimary's own decision, so
// a crown of any other member in that window (an outside actor) is a change.
// One the outside actor happened to give the same member is accepted: it is
// the state this row's hand-off leaves anyway.
//
// A member (retiree, not the survivor) whose journal rows the 90-day prune
// removed (jr.pruned) has no demote row left either: its primary flag
// dropped is this row's own demote, since the run demotes every member it
// retires (retireInto demotes before it soft-deletes) and the row retires
// it anyway.
//
// Any other difference (a member organized in place, demoted or crowned by
// someone else) is a change.
func (jr *fragJournal) liveFlagsExplained(b fragBook, fl fragPlannedFlags, crowns fragCrowns, retiree bool) string {
	if b.Organized != fl.Organized {
		return fmt.Sprintf("book %s is now organized=%t, not %t as planned", b.ID, b.Organized, fl.Organized)
	}
	if b.Primary == fl.Primary {
		return ""
	}
	g := b.VersionGroup
	if fl.Primary {
		if len(jr.runRows(b.ID, undo.ChangeTypeBookPrimaryDemote)) > 0 || (retiree && jr.pruned(b.ID)) {
			return ""
		}
		if g != "" {
			for crowned, cg := range crowns.named {
				if cg == g && crowned != b.ID {
					return ""
				}
			}
			if w := crowns.winners[g]; crowns.pending[g] && w != "" && w != b.ID {
				return ""
			}
		}
	} else if g != "" && (crowns.named[b.ID] == g || (crowns.pending[g] && crowns.winners[g] == b.ID)) {
		return ""
	}
	return fmt.Sprintf("book %s is now primary=%t, not %t as planned, and this row's apply did not change it", b.ID, b.Primary, fl.Primary)
}

// replayHandOff is the member versionprimary.EnsureSinglePrimary would crown
// in group gid as it stood before a hand-off of this row: the group's live
// rows now, with every member this row planned non-primary that is explicit
// primary now set back to false (a crown is the only write that raises one).
// "" when there is no hand-off store (no hand-off ever ran) or it would hold.
func (f *fragmentFixer) replayHandOff(ctx context.Context, store OpsStore, gid string, planned map[string]fragPlannedFlags) (string, error) {
	vps := f.p.deps.VersionPrimaryStore()
	if vps == nil {
		return "", nil
	}
	members, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		return "", fmt.Errorf("read version group %s: %w", gid, err)
	}
	for i := range members {
		m := &members[i]
		if fl, ok := planned[m.ID]; ok && !fl.Primary && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			no := false
			m.IsPrimaryVersion = &no
		}
	}
	return versionprimary.ChooseSinglePrimary(ctx, fragEnsureStore{OpsStore: store, chapters: vps}, members,
		versionprimary.Env{RootDir: config.AppConfig.RootDir})
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

// rowPaths lists the fragment files a row folds, a numbered set's renamed
// copies included: the paths checkOwners guards at apply and holdCoOwned
// reads at plan. A copy keeps its row on its retired book, but a live book
// outside the row that also claims the copy's file is still a co-owner the
// owner must see.
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
		for _, cp := range d.Copies {
			paths = append(paths, cp.Frag.File.Path)
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
	locked, err := f.replan(ctx, fresh, w.Beat)
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
	case []fragCarry:
		// Grouped by the book the files are on: one journaled move each
		// (repairs.Writer.MoveBookFiles), which a revert puts back.
		byFrom := map[string][]string{}
		var froms []string
		for _, c := range plan {
			if byFrom[c.From] == nil {
				froms = append(froms, c.From)
			}
			byFrom[c.From] = append(byFrom[c.From], c.File)
		}
		sort.Strings(froms)
		for _, from := range froms {
			if err := ctx.Err(); err != nil {
				return partial(err)
			}
			if err := w.MoveBookFiles(byFrom[from], from, plan[0].To); err != nil {
				return partial(err)
			}
			steps++
		}
		return nil
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
		// Copies are retired last, so a copy retireInto would refuse is
		// found here, before the first write: a refusal then writes nothing
		// instead of leaving the members moved and the copy live.
		for _, cp := range plan.Copies {
			if err := copyRetireRefusal(store, cp, plan.SurvivorID); err != nil {
				return err
			}
		}
		// The run's plan record goes into the journal before its first
		// write, so a plan made after a cut CONTINUES this run with this
		// decision (continueInterrupted) and a re-plan credits flag changes
		// to this row's runs only (fragJournal.forRow). A row with nothing
		// left to write journals none: resuming a finished row writes
		// nothing at all.
		var st fragGroupState
		if err := json.Unmarshal(locked.State, &st); err != nil {
			return fmt.Errorf("%s: row %s state unreadable: %w", fragFixerID, locked.RowID, err)
		}
		done, err := f.groupRunDone(store, locked.BookIDs, plan.SurvivorID, st, plan.Title, plan.Folder)
		if err != nil {
			return err
		}
		if !done {
			rec, err := planRecordOf(locked, plan, st)
			if err != nil {
				return fmt.Errorf("%s: encode the plan record of %s: %w", fragFixerID, locked.RowID, err)
			}
			if err := w.Journal(plan.SurvivorID, undo.ChangeTypeRepairPlanRecord, fragRecordField(locked.RowID), "", rec); err != nil {
				return err
			}
		}
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
		for _, cp := range plan.Copies {
			if err := ctx.Err(); err != nil {
				return partial(err)
			}
			rows, err := store.GetBookFiles(cp.Frag.Book.ID)
			if err != nil {
				return partial(err)
			}
			if len(rows) != 1 || rows[0].ID != cp.Frag.File.ID {
				return partial(fmt.Errorf("%w: copy %s no longer holds exactly its planned row", repairs.ErrChangedSincePlan, cp.Frag.Book.ID))
			}
			did, err := f.retire(ctx, store, w, cp.Frag.Book.ID, plan.SurvivorID,
				merge.SliceMapping{OffsetSeconds: cp.Offset, Mappable: true})
			steps += did
			if err != nil {
				return partial(err)
			}
		}
		// The book path and title come last, after every retire: a run cut
		// off before them re-plans from untouched survivor facts, and the
		// steps that can refuse (a retire) all run before the survivor
		// changes. replanGroup still accepts a survivor this row already
		// retitled, for a run cut off between these two steps and the
		// recompute.
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
		if err := w.Recompute(plan.SurvivorID); err != nil {
			return partial(fmt.Errorf("recompute %s: %w", plan.SurvivorID, err))
		}
		return nil
	default:
		return fmt.Errorf("%s: row %s carries no plan", fragFixerID, locked.RowID)
	}
}

// copyRetireRefusal is retireInto's refusals for one renamed copy, checked
// before a numbered set writes anything: the copy's book is gone or holds
// another id, it holds anything but exactly its planned row, or it, its row
// or an un-tombstoned external id carries an iTunes id. A copy an earlier,
// cut-off run already retired INTO survivor is not refused (retireInto
// resumes it); one retired into any other book is.
func copyRetireRefusal(store OpsStore, cp fragGroupCopy, survivor string) error {
	id := cp.Frag.Book.ID
	b, err := store.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("read copy %s: %w", id, err)
	}
	if b == nil || b.ID != id {
		return fmt.Errorf("%w: copy %s vanished", repairs.ErrChangedSincePlan, id)
	}
	if b.IsSoftDeleted() {
		// The locked Replan that just ran (same merge lock, nothing written
		// since) attributed every retired book of the row to this fixer's
		// journal; only where it went is re-checked here, not re-scanned.
		if b.MergedIntoBookID == nil || *b.MergedIntoBookID != survivor {
			return fmt.Errorf("%w: copy %s was retired into another book, not the survivor %s", repairs.ErrChangedSincePlan, id, survivor)
		}
		return nil
	}
	if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
		return fmt.Errorf("%w: copy %s now carries an iTunes id", repairs.ErrChangedSincePlan, id)
	}
	rows, err := store.GetBookFiles(id)
	if err != nil {
		return fmt.Errorf("files of copy %s: %w", id, err)
	}
	if len(rows) != 1 || rows[0].ID != cp.Frag.File.ID {
		return fmt.Errorf("%w: copy %s no longer holds exactly its planned row", repairs.ErrChangedSincePlan, id)
	}
	if rows[0].ITunesPersistentID != "" {
		return fmt.Errorf("%w: copy %s row %s now carries an iTunes id", repairs.ErrChangedSincePlan, id, rows[0].ID)
	}
	exts, err := store.GetExternalIDsForBook(id)
	if err != nil {
		return fmt.Errorf("external ids of copy %s: %w", id, err)
	}
	for _, e := range exts {
		if e.Source == "itunes" && !e.Tombstoned {
			return fmt.Errorf("%w: copy %s now carries iTunes id %s", repairs.ErrChangedSincePlan, id, e.ExternalID)
		}
	}
	return nil
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
