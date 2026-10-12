// file: internal/operations/registry/export_test.go
// version: 1.5.0
// guid: 5c45952c-3393-4f33-9784-efd565ec8c49
// last-edited: 2026-10-10

package registry

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// WatchdogCycleForTest runs one watchdog inspection pass synchronously, so an
// external test can step the watchdog against Options.LivenessNow instead of
// waiting on its ticker. Compiled into test binaries only.
func (r *Registry) WatchdogCycleForTest() { r.watchdogCycle() }

// SetReporterClockForTest replaces a DB reporter's clock. Call before the first UpdateProgress.
func SetReporterClockForTest(rep Reporter, now func() time.Time) {
	r, ok := rep.(*dbReporter)
	if !ok {
		panic("SetReporterClockForTest: reporter is not a *dbReporter")
	}
	r.progressMu.Lock()
	r.nowFn = now
	r.progressMu.Unlock()
}

// ScanStandDownDefIDForTest is the library.scan def id (and ConcurrencyKey)
// the registry quiesces, so external tests use the real constant rather than
// a copied string.
const ScanStandDownDefIDForTest = scanStandDownDefID

// DispatchCycleForTest runs one dispatcher pass synchronously.
func (r *Registry) DispatchCycleForTest(ctx context.Context) { r.dispatchCycle(ctx) }

// HeldConcurrencyKeysForTest returns a copy of the held ConcurrencyKeys,
// mapped to the op id holding each. The holder matters: a key can be released
// by one op and claimed by the next between two reads, so a test that asks
// "does op X still hold a key" must check the holder, not only the key set.
func (r *Registry) HeldConcurrencyKeysForTest() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	held := make(map[string]string, len(r.concurrencyKeys))
	for k, holder := range r.concurrencyKeys {
		held[k] = holder
	}
	return held
}

// BoundToForTest reports whether f was built bound to done: the channel its
// Check watches for the run context ending. It compares channels, not
// behavior, because a behavioral probe (Check() right after ctx.Done()) races
// executeRun's own revoke in its select and passes either way.
func (f Fence) BoundToForTest(done <-chan struct{}) bool {
	return f.st != nil && f.st.done != nil && f.st.done == done
}

// NopOpsV3 answers every database.OpsV3Store method as a store that holds no
// v3 run records (05-PR4): GetRunV3 finds nothing, so the registry's v3 paths
// (markStopping, the boot sweep, the requeue successor) fall back to exactly
// what they did before, and the writers report ErrRunNotFoundV3. Registry test
// fakes embed it so registry.New keeps accepting them; a test of the v3 paths
// uses a real Pebble store instead.
type NopOpsV3 struct{}

func (NopOpsV3) InsertRunV3(database.RunRecord) error { return database.ErrRunNotFoundV3 }
func (NopOpsV3) TransitionRunV3(database.TransitionRequest) (*database.RunRecord, error) {
	return nil, database.ErrRunNotFoundV3
}
func (NopOpsV3) DeleteRunV3(string, []state.V3) (bool, error) { return false, nil }
func (NopOpsV3) GetRunV3(string) (*database.RunRecord, error) { return nil, nil }
func (NopOpsV3) ListRunsV3(database.RunQuery) (database.RunPage, error) {
	return database.RunPage{}, nil
}
func (NopOpsV3) OpCountsV3() (database.OpCounts, error)             { return database.OpCounts{}, nil }
func (NopOpsV3) ListQueuedRunsV3(int) ([]database.RunRecord, error) { return nil, nil }
func (NopOpsV3) ListDoneRunsSinceV3(time.Time, int) ([]database.RunRecord, bool, error) {
	return nil, true, nil
}
func (NopOpsV3) ListChildRunsV3(string) ([]database.RunRecord, error)       { return nil, nil }
func (NopOpsV3) ListRunEventsV3(string, int) ([]database.Transition, error) { return nil, nil }
func (NopOpsV3) SaveCheckpointV3(string, database.Checkpoint) error {
	return database.ErrRunNotFoundV3
}
func (NopOpsV3) GetCheckpointV3(string) (*database.Checkpoint, error)  { return nil, nil }
func (NopOpsV3) PutSnapshotPageV3(string, int, []string) error         { return nil }
func (NopOpsV3) GetSnapshotPageV3(string, int) ([]string, bool, error) { return nil, false, nil }
func (NopOpsV3) PutChunkOffsetsV3(string, []int32) error               { return nil }
func (NopOpsV3) GetChunkOffsetsV3(string) ([]int32, bool, error)       { return nil, false, nil }
func (NopOpsV3) CommitLedgerV3(string, database.LedgerBatch) error     { return database.ErrRunNotFoundV3 }
func (NopOpsV3) GetLedgerV3(string) (*database.Ledger, error)          { return nil, nil }
func (NopOpsV3) PutIntentV3(string, database.Intent) error             { return nil }
func (NopOpsV3) ClearIntentV3(string, uint64) error                    { return nil }
func (NopOpsV3) ListIntentsV3(string) ([]database.Intent, error)       { return nil, nil }
func (NopOpsV3) PutItemOutcomesV3(string, []database.ItemOutcome) error {
	return database.ErrRunNotFoundV3
}
func (NopOpsV3) ListItemOutcomesV3(string, database.ItemQuery) (database.ItemPage, error) {
	return database.ItemPage{}, nil
}
func (NopOpsV3) AppendLogV3(string, database.LogLine) error                { return nil }
func (NopOpsV3) ListLogV3(string, int, string) ([]database.LogLine, error) { return nil, nil }

var _ database.OpsV3Store = NopOpsV3{}

// withNopV3 is a v2-only store (a mockery OpsV2Store mock) with NopOpsV3 for
// the v3 half.
type withNopV3 struct {
	database.OpsV2Store
	NopOpsV3
}

// WithNopV3 adapts a v2-only store to database.OpsStore.
func WithNopV3(s database.OpsV2Store) database.OpsStore { return withNopV3{OpsV2Store: s} }
