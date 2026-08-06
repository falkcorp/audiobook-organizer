// file: internal/server/handlers/review/handler_action_test.go
// version: 1.0.0
// guid: 5c1f0a83-6d47-4e29-b0a5-3f7c8e2d94b1
// last-edited: 2026-08-06

// Tests for ACTION-KEYED approve dispatch (owner item 2, 2026-08-06).
//
// The contract under test: approve dispatches on the action a human CHOSE, or on
// the hold's own recommendation when they chose nothing — never on ReviewItem.Kind.
// The Kind strings themselves are untouched and still what the frontend maps.
//
// Several of these assert a NEGATIVE (the apply handler was NOT invoked). That is
// deliberate: the failure this whole change prevents is a merge nobody asked for,
// and the only way to test "it did not merge" is to register a fake under the
// action that would have merged and assert its counter stayed at zero.

package reviewhandler_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

// approveBody posts an approve with an explicit {"action": ...} body. Passing ""
// posts no body at all, which is the pre-override frontend's request shape.
func approveBody(t *testing.T, h *reviewhandler.Handler, id, action string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var body any
	if action != "" {
		body = map[string]any{"action": action}
	}
	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+id+"/approve", body,
		gin.Params{{Key: "id", Value: id}})
	return w, decodeBody(t, w)
}

// doReqRaw posts a body that is not necessarily valid JSON, which doReq (which
// marshals) cannot express.
func doReqRaw(t *testing.T, fn gin.HandlerFunc, method, url, body string, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(method, url, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	c.Params = params
	fn(c)
	return w
}

// counters records which apply path ran, so a test can assert both that the right
// one fired AND that the wrong one did not.
type counters struct {
	combine      int
	versionGroup int
}

// newActionHandler wires a handler with fake apply funcs registered under the two
// real actions, mirroring wire_handlers.go.
func newActionHandler(s *database.PebbleStore, applyOn bool) (*reviewhandler.Handler, *counters) {
	c := &counters{}
	h := reviewhandler.New(s, func() bool { return applyOn })
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, _ database.ReviewItem) error {
		c.combine++
		return nil
	})
	h.RegisterApplyHandler(itunesservice.ActionVersionGroup, func(_ context.Context, _ database.ReviewItem) error {
		c.versionGroup++
		return nil
	})
	return h, c
}

// 🔴 THE POINT OF OWNER ITEM 2. The body's action beats the recommendation. A hold
// the classifier wants combined, that a human recognises as two editions, must run
// the version-group path — and must NOT run the combine path, which hard-deletes
// absorbed rows.
func TestApprove_ExplicitActionOverridesRecommendation(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h, c := newActionHandler(s, true)

	w, body := approveBody(t, h, it.ID, itunesservice.ActionVersionGroup)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if c.versionGroup != 1 {
		t.Fatalf("version-group apply ran %d times, want 1", c.versionGroup)
	}
	if c.combine != 0 {
		t.Fatalf("combine apply ran %d times — the override was ignored and books would have merged", c.combine)
	}
	data := body["data"].(map[string]any)
	if data["chosenAction"] != itunesservice.ActionVersionGroup {
		t.Fatalf("chosenAction = %v, want version-group", data["chosenAction"])
	}
}

// No body → the hold's own recommendation is what runs.
func TestApprove_EmptyBodyUsesRecommendation(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.version-group", "v1", itunesservice.ActionVersionGroup)
	h, c := newActionHandler(s, true)

	w, body := approveBody(t, h, it.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if c.versionGroup != 1 || c.combine != 0 {
		t.Fatalf("counters = %+v, want the recommendation (version-group) only", *c)
	}
	if data := body["data"].(map[string]any); data["chosenAction"] != itunesservice.ActionVersionGroup {
		t.Fatalf("chosenAction = %v, want version-group", data["chosenAction"])
	}
}

// 🔴 A typo must NOT fall back to the recommendation. "seperate" from someone who
// meant "leave these six novels apart" cannot become "combine".
func TestApprove_UnknownAction_Rejected_NoMutation(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h, c := newActionHandler(s, true)

	w, _ := approveBody(t, h, it.ID, "seperate")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400 for an unknown action", w.Code)
	}
	if c.combine != 0 || c.versionGroup != 0 {
		t.Fatalf("counters = %+v, want nothing applied on a rejected action", *c)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusPending {
		t.Fatalf("status = %q, want pending — a rejected action must not decide the hold", got.Status)
	}
}

// insufficient-evidence is a statement BY the machine. A human may not pick it,
// explicitly or by default.
func TestApprove_InsufficientEvidence_NotApprovable(t *testing.T) {
	s := newTestStore(t)
	explicit := seedAction(t, s, "regroup.ambiguous", "a1", itunesservice.ActionCombine)
	byDefault := seedAction(t, s, "regroup.ambiguous", "a2", itunesservice.ActionInsufficientEvidence)
	h, c := newActionHandler(s, true)

	w, _ := approveBody(t, h, explicit.ID, itunesservice.ActionInsufficientEvidence)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("explicit: code %d, want 400", w.Code)
	}
	w, _ = approveBody(t, h, byDefault.ID, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("default: code %d, want 400 — the machine cannot tell, so a human must say", w.Code)
	}
	if c.combine != 0 || c.versionGroup != 0 {
		t.Fatalf("counters = %+v, want nothing applied", *c)
	}
	for _, id := range []string{explicit.ID, byDefault.ID} {
		got, _ := s.GetReviewItem(id)
		if got.Status != database.ReviewStatusPending {
			t.Fatalf("%s status = %q, want pending", id, got.Status)
		}
	}
}

// 🔴 OLD HOLDS. Every one of the ~356 holds in prod's queue was written before
// recommendations existed. They must decode cleanly and behave as
// insufficient-evidence — refused until a human names an action — never dispatch to
// a merge on the strength of a field they do not carry.
func TestApprove_OldPayloadWithoutRecommendation_IsInsufficientEvidence(t *testing.T) {
	s := newTestStore(t)
	old := seed(t, s, "regroup.multidisc", "m1") // payload "{}", the pre-2026-08-06 shape
	h, c := newActionHandler(s, true)

	w, _ := approveBody(t, h, old.ID, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400 — an old hold has no recommendation to act on", w.Code)
	}
	if c.combine != 0 {
		t.Fatalf("combine ran %d times on a hold carrying no recommendation", c.combine)
	}

	// …but an explicit action still works, which is how the queue gets drained
	// without a payload migration.
	w, _ = approveBody(t, h, old.ID, itunesservice.ActionCombine)
	if w.Code != http.StatusOK {
		t.Fatalf("explicit action on an old hold: code %d body %s", w.Code, w.Body.String())
	}
	if c.combine != 1 {
		t.Fatalf("combine ran %d times, want 1", c.combine)
	}
}

// 🔴 separate IS a status transition and nothing else. The assertion that matters
// is the negative one: the combine apply path must not be invoked, even with the
// global switch ON and a combine handler registered.
func TestApprove_Separate_TransitionsWithoutApplying(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionSeparate)
	h, c := newActionHandler(s, true) // switch ON — separate still applies nothing

	w, body := approveBody(t, h, it.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if c.combine != 0 || c.versionGroup != 0 {
		t.Fatalf("counters = %+v — 'separate' must never invoke an apply path", *c)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApproved {
		t.Fatalf("status = %q, want approved so re-scans leave the folder alone", got.Status)
	}
	data := body["data"].(map[string]any)
	if data["chosenAction"] != itunesservice.ActionSeparate {
		t.Fatalf("chosenAction = %v, want separate", data["chosenAction"])
	}
	if data["note"] == nil {
		t.Fatal("expected a note explaining that separate needs no apply step")
	}
}

// duplicate-of has no apply path yet. Refuse loudly rather than marking the hold
// decided while doing nothing — "decided" is sticky across re-scans.
func TestApprove_DuplicateOf_RejectedAsUnimplemented(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.ambiguous", "a1", itunesservice.ActionCombine)
	h, c := newActionHandler(s, true)

	w, _ := approveBody(t, h, it.ID, itunesservice.ActionDuplicateOf)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("code %d, want 501 for an action with no implementation", w.Code)
	}
	if c.combine != 0 {
		t.Fatalf("combine ran %d times on a rejected duplicate-of", c.combine)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusPending {
		t.Fatalf("status = %q, want pending — an unimplemented action must not decide the hold", got.Status)
	}
}

// Anthology holds routed to the combine path under Kind dispatch (one anthology =
// one book, owner decision 2026-07-26). Action dispatch must preserve that.
func TestApprove_AnthologyRecommendingCombine_StillCombines(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.anthology", "an1", itunesservice.ActionCombine)
	h, c := newActionHandler(s, true)

	if w, _ := approveBody(t, h, it.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if c.combine != 1 {
		t.Fatalf("combine ran %d times, want 1 — anthology routing regressed", c.combine)
	}
}

// 🔴 DOCUMENTS A DELIBERATE WIDENING. Under Kind dispatch `regroup.ambiguous` had
// no handler and could never merge. Keyed by action, an ambiguous hold whose
// evidence says combine now reaches the combine path. That is intended — the
// recommendation is evidence-backed where the Kind was only a shape — and this test
// exists so the change is visible rather than discovered in an incident.
func TestApprove_AmbiguousRecommendingCombine_NowCombines(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.ambiguous", "a1", itunesservice.ActionCombine)
	h, c := newActionHandler(s, true)

	if w, _ := approveBody(t, h, it.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if c.combine != 1 {
		t.Fatalf("combine ran %d times, want 1", c.combine)
	}
}

// 🔴 review_apply_enabled IS STILL THE GATE. With the switch OFF, an explicit
// override to combine records the decision and executes NOTHING.
func TestApprove_ApplyGateOff_OverrideRecordsButDoesNotApply(t *testing.T) {
	s := newTestStore(t)
	// Recommendation is insufficient-evidence, so the override-persistence guard
	// does not fire (a later replay would skip this hold either way) and we get to
	// test the apply gate itself.
	it := seed(t, s, "regroup.ambiguous", "a1")
	h, c := newActionHandler(s, false) // switch OFF

	w, body := approveBody(t, h, it.ID, itunesservice.ActionCombine)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	if c.combine != 0 {
		t.Fatalf("combine apply ran %d times with review_apply_enabled OFF", c.combine)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusApproved {
		t.Fatalf("status = %q, want approved (recorded, not applied)", got.Status)
	}
	if body["data"].(map[string]any)["note"] == nil {
		t.Fatal("expected the review-only note explaining nothing was applied")
	}
}

// 🔴 AN OVERRIDE THAT CANNOT BE PERSISTED IS REFUSED. With the switch off the
// chosen action is stored nowhere, and ReplayApprovedItems re-resolves from the
// payload — so recording "approved" here would let a later replay run `combine` on
// books a human said to keep apart.
func TestApprove_OverrideOfReplayableRecommendation_RefusedWhileApplyIsOff(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h, c := newActionHandler(s, false) // switch OFF

	w, _ := approveBody(t, h, it.ID, itunesservice.ActionSeparate)
	if w.Code != http.StatusConflict {
		t.Fatalf("code %d, want 409 — the override would be silently lost and replayed as combine", w.Code)
	}
	if c.combine != 0 {
		t.Fatalf("combine ran %d times", c.combine)
	}
	got, _ := s.GetReviewItem(it.ID)
	if got.Status != database.ReviewStatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}

	// The same override with the switch ON applies immediately, so there is nothing
	// left for a replay to get wrong.
	hOn, cOn := newActionHandler(s, true)
	if w, _ := approveBody(t, hOn, it.ID, itunesservice.ActionSeparate); w.Code != http.StatusOK {
		t.Fatalf("switch on: code %d body %s", w.Code, w.Body.String())
	}
	if cOn.combine != 0 {
		t.Fatalf("combine ran %d times on a separate override", cOn.combine)
	}
}

// 🔴 BULK APPROVE USES EACH ITEM'S OWN RECOMMENDATION and must not abort on one
// undecidable hold. This is the whole payoff: the decisive holds get processed and
// the ones with no evidence are reported for individual attention.
func TestBulkApprove_UsesPerItemRecommendation_AndSkipsUndecidable(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	b := seedAction(t, s, "regroup.multidisc", "m2", itunesservice.ActionSeparate)
	c := seed(t, s, "regroup.multidisc", "m3") // old payload → insufficient-evidence
	h, cnt := newActionHandler(s, true)

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "kind": "regroup.multidisc"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}
	data := decodeBody(t, w)["data"].(map[string]any)
	if data["processed"].(float64) != 2 {
		t.Fatalf("processed = %v, want 2 (the two decidable holds)", data["processed"])
	}
	skipped, _ := data["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want the one undecidable hold (body %s)", skipped, w.Body.String())
	}
	if got := skipped[0].(map[string]any)["id"]; got != c.ID {
		t.Fatalf("skipped id = %v, want %s", got, c.ID)
	}
	// Only the combine hold reached an apply path; the separate hold was a status
	// transition and the undecidable one was skipped.
	if cnt.combine != 1 {
		t.Fatalf("combine ran %d times, want exactly 1", cnt.combine)
	}
	applied, _ := s.GetReviewItem(a.ID)
	if applied.Status != database.ReviewStatusApplied {
		t.Fatalf("combine hold status = %q, want applied", applied.Status)
	}
	sep, _ := s.GetReviewItem(b.ID)
	if sep.Status != database.ReviewStatusApproved {
		t.Fatalf("separate hold status = %q, want approved", sep.Status)
	}
	stillPending, _ := s.GetReviewItem(c.ID)
	if stillPending.Status != database.ReviewStatusPending {
		t.Fatalf("skipped hold status = %q, want pending — a skip must not decide it", stillPending.Status)
	}
}

// A malformed body is a 400, not a silent run of the recommendation: a caller who
// believes they overrode the action must never get the default instead.
func TestApprove_MalformedBody_Rejected(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h, c := newActionHandler(s, true)

	w := doReqRaw(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+it.ID+"/approve",
		"{not json", gin.Params{{Key: "id", Value: it.ID}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400 for a malformed body", w.Code)
	}
	if c.combine != 0 {
		t.Fatalf("combine ran %d times on a malformed request", c.combine)
	}
}
