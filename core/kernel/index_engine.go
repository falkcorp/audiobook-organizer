// file: core/kernel/index_engine.go
// version: 0.1.0
// guid: 3b7d1c52-9e48-4a06-8f2d-5c1a7e90b634
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/bleveindex"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
)

// The full-text index engine's kernel half (05-storage §5S.3.8, 06 §6.5.2):
// the c/index/ owner the store opens with, the engine built over it, the
// module-scoped openers the SDK hands module.StorageHandles.Indexes, and the
// reconciler's start and stop.
//
// Dark, like derived.go: free functions over explicit inputs. Nothing in boot.go,
// kernel.go or the supervisor calls them until F7-09's flip, which adds:
//
//   - indexOwnerSpec() to swap.go's storageOwners, so c/index/ exists (P2/P4a);
//   - buildIndexEngine at P4a, once the store exists, with Dir = <db parent>/index;
//   - indexOpenerFor(engine, id) as each enabled module's StorageHandles opener;
//   - startIndex after module hooks and workers, before the event bus opens, and
//     stopIndex in the reverse position of the stop order (06 §6.5.2);
//   - Engine.RebuildTask() as the core rebuild definition, which is B3-a's.

// indexOwnerID is the core owner of c/index/: the dirty sets of every
// module's indexes (index.Owner).
const indexOwnerID = index.Owner

// indexOwnerSpec is the OwnerSpec the store opens with for c/index/: a core
// owner, always enabled, with the Serialized keyspace the dirty sets need
// (index.StorageSpec).
func indexOwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: indexOwnerID, Spec: index.StorageSpec(), Enabled: true}
}

// indexSettingsSource is what the engine reads core.index through: the
// section's config handle (Get is one atomic load of the current snapshot).
type indexSettingsSource interface{ Get() *indexSettings }

var _ indexSettingsSource = (*config.Handle[indexSettings])(nil)

// indexInputs are what the engine is built from.
type indexInputs struct {
	// DB is the store, after Prepare. It must have been opened with
	// indexOwnerSpec among its owners.
	DB *storage.DB
	// Dir is the index directory, <db parent>/index, absolute. It is not
	// derived from the store's path: a store on an in-memory file system
	// (a test's) still has a real index directory.
	Dir string
	// Settings is the core.index section: a boot passes the section's
	// *config.Handle[indexSettings], so a hot reload of workers or interval
	// applies on the reconciler's next pass or wait. An untyped nil keeps the
	// engine's defaults, for a caller with no config store.
	Settings indexSettingsSource
	// Log is the engine's logger; nil discards.
	Log *slog.Logger
}

// indexOptions is the engine's configuration for in: the c/index/ keyspace, the
// store's generation handles, and core.index.reconcile.{workers,interval} read
// live through the section handle (the engine reads both on every pass or wait).
func indexOptions(in indexInputs) (bleveindex.Options, error) {
	if in.DB == nil {
		return bleveindex.Options{}, errors.New("kernel: index engine: no storage handle (build it after P4a)")
	}
	if in.Dir == "" || !filepath.IsAbs(in.Dir) {
		return bleveindex.Options{}, fmt.Errorf("kernel: index engine: the directory %q is not absolute", in.Dir)
	}
	root, ok := in.DB.Keyspace(indexOwnerID)
	if !ok {
		return bleveindex.Options{}, fmt.Errorf("kernel: index engine: the store has no %s keyspace (indexOwnerSpec was not among the owners it opened with)", indexOwnerID)
	}
	db := in.DB
	o := bleveindex.Options{
		Dir:     in.Dir,
		Root:    root,
		Derived: func(module, name string) (storage.Derived, error) { return db.Derived(storage.Owner(module), name) },
		Logger:  in.Log,
	}
	if src := in.Settings; src != nil {
		o.Workers = func() int { return src.Get().Reconcile.Workers }
		o.Interval = func() time.Duration { return src.Get().Reconcile.Interval.Std() }
	}
	return o, nil
}

// buildIndexEngine builds the engine over the store. It opens nothing and
// writes nothing (bleveindex.New): the indexes open when a module's opener
// opens them, and the reconciler runs when startIndex starts it.
func buildIndexEngine(in indexInputs) (*bleveindex.Engine, error) {
	o, err := indexOptions(in)
	if err != nil {
		return nil, err
	}
	e, err := bleveindex.New(o)
	if err != nil {
		return nil, fmt.Errorf("kernel: index engine: %w", err)
	}
	return e, nil
}

// indexOpenerFor is module's index.Opener: an index it opens lives at
// <dir>/<module>/<name>.bleve, takes its generation handle from the module's
// own Spec.Derived declaration (an index the module did not declare is
// refused), and keeps its dirty set under c/index/<module>/<name>/. The module
// is the opener's, never the caller's argument, so a module cannot open
// another's index.
func indexOpenerFor(e *bleveindex.Engine, module string) index.Opener { return e.Opener(module) }

// indexRunner is a started reconciler: what startIndex returns and stopIndex
// takes.
type indexRunner struct {
	e        *bleveindex.Engine
	done     chan struct{}
	stopping atomic.Bool
}

// startIndex starts e's reconciler in its own goroutine and returns at once
// (the engine's Run blocks). ctx bounds the reconciler's life: its end is the
// same as stopIndex, without the wait. The reconciler drains the dirty marks
// of every index a module has opened, now and later; one that opens after
// the start is picked up by the engine's nudge, not by a restart.
func startIndex(ctx context.Context, e *bleveindex.Engine, log *slog.Logger) *indexRunner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &indexRunner{e: e, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		// Run returns an error only for a second start or a start after Stop.
		// After stopIndex that is the race between the two (stopIndex ran before
		// this goroutine did), and says nothing.
		if err := e.Run(ctx); err != nil && !r.stopping.Load() {
			log.ErrorContext(ctx, "kernel: the index reconciler did not start", "err", err)
		}
	}()
	return r
}

// stopIndex ends the reconciler and waits for it to return, up to ctx: the
// engine's Stop, then the goroutine startIndex started. It is safe to call
// twice, and before the goroutine began. It does not close the engine or its
// indexes (Engine.Close, at the kernel's close step).
func stopIndex(ctx context.Context, r *indexRunner) error {
	if r == nil {
		return nil
	}
	r.stopping.Store(true)
	if err := r.e.Stop(ctx); err != nil {
		return fmt.Errorf("kernel: stop the index reconciler: %w", err)
	}
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("kernel: stop the index reconciler: %w", context.Cause(ctx))
	}
}
