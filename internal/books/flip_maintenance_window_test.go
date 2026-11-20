// file: internal/books/flip_maintenance_window_test.go
// version: 1.0.0
// guid: 3a7f9c25-6e14-4d80-b2a9-d41c58e07b36
// last-edited: 2026-11-20

package books

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	coreconfig "github.com/falkcorp/mediacore/config"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// The maintenance window (maintenance.{enabled,window_start,window_end}) is
// stored by core as members of the one map key core.tasks.windows. The
// differential oracle found the Engine reading none of them (the projected
// Config kept a zero window, so scheduler/maintenance.go never ran a task) and M2
// quarantining every stored value. These tests run both halves against a real
// core store: a stored window projects, and an Update of each field writes
// through and keeps its siblings.

type windowView struct {
	Enabled bool
	Start   int
	End     int
}

func windowOf(c config.Config) windowView {
	return windowView{c.Maintenance.Enabled, c.Maintenance.WindowStart, c.Maintenance.WindowEnd}
}

func missingRows(t *testing.T) []string {
	t.Helper()
	m, ok := config.Current().(interface{ Missing() []string })
	require.True(t, ok, "the installed Facade does not report Missing")
	return m.Missing()
}

func TestMaintenanceWindow_StoredWindowProjectsFromCore(t *testing.T) {
	fx := newFlipFixture(t, headSaved(t, func(c *config.Config) {
		c.Maintenance.Enabled, c.Maintenance.WindowStart, c.Maintenance.WindowEnd = false, 3, 6
	}), "")
	var got windowView
	var missing []string
	fx.runFlipped(t, func(*composed) {
		got = windowOf(config.Snapshot())
		missing = missingRows(t)
	})
	require.Equal(t, windowView{false, 3, 6}, got, "the stored window did not reach the projected Config")
	require.Empty(t, missing, "rows the core store does not register")
}

func TestMaintenanceWindow_UpdateRoundTripsOnRealCore(t *testing.T) {
	bootFlippedFresh(t)
	f := config.Current()
	require.NotNil(t, f)
	require.Equal(t, windowView{true, 1, 4}, windowOf(config.Snapshot()), "a fresh install runs HEAD's default window")
	require.Empty(t, missingRows(t))

	set := func(kv map[string]string) (config.UpdateResult, error) {
		raw := map[string]json.RawMessage{}
		for k, v := range kv {
			raw[k] = json.RawMessage(v)
		}
		return f.Update(t.Context(), settingsAdmin, config.Update{Set: raw})
	}
	want := windowView{true, 1, 4}
	for _, step := range []struct {
		name string
		kv   map[string]string
		with func(*windowView)
	}{
		{"enabled", map[string]string{"maintenance.enabled": "false"}, func(w *windowView) { w.Enabled = false }},
		{"start", map[string]string{"maintenance.window_start": "2"}, func(w *windowView) { w.Start = 2 }},
		{"end", map[string]string{"maintenance.window_end": "5"}, func(w *windowView) { w.End = 5 }},
		{"all three at once", map[string]string{"maintenance.enabled": "true", "maintenance.window_start": "22", "maintenance.window_end": "3"},
			func(w *windowView) { *w = windowView{true, 22, 3} }},
	} {
		gen := f.Generation()
		res, err := set(step.kv)
		require.NoError(t, err, step.name)
		step.with(&want)
		require.Equal(t, want, windowOf(config.Snapshot()), "%s: a field the update did not name changed, or the named one did not", step.name)
		require.Greater(t, f.Generation(), gen, step.name)
		require.ElementsMatch(t, windowKeys(step.kv), res.Applied, "%s: Applied must name the HEAD fields, not core's map key", step.name)
	}

	// A value core's window refuses is refused with the field named, and nothing
	// moves.
	gen := f.Generation()
	_, err := set(map[string]string{"maintenance.window_start": "99"})
	require.Error(t, err)
	require.ErrorIs(t, err, coreconfig.ErrInvalid)
	require.Equal(t, want, windowOf(config.Snapshot()))
	require.Equal(t, gen, f.Generation())

	// Clearing a field is its zero value; the others stay.
	_, err = f.Update(t.Context(), settingsAdmin, config.Update{Unset: []string{"maintenance.window_end"}})
	require.NoError(t, err)
	want.End = 0
	require.Equal(t, want, windowOf(config.Snapshot()))
}

func windowKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestEngine_EveryRowAddressesARegisteredCoreKey is the class guard behind the
// maintenance-window bug: a key row whose path is a member of a map-valued core
// key (rather than a key core registers) projects as unset and quarantines in M2.
// A real core boot must register every key and member-map the rename table names.
func TestEngine_EveryRowAddressesARegisteredCoreKey(t *testing.T) {
	bootFlippedFresh(t)
	require.Empty(t, missingRows(t), "rename-table rows that target a path the real core registry does not hold")
}
