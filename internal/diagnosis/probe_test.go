// file: internal/diagnosis/probe_test.go
// version: 1.0.1
// guid: e2f3a4b5-c6d7-8e9f-0a1b-2c3d4e5f6a7b
// last-edited: 2026-10-11

package diagnosis

import (
	"context"
	"testing"
)

// writeScript removed — helper unused; removed to satisfy staticcheck U1000

func TestDeriveFlags_Truncated(t *testing.T) {
	d := &FileDiagnostic{FFProbeErrorStr: "moov atom not found"}
	deriveFlags(d)
	if !d.IsTruncated {
		t.Error("expected IsTruncated=true for moov-not-found error")
	}
}

func TestDeriveFlags_ActiveDRM_ChannelError(t *testing.T) {
	d := &FileDiagnostic{FFProbeErrorStr: "channel element 2.1 is not allocated"}
	deriveFlags(d)
	if !d.HasActiveDRM {
		t.Error("expected HasActiveDRM=true for channel element error")
	}
}

func TestDeriveFlags_OriginallyDRM_InAudible(t *testing.T) {
	d := &FileDiagnostic{EncodedApplication: "inAudible 1.94"}
	deriveFlags(d)
	if !d.WasOriginallyDRM {
		t.Error("expected WasOriginallyDRM=true for inAudible encoder")
	}
}

func TestTruncate(t *testing.T) {
	s := truncate("abcdefgh", 4)
	if s != "abcd" {
		t.Errorf("got %q, want %q", s, "abcd")
	}
	if truncate("ab", 10) != "ab" {
		t.Error("should not truncate short string")
	}
}

// TestProbeFile_NonexistentPath verifies ProbeFile doesn't panic on a missing path.
func TestProbeFile_NonexistentPath(t *testing.T) {
	ctx := context.Background()
	d := ProbeFile(ctx, "/tmp/does-not-exist-audiobook-test-12345.m4b")
	// We only care that it doesn't panic; results depend on installed tools.
	_ = d
}
