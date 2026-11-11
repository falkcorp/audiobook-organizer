// file: core/internal/executor/batch.go
// version: 1.0.0
// guid: 9a863231-8741-4cda-83be-b9eddcb8c094
// last-edited: 2026-11-10

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// chunkState is a Batch run's chunk ledger as this attempt may use it:
// chunks a ResumeFromZero voided are left out.
type chunkState struct {
	byN      map[int]tasks.Chunk
	doneKeys map[string]bool // Snapshot + Key: keys of complete chunks
	maxN     int
}

func (a *attempt) loadChunks(ctx context.Context) (chunkState, error) {
	st := chunkState{byN: map[int]tasks.Chunk{}, doneKeys: map[string]bool{}, maxN: -1}
	for c, err := range a.e.runs.Chunks(ctx, a.id) {
		if err != nil {
			return st, err
		}
		if int(c.Counters[chunkAttempt]) < a.zeroFrom {
			continue
		}
		st.byN[c.N] = c
		st.maxN = max(st.maxN, c.N)
		if c.Complete {
			for _, k := range c.Keys {
				st.doneKeys[k] = true
			}
		}
	}
	return st, nil
}

// saveChunk stages one chunk-ledger entry through the per-run writer
// (closed-ledger rule), with the run's progress in the same batch.
func (a *attempt) saveChunk(ctx context.Context, c tasks.Chunk) error {
	c.Counters = maps.Clone(c.Counters)
	if c.Counters == nil {
		c.Counters = map[string]int64{}
	}
	c.Counters[chunkAttempt] = int64(a.n)
	c.At = a.e.now()
	return a.ledger(ctx, func(ctx context.Context, b *storage.Batch) error {
		return a.e.runs.SaveChunk(ctx, b, a.id, c)
	})
}

// recordItem stages a failed item's outcome (PageItems).
func (a *attempt) recordItem(ctx context.Context, key string, err error, chunk int) {
	if key == "" {
		return
	}
	o := tasks.ItemOutcome{Key: key, Outcome: "failed", Error: errText(err), Chunk: chunk}
	if lerr := a.ledger(ctx, func(ctx context.Context, b *storage.Batch) error {
		return a.e.runs.PutItem(ctx, b, a.id, key, o)
	}); lerr != nil && !errors.Is(lerr, tasks.ErrFenced) {
		a.log.Warn("item outcome not recorded", "item", key, "err", lerr)
	}
}

// itemsPar resolves a FromParam parallelism against the run's params; CPU,
// Fixed and Sequential pass through.
func (a *attempt) itemsPar(p tasks.Parallelism) tasks.Parallelism {
	if p.UsesCPUBudget() {
		return p
	}
	return tasks.Fixed(p.WorkersFor(a.rec.Params), p.String())
}

// batchTotals accumulates a Batch run's summary.
type batchTotals struct {
	mu       sync.Mutex
	sum      tasks.Summary
	failures atomic.Int64
}

func (t *batchTotals) add(c tasks.Chunk) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sum.Done += c.Done
	t.sum.Failed += c.Failed
	t.sum.Skipped += c.Skipped
	for k, v := range c.Counters {
		if k != chunkAttempt {
			t.sum.Counters[k] += v
		}
	}
	t.sum.ChunksDone++
}

// runBatch is the Batch runner (5T.3.2, 5T.3.6): the source is cut into
// chunks of ChunkSize; each chunk's items run through tasks.RunItems on the
// run's RC (so the CPU budget, pause gate, ref-lock table and parallelism
// cap apply), and a chunk whose every item ran is committed to the ledger
// with its counts. A resumed attempt skips complete chunks: by number for a
// slice source, by frozen keys for a Snapshot source with Key, by cursor
// for a paged source.
func (a *attempt) runBatch(rc *tasks.RC, p any) (json.RawMessage, error) {
	bs := a.d.batch
	ctx := rc.Context()
	src, err := bs.Source(rc, p)
	if err != nil {
		return nil, err
	}
	if _, _, dirty := src.Dirty(); dirty {
		return nil, ErrDirtySource
	}
	size := bs.ChunkSize
	if size <= 0 {
		size = tasks.DefaultChunkSize
	}
	ledger, err := a.loadChunks(ctx)
	if err != nil {
		return nil, err
	}
	tot := &batchTotals{sum: tasks.Summary{Counters: map[string]int64{}}}
	if n, ok := src.Total.Value(); ok {
		tot.sum.Total = n
	} else if src.Items != nil {
		tot.sum.Total = int64(len(src.Items))
	}
	mode := tasks.Collect
	if bs.OnItemError == tasks.BatchStop {
		mode = tasks.FailFast
	}
	key := func(it any) string {
		switch {
		case src.Key != nil:
			return src.Key(it)
		case bs.Label != nil:
			return bs.Label(it)
		}
		return ""
	}
	par := a.itemsPar(bs.Parallelism)
	run := func(n int, items []any, c tasks.Chunk) (bool, error) {
		itemFn := func(ic *tasks.ItemRC, it any) error {
			err := bs.Item(ic, p, it)
			if errors.Is(err, tasks.ErrPreview) {
				err = nil
			}
			if err != nil {
				tot.failures.Add(1)
				a.recordItem(ctx, key(it), err, n)
			}
			return err
		}
		tot.mu.Lock()
		base := int(tot.sum.Done + tot.sum.Failed + tot.sum.Skipped)
		total := int(tot.sum.Total)
		tot.mu.Unlock()
		opts := tasks.ItemsOptions[any]{OnError: mode, ItemTimeout: bs.ItemTimeout, ExclusiveRefs: bs.ExclusiveRefs, Label: bs.Label,
			ProgressBase: base, ProgressTotal: total}
		var res tasks.ItemsResult
		if bs.PartitionBy != nil {
			res = a.runPartitioned(rc, par, items, itemFn, opts, bs.PartitionBy)
		} else {
			res, _ = tasks.RunItems(rc, par, items, itemFn, opts)
		}
		if ctx.Err() != nil || res.NotStarted > 0 {
			return true, nil // incomplete: redone on resume
		}
		c.N, c.Complete = n, true
		c.Done, c.Failed, c.Skipped, c.Counters = res.Done, res.Failed, res.Skipped, res.Counters
		if err := a.saveChunk(ctx, c); err != nil {
			return true, err
		}
		tot.add(c)
		a.ItemsProgress(tot.sum.Done+tot.sum.Failed+tot.sum.Skipped, tot.sum.Total, "")
		return mode == tasks.FailFast && res.Failed > 0, nil
	}
	stop, err := a.batchChunks(ctx, src, size, ledger, tot, key, run)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	tot.sum.ChunksTotal = tot.sum.ChunksDone
	if stop && bs.OnItemError == tasks.BatchStop {
		return nil, fmt.Errorf("executor: %s: an item failed and OnItemError is BatchStop", a.d.kind)
	}
	if bs.Finish != nil {
		return bs.Finish(rc, p, tot.sum)
	}
	return json.Marshal(tot.sum)
}

// batchChunks walks the source chunk by chunk, skipping what the ledger
// says is complete.
func (a *attempt) batchChunks(ctx context.Context, src tasks.Source[any], size int, ledger chunkState, tot *batchTotals,
	key func(any) string, run func(n int, items []any, c tasks.Chunk) (bool, error)) (bool, error) {
	switch {
	case src.Pages != nil:
		after, n := "", 0
		for {
			c, ok := ledger.byN[n]
			if !ok || !c.Complete {
				break
			}
			tot.add(c)
			after = c.Last
			n++
			if after == "" {
				return false, nil
			}
		}
		for ctx.Err() == nil {
			items, next, err := src.Pages(ctx, after, size)
			if err != nil {
				return false, err
			}
			if len(items) == 0 {
				return false, nil
			}
			stop, err := run(n, items, tasks.Chunk{First: after, Last: next})
			if err != nil || stop {
				return stop, err
			}
			if next == "" {
				return false, nil
			}
			after = next
			n++
		}
		return false, nil
	case src.Order == tasks.Snapshot && src.Key != nil:
		var rest []any
		for _, it := range src.Items {
			if !ledger.doneKeys[key(it)] {
				rest = append(rest, it)
			}
		}
		for _, c := range ledger.byN {
			if c.Complete {
				tot.add(c)
			}
		}
		n := ledger.maxN + 1
		for i := 0; i < len(rest) && ctx.Err() == nil; i += size {
			items := rest[i:min(i+size, len(rest))]
			keys := make([]string, len(items))
			for j, it := range items {
				keys[j] = key(it)
			}
			// Freeze the chunk's keys before it runs (the item-key snapshot).
			if err := a.saveChunk(ctx, tasks.Chunk{N: n, Keys: keys}); err != nil {
				return true, err
			}
			stop, err := run(n, items, tasks.Chunk{Keys: keys})
			if err != nil || stop {
				return stop, err
			}
			n++
		}
		return false, nil
	default:
		for i, n := 0, 0; i < len(src.Items) && ctx.Err() == nil; i, n = i+size, n+1 {
			if c, ok := ledger.byN[n]; ok && c.Complete {
				tot.add(c)
				continue
			}
			stop, err := run(n, src.Items[i:min(i+size, len(src.Items))], tasks.Chunk{})
			if err != nil || stop {
				return stop, err
			}
		}
		return false, nil
	}
}

// runPartitioned is PartitionBy: items with one partition key run on one
// worker, in order; distinct keys run in parallel under the run's
// parallelism. Counts are per item.
func (a *attempt) runPartitioned(rc *tasks.RC, par tasks.Parallelism, items []any,
	fn func(ic *tasks.ItemRC, it any) error, opts tasks.ItemsOptions[any], part func(any) string) tasks.ItemsResult {
	var order []string
	groups := map[string][]any{}
	for _, it := range items {
		k := part(it)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], it)
	}
	gs := make([][]any, len(order))
	for i, k := range order {
		gs[i] = groups[k]
	}
	var done, failed atomic.Int64
	gopts := tasks.ItemsOptions[[]any]{OnError: opts.OnError, ItemTimeout: opts.ItemTimeout}
	res, _ := tasks.RunItems(rc, par, gs, func(ic *tasks.ItemRC, g []any) error {
		var first error
		for _, it := range g {
			if err := fn(ic, it); err != nil {
				failed.Add(1)
				if first == nil {
					first = err
				}
				if opts.OnError == tasks.FailFast {
					return first
				}
				continue
			}
			done.Add(1)
		}
		return first
	}, gopts)
	out := tasks.ItemsResult{Done: done.Load(), Failed: failed.Load(), Counters: res.Counters}
	out.NotStarted = int64(len(items)) - out.Done - out.Failed
	return out
}
