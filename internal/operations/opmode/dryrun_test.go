// file: internal/operations/opmode/dryrun_test.go
// version: 1.1.0
// guid: 84883e45-012b-49ee-b0c3-871398fbcd21
// last-edited: 2026-09-25

package opmode

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseDryRun(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    bool
		wantErr string
	}{
		{"nil params preview", ``, true, ""},
		{"empty object preview", `{}`, true, ""},
		{"null preview", `null`, true, ""},
		{"unrelated keys preview", `{"limit":5}`, true, ""},
		{"snake false live", `{"dry_run":false}`, false, ""},
		{"camel false live", `{"dryRun":false}`, false, ""},
		{"snake true preview", `{"dry_run":true}`, true, ""},
		{"camel true preview", `{"dryRun":true}`, true, ""},
		{"both agree live", `{"dry_run":false,"dryRun":false}`, false, ""},
		{"disagree refused", `{"dry_run":false,"dryRun":true}`, true, "disagree"},
		{"disagree other way refused", `{"dry_run":true,"dryRun":false}`, true, "disagree"},
		{"malformed refused", `{"dry_run":`, true, "invalid params"},
		{"string not bool refused", `{"dry_run":"false"}`, true, "invalid params"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDryRun("test.op", json.RawMessage(tc.raw))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				if !strings.HasPrefix(err.Error(), "test.op: ") {
					t.Fatalf("error must name the op: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("dryRun = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveDryRun_ErrorFailsTowardPreview(t *testing.T) {
	f, tr := false, true
	got, err := ResolveDryRun("x", &f, &tr)
	if err == nil || !got {
		t.Fatalf("disagreement must return (true, err); got (%v, %v)", got, err)
	}
	if got, err := ResolveDryRun("x", nil, nil); err != nil || !got {
		t.Fatalf("no mode stated must be preview; got (%v, %v)", got, err)
	}
}

func TestLiveAndPreviewPointers(t *testing.T) {
	if l := Live(); l == nil || *l {
		t.Fatal("Live() must point at false")
	}
	if p := Preview(); p == nil || !*p {
		t.Fatal("Preview() must point at true")
	}
	// Distinct allocations: mutating one caller's flag cannot flip another's.
	a, b := Live(), Live()
	*a = true
	if *b {
		t.Fatal("Live() must return a fresh pointer each call")
	}
}

// TestResolveDryRunDefault: an omitted mode takes the caller's default in
// either direction, a stated mode wins over it, and a disagreement is refused
// toward preview even when the default is live.
func TestResolveDryRunDefault(t *testing.T) {
	tr, fa := true, false
	cases := []struct {
		name         string
		snake, camel *bool
		omitted      bool
		want         bool
		wantErr      bool
	}{
		{"omitted, default live", nil, nil, false, false, false},
		{"omitted, default preview", nil, nil, true, true, false},
		{"camel false beats preview default", nil, &fa, true, false, false},
		{"snake true beats live default", &tr, nil, false, true, false},
		{"conflict with live default", &fa, &tr, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveDryRunDefault("test.op", tc.snake, tc.camel, tc.omitted)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
