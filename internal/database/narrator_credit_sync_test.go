// file: internal/database/narrator_credit_sync_test.go
// version: 1.2.0
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

// createNarratedBook creates a book with the given narrator credit and,
// when authorName is set, credits that author on it.
func createNarratedBook(t *testing.T, s *PebbleStore, path, credit, authorName string) *Book {
	t.Helper()
	b := &Book{Title: "T", FilePath: path, Narrator: strp(credit)}
	if authorName != "" {
		a, err := s.CreateAuthor(authorName)
		if err != nil {
			t.Fatal(err)
		}
		b.AuthorID = &a.ID
	}
	created, err := s.CreateBook(b)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestNarratorSync_SplitsCastIntoPeople(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b := createNarratedBook(t, s, "/tmp/nsync-cast.m4b", "Dorrie Sacks, Jeff Hays & Kate Reading", "")
	want := []string{"Dorrie Sacks", "Jeff Hays", "Kate Reading"}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, want) {
		t.Fatalf("narrators = %v, want %v", got, want)
	}
	rows, _ := s.GetBookNarrators(b.ID)
	if rows[0].Role != "narrator" || rows[1].Role != "co-narrator" || rows[2].Position != 2 {
		t.Errorf("roles/positions = %+v", rows)
	}
}

func TestNarratorSync_KeepsSurnameFirstWhole(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b := createNarratedBook(t, s, "/tmp/nsync-leguin.m4b", "Le Guin, Ursula", "")
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, []string{"Le Guin, Ursula"}) {
		t.Fatalf("narrators = %v, want one person", got)
	}
}

// Owner rule 2026-09-23: the book's own author is dropped from a credit that
// also names a real narrator.
func TestNarratorSync_DropsBookAuthorFromCredit(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b := createNarratedBook(t, s, "/tmp/nsync-author.m4b", "Adrian Tchaikovsky, Ben Allen", "Adrian Tchaikovsky")
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, []string{"Ben Allen"}) {
		t.Fatalf("narrators = %v, want [Ben Allen]", got)
	}
}

// An author credited only through the book_authors join counts too.
func TestNarratorSync_DropsJoinOnlyAuthor(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b, err := s.CreateBook(&Book{Title: "T", FilePath: "/tmp/nsync-joinauthor.m4b"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAuthor("Michael Anderle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetBookAuthors(b.ID, []BookAuthor{{AuthorID: a.ID, Role: "author"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModifyBook(b.ID, func(bk *Book) error { bk.Narrator = strp("Michael Anderle, Kate Reading"); return nil }); err != nil {
		t.Fatal(err)
	}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, []string{"Kate Reading"}) {
		t.Fatalf("narrators = %v, want [Kate Reading]", got)
	}
}

// Every piece is an author: self-read or mis-tag, so the junction is left.
func TestNarratorSync_AllAuthorsLeavesJunction(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b := createNarratedBook(t, s, "/tmp/nsync-allauth.m4b", "Old Reader", "Craig Martelle")
	if _, err := s.ModifyBook(b.ID, func(bk *Book) error { bk.Narrator = strp("Craig Martelle"); return nil }); err != nil {
		t.Fatal(err)
	}
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, []string{"Old Reader"}) {
		t.Fatalf("narrators = %v, want unchanged [Old Reader]", got)
	}
}

func TestNarratorSync_JunkCreditLeavesJunction(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b := createNarratedBook(t, s, "/tmp/nsync-junk.m4b", "https://kickass.to/user/Morrogoth/", "")
	if got := narratorNamesOf(t, s, b.ID); len(got) != 0 {
		t.Fatalf("narrators = %v, want none for a URL credit", got)
	}
}

func TestNarratorSync_DropsTranslatorAndByPrefix(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b := createNarratedBook(t, s, "/tmp/nsync-roles.m4b", "By: Rick Partlow, Zachary J. Lorang - translator", "")
	if got := narratorNamesOf(t, s, b.ID); !equalStrings(got, []string{"Rick Partlow"}) {
		t.Fatalf("narrators = %v, want [Rick Partlow]", got)
	}
}

// Same people in the same order: no write, so a re-save does not churn.
func TestNarratorSync_UnchangedPeopleDoNotRewrite(t *testing.T) {
	s := newNarratorSyncTestStore(t)
	b := createNarratedBook(t, s, "/tmp/nsync-same.m4b", "Kate Reading & Michael Kramer", "")
	wrote, err := setNarratorsIfChanged(s, b.ID, mustResolve(t, s, b.ID, "Kate Reading, Michael Kramer"))
	if err != nil || wrote {
		t.Fatalf("same people: wrote=%v err=%v; want no write", wrote, err)
	}
}

func mustResolve(t *testing.T, s *PebbleStore, bookID, credit string) []BookNarrator {
	t.Helper()
	rows, _, err := resolveNarratorCredit(s, bookID, credit, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
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
