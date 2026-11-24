// file: internal/plugins/maintenance/scan_standdown_native_test.go
// version: 0.1.0
// guid: c41e7a96-2d58-4b03-9f6e-8a15d7b3e024
// last-edited: 2026-11-22

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// nativeScanStandDown turns a failed hold renewal into the heartbeat's "lost", and
// says which failure it was: the lease lapsed (the executor's tasks.ErrHoldLost, the
// scanner may have resumed), or the marker that records the lease could not be
// written (any other error, the lease itself still the run's). The t6g4 hold fake's
// Renew never failed, so neither path ran before.
func TestNativeScanStandDown_RenewalFailuresAreToldApart(t *testing.T) {
	lapsed := fmt.Errorf("%w: the lease on %s lapsed", tasks.ErrHoldLost, t6g4ScanKey)
	diskFull := errors.New("pebble: disk full")
	for _, c := range []struct {
		name     string
		renewErr error
		wantLoss error
		wantText string
		lapse    bool // whether the loss is the lease's
	}{
		{"the lease lapsed", lapsed, lapsed, "scan stand-down lease lapsed", true},
		{"the marker write failed", diskFull, diskFull, "scan stand-down marker could not be written (pebble: disk full)", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, rc := newT6G4RC(fsRegroupKind, tasks.ModeLive, &t6g4RunWriter{})
			n := &nativeScanStandDown{rc: rc, key: t6g4ScanKey}
			rel, err := n.AcquireScanStandDown(context.Background(), "op-1", "regroup")
			if err != nil {
				t.Fatal(err)
			}
			defer rel()

			if !n.RenewScanStandDown("op-1") || !n.ScanStandDownValid("op-1") || n.ScanStandDownLoss() != nil {
				t.Fatal("a healthy hold reports lost")
			}
			if scanStandDownLostForApply(n, "op-1", true) {
				t.Fatal("the apply helper reports a healthy hold lost")
			}

			h.failRenewals(c.renewErr)
			if !scanStandDownLostForApply(n, "op-1", true) {
				t.Fatal("the apply helper does not report the failed renewal as lost")
			}
			if n.ScanStandDownValid("op-1") {
				t.Error("the hold is still valid after a failed renewal")
			}
			if got := n.ScanStandDownLoss(); !errors.Is(got, c.wantLoss) {
				t.Errorf("ScanStandDownLoss = %v, want %v", got, c.wantLoss)
			}
			if got := scanStandDownLossText(n); !strings.Contains(got, c.wantText) {
				t.Errorf("loss text = %q, want it to contain %q", got, c.wantText)
			}
			if errors.Is(n.ScanStandDownLoss(), tasks.ErrHoldLost) != c.lapse {
				t.Errorf("the loss is reported as a lapse = %v, want %v", !c.lapse, c.lapse)
			}
		})
	}
}

// The text of a controller that cannot say why (the registry's) is a lapse, which
// is the only way it loses a hold.
func TestScanStandDownLossText_AControllerWithNoReasonReportsALapse(t *testing.T) {
	if got := scanStandDownLossText(scanLeaseOnly{}); got != "scan stand-down lease lapsed" {
		t.Errorf("loss text = %q, want the lapse text", got)
	}
}

// scanLeaseOnly is a ScanController with no ScanStandDownLoss.
type scanLeaseOnly struct{ ScanController }
