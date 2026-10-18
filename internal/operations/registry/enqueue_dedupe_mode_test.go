// file: internal/operations/registry/enqueue_dedupe_mode_test.go
// version: 1.0.0
// guid: bf374303-0b13-466b-b0d6-f5efc8453cff
// last-edited: 2026-10-18

package registry_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// registerCronPreviewDef registers a scheduled def (dedupe collapses any
// request into its active row) that previews with dry_run, the shape of
// maintenance.author-split-scan.
func registerCronPreviewDef(t *testing.T, r *registry.Registry, id string) registry.OperationDef {
	t.Helper()
	sched := "0 2 * * 1"
	def := makeDedupeDef(t, id, "k."+id)
	def.Schedule = &sched
	def.Preview = registry.PreviewSpec{Params: opmode.DryRunParams{}}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	return def
}

func resolved(t *testing.T, def registry.OperationDef, params, mode string) json.RawMessage {
	t.Helper()
	out, err := def.Preview.Resolve(json.RawMessage(params), mode)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestEnqueueDedupe_LiveStartIsNotAnsweredWithTheQueuedPreview is the review
// repro: the cron tick queues {} (a preview), then a v3 live start arrived
// and got the preview run's id back, so the caller believed a live run was
// queued.
func TestEnqueueDedupe_LiveStartIsNotAnsweredWithTheQueuedPreview(t *testing.T) {
	r, store := newTestRegistry(t)
	def := registerCronPreviewDef(t, r, "test.preview-cron")
	tick, err := r.EnqueueOp(context.Background(), def.ID, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	live, err := r.EnqueueOp(context.Background(), def.ID, resolved(t, def, `{}`, registry.ModeLive))
	if err != nil {
		t.Fatal(err)
	}
	if live == tick {
		t.Fatalf("live start returned the queued preview run %s", tick)
	}
	if n := len(activeRowsForDef(t, store, def.ID)); n != 2 {
		t.Fatalf("active rows = %d, want the preview and the live run", n)
	}
}

// TestEnqueueDedupe_PreviewStartIsNotAnsweredWithTheQueuedLive is the reverse.
func TestEnqueueDedupe_PreviewStartIsNotAnsweredWithTheQueuedLive(t *testing.T) {
	r, _ := newTestRegistry(t)
	def := registerCronPreviewDef(t, r, "test.preview-cron2")
	liveID, err := r.EnqueueOp(context.Background(), def.ID, resolved(t, def, `{}`, registry.ModeLive))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.EnqueueOp(context.Background(), def.ID, resolved(t, def, `{}`, registry.ModePreview))
	if err != nil {
		t.Fatal(err)
	}
	if got == liveID {
		t.Fatalf("preview start returned the queued live run %s", liveID)
	}
}

// TestEnqueueDedupe_SameModeStillCollapses: the pile-up dedupe the schedule
// path was written for still holds within one mode, however the mode is
// spelled ({} and an explicit dry_run=true are both a preview).
func TestEnqueueDedupe_SameModeStillCollapses(t *testing.T) {
	r, store := newTestRegistry(t)
	def := registerCronPreviewDef(t, r, "test.preview-cron3")
	tick, err := r.EnqueueOp(context.Background(), def.ID, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.EnqueueOp(context.Background(), def.ID, resolved(t, def, `{}`, registry.ModePreview))
	if err != nil {
		t.Fatal(err)
	}
	if again != tick {
		t.Fatalf("a second preview queued %s beside %s", again, tick)
	}
	live1, err := r.EnqueueOp(context.Background(), def.ID, resolved(t, def, `{}`, registry.ModeLive))
	if err != nil {
		t.Fatal(err)
	}
	live2, err := r.EnqueueOp(context.Background(), def.ID, json.RawMessage(`{"dry_run":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if live1 != live2 {
		t.Fatalf("a second live request queued %s beside %s", live2, live1)
	}
	if n := len(activeRowsForDef(t, store, def.ID)); n != 2 {
		t.Fatalf("active rows = %d, want one preview and one live", n)
	}
}

type dedupeOneOfParams struct {
	Apply    bool `json:"apply"`
	Rollback bool `json:"rollback"`
}

// TestEnqueueDedupe_LiveOneOfSwitchesAreDifferentWork: two live runs that
// name different write kinds are not the same run.
func TestEnqueueDedupe_LiveOneOfSwitchesAreDifferentWork(t *testing.T) {
	r, _ := newTestRegistry(t)
	def := makeDedupeDef(t, "test.oneof-dedupe", "k.oneof")
	def.DedupeQueuedRuns = true
	def.Preview = registry.PreviewSpec{Params: dedupeOneOfParams{}, LiveOneOf: []string{"apply", "rollback"}}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	a, err := r.EnqueueOp(context.Background(), def.ID, resolved(t, def, `{"apply":true}`, registry.ModeLive))
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.EnqueueOp(context.Background(), def.ID, resolved(t, def, `{"rollback":true}`, registry.ModeLive))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("a rollback request was answered with the queued apply run %s", a)
	}
}

func TestPreviewSpec_SameMode(t *testing.T) {
	spec := registry.PreviewSpec{Params: opmode.DryRunParams{}}
	same := [][2]string{{`{}`, `{"dry_run":true}`}, {`{"dryRun":false}`, `{"dry_run":false,"dryRun":false}`}, {`{"x":1}`, `{}`}}
	for _, c := range same {
		if !spec.SameMode(json.RawMessage(c[0]), json.RawMessage(c[1])) {
			t.Errorf("SameMode(%s, %s) = false, want true", c[0], c[1])
		}
	}
	diff := [][2]string{{`{}`, `{"dry_run":false}`}, {`{"dry_run":true,"dryRun":false}`, `{"dry_run":true,"dryRun":false}`}, {`[1]`, `[1]`}}
	for _, c := range diff {
		if spec.SameMode(json.RawMessage(c[0]), json.RawMessage(c[1])) {
			t.Errorf("SameMode(%s, %s) = true, want false", c[0], c[1])
		}
	}
}
