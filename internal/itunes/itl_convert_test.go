// file: internal/itunes/itl_convert_test.go
// version: 1.0.1
// guid: b7e4c912-3f58-4d71-9a02-e8d61c5f47b3
// last-edited: 2026-10-30

package itunes

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers: build a minimal synthetic ITL file (LE / v12 format)
// ---------------------------------------------------------------------------

// mustBuildMhohLE wraps the production buildMhohLE (which now returns
// ([]byte, ok) — ok=false for hohmTypes absent from the corpus table) for use in
// fixtures where the type is known to be in the table.
func mustBuildMhohLE(hohmType uint32, value string) []byte {
	chunk, ok := buildMhohLE(hohmType, value)
	if !ok {
		panic("buildMhohLE: hohmType absent from corpus table in test fixture")
	}
	return chunk
}

// buildConvertTestITL constructs a minimal valid ITL binary containing one track
// and one playlist.  It uses the LE (v12) msdh format the real parser expects.
func buildConvertTestITL(t *testing.T) []byte {
	t.Helper()

	// PID for the test track — stored in LE byte order inside the binary.
	// The LE parser reverses these so ITLTrack.PersistentID will be {0xAB,…,0x12}.
	// pidToHex of that gives "ab89674523cd0112" and ToUpper → "AB89674523CD0112".
	pidLE := [8]byte{0x12, 0x01, 0xCD, 0x23, 0x45, 0x67, 0x89, 0xAB}

	trackContent := buildMithLeForConvert(1001, pidLE, 4096000, 28800000)
	trackContent = append(trackContent, mustBuildMhohLE(0x02, "Test Audiobook")...)  // Name
	trackContent = append(trackContent, mustBuildMhohLE(0x04, "Test Author")...)     // Artist
	trackContent = append(trackContent, mustBuildMhohLE(0x03, "Test Series")...)     // Album
	trackContent = append(trackContent, mustBuildMhohLE(0x05, "Audiobooks")...)      // Genre
	trackContent = append(trackContent, mustBuildMhohLE(0x06, "MPEG audio file")...) // Kind
	trackContent = append(trackContent, mustBuildMhohLE(0x0D,
		"file://localhost/mnt/bigdata/books/test.mp3")...) // Location

	trackMsdh := buildMsdhLE(0x01, trackContent)

	// Playlist: one miph + one mhoh title + one mtph referencing track 1001
	playlistContent := buildMiphLEMin(1)
	playlistContent = append(playlistContent, mustBuildMhohLE(0x64, "My Playlist")...) // title
	playlistContent = append(playlistContent, buildMtphLEMin(1001)...)

	playlistMsdh := buildMsdhLE(0x02, playlistContent)

	// Combine msdh blocks
	var payload bytes.Buffer
	payload.Write(trackMsdh)
	payload.Write(playlistMsdh)

	// Build a minimal hdfm header (version "12.13.10.3")
	version := "12.13.10.3"
	verBytes := []byte(version)
	// Header: "hdfm"(4) + headerLen(4) + fileLen(4) + unknown(4) + verLen(1) + version
	headerLen := 17 + len(verBytes)
	hdr := make([]byte, headerLen)
	copy(hdr[0:4], "hdfm")
	binary.BigEndian.PutUint32(hdr[4:8], uint32(headerLen))
	binary.BigEndian.PutUint32(hdr[8:12], uint32(headerLen+payload.Len()))
	binary.BigEndian.PutUint32(hdr[12:16], 0) // unknown
	hdr[16] = byte(len(verBytes))
	copy(hdr[17:], verBytes)

	// Encrypt the payload (itlEncrypt needs a hdfmHeader struct)
	fakeHdr := &hdfmHeader{
		headerLen: uint32(headerLen),
		version:   version,
	}
	encrypted := itlEncrypt(fakeHdr, payload.Bytes())

	var out bytes.Buffer
	out.Write(hdr)
	out.Write(encrypted)
	return out.Bytes()
}

// buildMithLeForConvert builds a 156-byte LE mith chunk for a given track.
// pidLE is the pid as it should appear in the binary (will be reversed by parser).
func buildMithLeForConvert(trackID int, pidLE [8]byte, size, totalTime int) []byte {
	buf := make([]byte, 156)
	copy(buf[0:4], "mith")
	putUint32LE(buf, 4, 156) // headerLen
	putUint32LE(buf, 8, 156) // totalLen
	putUint32LE(buf, 16, uint32(trackID))
	putUint32LE(buf, 36, uint32(size))
	putUint32LE(buf, 40, uint32(totalTime))
	putUint16LE(buf, 44, 1)    // TrackNumber
	putUint16LE(buf, 48, 10)   // TrackCount
	putUint16LE(buf, 54, 2023) // Year
	putUint16LE(buf, 104, 1)   // DiscNumber
	putUint16LE(buf, 106, 3)   // DiscCount
	buf[108] = 60              // Rating
	putUint32LE(buf, 76, 7)    // PlayCount
	// DateAdded: Mac epoch + 1 second (non-zero)
	putUint32LE(buf, 120, 1)
	// PID stored in LE byte order
	copy(buf[128:136], pidLE[:])
	return buf
}

// buildMiphLEMin builds a minimal miph (LE playlist header) chunk.
// The LE parser expects "miph" (not "hpim").
// It reads PID at remaining[420:428] = offset 20+420 = 440 if the chunk is big enough.
// Our minimal chunk skips the PID (just enough to satisfy the parser).
func buildMiphLEMin(itemCount int) []byte {
	// Make it large enough that parseMiphLE won't try to read out of bounds
	// (length-20 must be < 428, so it won't attempt to read the PID).
	size := 32
	buf := make([]byte, size)
	copy(buf[0:4], "miph")
	putUint32LE(buf, 4, uint32(size)) // headerLen
	putUint32LE(buf, 8, uint32(size)) // totalLen
	putUint32LE(buf, 16, uint32(itemCount))
	return buf
}

// buildMtphLEMin builds a minimal mtph (LE playlist track reference) chunk.
// The LE parser expects "mtph" (not "hptm").
func buildMtphLEMin(trackID int) []byte {
	buf := make([]byte, 28)
	copy(buf[0:4], "mtph")
	putUint32LE(buf, 4, 28) // headerLen
	putUint32LE(buf, 8, 28) // totalLen
	putUint32LE(buf, 24, uint32(trackID))
	return buf
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestParseITLAsLibrary_FieldMapping(t *testing.T) {
	itlData := buildConvertTestITL(t)

	tmp := filepath.Join(t.TempDir(), "test.itl")
	require.NoError(t, os.WriteFile(tmp, itlData, 0644))

	lib, err := ParseITLAsLibrary(tmp)
	require.NoError(t, err)
	require.NotNil(t, lib)

	// Should have exactly one track
	require.Len(t, lib.Tracks, 1)

	// Look up the track by its string key "1001"
	track, ok := lib.Tracks["1001"]
	require.True(t, ok, "track with key '1001' not found")

	assert.Equal(t, 1001, track.TrackID)
	assert.Equal(t, "Test Audiobook", track.Name)
	assert.Equal(t, "Test Author", track.Artist)
	assert.Equal(t, "Test Series", track.Album)
	assert.Equal(t, "Audiobooks", track.Genre)
	assert.Equal(t, "MPEG audio file", track.Kind)
	assert.Equal(t, "file://localhost/mnt/bigdata/books/test.mp3", track.Location)
	assert.Equal(t, int64(4096000), track.Size)
	assert.Equal(t, int64(28800000), track.TotalTime)
	assert.Equal(t, 1, track.TrackNumber)
	assert.Equal(t, 10, track.TrackCount)
	assert.Equal(t, 1, track.DiscNumber)
	assert.Equal(t, 3, track.DiscCount)
	assert.Equal(t, 2023, track.Year)
	assert.Equal(t, 7, track.PlayCount)
	assert.Equal(t, 60, track.Rating)
	assert.False(t, track.Bookmarkable, "ITL carries no Bookmarkable flag; it must not be assumed true")

	// DateAdded should be non-zero (Mac epoch + 1 second)
	assert.False(t, track.DateAdded.IsZero(), "DateAdded should not be zero")
}

func TestParseITLAsLibrary_PersistentID_Format(t *testing.T) {
	// The PID stored in the binary (LE) is:
	//   pidLE := [8]byte{0x12, 0x01, 0xCD, 0x23, 0x45, 0x67, 0x89, 0xAB}
	// The LE parser reverses this to:
	//   ITLTrack.PersistentID = {0xAB, 0x89, 0x67, 0x45, 0x23, 0xCD, 0x01, 0x12}
	// pidToHex of that → "ab89674523cd0112"
	// strings.ToUpper → "AB89674523CD0112"
	expectedPID := "AB89674523CD0112"

	itlData := buildConvertTestITL(t)
	tmp := filepath.Join(t.TempDir(), "test.itl")
	require.NoError(t, os.WriteFile(tmp, itlData, 0644))

	lib, err := ParseITLAsLibrary(tmp)
	require.NoError(t, err)

	track, ok := lib.Tracks["1001"]
	require.True(t, ok)

	assert.Equal(t, expectedPID, track.PersistentID,
		"PersistentID must be uppercase hex matching XML format")

	// Must be exactly 16 hex characters, uppercase
	assert.Len(t, track.PersistentID, 16)
	assert.Equal(t, strings.ToUpper(track.PersistentID), track.PersistentID,
		"PersistentID must be uppercase")

	// Must decode as valid hex
	decoded, err := hex.DecodeString(track.PersistentID)
	require.NoError(t, err)
	assert.Len(t, decoded, 8)
}

func TestParseITLAsLibrary_Playlists(t *testing.T) {
	itlData := buildConvertTestITL(t)
	tmp := filepath.Join(t.TempDir(), "test.itl")
	require.NoError(t, os.WriteFile(tmp, itlData, 0644))

	lib, err := ParseITLAsLibrary(tmp)
	require.NoError(t, err)

	require.NotEmpty(t, lib.Playlists, "expected at least one playlist")

	// Find the playlist named "My Playlist"
	var found *Playlist
	for _, pl := range lib.Playlists {
		if pl.Name == "My Playlist" {
			found = pl
			break
		}
	}
	require.NotNil(t, found, "playlist 'My Playlist' not found")

	// Should reference track 1001
	require.Contains(t, found.TrackIDs, 1001,
		"playlist should reference track 1001")
}

func TestParseITLAsLibrary_EmptyTrack(t *testing.T) {
	// An ITLLibrary with no tracks should return a Library with empty Tracks map.
	itlLib := &ITLLibrary{
		Tracks:    []ITLTrack{},
		Playlists: []ITLPlaylist{},
	}
	_ = itlLib // Validate via ParseITLAsLibrary by writing a minimal ITL.

	// Minimal check: verify the function handles an empty slice gracefully
	// by building a tiny synthetic ITL with no tracks.
	version := "12.0.0"
	verBytes := []byte(version)
	headerLen := 17 + len(verBytes)
	hdr := make([]byte, headerLen)
	copy(hdr[0:4], "hdfm")
	binary.BigEndian.PutUint32(hdr[4:8], uint32(headerLen))
	binary.BigEndian.PutUint32(hdr[8:12], uint32(headerLen))
	hdr[16] = byte(len(verBytes))
	copy(hdr[17:], verBytes)

	tmp := filepath.Join(t.TempDir(), "empty.itl")
	require.NoError(t, os.WriteFile(tmp, hdr, 0644))

	lib, err := ParseITLAsLibrary(tmp)
	require.NoError(t, err)
	assert.Empty(t, lib.Tracks)
	assert.Empty(t, lib.Playlists)
}

func TestParseITLAsLibrary_LocationFallback(t *testing.T) {
	// When Location (hohm 0x0D) is absent, LocalURL (hohm 0x0B) should be used.
	// Build a synthetic ITL with only a 0x0B hohm (no 0x0D).
	pidLE := [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	trackContent := buildMithLeForConvert(2002, pidLE, 1024, 60000)
	trackContent = append(trackContent, mustBuildMhohLE(0x02, "Fallback Track")...)
	// Only 0x0B (LocalURL), no 0x0D (Location)
	trackContent = append(trackContent, mustBuildMhohLE(0x0B, "file://localhost/mnt/fallback.mp3")...)

	trackMsdh := buildMsdhLE(0x01, trackContent)

	version := "12.13.10.3"
	verBytes := []byte(version)
	headerLen := 17 + len(verBytes)
	hdr := make([]byte, headerLen)
	copy(hdr[0:4], "hdfm")
	binary.BigEndian.PutUint32(hdr[4:8], uint32(headerLen))
	binary.BigEndian.PutUint32(hdr[8:12], uint32(headerLen+len(trackMsdh)))
	hdr[16] = byte(len(verBytes))
	copy(hdr[17:], verBytes)

	fakeHdr := &hdfmHeader{headerLen: uint32(headerLen), version: version}
	encrypted := itlEncrypt(fakeHdr, trackMsdh)

	var out bytes.Buffer
	out.Write(hdr)
	out.Write(encrypted)

	tmp := filepath.Join(t.TempDir(), "fallback.itl")
	require.NoError(t, os.WriteFile(tmp, out.Bytes(), 0644))

	lib, err := ParseITLAsLibrary(tmp)
	require.NoError(t, err)

	track, ok := lib.Tracks["2002"]
	require.True(t, ok)
	assert.Equal(t, "file://localhost/mnt/fallback.mp3", track.Location,
		"Location should fall back to LocalURL when Location is empty")
}

func TestParseITLAsLibrary_PlayDateConversion(t *testing.T) {
	// Build a track with a LastPlayDate (set to a known Mac epoch time).
	// Mac epoch + 3786825600 seconds ≈ 2024-01-01 00:00:00 UTC
	// Unix epoch  + 2082844800 seconds offset from Mac epoch
	// So Mac seconds 3786825600 → Unix seconds 1704067200 (2024-01-01).
	const macSecs = 3786825600

	// Re-derive: macEpoch is 1904-01-01. Unix epoch is 1970-01-01.
	// Difference in seconds: (1970-1904) * 365.25 * 86400 ≈ 2082844800.
	expected := macEpoch.Add(time.Duration(macSecs) * time.Second).Unix()

	pidLE := [8]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}
	buf := buildMithLeForConvert(3003, pidLE, 512, 30000)
	// Overwrite PlayCount offset to set LastPlayDate at offset 100.
	putUint32LE(buf, 100, macSecs)

	trackContent := append(buf, mustBuildMhohLE(0x02, "PlayDate Track")...)
	trackMsdh := buildMsdhLE(0x01, trackContent)

	version := "12.13.10.3"
	verBytes := []byte(version)
	headerLen := 17 + len(verBytes)
	hdr := make([]byte, headerLen)
	copy(hdr[0:4], "hdfm")
	binary.BigEndian.PutUint32(hdr[4:8], uint32(headerLen))
	binary.BigEndian.PutUint32(hdr[8:12], uint32(headerLen+len(trackMsdh)))
	hdr[16] = byte(len(verBytes))
	copy(hdr[17:], verBytes)

	fakeHdr := &hdfmHeader{headerLen: uint32(headerLen), version: version}
	encrypted := itlEncrypt(fakeHdr, trackMsdh)

	var out bytes.Buffer
	out.Write(hdr)
	out.Write(encrypted)

	tmp := filepath.Join(t.TempDir(), "playdate.itl")
	require.NoError(t, os.WriteFile(tmp, out.Bytes(), 0644))

	lib, err := ParseITLAsLibrary(tmp)
	require.NoError(t, err)

	track, ok := lib.Tracks["3003"]
	require.True(t, ok)
	assert.Equal(t, expected, track.PlayDate,
		"PlayDate should be the Unix timestamp of LastPlayDate")
}

func TestParseLibrary_AutoDetect_ITL(t *testing.T) {
	// ParseLibrary should auto-detect an ITL file and call ParseITLAsLibrary.
	itlData := buildConvertTestITL(t)
	tmp := filepath.Join(t.TempDir(), "auto.itl")
	require.NoError(t, os.WriteFile(tmp, itlData, 0644))

	lib, err := ParseLibrary(tmp)
	require.NoError(t, err)
	require.NotNil(t, lib)
	assert.NotEmpty(t, lib.Tracks, "auto-detected ITL should have tracks")
}

func TestParseLibrary_AutoDetect_XML(t *testing.T) {
	// ParseLibrary should still parse XML plist files normally.
	xmlData := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Major Version</key><integer>1</integer>
  <key>Minor Version</key><integer>1</integer>
  <key>Application Version</key><string>12.0</string>
  <key>Music Folder</key><string>file://localhost/Music/</string>
  <key>Tracks</key><dict></dict>
  <key>Playlists</key><array></array>
</dict>
</plist>`
	tmp := filepath.Join(t.TempDir(), "Library.xml")
	require.NoError(t, os.WriteFile(tmp, []byte(xmlData), 0644))

	lib, err := ParseLibrary(tmp)
	require.NoError(t, err)
	require.NotNil(t, lib)
	assert.Empty(t, lib.Tracks)
}
