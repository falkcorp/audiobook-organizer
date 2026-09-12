// file: internal/server/handlers/audiobooks/sort_by_metric_internal_test.go
// version: 1.0.0
// guid: 7d2e4b19-8c3a-4f60-a5e1-3b9d0c6f2e84
// last-edited: 2026-09-11

package audiobookshandler

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestSortByMetricLabel_DefaultAndOther(t *testing.T) {
	cases := map[string]string{
		"":                  "default",
		"title":             "title",
		"no_such_field":     "other",
		"Title":             "other", // SortBooks keys are exact; so is the label.
		"title; drop table": "other",
	}
	for in, want := range cases {
		if got := sortByMetricLabel(in); got != want {
			t.Errorf("sortByMetricLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every field SortBooks understands must be reported under its own name. If
// any of them fell into "other", the metric could not tell the owner which
// field to add to enabled_sort_indexes -- the one question it exists to
// answer. Enumerated from the comparator map, so a newly sortable field is
// covered on arrival.
func TestSortByMetricLabel_EverySortableFieldKeepsItsName(t *testing.T) {
	fields := database.SortableBookFields()
	if len(fields) == 0 {
		t.Fatal("SortableBookFields returned nothing; the test would pass vacuously")
	}
	for _, f := range fields {
		if got := sortByMetricLabel(f); got != f {
			t.Errorf("sortByMetricLabel(%q) = %q, want the field name itself", f, got)
		}
	}
}
