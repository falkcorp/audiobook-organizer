// file: internal/dedup/eligibility_test.go
// version: 1.1.0
// guid: f2a3b4c5-d6e7-4f8a-9b0c-1d2e3f4a5b6c
// last-edited: 2026-08-19

package dedup

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
)

// strPtr is already defined in helpers_test.go.

// intPtr returns a pointer to the given int value.
func intPtr(v int) *int { return &v }

// TestPairEligibility_TableDriven proves parity: PairEligibility makes the
// same accept/suppress decisions that the inline guards in engine.go made
// before T014.  Each test case corresponds to a guard site extracted verbatim.
func TestPairEligibility_TableDriven(t *testing.T) {
	dir1 := filepath.Join("tmp", "library", "AuthorA", "TitleX")
	dir2 := filepath.Join("tmp", "library", "AuthorB", "TitleY")

	tests := []struct {
		name            string
		a               *database.Book
		b               *database.Book
		wantOK          bool
		wantSuppressors []string // must be a subset of actual suppressors
	}{
		// ── version_group_same ───────────────────────────────────────────────
		{
			name:            "version_group_same: both in same non-empty group → suppressed",
			a:               &database.Book{ID: "A1", Title: "Dune", VersionGroupID: strPtr("VG1")},
			b:               &database.Book{ID: "B1", Title: "Dune", VersionGroupID: strPtr("VG1")},
			wantOK:          false,
			wantSuppressors: []string{"version_group_same"},
		},
		{
			name:   "version_group_same: different groups → eligible",
			a:      &database.Book{ID: "A2", Title: "Dune", VersionGroupID: strPtr("VG1")},
			b:      &database.Book{ID: "B2", Title: "Dune", VersionGroupID: strPtr("VG2")},
			wantOK: true,
		},
		{
			name:   "version_group_same: one nil group → eligible",
			a:      &database.Book{ID: "A3", Title: "Dune", VersionGroupID: strPtr("VG1")},
			b:      &database.Book{ID: "B3", Title: "Dune"},
			wantOK: true,
		},
		{
			name:   "version_group_same: both empty string group → eligible (empty groups are ignored)",
			a:      &database.Book{ID: "A4", Title: "Dune", VersionGroupID: strPtr("")},
			b:      &database.Book{ID: "B4", Title: "Dune", VersionGroupID: strPtr("")},
			wantOK: true,
		},
		// ── series_volume_differs (structured SeriesSequence) ────────────────
		{
			name:            "series_volume: distinct sequence numbers → suppressed",
			a:               &database.Book{ID: "A5", Title: "Series Name 3", SeriesSequence: intPtr(3)},
			b:               &database.Book{ID: "B5", Title: "Series Name 4", SeriesSequence: intPtr(4)},
			wantOK:          false,
			wantSuppressors: []string{"series_volume_differs"},
		},
		{
			name:   "series_volume: same sequence number → eligible",
			a:      &database.Book{ID: "A6", Title: "My Book", SeriesSequence: intPtr(1)},
			b:      &database.Book{ID: "B6", Title: "My Book", SeriesSequence: intPtr(1)},
			wantOK: true,
		},
		{
			name:   "series_volume: one has no sequence → eligible",
			a:      &database.Book{ID: "A7", Title: "My Book", SeriesSequence: intPtr(3)},
			b:      &database.Book{ID: "B7", Title: "My Book"},
			wantOK: true,
		},
		// ── series_volume_differs (title-extracted "Book N" pattern) ─────────
		{
			name:            "series_volume: title-extracted volume numbers differ → suppressed",
			a:               &database.Book{ID: "A8", Title: "Reclaiming Honor Book 6"},
			b:               &database.Book{ID: "B8", Title: "Reclaiming Honor Book 7"},
			wantOK:          false,
			wantSuppressors: []string{"series_volume_differs"},
		},
		{
			name:   "series_volume: same extracted volume → eligible",
			a:      &database.Book{ID: "A9", Title: "Reclaiming Honor Book 6"},
			b:      &database.Book{ID: "B9", Title: "Reclaiming Honor Book 6"},
			wantOK: true,
		},
		// ── series_volume_differs (digit-only difference fallback) ────────────
		{
			name:            "series_volume: titles differ only in digits → suppressed",
			a:               &database.Book{ID: "A10", Title: "Series Name 3"},
			b:               &database.Book{ID: "B10", Title: "Series Name 4"},
			wantOK:          false,
			wantSuppressors: []string{"series_volume_differs"},
		},
		{
			name:   "series_volume: titles without digits → eligible (no digit guard)",
			a:      &database.Book{ID: "A11", Title: "The Hobbit"},
			b:      &database.Book{ID: "B11", Title: "The Hobbit"},
			wantOK: true,
		},
		// ── same_dir_multi_file ───────────────────────────────────────────────
		{
			name:            "same_dir: both files in same directory → suppressed",
			a:               &database.Book{ID: "A12", Title: "Chapter 1", FilePath: filepath.Join(dir1, "001.mp3")},
			b:               &database.Book{ID: "B12", Title: "Chapter 2", FilePath: filepath.Join(dir1, "002.mp3")},
			wantOK:          false,
			wantSuppressors: []string{"same_dir_multi_file"},
		},
		{
			name:   "same_dir: different directories → eligible",
			a:      &database.Book{ID: "A13", Title: "BookA", FilePath: filepath.Join(dir1, "book.mp3")},
			b:      &database.Book{ID: "B13", Title: "BookB", FilePath: filepath.Join(dir2, "book.mp3")},
			wantOK: true,
		},
		{
			name:   "same_dir: one has empty path → eligible",
			a:      &database.Book{ID: "A14", Title: "BookA", FilePath: filepath.Join(dir1, "book.mp3")},
			b:      &database.Book{ID: "B14", Title: "BookB"},
			wantOK: true,
		},
		// ── no suppressors at all ─────────────────────────────────────────────
		{
			name: "eligible pair: different authors, different dirs, different series → ok",
			a: &database.Book{
				ID:       "A15",
				Title:    "Foundation",
				FilePath: filepath.Join(dir1, "foundation.mp3"),
			},
			b: &database.Book{
				ID:       "B15",
				Title:    "Foundation",
				FilePath: filepath.Join(dir2, "foundation.mp3"),
			},
			wantOK: true,
		},
		// ── multiple suppressors ──────────────────────────────────────────────
		{
			name: "multiple: same version group AND same dir → both suppressors",
			a: &database.Book{
				ID:             "A16",
				Title:          "Chapter 1",
				VersionGroupID: strPtr("VG1"),
				FilePath:       filepath.Join(dir1, "001.mp3"),
			},
			b: &database.Book{
				ID:             "B16",
				Title:          "Chapter 2",
				VersionGroupID: strPtr("VG1"),
				FilePath:       filepath.Join(dir1, "002.mp3"),
			},
			wantOK:          false,
			wantSuppressors: []string{"version_group_same", "same_dir_multi_file"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, suppressors := PairEligibility(tt.a, tt.b)
			assert.Equal(t, tt.wantOK, ok, "ok")
			for _, wantS := range tt.wantSuppressors {
				assert.Contains(t, suppressors, wantS, "expected suppressor %q", wantS)
			}
			if tt.wantOK {
				assert.Empty(t, suppressors, "no suppressors expected for eligible pair")
			} else {
				assert.NotEmpty(t, suppressors, "expected at least one suppressor")
			}
		})
	}
}

// TestPairEligibility_SymmetricDecisions verifies that PairEligibility(a, b)
// and PairEligibility(b, a) make the same eligibility decision.  Guards must
// not be order-dependent.
func TestPairEligibility_SymmetricDecisions(t *testing.T) {
	dir := filepath.Join("tmp", "dir", "A")
	pairs := []struct {
		name string
		a, b *database.Book
	}{
		{
			name: "version group",
			a:    &database.Book{ID: "A1", VersionGroupID: strPtr("VG1")},
			b:    &database.Book{ID: "B1", VersionGroupID: strPtr("VG1")},
		},
		{
			name: "same dir",
			a:    &database.Book{ID: "A2", Title: "T", FilePath: filepath.Join(dir, "001.mp3")},
			b:    &database.Book{ID: "B2", Title: "T", FilePath: filepath.Join(dir, "002.mp3")},
		},
		{
			name: "series differs",
			a:    &database.Book{ID: "A3", Title: "S 3", SeriesSequence: intPtr(3)},
			b:    &database.Book{ID: "B3", Title: "S 4", SeriesSequence: intPtr(4)},
		},
		{
			name: "eligible plain",
			a:    &database.Book{ID: "A4", Title: "Foundation"},
			b:    &database.Book{ID: "B4", Title: "Foundation"},
		},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			okFwd, _ := PairEligibility(p.a, p.b)
			okRev, _ := PairEligibility(p.b, p.a)
			assert.Equal(t, okFwd, okRev, "PairEligibility must be symmetric")
		})
	}
}
