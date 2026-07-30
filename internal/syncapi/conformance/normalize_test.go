// file: internal/syncapi/conformance/normalize_test.go
// version: 1.0.0
// guid: d620f49c-aba3-4604-bf38-eca1282d737b
// last-edited: 2026-07-29

package conformance

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizePreservesTypeWhileCanonicalizingValue(t *testing.T) {
	n := NewNormalizer()
	in := mustJSON(t, `{"id":"abc123","addedAt":1720000000000,"title":"Odyssey"}`)

	got := n.Normalize(in).(map[string]any)

	// Volatile string keeps string type.
	if JSONType(got["id"]) != "string" {
		t.Errorf("id should stay a string, got %s", JSONType(got["id"]))
	}
	if got["id"] == "abc123" {
		t.Errorf("id should have been canonicalized")
	}
	// Volatile number keeps number type.
	if JSONType(got["addedAt"]) != "number" {
		t.Errorf("addedAt should stay a number, got %s", JSONType(got["addedAt"]))
	}
	if got["addedAt"] != float64(0) {
		t.Errorf("addedAt should canonicalize to 0, got %v", got["addedAt"])
	}
	// Non-volatile values are untouched.
	if got["title"] != "Odyssey" {
		t.Errorf("title should be untouched, got %v", got["title"])
	}
}

func TestNormalizeRecursesIntoNestedObjectsAndArrays(t *testing.T) {
	n := NewNormalizer()
	in := mustJSON(t, `{"libraries":[{"id":"L1","name":"Books"},{"id":"L2","name":"Pods"}]}`)

	got := n.Normalize(in).(map[string]any)
	libs := got["libraries"].([]any)

	for i, raw := range libs {
		lib := raw.(map[string]any)
		if lib["id"] == "L1" || lib["id"] == "L2" {
			t.Errorf("libraries[%d].id should have been canonicalized, got %v", i, lib["id"])
		}
		if JSONType(lib["id"]) != "string" {
			t.Errorf("libraries[%d].id should stay a string", i)
		}
	}
	if libs[0].(map[string]any)["name"] != "Books" {
		t.Errorf("name should be untouched")
	}
}

func TestNormalizeMatchesKeysCaseInsensitively(t *testing.T) {
	// ABS is inconsistent about libraryId vs libraryID across endpoints.
	n := NewNormalizer()
	in := mustJSON(t, `{"libraryID":"X","LibraryId":"Y"}`)

	got := n.Normalize(in).(map[string]any)

	if got["libraryID"] == "X" {
		t.Errorf("libraryID should have been canonicalized")
	}
	if got["LibraryId"] == "Y" {
		t.Errorf("LibraryId should have been canonicalized")
	}
}

func TestNormalizeDoesNotMutateInput(t *testing.T) {
	n := NewNormalizer()
	raw := `{"id":"keepme","nested":{"token":"secret"}}`
	in := mustJSON(t, raw)

	_ = n.Normalize(in)

	original := mustJSON(t, raw)
	if !reflect.DeepEqual(in, original) {
		t.Errorf("Normalize mutated its input: %v", in)
	}
}

func TestNormalizedFixturesCompareClean(t *testing.T) {
	// Two responses differing ONLY in volatile fields must be conformant.
	n := NewNormalizer()
	a := mustJSON(t, `{"id":"aaa","updatedAt":1,"title":"T","tracks":[{"ino":"11","index":1}]}`)
	b := mustJSON(t, `{"id":"bbb","updatedAt":2,"title":"T","tracks":[{"ino":"22","index":1}]}`)

	fs := Compare(n.Normalize(a), n.Normalize(b), Options{CompareValues: true})

	if len(fs) != 0 {
		t.Errorf("normalized documents should compare clean, got %v", fs)
	}
}

func TestDefaultVolatileKeysCoverTheObviousOnes(t *testing.T) {
	keys := DefaultVolatileKeys()
	for _, k := range []string{"id", "token", "refreshtoken", "createdat", "updatedat", "addedat", "ino"} {
		if !keys[k] {
			t.Errorf("expected %q in DefaultVolatileKeys (lowercased)", k)
		}
	}
	// currentTime is MEANINGFUL progress data, never volatile.
	if keys["currenttime"] {
		t.Errorf("currentTime must not be treated as volatile -- it is real progress data")
	}
	_ = json.Marshal
}
