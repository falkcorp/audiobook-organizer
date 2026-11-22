// file: internal/server/handlers/user_merge_identity_test.go
// version: 0.1.0
// guid: c4e81a27-5b93-4d60-8f1a-7d2b6e90a3c5
// last-edited: 2026-11-21

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"testing"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// F4-08 B10l-op: under identity the merge routes are live. These tests run the
// real usermerge.Service over a stand-in for core's admin handles and check
// what the routes answer, what the preview shows and what the apply route puts
// in the grant.

const idCredential = "sess-batman-credential-id"

type idCore struct {
	// Admin is embedded so the fake keeps compiling when core adds methods to
	// auth.Admin; the methods the tests use are defined on idCore below.
	coreauth.Admin
	users   map[string]*coreauth.User
	summary coreauth.MergeSummary
	planErr error
}

func (c *idCore) User(_ context.Context, id coreauth.UserID) (*coreauth.User, error) {
	if u, ok := c.users[string(id)]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, coreauth.ErrNotFound
}

func (c *idCore) UserByUsername(context.Context, string) (*coreauth.User, error) {
	return nil, coreauth.ErrNotFound
}

func (c *idCore) Users(context.Context, coreauth.UserFilter) iter.Seq2[*coreauth.User, error] {
	return func(func(*coreauth.User, error) bool) {}
}

func (c *idCore) PrincipalFrom(ctx context.Context) (*coreauth.Principal, bool) {
	return coreauth.PrincipalFrom(ctx)
}

func (c *idCore) PlanUserMerge(ctx context.Context, source, target coreauth.UserID) (coreauth.UserMergePlan, coreauth.MergeSummary, error) {
	if _, ok := coreauth.PrincipalFrom(ctx); !ok {
		return coreauth.UserMergePlan{}, coreauth.MergeSummary{}, fmt.Errorf("%w: no principal", coreauth.ErrDenied)
	}
	if c.planErr != nil {
		return coreauth.UserMergePlan{}, coreauth.MergeSummary{}, c.planErr
	}
	s := c.summary
	s.Source, s.Target = source, target
	return coreauth.UserMergePlan{}, s, nil
}

func (c *idCore) StageUserMerge(context.Context, *storage.Batch, coreauth.UserMergePlan) (coreauth.AfterCommit, error) {
	return func(context.Context) {}, nil
}

// idMergeRig is the HEAD merge rig with the service wired for core identity.
func idMergeRig(t *testing.T) (*mergeRig, *idCore) {
	t.Helper()
	r := newMergeRig(t)
	core := &idCore{users: map[string]*coreauth.User{
		r.admin.ID:    {ID: coreauth.UserID(r.admin.ID), Username: "survivor-acct", Status: coreauth.StatusActive, Roles: []rbac.RoleName{rbac.RoleAdmin}, Version: 2},
		r.absorbed.ID: {ID: coreauth.UserID(r.absorbed.ID), Username: "gone-acct", Status: coreauth.StatusActive, Roles: []rbac.RoleName{rbac.RoleAdmin}, Version: 4},
	}, summary: coreauth.MergeSummary{TargetHoldsAdmin: true, Email: coreauth.MergeEmailKept, APIKeys: []string{"key-alfred"}, IdentityProviders: []string{"gotham-sso"}}}
	svc, err := usermerge.New(r.store,
		usermerge.WithIdentityFlipped(func() bool { return true }),
		usermerge.WithCoreIdentity(usermerge.CoreIdentity{Admin: core, Users: core,
			Refresh: func(_ context.Context, p *coreauth.Principal) (*coreauth.Principal, error) { return p, nil }}))
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	r.h.SetUserMerge(svc, r.enq, r.grants)
	return r, core
}

// engine re-serves the merge routes with a stage-12 principal for the actor and
// the Identity bound (principal false leaves the principal out).
func (r *mergeRig) idEngine(t *testing.T, principal bool) *coreauth.Principal {
	t.Helper()
	chk := bridgeChecker([]rbac.Permission{rbac.CoreAdmin})
	pr := &coreauth.Principal{User: &coreauth.User{ID: coreauth.UserID(r.actor.ID)}, CredentialID: idCredential,
		Method: coreauth.MethodSession, Perms: chk}
	e := gin.New()
	e.Use(func(c *gin.Context) {
		c.Set("auth_user", r.actor)
		ctx := rbac.WithChecker(legacyauth.WithIdentity(c.Request.Context(), bridgeIdentity(t)), chk)
		if principal {
			ctx = coreauth.WithPrincipal(ctx, pr)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	e.POST("/users/:id/merge-into/:target/preview", r.h.PreviewUserMerge)
	e.POST("/users/:id/merge-into/:target/apply", r.h.ApplyUserMerge)
	e.GET("/users/:id/footprint", r.h.UserFootprint)
	r.router = e
	return pr
}

func TestUserMergeRoutes_PreviewUnderIdentityShowsCoresSummaryAndNoCredentialBytes(t *testing.T) {
	r, _ := idMergeRig(t)
	r.idEngine(t, true)
	w, data := r.do(t, http.MethodPost, r.mergePath("preview"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("preview under identity: %d %s, want 200", w.Code, w.Body.String())
	}
	var env struct {
		Core struct {
			APIKeyIDs         []string `json:"api_key_ids"`
			IdentityProviders []string `json:"identity_providers"`
			Email             string   `json:"email"`
		} `json:"core"`
	}
	raw, _ := json.Marshal(data)
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"core"`) || !strings.Contains(body, "key-alfred") || !strings.Contains(body, "gotham-sso") {
		t.Errorf("the preview does not show core's summary: %s", body)
	}
	for _, needle := range []string{"h1", "h2", "bcrypt", idCredential} {
		if strings.Contains(body, `"`+needle+`"`) || strings.Contains(body, idCredential) {
			t.Errorf("the preview carries credential material %q: %s", needle, body)
		}
	}
	if d, _ := data["digest"].(string); d == "" {
		t.Errorf("no digest: %s", body)
	}
}

func TestUserMergeRoutes_ApplyUnderIdentityPutsTheRequestsPrincipalInTheGrantOnly(t *testing.T) {
	r, _ := idMergeRig(t)
	pr := r.idEngine(t, true)
	digest := r.previewDigest(t)
	w, _ := r.do(t, http.MethodPost, r.mergePath("apply"), map[string]any{"digest": digest, "acknowledge_owner_identity": true})
	if w.Code != http.StatusAccepted {
		t.Fatalf("apply under identity: %d %s, want 202", w.Code, w.Body.String())
	}
	if len(r.enq.calls) != 1 {
		t.Fatalf("enqueued %d operations", len(r.enq.calls))
	}
	// The enqueued parameters (stored, shown, logged) hold a token and no principal.
	params, _ := json.Marshal(r.enq.calls[0].params)
	if strings.Contains(string(params), idCredential) {
		t.Errorf("the principal reached the operation's parameters: %s", params)
	}
	tok, _ := r.enq.calls[0].params["grant"].(string)
	if tok == "" {
		t.Fatalf("no grant token in %s", params)
	}
	gr, err := r.grants.Redeem(tok, r.actor.ID, usermerge.Grant{Action: usermerge.ActionApply, SurvivorID: r.admin.ID, AbsorbedID: r.absorbed.ID, Digest: digest, AcknowledgeOwnerIdentity: true})
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if gr.Principal != pr {
		t.Errorf("the grant holds %v, want the request's own stage-12 principal", gr.Principal)
	}
}

func TestUserMergeRoutes_ApplyUnderIdentityWithoutAPrincipalIsForbidden(t *testing.T) {
	r, _ := idMergeRig(t)
	r.idEngine(t, true)
	digest := r.previewDigest(t)
	r.idEngine(t, false) // the same routes, a request that carries no principal
	w, _ := r.do(t, http.MethodPost, r.mergePath("apply"), map[string]any{"digest": digest, "acknowledge_owner_identity": true})
	// Core refuses the planning itself (no principal), before the grant step.
	if w.Code != http.StatusForbidden {
		t.Fatalf("apply without a principal: %d %s, want 403", w.Code, w.Body.String())
	}
	if len(r.enq.calls) != 0 {
		t.Errorf("an operation was enqueued without a principal")
	}
}

func TestUserMergeRoutes_CoreRefusalsAnswerWithTheirCode(t *testing.T) {
	codes := map[string]error{
		coreauth.RefusedAlreadyMerged: &coreauth.MergeRefusal{Code: coreauth.RefusedAlreadyMerged, Detail: "already merged"},
		coreauth.RefusedNoSignIn:      &coreauth.MergeRefusal{Code: coreauth.RefusedNoSignIn, Detail: "no sign-in"},
		coreauth.RefusedEmailLinkable: &coreauth.MergeRefusal{Code: coreauth.RefusedEmailLinkable, Detail: "linkable"},
		usermerge.CodeLastAdmin:       coreauth.ErrLastAdmin,
		usermerge.CodeEmailInUse:      coreauth.ErrEmailInUse,
		usermerge.CodeInactiveUser:    coreauth.ErrInactiveUser,
	}
	for code, planErr := range codes {
		t.Run(code, func(t *testing.T) {
			r, core := idMergeRig(t)
			r.idEngine(t, true)
			core.planErr = planErr
			// Preview is a 200 whose plan lists the refusal and its code.
			w, _ := r.do(t, http.MethodPost, r.mergePath("preview"), nil)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"`+code+`"`) {
				t.Errorf("preview: %d %s", w.Code, w.Body.String())
			}
			// Apply is a 409 carrying the code.
			w, _ = r.do(t, http.MethodPost, r.mergePath("apply"), map[string]any{"digest": "x"})
			if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != code {
				t.Errorf("apply: %d %s, want 409 %s", w.Code, w.Body.String(), code)
			}
			if len(r.enq.calls) != 0 {
				t.Errorf("a refused merge was enqueued")
			}
		})
	}
}

func TestUserMergeRoutes_CoreDenialIs403AndStoreFailureIs500(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"denied":            {fmt.Errorf("%w: users.manage", coreauth.ErrDenied), http.StatusForbidden},
		"store unavailable": {coreauth.ErrStoreUnavailable, http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			r, core := idMergeRig(t)
			r.idEngine(t, true)
			core.planErr = tc.err
			w, _ := r.do(t, http.MethodPost, r.mergePath("preview"), nil)
			if w.Code != tc.want {
				t.Errorf("preview: %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

// stubTooLarge answers every plan with the size cap's refusal.
type stubTooLarge struct{ *usermerge.Service }

func (stubTooLarge) BuildPlan(context.Context, usermerge.PlanRequest) (*usermerge.Plan, error) {
	return nil, fmt.Errorf("%w (estimated 70000000 bytes, cap 67108864)", usermerge.ErrMergeTooLarge)
}

func TestUserMergeRoutes_TooLargeIs409MergeTooLargeOnPreviewAndApply(t *testing.T) {
	r, _ := idMergeRig(t)
	r.h.SetUserMerge(stubTooLarge{r.svc}, r.enq, r.grants)
	r.idEngine(t, true)
	for _, action := range []string{"preview", "apply"} {
		body := map[string]any{"digest": "x"}
		w, _ := r.do(t, http.MethodPost, r.mergePath(action), body)
		if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemMergeTooLarge {
			t.Errorf("%s: %d %s, want 409 %s", action, w.Code, w.Body.String(), usermerge.ProblemMergeTooLarge)
		}
	}
	if len(r.enq.calls) != 0 {
		t.Errorf("an oversized merge was enqueued")
	}
}

func TestUserMergeRoutes_FootprintStaysUnavailableUnderIdentity(t *testing.T) {
	r, _ := idMergeRig(t)
	r.idEngine(t, true)
	w, _ := r.do(t, http.MethodGet, "/users/"+r.absorbed.ID+"/footprint", nil)
	if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemUnavailableUnderIdentity {
		t.Errorf("footprint under identity: %d %s", w.Code, w.Body.String())
	}
}
