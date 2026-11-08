// file: internal/database/booksspec_fresh_test.go
// version: 1.0.0
// guid: 3d6f0b81-2e47-4c9a-b5d8-7f1a0c9e2b64
// last-edited: 2026-11-08

package database

import (
	"fmt"
	"slices"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// TestFreshInstallMatchesReplayedLegacySteps: a fresh core install records
// books' steps 1..LegacyCap+2 applied without running them (§5S.4 step 6),
// where HEAD ran all 66 on an empty store. Replaying every legacy Up and the
// two versioned steps over the fresh store must therefore write nothing a
// served store needs: the keys the replay adds are listed here, each with
// why a fresh store does without it.
func TestFreshInstallMatchesReplayedLegacySteps(t *testing.T) {
	s, err := NewPebbleStoreInMemory("/fresh-diff")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	before := booksKeys(t, s)
	for _, m := range migrations {
		if err := m.Up(s); err != nil {
			t.Fatalf("replay migration %d: %v", m.Version, err)
		}
	}
	after := booksKeys(t, s)
	var added []string
	for _, k := range after {
		if !slices.Contains(before, k) {
			added = append(added, k)
		}
	}
	// The enumerated delta: each key the replay adds, and why a fresh
	// install does without it.
	allowed := map[string]func() error{
		// Migration 065 writes the opv3 counts it rebuilt; on an empty store
		// they are all zero, which is what an absent key reads as
		// (readOpCountsV3).
		opv3MetaCountsKey: func() error {
			c, err := s.OpCountsV3()
			if err != nil {
				return err
			}
			for st, n := range c.ByState {
				if n != 0 {
					return fmt.Errorf("by state %v = %d", st, n)
				}
			}
			for d, n := range c.ByDef {
				if n != 0 {
					return fmt.Errorf("by def %s = %d", d, n)
				}
			}
			return nil
		},
	}
	for _, k := range added {
		check, ok := allowed[k]
		if !ok {
			t.Errorf("replaying HEAD's migrations on a fresh core install adds %q, which a fresh install lacks", k)
			continue
		}
		if err := check(); err != nil {
			t.Errorf("replayed %q differs from its absent reading: %v", k, err)
		}
	}
}

// booksKeys lists every key books can see.
func booksKeys(t *testing.T, s *PebbleStore) []string {
	t.Helper()
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte{0}, UpperBound: []byte{0xff, 0xff}})
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()
	var out []string
	for ok := it.First(); ok; ok = it.Next() {
		out = append(out, string(it.Key()))
	}
	if err := it.Error(); err != nil {
		t.Fatal(err)
	}
	return out
}
