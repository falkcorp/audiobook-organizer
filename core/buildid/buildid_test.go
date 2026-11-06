// file: core/buildid/buildid_test.go
// version: 1.0.0
// guid: 45e59a78-eaba-496a-b705-c5b0056a694d
// last-edited: 2026-11-05

package buildid

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestBinaryHasVersionAndHash(t *testing.T) {
	got, err := Binary("v1.2.3")
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}
	version, sum, ok := strings.Cut(got, "+")
	if !ok || version != "v1.2.3" {
		t.Fatalf("Binary = %q, want the version, a '+', then the hash", got)
	}
	if !hexSHA256.MatchString(sum) {
		t.Fatalf("hash part %q is not 64 lowercase hex characters", sum)
	}

	// The hash is the executable's own SHA-256, not a hash of something else.
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	want := sha256.Sum256(b)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("hash part = %s, want the SHA-256 of %s", sum, exe)
	}

	// Stable within one process: a second call names the same binary.
	again, err := Binary("v1.2.3")
	if err != nil || again != got {
		t.Fatalf("second Binary = %q, %v; want %q", again, err, got)
	}
}

func TestCommitEmptyWithoutVCS(t *testing.T) {
	// go test binaries carry no vcs.* build settings.
	if got := Commit(); got != "" {
		t.Fatalf("Commit() in a test binary = %q, want empty", got)
	}
	cases := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{"no settings", nil, ""},
		{"modified without revision", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, ""},
		{"clean", []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "false"}}, "abc123"},
		{"dirty", []debug.BuildSetting{{Key: "vcs.revision", Value: "abc123"}, {Key: "vcs.modified", Value: "true"}}, "abc123+dirty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commitFrom(tc.settings); got != tc.want {
				t.Fatalf("commitFrom = %q, want %q", got, tc.want)
			}
		})
	}
}
