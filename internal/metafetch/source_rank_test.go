// file: internal/metafetch/source_rank_test.go
// version: 1.3.0
// guid: 0d6c2f85-47e1-4b39-9a70-3e8b5c1d2f94
// last-edited: 2026-10-12

package metafetch

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

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

// A book the owner fills from a local-catalog candidate is tagged apart from
// a live Audible fill, and ranked just below it: a hand-picked or review-lane
// apply from live Audible may replace it, and it replaces nothing Audible
// filled. The automatic upgrade sweep never visits it (it stays above the
// sweep's ceiling, LowQualitySourceMaxRank).
func TestMetadataSourceSlug_LocalCatalogRanksJustBelowAudible(t *testing.T) {
	if got := MetadataSourceSlug(metadata.CatalogSourceName); got != "audible_catalog" {
		t.Fatalf("MetadataSourceSlug(%q) = %q, want audible_catalog", metadata.CatalogSourceName, got)
	}
	if got := MetadataSourceTag(metadata.CatalogSourceName); got != "metadata:source:audible_catalog" {
		t.Errorf("MetadataSourceTag = %q", got)
	}
	cat, ok := SourceRank(metadata.CatalogSourceName)
	aud, _ := SourceRank("Audible")
	hc, _ := SourceRank("Hardcover")
	if !ok || cat >= aud || cat <= hc {
		t.Errorf("catalog rank %d (ranked %v) must sit between Hardcover %d and Audible %d", cat, ok, hc, aud)
	}
	if !SourceOutranks("Audible", metadata.CatalogSourceName) || !SourceOutranks("Audnexus (Audible)", metadata.CatalogSourceName) {
		t.Error("a hand-picked or review-lane apply from live Audible must be able to replace a catalog fill")
	}
	if SourceOutranks(metadata.CatalogSourceName, "Audible") || SourceOutranks(metadata.CatalogSourceName, metadata.CatalogSourceName) {
		t.Error("a catalog candidate must replace neither Audible's fill nor its own")
	}
	if !SourceOutranks(metadata.CatalogSourceName, "Open Library") {
		t.Error("a catalog candidate outranks Open Library")
	}
	if cat <= LowQualitySourceMaxRank {
		t.Errorf("catalog rank %d is at or below the upgrade sweep's ceiling %d: the sweep would revisit catalog fills", cat, LowQualitySourceMaxRank)
	}
}
