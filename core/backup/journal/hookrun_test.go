// file: core/backup/journal/hookrun_test.go
// version: 1.0.0
// guid: be4e1514-55f2-457c-b928-1ced9dfc10aa
// last-edited: 2026-11-12

package journal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// TestHookRun_IDSurvivesUntilCleared: the ID of a restore hook run is the same
// for every call (a re-run of the hooks after a crash) until ClearHookRun, and
// the next run gets another one. No run ID can equal a restore line's seq.
func TestHookRun_IDSurvivesUntilCleared(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)

	if id, err := HookRunID(ctx, fx.ks); err != nil || id != "" {
		t.Fatalf("HookRunID before any run = %q, %v, want empty", id, err)
	}
	first, err := EnsureHookRun(ctx, fx.ks, at)
	if err != nil || !strings.HasPrefix(first, hookRunPrefix) || len(first) <= len(hookRunPrefix) {
		t.Fatalf("EnsureHookRun = %q, %v", first, err)
	}
	again, err := EnsureHookRun(ctx, fx.ks, at.Add(time.Hour))
	if err != nil || again != first {
		t.Errorf("a second EnsureHookRun = %q, %v, want the first run's %q", again, err, first)
	}
	if id, err := HookRunID(ctx, fx.ks); err != nil || id != first {
		t.Errorf("HookRunID = %q, %v, want %q", id, err, first)
	}

	must(t, ClearHookRun(ctx, fx.ks))
	if id, err := HookRunID(ctx, fx.ks); err != nil || id != "" {
		t.Errorf("HookRunID after ClearHookRun = %q, %v, want empty", id, err)
	}
	must(t, ClearHookRun(ctx, fx.ks)) // nothing recorded is not an error
	next, err := EnsureHookRun(ctx, fx.ks, at.Add(2*time.Hour))
	if err != nil || next == first {
		t.Errorf("the run after ClearHookRun = %q, %v, want an ID other than %q", next, err, first)
	}
}

// TestHookRun_ARecordThatDoesNotDecodeIsAnError: a run record that is there and
// unreadable is not replaced by a fresh ID next to it, which would make the same
// restore's re-run write its records twice.
func TestHookRun_ARecordThatDoesNotDecodeIsAnError(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	b := fx.ks.NewBatch()
	must(t, b.In(fx.ks).Set([]byte(hookRunKey), []byte("{not json")))
	must(t, b.Commit(ctx, storage.Sync))
	must(t, b.Close())

	if id, err := EnsureHookRun(ctx, fx.ks, time.Now()); err == nil {
		t.Fatalf("EnsureHookRun over an unreadable record = %q, nil, want an error", id)
	}
	raw, err := fx.ks.Get(ctx, []byte(hookRunKey))
	if err != nil || string(raw) != "{not json" {
		t.Errorf("the unreadable record was replaced: %q, %v", raw, err)
	}
}
