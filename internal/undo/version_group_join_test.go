// file: internal/undo/version_group_join_test.go
// version: 1.0.1
// guid: e2a64b7d-0c19-4f85-8d3b-71f5c9a0e342
// last-edited: 2026-11-09

package undo

import (
	"errors"
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func joinChange(t *testing.T, old, now VersionGroupState) *database.OperationChange {
	t.Helper()
	o, err := old.Encode()
	if err != nil {
		t.Fatal(err)
	}
	n, err := now.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &database.OperationChange{ID: "chg-join", OperationID: "op-1", BookID: "01JBATMAN",
		ChangeType: ChangeTypeRepairVersionGroupJoin, FieldName: "version_group_id", OldValue: o, NewValue: n}
}

func TestDecodeVersionGroupJoin(t *testing.T) {
	minted := VersionGroupState{Group: ptr("vg-gotham"), Primary: ptr(true)}
	for _, tc := range []struct {
		name     string
		old, now VersionGroupState
		ok       bool
	}{
		{"never grouped", VersionGroupState{}, minted, true},
		{"empty group string and false flag", VersionGroupState{Group: ptr(""), Primary: ptr(false)}, minted, true},
		{"old group set", VersionGroupState{Group: ptr("vg-other")}, minted, false},
		{"new group empty", VersionGroupState{}, VersionGroupState{Group: ptr(""), Primary: ptr(true)}, false},
		{"new flag unset", VersionGroupState{}, VersionGroupState{Group: ptr("vg-gotham")}, false},
		{"new flag false", VersionGroupState{}, VersionGroupState{Group: ptr("vg-gotham"), Primary: ptr(false)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := DecodeVersionGroupJoin(joinChange(t, tc.old, tc.now))
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	t.Run("garbage", func(t *testing.T) {
		c := joinChange(t, VersionGroupState{}, minted)
		c.NewValue = "{"
		if _, _, err := DecodeVersionGroupJoin(c); err == nil {
			t.Fatal("an unparsable value must not decode")
		}
	})
}

func TestVersionGroupStatePointerShapeSurvivesApply(t *testing.T) {
	for _, s := range []VersionGroupState{{}, {Group: ptr("")}, {Primary: ptr(false)}, {Group: ptr("vg-gotham"), Primary: ptr(true)}} {
		b := &database.Book{VersionGroupID: ptr("x"), IsPrimaryVersion: ptr(true)}
		s.Apply(b)
		got := StateOfBook(b)
		if (got.Group == nil) != (s.Group == nil) || (got.Primary == nil) != (s.Primary == nil) || !got.Same(s) {
			t.Fatalf("state %+v came back as %+v", s, got)
		}
	}
	if (VersionGroupState{Primary: ptr(false)}).Same(VersionGroupState{}) {
		t.Fatal("an explicit false is not an unset flag")
	}
}

func TestCheckVersionGroupJoinCurrent(t *testing.T) {
	old := VersionGroupState{}
	now := VersionGroupState{Group: ptr("vg-gotham"), Primary: ptr(true)}
	c := joinChange(t, old, now)
	joined := func() *database.Book {
		return &database.Book{ID: "01JBATMAN", VersionGroupID: ptr("vg-gotham"), IsPrimaryVersion: ptr(true)}
	}
	member := func(id string, deleted bool) database.Book {
		b := database.Book{ID: id, VersionGroupID: ptr("vg-gotham"), IsPrimaryVersion: ptr(false)}
		if deleted {
			b.MarkedForDeletion = ptr(true)
		}
		return b
	}

	for _, tc := range []struct {
		name    string
		book    *database.Book
		members []database.Book
		ignore  map[string]bool
		want    string // "" ok, "restored", or a refusal reason
	}{
		{"as the join left it, alone in the group", joined(), []database.Book{*joined()}, nil, ""},
		{"a hidden member is not a member", joined(), []database.Book{*joined(), member("01JROBIN", true)}, nil, ""},
		{"a member this revert hides first is ignored", joined(), []database.Book{*joined(), member("01JROBIN", false)}, map[string]bool{"01JROBIN": true}, ""},
		{"another live member joined since", joined(), []database.Book{*joined(), member("01JROBIN", false)}, nil, ReasonChangedSince},
		{"never written: no group, flag unset", &database.Book{ID: "01JBATMAN"}, nil, nil, "restored"},
		{"no group but the flag was changed", &database.Book{ID: "01JBATMAN", IsPrimaryVersion: ptr(true)}, nil, nil, ReasonChangedSince},
		{"explicit false is not the unset flag the join found", &database.Book{ID: "01JBATMAN", IsPrimaryVersion: ptr(false)}, nil, nil, ReasonChangedSince},
		{"moved to another group", &database.Book{ID: "01JBATMAN", VersionGroupID: ptr("vg-elsewhere"), IsPrimaryVersion: ptr(true)}, nil, nil, ReasonChangedSince},
		{"demoted since", &database.Book{ID: "01JBATMAN", VersionGroupID: ptr("vg-gotham"), IsPrimaryVersion: ptr(false)}, nil, nil, ReasonChangedSince},
		{"flag unset since", &database.Book{ID: "01JBATMAN", VersionGroupID: ptr("vg-gotham")}, nil, nil, ReasonChangedSince},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckVersionGroupJoinCurrent(tc.book, tc.members, tc.ignore, c)
			switch tc.want {
			case "":
				if err != nil {
					t.Fatalf("want restorable, got %v", err)
				}
			case "restored":
				if !errors.Is(err, ErrAlreadyRestored) {
					t.Fatalf("want ErrAlreadyRestored, got %v", err)
				}
			default:
				if got := RefusalReason(err); got != tc.want {
					t.Fatalf("reason %q (%v), want %q", got, err, tc.want)
				}
			}
		})
	}
	t.Run("malformed row", func(t *testing.T) {
		bad := joinChange(t, VersionGroupState{Group: ptr("vg-x")}, now)
		if got := RefusalReason(CheckVersionGroupJoinCurrent(joined(), nil, nil, bad)); got != ReasonOldValueUnparsable {
			t.Fatalf("reason %q, want %q", got, ReasonOldValueUnparsable)
		}
	})
}

// TestCheckRepairBookCreate_VersionGroupAndAdoption: a book created as a
// non-primary member of a group is restorable only while it still is exactly
// that; a book a later operation adopted accepts the first operation's rows.
func TestCheckRepairBookCreate_VersionGroupAndAdoption(t *testing.T) {
	setup := func(t *testing.T, group string, primary *bool) (*database.PebbleStore, *database.OperationChange) {
		t.Helper()
		store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		store.WaitForWarmup()
		b := &database.Book{Title: "Hush", FilePath: "/lib/Hush"}
		if group != "" {
			b.VersionGroupID, b.IsPrimaryVersion = &group, primary
		}
		b, err = store.CreateBook(b)
		if err != nil {
			t.Fatal(err)
		}
		return store, &database.OperationChange{ID: "chg-1", OperationID: "op-1", BookID: b.ID,
			ChangeType: ChangeTypeRepairBookCreate, FieldName: "book",
			NewValue: mustEncode(t, RepairBookCreateValue{ID: b.ID, Title: "Hush", FilePath: "/lib/Hush", VersionGroupID: "vg-gotham"})}
	}
	t.Run("joined as an explicit non-primary member", func(t *testing.T) {
		store, c := setup(t, "vg-gotham", ptr(false))
		if err := CheckRepairBookCreate(store, c); err != nil {
			t.Fatalf("want restorable, got %v", err)
		}
	})
	for _, tc := range []struct {
		name    string
		group   string
		primary *bool
	}{
		{"crowned since", "vg-gotham", ptr(true)},
		{"flag unset since", "vg-gotham", nil},
		{"moved to another group", "vg-elsewhere", ptr(false)},
		{"left the group", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, c := setup(t, tc.group, tc.primary)
			if got := RefusalReason(CheckRepairBookCreate(store, c)); got != ReasonChangedSince {
				t.Fatalf("reason %q, want %q", got, ReasonChangedSince)
			}
		})
	}
	t.Run("a value with no group refuses a book that joined one", func(t *testing.T) {
		store, c := setup(t, "vg-gotham", ptr(false))
		c.NewValue = mustEncode(t, RepairBookCreateValue{ID: c.BookID, Title: "Hush", FilePath: "/lib/Hush"})
		if got := RefusalReason(CheckRepairBookCreate(store, c)); got != ReasonChangedSince {
			t.Fatalf("reason %q, want %q", got, ReasonChangedSince)
		}
	})
	t.Run("an adopted book's rows come from the first operation's journal", func(t *testing.T) {
		store, c := setup(t, "vg-gotham", ptr(false))
		row := &database.BookFile{ID: "01JROW", BookID: c.BookID, FilePath: "/lib/Hush/01.mp3"}
		if err := store.CreateBookFile(row); err != nil {
			t.Fatal(err)
		}
		journal := func(op, typ, field, val string) {
			t.Helper()
			if err := database.RecordOperationChange(store, &database.OperationChange{ID: op + typ + field, OperationID: op, BookID: c.BookID,
				ChangeType: typ, FieldName: field, NewValue: val}); err != nil {
				t.Fatal(err)
			}
		}
		// op-0 created the book and the row; op-1 adopted it.
		journal("op-0", ChangeTypeBookFileCreate, "book_file:01JROW", "/lib/Hush/01.mp3")
		journal("op-0", ChangeTypeRepairBookCreate, "book", mustEncode(t, RepairBookCreateValue{ID: c.BookID, Title: "Hush", FilePath: "/lib/Hush", VersionGroupID: "vg-gotham"}))
		if got := RefusalReason(CheckRepairBookCreate(store, c)); got != ReasonChangedSince {
			t.Fatalf("without AdoptedFrom the row is a stranger's: reason %q, want %q", got, ReasonChangedSince)
		}
		c.NewValue = mustEncode(t, RepairBookCreateValue{ID: c.BookID, Title: "Hush", FilePath: "/lib/Hush", VersionGroupID: "vg-gotham", AdoptedFrom: "op-0"})
		if err := CheckRepairBookCreate(store, c); err != nil {
			t.Fatalf("an adopted book accepts the earlier operation's rows, got %v", err)
		}
	})
}

func TestRepairBookCreateValueSameBook(t *testing.T) {
	a := RepairBookCreateValue{ID: "1", Title: "Hush", AuthorID: ptr(7), FilePath: "/p", VersionGroupID: "g"}
	if !a.SameBook(RepairBookCreateValue{ID: "1", Title: "Hush", AuthorID: ptr(7), FilePath: "/p", VersionGroupID: "g", AdoptedFrom: "op"}) {
		t.Fatal("AdoptedFrom is how it was journaled, not what it is")
	}
	for _, o := range []RepairBookCreateValue{
		{ID: "2", Title: "Hush", AuthorID: ptr(7), FilePath: "/p", VersionGroupID: "g"},
		{ID: "1", Title: "Hush!", AuthorID: ptr(7), FilePath: "/p", VersionGroupID: "g"},
		{ID: "1", Title: "Hush", AuthorID: ptr(8), FilePath: "/p", VersionGroupID: "g"},
		{ID: "1", Title: "Hush", FilePath: "/p", VersionGroupID: "g"},
		{ID: "1", Title: "Hush", AuthorID: ptr(7), FilePath: "/q", VersionGroupID: "g"},
		{ID: "1", Title: "Hush", AuthorID: ptr(7), FilePath: "/p", VersionGroupID: "h"},
		{ID: "1", Title: "Hush", AuthorID: ptr(7), FilePath: "/p"},
	} {
		if a.SameBook(o) {
			t.Fatalf("%+v must not equal %+v", o, a)
		}
	}
}
