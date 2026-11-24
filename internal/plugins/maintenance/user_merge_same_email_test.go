// file: internal/plugins/maintenance/user_merge_same_email_test.go
// version: 0.1.0
// guid: 00c1386a-8087-41c5-aace-e56f7a12420d
// last-edited: 2026-11-21

package maintenance

import (
	"context"
	"strings"
	"testing"
)

// F4-08 B10b2 (same-email pins): two admins sharing the owner's address, one of
// them linked to Cloudflare Access. The merge op is the only thing that can
// merge them and it refuses a live run that did not redeem the grant the owner
// route minted, so enqueueing it directly (what POST /operations/v2 can send)
// leaves both accounts active, unmerged and every key unchanged, in both
// directions.
func TestUserMergeOp_SameEmailAdminsNeedTheOwnerRouteGrantInEitherDirection(t *testing.T) {
	r := newUserMergeRig(t)
	const ownerAddr = "owner@example.com"
	for _, id := range []string{r.a.ID, r.b.ID} {
		u, err := r.store.GetUserByID(id)
		if err != nil {
			t.Fatal(err)
		}
		u.Email = ownerAddr
		if err := r.store.UpdateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	// b is the Access-linked account.
	if err := r.store.SetRaw("oauthid:idCF", []byte(`{"id":"idCF","provider":"cfaccess","subject":"cf-sub-bruce","email":"`+ownerAddr+`","user_id":"`+r.b.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetRaw("idx:oauth:user:"+r.b.ID+":idCF", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetRaw("idx:oauth:sub:cfaccess:cf-sub-bruce", []byte("idCF")); err != nil {
		t.Fatal(err)
	}
	before := r.keys()

	for name, p := range map[string]userMergeParams{
		"linked account absorbed":   r.live("", r.digest()),
		"unlinked account absorbed": swapped(r.live("", r.digest()), r),
	} {
		for tokName, tok := range map[string]string{"no token": "", "invented token": strings.Repeat("ab", 16)} {
			p.Grant = tok
			err := runUserMerge(context.Background(), p, r.env())
			if err == nil || !strings.Contains(err.Error(), "grant") {
				t.Errorf("%s, %s: err = %v, want a refusal naming the grant", name, tokName, err)
			}
			if !equalMaps(before, r.keys()) {
				t.Fatalf("%s, %s: a run without a valid grant wrote to the store", name, tokName)
			}
		}
	}
	for _, id := range []string{r.a.ID, r.b.ID} {
		if u, _ := r.store.GetUserByID(id); u.Status != "active" || u.MergedIntoUserID != "" {
			t.Errorf("account %s was touched: %+v", id, u)
		}
	}
}

// swapped is p with survivor and absorbed exchanged; the digest is not the
// swapped plan's, which is fine: the refusal under test comes before it.
func swapped(p userMergeParams, r *userMergeRig) userMergeParams {
	p.SurvivorID, p.AbsorbedID = r.b.ID, r.a.ID
	return p
}
