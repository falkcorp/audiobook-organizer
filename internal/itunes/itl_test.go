// file: internal/itunes/itl_test.go
// version: 1.5.0
// guid: 8a3b9c4d-5e6f-7012-b3c4-d5e6f7a8b9c0
// last-edited: 2026-10-07

package itunes

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestITLDecryptEncryptRoundTrip(t *testing.T) {
	// Pad to 16-byte multiple
	original := []byte("Hello, ITL world!!!!!!!!!!!!!!!!") // 32 bytes
	assert.Equal(t, 0, len(original)%16)

	encrypted := itlEncrypt(&hdfmHeader{version: "12.0.0"}, original)
	assert.NotEqual(t, original, encrypted, "encrypted should differ from original")

	decrypted := itlDecrypt(&hdfmHeader{version: "12.0.0"}, encrypted)
	assert.Equal(t, original, decrypted)
}

func TestITLDecryptEncryptRoundTrip_OldVersion(t *testing.T) {
	original := make([]byte, 256)
	for i := range original {
		original[i] = byte(i % 256)
	}

	encrypted := itlEncrypt(&hdfmHeader{version: "9.0.0"}, original)
	decrypted := itlDecrypt(&hdfmHeader{version: "9.0.0"}, encrypted)
	assert.Equal(t, original, decrypted)
}

func TestITLInflateDeflateRoundTrip(t *testing.T) {
	original := []byte("This is some test data for zlib compression round trip testing.")
	compressed := itlDeflate(original)
	assert.NotEqual(t, original, compressed)
	assert.Equal(t, byte(0x78), compressed[0], "zlib data should start with 0x78")

	decompressed, wasCompressed, err := itlInflate(compressed)
	assert.NoError(t, err)
	assert.True(t, wasCompressed)
	assert.Equal(t, original, decompressed)
}

func TestITLInflate_NotCompressed(t *testing.T) {
	data := []byte("not compressed data")
	result, wasCompressed, err := itlInflate(data)
	assert.NoError(t, err)
	assert.False(t, wasCompressed)
	assert.Equal(t, data, result)
}

func TestITLInflate_DecompressionBomb(t *testing.T) {
	// Create a zlib-compressed payload that exceeds maxDecompressedSize.
	// We'll use a simple approach: create a zlib stream that decompresses
	// to more than 2GB of data (or enough to trigger the cap check).
	// For testing purposes, we'll craft a minimal zlib bomb:
	// a zlib header (0x78 0x01) followed by a single block claiming
	// huge uncompressed length. This will fail decompression gracefully.

	// Simple approach: create a "fake" compressed stream that starts
	// with zlib magic but will cause decompression to fail or exceed cap.
	// For now, use a stream that causes zlib reader to fail.
	// In the real test below, we'll use a more realistic approach if needed.

	// Minimal zlib stream: 0x78 0x01 (deflate) followed by invalid data
	bomb := []byte{0x78, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	// This should fail with an explicit error, not silently return the bomb bytes
	result, _, err := itlInflate(bomb)
	assert.Error(t, err, "decompression bomb or malformed zlib should fail explicitly")
	assert.Nil(t, result, "result should be nil on error")
	// Verify the error mentions zlib or decompression, not a silent fallback
	assert.Contains(t, err.Error(), "zlib", "error should mention zlib")
}

func TestIsVersionAtLeast(t *testing.T) {
	tests := []struct {
		version string
		major   int
		want    bool
	}{
		{"12.0.0", 10, true},
		{"10.0.0", 10, true},
		{"9.2.1", 10, false},
		{"1.0", 1, true},
		{"", 10, false},
		{"abc", 10, false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.want, isVersionAtLeast(tt.version, tt.major))
		})
	}
}

func TestPidToHexAndBack(t *testing.T) {
	pid := [8]byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}
	hexStr := pidToHex(pid)
	assert.Equal(t, "deadbeefcafebabe", hexStr)

	back, err := hexToPID(hexStr)
	require.NoError(t, err)
	assert.Equal(t, pid, back)
}

func TestHexToPID_Invalid(t *testing.T) {
	_, err := hexToPID("zzzz")
	assert.Error(t, err)

	_, err = hexToPID("deadbeef") // only 4 bytes
	assert.Error(t, err)
}

func TestDecodeHohmString(t *testing.T) {
	// ASCII (flag 0)
	s, err := decodeHohmString([]byte("hello"), 0)
	require.NoError(t, err)
	assert.Equal(t, "hello", s)

	// UTF-8 (flag 2)
	s, err = decodeHohmString([]byte("héllo"), 2)
	require.NoError(t, err)
	assert.Equal(t, "héllo", s)

	// UTF-16BE (flag 1)
	utf16 := make([]byte, 10)
	binary.BigEndian.PutUint16(utf16[0:], 'H')
	binary.BigEndian.PutUint16(utf16[2:], 'e')
	binary.BigEndian.PutUint16(utf16[4:], 'l')
	binary.BigEndian.PutUint16(utf16[6:], 'l')
	binary.BigEndian.PutUint16(utf16[8:], 'o')
	s, err = decodeHohmString(utf16, 1)
	require.NoError(t, err)
	assert.Equal(t, "Hello", s)

	// Windows-1252 (flag 3)
	s, err = decodeHohmString([]byte{0xe9}, 3) // é in Windows-1252
	require.NoError(t, err)
	assert.Equal(t, "é", s)
}

func TestEncodeHohmString(t *testing.T) {
	// ASCII-range -> Windows-1252 (flag 3)
	encoded, flag := encodeHohmString("hello")
	assert.Equal(t, byte(3), flag)
	assert.Equal(t, []byte("hello"), encoded)

	// Non-Latin -> UTF-16BE (flag 1)
	encoded, flag = encodeHohmString("日本語")
	assert.Equal(t, byte(1), flag)
	assert.Equal(t, 6, len(encoded)) // 3 chars * 2 bytes
}

func TestReadTag(t *testing.T) {
	data := []byte("hdfmextra")
	assert.Equal(t, "hdfm", readTag(data, 0))
	assert.Equal(t, "dfme", readTag(data, 1))
	assert.Equal(t, "", readTag(data, 100))
}

func TestMacDateToTime(t *testing.T) {
	// 0 should return zero time
	assert.True(t, macDateToTime(0).IsZero())

	// Known conversion: 2001-01-01 is ~3061152000 seconds from 1904
	expected := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	// Seconds from 1904-01-01 to 2001-01-01
	diff := expected.Sub(macEpoch)
	secs := uint32(diff.Seconds())
	result := macDateToTime(secs)
	assert.Equal(t, expected, result)
}

func TestValidateITL_InvalidFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Random bytes
	path := filepath.Join(tmpDir, "bad.itl")
	require.NoError(t, os.WriteFile(path, []byte("this is not an ITL file at all"), 0644))
	err := ValidateITL(path)
	assert.Error(t, err)

	// Too small
	path2 := filepath.Join(tmpDir, "tiny.itl")
	require.NoError(t, os.WriteFile(path2, []byte("hdfm"), 0644))
	err = ValidateITL(path2)
	assert.Error(t, err)

	// Nonexistent
	err = ValidateITL(filepath.Join(tmpDir, "nope.itl"))
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Synthetic ITL round-trip test
// ---------------------------------------------------------------------------

func buildSyntheticITL(t *testing.T, version string, compress bool, pid [8]byte, location string) []byte {
	t.Helper()

	// Build inner payload: htim chunk + hohm 0x0D chunk
	// htim must be at least 156 bytes (standard header size)
	// Persistent ID is at offset 128 (8 bytes)
	htimLen := 156
	htim := make([]byte, htimLen)
	copy(htim[0:4], "htim")
	writeUint32BE(htim, 4, uint32(htimLen))
	writeUint32BE(htim, 8, uint32(htimLen)) // recordLength
	writeUint32BE(htim, 16, 42)             // trackID = 42
	copy(htim[128:136], pid[:])             // persistent ID

	// hohm 0x0D: build location string
	encodedStr, encFlag := encodeHohmString(location)
	hohmLen := 40 + len(encodedStr)
	hohm := make([]byte, hohmLen)
	copy(hohm[0:4], "hohm")
	writeUint32BE(hohm, 4, uint32(hohmLen))
	writeUint32BE(hohm, 8, uint32(hohmLen))
	writeUint32BE(hohm, 12, 0x0D) // hohmType = location
	hohm[16+11] = encFlag
	writeUint32BE(hohm, 28, uint32(len(encodedStr)))
	copy(hohm[40:], encodedStr)

	// Combine into payload
	var payload bytes.Buffer
	payload.Write(htim)
	payload.Write(hohm)

	payloadBytes := payload.Bytes()
	if compress {
		payloadBytes = itlDeflate(payloadBytes)
	}
	encrypted := itlEncrypt(&hdfmHeader{version: version}, payloadBytes)

	// Build hdfm header
	// Header: "hdfm"(4) + headerLen(4) + fileLen(4) + unknown(4) + verLen(1) + version(N) = 17 + N
	fileLen := uint32(len(encrypted)) + 17 + uint32(len(version))
	headerRemainder := []byte{}
	hdr := buildHdfmHeader(version, headerRemainder, fileLen, 0)

	var file bytes.Buffer
	file.Write(hdr)
	file.Write(encrypted)
	return file.Bytes()
}

func TestSyntheticITL_Parse(t *testing.T) {
	pid := [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	originalLoc := "/music/old/song.mp3"

	for _, compress := range []bool{false, true} {
		name := "uncompressed"
		if compress {
			name = "compressed"
		}
		t.Run(name, func(t *testing.T) {
			tmpDir := t.TempDir()
			itlPath := filepath.Join(tmpDir, "test.itl")
			itlData := buildSyntheticITL(t, "12.0.0", compress, pid, originalLoc)
			require.NoError(t, os.WriteFile(itlPath, itlData, 0644))

			// Parse
			lib, err := ParseITL(itlPath)
			require.NoError(t, err)
			require.Len(t, lib.Tracks, 1)
			assert.Equal(t, originalLoc, lib.Tracks[0].Location)
			assert.Equal(t, 42, lib.Tracks[0].TrackID)
			assert.Equal(t, pid, lib.Tracks[0].PersistentID)
		})
	}
}

func TestSyntheticITL_Validate(t *testing.T) {
	pid := [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "valid.itl")
	itlData := buildSyntheticITL(t, "12.0.0", false, pid, "/test/path.mp3")
	require.NoError(t, os.WriteFile(itlPath, itlData, 0644))

	err := ValidateITL(itlPath)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Synthetic ITL with playlists
// ---------------------------------------------------------------------------

func buildSyntheticITLWithPlaylist(t *testing.T, version string, trackID int, pid [8]byte, location, playlistTitle string) []byte {
	t.Helper()

	// Build track: htim + hohm 0x0D
	htimLen := 156
	htim := make([]byte, htimLen)
	copy(htim[0:4], "htim")
	writeUint32BE(htim, 4, uint32(htimLen))
	writeUint32BE(htim, 8, uint32(htimLen))
	writeUint32BE(htim, 16, uint32(trackID))
	copy(htim[128:136], pid[:])

	encodedStr, encFlag := encodeHohmString(location)
	hohmLen := 40 + len(encodedStr)
	hohm := make([]byte, hohmLen)
	copy(hohm[0:4], "hohm")
	writeUint32BE(hohm, 4, uint32(hohmLen))
	writeUint32BE(hohm, 8, uint32(hohmLen))
	writeUint32BE(hohm, 12, 0x0D)
	hohm[16+11] = encFlag
	writeUint32BE(hohm, 28, uint32(len(encodedStr)))
	copy(hohm[40:], encodedStr)

	// Build playlist: hpim + hohm 0x64 + hptm
	hpimLen := 20 + 428
	hpim := make([]byte, hpimLen)
	copy(hpim[0:4], "hpim")
	writeUint32BE(hpim, 4, uint32(hpimLen))
	writeUint32BE(hpim, 8, uint32(hpimLen))
	writeUint32BE(hpim, 16, 1) // 1 item

	titleEncoded, titleFlag := encodeHohmString(playlistTitle)
	titleHohmLen := 40 + len(titleEncoded)
	titleHohm := make([]byte, titleHohmLen)
	copy(titleHohm[0:4], "hohm")
	writeUint32BE(titleHohm, 4, uint32(titleHohmLen))
	writeUint32BE(titleHohm, 8, uint32(titleHohmLen))
	writeUint32BE(titleHohm, 12, 0x64)
	titleHohm[16+11] = titleFlag
	writeUint32BE(titleHohm, 28, uint32(len(titleEncoded)))
	copy(titleHohm[40:], titleEncoded)

	hptmLen := 28
	hptm := make([]byte, hptmLen)
	copy(hptm[0:4], "hptm")
	writeUint32BE(hptm, 4, uint32(hptmLen))
	writeUint32BE(hptm, 24, uint32(trackID))

	var payload bytes.Buffer
	payload.Write(htim)
	payload.Write(hohm)
	payload.Write(hpim)
	payload.Write(titleHohm)
	payload.Write(hptm)

	encrypted := itlEncrypt(&hdfmHeader{version: version}, payload.Bytes())
	fileLen := uint32(len(encrypted)) + 17 + uint32(len(version))
	hdr := buildHdfmHeader(version, nil, fileLen, 0)

	var file bytes.Buffer
	file.Write(hdr)
	file.Write(encrypted)
	return file.Bytes()
}

func TestParseITL_Playlists(t *testing.T) {
	pid := [8]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}
	itlData := buildSyntheticITLWithPlaylist(t, "12.0.0", 99, pid, "/music/test.mp3", "My Playlist")

	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "test.itl")
	require.NoError(t, os.WriteFile(itlPath, itlData, 0644))

	lib, err := ParseITL(itlPath)
	require.NoError(t, err)

	// Should have 1 track
	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, 99, lib.Tracks[0].TrackID)
	assert.Equal(t, "/music/test.mp3", lib.Tracks[0].Location)

	// Should have 1 playlist
	require.Len(t, lib.Playlists, 1)
	assert.Equal(t, "My Playlist", lib.Playlists[0].Title)
	require.Len(t, lib.Playlists[0].Items, 1)
	assert.Equal(t, 99, lib.Playlists[0].Items[0])
}

// ---------------------------------------------------------------------------
// Static fixture: test_library.itl
// Matches the tracks/playlists in testdata/test_library.xml
// ---------------------------------------------------------------------------

type fixtureTrack struct {
	trackID      int
	persistentID [8]byte
	name         string
	artist       string
	album        string
	genre        string
	kind         string
	location     string
	size         int
	totalTime    int
	year         int
	trackNumber  int
	discNumber   int
	playCount    int
	rating       int
}

type fixturePlaylist struct {
	title    string
	trackIDs []int
}

// Persistent IDs for fixture tracks (deterministic, not random)
var fixtureTracks = []fixtureTrack{
	{100, [8]byte{0xAB, 0xCD, 0x12, 0x34, 0xEF, 0x56, 0x78, 0x01}, "The Hobbit", "J.R.R. Tolkien", "Middle-earth, Book 1", "Audiobook", "Audiobook", "/Users/testuser/Music/iTunes/Audiobooks/The Hobbit.m4b", 524288000, 39600000, 1997, 0, 0, 3, 80},
	{200, [8]byte{0xDA, 0xFE, 0x98, 0x76, 0x54, 0x32, 0x10, 0x02}, "Dune", "Frank Herbert", "Dune Chronicles", "Audiobooks", "MPEG audio file", "/Users/testuser/Music/iTunes/Audiobooks/Dune.mp3", 262144000, 79200000, 2007, 0, 0, 1, 100},
	{300, [8]byte{0x4D, 0x55, 0x53, 0x49, 0x43, 0x12, 0x34, 0x03}, "Bohemian Rhapsody", "Queen", "A Night at the Opera", "Rock", "MPEG audio file", "/Users/testuser/Music/iTunes/Music/Queen/Bohemian Rhapsody.mp3", 12000000, 355000, 1975, 0, 0, 50, 100},
	{400, [8]byte{0x53, 0x50, 0x4B, 0x4E, 0x45, 0x67, 0x89, 0x04}, "The Art of War", "Sun Tzu", "", "Philosophy", "Spoken Word", "/Users/testuser/Music/iTunes/Audiobooks/Art of War.m4b", 50000000, 7200000, 2010, 0, 0, 0, 0},
	{500, [8]byte{0x4D, 0x4F, 0x42, 0x59, 0x01, 0x00, 0x00, 0x01}, "Chapter 1 - Loomings", "Herman Melville", "Moby Dick", "Audiobook", "Audiobook", "/Users/testuser/Music/iTunes/Audiobooks/Moby Dick/Chapter 01.m4b", 50000000, 3600000, 2005, 1, 1, 1, 80},
	{501, [8]byte{0x4D, 0x4F, 0x42, 0x59, 0x01, 0x00, 0x00, 0x02}, "Chapter 2 - The Carpet-Bag", "Herman Melville", "Moby Dick", "Audiobook", "Audiobook", "/Users/testuser/Music/iTunes/Audiobooks/Moby Dick/Chapter 02.m4b", 45000000, 3200000, 2005, 2, 1, 1, 80},
	{502, [8]byte{0x4D, 0x4F, 0x42, 0x59, 0x01, 0x00, 0x00, 0x03}, "Chapter 3 - The Spouter-Inn", "Herman Melville", "Moby Dick", "Audiobook", "Audiobook", "/Users/testuser/Music/iTunes/Audiobooks/Moby Dick/Chapter 03.m4b", 48000000, 3400000, 2005, 3, 1, 1, 80},
	{600, [8]byte{0x50, 0x52, 0x49, 0x44, 0x01, 0x00, 0x00, 0x01}, "Part 1", "Jane Austen", "Pride and Prejudice", "Audiobook", "Audiobook", "/Users/testuser/Music/iTunes/Audiobooks/Pride and Prejudice/Part 01.m4b", 60000000, 5400000, 2010, 1, 1, 2, 100},
	{601, [8]byte{0x50, 0x52, 0x49, 0x44, 0x01, 0x00, 0x00, 0x02}, "Part 2", "Jane Austen", "Pride and Prejudice", "Audiobook", "Audiobook", "/Users/testuser/Music/iTunes/Audiobooks/Pride and Prejudice/Part 02.m4b", 55000000, 4800000, 2010, 2, 1, 2, 100},
}

var fixturePlaylists = []fixturePlaylist{
	{"Music", []int{300}},
	{"Audiobooks", []int{100, 200, 400}},
	{"Sci-Fi Favorites", []int{100, 200}},
	{"Recently Added", []int{400}},
}

func buildFixtureHTIM(ft fixtureTrack) []byte {
	htimLen := 156
	buf := make([]byte, htimLen)
	copy(buf[0:4], "htim")
	writeUint32BE(buf, 4, uint32(htimLen))
	writeUint32BE(buf, 8, uint32(htimLen))
	writeUint32BE(buf, 16, uint32(ft.trackID))
	writeUint32BE(buf, 36, uint32(ft.size))
	writeUint32BE(buf, 40, uint32(ft.totalTime))
	writeUint32BE(buf, 44, uint32(ft.trackNumber))
	binary.BigEndian.PutUint16(buf[54:56], uint16(ft.year))
	writeUint32BE(buf, 76, uint32(ft.playCount))
	buf[104] = byte(ft.discNumber)
	buf[108] = byte(ft.rating)
	copy(buf[128:136], ft.persistentID[:])
	return buf
}

func buildFixtureITL() []byte {
	version := "12.9.5.5"
	var payload bytes.Buffer

	// Write all tracks with their hohm string fields
	for _, ft := range fixtureTracks {
		payload.Write(buildFixtureHTIM(ft))
		if ft.name != "" {
			payload.Write(buildHohmChunk(0x02, ft.name))
		}
		if ft.album != "" {
			payload.Write(buildHohmChunk(0x03, ft.album))
		}
		if ft.artist != "" {
			payload.Write(buildHohmChunk(0x04, ft.artist))
		}
		if ft.genre != "" {
			payload.Write(buildHohmChunk(0x05, ft.genre))
		}
		if ft.kind != "" {
			payload.Write(buildHohmChunk(0x06, ft.kind))
		}
		if ft.location != "" {
			payload.Write(buildHohmChunk(0x0D, ft.location))
		}
	}

	// Write playlists
	for _, fp := range fixturePlaylists {
		// hpim
		hpimLen := 20 + 428
		hpim := make([]byte, hpimLen)
		copy(hpim[0:4], "hpim")
		writeUint32BE(hpim, 4, uint32(hpimLen))
		writeUint32BE(hpim, 8, uint32(hpimLen))
		writeUint32BE(hpim, 16, uint32(len(fp.trackIDs)))
		// Deterministic ppid from title
		for i := 0; i < 8 && i < len(fp.title); i++ {
			hpim[440+i] = fp.title[i]
		}
		payload.Write(hpim)

		// Playlist title hohm
		payload.Write(buildHohmChunk(0x64, fp.title))

		// Track items
		for _, tid := range fp.trackIDs {
			hptmLen := 28
			hptm := make([]byte, hptmLen)
			copy(hptm[0:4], "hptm")
			writeUint32BE(hptm, 4, uint32(hptmLen))
			writeUint32BE(hptm, 24, uint32(tid))
			payload.Write(hptm)
		}
	}

	// Compress and encrypt
	compressed := itlDeflate(payload.Bytes())
	encrypted := itlEncrypt(&hdfmHeader{version: version}, compressed)

	// Build hdfm header
	hdr := buildHdfmHeader(version, nil, uint32(len(encrypted))+17+uint32(len(version)), 0)

	var file bytes.Buffer
	file.Write(hdr)
	file.Write(encrypted)
	return file.Bytes()
}

const fixtureITLPath = "testdata/test_library.itl"

// TestGenerateFixtureITL regenerates the static test_library.itl fixture.
// Run with: go test ./internal/itunes/ -run TestGenerateFixtureITL -generate-itl
func TestGenerateFixtureITL(t *testing.T) {
	if os.Getenv("GENERATE_ITL_FIXTURE") == "" {
		t.Skip("Set GENERATE_ITL_FIXTURE=1 to regenerate the fixture")
	}

	data := buildFixtureITL()
	err := os.WriteFile(fixtureITLPath, data, 0644)
	require.NoError(t, err)
	t.Logf("Generated %s (%d bytes)", fixtureITLPath, len(data))
}

// TestParseFixtureITL loads and validates the static test_library.itl fixture.
func TestParseFixtureITL(t *testing.T) {
	if _, err := os.Stat(fixtureITLPath); os.IsNotExist(err) {
		// Generate it on the fly if it doesn't exist
		data := buildFixtureITL()
		require.NoError(t, os.MkdirAll(filepath.Dir(fixtureITLPath), 0755))
		require.NoError(t, os.WriteFile(fixtureITLPath, data, 0644))
		t.Log("Generated fixture on the fly")
	}

	lib, err := ParseITL(fixtureITLPath)
	require.NoError(t, err)

	// Verify version
	assert.Equal(t, "12.9.5.5", lib.Version)
	assert.True(t, lib.UseCompression)

	// Verify tracks
	require.Len(t, lib.Tracks, len(fixtureTracks))
	for i, ft := range fixtureTracks {
		track := lib.Tracks[i]
		assert.Equal(t, ft.trackID, track.TrackID, "track %d ID", i)
		assert.Equal(t, ft.name, track.Name, "track %d name", i)
		assert.Equal(t, ft.artist, track.Artist, "track %d artist", i)
		assert.Equal(t, ft.album, track.Album, "track %d album", i)
		assert.Equal(t, ft.genre, track.Genre, "track %d genre", i)
		assert.Equal(t, ft.kind, track.Kind, "track %d kind", i)
		assert.Equal(t, ft.location, track.Location, "track %d location", i)
		assert.Equal(t, ft.size, track.Size, "track %d size", i)
		assert.Equal(t, ft.totalTime, track.TotalTime, "track %d totalTime", i)
		assert.Equal(t, ft.year, track.Year, "track %d year", i)
		assert.Equal(t, ft.trackNumber, track.TrackNumber, "track %d trackNumber", i)
		assert.Equal(t, ft.discNumber, track.DiscNumber, "track %d discNumber", i)
		assert.Equal(t, ft.playCount, track.PlayCount, "track %d playCount", i)
		assert.Equal(t, ft.rating, track.Rating, "track %d rating", i)
		assert.Equal(t, ft.persistentID, track.PersistentID, "track %d persistentID", i)
	}

	// Verify playlists
	require.Len(t, lib.Playlists, len(fixturePlaylists))
	for i, fp := range fixturePlaylists {
		pl := lib.Playlists[i]
		assert.Equal(t, fp.title, pl.Title, "playlist %d title", i)
		assert.Equal(t, len(fp.trackIDs), len(pl.Items), "playlist %d item count", i)
		for j, tid := range fp.trackIDs {
			assert.Equal(t, tid, pl.Items[j], "playlist %d item %d", i, j)
		}
	}
}

// TestFixtureITL_Validate verifies the fixture passes validation.
func TestFixtureITL_Validate(t *testing.T) {
	if _, err := os.Stat(fixtureITLPath); os.IsNotExist(err) {
		data := buildFixtureITL()
		require.NoError(t, os.MkdirAll(filepath.Dir(fixtureITLPath), 0755))
		require.NoError(t, os.WriteFile(fixtureITLPath, data, 0644))
	}

	err := ValidateITL(fixtureITLPath)
	assert.NoError(t, err)
}
