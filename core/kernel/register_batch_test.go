// file: core/kernel/register_batch_test.go
// version: 0.1.0
// guid: 36ad83db-8262-416e-8c0b-da1f4ed86059
// last-edited: 2026-11-22

package kernel

import (
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/config"
)

// TestRegisterSectionsBatchIndexMatchesPerSection: config.RegisterSections
// rebuilds a store's key index once at the end of the batch instead of once
// per section (the per-section rebuild walked every registered key each time,
// which made registration quadratic in the section count and cost a fifth of
// a boot's CPU with three config stores per boot). The index a batch leaves
// must be the one the per-section rebuilds left: for the kernel's real core
// section set, registered one at a time through a registry that cannot batch
// (each Register rebuilds the index, as every Register used to) and all in
// one call, every key's state is identical, in the same order, and again
// after Load.
// A batch that fails part-way still indexes the sections it registered.
func TestRegisterSectionsBatchIndexMatchesPerSection(t *testing.T) {
	secs := coreSectionsWith(CoreDefaults{})
	if len(secs) < 10 {
		t.Fatalf("only %d core sections: the real set is not what this test registers", len(secs))
	}
	newStore := func(t *testing.T) *config.Store {
		t.Helper()
		st, err := config.NewStore(config.Options{AppID: "hello", Environ: []string{}, Logger: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close(t.Context()) })
		return st
	}
	keys := func(st *config.Store) (paths []config.Path, states []config.KeyState) {
		for p, ks := range st.Keys() {
			paths = append(paths, p)
			states = append(states, ks)
		}
		return paths, states
	}
	compare := func(t *testing.T, when string, one, batch *config.Store) {
		t.Helper()
		op, os := keys(one)
		bp, bs := keys(batch)
		if len(op) == 0 {
			t.Fatalf("%s: the per-section store indexes no key", when)
		}
		if !reflect.DeepEqual(op, bp) {
			t.Fatalf("%s: the batch indexes %d keys, the per-section registrations %d (or in another order)", when, len(bp), len(op))
		}
		for i := range os {
			if !reflect.DeepEqual(os[i], bs[i]) {
				t.Errorf("%s: key %s: batch %+v, per-section %+v", when, op[i], bs[i], os[i])
			}
		}
	}

	// unbatched hides the store registry's batch method, so RegisterSections
	// over it takes the per-section path every Register took before the
	// batch: an index rebuild after each section.
	unbatched := func(st *config.Store) config.Registry { return struct{ config.Registry }{st.Registry()} }
	one, batch := newStore(t), newStore(t)
	for _, s := range secs {
		if _, err := config.RegisterSections(unbatched(one), s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := config.RegisterSections(batch.Registry(), secs...); err != nil {
		t.Fatal(err)
	}
	compare(t, "after registration", one, batch)
	for _, st := range []*config.Store{one, batch} {
		if err := st.Load(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	compare(t, "after Load", one, batch)

	t.Run("a batch that fails part-way indexes what it registered", func(t *testing.T) {
		part, ref := newStore(t), newStore(t)
		if _, err := config.RegisterSections(part.Registry(), append(secs[:3:3], "riddler")...); !errors.Is(err, config.ErrInvalid) {
			t.Fatalf("a non-Section element: %v, want ErrInvalid", err)
		}
		for _, s := range secs[:3] {
			if _, err := config.RegisterSections(unbatched(ref), s); err != nil {
				t.Fatal(err)
			}
		}
		compare(t, "after the failed batch", ref, part)
	})
}
