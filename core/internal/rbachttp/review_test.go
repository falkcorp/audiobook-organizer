// file: core/internal/rbachttp/review_test.go
// version: 0.1.0
// guid: 7aa92cb2-be1e-44f9-8754-dca4834f58ee
// last-edited: 2026-11-21

package rbachttp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only. F4-08 B10i: POST /users/{id}/review-admin, the route that
// clears restore_admins_review one holder at a time (05-rbac 7.1).

// fakeReview is an AdminReviewer whose answer a test sets; it records the
// actor and the account the handler passed.
type fakeReview struct {
	mu    sync.Mutex
	err   error
	calls []string
	actor rbac.Checker
}

func (f *fakeReview) AcknowledgeAdminReview(_ context.Context, actor rbac.Checker, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, userID)
	f.actor = actor
	return f.err
}

func reviewAPI(f *fakeReview) (*api, *emitted) {
	em := &emitted{}
	return &api{review: f, audit: em, log: discard(), base: "/api/core/v1"}, em
}

func ack(a *api, p *auth.Principal, id string) error {
	_, err := a.acknowledgeReview(context.Background(), in(p, userReq{ID: id}))
	return err
}

// TestAcknowledgeReview_NoPrincipalIs401AndNoServiceIs503: reached without a
// principal it fails closed; mounted without its service it answers 503.
func TestAcknowledgeReview_NoPrincipalIs401AndNoServiceIs503(t *testing.T) {
	_, svc := realAPI(t)
	f := &fakeReview{}
	a, _ := reviewAPI(f)
	wantStatus(t, ack(a, nil, "u-robin"), http.StatusUnauthorized, "authentication_required")
	if len(f.calls) != 0 {
		t.Errorf("the service was called without a principal: %v", f.calls)
	}
	none := &api{log: discard(), base: "/api/core/v1"}
	wantStatus(t, ack(none, svc.admin(), "u-robin"), http.StatusServiceUnavailable, "unavailable")
}

// TestAcknowledgeReview_NeedsAnInteractiveSession: an API key, an auth-disabled
// principal and an unauthenticated one are refused whatever they hold, with
// interactive_required, and the service is not called.
func TestAcknowledgeReview_NeedsAnInteractiveSession(t *testing.T) {
	_, svc := realAPI(t)
	for _, m := range []auth.Method{auth.MethodAPIKey, auth.MethodAuthDisabled, auth.MethodNone} {
		f := &fakeReview{}
		a, _ := reviewAPI(f)
		wantStatus(t, ack(a, svc.principal(m, rbac.RoleAdmin), "u-robin"), http.StatusForbidden, "interactive_required")
		if len(f.calls) != 0 {
			t.Errorf("method %q: the service was called: %v", m, f.calls)
		}
	}
}

// TestAcknowledgeReview_NeedsCoreAdmin: an interactive session that does not hold
// core:admin (a viewer, an editor, no role) is 403 permission_denied, and the
// service is not called.
func TestAcknowledgeReview_NeedsCoreAdmin(t *testing.T) {
	_, svc := realAPI(t)
	for name, p := range map[string]*auth.Principal{
		"viewer":   svc.principal(auth.MethodSession, rbac.RoleViewer),
		"editor":   svc.principal(auth.MethodSession, rbac.RoleEditor),
		"no roles": svc.principal(auth.MethodSession),
	} {
		f := &fakeReview{}
		a, _ := reviewAPI(f)
		wantStatus(t, ack(a, p, "u-robin"), http.StatusForbidden, "permission_denied")
		if len(f.calls) != 0 {
			t.Errorf("%s: the service was called: %v", name, f.calls)
		}
	}
}

// TestAcknowledgeReview_PassesTheAccountAndTheActor: an interactive admin's call
// reaches the service with the path's account and the principal's own checker.
func TestAcknowledgeReview_PassesTheAccountAndTheActor(t *testing.T) {
	_, svc := realAPI(t)
	f := &fakeReview{}
	a, _ := reviewAPI(f)
	p := svc.admin()
	if err := ack(a, p, "u-robin"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.calls, []string{"u-robin"}) || f.actor != p.Perms {
		t.Errorf("service saw account %v and actor %v, want [u-robin] and the principal's checker", f.calls, f.actor)
	}
}

// TestAcknowledgeReview_ErrorMapping: an account not on the list is 404 not_found,
// a refusal by the service is 403, and anything else (an unreadable report, a
// commit that failed) is neither a 404 nor a body that quotes the cause.
func TestAcknowledgeReview_ErrorMapping(t *testing.T) {
	_, svc := realAPI(t)
	p := svc.admin()

	f := &fakeReview{err: rbac.ErrNotInAdminReview}
	a, _ := reviewAPI(f)
	err := ack(a, p, "u-joker")
	wantStatus(t, err, http.StatusNotFound, "not_found")
	if blob := errBlob(t, err); strings.Contains(blob, "u-joker") {
		t.Errorf("the 404 echoes the account: %s", blob)
	}

	f = &fakeReview{err: rbac.ErrDenied}
	a, _ = reviewAPI(f)
	wantStatus(t, ack(a, p, "u-robin"), http.StatusForbidden, "permission_denied")

	f = &fakeReview{err: errors.New("journal: restore_report does not decode: invalid character 'x'")}
	a, _ = reviewAPI(f)
	err = ack(a, p, "u-robin")
	if err == nil {
		t.Fatal("a failed acknowledgement answered success")
	}
	if he, ok := errors.AsType[*httpapi.Error](err); ok && he.Status == http.StatusNotFound {
		t.Errorf("an unreadable report answered 404 (%v): it is not an unlisted account", err)
	}
	if blob := errBlob(t, err); strings.Contains(blob, "decode") {
		t.Errorf("the answer quotes the cause: %s", blob)
	}
}

// TestAcknowledgeReview_AnAccountTooLongToBeOneIs404: a path ID no user has is
// 404 and never reaches the service or an answer.
func TestAcknowledgeReview_AnAccountTooLongToBeOneIs404(t *testing.T) {
	_, svc := realAPI(t)
	f := &fakeReview{}
	a, _ := reviewAPI(f)
	long := strings.Repeat("u", maxIDLen+1)
	err := ack(a, svc.admin(), long)
	wantStatus(t, err, http.StatusNotFound, "not_found")
	if len(f.calls) != 0 || strings.Contains(errBlob(t, err), long) {
		t.Errorf("a too-long ID reached the service (%d calls) or the answer", len(f.calls))
	}
}
