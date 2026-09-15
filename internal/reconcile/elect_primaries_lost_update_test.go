// file: internal/reconcile/elect_primaries_lost_update_test.go
// version: 1.0.0
// guid: e7852bba-e5a0-4efe-980b-3c35ba39fb0d
// last-edited: 2026-09-14

package reconcile

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// electLostUpdateStore wraps electFakeStore with the OTHER writer: it commits
// Duration=4242 on the stored row right after every raw GetBookByID (which
// here returns a copy, as the real store does) and right before every
// ModifyBook takes the row lock. A site that reads the row and writes the
// WHOLE row back reverts it; ModifyBook keeps it.
type electLostUpdateStore struct {
	*electFakeStore
}

func (s electLostUpdateStore) landDuration(id string) {
	s.lock()
	defer s.unlock()
	if b := s.byID[id]; b != nil {
		d := 4242
		b.Duration = &d
	}
}

func (s electLostUpdateStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.electFakeStore.GetBookByID(id)
	if err != nil || b == nil {
		return b, err
	}
	cp := *b
	s.landDuration(id)
	return &cp, nil
}

func (s electLostUpdateStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.landDuration(id)
	return s.electFakeStore.ModifyBook(id, fn)
}

// TestElectMissingPrimaries_DoesNotRevertConcurrentColumns pins the
// lost-update fix on the election write (audit A1#15): it must set only
// IsPrimaryVersion, so a Duration another writer commits between its read and
// its write survives. Against the old GetBookByID -> UpdateBook(whole row) it
// fails with "Duration reverted".
func TestElectMissingPrimaries_DoesNotRevertConcurrentColumns(t *testing.T) {
	inner := newElectFakeStore()
	base := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	inner.addElectBook("solo", "Solo", "vg-solo", false, base)
	store := electLostUpdateStore{inner}

	res, err := ElectMissingPrimaries(store, false)
	if err != nil {
		t.Fatalf("ElectMissingPrimaries: %v", err)
	}
	if res.Elected != 1 || res.Errors != 0 {
		t.Fatalf("Elected=%d Errors=%d, want 1/0", res.Elected, res.Errors)
	}
	inner.lock()
	got := inner.updated["solo"]
	inner.unlock()
	if got == nil || got.IsPrimaryVersion == nil || !*got.IsPrimaryVersion {
		t.Fatalf("winner not written as primary: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the election write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}
