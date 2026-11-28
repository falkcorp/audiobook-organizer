// file: core/backup/journal/restorereport_ack_test.go
// version: 0.1.0
// guid: 89dac62b-341e-48f9-8583-895ebaaa25c3
// last-edited: 2026-11-21

package journal

import (
	"bytes"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// Batman names only. F4-08 B10i: ReviewAcks clears one account of the stored
// report's review list at a time.

var hookRun = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

// seedReport records rep as a hook run's report.
func seedReport(t *testing.T, fx *fixture, rep RestoreReport) {
	t.Helper()
	if err := PutRestoreReport(t.Context(), fx.ks, rep, hookRun); err != nil {
		t.Fatal(err)
	}
}

// recorded is whether c/backup/restore_report holds a record at all (not
// whether it holds an empty one: ReadRestoreReport cannot tell the two apart).
func recorded(t *testing.T, fx *fixture) bool {
	t.Helper()
	_, err := fx.ks.Get(t.Context(), []byte(restoreReportKey))
	switch {
	case err == nil:
		return true
	case errors.Is(err, storage.ErrNotFound):
		return false
	}
	t.Fatal(err)
	return false
}

// TestReviewAcks_RemovesOnlyTheNamedAccount: acknowledging one of two listed
// accounts leaves the other, the Kept list and the hook run's time as they were.
func TestReviewAcks_RemovesOnlyTheNamedAccount(t *testing.T) {
	fx := newFixture(t)
	seedReport(t, fx, RestoreReport{Kept: []string{"u-oracle"}, Review: []string{"u-nightwing", "u-robin"}})
	acks := NewReviewAcks(fx.ks)

	if err := acks.Acknowledge(t.Context(), "u-robin", nil); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRestoreReport(t.Context(), fx.ks)
	if err != nil || !slices.Equal(got.Review, []string{"u-nightwing"}) || !slices.Equal(got.Kept, []string{"u-oracle"}) {
		t.Fatalf("report after acknowledging u-robin = %+v, %v, want review [u-nightwing], kept [u-oracle]", got, err)
	}
	raw, err := fx.ks.Get(t.Context(), []byte(restoreReportKey))
	if err != nil {
		t.Fatal(err)
	}
	if want := hookRun.Format(time.RFC3339); !bytes.Contains(raw, []byte(want)) {
		t.Errorf("the record lost its hook run's time %s: %s", want, raw)
	}
}

// TestReviewAcks_TheLastAccountDeletesTheRecord: the report left with nobody on
// either list is deleted, as PutRestoreReport's replace rule says; one that
// still holds a Kept account stays, so restore_last_admin_kept does not clear.
func TestReviewAcks_TheLastAccountDeletesTheRecord(t *testing.T) {
	t.Run("nobody left", func(t *testing.T) {
		fx := newFixture(t)
		seedReport(t, fx, RestoreReport{Review: []string{"u-robin"}})
		if err := NewReviewAcks(fx.ks).Acknowledge(t.Context(), "u-robin", nil); err != nil {
			t.Fatal(err)
		}
		if recorded(t, fx) {
			t.Fatal("the report is still recorded with its last account acknowledged")
		}
	})
	t.Run("a kept account stays", func(t *testing.T) {
		fx := newFixture(t)
		seedReport(t, fx, RestoreReport{Kept: []string{"u-oracle"}, Review: []string{"u-robin"}})
		if err := NewReviewAcks(fx.ks).Acknowledge(t.Context(), "u-robin", nil); err != nil {
			t.Fatal(err)
		}
		got, err := ReadRestoreReport(t.Context(), fx.ks)
		if err != nil || len(got.Review) != 0 || !slices.Equal(got.Kept, []string{"u-oracle"}) {
			t.Fatalf("report = %+v, %v, want only kept [u-oracle]", got, err)
		}
	})
}

// TestReviewAcks_AnAccountNotListedIsNotInReview: no report, an account that
// was never listed, one already acknowledged and one only on the Kept list are
// ErrNotInReview, and stage is not called for any of them.
func TestReviewAcks_AnAccountNotListedIsNotInReview(t *testing.T) {
	fx := newFixture(t)
	acks := NewReviewAcks(fx.ks)
	staged := 0
	stage := func(*storage.Batch) error { staged++; return nil }

	if err := acks.Acknowledge(t.Context(), "u-robin", stage); !errors.Is(err, ErrNotInReview) {
		t.Fatalf("no report: %v, want ErrNotInReview", err)
	}
	seedReport(t, fx, RestoreReport{Kept: []string{"u-oracle"}, Review: []string{"u-robin", "u-nightwing"}})
	if err := acks.Acknowledge(t.Context(), "u-joker", stage); !errors.Is(err, ErrNotInReview) {
		t.Fatalf("an account never listed: %v, want ErrNotInReview", err)
	}
	if err := acks.Acknowledge(t.Context(), "u-oracle", stage); !errors.Is(err, ErrNotInReview) {
		t.Fatalf("an account only on the Kept list: %v, want ErrNotInReview", err)
	}
	if err := acks.Acknowledge(t.Context(), "u-robin", nil); err != nil {
		t.Fatal(err)
	}
	if err := acks.Acknowledge(t.Context(), "u-robin", stage); !errors.Is(err, ErrNotInReview) {
		t.Fatalf("a second acknowledgement: %v, want ErrNotInReview", err)
	}
	if staged != 0 {
		t.Errorf("stage ran %d times for an account that is not listed", staged)
	}
	got, _ := ReadRestoreReport(t.Context(), fx.ks)
	if !slices.Equal(got.Review, []string{"u-nightwing"}) || !slices.Equal(got.Kept, []string{"u-oracle"}) {
		t.Errorf("a refused acknowledgement changed the report: %+v", got)
	}
}

// TestReviewAcks_AReportThatCannotBeReadIsNotAnUnlistedAccount: a record that
// does not decode is an error that is not ErrNotInReview, and it is left as it
// was.
func TestReviewAcks_AReportThatCannotBeReadIsNotAnUnlistedAccount(t *testing.T) {
	fx := newFixture(t)
	if err := fx.ks.Set(t.Context(), []byte(restoreReportKey), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	err := NewReviewAcks(fx.ks).Acknowledge(t.Context(), "u-robin", nil)
	if err == nil || errors.Is(err, ErrNotInReview) {
		t.Fatalf("err = %v, want a read error that is not ErrNotInReview", err)
	}
	raw, gerr := fx.ks.Get(t.Context(), []byte(restoreReportKey))
	if gerr != nil || string(raw) != "not json" {
		t.Errorf("the unreadable record was changed: %q, %v", raw, gerr)
	}
}

// TestReviewAcks_AFailedStageDiscardsTheRemoval: what stage stages commits with
// the removal or not at all, so an audit record that cannot be staged leaves the
// account listed.
func TestReviewAcks_AFailedStageDiscardsTheRemoval(t *testing.T) {
	fx := newFixture(t)
	seedReport(t, fx, RestoreReport{Review: []string{"u-robin"}})
	boom := errors.New("the audit store is full")
	err := NewReviewAcks(fx.ks).Acknowledge(t.Context(), "u-robin", func(*storage.Batch) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the stage error", err)
	}
	got, _ := ReadRestoreReport(t.Context(), fx.ks)
	if !slices.Equal(got.Review, []string{"u-robin"}) {
		t.Fatalf("the removal committed although stage failed: %+v", got)
	}
}

// TestReviewAcks_TwoAcknowledgementsDoNotLoseEachOther: the record is a plain
// write, so nothing at commit would stop the second of two acknowledgements
// writing it back with the first's account on it. The first holds its read to
// its commit, so the second cannot even read until it is done: the second's
// stage, which runs after its read, must not run inside the first's window, and
// both accounts end up cleared.
func TestReviewAcks_TwoAcknowledgementsDoNotLoseEachOther(t *testing.T) {
	fx := newFixture(t)
	seedReport(t, fx, RestoreReport{Review: []string{"u-robin", "u-nightwing"}})
	acks := NewReviewAcks(fx.ks)

	var secondRead atomic.Bool
	second := make(chan error, 1)
	acks.beforeCommit = func() {
		acks.beforeCommit = nil // only the first acknowledgement's window
		go func() {
			second <- acks.Acknowledge(t.Context(), "u-nightwing", func(*storage.Batch) error { secondRead.Store(true); return nil })
		}()
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) && !secondRead.Load() {
			time.Sleep(time.Millisecond)
		}
		if secondRead.Load() {
			t.Error("a second acknowledgement read the report inside the first's read-to-commit window")
		}
	}
	if err := acks.Acknowledge(t.Context(), "u-robin", nil); err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatalf("the second acknowledgement: %v", err)
	}
	if recorded(t, fx) {
		got, _ := ReadRestoreReport(t.Context(), fx.ks)
		t.Fatalf("an account came back after both were acknowledged: %+v", got)
	}
}
