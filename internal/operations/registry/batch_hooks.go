// file: internal/operations/registry/batch_hooks.go
// version: 1.1.0
// guid: 6b0f3c52-8e47-4d1a-9f2b-3c7a5e91d846
// last-edited: 2026-10-14

package registry

import (
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// RunnerHooks are the executor's and the batch runner's injection points for
// the operations test harness (pkg/ops/opstest, 05-PR6). Production never
// sets them: a registry built without Options.RunnerHooksForTest gives every
// run nil hooks, so each hook point below costs one nil check. A hook runs on
// the goroutine that reaches its point and may block it; the harness's
// seeded scheduler and its fault injectors do.
//
// They are per run: Options.RunnerHooksForTest is asked for each op id, so
// two runs live in one registry (a stopped run and the run queued behind its
// key) are told apart. It is a struct of optional funcs rather than an
// interface so a harness sets only the points it needs.
type RunnerHooks struct {
	// Job runs once per attempt, after the def's OpenBatch built the job and
	// before the runner reads it: the harness reads the job's facts and may
	// override its Workers and ChunkSize.
	Job func(job *ops.BatchJob)
	// WrapWriter wraps the run's Writer (the one newV3Writer built) before
	// the run context is made, so every write an item makes goes through
	// the wrapper and then the production Writer.
	WrapWriter func(w ops.Writer) ops.Writer
	// Now is the runner's clock (batchConfig.now); nil is time.Now. The
	// ledger's commit throttle reads it, so a harness that advances it
	// decides when a folded completion is committed.
	Now func() time.Time
	// Summary receives the Summary the runner handed Finish (on a crash, the
	// one it had folded when the world stopped).
	Summary func(s ops.Summary)
	// Abandoned runs once, on the executor's goroutine, when the run's
	// goroutine outlived the abandon grace and the run became a zombie: its
	// worker slot is released and its key is still held.
	Abandoned func()
	// WorkerReady runs each time worker w is about to take its next chunk.
	WorkerReady func(w int)
	// WorkerExit runs once when worker w returns.
	WorkerExit func(w int)
	// ChunkLeased runs when worker w leases chunk with these item keys, in
	// the order the worker will run them. keys is the runner's own slice:
	// read it, do not keep or modify it.
	ChunkLeased func(w, chunk int, keys []string)
	// ItemStart runs right before an item's Item runs, on the item's
	// goroutine, with the item's run context (its Writer and its context).
	ItemStart func(it ItemInfo, rc *ops.ItemRC)
	// ItemDone runs right after the item returned, before the runner looks
	// at its outcome. Returning true stops the world there, like a process
	// kill: the item's chunk is not delivered, nothing more reaches the
	// ledger and Finish does not run.
	ItemDone func(it ItemInfo, err error) (stopWorld bool)
	// ChunkDone runs when worker w hands a completed chunk to the ledger
	// goroutine.
	ChunkDone func(w, chunk int)
	// BeforeLedgerCommit runs on the ledger goroutine before every commit
	// with the ledger about to be written (final: the run's last commit).
	// Returning true stops the world before that commit lands.
	BeforeLedgerCommit func(l database.Ledger, final bool) (stopWorld bool)
	// Progress receives every progress snapshot the ledger goroutine
	// publishes, after its commit succeeded: the same point the reporter's
	// UpdateProgress sees it.
	Progress func(p database.ProgressSnapshot)
	// Chunker replaces cutChunks when a Snapshot run freezes its chunk table
	// (same contract: the frozen order and the chunk start offsets into it).
	Chunker func(keys, parts []string, size int) (order, starts []int)
	// LeasePolicy decides, for each chunk of a Snapshot run's table in
	// order, which chunk to queue for leasing, or -1 for none. done reports
	// what the ledger records as complete. Unset, the runner queues exactly
	// the chunks done does not report.
	LeasePolicy func(done func(chunk int) bool, chunk int) int
}

// runnerHooksFor is the hooks of run opID: nil in production.
func (r *Registry) runnerHooksFor(opID string) *RunnerHooks {
	if r.runnerHooksForTest == nil {
		return nil
	}
	return r.runnerHooksForTest(opID)
}

// jobForTest hands an opened job to the Job hook. Nil-safe.
func (h *RunnerHooks) jobForTest(job *ops.BatchJob) {
	if h != nil && h.Job != nil {
		h.Job(job)
	}
}

// writerForTest is the run's Writer as items see it: w, or the WrapWriter of it.
func (h *RunnerHooks) writerForTest(w *opswriter.Writer) ops.Writer {
	if h != nil && h.WrapWriter != nil {
		return h.WrapWriter(w)
	}
	return w
}

// clockForTest is the runner clock the hooks set, or nil (time.Now).
func (h *RunnerHooks) clockForTest() func() time.Time {
	if h == nil {
		return nil
	}
	return h.Now
}

// summaryForTest hands the run's Summary to the Summary hook. Nil-safe.
func (h *RunnerHooks) summaryForTest(s ops.Summary) {
	if h != nil && h.Summary != nil {
		h.Summary(s)
	}
}

// abandonedForTest tells the Abandoned hook its run became a zombie. Nil-safe.
func (h *RunnerHooks) abandonedForTest() {
	if h != nil && h.Abandoned != nil {
		h.Abandoned()
	}
}

// ItemInfo names one item for the hooks.
type ItemInfo struct {
	Worker    int    // the worker running it
	Chunk     int    // the chunk it belongs to
	Index     int    // its position inside the chunk
	Key       string // Source.Key of the item
	Partition string // PartitionBy of the item, "" when the def does not partition
}

// hookItemForTest builds the ItemInfo of item j of ch run by worker w. Only
// called when a hook is set.
func hookItemForTest(w int, ch *leasedChunk, j int) ItemInfo {
	return ItemInfo{Worker: w, Chunk: ch.idx, Index: j, Key: ch.keys[j], Partition: ch.set.Partition(ch.items[j])}
}
