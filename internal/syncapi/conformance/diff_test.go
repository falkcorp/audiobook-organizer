// file: internal/syncapi/conformance/diff_test.go
// version: 1.0.0
// guid: 52981717-0dd3-4c3e-a5e5-7bd6f802fe0c
// last-edited: 2026-07-29

package conformance

import (
	"encoding/json"
	"testing"
)

// mustJSON unmarshals a JSON literal for use in table-driven tests.
func mustJSON(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return v
}

// findingAt returns the first finding at path with the given kind, or nil.
func findingAt(fs []Finding, path string, kind FindingKind) *Finding {
	for i := range fs {
		if fs[i].Path == path && fs[i].Kind == kind {
			return &fs[i]
		}
	}
	return nil
}

func TestCompareDetectsMissingField(t *testing.T) {
	want := mustJSON(t, `{"id":"x","title":"T"}`)
	got := mustJSON(t, `{"id":"x"}`)

	fs := Compare(want, got, Options{})

	if findingAt(fs, "title", KindMissingField) == nil {
		t.Fatalf("expected missing_field at %q, got %v", "title", fs)
	}
}

func TestCompareDetectsMissingNestedField(t *testing.T) {
	want := mustJSON(t, `{"media":{"metadata":{"title":"T","narrator":"N"}}}`)
	got := mustJSON(t, `{"media":{"metadata":{"title":"T"}}}`)

	fs := Compare(want, got, Options{})

	if findingAt(fs, "media.metadata.narrator", KindMissingField) == nil {
		t.Fatalf("expected missing_field at media.metadata.narrator, got %v", fs)
	}
}

func TestCompareDetectsTypeMismatch(t *testing.T) {
	// ABS returns duration as a number; a string would break clients.
	want := mustJSON(t, `{"duration":123.5}`)
	got := mustJSON(t, `{"duration":"123.5"}`)

	fs := Compare(want, got, Options{})

	f := findingAt(fs, "duration", KindTypeMismatch)
	if f == nil {
		t.Fatalf("expected type_mismatch at duration, got %v", fs)
	}
	if f.Want != "number" || f.Got != "string" {
		t.Errorf("want number/string, got %s/%s", f.Want, f.Got)
	}
}

func TestCompareReportsExtraFieldAndCanIgnoreIt(t *testing.T) {
	want := mustJSON(t, `{"id":"x"}`)
	got := mustJSON(t, `{"id":"x","ourExtra":1}`)

	if findingAt(Compare(want, got, Options{}), "ourExtra", KindExtraField) == nil {
		t.Errorf("expected extra_field to be reported by default")
	}
	if findingAt(Compare(want, got, Options{IgnoreExtra: true}), "ourExtra", KindExtraField) != nil {
		t.Errorf("expected extra_field to be suppressed by IgnoreExtra")
	}
}

func TestCompareChecksArrayElementShape(t *testing.T) {
	want := mustJSON(t, `{"tracks":[{"index":1,"startOffset":0.0}]}`)
	got := mustJSON(t, `{"tracks":[{"index":1}]}`)

	fs := Compare(want, got, Options{})

	if findingAt(fs, "tracks[0].startOffset", KindMissingField) == nil {
		t.Fatalf("expected missing_field at tracks[0].startOffset, got %v", fs)
	}
}

func TestCompareFlagsEmptyArrayWhenFixtureHasElements(t *testing.T) {
	// A client that expects chapters and receives none is a real failure.
	want := mustJSON(t, `{"chapters":[{"start":0.0}]}`)
	got := mustJSON(t, `{"chapters":[]}`)

	fs := Compare(want, got, Options{})

	f := findingAt(fs, "chapters", KindLengthMismatch)
	if f == nil {
		t.Fatalf("expected length_mismatch at chapters, got %v", fs)
	}
	if f.Want != "1" || f.Got != "0" {
		t.Errorf("want 1/0, got %s/%s", f.Want, f.Got)
	}
}

func TestCompareIgnoresValuesByDefaultAndComparesWhenAsked(t *testing.T) {
	want := mustJSON(t, `{"title":"Odyssey"}`)
	got := mustJSON(t, `{"title":"Iliad"}`)

	if fs := Compare(want, got, Options{}); len(fs) != 0 {
		t.Errorf("expected no findings when CompareValues is false, got %v", fs)
	}
	if findingAt(Compare(want, got, Options{CompareValues: true}), "title", KindValueMismatch) == nil {
		t.Errorf("expected value_mismatch when CompareValues is true")
	}
}

func TestCompareCleanWhenIdentical(t *testing.T) {
	want := mustJSON(t, `{"a":1,"b":{"c":[true,null]}}`)
	got := mustJSON(t, `{"a":1,"b":{"c":[true,null]}}`)

	if fs := Compare(want, got, Options{CompareValues: true}); len(fs) != 0 {
		t.Errorf("expected zero findings for identical documents, got %v", fs)
	}
}
