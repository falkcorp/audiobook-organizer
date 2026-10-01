// file: internal/metabatch/part_rows.go
// version: 1.0.0
// guid: 0e87a518-a04c-44d3-8d4d-3539bfc91b85
// last-edited: 2026-09-30
//
// Tells a book row that is one file of a set the scanner filed as separate
// book rows ("06 Chapter 6", "Cobra 100 of 151", "The Sunrise Lands 1" beside
// its chapter siblings) from a whole book, so ResolveCandidateSearchQuery can
// skip it instead of searching it -- or a stand-in naming the whole work.

package metabatch

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

var partRowLog = logger.New("metabatch.part-rows")

// minChapterSiblings is how many OTHER rows must back a title whose text is
// also a whole book's ("Cobra 100 of 151" is a chapter; "Dark Age (2 of 3)"
// and "Mistborn 1" beside "Mistborn 2" are books). Two siblings means a set
// of three or more: a two-part dramatization ("Golden Son (Part 1 of 2)") or
// a flat pair of series books never qualifies on count alone, and a longer
// set of whole books is saved by its duration (titleJudge.longRow). A plain
// chapter-number title ("06 Chapter 6", "98") names no book, so one sibling
// is enough for it.
const minChapterSiblings = 2

// FolderMemo shares folder listings across the rows of one pass and knows
// the library and import roots, which are never listed: a row filed
// directly under a root shares its folder with the whole library, and those
// rows are other books. Safe for concurrent use.
type FolderMemo struct {
	mu    sync.Mutex
	roots map[string]bool
	dirs  map[string]folderListing
}

type folderListing struct {
	rows map[string]string
	err  error
}

// NewFolderMemo returns a memo that treats roots (the library root, the
// import paths) as roots. Empty entries are ignored.
func NewFolderMemo(roots ...string) *FolderMemo {
	m := &FolderMemo{roots: map[string]bool{}, dirs: map[string]folderListing{}}
	for _, r := range roots {
		if r = strings.TrimSpace(r); r != "" {
			m.roots[filepath.Clean(r)] = true
		}
	}
	return m
}

func (m *FolderMemo) isRoot(dir string) bool {
	return m != nil && m.roots[filepath.Clean(dir)]
}

// list returns l's listing of dir, read at most once per memo. An error is
// remembered too, so one failing folder is not re-read (and re-logged) per row.
func (m *FolderMemo) list(l database.BookDirLister, dir string) (map[string]string, error) {
	if m == nil {
		return l.LiveBookPathsUnderDir(dir)
	}
	m.mu.Lock()
	if e, ok := m.dirs[dir]; ok {
		m.mu.Unlock()
		return e.rows, e.err
	}
	m.mu.Unlock()
	rows, err := l.LiveBookPathsUnderDir(dir)
	m.mu.Lock()
	m.dirs[dir] = folderListing{rows: rows, err: err}
	m.mu.Unlock()
	return rows, err
}

// listWarn rate-limits the sibling-listing failure warning: a store fault
// repeats for every row of a pass.
var listWarn struct {
	last       atomic.Int64
	suppressed atomic.Int64
}

func warnListFailed(dir string, err error) {
	now := time.Now().UnixNano()
	last := listWarn.last.Load()
	if now-last < int64(time.Minute) || !listWarn.last.CompareAndSwap(last, now) {
		listWarn.suppressed.Add(1)
		return
	}
	partRowLog.Warn("sibling listing failed; judging the title without its folder: dir=%s err=%s suppressed_since_last=%d",
		logger.SanitizeLogValue(dir), logger.SanitizeLogValue(err.Error()), listWarn.suppressed.Swap(0))
}

// partRowRefused reports whether title shows this row to be one file of a
// set the scanner filed as separate book rows (see ResolveCandidateSearchQuery):
//
//   - a chapter number or chapter fragment ("06 Chapter 6", "98",
//     "Elantris_copy179"; metadata.IsChapterOnlyTitle,
//     metadata.IsLikelyChapterFragment) on a part row (isPartRow);
//   - a counted part (metadata.IsCountedPartTitle: "002 of 341") with at
//     least minChapterSiblings sibling rows whose file names are counted
//     parts too (countedSiblings);
//   - a bare trailing part token (metadata.SiblingPartStem: "The Sunrise
//     Lands 1", "Sealed to the Flame E") with at least minChapterSiblings
//     rows whose file names are the same stem and a part token (stemSiblings);
//   - rip details (metadata.StripRipJunk) on a part row, when the title IS
//     the folder's name: a folder name stamped onto each of its files.
//     "American Gods [64k 577MB].m4b" beside "Coraline.m4b" is a book.
//
// The folder is listed only when one of these shapes matches.
func (j *titleJudge) partRowRefused(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	switch {
	case metadata.IsChapterOnlyTitle(t) || metadata.IsLikelyChapterFragment(t):
		return j.isPartRow()
	case metadata.IsCountedPartTitle(t):
		return j.countedSiblings()
	}
	if _, had := metadata.StripRipJunk(t); had {
		dir := j.fileRowDir()
		return dir != "" && normTitle(t) == normTitle(filepath.Base(dir)) && j.isPartRow()
	}
	return j.stemSiblings(t)
}

// ripFolderPartRow reports whether this is a part row whose folder's name
// carries rip details: one file of a folder the scanner split into rows.
// Checked before the transcription fallbacks, which would otherwise search
// the whole work by a mid-book file's intro.
func (j *titleJudge) ripFolderPartRow() bool {
	dir := j.fileRowDir()
	if dir == "" {
		return false
	}
	if _, had := metadata.StripRipJunk(filepath.Base(dir)); !had {
		return false
	}
	return j.isPartRow()
}

// longRow reports whether the row's one file runs at least the
// chapter-consolidation threshold (config.ChapterConsolidationThresholdMin,
// default 10 min): a whole book or a whole part, never a chapter, whatever
// its title says. Book.Duration and BookFile.Duration are seconds; an
// unknown (0) duration is no evidence.
func (j *titleJudge) longRow(present []database.BookFile) bool {
	mins := config.AppConfig.ChapterConsolidationThresholdMin
	if mins <= 0 {
		mins = 10 // config's documented default; the scanner falls back the same way
	}
	limit := mins * 60
	if j.book != nil && j.book.Duration != nil && *j.book.Duration >= limit {
		return true
	}
	return len(present) == 1 && present[0].Duration >= limit
}

// isRootDir reports whether dir is a library or import root, or a generic
// folder (metadata.IsGenericDirName), whose other rows are other books.
func (j *titleJudge) isRootDir(dir string) bool {
	if dir == "." || dir == string(filepath.Separator) || metadata.IsGenericDirName(filepath.Base(dir)) {
		return true
	}
	if root := strings.TrimSpace(config.AppConfig.RootDir); root != "" && filepath.Clean(root) == dir {
		return true
	}
	return j.memo.isRoot(dir)
}

// fileRowDir returns the folder holding this row's one file, or "" when the
// row cannot be a part row: two or more present files (it holds the whole
// work), a directory path, a file running at least the consolidation
// threshold (longRow), or a folder that is a root (isRootDir).
func (j *titleJudge) fileRowDir() string {
	present := j.presentFiles()
	if len(present) > 1 || j.longRow(present) {
		return ""
	}
	p := j.bookPath
	if len(present) == 1 {
		p = present[0].FilePath
	}
	p = strings.TrimSpace(p)
	if p == "" || !metadata.IsFileExt(filepath.Ext(p)) {
		return ""
	}
	dir := filepath.Dir(p)
	if j.isRootDir(dir) {
		return ""
	}
	return dir
}

// siblingPaths returns the FilePath of every OTHER live book row directly in
// this row's folder (fileRowDir), read at most once. Twin rows are not
// siblings: a row at this row's own path or one of its file paths (the
// organized/imported twins prod holds 3-7 of at one path), or one that
// differs from them only by extension ("Fahrenheit 451.m4b" beside
// "Fahrenheit 451.mp3"). A read fault is logged (rate-limited) and is no
// evidence: the title is judged on its own, so a real title is never refused
// on a read fault.
func (j *titleJudge) siblingPaths() []string {
	if j.siblingsLoaded {
		return j.siblings
	}
	j.siblingsLoaded = true
	dir := j.fileRowDir()
	if dir == "" || j.files == nil {
		return nil
	}
	all, err := j.memo.list(j.files, dir)
	if err != nil {
		warnListFailed(dir, err)
		return nil
	}
	own := map[string]bool{}
	for _, p := range append([]string{j.bookPath}, presentPaths(j.presentFiles())...) {
		if p = strings.TrimSpace(p); p != "" {
			own[p] = true
			own[strings.TrimSuffix(p, filepath.Ext(p))] = true
		}
	}
	for id, p := range all {
		if id == j.bookID || filepath.Dir(p) != dir || own[p] || own[strings.TrimSuffix(p, filepath.Ext(p))] {
			continue
		}
		j.siblings = append(j.siblings, p)
	}
	sort.Strings(j.siblings)
	return j.siblings
}

func presentPaths(files []database.BookFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.FilePath)
	}
	return out
}

// isPartRow reports whether this short single-file row shares its folder
// with another live book row that is not its twin.
func (j *titleJudge) isPartRow() bool { return len(j.siblingPaths()) > 0 }

// countedSiblings reports whether at least minChapterSiblings sibling rows'
// file names carry a count themselves ("Cobra 099 of 151.mp3", "Part 01 of
// 63.mp3"): the set this row's "N of M" is a member of. "Wheel of Time #3 of
// 14" filed beside its author's other books has none.
func (j *titleJudge) countedSiblings() bool {
	n := 0
	for _, p := range j.siblingPaths() {
		if metadata.IsCountedPartTitle(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))) {
			n++
		}
	}
	return n >= minChapterSiblings
}

// siblingPartTailRe is what may follow the stem in a sibling's file name: a
// separator and a part token -- digits, optionally sub-numbered ("2",
// "1-02") -- or one letter ("a").
var siblingPartTailRe = regexp.MustCompile(`^[\s_]+(?:\d{1,4}(?:[-_.]\d{1,4})*|\pL)$`)

// leadingTrackRe is a track-number prefix on a file name ("01 - ", "003. ").
var leadingTrackRe = regexp.MustCompile(`^\d{1,4}\s*[-.]?\s+`)

// stemSiblings reports whether title ends in a bare part token
// (metadata.SiblingPartStem) and at least minChapterSiblings sibling rows'
// file names, less extension and any track prefix, are the same stem and a
// part token (siblingPartTailRe). The token may be the same as ours: every
// sibling of "The Sunrise Lands 1" is "NN The Sunrise Lands 1.mp3". "Henry V"
// beside "Henry IV, Part 1", "Malcolm X" beside "Malcolm X Speaks" and "World
// War I" beside "World War II" do not match, and "Mistborn 1" beside one
// "Mistborn 2" is too few.
func (j *titleJudge) stemSiblings(title string) bool {
	stem, _, ok := metadata.SiblingPartStem(title)
	if !ok {
		return false
	}
	want := normTitle(stem)
	n := 0
	for _, p := range j.siblingPaths() {
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		base = normTitle(leadingTrackRe.ReplaceAllString(strings.TrimSpace(base), ""))
		if rest, ok := strings.CutPrefix(base, want); ok && siblingPartTailRe.MatchString(rest) {
			n++
		}
	}
	return n >= minChapterSiblings
}
