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

// A server wired for core identity WITHOUT the core handles the merge runs on
// fails closed: preview and apply answer 409 merge_unavailable_under_identity
// before any account is resolved, any body is read, any plan is built or any
// grant is minted, and so does the footprint (which has no core read at all).
// Resume and undo are B10b2's merge_permanent, and the merge listing stays
// readable. With the handles wired, the merge runs (identity_merge_test.go).

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

func TestUserMergeRoutes_FootprintIsUnavailableUnderIdentity(t *testing.T) {
	r := newMergeRig(t)
	// Before the flip the footprint reads.
	if w, _ := r.do(t, http.MethodGet, "/users/"+r.absorbed.ID+"/footprint", nil); w.Code != http.StatusOK {
		t.Fatalf("footprint before the flip: %d %s", w.Code, w.Body.String())
	}
	r.flipped(t, true)
	// An account that does not exist: without the guard this is a 404 from the
	// account lookup, so a 409 proves the guard ran before it.
	for _, id := range []string{r.absorbed.ID, "u-nope"} {
		w, _ := r.do(t, http.MethodGet, "/users/"+id+"/footprint", nil)
		if w.Code != http.StatusConflict || problemCode(t, w.Body.Bytes()) != usermerge.ProblemUnavailableUnderIdentity {
			t.Errorf("footprint %s under identity: %d %s, want 409 %s", id, w.Code, w.Body.String(), usermerge.ProblemUnavailableUnderIdentity)
		}
	}
	// The merge history is accurate pre-flip data and stays readable.
	if w, _ := r.do(t, http.MethodGet, "/users/merges", nil); w.Code != http.StatusOK {
		t.Errorf("listing merges under identity: %d, want 200", w.Code)
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

func TestUserMergeRoutes_ResumeIsPermanentUnderIdentity(t *testing.T) {
	r := newMergeRig(t)
	r.flipped(t, true)
	r.journal(t, "j-applying", journalJSON("j-applying", usermerge.StatusApplying))
	// A journal that exists, one that does not and one that cannot be decoded:
	// an identity-era merge is one batch, so there is nothing to resume, and a
	// pre-flip journal's remaining ops write keys the cutover retired. Resume is
	// the permanence refusal for a journal that is there (decodable or not); a
	// journal that is not there stays a 404.
	r.journal(t, "j-garbled", `{not json`)
	for jid, want := range map[string]int{"j-applying": http.StatusConflict, "j-garbled": http.StatusConflict, "j-nope": http.StatusNotFound} {
		w, _ := r.do(t, http.MethodPost, "/users/merges/"+jid+"/resume", nil)
		if w.Code != want {
			t.Errorf("resume %s under identity: %d %s, want %d", jid, w.Code, w.Body.String(), want)
		}
		if want == http.StatusConflict && problemCode(t, w.Body.Bytes()) != usermerge.ProblemMergePermanent {
			t.Errorf("resume %s under identity: %s, want code %s", jid, w.Body.String(), usermerge.ProblemMergePermanent)
		}
	}
	if len(r.enq.calls) != 0 {
		t.Fatalf("a refused resume enqueued %d operation(s)", len(r.enq.calls))
	}
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
