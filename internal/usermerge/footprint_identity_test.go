// file: internal/usermerge/footprint_identity_test.go
// version: 0.1.0
// guid: 9e3b71d8-4c26-4f05-a8d1-52b7c0e9f314
// last-edited: 2026-11-22

package usermerge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
)

// F4-08 B10fp: the footprint once accounts live in core. The service is real,
// the books store is real and seeded (fixture.seed leaves HEAD-era sign-in,
// key and invite rows on the absorbed account that core's numbers must override
// and that no answer may show), and core is fakeCore.

// AdminAccountFootprint is the stand-in for core's masked read: it needs a
// principal on ctx, as the real one does, records it, and answers the fixture.
func (c *fakeCore) AdminAccountFootprint(ctx context.Context, id coreauth.UserID) (coreauth.AccountFootprint, error) {
	p, ok := coreauth.PrincipalFrom(ctx)
	if !ok || p.User == nil {
		return coreauth.AccountFootprint{}, fmt.Errorf("%w: no principal", coreauth.ErrDenied)
	}
	c.mu.Lock()
	c.footprintSeen = append(c.footprintSeen, p)
	fp, err := c.footprint, c.footprintErr
	c.mu.Unlock()
	if err != nil {
		return coreauth.AccountFootprint{}, err
	}
	if c.user(string(id)) == nil {
		return coreauth.AccountFootprint{}, coreauth.ErrNotFound
	}
	return fp, nil
}

// coreFootprint is what the fixture's core says about the absorbed account: not
// the numbers the books store holds for it (one sign-in, one key, one invite,
// one grant, no session), so a footprint read from books shows.
func coreFootprint() coreauth.AccountFootprint {
	linked := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	return coreauth.AccountFootprint{
		Identities: []coreauth.IdentityLink{
			{Provider: "gotham-sso", SubjectMask: "su…42", Email: ownerAddr, LinkedAt: linked},
			{Provider: "gotham-sso", SubjectMask: "••••", Email: "robin@example.test", LinkedAt: linked.Add(time.Hour)},
		},
		APIKeys: 5, Invites: 3, InviteGrants: 0, ActiveSessions: 7,
	}
}

// The core-sourced parts: sign-ins masked as core gave them, the owner flag from
// the address, key, invite, grant and session counts from core, the account from
// core; and the books families from books' store.
func TestFootprintIdentity_ReadsCoreForWhatCoreOwnsAndBooksForTheRest(t *testing.T) {
	f := newIDFixture(t)
	f.core.footprint = coreFootprint()
	f.core.users[f.b.ID].Username = "robin-core"

	fp, err := f.svc.Footprint(f.ctx(), f.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fp.User.ID != f.b.ID || fp.User.Username != "robin-core" || fp.User.Email != "absorbed@admins.example.com" {
		t.Errorf("user = %+v, want core's account", fp.User)
	}
	for fam, want := range map[string]int{
		FamOAuthIdentities: 2, FamAPIKeys: 5, FamInvites: 3, FamInviteGrants: 0, // core's, not books' leftover 1, 1, 1, 1
		FamBookState: 2, FamPositions: 2, FamBookmarks: 2, FamPreferences: 2, FamPlaylists: 1, FamCollections: 1, // books'
	} {
		if got, ok := fp.Counts[fam]; !ok || got != want {
			t.Errorf("counts[%s] = %d (present %v), want %d: %v", fam, got, ok, want, fp.Counts)
		}
	}
	if fp.ActiveSessions != 7 {
		t.Errorf("ActiveSessions = %d, want core's 7", fp.ActiveSessions)
	}
	if len(fp.Identities) != 2 {
		t.Fatalf("identities = %+v", fp.Identities)
	}
	first, second := fp.Identities[0], fp.Identities[1]
	if first.Provider != "gotham-sso" || first.Subject != "su…42" || second.Subject != "••••" {
		t.Errorf("identities = %+v, want core's masked subjects in core's order", fp.Identities)
	}
	if !first.OwnerEmail || second.OwnerEmail {
		t.Errorf("owner flags = %v, %v; want true only for the owner's address", first.OwnerEmail, second.OwnerEmail)
	}
	if first.ID == "" || first.ID == second.ID {
		t.Errorf("identity ids %q, %q: want distinct and non-empty", first.ID, second.ID)
	}
	if !first.LinkedAt.Equal(coreFootprint().Identities[0].LinkedAt) || first.Email != ownerAddr {
		t.Errorf("identity %+v: linked_at or e-mail not carried from core", first)
	}
	// HEAD-era rows left in books' store are never shown: not read, not counted.
	raw, _ := json.Marshal(fp)
	for _, needle := range []string{credHash, credSubject, "gh-12345", "idB", "kB", "tokB", "friend@example.com"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("the footprint shows %q from books' retired rows: %s", needle, raw)
		}
	}
	if len(fp.Unreadable) != 0 || fp.Unreadable == nil {
		t.Errorf("Unreadable = %#v, want a non-nil empty list", fp.Unreadable)
	}
}

// What core could not read is added to what books could not read, so the admin
// sees both lists.
func TestFootprintIdentity_UnreadableIsBooksPlusCores(t *testing.T) {
	f := newIDFixture(t)
	f.put("upl:plBad", "{not json")
	cf := coreFootprint()
	cf.Unreadable = []string{"invite (undecodable)"}
	f.core.footprint = cf
	fp, err := f.svc.Footprint(f.ctx(), f.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fp.Unreadable, "invite (undecodable)") || len(fp.Unreadable) < 2 {
		t.Errorf("Unreadable = %v, want core's note beside books'", fp.Unreadable)
	}
}

// The refusal is gone: a flipped service that has core's handles answers the
// footprint, where HEAD's flip answered ErrUnavailableUnderIdentity. The same
// service without the handles still refuses (fail closed).
func TestFootprintIdentity_FlippedServiceAnswersInsteadOfRefusing(t *testing.T) {
	f := newIDFixture(t)
	f.core.footprint = coreFootprint()
	fp, err := f.svc.Footprint(f.ctx(), f.b.ID)
	if errors.Is(err, ErrUnavailableUnderIdentity) {
		t.Fatalf("a flipped service with core's handles refuses the footprint: %v", err)
	}
	if err != nil || fp == nil {
		t.Fatalf("Footprint = (%v, %v)", fp, err)
	}
	bare, err := New(f.s, WithIdentityFlipped(func() bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.Footprint(f.ctx(), f.b.ID); !errors.Is(err, ErrUnavailableUnderIdentity) {
		t.Errorf("a flipped service without core's handles = %v, want %v", err, ErrUnavailableUnderIdentity)
	}
}

// Core is asked as the request's own principal, and its denial and its
// not-found come out as themselves (a refused read shows nothing).
func TestFootprintIdentity_AsksCoreAsTheRequestsPrincipal(t *testing.T) {
	f := newIDFixture(t)
	f.core.footprint = coreFootprint()
	if _, err := f.svc.Footprint(f.ctx(), f.b.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.core.footprintSeen) != 1 || f.core.footprintSeen[0] != f.pr {
		t.Fatalf("core saw %v, want the request's principal %p", f.core.footprintSeen, f.pr)
	}

	fp, err := f.svc.Footprint(context.Background(), f.b.ID)
	if !errors.Is(err, coreauth.ErrDenied) || fp != nil {
		t.Errorf("no principal: (%v, %v), want ErrDenied and no footprint", fp, err)
	}
	f.core.footprintErr = fmt.Errorf("%w: needs core:admin", coreauth.ErrDenied)
	if fp, err := f.svc.Footprint(f.ctx(), f.b.ID); !errors.Is(err, coreauth.ErrDenied) || fp != nil {
		t.Errorf("core's denial: (%v, %v), want ErrDenied and no footprint", fp, err)
	}
	f.core.footprintErr = nil
	if _, err := f.svc.Footprint(f.ctx(), "u-nobody"); !errors.Is(err, database.ErrUserNotFound) {
		t.Errorf("an absent account = %v, want ErrUserNotFound", err)
	}
}

// The identity footprint has HEAD's shape: the same member names at every level
// that has fixed names (the top level, counts, an identity) and the same value
// types.
func TestFootprintIdentity_HasHEADsShape(t *testing.T) {
	head := newFixture(t)
	headFP, err := head.svc.Footprint(context.Background(), head.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := newIDFixture(t)
	id.core.footprint = coreFootprint()
	idFP, err := id.svc.Footprint(id.ctx(), id.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	shape := func(fp *Footprint) map[string]any {
		raw, _ := json.Marshal(fp)
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	h, i := shape(headFP), shape(idFP)
	keys := func(m map[string]any) []string { k := slices.Collect(maps.Keys(m)); slices.Sort(k); return k }
	if !slices.Equal(keys(h), keys(i)) {
		t.Fatalf("top-level members: HEAD %v, identity %v", keys(h), keys(i))
	}
	for k := range h {
		if fmt.Sprintf("%T", h[k]) != fmt.Sprintf("%T", i[k]) {
			t.Errorf("%s: HEAD is %T, identity is %T", k, h[k], i[k])
		}
	}
	if !slices.Equal(keys(h["counts"].(map[string]any)), keys(i["counts"].(map[string]any))) {
		t.Errorf("counts members: HEAD %v, identity %v", keys(h["counts"].(map[string]any)), keys(i["counts"].(map[string]any)))
	}
	hi, ii := h["identities"].([]any)[0].(map[string]any), i["identities"].([]any)[0].(map[string]any)
	if !slices.Equal(keys(hi), keys(ii)) {
		t.Errorf("identity members: HEAD %v, identity %v", keys(hi), keys(ii))
	}
	// The user's optional members are omitempty (email, email_source,
	// merged_into_user_id): core's user has no e-mail source, so email_source is
	// absent under identity exactly as in the identity-era merge plan
	// (summarizeCore). Every member that is always present must match.
	optional := map[string]bool{"email": true, "email_source": true, "merged_into_user_id": true}
	always := func(m map[string]any) []string {
		return slices.DeleteFunc(keys(m), func(k string) bool { return optional[k] })
	}
	hu, iu := h["user"].(map[string]any), i["user"].(map[string]any)
	if !slices.Equal(always(hu), always(iu)) {
		t.Errorf("user members: HEAD %v, identity %v", always(hu), always(iu))
	}
	for k := range iu {
		if _, ok := hu[k]; !ok {
			t.Errorf("identity user member %q is not one HEAD has", k)
		}
	}
}

// Core's mask is HEAD's mask, byte for byte, so a sign-in reads the same either
// side of the flip (core cannot import usermerge, and usermerge keeps its own
// copy for the pre-flip path).
func TestMaskSubject_CoreMatchesHEAD(t *testing.T) {
	for _, s := range []string{"", "a", "ab", "abcd", "abcde", "abcdef", "gotham-guest-subject-99",
		"é", "éé", "éééé", "ééééé", "gotham•city"} {
		if got, want := coreauth.MaskSubject(s), maskSubject(s); got != want {
			t.Errorf("MaskSubject(%q) = %q, HEAD's maskSubject = %q", s, got, want)
		}
	}
}
