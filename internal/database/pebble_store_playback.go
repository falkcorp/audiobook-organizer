// file: internal/database/pebble_store_playback.go
// version: 1.2.0
// guid: 7559a9db-cb41-4281-b8d2-2e644796eeb7
// last-edited: 2026-09-13

package database

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var playbackLog = logger.New("database.playback")

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

// stampFinishedAt maintains state.FinishedAt (see UserBookState) against the
// stored prev, and mutates state in place so the caller sees the stamp. Every
// writer of a UserBookState -- readstatus, the ABS handlers, merge, the iTunes
// position backfill -- goes through SetUserBookState, so this is the one
// place a finish is dated. prevErr is the error from reading prev.
//   - not Finished: no finish, the stamp is cleared.
//   - prev unreadable: the caller's value is kept, and nil stays nil. The row
//     that could not be read may already be Finished, so stamping now would
//     date an old finish as a new one, and the iTunes push would count it.
//   - still Finished: the original finish is kept, whatever the caller sent.
//   - newly Finished, and the caller sent a stamp: the caller's is kept.
//     This is safe because anyone who read a non-Finished row, or no row,
//     got nil: every non-Finished write clears the stamp. A non-nil value
//     arriving here was therefore carried from somewhere else, either by
//     the merge carry (a loser's finish written onto a winner whose row is
//     unfinished, merge/sync_follow.go) or by the undo restore (a snapshot
//     written back over the drained row, merge/combine_journal.go). In both
//     cases it is the same finish and must not be dated as new.
//   - newly Finished with no stamp: stamped now.
func stampFinishedAt(state, prev *UserBookState, prevErr error, now time.Time) {
	if state.Status != UserBookStatusFinished {
		state.FinishedAt = nil
		return
	}
	if prevErr != nil {
		return
	}
	if prev != nil && prev.Status == UserBookStatusFinished {
		state.FinishedAt = prev.FinishedAt
		return
	}
	if state.FinishedAt != nil {
		return
	}
	finished := now
	state.FinishedAt = &finished
}

func (p *PebbleStore) SetUserBookState(state *UserBookState) error {
	if state == nil || state.UserID == "" || state.BookID == "" {
		return fmt.Errorf("user and book required")
	}
	now := time.Now()
	state.UpdatedAt = now
	// A prev that cannot be read is still overwritten, because a row that
	// fails to decode must stay rewritable. stampFinishedAt then keeps the
	// caller's stamp instead of dating the finish now.
	prev, prevErr := p.GetUserBookState(state.UserID, state.BookID)
	if prevErr != nil {
		playbackLog.Warn("SetUserBookState %s/%s: stored state unreadable (%v); overwriting it and keeping the caller's finish stamp", state.UserID, state.BookID, prevErr)
		prev = nil
	}
	stampFinishedAt(state, prev, prevErr, now)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

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
	// A missing record (the getter's (nil, nil)) is a stale index entry and is
	// skipped. An unreadable member is NOT skipped silently: the readable
	// states come back together with an *UnreadableMembersError naming it (see
	// that type); it still occupies its slot in the page, so the limit/offset
	// window does not shift. An index-scan error is a hard failure with nil
	// rows. Until 2026-09-12 every member error was skipped and the scan's own
	// read error was never checked, so an unreadable member came back as a
	// short list.
	var out []UserBookState
	unreadable := &UnreadableMembersError{Op: "ListUserBookStatesByStatus " + userID + "/" + status}
	skipped := 0
	prefixLen := len(prefix)
	if err := forEachKeyInRange(p.db, prefix, upper, func(key, _ []byte) error {
		if limit > 0 && len(out)+unreadable.Count() >= limit {
			return errStopScan
		}
		bookID := string(key[prefixLen:])
		if skipped < offset {
			skipped++
			return nil
		}
		state, err := p.GetUserBookState(userID, bookID)
		if err != nil {
			unreadable.add(bookID, fmt.Errorf("reading state for book %s: %w", bookID, err))
			return nil
		}
		if state != nil {
			out = append(out, *state)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, unreadable.orNil()
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
