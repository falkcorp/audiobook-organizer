// file: internal/audiobooks/runtime_index.go
// version: 1.0.0
// guid: 2b9f4c61-8e3a-4a7d-9c05-5d1e7f3a6b28
// last-edited: 2026-09-27

package audiobooks

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// runtimeIndex caches every book's canonical runtime (database.ComputeBookRuntime
// over its book_file rows) so the duration: library filter can use the real
// runtime without a per-row GetBookFiles.
//
// WHY not Book.Duration alone: it is the sum of the file durations that are
// KNOWN, so a multi-file book with unprobed chapters carries a partial sum
// (see database/book_runtime.go). A 10 h book with two probed 20-minute
// chapters would read as 40 minutes and be listed by duration:<1h.
//
// WHY not per-row LoadBookRuntime: ~36% of prod books are under 20 minutes, so
// a lower-bound shortcut still needs file rows for tens of thousands of books,
// one Pebble prefix scan each, on the walker's single goroutine. One pass over
// GetAllBookFilesCore (memdb, in memory) grouped by book is far cheaper, and it
// is cached.
//
// Staleness: the index is rebuilt in the background once it is older than
// runtimeIndexTTL; the stale copy keeps serving meanwhile. A book absent from
// the index (imported since the last build) falls back to its stored
// Book.Duration. A runtime filter is a triage view, so minutes of staleness on
// a just-probed file is an acceptable price for a sub-second whole-library
// query; it is stated in the help text.
type runtimeIndex struct {
	mu      sync.RWMutex
	byBook  map[string]runtimeEntry
	builtAt time.Time
	// building guards against concurrent background rebuilds.
	building atomic.Bool
	// firstBuild serialises the inline first build.
	firstBuild sync.Mutex
	// now is injectable for tests.
	now func() time.Time
	ttl time.Duration
}

// runtimeEntry is a compact projection of database.BookRuntime.
type runtimeEntry struct {
	seconds      int32
	filesCounted int32
	filesKnown   int32
}

const runtimeIndexTTL = 5 * time.Minute

var runtimeIndexLog = logger.New("audiobooks.runtime_index")

// bookFileVisitor is the preferred store capability: walk every book_file row
// without copying (database.PebbleStore.VisitBookFiles).
type bookFileVisitor interface {
	VisitBookFiles(fn func(*database.BookFile)) error
}

// allBookFilesCoreGetter is the fallback capability (copies every row).
type allBookFilesCoreGetter interface {
	GetAllBookFilesCore() ([]database.BookFileCore, error)
}

func newRuntimeIndex() *runtimeIndex {
	return &runtimeIndex{now: time.Now, ttl: runtimeIndexTTL}
}

// runtimeRow is the part of a book_file row ComputeBookRuntime reads. Strings
// share the stored row's backing memory, so a row costs ~100 bytes, not the
// 792 of a BookFile (~742k rows on prod).
type runtimeRow struct {
	bookID, fileHash, origHash, origName string
	size                                 int64
	dur                                  int
	acoustid                             float64
	missing                              bool
}

func rowFromBookFile(f *database.BookFile) runtimeRow {
	return runtimeRow{
		bookID: f.BookID, fileHash: f.FileHash, origHash: f.OriginalFileHash, origName: f.OriginalFilename,
		size: f.FileSize, dur: f.Duration, acoustid: f.AcoustIDFingerprintDurationSec, missing: f.Missing,
	}
}

func rowFromCore(c *database.BookFileCore) runtimeRow {
	return runtimeRow{
		bookID: c.BookID, fileHash: c.FileHash, origHash: c.OriginalFileHash, origName: c.OriginalFilename,
		size: c.FileSize, dur: c.Duration, acoustid: c.AcoustIDFingerprintDurationSec, missing: c.Missing,
	}
}

// runtimeRowSource returns a loader for the store's rows, or nil when the
// store can supply none. Decorators are unwrapped via AsCapability.
func runtimeRowSource(store any) func() ([]runtimeRow, error) {
	if v, ok := database.AsCapability[bookFileVisitor](store); ok {
		return func() ([]runtimeRow, error) {
			var rows []runtimeRow
			err := v.VisitBookFiles(func(f *database.BookFile) { rows = append(rows, rowFromBookFile(f)) })
			return rows, err
		}
	}
	if g, ok := database.AsCapability[allBookFilesCoreGetter](store); ok {
		return func() ([]runtimeRow, error) {
			core, err := g.GetAllBookFilesCore()
			if err != nil {
				return nil, err
			}
			rows := make([]runtimeRow, len(core))
			for i := range core {
				rows[i] = rowFromCore(&core[i])
			}
			return rows, nil
		}
	}
	return nil
}

// buildRuntimeEntries groups rows by book and computes each book's runtime
// with database.ComputeBookRuntime. The per-book compute is sharded across
// NumCPU workers over disjoint book-ID chunks, so no two workers write the
// same key; each worker fills its own map and they are merged afterwards.
// Each worker converts one book's rows into a reused []BookFile buffer just
// before computing it, so a full BookFile never exists for every row at once.
func buildRuntimeEntries(rows []runtimeRow) map[string]runtimeEntry {
	byBook := make(map[string][]int32, len(rows)/4+1)
	for i := range rows {
		byBook[rows[i].bookID] = append(byBook[rows[i].bookID], int32(i))
	}
	ids := make([]string, 0, len(byBook))
	for id := range byBook {
		ids = append(ids, id)
	}
	workers := max(runtime.NumCPU(), 1)
	chunk := (len(ids) + workers - 1) / workers
	parts := make([]map[string]runtimeEntry, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo := w * chunk
		if lo >= len(ids) {
			break
		}
		hi := min(lo+chunk, len(ids))
		wg.Go(func() {
			m := make(map[string]runtimeEntry, hi-lo)
			var buf []database.BookFile
			for _, id := range ids[lo:hi] {
				buf = buf[:0]
				for _, i := range byBook[id] {
					r := &rows[i]
					buf = append(buf, database.BookFile{
						BookID: r.bookID, Duration: r.dur, FileSize: r.size, Missing: r.missing,
						FileHash: r.fileHash, OriginalFileHash: r.origHash, OriginalFilename: r.origName,
						AcoustIDFingerprintDurationSec: r.acoustid,
					})
				}
				rt := database.ComputeBookRuntime(nil, buf)
				m[id] = runtimeEntry{
					seconds:      int32(min(rt.Seconds, 1<<31-1)),
					filesCounted: int32(rt.FilesCounted),
					filesKnown:   int32(rt.FilesKnown),
				}
			}
			parts[w] = m
		})
	}
	wg.Wait()
	out := make(map[string]runtimeEntry, len(ids))
	for _, m := range parts {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// ensure makes the index usable: a synchronous build when it has never been
// built, a background rebuild when it is older than the TTL. Returns false
// when the store cannot supply file rows (the caller then uses Book.Duration).
func (ri *runtimeIndex) ensure(store any) bool {
	load := runtimeRowSource(store)
	if load == nil {
		return false
	}
	ri.mu.RLock()
	built := !ri.builtAt.IsZero()
	stale := built && ri.now().Sub(ri.builtAt) > ri.ttl
	ri.mu.RUnlock()
	if built && !stale {
		return true
	}
	if !built {
		// First use: build inline so the very first duration query is right.
		// Serialised so N concurrent cold requests run ONE whole-library build
		// (the rest wait for it and then find it built), not N.
		ri.firstBuild.Lock()
		defer ri.firstBuild.Unlock()
		ri.mu.RLock()
		built = !ri.builtAt.IsZero()
		ri.mu.RUnlock()
		if built {
			return true
		}
		return ri.rebuild(load)
	}
	if ri.building.CompareAndSwap(false, true) {
		go func() {
			defer ri.building.Store(false)
			ri.rebuild(load)
		}()
	}
	return true
}

func (ri *runtimeIndex) rebuild(load func() ([]runtimeRow, error)) bool {
	start := ri.now()
	rows, err := load()
	if err != nil {
		runtimeIndexLog.Warn("runtime index build failed; duration filters fall back to stored Book.Duration: %v", err)
		ri.mu.RLock()
		built := !ri.builtAt.IsZero()
		ri.mu.RUnlock()
		return built
	}
	entries := buildRuntimeEntries(rows)
	ri.mu.Lock()
	ri.byBook = entries
	ri.builtAt = ri.now()
	ri.mu.Unlock()
	runtimeIndexLog.Debug("runtime index built: books=%d files=%d elapsed=%s", len(entries), len(rows), ri.now().Sub(start))
	return true
}

// runtimeOf returns a book's canonical runtime and whether it is known,
// mirroring database.ComputeBookRuntime(book, files).KnownSeconds():
//   - every counted file has a known duration → their sum;
//   - no known file and at most one row → the stored Book.Duration (a
//     single-file book's probed duration);
//   - otherwise (partial multi-file book, or nothing known) → unknown.
//
// A book missing from the index uses the stored Book.Duration.
func (ri *runtimeIndex) runtimeOf(b *database.Book) (int, bool) {
	if b == nil {
		return 0, false
	}
	ri.mu.RLock()
	e, ok := ri.byBook[b.ID]
	ri.mu.RUnlock()
	if !ok {
		return storedRuntime(b)
	}
	if e.filesKnown > 0 {
		if e.filesKnown == e.filesCounted && e.seconds > 0 {
			return int(e.seconds), true
		}
		return 0, false // partial: a lower bound, never a total
	}
	if e.filesCounted <= 1 {
		return storedRuntime(b)
	}
	return 0, false
}

// runtimeFuncFor returns the runtime function the filters should use: the
// index when any filter needs a runtime and the store can build one, else nil
// (callers treat nil as the stored-aggregate fallback).
func (svc *AudiobookService) runtimeFuncFor(filters []FieldFilter) runtimeFunc {
	if svc == nil || !hasDurationFilter(filters) {
		return nil
	}
	if svc.runtimeIdx == nil {
		return nil
	}
	if !svc.runtimeIdx.ensure(svc.store) {
		return nil
	}
	return svc.runtimeIdx.runtimeOf
}
