// file: internal/database/iface_user.go
// version: 1.2.0
// guid: ca96abf5-5353-428c-aa7f-903b91a481e8

package database

import "time"

// UserReader is the read-only user slice.
type UserReader interface {
	GetUserByID(id string) (*User, error)
	GetUserByUsername(username string) (*User, error)
	GetUserByEmail(email string) (*User, error)
	ListUsers() ([]User, error)
	CountUsers() (int, error)
}

// UserWriter is the write-only user slice.
type UserWriter interface {
	CreateUser(username, email, passwordHashAlgo, passwordHash string, roles []string, status string) (*User, error)
	UpdateUser(user *User) error
}

// UserStore combines both halves.
type UserStore interface {
	UserReader
	UserWriter
}

// Split out of iface_misc.go on 2026-08-18, which held 27 interface
// declarations in one file. A file named `misc` is where wide interfaces go to
// avoid review: BookFileStore reached 27 methods while living there.

// UserPositionStore covers per-user position + derived book state.
type UserPositionStore interface {
	SetUserPosition(userID, bookID, segmentID string, positionSeconds float64) error
	GetUserPosition(userID, bookID string) (*UserPosition, error)
	ListUserPositionsForBook(userID, bookID string) ([]UserPosition, error)
	ClearUserPositions(userID, bookID string) error
	SetUserBookState(state *UserBookState) error
	GetUserBookState(userID, bookID string) (*UserBookState, error)
	ListUserBookStatesByStatus(userID, status string, limit, offset int) ([]UserBookState, error)
	ListUserPositionsSince(userID string, t time.Time) ([]UserPosition, error)
}

// UserPreferenceStore covers both global and per-user preferences.
type UserPreferenceStore interface {
	GetUserPreference(key string) (*UserPreference, error)
	SetUserPreference(key, value string) error
	GetAllUserPreferences() ([]UserPreference, error)
	SetUserPreferenceForUser(userID, key, value string) error
	GetUserPreferenceForUser(userID, key string) (*UserPreferenceKV, error)
	GetAllPreferencesForUser(userID string) ([]UserPreferenceKV, error)
}

// UserPlaylistReader looks up and lists playlists.
type UserPlaylistReader interface {
	GetUserPlaylist(id string) (*UserPlaylist, error)
	GetUserPlaylistByName(name string) (*UserPlaylist, error)
	GetUserPlaylistByITunesPID(pid string) (*UserPlaylist, error)
	ListUserPlaylists(playlistType string, limit, offset int) ([]UserPlaylist, int, error)
	// ListUserPlaylistsForUser returns only playlists created by userID. Used by
	// the API to scope list results per user (prevents cross-user disclosure).
	// An empty userID matches playlists with an empty CreatedByUserID.
	ListUserPlaylistsForUser(userID, playlistType string, limit, offset int) ([]UserPlaylist, int, error)
	ListDirtyUserPlaylists() ([]UserPlaylist, error)
}

// UserPlaylistWriter creates, updates and deletes playlists.
type UserPlaylistWriter interface {
	CreateUserPlaylist(pl *UserPlaylist) (*UserPlaylist, error)
	UpdateUserPlaylist(pl *UserPlaylist) error
	DeleteUserPlaylist(id string) error
}

// UserPlaylistStore covers smart + static user playlists (spec 3.4).
//
// Split into the 2 interfaces above on 2026-08-18. This name is retained as
// their composition so the method set is byte-identical and no consumer moves; the
// type checker proves it.
type UserPlaylistStore interface {
	UserPlaylistReader
	UserPlaylistWriter
}

// PlaylistStore covers the legacy series-playlist auto-generator.
type PlaylistStore interface {
	CreatePlaylist(name string, seriesID *int, filePath string) (*Playlist, error)
	GetPlaylistByID(id int) (*Playlist, error)
	GetPlaylistBySeriesID(seriesID int) (*Playlist, error)
	AddPlaylistItem(playlistID, bookID, position int) error
	GetPlaylistItems(playlistID int) ([]PlaylistItem, error)
}

// PlaybackStore covers playback events, progress, and stats.
type PlaybackStore interface {
	AddPlaybackEvent(event *PlaybackEvent) error
	ListPlaybackEvents(userID string, bookNumericID int, limit int) ([]PlaybackEvent, error)
	UpdatePlaybackProgress(progress *PlaybackProgress) error
	GetPlaybackProgress(userID string, bookNumericID int) (*PlaybackProgress, error)
	IncrementBookPlayStats(bookNumericID int, seconds int) error
	GetBookStats(bookNumericID int) (*BookStats, error)
	IncrementUserListenStats(userID string, seconds int) error
	GetUserStats(userID string) (*UserStats, error)
}
