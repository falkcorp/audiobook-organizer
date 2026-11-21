// file: internal/usermerge/owner_same_email_test.go
// version: 0.1.0
// guid: eeef5ee0-9b78-4634-a9bb-4f9f683b0c15
// last-edited: 2026-11-21

package usermerge

import (
	"context"
	"errors"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// F4-08 B10b2 (same-email pins): two admins who share the owner's e-mail, one of
// them linked to Cloudflare Access (the one whose sign-in the owner proves
// ownership with). Whatever else the merge becomes under core, these hold:
//
//   - merging the linked account away needs the explicit acknowledgement, and a
//     refused apply writes nothing;
//   - merging the other account away into the linked one does not;
//   - nothing merges them without an admin's apply: a plan, and a refused apply,
//     leave both accounts active and unmerged.
//
// The plan decides ack from the linked sign-in's address against owner_email,
// not from the accounts' own e-mail fields, so sharing the address cannot hide
// the linked account from it.

// sameEmailFixture is the fixture with its seeded sign-ins replaced: a is the
// unlinked admin, b the Access-linked one, and both carry the owner's address.
func sameEmailFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	for _, k := range []string{"oauthid:idA", "idx:oauth:user:" + f.a.ID + ":idA", "oauthid:idB",
		"idx:oauth:user:" + f.b.ID + ":idB", "idx:oauth:sub:google:google-67890"} {
		if err := f.s.DeleteRaw(k); err != nil {
			t.Fatal(err)
		}
	}
	f.setEmail(f.a.ID, ownerAddr, "")
	f.setEmail(f.b.ID, ownerAddr, "")
	f.linkAccess(f.b.ID)
	return f
}

// linkAccess links user to Cloudflare Access with the owner's address.
func (f *fixture) linkAccess(user string) {
	f.t.Helper()
	f.put("oauthid:idCF", database.OAuthIdentity{ID: "idCF", Provider: "cfaccess", Subject: "cf-sub-bruce", Email: ownerAddr, UserID: user, CreatedAt: f.t0})
	f.put("idx:oauth:user:"+user+":idCF", "1")
	f.put("idx:oauth:sub:cfaccess:cf-sub-bruce", "idCF")
}

func (f *fixture) mergeOf(absorbed, survivor string, ack bool) (*Plan, *Journal, error) {
	f.t.Helper()
	ctx := context.Background()
	plan, err := f.svc.BuildPlan(ctx, PlanRequest{ActorID: survivor, SurvivorID: survivor, AbsorbedID: absorbed})
	if err != nil {
		f.t.Fatal(err)
	}
	j, err := f.svc.Apply(ctx, ApplyRequest{ActorID: survivor, SurvivorID: survivor, AbsorbedID: absorbed,
		Digest: plan.Digest, AcknowledgeOwnerIdentity: ack}, nil)
	return plan, j, err
}

func TestSameEmailAdmins_MergingTheLinkedAccountAwayNeedsTheAcknowledgement(t *testing.T) {
	f := sameEmailFixture(t)
	plan, _, err := f.mergeOf(f.b.ID, f.a.ID, false)
	if !errors.Is(err, ErrOwnerIdentityNotAcknowledged) {
		t.Fatalf("apply without the acknowledgement = %v, want ErrOwnerIdentityNotAcknowledged", err)
	}
	if plan.OwnerIdentityHolder != HolderAbsorbed || !plan.OwnerIdentityOnAbsorbed || !plan.OwnerIdentityAckRequired {
		t.Fatalf("plan holder %q onAbsorbed=%v ack=%v, want the linked account to be the absorbed holder needing the acknowledgement",
			plan.OwnerIdentityHolder, plan.OwnerIdentityOnAbsorbed, plan.OwnerIdentityAckRequired)
	}
	for _, id := range []string{f.a.ID, f.b.ID} {
		if u, _ := f.s.GetUserByID(id); u.MergedIntoUserID != "" || u.Status != "active" {
			t.Fatalf("a refused apply changed account %s: merged into %q, status %q", id, u.MergedIntoUserID, u.Status)
		}
	}
	if _, j, err := f.mergeOf(f.b.ID, f.a.ID, true); err != nil || j == nil {
		t.Fatalf("apply with the acknowledgement = %v", err)
	}
}

func TestSameEmailAdmins_MergingTheOtherDirectionNeedsNoAcknowledgement(t *testing.T) {
	f := sameEmailFixture(t)
	plan, j, err := f.mergeOf(f.a.ID, f.b.ID, false)
	if err != nil || j == nil {
		t.Fatalf("merging the unlinked account into the linked one = %v, want it to need no acknowledgement", err)
	}
	if plan.OwnerIdentityHolder != HolderSurvivor || plan.OwnerIdentityAckRequired {
		t.Fatalf("plan holder %q ack=%v, want the survivor to hold the owner identity and no acknowledgement required",
			plan.OwnerIdentityHolder, plan.OwnerIdentityAckRequired)
	}
}

func TestSameEmailAdmins_NothingMergesThemWithoutAnApply(t *testing.T) {
	f := sameEmailFixture(t)
	before := f.snapshot()
	ctx := context.Background()
	for _, dir := range [][2]string{{f.a.ID, f.b.ID}, {f.b.ID, f.a.ID}} {
		if _, err := f.svc.BuildPlan(ctx, PlanRequest{ActorID: dir[1], SurvivorID: dir[1], AbsorbedID: dir[0]}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Footprint(dir[0]); err != nil {
			t.Fatal(err)
		}
	}
	if d := diff(before, f.snapshot()); len(d) != 0 {
		t.Fatalf("planning and footprints wrote %v", d)
	}
	for _, id := range []string{f.a.ID, f.b.ID} {
		if u, _ := f.s.GetUserByID(id); u.MergedIntoUserID != "" || u.Status != "active" {
			t.Fatalf("account %s was merged or disabled without an apply", id)
		}
	}
	// The linked sign-in still resolves to its own account.
	if got := f.get("idx:oauth:sub:cfaccess:cf-sub-bruce"); got != "idCF" {
		t.Fatalf("the Access sign-in index = %q", got)
	}
}
