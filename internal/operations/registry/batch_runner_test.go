// file: internal/operations/registry/batch_runner_test.go
// version: 1.5.1
// guid: 398171ae-de60-4dbe-9766-d9819c610ace
// last-edited: 2026-10-20

package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// memLedgerStore is an in-memory batchStore. It fails the test when two
// goroutines commit at once, or when commits come from more than one
// goroutine within one run.
type memLedgerStore struct {
	t          *testing.T
	mu         sync.Mutex
	snap       map[int][]string
	offsets    []int32
	hasOffsets bool
	ledger     *database.Ledger
	commits    []database.LedgerBatch
	commitAt   []time.Time
	inFlight   atomic.Int32
	committers map[uint64]bool
	marks      map[string]map[string]bool // name -> key set (opv3:mark:)
}

func newMemLedgerStore(t *testing.T) *memLedgerStore {
	return &memLedgerStore{t: t, snap: map[int][]string{}, committers: map[uint64]bool{}, marks: map[string]map[string]bool{}}
}

func (s *memLedgerStore) ListMarksV3(string) (map[string][]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]string{}
	for name, set := range s.marks {
		for k := range set {
			out[name] = append(out[name], k)
		}
		slices.Sort(out[name])
	}
	return out, nil
}

func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	f := bytes.Fields(buf[:n])
	id, _ := strconv.ParseUint(string(f[1]), 10, 64)
	return id
}

func (s *memLedgerStore) PutSnapshotPageV3(_ string, page int, keys []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap[page] = slices.Clone(keys)
	return nil
}

func (s *memLedgerStore) GetSnapshotPageV3(_ string, page int) ([]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.snap[page]
	return slices.Clone(k), ok, nil
}

func (s *memLedgerStore) PutChunkOffsetsV3(_ string, o []int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets, s.hasOffsets = slices.Clone(o), true
	return nil
}

func (s *memLedgerStore) GetChunkOffsetsV3(string) ([]int32, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.offsets), s.hasOffsets, nil
}

func (s *memLedgerStore) CommitLedgerV3(_ string, b database.LedgerBatch) error {
	if s.inFlight.Add(1) > 1 {
		s.t.Error("two goroutines are inside CommitLedgerV3 at once")
	}
	defer s.inFlight.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.committers[goroutineID()] = true
	l := b.Ledger
	s.ledger = &l
	if b.ResetMarks {
		s.marks = map[string]map[string]bool{}
	}
	for name, keys := range b.Marks {
		if s.marks[name] == nil {
			s.marks[name] = map[string]bool{}
		}
		for _, k := range keys {
			s.marks[name][k] = true
		}
	}
	s.commits = append(s.commits, b)
	s.commitAt = append(s.commitAt, time.Now())
	return nil
}

func (s *memLedgerStore) GetLedgerV3(string) (*database.Ledger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ledger == nil {
		return nil, nil
	}
	l := *s.ledger
	return &l, nil
}

// newAttempt forgets which goroutine committed (a new attempt has a new
// ledger goroutine).
func (s *memLedgerStore) newAttempt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.committers = map[uint64]bool{}
	s.commits, s.commitAt = nil, nil
}

func (s *memLedgerStore) committerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.committers)
}

func (s *memLedgerStore) commitCopy() []database.LedgerBatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commits)
}

// itemKey sorts in item order, so a source of items 0..n-1 ascends.
func itemKey(i int) string { return fmt.Sprintf("%06d", i) }

// counts records executions per item, per attempt.
type counts struct {
	mu   sync.Mutex
	runs map[int]int
}

func (c *counts) add(i int) {
	c.mu.Lock()
	c.runs[i]++
	c.mu.Unlock()
}

func (c *counts) get(i int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[i]
}

func newCounts() *counts { return &counts{runs: map[int]int{}} }

type batchOpts struct {
	order     ops.SourceOrder
	size      int
	workers   int
	partition func(int) string
	label     func(int) string
	stop      bool
	timeout   time.Duration
	resume    *ops.ResumePolicy
	finish    func(*ops.RC, ops.Summary)
}

// openJob compiles an ops.Batch over items and opens one run of it.
func openJob(t *testing.T, items []int, o batchOpts, item func(*ops.ItemRC, int) error) (*ops.BatchJob, *ops.RC, context.CancelFunc) {
	t.Helper()
	src := ops.Source[int]{Key: itemKey, Order: o.order}
	if o.order == ops.AppendOnly || o.order == ops.Unordered {
		all := slices.Clone(items)
		src.Pages = func(_ context.Context, after string, limit int) ([]int, string, error) {
			start := 0
			if after != "" {
				start, _ = strconv.Atoi(after)
			}
			end := min(start+limit, len(all))
			next := ""
			if end < len(all) {
				next = strconv.Itoa(end)
			}
			return all[start:end], next, nil
		}
	} else {
		src.Items = items
	}
	def := ops.Batch("test.batch", ops.BatchSpec[ops.NoParams, int, ops.NoResult]{
		Title:      "t",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		Resume:     o.resume,
		Source:     func(*ops.RC, ops.NoParams) (ops.Source[int], error) { return src, nil },
		Item: func(rc *ops.ItemRC, _ ops.NoParams, i int) error {
			return item(rc, i)
		},
		Finish: func(rc *ops.RC, _ ops.NoParams, s ops.Summary) (ops.NoResult, error) {
			if o.finish != nil {
				o.finish(rc, s)
			}
			return ops.NoResult{}, nil
		},
		Concurrency: ops.Network(max(o.workers, 1), "test"),
		ChunkSize:   o.size, PartitionBy: o.partition, Label: o.label, ItemTimeout: o.timeout,
		OnItemError: map[bool]ops.ErrorPolicy{true: ops.Stop, false: ops.Continue}[o.stop],
	})
	ctx, cancel := context.WithCancel(context.Background())
	rc := ops.NewRC(ops.RunEnv{Context: ctx, OpID: "op-test", DefID: "test.batch", Mode: ops.Live})
	job, err := ops.Compile(def).OpenBatch(rc, nil)
	if err != nil {
		t.Fatalf("OpenBatch: %v", err)
	}
	return job, rc, cancel
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func bitsSet(l database.Ledger) []int {
	var out []int
	for c := range l.ChunkCount {
		if l.Done[c/8]&(1<<(c%8)) != 0 {
			out = append(out, c)
		}
	}
	return out
}

// TestChunkLeasing_ResumeLeasesExactlyUnfinished is examples.md Example 2's
// worked resume: 1,000 items, chunks 0 and 2 complete, the world stops while
// chunk 1 is at item 100 and chunk 3 at item 40; the resume leases exactly
// the unfinished chunks from their first item and starts at 512, not 652.
func TestChunkLeasing_ResumeLeasesExactlyUnfinished(t *testing.T) {
	for _, size := range []int{256, 3} {
		t.Run(fmt.Sprintf("chunk=%d", size), func(t *testing.T) {
			store := newMemLedgerStore(t)
			items := seq(1000)
			first, second := newCounts(), newCounts()
			stopAt := map[int]int{1: 100, 3: 40} // chunk -> item index it is at
			job, rc, cancel := openJob(t, items, batchOpts{size: size, workers: 8}, func(irc *ops.ItemRC, i int) error {
				first.add(i)
				c, j := i/size, i%size
				if c == 0 || c == 2 {
					return nil
				}
				if at, ok := stopAt[c]; ok && j < at && size == 256 {
					return nil
				}
				<-irc.Context().Done() // in flight when the process dies
				return irc.Context().Err()
			})
			defer cancel()
			var atCrash database.ProgressSnapshot
			_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1,
				afterCommit: func(l database.Ledger, p database.ProgressSnapshot) bool {
					if slices.Equal(bitsSet(l), []int{0, 2}) {
						atCrash = p
						return true
					}
					return false
				}})
			if !errors.Is(err, errWorldStopped) {
				t.Fatalf("first attempt: %v", err)
			}
			if atCrash.Done != int64(2*size) {
				t.Fatalf("Done at the crash = %d, want %d (in-flight items are not counted)", atCrash.Done, 2*size)
			}

			store.newAttempt()
			var leased []int
			var leasedMu sync.Mutex
			job2, rc2, cancel2 := openJob(t, items, batchOpts{size: size, workers: 8}, func(_ *ops.ItemRC, i int) error {
				second.add(i)
				return nil
			})
			defer cancel2()
			sum, _, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1,
				onLease: func(c int) { leasedMu.Lock(); leased = append(leased, c); leasedMu.Unlock() }})
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			nChunks := (1000 + size - 1) / size
			var want []int
			for c := range nChunks {
				if c != 0 && c != 2 {
					want = append(want, c)
				}
			}
			slices.Sort(leased)
			if !slices.Equal(leased, want) {
				t.Fatalf("resume leased %v..., want every chunk but 0 and 2", leased[:min(len(leased), 6)])
			}
			for _, i := range items {
				c := i / size
				if first.get(i)+second.get(i) < 1 {
					t.Fatalf("item %d never ran", i)
				}
				if (c == 0 || c == 2) && second.get(i) != 0 {
					t.Fatalf("item %d of completed chunk %d ran again", i, c)
				}
			}
			commits := store.commitCopy()
			if commits[0].Progress.Done != int64(2*size) {
				t.Fatalf("the resume starts at Done=%d, want %d", commits[0].Progress.Done, 2*size)
			}
			if sum.Done != 1000 || sum.ChunksDone != nChunks || sum.ChunksTotal != nChunks || sum.Total != 1000 {
				t.Fatalf("summary = %+v", sum)
			}
		})
	}
}

// TestInvariant_EveryItemAtLeastOnce_NoCompletedChunkTwice disrupts runs at
// random points, resumes until done, and checks the D28a invariant: every
// item ran at least once, no item of a chunk (Snapshot) or key range
// (AppendOnly) the stored ledger recorded ever ran again, and the final
// Summary.Done is exactly n. Each seed picks the source order, the ledger
// throttle (every completion, or a real throttle that leaves completions
// folded but not committed), and up to three disruptions of three kinds: a
// crash right after a commit, a crash right after a fold (before its
// commit), writes refused from some item on (ErrFenced or a lost lease, as
// the Writer reports them), and a real fence revoked mid-run by an item whose
// write through a real Writer is then refused and swallowed. A run that
// reached a refusal must end with that error, no item that swallowed a
// refused write may sit in a recorded chunk,
// and the final Failed counts only the items that really fail
// (OnItemError Continue): a refused write is never an ordinary failure.
func TestInvariant_EveryItemAtLeastOnce_NoCompletedChunkTwice(t *testing.T) {
	const seeds = 32
	for seed := range seeds {
		t.Run(fmt.Sprint("seed=", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(uint64(seed), 7))
			n := 50 + rng.IntN(250)
			size := []int{1, 3, 7, 256}[rng.IntN(4)]
			workers := 4 + rng.IntN(5)
			order := []ops.SourceOrder{ops.Snapshot, ops.AppendOnly}[rng.IntN(2)]
			commitEvery := []time.Duration{-1, 15 * time.Millisecond}[rng.IntN(2)]
			items := seq(n)
			store := newMemLedgerStore(t)
			total := map[int]int{}
			recorded := map[int]bool{} // items whose chunk/range a stored ledger recorded
			var final ops.Summary
			wantFailed := int64(0)
			for i := range items {
				if i%11 == 0 {
					wantFailed++
				}
			}
			for attempt := 0; ; attempt++ {
				c := newCounts()
				// 0: crash after a commit; 1: crash after a fold, before its
				// commit; 2: the Writer refuses writes from fenceFrom on
				// (an ErrFenced or lost-lease error, no revoke seen yet);
				// 3: a real fence revoked by item fenceFrom, which swallows
				// its refused write and returns nil, then the context is
				// canceled as the registry does; 4: clean.
				mode := 4
				if attempt < 3 {
					mode = rng.IntN(4)
				}
				disruptAt := 1 + rng.IntN(max(n/size, 1)+1)
				fenceFrom := rng.IntN(n)
				refusal := []error{ops.ErrFenced, opswriter.ErrStandDownLost}[rng.IntN(2)]
				fence, revoke := opswriter.NewFence(uint64(attempt+1), nil)
				w := previewWriter(fence)
				var reached atomic.Bool
				refused := newCounts() // items whose write the Writer refused
				var stopRun context.CancelFunc
				job, rc, cancel := openJob(t, items, batchOpts{order: order, size: size, workers: workers},
					func(irc *ops.ItemRC, i int) error {
						c.add(i)
						runtime.Gosched()
						if mode == 2 && i >= fenceFrom {
							reached.Store(true)
							return fmt.Errorf("item %d: %w", i, refusal)
						}
						if mode == 3 && i == fenceFrom {
							reached.Store(true)
							revoke()
							stopRun()
							if err := tryWrite(irc, w); errors.Is(err, ops.ErrFenced) {
								refused.add(i)
							}
							return nil // the refused write, swallowed
						}
						if i%11 == 0 {
							return errors.New("a failing item settles too")
						}
						return nil
					})
				stopRun = cancel
				store.newAttempt()
				commits, folds := 0, 0
				ctx := opswriter.WithFence(rc.Context(), fence)
				sum, _, err := runBatch(ctx, batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: commitEvery,
					afterCommit: func(database.Ledger, database.ProgressSnapshot) bool {
						commits++
						return mode == 0 && commits == disruptAt
					},
					afterFold: func() bool {
						folds++
						return mode == 1 && folds == disruptAt
					}})
				cancel()
				for i := range items {
					if recorded[i] && c.get(i) > 0 {
						t.Fatalf("attempt %d (mode %d) re-ran item %d, which the ledger had recorded", attempt, mode, i)
					}
					total[i] += c.get(i)
				}
				if reached.Load() && !errors.Is(err, errWorldStopped) {
					want := refusal
					if mode == 3 {
						want = ops.ErrFenced
					}
					if !errors.Is(err, want) {
						t.Fatalf("attempt %d (mode %d) reached the refusal at item %d but returned %v, want %v",
							attempt, mode, fenceFrom, err, want)
					}
				}
				if err == nil {
					final = sum
					break
				}
				if !errors.Is(err, errWorldStopped) && !errors.Is(err, ops.ErrFenced) && !errors.Is(err, opswriter.ErrStandDownLost) {
					t.Fatalf("attempt %d: %v", attempt, err)
				}
				led, _ := store.GetLedgerV3("op")
				if led == nil {
					continue
				}
				for i := range items {
					if order == ops.Snapshot {
						if c := i / led.ChunkSize; c < led.ChunkCount && led.Done[c/8]&(1<<(c%8)) != 0 {
							recorded[i] = true
						}
					} else if inRanges(led.Ranges, itemKey(i)) {
						recorded[i] = true
					}
					if recorded[i] && refused.get(i) > 0 {
						t.Fatalf("attempt %d (mode %d): item %d swallowed a refused write, and its chunk was recorded", attempt, mode, i)
					}
				}
			}
			for i := range items {
				if total[i] < 1 {
					t.Fatalf("item %d never ran", i)
				}
			}
			if final.Done != int64(n) {
				t.Fatalf("final Done = %d, want %d", final.Done, n)
			}
			if final.Failed != wantFailed {
				t.Fatalf("final Failed = %d, want %d (only the i%%11 items fail; a refused write is never a failure)", final.Failed, wantFailed)
			}
		})
	}
}

// TestPartitionBy_OneWorkerInSourceOrder: a 700-item partition among small
// ones is one oversized chunk; its items run on one worker in source order,
// never two at once; no partition straddles two chunks.
func TestPartitionBy_OneWorkerInSourceOrder(t *testing.T) {
	var items []int
	part := map[int]string{}
	for i := range 1300 {
		items = append(items, i)
		switch {
		case i >= 300 && i < 1000:
			part[i] = "big"
		default:
			part[i] = fmt.Sprintf("p%03d", i/10)
		}
	}
	keys := make([]string, len(items))
	parts := make([]string, len(items))
	for i := range items {
		keys[i], parts[i] = itemKey(i), part[i]
	}
	order, starts := cutChunks(keys, parts, 256)
	owner := map[string]int{}
	for c := range starts {
		hi := len(order)
		if c+1 < len(starts) {
			hi = starts[c+1]
		}
		for _, o := range order[starts[c]:hi] {
			if prev, ok := owner[parts[o]]; ok && prev != c {
				t.Fatalf("partition %s straddles chunks %d and %d", parts[o], prev, c)
			}
			owner[parts[o]] = c
		}
		if owner["big"] == c && hi-starts[c] != 700 {
			t.Fatalf("the big partition's chunk holds %d items, want exactly its 700", hi-starts[c])
		}
	}

	var mu sync.Mutex
	var bigOrder []int
	var inBig atomic.Int32
	job, rc, cancel := openJob(t, items, batchOpts{size: 256, workers: 8, partition: func(i int) string { return part[i] }},
		func(_ *ops.ItemRC, i int) error {
			if part[i] == "big" {
				if inBig.Add(1) > 1 {
					t.Error("two items of one partition ran at once")
				}
				mu.Lock()
				bigOrder = append(bigOrder, i)
				mu.Unlock()
				runtime.Gosched()
				inBig.Add(-1)
			}
			return nil
		})
	defer cancel()
	store := newMemLedgerStore(t)
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1}); err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(bigOrder) || len(bigOrder) != 700 {
		t.Fatalf("the big partition ran %d items, sorted=%v", len(bigOrder), slices.IsSorted(bigOrder))
	}
	if !store.hasOffsets {
		t.Fatal("a partitioned snapshot must store its chunk offsets")
	}
}

// TestAppendOnly_RangesNotNumbers: the ledger records completed key ranges,
// so deleting an already-completed item between runs does not shift which
// items the resume skips, and completed ranges never reach Item.
func TestAppendOnly_RangesNotNumbers(t *testing.T) {
	store := newMemLedgerStore(t)
	items := seq(100)
	first := newCounts()
	job, rc, cancel := openJob(t, items, batchOpts{order: ops.AppendOnly, size: 10, workers: 4},
		func(irc *ops.ItemRC, i int) error {
			first.add(i)
			if i >= 30 && i%10 == 5 { // chunks from 3 on stall mid-chunk
				<-irc.Context().Done()
				return irc.Context().Err()
			}
			return nil
		})
	defer cancel()
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, _ database.ProgressSnapshot) bool {
			return len(l.Ranges) == 1 && l.Ranges[0] == database.KeyRange{First: itemKey(0), Last: itemKey(29)}
		}})
	if !errors.Is(err, errWorldStopped) {
		t.Fatalf("first attempt: %v", err)
	}
	led, _ := store.GetLedgerV3("op")
	if len(led.Ranges) != 1 {
		t.Fatalf("adjacent completed chunks must merge: %+v", led.Ranges)
	}

	// Item 12 (completed) is deleted between runs; a chunk number would now
	// point one item further along. Ranges do not.
	remaining := slices.DeleteFunc(slices.Clone(items), func(i int) bool { return i == 12 })
	second := newCounts()
	store.newAttempt()
	job2, rc2, cancel2 := openJob(t, remaining, batchOpts{order: ops.AppendOnly, size: 10, workers: 4},
		func(_ *ops.ItemRC, i int) error { second.add(i); return nil })
	defer cancel2()
	sum, _, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 30 {
		if second.get(i) != 0 {
			t.Fatalf("item %d of a completed range reached Item again", i)
		}
	}
	for i := 30; i < 100; i++ {
		if second.get(i) != 1 {
			t.Fatalf("item %d ran %d times on resume, want 1", i, second.get(i))
		}
	}
	if sum.Done != 100 {
		t.Fatalf("Done = %d, want 100 (30 from the ledger, 70 now)", sum.Done)
	}
	final, _ := store.GetLedgerV3("op")
	if len(final.Ranges) != 1 || final.Ranges[0].First != itemKey(0) || final.Ranges[0].Last != itemKey(99) {
		t.Fatalf("final ranges = %+v", final.Ranges)
	}
}

func TestAppendOnly_RefusesDescendingKeys(t *testing.T) {
	items := []int{1, 2, 5, 4, 6}
	job, rc, cancel := openJob(t, items, batchOpts{order: ops.AppendOnly, size: 2, workers: 2},
		func(*ops.ItemRC, int) error { return nil })
	defer cancel()
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, commitEvery: -1})
	if err == nil {
		t.Fatal("an AppendOnly source whose keys go backwards was accepted")
	}
}

// TestUnordered_RestartsFromZero_And_RefusesContinue: an Unordered source
// ignores the ledger on resume and re-runs everything; ResumeContinue on it is
// refused before any item runs.
func TestUnordered_RestartsFromZero_And_RefusesContinue(t *testing.T) {
	store := newMemLedgerStore(t)
	items := seq(40)
	job, rc, cancel := openJob(t, items, batchOpts{order: ops.Unordered, size: 5, workers: 4},
		func(*ops.ItemRC, int) error { return nil })
	defer cancel()
	if job.Resume != ops.ResumeFromZero {
		t.Fatalf("Unordered resume = %v", job.Resume)
	}
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, p database.ProgressSnapshot) bool { return p.Done >= 20 }})
	if !errors.Is(err, errWorldStopped) {
		t.Fatalf("first attempt: %v", err)
	}
	again := newCounts()
	job2, rc2, cancel2 := openJob(t, items, batchOpts{order: ops.Unordered, size: 5, workers: 4},
		func(_ *ops.ItemRC, i int) error { again.add(i); return nil })
	defer cancel2()
	sum, _, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		if again.get(i) != 1 {
			t.Fatalf("item %d ran %d times on the from-zero resume, want 1", i, again.get(i))
		}
	}
	if sum.Done != 40 {
		t.Fatalf("Done = %d", sum.Done)
	}

	cont := ops.ResumeContinue
	def := ops.Batch("u", ops.BatchSpec[ops.NoParams, int, ops.NoResult]{
		Title:      "u",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		Resume:     &cont,
		Source: func(*ops.RC, ops.NoParams) (ops.Source[int], error) {
			return ops.Source[int]{Items: items, Key: itemKey, Order: ops.Unordered}, nil
		},
		Item: func(*ops.ItemRC, ops.NoParams, int) error { t.Error("an item ran"); return nil },
	})
	if _, err := ops.Compile(def).OpenBatch(ops.NewRC(ops.RunEnv{}), nil); err == nil {
		t.Fatal("ResumeContinue on an Unordered source was accepted")
	}
}

// TestLedgerWriteRateBounded: 10,000 items of 1 ms commit the ledger at most
// once per commitEvery (plus the opening and the final commit). The run takes
// about 1.5 s on 8 workers, so the throttle under test is 500 ms rather than
// the production 2 s (asserted separately): the same code path, a window the
// run actually spans.
func TestLedgerWriteRateBounded(t *testing.T) {
	if defaultCommitEvery != 2*time.Second {
		t.Fatalf("the production ledger throttle is %s, want 2s", defaultCommitEvery)
	}
	store := newMemLedgerStore(t)
	job, rc, cancel := openJob(t, seq(10_000), batchOpts{size: 50, workers: 8}, func(*ops.ItemRC, int) error {
		time.Sleep(time.Millisecond)
		return nil
	})
	defer cancel()
	const every = 500 * time.Millisecond
	start := time.Now()
	sum, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: every})
	if err != nil || sum.Done != 10_000 {
		t.Fatalf("run: %v %+v", err, sum)
	}
	elapsed := time.Since(start)
	at := store.commitAt
	if limit := int(elapsed/every) + 2; len(at) > limit {
		t.Fatalf("%d commits in %s, want at most %d", len(at), elapsed, limit)
	}
	for i := 1; i < len(at)-1; i++ { // the final commit is not throttled
		if gap := at[i].Sub(at[i-1]); gap < every-20*time.Millisecond {
			t.Fatalf("commits %d and %d are %s apart, closer than %s", i-1, i, gap, every)
		}
	}
	if store.committerCount() != 1 {
		t.Fatalf("%d goroutines committed the ledger, want 1", store.committerCount())
	}
}

// TestOnlyLedgerGoroutineWritesLedger: with 8 workers finishing chunks as
// fast as they can, every CommitLedgerV3 comes from the one ledger goroutine
// and none overlap (the store fails the test on overlap).
func TestOnlyLedgerGoroutineWritesLedger(t *testing.T) {
	store := newMemLedgerStore(t)
	job, rc, cancel := openJob(t, seq(2000), batchOpts{size: 1, workers: 8}, func(*ops.ItemRC, int) error { return nil })
	defer cancel()
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1}); err != nil {
		t.Fatal(err)
	}
	if n := len(store.commitCopy()); n < 2000 {
		t.Fatalf("%d commits for 2000 chunks with no throttle", n)
	}
	if store.committerCount() != 1 {
		t.Fatalf("%d goroutines committed the ledger, want exactly 1", store.committerCount())
	}
}

// TestCountersFoldedAtChunkCompletion: progress Done is monotone within and
// across attempts, and only ever a sum of whole chunks.
func TestCountersFoldedAtChunkCompletion(t *testing.T) {
	store := newMemLedgerStore(t)
	const size = 10
	items := seq(200)
	var dones []int64
	record := func(_ database.Ledger, p database.ProgressSnapshot) bool {
		dones = append(dones, p.Done)
		return len(dones) == 8
	}
	job, rc, cancel := openJob(t, items, batchOpts{size: size, workers: 6}, func(irc *ops.ItemRC, i int) error {
		irc.Count("seen", 1)
		if i%7 == 0 {
			irc.Skip("seven")
		}
		return nil
	})
	defer cancel()
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1, afterCommit: record}); !errors.Is(err, errWorldStopped) {
		t.Fatalf("first attempt: %v", err)
	}
	job2, rc2, cancel2 := openJob(t, items, batchOpts{size: size, workers: 6}, func(irc *ops.ItemRC, i int) error {
		irc.Count("seen", 1)
		if i%7 == 0 {
			irc.Skip("seven")
		}
		return nil
	})
	defer cancel2()
	sum, _, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, p database.ProgressSnapshot) bool { dones = append(dones, p.Done); return false }})
	if err != nil {
		t.Fatal(err)
	}
	for i, d := range dones {
		if d%size != 0 {
			t.Fatalf("Done=%d is not a sum of whole chunks", d)
		}
		if i > 0 && d < dones[i-1] {
			t.Fatalf("Done went backwards: %v", dones)
		}
	}
	if sum.Done != 200 || sum.Counter("seen") != 200 || sum.Counter("skipped:seven") != 29 || sum.Skipped != 29 {
		t.Fatalf("summary = %+v (a completed chunk's counters must count exactly once)", sum)
	}
}

func TestCancelBetweenItems(t *testing.T) {
	var started atomic.Int64
	var canceled atomic.Bool
	job, rc, cancel := openJob(t, seq(500), batchOpts{size: 5, workers: 4}, func(*ops.ItemRC, int) error {
		if started.Add(1) == 50 {
			canceled.Store(true)
		}
		time.Sleep(200 * time.Microsecond)
		return nil
	})
	defer cancel()
	go func() {
		for !canceled.Load() {
			runtime.Gosched()
		}
		cancel()
	}()
	sum, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, commitEvery: -1})
	if !errors.Is(err, context.Canceled) || !sum.Canceled {
		t.Fatalf("err %v canceled %v", err, sum.Canceled)
	}
	// Items already past the check when the cancel landed may finish; no
	// item starts after a worker sees the cancel.
	if started.Load() >= 500 {
		t.Fatal("the run did not stop between items")
	}
}

func TestFinishRunsAfterCancel(t *testing.T) {
	finished := make(chan ops.Summary, 1)
	var sawCanceled atomic.Bool
	job, rc, cancel := openJob(t, seq(100), batchOpts{size: 10, workers: 2, finish: func(rc *ops.RC, s ops.Summary) {
		sawCanceled.Store(rc.Canceled())
		finished <- s
	}}, func(irc *ops.ItemRC, i int) error {
		if i == 25 {
			<-irc.Context().Done()
			return irc.Context().Err()
		}
		return nil
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, commitEvery: -1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	select {
	case s := <-finished:
		if !s.Canceled || !sawCanceled.Load() || s.Done >= 100 {
			t.Fatalf("Finish after cancel: %+v canceled=%v", s, sawCanceled.Load())
		}
	default:
		t.Fatal("Finish did not run after the cancel")
	}
}

func TestPauseGateHonored(t *testing.T) {
	globalPauseGate.Pause("test", "t")
	defer globalPauseGate.Resume()
	var ran atomic.Int64
	job, rc, cancel := openJob(t, seq(20), batchOpts{size: 5, workers: 4}, func(*ops.ItemRC, int) error {
		ran.Add(1)
		return nil
	})
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, commitEvery: -1})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if ran.Load() != 0 {
		t.Fatalf("%d items ran while operations were paused", ran.Load())
	}
	globalPauseGate.Resume()
	select {
	case err := <-done:
		if err != nil || ran.Load() != 20 {
			t.Fatalf("after resume: %v, ran %d", err, ran.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not continue after the pause was lifted")
	}
}

func TestStuckWorkerMarked(t *testing.T) {
	store := newMemLedgerStore(t)
	release := make(chan struct{})
	job, rc, cancel := openJob(t, seq(8), batchOpts{size: 4, workers: 2, timeout: 30 * time.Millisecond},
		func(_ *ops.ItemRC, i int) error {
			if i == 5 {
				<-release // ignores its deadline: the stuck worker
			}
			return nil
		})
	defer cancel()
	var mu sync.Mutex
	stuckSeen := false
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: 20 * time.Millisecond,
			afterCommit: func(_ database.Ledger, p database.ProgressSnapshot) bool {
				for _, w := range p.Workers {
					if w.Stuck && w.Chunk == 1 {
						mu.Lock()
						stuckSeen = true
						mu.Unlock()
					}
				}
				return false
			}})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		s := stuckSeen
		mu.Unlock()
		if s {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if !stuckSeen {
		t.Fatal("a worker past its item budget was never reported stuck")
	}
}

func TestLabelRunsInWorkerWithoutRace(t *testing.T) {
	store := newMemLedgerStore(t)
	var calls atomic.Int64
	job, rc, cancel := openJob(t, seq(400), batchOpts{size: 7, workers: 8,
		label: func(i int) string { calls.Add(1); return "item " + strconv.Itoa(i) }},
		func(*ops.ItemRC, int) error { runtime.Gosched(); return nil })
	defer cancel()
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 400 {
		t.Fatalf("Label called %d times, want once per item", calls.Load())
	}
	commits := store.commitCopy()
	if last := commits[len(commits)-1].Progress.Current; last == "" {
		t.Fatal("the progress carries no current item label")
	}
}

func TestStopOnErrorLeavesChunkIncomplete(t *testing.T) {
	store := newMemLedgerStore(t)
	boom := errors.New("boom")
	job, rc, cancel := openJob(t, seq(30), batchOpts{size: 10, workers: 1, stop: true}, func(_ *ops.ItemRC, i int) error {
		if i == 15 {
			return boom
		}
		return nil
	})
	defer cancel()
	sum, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	led, _ := store.GetLedgerV3("op")
	if !slices.Equal(bitsSet(*led), []int{0}) || sum.Done != 10 {
		t.Fatalf("Stop must leave the failing chunk incomplete: bits %v done %d", bitsSet(*led), sum.Done)
	}
}

func TestItemErrorsContinueAndAreRecorded(t *testing.T) {
	store := newMemLedgerStore(t)
	job, rc, cancel := openJob(t, seq(30), batchOpts{size: 10, workers: 3}, func(irc *ops.ItemRC, i int) error {
		switch {
		case i%10 == 3:
			return errors.New("bad item")
		case i%10 == 4:
			return ops.ErrPreview // a Preview write is not a failure
		}
		return nil
	})
	defer cancel()
	sum, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Failed != 3 || sum.Done != 30 || sum.Counter("previewed") != 3 || sum.Counter("ok") != 27 {
		t.Fatalf("summary = %+v", sum)
	}
	var rows int
	for _, c := range store.commitCopy() {
		rows += len(c.ItemRows)
	}
	if rows != 3 {
		t.Fatalf("%d failed rows reached the store, want 3", rows)
	}
}

// A frozen key whose item is gone on resume is counted skipped:gone, not run.
func TestResumeCountsGoneItems(t *testing.T) {
	store := newMemLedgerStore(t)
	items := seq(20)
	job, rc, cancel := openJob(t, items, batchOpts{size: 10, workers: 1}, func(irc *ops.ItemRC, i int) error {
		if i >= 10 {
			<-irc.Context().Done()
			return irc.Context().Err()
		}
		return nil
	})
	defer cancel()
	_, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, store: store, commitEvery: -1,
		afterCommit: func(l database.Ledger, _ database.ProgressSnapshot) bool { return len(bitsSet(l)) == 1 }})
	if !errors.Is(err, errWorldStopped) {
		t.Fatal(err)
	}
	fewer := slices.DeleteFunc(slices.Clone(items), func(i int) bool { return i == 15 })
	job2, rc2, cancel2 := openJob(t, fewer, batchOpts{size: 10, workers: 1}, func(_ *ops.ItemRC, i int) error {
		if i == 15 {
			t.Error("a gone item ran")
		}
		return nil
	})
	defer cancel2()
	sum, _, err := runBatch(rc2.Context(), batchConfig{opID: "op", job: job2, rc: rc2, store: store, commitEvery: -1})
	if err != nil || sum.Counter("skipped:gone") != 1 || sum.Done != 20 {
		t.Fatalf("resume: %v %+v", err, sum)
	}
}

// fakeDirty is a DirtySet store: entries are prefix+id -> epoch bytes.
type fakeDirty struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func (f *fakeDirty) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := slices.Sorted(maps.Keys(f.entries))
	var out []database.KVPair
	for _, k := range keys {
		if k <= after || len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		if len(out) == limit {
			return out, out[len(out)-1].Key, nil
		}
		out = append(out, database.KVPair{Key: k, Value: slices.Clone(f.entries[k])})
	}
	return out, "", nil
}

func (f *fakeDirty) CompareAndDeleteRaw(key string, expect []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.entries[key]; ok && bytes.Equal(cur, expect) {
		delete(f.entries, key)
		return true, nil
	}
	return false, nil
}

func TestDirtySetCompareAndDelete(t *testing.T) {
	const prefix = "idx:test:dirty:"
	fd := &fakeDirty{entries: map[string][]byte{}}
	for i := range 600 {
		fd.entries[prefix+itemKey(i)] = []byte{0, 0, 0, 0, 0, 0, 0, 1}
	}
	var ran atomic.Int64
	def := ops.Batch("d", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{
		Title:      "d",
		Effects:    ops.ReadOnly(),
		Permission: auth.PermLibraryView,
		Source: func(*ops.RC, ops.NoParams) (ops.Source[string], error) {
			return ops.DirtySet(prefix, func(_ context.Context, ids []string) ([]string, error) { return ids, nil }), nil
		},
		Item: func(_ *ops.ItemRC, _ ops.NoParams, id string) error {
			ran.Add(1)
			if id == itemKey(7) { // re-marked while in flight
				fd.mu.Lock()
				fd.entries[prefix+id] = []byte{0, 0, 0, 0, 0, 0, 0, 2}
				fd.mu.Unlock()
			}
			if id == itemKey(9) {
				return errors.New("failed items stay dirty")
			}
			return nil
		},
		Concurrency: ops.Network(4, "test"),
	})
	rc := ops.NewRC(ops.RunEnv{Context: context.Background(), OpID: "op"})
	job, err := ops.Compile(def).OpenBatch(rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, commitEvery: -1}); !errors.Is(err, ErrDirtySetUnsupported) {
		t.Fatalf("without the primitive: %v", err)
	}
	sum, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, dirty: fd, commitEvery: -1})
	if err != nil || ran.Load() != 600 || sum.ChunksDone != 3 {
		t.Fatalf("run: %v ran %d chunks %d (256 per page)", err, ran.Load(), sum.ChunksDone)
	}
	left := slices.Sorted(maps.Keys(fd.entries))
	if !slices.Equal(left, []string{prefix + itemKey(7), prefix + itemKey(9)}) {
		t.Fatalf("left dirty = %v, want the re-marked and the failed entry", left)
	}
}

// TestDirtySetPreviewKeepsEntries: a Preview run of a DirtySet source
// performs no write, so it deletes no entry; the live run that follows still
// finds every subject dirty.
func TestDirtySetPreviewKeepsEntries(t *testing.T) {
	const prefix = "idx:test:dirty:"
	fd := &fakeDirty{entries: map[string][]byte{}}
	for i := range 10 {
		fd.entries[prefix+itemKey(i)] = []byte{0, 0, 0, 0, 0, 0, 0, 1}
	}
	var ran atomic.Int64
	def := ops.Batch("d", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{
		Title:      "d",
		Effects:    ops.Writes(ops.ResBooks),
		Permission: auth.PermLibraryEditMetadata,
		Source: func(*ops.RC, ops.NoParams) (ops.Source[string], error) {
			return ops.DirtySet(prefix, func(_ context.Context, ids []string) ([]string, error) { return ids, nil }), nil
		},
		Item: func(_ *ops.ItemRC, _ ops.NoParams, _ string) error {
			ran.Add(1)
			return ops.ErrPreview
		},
	})
	rc := ops.NewRC(ops.RunEnv{Context: context.Background(), OpID: "op", Mode: ops.Preview})
	job, err := ops.Compile(def).OpenBatch(rc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runBatch(rc.Context(), batchConfig{opID: "op", job: job, rc: rc, dirty: fd, commitEvery: -1}); err != nil {
		t.Fatalf("preview run: %v", err)
	}
	if ran.Load() != 10 || len(fd.entries) != 10 {
		t.Fatalf("preview ran %d items and left %d of 10 entries dirty; it must run all and keep all", ran.Load(),
			len(fd.entries))
	}
}
