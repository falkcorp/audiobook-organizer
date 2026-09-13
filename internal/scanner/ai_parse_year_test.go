// file: internal/scanner/ai_parse_year_test.go
// version: 1.0.0
// guid: 8e2b4f61-3c7a-4d95-b0e8-5a1f9c6d2e47
// last-edited: 2026-09-13

package scanner

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/require"
)

// The batch AI parse used to decode the model's year and drop it. It now
// gap-fills audiobook_release_year under the same rule as the series position:
// only into an empty, unlocked column, never over an existing year.

func intPtr(v int) *int { return &v }

// aiYearRow creates a row from seed, runs the AI save with the given model
// year, and returns the stored row.
func aiYearRow(t *testing.T, seed *database.Book, year int, lockYear bool) *database.Book {
	t.Helper()
	store := aiSaveStore(t)
	seed.FilePath = "/library/Author/Book/book.m4b"
	row, err := store.CreateBook(seed)
	require.NoError(t, err)
	if lockYear {
		lockBlank(t, store, row.ID, database.FieldKeyAudiobookReleaseYear)
	}
	require.NoError(t, saveAI(row.ID, &Book{FilePath: row.FilePath, Year: year}))
	got, err := store.GetBookByID(row.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	return got
}

func TestSaveAIFields_FillsAnEmptyReleaseYear(t *testing.T) {
	got := aiYearRow(t, &database.Book{Title: "T"}, 2015, false)
	require.NotNil(t, got.AudiobookReleaseYear)
	require.Equal(t, 2015, *got.AudiobookReleaseYear)
}

// A stored pointer to 0 is empty to every year reader in the tree, so it is
// empty here too.
func TestSaveAIFields_FillsAZeroReleaseYear(t *testing.T) {
	got := aiYearRow(t, &database.Book{Title: "T", AudiobookReleaseYear: intPtr(0)}, 2015, false)
	require.NotNil(t, got.AudiobookReleaseYear)
	require.Equal(t, 2015, *got.AudiobookReleaseYear)
}

func TestSaveAIFields_DoesNotOverwriteAnExistingReleaseYear(t *testing.T) {
	got := aiYearRow(t, &database.Book{Title: "T", AudiobookReleaseYear: intPtr(2001)}, 2015, false)
	require.NotNil(t, got.AudiobookReleaseYear)
	require.Equal(t, 2001, *got.AudiobookReleaseYear, "an existing year must never be overwritten")
}

func TestSaveAIFields_HonorsALockedBlankReleaseYear(t *testing.T) {
	got := aiYearRow(t, &database.Book{Title: "T"}, 2015, true)
	require.Nil(t, got.AudiobookReleaseYear, "the user locked the year blank; the AI's year must not land")
}

func TestSaveAIFields_SkipsImplausibleYears(t *testing.T) {
	next := time.Now().Year() + 2
	for _, y := range []int{0, 1, 999, 3024, next, -5} {
		got := aiYearRow(t, &database.Book{Title: "T"}, y, false)
		require.Nil(t, got.AudiobookReleaseYear, "year %d must be skipped", y)
	}
}

func TestPlausibleAIYear_Bounds(t *testing.T) {
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	require.True(t, plausibleAIYear(1000, now))
	require.True(t, plausibleAIYear(2027, now), "next year is allowed for pre-release listings")
	require.False(t, plausibleAIYear(999, now))
	require.False(t, plausibleAIYear(2028, now))
}

// yearByFileParser answers each filename with the year mapped to it.
type yearByFileParser map[string]int

func (p yearByFileParser) ParseBatch(_ context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	out := make([]*ai.ParsedMetadata, len(filenames))
	for i, f := range filenames {
		out[i] = &ai.ParsedMetadata{Year: p[f]}
	}
	return out, nil
}

// End to end through the batch phase: the year has to survive the carry from
// ParsedMetadata to Book and then pass the saver's rules. Four books in one
// batch; only the empty, unlocked one with a plausible year gets a year.
func TestRunAIBatchPhase_SavesYearOnlyForEligibleBooks(t *testing.T) {
	store := aiSaveStore(t)

	type fixture struct {
		path   string
		seed   *int
		lock   bool
		model  int
		expect *int
	}
	fixtures := []fixture{
		{path: "/lib/empty.m4b", model: 2010, expect: intPtr(2010)},
		{path: "/lib/existing.m4b", seed: intPtr(1999), model: 2010, expect: intPtr(1999)},
		{path: "/lib/locked.m4b", lock: true, model: 2010},
		{path: "/lib/bogus.m4b", model: 3024},
	}

	parser := yearByFileParser{}
	books := make([]Book, len(fixtures))
	cands := make([]int, len(fixtures))
	ids := make(map[string]string, len(fixtures))
	for i, f := range fixtures {
		row, err := store.CreateBook(&database.Book{FilePath: f.path, Title: "T", AudiobookReleaseYear: f.seed})
		require.NoError(t, err)
		if f.lock {
			lockBlank(t, store, row.ID, database.FieldKeyAudiobookReleaseYear)
		}
		ids[f.path] = row.ID
		books[i] = Book{FilePath: f.path}
		cands[i] = i
		// The phase sends base names to the model, not full paths.
		parser[filepath.Base(f.path)] = f.model
	}

	var mu sync.Mutex
	save := func(ctx context.Context, b *Book) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return saveAIFieldsToPrimary(ctx, ids[b.FilePath], b)
	}
	summary := runAIBatchPhase(context.Background(), parser, books, cands, logger.New("test"), save)
	require.Equal(t, len(fixtures), summary.BooksParsed)
	require.Zero(t, summary.SavesFailed)

	for _, f := range fixtures {
		got, err := store.GetBookByID(ids[f.path])
		require.NoError(t, err)
		if f.expect == nil {
			require.Nil(t, got.AudiobookReleaseYear, f.path)
			continue
		}
		require.NotNil(t, got.AudiobookReleaseYear, f.path)
		require.Equal(t, *f.expect, *got.AudiobookReleaseYear, f.path)
	}
}
