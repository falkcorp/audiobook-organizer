// file: internal/itunes/mhoh_string_test.go
// version: 1.0.0
// guid: 2c8e7a14-9b03-4f6d-8a51-d3f0b6e29c87
//
// Tests for the iTunes-conformant mhoh string encoders (TASK-005, CRIT-1):
//   - property round-trips: encode → parse == input (ASCII, Latin-1, CJK, curly-quote)
//   - every written block passes the T003 mhoh-format guard (contract imported)
//   - buildMhohLE (append) and rewriteHohmLocationLE (replace) are byte-identical
//   - UTF-16 is LITTLE-endian (the OLD code wrote BE — proven with a fixture)
//   - dual-convention decode (legacy +27 still parses)
//   - out-of-corpus hohmType is refused (never invented)
//   - BE writeback returns ErrBEWritebackUnsupported (K12)

package itunes

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTripMhoh builds an mhoh block for (hohmType, s) via the production
// encoder, then decodes it via the dual-convention decoder and returns the
// decoded string. ok mirrors buildMhohLE's in-table flag.
func roundTripMhoh(t *testing.T, hohmType uint32, s string) (string, bool) {
	t.Helper()
	block, ok := buildMhohLE(hohmType, s)
	if !ok {
		return "", false
	}
	got, err := decodeMhohBlock(block)
	require.NoError(t, err)
	return got, true
}

// TestEncodeMhohITunes_RoundTrip covers the property: encode → parse == input
// across encodings, for the writer-relevant hohm types.
func TestEncodeMhohITunes_RoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		hohmType uint32
		value    string
	}{
		// 0x02 (Name) allows {1=utf16le, 3=latin1} (K16-corrected).
		{"ascii_name", 0x02, "The Way of Kings"},
		{"latin1_name", 0x02, "Café Société — naïve"},
		{"cjk_name", 0x02, "日本語のタイトル"},
		{"curly_quote_name", 0x02, "It’s a “Test” — dash"},
		{"emoji_name", 0x02, "Book \U0001F4DA Title"}, // astral plane → surrogate pair
		// 0x04 (Artist), 0x03 (Album), 0x05 (Genre), 0x0C (Composer/Narrator).
		{"latin1_artist", 0x04, "Brandon Sanderson"},
		{"cjk_album", 0x03, "村上春樹"},
		{"latin1_genre", 0x05, "Fiction"},
		{"latin1_composer", 0x0C, "Michael Kramer & Kate Reading"},
		// 0x06 (Kind) is {3}-only = Windows-1252-only (K16-corrected): its
		// values are ASCII enum strings. Non-Latin Kind is unrepresentable and
		// must error — covered by TestEncodeMhohITunes_KindNonLatinErrors.
		{"kind_ascii", 0x06, "MPEG audio file"},
		// 0x0D (Location) Windows path, Latin-1 representable.
		{"location_latin1", 0x0D, `W:\itunes\iTunes Media\Audiobooks\Author\book.mp3`},
		{"location_unicode", 0x0D, `W:\itunes\著者\タイトル.m4b`},
		// 0x0B (LocalURL) is ASCII percent-encoded → at24 ∈ {0,2}.
		{"localurl_ascii", 0x0B, "file://localhost/W:/itunes/iTunes%20Media/x.mp3"},
		// 0x64 (playlist title).
		{"playlist_latin1", 0x64, "My Playlist"},
		{"playlist_cjk", 0x64, "プレイリスト"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := roundTripMhoh(t, tc.hohmType, tc.value)
			require.True(t, ok, "type 0x%X must be in the corpus table", tc.hohmType)
			assert.Equal(t, tc.value, got, "encode→parse must equal input")
		})
	}
}

// TestEncodeMhohITunes_Plus27AlwaysZero asserts the CRIT-1 invariant: byte +27
// is always 0x00 in every block our writers produce. The OLD encoder stamped
// +27 ∈ {1,3}, the corruption signature foreign to iTunes.
func TestEncodeMhohITunes_Plus27AlwaysZero(t *testing.T) {
	for _, ht := range []uint32{0x02, 0x03, 0x04, 0x05, 0x06, 0x0B, 0x0C, 0x0D, 0x64} {
		for _, s := range []string{"ascii", "café", "日本語", "x’y"} {
			block, ok := buildMhohLE(ht, s)
			if !ok {
				// Latin-1-only types (e.g. 0x06 Kind, K16-corrected) must
				// refuse non-representable strings rather than invent an
				// indicator; that refusal is the correct outcome here.
				entry := ITunesMhohEncoding[ht]
				require.False(t, entry.AllowedAt24Contains(at24UTF16LE),
					"type 0x%X value %q: encode refused despite UTF-16LE being allowed", ht, s)
				require.False(t, isLatin1Representable(s),
					"type 0x%X value %q: encode refused a representable string", ht, s)
				continue
			}
			require.GreaterOrEqual(t, len(block), 40)
			assert.Equal(t, byte(0), block[27],
				"type 0x%X value %q: byte +27 must be 0x00 (K3)", ht, s)
			// headerLen must be the corpus-conformant 24.
			assert.Equal(t, uint32(24), binary.LittleEndian.Uint32(block[4:8]),
				"type 0x%X: headerLen must be 24 (K5)", ht)
			// +24 indicator must be in the corpus-allowed set for the type.
			at24 := binary.LittleEndian.Uint32(block[24:28])
			entry := ITunesMhohEncoding[ht]
			assert.True(t, entry.AllowedAt24Contains(at24),
				"type 0x%X value %q: +24=%d not in corpus set %v", ht, s, at24, entry.AllowedAt24)
		}
	}
}

// TestEncodeMhohITunes_UTF16IsLittleEndian proves the byte order: the OLD code
// wrote UTF-16 BIG-endian; iTunes' UTF-16 blocks (at24==1, K16-corrected) are
// LITTLE-endian. For "日" (U+65E5) the LE bytes are 0xE5 0x65, NOT 0x65 0xE5.
func TestEncodeMhohITunes_UTF16IsLittleEndian(t *testing.T) {
	block, ok := buildMhohLE(0x02, "日") // U+65E5
	require.True(t, ok)

	// at24 must be 1 (UTF-16LE, K16-corrected).
	assert.Equal(t, uint32(1), binary.LittleEndian.Uint32(block[24:28]), "non-Latin → UTF-16LE (at24=1)")

	// And a non-Latin Kind (a {3}=latin1-only type) must refuse to encode
	// rather than invent an indicator iTunes never writes for that field.
	_, _, err := encodeMhohITunes(0x06, "AAC オーディオ")
	assert.Error(t, err, "non-Latin Kind must error (latin1-only type)")

	strLen := int(binary.LittleEndian.Uint32(block[28:32]))
	require.Equal(t, 2, strLen, "one BMP rune → 2 bytes")
	payload := block[40 : 40+strLen]
	// LITTLE-endian: low byte first.
	assert.Equal(t, []byte{0xE5, 0x65}, payload,
		"U+65E5 must be encoded little-endian (0xE5 0x65); big-endian (0x65 0xE5) is the OLD corruption")

	// And it must decode back.
	got, err := decodeMhohBlock(block)
	require.NoError(t, err)
	assert.Equal(t, "日", got)
}

// TestEncodeMhohITunes_AppendAndReplaceByteIdentical asserts the two writer
// paths (buildMhohLE append, rewriteHohmLocationLE replace) produce byte-for-byte
// identical output for identical (hohmType, value) input.
func TestEncodeMhohITunes_AppendAndReplaceByteIdentical(t *testing.T) {
	cases := []struct {
		hohmType uint32
		value    string
	}{
		{0x02, "ASCII Title"},
		{0x02, "Café"},
		{0x02, "日本語"},
		{0x0D, `W:\itunes\book.mp3`},
		{0x0B, "file://localhost/W:/x.mp3"},
		{0x06, "MPEG audio file"},
	}
	for _, tc := range cases {
		appendBlock, ok := buildMhohLE(tc.hohmType, tc.value)
		require.True(t, ok)

		// rewriteHohmLocationLE needs an existing block to read the hohmType from.
		// Feed it the append block itself (offset 0) with the same value.
		replaceBlock := rewriteHohmLocationLE(appendBlock, 0, len(appendBlock), tc.value)

		assert.Equal(t, appendBlock, replaceBlock,
			"append and replace paths must be byte-identical for type 0x%X value %q", tc.hohmType, tc.value)
	}
}

// TestEncodeMhohITunes_EveryBlockPassesMhohFormatGuard wraps written blocks in a
// minimal LE payload and runs the T003 mhoh-format guard (imported from the
// contract). Every produced block must pass.
func TestEncodeMhohITunes_EveryBlockPassesMhohFormatGuard(t *testing.T) {
	pid := [8]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

	values := []struct {
		hohmType uint32
		value    string
	}{
		{0x02, "Latin Café"},
		{0x03, "日本語アルバム"},
		{0x04, "Author Name"},
		{0x05, "Genre"},
		{0x06, "MPEG audio file"},
		{0x0C, "Narrator"},
		{0x0D, `W:\itunes\book.mp3`},
	}
	var mhohs [][]byte
	for _, v := range values {
		block, ok := buildMhohLE(v.hohmType, v.value)
		require.True(t, ok)
		mhohs = append(mhohs, block)
	}

	trackContent := buildLETrackSection(1, pid, mhohs...)
	payload := buildLEPayload(trackContent)

	res := guardMhohFormat(nil, payload, nil, DefaultContractConfig())
	assert.Empty(t, res.Violations, "every written block must pass mhoh-format: %+v", res.Violations)
}

// TestDecodeMhohBlock_DualConvention verifies the decoder reads BOTH a legacy
// (+27 nonzero) block AND an iTunes-conformant (+27==0, +24 indicator) block.
func TestDecodeMhohBlock_DualConvention(t *testing.T) {
	// 1) iTunes-conformant block via the production encoder.
	conformant, ok := buildMhohLE(0x02, "Café")
	require.True(t, ok)
	require.Equal(t, byte(0), conformant[27])
	got, err := decodeMhohBlock(conformant)
	require.NoError(t, err)
	assert.Equal(t, "Café", got)

	// 2) Legacy block: +27 = 3 (old "Windows-1252" flag), +24 ignored.
	// Build a block manually with the legacy convention.
	legacy := make([]byte, 40+len("Café-ish"))
	copy(legacy[0:4], "mhoh")
	binary.LittleEndian.PutUint32(legacy[4:8], 24)
	binary.LittleEndian.PutUint32(legacy[8:12], uint32(len(legacy)))
	binary.LittleEndian.PutUint32(legacy[12:16], 0x02)
	// Legacy encoder stamped the flag at +27; encode the bytes as Windows-1252.
	legacyPayload, _ := encodeHohmString("Café-ish") // returns (latin1 bytes, flag 3)
	legacy = legacy[:40+len(legacyPayload)]
	binary.LittleEndian.PutUint32(legacy[28:32], uint32(len(legacyPayload)))
	legacy[27] = 3 // legacy Windows-1252 flag
	copy(legacy[40:], legacyPayload)

	gotLegacy, err := decodeMhohBlock(legacy)
	require.NoError(t, err)
	assert.Equal(t, "Café-ish", gotLegacy, "legacy +27=3 block must still decode")
}

// TestEncodeMhohITunes_RefusesOutOfCorpusType asserts the encoder errors (never
// guesses) for a hohmType absent from the corpus table, and that the LE writers
// surface that as a skip rather than an invented block.
func TestEncodeMhohITunes_RefusesOutOfCorpusType(t *testing.T) {
	const bogusType = 0x7777 // not in ITunesMhohEncoding

	_, _, err := encodeMhohITunes(bogusType, "x")
	require.Error(t, err, "out-of-corpus type must error, not guess")

	block, ok := buildMhohLE(bogusType, "x")
	assert.False(t, ok, "buildMhohLE must report not-built for an out-of-corpus type")
	assert.Nil(t, block)

	// rewriteHohmLocationLE must preserve the original block unmodified.
	orig := make([]byte, 48)
	copy(orig[0:4], "mhoh")
	binary.LittleEndian.PutUint32(orig[4:8], 24)
	binary.LittleEndian.PutUint32(orig[8:12], 48)
	binary.LittleEndian.PutUint32(orig[12:16], bogusType)
	binary.LittleEndian.PutUint32(orig[28:32], 8)
	copy(orig[40:], []byte("original"))

	out := rewriteHohmLocationLE(orig, 0, len(orig), "replacement")
	assert.Equal(t, orig, out, "out-of-corpus type must be preserved byte-for-byte")
}

// TestErrBEWritebackUnsupported_Sentinel documents the K12 sentinel exists and is
// distinct. (The full BE-refusal behavior of UpdateITLLocations / ApplyITLOperations
// / RewriteITLExtensions is asserted in itl_writeback_test, itl_mutation_test,
// itl_regression_test, and itl_test against BE fixtures.)
func TestErrBEWritebackUnsupported_Sentinel(t *testing.T) {
	require.Error(t, ErrBEWritebackUnsupported)
	assert.Contains(t, ErrBEWritebackUnsupported.Error(), "BE writeback unsupported")
}
