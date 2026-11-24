// file: internal/books/cutover/identity_guestpolicy_test.go
// version: 0.1.0
// guid: 5e8b1d37-0a4c-4f62-b9d1-7c3a2f68e015
// last-edited: 2026-11-22

package cutover

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// A HEAD guest whose accept-time sync added the address to the Cloudflare
// guest policy (the grant's cf_added) keeps that fact across M3. Core's
// allow-list is by address alone and has no such field, so the step writes
// books' own guestpolicy: row in the chunk that imports the grant; without it a
// disable, delete or merge after the flip leaves the entry HEAD removes.
const (
	gpAdded   = "selina.kyle@example.test"
	gpHandAdd = "harvey.dent@example.test"
	gpMixed   = "Pamela.Isley@Example.Test"
)

func gpRows() []seedRow {
	return []seedRow{
		{"inviteallow:" + gpAdded, map[string]any{"user_id": uBarbara, "invite_token": "tok-gp-1", "granted_at": "2026-11-10T11:00:00Z", "cf_added": true}},
		// Somebody put this address on the policy by hand: no cf_added, no row.
		{"inviteallow:" + gpHandAdd, map[string]any{"user_id": uDick, "granted_at": "2026-11-10T11:00:00Z"}},
		// cf_added explicitly false is the same as absent.
		{"inviteallow:harvey.bullock@example.test", map[string]any{"user_id": uDick, "granted_at": "2026-11-10T11:00:00Z", "cf_added": false}},
	}
}

func TestGuestPolicyCarry_CFAddedGrantBecomesAGuestPolicyRow(t *testing.T) {
	r := newIDRig(t, "stage1", idRows(gpRows()...))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	raw, ok := snap.Legacy["guestpolicy:"+gpAdded]
	if !ok {
		t.Fatalf("no guestpolicy: row for the cf_added grant; the legacy range holds %d rows", len(snap.Legacy))
	}
	var row struct {
		UserID  string `json:"user_id"`
		CFAdded bool   `json:"cf_added"`
	}
	if err := json.Unmarshal(raw, &row); err != nil || row.UserID != uBarbara || !row.CFAdded {
		t.Fatalf("row = %s (%v), want user %s with cf_added", raw, err, uBarbara)
	}
	for _, k := range []string{"guestpolicy:" + gpHandAdd, "guestpolicy:harvey.bullock@example.test"} {
		if _, ok := snap.Legacy[k]; ok {
			t.Errorf("%s was written for a grant HEAD did not add to Cloudflare", k)
		}
	}
	// The grant itself is imported and deleted as before (the allow-list is core's).
	if _, ok := snap.Legacy["inviteallow:"+gpAdded]; ok {
		t.Errorf("the inviteallow: row survived")
	}
	var imported bool
	for _, a := range snap.Allow {
		imported = imported || a == gpAdded
	}
	if !imported {
		t.Errorf("the grant was not imported onto the allow-list: %v", snap.Allow)
	}
	// The raw invite token HEAD recorded is not copied anywhere.
	for k, v := range snap.Legacy {
		if strings.HasPrefix(k, "guestpolicy:") && strings.Contains(string(v), "tok-gp-1") {
			t.Errorf("the grant's invite token leaked into %s", k)
		}
	}
}

// The address is normalized the way the store normalizes it, so the row is the
// one the sync's lookup finds.
func TestGuestPolicyCarry_AddressIsNormalized(t *testing.T) {
	r := newIDRig(t, "stage1", idRows(
		seedRow{"inviteallow:" + gpMixed, map[string]any{"user_id": uDick, "granted_at": "2026-11-10T11:00:00Z", "cf_added": true}},
	))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	if _, ok := snap.Legacy["guestpolicy:pamela.isley@example.test"]; !ok {
		var have []string
		for k := range snap.Legacy {
			if strings.HasPrefix(k, "guestpolicy:") {
				have = append(have, k)
			}
		}
		t.Fatalf("no normalized guestpolicy: row; have %v", have)
	}
}

// The row M3 writes is read back by the store's own getter: the frozen JSON in
// m3 and store.guestPolicyRow cannot drift apart unseen.
func TestGuestPolicyCarry_RowReadsBackThroughTheStore(t *testing.T) {
	r := newIDRig(t, "stage1", idRows(gpRows()[0]))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	raw := snap.Legacy["guestpolicy:"+gpAdded]
	if raw == nil {
		t.Fatal("no guestpolicy: row")
	}
	s, err := store.NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SetRaw("guestpolicy:"+gpAdded, raw); err != nil {
		t.Fatal(err)
	}
	g, err := s.GuestPolicyGrants().GetInviteEmailGrant(gpAdded)
	if err != nil || g == nil || g.UserID != uBarbara || !g.CFAdded || g.GrantedAt.IsZero() {
		t.Fatalf("store read = %+v, %v; want %s with CFAdded and a grant time", g, err, uBarbara)
	}
}

// M3 over a store that already holds a guestpolicy: row (a crashed-and-resumed
// run, or an install whose sync already wrote one) neither imports it as a
// grant nor deletes it: it is outside M3's Source.
func TestGuestPolicyCarry_ExistingRowIsLeftAlone(t *testing.T) {
	const existing = "guestpolicy:bruce.wayne@example.test"
	row := map[string]any{"user_id": uDick, "invite_id": "inv-9", "granted_at": "2026-11-11T11:00:00Z", "cf_added": true}
	r := newIDRig(t, "stage1", idRows(seedRow{existing, row}))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	want, _ := json.Marshal(row)
	if got := snap.Legacy[existing]; string(got) != string(want) {
		t.Fatalf("the row is %q after M3, want it untouched (%s)", got, want)
	}
	for _, a := range snap.Allow {
		if a == "bruce.wayne@example.test" {
			t.Errorf("the guestpolicy: row was imported onto the allow-list")
		}
	}
	for _, q := range snap.Quarantine {
		if q.Key == existing {
			t.Errorf("the guestpolicy: row was quarantined: %s", q.Reason)
		}
	}
}
