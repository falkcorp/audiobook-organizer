// file: internal/server/handlers/user_merge_unavailable_test.go
// version: 0.1.0
// guid: c2d94a07-5b3e-4e81-9f60-1a8d7b4c2e93
// last-edited: 2026-11-21

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// F4-08 B10l guard: once core identity is wired the merge plans and writes
// against rows books no longer holds, so preview and apply answer 409
// merge_unavailable_under_identity before any account is resolved, any body is
// read, any plan is built or any grant is minted. So does resume, before any
// journal is read; an undo stays B10b2's merge_permanent.

func problemCode(t *testing.T, body []byte) string {
	t.Helper()
	var b struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Code
}

func TestUserMergeRoutes_PreviewIsUnavailableUnderIdentity(t *testing.T) {
	r := newMergeRig(t)
	r.flipped(t, true)
	// Accounts that do not exist: without the guard this is a 404 from the
	// account lookup, so a 409 proves the guard ran before it.
	for _, path := range []string{r.mergePath("preview"), "/users/u-nope/merge-into/u-nada/preview"} {
		w, _ := r.do(t, http.MethodPost, path, nil)
		if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemUnavailableUnderIdentity {
			t.Errorf("preview %s under identity: %d %s, want 409 %s", path, w.Code, w.Body.String(), usermerge.ProblemUnavailableUnderIdentity)
		}
	}
}

func TestUserMergeRoutes_ApplyIsUnavailableUnderIdentity(t *testing.T) {
	r := newMergeRig(t)
	digest := r.previewDigest(t) // a real digest from before the flip
	r.flipped(t, true)
	for _, body := range []any{map[string]any{"digest": digest}, map[string]any{}, nil} {
		for _, path := range []string{r.mergePath("apply"), "/users/u-nope/merge-into/u-nada/apply"} {
			w, _ := r.do(t, http.MethodPost, path, body)
			if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemUnavailableUnderIdentity {
				t.Errorf("apply %s %v under identity: %d %s, want 409 %s", path, body, w.Code, w.Body.String(), usermerge.ProblemUnavailableUnderIdentity)
			}
		}
	}
	if len(r.enq.calls) != 0 {
		t.Fatalf("a refused apply enqueued %d operation(s)", len(r.enq.calls))
	}
	if u, _ := r.store.GetUserByID(r.absorbed.ID); u.MergedIntoUserID != "" || u.Status != "active" {
		t.Errorf("a refused apply changed the absorbed account: %+v", u)
	}
	if got, _ := r.store.ScanPrefix(database.UserMergeJournalPrefix); len(got) != 0 {
		t.Errorf("a refused apply wrote %d journal row(s)", len(got))
	}
}

func TestUserMergeRoutes_ResumeIsUnavailableUnderIdentity(t *testing.T) {
	r := newMergeRig(t)
	r.flipped(t, true)
	r.journal(t, "j-applying", journalJSON("j-applying", usermerge.StatusApplying))
	// A journal that exists, one that does not and one that cannot be decoded:
	// the guard answers before any journal is read, so all three are the same 409.
	r.journal(t, "j-garbled", `{not json`)
	for _, jid := range []string{"j-applying", "j-nope", "j-garbled"} {
		w, _ := r.do(t, http.MethodPost, "/users/merges/"+jid+"/resume", nil)
		if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemUnavailableUnderIdentity {
			t.Errorf("resume %s under identity: %d %s, want 409 %s", jid, w.Code, w.Body.String(), usermerge.ProblemUnavailableUnderIdentity)
		}
	}
	if len(r.enq.calls) != 0 {
		t.Fatalf("a refused resume enqueued %d operation(s)", len(r.enq.calls))
	}
	// An undo has no such guard: it stays the permanence refusal (B10b2).
	w, _ := r.do(t, http.MethodPost, "/users/merges/j-applying/undo", nil)
	if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemMergePermanent {
		t.Errorf("undo under identity: %d %s, want 409 %s", w.Code, w.Body.String(), usermerge.ProblemMergePermanent)
	}
}

// Without identity nothing changes: preview, then apply with the previewed
// digest, enqueue as HEAD does.
func TestUserMergeRoutes_WithoutIdentityPreviewAndApplyAreHeads(t *testing.T) {
	r := newMergeRig(t)
	r.flipped(t, false)
	digest := r.previewDigest(t)
	if w, _ := r.do(t, http.MethodPost, r.mergePath("apply"), map[string]any{"digest": digest}); w.Code != http.StatusAccepted {
		t.Fatalf("apply without identity: %d %s, want 202", w.Code, w.Body.String())
	}
	if len(r.enq.calls) != 1 {
		t.Fatalf("enqueued %d operations, want 1", len(r.enq.calls))
	}
}
