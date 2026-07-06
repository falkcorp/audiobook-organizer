// file: internal/dedup/engine_unit_test.go
// version: 1.1.0
// guid: f1a2b3c4-d5e6-7890-abcd-1234567890ab
// last-edited: 2026-07-05

package dedup

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- NormalizeAuthorName edge cases (supplements author_test.go) ---

func TestNormalizeAuthorName_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"single initial", "J.", "J."},
		{"single name no spaces", "Voltaire", "Voltaire"},
		{"tabs and newlines", "James\t\n S.A. \t Corey", "James S. A. Corey"},
		{"trailing dot only", ".", "."},
		{"unicode name preserved", "José Saramago", "José Saramago"},
		{"three collapsed initials", "J.R.R. Tolkien", "J. R. R. Tolkien"},
		{"all whitespace", "   \t  ", ""},
		{"leading/trailing with initials", "  R.A. Salvatore  ", "R. A. Salvatore"},
		{"already expanded initials", "J. K. Rowling", "J. K. Rowling"},
		{"lowercase initials ignored", "j.k. rowling", "j.k. rowling"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeAuthorName(tt.input)
			assert.Equal(t, tt.expected, got, "NormalizeAuthorName(%q)", tt.input)
		})
	}
}

// --- IsProductionCompany edge cases ---

func TestIsProductionCompany_EdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect bool
	}{
		{"exact match with whitespace", "  Podium Audio  ", true},
		{"case insensitive", "BRILLIANCE AUDIO", true},
		{"mixed case", "Recorded Books", true},
		{"suffix theater", "Custom Theater", true},
		{"suffix theatre", "Custom Theatre", true},
		{"not a company", "John Theater Smith", false}, // "theater" is not a suffix here — wait, it IS a suffix
		{"partial match not enough", "Audio", false},
		{"empty string", "", false},
		{"substring match should fail", "Podium", false},
		{"real author name", "Stephen King", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsProductionCompany(tt.input)
			assert.Equal(t, tt.expect, got, "IsProductionCompany(%q)", tt.input)
		})
	}
}

// --- authorNameScore ---

func TestAuthorNameScore_Preferences(t *testing.T) {
	// Lower score = better canonical candidate
	tests := []struct {
		name   string
		better string // should have lower score
		worse  string // should have higher score
	}{
		{"full name over initials", "James Corey", "J. Corey"},
		{"no slash over slash", "David Kushner", "David Kushner/Wil Wheaton"},
		{"no parens over parens", "Natalie Maher", "Natalie Maher (aka Thundamoo)"},
		{"proper case over all caps", "James Corey", "JAMES COREY"},
		{"no dash over dash", "Neal Stephenson", "Neal Stephenson - Snow Crash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			betterScore := authorNameScore(tt.better)
			worseScore := authorNameScore(tt.worse)
			assert.Less(t, betterScore, worseScore,
				"expected %q (score %d) < %q (score %d)",
				tt.better, betterScore, tt.worse, worseScore)
		})
	}
}

// --- CheckBook error handling ---

func TestCheckBook_GetBookByID_Error(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return nil, fmt.Errorf("database connection lost")
	}

	merged, err := engine.CheckBook(context.Background(), "BOOK_X")
	assert.False(t, merged)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database connection lost")
}

func TestCheckBook_BookNotFound(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return nil, nil
	}

	merged, err := engine.CheckBook(context.Background(), "NONEXISTENT")
	assert.False(t, merged)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCheckBook_CancelledContext(t *testing.T) {
	engine, _, _ := setupTestEngine(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	merged, err := engine.CheckBook(ctx, "BOOK_1")
	assert.False(t, merged)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// --- CheckBook with store failures in sub-checks ---

func TestCheckBook_FileHashCheckError_ContinuesGracefully(t *testing.T) {
	// When GetBookByFileHash fails, CheckBook should log the error and
	// continue to the ISBN/title checks rather than returning an error.
	engine, mock, es := setupTestEngine(t)

	authorID := 1
	book := &database.Book{
		ID:       "BOOK_1",
		Title:    "Test Book",
		AuthorID: &authorID,
		FileHash: strPtr("somehash"),
	}

	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return book, nil
	}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		return &database.Author{ID: 1, Name: "Test Author"}, nil
	}
	mock.GetBookByFileHashFunc = func(hash string) (*database.Book, error) {
		return nil, fmt.Errorf("hash lookup failed")
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return nil, fmt.Errorf("file lookup failed")
	}
	mock.GetBooksByAuthorIDCoreFunc = func(authorID int) ([]database.BookCore, error) {
		return []database.BookCore{book.Core()}, nil // only itself
	}
	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return nil, nil
	}

	// Should NOT return an error — sub-check errors are logged, not propagated
	merged, err := engine.CheckBook(context.Background(), "BOOK_1")
	assert.False(t, merged)
	assert.NoError(t, err)

	// No candidates should exist
	_, total, _ := es.ListCandidates(database.CandidateFilter{EntityType: "book"})
	assert.Equal(t, 0, total)
}

// --- FindDuplicateAuthors grouping logic ---

func TestFindDuplicateAuthors_InitialsVsFullName(t *testing.T) {
	authors := []database.Author{
		{ID: 1, Name: "J. K. Rowling"},
		{ID: 2, Name: "J.K. Rowling"},
		{ID: 3, Name: "Joanne K. Rowling"},
	}
	bookCountFn := func(id int) int { return 1 }

	groups := FindDuplicateAuthors(authors, 0.9, bookCountFn)
	require.GreaterOrEqual(t, len(groups), 1, "should find at least one duplicate group")

	// All three should be in one group
	allIDs := map[int]bool{}
	for _, g := range groups {
		ids := map[int]bool{g.Canonical.ID: true}
		for _, v := range g.Variants {
			ids[v.ID] = true
		}
		// Find the group containing ID 1
		if ids[1] {
			allIDs = ids
		}
	}
	assert.True(t, allIDs[1] && allIDs[2], "IDs 1 and 2 should be in the same group")
}

func TestFindDuplicateAuthors_DirtyNamesSkipped(t *testing.T) {
	authors := []database.Author{
		{ID: 1, Name: "Brandon Sanderson"},
		{ID: 2, Name: "Brandon Sanderson - Mistborn"}, // dirty: contains " - "
		{ID: 3, Name: "Penguin Random House"},         // dirty: publisher prefix
	}
	bookCountFn := func(id int) int { return 1 }

	groups := FindDuplicateAuthors(authors, 0.9, bookCountFn)

	// Dirty names should NOT be grouped with clean names
	for _, g := range groups {
		if g.Canonical.ID == 1 {
			for _, v := range g.Variants {
				assert.NotEqual(t, 2, v.ID, "dirty name should not be grouped with clean name")
				assert.NotEqual(t, 3, v.ID, "publisher should not be grouped with author")
			}
		}
	}
}

func TestFindDuplicateAuthors_NoFalsePositives_DifferentLastNames(t *testing.T) {
	// Authors with same first name but different last names should NOT be grouped
	authors := []database.Author{
		{ID: 1, Name: "Michael Grant"},
		{ID: 2, Name: "Michael Angel"},
		{ID: 3, Name: "Michael Troughton"},
	}
	bookCountFn := func(id int) int { return 1 }

	groups := FindDuplicateAuthors(authors, 0.9, bookCountFn)

	// None of these should be grouped together
	for _, g := range groups {
		assert.Empty(t, g.Variants,
			"authors with different last names should not be grouped: %s has variants",
			g.Canonical.Name)
	}
}

// --- SplitCompositeAuthorName edge cases ---

func TestSplitCompositeAuthorName_AndSeparator(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect []string
	}{
		{"and separator", "James Patterson and Bill Clinton", []string{"James Patterson", "Bill Clinton"}},
		{"ampersand separator", "Penn Jillette & Carter Beats", []string{"Penn Jillette", "Carter Beats"}},
		{"semicolon", "Terry Pratchett; Neil Gaiman", []string{"Terry Pratchett", "Neil Gaiman"}},
		{"aka pattern not split", "Natalie Maher (aka Thundamoo)", nil},
		{"single author", "Brandon Sanderson", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitCompositeAuthorName(tt.input)
			if tt.expect == nil {
				assert.Nil(t, got, "SplitCompositeAuthorName(%q) should be nil", tt.input)
			} else {
				assert.Equal(t, tt.expect, got, "SplitCompositeAuthorName(%q)", tt.input)
			}
		})
	}
}

// --- seriesNumberOf with structured metadata ---

func TestSeriesNumberOf_StructuredField(t *testing.T) {
	seq := 5
	book := &database.Book{
		ID:             "BOOK_1",
		Title:          "Foundation Book 3", // title says 3 but structured says 5
		SeriesSequence: &seq,
	}
	// SeriesSequence should take priority over title extraction
	got := seriesNumberOf(book)
	assert.Equal(t, "5", got, "structured SeriesSequence should take priority")
}

func TestSeriesNumberOf_FallbackToTitle(t *testing.T) {
	book := &database.Book{
		ID:    "BOOK_1",
		Title: "Foundation Book 3",
	}
	got := seriesNumberOf(book)
	assert.Equal(t, "3", got, "should fall back to title extraction")
}

func TestSeriesNumberOf_NoNumber(t *testing.T) {
	book := &database.Book{
		ID:    "BOOK_1",
		Title: "Foundation",
	}
	got := seriesNumberOf(book)
	assert.Equal(t, "", got, "should return empty for no series number")
}
