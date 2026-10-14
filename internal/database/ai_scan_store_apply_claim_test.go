// file: internal/database/ai_scan_store_apply_claim_test.go
// version: 1.1.0
// guid: 5b0e7c2a-91d4-4f68-a3c5-e8172d4b9a60
// last-edited: 2026-10-14

package database

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func completeScan(t *testing.T, s *AIScanStore) *Scan {
	t.Helper()
	scan, err := s.CreateScan("batch", nil, 0)
	require.NoError(t, err)
	require.NoError(t, s.UpdateScanStatus(scan.ID, "complete"))
	return scan
}

// backdateClaim ages a scan's claim as if its run died that long ago.
func backdateClaim(t *testing.T, s *AIScanStore, scanID int, age time.Duration) {
	t.Helper()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	scan, err := s.GetScan(scanID)
	require.NoError(t, err)
	at := time.Now().Add(-age)
	scan.ApplyStartedAt = &at
	require.NoError(t, s.writeScanLocked(scan))
}

func TestClaimScanForApply_AClaimedScanIsNotSupersededUntilItIsReleased(t *testing.T) {
	s := newGuardStore(t)
	scan := completeScan(t, s)
	require.NoError(t, s.ClaimScanForApply(scan.ID, "op-1"))

	ok, err := s.SupersedeIfUnapplied(scan.ID, scan.ID+100)
	require.NoError(t, err)
	require.False(t, ok, "a scan a run holds is treated like one with an applied result")
	got, err := s.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "complete", got.Status)

	require.NoError(t, s.ReleaseScanApply(scan.ID, "op-1"))
	got, err = s.GetScan(scan.ID)
	require.NoError(t, err)
	require.Empty(t, got.ApplyingOpID)
	require.Nil(t, got.ApplyStartedAt)

	ok, err = s.SupersedeIfUnapplied(scan.ID, scan.ID+100)
	require.NoError(t, err)
	require.True(t, ok, "released, and nothing applied: it can be superseded")
}

func TestClaimScanForApply_AStaleClaimFromADeadRunDoesNotHoldTheScan(t *testing.T) {
	s := newGuardStore(t)
	scan := completeScan(t, s)
	require.NoError(t, s.ClaimScanForApply(scan.ID, "op-dead"))
	backdateClaim(t, s, scan.ID, AIScanApplyClaimTTL+time.Minute)

	ok, err := s.SupersedeIfUnapplied(scan.ID, scan.ID+100)
	require.NoError(t, err)
	require.True(t, ok, "a claim older than the TTL is a crashed run's, and holds nothing")
}

func TestClaimScanForApply_ARunCannotClaimWhatAnotherHoldsButCanTakeAStaleClaim(t *testing.T) {
	s := newGuardStore(t)
	scan := completeScan(t, s)
	require.NoError(t, s.ClaimScanForApply(scan.ID, "op-1"))

	err := s.ClaimScanForApply(scan.ID, "op-2")
	var busy *ScanApplyBusyError
	require.True(t, errors.As(err, &busy), "got %v", err)
	require.Equal(t, "op-1", busy.OpID)
	require.NoError(t, s.ClaimScanForApply(scan.ID, "op-1"), "the holder re-claiming refreshes its claim")

	// op-2 releasing a claim it does not hold leaves op-1's alone.
	require.NoError(t, s.ReleaseScanApply(scan.ID, "op-2"))
	got, err := s.GetScan(scan.ID)
	require.NoError(t, err)
	require.Equal(t, "op-1", got.ApplyingOpID)

	backdateClaim(t, s, scan.ID, AIScanApplyClaimTTL+time.Minute)
	require.NoError(t, s.ClaimScanForApply(scan.ID, "op-2"), "a dead run's claim does not block a new run")
}

func TestClaimScanForApply_RefusesAScanThatIsSupersededOrNotComplete(t *testing.T) {
	s := newGuardStore(t)
	scan, err := s.CreateScan("batch", nil, 0)
	require.NoError(t, err)
	require.Error(t, s.ClaimScanForApply(scan.ID, "op-1"), "a pending scan has no results to apply")

	require.NoError(t, s.UpdateScanStatus(scan.ID, "complete"))
	ok, err := s.SupersedeIfUnapplied(scan.ID, scan.ID+100)
	require.NoError(t, err)
	require.True(t, ok)
	err = s.ClaimScanForApply(scan.ID, "op-1")
	var sup *ScanSupersededError
	require.True(t, errors.As(err, &sup), "got %v", err)
}

// A claim and a supersede race: exactly one wins, and a claim that wins is never
// followed by a supersede.
func TestClaimScanForApply_ClaimAndSupersedeAreExclusive(t *testing.T) {
	s := newGuardStore(t)
	for range 20 {
		scan := completeScan(t, s)
		var wg sync.WaitGroup
		var claimErr error
		var superseded bool
		wg.Add(2)
		go func() { defer wg.Done(); claimErr = s.ClaimScanForApply(scan.ID, "op-1") }()
		go func() { defer wg.Done(); superseded, _ = s.SupersedeIfUnapplied(scan.ID, scan.ID+1000) }()
		wg.Wait()
		got, err := s.GetScan(scan.ID)
		require.NoError(t, err)
		if superseded {
			require.Equal(t, "superseded", got.Status)
			var sup *ScanSupersededError
			require.True(t, errors.As(claimErr, &sup), "a superseded scan cannot be claimed: %v", claimErr)
		} else {
			require.NoError(t, claimErr)
			require.Equal(t, "op-1", got.ApplyingOpID)
			require.Equal(t, "complete", got.Status)
		}
	}
}

// DeleteScan takes the apply lock, so it cannot interleave with a result write
// that would resurrect a row it deleted.
func TestDeleteScan_HoldsTheApplyLock(t *testing.T) {
	s := newGuardStore(t)
	scan := completeScan(t, s)
	s.applyMu.Lock()
	done := make(chan error, 1)
	go func() { done <- s.DeleteScan(scan.ID) }()
	select {
	case <-done:
		s.applyMu.Unlock()
		t.Fatal("DeleteScan ran while the apply lock was held")
	case <-time.After(150 * time.Millisecond):
	}
	s.applyMu.Unlock()
	require.NoError(t, <-done)
	got, err := s.GetScan(scan.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}

// A claim whose operation is no longer running (the registry says so) holds
// nothing: it no longer blocks another apply or a supersede for the rest of its
// three-hour TTL. A live run's claim still does, and an unattributed claim (no
// operation id, nothing to ask) is judged by the TTL alone.
func TestClaimScanForApply_AClaimOfARunThatIsNotRunningDoesNotHoldTheScan(t *testing.T) {
	s := newGuardStore(t)
	running := map[string]bool{"op-live": true}
	s.SetApplyRunLiveness(func(opID string) bool { return running[opID] })

	dead := completeScan(t, s)
	require.NoError(t, s.ClaimScanForApply(dead.ID, "op-dead"))
	require.NoError(t, s.ClaimScanForApply(dead.ID, "op-next"), "a dead run's claim does not make the scan busy")
	require.NoError(t, s.ReleaseScanApply(dead.ID, "op-next"))
	require.NoError(t, s.ClaimScanForApply(dead.ID, "op-dead"))
	ok, err := s.SupersedeIfUnapplied(dead.ID, dead.ID+100)
	require.NoError(t, err)
	require.True(t, ok, "a dead run's claim does not hold off a supersede")

	live := completeScan(t, s)
	require.NoError(t, s.ClaimScanForApply(live.ID, "op-live"))
	var busy *ScanApplyBusyError
	require.ErrorAs(t, s.ClaimScanForApply(live.ID, "op-other"), &busy)

	anon := completeScan(t, s)
	require.NoError(t, s.ClaimScanForApply(anon.ID, UnattributedApplyClaimPrefix+"x"))
	require.ErrorAs(t, s.ClaimScanForApply(anon.ID, "op-other"), &busy, "an unattributed claim is held until its TTL")
}

// The startup sweep drops the claims that no longer hold and leaves the rest.
func TestClearDeadApplyClaims_DropsOnlyTheClaimsThatNoLongerHold(t *testing.T) {
	s := newGuardStore(t)
	running := map[string]bool{"op-live": true}
	s.SetApplyRunLiveness(func(opID string) bool { return running[opID] })
	dead, live, anon, expired := completeScan(t, s), completeScan(t, s), completeScan(t, s), completeScan(t, s)
	require.NoError(t, s.ClaimScanForApply(dead.ID, "op-dead"))
	require.NoError(t, s.ClaimScanForApply(live.ID, "op-live"))
	require.NoError(t, s.ClaimScanForApply(anon.ID, UnattributedApplyClaimPrefix+"y"))
	require.NoError(t, s.ClaimScanForApply(expired.ID, UnattributedApplyClaimPrefix+"z"))
	backdateClaim(t, s, expired.ID, AIScanApplyClaimTTL+time.Minute)

	n, err := s.ClearDeadApplyClaims()
	require.NoError(t, err)
	require.Equal(t, 2, n)
	for _, c := range []struct {
		id   int
		held bool
	}{{dead.ID, false}, {live.ID, true}, {anon.ID, true}, {expired.ID, false}} {
		got, err := s.GetScan(c.id)
		require.NoError(t, err)
		require.Equal(t, c.held, got.ApplyingOpID != "", "scan %d claim kept", c.id)
	}
}
