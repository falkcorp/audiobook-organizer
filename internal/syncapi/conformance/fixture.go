// file: internal/syncapi/conformance/fixture.go
// version: 1.0.0
// guid: 62ba661e-f75b-4f28-9571-1733fa5562b5
// last-edited: 2026-07-29

package conformance

import (
	"encoding/json"
	"fmt"
	"os"
)

// FixtureRequest is the request half of a captured pair.
type FixtureRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   any    `json:"body"`
}

// FixtureResponse is the response half of a captured pair.
type FixtureResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
}

// Fixture is one request/response pair captured verbatim from a real
// Audiobookshelf server. Bodies are stored raw; normalization happens here at
// compare time so the on-disk record stays faithful to what ABS returned.
type Fixture struct {
	Request  FixtureRequest  `json:"request"`
	Response FixtureResponse `json:"response"`
}

// LoadFixture reads and parses a fixture file written by
// scripts/abs_capture_fixtures.py.
func LoadFixture(path string) (*Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fixture %s: %w", path, err)
	}
	var f Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse fixture %s: %w", path, err)
	}
	return &f, nil
}

// CompareBody normalizes both the fixture body and got, then diffs them.
// A nil/empty result means our response is conformant with real ABS.
func (f *Fixture) CompareBody(got any, opts Options) []Finding {
	n := NewNormalizer()
	return Compare(n.Normalize(f.Response.Body), n.Normalize(got), opts)
}
