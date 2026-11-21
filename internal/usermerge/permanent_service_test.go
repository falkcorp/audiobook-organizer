// file: internal/usermerge/permanent_service_test.go
// version: 0.1.0
// guid: 813c824d-e2b3-4009-b803-694b187d50ca
// last-edited: 2026-11-21

package usermerge

import (
	"errors"
	"testing"
)

// CheckAction reads the flip from the service's wiring at call time: without
// the option it is HEAD's answer, and a flip that happens after construction is
// seen.
func TestCheckAction_ReadsTheFlipFromTheWiring(t *testing.T) {
	rows := journalRow("j1", StatusApplied)
	svc := permanentSvc(rows)
	if err := svc.CheckAction(ActionUndo, "j1"); err != nil {
		t.Fatalf("a service built without WithIdentityFlipped must answer as HEAD: %v", err)
	}
	flipped := false
	WithIdentityFlipped(func() bool { return flipped })(svc)
	if err := svc.CheckAction(ActionUndo, "j1"); err != nil {
		t.Fatalf("not flipped: %v", err)
	}
	flipped = true
	for _, action := range []Action{ActionUndo, ActionResume} {
		if err := svc.CheckAction(action, "j1"); !errors.Is(err, ErrMergePermanent) {
			t.Fatalf("%s after the flip = %v, want ErrMergePermanent", action, err)
		}
	}
	if err := svc.CheckAction(ActionApply, "j1"); err != nil {
		t.Fatalf("an apply names no journal and is not refused: %v", err)
	}
}
