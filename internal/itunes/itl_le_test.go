// file: internal/itunes/itl_le_test.go
// version: 1.4.0
// guid: c5f9e038-7d4a-4b92-af13-g8c4d9e5f67b
// last-edited: 2026-10-07

package itunes

import (
	"encoding/hex"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers — build synthetic LE binary data
// ---------------------------------------------------------------------------

// putUint32LE writes a uint32 in little-endian to buf at offset.
func putUint32LE(buf []byte, offset int, val uint32) {
	buf[offset] = byte(val)
	buf[offset+1] = byte(val >> 8)
	buf[offset+2] = byte(val >> 16)
	buf[offset+3] = byte(val >> 24)
}

// putUint16LE writes a uint16 in little-endian to buf at offset.
func putUint16LE(buf []byte, offset int, val uint16) {
	buf[offset] = byte(val)
	buf[offset+1] = byte(val >> 8)
}

// buildMhohLE builds a little-endian mhoh chunk for a given hohmType and ASCII string.
func testBuildMhohLE(hohmType uint32, value string) []byte {
	encoded := []byte(value)
	chunkLen := 40 + len(encoded)
	buf := make([]byte, chunkLen)
	copy(buf[0:4], "mhoh")
	putUint32LE(buf, 4, uint32(chunkLen))
	putUint32LE(buf, 8, uint32(chunkLen))
	putUint32LE(buf, 12, hohmType)
	buf[16+11] = 0 // ASCII encoding
	putUint32LE(buf, 28, uint32(len(encoded)))
	copy(buf[40:], encoded)
	return buf
}

// buildMithLE builds a little-endian mith chunk with the given fields.
func testBuildMithLE(trackID int, pid [8]byte, size, totalTime int) []byte {
	mithLen := 156
	buf := make([]byte, mithLen)
	copy(buf[0:4], "mith")
	putUint32LE(buf, 4, uint32(mithLen))
	putUint32LE(buf, 16, uint32(trackID))
	putUint32LE(buf, 36, uint32(size))
	putUint32LE(buf, 40, uint32(totalTime))
	putUint16LE(buf, 44, 3)     // TrackNumber
	putUint16LE(buf, 48, 12)    // TrackCount
	putUint16LE(buf, 54, 2024)  // Year
	putUint16LE(buf, 58, 320)   // BitRate
	putUint16LE(buf, 60, 44100) // SampleRate
	putUint32LE(buf, 76, 5)     // PlayCount
	putUint16LE(buf, 104, 1)    // DiscNumber
	putUint16LE(buf, 106, 2)    // DiscCount
	buf[108] = 80               // Rating
	copy(buf[128:136], pid[:])
	return buf
}

// buildMsdhLE builds an msdh container wrapping the given content.
func buildMsdhLE(blockType uint32, content []byte) []byte {
	headerLen := 16
	totalLen := headerLen + len(content)
	buf := make([]byte, totalLen)
	copy(buf[0:4], "msdh")
	putUint32LE(buf, 4, uint32(headerLen))
	putUint32LE(buf, 8, uint32(totalLen))
	putUint32LE(buf, 12, blockType)
	copy(buf[headerLen:], content)
	return buf
}

// buildMiphLE builds a little-endian miph chunk with a persistent ID.
func buildMiphLE(pid [8]byte) []byte {
	// miph: same layout as hpim — PID at remaining[420:428] = offset 20+420=440
	miphLen := 20 + 428
	buf := make([]byte, miphLen)
	copy(buf[0:4], "miph")
	putUint32LE(buf, 4, uint32(miphLen))
	putUint32LE(buf, 8, uint32(miphLen))
	putUint32LE(buf, 16, 1) // item count
	copy(buf[440:448], pid[:])
	return buf
}

// buildMtphLE builds a little-endian mtph chunk referencing a track ID.
func buildMtphLE(trackID int) []byte {
	mtphLen := 28
	buf := make([]byte, mtphLen)
	copy(buf[0:4], "mtph")
	putUint32LE(buf, 4, uint32(mtphLen))
	putUint32LE(buf, 24, uint32(trackID))
	return buf
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestWalkChunksLE_ParsesTracks(t *testing.T) {
	// LE byte order: reversed from XML hex "aabbccddeeff1122"
	pid := [8]byte{0x22, 0x11, 0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}
	location := "/music/audiobooks/test.m4b"

	// Build track content: mith + mhoh(name) + mhoh(location)
	mith := testBuildMithLE(42, pid, 1024000, 360000)
	nameMhoh := testBuildMhohLE(0x02, "Test Audiobook")
	albumMhoh := testBuildMhohLE(0x03, "Test Album")
	artistMhoh := testBuildMhohLE(0x04, "Test Author")
	genreMhoh := testBuildMhohLE(0x05, "Audiobooks")
	locMhoh := testBuildMhohLE(0x0D, location)

	var content []byte
	content = append(content, mith...)
	content = append(content, nameMhoh...)
	content = append(content, albumMhoh...)
	content = append(content, artistMhoh...)
	content = append(content, genreMhoh...)
	content = append(content, locMhoh...)

	data := buildMsdhLE(0x01, content)

	lib := &ITLLibrary{}
	walkChunksLEImpl(data, lib)

	if len(lib.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(lib.Tracks))
	}

	tr := lib.Tracks[0]
	if tr.TrackID != 42 {
		t.Errorf("TrackID: expected 42, got %d", tr.TrackID)
	}
	if tr.Name != "Test Audiobook" {
		t.Errorf("Name: expected 'Test Audiobook', got %q", tr.Name)
	}
	if tr.Album != "Test Album" {
		t.Errorf("Album: expected 'Test Album', got %q", tr.Album)
	}
	if tr.Artist != "Test Author" {
		t.Errorf("Artist: expected 'Test Author', got %q", tr.Artist)
	}
	if tr.Genre != "Audiobooks" {
		t.Errorf("Genre: expected 'Audiobooks', got %q", tr.Genre)
	}
	if tr.Location != location {
		t.Errorf("Location: expected %q, got %q", location, tr.Location)
	}
	if tr.Size != 1024000 {
		t.Errorf("Size: expected 1024000, got %d", tr.Size)
	}
	if tr.TotalTime != 360000 {
		t.Errorf("TotalTime: expected 360000, got %d", tr.TotalTime)
	}
	if tr.TrackNumber != 3 {
		t.Errorf("TrackNumber: expected 3, got %d", tr.TrackNumber)
	}
	if tr.TrackCount != 12 {
		t.Errorf("TrackCount: expected 12, got %d", tr.TrackCount)
	}
	if tr.Year != 2024 {
		t.Errorf("Year: expected 2024, got %d", tr.Year)
	}
	if tr.BitRate != 320 {
		t.Errorf("BitRate: expected 320, got %d", tr.BitRate)
	}
	if tr.SampleRate != 44100 {
		t.Errorf("SampleRate: expected 44100, got %d", tr.SampleRate)
	}
	if tr.PlayCount != 5 {
		t.Errorf("PlayCount: expected 5, got %d", tr.PlayCount)
	}
	if tr.DiscNumber != 1 {
		t.Errorf("DiscNumber: expected 1, got %d", tr.DiscNumber)
	}
	if tr.DiscCount != 2 {
		t.Errorf("DiscCount: expected 2, got %d", tr.DiscCount)
	}
	if tr.Rating != 80 {
		t.Errorf("Rating: expected 80, got %d", tr.Rating)
	}
	pidHex := hex.EncodeToString(tr.PersistentID[:])
	expectedPID := "aabbccddeeff1122"
	if pidHex != expectedPID {
		t.Errorf("PersistentID: expected %s, got %s", expectedPID, pidHex)
	}
}

func TestWalkChunksLE_ParsesPlaylists(t *testing.T) {
	// LE order: reverses to "1122334455667788"
	playlistPID := [8]byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}

	// Build playlist content: miph + mhoh(title) + mtph + mtph
	miph := buildMiphLE(playlistPID)
	titleMhoh := testBuildMhohLE(0x64, "My Audiobooks")
	mtph1 := buildMtphLE(42)
	mtph2 := buildMtphLE(99)

	var content []byte
	content = append(content, miph...)
	content = append(content, titleMhoh...)
	content = append(content, mtph1...)
	content = append(content, mtph2...)

	data := buildMsdhLE(0x02, content)

	lib := &ITLLibrary{}
	walkChunksLEImpl(data, lib)

	if len(lib.Playlists) != 1 {
		t.Fatalf("expected 1 playlist, got %d", len(lib.Playlists))
	}

	pl := lib.Playlists[0]
	if pl.Title != "My Audiobooks" {
		t.Errorf("Playlist title: expected 'My Audiobooks', got %q", pl.Title)
	}
	if len(pl.Items) != 2 {
		t.Fatalf("expected 2 playlist items, got %d", len(pl.Items))
	}
	if pl.Items[0] != 42 {
		t.Errorf("Playlist item 0: expected 42, got %d", pl.Items[0])
	}
	if pl.Items[1] != 99 {
		t.Errorf("Playlist item 1: expected 99, got %d", pl.Items[1])
	}
	pidHex := hex.EncodeToString(pl.PersistentID[:])
	if pidHex != "1122334455667788" {
		t.Errorf("Playlist PID: expected 1122334455667788, got %s", pidHex)
	}
}

func TestWalkChunksLE_MultipleMsdhContainers(t *testing.T) {
	trackPID := [8]byte{0x22, 0x11, 0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}
	playlistPID := [8]byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}

	// Build track msdh
	mith := testBuildMithLE(42, trackPID, 1024000, 360000)
	locMhoh := testBuildMhohLE(0x0D, "/path/to/book.m4b")
	var trackContent []byte
	trackContent = append(trackContent, mith...)
	trackContent = append(trackContent, locMhoh...)
	trackMsdh := buildMsdhLE(0x01, trackContent)

	// Build playlist msdh
	miph := buildMiphLE(playlistPID)
	titleMhoh := testBuildMhohLE(0x64, "Favorites")
	mtph := buildMtphLE(42)
	var playlistContent []byte
	playlistContent = append(playlistContent, miph...)
	playlistContent = append(playlistContent, titleMhoh...)
	playlistContent = append(playlistContent, mtph...)
	playlistMsdh := buildMsdhLE(0x02, playlistContent)

	// Concatenate both containers
	var data []byte
	data = append(data, trackMsdh...)
	data = append(data, playlistMsdh...)

	lib := &ITLLibrary{}
	walkChunksLEImpl(data, lib)

	if len(lib.Tracks) != 1 {
		t.Errorf("expected 1 track, got %d", len(lib.Tracks))
	}
	if len(lib.Playlists) != 1 {
		t.Errorf("expected 1 playlist, got %d", len(lib.Playlists))
	}
	if lib.Tracks[0].Location != "/path/to/book.m4b" {
		t.Errorf("Track location: expected '/path/to/book.m4b', got %q", lib.Tracks[0].Location)
	}
	if lib.Playlists[0].Title != "Favorites" {
		t.Errorf("Playlist title: expected 'Favorites', got %q", lib.Playlists[0].Title)
	}
}

// ---------------------------------------------------------------------------
// New edge-case tests
// ---------------------------------------------------------------------------

// buildMiahLE builds a little-endian miah (track item array) wrapper containing
// the given sub-content. The miah header is 12 bytes.
func buildMiahLE(subContent []byte) []byte {
	headerLen := 12
	totalLen := headerLen + len(subContent)
	buf := make([]byte, totalLen)
	copy(buf[0:4], "miah")
	putUint32LE(buf, 4, uint32(headerLen))
	putUint32LE(buf, 8, uint32(totalLen))
	copy(buf[headerLen:], subContent)
	return buf
}

// TestWalkMiahContent verifies that tracks nested inside a miah wrapper are
// parsed correctly when a track msdh contains miah → mith + mhoh sub-blocks.
func TestWalkMiahContent(t *testing.T) {
	pid := [8]byte{0x22, 0x11, 0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}
	location := "/audiobooks/nested/book.m4b"

	mith := testBuildMithLE(77, pid, 2048000, 720000)
	nameMhoh := testBuildMhohLE(0x02, "Nested Track")
	locMhoh := testBuildMhohLE(0x0D, location)

	// Pack mith + mhoh into a miah wrapper
	var subContent []byte
	subContent = append(subContent, mith...)
	subContent = append(subContent, nameMhoh...)
	subContent = append(subContent, locMhoh...)
	miah := buildMiahLE(subContent)

	// Wrap the miah in a track-list msdh
	data := buildMsdhLE(0x01, miah)

	lib := &ITLLibrary{}
	walkChunksLEImpl(data, lib)

	if len(lib.Tracks) != 1 {
		t.Fatalf("expected 1 track from miah wrapper, got %d", len(lib.Tracks))
	}

	tr := lib.Tracks[0]
	if tr.TrackID != 77 {
		t.Errorf("TrackID: expected 77, got %d", tr.TrackID)
	}
	if tr.Name != "Nested Track" {
		t.Errorf("Name: expected 'Nested Track', got %q", tr.Name)
	}
	if tr.Location != location {
		t.Errorf("Location: expected %q, got %q", location, tr.Location)
	}
	// PID bytes [0x22,0x11,...,0xAA] in LE → reversed → "aabbccddeeff1122"
	pidHex := hex.EncodeToString(tr.PersistentID[:])
	if pidHex != "aabbccddeeff1122" {
		t.Errorf("PersistentID: expected aabbccddeeff1122, got %s", pidHex)
	}
}

// TestPidToHexLE verifies that pidToHexLE reverses the LE byte order so that the
// resulting hex string matches the XML (big-endian / MSB-first) representation.
func TestPidToHexLE(t *testing.T) {
	// LE stored bytes — these are in reversed order compared to the XML string.
	input := [8]byte{0x22, 0x11, 0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}
	want := "aabbccddeeff1122"

	got := pidToHexLE(input)
	if got != want {
		t.Errorf("pidToHexLE: expected %q, got %q", want, got)
	}
}

// TestDetectLE verifies that detectLE returns true for data starting with "msdh",
// false for data starting with "hdsm", and false for data shorter than 4 bytes.
func TestDetectLE(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{
			name: "msdh prefix",
			data: []byte("msdh" + "extra data here"),
			want: true,
		},
		{
			name: "hdsm prefix (BE format)",
			data: []byte("hdsm" + "extra data here"),
			want: false,
		},
		{
			name: "short data (3 bytes)",
			data: []byte("msd"),
			want: false,
		},
		{
			name: "empty data",
			data: []byte{},
			want: false,
		},
		{
			name: "exactly 4 bytes msdh",
			data: []byte("msdh"),
			want: true,
		},
		{
			name: "exactly 4 bytes non-msdh",
			data: []byte("abcd"),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectLE(tc.data)
			if got != tc.want {
				t.Errorf("detectLE(%q): expected %v, got %v", tc.data, tc.want, got)
			}
		})
	}
}

// buildMithLEWithChildren builds an iTunes-style mith container where the mhoh string
// blocks are embedded as children of the mith chunk (totalLen > headerLen). This is
// the actual on-disk layout produced by iTunes 10+ that caused HIGH-2: the outer walker
// advanced by totalLen after parseMithLE, making the mhoh case unreachable.
func buildMithLEWithChildren(trackID int, pid [8]byte, size, totalTime int, mhohChildren [][]byte) []byte {
	// Fixed mith header: same 156-byte layout as testBuildMithLE
	headerLen := 156
	header := make([]byte, headerLen)
	copy(header[0:4], "mith")
	// headerLen at +4 will be set after we know the children total
	putUint32LE(header, 16, uint32(trackID))
	putUint32LE(header, 36, uint32(size))
	putUint32LE(header, 40, uint32(totalTime))
	putUint16LE(header, 44, 3)     // TrackNumber
	putUint16LE(header, 48, 12)    // TrackCount
	putUint16LE(header, 54, 2024)  // Year
	putUint16LE(header, 58, 320)   // BitRate
	putUint16LE(header, 60, 44100) // SampleRate
	putUint32LE(header, 76, 5)     // PlayCount
	putUint16LE(header, 104, 1)    // DiscNumber
	putUint16LE(header, 106, 2)    // DiscCount
	header[108] = 80               // Rating
	copy(header[128:136], pid[:])

	// Concatenate header + children
	var children []byte
	for _, c := range mhohChildren {
		children = append(children, c...)
	}
	totalLen := headerLen + len(children)

	// Write headerLen and totalLen into the header
	putUint32LE(header, 4, uint32(headerLen))
	putUint32LE(header, 8, uint32(totalLen))

	return append(header, children...)
}

// TestParseLE_TrackStrings is the acceptance test for HIGH-2 / TASK-001. It builds a
// fixture where mhoh string blocks are embedded as children of mith (the real iTunes
// on-disk layout) and asserts that all seven string fields round-trip correctly.
// Pre-fix: walkMsdhTracksLE advanced by totalLen after parseMithLE, skipping every mhoh
// child so all string fields were empty. Post-fix: the inner-walk descends into
// [offset+headerLen, offset+totalLen) and calls parseMhohLE on each child.
func TestParseLE_TrackStrings(t *testing.T) {
	pid := [8]byte{0x22, 0x11, 0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}

	wantName := "The Great Audiobook"
	wantAlbum := "Great Series Vol 1"
	wantArtist := "Jane Narrator"
	wantGenre := "Audiobooks"
	wantKind := "AAC audio file"
	wantLocation := `W:\audiobooks\Great Series Vol 1\chapter.m4b`
	wantLocalURL := "file://localhost/W:/audiobooks/Great+Series+Vol+1/chapter.m4b"

	// Build mhoh children in the iTunes-native layout (children of mith, not siblings).
	mhohChildren := [][]byte{
		testBuildMhohLE(0x02, wantName),
		testBuildMhohLE(0x03, wantAlbum),
		testBuildMhohLE(0x04, wantArtist),
		testBuildMhohLE(0x05, wantGenre),
		testBuildMhohLE(0x06, wantKind),
		testBuildMhohLE(0x0D, wantLocation),
		testBuildMhohLE(0x0B, wantLocalURL),
	}

	// mith with children embedded (totalLen > headerLen) — the layout that
	// broke walkMsdhTracksLE before this fix.
	mith := buildMithLEWithChildren(42, pid, 1024000, 360000, mhohChildren)

	// Wrap in a track-list msdh. No sibling mhoh blocks — the string data is only
	// reachable via the inner-walk of the mith span.
	data := buildMsdhLE(0x01, mith)

	lib := &ITLLibrary{}
	walkChunksLEImpl(data, lib)

	if len(lib.Tracks) != 1 {
		t.Fatalf("expected 1 track, got %d", len(lib.Tracks))
	}
	tr := lib.Tracks[0]

	if tr.Name != wantName {
		t.Errorf("Name: expected %q, got %q", wantName, tr.Name)
	}
	if tr.Album != wantAlbum {
		t.Errorf("Album: expected %q, got %q", wantAlbum, tr.Album)
	}
	if tr.Artist != wantArtist {
		t.Errorf("Artist: expected %q, got %q", wantArtist, tr.Artist)
	}
	if tr.Genre != wantGenre {
		t.Errorf("Genre: expected %q, got %q", wantGenre, tr.Genre)
	}
	if tr.Kind != wantKind {
		t.Errorf("Kind: expected %q, got %q", wantKind, tr.Kind)
	}
	if tr.Location != wantLocation {
		t.Errorf("Location: expected %q, got %q", wantLocation, tr.Location)
	}
	if tr.LocalURL != wantLocalURL {
		t.Errorf("LocalURL: expected %q, got %q", wantLocalURL, tr.LocalURL)
	}
	if tr.TrackID != 42 {
		t.Errorf("TrackID: expected 42, got %d", tr.TrackID)
	}
	pidHex := hex.EncodeToString(tr.PersistentID[:])
	if pidHex != "aabbccddeeff1122" {
		t.Errorf("PersistentID: expected aabbccddeeff1122, got %s", pidHex)
	}
}

// TestParseLE_TrackStrings_ViaMiah verifies the same mhoh-descent fix applies when
// the mith+children are wrapped in a miah (track item array) container. Both
// walkMsdhTracksLE and walkMiahContent must descend into the mith span.
func TestParseLE_TrackStrings_ViaMiah(t *testing.T) {
	pid := [8]byte{0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55}

	wantName := "Miah-Nested Book"
	wantArtist := "Miah Author"
	wantLocation := `W:\audiobooks\Miah Author\Miah-Nested Book\chapter.m4b`

	mhohChildren := [][]byte{
		testBuildMhohLE(0x02, wantName),
		testBuildMhohLE(0x04, wantArtist),
		testBuildMhohLE(0x0D, wantLocation),
	}
	mith := buildMithLEWithChildren(99, pid, 512000, 180000, mhohChildren)

	// Wrap in miah → msdh hierarchy
	miah := buildMiahLE(mith)
	data := buildMsdhLE(0x01, miah)

	lib := &ITLLibrary{}
	walkChunksLEImpl(data, lib)

	if len(lib.Tracks) != 1 {
		t.Fatalf("expected 1 track via miah, got %d", len(lib.Tracks))
	}
	tr := lib.Tracks[0]

	if tr.Name != wantName {
		t.Errorf("Name: expected %q, got %q", wantName, tr.Name)
	}
	if tr.Artist != wantArtist {
		t.Errorf("Artist: expected %q, got %q", wantArtist, tr.Artist)
	}
	if tr.Location != wantLocation {
		t.Errorf("Location: expected %q, got %q", wantLocation, tr.Location)
	}
}
