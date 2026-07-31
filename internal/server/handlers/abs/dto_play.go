// file: internal/server/handlers/abs/dto_play.go
// version: 1.0.0
// guid: 4f7d20b8-6c19-4a53-8e72-9b0af35d61c7
// last-edited: 2026-07-30

package abs

// playSessionResponse is the body of POST /api/items/:id/play.
//
// AudioBooth decodes PlaySession requiring id, userId, libraryItemId, currentTime,
// duration AND a complete embedded libraryItem (§1.8.5 item 6). Two fields carry
// requirements that are easy to violate silently:
//
//	AudioTracks — `omitempty` on a NIL-able slice, and that combination is the whole
//	  point. An explicit "audioTracks": [] defeats AudioBooth's `?? orderedTracks`
//	  local-track fallback, which only fires on nil, and KILLS PLAYBACK of an
//	  already-downloaded book (§1.8.5 item 3). Assign only from h.audioTracks, which
//	  returns nil rather than an empty slice.
//
//	CurrentTime / StartTime — must be the user's TRUE latest position, never 0 and
//	  never a session-start snapshot: AudioBooth takes max() on position at session
//	  start while ignoring timestamps, so a 0 here rewinds the user (§1.8.7).
//
// PlayMethod is always 0 (direct play). We ship no transcoder and no HLS packager, so
// there is no other honest value, and no hlsPlaylistUrl key exists for a client to
// wait on.
type playSessionResponse struct {
	AudioTracks   []audioTrackDTO        `json:"audioTracks,omitempty"`
	BookID        string                 `json:"bookId"`
	Chapters      []chapterDTO           `json:"chapters"`
	CoverPath     *string                `json:"coverPath"`
	CurrentTime   float64                `json:"currentTime"`
	Date          string                 `json:"date"`
	DayOfWeek     string                 `json:"dayOfWeek"`
	DeviceInfo    map[string]any         `json:"deviceInfo"`
	DisplayAuthor string                 `json:"displayAuthor"`
	DisplayTitle  string                 `json:"displayTitle"`
	Duration      float64                `json:"duration"`
	EpisodeID     *string                `json:"episodeId"`
	ID            string                 `json:"id"`
	LibraryID     string                 `json:"libraryId"`
	LibraryItem   libraryItemExpandedDTO `json:"libraryItem"`
	LibraryItemID string                 `json:"libraryItemId"`
	MediaMetadata bookMetadataDTO        `json:"mediaMetadata"`
	MediaPlayer   string                 `json:"mediaPlayer"`
	MediaType     string                 `json:"mediaType"`
	PlayMethod    int                    `json:"playMethod"`
	ServerVersion string                 `json:"serverVersion"`
	StartTime     float64                `json:"startTime"`
	StartedAt     int64                  `json:"startedAt"`
	TimeListening float64                `json:"timeListening"`
	UpdatedAt     int64                  `json:"updatedAt"`
	UserID        string                 `json:"userId"`
}
