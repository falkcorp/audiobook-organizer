// file: internal/usermerge/permanent_test.go
// version: 0.1.0
// guid: 0b6d4f1c-7e29-4a53-8c1d-93a5e2b71f48
// last-edited: 2026-11-20

package usermerge

import (
	"errors"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// journalRows is a Store whose only readable thing is raw rows by key.
type journalRows struct {
	Store
	rows   map[string][]byte
	getErr error
}

func (j journalRows) GetRaw(key string) ([]byte, error) {
	if j.getErr != nil {
		return nil, j.getErr
	}
	return j.rows[key], nil
}

func permanentSvc(rows map[string][]byte) *Service {
	return &Service{store: journalRows{rows: rows}}
}

func journalRow(id, status string) map[string][]byte {
	return map[string][]byte{
		database.UserMergeJournalPrefix + id: []byte(`{"id":"` + id + `","status":"` + status + `","survivor_id":"bruce","absorbed_id":"dick"}`),
	}
}

func TestMergePermanent_ProblemCodeIsTheSpecs(t *testing.T) {
	if ProblemMergePermanent != "merge_permanent" {
		t.Fatalf("problem code = %q", ProblemMergePermanent)
	}
}

func TestCheckMergeAction_FlippedRefusesEveryStatusForUndoAndResume(t *testing.T) {
	for _, status := range []string{StatusApplying, StatusApplied, StatusUndoing, StatusUndone, "", "bogus"} {
		for _, action := range []Action{ActionUndo, ActionResume} {
			svc := permanentSvc(journalRow("j1", status))
			err := svc.CheckMergeAction(action, "j1", true)
			if !errors.Is(err, ErrMergePermanent) {
				t.Errorf("flipped %s of a %q journal: err = %v, want ErrMergePermanent", action, status, err)
			}
		}
	}
}

func TestCheckMergeAction_NotFlippedRefusesNothingNew(t *testing.T) {
	for _, status := range []string{StatusApplying, StatusApplied, StatusUndoing, StatusUndone} {
		for _, action := range []Action{ActionUndo, ActionResume} {
			svc := permanentSvc(journalRow("j1", status))
			if err := svc.CheckMergeAction(action, "j1", false); err != nil {
				t.Errorf("not flipped %s of a %q journal: err = %v, want nil", action, status, err)
			}
		}
	}
	// Not flipped, the journal is not even read: an unreadable or undecodable row
	// is HEAD's existing path to report.
	svc := &Service{store: journalRows{getErr: errors.New("boom")}}
	if err := svc.CheckMergeAction(ActionUndo, "j1", false); err != nil {
		t.Errorf("not flipped, unreadable store: err = %v, want nil", err)
	}
}

func TestCheckMergeAction_FlippedUndecodableJournalFailsClosed(t *testing.T) {
	rows := map[string][]byte{database.UserMergeJournalPrefix + "j1": []byte(`{"id":"j1","status":`)}
	for _, action := range []Action{ActionUndo, ActionResume} {
		err := permanentSvc(rows).CheckMergeAction(action, "j1", true)
		if !errors.Is(err, ErrMergePermanent) {
			t.Errorf("flipped %s of an undecodable journal: err = %v, want ErrMergePermanent", action, err)
		}
	}
}

func TestCheckMergeAction_FlippedMissingJournalIsNotFound(t *testing.T) {
	err := permanentSvc(nil).CheckMergeAction(ActionUndo, "nope", true)
	if !errors.Is(err, ErrJournalNotFound) || errors.Is(err, ErrMergePermanent) {
		t.Fatalf("err = %v, want ErrJournalNotFound only", err)
	}
}

func TestCheckMergeAction_FlippedUnreadableJournalIsRefusedNotUndone(t *testing.T) {
	boom := errors.New("disk")
	svc := &Service{store: journalRows{getErr: boom}}
	err := svc.CheckMergeAction(ActionUndo, "j1", true)
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the read error (a refusal)", err)
	}
}

func TestCheckMergeAction_FlippedApplyIsNotThisChecksBusiness(t *testing.T) {
	if err := permanentSvc(journalRow("j1", StatusApplied)).CheckMergeAction(ActionApply, "j1", true); err != nil {
		t.Fatalf("apply: err = %v, want nil", err)
	}
}
