// file: internal/server/handlers/user_merge_permanent_test.go
// version: 0.1.0
// guid: 5e3d7e09-0bae-47d9-95a2-1397e6a42a7e
// last-edited: 2026-11-21

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/usermerge"
)

// F4-08 B10b2 (A9), narrowed by B10p: once the identity flip has happened a
// merge journal with no core half (one written before the flip), or one that
// cannot be decoded, is permanent. The journal routes ask the service before
// they read a journal, so an undo or a resume of such a journal answers 409
// merge_permanent for any status and enqueues nothing. A journal written after
// the flip is undone by core (user_merge_unmerge_test.go).

func (r *mergeRig) flipped(t *testing.T, flipped bool) {
	t.Helper()
	svc, err := usermerge.New(r.store, usermerge.WithIdentityFlipped(func() bool { return flipped }))
	if err != nil {
		t.Fatal(err)
	}
	r.h.SetUserMerge(svc, r.enq, r.grants)
}

func (r *mergeRig) journal(t *testing.T, id, raw string) {
	t.Helper()
	if err := r.store.SetRaw(database.UserMergeJournalPrefix+id, []byte(raw)); err != nil {
		t.Fatal(err)
	}
}

func journalJSON(id, status string) string {
	return `{"id":"` + id + `","status":"` + status + `","survivor_id":"u-bruce","absorbed_id":"u-dick"}`
}

func TestUserMergeRoutes_FlippedJournalActionsArePermanent(t *testing.T) {
	r := newMergeRig(t)
	r.flipped(t, true)
	r.journal(t, "j-applying", journalJSON("j-applying", usermerge.StatusApplying))
	r.journal(t, "j-applied", journalJSON("j-applied", usermerge.StatusApplied))
	r.journal(t, "j-undone", journalJSON("j-undone", usermerge.StatusUndone))
	// Reading is not a reversal: it stands (checked before the undecodable row
	// below, which ListJournals itself cannot read past). A new merge is not
	// available until B10l ports it onto core (user_merge_unavailable_test.go).
	if w, _ := r.do(t, http.MethodGet, "/users/merges", nil); w.Code != http.StatusOK {
		t.Errorf("listing merges after the flip: %d", w.Code)
	}
	r.journal(t, "j-garbled", `{not json`)
	for _, jid := range []string{"j-applying", "j-applied", "j-undone", "j-garbled"} {
		// Without a core handle wired a resume is answered by the unavailable guard
		// first (user_merge_unavailable_test.go); only an undo reaches the
		// permanence check here, so only it is pinned in this rig.
		for _, action := range []string{"undo"} {
			w, _ := r.do(t, http.MethodPost, "/users/merges/"+jid+"/"+action, nil)
			if w.Code != http.StatusConflict {
				t.Errorf("%s %s after the flip: %d %s, want 409", action, jid, w.Code, w.Body.String())
				continue
			}
			var body struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if body.Code != usermerge.ProblemMergePermanent {
				t.Errorf("%s %s: problem code %q, want %q", action, jid, body.Code, usermerge.ProblemMergePermanent)
			}
		}
	}
	if w, _ := r.do(t, http.MethodPost, "/users/merges/j-nope/undo", nil); w.Code != http.StatusNotFound {
		t.Errorf("a journal that does not exist after the flip: %d, want 404", w.Code)
	}
	if len(r.enq.calls) != 0 {
		t.Fatalf("a permanent merge enqueued %d operation(s)", len(r.enq.calls))
	}
}

func TestUserMergeRoutes_NotFlippedJournalActionsAreHeads(t *testing.T) {
	r := newMergeRig(t)
	r.flipped(t, false)
	r.journal(t, "j-applying", journalJSON("j-applying", usermerge.StatusApplying))
	if w, _ := r.do(t, http.MethodPost, "/users/merges/j-applying/resume", nil); w.Code != http.StatusAccepted {
		t.Fatalf("resume before the flip: %d %s, want 202", w.Code, w.Body.String())
	}
	if w, _ := r.do(t, http.MethodPost, "/users/merges/j-applying/undo", nil); w.Code != http.StatusAccepted {
		t.Fatalf("undo before the flip: %d, want 202", w.Code)
	}
	// An undecodable journal is HEAD's own load failure, not the permanent refusal.
	r.journal(t, "j-garbled", `{not json`)
	if w, _ := r.do(t, http.MethodPost, "/users/merges/j-garbled/undo", nil); w.Code == http.StatusConflict {
		t.Errorf("a garbled journal before the flip answered 409: %s", w.Body.String())
	}
}
