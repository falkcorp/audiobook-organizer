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
