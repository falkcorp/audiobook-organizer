// file: internal/database/pebble_store_playlists.go
// version: 1.0.0
// guid: b93ba897-1377-4cf7-9aea-ca57f135893e
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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

func (p *PebbleStore) CreateUserPlaylist(pl *UserPlaylist) (*UserPlaylist, error) {
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
			return nil, fmt.Errorf("playlist name %q already in use", pl.Name)
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
	v, closer, err := p.db.Get([]byte("idx:upl:name:" + util.NormalizeAuthor(name)))
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
	prev, err := p.GetUserPlaylist(pl.ID)
	if err != nil {
		return err
	}
	if prev == nil {
		return fmt.Errorf("playlist %s not found", pl.ID)
	}
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
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("idx:upl:dirty:"),
		UpperBound: []byte("idx:upl:dirty:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []UserPlaylist
	prefix := []byte("idx:upl:dirty:")
	for iter.First(); iter.Valid(); iter.Next() {
		id := string(iter.Key()[len(prefix):])
		pl, err := p.GetUserPlaylist(id)
		if err != nil || pl == nil {
			continue
		}
		out = append(out, *pl)
	}
	return out, nil
}
