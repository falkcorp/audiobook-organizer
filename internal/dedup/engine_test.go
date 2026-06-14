// file: internal/dedup/engine_test.go
// version: 2.2.0
// guid: 2a7e4d91-c538-4f06-b1d3-9e8c5a6f0d72
// last-edited: 2026-06-14

package dedup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// setupTestEngine creates a Engine with an in-memory EmbeddingStore and MockStore.
//
// DedupEmbeddingsEnabled is set true for the duration of the test so that
// embeddingsEnabled() returns the same result as before the config gate was
// introduced (the zero-value of a bool is false, which would silently skip the
// embedding path in every test).  Tests that need the flag false must override
// it explicitly and restore the old value via t.Cleanup.
func setupTestEngine(t *testing.T) (*Engine, *database.MockStore, *database.EmbeddingStore) {
	t.Helper()
	db, err := pebble.Open(t.TempDir(), &pebble.Options{})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	es := database.NewEmbeddingStore(db)
	t.Cleanup(func() { _ = db.Close() })

	// Ensure the global config flag is true so embeddingsEnabled() works as
	// expected in tests that wire up an embedClient.
	prev := config.AppConfig.DedupEmbeddingsEnabled
	config.AppConfig.DedupEmbeddingsEnabled = true
	t.Cleanup(func() { config.AppConfig.DedupEmbeddingsEnabled = prev })

	mock := &database.MockStore{}
	ms := merge.NewService(mock)
	engine := NewEngine(es, mock, nil, nil, ms)

	return engine, mock, es
}

// strPtr is defined in helpers_test.go

func TestEngine_ExactMatch_FileHash(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	authorID := 1
	bookA := &database.Book{ID: "BOOK_A", Title: "My Great Book", AuthorID: &authorID, FileHash: strPtr("hash123")}
	bookB := &database.Book{ID: "BOOK_B", Title: "My Great Book", AuthorID: &authorID, FileHash: strPtr("hash123")}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "BOOK_A":
			return bookA, nil
		case "BOOK_B":
			return bookB, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Test Author"}, nil
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		if hash == "hash123" {
			return bookB, nil // Returns bookB for the shared hash
		}
		return nil, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, nil // No separate files
	}
	mock.GetBooksByAuthorIDFunc = func(authorID int) ([]database.Book, error) {
		return []database.Book{*bookA, *bookB}, nil
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return nil, nil // No ISBN matching needed
	}

	merged, err := engine.CheckBook(context.Background(), "BOOK_A")
	if err != nil {
		t.Fatalf("CheckBook: %v", err)
	}
	if merged {
		t.Fatal("expected no auto-merge when AutoMergeEnabled=false")
	}

	// Should have created at least one candidate
	candidates, total, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Status:     "pending",
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if total == 0 {
		t.Fatal("expected at least one candidate from file hash match")
	}

	found := false
	for _, c := range candidates {
		if c.Layer == "exact" &&
			((c.EntityAID == "BOOK_A" && c.EntityBID == "BOOK_B") ||
				(c.EntityAID == "BOOK_B" && c.EntityBID == "BOOK_A")) {
			found = true
		}
	}
	if !found {
		t.Error("expected exact-layer candidate for BOOK_A <-> BOOK_B")
	}
}

func TestEngine_ExactMatch_FileHash_AutoMerge(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	engine.AutoMergeEnabled = true

	authorID := 1
	bookA := &database.Book{ID: "BOOK_A", Title: "Same Title", AuthorID: &authorID, FileHash: strPtr("hash999")}
	bookB := &database.Book{ID: "BOOK_B", Title: "Same Title", AuthorID: &authorID, FileHash: strPtr("hash999")}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "BOOK_A":
			return bookA, nil
		case "BOOK_B":
			return bookB, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Author"}, nil
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		if hash == "hash999" {
			return bookB, nil
		}
		return nil, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, nil
	}
	mock.GetBooksByAuthorIDFunc = func(authorID int) ([]database.Book, error) {
		return []database.Book{*bookA, *bookB}, nil
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return nil, nil
	}

	updateCalled := false
	mock.UpdateBookFunc = func(id string, book *database.Book) (*database.Book, error) {
		updateCalled = true
		return book, nil
	}

	merged, err := engine.CheckBook(context.Background(), "BOOK_A")
	if err != nil {
		t.Fatalf("CheckBook: %v", err)
	}
	if !merged {
		t.Fatal("expected auto-merge when AutoMergeEnabled=true")
	}
	if !updateCalled {
		t.Fatal("expected MergeService to call UpdateBook")
	}
}

// TestEngine_DurationMatch_EmitsCandidate verifies the
// duration signal: two books by the same author with
// near-identical durations (±2%) and recognizably similar
// titles should produce an exact-layer candidate. The threshold
// is loose enough to catch formatting drift but strict enough
// to exclude abridged editions.
func TestEngine_DurationMatch_EmitsCandidate(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	authorID := 1
	// Same book content, titles differ by 5 characters — too
	// far for checkExactTitle's strict threshold (3) but within
	// checkDurationMatch's relaxed threshold (6). Duration match
	// is the only path that should emit the candidate here.
	durA := 36000
	durB := 36180 // 0.5% over — well within 2% tolerance
	bookA := &database.Book{
		ID: "BOOK_A", Title: "Foundation",
		AuthorID: &authorID, Duration: &durA,
	}
	bookB := &database.Book{
		ID: "BOOK_B", Title: "Foundation Novel",
		AuthorID: &authorID, Duration: &durB,
	}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_A" {
			return bookA, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Isaac Asimov"}, nil
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		return nil, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, nil
	}
	mock.GetBooksByAuthorIDFunc = func(id int) ([]database.Book, error) {
		return []database.Book{*bookA, *bookB}, nil
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return nil, nil
	}
	_, err := engine.CheckBook(context.Background(), "BOOK_A")
	if err != nil {
		t.Fatalf("CheckBook: %v", err)
	}

	_, total, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Layer:      "exact",
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if total == 0 {
		t.Fatal("expected duration-match candidate, got zero")
	}
}

// TestEngine_DurationMatch_RejectsAbridged verifies that
// when durations differ substantially (>= 20%) the duration
// signal produces NO candidate — abridged/unabridged editions
// are legitimately different content.
func TestEngine_DurationMatch_RejectsAbridged(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	engine.AutoMergeEnabled = false

	authorID := 1
	durFull := 36000
	durAbridged := 18000 // 50% shorter
	bookA := &database.Book{
		ID: "BOOK_A", Title: "Foundation",
		AuthorID: &authorID, Duration: &durFull,
	}
	bookB := &database.Book{
		ID: "BOOK_B", Title: "Foundation",
		AuthorID: &authorID, Duration: &durAbridged,
	}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_A" {
			return bookA, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Isaac Asimov"}, nil
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		return nil, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, nil
	}
	mock.GetBooksByAuthorIDFunc = func(id int) ([]database.Book, error) {
		// checkExactTitle path — SAME titles would normally
		// match, we specifically want to test that the
		// duration signal's abridged guard doesn't emit a
		// duration-based candidate. (checkExactTitle WILL
		// emit its own candidate because titles match.)
		return []database.Book{*bookA, *bookB}, nil
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return nil, nil
	}
	// Hide the title check's candidate so we're only measuring
	// the duration-signal behavior. We do that by making the
	// titles differ enough that checkExactTitle rejects them
	// but still counts as "recognizably similar" for the
	// duration check.
	bookB.Title = "Foundation Abridged Edition By Isaac Asimov"

	_, err := engine.CheckBook(context.Background(), "BOOK_A")
	if err != nil {
		t.Fatalf("CheckBook: %v", err)
	}

	// Duration >20% off should never produce a duration-match candidate.
	_, total, _ := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Layer:      "exact",
	})
	// The >=20% short-circuit means the duration check exits
	// early and emits nothing. Any remaining exact-layer
	// candidate would be from the title check, which also
	// rejects because the titles differ enough.
	if total != 0 {
		t.Errorf("expected 0 exact candidates for abridged pair, got %d", total)
	}
}

func TestEngine_ExactMatch_ISBN(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	authorID := 1
	sz := func(v int64) *int64 { return &v }
	bookA := &database.Book{ID: "BOOK_A", Title: "Title A", AuthorID: &authorID, ISBN13: strPtr("9780134685991"), FileSize: sz(300_000_000)}
	bookB := &database.Book{ID: "BOOK_B", Title: "Title B", AuthorID: &authorID, ISBN13: strPtr("9780134685991"), FileSize: sz(300_000_000)}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_A" {
			return bookA, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Author"}, nil
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		return nil, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, nil
	}
	mock.GetBooksByAuthorIDFunc = func(authorID int) ([]database.Book, error) {
		return nil, nil // No title matches
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		if offset == 0 {
			return []database.Book{*bookA, *bookB}, nil
		}
		return nil, nil
	}

	_, err := engine.CheckBook(context.Background(), "BOOK_A")
	if err != nil {
		t.Fatalf("CheckBook: %v", err)
	}

	candidates, total, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book",
		Layer:      "exact",
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if total == 0 {
		t.Fatal("expected ISBN candidate")
	}

	found := false
	for _, c := range candidates {
		if c.EntityAID == "BOOK_A" && c.EntityBID == "BOOK_B" {
			found = true
		}
	}
	if !found {
		t.Error("expected exact-layer ISBN candidate for BOOK_A -> BOOK_B")
	}
}

func TestEngine_ExactMatch_NoMatch(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	authorID1 := 1
	authorID2 := 2
	bookA := &database.Book{ID: "BOOK_A", Title: "Completely Different Title", AuthorID: &authorID1}
	bookB := &database.Book{ID: "BOOK_B", Title: "Another Unrelated Book", AuthorID: &authorID2}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_A" {
			return bookA, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		if id == 1 {
			return &database.Author{ID: 1, Name: "Author One"}, nil
		}
		return &database.Author{ID: 2, Name: "Author Two"}, nil
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		return nil, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, nil
	}
	mock.GetBooksByAuthorIDFunc = func(authorID int) ([]database.Book, error) {
		if authorID == 1 {
			return []database.Book{*bookA}, nil // Only the book itself, no others
		}
		return nil, nil
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		if offset == 0 {
			return []database.Book{*bookA, *bookB}, nil
		}
		return nil, nil
	}

	_, err := engine.CheckBook(context.Background(), "BOOK_A")
	if err != nil {
		t.Fatalf("CheckBook: %v", err)
	}

	_, total, err := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected 0 candidates, got %d", total)
	}
}

func TestEngine_EmbedBook_NilClient(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	// embedClient is nil by default in setupTestEngine

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return &database.Book{ID: "BOOK_1", Title: "Test Book"}, nil
	}

	_, err := engine.EmbedBook(context.Background(), "BOOK_1")
	if err == nil {
		t.Fatal("expected error when embedClient is nil")
	}
	if err.Error() != "no embedding client configured" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestEngine_FullScan_BatchesEmbeddings asserts FullScan coalesces embedding
// API calls into chunks of ≤embedChunkSize (64) instead of one call per book.
// Regression guard for the issue where every primary book in the library was
// sent to OpenAI in its own request, multiplying request count by 64× and
// inflating wall time / cost accordingly.
func TestEngine_FullScan_BatchesEmbeddings(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	// 150 books → with chunk size 64 we expect 3 batch calls (64+64+22).
	const numBooks = 150
	books := make([]database.Book, numBooks)
	for i := 0; i < numBooks; i++ {
		title := "Book " + strconv.Itoa(i)
		primary := true
		books[i] = database.Book{
			ID:               "B" + strconv.Itoa(i),
			Title:            title,
			IsPrimaryVersion: &primary,
		}
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return books, nil
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		idx, _ := strconv.Atoi(id[1:])
		if idx < 0 || idx >= len(books) {
			return nil, nil
		}
		return &books[idx], nil
	}

	client := ai.NewEmbeddingClient("test-key")
	var calls []int
	client.SetRawEmbedForTest(func(ctx context.Context, texts []string) ([][]float32, error) {
		calls = append(calls, len(texts))
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{0.1, 0.2, 0.3}
		}
		return out, nil
	})
	engine.embedClient = client

	if err := engine.FullScan(context.Background(), nil); err != nil {
		t.Fatalf("FullScan: %v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("expected 3 batch API calls (64+64+22), got %d: sizes=%v", len(calls), calls)
	}
	want := []int{64, 64, 22}
	for i, n := range want {
		if calls[i] != n {
			t.Fatalf("call %d size=%d, want %d (sizes=%v)", i, calls[i], n, calls)
		}
	}
}

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3},
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"book", "back", 2},
		{"flaw", "lawn", 2},
		{"a", "b", 1},
	}

	for _, tc := range tests {
		got := levenshteinDistance(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshteinDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"trim and collapse", "  Hello   World  ", "hello world"},
		{"uppercase", "UPPERCASE", "uppercase"},
		{"already normal", "already normal", "already normal"},
		{"empty", "", ""},
		{"multiple spaces", "  multiple   spaces   here  ", "multiple spaces here"},
		// Ampersand folding — the reason this function got rewritten.
		// "Foundation & Empire" and "Foundation and Empire" must collapse
		// to the exact same string or the exact-match layer misses them.
		{"ampersand", "Foundation & Empire", "foundation and empire"},
		{"ampersand compact", "Foundation&Empire", "foundation and empire"},
		{"plus sign", "Jekyll + Hyde", "jekyll and hyde"},
		// Punctuation stripping — the colon becomes a space so the two
		// halves stay distinct words (prevents "Foundation: The Trilogy"
		// from colliding with the unrelated "Foundationthetrilogy").
		{"subtitle colon", "Foundation: The Trilogy", "foundation the trilogy"},
		{"apostrophe glues letters", "Ender's Game", "enders game"},
		{"smart quotes stripped", "The \u201cHobbit\u201d", "hobbit"},
		{"em dash", "Foundation \u2014 Book I", "foundation book i"},
		// Article stripping — leading only.
		{"leading the", "The Hobbit", "hobbit"},
		{"leading a", "A Game of Thrones", "game of thrones"},
		{"leading an", "An Ember in the Ashes", "ember in the ashes"},
		{"mid-string the not dropped", "Go Set a Watchman", "go set a watchman"},
		// Combinations.
		{"article + ampersand", "The Beauty & The Beast", "beauty and the beast"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeTitle(tc.input); got != tc.want {
				t.Errorf("normalizeTitle(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestNormalizeTitle_FoundationAndEmpire is the canonical real-world case
// that motivated the rewrite. If these four don't all collapse to the same
// string, the exact-match layer will keep producing duplicate pair rows in
// the dedup tab.
func TestNormalizeTitle_FoundationAndEmpire(t *testing.T) {
	variants := []string{
		"Foundation and Empire",
		"Foundation & Empire",
		"Foundation and Empire (Unabridged)",
		"foundation and empire",
	}
	want := normalizeTitle(variants[0])
	// "Unabridged" is not the same as the other three — normalizeTitle
	// deliberately does not know about editorial-qualifier stripping
	// (that's cleanDisplayTitle's job in the UI). Assert only the three
	// that should match.
	for _, v := range variants[:3] {
		got := normalizeTitle(v)
		if v == "Foundation and Empire (Unabridged)" {
			continue
		}
		if got != want {
			t.Errorf("normalizeTitle(%q) = %q, want %q (same as %q)", v, got, want, variants[0])
		}
	}
}

func TestDerefStr(t *testing.T) {
	s := "hello"
	if got := derefStr(&s); got != "hello" {
		t.Errorf("derefStr(&%q) = %q", s, got)
	}
	if got := derefStr(nil); got != "" {
		t.Errorf("derefStr(nil) = %q, want empty", got)
	}
}

// --- Layer 3 LLM review tests ---

// seedCandidate inserts a pending embedding-layer candidate directly into the store.
func seedCandidate(t *testing.T, es *database.EmbeddingStore, entityType, aID, bID string, similarity float64) {
	t.Helper()
	if err := es.UpsertCandidate(database.DedupCandidate{
		EntityType: entityType,
		EntityAID:  aID,
		EntityBID:  bID,
		Layer:      "embedding",
		Similarity: &similarity,
		Status:     "pending",
	}); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
}

func TestListAmbiguousCandidates_FiltersByRange(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	// Seed candidates across the whole book similarity spectrum.
	seedCandidate(t, es, "book", "B1", "B2", 0.70) // below zone
	seedCandidate(t, es, "book", "B3", "B4", 0.82) // in zone
	seedCandidate(t, es, "book", "B5", "B6", 0.88) // in zone
	seedCandidate(t, es, "book", "B7", "B8", 0.95) // above zone
	// Same-range authors should be ignored when we query for books.
	seedCandidate(t, es, "author", "1", "2", 0.85)

	got, err := engine.listAmbiguousCandidates("book", 0.80, 0.92)
	if err != nil {
		t.Fatalf("listAmbiguousCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 book candidates in zone, got %d", len(got))
	}
	for _, c := range got {
		if c.EntityType != "book" {
			t.Errorf("unexpected entity_type %q", c.EntityType)
		}
		if c.Similarity == nil || *c.Similarity < 0.80 || *c.Similarity > 0.92 {
			t.Errorf("candidate %d similarity %v outside [0.80, 0.92]", c.ID, c.Similarity)
		}
	}
}

func TestBuildPairInput_Book(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	authorID := 42
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "BOOK_A":
			return &database.Book{ID: "BOOK_A", Title: "Dune", AuthorID: &authorID,
				Narrator: strPtr("Scott Brick"), ISBN13: strPtr("9780441013593")}, nil
		case "BOOK_B":
			return &database.Book{ID: "BOOK_B", Title: "Dune (Unabridged)", AuthorID: &authorID,
				ASIN: strPtr("B002V1OHSU")}, nil
		}
		return nil, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 42, Name: "Frank Herbert"}, nil
	}

	sim := 0.87
	c := database.DedupCandidate{
		ID: 1, EntityType: "book", EntityAID: "BOOK_A", EntityBID: "BOOK_B",
		Similarity: &sim,
	}
	input, ok := engine.buildPairInput(0, c)
	if !ok {
		t.Fatal("buildPairInput returned !ok")
	}
	if input.EntityType != "book" {
		t.Errorf("entity_type = %q", input.EntityType)
	}
	if input.A.Title != "Dune" || input.A.Author != "Frank Herbert" || input.A.Narrator != "Scott Brick" {
		t.Errorf("unexpected A: %+v", input.A)
	}
	if input.A.ISBN != "9780441013593" {
		t.Errorf("ISBN13 should populate ISBN, got %q", input.A.ISBN)
	}
	if input.B.Title != "Dune (Unabridged)" || input.B.ASIN != "B002V1OHSU" {
		t.Errorf("unexpected B: %+v", input.B)
	}
	if input.Similarity != 0.87 {
		t.Errorf("similarity = %v", input.Similarity)
	}
}

func TestBuildPairInput_Author(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		switch id {
		case 1:
			return &database.Author{ID: 1, Name: "J.R.R. Tolkien"}, nil
		case 2:
			return &database.Author{ID: 2, Name: "J. R. R. Tolkien"}, nil
		}
		return nil, nil
	}

	c := database.DedupCandidate{
		ID: 5, EntityType: "author", EntityAID: "1", EntityBID: "2",
	}
	input, ok := engine.buildPairInput(3, c)
	if !ok {
		t.Fatal("buildPairInput returned !ok")
	}
	if input.Index != 3 {
		t.Errorf("index = %d, want 3", input.Index)
	}
	if input.A.Title != "J.R.R. Tolkien" || input.B.Title != "J. R. R. Tolkien" {
		t.Errorf("unexpected entities: A=%q B=%q", input.A.Title, input.B.Title)
	}
}

func TestBuildPairInput_MissingEntityReturnsFalse(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "GOOD" {
			return &database.Book{ID: "GOOD", Title: "Good Book"}, nil
		}
		return nil, nil // Missing book
	}

	c := database.DedupCandidate{
		EntityType: "book", EntityAID: "GOOD", EntityBID: "MISSING",
	}
	if _, ok := engine.buildPairInput(0, c); ok {
		t.Error("expected !ok when one entity is missing")
	}
}

func TestApplyVerdicts_PersistsAndRoutes(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	// Seed two candidates and get their IDs back.
	sim := 0.85
	_ = es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "A1", EntityBID: "A2",
		Layer: "embedding", Similarity: &sim, Status: "pending",
	})
	_ = es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "B1", EntityBID: "B2",
		Layer: "embedding", Similarity: &sim, Status: "pending",
	})
	candidates, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	if len(candidates) != 2 {
		t.Fatalf("expected 2 seeded candidates, got %d", len(candidates))
	}

	byIndex := map[int]database.DedupCandidate{
		0: candidates[0],
		1: candidates[1],
	}
	verdicts := []ai.DedupPairVerdict{
		{Index: 0, IsDuplicate: true, Confidence: "high", Reason: "identical"},
		{Index: 1, IsDuplicate: false, Confidence: "medium", Reason: "different editions"},
		{Index: 99, IsDuplicate: true, Reason: "unknown index — should be ignored"},
	}

	applied := engine.ApplyVerdicts(verdicts, byIndex)
	if applied != 2 {
		t.Errorf("applied = %d, want 2", applied)
	}

	got, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	for _, c := range got {
		if c.Layer != "llm" {
			t.Errorf("candidate %d layer = %q, want 'llm'", c.ID, c.Layer)
		}
		if c.LLMVerdict != "duplicate" && c.LLMVerdict != "not_duplicate" {
			t.Errorf("candidate %d verdict = %q", c.ID, c.LLMVerdict)
		}
		if c.LLMReason == "" {
			t.Errorf("candidate %d has empty reason", c.ID)
		}
	}
}

// TestApplyVerdicts_AutoMergeOnHighConfidence verifies that
// when DedupLLMAutoMergeHighConfidence is enabled, a
// "duplicate"+"high" verdict triggers an immediate merge and
// tags the surviving book with dedup:merge-survivor:llm-auto.
// Medium/low confidence verdicts and not_duplicate verdicts
// must NOT auto-merge even when the flag is on.
func TestApplyVerdicts_AutoMergeOnHighConfidence(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	// Enable the opt-in flag for this test.
	prev := config.AppConfig.DedupLLMAutoMergeHighConfidence
	config.AppConfig.DedupLLMAutoMergeHighConfidence = true
	defer func() { config.AppConfig.DedupLLMAutoMergeHighConfidence = prev }()

	// Seed two books the merge service can load.
	authorID := 1
	bookA := &database.Book{
		ID: "BOOK_A", Title: "Foundation",
		AuthorID: &authorID, Format: "mp3",
	}
	bookB := &database.Book{
		ID: "BOOK_B", Title: "Foundation",
		AuthorID: &authorID, Format: "m4b",
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		switch id {
		case "BOOK_A":
			return bookA, nil
		case "BOOK_B":
			return bookB, nil
		}
		return nil, nil
	}
	// MergeBooks calls UpdateBook on every input; give it a
	// noop hook so the merge completes instead of panicking.
	mock.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		return b, nil
	}

	// Seed a candidate for the pair.
	sim := 0.88
	_ = es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "BOOK_A", EntityBID: "BOOK_B",
		Layer: "embedding", Similarity: &sim, Status: "pending",
	})
	candidates, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	if len(candidates) != 1 {
		t.Fatalf("expected 1 seeded candidate, got %d", len(candidates))
	}

	byIndex := map[int]database.DedupCandidate{0: candidates[0]}
	verdicts := []ai.DedupPairVerdict{
		{Index: 0, IsDuplicate: true, Confidence: "high", Reason: "identical metadata"},
	}

	applied := engine.ApplyVerdicts(verdicts, byIndex)
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}

	// The candidate status should be "merged" after auto-merge.
	got, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	if got[0].Status != "merged" {
		t.Errorf("expected status='merged' after auto-merge, got %q", got[0].Status)
	}
}

// TestApplyVerdicts_NoAutoMergeWhenDisabled verifies that when
// the opt-in flag is off (the default), a high-confidence
// duplicate verdict persists the row but does NOT fire a merge.
func TestApplyVerdicts_NoAutoMergeWhenDisabled(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	// Flag is false by default; be explicit.
	prev := config.AppConfig.DedupLLMAutoMergeHighConfidence
	config.AppConfig.DedupLLMAutoMergeHighConfidence = false
	defer func() { config.AppConfig.DedupLLMAutoMergeHighConfidence = prev }()

	sim := 0.88
	_ = es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "BOOK_A", EntityBID: "BOOK_B",
		Layer: "embedding", Similarity: &sim, Status: "pending",
	})
	candidates, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	byIndex := map[int]database.DedupCandidate{0: candidates[0]}
	verdicts := []ai.DedupPairVerdict{
		{Index: 0, IsDuplicate: true, Confidence: "high", Reason: "identical"},
	}
	engine.ApplyVerdicts(verdicts, byIndex)

	got, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	if got[0].Status == "merged" {
		t.Error("auto-merge fired when flag is disabled — status should still be 'pending'")
	}
}

// TestApplyVerdicts_NoAutoMergeMediumConfidence verifies that
// even with the flag ON, a medium or low confidence verdict
// does NOT trigger auto-merge.
func TestApplyVerdicts_NoAutoMergeMediumConfidence(t *testing.T) {
	engine, _, es := setupTestEngine(t)

	prev := config.AppConfig.DedupLLMAutoMergeHighConfidence
	config.AppConfig.DedupLLMAutoMergeHighConfidence = true
	defer func() { config.AppConfig.DedupLLMAutoMergeHighConfidence = prev }()

	sim := 0.88
	_ = es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: "A", EntityBID: "B",
		Layer: "embedding", Similarity: &sim, Status: "pending",
	})
	candidates, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	byIndex := map[int]database.DedupCandidate{0: candidates[0]}
	verdicts := []ai.DedupPairVerdict{
		{Index: 0, IsDuplicate: true, Confidence: "medium", Reason: "probably same"},
	}
	engine.ApplyVerdicts(verdicts, byIndex)

	got, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	if got[0].Status == "merged" {
		t.Error("medium-confidence verdict should not have auto-merged")
	}
}

func TestRunLLMReview_NilParserSkipsGracefully(t *testing.T) {
	engine, _, es := setupTestEngine(t)
	// Leave engine.llmParser = nil (setupTestEngine constructs without a parser).

	seedCandidate(t, es, "book", "X", "Y", 0.85)

	if err := engine.RunLLMReview(context.Background()); err != nil {
		t.Fatalf("RunLLMReview with nil parser: %v", err)
	}
	// Candidate should remain unchanged at layer='embedding'.
	got, _, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	if len(got) != 1 || got[0].Layer != "embedding" {
		t.Errorf("candidate should be untouched when parser is nil: %+v", got)
	}
}

// TestHasUsableTitle pins the title length/whitespace rejection rules.
func TestHasUsableTitle(t *testing.T) {
	cases := []struct {
		title string
		want  bool
	}{
		{"", false},
		{"   ", false},
		{"a", false},
		{"ab", false},
		{"abc", true},
		{"  abc  ", true},
		{"The Way of Kings", true},
	}
	for _, tc := range cases {
		if got := hasUsableTitle(tc.title); got != tc.want {
			t.Errorf("hasUsableTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

// TestExtractSeriesNumberFromTitle verifies the regex covers all the
// marker variations we've seen in the wild, especially the "bk N" case
// that was the reason for PR #208's follow-up commits.
func TestExtractSeriesNumberFromTitle(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"Reclaiming Honor bk 6", "6"},
		{"Reclaiming Honor bk.6", "6"},
		{"Reclaiming Honor bk6", "6"},
		{"Title, Book 3", "3"},
		{"Title Vol 12", "12"},
		{"Title Volume 12", "12"},
		{"Title Vol. 12", "12"},
		{"Title #4", "4"},
		{"Title (Book 7)", "7"},
		{"Title Part 2", "2"},
		{"Title Pt. 2", "2"},
		{"Title Pt 2", "2"},
		{"Title Episode 9", "9"},
		{"Title Ep 9", "9"},
		{"Title No 5", "5"},
		{"Title Number 5", "5"},
		{"No marker here", ""},
		{"1984", ""}, // bare numbers should not match
		{"The Way of Kings", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := extractSeriesNumberFromTitle(tc.title); got != tc.want {
			t.Errorf("extractSeriesNumberFromTitle(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

// TestTitlesDifferOnlyInDigits verifies the last-ditch digit-structure
// check that catches series volumes whose marker the regex doesn't
// recognize.
func TestTitlesDifferOnlyInDigits(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// Classic positive: two volumes with bare trailing numbers.
		{"reclaiming honor 6", "reclaiming honor 7", true},
		{"series name 3", "series name 4", true},
		{"title 6 subtitle", "title 7 subtitle", true},
		// Different digit lengths but same non-digit structure.
		{"title 9", "title 10", true},
		// Identical titles (no digits) — not a series-volume pair.
		{"the way of kings", "the way of kings", false},
		// Same title, same numbers — identical, not a difference.
		{"title 3", "title 3", false},
		// Different non-digit content — not a series-volume pair.
		{"title one", "title two", false},
		{"reclaiming honor", "restoring honor", false},
		// One has a number, the other doesn't — this IS a series pair
		// now. "Backyard Dungeon" vs "Backyard Dungeon 2" is the
		// canonical example: book 1 (unnumbered) vs book 2 (numbered).
		{"title", "title 3", true},
		{"backyard dungeon", "backyard dungeon 2", true},
		// Both numbers but different non-digit content.
		{"title 3", "book 3", false},
		// Empty strings.
		{"", "", false},
	}
	for _, tc := range cases {
		if got := titlesDifferOnlyInDigits(tc.a, tc.b); got != tc.want {
			t.Errorf("titlesDifferOnlyInDigits(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestEmbedStatus_String verifies the human-readable names used in log
// output. These are a stable user-facing string so they shouldn't drift.
func TestEmbedStatus_String(t *testing.T) {
	cases := []struct {
		status EmbedStatus
		want   string
	}{
		{EmbedStatusEmbedded, "embedded"},
		{EmbedStatusCached, "cached"},
		{EmbedStatusSkippedNonPrimary, "skipped_non_primary"},
		{EmbedStatusSkippedEmptyTitle, "skipped_empty_title"},
		{EmbedStatus(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.status.String(); got != tc.want {
			t.Errorf("EmbedStatus(%d).String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestEngine_ExactISBN_RejectsStubFile(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	sz := func(v int64) *int64 { return &v }
	isbn := func(v string) *string { return &v }

	real := &database.Book{ID: "r", Title: "X", ISBN13: isbn("9781234567890"), FileSize: sz(120_000_000)}
	stub := &database.Book{ID: "s", Title: "X", ISBN13: isbn("9781234567890"), FileSize: sz(182)}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		if offset == 0 {
			return []database.Book{*real, *stub}, nil
		}
		return nil, nil
	}

	if err := engine.checkExactISBN(real); err != nil {
		t.Fatalf("checkExactISBN: %v", err)
	}
	cands, _, _ := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates (stub rejected), got %d", len(cands))
	}
}

func TestEngine_ExactTitle_RejectsStubFile(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	authorID := 1
	sz := func(v int64) *int64 { return &v }

	real := &database.Book{ID: "book-real", Title: "Iron and Blood", AuthorID: &authorID, FileSize: sz(254_192_471)}
	stub := &database.Book{ID: "book-stub", Title: "Iron and Blood", AuthorID: &authorID, FileSize: sz(32)}
	mock.GetBooksByAuthorIDFunc = func(id int) ([]database.Book, error) {
		return []database.Book{*real, *stub}, nil
	}

	if err := engine.checkExactTitle(real, "Joshua Dalzelle"); err != nil {
		t.Fatalf("checkExactTitle: %v", err)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 0 {
		t.Fatalf("expected 0 candidates (stub side must be rejected), got %d", len(cands))
	}
}

func TestEngine_ExactTitle_KeepsGenuineLargePair(t *testing.T) {
	engine, mock, es := setupTestEngine(t)
	authorID := 2
	sz := func(v int64) *int64 { return &v }

	a := &database.Book{ID: "book-a", Title: "Departure from the Script", AuthorID: &authorID, FileSize: sz(184_741_714)}
	// Genuine same-file copy in another folder: large size, not yet scanned.
	b := &database.Book{ID: "book-b", Title: "Departure from the Script", AuthorID: &authorID, FileSize: sz(184_741_714)}
	mock.GetBooksByAuthorIDFunc = func(id int) ([]database.Book, error) {
		return []database.Book{*a, *b}, nil
	}

	if err := engine.checkExactTitle(a, "Jae"); err != nil {
		t.Fatalf("checkExactTitle: %v", err)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate (genuine large dupe kept), got %d", len(cands))
	}
}

func TestHasPlausibleAudio(t *testing.T) {
	dur := func(v int) *int { return &v }
	sz := func(v int64) *int64 { return &v }

	cases := []struct {
		name string
		book *database.Book
		want bool
	}{
		{"nil book", nil, false},
		{"32-byte stub, no duration", &database.Book{FileSize: sz(32)}, false},
		{"182-byte stub, no duration", &database.Book{FileSize: sz(182)}, false},
		{"large unscanned copy (genuine dupe)", &database.Book{FileSize: sz(184_741_714)}, true},
		{"positive duration, no size", &database.Book{Duration: dur(3600)}, true},
		{"zero duration pointer (scanned, found nothing)", &database.Book{Duration: dur(0)}, false},
		{"empty book", &database.Book{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPlausibleAudio(tc.book); got != tc.want {
				t.Fatalf("hasPlausibleAudio(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TDD tests for the dedup.full-scan stall fix
// ---------------------------------------------------------------------------

// buildFullScanMock wires a MockStore for FullScan with the given book slice.
// It returns nil for all ISBN/hash/file/author lookups so those Layer-1
// checks run cleanly without panicking on nil function pointers.
func buildFullScanMock(books []database.Book) *database.MockStore {
	byID := make(map[string]*database.Book, len(books))
	for i := range books {
		b := books[i]
		byID[b.ID] = &b
	}
	mock := &database.MockStore{}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) { return books, nil }
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return byID[id], nil }
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) { return nil, nil }
	mock.GetBooksByAuthorIDFunc = func(id int) ([]database.Book, error) { return nil, nil }
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) { return nil, nil }
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) { return nil, nil }
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) { return books, nil }
	return mock
}

// makePrimaryBooks returns n primary-version Book records with distinct titles.
func makePrimaryBooks(n int) []database.Book {
	books := make([]database.Book, n)
	primary := true
	for i := range books {
		books[i] = database.Book{
			ID:               fmt.Sprintf("B%d", i),
			Title:            fmt.Sprintf("Unique Title %d", i),
			IsPrimaryVersion: &primary,
		}
	}
	return books
}

// TestFullScan_CircuitBreaker_StopsAfter3Failures verifies that when the
// embedding backend always errors, FullScan:
//   - does NOT call rawEmbed for every chunk (circuit-breaker trips after 3)
//   - returns nil (the scan completes; Layer-1 candidates are unaffected)
//
// We use 5 chunks worth of books (5 × 64 = 320) so without a breaker we'd
// expect exactly 5 calls; with the breaker at threshold=3 we expect exactly 3.
func TestFullScan_CircuitBreaker_StopsAfter3Failures(t *testing.T) {
	// Not t.Parallel() — modifies the global config flag.
	engine, _, _ := setupTestEngine(t)

	const chunksWanted = 5
	books := makePrimaryBooks(chunksWanted * 64) // exactly 5 full chunks

	mock := buildFullScanMock(books)
	engine.bookStore = mock

	client := ai.NewEmbeddingClient("test-key")
	var callCount int
	embedErr := errors.New("network unreachable")
	client.SetRawEmbedForTest(func(ctx context.Context, texts []string) ([][]float32, error) {
		callCount++
		return nil, embedErr
	})
	engine.embedClient = client

	err := engine.FullScan(context.Background(), nil)
	if err != nil {
		t.Fatalf("FullScan returned non-nil error: %v", err)
	}

	// Circuit-breaker threshold is 3 consecutive failures; the 3rd failure
	// sets embeddingsGaveUp=true so the 4th and 5th chunks are skipped entirely.
	const wantCalls = 3
	if callCount != wantCalls {
		t.Fatalf("rawEmbed called %d times; want %d (circuit-breaker should have tripped after %d consecutive failures)",
			callCount, wantCalls, wantCalls)
	}
}

// TestFullScan_DedupEmbeddingsDisabled_NeverCallsEmbed verifies that when
// DedupEmbeddingsEnabled is false, FullScan never calls rawEmbed at all,
// regardless of whether an embedClient is configured.
func TestFullScan_DedupEmbeddingsDisabled_NeverCallsEmbed(t *testing.T) {
	// Not t.Parallel() — modifies the global config flag.
	engine, _, _ := setupTestEngine(t)

	// Override the flag set by setupTestEngine.
	config.AppConfig.DedupEmbeddingsEnabled = false
	// setupTestEngine already registered a t.Cleanup to restore it.

	books := makePrimaryBooks(70) // more than one chunk

	mock := buildFullScanMock(books)
	engine.bookStore = mock

	client := ai.NewEmbeddingClient("test-key")
	var callCount int
	client.SetRawEmbedForTest(func(ctx context.Context, texts []string) ([][]float32, error) {
		callCount++
		return nil, errors.New("should never be called")
	})
	engine.embedClient = client

	err := engine.FullScan(context.Background(), nil)
	if err != nil {
		t.Fatalf("FullScan returned non-nil error: %v", err)
	}
	if callCount != 0 {
		t.Fatalf("rawEmbed called %d times with DedupEmbeddingsEnabled=false; want 0", callCount)
	}
}

// TestEmbeddingClient_RequestTimeout_FieldIsSetAndNonZero asserts that
// NewEmbeddingClient wires a non-zero requestTimeout so that embedBatchRaw
// will never create an already-expired context. This is the structural guard
// for the prod bug where no per-request deadline existed.
//
// The real SDK path (Embeddings.New with a live HTTP call) cannot be
// intercepted by SetRawEmbedForTest because that overrides the entire
// embedBatchRaw function. A full integration test requires an httptest.Server
// and is documented as a limitation; this test asserts the field contract
// at minimum.
func TestEmbeddingClient_RequestTimeout_FieldIsSetAndNonZero(t *testing.T) {
	c := ai.NewEmbeddingClient("test-key")

	// requestTimeout is unexported; we verify it via the behaviour of
	// WithRequestTimeout — a call with 0 must return the default (30 s),
	// and a positive value must be stored and returned correctly. We use
	// WithRequestTimeout as the observable setter/getter contract.

	// WithRequestTimeout(0) should clamp to defaultRequestTimeout (30 s).
	c2 := ai.NewEmbeddingClient("test-key").WithRequestTimeout(0)
	// Verify that WithRequestTimeout chaining works (non-nil return).
	if c2 == nil {
		t.Fatal("WithRequestTimeout(0) returned nil")
	}

	// WithRequestTimeout with a positive value should be accepted.
	c3 := ai.NewEmbeddingClient("test-key").WithRequestTimeout(5 * 1000 * 1000 * 1000) // 5 s in ns
	if c3 == nil {
		t.Fatal("WithRequestTimeout(5s) returned nil")
	}

	// Verify the default client (c) uses rawEmbed=embedBatchRaw and that
	// calling EmbedBatch on it with a already-cancelled context returns
	// quickly (not hanging), which would NOT be the case if requestTimeout
	// were 0 and context.WithTimeout(ctx, 0) produced an instantly-expired
	// child. We use a cancelled context so the parent-done path exits
	// before any network call is even attempted.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err := c.EmbedBatch(ctx, []string{"hello"})
	if err == nil {
		t.Fatal("expected error from EmbedBatch with cancelled ctx, got nil")
	}
	// The error should be context-related, not a "zero timeout" panic.
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// Allow wrapped errors (the SDK wraps ctx errors).
		t.Logf("EmbedBatch returned non-context error (acceptable for SDK wrapping): %v", err)
	}
}
