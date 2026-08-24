// file: internal/database/scan_state.go
// version: 1.0.0
// guid: 4a846cd3-3757-482a-b4df-71e02cb47292
// last-edited: 2026-08-23

package database

// ScanState is everything the staged library scan knows about one file's
// completeness. See docs/design/2026-08-23-staged-library-scan-design.md.
//
// Grouped into one struct rather than spread across BookFile as five loose
// fields: it is one coherent concept, and BookFile is constructed at many call
// sites, where five more zero-valued fields would be five more things to get
// wrong.
//
// EVERY field here, and the field on BookFile itself, is tagged `omitzero` and
// NOT `omitempty`. This is load-bearing, and it is about the v1 -> v2 migration
// this repo is part-way through (GOEXPERIMENT=jsonv2; 17 files already import
// encoding/json/v2, internal/database is still on v1).
//
// Measured on this module, both marshalers, 2026-08-23:
//
//	struct field   tag          v1 output          v2 output
//	bool false     omitempty    omitted            "bo":false
//	int 0          omitempty    omitted            "in":0
//	empty struct   omitempty    "scan":{}          "scan":{"a":false}
//	anything zero  omitzero     omitted            omitted
//
// v1's `omitempty` means "the Go value is a zero value"; v2's means "it encodes
// to an EMPTY JSON value", and false/0 are not empty JSON values. So a struct
// tagged `omitempty` silently changes shape the day its package moves to v2.
// `omitzero` means the same thing in both, so these rows serialize identically
// before and after that migration -- which matters here because a book_file row
// written by v1 today is read back after the store migrates.
//
// A value type rather than a pointer keeps every reader free of a nil check.
type ScanState struct {
	// NeedsDeep marks a row whose tags have been read but whose content has
	// not: FileHash, chapters and fingerprints are absent or stale until the
	// deep pass clears this.
	//
	// This is the field the dedup-sensitive read paths filter on. Do NOT
	// substitute "FileHash == \"\"" for it: an empty hash is ambiguous between
	// "not attempted yet" and "attempted and failed", and the second must stay
	// visible instead of being retried forever.
	NeedsDeep bool `json:"needs_deep,omitzero"`
	// HashStale means FileHash predates the current bytes -- the file changed
	// and the deep pass has not caught up. The old hash is deliberately KEPT so
	// a failed deep pass does not leave the row with nothing to match on.
	HashStale bool `json:"hash_stale,omitzero"`
	// HeaderBad means the tag header could not be parsed, so Title is derived
	// from the path. Flagged so it stays distinguishable from a curated title:
	// an unflagged path-derived title is exactly how junk-title debt accrues.
	HeaderBad bool `json:"header_bad,omitzero"`
	// Attempts counts deep-pass tries. At DeepScanMaxAttempts the row stops
	// retrying, is marked failed and is surfaced to the operator. Silence is
	// the failure mode this exists to avoid.
	Attempts int `json:"attempts,omitzero"`
	// LastError is why the most recent deep-pass attempt failed.
	LastError string `json:"last_error,omitzero"`
}

// DeepScanMaxAttempts is the retry ceiling for the deep pass. A row that has
// failed this many times stops being retried and is surfaced instead.
const DeepScanMaxAttempts = 3

// IsProvisional reports whether this file has been ingested but not yet
// content-scanned, so anything keyed on file content -- hash, chapters,
// fingerprint -- is absent or stale.
//
// This is the ONE question the dedup-sensitive and bulk-write paths should ask.
// It exists so those paths cannot drift apart the way the three OverrideLocked
// guards did: a provisional row must be excluded from dedup candidate sets and
// from bulk apply / bulk merge / organize, because a merge decided without a
// hash rests on title and author similarity alone, and this repo has already
// measured that dedup collisions are frequently genuine duplicate books rather
// than false pairs.
//
// It deliberately does NOT restrict browsing, playing, or a single deliberate
// manual edit: a user acting on one book can see what they are doing.
func (f BookFile) IsProvisional() bool {
	return f.Scan.NeedsDeep
}

// DeepScanExhausted reports whether this file has used up its deep-pass retries
// and should be surfaced rather than retried again.
func (f BookFile) DeepScanExhausted() bool {
	return f.Scan.Attempts >= DeepScanMaxAttempts
}

// The same two questions on BookFileCore, the lightweight projection served by
// the memdb read path. They exist because callers holding a Core must be able to
// ask without converting back to a BookFile -- and because a caller who CANNOT
// ask is a caller who silently skips the check.
//
// TestScanPredicatesAgreeAcrossBookFileAndCore pins the pair. This repo has
// already been bitten by two spellings of one predicate drifting apart (the
// three OverrideLocked guards, #2817), so the agreement is asserted rather than
// assumed.

// IsProvisional reports whether this file has been ingested but not yet
// content-scanned. See BookFile.IsProvisional.
func (f BookFileCore) IsProvisional() bool {
	return f.Scan.NeedsDeep
}

// DeepScanExhausted reports whether this file has used up its deep-pass retries.
// See BookFile.DeepScanExhausted.
func (f BookFileCore) DeepScanExhausted() bool {
	return f.Scan.Attempts >= DeepScanMaxAttempts
}
