// file: internal/server/file_io_pool.go
// version: 2.9.0
// guid: c4d5e6f7-a8b9-0c1d-2e3f-4a5b6c7d8e9f
// last-edited: 2026-09-12
//
// Bounded worker pool for file I/O operations (cover embed, tag write,
// rename). Tracks pending jobs in PebbleDB so they survive restarts.
//
// Tracking key schema: pending_file_op:{bookID}:{opType}. Multiple op
// types for the same book coexist without clobbering. Recovery looks
// each opType up in recoveryDispatch.

package server

import (
	"encoding/json"
	"log/slog"

	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

const pendingFileOpPrefix = "pending_file_op:"

// FileIOJob tracks a pending file I/O operation persistently.
type FileIOJob struct {
	BookID    string    `json:"book_id"`
	OpType    string    `json:"op_type"`
	CreatedAt time.Time `json:"created_at"`
}

// FileIOPool manages a bounded pool of workers for slow file operations.
// Jobs are tracked in PebbleDB so interrupted ones can be recovered on restart.
type FileIOPool struct {
	ch chan fileIOJobEntry
	wg sync.WaitGroup

	// submitMu serialises submitters against Stop. Submitters hold it for
	// reading across their whole check-and-act; Stop takes it for writing to
	// set stopped, so once Stop releases it no submitter can still be sitting
	// between "read stopped as false" and its send on p.ch.
	//
	// An atomic flag was not enough, and this is not a theoretical race. A
	// submitter that passed the check microseconds before Stop ran would send
	// on a channel Stop has since closed. That panic is unrecovered: the
	// recover() lives in the worker body (see worker), not in SubmitTyped, so
	// it takes the process down. The same window would let the overflow
	// goroutine's wg.Go run as a WaitGroup Add concurrent with Stop's Wait,
	// which panics on its own once the worker count has reached zero.
	submitMu sync.RWMutex
	stopped  bool

	pending  sync.Map      // "{bookID}:{opType}" -> FileIOJob, for in-memory tracking
	overflow chan struct{} // semaphore to limit overflow goroutines
	// store is the database backing the pending-op persistence layer.
	// Set via SetStore after construction (SERVER-GLOBAL-STORE-AUDIT
	// phase 3a). Nil-safe — the three persistence helpers all no-op
	// when nil, matching the prior GetGlobalStore == nil branch.
	store rawKVWriter
}

// SetStore sets the store the pool uses to persist pending file ops.
// Idempotent. Pass nil to disable persistence (no recovery on restart).
func (p *FileIOPool) SetStore(s rawKVWriter) { p.store = s }

type fileIOJobEntry struct {
	bookID string
	opType string
	fn     func()
}

var (
	GlobalFileIOPool   *FileIOPool
	globalFileIOPoolMu sync.Mutex

	// globalServer holds a Server reference used by the default recovery handler.
	globalServer *Server

	recoveryDispatch   = map[string]FileOpRecoveryFunc{}
	recoveryDispatchMu sync.RWMutex
)

// FileOpRecoveryFunc re-runs a specific file-op type for one book.
type FileOpRecoveryFunc func(bookID string)

// RegisterFileOpRecovery registers a recovery handler for a given op type.
// Overwrites any previous registration for the same type.
func RegisterFileOpRecovery(opType string, fn FileOpRecoveryFunc) {
	recoveryDispatchMu.Lock()
	recoveryDispatch[opType] = fn
	recoveryDispatchMu.Unlock()
}

func lookupFileOpRecovery(opType string) (FileOpRecoveryFunc, bool) {
	recoveryDispatchMu.RLock()
	fn, ok := recoveryDispatch[opType]
	recoveryDispatchMu.RUnlock()
	return fn, ok
}

// GetGlobalFileIOPool returns the pool safely.
func GetGlobalFileIOPool() *FileIOPool {
	globalFileIOPoolMu.Lock()
	p := GlobalFileIOPool
	globalFileIOPoolMu.Unlock()
	return p
}

// SetGlobalFileIOPool sets the pool safely.
func SetGlobalFileIOPool(p *FileIOPool) {
	globalFileIOPoolMu.Lock()
	GlobalFileIOPool = p
	globalFileIOPoolMu.Unlock()
}

// NewFileIOPool creates a pool with the given number of workers.
func NewFileIOPool(workers int) *FileIOPool {
	p := &FileIOPool{
		ch:       make(chan fileIOJobEntry, 500),
		overflow: make(chan struct{}, workers),
	}
	for i := range workers {
		p.wg.Go(func() { p.worker(i) })
	}
	slog.Info("file I/O pool started with workers, buffer 500", "workers", workers)
	return p
}

// worker drains p.ch until it is closed. It does NOT touch p.wg: the pool's
// only caller starts it via p.wg.Go, which owns the counter and decrements it
// when worker returns. Any new caller must do the same — and until 2026-09-07
// SubmitTyped's overflow path did not, starting a bare `go func()` that Stop's
// wg.Wait could not see. That goroutine calls fn() and then writes to the
// store, so a shutdown could return with it still running against a closing
// database.
func (p *FileIOPool) worker(id int) {
	for job := range p.ch {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("file I/O worker panicked on book (op)", "workerID", id, "bookID", job.bookID, "opType", job.opType, "r", r)
				}
			}()
			job.fn()
			p.pending.Delete(pendingKey(job.bookID, job.opType))
			p.removePendingFileOp(job.bookID, job.opType)
		}()
	}
}

// Submit queues a file I/O job with the default "apply_metadata" op type. It
// reports whether fn will run; false means the pool is stopped and the job was
// dropped, so a caller holding a resource for fn (the scan stand-down) must
// release it itself.
func (p *FileIOPool) Submit(bookID string, fn func()) bool {
	return p.SubmitTyped(bookID, "apply_metadata", fn)
}

// SubmitTyped queues a file I/O job with a specific operation type. Its result
// is Submit's: false when the job was dropped without running.
func (p *FileIOPool) SubmitTyped(bookID, opType string, fn func()) bool {
	if done, accepted := p.tryQueue(bookID, opType, fn); done {
		return accepted
	}

	// The worker buffer is full. Take an overflow slot BEFORE re-entering the
	// lock. This send is the pool's backpressure: it blocks until one of the
	// `workers` overflow goroutines finishes an arbitrarily slow fn(). Waiting
	// for that while holding submitMu would make Stop's write-lock acquisition
	// wait for it too, and Stop's 30-second budget only wraps wg.Wait() — it
	// would be blown before the timer ever started.
	p.overflow <- struct{}{}

	p.submitMu.RLock()
	defer p.submitMu.RUnlock()
	if p.stopped {
		// Stop ran while we were queued behind the semaphore. Drop the job but
		// leave the pending_file_op row that tryQueue persisted: that row is
		// the recovery mechanism, so the work is picked up on next start
		// rather than silently lost.
		<-p.overflow
		slog.Warn("file I/O pool stopped, dropping job for book (op)", "bookID", bookID, "opType", opType)
		return false
	}
	slog.Warn("file I/O pool buffer full, running overflow for book (op)", "bookID", bookID, "opType", opType)
	// wg.Go under the read lock. Stop cannot have reached wg.Wait(), because it
	// sets stopped under the write lock and we have just read it as false.
	p.wg.Go(func() {
		defer func() { <-p.overflow }()
		fn()
		p.pending.Delete(pendingKey(bookID, opType))
		p.removePendingFileOp(bookID, opType)
	})
	return true
}

// tryQueue records the job and attempts a non-blocking send onto the worker
// channel, holding submitMu for reading so Stop cannot close p.ch underneath
// the send. done reports whether the caller is finished: true when the job was
// queued (accepted) or the pool is stopped and the job was dropped (!accepted).
// done=false means the buffer was full and the caller must take the overflow path.
func (p *FileIOPool) tryQueue(bookID, opType string, fn func()) (done, accepted bool) {
	p.submitMu.RLock()
	defer p.submitMu.RUnlock()
	if p.stopped {
		slog.Warn("file I/O pool stopped, dropping job for book (op)", "bookID", bookID, "opType", opType)
		return true, false
	}
	job := FileIOJob{BookID: bookID, OpType: opType, CreatedAt: time.Now()}
	p.pending.Store(pendingKey(bookID, opType), job)
	p.storePendingFileOp(job)

	select {
	case p.ch <- fileIOJobEntry{bookID: bookID, opType: opType, fn: fn}:
		return true, true
	default:
		return false, false
	}
}

// Pending returns the number of queued jobs.
func (p *FileIOPool) Pending() int {
	return len(p.ch)
}

// PendingJobs returns all in-flight / queued jobs for observability.
func (p *FileIOPool) PendingJobs() []FileIOJob {
	var jobs []FileIOJob
	p.pending.Range(func(_, v any) bool {
		if job, ok := v.(FileIOJob); ok {
			jobs = append(jobs, job)
		}
		return true
	})
	return jobs
}

// PendingBookIDs returns all book IDs with at least one pending file op.
// Deduped across op types.
func (p *FileIOPool) PendingBookIDs() []string {
	seen := map[string]struct{}{}
	p.pending.Range(func(_, v any) bool {
		if job, ok := v.(FileIOJob); ok {
			seen[job.BookID] = struct{}{}
		}
		return true
	})
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// Stop drains the queue and waits for in-flight jobs to finish,
// with a 30-second timeout to prevent blocking shutdown indefinitely.
// Safe to call multiple times.
func (p *FileIOPool) Stop() {
	p.submitMu.Lock()
	if p.stopped {
		p.submitMu.Unlock()
		return
	}
	p.stopped = true
	p.submitMu.Unlock()

	// Safe to close now. Every submitter either returned before we took the
	// write lock, or is blocked acquiring the read lock and will read stopped
	// as true. None is mid-send on p.ch, and no wg.Go can still be pending —
	// which is what makes the wg.Wait below a complete join rather than a join
	// of whichever goroutines happened to have registered by this instant.
	close(p.ch)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("file I/O pool stopped, all jobs complete")
	case <-time.After(30 * time.Second):
		slog.Warn("file I/O pool shutdown timed out after 30s, jobs may be incomplete", "p", p.Pending())
	}
}

// InitFileIOPool creates the global pool and registers the default recovery handler.
func InitFileIOPool() {
	SetGlobalFileIOPool(NewFileIOPool(4))
	RegisterFileOpRecovery("apply_metadata", func(bookID string) {
		srv := globalServer
		if srv == nil || srv.metadataFetchService == nil {
			slog.Warn("no server instance for apply_metadata recovery of book", "bookID", bookID)
			return
		}
		var enqueue func(string)
		if srv.writeBackBatcher != nil {
			enqueue = srv.writeBackBatcher.Enqueue
		}
		recoverApplyMetadataFileOp(srv.metadataFetchService, enqueue, bookID)
	})
}

// RecoverInterruptedFileOps re-queues any interrupted file I/O jobs.
// Called from the server startup sequence after services are ready.
func RecoverInterruptedFileOps(pool *FileIOPool) {
	recoverInterruptedFileOps(pool)
}

// --- Persistent tracking via PebbleDB ---

func pendingKey(bookID, opType string) string {
	return bookID + ":" + opType
}

func pebbleKey(bookID, opType string) string {
	return pendingFileOpPrefix + bookID + ":" + opType
}

// parsePebbleKey splits a stored key back into (bookID, opType).
// Accepts legacy keys without opType ("pending_file_op:{bookID}"), treating them as apply_metadata.
func parsePebbleKey(key string) (bookID, opType string, ok bool) {
	rest := strings.TrimPrefix(key, pendingFileOpPrefix)
	if rest == key {
		return "", "", false
	}
	// Last ":" separates bookID from opType. Book IDs shouldn't contain ":"
	// but be defensive: split on the last colon only.
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return rest, "apply_metadata", true
	}
	return rest[:idx], rest[idx+1:], true
}

// storePendingFileOp is a method on FileIOPool so it uses the pool's
// configured store (set via SetStore from NewServer) instead of
// reaching for database.GetGlobalStore (SERVER-GLOBAL-STORE-AUDIT
// phase 3a). Nil-safe — no-op if the pool has no store.
func (p *FileIOPool) storePendingFileOp(job FileIOJob) {
	if p == nil || p.store == nil {
		return
	}
	data, _ := json.Marshal(job)
	_ = p.store.SetRaw(pebbleKey(job.BookID, job.OpType), data)
}

// removePendingFileOp clears the persisted record for a finished job.
// See storePendingFileOp for the audit-phase rationale.
func (p *FileIOPool) removePendingFileOp(bookID, opType string) {
	if p == nil || p.store == nil {
		return
	}
	_ = p.store.DeleteRaw(pebbleKey(bookID, opType))
}

// recoverInterruptedFileOps re-queues any file I/O jobs that were in-flight
// when the server last shut down (or crashed). Uses the pool's
// configured store rather than database.GetGlobalStore
// (SERVER-GLOBAL-STORE-AUDIT phase 3a).
func recoverInterruptedFileOps(pool *FileIOPool) {
	if pool == nil || pool.store == nil {
		return
	}
	store := pool.store

	keys, err := store.ScanPrefix(pendingFileOpPrefix)
	if err != nil || len(keys) == 0 {
		return
	}

	slog.Info("recovering interrupted file I/O operations", "keys_count", len(keys))

	for _, kv := range keys {
		var job FileIOJob
		if err := json.Unmarshal(kv.Value, &job); err != nil {
			_ = store.DeleteRaw(kv.Key)
			continue
		}

		// Backfill fields for legacy entries that predate the opType split.
		if job.BookID == "" || job.OpType == "" {
			if bid, op, ok := parsePebbleKey(kv.Key); ok {
				if job.BookID == "" {
					job.BookID = bid
				}
				if job.OpType == "" {
					job.OpType = op
				}
			}
		}
		if job.OpType == "" {
			job.OpType = "apply_metadata"
		}
		if job.BookID == "" {
			_ = store.DeleteRaw(kv.Key)
			continue
		}

		fn, ok := lookupFileOpRecovery(job.OpType)
		if !ok {
			slog.Warn("no recovery handler for op type, removing stale key", "opType", job.OpType, "bookID", job.BookID)
			_ = store.DeleteRaw(kv.Key)
			continue
		}

		bookID := job.BookID
		opType := job.OpType
		slog.Info("re-queuing file I/O for book (type, started)", "bookID", bookID, "opType", opType, "value2", job.CreatedAt.Format(time.RFC3339))
		if pool != nil {
			pool.SubmitTyped(bookID, opType, func() { fn(bookID) })
		}
	}
}

// applyMetadataRecoverer is the slice of *metafetch.Service the apply_metadata
// recovery replay needs. It deliberately does not include ApplyMetadataFileIO
// or WriteBackMetadataForBook: calling those two in a row is what tagged every
// file twice.
type applyMetadataRecoverer interface {
	// checkpoint, when non-nil, is the caller's scan stand-down check, re-run
	// before each file-writing step; nil means the caller holds none.
	FinishApplyFileWork(id, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error) error
}

// recoverApplyMetadataFileOp replays an interrupted apply's file work through
// the shared sequel, which writes the tags exactly once. Both apply_metadata
// recovery registrations (here and in server.go) route through it; they used
// to call ApplyMetadataFileIO and then WriteBackMetadataForBook, and with
// auto_write_tags_on_apply on the first had already written the tags. The
// pending cover URL was not persisted with the job, so nothing is downloaded.
// FinishApplyFileWork takes the path lock itself, per write, so the replay is
// locked exactly like the live job.
func recoverApplyMetadataFileOp(svc applyMetadataRecoverer, enqueue func(string), bookID string) {
	if err := svc.FinishApplyFileWork(bookID, "", true, true, nil); err != nil {
		slog.Warn("recovery apply file work failed", "bookID", bookID, "err", err)
	}
	if enqueue != nil {
		enqueue(bookID)
	}
}

// autoFetchFileOpType is the file-I/O pool op type for auto-fetch's file work.
// It is separate from apply_metadata so a replay after a restart runs the
// auto-fetch rules (never create a library copy), not a manual apply's.
const autoFetchFileOpType = "auto_fetch_file_work"

// autoFetchRecoverer is the slice of *metafetch.Service the auto-fetch replay needs.
type autoFetchRecoverer interface {
	FinishAutoFetchFileWork(id, pendingCoverURL string, writeTags bool) error
}

// recoverAutoFetchFileOp replays interrupted auto-fetch file work. It is
// resubmitted on the pool by RecoverInterruptedFileOps and, like the live job,
// takes no lock here: FinishAutoFetchFileWork locks each write on the path it
// writes (the library copy's for a protected book).
func recoverAutoFetchFileOp(svc autoFetchRecoverer, bookID string) {
	if err := svc.FinishAutoFetchFileWork(bookID, "", config.AppConfig.WriteBackMetadata); err != nil {
		slog.Warn("recovery auto-fetch file work failed", "bookID", bookID, "err", err)
	}
}

// newAutoFetchScheduler returns the metafetch.FileWorkScheduler the server
// wires: the work is queued on the file-I/O pool under its own op type, so a
// restart replays it with the auto-fetch rules. With no pool (test servers) it
// runs inline.
//
// It takes no path lock. The file work locks for itself
// (metafetch.Service.SetPathLocker, wired in server.go), on the library copy's
// path for a protected book and on the post-rename path for the tag write.
// Until 2026-09-12 this wrapped the whole job in a lock on the ORIGINAL
// book's path: for a protected book nothing wrote that path, and the tag write
// ran after the rename under the pre-rename key.
func newAutoFetchScheduler(pool func() *FileIOPool) metafetch.FileWorkScheduler {
	return func(bookID string, work func()) {
		p := pool()
		if p == nil {
			work()
			return
		}
		p.SubmitTyped(bookID, autoFetchFileOpType, work)
	}
}
