// file: internal/metafetch/isbn_cursor_test.go
// version: 1.0.0
// guid: 5c1e7a02-9b64-4d38-8a1f-2e6c4b0d9f77
// last-edited: 2026-09-10

package metafetch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// cursorFixture builds a MockStore over an ordered slice of books that behaves
// like the production GetAllBooksFullFrom key-seek cursor: it returns books
// strictly after afterID (or from the top when afterID==""), up to limit, and
// returns nil for a stale/unknown afterID (ending iteration, as prod does). The
// operation-state blob is round-tripped through a real map so a persisted cursor
// is actually observable on the next read.
func cursorFixture(t *testing.T, books []database.Book) (*database.MockStore, map[string][]byte, *[]string) {
	t.Helper()
	byID := make(map[string]database.Book, len(books))
	for _, b := range books {
		byID[b.ID] = b
	}
	state := map[string][]byte{}
	var afterIDsSeen []string

	store := &database.MockStore{
		GetAllBooksFullFromFunc: func(afterID string, limit int) ([]database.Book, error) {
			afterIDsSeen = append(afterIDsSeen, afterID)
			start := 0
			if afterID != "" {
				start = len(books) // unknown cursor => end iteration
				for i, b := range books {
					if b.ID == afterID {
						start = i + 1
						break
					}
				}
			}
			if start >= len(books) {
				return nil, nil
			}
			end := len(books)
			if limit > 0 && start+limit < end {
				end = start + limit
			}
			out := make([]database.Book, end-start)
			copy(out, books[start:end])
			return out, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := byID[id]
			if !ok {
				return nil, nil
			}
			cp := b
			return &cp, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) { return b, nil },
		GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) {
			return nil, nil
		},
		GetOperationStateFunc: func(opID string) ([]byte, error) { return state[opID], nil },
		SaveOperationStateFunc: func(opID string, data []byte) error {
			state[opID] = data
			return nil
		},
	}
	return store, state, &afterIDsSeen
}

// noHitSource returns no results, so the sweep never enriches -- isolating cursor
// mechanics from the enrichment path.
type noHitSource struct{}

func (noHitSource) Name() string { return "Audible" }
func (noHitSource) SearchByTitle(context.Context, string) ([]metadata.BookMetadata, error) {
	return nil, nil
}
func (noHitSource) SearchByTitleAndAuthor(context.Context, string, string) ([]metadata.BookMetadata, error) {
	return nil, nil
}

func candidateBook(id, title string) database.Book {
	// Both identifiers blank => a candidate for enrichment.
	return database.Book{ID: id, Title: title}
}

func readCursor(t *testing.T, state map[string][]byte) isbnEnrichCursor {
	t.Helper()
	raw, ok := state[isbnEnrichCursorKey]
	if !ok {
		t.Fatalf("no cursor persisted under %q", isbnEnrichCursorKey)
	}
	var cur isbnEnrichCursor
	if err := json.Unmarshal(raw, &cur); err != nil {
		t.Fatalf("unmarshal cursor: %v", err)
	}
	return cur
}

// A run that exhausts the library persists an empty cursor so the next run wraps.
func TestEnrichMissingISBNs_WrapsAtEndOfLibrary(t *testing.T) {
	books := []database.Book{
		candidateBook("b1", "A"), candidateBook("b2", "B"), candidateBook("b3", "C"),
	}
	store, state, seen := cursorFixture(t, books)
	svc := NewISBNService(store, []metadata.MetadataSource{noHitSource{}})

	checked, _, err := svc.EnrichMissingISBNs(context.Background(), 100, nil, "op-run-1")
	if err != nil {
		t.Fatalf("EnrichMissingISBNs: %v", err)
	}
	if checked != 3 {
		t.Errorf("checked = %d, want 3 (all candidates)", checked)
	}
	if (*seen)[0] != "" {
		t.Errorf("first page afterID = %q, want \"\" (start of library)", (*seen)[0])
	}
	if cur := readCursor(t, state); cur.AfterID != "" {
		t.Errorf("persisted cursor = %q, want \"\" (wrap after exhausting the library)", cur.AfterID)
	}
}

// The next run resumes strictly after the persisted cursor, not from the top.
func TestEnrichMissingISBNs_ResumesFromCursor(t *testing.T) {
	books := []database.Book{
		candidateBook("b1", "A"), candidateBook("b2", "B"),
		candidateBook("b3", "C"), candidateBook("b4", "D"),
	}
	store, state, seen := cursorFixture(t, books)
	// Seed the cursor as if a prior run stopped after b2.
	seed, _ := json.Marshal(isbnEnrichCursor{AfterID: "b2"})
	state[isbnEnrichCursorKey] = seed

	svc := NewISBNService(store, []metadata.MetadataSource{noHitSource{}})
	checked, _, err := svc.EnrichMissingISBNs(context.Background(), 100, nil, "op-run-2")
	if err != nil {
		t.Fatalf("EnrichMissingISBNs: %v", err)
	}
	if (*seen)[0] != "b2" {
		t.Fatalf("first page afterID = %q, want \"b2\" (resume, not restart)", (*seen)[0])
	}
	if checked != 2 {
		t.Errorf("checked = %d, want 2 (only b3, b4 remain)", checked)
	}
}

// When the batch limit is reached mid-library, the cursor keeps the position so
// the next run resumes there -- it must NOT wrap back to the top.
func TestEnrichMissingISBNs_LimitKeepsPositionNoWrap(t *testing.T) {
	books := []database.Book{
		candidateBook("b1", "A"), candidateBook("b2", "B"),
		candidateBook("b3", "C"), candidateBook("b4", "D"),
	}
	store, state, _ := cursorFixture(t, books)
	svc := NewISBNService(store, []metadata.MetadataSource{noHitSource{}})

	checked, _, err := svc.EnrichMissingISBNs(context.Background(), 2, nil, "op-run-3")
	if err != nil {
		t.Fatalf("EnrichMissingISBNs: %v", err)
	}
	if checked != 2 {
		t.Fatalf("checked = %d, want 2 (limit)", checked)
	}
	if cur := readCursor(t, state); cur.AfterID != "b2" {
		t.Errorf("persisted cursor = %q, want \"b2\" (mid-library position kept, no wrap)", cur.AfterID)
	}
}

// The cursor advances past non-candidate books (already-identified), so a run
// does not stall re-examining the same enriched front every time.
func TestEnrichMissingISBNs_AdvancesPastNonCandidates(t *testing.T) {
	isbn := "9780441569595"
	asin := "B000SEGUDE"
	done := database.Book{ID: "b1", Title: "Done", ISBN13: &isbn, ASIN: &asin} // non-candidate
	books := []database.Book{
		done, candidateBook("b2", "B"), candidateBook("b3", "C"),
	}
	store, state, _ := cursorFixture(t, books)
	svc := NewISBNService(store, []metadata.MetadataSource{noHitSource{}})

	checked, _, err := svc.EnrichMissingISBNs(context.Background(), 100, nil, "op-run-4")
	if err != nil {
		t.Fatalf("EnrichMissingISBNs: %v", err)
	}
	if checked != 2 {
		t.Errorf("checked = %d, want 2 (b1 is already identified, skipped)", checked)
	}
	if cur := readCursor(t, state); cur.AfterID != "" {
		t.Errorf("persisted cursor = %q, want \"\" (swept to the end past the non-candidate)", cur.AfterID)
	}
}

// A stale cursor (its book was deleted since it was saved) resolves to no books;
// the run wraps to the start so the next run re-sweeps from the top rather than
// staying stuck. checked is 0 this run because nothing after the (gone) cursor
// existed to examine.
func TestEnrichMissingISBNs_StaleCursorWrapsToStart(t *testing.T) {
	books := []database.Book{candidateBook("b1", "A"), candidateBook("b2", "B")}
	store, state, seen := cursorFixture(t, books)
	seed, _ := json.Marshal(isbnEnrichCursor{AfterID: "deleted-book"})
	state[isbnEnrichCursorKey] = seed

	svc := NewISBNService(store, []metadata.MetadataSource{noHitSource{}})
	checked, _, err := svc.EnrichMissingISBNs(context.Background(), 100, nil, "op-stale")
	if err != nil {
		t.Fatalf("EnrichMissingISBNs: %v", err)
	}
	if (*seen)[0] != "deleted-book" {
		t.Errorf("first page afterID = %q, want \"deleted-book\" (attempted resume)", (*seen)[0])
	}
	if checked != 0 {
		t.Errorf("checked = %d, want 0 (nothing after the deleted cursor)", checked)
	}
	if cur := readCursor(t, state); cur.AfterID != "" {
		t.Errorf("persisted cursor = %q, want \"\" (wrapped to start for the next run)", cur.AfterID)
	}
}

// Fail-open: an unreadable cursor blob starts the sweep from the top rather than
// aborting enrichment.
func TestEnrichMissingISBNs_FailOpenOnCursorReadError(t *testing.T) {
	books := []database.Book{candidateBook("b1", "A"), candidateBook("b2", "B")}
	store, _, seen := cursorFixture(t, books)
	store.GetOperationStateFunc = func(string) ([]byte, error) {
		return nil, errors.New("pebble: closed")
	}
	svc := NewISBNService(store, []metadata.MetadataSource{noHitSource{}})

	checked, _, err := svc.EnrichMissingISBNs(context.Background(), 100, nil, "op-run-5")
	if err != nil {
		t.Fatalf("EnrichMissingISBNs should fail open, got err: %v", err)
	}
	if len(*seen) == 0 || (*seen)[0] != "" {
		t.Errorf("first page afterID = %v, want \"\" (fail-open to library start)", *seen)
	}
	if checked != 2 {
		t.Errorf("checked = %d, want 2", checked)
	}
}

// effectiveEnrichTitle prefers the canonical title, falls back to the transcribed
// title, and is empty only when neither exists.
func TestEffectiveEnrichTitle(t *testing.T) {
	tr := "Transcribed Title"
	cases := []struct {
		name string
		book database.Book
		want string
	}{
		{"canonical wins", database.Book{Title: "Real", TranscribedTitle: &tr}, "Real"},
		{"blank falls back to transcribed", database.Book{Title: "  ", TranscribedTitle: &tr}, tr},
		{"neither", database.Book{Title: ""}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveEnrichTitle(&tc.book); got != tc.want {
				t.Errorf("effectiveEnrichTitle = %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty-canonical-title book with a transcribed title can be enriched: the
// query is non-empty and the hit strict-matches the transcribed title.
func TestEnrichBookISBN_UsesTranscribedTitleWhenTitleBlank(t *testing.T) {
	tr := "Neuromancer"
	var wrote *database.Book
	store := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "", TranscribedTitle: &tr}, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) { wrote = b; return b, nil },
		GetMetadataFieldStatesFunc: func(string) ([]database.MetadataFieldState, error) {
			return nil, nil
		},
	}
	svc := NewISBNService(store, []metadata.MetadataSource{&isbnStubSource{name: "Audible", results: isbnHit()}})

	found, err := svc.EnrichBookISBN(context.Background(), "b1")
	if err != nil {
		t.Fatalf("EnrichBookISBN: %v", err)
	}
	if !found {
		t.Fatal("found=false; the transcribed title should have driven a match")
	}
	if wrote == nil || wrote.ISBN13 == nil || *wrote.ISBN13 != "9780441569595" {
		t.Errorf("ISBN13 not written from a transcribed-title match: %+v", wrote)
	}
}

// resolveBatchLimit: setting present/absent/unparseable.
func TestResolveBatchLimit(t *testing.T) {
	mk := func(f func(string) (*database.Setting, error)) *ISBNService {
		return NewISBNService(&database.MockStore{GetSettingFunc: f}, nil)
	}
	cases := []struct {
		name string
		fn   func(string) (*database.Setting, error)
		want int
	}{
		{"absent (ErrSettingNotFound)", func(string) (*database.Setting, error) {
			return nil, database.ErrSettingNotFound
		}, defaultISBNEnrichBatchLimit},
		{"absent (nil,nil mock shape)", func(string) (*database.Setting, error) {
			return nil, nil
		}, defaultISBNEnrichBatchLimit},
		{"valid", func(string) (*database.Setting, error) {
			return &database.Setting{Value: "500"}, nil
		}, 500},
		{"unparseable", func(string) (*database.Setting, error) {
			return &database.Setting{Value: "lots"}, nil
		}, defaultISBNEnrichBatchLimit},
		{"non-positive", func(string) (*database.Setting, error) {
			return &database.Setting{Value: "0"}, nil
		}, defaultISBNEnrichBatchLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mk(tc.fn).resolveBatchLimit(context.Background()); got != tc.want {
				t.Errorf("resolveBatchLimit = %d, want %d", got, tc.want)
			}
		})
	}
}
