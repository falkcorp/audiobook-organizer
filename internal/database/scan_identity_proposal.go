// file: internal/database/scan_identity_proposal.go
// version: 1.0.0
// guid: 9b0c2ea4-f50d-4a80-9711-1408178a211c
// last-edited: 2026-10-06

package database

import (
	"maps"
	"strings"
	"time"
)

// ScanIdentityProposalPrefix keys the library scan's identity proposals in
// the raw KV keyspace: one key per book, ScanIdentityProposalPrefix + book id.
//
// A rescan never rewrites an EXISTING book's title, author, series or series
// position (scanner holdIdentityForExisting, 2026-10-06): the nightly scan of
// 2026-10-06 did, from tags and file names, on 1,176 books, and each title or
// author change dropped the book's cached candidates. What the file now says
// is kept here instead, for the reviewed fixer
// maintenance.scan-proposed-identity, which lists stored -> scanned per field
// and writes only the rows the owner approves.
const ScanIdentityProposalPrefix = "scan_identity_proposal:"

// Scan identity proposal field names.
const (
	ScanProposalTitle          = "title"
	ScanProposalAuthor         = "author"
	ScanProposalSeries         = "series"
	ScanProposalSeriesPosition = "series_position"
)

// ScanIdentityProposalKey is the raw KV key of bookID's proposal.
func ScanIdentityProposalKey(bookID string) string { return ScanIdentityProposalPrefix + bookID }

// ScanIdentityChange is one held field: what the row holds (From) and what
// the scan read off the file (To).
type ScanIdentityChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ScanIdentityProposal is what the last scan of a book read that differs
// from the row's held identity.
type ScanIdentityProposal struct {
	BookID     string                        `json:"book_id"`
	FilePath   string                        `json:"file_path"`
	ObservedAt time.Time                     `json:"observed_at"`
	Fields     map[string]ScanIdentityChange `json:"fields"`
}

// SameFields reports whether p proposes exactly fields (so a rescan that
// reads the same thing again does not rewrite the key).
func (p *ScanIdentityProposal) SameFields(fields map[string]ScanIdentityChange) bool {
	if p == nil {
		return len(fields) == 0
	}
	return maps.Equal(p.Fields, fields)
}

// SameIdentityText compares two identity values the way the scan hold and its
// fixer do: case and surrounding space are ignored.
func SameIdentityText(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
