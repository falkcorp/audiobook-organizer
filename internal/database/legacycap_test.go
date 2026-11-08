// file: internal/database/legacycap_test.go
// version: 1.0.0
// guid: 6e3a9d17-4f82-4b05-8c6e-2d7b1f9a3c48
// last-edited: 2026-11-08

package database

import "testing"

// TestNoNewLegacyMigrations is the ratchet at LegacyCap (05-storage §5S.3.5,
// DATA4-19): the HEAD-style track is exactly steps 1..LegacyCap, contiguous.
// A new books step is a storage.Migration numbered from LegacyCap+1, never
// another HEAD-style Migration; raising LegacyCap to admit one would make it
// a step that runs outside the atomic runner, which only the frozen legacy
// track may do.
func TestNoNewLegacyMigrations(t *testing.T) {
	if len(migrations) != LegacyCap {
		t.Fatalf("%d HEAD-style migrations, want exactly LegacyCap = %d: a new books step is a storage.Migration numbered from %d",
			len(migrations), LegacyCap, LegacyCap+1)
	}
	for i, m := range migrations {
		if m.Version != i+1 {
			t.Fatalf("HEAD-style migration %d has version %d; the legacy track is 1..%d, contiguous", i, m.Version, LegacyCap)
		}
		if m.Up == nil {
			t.Fatalf("HEAD-style migration %d has no Up", m.Version)
		}
	}
}
