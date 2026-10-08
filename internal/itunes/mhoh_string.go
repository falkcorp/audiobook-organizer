// file: internal/itunes/mhoh_string.go
// version: 2.1.0
// guid: 6f3b9d12-4a87-4c0e-9b21-7e5d2a8c1f04
// last-edited: 2026-10-07

// iTunes-conformant mhoh string encoders/decoders (fable5 TASK-005, CRIT-1).
//
// WHY this file exists (K3 / corpus facts):
//
// Forensics on the golden iTunes library (cmd/itl-audit-encoding, 2026-06-09;
// internal/itunes/mhoh_encoding_table.go) proved two facts our OLD writer
// violated:
//
//  1. iTunes writes byte +27 of every string mhoh as 0x00 — ALWAYS. Our old
//     encodeHohmString stamped +27 ∈ {1,3} (its "encoding flag"), a value that
//     appears in NONE of the 281,790 golden string blocks but in tens of
//     thousands of blocks of every iTunes-rejected ("Damaged") library. +27 != 0
//     is a corruption signature foreign to iTunes (CRIT-1 / SPEC §1 K3).
//
//  2. iTunes signals string encoding at byte +24 (a little-endian u32), NOT at
//     +27. Corpus-observed values: 0=ASCII/percent-encoded, 1=UTF-16LE,
//     2=UTF-8, 3=Windows-1252/Latin-1. The allowed set per hohmType is the
//     authoritative ITunesMhohEncoding table (T002).
//
//     K16 CORRECTION (2026-07-03, SPEC 3): the original T002/T005 work assigned
//     1=Latin-1 and 3=UTF-16LE — EXACTLY SWAPPED. Byte-level probes of
//     golden-library blocks are unambiguous: at24==3 blocks hold single-byte
//     text ("W:\itunes\...", "MPEG audio file") and at24==1 blocks hold
//     UTF-16LE (curly quotes as `19 20`). The inverted table made every
//     decoded golden string mojibake (location-form fired on known-good
//     libraries) and made our writer stamp at24 values iTunes interprets as
//     the OTHER charset — real write-path corruption on rewritten strings.
//
//  3. The OLD code, when it fell back to UTF-16, wrote UTF-16 BIG-endian. The
//     corpus shows iTunes' UTF-16 blocks (at24==1) are LITTLE-endian. Writing
//     the correct Unicode string with the wrong byte order is itself corruption
//     (SPEC §1b rule 4). encodeMhohITunes emits UTF-16LE.
//
// encodeMhohITunes is the single iTunes-conformant encoder. It is driven by the
// corpus table and ERRORS (never guesses) for any hohmType absent from the
// table — callers must then preserve the original block unmodified and WARN,
// rather than invent a header iTunes never produces.

package itunes

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/text/encoding/charmap"
)

// mhohFixedHeaderTotal is the fixed-header span (offset of the string payload)
// of every standard-layout LE mhoh block: 40 bytes. headerLen (the +4 field) is
// 24, but the string data begins at +40 (+28 strLen, +32..+39 reserved zero).
const mhohFixedHeaderTotal = 40

// at24 encoding-indicator values, named per the corpus table (mhoh_encoding_table.go).
// These are the LITTLE-ENDIAN u32 written at byte offset +24 of an mhoh block.
const (
	at24ASCII   uint32 = 0 // ASCII / percent-encoded (0x0B LocalURL, advisory strings)
	at24UTF16LE uint32 = 1 // UTF-16 LITTLE-endian (non-Latin text) — K16-corrected
	at24UTF8    uint32 = 2 // UTF-8 / pure ASCII (0x0B encoded URLs)
	at24Latin1  uint32 = 3 // Windows-1252 / Latin-1 (Latin text) — K16-corrected
)

// decodeMhohUTF16LE decodes UTF-16 LITTLE-endian bytes (at24==1) into a string,
// handling surrogate pairs and embedded NULs. The inverse of encodeUTF16LE.
// (Distinct from smart_criteria_reader.go's decodeUTF16LE, which stops at the
// first NUL and ignores surrogates — wrong for full mhoh string payloads.)
func decodeMhohUTF16LE(data []byte) string {
	if len(data)%2 != 0 {
		data = append(append([]byte{}, data...), 0)
	}
	u16 := make([]uint16, len(data)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(data[i*2 : i*2+2])
	}
	runes := make([]rune, 0, len(u16))
	for i := 0; i < len(u16); i++ {
		c := u16[i]
		if c >= 0xD800 && c <= 0xDBFF && i+1 < len(u16) {
			lo := u16[i+1]
			if lo >= 0xDC00 && lo <= 0xDFFF {
				r := (rune(c-0xD800) << 10) + rune(lo-0xDC00) + 0x10000
				runes = append(runes, r)
				i++
				continue
			}
		}
		runes = append(runes, rune(c))
	}
	return string(runes)
}

// decodeMhohBlock decodes the string carried by a 40+-byte LE mhoh block using
// the DUAL convention (TASK-005):
//
//   - byte +27 != 0  → LEGACY block written by our OLD encoder. Decode via the
//     legacy flag at +27 (decodeHohmString: 1=UTF-16BE, 2=UTF-8, 3=Win-1252,
//     0=ASCII). This keeps libraries we previously wrote parseable.
//   - byte +27 == 0  → iTunes-conformant block. Read the +24 u32 indicator and
//     decode per the corpus semantics (0/2=ASCII/UTF-8, 1=UTF-16LE, 3=Latin-1;
//     K16-corrected 2026-07-03 — the original assignment had 1 and 3 swapped).
//
// The dual path is required because the two conventions assign DIFFERENT
// meanings to the same numeric value (legacy 3 = Win-1252; corpus 3 = UTF-16LE),
// so the +27==0 discriminator must select the table before interpreting +24.
func decodeMhohBlock(block []byte) (string, error) {
	if len(block) < mhohFixedHeaderTotal {
		return "", fmt.Errorf("decodeMhohBlock: block too short (%d < 40)", len(block))
	}
	strLen := int(binary.LittleEndian.Uint32(block[28:32]))
	strStart := mhohFixedHeaderTotal
	if strStart+strLen > len(block) {
		strLen = len(block) - strStart
		if strLen < 0 {
			return "", fmt.Errorf("decodeMhohBlock: negative string length")
		}
	}
	strData := block[strStart : strStart+strLen]

	legacyFlag := block[27]
	if legacyFlag != 0 {
		// Legacy block: +27 carries the encoding (our old convention).
		return decodeHohmString(strData, legacyFlag)
	}

	// iTunes-conformant block: encoding indicator at +24.
	at24 := binary.LittleEndian.Uint32(block[24:28])
	switch at24 {
	case at24UTF16LE:
		return decodeMhohUTF16LE(strData), nil
	case at24Latin1:
		dec := charmap.Windows1252.NewDecoder()
		out, err := dec.Bytes(strData)
		if err != nil {
			return string(strData), err
		}
		return string(out), nil
	case at24ASCII, at24UTF8:
		return string(strData), nil
	default:
		// Unknown indicator with +27==0: best-effort raw bytes (validated
		// separately by the mhoh-format guard, which rejects out-of-corpus +24).
		return string(strData), nil
	}
}
