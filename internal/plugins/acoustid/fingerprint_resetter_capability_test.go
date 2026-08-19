// file: internal/plugins/acoustid/fingerprint_resetter_capability_test.go
// version: 1.0.0
// guid: 3a7f5e91-8b26-4c04-97d1-5e8a2b4f6c73
// last-edited: 2026-08-19

package acoustid

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type resetCapableStore struct {
	database.Store
}

func (resetCapableStore) ClearAllAcoustIDFingerprints(context.Context, int, func(int, int, int)) (int, int, error) {
	return 3, 3, nil
}

type resetDecorator struct {
	database.Store
	inner database.Store
}

func (d resetDecorator) Unwrap() database.Store { return d.inner }

// TestResolveFingerprintResetterThroughDecorator pins the batched wipe.
// ClearAllAcoustIDFingerprints is not on database.Store (compile-probed), so a
// bare assertion fails through the production decorator — which is how this op
// degraded to the one-row-at-a-time path.
func TestResolveFingerprintResetterThroughDecorator(t *testing.T) {
	got := resolveFingerprintResetter(resetDecorator{inner: resetCapableStore{}})
	if got == nil {
		t.Fatal("resolveFingerprintResetter returned nil through the decorator; the reset would fall back to per-row deletes")
	}
	c, total, err := got.ClearAllAcoustIDFingerprints(context.Background(), 10, nil)
	if err != nil || c != 3 || total != 3 {
		t.Fatalf("ClearAllAcoustIDFingerprints = (%d, %d, %v), want (3, 3, nil)", c, total, err)
	}
}

func TestResolveFingerprintResetterOnUncapableBackend(t *testing.T) {
	type plain struct{ database.Store }
	if got := resolveFingerprintResetter(plain{}); got != nil {
		t.Fatalf("resolveFingerprintResetter = %v without the batched wipe, want nil", got)
	}
}
