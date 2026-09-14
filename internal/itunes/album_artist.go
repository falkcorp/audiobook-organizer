// file: internal/itunes/album_artist.go
// version: 1.0.0
// guid: 3b9f2c6e-8d41-4a7e-b5c3-1f0e9a2d7c84
// last-edited: 2026-09-14

package itunes

import "strings"

// AuthorAndNarrator returns a track's author and narrator from its Artist and
// Album Artist fields.
//
// Album Artist is the AUTHOR (owner decision 2026-09-14). The importers read
// it as the narrator until then, while the file-tag readers
// (metadata.BuildMetadataFromTag, BuildMetadataFromTaglibMap) read the
// ALBUMARTIST / aART / TPE2 tag as the author, so one book imported from the
// iTunes XML and scanned from its file got two different authors. This uses
// the readers' precedence: Album Artist, else Artist, is the author; when
// both are set and differ, Artist is the narrator, as the readers take ARTIST
// as the narrator when ALBUMARTIST supplied the author.
func AuthorAndNarrator(t *Track) (author, narrator string) {
	if t == nil {
		return "", ""
	}
	albumArtist := strings.TrimSpace(t.AlbumArtist)
	artist := strings.TrimSpace(t.Artist)
	if albumArtist == "" {
		return artist, ""
	}
	if artist != "" && artist != albumArtist {
		return albumArtist, artist
	}
	return albumArtist, ""
}
