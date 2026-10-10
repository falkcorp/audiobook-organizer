// file: internal/telemetry/endpoint_test.go
// version: 1.1.0
// guid: 0b8d3f6e-7a41-4c29-8e5d-91f2a6c4b370
// last-edited: 2026-10-10

package telemetry

import "testing"

// The accepted forms and their canonical rebuild. This table started as the
// pre-extraction accept/reject table; the strict parser (round 5) deliberately
// narrows it: nothing with a path, query, userinfo or unusual scheme is
// accepted any more (see endpoint_fuzz_test.go for the full invariants).
func TestParseOTLPEndpoint_Forms(t *testing.T) {
	ok := map[string]otlpTarget{
		"127.0.0.1:4317":                         {Target: "127.0.0.1:4317", Bare: true},
		"localhost:4317":                         {Target: "localhost:4317", Bare: true},
		"[::1]:4317":                             {Target: "[::1]:4317", Bare: true},
		"tempo:4317":                             {Target: "tempo:4317", Bare: true},
		"http://127.0.0.1:4317":                  {URL: "http://127.0.0.1:4317"},
		"https://collector.example.invalid:4317": {URL: "https://collector.example.invalid:4317"},
		"dns:///tempo:4317":                      {Target: "dns:///tempo:4317"},
		" http://127.0.0.1:4317 ":                {URL: "http://127.0.0.1:4317"},
	}
	for ep, want := range ok {
		got, err := parseOTLPEndpoint(keyMetricsEndpoint, ep)
		if err != nil {
			t.Errorf("parseOTLPEndpoint(keyMetricsEndpoint, %q) = %v, want accepted", ep, err)
			continue
		}
		if got != want {
			t.Errorf("parseOTLPEndpoint(keyMetricsEndpoint, %q) = %+v, want %+v", ep, got, want)
		}
	}
	bad := []string{"", "127.0.0.1", "http://127.0.0.1", "http://:4317", "ftp://host:21", "invalid://endpoint", "dns:///", ":4317"}
	for _, ep := range bad {
		if _, err := parseOTLPEndpoint(keyMetricsEndpoint, ep); err == nil {
			t.Errorf("parseOTLPEndpoint(keyMetricsEndpoint, %q) accepted, want an error", ep)
		}
	}
}
