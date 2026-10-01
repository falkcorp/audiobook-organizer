// file: internal/metabatch/part_rows.go
// version: 1.2.1
// guid: 0e87a518-a04c-44d3-8d4d-3539bfc91b85
// last-edited: 2026-10-01
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
	"unicode"
	"unicode/utf8"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

var partRowLog = logger.New("metabatch.part-rows")

// Thresholds for the two title shapes whose TEXT is also a whole book's: a
// counted part ("Cobra 100 of 151" / "Golden Son (Part 1 of 2)") and a bare
// trailing token ("The Sunrise Lands 1" / "The Primal Hunter 4"). Measured on
// prod 2026-10-01 over all 100,813 book rows:
//
//   - counted-part rows (5,674): 5,132 run under 1 h (4,996 under 15 min),
//     28 run 1-2 h (file-split parts: "Annie on My Mind 3 of 6" 1.15 h,
//     "Dolphin Island (2 of 4)" 1.48 h), ONE runs 2-3 h ("Super Powereds:
//     Year Two (Part 2 of 3) (Dramatized Adaptation)" 2.16 h, a sold part),
//     and 174 run 3 h or more (the dramatized products, 11-25 h). The gap
//     sits between 1.48 h and 2.16 h, so productMinSec is 2 h.
//   - trailing-token rows in sets of 3-5 rows run a median 6.7 h (whole
//     series books: "The Primal Hunter 1-4", "Mage Academy 1-3"); sets of 10
//     or more run p95 0.5 h (chapters). The same 2 h line splits them.
//   - 340 counted and 2,980 trailing-token rows have no duration (and no
//     file size), so for those the set's size decides: chapter sets run
//     10-341 rows, product and series sets 2-5 (unknownDurationMinSiblings).
const (
	// productMinSec: a row's one file running at least this long is a sold
	// product or whole book, never a chapter, whatever its title says.
	productMinSec = 2 * 3600
	// minChapterSiblings: with a known short duration, two same-set siblings
	// (a set of three or more) make a counted or trailing-token title a part.
	minChapterSiblings = 2
	// unknownDurationMinSiblings: with no trustworthy duration, five same-set
	// siblings (a set of six or more) are needed.
	unknownDurationMinSiblings = 5
	// maxPlausibleSec: a duration past a week is a unit error, not a book.
	maxPlausibleSec = 7 * 24 * 3600
)

// FolderMemo shares folder listings across the rows of one pass. Each
// folder is read once, and only its direct children are kept. A read error
// is remembered too, so one failing folder is neither re-read nor re-logged
// per row of the pass. Safe for concurrent use.
type FolderMemo struct {
	mu   sync.Mutex
	dirs map[string]folderListing
}

type folderListing struct {
	rows map[string]string
	err  error
}

// NewFolderMemo returns an empty memo for one pass.
func NewFolderMemo() *FolderMemo {
	return &FolderMemo{dirs: map[string]folderListing{}}
}

// listDirectChildren is l's listing of dir (recursive) cut down to the rows
// directly in dir.
func listDirectChildren(l database.BookDirLister, dir string) (map[string]string, error) {
	all, err := l.LiveBookPathsUnderDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(all))
	for id, p := range all {
		if filepath.Dir(p) == dir {
			out[id] = p
		}
	}
	return out, nil
}

// list returns the direct children of dir, read at most once per memo.
func (m *FolderMemo) list(l database.BookDirLister, dir string) (map[string]string, error) {
	if m == nil {
		return listDirectChildren(l, dir)
	}
	m.mu.Lock()
	if e, ok := m.dirs[dir]; ok {
		m.mu.Unlock()
		return e.rows, e.err
	}
	m.mu.Unlock()
	rows, err := listDirectChildren(l, dir)
	m.mu.Lock()
	m.dirs[dir] = folderListing{rows: rows, err: err}
	m.mu.Unlock()
	return rows, err
}

// importRoots is the shared source of the import-path roots every resolver
// call consults (isRootDir), so the fetch paths and the apply/gate paths
// reach the same verdict for the same row. The server registers the source
// once (SetImportRootsSource); the list is re-read at most once a minute.
var importRoots struct {
	mu     sync.Mutex
	source func() ([]string, error)
	gen    uint64 // bumped by SetImportRootsSource; a stale refresh is dropped
	roots  map[string]bool
	at     time.Time
	// ready is closed when this generation's FIRST load finishes (success or
	// error). Callers that arrive while it is in flight wait on it instead of
	// reading the still-nil list, which would answer "not a root" for every
	// import path -- the candidate op starts 16-32 workers at once, so the
	// first pass after start-up would otherwise list whole import roots.
	ready chan struct{}
}

const importRootsTTL = time.Minute

// importRootsFirstLoadWait bounds how long a caller waits for a generation's
// first load; the load is one store read, normally milliseconds.
var importRootsFirstLoadWait = 2 * time.Second

// SetImportRootsSource registers where the import paths come from (the
// store's GetAllImportPaths) and drops the cached list. nil unregisters.
func SetImportRootsSource(source func() ([]string, error)) {
	importRoots.mu.Lock()
	defer importRoots.mu.Unlock()
	importRoots.source = source
	importRoots.gen++
	importRoots.roots = nil
	importRoots.at = time.Time{}
	// Release anyone waiting on the replaced generation's first load.
	if importRoots.ready != nil {
		close(importRoots.ready)
		importRoots.ready = nil
	}
	if source != nil {
		importRoots.ready = make(chan struct{})
	}
}

// isImportRoot reports whether dir is a registered import path. The source
// is called outside the lock (it is a store read), by one caller per TTL
// window; the others use the list on hand. A read error is logged at Warn
// and keeps the previous list.
func isImportRoot(dir string) bool {
	importRoots.mu.Lock()
	source, gen, ready := importRoots.source, importRoots.gen, importRoots.ready
	refresh := source != nil && time.Since(importRoots.at) >= importRootsTTL
	if refresh {
		importRoots.at = time.Now() // claim this window's refresh
	}
	roots := importRoots.roots
	importRoots.mu.Unlock()
	if !refresh {
		if roots == nil && ready != nil {
			// The first load of this generation is in flight: wait for it,
			// bounded so a source that re-enters the registry (or a stuck
			// store read) degrades to "not a root" instead of hanging.
			select {
			case <-ready:
			case <-time.After(importRootsFirstLoadWait):
			}
			importRoots.mu.Lock()
			roots = importRoots.roots
			importRoots.mu.Unlock()
		}
		return roots[dir]
	}
	paths, err := source()
	var fresh map[string]bool
	if err == nil {
		fresh = make(map[string]bool, len(paths))
		for _, p := range paths {
			if p = strings.TrimSpace(p); p != "" {
				fresh[filepath.Clean(p)] = true
			}
		}
	}
	importRoots.mu.Lock()
	if importRoots.gen == gen {
		if err == nil {
			importRoots.roots = fresh
		}
		// First load of this generation done (even on error): release waiters.
		if importRoots.ready != nil && importRoots.ready == ready {
			close(importRoots.ready)
			importRoots.ready = nil
		}
	}
	importRoots.mu.Unlock()
	if err != nil {
		partRowLog.Warn("import paths unreadable; keeping the previous root list: err=%s",
			logger.SanitizeLogValue(err.Error()))
		return roots[dir]
	}
	return fresh[dir]
}

// listWarn rate-limits the sibling-listing failure warning: a store fault
// repeats for every folder of a pass.
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
//     metadata.IsLikelyChapterFragment) on a part row (isPartRow), whatever
//     the file's duration -- a file-split part runs 10-60 min;
//   - an empty or placeholder title ("", "Unknown Title", "Unknown",
//     "Untitled"; isPlaceholderTitle) on a part row
//     whose FILE is named by a chapter number ("Eldest/98.mp3");
//   - a counted part (metadata.IsCountedPartTitle: "002 of 341") or a bare
//     trailing part token (metadata.SiblingPartStem: "The Sunrise Lands 1")
//     with enough same-set siblings (countedSiblings, stemSiblings) on a row
//     that is not a whole product by duration (textShapePart);
//   - rip details (metadata.StripRipJunk) on a part row (ripShapePart), when
//     the title IS the folder's name: a folder name stamped onto each of its
//     files.
//     "American Gods [64k 577MB].m4b" beside "Coraline.m4b" is a book.
//
// The folder is listed only when one of these shapes matches.
func (j *titleJudge) partRowRefused(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" || isPlaceholderTitle(t) {
		name := j.fileBaseName()
		if name == "" || !(metadata.IsChapterOnlyTitle(name) || metadata.IsLikelyChapterFragment(name)) {
			return false
		}
		return j.isPartRow()
	}
	switch {
	case metadata.IsChapterOnlyTitle(t) || metadata.IsLikelyChapterFragment(t):
		return j.isPartRow()
	case metadata.IsCountedPartTitle(t):
		return j.textShapePart(func() int { return j.countedSiblings(t) })
	}
	if _, had := metadata.StripRipJunk(t); had {
		dir := j.fileRowDir()
		return dir != "" && normTitle(t) == normTitle(filepath.Base(dir)) && j.ripShapePart()
	}
	if _, _, ok := metadata.SiblingPartStem(t); ok {
		return j.textShapePart(func() int { return j.stemSiblings(t) })
	}
	return false
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
	return j.ripShapePart()
}

// ripShapePart decides a row of a rip-details folder by duration first,
// as textShapePart does: a file of productMinSec or more is a whole book
// (a "Harry Potter 1-7 [64k]" box set's 10 h files), a shorter one is a
// part beside any sibling, and with no trustworthy duration the folder must
// hold unknownDurationMinSiblings siblings.
func (j *titleJudge) ripShapePart() bool {
	return j.durationGatedPart(1, func() int { return len(j.siblingPaths()) })
}

// textShapePart decides a counted or trailing-token title from its same-set
// sibling count: with a trustworthy duration, a row under productMinSec with
// minChapterSiblings siblings is a part and one at or over it never is; with
// none, unknownDurationMinSiblings siblings are needed (see the threshold
// notes above).
func (j *titleJudge) textShapePart(siblings func() int) bool {
	return j.durationGatedPart(minChapterSiblings, siblings)
}

// durationGatedPart is the duration-first rule textShapePart and
// ripShapePart share: at or over productMinSec never a part; under it, a
// part with minKnown siblings; with no trustworthy duration, a part with
// unknownDurationMinSiblings. The duration is checked first, so a whole
// product never lists its folder; siblings is called only when the count
// decides.
func (j *titleJudge) durationGatedPart(minKnown int, siblings func() int) bool {
	d := j.rowDurationSec()
	switch {
	case d >= productMinSec:
		return false
	case d > 0:
		return siblings() >= minKnown
	default:
		return siblings() >= unknownDurationMinSiblings
	}
}

// rowDurationSec returns the row's trustworthy duration in seconds, 0 when
// unknown. The single present file's own duration wins; when the file has
// one, the book's is never consulted, so a file value rejected as
// milliseconds cannot be replaced by a book copy of the same bad value. A
// value is judged against a file size (the file's, else the book's):
// database.DurationLooksLikeMillis rejects one too long for its bytes. With
// no size at all, a value under productMinSec is kept (read as seconds or
// as milliseconds, the file is short), while one of productMinSec or more is
// unknown: a 2 h-7 d reading is exactly what a 7-604 s chapter's duration
// stored in milliseconds looks like. Past maxPlausibleSec is always unknown.
func (j *titleJudge) rowDurationSec() int {
	var bookSize int64
	if b := j.book; b != nil && b.FileSize != nil {
		bookSize = *b.FileSize
	}
	if present := j.presentFiles(); len(present) == 1 && present[0].Duration > 0 {
		size := present[0].FileSize
		if size <= 0 {
			size = bookSize
		}
		return trustedDurationSec(present[0].Duration, size)
	}
	if b := j.book; b != nil && b.Duration != nil {
		return trustedDurationSec(*b.Duration, bookSize)
	}
	return 0
}

// trustedDurationSec is d when it reads as seconds (see rowDurationSec),
// else 0.
func trustedDurationSec(d int, size int64) int {
	switch {
	case d <= 0 || d >= maxPlausibleSec:
		return 0
	case size > 0:
		if database.DurationLooksLikeMillis(size, d) {
			return 0
		}
		return d
	case d < productMinSec:
		return d
	default:
		return 0
	}
}

// isRootDir reports whether dir is the library root (config RootDir), a
// registered import path (isImportRoot), or a generic folder
// (metadata.IsGenericDirName): its other rows are other books, and listing it
// would read the whole library.
func (j *titleJudge) isRootDir(dir string) bool {
	if dir == "." || dir == string(filepath.Separator) || metadata.IsGenericDirName(filepath.Base(dir)) {
		return true
	}
	if root := strings.TrimSpace(config.AppConfig.RootDir); root != "" && filepath.Clean(root) == dir {
		return true
	}
	return isImportRoot(dir)
}

// rowFilePath returns the path of the row's one file, or "" when the row
// has two or more present files (it holds the whole work) or no file path.
func (j *titleJudge) rowFilePath() string {
	present := j.presentFiles()
	if len(present) > 1 {
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
	return p
}

// fileBaseName is the row's one file's name without its extension, or "".
func (j *titleJudge) fileBaseName() string {
	p := j.rowFilePath()
	if p == "" {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
}

// fileRowDir returns the folder holding this row's one file, or "" when the
// row cannot be a part row: two or more present files, a directory path, or
// a folder that is a root (isRootDir).
func (j *titleJudge) fileRowDir() string {
	p := j.rowFilePath()
	if p == "" {
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
	rows, err := j.memo.list(j.files, dir)
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
	for id, p := range rows {
		if id == j.bookID || own[p] || own[strings.TrimSuffix(p, filepath.Ext(p))] {
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

// isPartRow reports whether this single-file row shares its folder with
// another live book row that is not its twin.
func (j *titleJudge) isPartRow() bool { return len(j.siblingPaths()) > 0 }

// countedSiblings counts the sibling rows whose file names are parts of the
// SAME counted set as this row (metadata.CountedPartKey): the same count,
// and stems that match (sameCountedStem). This row's key comes from its own
// FILE name, like its siblings', so a tag title that differs from the file
// names ("Before They Are Hanged 002 of 341" on "Joe Abercrombie - Before
// They Are Hanged 002 of 341.mp3", "Cobra (Unabridged) 100 of 151" on
// "Cobra 100 of 151.mp3") still finds its set; the title is the key only
// when the file name is not counted. "Red Rising (Part 1 of 2)" is not a
// sibling of "Golden Son (Part 1 of 2)".
func (j *titleJudge) countedSiblings(title string) int {
	stem, count, ok := metadata.CountedPartKey(countedFileName(j.fileBaseName()))
	if !ok {
		if stem, count, ok = metadata.CountedPartKey(title); !ok {
			return 0
		}
	}
	n := 0
	for _, p := range j.siblingPaths() {
		s, c, ok := metadata.CountedPartKey(countedFileName(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))))
		if ok && c == count && sameCountedStem(s, stem) {
			n++
		}
	}
	return n
}

// countedFileName is a file's base name (no extension) less any track
// prefix ("01 - ").
func countedFileName(base string) string {
	return leadingTrackRe.ReplaceAllString(strings.TrimSpace(base), "")
}

// sameCountedStem reports whether two counted-part stems name one set:
// equal, either empty ("Part 01 of 63.mp3" names nothing else), or one
// ending in the other at a word boundary ("timothy zahn - cobra" and
// "cobra": an author prefix on some files).
func sameCountedStem(a, b string) bool {
	if a == b || a == "" || b == "" {
		return true
	}
	if len(a) < len(b) {
		a, b = b, a
	}
	rest, ok := strings.CutSuffix(a, b)
	return ok && !unicode.IsLetter(lastRune(rest)) && !unicode.IsDigit(lastRune(rest))
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// isPlaceholderTitle is a title that names nothing: the shared placeholder
// set (authorname.IsPlaceholderTitle: "Unknown Title", ...) plus the bare
// "Unknown" and "Untitled" taggers write. Local to the part-row rule; the
// shared set is not widened.
func isPlaceholderTitle(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "unknown", "untitled":
		return true
	}
	return authorname.IsPlaceholderTitle(t)
}

// siblingPartTailRe is what may follow the stem in a sibling's file name: a
// separator and a part token -- digits, optionally sub-numbered ("2",
// "1-02") -- or one letter ("a").
var siblingPartTailRe = regexp.MustCompile(`^[\s_]+(?:\d{1,4}(?:[-_.]\d{1,4})*|\pL)$`)

// leadingTrackRe is a track-number prefix on a file name ("01 - ", "003. ").
var leadingTrackRe = regexp.MustCompile(`^\d{1,4}\s*[-.]?\s+`)

// stemSiblings counts the sibling rows whose file names, less extension and
// any track prefix, are title's stem (metadata.SiblingPartStem) and a part
// token (siblingPartTailRe). The token may be the same as ours: every
// sibling of "The Sunrise Lands 1" is "NN The Sunrise Lands 1.mp3". "Henry
// V" beside "Henry IV, Part 1", "Malcolm X" beside "Malcolm X Speaks" and
// "World War I" beside "World War II" do not count.
func (j *titleJudge) stemSiblings(title string) int {
	stem, _, ok := metadata.SiblingPartStem(title)
	if !ok {
		return 0
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
	return n
}
