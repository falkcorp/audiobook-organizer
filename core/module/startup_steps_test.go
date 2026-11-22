// file: core/module/startup_steps_test.go
// version: 0.1.0
// guid: 5c1e8d37-9a42-4b60-8f13-d27a6e0b4c95
// last-edited: 2026-11-22

package module

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/health"
)

// stepNames is the Name of each row, in order.
func stepNames(rows []health.StartupStep) []string {
	out := make([]string, len(rows))
	for i, st := range rows {
		out[i] = st.Name
	}
	return out
}

// Registrar.StartupSteps records the module's rows in call order, across
// calls, and Close hands the kernel a copy: a row appended after Close (a
// goroutine Register started) must not appear in a slice the kernel already
// holds.
func TestRegistrar_StartupStepsRecordedInCallOrder(t *testing.T) {
	r := newTestRegistrar(t, false)
	r.StartupSteps(
		health.StartupStep{Name: "gotham.lamp", Class: health.Fatal, Reason: "the signal cannot light without it"},
		health.StartupStep{Name: "gotham.archive", Class: health.Degraded, Token: "archive_stale", Reason: "serves without the archive"},
	)
	r.StartupSteps(health.StartupStep{Name: "gotham.cells", Class: health.Fatal, Reason: "no cell, no Arkham"})
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v", err)
	}
	rec := r.Close()
	if got, want := stepNames(rec.StartupSteps), []string{"gotham.lamp", "gotham.archive", "gotham.cells"}; !slices.Equal(got, want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
	if st := rec.StartupSteps[1]; st.Class != health.Degraded || st.Token != "archive_stale" || st.Reason != "serves without the archive" {
		t.Fatalf("row 1 = %+v, want the Degraded row as registered", st)
	}
	rec.StartupSteps[0].Name = "mutated"
	if again := r.Close(); again.StartupSteps[0].Name != "gotham.lamp" {
		t.Fatalf("Close returned the registrar's own slice: a second Close sees %q", again.StartupSteps[0].Name)
	}
}

// A module that registers no rows records none, and the kernel's table is not
// touched by a module that never calls StartupSteps.
func TestRegistrar_NoStartupStepsByDefault(t *testing.T) {
	r := newTestRegistrar(t, false)
	if rec := r.Close(); len(rec.StartupSteps) != 0 {
		t.Fatalf("recorded %v with no call", rec.StartupSteps)
	}
}

// A row health.Startup would refuse is ProblemInvalidRegistration, and the whole
// call is dropped (all or none, as Startup.Add): the valid row beside the bad
// one is not recorded. A duplicate against an earlier call is refused too.
func TestRegistrar_StartupStepsRefusals(t *testing.T) {
	good := health.StartupStep{Name: "gotham.lamp", Class: health.Fatal, Reason: "needed to serve"}
	cases := map[string]struct {
		prior []health.StartupStep
		call  []health.StartupStep
		want  string
	}{
		"empty name":         {call: []health.StartupStep{{Class: health.Fatal}}, want: "empty"},
		"padded name":        {call: []health.StartupStep{{Name: " gotham.lamp", Class: health.Fatal}}, want: "surrounding space"},
		"no class":           {call: []health.StartupStep{{Name: "gotham.vault"}}, want: "no class"},
		"fatal with a token": {call: []health.StartupStep{{Name: "gotham.vault", Class: health.Fatal, Token: "vault_cold"}}, want: "has a token"},
		"degraded no token":  {call: []health.StartupStep{{Name: "gotham.vault", Class: health.Degraded}}, want: "needs a token"},
		"degraded bad token": {call: []health.StartupStep{{Name: "gotham.vault", Class: health.Degraded, Token: "Vault Cold"}}, want: "needs a token"},
		"twice in one call":  {call: []health.StartupStep{good, good}, want: "twice"},
		"already registered": {prior: []health.StartupStep{good}, call: []health.StartupStep{good}, want: "twice"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newTestRegistrar(t, false)
			if len(tc.prior) > 0 {
				r.StartupSteps(tc.prior...)
			}
			// A valid row in the same call must be dropped with the bad one.
			r.StartupSteps(append([]health.StartupStep{{Name: "gotham.sibling", Class: health.Fatal}}, tc.call...)...)
			err := r.Err()
			if !errors.Is(err, ErrInvalidRegistration) || !strings.Contains(err.Error(), "StartupSteps") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Err = %v, want ErrInvalidRegistration naming StartupSteps and %q", err, tc.want)
			}
			if got := stepNames(r.Close().StartupSteps); !slices.Equal(got, stepNames(tc.prior)) {
				t.Fatalf("recorded %v after a refused call, want only the earlier call's %v", got, stepNames(tc.prior))
			}
		})
	}
}

// After Close the call takes no effect and is reported, like every other
// registrar call.
func TestRegistrar_StartupStepsAfterClose(t *testing.T) {
	var late []Problem
	r := NewRegistrar(RegistrarConfig{Context: t.Context(), Deps: Deps{Module: "batcave"},
		AfterClose: func(p Problem) { late = append(late, p) }})
	r.Close()
	r.StartupSteps(health.StartupStep{Name: "batcave.late", Class: health.Fatal})
	if len(late) != 1 || late[0].Kind != ProblemRegistrarClosed || !strings.Contains(late[0].Detail, "StartupSteps") {
		t.Fatalf("AfterClose got %v, want one ProblemRegistrarClosed naming StartupSteps", late)
	}
	if rec := r.Close(); len(rec.StartupSteps) != 0 {
		t.Fatalf("a call after Close recorded %v", rec.StartupSteps)
	}
}
