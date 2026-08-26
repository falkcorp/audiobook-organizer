// file: internal/operations/registry/liveness_contract_test.go
// version: 1.0.0
// guid: c4e91a37-8b02-4d65-9f18-6a30de75b2c1
// last-edited: 2026-08-16

package registry_test

// The liveness contract, added 2026-08-16.
//
// "Report your progress" was a convention for as long as the registry existed.
// Nothing checked it, so an op that never touched its reporter looked exactly
// like an op that reported once and then wedged: both went quiet, both got a
// "stuck" strike at five minutes, and the natural response to both -- raise
// ProgressTimeout -- fixes the second and hides the first forever. That is how
// LoggerFromReporter shipped with its reporter argument discarded for three
// months while eight operations were silently progress-blind, and it is why
// three separate workarounds were applied to the symptom.
//
// OperationDef.Liveness makes the convention a contract by removing the option
// of not answering: there is no valid zero value, so a def cannot reach the
// registry without its author choosing one of three modes. Declaring does not
// make an op report -- LivenessManual still has to call UpdateProgress -- but
// it does make silence a stated position rather than an oversight.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// TestRegisterOp_RejectsUndeclaredLiveness is the whole point of the field: the
// zero value must not register.
func TestRegisterOp_RejectsUndeclaredLiveness(t *testing.T) {
	r, _ := newTestRegistry(t)

	def := makeValidDef("test.liveness-undeclared")
	def.Liveness = registry.LivenessUnspecified

	err := r.RegisterOp(def)
	if err == nil {
		t.Fatal("RegisterOp accepted a def that never declared how it reports progress")
	}
	// The error has to be actionable at 3am, so it names the three ways out
	// rather than just refusing.
	for _, want := range []string{"Liveness", "LivenessRunItems", "LivenessManual", "LivenessNone"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q so the author knows the options; got: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "test.liveness-undeclared") {
		t.Errorf("error should name the offending def; got: %v", err)
	}
}

// TestRegisterOp_AcceptsEachDeclaredMode is the negative control. Without it a
// validator that rejected everything would satisfy the test above while making
// the registry unusable.
func TestRegisterOp_AcceptsEachDeclaredMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    registry.LivenessMode
		timeout time.Duration
	}{
		{name: "run items", mode: registry.LivenessRunItems},
		{name: "manual", mode: registry.LivenessManual},
		{name: "none with an explicit budget", mode: registry.LivenessNone, timeout: 10 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRegistry(t)
			def := makeValidDef("test.liveness-" + strings.ReplaceAll(tc.name, " ", "-"))
			def.Liveness = tc.mode
			def.ProgressTimeout = tc.timeout

			if err := r.RegisterOp(def); err != nil {
				t.Fatalf("RegisterOp rejected a validly declared def: %v", err)
			}
		})
	}
}

// TestRegisterOp_LivenessNoneMustNameItsBudget keeps the escape hatch
// expensive.
//
// If LivenessNone could be declared with no ProgressTimeout it would be the
// cheapest of the three answers -- one word, no thought, inherit the 5m default
// by silence -- and every migration would reach for it. Requiring a number
// makes the author state how long this specific op may run without a sign of
// life, which is the question the mode exists to force.
func TestRegisterOp_LivenessNoneMustNameItsBudget(t *testing.T) {
	r, _ := newTestRegistry(t)

	def := makeValidDef("test.liveness-none-no-budget")
	def.Liveness = registry.LivenessNone
	def.ProgressTimeout = 0

	err := r.RegisterOp(def)
	if err == nil {
		t.Fatal("RegisterOp accepted LivenessNone with no ProgressTimeout — the cheap answer must not be the default one")
	}
	if !strings.Contains(err.Error(), "ProgressTimeout") {
		t.Errorf("error should say what is missing; got: %v", err)
	}
}

// TestRegisterOp_LivenessDoesNotWeakenExistingValidation guards against the
// new check short-circuiting the old ones. Validation order is not obvious from
// reading the function, and a nil Run must still be caught regardless of what
// Liveness says.
func TestRegisterOp_LivenessDoesNotWeakenExistingValidation(t *testing.T) {
	r, _ := newTestRegistry(t)

	def := makeValidDef("test.liveness-nil-run")
	def.Liveness = registry.LivenessRunItems
	def.Run = nil

	if err := r.RegisterOp(def); err == nil {
		t.Fatal("RegisterOp accepted a nil Run because Liveness was declared")
	}

	def2 := makeValidDef("test.liveness-unspecified-resume")
	def2.Liveness = registry.LivenessManual
	def2.ResumePolicy = registry.ResumeUnspecified

	if err := r.RegisterOp(def2); err == nil {
		t.Fatal("RegisterOp accepted ResumeUnspecified because Liveness was declared")
	}
}

// TestLivenessMode_String pins the strings that reach logs and errors. A mode
// that printed as an integer would make the boot-time WARN listing the opted-out
// ops unreadable, which is the only thing keeping that set visible.
func TestLivenessMode_String(t *testing.T) {
	cases := map[registry.LivenessMode]string{
		registry.LivenessUnspecified: "unspecified",
		registry.LivenessRunItems:    "run_items",
		registry.LivenessManual:      "manual",
		registry.LivenessNone:        "none",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Errorf("LivenessMode(%d).String() = %q, want %q", int(mode), got, want)
		}
	}
}

// TestRegisteredOpStillRuns is an end-to-end check that declaring liveness did
// not break dispatch. The validator runs on a path every op takes, so a mistake
// here would disable the registry rather than tighten it.
func TestRegisteredOpStillRuns(t *testing.T) {
	ctx := t.Context()

	r, store := newTestRegistry(t)
	ran := make(chan struct{})

	def := makeValidDef("test.liveness-e2e")
	def.Liveness = registry.LivenessManual
	def.Run = func(_ context.Context, _ json.RawMessage, rep registry.Reporter) error {
		_ = rep.UpdateProgress(1, 1, "done")
		close(ran)
		return nil
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}
	r.Start(ctx)

	opID, err := r.EnqueueOp(ctx, "test.liveness-e2e", nil)
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("declared op never ran")
	}
	awaitStatus(t, store, opID, "completed", 5*time.Second)
	_ = slog.Default()
}
