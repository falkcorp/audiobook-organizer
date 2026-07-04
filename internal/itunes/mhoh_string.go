// file: internal/itunes/mhoh_string.go
// version: 2.0.0
// guid: 6f3b9d12-4a87-4c0e-9b21-7e5d2a8c1f04
// last-edited: 2026-07-03

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
	"errors"
	"fmt"

	"golang.org/x/text/encoding/charmap"
)

// ErrBEWritebackUnsupported is returned by every writeback entry point when the
// target library is big-endian (BE / PowerPC-era format).
//
// WHY refuse (K12 / LOW-2 / SPEC §3 step 1): all corpus forensics and the
// iTunes-conformant encoder are LITTLE-endian (production is iTunes v12.13 LE).
// The BE writer shared the same CRIT-1 +27 flag invention and has no corpus to
// validate against, so writing BE risks the exact corruption class T005 fixes
// with zero coverage. Production never produces BE libraries, so refusing is
// strictly safer than replicating an unvalidated BE encoder.
var ErrBEWritebackUnsupported = errors.New("iTunes BE writeback unsupported: only little-endian (v10+) libraries can be safely written (K12)")

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

// MhohHeaderBytes carries the deterministic per-block header values the LE
// writers stamp. It exists so both writer paths (buildMhohLE append path and
// rewriteHohmLocationLE replace path) build the SAME 40-byte header from the
// SAME inputs — guaranteeing byte-identical output for identical input
// (TASK-005 acceptance criterion).
type MhohHeaderBytes struct {
	HeaderLen uint32 // always mhohFixedHeaderLen (24)
	At24      uint32 // encoding indicator at byte +24
	StrLen    uint32 // length of the encoded payload (bytes)
	TotalLen  uint32 // 40 + StrLen
}

// encodeMhohITunes encodes s for an mhoh block of hohmType, choosing an encoding
// that is byte-conformant with iTunes-authored libraries for that type.
//
// Returns the encoded string payload, the deterministic header bytes, and an
// error. The error is non-nil (and payload/hdr zero) when hohmType is absent
// from the corpus table — callers MUST preserve the original block and WARN
// rather than write an invented encoding (SPEC §5 ITW-2: "never invent flags").
//
// Encoding choice (corpus-driven, per ITunesMhohEncoding[hohmType].AllowedAt24):
//   - {0} or {2}: ASCII/percent-encoded field (e.g. 0x0B LocalURL). Encoded as
//     raw bytes; at24 = the single allowed value (2 preferred over 0 when both
//     are allowed, matching iTunes' dominant rendering for encoded URLs).
//   - {3} only: Windows-1252 always (e.g. 0x06 Kind — iTunes encodes Kind as
//     single-byte text uniformly; the strings come from an internal enum).
//   - contains both 1 and 3: latin1 (3) when every rune <= 0xFF, else
//     UTF-16LE (1) — matching the golden distribution (Latin-representable
//     names/locations dominate at 3; curly-quote/CJK strings carry 1).
//   - {1} only: UTF-16LE always.
func encodeMhohITunes(hohmType uint32, s string) ([]byte, MhohHeaderBytes, error) {
	entry, ok := ITunesMhohEncoding[hohmType]
	if !ok {
		return nil, MhohHeaderBytes{}, fmt.Errorf(
			"encodeMhohITunes: hohmType 0x%X absent from corpus table; refusing to invent an encoding (CRIT-1)", hohmType)
	}

	at24 := chooseAt24(entry, s)

	var payload []byte
	switch at24 {
	case at24UTF16LE:
		payload = encodeUTF16LE(s)
	case at24ASCII, at24UTF8:
		// ASCII / percent-encoded URL fields: raw bytes. These are pure ASCII
		// in the corpus (percent-escaped URLs, advisory codes), so byte-identity
		// holds without transcoding.
		payload = []byte(s)
	case at24Latin1:
		enc := charmap.Windows1252.NewEncoder()
		out, err := enc.Bytes([]byte(s))
		if err != nil {
			// A {1,3} type would have selected UTF-16LE above; reaching here
			// means a {1}-only type received a non-Latin rune (not seen in the
			// corpus). Fail rather than silently corrupt.
			return nil, MhohHeaderBytes{}, fmt.Errorf(
				"encodeMhohITunes: type 0x%X (latin1-only) cannot encode %q: %w", hohmType, s, err)
		}
		payload = out
	default:
		return nil, MhohHeaderBytes{}, fmt.Errorf("encodeMhohITunes: unsupported at24 indicator %d for type 0x%X", at24, hohmType)
	}

	hdr := MhohHeaderBytes{
		HeaderLen: mhohFixedHeaderLen,
		At24:      at24,
		StrLen:    uint32(len(payload)),
		TotalLen:  uint32(mhohFixedHeaderTotal + len(payload)),
	}
	return payload, hdr, nil
}

// chooseAt24 picks the corpus-allowed encoding indicator for s given the type's
// AllowedAt24 set. WHY the priority order: it mirrors what iTunes itself writes —
// UTF-16-only types always 1; URL/ASCII types take their single allowed code;
// {1,3} text types use latin1 (3) for Latin-representable strings and
// UTF-16LE (1) otherwise.
func chooseAt24(entry MhohEncodingEntry, s string) uint32 {
	allows := func(v uint32) bool { return entry.AllowedAt24Contains(v) }

	// UTF-16LE-only types: always 1.
	if allows(at24UTF16LE) && !allows(at24Latin1) && !allows(at24ASCII) && !allows(at24UTF8) {
		return at24UTF16LE
	}

	// ASCII / percent-encoded URL types (e.g. 0x0B): prefer 2 (UTF-8/ASCII) over
	// 0 when both are allowed — iTunes' dominant rendering for encoded URLs.
	if (allows(at24ASCII) || allows(at24UTF8)) && !allows(at24Latin1) {
		if allows(at24UTF8) {
			return at24UTF8
		}
		return at24ASCII
	}

	// Text types allowing latin1: latin1 when Latin-representable, else UTF-16LE.
	if allows(at24Latin1) {
		if isLatin1Representable(s) && allows(at24Latin1) {
			return at24Latin1
		}
		if allows(at24UTF16LE) {
			return at24UTF16LE
		}
		return at24Latin1
	}

	// Fallback: UTF-16LE if allowed, else the first allowed value.
	if allows(at24UTF16LE) {
		return at24UTF16LE
	}
	if len(entry.AllowedAt24) > 0 {
		return entry.AllowedAt24[0]
	}
	return at24Latin1
}

// isLatin1Representable reports whether every rune of s is <= 0xFF (encodable in
// a single Windows-1252/Latin-1 byte). Mirrors how iTunes chooses latin1 vs
// UTF-16LE for {1,3} text fields in the corpus.
func isLatin1Representable(s string) bool {
	for _, r := range s {
		if r > 0xFF {
			return false
		}
	}
	return true
}

// encodeUTF16LE encodes s as UTF-16 LITTLE-endian (at24==1). WHY little-endian:
// byte-level probes show iTunes' UTF-16 blocks are LE; our OLD encoder wrote
// big-endian, which was part of the CRIT-1 corruption (SPEC §1b rule 4).
// Astral-plane runes (> U+FFFF) are emitted as surrogate pairs.
func encodeUTF16LE(s string) []byte {
	var buf []byte
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			hi := 0xD800 + (r >> 10)
			lo := 0xDC00 + (r & 0x3FF)
			var b [4]byte
			binary.LittleEndian.PutUint16(b[0:2], uint16(hi))
			binary.LittleEndian.PutUint16(b[2:4], uint16(lo))
			buf = append(buf, b[:]...)
			continue
		}
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(r))
		buf = append(buf, b[:]...)
	}
	return buf
}

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
