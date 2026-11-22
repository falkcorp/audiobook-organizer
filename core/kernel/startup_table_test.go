// file: core/kernel/startup_table_test.go
// version: 0.1.0
// guid: 1f7d3b58-6a20-4e94-8c3d-b5e9a02f7164
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/module"
)

// recordedEntry runs rows through a real Registrar, so the entry carries what
// Register would have left in e.rec rather than a hand-built slice.
func recordedEntry(t *testing.T, id module.ID, enabled bool, rows ...health.StartupStep) *entry {
	t.Helper()
	r := module.NewRegistrar(module.RegistrarConfig{Context: t.Context(), Deps: module.Deps{Module: id}})
	r.StartupSteps(rows...)
	if err := r.Err(); err != nil {
		t.Fatalf("module %s: StartupSteps: %v", id, err)
	}
	return &entry{man: module.Manifest{ID: id}, enabled: enabled, rec: r.Close()}
}

func stepRowNames(st *health.Startup) []string {
	var out []string
	for _, s := range st.Steps() {
		out = append(out, s.Name)
	}
	return out
}

// The table holds core's rows and every enabled module's recorded rows; a
// disabled module's rows are absent, because a module that is off runs none of
// its steps.
func TestStartupTable_CorePlusEnabledModules(t *testing.T) {
	lamp := health.StartupStep{Name: "gotham.lamp", Class: health.Fatal, Reason: "the signal cannot light without it"}
	archive := health.StartupStep{Name: "gotham.archive", Class: health.Degraded, Token: "archive_stale", Reason: "serves without the archive"}
	dark := health.StartupStep{Name: "arkham.cells", Class: health.Fatal, Reason: "no cell, no Arkham"}
	tbl, err := newStartupTable(nil, nil, []*entry{
		recordedEntry(t, "gotham", true, lamp, archive),
		recordedEntry(t, "arkham", false, dark),
		recordedEntry(t, "wayne", true), // enabled, no rows
	})
	if err != nil {
		t.Fatalf("newStartupTable: %v", err)
	}
	want := []string{"gotham.lamp", "gotham.archive"}
	for _, c := range health.CoreStartupSteps() {
		want = append(want, c.Name)
	}
	got := stepRowNames(tbl.startup)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("table rows = %v, want %v (core rows plus the enabled module's, not arkham's)", got, want)
	}
	if slices.Contains(got, "arkham.cells") {
		t.Fatalf("a disabled module's row is in the table: %v", got)
	}
}

// A module row keeps its class through the table: a failing Degraded row
// records its token and lets startup go on, a failing Fatal row stops it, and a
// name no row holds is refused.
func TestStartupTable_ModuleRowsKeepTheirClass(t *testing.T) {
	boom := errors.New("the grapple jammed")
	tbl, err := newStartupTable(nil, nil, []*entry{recordedEntry(t, "gotham", true,
		health.StartupStep{Name: "gotham.lamp", Class: health.Fatal, Reason: "needed to serve"},
		health.StartupStep{Name: "gotham.archive", Class: health.Degraded, Token: "archive_stale", Reason: "serves without it"},
	)})
	if err != nil {
		t.Fatalf("newStartupTable: %v", err)
	}
	ctx := context.Background()
	fail := func(context.Context) error { return boom }
	if err := tbl.startup.Run(ctx, "gotham.archive", fail); err != nil {
		t.Fatalf("a failing Degraded module row returned %v, want nil", err)
	}
	if got := tbl.startup.Degraded(); !slices.Equal(got, []string{"archive_stale"}) {
		t.Fatalf("Degraded = %v, want [archive_stale]", got)
	}
	if err := tbl.startup.Run(ctx, "gotham.lamp", fail); !errors.Is(err, boom) {
		t.Fatalf("a failing Fatal module row returned %v, want the step's error", err)
	}
	ran := false
	if err := tbl.startup.Run(ctx, "gotham.unlisted", func(context.Context) error { ran = true; return nil }); !errors.Is(err, health.ErrUnknownStep) || ran {
		t.Fatalf("an unlisted name returned %v (ran=%v), want ErrUnknownStep without running", err, ran)
	}
}

// A row core already holds, or another module's, is refused, and the error names
// the module that came second. Every offender is reported in one error, and no
// table is returned.
func TestStartupTable_NameClashesNameTheModule(t *testing.T) {
	core := health.CoreStartupSteps()[0].Name
	first := health.StartupStep{Name: "gotham.lamp", Class: health.Fatal}
	tbl, err := newStartupTable(nil, nil, []*entry{
		recordedEntry(t, "gotham", true, first),
		recordedEntry(t, "arkham", true, first),                                              // another module's name
		recordedEntry(t, "wayne", true, health.StartupStep{Name: core, Class: health.Fatal}), // core's name
		recordedEntry(t, "robin", true, health.StartupStep{Name: "robin.zip", Class: health.Fatal}),
	})
	if err == nil || tbl != nil {
		t.Fatalf("newStartupTable = (%v, %v), want an error and no table", tbl, err)
	}
	msg := err.Error()
	for _, want := range []string{"module arkham: startup steps", "module wayne: startup steps", "already in the table"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q lacks %q", msg, want)
		}
	}
	for _, not := range []string{"module gotham:", "module robin:"} {
		if strings.Contains(msg, not) {
			t.Errorf("error %q blames a module that clashed with nobody (%s)", msg, not)
		}
	}
}
