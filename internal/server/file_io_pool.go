// file: internal/server/file_io_pool.go
// version: 2.3.4
// guid: c4d5e6f7-a8b9-0c1d-2e3f-4a5b6c7d8e9f
// last-edited: 2026-07-03
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
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	ch       chan fileIOJobEntry
	wg       sync.WaitGroup
	stopped  int32
	pending  sync.Map      // "{bookID}:{opType}" -> FileIOJob, for in-memory tracking
	overflow chan struct{} // semaphore to limit overflow goroutines
	// store is the database backing the pending-op persistence layer.
	// Set via SetStore after construction (SERVER-GLOBAL-STORE-AUDIT
	// phase 3a). Nil-safe — the three persistence helpers all no-op
	// when nil, matching the prior GetGlobalStore == nil branch.
	store database.Store
}

// SetStore sets the store the pool uses to persist pending file ops.
// Idempotent. Pass nil to disable persistence (no recovery on restart).
func (p *FileIOPool) SetStore(s database.Store) { p.store = s }

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
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	slog.Info("file I/O pool started with workers, buffer 500", "workers", workers)
	return p
}

func (p *FileIOPool) worker(id int) {
	defer p.wg.Done()
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

// Submit queues a file I/O job with the default "apply_metadata" op type.
func (p *FileIOPool) Submit(bookID string, fn func()) {
	p.SubmitTyped(bookID, "apply_metadata", fn)
}

// SubmitTyped queues a file I/O job with a specific operation type.
func (p *FileIOPool) SubmitTyped(bookID, opType string, fn func()) {
	if atomic.LoadInt32(&p.stopped) == 1 {
		slog.Warn("file I/O pool stopped, dropping job for book (op)", "bookID", bookID, "opType", opType)
		return
	}
	job := FileIOJob{BookID: bookID, OpType: opType, CreatedAt: time.Now()}
	p.pending.Store(pendingKey(bookID, opType), job)
	p.storePendingFileOp(job)

	select {
	case p.ch <- fileIOJobEntry{bookID: bookID, opType: opType, fn: fn}:
	default:
		p.overflow <- struct{}{}
		slog.Warn("file I/O pool buffer full, running overflow for book (op)", "bookID", bookID, "opType", opType)
		go func() {
			defer func() { <-p.overflow }()
			fn()
			p.pending.Delete(pendingKey(bookID, opType))
			p.removePendingFileOp(bookID, opType)
		}()
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
	if !atomic.CompareAndSwapInt32(&p.stopped, 0, 1) {
		return
	}
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
		srv.metadataFetchService.ApplyMetadataFileIO(bookID)
		if _, err := srv.metadataFetchService.WriteBackMetadataForBook(bookID); err != nil {
			slog.Warn("recovery write-back for", "bookID", bookID, "err", err)
		}
		if srv.writeBackBatcher != nil {
			srv.writeBackBatcher.Enqueue(bookID)
		}
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
