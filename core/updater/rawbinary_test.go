// file: core/updater/rawbinary_test.go
// version: 1.0.0
// guid: a7d26e94-1f58-4c03-9b8e-5c40d3f7a216
// last-edited: 2026-11-18

package updater

import "testing"

// TestRawBinary_MatchesHEAD: HEAD asked for the asset "<binary>-<GOOS>-<GOARCH>"
// and nothing else; RawBinary is that rule with the binary name as a
// parameter. Core names no app, so the fixtures are Gotham's; ABO's own name
// is pinned where it lives (internal/updater's TestCoreOptions).
func TestRawBinary_MatchesHEAD(t *testing.T) {
	cases := []struct{ binary, goos, goarch, want string }{
		{"gotham-hq", "linux", "amd64", "gotham-hq-linux-amd64"},
		{"gotham-hq", "linux", "arm64", "gotham-hq-linux-arm64"},
		{"gotham-hq", "darwin", "arm64", "gotham-hq-darwin-arm64"},
		{"batcave", "linux", "amd64", "batcave-linux-amd64"},
	}
	for _, tc := range cases {
		if got := RawBinary(tc.binary)(Release{Version: "9.9.9"}, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("RawBinary(%q)(%s, %s) = %q, want %q", tc.binary, tc.goos, tc.goarch, got, tc.want)
		}
	}
}
