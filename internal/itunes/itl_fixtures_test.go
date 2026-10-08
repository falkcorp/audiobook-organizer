// file: internal/itunes/itl_fixtures_test.go
// version: 1.0.0
// guid: 8b1e4c7d-2f95-4a36-b0d8-6c3e9f1a5d27
// last-edited: 2026-10-07
//
// Synthetic ITL fixture builders for the read-side tests. They lived in the
// removed writer files (iTunes is import-only since 2026-10-07); the parser,
// contract-audit and mhoh-decode tests still build their inputs with them.

package itunes

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"golang.org/x/text/encoding/charmap"
)

// buildMhohLE builds an LE metadata chunk (mhoh) for a given type and string,
// emitting an iTunes-conformant header via encodeMhohITunes (TASK-005, CRIT-1).
//
// The full 40-byte header is set DETERMINISTICALLY from MhohHeaderBytes: byte
// +27 is left 0x00 (iTunes never writes a non-zero +27 — K3), the +24 u32 carries
// the corpus encoding indicator, and bytes +32..+39 stay zero. This is the
// "append" writer path; rewriteHohmLocationLE is the "replace" path — both build
// the SAME header from the SAME inputs so their output is byte-identical for
// identical input.
//
// Returns (nil, false) when the type is absent from the corpus table — the
// caller must then preserve the original block unmodified and WARN, rather than
// write an invented encoding (SPEC §5 ITW-2: "never invent flags").
func buildMhohLE(mhohType uint32, value string) ([]byte, bool) {
	payload, hdr, err := encodeMhohITunes(mhohType, value)
	if err != nil {
		return nil, false
	}
	buf := make([]byte, hdr.TotalLen)
	copy(buf[0:4], "mhoh")
	writeUint32LE(buf, 4, hdr.HeaderLen) // headerLen: fixed 24 (NOT totalLen — K5)
	writeUint32LE(buf, 8, hdr.TotalLen)  // totalLen: 40 + strLen
	writeUint32LE(buf, 12, mhohType)
	writeUint32LE(buf, 24, hdr.At24) // +24: corpus encoding indicator (K3)
	// byte +27 stays 0x00 (zero-initialized) — iTunes' invariant (K3).
	writeUint32LE(buf, 28, hdr.StrLen)
	// bytes +32..+39 stay zero (reserved tail).
	copy(buf[40:], payload)
	return buf, true
}

func writeUint32LE(buf []byte, offset int, val uint32) {
	buf[offset] = byte(val)
	buf[offset+1] = byte(val >> 8)
	buf[offset+2] = byte(val >> 16)
	buf[offset+3] = byte(val >> 24)
}

// pidToHexLE converts a PID stored in little-endian byte order (v10+ ITL)
// to the same hex string format used in the XML (big-endian / MSB first).
func pidToHexLE(pid [8]byte) string {
	reversed := [8]byte{pid[7], pid[6], pid[5], pid[4], pid[3], pid[2], pid[1], pid[0]}
	return hex.EncodeToString(reversed[:])
}

func itlEncrypt(hdr *hdfmHeader, data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	block, err := aes.NewCipher(itlAESKey)
	if err != nil {
		return data
	}
	bs := block.BlockSize()

	limit := len(data)
	if isVersionAtLeast(hdr.version, 10) {
		if hdr.maxCryptSize > 0 {
			limit = int(hdr.maxCryptSize)
		} else if limit > 102400 {
			limit = 102400
		}
	}
	if limit > len(data) {
		limit = len(data)
	}
	limit = (limit / bs) * bs

	out := make([]byte, len(data))
	copy(out, data)

	for i := 0; i < limit; i += bs {
		block.Encrypt(out[i:i+bs], data[i:i+bs])
	}
	return out
}

func itlDeflate(data []byte) []byte {
	var buf bytes.Buffer
	// Use BestSpeed (level 1) to match iTunes' compression more closely.
	// iTunes appears to use a low compression level. Using Go's default (level 6)
	// produces smaller but different output that iTunes rejects.
	w, err := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
	if err != nil {
		// Fallback to default if level fails
		w = zlib.NewWriter(&buf)
	}
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}

// encodeHohmString encodes a string for writing into a hohm record.
// Returns (encoded bytes, encoding flag).
// If all runes <= 0xFF, uses Windows-1252 (flag 3). Otherwise UTF-16BE (flag 1).
func encodeHohmString(s string) ([]byte, byte) {
	allLatin := true
	for _, r := range s {
		if r > 0xFF {
			allLatin = false
			break
		}
	}

	if allLatin {
		enc := charmap.Windows1252.NewEncoder()
		out, err := enc.Bytes([]byte(s))
		if err != nil {
			// Fallback to UTF-16BE
			return encodeUTF16BE(s), 1
		}
		return out, 3
	}

	return encodeUTF16BE(s), 1
}

func encodeUTF16BE(s string) []byte {
	runes := []rune(s)
	buf := make([]byte, len(runes)*2)
	for i, r := range runes {
		binary.BigEndian.PutUint16(buf[i*2:i*2+2], uint16(r))
	}
	return buf
}

// buildHohmChunk builds a hohm chunk for a given type and string value.
func buildHohmChunk(hohmType uint32, value string) []byte {
	encodedStr, encFlag := encodeHohmString(value)
	chunkLen := 40 + len(encodedStr)
	buf := make([]byte, chunkLen)
	copy(buf[0:4], "hohm")
	writeUint32BE(buf, 4, uint32(chunkLen))
	writeUint32BE(buf, 8, uint32(chunkLen))
	writeUint32BE(buf, 12, hohmType)
	buf[16+11] = encFlag
	writeUint32BE(buf, 28, uint32(len(encodedStr)))
	// bytes 32-39 are zero (already)
	copy(buf[40:], encodedStr)
	return buf
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

// mhohFixedHeaderLen is the correct headerLen value for LE mhoh chunks.
// iTunes uses this fixed value to locate type-specific data within the chunk.
// Setting headerLen = totalLen (the full chunk size) corrupts the library —
// see regression test TestRewriteHohmLocationLE_PreservesHeaderLen.
const mhohFixedHeaderLen = 24

// ITLNewTrack describes a track to insert into an ITL file.
type ITLNewTrack struct {
	Location    string
	Name        string
	Album       string
	Artist      string
	Genre       string
	Kind        string // e.g. "MPEG audio file", "AAC audio file"
	Size        int
	TotalTime   int // milliseconds
	TrackNumber int
	DiscNumber  int
	Year        int
	BitRate     int
	SampleRate  int
}

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
