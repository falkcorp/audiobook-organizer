// file: internal/plugins/dedup/score_hook_test.go
// version: 0.1.0
// guid: 961e993d-4ea6-4eb6-b84a-6011897b3b27
// last-edited: 2026-11-20

package dedup

import (
	"context"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// hookFacade is a config.Facade that records the UpdateHooks registered on it.
// Only OnUpdate is exercised; the embedded nil Facade panics on anything else.
type hookFacade struct {
	config.Facade
	names []string
	hooks []config.UpdateHook
}

func (f *hookFacade) OnUpdate(name string, fn config.UpdateHook) {
	f.names = append(f.names, name)
	f.hooks = append(f.hooks, fn)
}

// noHookFacade is a Facade that runs no hooks.
type noHookFacade struct{ config.Facade }

// signals returns a Config whose dedup.signals ladder is the given four mins.
func signals(certain, high, medium, review float64) config.Config {
	var c config.Config
	c.Dedup.Signals.BandCertainMin = certain
	c.Dedup.Signals.BandHighMin = high
	c.Dedup.Signals.BandMediumMin = medium
	c.Dedup.Signals.BandReviewMin = review
	return c
}

// TestRegisterScoreHook_OnTheInstalledFacade: PostInit's hook lands on a Facade
// that runs hooks, a Facade that runs none fails the start, and no Facade at
// all (no settings write path) registers nothing and is not an error.
//
// Mutation check: drop the OnUpdate call and the first case fails; return nil
// for a Facade without OnUpdate and the second does.
func TestRegisterScoreHook_OnTheInstalledFacade(t *testing.T) {
	p := &Plugin{}
	f := &hookFacade{}
	if err := p.registerScoreHook(f); err != nil {
		t.Fatalf("registerScoreHook: %v", err)
	}
	if len(f.names) != 1 || f.names[0] != "dedup.signals" {
		t.Fatalf("hooks registered = %v, want exactly dedup.signals", f.names)
	}

	err := p.registerScoreHook(noHookFacade{})
	if err == nil || !strings.Contains(err.Error(), "runs no update hooks") {
		t.Fatalf("a Facade without hooks must fail the start, got %v", err)
	}

	if err := p.registerScoreHook(nil); err != nil {
		t.Fatalf("no Facade installed must not be an error, got %v", err)
	}
}

// TestDedupScoreHook_ChangedLadderReachesEngineAndQueuesRescore: a committed
// Update that moved the ladder swaps it into the live engine and reports the
// queued dedup.rescore under dedup_rescore_op_id, the key the settings handler
// copies into the PUT response. This is what UpdateService's sink did before
// F3-07 deleted the service.
//
// Mutation check: return nil ops from the changed branch and the op id check
// fails; skip dedupScoreSink and the engine check does.
func TestDedupScoreHook_ChangedLadderReachesEngineAndQueuesRescore(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	p := newCalibratePlugin(t, pebble, database.NewEmbeddingStore(pebble.DB()))
	reg := &fakeOpRegistry{enqueueID: "01JHOOKRESCORE"}
	p.registry = reg

	ops, err := p.dedupScoreHook(context.Background(), signals(97, 90, 75, 60), signals(98.5, 91, 76, 61))
	if err != nil {
		t.Fatalf("dedupScoreHook: %v", err)
	}
	if ops[dedupRescoreOpKey] != "01JHOOKRESCORE" {
		t.Fatalf("ops = %v, want %s = the queued op id", ops, dedupRescoreOpKey)
	}
	live := p.engine.ScoreConfig()
	if live.BandCertainMin != 98.5 || live.BandHighMin != 91 || live.BandMediumMin != 76 || live.BandReviewMin != 61 {
		t.Fatalf("the live engine ladder = %.2f/%.2f/%.2f/%.2f, want 98.5/91/76/61",
			live.BandCertainMin, live.BandHighMin, live.BandMediumMin, live.BandReviewMin)
	}
	if len(reg.enqueued) != 1 || reg.enqueued[0] != "dedup.rescore" {
		t.Fatalf("enqueued = %v, want one dedup.rescore", reg.enqueued)
	}
}

// TestDedupScoreHook_UnchangedLadderDoesNothing: an Update that left the ladder
// alone (any other setting) neither touches the engine nor queues a re-band.
//
// Mutation check: drop the DeepEqual short-circuit and a re-band is queued.
func TestDedupScoreHook_UnchangedLadderDoesNothing(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	p := newCalibratePlugin(t, pebble, database.NewEmbeddingStore(pebble.DB()))
	reg := &fakeOpRegistry{enqueueID: "01JNEVER"}
	p.registry = reg

	before, after := signals(97, 90, 75, 60), signals(97, 90, 75, 60)
	after.ConcurrentScans = before.ConcurrentScans + 1
	ops, err := p.dedupScoreHook(context.Background(), before, after)
	if err != nil {
		t.Fatalf("dedupScoreHook: %v", err)
	}
	if len(ops) != 0 || len(reg.enqueued) != 0 {
		t.Fatalf("an unchanged ladder queued work: ops=%v enqueued=%v", ops, reg.enqueued)
	}
}

// TestDedupScoreHook_InvalidLadderIsReportedWithTheRemedy: a ladder that does
// not convert is not handed to the engine; the error names the remedy, and
// nothing is queued.
func TestDedupScoreHook_InvalidLadderIsReportedWithTheRemedy(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	p := newCalibratePlugin(t, pebble, database.NewEmbeddingStore(pebble.DB()))
	reg := &fakeOpRegistry{enqueueID: "01JNEVER"}
	p.registry = reg
	startCertain := p.engine.ScoreConfig().BandCertainMin

	_, err := p.dedupScoreHook(context.Background(), signals(97, 90, 75, 60), signals(1, 90, 75, 60))
	if err == nil || !strings.Contains(err.Error(), "POST /api/v1/dedup/rescore") {
		t.Fatalf("an invalid ladder must be an error naming the remedy, got %v", err)
	}
	if got := p.engine.ScoreConfig().BandCertainMin; got != startCertain {
		t.Errorf("an invalid ladder reached the engine: band_certain_min = %v", got)
	}
	if len(reg.enqueued) != 0 {
		t.Errorf("an invalid ladder queued a re-band: %v", reg.enqueued)
	}
}
