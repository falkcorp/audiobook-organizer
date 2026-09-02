// file: internal/itunes/service/path_repair_resolver.go
// version: 1.1.1
// guid: 7d4f25a1-8e29-4b8b-9a02-3c5e1f9d4b27
//
// Pure-function resolvers for the path-repair operation. Each tier
// takes a narrow store interface and an existsFn so tests can drive
// them without a real filesystem.
// last-edited: 2026-09-02

package itunesservice

import (
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/matcher"
)

// audioExt is the set of file extensions tier B inspects. Anything
// outside the set is skipped during the walk.
var audioExt = map[string]struct{}{
	".m4b": {}, ".m4a": {}, ".mp3": {}, ".ogg": {}, ".flac": {},
	".opus": {}, ".aac": {}, ".wav": {},
}

// bookIDExtractor pulls the AUDIOBOOK_ORGANIZER_ID tag from one
// audio file. Returns "" when the tag is absent. Production wires
// this to metadata.ExtractMetadata; tests inject a deterministic fake.
type bookIDExtractor func(audioFilePath string) (string, error)

// noopTagScanner returns no matches. Used when tier B is misconfigured
// (no audiobook root, no extractor) so the worker can stay on a single
// code path without nil-checks.
type noopTagScanner struct{}

func (noopTagScanner) bookIDToPaths(string) []string { return nil }
func (noopTagScanner) allPaths() []string            { return nil }

// scanProgressFn is invoked periodically while the audiobook tree is
// being scanned. (done, total) where total is the count of audio files
// discovered in phase 1; done counts how many have been tag-extracted
// in phase 2. Implementations should be cheap — the scanner calls this
// off the hot path but still in the same process.
type scanProgressFn func(done, total int)

// fsTagScanner walks the audiobook root once, lazily, the first time
// bookIDToPaths or allPaths is called. The walk runs in two phases:
// phase 1 enumerates audio files (cheap, single goroutine, just
// directory I/O), phase 2 extracts the AUDIOBOOK_ORGANIZER_ID tag
// from each file in parallel via a worker pool. Subsequent calls hit
// the in-memory state.
type fsTagScanner struct {
	root       string
	app        pathutil.AppDirs
	extract    bookIDExtractor
	workers    int            // 0 → runtime.NumCPU() * 4 (tag reads are I/O-bound)
	progress   scanProgressFn // optional; nil means silent
	progressEv int            // emit progress every N files; 0 → 250

	once  sync.Once
	index map[string][]string
	all   []string
}

// newFSTagScanner builds a scanner over `root`.
//
// `app` names the application-owned directories under root that must not be
// scanned, and is REQUIRED for the reason pathutil.AppDirs documents. root is
// cfg.AudiobookRoot, which is wired from config RootDir -- the library root,
// inside which the application keeps a backup directory and an OpenLibrary
// dump directory. Phase 2 below reads TAGS from every file phase 1 enumerates,
// so an unexcluded app dir costs tag I/O over tens of GB and can put
// application state into the bookID→paths index used to REPAIR track paths.
func newFSTagScanner(root string, app pathutil.AppDirs, extract bookIDExtractor) *fsTagScanner {
	return &fsTagScanner{root: root, app: app, extract: extract}
}

// withWorkers overrides the default worker count. Useful for tests
// that want a deterministic single-threaded scan.
func (s *fsTagScanner) withWorkers(n int) *fsTagScanner {
	s.workers = n
	return s
}

// withProgress installs a callback fired every N processed files
// during phase 2. Pass everyN=0 for the default cadence.
func (s *fsTagScanner) withProgress(fn scanProgressFn, everyN int) *fsTagScanner {
	s.progress = fn
	s.progressEv = everyN
	return s
}

func (s *fsTagScanner) bookIDToPaths(bookID string) []string {
	s.once.Do(s.scan)
	return s.index[bookID]
}

func (s *fsTagScanner) allPaths() []string {
	s.once.Do(s.scan)
	return s.all
}

func (s *fsTagScanner) scan() {
	s.index = make(map[string][]string)
	if s.root == "" {
		return
	}

	// Phase 1: enumerate every audio file. Cheap (no tag I/O); must
	// be sequential because filepath.WalkDir doesn't parallelize.
	var paths []string
	_ = filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			if pathutil.ShouldSkipDir(s.root, path, s.app) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if _, ok := audioExt[ext]; !ok {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	s.all = paths

	if s.extract == nil || len(paths) == 0 {
		return
	}

	// Phase 2: parallel tag extraction. Tag reads are dominated by
	// taglib + disk seek; oversubscribing relative to NumCPU keeps
	// spinning disks busy.
	workers := s.workers
	if workers <= 0 {
		workers = runtime.NumCPU() * 4
	}
	if workers > len(paths) {
		workers = len(paths)
	}
	everyN := s.progressEv
	if everyN <= 0 {
		everyN = 250
	}

	type result struct {
		path   string
		bookID string
	}
	jobs := make(chan string, workers*2)
	results := make(chan result, workers*2)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for p := range jobs {
				bookID, err := s.extract(p)
				if err != nil || bookID == "" {
					results <- result{path: p}
					continue
				}
				results <- result{path: p, bookID: bookID}
			}
		}()
	}
	go func() {
		for _, p := range paths {
			jobs <- p
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	var done atomic.Int64
	total := len(paths)
	for r := range results {
		if r.bookID != "" {
			s.index[r.bookID] = append(s.index[r.bookID], r.path)
		}
		n := done.Add(1)
		if s.progress != nil && (n%int64(everyN) == 0 || n == int64(total)) {
			s.progress(int(n), total)
		}
	}
}

// tierAStore is the slice tier A needs after the bookID has been
// resolved at the worker level (see lookupBookID).
type tierAStore interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// pidLookup is the worker-level PID → bookID hop. Hoisted so tier A
// and tier B share a single DB call per missing track.
type pidLookup interface {
	GetBookByExternalID(source, externalID string) (string, error)
}

// lookupBookID returns the bookID for an iTunes PID, or "" when the
// mapping is absent or the store errors. Errors are intentionally
// swallowed here — at the tier resolver level "no mapping" and
// "lookup failed" lead to the same fall-through path.
func lookupBookID(s pidLookup, pid string) string {
	bookID, err := s.GetBookByExternalID("itunes", pid)
	if err != nil {
		return ""
	}
	return bookID
}

// tagScanner exposes a lazy, cached lookup from
// AUDIOBOOK_ORGANIZER_ID tag value (the audiobook-organizer book ID)
// to the on-disk paths whose audio files carry that tag, plus the
// flat list of every audio file the walk found (for tier C scoring).
// Tests inject a fake; production walks the audiobook root.
type tagScanner interface {
	bookIDToPaths(bookID string) []string
	allPaths() []string
}

// resolveTierB resolves a missing PID via the embedded
// AUDIOBOOK_ORGANIZER_ID tag scan: bookID (already looked up at the
// worker level) → unique on-disk path. Multi-segment books with
// multiple disk matches are deliberately returned unresolved — those
// go to tier C for human review.
func resolveTierB(scanner tagScanner, bookID string, existsFn func(string) bool) (string, bool) {
	if bookID == "" {
		return "", false
	}
	paths := scanner.bookIDToPaths(bookID)
	if len(paths) != 1 {
		return "", false
	}
	if !existsFn(paths[0]) {
		return "", false
	}
	return paths[0], true
}

// trackInfo carries the iTunes-side hints tier C scores against.
type trackInfo struct {
	Title       string
	OldBasename string
}

// tierCCandidate is one ranked match emitted to the needs_review list.
type tierCCandidate struct {
	Path  string `json:"path"`
	Score int    `json:"score"`
}

// resolveTierC scores every candidate path against the iTunes track
// title and the old basename, then returns the top-N candidates whose
// score meets the threshold. Never auto-applies — caller emits to the
// needs_review list for human confirmation.
//
// We score against both the title and the old basename and take the
// max so e.g. a file renamed to use the title still scores well, and
// a file whose basename was preserved across a directory move also
// scores well.
func resolveTierC(candidates []string, info trackInfo, threshold, topN int) []tierCCandidate {
	if len(candidates) == 0 || (info.Title == "" && info.OldBasename == "") {
		return nil
	}
	scored := make([]tierCCandidate, 0, len(candidates))
	for _, p := range candidates {
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		var s int
		if info.Title != "" {
			if v := matcher.ScoreMatch(info.Title, base); v > s {
				s = v
			}
		}
		if info.OldBasename != "" {
			oldBase := strings.TrimSuffix(info.OldBasename, filepath.Ext(info.OldBasename))
			if v := matcher.ScoreMatch(oldBase, base); v > s {
				s = v
			}
		}
		if s < threshold {
			continue
		}
		scored = append(scored, tierCCandidate{Path: p, Score: s})
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if topN > 0 && len(scored) > topN {
		scored = scored[:topN]
	}
	return scored
}

// resolveTierA returns the on-disk path the DB thinks the file is at
// for a given (pid, bookID) — preferring the matching BookFile over
// Book.FilePath. Returns ok=false when the DB-known path doesn't
// exist on disk.
//
// The caller is responsible for resolving PID → bookID via
// lookupBookID first, so tier A and tier B share that DB call.
func resolveTierA(s tierAStore, pid, bookID string, existsFn func(string) bool) (string, bool) {
	if bookID == "" {
		return "", false
	}
	if files, err := s.GetBookFiles(bookID); err == nil {
		for _, bf := range files {
			if bf.ITunesPersistentID == pid && bf.FilePath != "" && existsFn(bf.FilePath) {
				return bf.FilePath, true
			}
		}
	}
	if book, err := s.GetBookByID(bookID); err == nil && book != nil && book.FilePath != "" && existsFn(book.FilePath) {
		return book.FilePath, true
	}
	return "", false
}
