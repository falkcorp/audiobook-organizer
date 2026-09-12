// file: internal/database/pebble_store_series_rename_if_test.go
// version: 1.0.0
// guid: 7c3e9b14-52d8-4a6f-9e01-b8d4f2a6c935
// last-edited: 2026-09-12

package database

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newRenameIfStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRenameSeriesIf_RenamesAndMovesTheNameIndex(t *testing.T) {
	s := newRenameIfStore(t)
	ser, err := s.CreateSeries("New Name", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameSeriesIf(ser.ID, "New Name", "Old Name"); err != nil {
		t.Fatalf("RenameSeriesIf: %v", err)
	}
	if got, _ := s.GetSeriesByID(ser.ID); got == nil || got.Name != "Old Name" {
		t.Fatalf("series = %+v, want Old Name", got)
	}
	if got, _ := s.GetSeriesByName("Old Name", nil); got == nil || got.ID != ser.ID {
		t.Errorf("GetSeriesByName(Old Name) = %+v, want series %d", got, ser.ID)
	}
	if got, _ := s.GetSeriesByName("New Name", nil); got != nil {
		t.Errorf("GetSeriesByName(New Name) = %+v, want nil", got)
	}
}

// Every refusal writes nothing: the row and both name-index keys are as before.
func TestRenameSeriesIf_Refusals(t *testing.T) {
	three := 3
	cases := []struct {
		name    string
		setup   func(t *testing.T, s *PebbleStore) (id int)
		expect  string
		wantErr error
	}{
		{"renamed since", func(t *testing.T, s *PebbleStore) int {
			ser, _ := s.CreateSeries("Third Name", nil)
			return ser.ID
		}, "New Name", ErrRenameSeriesRenamedSince},
		{"name taken, case-insensitive", func(t *testing.T, s *PebbleStore) int {
			ser, _ := s.CreateSeries("New Name", nil)
			if _, err := s.CreateSeries("old name", nil); err != nil {
				t.Fatal(err)
			}
			return ser.ID
		}, "New Name", ErrRenameSeriesNameTaken},
		{"not found", func(*testing.T, *PebbleStore) int { return 999 }, "New Name", ErrRenameSeriesNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newRenameIfStore(t)
			id := tc.setup(t, s)
			before, _ := s.GetSeriesByID(id)
			holder, _ := s.GetSeriesByName("Old Name", nil)

			err := s.RenameSeriesIf(id, tc.expect, "Old Name")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if after, _ := s.GetSeriesByID(id); fmt.Sprint(after) != fmt.Sprint(before) {
				t.Errorf("series changed on refusal: %+v -> %+v", before, after)
			}
			if after, _ := s.GetSeriesByName("Old Name", nil); fmt.Sprint(after) != fmt.Sprint(holder) {
				t.Errorf("Old Name index changed on refusal: %+v -> %+v", holder, after)
			}
		})
	}

	// The name index is per author: the same name under another author is free.
	t.Run("same name under another author", func(t *testing.T) {
		s := newRenameIfStore(t)
		ser, _ := s.CreateSeries("New Name", nil)
		if _, err := s.CreateSeries("Old Name", &three); err != nil {
			t.Fatal(err)
		}
		if err := s.RenameSeriesIf(ser.ID, "New Name", "Old Name"); err != nil {
			t.Fatalf("RenameSeriesIf: %v", err)
		}
	})
}

// A CreateSeries of the old name racing the rename-back never leaves two
// series answering to it: both hold nameIdx.series, so either the create finds
// the renamed series or the rename finds the name taken.
func TestRenameSeriesIf_RaceWithCreateSeriesNeverDuplicates(t *testing.T) {
	s := newRenameIfStore(t)
	for i := 0; i < 40; i++ {
		oldName, newName := fmt.Sprintf("Old %d", i), fmt.Sprintf("New %d", i)
		ser, err := s.CreateSeries(newName, nil)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.RenameSeriesIf(ser.ID, newName, oldName)
		}()
		go func() {
			defer wg.Done()
			_, _ = s.CreateSeries(oldName, nil)
		}()
		wg.Wait()

		all, err := s.GetAllSeries()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, a := range all {
			if strings.EqualFold(a.Name, oldName) {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("iteration %d: %d series named %q, want exactly 1", i, n, oldName)
		}
	}
}
