// file: internal/itunes/mhoh_encoding_table.go
// version: 2.0.0
// guid: a0dacfc4-01c3-4a83-9404-b510ca4d051a
// last-edited: 2026-07-03

// ITunesMhohEncoding is the authoritative per-hohmType encoding constant table
// for mhoh string blocks in iTunes-authored .itl files.
//
// Provenance: derived by running cmd/itl-audit-encoding against the golden
// iTunes library (path "/tmp/itunes-libraries/iTunes Library.itl",
// version 12.13.10.3, 94,575 tracks, 965,223 mhoh blocks audited total)
// on 2026-06-09. Container types 1 (tracks), 2 (playlists), 9 (albums), 11
// (artists) were walked. The walker descends into mith/miah/miph/miih children.
//
// Shape: map[hohmType uint32] → MhohEncodingEntry. Downstream guard
// "mhoh-format" (T003 / ITLSafetyContract) consumes this map as follows:
//
//	entry, ok := ITunesMhohEncoding[hohmType]
//	if !ok {
//	    // hohmType not in corpus — skip +24 check, but still enforce
//	    // +27==0 and headerLen==24 for all blocks.
//	    continue
//	}
//	if !entry.AllowedAt24Contains(at24) {
//	    // violation: emit a mhoh-format guard violation
//	}
//
// WHY a map (not a slice): O(1) lookup per block during contract runs over
// libraries with 965K+ blocks.
//
// WHY AllowedAt24 is a []uint32 (not a set): the corpus shows at most 3
// distinct values per type; a linear scan over 3 elements is cheaper than
// map allocation for per-block hot-path guard checks.
//
// Global invariants confirmed across all 965,223 observed blocks:
//   - headerLen == 24: 100% uniform across all types.
//   - byte +27 == 0x00: 100% uniform for all text string types (types where
//     at24 ∈ {0, 1, 2, 3}); non-zero only in binary-blob types (0x36, 0x65,
//     0x66, etc.) which store non-string payloads.
//   - bytes +32..+39 all zero: true for all text string types.
//
// Encoding indicator values at byte +24 (not +27 as our old code assumed):
//   0 = ASCII/percent-encoded (used exclusively by 0x0B LocalURL and similar)
//   1 = UTF-16LE (used for non-Latin text with characters > U+00FF)
//   2 = UTF-8 / pure ASCII (used by 0x0B LocalURL for encoded URLs)
//   3 = Windows-1252 / Latin-1 (used for Latin text)
//
// K16 CORRECTION (2026-07-03): the 2026-06-09 derivation recorded the VALUE
// SETS correctly but assigned the 1/3 semantics backwards. Byte-level probes
// of golden blocks (see mhoh_string.go and SPEC 3 §6) prove at24==3 carries
// single-byte text and at24==1 carries UTF-16LE. The AllowedAt24 sets below
// are unchanged (they are numeric observations); only per-type semantic
// commentary has been corrected.
//
// Note: this table covers ALL 44 text string hohmTypes found in the corpus.
// The guard-critical types (used by our writers) are 0x02, 0x03, 0x04, 0x05,
// 0x06, 0x0B, 0x0D — see CRIT-1 and SPEC 2 §2.
package itunes

// MhohEncodingEntry holds the corpus-derived constraints for one hohmType.
type MhohEncodingEntry struct {
	// HeaderLen is the exclusively observed headerLen value. In every
	// iTunes-authored library inspected this is 24. The guard rejects any
	// block where headerLen differs.
	HeaderLen uint32

	// AllowedAt24 is the set of u32 values observed at byte offset +24 (the
	// encoding indicator field) in the corpus. The guard rejects any block
	// whose +24 value is not in this set.
	//
	// Values seen: 0=ASCII/percent-encoded, 1=UTF-16LE, 2=UTF-8, 3=Windows-1252.
	AllowedAt24 []uint32

	// At27Uniform records whether byte +27 was exclusively 0x00 in the corpus.
	// For every text string type in the table this is true — it is recorded here
	// so the guard can distinguish "corpus confirms: must be 0" from "no data".
	At27Uniform bool

	// Count is the number of blocks of this type observed in the golden corpus.
	// Informational; not used by the guard at runtime.
	Count int
}

// AllowedAt24Contains reports whether v is in e.AllowedAt24.
func (e MhohEncodingEntry) AllowedAt24Contains(v uint32) bool {
	for _, a := range e.AllowedAt24 {
		if a == v {
			return true
		}
	}
	return false
}

// ITunesMhohEncoding is the corpus-derived constant table for text string
// hohmTypes. Binary-blob types (0x36=raw audio data, 0x65=SmartCriteria,
// 0x66=SmartInfo, etc.) are intentionally excluded — their payload bytes
// are NOT string encoding headers, so applying the +24/+27 guard to them
// would produce false positives.
//
// Generated from golden library version 12.13.10.3 on 2026-06-09 by
// cmd/itl-audit-encoding. Re-run the tool to regenerate from a new corpus.
var ITunesMhohEncoding = map[uint32]MhohEncodingEntry{

	// --------------- Core track metadata (written by our writers) ---------------

	// 0x02 = Name (track title).
	// Corpus: 94,575 blocks. at24 ∈ {1=utf16le, 3=latin1}. +27=0 uniform.
	// The minority value 1 (utf16le) appears for titles with non-Latin chars.
	0x02: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 94575},

	// 0x03 = Album.
	// Corpus: 94,174 blocks. at24 ∈ {1, 3}. +27=0 uniform.
	0x03: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 94174},

	// 0x04 = Artist.
	// Corpus: 94,339 blocks. at24 ∈ {1, 3}. +27=0 uniform.
	0x04: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 94339},

	// 0x05 = Genre.
	// Corpus: 78,495 blocks (not all tracks have genre). at24 ∈ {1, 3}. +27=0.
	0x05: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 78495},

	// 0x06 = Kind (file format description, e.g. "AAC audio file").
	// Corpus: 93,539 blocks. ONLY at24=3 observed — iTunes encodes "Kind"
	// as single-byte Windows-1252 exclusively (K16-corrected; the strings are
	// pure-ASCII values from an internal enum). +27=0 uniform.
	0x06: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 93539},

	// 0x0B = LocalURL (file://localhost/... percent-encoded URL, or https:// for podcasts).
	// Corpus: 94,201 blocks (93,014 local + 1,187 podcast).
	// IMPORTANT: at24 ∈ {0, 2} — NOT {1, 3}. URLs are pure ASCII (percent-
	// escaped), so iTunes never needs Windows-1252 or UTF-16LE for this field.
	// at24=0: appears to be the "plain ASCII" marker (similar to at24=2).
	// at24=2: appears for the same ASCII strings (iTunes may use 0 or 2
	// interchangeably for ASCII-only strings in this field).
	// +27=0 uniform; tail_zero=true.
	0x0B: {HeaderLen: 24, AllowedAt24: []uint32{0, 2}, At27Uniform: true, Count: 94201},

	// 0x0D = Location (native Windows path, e.g. W:\itunes\...).
	// Corpus: 93,014 blocks (1,736 UTF-16LE at at24=1 for non-ASCII paths;
	// 91,278 Latin-1/Windows-1252 at at24=3).
	// at24 ∈ {1, 3}. +27=0 uniform; tail_zero=true.
	0x0D: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 93014},

	// --------------- Additional track metadata ---------------

	// 0x08 = Comment.
	// Corpus: 47,225 blocks. at24 ∈ {1, 3}. +27=0.
	0x08: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 47225},

	// 0x09 = Podcast episode description or similar (rare).
	// Corpus: 159 blocks. Only at24=3.
	0x09: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 159},

	// 0x0C = Composer / Narrator (audiobook narrator stored here).
	// Corpus: 32,289 blocks. at24 ∈ {1, 3}.
	0x0C: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 32289},

	// 0x0E = Series / Work name (rare).
	// Corpus: 1,915 blocks. Only at24=3 (Windows-1252, K16-corrected).
	0x0E: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 1915},

	// 0x12 = Sort Artist.
	// Corpus: 12,772 blocks. at24 ∈ {1, 3}.
	0x12: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 12772},

	// 0x15 = Content rating / advisory string.
	// Corpus: 9,874 blocks. Only at24=0. +27=0 uniform. Note: len arithmetic
	// does not hold for this type (tail_zero=false) — it uses a non-standard
	// length field layout, but the encoding indicator itself is text-style.
	0x15: {HeaderLen: 24, AllowedAt24: []uint32{0}, At27Uniform: true, Count: 9874},

	// 0x16 = Album Artist (or similar).
	// Corpus: 1,712 blocks. at24 ∈ {1, 3}.
	0x16: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 1712},

	// 0x18 = Sort Album Artist.
	// Corpus: 47 blocks. Only at24=3.
	0x18: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 47},

	// 0x19 = Sort Composer.
	// Corpus: 46 blocks. Only at24=3.
	0x19: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 46},

	// 0x1B = Sort Name.
	// Corpus: 36,294 blocks. at24 ∈ {1, 3}.
	0x1B: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 36294},

	// 0x1C = Sort Album.
	// Corpus: 65 blocks. Only at24=3.
	0x1C: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 65},

	// 0x1D = Purchase-related URL or similar.
	// Corpus: 29 blocks. Only at24=2.
	0x1D: {HeaderLen: 24, AllowedAt24: []uint32{2}, At27Uniform: true, Count: 29},

	// 0x1E = Content Advisory Rating.
	// Corpus: 14,444 blocks. at24 ∈ {1, 3}.
	0x1E: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 14444},

	// 0x1F = Content Description / Advisory.
	// Corpus: 23,056 blocks. at24 ∈ {1, 3}.
	0x1F: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 23056},

	// 0x20 = Podcast feed URL.
	// Corpus: 696 blocks. Only at24=3.
	0x20: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 696},

	// 0x21 = Podcast episode URL.
	// Corpus: 252 blocks. Only at24=3.
	0x21: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 252},

	// 0x22 = Podcast title / channel.
	// Corpus: 95 blocks. Only at24=3.
	0x22: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 95},

	// 0x23 = Podcast category (very rare in this corpus).
	// Corpus: 1 block. Only at24=3.
	0x23: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 1},

	// 0x25 = Vendor identifier or similar.
	// Corpus: 1,236 blocks. Only at24=0.
	0x25: {HeaderLen: 24, AllowedAt24: []uint32{0}, At27Uniform: true, Count: 1236},

	// 0x2B = iTunes Store URL (rare).
	// Corpus: 37 blocks. Only at24=3.
	0x2B: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 37},

	// 0x2E = Additional metadata string (possibly "Work" or "Movement").
	// Corpus: 6,264 blocks. at24 ∈ {1, 3}.
	0x2E: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 6264},

	// 0x33 = (Identity string of some kind).
	// Corpus: 47 blocks. Only at24=1.
	0x33: {HeaderLen: 24, AllowedAt24: []uint32{1}, At27Uniform: true, Count: 47},

	// 0x39 = Apple Music ID or content identifier.
	// Corpus: 1,200 blocks. Only at24=0.
	0x39: {HeaderLen: 24, AllowedAt24: []uint32{0}, At27Uniform: true, Count: 1200},

	// 0x3A = Related string (very rare).
	// Corpus: 6 blocks. Only at24=0.
	0x3A: {HeaderLen: 24, AllowedAt24: []uint32{0}, At27Uniform: true, Count: 6},

	// 0x3F = Playlist sort field / description.
	// Corpus: 1,826 blocks. Only at24=3.
	0x3F: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 1826},

	// 0x40 = Related string field.
	// Corpus: 881 blocks. Only at24=3.
	0x40: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 881},

	// 0x64 = Playlist name (miph containers).
	// Corpus: 338 blocks. at24 ∈ {1, 3}. +27=0.
	0x64: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 338},

	// 0x69 = Playlist string field (rare, in miph containers).
	// Corpus: 678 blocks. Only at24=0. +27=0 uniform. Non-standard length layout
	// (len arithmetic does not hold), but encoding indicator is text-style.
	0x69: {HeaderLen: 24, AllowedAt24: []uint32{0}, At27Uniform: true, Count: 678},

	// 0x6C = Playlist string field (rare, in miph containers).
	// Corpus: 338 blocks. at24 ∈ {0, 3}. +27=0 uniform. Non-standard length layout.
	0x6C: {HeaderLen: 24, AllowedAt24: []uint32{0, 3}, At27Uniform: true, Count: 338},

	// 0xC8 = Container-level name (very rare, in album/artist containers).
	// Corpus: 6 blocks. at24 ∈ {1, 3}.
	0xC8: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 6},

	// --------------- Album/Artist container types (msdh 9 and 11) ---------------

	// 0x12C = Album name (miah containers, msdh type 9).
	// Corpus: 12,428 blocks. at24 ∈ {1, 3}.
	0x12C: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 12428},

	// 0x12D = Album artist name (miah containers).
	// Corpus: 12,444 blocks. at24 ∈ {1, 3}.
	0x12D: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 12444},

	// 0x12E = Album sort field.
	// Corpus: 6,197 blocks. at24 ∈ {1, 3}.
	0x12E: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 6197},

	// 0x130 = Album genre (rare).
	// Corpus: 17 blocks. Only at24=3.
	0x130: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 17},

	// 0x131 = Album container string (very rare).
	// Corpus: 6 blocks. Only at24=0.
	0x131: {HeaderLen: 24, AllowedAt24: []uint32{0}, At27Uniform: true, Count: 6},

	// 0x190 = Artist name (miih containers, msdh type 11).
	// Corpus: 3,555 blocks. at24 ∈ {1, 3}.
	0x190: {HeaderLen: 24, AllowedAt24: []uint32{1, 3}, At27Uniform: true, Count: 3555},

	// 0x191 = Artist sort field (miih containers).
	// Corpus: 91 blocks. Only at24=3.
	0x191: {HeaderLen: 24, AllowedAt24: []uint32{3}, At27Uniform: true, Count: 91},
}
