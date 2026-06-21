// file: internal/dedup/chapter_sibling_test.go
// version: 1.0.0
// guid: e3a8c7d1-4b62-4f90-9a05-7c2e1d8b6f54
// last-edited: 2026-06-21

package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func chapterBook(id, title, path string) *database.Book {
	b := primaryBook(id, title)
	b.FilePath = path
	return b
}

func TestChapterSiblings(t *testing.T) {
	base := "/lib/Brandon Sanderson/Elantris - Elantris"
	tests := []struct {
		name, a, b string
		want       bool
	}{
		{"shattered siblings same grandparent+prefix", base + "/Elantris - 1/01.mp3", base + "/Elantris - 2/01.mp3", true},
		{"same parent dir (multi-file in one folder)", base + "/01.mp3", base + "/02.mp3", false}, // caught by same-parent, not chapterSiblings
		{"different prefixes under same grandparent", "/lib/A/Book A - 1/f.mp3", "/lib/A/Book B - 2/f.mp3", false},
		{"different grandparents", "/lib/A/Elantris - 1/f.mp3", "/lib/B/Elantris - 2/f.mp3", false},
		{"not a chapter dir", "/lib/A/Elantris/f.mp3", "/lib/A/Elantris/g.mp3", false},
		{"empty path", "", base + "/Elantris - 2/01.mp3", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chapterSiblings(tt.a, tt.b); got != tt.want {
				t.Errorf("chapterSiblings(%q,%q)=%v want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSameMultiFileBook(t *testing.T) {
	base := "/lib/Author/Cage of Souls - Cage of Souls"
	// same parent dir
	a := &database.Book{FilePath: base + "/01.mp3"}
	b := &database.Book{FilePath: base + "/02.mp3"}
	if !sameMultiFileBook(a, b) {
		t.Error("same parent dir should be sameMultiFileBook")
	}
	// shattered siblings
	c := &database.Book{FilePath: base + "/Cage of Souls - 1/01.mp3"}
	d := &database.Book{FilePath: base + "/Cage of Souls - 2/01.mp3"}
	if !sameMultiFileBook(c, d) {
		t.Error("shattered siblings should be sameMultiFileBook")
	}
	// genuine cross-dir duplicate — must NOT be suppressed
	e := &database.Book{FilePath: "/lib/A/Mistborn/Mistborn.m4b"}
	f := &database.Book{FilePath: "/lib/B/Mistborn (2)/Mistborn.m4b"}
	if sameMultiFileBook(e, f) {
		t.Error("genuine cross-dir duplicate must NOT be sameMultiFileBook")
	}
}

func TestPairEligibility_SuppressesChapterSiblings(t *testing.T) {
	base := "/lib/Author/Cage of Souls - Cage of Souls"
	a := &database.Book{ID: "A", Title: "Cage of Souls", FilePath: base + "/Cage of Souls - 1/01.mp3"}
	b := &database.Book{ID: "B", Title: "Cage of Souls", FilePath: base + "/Cage of Souls - 2/01.mp3"}
	ok, sup := PairEligibility(a, b)
	if ok {
		t.Fatalf("chapter siblings should be ineligible, got eligible")
	}
	found := false
	for _, s := range sup {
		if s == "same_dir_multi_file" {
			found = true
		}
	}
	if !found {
		t.Errorf("suppressors = %v, want same_dir_multi_file", sup)
	}
}

// Emitter must NOT cross-pair chapter siblings of one shattered book.
func TestExactEmitters_SuppressChapterSiblings(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false
	base := "/lib/Brandon Sanderson/Elantris - Elantris"
	a := chapterBook("A", "Elantris", base+"/Elantris - 1/01.mp3")
	b := chapterBook("B", "Elantris", base+"/Elantris - 2/01.mp3")
	byAuthor := []database.Book{*a, *b}
	wireExactTitleOnly(mock, map[string]*database.Book{"A": a, "B": b}, byAuthor)

	for _, id := range []string{"A", "B"} {
		if _, err := engine.CheckBook(context.Background(), id); err != nil {
			t.Fatalf("CheckBook(%s): %v", id, err)
		}
	}
	if cands := pendingCandidates(t, es); len(cands) != 0 {
		t.Errorf("chapter siblings produced %d candidates, want 0: %+v", len(cands), cands)
	}
}

// Emitter MUST still emit for a genuine cross-directory same-title duplicate.
func TestExactEmitters_EmitCrossDirDuplicate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false
	x := chapterBook("X", "Mistborn", "/lib/A/Mistborn/Mistborn.m4b")
	y := chapterBook("Y", "Mistborn", "/lib/B/Mistborn (2)/Mistborn.m4b")
	byAuthor := []database.Book{*x, *y}
	wireExactTitleOnly(mock, map[string]*database.Book{"X": x, "Y": y}, byAuthor)

	for _, id := range []string{"X", "Y"} {
		if _, err := engine.CheckBook(context.Background(), id); err != nil {
			t.Fatalf("CheckBook(%s): %v", id, err)
		}
	}
	cands := pendingCandidates(t, es)
	if len(cands) == 0 {
		t.Fatalf("genuine cross-dir duplicate produced 0 candidates, want ≥1")
	}
}

// Backstop: a suppressible pending candidate (chapter siblings) that already exists
// must be DELETED by the unified pass, not merely skipped.
func TestUnifiedPass_DeletesSuppressedChapterSiblingCandidate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	base := "/lib/Author/Elantris - Elantris"
	a := chapterBook("A", "Elantris", base+"/Elantris - 1/01.mp3")
	b := chapterBook("B", "Elantris", base+"/Elantris - 2/01.mp3")
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return map[string]*database.Book{"A": a, "B": b}[id], nil
	}
	mock.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return nil, nil }

	// Seed a pending candidate for the sibling pair (upsert does not apply the
	// emit-time same-dir guard, so it inserts — exactly the residual we must purge).
	if err := engine.upsertExactCandidate(a, b, "exact", 1.0); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	if len(pendingCandidates(t, es)) != 1 {
		t.Fatalf("setup: expected 1 seeded candidate")
	}

	if err := engine.runUnifiedScoringForBook(context.Background(), a, "Author"); err != nil {
		t.Fatalf("runUnifiedScoringForBook: %v", err)
	}
	if cands := pendingCandidates(t, es); len(cands) != 0 {
		t.Errorf("backstop should have deleted the suppressed candidate, got %d: %+v", len(cands), cands)
	}
}
