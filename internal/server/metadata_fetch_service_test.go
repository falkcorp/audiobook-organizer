// file: internal/server/metadata_fetch_service_test.go
// version: 4.4.1
// guid: f6a7b8c9-d0e1-f2a3-b4c5-d6e7f8a9b0c1
// last-edited: 2026-07-13

package server

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/stretchr/testify/mock"
)

// mockMetadataSource implements metadata.MetadataSource for unit tests.
type mockMetadataSource struct {
	name                   string
	searchByTitleFunc      func(title string) ([]metadata.BookMetadata, error)
	searchByTitleAndAuthor func(title, author string) ([]metadata.BookMetadata, error)
}

func (m *mockMetadataSource) Name() string { return m.name }
func (m *mockMetadataSource) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	if m.searchByTitleFunc != nil {
		return m.searchByTitleFunc(title)
	}
	return nil, nil
}
func (m *mockMetadataSource) SearchByTitleAndAuthor(_ context.Context, title, author string) ([]metadata.BookMetadata, error) {
	if m.searchByTitleAndAuthor != nil {
		return m.searchByTitleAndAuthor(title, author)
	}
	return nil, nil
}

// setupGlobalStoreForTest sets database.GetGlobalStore() to a MockStore that handles
// metadata state calls (needed by persistFetchedMetadata -> loadMetadataState).
func setupGlobalStoreForTest(t *testing.T) {
	t.Helper()
	old := database.GetGlobalStore()
	t.Cleanup(func() { database.SetGlobalStore(old) })
	database.SetGlobalStore(&database.MockStore{
		GetMetadataFieldStatesFunc: func(bookID string) ([]database.MetadataFieldState, error) {
			return nil, nil
		},
		UpsertMetadataFieldStateFunc: func(state *database.MetadataFieldState) error {
			return nil
		},
	})
}

// --- Existing tests ---

func TestMetadataFetchService_FetchMetadataForBook_NotFound(t *testing.T) {
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return nil, nil
		},
	}
	mfs := metafetch.NewService(mockDB)

	_, err := mfs.FetchMetadataForBook(context.Background(), "nonexistent")

	if err == nil {
		t.Error("expected error for nonexistent book")
	}
}

func TestMetadataFetchService_ApplyMetadataToBook(t *testing.T) {
	mockDB := &database.MockStore{}
	mfs := metafetch.NewService(mockDB)

	book := &database.Book{ID: "1", Title: "Original Title"}
	meta := metadata.BookMetadata{
		Title:     "Fetched Title",
		Publisher: "Test Publisher",
	}

	mfs.ApplyMetadataToBook(book, meta)

	if book.Title != "Fetched Title" {
		t.Errorf("expected title 'Fetched Title', got %q", book.Title)
	}
	if book.Publisher == nil || *book.Publisher != "Test Publisher" {
		t.Errorf("expected publisher 'Test Publisher', got %v", book.Publisher)
	}
}

func TestMetadataFetchService_BuildSourceChain(t *testing.T) {
	// Save and restore config
	origSources := config.AppConfig.MetadataSources
	defer func() { config.AppConfig.MetadataSources = origSources }()

	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "google-books", Name: "Google Books", Enabled: true, Priority: 3},
		{ID: "openlibrary", Name: "Open Library", Enabled: true, Priority: 1},
		{ID: "audnexus", Name: "Audnexus", Enabled: false, Priority: 2},
	}

	mockDB := &database.MockStore{}
	mfs := metafetch.NewService(mockDB)
	chain := mfs.BuildSourceChain()

	if len(chain) != 2 {
		t.Fatalf("expected 2 enabled sources, got %d", len(chain))
	}
	if chain[0].Name() != "Open Library" {
		t.Errorf("expected first source 'Open Library', got %q", chain[0].Name())
	}
	if chain[1].Name() != "Google Books" {
		t.Errorf("expected second source 'Google Books', got %q", chain[1].Name())
	}
}

func TestMetadataFetchService_NoSourcesEnabled(t *testing.T) {
	origSources := config.AppConfig.MetadataSources
	defer func() { config.AppConfig.MetadataSources = origSources }()

	config.AppConfig.MetadataSources = []config.MetadataSource{
		{ID: "openlibrary", Enabled: false, Priority: 1},
	}

	authorID := 1
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Test", AuthorID: &authorID}, nil
		},
	}
	mfs := metafetch.NewService(mockDB)

	_, err := mfs.FetchMetadataForBook(context.Background(), "book1")
	if err == nil {
		t.Error("expected error when no sources enabled")
	}
}

// --- New tests ---

func TestMetadataFetchService_Source1Fails_Source2Succeeds(t *testing.T) {
	setupGlobalStoreForTest(t)

	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "The Hobbit"}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
	}
	mfs := metafetch.NewService(mockDB)

	source1 := &mockMetadataSource{
		name: "FailSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			return nil, fmt.Errorf("network timeout")
		},
	}
	source2 := &mockMetadataSource{
		name: "GoodSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			return []metadata.BookMetadata{{Title: "The Hobbit", Author: "J.R.R. Tolkien"}}, nil
		},
	}
	mfs.SetOverrideSources([]metadata.MetadataSource{source1, source2})

	resp, err := mfs.FetchMetadataForBook(context.Background(), "book1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Source != "GoodSource" {
		t.Errorf("expected source 'GoodSource', got %q", resp.Source)
	}
}

func TestMetadataFetchService_TitleStrippingFallback(t *testing.T) {
	setupGlobalStoreForTest(t)

	authorID := 1
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "The Hobbit - Chapter 5", AuthorID: &authorID}, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: "Tolkien"}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
	}
	mfs := metafetch.NewService(mockDB)

	var searchCount int32
	src := &mockMetadataSource{
		name: "TestSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			atomic.AddInt32(&searchCount, 1)
			// Only the stripped title search succeeds (title-only, no author in query)
			if title == "The Hobbit" {
				return []metadata.BookMetadata{{Title: "The Hobbit", Author: "Tolkien"}}, nil
			}
			return nil, nil
		},
	}
	mfs.SetOverrideSources([]metadata.MetadataSource{src})

	resp, err := mfs.FetchMetadataForBook(context.Background(), "book1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}

	count := atomic.LoadInt32(&searchCount)
	if count < 1 {
		t.Errorf("expected at least 1 search call (stripped title), got %d", count)
	}
}

func TestMetadataFetchService_AllSourcesNoResults(t *testing.T) {
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Nonexistent Book"}, nil
		},
	}
	mfs := metafetch.NewService(mockDB)

	emptySrc := func(name string) *mockMetadataSource {
		return &mockMetadataSource{
			name: name,
			searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
				return nil, nil
			},
		}
	}
	mfs.SetOverrideSources([]metadata.MetadataSource{emptySrc("A"), emptySrc("B"), emptySrc("C")})

	_, err := mfs.FetchMetadataForBook(context.Background(), "book1")
	if err == nil {
		t.Fatal("expected error when all sources return no results")
	}
	if !strings.Contains(err.Error(), "no metadata found") {
		t.Errorf("expected error to contain 'no metadata found', got %q", err.Error())
	}
}

func TestMetadataFetchService_AllSourcesError(t *testing.T) {
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Some Book"}, nil
		},
	}
	mfs := metafetch.NewService(mockDB)

	errSrc := func(name string) *mockMetadataSource {
		return &mockMetadataSource{
			name: name,
			searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
				return nil, fmt.Errorf("%s error", name)
			},
		}
	}
	mfs.SetOverrideSources([]metadata.MetadataSource{errSrc("SourceA"), errSrc("SourceB")})

	_, err := mfs.FetchMetadataForBook(context.Background(), "book1")
	if err == nil {
		t.Fatal("expected error when all sources return errors")
	}
	if !strings.Contains(err.Error(), "last error") {
		t.Errorf("expected error to contain 'last error', got %q", err.Error())
	}
}

func TestMetadataFetchService_ApplyMetadata_AllFields(t *testing.T) {
	mockDB := &database.MockStore{}
	mfs := metafetch.NewService(mockDB)

	book := &database.Book{ID: "1", Title: "Old"}
	meta := metadata.BookMetadata{
		Title:       "New Title",
		Publisher:   "Acme Press",
		Language:    "en",
		PublishYear: 2023,
		// PublishYear is an audiobook release year (Audible/Audnexus-style source),
		// so it routes to AudiobookReleaseYear; without this flag it would route to
		// PrintYear (see fix/metadata-year-and-language-corruption).
		PublishYearIsAudiobookRelease: true,
		CoverURL:                      "https://example.com/cover.jpg",
	}

	mfs.ApplyMetadataToBook(book, meta)

	if book.Title != "New Title" {
		t.Errorf("Title: want 'New Title', got %q", book.Title)
	}
	if book.Publisher == nil || *book.Publisher != "Acme Press" {
		t.Errorf("Publisher: want 'Acme Press', got %v", book.Publisher)
	}
	if book.Language == nil || *book.Language != "en" {
		t.Errorf("Language: want 'en', got %v", book.Language)
	}
	if book.AudiobookReleaseYear == nil || *book.AudiobookReleaseYear != 2023 {
		t.Errorf("AudiobookReleaseYear: want 2023, got %v", book.AudiobookReleaseYear)
	}
	if book.CoverURL == nil || *book.CoverURL != "https://example.com/cover.jpg" {
		t.Errorf("CoverURL: want cover URL, got %v", book.CoverURL)
	}
}

func TestMetadataFetchService_ApplyMetadata_EmptyFieldsPreserved(t *testing.T) {
	mockDB := &database.MockStore{}
	mfs := metafetch.NewService(mockDB)

	origPublisher := "Original Publisher"
	origLang := "fr"
	origYear := 1999
	origCover := "https://example.com/old.jpg"
	book := &database.Book{
		ID:                   "1",
		Title:                "Original Title",
		Publisher:            &origPublisher,
		Language:             &origLang,
		AudiobookReleaseYear: &origYear,
		CoverURL:             &origCover,
	}

	// Empty metadata should not overwrite existing fields
	meta := metadata.BookMetadata{}
	mfs.ApplyMetadataToBook(book, meta)

	if book.Title != "Original Title" {
		t.Errorf("Title should be preserved, got %q", book.Title)
	}
	if *book.Publisher != "Original Publisher" {
		t.Errorf("Publisher should be preserved, got %q", *book.Publisher)
	}
	if *book.Language != "fr" {
		t.Errorf("Language should be preserved, got %q", *book.Language)
	}
	if *book.AudiobookReleaseYear != 1999 {
		t.Errorf("Year should be preserved, got %d", *book.AudiobookReleaseYear)
	}
	if *book.CoverURL != "https://example.com/old.jpg" {
		t.Errorf("CoverURL should be preserved, got %q", *book.CoverURL)
	}
}

func TestMetadataFetchService_AuthorLookupFails(t *testing.T) {
	setupGlobalStoreForTest(t)

	authorID := 99
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Test Book", AuthorID: &authorID}, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return nil, fmt.Errorf("author DB error")
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
	}
	mfs := metafetch.NewService(mockDB)

	src := &mockMetadataSource{
		name: "TestSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			return []metadata.BookMetadata{{Title: "Test Book", Publisher: "Found Publisher"}}, nil
		},
	}
	mfs.SetOverrideSources([]metadata.MetadataSource{src})

	resp, err := mfs.FetchMetadataForBook(context.Background(), "book1")
	if err != nil {
		t.Fatalf("search should proceed despite author lookup failure: %v", err)
	}
	if resp.Source != "TestSource" {
		t.Errorf("expected source 'TestSource', got %q", resp.Source)
	}
}

func TestStripChapterFromTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"The Hobbit", "The Hobbit"},
		{"The Hobbit - Chapter 5", "The Hobbit"},
		{"The Hobbit: Book 2", "The Hobbit"},
		{"The Hobbit (Chapter 3)", "The Hobbit"},
		{"Series Name Book 1", "Series Name"},
		{"Already Clean Title", "Already Clean Title"},
		{"Title, Chapter 7", "Title"},
		{"Title (Unabridged)", "Title"},
		// Bracket series prefix patterns
		{"[The Expanse 9.0] Leviathan Falls", "Leviathan Falls"},
		{"[Series Name] The Book Title", "The Book Title"},
		{"[Dresden Files 1] Storm Front", "Storm Front"},
		// Trailing brackets
		{"Title [Unabridged]", "Title"},
		// Part/Volume patterns
		{"The Fellowship of the Ring Part 2", "The Fellowship of the Ring"},
		{"Dune Volume 1", "Dune"},
		{"Book Title Vol. 3", "Book Title"},
		// Hash number patterns
		{"Title #12", "Title"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := stripChapterFromTitle(tc.input)
			if got != tc.want {
				t.Errorf("stripChapterFromTitle(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestStripSubtitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Title: Subtitle", "Title"},
		{"Title - Subtitle", "Title"},
		{"Title — Subtitle", "Title"},
		{"No Subtitle", "No Subtitle"},
		{"", ""},
		{"Colon:NoSpace", "Colon:NoSpace"},
		{"A: B - C", "A"},                     // colon takes priority
		{"Multi - Word Title - Sub", "Multi"}, // first dash wins
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := stripSubtitle(tc.input)
			if got != tc.want {
				t.Errorf("stripSubtitle(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestBestTitleMatch(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "Completely Different Book"},
		{Title: "The Great Adventure Story"},
		{Title: "Great Adventure"},
	}

	got := metafetch.BestTitleMatch(results, "The Great Adventure")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	// With precision+recall scoring, "Great Adventure" (2/2 result words match
	// query words → precision 1.0) beats "The Great Adventure Story" (2/4 words
	// match → precision 0.5). Both have recall 1.0; "Great Adventure" wins on F1.
	if got[0].Title != "Great Adventure" {
		t.Errorf("expected 'Great Adventure' (higher precision), got %q", got[0].Title)
	}
}

func TestBestTitleMatch_NoOverlap(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "Completely Different Book"},
		{Title: "Another Unrelated Title"},
	}

	got := metafetch.BestTitleMatch(results, "Xyz Qwerty")
	if got != nil {
		t.Errorf("expected nil for no overlap, got %v", got)
	}
}

func TestBestTitleMatch_EmptyResults(t *testing.T) {
	got := metafetch.BestTitleMatch(nil, "Some Title")
	if got != nil {
		t.Errorf("expected nil for empty results, got %v", got)
	}

	got = metafetch.BestTitleMatch([]metadata.BookMetadata{}, "Some Title")
	if got != nil {
		t.Errorf("expected nil for empty slice, got %v", got)
	}
}

func TestIsGarbageValue(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"Unknown", true},
		{"unknown", true},
		{"UNKNOWN", true},
		{"narrator", true},
		{"Narrator", true},
		{"", true},
		{"n/a", true},
		{"Terry Pratchett", false},
		{"Stephen King", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := metafetch.IsGarbageValue(tc.input); got != tc.want {
				t.Errorf("metafetch.IsGarbageValue(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsBetterValue_NeverDowngrade(t *testing.T) {
	// Real author -> "Unknown" should NOT replace
	if metafetch.IsBetterValue("Terry Pratchett", "Unknown") {
		t.Error("should not replace real author with Unknown")
	}
	// Empty -> real value should replace
	if !metafetch.IsBetterValue("", "Terry Pratchett") {
		t.Error("should replace empty with real value")
	}
	// "Unknown" -> real value should replace
	if !metafetch.IsBetterValue("Unknown", "Terry Pratchett") {
		t.Error("should replace Unknown with real value")
	}
	// Real -> real is allowed
	if !metafetch.IsBetterValue("Old Title", "New Title") {
		t.Error("should allow real->real replacement")
	}
}

func TestIsBetterStringPtr_NeverDowngrade(t *testing.T) {
	real := "Real Narrator"
	// Real narrator -> "narrator" garbage should NOT replace
	if metafetch.IsBetterStringPtr(&real, "narrator") {
		t.Error("should not replace real narrator with garbage 'narrator'")
	}
	// nil -> real should replace
	if !metafetch.IsBetterStringPtr(nil, "John Smith") {
		t.Error("should replace nil with real value")
	}
	// garbage -> real should replace
	unknown := "Unknown"
	if !metafetch.IsBetterStringPtr(&unknown, "John Smith") {
		t.Error("should replace Unknown with real value")
	}
}

func TestParseSeriesFromTitle(t *testing.T) {
	tests := []struct {
		input      string
		wantSeries string
		wantPos    string
		wantTitle  string
	}{
		{"(Long Earth 05) The Long Cosmos", "Long Earth", "5", "The Long Cosmos"},
		{"(Dresden Files 01) Storm Front", "Dresden Files", "1", "Storm Front"},
		{"(Discworld 41) The Shepherd's Crown", "Discworld", "41", "The Shepherd's Crown"},
		{"(Series #3) Title", "Series", "3", "Title"},
		{"Series Name, Book 7", "Series Name", "7", ""},
		{"Just A Regular Title", "", "", ""},
		{"", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			series, pos, title := metafetch.ParseSeriesFromTitle(tc.input)
			if series != tc.wantSeries {
				t.Errorf("series: got %q, want %q", series, tc.wantSeries)
			}
			if pos != tc.wantPos {
				t.Errorf("position: got %q, want %q", pos, tc.wantPos)
			}
			if title != tc.wantTitle {
				t.Errorf("title: got %q, want %q", title, tc.wantTitle)
			}
		})
	}
}

func TestApplyMetadataToBook_NoDowngrade(t *testing.T) {
	mockDB := &database.MockStore{}
	mfs := metafetch.NewService(mockDB)

	realAuthor := "Terry Pratchett & Stephen Baxter"
	realNarrator := "Michael Fenton Stevens"
	book := &database.Book{
		ID:       "1",
		Title:    "The Long Cosmos",
		Narrator: &realNarrator,
	}
	// Simulate AuthorID being set (author resolved separately)
	_ = realAuthor

	meta := metadata.BookMetadata{
		Title:    "Unknown",
		Narrator: "narrator",
		Author:   "Unknown",
	}

	mfs.ApplyMetadataToBook(book, meta)

	// Title should NOT be replaced with "Unknown"
	if book.Title != "The Long Cosmos" {
		t.Errorf("title was downgraded to %q", book.Title)
	}
	// Narrator should NOT be replaced with garbage "narrator"
	if book.Narrator == nil || *book.Narrator != "Michael Fenton Stevens" {
		t.Errorf("narrator was downgraded to %v", book.Narrator)
	}
}

func TestRecordChangeHistory(t *testing.T) {
	var recorded []*database.MetadataChangeRecord
	mockDB := &database.MockStore{
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: "Old Author"}, nil
		},
		GetSeriesByIDFunc: func(id int) (*database.Series, error) {
			return &database.Series{ID: id, Name: "Old Series"}, nil
		},
		RecordMetadataChangeFunc: func(record *database.MetadataChangeRecord) error {
			recorded = append(recorded, record)
			return nil
		},
	}
	mfs := metafetch.NewService(mockDB)

	authorID := 1
	seriesID := 2
	book := &database.Book{
		ID:       "book1",
		Title:    "Old Title",
		AuthorID: &authorID,
		SeriesID: &seriesID,
	}
	meta := metadata.BookMetadata{
		Title:     "New Title",
		Author:    "New Author",
		Publisher: "New Publisher",
	}

	mfs.RecordChangeHistory(book, meta, "TestSource")

	if len(recorded) < 3 {
		t.Fatalf("expected at least 3 change records, got %d", len(recorded))
	}

	// Verify all records have correct source and type
	for _, r := range recorded {
		if r.Source != "TestSource" {
			t.Errorf("expected source 'TestSource', got %q", r.Source)
		}
		if r.ChangeType != "fetched" {
			t.Errorf("expected change_type 'fetched', got %q", r.ChangeType)
		}
		if r.BookID != "book1" {
			t.Errorf("expected book_id 'book1', got %q", r.BookID)
		}
	}
}

// --- scoreTitleMatch tests (Task 1) ---

func TestScoreTitleMatch_BoxSetPenalised(t *testing.T) {
	// The individual book should beat the box set even if the box set contains
	// all the query words plus a lot more.
	results := []metadata.BookMetadata{
		{Title: "The Long Earth Series 5 Books Collection Terry Pratchett and Stephen Baxter Box Set"},
		{Title: "The Long Earth", Description: "A novel.", CoverURL: "https://example.com/cover.jpg"},
	}
	got := metafetch.BestTitleMatch(results, "The Long Earth")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Title != "The Long Earth" {
		t.Errorf("expected individual book, got box set: %q", got[0].Title)
	}
}

func TestScoreTitleMatch_CollectionPenalised(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "Discworld Collection: Books 1-5"},
		{Title: "The Colour of Magic", Description: "First Discworld novel.", CoverURL: "https://cdn.example.com/cover.jpg", Narrator: "Tony Robinson"},
	}
	got := metafetch.BestTitleMatch(results, "The Colour of Magic")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Title != "The Colour of Magic" {
		t.Errorf("expected individual book, got %q", got[0].Title)
	}
}

func TestScoreTitleMatch_OmnibusPenalised(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "Foundation Omnibus: Foundation, Foundation and Empire, Second Foundation"},
		{Title: "Foundation", Author: "Isaac Asimov", Description: "The galactic empire crumbles.", ISBN: "9780553293357"},
	}
	got := metafetch.BestTitleMatch(results, "Foundation")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Title != "Foundation" {
		t.Errorf("expected 'Foundation', got %q", got[0].Title)
	}
}

func TestScoreTitleMatch_ExactMatchWins(t *testing.T) {
	// A result with an exact title match should always win.
	results := []metadata.BookMetadata{
		{Title: "The Long Cosmos and Other Stories Collection"},
		{Title: "The Long Cosmos", Description: "Book 5 of the Long Earth series.", CoverURL: "https://example.com/c.jpg"},
	}
	got := metafetch.BestTitleMatch(results, "The Long Cosmos")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Title != "The Long Cosmos" {
		t.Errorf("expected 'The Long Cosmos', got %q", got[0].Title)
	}
}

func TestScoreTitleMatch_BelowThresholdReturnsNil(t *testing.T) {
	// A result that shares no significant words with the query should be
	// rejected (score below minimum threshold).
	results := []metadata.BookMetadata{
		{Title: "A Completely Unrelated Title About Cooking"},
	}
	got := metafetch.BestTitleMatch(results, "The Long Cosmos")
	if got != nil {
		t.Errorf("expected nil (below quality threshold), got %v", got)
	}
}

func TestScoreTitleMatch_RichMetadataBonus(t *testing.T) {
	// When two results score similarly on title, the one with richer
	// metadata (description + cover) should win.
	results := []metadata.BookMetadata{
		{Title: "Dune"},
		{Title: "Dune", Description: "Paul Atreides travels to Arrakis.", CoverURL: "https://example.com/dune.jpg", ISBN: "9780441013593"},
	}
	got := metafetch.BestTitleMatch(results, "Dune")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	// The richer result is at index 1; it should be preferred.
	if got[0].Description == "" {
		t.Errorf("expected the richer result (with description), got title-only result")
	}
}

func TestScoreTitleMatch_LengthPenalty(t *testing.T) {
	// A very long title with the search words buried inside should score
	// lower than a concise matching title.
	results := []metadata.BookMetadata{
		// 10-word title containing all query words
		{Title: "Ender Game Complete Guide Expanded Universe Fan Edition Deluxe Version"},
		// Concise exact match
		{Title: "Ender's Game", Description: "Military sci-fi classic.", CoverURL: "https://example.com/enders.jpg"},
	}
	got := metafetch.BestTitleMatch(results, "Ender's Game")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Title != "Ender's Game" {
		t.Errorf("expected concise match, got %q", got[0].Title)
	}
}

func TestScoreTitleMatch_NDigitBooksPenalised(t *testing.T) {
	// "5 books" pattern should trigger the compilation penalty.
	results := []metadata.BookMetadata{
		{Title: "Hitchhiker 5 Books Complete Collection Douglas Adams"},
		{Title: "The Hitchhiker's Guide to the Galaxy", Description: "Don't panic.", CoverURL: "https://example.com/h2g2.jpg"},
	}
	got := metafetch.BestTitleMatch(results, "The Hitchhiker's Guide to the Galaxy")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if strings.Contains(strings.ToLower(got[0].Title), "books") {
		t.Errorf("compilation result should not win: got %q", got[0].Title)
	}
}

func TestScoreTitleMatch_MultipleVariants(t *testing.T) {
	// bestTitleMatch accepts multiple title variants; scoring should use
	// the union of words from all variants.
	results := []metadata.BookMetadata{
		{Title: "The Fellowship of the Ring Box Set"},
		{Title: "Fellowship of the Ring", Description: "Part one of LOTR.", CoverURL: "https://example.com/lotr.jpg"},
	}
	// Provide both a cleaned and raw title variant.
	got := metafetch.BestTitleMatch(results, "Fellowship of the Ring", "The Fellowship of the Ring")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if strings.Contains(strings.ToLower(got[0].Title), "box set") {
		t.Errorf("box set should not win: got %q", got[0].Title)
	}
}

func TestApplySeriesPositionFilter_RejectsWrongPosition(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "The Long Cosmos", SeriesPosition: "3"}, // wrong — book is #5
	}
	got := metafetch.ApplySeriesPositionFilter(results, 5)
	if got != nil {
		t.Errorf("expected nil (wrong position), got %v", got)
	}
}

func TestApplySeriesPositionFilter_AcceptsCorrectPosition(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "The Long Cosmos", SeriesPosition: "5"},
	}
	got := metafetch.ApplySeriesPositionFilter(results, 5)
	if got == nil {
		t.Fatal("expected result, got nil")
	}
	if got[0].SeriesPosition != "5" {
		t.Errorf("expected position 5, got %q", got[0].SeriesPosition)
	}
}

func TestApplySeriesPositionFilter_NoKnownPosition(t *testing.T) {
	results := []metadata.BookMetadata{
		{Title: "Some Book", SeriesPosition: "3"},
	}
	// knownPosition == 0 means "we don't know" — pass through unchanged
	got := metafetch.ApplySeriesPositionFilter(results, 0)
	if len(got) != 1 {
		t.Errorf("expected 1 result, got %d", len(got))
	}
}

func TestApplySeriesPositionFilter_NoPositionInResult(t *testing.T) {
	// If the result has no SeriesPosition, we can't reject it on position grounds.
	results := []metadata.BookMetadata{
		{Title: "The Long Cosmos"},
	}
	got := metafetch.ApplySeriesPositionFilter(results, 5)
	if len(got) != 1 {
		t.Errorf("expected 1 result (no position to reject), got %d", len(got))
	}
}

func TestFetchMetadataForBook_BoxSetRejected_IndividualBookApplied(t *testing.T) {
	setupGlobalStoreForTest(t)

	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "The Long Cosmos"}, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
		RecordMetadataChangeFunc: func(record *database.MetadataChangeRecord) error {
			return nil
		},
	}

	// Source returns a box set first, then the real book.
	src := &mockMetadataSource{
		name: "TestSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			return []metadata.BookMetadata{
				// Box set — should be penalised and rejected.
				{
					Title:  "The Long Earth Series 5 Books Collection Terry Pratchett and Stephen Baxter Box Set",
					Author: "Terry Pratchett",
				},
				// Individual book — should win.
				{
					Title:       "The Long Cosmos",
					Author:      "Terry Pratchett",
					Description: "The fifth book in the Long Earth series.",
					CoverURL:    "https://example.com/long-cosmos.jpg",
					PublishYear: 2016,
				},
			}, nil
		},
	}

	mfs := metafetch.NewService(mockDB)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})

	resp, err := mfs.FetchMetadataForBook(context.Background(), "book1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	// The applied book title should be from the individual book, not the box set.
	if resp.Book == nil {
		t.Fatal("expected non-nil Book in response")
	}
	if strings.Contains(strings.ToLower(resp.Book.Title), "collection") ||
		strings.Contains(strings.ToLower(resp.Book.Title), "box set") {
		t.Errorf("box set was applied to book: %q", resp.Book.Title)
	}
	if resp.Book.Title != "The Long Cosmos" {
		t.Errorf("expected title 'The Long Cosmos', got %q", resp.Book.Title)
	}
}

// ---------------------------------------------------------------------------
// WriteBackMetadataForBook tests
// ---------------------------------------------------------------------------

func TestWriteBackMetadataForBook_NotFound(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", "missing-id").Return(nil, nil)

	svc := metafetch.NewService(mockStore)
	_, err := svc.WriteBackMetadataForBook("missing-id")
	if err == nil {
		t.Fatal("expected error for missing book")
	}
	if !strings.Contains(err.Error(), "audiobook not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWriteBackMetadataForBook_SingleFile(t *testing.T) {
	authorID := 42
	year := 2022
	book := &database.Book{
		ID:                   "01JTEST000000000000000001",
		Title:                "Test Book",
		AuthorID:             &authorID,
		AudiobookReleaseYear: &year,
		FilePath:             "/tmp/test-nonexistent.m4b",
	}
	author := &database.Author{ID: 42, Name: "Test Author"}

	mockStore := mocks.NewMockStore(t)
	mockStore.On("GetBookByID", book.ID).Return(book, nil)
	mockStore.On("GetBookAuthors", book.ID).Return([]database.BookAuthor{
		{BookID: book.ID, AuthorID: 42, Role: "author", Position: 0},
	}, nil)
	mockStore.On("GetAuthorByID", 42).Return(author, nil)
	mockStore.On("GetBookNarrators", book.ID).Return([]database.BookNarrator{}, nil)
	mockStore.On("GetBookFiles", book.ID).Return([]database.BookFile{}, nil)
	mockStore.On("GetBookFileByPath", book.FilePath).Return((*database.BookFile)(nil), nil)
	mockStore.On("RecordMetadataChange", mock.AnythingOfType("*database.MetadataChangeRecord")).Return(nil)
	// isProtectedPath (SERVER-GLOBAL-STORE-AUDIT phase 4) consults
	// GetAllImportPaths via mfs.db before any write.
	mockStore.On("GetAllImportPaths").Return([]database.ImportPath{}, nil).Maybe()

	svc := metafetch.NewService(mockStore)
	// WriteMetadataToFile will fail because the file doesn't exist, but the
	// function should return (0, nil) — failures are logged not returned.
	count, err := svc.WriteBackMetadataForBook(book.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected written count 0, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Scoring improvements tests
// ---------------------------------------------------------------------------

func TestBestTitleMatchWithContext_AuthorTiebreaking(t *testing.T) {
	// When two results have equal base title scores, the one matching the
	// book's author should score higher and be selected.
	results := []metadata.BookMetadata{
		{Title: "Dune", Author: "Frank Herbert", Narrator: "Scott Brick"},
		{Title: "Dune", Author: "Someone Else", Narrator: "Jane Doe"},
	}

	got := metafetch.BestTitleMatchWithContext(results, "Frank Herbert", "", "Dune")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if got[0].Author != "Frank Herbert" {
		t.Errorf("expected author 'Frank Herbert' to win tiebreak, got %q", got[0].Author)
	}
}

func TestBestTitleMatchWithContext_AuthorTiebreaking_Substring(t *testing.T) {
	// Author matching uses substring containment, so partial matches work.
	results := []metadata.BookMetadata{
		{Title: "The Colour of Magic", Author: "Terry Pratchett", Narrator: "Nigel Planer"},
		{Title: "The Colour of Magic", Author: "Unknown Publisher", Narrator: "Nigel Planer"},
	}

	got := metafetch.BestTitleMatchWithContext(results, "Pratchett", "", "The Colour of Magic")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Author != "Terry Pratchett" {
		t.Errorf("expected 'Terry Pratchett' (substring match), got %q", got[0].Author)
	}
}

func TestBestTitleMatchWithContext_MissingAuthorPenalty(t *testing.T) {
	// Results with no author should get 0.75x when book's author is known,
	// making them lose to a result with a matching author.
	results := []metadata.BookMetadata{
		{Title: "Foundation", Author: "", Narrator: "Scott Brick"},             // no author → 0.75x
		{Title: "Foundation", Author: "Isaac Asimov", Narrator: "Scott Brick"}, // matching → 1.5x
	}

	got := metafetch.BestTitleMatchWithContext(results, "Isaac Asimov", "", "Foundation")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Author != "Isaac Asimov" {
		t.Errorf("expected result with author to win over missing-author result, got %q", got[0].Author)
	}
}

func TestBestTitleMatchWithContext_MissingAuthorPenalty_NoBookAuthor(t *testing.T) {
	// When book's author is unknown (empty), no author penalty should apply,
	// so results with or without author are scored the same on that axis.
	withAuthor := metadata.BookMetadata{Title: "Foundation", Author: "Isaac Asimov", Narrator: "Scott Brick"}
	withoutAuthor := metadata.BookMetadata{Title: "Foundation", Author: "", Narrator: "Scott Brick"}

	// Both have same narrator so narrator boost is the same.
	// With empty bookAuthor, neither gets author boost/penalty.
	gotWith := metafetch.BestTitleMatchWithContext([]metadata.BookMetadata{withAuthor}, "", "", "Foundation")
	gotWithout := metafetch.BestTitleMatchWithContext([]metadata.BookMetadata{withoutAuthor}, "", "", "Foundation")

	// Both should return results (no penalty kills them)
	if gotWith == nil {
		t.Fatal("expected result with author, got nil")
	}
	if gotWithout == nil {
		t.Fatal("expected result without author, got nil")
	}
}

func TestBestTitleMatchWithContext_NarratorBoost(t *testing.T) {
	// Results with narrator info get 1.15x, without get 0.85x.
	// Two identical results except for narrator presence.
	results := []metadata.BookMetadata{
		{Title: "Neuromancer", Author: "William Gibson"},                             // no narrator → 0.85x
		{Title: "Neuromancer", Author: "William Gibson", Narrator: "Robertson Dean"}, // narrator → 1.15x
	}

	got := metafetch.BestTitleMatchWithContext(results, "", "", "Neuromancer")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Narrator != "Robertson Dean" {
		t.Errorf("expected result with narrator to win, got narrator=%q", got[0].Narrator)
	}
}

func TestBestTitleMatchWithContext_NarratorMatchBoost(t *testing.T) {
	// When book's narrator is known, a result matching it gets 1.3x on top of 1.15x.
	results := []metadata.BookMetadata{
		{Title: "Dune", Author: "Frank Herbert", Narrator: "Wrong Narrator"},
		{Title: "Dune", Author: "Frank Herbert", Narrator: "Scott Brick"},
	}

	got := metafetch.BestTitleMatchWithContext(results, "Frank Herbert", "Scott Brick", "Dune")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Narrator != "Scott Brick" {
		t.Errorf("expected matching narrator 'Scott Brick' to win, got %q", got[0].Narrator)
	}
}

func TestBestTitleMatchWithContext_AudiobookSourcePreference(t *testing.T) {
	// Audible-like result (with narrator) should outrank Open Library-like
	// result (no narrator) when both match on title and author.
	results := []metadata.BookMetadata{
		// Open Library-like: no narrator
		{Title: "The Name of the Wind", Author: "Patrick Rothfuss", Description: "A novel."},
		// Audible-like: has narrator
		{Title: "The Name of the Wind", Author: "Patrick Rothfuss", Narrator: "Nick Podehl", Description: "A novel."},
	}

	got := metafetch.BestTitleMatchWithContext(results, "Patrick Rothfuss", "", "The Name of the Wind")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Narrator == "" {
		t.Error("expected Audible-like result (with narrator) to outrank Open Library (no narrator)")
	}
	if got[0].Narrator != "Nick Podehl" {
		t.Errorf("expected narrator 'Nick Podehl', got %q", got[0].Narrator)
	}
}

func TestBestTitleMatchWithContext_AuthorMismatchPenalty(t *testing.T) {
	// A result with a non-matching author gets 0.7x, which is worse than
	// a result with a matching author at 1.5x.
	results := []metadata.BookMetadata{
		{Title: "Storm Front", Author: "Jim Butcher", Narrator: "James Marsters"},
		{Title: "Storm Front", Author: "R.S. Belcher", Narrator: "Bronson Pinchot"},
	}

	got := metafetch.BestTitleMatchWithContext(results, "Jim Butcher", "", "Storm Front")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Author != "Jim Butcher" {
		t.Errorf("expected matching author to win over mismatched author, got %q", got[0].Author)
	}
}

func TestSearchMetadataForBook_SeriesBoost(t *testing.T) {
	// Results matching the search series should get 1.4x boost.
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Storm Front"}, nil
		},
	}

	src := &mockMetadataSource{
		name: "TestSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			return []metadata.BookMetadata{
				{Title: "Storm Front", Author: "Jim Butcher", Series: "The Dresden Files", Narrator: "James Marsters"},
				// Different author so dedup key (title|author) is unique
				{Title: "Storm Front", Author: "Jim Butcher Jr", Series: "Unrelated Series", Narrator: "James Marsters"},
			}, nil
		},
	}

	mfs := metafetch.NewService(mockDB)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})

	// Pass series as the third authorHint parameter
	resp, err := mfs.SearchMetadataForBook("book1", "Storm Front", "Jim Butcher", "", "The Dresden Files")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(resp.Results))
	}
	// The result with matching series should be ranked first (higher score)
	if resp.Results[0].Series != "The Dresden Files" {
		t.Errorf("expected series 'The Dresden Files' to rank first, got %q", resp.Results[0].Series)
	}
	if resp.Results[0].Score <= resp.Results[1].Score {
		t.Errorf("expected series-matching result to have higher score: got %.3f <= %.3f",
			resp.Results[0].Score, resp.Results[1].Score)
	}
}

func TestIsGarbageValue_ExtendedValues(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Core garbage values
		{"Various", true},
		{"VARIOUS", true},
		{"various", true},
		{"None", true},
		{"null", true},
		{"undefined", true},
		{"test", true},
		{"Untitled", true},
		{"No Title", true},
		{"No Author", true},
		{"Various Authors", true},
		{"Various Artists", true},
		// HTML/error fragments
		{"<html>stuff</html>", true},
		{"<!DOCTYPE html>", true},
		{"403 Forbidden", true},
		{"error occurred", true},
		// Whitespace-only
		{"   ", true},
		// Real values should not be garbage
		{"J.R.R. Tolkien", false},
		{"Neil Gaiman", false},
		{"The Hobbit", false},
		{"Audible Studios", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := metafetch.IsGarbageValue(tc.input); got != tc.want {
				t.Errorf("metafetch.IsGarbageValue(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestSearchMetadataForBook_ResultLimitCap50(t *testing.T) {
	// Verify that results are capped at 50, not 10.
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Test"}, nil
		},
	}

	// Generate 60 unique results
	src := &mockMetadataSource{
		name: "BigSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			var results []metadata.BookMetadata
			for i := 0; i < 60; i++ {
				results = append(results, metadata.BookMetadata{
					Title:    fmt.Sprintf("Test Book %d", i),
					Author:   fmt.Sprintf("Author %d", i),
					Narrator: "Some Narrator",
				})
			}
			return results, nil
		},
	}

	mfs := metafetch.NewService(mockDB)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})

	resp, err := mfs.SearchMetadataForBook("book1", "Test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) > 50 {
		t.Errorf("expected at most 50 results, got %d", len(resp.Results))
	}
	// Ensure we get more than 10 (the old limit) — cap is 50
	if len(resp.Results) < 11 {
		t.Errorf("expected more than 10 results (cap is 50), got %d", len(resp.Results))
	}
}

func TestSearchMetadataForBook_GarbageAuthorTreatedAsEmpty(t *testing.T) {
	// When book's author resolves to a garbage value, it should be treated
	// as empty and not used for scoring (no author penalty applied).
	authorID := 1
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Test Book", AuthorID: &authorID}, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return &database.Author{ID: id, Name: "Unknown"}, nil // garbage
		},
	}

	src := &mockMetadataSource{
		name: "TestSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			return []metadata.BookMetadata{
				{Title: "Test Book", Author: ""}, // no author
			}, nil
		},
	}

	mfs := metafetch.NewService(mockDB)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})

	resp, err := mfs.SearchMetadataForBook("book1", "Test Book")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The result should not have been penalized for missing author since
	// the book's own author is garbage. At least 1 result should be returned.
	if len(resp.Results) == 0 {
		t.Error("expected at least 1 result when book's author is garbage (no penalty)")
	}
}

func TestSearchMetadataForBook_GarbageNarratorTreatedAsEmpty(t *testing.T) {
	// Garbage narrator on the book should not trigger narrator-match scoring.
	narrator := "Narrator" // garbage value
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Test Book", Narrator: &narrator}, nil
		},
	}

	src := &mockMetadataSource{
		name: "TestSource",
		searchByTitleFunc: func(title string) ([]metadata.BookMetadata, error) {
			return []metadata.BookMetadata{
				// Different authors so dedup key (title|author) is unique
				{Title: "Test Book", Author: "Author A", Narrator: "Narrator"}, // also garbage narrator
				{Title: "Test Book", Author: "Author B", Narrator: "Real Person"},
			}, nil
		},
	}

	mfs := metafetch.NewService(mockDB)
	mfs.SetOverrideSources([]metadata.MetadataSource{src})

	resp, err := mfs.SearchMetadataForBook("book1", "Test Book")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Both results should be present; the garbage narrator match should not
	// have gotten an unfair boost.
	if len(resp.Results) < 2 {
		t.Errorf("expected 2 results, got %d", len(resp.Results))
	}
}

func TestScoreOneResult_BasicScoring(t *testing.T) {
	searchWords := map[string]bool{"dune": true}

	tests := []struct {
		name   string
		result metadata.BookMetadata
		minExp float64
		maxExp float64
	}{
		{
			name:   "exact single-word match",
			result: metadata.BookMetadata{Title: "Dune"},
			minExp: 0.8, // F1=1.0, no bonus
			maxExp: 1.2,
		},
		{
			name:   "no overlap",
			result: metadata.BookMetadata{Title: "Foundation"},
			minExp: 0,
			maxExp: 0.01,
		},
		{
			name:   "match with rich metadata",
			result: metadata.BookMetadata{Title: "Dune", Description: "A novel.", CoverURL: "http://x", ISBN: "123"},
			minExp: 1.0, // F1=1.0 + bonus
			maxExp: 1.3,
		},
		{
			name:   "compilation penalty",
			result: metadata.BookMetadata{Title: "Dune Complete Collection Box Set"},
			minExp: 0,
			maxExp: 0.2, // heavily penalized
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score := metafetch.ScoreOneResult(tc.result, searchWords)
			if score < tc.minExp || score > tc.maxExp {
				t.Errorf("metafetch.ScoreOneResult() = %.3f, want [%.3f, %.3f]", score, tc.minExp, tc.maxExp)
			}
		})
	}
}

func TestBestTitleMatchWithContext_AllMultipliersCombine(t *testing.T) {
	// Verify that author match (1.5x), narrator presence (1.15x), and narrator
	// match (1.3x) all stack to create a significant scoring advantage.
	ideal := metadata.BookMetadata{
		Title:    "The Hobbit",
		Author:   "J.R.R. Tolkien",
		Narrator: "Martin Freeman",
	}
	poor := metadata.BookMetadata{
		Title:  "The Hobbit",
		Author: "Wrong Person",
		// no narrator
	}

	results := []metadata.BookMetadata{poor, ideal}
	got := metafetch.BestTitleMatchWithContext(results, "J.R.R. Tolkien", "Martin Freeman", "The Hobbit")
	if got == nil {
		t.Fatal("expected a match, got nil")
	}
	if got[0].Author != "J.R.R. Tolkien" {
		t.Errorf("expected ideal result with all boosts to win, got author=%q narrator=%q",
			got[0].Author, got[0].Narrator)
	}
}
