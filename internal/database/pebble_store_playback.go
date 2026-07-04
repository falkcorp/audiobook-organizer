// file: internal/database/pebble_store_playback.go
// version: 1.0.0
// guid: 7559a9db-cb41-4281-b8d2-2e644796eeb7
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

func (p *PebbleStore) SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error {
	if userID == "" || bookID == "" || segmentID == "" {
		return fmt.Errorf("user/book/segment required")
	}
	pos := UserPosition{
		UserID: userID, BookID: bookID, SegmentID: segmentID,
		PositionSeconds: positionSeconds, UpdatedAt: time.Now(),
	}
	data, err := json.Marshal(pos)
	if err != nil {
		return err
	}
	return p.db.Set([]byte("upos:"+userID+":"+bookID+":"+segmentID), data, pebble.NoSync)
}

func (p *PebbleStore) GetUserPosition(userID, bookID string) (*UserPosition, error) {
	if userID == "" || bookID == "" {
		return nil, nil
	}
	prefix := []byte("upos:" + userID + ":" + bookID + ":")
	upper := []byte("upos:" + userID + ":" + bookID + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var latest *UserPosition
	for iter.First(); iter.Valid(); iter.Next() {
		var pos UserPosition
		if err := json.Unmarshal(iter.Value(), &pos); err != nil {
			continue
		}
		if latest == nil || pos.UpdatedAt.After(latest.UpdatedAt) {
			posCopy := pos
			latest = &posCopy
		}
	}
	return latest, nil
}

func (p *PebbleStore) ListUserPositionsForBook(userID, bookID string) ([]UserPosition, error) {
	if userID == "" || bookID == "" {
		return nil, nil
	}
	prefix := []byte("upos:" + userID + ":" + bookID + ":")
	upper := []byte("upos:" + userID + ":" + bookID + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []UserPosition
	for iter.First(); iter.Valid(); iter.Next() {
		var pos UserPosition
		if err := json.Unmarshal(iter.Value(), &pos); err != nil {
			continue
		}
		out = append(out, pos)
	}
	return out, nil
}

func (p *PebbleStore) ClearUserPositions(userID, bookID string) error {
	positions, err := p.ListUserPositionsForBook(userID, bookID)
	if err != nil {
		return err
	}
	if len(positions) == 0 {
		return nil
	}
	b := p.db.NewBatch()
	for _, pos := range positions {
		if err := b.Delete([]byte("upos:"+pos.UserID+":"+pos.BookID+":"+pos.SegmentID), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) SetUserBookState(state *UserBookState) error {
	if state == nil || state.UserID == "" || state.BookID == "" {
		return fmt.Errorf("user and book required")
	}
	state.UpdatedAt = time.Now()
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	prev, _ := p.GetUserBookState(state.UserID, state.BookID)
	b := p.db.NewBatch()
	if err := b.Set([]byte("ubs:"+state.UserID+":"+state.BookID), data, nil); err != nil {
		b.Close()
		return err
	}
	if prev != nil && prev.Status != "" && prev.Status != state.Status {
		if err := b.Delete([]byte("idx:ubs:status:"+state.UserID+":"+prev.Status+":"+state.BookID), nil); err != nil {
			b.Close()
			return err
		}
	}
	if state.Status != "" {
		if err := b.Set([]byte("idx:ubs:status:"+state.UserID+":"+state.Status+":"+state.BookID), []byte("1"), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) GetUserBookState(userID, bookID string) (*UserBookState, error) {
	if userID == "" || bookID == "" {
		return nil, nil
	}
	data, closer, err := p.db.Get([]byte("ubs:" + userID + ":" + bookID))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var s UserBookState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (p *PebbleStore) ListUserBookStatesByStatus(userID, status string, limit, offset int) ([]UserBookState, error) {
	if userID == "" || status == "" {
		return nil, nil
	}
	prefix := []byte("idx:ubs:status:" + userID + ":" + status + ":")
	upper := []byte("idx:ubs:status:" + userID + ":" + status + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []UserBookState
	skipped := 0
	prefixLen := len(prefix)
	for iter.First(); iter.Valid(); iter.Next() {
		if limit > 0 && len(out) >= limit {
			break
		}
		bookID := string(iter.Key()[prefixLen:])
		if skipped < offset {
			skipped++
			continue
		}
		state, err := p.GetUserBookState(userID, bookID)
		if err != nil || state == nil {
			continue
		}
		out = append(out, *state)
	}
	return out, nil
}

func (p *PebbleStore) ListUserPositionsSince(userID string, t time.Time) ([]UserPosition, error) {
	if userID == "" {
		return nil, nil
	}
	prefix := []byte("upos:" + userID + ":")
	upper := []byte("upos:" + userID + ":~")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []UserPosition
	for iter.First(); iter.Valid(); iter.Next() {
		var pos UserPosition
		if err := json.Unmarshal(iter.Value(), &pos); err != nil {
			continue
		}
		if pos.UpdatedAt.After(t) {
			out = append(out, pos)
		}
	}
	return out, nil
}

// Playback events & progress
func (p *PebbleStore) AddPlaybackEvent(event *PlaybackEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	event.Version = 1
	data, _ := json.Marshal(event)
	key := fmt.Sprintf("playe:%s:%d:%d", event.UserID, event.BookID, event.CreatedAt.UnixNano())
	return p.db.Set([]byte(key), data, pebble.Sync)
}

func (p *PebbleStore) ListPlaybackEvents(userID string, bookNumericID int, limit int) ([]PlaybackEvent, error) {
	prefix := []byte(fmt.Sprintf("playe:%s:%d:", userID, bookNumericID))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: append(prefix, 0xFF)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var events []PlaybackEvent
	for iter.First(); iter.Valid(); iter.Next() {
		var ev PlaybackEvent
		if err := json.Unmarshal(iter.Value(), &ev); err == nil {
			events = append(events, ev)
		}
	}
	// reverse chronological and cap to limit
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (p *PebbleStore) UpdatePlaybackProgress(progress *PlaybackProgress) error {
	if progress.UpdatedAt.IsZero() {
		progress.UpdatedAt = time.Now()
	}
	progress.Version = 1
	data, _ := json.Marshal(progress)
	key := fmt.Sprintf("playp:%s:%d", progress.UserID, progress.BookID)
	return p.db.Set([]byte(key), data, pebble.Sync)
}

func (p *PebbleStore) GetPlaybackProgress(userID string, bookNumericID int) (*PlaybackProgress, error) {
	v, closer, err := p.db.Get([]byte(fmt.Sprintf("playp:%s:%d", userID, bookNumericID)))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var pr PlaybackProgress
	if err := json.Unmarshal(v, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

// Stats aggregation
func (p *PebbleStore) IncrementBookPlayStats(bookNumericID int, seconds int) error {
	// increment counters stored as decimal strings
	if err := p.incrementIntKey(fmt.Sprintf("stats:book:plays:%d", bookNumericID), 1); err != nil {
		return err
	}
	return p.incrementIntKey(fmt.Sprintf("stats:book:listen_seconds:%d", bookNumericID), seconds)
}

func (p *PebbleStore) GetBookStats(bookNumericID int) (*BookStats, error) {
	playsKey := fmt.Sprintf("stats:book:plays:%d", bookNumericID)
	secsKey := fmt.Sprintf("stats:book:listen_seconds:%d", bookNumericID)
	plays, playsErr := p.readIntKey(playsKey)
	secs, secsErr := p.readIntKey(secsKey)
	// Return nil,nil when no stats have been recorded for this book (both keys absent).
	if playsErr == nil && secsErr == nil && plays == 0 && secs == 0 {
		// Check if either key actually exists (zero is a valid recorded value).
		if _, _, err := p.db.Get([]byte(playsKey)); err == pebble.ErrNotFound {
			if _, _, err2 := p.db.Get([]byte(secsKey)); err2 == pebble.ErrNotFound {
				return nil, nil
			}
		}
	}
	return &BookStats{BookID: bookNumericID, PlayCount: plays, ListenSeconds: secs, Version: 1}, nil
}

func (p *PebbleStore) IncrementUserListenStats(userID string, seconds int) error {
	return p.incrementIntKey("stats:user:listen_seconds:"+userID, seconds)
}

func (p *PebbleStore) GetUserStats(userID string) (*UserStats, error) {
	secsKey := "stats:user:listen_seconds:" + userID
	secs, _ := p.readIntKey(secsKey)
	if secs == 0 {
		if _, _, err := p.db.Get([]byte(secsKey)); err == pebble.ErrNotFound {
			return nil, nil
		}
	}
	return &UserStats{UserID: userID, ListenSeconds: secs, Version: 1}, nil
}

func (p *PebbleStore) readIntKey(key string) (int, error) {
	v, closer, err := p.db.Get([]byte(key))
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer closer.Close()
	return strconv.Atoi(string(v))
}

func (p *PebbleStore) incrementIntKey(key string, delta int) error {
	cur, _ := p.readIntKey(key)
	cur += delta
	return p.db.Set([]byte(key), []byte(strconv.Itoa(cur)), pebble.Sync)
}
