// file: internal/books/store/pebble_store_series_restore.go
// version: 1.0.0
// guid: 2e6b9a41-d305-4c78-b1f4-87a0c6d53e92
// last-edited: 2026-11-16

package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// ErrSeriesNameTaken: RestoreSeries found another live series holding the name
// (under the same author), so bringing the deleted row back would put two rows
// under one name-index entry.
var ErrSeriesNameTaken = errors.New("series name is held by another series")

// ErrSeriesIDInUse: RestoreSeries found a live series of that id that is not the
// one being restored.
var ErrSeriesIDInUse = errors.New("series id is in use by a different series")

// SeriesRestorer is the one series write an undo needs and nothing else does:
// bring a deleted series back under its own id. It is not on Store (every other
// writer of series goes through CreateSeries, which mints a new id) -- resolve
// it with AsCapability. *PebbleStore implements it.
type SeriesRestorer interface {
	// RestoreSeries recreates the series row with exactly this id, name and
	// author. A live row that is already exactly that is not an error (nil, nothing
	// written), so a revert that stopped part way finishes on a retry. It returns
	// ErrSeriesIDInUse when the id is live as something else and
	// ErrSeriesNameTaken when another series holds the name; either way nothing is
	// written.
	RestoreSeries(id int, name string, authorID *int) error
}

var _ SeriesRestorer = (*PebbleStore)(nil)

// RestoreSeries implements SeriesRestorer, under the same series name-index lock
// CreateSeries and DeleteSeries take, so it cannot interleave with either.
func (p *PebbleStore) RestoreSeries(id int, name string, authorID *int) error {
	if id <= 0 {
		return fmt.Errorf("restore series: invalid id %d", id)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("restore series %d: empty name", id)
	}
	p.nameIdx.series.Lock()
	defer p.nameIdx.series.Unlock()

	key := []byte(fmt.Sprintf("series:%d", id))
	cur, err := p.getValueCopy(key)
	if err != nil {
		return fmt.Errorf("restore series %d: %w", id, err)
	}
	if cur != nil {
		var live Series
		if json.Unmarshal(cur, &live) == nil && live.ID == id && live.Name == name && sameSeriesAuthor(live.AuthorID, authorID) {
			return nil
		}
		return fmt.Errorf("%w: series %d is live as %q", ErrSeriesIDInUse, id, live.Name)
	}

	// GetSeriesByName follows the index to a live row, so an entry left naming a
	// deleted id reads as free here and is overwritten below.
	holder, err := p.GetSeriesByName(name, authorID)
	if err != nil {
		return fmt.Errorf("restore series %d: look up the name %q: %w", id, name, err)
	}
	if holder != nil {
		return fmt.Errorf("%w: series %d holds %q", ErrSeriesNameTaken, holder.ID, holder.Name)
	}

	series := &Series{ID: id, Name: name, AuthorID: authorID}
	data, err := json.Marshal(series)
	if err != nil {
		return err
	}
	authorIDStr := "nil"
	if authorID != nil {
		authorIDStr = strconv.Itoa(*authorID)
	}
	indexKey := []byte(seriesNameIndexKey(authorIDStr)(util.NormalizeAuthor(name)))

	batch := p.db.NewBatch()
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("restore series %d: %w", id, err)
	}
	p.UpsertSeriesToMemDB(series)
	p.notifySeriesRenamed(id)
	return nil
}

func sameSeriesAuthor(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
