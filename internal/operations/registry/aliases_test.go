// file: internal/operations/registry/aliases_test.go
// version: 1.0.0
// guid: 9b4e2d71-3f6a-4c85-b1d0-6e8a5c3f2b97
// last-edited: 2026-09-25

package registry_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/prometheus/client_golang/prometheus"
)

// deprecatedUses reads audiobook_organizer_operation_deprecated_def_id_total
// for one (alias, entry) pair from the default gatherer. metrics.Register is
// idempotent, so calling it here is safe alongside any other test.
func deprecatedUses(t *testing.T, alias, entry string) float64 {
	t.Helper()
	metrics.Register()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "audiobook_organizer_operation_deprecated_def_id_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["alias"] == alias && labels["entry"] == entry {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// renamedDef is a def that used to be registered as each of former.
func renamedDef(id string, former ...string) registry.OperationDef {
	def := makeValidDef(id)
	def.FormerIDs = former
	return def
}

func TestAliases_DefResolvesFormerIDToCanonicalDef(t *testing.T) {
	r, _ := newTestRegistry(t)
	if err := r.RegisterOp(renamedDef("test.new-name", "old.name")); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	def, ok := r.Def("old.name")
	if !ok {
		t.Fatal("Def(former ID) not found; every alias must resolve")
	}
	if def.ID != "test.new-name" {
		t.Fatalf("Def(former ID).ID = %q, want the canonical test.new-name", def.ID)
	}
	if got := r.CanonicalDefID("old.name"); got != "test.new-name" {
		t.Fatalf("CanonicalDefID(former) = %q", got)
	}
	// Unknown and canonical IDs pass through unchanged.
	if got := r.CanonicalDefID("test.new-name"); got != "test.new-name" {
		t.Fatalf("CanonicalDefID(canonical) = %q", got)
	}
	if got := r.CanonicalDefID("nope.nothing"); got != "nope.nothing" {
		t.Fatalf("CanonicalDefID(unknown) = %q", got)
	}
	if got := r.Aliases(); len(got) != 1 || got["old.name"] != "test.new-name" {
		t.Fatalf("Aliases() = %v", got)
	}
}

// An enqueue under the former ID must run the renamed op and write its row
// under the CANONICAL ID, so nothing downstream ever sees the alias, and it
// must count toward the deprecation metric.
func TestAliases_EnqueueUnderFormerIDWritesCanonicalRow(t *testing.T) {
	r, store := newTestRegistry(t)
	if err := r.RegisterOp(renamedDef("test.enqueue-renamed", "old.enqueue")); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	before := deprecatedUses(t, "old.enqueue", "enqueue")

	opID, err := r.EnqueueOp(context.Background(), "old.enqueue", nil)
	if err != nil {
		t.Fatalf("EnqueueOp(former ID): %v", err)
	}
	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil {
		t.Fatalf("row not written: %v", err)
	}
	if row.DefID != "test.enqueue-renamed" {
		t.Fatalf("row.DefID = %q, want the canonical ID", row.DefID)
	}
	if got := deprecatedUses(t, "old.enqueue", "enqueue") - before; got != 1 {
		t.Fatalf("deprecation counter moved by %v, want 1", got)
	}

	// The canonical ID must not count.
	if _, err := r.EnqueueOp(context.Background(), "test.enqueue-renamed", map[string]int{"n": 2}); err != nil {
		t.Fatalf("EnqueueOp(canonical): %v", err)
	}
	if got := deprecatedUses(t, "old.enqueue", "enqueue") - before; got != 1 {
		t.Fatalf("canonical enqueue counted as deprecated use (moved by %v)", got)
	}
}

// A row persisted under the former ID and an enqueue under the new ID are the
// same op: the ConcurrencyKey dedupe must see through the rename, or a rename
// would let a second copy of a serialized op queue up beside the first.
func TestAliases_EnqueueDedupesAgainstRowStoredUnderFormerID(t *testing.T) {
	r, store := newTestRegistry(t)
	def := renamedDef("test.dedupe-renamed", "old.dedupe")
	def.ConcurrencyKey = "test.dedupe-renamed"
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	existing := insertQueuedOp(store, "old.dedupe", "test", 1)

	got, err := r.EnqueueOp(context.Background(), "test.dedupe-renamed", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	if got != existing {
		t.Fatalf("EnqueueOp returned %q, want the existing former-ID row %q (dedupe did not resolve the alias)", got, existing)
	}
}

// Resume is the case the audit called out: an op interrupted by a restart
// across the deploy that renamed it is persisted under the OLD ID. Resume must
// still find the def and run it.
func TestAliases_ResumeRestartsRowStoredUnderFormerID(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})
	ran := make(chan string, 1)
	def := renamedDef("test.resume-renamed", "old.resume")
	def.ResumePolicy = registry.ResumeRestart
	def.Run = func(ctx context.Context, _ json.RawMessage, _ registry.Reporter) error {
		select {
		case ran <- "ran":
		default:
		}
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	before := deprecatedUses(t, "old.resume", "stored_row")

	opID := insertRunningOp(store, "old.resume", "test", 1)
	r.Start(t.Context())

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		row, _ := store.GetOperationV2(opID)
		status := "<missing>"
		if row != nil {
			status = row.Status
		}
		t.Fatalf("op stored under its former ID was not resumed within 5s (status=%s)", status)
	}
	if got := deprecatedUses(t, "old.resume", "stored_row") - before; got < 1 {
		t.Fatalf("resume of a former-ID row did not count toward the deprecation metric")
	}
	// The row itself is history: resume does not rewrite its def_id.
	row, _ := store.GetOperationV2(opID)
	if row == nil || row.DefID != "old.resume" {
		t.Fatalf("resumed row's stored def_id changed: %+v", row)
	}
}

// ResumeRequeue replaces the interrupted row with a NEW one; the replacement is
// written under the canonical ID.
func TestAliases_ResumeRequeueWritesReplacementUnderCanonicalID(t *testing.T) {
	store := newFakeStore()
	r := registry.NewWithOptions(store, slog.Default(), 2, registry.Options{
		WatchdogInterval: 30 * time.Second,
	})
	ran := make(chan struct{}, 1)
	def := renamedDef("test.requeue-renamed", "old.requeue")
	def.ResumePolicy = registry.ResumeRequeue
	def.Run = func(context.Context, json.RawMessage, registry.Reporter) error {
		select {
		case ran <- struct{}{}:
		default:
		}
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	orig := insertRunningOp(store, "old.requeue", "test", 1)
	r.Start(t.Context())

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("requeued replacement did not run within 5s")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	var replacements int
	for id, row := range store.ops {
		if id == orig {
			continue
		}
		if row.DefID != "test.requeue-renamed" {
			t.Errorf("replacement row %s has DefID %q, want the canonical test.requeue-renamed", id, row.DefID)
		}
		replacements++
	}
	if replacements != 1 {
		t.Fatalf("found %d replacement rows, want 1", replacements)
	}
}

// No alias may shadow a real ID, in either registration order, and no alias
// may be claimed by two defs.
func TestAliases_CollisionsAreRejected(t *testing.T) {
	t.Run("former ID equals an already-registered ID", func(t *testing.T) {
		r, _ := newTestRegistry(t)
		if err := r.RegisterOp(makeValidDef("test.real")); err != nil {
			t.Fatal(err)
		}
		err := r.RegisterOp(renamedDef("test.other", "test.real"))
		if err == nil || !strings.Contains(err.Error(), "shadow") {
			t.Fatalf("want shadow error, got %v", err)
		}
		if def, _ := r.Def("test.real"); def.ID != "test.real" {
			t.Fatalf("real ID now resolves to %q", def.ID)
		}
	})
	t.Run("new ID equals an existing alias", func(t *testing.T) {
		r, _ := newTestRegistry(t)
		if err := r.RegisterOp(renamedDef("test.renamed", "test.real")); err != nil {
			t.Fatal(err)
		}
		err := r.RegisterOp(makeValidDef("test.real"))
		if err == nil || !strings.Contains(err.Error(), "former ID") {
			t.Fatalf("want already-a-former-ID error, got %v", err)
		}
	})
	t.Run("two defs claim the same former ID", func(t *testing.T) {
		r, _ := newTestRegistry(t)
		if err := r.RegisterOp(renamedDef("test.first", "old.shared")); err != nil {
			t.Fatal(err)
		}
		if err := r.RegisterOp(renamedDef("test.second", "old.shared")); err == nil {
			t.Fatal("second claim of the same former ID was accepted")
		}
		if got := r.CanonicalDefID("old.shared"); got != "test.first" {
			t.Fatalf("old.shared now resolves to %q, want test.first", got)
		}
		if _, ok := r.Def("test.second"); ok {
			t.Fatal("rejected def was registered anyway")
		}
	})
}

func TestValidateOpDef_FormerIDRules(t *testing.T) {
	cases := map[string]func(*registry.OperationDef){
		"empty":     func(d *registry.OperationDef) { d.FormerIDs = []string{""} },
		"colon":     func(d *registry.OperationDef) { d.FormerIDs = []string{"old:name"} },
		"own ID":    func(d *registry.OperationDef) { d.FormerIDs = []string{d.ID} },
		"duplicate": func(d *registry.OperationDef) { d.FormerIDs = []string{"old.a", "old.a"} },
		"batchable": func(d *registry.OperationDef) { d.Batchable = true; d.FormerIDs = []string{"old.a"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			def := makeValidDef("test.validate-former")
			mutate(&def)
			if err := registry.ValidateOpDef(def); err == nil {
				t.Fatalf("ValidateOpDef accepted FormerIDs case %q", name)
			}
		})
	}
	ok := makeValidDef("test.validate-former")
	ok.FormerIDs = []string{"old.a", "old.b"}
	if err := registry.ValidateOpDef(ok); err != nil {
		t.Fatalf("valid FormerIDs rejected: %v", err)
	}
}
