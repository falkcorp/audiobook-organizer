// file: internal/itunes/plist_parser.go
// version: 1.3.0
// guid: d1f3e5c7-a9b1-c3d5-e7f9-1a3b5c7d9e1f

package itunes

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"howett.net/plist"
)

// plistLibrary represents the raw plist structure from iTunes
type plistLibrary struct {
	MajorVersion       int                    `plist:"Major Version"`
	MinorVersion       int                    `plist:"Minor Version"`
	ApplicationVersion string                 `plist:"Application Version"`
	MusicFolder        string                 `plist:"Music Folder"`
	Tracks             map[string]*plistTrack `plist:"Tracks"`
	Playlists          []*plistPlaylist       `plist:"Playlists"`
}

// plistTrack represents a single track in the plist format
type plistTrack struct {
	TrackID      int       `plist:"Track ID"`
	PersistentID string    `plist:"Persistent ID"`
	Name         string    `plist:"Name"`
	Artist       string    `plist:"Artist"`
	AlbumArtist  string    `plist:"Album Artist"`
	Album        string    `plist:"Album"`
	Grouping     string    `plist:"Grouping"`
	Genre        string    `plist:"Genre"`
	Kind         string    `plist:"Kind"`
	Year         int       `plist:"Year"`
	Comments     string    `plist:"Comments"`
	Location     string    `plist:"Location"`
	Size         int64     `plist:"Size"`
	TotalTime    int64     `plist:"Total Time"` // milliseconds
	DateAdded    time.Time `plist:"Date Added"`
	PlayCount    int       `plist:"Play Count"`
	PlayDate     int64     `plist:"Play Date"` // Unix timestamp
	PlayDateUTC  time.Time `plist:"Play Date UTC"`
	Rating       int       `plist:"Rating"`   // 0-100 scale
	Bookmark     int64     `plist:"Bookmark"` // milliseconds
	Bookmarkable bool      `plist:"Bookmarkable"`
	TrackNumber  int       `plist:"Track Number"`
	TrackCount   int       `plist:"Track Count"`
	DiscNumber   int       `plist:"Disc Number"`
	DiscCount    int       `plist:"Disc Count"`
}

// plistPlaylist represents a playlist in the plist format
type plistPlaylist struct {
	Name          string               `plist:"Name"`
	PlaylistID    int                  `plist:"Playlist ID"`
	PlaylistItems []*plistPlaylistItem `plist:"Playlist Items"`
}

// plistPlaylistItem represents a track reference in a playlist
type plistPlaylistItem struct {
	TrackID int `plist:"Track ID"`
}

// parsePlist parses an iTunes plist format XML file
func parsePlist(data []byte) (*Library, error) {
	var raw plistLibrary

	// Decode the plist XML
	_, err := plist.Unmarshal(data, &raw)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal plist: %w", err)
	}

	// Convert to our internal Library structure
	library := &Library{
		MajorVersion:       raw.MajorVersion,
		MinorVersion:       raw.MinorVersion,
		ApplicationVersion: raw.ApplicationVersion,
		MusicFolder:        raw.MusicFolder,
		Tracks:             make(map[string]*Track),
		Playlists:          make([]*Playlist, 0, len(raw.Playlists)),
	}

	// Convert tracks
	for id, rawTrack := range raw.Tracks {
		// Guard against int64 overflow: iTunes occasionally writes file sizes
		// as unsigned 64-bit integers. Values > math.MaxInt64 wrap negative
		// when parsed into int64; reset to 0 so the os.Stat fallback in
		// buildBookFromTrack populates the correct value.
		size := rawTrack.Size
		if size < 0 {
			size = 0
		}

		library.Tracks[id] = &Track{
			TrackID:      rawTrack.TrackID,
			PersistentID: rawTrack.PersistentID,
			Name:         rawTrack.Name,
			Artist:       rawTrack.Artist,
			AlbumArtist:  rawTrack.AlbumArtist,
			Album:        rawTrack.Album,
			Genre:        rawTrack.Genre,
			Kind:         rawTrack.Kind,
			Year:         rawTrack.Year,
			Comments:     rawTrack.Comments,
			Location:     rawTrack.Location,
			Size:         size,
			TotalTime:    rawTrack.TotalTime,
			DateAdded:    rawTrack.DateAdded,
			PlayCount:    rawTrack.PlayCount,
			PlayDate:     rawTrack.PlayDate,
			Rating:       rawTrack.Rating,
			Bookmark:     rawTrack.Bookmark,
			Bookmarkable: rawTrack.Bookmarkable,
			TrackNumber:  rawTrack.TrackNumber,
			TrackCount:   rawTrack.TrackCount,
			DiscNumber:   rawTrack.DiscNumber,
			DiscCount:    rawTrack.DiscCount,
		}
	}

	// Convert playlists
	for _, rawPlaylist := range raw.Playlists {
		if rawPlaylist.PlaylistItems == nil {
			continue
		}

		trackIDs := make([]int, 0, len(rawPlaylist.PlaylistItems))
		for _, item := range rawPlaylist.PlaylistItems {
			trackIDs = append(trackIDs, item.TrackID)
		}

		library.Playlists = append(library.Playlists, &Playlist{
			PlaylistID: rawPlaylist.PlaylistID,
			Name:       rawPlaylist.Name,
			TrackIDs:   trackIDs,
		})
	}

	return library, nil
}

// writePlist writes a Library structure to an iTunes plist XML file
func writePlist(library *Library, path string) error {
	// Convert from our internal structure to plist structure
	raw := &plistLibrary{
		MajorVersion:       library.MajorVersion,
		MinorVersion:       library.MinorVersion,
		ApplicationVersion: library.ApplicationVersion,
		MusicFolder:        library.MusicFolder,
		Tracks:             make(map[string]*plistTrack),
		Playlists:          make([]*plistPlaylist, 0, len(library.Playlists)),
	}

	// Convert tracks
	for id, track := range library.Tracks {
		raw.Tracks[id] = &plistTrack{
			TrackID:      track.TrackID,
			PersistentID: track.PersistentID,
			Name:         track.Name,
			Artist:       track.Artist,
			AlbumArtist:  track.AlbumArtist,
			Album:        track.Album,
			Grouping:     track.Grouping,
			Genre:        track.Genre,
			Kind:         track.Kind,
			Year:         track.Year,
			Comments:     track.Comments,
			Location:     track.Location,
			Size:         track.Size,
			TotalTime:    track.TotalTime,
			DateAdded:    track.DateAdded,
			PlayCount:    track.PlayCount,
			PlayDate:     track.PlayDate,
			Rating:       track.Rating,
			Bookmark:     track.Bookmark,
			Bookmarkable: track.Bookmarkable,
			TrackNumber:  track.TrackNumber,
			TrackCount:   track.TrackCount,
			DiscNumber:   track.DiscNumber,
			DiscCount:    track.DiscCount,
		}
	}

	// Convert playlists
	for _, playlist := range library.Playlists {
		items := make([]*plistPlaylistItem, 0, len(playlist.TrackIDs))
		for _, trackID := range playlist.TrackIDs {
			items = append(items, &plistPlaylistItem{TrackID: trackID})
		}

		raw.Playlists = append(raw.Playlists, &plistPlaylist{
			Name:          playlist.Name,
			PlaylistID:    playlist.PlaylistID,
			PlaylistItems: items,
		})
	}

	// Create a temporary file
	tempFile := path + ".tmp"
	file, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func() {
		file.Close()
		os.Remove(tempFile) // Clean up temp file on error
	}()

	// Encode to plist XML format
	encoder := plist.NewEncoder(file)
	encoder.Indent("\t") // Use tabs for indentation like iTunes
	if err := encoder.Encode(raw); err != nil {
		return fmt.Errorf("failed to encode plist: %w", err)
	}

	// Close file before rename
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempFile, path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// StreamingParseLibrary reads an iTunes plist XML file and yields tracks via callback.
// Unlike ParseLibrary which loads the entire file into memory, this streams through
// the XML and calls onTrack for each track found. This prevents the 53GB memory spike
// on large iTunes libraries (88K+ tracks). Context cancellation is respected.
func StreamingParseLibrary(ctx context.Context, path string, onTrack func(*Track) error) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	file, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("failed to open iTunes library file: %w", err)
	}
	defer file.Close()

	decoder := xml.NewDecoder(file)
	count := 0
	inTracksDict := false

	for {
		if err := ctx.Err(); err != nil {
			return count, nil // Cancellation is not an error, just stop
		}

		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return count, fmt.Errorf("XML decode error: %w", err)
		}

		// Look for the <key>Tracks</key><dict> section
		if keyToken, ok := token.(xml.CharData); ok {
			if inTracksDict && string(keyToken) == "Tracks" {
				// Next token should be the opening <dict>
				token, err := decoder.Token()
				if err != nil {
					return count, fmt.Errorf("failed to read Tracks dict: %w", err)
				}
				if _, ok := token.(xml.StartElement); ok {
					// Now parse track dicts
					count, err = parseStreamingTracks(ctx, decoder, onTrack)
					return count, err
				}
			}
		}

		// Look for root plist dict
		if startElem, ok := token.(xml.StartElement); ok {
			if startElem.Name.Local == "dict" && !inTracksDict {
				inTracksDict = true
			}
		}
	}

	return count, nil
}

// parseStreamingTracks reads track dict entries and yields them via callback
func parseStreamingTracks(ctx context.Context, decoder *xml.Decoder, onTrack func(*Track) error) (int, error) {
	count := 0

	for {
		if err := ctx.Err(); err != nil {
			return count, nil
		}

		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return count, err
		}

		startElem, ok := token.(xml.StartElement)
		if !ok {
			// Look for </dict> to end the Tracks section
			endElem, ok := token.(xml.EndElement)
			if ok && endElem.Name.Local == "dict" {
				break
			}
			continue
		}

		// Each track is a <key>id</key><dict>...fields...</dict> pair
		if startElem.Name.Local == "key" {
			// Read the track ID
			var trackID string
			if err := decoder.DecodeElement(&trackID, &startElem); err != nil {
				continue
			}

			// Next should be the track's <dict> element
			token, err := decoder.Token()
			if err != nil {
				if err == io.EOF {
					break
				}
				continue
			}

			dictElem, ok := token.(xml.StartElement)
			if !ok || dictElem.Name.Local != "dict" {
				continue
			}

			// Parse the track dict
			track, err := parseStreamingTrackDict(decoder)
			if err != nil {
				continue
			}

			// Callback with the track
			if err := onTrack(track); err != nil {
				return count, err
			}
			count++

			// Log progress every 10K tracks
			if count%10000 == 0 {
				// Progress logging happens at caller level
			}
		}
	}

	return count, nil
}

// parseStreamingTrackDict unmarshals a single track from its dict element
func parseStreamingTrackDict(decoder *xml.Decoder) (*Track, error) {
	trackData := make(map[string]string)
	var currentKey string

	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		switch elem := token.(type) {
		case xml.StartElement:
			if elem.Name.Local == "key" {
				var key string
				if err := decoder.DecodeElement(&key, &elem); err == nil {
					currentKey = key
				}
			} else if elem.Name.Local == "string" {
				var val string
				if err := decoder.DecodeElement(&val, &elem); err == nil {
					trackData[currentKey] = val
				}
			} else if elem.Name.Local == "integer" {
				var val int
				if err := decoder.DecodeElement(&val, &elem); err == nil {
					trackData[currentKey] = strconv.Itoa(val)
				}
			} else if elem.Name.Local == "real" {
				var val float64
				if err := decoder.DecodeElement(&val, &elem); err == nil {
					trackData[currentKey] = fmt.Sprintf("%f", val)
				}
			} else if elem.Name.Local == "true" {
				trackData[currentKey] = "true"
			} else if elem.Name.Local == "false" {
				trackData[currentKey] = "false"
			} else if elem.Name.Local == "date" {
				var val time.Time
				if err := decoder.DecodeElement(&val, &elem); err == nil {
					trackData[currentKey] = val.Format(time.RFC3339)
				}
			}

		case xml.EndElement:
			if elem.Name.Local == "dict" {
				// End of track dict, convert to Track struct
				return buildTrackFromDict(trackData), nil
			}
		}
	}
}

// buildTrackFromDict converts parsed dict data to a Track struct
func buildTrackFromDict(data map[string]string) *Track {
	track := &Track{}

	if v := data["Track ID"]; v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			track.TrackID = id
		}
	}
	track.PersistentID = data["Persistent ID"]
	track.Name = data["Name"]
	track.Artist = data["Artist"]
	track.AlbumArtist = data["Album Artist"]
	track.Album = data["Album"]
	track.Grouping = data["Grouping"]
	track.Genre = data["Genre"]
	track.Kind = data["Kind"]

	if v := data["Year"]; v != "" {
		if year, err := strconv.Atoi(v); err == nil {
			track.Year = year
		}
	}

	track.Comments = data["Comments"]
	track.Location = data["Location"]

	if v := data["Size"]; v != "" {
		if size, err := strconv.ParseInt(v, 10, 64); err == nil && size >= 0 {
			track.Size = size
		}
	}

	if v := data["Total Time"]; v != "" {
		if t, err := strconv.ParseInt(v, 10, 64); err == nil {
			track.TotalTime = t
		}
	}

	if v := data["Date Added"]; v != "" {
		if dt, err := time.Parse(time.RFC3339, v); err == nil {
			track.DateAdded = dt
		}
	}

	if v := data["Play Count"]; v != "" {
		if pc, err := strconv.Atoi(v); err == nil {
			track.PlayCount = pc
		}
	}

	if v := data["Play Date"]; v != "" {
		if pd, err := strconv.ParseInt(v, 10, 64); err == nil {
			track.PlayDate = pd
		}
	}

	if v := data["Rating"]; v != "" {
		if r, err := strconv.Atoi(v); err == nil {
			track.Rating = r
		}
	}

	if v := data["Bookmark"]; v != "" {
		if bm, err := strconv.ParseInt(v, 10, 64); err == nil {
			track.Bookmark = bm
		}
	}

	track.Bookmarkable = strings.ToLower(data["Bookmarkable"]) == "true"

	if v := data["Track Number"]; v != "" {
		if tn, err := strconv.Atoi(v); err == nil {
			track.TrackNumber = tn
		}
	}

	if v := data["Track Count"]; v != "" {
		if tc, err := strconv.Atoi(v); err == nil {
			track.TrackCount = tc
		}
	}

	if v := data["Disc Number"]; v != "" {
		if dn, err := strconv.Atoi(v); err == nil {
			track.DiscNumber = dn
		}
	}

	if v := data["Disc Count"]; v != "" {
		if dc, err := strconv.Atoi(v); err == nil {
			track.DiscCount = dc
		}
	}

	return track
}
