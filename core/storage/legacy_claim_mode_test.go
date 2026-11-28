// file: core/storage/legacy_claim_mode_test.go
// version: 1.0.0
// guid: 9a5677ce-4c00-4fa1-82a6-b401d5226521
// last-edited: 2026-11-28

package storage

import "testing"

// LegacyRange.ClaimMode answers from the claim set by longest match, and only
// for the handle's own owner.
func TestLegacyRangeClaimMode_LongestMatchOwnClaimsOnly(t *testing.T) {
	e := newEnv(t, atomicOwners()...)
	db := e.open()
	lr, ok := db.Legacy("gotham")
	if !ok {
		t.Fatal("no legacy range for gotham")
	}
	for _, tc := range []struct {
		key  string
		mode ClaimMode
		ok   bool
	}{
		{key: "joker:", mode: ClaimReadWrite, ok: true},
		{key: "joker:laugh", mode: ClaimReadWrite, ok: true},
		{key: "joker:retired:", mode: ClaimRetired, ok: true},
		{key: "joker:retired:card", mode: ClaimRetired, ok: true},
		{key: "joker_counter", mode: ClaimReadWrite, ok: true},
		{key: "arkham:cell", mode: ClaimReadOnly, ok: true},
		{key: "penguin:", mode: ClaimInvalid, ok: false},
	} {
		mode, ok := lr.ClaimMode([]byte(tc.key))
		if mode != tc.mode || ok != tc.ok {
			t.Errorf("ClaimMode(%q) = %v, %v; want %v, %v", tc.key, mode, ok, tc.mode, tc.ok)
		}
	}
	if mode, ok := (LegacyRange{}).ClaimMode([]byte("joker:")); ok || mode != ClaimInvalid {
		t.Errorf("a zero LegacyRange answered %v, %v", mode, ok)
	}
}
