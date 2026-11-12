// file: core/internal/bleveindex/engine.go
// version: 1.0.1
// guid: 6c4bafa3-bd6e-4dbf-ae86-9a3f5c8eadd4
// last-edited: 2026-11-11

// Package bleveindex implements storage/index on bleve's scorch backend
// (05-storage §5S.3.8). One Engine serves every module's indexes: it opens
// them at <db parent>/index/<module>/<name>.bleve, reconciles their durable
// dirty sets, and builds the task definition that fully rebuilds one.
//
// Nothing in ABO opens this package until F7-09; ABO's internal/search keeps
// its own index at library.bleve (I6).
package bleveindex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Defaults of core.index.reconcile.workers and .interval (05-storage §5S.7),
// used when Options leaves the readers nil.
const DefaultInterval = 5 * time.Second

// defaultChunk is how many marks one bleve batch applies.
const defaultChunk = 64

// pageSize is how many marks a reconcile pass reads at a time.
const pageSize = 1024

// Query limits. A query's cost is set by the terms its wildcards, prefixes and
// fuzzy matches expand to, not by its size, so the engine bounds the expansion
// itself (bleve's term-searcher cap, set on every Search, Count and Facets),
// the time one call may take when its caller set no deadline, and the size and
// shape of the query it will translate.
const (
	// DefaultMaxTermSearchers is the most leaf term searchers one search may
	// create (Options.MaxTermSearchers). A prefix over a large field fails fast
	// at this bound instead of allocating gigabytes.
	DefaultMaxTermSearchers = 1024
	// DefaultSearchTimeout bounds a call whose context has no deadline.
	DefaultSearchTimeout = 30 * time.Second
	// DefaultPassBudget is how long one reconcile pass spends on one index
	// before it yields to the next (it always applies at least one page).
	DefaultPassBudget = 10 * time.Second

	maxQueryStringBytes = 4 << 10 // a QueryString and any one query text
	maxQueryNodes       = 1024    // nodes in one query tree
	maxQueryDepth       = 32      // nesting of And, Or and Not
)

// ErrNotReady: CopyTo of an index that is not Ready (Rebuilding, Degraded):
// its content does not cover its source, so no artifact is taken from it.
var ErrNotReady = errors.New("bleveindex: index is not ready")

// ErrNotQuiescent: CopyTo while marks are pending. The generation does not move
// when the reconciler indexes a mark, so an artifact stamped now would be
// accepted after the drain with a content that lags its source. Retry after a
// reconcile pass.
var ErrNotQuiescent = errors.New("bleveindex: index has pending dirty marks")

// ErrAlreadyOpen: the index is already open in this engine.
var ErrAlreadyOpen = errors.New("bleveindex: index already open")

// ErrNotOpen: the engine holds no open index of that name.
var ErrNotOpen = errors.New("bleveindex: index not open")

// Options configure an Engine. Dir, Root and Derived are required.
type Options struct {
	Dir      string                                             // <db parent>/index, absolute; a parameter, not derived from the store path, so a store on an in-memory filesystem still has a real index directory
	Root     storage.Keyspace                                   // c/index/ (index.Owner)
	Derived  func(module, name string) (storage.Derived, error) // the module's generation handle for an index; the kernel's db.Derived
	Logger   *slog.Logger                                       // nil discards
	Workers  func() int                                         // core.index.reconcile.workers; nil = runtime.NumCPU(); read on every pass, so a hot reload applies
	Interval func() time.Duration                               // core.index.reconcile.interval; nil = DefaultInterval; read on every wait
	Chunk    int                                                // marks per bleve batch; 0 = 64

	SearchTimeout    time.Duration // bounds a Search, Count or Facets whose context has no deadline; 0 = DefaultSearchTimeout
	MaxTermSearchers int           // leaf term searchers one search may create; 0 = DefaultMaxTermSearchers
	PassBudget       time.Duration // time one reconcile pass spends on one index before yielding; 0 = DefaultPassBudget
}

// Engine opens indexes and reconciles them. Its methods are safe for
// concurrent use.
type Engine struct {
	o   Options
	log *slog.Logger

	mu      sync.Mutex
	idxs    map[string]*idx     // "<module>/<name>", OPEN indexes only: an index is published here after its open finished
	opening map[string]struct{} // keys being opened, the duplicate guard

	wake chan struct{} // buffered(1): a MarkDirty nudge

	runMu   sync.Mutex
	running bool
	stopped bool
	stop    chan struct{}
	done    chan struct{}

	passes atomic.Int64
	// duringOpen is a test seam, nil in production: idx.open calls it first, the
	// window in which the index must not be visible to the reconciler.
	duringOpen func(*idx)
	// yielded is set when a pass left marks behind because an index used up its
	// budget: Run starts the next pass at once instead of waiting.
	yielded atomic.Bool
}

// New builds an Engine. It opens nothing and writes nothing.
func New(o Options) (*Engine, error) {
	switch {
	case o.Dir == "" || !filepath.IsAbs(o.Dir):
		return nil, errors.New("bleveindex: Options.Dir must be an absolute path")
	case o.Root.Owner() != index.Owner:
		return nil, fmt.Errorf("bleveindex: Options.Root must be the %s keyspace", index.Owner)
	case o.Derived == nil:
		return nil, errors.New("bleveindex: Options.Derived is required")
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Engine{
		o:       o,
		log:     log,
		idxs:    map[string]*idx{},
		opening: map[string]struct{}{},
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

func (e *Engine) workers() int {
	if e.o.Workers != nil {
		if n := e.o.Workers(); n > 0 {
			return n
		}
	}
	return max(runtime.NumCPU(), 1)
}

func (e *Engine) interval() time.Duration {
	if e.o.Interval != nil {
		if d := e.o.Interval(); d > 0 {
			return d
		}
	}
	return DefaultInterval
}

func (e *Engine) searchTimeout() time.Duration {
	if e.o.SearchTimeout > 0 {
		return e.o.SearchTimeout
	}
	return DefaultSearchTimeout
}

func (e *Engine) maxTermSearchers() int {
	if e.o.MaxTermSearchers > 0 {
		return e.o.MaxTermSearchers
	}
	return DefaultMaxTermSearchers
}

func (e *Engine) passBudget() time.Duration {
	if e.o.PassBudget > 0 {
		return e.o.PassBudget
	}
	return DefaultPassBudget
}

func (e *Engine) chunk() int {
	if e.o.Chunk > 0 {
		return e.o.Chunk
	}
	return defaultChunk
}

// nudge wakes the reconciler without blocking.
func (e *Engine) nudge() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Opener is the module-scoped index.Opener the kernel hands to
// module.StorageHandles.Indexes (F7-09 wires it).
func (e *Engine) Opener(module string) index.Opener { return opener{e: e, module: module} }

type opener struct {
	e      *Engine
	module string
}

// Open opens or creates the module's index s. A failure of the index itself
// (a corrupt directory, an unwritable disk) is a Degraded index and a nil
// error (I4); an invalid Spec, an index already open and a closed engine are
// errors.
func (o opener) Open(ctx context.Context, s index.Spec) (index.Index, error) {
	return o.e.open(ctx, o.module, s)
}

func (e *Engine) open(ctx context.Context, module string, s index.Spec) (index.Index, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if err := keys.ValidName(module); err != nil {
		return nil, fmt.Errorf("bleveindex: module name: %w", err)
	}
	if _, err := buildMapping(s); err != nil { // refuse an unknown analyzer before touching the disk
		return nil, err
	}
	derived, err := e.o.Derived(module, s.Name)
	if err != nil {
		return nil, fmt.Errorf("bleveindex: generation handle of %s/%s: %w", module, s.Name, err)
	}
	dirty, err := index.NewDirtySet(e.o.Root, derived, module, s.Name)
	if err != nil {
		return nil, err
	}
	i := &idx{
		e: e, module: module, name: s.Name, spec: s, derived: derived, dirty: dirty,
		path:  filepath.Join(e.o.Dir, module, s.Name+".bleve"),
		kinds: make(map[string]index.FieldKind, len(s.Fields)),
		log:   e.log,
	}
	for _, f := range s.Fields {
		i.kinds[f.Name] = f.Kind
		if f.Store {
			i.stored = append(i.stored, f.Name)
		}
	}
	i.cb.log = func(err error, path string, total int64) {
		e.log.Error("index background error (scorch merger or persister)",
			"module", module, "index", s.Name, "path", path, "err", err, "total_async_errors", total)
	}
	key := module + "/" + s.Name
	e.mu.Lock()
	_, dup := e.idxs[key]
	if _, busy := e.opening[key]; dup || busy {
		e.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrAlreadyOpen, key)
	}
	e.opening[key] = struct{}{} // reserved before the (slow) open, so a second Open cannot race it
	e.mu.Unlock()
	// The open runs before the index is published: the reconciler, Stats and
	// CopyTo only ever see an index whose handle and state are final, and the
	// mutex hand-off below orders every write open() made before their reads.
	i.open()
	e.mu.Lock()
	delete(e.opening, key)
	e.idxs[key] = i
	e.mu.Unlock()
	e.nudge() // a pending mark of a freshly opened index drains without waiting a tick
	return i, nil
}

// forget removes a closed index from the engine.
func (e *Engine) forget(i *idx) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.idxs[i.module+"/"+i.name] == i {
		delete(e.idxs, i.module+"/"+i.name)
	}
}

func (e *Engine) lookup(module, name string) (*idx, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.idxs[module+"/"+name]
	if i == nil {
		return nil, fmt.Errorf("%w: %s/%s", ErrNotOpen, module, name)
	}
	return i, nil
}

// snapshot is the open indexes in a stable order.
func (e *Engine) snapshot() []*idx {
	e.mu.Lock()
	out := make([]*idx, 0, len(e.idxs))
	for _, i := range e.idxs {
		if i.State() == index.StateInvalid {
			continue // not opened yet (never published that way; a guard, not a rule)
		}
		out = append(out, i)
	}
	e.mu.Unlock()
	slices.SortFunc(out, func(a, b *idx) int {
		if c := compareStr(a.module, b.module); c != 0 {
			return c
		}
		return compareStr(a.name, b.name)
	})
	return out
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Ref names one index.
type Ref struct{ Module, Index string }

// PendingRebuilds lists the open indexes that need a full rebuild: those
// Rebuilding that have not had one run to completion in this process (a fresh
// index, one recreated for a mapping change, or one whose rebuild a restart
// interrupted), and every Degraded one, because the rebuild is what recreates
// an index that would not open. The kernel enqueues RebuildKind for each at Run
// start (F7-09).
func (e *Engine) PendingRebuilds() []Ref {
	var out []Ref
	for _, i := range e.snapshot() {
		if st := i.State(); st == index.Degraded || (st == index.Rebuilding && !i.rebuilt.Load()) {
			out = append(out, Ref{i.module, i.name})
		}
	}
	return out
}

// IndexStats is one index's gauges: the series of 05-storage §5S.8
// (mediacore_index_docs, _dirty and _reconcile_lag_seconds) plus the state and
// the failure counters. The kernel registers the metrics from it (F7-09).
type IndexStats struct {
	Module, Index string
	State         index.State
	Docs          uint64
	Dirty         int
	ReconcileLag  time.Duration
	AsyncErrors   int64 // scorch merger or persister failures
	Failures      int64 // documents a reconcile pass could not load or index
}

// Stats samples every open index.
func (e *Engine) Stats(ctx context.Context) []IndexStats {
	now := time.Now()
	var out []IndexStats
	for _, i := range e.snapshot() {
		n, err := i.dirty.Count(ctx)
		if err != nil {
			n = -1
		}
		out = append(out, IndexStats{
			Module: i.module, Index: i.name, State: i.State(), Docs: i.docCount(), Dirty: n,
			ReconcileLag: i.lag(now), AsyncErrors: i.cb.errors.Load(), Failures: i.failures.Load(),
		})
	}
	return out
}

// CopyTo takes the online copy of an open index into dir (see idx.CopyTo).
func (e *Engine) CopyTo(ctx context.Context, module, name, dir string) error {
	i, err := e.lookup(module, name)
	if err != nil {
		return err
	}
	return i.CopyTo(ctx, dir)
}

// Close closes every open index. The kernel calls it after the reconciler has
// stopped.
func (e *Engine) Close() error {
	var errs []error
	for _, i := range e.snapshot() {
		errs = append(errs, i.Close())
	}
	return errors.Join(errs...)
}
