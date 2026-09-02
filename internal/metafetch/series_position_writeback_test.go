// file: internal/metafetch/series_position_writeback_test.go
// version: 1.0.0
// guid: 4f9a1c62-3d85-4b07-9e21-8c60d4f7a5b3
// last-edited: 2026-09-02

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The number stripped out of a series name is INFORMATION. Deleting it from the
// name and recording it nowhere is data loss, not cleanup -- it was the only
// statement of where the book sits in its series. These tests pin that the
// metadata-apply path moves it rather than dropping it.
func TestNormalizeMetaSeries_MovesPositionIntoSeriesPosition(t *testing.T) {
	tests := []struct {
		name         string
		meta         metadata.BookMetadata
		wantSeries   string
		wantPosition string
	}{
		{
			// The owner's headline case. Before this change "#5" matched no rule
			// and the whole string was written through as the series NAME.
			name:         "hash suffix moves into SeriesPosition",
			meta:         metadata.BookMetadata{Title: "The Fifth Sovereign", Series: "Nameless Sovereign #5"},
			wantSeries:   "Nameless Sovereign",
			wantPosition: "5",
		},
		{
			name:         "comma Book N moves into SeriesPosition",
			meta:         metadata.BookMetadata{Title: "Priests of Mars", Series: "Adeptus Mechanicus, Book 1"},
			wantSeries:   "Adeptus Mechanicus",
			wantPosition: "1",
		},
		{
			name:         "bare trailing number moves into SeriesPosition",
			meta:         metadata.BookMetadata{Title: "Wyrd Sisters", Series: "Discworld 05"},
			wantSeries:   "Discworld",
			wantPosition: "5",
		},
		{
			name:         "bracketed number moves into SeriesPosition",
			meta:         metadata.BookMetadata{Title: "Blood Bond", Series: "Dragon Born [04]"},
			wantSeries:   "Dragon Born",
			wantPosition: "4",
		},
		{
			name:         "embedded keyword position moves into SeriesPosition",
			meta:         metadata.BookMetadata{Title: "Becoming the Apex Supervillain", Series: "Evil Genius: Book 4: Becoming the Apex Supervillain"},
			wantSeries:   "Evil Genius",
			wantPosition: "4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.meta
			NormalizeMetaSeries(&m)
			if m.Series != tt.wantSeries {
				t.Errorf("Series: got %q, want %q", m.Series, tt.wantSeries)
			}
			if m.SeriesPosition != tt.wantPosition {
				t.Errorf("SeriesPosition: got %q, want %q -- the stripped number was DROPPED", m.SeriesPosition, tt.wantPosition)
			}
		})
	}
}

// An existing position outranks one recovered from the tail of a name. It came
// from the provider's own dedicated Sequence field.
func TestNormalizeMetaSeries_DoesNotOverwriteExistingPosition(t *testing.T) {
	m := metadata.BookMetadata{
		Title:          "Wyrd Sisters",
		Series:         "Discworld 05",
		SeriesPosition: "6",
	}
	NormalizeMetaSeries(&m)
	if m.Series != "Discworld" {
		t.Errorf("Series: got %q, want %q", m.Series, "Discworld")
	}
	if m.SeriesPosition != "6" {
		t.Errorf("SeriesPosition: got %q, want %q -- an existing sequence must never be overwritten", m.SeriesPosition, "6")
	}
}

// ⚠️ REGRESSION GUARD for the second half of NormalizeMetaSeries.
//
// ParseSeriesFromTitle runs AFTER the strip block and used to assign
// SeriesPosition UNCONDITIONALLY. So a position the strip had just recovered
// could be silently clobbered one branch later, which would make the whole
// write-back inert on exactly the inputs it exists for. The title here DOES
// parse (Pattern 1), so this test fails if that guard is removed.
func TestNormalizeMetaSeries_TitleParseDoesNotClobberExistingPosition(t *testing.T) {
	m := metadata.BookMetadata{
		Title:          "(Long Earth 05) The Long Cosmos",
		SeriesPosition: "3",
	}
	NormalizeMetaSeries(&m)
	if m.Series != "Long Earth" {
		t.Errorf("Series: got %q, want %q", m.Series, "Long Earth")
	}
	if m.SeriesPosition != "3" {
		t.Errorf("SeriesPosition: got %q, want %q -- ParseSeriesFromTitle clobbered an existing sequence", m.SeriesPosition, "3")
	}
}

// A legitimately un-numbered series is untouched, and an un-vouched number is
// left exactly as it is rather than mangled into "-EIGHTY-SIX".
func TestNormalizeMetaSeries_LeavesCleanAndUnvouchedNamesAlone(t *testing.T) {
	for _, tc := range []struct{ series, want string }{
		{"The Expanse", "The Expanse"},
		{"86—EIGHTY-SIX", "86—EIGHTY-SIX"},
		{"08. Battle for the Abyss", "08. Battle for the Abyss"},
	} {
		m := metadata.BookMetadata{Title: "Some Book", Series: tc.series}
		NormalizeMetaSeries(&m)
		if m.Series != tc.want {
			t.Errorf("Series: got %q, want %q", m.Series, tc.want)
		}
		if m.SeriesPosition != "" {
			t.Errorf("SeriesPosition: got %q, want empty for %q", m.SeriesPosition, tc.series)
		}
	}
}
