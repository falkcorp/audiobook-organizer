// file: internal/database/review_claim_test.go
// version: 2.0.0
// guid: 74ff5b50-d848-43ed-91e5-889e92c828b1
// last-edited: 2026-10-18

package database

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func seedClaimItem(t *testing.T, s *PebbleStore, key, status string) ReviewItem {
	t.Helper()
	it, err := s.UpsertReviewItem(ReviewItem{Kind: "regroup.multidisc", DedupKey: key, FolderRef: "/f/" + key, Summary: "s", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if status != ReviewStatusPending {
		if _, err := s.SetReviewItemDecision(it.ID, status, "combine"); err != nil {
			t.Fatal(err)
		}
	}
	return it
}

func countStatus(t *testing.T, s *PebbleStore, status string) int {
	t.Helper()
	n, err := s.CountReviewItems(status)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func mustClaim(t *testing.T, s *PebbleStore, id, action string) *ReviewApplyClaim {
	t.Helper()
	c, err := s.ClaimReviewItemForApply(id, action, time.Time{})
	if err != nil || c == nil {
		t.Fatalf("claim %s: %+v, %v", id, c, err)
	}
	return c
}

func wantConflict(t *testing.T, err error, reason ReviewClaimConflictReason) {
	t.Helper()
	var conflict *ReviewClaimConflictError
	if !errors.As(err, &conflict) || conflict.Reason != reason {
		t.Fatalf("err = %v, want a %q conflict", err, reason)
	}
}

// simulateRestart gives s a new claim instance id, which is what a new process
// opening the store has: every claim taken before this call becomes a claim left by
// an exited process. TestClaim_ReopenedStoreSeesOldClaimsAsDead proves a real reopen
// behaves the same way.
func simulateRestart(t *testing.T, s *PebbleStore) {
	t.Helper()
	if _, err := s.claimInstanceID(); err != nil {
		t.Fatal(err)
	}
	id, err := newUnlinkedULID()
	if err != nil {
		t.Fatal(err)
	}
	s.reviewClaim.id = id
}

func TestClaim_ClaimFinishReleaseKeepIndexConsistent(t *testing.T) {
	s := newReviewTestStore(t)
	it := seedClaimItem(t, s, "k1", ReviewStatusApproved)

	c := mustClaim(t, s, it.ID, "version-group")
	got := c.Item
	if got.Status != ReviewStatusApplying || got.ApplyClaimedFrom != ReviewStatusApproved ||
		got.ApplyClaimAction != "version-group" || got.ApplyClaimToken != c.Token || got.ApplyClaimInstance == "" || c.TookOver {
		t.Fatalf("claim = %+v (took over %v)", got, c.TookOver)
	}
	if countStatus(t, s, ReviewStatusApplying) != 1 || countStatus(t, s, ReviewStatusApproved) != 0 {
		t.Fatal("status index not moved by the claim")
	}
	_, err := s.ClaimReviewItemForApply(it.ID, "version-group", time.Time{})
	wantConflict(t, err, ReviewClaimInProgress)

	// A token that does not hold the claim changes nothing.
	if ok, err := s.ReleaseReviewItemClaim(it.ID, "not-the-token"); ok || err != nil {
		t.Fatalf("release with a wrong token = %v, %v", ok, err)
	}
	_, err = s.FinishReviewItemApply(it.ID, "not-the-token")
	wantConflict(t, err, ReviewClaimNotHeld)

	if ok, err := s.ReleaseReviewItemClaim(it.ID, c.Token); !ok || err != nil {
		t.Fatalf("release = %v, %v", ok, err)
	}
	back, _ := s.GetReviewItem(it.ID)
	if back.Status != ReviewStatusApproved || back.ApplyClaimedFrom != "" || back.ApplyClaimToken != "" ||
		back.ApplyClaimAction != "" || back.ApplyClaimInstance != "" || back.ChosenAction != "combine" {
		t.Fatalf("after release = %+v (a release records no action)", back)
	}
	if countStatus(t, s, ReviewStatusApplying) != 0 || countStatus(t, s, ReviewStatusApproved) != 1 {
		t.Fatal("status index not restored by the release")
	}
	// The released token is spent.
	if ok, _ := s.ReleaseReviewItemClaim(it.ID, c.Token); ok {
		t.Fatal("a spent token released again")
	}

	c2 := mustClaim(t, s, it.ID, "version-group")
	done, err := s.FinishReviewItemApply(it.ID, c2.Token)
	if err != nil || done.Status != ReviewStatusApplied || done.ChosenAction != "version-group" ||
		done.ApplyClaimToken != "" || done.ApplyClaimedFrom != "" {
		t.Fatalf("after finish = %+v, %v; want applied recording the claimed action", done, err)
	}
	if countStatus(t, s, ReviewStatusApplying) != 0 || countStatus(t, s, ReviewStatusApplied) != 1 {
		t.Fatal("status index not moved by the finish")
	}
	_, err = s.ClaimReviewItemForApply(it.ID, "version-group", time.Time{})
	wantConflict(t, err, ReviewClaimApplied)
	_, err = s.FinishReviewItemApply(it.ID, c2.Token)
	wantConflict(t, err, ReviewClaimNotHeld)
}

func TestClaim_RejectedIsClaimableMissingIsNil(t *testing.T) {
	s := newReviewTestStore(t)
	rej := seedClaimItem(t, s, "k1", ReviewStatusRejected)
	c := mustClaim(t, s, rej.ID, "combine")
	if c.Item.ApplyClaimedFrom != ReviewStatusRejected {
		t.Fatalf("claimed from %q, want rejected (re-deciding is allowed)", c.Item.ApplyClaimedFrom)
	}
	if ok, err := s.ReleaseReviewItemClaim(rej.ID, c.Token); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if back, _ := s.GetReviewItem(rej.ID); back.Status != ReviewStatusRejected {
		t.Fatalf("release returned it to %q, want rejected", back.Status)
	}
	if got, err := s.ClaimReviewItemForApply("nope", "combine", time.Time{}); got != nil || err != nil {
		t.Fatalf("claim of missing = %v, %v", got, err)
	}
	if _, err := s.ClaimReviewItemForApply(rej.ID, "", time.Time{}); err == nil {
		t.Fatal("a claim with no action was accepted")
	}
}

// 🔴 A LIVE CLAIM IS NEVER TAKEN OVER, HOWEVER OLD. There is no clock in the decision:
// an apply in this process may run for a long time and must keep its claim.
func TestClaim_LiveClaimIsNeverTakenOver(t *testing.T) {
	s := newReviewTestStore(t)
	it := seedClaimItem(t, s, "k1", ReviewStatusPending)
	mustClaim(t, s, it.ID, "combine")
	time.Sleep(5 * time.Millisecond)
	_, err := s.ClaimReviewItemForApply(it.ID, "combine", time.Time{})
	wantConflict(t, err, ReviewClaimInProgress)
}

// A claim left by an exited process is taken over at once by an approve of the same
// action, keeping the ORIGINAL prior status; the dead claim's token is spent.
func TestClaim_DeadClaimIsTakenOverForTheSameActionOnly(t *testing.T) {
	s := newReviewTestStore(t)
	it := seedClaimItem(t, s, "k1", ReviewStatusPending)
	dead := mustClaim(t, s, it.ID, "combine")
	simulateRestart(t, s)

	_, err := s.ClaimReviewItemForApply(it.ID, "version-group", time.Time{})
	wantConflict(t, err, ReviewClaimInterrupted)

	c := mustClaim(t, s, it.ID, "combine")
	if !c.TookOver || c.Item.ApplyClaimedFrom != ReviewStatusPending || c.Token == dead.Token {
		t.Fatalf("takeover = %+v took over %v; want a new token claimed from pending", c.Item, c.TookOver)
	}
	if countStatus(t, s, ReviewStatusApplying) != 1 || countStatus(t, s, ReviewStatusPending) != 0 {
		t.Fatal("index inconsistent after takeover")
	}
	_, err = s.FinishReviewItemApply(it.ID, dead.Token)
	wantConflict(t, err, ReviewClaimNotHeld)
	// The new claim is this process's own and is now as untouchable as any live one.
	_, err = s.ClaimReviewItemForApply(it.ID, "combine", time.Time{})
	wantConflict(t, err, ReviewClaimInProgress)
}

// The real lifecycle behind simulateRestart: a reopened store is a new instance.
func TestClaim_ReopenedStoreSeesOldClaimsAsDead(t *testing.T) {
	dir := t.TempDir()
	s, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.WaitForWarmup()
	it := seedClaimItem(t, s, "k1", ReviewStatusApproved)
	mustClaim(t, s, it.ID, "combine")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	s2.WaitForWarmup()
	_, err = s2.DecideReviewItem(it.ID, ReviewStatusRejected, "")
	wantConflict(t, err, ReviewClaimInterrupted)
	c := mustClaim(t, s2, it.ID, "combine")
	if !c.TookOver || c.Item.ApplyClaimedFrom != ReviewStatusApproved {
		t.Fatalf("after reopen: %+v took over %v", c.Item, c.TookOver)
	}
}

// The claim is a compare-and-set on what the caller read: a record that changed in
// between (a re-scan refreshing the payload, a decision) is refused.
func TestClaim_RefusesARecordThatChangedSinceItWasRead(t *testing.T) {
	s := newReviewTestStore(t)
	it := seedClaimItem(t, s, "k1", ReviewStatusPending)
	read, _ := s.GetReviewItem(it.ID)
	time.Sleep(2 * time.Millisecond)
	if _, err := s.DecideReviewItem(it.ID, ReviewStatusApproved, "separate"); err != nil {
		t.Fatal(err)
	}
	_, err := s.ClaimReviewItemForApply(it.ID, "combine", read.UpdatedAt)
	wantConflict(t, err, ReviewClaimChanged)
	fresh, _ := s.GetReviewItem(it.ID)
	if _, err := s.ClaimReviewItemForApply(it.ID, "separate", fresh.UpdatedAt); err != nil {
		t.Fatalf("claim against the fresh read: %v", err)
	}
}

// 🔴 NO DECISION LANDS ON A CLAIMED OR APPLIED ITEM — through the decision method or
// the generic Store writer.
func TestDecide_RefusesAppliedAndApplyingItems(t *testing.T) {
	s := newReviewTestStore(t)
	live := seedClaimItem(t, s, "live", ReviewStatusPending)
	dead := seedClaimItem(t, s, "dead", ReviewStatusPending)
	applied := seedClaimItem(t, s, "applied", ReviewStatusPending)
	open := seedClaimItem(t, s, "open", ReviewStatusPending)

	mustClaim(t, s, dead.ID, "combine")
	ac := mustClaim(t, s, applied.ID, "combine")
	if _, err := s.FinishReviewItemApply(applied.ID, ac.Token); err != nil {
		t.Fatal(err)
	}
	simulateRestart(t, s)
	lc := mustClaim(t, s, live.ID, "combine")

	for _, status := range []string{ReviewStatusRejected, ReviewStatusApproved, ReviewStatusPending} {
		_, err := s.DecideReviewItem(live.ID, status, "separate")
		wantConflict(t, err, ReviewClaimInProgress)
		_, err = s.DecideReviewItem(dead.ID, status, "separate")
		wantConflict(t, err, ReviewClaimInterrupted)
		_, err = s.DecideReviewItem(applied.ID, status, "separate")
		wantConflict(t, err, ReviewClaimApplied)
		_, err = s.SetReviewItemDecision(live.ID, status, "separate")
		wantConflict(t, err, ReviewClaimInProgress)
		_, err = s.SetReviewItemDecision(dead.ID, status, "")
		wantConflict(t, err, ReviewClaimInterrupted)
	}
	if got, _ := s.GetReviewItem(live.ID); got.Status != ReviewStatusApplying || got.ApplyClaimToken != lc.Token {
		t.Fatalf("live claim disturbed: %+v", got)
	}
	for _, status := range []string{ReviewStatusApplying, ReviewStatusApplied, ""} {
		if _, err := s.DecideReviewItem(open.ID, status, ""); err == nil {
			t.Fatalf("DecideReviewItem accepted status %q", status)
		}
	}
	if _, err := s.SetReviewItemDecision(open.ID, ReviewStatusApplying, ""); err == nil {
		t.Fatal("SetReviewItemDecision moved an item into applying")
	}
	got, err := s.DecideReviewItem(open.ID, ReviewStatusApproved, "separate")
	if err != nil || got.Status != ReviewStatusApproved || got.ChosenAction != "separate" {
		t.Fatalf("decide on an open item = %+v, %v", got, err)
	}
	if got, err := s.DecideReviewItem("nope", ReviewStatusRejected, ""); got != nil || err != nil {
		t.Fatalf("decide on missing = %v, %v", got, err)
	}
	assertStatusIndexConsistent(t, s)
}

func TestClaim_ConcurrentClaimsHaveOneWinner(t *testing.T) {
	s := newReviewTestStore(t)
	it := seedClaimItem(t, s, "k1", ReviewStatusApproved)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if got, err := s.ClaimReviewItemForApply(it.ID, "combine", time.Time{}); err == nil && got != nil {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims won, want exactly 1", wins.Load())
	}
}

// Decisions racing a claim: whichever lands first, a successful claim is never
// overwritten afterwards, and the index stays exact (run with -race).
func TestClaim_ConcurrentDecisionsNeverReplaceAClaim(t *testing.T) {
	s := newReviewTestStore(t)
	for round := range 20 {
		it := seedClaimItem(t, s, "k"+string(rune('a'+round)), ReviewStatusPending)
		var claim atomic.Pointer[ReviewApplyClaim]
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				if i == 0 {
					if c, err := s.ClaimReviewItemForApply(it.ID, "combine", time.Time{}); err == nil {
						claim.Store(c)
					}
					return
				}
				_, _ = s.DecideReviewItem(it.ID, ReviewStatusRejected, "")
				_, _ = s.SetReviewItemDecision(it.ID, ReviewStatusApproved, "separate")
			})
		}
		wg.Wait()
		c := claim.Load()
		if c == nil {
			t.Fatalf("round %d: the claim lost to a decision, which must not happen (claims take any open status)", round)
		}
		got, _ := s.GetReviewItem(it.ID)
		if got.Status != ReviewStatusApplying || got.ApplyClaimToken != c.Token {
			t.Fatalf("round %d: claim replaced: %+v", round, got)
		}
	}
	assertStatusIndexConsistent(t, s)
}

func TestDeleteReviewItemIfPending_DeletesOnlyPending(t *testing.T) {
	s := newReviewTestStore(t)
	pending := seedClaimItem(t, s, "p", ReviewStatusPending)
	approved := seedClaimItem(t, s, "a", ReviewStatusApproved)
	claimed := seedClaimItem(t, s, "c", ReviewStatusPending)
	mustClaim(t, s, claimed.ID, "combine")

	if ok, err := s.DeleteReviewItemIfPending(pending.ID); !ok || err != nil {
		t.Fatalf("pending delete = %v, %v", ok, err)
	}
	for _, id := range []string{approved.ID, claimed.ID, "nope"} {
		if ok, err := s.DeleteReviewItemIfPending(id); ok || err != nil {
			t.Fatalf("delete %s = %v, %v; want left alone", id, ok, err)
		}
	}
	if got, _ := s.GetReviewItem(pending.ID); got != nil {
		t.Fatal("pending item survived")
	}
	// The dedup index went with it: a re-scan creates a fresh hold.
	again, err := s.UpsertReviewItem(ReviewItem{Kind: "regroup.multidisc", DedupKey: "p", FolderRef: "/f/p", Summary: "s", Payload: "{}"})
	if err != nil || again.ID == pending.ID {
		t.Fatalf("re-scan after delete = %+v, %v; want a new row", again, err)
	}
	if got, _ := s.GetReviewItem(approved.ID); got == nil || got.Status != ReviewStatusApproved {
		t.Fatalf("approved item = %+v", got)
	}
	assertStatusIndexConsistent(t, s)
}
