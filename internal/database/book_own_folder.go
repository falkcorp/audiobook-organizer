// file: internal/database/book_own_folder.go
// version: 3.3.0
// guid: 83d7e159-50f7-47e5-8303-8d8212c3bd8e
// last-edited: 2026-09-26

package database

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/chaptershape"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// Copies of a book's files.
//
// WHY this exists: a book's book_file rows can point at several copies of the
// same content — the iTunes copy, the organized library copy, chapter files
// left behind by an older layout, and `_copyN` twins that organize minted
// beside an occupant (nextAvailableTargetPath) inside the book's own folder.
// Every consumer that sums the rows (the ABS mapper, the ABS progress/userdata
// duration, RecomputeBookAggregates, zero_rows_only) then counts that content
// once per copy. Measured 2026-09-25: "Awaken Online: Flame" has `_copy1`
// twins and zero-duration rows all inside its own folder; filling its zero
// rows would have taken it to about 26.4 h against a true 21.85 h.
//
// No existing BookFile field can mark the extra copies out of the sum:
//   - Missing is ignored by the ABS mapper (it sums every row), and
//     ComputeBookRuntime drops a missing row only when it is a content
//     duplicate of a present one. It would also be false: the files exist,
//     and the next rescan's stat flips it back.
//   - SkipScan and VersionID are honoured by neither sum; there is no
//     superseded / role / kind field.
//
// Not every row outside the book's own folder is a copy: a merge moves the
// loser's rows to the winner, and they stay in the loser's folder until
// organize runs. They are real content and must count (the owner's decision
// of 2026-09-25, "skip only copies"; TestMoveBookFilesToBookRecomputesBothBooks
// pins it). And not every `_copyN` name is a copy: organize also suffixes a
// DIFFERENT file that collided with an occupant, so prod has distinct chapters
// named "... - 02_copy1.m4a" with their own sizes. Those must count.
//
// So the rule is evidence, not location: rows that are copies of each other
// (IsBookFileCopy) form one cluster, and exactly one row per cluster counts —
// wherever the rows sit (one folder, own folder vs elsewhere, or two folders
// that are both elsewhere). A row that is a copy of no other row counts. The
// row a cluster counts is chosen by keeperLess. The rows are never deleted or
// rewritten — the copies are simply not summed.
//
// One limit, kept from #3552: rows in DIFFERENT directories are matched only
// when the book has a present row in its own folder (HasOwnBasis). Without
// one, Book.FilePath may be the stale one and the rows may be a merge's moved
// rows from several losers, which the owner's "skip only copies" decision says
// must count; so only same-directory copies (a `_copyN` twin beside its
// original) are found there. That evidence does not depend on which folder is
// the book's.

// OwnFolderSplit partitions a book's rows by the book's own folder (the
// directory of Book.FilePath) and marks the copies.
type OwnFolderSplit struct {
	// Dir is the book's own folder, or "" when Book.FilePath gives none.
	Dir string
	// Own are the rows inside Dir (recursively: CD1/, CD2/ subfolders count),
	// copies included.
	Own []BookFile
	// Copies are the rows excluded from the sums: every row of a copy cluster
	// except its keeper, inside or outside Dir.
	Copies []BookFile
	// Others are the rows outside Dir that are not copies. They count.
	Others []BookFile

	// outside is how many rows lie outside Dir, copies included (0 when Dir
	// is "").
	outside int
	// copyIdx are the Copies' indexes in the files slice that was split.
	copyIdx []int
}

// Spans reports whether the rows lie both inside the own folder and outside it.
func (s OwnFolderSplit) Spans() bool {
	return len(s.Own) > 0 && s.outside > 0
}

// HasOutside reports whether any row lies outside the own folder.
func (s OwnFolderSplit) HasOutside() bool {
	return s.outside > 0
}

// HasOwnBasis reports whether at least one own-folder row is present on disk.
// Without one Book.FilePath may be the stale one (book.file_path is known to
// lag the active book_file rows).
func (s OwnFolderSplit) HasOwnBasis() bool {
	for i := range s.Own {
		if !s.Own[i].Missing {
			return true
		}
	}
	return false
}

// Counted returns the rows every sum over this book must use: files without
// the Copies, in their original order. With no copies it returns files
// unchanged.
func (s OwnFolderSplit) Counted(files []BookFile) []BookFile {
	if len(s.Copies) == 0 {
		return files
	}
	drop := make(map[int]bool, len(s.Copies))
	for _, i := range s.copyIdx {
		drop[i] = true
	}
	out := make([]BookFile, 0, len(files)-len(drop))
	for i := range files {
		if !drop[i] {
			out = append(out, files[i])
		}
	}
	return out
}

// SplitOwnFolderFiles partitions files by the book's own folder and marks the
// copies. It keeps the order of files within each part. book may be nil: the
// copies are still found, there is only no own folder to prefer a keeper in.
func SplitOwnFolderFiles(book *Book, files []BookFile) OwnFolderSplit {
	var s OwnFolderSplit
	if book != nil {
		s.Dir = bookOwnFolder(book.FilePath, files)
	}
	inside := make([]bool, len(files))
	for i := range files {
		switch {
		case s.Dir == "":
		case pathutil.IsWithin(files[i].FilePath, s.Dir):
			inside[i] = true
			s.Own = append(s.Own, files[i])
		default:
			s.outside++
		}
	}
	isCopy := markBookFileCopies(files, inside, s.HasOwnBasis())
	for i := range files {
		switch {
		case isCopy[i]:
			s.Copies = append(s.Copies, files[i])
			s.copyIdx = append(s.copyIdx, i)
		case s.Dir != "" && !inside[i]:
			s.Others = append(s.Others, files[i])
		}
	}
	return s
}

// OwnFolderFiles is SplitOwnFolderFiles(book, files).Counted(files): the rows
// a book's duration and size sums must count.
func OwnFolderFiles(book *Book, files []BookFile) []BookFile {
	return SplitOwnFolderFiles(book, files).Counted(files)
}

// markBookFileCopies clusters files by IsBookFileCopy and marks every row of a
// cluster except its keeper. Rows are visited in keeper order and a row is a
// copy when it is a copy of a row already kept, so each cluster keeps its best
// row. Candidates come from two buckets (hash; copy-normalized name + size)
// rather than every pair: the ABS item view runs this for every book it
// renders, and some books hold ~1,500 rows. crossDir false restricts matches
// to rows in the same directory.
func markBookFileCopies(files []BookFile, inside []bool, crossDir bool) []bool {
	isCopy := make([]bool, len(files))
	if len(files) < 2 {
		return isCopy
	}
	order := make([]int, len(files))
	// frozen is computed once here, not in the comparator: the sort compares
	// each row O(log n) times and the predicate allocates.
	frozen := make([]bool, len(files))
	for i := range order {
		order[i] = i
		frozen[i] = pathutil.UnderFrozenITunesTree(files[i].FilePath)
	}
	sort.SliceStable(order, func(a, b int) bool {
		return keeperLess(files, inside, frozen, order[a], order[b])
	})

	type nameSize struct {
		name string
		size int64
	}
	byHash := map[string][]int{}
	byName := map[nameSize][]int{}
	copyOfKept := func(f BookFile, kept []int) bool {
		for _, j := range kept {
			if !crossDir && filepath.Dir(f.FilePath) != filepath.Dir(files[j].FilePath) {
				continue
			}
			if IsBookFileCopy(f, files[j]) {
				return true
			}
		}
		return false
	}
	for _, i := range order {
		f := files[i]
		var names []string
		if f.FileSize > 0 { // an unmeasured size is no name evidence
			names = bookFileNames(f)
		}
		matched := f.FileHash != "" && copyOfKept(f, byHash[f.FileHash])
		for n := 0; !matched && n < len(names); n++ {
			matched = copyOfKept(f, byName[nameSize{names[n], f.FileSize}])
		}
		if matched {
			isCopy[i] = true
			continue
		}
		if f.FileHash != "" {
			byHash[f.FileHash] = append(byHash[f.FileHash], i)
		}
		for _, n := range names {
			k := nameSize{n, f.FileSize}
			byName[k] = append(byName[k], i)
		}
	}
	return isCopy
}

// keeperLess orders rows by how strongly each should be the row its copy
// cluster counts:
//  1. present on disk, so ABS lists and streams a file that exists. A
//     missing keeper would also exclude its present twin for good, since
//     nothing re-admits a copy;
//  2. inside the book's own folder, so ABS lists the library copy and the
//     book's sums are judged on its own rows. This ranks above a measured
//     duration on purpose: an own row that is still unmeasured, beside an
//     out-of-folder twin that is measured (typically the iTunes copy under
//     books/itunes/**), must stay the counted row. Keeping the twin instead
//     made zero_rows_only skip the whole book as "itunes" (a counted row
//     under the frozen tree), left the own zero rows unfilled on every run,
//     and made ABS stream the iTunes file. The book reads short until the
//     duration backfill fills the counted own row;
//  3. outside the frozen iTunes tree (pathutil.UnderFrozenITunesTree), for
//     the same reason as rule 2, in the case rule 2 cannot decide: a cluster
//     with no own-folder row, whose rows all lie elsewhere. Keeping a
//     books/itunes/** row there when a non-iTunes twin exists made
//     zero_rows_only skip the whole book as "itunes" all the same. The frozen
//     rule is the one shared predicate, so this choice and the duration
//     job's iTunes skip can never disagree about which rows are frozen;
//  4. a known duration (> 0), so among present rows on the same side of the
//     own folder and of the iTunes tree, excluding a copy never drops a
//     measured duration. (A present unmeasured row beats a missing measured
//     twin: the book reads short until the next duration backfill fills that
//     counted zero row, rather than listing a dead track.);
//  5. a name without organize's `_copyN` suffix (the original);
//  6. original row order.
//
// frozen[i] is pathutil.UnderFrozenITunesTree(files[i].FilePath), computed
// once by the caller.
func keeperLess(files []BookFile, inside, frozen []bool, a, b int) bool {
	fa, fb := &files[a], &files[b]
	if fa.Missing != fb.Missing {
		return !fa.Missing
	}
	if inside[a] != inside[b] {
		return inside[a]
	}
	if frozen[a] != frozen[b] {
		return !frozen[a]
	}
	if ka, kb := fa.Duration > 0, fb.Duration > 0; ka != kb {
		return ka
	}
	if ca, cb := hasCopySuffix(fa.FilePath), hasCopySuffix(fb.FilePath); ca != cb {
		return !ca
	}
	return a < b
}

// IsBookFileCopy reports whether a is a copy of b. It is a copy when EITHER:
//   - both carry the same non-empty FileHash and their sizes do not
//     conflict; OR
//   - they share a file name, have the same known FileSize (> 0: an
//     unmeasured size is no evidence), and their durations agree.
//
// A shared hash is not enough on its own: legacy rows carry
// scanner.ComputeSegmentFileHash, a SHA-256 of the first 1 MB only, so
// distinct tracks that share an opening (identical album-only tags and a large
// embedded cover) share a hash. Such tracks almost always differ in size, so
// sizes that are both known (> 0) and differ veto the hash. An unknown size
// does not veto: some hash writers (SetBookFileHash, extract-wav-clips) set
// the hash without the size. The canonical chunked hash digests the size, so
// for current rows the veto never fires on a true copy.
//
// Durations deliberately do NOT veto a hash match: the two rows' durations
// can come from different measurements (iTunes' Total Time on the iTunes
// twin, the fingerprint or ffprobe duration the backfill writes on the own
// row), which drift by more than 1 s on VBR files. A duration veto would
// split a byte-identical pair and count that track twice.
//
// Names are a non-empty OriginalFilename or the base name of FilePath,
// compared across both, with organize's `_copyN` collision suffix removed from
// the stem ("01_copy1.m4a" is named "01.m4a"). A `_copyN` file of a DIFFERENT
// size is still not a copy: organize suffixes distinct files too.
//
// Durations agree when both are known (> 0) and within 1 s, or both are
// unknown. One known and one unknown agree only when both rows sit in the
// same directory — where organize puts a `_copyN` twin — so across folders a
// zero-duration row is never called a copy of a measured one on name and size
// alone.
//
// Name evidence never counts between two rows in different "<Book> - N"
// chapter folders of one book folder (internal/chaptershape): that layout
// gives every chapter the same file name (`The Shining - 3/58.MP3`), so there
// the shared name is the layout, not a copy. A shared hash still counts.
func IsBookFileCopy(a, b BookFile) bool {
	if a.FileHash != "" && a.FileHash == b.FileHash {
		return !knownSizesConflict(a, b)
	}
	if a.FileSize <= 0 || a.FileSize != b.FileSize {
		return false
	}
	sameDir := a.FilePath != "" && filepath.Dir(a.FilePath) == filepath.Dir(b.FilePath)
	if knownDurationsConflict(a, b) {
		return false
	}
	if (a.Duration > 0) != (b.Duration > 0) && !sameDir {
		return false
	}
	if !sameDir && chapterFolderSiblings(a.FilePath, b.FilePath) {
		return false
	}
	for _, x := range bookFileNames(a) {
		for _, y := range bookFileNames(b) {
			if x == y {
				return true
			}
		}
	}
	return false
}

// knownSizesConflict reports whether both rows carry a known size (> 0) and
// the sizes differ.
func knownSizesConflict(a, b BookFile) bool {
	return a.FileSize > 0 && b.FileSize > 0 && a.FileSize != b.FileSize
}

// knownDurationsConflict reports whether both rows carry a known duration
// (> 0) more than 1 s apart.
func knownDurationsConflict(a, b BookFile) bool {
	if a.Duration <= 0 || b.Duration <= 0 {
		return false
	}
	d := a.Duration - b.Duration
	return d > 1 || d < -1
}

// copySuffixRe matches organize's collision suffix at the end of a file stem,
// "<stem>_copy<N>" (nextAvailableTargetPath in internal/organizer).
var copySuffixRe = regexp.MustCompile(`_copy\d+$`)

// stripCopySuffix removes a `_copyN` suffix from name's stem. A stem that is
// nothing but the suffix is left alone.
func stripCopySuffix(name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if loc := copySuffixRe.FindStringIndex(stem); loc != nil && loc[0] > 0 {
		return stem[:loc[0]] + ext
	}
	return name
}

// hasCopySuffix reports whether path's file name carries a `_copyN` suffix.
func hasCopySuffix(path string) bool {
	if path == "" {
		return false
	}
	base := filepath.Base(path)
	return stripCopySuffix(base) != base
}

// chapterFolderSiblings reports whether a and b sit in chapter folders
// ("<prefix> - N") of the same book folder with the same prefix.
func chapterFolderSiblings(a, b string) bool {
	pa, xa, ok := chaptershape.IsChapterFolderFile(a)
	if !ok {
		return false
	}
	pb, xb, ok := chaptershape.IsChapterFolderFile(b)
	return ok && pa == pb && chaptershape.NormPrefix(xa) == chaptershape.NormPrefix(xb)
}

// bookFileNames are the distinct non-empty names a row is known by, with the
// `_copyN` suffix removed.
func bookFileNames(f BookFile) []string {
	var out []string
	if f.OriginalFilename != "" {
		out = append(out, stripCopySuffix(f.OriginalFilename))
	}
	if f.FilePath != "" {
		if n := stripCopySuffix(filepath.Base(f.FilePath)); len(out) == 0 || out[0] != n {
			out = append(out, n)
		}
	}
	return out
}

// bookOwnFolder derives the book's own folder from its library path.
// FilePath is a FILE (so its directory is the folder) only when it is one of
// the rows' paths or carries an audio extension; otherwise it is the folder
// itself. Deciding "directory" from "some row lies under it" would be wrong
// the other way: with no row under it, Dir(FilePath) climbs to the author
// folder, where sibling copies would then count as own.
func bookOwnFolder(p string, files []BookFile) string {
	if p == "" {
		return ""
	}
	isFile := ownFolderAudioExt[strings.ToLower(filepath.Ext(p))]
	for i := 0; !isFile && i < len(files); i++ {
		isFile = files[i].FilePath == p
	}
	dir := filepath.Clean(p)
	if isFile {
		dir = filepath.Dir(dir)
	}
	// A root or relative "." would contain every row: no own folder.
	if dir == "/" || dir == "." || dir == string(filepath.Separator) {
		return ""
	}
	return dir
}

// ownFolderAudioExt are the extensions that mark Book.FilePath as a file.
var ownFolderAudioExt = map[string]bool{
	".m4b": true, ".m4a": true, ".mp3": true, ".mp4": true, ".aac": true,
	".flac": true, ".ogg": true, ".oga": true, ".opus": true, ".wma": true,
	".wav": true, ".aax": true, ".aa": true, ".aif": true, ".aiff": true,
}
