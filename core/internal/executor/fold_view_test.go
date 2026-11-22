// file: core/internal/executor/fold_view_test.go
// version: 0.1.0
// guid: 3e9b6a1d-84c2-4f07-b5de-61a09c7e2f48
// last-edited: 2026-11-22

package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// countingRuns counts Queued reads of the run store.
type countingRuns struct {
	tasks.RunStore
	queued atomic.Int64
}

func (c *countingRuns) Queued(ctx context.Context) iter.Seq2[tasks.RunRecord, error] {
	c.queued.Add(1)
	return c.RunStore.Queued(ctx)
}

// foldRig boots an executor that is not running (queued runs stay queued)
// over a counting run store, with one kind folding under pol.
func foldRig(t *testing.T, pol tasks.DedupePolicy) (*harness, *countingRuns) {
	t.Helper()
	fs := vfs.NewCrashableMem()
	stores, ks := bootStores(t, fs)
	cr := &countingRuns{RunStore: stores.Runs}
	stores.Runs = cr
	def := taskDef("gotham.relocate", okBody, func(c *tasks.Common) { c.Dedupe = pol })
	h := onStores(t, fs, stores, ks, []tasks.Definition{def}, func(o *Options) {
		o.Catalog = catalogWith(t, "gotham:run")
		o.NewBatch = ks.NewIndexedBatch // the counting wrapper hides the store's own opener
	}, false)
	return h, cr
}

func foldParams(t testing.TB, book int) json.RawMessage {
	t.Helper()
	return mustParams(t, fmt.Sprintf("book-%04d", book))
}

// TestStartIn_BulkFoldDecodesLinearly: N starts staged into one batch (a bulk
// organize's relocations) read the queue once and run Dedupe.Key once per
// queued run, not once per queued run per start (#4829 m4: O(N*M) decodes).
// The folds are what a per-start read would find: the first half of the batch
// repeats books that are already queued and folds into their runs, the second
// half is new.
func TestStartIn_BulkFoldDecodesLinearly(t *testing.T) {
	const queued, batch = 500, 500
	var keyCalls atomic.Int64
	key := func(raw json.RawMessage) (string, error) {
		keyCalls.Add(1)
		var p noParams
		err := json.Unmarshal(raw, &p)
		return p.Case, err
	}
	h, cr := foldRig(t, tasks.DedupePolicy{Key: key})
	ctx := context.Background()
	seeded := make([]tasks.RunID, queued)
	for i := range queued {
		ref, err := h.x.Start(ctx, "gotham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: foldParams(t, i)})
		if err != nil {
			t.Fatal(err)
		}
		seeded[i] = ref.ID
	}
	keyCalls.Store(0)
	cr.queued.Store(0)

	refs := make([]tasks.RunRef, batch)
	err := h.e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
		for j := range batch {
			ref, err := h.e.StartIn(ctx, b, "gotham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: foldParams(t, queued/2+j)})
			if err != nil {
				return err
			}
			refs[j] = ref
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for j, ref := range refs {
		book := queued/2 + j
		if book < queued {
			if !ref.Existing || ref.ID != seeded[book] {
				t.Fatalf("start %d (book %d, queued): %+v, want folded into %s", j, book, ref, seeded[book])
			}
		} else if ref.Existing {
			t.Fatalf("start %d (book %d, not queued): %+v, want a new run", j, book, ref)
		}
	}
	n := 0
	for _, err := range h.stores.Runs.Queued(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if want := queued + batch - queued/2; n != want {
		t.Fatalf("%d queued runs after the batch, want %d", n, want)
	}
	cr.queued.Add(-1) // the count above is this test's own read
	if got := cr.queued.Load(); got != 1 {
		t.Errorf("the batch read the queue %d times, want once", got)
	}
	// Linear: one Key for each incoming start plus one for each queued run. A
	// per-start read costs batch*queued = 250,000.
	if got, max := keyCalls.Load(), int64(batch+queued); got > max {
		t.Errorf("Dedupe.Key ran %d times for %d starts over %d queued runs, want at most %d", got, batch, queued, max)
	}
}

// TestStartIn_MergesInOneBatchChain: two starts staged into one batch that
// merge into the same queued run build on each other. The second merge sees
// the first's params and the run's next event number, not the committed
// record's (the per-start read it replaces handed both the committed record,
// so the second merge overwrote the first and both appended the same event).
func TestStartIn_MergesInOneBatchChain(t *testing.T) {
	var seen []string
	merge := func(cur, incoming json.RawMessage) (json.RawMessage, bool, error) {
		var c, in noParams
		if err := json.Unmarshal(cur, &c); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(incoming, &in); err != nil {
			return nil, false, err
		}
		seen = append(seen, c.Case)
		out, err := json.Marshal(noParams{Case: c.Case + "+" + in.Case})
		return out, true, err
	}
	h, _ := foldRig(t, tasks.DedupePolicy{Merge: merge})
	ctx := context.Background()
	ref, err := h.x.Start(ctx, "gotham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, "arkham")})
	if err != nil {
		t.Fatal(err)
	}
	err = h.e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
		for _, c := range []string{"blackgate", "gotham-pd"} {
			got, err := h.e.StartIn(ctx, b, "gotham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, c)})
			if err != nil {
				return err
			}
			if !got.Existing || got.ID != ref.ID {
				return fmt.Errorf("start %s: %+v, want merged into %s", c, got, ref.ID)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "arkham|arkham+blackgate"; strings.Join(seen, "|") != want {
		t.Fatalf("Merge saw existing params %q, want %q", strings.Join(seen, "|"), want)
	}
	var p noParams
	if err := json.Unmarshal(h.get(ref.ID).Params, &p); err != nil || p.Case != "arkham+blackgate+gotham-pd" {
		t.Fatalf("stored params %+v (%v), want both merges kept", p, err)
	}
	merges := 0
	seq := map[uint64]bool{}
	for p, err := range h.stores.Runs.Pages(ctx, ref.ID, tasks.PageQuery{Page: tasks.PageEvents, Limit: 100}) {
		if err != nil {
			t.Fatal(err)
		}
		if p.Event.Event == "merge" {
			merges++
			if seq[p.Seq] {
				t.Fatalf("two merge events share seq %d", p.Seq)
			}
			seq[p.Seq] = true
		}
	}
	if merges != 2 {
		t.Fatalf("%d merge events, want 2", merges)
	}
}

// TestStartIn_EachBatchReadsTheQueueAfresh: a batch's view is its own. A run
// that an earlier batch committed is found by the next batch's start (a view
// shared across batches would hand it the queue as of the first read), and a
// run a batch inserted is not visible to that same batch (the same as a
// per-start read of the store, which does not see an uncommitted batch).
func TestStartIn_EachBatchReadsTheQueueAfresh(t *testing.T) {
	key := func(raw json.RawMessage) (string, error) {
		var p noParams
		err := json.Unmarshal(raw, &p)
		return p.Case, err
	}
	h, _ := foldRig(t, tasks.DedupePolicy{Key: key})
	ctx := context.Background()
	stage := func(books ...string) []tasks.RunRef {
		var refs []tasks.RunRef
		err := h.e.write(ctx, func(ctx context.Context, b *storage.Batch) error {
			refs = nil
			for _, c := range books {
				ref, err := h.e.StartIn(ctx, b, "gotham.relocate", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, c)})
				if err != nil {
					return err
				}
				refs = append(refs, ref)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return refs
	}
	first := stage("scarecrow", "scarecrow")
	if first[0].Existing || first[1].Existing || first[0].ID == first[1].ID {
		t.Fatalf("one batch: %+v, want two new runs (an uncommitted insert is not visible to its own batch)", first)
	}
	second := stage("scarecrow")
	if !second[0].Existing || (second[0].ID != first[0].ID && second[0].ID != first[1].ID) {
		t.Fatalf("next batch: %+v, want folded into a run the first batch committed (%+v)", second[0], first)
	}
}
