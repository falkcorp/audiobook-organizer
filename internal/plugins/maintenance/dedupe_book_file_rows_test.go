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

// 🔴 THE CANARY REGRESSION. "The Trapped Mind Project" had 130 rows for one
// file. Ranking kept the fingerprinted row, which had Duration == 0, and the
// book dropped to 0.00h — the duration existed only on rows that were then
// deleted.
//
// Ranking picks a ROW; the keeper must instead end up with the best of EVERY
// field before its twins are destroyed.
func TestMergeMissingFields_RecoversDurationFromADiscardedTwin(t *testing.T) {
	keeper := database.BookFile{ID: "keeper", AcoustIDFingerprint: []byte{0x01}}
	twins := []database.BookFile{
		{ID: "twin-a", Duration: 0},
		{ID: "twin-b", Duration: 21877},
	}
	got, changed := mergeMissingFields(keeper, twins)
	if !changed {
		t.Fatal("merge reported no change, but the keeper was missing a duration")
	}
	if got.Duration != 21877 {
		t.Fatalf("duration = %d, want 21877 recovered from the twin that is about to be deleted", got.Duration)
	}
	if len(got.AcoustIDFingerprint) != 1 {
		t.Fatal("the keeper's own fingerprint was lost during the merge")
	}
}

// The merge must be strictly additive: a value the keeper already holds always
// wins, so this can never replace good data with worse.
func TestMergeMissingFields_NeverOverwritesExistingValues(t *testing.T) {
	keeper := database.BookFile{
		ID: "keeper", Duration: 3600, FileHash: "good", FileSize: 999,
		AcoustIDFingerprint: []byte{0xAA},
	}
	twins := []database.BookFile{{
		ID: "twin", Duration: 1, FileHash: "worse", FileSize: 1,
		AcoustIDFingerprint: []byte{0xBB},
	}}
	got, changed := mergeMissingFields(keeper, twins)
	if changed {
		t.Fatal("merge reported a change although the keeper was complete")
	}
	if got.Duration != 3600 || got.FileHash != "good" || got.FileSize != 999 ||
		got.AcoustIDFingerprint[0] != 0xAA {
		t.Fatalf("an existing keeper value was overwritten: %+v", got)
	}
}

// A fingerprint can be salvaged too, not just a duration.
func TestMergeMissingFields_RecoversFingerprintAndFpDuration(t *testing.T) {
	keeper := database.BookFile{ID: "keeper", Duration: 600}
	twins := []database.BookFile{{
		ID: "twin", AcoustIDFingerprint: []byte{0x07, 0x08},
		AcoustIDFingerprintDurationSec: 601.5,
	}}
	got, changed := mergeMissingFields(keeper, twins)
	if !changed || len(got.AcoustIDFingerprint) != 2 {
		t.Fatalf("fingerprint was not salvaged: %+v", got)
	}
	if got.AcoustIDFingerprintDurationSec != 601.5 {
		t.Fatalf("fingerprint duration = %v, want 601.5", got.AcoustIDFingerprintDurationSec)
	}
}

// Nothing to salvage means nothing is written — the op must not issue a pointless
// UpdateBookFile, which is the one write path that bypasses the millisecond guard.
func TestMergeMissingFields_NoChangeWhenNothingToSalvage(t *testing.T) {
	keeper := database.BookFile{ID: "k", Duration: 100, FileSize: 10, FileHash: "h",
		AcoustIDFingerprint: []byte{0x01}, AcoustIDFingerprintDurationSec: 100}
	_, changed := mergeMissingFields(keeper, []database.BookFile{{ID: "t"}})
	if changed {
		t.Fatal("merge reported a change with nothing to salvage")
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
