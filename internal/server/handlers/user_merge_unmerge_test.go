// file: internal/server/handlers/user_merge_unmerge_test.go
// version: 0.1.0
// guid: 6a4c9e12-3d58-4b7f-a0e1-9c2f5b8d7a34
// last-edited: 2026-11-22

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage"

	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// F4-08 B10p: under identity the undo and resume routes are live for an
// identity-era journal. These tests run the real usermerge.Service over a
// stand-in for core's admin handles and check what the routes answer and what
// the grant holds. The real core's own reversal is in core/auth, and the
// full-boot undo is in internal/books.

func (c *idCore) PlanUserUnmerge(ctx context.Context, rec coreauth.UnmergeRecord) (coreauth.UserUnmergePlan, coreauth.UnmergeSummary, error) {
	if _, ok := coreauth.PrincipalFrom(ctx); !ok {
		return coreauth.UserUnmergePlan{}, coreauth.UnmergeSummary{}, fmt.Errorf("%w: no principal", coreauth.ErrDenied)
	}
	if c.unmergeErr != nil {
		return coreauth.UserUnmergePlan{}, coreauth.UnmergeSummary{}, c.unmergeErr
	}
	return coreauth.UserUnmergePlan{}, coreauth.UnmergeSummary{Source: rec.Source, Target: rec.Target}, nil
}

func (c *idCore) StageUserUnmerge(context.Context, *storage.Batch, coreauth.UserUnmergePlan) (coreauth.AfterCommit, error) {
	return func(context.Context) {}, nil
}

// identityJournal is the JSON of an applied identity-era merge of the rig's
// absorbed account into its admin, with the receipt a real core leaves.
func (r *mergeRig) identityJournal(t *testing.T, id string, pending bool) {
	t.Helper()
	raw := fmt.Sprintf(`{"id":%q,"status":"applied","survivor_id":%q,"absorbed_id":%q,"core":{"era":"identity",`+
		`"source_status_before":"active","source_version_before":4,"api_key_ids":["key-alfred"],`+
		`"identity_providers":["gotham-sso"],"identity_ids":["gotham-sso/batman-subject-0001"],`+
		`"api_key_versions":{"key-alfred":3},"sweep_pending":%t}}`, id, r.admin.ID, r.absorbed.ID, pending)
	r.journal(t, id, raw)
}

func TestUserMergeRoutes_UndoUnderIdentityPutsThePrincipalInTheGrant(t *testing.T) {
	r, _ := idMergeRig(t)
	pr := r.idEngine(t, true)
	r.identityJournal(t, "j-id", false)
	w, _ := r.do(t, http.MethodPost, "/users/merges/j-id/undo", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("undo of an identity-era merge: %d %s, want 202", w.Code, w.Body.String())
	}
	if len(r.enq.calls) != 1 {
		t.Fatalf("enqueued %d operations", len(r.enq.calls))
	}
	params, _ := json.Marshal(r.enq.calls[0].params)
	if strings.Contains(string(params), idCredential) {
		t.Errorf("the principal reached the operation's parameters: %s", params)
	}
	tok, _ := r.enq.calls[0].params["grant"].(string)
	gr, err := r.grants.Redeem(tok, r.actor.ID, usermerge.Grant{Action: usermerge.ActionUndo, JournalID: "j-id"})
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if gr.Principal != pr {
		t.Errorf("the grant holds %v, want the request's own stage-12 principal", gr.Principal)
	}
}

func TestUserMergeRoutes_UndoRefusalsAnswerWithTheirCodeAndEnqueueNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
		code string
	}{
		{"a key moved on", &coreauth.MergeRefusal{Code: coreauth.RefusedKeyMoved, Detail: "a key changed"}, http.StatusConflict, coreauth.RefusedKeyMoved},
		{"a sign-in moved on", &coreauth.MergeRefusal{Code: coreauth.RefusedSignInMoved, Detail: "x"}, http.StatusConflict, coreauth.RefusedSignInMoved},
		{"the address is claimed", &coreauth.MergeRefusal{Code: coreauth.RefusedEmailClaimed, Detail: "x"}, http.StatusConflict, coreauth.RefusedEmailClaimed},
		{"the source changed", &coreauth.MergeRefusal{Code: coreauth.RefusedSourceChanged, Detail: "x"}, http.StatusConflict, coreauth.RefusedSourceChanged},
		{"the last admin", coreauth.ErrLastAdmin, http.StatusConflict, usermerge.CodeLastAdmin},
		{"an admin below the source", coreauth.ErrDominance, http.StatusForbidden, ""},
		{"not an admin", fmt.Errorf("%w: users.manage", coreauth.ErrDenied), http.StatusForbidden, ""},
		{"core is down", coreauth.ErrStoreUnavailable, http.StatusInternalServerError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, core := idMergeRig(t)
			r.idEngine(t, true)
			r.identityJournal(t, "j-id", false)
			core.unmergeErr = tc.err
			w, _ := r.do(t, http.MethodPost, "/users/merges/j-id/undo", nil)
			if w.Code != tc.want {
				t.Fatalf("undo: %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
			if tc.code != "" && problemCode(t, w.Body.Bytes()) != tc.code {
				t.Errorf("code = %q, want %q (%s)", problemCode(t, w.Body.Bytes()), tc.code, w.Body.String())
			}
			// The refusal text names no sign-in, key or address.
			for _, leak := range []string{"batman-subject-0001", "gone@example.com", "survivor@example.com"} {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("the answer carries %q: %s", leak, w.Body.String())
				}
			}
			if len(r.enq.calls) != 0 {
				t.Errorf("a refused undo enqueued %d operation(s)", len(r.enq.calls))
			}
		})
	}
}

func TestUserMergeRoutes_UndoUnderIdentityWithAPendingSweepIs409SweepPending(t *testing.T) {
	r, _ := idMergeRig(t)
	r.idEngine(t, true)
	r.identityJournal(t, "j-pending", true)
	w, _ := r.do(t, http.MethodPost, "/users/merges/j-pending/undo", nil)
	if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.CodeSweepPending {
		t.Fatalf("undo: %d %s, want 409 %s", w.Code, w.Body.String(), usermerge.CodeSweepPending)
	}
	if len(r.enq.calls) != 0 {
		t.Error("a refused undo enqueued an operation")
	}
}

func TestUserMergeRoutes_UndoUnderIdentityWithoutAPrincipalIsForbidden(t *testing.T) {
	r, _ := idMergeRig(t)
	r.idEngine(t, false)
	r.identityJournal(t, "j-id", false)
	w, _ := r.do(t, http.MethodPost, "/users/merges/j-id/undo", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("undo without a principal: %d %s, want 403", w.Code, w.Body.String())
	}
	if len(r.enq.calls) != 0 {
		t.Error("an operation was enqueued without a principal")
	}
}

func TestUserMergeRoutes_ResumeUnderIdentityIsOnlyForAPendingSweep(t *testing.T) {
	r, _ := idMergeRig(t)
	r.idEngine(t, true)
	r.identityJournal(t, "j-pending", true)
	r.identityJournal(t, "j-clean", false)
	r.journal(t, "j-preflip", journalJSON("j-preflip", usermerge.StatusApplying))

	w, _ := r.do(t, http.MethodPost, "/users/merges/j-pending/resume", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("resume of a pending sweep: %d %s, want 202", w.Code, w.Body.String())
	}
	if len(r.enq.calls) != 1 {
		t.Fatalf("enqueued %d operations", len(r.enq.calls))
	}
	for _, jid := range []string{"j-clean", "j-preflip"} {
		w, _ := r.do(t, http.MethodPost, "/users/merges/"+jid+"/resume", nil)
		if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemMergePermanent {
			t.Errorf("resume %s: %d %s, want 409 %s", jid, w.Code, w.Body.String(), usermerge.ProblemMergePermanent)
		}
	}
	if len(r.enq.calls) != 1 {
		t.Errorf("a permanent resume enqueued an operation (%d in all)", len(r.enq.calls))
	}
}

// A pre-flip journal's undo stays 409 merge_permanent even with core wired.
func TestUserMergeRoutes_UndoOfAPreFlipJournalUnderIdentityStaysPermanent(t *testing.T) {
	r, _ := idMergeRig(t)
	r.idEngine(t, true)
	r.journal(t, "j-preflip", journalJSON("j-preflip", usermerge.StatusApplied))
	w, _ := r.do(t, http.MethodPost, "/users/merges/j-preflip/undo", nil)
	if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemMergePermanent {
		t.Fatalf("undo: %d %s, want 409 %s", w.Code, w.Body.String(), usermerge.ProblemMergePermanent)
	}
	if len(r.enq.calls) != 0 {
		t.Error("an operation was enqueued")
	}
}

// noGuard is the real service with the permanence gate left open, so the
// handler's own predicate is what decides: it must still refuse a resume of an
// identity-era journal whose sweep is not pending (defence behind CheckAction).
type noGuard struct{ *usermerge.Service }

func (noGuard) CheckAction(usermerge.Action, string) error { return nil }

func TestUserMergeRoutes_TheResumePredicateAloneRefusesACleanIdentityJournal(t *testing.T) {
	r, _ := idMergeRig(t)
	r.h.SetUserMerge(noGuard{r.svc}, r.enq, r.grants)
	r.idEngine(t, true)
	r.identityJournal(t, "j-clean", false)
	w, _ := r.do(t, http.MethodPost, "/users/merges/j-clean/resume", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("resume of a clean identity journal with the service gate open: %d %s, want 409", w.Code, w.Body.String())
	}
	if len(r.enq.calls) != 0 {
		t.Errorf("enqueued %d operation(s)", len(r.enq.calls))
	}
}
