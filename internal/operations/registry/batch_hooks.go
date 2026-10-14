// file: internal/operations/registry/batch_hooks.go
// version: 1.0.0
// guid: 6b0f3c52-8e47-4d1a-9f2b-3c7a5e91d846
// last-edited: 2026-10-14

package registry

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// RunnerHooks are the batch runner's injection points for the operations
// test harness (pkg/ops/opstest, 05-PR6). Production never sets them: runV3
// leaves batchConfig.hooks nil, so every hook point below costs one nil
// check. A hook runs on the runner goroutine that reaches its point and may
// block it; the harness's seeded scheduler and its CrashMidChunk fault both
// do.
//
// It is a struct of optional funcs rather than an interface so a harness sets
// only the points it needs and production pays for nothing it does not use.
type RunnerHooks struct {
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

// BatchRunForTest is one attempt of a batch job as pkg/ops/opstest drives
// it: the production runner (runBatch) with the persistence, clock and hooks
// the harness injects. Never used by the executor.
type BatchRunForTest struct {
	OpID string
	Job  *ops.BatchJob
	// RC is the run context; its Context carries the run's refusal latch
	// (ops.NewRC installs one).
	RC *ops.RC
	// Store persists the snapshot, the ledger and the marks. nil runs with
	// no persistence (and so no resume).
	Store database.OpsV3ChunkStore
	// Now is the runner's clock; nil is time.Now.
	Now   func() time.Time
	Hooks *RunnerHooks
}

// RunBatchForTest runs one attempt of c.Job and returns the Summary its
// Finish saw, the Finish result and the run error. ctx is the run context:
// it carries the run's fence (opswriter.WithFence) as executeRun's does. The
// ledger commits after every chunk completion instead of throttling, so a
// stop-the-world hook sees each completion on the store at once. A hook that
// stopped the world makes it return errWorldStopped.
func RunBatchForTest(ctx context.Context, c BatchRunForTest) (ops.Summary, json.RawMessage, error) {
	return runBatch(ctx, batchConfig{opID: c.OpID, job: c.Job, rc: c.RC, store: c.Store, now: c.Now,
		commitEvery: -1, hooks: c.Hooks})
}
