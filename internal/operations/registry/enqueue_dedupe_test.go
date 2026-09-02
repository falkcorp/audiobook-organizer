// file: internal/operations/registry/enqueue_dedupe_test.go
// version: 1.1.1
// guid: 4352733d-985f-4541-87ce-22f4698d67cc
// last-edited: 2026-09-02

// ENQ-DEDUP-1 regression suite.
//
// EnqueueOp's ConcurrencyKey dedupe used to match on defID alone: if any op for
// the same def was queued or running, the new request returned that op's id and
// its params were discarded. Prod, 2026-08-21 23:49 — approving more books while
// metadata.batch-apply-cached was running neither queued a second job nor grew
// the running one. The caller saw success; the approved books were never applied.
//
// The dedupe now additionally requires that the request asks for the SAME work:
// byte-equal marshalled params, or an explicit def-level opt-in
// (DedupeQueuedRuns), or a cron-scheduled def (Schedule != nil). Everything else
// enqueues a second QUEUED row — the dispatcher's Gate 3 (dispatcher.go:107)
// already refuses to START a second op holding the same ConcurrencyKey, so the
// second job waits and then runs instead of vanishing.
//
// These tests pin BOTH directions. Deleting the dedupe block entirely would pass
// every "queues a second op" test here; TestEnqueueOp_DedupeStillFiresForTheCommonDoubleClick
// and TestEnqueueOp_CronScheduledDefStillDedupesOnDefIDAlone are what stop that.
package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// dedupeParams mirrors the shape of the params in the prod incident: an
// explicit selection of books that the caller expects to be acted on.
type dedupeParams struct {
	BookIDs []string `json:"book_ids"`
}

// makeDedupeDef returns a registered-shaped def with a non-empty ConcurrencyKey
// and no Schedule — the CATEGORY C default that most defs fall into.
func makeDedupeDef(t *testing.T, id, concurrencyKey string) registry.OperationDef {
	t.Helper()
	def := makeValidDef(id)
	def.ConcurrencyKey = concurrencyKey
	return def
}

// activeRowsForDef returns the active (queued|running) rows the store holds for
// one def id. Deliberately reads through the same store method EnqueueOp uses,
// so a test asserting "two rows exist" is asserting what the dedupe itself sees.
func activeRowsForDef(t *testing.T, store *fakeStore, defID string) []database.OperationV2Row {
	t.Helper()
	all, err := store.ListActiveOperationsV2()
	if err != nil {
		t.Fatalf("ListActiveOperationsV2: %v", err)
	}
	var out []database.OperationV2Row
	for _, op := range all {
		if op.DefID == defID {
			out = append(out, op)
		}
	}
	return out
}

// TestEnqueueOp_SameParamsReturnsSameOpID — the dedupe still fires when the
// second request asks for exactly the same work.
func TestEnqueueOp_SameParamsReturnsSameOpID(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeDedupeDef(t, "test.dedupe-same", "t.dedupe")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	p := dedupeParams{BookIDs: []string{"a"}}

	first, err := r.EnqueueOp(context.Background(), def.ID, p)
	if err != nil {
		t.Fatalf("first EnqueueOp: %v", err)
	}
	second, err := r.EnqueueOp(context.Background(), def.ID, p)
	if err != nil {
		t.Fatalf("second EnqueueOp: %v", err)
	}

	if first != second {
		t.Errorf("identical params must dedupe: first=%s second=%s", first, second)
	}
	if rows := activeRowsForDef(t, store, def.ID); len(rows) != 1 {
		t.Errorf("expected exactly 1 active row for %s, got %d", def.ID, len(rows))
	}
}

func TestEnqueueOp_MergesCompatibleQueuedSelectionsWithoutDroppingIDs(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeDedupeDef(t, "test.dedupe-merge-queued", "t.dedupe")
	def.MergeQueuedParams = func(existing, incoming json.RawMessage) (json.RawMessage, bool, error) {
		var oldParams, newParams dedupeParams
		if err := json.Unmarshal(existing, &oldParams); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(incoming, &newParams); err != nil {
			return nil, false, err
		}
		seen := map[string]struct{}{}
		merged := dedupeParams{}
		for _, id := range append(oldParams.BookIDs, newParams.BookIDs...) {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			merged.BookIDs = append(merged.BookIDs, id)
		}
		out, err := json.Marshal(merged)
		return out, err == nil, err
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	first, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("first EnqueueOp: %v", err)
	}
	second, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"b", "c"}})
	if err != nil {
		t.Fatalf("second EnqueueOp: %v", err)
	}
	if second != first {
		t.Fatalf("merged request id = %q, want existing queued id %q", second, first)
	}
	rows := activeRowsForDef(t, store, def.ID)
	if len(rows) != 1 {
		t.Fatalf("active rows = %d, want one merged queued row", len(rows))
	}
	var got dedupeParams
	if err := json.Unmarshal([]byte(rows[0].Params), &got); err != nil {
		t.Fatalf("decode merged params: %v", err)
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(got.BookIDs, want) {
		t.Fatalf("merged book_ids = %v, want %v", got.BookIDs, want)
	}
}

// TestEnqueueOp_DifferentParamsQueuesASecondOp — the prod-incident regression.
// A second request carrying a DIFFERENT selection must queue, not be dropped.
func TestEnqueueOp_DifferentParamsQueuesASecondOp(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeDedupeDef(t, "test.dedupe-diff", "t.dedupe")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	first, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"a"}})
	if err != nil {
		t.Fatalf("first EnqueueOp: %v", err)
	}
	second, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"b"}})
	if err != nil {
		t.Fatalf("second EnqueueOp: %v", err)
	}

	if second == "" {
		t.Fatal("second EnqueueOp returned an empty op id")
	}
	if second == first {
		t.Fatalf("different params were silently deduped onto the running op (%s) — "+
			"this is the 2026-08-21 prod incident", first)
	}

	rows := activeRowsForDef(t, store, def.ID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 active rows for %s, got %d", def.ID, len(rows))
	}
	var secondRow *database.OperationV2Row
	for i := range rows {
		if rows[i].ID == second {
			secondRow = &rows[i]
		}
	}
	if secondRow == nil {
		t.Fatalf("second op id %s is not among the active rows", second)
	}
	if secondRow.Status != "queued" {
		t.Errorf("second op status = %q, want %q", secondRow.Status, "queued")
	}
	// And it must carry the params the caller actually asked for.
	var got dedupeParams
	if err := json.Unmarshal([]byte(secondRow.Params), &got); err != nil {
		t.Fatalf("unmarshal second row params %q: %v", secondRow.Params, err)
	}
	if len(got.BookIDs) != 1 || got.BookIDs[0] != "b" {
		t.Errorf("second op params = %+v, want BookIDs [b] — the new selection was lost", got)
	}
}

// TestEnqueueOp_SecondQueuedOpStartsOnlyAfterTheFirstCompletes drives the real
// dispatcher: the second QUEUED row must not START while the first holds the
// ConcurrencyKey (Gate 3, dispatcher.go:107), and must run once it is released.
// This is what makes queueing-instead-of-dropping safe.
func TestEnqueueOp_SecondQueuedOpStartsOnlyAfterTheFirstCompletes(t *testing.T) {
	ctx := t.Context()

	store := newFakeStore()
	r := registry.New(store, slog.Default(), 4, nil)

	started := make(chan struct{})
	release := make(chan struct{})
	runs := make(chan string, 4)

	def := makeDedupeDef(t, "test.dedupe-gate3", "t.gate3")
	def.Run = func(runCtx context.Context, raw json.RawMessage, _ registry.Reporter) error {
		var p dedupeParams
		_ = json.Unmarshal(raw, &p)
		if len(p.BookIDs) > 0 {
			runs <- p.BookIDs[0]
		}
		if len(p.BookIDs) > 0 && p.BookIDs[0] == "a" {
			close(started)
			select {
			case <-release:
			case <-runCtx.Done():
			}
		}
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)

	first, err := r.EnqueueOp(ctx, def.ID, dedupeParams{BookIDs: []string{"a"}})
	if err != nil {
		t.Fatalf("first EnqueueOp: %v", err)
	}
	<-started

	second, err := r.EnqueueOp(ctx, def.ID, dedupeParams{BookIDs: []string{"b"}})
	if err != nil {
		t.Fatalf("second EnqueueOp: %v", err)
	}
	if second == first {
		t.Fatalf("different params deduped onto the running op %s", first)
	}

	// Gate 3 must hold it queued for as long as the first op runs.
	assertStaysQueued(t, store, second, 400*time.Millisecond)

	close(release)
	awaitStatus(t, store, first, "completed", 5*time.Second)
	awaitStatus(t, store, second, "completed", 5*time.Second)

	close(runs)
	var seen []string
	for s := range runs {
		seen = append(seen, s)
	}
	if len(seen) != 2 || seen[0] != "a" || seen[1] != "b" {
		t.Errorf("run order = %v, want [a b] — both selections must run, first-enqueued first", seen)
	}
}

// TestEnqueueOp_CronScheduledDefStillDedupesOnDefIDAlone pins the pile-up
// behaviour the original dedupe existed for: a cron-scheduled def dedupes on
// def id alone, params notwithstanding. A resumable cron op's stored params
// drift via checkpoint merge (UpdateOperationV2Params, iface_ops_v2.go:130), so
// byte-comparison would not be reliable for these.
func TestEnqueueOp_CronScheduledDefStillDedupesOnDefIDAlone(t *testing.T) {
	r, store := newTestRegistry(t)
	sched := "0 3 * * *"
	def := makeDedupeDef(t, "test.dedupe-cron", "t.cron")
	def.Schedule = &sched
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	first, err := r.EnqueueOp(context.Background(), def.ID, map[string]any{"limit": 1})
	if err != nil {
		t.Fatalf("first EnqueueOp: %v", err)
	}
	second, err := r.EnqueueOp(context.Background(), def.ID, map[string]any{"limit": 2})
	if err != nil {
		t.Fatalf("second EnqueueOp: %v", err)
	}

	if first != second {
		t.Errorf("cron-scheduled def must dedupe on def id alone: first=%s second=%s", first, second)
	}
	if rows := activeRowsForDef(t, store, def.ID); len(rows) != 1 {
		t.Errorf("expected exactly 1 active row for a cron def, got %d", len(rows))
	}
}

// TestEnqueueOp_DedupeQueuedRunsOptInIgnoresParams — the explicit escape hatch
// for a future def whose params legitimately vary per tick.
func TestEnqueueOp_DedupeQueuedRunsOptInIgnoresParams(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeDedupeDef(t, "test.dedupe-optin", "t.optin")
	def.DedupeQueuedRuns = true
	if def.Schedule != nil {
		t.Fatal("fixture must have a nil Schedule so the opt-in is what is under test")
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	first, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"a"}})
	if err != nil {
		t.Fatalf("first EnqueueOp: %v", err)
	}
	second, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"b"}})
	if err != nil {
		t.Fatalf("second EnqueueOp: %v", err)
	}

	if first != second {
		t.Errorf("DedupeQueuedRuns=true must dedupe regardless of params: first=%s second=%s", first, second)
	}
	if rows := activeRowsForDef(t, store, def.ID); len(rows) != 1 {
		t.Errorf("expected exactly 1 active row for an opted-in def, got %d", len(rows))
	}
}

// TestEnqueueOp_NilParamsAndEmptyObjectAreEqual — nil params normalize to "{}"
// BEFORE the comparison, so a nil caller and an empty-struct caller do not
// spuriously queue a duplicate.
func TestEnqueueOp_NilParamsAndEmptyObjectAreEqual(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeDedupeDef(t, "test.dedupe-nil", "t.nil")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	first, err := r.EnqueueOp(context.Background(), def.ID, nil)
	if err != nil {
		t.Fatalf("first EnqueueOp(nil): %v", err)
	}
	second, err := r.EnqueueOp(context.Background(), def.ID, struct{}{})
	if err != nil {
		t.Fatalf("second EnqueueOp(struct{}{}): %v", err)
	}

	if first != second {
		t.Errorf("nil params and an empty struct must compare equal: first=%s second=%s", first, second)
	}
	if rows := activeRowsForDef(t, store, def.ID); len(rows) != 1 {
		t.Errorf("expected exactly 1 active row, got %d", len(rows))
	}
}

// TestEnqueueOp_ZombieRunningRowIsStillSkipped — the C-3 regression. A row left
// "running" with no live run handle must be skipped BEFORE the params
// comparison: its params are irrelevant, it is dead either way. Deduping
// against it would return a dead op's id for every future enqueue of the def.
func TestEnqueueOp_ZombieRunningRowIsStillSkipped(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeDedupeDef(t, "test.dedupe-zombie", "t.zombie")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	p := dedupeParams{BookIDs: []string{"a"}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	// Seed a zombie: status "running", identical params, no live handle
	// (the registry never dispatched it, so r.running has no entry).
	const zombieID = "01ZOMBIEZOMBIEZOMBIEZOMBIE"
	store.insertQueuedAtomic(database.OperationV2Row{
		ID:       zombieID,
		DefID:    def.ID,
		Plugin:   def.Plugin,
		Status:   "running",
		Params:   string(raw),
		QueuedAt: time.Now().UTC(),
	})

	got, err := r.EnqueueOp(context.Background(), def.ID, p)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if got == zombieID {
		t.Fatal("enqueue deduped against a zombie running row — the C-3 skip regressed")
	}
	if got == "" {
		t.Fatal("EnqueueOp returned an empty op id")
	}
}

// TestEnqueueOp_DedupeStillFiresForTheCommonDoubleClick is the
// ANTI-OVER-SUPPRESSION test: without it, a change that simply deleted the
// dedupe block would pass every "queues a second op" test above. Three
// identical enqueues in a row must leave exactly ONE active row.
func TestEnqueueOp_DedupeStillFiresForTheCommonDoubleClick(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeDedupeDef(t, "test.dedupe-doubleclick", "t.doubleclick")
	if def.DedupeQueuedRuns {
		t.Fatal("fixture must have DedupeQueuedRuns=false so byte-equality is what is under test")
	}
	if def.Schedule != nil {
		t.Fatal("fixture must have a nil Schedule so byte-equality is what is under test")
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	p := dedupeParams{BookIDs: []string{"a", "b", "c"}}
	ids := make([]string, 0, 3)
	for i := range 3 {
		id, err := r.EnqueueOp(context.Background(), def.ID, p)
		if err != nil {
			t.Fatalf("EnqueueOp #%d: %v", i+1, err)
		}
		ids = append(ids, id)
	}

	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Errorf("three identical enqueues returned different ids: %v", ids)
	}
	if rows := activeRowsForDef(t, store, def.ID); len(rows) != 1 {
		t.Errorf("expected exactly 1 active row after 3 identical enqueues, got %d", len(rows))
	}
}
