// file: internal/metadata/signal_fields_test.go
// version: 1.0.0
// guid: 4d6b1f90-2a7c-4e58-9b03-1c8e5a2f7d64
// last-edited: 2026-09-10

package metadata

import "testing"

// These tests lock the content-matcher SIGNAL fields that providers previously
// decoded and dropped: abridged/unabridged, per-provider runtime, secondary
// series, page count, subtitle, and the ISBN-10/ISBN-13 split.

func TestAudible_ProductToMetadata_FormatTypeAndSubtitle(t *testing.T) {
	client := NewAudibleClientWithBaseURL("http://unused")

	cases := []struct {
		name       string
		formatType string
		want       *bool // nil = expect unset
	}{
		{"abridged", "abridged", ptrBool(true)},
		{"unabridged", "unabridged", ptrBool(false)},
		{"mixed_case", "Unabridged", ptrBool(false)},
		{"unknown", "", nil},
		{"garbage", "something-else", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &audibleProduct{ASIN: "B01", Title: "T", Subtitle: "A Sub", FormatType: tc.formatType}
			meta := client.productToMetadata(p)
			switch {
			case tc.want == nil && meta.Abridged != nil:
				t.Errorf("Abridged: got %v, want nil", *meta.Abridged)
			case tc.want != nil && meta.Abridged == nil:
				t.Errorf("Abridged: got nil, want %v", *tc.want)
			case tc.want != nil && *meta.Abridged != *tc.want:
				t.Errorf("Abridged: got %v, want %v", *meta.Abridged, *tc.want)
			}
			if meta.Subtitle != "A Sub" {
				t.Errorf("Subtitle: got %q, want %q", meta.Subtitle, "A Sub")
			}
		})
	}
}

func TestAudnexus_BookToMetadata_SecondarySeriesAndRuntime(t *testing.T) {
	client := NewAudnexusClientWithBaseURL("http://unused")
	runtimeMin := 615
	book := &audnexusBook{
		ASIN:             "B02",
		Title:            "T",
		Subtitle:         "Sub2",
		RuntimeLengthMin: &runtimeMin,
		SeriesPrimary:    &audnexusSeries{Name: "Primary", Position: "3"},
		SeriesSecondary:  &audnexusSeries{Name: "Secondary", Position: "1"},
	}
	meta := client.bookToMetadata(book)
	if meta == nil {
		t.Fatal("bookToMetadata returned nil")
	}
	if meta.SeriesSecondary != "Secondary" {
		t.Errorf("SeriesSecondary: got %q, want %q", meta.SeriesSecondary, "Secondary")
	}
	if meta.SeriesSecondaryPosition != "1" {
		t.Errorf("SeriesSecondaryPosition: got %q, want %q", meta.SeriesSecondaryPosition, "1")
	}
	if want := runtimeMin * 60; meta.DurationSec != want {
		t.Errorf("DurationSec: got %d, want %d", meta.DurationSec, want)
	}
	if meta.Subtitle != "Sub2" {
		t.Errorf("Subtitle: got %q, want %q", meta.Subtitle, "Sub2")
	}
}

func TestHardcover_DocumentToMetadata_RuntimePagesAndISBNSplit(t *testing.T) {
	doc := &hardcoverDocument{
		Title:        "T",
		ISBNs:        []string{"0000000010", "9780000000013"}, // ISBN-10 then ISBN-13
		Pages:        321,
		AudioSeconds: 43200,
	}
	meta := hardcoverDocumentToMetadata(doc)
	if meta.ISBN10 != "0000000010" {
		t.Errorf("ISBN10: got %q, want %q", meta.ISBN10, "0000000010")
	}
	if meta.ISBN13 != "9780000000013" {
		t.Errorf("ISBN13: got %q, want %q", meta.ISBN13, "9780000000013")
	}
	if meta.ISBN != "9780000000013" { // single ISBN prefers 13 for back-compat
		t.Errorf("ISBN (back-compat): got %q, want the ISBN-13", meta.ISBN)
	}
	if meta.PageCount != 321 {
		t.Errorf("PageCount: got %d, want 321", meta.PageCount)
	}
	if meta.DurationSec != 43200 {
		t.Errorf("DurationSec: got %d, want 43200", meta.DurationSec)
	}
}

func ptrBool(b bool) *bool { return &b }
