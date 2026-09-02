// file: internal/itunes/itl_mutation_test.go
// version: 1.0.1
// guid: c9d2e4f6-8a1b-4c3d-9e5f-7a6b8c0d1e2f
// last-edited: 2026-09-02

package itunes

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// trackSpec describes a track with configurable metadata for synthetic ITL
// building. Each field maps to a hohm type.
// ---------------------------------------------------------------------------

type trackSpec struct {
	TrackID  int
	PID      [8]byte
	Location string // hohm 0x0D
	LocalURL string // hohm 0x0B
	Name     string // hohm 0x02
	Album    string // hohm 0x03
	Artist   string // hohm 0x04
	Genre    string // hohm 0x05
	Kind     string // hohm 0x06

	// Numeric fields stored in htim header
	Size        int
	TotalTime   int
	TrackNumber int
	TrackCount  int
	DiscNumber  int
	DiscCount   int
	Year        int
	BitRate     int
	SampleRate  int
	PlayCount   int
	Rating      int
}

// makePID creates a deterministic 8-byte PID from an index.
func makePID(index int) [8]byte {
	var pid [8]byte
	pid[0] = 0xAA
	pid[1] = byte(index >> 24)
	pid[2] = byte(index >> 16)
	pid[3] = byte(index >> 8)
	pid[4] = byte(index)
	pid[5] = 0xBB
	pid[6] = 0xCC
	pid[7] = 0xDD
	return pid
}

// makeTrackSpec creates a trackSpec with sensible defaults for a given index.
func makeTrackSpec(index int) trackSpec {
	return trackSpec{
		TrackID:     100 + index,
		PID:         makePID(index),
		Location:    fmt.Sprintf("/music/track_%03d.mp3", index),
		Name:        fmt.Sprintf("Track %d", index),
		Album:       fmt.Sprintf("Album %d", index),
		Artist:      fmt.Sprintf("Artist %d", index),
		Genre:       "Rock",
		Kind:        "MPEG audio file",
		Size:        5000000 + index*100,
		TotalTime:   240000 + index*1000,
		TrackNumber: index + 1,
		Year:        2020 + (index % 6),
		BitRate:     320,
		SampleRate:  44100,
	}
}

// ---------------------------------------------------------------------------
// buildSyntheticITLMultiTrack builds a synthetic ITL (BE format, pre-v10)
// with N tracks, each with configurable metadata fields.
// This is the key helper for understanding track chunk structure.
// ---------------------------------------------------------------------------

func buildSyntheticITLMultiTrack(t *testing.T, version string, compress bool, tracks []trackSpec) []byte {
	t.Helper()

	var payload bytes.Buffer

	for _, tr := range tracks {
		// Build htim chunk (156 bytes, standard header)
		htimLen := 156
		htim := make([]byte, htimLen)
		copy(htim[0:4], "htim")
		writeUint32BE(htim, 4, uint32(htimLen))
		writeUint32BE(htim, 8, uint32(htimLen))
		writeUint32BE(htim, 16, uint32(tr.TrackID))
		writeUint32BE(htim, 36, uint32(tr.Size))
		writeUint32BE(htim, 40, uint32(tr.TotalTime))
		writeUint32BE(htim, 44, uint32(tr.TrackNumber))
		if tr.TrackCount > 0 {
			writeUint32BE(htim, 48, uint32(tr.TrackCount))
		}
		if tr.Year > 0 {
			binary.BigEndian.PutUint16(htim[54:56], uint16(tr.Year))
		}
		if tr.BitRate > 0 {
			binary.BigEndian.PutUint16(htim[58:60], uint16(tr.BitRate))
		}
		if tr.SampleRate > 0 {
			binary.BigEndian.PutUint16(htim[60:62], uint16(tr.SampleRate))
		}
		writeUint32BE(htim, 76, uint32(tr.PlayCount))
		htim[104] = byte(tr.DiscNumber)
		if tr.DiscCount > 0 {
			htim[106] = byte(tr.DiscCount)
		}
		htim[108] = byte(tr.Rating)
		copy(htim[128:136], tr.PID[:])
		payload.Write(htim)

		// Build hohm chunks for each non-empty string field.
		// Order matters: location first, then metadata (matches iTunes convention).
		if tr.Location != "" {
			payload.Write(buildHohmChunk(0x0D, tr.Location))
		}
		if tr.LocalURL != "" {
			payload.Write(buildHohmChunk(0x0B, tr.LocalURL))
		}
		if tr.Name != "" {
			payload.Write(buildHohmChunk(0x02, tr.Name))
		}
		if tr.Album != "" {
			payload.Write(buildHohmChunk(0x03, tr.Album))
		}
		if tr.Artist != "" {
			payload.Write(buildHohmChunk(0x04, tr.Artist))
		}
		if tr.Genre != "" {
			payload.Write(buildHohmChunk(0x05, tr.Genre))
		}
		if tr.Kind != "" {
			payload.Write(buildHohmChunk(0x06, tr.Kind))
		}
	}

	payloadBytes := payload.Bytes()
	if compress {
		payloadBytes = itlDeflate(payloadBytes)
	}
	encrypted := itlEncrypt(&hdfmHeader{version: version}, payloadBytes)

	fileLen := uint32(len(encrypted)) + 17 + uint32(len(version))
	hdr := buildHdfmHeader(version, nil, fileLen, 0)

	var file bytes.Buffer
	file.Write(hdr)
	file.Write(encrypted)
	return file.Bytes()
}

// parseITLFromBytes is a helper that writes data to a temp file and parses it.
func parseITLFromBytes(t *testing.T, data []byte) *ITLLibrary {
	t.Helper()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.itl")
	require.NoError(t, os.WriteFile(path, data, 0644))
	lib, err := ParseITL(path)
	require.NoError(t, err)
	return lib
}

// ===========================================================================
// 1. Single track addition tests
// ===========================================================================

func TestMutation_ZeroTracks(t *testing.T) {
	// Build ITL with zero tracks — should parse with empty track list
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, nil)
	lib := parseITLFromBytes(t, data)
	assert.Empty(t, lib.Tracks)
}

func TestMutation_SingleTrackMinimal(t *testing.T) {
	// Single track with only a location (minimal viable track)
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/music/minimal.mp3",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, 1, lib.Tracks[0].TrackID)
	assert.Equal(t, "/music/minimal.mp3", lib.Tracks[0].Location)
	assert.Equal(t, spec.PID, lib.Tracks[0].PersistentID)
	// Metadata fields should be empty
	assert.Empty(t, lib.Tracks[0].Name)
	assert.Empty(t, lib.Tracks[0].Album)
	assert.Empty(t, lib.Tracks[0].Artist)
}

func TestMutation_SingleTrackFull(t *testing.T) {
	// Single track with all fields populated
	spec := trackSpec{
		TrackID:     42,
		PID:         makePID(42),
		Location:    "/music/full.mp3",
		Name:        "Full Track",
		Album:       "Full Album",
		Artist:      "Full Artist",
		Genre:       "Jazz",
		Kind:        "MPEG audio file",
		Size:        10000000,
		TotalTime:   360000,
		TrackNumber: 3,
		TrackCount:  12,
		DiscNumber:  1,
		DiscCount:   2,
		Year:        2023,
		BitRate:     256,
		SampleRate:  48000,
		PlayCount:   15,
		Rating:      80,
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	tr := lib.Tracks[0]
	assert.Equal(t, 42, tr.TrackID)
	assert.Equal(t, spec.PID, tr.PersistentID)
	assert.Equal(t, "/music/full.mp3", tr.Location)
	assert.Equal(t, "Full Track", tr.Name)
	assert.Equal(t, "Full Album", tr.Album)
	assert.Equal(t, "Full Artist", tr.Artist)
	assert.Equal(t, "Jazz", tr.Genre)
	assert.Equal(t, "MPEG audio file", tr.Kind)
	assert.Equal(t, 10000000, tr.Size)
	assert.Equal(t, 360000, tr.TotalTime)
	assert.Equal(t, 3, tr.TrackNumber)
	assert.Equal(t, 12, tr.TrackCount)
	assert.Equal(t, 1, tr.DiscNumber)
	assert.Equal(t, 2, tr.DiscCount)
	assert.Equal(t, 2023, tr.Year)
	assert.Equal(t, 256, tr.BitRate)
	assert.Equal(t, 48000, tr.SampleRate)
	assert.Equal(t, 15, tr.PlayCount)
	assert.Equal(t, 80, tr.Rating)
}

func TestMutation_ZeroVsOneTrack(t *testing.T) {
	// Compare ITL with 0 tracks to one with 1 track to understand the delta
	zeroData := buildSyntheticITLMultiTrack(t, "9.0.0", false, nil)
	oneSpec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/music/one.mp3",
	}
	oneData := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{oneSpec})

	zeroLib := parseITLFromBytes(t, zeroData)
	oneLib := parseITLFromBytes(t, oneData)

	assert.Len(t, zeroLib.Tracks, 0)
	assert.Len(t, oneLib.Tracks, 1)
	assert.Equal(t, "/music/one.mp3", oneLib.Tracks[0].Location)

	// The one-track file should be larger
	assert.Greater(t, len(oneData), len(zeroData))
}

// ===========================================================================
// 2. Multi-track addition tests
// ===========================================================================

func TestMutation_MultiTrackCounts(t *testing.T) {
	counts := []int{1, 2, 5, 10, 50}
	for _, n := range counts {
		t.Run(fmt.Sprintf("%d_tracks", n), func(t *testing.T) {
			specs := make([]trackSpec, n)
			for i := range n {
				specs[i] = makeTrackSpec(i)
			}
			data := buildSyntheticITLMultiTrack(t, "9.0.0", true, specs)
			lib := parseITLFromBytes(t, data)

			require.Len(t, lib.Tracks, n, "expected %d tracks", n)

			// Verify PIDs are unique
			pidSet := make(map[string]bool)
			for i, tr := range lib.Tracks {
				pidHex := pidToHex(tr.PersistentID)
				assert.False(t, pidSet[pidHex], "duplicate PID at track %d: %s", i, pidHex)
				pidSet[pidHex] = true
			}

			// Verify each track has correct location
			for i, tr := range lib.Tracks {
				expected := fmt.Sprintf("/music/track_%03d.mp3", i)
				assert.Equal(t, expected, tr.Location, "track %d location", i)
			}
		})
	}
}

func TestMutation_MultiTrackMetadataPreserved(t *testing.T) {
	specs := make([]trackSpec, 5)
	for i := range 5 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 5)
	for i, tr := range lib.Tracks {
		assert.Equal(t, specs[i].TrackID, tr.TrackID, "track %d ID", i)
		assert.Equal(t, specs[i].Name, tr.Name, "track %d name", i)
		assert.Equal(t, specs[i].Album, tr.Album, "track %d album", i)
		assert.Equal(t, specs[i].Artist, tr.Artist, "track %d artist", i)
		assert.Equal(t, specs[i].Genre, tr.Genre, "track %d genre", i)
		assert.Equal(t, specs[i].Kind, tr.Kind, "track %d kind", i)
		assert.Equal(t, specs[i].Location, tr.Location, "track %d location", i)
		assert.Equal(t, specs[i].Size, tr.Size, "track %d size", i)
		assert.Equal(t, specs[i].TotalTime, tr.TotalTime, "track %d totalTime", i)
		assert.Equal(t, specs[i].TrackNumber, tr.TrackNumber, "track %d trackNumber", i)
		assert.Equal(t, specs[i].Year, tr.Year, "track %d year", i)
		assert.Equal(t, specs[i].BitRate, tr.BitRate, "track %d bitRate", i)
		assert.Equal(t, specs[i].SampleRate, tr.SampleRate, "track %d sampleRate", i)
	}
}

// ===========================================================================
// 3. Track removal simulation
// ===========================================================================

func TestMutation_TrackRemovalByExclusion(t *testing.T) {
	// Build ITL with 5 tracks
	specs5 := make([]trackSpec, 5)
	for i := range 5 {
		specs5[i] = makeTrackSpec(i)
	}

	// Build ITL with 4 tracks (remove track at index 2)
	specs4 := make([]trackSpec, 0, 4)
	specs4 = append(specs4, specs5[0], specs5[1], specs5[3], specs5[4])

	data5 := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs5)
	data4 := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs4)

	lib5 := parseITLFromBytes(t, data5)
	lib4 := parseITLFromBytes(t, data4)

	require.Len(t, lib5.Tracks, 5)
	require.Len(t, lib4.Tracks, 4)

	// The removed track's PID should not appear in lib4
	removedPID := pidToHex(specs5[2].PID)
	for _, tr := range lib4.Tracks {
		assert.NotEqual(t, removedPID, pidToHex(tr.PersistentID),
			"removed track should not be present")
	}

	// The remaining tracks should be intact
	assert.Equal(t, specs5[0].Location, lib4.Tracks[0].Location)
	assert.Equal(t, specs5[1].Location, lib4.Tracks[1].Location)
	assert.Equal(t, specs5[3].Location, lib4.Tracks[2].Location)
	assert.Equal(t, specs5[4].Location, lib4.Tracks[3].Location)
}

func TestMutation_RemoveFirstTrack(t *testing.T) {
	specs := make([]trackSpec, 3)
	for i := range 3 {
		specs[i] = makeTrackSpec(i)
	}
	// Remove first
	remaining := specs[1:]
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, remaining)
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 2)
	assert.Equal(t, specs[1].Location, lib.Tracks[0].Location)
	assert.Equal(t, specs[2].Location, lib.Tracks[1].Location)
}

func TestMutation_RemoveLastTrack(t *testing.T) {
	specs := make([]trackSpec, 3)
	for i := range 3 {
		specs[i] = makeTrackSpec(i)
	}
	// Remove last
	remaining := specs[:2]
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, remaining)
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 2)
	assert.Equal(t, specs[0].Location, lib.Tracks[0].Location)
	assert.Equal(t, specs[1].Location, lib.Tracks[1].Location)
}

func TestMutation_RemoveAllTracks(t *testing.T) {
	// Start with 3, remove all
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, nil)
	lib := parseITLFromBytes(t, data)
	assert.Empty(t, lib.Tracks)
}

// ===========================================================================
// 4. Metadata variation tests — different hohm type combinations
// ===========================================================================

func TestMutation_LocationOnly(t *testing.T) {
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/music/loc_only.mp3",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "/music/loc_only.mp3", lib.Tracks[0].Location)
	assert.Empty(t, lib.Tracks[0].Name)
	assert.Empty(t, lib.Tracks[0].Album)
	assert.Empty(t, lib.Tracks[0].Artist)
	assert.Empty(t, lib.Tracks[0].Genre)
	assert.Empty(t, lib.Tracks[0].Kind)
}

func TestMutation_LocationAndTitle(t *testing.T) {
	spec := trackSpec{
		TrackID:  2,
		PID:      makePID(2),
		Location: "/music/with_title.mp3",
		Name:     "My Title",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "/music/with_title.mp3", lib.Tracks[0].Location)
	assert.Equal(t, "My Title", lib.Tracks[0].Name)
	assert.Empty(t, lib.Tracks[0].Album)
	assert.Empty(t, lib.Tracks[0].Artist)
}

func TestMutation_LocationTitleArtist(t *testing.T) {
	spec := trackSpec{
		TrackID:  3,
		PID:      makePID(3),
		Location: "/music/with_artist.mp3",
		Name:     "Track Title",
		Artist:   "Track Artist",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "Track Title", lib.Tracks[0].Name)
	assert.Equal(t, "Track Artist", lib.Tracks[0].Artist)
	assert.Empty(t, lib.Tracks[0].Album)
}

func TestMutation_LocationTitleArtistAlbum(t *testing.T) {
	spec := trackSpec{
		TrackID:  4,
		PID:      makePID(4),
		Location: "/music/with_album.mp3",
		Name:     "Track Title",
		Artist:   "Track Artist",
		Album:    "Track Album",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "Track Title", lib.Tracks[0].Name)
	assert.Equal(t, "Track Artist", lib.Tracks[0].Artist)
	assert.Equal(t, "Track Album", lib.Tracks[0].Album)
	assert.Empty(t, lib.Tracks[0].Genre)
}

func TestMutation_AllStringFields(t *testing.T) {
	spec := trackSpec{
		TrackID:  5,
		PID:      makePID(5),
		Location: "/music/all_fields.mp3",
		LocalURL: "file://localhost/music/all_fields.mp3",
		Name:     "All Fields Track",
		Album:    "All Fields Album",
		Artist:   "All Fields Artist",
		Genre:    "Electronic",
		Kind:     "MPEG audio file",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	tr := lib.Tracks[0]
	assert.Equal(t, "/music/all_fields.mp3", tr.Location)
	assert.Equal(t, "file://localhost/music/all_fields.mp3", tr.LocalURL)
	assert.Equal(t, "All Fields Track", tr.Name)
	assert.Equal(t, "All Fields Album", tr.Album)
	assert.Equal(t, "All Fields Artist", tr.Artist)
	assert.Equal(t, "Electronic", tr.Genre)
	assert.Equal(t, "MPEG audio file", tr.Kind)
}

func TestMutation_MixedMetadataPerTrack(t *testing.T) {
	// Multiple tracks with different metadata combinations in the same ITL
	specs := []trackSpec{
		{TrackID: 1, PID: makePID(1), Location: "/music/a.mp3"},
		{TrackID: 2, PID: makePID(2), Location: "/music/b.mp3", Name: "B"},
		{TrackID: 3, PID: makePID(3), Location: "/music/c.mp3", Name: "C", Artist: "Artist C"},
		{TrackID: 4, PID: makePID(4), Location: "/music/d.mp3", Name: "D", Artist: "Artist D", Album: "Album D", Genre: "Pop"},
	}

	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 4)
	// Track 1: location only
	assert.Equal(t, "/music/a.mp3", lib.Tracks[0].Location)
	assert.Empty(t, lib.Tracks[0].Name)
	// Track 2: location + name
	assert.Equal(t, "B", lib.Tracks[1].Name)
	assert.Empty(t, lib.Tracks[1].Artist)
	// Track 3: location + name + artist
	assert.Equal(t, "Artist C", lib.Tracks[2].Artist)
	assert.Empty(t, lib.Tracks[2].Album)
	// Track 4: all metadata
	assert.Equal(t, "Pop", lib.Tracks[3].Genre)
}

// ===========================================================================
// 5. Round-trip stability tests
// ===========================================================================

func TestMutation_RoundTripUncompressed(t *testing.T) {
	specs := make([]trackSpec, 3)
	for i := range 3 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)

	// Parse
	lib := parseITLFromBytes(t, data)
	require.Len(t, lib.Tracks, 3)

	// Verify every field survives
	for i, tr := range lib.Tracks {
		assert.Equal(t, specs[i].TrackID, tr.TrackID, "track %d ID", i)
		assert.Equal(t, specs[i].PID, tr.PersistentID, "track %d PID", i)
		assert.Equal(t, specs[i].Location, tr.Location, "track %d location", i)
		assert.Equal(t, specs[i].Name, tr.Name, "track %d name", i)
		assert.Equal(t, specs[i].Album, tr.Album, "track %d album", i)
		assert.Equal(t, specs[i].Artist, tr.Artist, "track %d artist", i)
		assert.Equal(t, specs[i].Genre, tr.Genre, "track %d genre", i)
		assert.Equal(t, specs[i].Kind, tr.Kind, "track %d kind", i)
		assert.Equal(t, specs[i].Size, tr.Size, "track %d size", i)
		assert.Equal(t, specs[i].TotalTime, tr.TotalTime, "track %d totalTime", i)
		assert.Equal(t, specs[i].TrackNumber, tr.TrackNumber, "track %d trackNumber", i)
		assert.Equal(t, specs[i].Year, tr.Year, "track %d year", i)
		assert.Equal(t, specs[i].BitRate, tr.BitRate, "track %d bitRate", i)
		assert.Equal(t, specs[i].SampleRate, tr.SampleRate, "track %d sampleRate", i)
	}
}

func TestMutation_RoundTripCompressed(t *testing.T) {
	specs := make([]trackSpec, 3)
	for i := range 3 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", true, specs)

	lib := parseITLFromBytes(t, data)
	require.Len(t, lib.Tracks, 3)
	assert.True(t, lib.UseCompression)

	for i, tr := range lib.Tracks {
		assert.Equal(t, specs[i].Location, tr.Location, "track %d location", i)
		assert.Equal(t, specs[i].Name, tr.Name, "track %d name", i)
	}
}

func TestMutation_RoundTripV12(t *testing.T) {
	// v12 uses different encryption limit (maxCryptSize)
	specs := make([]trackSpec, 3)
	for i := range 3 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "12.0.0", true, specs)

	lib := parseITLFromBytes(t, data)
	require.Len(t, lib.Tracks, 3)
	assert.Equal(t, "12.0.0", lib.Version)

	for i, tr := range lib.Tracks {
		assert.Equal(t, specs[i].Location, tr.Location, "track %d location", i)
	}
}

func TestMutation_RoundTripWriteAndReparse(t *testing.T) {
	// Build -> write -> parse -> update location -> write -> parse -> verify
	specs := make([]trackSpec, 2)
	for i := range 2 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", true, specs)

	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "original.itl")
	require.NoError(t, os.WriteFile(itlPath, data, 0644))

	// Parse and verify
	lib1, err := ParseITL(itlPath)
	require.NoError(t, err)
	require.Len(t, lib1.Tracks, 2)

	// BE fixture — BE writeback refused (K12). The read path above still works.
	outPath := filepath.Join(tmpDir, "updated.itl")
	pid0Hex := pidToHex(specs[0].PID)
	_, err = UpdateITLLocations(itlPath, outPath, []ITLLocationUpdate{
		{PersistentID: pid0Hex, NewLocation: "/new/path/updated.mp3"},
	})
	require.ErrorIs(t, err, ErrBEWritebackUnsupported, "BE writeback must be refused (K12)")
}

// ===========================================================================
// 6. Mixed encoding tests
// ===========================================================================

func TestMutation_ASCIIPath(t *testing.T) {
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/music/simple/path.mp3",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)
	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "/music/simple/path.mp3", lib.Tracks[0].Location)
}

func TestMutation_UTF16Title(t *testing.T) {
	// Non-Latin characters force UTF-16BE encoding
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/music/japanese.mp3",
		Name:     "日本語タイトル",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "日本語タイトル", lib.Tracks[0].Name)
	assert.Equal(t, "/music/japanese.mp3", lib.Tracks[0].Location)
}

func TestMutation_Windows1252Artist(t *testing.T) {
	// Latin characters with accents -> Windows-1252 encoding
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/music/french.mp3",
		Artist:   "Édith Piaf",
		Name:     "La Vie en Rose",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "Édith Piaf", lib.Tracks[0].Artist)
	assert.Equal(t, "La Vie en Rose", lib.Tracks[0].Name)
}

func TestMutation_MixedEncodingsInSameTrack(t *testing.T) {
	// ASCII path, UTF-16 title (CJK), Windows-1252 artist (accented Latin)
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/music/mixed_encoding.mp3",
		Name:     "東京ドリフト",            // UTF-16 (CJK)
		Artist:   "Müller & Associés", // Windows-1252 (accented)
		Album:    "Plain Album",       // ASCII
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "/music/mixed_encoding.mp3", lib.Tracks[0].Location)
	assert.Equal(t, "東京ドリフト", lib.Tracks[0].Name)
	assert.Equal(t, "Müller & Associés", lib.Tracks[0].Artist)
	assert.Equal(t, "Plain Album", lib.Tracks[0].Album)
}

func TestMutation_MixedEncodingsAcrossTracks(t *testing.T) {
	specs := []trackSpec{
		{TrackID: 1, PID: makePID(1), Location: "/music/a.mp3", Name: "English Title"},
		{TrackID: 2, PID: makePID(2), Location: "/music/b.mp3", Name: "日本語タイトル"},
		{TrackID: 3, PID: makePID(3), Location: "/music/c.mp3", Name: "Über den Wolken"},
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 3)
	assert.Equal(t, "English Title", lib.Tracks[0].Name)
	assert.Equal(t, "日本語タイトル", lib.Tracks[1].Name)
	assert.Equal(t, "Über den Wolken", lib.Tracks[2].Name)
}

// ===========================================================================
// 7. Edge cases
// ===========================================================================

func TestMutation_EmptyLocation(t *testing.T) {
	spec := trackSpec{
		TrackID: 1,
		PID:     makePID(1),
		// Location intentionally empty — no hohm 0x0D written
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Empty(t, lib.Tracks[0].Location)
}

func TestMutation_VeryLongPath(t *testing.T) {
	// 500+ character path
	longDir := strings.Repeat("/very/long/directory/path", 25)
	longPath := longDir + "/track.mp3"
	assert.Greater(t, len(longPath), 500, "path should be > 500 chars")

	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: longPath,
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, longPath, lib.Tracks[0].Location)
}

func TestMutation_TrackIDZero(t *testing.T) {
	spec := trackSpec{
		TrackID:  0,
		PID:      makePID(0),
		Location: "/music/zero_id.mp3",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, 0, lib.Tracks[0].TrackID)
	assert.Equal(t, "/music/zero_id.mp3", lib.Tracks[0].Location)
}

func TestMutation_AllZeroPID(t *testing.T) {
	spec := trackSpec{
		TrackID:  1,
		PID:      [8]byte{0, 0, 0, 0, 0, 0, 0, 0},
		Location: "/music/zero_pid.mp3",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, [8]byte{}, lib.Tracks[0].PersistentID)
	assert.Equal(t, "/music/zero_pid.mp3", lib.Tracks[0].Location)
}

func TestMutation_DuplicatePIDs(t *testing.T) {
	// Two tracks with the same PID — parser should still return both
	// (dedup is a higher-level concern)
	samePID := makePID(99)
	specs := []trackSpec{
		{TrackID: 1, PID: samePID, Location: "/music/dup1.mp3", Name: "Dup 1"},
		{TrackID: 2, PID: samePID, Location: "/music/dup2.mp3", Name: "Dup 2"},
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 2)
	assert.Equal(t, samePID, lib.Tracks[0].PersistentID)
	assert.Equal(t, samePID, lib.Tracks[1].PersistentID)
	assert.Equal(t, "/music/dup1.mp3", lib.Tracks[0].Location)
	assert.Equal(t, "/music/dup2.mp3", lib.Tracks[1].Location)
}

func TestMutation_StressTest1000Tracks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}
	n := 1000
	specs := make([]trackSpec, n)
	for i := range n {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", true, specs)
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, n)

	// Spot check first, middle, last
	assert.Equal(t, "/music/track_000.mp3", lib.Tracks[0].Location)
	assert.Equal(t, fmt.Sprintf("/music/track_%03d.mp3", n/2), lib.Tracks[n/2].Location)
	assert.Equal(t, fmt.Sprintf("/music/track_%03d.mp3", n-1), lib.Tracks[n-1].Location)

	// Verify all PIDs unique
	pidSet := make(map[string]bool)
	for _, tr := range lib.Tracks {
		pidHex := pidToHex(tr.PersistentID)
		assert.False(t, pidSet[pidHex], "duplicate PID: %s", pidHex)
		pidSet[pidHex] = true
	}
}

func TestMutation_UnicodeInPath(t *testing.T) {
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/müsik/フォルダ/трек.mp3",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, "/müsik/フォルダ/трек.mp3", lib.Tracks[0].Location)
}

func TestMutation_SpecialCharsInMetadata(t *testing.T) {
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/music/special.mp3",
		Name:     `Track "With" <Special> & Chars`,
		Artist:   "O'Brien & Associates",
		Album:    "100% Pure (Deluxe)",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, `Track "With" <Special> & Chars`, lib.Tracks[0].Name)
	assert.Equal(t, "O'Brien & Associates", lib.Tracks[0].Artist)
	assert.Equal(t, "100% Pure (Deluxe)", lib.Tracks[0].Album)
}

func TestMutation_LargeTrackID(t *testing.T) {
	spec := trackSpec{
		TrackID:  999999,
		PID:      makePID(1),
		Location: "/music/large_id.mp3",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 1)
	assert.Equal(t, 999999, lib.Tracks[0].TrackID)
}

// ===========================================================================
// 8. InsertITLTracks integration with multi-track synthetic ITLs
// ===========================================================================

// TestMutation_InsertIntoEmptyITL: v9 synthetic libraries are big-endian; the
// SafeWriteITL chokepoint refuses BE writeback (TASK-004, SPEC §3 step 1 / K12).
func TestMutation_InsertIntoEmptyITL(t *testing.T) {
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, nil)

	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "empty.itl")
	outPath := filepath.Join(tmpDir, "out.itl")
	require.NoError(t, os.WriteFile(itlPath, data, 0644))

	_, err := InsertITLTracks(itlPath, outPath, []ITLNewTrack{
		{Location: "/music/inserted.mp3", Name: "Inserted Track"},
	})
	require.ErrorIs(t, err, ErrBEWritebackUnsupported)
	_, statErr := os.Stat(outPath)
	require.Error(t, statErr, "refused BE write must not create output")
}

// TestMutation_InsertMultipleIntoExisting: BE fixture → refused (see above).
func TestMutation_InsertMultipleIntoExisting(t *testing.T) {
	specs := make([]trackSpec, 3)
	for i := range 3 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)

	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "base.itl")
	outPath := filepath.Join(tmpDir, "out.itl")
	require.NoError(t, os.WriteFile(itlPath, data, 0644))

	_, err := InsertITLTracks(itlPath, outPath, []ITLNewTrack{
		{Location: "/music/new1.mp3", Name: "New Track 1", Artist: "New Artist"},
		{Location: "/music/new2.mp3", Name: "New Track 2", Album: "New Album"},
	})
	require.ErrorIs(t, err, ErrBEWritebackUnsupported)
	_, statErr := os.Stat(outPath)
	require.Error(t, statErr, "refused BE write must not create output")
}

// ===========================================================================
// 9. Version variation tests
// ===========================================================================

func TestMutation_VersionVariations(t *testing.T) {
	versions := []string{"7.0.0", "9.0.0", "9.2.1", "10.0.0", "12.0.0", "12.9.5.5"}
	for _, ver := range versions {
		t.Run(ver, func(t *testing.T) {
			spec := makeTrackSpec(0)
			data := buildSyntheticITLMultiTrack(t, ver, true, []trackSpec{spec})
			lib := parseITLFromBytes(t, data)

			assert.Equal(t, ver, lib.Version)
			require.Len(t, lib.Tracks, 1)
			assert.Equal(t, spec.Location, lib.Tracks[0].Location)
		})
	}
}

// ===========================================================================
// 10. Compression toggle tests
// ===========================================================================

func TestMutation_CompressionToggle(t *testing.T) {
	specs := make([]trackSpec, 5)
	for i := range 5 {
		specs[i] = makeTrackSpec(i)
	}

	for _, compress := range []bool{false, true} {
		name := "uncompressed"
		if compress {
			name = "compressed"
		}
		t.Run(name, func(t *testing.T) {
			data := buildSyntheticITLMultiTrack(t, "9.0.0", compress, specs)
			lib := parseITLFromBytes(t, data)

			assert.Equal(t, compress, lib.UseCompression)
			require.Len(t, lib.Tracks, 5)
			for i, tr := range lib.Tracks {
				assert.Equal(t, specs[i].Location, tr.Location, "track %d", i)
			}
		})
	}
}

// ===========================================================================
// 11. PID hex conversion consistency
// ===========================================================================

func TestMutation_PIDHexRoundTrip(t *testing.T) {
	specs := make([]trackSpec, 10)
	for i := range 10 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)
	lib := parseITLFromBytes(t, data)

	require.Len(t, lib.Tracks, 10)
	for i, tr := range lib.Tracks {
		// PID should survive: spec -> bytes -> parse -> PID
		assert.Equal(t, specs[i].PID, tr.PersistentID, "track %d PID bytes", i)

		// pidToHex should produce consistent hex
		hexStr := pidToHex(tr.PersistentID)
		back, err := hexToPID(hexStr)
		require.NoError(t, err, "track %d hex->PID", i)
		assert.Equal(t, tr.PersistentID, back, "track %d PID round-trip via hex", i)
	}
}

// ===========================================================================
// 12. Location update with multi-track (update one, leave others)
// ===========================================================================

func TestMutation_UpdateOneOfMany(t *testing.T) {
	specs := make([]trackSpec, 5)
	for i := range 5 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", true, specs)

	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "multi.itl")
	outPath := filepath.Join(tmpDir, "updated.itl")
	require.NoError(t, os.WriteFile(itlPath, data, 0644))

	// BE fixture — BE writeback refused (K12).
	targetPID := pidToHex(specs[2].PID)
	_, err := UpdateITLLocations(itlPath, outPath, []ITLLocationUpdate{
		{PersistentID: targetPID, NewLocation: "/updated/track_002.mp3"},
	})
	require.ErrorIs(t, err, ErrBEWritebackUnsupported, "BE writeback must be refused (K12)")
}

func TestMutation_UpdateMultipleOfMany(t *testing.T) {
	specs := make([]trackSpec, 5)
	for i := range 5 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)

	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "multi.itl")
	outPath := filepath.Join(tmpDir, "updated.itl")
	require.NoError(t, os.WriteFile(itlPath, data, 0644))

	// BE fixture — BE writeback refused (K12).
	updates := []ITLLocationUpdate{
		{PersistentID: pidToHex(specs[0].PID), NewLocation: "/new/track_000.mp3"},
		{PersistentID: pidToHex(specs[2].PID), NewLocation: "/new/track_002.mp3"},
		{PersistentID: pidToHex(specs[4].PID), NewLocation: "/new/track_004.mp3"},
	}
	_, err := UpdateITLLocations(itlPath, outPath, updates)
	require.ErrorIs(t, err, ErrBEWritebackUnsupported, "BE writeback must be refused (K12)")
}

// ===========================================================================
// 13. Extension rewrite with multi-track
// ===========================================================================

func TestMutation_ExtensionRewriteMultiTrack(t *testing.T) {
	specs := []trackSpec{
		{TrackID: 1, PID: makePID(1), Location: "/music/a.flac", Name: "A"},
		{TrackID: 2, PID: makePID(2), Location: "/music/b.mp3", Name: "B"},
		{TrackID: 3, PID: makePID(3), Location: "/music/c.flac", Name: "C"},
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)

	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "ext.itl")
	outPath := filepath.Join(tmpDir, "out.itl")
	require.NoError(t, os.WriteFile(itlPath, data, 0644))

	// BE fixture — extension rewrite is BE-only; BE writeback refused (K12).
	_, err := RewriteITLExtensions(itlPath, outPath, ".flac", ".mp3")
	require.ErrorIs(t, err, ErrBEWritebackUnsupported, "BE writeback must be refused (K12)")
}

// ===========================================================================
// 14. Chunk structure analysis tests
// ===========================================================================

func TestMutation_ChunkStructureMinimalTrack(t *testing.T) {
	// Build a single track and inspect the raw decrypted payload to understand
	// the minimal chunk structure needed for a track
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/test.mp3",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})

	// Parse the hdfm header to get to the payload
	hdr, err := parseHdfmHeader(data)
	require.NoError(t, err)

	payload := data[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, _, err := itlInflate(decrypted)
	require.NoError(t, err)

	// Walk the raw chunks and catalog them
	offset := 0
	var tags []string
	for offset+8 <= len(decompressed) {
		tag := readTag(decompressed, offset)
		if tag == "" {
			break
		}
		length := int(readUint32BE(decompressed, offset+4))
		if length < 8 || offset+length > len(decompressed) {
			break
		}
		tags = append(tags, tag)
		offset += length
	}

	// Minimal track should have exactly: htim + hohm (location)
	require.Len(t, tags, 2)
	assert.Equal(t, "htim", tags[0])
	assert.Equal(t, "hohm", tags[1])
}

func TestMutation_ChunkStructureFullTrack(t *testing.T) {
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/test.mp3",
		Name:     "Test",
		Album:    "Album",
		Artist:   "Artist",
		Genre:    "Rock",
		Kind:     "MPEG audio file",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})

	hdr, err := parseHdfmHeader(data)
	require.NoError(t, err)
	payload := data[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, _, err := itlInflate(decrypted)
	require.NoError(t, err)

	offset := 0
	var tags []string
	for offset+8 <= len(decompressed) {
		tag := readTag(decompressed, offset)
		if tag == "" {
			break
		}
		length := int(readUint32BE(decompressed, offset+4))
		if length < 8 || offset+length > len(decompressed) {
			break
		}
		tags = append(tags, tag)
		offset += length
	}

	// Full track: htim + 6 hohm chunks (location, name, album, artist, genre, kind)
	require.Len(t, tags, 7)
	assert.Equal(t, "htim", tags[0])
	for i := 1; i < 7; i++ {
		assert.Equal(t, "hohm", tags[i], "chunk %d should be hohm", i)
	}
}

func TestMutation_HohmTypeOrdering(t *testing.T) {
	// Verify the hohm types are written in the expected order
	spec := trackSpec{
		TrackID:  1,
		PID:      makePID(1),
		Location: "/test.mp3",
		LocalURL: "file://localhost/test.mp3",
		Name:     "Test",
		Album:    "Album",
		Artist:   "Artist",
		Genre:    "Rock",
		Kind:     "MPEG audio file",
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, []trackSpec{spec})

	hdr, err := parseHdfmHeader(data)
	require.NoError(t, err)
	payload := data[hdr.headerLen:]
	decrypted := itlDecrypt(hdr, payload)
	decompressed, _, err := itlInflate(decrypted)
	require.NoError(t, err)

	offset := 0
	var hohmTypes []int
	for offset+8 <= len(decompressed) {
		tag := readTag(decompressed, offset)
		if tag == "" {
			break
		}
		length := int(readUint32BE(decompressed, offset+4))
		if length < 8 || offset+length > len(decompressed) {
			break
		}
		if tag == "hohm" && length >= 16 {
			hohmType := int(readUint32BE(decompressed, offset+12))
			hohmTypes = append(hohmTypes, hohmType)
		}
		offset += length
	}

	// Expected order: location(0x0D), localURL(0x0B), name(0x02), album(0x03),
	// artist(0x04), genre(0x05), kind(0x06)
	expected := []int{0x0D, 0x0B, 0x02, 0x03, 0x04, 0x05, 0x06}
	assert.Equal(t, expected, hohmTypes)
}

// ===========================================================================
// 15. Playlist tests with multi-track
// ===========================================================================

// TestMutation_InsertPlaylistIntoMultiTrackITL: BE fixture → refused (TASK-004,
// SPEC §3 step 1 / K12).
func TestMutation_InsertPlaylistIntoMultiTrackITL(t *testing.T) {
	specs := make([]trackSpec, 3)
	for i := range 3 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", false, specs)

	tmpDir := t.TempDir()
	itlPath := filepath.Join(tmpDir, "base.itl")
	outPath := filepath.Join(tmpDir, "with_playlist.itl")
	require.NoError(t, os.WriteFile(itlPath, data, 0644))

	_, err := InsertITLPlaylist(itlPath, outPath, ITLNewPlaylist{
		Title:    "Test Playlist",
		TrackIDs: []int{specs[0].TrackID, specs[2].TrackID},
	})
	require.ErrorIs(t, err, ErrBEWritebackUnsupported)
	_, statErr := os.Stat(outPath)
	require.Error(t, statErr, "refused BE write must not create output")
}

// ===========================================================================
// 16. Sequential mutation tests (build -> insert -> update -> verify)
// ===========================================================================

// TestMutation_SequentialMutations: post-TASK-004 EVERY writeback entry point
// routes through the SafeWriteITL chokepoint, which refuses BE (K12). The v9
// synthetic fixture is big-endian, so the very first mutation (InsertITLTracks)
// is refused — previously only the location-update path refused BE.
func TestMutation_SequentialMutations(t *testing.T) {
	tmpDir := t.TempDir()

	// Step 1: Build initial BE (v9) ITL with 2 tracks.
	specs := make([]trackSpec, 2)
	for i := range 2 {
		specs[i] = makeTrackSpec(i)
	}
	data := buildSyntheticITLMultiTrack(t, "9.0.0", true, specs)
	path1 := filepath.Join(tmpDir, "step1.itl")
	require.NoError(t, os.WriteFile(path1, data, 0644))

	// Step 2: Insert a new track — refused, BE writeback unsupported.
	path2 := filepath.Join(tmpDir, "step2.itl")
	_, err := InsertITLTracks(path1, path2, []ITLNewTrack{
		{Location: "/music/new.mp3", Name: "New Track", Artist: "New Artist"},
	})
	require.ErrorIs(t, err, ErrBEWritebackUnsupported, "BE writeback must be refused (K12)")
	_, statErr := os.Stat(path2)
	require.Error(t, statErr, "refused BE write must not create output")

	// Step 3: Update location — also refused for the same BE fixture.
	path3 := filepath.Join(tmpDir, "step3.itl")
	_, err = UpdateITLLocations(path1, path3, []ITLLocationUpdate{
		{PersistentID: pidToHex(specs[0].PID), NewLocation: "/moved/track_000.mp3"},
	})
	require.ErrorIs(t, err, ErrBEWritebackUnsupported, "BE writeback must be refused (K12)")
}
