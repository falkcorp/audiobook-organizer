// file: internal/plugins/acoustid/fingerprint_rescan_guard_test.go
// version: 1.0.0
// guid: 91c3f5a8-2e64-4b07-a1d9-6f8e0b3c4725
// last-edited: 2026-10-09

package acoustid

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// With ALLOW_SERVER_DECODE unset the rescan must refuse before it reads the
// store or probes for fpcalc/ffmpeg. A nil store and nil reporter prove it:
// any later statement would return a different error or panic.
func TestFingerprintRescan_RefusesWithoutServerDecode(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	p := &Plugin{}
	var rep sdk.Reporter
	err := p.runFingerprintRescan(context.Background(), nil, rep)
	if !errors.Is(err, serverdecode.ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
}

// Positive control: with the switch on, the guard passes and the run proceeds to
// its next check (no store configured here).
func TestFingerprintRescan_GuardPassesWhenAllowed(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "1")
	p := &Plugin{}
	err := p.runFingerprintRescan(context.Background(), nil, nil)
	if err == nil || errors.Is(err, serverdecode.ErrRefused) {
		t.Fatalf("err = %v, want the post-guard store error", err)
	}
}
