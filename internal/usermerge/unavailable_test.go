// file: internal/usermerge/unavailable_test.go
// version: 0.1.0
// guid: 8e0b53c7-1a2d-4f96-b4c8-6d71e3a90f25
// last-edited: 2026-11-21

package usermerge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// touchedStore counts every call the service makes into the store, whichever
// method, and answers each with a benign empty value, so a guard that goes
// missing shows as a wrong error or a nonzero count in an assertion and never
// as a nil-interface panic.
type touchedStore struct {
	journalRows
	calls atomic.Int64
}

func (s *touchedStore) GetUserByID(string) (*database.User, error) {
	s.calls.Add(1)
	return nil, nil
}

func (s *touchedStore) GetRaw(key string) ([]byte, error) {
	s.calls.Add(1)
	return s.journalRows.GetRaw(key)
}

func (s *touchedStore) SetRaw(string, []byte) error {
	s.calls.Add(1)
	return nil
}

func (s *touchedStore) ScanPrefix(string) ([]database.KVPair, error) {
	s.calls.Add(1)
	return nil, nil
}

func (s *touchedStore) ListUserSessions(string) ([]database.Session, error) {
	s.calls.Add(1)
	return nil, nil
}

func (s *touchedStore) RevokeSession(string) error {
	s.calls.Add(1)
	return nil
}

func (s *touchedStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	s.calls.Add(1)
	return nil, nil
}

func TestProblemUnavailableUnderIdentity_IsTheSpecsCode(t *testing.T) {
	if ProblemUnavailableUnderIdentity != "merge_unavailable_under_identity" {
		t.Fatalf("problem code = %q", ProblemUnavailableUnderIdentity)
	}
}

// F4-08 B10l guard: with the identity flip wired, no entry point plans, applies
// or resumes, and none calls into the store to find out. Each entry point is its
// own subtest so a guard dropped from one of them (the others still refuse, one
// layer down) is caught by its own check.
func TestMergeEntryPoints_RefuseBeforeTouchingTheStoreWhenFlipped(t *testing.T) {
	ctx := context.Background()
	entry := map[string]func(*Service) error{
		"BuildPlan": func(svc *Service) error {
			_, err := svc.BuildPlan(ctx, PlanRequest{ActorID: "bruce", SurvivorID: "bruce", AbsorbedID: "dick"})
			return err
		},
		"Apply": func(svc *Service) error {
			_, err := svc.Apply(ctx, ApplyRequest{ActorID: "bruce", SurvivorID: "bruce", AbsorbedID: "dick", Digest: "d"}, nil)
			return err
		},
		"Resume": func(svc *Service) error {
			_, err := svc.Resume(ctx, "j1", nil)
			return err
		},
		"CheckAvailable": func(svc *Service) error { return svc.CheckAvailable() },
	}
	for name, call := range entry {
		t.Run(name, func(t *testing.T) {
			store := &touchedStore{journalRows: journalRows{rows: journalRow("j1", StatusApplying)}}
			flipped := true
			svc := &Service{store: store, identityFlipped: func() bool { return flipped }}
			if err := call(svc); !errors.Is(err, ErrUnavailableUnderIdentity) {
				t.Errorf("%s = %v, want ErrUnavailableUnderIdentity", name, err)
			}
			if n := store.calls.Load(); n != 0 {
				t.Errorf("%s called into the store %d time(s) before refusing", name, n)
			}
			// The flip is read at call time.
			flipped = false
			if name == "CheckAvailable" {
				if err := call(svc); err != nil {
					t.Errorf("not flipped: %v", err)
				}
			}
		})
	}
	// A service built without WithIdentityFlipped is HEAD's.
	if err := (&Service{store: &touchedStore{}}).CheckAvailable(); err != nil {
		t.Errorf("built without WithIdentityFlipped: %v", err)
	}
}
