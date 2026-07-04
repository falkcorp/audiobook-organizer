// file: internal/database/pebble_store_series.go
// version: 1.0.0
// guid: 29120d16-9add-4efd-81a5-edc1e8951f4d
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func (p *PebbleStore) GetAllSeries() ([]Series, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllSeries()
	}
	return p.GetAllSeries_Pebble()
}

// GetAllSeries_Pebble returns all series using Pebble key-range iteration.
func (p *PebbleStore) GetAllSeries_Pebble() ([]Series, error) {
	var series []Series
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("series:0"),
		UpperBound: []byte("series:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		// Skip index keys
		if strings.Contains(string(iter.Key()), ":name:") {
			continue
		}

		var s Series
		if err := json.Unmarshal(iter.Value(), &s); err != nil {
			return nil, err
		}
		series = append(series, s)
	}

	return series, nil
}

func (p *PebbleStore) GetSeriesByID(id int) (*Series, error) {
	key := []byte(fmt.Sprintf("series:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var series Series
	if err := json.Unmarshal(value, &series); err != nil {
		return nil, err
	}
	return &series, nil
}

// GetSeriesByIDs returns a map from seriesID → *Series for the given IDs.
// Deduplicates IDs before fetching; missing IDs are absent from the result map.
func (p *PebbleStore) GetSeriesByIDs(ids []int) (map[int]*Series, error) {
	result := make(map[int]*Series, len(ids))
	for _, id := range ids {
		if _, already := result[id]; already {
			continue
		}
		s, err := p.GetSeriesByID(id)
		if err != nil {
			return nil, err
		}
		if s != nil {
			result[id] = s
		}
	}
	return result, nil
}

func (p *PebbleStore) GetSeriesByName(name string, authorID *int) (*Series, error) {
	authorIDStr := "nil"
	if authorID != nil {
		authorIDStr = strconv.Itoa(*authorID)
	}

	// Use lowercase for case-insensitive lookup
	indexKey := []byte(fmt.Sprintf("series:name:%s:%s", util.NormalizeAuthor(name), authorIDStr))
	value, closer, err := p.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return nil, err
	}

	return p.GetSeriesByID(id)
}

func (p *PebbleStore) CreateSeries(name string, authorID *int) (*Series, error) {
	// Check if series already exists
	existing, err := p.GetSeriesByName(name, authorID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	id, err := p.nextID("series")
	if err != nil {
		return nil, err
	}

	series := &Series{ID: id, Name: name, AuthorID: authorID}
	data, err := json.Marshal(series)
	if err != nil {
		return nil, err
	}

	authorIDStr := "nil"
	if authorID != nil {
		authorIDStr = strconv.Itoa(*authorID)
	}

	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("series:%d", id))
	// Use lowercase for case-insensitive lookup
	indexKey := []byte(fmt.Sprintf("series:name:%s:%s", util.NormalizeAuthor(name), authorIDStr))

	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}
	if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return nil, err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	p.UpsertSeriesToMemDB(series)
	return series, nil
}

func (p *PebbleStore) DeleteSeries(id int) error {
	key := []byte(fmt.Sprintf("series:%d", id))

	// Read the series first to clean up the name index
	val, closer, err := p.db.Get(key)
	if err == nil {
		var series Series
		if json.Unmarshal(val, &series) == nil {
			authorIDStr := "nil"
			if series.AuthorID != nil {
				authorIDStr = strconv.Itoa(*series.AuthorID)
			}
			indexKey := []byte(fmt.Sprintf("series:name:%s:%s", util.NormalizeAuthor(series.Name), authorIDStr))
			if err := p.db.Delete(indexKey, pebble.Sync); err != nil {
				slog.Warn("pebble Delete series name index", "key", string(indexKey), "error", err)
			}
		}
		closer.Close()
	}

	if err := p.db.Delete(key, pebble.Sync); err != nil {
		return err
	}
	p.DeleteSeriesFromMemDB(id)
	return nil
}

func (p *PebbleStore) UpdateSeriesName(id int, name string) error {
	key := []byte(fmt.Sprintf("series:%d", id))
	val, closer, err := p.db.Get(key)
	if err != nil {
		return fmt.Errorf("series %d not found: %w", id, err)
	}
	var series Series
	if err := json.Unmarshal(val, &series); err != nil {
		closer.Close()
		return err
	}
	closer.Close()

	// Delete old name index
	oldAuthorIDStr := "nil"
	if series.AuthorID != nil {
		oldAuthorIDStr = strconv.Itoa(*series.AuthorID)
	}
	oldIndexKey := []byte(fmt.Sprintf("series:name:%s:%s", util.NormalizeAuthor(series.Name), oldAuthorIDStr))
	if err := p.db.Delete(oldIndexKey, pebble.Sync); err != nil {
		slog.Warn("pebble Delete old series name index", "key", string(oldIndexKey), "error", err)
	}

	// Update name
	series.Name = name
	data, err := json.Marshal(series)
	if err != nil {
		return err
	}
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return err
	}

	// Create new name index
	newIndexKey := []byte(fmt.Sprintf("series:name:%s:%s", util.NormalizeAuthor(name), oldAuthorIDStr))
	idBytes := []byte(fmt.Sprintf("%d", id))
	if err := p.db.Set(newIndexKey, idBytes, pebble.Sync); err != nil {
		return err
	}
	if updated, err := p.GetSeriesByID(id); err == nil && updated != nil {
		p.UpsertSeriesToMemDB(updated)
	}
	return nil
}

func (p *PebbleStore) GetAllSeriesBookCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllSeriesBookCounts()
	}
	return p.GetAllSeriesBookCounts_Pebble()
}

// GetAllSeriesBookCounts_Pebble returns the number of books per series using Pebble iteration
func (p *PebbleStore) GetAllSeriesBookCounts_Pebble() (map[int]int, error) {
	counts := make(map[int]int)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, "book:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) < 2 || len(parts) > 2 {
			continue
		}

		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.SeriesID == nil || (b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion) {
			continue
		}
		counts[*b.SeriesID]++
	}
	return counts, nil
}

// GetAllSeriesFileCounts returns the number of audio files per series.
func (p *PebbleStore) GetAllSeriesFileCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllSeriesFileCounts()
	}
	bookIDToSeriesID := make(map[string]int)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book:0"),
		UpperBound: []byte("book:;"),
	})
	if err != nil {
		return nil, err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, "book:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}
		var b Book
		if err := json.Unmarshal(iter.Value(), &b); err != nil {
			continue
		}
		if b.SeriesID != nil && (b.IsPrimaryVersion == nil || *b.IsPrimaryVersion) {
			bookIDToSeriesID[b.ID] = *b.SeriesID
		}
	}
	iter.Close()

	// Count actual BookFile records per book.
	bookFileCounts := make(map[string]int) // bookID → actual file count
	fileIter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("book_file:0"),
		UpperBound: []byte("book_file:;"),
	})
	if err != nil {
		return nil, err
	}
	for fileIter.First(); fileIter.Valid(); fileIter.Next() {
		key := string(fileIter.Key())
		if !strings.HasPrefix(key, "book_file:") {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) < 3 {
			continue
		}
		bookID := parts[1]
		if _, inSeries := bookIDToSeriesID[bookID]; inSeries {
			var f BookFile
			if err := json.Unmarshal(fileIter.Value(), &f); err != nil {
				continue
			}
			if !f.Missing {
				bookFileCounts[bookID]++
			}
		}
	}
	fileIter.Close()

	// Aggregate into series counts.
	// Books with no files count as 1 (matches SQLite behaviour).
	counts := make(map[int]int)
	for bookID, seriesID := range bookIDToSeriesID {
		n := bookFileCounts[bookID]
		if n == 0 {
			n = 1
		}
		counts[seriesID] += n
	}
	return counts, nil
}
