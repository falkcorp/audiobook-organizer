// file: cmd/seed.go
// version: 1.3.1
// guid: 7d2e9a4f-1b85-4c63-9f0a-3e8d7b2c1f56
// last-edited: 2026-07-16
//
// `seed` populates a fresh database with synthetic books for local
// development. Use it after `make build` so a dev can hit `make run`
// and have a populated UI without scanning real files.

package cmd

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"
)

var seedCount int
var seedAuthors int
var seedSeries int
var seedReset bool

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Populate the database with synthetic books for local dev",
	Long: `Populate the database with N synthetic books spread across a small
number of authors and series. Useful for booting the UI against an
empty database without scanning real files.

Example:
  audiobook-organizer seed --count 50 --authors 5 --series 8

Each book gets a fake file path under ROOT_DIR/seed/<author>/<title>.<format>
— no actual files are written, only DB rows.`,
	RunE: runSeed,
}

func init() {
	seedCmd.Flags().IntVar(&seedCount, "count", 50, "number of books to create")
	seedCmd.Flags().IntVar(&seedAuthors, "authors", 5, "number of distinct authors")
	seedCmd.Flags().IntVar(&seedSeries, "series", 8, "number of distinct series across all authors")
	seedCmd.Flags().BoolVar(&seedReset, "reset", false, "delete existing seed:* books before seeding")
}

var seedAuthorPool = []string{
	"Brandon Sanderson", "N. K. Jemisin", "Ursula K. Le Guin",
	"Ann Leckie", "Tamsyn Muir", "Becky Chambers",
	"Pierce Brown", "Terry Pratchett", "Iain M. Banks",
	"Liu Cixin", "Andy Weir", "Susanna Clarke",
}

var seedSeriesPool = []string{
	"Stormlight Archive", "Mistborn", "Broken Earth",
	"Earthsea Cycle", "Imperial Radch", "The Locked Tomb",
	"Wayfarers", "Red Rising", "Discworld", "Culture",
	"Remembrance of Earth's Past", "Founders Trilogy",
}

var seedTitleAdjectives = []string{
	"Forgotten", "Crimson", "Hollow", "Final", "Silver",
	"Burning", "Distant", "Whispering", "Eternal", "Hidden",
	"Restless", "Sacred", "Sunless", "Frozen", "Splintered",
}

var seedTitleNouns = []string{
	"Crown", "Tide", "Forge", "Court", "Shore",
	"Path", "Garden", "Tower", "Reckoning", "Empire",
	"Echo", "Spear", "Throne", "Memory", "Dawn",
}

var seedFormats = []string{"m4b", "mp3", "flac"}

func runSeed(cmd *cobra.Command, _ []string) error {
	if config.AppConfig.RootDir == "" {
		return fmt.Errorf("root directory not specified — pass --dir or set ROOT_DIR")
	}
	if seedCount <= 0 {
		return fmt.Errorf("--count must be > 0")
	}
	if seedAuthors <= 0 {
		return fmt.Errorf("--authors must be > 0")
	}
	if seedSeries <= 0 {
		return fmt.Errorf("--series must be > 0")
	}

	store, err := initializeStore(config.AppConfig.DatabaseType, config.AppConfig.DatabasePath, config.AppConfig.EnableSQLite)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer closeStore()

	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	if seedReset {
		removed, err := purgeSeedBooks(store)
		if err != nil {
			return fmt.Errorf("reset failed: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed %d existing seed books\n", removed)
	}

	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xc0ffee))

	authors := make([]*database.Author, 0, seedAuthors)
	for i := 0; i < seedAuthors; i++ {
		name := pickUniqueAuthor(rng, authors)
		a, err := upsertAuthor(store, name)
		if err != nil {
			return fmt.Errorf("create author %q: %w", name, err)
		}
		authors = append(authors, a)
	}

	type seedSeriesEntry struct {
		series *database.Series
		next   int
	}
	seriesList := make([]*seedSeriesEntry, 0, seedSeries)
	usedSeriesNames := make(map[string]bool, seedSeries)
	for i := 0; i < seedSeries; i++ {
		owner := authors[rng.IntN(len(authors))]
		name := pickUniqueSeries(rng, usedSeriesNames, len(seriesList))
		usedSeriesNames[name] = true
		s, err := upsertSeries(store, name, &owner.ID)
		if err != nil {
			return fmt.Errorf("create series %q: %w", name, err)
		}
		seriesList = append(seriesList, &seedSeriesEntry{series: s, next: 1})
	}

	created := 0
	for i := 0; i < seedCount; i++ {
		entry := seriesList[rng.IntN(len(seriesList))]
		series := entry.series
		seq := entry.next
		entry.next++

		var authorID *int
		if series.AuthorID != nil {
			authorID = series.AuthorID
		}

		title := pickTitle(rng)
		format := seedFormats[rng.IntN(len(seedFormats))]
		duration := 3600 + rng.IntN(36000) // 1h–11h, in seconds
		state := "imported"

		// Synthesize a path under <root>/seed/<author>/<series>/<title>.<format>
		var authorName string
		if authorID != nil {
			if a, _ := store.GetAuthorByID(*authorID); a != nil {
				authorName = a.Name
			}
		}
		if authorName == "" {
			authorName = "Unknown"
		}
		safeAuthor := safeForPath(authorName)
		safeSeries := safeForPath(series.Name)
		safeTitle := safeForPath(title)
		filePath := filepath.Join(config.AppConfig.RootDir, "seed", safeAuthor, safeSeries, fmt.Sprintf("%s.%s", safeTitle, format))

		seriesID := series.ID
		seriesSeq := seq
		quantity := 1
		book := &database.Book{
			ID:             fmt.Sprintf("seed_%s", ulid.Make().String()),
			Title:          title,
			FilePath:       filePath,
			Format:         format,
			AuthorID:       authorID,
			SeriesID:       &seriesID,
			SeriesSequence: &seriesSeq,
			Duration:       &duration,
			LibraryState:   &state,
			Quantity:       &quantity,
		}
		if _, err := store.CreateBook(book); err != nil {
			return fmt.Errorf("create book %q: %w", title, err)
		}
		created++
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Seeded %d books across %d authors and %d series under %s/seed/\n",
		created, len(authors), len(seriesList), config.AppConfig.RootDir,
	)
	return nil
}

func pickUniqueAuthor(rng *rand.Rand, existing []*database.Author) string {
	used := make(map[string]bool, len(existing))
	for _, a := range existing {
		used[a.Name] = true
	}
	for _, name := range rng.Perm(len(seedAuthorPool)) {
		candidate := seedAuthorPool[name]
		if !used[candidate] {
			return candidate
		}
	}
	return fmt.Sprintf("Author %d", len(existing)+1)
}

func pickUniqueSeries(rng *rand.Rand, used map[string]bool, fallbackIdx int) string {
	for _, idx := range rng.Perm(len(seedSeriesPool)) {
		candidate := seedSeriesPool[idx]
		if !used[candidate] {
			return candidate
		}
	}
	return fmt.Sprintf("Series %d", fallbackIdx+1)
}

func pickTitle(rng *rand.Rand) string {
	adj := seedTitleAdjectives[rng.IntN(len(seedTitleAdjectives))]
	noun := seedTitleNouns[rng.IntN(len(seedTitleNouns))]
	return fmt.Sprintf("The %s %s", adj, noun)
}

func upsertAuthor(store database.AuthorStore, name string) (*database.Author, error) {
	if existing, err := store.GetAuthorByName(name); err == nil && existing != nil {
		return existing, nil
	}
	return store.CreateAuthor(name)
}

func upsertSeries(store database.SeriesStore, name string, authorID *int) (*database.Series, error) {
	if existing, err := store.GetSeriesByName(name, authorID); err == nil && existing != nil {
		return existing, nil
	}
	return store.CreateSeries(name, authorID)
}

func purgeSeedBooks(store database.BookStore) (int, error) {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, b := range books {
		if !strings.HasPrefix(b.ID, "seed_") {
			continue
		}
		if err := store.DeleteBook(b.ID); err != nil {
			return removed, fmt.Errorf("delete %s: %w", b.ID, err)
		}
		removed++
	}
	return removed, nil
}

func safeForPath(s string) string {
	s = strings.ReplaceAll(s, string(os.PathSeparator), "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.TrimSpace(s)
	if s == "" {
		return "_"
	}
	return s
}
