// file: internal/plugins/maintenance/dedupe_book_file_rows_test.go
// version: 1.0.0
// guid: 3b6d19a7-4e52-4c08-b7f1-90a5e2c4d738
// last-edited: 2026-08-03

package maintenance

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// 🔴 The keeper choice is the data-loss-critical part of this op. Every one of
// these asserts that the row carrying irreplaceable evidence SURVIVES, because
// the rows that lose are deleted outright.
//
// This repo has already shipped a bug that wiped AcoustIDFingerprint, and a
// fingerprint costs a full-file decode to regenerate. Deleting the wrong twin
// would be silent: the book would simply stop matching in dedup, with nothing
// reporting why.
func TestRankKeeper_PrefersTheFingerprintedRow(t *testing.T) {
	rows := []database.BookFile{
		{ID: "aaa-first-alphabetically", Duration: 600, FileHash: "h1"},
		{ID: "zzz-last-alphabetically", Duration: 600, FileHash: "h1",
			AcoustIDFingerprint: []byte{0x01, 0x02, 0x03}},
	}
	got := rankKeeper(rows)[0]
	if got.ID != "zzz-last-alphabetically" {
		t.Fatalf("keeper = %q, want the FINGERPRINTED row — a fingerprint costs a "+
			"full-file decode and cannot be guessed back", got.ID)
	}
}

// Duration is the field this cleanup exists to fix, so a row that has one beats
// a row that does not — but only when neither side has a fingerprint.
func TestRankKeeper_PrefersRowWithDuration(t *testing.T) {
	rows := []database.BookFile{
		{ID: "a", Duration: 0},
		{ID: "b", Duration: 3600},
	}
	if got := rankKeeper(rows)[0]; got.ID != "b" {
		t.Fatalf("keeper = %q, want the row with a duration", got.ID)
	}
}

// A fingerprint outranks a duration: losing a duration is recoverable by
// re-probing the file, losing a fingerprint is not cheap to undo.
func TestRankKeeper_FingerprintOutranksDuration(t *testing.T) {
	rows := []database.BookFile{
		{ID: "has-duration-only", Duration: 3600},
		{ID: "has-fingerprint-only", AcoustIDFingerprint: []byte{0x09}},
	}
	if got := rankKeeper(rows)[0]; got.ID != "has-fingerprint-only" {
		t.Fatalf("keeper = %q, want the fingerprinted row", got.ID)
	}
}

// 🔑 Stability matters operationally: the dry run and the apply that follows it
// must choose the SAME keeper, or the report the owner approved describes a
// different deletion than the one performed.
func TestRankKeeper_IsDeterministicAcrossRuns(t *testing.T) {
	mk := func() []database.BookFile {
		return []database.BookFile{
			{ID: "ccc", Duration: 600},
			{ID: "aaa", Duration: 600},
			{ID: "bbb", Duration: 600},
		}
	}
	first := rankKeeper(mk())[0].ID
	for i := 0; i < 20; i++ {
		if got := rankKeeper(mk())[0].ID; got != first {
			t.Fatalf("keeper changed between runs: %q then %q — a dry run would not "+
				"describe the same deletion the apply performs", first, got)
		}
	}
	if first != "aaa" {
		t.Fatalf("keeper = %q, want the lexicographically smallest id as the stable tiebreak", first)
	}
}

// All else equal the file hash breaks the tie before the id does.
func TestRankKeeper_PrefersRowWithFileHash(t *testing.T) {
	rows := []database.BookFile{
		{ID: "aaa", Duration: 600},
		{ID: "zzz", Duration: 600, FileHash: "abc123"},
	}
	if got := rankKeeper(rows)[0]; got.ID != "zzz" {
		t.Fatalf("keeper = %q, want the row carrying a file hash", got.ID)
	}
}

// A single row is returned untouched — nothing to choose, nothing to delete.
func TestRankKeeper_SingleRowUnchanged(t *testing.T) {
	rows := []database.BookFile{{ID: "only", Duration: 42}}
	got := rankKeeper(rows)
	if len(got) != 1 || got[0].ID != "only" {
		t.Fatalf("single row was altered: %+v", got)
	}
}

// rankKeeper must not mutate its input: the caller still holds the original
// slice and uses ranked[1:] to decide what to delete.
func TestRankKeeper_DoesNotMutateInput(t *testing.T) {
	rows := []database.BookFile{
		{ID: "aaa"},
		{ID: "zzz", AcoustIDFingerprint: []byte{0x01}},
	}
	_ = rankKeeper(rows)
	if rows[0].ID != "aaa" || rows[1].ID != "zzz" {
		t.Fatalf("input slice was reordered: %s, %s", rows[0].ID, rows[1].ID)
	}
}
