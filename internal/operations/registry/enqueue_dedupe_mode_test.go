// file: internal/operations/registry/enqueue_dedupe_mode_test.go
// version: 1.2.0
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

// TestPreviewSpec_SameMode reads bodies by what they do at run time: an
// absent or null key is the preview default, a case variant decodes into the
// same field, a key the type does not decode is ignored, and dry_run/dryRun
// that disagree are a preview. Bodies whose mode cannot be read are in no mode.
func TestPreviewSpec_SameMode(t *testing.T) {
	spec := registry.PreviewSpec{Params: opmode.DryRunParams{}}
	same := [][2]string{
		{`{}`, `{"dry_run":true}`},
		{`{"dryRun":false}`, `{"dry_run":false,"dryRun":false}`},
		{`{"x":1}`, `{}`},
		{`{"apply":true}`, `{}`},
		{`{"dry_run":null}`, `{}`},
		{`{"DRY_RUN":false}`, `{"dry_run":false}`},
		{`{"dry_run":true,"dryRun":false}`, `{}`},
		{` null` + "\n", `{}`},
		{``, `{}`},
	}
	for _, c := range same {
		if !spec.SameMode(json.RawMessage(c[0]), json.RawMessage(c[1])) {
			t.Errorf("SameMode(%s, %s) = false, want true", c[0], c[1])
		}
	}
	diff := [][2]string{
		{`{}`, `{"dry_run":false}`},
		{`[1]`, `[1]`},
		{`{"dry_run":"false"}`, `{"dry_run":"false"}`},
		{`{"DRY_RUN":true,"dry_run":false}`, `{"DRY_RUN":true,"dry_run":false}`},
	}
	for _, c := range diff {
		if spec.SameMode(json.RawMessage(c[0]), json.RawMessage(c[1])) {
			t.Errorf("SameMode(%s, %s) = true, want false", c[0], c[1])
		}
	}

	oneOf := registry.PreviewSpec{Params: dedupeOneOfParams{}, LiveOneOf: []string{"apply", "rollback"}}
	if !oneOf.SameMode(json.RawMessage(`{}`), json.RawMessage(`{"apply":false,"rollback":false}`)) {
		t.Error("LiveOneOf: {} and every switch false are both a preview")
	}
	if oneOf.SameMode(json.RawMessage(`{"apply":true}`), json.RawMessage(`{"rollback":true}`)) {
		t.Error("LiveOneOf: apply and rollback are different work")
	}
}

type twoFamilyParams struct {
	DryRun *bool `json:"dry_run"`
	Apply  bool  `json:"apply"`
}

// TestPreviewSpec_SameMode_TwoFamilies: with dry_run and apply both decoded,
// a body is live only when every family says write (the default, apply &&
// !dry_run), or when any does for AnyFamilyWrites (apply || !dry_run).
// Resolve's normalization classed {"dry_run":false} as live on the first
// shape, which previews at run time because apply is false.
func TestPreviewSpec_SameMode_TwoFamilies(t *testing.T) {
	all := registry.PreviewSpec{Params: twoFamilyParams{}}
	anyW := registry.PreviewSpec{Params: twoFamilyParams{}, AnyFamilyWrites: true}
	if err := anyW.Check(); err != nil {
		t.Fatalf("AnyFamilyWrites on two families must check: %v", err)
	}
	for _, c := range []struct {
		spec registry.PreviewSpec
		a, b string
		same bool
	}{
		{all, `{"dry_run":false}`, `{}`, true},
		{all, `{"apply":true}`, `{}`, true},
		{all, `{"dry_run":false}`, `{"dry_run":false,"apply":true}`, false},
		{anyW, `{"apply":true}`, `{"dry_run":false}`, true},
		{anyW, `{"apply":true}`, `{}`, false},
		{anyW, `{"dry_run":false}`, `{"dry_run":false,"apply":true}`, true},
	} {
		if got := c.spec.SameMode(json.RawMessage(c.a), json.RawMessage(c.b)); got != c.same {
			t.Errorf("AnyFamilyWrites=%v SameMode(%s, %s) = %v, want %v", c.spec.AnyFamilyWrites, c.a, c.b, got, c.same)
		}
	}
	if err := (registry.PreviewSpec{Params: opmode.DryRunParams{}, AnyFamilyWrites: true}).Check(); err == nil {
		t.Error("AnyFamilyWrites on a single family must not check")
	}
}

// TestEnqueueDedupe_IdenticalBodiesAlwaysFold: byte-identical params are one
// request whatever SameMode can read from them. Before the byte check, a
// body SameMode could not read never folded, so five cron ticks left five
// rows.
func TestEnqueueDedupe_IdenticalBodiesAlwaysFold(t *testing.T) {
	for i, body := range []string{
		`{"apply":true}`, `{"dry_run":true,"dryRun":false}`, `{"dry_run":null}`, `{"DRY_RUN":true}`,
		`{"dry_run":"false"}`, `{"DRY_RUN":true,"dry_run":false}`,
	} {
		r, store := newTestRegistry(t)
		def := registerCronPreviewDef(t, r, "test.identical-"+string(rune('a'+i)))
		for n := 0; n < 5; n++ {
			if _, err := r.EnqueueOp(context.Background(), def.ID, json.RawMessage(body)); err != nil {
				t.Fatal(err)
			}
		}
		if got := len(activeRowsForDef(t, store, def.ID)); got != 1 {
			t.Errorf("%s enqueued 5 times on a scheduled def left %d active rows, want 1", body, got)
		}
	}
}

// TestEnqueueDedupe_TwoFamilyDefFoldsByRunTimeMode: on an apply && !dry_run
// def, {"dry_run":false} previews at run time (apply is false), so it folds
// into the queued preview tick; only a body that sets both is live.
func TestEnqueueDedupe_TwoFamilyDefFoldsByRunTimeMode(t *testing.T) {
	r, store := newTestRegistry(t)
	sched := "0 2 * * 1"
	def := makeDedupeDef(t, "test.two-family-cron", "k.two-family-cron")
	def.Schedule = &sched
	def.Preview = registry.PreviewSpec{Params: twoFamilyParams{}}
	if err := r.RegisterOp(def); err != nil {
		t.Fatal(err)
	}
	tick, err := r.EnqueueOp(context.Background(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	half, err := r.EnqueueOp(context.Background(), def.ID, json.RawMessage(`{"dry_run":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if half != tick {
		t.Fatalf(`{"dry_run":false} previews at run time but queued %s beside the preview tick %s`, half, tick)
	}
	live, err := r.EnqueueOp(context.Background(), def.ID, json.RawMessage(`{"dry_run":false,"apply":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if live == tick {
		t.Fatal("a live body was answered with the queued preview tick")
	}
	if n := len(activeRowsForDef(t, store, def.ID)); n != 2 {
		t.Fatalf("active rows = %d, want the preview tick and the live run", n)
	}
}

// TestEnqueueDedupe_NoParamsTicksStillCollapse: the pile-up this dedupe was
// written for is scheduler ticks, which enqueue with no params. nil params
// (stored as {}) and a nil json.RawMessage (marshalled to null) are the same
// work in the same mode, so repeated ticks keep collapsing into one row.
func TestEnqueueDedupe_NoParamsTicksStillCollapse(t *testing.T) {
	r, store := newTestRegistry(t)
	def := registerCronPreviewDef(t, r, "test.preview-cron-ticks")
	first, err := r.EnqueueOp(context.Background(), def.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, params := range []any{nil, json.RawMessage(nil), struct{}{}} {
		id, err := r.EnqueueOp(context.Background(), def.ID, params)
		if err != nil {
			t.Fatal(err)
		}
		if id != first {
			t.Fatalf("tick %d (%T) queued %s instead of folding into %s", i, params, id, first)
		}
	}
	if n := len(activeRowsForDef(t, store, def.ID)); n != 1 {
		t.Fatalf("active rows = %d, want the one tick row", n)
	}
	if !def.Preview.SameMode(json.RawMessage("null"), json.RawMessage("{}")) {
		t.Fatal("SameMode(null, {}) = false; a null body is no params")
	}
}
