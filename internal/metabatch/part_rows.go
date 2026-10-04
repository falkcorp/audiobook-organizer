// file: internal/metabatch/part_rows.go
// version: 1.6.2
// guid: 0e87a518-a04c-44d3-8d4d-3539bfc91b85
// last-edited: 2026-10-01
//
// Tells a book row that is one file of a set the scanner filed as separate
// book rows ("06 Chapter 6", "Cobra 100 of 151", "The Sunrise Lands 1" beside
// its chapter siblings) from a whole book, so ResolveCandidateSearchQuery can
// skip it instead of searching it -- or a stand-in naming the whole work.
// The set is found in the row's own folder (siblingPaths) or, when each file
// sits alone in a folder named for it ("Great Sky River 18 6/Great Sky
// River 18 6.mp3"), in the folders beside that one (cousinPaths).

package metabatch

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	"golang.org/x/sync/singleflight"
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

// Thresholds for COUSIN evidence (cousinPaths: a file alone in a folder named
// for it, beside other such folders). One book per folder is also how whole
// books are shelved, so cousins are weaker evidence than rows sharing one
// folder. Measured on prod 2026-10-01 over the 15,648 unmatched rows: 98%
// have a stored duration and a size; the fragments run a median 4 min, p75
// 10 min, p90 29 min; a 30 min ceiling keeps 10,371 of the 11,495 rows under
// 2 h. Whole series books ("Mistborn 1-7", "Magic Tree House 1-3", "Wheel of
// Time 01-14", "Discworld 01-41") are 1 h or more, or have no trusted
// duration. Accepted trade-off: a collection of 11 or more short items
// (under 30 min each) filed one per folder with a single trailing number
// ("Mr Men 1-40" at 8 min, "Short Trips N of 12" at 25 min, numbered
// Big Finish releases) looks exactly like a chapter set from file names and
// durations, and is skipped by the automatic fetch. A per-book search and a
// per-row apply still work on it.
const (
	// cousinMaxPartSec: cousins count only for a row whose trusted duration
	// is under this; an unknown duration or one of 30 min-2 h never counts
	// them.
	cousinMaxPartSec = 30 * 60
	// cousinBigSet: a set signal a whole-book series lacks. A single-token
	// trailing title ("Stem 3") or a counted title of fewer than this many
	// parts ("N of 7") needs this many matching cousins; a sub-numbered
	// token ("Great Sky River 18 6") or a count of at least this many
	// ("123 of 180") needs only minChapterSiblings.
	cousinBigSet = 10
	// cousinParentMaxRows: a cousin parent holding more live rows than this
	// is a shelf, not a work's folder; its cousins are unavailable.
	cousinParentMaxRows = 2000
)

// cousinParentContainerNames are folder names that hold many works, beyond
// metadata.IsGenericDirName's set (which already has "audiobooks", "books",
// "library", ...) and authorname.IsGenreFolder's genre shelves: never a
// cousin parent.
var cousinParentContainerNames = map[string]bool{
	"authors": true, "itunes media": true, "audible": true, "libation": true,
}

// errCousinParentTooLarge: the parent's listing exceeded cousinParentMaxRows.
type errCousinParentTooLarge struct{ rows int }

func (e errCousinParentTooLarge) Error() string {
	return fmt.Sprintf("folder holds %d live rows, over the cousin cap of %d", e.rows, cousinParentMaxRows)
}

// FolderMemo shares folder listings across the rows of one pass. Each
// folder is read once per kind of listing: its direct children (list), and,
// as the parent of folder-wrapped rows, the wrapped rows two levels down
// (listWrapped). Concurrent callers for the same folder and kind share ONE
// in-flight read (singleflight), so the 16-32 workers of a fetch pass that
// reach one author folder together list it once, not once each. A failed
// read is logged (warnListFailed, rate-limited) only by the read that hit
// it and shared with the callers already waiting on it, but it is not kept:
// a later row reads the folder again, so one transient store fault does not
// cost the folder's evidence for the rest of a run that can last hours.
// Only the cousin size cap, a property of the folder, is kept like a
// listing. Safe for concurrent use.
type FolderMemo struct {
	mu       sync.Mutex
	dirs     map[memoKey]folderListing
	inflight singleflight.Group

	// rootsMu guards the import-root set (importRoots). Held across the load
	// on purpose, so concurrent first callers wait for it instead of reading
	// an empty set.
	rootsMu sync.Mutex
	roots   map[string]bool
	rootsAt time.Time
}

// memoKey keys one listing: a folder's direct children, or (wrapped) the
// folder-wrapped rows two levels under it. The two never share an entry: an
// author folder can hold loose rows of its own and wrapped book folders.
type memoKey struct {
	dir     string
	wrapped bool
}

type folderListing struct {
	rows map[string]string
	err  error
}

// NewFolderMemo returns an empty memo for one pass.
func NewFolderMemo() *FolderMemo {
	return &FolderMemo{dirs: map[memoKey]folderListing{}}
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

// listWrappedGrandchildren is l's listing of parent (recursive) cut down to
// the folder-wrapped rows exactly two levels under it: parent/<X>/<file>
// where <X> is named like <file> (isWrappedFile). A parent holding more than
// cousinParentMaxRows rows is refused (errCousinParentTooLarge).
func listWrappedGrandchildren(l database.BookDirLister, parent string) (map[string]string, error) {
	all, err := l.LiveBookPathsUnderDir(parent)
	if err != nil {
		return nil, err
	}
	if len(all) > cousinParentMaxRows {
		return nil, errCousinParentTooLarge{rows: len(all)}
	}
	out := map[string]string{}
	for id, p := range all {
		if dir := filepath.Dir(p); filepath.Dir(dir) == parent && isWrappedFile(dir, p) {
			out[id] = p
		}
	}
	return out, nil
}

// list returns the direct children of dir, read at most once per memo.
func (m *FolderMemo) list(l database.BookDirLister, dir string) (map[string]string, error) {
	return m.cached(memoKey{dir: dir}, func() (map[string]string, error) { return listDirectChildren(l, dir) })
}

// listWrapped returns the folder-wrapped rows two levels under parent
// (listWrappedGrandchildren), read at most once per memo.
func (m *FolderMemo) listWrapped(l database.BookDirLister, parent string) (map[string]string, error) {
	return m.cached(memoKey{dir: parent, wrapped: true}, func() (map[string]string, error) {
		return listWrappedGrandchildren(l, parent)
	})
}

// cached returns key's listing, calling read only when the memo has none;
// concurrent misses on one key wait for a single read. The result is stored
// before the in-flight entry is released, so a caller arriving after the
// read finished finds it in dirs rather than starting a second read. A
// failed read is logged here, once per actual read (rate-limited by
// warnListFailed), never by the callers that share or reuse it. A nil memo
// always reads (and logs a failure).
func (m *FolderMemo) cached(key memoKey, read func() (map[string]string, error)) (map[string]string, error) {
	readLogged := func() (map[string]string, error) {
		rows, err := read()
		var tooLarge errCousinParentTooLarge
		switch {
		case errors.As(err, &tooLarge):
			cousinCapWarn.warn("cousin parent too large; judging the row without cousins: dir=%s rows=%d cap=%d suppressed_since_last=%d",
				logger.SanitizeLogValue(key.dir), tooLarge.rows, cousinParentMaxRows)
		case err != nil:
			warnListFailed(key.dir, err)
		}
		return rows, err
	}
	if m == nil {
		return readLogged()
	}
	if e, ok := m.lookup(key); ok {
		return e.rows, e.err
	}
	flightKey := "d:" + key.dir
	if key.wrapped {
		flightKey = "w:" + key.dir
	}
	// read must never call back into this memo on the same key:
	// singleflight does not detect re-entry and would wait on itself.
	v, err, _ := m.inflight.Do(flightKey, func() (any, error) {
		// A read that finished between our lookup and Do already stored it.
		if e, ok := m.lookup(key); ok {
			return e, nil
		}
		rows, err := readLogged()
		e := folderListing{rows: rows, err: err}
		// Only a successful listing or the size cap (a property of the
		// folder, not a fault) is kept for the pass. A transient read
		// fault is shared with the callers already waiting on this read,
		// but a later row reads again rather than going without folder
		// evidence for the rest of a run that can last hours.
		var tooLarge errCousinParentTooLarge
		if err == nil || errors.As(err, &tooLarge) {
			m.mu.Lock()
			m.dirs[key] = e
			m.mu.Unlock()
		}
		return e, nil
	})
	if err != nil {
		return nil, err
	}
	e := v.(folderListing)
	return e.rows, e.err
}

// lookup returns key's stored listing, if any.
func (m *FolderMemo) lookup(key memoKey) (folderListing, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.dirs[key]
	return e, ok
}

// isWrappedFile reports whether the file at p sits in a folder (dir) named
// exactly like it: its base name less extension, or less a track prefix too
// ("Great Sky River 18 6/Great Sky River 18 6.mp3"). Only a file path
// (metadata.IsFileExt) qualifies; a multi-file book's row path is a folder.
func isWrappedFile(dir, p string) bool {
	if !metadata.IsFileExt(filepath.Ext(p)) {
		return false
	}
	folder := normTitle(filepath.Base(dir))
	if folder == "" {
		return false
	}
	base := strings.TrimSpace(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
	return folder == normTitle(base) || folder == normTitle(countedFileName(base))
}

// importRootsTTL bounds how stale a FolderMemo's import-root list may get
// over a long pass (a fetch op can run for hours): the list is re-read at
// most once per window.
const importRootsTTL = time.Minute

// ImportPathReader is the one read the resolver needs for import roots: a
// folder that is a registered import path is never listed for sibling rows,
// on every path -- fetch, stale scan, apply and gate alike -- because its
// other rows are other books and listing it would read a whole import tree.
//
// The roots come from the SAME store the caller hands the resolver, never
// from a process-wide registration. Until 2026-10-04 they came from a
// package global the server set in NewServer (SetImportRootsSource); the
// global outlived the store it closed over, so a later caller resolved
// through a closed Pebble store and panicked "pebble: closed" (the
// TestApplyCachedCandidate_GateRefuses flake). Reading from the caller's own
// store makes that impossible by construction, and two stores in one
// process can no longer see each other's roots.
type ImportPathReader interface {
	GetAllImportPaths() ([]database.ImportPath, error)
}

// importRootLog rate-limits the Warn for an unreadable import-path list.
var importRootLog warnLimiter

// readImportRoots loads the cleaned import-path set from r. ok is false when
// the read failed; the failure is logged here at Warn (rate-limited), and the
// caller keeps its previous list rather than replacing it with nothing.
func readImportRoots(r ImportPathReader) (roots map[string]bool, ok bool) {
	if r == nil {
		return nil, true
	}
	paths, err := r.GetAllImportPaths()
	if err != nil {
		importRootLog.warn("import paths unreadable; keeping the previous root list (none on a first read): err=%s suppressed_since_last=%d",
			logger.SanitizeLogValue(err.Error()))
		return nil, false
	}
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		if c := strings.TrimSpace(p.Path); c != "" {
			out[filepath.Clean(c)] = true
		}
	}
	return out, true
}

// importRoots returns the memo's import-root set, read from r at most once
// per importRootsTTL. Concurrent callers wait on the one in-flight load
// rather than reading a still-empty list -- the candidate op starts 16-32
// workers at once, and a worker answering "not a root" during the first
// load would list a whole import root. A failed read keeps the previous list
// (none, on a first read) and is retried after one TTL window, not by every
// row: the read runs under the lock, so retrying a failing store per row
// would serialize every worker of the pass behind it.
func (m *FolderMemo) importRoots(r ImportPathReader) map[string]bool {
	m.rootsMu.Lock()
	defer m.rootsMu.Unlock()
	if !m.rootsAt.IsZero() && time.Since(m.rootsAt) < importRootsTTL {
		return m.roots
	}
	m.rootsAt = time.Now() // this window's read, success or failure
	if fresh, ok := readImportRoots(r); ok {
		m.roots = fresh
	}
	return m.roots
}

// warnLimiter logs a Warn at most once a minute and counts the rest; the
// suppressed count rides on the next logged line (its last format verb).
type warnLimiter struct {
	last       atomic.Int64
	suppressed atomic.Int64
	// failures counts every report (logged or suppressed).
	failures atomic.Int64
}

func (w *warnLimiter) warn(format string, args ...any) {
	w.failures.Add(1)
	now := time.Now().UnixNano()
	last := w.last.Load()
	if now-last < int64(time.Minute) || !w.last.CompareAndSwap(last, now) {
		w.suppressed.Add(1)
		return
	}
	partRowLog.Warn(format, append(args, w.suppressed.Swap(0))...)
}

// listWarn rate-limits the folder-listing failure warning: a store fault
// repeats for every folder of a pass. cousinCapWarn does the same for
// cousin parents over cousinParentMaxRows.
var listWarn, cousinCapWarn warnLimiter

func warnListFailed(dir string, err error) {
	listWarn.warn("sibling listing failed; judging the title without its folder: dir=%s err=%s suppressed_since_last=%d",
		logger.SanitizeLogValue(dir), logger.SanitizeLogValue(err.Error()))
}

// partRowRefused returns the skip kind when title shows this row to be one
// file of a set the scanner filed as separate book rows (see
// ResolveCandidateSearchQuery), else "": SkipKindSiblingPart on evidence from
// the row's own folder, SkipKindCousinPart on evidence from the like-named
// folders beside a folder-wrapped row (cousinPart). The shapes:
//
//   - a chapter number or chapter fragment ("06 Chapter 6", "98",
//     "Elantris_copy179"; metadata.IsChapterOnlyTitle,
//     metadata.IsLikelyChapterFragment) on a chapter part row
//     (chapterPartKind): beside any sibling in its folder whatever the file's
//     duration -- a file-split part runs 10-60 min -- or, folder-wrapped, a
//     chapter-named file beside chapter-named cousins (cousinPart);
//   - an empty or placeholder title ("", "Unknown Title", "Unknown",
//     "Untitled"; isPlaceholderTitle) on a chapter part row
//     whose FILE is named by a chapter number ("Eldest/98.mp3");
//   - a counted part (metadata.IsCountedPartTitle: "002 of 341") or a bare
//     trailing part token (metadata.SiblingPartStem: "The Sunrise Lands 1")
//     with enough same-set siblings (countedSiblings, stemSiblings) on a row
//     that is not a whole product by duration (textShapePart), or,
//     folder-wrapped, enough same-set cousins with a set signal
//     (countedCousinSet, stemCousinSet; cousinPart);
//   - rip details (metadata.StripRipJunk) on a part row (ripShapePart), when
//     the title IS the folder's name: a folder name stamped onto each of its
//     files. Cousins never count here: a wrapped row's title is always its
//     folder's name.
//     "American Gods [64k 577MB].m4b" beside "Coraline.m4b" is a book.
//
// The folder is listed only when one of these shapes matches.
func (j *titleJudge) partRowRefused(title string) string {
	t := strings.TrimSpace(title)
	if t == "" || isPlaceholderTitle(t) {
		name := j.fileBaseName()
		if name == "" || !(metadata.IsChapterOnlyTitle(name) || metadata.IsLikelyChapterFragment(name)) {
			return ""
		}
		return j.chapterPartKind()
	}
	switch {
	case metadata.IsChapterOnlyTitle(t) || metadata.IsLikelyChapterFragment(t):
		return j.chapterPartKind()
	case metadata.IsCountedPartTitle(t):
		if j.textShapePart(func() int { return j.countedSiblings(t) }) {
			return SkipKindSiblingPart
		}
		return j.cousinPart(func() bool { return j.countedCousinSet(t) })
	}
	if _, had := metadata.StripRipJunk(t); had {
		dir := j.fileRowDir()
		if dir != "" && normTitle(t) == normTitle(filepath.Base(dir)) && j.ripShapePart() {
			return SkipKindSiblingPart
		}
		return ""
	}
	if _, _, ok := metadata.SiblingPartStem(t); ok {
		if j.textShapePart(func() int { return j.stemSiblings(t) }) {
			return SkipKindSiblingPart
		}
		return j.cousinPart(func() bool { return j.stemCousinSet(t) })
	}
	return ""
}

// cousinPart returns SkipKindCousinPart when the row's trusted duration is
// under cousinMaxPartSec and set reports a cousin set, else "". The
// duration is checked first, so a long or unknown-length row never lists its
// parent; an unknown duration never counts cousins at all (the probe-found
// whole-book series -- "Mistborn 1-7", "Discworld 01-41" -- mostly have no
// trusted duration or run 1 h and more).
func (j *titleJudge) cousinPart(set func() bool) string {
	if d := j.rowDurationSec(); d <= 0 || d >= cousinMaxPartSec {
		return ""
	}
	if set() {
		return SkipKindCousinPart
	}
	return ""
}

// chapterPartKind decides a row whose title (or, untitled, whose file name)
// is a chapter number or fragment. Beside any sibling in its own folder it
// is a part whatever its duration (isPartRow). With none, a folder-wrapped
// row ("Eldest/98/98.mp3") is a part only on matching evidence: its own FILE
// name is chapter-shaped too (a junk "01" tag on "Dune/Dune.m4b" is not),
// cousins are counted only when chapter-named ("Eldest/97/97.mp3", never
// "Hyperion/Hyperion.m4b"), and the duration decides first
// (cousinPart: a trusted duration under cousinMaxPartSec), since one book
// per folder is also how whole books are shelved.
func (j *titleJudge) chapterPartKind() string {
	if j.isPartRow() {
		return SkipKindSiblingPart
	}
	if !isChapterShaped(j.fileBaseName()) {
		return ""
	}
	return j.cousinPart(func() bool { return j.chapterCousins() >= 1 })
}

// chapterCousins counts the cousin rows (cousinPaths) whose file names are
// chapter-shaped.
func (j *titleJudge) chapterCousins() int {
	n := 0
	for _, p := range j.cousinPaths() {
		if isChapterShaped(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))) {
			n++
		}
	}
	return n
}

// isChapterShaped reports whether a title or file base name, as is or less a
// track prefix, is a chapter number or chapter fragment
// (metadata.IsChapterOnlyTitle, metadata.IsLikelyChapterFragment).
func isChapterShaped(s string) bool {
	for _, c := range []string{strings.TrimSpace(s), countedFileName(s)} {
		if c != "" && (metadata.IsChapterOnlyTitle(c) || metadata.IsLikelyChapterFragment(c)) {
			return true
		}
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
// registered import path (importRoots), or a generic folder
// (metadata.IsGenericDirName): its other rows are other books, and listing it
// would read the whole library.
func (j *titleJudge) isRootDir(dir string) bool {
	if dir == "." || dir == string(filepath.Separator) || metadata.IsGenericDirName(filepath.Base(dir)) {
		return true
	}
	if root := strings.TrimSpace(config.AppConfig.RootDir); root != "" && filepath.Clean(root) == dir {
		return true
	}
	return j.importRoots()[dir]
}

// importRoots is the import-root set for this row: the memo's shared,
// TTL-bounded set when there is a memo, else one read of j.files, made only
// when a row actually reaches a root check and kept for the rest of this
// judge.
func (j *titleJudge) importRoots() map[string]bool {
	if j.memo != nil {
		return j.memo.importRoots(j.files)
	}
	if !j.rootsLoaded {
		j.rootsLoaded = true
		// A failed read is logged inside; the row is then judged with no
		// import roots, as a first-read failure on the memo path is.
		if roots, ok := readImportRoots(j.files); ok {
			j.roots = roots
		}
	}
	return j.roots
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
		j.siblingsFailed = true // logged by the memo's read
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

// isCousinParentExcluded reports whether parent may not be listed for
// cousins: a root or generic folder (isRootDir), a genre shelf
// (authorname.IsGenreFolder), or a many-works container
// (cousinParentContainerNames: "Authors", "iTunes Media", ...).
func (j *titleJudge) isCousinParentExcluded(parent string) bool {
	if j.isRootDir(parent) {
		return true
	}
	base := filepath.Base(parent)
	return authorname.IsGenreFolder(base) || cousinParentContainerNames[strings.ToLower(strings.TrimSpace(base))]
}

// cousinPaths returns, for a folder-wrapped row -- its one file alone in a
// folder named like the file (isWrappedFile: "Gregory Benford/Great Sky
// River 18 6/Great Sky River 18 6.mp3") -- one FilePath per OTHER wrapped
// folder beside its own: parent/<X>/<file> with <X> named like <file>
// (listWrappedGrandchildren), read at most once. It is empty when the row is
// not wrapped, when its own folder holds a non-twin sibling (siblingPaths
// decides then) or could not be read (no evidence), when the parent is
// excluded (isCousinParentExcluded: its folders are other books, and listing
// it would read a whole shelf), or when it holds more than
// cousinParentMaxRows rows. Each cousin FOLDER counts once, so twin rows inside
// one (3-7 at one path on prod, ".mp3" beside ".m4b") are a single cousin,
// and nothing under the row's own folder is a cousin. A read fault is
// logged (rate-limited) and is no evidence, as for siblingPaths.
func (j *titleJudge) cousinPaths() []string {
	if j.cousinsLoaded {
		return j.cousins
	}
	j.cousinsLoaded = true
	dir := j.fileRowDir()
	if dir == "" || j.files == nil || !isWrappedFile(dir, j.rowFilePath()) {
		return nil
	}
	if len(j.siblingPaths()) > 0 || j.siblingsFailed {
		return nil
	}
	parent := filepath.Dir(dir)
	if parent == dir || j.isCousinParentExcluded(parent) {
		return nil
	}
	rows, err := j.memo.listWrapped(j.files, parent)
	if err != nil {
		return nil // logged by the memo's read
	}
	byFolder := map[string]string{}
	for id, p := range rows {
		d := filepath.Dir(p)
		if id == j.bookID || d == dir {
			continue
		}
		if cur, ok := byFolder[d]; !ok || p < cur {
			byFolder[d] = p
		}
	}
	for _, p := range byFolder {
		j.cousins = append(j.cousins, p)
	}
	sort.Strings(j.cousins)
	return j.cousins
}

// countedCousinSet reports whether a counted title's cousins make a set:
// minChapterSiblings same-set cousins (countedMatches) when the count is at
// least cousinBigSet ("In The Ocean Of Night 123 of 180"), else cousinBigSet
// of them (a set of fewer parts, "Dark Age (2 of 3)", never has enough).
// cousinPart's duration gate comes first either way.
func (j *titleJudge) countedCousinSet(title string) bool {
	stem, count, ok := j.countedKey(title)
	if !ok {
		return false
	}
	need := minChapterSiblings
	if m, err := strconv.Atoi(count); err != nil || m < cousinBigSet {
		need = cousinBigSet
	}
	return countedMatches(stem, count, j.cousinPaths()) >= need
}

// stemCousinSet reports whether a trailing-token title's cousins make a set:
// minChapterSiblings same-stem cousins (stemMatches) when the token is
// sub-numbered -- the stem itself ends in a number ("Great Sky River 18 6":
// stem "Great Sky River 18") -- else cousinBigSet of them ("Stem 3" beside
// "Stem 1"-"Stem 12"); "Mistborn 1" beside six series books is not a set.
func (j *titleJudge) stemCousinSet(title string) bool {
	stem, _, ok := metadata.SiblingPartStem(title)
	if !ok {
		return false
	}
	need := cousinBigSet
	if f := strings.Fields(stem); len(f) > 0 && allDigits(f[len(f)-1]) {
		need = minChapterSiblings
	}
	return stemMatches(stem, j.cousinPaths()) >= need
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func presentPaths(files []database.BookFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.FilePath)
	}
	return out
}

// isPartRow reports whether this single-file row shares its folder with
// another live book row that is not its twin. Cousins (cousinPaths) are not
// counted: a book alone in a folder named for it, beside other such folders,
// is how whole books are shelved too.
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
	stem, count, ok := j.countedKey(title)
	if !ok {
		return 0
	}
	return countedMatches(stem, count, j.siblingPaths())
}

// countedKey is this row's counted-set key (metadata.CountedPartKey): from
// its own file name, else from title.
func (j *titleJudge) countedKey(title string) (stem, count string, ok bool) {
	if stem, count, ok = metadata.CountedPartKey(countedFileName(j.fileBaseName())); ok {
		return stem, count, true
	}
	return metadata.CountedPartKey(title)
}

// countedMatches counts the paths whose file names are parts of the counted
// set (stem, count).
func countedMatches(stem, count string, paths []string) int {
	n := 0
	for _, p := range paths {
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
	return stemMatches(stem, j.siblingPaths())
}

// stemMatches counts the paths whose file names, less extension and any
// track prefix, are stem and a part token (siblingPartTailRe).
func stemMatches(stem string, paths []string) int {
	want := normTitle(stem)
	n := 0
	for _, p := range paths {
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		base = normTitle(leadingTrackRe.ReplaceAllString(strings.TrimSpace(base), ""))
		if rest, ok := strings.CutPrefix(base, want); ok && siblingPartTailRe.MatchString(rest) {
			n++
		}
	}
	return n
}
