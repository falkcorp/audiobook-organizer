// file: internal/database/review_claim_test.go
// version: 1.0.0
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

func TestClaimReviewItemForApply_ClaimReleaseKeepsIndexConsistent(t *testing.T) {
	s := newReviewTestStore(t)
	it := seedClaimItem(t, s, "k1", ReviewStatusApproved)

	got, err := s.ClaimReviewItemForApply(it.ID, time.Minute)
	if err != nil || got == nil || got.Status != ReviewStatusApplying || got.ApplyClaimedFrom != ReviewStatusApproved {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	if countStatus(t, s, ReviewStatusApplying) != 1 || countStatus(t, s, ReviewStatusApproved) != 0 {
		t.Fatal("status index not moved by the claim")
	}
	var conflict *ReviewClaimConflictError
	if _, err := s.ClaimReviewItemForApply(it.ID, time.Minute); !errors.As(err, &conflict) || conflict.Status != ReviewStatusApplying {
		t.Fatalf("second claim err = %v, want a conflict on applying", err)
	}

	if err := s.ReleaseReviewItemClaim(it.ID); err != nil {
		t.Fatal(err)
	}
	back, _ := s.GetReviewItem(it.ID)
	if back.Status != ReviewStatusApproved || back.ApplyClaimedFrom != "" || back.ChosenAction != "combine" {
		t.Fatalf("after release = %+v", back)
	}
	if countStatus(t, s, ReviewStatusApplying) != 0 || countStatus(t, s, ReviewStatusApproved) != 1 {
		t.Fatal("status index not restored by the release")
	}
	// Finishing the apply clears the claim marker and lands on applied.
	if _, err := s.ClaimReviewItemForApply(it.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	done, _ := s.SetReviewItemDecision(it.ID, ReviewStatusApplied, "combine")
	if done.Status != ReviewStatusApplied || done.ApplyClaimedFrom != "" {
		t.Fatalf("after finish = %+v", done)
	}
	if _, err := s.ClaimReviewItemForApply(it.ID, time.Minute); !errors.As(err, &conflict) || conflict.Status != ReviewStatusApplied {
		t.Fatalf("claim of applied err = %v", err)
	}
}

func TestClaimReviewItemForApply_RejectedIsClaimableMissingIsNil(t *testing.T) {
	s := newReviewTestStore(t)
	rej := seedClaimItem(t, s, "k1", ReviewStatusRejected)
	got, err := s.ClaimReviewItemForApply(rej.ID, time.Minute)
	if err != nil || got.ApplyClaimedFrom != ReviewStatusRejected {
		t.Fatalf("claim of rejected = %+v, %v; want it claimable (re-deciding is allowed)", got, err)
	}
	if err := s.ReleaseReviewItemClaim(rej.ID); err != nil {
		t.Fatal(err)
	}
	if back, _ := s.GetReviewItem(rej.ID); back.Status != ReviewStatusRejected {
		t.Fatalf("release returned it to %q, want rejected", back.Status)
	}
	if got, err := s.ClaimReviewItemForApply("nope", time.Minute); got != nil || err != nil {
		t.Fatalf("claim of missing = %v, %v", got, err)
	}
}

// A claim left behind by a crash must not block the item forever: past staleAfter
// the next claim takes it over and keeps the ORIGINAL prior status.
func TestClaimReviewItemForApply_StaleClaimIsReclaimable(t *testing.T) {
	s := newReviewTestStore(t)
	it := seedClaimItem(t, s, "k1", ReviewStatusPending)
	if _, err := s.ClaimReviewItemForApply(it.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	var conflict *ReviewClaimConflictError
	if _, err := s.ClaimReviewItemForApply(it.ID, time.Hour); !errors.As(err, &conflict) {
		t.Fatalf("fresh claim must hold, err = %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	got, err := s.ClaimReviewItemForApply(it.ID, time.Millisecond)
	if err != nil || got.Status != ReviewStatusApplying || got.ApplyClaimedFrom != ReviewStatusPending {
		t.Fatalf("takeover = %+v, %v; want applying claimed from pending", got, err)
	}
	if countStatus(t, s, ReviewStatusApplying) != 1 || countStatus(t, s, ReviewStatusPending) != 0 {
		t.Fatal("index inconsistent after takeover")
	}
}

func TestClaimReviewItemForApply_ConcurrentClaimsHaveOneWinner(t *testing.T) {
	s := newReviewTestStore(t)
	it := seedClaimItem(t, s, "k1", ReviewStatusApproved)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if got, err := s.ClaimReviewItemForApply(it.ID, time.Minute); err == nil && got != nil {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims won, want exactly 1", wins.Load())
	}
}
