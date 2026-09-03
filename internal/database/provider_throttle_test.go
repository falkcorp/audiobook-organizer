// file: internal/database/provider_throttle_test.go
// version: 1.0.0
// guid: b66678de-73cb-4f2b-8d84-94bea576add7
// last-edited: 2026-09-03

package database

import (
	"path/filepath"
	"testing"
)

func newThrottleTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProviderThrottle_RoundTrip(t *testing.T) {
	s := newThrottleTestStore(t)

	if err := s.SaveProviderThrottle("google-books", []byte(`{"reason":"daily-quota"}`)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.SaveProviderThrottle("hardcover", []byte(`{"reason":"auth"}`)); err != nil {
		t.Fatalf("save: %v", err)
	}

	rows, err := s.LoadProviderThrottles()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// The map key must be the bare provider id, not the prefixed Pebble key —
	// the registry looks holds up by provider id, so a leaked "provider_throttle:"
	// prefix would restore every hold under a name nothing ever queries.
	if got := string(rows["google-books"]); got != `{"reason":"daily-quota"}` {
		t.Fatalf("google-books payload = %q", got)
	}
	if _, ok := rows["hardcover"]; !ok {
		t.Fatalf("hardcover missing; keys = %v", keysOf(rows))
	}

	if err := s.DeleteProviderThrottle("google-books"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, err = s.LoadProviderThrottles()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := rows["google-books"]; ok {
		t.Fatal("deleted hold is still readable")
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
}

// Expiry and the manual reset both delete, and neither knows whether a row is
// there. Erroring on an absent key would turn a normal path into a logged
// failure.
func TestProviderThrottle_DeleteAbsentIsNotAnError(t *testing.T) {
	s := newThrottleTestStore(t)
	if err := s.DeleteProviderThrottle("never-existed"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
}

func TestProviderThrottle_EmptyIDRejected(t *testing.T) {
	s := newThrottleTestStore(t)
	if err := s.SaveProviderThrottle("  ", []byte("{}")); err == nil {
		t.Fatal("an empty provider id was accepted; it would write a key nothing can address")
	}
}

// The keyspace must not collect rows belonging to a neighbouring prefix.
func TestProviderThrottle_LoadIsScopedToItsPrefix(t *testing.T) {
	s := newThrottleTestStore(t)
	if err := s.SaveProviderThrottle("google-books", []byte("{}")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.SetUserPreference("provider_throttle_lookalike", "x"); err != nil {
		t.Fatalf("preference: %v", err)
	}
	rows, err := s.LoadProviderThrottles()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %v", len(rows), keysOf(rows))
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
