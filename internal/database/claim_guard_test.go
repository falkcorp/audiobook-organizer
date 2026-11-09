// file: internal/database/claim_guard_test.go
// version: 1.0.3
// guid: ecd879ea-72b8-4adc-a3a5-5b0bfd525c33
// last-edited: 2026-11-09

package database

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/mediacore/storage"
)

// TestStandaloneClaimGuard (R1-23 review B1): in a test binary a standalone
// store's Close panics when books made an out-of-claim access, naming the
// series and the key, even though the caller swallowed the refusal; a store
// opened with ExpectClaimViolations, or one with no violation, closes
// quietly.
func TestStandaloneClaimGuard(t *testing.T) {
	open := func(t *testing.T, expect bool) *PebbleStore {
		t.Helper()
		s, err := OpenStandalone(filepath.Join(t.TempDir(), "gcpd.pebble"),
			StandaloneOptions{FS: storage.MemFS(), ExpectClaimViolations: expect})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	closeRecovering := func(s *PebbleStore) (msg string) {
		defer func() {
			if r := recover(); r != nil {
				msg, _ = r.(string)
				if msg == "" {
					msg = "non-string panic"
				}
			}
		}()
		_ = s.Close()
		return ""
	}

	t.Run("unexpected", func(t *testing.T) {
		s := open(t, false)
		// A best-effort writer that drops the error, as a swallowed refusal
		// would: only the guard can see it.
		if err := s.db.Set([]byte("arkham-unregistered:1"), []byte("x"), pebble.Sync); !errors.Is(err, storage.ErrOutsideKeyspace) {
			t.Fatalf("Set outside books' claims = %v, want ErrOutsideKeyspace", err)
		}
		msg := closeRecovering(s)
		// The key appears as core describes it. It is in no claim, so that is
		// its length and its access count, and nothing of the key itself
		// (R1-23 review SF1); the test author finds the write by the series.
		for _, want := range []string{"storage claim guard", "books write refused x1: unclaimed key len=21 x1", "keyfamilies.go"} {
			if !strings.Contains(msg, want) {
				t.Errorf("guard panic %q does not contain %q", msg, want)
			}
		}
		if strings.Contains(msg, "arkham") || strings.Contains(msg, "sha256") {
			t.Errorf("guard panic %q carries the key's bytes", msg)
		}
	})
	t.Run("expected", func(t *testing.T) {
		s := open(t, true)
		if _, _, err := s.db.Get([]byte("arkham-unregistered:1")); !errors.Is(err, storage.ErrOutsideKeyspace) {
			t.Fatalf("Get outside books' claims = %v, want ErrOutsideKeyspace", err)
		}
		if msg := closeRecovering(s); msg != "" {
			t.Errorf("ExpectClaimViolations store panicked at Close: %s", msg)
		}
	})
	t.Run("clean", func(t *testing.T) {
		s := open(t, false)
		if err := s.db.Set([]byte("book:batman-year-one"), []byte("x"), pebble.Sync); err != nil {
			t.Fatal(err)
		}
		if msg := closeRecovering(s); msg != "" {
			t.Errorf("a store with no violation panicked at Close: %s", msg)
		}
	})
}
