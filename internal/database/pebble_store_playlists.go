// file: internal/database/pebble_store_playlists.go
// version: 1.2.0
// guid: b93ba897-1377-4cf7-9aea-ca57f135893e
// last-edited: 2026-09-19

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

func (p *PebbleStore) CreatePlaylist(name string, seriesID *int, filePath string) (*Playlist, error) {
	id, err := p.nextID("playlist")
	if err != nil {
		return nil, err
	}

	playlist := &Playlist{
		ID:       id,
		Name:     name,
		SeriesID: seriesID,
		FilePath: filePath,
	}

	data, err := json.Marshal(playlist)
	if err != nil {
		return nil, err
	}

	batch := p.db.NewBatch()
	key := []byte(fmt.Sprintf("playlist:%d", id))
	if err := batch.Set(key, data, nil); err != nil {
		batch.Close()
		return nil, err
	}

	if seriesID != nil {
		indexKey := []byte(fmt.Sprintf("playlist:series:%d", *seriesID))
		if err := batch.Set(indexKey, []byte(strconv.Itoa(id)), nil); err != nil {
			batch.Close()
			return nil, err
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	return playlist, nil
}

func (p *PebbleStore) GetPlaylistByID(id int) (*Playlist, error) {
	key := []byte(fmt.Sprintf("playlist:%d", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var playlist Playlist
	if err := json.Unmarshal(value, &playlist); err != nil {
		return nil, err
	}
	return &playlist, nil
}

func (p *PebbleStore) GetPlaylistBySeriesID(seriesID int) (*Playlist, error) {
	indexKey := []byte(fmt.Sprintf("playlist:series:%d", seriesID))
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

	return p.GetPlaylistByID(id)
}

func (p *PebbleStore) AddPlaylistItem(playlistID, bookID, position int) error {
	id, err := p.nextID("playlistitem")
	if err != nil {
		return err
	}

	item := &PlaylistItem{
		ID:         id,
		PlaylistID: playlistID,
		BookID:     bookID,
		Position:   position,
	}

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}

	key := []byte(fmt.Sprintf("playlistitem:%d:%d", playlistID, position))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetPlaylistItems(playlistID int) ([]PlaylistItem, error) {
	var items []PlaylistItem
	prefix := []byte(fmt.Sprintf("playlistitem:%d:", playlistID))

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var item PlaylistItem
		if err := json.Unmarshal(iter.Value(), &item); err != nil {
			continue
		}
		items = append(items, item)
	}

	return items, nil
}

// ErrUserPlaylistVersionConflict is returned by UpdateUserPlaylist when the
// caller's Version does not match the stored row's: the playlist changed
// between the caller's read and its write. Callers re-read, re-apply their
// change and retry (see UpdateUserPlaylistWithRetry); a whole-record
// last-writer-wins overwrite is what this exists to prevent.
var ErrUserPlaylistVersionConflict = errors.New("playlist version conflict")

// ErrUserPlaylistNameInUse is returned by CreateUserPlaylist and
// UpdateUserPlaylist when another playlist already holds the name (compared
// after util.NormalizeString). The name index is GLOBAL across users by design
// (GetUserPlaylistByName, used by the iTunes import, resolves by name alone),
// so the message is deliberately generic: it confirms only that the caller's
// own requested name is taken, never whose playlist holds it.
var ErrUserPlaylistNameInUse = errors.New("a playlist with that name already exists")

// userPlaylistWriteMu serializes every user-playlist write in this process so
// UpdateUserPlaylist's read → version compare → commit is ATOMIC. Without it two
// writers (the native /api/v1 handlers and the ABS handlers, or the iTunes
// dirty-clear) could both read the same Version, both pass the compare, and the
// second commit would silently drop the first one's change. Playlist writes are
// rare user actions, so one process-wide lock costs nothing measurable.
var userPlaylistWriteMu sync.Mutex

func (p *PebbleStore) CreateUserPlaylist(pl *UserPlaylist) (*UserPlaylist, error) {
	userPlaylistWriteMu.Lock()
	defer userPlaylistWriteMu.Unlock()
	if pl == nil || pl.Name == "" {
		return nil, fmt.Errorf("playlist: name required")
	}
	if pl.Type != UserPlaylistTypeStatic && pl.Type != UserPlaylistTypeSmart {
		return nil, fmt.Errorf("playlist: type must be static or smart")
	}
	if pl.ID == "" {
		id, err := newULID()
		if err != nil {
			return nil, err
		}
		pl.ID = id
	}
	lower := util.NormalizeString(pl.Name)
	if v, closer, err := p.db.Get([]byte("idx:upl:name:" + lower)); err == nil {
		existing := string(v)
		closer.Close()
		if existing != pl.ID {
			return nil, ErrUserPlaylistNameInUse
		}
	}
	now := time.Now()
	if pl.CreatedAt.IsZero() {
		pl.CreatedAt = now
	}
	pl.UpdatedAt = now
	if pl.Version == 0 {
		pl.Version = 1
	}
	data, err := json.Marshal(pl)
	if err != nil {
		return nil, err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("upl:"+pl.ID), data, nil); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.Set([]byte("idx:upl:name:"+lower), []byte(pl.ID), nil); err != nil {
		b.Close()
		return nil, err
	}
	if pl.ITunesPersistentID != "" {
		if err := b.Set([]byte("idx:upl:itunes:"+pl.ITunesPersistentID), []byte(pl.ID), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if pl.Dirty {
		if err := b.Set([]byte("idx:upl:dirty:"+pl.ID), []byte("1"), nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return pl, nil
}

func (p *PebbleStore) GetUserPlaylist(id string) (*UserPlaylist, error) {
	data, closer, err := p.db.Get([]byte("upl:" + id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var pl UserPlaylist
	if err := json.Unmarshal(data, &pl); err != nil {
		return nil, err
	}
	return &pl, nil
}

func (p *PebbleStore) GetUserPlaylistByName(name string) (*UserPlaylist, error) {
	// NormalizeString, matching the three writers of idx:upl:name: (create,
	// update, delete). NormalizeAuthor collapses internal whitespace since
	// 2026-09-12 and would miss a stored "my  list" key.
	v, closer, err := p.db.Get([]byte("idx:upl:name:" + util.NormalizeString(name)))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := string(v)
	closer.Close()
	return p.GetUserPlaylist(id)
}

func (p *PebbleStore) GetUserPlaylistByITunesPID(pid string) (*UserPlaylist, error) {
	if pid == "" {
		return nil, nil
	}
	v, closer, err := p.db.Get([]byte("idx:upl:itunes:" + pid))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id := string(v)
	closer.Close()
	return p.GetUserPlaylist(id)
}

func (p *PebbleStore) ListUserPlaylists(playlistType string, limit, offset int) ([]UserPlaylist, int, error) {
	// matchUser=false → all users.
	return p.listUserPlaylists(playlistType, "", false, limit, offset)
}

// ListUserPlaylistsForUser returns only playlists created by userID.
func (p *PebbleStore) ListUserPlaylistsForUser(userID, playlistType string, limit, offset int) ([]UserPlaylist, int, error) {
	return p.listUserPlaylists(playlistType, userID, true, limit, offset)
}

// listUserPlaylists scans all user playlists, optionally filtering by type and
// (when matchUser is true) by CreatedByUserID == userFilter, then paginates.
func (p *PebbleStore) listUserPlaylists(playlistType, userFilter string, matchUser bool, limit, offset int) ([]UserPlaylist, int, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("upl:"),
		UpperBound: []byte("upl:~"),
	})
	if err != nil {
		return nil, 0, err
	}
	defer iter.Close()
	var all []UserPlaylist
	for iter.First(); iter.Valid(); iter.Next() {
		var pl UserPlaylist
		if err := json.Unmarshal(iter.Value(), &pl); err != nil {
			continue
		}
		if playlistType != "" && pl.Type != playlistType {
			continue
		}
		// When scoping to a user, include that user's playlists AND legacy
		// unowned rows (empty CreatedByUserID, e.g. pre-ownership iTunes
		// imports) so the refactor doesn't hide pre-existing data. This mirrors
		// the handler's ownedByCaller semantics.
		if matchUser && pl.CreatedByUserID != "" && pl.CreatedByUserID != userFilter {
			continue
		}
		all = append(all, pl)
	}
	total := len(all)
	if offset >= total {
		return []UserPlaylist{}, total, nil
	}
	end := offset + limit
	if limit <= 0 || end > total {
		end = total
	}
	return all[offset:end], total, nil
}

func (p *PebbleStore) UpdateUserPlaylist(pl *UserPlaylist) error {
	if pl == nil || pl.ID == "" {
		return fmt.Errorf("playlist id required")
	}
	userPlaylistWriteMu.Lock()
	defer userPlaylistWriteMu.Unlock()
	prev, err := p.GetUserPlaylist(pl.ID)
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("playlist %s not found", pl.ID)
	}
	// Compare-and-swap on Version, atomic under userPlaylistWriteMu. Plain
	// equality, exactly like UpdateCollection: a caller that read the row has
	// pl.Version == prev.Version by construction, and a row stored before the
	// field existed reads back 0 on both sides.
	if pl.Version != prev.Version {
		return fmt.Errorf("playlist %s: %w: expected %d, got %d", pl.ID, ErrUserPlaylistVersionConflict, prev.Version, pl.Version)
	}
	// The name index is global; a rename onto a name another playlist holds
	// must be refused, or the index would be silently repointed and the other
	// playlist would become unreachable by name.
	if lower := util.NormalizeString(pl.Name); lower != util.NormalizeString(prev.Name) {
		if v, closer, gerr := p.db.Get([]byte("idx:upl:name:" + lower)); gerr == nil {
			existing := string(v)
			closer.Close()
			if existing != pl.ID {
				return ErrUserPlaylistNameInUse
			}
		}
	}
	pl.CreatedAt = prev.CreatedAt
	pl.UpdatedAt = time.Now()
	pl.Version = prev.Version + 1
	data, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	if err := b.Set([]byte("upl:"+pl.ID), data, nil); err != nil {
		b.Close()
		return err
	}
	if !strings.EqualFold(prev.Name, pl.Name) {
		if err := b.Delete([]byte("idx:upl:name:"+util.NormalizeString(prev.Name)), nil); err != nil {
			b.Close()
			return err
		}
		if err := b.Set([]byte("idx:upl:name:"+util.NormalizeString(pl.Name)), []byte(pl.ID), nil); err != nil {
			b.Close()
			return err
		}
	}
	if prev.ITunesPersistentID != pl.ITunesPersistentID {
		if prev.ITunesPersistentID != "" {
			if err := b.Delete([]byte("idx:upl:itunes:"+prev.ITunesPersistentID), nil); err != nil {
				b.Close()
				return err
			}
		}
		if pl.ITunesPersistentID != "" {
			if err := b.Set([]byte("idx:upl:itunes:"+pl.ITunesPersistentID), []byte(pl.ID), nil); err != nil {
				b.Close()
				return err
			}
		}
	}
	if pl.Dirty {
		if err := b.Set([]byte("idx:upl:dirty:"+pl.ID), []byte("1"), nil); err != nil {
			b.Close()
			return err
		}
	} else if prev.Dirty {
		if err := b.Delete([]byte("idx:upl:dirty:"+pl.ID), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) DeleteUserPlaylist(id string) error {
	userPlaylistWriteMu.Lock()
	defer userPlaylistWriteMu.Unlock()
	pl, err := p.GetUserPlaylist(id)
	if err != nil {
		return err
	}
	if pl == nil {
		return nil
	}
	b := p.db.NewBatch()
	if err := b.Delete([]byte("upl:"+id), nil); err != nil {
		b.Close()
		return err
	}
	if err := b.Delete([]byte("idx:upl:name:"+util.NormalizeString(pl.Name)), nil); err != nil {
		b.Close()
		return err
	}
	if pl.ITunesPersistentID != "" {
		if err := b.Delete([]byte("idx:upl:itunes:"+pl.ITunesPersistentID), nil); err != nil {
			b.Close()
			return err
		}
	}
	if pl.Dirty {
		if err := b.Delete([]byte("idx:upl:dirty:"+id), nil); err != nil {
			b.Close()
			return err
		}
	}
	return b.Commit(pebble.Sync)
}

func (p *PebbleStore) ListDirtyUserPlaylists() ([]UserPlaylist, error) {
	// Only a missing record (the getter's (nil, nil)) is a stale index entry
	// that may be skipped; any other read error fails the list. Until
	// 2026-09-12 every error was skipped and the scan's own read error was
	// never checked, so an unreadable member came back as a short list.
	var out []UserPlaylist
	prefix := []byte("idx:upl:dirty:")
	if err := forEachKeyInRange(p.db, prefix, []byte("idx:upl:dirty:~"), func(key, _ []byte) error {
		id := string(key[len(prefix):])
		pl, err := p.GetUserPlaylist(id)
		if err != nil {
			return fmt.Errorf("ListDirtyUserPlaylists: reading playlist %s: %w", id, err)
		}
		if pl != nil {
			out = append(out, *pl)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}
