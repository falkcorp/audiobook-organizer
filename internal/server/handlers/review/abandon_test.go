// file: internal/server/handlers/review/abandon_test.go
// version: 1.1.0
// guid: a69f495d-437b-4cc8-be64-db0a76852163
// last-edited: 2026-10-18

package reviewhandler_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

// These pin what happens after an apply is interrupted: a failed re-run keeps the item
// held, an operator can abandon it to needs_attention, and requests carrying the
// caller's if_updated_at are refused when the item changed under them.

func abandonReq(t *testing.T, h *reviewhandler.Handler, id string) (int, string) {
	t.Helper()
	w := doReq(t, h.AbandonApply, http.MethodPost, "/review/items/"+id+"/abandon-apply", nil, gin.Params{{Key: "id", Value: id}})
	return w.Code, w.Body.String()
}

// interruptedItem seeds a hold recommending action, claims it as a process that then
// exits, and returns the reopened store: the item is applying under a dead claim.
func interruptedItem(t *testing.T, action string) (*database.PebbleStore, database.ReviewItem) {
	t.Helper()
	dir := t.TempDir()
	s := openStoreAt(t, dir)
	a := seedAction(t, s, "regroup.multidisc", "p1", action)
	if _, err := s.ClaimReviewItemForApply(a.ID, action, time.Time{}); err != nil {
		t.Fatal(err)
	}
	return restart(t, s, dir), a
}

// 🔴 A FAILED RE-RUN DOES NOT MAKE A HALF-APPLIED ITEM DECIDABLE. Was the review's
// TestProbe_TakeoverFailure_ReleaseMakesItemDecidable: the takeover's re-run failed,
// its release handed the item back to pending, and a reject then succeeded over a
// library the interrupted first run may already have changed. Now the item stays held
// as interrupted, and the way out is abandon-apply.
func TestTakeover_FailedRerun_StaysInterrupted_ThenAbandon(t *testing.T) {
	s, a := interruptedItem(t, itunesservice.ActionDuplicateOf)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	var runs atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionDuplicateOf, func(context.Context, database.ReviewItem) error {
		runs.Add(1)
		return errors.New("dedup track names no book outside the folder")
	})

	code, body := rejectReq(t, h, a.ID)
	want409(t, "reject before takeover", code, body, "REVIEW_APPLY_INTERRUPTED")

	code, body = approveReq(t, h, a.ID, nil)
	want409(t, "failed re-run", code, body, "REVIEW_RERUN_FAILED")
	if !strings.Contains(body, "stays held as interrupted") || !strings.Contains(body, "abandon-apply") ||
		!strings.Contains(body, "dedup track names no book") {
		t.Fatalf("failed re-run body %s, want the cause, the held state and abandon-apply", body)
	}
	got, _ := s.GetReviewItem(a.ID)
	if got.Status != database.ReviewStatusApplying || got.ApplyClaimAction != itunesservice.ActionDuplicateOf ||
		got.ApplyClaimToken != "" || got.ApplyClaimInstance != "" {
		t.Fatalf("after a failed re-run = %+v, want applying under a claim no process holds", got)
	}
	for name, do := range map[string]func() (int, string){
		"reject":           func() (int, string) { return rejectReq(t, h, a.ID) },
		"approve separate": func() (int, string) { return approveReq(t, h, a.ID, map[string]any{"action": "separate"}) },
	} {
		code, body := do()
		want409(t, name+" after a failed re-run", code, body, "REVIEW_APPLY_INTERRUPTED")
	}
	// The same process can retry: its own failed re-run is not "in progress".
	code, _ = approveReq(t, h, a.ID, nil)
	if code != http.StatusConflict || runs.Load() != 2 {
		t.Fatalf("retry: code %d runs %d, want a second failing re-run", code, runs.Load())
	}

	code, body = abandonReq(t, h, a.ID)
	if code != http.StatusOK {
		t.Fatalf("abandon: code %d body %s", code, body)
	}
	got, _ = s.GetReviewItem(a.ID)
	if got.Status != database.ReviewStatusNeedsAttention || got.ApplyAbandonedAction != itunesservice.ActionDuplicateOf ||
		got.ApplyClaimAction != "" {
		t.Fatalf("after abandon = %+v, want needs_attention noting the abandoned duplicate-of", got)
	}
	// Decidable now, by a human who has checked the library.
	code, body = rejectReq(t, h, a.ID)
	if code != http.StatusOK {
		t.Fatalf("reject after abandon: code %d body %s", code, body)
	}
}

// An interrupted claim whose action has no apply handler any more cannot be finished by
// approving it again; the 409 points at abandon-apply, which works.
func TestInterrupted_NoApplyHandler_AbandonIsTheWayOut(t *testing.T) {
	s, a := interruptedItem(t, itunesservice.ActionVersionGroup)
	h := reviewhandler.New(s, func() bool { return true }, nil) // nothing registered

	code, body := approveReq(t, h, a.ID, nil)
	want409(t, "approve of an interrupted claim with no handler", code, body, "REVIEW_APPLY_INTERRUPTED")
	if !strings.Contains(body, "abandon-apply") {
		t.Fatalf("the 409 does not name the way out: %s", body)
	}
	code, body = abandonReq(t, h, a.ID)
	if code != http.StatusOK {
		t.Fatalf("abandon: code %d body %s", code, body)
	}
	w := doReq(t, h.GetReviewCount, http.MethodGet, "/review/count", nil, nil)
	data := decodeBody(t, w)["data"].(map[string]any)
	if data["needs_attention"] != float64(1) || data["applying"] != float64(0) {
		t.Fatalf("count = %v, want needs_attention 1, applying 0", data)
	}
	w = doReq(t, h.ListReviewItems, http.MethodGet, "/review/items?status=needs_attention", nil, nil)
	if !strings.Contains(w.Body.String(), a.ID) || !strings.Contains(w.Body.String(), `"apply_abandoned_action":"version-group"`) {
		t.Fatalf("needs_attention list = %s", w.Body.String())
	}
}

func TestAbandon_RefusesEverythingButAnInterruptedClaim(t *testing.T) {
	s := newTestStore(t)
	open := seedAction(t, s, "regroup.multidisc", "open", itunesservice.ActionCombine)
	done := seedAction(t, s, "regroup.multidisc", "done", itunesservice.ActionCombine)
	live := seedAction(t, s, "regroup.multidisc", "live", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	ba := newBlockingApply()
	h.RegisterApplyHandler(itunesservice.ActionCombine, ba.fn)
	ba.calls.Store(1) // the first call does not block
	if code, body := approveReq(t, h, done.ID, nil); code != http.StatusOK {
		t.Fatalf("seed applied: %d %s", code, body)
	}
	ba.calls.Store(0)

	var wg sync.WaitGroup
	wg.Go(func() { approveReq(t, h, live.ID, nil) })
	<-ba.entered

	code, body := abandonReq(t, h, live.ID)
	want409(t, "abandon of a running apply", code, body, "REVIEW_APPLY_IN_PROGRESS")
	code, body = abandonReq(t, h, done.ID)
	want409(t, "abandon of an applied item", code, body, "REVIEW_ALREADY_APPLIED")
	code, body = abandonReq(t, h, open.ID)
	want409(t, "abandon of an item under no claim", code, body, "REVIEW_APPLY_NOT_INTERRUPTED")
	if code, _ := abandonReq(t, h, "nope"); code != http.StatusNotFound {
		t.Fatalf("abandon of a missing item: code %d, want 404", code)
	}
	close(ba.release)
	wg.Wait()
	assertAppliedOnce(t, s, live.ID, ba.calls.Load(), itunesservice.ActionCombine)
}

// An apply that succeeded in this process but is not recorded yet is applied, not
// interrupted: abandoning it would send someone to inspect a change that completed.
func TestAbandon_UnrecordedApplyIsAlreadyApplied(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	fs := &flakyStore{PebbleStore: s}
	fs.failFinish.Store(true)
	h := reviewhandler.New(fs, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { return nil })
	if code, _ := approveReq(t, h, a.ID, nil); code != http.StatusInternalServerError {
		t.Fatalf("seed unrecorded: code %d", code)
	}
	w := doReq(t, h.AbandonApply, http.MethodPost, "/review/items/"+a.ID+"/abandon-apply", nil, gin.Params{{Key: "id", Value: a.ID}})
	want409(t, "abandon of an unrecorded apply", w.Code, w.Body.String(), "REVIEW_ALREADY_APPLIED")
	// One response: a handler that answered and then fell through to the store would
	// append a second (in progress) body, which decodeBody refuses.
	if body := decodeBody(t, w); body["code"] != "REVIEW_ALREADY_APPLIED" {
		t.Fatalf("abandon of an unrecorded apply answered %v", body)
	}
}

// 🔴 A TOKEN FOR AN APPLY ANOTHER REQUEST RECORDED IS DROPPED BY THE NEXT APPROVE.
// The other request's finish lands between this process's failed finish and the next
// approve, which then reads the item as applied. Kept, the token would make every
// later reject answer "its result is not recorded yet" about an applied item.
func TestApprove_AppliedWithAStaleUnrecordedToken_DropsTheToken(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	fs := &flakyStore{PebbleStore: s}
	fs.failFinish.Store(true)
	h := reviewhandler.New(fs, func() bool { return true }, nil)
	var calls atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { calls.Add(1); return nil })
	if code, _ := approveReq(t, h, a.ID, nil); code != http.StatusInternalServerError {
		t.Fatalf("seed unrecorded: code %d", code)
	}
	held, _ := s.GetReviewItem(a.ID) // the store's copy carries the token
	if _, err := s.FinishReviewItemApply(a.ID, held.ApplyClaimToken); err != nil {
		t.Fatalf("the other request's finish: %v", err)
	}
	fs.failFinish.Store(false)

	code, body := approveReq(t, h, a.ID, nil)
	want409(t, "approve of an applied item", code, body, "REVIEW_ALREADY_APPLIED")
	code, body = rejectReq(t, h, a.ID)
	want409(t, "reject of an applied item", code, body, "REVIEW_ALREADY_APPLIED")
	if strings.Contains(body, "not recorded") {
		t.Fatalf("reject still reports the result unrecorded: %s", body)
	}
	assertAppliedOnce(t, s, a.ID, calls.Load(), itunesservice.ActionCombine)
}

// staleFinishStore plays the losing side of two requests racing to record one
// unrecorded apply: the handler's read still shows the item applying, and the other
// request's finish (with the same token) lands just before this one's.
type staleFinishStore struct {
	*database.PebbleStore
	failFinish atomic.Bool
	raceOnce   sync.Once
}

func (f *staleFinishStore) FinishReviewItemApply(id, token string) (*database.ReviewItem, error) {
	if f.failFinish.Load() {
		return nil, errors.New("synthetic pebble write error")
	}
	f.raceOnce.Do(func() {
		_, _ = f.PebbleStore.FinishReviewItemApply(id, token) // the winner
	})
	return f.PebbleStore.FinishReviewItemApply(id, token)
}

// 🔴 THE LOSER OF A RACE TO RECORD AN UNRECORDED APPLY ANSWERS "ALREADY APPLIED". It
// used to answer 500 REVIEW_APPLIED_UNRECORDED (sending the reviewer to approve again
// for nothing), and it kept nothing wrong only by luck: had the stale token been kept,
// a reject would keep answering "its result is not recorded yet" about an applied item.
func TestUnrecordedFinish_LosingARace_Is409AlreadyApplied_AndDropsTheToken(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	fs := &staleFinishStore{PebbleStore: s}
	fs.failFinish.Store(true)
	h := reviewhandler.New(fs, func() bool { return true }, nil)
	var calls atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { calls.Add(1); return nil })
	if code, _ := approveReq(t, h, a.ID, nil); code != http.StatusInternalServerError {
		t.Fatalf("seed unrecorded: code %d", code)
	}
	fs.failFinish.Store(false)

	code, body := approveReq(t, h, a.ID, nil)
	want409(t, "re-approve that lost the race to record", code, body, "REVIEW_ALREADY_APPLIED")
	code, body = rejectReq(t, h, a.ID)
	want409(t, "reject after the race", code, body, "REVIEW_ALREADY_APPLIED")
	if strings.Contains(body, "not recorded") {
		t.Fatalf("reject still reports the result unrecorded after it was recorded: %s", body)
	}
	assertAppliedOnce(t, s, a.ID, calls.Load(), itunesservice.ActionCombine)
}

// The same race for real: concurrent re-approves of one unrecorded apply. Exactly one
// records it; every other answers 409 already-applied, and the apply never re-runs.
func TestUnrecordedFinish_ConcurrentReapproves_RecordOnceNo500(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	fs := &flakyStore{PebbleStore: s}
	fs.failFinish.Store(true)
	h := reviewhandler.New(fs, func() bool { return true }, nil)
	var calls atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { calls.Add(1); return nil })
	if code, _ := approveReq(t, h, a.ID, nil); code != http.StatusInternalServerError {
		t.Fatalf("seed unrecorded: code %d", code)
	}
	fs.failFinish.Store(false)
	codes := make([]int, 8)
	bodies := make([]string, 8)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Go(func() { codes[i], bodies[i] = approveReq(t, h, a.ID, nil) })
	}
	wg.Wait()
	ok := 0
	for i, code := range codes {
		switch {
		case code == http.StatusOK:
			ok++
		case code == http.StatusConflict && strings.Contains(bodies[i], "REVIEW_ALREADY_APPLIED"):
		default:
			t.Fatalf("re-approve %d: code %d body %s, want 200 or 409 already-applied", i, code, bodies[i])
		}
	}
	if ok != 1 {
		t.Fatalf("%d re-approves recorded the apply, want exactly 1 (codes %v)", ok, codes)
	}
	assertAppliedOnce(t, s, a.ID, calls.Load(), itunesservice.ActionCombine)
}

// ── if_updated_at ────────────────────────────────────────────────────────────────

func stamp(t *testing.T, s *database.PebbleStore, id string) string {
	t.Helper()
	it, err := s.GetReviewItem(id)
	if err != nil || it == nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return it.UpdatedAt.Format(time.RFC3339Nano)
}

// rescan refreshes a pending hold's payload the way a producer re-scan does, which
// moves its updated_at.
func rescan(t *testing.T, s *database.PebbleStore, it database.ReviewItem, action string) {
	t.Helper()
	time.Sleep(2 * time.Millisecond)
	if _, err := s.UpsertReviewItem(database.ReviewItem{Kind: it.Kind, DedupKey: it.DedupKey, FolderRef: it.FolderRef,
		Summary: "rescanned", Payload: `{"recommendedAction":"` + action + `"}`}); err != nil {
		t.Fatal(err)
	}
}

// 🔴 A DECISION IS MADE ON WHAT THE REVIEWER SAW. The page rendered a `separate`
// recommendation; a re-scan then changed it to `combine`. An approve with an empty body
// carrying the rendered updated_at must be refused, not merge books on a recommendation
// the reviewer never read. Each write path refuses the same way.
func TestIfUpdatedAt_StaleViewIsRefusedOnEveryPath(t *testing.T) {
	s := newTestStore(t)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	var combined atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { combined.Add(1); return nil })

	approveIt := seedAction(t, s, "regroup.multidisc", "approve", itunesservice.ActionSeparate)
	separateIt := seedAction(t, s, "regroup.multidisc", "separate", itunesservice.ActionCombine)
	rejectIt := seedAction(t, s, "regroup.multidisc", "reject", itunesservice.ActionCombine)
	bulkIt := seedAction(t, s, "regroup.multidisc", "bulk", itunesservice.ActionSeparate)
	seen := map[string]string{}
	for _, it := range []database.ReviewItem{approveIt, separateIt, rejectIt, bulkIt} {
		seen[it.ID] = stamp(t, s, it.ID)
		rescan(t, s, it, itunesservice.ActionCombine)
	}

	code, body := approveReq(t, h, approveIt.ID, map[string]any{"if_updated_at": seen[approveIt.ID]})
	want409(t, "approve on a stale view", code, body, "REVIEW_ITEM_CHANGED")
	code, body = approveReq(t, h, separateIt.ID, map[string]any{"action": "separate", "if_updated_at": seen[separateIt.ID]})
	want409(t, "separate on a stale view", code, body, "REVIEW_ITEM_CHANGED")
	w := doReq(t, h.RejectReviewItem, http.MethodPost, "/review/items/"+rejectIt.ID+"/reject",
		map[string]any{"if_updated_at": seen[rejectIt.ID]}, gin.Params{{Key: "id", Value: rejectIt.ID}})
	want409(t, "reject on a stale view", w.Code, w.Body.String(), "REVIEW_ITEM_CHANGED")
	w = doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk", map[string]any{
		"action": "approve", "ids": []string{bulkIt.ID}, "if_updated_at": map[string]string{bulkIt.ID: seen[bulkIt.ID]},
	}, nil)
	data := decodeBody(t, w)["data"].(map[string]any)
	skipped, _ := data["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["code"] != "REVIEW_ITEM_CHANGED" {
		t.Fatalf("bulk on a stale view = %v, want one skip coded REVIEW_ITEM_CHANGED", data)
	}
	if combined.Load() != 0 {
		t.Fatalf("combine ran %d times on a recommendation nobody saw", combined.Load())
	}
	for _, it := range []database.ReviewItem{approveIt, separateIt, rejectIt, bulkIt} {
		if got, _ := s.GetReviewItem(it.ID); got.Status != database.ReviewStatusPending {
			t.Fatalf("%s: a refused decision wrote %q", it.DedupKey, got.Status)
		}
	}

	// With the current stamp each goes through.
	code, body = approveReq(t, h, approveIt.ID, map[string]any{"if_updated_at": stamp(t, s, approveIt.ID)})
	if code != http.StatusOK || combined.Load() != 1 {
		t.Fatalf("approve on the current view: code %d body %s combined %d", code, body, combined.Load())
	}
	w = doReq(t, h.RejectReviewItem, http.MethodPost, "/review/items/"+rejectIt.ID+"/reject",
		map[string]any{"if_updated_at": stamp(t, s, rejectIt.ID)}, gin.Params{{Key: "id", Value: rejectIt.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("reject on the current view: code %d body %s", w.Code, w.Body.String())
	}
}

func TestIfUpdatedAt_BulkByKindIsRefused(t *testing.T) {
	s := newTestStore(t)
	it := seedAction(t, s, "regroup.multidisc", "k", itunesservice.ActionSeparate)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk", map[string]any{
		"action": "approve", "kind": "regroup.multidisc", "if_updated_at": map[string]string{it.ID: stamp(t, s, it.ID)},
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("kind-scoped bulk with if_updated_at: code %d body %s, want 400", w.Code, w.Body.String())
	}
}

// ── responses ────────────────────────────────────────────────────────────────────

// The claim token can finish or release a claim and the instance id is the process's
// identity; no response carries either.
func TestResponses_NeverCarryTheClaimTokenOrInstance(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	held := seedAction(t, s, "regroup.multidisc", "m2", itunesservice.ActionCombine)
	c, err := s.ClaimReviewItemForApply(held.ID, itunesservice.ActionCombine, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { return nil })

	bodies := map[string]string{}
	w := doReq(t, h.ListReviewItems, http.MethodGet, "/review/items?status=all", nil, nil)
	bodies["list"] = w.Body.String()
	_, bodies["approve"] = approveReq(t, h, a.ID, nil)
	b := seedAction(t, s, "regroup.multidisc", "m3", itunesservice.ActionCombine)
	_, bodies["reject"] = rejectReq(t, h, b.ID)
	if !strings.Contains(bodies["list"], held.ID) || !strings.Contains(bodies["list"], `"apply_claim_action":"combine"`) {
		t.Fatalf("list does not show the held item and its action: %s", bodies["list"])
	}
	for name, body := range bodies {
		if strings.Contains(body, c.Token) || strings.Contains(body, "apply_claim_token") ||
			strings.Contains(body, "apply_claim_instance") {
			t.Fatalf("%s response exposes the claim token or instance: %s", name, body)
		}
	}
}

// conflictStore answers every decision with a conflict whose reason the handler does
// not know (a reason added to the store and not to the handler).
type conflictStore struct{ *database.PebbleStore }

func (c *conflictStore) DecideReviewItem(id, status, _ string, _ time.Time) (*database.ReviewItem, error) {
	return nil, &database.ReviewClaimConflictError{ID: id, Status: "weird", Reason: "something-new"}
}

func TestConflict_UnknownReasonIsNotReportedAsInProgress(t *testing.T) {
	s := newTestStore(t)
	a := seed(t, s, "regroup.multidisc", "m1")
	h := reviewhandler.New(&conflictStore{PebbleStore: s}, func() bool { return true }, nil)
	code, body := rejectReq(t, h, a.ID)
	want409(t, "unknown conflict reason", code, body, "REVIEW_CONFLICT")
	if strings.Contains(body, "REVIEW_APPLY_IN_PROGRESS") {
		t.Fatalf("an unknown conflict is reported as an apply in progress: %s", body)
	}
}
