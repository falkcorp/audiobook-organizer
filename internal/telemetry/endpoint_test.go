// file: internal/telemetry/endpoint_test.go
// version: 1.0.0
// guid: 0b8d3f6e-7a41-4c29-8e5d-91f2a6c4b370
// last-edited: 2026-10-10

package telemetry

import "testing"

// The same table as TestTraceEndpointOption_Forms: the extraction must not
// change a single accepted or rejected input.
func TestParseOTLPEndpoint_Forms(t *testing.T) {
	ok := map[string]otlpTarget{
		"127.0.0.1:4317":                 {Target: "127.0.0.1:4317", Bare: true},
		"localhost:4317":                 {Target: "localhost:4317", Bare: true},
		"[::1]:4317":                     {Target: "[::1]:4317", Bare: true},
		"tempo:4317":                     {Target: "tempo:4317", Bare: true},
		"http://127.0.0.1:4317":          {URL: "http://127.0.0.1:4317"},
		"https://collector.example:4317": {URL: "https://collector.example:4317"},
		"dns:///tempo:4317":              {Target: "dns:///tempo:4317"},
		" http://127.0.0.1:4317 ":        {URL: "http://127.0.0.1:4317"},
	}
	for ep, want := range ok {
		got, err := parseOTLPEndpoint(ep)
		if err != nil {
			t.Errorf("parseOTLPEndpoint(%q) = %v, want accepted", ep, err)
			continue
		}
		if got != want {
			t.Errorf("parseOTLPEndpoint(%q) = %+v, want %+v", ep, got, want)
		}
	}
	bad := []string{"", "127.0.0.1", "http://127.0.0.1", "http://:4317", "ftp://host:21", "invalid://endpoint", "dns:///", ":4317"}
	for _, ep := range bad {
		if _, err := parseOTLPEndpoint(ep); err == nil {
			t.Errorf("parseOTLPEndpoint(%q) accepted, want an error", ep)
		}
	}
}
