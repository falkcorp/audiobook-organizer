// file: internal/database/pebble_store_abssession_sliding_test.go
// version: 1.0.0
// guid: 3a8617fc-1971-4c2c-96c2-a40298b4675e
// last-edited: 2026-10-19

package database

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// assertABSIndexInvariant fails when any stored abs_sess record is missing an
// index entry it needs: every record needs its per-user entry (revoke-all walks
// it), and every NON-revoked record needs its current refresh-token entry (refresh
// resolves through it; revocation deliberately drops it).
func assertABSIndexInvariant(t *testing.T, store *PebbleStore) {
	t.Helper()
	prefix := []byte(absSessionKeyPrefix)
	if err := forEachKeyInRange(store.db, prefix, prefixEnd(prefix), func(key, _ []byte) error {
		id := strings.TrimPrefix(string(key), absSessionKeyPrefix)
		s, err := store.GetABSSession(id)
		if err != nil || s == nil {
			return fmt.Errorf("record %s unreadable: %v", id, err)
		}
		if _, c, err := store.db.Get(absSessionUserKey(s.UserID, s.ID)); err != nil {
			t.Errorf("record %s has no per-user index entry (%v): invisible to revoke-all", id, err)
		} else {
			c.Close()
		}
		if !s.Revoked && s.RefreshTokenHash != "" {
			if _, c, err := store.db.Get(absSessionTokenKey(s.RefreshTokenHash)); err != nil {
				t.Errorf("live record %s has no refresh-token index entry (%v)", id, err)
			} else {
				c.Close()
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestABSSession_ExtendRules(t *testing.T) {
	store := newABSSessionStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	s := sampleABSSession("s1", "u1")
	s.ExpiresAt = now.Add(10 * 24 * time.Hour)
	if err := store.CreateABSSession(s); err != nil {
		t.Fatal(err)
	}

	// Less than minAdvance of gain: throttled, nothing written.
	wrote, err := store.ExtendABSSession("s1", now, s.ExpiresAt.Add(time.Hour), 24*time.Hour)
	if err != nil || wrote {
		t.Fatalf("an advance under the window must not write: wrote=%v err=%v", wrote, err)
	}
	// A real advance writes ExpiresAt and LastUsedAt and nothing else.
	target := now.Add(60 * 24 * time.Hour)
	if wrote, err = store.ExtendABSSession("s1", now.Add(time.Minute), target, 24*time.Hour); err != nil || !wrote {
		t.Fatalf("a day-plus advance must write: wrote=%v err=%v", wrote, err)
	}
	got, _ := store.GetABSSession("s1")
	if !got.ExpiresAt.Equal(target) || !got.LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("extend wrote ExpiresAt=%v LastUsedAt=%v", got.ExpiresAt, got.LastUsedAt)
	}
	if got.RefreshTokenHash != s.RefreshTokenHash || got.Generation != s.Generation {
		t.Fatal("extend must not touch any other field")
	}
	// Never shortens.
	if wrote, _ = store.ExtendABSSession("s1", now, now.Add(time.Hour), 0); wrote {
		t.Fatal("extend must never move ExpiresAt backwards")
	}
	// Expired: dead, never resurrected.
	if wrote, _ = store.ExtendABSSession("s1", target.Add(time.Second), target.Add(90*24*time.Hour), 0); wrote {
		t.Fatal("an expired session must not be extended")
	}
	// Revoked.
	if err := store.RevokeABSSession("s1"); err != nil {
		t.Fatal(err)
	}
	if wrote, _ = store.ExtendABSSession("s1", now, target.Add(30*24*time.Hour), 0); wrote {
		t.Fatal("a revoked session must not be extended")
	}
	if got, _ := store.GetABSSession("s1"); !got.Revoked {
		t.Fatal("extend un-revoked a session")
	}
	// Missing.
	if wrote, _ = store.ExtendABSSession("nope", now, target, 0); wrote {
		t.Fatal("a missing session must not be created by extend")
	}
	if got, _ := store.GetABSSession("nope"); got != nil {
		t.Fatal("extend resurrected a missing session")
	}
}

// TestABSSession_UpdateCannotUnrevoke is the refresh-vs-logout race: a refresh
// read the row, a logout revoked it, then the refresh writes its stale copy back.
func TestABSSession_UpdateCannotUnrevoke(t *testing.T) {
	store := newABSSessionStore(t)
	s := sampleABSSession("s1", "u1")
	if err := store.CreateABSSession(s); err != nil {
		t.Fatal(err)
	}
	stale, _ := store.GetABSSession("s1")
	if err := store.RevokeABSSession("s1"); err != nil {
		t.Fatal(err)
	}
	stale.Generation++
	stale.RefreshTokenHash = "hash-s1-2"
	if err := store.UpdateABSSession(stale); !errors.Is(err, ErrABSSessionRevoked) {
		t.Fatalf("update over a revoked row = %v, want ErrABSSessionRevoked", err)
	}
	got, _ := store.GetABSSession("s1")
	if !got.Revoked || got.RefreshTokenHash != "hash-s1-1" {
		t.Fatalf("revoked row was overwritten: %+v", got)
	}
	if hit, _ := store.GetABSSessionByRefreshHash("hash-s1-2"); hit != nil {
		t.Fatal("the stale refresh token was indexed for a revoked session")
	}
	// Deleted rows are not resurrected either.
	if err := store.UpdateABSSession(sampleABSSession("gone", "u1")); !errors.Is(err, ErrABSSessionRevoked) {
		t.Fatalf("update of a missing row = %v, want ErrABSSessionRevoked", err)
	}
	if got, _ := store.GetABSSession("gone"); got != nil {
		t.Fatal("update created a row that did not exist")
	}
}

// TestABSSession_SweepRechecksAfterExtend interleaves deterministically: the
// sweep nominates a session that is past expiry at the sweep's now, an extension
// (whose own clock still sees it live) slides it before the sweep deletes, and the
// sweep must notice and leave it alone with its indexes intact.
func TestABSSession_SweepRechecksAfterExtend(t *testing.T) {
	store := newABSSessionStore(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	s := sampleABSSession("s1", "u1")
	s.ExpiresAt = base
	if err := store.CreateABSSession(s); err != nil {
		t.Fatal(err)
	}
	slid := base.Add(60 * 24 * time.Hour)
	absSweepAfterScanHook = func() {
		wrote, err := store.ExtendABSSession("s1", base.Add(-time.Millisecond), slid, 0)
		if err != nil || !wrote {
			t.Errorf("extension inside the sweep window: wrote=%v err=%v", wrote, err)
		}
	}
	t.Cleanup(func() { absSweepAfterScanHook = nil })

	n, err := store.DeleteExpiredABSSessions(base.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("sweep deleted %d sessions; the candidate was slid before the delete and must survive", n)
	}
	got, _ := store.GetABSSession("s1")
	if got == nil || !got.ExpiresAt.Equal(slid) {
		t.Fatalf("slid session after sweep = %+v", got)
	}
	if hit, _ := store.GetABSSessionByRefreshHash(s.RefreshTokenHash); hit == nil {
		t.Fatal("the slid session lost its refresh-token index")
	}
	assertABSIndexInvariant(t, store)
}

// TestABSSession_SweepCannotBeUndoneByExtend is the resurrection direction: once
// the sweep has deleted an expired row, an extension must not write it back.
func TestABSSession_SweepCannotBeUndoneByExtend(t *testing.T) {
	store := newABSSessionStore(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	s := sampleABSSession("s1", "u1")
	s.ExpiresAt = base
	if err := store.CreateABSSession(s); err != nil {
		t.Fatal(err)
	}
	if n, err := store.DeleteExpiredABSSessions(base.Add(time.Millisecond)); err != nil || n != 1 {
		t.Fatalf("sweep: n=%d err=%v", n, err)
	}
	if wrote, _ := store.ExtendABSSession("s1", base.Add(-time.Millisecond), base.Add(time.Hour), 0); wrote {
		t.Fatal("extend resurrected a swept session")
	}
	if got, _ := store.GetABSSession("s1"); got != nil {
		t.Fatal("swept session is back")
	}
	var raw []byte
	if v, c, err := store.db.Get(absSessionKey("s1")); err == nil {
		raw = append(raw, v...)
		c.Close()
	} else if err != pebble.ErrNotFound {
		t.Fatal(err)
	}
	if raw != nil {
		t.Fatalf("raw record exists after sweep: %s", raw)
	}
}

// TestABSSession_ConcurrentExtendSweepRevoke hammers extensions against the sweep
// and revocation (run with -race). Sessions straddle the expiry instant so some
// are live to the extender and sweepable to the sweeper. Afterwards no record may
// be missing its indexes, and no revoked session may be live again.
func TestABSSession_ConcurrentExtendSweepRevoke(t *testing.T) {
	store := newABSSessionStore(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	const n = 64
	for i := range n {
		s := sampleABSSession(fmt.Sprintf("s%02d", i), fmt.Sprintf("u%d", i%4))
		s.ExpiresAt = base.Add(time.Duration(i%3-1) * time.Millisecond) // -1ms, 0, +1ms
		if err := store.CreateABSSession(s); err != nil {
			t.Fatal(err)
		}
	}
	revoked := map[string]bool{}
	for i := 0; i < n; i += 5 {
		revoked[fmt.Sprintf("s%02d", i)] = true
	}

	var wg sync.WaitGroup
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := range 20 {
				for i := range n {
					id := fmt.Sprintf("s%02d", i)
					_, _ = store.ExtendABSSession(id, base.Add(-2*time.Millisecond),
						base.Add(time.Duration(round+w+1)*time.Hour), 0)
				}
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 20 {
			_, _ = store.DeleteExpiredABSSessions(base)
		}
	}()
	go func() {
		defer wg.Done()
		for id := range revoked {
			_ = store.RevokeABSSession(id)
		}
		_, _ = store.RevokeAllABSSessionsForUser("u3")
	}()
	wg.Wait()

	assertABSIndexInvariant(t, store)
	for id := range revoked {
		if s, _ := store.GetABSSession(id); s != nil && s.Live(base) {
			t.Errorf("revoked session %s is live again", id)
		}
	}
	for i := range n {
		id := fmt.Sprintf("s%02d", i)
		s, _ := store.GetABSSession(id)
		if s != nil && i%4 == 3 && !s.Revoked {
			t.Errorf("session %s survived revoke-all for its user", id)
		}
	}
}
