// file: internal/server/op_registration_gate_test.go
// version: 1.0.0
// guid: 7a1f4c92-3e58-4b6d-9c07-2d8ba5f1e304
// last-edited: 2026-08-16

package server

import (
	"errors"
	"strings"
	"testing"
)

// TestStart_RefusesWhenOpRegistrationFailed pins the gate added on 2026-08-16.
//
// NewServer's op-registrar loop used to log a failed RegisterOp at WARN and
// continue. The result was a server that booted clean, passed every health
// check, and was simply missing an operation -- whatever enqueued it later got
// "unknown op" from an otherwise healthy process, which is a much harder thing
// to diagnose than a refusal to start. This asserts the refusal happens, and
// that the message carries enough to find the culprit.
func TestStart_RefusesWhenOpRegistrationFailed(t *testing.T) {
	s := &Server{
		opRegistrationErrs: []error{
			errors.New("registry: RegisterOp(library.scan): boom"),
			errors.New("registry: RegisterOp(dedup.full-scan): also boom"),
		},
	}

	err := s.Start(ServerConfig{})
	if err == nil {
		t.Fatal("Start returned nil with 2 registration failures pending; a server missing ops must not serve")
	}

	// The count tells an operator how bad it is; the joined causes tell them
	// which ops to look at. A bare "registration failed" would send them to the
	// logs to reconstruct both.
	if !strings.Contains(err.Error(), "2 op(s)") {
		t.Errorf("error should name how many ops failed, got: %v", err)
	}
	for _, want := range []string{"library.scan", "dedup.full-scan"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the failed op %q, got: %v", want, err)
		}
	}
}

// TestOpRegistrationGate_ClearWhenNothingFailed is the negative control for the
// test above. Without it a gate that refused unconditionally would still pass
// TestStart_RefusesWhenOpRegistrationFailed, and the gate would look tested
// while measuring nothing.
//
// This exercises opRegistrationGate rather than Start on purpose: Start's later
// stages spawn background goroutines against a fully wired Server, so a bare
// &Server{} run through Start panics in startBackfills instead of
// demonstrating that the gate let it through. Measured, not assumed -- that is
// what the first draft of this test did.
func TestOpRegistrationGate_ClearWhenNothingFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		errs []error
	}{
		{name: "nil slice", errs: nil},
		{name: "empty slice", errs: []error{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{opRegistrationErrs: tc.errs}
			if err := s.opRegistrationGate(); err != nil {
				t.Errorf("gate refused a healthy registry: %v", err)
			}
		})
	}
}

// TestOpRegistrationGate_OneFailureIsEnough guards the boundary. A gate written
// as "more than a few failures" or one that only fired on a plural count would
// let a single missing op through -- and one missing op is the whole failure
// mode, not a lesser version of it.
func TestOpRegistrationGate_OneFailureIsEnough(t *testing.T) {
	s := &Server{opRegistrationErrs: []error{errors.New("registry: RegisterOp(library.scan): boom")}}

	err := s.opRegistrationGate()
	if err == nil {
		t.Fatal("gate allowed startup with 1 failed registration")
	}
	if !strings.Contains(err.Error(), "1 op(s)") {
		t.Errorf("error should report a count of 1, got: %v", err)
	}
}
