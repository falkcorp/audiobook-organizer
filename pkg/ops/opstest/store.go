// file: pkg/ops/opstest/store.go
// version: 1.0.1
// guid: 8e3c5a17-2f94-4b6d-a0c8-71d4e9b25f36
// last-edited: 2026-10-14

package opstest

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// worldStore is the store one generation's registry runs over: the World's
// real in-memory Pebble store, method by method, with every write admitted
// by the generation's gate (refused once a simulated crash killed the
// generation) and the writes and history rows a def can make recorded in
// the World's commit-ordered log.
//
// It implements database.OpsV2Store and every capability the registry
// resolves with database.AsCapability (TestWorldStoreServesEveryCapability
// checks that list against the registry's source), and it deliberately has
// no Unwrap: a capability it lacked would otherwise resolve to the bare
// Pebble store, whose writes are neither recorded nor refused after a
// crash, and every crash test would pass while proving nothing.
type worldStore struct {
	w *World
	g *generation
}

var (
	_ database.OpsV2Store       = (*worldStore)(nil)
	_ database.OpsV3ChunkStore  = (*worldStore)(nil)
	_ opswriter.BookStore       = (*worldStore)(nil)
	_ opswriter.BookFileStore   = (*worldStore)(nil)
	_ opswriter.AuthorStore     = (*worldStore)(nil)
	_ opswriter.HistoryRecorder = (*worldStore)(nil)
	_ opswriter.ChangeJournal   = (*worldStore)(nil)
	_ opswriter.IntentStore     = (*worldStore)(nil)
)

func (s *worldStore) p() *database.PebbleStore { return s.w.st() }

// write runs fn under the generation's gate.
func (s *worldStore) write(fn func(*database.PebbleStore) error) error {
	release, err := s.g.admit()
	if err != nil {
		return err
	}
	defer release()
	return fn(s.p())
}

// write2 is write for a method that also returns a value.
func write2[T any](s *worldStore, fn func(*database.PebbleStore) (T, error)) (T, error) {
	release, err := s.g.admit()
	if err != nil {
		var zero T
		return zero, err
	}
	defer release()
	return fn(s.p())
}

// ── v2 operation rows (database.OpsV2Store) ────────────────────────────────

func (s *worldStore) UpsertOpDefinitionV2(row database.OpDefinitionV2Row) error {
	return s.write(func(p *database.PebbleStore) error { return p.UpsertOpDefinitionV2(row) })
}

func (s *worldStore) DeleteOrphanOpDefsV2(keepIDs []string) error {
	return s.write(func(p *database.PebbleStore) error { return p.DeleteOrphanOpDefsV2(keepIDs) })
}

func (s *worldStore) InsertOperationV2(row database.OperationV2Row) error {
	return s.write(func(p *database.PebbleStore) error { return p.InsertOperationV2(row) })
}

func (s *worldStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	return s.p().GetOperationV2(id)
}

func (s *worldStore) UpdateOperationV2Params(id string, params []byte) error {
	return s.write(func(p *database.PebbleStore) error { return p.UpdateOperationV2Params(id, params) })
}

func (s *worldStore) IncrementResumeCountV2(id string) error {
	return s.write(func(p *database.PebbleStore) error { return p.IncrementResumeCountV2(id) })
}

func (s *worldStore) MarkOperationV2ManualRetry(id string) error {
	return s.write(func(p *database.PebbleStore) error { return p.MarkOperationV2ManualRetry(id) })
}

func (s *worldStore) SetOperationV2Result(id string, resultData string) error {
	return s.write(func(p *database.PebbleStore) error { return p.SetOperationV2Result(id, resultData) })
}

func (s *worldStore) DeleteOperationV2(id string, allowedStatuses []string) (string, bool, error) {
	release, err := s.g.admit()
	if err != nil {
		return "", false, err
	}
	defer release()
	return s.p().DeleteOperationV2(id, allowedStatuses)
}

func (s *worldStore) UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error {
	return s.write(func(p *database.PebbleStore) error {
		return p.UpdateOperationV2Status(id, status, startedAt, completedAt, errMsg)
	})
}

func (s *worldStore) UpdateOperationV2StatusIf(id, fromStatus, status string, startedAt, completedAt *time.Time, errMsg *string) (bool, error) {
	return write2(s, func(p *database.PebbleStore) (bool, error) {
		return p.UpdateOperationV2StatusIf(id, fromStatus, status, startedAt, completedAt, errMsg)
	})
}

func (s *worldStore) ResetOperationV2ForResume(id string) error {
	return s.write(func(p *database.PebbleStore) error { return p.ResetOperationV2ForResume(id) })
}

func (s *worldStore) RepairOpsV2MissingCompletedAt() (int, error) {
	return write2(s, func(p *database.PebbleStore) (int, error) { return p.RepairOpsV2MissingCompletedAt() })
}

// ListQueuedOperationsV2 is the dispatcher's read of the queue, one call per
// dispatch cycle; the generation counts the calls so AbandonAt can wait for
// a whole cycle that saw its second run.
func (s *worldStore) ListQueuedOperationsV2() ([]database.OperationV2Row, error) {
	n := s.g.cycleBegins()
	rows, err := s.p().ListQueuedOperationsV2()
	if err == nil {
		s.g.cycleListed(n, rows)
	}
	return rows, err
}

func (s *worldStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return s.p().ListActiveOperationsV2()
}

func (s *worldStore) ListResumableOperationsV2() ([]database.OperationV2Row, error) {
	return s.p().ListResumableOperationsV2()
}

func (s *worldStore) ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error) {
	return s.p().ListOperationsV2Since(since, limit)
}

func (s *worldStore) CountRunningByPluginV2(plugin string) (int, error) {
	return s.p().CountRunningByPluginV2(plugin)
}

func (s *worldStore) PromoteToQueued(id string) error {
	return s.write(func(p *database.PebbleStore) error { return p.PromoteToQueued(id) })
}

func (s *worldStore) ListWaitingDepsOps() ([]database.OperationV2Row, error) {
	return s.p().ListWaitingDepsOps()
}

func (s *worldStore) GetOpStateV2(opID string) (*database.OpStateV2Row, error) {
	return s.p().GetOpStateV2(opID)
}

func (s *worldStore) DeleteOpStateV2(opID string) error {
	return s.write(func(p *database.PebbleStore) error { return p.DeleteOpStateV2(opID) })
}

func (s *worldStore) UpsertOpStateV2(row database.OpStateV2Row) error {
	return s.write(func(p *database.PebbleStore) error { return p.UpsertOpStateV2(row) })
}

func (s *worldStore) UpdateOpCheckpointV2(id string, newHWM int) error {
	return s.write(func(p *database.PebbleStore) error { return p.UpdateOpCheckpointV2(id, newHWM) })
}

func (s *worldStore) UpdateOpProgressV2(id string, current, total int, message string) error {
	return s.write(func(p *database.PebbleStore) error { return p.UpdateOpProgressV2(id, current, total, message) })
}

func (s *worldStore) SetOpQueuedProgressV2(id string, current, total int, message string) (bool, error) {
	return write2(s, func(p *database.PebbleStore) (bool, error) {
		return p.SetOpQueuedProgressV2(id, current, total, message)
	})
}

func (s *worldStore) UpdateOpPhaseV2(id string, phase *string) error {
	return s.write(func(p *database.PebbleStore) error { return p.UpdateOpPhaseV2(id, phase) })
}

func (s *worldStore) AppendOpLogsV2(rows []database.OpLogV2Row) error {
	return s.write(func(p *database.PebbleStore) error { return p.AppendOpLogsV2(rows) })
}

func (s *worldStore) GetOpLogsV2(opID string, limit int) ([]database.OpLogV2Row, error) {
	return s.p().GetOpLogsV2(opID, limit)
}

func (s *worldStore) InsertOpErrorV2(row database.OpErrorV2Row) error {
	return s.write(func(p *database.PebbleStore) error { return p.InsertOpErrorV2(row) })
}

func (s *worldStore) InsertOpStrikeV2(row database.OpStrikeV2Row) error {
	return s.write(func(p *database.PebbleStore) error { return p.InsertOpStrikeV2(row) })
}

func (s *worldStore) GetDepRev(sub database.OpSubject) (uint64, error) { return s.p().GetDepRev(sub) }

func (s *worldStore) BumpDepRev(sub database.OpSubject) (uint64, error) {
	return write2(s, func(p *database.PebbleStore) (uint64, error) { return p.BumpDepRev(sub) })
}

func (s *worldStore) RecordOpCompletion(sub database.OpSubject, opType, fileID string, depRev uint64) error {
	return s.write(func(p *database.PebbleStore) error { return p.RecordOpCompletion(sub, opType, fileID, depRev) })
}

func (s *worldStore) GetOpCompletion(sub database.OpSubject, opType string) (uint64, bool, error) {
	return s.p().GetOpCompletion(sub, opType)
}

func (s *worldStore) ListFileCompletions(sub database.OpSubject, opType string) (map[string]uint64, error) {
	return s.p().ListFileCompletions(sub, opType)
}

func (s *worldStore) AddToBatchBucket(opType string, sub database.OpSubject) error {
	return s.write(func(p *database.PebbleStore) error { return p.AddToBatchBucket(opType, sub) })
}

func (s *worldStore) ListBatchBucket(opType string) ([]database.BatchBucketEntry, error) {
	return s.p().ListBatchBucket(opType)
}

func (s *worldStore) ClearBatchBucket(opType string, subs []database.OpSubject) error {
	return s.write(func(p *database.PebbleStore) error { return p.ClearBatchBucket(opType, subs) })
}

// ── v3 run state (the registry's runStateV3) ───────────────────────────────

func (s *worldStore) GetRunV3(id string) (*database.RunRecord, error) { return s.p().GetRunV3(id) }

func (s *worldStore) TransitionRunV3(req database.TransitionRequest) (*database.RunRecord, error) {
	return write2(s, func(p *database.PebbleStore) (*database.RunRecord, error) { return p.TransitionRunV3(req) })
}

// ── the runner's chunk store (database.OpsV3ChunkStore) ────────────────────

func (s *worldStore) PutSnapshotPageV3(id string, page int, keys []string) error {
	return s.write(func(p *database.PebbleStore) error { return p.PutSnapshotPageV3(id, page, keys) })
}

func (s *worldStore) GetSnapshotPageV3(id string, page int) ([]string, bool, error) {
	return s.p().GetSnapshotPageV3(id, page)
}

func (s *worldStore) PutChunkOffsetsV3(id string, offsets []int32) error {
	return s.write(func(p *database.PebbleStore) error { return p.PutChunkOffsetsV3(id, offsets) })
}

func (s *worldStore) GetChunkOffsetsV3(id string) ([]int32, bool, error) {
	return s.p().GetChunkOffsetsV3(id)
}

// CommitLedgerV3 commits, then tells the run's attempt what landed (after
// the gate is released: the attempt may stop the world in response).
func (s *worldStore) CommitLedgerV3(id string, b database.LedgerBatch) error {
	if err := s.write(func(p *database.PebbleStore) error { return p.CommitLedgerV3(id, b) }); err != nil {
		return err
	}
	if a := s.g.attemptOf(id); a != nil {
		a.observeCommit(b.Ledger)
	}
	return nil
}

func (s *worldStore) GetLedgerV3(id string) (*database.Ledger, error) { return s.p().GetLedgerV3(id) }

func (s *worldStore) ListMarksV3(id string) (map[string][]string, error) {
	return s.p().ListMarksV3(id)
}

// ── DirtySet sources (the registry's dirtySetStore) ────────────────────────

// ScanPrefixPage pages a raw key prefix (a DirtySet source's dirty set).
func (s *worldStore) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	return s.p().ScanPrefixPage(prefix, after, limit)
}

// CompareAndDeleteRaw deletes key when it still holds expect. The Pebble
// store has no such primitive yet (the dirty-set marker, 02 PR 14, adds
// it), so production refuses every DirtySet source with
// registry.ErrDirtySetUnsupported; the World supplies it, atomic against
// every other dirty-set write the World makes (World.MarkDirty), so a
// DirtySet def can be run through Conformance before that lands.
func (s *worldStore) CompareAndDeleteRaw(key string, expect []byte) (bool, error) {
	return write2(s, func(p *database.PebbleStore) (bool, error) {
		s.w.dirtyMu.Lock()
		defer s.w.dirtyMu.Unlock()
		cur, err := p.GetRaw(key)
		if err != nil || cur == nil || string(cur) != string(expect) {
			return false, err
		}
		if err := p.DeleteRaw(key); err != nil {
			return false, err
		}
		s.w.log.addClear(key)
		return true, nil
	})
}

// ── the Writer's stores ────────────────────────────────────────────────────

func (s *worldStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return s.w.modifyBook(s.g, id, fn)
}

func (s *worldStore) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	return s.p().GetBookFileByID(bookID, fileID)
}

func (s *worldStore) GetBookFileByPath(filePath string) (*database.BookFile, error) {
	return s.p().GetBookFileByPath(filePath)
}

func (s *worldStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	return s.w.modifyBookFile(s.g, bookID, fileID, fn)
}

func (s *worldStore) ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
	return s.w.modifyBookAuthors(s.g, bookID, fn)
}

func (s *worldStore) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	return s.w.recordMetadataChange(s.g, r)
}

func (s *worldStore) CreateOperationChange(c *database.OperationChange) error {
	return s.w.createOperationChange(s.g, c)
}

func (s *worldStore) PutIntentV3(id string, in database.Intent) error {
	return s.write(func(p *database.PebbleStore) error { return p.PutIntentV3(id, in) })
}

func (s *worldStore) ClearIntentV3(id string, seq uint64) error {
	return s.write(func(p *database.PebbleStore) error { return p.ClearIntentV3(id, seq) })
}

func (s *worldStore) ListIntentsV3(id string) ([]database.Intent, error) {
	return s.p().ListIntentsV3(id)
}

// GetBookChangeHistory reads a book's metadata history rows (a read). No
// capability the registry resolves on this branch needs it. The Writer's
// evidence reader that 05-PR5b (#4017) adds is opswriter.BookHistoryReader,
// whose ReadBookChangeHistory also counts the rows that did not decode, which
// this method drops silently; the PR that merges second replaces this
// forwarder with ReadBookChangeHistory.
func (s *worldStore) GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error) {
	return s.p().GetBookChangeHistory(bookID, limit)
}

// ── generations ─────────────────────────────────────────────────────────────

// generation is one process lifetime of the World: the registry that runs
// in it and the gate every write it makes passes. A simulated crash kills
// the generation: the gate waits for the writes in flight, then refuses
// every later one for good, so neither the dying registry (its failed
// terminal status, its Shutdown's interrupted stamps) nor a goroutine it
// leaked can write after the crash. The next boot is a new generation over
// the same store.
type generation struct {
	w *World

	gate    sync.RWMutex
	dead    bool
	deadErr error // what a write after death gets: errWorldStopped for a crash, errWorldReset or errWorldClosed after a retire

	mu       sync.Mutex
	cond     *sync.Cond
	attempts map[string]*attempt // by op id
	expect   []*attempt          // attempts enqueued whose op id the registry has not run yet
	cycles   int                 // dispatch cycles begun (ListQueuedOperationsV2 calls)
	listed   map[string]int      // op id -> the first cycle whose queue read listed it

	reg    *registry.Registry
	cancel context.CancelFunc
	sdSkew atomic.Int64 // the stand-down clock's lead over the wall clock (ns)
}

func newGeneration(w *World) *generation {
	g := &generation{w: w, attempts: map[string]*attempt{}, listed: map[string]int{}}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// admit is taken by every write of the generation for its duration; it
// refuses once the generation is dead. A refusal after a retire (the World
// was Reset, or its test finished) is a goroutine a def left behind, and is
// recorded: it is logged, as the World's testing.TB can no longer be used.
func (g *generation) admit() (release func(), err error) {
	g.gate.RLock()
	if g.dead {
		err := g.deadErr
		g.gate.RUnlock()
		if err != errWorldStopped {
			logger.New("opstest").Warn("a write was refused and dropped: %v", err)
		}
		return nil, err
	}
	return g.gate.RUnlock, nil
}

// kill is the crash: it waits for the writes in flight, then refuses every
// later one. Never call it while holding the gate (from inside a write).
func (g *generation) kill() { g.retire(errWorldStopped) }

// retire is kill with the reason a later write is refused for. The World
// retires the generation it replaces (Reset) and the one it ends with
// (cleanup) before it closes the store under them, so a write that outlives
// its run gets an error, never a closed or missing store. A retire overrides
// an earlier crash's reason.
func (g *generation) retire(reason error) {
	g.gate.Lock()
	g.dead = true
	g.deadErr = reason
	g.gate.Unlock()
}

func (g *generation) isDead() bool {
	g.gate.RLock()
	defer g.gate.RUnlock()
	return g.dead
}

func (g *generation) attemptOf(opID string) *attempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.attempts[opID]
}

func (g *generation) cycleBegins() int {
	g.mu.Lock()
	g.cycles++
	n := g.cycles
	g.mu.Unlock()
	g.cond.Broadcast()
	return n
}

func (g *generation) cycleListed(n int, rows []database.OperationV2Row) {
	g.mu.Lock()
	for _, r := range rows {
		if _, ok := g.listed[r.ID]; !ok {
			g.listed[r.ID] = n
		}
	}
	g.mu.Unlock()
	g.cond.Broadcast()
}
