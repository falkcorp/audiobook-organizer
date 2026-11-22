// file: core/auth/oidc/returnpath_test.go
// version: 0.1.0
// guid: 3b9e7c24-6d1f-4a85-9c02-7e4a5d8f1b63
// last-edited: 2026-11-22

package oidc

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// ReturnPayloads is testdata/return_payloads.json, the payload table every
// return-path sanitizer (Go and the shell's TypeScript) is checked against.
type returnPayloads struct {
	Reject []string `json:"reject"`
	Accept []string `json:"accept"`
}

func loadReturnPayloads(t *testing.T) returnPayloads {
	t.Helper()
	raw, err := os.ReadFile("testdata/return_payloads.json")
	if err != nil {
		t.Fatal(err)
	}
	var p returnPayloads
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Reject) == 0 || len(p.Accept) == 0 {
		t.Fatal("return_payloads.json has an empty list")
	}
	return p
}

// TestSanitizeReturn_PayloadTable: every shared reject payload is refused and
// every accept payload comes back byte-for-byte.
func TestSanitizeReturn_PayloadTable(t *testing.T) {
	p := loadReturnPayloads(t)
	for _, in := range p.Reject {
		if got := SanitizeReturn(in); got != "" {
			t.Errorf("SanitizeReturn(%q) = %q, want it refused", in, got)
		}
	}
	for _, in := range p.Accept {
		if got := SanitizeReturn(in); got != in {
			t.Errorf("SanitizeReturn(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestSanitizeReturn_DotSegmentsResolveScheme names the parser differential
// directly: each payload passes the raw-prefix checks but resolves to "//host".
func TestSanitizeReturn_DotSegmentsResolveScheme(t *testing.T) {
	for _, in := range []string{"/.//gotham.example.test", "/%2e//gotham.example.test", "/arkham/..//gotham.example.test", "/./%2F/gotham.example.test"} {
		if got := SanitizeReturn(in); got != "" {
			t.Errorf("SanitizeReturn(%q) = %q: it resolves to a scheme-relative URL and must be refused", in, got)
		}
	}
}

func TestSanitizeReturn_Length(t *testing.T) {
	if got := SanitizeReturn("/" + strings.Repeat("a", maxReturnLen-1)); got == "" {
		t.Error("a path of exactly maxReturnLen bytes must be accepted")
	}
	if got := SanitizeReturn("/" + strings.Repeat("a", maxReturnLen)); got != "" {
		t.Error("a path over maxReturnLen bytes must be refused")
	}
}

func TestRemoveDotSegments_RFC3986Examples(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b/c/./../../g":   "/a/g",
		"mid/content=5/../6": "mid/6",
		"/.//x":              "//x",
		"/a/..//x":           "//x",
		"/a/b/..":            "/a/",
		"/..":                "/",
		"/":                  "/",
	} {
		if got := removeDotSegments(in); got != want {
			t.Errorf("removeDotSegments(%q) = %q, want %q", in, got, want)
		}
	}
}
