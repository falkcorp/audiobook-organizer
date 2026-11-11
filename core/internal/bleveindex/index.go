// file: core/internal/bleveindex/index.go
// version: 1.0.0
// guid: 5b3a9e32-ac5d-4cae-9d75-8f2e4b7d9c33
// last-edited: 2026-11-11

package bleveindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
)

// idx is one open index. It generalises HEAD's BleveIndex
// (internal/search/bleve_index.go): scorch, the mapping marker and the
// rebuilding marker as siblings of the index directory, an open failure that
// degrades instead of failing boot, and a mapping change that drops and
// rebuilds.
type idx struct {
	e       *Engine
	module  string
	name    string
	spec    index.Spec
	path    string
	kinds   map[string]index.FieldKind
	stored  []string
	dirty   *index.DirtySet
	log     *slog.Logger
	cb      asyncCallback
	derived storage.Derived

	// mu guards bi and closed: searches and writes hold it for reading, a
	// recreate (rebuild) and Close for writing.
	mu     sync.RWMutex
	bi     bleve.Index // nil when Degraded or closed
	closed bool

	state atomic.Uint32 // index.State

	// opMu serialises the two writers of the index's contents: a reconcile pass
	// (TryLock, skipped while busy) and a full rebuild (Lock, for its whole
	// run). A reconcile pass that applied marks while a rebuild dropped and
	// refilled the index would let the rebuild overwrite a newer document with
	// the older one it read, and clear the mark: a stale document forever.
	opMu sync.Mutex

	// rebuilt is set when a full rebuild ran to completion. The index leaves
	// Rebuilding only after that AND a reconcile pass that found the dirty
	// set empty (HEAD's rule: coverage seeded AND drained to zero).
	rebuilt atomic.Bool
	// recreatedForMapping records that Open threw an index away because its
	// mapping version was stale.
	recreatedForMapping bool
	// dirtySince is the UnixNano the dirty set was last seen going non-empty,
	// 0 while it is empty: the reconcile lag.
	dirtySince atomic.Int64
	failures   atomic.Int64 // documents a reconcile pass could not load or index

	// afterGenerationRead is a test seam, nil in production: CopyTo calls it
	// right after reading the generation, the window in which a write must leave
	// the artifact rejected.
	afterGenerationRead func()
}

func (i *idx) setState(s index.State) { i.state.Store(uint32(s)) }

// State reports Ready, Rebuilding or Degraded.
func (i *idx) State() index.State { return index.State(i.state.Load()) }

func mappingMarkerPath(indexPath string) string { return indexPath + ".mapping" }

func rebuildingMarkerPath(indexPath string) string { return indexPath + ".rebuilding" }

// open creates or opens the on-disk index. It never returns an error for a
// failure of the index itself: that is Degraded (I4), so a corrupt or
// unwritable index costs search, not boot.
func (i *idx) open() {
	cfg := i.cb.register(i.module, i.name)
	if info, err := os.Stat(i.path); err == nil && info.IsDir() {
		stored := readMarker(mappingMarkerPath(i.path))
		if stored == i.spec.MappingVersion {
			bi, err := bleve.OpenUsing(i.path, cfg)
			if err != nil {
				i.degrade(fmt.Errorf("open existing index: %w", err))
				return
			}
			i.bi = bi
			if _, err := os.Stat(rebuildingMarkerPath(i.path)); err == nil {
				i.setState(index.Rebuilding)
			} else {
				i.setState(index.Ready)
			}
			return
		}
		i.log.Warn("index mapping version changed; recreating the index; search falls back until the rebuild finishes",
			"module", i.module, "index", i.name, "stored", stored, "want", i.spec.MappingVersion)
		if err := os.RemoveAll(i.path); err != nil {
			i.degrade(fmt.Errorf("remove stale index: %w", err))
			return
		}
		if err := i.create(cfg); err != nil {
			i.degrade(err)
			return
		}
		i.recreatedForMapping = true
		return
	}
	if err := i.create(cfg); err != nil {
		i.degrade(err)
	}
}

func (i *idx) degrade(err error) {
	i.bi = nil
	i.setState(index.Degraded)
	i.log.Error("index failed to open; it is degraded and its callers fall back",
		"module", i.module, "index", i.name, "path", i.path, "err", err)
}

// create builds a new empty index and records its markers. The rebuilding
// marker is written FIRST and its failure is fatal to the create: a crash
// between create and the rebuild finishing must come back as "rebuilding",
// never as a complete index. The mapping marker is written only AFTER a
// successful create: written first, a failed create would leave a marker with
// no index. Its failure is not fatal (the index is valid) but is loud, because
// the index is recreated on every restart until it is fixed.
func (i *idx) create(cfg map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(i.path), 0o750); err != nil {
		return fmt.Errorf("create index directory: %w", err)
	}
	if err := os.WriteFile(rebuildingMarkerPath(i.path), []byte("1\n"), 0o600); err != nil {
		return fmt.Errorf("write rebuilding marker: %w", err)
	}
	m, err := buildMapping(i.spec)
	if err != nil {
		return err
	}
	bi, err := bleve.NewUsing(i.path, m, "scorch", "scorch", cfg)
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	i.bi = bi
	i.setState(index.Rebuilding)
	if err := os.WriteFile(mappingMarkerPath(i.path), []byte(i.spec.MappingVersion+"\n"), 0o600); err != nil {
		i.log.Error("failed to write the index mapping marker; the index will be rebuilt on EVERY restart until this is fixed",
			"marker", mappingMarkerPath(i.path), "err", err)
	}
	return nil
}

// readMarker returns a marker's content, or "" when absent or unreadable ("" is
// the answer for an index built before markers existed and forces a recreate).
func readMarker(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// recreate drops the on-disk index and creates it empty, Rebuilding: the first
// step of a full rebuild, which must not leave a document the source no longer
// has.
func (i *idx) recreate() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return index.ErrClosed
	}
	if i.bi != nil {
		if err := i.bi.Close(); err != nil {
			i.log.Warn("closing the index to recreate it", "module", i.module, "index", i.name, "err", err)
		}
		i.bi = nil
	}
	if err := os.RemoveAll(i.path); err != nil {
		i.degrade(fmt.Errorf("remove index to rebuild: %w", err))
		return fmt.Errorf("bleveindex: %w", index.ErrDegraded)
	}
	i.rebuilt.Store(false)
	if err := i.create(i.cb.register(i.module, i.name)); err != nil {
		i.degrade(err)
		return fmt.Errorf("bleveindex: %w: %v", index.ErrDegraded, err)
	}
	return nil
}

// acquire returns the live bleve index under a read lock, or the reason there
// is none. release must be called.
func (i *idx) acquire() (bleve.Index, func(), error) {
	i.mu.RLock()
	switch {
	case i.closed:
		i.mu.RUnlock()
		return nil, nil, index.ErrClosed
	case i.bi == nil:
		i.mu.RUnlock()
		return nil, nil, index.ErrDegraded
	}
	return i.bi, i.mu.RUnlock, nil
}

// Search runs q. A Rebuilding index answers from what it holds so far: a miss
// is not "no such document", which is why callers consult State. A Degraded
// index answers ErrDegraded and the caller serves its fallback.
func (i *idx) Search(ctx context.Context, q index.Query, page index.Page) (index.Result, error) {
	bq, err := i.query(q)
	if err != nil {
		return index.Result{}, err
	}
	size, err := pageWindow(page)
	if err != nil {
		return index.Result{}, err
	}
	bi, release, err := i.acquire()
	if err != nil {
		return index.Result{}, err
	}
	defer release()
	req := bleve.NewSearchRequestOptions(bq, size, page.From, false)
	if len(page.SortBy) > 0 {
		req.SortBy(page.SortBy)
	} else {
		req.SortBy([]string{"-_score", "_id"})
	}
	req.Fields = i.stored
	res, err := bi.SearchInContext(ctx, req)
	if err != nil {
		return index.Result{}, fmt.Errorf("bleveindex: search %s/%s: %w", i.module, i.name, err)
	}
	out := index.Result{Total: res.Total, Hits: make([]index.Hit, 0, len(res.Hits))}
	for _, h := range res.Hits {
		out.Hits = append(out.Hits, index.Hit{ID: h.ID, Score: h.Score, Fields: h.Fields})
	}
	return out, nil
}

// pageWindow validates page and returns the page size to ask bleve for. Each
// value is bounded on its own BEFORE any arithmetic: From+Size would wrap for a
// From near MaxInt, go negative, pass a plain "sum > max" test and make bleve
// allocate a window of that size.
func pageWindow(page index.Page) (int, error) {
	size := page.Size
	if size == 0 {
		size = defaultPageSize
	}
	if page.From < 0 || size < 0 ||
		page.From > maxResultWindow || size > maxResultWindow ||
		page.From > maxResultWindow-size {
		return 0, fmt.Errorf("%w: page [from %d, size %d] is negative or ends past %d results", index.ErrInvalidQuery, page.From, page.Size, maxResultWindow)
	}
	return size, nil
}

// Page limits: a window deeper than maxResultWindow is refused rather than
// paid for in memory, and a page of size 0 is defaultPageSize.
const (
	defaultPageSize = 10
	maxResultWindow = 10000
)

// Count is the number of documents matching q.
func (i *idx) Count(ctx context.Context, q index.Query) (uint64, error) {
	bq, err := i.query(q)
	if err != nil {
		return 0, err
	}
	bi, release, err := i.acquire()
	if err != nil {
		return 0, err
	}
	defer release()
	res, err := bi.SearchInContext(ctx, bleve.NewSearchRequestOptions(bq, 0, 0, false))
	if err != nil {
		return 0, fmt.Errorf("bleveindex: count %s/%s: %w", i.module, i.name, err)
	}
	return res.Total, nil
}

// Facets counts, for each field, the terms among the documents matching q,
// most frequent first. Only fields declared Facet are countable.
func (i *idx) Facets(ctx context.Context, q index.Query, fields []string, size int) (map[string][]index.FacetValue, error) {
	bq, err := i.query(q)
	if err != nil {
		return nil, err
	}
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxResultWindow {
		return nil, fmt.Errorf("%w: facet size %d is past %d", index.ErrInvalidQuery, size, maxResultWindow)
	}
	for _, f := range fields {
		if !i.facetable(f) {
			return nil, fmt.Errorf("%w: field %q is not a facet", index.ErrInvalidQuery, f)
		}
	}
	bi, release, err := i.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	req := bleve.NewSearchRequestOptions(bq, 0, 0, false)
	for _, f := range fields {
		req.AddFacet(f, bleve.NewFacetRequest(f, size))
	}
	res, err := bi.SearchInContext(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("bleveindex: facets %s/%s: %w", i.module, i.name, err)
	}
	out := make(map[string][]index.FacetValue, len(fields))
	for _, f := range fields {
		fr := res.Facets[f]
		vals := []index.FacetValue{}
		if fr != nil && fr.Terms != nil {
			for _, tf := range fr.Terms.Terms() {
				vals = append(vals, index.FacetValue{Term: tf.Term, Count: tf.Count})
			}
		}
		out[f] = vals
	}
	return out, nil
}

func (i *idx) facetable(field string) bool {
	for _, f := range i.spec.Fields {
		if f.Name == field {
			return f.Facet
		}
	}
	return false
}

func (i *idx) query(q index.Query) (query.Query, error) {
	n, err := index.Describe(q)
	if err != nil {
		return nil, err
	}
	return translate(i.kinds, n)
}

// MarkDirty records id in the durable dirty set inside b, the entity's own
// batch, and touches the index's generation in it. It works in every state,
// Degraded included: the mark is durable and drains when the index is healthy
// again.
//
// It takes no context (the contract's signature has none), but the generation
// touch draws from a Sequence that may commit a lease refill of its own, so it
// runs under a background context: a refill is one short local commit.
func (i *idx) MarkDirty(b *storage.Batch, id string) error {
	i.mu.RLock()
	closed := i.closed
	i.mu.RUnlock()
	if closed {
		return index.ErrClosed
	}
	if err := i.dirty.Mark(context.Background(), b, id); err != nil {
		return err
	}
	// The reconciler also wakes on its interval, so a nudge that fires before
	// b commits (a Batch has no commit hook) costs at most one idle pass; the
	// mark is then found on the next tick.
	i.e.nudge()
	return nil
}

// Close ends the index: it leaves the engine, drops its callback and closes
// the bleve handle. It is idempotent.
func (i *idx) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	bi := i.bi
	i.bi = nil
	i.mu.Unlock()
	i.e.forget(i)
	var err error
	if bi != nil {
		err = bi.Close()
	}
	i.cb.unregister()
	return err
}

// applyBatch applies docs (a nil Doc deletes the id) as one bleve batch.
func (i *idx) applyBatch(entries []entry) error {
	bi, release, err := i.acquire()
	if err != nil {
		return err
	}
	defer release()
	b := bi.NewBatch()
	for _, e := range entries {
		if e.doc == nil {
			b.Delete(e.id)
			continue
		}
		if err := b.Index(e.id, map[string]any(e.doc)); err != nil {
			return fmt.Errorf("bleveindex: index %q: %w", e.id, err)
		}
	}
	if err := bi.Batch(b); err != nil {
		return fmt.Errorf("bleveindex: apply batch to %s/%s: %w", i.module, i.name, err)
	}
	return nil
}

// entry is one document operation: a nil doc deletes the id.
type entry struct {
	id  string
	doc index.Doc
}

// markReady ends Rebuilding: coverage was seeded and the dirty set drained.
func (i *idx) markReady() error {
	if err := os.Remove(rebuildingMarkerPath(i.path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove rebuilding marker: %w", err)
	}
	i.setState(index.Ready)
	return nil
}

// docCount is the number of documents in the index (0 when unavailable).
func (i *idx) docCount() uint64 {
	bi, release, err := i.acquire()
	if err != nil {
		return 0
	}
	defer release()
	n, err := bi.DocCount()
	if err != nil {
		return 0
	}
	return n
}

func (i *idx) lag(now time.Time) time.Duration {
	since := i.dirtySince.Load()
	if since == 0 {
		return 0
	}
	return max(now.Sub(time.Unix(0, since)), 0)
}

// CopyTo takes bleve's online copy of the index into dir, which must not hold
// an index, and stamps it with the generation read BEFORE the copy
// (storage.StampArtifact), so a touch that commits during the copy leaves the
// artifact rejected rather than accepted with a document missing (§5S.3.9).
// The generation is the index's, which MarkDirty touches, so an artifact
// restored beside a store that has since seen any write to the index's source
// does not match. Backups call it (05-backup).
func (i *idx) CopyTo(ctx context.Context, dir string) error {
	gen, err := i.derived.Current(ctx)
	if err != nil {
		return err
	}
	if i.afterGenerationRead != nil {
		i.afterGenerationRead()
	}
	bi, release, err := i.acquire()
	if err != nil {
		return err
	}
	defer release()
	cp, ok := bi.(bleve.IndexCopyable)
	if !ok {
		return errors.New("bleveindex: the index backend cannot copy online")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("bleveindex: copy destination: %w", err)
	}
	if err := cp.CopyTo(bleve.FileSystemDirectory(dir)); err != nil {
		return fmt.Errorf("bleveindex: copy %s/%s: %w", i.module, i.name, err)
	}
	return storage.StampArtifact(dir, gen)
}
