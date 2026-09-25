// file: internal/playlist/evaluator_test.go
// version: 1.0.1
// guid: 9d3e5f2a-7b4a-4a70-b8c5-3d7e0f1b9a69

package playlist

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// buildEvalFixture seeds a PebbleStore with three books and a
// Bleve index with matching docs. Returns the store, index, and
// book IDs in insertion order.
func buildEvalFixture(t *testing.T) (*database.PebbleStore, *search.BleveIndex, []string) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	// Fixture indexes every book itself, so it is complete by construction.
	if err := idx.MarkRebuilt(); err != nil {
		t.Fatalf("MarkRebuilt: %v", err)
	}

	type seed struct {
		id     string
		title  string
		author string
		series string
		year   int
		format string
	}
	rows := []seed{
		{"b1", "The Way of Kings", "Brandon Sanderson", "Stormlight Archive", 2010, "m4b"},
		{"b2", "Words of Radiance", "Brandon Sanderson", "Stormlight Archive", 2014, "m4b"},
		{"b3", "The Fifth Season", "N. K. Jemisin", "Broken Earth", 2015, "mp3"},
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		year := r.year
		_, err := store.CreateBook(&database.Book{
			ID: r.id, Title: r.title, FilePath: "/tmp/" + r.id, Format: r.format, PrintYear: &year,
		})
		if err != nil {
			t.Fatalf("create book %s: %v", r.id, err)
		}
		if err := idx.IndexBook(search.BookDocument{
			BookID: r.id, Title: r.title, Author: r.author,
			Series: r.series, Year: r.year, Format: r.format,
		}); err != nil {
			t.Fatalf("index %s: %v", r.id, err)
		}
		ids = append(ids, r.id)
	}
	return store, idx, ids
}

func TestEvaluate_BasicAuthorQuery(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	got, err := EvaluateSmartPlaylist(store, idx, "author:sanderson", "", 0, "_local")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d, want 2 (both Sanderson)", len(got))
	}
}

func TestEvaluate_NilIndexErrors(t *testing.T) {
	store, _, _ := buildEvalFixture(t)

	_, err := EvaluateSmartPlaylist(store, nil, "author:sanderson", "", 0, "_local")
	if err != ErrSearchIndexUnavailable {
		t.Errorf("want ErrSearchIndexUnavailable, got: %v", err)
	}
}

func TestEvaluate_EmptyQueryErrors(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	if _, err := EvaluateSmartPlaylist(store, idx, "   ", "", 0, "_local"); err == nil {
		t.Error("empty query should error")
	}
}

func TestEvaluate_LimitTruncates(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	got, err := EvaluateSmartPlaylist(store, idx, "format:m4b", "", 1, "_local")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("limit=1 returned %d ids", len(got))
	}
}

func TestEvaluate_SortByYearDesc(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	sortJSON := `[{"field":"year","direction":"desc"}]`
	got, err := EvaluateSmartPlaylist(store, idx, "author:sanderson", sortJSON, 0, "_local")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// Sanderson books: b2 (2014) should come before b1 (2010) on desc.
	if len(got) != 2 || got[0] != "b2" || got[1] != "b1" {
		t.Errorf("sorted = %v, want [b2 b1]", got)
	}
}

func TestEvaluate_SortByTitleAsc(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	sortJSON := `[{"field":"title","direction":"asc"}]`
	got, err := EvaluateSmartPlaylist(store, idx, "*", sortJSON, 0, "_local")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// Titles sorted asc: "The Fifth Season" < "The Way of Kings" < "Words of Radiance"
	if len(got) != 3 || got[0] != "b3" || got[1] != "b1" || got[2] != "b2" {
		t.Errorf("sorted = %v, want [b3 b1 b2]", got)
	}
}

func TestEvaluate_PerUserFilterReadStatus(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	// Mark b1 finished for user "alice".
	_ = store.SetUserBookState(&database.UserBookState{
		UserID: "alice", BookID: "b1", Status: database.UserBookStatusFinished,
		StatusManual: true, LastActivityAt: time.Now(),
	})

	// Query targets all Sanderson, but filter to finished only.
	got, err := EvaluateSmartPlaylist(store, idx, "author:sanderson read_status:finished", "", 0, "alice")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 1 || got[0] != "b1" {
		t.Errorf("want [b1], got %v", got)
	}

	// Different user — no state, so no match.
	got, err = EvaluateSmartPlaylist(store, idx, "author:sanderson read_status:finished", "", 0, "bob")
	if err != nil {
		t.Fatalf("evaluate (bob): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("bob should see no finished books, got %v", got)
	}
}

func TestEvaluate_PerUserFilterNegated(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	_ = store.SetUserBookState(&database.UserBookState{
		UserID: "alice", BookID: "b1", Status: database.UserBookStatusFinished,
		StatusManual: true, LastActivityAt: time.Now(),
	})

	// NOT read_status:finished should exclude b1 but include b2 (unstarted).
	got, err := EvaluateSmartPlaylist(store, idx, "author:sanderson -read_status:finished", "", 0, "alice")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 1 || got[0] != "b2" {
		t.Errorf("want [b2], got %v", got)
	}
}

func TestEvaluate_PerUserFilterProgressRange(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	_ = store.SetUserBookState(&database.UserBookState{
		UserID: "alice", BookID: "b1", Status: database.UserBookStatusInProgress,
		ProgressPct: 50, LastActivityAt: time.Now(),
	})
	_ = store.SetUserBookState(&database.UserBookState{
		UserID: "alice", BookID: "b2", Status: database.UserBookStatusInProgress,
		ProgressPct: 10, LastActivityAt: time.Now(),
	})

	got, err := EvaluateSmartPlaylist(store, idx, "author:sanderson progress_pct:>25", "", 0, "alice")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 1 || got[0] != "b1" {
		t.Errorf("want [b1] with >25%% progress, got %v", got)
	}
}

func TestEvaluate_YearRangeQuery(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	got, err := EvaluateSmartPlaylist(store, idx, "year:[2013 TO 2020]", "", 0, "_local")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// b2 (2014) + b3 (2015) in range, b1 (2010) out.
	if len(got) != 2 {
		t.Errorf("year range got %d, want 2", len(got))
	}
	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	if !seen["b2"] || !seen["b3"] || seen["b1"] {
		t.Errorf("year range picked wrong books: %v", got)
	}
}

func TestEvaluate_MalformedQueryErrors(t *testing.T) {
	store, idx, _ := buildEvalFixture(t)

	// Unterminated quote — parser should reject.
	if _, err := EvaluateSmartPlaylist(store, idx, `title:"unterminated`, "", 0, "_local"); err == nil {
		t.Error("expected parse error on unterminated quote")
	}
}
