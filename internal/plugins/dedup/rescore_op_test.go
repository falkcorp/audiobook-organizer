// file: internal/plugins/dedup/rescore_op_test.go
// version: 1.0.0
// guid: 6b3d90e7-4c21-4a58-8f07-1d92e5cb37a0
// last-edited: 2026-09-02

package dedup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// fakeOpRegistry records EnqueueOp calls so a test can assert WHAT the config
// sink queued instead of running the re-band inline.
type fakeOpRegistry struct {
	defs      []sdk.OperationDef
	enqueued  []string
	params    []any
	enqueueID string
	enqueueEr error
}

func (f *fakeOpRegistry) RegisterOp(def sdk.OperationDef) error {
	f.defs = append(f.defs, def)
	return nil
}

func (f *fakeOpRegistry) EnqueueOp(_ context.Context, defID string, params any, _ ...sdk.EnqueueOption) (string, error) {
	f.enqueued = append(f.enqueued, defID)
	f.params = append(f.params, params)
	return f.enqueueID, f.enqueueEr
}

// TestDedupScoreSink_SwapsLadderAndQueuesRescore is D4: the config PUT's sink
// must apply the ladder to the live engine SYNCHRONOUSLY (the engine has to
// score on what was just persisted) but must NOT re-band the backlog inline —
// on production's 27k pending rows that turned a config save into a
// minutes-long HTTP request holding no lock against a running dedup.full-scan.
// It queues dedup.rescore and hands back the op id.
//
// Mutation check: replace the EnqueueOp call with an inline
// engine.ReloadScoreConfig and this test fails — nothing is enqueued and no
// op id comes back.
func TestDedupScoreSink_SwapsLadderAndQueuesRescore(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	p := newCalibratePlugin(t, pebble, es)
	reg := &fakeOpRegistry{enqueueID: "01JRESCOREOP"}
	p.registry = reg

	cfg := unified.DefaultScoreConfig()
	cfg.BandCertainMin = 93

	opID, err := p.dedupScoreSink(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dedupScoreSink: %v", err)
	}
	if opID != "01JRESCOREOP" {
		t.Errorf("op id = %q, want the queued op's id", opID)
	}
	if got := p.engine.ScoreConfig().BandCertainMin; got != 93 {
		t.Errorf("the live engine did not get the new ladder synchronously: band_certain_min = %v", got)
	}
	if len(reg.enqueued) != 1 || reg.enqueued[0] != "dedup.rescore" {
		t.Fatalf("expected exactly one dedup.rescore enqueue, got %v", reg.enqueued)
	}
	params, ok := reg.params[0].(RescoreParams)
	if !ok || !params.Apply {
		t.Errorf("the queued re-band must have apply=true (a dry run changes no band); params = %+v", reg.params[0])
	}
}

// TestDedupScoreSink_EnqueueFailureNamesTheRemedy: if the op cannot be queued
// the caller is told the ladder is live but the rows are not re-banded, and
// which endpoint finishes the job. Silence here is how a backlog keeps
// auto-resolving on the previous ladder.
func TestDedupScoreSink_EnqueueFailureNamesTheRemedy(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	p := newCalibratePlugin(t, pebble, es)
	p.registry = &fakeOpRegistry{enqueueEr: errors.New("queue closed")}

	_, err := p.dedupScoreSink(context.Background(), unified.DefaultScoreConfig())
	if err == nil {
		t.Fatal("sink returned nil error although the re-band could not be queued")
	}
	if !strings.Contains(err.Error(), "POST /api/v1/dedup/rescore") {
		t.Errorf("error must name the remedy endpoint, got: %v", err)
	}
}

// TestRescoreDef_SharesFullScanConcurrencyKey: the re-band writes the same
// candidate rows dedup.full-scan writes, so the two must be serialized by the
// dispatcher rather than racing on dedupRecKey.
//
// Mutation check: give rescoreDef its own ConcurrencyKey and this fails.
func TestRescoreDef_SharesFullScanConcurrencyKey(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	p := newCalibratePlugin(t, pebble, es)

	def := p.rescoreDef()
	if def.ID != "dedup.rescore" {
		t.Fatalf("op id = %q", def.ID)
	}
	if got, want := def.ConcurrencyKey, p.fullScanDef().ConcurrencyKey; got != want {
		t.Errorf("dedup.rescore ConcurrencyKey = %q, want dedup.full-scan's %q so a re-band and a scan cannot write the same candidate rows at once", got, want)
	}
}

// TestRescoreOp_RegisteredWithPlugin: the op has to be in the plugin's
// registration list, or the sink enqueues a defID the registry does not know.
func TestRescoreOp_RegisteredWithPlugin(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	p := newCalibratePlugin(t, pebble, es)
	reg := &fakeOpRegistry{}
	if err := p.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, d := range reg.defs {
		if d.ID == "dedup.rescore" {
			return
		}
	}
	t.Fatalf("dedup.rescore is not registered; the config sink would enqueue an unknown defID")
}
