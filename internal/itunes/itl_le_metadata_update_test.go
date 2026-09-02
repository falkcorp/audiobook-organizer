// file: internal/itunes/itl_le_metadata_update_test.go
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
//
// Regression tests for UpdateMetadataLE and buildMhohLE.
// Root cause for BUG-ITUNES-WRITEBACK-CORRUPTS-LIBRARY: buildMhohLE was
// setting headerLen = totalLen (full chunk size), but iTunes uses headerLen
// to locate type-specific data within the mhoh. Wrong headerLen causes iTunes
// to read string data at the wrong offset and declare the library corrupt.
// last-edited: 2026-09-02

package itunes

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildITUNESmhohLE builds an mhoh chunk with the correct iTunes format:
// headerLen = 24 (fixed portion), totalLen = 40 + len(string).
func buildITunesMhohLE(hohmType uint32, value string) []byte {
	encoded := []byte(value)
	totalLen := 40 + len(encoded)
	buf := make([]byte, totalLen)
	copy(buf[0:4], "mhoh")
	// Correct iTunes headerLen = 24 (NOT totalLen)
	binary.LittleEndian.PutUint32(buf[4:8], 24)
	binary.LittleEndian.PutUint32(buf[8:12], uint32(totalLen))
	binary.LittleEndian.PutUint32(buf[12:16], hohmType)
	buf[27] = 0 // ASCII encoding flag
	binary.LittleEndian.PutUint32(buf[28:32], uint32(len(encoded)))
	copy(buf[40:], encoded)
	return buf
}

// buildLETrackSection builds a minimal LE track section (mlth + one mith + mhoh chunks).
// Returns the raw bytes for the content inside an msdh blockType=1.
func buildLETrackSection(trackID uint32, pid [8]byte, mhohs ...[]byte) []byte {
	// mlth: tag(4) + headerLen(4) + trackCount(4) + padding(4)
	mlthLen := 16
	mlth := make([]byte, mlthLen)
	copy(mlth[0:4], "mlth")
	binary.LittleEndian.PutUint32(mlth[4:8], uint32(mlthLen))
	binary.LittleEndian.PutUint32(mlth[8:12], 1) // 1 track

	// mhoh data combined
	var mhohData []byte
	for _, m := range mhohs {
		mhohData = append(mhohData, m...)
	}

	// mith: 156-byte header with track ID + PID
	mithHeaderLen := 156
	mithTotalLen := mithHeaderLen + len(mhohData)
	mith := make([]byte, mithHeaderLen)
	copy(mith[0:4], "mith")
	binary.LittleEndian.PutUint32(mith[4:8], uint32(mithHeaderLen))
	binary.LittleEndian.PutUint32(mith[8:12], uint32(mithTotalLen))
	binary.LittleEndian.PutUint32(mith[16:20], trackID)
	// PID stored reversed at bytes 128-135
	for i := range 8 {
		mith[135-i] = pid[i]
	}

	var content []byte
	content = append(content, mlth...)
	content = append(content, mith...)
	content = append(content, mhohData...)
	return content
}

// buildLEPayload wraps track section in an msdh(blockType=1) to form a valid LE payload.
func buildLEPayload(trackContent []byte) []byte {
	msdhHeaderLen := 16
	msdhTotalLen := msdhHeaderLen + len(trackContent)
	msdh := make([]byte, msdhTotalLen)
	copy(msdh[0:4], "msdh")
	binary.LittleEndian.PutUint32(msdh[4:8], uint32(msdhHeaderLen))
	binary.LittleEndian.PutUint32(msdh[8:12], uint32(msdhTotalLen))
	binary.LittleEndian.PutUint32(msdh[12:16], 1) // blockType = 1 (tracks)
	copy(msdh[msdhHeaderLen:], trackContent)
	return msdh
}

// readMhohHeaderLen reads the headerLen field of an mhoh chunk at the given offset.
func readMhohHeaderLen(data []byte, offset int) uint32 {
	return binary.LittleEndian.Uint32(data[offset+4 : offset+8])
}

// readMhohTotalLen reads the totalLen field of an mhoh chunk at the given offset.
func readMhohTotalLen(data []byte, offset int) uint32 {
	return binary.LittleEndian.Uint32(data[offset+8 : offset+12])
}

// readMhohString reads the string value from an mhoh chunk at the given offset.
func readMhohString(data []byte, offset int) string {
	strLen := int(binary.LittleEndian.Uint32(data[offset+28 : offset+32]))
	if offset+40+strLen > len(data) {
		return ""
	}
	return string(data[offset+40 : offset+40+strLen])
}

// TestBuildMhohLE_FixedHeaderLen verifies that buildMhohLE uses headerLen=24,
// not headerLen=totalLen. Setting headerLen=totalLen corrupts iTunes library.
func TestBuildMhohLE_FixedHeaderLen(t *testing.T) {
	chunk, ok := buildMhohLE(0x02, "My Book Title")
	require.True(t, ok, "0x02 is in the corpus table")

	headerLen := binary.LittleEndian.Uint32(chunk[4:8])
	totalLen := binary.LittleEndian.Uint32(chunk[8:12])

	assert.Equal(t, uint32(24), headerLen,
		"mhoh.headerLen must be 24 (fixed iTunes format); setting it to totalLen corrupts the library")
	assert.Greater(t, totalLen, headerLen,
		"mhoh.totalLen must be larger than headerLen (includes string data)")
	assert.Equal(t, uint32(40+len("My Book Title")), totalLen,
		"mhoh.totalLen must be 40 + len(string)")
}

// TestUpdateMetadataLE_PreservesHeaderLen is the primary regression test.
// It verifies that when UpdateMetadataLE replaces an existing mhoh chunk,
// the original headerLen (24) is preserved — not overwritten with totalLen.
func TestUpdateMetadataLE_PreservesHeaderLen(t *testing.T) {
	pid := [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}
	pidHex := "0102030405060708" // extractMithPIDLE returns hex(pid[0]..pid[7])

	// Build iTunes-format mhoh chunks with correct headerLen=24
	nameMhoh := buildITunesMhohLE(0x02, "Old Title")
	artistMhoh := buildITunesMhohLE(0x04, "Old Artist")

	trackContent := buildLETrackSection(1, pid, nameMhoh, artistMhoh)
	payload := buildLEPayload(trackContent)

	// Apply metadata update
	updated, count := UpdateMetadataLE(payload, []ITLMetadataUpdate{
		{
			PersistentID: pidHex,
			Name:         "New Title",
			Artist:       "New Artist",
		},
	})
	require.Equal(t, 1, count, "one track should be updated")

	// Find the updated mhoh chunks inside the updated payload.
	// The msdh header is 16 bytes, mlth is 16 bytes, mith is 156 bytes.
	// mhoh chunks start at offset 16 + 16 + 156 = 188.
	mhohStart := 16 + 16 + 156

	// First mhoh (name, type 0x02)
	firstMhohHeaderLen := readMhohHeaderLen(updated, mhohStart)
	firstMhohTotalLen := readMhohTotalLen(updated, mhohStart)
	firstMhohStr := readMhohString(updated, mhohStart)

	assert.Equal(t, uint32(24), firstMhohHeaderLen,
		"updated mhoh.headerLen must remain 24 — not overwritten with totalLen (corruption bug)")
	assert.Greater(t, firstMhohTotalLen, firstMhohHeaderLen,
		"updated mhoh.totalLen must exceed headerLen")
	assert.Equal(t, "New Title", firstMhohStr,
		"updated mhoh must contain the new string value")

	// Second mhoh (artist, type 0x04)
	secondMhohStart := mhohStart + int(firstMhohTotalLen)
	secondMhohHeaderLen := readMhohHeaderLen(updated, secondMhohStart)
	secondMhohStr := readMhohString(updated, secondMhohStart)

	assert.Equal(t, uint32(24), secondMhohHeaderLen,
		"second updated mhoh.headerLen must also remain 24")
	assert.Equal(t, "New Artist", secondMhohStr)
}

// TestUpdateMetadataLE_AllFields verifies that all supported metadata fields
// are updated correctly. Uses direct byte inspection since the LE parser
// reads mhoh as top-level siblings, but UpdateMetadataLE produces the
// children format (mhoh inside mith's span — matching real iTunes ITL files).
func TestUpdateMetadataLE_AllFields(t *testing.T) {
	pid := [8]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}
	pidHex := "aabbccddeeff1122"

	nameMhoh := buildITunesMhohLE(0x02, "Original Book")
	albumMhoh := buildITunesMhohLE(0x03, "Original Album")
	artistMhoh := buildITunesMhohLE(0x04, "Original Author")
	genreMhoh := buildITunesMhohLE(0x05, "Fiction")

	trackContent := buildLETrackSection(42, pid, nameMhoh, albumMhoh, artistMhoh, genreMhoh)
	payload := buildLEPayload(trackContent)

	updated, count := UpdateMetadataLE(payload, []ITLMetadataUpdate{
		{
			PersistentID: pidHex,
			Name:         "Updated Book",
			Album:        "Updated Book",
			Artist:       "Updated Author",
			Genre:        "Audiobook",
		},
	})
	require.Equal(t, 1, count)

	// Direct byte inspection: msdh(16) + mlth(16) + mith(156) = 188 bytes before first mhoh.
	base := 16 + 16 + 156

	type mhohExpect struct {
		hohmType uint32
		value    string
	}
	expected := []mhohExpect{
		{0x02, "Updated Book"},   // name
		{0x03, "Updated Book"},   // album
		{0x04, "Updated Author"}, // artist
		{0x05, "Audiobook"},      // genre
	}

	off := base
	for i, exp := range expected {
		require.Less(t, off+12, len(updated), "not enough data for mhoh %d", i)
		tag := string(updated[off : off+4])
		require.Equal(t, "mhoh", tag, "chunk %d should be mhoh", i)

		gotType := binary.LittleEndian.Uint32(updated[off+12 : off+16])
		assert.Equal(t, exp.hohmType, gotType, "mhoh[%d] type", i)

		gotStr := readMhohString(updated, off)
		assert.Equal(t, exp.value, gotStr, "mhoh[%d] string", i)

		// Advance to next mhoh
		totalLen := int(binary.LittleEndian.Uint32(updated[off+8 : off+12]))
		off += totalLen
	}
}

// TestUpdateMetadataLE_MsdhTotalLenUpdated verifies the msdh totalLen is
// correctly updated when track metadata sizes change.
func TestUpdateMetadataLE_MsdhTotalLenUpdated(t *testing.T) {
	pid := [8]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	pidHex := "0100000000000000"

	// Short original title
	nameMhoh := buildITunesMhohLE(0x02, "A")
	trackContent := buildLETrackSection(1, pid, nameMhoh)
	payload := buildLEPayload(trackContent)

	origMsdhTotal := int(binary.LittleEndian.Uint32(payload[8:12]))

	// Update with a much longer title
	updated, count := UpdateMetadataLE(payload, []ITLMetadataUpdate{
		{PersistentID: pidHex, Name: "A Very Long Title That Is Much Longer Than Before"},
	})
	require.Equal(t, 1, count)

	newMsdhTotal := int(binary.LittleEndian.Uint32(updated[8:12]))
	assert.Greater(t, newMsdhTotal, origMsdhTotal,
		"msdh totalLen must increase when metadata grows")
	assert.Equal(t, len(updated), newMsdhTotal,
		"msdh totalLen must equal actual payload size (no other blocks)")
}

// TestUpdateMetadataLE_UnknownPID verifies that tracks with non-matching PIDs
// are copied unchanged.
func TestUpdateMetadataLE_UnknownPID(t *testing.T) {
	pid := [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	nameMhoh := buildITunesMhohLE(0x02, "Unchanged Title")
	trackContent := buildLETrackSection(1, pid, nameMhoh)
	payload := buildLEPayload(trackContent)

	// Update a different PID
	updated, count := UpdateMetadataLE(payload, []ITLMetadataUpdate{
		{PersistentID: "ffffffffffffffff", Name: "Should Not Apply"},
	})
	assert.Equal(t, 0, count, "no tracks should match an unknown PID")
	assert.Equal(t, payload, updated, "payload should be unchanged for unknown PID")
}

// TestUpdateMetadataLE_OutputPassesMhohFormatGuard (TASK-005) is the central
// CRIT-1 regression: after UpdateMetadataLE replaces AND appends mhoh blocks
// (incl. a non-ASCII title forcing UTF-16LE), EVERY block in the output must
// pass the T003 mhoh-format guard — byte +27==0, headerLen==24, +24 ∈ corpus.
func TestUpdateMetadataLE_OutputPassesMhohFormatGuard(t *testing.T) {
	pid := [8]byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	pidHex := "1020304050607080"

	// Start with one existing iTunes-format ASCII name block (replace path),
	// then update with a CJK name (UTF-16LE) plus new artist/album/genre/kind
	// (append path) — exercising both writer paths.
	nameMhoh := buildITunesMhohLE(0x02, "Original Title")
	trackContent := buildLETrackSection(1, pid, nameMhoh)
	payload := buildLEPayload(trackContent)

	updated, count := UpdateMetadataLE(payload, []ITLMetadataUpdate{
		{
			PersistentID: pidHex,
			Name:         "日本語のタイトル",    // UTF-16LE, replace path
			Artist:       "Café Artist", // latin1, append path
			Album:        "Album",
			Genre:        "Fiction",
			Kind:         "MPEG audio file", // 0x06 → UTF-16LE always
			Location:     `W:\itunes\book.mp3`,
		},
	})
	require.Equal(t, 1, count)

	// Every mhoh in the rewritten payload must satisfy the format contract.
	res := guardMhohFormat(nil, updated, nil, DefaultContractConfig())
	assert.Empty(t, res.Violations,
		"all mhoh blocks written by UpdateMetadataLE must pass mhoh-format (CRIT-1): %+v", res.Violations)

	// And the values must round-trip through the dual-convention reader.
	lib := parseLEPayloadForTest(t, updated)
	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "日本語のタイトル", lib.Tracks[0].Name)
	assert.Equal(t, "Café Artist", lib.Tracks[0].Artist)
}

// parseLEPayloadForTest wraps a decompressed LE payload into an ITLLibrary by
// walking the chunks directly (the payload is already decrypted/inflated).
func parseLEPayloadForTest(t *testing.T, payload []byte) *ITLLibrary {
	t.Helper()
	lib := &ITLLibrary{}
	walkChunksLEImpl(payload, lib)
	return lib
}
