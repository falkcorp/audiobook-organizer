// file: internal/database/narrator_credit_sync_test.go
// version: 1.0.0
// guid: 3345795a-09fa-4889-a511-4bb98ed412d5
// last-edited: 2026-09-22

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
