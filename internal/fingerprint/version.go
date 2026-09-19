// file: internal/fingerprint/version.go
// version: 1.0.0
// guid: 3a7c1e52-6d94-4b08-a2f3-8e5b0c9d4f61
// last-edited: 2026-09-19

package fingerprint

// PrintEncodingVersion is stamped on a BookFile (AcoustIDFPVersion) when the
// acoustid backfill writes its whole-file print. Version 1 = frames decoded
// with decompressChromaprint (2026-09-19). Rows at 0 were written while
// fpcalc's compressed output was misread as frames: their bytes are not
// frames and must never be compared with anything (treat as missing).
const PrintEncodingVersion = 1

// BookSignatureVersion is stamped on a Book (BookSigVersion) when its
// book_sig_v1 is synthesized. Version 1 = built only from current-era file
// prints. Legacy signatures (nil/0) are misdecoded data and must be treated
// as missing evidence, never as a mismatch.
const BookSignatureVersion = 1
