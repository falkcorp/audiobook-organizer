// file: core/version/version_test.go
// version: 1.0.0
// guid: 1fd445cc-cf0b-42c5-bf2d-498ddf900fc8
// last-edited: 2026-11-07

package version

import (
	"regexp"
	"testing"
)

func TestVersionIsSemver(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Fatalf("Version = %q, want MAJOR.MINOR.PATCH", Version)
	}
}
