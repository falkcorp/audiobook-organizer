// file: internal/metadata/album_artist_guard.go
// version: 1.0.0
// guid: 7e1a4d92-c35b-4f08-9a6d-2b8e5c0f13a7
// last-edited: 2026-09-14

package metadata

import "strings"

// AlbumArtistIsNarrator reports whether a file's ALBUMARTIST value must not be
// taken as the book's author because it names the narrator.
//
// ALBUMARTIST is the author (owner decision 2026-09-14) and every tag reader
// takes it first. Some files carry the narrator there instead: organize wrote
// the narrator into ALBUMARTIST between da064ef4c and c81b39801 on 2026-09-13,
// and other taggers use the field that way. When ALBUMARTIST equals the file's
// own narrator tag (NARRATOR / PERFORMER / READER) and ARTIST names someone
// else, ARTIST is the author. Values are compared case-insensitively after
// trimming; an empty argument never triggers the guard.
func AlbumArtistIsNarrator(albumArtist, artist, narrator string) bool {
	albumArtist = strings.TrimSpace(albumArtist)
	artist = strings.TrimSpace(artist)
	narrator = strings.TrimSpace(narrator)
	if albumArtist == "" || artist == "" || narrator == "" {
		return false
	}
	return strings.EqualFold(albumArtist, narrator) && !strings.EqualFold(artist, albumArtist)
}
