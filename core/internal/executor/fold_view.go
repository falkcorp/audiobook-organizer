// file: core/internal/executor/fold_view.go
// version: 0.1.0
// guid: 7c0d2f4e-5b1a-4e8d-9a63-2f1b8c47d905
// last-edited: 2026-11-22

package executor

import (
	"encoding/json"
	"runtime"
	"sync"
	"weak"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// foldView is one batch's read of the queued runs for stageFold. A start made
// through StartIn stages into a batch the relay shares with every other
// delivery of that commit, so a bulk relocation stages N starts into one batch.
// Reading the queue afresh for each of them decodes every queued run (and runs
// the kind's Dedupe.Key on its params) N times over, which is O(N*M) decodes
// for M queued runs. The view reads the queue once per batch and computes each
// run's Dedupe.Key once per kind, so a batch of N starts costs M decodes plus
// N key computations on the incoming params.
//
// It is a snapshot taken at the batch's first fold, with the staleness the
// per-start read already had between its read and the commit: the commit's
// Expect checks (Transition's want) refuse a run that moved. It sees the runs
// committed when it was taken and the merges this batch staged into them
// (merge writes the merged record back), not the runs the batch inserted: that
// is what a per-start read of the store sees too, since the store's reads do
// not see an uncommitted batch.
type foldView struct {
	runs []tasks.RunRecord           // queued runs in dispatch order, undecodable ones left out (5T.5)
	keys map[tasks.Kind][]foldKeyVal // runs[i]'s Dedupe.Key for a kind, computed on first use
}

// foldKeyVal is a memoised Dedupe.Key result.
type foldKeyVal struct {
	done bool
	key  string
	err  error
}

// key returns runs[i]'s Dedupe.Key under fn, computing it once per kind.
func (v *foldView) key(kind tasks.Kind, i int, fn func(json.RawMessage) (string, error)) (string, error) {
	ks := v.keys[kind]
	if ks == nil {
		ks = make([]foldKeyVal, len(v.runs))
		v.keys[kind] = ks
	}
	if !ks[i].done {
		k, err := fn(v.runs[i].Params)
		ks[i] = foldKeyVal{done: true, key: k, err: err}
	}
	return ks[i].key, ks[i].err
}

// merged records that a merge rewrote runs[i], so the next start of the same
// batch merges into the merged record (its params and its next event number)
// and not into the snapshot's older copy. The memoised keys of i are dropped:
// the params changed.
func (v *foldView) merged(i int, next tasks.RunRecord) {
	v.runs[i] = next
	for _, ks := range v.keys {
		ks[i] = foldKeyVal{}
	}
}

// foldViews holds the views of the batches that are being staged into. An
// entry goes with its batch: it is keyed by a weak pointer and dropped by a
// cleanup when the batch is collected, so a view never outlives the work it
// serves by more than a collection.
type foldViews struct {
	mu sync.Mutex
	m  map[weak.Pointer[storage.Batch]]*foldView
}

// of returns b's view, reading the queue through load on b's first fold. load
// runs outside the lock (it reads the store); a batch is staged by one
// goroutine, so two loads for one batch cannot race.
func (f *foldViews) of(b *storage.Batch, load func() *foldView) *foldView {
	key := weak.Make(b)
	f.mu.Lock()
	v, ok := f.m[key]
	f.mu.Unlock()
	if ok {
		return v
	}
	v = load()
	f.mu.Lock()
	if f.m == nil {
		f.m = map[weak.Pointer[storage.Batch]]*foldView{}
	}
	f.m[key] = v
	f.mu.Unlock()
	runtime.AddCleanup(b, func(k weak.Pointer[storage.Batch]) {
		f.mu.Lock()
		delete(f.m, k)
		f.mu.Unlock()
	}, key)
	return v
}
