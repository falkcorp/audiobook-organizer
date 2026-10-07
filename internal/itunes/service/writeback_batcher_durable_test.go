// file: internal/itunes/service/writeback_batcher_durable_test.go
// version: 1.0.0
// guid: 6d2a8f43-1b7e-4c95-a3d0-9e5f2b7c4a18
// last-edited: 2026-10-07
//
// Regression tests for the 2026-10-07 write-back drop fix: the write-back root
// reaches the writer and the audit, failed batches are kept and retried with
// backoff (never dropped), removes are tombstoned only after the write lands,
// the queue survives a restart, over-cap removes are held, and backups are
// taken only for writes that will land. See
// docs/plans/2026-10-07-itunes-writeback-drops.md.

package itunesservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/itunes"
)

// kvStore is an in-memory raw KV shared across batcher instances (a restart).
type kvStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newKV() *kvStore { return &kvStore{m: map[string][]byte{}} }

func (kv *kvStore) has(key string) bool {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	_, ok := kv.m[key]
	return ok
}

func (kv *kvStore) keys(prefix string) []string {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	var out []string
	for k := range kv.m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// durableStore wires a MockStore to kv, plus one book owning one PID, and
// counts tombstones.
func durableStore(kv *kvStore, tombstones *atomic.Int32) *database.MockStore {
	st := writebackTestStore("book-1", "aabbccdd11223344")
	st.SetRawFunc = func(k string, v []byte) error {
		kv.mu.Lock()
		defer kv.mu.Unlock()
		kv.m[k] = append([]byte(nil), v...)
		return nil
	}
	st.GetRawFunc = func(k string) ([]byte, error) {
		kv.mu.Lock()
		defer kv.mu.Unlock()
		return kv.m[k], nil
	}
	st.DeleteRawFunc = func(k string) error {
		kv.mu.Lock()
		defer kv.mu.Unlock()
		delete(kv.m, k)
		return nil
	}
	st.ScanPrefixFunc = func(p string) ([]database.KVPair, error) {
		kv.mu.Lock()
		defer kv.mu.Unlock()
		var out []database.KVPair
		for k, v := range kv.m {
			if strings.HasPrefix(k, p) {
				out = append(out, database.KVPair{Key: k, Value: v})
			}
		}
		return out, nil
	}
	st.MarkExternalIDRemovedFunc = func(_, _ string) error {
		if tombstones != nil {
			tombstones.Add(1)
		}
		return nil
	}
	return st
}

// controllableWriter fakes the ITL hooks with a switchable failure.
type controllableWriter struct {
	fail  atomic.Bool
	calls atomic.Int32
}

func (w *controllableWriter) install(t *testing.T) {
	t.Helper()
	withFakeITLHooks(t,
		func(string) error { return nil },
		func(in, out string, _ itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
			w.calls.Add(1)
			if w.fail.Load() {
				return nil, errors.New("ITLSafetyContract REJECTED write: [location-form@1/mhoh: synthetic]")
			}
			data, _ := os.ReadFile(in)
			if err := os.WriteFile(out, append(data, '!'), 0o644); err != nil {
				return nil, err
			}
			return &itunes.ITLWriteBackResult{UpdatedCount: 1, OutputPath: out}, nil
		},
	)
	withFakeParseITLHook(t, &itunes.ITLLibrary{}, nil)
	withStubPinLKG(t)
}

// newDurableBatcher builds a batcher whose timers never fire during the test
// (long debounce and backoff); the test drives drainFlush directly.
func newDurableBatcher(t *testing.T, itlPath string, st WriteBackStore) *WriteBackBatcher {
	t.Helper()
	b := NewWriteBackBatcher(time.Hour, enabledFlushCfg(itlPath), st)
	_ = b.Start(context.Background()) // reloads the durable queue
	b.retryBase = time.Hour
	b.retryMax = 4 * time.Hour
	t.Cleanup(func() {
		b.mu.Lock()
		b.stopTimerLocked()
		b.stopped = true
		b.mu.Unlock()
	})
	return b
}

// TestSafeWriteITL_PassesWritebackRoot: the root-cause regression at the
// service layer. The batcher's writer must hand the AO library's root to BOTH
// the write and the step-4b re-read audit; before the fix it passed neither,
// so prod rejected every flush. A non-AO library stays strict.
func TestSafeWriteITL_PassesWritebackRoot(t *testing.T) {
	for _, tc := range []struct {
		name, dir, want string
	}{
		{"AO writeback library", filepath.Join("audiobook-organizer", ".itunes-writeback"), "audiobook-organizer/.itunes-writeback/"},
		{"any other library", filepath.Join("books", "itunes"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.dir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			itlPath := makeITL(t, dir, "iTunes Library.itl", "lib")

			var applyCfg, auditCfg []itunes.ContractConfig
			prevApply, prevValidate, prevAudit := itlApplyOperationsFn, itlValidateFn, itlAuditFileFn
			t.Cleanup(func() { itlApplyOperationsFn, itlValidateFn, itlAuditFileFn = prevApply, prevValidate, prevAudit })
			itlValidateFn = func(string) error { return nil }
			itlApplyOperationsFn = func(in, out string, _ itunes.ITLOperationSet, cfg ...itunes.ContractConfig) (*itunes.ITLWriteBackResult, error) {
				applyCfg = cfg
				data, _ := os.ReadFile(in)
				_ = os.WriteFile(out, data, 0o644)
				return &itunes.ITLWriteBackResult{UpdatedCount: 1}, nil
			}
			itlAuditFileFn = func(_ string, cfg itunes.ContractConfig) error {
				auditCfg = append(auditCfg, cfg)
				return nil
			}

			if err := SafeWriteITL(itlPath, itunes.ITLOperationSet{Removes: map[string]bool{"aa": true}}); err != nil {
				t.Fatalf("SafeWriteITL: %v", err)
			}
			if len(applyCfg) != 1 || applyCfg[0].AllowedWritebackRoot != tc.want {
				t.Fatalf("ApplyITLOperations got cfg %+v, want AllowedWritebackRoot %q", applyCfg, tc.want)
			}
			if len(auditCfg) != 1 || auditCfg[0].AllowedWritebackRoot != tc.want {
				t.Fatalf("audit got cfg %+v, want AllowedWritebackRoot %q", auditCfg, tc.want)
			}
			if applyCfg[0].RemovedTracksMax != itunes.DefaultContractConfig().RemovedTracksMax || applyCfg[0].Force {
				t.Fatalf("the root must not relax the bounded-delta defaults: %+v", applyCfg[0])
			}
		})
	}
}

// TestFlush_RemoveStaysPendingWhenWriteFails: a remove whose write fails stays
// queued (memory and store) and is NOT tombstoned; the retry that succeeds
// tombstones it exactly once and clears its key.
func TestFlush_RemoveStaysPendingWhenWriteFails(t *testing.T) {
	var w controllableWriter
	w.install(t)
	w.fail.Store(true)

	kv := newKV()
	var tombstones atomic.Int32
	itlPath := makeITL(t, t.TempDir(), "library.itl", "lib")
	b := newDurableBatcher(t, itlPath, durableStore(kv, &tombstones))

	b.EnqueueRemove("AABBCCDD11223344")
	if !kv.has(wbKeyRemove + "aabbccdd11223344") {
		t.Fatal("remove was not persisted at enqueue")
	}

	b.drainFlush()

	b.mu.Lock()
	stillPending := b.pendingRemoves["aabbccdd11223344"]
	b.mu.Unlock()
	if !stillPending {
		t.Fatal("failed write: remove was dropped from the queue")
	}
	if !kv.has(wbKeyRemove + "aabbccdd11223344") {
		t.Fatal("failed write: remove's store key was deleted")
	}
	if n := tombstones.Load(); n != 0 {
		t.Fatalf("failed write: external id tombstoned %d times; the DB would say removed while the track is still in iTunes", n)
	}
	st := b.Status()
	if st.ConsecutiveFailures != 1 || st.NextRetryAt == nil || st.LastError == "" || st.Healthy {
		t.Fatalf("failure not surfaced in status: %+v", st)
	}

	// The retry succeeds: tombstone once, key gone, status healthy.
	w.fail.Store(false)
	b.drainFlush()
	if n := tombstones.Load(); n != 1 {
		t.Fatalf("after the write landed: tombstones = %d, want 1", n)
	}
	if kv.has(wbKeyRemove + "aabbccdd11223344") {
		t.Fatal("after the write landed: the remove's queue key is still there")
	}
	if st := b.Status(); st.ConsecutiveFailures != 0 || st.PendingRemoves != 0 || !st.Healthy || st.LastSuccessAt == nil {
		t.Fatalf("status after success: %+v", st)
	}
}

// TestFlush_NeverDropsAfterRepeatedFailures: the old batcher dropped a batch
// after 3 consecutive failures (and, with its shared counter, every later
// batch on its first failure). Twelve failures in a row must keep everything,
// with the backoff growing to its cap.
func TestFlush_NeverDropsAfterRepeatedFailures(t *testing.T) {
	var w controllableWriter
	w.install(t)
	w.fail.Store(true)

	kv := newKV()
	itlPath := makeITL(t, t.TempDir(), "library.itl", "lib")
	b := newDurableBatcher(t, itlPath, durableStore(kv, nil))

	b.Enqueue("book-1")
	b.EnqueueAdd(itunes.ITLNewTrack{Name: "Ch 1", Location: `W:\x\ch1.m4b`})
	b.EnqueueRemove("1122334455667788")

	for i := range 12 {
		// A second book arriving mid-outage must be kept too (the old shared
		// counter dropped it on its first failure).
		if i == 6 {
			b.Enqueue("book-2")
		}
		b.drainFlush()
	}

	st := b.Status()
	if st.PendingUpdates != 2 || st.PendingAdds != 1 || st.PendingRemoves != 1 {
		t.Fatalf("batch dropped after repeated failures: %+v", st)
	}
	if st.ConsecutiveFailures != 12 {
		t.Fatalf("consecutive failures = %d, want 12", st.ConsecutiveFailures)
	}
	if got := len(kv.keys(wbQueuePrefix)); got != 4 {
		t.Fatalf("durable queue holds %d keys, want 4: %v", got, kv.keys(wbQueuePrefix))
	}
	if w.calls.Load() != 12 {
		t.Fatalf("writer attempts = %d, want 12", w.calls.Load())
	}
	if d := time.Until(*st.NextRetryAt); d > 4*time.Hour || d < 3*time.Hour {
		t.Fatalf("backoff not capped at retryMax: next retry in %v", d)
	}
}

func TestRetryDelay_Backoff(t *testing.T) {
	b := &WriteBackBatcher{}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour}
	for i, w := range want {
		if got := b.retryDelay(i + 1); got != w {
			t.Errorf("retryDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
}

// TestFlush_BackoffDefersEnqueueTriggeredRetry: during backoff a new enqueue
// must not trigger an early retry (it would re-encode the library every
// debounce tick while the write keeps failing).
func TestFlush_BackoffDefersEnqueueTriggeredRetry(t *testing.T) {
	var w controllableWriter
	w.install(t)
	w.fail.Store(true)

	itlPath := makeITL(t, t.TempDir(), "library.itl", "lib")
	b := NewWriteBackBatcher(5*time.Millisecond, enabledFlushCfg(itlPath), durableStore(newKV(), nil))
	b.retryBase = time.Hour
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	b.Enqueue("book-1")
	deadline := time.Now().Add(5 * time.Second)
	for w.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if w.calls.Load() != 1 {
		t.Fatalf("first attempt never ran")
	}
	for range 5 {
		b.Enqueue("book-2")
		time.Sleep(20 * time.Millisecond)
	}
	if n := w.calls.Load(); n != 1 {
		t.Fatalf("enqueues during backoff triggered %d extra write attempts", n-1)
	}
	// Stop's final drain is the one permitted extra attempt; make it succeed.
	w.fail.Store(false)
}

// TestQueue_SurvivesRestart: queued books, adds, removes, held removes and the
// failure status are reloaded by a new batcher on the same store.
func TestQueue_SurvivesRestart(t *testing.T) {
	var w controllableWriter
	w.install(t)
	w.fail.Store(true)

	kv := newKV()
	itlPath := makeITL(t, t.TempDir(), "library.itl", "lib")
	b1 := newDurableBatcher(t, itlPath, durableStore(kv, nil))
	b1.Enqueue("book-1")
	b1.EnqueueAdd(itunes.ITLNewTrack{Name: "Ch 1", Location: `W:\x\ch1.m4b`})
	b1.EnqueueRemove("1122334455667788")
	b1.holdRemoves(map[string]bool{"99aabbccddeeff00": true})
	b1.drainFlush() // fails: status persisted

	b2 := newDurableBatcher(t, itlPath, durableStore(kv, nil))
	st := b2.Status()
	if st.PendingUpdates != 1 || st.PendingAdds != 1 || st.PendingRemoves != 1 || st.HeldRemoves != 1 {
		t.Fatalf("queue not restored after restart: %+v", st)
	}
	if st.ConsecutiveFailures != 1 || st.LastError == "" {
		t.Fatalf("failure status not restored: %+v", st)
	}
	if st.NextRetryAt != nil {
		t.Fatalf("a restart should get a prompt attempt, got next retry %v", st.NextRetryAt)
	}

	// The restored add round-trips intact and the write clears the store.
	w.fail.Store(false)
	b2.drainFlush()
	if keys := kv.keys(wbQueuePrefix); len(keys) != 0 {
		t.Fatalf("queue keys left after a successful write: %v", keys)
	}
	if !kv.has(wbHeldRemovePrefix + "99aabbccddeeff00") {
		t.Fatal("a successful flush must not release held removes")
	}
}

// TestFlush_OverCapHoldsRemovesAndContinues: an over-cap set of removes is
// held (none applied, none dropped) while the batch's other work is written;
// the owner releases at most MaxRemovesPerFlush at a time.
func TestFlush_OverCapHoldsRemovesAndContinues(t *testing.T) {
	var w controllableWriter
	w.install(t)

	kv := newKV()
	var tombstones atomic.Int32
	itlPath := makeITL(t, t.TempDir(), "library.itl", "lib")
	b := newDurableBatcher(t, itlPath, durableStore(kv, &tombstones))

	b.Enqueue("book-1")
	for i := range MaxRemovesPerFlush + 1 {
		b.EnqueueRemove(strings.Repeat("a", 16-len(itoaSafe(i))) + itoaSafe(i))
	}
	b.drainFlush()

	st := b.Status()
	if st.HeldRemoves != MaxRemovesPerFlush+1 || st.PendingRemoves != 0 {
		t.Fatalf("over-cap removes not held: %+v", st)
	}
	if w.calls.Load() != 1 || st.PendingUpdates != 0 {
		t.Fatalf("the batch's book update should still be written: calls=%d status=%+v", w.calls.Load(), st)
	}
	if tombstones.Load() != 0 {
		t.Fatal("held removes must not be tombstoned")
	}
	if got := len(kv.keys(wbHeldRemovePrefix)); got != MaxRemovesPerFlush+1 {
		t.Fatalf("held list not durable: %d keys", got)
	}
	if st.Healthy {
		t.Fatal("held removes must mark the status unhealthy")
	}

	released, still := b.ReleaseHeldRemoves(1000)
	if released != MaxRemovesPerFlush || still != 1 {
		t.Fatalf("release: released=%d still=%d, want %d and 1", released, still, MaxRemovesPerFlush)
	}
	b.drainFlush()
	if n := tombstones.Load(); n != MaxRemovesPerFlush {
		t.Fatalf("released removes tombstoned %d times, want %d", n, MaxRemovesPerFlush)
	}
}

// TestSafeWriteITL_NoBackupOnRejectedWrite: a write rejected by the writer or
// by the audit leaves no .bak (a backup of an unchanged library only pushes
// real history out of the keep-N rotation).
func TestSafeWriteITL_NoBackupOnRejectedWrite(t *testing.T) {
	for _, stage := range []string{"apply", "audit"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			itlPath := makeITL(t, dir, "library.itl", "original")
			withFakeITLHooks(t,
				func(string) error { return nil },
				func(in, out string, _ itunes.ITLOperationSet) (*itunes.ITLWriteBackResult, error) {
					if stage == "apply" {
						return nil, errors.New("rejected")
					}
					data, _ := os.ReadFile(in)
					_ = os.WriteFile(out, data, 0o644)
					return &itunes.ITLWriteBackResult{UpdatedCount: 1}, nil
				},
			)
			if stage == "audit" {
				itlAuditFileFn = func(string, itunes.ContractConfig) error { return errors.New("location-form") }
			}
			if err := SafeWriteITL(itlPath, itunes.ITLOperationSet{Removes: map[string]bool{"aa": true}}); err == nil {
				t.Fatal("expected rejection")
			}
			if baks, _ := filepath.Glob(itlPath + ".bak-*"); len(baks) != 0 {
				t.Fatalf("rejected write left backups: %v", baks)
			}
		})
	}
}

// TestPruneITLBackups_BoundedAndKeepsNamedSnapshots: rotation keeps exactly the
// newest N timestamped backups across layouts and never touches the pinned
// anchor or hand-named snapshots (the shapes that sit in prod's directory).
func TestPruneITLBackups_BoundedAndKeepsNamedSnapshots(t *testing.T) {
	dir := t.TempDir()
	itlPath := makeITL(t, dir, "iTunes Library.itl", "live")
	named := []string{
		itlPath + ".bak",
		itlPath + ".bak-lkg",
		itlPath + ".itunes-real-ref-20260713",
		itlPath + ".prototype-2mb-bak-20260722",
		itlPath + ".rebuild-bak-20260502-075930",
	}
	for _, p := range named {
		if err := os.WriteFile(p, []byte("keep"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)
	for i := range 8 {
		if err := os.WriteFile(itunes.BackupName(itlPath, base.Add(time.Duration(i)*time.Minute)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneITLBackups(itlPath, itlBackupRetention); err != nil {
		t.Fatal(err)
	}
	for _, p := range named {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("named snapshot %s removed by rotation", filepath.Base(p))
		}
	}
	var stamped []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if _, ok := itunes.ParseBackupTime(e.Name(), filepath.Base(itlPath)); ok {
			stamped = append(stamped, e.Name())
		}
	}
	if len(stamped) != itlBackupRetention {
		t.Fatalf("timestamped backups after rotation = %d, want %d: %v", len(stamped), itlBackupRetention, stamped)
	}
	sort.Strings(stamped)
	if want := filepath.Base(itunes.BackupName(itlPath, base.Add(3*time.Minute))); stamped[0] != want {
		t.Fatalf("oldest survivor = %s, want %s (the newest %d must be kept)", stamped[0], want, itlBackupRetention)
	}
}

// TestNewWriteBackBatcher_NoStoreIOUntilStart: construction must not touch the
// store (server tests build the batcher on strict mocks that panic on an
// unexpected ScanPrefix); Start reloads the queue exactly once.
func TestNewWriteBackBatcher_NoStoreIOUntilStart(t *testing.T) {
	var scans atomic.Int32
	st := &database.MockStore{ScanPrefixFunc: func(string) ([]database.KVPair, error) {
		scans.Add(1)
		return nil, nil
	}}
	b := NewWriteBackBatcher(time.Hour, disabledFlushCfg(), st)
	if scans.Load() != 0 {
		t.Fatalf("constructor scanned the store %d times", scans.Load())
	}
	_ = b.Start(context.Background())
	_ = b.Start(context.Background())
	if n := scans.Load(); n != 2 { // queue prefix + held prefix, once
		t.Fatalf("Start scanned %d prefixes, want 2 (one load)", n)
	}
	_ = b.Stop(context.Background())
}
