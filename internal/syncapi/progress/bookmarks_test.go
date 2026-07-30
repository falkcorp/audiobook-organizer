// file: internal/syncapi/progress/bookmarks_test.go
// version: 1.0.0
// guid: 57379a49-1562-45d1-94bb-6ca07373721c
// last-edited: 2026-07-30

package progress

import (
	"sort"
	"testing"
)

func TestParseTimeSec_AcceptsIntAndFloatStrings(t *testing.T) {
	cases := []string{"12", "12.0", "12.5", "0"}
	for _, raw := range cases {
		if _, err := ParseTimeSec(raw); err != nil {
			t.Errorf("ParseTimeSec(%q) unexpected error: %v", raw, err)
		}
	}

	intVal, err := ParseTimeSec("12")
	if err != nil {
		t.Fatalf("ParseTimeSec(%q) unexpected error: %v", "12", err)
	}
	floatVal, err := ParseTimeSec("12.0")
	if err != nil {
		t.Fatalf("ParseTimeSec(%q) unexpected error: %v", "12.0", err)
	}
	if intVal != floatVal {
		t.Errorf("ParseTimeSec(\"12\")=%v != ParseTimeSec(\"12.0\")=%v, want equal", intVal, floatVal)
	}
}

func TestParseTimeSec_RejectsGarbage(t *testing.T) {
	cases := []string{"abc", "", "12.5.5"}
	for _, raw := range cases {
		if _, err := ParseTimeSec(raw); err == nil {
			t.Errorf("ParseTimeSec(%q) expected error, got nil", raw)
		}
	}
}

func TestCanonicalTimeKey_IntAndFloatCollide(t *testing.T) {
	a := CanonicalTimeKey(12)
	b := CanonicalTimeKey(12.0)
	if a != b {
		t.Errorf("CanonicalTimeKey(12)=%q != CanonicalTimeKey(12.0)=%q, want identical", a, b)
	}
}

func TestCanonicalTimeKey_OrdersNumerically(t *testing.T) {
	values := []float64{0, 1, 1.5, 10, 100.25, 9999}
	keys := make([]string, len(values))
	for i, v := range values {
		keys[i] = CanonicalTimeKey(v)
	}

	sorted := make([]string, len(keys))
	copy(sorted, keys)
	sort.Strings(sorted)

	for i := range keys {
		if keys[i] != sorted[i] {
			t.Fatalf("CanonicalTimeKey outputs not in numeric order: got %v, sorted %v (input values %v)", keys, sorted, values)
		}
	}
}

func TestValidateBookmark_RejectsNegativeTime(t *testing.T) {
	b := Bookmark{UserID: "u1", ItemID: "i1", TimeSec: -1, Title: "t"}
	if err := ValidateBookmark(b); err == nil {
		t.Error("expected error for negative TimeSec, got nil")
	}
}

func TestValidateBookmark_RejectsEmptyTitle(t *testing.T) {
	b := Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 10, Title: ""}
	if err := ValidateBookmark(b); err == nil {
		t.Error("expected error for empty Title, got nil")
	}
}

func TestValidateBookmark_RejectsEmptyUserOrItem(t *testing.T) {
	cases := []Bookmark{
		{UserID: "", ItemID: "i1", TimeSec: 10, Title: "t"},
		{UserID: "u1", ItemID: "", TimeSec: 10, Title: "t"},
	}
	for _, b := range cases {
		if err := ValidateBookmark(b); err == nil {
			t.Errorf("expected error for %+v, got nil", b)
		}
	}
}

func TestValidateBookmark_AcceptsValid(t *testing.T) {
	b := Bookmark{UserID: "u1", ItemID: "i1", TimeSec: 10, Title: "t"}
	if err := ValidateBookmark(b); err != nil {
		t.Errorf("expected valid bookmark to pass, got error: %v", err)
	}
}
