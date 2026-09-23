// file: internal/database/narrator_credit_sync_test.go
// version: 1.1.1
// guid: 3345795a-09fa-4889-a511-4bb98ed412d5
// last-edited: 2026-09-23

package database

import "testing"

func narratorNamesOf(t *testing.T, s *PebbleStore, bookID string) []string {
	t.Helper()
	rows, err := s.GetBookNarrators(bookID)
	if err != nil {
		t.Fatalf("GetBookNarrators: %v", err)
	}
	var names []string
	for _, r := range rows {
		n, err := s.GetNarratorByID(r.NarratorID)
		if err != nil || n == nil {
			t.Fatalf("GetNarratorByID(%d): %v", r.NarratorID, err)
		}
		names = append(names, n.Name)
	}
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSyncBookNarratorsFromCredit_SplitsCastIntoPeople(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	wrote, err := SyncBookNarratorsFromCredit(s, "book1", "Dorrie Sacks, Jeff Hays & Kate Reading")
	if err != nil || !wrote {
		t.Fatalf("SyncBookNarratorsFromCredit = %v, %v; want wrote", wrote, err)
	}
	want := []string{"Dorrie Sacks", "Jeff Hays", "Kate Reading"}
	if got := narratorNamesOf(t, s, "book1"); !equalStrings(got, want) {
		t.Fatalf("narrators = %v, want %v", got, want)
	}
	rows, _ := s.GetBookNarrators("book1")
	if rows[0].Role != "narrator" || rows[1].Role != "co-narrator" || rows[2].Position != 2 {
		t.Errorf("roles/positions = %+v", rows)
	}
}

func TestSyncBookNarratorsFromCredit_KeepsSurnameFirstWhole(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	if _, err := SyncBookNarratorsFromCredit(s, "book1", "Le Guin, Ursula"); err != nil {
		t.Fatal(err)
	}
	if got := narratorNamesOf(t, s, "book1"); !equalStrings(got, []string{"Le Guin, Ursula"}) {
		t.Fatalf("narrators = %v, want one person", got)
	}
}

// Replace, not merge: a narrator the new credit drops leaves the junction.
func TestSyncBookNarratorsFromCredit_ReplacesExisting(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	if _, err := SyncBookNarratorsFromCredit(s, "book1", "Old Reader, Kate Reading"); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncBookNarratorsFromCredit(s, "book1", "Kate Reading, Michael Kramer"); err != nil {
		t.Fatal(err)
	}
	want := []string{"Kate Reading", "Michael Kramer"}
	if got := narratorNamesOf(t, s, "book1"); !equalStrings(got, want) {
		t.Fatalf("narrators = %v, want %v", got, want)
	}
}

func TestSyncBookNarratorsFromCredit_EmptyCreditLeavesJunction(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	if _, err := SyncBookNarratorsFromCredit(s, "book1", "Kate Reading"); err != nil {
		t.Fatal(err)
	}
	wrote, err := SyncBookNarratorsFromCredit(s, "book1", "   ")
	if err != nil || wrote {
		t.Fatalf("empty credit: wrote=%v err=%v; want no-op", wrote, err)
	}
	if got := narratorNamesOf(t, s, "book1"); !equalStrings(got, []string{"Kate Reading"}) {
		t.Fatalf("narrators = %v, want unchanged", got)
	}
}

func TestSyncBookNarratorsFromCredit_UnchangedCreditDoesNotWrite(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	if _, err := SyncBookNarratorsFromCredit(s, "book1", "Kate Reading & Michael Kramer"); err != nil {
		t.Fatal(err)
	}
	wrote, err := SyncBookNarratorsFromCredit(s, "book1", "Kate Reading, Michael Kramer")
	if err != nil || wrote {
		t.Fatalf("same people re-synced: wrote=%v err=%v; want no write", wrote, err)
	}
}

func newNarratorSyncTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---- store-level sync: every book write that changes Narrator ----

func TestCreateBook_SyncsNarratorJunction(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/tmp/nsync-create.m4b", Narrator: strp("Kate Reading, Michael Kramer")})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Kate Reading", "Michael Kramer"}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, want) {
		t.Fatalf("narrators = %v, want %v", got, want)
	}
}

func TestModifyBook_NarratorChangeReplacesJunction(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/tmp/nsync-modify.m4b", Narrator: strp("Old Reader")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModifyBook(b.ID, func(bk *Book) error { bk.Narrator = strp("Dorrie Sacks & Jeff Hays"); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"Dorrie Sacks", "Jeff Hays"}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, want) {
		t.Fatalf("narrators = %v, want %v", got, want)
	}
}

func TestUpdateBook_NarratorChangeReplacesJunction(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/tmp/nsync-update.m4b", Narrator: strp("Old Reader")})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Narrator = strp("Kate Reading; Michael Kramer")
	if _, err := s.UpdateBook(b.ID, fresh); err != nil {
		t.Fatal(err)
	}
	want := []string{"Kate Reading", "Michael Kramer"}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, want) {
		t.Fatalf("narrators = %v, want %v", got, want)
	}
}

// A write that leaves Narrator alone must not touch a hand-curated junction.
func TestModifyBook_UnchangedNarratorLeavesCuratedJunction(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/tmp/nsync-curated.m4b", Narrator: strp("Kate Reading")})
	if err != nil {
		t.Fatal(err)
	}
	extra, err := s.CreateNarrator("Hand Added")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := s.GetBookNarrators(b.ID)
	rows = append(rows, BookNarrator{NarratorID: extra.ID, Role: "co-narrator", Position: 1})
	if err := s.SetBookNarrators(b.ID, rows); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModifyBook(b.ID, func(bk *Book) error { bk.Title = "Retitled"; return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"Kate Reading", "Hand Added"}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, want) {
		t.Fatalf("narrators = %v, want %v (untouched)", got, want)
	}
}

// Clearing the column is not "no narrators": the junction stays.
func TestModifyBook_ClearedNarratorLeavesJunction(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/tmp/nsync-clear.m4b", Narrator: strp("Kate Reading")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModifyBook(b.ID, func(bk *Book) error { bk.Narrator = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, []string{"Kate Reading"}) {
		t.Fatalf("narrators = %v, want unchanged", got)
	}
}

// A write that lands after this sync resolved its credit, but before it takes
// the stripe, owns the junction: the stale sync must not overwrite it.
func TestNarratorSync_LaterWriteWinsTheRace(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/tmp/nsync-race.m4b", Narrator: strp("Old Reader")})
	if err != nil {
		t.Fatal(err)
	}
	fired := false
	narratorSyncAfterResolveHook = func(bookID string) {
		if fired || bookID != b.ID {
			return
		}
		fired = true
		if _, err := s.ModifyBook(b.ID, func(bk *Book) error { bk.Narrator = strp("Later Writer"); return nil }); err != nil {
			t.Errorf("racing write: %v", err)
		}
	}
	t.Cleanup(func() { narratorSyncAfterResolveHook = nil })

	if _, err := s.ModifyBook(b.ID, func(bk *Book) error { bk.Narrator = strp("Stale Credit"); return nil }); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("race hook never ran")
	}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, []string{"Later Writer"}) {
		t.Fatalf("narrators = %v, want the later write's [Later Writer]", got)
	}
}
