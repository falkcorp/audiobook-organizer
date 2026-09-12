// file: internal/itunes/itl_convert.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-09-11

package itunes

import (
	"fmt"
	"strings"
)

// ParseITLAsLibrary parses an iTunes .itl binary file and returns a Library
// structure identical to what ParseLibrary returns for XML files.
// This allows the sync code to accept either format transparently.
func ParseITLAsLibrary(path string) (*Library, error) {
	itlLib, err := ParseITL(path)
	if err != nil {
		return nil, fmt.Errorf("parsing ITL file: %w", err)
	}

	lib := &Library{
		Tracks:    make(map[string]*Track),
		Playlists: make([]*Playlist, 0, len(itlLib.Playlists)),
		// No bookmark is decoded from the ITL, so every Track.Bookmark below
		// is 0 meaning "unknown". Declaring that stops the importer from
		// writing it over a stored bookmark (see SourceFields).
		Carries: ITLSourceFields(),
	}

	for _, t := range itlLib.Tracks {
		// Convert PID to uppercase hex to match XML format.
		// The LE parser already reverses bytes so t.PersistentID is in
		// big-endian / MSB-first order (same as the XML persistent ID).
		// Use pidToHex (plain, no reversal) and uppercase to match XML.
		pid := strings.ToUpper(pidToHex(t.PersistentID))

		track := &Track{
			TrackID:      t.TrackID,
			PersistentID: pid,
			Name:         t.Name,
			Artist:       t.Artist,
			Album:        t.Album,
			Genre:        t.Genre,
			Kind:         t.Kind,
			Size:         int64(t.Size),
			TotalTime:    int64(t.TotalTime),
			TrackNumber:  t.TrackNumber,
			TrackCount:   t.TrackCount,
			DiscNumber:   t.DiscNumber,
			DiscCount:    t.DiscCount,
			Year:         t.Year,
			PlayCount:    t.PlayCount,
			Rating:       t.Rating,
			DateAdded:    t.DateAdded,
			Bookmarkable: true, // ITL tracks don't expose this flag; assume true
		}

		// Prefer Location (hohm 0x0D); fall back to LocalURL (hohm 0x0B).
		track.Location = t.Location
		if track.Location == "" && t.LocalURL != "" {
			track.Location = t.LocalURL
		}

		// Convert LastPlayDate to Unix timestamp.
		if !t.LastPlayDate.IsZero() {
			track.PlayDate = t.LastPlayDate.Unix()
		}

		// Use the track ID as the string map key (same convention as XML parser).
		key := fmt.Sprintf("%d", t.TrackID)
		lib.Tracks[key] = track
	}

	for _, p := range itlLib.Playlists {
		pl := &Playlist{
			Name:     p.Title,
			TrackIDs: make([]int, len(p.Items)),
		}
		copy(pl.TrackIDs, p.Items)
		lib.Playlists = append(lib.Playlists, pl)
	}

	return lib, nil
}
