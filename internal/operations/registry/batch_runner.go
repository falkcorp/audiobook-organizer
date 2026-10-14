// file: internal/operations/registry/batch_runner.go
// version: 1.6.1
// guid: d5558a83-b2da-485c-bb5c-dba9bddea8eb
// last-edited: 2026-10-13

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The chunk-leasing Batch runner (05-PR5, sdk-api.md §5). It generalizes
// run_items.go for ops.Batch and ops.Fixer definitions:
//
//   - The source is cut into chunks (ChunkSize items, default 256). A Snapshot
//     source's keys are frozen at start in opv3:snap:, partition-major when
//     the def partitions, so the chunk table is a function of the snapshot
//     alone; an AppendOnly or Unordered source is chunked as it streams.
//   - A bounded pool of Workers goroutines leases chunks. A worker runs its
//     chunk's items in order, so every item of a partition runs on one worker
//     in source order, and any free worker takes any free chunk. A lease is
//     never reassigned while its owner goroutine is alive.
//   - Workers buffer each item's counters and hand the whole chunk's tally
//     to ONE ledger goroutine when the chunk completes. Only that goroutine
//     writes the ledger (CommitLedgerV3: ledger + lease table + progress in
//     one batch), at most every commitEvery, so a crash loses at most the
//     chunks in flight and Done never counts half a chunk.
//   - A resume (same op id) leases every chunk the ledger does not record,
//     from its first item; Item must be idempotent.
//
// Concurrency rules (CLAUDE.md "Concurrency"): the pool is bounded; workers
// share nothing but the chunk channel, the lease table (one mutex, touched
// once per item) and the completion channel; Label runs inside the worker for
// the worker's own item and reads nothing shared.

// defaultCommitEvery is the ledger write throttle.
const defaultCommitEvery = 2 * time.Second

// progressRefreshEvery re-commits progress (the worker rows) without a
// completion, so a run of slow chunks still shows where each worker is.
const progressRefreshEvery = 10 * time.Second

// maxItemRowsPerRun caps the failed/skipped rows the runner hands the store.
// One past the store's cap is sent, so the store records a drop and
// ItemPage.Truncated reports it.
const maxItemRowsPerRun = 10_001

// ErrDirtySetUnsupported is returned when a DirtySet source runs on a store
// without the compare-and-delete primitive (02 PR 14 adds it with the marker).
var ErrDirtySetUnsupported = errors.New("registry: the store has no compare-and-delete for DirtySet sources")

// batchStore is the chunk-ledger surface the runner persists through.
type batchStore interface {
	PutSnapshotPageV3(id string, page int, keys []string) error
	GetSnapshotPageV3(id string, page int) ([]string, bool, error)
	PutChunkOffsetsV3(id string, offsets []int32) error
	GetChunkOffsetsV3(id string) ([]int32, bool, error)
	CommitLedgerV3(id string, b database.LedgerBatch) error
	GetLedgerV3(id string) (*database.Ledger, error)
	ListMarksV3(id string) (map[string][]string, error)
}

var _ batchStore = (*database.PebbleStore)(nil)

// dirtySetStore is what a DirtySet source needs: paging the set and deleting
// an entry only if its value (which begins with the mark epoch) is unchanged.
type dirtySetStore interface {
	ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error)
	CompareAndDeleteRaw(key string, expect []byte) (bool, error)
}

// batchConfig is one run of a BatchJob.
type batchConfig struct {
	opID  string
	job   *ops.BatchJob
	rc    *ops.RC
	store batchStore    // nil: no persistence, so no resume
	dirty dirtySetStore // required for a DirtySet source
	rep   Reporter      // nil in unit tests
	log   *slog.Logger
	// resumes reports, once the run's context is done, whether the registry
	// will record this stop as an interrupted run that the next boot resumes
	// under the same op id (Registry.stopResumes, the same classification
	// finalStatusForCanceledRun writes). Such an attempt skips Finish; the
	// resumed attempt runs it once, over every attempt's ledger. Asking the
	// registry, rather than reading the cancel cause here, keeps the two
	// decisions from disagreeing: a skipped Finish on a run that is never
	// resumed, or a Finish here and again on the resume. nil: never resumes.
	//
	// Two later decisions can still keep a run recorded as resumable from
	// ever resuming, and its Finish then never runs (todo.d
	// 05-PR5-FINISH-ON-DECLINE):
	//   - the restart-strike guard (checkInfiniteRestart) force-drops a
	//     ResumeRestart run only when high_water_progress is still 0, i.e.
	//     no chunk ever completed, so the ledger holds no counts and no
	//     marks: a Finish there would get an empty Summary and a Fixer's
	//     AfterApply nothing to follow up on. Running it buys nothing.
	//   - a ResumeAsk run (the Fixer default) waits in awaiting_decision for
	//     a person, whose only choices today are Retry (the run resumes and
	//     Finish runs once, at its end) or Discard (DeleteRunV3 deletes the
	//     record, ledger and marks). After a Discard, rows applied before the
	//     shutdown keep their writes but never get AfterApply. Running op
	//     code from Discard would make a generic delete run a def's writes
	//     outside any attempt (no fence, no reporter). The owner chose
	//     (2026-10-13) a third decision, "Finish without resuming": an
	//     attempt under a fresh lease and fence that leases nothing and runs
	//     only Finish/AfterApply over the ledger. It is a follow-up to 05-PR5.
	resumes func() bool
	// stuckAfter is the heartbeat age at which a worker is reported stuck
	// when the job has no ItemTimeout (the def Timeout/6).
	stuckAfter  time.Duration
	commitEvery time.Duration // 0 = defaultCommitEvery; negative = every completion
	now         func() time.Time

	// Test hooks. afterCommit sees every committed ledger; returning true
	// stops the world right there, like a process kill: no further commit,
	// no Finish. onLease sees every chunk a worker leases.
	afterCommit func(database.Ledger, database.ProgressSnapshot) bool
	// afterFold sees every folded completion before any commit of it;
	// returning true stops the world in the folded-but-not-committed state.
	afterFold func() bool
	onLease   func(chunk int)
}

// errWorldStopped is what runBatch returns when afterCommit stopped it.
var errWorldStopped = errors.New("registry: batch run stopped by a test crash hook")

// leasedChunk is one chunk handed to a worker.
type leasedChunk struct {
	idx   int
	set   ops.ItemSet
	items []int    // indices into set; -1 = the frozen key no longer exists
	keys  []string // the chunk's keys, aligned with items
	// AppendOnly: the chunk's key range and the stream key before it.
	first, last, after string
	// DirtySet: each entry's stored value (it begins with the mark epoch).
	vals [][]byte
}

// completion is a finished chunk sent to the ledger goroutine.
type completion struct {
	chunk *leasedChunk
	tally *chunkTally
}

// workerRow is one worker's lease, guarded by leaseTable.mu.
type workerRow struct {
	chunk       int
	chunkSize   int
	leasedAt    time.Time
	heartbeat   time.Time
	itemIndex   int
	item        string
	stuckLogged bool
}

type leaseTable struct {
	mu      sync.Mutex
	rows    []workerRow
	current string
}

func (lt *leaseTable) set(w int, fn func(*workerRow)) {
	lt.mu.Lock()
	fn(&lt.rows[w])
	lt.mu.Unlock()
}

// batchRun is the state of one runBatch call.
type batchRun struct {
	cfg  batchConfig
	job  *ops.BatchJob
	now  func() time.Time
	size int

	ctx     context.Context // the run: canceled on cancel/timeout
	workCtx context.Context // ctx plus stop (StopOnError, a crash)
	stop    context.CancelFunc
	runErr  atomic.Pointer[error]
	crashed atomic.Bool
	ledger  *chunkLedger
	table   *chunkTable // Snapshot only
	// skip is the AppendOnly ranges an earlier attempt completed, copied at
	// plan time. The producer reads only this copy: the ledger itself
	// belongs to the ledger goroutine, and the ranges this attempt completes
	// lie behind the ascending cursor anyway.
	skip     []database.KeyRange
	leases   *leaseTable
	chunks   chan *leasedChunk
	done     chan completion
	cutCount atomic.Int64 // stream chunks cut so far (AppendOnly/Unordered)
	// priorChunks is the stream chunks earlier attempts completed (their
	// items are skipped, not cut again).
	priorChunks int
	total       *int64
	fence       opswriter.Fence
	// latch is the run's ops.RefusalLatch: a fenced or lease-lost refusal of
	// a write no live item owns. Checked after every item.
	latch *ops.RefusalLatch
}

func (b *batchRun) fail(err error) {
	if err == nil {
		return
	}
	b.runErr.CompareAndSwap(nil, &err)
	b.stop()
}

func (b *batchRun) err() error {
	if p := b.runErr.Load(); p != nil {
		return *p
	}
	return nil
}

// runBatch runs one attempt of a batch job and returns the Finish result.
func runBatch(ctx context.Context, cfg batchConfig) (ops.Summary, json.RawMessage, error) {
	job := cfg.job
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.commitEvery == 0 {
		cfg.commitEvery = defaultCommitEvery
	}
	if cfg.log == nil {
		cfg.log = slog.New(slog.DiscardHandler)
	}
	if job.Dirty != nil && cfg.dirty == nil {
		return ops.Summary{}, nil, fmt.Errorf("%s: %w", cfg.opID, ErrDirtySetUnsupported)
	}
	workers := max(job.Workers, 1)
	b := &batchRun{cfg: cfg, job: job, now: cfg.now, size: max(job.ChunkSize, 1), ctx: ctx,
		leases: &leaseTable{rows: make([]workerRow, workers)}, done: make(chan completion, workers)}
	// The run's refusal latch: the one NewRC (or runV3, before building the
	// Writer) installed on the run context. Item contexts carry it too, so a
	// refusal a sealed item's late write makes still reaches it.
	b.latch = ops.RefusalLatchFrom(cfg.rc.Context())
	if b.latch == nil {
		b.latch = &ops.RefusalLatch{}
	}
	b.workCtx, b.stop = context.WithCancel(ops.WithRefusalLatch(ctx, b.latch))
	defer b.stop()
	b.fence = opswriter.FenceFromContext(ctx)
	for i := range b.leases.rows {
		b.leases.rows[i].chunk = -1
	}
	if n, ok := job.Total.Value(); ok {
		b.total = &n
	}

	// Plan: freeze or reload the snapshot, or load the stream's ranges.
	if err := b.plan(); err != nil {
		return ops.Summary{}, nil, err
	}

	ledgerDone := make(chan struct{})
	go func() {
		defer close(ledgerDone)
		b.ledgerLoop()
	}()

	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() { b.worker(w) })
	}
	if job.Order != ops.Snapshot {
		wg.Go(b.produce)
	}
	wg.Wait()
	close(b.done)
	<-ledgerDone

	if b.crashed.Load() {
		return b.summary(), nil, errWorldStopped
	}
	b.settleStop()
	// After settleStop, so Summary.Canceled (and rc.Canceled in Finish) sees
	// a cancel that landed just after the fence was revoked.
	sum := b.summary()
	runErr := b.err()
	if b.stoppedForResume() {
		// Shutdown: the run resumes under the same op id and its Finish runs
		// then, once, over every attempt's chunks. Running it here as well
		// would run a Fixer's AfterApply twice and report a partial result.
		return sum, nil, errors.Join(context.Cause(ctx), runErr)
	}
	result, ferr := b.finish(sum)
	if runErr == nil && ctx.Err() != nil {
		runErr = context.Cause(ctx)
	}
	return sum, result, errors.Join(runErr, ferr)
}

// stopSettle bounds settleStop's wait. The registry's stop paths (cancel,
// timeout, abandon, shutdown) revoke the fence and cancel the context back
// to back on one goroutine, so the cancel follows the revoke within
// microseconds; a second is generous for a loaded host. The only revoke with
// no cancel behind it is a lost stand-down lease, where the wait runs out
// once, at the end of a run that is failing anyway.
const stopSettle = time.Second

// settleStop waits, when the workers stopped on a revoked fence before the
// run's context was canceled, for that cancel to land (or stopSettle), so
// the summary and the resume decision see which stop this was.
func (b *batchRun) settleStop() {
	if b.ctx.Err() == nil && b.fence.Revoked() {
		select {
		case <-b.ctx.Done():
		case <-time.After(stopSettle):
		}
	}
}

// stoppedForResume reports a stopped run the registry will resume (see
// batchConfig.resumes). Call it after settleStop.
func (b *batchRun) stoppedForResume() bool {
	return b.ctx.Err() != nil && b.cfg.resumes != nil && b.cfg.resumes()
}

// finish runs the job's Finish, after a cancel too (rc.Canceled() is then true).
func (b *batchRun) finish(sum ops.Summary) (json.RawMessage, error) {
	if b.job.Finish == nil {
		return nil, nil
	}
	return b.job.Finish(b.cfg.rc, sum)
}

func (b *batchRun) summary() ops.Summary {
	l := b.ledger.snapshot()
	s := ops.Summary{Done: l.Items.Done, Failed: l.Items.Failed, Skipped: l.Items.Skipped,
		Counters: l.Counters, Marks: b.ledger.allMarks(), ChunksDone: b.ledger.chunksDone, ChunksTotal: b.chunkTotal(),
		Canceled: b.ctx.Err() != nil}
	if b.total != nil {
		s.Total = *b.total
	} else {
		s.Total = s.Done
	}
	return s
}

func (b *batchRun) chunkTotal() int {
	if b.table != nil {
		return b.table.count()
	}
	return b.priorChunks + int(b.cutCount.Load())
}

// ── plan ───────────────────────────────────────────────────────────────────

func (b *batchRun) plan() error {
	var stored *database.Ledger
	if b.cfg.store != nil && b.job.Resume == ops.ResumeContinue {
		l, err := b.cfg.store.GetLedgerV3(b.cfg.opID)
		if err != nil {
			return fmt.Errorf("%s: read the chunk ledger: %w", b.cfg.opID, err)
		}
		stored = l
	}
	var marks map[string][]string
	if stored != nil {
		m, err := b.cfg.store.ListMarksV3(b.cfg.opID)
		if err != nil {
			return fmt.Errorf("%s: read the run's marks: %w", b.cfg.opID, err)
		}
		marks = m
	}
	switch b.job.Order {
	case ops.Snapshot:
		return b.planSnapshot(stored, marks)
	case ops.AppendOnly:
		if stored != nil {
			b.ledger = loadChunkLedger(stored, marks)
			b.size = max(stored.ChunkSize, 1)
			b.skip = slices.Clone(b.ledger.l.Ranges)
			b.priorChunks = b.ledger.chunksDone
		} else {
			b.ledger = newChunkLedger(b.size, 0)
		}
	default: // Unordered: no ledger to resume from, ever
		b.ledger = newChunkLedger(b.size, 0)
	}
	b.chunks = make(chan *leasedChunk, max(b.job.Workers, 1))
	return nil
}

// planSnapshot freezes the snapshot (first attempt, or ResumeFromZero) or
// reloads it (resume), and queues every chunk the ledger does not record.
func (b *batchRun) planSnapshot(stored *database.Ledger, marks map[string][]string) error {
	set, err := b.snapshotItems()
	if err != nil {
		return err
	}
	keys := make([]string, set.Len())
	index := make(map[string]int, set.Len())
	for i := range keys {
		k := set.Key(i)
		if _, dup := index[k]; dup {
			return fmt.Errorf("%s: two items share the key %q; Key must be unique", b.cfg.opID, k)
		}
		keys[i], index[k] = k, i
	}
	if stored != nil {
		if t, ok, err := b.loadSnapshot(stored); err != nil {
			return err
		} else if ok {
			b.table, b.ledger = t, loadChunkLedger(stored, marks)
			b.size = max(stored.ChunkSize, 1)
		}
	}
	if b.table == nil {
		var parts []string
		if b.job.Partitioned {
			parts = make([]string, set.Len())
			for i := range parts {
				parts[i] = set.Partition(i)
			}
		}
		order, starts := cutChunks(keys, parts, b.size)
		frozen := make([]string, len(order))
		for i, o := range order {
			frozen[i] = keys[o]
		}
		b.table = &chunkTable{keys: frozen, starts: starts}
		b.ledger = newChunkLedger(b.size, len(starts))
		if err := b.freeze(); err != nil {
			return err
		}
	}
	if b.total == nil {
		n := int64(len(b.table.keys))
		b.total = &n
	}
	b.chunks = make(chan *leasedChunk, b.table.count())
	for c := range b.table.count() {
		if b.ledger.isDone(c) {
			continue // never leased again
		}
		lo, hi := b.table.bounds(c)
		ch := &leasedChunk{idx: c, set: set, keys: b.table.keys[lo:hi], items: make([]int, hi-lo)}
		for j, k := range ch.keys {
			if i, ok := index[k]; ok {
				ch.items[j] = i
			} else {
				ch.items[j] = -1
			}
		}
		b.chunks <- ch
	}
	close(b.chunks)
	return nil
}

// snapshotItems is the full item set: Items, or every page drained once.
func (b *batchRun) snapshotItems() (ops.ItemSet, error) {
	if b.job.Items != nil {
		return b.job.Items, nil
	}
	if b.job.Pages == nil {
		return nil, fmt.Errorf("%s: a Snapshot source needs Items or Pages", b.cfg.opID)
	}
	var sets []ops.ItemSet
	after := ""
	for {
		page, next, err := b.job.Pages(b.ctx, after, b.size)
		if err != nil {
			return nil, fmt.Errorf("%s: read the source: %w", b.cfg.opID, err)
		}
		if page != nil && page.Len() > 0 {
			sets = append(sets, page)
		}
		if next == "" {
			break
		}
		after = next
	}
	return concatSets(sets), nil
}

// freeze stores the frozen keys (10,000 per page, then an empty page so a
// reader stops even when the last real page is full) and the
// partition-adjusted chunk offsets. The empty ledger is the ledger
// goroutine's opening commit (ledgerLoop), so a crash before the first
// completion still finds a ledger and resumes over the same chunk table.
func (b *batchRun) freeze() error {
	st := b.cfg.store
	if st == nil {
		return nil
	}
	keys := b.table.keys
	page := 0
	for lo := 0; lo < len(keys); lo += snapshotPageSize {
		if err := st.PutSnapshotPageV3(b.cfg.opID, page, keys[lo:min(lo+snapshotPageSize, len(keys))]); err != nil {
			return fmt.Errorf("%s: freeze snapshot page %d: %w", b.cfg.opID, page, err)
		}
		page++
	}
	if err := st.PutSnapshotPageV3(b.cfg.opID, page, nil); err != nil {
		return fmt.Errorf("%s: freeze snapshot end: %w", b.cfg.opID, err)
	}
	if b.job.Partitioned {
		offsets := make([]int32, len(b.table.starts))
		for i, s := range b.table.starts {
			offsets[i] = int32(s) // bounded: a snapshot past 2^31 keys is not a library
		}
		if err := st.PutChunkOffsetsV3(b.cfg.opID, offsets); err != nil {
			return fmt.Errorf("%s: freeze chunk offsets: %w", b.cfg.opID, err)
		}
	}
	return nil
}

// loadSnapshot rebuilds a resumed run's chunk table from opv3:snap:. It
// reports false (start over) when the snapshot is missing.
func (b *batchRun) loadSnapshot(stored *database.Ledger) (*chunkTable, bool, error) {
	st := b.cfg.store
	var keys []string
	for page := 0; ; page++ {
		ks, found, err := st.GetSnapshotPageV3(b.cfg.opID, page)
		if err != nil {
			return nil, false, fmt.Errorf("%s: read snapshot page %d: %w", b.cfg.opID, page, err)
		}
		if !found {
			if page == 0 {
				b.cfg.log.Warn("batch runner: ledger without a snapshot; starting over", "op_id", b.cfg.opID)
				return nil, false, nil
			}
			break
		}
		keys = append(keys, ks...)
		if len(ks) < snapshotPageSize {
			break
		}
	}
	t := &chunkTable{keys: keys}
	if b.job.Partitioned {
		offs, found, err := st.GetChunkOffsetsV3(b.cfg.opID)
		if err != nil {
			return nil, false, fmt.Errorf("%s: read chunk offsets: %w", b.cfg.opID, err)
		}
		if !found {
			return nil, false, fmt.Errorf("%s: a partitioned snapshot has no chunk offsets; cannot resume", b.cfg.opID)
		}
		for _, o := range offs {
			t.starts = append(t.starts, int(o))
		}
	} else {
		size := max(stored.ChunkSize, 1)
		for s := 0; s < len(keys); s += size {
			t.starts = append(t.starts, s)
		}
	}
	if t.count() != stored.ChunkCount {
		return nil, false, fmt.Errorf("%s: snapshot has %d chunks, ledger %d; cannot resume", b.cfg.opID, t.count(), stored.ChunkCount)
	}
	return t, true, nil
}

// ── stream producer (AppendOnly, Unordered, DirtySet) ──────────────────────

// produce cuts a streaming source into chunks of size items in arrival order
// and hands them to the workers. AppendOnly keys must ascend; an item inside
// a completed range is skipped without calling Item.
func (b *batchRun) produce() {
	defer close(b.chunks)
	defer b.recoverCallback("producer")
	if b.job.Dirty != nil {
		b.produceDirty()
		return
	}
	var (
		pend    []int
		pendSet ops.ItemSet
		prevKey string // the last stream key seen (AppendOnly ascending check)
		after   string // the stream key before the pending chunk's first item
		seq     int
	)
	emit := func() bool {
		if len(pend) == 0 {
			return true
		}
		ch := &leasedChunk{idx: seq, set: pendSet, items: pend, after: after}
		ch.keys = make([]string, len(pend))
		for j, i := range pend {
			ch.keys[j] = pendSet.Key(i)
		}
		ch.first, ch.last = ch.keys[0], ch.keys[len(ch.keys)-1]
		seq++
		b.cutCount.Add(1)
		pend = nil
		select {
		case b.chunks <- ch:
			return true
		case <-b.workCtx.Done():
			return false
		}
	}
	cursor := ""
	for {
		if b.workCtx.Err() != nil {
			return
		}
		page, next, err := b.job.Pages(b.workCtx, cursor, b.size)
		if err != nil {
			b.fail(fmt.Errorf("%s: read the source: %w", b.cfg.opID, err))
			return
		}
		for i := 0; page != nil && i < page.Len(); i++ {
			key := page.Key(i)
			if b.job.Order == ops.AppendOnly {
				if prevKey != "" && key <= prevKey {
					b.fail(fmt.Errorf("%s: AppendOnly source returned %q after %q; keys must ascend", b.cfg.opID, key, prevKey))
					return
				}
				if inRanges(b.skip, key) {
					// Completed in an earlier attempt: skip without Item.
					if !emit() {
						return
					}
					prevKey, after = key, key
					continue
				}
			}
			if pendSet != page && len(pend) > 0 {
				if !emit() { // a chunk never spans two page sets
					return
				}
			}
			if len(pend) == 0 {
				after = prevKey
			}
			pendSet = page
			pend = append(pend, i)
			prevKey = key
			if len(pend) >= b.size && !emit() {
				return
			}
		}
		if next == "" {
			emit()
			return
		}
		cursor = next
	}
}

// produceDirty leases each DirtySet page (up to size entries) as one chunk.
func (b *batchRun) produceDirty() {
	d := b.job.Dirty
	cursor, seq := "", 0
	for b.workCtx.Err() == nil {
		kvs, next, err := b.cfg.dirty.ScanPrefixPage(d.Prefix, cursor, b.size)
		if err != nil {
			b.fail(fmt.Errorf("%s: read the dirty set: %w", b.cfg.opID, err))
			return
		}
		if len(kvs) == 0 {
			return
		}
		ids := make([]string, len(kvs))
		vals := make([][]byte, len(kvs))
		for i, kv := range kvs {
			ids[i], vals[i] = strings.TrimPrefix(kv.Key, d.Prefix), kv.Value
		}
		set, err := d.Load(b.workCtx, ids)
		if err != nil {
			b.fail(fmt.Errorf("%s: load dirty subjects: %w", b.cfg.opID, err))
			return
		}
		ch := &leasedChunk{idx: seq, set: set, keys: ids, vals: vals, items: make([]int, len(ids))}
		for i := range ch.items {
			ch.items[i] = i
		}
		seq++
		b.cutCount.Add(1)
		select {
		case b.chunks <- ch:
		case <-b.workCtx.Done():
			return
		}
		if next == "" {
			return
		}
		cursor = kvs[len(kvs)-1].Key
	}
}

// ── workers ────────────────────────────────────────────────────────────────

func (b *batchRun) worker(w int) {
	defer b.recoverCallback("worker")
	for {
		select {
		case <-b.workCtx.Done():
			return
		case ch, ok := <-b.chunks:
			if !ok {
				return
			}
			if b.workCtx.Err() != nil {
				return
			}
			b.runChunk(w, ch)
		}
	}
}

// runChunk runs one leased chunk. It reports the chunk complete only when
// every item returned (ok, failed-and-continued, or skipped).
func (b *batchRun) runChunk(w int, ch *leasedChunk) {
	now := b.now()
	b.leases.set(w, func(r *workerRow) {
		*r = workerRow{chunk: ch.idx, chunkSize: len(ch.items), leasedAt: now, heartbeat: now}
	})
	defer b.leases.set(w, func(r *workerRow) { *r = workerRow{chunk: -1} })
	if b.cfg.onLease != nil {
		b.cfg.onLease(ch.idx)
	}
	tally := &chunkTally{}
	touch := func() {
		t := b.now()
		b.leases.set(w, func(r *workerRow) { r.heartbeat = t })
		if b.cfg.rep != nil {
			TouchLiveness(b.cfg.rep)
		}
	}
	for j, i := range ch.items {
		if b.workCtx.Err() != nil {
			return // incomplete: the ledger never hears of this chunk
		}
		// The run's write permit. A revoked fence (cancel, timeout, abandon,
		// shutdown, a lost stand-down lease) stops the run: the next item
		// could not write anyway, and counting it as an ordinary failure
		// would record its chunk done.
		if err := b.fence.Check(); err != nil {
			b.fail(fmt.Errorf("%s: %w", b.cfg.opID, err))
			return
		}
		if i < 0 {
			tally.skipped++
			tally.add("skipped:gone", 1)
			b.addRow(tally, database.ItemOutcome{Key: ch.keys[j], Outcome: "skipped:gone", Chunk: ch.idx})
			continue
		}
		label := ch.set.Label(i)
		if globalPauseGate.IsPaused() {
			if err := globalPauseGate.Wait(b.workCtx, b.cfg.rep, label); err != nil {
				return
			}
		}
		b.leases.set(w, func(r *workerRow) { r.itemIndex, r.item, r.heartbeat = j, label, b.now() })
		b.leases.mu.Lock()
		b.leases.current = label
		b.leases.mu.Unlock()
		if b.cfg.rep != nil {
			b.cfg.rep.SetCurrentItem(label)
		}
		// A fresh Tally per item, sealed when the item returns: a write a
		// goroutine the item left behind cannot flag the next item.
		itemTally := &ops.Tally{}
		err := b.runItem(ch, j, itemTally, touch)
		itemTally.Seal()
		touch()
		if err != nil && !errors.Is(err, ops.ErrPreview) && b.workCtx.Err() != nil {
			return // the item was cut short by the stop; it did not settle
		}
		// The Writer flagged, on the item's outcome (Tally.Refused), a write it
		// refused because the run is fenced or lost its lease. The item may have swallowed it and returned nil,
		// and as a chunk's last item that would record the chunk done: so
		// the chunk stays unfinished (a resume re-runs it) and the run ends.
		// An item that finished without a refusal settled even if the fence
		// was revoked meanwhile (a registry stop revokes, then cancels), so a
		// chunk whose items all finished cleanly is still delivered below.
		if refused := itemTally.Refused(); refused != nil {
			b.fail(fmt.Errorf("%s: item %s: %w", b.cfg.opID, ch.keys[j], errors.Join(refused, err)))
			return
		}
		// A refused write no live item owned (made on the run's context, an
		// unrelated one, or after its item returned) cannot be pinned on a
		// chunk, so this chunk is not recorded either: fail closed.
		if latched := b.latch.Err(); latched != nil {
			b.fail(fmt.Errorf("%s: a write was refused outside its item (ctx not the item's): %w", b.cfg.opID, latched))
			return
		}
		if err != nil && !errors.Is(err, ops.ErrPreview) {
			if stop := b.writeStopped(err); stop != nil {
				// Fenced or lease lost: the item did not settle, and neither
				// does its chunk. The run ends with this error.
				b.fail(fmt.Errorf("%s: item %s: %w", b.cfg.opID, ch.keys[j], stop))
				return
			}
		}
		for _, m := range itemTally.Marks() {
			if tally.marks == nil {
				tally.marks = map[string][]string{}
			}
			tally.marks[m] = append(tally.marks[m], ch.keys[j])
		}
		switch {
		case err == nil || errors.Is(err, ops.ErrPreview):
			if errors.Is(err, ops.ErrPreview) {
				tally.add("previewed", 1)
			}
			if reason := itemTally.SkipReason(); reason != "" {
				tally.skipped++
				tally.add("skipped:"+reason, 1)
				b.addRow(tally, database.ItemOutcome{Key: ch.keys[j], Outcome: "skipped:" + reason, Chunk: ch.idx})
			} else {
				tally.ok++
				tally.add("ok", 1)
			}
			for k, v := range itemTally.Counters() {
				tally.add(k, v)
			}
			if ch.vals != nil && !b.clearDirty(ch.keys[j], ch.vals[j]) {
				return
			}
		default:
			tally.failed++
			tally.add("failed", 1)
			b.addRow(tally, database.ItemOutcome{Key: ch.keys[j], Outcome: "failed", Error: err.Error(), Chunk: ch.idx})
			if b.job.StopOnError {
				b.fail(fmt.Errorf("%s: item %s: %w", b.cfg.opID, ch.keys[j], err))
				return
			}
		}
	}
	// Every item settled, so the chunk is complete even if the run is
	// stopping now: deliver it. A plain send cannot block forever, because
	// ledgerLoop drains b.done until it is closed, after every worker returned.
	b.done <- completion{chunk: ch, tally: tally}
}

// writeStopped reports why an item error stops the run rather than counting
// as one failed item: the error says a write was refused because the run is
// fenced or lost its stand-down lease. (A refusal the item swallowed is
// caught before this, by Tally.Refused.)
func (b *batchRun) writeStopped(itemErr error) error {
	if errors.Is(itemErr, ops.ErrFenced) || errors.Is(itemErr, opswriter.ErrStandDownLost) ||
		errors.Is(itemErr, ErrScanStandDownLost) {
		return itemErr
	}
	return nil
}

// recoverCallback is deferred at the top of every runner goroutine that calls
// op code outside runItem: the source's Pages and Key (producer), an item's
// Label and Key (worker), a DirtySet's Load. Those goroutines are the
// runner's own, not the registry's, so an unrecovered panic there would take
// the server down. A panic fails the run with the stack logged; the chunk in
// hand is not delivered, so it stays unfinished for a resume.
func (b *batchRun) recoverCallback(where string) {
	if p := recover(); p != nil {
		b.cfg.log.Error("batch runner: op callback panicked", "op_id", b.cfg.opID, "goroutine", where,
			"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		b.fail(fmt.Errorf("%s: %s: op callback panicked: %v", b.cfg.opID, where, p))
	}
}

// runItem runs item j of the chunk with its own deadline (started after the pause gate)
// and a fresh tally.
//
// A panic in Item is recovered here and returned as the item's error, with
// the stack logged: a worker is a bare pool goroutine, and an unrecovered
// panic there would take the whole server down.
func (b *batchRun) runItem(ch *leasedChunk, j int, t *ops.Tally, touch func()) (err error) {
	defer func() {
		if p := recover(); p != nil {
			b.cfg.log.Error("batch runner: item panicked", "op_id", b.cfg.opID, "item", ch.keys[j],
				"panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			err = fmt.Errorf("item panicked: %v", p)
		}
	}()
	// The item's context is canceled with item-scoped causes the Writer
	// recognizes (ops.ErrItemTimedOut, ops.ErrItemReturned): a write refused
	// for them is the item's business, not a stopped run's.
	itemCtx, cancel := context.WithCancelCause(b.workCtx)
	cancelTimeout := context.CancelFunc(func() {})
	if b.job.ItemTimeout > 0 {
		itemCtx, cancelTimeout = context.WithTimeoutCause(itemCtx, b.job.ItemTimeout, ops.ErrItemTimedOut)
	}
	// The outer cancel first, so the timeout child inherits ErrItemReturned
	// as its cause; the child's own cancel would record context.Canceled.
	defer func() {
		cancel(ops.ErrItemReturned)
		cancelTimeout()
	}()
	return ch.set.Run(ops.NewItemRC(b.cfg.rc, itemCtx, t, touch), ch.items[j])
}

func (b *batchRun) addRow(t *chunkTally, row database.ItemOutcome) {
	t.rows = append(t.rows, row)
}

// clearDirty deletes a processed DirtySet entry only if its value (mark
// epoch) is unchanged; a re-mark during the item keeps the entry dirty.
func (b *batchRun) clearDirty(id string, val []byte) bool {
	if _, err := b.cfg.dirty.CompareAndDeleteRaw(b.job.Dirty.Prefix+id, val); err != nil {
		b.fail(fmt.Errorf("%s: clear dirty entry %s: %w", b.cfg.opID, id, err))
		return false
	}
	return true
}

// ── the ledger goroutine ───────────────────────────────────────────────────

// ledgerLoop is the ONLY writer of the run's ledger. It folds completions
// and commits ledger + lease table + progress in one batch, at most every
// commitEvery (a completion after a quiet spell commits at once), then a
// final commit when the workers are done.
func (b *batchRun) ledgerLoop() {
	// A quarter of the throttle, so a deferred commit lands within 1.25x
	// commitEvery of the one before it.
	tick := time.NewTicker(max(b.cfg.commitEvery/4, 5*time.Millisecond))
	defer tick.Stop()
	var (
		dirty      bool
		lastCommit time.Time
		rows       []database.ItemOutcome
		rowsSent   int
		samples    []rateSample
		closed     bool // the store refused: the run is terminal
	)
	commit := func(final bool) {
		if b.crashed.Load() {
			return
		}
		now := b.now()
		prog := b.progress(now, &samples, final)
		send := rows
		if room := maxItemRowsPerRun - rowsSent; len(send) > room {
			send = send[:max(room, 0)]
		}
		led := b.ledger.snapshot()
		led.UpdatedAt = now
		led.ChunkCount = b.chunkTotal()
		if b.cfg.store != nil && !closed {
			err := b.cfg.store.CommitLedgerV3(b.cfg.opID, database.LedgerBatch{
				Ledger: led, Leases: b.leaseCopy(), Progress: prog, ItemRows: send, Marks: b.ledger.pendingMarks(),
				ResetMarks: b.ledger.fresh})
			if err != nil {
				if errors.Is(err, database.ErrStaleTransition) || errors.Is(err, database.ErrRunNotFoundV3) {
					closed = true
				}
				b.cfg.log.Warn("batch runner: ledger commit failed", "op_id", b.cfg.opID, "error", err)
				return // stays dirty; retried on the next tick
			}
		}
		rowsSent += len(send)
		rows = nil
		b.ledger.clearPending()
		if b.cfg.store != nil && !closed {
			b.ledger.fresh = false // the old marks are gone
		}
		dirty, lastCommit = false, now
		if b.cfg.rep != nil {
			total := 0
			if prog.Total != nil {
				total = int(*prog.Total)
			}
			if err := b.cfg.rep.UpdateProgress(int(prog.Done), total, prog.Current); err != nil {
				b.cfg.log.Debug("batch runner: progress update failed", "op_id", b.cfg.opID, "error", err)
			}
		}
		if b.cfg.afterCommit != nil && b.cfg.afterCommit(led, prog) {
			b.crashed.Store(true)
			b.stop()
		}
	}
	// The resume point is visible at once: a resumed run shows the ledger's
	// counts before any chunk of this attempt completes.
	commit(false)
	for {
		select {
		case c, ok := <-b.done:
			if !ok {
				if !b.crashed.Load() {
					commit(true)
				}
				return
			}
			if b.crashed.Load() {
				continue // the world stopped: nothing more reaches the ledger
			}
			if b.table != nil {
				b.ledger.markDone(c.chunk.idx)
			} else if b.job.Order == ops.AppendOnly {
				b.ledger.addRange(c.chunk.first, c.chunk.last, c.chunk.after)
			} else {
				b.ledger.chunksDone++
			}
			b.ledger.fold(c.tally)
			rows = append(rows, c.tally.rows...)
			dirty = true
			if b.cfg.afterFold != nil && b.cfg.afterFold() {
				b.crashed.Store(true)
				b.stop()
				continue
			}
			if b.cfg.commitEvery < 0 || b.now().Sub(lastCommit) >= b.cfg.commitEvery {
				commit(false)
			}
		case <-tick.C:
			if b.markStuck() {
				dirty = true // a worker just went stuck: publish it
			}
			if dirty || b.now().Sub(lastCommit) >= progressRefreshEvery {
				if b.cfg.commitEvery < 0 || b.now().Sub(lastCommit) >= b.cfg.commitEvery {
					commit(false)
				}
			}
		}
	}
}

// rateSample is (time, done) at a commit, for items/s over the last 30 s.
type rateSample struct {
	at   time.Time
	done int64
}

// progress builds the typed progress snapshot from the ledger and the lease
// table. Done and the counters are the ledger's: completed chunks only.
func (b *batchRun) progress(now time.Time, samples *[]rateSample, final bool) database.ProgressSnapshot {
	l := b.ledger.l
	p := database.ProgressSnapshot{Done: l.Items.Done, Total: b.total, Failed: l.Items.Failed,
		Counters: maps.Clone(l.Counters), UpdatedAt: now,
		Chunks: &database.ChunkProgress{Done: b.ledger.chunksDone, Total: b.chunkTotal(), Size: b.size}}
	if !final {
		p.Workers = b.workerRows(now)
	}
	b.leases.mu.Lock()
	p.Current = b.leases.current
	b.leases.mu.Unlock()
	*samples = append(*samples, rateSample{at: now, done: p.Done})
	for len(*samples) > 1 && now.Sub((*samples)[0].at) > 30*time.Second {
		*samples = (*samples)[1:]
	}
	if s := *samples; len(s) > 1 {
		if dt := now.Sub(s[0].at).Seconds(); dt > 0 {
			p.RatePerS = float64(p.Done-s[0].done) / dt
		}
	}
	if p.Total != nil && p.RatePerS > 0 {
		eta := time.Duration(float64(*p.Total-p.Done) / p.RatePerS * float64(time.Second))
		p.ETA = &eta
	}
	return p
}

func (b *batchRun) stuckBudget() time.Duration {
	if b.job.ItemTimeout > 0 {
		return b.job.ItemTimeout
	}
	if b.cfg.stuckAfter > 0 {
		return b.cfg.stuckAfter
	}
	return defaultProgressTimeout
}

func (b *batchRun) workerRows(now time.Time) []database.WorkerProgress {
	budget := b.stuckBudget()
	b.leases.mu.Lock()
	defer b.leases.mu.Unlock()
	out := make([]database.WorkerProgress, len(b.leases.rows))
	for w, r := range b.leases.rows {
		out[w] = database.WorkerProgress{Worker: w, Chunk: r.chunk, Item: r.item, ItemIndex: r.itemIndex,
			ChunkSize: r.chunkSize, LeasedAt: r.leasedAt, Heartbeat: r.heartbeat,
			Stuck: r.chunk >= 0 && now.Sub(r.heartbeat) > budget}
	}
	return out
}

// markStuck logs, once per lease, a worker whose heartbeat is older than the
// item budget, and reports whether one just went stuck (the ledger goroutine
// then publishes the worker rows). It does not reassign the chunk: the goroutine may still be
// running it. The way out is cancel or restart; the worker row says "stuck",
// and a run whose every worker is stuck stops stamping liveness, which is
// what trips the watchdog.
func (b *batchRun) markStuck() (newly bool) {
	budget := b.stuckBudget()
	now := b.now()
	b.leases.mu.Lock()
	defer b.leases.mu.Unlock()
	for w := range b.leases.rows {
		r := &b.leases.rows[w]
		if r.chunk < 0 || r.stuckLogged || now.Sub(r.heartbeat) <= budget {
			continue
		}
		r.stuckLogged = true
		newly = true
		b.cfg.log.Warn("batch runner: worker stuck", "op_id", b.cfg.opID, "worker", w, "chunk", r.chunk,
			"item", r.item, "heartbeat_age", now.Sub(r.heartbeat).Round(time.Second))
	}
	return newly
}

// leaseCopy is the lease table as stored in opv3:ckpt: (active leases only).
func (b *batchRun) leaseCopy() []database.Lease {
	b.leases.mu.Lock()
	defer b.leases.mu.Unlock()
	var out []database.Lease
	for w, r := range b.leases.rows {
		if r.chunk < 0 {
			continue
		}
		out = append(out, database.Lease{Chunk: r.chunk, Worker: w, LeasedAt: r.leasedAt,
			Heartbeat: r.heartbeat, ItemIndex: r.itemIndex, Item: r.item})
	}
	return out
}

// concatSets joins drained pages into one index-addressed set.
type multiSet struct {
	sets   []ops.ItemSet
	starts []int
	n      int
}

func concatSets(sets []ops.ItemSet) ops.ItemSet {
	m := &multiSet{sets: sets}
	for _, s := range sets {
		m.starts = append(m.starts, m.n)
		m.n += s.Len()
	}
	return m
}

func (m *multiSet) locate(i int) (ops.ItemSet, int) {
	k := sort.Search(len(m.starts), func(k int) bool { return m.starts[k] > i }) - 1
	return m.sets[k], i - m.starts[k]
}

func (m *multiSet) Len() int { return m.n }
func (m *multiSet) Key(i int) string {
	s, j := m.locate(i)
	return s.Key(j)
}
func (m *multiSet) Partition(i int) string {
	s, j := m.locate(i)
	return s.Partition(j)
}
func (m *multiSet) Label(i int) string {
	s, j := m.locate(i)
	return s.Label(j)
}
func (m *multiSet) Run(rc *ops.ItemRC, i int) error {
	s, j := m.locate(i)
	return s.Run(rc, j)
}
