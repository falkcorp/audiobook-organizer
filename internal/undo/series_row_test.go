// file: internal/undo/series_row_test.go
// version: 1.0.0
// guid: 6d2f90c8-1e73-4a5b-b840-7f19c3e5a2d6
// last-edited: 2026-11-16

package undo

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func seriesRow(t *testing.T, ct string, id int, snap SeriesSnapshot) *database.OperationChange {
	t.Helper()
	body, err := NewSeriesSnapshotJSON(snap)
	if err != nil {
		t.Fatal(err)
	}
	c := &database.OperationChange{ID: "c", OperationID: "op", ChangeType: ct, SeriesID: &id}
	if ct == ChangeTypeSeriesCreate {
		c.NewValue = body
	} else {
		c.OldValue = body
	}
	return c
}

func TestSeriesRowsDecodeAndLabel(t *testing.T) {
	author := 3
	snap := SeriesSnapshot{ID: 7, Name: "Court of Owls", AuthorID: &author}
	for _, ct := range []string{ChangeTypeSeriesCreate, ChangeTypeSeriesRowDelete} {
		c := seriesRow(t, ct, 7, snap)
		if got := NotRestorableLabel(c); got != "" {
			t.Errorf("%s: label %q, want restorable", ct, got)
		}
		var got SeriesSnapshot
		var err error
		if ct == ChangeTypeSeriesCreate {
			got, err = DecodeSeriesCreate(c)
		} else {
			got, err = DecodeSeriesRowDelete(c)
		}
		if err != nil || got.ID != 7 || got.Name != "Court of Owls" || got.AuthorID == nil || *got.AuthorID != 3 {
			t.Errorf("%s: decode = %+v, %v", ct, got, err)
		}
	}
}

func TestSeriesRowsRefuseMalformedRows(t *testing.T) {
	good := SeriesSnapshot{ID: 7, Name: "Court of Owls"}
	cases := map[string]func(c *database.OperationChange){
		"no series id":        func(c *database.OperationChange) { c.SeriesID = nil },
		"snapshot id differs": func(c *database.OperationChange) { id := 8; c.SeriesID = &id },
		"empty name": func(c *database.OperationChange) {
			c.OldValue, c.NewValue = `{"id":7,"name":" "}`, `{"id":7,"name":" "}`
		},
		"not json": func(c *database.OperationChange) { c.OldValue, c.NewValue = "nope", "nope" },
		"zero id in the snapshot": func(c *database.OperationChange) {
			c.OldValue, c.NewValue = `{"id":0,"name":"x"}`, `{"id":0,"name":"x"}`
		},
	}
	for _, ct := range []string{ChangeTypeSeriesCreate, ChangeTypeSeriesRowDelete} {
		for name, mut := range cases {
			c := seriesRow(t, ct, 7, good)
			mut(c)
			if NotRestorableLabel(c) == "" {
				t.Errorf("%s/%s: counted restorable", ct, name)
			}
		}
	}
}

// fakeSeriesStore is a ConflictChecker over a fixed series table that can
// restore series (the capability the delete row's verdict requires).
type fakeSeriesStore struct {
	byID   map[int]*database.Series
	byName map[string]*database.Series
}

func (s fakeSeriesStore) GetOperationChanges(string) ([]*database.OperationChange, error) {
	return nil, nil
}
func (fakeSeriesStore) GetBookByID(string) (*database.Book, error)       { return nil, nil }
func (s fakeSeriesStore) GetSeriesByID(id int) (*database.Series, error) { return s.byID[id], nil }
func (s fakeSeriesStore) GetSeriesByName(name string, _ *int) (*database.Series, error) {
	return s.byName[name], nil
}
func (fakeSeriesStore) RestoreSeries(int, string, *int) error { return nil }

func TestSeriesRowDeleteVerdict(t *testing.T) {
	snap := SeriesSnapshot{ID: 7, Name: "Court of Owls 05"}
	row := seriesRow(t, ChangeTypeSeriesRowDelete, 7, snap)
	live := &database.Series{ID: 7, Name: "Court of Owls 05"}
	cases := []struct {
		name    string
		s       fakeSeriesStore
		already bool
		reason  string
	}{
		{"deleted and the name is free: restorable", fakeSeriesStore{}, false, ""},
		{"live exactly as snapshotted: already restored", fakeSeriesStore{byID: map[int]*database.Series{7: live}}, true, ""},
		{"id live as another name: refused", fakeSeriesStore{byID: map[int]*database.Series{7: {ID: 7, Name: "Other"}}}, false, ReasonSeriesRenamedSince},
		{"another series holds the name: refused", fakeSeriesStore{byName: map[string]*database.Series{"Court of Owls 05": {ID: 9, Name: "Court of Owls 05"}}}, false, ReasonSeriesNameTaken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SeriesRowDeleteVerdict(tc.s, row)
			switch {
			case tc.reason != "":
				if RefusalReason(err) != tc.reason {
					t.Fatalf("err = %v, want reason %q", err, tc.reason)
				}
			case tc.already:
				if err != ErrAlreadyRestored {
					t.Fatalf("err = %v, want already restored", err)
				}
			case err != nil:
				t.Fatalf("err = %v, want restorable", err)
			}
		})
	}
}

// A series_row_delete the plan recorded restored makes the series read as live
// to the older rows that go back into it; one recorded refused does not.
func TestRevertPlanSeriesView(t *testing.T) {
	store := fakeSeriesStore{}
	del := seriesRow(t, ChangeTypeSeriesRowDelete, 7, SeriesSnapshot{ID: 7, Name: "Court of Owls 05"})
	plan, err := PlanRevert([]*database.OperationChange{del}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := plan.SeriesView(store).GetSeriesByID(7); s != nil {
		t.Fatalf("series 7 reads live before the delete row is restored")
	}
	plan.Record(del, nil)
	if s, _ := plan.SeriesView(store).GetSeriesByID(7); s == nil {
		t.Fatalf("series 7 reads deleted after its delete row was recorded restored")
	}
	if s, _ := plan.SeriesView(store).GetSeriesByID(8); s != nil {
		t.Fatalf("series 8 reads live")
	}

	refused, _ := PlanRevert([]*database.OperationChange{del}, nil)
	refused.Record(del, refuse(ReasonSeriesNameTaken, "taken"))
	if s, _ := refused.SeriesView(store).GetSeriesByID(7); s != nil {
		t.Fatalf("series 7 reads live though its restore was refused")
	}
}
