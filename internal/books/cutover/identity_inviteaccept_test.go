// file: internal/books/cutover/identity_inviteaccept_test.go
// version: 0.1.0
// guid: 3c8e5b17-9d42-4a60-b1f7-e25a08d63c94
// last-edited: 2026-11-22

package cutover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// A HEAD invite that was USED inside the 30 days HEAD's Users page lists keeps its
// place in that list across M3 (F4-08 B10x2). Under core identity an invite is core's
// and M3 deletes HEAD's, so without the carry every guest who accepted shortly before
// the flip would vanish from the owner's view of who got in and when. M3 writes
// books' own acceptedinvite: row for each, in the chunk that deletes the invite, keyed
// by HEAD's derived invite ID (never the token), with HEAD's own values.
const iaPrefix = "acceptedinvite:"

var iaDay = 24 * time.Hour

// iaToken makes a token no real invitation has; the "r2" in the prefix keeps a scanner
// from reading it as a credential.
func iaToken(name string) string { return "ia-fixture-token-" + name + "-0123456789abcdef" }

// iaInvite is a HEAD invite row (invite:<token>) used usedAgo before M3's clock
// (idNow). usedAgo < 0 makes it an open invite.
func iaInvite(name string, usedAgo time.Duration, extra map[string]any) seedRow {
	v := map[string]any{
		"token": iaToken(name), "username": name, "role_id": "viewer", "created_by_user_id": uAdmin,
		"created_at": idNow.Add(-90 * iaDay).Format(time.RFC3339Nano), "expires_at": idNow.Add(30 * iaDay).Format(time.RFC3339Nano),
	}
	if usedAgo >= 0 {
		v["used_at"] = idNow.Add(-usedAgo).Format(time.RFC3339Nano)
	}
	maps.Copy(v, extra)
	return seedRow{"invite:" + iaToken(name), v}
}

// iaFull is every member HEAD records on an invite that bound its address and ran the
// sync, for the exact-member-set cell.
func iaFull(name string, usedAgo time.Duration) seedRow {
	return iaInvite(name, usedAgo, map[string]any{
		"email": name + "@example.test", "accepted_user_id": uBarbara,
		"email_binding": store.InviteEmailBound, "sso_grant": store.InviteSSOGranted,
		"displaced_user_ids":  []string{uDick, uHarleen},
		"guest_policy_status": store.InviteGuestPolicyFailed, "guest_policy_detail": "the Access policy refused the update",
		"guest_policy_at": idNow.Add(-usedAgo + 3*time.Second).Format(time.RFC3339Nano),
	})
}

// iaRows is the accepted-invite rows this file's fixtures account for: the corpus
// carries invites of its own (users.invited, accepted inside the window on the
// cutover's clock), whose rows are not what a cell is asking about.
func iaRows(snap *idSnap) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range snap.Legacy {
		if strings.HasPrefix(k, iaPrefix) && iaOurs(k) {
			out[k] = v
		}
	}
	return out
}

var iaNames = []string{"barbara.gordon", "dick.grayson", "edge.in", "edge.out", "long.ago", "never.used", "just.now", "old.timer", "pending.invitee", "harvey.dent"}

func iaOurs(key string) bool {
	for _, n := range iaNames {
		if key == iaKey(n) {
			return true
		}
	}
	return false
}

func iaKey(name string) string { return iaPrefix + store.InviteID(iaToken(name)) }

func iaRun(t *testing.T, rows ...seedRow) (*idSnap, *idRig) {
	t.Helper()
	r := newIDRig(t, "stage1", idRows(rows...))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	return snap, r
}

// A used invite inside the window is carried with exactly HEAD's members: the exact
// member set of the stored row, each value HEAD's own, the accept time HEAD's UsedAt.
func TestInviteAcceptCarry_WithinWindowCarriedWithHEADsValues(t *testing.T) {
	snap, r := iaRun(t, iaFull("barbara.gordon", 5*iaDay))
	raw, ok := snap.Legacy[iaKey("barbara.gordon")]
	if !ok {
		t.Fatalf("no accepted-invite row for an invite used 5 days before the flip; the carried rows are %v\n%s", mapKeys(iaRows(snap)), r.logs.String())
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range members {
		got = append(got, k)
	}
	slices.Sort(got)
	want := []string{"accepted_at", "displaced_user_ids", "email", "email_binding", "guest_policy_at", "guest_policy_detail", "guest_policy_status",
		"invite_id", "sso_grant", "user_id", "username"}
	if !slices.Equal(got, want) {
		t.Fatalf("members = %v, want exactly %v", got, want)
	}
	var a store.InviteAccept
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	usedAt := idNow.Add(-5 * iaDay)
	if a.InviteID != store.InviteID(iaToken("barbara.gordon")) || a.UserID != uBarbara || a.Username != "barbara.gordon" ||
		a.Email != "barbara.gordon@example.test" || !a.AcceptedAt.Equal(usedAt) ||
		a.EmailBinding != store.InviteEmailBound || a.SSOGrant != store.InviteSSOGranted ||
		!slices.Equal(a.DisplacedUserIDs, []string{uDick, uHarleen}) ||
		a.GuestPolicyStatus != store.InviteGuestPolicyFailed || a.GuestPolicyDetail != "the Access policy refused the update" ||
		a.GuestPolicyAt == nil || !a.GuestPolicyAt.Equal(usedAt.Add(3*time.Second)) {
		t.Fatalf("row = %s", raw)
	}
	// The invite itself is gone, as before.
	if _, ok := snap.Legacy["invite:"+iaToken("barbara.gordon")]; ok {
		t.Errorf("the invite row survived")
	}
}

// An accept that named no address is carried too: HEAD listed it (username and time).
func TestInviteAcceptCarry_AnInviteWithNoAddressIsCarried(t *testing.T) {
	snap, _ := iaRun(t, iaInvite("dick.grayson", 2*iaDay, map[string]any{"accepted_user_id": uDick}))
	var a store.InviteAccept
	raw := snap.Legacy[iaKey("dick.grayson")]
	if raw == nil {
		t.Fatalf("no row for an accepted invite with no address")
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.Username != "dick.grayson" || a.UserID != uDick || a.Email != "" || a.EmailBinding != "" || a.SSOGrant != "" {
		t.Fatalf("row = %s (%v)", raw, err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"email", "email_binding", "sso_grant", "displaced_user_ids", "guest_policy_status", "guest_policy_detail", "guest_policy_at"} {
		if _, ok := m[k]; ok {
			t.Errorf("the row has a %q member HEAD's invite did not have", k)
		}
	}
}

// The window is HEAD's: an invite used exactly at its edge is carried (HEAD skipped a
// row only when UsedAt was before the edge), one a second older is not, and an open
// invite never is. Every invite row is deleted either way.
func TestInviteAcceptCarry_OutsideTheWindowIsNotCarried(t *testing.T) {
	snap, _ := iaRun(t,
		iaInvite("edge.in", 30*iaDay, nil),
		iaInvite("edge.out", 30*iaDay+time.Second, nil),
		iaInvite("long.ago", 200*iaDay, nil),
		iaInvite("never.used", -1, nil),
		iaInvite("just.now", 0, nil),
	)
	rows := iaRows(snap)
	for name, want := range map[string]bool{"edge.in": true, "edge.out": false, "long.ago": false, "never.used": false, "just.now": true} {
		if _, got := rows[iaKey(name)]; got != want {
			t.Errorf("%s: carried = %v, want %v", name, got, want)
		}
		if _, ok := snap.Legacy["invite:"+iaToken(name)]; ok {
			t.Errorf("%s: the invite row survived", name)
		}
	}
	if len(rows) != 2 {
		t.Errorf("%d rows carried (%v), want 2", len(rows), mapKeys(rows))
	}
}

// The family never holds a token or a secret: no row carries any invite's token, in a
// member or in its key, and no invite row (decodable or not) is kept anywhere M3
// writes, quarantine included.
func TestInviteAcceptCarry_NoRowHoldsATokenOrASecret(t *testing.T) {
	const bad = "invite:ia-fixture-token-undecodable-0123456789abcdef"
	snap, r := iaRun(t, iaFull("barbara.gordon", 5*iaDay), iaInvite("dick.grayson", 1*iaDay, map[string]any{"accepted_user_id": uDick}),
		seedRow{bad, "{this is not json"})
	toks := []string{iaToken("barbara.gordon"), iaToken("dick.grayson"), "ia-fixture-token-undecodable"}
	for k, v := range iaRows(snap) {
		for _, tok := range toks {
			if strings.Contains(k, tok) || strings.Contains(string(v), tok) {
				t.Errorf("%s names an invite token", k)
			}
		}
		var m map[string]any
		if err := json.Unmarshal(v, &m); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"token", "invite_token", "password", "secret", "token_hash"} {
			if _, ok := m[name]; ok {
				t.Errorf("%s has a %q member", k, name)
			}
		}
	}
	for _, q := range snap.Quarantine {
		if strings.Contains(q.Key, "ia-fixture-token") || strings.Contains(q.Value, "ia-fixture-token") {
			t.Errorf("an invite row was quarantined: %s", q.Key)
		}
	}
	if strings.Contains(r.logs.String(), "ia-fixture-token") {
		t.Errorf("the log names an invite token:\n%s", r.logs.String())
	}
	if _, ok := snap.Legacy[bad]; ok {
		t.Errorf("the undecodable invite row survived: it keeps a raw token")
	}
	if len(snap.Verify.Failures) != 0 {
		t.Errorf("M3's own verify failed: %v", snap.Verify.Failures)
	}
}

// A row that does not decode is deleted as every invite row is, not carried, and named
// in the log without its key (the key is the token).
func TestInviteAcceptCarry_AnUndecodableInviteIsDeletedAndReported(t *testing.T) {
	snap, r := iaRun(t, seedRow{"invite:ia-fixture-token-undecodable-0123456789abcdef", "{this is not json"},
		iaInvite("dick.grayson", 1*iaDay, map[string]any{"accepted_user_id": uDick}))
	if got := len(iaRows(snap)); got != 1 {
		t.Errorf("%d rows carried, want only the decodable invite's", got)
	}
	if !strings.Contains(r.logs.String(), "an invite row does not decode") {
		t.Errorf("the undecodable invite was not named in the log:\n%s", r.logs.String())
	}
}

// The key is the store's own InviteID of the token, and the row decodes strictly into
// the store's InviteAccept: the frozen copies in m3 cannot drift from either unseen.
func TestInviteAcceptCarry_KeyIsStoreInviteID_RowReadsBackThroughTheStore(t *testing.T) {
	snap, _ := iaRun(t, iaFull("barbara.gordon", 5*iaDay))
	if want := iaPrefix + store.InviteID(iaToken("barbara.gordon")); snap.Legacy[want] == nil {
		t.Fatalf("no row at %s; have %v", want, mapKeys(iaRows(snap)))
	}
	raw := snap.Legacy[iaKey("barbara.gordon")]
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var a store.InviteAccept
	if err := dec.Decode(&a); err != nil {
		t.Fatalf("the carried row has a member store.InviteAccept does not: %v\n%s", err, raw)
	}
}

// M3 killed at any point and booted again ends with the same accepted-invite rows an
// uninterrupted run leaves: a re-run of a chunk rewrites the same bytes (the key is the
// invite's ID, one row per invite), and an invite whose chunk committed is not carried
// a second time because its row is gone.
func TestInviteAcceptCarry_ResumedRunYieldsTheSameRows(t *testing.T) {
	seed := idRows(iaFull("barbara.gordon", 5*iaDay), iaInvite("dick.grayson", 1*iaDay, map[string]any{"accepted_user_id": uDick}),
		iaInvite("old.timer", 90*iaDay, nil), iaInvite("pending.invitee", -1, nil), iaFull("harvey.dent", 29*iaDay))
	want, err := newIDRig(t, "stage1", seed).run(t)
	if err != nil {
		t.Fatal(err)
	}
	wantRows := iaRows(want)
	if len(wantRows) != 3 {
		t.Fatalf("the uninterrupted run carried %d rows (%v), want 3", len(wantRows), mapKeys(wantRows))
	}
	var total int64
	{
		r := newIDRig(t, "stage1", seed)
		dm := r.migration()
		var calls atomic.Int64
		step := dm.Step
		dm.Step = func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			calls.Add(1)
			return step(ctx, kv, w)
		}
		if err := r.boot(context.Background(), r.newMem(t), []datamigrate.DataMigration{dm}, nil); err != nil {
			t.Fatal(err)
		}
		total = calls.Load()
	}
	for n := int64(1); n <= total; n++ {
		r := newIDRig(t, "stage1", seed)
		mem := r.newMem(t)
		if err := r.boot(context.Background(), mem, []datamigrate.DataMigration{killAt(r.migration(), n)}, nil); !errors.Is(err, errKilled) {
			t.Fatalf("kill at call %d of %d: the boot returned %v", n, total, err)
		}
		got, err := r.runOn(t, mem)
		if err != nil {
			t.Fatalf("kill at call %d: the boot after it failed: %v\n%s", n, err, r.logs.String())
		}
		gotRows := iaRows(got)
		if !slices.Equal(mapKeys(gotRows), mapKeys(wantRows)) {
			t.Fatalf("kill at call %d of %d: rows %v, want %v", n, total, mapKeys(gotRows), mapKeys(wantRows))
		}
		for k, v := range wantRows {
			if !bytes.Equal(gotRows[k], v) {
				t.Fatalf("kill at call %d of %d: %s is %s, want %s", n, total, k, gotRows[k], v)
			}
		}
	}
}

// The carry's window is the one HEAD's list used, so a row M3 carries is a row the
// list will still show on the day of the flip.
func TestAcceptedInviteWindow_IsThirtyDays(t *testing.T) {
	if m3.AcceptedInviteWindow != 30*24*time.Hour {
		t.Fatalf("m3.AcceptedInviteWindow = %v, want 30 days (HEAD's accepted list window)", m3.AcceptedInviteWindow)
	}
}
