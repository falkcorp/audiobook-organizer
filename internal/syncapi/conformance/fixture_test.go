// file: internal/syncapi/conformance/fixture_test.go
// version: 1.0.0
// guid: 87d50c9b-de02-4056-a02f-bca4ac398e19
// last-edited: 2026-07-29

package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempFixture creates a fixture file and returns its path.
func writeTempFixture(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "get_api_libraries.json")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

const sampleFixture = `{
  "request": {"method": "GET", "path": "/api/libraries", "body": null},
  "response": {
    "status": 200,
    "headers": {"content-type": "application/json"},
    "body": {"libraries": [{"id": "lib-1", "name": "Books", "mediaType": "book"}]}
  }
}`

func TestLoadFixtureParsesEnvelope(t *testing.T) {
	f, err := LoadFixture(writeTempFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if f.Request.Method != "GET" || f.Request.Path != "/api/libraries" {
		t.Errorf("unexpected request: %+v", f.Request)
	}
	if f.Response.Status != 200 {
		t.Errorf("status = %d, want 200", f.Response.Status)
	}
	if f.Response.Headers["content-type"] != "application/json" {
		t.Errorf("unexpected headers: %v", f.Response.Headers)
	}
	if JSONType(f.Response.Body) != "object" {
		t.Errorf("body should be an object, got %s", JSONType(f.Response.Body))
	}
}

func TestLoadFixtureErrorsOnMissingFile(t *testing.T) {
	if _, err := LoadFixture(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected an error for a missing fixture file")
	}
}

func TestCompareBodyIgnoresVolatileIDs(t *testing.T) {
	f, err := LoadFixture(writeTempFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	// Our response has a DIFFERENT id but the same shape -- must be conformant.
	got := mustJSON(t, `{"libraries":[{"id":"01HXYZ","name":"Books","mediaType":"book"}]}`)

	if fs := f.CompareBody(got, Options{CompareValues: true}); len(fs) != 0 {
		t.Errorf("differing ids should normalize away, got %v", fs)
	}
}

func TestCompareBodyCatchesAMissingRequiredField(t *testing.T) {
	f, err := LoadFixture(writeTempFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	// mediaType omitted -- exactly the class of bug that breaks ABS clients.
	got := mustJSON(t, `{"libraries":[{"id":"01HXYZ","name":"Books"}]}`)

	fs := f.CompareBody(got, Options{})
	if findingAt(fs, "libraries[0].mediaType", KindMissingField) == nil {
		t.Fatalf("expected missing_field at libraries[0].mediaType, got %v", fs)
	}
}
