// file: internal/reconcile/reconcile_scan_ctx_test.go
// version: 1.0.0
// guid: 3f7a1d92-6c48-4e05-b1a9-8d2e5c70f634
// last-edited: 2026-10-14

package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The reconcile scan runs on the registry's 5-minute silence watchdog and, since
// 04-P14c, is also the nightly job. These tests pin the two things that made
// that unsafe before: stretches that report nothing (the directory walk, the
// title match) and a Cancel that the body never looked at.

// countingHooks returns hooks whose Heartbeat increments n and, when cancelAt is
// positive, cancels the context on that call.
func countingHooks(n *atomic.Int64, cancelAt int64, cancel context.CancelFunc) ScanHooks {
	return ScanHooks{Heartbeat: func() {
		if got := n.Add(1); cancelAt > 0 && got == cancelAt {
			cancel()
		}
	}}
}

// recordingProgress is a ProgressReporter that discards everything.
type recordingProgress struct{}

func (recordingProgress) UpdateProgress(int, int, string) error { return nil }
func (recordingProgress) Log(string, string, *string) error     { return nil }
func (recordingProgress) IsCanceled() bool                      { return false }

// scanFixture builds a library root holding two untracked audio files and a
// store with two broken records that match neither by hash nor by name, so a
// full scan reaches the title-in-filename phase.
func scanFixture(t *testing.T) *fakeReconcileStore {
	t.Helper()
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "zzz-one.m4b", "zzz-two.m4b")

	store := newFakeStore()
	missing := t.TempDir()
	store.books = []database.BookCore{
		{ID: "b1", Title: "alpha", FilePath: filepath.Join(missing, "alpha-gone.m4b")},
		{ID: "b2", Title: "bravo", FilePath: filepath.Join(missing, "bravo-gone.m4b")},
	}
	return store
}

func TestFindUntrackedFiles_HeartbeatsPerWalkedEntry(t *testing.T) {
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "a.m4b", "b.m4b", "c.m4b")

	var beats atomic.Int64
	got, err := FindUntrackedFiles(context.Background(), appDirStore{}, map[string]bool{}, countingHooks(&beats, 0, nil))
	if err != nil {
		t.Fatalf("FindUntrackedFiles: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("found %d files, want 3", len(got))
	}
	// 3 files plus the root and the two directories above them: the walk is the
	// longest silent stretch on network storage, so every entry must stamp.
	if beats.Load() < 3 {
		t.Errorf("heartbeats = %d, want at least one per walked file (3)", beats.Load())
	}
}

func TestFindUntrackedFiles_CanceledContextStopsTheWalk(t *testing.T) {
	root := withEmptyExtensionConfig(t, nil)
	writeAudio(t, root, "a.m4b", "b.m4b")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := FindUntrackedFiles(ctx, appDirStore{}, map[string]bool{}, ScanHooks{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled walk must return context.Canceled, got %v (files %v)", err, got)
	}
	if got != nil {
		t.Errorf("a canceled walk must not hand back a partial list, got %v", got)
	}
}

// A scan that is never canceled must still finish and see the whole fixture:
// the control for the cancel tests below.
func TestBuildReconcilePreviewCtx_UncanceledRunCompletes(t *testing.T) {
	store := scanFixture(t)
	var beats atomic.Int64
	res, err := BuildReconcilePreviewCtx(context.Background(), store, nil, countingHooks(&beats, 0, nil))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(res.BrokenRecords) != 2 || len(res.UntrackedFiles) != 2 || len(res.UnmatchedBooks) != 2 {
		t.Fatalf("fixture drifted: broken=%d untracked=%d unmatched=%d, want 2/2/2",
			len(res.BrokenRecords), len(res.UntrackedFiles), len(res.UnmatchedBooks))
	}
	if beats.Load() == 0 {
		t.Error("a full scan must send heartbeats")
	}
}

// Canceling during the stat pass must fail the scan. Stats that were skipped
// leave their broken flag false, so without the check after the pool drains the
// unchecked tail reads as "all paths valid" and the scan returns a clean, wrong
// result.
func TestBuildReconcilePreviewCtx_CancelDuringStatPassFailsTheScan(t *testing.T) {
	store := scanFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var beats atomic.Int64
	// The first beats are the stat workers' (one per book with a path).
	res, err := BuildReconcilePreviewCtx(ctx, store, nil, countingHooks(&beats, 1, cancel))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan canceled during the stat pass must return context.Canceled, got err=%v res=%v", err, res)
	}
	if res != nil {
		t.Errorf("a canceled scan must not return a result, got %+v", res)
	}
}

// Canceling inside the title-in-filename phase, the O(broken x untracked) loop
// that reports nothing of its own, must stop it at the next record.
func TestBuildReconcilePreviewCtx_CancelInTitleMatchStopsAtNextRecord(t *testing.T) {
	// Count the beats of a full run, then cancel on the first title-match beat
	// (the second-to-last: two broken records beat once each, last).
	var total atomic.Int64
	if _, err := BuildReconcilePreviewCtx(context.Background(), scanFixture(t), nil, countingHooks(&total, 0, nil)); err != nil {
		t.Fatalf("baseline preview: %v", err)
	}

	store := scanFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var beats atomic.Int64
	res, err := BuildReconcilePreviewCtx(ctx, store, nil, countingHooks(&beats, total.Load()-1, cancel))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan canceled inside the title match must return context.Canceled, got err=%v res=%v", err, res)
	}
}

// The hash pool skips files once canceled, which would leave a partial index
// that reads as "nothing matched". The check that follows the pool must refuse
// to go on. Cancel on the last hash beat: beats of a full run are the stat
// workers, the walk entries, one per hashed file, then one per broken record in
// the title match, so total-3 is the last hash beat (two files, two records).
func TestBuildReconcilePreviewCtx_CancelWhileHashingFailsTheScan(t *testing.T) {
	var total atomic.Int64
	if _, err := BuildReconcilePreviewCtx(context.Background(), scanFixture(t), nil, countingHooks(&total, 0, nil)); err != nil {
		t.Fatalf("baseline preview: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var beats atomic.Int64
	_, err := BuildReconcilePreviewCtx(ctx, scanFixture(t), nil, countingHooks(&beats, total.Load()-3, cancel))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a scan canceled while hashing must return context.Canceled, got %v", err)
	}
}

func TestRunReconcileScan_CanceledScanSavesNothing(t *testing.T) {
	store := scanFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	saved := 0
	err := RunReconcileScan(ctx, store, func(any) error { saved++; return nil }, recordingProgress{}, ScanHooks{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if saved != 0 {
		t.Errorf("a canceled scan saved %d results; a partial preview must never be stored", saved)
	}
}

func TestRunReconcileScan_CompletedScanSavesOnceAndHeartbeats(t *testing.T) {
	store := scanFixture(t)
	var beats atomic.Int64
	saved := 0
	var payload any
	err := RunReconcileScan(context.Background(), store, func(p any) error {
		saved++
		payload = p
		return nil
	}, recordingProgress{}, countingHooks(&beats, 0, nil))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if saved != 1 {
		t.Errorf("saved %d results, want 1", saved)
	}
	if beats.Load() == 0 {
		t.Error("the scan must send heartbeats")
	}
	// The payload shape is the /latest endpoint's contract (the same type the
	// retired nightly op stored), so nightly and manual rows decode identically.
	if _, ok := payload.(json.RawMessage); !ok {
		t.Errorf("payload is %T, want json.RawMessage of a ReconcilePreviewResult", payload)
	}
}
