// file: internal/server/handlers/user_merge_test.go
// version: 1.0.0
// guid: 91c4e7b2-5d38-4a60-a7f1-3b08d6e2c945
// last-edited: 2026-10-23

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
	"github.com/gin-gonic/gin"
)

type recordedEnqueue struct {
	defID  string
	params map[string]any
}

type fakeMergeEnqueuer struct {
	mu    sync.Mutex
	calls []recordedEnqueue
}

func (f *fakeMergeEnqueuer) EnqueueOp(_ context.Context, defID string, params any, _ ...opsregistry.EnqueueOption) (string, error) {
	raw, _ := json.Marshal(params)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedEnqueue{defID: defID, params: m})
	return "op-1", nil
}

type mergeRig struct {
	*inviteFixture
	svc      *usermerge.Service
	enq      *fakeMergeEnqueuer
	grants   *usermerge.Grants
	admin    *database.User // survivor, and the actor
	absorbed *database.User
	viewer   *database.User
	actor    *database.User
}

func newMergeRig(t *testing.T) *mergeRig {
	t.Helper()
	f := newInviteFixture(t)
	admin, err := f.store.CreateUser("survivor-acct", "survivor@example.com", "bcrypt", "h1", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := f.store.CreateUser("gone-acct", "gone@example.com", "bcrypt", "h2", []string{"admin"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := f.store.CreateUser("viewer-acct", "viewer@example.com", "bcrypt", "h3", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetRaw("pref:"+gone.ID+":speed", []byte(`{"key":"speed","value":"1.5"}`)); err != nil {
		t.Fatal(err)
	}
	svc, err := usermerge.New(f.store, usermerge.WithOwnerEmail(func() string { return "owner@example.com" }),
		usermerge.WithBackfillUserID(func() string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	r := &mergeRig{inviteFixture: f, svc: svc, enq: &fakeMergeEnqueuer{}, grants: usermerge.NewGrants(),
		admin: admin, absorbed: gone, viewer: viewer, actor: admin}
	f.h.SetUserMerge(svc, r.enq, r.grants)
	setActor := func(c *gin.Context) { c.Set("auth_user", r.actor) }
	f.router.GET("/users/merges", setActor, f.h.ListUserMerges)
	f.router.POST("/users/merges/:jid/resume", setActor, f.h.ResumeUserMerge)
	f.router.POST("/users/merges/:jid/undo", setActor, f.h.UndoUserMerge)
	f.router.GET("/users/:id/footprint", setActor, f.h.UserFootprint)
	f.router.POST("/users/:id/merge-into/:target/preview", setActor, f.h.PreviewUserMerge)
	f.router.POST("/users/:id/merge-into/:target/apply", setActor, f.h.ApplyUserMerge)
	f.router.POST("/users/:id/reactivate", setActor, f.h.ReactivateUser)
	return r
}

func (r *mergeRig) mergePath(action string) string {
	return "/users/" + r.absorbed.ID + "/merge-into/" + r.admin.ID + "/" + action
}

func (r *mergeRig) previewDigest(t *testing.T) string {
	t.Helper()
	w, data := r.do(t, http.MethodPost, r.mergePath("preview"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	d, _ := data["digest"].(string)
	if d == "" {
		t.Fatalf("preview has no digest: %s", w.Body.String())
	}
	return d
}

func TestUserMergeRoutes_RequireAnAdminActor(t *testing.T) {
	r := newMergeRig(t)
	r.actor = r.viewer
	calls := []struct{ method, path string }{
		{http.MethodGet, "/users/" + r.absorbed.ID + "/footprint"},
		{http.MethodPost, r.mergePath("preview")},
		{http.MethodPost, r.mergePath("apply")},
		{http.MethodGet, "/users/merges"},
		{http.MethodPost, "/users/merges/j1/resume"},
		{http.MethodPost, "/users/merges/j1/undo"},
	}
	for _, c := range calls {
		if w, _ := r.do(t, c.method, c.path, map[string]any{"digest": "x"}); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin: %d, want 403", c.method, c.path, w.Code)
		}
	}
	locked := *r.admin
	locked.Status = "locked"
	r.actor = &locked
	if w, _ := r.do(t, http.MethodPost, r.mergePath("preview"), nil); w.Code != http.StatusForbidden {
		t.Errorf("a locked admin: %d, want 403", w.Code)
	}
	if len(r.enq.calls) != 0 {
		t.Errorf("a refused caller enqueued %d operation(s)", len(r.enq.calls))
	}
}

func TestUserMergeRoutes_UnwiredAnswers503(t *testing.T) {
	f := newInviteFixture(t)
	admin, _ := f.store.CreateUser("boss-acct", "boss@example.com", "bcrypt", "h", []string{"admin"}, "active")
	f.router.GET("/users/merges", func(c *gin.Context) { c.Set("auth_user", admin) }, f.h.ListUserMerges)
	if w, _ := f.do(t, http.MethodGet, "/users/merges", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired: %d, want 503", w.Code)
	}
}

func TestUserMergeRoutes_PreviewThenApplyEnqueuesWithAGrant(t *testing.T) {
	r := newMergeRig(t)
	if w, data := r.do(t, http.MethodGet, "/users/"+r.absorbed.ID+"/footprint", nil); w.Code != http.StatusOK || data["counts"] == nil {
		t.Fatalf("footprint: %d %s", w.Code, w.Body.String())
	}
	digest := r.previewDigest(t)

	// No body, or a stale digest, never enqueues; a stale digest returns the new plan.
	if w, _ := r.do(t, http.MethodPost, r.mergePath("apply"), map[string]any{}); w.Code != http.StatusBadRequest {
		t.Errorf("apply without a digest: %d, want 400", w.Code)
	}
	w, _ := r.do(t, http.MethodPost, r.mergePath("apply"), map[string]any{"digest": "stale"})
	if w.Code != http.StatusConflict {
		t.Errorf("apply with a stale digest: %d, want 409", w.Code)
	}
	var env struct {
		Plan struct {
			Digest string `json:"digest"`
		} `json:"plan"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Plan.Digest != digest {
		t.Errorf("the 409 carries plan digest %q, want the current %q so the page can refresh", env.Plan.Digest, digest)
	}
	if len(r.enq.calls) != 0 {
		t.Fatalf("a refused apply enqueued %d operation(s)", len(r.enq.calls))
	}

	w, data := r.do(t, http.MethodPost, r.mergePath("apply"), map[string]any{"digest": digest})
	if w.Code != http.StatusAccepted || data["operation_id"] != "op-1" {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	if len(r.enq.calls) != 1 {
		t.Fatalf("enqueued %d operations, want 1", len(r.enq.calls))
	}
	c := r.enq.calls[0]
	if c.defID != usermerge.OpDefID || c.params["dry_run"] != false || c.params["action"] != "apply" ||
		c.params["survivor_id"] != r.admin.ID || c.params["absorbed_id"] != r.absorbed.ID || c.params["digest"] != digest {
		t.Errorf("enqueued %+v", c)
	}
	tok, _ := c.params["grant"].(string)
	want := usermerge.Grant{Action: usermerge.ActionApply, SurvivorID: r.admin.ID, AbsorbedID: r.absorbed.ID, Digest: digest}
	if _, err := r.grants.Redeem(tok, r.admin.ID, want); err != nil {
		t.Errorf("the minted grant does not redeem for the actor: %v", err)
	}
	if u, _ := r.store.GetUserByID(r.absorbed.ID); u.MergedIntoUserID != "" {
		t.Error("the route merged inline instead of enqueuing")
	}
}

func TestUserMergeRoutes_TheActorCannotBeTheMergedAwayAccount(t *testing.T) {
	r := newMergeRig(t)
	r.actor = r.absorbed
	w, data := r.do(t, http.MethodPost, r.mergePath("preview"), nil)
	if w.Code != http.StatusOK || data["applicable"] == true {
		t.Fatalf("preview as the absorbed account: %d %s", w.Code, w.Body.String())
	}
	if refusals, _ := data["refusals"].([]any); len(refusals) == 0 {
		t.Errorf("no refusal listed: %s", w.Body.String())
	}
	if w, _ := r.do(t, http.MethodPost, r.mergePath("apply"), map[string]any{"digest": "x"}); w.Code != http.StatusConflict {
		t.Errorf("apply as the absorbed account: %d, want 409", w.Code)
	}
	if len(r.enq.calls) != 0 {
		t.Errorf("enqueued %d operation(s)", len(r.enq.calls))
	}
}

func TestUserMergeRoutes_SurvivorMustBeAnAdmin(t *testing.T) {
	r := newMergeRig(t)
	path := "/users/" + r.absorbed.ID + "/merge-into/" + r.viewer.ID + "/apply"
	if w, _ := r.do(t, http.MethodPost, path, map[string]any{"digest": "x"}); w.Code != http.StatusConflict {
		t.Errorf("a non-admin survivor: %d, want 409", w.Code)
	}
}

func TestUserMergeRoutes_ReactivateRefusesAMergedAwayAccount(t *testing.T) {
	r := newMergeRig(t)
	digest := r.previewDigest(t)
	if _, err := r.svc.Apply(context.Background(), usermerge.ApplyRequest{ActorID: r.admin.ID, SurvivorID: r.admin.ID,
		AbsorbedID: r.absorbed.ID, Digest: digest}, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	w, _ := r.do(t, http.MethodPost, "/users/"+r.absorbed.ID+"/reactivate", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("reactivate a merged account: %d %s, want 409", w.Code, w.Body.String())
	}
	if u, _ := r.store.GetUserByID(r.absorbed.ID); u.Status == "active" {
		t.Error("a merged-away account was reactivated")
	}
	// Undo is the way back: the journal list shows the merge and undo enqueues.
	w, data := r.do(t, http.MethodGet, "/users/merges", nil)
	list, _ := data["merges"].([]any)
	if w.Code != http.StatusOK || len(list) != 1 {
		t.Fatalf("merges: %d %s", w.Code, w.Body.String())
	}
	jid, _ := list[0].(map[string]any)["id"].(string)
	if w, _ := r.do(t, http.MethodPost, "/users/merges/"+jid+"/undo", nil); w.Code != http.StatusAccepted {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	if w, _ := r.do(t, http.MethodPost, "/users/merges/"+jid+"/resume", nil); w.Code != http.StatusConflict {
		t.Errorf("resume of an applied merge: %d, want 409", w.Code)
	}
	if w, _ := r.do(t, http.MethodPost, "/users/merges/nope/undo", nil); w.Code != http.StatusNotFound {
		t.Errorf("undo of an unknown merge: %d, want 404", w.Code)
	}
}
