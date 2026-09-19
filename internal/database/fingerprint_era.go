// file: internal/database/fingerprint_era.go
// version: 1.0.0
// guid: 7b2e9f40-1c63-4d85-9e1a-5f0c3b8d6a27
// last-edited: 2026-09-19

package database

import "github.com/falkcorp/audiobook-organizer/internal/fingerprint"

// HasCurrentPrint reports whether f's whole-file print (and the Seg0 derived
// from it) was written under the current fingerprint encoding. A legacy row
// is MISSING evidence for every fuzzy comparison: skip the signal, never
// score it as a mismatch. Exact matches on the stored segment string are
// era-independent and do not need this check.
func (f *BookFile) HasCurrentPrint() bool {
	return f != nil && f.AcoustIDFPVersion >= fingerprint.PrintEncodingVersion
}

// HasCurrentPrint is BookFile.HasCurrentPrint for the slim Core projection.
func (c *BookFileCore) HasCurrentPrint() bool {
	return c != nil && c.AcoustIDFPVersion >= fingerprint.PrintEncodingVersion
}

// HasCurrentBookSig reports whether b carries a non-empty book signature
// synthesized under the current BookSignatureVersion. Legacy signatures are
// missing evidence: no veto, no candidate, no score.
func (b *Book) HasCurrentBookSig() bool {
	return b != nil && b.BookSigV1 != nil && *b.BookSigV1 != "" &&
		b.BookSigVersion != nil && *b.BookSigVersion >= fingerprint.BookSignatureVersion
}
