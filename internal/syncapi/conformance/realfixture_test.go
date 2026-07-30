// file: internal/syncapi/conformance/realfixture_test.go
// version: 1.0.0
// guid: 1a27b9b3-fd95-4105-a378-4f45aefc0a98
// last-edited: 2026-07-29

package conformance

import "testing"

// TestRealFixtureSelfCompare loads a real captured fixture and compares it to
// itself. It must be perfectly conformant; if not, the normalizer or differ is
// broken (e.g. map iteration order leaking, or a mutating Normalize).
func TestRealFixtureSelfCompare(t *testing.T) {
	f, err := LoadFixture("../../../testdata/abs-fixtures/get_api_libraries.json")
	if err != nil {
		t.Skipf("real fixture not captured yet: %v", err)
	}
	if fs := f.CompareBody(f.Response.Body, Options{CompareValues: true}); len(fs) != 0 {
		t.Fatalf("a real fixture must self-compare clean, got %v", fs)
	}
}
