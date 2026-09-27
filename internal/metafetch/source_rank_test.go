// file: internal/metafetch/source_rank_test.go
// version: 1.0.0
// guid: 0d6c2f85-47e1-4b39-9a70-3e8b5c1d2f94
// last-edited: 2026-09-27

package metafetch

import "testing"

// Every provider's real display name (metadata.MetadataSource.Name()) maps to
// a ranked slug, and the tag writer uses the same slug.
func TestMetadataSourceSlug_EveryProviderIsRanked(t *testing.T) {
	for name, want := range map[string]string{
		"Audible":            "audible",
		"Audnexus (Audible)": "audnexus",
		"Hardcover":          "hardcover",
		"Open Library":       "open_library",
		"Google Books":       "google_books",
		"Wikipedia":          "wikipedia",
		"openlibrary":        "open_library",
		"googlebooks":        "google_books",
	} {
		if got := MetadataSourceSlug(name); got != want {
			t.Errorf("MetadataSourceSlug(%q) = %q, want %q", name, got, want)
		}
		if _, ok := SourceRank(name); !ok {
			t.Errorf("%q is unranked", name)
		}
		if tag := MetadataSourceTag(name); tag != "metadata:source:"+want {
			t.Errorf("MetadataSourceTag(%q) = %q", name, tag)
		}
	}
	if MetadataSourceTag("  ") != "" {
		t.Error("an empty name must produce no tag")
	}
}

func TestSourceOutranks(t *testing.T) {
	cases := []struct {
		cand, cur string
		want      bool
	}{
		{"Audible", "open_library", true},
		{"Audnexus (Audible)", "google_books", true},
		{"Hardcover", "open_library", true},
		{"Open Library", "google_books", true},
		{"Google Books", "wikipedia", true},
		// Same rank never replaces.
		{"Audible", "audnexus", false},
		{"Open Library", "open_library", false},
		// Lower rank never replaces.
		{"Open Library", "audible", false},
		{"Wikipedia", "hardcover", false},
		// Unranked on either side never replaces.
		{"Audible", "some_new_provider", false},
		{"Some New Provider", "wikipedia", false},
		{"Audible", "", false},
	}
	for _, tc := range cases {
		if got := SourceOutranks(tc.cand, tc.cur); got != tc.want {
			t.Errorf("SourceOutranks(%q, %q) = %v, want %v", tc.cand, tc.cur, got, tc.want)
		}
	}
}

// historySource and historyKind label a rank upgrade as itself, never as an
// owner action, and a rank upgrade requires its history.
func TestApplyOptions_RankUpgradeLabels(t *testing.T) {
	o := ApplyOptions{UnseenCandidate: true, RankUpgrade: "rank upgrade: open_library -> audible"}
	if got := o.historySource("Audible"); got != "Audible (rank upgrade: open_library -> audible)" {
		t.Errorf("historySource = %q", got)
	}
	if got := o.historyKind(); got != "rank-upgrade" {
		t.Errorf("historyKind = %q", got)
	}
	if !o.requiresHistory() {
		t.Error("a rank upgrade overwrote fields; its history is required")
	}
	if !o.automatic() {
		t.Error("a rank upgrade is automatic")
	}
	if (ApplyOptions{FillOnly: true}).requiresHistory() {
		t.Error("a fill-only apply does not require history")
	}
}
