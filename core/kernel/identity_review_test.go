// file: core/kernel/identity_review_test.go
// version: 0.1.0
// guid: d41e8b07-6c2f-4a93-b5e0-7f18a3c9d264
// last-edited: 2026-11-21

package kernel

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only. F4-08 B10i: POST /api/core/v1/users/{id}/review-admin through
// the real stages, the store and a restart.

const reviewPath = "/api/core/v1/users/%s/review-admin"

func (fx *mountFx) ack(id string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	return fx.do(http.MethodPost, strings.Replace(reviewPath, "%s", id, 1), mutate)
}

// reviewReason is core.backup's probe reason now, "" when it is healthy.
func (fx *mountFx) backupReason(t *testing.T) string {
	t.Helper()
	res := fx.k.ident.backupProbe()(t.Context())
	if res.Status == health.StatusUp {
		return ""
	}
	return res.Reason
}

func (fx *mountFx) storedReview(t *testing.T) journal.RestoreReport {
	t.Helper()
	got, err := journal.ReadRestoreReport(t.Context(), fx.k.ident.backupKS)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// reviewRecords is the stored core.role.review_admin audit records.
func (fx *mountFx) reviewRecords(t *testing.T) []audit.Record {
	t.Helper()
	var out []audit.Record
	for rec, err := range fx.k.audit.Query(t.Context(), audit.Filter{ActionPrefix: "core.role.review_admin", Limit: 100}) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

// TestAcknowledgeAdminReview_ClearsTheTokenOneHolderAtATimeAcrossARestart: two
// holders listed; an interactive admin acknowledges one (204, still degraded),
// then the other (204, healthy, record deleted); the token stays gone after a
// restart, and a third acknowledgement is 404 and audits nothing.
func TestAcknowledgeAdminReview_ClearsTheTokenOneHolderAtATimeAcrossARestart(t *testing.T) {
	h, c, _ := mountConfig(t, true)
	k := h.mustBoot(c)
	fx := &mountFx{k: k, h: k.Handler()}
	admin, adminCookie := fx.user(t, "u-bruce", rbac.RoleAdmin)
	if err := journal.PutRestoreReport(t.Context(), k.ident.backupKS,
		journal.RestoreReport{Review: []string{"u-nightwing", "u-bruce"}}, time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := fx.backupReason(t); got != reasonRestoreAdminsReview {
		t.Fatalf("core.backup before any acknowledgement = %q, want %q", got, reasonRestoreAdminsReview)
	}

	if rec := fx.ack("u-nightwing", withCookie(adminCookie)); rec.Code != http.StatusNoContent {
		t.Fatalf("acknowledging u-nightwing = %d %s", rec.Code, rec.Body)
	}
	if got := fx.storedReview(t).Review; !slices.Equal(got, []string{"u-bruce"}) {
		t.Fatalf("review list after the first = %v, want [u-bruce]", got)
	}
	if got := fx.backupReason(t); got != reasonRestoreAdminsReview {
		t.Fatalf("core.backup with one holder left = %q, want it still raised", got)
	}

	// The last one is the acknowledging admin itself: allowed.
	if rec := fx.ack("u-bruce", withCookie(adminCookie)); rec.Code != http.StatusNoContent {
		t.Fatalf("acknowledging oneself = %d %s", rec.Code, rec.Body)
	}
	if got := fx.backupReason(t); got != "" {
		t.Fatalf("core.backup after the last acknowledgement = %q, want healthy", got)
	}
	if _, err := k.ident.backupKS.Get(t.Context(), []byte("restore_report")); err == nil {
		t.Fatal("the report record survives its last acknowledgement")
	}

	// The audit records name the actor and the acknowledged account, in order.
	recs := fx.reviewRecords(t)
	if len(recs) != 2 {
		t.Fatalf("%d review_admin audit records, want 2: %+v", len(recs), recs)
	}
	ids := []string{recs[0].Target.ID, recs[1].Target.ID}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"u-bruce", "u-nightwing"}) {
		t.Errorf("audited subjects = %v", ids)
	}
	for _, r := range recs {
		if r.Action != "core.role.review_admin" || r.Outcome != audit.Success || r.Actor.UserID != string(admin.ID) ||
			r.Target.Module != "core" || r.Target.Kind != "user" || len(r.Details) != 0 {
			t.Errorf("audit record = %+v, want core.role.review_admin success by %s on a core user, no details", r, admin.ID)
		}
	}

	// A second acknowledgement of the same holder: 404, and no third record.
	if rec := fx.ack("u-bruce", withCookie(adminCookie)); rec.Code != http.StatusNotFound {
		t.Fatalf("acknowledging again = %d %s, want 404", rec.Code, rec.Body)
	}
	if n := len(fx.reviewRecords(t)); n != 2 {
		t.Errorf("a refused acknowledgement wrote an audit record: %d records", n)
	}

	h.close(k)
	again := h.mustBoot(c)
	fx2 := &mountFx{k: again, h: again.Handler()}
	if got := fx2.backupReason(t); got != "" {
		t.Fatalf("core.backup after a restart = %q, want healthy: the token came back", got)
	}
}

// TestAcknowledgeAdminReview_RefusalsChangeNothing: no credential, a session
// without core:admin, an API key and a holder not on the list each leave the
// report and the audit log as they were, with the status the stages give.
func TestAcknowledgeAdminReview_RefusalsChangeNothing(t *testing.T) {
	h, c, _ := mountConfig(t, true)
	k := h.mustBoot(c)
	fx := &mountFx{k: k, h: k.Handler()}
	_, adminCookie := fx.user(t, "u-bruce", rbac.RoleAdmin)
	_, workerCookie := fx.user(t, "u-barbara", "relay-worker")
	keyOwner, _ := fx.user(t, "u-lucius", rbac.RoleAdmin)
	if err := journal.PutRestoreReport(t.Context(), k.ident.backupKS,
		journal.RestoreReport{Review: []string{"u-nightwing"}}, time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	actor, err := k.ident.svc.Auth.PrincipalFor(t.Context(), keyOwner, auth.MethodSession, nil, keyOwner.CredentialEpoch)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := k.ident.svc.Auth.CreateAPIKey(t.Context(), actor, auth.KeySpec{Owner: keyOwner.ID, Name: "oracle", Scopes: []rbac.Permission{rbac.CoreAdmin}})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		id     string
		mutate func(*http.Request)
		code   int
		body   string
	}{
		{"anonymous", "u-nightwing", nil, http.StatusUnauthorized, ""},
		{"a session without core:admin", "u-nightwing", withCookie(workerCookie), http.StatusForbidden, "permission_denied"},
		{"an API key holding core:admin", "u-nightwing", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+raw) }, http.StatusForbidden, "interactive_required"},
		{"a holder not on the list", "u-joker", withCookie(adminCookie), http.StatusNotFound, "not_found"},
	} {
		rec := fx.ack(tc.id, tc.mutate)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s = %d %s, want %d %q", tc.name, rec.Code, rec.Body, tc.code, tc.body)
		}
	}
	if got := fx.storedReview(t).Review; !slices.Equal(got, []string{"u-nightwing"}) {
		t.Errorf("a refused acknowledgement changed the report: %v", got)
	}
	if n := len(fx.reviewRecords(t)); n != 0 {
		t.Errorf("a refused acknowledgement was audited %d times", n)
	}
}
