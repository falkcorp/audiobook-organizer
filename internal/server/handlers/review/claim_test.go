// file: internal/server/handlers/review/claim_test.go
// version: 1.3.0
// guid: 4ac78f22-e4e5-46e6-963d-64243d9d758f
// last-edited: 2026-10-18

package reviewhandler_test

import (
	"context"
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

// These tests pin the apply claim from the handler's side: one apply per item, no
// decision landing over a running apply, and a claim left by a dead process being
// recoverable. Run with -race. Each scenario that once double-applied is reproduced
// with gates rather than sleeps, so the interleaving is the one under test every run.

// blockingApply counts calls; the first one signals entered and waits for release.
type blockingApply struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func newBlockingApply() *blockingApply {
	return &blockingApply{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingApply) fn(_ context.Context, _ database.ReviewItem) error {
	if b.calls.Add(1) == 1 {
		close(b.entered)
		<-b.release
	}
	return nil
}

// gateDecide holds a chosen decision write at the store boundary until gate closes:
// the decision STARTED before the apply claimed the item, and its write lands while
// the apply runs — the interleaving that used to overwrite the claim.
type gateDecide struct {
	*database.PebbleStore
	gate   <-chan struct{}
	status string
	chosen string
}

func (g *gateDecide) DecideReviewItem(id, status, chosen string, ifUpdatedAt time.Time) (*database.ReviewItem, error) {
	if status == g.status && chosen == g.chosen {
		<-g.gate
	}
	return g.PebbleStore.DecideReviewItem(id, status, chosen, ifUpdatedAt)
}

func approveReq(t *testing.T, h *reviewhandler.Handler, id string, body any) (int, string) {
	t.Helper()
	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/review/items/"+id+"/approve", body, gin.Params{{Key: "id", Value: id}})
	return w.Code, w.Body.String()
}

func rejectReq(t *testing.T, h *reviewhandler.Handler, id string) (int, string) {
	t.Helper()
	w := doReq(t, h.RejectReviewItem, http.MethodPost, "/review/items/"+id+"/reject", nil, gin.Params{{Key: "id", Value: id}})
	return w.Code, w.Body.String()
}

func want409(t *testing.T, what string, code int, body, errCode string) {
	t.Helper()
	if code != http.StatusConflict || !strings.Contains(body, errCode) {
		t.Fatalf("%s: code %d body %s, want 409 %s", what, code, body, errCode)
	}
}

func assertAppliedOnce(t *testing.T, s *database.PebbleStore, id string, calls int32, action string) {
	t.Helper()
	if calls != 1 {
		t.Fatalf("apply ran %d times, want exactly 1", calls)
	}
	got, _ := s.GetReviewItem(id)
	if got.Status != database.ReviewStatusApplied || got.ChosenAction != action || got.ApplyClaimToken != "" {
		t.Fatalf("final item = status %q chosen %q token %q, want applied/%s with no claim",
			got.Status, got.ChosenAction, got.ApplyClaimToken, action)
	}
}

// openStoreAt opens a disk store at dir WITHOUT registering a close, so a test can
// close it and reopen the directory to play a restart.
func openStoreAt(t *testing.T, dir string) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	s.WaitForWarmup()
	return s
}

// restart closes s and reopens its directory: the reopened handle is a new claim
// instance, exactly as a new process is.
func restart(t *testing.T, s *database.PebbleStore, dir string) *database.PebbleStore {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2 := openStoreAt(t, dir)
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

// 🔴 BLOCKER 1, reject: a reject that started before the claim and lands during the
// apply is refused; it no longer un-claims the item, so a third approve cannot claim
// it again and apply a second time. (Was TestProbe_RejectOverwritesClaim_DoubleApply.)
func TestReject_LandingDuringApply_IsRefused_ApplyRunsOnce(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	ba := newBlockingApply()
	gs := &gateDecide{PebbleStore: s, gate: ba.entered, status: database.ReviewStatusRejected}
	h := reviewhandler.New(gs, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, ba.fn)

	var rejCode int
	var rejBody string
	var rejWG sync.WaitGroup
	rejWG.Go(func() { rejCode, rejBody = rejectReq(t, h, a.ID) })
	var aCode int
	var aWG sync.WaitGroup
	aWG.Go(func() { aCode, _ = approveReq(t, h, a.ID, nil) })

	rejWG.Wait() // the gate opened once the apply was running; the reject has landed
	want409(t, "reject during apply", rejCode, rejBody, "REVIEW_APPLY_IN_PROGRESS")
	if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusApplying {
		t.Fatalf("mid-apply status = %q, want applying (the reject must not replace the claim)", got.Status)
	}
	code, body := approveReq(t, h, a.ID, nil)
	want409(t, "approve during apply", code, body, "REVIEW_APPLY_IN_PROGRESS")

	close(ba.release)
	aWG.Wait()
	if aCode != http.StatusOK {
		t.Fatalf("first approve: code %d", aCode)
	}
	assertAppliedOnce(t, s, a.ID, ba.calls.Load(), itunesservice.ActionCombine)
}

// 🔴 BLOCKER 1, separate: same race with approve {"action":"separate"} as the late
// writer. (Was TestProbe_SeparateOverwritesClaim_DoubleApply.)
func TestSeparate_LandingDuringApply_IsRefused_ApplyRunsOnce(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	ba := newBlockingApply()
	gs := &gateDecide{PebbleStore: s, gate: ba.entered, status: database.ReviewStatusApproved, chosen: itunesservice.ActionSeparate}
	h := reviewhandler.New(gs, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, ba.fn)

	var sepCode int
	var sepBody string
	var sepWG sync.WaitGroup
	sepWG.Go(func() { sepCode, sepBody = approveReq(t, h, a.ID, map[string]any{"action": "separate"}) })
	var aWG sync.WaitGroup
	aWG.Go(func() { approveReq(t, h, a.ID, nil) })

	sepWG.Wait()
	want409(t, "separate during apply", sepCode, sepBody, "REVIEW_APPLY_IN_PROGRESS")
	code, body := approveReq(t, h, a.ID, map[string]any{"action": "combine"})
	want409(t, "approve combine during apply", code, body, "REVIEW_APPLY_IN_PROGRESS")

	close(ba.release)
	aWG.Wait()
	assertAppliedOnce(t, s, a.ID, ba.calls.Load(), itunesservice.ActionCombine)
}

// 🔴 BLOCKER 1, the third writer: an approve that records "approved" without applying
// — apply switched off, or no handler for the action — is refused during an apply
// too, single and bulk.
func TestApproveWithoutApply_LandingDuringApply_IsRefused(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	ba := newBlockingApply()
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, ba.fn)
	hOff := reviewhandler.New(s, func() bool { return false }, nil)
	hOff.RegisterApplyHandler(itunesservice.ActionCombine, ba.fn)
	hNone := reviewhandler.New(s, func() bool { return true }, nil) // no handler registered

	var aWG sync.WaitGroup
	aWG.Go(func() { approveReq(t, h, a.ID, nil) })
	<-ba.entered

	code, body := approveReq(t, hOff, a.ID, nil)
	want409(t, "apply-off approve during apply", code, body, "REVIEW_APPLY_IN_PROGRESS")
	code, body = approveReq(t, hNone, a.ID, map[string]any{"action": "combine"})
	want409(t, "no-handler approve during apply", code, body, "REVIEW_APPLY_IN_PROGRESS")
	w := doReq(t, hOff.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": []string{a.ID}}, nil)
	data := decodeBody(t, w)["data"].(map[string]any)
	skipped, _ := data["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["code"] != "REVIEW_APPLY_IN_PROGRESS" {
		t.Fatalf("bulk approve during apply: %v, want one skip coded REVIEW_APPLY_IN_PROGRESS", data)
	}
	w = doReq(t, hOff.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "reject", "ids": []string{a.ID}}, nil)
	data = decodeBody(t, w)["data"].(map[string]any)
	if skipped, _ := data["skipped"].([]any); len(skipped) != 1 || data["rejected"] != nil {
		t.Fatalf("bulk reject during apply: %v", data)
	}

	close(ba.release)
	aWG.Wait()
	assertAppliedOnce(t, s, a.ID, ba.calls.Load(), itunesservice.ActionCombine)
}

// A claim held by this process is never taken over or decided over, and never runs
// the apply.
func TestLiveClaim_ApproveAndRejectAre409_NoApply(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	if _, err := s.ClaimReviewItemForApply(a.ID, itunesservice.ActionCombine, time.Time{}); err != nil {
		t.Fatal(err)
	}
	h := reviewhandler.New(s, func() bool { return true }, nil)
	var calls atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { calls.Add(1); return nil })

	code, body := approveReq(t, h, a.ID, nil)
	want409(t, "approve of a live claim", code, body, "REVIEW_APPLY_IN_PROGRESS")
	code, body = rejectReq(t, h, a.ID)
	want409(t, "reject of a live claim", code, body, "REVIEW_APPLY_IN_PROGRESS")
	if calls.Load() != 0 {
		t.Fatalf("apply ran %d times over a live claim", calls.Load())
	}
}

// 🔴 BLOCKER 2: a claim left by a crashed process is recoverable at once. Reject and
// a different action are refused (the interrupted apply may have changed the library);
// an approve of the claimed action takes it over and applies exactly once.
// (Was TestProbe_StaleClaimTakeoverReachable.)
func TestDeadClaim_IsTakenOverByApprove_ApplyRunsOnce(t *testing.T) {
	dir := t.TempDir()
	s := openStoreAt(t, dir)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	if _, err := s.ClaimReviewItemForApply(a.ID, itunesservice.ActionCombine, time.Time{}); err != nil { // the approve that "crashed"
		t.Fatal(err)
	}
	s = restart(t, s, dir)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	var calls atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { calls.Add(1); return nil })
	h.RegisterApplyHandler(itunesservice.ActionVersionGroup, func(context.Context, database.ReviewItem) error { calls.Add(100); return nil })

	code, body := rejectReq(t, h, a.ID)
	want409(t, "reject of an interrupted apply", code, body, "REVIEW_APPLY_INTERRUPTED")
	code, body = approveReq(t, h, a.ID, map[string]any{"action": "version-group"})
	want409(t, "different action over an interrupted apply", code, body, "REVIEW_APPLY_INTERRUPTED")
	code, body = approveReq(t, h, a.ID, map[string]any{"action": "separate"})
	want409(t, "separate over an interrupted apply", code, body, "REVIEW_APPLY_INTERRUPTED")

	code, body = approveReq(t, h, a.ID, nil)
	if code != http.StatusOK || !strings.Contains(body, "interrupted") {
		t.Fatalf("takeover approve: code %d body %s, want 200 with a note that it finished an interrupted apply", code, body)
	}
	assertAppliedOnce(t, s, a.ID, calls.Load(), itunesservice.ActionCombine)
	code, body = rejectReq(t, h, a.ID)
	want409(t, "reject after takeover", code, body, "REVIEW_ALREADY_APPLIED")
}

// A crash mid-apply of an explicit OVERRIDE: the takeover re-runs the override, not
// the payload's recommendation, even when re-approved with an empty body.
func TestDeadClaim_TakeoverRunsTheClaimedOverride(t *testing.T) {
	dir := t.TempDir()
	s := openStoreAt(t, dir)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	if _, err := s.ClaimReviewItemForApply(a.ID, itunesservice.ActionVersionGroup, time.Time{}); err != nil {
		t.Fatal(err)
	}
	s = restart(t, s, dir)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	var combined, grouped atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { combined.Add(1); return nil })
	h.RegisterApplyHandler(itunesservice.ActionVersionGroup, func(context.Context, database.ReviewItem) error { grouped.Add(1); return nil })

	reApprove(t, h, a.ID)
	if combined.Load() != 0 {
		t.Fatalf("combine ran %d times; the interrupted apply was version-group", combined.Load())
	}
	assertAppliedOnce(t, s, a.ID, grouped.Load(), itunesservice.ActionVersionGroup)
}

// applied_unrecorded across a restart: the token is gone, so the approve takes the
// dead claim over and re-runs the apply (the documented, handler-tolerated re-run).
func TestUnrecordedApply_AfterRestart_IsReRunAndRecorded(t *testing.T) {
	dir := t.TempDir()
	s := openStoreAt(t, dir)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	fs := &flakyStore{PebbleStore: s}
	fs.failFinish.Store(true)
	h := reviewhandler.New(fs, func() bool { return true }, nil)
	var calls atomic.Int32
	count := func(context.Context, database.ReviewItem) error { calls.Add(1); return nil }
	h.RegisterApplyHandler(itunesservice.ActionCombine, count)
	w := doReq(t, h.ApproveReviewItem, http.MethodPost, "/a", nil, gin.Params{{Key: "id", Value: a.ID}})
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "REVIEW_APPLIED_UNRECORDED") {
		t.Fatalf("code %d body %s", w.Code, w.Body.String())
	}

	s = restart(t, s, dir)
	h2 := reviewhandler.New(s, func() bool { return true }, nil)
	h2.RegisterApplyHandler(itunesservice.ActionCombine, count)
	reApprove(t, h2, a.ID)
	if calls.Load() != 2 {
		t.Fatalf("apply ran %d times; after a restart the unrecorded apply re-runs once more", calls.Load())
	}
	if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusApplied {
		t.Fatalf("status = %q, want applied", got.Status)
	}
}

// A panicking apply cannot say whether it wrote anything, so its claim is HELD, not
// handed back: the item stays applying with the claim blanked (a claim from this
// process is never taken over, so a leaked live one would hold the item until a
// restart). An approve of the same action then takes it over and re-runs the apply.
func TestApplyPanic_HoldsTheClaim(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { panic("synthetic") })

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("the panic was swallowed; gin's recovery must still see it")
			}
		}()
		approveReq(t, h, a.ID, nil)
	}()
	got, _ := s.GetReviewItem(a.ID)
	if got.Status != database.ReviewStatusApplying || got.ApplyClaimToken != "" || got.ApplyClaimInstance != "" ||
		got.ApplyClaimAction != itunesservice.ActionCombine {
		t.Fatalf("after panic = status %q token %q instance %q action %q, want applying, claim blanked, action kept",
			got.Status, got.ApplyClaimToken, got.ApplyClaimInstance, got.ApplyClaimAction)
	}

	// Retryable: the next approve takes the held claim over and finishes the apply.
	var calls atomic.Int32
	h2 := reviewhandler.New(s, func() bool { return true }, nil)
	h2.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { calls.Add(1); return nil })
	if code, body := approveReq(t, h2, a.ID, nil); code != http.StatusOK {
		t.Fatalf("retry after panic: code %d body %s", code, body)
	}
	if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusApplied || calls.Load() != 1 {
		t.Fatalf("after retry = %q with %d apply calls, want applied after 1", got.Status, calls.Load())
	}
}

// changingStore refreshes the hold's payload (a producer re-scan) right after the
// handler reads it, before the claim.
type changingStore struct {
	*database.PebbleStore
	once sync.Once
}

func (c *changingStore) GetReviewItem(id string) (*database.ReviewItem, error) {
	it, err := c.PebbleStore.GetReviewItem(id)
	if err == nil && it != nil {
		c.once.Do(func() {
			time.Sleep(2 * time.Millisecond)
			_, _ = c.UpsertReviewItem(database.ReviewItem{Kind: it.Kind, DedupKey: it.DedupKey, FolderRef: it.FolderRef,
				Summary: "rescanned", Payload: `{"recommendedAction":"separate"}`})
		})
	}
	return it, err
}

// The claim is a compare-and-set on what approve read: a hold that changed in between
// is refused instead of applied from a stale reading.
func TestApprove_ItemChangedBeforeClaim_Is409NoApply(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(&changingStore{PebbleStore: s}, func() bool { return true }, nil)
	var calls atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { calls.Add(1); return nil })
	code, body := approveReq(t, h, a.ID, nil)
	want409(t, "approve of a changed hold", code, body, "REVIEW_ITEM_CHANGED")
	if calls.Load() != 0 {
		t.Fatalf("apply ran %d times on a stale read", calls.Load())
	}
	if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
}

// Items held by a claim are visible: counted separately, and listable.
func TestApplyingItems_AreCountedAndListable(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	seedAction(t, s, "regroup.multidisc", "m2", itunesservice.ActionCombine)
	if _, err := s.ClaimReviewItemForApply(a.ID, itunesservice.ActionCombine, time.Time{}); err != nil {
		t.Fatal(err)
	}
	h := reviewhandler.New(s, func() bool { return true }, nil)
	w := doReq(t, h.GetReviewCount, http.MethodGet, "/review/count", nil, nil)
	data := decodeBody(t, w)["data"].(map[string]any)
	if data["count"].(float64) != 1 || data["applying"].(float64) != 1 {
		t.Fatalf("count response = %v, want count 1 (pending) and applying 1", data)
	}
	w = doReq(t, h.ListReviewItems, http.MethodGet, "/review/items?status=applying", nil, nil)
	body := decodeBody(t, w)
	items, _ := body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != a.ID {
		t.Fatalf("?status=applying = %v, want the claimed item", body)
	}
}

// reviewOnlyStore hides every capability: only ReviewStore's methods are promoted.
type reviewOnlyStore struct{ database.ReviewStore }

// Without the claim capability no review write is made: an unguarded decision could
// land over a running apply.
func TestNoClaimer_WritesRefusedWith500(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(reviewOnlyStore{s}, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error {
		t.Error("apply ran without a claim")
		return nil
	})
	for name, w := range map[string]int{
		"approve": func() int { c, _ := approveReq(t, h, a.ID, nil); return c }(),
		"reject":  func() int { c, _ := rejectReq(t, h, a.ID); return c }(),
		"bulk": doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
			map[string]any{"action": "approve", "ids": []string{a.ID}}, nil).Code,
	} {
		if w != http.StatusInternalServerError {
			t.Errorf("%s without a claimer: code %d, want 500", name, w)
		}
	}
	code, body := approveReq(t, h, a.ID, nil)
	if !strings.Contains(body, "REVIEW_CLAIM_UNAVAILABLE") {
		t.Fatalf("approve: code %d body %s, want REVIEW_CLAIM_UNAVAILABLE", code, body)
	}
	if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
}

// A storm of approves and rejects on one item: the apply runs at most once, and the
// item is applied exactly when it ran (run with -race).
func TestApproveRejectStorm_AppliesAtMostOnce(t *testing.T) {
	for round := range 10 {
		s := newTestStore(t)
		a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
		h := reviewhandler.New(s, func() bool { return true }, nil)
		var calls atomic.Int32
		h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error {
			calls.Add(1)
			time.Sleep(5 * time.Millisecond)
			return nil
		})
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				var code int
				var body string
				if i%2 == 0 {
					code, body = approveReq(t, h, a.ID, nil)
				} else {
					code, body = rejectReq(t, h, a.ID)
				}
				if code != http.StatusOK && code != http.StatusConflict {
					t.Errorf("round %d: code %d body %s", round, code, body)
				}
			})
		}
		wg.Wait()
		got, _ := s.GetReviewItem(a.ID)
		if calls.Load() > 1 || (calls.Load() == 1) != (got.Status == database.ReviewStatusApplied) {
			t.Fatalf("round %d: apply ran %d times, final status %q", round, calls.Load(), got.Status)
		}
		if got.Status == database.ReviewStatusApplying {
			t.Fatalf("round %d: claim leaked", round)
		}
	}
}

// An applied hold whose action resolves to insufficient-evidence (no recorded choice,
// old payload) answers "already applied", not "choose an action": bulk must not list
// it as still pending.
//
// The store can no longer write an applied item without a recorded action (the finish
// records the claim's), but production holds applied rows from before it did, so the
// handler's read is made to return one: the hold is applied through the claim, and
// oldShapeRead blanks the action the finish recorded.
func TestApprove_AppliedOldShapeHold_Is409AlreadyApplied(t *testing.T) {
	s := newTestStore(t)
	a := seed(t, s, "regroup.multidisc", "m1") // payload "{}": insufficient-evidence
	c, err := s.ClaimReviewItemForApply(a.ID, itunesservice.ActionCombine, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishReviewItemApply(a.ID, c.Token); err != nil {
		t.Fatal(err)
	}
	h := reviewhandler.New(&oldShapeRead{PebbleStore: s}, func() bool { return true }, nil)
	code, body := approveReq(t, h, a.ID, nil)
	want409(t, "approve of an applied old-shape hold", code, body, "REVIEW_ALREADY_APPLIED")
	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": []string{a.ID}}, nil)
	data := decodeBody(t, w)["data"].(map[string]any)
	skipped, _ := data["skipped"].([]any)
	if len(skipped) != 1 || skipped[0].(map[string]any)["code"] != "REVIEW_ALREADY_APPLIED" {
		t.Fatalf("bulk = %v, want one skip coded REVIEW_ALREADY_APPLIED", data)
	}
}

// oldShapeRead returns every review item without its recorded action, as an item
// decided before ChosenAction existed reads.
type oldShapeRead struct{ *database.PebbleStore }

func (o *oldShapeRead) GetReviewItem(id string) (*database.ReviewItem, error) {
	it, err := o.PebbleStore.GetReviewItem(id)
	if it != nil {
		it.ChosenAction = ""
	}
	return it, err
}
