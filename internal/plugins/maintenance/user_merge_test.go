// file: internal/plugins/maintenance/user_merge_test.go
// version: 1.0.0
// guid: d9e3a5f0-7b14-4c82-8a6d-2f1c0b9e4573
// last-edited: 2026-10-23

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

type userMergeRig struct {
	t      *testing.T
	store  *database.PebbleStore
	svc    *usermerge.Service
	grants *usermerge.Grants
	a, b   *database.User
	result any
	actor  string
	fresh  bool
}

func newUserMergeRig(t *testing.T) *userMergeRig {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.CreateUser("keeper", "keeper@admins.example.com", "bcrypt", "hash-a", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateUser("absorbed", "absorbed@admins.example.com", "bcrypt", "hash-b", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRaw("pref:"+b.ID+":speed", []byte(`{"key":"speed","value":"1.5"}`)); err != nil {
		t.Fatal(err)
	}
	svc, err := usermerge.New(s, usermerge.WithOwnerEmail(func() string { return "owner@example.com" }), usermerge.WithBackfillUserID(func() string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	return &userMergeRig{t: t, store: s, svc: svc, grants: usermerge.NewGrants(), a: a, b: b, actor: a.ID, fresh: true}
}

func (r *userMergeRig) env() userMergeEnv {
	return userMergeEnv{
		Service: r.svc, Grants: r.grants,
		Actor:     func() (string, bool, error) { return r.actor, r.fresh, nil },
		SetResult: func(v any) error { r.result = v; return nil },
		Progress:  func(int, int, string) {},
		Log:       func(slog.Level, string) {},
	}
}

func (r *userMergeRig) keys() map[string]string {
	pairs, err := r.store.ScanPrefix("")
	if err != nil {
		r.t.Fatal(err)
	}
	m := map[string]string{}
	for _, kv := range pairs {
		m[kv.Key] = string(kv.Value)
	}
	return m
}

func (r *userMergeRig) digest() string {
	p, err := r.svc.BuildPlan(context.Background(), usermerge.PlanRequest{ActorID: r.a.ID, SurvivorID: r.a.ID, AbsorbedID: r.b.ID})
	if err != nil || !p.Applicable() {
		r.t.Fatalf("plan: %v %v", err, p)
	}
	return p.Digest
}

func (r *userMergeRig) live(token, digest string) userMergeParams {
	no := false
	return userMergeParams{DryRun: &no, SurvivorID: r.a.ID, AbsorbedID: r.b.ID, Digest: digest, Grant: token}
}

func (r *userMergeRig) grant(actor string) (string, string) {
	d := r.digest()
	tok, err := r.grants.Issue(usermerge.Grant{ActorID: actor, Action: usermerge.ActionApply, SurvivorID: r.a.ID, AbsorbedID: r.b.ID, Digest: d})
	if err != nil {
		r.t.Fatal(err)
	}
	return tok, d
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestUserMergeOp_DirectEnqueueWithoutAGrantWritesNothing(t *testing.T) {
	r := newUserMergeRig(t)
	before := r.keys()
	// What POST /api/v1/operations/v2 can send: a live apply with the right ids and
	// digest but no token the merge route minted.
	for name, tok := range map[string]string{"no token": "", "invented token": "0123456789abcdef0123456789abcdef"} {
		err := runUserMerge(context.Background(), r.live(tok, r.digest()), r.env())
		if err == nil || !strings.Contains(err.Error(), "grant") {
			t.Errorf("%s: err = %v, want a refusal naming the grant", name, err)
		}
		if !equalMaps(before, r.keys()) {
			t.Fatalf("%s: a run without a valid grant wrote to the store", name)
		}
	}
	if u, _ := r.store.GetUserByID(r.b.ID); u.Status != "active" || u.MergedIntoUserID != "" {
		t.Errorf("the absorbed account was touched: %+v", u)
	}
}

func TestUserMergeOp_GrantIsBoundToTheActorTheDigestAndIsSingleUse(t *testing.T) {
	r := newUserMergeRig(t)
	other, err := r.store.CreateUser("other-admin", "other@admins.example.com", "bcrypt", "h", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	before := r.keys()

	// A token minted for one admin, redeemed by an op row of another.
	tok, d := r.grant(other.ID)
	if err := runUserMerge(context.Background(), r.live(tok, d), r.env()); err == nil {
		t.Error("a grant minted for another user was honoured")
	}
	// A token for one digest, an op carrying another.
	tok, _ = r.grant(r.a.ID)
	if err := runUserMerge(context.Background(), r.live(tok, "not-the-digest"), r.env()); err == nil {
		t.Error("a grant for a different digest was honoured")
	}
	// A run retried in place keeps its actor, so it is refused even with a grant.
	r.fresh = false
	tok, d = r.grant(r.a.ID)
	if err := runUserMerge(context.Background(), r.live(tok, d), r.env()); err == nil {
		t.Error("a retried run was honoured")
	}
	r.fresh = true
	if !equalMaps(before, r.keys()) {
		t.Fatal("a refused run wrote to the store")
	}

	// A good run applies, and its token cannot be replayed.
	tok, d = r.grant(r.a.ID)
	if err := runUserMerge(context.Background(), r.live(tok, d), r.env()); err != nil {
		t.Fatalf("a valid run: %v", err)
	}
	if u, _ := r.store.GetUserByID(r.b.ID); u.MergedIntoUserID != r.a.ID {
		t.Errorf("the absorbed account was not merged: %+v", u)
	}
	sum, ok := r.result.(usermerge.JournalSummary)
	if !ok || sum.Status != usermerge.StatusApplied {
		t.Errorf("result = %#v, want an applied journal summary", r.result)
	}
	if err := runUserMerge(context.Background(), r.live(tok, d), r.env()); err == nil {
		t.Error("a grant was redeemed twice")
	}
}

func TestUserMergeOp_OmittedModeIsAPreviewAndNeedsAnAdminActor(t *testing.T) {
	r := newUserMergeRig(t)
	before := r.keys()
	params := userMergeParams{SurvivorID: r.a.ID, AbsorbedID: r.b.ID} // dry_run omitted
	if err := runUserMerge(context.Background(), params, r.env()); err != nil {
		t.Fatalf("preview: %v", err)
	}
	plan, ok := r.result.(*usermerge.Plan)
	if !ok || plan.Digest == "" || plan.Ops == 0 {
		t.Fatalf("result = %#v, want a plan with ops", r.result)
	}
	if !equalMaps(before, r.keys()) {
		t.Fatal("a preview wrote to the store")
	}
	raw, _ := json.Marshal(plan)
	if strings.Contains(string(raw), `"ops":[`) {
		t.Error("the plan JSON exposes the raw ops")
	}

	viewer, err := r.store.CreateUser("viewer", "viewer@users.example.com", "bcrypt", "h", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	r.actor, r.result = viewer.ID, nil
	if err := runUserMerge(context.Background(), params, r.env()); err == nil || r.result != nil {
		t.Errorf("a preview for a non-admin actor: err=%v result=%#v, want a refusal and no result", err, r.result)
	}
	r.actor = ""
	if err := runUserMerge(context.Background(), params, r.env()); err == nil {
		t.Error("a preview with no actor on the op row was honoured")
	}
}
