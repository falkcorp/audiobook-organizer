// file: core/internal/bleveindex/rebuild.go
// version: 1.0.0
// guid: 8e6dc1c5-df80-4fdf-80a8-bc517a0ac0f6
// last-edited: 2026-11-11

package bleveindex

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage/index"
	"github.com/falkcorp/mediacore/tasks"
)

// RebuildKind is the one task kind that fully rebuilds a full-text index from
// its module's storage. Its params name the index, so one definition serves
// every module's indexes (the kind is core's, not a module's: the spec names
// no kind for it, so this follows datamigrate.DerivedBuildTask's
// "<module>.derived.<name>.build" only in being declared data, not a hook).
const RebuildKind tasks.Kind = "core.index.rebuild"

// RebuildParams selects the index to rebuild.
type RebuildParams struct {
	Module string `json:"module"`
	Index  string `json:"index"`
}

// RebuildResult is what a rebuild reports.
type RebuildResult struct {
	Documents int64 `json:"documents"`
}

// rebuildChunk is how many documents one bleve batch of a rebuild holds.
const rebuildChunk = 256

// RebuildTask is the definition the kernel registers for RebuildKind (F7-09).
// The rebuild is a library-scale loop, so it declares its parallelism at the
// one place the work is: tasks.CPU(), because analysing and indexing a
// document is CPU-bound. The Spec's Rebuild callback only produces documents
// (and may scan its own storage in parallel: RebuildWriter.Put is safe for
// concurrent use); the pool of workers that analyse and write them is this
// task's, shared with the executor's CPU budget.
//
//tasklint:constructor the kernel passes it to its core task registration when it wires the index engine (F7-09); nothing in this row registers a kind
func (e *Engine) RebuildTask() tasks.Definition {
	return tasks.Task(RebuildKind, tasks.TaskSpec[RebuildParams, RebuildResult]{
		Title:   "Rebuild a full-text index",
		Help:    "Drops a module's full-text index and rebuilds it from the module's storage. Search falls back while it runs.",
		Effects: tasks.Writes(tasks.Res("core", "index")),
		Perms:   tasks.Permissions{Start: rbac.Need("core:system.manage")},
		Resume:  tasks.ResumeFromZero,
		Timeout: 24 * time.Hour,
		Run:     e.runRebuild,
	})
}

func (e *Engine) runRebuild(rc *tasks.RC, p RebuildParams) (RebuildResult, error) {
	if p.Module == "" || p.Index == "" {
		return RebuildResult{}, errors.New("bleveindex: rebuild needs a module and an index")
	}
	i, err := e.lookup(p.Module, p.Index)
	if err != nil {
		return RebuildResult{}, err
	}
	if i.State() == index.Degraded {
		return RebuildResult{}, fmt.Errorf("bleveindex: %s/%s: %w", p.Module, p.Index, index.ErrDegraded)
	}

	// The rebuild holds the index's writer lock for its whole run, so no
	// reconcile pass applies a mark while the index is dropped and refilled
	// (see idx.opMu). Marks written meanwhile stay in the durable dirty set and
	// drain after the rebuild, which converges: every write after the
	// rebuild's read of a document marked it.
	i.opMu.Lock()
	n, runErr := e.rebuildLocked(rc, i)
	i.opMu.Unlock()
	if runErr != nil {
		return RebuildResult{}, runErr
	}
	// The tail drain: apply what was marked during the rebuild, and leave
	// Rebuilding when nothing is pending.
	if err := e.drain(rc.Context(), i); err != nil {
		return RebuildResult{Documents: n}, fmt.Errorf("bleveindex: drain after rebuild: %w", err)
	}
	return RebuildResult{Documents: n}, nil
}

func (e *Engine) rebuildLocked(rc *tasks.RC, i *idx) (int64, error) {
	if err := i.recreate(); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithCancelCause(rc.Context())
	defer cancel(nil)

	par := tasks.CPU()
	ch := make(chan []entry, 2*par.Workers())
	w := &rebuildWriter{ctx: ctx, ch: ch}
	prod := make(chan error, 1)
	go func() {
		err := i.spec.Rebuild(ctx, w)
		if err == nil {
			err = w.flush()
		}
		close(ch)
		prod <- err
	}()
	seq := func(yield func([]entry) bool) {
		for c := range ch {
			if !yield(c) {
				cancel(context.Canceled)
				for range ch { // unblock the producer, which stops at the cancelled ctx
				}
				return
			}
		}
	}
	res, runErr := tasks.RunSeq(rc, par, seq, tasks.Unknown(),
		func(ic *tasks.ItemRC, c []entry) error {
			if err := i.applyBatch(c); err != nil {
				return err
			}
			ic.Count("documents", int64(len(c)))
			return nil
		})
	cancel(context.Canceled)
	for range ch {
	}
	prodErr := <-prod
	switch {
	case runErr != nil:
		return 0, fmt.Errorf("bleveindex: rebuild %s/%s: %w", i.module, i.name, runErr)
	case prodErr != nil && !errors.Is(prodErr, context.Canceled):
		return 0, fmt.Errorf("bleveindex: rebuild %s/%s: the Rebuild callback: %w", i.module, i.name, prodErr)
	case rc.Canceled():
		return 0, fmt.Errorf("bleveindex: rebuild %s/%s: %w", i.module, i.name, context.Cause(rc.Context()))
	}
	// Coverage is complete: from here a crash comes back as a normal index with
	// whatever marks are durable, so the rebuilding marker goes, and the state
	// stays Rebuilding in memory until a drain finds the dirty set empty.
	if err := os.Remove(rebuildingMarkerPath(i.path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("bleveindex: remove rebuilding marker: %w", err)
	}
	i.rebuilt.Store(true)
	return res.Counters["documents"], nil
}

// rebuildWriter is the index.RebuildWriter of one rebuild: it groups documents
// into chunks and hands each to the task's worker pool.
type rebuildWriter struct {
	ctx context.Context
	ch  chan<- []entry

	mu  sync.Mutex
	buf []entry
}

// Put queues one document. It is safe for concurrent use and returns the
// run's cancellation cause once the rebuild is cancelled.
func (w *rebuildWriter) Put(id string, d index.Doc) error {
	if id == "" {
		return errors.New("bleveindex: Put of an empty id")
	}
	if d == nil {
		return fmt.Errorf("bleveindex: Put of %q with a nil document (a deletion is not a rebuild)", id)
	}
	w.mu.Lock()
	// The callback may reuse its map, so the writer keeps its own copy.
	w.buf = append(w.buf, entry{id: id, doc: maps.Clone(d)})
	var full []entry
	if len(w.buf) >= rebuildChunk {
		full, w.buf = w.buf, nil
	}
	w.mu.Unlock()
	if full != nil {
		return w.send(full)
	}
	return nil
}

func (w *rebuildWriter) flush() error {
	w.mu.Lock()
	rest := w.buf
	w.buf = nil
	w.mu.Unlock()
	if len(rest) == 0 {
		return nil
	}
	return w.send(rest)
}

func (w *rebuildWriter) send(c []entry) error {
	select {
	case w.ch <- c:
		return nil
	case <-w.ctx.Done():
		return context.Cause(w.ctx)
	}
}
