// file: internal/books/store/pebble_store_series_restore_test.go
// version: 1.1.0
// guid: 7c1d4e83-9a26-4f05-b8e7-3d52a0c6f194
// last-edited: 2026-11-17

package store

import (
	"errors"
	"testing"
)

func newRestoreStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRestoreSeries_BringsADeletedSeriesBackUnderItsOwnID(t *testing.T) {
	s := newRestoreStore(t)
	author := 12
	made, err := s.CreateSeries("Court of Owls 05", &author)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSeries(made.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSeriesByID(made.ID); got != nil {
		t.Fatalf("series %d still live after delete", made.ID)
	}
	if err := s.RestoreSeries(made.ID, "Court of Owls 05", &author); err != nil {
		t.Fatalf("RestoreSeries: %v", err)
	}
	got, err := s.GetSeriesByID(made.ID)
	if err != nil || got == nil || got.Name != "Court of Owls 05" || got.AuthorID == nil || *got.AuthorID != author {
		t.Fatalf("GetSeriesByID = %+v, %v; want the series back under id %d", got, err, made.ID)
	}
	byName, err := s.GetSeriesByName("court of owls 05", &author)
	if err != nil || byName == nil || byName.ID != made.ID {
		t.Fatalf("GetSeriesByName = %+v, %v; want id %d (the name index is restored)", byName, err, made.ID)
	}
}

func TestRestoreSeries_AuthorlessSeries(t *testing.T) {
	s := newRestoreStore(t)
	made, _ := s.CreateSeries("Hush", nil)
	_ = s.DeleteSeries(made.ID)
	if err := s.RestoreSeries(made.ID, "Hush", nil); err != nil {
		t.Fatalf("RestoreSeries: %v", err)
	}
	if got, _ := s.GetSeriesByName("Hush", nil); got == nil || got.ID != made.ID {
		t.Fatalf("GetSeriesByName = %+v, want id %d", got, made.ID)
	}
}

// A live row that is already exactly the one being restored is a no-op, so a
// revert that stopped part way finishes on a retry.
func TestRestoreSeries_IsIdempotent(t *testing.T) {
	s := newRestoreStore(t)
	made, _ := s.CreateSeries("Hush", nil)
	if err := s.RestoreSeries(made.ID, "Hush", nil); err != nil {
		t.Fatalf("RestoreSeries on a live identical row: %v", err)
	}
}

func TestRestoreSeries_RefusesWhenAnotherSeriesHoldsTheName(t *testing.T) {
	s := newRestoreStore(t)
	made, _ := s.CreateSeries("Hush", nil)
	_ = s.DeleteSeries(made.ID)
	other, _ := s.CreateSeries("Hush", nil) // a later series took the name
	err := s.RestoreSeries(made.ID, "Hush", nil)
	if !errors.Is(err, ErrSeriesNameTaken) {
		t.Fatalf("err = %v, want ErrSeriesNameTaken", err)
	}
	if got, _ := s.GetSeriesByID(made.ID); got != nil && made.ID != other.ID {
		t.Fatalf("a refused restore wrote series %d", made.ID)
	}
	if got, _ := s.GetSeriesByName("Hush", nil); got == nil || got.ID != other.ID {
		t.Fatalf("the later series lost its name entry: %+v", got)
	}
}

func TestRestoreSeries_RefusesWhenTheIDIsLiveAsSomethingElse(t *testing.T) {
	s := newRestoreStore(t)
	made, _ := s.CreateSeries("Hush", nil)
	err := s.RestoreSeries(made.ID, "Knightfall", nil)
	if !errors.Is(err, ErrSeriesIDInUse) {
		t.Fatalf("err = %v, want ErrSeriesIDInUse", err)
	}
	if got, _ := s.GetSeriesByID(made.ID); got == nil || got.Name != "Hush" {
		t.Fatalf("the live series was changed: %+v", got)
	}
}

// A live row with the same id and name but another author is not the row being
// restored: the retry shortcut must not treat it as done.
func TestRestoreSeries_RefusesWhenTheIDIsLiveUnderAnotherAuthor(t *testing.T) {
	s := newRestoreStore(t)
	owner, other := 12, 13
	made, _ := s.CreateSeries("Hush", &owner)
	for name, author := range map[string]*int{"another author": &other, "no author": nil} {
		if err := s.RestoreSeries(made.ID, "Hush", author); !errors.Is(err, ErrSeriesIDInUse) {
			t.Errorf("%s: err = %v, want ErrSeriesIDInUse", name, err)
		}
	}
	if err := s.RestoreSeries(made.ID, "Hush", &owner); err != nil {
		t.Errorf("the identical row is a no-op, got %v", err)
	}
	if got, _ := s.GetSeriesByID(made.ID); got == nil || got.AuthorID == nil || *got.AuthorID != owner {
		t.Fatalf("the live series was changed: %+v", got)
	}
}

func TestRestoreSeries_RejectsBadInput(t *testing.T) {
	s := newRestoreStore(t)
	if err := s.RestoreSeries(0, "Hush", nil); err == nil {
		t.Error("id 0 accepted")
	}
	if err := s.RestoreSeries(5, "  ", nil); err == nil {
		t.Error("empty name accepted")
	}
}
