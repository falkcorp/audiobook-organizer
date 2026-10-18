// file: internal/server/handlers/review/partial_apply_test.go
// version: 1.1.0
// guid: 8d4a1f60-2c97-4e35-b0a8-5f1e93c7d268
// last-edited: 2026-10-18

package reviewhandler_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	reviewhandler "github.com/falkcorp/audiobook-organizer/internal/server/handlers/review"
)

// 🔴 A FIRST-RUN APPLY THAT FAILED IS HELD UNLESS IT SAYS IT WROTE NOTHING (owner
// decision 2026-10-18). These pin the handler's side: an unmarked error (it may have
// written part of what it set out to) leaves the item applying with its claim blanked,
// exactly like a failed re-run, and a database.NothingWritten error is handed back as
// before. The real apply functions' classification is pinned in the maintenance
// package (regroup_apply_nothing_written_test.go).

// A first run that fails with an unmarked error is held: 409 REVIEW_APPLY_PARTIAL, the
// item stays applying with the claim blanked and its action kept, nothing else can be
// decided over it, and approving again retries and finishes it.
func TestApprove_FailedFirstRun_ThatMayHaveWritten_IsHeldAndRetryable(t *testing.T) {
	for _, from := range []string{database.ReviewStatusPending, database.ReviewStatusApproved} {
		t.Run(from, func(t *testing.T) {
			s := newTestStore(t)
			a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
			applyOn := from == database.ReviewStatusPending
			h := reviewhandler.New(s, func() bool { return applyOn }, nil)
			var calls atomic.Int32
			failFirst := true
			h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error {
				calls.Add(1)
				if failFirst {
					failFirst = false
					return errors.New("merge stopped after moving two of five files")
				}
				return nil
			})
			if from == database.ReviewStatusApproved {
				reApprove(t, h, a.ID) // apply off: records approved
				applyOn = true
			}

			code, body := approveReq(t, h, a.ID, nil)
			want409(t, "failed first run that may have written", code, body, "REVIEW_APPLY_PARTIAL")
			if !strings.Contains(body, "merge stopped after moving two of five files") ||
				!strings.Contains(body, "abandon-apply") || !strings.Contains(body, "Approve it again to retry") {
				t.Fatalf("body %s, want the cause, retry and abandon-apply", body)
			}
			got, _ := s.GetReviewItem(a.ID)
			if got.Status != database.ReviewStatusApplying || got.ApplyClaimToken != "" || got.ApplyClaimInstance != "" ||
				got.ApplyClaimAction != itunesservice.ActionCombine || got.ApplyClaimedFrom != from {
				t.Fatalf("after a held first run = %+v, want applying, claim blanked, action and claimed-from kept", got)
			}
			if n, _ := s.CountReviewItems(database.ReviewStatusApplying); n != 1 {
				t.Fatalf("%d items applying, want the held one", n)
			}

			// Not decidable over a half-changed library.
			code, body = rejectReq(t, h, a.ID)
			want409(t, "reject over a held first run", code, body, "REVIEW_APPLY_INTERRUPTED")
			code, body = approveReq(t, h, a.ID, map[string]any{"action": "separate"})
			want409(t, "separate over a held first run", code, body, "REVIEW_APPLY_INTERRUPTED")

			// Re-approve retries (the same action) and finishes it.
			code, body = approveReq(t, h, a.ID, nil)
			if code != http.StatusOK || !strings.Contains(body, "interrupted") {
				t.Fatalf("retry: code %d body %s, want 200 noting the interrupted apply was finished", code, body)
			}
			if calls.Load() != 2 {
				t.Fatalf("apply ran %d times, want the failed run and the retry", calls.Load())
			}
			if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusApplied || got.ChosenAction != itunesservice.ActionCombine {
				t.Fatalf("after the retry = %+v, want applied as combine", got)
			}
		})
	}
}

// A held first run whose retry fails too stays held (REVIEW_RERUN_FAILED), whatever the
// retry says about what it wrote, and abandon-apply is the way out.
func TestApprove_HeldFirstRun_FailedRetryStaysHeld_ThenAbandon(t *testing.T) {
	s := newTestStore(t)
	a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	var calls atomic.Int32
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error {
		if calls.Add(1) == 1 {
			return errors.New("first run: partial merge")
		}
		return database.NothingWritten(errors.New("retry: refused again"))
	})
	code, body := approveReq(t, h, a.ID, nil)
	want409(t, "first run", code, body, "REVIEW_APPLY_PARTIAL")
	code, body = approveReq(t, h, a.ID, nil)
	want409(t, "retry", code, body, "REVIEW_RERUN_FAILED")
	if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusApplying || got.ApplyClaimToken != "" {
		t.Fatalf("after a failed retry = %+v, want still held", got)
	}

	code, body = abandonReq(t, h, a.ID)
	if code != http.StatusOK {
		t.Fatalf("abandon: code %d body %s", code, body)
	}
	got, _ := s.GetReviewItem(a.ID)
	if got.Status != database.ReviewStatusNeedsAttention || got.ApplyAbandonedAction != itunesservice.ActionCombine {
		t.Fatalf("after abandon = %+v, want needs_attention noting the abandoned combine", got)
	}
}

// A first run that fails having written nothing is handed back to the status it was
// claimed from, as before: decidable again, no claim left, 500 with the cause.
func TestApprove_FailedFirstRun_NothingWritten_IsReleasedToItsPriorStatus(t *testing.T) {
	for _, from := range []string{database.ReviewStatusPending, database.ReviewStatusApproved} {
		t.Run(from, func(t *testing.T) {
			s := newTestStore(t)
			a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
			applyOn := from == database.ReviewStatusPending
			h := reviewhandler.New(s, func() bool { return applyOn }, nil)
			h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error {
				return database.NothingWritten(errors.New("dedup track names no survivor"))
			})
			if from == database.ReviewStatusApproved {
				reApprove(t, h, a.ID)
				applyOn = true
			}
			code, body := approveReq(t, h, a.ID, nil)
			if code != http.StatusInternalServerError || strings.Contains(body, "REVIEW_APPLY_PARTIAL") {
				t.Fatalf("clean failure: code %d body %s, want a plain 500", code, body)
			}
			got, _ := s.GetReviewItem(a.ID)
			if got.Status != from || got.ApplyClaimAction != "" || got.ApplyClaimedFrom != "" || got.ApplyClaimToken != "" {
				t.Fatalf("after a clean failure = %+v, want %s with no claim", got, from)
			}
			if code, body := rejectReq(t, h, a.ID); code != http.StatusOK {
				t.Fatalf("a released item must be decidable: code %d body %s", code, body)
			}
		})
	}
}

// The iTunes refusal is a first run that wrote nothing: released and answered
// REVIEW_ITUNES_PROTECTED, as ever. The same error reaching the handler WITHOUT the
// marker (an apply function that cannot say) is held and answered REVIEW_APPLY_PARTIAL:
// the code is what tells a client whether the item is still in a pending list.
func TestApprove_ITunesRefusal_ReleasedWhenMarkedHeldWhenNot(t *testing.T) {
	refusal := fmt.Errorf("combine: %w", &merge.ITunesProtectedError{BookID: "B1", Path: "/srv/media/Audiobooks/x.m4b"})
	t.Run("marked", func(t *testing.T) {
		s := newTestStore(t)
		a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
		h := reviewhandler.New(s, func() bool { return true }, nil)
		h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error {
			return database.NothingWritten(refusal)
		})
		code, body := approveReq(t, h, a.ID, nil)
		want409(t, "marked iTunes refusal", code, body, "REVIEW_ITUNES_PROTECTED")
		if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusPending {
			t.Fatalf("status %q, want pending", got.Status)
		}
	})
	t.Run("unmarked", func(t *testing.T) {
		s := newTestStore(t)
		a := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
		h := reviewhandler.New(s, func() bool { return true }, nil)
		h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { return refusal })
		code, body := approveReq(t, h, a.ID, nil)
		want409(t, "unmarked iTunes refusal", code, body, "REVIEW_APPLY_PARTIAL")
		if got, _ := s.GetReviewItem(a.ID); got.Status != database.ReviewStatusApplying || got.ApplyClaimToken != "" {
			t.Fatalf("after an unmarked refusal = %+v, want held", got)
		}
	})
}

// Bulk approve by ids reports a held first run as SKIPPED with REVIEW_APPLY_PARTIAL (a
// coded per-item refusal, not a failure that aborts the batch), carries on with the
// rest, and reports a clean failure as failed, retryable, as before.
func TestBulkApprove_HeldFirstRun_IsSkippedWithItsCode(t *testing.T) {
	s := newTestStore(t)
	ok := seedAction(t, s, "regroup.multidisc", "m1", itunesservice.ActionCombine)
	partial := seedAction(t, s, "regroup.multidisc", "m2", itunesservice.ActionCombine)
	clean := seedAction(t, s, "regroup.multidisc", "m3", itunesservice.ActionCombine)
	last := seedAction(t, s, "regroup.multidisc", "m4", itunesservice.ActionCombine)
	h := reviewhandler.New(s, func() bool { return true }, nil)
	h.RegisterApplyHandler(itunesservice.ActionCombine, func(_ context.Context, it database.ReviewItem) error {
		switch it.ID {
		case partial.ID:
			return errors.New("partial merge")
		case clean.ID:
			return database.NothingWritten(errors.New("refused up front"))
		}
		return nil
	})

	w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
		map[string]any{"action": "approve", "ids": []string{ok.ID, partial.ID, clean.ID, last.ID}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("bulk: code %d body %s", w.Code, w.Body.String())
	}
	data := decodeBody(t, w)["data"].(map[string]any)
	skipped, _ := data["skipped"].([]any)
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want only the held item", data["skipped"])
	}
	sk := skipped[0].(map[string]any)
	if sk["id"] != partial.ID || sk["code"] != "REVIEW_APPLY_PARTIAL" || sk["action"] != itunesservice.ActionCombine ||
		!strings.Contains(fmt.Sprint(sk["reason"]), "partial merge") {
		t.Fatalf("skipped[0] = %v, want %s coded REVIEW_APPLY_PARTIAL with the cause", sk, partial.ID)
	}
	failed, _ := data["failed"].([]any)
	if len(failed) != 1 || failed[0].(map[string]any)["id"] != clean.ID {
		t.Fatalf("failed = %v, want only the clean failure", data["failed"])
	}
	if applied, _ := data["applied"].([]any); len(applied) != 2 {
		t.Fatalf("applied = %v, want the first and last items", data["applied"])
	}
	for id, want := range map[string]string{
		partial.ID: database.ReviewStatusApplying,
		clean.ID:   database.ReviewStatusPending,
		ok.ID:      database.ReviewStatusApplied,
		last.ID:    database.ReviewStatusApplied,
	} {
		if got, _ := s.GetReviewItem(id); got.Status != want {
			t.Fatalf("item %s status = %q, want %q", id, got.Status, want)
		}
	}
}

// claimEndFailStore fails the claim-ending call the handler makes after a failed apply,
// so the reason the handler builds for that case can be read.
type claimEndFailStore struct {
	*database.PebbleStore
}

func (claimEndFailStore) HoldReviewItemClaim(string, string) (*database.ReviewItem, error) {
	return nil, errors.New("pebble write stalled")
}

func (claimEndFailStore) ReleaseReviewItemClaim(string, string) (*database.ReviewItem, error) {
	return nil, errors.New("pebble write stalled")
}

// The reason for a claim that could not be ended names the call that failed: a failure
// that may have written is HELD, one that wrote nothing is RELEASED, and the text must
// not say "released" for the first.
func TestApprove_ClaimEndFailure_NamesTheFailedStep(t *testing.T) {
	for _, tc := range []struct {
		name, want, notWant string
		applyErr            error
	}{
		{"hold", "could not be held", "could not be released", errors.New("merge stopped after moving two of five files")},
		{"release", "could not be released", "could not be held", database.NothingWritten(errors.New("refused before any write"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			a := seedAction(t, s, "regroup.multidisc", "m-"+tc.name, itunesservice.ActionCombine)
			h := reviewhandler.New(claimEndFailStore{s}, func() bool { return true }, nil)
			h.RegisterApplyHandler(itunesservice.ActionCombine, func(context.Context, database.ReviewItem) error { return tc.applyErr })

			// The single-item route hides the reason behind a generic 500; bulk
			// reports it per item.
			w := doReq(t, h.BulkReviewAction, http.MethodPost, "/review/bulk",
				map[string]any{"action": "approve", "ids": []string{a.ID}}, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("bulk: code %d body %s", w.Code, w.Body.String())
			}
			failed, _ := decodeBody(t, w)["data"].(map[string]any)["failed"].([]any)
			if len(failed) != 1 {
				t.Fatalf("failed = %v, want the one item", failed)
			}
			reason, _ := failed[0].(map[string]any)["reason"].(string)
			if !strings.Contains(reason, tc.want) || strings.Contains(reason, tc.notWant) ||
				!strings.Contains(reason, "pebble write stalled") {
				t.Fatalf("reason %q, want the claim %q with the store's cause, never %q", reason, tc.want, tc.notWant)
			}
		})
	}
}
