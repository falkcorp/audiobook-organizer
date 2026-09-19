// file: internal/database/user_playlist_retry.go
// version: 1.0.0
// guid: a111a79d-270a-4d7a-8ef0-2e4778526e05
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// UserPlaylistReadWriter is the slice of the store UpdateUserPlaylistWithRetry
// needs.
type UserPlaylistReadWriter interface {
	GetUserPlaylist(id string) (*UserPlaylist, error)
	UpdateUserPlaylist(pl *UserPlaylist) error
}

// ErrUserPlaylistNotFound is returned by UpdateUserPlaylistWithRetry when the
// playlist disappeared (deleted between reads).
var ErrUserPlaylistNotFound = errors.New("playlist not found")

// userPlaylistMaxAttempts bounds the conflict retries. A conflict means another
// writer committed in between; each retry re-reads, so it only keeps failing
// under a sustained write storm on ONE playlist, which no user produces.
const userPlaylistMaxAttempts = 8

// UpdateUserPlaylistWithRetry is the only correct way to read-modify-write a
// user playlist: read the CURRENT row, apply mutate to it, write with the
// version compare, and on ErrUserPlaylistVersionConflict start over from a
// fresh read. Every writer (the native /api/v1 handlers and the ABS handlers)
// goes through this, so two concurrent edits compose — a rename and an add both
// land — instead of the second whole-record write silently erasing the first.
//
// mutate may return an error to abort without writing; that error is returned
// unchanged (callers use it to carry a status). mutate is re-run on every
// attempt against the fresh row, so it must be a pure function of the row and
// the request, never of state it captured from an earlier read.
func UpdateUserPlaylistWithRetry(store UserPlaylistReadWriter, id string, mutate func(pl *UserPlaylist) error) (*UserPlaylist, error) {
	var lastErr error
	for range userPlaylistMaxAttempts {
		pl, err := store.GetUserPlaylist(id)
		if err != nil {
			return nil, err
		}
		if pl == nil {
			return nil, ErrUserPlaylistNotFound
		}
		// Never mutate a slice the store (or a cache) may still share.
		pl.BookIDs = append([]string(nil), pl.BookIDs...)
		if err := mutate(pl); err != nil {
			return nil, err
		}
		err = store.UpdateUserPlaylist(pl)
		if err == nil {
			return pl, nil
		}
		if !errors.Is(err, ErrUserPlaylistVersionConflict) {
			return nil, err
		}
		lastErr = err
		// Jittered backoff so writers that collided do not re-collide in
		// lockstep on the next attempt.
		time.Sleep(time.Duration(1+rand.IntN(4)) * time.Millisecond)
	}
	return nil, fmt.Errorf("playlist %s: gave up after %d conflicting writes: %w", id, userPlaylistMaxAttempts, lastErr)
}
