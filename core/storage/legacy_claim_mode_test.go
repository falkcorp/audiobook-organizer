// file: core/storage/legacy_claim_mode_test.go
// version: 1.0.0
// guid: 9a5677ce-4c00-4fa1-82a6-b401d5226521
// last-edited: 2026-11-28

package storage

import "testing"

// LegacyRange.ClaimMode answers from the claim set by longest match, and only
// for the handle's own owner: a key inside another owner's claim is not the
// handle's (cobblepot retires penguin:, and gotham's handle must not answer
// that mode), and neither is a key nobody claims.
func TestLegacyRangeClaimMode_LongestMatchOwnClaimsOnly(t *testing.T) {
	owners := append(atomicOwners(), OwnerSpec{
		Owner: "cobblepot", Spec: Spec{Claims: []Claim{{Prefix: "penguin:", Mode: ClaimRetired}}}, Enabled: true,
	})
	e := newEnv(t, owners...)
	db := e.open()
	lr, ok := db.Legacy("gotham")
	if !ok {
		t.Fatal("no legacy range for gotham")
	}
	other, ok := db.Legacy("cobblepot")
	if !ok {
		t.Fatal("no legacy range for cobblepot")
	}
	if mode, ok := other.ClaimMode([]byte("penguin:umbrella")); !ok || mode != ClaimRetired {
		t.Errorf("cobblepot's own claim answered %v, %v; want %v, true", mode, ok, ClaimRetired)
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
		{key: "penguin:", mode: ClaimInvalid, ok: false},         // another owner's (retired) claim
		{key: "penguin:umbrella", mode: ClaimInvalid, ok: false}, // inside it
		{key: "riddler:", mode: ClaimInvalid, ok: false},         // nobody's claim
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
