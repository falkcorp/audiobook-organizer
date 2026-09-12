// file: internal/database/pebble_store_series.go
// version: 1.7.0
// guid: 29120d16-9add-4efd-81a5-edc1e8951f4d
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"errors"
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

	sortByLowerName(series, func(s Series) string { return s.Name })
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

	// Case- and whitespace-insensitive lookup with the legacy-key fallback
	// (pebble_store_name_index.go).
	value, err := p.nameIndexGet(seriesNameIndexKey(authorIDStr), name)
	if err != nil || value == nil {
		return nil, err
	}

	id, err := strconv.Atoi(string(value))
	if err != nil {
		return nil, err
	}

	return p.GetSeriesByID(id)
}

// CreateSeries returns the series with this name under authorID, creating it
// if absent. Same shape as CreateAuthor: an unlocked fast path for a name that
// already resolves, then nameIdx.series and a re-check under it, so two
// concurrent creates of one name cannot both mint a row and a create cannot
// interleave with a rename or delete of the same series:name: key.
func (p *PebbleStore) CreateSeries(name string, authorID *int) (*Series, error) {
	// Fast path: an existing series needs no lock.
	existing, err := p.GetSeriesByName(name, authorID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	p.nameIdx.series.Lock()
	defer p.nameIdx.series.Unlock()

	// Re-check under the lock: another goroutine may have created it between
	// the fast-path miss and acquiring the lock.
	existing, err = p.GetSeriesByName(name, authorID)
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
	// Held from the row read through the last write; see nameIndexLocks.
	p.nameIdx.series.Lock()
	defer p.nameIdx.series.Unlock()

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
			// Ownership-checked: a series whose name collapses to the same
			// key under the same author may own the entry. With p.db as the
			// writer the delete lands inside the call; nameIdx.series keeps
			// any other writer out of the read-to-delete window.
			if err := p.deleteNameIndexIfOwned(p.db, pebble.Sync, seriesNameIndexKey(authorIDStr), series.Name, nameIndexOwner(id)); err != nil {
				slog.Warn("pebble Delete series name index", "series_id", id, "name", series.Name, "error", err)
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
	// Held from the row read (the old name decides which key is deleted)
	// through the new index write; see nameIndexLocks.
	p.nameIdx.series.Lock()
	defer p.nameIdx.series.Unlock()

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
	return p.renameSeriesLocked(id, &series, name)
}

// RenameSeriesIf refusals, wrapped with the specifics. Nothing is written.
var (
	// ErrRenameSeriesNotFound: no series has that id.
	ErrRenameSeriesNotFound = errors.New("series not found")
	// ErrRenameSeriesRenamedSince: the series is not named expectCurrent.
	ErrRenameSeriesRenamedSince = errors.New("series renamed since")
	// ErrRenameSeriesNameTaken: another series under the same author already
	// answers to the new name.
	ErrRenameSeriesNameTaken = errors.New("series name taken")
)

// RenameSeriesIf renames series id to newName only if it is still named
// expectCurrent and no other series under the same author answers to newName,
// compared the way the name index compares (case and whitespace insensitive).
// The checks and the write all run under nameIdx.series, which CreateSeries,
// UpdateSeriesName and DeleteSeries also hold, so a concurrent create of
// newName or a concurrent rename cannot land between check and write -- the
// window a GetSeriesByName check followed by UpdateSeriesName leaves open. The
// undo revert uses it to rename a series back.
func (p *PebbleStore) RenameSeriesIf(id int, expectCurrent, newName string) error {
	p.nameIdx.series.Lock()
	defer p.nameIdx.series.Unlock()

	series, err := p.GetSeriesByID(id)
	if err != nil {
		return fmt.Errorf("read series %d: %w", id, err)
	}
	if series == nil {
		return fmt.Errorf("series %d: %w", id, ErrRenameSeriesNotFound)
	}
	if series.Name != expectCurrent {
		return fmt.Errorf("series %d is named %q, not %q: %w", id, series.Name, expectCurrent, ErrRenameSeriesRenamedSince)
	}
	holder, err := p.GetSeriesByName(newName, series.AuthorID)
	if err != nil {
		return fmt.Errorf("look up series named %q: %w", newName, err)
	}
	if holder != nil && holder.ID != id {
		return fmt.Errorf("series %d already answers to %q: %w", holder.ID, holder.Name, ErrRenameSeriesNameTaken)
	}
	return p.renameSeriesLocked(id, series, newName)
}

// renameSeriesLocked moves series (the stored row for id) to name: the old
// name-index key is removed if this series owns it, the row is rewritten and
// the new key set. The caller holds nameIdx.series.
func (p *PebbleStore) renameSeriesLocked(id int, series *Series, name string) error {
	key := []byte(fmt.Sprintf("series:%d", id))

	// Delete old name index
	oldAuthorIDStr := "nil"
	if series.AuthorID != nil {
		oldAuthorIDStr = strconv.Itoa(*series.AuthorID)
	}
	if err := p.deleteNameIndexIfOwned(p.db, pebble.Sync, seriesNameIndexKey(oldAuthorIDStr), series.Name, nameIndexOwner(id)); err != nil {
		slog.Warn("pebble Delete old series name index", "series_id", id, "name", series.Name, "error", err)
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

	if err := forEachBookRow(p.db, func(rowID string, rowValue []byte) error {

		var b Book
		if err := json.Unmarshal(rowValue, &b); err != nil {
			return nil
		}
		if b.SeriesID == nil || (b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion) {
			return nil
		}
		// The memdb counterpart has always excluded the trash; this path did
		// not, so a series reported more books than it has whenever it was
		// served before memdb published. See aggregate_count_conformance_test.go.
		if bookIsSoftDeleted(&b) {
			return nil
		}
		counts[*b.SeriesID]++
		return nil
	}); err != nil {
		return nil, err
	}
	return counts, nil
}

// GetAllSeriesFileCounts returns the number of audio files per series.
func (p *PebbleStore) GetAllSeriesFileCounts() (map[int]int, error) {
	if p.UseMemDB && p.mem() != nil {
		return p.mem().GetAllSeriesFileCounts()
	}
	bookIDToSeriesID := make(map[string]int)
	if err := forEachBookRow(p.db, func(rowID string, rowValue []byte) error {
		var b Book
		if err := json.Unmarshal(rowValue, &b); err != nil {
			return nil
		}
		// Soft-deleted books are excluded here rather than when counting files,
		// so their files never enter the map in the first place — matching the
		// memdb counterpart. See aggregate_count_conformance_test.go.
		if b.SeriesID != nil && (b.IsPrimaryVersion == nil || *b.IsPrimaryVersion) && !bookIsSoftDeleted(&b) {
			bookIDToSeriesID[b.ID] = *b.SeriesID
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Count actual BookFile records per book.
	bookFileCounts := make(map[string]int) // bookID → actual file count
	if err := forEachKeyInRange(p.db, []byte("book_file:"), []byte("book_file;"), func(k, value []byte) error {
		key := string(k)
		if !strings.HasPrefix(key, "book_file:") {
			return nil
		}
		parts := strings.Split(key, ":")
		if len(parts) < 3 {
			return nil
		}
		bookID := parts[1]
		if _, inSeries := bookIDToSeriesID[bookID]; inSeries {
			var f BookFile
			if err := json.Unmarshal(value, &f); err != nil {
				return nil
			}
			if !f.Missing {
				bookFileCounts[bookID]++
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

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
